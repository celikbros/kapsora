package dbtests

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// The settlement, payment-record and reimbursement schema of migration 000046 (WP-I7-04), with
// the application layer bypassed entirely: every statement below runs as the schema owner
// through the admin pool, so no Go code of ours is between them and the constraint.
//
// These are the rules the service is allowed to lean on, and a rule the service can forget is
// not a rule. Four carry the package: a settlement never exceeds the batch's approved total and
// its figure freezes when it leaves DRAFT, one batch has one live settlement, the payment
// records sum to the settlement's own stored figure and never above what is payable, and a
// reimbursement's bank reference is a ciphertext beside four characters and nothing else.

// settlementSeed is a batchSeed with a decided icmal and a settlement opened on it.
type settlementSeed struct {
	batchSeed
	settlement uuid.UUID
}

// seedSettlement decides the seeded batch and opens one PENDING_APPROVAL settlement on it.
func (s batchSeed) seedSettlement(t *testing.T, h *dbtest.Harness) settlementSeed {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	out := settlementSeed{batchSeed: s}

	// The batch has to be DECIDED for its totals to reconcile, and the two members have to
	// carry decisions for the reconciliation CHECK to hold.
	h.AdminExec(`
		UPDATE billing.batch_invoice
		   SET decision = 'APPROVE', approved_amount = submitted_amount, decided_by = $2,
		       decided_at = clock_timestamp()
		 WHERE tenant_id = $1 AND batch_id = $3`, s.tenant, s.actor, s.batch)
	h.AdminExec(`
		UPDATE billing.batch
		   SET status = 'DECIDED', invoice_count = 2, submitted_total = 1500,
		       approved_total = 1500, submitted_at = clock_timestamp(), submitted_by = $2,
		       decided_at = clock_timestamp(), decided_by = $2
		 WHERE tenant_id = $1 AND id = $3`, s.tenant, s.actor, s.batch)

	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO billing.settlement (tenant_id, reference, batch_id, version_no,
		                                provider_organization_id, currency_code,
		                                approved_amount, withheld_amount, payable_amount,
		                                due_date, settlement_method, status)
		VALUES ($1, $2, $3, 1, $4, 'TRY', 1500, 100, 1400, '2026-04-30', 'BANK_TRANSFER',
		        'PENDING_APPROVAL') RETURNING id`,
		s.tenant, "ST-202603-"+batchTail(), s.batch, s.provider).Scan(&out.settlement); err != nil {
		t.Fatalf("insert the settlement: %v", err)
	}
	return out
}

// TestPayableNeverExceedsTheApprovedAmount is the first rule, as a CHECK: a settlement whose
// payable figure is above what the batch approved cannot be written at all.
func TestPayableNeverExceedsTheApprovedAmount(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h).seedBatch(t, h).seedSettlement(t, h)

	err := h.AdminExecErr(`
		INSERT INTO billing.settlement (tenant_id, reference, batch_id, version_no,
		                                provider_organization_id, currency_code,
		                                approved_amount, withheld_amount, payable_amount,
		                                due_date, settlement_method, status)
		VALUES ($1, $2, $3, 2, $4, 'TRY', 1500, -100, 1600, '2026-04-30', 'BANK_TRANSFER',
		        'CANCELLED')`,
		s.tenant, "ST-202603-"+batchTail(), s.batch, s.provider)
	if err == nil {
		t.Fatal("a settlement above the batch's approved total was accepted")
	}
	if !strings.Contains(err.Error(), "ck_billing_settlement_payable") &&
		!strings.Contains(err.Error(), "ck_billing_settlement_amounts_sign") {
		t.Errorf("refused by %v, want the payable CHECK", err)
	}

	// And the arithmetic has to hold as well as the inequality: a payable figure below the
	// difference is just as wrong as one above the approved total.
	err = h.AdminExecErr(`
		UPDATE billing.settlement SET payable_amount = 1200
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.settlement)
	if err == nil {
		t.Fatal("a payable amount that is not approved minus withheld was accepted")
	}
}

