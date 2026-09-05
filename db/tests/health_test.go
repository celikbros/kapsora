package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// healthSeed is one tenant with everything migration 000031 hangs together: a member
// enrolled in a plan, a provider, a small ICD-10 code system with one ordinary code and one
// the publisher marked sensitive, and a case with an encounter.
type healthSeed struct {
	tenant     uuid.UUID
	actor      uuid.UUID
	provider   uuid.UUID
	person     uuid.UUID
	program    uuid.UUID
	enrollment uuid.UUID
	codeSystem uuid.UUID
	codePlain  uuid.UUID
	codeStrict uuid.UUID
	healthCase uuid.UUID
	encounter  uuid.UUID
}

func seedHealth(h *dbtest.Harness, code string) healthSeed { //nolint:funlen // one linear fixture reads better whole
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	s := healthSeed{tenant: h.CreateTenant(code)}
	s.actor = h.CreateActor("health-"+code, "Health Clerk "+code)
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

	// ICD-10 is a code system like any other and WP-I5-05 seeds the real one. This fixture
	// seeds two codes of its own, because what this package needs from the catalogue is not
	// the content: it is that a code value can say, in its own attributes, that its category
	// is one v1.2 11.10 protects.
	scan(&s.codeSystem, "code system", `
		INSERT INTO catalog.code_system (tenant_id, code, name, version, authority, valid_from)
		VALUES ($1, 'ICD10', 'ICD-10', '2026', 'WHO', '2026-01-01') RETURNING id`, s.tenant)
	scan(&s.codePlain, "plain code value", `
		INSERT INTO catalog.code_value (tenant_id, code_system_id, code, display, valid_from)
		VALUES ($1, $2, 'J06.9', 'Üst solunum yolu enfeksiyonu', '2026-01-01') RETURNING id`,
		s.tenant, s.codeSystem)
	scan(&s.codeStrict, "sensitive code value", `
		INSERT INTO catalog.code_value (tenant_id, code_system_id, code, display, valid_from, attributes)
		VALUES ($1, $2, 'F32.1', 'Orta düzeyde depresif atak', '2026-01-01',
		        '{"chapter":"V","sensitive":true}'::jsonb) RETURNING id`,
		s.tenant, s.codeSystem)

	scan(&s.healthCase, "health case", `
		INSERT INTO health.health_case (tenant_id, person_id, program_id, enrollment_id, case_type,
		                                provider_organization_id, created_by)
		VALUES ($1, $2, $3, $4, 'OUTPATIENT', $5, $6) RETURNING id`,
		s.tenant, s.person, s.program, s.enrollment, s.provider, s.actor)
	scan(&s.encounter, "encounter", `
		INSERT INTO health.encounter (tenant_id, case_id, encounter_type, started_at, ended_at,
		                              branch_code, notes_clinical, created_by)
		VALUES ($1, $2, 'OUTPATIENT', '2026-06-15T09:00:00Z', '2026-06-15T09:40:00Z',
		        'PSK', 'Hasta uyku düzeninden şikayetçi.', $3) RETURNING id`,
		s.tenant, s.healthCase, s.actor)
	return s
}

