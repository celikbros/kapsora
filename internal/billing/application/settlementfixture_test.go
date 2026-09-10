package application_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// WP-I7-04's half of the fixture, on the same real database, the same real claim module and the
// same real entitlement ledger as the invoice's and the icmal's.
//
// Nothing here is stubbed, and that is the point twice over: what this package must never get
// wrong is that an approved reimbursement moves real money out of a real wallet, and that the
// member's IBAN reaches the database only as a ciphertext. A fake ledger would prove that a
// fake was debited, and a fake cipher would prove that a fake encrypted something.

// testIBAN is the account number every reimbursement test types. It is a constant so the sweep
// can look for it in every column of every table, in the audit log and in the outbox: the whole
// privacy claim of this package is that this string exists in exactly one place.
const testIBAN = "TR330006100519786457841326"

// testIBANTail is what `MaskAccount` produces from it, and the only form of it that may be
// stored, logged or sent.
const testIBANTail = "1326"

// financeRC is the payer's financial reviewer as FINANCIAL_REVIEWER is issued in roles.go, with
// the settlement grants WP-I7-04 adds: read, record a payment, and review a claim financially.
// The actor is spelled out because every maker-checker rule in this package is a rule about
// *which person*.
func (f *fixture) financeRC(actorID uuid.UUID) identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: actorID},
		Permissions: map[string]struct{}{
			application.PermissionRead:                    {},
			application.PermissionSettlementRead:          {},
			application.PermissionSettlementRecordPayment: {},
			application.PermissionClaimFinancialReview:    {},
		},
	}
}

// approverRC is the payer's approver as PAYER_APPROVER is issued: settlement.read and
// settlement.approve, tenant-wide. `StepUpValid` is the caller having re-entered their password,
// which every approval above the threshold needs; a test that is about the step-up turns it off.
func (f *fixture) approverRC(actorID uuid.UUID) identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal:   identity.Principal{ActorID: actorID},
		StepUpValid: true,
		Permissions: map[string]struct{}{
			application.PermissionRead:                    {},
			application.PermissionSettlementRead:          {},
			application.PermissionSettlementApprove:       {},
			application.PermissionSettlementRecordPayment: {},
		},
	}
}

// memberRC is the member as MEMBER is issued, bound to the fixture's person by a PERSON-scoped
// grant. It is what `identity.RequirePerson` reads, so a test that asks for somebody else's
// reimbursement through it is refused by the same code the transport uses.
func (f *fixture) memberRC() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{
			application.PermissionServiceRequestRead:   {},
			application.PermissionServiceRequestCreate: {},
		},
		Scopes: []identity.Scope{{
			Type: identity.ScopePerson,
			ID:   uuid.NullUUID{UUID: f.person, Valid: true},
		}},
	}
}

// contractWithTerm gives the fixture's provider an ACTIVE contract with a PUBLISHED version and
// a payment term, which is what a settlement's due date comes from. Without one the handler
// answers PAYMENT_TERM_MISSING, and a test asserts that separately.
func (f *fixture) contractWithTerm(t *testing.T, dueDays int, method string) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := f.h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	var profile, contract, version uuid.UUID
	scan(&profile, "provider profile", `
		INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type,
		                                       status)
		VALUES ($1, $2, 'CLINIC', 'ACTIVE') RETURNING id`, f.tenant, f.provider)
	scan(&contract, "contract", `
		INSERT INTO contract.contract (tenant_id, code, name, payer_organization_id,
		                               provider_profile_id, domain_code, status)
		VALUES ($1, $2, 'Sözleşme', $3, $4, 'GENERIC', 'ACTIVE') RETURNING id`,
		f.tenant, "CTR"+upperTail(6), f.sponsor, profile)
	scan(&version, "contract version", `
		INSERT INTO contract.contract_version (tenant_id, contract_id, version_no, status,
		                                       valid_from, configuration_hash, published_by,
		                                       published_at)
		VALUES ($1, $2, 1, 'PUBLISHED', '2026-01-01', 'hash', $3, clock_timestamp())
		RETURNING id`, f.tenant, contract, f.actor)
	f.h.AdminExec(`
		INSERT INTO contract.payment_term (tenant_id, contract_version_id, due_days,
		                                   settlement_method, tax_behaviour, vat_rate)
		VALUES ($1, $2, $3, $4, 'EXCLUSIVE', 20)`, f.tenant, version, dueDays, method)
	return version
}