// TestApprovedAmountIsFrozenAfterDraft is the second half of the same rule, and it is a trigger
// because it is about what the row *was*.
func TestApprovedAmountIsFrozenAfterDraft(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h).seedBatch(t, h).seedSettlement(t, h)

	// Both money columns move together, so the payable CHECK cannot be what refuses this.
	err := h.AdminExecErr(`
		UPDATE billing.settlement
		   SET approved_amount = 2000, payable_amount = 1900
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.settlement)
	if err == nil {
		t.Fatal("the approved amount moved after the settlement left DRAFT")
	}
	if !strings.Contains(err.Error(), "frozen") {
		t.Errorf("refused by %v, want the freeze trigger", err)
	}

	// The lifecycle still moves, which is the whole point of freezing the figures rather than
	// the row.
	h.AdminExec(`
		UPDATE billing.settlement SET status = 'APPROVED', approved_by = $2,
		                              approved_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $3`, s.tenant, s.actor, s.settlement)
}

// TestOneBatchHasOneLiveSettlement is what makes a redelivered `batch.decided` harmless however
// carefully the handler looks first.
func TestOneBatchHasOneLiveSettlement(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h).seedBatch(t, h).seedSettlement(t, h)

	err := h.AdminExecErr(`
		INSERT INTO billing.settlement (tenant_id, reference, batch_id, version_no,
		                                provider_organization_id, currency_code,
		                                approved_amount, withheld_amount, payable_amount,
		                                due_date, settlement_method, status)
		VALUES ($1, $2, $3, 2, $4, 'TRY', 1500, 0, 1500, '2026-04-30', 'BANK_TRANSFER',
		        'PENDING_APPROVAL')`,
		s.tenant, "ST-202603-"+batchTail(), s.batch, s.provider)
	if err == nil {
		t.Fatal("a second live settlement was opened on one batch")
	}
	if !strings.Contains(err.Error(), "uq_billing_settlement_live_batch") {
		t.Errorf("refused by %v, want uq_billing_settlement_live_batch", err)
	}

	// A cancelled one is outside the index, which is exactly what lets version 2 be opened.
	h.AdminExec(`
		UPDATE billing.settlement SET status = 'CANCELLED', cancel_reason_code = 'WRONG_TOTAL'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.settlement)
	if err := h.AdminExecErr(`
		INSERT INTO billing.settlement (tenant_id, reference, batch_id, version_no,
		                                provider_organization_id, currency_code,
		                                approved_amount, withheld_amount, payable_amount,
		                                due_date, settlement_method, status)
		VALUES ($1, $2, $3, 2, $4, 'TRY', 1500, 0, 1500, '2026-04-30', 'BANK_TRANSFER',
		        'PENDING_APPROVAL')`,
		s.tenant, "ST-202603-"+batchTail(), s.batch, s.provider); err != nil {
		t.Fatalf("version 2 was refused after the first was cancelled: %v", err)
	}
}

// TestPaymentRecordsNeverExceedThePayableAmount is the deferred ceiling, and it holds against a
// psql session that never went near the service.
func TestPaymentRecordsNeverExceedThePayableAmount(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h).seedBatch(t, h).seedSettlement(t, h)
	h.AdminExec(`
		UPDATE billing.settlement SET status = 'APPROVED', approved_by = $2,
		                              approved_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $3`, s.tenant, s.actor, s.settlement)

	// A record beyond the payable amount, with the stored figure moved to match: both halves
	// are wrong and the trigger has to catch them at commit.
	err := h.AdminExecErr(`
		INSERT INTO billing.payment_record (tenant_id, settlement_id, provider_organization_id,
		                                    external_reference, amount, currency_code, paid_at)
		VALUES ($1, $2, $3, 'TRF-TOO-MUCH', 1500, 'TRY', clock_timestamp())`,
		s.tenant, s.settlement, s.provider)
	if err == nil {
		t.Fatal("a payment record above the payable amount was accepted")
	}

	// And a record inside it has to move the stored figure with it, or the trigger refuses the
	// pair. A service that wrote the row and forgot the figure fails here.
	if err := h.AdminExecErr(`
		INSERT INTO billing.payment_record (tenant_id, settlement_id, provider_organization_id,
		                                    external_reference, amount, currency_code, paid_at)
		VALUES ($1, $2, $3, 'TRF-ORPHAN', 400, 'TRY', clock_timestamp())`,
		s.tenant, s.settlement, s.provider); err == nil {
		t.Fatal("a payment record was accepted without the settlement's paid amount moving")
	}
}

