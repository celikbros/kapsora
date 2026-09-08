package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/claim/domain"
)

// The adjustment ledger and the provider's earnings view.
//
// One sentence is under every test here: **the approved total is the lines minus the
// adjustments, exactly, and the only way to undo an adjustment is a reversal.** Every
// assertion below is a way for that sentence to be false — a split that does not add up, a
// reversal that gives back a different figure, a second reversal of one row, an invoiced claim
// counted as money still to be collected.

// approvedClaim drives one health claim from draft to APPROVED, so the adjustment commands
// have a decided claim to act on. It is the fixture's own path — priced by the real ladder,
// decided by a real reviewer — because an adjustment against a claim nobody decided would
// prove nothing about the arithmetic the settlement reads.
func approvedClaim(t *testing.T, f *fixture) application.ClaimView {
	t.Helper()
	f.publishAdjudicationRule(t, "PHYSIO_FINANCIAL_ADJ",
		`serviceCode == "PHYSIO_SESSION"`, actionFinancialReview)
	authorization := f.authorizeSessions(t, "4")
	draft := f.newClaim(t, []application.NewLineInput{
		f.consultLine(1),
		f.physioLine(2, "2", physioTwoContract),
	}, func(in *application.NewClaimInput) { in.AuthorizationID = &authorization })
	submitted := submitClaim(t, f, draft)

	decided, err := f.claims.DecideLines(context.Background(), f.financialRC(), submitted.Claim.ID,
		application.DecideInput{
			Decisions: []application.DecisionInput{{
				LineNo: 2, Decision: domain.DecisionApproved, ApprovedQuantity: "2",
				ApprovedAmount: physioTwoContract, PayerAmount: physioTwoContract,
				MemberAmount: "0", ReasonCode: "WITHIN_AUTHORIZATION",
			}},
			ExpectedVersion: submitted.Claim.RowVersion,
		}, application.AccessRequest{})
	if err != nil {
		t.Fatalf("decide lines: %v", err)
	}
	approved, err := f.claims.Approve(context.Background(), f.financialRC(), decided.Claim.ID,
		application.ReasonInput{ReasonCode: "COMPLETE", ExpectedVersion: decided.Claim.RowVersion})
	if err != nil {
		t.Fatalf("approve claim: %v", err)
	}
	if approved.Claim.Status != domain.StatusApproved {
		t.Fatalf("status = %s, want APPROVED", approved.Claim.Status)
	}
	return approved
}

// readiness reads the totals back through the endpoint the invoice will use.
func readinessOf(t *testing.T, f *fixture, claimID uuid.UUID) application.InvoiceReadiness {
	t.Helper()
	out, err := f.claims.InvoiceReadiness(context.Background(), f.financialRC(), claimID)
	if err != nil {
		t.Fatalf("invoice readiness: %v", err)
	}
	return out
}

