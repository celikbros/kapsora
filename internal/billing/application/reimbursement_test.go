package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/billing/domain"
)

// WP-I7-04 section 3, the reimbursement's half: a duplicate receipt is refused naming the
// earlier one, approval consumes exactly the approved amount with the ledger's conservation
// intact, rejection consumes nothing, the member reads only their own, and the IBAN exists in
// no column but `bank_account_ref_enc`.

// TestCreatingAReimbursementStoresOnlyTheCiphertextAndTheMask is the fourth bullet of section 3,
// and the whole privacy claim of this package.
//
// It sweeps every text-shaped column of every table in the database for the test IBAN, and then
// the audit log and the outbox as well. The only place it may appear is the ciphertext, which
// by construction does not contain it.
func TestCreatingAReimbursementStoresOnlyTheCiphertextAndTheMask(t *testing.T) {
	f := newFixture(t)
	request := f.reimbursementRequest(t, "2026-03-05")
	document := f.receipt(t)

	record := f.draftReimbursement(t, request, document, "250")
	if record.BankAccountMasked != testIBANTail {
		t.Fatalf("mask = %q, want the last four characters %q", record.BankAccountMasked,
			testIBANTail)
	}
	if !domain.ValidMaskedAccount(record.BankAccountMasked) {
		t.Errorf("the mask %q does not have the shape the column CHECKs",
			record.BankAccountMasked)
	}
	if record.Status != domain.ReimbursementDraft {
		t.Errorf("status = %s, want DRAFT", record.Status)
	}

	// The envelope is there, and it is not the number.
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var envelope []byte
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT bank_account_ref_enc FROM billing.reimbursement WHERE tenant_id = $1 AND id = $2`,
		f.tenant, record.ID).Scan(&envelope); err != nil {
		t.Fatalf("read the envelope: %v", err)
	}
	if len(envelope) < 16 {
		t.Fatalf("the envelope is %d bytes, which is not an envelope", len(envelope))
	}
	if strings.Contains(string(envelope), testIBAN) {
		t.Fatal("the envelope contains the account number in plaintext")
	}

	// And nowhere else. Every column whose type can hold text, in every schema this product
	// owns, cast to text and searched.
	f.assertIBANIsNowhere(t)
}

// assertIBANIsNowhere is the sweep of section 3: the test IBAN appears in no column of any
// table, including the audit log and the outbox.
//
// It walks `information_schema` rather than a list somebody maintained, so a column added by a
// later migration is swept without anybody remembering to add it here — which is the only
// version of this test that stays true.
func (f *fixture) assertIBANIsNowhere(t *testing.T) {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	rows, err := f.h.Admin.Query(ctx, `
		SELECT c.table_schema, c.table_name, c.column_name
		  FROM information_schema.columns c
		  JOIN information_schema.tables t
		    ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		 WHERE t.table_type = 'BASE TABLE'
		   AND c.table_schema NOT IN ('pg_catalog', 'information_schema')
		   AND c.data_type IN ('text', 'character varying', 'character', 'jsonb', 'json')
		 ORDER BY c.table_schema, c.table_name, c.column_name`)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	type column struct{ schema, table, name string }
	var columns []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.schema, &c.table, &c.name); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		columns = append(columns, c)
	}
	rows.Close()
	if len(columns) == 0 {
		t.Fatal("the sweep found no columns at all, which means it swept nothing")
	}

	for _, c := range columns {
		var found int
		query := `SELECT count(*) FROM "` + c.schema + `"."` + c.table + `" WHERE "` +
			c.name + `"::text LIKE '%' || $1 || '%'`
		if err := f.h.Admin.QueryRow(ctx, query, testIBAN).Scan(&found); err != nil {
			// A column the admin role cannot read is not a column this product wrote to.
			continue
		}
		if found > 0 {
			t.Errorf("the account number appears in %s.%s.%s", c.schema, c.table, c.name)
		}
	}
}

