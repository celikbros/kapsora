package dbtests

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// coreSeed holds the minimal chain needed by request/enrollment tests.
type coreSeed struct {
	tenant     uuid.UUID
	sponsorOrg uuid.UUID
	payerOrg   uuid.UUID
	provider   uuid.UUID
	person     uuid.UUID
	membership uuid.UUID
	program    uuid.UUID
	plan       uuid.UUID
	enrollment uuid.UUID
	category   uuid.UUID
	service    uuid.UUID
}

func (h *harness) seedCore(code string) coreSeed {
	h.t.Helper()
	ctx, cancel := h.ctx()
	defer cancel()

	s := coreSeed{tenant: h.createTenant(code)}
	s.sponsorOrg = h.createTenantOrganization(s.tenant, code+" Sponsor", "SPONSOR")
	s.payerOrg = h.createTenantOrganization(s.tenant, code+" Payer", "PAYER")
	s.provider = h.createTenantOrganization(s.tenant, code+" Provider", "PROVIDER")

	must := func(err error, what string) {
		h.t.Helper()
		if err != nil {
			h.t.Fatalf("seed %s: %v", what, err)
		}
	}

	must(h.admin.QueryRow(ctx, `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Test', 'Person', 'test person') RETURNING id`, s.tenant).Scan(&s.person), "person")

	h.adminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, s.tenant)
	must(h.admin.QueryRow(ctx, `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id, membership_type, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, s.person, s.sponsorOrg).Scan(&s.membership), "sponsor membership")

	h.adminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name) VALUES ($1, 'BENEFIT', 'Fayda')`, s.tenant)
	must(h.admin.QueryRow(ctx, `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id, payer_tenant_organization_id, code, name, program_type, valid_period)
		VALUES ($1, $2, $3, 'PRG1', 'Program 1', 'BENEFIT', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, s.sponsorOrg, s.payerOrg).Scan(&s.program), "program")
	must(h.admin.QueryRow(ctx, `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name) VALUES ($1, $2, 'PLAN1', 'Plan 1') RETURNING id`,
		s.tenant, s.program).Scan(&s.plan), "plan")
	must(h.admin.QueryRow(ctx, `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, valid_period)
		VALUES ($1, $2, $3, daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, s.membership, s.plan).Scan(&s.enrollment), "enrollment")

	must(h.admin.QueryRow(ctx, `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'GEN', 'Genel', 'GENERIC') RETURNING id`, s.tenant).Scan(&s.category), "category")
	must(h.admin.QueryRow(ctx, `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'SVC1', 'Hizmet 1', 'DIRECT', 'COUNT') RETURNING id`, s.tenant, s.category).Scan(&s.service), "service")
	return s
}

func TestCompositeForeignKeyRejectsCrossTenantReference(t *testing.T) {
	h := newHarness(t)
	a := h.seedCore("FK_A")
	tenantB := h.createTenant("FK_B")
	sponsorB := h.createTenantOrganization(tenantB, "B Sponsor", "SPONSOR")
	h.adminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, tenantB)

	// Person belongs to tenant A; membership row claims tenant B -> composite FK fails.
	err := h.adminExecErr(`
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id, membership_type, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', daterange('2026-01-01', NULL, '[)'))`, tenantB, a.person, sponsorB)
	expectSQLState(t, err, stateForeignKeyViolation, "cross-tenant person reference")
}

func TestServiceRequestDraftCanBeCancelled(t *testing.T) {
	h := newHarness(t)
	s := h.seedCore("SR_D1")

	var id uuid.UUID
	ctx, cancel := h.ctx()
	defer cancel()
	err := h.admin.QueryRow(ctx, `
		INSERT INTO service.service_request (tenant_id, request_reference, request_type, person_id, program_id, enrollment_id, service_date, channel)
		VALUES ($1, 'SR-1', 'DIRECT_SERVICE', $2, $3, $4, '2026-09-01', 'BACKOFFICE') RETURNING id`,
		s.tenant, s.person, s.program, s.enrollment).Scan(&id)
	if err != nil {
		t.Fatalf("insert draft: %v", err)
	}

	// D1: DRAFT -> CANCELLED without ever submitting must be allowed.
	h.adminExec(`UPDATE service.service_request SET status = 'CANCELLED', closed_at = clock_timestamp() WHERE id = $1`, id)

	// A non-draft, non-cancelled status still requires submitted_at.
	err = h.adminExecErr(`UPDATE service.service_request SET status = 'SUBMITTED', closed_at = NULL WHERE id = $1`, id)
	expectSQLState(t, err, stateCheckViolation, "submitted without submitted_at")
}

