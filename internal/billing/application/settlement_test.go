package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/billing/settings"
	"github.com/celikbros/kapsora/internal/identity"
)

// WP-I7-04 section 3, the settlement's half: the arithmetic is exact, the constraints are the
// database's, a second delivery opens nothing, the sum of the payment records never exceeds the
// settlement, and the batch's decider cannot release the money above the threshold.

// TestSettlementOpensFromTheDecidedBatchExactly is the first line of section 3: a settlement
// opened from a batch equals its approved total minus the netted recoveries, exactly.
func TestSettlementOpensFromTheDecidedBatchExactly(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)

	// One claim, one invoice, one icmal, approved in full. Then a recovery the payer is
	// holding against this provider from an earlier overpayment.
	claim := f.approvedClaim(t, "CLM-SETTLE-1", "1000.55")
	invoice := f.submittedInvoice(t, "STL2026001", map[uuid.UUID]string{claim: "1000.55"})
	batch := f.submittedBatch(t, invoice.Invoice.ID)
	batch = f.approveAll(t, f.reviewerRC(f.reviewer), batch)

	other := f.approvedClaim(t, "CLM-SETTLE-OLD", "400")
	f.recovery(t, other, "100.05")

	decided, err := f.invoices.DecideBatch(context.Background(), f.reviewerRC(f.approver),
		batch.Batch.ID, batch.Batch.RowVersion)
	if err != nil {
		t.Fatalf("decide batch: %v", err)
	}
	if err := f.deliverBatchDecided(t, decided.Batch.ID); err != nil {
		t.Fatalf("deliver batch.decided: %v", err)
	}

	view := f.settlementOf(t, decided.Batch.ID)
	if view.Settlement.ApprovedAmount != "1000.55" {
		t.Errorf("approved amount = %s, want the batch's 1000.55",
			view.Settlement.ApprovedAmount)
	}
	if view.Settlement.WithheldAmount != "100.05" {
		t.Errorf("withheld amount = %s, want 100.05", view.Settlement.WithheldAmount)
	}
	// Exactly, to the kuruş. A float would land on 900.4999999999999 here.
	if view.Settlement.PayableAmount != "900.5" {
		t.Errorf("payable amount = %s, want exactly 900.5", view.Settlement.PayableAmount)
	}
	if view.Settlement.Status != domain.SettlementPendingApproval {
		t.Errorf("status = %s, want PENDING_APPROVAL", view.Settlement.Status)
	}
	if !domain.ValidSettlementReference(view.Settlement.Reference) {
		t.Errorf("reference %q does not have the shape the column CHECKs",
			view.Settlement.Reference)
	}
	if view.Settlement.VersionNo != 1 {
		t.Errorf("version = %d, want 1", view.Settlement.VersionNo)
	}
	if len(view.Recoveries) != 1 || view.Recoveries[0].Amount != "100.05" {
		t.Fatalf("recoveries = %+v, want exactly the one that was netted", view.Recoveries)
	}

	// The due date is the contract's term applied to the day the batch was decided, and not to
	// the day the handler happened to run.
	decidedDay := decided.Batch.DecidedAt.UTC().Format(time.DateOnly)
	want := dayAfter(t, decidedDay, 30)
	if got := view.Settlement.DueDate.UTC().Format(time.DateOnly); got != want {
		t.Errorf("due date = %s, want %s (30 days after the decision)", got, want)
	}
	if view.Settlement.SettlementMethod != domain.MethodBankTransfer {
		t.Errorf("settlement method = %s, want the contract's BANK_TRANSFER",
			view.Settlement.SettlementMethod)
	}
}

// TestASecondBatchDecidedDeliveryOpensNothing is the other half of the same line: the outbox
// delivers at least once, and a redelivered event must not open a second settlement.
func TestASecondBatchDecidedDeliveryOpensNothing(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 15, domain.MethodBankTransfer)
	batch, first := f.openSettlement(t, "STL2026002", "CLM-SETTLE-2", "500")

	if err := f.deliverBatchDecided(t, batch.Batch.ID); err != nil {
		t.Fatalf("second delivery: %v", err)
	}
	if err := f.deliverBatchDecided(t, batch.Batch.ID); err != nil {
		t.Fatalf("third delivery: %v", err)
	}
	if got := f.settlementCount(t, batch.Batch.ID); got != 1 {
		t.Fatalf("the batch has %d settlements after three deliveries, want exactly 1", got)
	}
	again := f.settlementOf(t, batch.Batch.ID)
	if again.Settlement.ID != first.Settlement.ID {
		t.Errorf("the live settlement changed identity between deliveries")
	}
}

