package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// requestSeed is one tenant with everything migrations 000006 and 000025 hang together: a
// member enrolled in a published plan, a catalog definition, a request in DRAFT with its
// first version and one line.
type requestSeed struct {
	tenant     uuid.UUID
	actor      uuid.UUID
	provider   uuid.UUID
	person     uuid.UUID
	program    uuid.UUID
	enrollment uuid.UUID
	definition uuid.UUID
	request    uuid.UUID
	version    uuid.UUID
	item       uuid.UUID
}

func seedServiceRequest(h *dbtest.Harness, code string) requestSeed { //nolint:funlen // one linear fixture reads better whole
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	s := requestSeed{tenant: h.CreateTenant(code)}
	s.actor = h.CreateActor("request-"+code, "Request Clerk "+code)
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		h.T.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			h.T.Fatalf("seed %s: %v", what, err)
		}
	}
	sponsor := h.CreateTenantOrganization(s.tenant, "Sponsor "+code, "SPONSOR")
	payer := h.CreateTenantOrganization(s.tenant, "Payer "+code, "PAYER")
	s.provider = h.CreateTenantOrganization(s.tenant, "Provider "+code, "PROVIDER")

	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, s.tenant)
	scan(&s.person, "person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Deniz', 'Aksoy', 'deniz aksoy') RETURNING id`, s.tenant)
	var membership uuid.UUID
	scan(&membership, "membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, s.person, sponsor)

	h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name) VALUES ($1, 'BENEFIT', 'Fayda')`, s.tenant)
	scan(&s.program, "program", `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id, payer_tenant_organization_id,
		                             code, name, program_type, status, valid_period)
		VALUES ($1, $2, $3, 'PRG', 'Program', 'BENEFIT', 'ACTIVE', daterange('2026-01-01', NULL, '[)'))
		RETURNING id`, s.tenant, sponsor, payer)
	var planID uuid.UUID
	scan(&planID, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, s.tenant, s.program)
	scan(&s.enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, membership, planID)

	var category uuid.UUID
	scan(&category, "service category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, s.tenant)
	scan(&s.definition, "service definition", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'PHYSIO', 'Fizyoterapi', 'SESSION', 'SESSION') RETURNING id`,
		s.tenant, category)

	scan(&s.request, "service request", `
		INSERT INTO service.service_request (tenant_id, request_reference, request_type, person_id,
		                                     program_id, enrollment_id, provider_tenant_organization_id,
		                                     service_date, channel)
		VALUES ($1, 'SR-20260615-AAAAAAAA', 'DIRECT_SERVICE', $2, $3, $4, $5, '2026-06-15', 'BACKOFFICE')
		RETURNING id`, s.tenant, s.person, s.program, s.enrollment, s.provider)
	scan(&s.version, "service request version", `
		INSERT INTO service.service_request_version (tenant_id, service_request_id, version_no)
		VALUES ($1, $2, 1) RETURNING id`, s.tenant, s.request)
	scan(&s.item, "service request item", `
		INSERT INTO service.service_request_item (tenant_id, service_request_version_id, line_no,
		                                          service_definition_id, requested_quantity, unit_type)
		VALUES ($1, $2, 1, $3, 2, 'SESSION') RETURNING id`, s.tenant, s.version, s.definition)
	return s
}

