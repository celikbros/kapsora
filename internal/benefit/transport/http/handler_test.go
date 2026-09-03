package benefithttp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/benefit/application"
	benefitpg "github.com/celikbros/kapsora/internal/benefit/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	benefithttp "github.com/celikbros/kapsora/internal/benefit/transport/http"
	"github.com/celikbros/kapsora/internal/identity"
	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Test headers letting each request choose its actor, permissions and step-up state.
const (
	permsHeader  = "X-Test-Permissions"
	stepUpHeader = "X-Test-StepUp"
	actorHeader  = "X-Test-Actor"

	allPermissions = "program.read,program.manage,plan.manage,plan.publish,enrollment.manage," +
		"entitlement.read,entitlement.adjust"
	patchType = "application/merge-patch+json"
)

type denyRecorder struct{ permissions []string }

func (d *denyRecorder) Deny(w http.ResponseWriter, r *http.Request, err error, permission string) {
	d.permissions = append(d.permissions, permission)
	identityhttp.WriteAuthError(w, r, err, nil)
}

type server struct {
	h            *dbtest.Harness
	entitlements *ledger.Service
	handler      http.Handler
	denied       *denyRecorder
	logs         *bytes.Buffer
	tenant       uuid.UUID
	maker        uuid.UUID
	checker      uuid.UUID
	sponsorOrg   uuid.UUID
	payerOrg     uuid.UUID
	person       uuid.UUID
	membership   uuid.UUID
}

func newServer(t *testing.T) *server {
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
	entitlements, err := ledger.New(ledger.Deps{Pool: h.App, Audit: auditpg.New(), Cursors: cursors})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{
		h: h, entitlements: entitlements, tenant: h.CreateTenant("HTTP_BENEFIT"),
		maker:   h.CreateActor("benefit-http-maker", "Maker"),
		checker: h.CreateActor("benefit-http-checker", "Checker"),
	}
	for _, e := range identityapp.DefaultBaselineCatalogs().ProgramTypes {
		h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name) VALUES ($1, $2, $3)`,
			s.tenant, e.Code, e.DisplayName)
	}
	s.sponsorOrg = h.CreateTenantOrganization(s.tenant, "HTTP Sponsor", "SPONSOR")
	s.payerOrg = h.CreateTenantOrganization(s.tenant, "HTTP Payer", "PAYER")
	s.seedMember(t)

	s.logs = &bytes.Buffer{}
	s.denied = &denyRecorder{}
	logger := slog.New(slog.NewJSONHandler(s.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := benefithttp.NewHandler(svc, s.denied, logger)
	entitlementHandler := benefithttp.NewEntitlementHandler(entitlements, s.denied, logger)

	// Stand-in for RequireTenantContext: actor, permissions and step-up come from headers.
	fakeContext := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := s.maker
			if r.Header.Get(actorHeader) == "checker" {
				actor = s.checker
			}
			rc := identity.RequestContext{
				TenantID: s.tenant, MembershipID: uuid.New(),
				Principal:   identity.Principal{ActorID: actor},
				Permissions: map[string]struct{}{},
				StepUpValid: r.Header.Get(stepUpHeader) == "1",
			}
			for _, p := range strings.Split(r.Header.Get(permsHeader), ",") {
				if p != "" {
					rc.Permissions[p] = struct{}{}
				}
			}
			next.ServeHTTP(w, r.WithContext(identity.WithRequestContext(r.Context(), rc)))
		})
	}

	r := chi.NewRouter()
	mount := func(pattern string, register func(chi.Router)) {
		r.Route(pattern, func(rr chi.Router) {
			rr.Use(fakeContext)
			register(rr)
		})
	}
	mw := benefithttp.Middlewares{}
	mount("/api/v1/programs", func(rr chi.Router) { handler.ProgramRoutes(rr, mw) })
	mount("/api/v1/plans", func(rr chi.Router) { handler.PlanRoutes(rr, mw) })
	mount("/api/v1/plan-versions", handler.PlanVersionRoutes)
	mount("/api/v1/enrollments", handler.EnrollmentRoutes)
	mount("/api/v1/people", func(rr chi.Router) {
		handler.PersonRoutes(rr, mw)
		entitlementHandler.PersonRoutes(rr)
	})
	mount("/api/v1/entitlement-accounts", func(rr chi.Router) {
		entitlementHandler.AccountRoutes(rr, benefithttp.EntitlementMiddlewares{})
	})
	mount("/api/v1/entitlement-adjustments", entitlementHandler.AdjustmentRoutes)
	s.handler = r
	return s
}

func (s *server) seedMember(t *testing.T) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Zeynep', 'Karaca', 'zeynep karaca') RETURNING id`, s.tenant).Scan(&s.person); err != nil {
		t.Fatalf("seed person: %v", err)
	}
	s.h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, s.tenant)
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, s.person, s.sponsorOrg).Scan(&s.membership); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

