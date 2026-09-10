-- WP-I7-04: the settlement a decided batch opens, the payment records against it, and the
-- member's reimbursement.
--
-- Every money column arrives and leaves as exact decimal text -- `sqlc.arg(x)::text::numeric`
-- on the way in and `trim_scale(x)::text` on the way out -- so nothing in this module ever
-- becomes a float and two tenants who typed "1000" and "1000.00" read the same string back.
--
-- The reads below reach into `billing.batch`, `claim`, `contract`, `directory`, `service`,
-- `document` and `catalog`. That is deliberate and it is read-only: a settlement is a
-- statement about a batch of a provider under a contract, and asking four services for four
-- halves of one page would be four round trips that could disagree with each other. Every
-- *write* that changes a claim goes through the claim module's own command.
--
-- One statement in this file returns a ciphertext (`GetReimbursement` does not; nothing does).
-- The envelope of `bank_account_ref_enc` is written by `CreateReimbursement` and is never
-- selected back: no screen, no list and no export has a reason to hold it, and a column that
-- is only ever written is a column that cannot leak through a mapper somebody forgot.

-- ---------------------------------------------------------------------------
-- billing.settlement
-- ---------------------------------------------------------------------------

-- name: CreateSettlement :one
-- The header, opened by the outbox handler of `batch.decided`. `payable_amount` is passed in
-- rather than computed here so that the service and the CHECK are two independent statements
-- of the same arithmetic; a service that got it wrong fails at the constraint.
INSERT INTO billing.settlement (
    tenant_id, reference, batch_id, version_no, provider_organization_id,
    payer_organization_id, currency_code, approved_amount, withheld_amount, payable_amount,
    due_date, settlement_method, status, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('reference'), sqlc.arg('batch_id'),
        sqlc.arg('version_no'), sqlc.arg('provider_organization_id'),
        sqlc.narg('payer_organization_id'), sqlc.arg('currency_code'),
        sqlc.arg('approved_amount')::text::numeric,
        sqlc.arg('withheld_amount')::text::numeric,
        sqlc.arg('payable_amount')::text::numeric,
        sqlc.arg('due_date'), sqlc.arg('settlement_method'), sqlc.arg('status'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, reference, batch_id, version_no, provider_organization_id, payer_organization_id,
          currency_code,
          trim_scale(approved_amount)::text AS approved_amount,
          trim_scale(withheld_amount)::text AS withheld_amount,
          trim_scale(payable_amount)::text AS payable_amount,
          trim_scale(paid_amount)::text AS paid_amount,
          due_date, settlement_method, status, approved_by, approved_at, checked_by,
          posting_id, cancel_reason_code, created_at, row_version;

-- name: GetSettlement :one
-- One settlement, bounded by the caller's provider scope. An empty scope array means "no
-- restriction"; a scope that names organizations narrows the read in SQL, so a settlement of
-- somebody else's provider is not found rather than found and then hidden.
SELECT s.id, s.reference, s.batch_id, s.version_no, s.provider_organization_id,
       s.payer_organization_id, s.currency_code,
       trim_scale(s.approved_amount)::text AS approved_amount,
       trim_scale(s.withheld_amount)::text AS withheld_amount,
       trim_scale(s.payable_amount)::text AS payable_amount,
       trim_scale(s.paid_amount)::text AS paid_amount,
       s.due_date, s.settlement_method, s.status, s.approved_by, s.approved_at, s.checked_by,
       s.posting_id, s.cancel_reason_code, s.created_at, s.row_version,
       o.display_name AS provider_name,
       b.reference AS batch_reference,
       b.decided_by AS batch_decided_by
  FROM billing.settlement s
  JOIN billing.batch b ON b.tenant_id = s.tenant_id AND b.id = s.batch_id
  JOIN directory.tenant_organization t
    ON t.tenant_id = s.tenant_id AND t.id = s.provider_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.id = sqlc.arg('id')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR s.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]));

-- name: LockSettlement :one
-- The same read, holding the row for the length of the command. The approval, the
-- cancellation and every payment record take it first: two payment records entered at the
-- same moment serialise here, which is what makes the deferred ceiling below a guarantee
-- rather than a race.
SELECT s.id, s.reference, s.batch_id, s.version_no, s.provider_organization_id,
       s.payer_organization_id, s.currency_code,
       trim_scale(s.approved_amount)::text AS approved_amount,
       trim_scale(s.withheld_amount)::text AS withheld_amount,
       trim_scale(s.payable_amount)::text AS payable_amount,
       trim_scale(s.paid_amount)::text AS paid_amount,
       s.due_date, s.settlement_method, s.status, s.approved_by, s.approved_at, s.checked_by,
       s.posting_id, s.cancel_reason_code, s.created_at, s.row_version
  FROM billing.settlement s
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.id = sqlc.arg('id')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR s.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
   FOR UPDATE;