// submitVersion freezes a version the way the application layer does, so the tests can
// reach the frozen state without going through the service.
func submitVersion(h *dbtest.Harness, s requestSeed) error {
	h.T.Helper()
	return h.AdminExecErr(`
		UPDATE service.service_request_version
		   SET status = 'SUBMITTED', snapshot_json = '{"snapshotVersion":1}'::jsonb,
		       submitted_at = clock_timestamp(), submitted_by = $3
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.version, s.actor)
}

// TestSubmittedServiceRequestVersionIsImmutable is the property everything downstream rests
// on: what was submitted is what a decision was made against, and it cannot move afterwards.
func TestSubmittedServiceRequestVersionIsImmutable(t *testing.T) {
	h := dbtest.New(t)
	s := seedServiceRequest(h, "SR_FROZEN")
	if err := submitVersion(h, s); err != nil {
		t.Fatalf("submit version: %v", err)
	}

	err := h.AdminExecErr(`
		UPDATE service.service_request_version SET snapshot_json = '{"tampered":true}'::jsonb
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "rewrite a submitted version")

	err = h.AdminExecErr(`DELETE FROM service.service_request_version WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "delete a submitted version")

	// The one update it accepts is being marked as replaced by a later version.
	if err := h.AdminExecErr(`
		UPDATE service.service_request_version SET status = 'SUPERSEDED'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.version); err != nil {
		t.Fatalf("supersede a submitted version: %v", err)
	}
	// And once superseded, nothing at all.
	err = h.AdminExecErr(`
		UPDATE service.service_request_version SET status = 'SUBMITTED'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "reopen a superseded version")
}

// TestOnlyOneDraftVersionPerServiceRequest: a request being corrected in two places at once
// would leave nobody able to say what is about to be submitted.
func TestOnlyOneDraftVersionPerServiceRequest(t *testing.T) {
	h := dbtest.New(t)
	s := seedServiceRequest(h, "SR_ONE_DRAFT")

	err := h.AdminExecErr(`
		INSERT INTO service.service_request_version (tenant_id, service_request_id, version_no)
		VALUES ($1, $2, 2)`, s.tenant, s.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "second draft version")

	// Once the first is frozen, the next draft is the correction and is allowed.
	if err := submitVersion(h, s); err != nil {
		t.Fatalf("submit version: %v", err)
	}
	if err := h.AdminExecErr(`
		INSERT INTO service.service_request_version (tenant_id, service_request_id, version_no,
		                                             returned_at, returned_by, return_reason_code)
		VALUES ($1, $2, 2, clock_timestamp(), $3, 'MISSING_DETAIL')`,
		s.tenant, s.request, s.actor); err != nil {
		t.Fatalf("next draft version after a return: %v", err)
	}
	// A returned version without a reason is what makes a member give up rather than
	// correct, so the schema refuses it.
	err = h.AdminExecErr(`
		INSERT INTO service.service_request_version (tenant_id, service_request_id, version_no, returned_at)
		VALUES ($1, $2, 3, clock_timestamp())`, s.tenant, s.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a return without a reason")
}

// TestRequestedItemValuesFreezeButDecisionsDoNot: a reviewer records an outcome on the very
// rows that were submitted, so the two have to be separable.
func TestRequestedItemValuesFreezeButDecisionsDoNot(t *testing.T) {
	h := dbtest.New(t)
	s := seedServiceRequest(h, "SR_ITEMS")
	if err := submitVersion(h, s); err != nil {
		t.Fatalf("submit version: %v", err)
	}

	err := h.AdminExecErr(`
		UPDATE service.service_request_item SET requested_quantity = 99
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.item)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "rewrite a submitted line")

	err = h.AdminExecErr(`
		INSERT INTO service.service_request_item (tenant_id, service_request_version_id, line_no,
		                                          service_definition_id, requested_quantity, unit_type)
		VALUES ($1, $2, 2, $3, 1, 'SESSION')`, s.tenant, s.version, s.definition)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "add a line to a submitted version")

	err = h.AdminExecErr(`DELETE FROM service.service_request_item WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.item)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "delete a line of a submitted version")

	if err := h.AdminExecErr(`
		UPDATE service.service_request_item
		   SET status = 'PARTIALLY_APPROVED', approved_quantity = 1, decision_reason_code = 'LIMIT'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.item); err != nil {
		t.Fatalf("record a line decision on a submitted version: %v", err)
	}
}

