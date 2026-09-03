package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/domain"
	benefitpg "github.com/celikbros/kapsora/internal/benefit/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

type fixture struct {
	h          *dbtest.Harness
	svc        *application.Service
	tenantA    uuid.UUID
	tenantB    uuid.UUID
	maker      uuid.UUID
	checker    uuid.UUID
	sponsorOrg uuid.UUID
	payerOrg   uuid.UUID
	person     uuid.UUID
	membership uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := dbtest.New(t)
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: benefitpg.New(), Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		h: h, svc: svc,
		tenantA: h.CreateTenant("BENEFIT_A"), tenantB: h.CreateTenant("BENEFIT_B"),
		maker:   h.CreateActor("benefit-maker", "Benefit Maker"),
		checker: h.CreateActor("benefit-checker", "Benefit Checker"),
	}
	f.seedProgramTypes(f.tenantA)
	f.seedProgramTypes(f.tenantB)
	f.sponsorOrg = h.CreateTenantOrganization(f.tenantA, "Benefit Sponsor", "SPONSOR")
	f.payerOrg = h.CreateTenantOrganization(f.tenantA, "Benefit Payer", "PAYER")
	f.person, f.membership = f.seedMember(f.tenantA, f.sponsorOrg, "2026-01-01")
	return f
}

// seedProgramTypes writes the baseline catalog WP-I1-02 provisioning gives a new tenant.
func (f *fixture) seedProgramTypes(tenant uuid.UUID) {
	f.h.T.Helper()
	for _, e := range identityapp.DefaultBaselineCatalogs().ProgramTypes {
		f.h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name) VALUES ($1, $2, $3)`,
			tenant, e.Code, e.DisplayName)
	}
}

// seedMember creates a synthetic person with one ACTIVE sponsor membership.
func (f *fixture) seedMember(tenant, sponsor uuid.UUID, from string) (person, membership uuid.UUID) {
	f.h.T.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Zeynep', 'Karaca', 'zeynep karaca') RETURNING id`, tenant).Scan(&person); err != nil {
		f.h.T.Fatalf("seed person: %v", err)
	}
	f.h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name)
	               VALUES ($1, 'MEMBER', 'Üye') ON CONFLICT DO NOTHING`, tenant)
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange($4::date, NULL, '[)')) RETURNING id`,
		tenant, person, sponsor, from).Scan(&membership); err != nil {
		f.h.T.Fatalf("seed membership: %v", err)
	}
	return person, membership
}

func (f *fixture) rc(tenant, actor uuid.UUID) identity.RequestContext {
	perms := map[string]struct{}{}
	for _, p := range []string{"program.read", "program.manage", "plan.manage", "plan.publish", "enrollment.manage"} {
		perms[p] = struct{}{}
	}
	return identity.RequestContext{
		TenantID: tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: actor}, StepUpValid: true, Permissions: perms,
	}
}

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return d
}

func dayPtr(t *testing.T, s string) *time.Time {
	d := day(t, s)
	return &d
}

// program creates an ACTIVE program with one ACTIVE plan and returns the plan id.
func (f *fixture) activePlan(t *testing.T, code string) (programID, planID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	rc := f.rc(f.tenantA, f.maker)
	program, err := f.svc.CreateProgram(ctx, rc, application.NewProgramInput{
		Code: code, Name: "Program " + code, ProgramType: "EMPLOYEE_BENEFIT",
		SponsorOrganizationID: f.sponsorOrg, PayerOrganizationID: f.payerOrg,
		ValidFrom: dayPtr(t, "2026-01-01"),
	})
	if err != nil {
		t.Fatalf("create program: %v", err)
	}
	program, err = f.svc.UpdateProgram(ctx, rc, program.ID, application.ProgramPatch{
		Status: strPtr("ACTIVE"), ExpectedVersion: program.RowVersion,
	})
	if err != nil {
		t.Fatalf("activate program: %v", err)
	}
	plan, err := f.svc.CreatePlan(ctx, rc, program.ID, application.NewPlanInput{Code: code + "-P1", Name: "Plan 1"})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	plan, err = f.svc.UpdatePlan(ctx, rc, plan.ID, application.PlanPatch{
		Status: strPtr("ACTIVE"), ExpectedVersion: plan.RowVersion,
	})
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}
	return program.ID, plan.ID
}

