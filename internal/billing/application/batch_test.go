package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/billing/settings"
	claimdomain "github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// WP-I7-03 section 3, driven end to end: real invoices, real claims, the real claim ledger
// behind every cut and every reversal, and the real work queue behind the review.

// TestPutBatchInvoicesRefusesAMixture is section 3's second requirement, first half.
//
// A batch is one provider, one payer, one currency, one domain and one *submitted* invoice
// each, and each of the five is refused separately so a provider is told which one.
func TestPutBatchInvoicesRefusesAMixture(t *testing.T) {
	f := newFixture(t)
	rc := f.providerBatchRC()

	mine := f.submittedInvoice(t, "ICM0001", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-BATCH-0001", "1000"): "1000",
	})
	batch := f.draftBatch(t)

	// A currency the batch is not in. The invoice is raised in EUR and the icmal in TRY, and a
	// total across currencies is not a total.
	euroClaim := f.approvedClaim(t, "CLM-BATCH-0002", "500")
	f.h.AdminExec(`UPDATE claim.claim_line l SET currency_code = 'EUR'
	                 FROM claim.claim_version v
	                WHERE v.tenant_id = l.tenant_id AND v.id = l.version_id
	                  AND v.claim_id = $2 AND l.tenant_id = $1`, f.tenant, euroClaim)
	euro := f.submittedInvoiceInCurrency(t, "ICM0002", euroClaim, "500", "EUR")

	_, err := f.invoices.PutBatchInvoices(context.Background(), rc, batch.Batch.ID,
		[]uuid.UUID{mine.Invoice.ID, euro.Invoice.ID}, batch.Batch.RowVersion)
	var mixed *application.MixedBatchError
	if !errors.As(err, &mixed) {
		t.Fatalf("a mixed currency was accepted: %v", err)
	}
	if mixed.Field != "currencyCode" {
		t.Errorf("mixed field = %s, want currencyCode", mixed.Field)
	}
	if mixed.ExpectedValue != "TRY" || mixed.ActualValue != "EUR" {
		t.Errorf("mixed values = %s/%s, want TRY/EUR", mixed.ExpectedValue, mixed.ActualValue)
	}

	// A domain the batch is not in.
	other := f.submittedInvoiceInDomain(t, "ICM0003", f.approvedClaim(t, "CLM-BATCH-0003", "300"),
		"300", "ACCOMMODATION")
	_, err = f.invoices.PutBatchInvoices(context.Background(), rc, batch.Batch.ID,
		[]uuid.UUID{other.Invoice.ID}, batch.Batch.RowVersion)
	if !errors.As(err, &mixed) || mixed.Field != "domainCode" {
		t.Fatalf("a mixed domain was accepted: %v", err)
	}

	// A draft, which nobody has sent to the payer yet.
	draftClaim := f.approvedClaim(t, "CLM-BATCH-0004", "200")
	draft := f.createInvoice(t, rc, "ICM0004", "200")
	draft = f.allocate(t, rc, draft, draftClaim, "200")
	_, err = f.invoices.PutBatchInvoices(context.Background(), rc, batch.Batch.ID,
		[]uuid.UUID{draft.Invoice.ID}, batch.Batch.RowVersion)
	if !errors.As(err, &mixed) || mixed.Field != "status" {
		t.Fatalf("a draft invoice was accepted into an icmal: %v", err)
	}

	// And nothing of the refused sets was written: a replacement that refused halfway would be
	// a batch holding whatever happened to come before the bad row.
	stored, err := f.invoices.GetBatch(context.Background(), rc, batch.Batch.ID)
	if err != nil {
		t.Fatalf("read the batch back: %v", err)
	}
	if len(stored.Invoices) != 0 {
		t.Fatalf("the refused replacements left %d invoices in the batch", len(stored.Invoices))
	}
}