// TestApprovedTotalIsLinesMinusAdjustments is section 3's fifth requirement, checked after
// every command rather than once at the end.
//
// The claim approves 949.99. A cut of 100 leaves 849.99 and a recovery of 49.99 leaves 800 —
// figures chosen so that an implementation that dropped a fraction, or that added the
// adjustments instead of subtracting them, gives a visibly different answer.
func TestApprovedTotalIsLinesMinusAdjustments(t *testing.T) {
	f := newFixture(t)
	claim := approvedClaim(t, f)
	ctx := context.Background()

	before := readinessOf(t, f, claim.Claim.ID)
	if before.LineTotal != "949.99" || before.ApprovedTotal != "949.99" {
		t.Fatalf("before any adjustment: lines %s, approved %s, want 949.99 / 949.99",
			before.LineTotal, before.ApprovedTotal)
	}
	if before.AdjustmentTotal != "0" || before.AdjustmentCount != 0 {
		t.Fatalf("a claim nobody adjusted carries %s over %d rows, want 0 over 0",
			before.AdjustmentTotal, before.AdjustmentCount)
	}

	cut, err := f.claims.CreateAdjustment(ctx, f.financialRC(), claim.Claim.ID,
		application.AdjustmentInput{
			AdjustmentType: domain.AdjustmentCut, Amount: "100", PayerAmount: "80",
			MemberAmount: "20", ReasonCode: "TARIFF_EXCEEDED",
		})
	if err != nil {
		t.Fatalf("create cut: %v", err)
	}
	// The command answers with the totals it produced, computed by the readiness code rather
	// than by a second sum.
	if cut.Readiness.ApprovedTotal != "849.99" {
		t.Errorf("after a cut of 100: approved = %s, want 849.99", cut.Readiness.ApprovedTotal)
	}
	if cut.Readiness.AdjustmentTotal != "100" {
		t.Errorf("adjustment total = %s, want 100", cut.Readiness.AdjustmentTotal)
	}
	// The split survives the subtraction: 893.74 - 80 and 56.25 - 20.
	if cut.Readiness.PayerTotal != "813.74" || cut.Readiness.MemberTotal != "36.25" {
		t.Errorf("payer/member = %s/%s, want 813.74/36.25",
			cut.Readiness.PayerTotal, cut.Readiness.MemberTotal)
	}
	assertSplitHolds(t, cut.Readiness)

	recovery, err := f.claims.CreateAdjustment(ctx, f.financialRC(), claim.Claim.ID,
		application.AdjustmentInput{
			AdjustmentType: domain.AdjustmentRecovery, Amount: "49.99", PayerAmount: "49.99",
			MemberAmount: "0", ReasonCode: "OVERPAYMENT",
		})
	if err != nil {
		t.Fatalf("create recovery: %v", err)
	}
	if recovery.Readiness.ApprovedTotal != "800" {
		t.Errorf("after a recovery of 49.99: approved = %s, want 800",
			recovery.Readiness.ApprovedTotal)
	}
	if recovery.Readiness.AdjustmentCount != 2 {
		t.Errorf("adjustment count = %d, want 2", recovery.Readiness.AdjustmentCount)
	}
	assertSplitHolds(t, recovery.Readiness)

	// And the endpoint answers the same thing the command did.
	after := readinessOf(t, f, claim.Claim.ID)
	if after.ApprovedTotal != recovery.Readiness.ApprovedTotal {
		t.Errorf("readiness answers %s and the command answered %s; there is one sum",
			after.ApprovedTotal, recovery.Readiness.ApprovedTotal)
	}
	// A RECOVERY is a recovery rather than a review cut, and the ledger says so.
	rows, err := f.claims.ListAdjustments(ctx, f.financialRC(), claim.Claim.ID)
	if err != nil {
		t.Fatalf("list adjustments: %v", err)
	}
	if len(rows) != 2 || rows[0].SourceType != domain.AdjustmentSourceReview ||
		rows[1].SourceType != domain.AdjustmentSourceRecovery {
		t.Errorf("ledger sources = %v, want REVIEW then RECOVERY", sourcesOf(rows))
	}
	if rows[0].CreatedBy == nil || *rows[0].CreatedBy != f.reviewer {
		t.Errorf("the cut names %v, want the reviewer who made it", rows[0].CreatedBy)
	}
}