type call struct {
	method, path, body, contentType, ifMatch, perms, actor string
	stepUp                                                 bool
}

func (s *server) do(c call) *httptest.ResponseRecorder {
	var body io.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
	}
	req := httptest.NewRequest(c.method, c.path, body)
	if c.contentType == "" {
		c.contentType = "application/json"
	}
	req.Header.Set("Content-Type", c.contentType)
	if c.ifMatch != "" {
		req.Header.Set("If-Match", c.ifMatch)
	}
	if c.perms == "" {
		c.perms = allPermissions
	}
	req.Header.Set(permsHeader, c.perms)
	if c.actor != "" {
		req.Header.Set(actorHeader, c.actor)
	}
	if c.stepUp {
		req.Header.Set(stepUpHeader, "1")
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) kapsorav1.Problem {
	t.Helper()
	var p kapsorav1.Problem
	decode(t, rec, &p)
	return p
}

// activePlan drives the HTTP surface up to an ACTIVE plan and returns its id and ETag.
func (s *server) activePlan(t *testing.T, code string) (planID uuid.UUID, etag string) {
	t.Helper()
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/programs", body: fmt.Sprintf(
		`{"code":%q,"name":"Program","programType":"EMPLOYEE_BENEFIT","sponsorOrganizationId":%q,"payerOrganizationId":%q,"validFrom":"2026-01-01"}`,
		code, s.sponsorOrg, s.payerOrg)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create program: %d %s", rec.Code, rec.Body.String())
	}
	var program kapsorav1.Program
	decode(t, rec, &program)

	rec = s.do(call{method: http.MethodPatch, path: "/api/v1/programs/" + program.Id.String(),
		contentType: patchType, ifMatch: rec.Header().Get("ETag"), body: `{"status":"ACTIVE"}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("activate program: %d %s", rec.Code, rec.Body.String())
	}

	rec = s.do(call{method: http.MethodPost, path: "/api/v1/programs/" + program.Id.String() + "/plans",
		body: fmt.Sprintf(`{"code":%q,"name":"Plan"}`, code+"-P")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create plan: %d %s", rec.Code, rec.Body.String())
	}
	var plan kapsorav1.Plan
	decode(t, rec, &plan)

	rec = s.do(call{method: http.MethodPatch, path: "/api/v1/plans/" + plan.Id.String(),
		contentType: patchType, ifMatch: rec.Header().Get("ETag"), body: `{"status":"ACTIVE"}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("activate plan: %d %s", rec.Code, rec.Body.String())
	}
	return plan.Id, rec.Header().Get("ETag")
}

// planVersion is the response shape of the plan version endpoints (the contract's
// PlanVersion; quantities arrive as JSON numbers and are read back as raw text).
type planVersion struct {
	Id                uuid.UUID `json:"id"`
	PlanId            uuid.UUID `json:"planId"`
	VersionNo         int       `json:"versionNo"`
	Status            string    `json:"status"`
	ValidFrom         *string   `json:"validFrom"`
	ValidTo           *string   `json:"validTo"`
	RowVersion        int64     `json:"rowVersion"`
	ConfigurationHash *string   `json:"configurationHash"`
	SubmittedBy       *string   `json:"submittedBy"`
	PublishedBy       *string   `json:"publishedBy"`
	RetireReasonCode  *string   `json:"retireReasonCode"`
	Definitions       []struct {
		Id              uuid.UUID    `json:"id"`
		Code            string       `json:"code"`
		InitialQuantity json.Number  `json:"initialQuantity"`
		CurrencyCode    *string      `json:"currencyCode"`
		RolloverCap     *json.Number `json:"rolloverCap"`
	} `json:"definitions"`
}

const definitionsBody = `{"items":[
  {"code":"DENTAL","name":"Diş","unitType":"MONEY","currencyCode":"TRY","periodType":"CALENDAR_YEAR","initialQuantity":1500.5},
  {"code":"CHECKUP","name":"Kontrol","unitType":"COUNT","periodType":"LIFETIME","initialQuantity":2}
]}`