// TestPaymentRecordAndPaidAmountMoveTogether is the same invariant from the happy side: written
// as one transaction, the pair is accepted and the status may follow the sum.
func TestPaymentRecordAndPaidAmountMoveTogether(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h).seedBatch(t, h).seedSettlement(t, h)
	h.AdminExec(`
		UPDATE billing.settlement SET status = 'APPROVED', approved_by = $2,
		                              approved_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $3`, s.tenant, s.actor, s.settlement)

	h.AdminExec(`
		WITH written AS (
		    INSERT INTO billing.payment_record (tenant_id, settlement_id,
		                                        provider_organization_id, external_reference,
		                                        amount, currency_code, paid_at)
		    VALUES ($1, $2, $3, 'TRF-OK', 1400, 'TRY', clock_timestamp())
		    RETURNING amount
		)
		UPDATE billing.settlement s
		   SET paid_amount = (SELECT amount FROM written), status = 'PAID'
		 WHERE s.tenant_id = $1 AND s.id = $2`, s.tenant, s.settlement, s.provider)

	// PAID beside a figure short of the payable amount is refused by the plain CHECK.
	if err := h.AdminExecErr(`
		UPDATE billing.settlement SET status = 'PARTIALLY_PAID'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.settlement); err == nil {
		t.Fatal("PARTIALLY_PAID was accepted beside a fully paid figure")
	}
}

// TestTheSameExternalReferenceIsUniquePerProvider: the same transfer entered twice is how a
// settlement comes to look paid when half of it was not.
func TestTheSameExternalReferenceIsUniquePerProvider(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h).seedBatch(t, h).seedSettlement(t, h)
	h.AdminExec(`
		UPDATE billing.settlement SET status = 'APPROVED', approved_by = $2,
		                              approved_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $3`, s.tenant, s.actor, s.settlement)
	h.AdminExec(`
		WITH written AS (
		    INSERT INTO billing.payment_record (tenant_id, settlement_id,
		                                        provider_organization_id, external_reference,
		                                        amount, currency_code, paid_at)
		    VALUES ($1, $2, $3, 'TRF-UNIQUE', 100, 'TRY', clock_timestamp())
		    RETURNING amount
		)
		UPDATE billing.settlement s SET paid_amount = (SELECT amount FROM written),
		                                status = 'PARTIALLY_PAID'
		 WHERE s.tenant_id = $1 AND s.id = $2`, s.tenant, s.settlement, s.provider)

	err := h.AdminExecErr(`
		INSERT INTO billing.payment_record (tenant_id, settlement_id, provider_organization_id,
		                                    external_reference, amount, currency_code, paid_at)
		VALUES ($1, $2, $3, 'TRF-UNIQUE', 100, 'TRY', clock_timestamp())`,
		s.tenant, s.settlement, s.provider)
	if err == nil {
		t.Fatal("the same external reference was entered twice for one provider")
	}
	if !strings.Contains(err.Error(), "uq_billing_payment_record_reference") {
		t.Errorf("refused by %v, want uq_billing_payment_record_reference", err)
	}
}

// TestARecoveryIsNettedIntoOneSettlementOnly is what `billing.settlement_recovery` exists for.
func TestARecoveryIsNettedIntoOneSettlementOnly(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h).seedBatch(t, h).seedSettlement(t, h)
	ctx, cancel := h.Ctx()
	defer cancel()

	claim := s.claim(t, h, "CLM-REC-DB", "APPROVED", "500")
	var adjustment uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO claim.adjustment (tenant_id, claim_id, version_no, adjustment_type, amount,
		                              payer_amount, member_amount, reason_code, source_type,
		                              created_by)
		VALUES ($1, $2, 1, 'RECOVERY', 100, 100, 0, 'OVERPAYMENT', 'MANUAL', $3) RETURNING id`,
		s.tenant, claim, s.actor).Scan(&adjustment); err != nil {
		t.Fatalf("insert the recovery: %v", err)
	}

	h.AdminExec(`
		INSERT INTO billing.settlement_recovery (tenant_id, settlement_id, claim_id,
		                                         adjustment_id, amount)
		VALUES ($1, $2, $3, $4, 100)`, s.tenant, s.settlement, claim, adjustment)

	// A second settlement on another batch cannot take the same recovery back again.
	err := h.AdminExecErr(`
		INSERT INTO billing.settlement_recovery (tenant_id, settlement_id, claim_id,
		                                         adjustment_id, amount)
		VALUES ($1, $2, $3, $4, 100)`, s.tenant, s.settlement, claim, adjustment)
	if err == nil {
		t.Fatal("one recovery was netted into two settlements")
	}
	if !strings.Contains(err.Error(), "uq_billing_settlement_recovery_adjustment") {
		t.Errorf("refused by %v, want uq_billing_settlement_recovery_adjustment", err)
	}
}

