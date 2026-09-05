package eligibility_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/eligibility"
)

// The entitlement the mapped plan carries, and the factor one unit of the mapped service
// draws from it. Two, and not one, because a factor of one is a factor that could be
// missing: with it, "four sessions out of ten" and "four sessions drawing eight of ten"
// are different answers and the test can tell them apart.
const (
	defSession       = "SESSION"
	sessionQuantity  = "10"
	mappedUnitFactor = "2"
)

// mappedWorld is a second plan beside the fixture's own: a version whose mappings were
// written while it was still a draft and which was then published, one catalogue service
// mapped onto SESSION and one mapped onto nothing at all.
type mappedWorld struct {
	planID      uuid.UUID
	versionID   uuid.UUID
	mappedSvc   uuid.UUID
	unmappedSvc uuid.UUID
	person      uuid.UUID
	membership  uuid.UUID
	enrollment  uuid.UUID
}

// seedMapped builds that world. The order is the rule the schema enforces: the version is
// inserted as a DRAFT, the mapping is written while it is one, and only then is it
// published — a mapping cannot be attached to a published version by any route at all.
func (f *fixture) seedMapped(t *testing.T) mappedWorld {
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

	var w mappedWorld
	var categoryID uuid.UUID
	scan(&categoryID, "service category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'ELIG_CAT', 'Eşleşme', 'HEALTH') RETURNING id`, f.tenant)
	scan(&w.mappedSvc, "mapped service", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'ELIG_PHYSIO', 'Fizyoterapi', 'SESSION', 'SESSION') RETURNING id`,
		f.tenant, categoryID)
	scan(&w.unmappedSvc, "unmapped service", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'ELIG_UNMAPPED', 'Eşlenmemiş hizmet', 'APPOINTMENT', 'COUNT') RETURNING id`,
		f.tenant, categoryID)

	scan(&w.planID, "mapped plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN_MAPPED', 'Eşlenmiş plan', 'ACTIVE') RETURNING id`, f.tenant, f.program)
	scan(&w.versionID, "mapped plan version", `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period)
		VALUES ($1, $2, 1, 'DRAFT', daterange('2026-01-01','2027-01-01','[)')) RETURNING id`,
		f.tenant, w.planID)
	var definitionID uuid.UUID
	scan(&definitionID, "session definition", `
		INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name, unit_type,
		                                            period_type, initial_quantity, allow_overdraft, family_shared)
		VALUES ($1, $2, $3, $3, 'SESSION', 'CALENDAR_YEAR', $4::text::numeric, false, false) RETURNING id`,
		f.tenant, w.versionID, defSession, sessionQuantity)
	h.AdminExec(`
		INSERT INTO benefit.service_entitlement_mapping (tenant_id, plan_version_id, service_definition_id,
		                                                 entitlement_definition_id, unit_factor)
		VALUES ($1, $2, $3, $4, $5::text::numeric)`,
		f.tenant, w.versionID, w.mappedSvc, definitionID, mappedUnitFactor)
	h.AdminExec(`
		UPDATE benefit.plan_version
		   SET status = 'PUBLISHED', published_at = clock_timestamp(), published_by = $2
		 WHERE id = $1`, w.versionID, f.actor)

	scan(&w.person, "mapped person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Melis', 'Aksu', 'melis aksu') RETURNING id`, f.tenant)
	scan(&w.membership, "mapped membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, w.person, f.sponsor)
	scan(&w.enrollment, "mapped enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, w.membership, w.planID)
	return w
}

// serviceRequest is a check that names a catalogue service and gives no entitlement hint
// at all: whatever the answer is, the mapping is the only thing that could have produced
// it.
func serviceRequest(person uuid.UUID, date time.Time, service uuid.UUID, quantity string) eligibility.CheckInput {
	return eligibility.CheckInput{
		PersonID: person, ServiceDate: date,
		Items: []eligibility.RequestItem{{
			ServiceDefinitionID: service, Quantity: domain.MustQuantity(quantity),
		}},
	}
}

// TestMappedServiceIsJudgedAgainstItsEntitlementBalance is the acceptance criterion of
// WP-I5-05 in one function: a provider's check says "Uygun" for a mapped service, and the
// same service on a plan nobody has mapped is still SERVICE_MAPPING_PENDING.
func TestMappedServiceIsJudgedAgainstItsEntitlementBalance(t *testing.T) {
	f := newFixture(t)
	w := f.seedMapped(t)

	// Four sessions at a factor of two draw eight of the ten the entitlement carries.
	result := f.check(t, serviceRequest(w.person, checkDate, w.mappedSvc, "4"))
	if result.Outcome != eligibility.OutcomeEligible || !result.Eligible {
		t.Fatalf("outcome = %s (eligible=%v), want ELIGIBLE: %+v", result.Outcome, result.Eligible, itemCodes(result.Items))
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(result.Items))
	}
	item := result.Items[0]
	if item.EntitlementCode == nil || *item.EntitlementCode != defSession {
		t.Fatalf("entitlementCode = %v, want %s — the mapping did not decide the line", item.EntitlementCode, defSession)
	}
	if item.AvailableQuantity == nil || item.AvailableQuantity.String() != sessionQuantity {
		t.Fatalf("availableQuantity = %v, want %s", item.AvailableQuantity, sessionQuantity)
	}
	if got := balanceOf(t, result, defSession); got != sessionQuantity {
		t.Fatalf("SESSION balance = %s, want %s", got, sessionQuantity)
	}

	// The same catalogue service asked for by somebody on the unmapped plan. Nothing about
	// the service changed; the plan version behind the person did.
	unmappedPlan := f.check(t, serviceRequest(f.principal, checkDate, w.mappedSvc, "4"))
	if unmappedPlan.Outcome != eligibility.OutcomeReviewRequired {
		t.Fatalf("unmapped plan outcome = %s, want REVIEW_REQUIRED", unmappedPlan.Outcome)
	}
	if codes := itemCodes(unmappedPlan.Items); len(codes) != 1 || codes[0] != eligibility.CodeServiceMappingPending {
		t.Fatalf("unmapped plan codes = %v, want [SERVICE_MAPPING_PENDING]", codes)
	}

	// And a service the mapped plan version does not map is pending too, on the very plan
	// that maps something else: the mapping is per service, not per plan.
	unmappedService := f.check(t, serviceRequest(w.person, checkDate, w.unmappedSvc, "1"))
	if unmappedService.Outcome != eligibility.OutcomeReviewRequired {
		t.Fatalf("unmapped service outcome = %s, want REVIEW_REQUIRED", unmappedService.Outcome)
	}
	if codes := itemCodes(unmappedService.Items); len(codes) != 1 || codes[0] != eligibility.CodeServiceMappingPending {
		t.Fatalf("unmapped service codes = %v, want [SERVICE_MAPPING_PENDING]", codes)
	}
}

// TestUnitFactorDecidesHowMuchIsDrawn: the factor is the reason the mapping is a table
// rather than a two-column join. Six sessions at a factor of two draw twelve of ten, and
// the answer has to be "no" — a resolver that ignored the factor would say six of ten and
// approve it.
func TestUnitFactorDecidesHowMuchIsDrawn(t *testing.T) {
	f := newFixture(t)
	w := f.seedMapped(t)

	within := f.check(t, serviceRequest(w.person, checkDate, w.mappedSvc, "5"))
	if within.Outcome != eligibility.OutcomeEligible {
		t.Fatalf("five sessions (ten of ten) = %s, want ELIGIBLE", within.Outcome)
	}

	over := f.check(t, serviceRequest(w.person, checkDate, w.mappedSvc, "6"))
	if over.Outcome != eligibility.OutcomeIneligible {
		t.Fatalf("six sessions (twelve of ten) = %s, want INELIGIBLE", over.Outcome)
	}
	if codes := itemCodes(over.Items); len(codes) != 1 || codes[0] != eligibility.CodeBalanceInsufficient {
		t.Fatalf("codes = %v, want [BALANCE_INSUFFICIENT]", codes)
	}
	// The line still reports what was asked for, in the unit the caller asked in: the
	// factor decides what is drawn, not what is displayed.
	if q := over.Items[0].RequestedQuantity.String(); q != "6" {
		t.Fatalf("requestedQuantity = %s, want 6", q)
	}
}

// TestEnrollmentMultipleNamesTheCandidatesAndAskingAgainResolves closes the second thing
// M4's screens found: a desk told "this person has more than one plan" could not ask
// again, because a provider may not list a person's enrollments and had nothing to name.
func TestEnrollmentMultipleNamesTheCandidatesAndAskingAgainResolves(t *testing.T) {
	f := newFixture(t)
	w := f.seedMapped(t)

	// A second enrollment for the same person, in the fixture's own plan. Two active
	// enrollments on one service date is exactly the situation ENROLLMENT_MULTIPLE names.
	var second uuid.UUID
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, w.membership, f.planID).Scan(&second); err != nil {
		t.Fatalf("seed second enrollment: %v", err)
	}

	ambiguous := f.check(t, serviceRequest(w.person, checkDate, w.mappedSvc, "1"))
	if ambiguous.Outcome != eligibility.OutcomeReviewRequired {
		t.Fatalf("outcome = %s, want REVIEW_REQUIRED", ambiguous.Outcome)
	}
	if !hasCode(ambiguous.Explanations, eligibility.CodeEnrollmentMultiple) {
		t.Fatalf("explanations = %+v, want ENROLLMENT_MULTIPLE", ambiguous.Explanations)
	}
	if len(ambiguous.EnrollmentCandidates) != 2 {
		t.Fatalf("candidates = %d, want 2: %+v", len(ambiguous.EnrollmentCandidates), ambiguous.EnrollmentCandidates)
	}
	named := map[uuid.UUID]string{}
	for _, c := range ambiguous.EnrollmentCandidates {
		if c.PlanCode == "" || c.PlanName == "" || c.ValidFrom != "2026-01-01" {
			t.Fatalf("candidate is not usable by a desk: %+v", c)
		}
		named[c.EnrollmentID] = c.PlanCode
	}
	if named[w.enrollment] != "PLAN_MAPPED" || named[second] != "PLAN" {
		t.Fatalf("candidates name the wrong enrollments: %+v", named)
	}

	// Asking again with one of the ids the answer named resolves it, and the answer is
	// the one that enrollment's plan gives: the mapped plan judges the line, the fixture's
	// own plan does not map the service at all.
	chosen := w.enrollment
	in := serviceRequest(w.person, checkDate, w.mappedSvc, "1")
	in.EnrollmentID = &chosen
	resolved := f.check(t, in)
	if resolved.Outcome != eligibility.OutcomeEligible {
		t.Fatalf("outcome after choosing = %s, want ELIGIBLE", resolved.Outcome)
	}
	if resolved.EnrollmentID == nil || *resolved.EnrollmentID != chosen {
		t.Fatalf("enrollmentId = %v, want %s", resolved.EnrollmentID, chosen)
	}
	if len(resolved.EnrollmentCandidates) != 0 {
		t.Fatalf("a resolved check still carries candidates: %+v", resolved.EnrollmentCandidates)
	}

	other := second
	in.EnrollmentID = &other
	otherResult := f.check(t, in)
	if otherResult.EnrollmentID == nil || *otherResult.EnrollmentID != other {
		t.Fatalf("enrollmentId = %v, want %s", otherResult.EnrollmentID, other)
	}
	if codes := itemCodes(otherResult.Items); len(codes) != 1 || codes[0] != eligibility.CodeServiceMappingPending {
		t.Fatalf("the other plan's answer = %v, want [SERVICE_MAPPING_PENDING]", codes)
	}
}

func hasCode(explanations []eligibility.ExplanationView, code string) bool {
	for _, e := range explanations {
		if e.Code == code {
			return true
		}
	}
	return false
}
