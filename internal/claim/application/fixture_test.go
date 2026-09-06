// The claim, driven end to end against a real database: priced through the real pricing
// ladder, decided by real published ADJUDICATION rules, drawing on a real authorization taken
// against the real entitlement ledger, and projected by WP-I5-01's own visibility rules.
//
// Nothing below stubs the modules this package leans on. Every property the work package asks
// for is a property of the join between them — a fake pricing port would prove the arithmetic
// of a fake, and a fake hold would prove the over-consumption exception against a hold nobody
// could over-consume.
package application_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
	authorizationpg "github.com/celikbros/kapsora/internal/authorization/infrastructure/postgres"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/claim/application"
	claimgw "github.com/celikbros/kapsora/internal/claim/infrastructure/gateway"
	claimpg "github.com/celikbros/kapsora/internal/claim/infrastructure/postgres"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	healthpg "github.com/celikbros/kapsora/internal/health/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	pricingapp "github.com/celikbros/kapsora/internal/pricing/application"
	pricingpg "github.com/celikbros/kapsora/internal/pricing/infrastructure/postgres"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
	servicerequestpg "github.com/celikbros/kapsora/internal/servicerequest/infrastructure/postgres"
	workflowapp "github.com/celikbros/kapsora/internal/workflow/application"
	workflowpg "github.com/celikbros/kapsora/internal/workflow/infrastructure/postgres"
)

// The figures the arithmetic tests turn on. They are chosen so that a wrong rounding or a
// wrong split gives a visibly different answer rather than one that happens to agree:
//
//   - the consultation costs 449.99 with a 12.5 % member share, so the share is 56.24875 —
//     a figure that only comes out right if the covered amount is rounded once, at the end,
//     and the member takes the difference;
//   - the physiotherapy line is priced per unit, so a claim for two sessions is 500 and a
//     claim for three is 750: an implementation that priced the line rather than the units
//     would give 250 for both.
const (
	consultPrice      = "449.99"
	consultShare      = "12.5"
	physioUnitPrice   = "250"
	consultPayer      = "393.74"
	consultMember     = "56.25"
	physioTwoContract = "500"
)

// serviceDay is the day every claim in this file is for. It sits inside the plan version and
// inside the contract version, so nothing below depends on a clock.
var serviceDay = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

// fixtureNow is where the fixture's clock is pinned.
var fixtureNow = time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC)