// TestTheBankAccountMaskIsExactlyFourCharacters is the schema's half of the privacy claim: the
// column cannot hold anything longer than a mask, so it cannot one day hold the number.
func TestTheBankAccountMaskIsExactlyFourCharacters(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h)
	request, receipt := s.seedReimbursementSources(t, h)

	for _, mask := range []string{"TR330006100519786457841326", "132", "13266", "13 6", "abcd"} {
		err := h.AdminExecErr(`
			INSERT INTO billing.reimbursement (tenant_id, reference, person_id, enrollment_id,
			                                   service_request_id, receipt_document_id,
			                                   receipt_sha256, service_definition_id,
			                                   service_date, provider_organization_id,
			                                   requested_amount, currency_code,
			                                   bank_account_ref_enc, bank_account_masked)
			VALUES ($1, $2, $3, $4, $5, $6, sha256($2::text::bytea), $7, '2026-03-05', $8, 100,
			        'TRY', decode('0102030405060708090a0b0c0d0e0f10', 'hex'), $9)`,
			s.tenant, "RB-202603-"+batchTail(), s.person, s.enrollment, request, receipt,
			s.definition, s.provider, mask)
		if err == nil {
			t.Fatalf("the mask %q was accepted; only four upper-case alphanumerics may be", mask)
		}
	}

	// Four characters is accepted, and the envelope has to be an envelope.
	if err := h.AdminExecErr(`
		INSERT INTO billing.reimbursement (tenant_id, reference, person_id, enrollment_id,
		                                   service_request_id, receipt_document_id,
		                                   receipt_sha256, service_definition_id, service_date,
		                                   provider_organization_id, requested_amount,
		                                   currency_code, bank_account_ref_enc,
		                                   bank_account_masked)
		VALUES ($1, $2, $3, $4, $5, $6, sha256($2::text::bytea), $7, '2026-03-05', $8, 100,
		        'TRY', decode('01', 'hex'), '1326')`,
		s.tenant, "RB-202603-"+batchTail(), s.person, s.enrollment, request, receipt,
		s.definition, s.provider); err == nil {
		t.Fatal("a one-byte 'envelope' was accepted as a cipher envelope")
	}
	if err := h.AdminExecErr(`
		INSERT INTO billing.reimbursement (tenant_id, reference, person_id, enrollment_id,
		                                   service_request_id, receipt_document_id,
		                                   receipt_sha256, service_definition_id, service_date,
		                                   provider_organization_id, requested_amount,
		                                   currency_code, bank_account_ref_enc,
		                                   bank_account_masked)
		VALUES ($1, $2, $3, $4, $5, $6, sha256($2::text::bytea), $7, '2026-03-05', $8, 100,
		        'TRY', decode('0102030405060708090a0b0c0d0e0f10', 'hex'), '1326')`,
		s.tenant, "RB-202603-"+batchTail(), s.person, s.enrollment, request, receipt,
		s.definition, s.provider); err != nil {
		t.Fatalf("a well-formed reimbursement was refused: %v", err)
	}
}