-- name: ListSettlements :many
-- One page of the keyset on (created_at DESC, id DESC).
SELECT s.id, s.reference, s.batch_id, s.version_no, s.provider_organization_id,
       s.payer_organization_id, s.currency_code,
       trim_scale(s.approved_amount)::text AS approved_amount,
       trim_scale(s.withheld_amount)::text AS withheld_amount,
       trim_scale(s.payable_amount)::text AS payable_amount,
       trim_scale(s.paid_amount)::text AS paid_amount,
       s.due_date, s.settlement_method, s.status, s.approved_by, s.approved_at, s.checked_by,
       s.posting_id, s.cancel_reason_code, s.created_at, s.row_version,
       o.display_name AS provider_name,
       b.reference AS batch_reference,
       b.decided_by AS batch_decided_by
  FROM billing.settlement s
  JOIN billing.batch b ON b.tenant_id = s.tenant_id AND b.id = s.batch_id
  JOIN directory.tenant_organization t
    ON t.tenant_id = s.tenant_id AND t.id = s.provider_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR s.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR s.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND (sqlc.narg('payer_organization_id')::uuid IS NULL
        OR s.payer_organization_id = sqlc.narg('payer_organization_id')::uuid)
   AND (sqlc.narg('batch_id')::uuid IS NULL OR s.batch_id = sqlc.narg('batch_id')::uuid)
   AND (sqlc.narg('status')::text IS NULL OR s.status = sqlc.narg('status')::text)
   AND (sqlc.narg('currency_code')::text IS NULL
        OR s.currency_code = sqlc.narg('currency_code')::text)
   AND (sqlc.narg('due_from')::date IS NULL OR s.due_date >= sqlc.narg('due_from')::date)
   AND (sqlc.narg('due_to')::date IS NULL OR s.due_date <= sqlc.narg('due_to')::date)
   AND (sqlc.narg('after_created_at')::timestamptz IS NULL
        OR (s.created_at, s.id) < (sqlc.narg('after_created_at')::timestamptz,
                                   sqlc.narg('after_id')::uuid))
 ORDER BY s.created_at DESC, s.id DESC
 LIMIT sqlc.arg('page_size');

-- name: FindLiveSettlementForBatch :one
-- "Has a settlement already been opened on this batch?" It is the first half of the outbox
-- handler's idempotency; `uq_billing_settlement_live_batch` is the half that holds when two
-- deliveries look at the same moment and both find nothing.
SELECT s.id, s.reference, s.version_no, s.status
  FROM billing.settlement s
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.batch_id = sqlc.arg('batch_id')
   AND s.status <> 'CANCELLED';

-- name: NextSettlementVersion :one
-- Which attempt the next settlement on this batch is. It counts cancelled ones too: version
-- numbers are the history of the batch's settlements and reusing one would make "settlement 2
-- of this batch" a phrase with two meanings.
SELECT COALESCE(max(s.version_no), 0)::int + 1 AS next_version
  FROM billing.settlement s
 WHERE s.tenant_id = sqlc.arg('tenant_id') AND s.batch_id = sqlc.arg('batch_id');