// TestBatchSizeIsTheTenants is section 3's second requirement, second half.
func TestBatchSizeIsTheTenants(t *testing.T) {
	f := newFixture(t)
	rc := f.providerBatchRC()
	f.setSetting(settings.KeyBatchMaxInvoices, "1")

	first := f.submittedInvoice(t, "ICM0101", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-SIZE-0001", "100"): "100",
	})
	second := f.submittedInvoice(t, "ICM0102", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-SIZE-0002", "100"): "100",
	})

	batch := f.draftBatch(t)
	filled, err := f.invoices.PutBatchInvoices(context.Background(), rc, batch.Batch.ID,
		[]uuid.UUID{first.Invoice.ID, second.Invoice.ID}, batch.Batch.RowVersion)
	if err != nil {
		t.Fatalf("put batch invoices: %v", err)
	}
	// Two invoices are allowed in a draft; the count is the tenant's rule at *submit*, which is
	// the moment the membership stops being editable.
	_, err = f.invoices.SubmitBatch(context.Background(), rc, filled.Batch.ID,
		filled.Batch.RowVersion)
	var size *application.BatchSizeError
	if !errors.As(err, &size) {
		t.Fatalf("a batch above the tenant's maximum was submitted: %v", err)
	}
	if size.Count != 2 || size.Max != 1 {
		t.Errorf("size refusal = count %d max %d, want 2/1", size.Count, size.Max)
	}

	// Inside the bound it goes.
	trimmed, err := f.invoices.PutBatchInvoices(context.Background(), rc, filled.Batch.ID,
		[]uuid.UUID{first.Invoice.ID}, filled.Batch.RowVersion)
	if err != nil {
		t.Fatalf("trim the batch: %v", err)
	}
	if _, err := f.invoices.SubmitBatch(context.Background(), rc, trimmed.Batch.ID,
		trimmed.Batch.RowVersion); err != nil {
		t.Fatalf("submit a batch inside the bound: %v", err)
	}
}

// TestReplacingTheMembershipMovesTheETag guards the race the If-Match exists for: two provider
// clerks with the same ETag, one of whom replaced the membership first.
func TestReplacingTheMembershipMovesTheETag(t *testing.T) {
	f := newFixture(t)
	rc := f.providerBatchRC()

	first := f.submittedInvoice(t, "ICM0151", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-ETAG-0001", "100"): "100",
	})
	second := f.submittedInvoice(t, "ICM0152", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-ETAG-0002", "200"): "200",
	})
	batch := f.draftBatch(t)
	filled, err := f.invoices.PutBatchInvoices(context.Background(), rc, batch.Batch.ID,
		[]uuid.UUID{first.Invoice.ID}, batch.Batch.RowVersion)
	if err != nil {
		t.Fatalf("put batch invoices: %v", err)
	}
	// The header did not change and the ETag did: it is a statement about the icmal and what it
	// covers.
	if filled.Batch.RowVersion == batch.Batch.RowVersion {
		t.Fatalf("the row version stayed at %d after the membership was replaced",
			filled.Batch.RowVersion)
	}
	// The second clerk, still holding the old one, writes nothing.
	if _, err := f.invoices.PutBatchInvoices(context.Background(), rc, batch.Batch.ID,
		[]uuid.UUID{second.Invoice.ID}, batch.Batch.RowVersion); !errors.Is(
		err, application.ErrVersionMismatch) {
		t.Fatalf("a stale If-Match replaced the membership: %v", err)
	}
	stored, err := f.invoices.GetBatch(context.Background(), rc, batch.Batch.ID)
	if err != nil {
		t.Fatalf("read the batch back: %v", err)
	}
	if len(stored.Invoices) != 1 || stored.Invoices[0].InvoiceID != first.Invoice.ID {
		t.Fatalf("the batch holds %+v", stored.Invoices)
	}
}

// TestOneLiveBatchPerInvoice is section 3's third requirement, at the level the service sees it:
// an invoice already in a live icmal is refused by name, and the refusal says which icmal.
func TestOneLiveBatchPerInvoice(t *testing.T) {
	f := newFixture(t)
	rc := f.providerBatchRC()

	invoice := f.submittedInvoice(t, "ICM0201", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-LIVE-0001", "400"): "400",
	})
	first := f.draftBatch(t)
	if _, err := f.invoices.PutBatchInvoices(context.Background(), rc, first.Batch.ID,
		[]uuid.UUID{invoice.Invoice.ID}, first.Batch.RowVersion); err != nil {
		t.Fatalf("put the invoice into the first batch: %v", err)
	}

	second := f.draftBatch(t)
	_, err := f.invoices.PutBatchInvoices(context.Background(), rc, second.Batch.ID,
		[]uuid.UUID{invoice.Invoice.ID}, second.Batch.RowVersion)
	var batched *application.InvoiceBatchedError
	if !errors.As(err, &batched) {
		t.Fatalf("one invoice went into two live batches: %v", err)
	}
	if batched.LiveBatchID != first.Batch.ID {
		t.Errorf("the refusal named batch %s, want %s", batched.LiveBatchID, first.Batch.ID)
	}
}

