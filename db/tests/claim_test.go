package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// TestClaimPermissionsAreSeededAndGrantable checks both halves of the same fact: the catalogue
// rows of migrations 000008 and 000034 and the role templates in
// internal/identity/application/roles.go have to agree, because a permission that exists in one
// and not the other is a permission nobody can hold or one nobody can be given.
//
// The sensitivities are asserted too. `claim.medical.review` is SENSITIVE because deciding a
// claim on clinical grounds means reading clinical grounds; the other five are NORMAL, and one
// of them quietly becoming SENSITIVE would drop a provider's billing clerk out of every claim
// they raise.
func TestClaimPermissionsAreSeededAndGrantable(t *testing.T) {
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
		"claim.read":             "NORMAL",
		"claim.create":           "NORMAL",
		"claim.submit":           "NORMAL",
		"claim.cancel":           "NORMAL",
		"claim.medical.review":   "SENSITIVE",
		"claim.financial.review": "NORMAL",
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
			`SELECT sensitivity FROM iam.permission WHERE code = $1`, code).Scan(&sensitivity); err != nil {
			t.Fatalf("read sensitivity of %s: %v", code, err)
		}
		if sensitivity != wantSensitivity {
			t.Errorf("permission %s sensitivity = %s, want %s", code, sensitivity, wantSensitivity)
		}
		if len(granted[code]) == 0 {
			t.Errorf("permission %s is in the catalogue but in no role template: nobody can hold it", code)
		}
	}

	// Section 2.6, role by role.
	for _, want := range []struct{ role, permission string }{
		{"PROVIDER_BILLING", "claim.create"},
		{"PROVIDER_BILLING", "claim.submit"},
		{"PROVIDER_BILLING", "claim.cancel"},
		{"MEDICAL_REVIEWER", "claim.medical.review"},
		{"FINANCIAL_REVIEWER", "claim.financial.review"},
		{"PROGRAM_MANAGER", "claim.read"},
		{"SPONSOR_HR", "claim.read"},
	} {
		if !byRole[want.role][want.permission] {
			t.Errorf("role %s does not hold %s", want.role, want.permission)
		}
	}

	// Whoever may raise a claim may withdraw one: a provider that could submit and not cancel
	// would have no way to take back a mistake, and "delete the draft" is a different act.
	for _, tpl := range identityapp.RoleTemplates() {
		if byRole[tpl.Code]["claim.submit"] && !byRole[tpl.Code]["claim.cancel"] {
			t.Errorf("role %s may submit a claim but not cancel one", tpl.Code)
		}
	}

	// The sponsor's HR user reads claims and never their clinical half. This is the pairing
	// the whole visibility half of the package exists for, and a grant quietly added here
	// would defeat it without a line of the service changing.
	for _, permission := range []string{
		"health.clinical.read", "health.sensitive.read", "claim.medical.review",
	} {
		if byRole["SPONSOR_HR"][permission] {
			t.Errorf("SPONSOR_HR holds %s", permission)
		}
	}
	// The financial reviewer decides money and holds no clinical grant either, which is what
	// makes the financial projection the one they are served.
	for _, permission := range []string{"health.clinical.read", "health.sensitive.read"} {
		if byRole["FINANCIAL_REVIEWER"][permission] {
			t.Errorf("FINANCIAL_REVIEWER holds %s", permission)
		}
	}
}

