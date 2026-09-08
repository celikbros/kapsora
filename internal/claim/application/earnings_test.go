package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/claim/domain"
)

// The provider's earnings view: the figure their invoice will be checked against.

// earningsOf asks the endpoint the provider's screen asks.
func earningsOf(t *testing.T, f *fixture, from, to *time.Time, currency string,
) application.ProviderEarnings {
	t.Helper()
	out, err := f.claims.ProviderEarnings(context.Background(), f.financialRC(), f.provider,
		application.EarningsFilter{From: from, To: to, CurrencyCode: currency})
	if err != nil {
		t.Fatalf("provider earnings: %v", err)
	}
	return out
}

// bucket finds one currency's answer.
func bucket(t *testing.T, earnings application.ProviderEarnings, currency string,
) application.EarningsCurrency {
	t.Helper()
	for _, row := range earnings.Currencies {
		if row.CurrencyCode == currency {
			return row
		}
	}
	t.Fatalf("earnings carry no %s bucket; currencies = %d", currency, len(earnings.Currencies))
	return application.EarningsCurrency{}
}

// TestEarningsInvoiceableTotalIsWhatIsNotYetOnAnInvoice is section 3's last requirement.
//
// Two decided claims: one still APPROVED and one already INVOICED. Both count towards what the
// provider earned; only the first is money still to be collected. An implementation that
// counted the invoiced one as invoiceable would have the provider bill for it twice, and
// nothing downstream would notice until a reconciliation months later.
func TestEarningsInvoiceableTotalIsWhatIsNotYetOnAnInvoice(t *testing.T) {
	f := newFixture(t)
	first := approvedClaim(t, f)

	// A second decided claim of the same provider, moved on to INVOICED the way WP-I7-02
	// will move it. The application has no command for that yet, so the status is written
	// directly — which is exactly the state this endpoint has to answer correctly about.
	second := f.approveConsultOnly(t)
	f.h.AdminExec(`UPDATE claim.claim SET status = 'INVOICED' WHERE tenant_id = $1 AND id = $2`,
		f.tenant, second.Claim.ID)

	earnings := earningsOf(t, f, nil, nil, "")
	if earnings.ProviderOrganizationID != f.provider || earnings.ProviderName == "" {
		t.Errorf("earnings name %q for %v, want the provider's own",
			earnings.ProviderName, earnings.ProviderOrganizationID)
	}
	try := bucket(t, earnings, "TRY")
	if try.ClaimCount != 2 {
		t.Fatalf("claim count = %d, want both decided claims", try.ClaimCount)
	}
	// 949.99 approved + 449.99 invoiced.
	if try.ApprovedTotal != "1399.98" {
		t.Errorf("approved total = %s, want 1399.98", try.ApprovedTotal)
	}
	if try.InvoiceableTotal != "949.99" {
		t.Errorf("invoiceable total = %s, want only the claim nobody has invoiced (949.99)",
			try.InvoiceableTotal)
	}
	if len(try.InvoiceableClaimIDs) != 1 || try.InvoiceableClaimIDs[0] != first.Claim.ID {
		t.Errorf("invoiceable claim ids = %v, want only the APPROVED claim",
			try.InvoiceableClaimIDs)
	}
	// The grouping by status is the same two claims, said the other way round.
	byStatus := map[string]application.EarningsStatusTotal{}
	for _, row := range try.ByStatus {
		byStatus[row.Status] = row
	}
	if got := byStatus[domain.StatusApproved]; got.ClaimCount != 1 || got.ApprovedTotal != "949.99" {
		t.Errorf("APPROVED bucket = %+v, want one claim of 949.99", got)
	}
	if got := byStatus[domain.StatusInvoiced]; got.ClaimCount != 1 || got.ApprovedTotal != "449.99" {
		t.Errorf("INVOICED bucket = %+v, want one claim of 449.99", got)
	}

	// An adjustment moves the invoiceable total by exactly what it took off, and nothing else.
	if _, err := f.claims.CreateAdjustment(context.Background(), f.financialRC(), first.Claim.ID,
		application.AdjustmentInput{
			AdjustmentType: domain.AdjustmentCut, Amount: "49.99", PayerAmount: "49.99",
			MemberAmount: "0", ReasonCode: "TARIFF_EXCEEDED",
		}); err != nil {
		t.Fatalf("create cut: %v", err)
	}
	adjusted := bucket(t, earningsOf(t, f, nil, nil, ""), "TRY")
	if adjusted.InvoiceableTotal != "900" {
		t.Errorf("after a cut of 49.99: invoiceable = %s, want 900", adjusted.InvoiceableTotal)
	}
	if adjusted.AdjustmentTotal != "49.99" {
		t.Errorf("adjustment total = %s, want 49.99", adjusted.AdjustmentTotal)
	}
	if adjusted.ApprovedTotal != "1349.99" {
		t.Errorf("approved total = %s, want 1349.99", adjusted.ApprovedTotal)
	}
}