func TestServiceRequestVersionFreezesRequestedValues(t *testing.T) {
	h := newHarness(t)
	s := h.seedCore("SR_D6")
	ctx, cancel := h.ctx()
	defer cancel()

	var reqID, verID, itemID uuid.UUID
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO service.service_request (tenant_id, request_reference, request_type, person_id, program_id, enrollment_id, service_date, channel)
		VALUES ($1, 'SR-2', 'DIRECT_SERVICE', $2, $3, $4, '2026-09-01', 'BACKOFFICE') RETURNING id`,
		s.tenant, s.person, s.program, s.enrollment).Scan(&reqID); err != nil {
		t.Fatalf("insert request: %v", err)
	}
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO service.service_request_version (tenant_id, service_request_id, version_no)
		VALUES ($1, $2, 1) RETURNING id`, s.tenant, reqID).Scan(&verID); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO service.service_request_item (tenant_id, service_request_version_id, line_no, service_definition_id, requested_quantity, unit_type)
		VALUES ($1, $2, 1, $3, 2, 'COUNT') RETURNING id`, s.tenant, verID, s.service).Scan(&itemID); err != nil {
		t.Fatalf("insert item: %v", err)
	}

	// Only one draft version per request.
	err := h.adminExecErr(`INSERT INTO service.service_request_version (tenant_id, service_request_id, version_no) VALUES ($1, $2, 2)`, s.tenant, reqID)
	expectSQLState(t, err, stateUniqueViolation, "second draft version")

	// Drafts are editable.
	h.adminExec(`UPDATE service.service_request_item SET requested_quantity = 3 WHERE id = $1`, itemID)

	// Submit: freeze the version.
	h.adminExec(`UPDATE service.service_request_version
		SET status = 'SUBMITTED', submitted_at = clock_timestamp(), snapshot_json = '{"items":[{"lineNo":1,"requestedQuantity":3}]}'
		WHERE id = $1`, verID)
	h.adminExec(`UPDATE service.service_request SET status = 'SUBMITTED', submitted_at = clock_timestamp() WHERE id = $1`, reqID)

	// Requested values are frozen; decision columns stay writable; no new lines.
	err = h.adminExecErr(`UPDATE service.service_request_item SET requested_quantity = 4 WHERE id = $1`, itemID)
	expectSQLState(t, err, stateIntegrityConstraintRaised, "edit frozen requested_quantity")

	h.adminExec(`UPDATE service.service_request_item SET status = 'PARTIALLY_APPROVED', approved_quantity = 2, decision_reason_code = 'LIMIT' WHERE id = $1`, itemID)

	err = h.adminExecErr(`INSERT INTO service.service_request_item (tenant_id, service_request_version_id, line_no, service_definition_id, requested_quantity, unit_type)
		VALUES ($1, $2, 2, $3, 1, 'COUNT')`, s.tenant, verID, s.service)
	expectSQLState(t, err, stateIntegrityConstraintRaised, "add line to submitted version")

	err = h.adminExecErr(`UPDATE service.service_request_version SET snapshot_json = '{}' WHERE id = $1`, verID)
	expectSQLState(t, err, stateIntegrityConstraintRaised, "edit submitted snapshot")

	// Supersede is the only permitted transition afterwards.
	h.adminExec(`UPDATE service.service_request_version SET status = 'SUPERSEDED' WHERE id = $1`, verID)
}

func TestPublishedPlanVersionIsImmutableAndNonOverlapping(t *testing.T) {
	h := newHarness(t)
	s := h.seedCore("PLAN_V")
	ctx, cancel := h.ctx()
	defer cancel()
	actor := uuid.New()

	var v1 uuid.UUID
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period, published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', daterange('2026-01-01','2027-01-01','[)'), clock_timestamp(), $3) RETURNING id`,
		s.tenant, s.plan, actor).Scan(&v1); err != nil {
		t.Fatalf("publish v1: %v", err)
	}

	err := h.adminExecErr(`
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period, published_at, published_by)
		VALUES ($1, $2, 2, 'PUBLISHED', daterange('2026-06-01','2027-06-01','[)'), clock_timestamp(), $3)`,
		s.tenant, s.plan, actor)
	expectSQLState(t, err, stateExclusionViolation, "overlapping published version")

	err = h.adminExecErr(`UPDATE benefit.plan_version SET valid_period = daterange('2026-01-01','2028-01-01','[)') WHERE id = $1`, v1)
	expectSQLState(t, err, stateIntegrityConstraintRaised, "edit published version")

	err = h.adminExecErr(`DELETE FROM benefit.plan_version WHERE id = $1`, v1)
	expectSQLState(t, err, stateIntegrityConstraintRaised, "delete published version")

	h.adminExec(`UPDATE benefit.plan_version SET status = 'RETIRED' WHERE id = $1`, v1)
}