// TestARecoveryIsNettedOnceAcrossTwoSettlements is what `billing.settlement_recovery` exists
// for: a recovery taken by one settlement is not open for the next.
func TestARecoveryIsNettedOnceAcrossTwoSettlements(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	old := f.approvedClaim(t, "CLM-REC-OLD", "400")
	f.recovery(t, old, "90")

	_, first := f.openSettlement(t, "STL2026010", "CLM-REC-1", "300")
	if first.Settlement.WithheldAmount != "90" {
		t.Fatalf("the first settlement withheld %s, want 90", first.Settlement.WithheldAmount)
	}
	_, second := f.openSettlement(t, "STL2026011", "CLM-REC-2", "300")
	if second.Settlement.WithheldAmount != "0" {
		t.Errorf("the second settlement withheld %s, want 0: the recovery was already netted",
			second.Settlement.WithheldAmount)
	}
	if second.Settlement.PayableAmount != "300" {
		t.Errorf("the second settlement pays %s, want the whole 300",
			second.Settlement.PayableAmount)
	}
}

// TestAContractWithNoPaymentTermOpensNoSettlement is `PAYMENT_TERM_MISSING`: this platform will
// not invent a due date, and the event dead-letters rather than being retried for ever.
func TestAContractWithNoPaymentTermOpensNoSettlement(t *testing.T) {
	f := newFixture(t)
	batch := f.decidedBatch(t, "STL2026003", "CLM-SETTLE-3", "250")

	err := f.deliverBatchDecided(t, batch.Batch.ID)
	if !errors.Is(err, application.ErrPaymentTermMissing) {
		t.Fatalf("delivery answered %v, want PAYMENT_TERM_MISSING", err)
	}
	if got := f.settlementCount(t, batch.Batch.ID); got != 0 {
		t.Errorf("%d settlements were opened without a payment term, want 0", got)
	}
}

// TestPayableNeverExceedsApprovedIsTheDatabases is the first bullet of section 3, proved
// against the constraint rather than against the service: a psql session writing a settlement
// whose payable amount is above the approved total is refused by the CHECK.
//
// It is an insert rather than an update because the freeze trigger would catch an update first,
// and what this test is about is the arithmetic and not the immutability — the freeze has its
// own test underneath.
func TestPayableNeverExceedsApprovedIsTheDatabases(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	batch, _ := f.openSettlement(t, "STL2026004", "CLM-SETTLE-4", "700")

	err := f.h.AdminExecErr(`
		INSERT INTO billing.settlement (tenant_id, reference, batch_id, version_no,
		                                provider_organization_id, currency_code,
		                                approved_amount, withheld_amount, payable_amount,
		                                due_date, settlement_method, status)
		VALUES ($1, 'ST-202603-AAAAAAAA', $2, 99, $3, 'TRY', 700, -100, 800, '2026-04-01',
		        'BANK_TRANSFER', 'CANCELLED')`,
		f.tenant, batch.Batch.ID, f.provider)
	if err == nil {
		t.Fatal("the database accepted a payable amount above the approved total")
	}
	if !strings.Contains(err.Error(), "ck_billing_settlement_payable") &&
		!strings.Contains(err.Error(), "ck_billing_settlement_amounts_sign") {
		t.Errorf("refused by %v, want the payable CHECK", err)
	}
}

// TestTheApprovedAmountIsFrozenAfterDraft is the second half of the same bullet, and it is a
// trigger rather than a CHECK because it is about what the row *was*.
func TestTheApprovedAmountIsFrozenAfterDraft(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	_, view := f.openSettlement(t, "STL2026005", "CLM-SETTLE-5", "700")

	// PENDING_APPROVAL, so the freeze is on. Both figures move together, so the payable CHECK
	// cannot be what refuses this.
	err := f.h.AdminExecErr(`
		UPDATE billing.settlement
		   SET approved_amount = approved_amount + 100, payable_amount = payable_amount + 100
		 WHERE tenant_id = $1 AND id = $2`, f.tenant, view.Settlement.ID)
	if err == nil {
		t.Fatal("the database let the approved amount move after DRAFT")
	}
	if !strings.Contains(err.Error(), "frozen") {
		t.Errorf("refused by %v, want the freeze trigger", err)
	}
}