// TestEarningsAreAnsweredPerCurrency: a total across currencies is not a total, it is two
// numbers written next to each other.
func TestEarningsAreAnsweredPerCurrency(t *testing.T) {
	f := newFixture(t)
	approvedClaim(t, f)
	f.approveInCurrency(t, "EUR")

	earnings := earningsOf(t, f, nil, nil, "")
	if len(earnings.Currencies) != 2 {
		t.Fatalf("currencies = %d, want one bucket each for TRY and EUR", len(earnings.Currencies))
	}
	try := bucket(t, earnings, "TRY")
	eur := bucket(t, earnings, "EUR")
	if try.ClaimCount != 1 || eur.ClaimCount != 1 {
		t.Errorf("claims per currency = %d TRY / %d EUR, want one each",
			try.ClaimCount, eur.ClaimCount)
	}
	if try.InvoiceableTotal == eur.InvoiceableTotal {
		t.Errorf("both buckets answer %s; the two currencies were added together",
			try.InvoiceableTotal)
	}

	// And the filter narrows to one bucket rather than summing across them.
	only := earningsOf(t, f, nil, nil, "EUR")
	if len(only.Currencies) != 1 || only.Currencies[0].CurrencyCode != "EUR" {
		t.Errorf("currency filter answered %d buckets, want only EUR", len(only.Currencies))
	}
}

// TestAClaimInTwoCurrenciesIsRefusedAtCreation is why the earnings view can bucket a whole
// claim by one currency at all.
func TestAClaimInTwoCurrenciesIsRefusedAtCreation(t *testing.T) {
	f := newFixture(t)
	try, eur := "TRY", "EUR"
	first := f.consultLine(1)
	first.CurrencyCode = &try
	second := f.physioLine(2, "1", "250")
	second.CurrencyCode = &eur

	_, err := f.claims.CreateClaim(context.Background(), f.providerRC(),
		application.NewClaimInput{
			PersonID: f.person, ProgramID: f.program, EnrollmentID: f.enrollment,
			ProviderOrganizationID: f.provider, CaseID: &f.caseID,
			ServiceDateFrom: serviceDay, ServiceDateTo: serviceDay,
			Lines: []application.NewLineInput{first, second},
		}, application.AccessRequest{})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a claim in two currencies: %v, want a validation error", err)
	}
}

// TestAProviderMayOnlyAskAboutItself: the earnings figure is the provider's own money, and a
// provider-scoped caller asking about somebody else's is refused by the same function the
// create command uses.
func TestAProviderMayOnlyAskAboutItself(t *testing.T) {
	f := newFixture(t)
	_, err := f.claims.ProviderEarnings(context.Background(), f.providerRC(), f.otherOrg,
		application.EarningsFilter{})
	if !errors.Is(err, application.ErrProviderScope) {
		t.Errorf("asking about another provider: %v, want ErrProviderScope", err)
	}
	if _, err := f.claims.ProviderEarnings(context.Background(), f.providerRC(), f.provider,
		application.EarningsFilter{}); err != nil {
		t.Errorf("asking about its own provider: %v, want an answer", err)
	}
}