// priceCeiling gives the contract version a price item with a maximum for the fixture's
// service, which is the reimbursement ceiling of v1.2 10.10.
func (f *fixture) priceCeiling(t *testing.T, versionID uuid.UUID, maxAmount string) {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var list uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO contract.price_list (tenant_id, contract_version_id, code, name)
		VALUES ($1, $2, $3, 'Fiyat listesi') RETURNING id`,
		f.tenant, versionID, "PL"+upperTail(6)).Scan(&list); err != nil {
		t.Fatalf("seed price list: %v", err)
	}
	f.h.AdminExec(`
		INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id,
		                                 unit_type, pricing_method, amount, max_amount,
		                                 valid_from)
		VALUES ($1, $2, $3, 'MONEY', 'FIXED', $4::text::numeric, $4::text::numeric,
		        '2026-01-01')`, f.tenant, list, f.definition, maxAmount)
}

// moneyEntitlement opens a MONEY entitlement account for the fixture's member with the given
// balance, the way `EnsureAccounts` would. It is written directly because what these tests care
// about is what a *balance* does when a reimbursement is approved, and driving the plan
// publication to get one would be four commands of somebody else's package.
func (f *fixture) moneyEntitlement(t *testing.T, granted string) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var planVersion, definition, account uuid.UUID
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := f.h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	var plan uuid.UUID
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT plan_id FROM benefit.enrollment WHERE tenant_id = $1 AND id = $2`,
		f.tenant, f.enrollment).Scan(&plan); err != nil {
		t.Fatalf("read the enrollment's plan: %v", err)
	}
	scan(&planVersion, "plan version", `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period,
		                                  published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', daterange('2026-01-01', NULL, '[)'),
		        clock_timestamp(), $3) RETURNING id`, f.tenant, plan, f.actor)
	scan(&definition, "entitlement definition", `
		INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name,
		                                            unit_type, currency_code, period_type,
		                                            initial_quantity)
		VALUES ($1, $2, 'WALLET', 'Cüzdan', 'MONEY', 'TRY', 'CALENDAR_YEAR',
		        $3::text::numeric) RETURNING id`, f.tenant, planVersion, granted)
	scan(&account, "entitlement account", `
		INSERT INTO benefit.entitlement_account (tenant_id, enrollment_id,
		                                         entitlement_definition_id, benefit_period,
		                                         total_granted, available_quantity)
		VALUES ($1, $2, $3, daterange('2026-01-01', '2027-01-01', '[)'),
		        $4::text::numeric, $4::text::numeric) RETURNING id`,
		f.tenant, f.enrollment, definition, granted)
	// The GRANT movement the ledger would have written. Conservation is checked over the
	// ledger, so an account whose opening balance had no movement behind it would make every
	// conservation assertion in this file vacuous.
	f.h.AdminExec(`
		INSERT INTO benefit.entitlement_ledger (tenant_id, entitlement_account_id, movement_type,
		                                        effective_at, delta_total, delta_available,
		                                        reference_type, reference_id, idempotency_key,
		                                        reason_code)
		VALUES ($1, $2, 'GRANT', clock_timestamp(), $3::text::numeric, $3::text::numeric,
		        'ENROLLMENT', $4, $5, 'INITIAL_GRANT')`,
		f.tenant, account, granted, f.enrollment, "grant:"+account.String())
	return account
}

// balances is one entitlement account's four figures, as a test reads them back.
type balances struct {
	Granted   string
	Available string
	Reserved  string
	Consumed  string
}