func TestMakerCheckerPublishThroughHTTP(t *testing.T) {
	s := newServer(t)
	planID, _ := s.activePlan(t, "HTTPMC")

	rec := s.do(call{method: http.MethodPost, path: "/api/v1/plans/" + planID.String() + "/versions",
		body: `{"validFrom":"2026-01-01","validTo":"2027-01-01"}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create version: %d %s", rec.Code, rec.Body.String())
	}
	var version planVersion
	decode(t, rec, &version)
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("the draft has no ETag")
	}

	// The definitions PUT needs If-Match and moves the ETag.
	rec = s.do(call{method: http.MethodPut,
		path: "/api/v1/plan-versions/" + version.Id.String() + "/entitlement-definitions", body: definitionsBody})
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match = %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodPut, ifMatch: etag,
		path: "/api/v1/plan-versions/" + version.Id.String() + "/entitlement-definitions", body: definitionsBody})
	if rec.Code != http.StatusOK {
		t.Fatalf("replace definitions: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec, &version)
	if rec.Header().Get("ETag") == etag {
		t.Fatal("replacing the definitions must move the version ETag")
	}
	etag = rec.Header().Get("ETag")
	if len(version.Definitions) != 2 || version.Definitions[1].InitialQuantity.String() != "1500.5" {
		t.Fatalf("definitions = %+v", version.Definitions)
	}

	rec = s.do(call{method: http.MethodPost, ifMatch: etag,
		path: "/api/v1/plan-versions/" + version.Id.String() + "/submit", body: `{"comment":"hazır"}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec, &version)
	etag = rec.Header().Get("ETag")

	// Publish without step-up is refused before the maker-checker rule is reached.
	rec = s.do(call{method: http.MethodPost, ifMatch: etag, actor: "checker",
		path: "/api/v1/plan-versions/" + version.Id.String() + "/publish"})
	if rec.Code != http.StatusForbidden || problemOf(t, rec).Code != "STEP_UP_REQUIRED" {
		t.Fatalf("publish without step-up = %d %s", rec.Code, rec.Body.String())
	}

	// Publish without plan.publish is denied and audited by the denier.
	rec = s.do(call{method: http.MethodPost, ifMatch: etag, actor: "checker", stepUp: true,
		perms: "program.read,plan.manage", path: "/api/v1/plan-versions/" + version.Id.String() + "/publish"})
	if rec.Code != http.StatusForbidden || problemOf(t, rec).Code != "PERMISSION_DENIED" {
		t.Fatalf("publish without plan.publish = %d %s", rec.Code, rec.Body.String())
	}
	if len(s.denied.permissions) == 0 || s.denied.permissions[len(s.denied.permissions)-1] != "plan.publish" {
		t.Fatalf("denials = %v", s.denied.permissions)
	}

	// The submitter may not publish: 403 MAKER_CHECKER_SAME_ACTOR, audited.
	rec = s.do(call{method: http.MethodPost, ifMatch: etag, stepUp: true,
		path: "/api/v1/plan-versions/" + version.Id.String() + "/publish"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("self-publish = %d %s", rec.Code, rec.Body.String())
	}
	if code := problemOf(t, rec).Code; code != "MAKER_CHECKER_SAME_ACTOR" {
		t.Fatalf("self-publish problem code = %q", code)
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var denials int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM audit.event
		 WHERE tenant_id = $1 AND action_code = 'plan_version.publish' AND outcome = 'DENIED'`,
		s.tenant).Scan(&denials); err != nil {
		t.Fatal(err)
	}
	if denials != 1 {
		t.Fatalf("audited denials = %d, want 1", denials)
	}

	// The checker publishes with step-up.
	rec = s.do(call{method: http.MethodPost, ifMatch: etag, actor: "checker", stepUp: true,
		path: "/api/v1/plan-versions/" + version.Id.String() + "/publish", body: `{"comment":"onay"}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec, &version)
	if version.Status != "PUBLISHED" || version.ConfigurationHash == nil || len(*version.ConfigurationHash) != 64 {
		t.Fatalf("published = %+v", version)
	}
	etag = rec.Header().Get("ETag")

	// A published version is immutable.
	rec = s.do(call{method: http.MethodPatch, contentType: patchType, ifMatch: etag,
		path: "/api/v1/plan-versions/" + version.Id.String(), body: `{"notes":"sonradan"}`})
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != "PLAN_VERSION_IMMUTABLE" {
		t.Fatalf("patching a published version = %d %s", rec.Code, rec.Body.String())
	}

	// Retire needs step-up too and records the reason.
	rec = s.do(call{method: http.MethodPost, ifMatch: etag, actor: "checker",
		path: "/api/v1/plan-versions/" + version.Id.String() + "/retire", body: `{"reasonCode":"SUPERSEDED"}`})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("retire without step-up = %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodPost, ifMatch: etag, actor: "checker", stepUp: true,
		path: "/api/v1/plan-versions/" + version.Id.String() + "/retire", body: `{"reasonCode":"SUPERSEDED"}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("retire: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec, &version)
	if version.Status != "RETIRED" || version.RetireReasonCode == nil || *version.RetireReasonCode != "SUPERSEDED" {
		t.Fatalf("retired = %+v", version)
	}
}

func TestValidationPayloads(t *testing.T) {
	s := newServer(t)

	// Program code and name are validated before anything touches the database.
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/programs", body: fmt.Sprintf(
		`{"code":"lower case","name":"x","programType":"EMPLOYEE_BENEFIT","sponsorOrganizationId":%q,"payerOrganizationId":%q}`,
		s.sponsorOrg, s.payerOrg)})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid program = %d %s", rec.Code, rec.Body.String())
	}
	p := problemOf(t, rec)
	if p.Code != "VALIDATION_FAILED" || p.Errors == nil {
		t.Fatalf("problem = %+v", p)
	}
	fields := map[string]string{}
	for _, e := range *p.Errors {
		fields[e.Field] = e.Code
	}
	if fields["code"] != "FORMAT" || fields["name"] != "LENGTH" {
		t.Fatalf("field errors = %+v", fields)
	}

	// An unknown sponsor organization is a field error, not a 500.
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/programs", body: fmt.Sprintf(
		`{"code":"ORGTEST","name":"Program","programType":"EMPLOYEE_BENEFIT","sponsorOrganizationId":%q,"payerOrganizationId":%q}`,
		uuid.New(), s.payerOrg)})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown sponsor = %d %s", rec.Code, rec.Body.String())
	}

	planID, planETag := s.activePlan(t, "VALID")

	// Entitlement definitions surface the database CHECKs as field errors.
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/plans/" + planID.String() + "/versions",
		body: `{"validFrom":"2026-01-01"}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create version: %d %s", rec.Code, rec.Body.String())
	}
	var version planVersion
	decode(t, rec, &version)
	rec = s.do(call{method: http.MethodPut, ifMatch: rec.Header().Get("ETag"),
		path: "/api/v1/plan-versions/" + version.Id.String() + "/entitlement-definitions",
		body: `{"items":[{"code":"DENTAL","name":"Diş","unitType":"MONEY","periodType":"ROLLING_DAYS","initialQuantity":10}]}`})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid definitions = %d %s", rec.Code, rec.Body.String())
	}
	p = problemOf(t, rec)
	fields = map[string]string{}
	for _, e := range *p.Errors {
		fields[e.Field] = e.Code
	}
	if fields["items[0].currencyCode"] != "REQUIRED" || fields["items[0].periodLength"] != "REQUIRED" {
		t.Fatalf("definition field errors = %+v", fields)
	}

	// An unknown plan is 404, never 500, and a stale If-Match is 412.
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/plans/" + uuid.New().String()})
	if rec.Code != http.StatusNotFound || problemOf(t, rec).Code != "PLAN_NOT_FOUND" {
		t.Fatalf("unknown plan = %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodPatch, contentType: patchType, ifMatch: `"999"`,
		path: "/api/v1/plans/" + planID.String(), body: `{"name":"Yeni ad"}`})
	if rec.Code != http.StatusPreconditionFailed || problemOf(t, rec).Code != "ETAG_MISMATCH" {
		t.Fatalf("stale If-Match = %d %s", rec.Code, rec.Body.String())
	}
	// A plain JSON content type is refused on a merge-patch endpoint.
	rec = s.do(call{method: http.MethodPatch, ifMatch: planETag,
		path: "/api/v1/plans/" + planID.String(), body: `{"name":"Yeni ad"}`})
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content type = %d %s", rec.Code, rec.Body.String())
	}
	// Unknown merge-patch fields are reported, not ignored.
	rec = s.do(call{method: http.MethodPatch, contentType: patchType, ifMatch: planETag,
		path: "/api/v1/plans/" + planID.String(), body: `{"code":"NEWCODE"}`})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown patch field = %d %s", rec.Code, rec.Body.String())
	}
}

