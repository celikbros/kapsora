package main

import (
	"testing"

	"github.com/google/uuid"

	organization "github.com/celikbros/kapsora/internal/organization/domain"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// The staff member's identifier has to be a TCKN the party module accepts — it runs the same
// checksum — and it must not be the demo member's, or the second person would collide with the
// first on the blind index.
func TestStaffMemberTCKNIsASyntheticValidTCKN(t *testing.T) {
	if err := organization.ValidateTCKN(staffMemberTCKN); err != nil {
		t.Fatalf("%s is not a valid TCKN: %v", staffMemberTCKN, err)
	}
	if staffMemberTCKN == demoMemberTCKN {
		t.Fatalf("the staff member and the demo member share the TCKN %s", staffMemberTCKN)
	}
}

// assertStaffMember checks the staff member: one account with exactly two grants in DEMO_A —
// MEDICAL_REVIEWER for the tenant and MEMBER bound to Deniz Çalışan, not to Melis — enrolled
// in the demo plan, with a service request waiting in PENDING_REVIEW and a medical report
// waiting in SUBMITTED with a clean document behind it.
//
//nolint:funlen // one list of facts about one account
func assertStaffMember(t *testing.T, h *dbtest.Harness) {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	var tenantA uuid.UUID
	if err := h.Admin.QueryRow(ctx,
		`SELECT id FROM platform.tenant WHERE code = 'DEMO_A'`).Scan(&tenantA); err != nil {
		t.Fatalf("find DEMO_A: %v", err)
	}

	rows, err := h.Admin.Query(ctx, `
		SELECT r.code, g.scope_type, g.scope_id
		  FROM iam.access_grant g
		  JOIN iam.role r ON r.tenant_id = g.tenant_id AND r.id = g.role_id
		  JOIN iam.tenant_membership m ON m.tenant_id = g.tenant_id AND m.id = g.tenant_membership_id
		  JOIN iam.actor a ON a.id = m.actor_id
		 WHERE g.tenant_id = $1 AND a.identity_subject = $2
		 ORDER BY r.code`, tenantA, staffMemberUsername)
	if err != nil {
		t.Fatalf("read the grants of %s: %v", staffMemberUsername, err)
	}
	type grant struct {
		role, scopeType string
		scopeID         uuid.NullUUID
	}
	var grants []grant
	for rows.Next() {
		var g grant
		if err := rows.Scan(&g.role, &g.scopeType, &g.scopeID); err != nil {
			rows.Close()
			t.Fatalf("scan a grant of %s: %v", staffMemberUsername, err)
		}
		grants = append(grants, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read the grants of %s: %v", staffMemberUsername, err)
	}
	if len(grants) != 2 {
		t.Fatalf("%s holds %d grants in DEMO_A (%v), want MEDICAL_REVIEWER and MEMBER",
			staffMemberUsername, len(grants), grants)
	}
	if g := grants[0]; g.role != "MEDICAL_REVIEWER" || g.scopeType != "TENANT" || g.scopeID.Valid {
		t.Errorf("%s: first grant is %+v, want MEDICAL_REVIEWER TENANT", staffMemberUsername, g)
	}
	member := grants[1]
	if member.role != "MEMBER" || member.scopeType != "PERSON" || !member.scopeID.Valid {
		t.Fatalf("%s: second grant is %+v, want MEMBER PERSON <person>", staffMemberUsername, member)
	}
	person := member.scopeID.UUID

	var first, last string
	if err := h.Admin.QueryRow(ctx,
		`SELECT first_name, last_name FROM party.person WHERE tenant_id = $1 AND id = $2`,
		tenantA, person).Scan(&first, &last); err != nil {
		t.Fatalf("read the person %s is bound to: %v", staffMemberUsername, err)
	}
	if first != staffMemberFirstName || last != staffMemberLastName {
		t.Errorf("%s is bound to %s %s, want %s %s", staffMemberUsername, first, last,
			staffMemberFirstName, staffMemberLastName)
	}
	var melis uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		SELECT g.scope_id
		  FROM iam.access_grant g
		  JOIN iam.role r ON r.tenant_id = g.tenant_id AND r.id = g.role_id
		  JOIN iam.tenant_membership m ON m.tenant_id = g.tenant_id AND m.id = g.tenant_membership_id
		  JOIN iam.actor a ON a.id = m.actor_id
		 WHERE g.tenant_id = $1 AND r.code = 'MEMBER' AND a.identity_subject = $2`,
		tenantA, demoMemberUsername).Scan(&melis); err != nil {
		t.Fatalf("read the binding of %s: %v", demoMemberUsername, err)
	}
	if melis == person {
		t.Errorf("%s and %s are bound to the same person", staffMemberUsername, demoMemberUsername)
	}

	count := func(what, sql string, args ...any) int {
		t.Helper()
		var n int
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return n
	}
	if n := count("the staff member's enrollment", `
		SELECT count(*) FROM benefit.enrollment e
		  JOIN party.sponsor_membership sm ON sm.tenant_id = e.tenant_id AND sm.id = e.sponsor_membership_id
		 WHERE e.tenant_id = $1 AND sm.person_id = $2 AND e.status = 'ACTIVE'`,
		tenantA, person); n != 1 {
		t.Errorf("%s has %d active enrollments, want one", staffMemberUsername, n)
	}
	if n := count("the staff member's entitlement accounts", `
		SELECT count(*) FROM benefit.entitlement_account ea
		  JOIN benefit.enrollment e ON e.tenant_id = ea.tenant_id AND e.id = ea.enrollment_id
		  JOIN party.sponsor_membership sm ON sm.tenant_id = e.tenant_id AND sm.id = e.sponsor_membership_id
		 WHERE ea.tenant_id = $1 AND sm.person_id = $2`, tenantA, person); n == 0 {
		t.Errorf("%s has no entitlement account; nothing she asks for could be eligible",
			staffMemberUsername)
	}

	var requestStatus string
	if err := h.Admin.QueryRow(ctx, `
		SELECT status FROM service.service_request
		 WHERE tenant_id = $1 AND person_id = $2 AND request_type = 'DIRECT_SERVICE'`,
		tenantA, person).Scan(&requestStatus); err != nil {
		t.Fatalf("read the request of %s (want exactly one): %v", staffMemberUsername, err)
	}
	if requestStatus != "PENDING_REVIEW" {
		t.Errorf("the request of %s is %s, want PENDING_REVIEW", staffMemberUsername, requestStatus)
	}

	var reportID uuid.UUID
	var reportStatus string
	if err := h.Admin.QueryRow(ctx, `
		SELECT id, status FROM health.medical_report WHERE tenant_id = $1 AND person_id = $2`,
		tenantA, person).Scan(&reportID, &reportStatus); err != nil {
		t.Fatalf("read the medical report of %s (want exactly one): %v", staffMemberUsername, err)
	}
	if reportStatus != "SUBMITTED" {
		t.Errorf("the medical report of %s is %s, want SUBMITTED", staffMemberUsername, reportStatus)
	}
	if n := count("the report's clean document", `
		SELECT count(*) FROM document.link l
		  JOIN document.object o ON o.tenant_id = l.tenant_id AND o.id = l.object_id
		 WHERE l.tenant_id = $1 AND l.aggregate_type = 'MEDICAL_REPORT' AND l.aggregate_id = $2
		   AND o.scan_status = 'CLEAN'`, tenantA, reportID); n != 1 {
		t.Errorf("the medical report of %s has %d clean documents, want one", staffMemberUsername, n)
	}
	if n := count("the report's work item", `
		SELECT count(*) FROM workflow.work_item
		 WHERE tenant_id = $1 AND aggregate_type = 'MEDICAL_REPORT' AND aggregate_id = $2`,
		tenantA, reportID); n != 1 {
		t.Errorf("the medical report of %s raised %d work items, want one", staffMemberUsername, n)
	}
}