func TestEnrollmentOverlapRejected(t *testing.T) {
	h := newHarness(t)
	s := h.seedCore("ENR_OV")
	err := h.adminExecErr(`
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, valid_period)
		VALUES ($1, $2, $3, daterange('2026-06-01', NULL, '[)'))`, s.tenant, s.membership, s.plan)
	expectSQLState(t, err, stateExclusionViolation, "overlapping enrollment")
}

func TestLedgerConservationAndAppendOnly(t *testing.T) {
	h := newHarness(t)
	s := h.seedCore("LEDGER")
	ctx, cancel := h.ctx()
	defer cancel()
	actor := uuid.New()

	var pv, def, acct uuid.UUID
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period, published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', daterange('2026-01-01','2027-01-01','[)'), clock_timestamp(), $3) RETURNING id`,
		s.tenant, s.plan, actor).Scan(&pv); err != nil {
		t.Fatalf("plan version: %v", err)
	}
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name, unit_type, period_type, initial_quantity)
		VALUES ($1, $2, 'NIGHTS', 'Gece hakkı', 'NIGHT', 'CALENDAR_YEAR', 7) RETURNING id`, s.tenant, pv).Scan(&def); err != nil {
		t.Fatalf("definition: %v", err)
	}
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO benefit.entitlement_account (tenant_id, enrollment_id, entitlement_definition_id, benefit_period, total_granted, available_quantity)
		VALUES ($1, $2, $3, daterange('2026-01-01','2027-01-01','[)'), 7, 7) RETURNING id`, s.tenant, s.enrollment, def).Scan(&acct); err != nil {
		t.Fatalf("account: %v", err)
	}

	// Balance conservation on the account.
	err := h.adminExecErr(`UPDATE benefit.entitlement_account SET reserved_quantity = 2 WHERE id = $1`, acct)
	expectSQLState(t, err, stateCheckViolation, "account conservation")
	h.adminExec(`UPDATE benefit.entitlement_account SET reserved_quantity = 2, available_quantity = 5 WHERE id = $1`, acct)

	// Ledger conservation and append-only.
	err = h.adminExecErr(`
		INSERT INTO benefit.entitlement_ledger (tenant_id, entitlement_account_id, movement_type, effective_at, delta_available, delta_reserved, reference_type, reference_id, idempotency_key)
		VALUES ($1, $2, 'RESERVE', clock_timestamp(), -2, 1, 'TEST', $3, 'k1')`, s.tenant, acct, uuid.New())
	expectSQLState(t, err, stateCheckViolation, "ledger conservation")

	var ledgerID uuid.UUID
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO benefit.entitlement_ledger (tenant_id, entitlement_account_id, movement_type, effective_at, delta_available, delta_reserved, reference_type, reference_id, idempotency_key)
		VALUES ($1, $2, 'RESERVE', clock_timestamp(), -2, 2, 'TEST', $3, 'k1') RETURNING id`, s.tenant, acct, uuid.New()).Scan(&ledgerID); err != nil {
		t.Fatalf("ledger insert: %v", err)
	}
	err = h.adminExecErr(`
		INSERT INTO benefit.entitlement_ledger (tenant_id, entitlement_account_id, movement_type, effective_at, delta_available, delta_reserved, reference_type, reference_id, idempotency_key)
		VALUES ($1, $2, 'RESERVE', clock_timestamp(), -2, 2, 'TEST', $3, 'k1')`, s.tenant, acct, uuid.New())
	expectSQLState(t, err, stateUniqueViolation, "ledger idempotency")

	err = h.adminExecErr(`UPDATE benefit.entitlement_ledger SET reason_text = 'x' WHERE id = $1`, ledgerID)
	expectSQLState(t, err, stateIntegrityConstraintRaised, "ledger update")
	err = h.adminExecErr(`DELETE FROM benefit.entitlement_ledger WHERE id = $1`, ledgerID)
	expectSQLState(t, err, stateIntegrityConstraintRaised, "ledger delete")
}

func TestIdentifierUniquenessScope(t *testing.T) {
	h := newHarness(t)
	s := h.seedCore("IDENT")
	ctx, cancel := h.ctx()
	defer cancel()

	h.adminExec(`INSERT INTO party.identifier_type (tenant_id, code, display_name, uniqueness_scope) VALUES ($1, 'TCKN', 'TCKN', 'TENANT')`, s.tenant)
	h.adminExec(`INSERT INTO party.identifier_type (tenant_id, code, display_name, uniqueness_scope, is_sensitive) VALUES ($1, 'MEMBER_NO', 'Üye No', 'SPONSOR', false)`, s.tenant)

	var person2 uuid.UUID
	if err := h.admin.QueryRow(ctx, `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Second', 'Person', 'second person') RETURNING id`, s.tenant).Scan(&person2); err != nil {
		t.Fatalf("person2: %v", err)
	}
	hash := make([]byte, 32)
	for i := range hash {
		hash[i] = 0xAB
	}

	// TENANT scope: same hash twice in the tenant is rejected.
	h.adminExec(`INSERT INTO party.person_identifier (tenant_id, person_id, identifier_type, identifier_cipher, identifier_hash, masked_value)
		VALUES ($1, $2, 'TCKN', '\x00'::bytea, $3, '***')`, s.tenant, s.person, hash)
	err := h.adminExecErr(`INSERT INTO party.person_identifier (tenant_id, person_id, identifier_type, identifier_cipher, identifier_hash, masked_value)
		VALUES ($1, $2, 'TCKN', '\x00'::bytea, $3, '***')`, s.tenant, person2, hash)
	expectSQLState(t, err, stateUniqueViolation, "duplicate TCKN in tenant")

	// SPONSOR scope: same member number under two sponsors is fine, same sponsor twice is not.
	otherSponsor := h.createTenantOrganization(s.tenant, "Other Sponsor", "SPONSOR")
	h.adminExec(`INSERT INTO party.person_identifier (tenant_id, person_id, identifier_type, identifier_cipher, identifier_hash, masked_value, scope_key)
		VALUES ($1, $2, 'MEMBER_NO', '\x00'::bytea, $3, '1001', $4)`, s.tenant, s.person, hash, s.sponsorOrg.String())
	h.adminExec(`INSERT INTO party.person_identifier (tenant_id, person_id, identifier_type, identifier_cipher, identifier_hash, masked_value, scope_key)
		VALUES ($1, $2, 'MEMBER_NO', '\x00'::bytea, $3, '1001', $4)`, s.tenant, person2, hash, otherSponsor.String())
	err = h.adminExecErr(`INSERT INTO party.person_identifier (tenant_id, person_id, identifier_type, identifier_cipher, identifier_hash, masked_value, scope_key)
		VALUES ($1, $2, 'MEMBER_NO', '\x00'::bytea, $3, '1001', $4)`, s.tenant, person2, hash, s.sponsorOrg.String())
	expectSQLState(t, err, stateUniqueViolation, "duplicate member no under one sponsor")
}