// TestAReversalRestoresTheApprovedTotalExactly is section 3's fourth requirement.
//
// The reversal carries no figures of its own: it names the row it takes back, and the amount
// and the split are read off that row. An implementation that let the caller supply them, or
// that recorded the reversal without negating the split, would leave the payer total off by
// the eighty lira nobody would ever find.
func TestAReversalRestoresTheApprovedTotalExactly(t *testing.T) {
	f := newFixture(t)
	claim := approvedClaim(t, f)
	ctx := context.Background()
	original := readinessOf(t, f, claim.Claim.ID)

	cut, err := f.claims.CreateAdjustment(ctx, f.financialRC(), claim.Claim.ID,
		application.AdjustmentInput{
			AdjustmentType: domain.AdjustmentCut, Amount: "100", PayerAmount: "80",
			MemberAmount: "20", ReasonCode: "TARIFF_EXCEEDED",
		})
	if err != nil {
		t.Fatalf("create cut: %v", err)
	}
	if cut.Readiness.ApprovedTotal == original.ApprovedTotal {
		t.Fatal("the cut changed nothing; there is nothing to reverse")
	}

	reversal, err := f.claims.CreateAdjustment(ctx, f.financialRC(), claim.Claim.ID,
		application.AdjustmentInput{
			ReversesAdjustmentID: &cut.Adjustment.ID, ReasonCode: "REVIEW_REVERSED",
		})
	if err != nil {
		t.Fatalf("reverse the cut: %v", err)
	}
	if reversal.Adjustment.AdjustmentType != domain.AdjustmentReversal {
		t.Errorf("type = %s, want REVERSAL", reversal.Adjustment.AdjustmentType)
	}
	if reversal.Adjustment.Amount != "-100" || reversal.Adjustment.PayerAmount != "-80" ||
		reversal.Adjustment.MemberAmount != "-20" {
		t.Errorf("reversal = %s (%s/%s), want the exact negative of the cut",
			reversal.Adjustment.Amount, reversal.Adjustment.PayerAmount,
			reversal.Adjustment.MemberAmount)
	}
	// **Exactly** where it was, on all three figures.
	if reversal.Readiness.ApprovedTotal != original.ApprovedTotal ||
		reversal.Readiness.PayerTotal != original.PayerTotal ||
		reversal.Readiness.MemberTotal != original.MemberTotal {
		t.Errorf("after the reversal: %s/%s/%s, want the original %s/%s/%s",
			reversal.Readiness.ApprovedTotal, reversal.Readiness.PayerTotal,
			reversal.Readiness.MemberTotal, original.ApprovedTotal, original.PayerTotal,
			original.MemberTotal)
	}
	if reversal.Readiness.AdjustmentTotal != "0" {
		t.Errorf("adjustment total = %s after a cut and its reversal, want 0",
			reversal.Readiness.AdjustmentTotal)
	}
	// Nothing was deleted: both rows are still there, and the chain is readable.
	rows, err := f.claims.ListAdjustments(ctx, f.financialRC(), claim.Claim.ID)
	if err != nil {
		t.Fatalf("list adjustments: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ledger has %d rows after a cut and its reversal, want both", len(rows))
	}
	if rows[1].ReversesAdjustmentID == nil || *rows[1].ReversesAdjustmentID != cut.Adjustment.ID {
		t.Errorf("the reversal points at %v, want the cut it took back", rows[1].ReversesAdjustmentID)
	}

	// A second reversal of the same row would give the money back twice.
	if _, err := f.claims.CreateAdjustment(ctx, f.financialRC(), claim.Claim.ID,
		application.AdjustmentInput{
			ReversesAdjustmentID: &cut.Adjustment.ID, ReasonCode: "REVIEW_REVERSED",
		}); !errors.Is(err, application.ErrAdjustmentReversed) {
		t.Errorf("a second reversal of one cut: %v, want ErrAdjustmentReversed", err)
	}
	// And a reversal of a reversal is refused, so no reader has to walk a chain of unknown
	// length to find out what a claim is worth.
	if _, err := f.claims.CreateAdjustment(ctx, f.financialRC(), claim.Claim.ID,
		application.AdjustmentInput{
			ReversesAdjustmentID: &reversal.Adjustment.ID, ReasonCode: "REVIEW_REVERSED",
		}); !errors.Is(err, application.ErrAdjustmentNotReversible) {
		t.Errorf("reversing a reversal: %v, want ErrAdjustmentNotReversible", err)
	}
}