// TestOneLiveReimbursementPerReceipt is the duplicate rule as the database holds it, under the
// race where two submissions look first at the same moment.
func TestOneLiveReimbursementPerReceipt(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h)
	first, receipt := s.seedReimbursementSources(t, h)
	second, _ := s.seedReimbursementSources(t, h)

	s.insertReimbursement(t, h, first, receipt, "SUBMITTED")
	err := s.insertReimbursementErr(h, second, receipt, "SUBMITTED")
	if err == nil {
		t.Fatal("two live reimbursements were written against one receipt")
	}
	if !strings.Contains(err.Error(), "uq_billing_reimbursement_live_receipt") {
		t.Errorf("refused by %v, want uq_billing_reimbursement_live_receipt", err)
	}
}

// TestARejectedReimbursementFreesItsReceipt: a receipt refused on a technicality can be
// submitted again, which is why the index is partial.
func TestARejectedReimbursementFreesItsReceipt(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h)
	first, receipt := s.seedReimbursementSources(t, h)
	second, _ := s.seedReimbursementSources(t, h)

	id := s.insertReimbursement(t, h, first, receipt, "SUBMITTED")
	h.AdminExec(`
		UPDATE billing.reimbursement
		   SET status = 'REJECTED', approved_amount = 0, decision_reason_code = 'NOT_COVERED',
		       decided_by = $2, decided_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $3`, s.tenant, s.actor, id)

	if err := s.insertReimbursementErr(h, second, receipt, "SUBMITTED"); err != nil {
		t.Fatalf("the receipt was still held by a rejected request: %v", err)
	}
}

// TestARejectedReimbursementApprovesNothing is the CHECK that keeps the two decisions apart: a
// rejection approves nought and an approval approves something.
func TestARejectedReimbursementApprovesNothing(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h)
	request, receipt := s.seedReimbursementSources(t, h)
	id := s.insertReimbursement(t, h, request, receipt, "SUBMITTED")

	if err := h.AdminExecErr(`
		UPDATE billing.reimbursement
		   SET status = 'REJECTED', approved_amount = 50, decision_reason_code = 'PARTIAL',
		       decided_by = $2, decided_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $3`, s.tenant, s.actor, id); err == nil {
		t.Fatal("a rejection approved 50")
	}
	if err := h.AdminExecErr(`
		UPDATE billing.reimbursement
		   SET status = 'APPROVED', approved_amount = 0, decided_by = $2,
		       decided_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $3`, s.tenant, s.actor, id); err == nil {
		t.Fatal("an approval approved nothing")
	}
	if err := h.AdminExecErr(`
		UPDATE billing.reimbursement
		   SET status = 'APPROVED', approved_amount = 200, decided_by = $2,
		       decided_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $3`, s.tenant, s.actor, id); err == nil {
		t.Fatal("an approval was accepted above the requested amount")
	}
}

