package eligibility_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/eligibility"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// The fixture seeds the whole WP-I2-01..03 chain: a tenant with a sponsor, a principal
// member and a dependant whose membership points at the principal, an active plan with
// one published version carrying two entitlement definitions, and an enrollment for each
// person. The entitlement accounts are deliberately NOT opened: the first check opens
// them through EnsureAccounts, which is exactly what a provider counter triggers.
const (
	defDental = "DENTAL" // 10 units, the person's own account
	defOptic  = "OPTIC"  // 4 units, family_shared: opens on the principal
)

// checkDate sits inside the published version and inside the 2026 calendar year.
var checkDate = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// The synthetic names the snapshot assertions look for. They are invented, never real.
const (
	principalFirstName = "Asli"
	principalLastName  = "Yildizhan"
	dependantFirstName = "Emir"
	memberNo           = "SPONSOR-MEMBER-0042"
)

type fixture struct {
	h   *dbtest.Harness
	svc *eligibility.Service

	tenant    uuid.UUID
	actor     uuid.UUID
	sponsor   uuid.UUID
	provider  uuid.UUID
	program   uuid.UUID
	planID    uuid.UUID
	versionID uuid.UUID

	principal           uuid.UUID
	dependant           uuid.UUID
	unenrolled          uuid.UUID
	principalEnrollment uuid.UUID
	dependantEnrollment uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := dbtest.New(t)
	svc, err := eligibility.New(eligibility.Deps{
		Pool: h.App, Audit: auditpg.New(),
		Now: func() time.Time { return time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{h: h, svc: svc}
	f.seed(t)
	return f
}

func (f *fixture) seed(t *testing.T) {
	t.Helper()
	h := f.h
	ctx, cancel := h.Ctx()
	defer cancel()

	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	f.tenant = h.CreateTenant("ELIG")
	f.actor = h.CreateActor("eligibility-clerk", "Eligibility Clerk")
	f.sponsor = h.CreateTenantOrganization(f.tenant, "Eligibility Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(f.tenant, "Eligibility Payer", "PAYER")
	f.provider = h.CreateTenantOrganization(f.tenant, "Eligibility Provider", "PROVIDER")

	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, f.tenant)
	scan(&f.principal, "principal person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, $2, $3, 'asli yildizhan') RETURNING id`, f.tenant, principalFirstName, principalLastName)
	scan(&f.dependant, "dependant person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, $2, $3, 'emir yildizhan') RETURNING id`, f.tenant, dependantFirstName, principalLastName)
	scan(&f.unenrolled, "unenrolled person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Deniz', 'Kayacan', 'deniz kayacan') RETURNING id`, f.tenant)

	var principalMembership, dependantMembership uuid.UUID
	scan(&principalMembership, "principal membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, external_member_no, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', $4, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, f.principal, f.sponsor, memberNo)
	scan(&dependantMembership, "dependant membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      principal_membership_id, membership_type, status, valid_period)
		VALUES ($1, $2, $3, $4, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, f.dependant, f.sponsor, principalMembership)
	h.AdminExec(`
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)'))`,
		f.tenant, f.unenrolled, f.sponsor)

	h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name) VALUES ($1, 'BENEFIT', 'Fayda')`, f.tenant)
	scan(&f.program, "program", `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id, payer_tenant_organization_id,
		                             code, name, program_type, status, valid_period)
		VALUES ($1, $2, $3, 'PRG', 'Program', 'BENEFIT', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, f.sponsor, payer)
	scan(&f.planID, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, f.tenant, f.program)
	scan(&f.versionID, "plan version", `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period,
		                                  published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', daterange('2026-01-01','2027-01-01','[)'), clock_timestamp(), $3)
		RETURNING id`, f.tenant, f.planID, f.actor)

	definitions := []struct {
		code     string
		quantity string
		shared   bool
	}{
		{defDental, "10", false},
		{defOptic, "4", true},
	}
	for _, d := range definitions {
		h.AdminExec(`
			INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name, unit_type,
			                                            period_type, initial_quantity, allow_overdraft, family_shared)
			VALUES ($1, $2, $3, $3, 'COUNT', 'CALENDAR_YEAR', $4::text::numeric, false, $5)`,
			f.tenant, f.versionID, d.code, d.quantity, d.shared)
	}

	scan(&f.principalEnrollment, "principal enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, principalMembership, f.planID)
	scan(&f.dependantEnrollment, "dependant enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, dependantMembership, f.planID)
}

// rc is a tenant-wide clerk holding eligibility.check.
func (f *fixture) rc() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal:   identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{"eligibility.check": {}},
	}
}

// providerRC is a provider-scoped actor: its grants are bound to one organization.
func (f *fixture) providerRC(scope uuid.UUID) identity.RequestContext {
	rc := f.rc()
	rc.Scopes = []identity.Scope{{Type: eligibility.ScopeOrganization, ID: uuid.NullUUID{UUID: scope, Valid: true}}}
	return rc
}

// check runs one eligibility question and fails the test on an unexpected error.
func (f *fixture) check(t *testing.T, in eligibility.CheckInput) eligibility.ResultView {
	t.Helper()
	out, err := f.svc.Check(context.Background(), f.rc(), in)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	return out
}

// request builds a check for one person and one entitlement code.
func request(person uuid.UUID, date time.Time, code string, quantity string) eligibility.CheckInput {
	return eligibility.CheckInput{
		PersonID: person, ServiceDate: date,
		Items: []eligibility.RequestItem{{
			ServiceDefinitionID: uuid.New(), Quantity: domain.MustQuantity(quantity),
		}},
		Context: map[string]any{"entitlementCode": code},
	}
}

func itemCodes(items []eligibility.ItemView) []string {
	out := []string{}
	for _, item := range items {
		for _, e := range item.Explanations {
			out = append(out, e.Code)
		}
	}
	return out
}

func balanceOf(t *testing.T, result eligibility.ResultView, code string) string {
	t.Helper()
	for _, b := range result.Balances {
		if b.EntitlementCode == code {
			return b.Available.String()
		}
	}
	t.Fatalf("balance %s missing from %+v", code, result.Balances)
	return ""
}

func TestCheckEligibleOpensAccountsAndNamesThePlanVersion(t *testing.T) {
	f := newFixture(t)

	result := f.check(t, request(f.principal, checkDate, defDental, "2"))

	if result.Outcome != eligibility.OutcomeEligible || !result.Eligible {
		t.Fatalf("outcome = %s (eligible=%v), want ELIGIBLE", result.Outcome, result.Eligible)
	}
	if result.EvaluationID == uuid.Nil {
		t.Fatal("no evaluationId in the result")
	}
	if result.PlanVersionID == nil || *result.PlanVersionID != f.versionID {
		t.Fatalf("plan version = %v, want %s", result.PlanVersionID, f.versionID)
	}
	if result.EnrollmentID == nil || *result.EnrollmentID != f.principalEnrollment {
		t.Fatalf("enrollment = %v, want %s", result.EnrollmentID, f.principalEnrollment)
	}
	// The first check opened both accounts: the own one and the family-shared one.
	if got := balanceOf(t, result, defDental); got != "10" {
		t.Fatalf("DENTAL balance = %s, want 10", got)
	}
	if got := balanceOf(t, result, defOptic); got != "4" {
		t.Fatalf("OPTIC balance = %s, want 4", got)
	}
	if len(result.Items) != 1 || result.Items[0].Outcome != eligibility.ItemEligible {
		t.Fatalf("items = %+v", result.Items)
	}
	if result.Items[0].AvailableQuantity == nil || result.Items[0].AvailableQuantity.String() != "10" {
		t.Fatalf("available quantity = %v", result.Items[0].AvailableQuantity)
	}
}

func TestCheckPartiallyEligibleAndInsufficient(t *testing.T) {
	f := newFixture(t)

	partial, err := f.svc.Check(context.Background(), f.rc(), eligibility.CheckInput{
		PersonID: f.principal, ServiceDate: checkDate,
		Items: []eligibility.RequestItem{
			{ServiceDefinitionID: uuid.New(), Quantity: domain.MustQuantity("2")},
			{ServiceDefinitionID: uuid.New(), Quantity: domain.MustQuantity("9")},
		},
		Context: map[string]any{"entitlementCodes": []any{defDental, defOptic}},
	})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if partial.Outcome != eligibility.OutcomePartiallyEligible {
		t.Fatalf("outcome = %s, want PARTIALLY_ELIGIBLE", partial.Outcome)
	}
	if codes := itemCodes(partial.Items); len(codes) != 1 || codes[0] != eligibility.CodeBalanceInsufficient {
		t.Fatalf("item codes = %v", codes)
	}

	none := f.check(t, request(f.principal, checkDate, defDental, "40"))
	if none.Outcome != eligibility.OutcomeIneligible {
		t.Fatalf("outcome = %s, want INELIGIBLE", none.Outcome)
	}
	if codes := itemCodes(none.Items); len(codes) != 1 || codes[0] != eligibility.CodeBalanceInsufficient {
		t.Fatalf("item codes = %v", codes)
	}
}

func TestCheckWithoutEnrollment(t *testing.T) {
	f := newFixture(t)

	result := f.check(t, request(f.unenrolled, checkDate, defDental, "1"))

	if result.Outcome != eligibility.OutcomeIneligible {
		t.Fatalf("outcome = %s, want INELIGIBLE", result.Outcome)
	}
	if len(result.Explanations) != 1 || result.Explanations[0].Code != eligibility.CodeEnrollmentNone {
		t.Fatalf("explanations = %+v", result.Explanations)
	}
	if result.PlanVersionID != nil {
		t.Fatalf("plan version = %v on a person without an enrollment", result.PlanVersionID)
	}
}

func TestCheckBoundaryServiceDates(t *testing.T) {
	f := newFixture(t)

	cases := []struct {
		date    string
		outcome string
		code    string
	}{
		// The day before the membership and the enrollment start: the membership is the
		// first step that fails, and missing membership data is MISSING_DATA, not a "no".
		{"2025-12-31", eligibility.OutcomeMissingData, eligibility.CodeMembershipNone},
		{"2026-01-01", eligibility.OutcomeEligible, ""}, // first covered day
		{"2026-12-31", eligibility.OutcomeEligible, ""}, // last covered day of the version
		// The version period is half-open, so its upper bound is already outside.
		{"2027-01-01", eligibility.OutcomeIneligible, eligibility.CodePlanVersionNone},
	}
	for _, tc := range cases {
		day, err := time.Parse(time.DateOnly, tc.date)
		if err != nil {
			t.Fatal(err)
		}
		result := f.check(t, request(f.principal, day, defDental, "1"))
		if result.Outcome != tc.outcome {
			t.Fatalf("%s: outcome = %s, want %s (%+v)", tc.date, result.Outcome, tc.outcome, result.Explanations)
		}
		if tc.code == "" {
			continue
		}
		if len(result.Explanations) == 0 || result.Explanations[0].Code != tc.code {
			t.Fatalf("%s: explanations = %+v, want %s", tc.date, result.Explanations, tc.code)
		}
	}
}

func TestCheckSeesTheFamilySharedBalanceOfThePrincipal(t *testing.T) {
	f := newFixture(t)

	// The dependant asks first: the shared account is opened on the principal and the
	// dependant still reaches it.
	result := f.check(t, request(f.dependant, checkDate, defOptic, "3"))

	if result.Outcome != eligibility.OutcomeEligible {
		t.Fatalf("outcome = %s, want ELIGIBLE (%+v)", result.Outcome, result.Explanations)
	}
	if result.EnrollmentID == nil || *result.EnrollmentID != f.dependantEnrollment {
		t.Fatalf("enrollment = %v, want the dependant's %s", result.EnrollmentID, f.dependantEnrollment)
	}
	if got := balanceOf(t, result, defOptic); got != "4" {
		t.Fatalf("shared OPTIC balance = %s, want 4", got)
	}

	ctx, cancel := f.h.Ctx()
	defer cancel()
	var holder uuid.UUID
	err := f.h.Admin.QueryRow(ctx, `
		SELECT a.enrollment_id FROM benefit.entitlement_account a
		  JOIN benefit.entitlement_definition d ON d.id = a.entitlement_definition_id
		 WHERE a.tenant_id = $1 AND d.code = $2`, f.tenant, defOptic).Scan(&holder)
	if err != nil {
		t.Fatalf("read shared account: %v", err)
	}
	if holder != f.principalEnrollment {
		t.Fatalf("shared account opened on %s, want the principal's %s", holder, f.principalEnrollment)
	}
}

func TestEvaluationRowIsImmutableAndFreeOfPersonalData(t *testing.T) {
	f := newFixture(t)
	result := f.check(t, request(f.principal, checkDate, defDental, "2"))

	ctx, cancel := f.h.Ctx()
	defer cancel()
	var requestSnapshot, resultSnapshot, outcome, classification string
	err := f.h.Admin.QueryRow(ctx, `
		SELECT request_snapshot::text, result_snapshot::text, outcome, data_classification
		  FROM benefit.eligibility_evaluation WHERE id = $1`, result.EvaluationID).
		Scan(&requestSnapshot, &resultSnapshot, &outcome, &classification)
	if err != nil {
		t.Fatalf("read evaluation: %v", err)
	}
	if outcome != eligibility.OutcomeEligible || classification != "PERSONAL" {
		t.Fatalf("row = %s / %s", outcome, classification)
	}

	// Nothing that identifies a human may survive in an immutable snapshot.
	stored := requestSnapshot + resultSnapshot
	for _, forbidden := range []string{
		principalFirstName, principalLastName, dependantFirstName, memberNo,
		"asli yildizhan", strings.ToLower(principalLastName),
	} {
		if strings.Contains(strings.ToLower(stored), strings.ToLower(forbidden)) {
			t.Fatalf("snapshot leaks %q: %s", forbidden, stored)
		}
	}
	// The ids, dates and quantities it is allowed to carry are there (jsonb renders one
	// space after each key).
	for _, want := range []string{
		f.principal.String(), f.versionID.String(), "2026-06-01", `"quantity": 2`, `"available": 10`,
	} {
		if !strings.Contains(stored, want) {
			t.Fatalf("snapshot is missing %q: %s", want, stored)
		}
	}

	err = f.h.AdminExecErr(`UPDATE benefit.eligibility_evaluation SET outcome = 'INELIGIBLE' WHERE id = $1`,
		result.EvaluationID)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "update an evaluation")
	err = f.h.AdminExecErr(`DELETE FROM benefit.eligibility_evaluation WHERE id = $1`, result.EvaluationID)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "delete an evaluation")
}

// TestEvaluationIsInvisibleToAnotherTenant is the negative RLS test of the new table:
// benefit.eligibility_evaluation is tenant scoped, so another tenant's transaction sees
// no rows at all and the read-by-id answers 404 rather than admitting the row exists.
func TestEvaluationIsInvisibleToAnotherTenant(t *testing.T) {
	f := newFixture(t)
	created := f.check(t, request(f.principal, checkDate, defDental, "1"))

	other := f.h.CreateTenant("ELIG_OTHER")
	foreign := f.rc()
	foreign.TenantID = other
	if _, err := f.svc.GetEvaluation(context.Background(), foreign, created.EvaluationID); !errors.Is(err, eligibility.ErrEvaluationNotFound) {
		t.Fatalf("cross-tenant read error = %v, want ErrEvaluationNotFound", err)
	}

	count := func(tenant uuid.UUID) int {
		t.Helper()
		var n int
		err := f.h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM benefit.eligibility_evaluation`).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count as tenant %s: %v", tenant, err)
		}
		return n
	}
	if got := count(f.tenant); got != 1 {
		t.Fatalf("own tenant sees %d evaluations, want 1", got)
	}
	if got := count(other); got != 0 {
		t.Fatalf("other tenant sees %d evaluations, want 0", got)
	}
}

func TestIdempotentReplayReturnsTheSameEvaluation(t *testing.T) {
	f := newFixture(t)
	in := request(f.principal, checkDate, defDental, "2")
	in.IdempotencyKey = "counter-0001"

	first := f.check(t, in)
	second := f.check(t, in)

	if first.EvaluationID != second.EvaluationID {
		t.Fatalf("replay produced a new evaluation: %s vs %s", first.EvaluationID, second.EvaluationID)
	}
	if !first.EvaluatedAt.Equal(second.EvaluatedAt) || first.Outcome != second.Outcome {
		t.Fatalf("replay differs: %+v vs %+v", first, second)
	}

	ctx, cancel := f.h.Ctx()
	defer cancel()
	var rows int
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM benefit.eligibility_evaluation WHERE tenant_id = $1`, f.tenant).Scan(&rows); err != nil {
		t.Fatalf("count evaluations: %v", err)
	}
	if rows != 1 {
		t.Fatalf("%d evaluation rows, want 1", rows)
	}

	// The same key with a different question is a conflict, not a replay.
	other := request(f.principal, checkDate, defDental, "3")
	other.IdempotencyKey = in.IdempotencyKey
	if _, err := f.svc.Check(context.Background(), f.rc(), other); !errors.Is(err, eligibility.ErrIdempotencyKeyReuse) {
		t.Fatalf("reused key error = %v, want ErrIdempotencyKeyReuse", err)
	}
}

func TestCheckWritesAnAccessEvent(t *testing.T) {
	f := newFixture(t)

	personal := f.check(t, request(f.principal, checkDate, defDental, "1"))

	health := request(f.principal, checkDate, defDental, "1")
	health.Context["domain"] = "HEALTH"
	healthResult := f.check(t, health)

	ctx, cancel := f.h.Ctx()
	defer cancel()
	read := func(evaluationID uuid.UUID) (string, string, string, uuid.UUID) {
		t.Helper()
		var classification, accessType, purpose string
		var person uuid.UUID
		err := f.h.Admin.QueryRow(ctx, `
			SELECT data_classification, access_type, purpose_code, person_id
			  FROM audit.access_event
			 WHERE tenant_id = $1 AND resource_type = 'eligibility_evaluation' AND resource_id = $2`,
			f.tenant, evaluationID).Scan(&classification, &accessType, &purpose, &person)
		if err != nil {
			t.Fatalf("read access event: %v", err)
		}
		return classification, accessType, purpose, person
	}

	classification, accessType, purpose, person := read(personal.EvaluationID)
	if classification != "PERSONAL" || accessType != "VIEW" || purpose != "ELIGIBILITY_CHECK" || person != f.principal {
		t.Fatalf("personal access event = %s/%s/%s/%s", classification, accessType, purpose, person)
	}
	if classification, _, _, _ = read(healthResult.EvaluationID); classification != "HEALTH" {
		t.Fatalf("health-context access event classified %s, want HEALTH", classification)
	}
}

func TestGetEvaluationReturnsTheStoredSnapshot(t *testing.T) {
	f := newFixture(t)
	created := f.check(t, request(f.principal, checkDate, defDental, "2"))

	stored, err := f.svc.GetEvaluation(context.Background(), f.rc(), created.EvaluationID)
	if err != nil {
		t.Fatalf("get evaluation: %v", err)
	}
	if stored.ID != created.EvaluationID || stored.PersonID != f.principal {
		t.Fatalf("evaluation = %+v", stored)
	}
	if stored.ServiceDate != "2026-06-01" || stored.Outcome != eligibility.OutcomeEligible {
		t.Fatalf("evaluation = %+v", stored)
	}
	if stored.Result.EvaluationID != created.EvaluationID || len(stored.Result.Items) != 1 {
		t.Fatalf("stored result = %+v", stored.Result)
	}
	if stored.Request.ServiceItems[0].Quantity.String() != "2" {
		t.Fatalf("stored request = %+v", stored.Request)
	}
	if stored.EvaluatedBy == nil || *stored.EvaluatedBy != f.actor {
		t.Fatalf("evaluatedBy = %v, want %s", stored.EvaluatedBy, f.actor)
	}

	if _, err := f.svc.GetEvaluation(context.Background(), f.rc(), uuid.New()); !errors.Is(err, eligibility.ErrEvaluationNotFound) {
		t.Fatalf("unknown evaluation error = %v, want ErrEvaluationNotFound", err)
	}
}

func TestProviderScopeIsEnforced(t *testing.T) {
	f := newFixture(t)
	inScope := request(f.principal, checkDate, defDental, "1")
	inScope.ProviderOrganizationID = &f.provider

	if _, err := f.svc.Check(context.Background(), f.providerRC(f.provider), inScope); err != nil {
		t.Fatalf("check inside the provider scope: %v", err)
	}

	// Another organization, and no organization at all, are both refused.
	outOfScope := request(f.principal, checkDate, defDental, "1")
	outOfScope.ProviderOrganizationID = &f.sponsor
	if _, err := f.svc.Check(context.Background(), f.providerRC(f.provider), outOfScope); !errors.Is(err, eligibility.ErrProviderScope) {
		t.Fatalf("out-of-scope error = %v, want ErrProviderScope", err)
	}
	missing := request(f.principal, checkDate, defDental, "1")
	if _, err := f.svc.Check(context.Background(), f.providerRC(f.provider), missing); !errors.Is(err, eligibility.ErrProviderScope) {
		t.Fatalf("scopeless request error = %v, want ErrProviderScope", err)
	}

	// A provider-scoped actor cannot read another provider's evaluation either.
	other := f.check(t, request(f.principal, checkDate, defDental, "1"))
	if _, err := f.svc.GetEvaluation(context.Background(), f.providerRC(f.provider), other.EvaluationID); !errors.Is(err, eligibility.ErrEvaluationNotFound) {
		t.Fatalf("foreign evaluation error = %v, want ErrEvaluationNotFound", err)
	}
}

func TestCheckRejectsInvalidInput(t *testing.T) {
	f := newFixture(t)

	cases := []struct {
		name string
		in   eligibility.CheckInput
	}{
		{"no person", eligibility.CheckInput{ServiceDate: checkDate, Items: []eligibility.RequestItem{{
			ServiceDefinitionID: uuid.New(), Quantity: domain.MustQuantity("1")}}}},
		{"no service date", eligibility.CheckInput{PersonID: f.principal, Items: []eligibility.RequestItem{{
			ServiceDefinitionID: uuid.New(), Quantity: domain.MustQuantity("1")}}}},
		{"no items", eligibility.CheckInput{PersonID: f.principal, ServiceDate: checkDate}},
		{"zero quantity", eligibility.CheckInput{PersonID: f.principal, ServiceDate: checkDate,
			Items: []eligibility.RequestItem{{ServiceDefinitionID: uuid.New()}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.Check(context.Background(), f.rc(), tc.in)
			if !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("error = %v, want a validation error", err)
			}
		})
	}
}