// TestCutSpreadsExactlyAcrossTheClaims is section 3's fourth requirement, first half.
//
// The allocations are chosen so the proportional split does not come out even: the shares are
// truncated, the remainder is one part in ten million, and it lands on the *largest* allocation.
// A mutation that rounded each share, or dropped the remainder, or put it on the smallest, is
// caught by the three assertions below.
func TestCutSpreadsExactlyAcrossTheClaims(t *testing.T) {
	f := newFixture(t)
	reviewer := f.reviewerRC(f.reviewer)

	big := f.approvedClaim(t, "CLM-CUT-0001", "1000")
	middle := f.approvedClaim(t, "CLM-CUT-0002", "500")
	small := f.approvedClaim(t, "CLM-CUT-0003", "1")
	invoice := f.submittedInvoice(t, "ICM0301", map[uuid.UUID]string{
		big: "1000", middle: "500", small: "1",
	})
	batch := f.submittedBatch(t, invoice.Invoice.ID)

	// 1501 billed, 1401 approved: a cut of exactly 100 to spread over 1000, 500 and 1.
	after := f.review(t, reviewer, batch, invoice.Invoice.ID,
		application.ReviewBatchInvoiceInput{
			Decision: domain.DecisionCut, ApprovedAmount: "1401",
			ReasonCode: "TARIFF_EXCEEDED",
		})
	member := memberOf(t, after, invoice.Invoice.ID)
	if member.ApprovedAmount != "1401" {
		t.Fatalf("approved amount = %s, want 1401", member.ApprovedAmount)
	}

	rows := f.adjustments(t, big, middle, small)
	if len(rows) != 3 {
		t.Fatalf("the cut wrote %d adjustments, want one per claim", len(rows))
	}
	total := benefitdomain.ZeroQuantity()
	byClaim := map[uuid.UUID]string{}
	for _, row := range rows {
		if row.Type != claimdomain.AdjustmentCut {
			t.Errorf("adjustment on %s is %s, want CUT", row.ClaimID, row.Type)
		}
		byClaim[row.ClaimID] = row.Amount
		total = total.Add(mustQuantity(t, row.Amount))
	}
	// **The adjustments sum exactly to the cut.** Not within a kuruş: exactly.
	if total.String() != "100" {
		t.Fatalf("the cut's adjustments sum to %s, want exactly 100", total.String())
	}
	// The proportional shares, truncated, with the remainder of two micro-units on the largest
	// allocation. 100 × 1000/1501 = 66.622251..., 100 × 500/1501 = 33.311125...,
	// 100 × 1/1501 = 0.066622...
	if got := byClaim[big]; got != "66.622253" {
		t.Errorf("the largest allocation was cut %s, want 66.622253 (its share plus the remainder)", got)
	}
	if got := byClaim[middle]; got != "33.311125" {
		t.Errorf("the middle allocation was cut %s, want 33.311125", got)
	}
	if got := byClaim[small]; got != "0.066622" {
		t.Errorf("the smallest allocation was cut %s, want 0.066622", got)
	}
}

