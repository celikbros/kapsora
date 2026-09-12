package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	billingapp "github.com/celikbros/kapsora/internal/billing/application"
	billingdomain "github.com/celikbros/kapsora/internal/billing/domain"
	claimapp "github.com/celikbros/kapsora/internal/claim/application"
	documentapp "github.com/celikbros/kapsora/internal/document/application"
	documentdomain "github.com/celikbros/kapsora/internal/document/domain"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// The money half of the demo world: the claims a provider may bill, the invoices it raises, the
// icmals it sends, the decisions the payer takes and the settlements that follow.
//
// Every state the six scenarios open on is produced here, and each is produced by the command
// that produces it in production — an icmal reaches UNDER_REVIEW because somebody submitted it,
// a settlement reaches PENDING_APPROVAL because the batch's own `batch.decided` event was
// handed to the handler the worker subscribes with. Nothing below sets a status.

// The invoice numbers the demo world uses. They are the natural key every step here is
// idempotent on: an invoice that is already there is found by its number and left alone.
const (
	invoiceMismatched = "DEMO-0001" // scenario 1: the draft whose allocation does not add up
	invoiceForDraft   = "DEMO-0002" // scenario 1: the submitted invoice in the draft icmal
	invoiceUndecided  = "DEMO-0003" // scenario 2: the one still waiting for a decision
	invoiceDecidedA   = "DEMO-0004" // scenario 2: already approved by the reviewer
	invoiceDecidedB   = "DEMO-0005" // scenario 2: already approved by the reviewer
	invoiceSettling   = "DEMO-0006" // scenario 3: the icmal whose settlement waits for approval
	invoicePaid       = "DEMO-0007" // scenario 6: the icmal whose settlement was paid
	invoiceApproved   = "DEMO-0008" // scenario 6: the settlement approved and waiting for the bank
)

// The amounts. They are round so that the 500 lira cut of scenario 2 is arithmetic anybody
// watching can follow, and every total stays under the tenant's decision threshold (100 000) and
// settlement threshold (50 000) so that no scenario stalls on a gate the guide never mentions.
const (
	amountMismatched = "2500"
	amountDraft      = "1500"
	amountUndecided  = "4000"
	amountDecidedA   = "1500"
	amountDecidedB   = "2500"
	amountSettling   = "2500"
	amountPaid       = "1500"
	amountApproved   = "2500"
)

// ensureBillingStory builds every invoice, icmal and settlement the guide opens on.
//
// The order matters and it is the order of the guide: the provider's side first (an unsendable
// draft and a sendable icmal), then the payer's (an icmal under review with one invoice left,
// an icmal decided with a settlement waiting), then the one that is already finished and paid,
// which is what gives the reconciliation of scenario 6 a day that balanced.
func (s *seeder) ensureBillingStory(ctx context.Context, sc *scenario) error {
	today := s.today()
	// The icmals are anchored to the month rather than to the day this runs, so a second run on
	// another day finds the icmals the first one opened instead of opening a month of its own.
	month := monthStart(today)
	if err := s.ensureDemoHealthCase(ctx, sc); err != nil {
		return err
	}

	// Scenario 1. The draft whose allocation is 300 short of its header, so "Gönder" is
	// refused and says why; and the submitted invoice sitting in a draft icmal, so the next
	// step of the same scenario has something to send.
	s.clock.at = today
	if err := s.ensureDraftInvoiceWithGap(ctx, sc); err != nil {
		return err
	}
	if err := s.ensureDraftBatch(ctx, sc, month); err != nil {
		return err
	}

	// Scenario 2. An icmal already under review: three invoices, two of them answered by the
	// financial reviewer and one still saying "Karar bekliyor".
	s.clock.at = today.AddDate(0, 0, -5)
	if err := s.ensureBatchUnderReview(ctx, sc, month.AddDate(0, -1, 0)); err != nil {
		return err
	}

	// Scenario 3. An icmal decided a month ago, whose settlement therefore falls due today and
	// is still waiting for the approver.
	s.clock.at = today.AddDate(0, 0, -30)
	if err := s.ensureSettledBatch(ctx, sc, settledBatchSpec{
		Period: month.AddDate(0, -2, 0), Invoice: invoiceSettling, Amount: amountSettling,
	}); err != nil {
		return err
	}

	// Scenario 6. An icmal decided five weeks ago, approved and paid, whose settlement fell due
	// a week ago and balanced. It is what makes one reconciliation day BALANCED beside the one
	// that has a difference on it.
	s.clock.at = today.AddDate(0, 0, -37)
	if err := s.ensureSettledBatch(ctx, sc, settledBatchSpec{
		Period: month.AddDate(0, -3, 0), Invoice: invoicePaid, Amount: amountPaid,
		Approve: true, Pay: true,
	}); err != nil {
		return err
	}

	// And one more that the approver has released and the bank has not yet paid, due in three
	// days. It is what puts a figure on the "ödenecek" row of the operations dashboard scenario
	// 6 opens on: that row counts APPROVED, POSTED and PARTIALLY_PAID and nothing else, so a
	// world holding only a settlement waiting for approval and one already paid would show a
	// dashboard of zeroes.
	s.clock.at = today.AddDate(0, 0, -27)
	if err := s.ensureSettledBatch(ctx, sc, settledBatchSpec{
		Period: today.AddDate(0, -4, 0), Invoice: invoiceApproved, Amount: amountApproved,
		Approve: true,
	}); err != nil {
		return err
	}

	// One approved claim nobody has billed, so the provider's earnings screen — the first step
	// of scenario 1 — has a figure on it however much of the rest has been invoiced.
	//
	// "Is there anything left to bill" is its own natural key, and it is read through the same
	// figure the provider's screen shows: a claim already allocated to a draft invoice is still
	// APPROVED but is no longer invoiceable, so counting statuses here would leave the screen
	// at zero while this step believed it had done its job.
	s.clock.at = today
	invoiceable, err := s.invoiceableTotal(ctx, sc)
	if err != nil {
		return err
	}
	if invoiceable == "" || invoiceable == "0" {
		if _, err := s.ensureApprovedClaim(ctx, sc, serviceGPVisit, "1", "earnings"); err != nil {
			return err
		}
	}
	s.clock.at = time.Now().UTC()
	step("billing", "invoices and icmals", "ready")
	return nil
}