type fixture struct {
	h *dbtest.Harness

	claims         *application.Service
	requests       *servicerequestapp.Service
	authorizations *authorizationapp.Service
	health         *healthapp.Service

	tenant       uuid.UUID
	actor        uuid.UUID
	reviewer     uuid.UUID
	person       uuid.UUID
	program      uuid.UUID
	enrollment   uuid.UUID
	planVersion  uuid.UUID
	provider     uuid.UUID
	otherOrg     uuid.UUID
	consult      uuid.UUID
	physio       uuid.UUID
	lab          uuid.UUID
	caseID       uuid.UUID
	encounterID  uuid.UUID
	diagnosisID  uuid.UUID
	physioAcct   uuid.UUID
	consultAcct  uuid.UUID
	medicalQueue uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := dbtest.New(t)

	cursors, err := httpx.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	clock := func() time.Time { return fixtureNow }

	entitlements, err := ledger.New(ledger.Deps{Pool: h.App, Audit: auditpg.New(), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	authorizations, err := authorizationapp.New(authorizationapp.Deps{
		Pool: h.App, Repo: authorizationpg.New(), Ledger: entitlements.Ledger(),
		Audit: auditpg.New(), Cursors: cursors, Logger: logger, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	requests, err := servicerequestapp.New(servicerequestapp.Deps{
		Pool: h.App, Repo: servicerequestpg.New(), Audit: auditpg.New(),
		Cursors: cursors, Logger: logger, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	pricing, err := pricingapp.New(pricingapp.Deps{
		Pool: h.App, Repo: pricingpg.New(), Audit: auditpg.New(), Logger: logger, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	healthSvc, err := healthapp.New(healthapp.Deps{
		Pool: h.App, Repo: healthpg.New(), Reports: healthpg.NewReports(),
		Audit: auditpg.New(), Cursors: cursors, Logger: logger, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflows, err := workflowapp.New(workflowapp.Deps{
		Pool: h.App, Repo: workflowpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Logger: logger, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := application.New(application.Deps{
		Pool: h.App, Repo: claimpg.New(),
		Pricing:        claimgw.NewPricing(pricing),
		Rules:          claimgw.NewRules(logger),
		Authorizations: claimgw.NewAuthorizations(authorizations),
		Reports:        healthSvc,
		Policies:       claimgw.NewPolicies(workflows),
		WorkItems:      claimgw.NewWorkItems(logger),
		Audit:          auditpg.New(), Cursors: cursors, Logger: logger, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{
		h: h, claims: claims, requests: requests, authorizations: authorizations,
		health: healthSvc,
	}
	f.seed(t)
	return f
}

func (f *fixture) seed(t *testing.T) { //nolint:funlen // one linear fixture reads better whole
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

	f.tenant = h.CreateTenant("CLAIMS")
	f.actor = h.CreateActor("claim-clerk", "Claim Clerk")
	f.reviewer = h.CreateActor("claim-reviewer", "Claim Reviewer")
	sponsor := h.CreateTenantOrganization(f.tenant, "Claim Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(f.tenant, "Claim Payer", "PAYER")
	f.provider = h.CreateTenantOrganization(f.tenant, "Claim Hospital", "PROVIDER")
	f.otherOrg = h.CreateTenantOrganization(f.tenant, "Other Hospital", "PROVIDER")
	// The provider carries a tax identity, so invoice readiness is not blocked by the
	// directory. The test that asserts the blocker clears it deliberately.
	h.AdminExec(`
		UPDATE directory.organization o
		   SET tax_number_cipher = '\x00'::bytea,
		       tax_number_hash = decode(repeat('ab', 32), 'hex')
		  FROM directory.tenant_organization t
		 WHERE t.id = $1 AND o.id = t.organization_id`, f.provider)

	var providerProfile uuid.UUID
	scan(&providerProfile, "provider profile", `
		INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type, status)
		VALUES ($1, $2, 'HOSPITAL', 'ACTIVE') RETURNING id`, f.tenant, f.provider)

	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, f.tenant)
	scan(&f.person, "person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Deniz', 'Korkmaz', 'deniz korkmaz') RETURNING id`, f.tenant)
	var membership uuid.UUID
	scan(&membership, "membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, f.person, sponsor)

	h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name) VALUES ($1, 'BENEFIT', 'Fayda')`, f.tenant)
	scan(&f.program, "program", `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id, payer_tenant_organization_id,
		                             code, name, program_type, status, valid_period)
		VALUES ($1, $2, $3, 'PRG', 'Program', 'BENEFIT', 'ACTIVE', daterange('2026-01-01', NULL, '[)'))
		RETURNING id`, f.tenant, sponsor, payer)
	var planID uuid.UUID
	scan(&planID, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, f.tenant, f.program)
	// The version is opened as a draft so the entitlement definitions and the service
	// mappings can be written into it — a published version is frozen, and the mapping
	// trigger of migration 000035 enforces that — and published once they are there.
	scan(&f.planVersion, "plan version", `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period)
		VALUES ($1, $2, 1, 'DRAFT', daterange('2026-01-01','2027-01-01','[)')) RETURNING id`,
		f.tenant, planID)
	scan(&f.enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, membership, planID)

	var category uuid.UUID
	scan(&category, "service category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, f.tenant)
	service := func(code, name, unit string) uuid.UUID {
		var id uuid.UUID
		scan(&id, "service "+code, `
			INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
			                                        fulfillment_mode, default_unit_type, requires_provider)
			VALUES ($1, $2, $3, $4, 'DIRECT', $5, true) RETURNING id`,
			f.tenant, category, code, name, unit)
		return id
	}
	f.consult = service("CONSULT", "Muayene", "COUNT")
	f.physio = service("PHYSIO_SESSION", "Fizyoterapi seansı", "SESSION")
	f.lab = service("LAB_TEST", "Laboratuvar tetkiki", "COUNT")

	// One entitlement per service, its code matching the service's own — the convention
	// WP-I4-02 maps an authorization line onto a balance with. The mapping table beside it
	// is WP-I5-05's, and it is what the claim's pricing reads.
	entitlement := func(code, name string) uuid.UUID {
		var id uuid.UUID
		scan(&id, "entitlement "+code, `
			INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name,
			                                            unit_type, currency_code, period_type,
			                                            initial_quantity)
			VALUES ($1, $2, $3, $4, 'MONEY', 'TRY', 'CALENDAR_YEAR', 5000) RETURNING id`,
			f.tenant, f.planVersion, code, name)
		return id
	}
	consultDef := entitlement("CONSULT", "Muayene bütçesi")
	physioDef := entitlement("PHYSIO_SESSION", "Fizyoterapi bütçesi")
	labDef := entitlement("LAB_TEST", "Laboratuvar bütçesi")
	for _, pair := range []struct {
		service, definition uuid.UUID
	}{
		{f.consult, consultDef}, {f.physio, physioDef}, {f.lab, labDef},
	} {
		h.AdminExec(`
			INSERT INTO benefit.service_entitlement_mapping (tenant_id, plan_version_id,
			                                                 service_definition_id,
			                                                 entitlement_definition_id)
			VALUES ($1, $2, $3, $4)`, f.tenant, f.planVersion, pair.service, pair.definition)
	}
	h.AdminExec(`
		UPDATE benefit.plan_version SET status = 'PUBLISHED', published_at = clock_timestamp(),
		       published_by = $2
		 WHERE id = $1`, f.planVersion, f.actor)

	account := func(definition uuid.UUID, key string) uuid.UUID {
		var id uuid.UUID
		scan(&id, "account "+key, `
			INSERT INTO benefit.entitlement_account (tenant_id, enrollment_id, entitlement_definition_id,
			                                         benefit_period, total_granted, available_quantity)
			VALUES ($1, $2, $3, daterange('2026-01-01','2027-01-01','[)'), 5000, 5000) RETURNING id`,
			f.tenant, f.enrollment, definition)
		h.AdminExec(`
			INSERT INTO benefit.entitlement_ledger (tenant_id, entitlement_account_id, movement_type,
			                                        effective_at, delta_total, delta_available,
			                                        reference_type, reference_id, idempotency_key)
			VALUES ($1, $2, 'GRANT', clock_timestamp(), 5000, 5000, 'ENROLLMENT', $3, $4)`,
			f.tenant, id, f.enrollment, "grant:seed:"+key)
		return id
	}
	f.consultAcct = account(consultDef, "consult")
	f.physioAcct = account(physioDef, "physio")
	account(labDef, "lab")

	// The contract the lines are priced against. The physiotherapy price is per unit, so a
	// claim for two sessions is twice a claim for one; the consultation is a flat price with
	// a member share, which is where the split arithmetic is exercised.
	var contractID, contractVersion, priceList uuid.UUID
	scan(&contractID, "contract", `
		INSERT INTO contract.contract (tenant_id, code, name, payer_organization_id,
		                               provider_profile_id, domain_code, status)
		VALUES ($1, 'HEALTH_2026', 'Sağlık 2026', $2, $3, 'HEALTH', 'ACTIVE') RETURNING id`,
		f.tenant, payer, providerProfile)
	scan(&contractVersion, "contract version", `
		INSERT INTO contract.contract_version (tenant_id, contract_id, version_no, status,
		                                       valid_from, valid_to, currency_code,
		                                       configuration_hash, published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', '2026-01-01', '2027-01-01', 'TRY', 'c1a1m',
		        clock_timestamp(), $3) RETURNING id`, f.tenant, contractID, f.actor)
	scan(&priceList, "price list", `
		INSERT INTO contract.price_list (tenant_id, contract_version_id, code, name)
		VALUES ($1, $2, 'STANDART', 'Standart liste') RETURNING id`, f.tenant, contractVersion)
	h.AdminExec(`
		INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id, unit_type,
		                                 pricing_method, amount, member_share_method,
		                                 member_share_percent, valid_from)
		VALUES ($1, $2, $3, 'COUNT', 'FIXED', $4::text::numeric, 'PERCENT', $5::text::numeric, '2026-01-01')`,
		f.tenant, priceList, f.consult, consultPrice, consultShare)
	h.AdminExec(`
		INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id, unit_type,
		                                 pricing_method, amount, member_share_method, valid_from)
		VALUES ($1, $2, $3, 'SESSION', 'UNIT', $4::text::numeric, 'NONE', '2026-01-01')`,
		f.tenant, priceList, f.physio, physioUnitPrice)
	// LAB_TEST is deliberately left unpriced: a line the ladder cannot price is what sends a
	// claim to financial review with the pricing explanation attached.

	// The two queues a routed claim is raised into.
	scan(&f.medicalQueue, "medical queue", `
		INSERT INTO workflow.work_queue (tenant_id, code, name, domain_code, assignment_policy)
		VALUES ($1, 'MEDICAL_REVIEW', 'Tıbbi Değerlendirme', 'HEALTH', 'MANUAL') RETURNING id`, f.tenant)
	h.AdminExec(`
		INSERT INTO workflow.work_queue (tenant_id, code, name, domain_code, assignment_policy)
		VALUES ($1, 'FINANCIAL_REVIEW', 'Mali Değerlendirme', 'HEALTH', 'MANUAL')`, f.tenant)

	// The episode of care, with an encounter and a real diagnosis on it. Every projection
	// test below runs against a claim whose lines actually carry the clinical fields, which
	// is the only way an assertion that a field is absent proves anything.
	scan(&f.caseID, "health case", `
		INSERT INTO health.health_case (tenant_id, person_id, program_id, enrollment_id, case_type,
		                                provider_organization_id, opened_at)
		VALUES ($1, $2, $3, $4, 'OUTPATIENT', $5, $6) RETURNING id`,
		f.tenant, f.person, f.program, f.enrollment, f.provider, serviceDay)
	scan(&f.encounterID, "encounter", `
		INSERT INTO health.encounter (tenant_id, case_id, encounter_type, started_at, ended_at,
		                              branch_code, notes_clinical)
		VALUES ($1, $2, 'OUTPATIENT', $3, $4, 'ORTOPEDI', 'Sol dizde ağrı.') RETURNING id`,
		f.tenant, f.caseID, serviceDay, serviceDay.Add(time.Hour))
	var codeSystem, codeValue uuid.UUID
	scan(&codeSystem, "code system", `
		INSERT INTO catalog.code_system (tenant_id, code, name, version, authority, valid_from)
		VALUES ($1, 'ICD10', 'ICD-10', '2026', 'WHO', '2026-01-01') RETURNING id`, f.tenant)
	scan(&codeValue, "code value", `
		INSERT INTO catalog.code_value (tenant_id, code_system_id, code, display, valid_from)
		VALUES ($1, $2, 'M23.2', 'Menisküs yırtığı', '2026-01-01') RETURNING id`, f.tenant, codeSystem)
	scan(&f.diagnosisID, "diagnosis", `
		INSERT INTO health.diagnosis (tenant_id, encounter_id, code_system_id, code_value_id,
		                              diagnosis_type, sensitive)
		VALUES ($1, $2, $3, $4, 'PRIMARY', false) RETURNING id`,
		f.tenant, f.encounterID, codeSystem, codeValue)
}

// providerRC is the provider's billing clerk: it may raise, submit and cancel a claim, and it
// is scoped to one organization.
func (f *fixture) providerRC() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{
			application.PermissionRead: {}, application.PermissionCreate: {},
			application.PermissionSubmit: {}, application.PermissionCancel: {},
		},
		Scopes: []identity.Scope{{
			Type: application.ScopeOrganization,
			ID:   uuid.NullUUID{UUID: f.provider, Valid: true},
		}},
	}
}

// medicalRC is the medical reviewer: it decides on clinical grounds and sees the clinical
// projection, exactly as internal/identity/application/roles.go issues the role.
func (f *fixture) medicalRC() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.reviewer},
		Permissions: map[string]struct{}{
			application.PermissionRead: {}, application.PermissionMedicalReview: {},
			application.PermissionClinicalRead: {}, application.PermissionSensitiveRead: {},
		},
	}
}

// financialRC is the financial reviewer. It holds no clinical grant, which is what
// FINANCIAL_REVIEWER holds in roles.go — and it is why the financial projection is what this
// caller is served.
func (f *fixture) financialRC() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.reviewer},
		Permissions: map[string]struct{}{
			application.PermissionRead: {}, application.PermissionFinancialReview: {},
		},
	}
}

// sponsorHRRC is the sponsor's HR user: member.read, service_request.read, health.case.read,
// claim.read — and no clinical grant of any kind.
func (f *fixture) sponsorHRRC() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal:   identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{application.PermissionRead: {}},
	}
}

// requestRC is the caller that raises and decides the preauthorization behind an
// authorization. It is a different job from claiming, so it is a different context.
func (f *fixture) requestRC() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.reviewer},
		Permissions: map[string]struct{}{
			servicerequestapp.PermissionRead: {}, servicerequestapp.PermissionCreate: {},
			servicerequestapp.PermissionSubmit: {}, servicerequestapp.PermissionReview: {},
			"authorization.manage": {},
		},
	}
}

// newClaim raises a draft with the given lines.
func (f *fixture) newClaim(t *testing.T, lines []application.NewLineInput,
	mutate func(*application.NewClaimInput),
) application.ClaimView {
	t.Helper()
	in := application.NewClaimInput{
		PersonID: f.person, ProgramID: f.program, EnrollmentID: f.enrollment,
		ProviderOrganizationID: f.provider, CaseID: &f.caseID,
		ServiceDateFrom: serviceDay, ServiceDateTo: serviceDay, Lines: lines,
	}
	if mutate != nil {
		mutate(&in)
	}
	view, err := f.claims.CreateClaim(context.Background(), f.providerRC(), in, application.AccessRequest{})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}
	return view
}

// consultLine is the priced, auto-approvable line. It carries a description and a diagnosis on
// purpose: every test that asserts a field is absent runs against a claim where it is present.
func (f *fixture) consultLine(lineNo int) application.NewLineInput {
	description := "Sol diz artroskopi sonrası kontrol muayenesi"
	return application.NewLineInput{
		LineNo: lineNo, ServiceDefinitionID: f.consult, UnitType: "COUNT",
		Quantity: "1", LineAmount: consultPrice, Description: &description,
		DiagnosisID: &f.diagnosisID,
	}
}

// physioLine is the line an authorization is taken against.
func (f *fixture) physioLine(lineNo int, quantity, amount string) application.NewLineInput {
	description := "Menisküs sonrası fizik tedavi seansları"
	return application.NewLineInput{
		LineNo: lineNo, ServiceDefinitionID: f.physio, UnitType: "SESSION",
		Quantity: quantity, LineAmount: amount, Description: &description,
		DiagnosisID: &f.diagnosisID,
	}
}

// labLine is the line the pricing ladder cannot price, because nothing contracted it.
func (f *fixture) labLine(lineNo int) application.NewLineInput {
	description := "Sedimantasyon ve CRP"
	return application.NewLineInput{
		LineNo: lineNo, ServiceDefinitionID: f.lab, UnitType: "COUNT",
		Quantity: "1", LineAmount: "200", Description: &description,
	}
}

// authorizeSessions takes a real hold for the given number of physiotherapy sessions: a
// preauthorization request through WP-I4-01's own gate, approved by a reviewer, and the
// authorization WP-I4-02 creates from it. Nothing is faked, so the consume the claim performs
// moves a real ledger balance.
func (f *fixture) authorizeSessions(t *testing.T, sessions string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	rc := f.requestRC()
	provider := f.provider
	draft, err := f.requests.Create(ctx, rc, servicerequestapp.NewRequestInput{
		RequestType: "PREAUTHORIZATION", PersonID: f.person, ProgramID: f.program,
		EnrollmentID: f.enrollment, ProviderOrganizationID: &provider,
		ServiceDate: serviceDay, Channel: "PROVIDER_PORTAL",
		Items: []servicerequestdomain.ItemInput{{
			ServiceDefinitionID: f.physio.String(), RequestedQuantity: sessions,
			UnitType: "SESSION",
		}},
	})
	if err != nil {
		t.Fatalf("create preauthorization: %v", err)
	}
	submitted, err := f.requests.Submit(ctx, rc, draft.Request.ID, nil, draft.Request.RowVersion)
	if err != nil {
		t.Fatalf("submit preauthorization: %v", err)
	}
	if submitted.Request.Status != "APPROVED" {
		approved, err := f.requests.Approve(ctx, rc, draft.Request.ID, servicerequestapp.DecisionInput{
			ReasonCode: "MEDICALLY_NECESSARY", ExpectedVersion: submitted.Request.RowVersion,
		})
		if err != nil {
			t.Fatalf("approve preauthorization: %v", err)
		}
		submitted = approved
	}
	authorization, err := f.authorizations.Create(ctx, rc, authorizationapp.NewAuthorizationInput{
		RequestID: draft.Request.ID, ValidTo: serviceDay.AddDate(0, 3, 0),
		IdempotencyKey: "claim-test-authorization-" + draft.Request.ID.String(),
	})
	if err != nil {
		t.Fatalf("create authorization: %v", err)
	}
	return authorization.Authorization.ID
}

// publishAdjudicationRule publishes one ADJUDICATION rule set version carrying one rule. It is
// the real engine over the real tables: the claim reads what an author would have published.
func (f *fixture) publishAdjudicationRule(t *testing.T, code, condition, actions string) {
	t.Helper()
	h := f.h
	ctx, cancel := h.Ctx()
	defer cancel()
	var setID, versionID uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO rules.rule_set (tenant_id, code, name, domain_code, purpose, status)
		VALUES ($1, $2, $2, 'HEALTH', 'ADJUDICATION', 'ACTIVE') RETURNING id`,
		f.tenant, code).Scan(&setID); err != nil {
		t.Fatalf("seed rule set: %v", err)
	}
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO rules.rule_set_version (tenant_id, rule_set_id, version_no, status,
		                                    valid_from, valid_to, input_schema, content_hash,
		                                    published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', '2026-01-01', '2027-01-01',
		        '{"serviceCode":"string","lineAmount":"string"}'::jsonb, 'c1a1m-rule',
		        clock_timestamp(), $3) RETURNING id`,
		f.tenant, setID, f.actor).Scan(&versionID); err != nil {
		t.Fatalf("seed rule set version: %v", err)
	}
	h.AdminExec(`
		INSERT INTO rules.rule (tenant_id, rule_set_version_id, code, name, priority, condition,
		                        actions, explanation_code)
		VALUES ($1, $2, $3, $3, 10, $4, $5::jsonb, 'CLAIM_RULE')`,
		f.tenant, versionID, code, condition, actions)
}

// consumedTotal reads what the authorization has drawn down, as exact decimal text.
func (f *fixture) consumedTotal(t *testing.T, authorizationID uuid.UUID) string {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var total string
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT trim_scale(consumed_total)::text FROM service.authorization WHERE id = $1`,
		authorizationID).Scan(&total); err != nil {
		t.Fatalf("read consumed total: %v", err)
	}
	return total
}

// ledgerConserved is WP-I4-02's assertion, applied after every flow in this file: every
// account's stored balance equals the sum of the movements posted against it, and no movement
// ever left an account negative.
func (f *fixture) ledgerConserved(t *testing.T) {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	rows, err := f.h.Admin.Query(ctx, `
		SELECT a.id,
		       trim_scale(a.available_quantity)::text,
		       trim_scale(COALESCE(SUM(l.delta_available), 0))::text,
		       trim_scale(a.total_granted)::text,
		       trim_scale(COALESCE(SUM(l.delta_total), 0))::text
		  FROM benefit.entitlement_account a
		  LEFT JOIN benefit.entitlement_ledger l ON l.entitlement_account_id = a.id
		 WHERE a.tenant_id = $1
		 GROUP BY a.id`, f.tenant)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	defer rows.Close()
	checked := 0
	for rows.Next() {
		var id uuid.UUID
		var available, movements, granted, grantedMovements string
		if err := rows.Scan(&id, &available, &movements, &granted, &grantedMovements); err != nil {
			t.Fatalf("scan ledger: %v", err)
		}
		checked++
		if available != movements {
			t.Errorf("account %s available = %s, movements sum to %s", id, available, movements)
		}
		if granted != grantedMovements {
			t.Errorf("account %s granted = %s, movements sum to %s", id, granted, grantedMovements)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate ledger: %v", err)
	}
	if checked < 3 {
		t.Fatalf("expected the fixture's three entitlement accounts, checked %d", checked)
	}
	var negative int
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM benefit.entitlement_account
		 WHERE tenant_id = $1 AND (available_quantity < 0 OR total_granted < 0)`,
		f.tenant).Scan(&negative); err != nil {
		t.Fatalf("count negative balances: %v", err)
	}
	if negative != 0 {
		t.Errorf("%d entitlement accounts went negative", negative)
	}
}

// lineByNo finds one line of a view by its number.
func lineByNo(t *testing.T, view application.ClaimView, lineNo int) application.LineView {
	t.Helper()
	for _, line := range view.Lines {
		if line.Line.LineNo == lineNo {
			return line
		}
	}
	t.Fatalf("claim has no line %d", lineNo)
	return application.LineView{}
}

// exceptionByCode finds one exception of a view by its code.
func exceptionByCode(view application.ClaimView, code string) (application.ClaimException, bool) {
	for _, exception := range view.Exceptions {
		if exception.Code == code {
			return exception, true
		}
	}
	return application.ClaimException{}, false
}

// mustQuantity parses an exact decimal the service produced; a failure here is a failure of
// the service rather than of the test.
func mustQuantity(t *testing.T, raw string) benefitdomain.Quantity {
	t.Helper()
	value, err := benefitdomain.ParseQuantity(raw)
	if err != nil {
		t.Fatalf("%q is not an exact decimal: %v", raw, err)
	}
	return value
}

// zeroSum is the exact decimal zero an accumulator starts at.
func zeroSum() benefitdomain.Quantity { return benefitdomain.ZeroQuantity() }