// TestApprovingASettlementTellsTheProviderAndPublishesTheEvent is section 2.2's approval path.
func TestApprovingASettlementTellsTheProviderAndPublishesTheEvent(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	_, view := f.openSettlement(t, "STL2026006", "CLM-SETTLE-6", "1200")

	approved, err := f.invoices.ApproveSettlement(context.Background(),
		f.approverRC(f.approver), view.Settlement.ID, view.Settlement.RowVersion)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Settlement.Status != domain.SettlementApproved {
		t.Fatalf("status = %s, want APPROVED", approved.Settlement.Status)
	}
	if approved.Settlement.ApprovedBy == nil || *approved.Settlement.ApprovedBy != f.approver {
		t.Errorf("approved_by = %v, want the approver", approved.Settlement.ApprovedBy)
	}

	types := f.outboxTypes(t, view.Settlement.ID)
	if !contains(types, application.SettlementApprovedEvent) {
		t.Errorf("outbox carries %v, want %s", types, application.SettlementApprovedEvent)
	}

	// The message the provider gets: the reference, the payable figure, the due date, and no
	// invoice number of any kind.
	messages := f.notificationVariables(t, "settlement.approved")
	if len(messages) == 0 {
		t.Fatal("the provider was told nothing about an approved settlement")
	}
	variables, _ := messages[0]["variables"].(map[string]any)
	if variables["amount"] != approved.Settlement.PayableAmount {
		t.Errorf("the message says %v, want the payable amount %s",
			variables["amount"], approved.Settlement.PayableAmount)
	}
	if variables["reference_no"] != approved.Settlement.Reference {
		t.Errorf("the message references %v, want %s",
			variables["reference_no"], approved.Settlement.Reference)
	}
}

// TestTheBatchDeciderCannotApproveAboveTheThreshold is the third bullet of section 3.
func TestTheBatchDeciderCannotApproveAboveTheThreshold(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	f.setSetting(settings.KeySettlementCheckerThreshold, `"100"`)
	_, view := f.openSettlement(t, "STL2026007", "CLM-SETTLE-7", "5000")

	// The person who decided the batch is `f.approver` — that is who `decidedBatch` closes it
	// with. Above the threshold they may not also release the money.
	_, err := f.invoices.ApproveSettlement(context.Background(), f.approverRC(f.approver),
		view.Settlement.ID, view.Settlement.RowVersion)
	if !errors.Is(err, application.ErrSettlementDeciderCannotApprove) {
		t.Fatalf("the batch's decider approved and got %v, want "+
			"SETTLEMENT_DECIDER_CANNOT_APPROVE", err)
	}

	// Anybody else may. The settlement is untouched by the refusal, so the same row version
	// still applies.
	second := f.h.CreateActor("payer-approver-"+uuid.NewString()[:8], "İkinci Onaylayıcı")
	f.h.CreateMembership(f.tenant, second)
	approved, err := f.invoices.ApproveSettlement(context.Background(), f.approverRC(second),
		view.Settlement.ID, view.Settlement.RowVersion)
	if err != nil {
		t.Fatalf("a second person approving: %v", err)
	}
	if approved.Settlement.CheckedBy == nil || *approved.Settlement.CheckedBy != f.approver {
		t.Errorf("checked_by = %v, want the batch's decider %s",
			approved.Settlement.CheckedBy, f.approver)
	}
}

// TestTheDeciderMayApproveBelowTheThreshold is the other side of the rule: what the threshold
// buys is a second pair of eyes on large money, not a rule that a reviewer may never see their
// own work again.
func TestTheDeciderMayApproveBelowTheThreshold(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	f.setSetting(settings.KeySettlementCheckerThreshold, `"100000"`)
	_, view := f.openSettlement(t, "STL2026008", "CLM-SETTLE-8", "500")

	approved, err := f.invoices.ApproveSettlement(context.Background(),
		f.approverRC(f.approver), view.Settlement.ID, view.Settlement.RowVersion)
	if err != nil {
		t.Fatalf("approving below the threshold: %v", err)
	}
	if approved.Settlement.CheckedBy != nil {
		t.Errorf("checked_by = %v below the threshold, want nil: recording a check that never "+
			"happened is worse than recording none", approved.Settlement.CheckedBy)
	}
}