func TestRowVersionIsOwnedByDatabase(t *testing.T) {
	h := newHarness(t)
	tenant := h.createTenant("ROWVER")
	ctx, cancel := h.ctx()
	defer cancel()

	var before, after int64
	var updatedBefore, updatedAfter time.Time
	if err := h.admin.QueryRow(ctx, `SELECT row_version, updated_at FROM platform.tenant WHERE id = $1`, tenant).Scan(&before, &updatedBefore); err != nil {
		t.Fatalf("read: %v", err)
	}
	h.adminExec(`UPDATE platform.tenant SET display_name = 'Renamed' WHERE id = $1 AND row_version = $2`, tenant, before)
	if err := h.admin.QueryRow(ctx, `SELECT row_version, updated_at FROM platform.tenant WHERE id = $1`, tenant).Scan(&after, &updatedAfter); err != nil {
		t.Fatalf("read: %v", err)
	}
	if after != before+1 {
		t.Fatalf("row_version %d -> %d, want +1", before, after)
	}
	if !updatedAfter.After(updatedBefore) {
		t.Fatalf("updated_at did not advance")
	}

	// Stale row_version updates nothing (optimistic concurrency).
	var tag int64
	err := h.appTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
		t2, err := tx.Exec(ctx, `UPDATE platform.tenant SET display_name = 'Stale' WHERE id = $1 AND row_version = $2`, tenant, before)
		tag = t2.RowsAffected()
		return err
	})
	if err != nil {
		t.Fatalf("stale update: %v", err)
	}
	if tag != 0 {
		t.Fatalf("stale update affected %d rows, want 0", tag)
	}
}

func TestIdempotencyRecordUniquePerCommand(t *testing.T) {
	h := newHarness(t)
	tenant := h.createTenant("IDEMP")
	actor := uuid.New()
	hash := make([]byte, 32)

	insert := func() error {
		return h.appTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO system.idempotency_record (tenant_id, actor_id, command_code, idempotency_key, request_hash)
				VALUES ($1, $2, 'organization.create', 'abcdefghijklmnop', $3)`, tenant, actor, hash)
			return err
		})
	}
	if err := insert(); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	expectSQLState(t, insert(), stateUniqueViolation, "duplicate idempotency key")
}
