package servicerequesthttp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	rulesapp "github.com/celikbros/kapsora/internal/rules/application"
	"github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestpg "github.com/celikbros/kapsora/internal/servicerequest/infrastructure/postgres"
	servicerequesthttp "github.com/celikbros/kapsora/internal/servicerequest/transport/http"
)

// Test headers let each request choose its permissions and its provider scope, which is
// what a review flow and a provider boundary need from an HTTP test.
const (
	permsHeader    = "X-Test-Permissions"
	scopeHeader    = "X-Test-Scope"
	allPermissions = "service_request.read,service_request.create,service_request.submit," +
		"service_request.review,service_request.cancel"
	readOnly  = "service_request.read"
	patchType = "application/merge-patch+json"
)

const entitlementCode = "PHYSIO"

var serviceDate = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

type denyRecorder struct{ permissions []string }

func (d *denyRecorder) Deny(w http.ResponseWriter, r *http.Request, err error, permission string) {
	d.permissions = append(d.permissions, permission)
	identityhttp.WriteAuthError(w, r, err, nil)
}

type server struct {
	h       *dbtest.Harness
	handler http.Handler
	denied  *denyRecorder

	tenant     uuid.UUID
	actor      uuid.UUID
	providerOr uuid.UUID
	otherOr    uuid.UUID
	person     uuid.UUID
	program    uuid.UUID
	enrollment uuid.UUID
	definition uuid.UUID
}

func newServer(t *testing.T) *server {
	t.Helper()
	h := dbtest.New(t)
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: servicerequestpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Programs: rulesapp.NewProgramCache(8),
		Now:      func() time.Time { return time.Date(2026, 6, 15, 9, 30, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{h: h, denied: &denyRecorder{}}
	s.seed(t)

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := servicerequesthttp.NewHandler(svc, s.denied, logger)

	// Stand-in for RequireTenantContext: the permissions and the provider scope come from
	// test headers, and the step-up window is always fresh.
	fakeContext := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := identity.RequestContext{
				TenantID: s.tenant, MembershipID: uuid.New(),
				Principal:   identity.Principal{ActorID: s.actor},
				StepUpValid: true,
				Permissions: map[string]struct{}{},
			}
			for _, p := range strings.Split(r.Header.Get(permsHeader), ",") {
				if p != "" {
					rc.Permissions[p] = struct{}{}
				}
			}
			if scope := r.Header.Get(scopeHeader); scope != "" {
				id, err := uuid.Parse(scope)
				if err != nil {
					t.Fatalf("bad test scope header %q", scope)
				}
				rc.Scopes = []identity.Scope{{
					Type: application.ScopeOrganization, ID: uuid.NullUUID{UUID: id, Valid: true},
				}}
			}
			next.ServeHTTP(w, r.WithContext(identity.WithRequestContext(r.Context(), rc)))
		})
	}

	r := chi.NewRouter()
	r.Route("/api/v1", func(api chi.Router) {
		api.Use(fakeContext)
		api.Route("/service-requests", func(rr chi.Router) {
			handler.Routes(rr, servicerequesthttp.Middlewares{})
		})
	})
	s.handler = r
	return s
}