// TestStepUpIsAskedForAboveTheThreshold is the second half of the third bullet.
func TestStepUpIsAskedForAboveTheThreshold(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	f.setSetting(settings.KeySettlementCheckerThreshold, `"100"`)
	_, view := f.openSettlement(t, "STL2026009", "CLM-SETTLE-9", "5000")

	rc := f.approverRC(f.reviewer)
	rc.StepUpValid = false
	_, err := f.invoices.ApproveSettlement(context.Background(), rc, view.Settlement.ID,
		view.Settlement.RowVersion)
	if !errors.Is(err, identity.ErrStepUpRequired) {
		t.Fatalf("approving without a step-up answered %v, want ErrStepUpRequired", err)
	}
	if got := f.settlementOf(t, view.Settlement.BatchID); got.Settlement.Status !=
		domain.SettlementPendingApproval {
		t.Errorf("the settlement is %s after the refusal, want PENDING_APPROVAL",
			got.Settlement.Status)
	}
}

// TestPaymentRecordsMoveTheStatusWithTheSum is the second bullet of section 3.
func TestPaymentRecordsMoveTheStatusWithTheSum(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	_, view := f.openSettlement(t, "STL2026020", "CLM-PAY-1", "1000")
	approved := f.approve(t, view)

	partial, err := f.invoices.CreatePaymentRecord(context.Background(), f.financeRC(f.actor),
		approved.Settlement.ID, application.CreatePaymentRecordInput{
			ExternalReference: "TRF-0001", Amount: "400", PaidAt: fixtureNow,
		})
	if err != nil {
		t.Fatalf("first payment: %v", err)
	}
	if partial.Settlement.PaidAmount != "400" {
		t.Errorf("paid amount = %s, want 400", partial.Settlement.PaidAmount)
	}
	if partial.Settlement.Status != domain.SettlementPartiallyPaid {
		t.Errorf("status = %s, want PARTIALLY_PAID", partial.Settlement.Status)
	}

	full, err := f.invoices.CreatePaymentRecord(context.Background(), f.financeRC(f.actor),
		approved.Settlement.ID, application.CreatePaymentRecordInput{
			ExternalReference: "TRF-0002", Amount: "600", PaidAt: fixtureNow,
		})
	if err != nil {
		t.Fatalf("second payment: %v", err)
	}
	if full.Settlement.PaidAmount != "1000" {
		t.Errorf("paid amount = %s, want 1000", full.Settlement.PaidAmount)
	}
	if full.Settlement.Status != domain.SettlementPaid {
		t.Errorf("status = %s, want PAID", full.Settlement.Status)
	}
	if len(full.Payments) != 2 {
		t.Errorf("%d payment records, want 2", len(full.Payments))
	}
}

// TestAPaymentBeyondThePayableAmountIsRefusedWithTheRemainder is the ceiling, and it carries
// what a clerk may still enter rather than only that this one was too much.
func TestAPaymentBeyondThePayableAmountIsRefusedWithTheRemainder(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	_, view := f.openSettlement(t, "STL2026021", "CLM-PAY-2", "1000")
	approved := f.approve(t, view)

	if _, err := f.invoices.CreatePaymentRecord(context.Background(), f.financeRC(f.actor),
		approved.Settlement.ID, application.CreatePaymentRecordInput{
			ExternalReference: "TRF-0010", Amount: "700", PaidAt: fixtureNow,
		}); err != nil {
		t.Fatalf("first payment: %v", err)
	}

	_, err := f.invoices.CreatePaymentRecord(context.Background(), f.financeRC(f.actor),
		approved.Settlement.ID, application.CreatePaymentRecordInput{
			ExternalReference: "TRF-0011", Amount: "400", PaidAt: fixtureNow,
		})
	var exceeds *application.PaymentExceedsError
	if !errors.As(err, &exceeds) {
		t.Fatalf("the second payment answered %v, want PAYMENT_EXCEEDS_SETTLEMENT", err)
	}
	if exceeds.Remainder != "300" {
		t.Errorf("the refusal says %s may still be entered, want 300", exceeds.Remainder)
	}
	after := f.settlementOf(t, view.Settlement.BatchID)
	if after.Settlement.PaidAmount != "700" {
		t.Errorf("paid amount = %s after the refusal, want 700",
			after.Settlement.PaidAmount)
	}
}