// TestChangedDecisionReversesRatherThanEdits is section 3's fourth requirement, second half.
func TestChangedDecisionReversesRatherThanEdits(t *testing.T) {
	f := newFixture(t)
	reviewer := f.reviewerRC(f.reviewer)

	claim := f.approvedClaim(t, "CLM-REV-0001", "1000")
	invoice := f.submittedInvoice(t, "ICM0401", map[uuid.UUID]string{claim: "1000"})
	batch := f.submittedBatch(t, invoice.Invoice.ID)

	cut := f.review(t, reviewer, batch, invoice.Invoice.ID, application.ReviewBatchInvoiceInput{
		Decision: domain.DecisionCut, ApprovedAmount: "900", ReasonCode: "CONTRACT_TERMS",
	})
	first := f.adjustments(t, claim)
	if len(first) != 1 || first[0].Amount != "100" {
		t.Fatalf("the cut wrote %v, want one row of 100", first)
	}

	// The reviewer changes their mind: the invoice is approved in full.
	changed := f.review(t, reviewer, cut, invoice.Invoice.ID,
		application.ReviewBatchInvoiceInput{Decision: domain.DecisionApprove})
	member := memberOf(t, changed, invoice.Invoice.ID)
	if member.Decision != domain.DecisionApprove || member.ApprovedAmount != "1000" {
		t.Fatalf("the changed decision reads %s/%s, want APPROVE/1000",
			member.Decision, member.ApprovedAmount)
	}

	// **The cut is still on the record and a reversal stands beside it.** The ledger is what an
	// appeal is answered from, so nothing in it is ever edited or deleted.
	second := f.adjustments(t, claim)
	if len(second) != 2 {
		t.Fatalf("the change left %d ledger rows, want the cut and its reversal", len(second))
	}
	if second[0].Amount != "100" || second[0].Type != claimdomain.AdjustmentCut {
		t.Errorf("the original cut was edited: %+v", second[0])
	}
	if second[1].Type != claimdomain.AdjustmentReversal || second[1].Amount != "-100" {
		t.Errorf("the reversal reads %+v, want REVERSAL of -100", second[1])
	}
	if second[1].Reverses == nil {
		t.Fatal("the reversal names no adjustment")
	}

	// And the claim is worth exactly what it was before the cut: a reversal restores the total
	// rather than approximately restoring it.
	if total := f.claimApprovedTotal(t, claim); total != "1000" {
		t.Fatalf("after the reversal the claim is worth %s, want 1000", total)
	}
}

// TestReturnFreesTheClaimsAndTheCorrectionCanBeBatchedAgain is section 3's fifth requirement,
// first half.
func TestReturnFreesTheClaimsAndTheCorrectionCanBeBatchedAgain(t *testing.T) {
	f := newFixture(t)
	provider := f.providerBatchRC()
	reviewer := f.reviewerRC(f.reviewer)

	claim := f.approvedClaim(t, "CLM-RET-0001", "600")
	invoice := f.submittedInvoice(t, "ICM0501", map[uuid.UUID]string{claim: "600"})
	batch := f.submittedBatch(t, invoice.Invoice.ID)

	f.review(t, reviewer, batch, invoice.Invoice.ID, application.ReviewBatchInvoiceInput{
		Decision: domain.DecisionReturn, ReasonCode: "DOCUMENT_MISSING",
	})
	if status := f.invoiceStatus(t, invoice.Invoice.ID); status != domain.StatusReturned {
		t.Fatalf("the returned invoice is %s, want RETURNED", status)
	}
	if status := f.claimStatus(t, claim); status != claimdomain.StatusApproved {
		t.Fatalf("the claim of a returned invoice is %s, want APPROVED", status)
	}

	// The provider raises the correction, submits it and puts it into a second icmal. That the
	// whole chain works is the point: a return that freed the claim but left it unbatchable
	// would be a return nobody could act on.
	correction, err := f.invoices.CreateInvoice(context.Background(), provider,
		f.correctionHeader("ICM0502", "600", invoice.Invoice.ID))
	if err != nil {
		t.Fatalf("open the correction: %v", err)
	}
	correction = f.allocate(t, provider, correction, claim, "600")
	submitted, err := f.invoices.SubmitInvoice(context.Background(), provider,
		correction.Invoice.ID, correction.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("submit the correction: %v", err)
	}
	second := f.submittedBatch(t, submitted.Invoice.ID)
	if len(second.Invoices) != 1 {
		t.Fatalf("the correction did not go into a second icmal: %+v", second.Invoices)
	}
}

