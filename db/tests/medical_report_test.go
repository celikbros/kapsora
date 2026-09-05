package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// reportSeed is one health seed plus a catalogue service a report may cover.
type reportSeed struct {
	healthSeed
	service uuid.UUID
}

func seedReport(h *dbtest.Harness, code string) reportSeed {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	s := reportSeed{healthSeed: seedHealth(h, code)}
	var category uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, s.tenant).Scan(&category); err != nil {
		h.T.Fatalf("seed service category: %v", err)
	}
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'PHYSIO', 'Fizyoterapi', 'SESSION', 'SESSION') RETURNING id`,
		s.tenant, category).Scan(&s.service); err != nil {
		h.T.Fatalf("seed service definition: %v", err)
	}
	return s
}

// insertReport writes one version of a chain straight into the table. The application
// service is the only thing that mints a reference and walks the lifecycle; what these tests
// are about is the half of the rules the schema holds whatever writes the row.
func insertReport(h *dbtest.Harness, s reportSeed, reference string, versionNo int,
	root, supersedes *uuid.UUID, status string,
) uuid.UUID {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	id := uuid.New()
	rootID := id
	if root != nil {
		rootID = *root
	}
	var reviewedBy *uuid.UUID
	var reviewedAt, submittedAt *string
	if status != "DRAFT" {
		at := "2026-06-15T10:00:00Z"
		submittedAt = &at
	}
	switch status {
	case "APPROVED", "REJECTED", "SUPERSEDED":
		at := "2026-06-16T10:00:00Z"
		reviewedAt = &at
		reviewedBy = &s.actor
	}
	reason := "MISSING_EVIDENCE"
	var rejectReason *string
	if status == "REJECTED" {
		rejectReason = &reason
	}
	if _, err := h.Admin.Exec(ctx, `
		INSERT INTO health.medical_report (
			id, tenant_id, person_id, reference, version_no, root_report_id, supersedes_report_id,
			report_type, issued_at, valid_from, valid_to, status, clinical_summary,
			reject_reason_code, reviewed_by, reviewed_at, submitted_at, submitted_by, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'FIZIK_TEDAVI', '2026-06-15', '2026-06-15',
		        '2026-12-31', $8, 'Fizik tedavi gereklidir.', $9, $10, $11::timestamptz,
		        $12::timestamptz, $13, $14)`,
		id, s.tenant, s.person, reference, versionNo, rootID, supersedes, status,
		rejectReason, reviewedBy, reviewedAt, submittedAt, reviewedBy, s.actor); err != nil {
		h.T.Fatalf("insert medical report v%d: %v", versionNo, err)
	}
	return id
}

// TestOneApprovedVersionPerReportChain is the version rule as the schema holds it. The
// application service supersedes the predecessor inside the approval's own transaction; this
// is the half that holds whatever writes the row, and it is why "which report is in force for
// this person" has one answer without anybody walking the chain to find out.
func TestOneApprovedVersionPerReportChain(t *testing.T) {
	h := dbtest.New(t)
	s := seedReport(h, "MR_CHAIN")
	ctx, cancel := h.Ctx()
	defer cancel()

	first := insertReport(h, s, "MR-20260615-AAAAAAAA", 1, nil, nil, "APPROVED")
	root := first

	// A second approved version of the same chain is refused.
	second := uuid.New()
	_, err := h.Admin.Exec(ctx, `
		INSERT INTO health.medical_report (
			id, tenant_id, person_id, reference, version_no, root_report_id, supersedes_report_id,
			report_type, issued_at, valid_from, valid_to, status, reviewed_by, reviewed_at,
			submitted_at, submitted_by)
		VALUES ($1, $2, $3, 'MR-20260615-AAAAAAAA', 2, $4, $5, 'FIZIK_TEDAVI', '2026-06-15',
		        '2026-06-15', '2026-12-31', 'APPROVED', $6, '2026-06-17T10:00:00Z',
		        '2026-06-16T10:00:00Z', $6)`,
		second, s.tenant, s.person, root, first, s.actor)
	if dbtest.SQLState(err) != dbtest.SQLStateUniqueViolation {
		t.Fatalf("second approved version of one chain: %v, want a unique violation", err)
	}

	// Superseding the first one makes room for it, and nothing else about the first row
	// moves: the reviewer, the moment and the summary are what the reviewer decided.
	var summaryBefore, reviewedBefore string
	if err := h.Admin.QueryRow(ctx,
		`SELECT clinical_summary, reviewed_at::text FROM health.medical_report WHERE id = $1`,
		first).Scan(&summaryBefore, &reviewedBefore); err != nil {
		t.Fatalf("read version 1: %v", err)
	}
	h.AdminExec(`UPDATE health.medical_report SET status = 'SUPERSEDED' WHERE id = $1`, first)
	insertReport(h, s, "MR-20260615-AAAAAAAA", 2, &root, &first, "APPROVED")
	var summaryAfter, reviewedAfter string
	if err := h.Admin.QueryRow(ctx,
		`SELECT clinical_summary, reviewed_at::text FROM health.medical_report WHERE id = $1`,
		first).Scan(&summaryAfter, &reviewedAfter); err != nil {
		t.Fatalf("re-read version 1: %v", err)
	}
	if summaryAfter != summaryBefore || reviewedAfter != reviewedBefore {
		t.Fatal("superseding version 1 changed what it said; the old decision must be preserved")
	}

	// A chain that already holds two decided versions still holds exactly one approval.
	var approved int
	if err := h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM health.medical_report
		 WHERE tenant_id = $1 AND root_report_id = $2 AND status = 'APPROVED'`,
		s.tenant, root).Scan(&approved); err != nil {
		t.Fatalf("count approved: %v", err)
	}
	if approved != 1 {
		t.Fatalf("the chain holds %d approved versions, want 1", approved)
	}
}