// invoiceableTotal is what the provider's earnings screen shows as still billable, in the
// tenant's own currency. It is the figure scenario 1 opens on.
func (s *seeder) invoiceableTotal(ctx context.Context, sc *scenario) (string, error) {
	earnings, err := s.biz.claims.ProviderEarnings(ctx,
		rcOrganization(sc.tenant, sc.billing, sc.hospitalOrg, claimapp.PermissionRead),
		sc.hospitalOrg, claimapp.EarningsFilter{CurrencyCode: "TRY"})
	if err != nil {
		return "", fmt.Errorf("read what the provider may still bill: %w", err)
	}
	for _, bucket := range earnings.Currencies {
		if bucket.CurrencyCode == "TRY" {
			return bucket.InvoiceableTotal, nil
		}
	}
	return "", nil
}

// ensureDemoHealthCase opens the episode of care the claims hang off, so the case screens of the
// back office and the provider portal are about something.
func (s *seeder) ensureDemoHealthCase(ctx context.Context, sc *scenario) error {
	rc := rcOrganization(sc.tenant, sc.billing, sc.hospitalOrg,
		healthapp.PermissionCaseRead, healthapp.PermissionCaseManage,
		healthapp.PermissionClinicalRead)
	page, err := s.biz.health.ListCases(ctx, rc, healthapp.ListFilter{Limit: 50},
		healthapp.AccessRequest{FinancialOnly: true})
	if err != nil {
		return fmt.Errorf("list health cases: %w", err)
	}
	if len(page.Items) > 0 {
		sc.healthCase = page.Items[0].Case.ID
		return nil
	}
	provider := sc.hospitalOrg
	opened := s.clock.at.AddDate(0, 0, -40)
	view, err := s.biz.health.CreateCase(ctx, rc, healthapp.NewCaseInput{
		PersonID: sc.person, EnrollmentID: sc.enrollment, CaseType: "OUTPATIENT",
		ProviderOrganizationID: &provider, OpenedAt: &opened,
	})
	if err != nil {
		return fmt.Errorf("open the demo health case: %w", err)
	}
	sc.healthCase = view.Case.ID
	step("health", "demo case", "opened")
	return nil
}