// TestTwoConcurrentPaymentRecordsCannotBothBeAccepted is the concurrent double record section 3
// names. Both transactions read the same settlement and try to pay it in full; the row lock
// serialises them and the second one is refused by the ceiling.
func TestTwoConcurrentPaymentRecordsCannotBothBeAccepted(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	_, view := f.openSettlement(t, "STL2026022", "CLM-PAY-3", "1000")
	approved := f.approve(t, view)

	type result struct {
		reference string
		err       error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	// Six hundred each against a payable thousand: whichever commits first leaves the
	// settlement PARTIALLY_PAID, so the second one meets the ceiling rather than a status
	// gate — which is the invariant this test is about.
	for _, reference := range []string{"TRF-RACE-A", "TRF-RACE-B"} {
		go func(reference string) {
			<-start
			_, err := f.invoices.CreatePaymentRecord(context.Background(),
				f.financeRC(f.actor), approved.Settlement.ID,
				application.CreatePaymentRecordInput{
					ExternalReference: reference, Amount: "600", PaidAt: fixtureNow,
				})
			results <- result{reference: reference, err: err}
		}(reference)
	}
	close(start)

	accepted, refused := 0, 0
	for range 2 {
		got := <-results
		switch {
		case got.err == nil:
			accepted++
		case errors.Is(got.err, application.ErrPaymentExceeds):
			refused++
		default:
			t.Fatalf("%s answered an unexpected error: %v", got.reference, got.err)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("%d accepted and %d refused, want exactly one of each", accepted, refused)
	}
	after := f.settlementOf(t, view.Settlement.BatchID)
	if after.Settlement.PaidAmount != "600" {
		t.Errorf("paid amount = %s, want exactly 600", after.Settlement.PaidAmount)
	}
	if len(after.Payments) != 1 {
		t.Errorf("%d payment records survived the race, want 1", len(after.Payments))
	}
}

// TestThePaymentSumIsTheDatabasesToo is the same ceiling under a psql session, which is why the
// trigger is deferred rather than the service being trusted.
func TestThePaymentSumIsTheDatabasesToo(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	_, view := f.openSettlement(t, "STL2026023", "CLM-PAY-4", "500")
	approved := f.approve(t, view)

	err := f.h.AdminExecErr(`
		INSERT INTO billing.payment_record (tenant_id, settlement_id, provider_organization_id,
		                                    external_reference, amount, currency_code, paid_at)
		VALUES ($1, $2, $3, 'TRF-DIRECT', 900, 'TRY', clock_timestamp())`,
		f.tenant, approved.Settlement.ID, f.provider)
	if err == nil {
		t.Fatal("the database accepted a payment record above the payable amount")
	}
	if !strings.Contains(err.Error(), "payment records on settlement") &&
		!strings.Contains(err.Error(), "paid amount says") {
		t.Errorf("refused by %v, want the deferred ceiling trigger", err)
	}
}

// TestTheSameExternalReferenceCannotBeEnteredTwice is the uniqueness rule: the same transfer
// entered twice is how a settlement comes to look paid when half of it was not.
func TestTheSameExternalReferenceCannotBeEnteredTwice(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	_, view := f.openSettlement(t, "STL2026024", "CLM-PAY-5", "500")
	approved := f.approve(t, view)

	if _, err := f.invoices.CreatePaymentRecord(context.Background(), f.financeRC(f.actor),
		approved.Settlement.ID, application.CreatePaymentRecordInput{
			ExternalReference: "TRF-DUP", Amount: "100", PaidAt: fixtureNow,
		}); err != nil {
		t.Fatalf("first payment: %v", err)
	}
	_, err := f.invoices.CreatePaymentRecord(context.Background(), f.financeRC(f.actor),
		approved.Settlement.ID, application.CreatePaymentRecordInput{
			ExternalReference: "TRF-DUP", Amount: "100", PaidAt: fixtureNow,
		})
	if !errors.Is(err, application.ErrPaymentReferenceTaken) {
		t.Fatalf("the repeated reference answered %v, want PAYMENT_REFERENCE_TAKEN", err)
	}
}

// TestACancelledSettlementIsReplacedByANewVersion is section 2.2's last sentence.
func TestACancelledSettlementIsReplacedByANewVersion(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	old := f.approvedClaim(t, "CLM-CANCEL-OLD", "400")
	f.recovery(t, old, "50")
	batch, view := f.openSettlement(t, "STL2026030", "CLM-CANCEL-1", "800")
	if view.Settlement.WithheldAmount != "50" {
		t.Fatalf("the first settlement withheld %s, want 50", view.Settlement.WithheldAmount)
	}

	cancelled, err := f.invoices.CancelSettlement(context.Background(),
		f.approverRC(f.approver), view.Settlement.ID, "WRONG_TOTAL", nil,
		view.Settlement.RowVersion)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Settlement.Status != domain.SettlementCancelled {
		t.Fatalf("status = %s, want CANCELLED", cancelled.Settlement.Status)
	}
	if len(cancelled.Recoveries) != 0 {
		t.Errorf("the cancelled settlement still holds %d recoveries, want none: they are open "+
			"again", len(cancelled.Recoveries))
	}

	// The replacement opens on the same batch, as version 2, and nets the recovery the
	// cancelled one had let go.
	if err := f.deliverBatchDecided(t, batch.Batch.ID); err != nil {
		t.Fatalf("redeliver batch.decided: %v", err)
	}
	replacement := f.settlementOf(t, batch.Batch.ID)
	if replacement.Settlement.ID == view.Settlement.ID {
		t.Fatal("the cancelled settlement is still the live one")
	}
	if replacement.Settlement.VersionNo != 2 {
		t.Errorf("the replacement is version %d, want 2", replacement.Settlement.VersionNo)
	}
	if replacement.Settlement.WithheldAmount != "50" {
		t.Errorf("the replacement withheld %s, want the released 50",
			replacement.Settlement.WithheldAmount)
	}
}

// TestASettlementWithPaymentsCannotBeCancelled: the money has moved and the answer is a dispute
// on the record rather than a rewrite of the header.
func TestASettlementWithPaymentsCannotBeCancelled(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	_, view := f.openSettlement(t, "STL2026031", "CLM-CANCEL-2", "300")
	approved := f.approve(t, view)
	paid, err := f.invoices.CreatePaymentRecord(context.Background(), f.financeRC(f.actor),
		approved.Settlement.ID, application.CreatePaymentRecordInput{
			ExternalReference: "TRF-NOCANCEL", Amount: "100", PaidAt: fixtureNow,
		})
	if err != nil {
		t.Fatalf("payment: %v", err)
	}

	_, err = f.invoices.CancelSettlement(context.Background(), f.approverRC(f.approver),
		paid.Settlement.ID, "WRONG_TOTAL", nil, paid.Settlement.RowVersion)
	if !errors.Is(err, application.ErrSettlementHasPayments) {
		t.Fatalf("cancelling a paid settlement answered %v, want SETTLEMENT_HAS_PAYMENTS", err)
	}
}

// TestSettlementAuditKeysAreSnakeCase: `audit.SanitizeDetail` drops anything else silently, so
// a camelCase key here would be an audit row that recorded nothing and said so to nobody.
func TestSettlementAuditKeysAreSnakeCase(t *testing.T) {
	f := newFixture(t)
	f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	_, view := f.openSettlement(t, "STL2026040", "CLM-AUDIT-1", "600")
	approved := f.approve(t, view)
	if _, err := f.invoices.CreatePaymentRecord(context.Background(), f.financeRC(f.actor),
		approved.Settlement.ID, application.CreatePaymentRecordInput{
			ExternalReference: "TRF-AUDIT", Amount: "600", PaidAt: fixtureNow,
		}); err != nil {
		t.Fatalf("payment: %v", err)
	}

	for _, action := range []string{"settlement.open", "settlement.approve",
		"settlement.payment.record"} {
		rows := f.settlementAudit(t, view.Settlement.ID, action)
		if len(rows) == 0 {
			t.Fatalf("no audit row for %s", action)
		}
		for _, detail := range rows {
			if len(detail) == 0 {
				t.Fatalf("%s wrote an empty detail map: every key was dropped", action)
			}
			for key := range detail {
				if !isSnakeCase(key) {
					t.Errorf("%s wrote key %q, which audit.SanitizeDetail drops", action, key)
				}
			}
		}
	}
}

// approve releases a settlement as an approver who did not decide the batch, so a test that is
// about something else does not have to think about the maker-checker rule.
func (f *fixture) approve(t *testing.T, view application.SettlementView,
) application.SettlementView {
	t.Helper()
	actor := f.h.CreateActor("releaser-"+uuid.NewString()[:8], "Serbest Bırakan")
	f.h.CreateMembership(f.tenant, actor)
	approved, err := f.invoices.ApproveSettlement(context.Background(), f.approverRC(actor),
		view.Settlement.ID, view.Settlement.RowVersion)
	if err != nil {
		t.Fatalf("approve settlement: %v", err)
	}
	return approved
}

// contains is `slices.Contains` spelled for the two places this file needs it.
func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// isSnakeCase mirrors `audit.SanitizeDetail`'s own rule.
func isSnakeCase(key string) bool {
	if key == "" || key[0] < 'a' || key[0] > 'z' {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			continue
		}
		return false
	}
	return true
}