// TestClaimSchemaShape pins the four things about migration 000034 that the application layer
// relies on and could not detect the loss of.
func TestClaimSchemaShape(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	// One draft version per claim, and one reference per tenant.
	for _, want := range []struct{ table, index string }{
		{"claim.claim_version", "uq_claim_version_draft"},
		{"claim.claim_version", "uq_claim_version_no"},
		{"claim.claim", "uq_claim_reference"},
		{"claim.claim_line", "uq_claim_line_no"},
	} {
		var n int
		if err := h.Admin.QueryRow(ctx, `
			SELECT count(*) FROM pg_indexes
			 WHERE schemaname || '.' || tablename = $1 AND indexname = $2`,
			want.table, want.index).Scan(&n); err != nil {
			t.Fatalf("read indexes of %s: %v", want.table, err)
		}
		if n != 1 {
			t.Errorf("%s has no %s", want.table, want.index)
		}
	}

	// Every money and quantity column is an exact decimal. A balance that depended on binary
	// rounding would be a balance nobody could reconcile.
	rows, err := h.Admin.Query(ctx, `
		SELECT table_name || '.' || column_name, data_type
		  FROM information_schema.columns
		 WHERE table_schema = 'claim'
		   AND column_name IN ('quantity','unit_amount','line_amount','approved_quantity',
		                       'approved_amount','contract_amount','payer_amount',
		                       'member_amount','amount')`)
	if err != nil {
		t.Fatalf("read claim columns: %v", err)
	}
	defer rows.Close()
	checked := 0
	for rows.Next() {
		var column, dataType string
		if err := rows.Scan(&column, &dataType); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		checked++
		if dataType != "numeric" {
			t.Errorf("claim.%s is %s, want numeric", column, dataType)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	if checked < 9 {
		t.Errorf("checked %d money columns, expected at least 9", checked)
	}

	// The two append-only tables really refuse an update and a delete.
	for _, table := range []string{"claim.line_decision", "claim.adjustment"} {
		var n int
		if err := h.Admin.QueryRow(ctx, `
			SELECT count(*) FROM pg_trigger
			 WHERE tgrelid = $1::regclass AND tgname = 'tg_append_only'`, table).Scan(&n); err != nil {
			t.Fatalf("read triggers of %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("%s is not append-only", table)
		}
	}
}

// TestClaimLineDecisionSplitIsCheckedByTheDatabase is the invariant the whole settlement rests
// on, asserted where it cannot be forgotten: the payer's half and the member's half add up to
// the approved amount, exactly, and a row that does not is refused by PostgreSQL rather than by
// a service somebody may later bypass.
func TestClaimLineDecisionSplitIsCheckedByTheDatabase(t *testing.T) {
	h := dbtest.New(t)
	f := seedClaimRow(t, h)

	// The honest row goes in.
	h.AdminExec(`
		INSERT INTO claim.line_decision (tenant_id, line_id, decided_in_version_no, decision,
		                                 approved_quantity, approved_amount, payer_amount,
		                                 member_amount, reason_code, stage)
		VALUES ($1, $2, 1, 'APPROVED', 1, 449.99, 393.74, 56.25, 'AUTO_APPROVED', 'AUTO')`,
		f.tenant, f.line)

	// One kuruş out is refused.
	err := h.AdminExecErr(`
		INSERT INTO claim.line_decision (tenant_id, line_id, decided_in_version_no, decision,
		                                 approved_quantity, approved_amount, payer_amount,
		                                 member_amount, reason_code, stage)
		VALUES ($1, $2, 1, 'APPROVED', 1, 449.99, 393.74, 56.24, 'AUTO_APPROVED', 'AUTO')`,
		f.tenant, f.line)
	if err == nil {
		t.Fatal("a decision whose halves do not add up was accepted")
	}

	// An AUTO decision names nobody, and a decision by a person names somebody.
	if err := h.AdminExecErr(`
		INSERT INTO claim.line_decision (tenant_id, line_id, decided_in_version_no, decision,
		                                 approved_quantity, approved_amount, payer_amount,
		                                 member_amount, reason_code, decided_by, stage)
		VALUES ($1, $2, 1, 'APPROVED', 1, 100, 100, 0, 'AUTO_APPROVED', $3, 'AUTO')`,
		f.tenant, f.line, f.actor); err == nil {
		t.Error("an AUTO decision was allowed to name an actor")
	}
	if err := h.AdminExecErr(`
		INSERT INTO claim.line_decision (tenant_id, line_id, decided_in_version_no, decision,
		                                 approved_quantity, approved_amount, payer_amount,
		                                 member_amount, reason_code, stage)
		VALUES ($1, $2, 1, 'APPROVED', 1, 100, 100, 0, 'REVIEWED', 'MEDICAL')`,
		f.tenant, f.line); err == nil {
		t.Error("a MEDICAL decision was allowed with no actor")
	}

	// A rejected line approves nothing.
	if err := h.AdminExecErr(`
		INSERT INTO claim.line_decision (tenant_id, line_id, decided_in_version_no, decision,
		                                 approved_quantity, approved_amount, payer_amount,
		                                 member_amount, reason_code, decided_by, stage)
		VALUES ($1, $2, 1, 'REJECTED', 1, 100, 100, 0, 'NOT_COVERED', $3, 'FINANCIAL')`,
		f.tenant, f.line, f.actor); err == nil {
		t.Error("a rejected line was allowed to approve an amount")
	}

	// And the decision it was given cannot be edited away.
	if err := h.AdminExecErr(`
		UPDATE claim.line_decision SET approved_amount = 0 WHERE line_id = $1`, f.line); err == nil {
		t.Error("a line decision was updated in place")
	}
	if err := h.AdminExecErr(`DELETE FROM claim.line_decision WHERE line_id = $1`, f.line); err == nil {
		t.Error("a line decision was deleted")
	}
}

// TestClaimRowsAreTenantIsolated: the application role sees only the selected tenant's claims,
// and cannot write one into somebody else's.
func TestClaimRowsAreTenantIsolated(t *testing.T) {
	h := dbtest.New(t)
	first := seedClaimRow(t, h)
	second := seedClaimRow(t, h)

	if err := h.AppTx(first.tenant, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM claim.claim`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Errorf("the app role sees %d claims from inside one tenant, want 1", n)
		}
		var lines int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM claim.claim_line`).Scan(&lines); err != nil {
			return err
		}
		if lines != 1 {
			t.Errorf("the app role sees %d claim lines, want 1", lines)
		}
		return nil
	}); err != nil {
		t.Fatalf("read inside the tenant: %v", err)
	}
	// The second tenant's claim exists and is simply not there from inside the first.
	if err := h.AppTx(second.tenant, func(ctx context.Context, tx pgx.Tx) error {
		var reference string
		return tx.QueryRow(ctx, `SELECT reference FROM claim.claim WHERE id = $1`,
			second.claim).Scan(&reference)
	}); err != nil {
		t.Fatalf("read the second tenant's own claim: %v", err)
	}
	// And writing into somebody else's tenant is refused by the policy, not by a service.
	if err := h.AppTx(first.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE claim.claim SET review_comment_financial = 'x' WHERE id = $1`, second.claim)
		if err != nil {
			return err
		}
		var moved int
		return tx.QueryRow(ctx, `SELECT count(*) FROM claim.claim WHERE id = $1`,
			second.claim).Scan(&moved)
	}); err != nil {
		t.Fatalf("cross-tenant write: %v", err)
	}
	if err := h.AppTx(second.tenant, func(ctx context.Context, tx pgx.Tx) error {
		var comment *string
		if err := tx.QueryRow(ctx, `
			SELECT review_comment_financial FROM claim.claim WHERE id = $1`,
			second.claim).Scan(&comment); err != nil {
			return err
		}
		if comment != nil {
			t.Errorf("a claim was written from another tenant: comment = %q", *comment)
		}
		return nil
	}); err != nil {
		t.Fatalf("read back the second tenant's claim: %v", err)
	}
}

// claimRowFixture is the smallest claim the schema will accept, for the constraint tests.
type claimRowFixture struct {
	tenant uuid.UUID
	actor  uuid.UUID
	claim  uuid.UUID
	line   uuid.UUID
}

func seedClaimRow(t *testing.T, h *dbtest.Harness) claimRowFixture {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var f claimRowFixture
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	f.tenant = h.CreateTenant("CLM" + uuid.NewString()[:6])
	f.actor = h.CreateActor("claim-db-"+uuid.NewString()[:8], "Claim DB")
	sponsor := h.CreateTenantOrganization(f.tenant, "Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(f.tenant, "Payer", "PAYER")
	provider := h.CreateTenantOrganization(f.tenant, "Provider", "PROVIDER")

	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, f.tenant)
	var person, membership, program, plan, planVersion, enrollment uuid.UUID
	scan(&person, "person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Ada', 'Kaya', 'ada kaya') RETURNING id`, f.tenant)
	scan(&membership, "membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, person, sponsor)
	h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name) VALUES ($1, 'BENEFIT', 'Fayda')`, f.tenant)
	scan(&program, "program", `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id, payer_tenant_organization_id,
		                             code, name, program_type, status, valid_period)
		VALUES ($1, $2, $3, 'PRG', 'Program', 'BENEFIT', 'ACTIVE', daterange('2026-01-01', NULL, '[)'))
		RETURNING id`, f.tenant, sponsor, payer)
	scan(&plan, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, f.tenant, program)
	scan(&planVersion, "plan version", `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period)
		VALUES ($1, $2, 1, 'DRAFT', daterange('2026-01-01','2027-01-01','[)')) RETURNING id`,
		f.tenant, plan)
	_ = planVersion
	scan(&enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, membership, plan)

	var category, definition uuid.UUID
	scan(&category, "category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, f.tenant)
	scan(&definition, "definition", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'CONSULT', 'Muayene', 'DIRECT', 'COUNT') RETURNING id`, f.tenant, category)

	scan(&f.claim, "claim", `
		INSERT INTO claim.claim (tenant_id, reference, person_id, program_id, enrollment_id,
		                         provider_organization_id, service_date_from, service_date_to)
		VALUES ($1, 'CLM-20260615-AAAAAAAA', $2, $3, $4, $5, '2026-06-15', '2026-06-15')
		RETURNING id`, f.tenant, person, program, enrollment, provider)
	var version uuid.UUID
	scan(&version, "claim version", `
		INSERT INTO claim.claim_version (tenant_id, claim_id, version_no)
		VALUES ($1, $2, 1) RETURNING id`, f.tenant, f.claim)
	scan(&f.line, "claim line", `
		INSERT INTO claim.claim_line (tenant_id, version_id, line_no, service_definition_id,
		                              unit_type, quantity, line_amount)
		VALUES ($1, $2, 1, $3, 'COUNT', 1, 449.99) RETURNING id`, f.tenant, version, definition)
	return f
}