// TestMedicalReportChainShape is the pair of CHECKs that make a chain walkable: version 1
// supersedes nothing and is its own root, every later version does both.
func TestMedicalReportChainShape(t *testing.T) {
	h := dbtest.New(t)
	s := seedReport(h, "MR_SHAPE")
	ctx, cancel := h.Ctx()
	defer cancel()

	id := uuid.New()
	_, err := h.Admin.Exec(ctx, `
		INSERT INTO health.medical_report (id, tenant_id, person_id, reference, version_no,
		                                   root_report_id, report_type, issued_at, valid_from, valid_to)
		VALUES ($1, $2, $3, 'MR-20260615-BBBBBBBB', 2, $1, 'FIZIK_TEDAVI', '2026-06-15',
		        '2026-06-15', '2026-12-31')`, id, s.tenant, s.person)
	if dbtest.SQLState(err) != dbtest.SQLStateCheckViolation {
		t.Fatalf("version 2 superseding nothing: %v, want a check violation", err)
	}

	// The period, too: a report valid until before it starts is a report nobody can use.
	id = uuid.New()
	_, err = h.Admin.Exec(ctx, `
		INSERT INTO health.medical_report (id, tenant_id, person_id, reference, version_no,
		                                   root_report_id, report_type, issued_at, valid_from, valid_to)
		VALUES ($1, $2, $3, 'MR-20260615-CCCCCCCC', 1, $1, 'FIZIK_TEDAVI', '2026-06-15',
		        '2026-12-31', '2026-06-15')`, id, s.tenant, s.person)
	if dbtest.SQLState(err) != dbtest.SQLStateCheckViolation {
		t.Fatalf("valid_to before valid_from: %v, want a check violation", err)
	}
}