// TestRejectClosesTheClaimsUnpaid is section 3's fifth requirement, second half.
func TestRejectClosesTheClaimsUnpaid(t *testing.T) {
	f := newFixture(t)
	reviewer := f.reviewerRC(f.reviewer)

	claim := f.approvedClaim(t, "CLM-REJ-0001", "750")
	invoice := f.submittedInvoice(t, "ICM0601", map[uuid.UUID]string{claim: "750"})
	batch := f.submittedBatch(t, invoice.Invoice.ID)

	after := f.review(t, reviewer, batch, invoice.Invoice.ID,
		application.ReviewBatchInvoiceInput{
			Decision: domain.DecisionReject, ReasonCode: "NOT_COVERED",
		})
	if status := f.invoiceStatus(t, invoice.Invoice.ID); status != domain.StatusRejected {
		t.Fatalf("the rejected invoice is %s, want REJECTED", status)
	}
	if status := f.claimStatus(t, claim); status != claimdomain.StatusClosedUnpaid {
		t.Fatalf("the claim of a rejected invoice is %s, want CLOSED_UNPAID", status)
	}

	// And withdrawing the rejection puts the claim back on the document rather than leaving it
	// finished: a decision may be changed while the batch is under review.
	f.review(t, reviewer, after, invoice.Invoice.ID, application.ReviewBatchInvoiceInput{
		Decision: domain.DecisionApprove,
	})
	if status := f.claimStatus(t, claim); status != claimdomain.StatusInvoiced {
		t.Fatalf("after withdrawing the rejection the claim is %s, want INVOICED", status)
	}
	if status := f.invoiceStatus(t, invoice.Invoice.ID); status != domain.StatusInBatch {
		t.Fatalf("after withdrawing the rejection the invoice is %s, want IN_BATCH", status)
	}
}

// TestTotalsReconcileAfterEveryDecision is the acceptance criterion the settlement rests on.
//
// Four invoices, one of each decision, and the four totals have to be the submitted total at the
// moment the icmal is decided — which is also `ck_billing_batch_totals`, so a service that got
// the arithmetic wrong could not have written the row at all.
func TestTotalsReconcileAfterEveryDecision(t *testing.T) {
	f := newFixture(t)
	reviewer := f.reviewerRC(f.reviewer)
	approver := f.reviewerRC(f.approver)

	approved := f.submittedInvoice(t, "ICM0701", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-TOT-0001", "1000"): "1000",
	})
	cut := f.submittedInvoice(t, "ICM0702", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-TOT-0002", "800"): "800",
	})
	returned := f.submittedInvoice(t, "ICM0703", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-TOT-0003", "400"): "400",
	})
	rejected := f.submittedInvoice(t, "ICM0704", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-TOT-0004", "200"): "200",
	})
	batch := f.submittedBatch(t, approved.Invoice.ID, cut.Invoice.ID, returned.Invoice.ID,
		rejected.Invoice.ID)
	if batch.Batch.SubmittedTotal != "2400" {
		t.Fatalf("submitted total = %s, want 2400", batch.Batch.SubmittedTotal)
	}

	view := f.review(t, reviewer, batch, approved.Invoice.ID,
		application.ReviewBatchInvoiceInput{Decision: domain.DecisionApprove})
	view = f.review(t, reviewer, view, cut.Invoice.ID, application.ReviewBatchInvoiceInput{
		Decision: domain.DecisionCut, ApprovedAmount: "600", ReasonCode: "TARIFF_EXCEEDED",
	})
	view = f.review(t, reviewer, view, returned.Invoice.ID,
		application.ReviewBatchInvoiceInput{
			Decision: domain.DecisionReturn, ReasonCode: "DOCUMENT_MISSING",
		})
	view = f.review(t, reviewer, view, rejected.Invoice.ID,
		application.ReviewBatchInvoiceInput{
			Decision: domain.DecisionReject, ReasonCode: "NOT_COVERED",
		})

	// The summary reconciles before the batch is closed, so a reviewer's progress bar and the
	// settlement's figures are produced by one arithmetic.
	summary, err := f.invoices.GetBatchSummary(context.Background(), reviewer, view.Batch.ID)
	if err != nil {
		t.Fatalf("read the summary: %v", err)
	}
	if summary.PendingCount != 0 {
		t.Errorf("summary says %d invoices are still pending", summary.PendingCount)
	}
	counts := map[string]int{}
	for _, row := range summary.Decisions {
		counts[row.Decision] = row.Count
	}
	for _, decision := range domain.BatchDecisions {
		if counts[decision] != 1 {
			t.Errorf("summary counts %d invoices as %s, want 1", counts[decision], decision)
		}
	}

	decided, err := f.invoices.DecideBatch(context.Background(), approver, view.Batch.ID,
		view.Batch.RowVersion)
	if err != nil {
		t.Fatalf("decide the batch: %v", err)
	}
	if decided.Batch.Status != domain.BatchDecided {
		t.Fatalf("the batch is %s, want DECIDED", decided.Batch.Status)
	}
	// 1000 approved + 600 of the cut = 1600; 200 cut; 400 returned; 200 rejected.
	if decided.Batch.ApprovedTotal != "1600" || decided.Batch.CutTotal != "200" ||
		decided.Batch.ReturnedTotal != "400" || decided.Batch.RejectedTotal != "200" {
		t.Fatalf("totals = approved %s cut %s returned %s rejected %s",
			decided.Batch.ApprovedTotal, decided.Batch.CutTotal,
			decided.Batch.ReturnedTotal, decided.Batch.RejectedTotal)
	}
	sum := mustQuantity(t, decided.Batch.ApprovedTotal).
		Add(mustQuantity(t, decided.Batch.CutTotal)).
		Add(mustQuantity(t, decided.Batch.ReturnedTotal)).
		Add(mustQuantity(t, decided.Batch.RejectedTotal))
	if sum.String() != decided.Batch.SubmittedTotal {
		t.Fatalf("the decided totals sum to %s, want the submitted %s",
			sum.String(), decided.Batch.SubmittedTotal)
	}

	// Where each decision left its invoice.
	if status := f.invoiceStatus(t, approved.Invoice.ID); status != domain.StatusApproved {
		t.Errorf("the approved invoice is %s, want APPROVED", status)
	}
	if status := f.invoiceStatus(t, cut.Invoice.ID); status != domain.StatusPartiallyApproved {
		t.Errorf("the cut invoice is %s, want PARTIALLY_APPROVED", status)
	}
}