func strPtr(s string) *string { return &s }

func sampleDefinitions() []domain.EntitlementDefinition {
	return []domain.EntitlementDefinition{
		{Code: "DENTAL", Name: "Diş", UnitType: "MONEY", CurrencyCode: "TRY",
			PeriodType: "CALENDAR_YEAR", InitialQuantity: "1500.50", RolloverPolicy: "NONE"},
		{Code: "CHECKUP", Name: "Kontrol", UnitType: "COUNT", PeriodType: "LIFETIME",
			InitialQuantity: "2", RolloverPolicy: "NONE"},
	}
}

// publishVersion runs the whole maker-checker path and returns the published version.
func (f *fixture) publishVersion(t *testing.T, planID uuid.UUID, from, to string) application.PlanVersion {
	t.Helper()
	ctx := context.Background()
	maker, checker := f.rc(f.tenantA, f.maker), f.rc(f.tenantA, f.checker)
	in := application.NewPlanVersionInput{ValidFrom: dayPtr(t, from)}
	if to != "" {
		in.ValidTo = dayPtr(t, to)
	}
	version, err := f.svc.CreatePlanVersion(ctx, maker, planID, in)
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	version, err = f.svc.ReplaceDefinitions(ctx, maker, version.ID, sampleDefinitions(), version.RowVersion)
	if err != nil {
		t.Fatalf("replace definitions: %v", err)
	}
	version, err = f.svc.SubmitPlanVersion(ctx, maker, version.ID, strPtr("hazır"), version.RowVersion)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	version, err = f.svc.PublishPlanVersion(ctx, checker, version.ID, nil, version.RowVersion)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	return version
}