// TestMedicalReportUsageIsAppendOnly: a usage row is evidence, and evidence that could be
// edited afterwards is not evidence of anything.
func TestMedicalReportUsageIsAppendOnly(t *testing.T) {
	h := dbtest.New(t)
	s := seedReport(h, "MR_USAGE")
	ctx, cancel := h.Ctx()
	defer cancel()

	report := insertReport(h, s, "MR-20260615-DDDDDDDD", 1, nil, nil, "APPROVED")
	var usage uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO health.medical_report_usage (tenant_id, report_id, used_by_type, used_by_id)
		VALUES ($1, $2, 'CLAIM', $3) RETURNING id`,
		s.tenant, report, uuid.New()).Scan(&usage); err != nil {
		t.Fatalf("insert usage: %v", err)
	}

	_, err := h.Admin.Exec(ctx,
		`UPDATE health.medical_report_usage SET used_by_type = 'AUTHORIZATION' WHERE id = $1`, usage)
	if dbtest.SQLState(err) != dbtest.SQLStateIntegrityConstraint {
		t.Fatalf("update on an append-only usage: %v, want the guard to refuse it", err)
	}
	_, err = h.Admin.Exec(ctx, `DELETE FROM health.medical_report_usage WHERE id = $1`, usage)
	if dbtest.SQLState(err) != dbtest.SQLStateIntegrityConstraint {
		t.Fatalf("delete on an append-only usage: %v, want the guard to refuse it", err)
	}
}

// TestMedicalReportTenantIsolation: all three tables of the package are invisible across
// tenants through the application role, and a write tagged with another tenant fails the
// policy's WITH CHECK. A report is clinical, so "RLS is probably on" is not good enough.
func TestMedicalReportTenantIsolation(t *testing.T) {
	h := dbtest.New(t)
	a := seedReport(h, "MR_RLS_A")
	b := seedReport(h, "MR_RLS_B")

	report := insertReport(h, a, "MR-20260615-EEEEEEEE", 1, nil, nil, "APPROVED")
	h.AdminExec(`
		INSERT INTO health.medical_report_service (tenant_id, report_id, service_definition_id,
		                                           covered_quantity, covered_amount, currency_code)
		VALUES ($1, $2, $3, 12, 9000, 'TRY')`, a.tenant, report, a.service)
	h.AdminExec(`
		INSERT INTO health.medical_report_usage (tenant_id, report_id, used_by_type, used_by_id)
		VALUES ($1, $2, 'CLAIM', $3)`, a.tenant, report, uuid.New())

	countIn := func(tenant uuid.UUID, table string) int {
		t.Helper()
		var n int
		err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, a.tenant).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count %s for %s: %v", table, tenant, err)
		}
		return n
	}
	for _, table := range []string{
		"health.medical_report", "health.medical_report_service", "health.medical_report_usage",
	} {
		if got := countIn(a.tenant, table); got != 1 {
			t.Fatalf("%s: tenant A sees %d of its own rows, want 1", table, got)
		}
		if got := countIn(b.tenant, table); got != 0 {
			t.Fatalf("%s: tenant B sees %d of tenant A's rows", table, got)
		}
	}

	err := h.AppTx(b.tenant, func(ctx context.Context, tx pgx.Tx) error {
		id := uuid.New()
		_, execErr := tx.Exec(ctx, `
			INSERT INTO health.medical_report (id, tenant_id, person_id, reference, version_no,
			                                   root_report_id, report_type, issued_at, valid_from, valid_to)
			VALUES ($1, $2, $3, 'MR-20260615-FFFFFFFF', 1, $1, 'FIZIK_TEDAVI', '2026-06-15',
			        '2026-06-15', '2026-12-31')`, id, a.tenant, a.person)
		return execErr
	})
	if dbtest.SQLState(err) != dbtest.SQLStateInsufficientPrivilege {
		t.Fatalf("cross-tenant report insert: %v, want an RLS refusal", err)
	}
}

// TestMedicalReportPermissionsAreSeededAndGrantable is the two-halves test this package owes:
// the catalogue row of migration 000008 and the role template in
// internal/identity/application/roles.go have to agree, because a permission that exists in
// one and not the other is a permission nobody can hold or one nobody can be given.
//
// This package adds no permission. What it adds is the assertion that the two that already
// exist are on the right roles and, just as importantly, not on the wrong ones: writing a
// report and deciding about one are the provider's job and the payer's, and a role holding
// both would be a provider approving its own reports.
func TestMedicalReportPermissionsAreSeededAndGrantable(t *testing.T) {
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

	for _, code := range []string{"health.medical_report.manage", "health.medical_report.review"} {
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
			`SELECT sensitivity FROM iam.permission WHERE code = $1`, code).Scan(&sensitivity); err != nil {
			t.Fatalf("read sensitivity of %s: %v", code, err)
		}
		if sensitivity != "SENSITIVE" {
			t.Errorf("permission %s sensitivity = %s, want SENSITIVE: a treatment report is health data",
				code, sensitivity)
		}
		if len(granted[code]) == 0 {
			t.Errorf("permission %s is in the catalogue but in no role template: nobody can hold it", code)
		}
	}

	for _, want := range []struct{ role, permission string }{
		{"PROVIDER_STAFF", "health.medical_report.manage"},
		{"MEDICAL_REVIEWER", "health.medical_report.review"},
	} {
		if !byRole[want.role][want.permission] {
			t.Errorf("role %s does not hold %s", want.role, want.permission)
		}
	}
	// Writing a report and deciding about one are two grants because they are two jobs.
	for _, tpl := range identityapp.RoleTemplates() {
		if byRole[tpl.Code]["health.medical_report.manage"] &&
			byRole[tpl.Code]["health.medical_report.review"] {
			t.Errorf("role %s both writes and decides treatment reports", tpl.Code)
		}
	}
	// A report is clinical: deciding about one without being able to read clinical detail
	// would be deciding blind.
	for _, role := range granted["health.medical_report.review"] {
		if !byRole[role]["health.clinical.read"] {
			t.Errorf("role %s reviews treatment reports but cannot read clinical detail", role)
		}
	}
	// And the sponsor's HR user holds neither, in either direction.
	for _, forbidden := range []string{"health.medical_report.manage", "health.medical_report.review"} {
		if byRole["SPONSOR_HR"][forbidden] {
			t.Errorf("SPONSOR_HR holds %s", forbidden)
		}
	}
}