// accountBalances reads them straight out of the table.
func (f *fixture) accountBalances(t *testing.T, accountID uuid.UUID) balances {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var out balances
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT trim_scale(total_granted)::text, trim_scale(available_quantity)::text,
		       trim_scale(reserved_quantity)::text, trim_scale(consumed_quantity)::text
		  FROM benefit.entitlement_account WHERE tenant_id = $1 AND id = $2`,
		f.tenant, accountID).Scan(&out.Granted, &out.Available, &out.Reserved,
		&out.Consumed); err != nil {
		t.Fatalf("read account balances: %v", err)
	}
	return out
}

// assertConservation is WP-I2-03's own invariant, checked from the ledger rather than from the
// account: the sum of every movement's deltas equals the account's stored balances. It is what
// makes "approval consumes exactly the approved amount" a statement about money rather than
// about a column somebody set.
func (f *fixture) assertConservation(t *testing.T, accountID uuid.UUID) {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var total, available, reserved, consumed, expired string
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT trim_scale(COALESCE(sum(delta_total), 0))::text,
		       trim_scale(COALESCE(sum(delta_available), 0))::text,
		       trim_scale(COALESCE(sum(delta_reserved), 0))::text,
		       trim_scale(COALESCE(sum(delta_consumed), 0))::text,
		       trim_scale(COALESCE(sum(delta_expired), 0))::text
		  FROM benefit.entitlement_ledger WHERE tenant_id = $1 AND entitlement_account_id = $2`,
		f.tenant, accountID).Scan(&total, &available, &reserved, &consumed,
		&expired); err != nil {
		t.Fatalf("sum the ledger: %v", err)
	}
	stored := f.accountBalances(t, accountID)
	if total != stored.Granted || available != stored.Available ||
		reserved != stored.Reserved || consumed != stored.Consumed {
		t.Fatalf("the ledger sums to (%s, %s, %s, %s) but the account holds (%s, %s, %s, %s)",
			total, available, reserved, consumed,
			stored.Granted, stored.Available, stored.Reserved, stored.Consumed)
	}
}

// reimbursementRequest writes one REIMBURSEMENT service request of WP-I4-01 for the fixture's
// member, with one item naming the fixture's service, and returns its id.
func (f *fixture) reimbursementRequest(t *testing.T, serviceDate string) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var request, version uuid.UUID
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := f.h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	scan(&request, "service request", `
		INSERT INTO service.service_request (tenant_id, request_reference, request_type,
		                                     person_id, program_id, enrollment_id,
		                                     provider_tenant_organization_id, service_date,
		                                     channel, status, submitted_at)
		VALUES ($1, $2, 'REIMBURSEMENT', $3, $4, $5, $6, $7::date, 'MEMBER_PORTAL', 'SUBMITTED',
		        clock_timestamp()) RETURNING id`,
		f.tenant, "SR-"+uuid.NewString()[:12], f.person, f.program, f.enrollment, f.provider,
		serviceDate)
	// The version is opened as a draft, given its item and only then frozen. That is the order
	// `service.tg_request_item_guard` insists on -- items belong to a draft -- and a fixture
	// that wrote a SUBMITTED version straight away would be a fixture the schema refuses.
	scan(&version, "service request version", `
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
	return request
}

// receipt writes one clean scanned document of the member's, and returns its id. Every call
// produces a distinct digest, so two receipts are two receipts unless a test asks for one.
func (f *fixture) receipt(t *testing.T) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var id uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO document.object (tenant_id, owner_tenant_organization_id, object_key,
		                             original_filename, content_type, byte_size, sha256,
		                             bucket, scan_status, uploaded_at)
		VALUES ($1, $2, $3::text, 'fis.pdf', 'application/pdf', 512, sha256($3::text::bytea),
		        'secure', 'CLEAN', clock_timestamp()) RETURNING id`,
		f.tenant, f.provider, "receipts/"+uuid.NewString()).Scan(&id); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	return id
}