// seedReimbursementSources writes one REIMBURSEMENT service request of the seed's member and one
// clean receipt, which is the pair a `billing.reimbursement` row points at.
//
// The version is opened as a draft, given its item and only then frozen: that is the order
// `service.tg_request_item_guard` insists on, and a seed that wrote a SUBMITTED version straight
// away would be a seed the schema refuses.
func (s billingSeed) seedReimbursementSources(t *testing.T, h *dbtest.Harness,
) (request, receipt uuid.UUID) {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	var version uuid.UUID
	scan(&request, "service request", `
		INSERT INTO service.service_request (tenant_id, request_reference, request_type,
		                                     person_id, program_id, enrollment_id,
		                                     provider_tenant_organization_id, service_date,
		                                     channel, status, submitted_at)
		VALUES ($1, $2, 'REIMBURSEMENT', $3, $4, $5, $6, '2026-03-05', 'MEMBER_PORTAL',
		        'SUBMITTED', clock_timestamp()) RETURNING id`,
		s.tenant, "SR-"+uuid.NewString()[:12], s.person, s.program, s.enrollment, s.provider)
	scan(&version, "service request version", `
		INSERT INTO service.service_request_version (tenant_id, service_request_id, version_no,
		                                             status)
		VALUES ($1, $2, 1, 'DRAFT') RETURNING id`, s.tenant, request)
	h.AdminExec(`
		INSERT INTO service.service_request_item (tenant_id, service_request_version_id, line_no,
		                                          service_definition_id, requested_quantity,
		                                          unit_type)
		VALUES ($1, $2, 1, $3, 1, 'MONEY')`, s.tenant, version, s.definition)
	h.AdminExec(`
		UPDATE service.service_request_version
		   SET status = 'SUBMITTED', snapshot_json = '{}'::jsonb,
		       submitted_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, version)
	scan(&receipt, "receipt", `
		INSERT INTO document.object (tenant_id, owner_tenant_organization_id, object_key,
		                             original_filename, content_type, byte_size, sha256,
		                             bucket, scan_status, uploaded_at)
		VALUES ($1, $2, $3::text, 'fis.pdf', 'application/pdf', 512, sha256($3::text::bytea),
		        'secure', 'CLEAN', clock_timestamp()) RETURNING id`,
		s.tenant, s.provider, "receipts/"+uuid.NewString())
	return request, receipt
}

// insertReimbursement writes one row directly and fails on anything unexpected.
func (s billingSeed) insertReimbursement(t *testing.T, h *dbtest.Harness,
	request, receipt uuid.UUID, status string,
) uuid.UUID {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var id uuid.UUID
	if err := h.Admin.QueryRow(ctx, reimbursementInsert+" RETURNING id",
		s.tenant, "RB-202603-"+batchTail(), s.person, s.enrollment, request, receipt,
		s.definition, s.provider, status).Scan(&id); err != nil {
		t.Fatalf("insert the reimbursement: %v", err)
	}
	return id
}

// insertReimbursementErr is the same write, returning the error so a test can assert a refusal.
func (s billingSeed) insertReimbursementErr(h *dbtest.Harness, request, receipt uuid.UUID,
	status string,
) error {
	return h.AdminExecErr(reimbursementInsert,
		s.tenant, "RB-202603-"+batchTail(), s.person, s.enrollment, request, receipt,
		s.definition, s.provider, status)
}

// reimbursementInsert is the statement both of the above run. The envelope is sixteen bytes of
// nothing in particular: what the column CHECKs is that it is long enough to be an envelope, and
// what it holds is the cipher's business.
const reimbursementInsert = `
	INSERT INTO billing.reimbursement (tenant_id, reference, person_id, enrollment_id,
	                                   service_request_id, receipt_document_id, receipt_sha256,
	                                   service_definition_id, service_date,
	                                   provider_organization_id, requested_amount,
	                                   currency_code, bank_account_ref_enc, bank_account_masked,
	                                   status, submitted_at)
	VALUES ($1, $2, $3, $4, $5, $6, sha256($2::text::bytea), $7, '2026-03-05', $8, 100, 'TRY',
	        decode('0102030405060708090a0b0c0d0e0f10', 'hex'), '1326', $9, clock_timestamp())`

// TestSettlementPermissionsAreSeededAndGrantable checks both halves of the same fact: the
// catalogue row of migration 000046 and the role templates in
// internal/identity/application/roles.go have to agree, because a permission that exists in one
// and not the other is a permission nobody can hold or one nobody can be given.
//
// `settlement.record_payment` is the row that matters. It is a grant of its own rather than a
// second use of `settlement.approve` so that a tenant that wants the approver never to touch the
// payment file can arrange that — which is impossible while the two are one permission.
func TestSettlementPermissionsAreSeededAndGrantable(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	granted := map[string][]string{}
	byRole := map[string]map[string]bool{}
	for _, tpl := range identityapp.RoleTemplates() {
		byRole[tpl.Code] = map[string]bool{}
		for _, code := range tpl.Permissions {
			granted[code] = append(granted[code], tpl.Code)
			byRole[tpl.Code][code] = true
		}
	}

	for code, wantSensitivity := range map[string]string{
		"settlement.read":           "NORMAL",
		"settlement.approve":        "PRIVILEGED",
		"settlement.record_payment": "NORMAL",
	} {
		var n int
		if err := h.Admin.QueryRow(ctx,
			`SELECT count(*) FROM iam.permission WHERE code = $1`, code).Scan(&n); err != nil {
			t.Fatalf("read permission %s: %v", code, err)
		}
		if n != 1 {
			t.Fatalf("permission %s is seeded %d times, want once", code, n)
		}
		var sensitivity string
		if err := h.Admin.QueryRow(ctx,
			`SELECT sensitivity FROM iam.permission WHERE code = $1`, code).Scan(
			&sensitivity); err != nil {
			t.Fatalf("read sensitivity of %s: %v", code, err)
		}
		if sensitivity != wantSensitivity {
			t.Errorf("permission %s sensitivity = %s, want %s", code, sensitivity,
				wantSensitivity)
		}
		if len(granted[code]) == 0 {
			t.Errorf("permission %s is in the catalogue but in no role template: nobody can "+
				"hold it", code)
		}
	}

	for _, want := range []struct{ role, permission string }{
		{"FINANCIAL_REVIEWER", "settlement.read"},
		{"FINANCIAL_REVIEWER", "settlement.record_payment"},
		{"FINANCIAL_REVIEWER", "claim.financial.review"},
		{"PAYER_APPROVER", "settlement.approve"},
		{"PAYER_APPROVER", "settlement.record_payment"},
		{"PROVIDER_BILLING", "settlement.read"},
		// The member reaches their own reimbursement through WP-I4-01's own grants; this
		// package invents no permission for them, and these two are what makes that true.
		{"MEMBER", "service_request.create"},
		{"MEMBER", "service_request.read"},
	} {
		if !byRole[want.role][want.permission] {
			t.Errorf("%s does not hold %s", want.role, want.permission)
		}
	}

	// The provider's billing clerk reads settlements and never approves one: the money leaving
	// is the payer's decision.
	if byRole["PROVIDER_BILLING"]["settlement.approve"] {
		t.Error("PROVIDER_BILLING holds settlement.approve: a provider may not release its own money")
	}
	if byRole["PROVIDER_BILLING"]["settlement.record_payment"] {
		t.Error("PROVIDER_BILLING holds settlement.record_payment: a provider may not record " +
			"a payment it says it received")
	}
	// And the member holds nothing about settlements at all.
	for _, code := range []string{"settlement.read", "settlement.approve",
		"settlement.record_payment"} {
		if byRole["MEMBER"][code] {
			t.Errorf("MEMBER holds %s", code)
		}
	}

	// Every permission a template names exists in the catalogue, so the two halves cannot come
	// apart in the other direction either.
	for code := range granted {
		var n int
		if err := h.Admin.QueryRow(ctx,
			`SELECT count(*) FROM iam.permission WHERE code = $1`, code).Scan(&n); err != nil {
			t.Fatalf("read permission %s: %v", code, err)
		}
		if n != 1 {
			t.Errorf("permission %s is granted by a role template but seeded %d times", code, n)
		}
	}
}

// TestMaskedAccountIsInTheSafeVariableCatalogue is migration 000046's other half: the CHECK on
// `notification.template.declared_variables` admits `masked_account` and still admits nothing
// else, so "there is no slot an account number could be supplied under" is a fact about the
// schema rather than only about the Go.
func TestMaskedAccountIsInTheSafeVariableCatalogue(t *testing.T) {
	h := dbtest.New(t)
	tenant := h.CreateTenant("NTF" + uuid.NewString()[:6])

	if err := h.AdminExecErr(`
		INSERT INTO notification.template (tenant_id, event_code, channel, locale, version_no,
		                                   subject, body, declared_variables)
		VALUES ($1, 'reimbursement.paid', 'EMAIL', 'tr-TR', 1, 'Geri ödemeniz gönderildi',
		        'Son dört hane: {{masked_account}}', ARRAY['masked_account'])`,
		tenant); err != nil {
		t.Fatalf("a template declaring masked_account was refused: %v", err)
	}

	if err := h.AdminExecErr(`
		INSERT INTO notification.template (tenant_id, event_code, channel, locale, version_no,
		                                   subject, body, declared_variables)
		VALUES ($1, 'reimbursement.paid', 'SMS', 'tr-TR', 1, 'x', 'y', ARRAY['bank_account'])`,
		tenant); err == nil {
		t.Fatal("a template declaring bank_account was accepted")
	}
}