// TestTheSubmitterCannotDecideAndTheSecondPersonRule is section 3's sixth requirement.
func TestTheSubmitterCannotDecideAndTheSecondPersonRule(t *testing.T) {
	f := newFixture(t)
	// The provider clerk who sends the batch also happens to hold batch.review in this test —
	// which is exactly the case the rule exists for: holding the permission is not the
	// question, and being the submitter is.
	submitter := f.providerBatchRC()
	submitter.Permissions[application.PermissionBatchReview] = struct{}{}
	reviewer := f.reviewerRC(f.reviewer)

	invoice := f.submittedInvoice(t, "ICM0801", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-MC-0001", "5000"): "5000",
	})
	batch := f.submittedBatch(t, invoice.Invoice.ID)
	view := f.approveAll(t, reviewer, batch)

	// The submitter never decides, at any amount.
	_, err := f.invoices.DecideBatch(context.Background(), submitter, view.Batch.ID,
		view.Batch.RowVersion)
	if !errors.Is(err, application.ErrBatchSubmitterCannotDecide) {
		t.Fatalf("the submitter decided their own icmal: %v", err)
	}

	// Below the threshold the reviewer may close it themselves.
	if _, err := f.invoices.DecideBatch(context.Background(), reviewer, view.Batch.ID,
		view.Batch.RowVersion); err != nil {
		t.Fatalf("a reviewer below the threshold could not close the batch: %v", err)
	}
}

// TestAboveTheThresholdTheLastReviewerCannotDecide is the other half of the maker-checker rule.
func TestAboveTheThresholdTheLastReviewerCannotDecide(t *testing.T) {
	f := newFixture(t)
	f.setSetting(settings.KeyBatchDecisionThreshold, `"1000"`)
	reviewer := f.reviewerRC(f.reviewer)
	approver := f.reviewerRC(f.approver)

	invoice := f.submittedInvoice(t, "ICM0901", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-MC-0002", "5000"): "5000",
	})
	batch := f.submittedBatch(t, invoice.Invoice.ID)
	view := f.approveAll(t, reviewer, batch)

	_, err := f.invoices.DecideBatch(context.Background(), reviewer, view.Batch.ID,
		view.Batch.RowVersion)
	if !errors.Is(err, application.ErrBatchSecondReviewerRequired) {
		t.Fatalf("the last reviewer closed an icmal above the threshold: %v", err)
	}
	if _, err := f.invoices.DecideBatch(context.Background(), approver, view.Batch.ID,
		view.Batch.RowVersion); err != nil {
		t.Fatalf("a second person could not close the batch: %v", err)
	}
}