// draftReimbursement opens one through the real command, which is what applies every check of
// v1.2 10.10.
func (f *fixture) draftReimbursement(t *testing.T, requestID, documentID uuid.UUID,
	amount string,
) application.ReimbursementRecord {
	t.Helper()
	record, err := f.invoices.CreateReimbursement(context.Background(), f.memberRC(),
		application.CreateReimbursementInput{
			PersonID: f.person, ServiceRequestID: requestID, ReceiptDocumentID: documentID,
			RequestedAmount: amount, BankAccount: testIBAN,
		})
	if err != nil {
		t.Fatalf("create reimbursement: %v", err)
	}
	return record
}

// submittedReimbursement opens one and sends it, which is the state a decision runs from.
func (f *fixture) submittedReimbursement(t *testing.T, amount string,
) application.ReimbursementRecord {
	t.Helper()
	request := f.reimbursementRequest(t, "2026-03-05")
	document := f.receipt(t)
	draft := f.draftReimbursement(t, request, document, amount)
	submitted, err := f.invoices.SubmitReimbursement(context.Background(), f.memberRC(),
		f.person, draft.ID, draft.RowVersion)
	if err != nil {
		t.Fatalf("submit reimbursement: %v", err)
	}
	return submitted
}

// decidedBatch drives a whole icmal to DECIDED and returns it. It is what `batch.decided`
// carries, and therefore what a settlement opens on.
func (f *fixture) decidedBatch(t *testing.T, number, claimReference, amount string,
) application.BatchView {
	t.Helper()
	claim := f.approvedClaim(t, claimReference, amount)
	invoice := f.submittedInvoice(t, number, map[uuid.UUID]string{claim: amount})
	batch := f.submittedBatch(t, invoice.Invoice.ID)
	reviewer := f.reviewerRC(f.reviewer)
	batch = f.approveAll(t, reviewer, batch)
	decided, err := f.invoices.DecideBatch(context.Background(), f.reviewerRC(f.approver),
		batch.Batch.ID, batch.Batch.RowVersion)
	if err != nil {
		t.Fatalf("decide batch: %v", err)
	}
	return decided
}

// deliverBatchDecided hands the batch's own `batch.decided` event to the handler, exactly as the
// worker's dispatcher would — the same payload, the same tenant, the same delivery shape.
//
// It reads the row the command published rather than building one, because a test that
// hand-wrote the payload would be a test of a payload nobody publishes.
func (f *fixture) deliverBatchDecided(t *testing.T, batchID uuid.UUID) error {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var payload []byte
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT payload_json FROM system.outbox_event
		 WHERE tenant_id = $1 AND aggregate_id = $2 AND event_type = $3`,
		f.tenant, batchID, application.BatchDecidedEvent).Scan(&payload); err != nil {
		t.Fatalf("read the batch.decided payload: %v", err)
	}
	return f.invoices.HandleBatchDecided(context.Background(), outbox.Delivery{
		ID: uuid.New(), TenantID: uuid.NullUUID{UUID: f.tenant, Valid: true},
		AggregateType: domain.AggregateBatch, AggregateID: batchID,
		Type: application.BatchDecidedEvent, Payload: payload, OccurredAt: fixtureNow,
	})
}

// openSettlement drives a batch to DECIDED, delivers the event and answers the settlement the
// handler opened.
func (f *fixture) openSettlement(t *testing.T, number, claimReference, amount string,
) (application.BatchView, application.SettlementView) {
	t.Helper()
	batch := f.decidedBatch(t, number, claimReference, amount)
	if err := f.deliverBatchDecided(t, batch.Batch.ID); err != nil {
		t.Fatalf("deliver batch.decided: %v", err)
	}
	return batch, f.settlementOf(t, batch.Batch.ID)
}

// settlementOf finds the live settlement of a batch and reads it back through the service.
func (f *fixture) settlementOf(t *testing.T, batchID uuid.UUID) application.SettlementView {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var id uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT id FROM billing.settlement
		 WHERE tenant_id = $1 AND batch_id = $2 AND status <> 'CANCELLED'`,
		f.tenant, batchID).Scan(&id); err != nil {
		t.Fatalf("find the settlement of batch %s: %v", batchID, err)
	}
	view, err := f.invoices.GetSettlement(context.Background(), f.financeRC(f.actor), id)
	if err != nil {
		t.Fatalf("read settlement %s: %v", id, err)
	}
	return view
}