-- name: ApproveSettlement :execrows
-- PENDING_APPROVAL to APPROVED, guarded by the row version the caller held. `checked_by` is
-- written only above the threshold; below it there is one person and recording them twice
-- would say a check happened that did not.
UPDATE billing.settlement
   SET status = 'APPROVED',
       approved_by = sqlc.arg('approved_by'),
       approved_at = sqlc.arg('approved_at'),
       checked_by = sqlc.narg('checked_by'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'PENDING_APPROVAL'
   AND row_version = sqlc.arg('expected_row_version');

-- name: SetSettlementStatus :execrows
-- The transitions that carry nothing but a word: DRAFT to PENDING_APPROVAL when the handler
-- has finished opening the settlement, and APPROVED to POSTED when M9's posting lands.
UPDATE billing.settlement
   SET status = sqlc.arg('status'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = ANY(sqlc.arg('from_statuses')::text[]);

-- name: CancelSettlement :execrows
-- A wrong settlement is cancelled with a reason, never edited, and a new version is opened on
-- the same batch. A settlement that has been paid against is not cancellable here: the money
-- has moved and the answer is a dispute on the record rather than a rewrite of the header.
UPDATE billing.settlement
   SET status = 'CANCELLED',
       cancel_reason_code = sqlc.arg('reason_code'),
       cancel_reason_text = sqlc.narg('reason_text'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('DRAFT', 'PENDING_APPROVAL', 'APPROVED', 'POSTED')
   AND paid_amount = 0
   AND row_version = sqlc.arg('expected_row_version');

-- name: SetSettlementPaidAmount :execrows
-- The stored sum and the word that follows it, written together. They are one statement
-- because `ck_billing_settlement_paid_status` refuses them apart, which is the point: a
-- settlement that said PAID beside a figure short of the payable amount would be a settlement
-- nobody would chase.
UPDATE billing.settlement
   SET paid_amount = sqlc.arg('paid_amount')::text::numeric,
       status = sqlc.arg('status'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id');

-- name: GetBatchForSettlement :one
-- What a settlement needs from the batch it opens on, in one read: the parties, the currency,
-- the approved total, and who decided it -- the last because the maker-checker rule above the
-- threshold is about that person.
SELECT b.id, b.reference, b.provider_organization_id, b.payer_organization_id, b.currency_code,
       b.domain_code, b.status, b.decided_at, b.decided_by,
       trim_scale(b.approved_total)::text AS approved_total,
       o.display_name AS provider_name,
       pp.id AS provider_profile_id
  FROM billing.batch b
  JOIN directory.tenant_organization t
    ON t.tenant_id = b.tenant_id AND t.id = b.provider_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
  LEFT JOIN provider.provider_profile pp
    ON pp.tenant_id = b.tenant_id AND pp.tenant_organization_id = b.provider_organization_id
 WHERE b.tenant_id = sqlc.arg('tenant_id') AND b.id = sqlc.arg('id');

-- name: GetPaymentTermForProvider :one
-- The payment term of the contract that covers this provider on the day the batch was
-- decided. A contract with no term on the day answers nothing, and the service turns that
-- into `PAYMENT_TERM_MISSING` rather than inventing a due date.
--
-- `payer_organization_id` narrows the search when the batch names a sponsor; a batch whose
-- payer is the tenant itself passes NULL and takes whichever ACTIVE contract covers the day.
-- Ties fall to the newest published version, which is the one in force.
SELECT pt.due_days, pt.settlement_method, c.id AS contract_id, v.id AS contract_version_id
  FROM contract.payment_term pt
  JOIN contract.contract_version v
    ON v.tenant_id = pt.tenant_id AND v.id = pt.contract_version_id
  JOIN contract.contract c ON c.tenant_id = v.tenant_id AND c.id = v.contract_id
 WHERE pt.tenant_id = sqlc.arg('tenant_id')
   AND c.provider_profile_id = sqlc.arg('provider_profile_id')
   AND c.status = 'ACTIVE'
   AND v.status = 'PUBLISHED'
   AND v.valid_from <= sqlc.arg('as_of')::date
   AND (v.valid_to IS NULL OR v.valid_to > sqlc.arg('as_of')::date)
   AND (sqlc.narg('payer_organization_id')::uuid IS NULL
        OR c.payer_organization_id = sqlc.narg('payer_organization_id')::uuid)
 ORDER BY v.valid_from DESC, v.version_no DESC
 LIMIT 1;

-- name: ListOpenRecoveries :many
-- The recoveries this settlement has to net: RECOVERY adjustments on claims of this provider
-- that no settlement has taken yet and that nobody has reversed.
--
-- "Not yet netted" is `billing.settlement_recovery` rather than a flag on the adjustment,
-- because `claim.adjustment` is append-only and a flag on it would be the one column of that
-- table anybody ever updated. "Not reversed" is the absence of a REVERSAL pointing at the row:
-- a recovery somebody took back is not money the payer is still holding.
SELECT a.id AS adjustment_id, a.claim_id, trim_scale(a.amount)::text AS amount,
       a.reason_code, a.created_at
  FROM claim.adjustment a
  JOIN claim.claim c ON c.tenant_id = a.tenant_id AND c.id = a.claim_id
 WHERE a.tenant_id = sqlc.arg('tenant_id')
   AND a.adjustment_type = 'RECOVERY'
   AND c.provider_organization_id = sqlc.arg('provider_organization_id')
   AND NOT EXISTS (
       SELECT 1 FROM billing.settlement_recovery r
        WHERE r.tenant_id = a.tenant_id AND r.adjustment_id = a.id)
   AND NOT EXISTS (
       SELECT 1 FROM claim.adjustment rev
        WHERE rev.tenant_id = a.tenant_id AND rev.reverses_adjustment_id = a.id)
 ORDER BY a.created_at, a.id;

-- name: CreateSettlementRecovery :exec
-- One recovery marked as netted into one settlement.
INSERT INTO billing.settlement_recovery (
    tenant_id, settlement_id, claim_id, adjustment_id, amount, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('settlement_id'), sqlc.arg('claim_id'),
        sqlc.arg('adjustment_id'), sqlc.arg('amount')::text::numeric, sqlc.narg('actor_id'));

-- name: ListSettlementRecoveries :many
SELECT r.id, r.claim_id, r.adjustment_id, trim_scale(r.amount)::text AS amount, r.created_at
  FROM billing.settlement_recovery r
 WHERE r.tenant_id = sqlc.arg('tenant_id') AND r.settlement_id = sqlc.arg('settlement_id')
 ORDER BY r.created_at, r.id;

-- name: DeleteSettlementRecoveries :execrows
-- A cancelled settlement lets its recoveries go: they are open again and the replacement
-- version nets them. It is the one delete in this schema, and it is a delete rather than a
-- flag because the row's whole meaning is "this recovery is spoken for".
DELETE FROM billing.settlement_recovery
 WHERE tenant_id = sqlc.arg('tenant_id') AND settlement_id = sqlc.arg('settlement_id');

-- ---------------------------------------------------------------------------
-- billing.payment_record
-- ---------------------------------------------------------------------------

-- name: CreatePaymentRecord :one
INSERT INTO billing.payment_record (
    tenant_id, settlement_id, provider_organization_id, external_reference, amount,
    currency_code, paid_at, source, recorded_by, notes, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('settlement_id'),
        sqlc.arg('provider_organization_id'), sqlc.arg('external_reference'),
        sqlc.arg('amount')::text::numeric, sqlc.arg('currency_code'), sqlc.arg('paid_at'),
        sqlc.arg('source'), sqlc.narg('recorded_by'), sqlc.narg('notes'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, settlement_id, provider_organization_id, external_reference,
          trim_scale(amount)::text AS amount, currency_code, paid_at, source, status,
          recorded_by, notes, created_at, row_version;

-- name: ListPaymentRecords :many
SELECT p.id, p.settlement_id, p.provider_organization_id, p.external_reference,
       trim_scale(p.amount)::text AS amount, p.currency_code, p.paid_at, p.source, p.status,
       p.recorded_by, p.notes, p.created_at, p.row_version
  FROM billing.payment_record p
  JOIN billing.settlement s ON s.tenant_id = p.tenant_id AND s.id = p.settlement_id
 WHERE p.tenant_id = sqlc.arg('tenant_id')
   AND p.settlement_id = sqlc.arg('settlement_id')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR s.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
 ORDER BY p.paid_at, p.id;

-- name: SumLivePaymentRecords :one
-- What the settlement's records add up to, disputed ones excluded. The service uses it to
-- write `paid_amount`; the deferred trigger computes the same sum and refuses the write when
-- the two disagree.
SELECT COALESCE(trim_scale(sum(p.amount))::text, '0')::text AS live_total
  FROM billing.payment_record p
 WHERE p.tenant_id = sqlc.arg('tenant_id')
   AND p.settlement_id = sqlc.arg('settlement_id')
   AND p.status <> 'DISPUTED';

-- ---------------------------------------------------------------------------
-- billing.reimbursement
-- ---------------------------------------------------------------------------

-- name: CreateReimbursement :one
-- The header, with the cipher's envelope and the four characters of the mask. The envelope is
-- written here and read back by nothing.
INSERT INTO billing.reimbursement (
    tenant_id, reference, person_id, enrollment_id, service_request_id, receipt_document_id,
    receipt_sha256, service_definition_id, service_date, provider_organization_id,
    requested_amount, currency_code, bank_account_ref_enc, bank_account_masked,
    created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('reference'), sqlc.arg('person_id'),
        sqlc.arg('enrollment_id'), sqlc.arg('service_request_id'),
        sqlc.arg('receipt_document_id'), sqlc.arg('receipt_sha256'),
        sqlc.arg('service_definition_id'), sqlc.arg('service_date'),
        sqlc.arg('provider_organization_id'), sqlc.arg('requested_amount')::text::numeric,
        sqlc.arg('currency_code'), sqlc.arg('bank_account_ref_enc'),
        sqlc.arg('bank_account_masked'), sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, reference, person_id, enrollment_id, service_request_id, claim_id,
          receipt_document_id, service_definition_id, service_date, provider_organization_id,
          trim_scale(requested_amount)::text AS requested_amount,
          COALESCE(trim_scale(approved_amount)::text, '')::text AS approved_amount,
          currency_code, bank_account_masked, status, duplicate_of_id, decision_reason_code,
          decided_by, decided_at, submitted_at, payment_reference, paid_at, created_at,
          row_version;

-- name: GetReimbursement :one
-- One reimbursement. `person_id` narrows the read for the member's own screens: the PERSON
-- scope passes it and another member's row is simply not found, which is the same answer as
-- a row that does not exist -- and it is the same answer on purpose.
SELECT r.id, r.reference, r.person_id, r.enrollment_id, r.service_request_id, r.claim_id,
       r.receipt_document_id, r.service_definition_id, r.service_date,
       r.provider_organization_id,
       trim_scale(r.requested_amount)::text AS requested_amount,
       COALESCE(trim_scale(r.approved_amount)::text, '')::text AS approved_amount,
       r.currency_code, r.bank_account_masked, r.status, r.duplicate_of_id,
       r.decision_reason_code, r.decided_by, r.decided_at, r.submitted_at,
       r.payment_reference, r.paid_at, r.created_at, r.row_version,
       d.reference AS duplicate_of_reference
  FROM billing.reimbursement r
  LEFT JOIN billing.reimbursement d
    ON d.tenant_id = r.tenant_id AND d.id = r.duplicate_of_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.id = sqlc.arg('id')
   AND (sqlc.narg('person_id')::uuid IS NULL OR r.person_id = sqlc.narg('person_id')::uuid);

-- name: LockReimbursement :one
SELECT r.id, r.reference, r.person_id, r.enrollment_id, r.service_request_id, r.claim_id,
       r.receipt_document_id, r.service_definition_id, r.service_date,
       r.provider_organization_id,
       trim_scale(r.requested_amount)::text AS requested_amount,
       COALESCE(trim_scale(r.approved_amount)::text, '')::text AS approved_amount,
       r.currency_code, r.bank_account_masked, r.status, r.duplicate_of_id,
       r.decision_reason_code, r.decided_by, r.decided_at, r.submitted_at,
       r.payment_reference, r.paid_at, r.created_at, r.row_version
  FROM billing.reimbursement r
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.id = sqlc.arg('id')
   AND (sqlc.narg('person_id')::uuid IS NULL OR r.person_id = sqlc.narg('person_id')::uuid)
   FOR UPDATE;

-- name: ListReimbursements :many
-- One page of the keyset on (created_at DESC, id DESC). `person_id` is the member's own list
-- and the back-office list is the same statement without it.
SELECT r.id, r.reference, r.person_id, r.enrollment_id, r.service_request_id, r.claim_id,
       r.receipt_document_id, r.service_definition_id, r.service_date,
       r.provider_organization_id,
       trim_scale(r.requested_amount)::text AS requested_amount,
       COALESCE(trim_scale(r.approved_amount)::text, '')::text AS approved_amount,
       r.currency_code, r.bank_account_masked, r.status, r.duplicate_of_id,
       r.decision_reason_code, r.decided_by, r.decided_at, r.submitted_at,
       r.payment_reference, r.paid_at, r.created_at, r.row_version,
       d.reference AS duplicate_of_reference
  FROM billing.reimbursement r
  LEFT JOIN billing.reimbursement d
    ON d.tenant_id = r.tenant_id AND d.id = r.duplicate_of_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('person_id')::uuid IS NULL OR r.person_id = sqlc.narg('person_id')::uuid)
   AND (sqlc.narg('status')::text IS NULL OR r.status = sqlc.narg('status')::text)
   AND (sqlc.narg('date_from')::date IS NULL OR r.service_date >= sqlc.narg('date_from')::date)
   AND (sqlc.narg('date_to')::date IS NULL OR r.service_date <= sqlc.narg('date_to')::date)
   AND (sqlc.narg('after_created_at')::timestamptz IS NULL
        OR (r.created_at, r.id) < (sqlc.narg('after_created_at')::timestamptz,
                                   sqlc.narg('after_id')::uuid))
 ORDER BY r.created_at DESC, r.id DESC
 LIMIT sqlc.arg('page_size');

-- name: SubmitReimbursement :execrows
UPDATE billing.reimbursement
   SET status = 'SUBMITTED',
       submitted_at = sqlc.arg('submitted_at'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT'
   AND row_version = sqlc.arg('expected_row_version');

-- name: DecideReimbursement :execrows
-- Approved in full, approved in part or rejected: one statement, because the three are one
-- decision and the CHECKs on the table refuse every combination that is not one of them.
UPDATE billing.reimbursement
   SET status = sqlc.arg('status'),
       approved_amount = sqlc.arg('approved_amount')::text::numeric,
       decision_reason_code = sqlc.narg('reason_code'),
       decision_reason_text = sqlc.narg('reason_text'),
       duplicate_of_id = COALESCE(sqlc.narg('duplicate_of_id'), duplicate_of_id),
       claim_id = COALESCE(sqlc.narg('claim_id'), claim_id),
       decided_by = sqlc.arg('decided_by'),
       decided_at = sqlc.arg('decided_at'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('SUBMITTED', 'UNDER_REVIEW')
   AND row_version = sqlc.arg('expected_row_version');

-- name: SetReimbursementStatus :execrows
-- The words a decision leads to: PAYMENT_ORDERED once the payment adapter has been handed the
-- order, and CANCELLED when the member withdraws a draft.
UPDATE billing.reimbursement
   SET status = sqlc.arg('status'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = ANY(sqlc.arg('from_statuses')::text[]);

-- name: SetReimbursementPaid :execrows
-- Finance entered the reference the bank gave them. It is the end of the road: nothing moves
-- a PAID reimbursement afterwards.
UPDATE billing.reimbursement
   SET status = 'PAID',
       payment_reference = sqlc.arg('payment_reference'),
       paid_at = sqlc.arg('paid_at'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('APPROVED', 'PARTIALLY_APPROVED', 'PAYMENT_ORDERED');

-- name: FindReimbursementDuplicate :one
-- The duplicate rule of v1.2 10.10, both halves in one read: the same person's earlier
-- reimbursement carrying the same receipt digest, or the same person's earlier reimbursement
-- at the same provider on the same day for the same amount, inside the tenant's window.
--
-- Rejected and cancelled rows are outside it. A receipt refused on a technicality is not a
-- duplicate of anything, and refusing the corrected resubmission as one would be the platform
-- arguing with itself.
--
-- The window is a number of days rather than a fixed period because "the same receipt twice"
-- and "the same receipt again next year" are different facts, and a tenant decides where the
-- line is.
SELECT r.id, r.reference, r.service_date, r.status,
       trim_scale(r.requested_amount)::text AS requested_amount,
       (r.receipt_sha256 = sqlc.arg('receipt_sha256')) AS by_receipt
  FROM billing.reimbursement r
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.person_id = sqlc.arg('person_id')
   AND r.id <> sqlc.arg('exclude_id')
   AND r.status NOT IN ('REJECTED', 'CANCELLED')
   AND r.created_at >= sqlc.arg('window_from')
   AND (
        r.receipt_sha256 = sqlc.arg('receipt_sha256')
        OR (r.provider_organization_id = sqlc.arg('provider_organization_id')
            AND r.service_date = sqlc.arg('service_date')::date
            AND r.requested_amount = sqlc.arg('requested_amount')::text::numeric)
       )
 ORDER BY (r.receipt_sha256 = sqlc.arg('receipt_sha256')) DESC, r.created_at, r.id
 LIMIT 1;

-- name: GetReimbursementRequest :one
-- What a reimbursement needs from the request it wraps and the receipt it carries, in one
-- read: the member, the enrollment, the service, the day, the provider, and whether the
-- document is a clean one of this member's.
--
-- The document join is deliberately not narrowed to a link row. WP-I4-04's link is what a
-- reviewer opens; what this gate needs is that the bytes were scanned and found clean, and
-- that is a fact about the object.
SELECT sr.id AS service_request_id, sr.person_id, sr.enrollment_id, sr.program_id,
       sr.service_date, sr.provider_tenant_organization_id, sr.request_type, sr.status,
       sr.request_reference,
       o.id AS document_id, o.scan_status, o.sha256, o.purged_at,
       -- The service the request's newest version names on its first line. It is COALESCEd
       -- to the nil uuid rather than left null so the column is not nullable in one shape
       -- and present in every other: a request carrying no item at all is a request nobody
       -- can reimburse, and the service says so by name.
       COALESCE((SELECT i.service_definition_id
          FROM service.service_request_item i
          JOIN service.service_request_version v
            ON v.tenant_id = i.tenant_id AND v.id = i.service_request_version_id
         WHERE v.tenant_id = sr.tenant_id AND v.service_request_id = sr.id
         ORDER BY v.version_no DESC, i.line_no
         LIMIT 1), '00000000-0000-0000-0000-000000000000'::uuid)::uuid
         AS service_definition_id
  FROM service.service_request sr
  JOIN document.object o ON o.tenant_id = sr.tenant_id AND o.id = sqlc.arg('document_id')
 WHERE sr.tenant_id = sqlc.arg('tenant_id') AND sr.id = sqlc.arg('service_request_id');

-- name: GetReimbursementCeiling :one
-- The contract's ceiling for this service on this day, when one exists. It is
-- `contract.price_item.max_amount` -- the bound the contract already carries for what a
-- service may cost -- rather than a column of its own, because a second place to state "the
-- most this service is worth" is a second place for it to disagree with the price sheet.
--
-- The narrowest scope wins: an item naming the service beats one naming its category, and a
-- later `valid_from` beats an earlier one. NULL means the contract sets no ceiling, which is
-- ordinary.
SELECT trim_scale(pi.max_amount)::text AS max_amount, pi.id AS price_item_id
  FROM contract.price_item pi
  JOIN contract.price_list pl ON pl.tenant_id = pi.tenant_id AND pl.id = pi.price_list_id
  JOIN contract.contract_version v ON v.tenant_id = pl.tenant_id AND v.id = pl.contract_version_id
  JOIN contract.contract c ON c.tenant_id = v.tenant_id AND c.id = v.contract_id
 WHERE pi.tenant_id = sqlc.arg('tenant_id')
   AND pi.max_amount IS NOT NULL
   AND pi.service_definition_id = sqlc.arg('service_definition_id')
   AND pi.valid_from <= sqlc.arg('as_of')::date
   AND (pi.valid_to IS NULL OR pi.valid_to > sqlc.arg('as_of')::date)
   AND c.status = 'ACTIVE'
   AND v.status = 'PUBLISHED'
   AND v.valid_from <= sqlc.arg('as_of')::date
   AND (v.valid_to IS NULL OR v.valid_to > sqlc.arg('as_of')::date)
   AND (sqlc.narg('provider_profile_id')::uuid IS NULL
        OR c.provider_profile_id = sqlc.narg('provider_profile_id')::uuid)
 ORDER BY pi.priority, pi.valid_from DESC, pi.id
 LIMIT 1;

-- name: GetProviderProfileForOrganization :one
-- The provider profile of a tenant organization. Contracts hang off the profile and every
-- other table in this module names the organization, so the translation happens once.
SELECT pp.id AS provider_profile_id, pp.status
  FROM provider.provider_profile pp
 WHERE pp.tenant_id = sqlc.arg('tenant_id')
   AND pp.tenant_organization_id = sqlc.arg('tenant_organization_id');

-- name: GetEnrollmentCoverage :one
-- Whether the enrollment the request names actually covered the member on the day they spent
-- the money. It is the eligibility check of v1.2 10.10 as this package can honestly make it:
-- the enrollment is the member's, it is ACTIVE, and the service date falls inside its period.
--
-- It is a read of the enrollment rather than a call into the eligibility engine because the
-- engine answers a question about a *future* service — may this be authorised — and a
-- reimbursement is about one already paid for. What matters afterwards is only whether the
-- member was covered on the day.
SELECT e.id, e.status,
       (e.valid_period @> sqlc.arg('service_date')::date)::boolean AS covers_date,
       sm.person_id
  FROM benefit.enrollment e
  JOIN party.sponsor_membership sm
    ON sm.tenant_id = e.tenant_id AND sm.id = e.sponsor_membership_id
 WHERE e.tenant_id = sqlc.arg('tenant_id') AND e.id = sqlc.arg('id');