// TestTheAdjustmentSplitIsTheDatabases proves the invariant survives the service being
// bypassed. The domain refuses a split that does not add up with a sentence naming the field;
// this is the half that holds whatever reaches the table.
func TestTheAdjustmentSplitIsTheDatabases(t *testing.T) {
	f := newFixture(t)
	claim := approvedClaim(t, f)

	// The service's half.
	_, err := f.claims.CreateAdjustment(context.Background(), f.financialRC(), claim.Claim.ID,
		application.AdjustmentInput{
			AdjustmentType: domain.AdjustmentCut, Amount: "100", PayerAmount: "60",
			MemberAmount: "50", ReasonCode: "TARIFF_EXCEEDED",
		})
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("60 + 50 against 100: %v, want a validation error naming the field", err)
	}

	// The database's half, with the application bypassed entirely.
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if _, err := f.h.Admin.Exec(ctx, `
		INSERT INTO claim.adjustment (tenant_id, claim_id, version_no, adjustment_type, amount,
		                              payer_amount, member_amount, currency_code, reason_code,
		                              source_type, created_by)
		VALUES ($1, $2, 1, 'CUT', 100, 60, 50, 'TRY', 'TARIFF_EXCEEDED', 'REVIEW', $3)`,
		f.tenant, claim.Claim.ID, f.reviewer); err == nil {
		t.Fatal("the database accepted an adjustment whose halves do not add up")
	}

	// So does the actor rule: only the two system sources may have nobody behind them.
	if _, err := f.h.Admin.Exec(ctx, `
		INSERT INTO claim.adjustment (tenant_id, claim_id, version_no, adjustment_type, amount,
		                              payer_amount, member_amount, currency_code, reason_code,
		                              source_type)
		VALUES ($1, $2, 1, 'CUT', 100, 100, 0, 'TRY', 'TARIFF_EXCEEDED', 'REVIEW')`,
		f.tenant, claim.Claim.ID); err == nil {
		t.Fatal("the database accepted a reviewer's cut with no reviewer on it")
	}
}

// TestAnAdjustmentNeedsADecidedClaim: an adjustment on a draft is not a cut, it is somebody
// editing a statement nobody has answered yet.
func TestAnAdjustmentNeedsADecidedClaim(t *testing.T) {
	f := newFixture(t)
	draft := f.newClaim(t, []application.NewLineInput{f.consultLine(1)}, nil)
	_, err := f.claims.CreateAdjustment(context.Background(), f.financialRC(), draft.Claim.ID,
		application.AdjustmentInput{
			AdjustmentType: domain.AdjustmentCut, Amount: "10", PayerAmount: "10",
			MemberAmount: "0", ReasonCode: "TARIFF_EXCEEDED",
		})
	if !errors.Is(err, application.ErrNotDecided) {
		t.Errorf("adjusting a draft: %v, want ErrNotDecided", err)
	}
}