// settlementCount is how many settlements this batch has, cancelled ones included. A second
// delivery that opened a second settlement would show up here and nowhere else.
func (f *fixture) settlementCount(t *testing.T, batchID uuid.UUID) int {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM billing.settlement WHERE tenant_id = $1 AND batch_id = $2`,
		f.tenant, batchID).Scan(&n); err != nil {
		t.Fatalf("count settlements: %v", err)
	}
	return n
}

// recovery writes one open RECOVERY adjustment against a claim of the fixture's provider, which
// is what a settlement nets.
func (f *fixture) recovery(t *testing.T, claimID uuid.UUID, amount string) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var id uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO claim.adjustment (tenant_id, claim_id, version_no, adjustment_type, amount,
		                              payer_amount, member_amount, reason_code, source_type,
		                              created_by)
		VALUES ($1, $2, 1, 'RECOVERY', $3::text::numeric, $3::text::numeric, 0,
		        'OVERPAYMENT', 'MANUAL', $4) RETURNING id`,
		f.tenant, claimID, amount, f.actor).Scan(&id); err != nil {
		t.Fatalf("seed recovery: %v", err)
	}
	return id
}

// settlementAudit reads the detail maps one action wrote about one settlement.
func (f *fixture) settlementAudit(t *testing.T, settlementID uuid.UUID, action string,
) []map[string]any {
	t.Helper()
	return f.auditFor(t, domain.AggregateSettlement, settlementID, action)
}

// reimbursementAudit is the same for a reimbursement.
func (f *fixture) reimbursementAudit(t *testing.T, reimbursementID uuid.UUID, action string,
) []map[string]any {
	t.Helper()
	return f.auditFor(t, domain.AggregateReimbursement, reimbursementID, action)
}

func (f *fixture) auditFor(t *testing.T, resourceType string, resourceID uuid.UUID,
	action string,
) []map[string]any {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	rows, err := f.h.Admin.Query(ctx, `
		SELECT detail_json FROM audit.event
		 WHERE tenant_id = $1 AND resource_type = $2 AND resource_id = $3 AND action_code = $4
		 ORDER BY occurred_at, id`, f.tenant, resourceType, resourceID, action)
	if err != nil {
		t.Fatalf("read audit rows: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var detail map[string]any
		if err := rows.Scan(&detail); err != nil {
			t.Fatalf("scan audit row: %v", err)
		}
		out = append(out, detail)
	}
	return out
}

// notificationVariables reads the safe variables of the messages one event produced, so a test
// can assert what actually leaves the building rather than what a service meant to send.
func (f *fixture) notificationVariables(t *testing.T, eventCode string) []map[string]any {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	rows, err := f.h.Admin.Query(ctx, `
		SELECT payload_json FROM system.outbox_event
		 WHERE tenant_id = $1 AND event_type = $2 AND payload_json->>'eventCode' = $3
		 ORDER BY occurred_at, id`,
		f.tenant, notificationapp.NotifyRequestedEvent, eventCode)
	if err != nil {
		t.Fatalf("read notification events: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var payload map[string]any
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scan notification event: %v", err)
		}
		out = append(out, payload)
	}
	return out
}

// upperTail is a short random suffix in the alphabet the code CHECKs accept: upper-case letters
// and digits. A lower-case hex tail would be refused by `ck_contract_code`, and a fixture that
// failed on a constraint nobody is testing is a fixture that hides the ones that are.
func upperTail(n int) string {
	return strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:n]
}

// dayAfter is `time.Time` arithmetic spelled once, so a due-date assertion reads as a date and
// not as a calculation.
func dayAfter(t *testing.T, day string, days int) string {
	t.Helper()
	parsed, err := time.Parse(time.DateOnly, day)
	if err != nil {
		t.Fatalf("parse %q: %v", day, err)
	}
	return parsed.AddDate(0, 0, days).Format(time.DateOnly)
}