// TestABatchCannotBeDecidedBeforeEveryInvoiceIsAnswered guards the totals: a batch closed with
// an undecided invoice in it would be a settlement opening on a figure that is not the sum of
// its parts.
func TestABatchCannotBeDecidedBeforeEveryInvoiceIsAnswered(t *testing.T) {
	f := newFixture(t)
	reviewer := f.reviewerRC(f.reviewer)
	approver := f.reviewerRC(f.approver)

	first := f.submittedInvoice(t, "ICM1001", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-PEND-0001", "100"): "100",
	})
	second := f.submittedInvoice(t, "ICM1002", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-PEND-0002", "100"): "100",
	})
	batch := f.submittedBatch(t, first.Invoice.ID, second.Invoice.ID)
	view := f.review(t, reviewer, batch, first.Invoice.ID,
		application.ReviewBatchInvoiceInput{Decision: domain.DecisionApprove})

	_, err := f.invoices.DecideBatch(context.Background(), approver, view.Batch.ID,
		view.Batch.RowVersion)
	if !errors.Is(err, application.ErrBatchNotFullyDecided) {
		t.Fatalf("a half-reviewed icmal was closed: %v", err)
	}
}

// TestTheWorkItemFollowsTheBatch is section 3's sixth requirement, third clause.
func TestTheWorkItemFollowsTheBatch(t *testing.T) {
	f := newFixture(t)
	f.createReviewQueue(t)
	reviewer := f.reviewerRC(f.reviewer)
	approver := f.reviewerRC(f.approver)

	invoice := f.submittedInvoice(t, "ICM1101", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-WI-0001", "300"): "300",
	})
	batch := f.submittedBatch(t, invoice.Invoice.ID)

	item, found := f.batchWorkItem(t, batch.Batch.ID)
	if !found {
		t.Fatal("the submit raised no work item in the payer's finance queue")
	}
	if item.Status != "OPEN" {
		t.Fatalf("the raised work item is %s, want OPEN", item.Status)
	}

	// The first decision claims it, and the batch comes under review.
	view := f.approveAll(t, reviewer, batch)
	if view.Batch.Status != domain.BatchUnderReview {
		t.Fatalf("after the first decision the batch is %s, want UNDER_REVIEW", view.Batch.Status)
	}
	item, _ = f.batchWorkItem(t, batch.Batch.ID)
	if item.Status != "CLAIMED" || item.Assignee.UUID != f.reviewer {
		t.Fatalf("the work item is %s held by %v, want CLAIMED by the reviewer",
			item.Status, item.Assignee)
	}

	if _, err := f.invoices.DecideBatch(context.Background(), approver, view.Batch.ID,
		view.Batch.RowVersion); err != nil {
		t.Fatalf("decide: %v", err)
	}
	item, _ = f.batchWorkItem(t, batch.Batch.ID)
	if item.Status != "COMPLETED" || item.Outcome == nil || *item.Outcome != "DECIDED" {
		t.Fatalf("the work item is %s with outcome %v, want COMPLETED/DECIDED",
			item.Status, item.Outcome)
	}
}