func TestPlanVersionLifecycleDraftReviewPublishRetire(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	maker, checker := f.rc(f.tenantA, f.maker), f.rc(f.tenantA, f.checker)
	_, planID := f.activePlan(t, "LIFE")

	version, err := f.svc.CreatePlanVersion(ctx, maker, planID, application.NewPlanVersionInput{
		ValidFrom: dayPtr(t, "2026-01-01"), ValidTo: dayPtr(t, "2027-01-01"), Notes: strPtr("ilk sürüm"),
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	if version.VersionNo != 1 || version.Status != domain.VersionDraft || version.ConfigurationHash != "" {
		t.Fatalf("draft = %+v", version)
	}

	// A submit without definitions is refused.
	if _, err := f.svc.SubmitPlanVersion(ctx, maker, version.ID, nil, version.RowVersion); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("submit without definitions = %v, want a validation error", err)
	}

	version, err = f.svc.ReplaceDefinitions(ctx, maker, version.ID, sampleDefinitions(), version.RowVersion)
	if err != nil {
		t.Fatalf("replace definitions: %v", err)
	}
	if len(version.Definitions) != 2 {
		t.Fatalf("definitions = %+v", version.Definitions)
	}
	if version.Definitions[0].Spec.Code != "CHECKUP" {
		t.Fatalf("definitions are not ordered by code: %+v", version.Definitions)
	}
	for _, d := range version.Definitions {
		if d.Spec.Code == "DENTAL" && d.Spec.InitialQuantity != "1500.5" {
			t.Fatalf("quantity round-trip lost precision: %q", d.Spec.InitialQuantity)
		}
	}

	version, err = f.svc.SubmitPlanVersion(ctx, maker, version.ID, strPtr("incelemeye hazır"), version.RowVersion)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if version.Status != domain.VersionUnderReview || version.SubmittedBy == nil || *version.SubmittedBy != f.maker {
		t.Fatalf("under review = %+v", version)
	}

	published, err := f.svc.PublishPlanVersion(ctx, checker, version.ID, strPtr("onaylandı"), version.RowVersion)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.Status != domain.VersionPublished || published.PublishedAt == nil ||
		published.PublishedBy == nil || *published.PublishedBy != f.checker {
		t.Fatalf("published = %+v", published)
	}
	if len(published.ConfigurationHash) != 64 {
		t.Fatalf("configuration hash = %q", published.ConfigurationHash)
	}

	// The stored hash must equal a fresh hash over the stored configuration.
	want, err := domain.ConfigurationHash(published.ValidFrom, published.ValidTo, specsOf(published))
	if err != nil {
		t.Fatal(err)
	}
	if domain.HexHash(want) != published.ConfigurationHash {
		t.Fatalf("stored hash %s != recomputed %s", published.ConfigurationHash, domain.HexHash(want))
	}

	retired, err := f.svc.RetirePlanVersion(ctx, checker, published.ID, "SUPERSEDED", strPtr("yeni sürüm"), published.RowVersion)
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if retired.Status != domain.VersionRetired || retired.RetireReasonCode == nil || *retired.RetireReasonCode != "SUPERSEDED" {
		t.Fatalf("retired = %+v", retired)
	}
	// A retired version stays readable with its hash.
	again, err := f.svc.GetPlanVersion(ctx, maker, published.ID)
	if err != nil || again.ConfigurationHash != published.ConfigurationHash {
		t.Fatalf("retired version is no longer readable: %+v %v", again, err)
	}
}

func specsOf(v application.PlanVersion) []domain.EntitlementDefinition {
	out := make([]domain.EntitlementDefinition, 0, len(v.Definitions))
	for _, d := range v.Definitions {
		out = append(out, d.Spec)
	}
	return out
}

func TestPublishRejectsTheSubmitterAndAuditsTheDenial(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	maker := f.rc(f.tenantA, f.maker)
	_, planID := f.activePlan(t, "MAKER")

	version, err := f.svc.CreatePlanVersion(ctx, maker, planID, application.NewPlanVersionInput{ValidFrom: dayPtr(t, "2026-01-01")})
	if err != nil {
		t.Fatal(err)
	}
	version, err = f.svc.ReplaceDefinitions(ctx, maker, version.ID, sampleDefinitions(), version.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	version, err = f.svc.SubmitPlanVersion(ctx, maker, version.ID, nil, version.RowVersion)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.PublishPlanVersion(ctx, maker, version.ID, nil, version.RowVersion); !errors.Is(err, application.ErrMakerCheckerSame) {
		t.Fatalf("publish by the submitter = %v, want ErrMakerCheckerSame", err)
	}

	// The refusal is audited even though the transaction rolled back the state change.
	dbCtx, cancel := f.h.Ctx()
	defer cancel()
	var denials int
	if err := f.h.Admin.QueryRow(dbCtx, `
		SELECT count(*) FROM audit.event
		 WHERE tenant_id = $1 AND action_code = 'plan_version.publish'
		   AND outcome = 'DENIED' AND reason_code = 'MAKER_CHECKER_SAME_ACTOR'`, f.tenantA).Scan(&denials); err != nil {
		t.Fatal(err)
	}
	if denials != 1 {
		t.Fatalf("audited denials = %d, want 1", denials)
	}

	// The version is still under review and can be published by a different actor.
	if _, err := f.svc.PublishPlanVersion(ctx, f.rc(f.tenantA, f.checker), version.ID, nil, version.RowVersion); err != nil {
		t.Fatalf("publish by the checker: %v", err)
	}
}

func TestPublishedVersionsAreImmutableAndNonOverlapping(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	maker, checker := f.rc(f.tenantA, f.maker), f.rc(f.tenantA, f.checker)
	_, planID := f.activePlan(t, "OVERLAP")

	first := f.publishVersion(t, planID, "2026-01-01", "2027-01-01")

	// Editing a published version is refused.
	if _, err := f.svc.UpdatePlanVersion(ctx, maker, first.ID, application.PlanVersionPatch{
		Notes: strPtr("sonradan not"), ExpectedVersion: first.RowVersion,
	}); !errors.Is(err, application.ErrVersionImmutable) {
		t.Fatalf("editing a published version = %v, want ErrVersionImmutable", err)
	}
	if _, err := f.svc.ReplaceDefinitions(ctx, maker, first.ID, sampleDefinitions(), first.RowVersion); !errors.Is(err, application.ErrVersionImmutable) {
		t.Fatalf("replacing definitions of a published version = %v, want ErrVersionImmutable", err)
	}

	// A second version whose period overlaps the published one cannot be published.
	second, err := f.svc.CreatePlanVersion(ctx, maker, planID, application.NewPlanVersionInput{
		ValidFrom: dayPtr(t, "2026-06-01"), ValidTo: dayPtr(t, "2027-06-01"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.VersionNo != 2 {
		t.Fatalf("version number = %d, want 2", second.VersionNo)
	}
	second, err = f.svc.ReplaceDefinitions(ctx, maker, second.ID, sampleDefinitions(), second.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	second, err = f.svc.SubmitPlanVersion(ctx, maker, second.ID, nil, second.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PublishPlanVersion(ctx, checker, second.ID, nil, second.RowVersion); !errors.Is(err, application.ErrVersionOverlap) {
		t.Fatalf("overlapping publish = %v, want ErrVersionOverlap", err)
	}

	// Retiring the first frees the period, and the second can then be published.
	retired, err := f.svc.RetirePlanVersion(ctx, checker, first.ID, "SUPERSEDED", nil, first.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	if retired.Status != domain.VersionRetired {
		t.Fatalf("retired = %+v", retired)
	}
	current, err := f.svc.GetPlanVersion(ctx, maker, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PublishPlanVersion(ctx, checker, second.ID, nil, current.RowVersion); err != nil {
		t.Fatalf("publish after retire: %v", err)
	}
}

func TestCopyFromVersionCopiesDefinitions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	maker := f.rc(f.tenantA, f.maker)
	_, planID := f.activePlan(t, "COPY")

	origin := f.publishVersion(t, planID, "2026-01-01", "2027-01-01")
	copied, err := f.svc.CreatePlanVersion(ctx, maker, planID, application.NewPlanVersionInput{
		CopyFromVersionID: &origin.ID, ValidFrom: dayPtr(t, "2027-01-01"),
	})
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if len(copied.Definitions) != len(origin.Definitions) {
		t.Fatalf("copied %d definitions, want %d", len(copied.Definitions), len(origin.Definitions))
	}
	if copied.Definitions[0].ID == origin.Definitions[0].ID {
		t.Fatal("the copy must own new definition rows")
	}
}

func TestResolvePlanVersionAtPeriodBoundaries(t *testing.T) {
	f := newFixture(t)
	_, planID := f.activePlan(t, "RESOLVE")
	published := f.publishVersion(t, planID, "2026-01-01", "2027-01-01")

	ctx := context.Background()
	resolve := func(on string) (application.PlanVersionRow, error) {
		var row application.PlanVersionRow
		err := db.WithTenantTx(ctx, f.h.App, db.TenantContext{TenantID: f.tenantA, ActorID: f.maker},
			func(ctx context.Context, tx pgx.Tx) error {
				var err error
				row, err = application.ResolvePlanVersion(ctx, tx, f.tenantA, planID, day(t, on))
				return err
			})
		return row, err
	}

	for _, inside := range []string{"2026-01-01", "2026-06-15", "2026-12-31"} {
		row, err := resolve(inside)
		if err != nil {
			t.Fatalf("resolve %s: %v", inside, err)
		}
		if row.ID != published.ID {
			t.Fatalf("resolve %s returned %s, want %s", inside, row.ID, published.ID)
		}
	}
	for _, outside := range []string{"2025-12-31", "2027-01-01"} {
		if _, err := resolve(outside); !errors.Is(err, application.ErrNoPublishedVersion) {
			t.Fatalf("resolve %s = %v, want ErrNoPublishedVersion", outside, err)
		}
	}

	// A retired version no longer answers.
	if _, err := f.svc.RetirePlanVersion(ctx, f.rc(f.tenantA, f.checker), published.ID, "SUPERSEDED", nil, published.RowVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve("2026-06-15"); !errors.Is(err, application.ErrNoPublishedVersion) {
		t.Fatalf("resolve after retire = %v, want ErrNoPublishedVersion", err)
	}
}

func TestEnrollmentRequiresAPublishedVersionAndWritesTheOutboxEvent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA, f.maker)
	_, planID := f.activePlan(t, "ENROLL")

	// No published version yet: 422 PLAN_NOT_PUBLISHED on planId.
	_, err := f.svc.CreateEnrollment(ctx, rc, f.person, application.NewEnrollmentInput{
		SponsorMembershipID: f.membership, PlanID: planID, ValidFrom: day(t, "2026-03-01"),
	})
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || ve.Fields[0].Field != "planId" || ve.Fields[0].Code != "PLAN_NOT_PUBLISHED" {
		t.Fatalf("enrollment without a published version = %v", err)
	}

	f.publishVersion(t, planID, "2026-01-01", "2027-01-01")

	enrollment, err := f.svc.CreateEnrollment(ctx, rc, f.person, application.NewEnrollmentInput{
		SponsorMembershipID: f.membership, PlanID: planID, ValidFrom: day(t, "2026-03-01"),
		EnrollmentReason: strPtr("işe giriş"),
	})
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	if enrollment.Status != domain.EnrollmentActive || enrollment.PersonID != f.person {
		t.Fatalf("enrollment = %+v", enrollment)
	}

	// The outbox event committed with the enrollment; the payload carries ids only.
	dbCtx, cancel := f.h.Ctx()
	defer cancel()
	var payload string
	if err := f.h.Admin.QueryRow(dbCtx, `
		SELECT payload_json::text FROM system.outbox_event
		 WHERE tenant_id = $1 AND event_type = $2 AND aggregate_id = $3`,
		f.tenantA, application.EnrollmentCreatedEvent, enrollment.ID).Scan(&payload); err != nil {
		t.Fatalf("outbox event not written: %v", err)
	}
	for _, forbidden := range []string{"Zeynep", "Karaca", "işe giriş"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("outbox payload leaks %q: %s", forbidden, payload)
		}
	}

	// A second, overlapping enrollment of the same membership and plan is refused.
	if _, err := f.svc.CreateEnrollment(ctx, rc, f.person, application.NewEnrollmentInput{
		SponsorMembershipID: f.membership, PlanID: planID, ValidFrom: day(t, "2026-06-01"),
	}); !errors.Is(err, application.ErrEnrollmentOverlap) {
		t.Fatalf("overlapping enrollment = %v, want ErrEnrollmentOverlap", err)
	}

	// A date before the published period is refused as well.
	if _, err := f.svc.CreateEnrollment(ctx, rc, f.person, application.NewEnrollmentInput{
		SponsorMembershipID: f.membership, PlanID: planID, ValidFrom: day(t, "2025-06-01"),
	}); !errors.As(err, &ve) {
		t.Fatalf("enrollment before the published period = %v, want a validation error", err)
	}
}

func TestEnrollmentRollsBackTheOutboxEventOnFailure(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA, f.maker)
	_, planID := f.activePlan(t, "ROLLBACK")
	f.publishVersion(t, planID, "2026-01-01", "2027-01-01")

	if _, err := f.svc.CreateEnrollment(ctx, rc, f.person, application.NewEnrollmentInput{
		SponsorMembershipID: f.membership, PlanID: planID, ValidFrom: day(t, "2026-03-01"),
	}); err != nil {
		t.Fatal(err)
	}
	// The overlapping attempt fails after the outbox insert would have run.
	if _, err := f.svc.CreateEnrollment(ctx, rc, f.person, application.NewEnrollmentInput{
		SponsorMembershipID: f.membership, PlanID: planID, ValidFrom: day(t, "2026-04-01"),
	}); !errors.Is(err, application.ErrEnrollmentOverlap) {
		t.Fatalf("second enrollment = %v", err)
	}

	dbCtx, cancel := f.h.Ctx()
	defer cancel()
	var events int
	if err := f.h.Admin.QueryRow(dbCtx, `
		SELECT count(*) FROM system.outbox_event WHERE tenant_id = $1 AND event_type = $2`,
		f.tenantA, application.EnrollmentCreatedEvent).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("outbox events = %d, want 1 (the failed command must not leave one behind)", events)
	}
}

func TestEnrollmentValidatesMembershipOwnershipAndStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA, f.maker)
	_, planID := f.activePlan(t, "MEMBERCHK")
	f.publishVersion(t, planID, "2026-01-01", "2027-01-01")

	other, otherMembership := f.seedMember(f.tenantA, f.sponsorOrg, "2026-01-01")
	_ = other

	var ve *domain.ValidationError
	// The membership belongs to a different person.
	if _, err := f.svc.CreateEnrollment(ctx, rc, f.person, application.NewEnrollmentInput{
		SponsorMembershipID: otherMembership, PlanID: planID, ValidFrom: day(t, "2026-03-01"),
	}); !errors.As(err, &ve) || ve.Fields[0].Field != "sponsorMembershipId" {
		t.Fatalf("foreign membership = %v", err)
	}

	// The membership is not active on the start date.
	f.h.AdminExec(`UPDATE party.sponsor_membership SET status = 'SUSPENDED' WHERE id = $1`, f.membership)
	if _, err := f.svc.CreateEnrollment(ctx, rc, f.person, application.NewEnrollmentInput{
		SponsorMembershipID: f.membership, PlanID: planID, ValidFrom: day(t, "2026-03-01"),
	}); !errors.As(err, &ve) || ve.Fields[0].Code != "MEMBERSHIP_NOT_ACTIVE" {
		t.Fatalf("inactive membership = %v", err)
	}
}

func TestEnrollmentPatchAndListing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA, f.maker)
	_, planID := f.activePlan(t, "PATCH")
	f.publishVersion(t, planID, "2026-01-01", "2027-01-01")

	enrollment, err := f.svc.CreateEnrollment(ctx, rc, f.person, application.NewEnrollmentInput{
		SponsorMembershipID: f.membership, PlanID: planID, ValidFrom: day(t, "2026-03-01"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// A stale If-Match is refused.
	if _, err := f.svc.UpdateEnrollment(ctx, rc, enrollment.ID, application.EnrollmentPatch{
		Status: strPtr("SUSPENDED"), ExpectedVersion: enrollment.RowVersion + 5,
	}); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("stale If-Match = %v", err)
	}

	suspended, err := f.svc.UpdateEnrollment(ctx, rc, enrollment.ID, application.EnrollmentPatch{
		Status: strPtr("SUSPENDED"), ValidTo: dayPtr(t, "2026-12-01"), ExpectedVersion: enrollment.RowVersion,
	})
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if suspended.Status != domain.EnrollmentSuspended || suspended.ValidTo == nil {
		t.Fatalf("suspended = %+v", suspended)
	}

	ended, err := f.svc.UpdateEnrollment(ctx, rc, enrollment.ID, application.EnrollmentPatch{
		Status: strPtr("ENDED"), ExpectedVersion: suspended.RowVersion,
	})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	// ENDED is terminal.
	if _, err := f.svc.UpdateEnrollment(ctx, rc, enrollment.ID, application.EnrollmentPatch{
		Status: strPtr("ACTIVE"), ExpectedVersion: ended.RowVersion,
	}); !errors.Is(err, application.ErrEnrollmentTransition) {
		t.Fatalf("reviving an ended enrollment = %v", err)
	}

	byPerson, err := f.svc.ListPersonEnrollments(ctx, rc, f.person)
	if err != nil || len(byPerson) != 1 {
		t.Fatalf("person enrollments = %d (%v)", len(byPerson), err)
	}
	page, err := f.svc.ListEnrollments(ctx, rc, application.EnrollmentFilter{PlanID: planID, Status: "ENDED"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("filtered enrollments = %+v (%v)", page.Items, err)
	}
	empty, err := f.svc.ListEnrollments(ctx, rc, application.EnrollmentFilter{Status: "ACTIVE"})
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("ACTIVE filter should be empty: %+v (%v)", empty.Items, err)
	}
	if _, err := f.svc.ListPersonEnrollments(ctx, rc, uuid.New()); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("unknown person = %v, want ErrNotFound", err)
	}
}

func TestProgramCrudCodeUniquenessAndPaging(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA, f.maker)

	first, err := f.svc.CreateProgram(ctx, rc, application.NewProgramInput{
		Code: "EMP2026", Name: "Çalışan Programı", ProgramType: "EMPLOYEE_BENEFIT",
		SponsorOrganizationID: f.sponsorOrg, PayerOrganizationID: f.payerOrg,
		ValidFrom: dayPtr(t, "2026-01-01"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if first.Status != domain.ProgramDraft || first.SponsorDisplayName == "" || first.PayerDisplayName == "" {
		t.Fatalf("program = %+v", first)
	}

	if _, err := f.svc.CreateProgram(ctx, rc, application.NewProgramInput{
		Code: "EMP2026", Name: "Aynı kod", ProgramType: "EMPLOYEE_BENEFIT",
		SponsorOrganizationID: f.sponsorOrg, PayerOrganizationID: f.payerOrg,
	}); !errors.Is(err, application.ErrProgramCodeTaken) {
		t.Fatalf("duplicate code = %v, want ErrProgramCodeTaken", err)
	}

	// The sponsor and the payer must carry the right roles.
	if _, err := f.svc.CreateProgram(ctx, rc, application.NewProgramInput{
		Code: "ROLES", Name: "Rol testi", ProgramType: "EMPLOYEE_BENEFIT",
		SponsorOrganizationID: f.payerOrg, PayerOrganizationID: f.sponsorOrg,
	}); err == nil {
		t.Fatal("swapped sponsor and payer roles must be refused")
	}

	// DRAFT -> CLOSED is not a documented transition.
	if _, err := f.svc.UpdateProgram(ctx, rc, first.ID, application.ProgramPatch{
		Status: strPtr("CLOSED"), ExpectedVersion: first.RowVersion,
	}); !errors.Is(err, application.ErrProgramTransition) {
		t.Fatalf("DRAFT->CLOSED = %v, want ErrProgramTransition", err)
	}

	active, err := f.svc.UpdateProgram(ctx, rc, first.ID, application.ProgramPatch{
		Status: strPtr("ACTIVE"), Name: strPtr("Çalışan Programı 2026"), ExpectedVersion: first.RowVersion,
	})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if active.Status != domain.ProgramActive || active.RowVersion <= first.RowVersion {
		t.Fatalf("activated = %+v", active)
	}
	if _, err := f.svc.UpdateProgram(ctx, rc, first.ID, application.ProgramPatch{
		Name: strPtr("stale"), ExpectedVersion: first.RowVersion,
	}); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("stale If-Match = %v", err)
	}

	for i := range 3 {
		if _, err := f.svc.CreateProgram(ctx, rc, application.NewProgramInput{
			Code: "PAGE" + string(rune('A'+i)), Name: "Sayfa", ProgramType: "MEMBER_PROGRAM",
			SponsorOrganizationID: f.sponsorOrg, PayerOrganizationID: f.payerOrg,
		}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.svc.ListPrograms(ctx, rc, application.ProgramFilter{Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("first page = %d items, cursor %q (%v)", len(page.Items), page.NextCursor, err)
	}
	next, err := f.svc.ListPrograms(ctx, rc, application.ProgramFilter{Limit: 2, Cursor: page.NextCursor})
	if err != nil || len(next.Items) != 2 {
		t.Fatalf("second page = %d items (%v)", len(next.Items), err)
	}
	if next.Items[0].ID == page.Items[0].ID {
		t.Fatal("the second page repeats the first")
	}
	filtered, err := f.svc.ListPrograms(ctx, rc, application.ProgramFilter{Status: "ACTIVE"})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != first.ID {
		t.Fatalf("status filter = %+v (%v)", filtered.Items, err)
	}
	searched, err := f.svc.ListPrograms(ctx, rc, application.ProgramFilter{Query: "EMP20"})
	if err != nil || len(searched.Items) != 1 {
		t.Fatalf("search = %+v (%v)", searched.Items, err)
	}
}

func TestPlanCrudAndCodeUniqueness(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA, f.maker)
	programID, planID := f.activePlan(t, "PLANS")

	if _, err := f.svc.CreatePlan(ctx, rc, programID, application.NewPlanInput{Code: "PLANS-P1", Name: "Aynı kod"}); !errors.Is(err, application.ErrPlanCodeTaken) {
		t.Fatalf("duplicate plan code = %v, want ErrPlanCodeTaken", err)
	}
	plans, err := f.svc.ListPlans(ctx, rc, programID)
	if err != nil || len(plans) != 1 || plans[0].ID != planID {
		t.Fatalf("plans = %+v (%v)", plans, err)
	}
	plan, err := f.svc.GetPlan(ctx, rc, planID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdatePlan(ctx, rc, planID, application.PlanPatch{
		Status: strPtr("ACTIVE"), ExpectedVersion: plan.RowVersion,
	}); err != nil {
		t.Fatalf("ACTIVE -> ACTIVE must be a no-op: %v", err)
	}
	plan, err = f.svc.GetPlan(ctx, rc, planID)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := f.svc.UpdatePlan(ctx, rc, planID, application.PlanPatch{
		Status: strPtr("RETIRED"), ExpectedVersion: plan.RowVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdatePlan(ctx, rc, planID, application.PlanPatch{
		Status: strPtr("ACTIVE"), ExpectedVersion: retired.RowVersion,
	}); !errors.Is(err, application.ErrPlanTransition) {
		t.Fatalf("RETIRED -> ACTIVE = %v, want ErrPlanTransition", err)
	}
	if _, err := f.svc.GetPlan(ctx, rc, uuid.New()); !errors.Is(err, application.ErrPlanNotFound) {
		t.Fatalf("unknown plan = %v", err)
	}
}

func TestAnotherTenantSeesNothing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	programID, planID := f.activePlan(t, "ISOLATE")
	version := f.publishVersion(t, planID, "2026-01-01", "2027-01-01")
	other := f.rc(f.tenantB, f.maker)

	if _, err := f.svc.GetProgram(ctx, other, programID); !errors.Is(err, application.ErrProgramNotFound) {
		t.Fatalf("cross-tenant program read = %v", err)
	}
	if _, err := f.svc.GetPlan(ctx, other, planID); !errors.Is(err, application.ErrPlanNotFound) {
		t.Fatalf("cross-tenant plan read = %v", err)
	}
	if _, err := f.svc.GetPlanVersion(ctx, other, version.ID); !errors.Is(err, application.ErrPlanVersionNotFound) {
		t.Fatalf("cross-tenant version read = %v", err)
	}
	page, err := f.svc.ListPrograms(ctx, other, application.ProgramFilter{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("cross-tenant list = %+v (%v)", page.Items, err)
	}
}