// TestAnAdjustmentIsRefusedInAnotherCurrency: one claim is denominated once, so an adjustment
// in another currency would be a figure nobody can subtract from the claim's total — and a
// service that took it anyway would produce an approved total in two currencies at once.
//
// **Nothing is written.** The refusal happens before the row, so a caller who mistyped the
// currency has left no ledger row to have to reverse.
func TestAnAdjustmentIsRefusedInAnotherCurrency(t *testing.T) {
	f := newFixture(t)
	claim := approvedClaim(t, f)
	ctx := context.Background()

	_, err := f.claims.CreateAdjustment(ctx, f.financialRC(), claim.Claim.ID,
		application.AdjustmentInput{
			AdjustmentType: domain.AdjustmentCut, Amount: "100", PayerAmount: "100",
			MemberAmount: "0", CurrencyCode: "EUR", ReasonCode: "TARIFF_EXCEEDED",
		})
	if !errors.Is(err, application.ErrAdjustmentCurrency) {
		t.Fatalf("an adjustment in EUR on a TRY claim: %v, want ErrAdjustmentCurrency", err)
	}
	rows, err := f.claims.ListAdjustments(ctx, f.financialRC(), claim.Claim.ID)
	if err != nil {
		t.Fatalf("list adjustments: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("the refused adjustment left %d rows behind, want none", len(rows))
	}
	after := readinessOf(t, f, claim.Claim.ID)
	if after.ApprovedTotal != "949.99" || after.AdjustmentTotal != "0" {
		t.Errorf("after the refusal: approved %s over %s of adjustments, want 949.99 over 0",
			after.ApprovedTotal, after.AdjustmentTotal)
	}

	// The claim's own currency is accepted, so the check is about the mismatch rather than
	// about the field being present at all.
	if _, err := f.claims.CreateAdjustment(ctx, f.financialRC(), claim.Claim.ID,
		application.AdjustmentInput{
			AdjustmentType: domain.AdjustmentCut, Amount: "100", PayerAmount: "100",
			MemberAmount: "0", CurrencyCode: "TRY", ReasonCode: "TARIFF_EXCEEDED",
		}); err != nil {
		t.Fatalf("an adjustment in the claim's own currency: %v, want it accepted", err)
	}
}

// TestAnAdjustmentReasonComesFromTheClosedList: a dispute is answerable only if the reason is
// a code somebody can count rather than a sentence one reviewer typed.
func TestAnAdjustmentReasonComesFromTheClosedList(t *testing.T) {
	f := newFixture(t)
	claim := approvedClaim(t, f)
	_, err := f.claims.CreateAdjustment(context.Background(), f.financialRC(), claim.Claim.ID,
		application.AdjustmentInput{
			AdjustmentType: domain.AdjustmentCut, Amount: "10", PayerAmount: "10",
			MemberAmount: "0", ReasonCode: "BECAUSE_I_SAID_SO",
		})
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("an unknown reason code: %v, want a validation error", err)
	}
}

// TestEveryAdjustmentIsAnAuditRowAndAnEvent: the ledger is also a record of who moved money.
func TestEveryAdjustmentIsAnAuditRowAndAnEvent(t *testing.T) {
	f := newFixture(t)
	claim := approvedClaim(t, f)
	if _, err := f.claims.CreateAdjustment(context.Background(), f.financialRC(), claim.Claim.ID,
		application.AdjustmentInput{
			AdjustmentType: domain.AdjustmentCut, Amount: "100", PayerAmount: "100",
			MemberAmount: "0", ReasonCode: "TARIFF_EXCEEDED",
		}); err != nil {
		t.Fatalf("create cut: %v", err)
	}
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var audits, events int
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM audit.event
		         WHERE tenant_id = $1 AND action_code = 'claim.adjust' AND resource_id = $2),
		       (SELECT count(*) FROM system.outbox_event
		         WHERE tenant_id = $1 AND event_type = 'claim.adjusted' AND aggregate_id = $2)`,
		f.tenant, claim.Claim.ID).Scan(&audits, &events); err != nil {
		t.Fatalf("read audit and outbox: %v", err)
	}
	if audits != 1 || events != 1 {
		t.Errorf("one adjustment left %d audit rows and %d events, want 1 and 1", audits, events)
	}
}

// assertSplitHolds is the invariant every total in this file carries: the two halves are the
// whole, exactly.
func assertSplitHolds(t *testing.T, readiness application.InvoiceReadiness) {
	t.Helper()
	payer := mustQuantity(t, readiness.PayerTotal)
	member := mustQuantity(t, readiness.MemberTotal)
	approved := mustQuantity(t, readiness.ApprovedTotal)
	if payer.Add(member).Cmp(approved) != 0 {
		t.Errorf("payer %s + member %s != approved %s",
			readiness.PayerTotal, readiness.MemberTotal, readiness.ApprovedTotal)
	}
}

func sourcesOf(rows []application.AdjustmentRecord) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.SourceType)
	}
	return out
}