// TestEarningsArePeriodBoundedByTheDecision: what a provider earned in March is what was
// decided in March.
func TestEarningsArePeriodBoundedByTheDecision(t *testing.T) {
	f := newFixture(t)
	approvedClaim(t, f)

	day := domain.DateOnly(fixtureNow)
	inside := earningsOf(t, f, &day, &day, "")
	if len(inside.Currencies) != 1 {
		t.Fatalf("a period covering the decision day answered %d buckets, want 1",
			len(inside.Currencies))
	}
	before := day.AddDate(0, 0, -2)
	dayBefore := day.AddDate(0, 0, -1)
	outside := earningsOf(t, f, &before, &dayBefore, "")
	if len(outside.Currencies) != 0 {
		t.Errorf("a period ending before the decision answered %d buckets, want none",
			len(outside.Currencies))
	}
}

// approveConsultOnly raises and approves a second, smaller claim of the same provider.
//
// It is deliberately the same service on the same day as the first claim, so the submit's own
// duplicate cross-check sends it to a person — which is the ordinary way a second claim
// reaches a reviewer, and exactly the state the earnings view has to answer about.
func (f *fixture) approveConsultOnly(t *testing.T) application.ClaimView {
	t.Helper()
	draft := f.newClaim(t, []application.NewLineInput{f.consultLine(1)}, nil)
	return f.decideAllAndApprove(t, submitClaim(t, f, draft), consultPrice)
}

// approveInCurrency raises a claim whose lines are denominated in another currency and decides
// it, so the earnings view has a second bucket to keep apart.
func (f *fixture) approveInCurrency(t *testing.T, currency string) application.ClaimView {
	t.Helper()
	line := f.consultLine(1)
	line.CurrencyCode = &currency
	draft := f.newClaim(t, []application.NewLineInput{line}, nil)
	return f.decideAllAndApprove(t, submitClaim(t, f, draft), consultPrice)
}

// decideAllAndApprove answers every undecided line of a submitted claim at the whole amount,
// entirely to the payer, and finishes it. It exists so the earnings tests are about the
// earnings arithmetic rather than about which cross-check happened to route a claim.
func (f *fixture) decideAllAndApprove(t *testing.T, view application.ClaimView, amount string,
) application.ClaimView {
	t.Helper()
	ctx := context.Background()
	if domain.Decided(view.Claim.Status) {
		return view
	}
	rc := f.financialRC()
	if view.Claim.Status == domain.StatusPendingMedical {
		rc = f.medicalRC()
	}
	decisions := make([]application.DecisionInput, 0, len(view.Lines))
	for _, line := range view.Lines {
		if line.Decision != nil {
			continue
		}
		decisions = append(decisions, application.DecisionInput{
			LineNo: line.Line.LineNo, Decision: domain.DecisionApproved,
			ApprovedQuantity: line.Line.Quantity, ApprovedAmount: amount,
			PayerAmount: amount, MemberAmount: "0", ReasonCode: "WITHIN_AUTHORIZATION",
		})
	}
	if len(decisions) > 0 {
		decided, err := f.claims.DecideLines(ctx, rc, view.Claim.ID, application.DecideInput{
			Decisions: decisions, ExpectedVersion: view.Claim.RowVersion,
		}, application.AccessRequest{})
		if err != nil {
			t.Fatalf("decide the second claim: %v", err)
		}
		view = decided
	}
	if domain.Decided(view.Claim.Status) {
		return view
	}
	approved, err := f.claims.Approve(ctx, f.financialRC(), view.Claim.ID,
		application.ReasonInput{ReasonCode: "COMPLETE", ExpectedVersion: view.Claim.RowVersion})
	if err != nil {
		t.Fatalf("approve the second claim: %v", err)
	}
	return approved
}