func TestReadEndpointsRequireProgramRead(t *testing.T) {
	s := newServer(t)
	planID, _ := s.activePlan(t, "PERMS")

	for _, path := range []string{
		"/api/v1/programs",
		"/api/v1/plans/" + planID.String(),
		"/api/v1/plans/" + planID.String() + "/versions",
		"/api/v1/enrollments",
		"/api/v1/people/" + s.person.String() + "/enrollments",
	} {
		rec := s.do(call{method: http.MethodGet, path: path, perms: "member.read"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s without program.read = %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/people/" + s.person.String() + "/enrollments",
		perms: "program.read", body: `{"sponsorMembershipId":"` + s.membership.String() + `","planId":"` + planID.String() + `","validFrom":"2026-03-01"}`})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("enrollment without enrollment.manage = %d %s", rec.Code, rec.Body.String())
	}
}

func TestEnrollmentThroughHTTP(t *testing.T) {
	s := newServer(t)
	planID, _ := s.activePlan(t, "HTTPENR")

	// Without a published version the create answers 422 PLAN_NOT_PUBLISHED on planId.
	body := fmt.Sprintf(`{"sponsorMembershipId":%q,"planId":%q,"validFrom":"2026-03-01"}`, s.membership, planID)
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/people/" + s.person.String() + "/enrollments", body: body})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("enrollment without a published plan = %d %s", rec.Code, rec.Body.String())
	}
	p := problemOf(t, rec)
	if p.Errors == nil || (*p.Errors)[0].Field != "planId" || (*p.Errors)[0].Code != "PLAN_NOT_PUBLISHED" {
		t.Fatalf("problem = %+v", p)
	}

	s.publish(t, planID)

	rec = s.do(call{method: http.MethodPost, path: "/api/v1/people/" + s.person.String() + "/enrollments", body: body})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create enrollment: %d %s", rec.Code, rec.Body.String())
	}
	var enrollment kapsorav1.Enrollment
	decode(t, rec, &enrollment)
	etag := rec.Header().Get("ETag")
	if enrollment.PlanCode == "" || enrollment.PersonId != s.person {
		t.Fatalf("enrollment = %+v", enrollment)
	}

	// The overlap is reported as 409 ENROLLMENT_OVERLAP.
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/people/" + s.person.String() + "/enrollments",
		body: fmt.Sprintf(`{"sponsorMembershipId":%q,"planId":%q,"validFrom":"2026-06-01"}`, s.membership, planID)})
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != "ENROLLMENT_OVERLAP" {
		t.Fatalf("overlap = %d %s", rec.Code, rec.Body.String())
	}

	rec = s.do(call{method: http.MethodPatch, contentType: patchType, ifMatch: etag,
		path: "/api/v1/enrollments/" + enrollment.Id.String(), body: `{"status":"SUSPENDED","validTo":"2026-12-01"}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch enrollment: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec, &enrollment)
	if enrollment.Status != kapsorav1.EnrollmentStatusSUSPENDED || enrollment.ValidTo == nil {
		t.Fatalf("patched = %+v", enrollment)
	}

	rec = s.do(call{method: http.MethodGet, path: "/api/v1/enrollments?planId=" + planID.String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var page kapsorav1.EnrollmentPage
	decode(t, rec, &page)
	if len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/enrollments?cursor=not-a-cursor"})
	if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "CURSOR_INVALID" {
		t.Fatalf("bad cursor = %d %s", rec.Code, rec.Body.String())
	}
}

// publish drives a plan version from draft to PUBLISHED over HTTP.
func (s *server) publish(t *testing.T, planID uuid.UUID) planVersion {
	t.Helper()
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/plans/" + planID.String() + "/versions",
		body: `{"validFrom":"2026-01-01","validTo":"2027-01-01"}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create version: %d %s", rec.Code, rec.Body.String())
	}
	var version planVersion
	decode(t, rec, &version)
	rec = s.do(call{method: http.MethodPut, ifMatch: rec.Header().Get("ETag"),
		path: "/api/v1/plan-versions/" + version.Id.String() + "/entitlement-definitions", body: definitionsBody})
	if rec.Code != http.StatusOK {
		t.Fatalf("definitions: %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodPost, ifMatch: rec.Header().Get("ETag"),
		path: "/api/v1/plan-versions/" + version.Id.String() + "/submit"})
	if rec.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodPost, ifMatch: rec.Header().Get("ETag"), actor: "checker", stepUp: true,
		path: "/api/v1/plan-versions/" + version.Id.String() + "/publish"})
	if rec.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec, &version)
	return version
}