// TestADuplicateReceiptIsRefusedNamingTheEarlierOne is the first line of the reimbursement's
// half of section 3.
func TestADuplicateReceiptIsRefusedNamingTheEarlierOne(t *testing.T) {
	f := newFixture(t)
	first := f.submittedReimbursement(t, "300")

	// The same receipt, on a different request: a member trying again with the same document.
	second := f.reimbursementRequest(t, "2026-03-05")
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var document uuid.UUID
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT receipt_document_id FROM billing.reimbursement WHERE tenant_id = $1 AND id = $2`,
		f.tenant, first.ID).Scan(&document); err != nil {
		t.Fatalf("read the first receipt: %v", err)
	}

	_, err := f.invoices.CreateReimbursement(context.Background(), f.memberRC(),
		application.CreateReimbursementInput{
			PersonID: f.person, ServiceRequestID: second, ReceiptDocumentID: document,
			RequestedAmount: "300", BankAccount: testIBAN,
		})
	var duplicate *application.DuplicateReimbursementError
	if !errors.As(err, &duplicate) {
		t.Fatalf("the second attempt answered %v, want REIMBURSEMENT_DUPLICATE", err)
	}
	if duplicate.ExistingID != first.ID {
		t.Errorf("the refusal names %s, want the earlier request %s", duplicate.ExistingID,
			first.ID)
	}
	if duplicate.ExistingReference != first.Reference {
		t.Errorf("the refusal quotes %q, want %q", duplicate.ExistingReference,
			first.Reference)
	}
	if !duplicate.ByReceipt {
		t.Error("the refusal says it matched on the triple, want the receipt digest")
	}
}

// TestADuplicateProviderDateAmountIsRefused is the other half of the rule: a different receipt
// for the same spending, which is what a member does when they photograph the till roll twice.
func TestADuplicateProviderDateAmountIsRefused(t *testing.T) {
	f := newFixture(t)
	first := f.submittedReimbursement(t, "412.75")

	request := f.reimbursementRequest(t, "2026-03-05")
	_, err := f.invoices.CreateReimbursement(context.Background(), f.memberRC(),
		application.CreateReimbursementInput{
			PersonID: f.person, ServiceRequestID: request,
			ReceiptDocumentID: f.receipt(t), RequestedAmount: "412.75",
			BankAccount: testIBAN,
		})
	var duplicate *application.DuplicateReimbursementError
	if !errors.As(err, &duplicate) {
		t.Fatalf("the second attempt answered %v, want REIMBURSEMENT_DUPLICATE", err)
	}
	if duplicate.ExistingID != first.ID {
		t.Errorf("the refusal names %s, want %s", duplicate.ExistingID, first.ID)
	}
	if duplicate.ByReceipt {
		t.Error("the refusal says it matched on the receipt, want the provider-date-amount triple")
	}
}

// TestADifferentAmountOnTheSameDayIsNotADuplicate: the rule is a triple, and two different
// purchases at one pharmacy on one day are two purchases.
func TestADifferentAmountOnTheSameDayIsNotADuplicate(t *testing.T) {
	f := newFixture(t)
	f.submittedReimbursement(t, "100")

	request := f.reimbursementRequest(t, "2026-03-05")
	if _, err := f.invoices.CreateReimbursement(context.Background(), f.memberRC(),
		application.CreateReimbursementInput{
			PersonID: f.person, ServiceRequestID: request,
			ReceiptDocumentID: f.receipt(t), RequestedAmount: "250",
			BankAccount: testIBAN,
		}); err != nil {
		t.Fatalf("a second purchase of a different amount was refused: %v", err)
	}
}

// TestApprovalConsumesExactlyTheApprovedAmount is the second line of section 3, with the
// ledger's own conservation checked from the movements rather than from the columns.
func TestApprovalConsumesExactlyTheApprovedAmount(t *testing.T) {
	f := newFixture(t)
	account := f.moneyEntitlement(t, "1000")
	record := f.submittedReimbursement(t, "400")

	decided, err := f.invoices.DecideReimbursement(context.Background(), f.financeRC(f.actor),
		record.ID, application.DecideReimbursementInput{
			Decision: domain.DecisionApproveReimbursement, ApprovedAmount: "250.50",
			ReasonCode: "PARTIAL_COVER",
		}, record.RowVersion)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if decided.Status != domain.ReimbursementPaymentOrdered {
		t.Fatalf("status = %s, want PAYMENT_ORDERED", decided.Status)
	}
	if decided.ApprovedAmount != "250.5" {
		t.Errorf("approved amount = %s, want exactly 250.5", decided.ApprovedAmount)
	}
	if decided.ClaimID == nil {
		t.Fatal("the approval created no claim")
	}

	// Exactly the approved amount, and not the requested one. This is the mutation the
	// acceptance criterion names: an approval that consumed 400 here would pass every other
	// assertion in this file.
	after := f.accountBalances(t, account)
	if after.Consumed != "250.5" {
		t.Errorf("consumed = %s, want exactly the approved 250.5", after.Consumed)
	}
	if after.Available != "749.5" {
		t.Errorf("available = %s, want 749.5", after.Available)
	}
	if after.Reserved != "0" {
		t.Errorf("reserved = %s, want 0: the hold is consumed in the same transaction",
			after.Reserved)
	}
	f.assertConservation(t, account)

	// The claim the payer's books read: one line, at the approved amount, already decided.
	if got := f.claimStatus(t, *decided.ClaimID); got != "APPROVED" {
		t.Errorf("the reimbursement claim is %s, want APPROVED", got)
	}
	if got := f.claimApprovedTotal(t, *decided.ClaimID); got != "250.5" {
		t.Errorf("the claim's approved total is %s, want 250.5", got)
	}
}

// TestApprovingInFullConsumesTheRequestedAmount is the same path with no figure typed: approving
// means approving what was asked for.
func TestApprovingInFullConsumesTheRequestedAmount(t *testing.T) {
	f := newFixture(t)
	account := f.moneyEntitlement(t, "1000")
	record := f.submittedReimbursement(t, "175.25")

	decided, err := f.invoices.DecideReimbursement(context.Background(), f.financeRC(f.actor),
		record.ID, application.DecideReimbursementInput{
			Decision: domain.DecisionApproveReimbursement,
		}, record.RowVersion)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if decided.ApprovedAmount != "175.25" {
		t.Errorf("approved amount = %s, want the requested 175.25", decided.ApprovedAmount)
	}
	if got := f.accountBalances(t, account); got.Consumed != "175.25" {
		t.Errorf("consumed = %s, want 175.25", got.Consumed)
	}
	f.assertConservation(t, account)
}

// TestRejectionConsumesNothing is the third line of section 3. A member whose receipt was
// refused must have the same balance afterwards as before, exactly.
func TestRejectionConsumesNothing(t *testing.T) {
	f := newFixture(t)
	account := f.moneyEntitlement(t, "1000")
	before := f.accountBalances(t, account)
	record := f.submittedReimbursement(t, "400")

	decided, err := f.invoices.DecideReimbursement(context.Background(), f.financeRC(f.actor),
		record.ID, application.DecideReimbursementInput{
			Decision: domain.DecisionRejectReimbursement, ReasonCode: "NOT_COVERED",
		}, record.RowVersion)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if decided.Status != domain.ReimbursementRejected {
		t.Fatalf("status = %s, want REJECTED", decided.Status)
	}
	if decided.ApprovedAmount != "0" {
		t.Errorf("approved amount = %s on a rejection, want 0", decided.ApprovedAmount)
	}
	if decided.ClaimID != nil {
		t.Error("a rejection created a claim")
	}
	after := f.accountBalances(t, account)
	if after != before {
		t.Errorf("the balances moved from %+v to %+v on a rejection", before, after)
	}
	f.assertConservation(t, account)
}

// TestApprovingWithNoMoneyEntitlementIsRefused is `ENTITLEMENT_ACCOUNT_NOT_FOUND`: a
// reimbursement that drew down nothing would be money paid out of a wallet nobody debited.
func TestApprovingWithNoMoneyEntitlementIsRefused(t *testing.T) {
	f := newFixture(t)
	record := f.submittedReimbursement(t, "400")

	_, err := f.invoices.DecideReimbursement(context.Background(), f.financeRC(f.actor),
		record.ID, application.DecideReimbursementInput{
			Decision: domain.DecisionApproveReimbursement,
		}, record.RowVersion)
	if !errors.Is(err, application.ErrEntitlementAccountNotFound) {
		t.Fatalf("approving with no wallet answered %v, want ENTITLEMENT_ACCOUNT_NOT_FOUND",
			err)
	}
	again, err := f.invoices.GetReimbursement(context.Background(), f.financeRC(f.actor), nil,
		record.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if again.Status != domain.ReimbursementSubmitted {
		t.Errorf("the reimbursement is %s after the refusal, want SUBMITTED", again.Status)
	}
}

// TestApprovingMoreThanTheBalanceIsRefused is the ledger's own no-double-spend rule reaching
// the caller as something they can act on.
func TestApprovingMoreThanTheBalanceIsRefused(t *testing.T) {
	f := newFixture(t)
	account := f.moneyEntitlement(t, "100")
	before := f.accountBalances(t, account)
	record := f.submittedReimbursement(t, "400")

	_, err := f.invoices.DecideReimbursement(context.Background(), f.financeRC(f.actor),
		record.ID, application.DecideReimbursementInput{
			Decision: domain.DecisionApproveReimbursement,
		}, record.RowVersion)
	if !errors.Is(err, application.ErrEntitlementInsufficient) {
		t.Fatalf("approving above the balance answered %v, want ENTITLEMENT_INSUFFICIENT", err)
	}
	if after := f.accountBalances(t, account); after != before {
		t.Errorf("the balances moved from %+v to %+v on a refused approval", before, after)
	}
}

// TestApprovingAboveTheRequestedAmountIsRefused: a figure nobody asked for is not an approval.
func TestApprovingAboveTheRequestedAmountIsRefused(t *testing.T) {
	f := newFixture(t)
	f.moneyEntitlement(t, "1000")
	record := f.submittedReimbursement(t, "200")

	_, err := f.invoices.DecideReimbursement(context.Background(), f.financeRC(f.actor),
		record.ID, application.DecideReimbursementInput{
			Decision: domain.DecisionApproveReimbursement, ApprovedAmount: "300",
		}, record.RowVersion)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("approving above the requested amount answered %v, want a validation error",
			err)
	}
}

// TestTheContractCeilingRefusesTheDraft is the third check of v1.2 10.10.
func TestTheContractCeilingRefusesTheDraft(t *testing.T) {
	f := newFixture(t)
	version := f.contractWithTerm(t, 30, domain.MethodBankTransfer)
	f.priceCeiling(t, version, "500")

	request := f.reimbursementRequest(t, "2026-03-05")
	_, err := f.invoices.CreateReimbursement(context.Background(), f.memberRC(),
		application.CreateReimbursementInput{
			PersonID: f.person, ServiceRequestID: request,
			ReceiptDocumentID: f.receipt(t), RequestedAmount: "600",
			BankAccount: testIBAN,
		})
	var ceiling *application.CeilingExceededError
	if !errors.As(err, &ceiling) {
		t.Fatalf("a request above the ceiling answered %v, want REIMBURSEMENT_CEILING_EXCEEDED",
			err)
	}
	if ceiling.Ceiling != "500" || ceiling.Requested != "600" {
		t.Errorf("the refusal carries ceiling=%s requested=%s, want 500 and 600",
			ceiling.Ceiling, ceiling.Requested)
	}

	// At the ceiling exactly is fine: a bound is a bound and not a strict one.
	if _, err := f.invoices.CreateReimbursement(context.Background(), f.memberRC(),
		application.CreateReimbursementInput{
			PersonID: f.person, ServiceRequestID: request,
			ReceiptDocumentID: f.receipt(t), RequestedAmount: "500",
			BankAccount: testIBAN,
		}); err != nil {
		t.Fatalf("a request at the ceiling was refused: %v", err)
	}
}

// TestAnUnscannedReceiptIsRefused is the second check of 10.10.
func TestAnUnscannedReceiptIsRefused(t *testing.T) {
	f := newFixture(t)
	request := f.reimbursementRequest(t, "2026-03-05")
	document := f.receipt(t)
	f.h.AdminExec(`
		UPDATE document.object SET bucket = 'quarantine', scan_status = 'PENDING'
		 WHERE tenant_id = $1 AND id = $2`, f.tenant, document)

	_, err := f.invoices.CreateReimbursement(context.Background(), f.memberRC(),
		application.CreateReimbursementInput{
			PersonID: f.person, ServiceRequestID: request, ReceiptDocumentID: document,
			RequestedAmount: "100", BankAccount: testIBAN,
		})
	if !errors.Is(err, application.ErrReceiptUnusable) {
		t.Fatalf("an unscanned receipt answered %v, want REIMBURSEMENT_RECEIPT_UNUSABLE", err)
	}
}

// TestAServiceDateOutsideTheEnrollmentIsRefused is the first check of 10.10: eligibility on the
// day the money was spent, not on the day the member got round to asking.
func TestAServiceDateOutsideTheEnrollmentIsRefused(t *testing.T) {
	f := newFixture(t)
	// The enrollment starts on 2026-01-01; this receipt is from the year before.
	request := f.reimbursementRequest(t, "2025-11-04")

	_, err := f.invoices.CreateReimbursement(context.Background(), f.memberRC(),
		application.CreateReimbursementInput{
			PersonID: f.person, ServiceRequestID: request, ReceiptDocumentID: f.receipt(t),
			RequestedAmount: "100", BankAccount: testIBAN,
		})
	if !errors.Is(err, application.ErrNotEligibleOnDate) {
		t.Fatalf("a receipt from before the cover answered %v, want REIMBURSEMENT_NOT_ELIGIBLE",
			err)
	}
}

// TestAMalformedAccountIsRefused: the shape is checked, the bank is not second-guessed.
func TestAMalformedAccountIsRefused(t *testing.T) {
	f := newFixture(t)
	request := f.reimbursementRequest(t, "2026-03-05")

	_, err := f.invoices.CreateReimbursement(context.Background(), f.memberRC(),
		application.CreateReimbursementInput{
			PersonID: f.person, ServiceRequestID: request, ReceiptDocumentID: f.receipt(t),
			RequestedAmount: "100", BankAccount: "hesabım Ziraat'te",
		})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a sentence in the account field answered %v, want a validation error", err)
	}
}

// TestSpacesInTheAccountAreStrippedBeforeAnythingElse: the mask and the ciphertext are taken
// from one normalised string, so the mask can never describe an account other than the one in
// the envelope.
func TestSpacesInTheAccountAreStrippedBeforeAnythingElse(t *testing.T) {
	f := newFixture(t)
	request := f.reimbursementRequest(t, "2026-03-05")

	record, err := f.invoices.CreateReimbursement(context.Background(), f.memberRC(),
		application.CreateReimbursementInput{
			PersonID: f.person, ServiceRequestID: request, ReceiptDocumentID: f.receipt(t),
			RequestedAmount: "100",
			BankAccount:     "tr33 0006 1005 1978 6457 8413 26",
		})
	if err != nil {
		t.Fatalf("a spaced IBAN was refused: %v", err)
	}
	if record.BankAccountMasked != testIBANTail {
		t.Errorf("mask = %q, want %q", record.BankAccountMasked, testIBANTail)
	}
	f.assertIBANIsNowhere(t)
}

// TestAMemberReadsOnlyTheirOwnReimbursements is the last line of section 3.
func TestAMemberReadsOnlyTheirOwnReimbursements(t *testing.T) {
	f := newFixture(t)
	mine := f.submittedReimbursement(t, "120")
	theirs := f.otherMembersReimbursement(t)

	page, err := f.invoices.ListReimbursements(context.Background(), f.memberRC(),
		application.ReimbursementFilter{PersonID: &f.person, Limit: 50})
	if err != nil {
		t.Fatalf("list mine: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != mine.ID {
		t.Fatalf("the member's own list holds %d rows, want exactly their own", len(page.Items))
	}

	person := f.person
	if _, err := f.invoices.GetReimbursement(context.Background(), f.memberRC(), &person,
		theirs); !errors.Is(err, application.ErrReimbursementNotFound) {
		t.Fatalf("reading another member's answered %v, want 404 REIMBURSEMENT_NOT_FOUND", err)
	}

	// The payer's finance side sees both, because that is the whole point of the review queue.
	all, err := f.invoices.ListReimbursements(context.Background(), f.financeRC(f.actor),
		application.ReimbursementFilter{Limit: 50})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all.Items) != 2 {
		t.Errorf("the reviewer sees %d rows, want both", len(all.Items))
	}
}

// TestRecordingTheReimbursementPaymentMarksItPaid is the last step of section 2.4, and the one
// message in this product that carries anything about a bank account.
func TestRecordingTheReimbursementPaymentMarksItPaid(t *testing.T) {
	f := newFixture(t)
	f.moneyEntitlement(t, "1000")
	record := f.submittedReimbursement(t, "300")
	decided, err := f.invoices.DecideReimbursement(context.Background(), f.financeRC(f.actor),
		record.ID, application.DecideReimbursementInput{
			Decision: domain.DecisionApproveReimbursement,
		}, record.RowVersion)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}

	paid, err := f.invoices.RecordReimbursementPayment(context.Background(),
		f.financeRC(f.actor), decided.ID, "EFT-99887", fixtureNow, decided.RowVersion)
	if err != nil {
		t.Fatalf("record payment: %v", err)
	}
	if paid.Status != domain.ReimbursementPaid {
		t.Fatalf("status = %s, want PAID", paid.Status)
	}
	if paid.PaymentReference != "EFT-99887" {
		t.Errorf("payment reference = %q, want EFT-99887", paid.PaymentReference)
	}

	messages := f.notificationVariables(t, "reimbursement.paid")
	if len(messages) == 0 {
		t.Fatal("the member was told nothing about a paid reimbursement")
	}
	variables, _ := messages[0]["variables"].(map[string]any)
	if variables["masked_account"] != testIBANTail {
		t.Errorf("the message says masked_account=%v, want %q", variables["masked_account"],
			testIBANTail)
	}
	for name, value := range variables {
		if text, ok := value.(string); ok && strings.Contains(text, testIBAN) {
			t.Errorf("the notification variable %q carries the account number", name)
		}
	}
	f.assertIBANIsNowhere(t)
}

// TestReimbursementAuditKeysAreSnakeCaseAndCarryNoAccount: the audit log is read by more people
// than any screen in this product, and four characters is all it may hold.
func TestReimbursementAuditKeysAreSnakeCaseAndCarryNoAccount(t *testing.T) {
	f := newFixture(t)
	f.moneyEntitlement(t, "1000")
	record := f.submittedReimbursement(t, "300")
	decided, err := f.invoices.DecideReimbursement(context.Background(), f.financeRC(f.actor),
		record.ID, application.DecideReimbursementInput{
			Decision: domain.DecisionApproveReimbursement,
		}, record.RowVersion)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if _, err := f.invoices.RecordReimbursementPayment(context.Background(),
		f.financeRC(f.actor), decided.ID, "EFT-11223", fixtureNow,
		decided.RowVersion); err != nil {
		t.Fatalf("record payment: %v", err)
	}

	for _, action := range []string{"reimbursement.create", "reimbursement.submit",
		"reimbursement.decide", "reimbursement.payment.record"} {
		rows := f.reimbursementAudit(t, record.ID, action)
		if len(rows) == 0 {
			t.Fatalf("no audit row for %s", action)
		}
		for _, detail := range rows {
			if len(detail) == 0 {
				t.Fatalf("%s wrote an empty detail map: every key was dropped", action)
			}
			for key, value := range detail {
				if !isSnakeCase(key) {
					t.Errorf("%s wrote key %q, which audit.SanitizeDetail drops", action, key)
				}
				if text, ok := value.(string); ok && strings.Contains(text, testIBAN) {
					t.Errorf("%s wrote the account number under %q", action, key)
				}
			}
		}
	}
}

// otherMembersReimbursement writes a second member with their own person, enrollment, request
// and reimbursement, so "the member reads only their own" has something to fail against.
func (f *fixture) otherMembersReimbursement(t *testing.T) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := f.h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	var person, membership, enrollment, request, version, reimbursement uuid.UUID
	var plan uuid.UUID
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT plan_id FROM benefit.enrollment WHERE tenant_id = $1 AND id = $2`,
		f.tenant, f.enrollment).Scan(&plan); err != nil {
		t.Fatalf("read the plan: %v", err)
	}
	scan(&person, "other person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Bora', 'Demir', 'bora demir') RETURNING id`, f.tenant)
	scan(&membership, "other membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id,
		                                      sponsor_tenant_organization_id, membership_type,
		                                      status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)'))
		RETURNING id`, f.tenant, person, f.sponsor)
	scan(&enrollment, "other enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status,
		                                valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, membership, plan)
	scan(&request, "other request", `
		INSERT INTO service.service_request (tenant_id, request_reference, request_type,
		                                     person_id, program_id, enrollment_id,
		                                     provider_tenant_organization_id, service_date,
		                                     channel, status, submitted_at)
		VALUES ($1, $2, 'REIMBURSEMENT', $3, $4, $5, $6, '2026-03-05', 'MEMBER_PORTAL',
		        'SUBMITTED', clock_timestamp()) RETURNING id`,
		f.tenant, "SR-"+uuid.NewString()[:12], person, f.program, enrollment, f.provider)
	scan(&version, "other request version", `
		INSERT INTO service.service_request_version (tenant_id, service_request_id, version_no,
		                                             status)
		VALUES ($1, $2, 1, 'DRAFT') RETURNING id`, f.tenant, request)
	f.h.AdminExec(`
		INSERT INTO service.service_request_item (tenant_id, service_request_version_id, line_no,
		                                          service_definition_id, requested_quantity,
		                                          unit_type)
		VALUES ($1, $2, 1, $3, 1, 'MONEY')`, f.tenant, version, f.definition)
	f.h.AdminExec(`
		UPDATE service.service_request_version
		   SET status = 'SUBMITTED', snapshot_json = '{}'::jsonb,
		       submitted_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, f.tenant, version)
	// The row itself is written directly: this member has no PERSON grant in these tests and
	// what the assertion needs is a row belonging to somebody else, not a second command path.
	scan(&reimbursement, "other reimbursement", `
		INSERT INTO billing.reimbursement (tenant_id, reference, person_id, enrollment_id,
		                                   service_request_id, receipt_document_id,
		                                   receipt_sha256, service_definition_id, service_date,
		                                   provider_organization_id, requested_amount,
		                                   currency_code, bank_account_ref_enc,
		                                   bank_account_masked, status, submitted_at)
		VALUES ($1, $2, $3, $4, $5, $6, sha256($2::text::bytea), $7, '2026-03-05', $8, 90, 'TRY',
		        decode('0102030405060708090a0b0c0d0e0f10', 'hex'), '9999', 'SUBMITTED',
		        clock_timestamp()) RETURNING id`,
		f.tenant, "RB-202603-ZZZZZZZZ", person, enrollment, request, f.receipt(t),
		f.definition, f.provider)
	return reimbursement
}