// ensureApprovedClaim raises one claim through the real pipeline and returns it once the pipeline
// has decided it.
//
// Nothing here approves anything: the claim is priced against the published tariff, no rule
// objects, no approval policy asks for a person, and it comes out of `Submit` APPROVED. A claim
// this seed could not get approved that way would be a claim the demo world should not contain.
func (s *seeder) ensureApprovedClaim(ctx context.Context, sc *scenario,
	serviceCode, quantity, purpose string,
) (uuid.UUID, error) {
	rc := rcOrganization(sc.tenant, sc.billing, sc.hospitalOrg,
		claimapp.PermissionRead, claimapp.PermissionCreate, claimapp.PermissionSubmit)
	serviceDay := s.nextServiceDay()
	unit := map[string]string{
		serviceGPVisit: "COUNT", servicePhysio: "SESSION", serviceMRI: "COUNT",
	}[serviceCode]
	amount := map[string]string{
		serviceGPVisit: priceGPVisit, servicePhysio: pricePhysioUnit, serviceMRI: priceMRI,
	}[serviceCode]
	description := "Demo veri: " + purpose

	draft, err := s.biz.claims.CreateClaim(ctx, rc, claimapp.NewClaimInput{
		PersonID: sc.person, ProgramID: sc.program, EnrollmentID: sc.enrollment,
		ProviderOrganizationID: sc.hospitalOrg, CaseID: caseOrNil(sc.healthCase),
		ServiceDateFrom: serviceDay, ServiceDateTo: serviceDay, Channel: "PROVIDER_PORTAL",
		Lines: []claimapp.NewLineInput{{
			LineNo: 1, ServiceDefinitionID: sc.services[serviceCode], UnitType: unit,
			Quantity: quantity, LineAmount: amount, Description: &description,
		}},
	}, claimapp.AccessRequest{FinancialOnly: true})
	if err != nil {
		return uuid.Nil, fmt.Errorf("raise a %s claim: %w", serviceCode, err)
	}
	submitted, err := s.biz.claims.Submit(ctx, rc, draft.Claim.ID, draft.Claim.RowVersion,
		claimapp.AccessRequest{FinancialOnly: true})
	if err != nil {
		return uuid.Nil, fmt.Errorf("submit a %s claim: %w", serviceCode, err)
	}
	if submitted.Claim.Status != "APPROVED" {
		return uuid.Nil, fmt.Errorf(
			"a %s claim came out of the pipeline %s (%s); the demo world would contain a claim "+
				"nobody can bill", serviceCode, submitted.Claim.Status,
			claimExceptions(submitted))
	}
	return submitted.Claim.ID, nil
}

// nextServiceDay hands out a service date no other claim of this run has used.
//
// The duplicate check of WP-I5-04 looks for the same person, the same provider and the same
// service on the same day, so a world whose claims were all dated one week ago would be a world
// where every claim after the first is routed to a reviewer as a suspected duplicate. The dates
// are counted back from wherever the seed's clock stands, and the counter restarts when the
// clock moves — so a claim is always a few days before the invoice that bills it, which is what
// a month of a hospital's work actually looks like.
func (s *seeder) nextServiceDay() time.Time {
	at := day(s.clock.at)
	if !at.Equal(s.claimEpoch) {
		s.claimEpoch, s.claimSeq = at, 0
	}
	s.claimSeq++
	return coveredDay(at.AddDate(0, 0, -s.claimSeq), s.claimSeq)
}

// coveredDay keeps a date inside the member's enrollment.
//
// The enrollment starts on the first day of the calendar year, so a seed run in the first weeks
// of January would otherwise date its claims into a year the member was not enrolled in — and
// every one of them would be refused as not eligible on the day. The nudge keeps the dates
// distinct, because two claims pushed onto the same floor would be a suspected duplicate.
func coveredDay(at time.Time, nudge int) time.Time {
	floor := startOfYear(time.Now()).AddDate(0, 0, nudge)
	if at.Before(floor) {
		return floor
	}
	return at
}

// claimExceptions renders what the pipeline objected to, so a seed that cannot get a claim
// approved says which check refused it rather than only that one did.
func claimExceptions(view claimapp.ClaimView) string {
	if len(view.Exceptions) == 0 {
		return "no exception recorded"
	}
	parts := make([]string, 0, len(view.Exceptions))
	for _, e := range view.Exceptions {
		parts = append(parts, fmt.Sprintf("line %d %s/%s: %s", e.LineNo, e.Code, e.Stage, e.Detail))
	}
	return strings.Join(parts, "; ")
}