// TestOnePrimaryDiagnosisPerEncounter is the invariant every downstream rule rests on:
// "what was this for" has exactly one answer. The application layer answers 422 before it
// gets here; this is the half that holds whatever writes the row.
func TestOnePrimaryDiagnosisPerEncounter(t *testing.T) {
	h := dbtest.New(t)
	s := seedHealth(h, "HEALTH_PRIMARY")

	insert := func(codeValue uuid.UUID, diagnosisType string) error {
		return h.AdminExecErr(`
			INSERT INTO health.diagnosis (tenant_id, encounter_id, code_system_id, code_value_id,
			                              diagnosis_type, recorded_by)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			s.tenant, s.encounter, s.codeSystem, codeValue, diagnosisType, s.actor)
	}
	if err := insert(s.codePlain, "PRIMARY"); err != nil {
		t.Fatalf("first primary diagnosis: %v", err)
	}
	if err := insert(s.codeStrict, "SECONDARY"); err != nil {
		t.Fatalf("secondary diagnosis beside it: %v", err)
	}
	err := insert(s.codeStrict, "PRIMARY")
	if dbtest.SQLState(err) != dbtest.SQLStateUniqueViolation {
		t.Fatalf("second primary diagnosis: %v, want a unique violation", err)
	}
}

// TestHealthCaseClosureIsOneFact: a case is CLOSED with a moment or OPEN without one, and
// the two halves cannot come apart.
func TestHealthCaseClosureIsOneFact(t *testing.T) {
	h := dbtest.New(t)
	s := seedHealth(h, "HEALTH_CLOSE")

	err := h.AdminExecErr(`
		UPDATE health.health_case SET status = 'CLOSED' WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.healthCase)
	if dbtest.SQLState(err) != dbtest.SQLStateCheckViolation {
		t.Fatalf("close without a moment: %v, want a check violation", err)
	}
	err = h.AdminExecErr(`
		UPDATE health.health_case SET closed_at = clock_timestamp() WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.healthCase)
	if dbtest.SQLState(err) != dbtest.SQLStateCheckViolation {
		t.Fatalf("a moment without the status: %v, want a check violation", err)
	}
	if err := h.AdminExecErr(`
		UPDATE health.health_case SET status = 'CLOSED', closed_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.healthCase); err != nil {
		t.Fatalf("close with both halves: %v", err)
	}
	// And a close before the case was opened is not a close anybody can read.
	err = h.AdminExecErr(`
		UPDATE health.health_case SET closed_at = opened_at - interval '1 day'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.healthCase)
	if dbtest.SQLState(err) != dbtest.SQLStateCheckViolation {
		t.Fatalf("close before open: %v, want a check violation", err)
	}
}

// TestEncounterPeriod: an encounter that ended before it started is not a record of
// anything.
func TestEncounterPeriod(t *testing.T) {
	h := dbtest.New(t)
	s := seedHealth(h, "HEALTH_PERIOD")

	err := h.AdminExecErr(`
		UPDATE health.encounter SET ended_at = started_at - interval '1 hour'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.encounter)
	if dbtest.SQLState(err) != dbtest.SQLStateCheckViolation {
		t.Fatalf("ended before started: %v, want a check violation", err)
	}
}

// TestHealthTenantIsolation: every table of the module is invisible across tenants through
// the application role, and a write tagged with another tenant fails the policy's WITH
// CHECK. A clinical schema is the one where "RLS is probably on" is not good enough.
func TestHealthTenantIsolation(t *testing.T) {
	h := dbtest.New(t)
	a := seedHealth(h, "HEALTH_RLS_A")
	b := seedHealth(h, "HEALTH_RLS_B")
	h.AdminExec(`
		INSERT INTO health.diagnosis (tenant_id, encounter_id, code_system_id, code_value_id,
		                              diagnosis_type, sensitive, recorded_by)
		VALUES ($1, $2, $3, $4, 'PRIMARY', true, $5)`,
		a.tenant, a.encounter, a.codeSystem, a.codeStrict, a.actor)

	countIn := func(tenant uuid.UUID, table, where string, args ...any) int {
		t.Helper()
		var n int
		err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE `+where, args...).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count %s for %s: %v", table, tenant, err)
		}
		return n
	}
	for _, probe := range []struct {
		table string
		id    uuid.UUID
	}{
		{"health.health_case", a.healthCase},
		{"health.encounter", a.encounter},
	} {
		if got := countIn(a.tenant, probe.table, "id = $1", probe.id); got != 1 {
			t.Fatalf("%s: tenant A sees %d of its own row, want 1", probe.table, got)
		}
		if got := countIn(b.tenant, probe.table, "id = $1", probe.id); got != 0 {
			t.Fatalf("%s: tenant B sees %d of tenant A's rows", probe.table, got)
		}
	}
	if got := countIn(a.tenant, "health.diagnosis", "encounter_id = $1", a.encounter); got != 1 {
		t.Fatalf("health.diagnosis: tenant A sees %d of its own rows, want 1", got)
	}
	if got := countIn(b.tenant, "health.diagnosis", "encounter_id = $1", a.encounter); got != 0 {
		t.Fatalf("health.diagnosis: tenant B sees tenant A's diagnosis")
	}

	// A write tagged with another tenant fails the WITH CHECK of the policy.
	err := h.AppTx(b.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `
			INSERT INTO health.health_case (tenant_id, person_id, program_id, enrollment_id, case_type)
			VALUES ($1, $2, $3, $4, 'OUTPATIENT')`,
			a.tenant, a.person, a.program, a.enrollment)
		return execErr
	})
	if dbtest.SQLState(err) != dbtest.SQLStateInsufficientPrivilege {
		t.Fatalf("cross-tenant insert: %v, want an RLS refusal", err)
	}
}

// TestClinicalAccessPurposesSeeded: the six purposes of v1.2 11.10 are there, with Turkish
// labels, and they are tenant-independent — a tenant that could add one could add "OTHER"
// and make the whole column meaningless.
func TestClinicalAccessPurposesSeeded(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	for _, code := range []string{
		"TREATMENT", "PRE_AUTHORIZATION", "CLAIM_REVIEW", "MEDICAL_REVIEW", "AUDIT", "MEMBER_REQUEST",
	} {
		var label string
		if err := h.Admin.QueryRow(ctx,
			`SELECT display_name FROM health.clinical_access_purpose WHERE purpose_code = $1`,
			code).Scan(&label); err != nil {
			t.Fatalf("purpose %s is not seeded: %v", code, err)
		}
		if label == "" {
			t.Fatalf("purpose %s has no Turkish label", code)
		}
	}
	var hasTenant bool
	if err := h.Admin.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.columns
		                WHERE table_schema = 'health' AND table_name = 'clinical_access_purpose'
		                  AND column_name = 'tenant_id')`).Scan(&hasTenant); err != nil {
		t.Fatalf("read columns: %v", err)
	}
	if hasTenant {
		t.Fatal("clinical_access_purpose has a tenant_id: the purposes are the same everywhere")
	}
}

// TestHealthPermissionsAreSeededAndGrantable checks both halves of the same fact. The
// catalogue row and the role template have to agree, because a permission that exists in
// one and not the other is a permission nobody can hold or one nobody can be given.
//
// The second half of this test is the one WP-I5-01 exists for: SPONSOR_HR holds
// health.case.read and must never hold health.clinical.read. A permission quietly added to
// that template would defeat the whole package without a line of it changing, so the
// absence is asserted rather than assumed.
func TestHealthPermissionsAreSeededAndGrantable(t *testing.T) {
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
	for _, code := range []string{
		"health.case.read", "health.case.manage", "health.clinical.read", "health.sensitive.read",
	} {
		var n int
		if err := h.Admin.QueryRow(ctx,
			`SELECT count(*) FROM iam.permission WHERE code = $1`, code).Scan(&n); err != nil {
			t.Fatalf("read permission %s: %v", code, err)
		}
		if n != 1 {
			t.Fatalf("permission %s is seeded %d times, want once", code, n)
		}
		if len(granted[code]) == 0 {
			t.Fatalf("permission %s is in the catalogue but in no role template: nobody can hold it", code)
		}
	}
	var sensitivity string
	if err := h.Admin.QueryRow(ctx,
		`SELECT sensitivity FROM iam.permission WHERE code = 'health.sensitive.read'`).Scan(&sensitivity); err != nil {
		t.Fatalf("read health.sensitive.read: %v", err)
	}
	if sensitivity != "SENSITIVE" {
		t.Fatalf("health.sensitive.read sensitivity = %s, want SENSITIVE", sensitivity)
	}

	if _, ok := byRole["SPONSOR_HR"]; !ok {
		t.Fatal("there is no SPONSOR_HR role template: the acceptance criterion has no subject")
	}
	for _, want := range []struct{ role, permission string }{
		{"SPONSOR_HR", "member.read"},
		{"SPONSOR_HR", "service_request.read"},
		{"SPONSOR_HR", "health.case.read"},
		{"SPONSOR_HR", "entitlement.read"},
		{"SPONSOR_HR", "report.read"},
		{"MEDICAL_REVIEWER", "health.clinical.read"},
		{"MEDICAL_REVIEWER", "health.sensitive.read"},
		{"PROVIDER_STAFF", "health.case.manage"},
	} {
		if !byRole[want.role][want.permission] {
			t.Fatalf("role %s does not hold %s", want.role, want.permission)
		}
	}
	// The whole point of the role, as an assertion.
	for _, forbidden := range []string{"health.clinical.read", "health.sensitive.read", "health.case.manage"} {
		if byRole["SPONSOR_HR"][forbidden] {
			t.Fatalf("SPONSOR_HR holds %s; a sponsor's HR user could then see clinical detail", forbidden)
		}
	}
	// v1.2 11.10: the extra grant belongs to the medical reviewer and to nobody else.
	for _, role := range granted["health.sensitive.read"] {
		if role != "MEDICAL_REVIEWER" {
			t.Fatalf("role %s holds health.sensitive.read; only MEDICAL_REVIEWER may", role)
		}
	}
}