// TestEveryDecisionIsAnAuditRow is section 3's seventh requirement.
func TestEveryDecisionIsAnAuditRow(t *testing.T) {
	f := newFixture(t)
	reviewer := f.reviewerRC(f.reviewer)

	invoice := f.submittedInvoice(t, "ICM1201", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-AUD-0001", "1000"): "1000",
	})
	batch := f.submittedBatch(t, invoice.Invoice.ID)
	cut := f.review(t, reviewer, batch, invoice.Invoice.ID, application.ReviewBatchInvoiceInput{
		Decision: domain.DecisionCut, ApprovedAmount: "900", ReasonCode: "CONTRACT_TERMS",
	})
	f.review(t, reviewer, cut, invoice.Invoice.ID, application.ReviewBatchInvoiceInput{
		Decision: domain.DecisionApprove,
	})

	rows := f.auditDetails(t, batch.Batch.ID, "batch.invoice.review")
	if len(rows) != 2 {
		t.Fatalf("two decisions produced %d audit rows", len(rows))
	}
	first := rows[0]
	if first["decision"] != domain.DecisionCut {
		t.Errorf("the first audit row records %v, want CUT", first["decision"])
	}
	if first["reason_code"] != "CONTRACT_TERMS" {
		t.Errorf("the first audit row records reason %v", first["reason_code"])
	}
	if first["submitted_amount"] != "1000" || first["approved_amount"] != "900" ||
		first["cut_amount"] != "100" {
		t.Errorf("the first audit row's amounts are %v/%v/%v",
			first["submitted_amount"], first["approved_amount"], first["cut_amount"])
	}
	// The change records what it changed *from*, which is what makes a chain of decisions
	// readable without joining the ledger.
	if rows[1]["previous_decision"] != domain.DecisionCut {
		t.Errorf("the second audit row's previous decision is %v, want CUT",
			rows[1]["previous_decision"])
	}

	// And every one of them names the actor.
	if actor := f.auditActor(t, batch.Batch.ID, "batch.invoice.review"); actor != f.reviewer {
		t.Errorf("the decision was audited against %s, want the reviewer %s", actor, f.reviewer)
	}
}

// TestSubmitAndDecidePublishTheirEvents is what WP-I7-04 opens a settlement on.
func TestSubmitAndDecidePublishTheirEvents(t *testing.T) {
	f := newFixture(t)
	reviewer := f.reviewerRC(f.reviewer)
	approver := f.reviewerRC(f.approver)

	invoice := f.submittedInvoice(t, "ICM1301", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-EV-0001", "250"): "250",
	})
	batch := f.submittedBatch(t, invoice.Invoice.ID)
	if types := f.outboxTypes(t, batch.Batch.ID); len(types) != 1 ||
		types[0] != application.BatchSubmittedEvent {
		t.Fatalf("after the submit the outbox holds %v", types)
	}
	view := f.approveAll(t, reviewer, batch)
	if _, err := f.invoices.DecideBatch(context.Background(), approver, view.Batch.ID,
		view.Batch.RowVersion); err != nil {
		t.Fatalf("decide: %v", err)
	}
	types := f.outboxTypes(t, batch.Batch.ID)
	if len(types) != 2 || types[1] != application.BatchDecidedEvent {
		t.Fatalf("after the decide the outbox holds %v", types)
	}
}

// TestSubmitAboveTheThresholdNeedsAStepUp is section 2.2's step-up.
func TestSubmitAboveTheThresholdNeedsAStepUp(t *testing.T) {
	f := newFixture(t)
	f.setSetting(settings.KeyBatchDecisionThreshold, `"100"`)
	rc := f.providerBatchRC()

	invoice := f.submittedInvoice(t, "ICM1401", map[uuid.UUID]string{
		f.approvedClaim(t, "CLM-SU-0001", "500"): "500",
	})
	batch := f.draftBatch(t)
	filled, err := f.invoices.PutBatchInvoices(context.Background(), rc, batch.Batch.ID,
		[]uuid.UUID{invoice.Invoice.ID}, batch.Batch.RowVersion)
	if err != nil {
		t.Fatalf("put batch invoices: %v", err)
	}
	if _, err := f.invoices.SubmitBatch(context.Background(), rc, filled.Batch.ID,
		filled.Batch.RowVersion); !errors.Is(err, identity.ErrStepUpRequired) {
		t.Fatalf("a batch above the threshold went without a step-up: %v", err)
	}
	stepped := rc
	stepped.StepUpValid = true
	if _, err := f.invoices.SubmitBatch(context.Background(), stepped, filled.Batch.ID,
		filled.Batch.RowVersion); err != nil {
		t.Fatalf("submit with a valid step-up: %v", err)
	}
}

// mustQuantity parses an exact decimal a test produced.
func mustQuantity(t *testing.T, raw string) benefitdomain.Quantity {
	t.Helper()
	value, err := benefitdomain.ParseQuantity(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return value
}