func caseOrNil(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// findInvoice looks an invoice up by the number this file gave it. It is the natural key every
// step below is idempotent on.
func (s *seeder) findInvoice(ctx context.Context, sc *scenario, number string,
) (billingapp.InvoiceRecord, bool, error) {
	rc := rcOrganization(sc.tenant, sc.billing, sc.hospitalOrg, billingapp.PermissionRead)
	provider := sc.hospitalOrg
	page, err := s.biz.billing.ListInvoices(ctx, rc, billingapp.InvoiceFilter{
		ProviderOrganizationID: &provider, Limit: 200,
	})
	if err != nil {
		return billingapp.InvoiceRecord{}, false, fmt.Errorf("list invoices: %w", err)
	}
	for _, item := range page.Items {
		if item.InvoiceNumber == number {
			return item.InvoiceRecord, true, nil
		}
	}
	return billingapp.InvoiceRecord{}, false, nil
}

// ensureInvoice raises one invoice covering one freshly approved claim, and leaves it DRAFT.
//
// `allocated` may be less than `payable`, which is the whole point of the first invoice: the
// difference the provider's screen shows and the submit refuses on.
func (s *seeder) ensureInvoice(ctx context.Context, sc *scenario, number, payable, allocated,
	serviceCode, quantity string,
) (billingapp.InvoiceView, error) {
	rc := rcOrganization(sc.tenant, sc.billing, sc.hospitalOrg,
		billingapp.PermissionRead, billingapp.PermissionManage)
	existing, found, err := s.findInvoice(ctx, sc, number)
	if err != nil {
		return billingapp.InvoiceView{}, err
	}
	if found {
		return s.biz.billing.GetInvoice(ctx, rc, existing.ID)
	}

	claimID, err := s.ensureApprovedClaim(ctx, sc, serviceCode, quantity, number)
	if err != nil {
		return billingapp.InvoiceView{}, err
	}
	documentID, err := s.storeDemoDocument(ctx, sc.tenant, sc.hospitalOrg,
		"fatura-"+number+".pdf", documentdomain.ClassConfidential,
		"KAPSORA demo faturası "+number)
	if err != nil {
		return billingapp.InvoiceView{}, err
	}
	payer := sc.payerOrg
	view, err := s.biz.billing.CreateInvoice(ctx, rc, billingapp.CreateInvoiceInput{
		ProviderOrganizationID: sc.hospitalOrg, PayerOrganizationID: &payer,
		InvoiceNumber: number, InvoiceDate: day(s.clock.at), CurrencyCode: "TRY",
		LineExtensionAmount: payable, TaxAmount: "0", PayableAmount: payable,
		DomainCode: "HEALTH", DocumentID: &documentID,
	})
	if err != nil {
		return billingapp.InvoiceView{}, fmt.Errorf("raise invoice %s: %w", number, err)
	}
	allocatedView, err := s.biz.billing.PutAllocations(ctx, rc, view.Invoice.ID,
		[]billingapp.AllocationInput{{ClaimID: claimID, AllocatedAmount: allocated}},
		view.Invoice.RowVersion)
	if err != nil {
		return billingapp.InvoiceView{}, fmt.Errorf("allocate invoice %s: %w", number, err)
	}
	return allocatedView, nil
}

// ensureDraftInvoiceWithGap is the first step of scenario 1: a draft that bills 2 500 and covers
// 2 200 of a claim, so the difference on the screen is 300 and pressing "Gönder" is refused with
// a reason rather than accepted quietly.
func (s *seeder) ensureDraftInvoiceWithGap(ctx context.Context, sc *scenario) error {
	view, err := s.ensureInvoice(ctx, sc, invoiceMismatched, amountMismatched, "2200",
		serviceMRI, "1")
	if err != nil {
		return err
	}
	if view.Invoice.Status != billingdomain.StatusDraft {
		return fmt.Errorf("invoice %s is %s and should be a draft", invoiceMismatched,
			view.Invoice.Status)
	}
	step("invoice", invoiceMismatched, "draft, allocation short by "+view.AllocationDifference)
	return nil
}

// ensureSubmittedInvoice raises an invoice whose allocation matches its header and submits it,
// which is the state an icmal may carry one in.
func (s *seeder) ensureSubmittedInvoice(ctx context.Context, sc *scenario,
	number, amount, serviceCode, quantity string,
) (billingapp.InvoiceRecord, error) {
	rc := rcOrganization(sc.tenant, sc.billing, sc.hospitalOrg,
		billingapp.PermissionRead, billingapp.PermissionManage)
	view, err := s.ensureInvoice(ctx, sc, number, amount, amount, serviceCode, quantity)
	if err != nil {
		return billingapp.InvoiceRecord{}, err
	}
	if view.Invoice.Status != billingdomain.StatusDraft {
		return view.Invoice, nil
	}
	submitted, err := s.biz.billing.SubmitInvoice(ctx, rc, view.Invoice.ID, view.Invoice.RowVersion)
	if err != nil {
		return billingapp.InvoiceRecord{}, fmt.Errorf("submit invoice %s: %w", number, err)
	}
	return submitted.Invoice, nil
}

// monthStart is the first day of the month t falls in.
func monthStart(t time.Time) time.Time {
	t = day(t)
	return t.AddDate(0, 0, 1-t.Day())
}

// ensureBatch opens an icmal over one month and puts the given invoices in it, leaving it DRAFT.
func (s *seeder) ensureBatch(ctx context.Context, sc *scenario, periodFrom time.Time,
	invoiceIDs []uuid.UUID,
) (billingapp.BatchView, error) {
	rc := rcOrganization(sc.tenant, sc.billing, sc.hospitalOrg,
		billingapp.PermissionRead, billingapp.PermissionManage,
		billingapp.PermissionBatchCreate, billingapp.PermissionBatchSubmit)
	// An icmal is recognised by the invoices in it, not by the period it covers. The periods
	// move with the month the seed runs in, so a second run on another day would otherwise look
	// for this step's icmal where the previous run put a different one -- and submit the draft
	// icmal as if it were the one under review. An invoice lives in exactly one live icmal.
	if view, found, err := s.batchHolding(ctx, sc, invoiceIDs); err != nil {
		return billingapp.BatchView{}, err
	} else if found {
		return view, nil
	}
	payer := sc.payerOrg
	from := day(periodFrom)
	view, err := s.biz.billing.CreateBatch(ctx, rc, billingapp.CreateBatchInput{
		ProviderOrganizationID: sc.hospitalOrg, PayerOrganizationID: &payer,
		DomainCode: "HEALTH", CurrencyCode: "TRY",
		PeriodFrom: from, PeriodTo: from.AddDate(0, 1, -1),
	})
	if err != nil {
		return billingapp.BatchView{}, fmt.Errorf("open an icmal for %s: %w",
			from.Format(time.DateOnly), err)
	}
	filled, err := s.biz.billing.PutBatchInvoices(ctx, rc, view.Batch.ID, invoiceIDs,
		view.Batch.RowVersion)
	if err != nil {
		return billingapp.BatchView{}, fmt.Errorf("fill the icmal of %s: %w",
			from.Format(time.DateOnly), err)
	}
	return filled, nil
}

// batchHolding finds the icmal one of these invoices already sits in. An invoice may be in
// exactly one live icmal, so finding it is what lets a second run of the seed on another day
// reuse what the first run opened instead of opening a second icmal for the same invoice — the
// failure a demo database met the morning after it was seeded.
func (s *seeder) batchHolding(ctx context.Context, sc *scenario, invoiceIDs []uuid.UUID,
) (billingapp.BatchView, bool, error) {
	rc := rcOrganization(sc.tenant, sc.billing, sc.hospitalOrg, billingapp.PermissionRead)
	provider := sc.hospitalOrg
	page, err := s.biz.billing.ListBatches(ctx, rc, billingapp.BatchFilter{
		ProviderOrganizationID: &provider, Limit: 200,
	})
	if err != nil {
		return billingapp.BatchView{}, false, fmt.Errorf("list icmals: %w", err)
	}
	wanted := make(map[uuid.UUID]struct{}, len(invoiceIDs))
	for _, id := range invoiceIDs {
		wanted[id] = struct{}{}
	}
	for _, record := range page.Items {
		view, err := s.biz.billing.GetBatch(ctx, rc, record.ID)
		if err != nil {
			return billingapp.BatchView{}, false, fmt.Errorf("read an icmal: %w", err)
		}
		for _, member := range view.Invoices {
			if _, ok := wanted[member.InvoiceID]; ok {
				return view, true, nil
			}
		}
	}
	return billingapp.BatchView{}, false, nil
}

// ensureDraftBatch is the last step of scenario 1: an icmal still in draft, carrying one
// submitted invoice, so "İcmali gönder" has something to send.
func (s *seeder) ensureDraftBatch(ctx context.Context, sc *scenario, periodFrom time.Time) error {
	invoice, err := s.ensureSubmittedInvoice(ctx, sc, invoiceForDraft, amountDraft,
		serviceGPVisit, "1")
	if err != nil {
		return err
	}
	view, err := s.ensureBatch(ctx, sc, periodFrom, []uuid.UUID{invoice.ID})
	if err != nil {
		return err
	}
	step("icmal", periodFrom.Format("2006-01"), "draft, "+view.Batch.Status)
	return nil
}

// ensureBatchUnderReview is the state scenario 2 opens on: an icmal the provider has sent, that
// the financial reviewer has worked partway through. Two invoices are answered and the third is
// left alone — which is the "Karar bekliyor" row the guide asks the operator to cut by 500.
func (s *seeder) ensureBatchUnderReview(ctx context.Context, sc *scenario,
	periodFrom time.Time,
) error {
	undecided, err := s.ensureSubmittedInvoice(ctx, sc, invoiceUndecided, amountUndecided,
		servicePhysio, "10")
	if err != nil {
		return err
	}
	decidedA, err := s.ensureSubmittedInvoice(ctx, sc, invoiceDecidedA, amountDecidedA,
		serviceGPVisit, "1")
	if err != nil {
		return err
	}
	decidedB, err := s.ensureSubmittedInvoice(ctx, sc, invoiceDecidedB, amountDecidedB,
		serviceMRI, "1")
	if err != nil {
		return err
	}
	batch, err := s.ensureBatch(ctx, sc, periodFrom,
		[]uuid.UUID{undecided.ID, decidedA.ID, decidedB.ID})
	if err != nil {
		return err
	}
	batch, err = s.submitBatch(ctx, sc, batch)
	if err != nil {
		return err
	}
	// The two the reviewer has already answered. The third is deliberately not touched: a
	// scenario that opened on an icmal with nothing left to decide would be a scenario with
	// nothing to do.
	reviewer := rcTenant(sc.tenant, sc.reviewer, billingapp.PermissionRead,
		billingapp.PermissionBatchReview)
	for _, invoiceID := range []uuid.UUID{decidedA.ID, decidedB.ID} {
		if batchInvoiceDecided(batch, invoiceID) {
			continue
		}
		batch, err = s.biz.billing.ReviewBatchInvoice(ctx, reviewer, batch.Batch.ID, invoiceID,
			billingapp.ReviewBatchInvoiceInput{Decision: billingdomain.DecisionApprove},
			batch.Batch.RowVersion)
		if err != nil {
			return fmt.Errorf("approve an invoice of the icmal under review: %w", err)
		}
	}
	step("icmal", periodFrom.Format("2006-01"),
		fmt.Sprintf("%s, 1 invoice still to decide", batch.Batch.Status))
	return nil
}

// settledBatchSpec is one whole icmal driven to a settlement: which month it covers, which
// invoice it carries, and whether the money was actually paid.
type settledBatchSpec struct {
	Period  time.Time
	Invoice string
	Amount  string
	// Approve releases the money: the settlement leaves PENDING_APPROVAL for APPROVED.
	Approve bool
	// Pay enters the bank's reference against it, which is what marks it PAID.
	Pay bool
}

// ensureSettledBatch drives an icmal all the way to a settlement and, when the spec asks for it,
// approves and pays it.
//
// The settlement is not written here. The icmal's own `batch.decided` event is read back and
// handed to the handler kapsora-worker subscribes with, so the settlement in a demo database was
// opened by the code that opens real ones — with the contract's payment term, the netted
// recoveries and the due date that follows from the day the icmal was decided.
func (s *seeder) ensureSettledBatch(ctx context.Context, sc *scenario, spec settledBatchSpec) error {
	invoice, err := s.ensureSubmittedInvoice(ctx, sc, spec.Invoice, spec.Amount, serviceMRI, "1")
	if err != nil {
		return err
	}
	batch, err := s.ensureBatch(ctx, sc, spec.Period, []uuid.UUID{invoice.ID})
	if err != nil {
		return err
	}
	if batch, err = s.submitBatch(ctx, sc, batch); err != nil {
		return err
	}

	reviewer := rcTenant(sc.tenant, sc.reviewer, billingapp.PermissionRead,
		billingapp.PermissionBatchReview)
	// SUBMITTED as well as UNDER_REVIEW: an icmal the provider has just sent is SUBMITTED, and
	// it is the reviewer's first decision that moves it to UNDER_REVIEW.
	if batch.Batch.Status == billingdomain.BatchSubmitted ||
		batch.Batch.Status == billingdomain.BatchUnderReview {
		for _, member := range batch.Invoices {
			if member.Decision != "" {
				continue
			}
			if batch, err = s.biz.billing.ReviewBatchInvoice(ctx, reviewer, batch.Batch.ID,
				member.InvoiceID, billingapp.ReviewBatchInvoiceInput{
					Decision: billingdomain.DecisionApprove,
				}, batch.Batch.RowVersion); err != nil {
				return fmt.Errorf("approve %s: %w", spec.Invoice, err)
			}
		}
		if batch, err = s.biz.billing.DecideBatch(ctx, reviewer, batch.Batch.ID,
			batch.Batch.RowVersion); err != nil {
			return fmt.Errorf("decide the icmal of %s: %w", spec.Invoice, err)
		}
	}

	delivery, err := s.readOutboxEvent(ctx, sc.tenant, batch.Batch.ID,
		billingapp.BatchDecidedEvent)
	if err != nil {
		return err
	}
	if err := s.biz.billing.HandleBatchDecided(ctx, delivery); err != nil {
		return fmt.Errorf("open the settlement of %s: %w", spec.Invoice, err)
	}
	settlement, err := s.settlementOfBatch(ctx, sc, batch.Batch.ID)
	if err != nil {
		return err
	}
	if !spec.Approve {
		step("settle", spec.Invoice, settlement.Settlement.Status+", due "+
			settlement.Settlement.DueDate.Format(time.DateOnly))
		return nil
	}

	// The approver is a different person from the icmal's decider, which is the gate above the
	// tenant's settlement threshold and the honest way to do it below one.
	approver := rcTenant(sc.tenant, sc.approver, billingapp.PermissionRead,
		billingapp.PermissionSettlementRead, billingapp.PermissionSettlementApprove,
		billingapp.PermissionSettlementRecordPayment)
	if settlement.Settlement.Status == billingdomain.SettlementPendingApproval {
		if settlement, err = s.biz.billing.ApproveSettlement(ctx, approver,
			settlement.Settlement.ID, settlement.Settlement.RowVersion); err != nil {
			return fmt.Errorf("approve the settlement of %s: %w", spec.Invoice, err)
		}
	}
	// A payment is only entered against a settlement that is waiting for one, and only when
	// this spec asks for one. On a second seed run a paid settlement is PAID — or RECONCILED,
	// because the reconciliation of the same run marked it — and both are states a second
	// payment would rightly be refused against.
	if spec.Pay && billingdomain.CanRecordPayment(settlement.Settlement.Status) {
		paidAt := day(settlement.Settlement.DueDate)
		if settlement, err = s.biz.billing.CreatePaymentRecord(ctx, approver,
			settlement.Settlement.ID, billingapp.CreatePaymentRecordInput{
				ExternalReference: "EFT-DEMO-" + spec.Invoice,
				Amount:            settlement.Settlement.PayableAmount,
				CurrencyCode:      "TRY", PaidAt: paidAt, Source: "MANUAL",
			}); err != nil {
			return fmt.Errorf("pay the settlement of %s: %w", spec.Invoice, err)
		}
	}
	step("settle", spec.Invoice, settlement.Settlement.Status+", due "+
		settlement.Settlement.DueDate.Format(time.DateOnly))
	return nil
}

// submitBatch sends a draft icmal, and leaves one that has already been sent alone.
func (s *seeder) submitBatch(ctx context.Context, sc *scenario, batch billingapp.BatchView,
) (billingapp.BatchView, error) {
	if batch.Batch.Status != billingdomain.BatchDraft {
		return batch, nil
	}
	rc := rcOrganization(sc.tenant, sc.billing, sc.hospitalOrg,
		billingapp.PermissionRead, billingapp.PermissionBatchCreate,
		billingapp.PermissionBatchSubmit)
	out, err := s.biz.billing.SubmitBatch(ctx, rc, batch.Batch.ID, batch.Batch.RowVersion)
	if err != nil {
		return billingapp.BatchView{}, fmt.Errorf("send the icmal of %s: %w",
			batch.Batch.PeriodFrom.Format("2006-01"), err)
	}
	return out, nil
}

func batchInvoiceDecided(batch billingapp.BatchView, invoiceID uuid.UUID) bool {
	for _, member := range batch.Invoices {
		if member.InvoiceID == invoiceID {
			return member.Decision != ""
		}
	}
	return false
}

// settlementOfBatch finds the live settlement the handler opened for an icmal.
func (s *seeder) settlementOfBatch(ctx context.Context, sc *scenario, batchID uuid.UUID,
) (billingapp.SettlementView, error) {
	rc := rcTenant(sc.tenant, sc.reviewer, billingapp.PermissionRead,
		billingapp.PermissionSettlementRead)
	page, err := s.biz.billing.ListSettlements(ctx, rc, billingapp.SettlementFilter{
		BatchID: &batchID, Limit: 20,
	})
	if err != nil {
		return billingapp.SettlementView{}, fmt.Errorf("list settlements: %w", err)
	}
	for _, item := range page.Items {
		if item.Status == billingdomain.SettlementCancelled {
			continue
		}
		return s.biz.billing.GetSettlement(ctx, rc, item.ID)
	}
	return billingapp.SettlementView{},
		fmt.Errorf("the icmal %s has no settlement", batchID)
}

// ensureDemoReimbursement is the state scenario 4 opens on: the member has paid a provider
// themselves, sent a clean receipt and a masked account number, and is waiting for a decision.
//
// It goes through WP-I4-01's own request and WP-I7-04's own command, so every check of v1.2
// 10.10 applies: the receipt is scanned clean, the enrollment covers the service date, the
// contract's ceiling is respected and the duplicate window was searched.
func (s *seeder) ensureDemoReimbursement(ctx context.Context, sc *scenario) error {
	memberRC := rcPerson(sc.tenant, sc.member, sc.person,
		billingapp.PermissionServiceRequestRead, billingapp.PermissionServiceRequestCreate)
	page, err := s.biz.billing.ListReimbursements(ctx, memberRC, billingapp.ReimbursementFilter{
		PersonID: &sc.person, Limit: 20,
	})
	if err != nil {
		return fmt.Errorf("list the member's reimbursements: %w", err)
	}
	if len(page.Items) > 0 {
		step("refund", "member.a", page.Items[0].Status)
		return nil
	}

	requestID, err := s.ensureReimbursementRequest(ctx, sc)
	if err != nil {
		return err
	}
	receiptID, err := s.storeDemoDocument(ctx, sc.tenant, sc.hospitalOrg, "makbuz.pdf",
		documentdomain.ClassPersonal, "KAPSORA demo makbuzu")
	if err != nil {
		return err
	}
	draft, err := s.biz.billing.CreateReimbursement(ctx, memberRC,
		billingapp.CreateReimbursementInput{
			PersonID: sc.person, ServiceRequestID: requestID, ReceiptDocumentID: receiptID,
			RequestedAmount: "1200", CurrencyCode: "TRY",
			// A synthetic IBAN with a valid shape. It never reaches a column in the clear:
			// the command enciphers it and stores four characters of it as the mask.
			BankAccount: "TR330006100519786457841326",
		})
	if err != nil {
		return fmt.Errorf("open the member's reimbursement: %w", err)
	}
	submitted, err := s.biz.billing.SubmitReimbursement(ctx, memberRC, sc.person, draft.ID,
		draft.RowVersion)
	if err != nil {
		return fmt.Errorf("submit the member's reimbursement: %w", err)
	}
	step("refund", "member.a", submitted.Status+", account "+submitted.BankAccountMasked)
	return nil
}

// ensureReimbursementRequest raises the REIMBURSEMENT service request the reimbursement wraps.
// It is WP-I4-01's own command, so the request carries a version, an item and a status the rest
// of the platform recognises.
func (s *seeder) ensureReimbursementRequest(ctx context.Context, sc *scenario) (uuid.UUID, error) {
	rc := rcPerson(sc.tenant, sc.member, sc.person,
		billingapp.PermissionServiceRequestRead, billingapp.PermissionServiceRequestCreate)
	serviceDate := coveredDay(day(time.Now()).AddDate(0, 0, -20), 0)
	provider := sc.hospitalOrg
	draft, err := s.biz.requests.Create(ctx, rc, servicerequestapp.NewRequestInput{
		RequestType: "REIMBURSEMENT", PersonID: sc.person, ProgramID: sc.program,
		EnrollmentID: sc.enrollment, ProviderOrganizationID: &provider,
		ServiceDate: serviceDate, Channel: "MEMBER_PORTAL",
		Items: []servicerequestdomain.ItemInput{{
			ServiceDefinitionID: sc.services[serviceGPVisit].String(),
			RequestedQuantity:   "1", UnitType: "COUNT",
			RequestedAmount: "1200", CurrencyCode: "TRY",
		}},
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("raise the member's reimbursement request: %w", err)
	}
	submitted, err := s.biz.requests.Submit(ctx, rc, draft.Request.ID, nil,
		draft.Request.RowVersion)
	if err != nil {
		return uuid.Nil, fmt.Errorf("submit the member's reimbursement request: %w", err)
	}
	return submitted.Request.ID, nil
}

// storeDemoDocument writes one file this platform produced into the secure bucket and returns the
// clean document that holds it.
//
// It is `StoreRendered`, the path WP-I7-05's exports take: nobody uploaded these bytes, the seed
// produced them, so there is no browser to hand a presigned URL to and nothing untrusted to
// quarantine. The digest is taken over the whole body and the CLEAN verdict names this platform
// as the engine, so what a reviewer opens is a document whose scan history says who cleared it.
func (s *seeder) storeDemoDocument(ctx context.Context, tenantID, ownerID uuid.UUID,
	filename, classification, title string,
) (uuid.UUID, error) {
	owner := ownerID
	body := demoPDF(title, time.Now().UTC())
	doc, err := s.biz.documents.StoreRendered(ctx, tenantID, documentapp.RenderedFile{
		Filename: filename, ContentType: "application/pdf", Classification: classification,
		Body: body, OwnerOrganizationID: &owner, Action: "document.seed.store",
		Detail: map[string]any{"source": "kapsora-seed"},
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("store the demo document %s: %w", filename, err)
	}
	return doc.Object.ID, nil
}

// demoPDF is the smallest thing a PDF reader will open: one page with one line of text on it. It
// carries the moment it was produced so that two demo documents are never the same bytes — the
// document store deduplicates on the digest, and two receipts that were one file would be a
// reimbursement pointing at somebody else's paper.
func demoPDF(title string, at time.Time) []byte {
	text := fmt.Sprintf("%s - %s", title, at.Format(time.RFC3339Nano))
	body := "%PDF-1.4\n" +
		"1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
		"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
		"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 595 842]/Contents 4 0 R>>endobj\n" +
		fmt.Sprintf("4 0 obj<</Length %d>>stream\nBT /F1 12 Tf 72 760 Td (%s) Tj ET\nendstream endobj\n",
			len(text)+40, text) +
		"trailer<</Root 1 0 R>>\n%%EOF\n"
	return []byte(body)
}