func (s *server) seed(t *testing.T) { //nolint:funlen // one linear fixture reads better whole
	t.Helper()
	h := s.h
	ctx, cancel := h.Ctx()
	defer cancel()
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	s.tenant = h.CreateTenant("HTTP_REQUESTS")
	s.actor = h.CreateActor("request-http-clerk", "Request Clerk")
	sponsor := h.CreateTenantOrganization(s.tenant, "HTTP Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(s.tenant, "HTTP Payer", "PAYER")
	s.providerOr = h.CreateTenantOrganization(s.tenant, "HTTP Provider", "PROVIDER")
	s.otherOr = h.CreateTenantOrganization(s.tenant, "HTTP Other Provider", "PROVIDER")

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
	var planID, planVersion, entitlement uuid.UUID
	scan(&planID, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, s.tenant, s.program)
	scan(&planVersion, "plan version", `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period,
		                                  published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', daterange('2026-01-01','2027-01-01','[)'), clock_timestamp(), $3)
		RETURNING id`, s.tenant, planID, s.actor)
	scan(&entitlement, "entitlement definition", `
		INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name, unit_type,
		                                            period_type, initial_quantity)
		VALUES ($1, $2, $3, $3, 'SESSION', 'CALENDAR_YEAR', 300) RETURNING id`,
		s.tenant, planVersion, entitlementCode)
	scan(&s.enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, membership, planID)
	var account uuid.UUID
	scan(&account, "entitlement account", `
		INSERT INTO benefit.entitlement_account (tenant_id, enrollment_id, entitlement_definition_id,
		                                         benefit_period, total_granted, available_quantity)
		VALUES ($1, $2, $3, daterange('2026-01-01','2027-01-01','[)'), 300, 300) RETURNING id`,
		s.tenant, s.enrollment, entitlement)
	h.AdminExec(`
		INSERT INTO benefit.entitlement_ledger (tenant_id, entitlement_account_id, movement_type,
		                                        effective_at, delta_total, delta_available,
		                                        reference_type, reference_id, idempotency_key)
		VALUES ($1, $2, 'GRANT', clock_timestamp(), 300, 300, 'ENROLLMENT', $3, 'grant:seed')`,
		s.tenant, account, s.enrollment)

	var category uuid.UUID
	scan(&category, "service category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, s.tenant)
	scan(&s.definition, "service definition", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type, requires_provider)
		VALUES ($1, $2, $3, 'Fizyoterapi seansı', 'SESSION', 'SESSION', false) RETURNING id`,
		s.tenant, category, entitlementCode)
}

type response struct {
	code int
	etag string
	body map[string]any
}

func (s *server) do(t *testing.T, method, path, perms, body string, headers map[string]string) response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(permsHeader, perms)
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)

	out := response{code: rec.Code, etag: rec.Header().Get("ETag")}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out.body); err != nil {
			t.Fatalf("%s %s: decode body %q: %v", method, path, rec.Body.String(), err)
		}
	}
	return out
}

func ifMatch(etag string) map[string]string { return map[string]string{"If-Match": etag} }

func problemCode(r response) string {
	code, _ := r.body["code"].(string)
	return code
}

func firstFieldError(t *testing.T, r response) (field, code string) {
	t.Helper()
	list, ok := r.body["errors"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("expected field errors, got %v", r.body)
	}
	first, _ := list[0].(map[string]any)
	field, _ = first["field"].(string)
	code, _ = first["code"].(string)
	return field, code
}

func id(t *testing.T, r response) string {
	t.Helper()
	value, _ := r.body["id"].(string)
	if value == "" {
		t.Fatalf("response carries no id: %v", r.body)
	}
	return value
}

func status(t *testing.T, r response) string {
	t.Helper()
	value, _ := r.body["status"].(string)
	return value
}

// createBody is the standard draft: one session of the seeded definition.
func (s *server) createBody() string {
	return fmt.Sprintf(`{"requestType":"DIRECT_SERVICE","personId":%q,"programId":%q,`+
		`"enrollmentId":%q,"providerOrganizationId":%q,"serviceDate":%q,"channel":"BACKOFFICE",`+
		`"items":[{"serviceDefinitionId":%q,"requestedQuantity":"1","unitType":"SESSION"}]}`,
		s.person, s.program, s.enrollment, s.providerOr,
		serviceDate.Format(time.DateOnly), s.definition)
}

// createDraft posts a draft and returns its id and ETag.
func (s *server) createDraft(t *testing.T) (string, string) {
	t.Helper()
	created := s.do(t, http.MethodPost, "/api/v1/service-requests", allPermissions, s.createBody(), nil)
	if created.code != http.StatusCreated {
		t.Fatalf("create: %d %v", created.code, created.body)
	}
	return id(t, created), created.etag
}

// submitDraft submits a draft and returns the request id and its new ETag.
func (s *server) submitDraft(t *testing.T) (string, string) {
	t.Helper()
	requestID, etag := s.createDraft(t)
	submitted := s.do(t, http.MethodPost, "/api/v1/service-requests/"+requestID+"/submit",
		allPermissions, "", ifMatch(etag))
	if submitted.code != http.StatusOK {
		t.Fatalf("submit: %d %v", submitted.code, submitted.body)
	}
	if got := status(t, submitted); got != "PENDING_REVIEW" {
		t.Fatalf("status after submit = %q, want PENDING_REVIEW", got)
	}
	return requestID, submitted.etag
}

func TestCreateReadAndListAServiceRequest(t *testing.T) {
	s := newServer(t)
	requestID, etag := s.createDraft(t)
	if etag == "" {
		t.Fatal("the create answered without an ETag")
	}

	got := s.do(t, http.MethodGet, "/api/v1/service-requests/"+requestID, readOnly, "", nil)
	if got.code != http.StatusOK {
		t.Fatalf("get: %d %v", got.code, got.body)
	}
	if status(t, got) != "DRAFT" {
		t.Fatalf("status = %q, want DRAFT", status(t, got))
	}
	if got.etag != etag {
		t.Fatalf("ETag changed by a read: %q -> %q", etag, got.etag)
	}

	page := s.do(t, http.MethodGet, "/api/v1/service-requests?status=DRAFT", readOnly, "", nil)
	if page.code != http.StatusOK {
		t.Fatalf("list: %d %v", page.code, page.body)
	}
	items, _ := page.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list returned %d items, want 1", len(items))
	}

	unknown := s.do(t, http.MethodGet, "/api/v1/service-requests?status=NONSENSE", readOnly, "", nil)
	if unknown.code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown status filter: %d %v", unknown.code, unknown.body)
	}
}

// TestARequestCarriesTheNamesItsScreenNeeds is WP-I5-05 section 2.6 at the wire: a list of
// requests names the member and the provider, so a screen showing thirty of them makes one
// request rather than sixty-one. Both the single read and the list carry them, because a
// list that named people and a detail that did not would send an operator looking for a
// bug in the screen.
func TestARequestCarriesTheNamesItsScreenNeeds(t *testing.T) {
	s := newServer(t)
	requestID, _ := s.createDraft(t)

	got := s.do(t, http.MethodGet, "/api/v1/service-requests/"+requestID, readOnly, "", nil)
	if got.code != http.StatusOK {
		t.Fatalf("get: %d %v", got.code, got.body)
	}
	if name, _ := got.body["personDisplayName"].(string); name != "Deniz Aksoy" {
		t.Fatalf("personDisplayName = %q, want the member's name", name)
	}
	if name, _ := got.body["providerDisplayName"].(string); name != "HTTP Provider" {
		t.Fatalf("providerDisplayName = %q, want the provider's name", name)
	}

	page := s.do(t, http.MethodGet, "/api/v1/service-requests?status=DRAFT", readOnly, "", nil)
	items, _ := page.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list returned %d items, want 1", len(items))
	}
	row, _ := items[0].(map[string]any)
	if name, _ := row["personDisplayName"].(string); name != "Deniz Aksoy" {
		t.Fatalf("list personDisplayName = %q", name)
	}
	if name, _ := row["providerDisplayName"].(string); name != "HTTP Provider" {
		t.Fatalf("list providerDisplayName = %q", name)
	}
}

// TestPatchRefusesAStatusField is the property the whole package is built around: there is
// no endpoint that writes status, and a body that tries is told so rather than ignored.
func TestPatchRefusesAStatusField(t *testing.T) {
	s := newServer(t)
	requestID, etag := s.createDraft(t)

	got := s.do(t, http.MethodPatch, "/api/v1/service-requests/"+requestID, allPermissions,
		`{"status":"APPROVED"}`,
		map[string]string{"Content-Type": patchType, "If-Match": etag})
	if got.code != http.StatusUnprocessableEntity {
		t.Fatalf("patch with status: %d %v", got.code, got.body)
	}
	field, code := firstFieldError(t, got)
	if field != "status" || code != "IMMUTABLE" {
		t.Fatalf("field error = %s/%s, want status/IMMUTABLE", field, code)
	}

	// And the request did not move.
	after := s.do(t, http.MethodGet, "/api/v1/service-requests/"+requestID, readOnly, "", nil)
	if status(t, after) != "DRAFT" {
		t.Fatalf("status = %q after the refused patch", status(t, after))
	}
}

func TestPatchUpdatesTheDraftAndNeedsIfMatch(t *testing.T) {
	s := newServer(t)
	requestID, etag := s.createDraft(t)

	missing := s.do(t, http.MethodPatch, "/api/v1/service-requests/"+requestID, allPermissions,
		`{"serviceDate":"2026-06-16"}`, map[string]string{"Content-Type": patchType})
	if missing.code != http.StatusPreconditionRequired || problemCode(missing) != "IF_MATCH_REQUIRED" {
		t.Fatalf("missing If-Match: %d %s", missing.code, problemCode(missing))
	}

	wrongType := s.do(t, http.MethodPatch, "/api/v1/service-requests/"+requestID, allPermissions,
		`{"serviceDate":"2026-06-16"}`, ifMatch(etag))
	if wrongType.code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong media type: %d %v", wrongType.code, wrongType.body)
	}

	ok := s.do(t, http.MethodPatch, "/api/v1/service-requests/"+requestID, allPermissions,
		`{"serviceDate":"2026-06-16"}`,
		map[string]string{"Content-Type": patchType, "If-Match": etag})
	if ok.code != http.StatusOK {
		t.Fatalf("patch: %d %v", ok.code, ok.body)
	}
	if ok.body["serviceDate"] != "2026-06-16" {
		t.Fatalf("serviceDate = %v", ok.body["serviceDate"])
	}

	stale := s.do(t, http.MethodPatch, "/api/v1/service-requests/"+requestID, allPermissions,
		`{"serviceDate":"2026-06-17"}`,
		map[string]string{"Content-Type": patchType, "If-Match": etag})
	if stale.code != http.StatusPreconditionFailed || problemCode(stale) != "ETAG_MISMATCH" {
		t.Fatalf("stale If-Match: %d %s", stale.code, problemCode(stale))
	}
}

func TestPutItemsReplacesTheDraftLines(t *testing.T) {
	s := newServer(t)
	requestID, etag := s.createDraft(t)

	body := fmt.Sprintf(`{"items":[{"serviceDefinitionId":%q,"requestedQuantity":"2","unitType":"SESSION"},`+
		`{"serviceDefinitionId":%q,"requestedQuantity":"3","unitType":"SESSION","requestedAmount":"99.25","currencyCode":"TRY"}]}`,
		s.definition, s.definition)
	got := s.do(t, http.MethodPut, "/api/v1/service-requests/"+requestID+"/items",
		allPermissions, body, ifMatch(etag))
	if got.code != http.StatusOK {
		t.Fatalf("put items: %d %v", got.code, got.body)
	}
	items, _ := got.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	second, _ := items[1].(map[string]any)
	if second["requestedAmount"] != "99.25" {
		t.Fatalf("requestedAmount = %v, want the exact decimal string 99.25", second["requestedAmount"])
	}
	if got.etag == etag {
		t.Fatal("replacing the lines left the ETag where it was")
	}
}

func TestSubmitFreezesTheVersionAndTheVersionIsThenImmutable(t *testing.T) {
	s := newServer(t)
	requestID, etag := s.submitDraft(t)

	versions := s.do(t, http.MethodGet, "/api/v1/service-requests/"+requestID+"/versions", readOnly, "", nil)
	if versions.code != http.StatusOK {
		t.Fatalf("list versions: %d %v", versions.code, versions.body)
	}
	items, _ := versions.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("versions = %d, want 1", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["status"] != "SUBMITTED" {
		t.Fatalf("version status = %v, want SUBMITTED", first["status"])
	}

	version := s.do(t, http.MethodGet, "/api/v1/service-requests/"+requestID+"/versions/1", readOnly, "", nil)
	if version.code != http.StatusOK {
		t.Fatalf("get version: %d %v", version.code, version.body)
	}
	lines, _ := version.body["items"].([]any)
	if len(lines) != 1 {
		t.Fatalf("version lines = %d, want 1", len(lines))
	}

	frozen := s.do(t, http.MethodPut, "/api/v1/service-requests/"+requestID+"/items", allPermissions,
		fmt.Sprintf(`{"items":[{"serviceDefinitionId":%q,"requestedQuantity":"9","unitType":"SESSION"}]}`, s.definition),
		ifMatch(etag))
	if frozen.code != http.StatusConflict ||
		problemCode(frozen) != "SERVICE_REQUEST_VERSION_IMMUTABLE" {
		t.Fatalf("writing to a submitted version: %d %s", frozen.code, problemCode(frozen))
	}

	missing := s.do(t, http.MethodGet, "/api/v1/service-requests/"+requestID+"/versions/9", readOnly, "", nil)
	if missing.code != http.StatusNotFound {
		t.Fatalf("unknown version: %d %v", missing.code, missing.body)
	}
}

func TestReturnOpensTheNextVersionAndKeepsTheReference(t *testing.T) {
	s := newServer(t)
	requestID, etag := s.submitDraft(t)
	before := s.do(t, http.MethodGet, "/api/v1/service-requests/"+requestID, readOnly, "", nil)
	reference := before.body["reference"]

	returned := s.do(t, http.MethodPost, "/api/v1/service-requests/"+requestID+"/return",
		allPermissions, `{"reasonCode":"MISSING_DETAIL","reasonText":"Tarih eksik"}`, ifMatch(etag))
	if returned.code != http.StatusOK {
		t.Fatalf("return: %d %v", returned.code, returned.body)
	}
	if status(t, returned) != "DRAFT" {
		t.Fatalf("status = %q, want DRAFT", status(t, returned))
	}
	if returned.body["reference"] != reference {
		t.Fatalf("reference changed: %v -> %v", reference, returned.body["reference"])
	}
	if returned.body["currentVersionNo"] != float64(2) {
		t.Fatalf("currentVersionNo = %v, want 2", returned.body["currentVersionNo"])
	}
	if returned.body["returnReasonCode"] != "MISSING_DETAIL" {
		t.Fatalf("returnReasonCode = %v", returned.body["returnReasonCode"])
	}

	// Version 1 is still readable exactly as it was submitted.
	version := s.do(t, http.MethodGet, "/api/v1/service-requests/"+requestID+"/versions/1", readOnly, "", nil)
	if version.body["status"] != "SUPERSEDED" {
		t.Fatalf("version 1 status = %v, want SUPERSEDED", version.body["status"])
	}
	lines, _ := version.body["items"].([]any)
	if len(lines) != 1 {
		t.Fatalf("version 1 lines = %d, want the one that was submitted", len(lines))
	}

	// And the corrected draft can be submitted again.
	again := s.do(t, http.MethodPost, "/api/v1/service-requests/"+requestID+"/submit",
		allPermissions, "", ifMatch(returned.etag))
	if again.code != http.StatusOK {
		t.Fatalf("resubmit: %d %v", again.code, again.body)
	}
	if again.body["currentVersionNo"] != float64(2) {
		t.Fatalf("currentVersionNo after resubmit = %v", again.body["currentVersionNo"])
	}
}

func TestRejectIsFinalAndReturnIsRefusedAfterwards(t *testing.T) {
	s := newServer(t)
	requestID, etag := s.submitDraft(t)

	rejected := s.do(t, http.MethodPost, "/api/v1/service-requests/"+requestID+"/reject",
		allPermissions, `{"reasonCode":"NOT_COVERED"}`, ifMatch(etag))
	if rejected.code != http.StatusOK {
		t.Fatalf("reject: %d %v", rejected.code, rejected.body)
	}
	if status(t, rejected) != "REJECTED" {
		t.Fatalf("status = %q, want REJECTED", status(t, rejected))
	}

	again := s.do(t, http.MethodPost, "/api/v1/service-requests/"+requestID+"/return",
		allPermissions, `{"reasonCode":"FIX_IT"}`, ifMatch(rejected.etag))
	if again.code != http.StatusConflict || problemCode(again) != "REQUEST_TRANSITION_INVALID" {
		t.Fatalf("return after reject: %d %s", again.code, problemCode(again))
	}
}

func TestApproveAndPartiallyApprove(t *testing.T) {
	s := newServer(t)

	requestID, etag := s.submitDraft(t)
	approved := s.do(t, http.MethodPost, "/api/v1/service-requests/"+requestID+"/approve",
		allPermissions, `{"reasonCode":"COVERED"}`, ifMatch(etag))
	if approved.code != http.StatusOK || status(t, approved) != "APPROVED" {
		t.Fatalf("approve: %d %v", approved.code, approved.body)
	}

	otherID, otherETag := s.submitDraft(t)
	notPartial := s.do(t, http.MethodPost, "/api/v1/service-requests/"+otherID+"/partially-approve",
		allPermissions, `{"reasonCode":"PARTIAL","items":[{"lineNo":1,"status":"APPROVED"}]}`,
		ifMatch(otherETag))
	if notPartial.code != http.StatusUnprocessableEntity {
		t.Fatalf("a partial approval that approves everything: %d %v", notPartial.code, notPartial.body)
	}
	partial := s.do(t, http.MethodPost, "/api/v1/service-requests/"+otherID+"/partially-approve",
		allPermissions, `{"reasonCode":"LIMIT","items":[{"lineNo":1,"status":"REJECTED"}]}`,
		ifMatch(otherETag))
	if partial.code != http.StatusOK || status(t, partial) != "PARTIALLY_APPROVED" {
		t.Fatalf("partially approve: %d %v", partial.code, partial.body)
	}
	items, _ := partial.body["items"].([]any)
	line, _ := items[0].(map[string]any)
	if line["status"] != "REJECTED" {
		t.Fatalf("line status = %v, want REJECTED", line["status"])
	}
}

func TestCancelWithdrawsADraft(t *testing.T) {
	s := newServer(t)
	requestID, etag := s.createDraft(t)

	cancelled := s.do(t, http.MethodPost, "/api/v1/service-requests/"+requestID+"/cancel",
		allPermissions, `{"reasonCode":"MEMBER_WITHDREW"}`, ifMatch(etag))
	if cancelled.code != http.StatusOK || status(t, cancelled) != "CANCELLED" {
		t.Fatalf("cancel: %d %v", cancelled.code, cancelled.body)
	}
	again := s.do(t, http.MethodPost, "/api/v1/service-requests/"+requestID+"/cancel",
		allPermissions, `{"reasonCode":"MEMBER_WITHDREW"}`, ifMatch(cancelled.etag))
	if again.code != http.StatusConflict || problemCode(again) != "REQUEST_TRANSITION_INVALID" {
		t.Fatalf("second cancel: %d %s", again.code, problemCode(again))
	}
}

func TestEveryCommandNeedsItsOwnPermission(t *testing.T) {
	s := newServer(t)
	requestID, etag := s.submitDraft(t)

	cases := []struct {
		name, method, path, body, perms, want string
	}{
		{"submit", http.MethodPost, "/submit", "", readOnly, "service_request.submit"},
		{"return", http.MethodPost, "/return", `{"reasonCode":"ANY"}`, readOnly, "service_request.review"},
		{"reject", http.MethodPost, "/reject", `{"reasonCode":"ANY"}`, readOnly, "service_request.review"},
		{"approve", http.MethodPost, "/approve", `{"reasonCode":"ANY"}`, readOnly, "service_request.review"},
		{"partially approve", http.MethodPost, "/partially-approve", `{"reasonCode":"ANY"}`, readOnly, "service_request.review"},
		{"cancel", http.MethodPost, "/cancel", `{"reasonCode":"ANY"}`, readOnly, "service_request.cancel"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s.denied.permissions = nil
			got := s.do(t, tc.method, "/api/v1/service-requests/"+requestID+tc.path,
				tc.perms, tc.body, ifMatch(etag))
			if got.code != http.StatusForbidden {
				t.Fatalf("%s without permission: %d %v", tc.name, got.code, got.body)
			}
			if len(s.denied.permissions) != 1 || s.denied.permissions[0] != tc.want {
				t.Fatalf("denied permission = %v, want %q", s.denied.permissions, tc.want)
			}
		})
	}
}

func TestEveryCommandNeedsIfMatch(t *testing.T) {
	s := newServer(t)
	requestID, _ := s.submitDraft(t)

	for _, path := range []string{"/submit", "/return", "/reject", "/approve", "/partially-approve", "/cancel"} {
		t.Run(path, func(t *testing.T) {
			got := s.do(t, http.MethodPost, "/api/v1/service-requests/"+requestID+path,
				allPermissions, `{"reasonCode":"ANY"}`, nil)
			if got.code != http.StatusPreconditionRequired || problemCode(got) != "IF_MATCH_REQUIRED" {
				t.Fatalf("%s without If-Match: %d %s", path, got.code, problemCode(got))
			}
		})
	}
}

func TestAnotherProvidersRequestIsNotFound(t *testing.T) {
	s := newServer(t)
	requestID, _ := s.createDraft(t)

	got := s.do(t, http.MethodGet, "/api/v1/service-requests/"+requestID, allPermissions, "",
		map[string]string{scopeHeader: s.otherOr.String()})
	if got.code != http.StatusNotFound || problemCode(got) != "SERVICE_REQUEST_NOT_FOUND" {
		t.Fatalf("another provider: %d %s, want 404 SERVICE_REQUEST_NOT_FOUND", got.code, problemCode(got))
	}

	own := s.do(t, http.MethodGet, "/api/v1/service-requests/"+requestID, allPermissions, "",
		map[string]string{scopeHeader: s.providerOr.String()})
	if own.code != http.StatusOK {
		t.Fatalf("the owning provider: %d %v", own.code, own.body)
	}
}

func TestUnknownRequestIsNotFound(t *testing.T) {
	s := newServer(t)
	got := s.do(t, http.MethodGet, "/api/v1/service-requests/"+uuid.New().String(), readOnly, "", nil)
	if got.code != http.StatusNotFound || problemCode(got) != "SERVICE_REQUEST_NOT_FOUND" {
		t.Fatalf("unknown request: %d %s", got.code, problemCode(got))
	}
	malformed := s.do(t, http.MethodGet, "/api/v1/service-requests/not-a-uuid", readOnly, "", nil)
	if malformed.code != http.StatusNotFound {
		t.Fatalf("malformed id: %d %v", malformed.code, malformed.body)
	}
}

func TestCreateRejectsAnInvalidBody(t *testing.T) {
	s := newServer(t)

	body := fmt.Sprintf(`{"requestType":"DIRECT_SERVICE","personId":%q,"programId":%q,`+
		`"enrollmentId":%q,"serviceDate":%q,"channel":"BACKOFFICE",`+
		`"items":[{"serviceDefinitionId":%q,"requestedQuantity":"0","unitType":"SESSION"}]}`,
		s.person, s.program, s.enrollment, serviceDate.Format(time.DateOnly), s.definition)
	got := s.do(t, http.MethodPost, "/api/v1/service-requests", allPermissions, body, nil)
	if got.code != http.StatusUnprocessableEntity {
		t.Fatalf("zero quantity: %d %v", got.code, got.body)
	}
	field, code := firstFieldError(t, got)
	if field != "items[0].requestedQuantity" || code != "RANGE" {
		t.Fatalf("field error = %s/%s", field, code)
	}
}