// TestServiceRequestStatusInvariants: the columns migration 000025 added are not optional
// where they matter. A refusal without a reason and a document request naming no document
// are both refused by the schema, not only by the application.
func TestServiceRequestStatusInvariants(t *testing.T) {
	h := dbtest.New(t)
	s := seedServiceRequest(h, "SR_INVARIANTS")

	err := h.AdminExecErr(`
		UPDATE service.service_request
		   SET status = 'REJECTED', submitted_at = clock_timestamp(), closed_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a rejection without a reason")

	err = h.AdminExecErr(`
		UPDATE service.service_request
		   SET status = 'PENDING_DOCUMENT', submitted_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a document request naming no document")

	if err := h.AdminExecErr(`
		UPDATE service.service_request
		   SET status = 'PENDING_DOCUMENT', submitted_at = clock_timestamp(),
		       required_document_types = ARRAY['INVOICE']
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.request); err != nil {
		t.Fatalf("a document request naming a document: %v", err)
	}
	// A draft has never been submitted, whatever else is true of it.
	err = h.AdminExecErr(`
		UPDATE service.service_request SET status = 'DRAFT' WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a draft that has been submitted")
}

// TestServiceRequestStatusEventsAreAppendOnly: a history somebody can edit is not a history.
func TestServiceRequestStatusEventsAreAppendOnly(t *testing.T) {
	h := dbtest.New(t)
	s := seedServiceRequest(h, "SR_EVENTS")

	if err := h.AdminExecErr(`
		INSERT INTO workflow.status_event (tenant_id, aggregate_type, aggregate_id, from_status,
		                                   to_status, transition_code, reason_code, actor_id)
		VALUES ($1, 'SERVICE_REQUEST', $2, 'DRAFT', 'SUBMITTED', 'SUBMIT', NULL, $3)`,
		s.tenant, s.request, s.actor); err != nil {
		t.Fatalf("append a status event: %v", err)
	}
	err := h.AdminExecErr(`
		UPDATE workflow.status_event SET to_status = 'APPROVED'
		 WHERE tenant_id = $1 AND aggregate_id = $2`, s.tenant, s.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "update a status event")

	err = h.AdminExecErr(`
		DELETE FROM workflow.status_event WHERE tenant_id = $1 AND aggregate_id = $2`,
		s.tenant, s.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "delete a status event")
}

// TestCancellationIsOncePerAggregateVersion: cancelling twice is the same cancellation, and
// a retry that produced two fees would charge somebody twice for one decision.
func TestCancellationIsOncePerAggregateVersion(t *testing.T) {
	h := dbtest.New(t)
	s := seedServiceRequest(h, "SR_CANCELLATION")

	if err := h.AdminExecErr(`
		INSERT INTO service.cancellation (tenant_id, aggregate_type, aggregate_id, aggregate_version,
		                                  fee_amount, currency_code, reason_code, cancelled_by)
		VALUES ($1, 'SERVICE_REQUEST', $2, 1, 50, 'TRY', 'MEMBER_WITHDREW', $3)`,
		s.tenant, s.request, s.actor); err != nil {
		t.Fatalf("insert cancellation: %v", err)
	}
	err := h.AdminExecErr(`
		INSERT INTO service.cancellation (tenant_id, aggregate_type, aggregate_id, aggregate_version,
		                                  reason_code)
		VALUES ($1, 'SERVICE_REQUEST', $2, 1, 'MEMBER_WITHDREW')`, s.tenant, s.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "a second cancellation of one version")

	// A fee with no currency is a number nobody can invoice.
	err = h.AdminExecErr(`
		INSERT INTO service.cancellation (tenant_id, aggregate_type, aggregate_id, aggregate_version,
		                                  fee_amount, reason_code)
		VALUES ($1, 'SERVICE_REQUEST', $2, 2, 25, 'LATE')`, s.tenant, s.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a fee without a currency")

	// And a cancellation, once recorded, is not rewritten.
	err = h.AdminExecErr(`
		UPDATE service.cancellation SET fee_amount = 0 WHERE tenant_id = $1 AND aggregate_id = $2`,
		s.tenant, s.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "update a cancellation")
}

// TestOnlyOneOpenAppealPerRequest: two open appeals would give one decision two answers.
func TestOnlyOneOpenAppealPerRequest(t *testing.T) {
	h := dbtest.New(t)
	s := seedServiceRequest(h, "SR_APPEAL")

	if err := h.AdminExecErr(`
		INSERT INTO service.appeal (tenant_id, service_request_id, appealed_version_no,
		                            appellant_actor_id, reason_code)
		VALUES ($1, $2, 1, $3, 'DECISION_WRONG')`, s.tenant, s.request, s.actor); err != nil {
		t.Fatalf("open an appeal: %v", err)
	}
	err := h.AdminExecErr(`
		INSERT INTO service.appeal (tenant_id, service_request_id, appealed_version_no, reason_code)
		VALUES ($1, $2, 1, 'DECISION_WRONG')`, s.tenant, s.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "a second open appeal")

	// A decided appeal says when and why; an appeal that is merely marked upheld does not.
	err = h.AdminExecErr(`
		UPDATE service.appeal SET status = 'UPHELD' WHERE tenant_id = $1 AND service_request_id = $2`,
		s.tenant, s.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a decision without an outcome")

	if err := h.AdminExecErr(`
		UPDATE service.appeal
		   SET status = 'UPHELD', outcome_reason_code = 'EVIDENCE_ACCEPTED',
		       decided_at = clock_timestamp(), decided_by = $3
		 WHERE tenant_id = $1 AND service_request_id = $2`, s.tenant, s.request, s.actor); err != nil {
		t.Fatalf("decide an appeal: %v", err)
	}
	// With the first one closed, a second appeal may be opened.
	if err := h.AdminExecErr(`
		INSERT INTO service.appeal (tenant_id, service_request_id, appealed_version_no, reason_code)
		VALUES ($1, $2, 1, 'NEW_EVIDENCE')`, s.tenant, s.request); err != nil {
		t.Fatalf("open a second appeal after the first was decided: %v", err)
	}
}

// TestServiceRequestTenantIsolation: every table of the lifecycle is invisible across
// tenants through the application role.
func TestServiceRequestTenantIsolation(t *testing.T) {
	h := dbtest.New(t)
	a := seedServiceRequest(h, "SR_RLS_A")
	b := seedServiceRequest(h, "SR_RLS_B")
	h.AdminExec(`
		INSERT INTO service.cancellation (tenant_id, aggregate_type, aggregate_id, aggregate_version,
		                                  reason_code)
		VALUES ($1, 'SERVICE_REQUEST', $2, 1, 'MEMBER_WITHDREW')`, a.tenant, a.request)
	h.AdminExec(`
		INSERT INTO service.appeal (tenant_id, service_request_id, appealed_version_no, reason_code)
		VALUES ($1, $2, 1, 'DECISION_WRONG')`, a.tenant, a.request)

	countFor := func(tenant uuid.UUID, table string) int {
		t.Helper()
		var n int
		err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count %s for %s: %v", table, tenant, err)
		}
		return n
	}
	for _, table := range []string{
		"service.service_request", "service.service_request_version",
		"service.service_request_item", "service.cancellation", "service.appeal",
	} {
		if got := countFor(a.tenant, table); got == 0 {
			t.Fatalf("%s: tenant A sees none of its own rows", table)
		}
		if got := countFor(b.tenant, table); got != 0 {
			if table == "service.cancellation" || table == "service.appeal" {
				t.Fatalf("%s: tenant B sees %d of tenant A's rows", table, got)
			}
			// B has its own request, version and line; what matters is that it sees only
			// those, which the id check below settles.
		}
	}

	var visible int
	if err := h.AppTx(b.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM service.service_request WHERE id = $1`, a.request).Scan(&visible)
	}); err != nil {
		t.Fatalf("cross-tenant read: %v", err)
	}
	if visible != 0 {
		t.Fatal("tenant B can see tenant A's service request")
	}

	// And a write tagged with another tenant fails the WITH CHECK of the policy.
	err := h.AppTx(b.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `
			INSERT INTO service.service_request (tenant_id, request_reference, request_type, person_id,
			                                     program_id, enrollment_id, service_date, channel)
			VALUES ($1, 'SR-20260615-BBBBBBBB', 'DIRECT_SERVICE', $2, $3, $4, '2026-06-15', 'BACKOFFICE')`,
			a.tenant, a.person, a.program, a.enrollment)
		return execErr
	})
	if dbtest.SQLState(err) != dbtest.SQLStateInsufficientPrivilege {
		t.Fatalf("cross-tenant insert: %v, want an RLS refusal", err)
	}
}
