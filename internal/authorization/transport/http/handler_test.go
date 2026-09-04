package authorizationhttp_test

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
	"github.com/celikbros/kapsora/internal/authorization/application"
	authorizationpg "github.com/celikbros/kapsora/internal/authorization/infrastructure/postgres"
	authorizationhttp "github.com/celikbros/kapsora/internal/authorization/transport/http"
	"github.com/celikbros/kapsora/internal/identity"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Test headers let each request choose its permissions and its provider scope, which is
// what a provider boundary and a three-permission module need from an HTTP test.
const (
	permsHeader    = "X-Test-Permissions"
	scopeHeader    = "X-Test-Scope"
	allPermissions = "authorization.manage,fulfilment.record,voucher.redeem,service_request.read"
	recordOnly     = "fulfilment.record"
	readOnly       = "service_request.read"
	entitlement    = "PHYSIO"
)

var (
	testNow     = time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	serviceDate = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
)

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
		Pool: h.App, Repo: authorizationpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{h: h, denied: &denyRecorder{}}
	s.seed(t)

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := authorizationhttp.NewHandler(svc, s.denied, logger)

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
		api.Route("/authorizations", func(rr chi.Router) {
			handler.AuthorizationRoutes(rr, authorizationhttp.Middlewares{})
		})
		api.Route("/fulfilments", func(rr chi.Router) {
			handler.FulfilmentRoutes(rr, authorizationhttp.Middlewares{})
		})
		handler.VoucherRoutes(api, authorizationhttp.Middlewares{})
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

	s.tenant = h.CreateTenant("HTTP_AUTHORIZATIONS")
	s.actor = h.CreateActor("authorization-http-clerk", "Authorization Clerk")
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
	var planID, planVersion, definitionID uuid.UUID
	scan(&planID, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, s.tenant, s.program)
	scan(&planVersion, "plan version", `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period,
		                                  published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', daterange('2026-01-01','2027-01-01','[)'), clock_timestamp(), $3)
		RETURNING id`, s.tenant, planID, s.actor)
	scan(&definitionID, "entitlement definition", `
		INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name, unit_type,
		                                            period_type, initial_quantity)
		VALUES ($1, $2, $3, $3, 'SESSION', 'CALENDAR_YEAR', 300) RETURNING id`,
		s.tenant, planVersion, entitlement)
	scan(&s.enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, membership, planID)
	var account uuid.UUID
	scan(&account, "entitlement account", `
		INSERT INTO benefit.entitlement_account (tenant_id, enrollment_id, entitlement_definition_id,
		                                         benefit_period, total_granted, available_quantity)
		VALUES ($1, $2, $3, daterange('2026-01-01','2027-01-01','[)'), 300, 300) RETURNING id`,
		s.tenant, s.enrollment, definitionID)
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
		s.tenant, category, entitlement)
}

// seedApprovedRequest writes an APPROVED request with one line for the quantity given.
func (s *server) seedApprovedRequest(t *testing.T, provider uuid.UUID, quantity string) uuid.UUID {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var requestID, versionID uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO service.service_request (tenant_id, request_reference, request_type, person_id,
		                                     program_id, enrollment_id, provider_tenant_organization_id,
		                                     service_date, channel)
		VALUES ($1, $2, 'PREAUTHORIZATION', $3, $4, $5, $6, $7, 'PROVIDER_PORTAL') RETURNING id`,
		s.tenant, "SR-HTTP-"+uuid.NewString()[:8], s.person, s.program, s.enrollment,
		provider, serviceDate).Scan(&requestID); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO service.service_request_version (tenant_id, service_request_id, version_no, status)
		VALUES ($1, $2, 1, 'DRAFT') RETURNING id`, s.tenant, requestID).Scan(&versionID); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	s.h.AdminExec(`
		INSERT INTO service.service_request_item (tenant_id, service_request_version_id, line_no,
		                                          service_definition_id, requested_quantity, unit_type)
		VALUES ($1, $2, 1, $3, $4::text::numeric, 'SESSION')`,
		s.tenant, versionID, s.definition, quantity)
	s.h.AdminExec(`
		UPDATE service.service_request_version
		   SET status = 'SUBMITTED', snapshot_json = '{}'::jsonb, submitted_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, versionID)
	s.h.AdminExec(`
		UPDATE service.service_request_item SET status = 'APPROVED', approved_quantity = requested_quantity
		 WHERE tenant_id = $1 AND service_request_version_id = $2`, s.tenant, versionID)
	s.h.AdminExec(`
		UPDATE service.service_request SET status = 'APPROVED', submitted_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, requestID)
	return requestID
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

func id(t *testing.T, r response) string {
	t.Helper()
	value, _ := r.body["id"].(string)
	if value == "" {
		t.Fatalf("response carries no id: %v", r.body)
	}
	return value
}

// create posts an authorization over an approved request and returns the response.
func (s *server) create(t *testing.T, requestID uuid.UUID, perms string) response {
	t.Helper()
	body := fmt.Sprintf(`{"requestId":%q,"validTo":%q}`,
		requestID, testNow.Add(24*time.Hour).Format(time.RFC3339))
	return s.do(t, http.MethodPost, "/api/v1/authorizations", perms, body,
		map[string]string{"Idempotency-Key": uuid.NewString()})
}

func TestCreateAuthorizationEndToEnd(t *testing.T) {
	s := newServer(t)
	requestID := s.seedApprovedRequest(t, s.providerOr, "3")

	created := s.create(t, requestID, allPermissions)
	if created.code != http.StatusCreated {
		t.Fatalf("create answered %d %v", created.code, created.body)
	}
	if created.etag == "" {
		t.Fatal("create must answer with an ETag")
	}
	if created.body["reservedTotal"] != "3" {
		t.Fatalf("reservedTotal = %v, want \"3\"", created.body["reservedTotal"])
	}
	authorizationID := id(t, created)

	// The lines carry the exact hold each of them took.
	itemsRaw, _ := created.body["items"].([]any)
	if len(itemsRaw) != 1 {
		t.Fatalf("items = %v, want one", created.body["items"])
	}
	item, _ := itemsRaw[0].(map[string]any)
	if item["entitlementReservationId"] == nil {
		t.Fatalf("the line carries no reservation id: %v", item)
	}
	if item["approvedQuantity"] != "3" {
		t.Fatalf("approvedQuantity = %v, want a decimal string \"3\"", item["approvedQuantity"])
	}

	got := s.do(t, http.MethodGet, "/api/v1/authorizations/"+authorizationID, readOnly, "", nil)
	if got.code != http.StatusOK {
		t.Fatalf("get answered %d %v", got.code, got.body)
	}

	page := s.do(t, http.MethodGet, "/api/v1/authorizations?status=ACTIVE", readOnly, "", nil)
	if page.code != http.StatusOK {
		t.Fatalf("list answered %d %v", page.code, page.body)
	}
	if list, _ := page.body["items"].([]any); len(list) != 1 {
		t.Fatalf("list returned %v, want one authorization", page.body["items"])
	}
}

func TestCreateAuthorizationNeedsItsOwnPermission(t *testing.T) {
	s := newServer(t)
	requestID := s.seedApprovedRequest(t, s.providerOr, "1")

	// Recording a delivery is not the same grant as promising one.
	refused := s.create(t, requestID, recordOnly)
	if refused.code != http.StatusForbidden {
		t.Fatalf("create with only %s answered %d %v", recordOnly, refused.code, refused.body)
	}
	if len(s.denied.permissions) == 0 || s.denied.permissions[0] != "authorization.manage" {
		t.Fatalf("denial audited as %v, want authorization.manage", s.denied.permissions)
	}
}

func TestAuthorizationOutsideTheProviderBoundaryIsNotFound(t *testing.T) {
	s := newServer(t)
	requestID := s.seedApprovedRequest(t, s.providerOr, "1")
	created := s.create(t, requestID, allPermissions)
	authorizationID := id(t, created)

	// 404 rather than 403: that such an authorization exists is itself information.
	outside := s.do(t, http.MethodGet, "/api/v1/authorizations/"+authorizationID, readOnly, "",
		map[string]string{scopeHeader: s.otherOr.String()})
	if outside.code != http.StatusNotFound {
		t.Fatalf("another provider answered %d %v, want 404", outside.code, outside.body)
	}
	if problemCode(outside) != "AUTHORIZATION_NOT_FOUND" {
		t.Fatalf("problem code = %s, want AUTHORIZATION_NOT_FOUND", problemCode(outside))
	}
	inside := s.do(t, http.MethodGet, "/api/v1/authorizations/"+authorizationID, readOnly, "",
		map[string]string{scopeHeader: s.providerOr.String()})
	if inside.code != http.StatusOK {
		t.Fatalf("the owning provider answered %d %v", inside.code, inside.body)
	}
}

func TestFulfilmentAndVoucherEndToEnd(t *testing.T) {
	s := newServer(t)
	requestID := s.seedApprovedRequest(t, s.providerOr, "2")
	created := s.create(t, requestID, allPermissions)
	authorizationID := id(t, created)
	itemsRaw, _ := created.body["items"].([]any)
	item, _ := itemsRaw[0].(map[string]any)
	itemID, _ := item["id"].(string)

	recorded := s.do(t, http.MethodPost, "/api/v1/fulfilments", allPermissions,
		fmt.Sprintf(`{"authorizationId":%q,"performedAt":%q,"items":[{"authorizationItemId":%q,"actualQuantity":"1"}]}`,
			authorizationID, testNow.Format(time.RFC3339), itemID), nil)
	if recorded.code != http.StatusCreated {
		t.Fatalf("record answered %d %v", recorded.code, recorded.body)
	}
	fulfilmentID := id(t, recorded)

	// Completing needs the If-Match of the recorded fulfilment.
	missing := s.do(t, http.MethodPost, "/api/v1/fulfilments/"+fulfilmentID+"/complete",
		allPermissions, "", nil)
	if missing.code != http.StatusPreconditionRequired {
		t.Fatalf("complete without If-Match answered %d %v", missing.code, missing.body)
	}
	completed := s.do(t, http.MethodPost, "/api/v1/fulfilments/"+fulfilmentID+"/complete",
		allPermissions, "", ifMatch(recorded.etag))
	if completed.code != http.StatusOK {
		t.Fatalf("complete answered %d %v", completed.code, completed.body)
	}
	if completed.body["status"] != "COMPLETED" {
		t.Fatalf("status = %v, want COMPLETED", completed.body["status"])
	}

	// A voucher is shown once and never again.
	issued := s.do(t, http.MethodPost, "/api/v1/authorizations/"+authorizationID+"/vouchers",
		allPermissions, "", nil)
	if issued.code != http.StatusCreated {
		t.Fatalf("issue answered %d %v", issued.code, issued.body)
	}
	token, _ := issued.body["token"].(string)
	if token == "" {
		t.Fatalf("the issue response carries no token: %v", issued.body)
	}
	voucher, _ := issued.body["voucher"].(map[string]any)
	if _, leaked := voucher["token"]; leaked {
		t.Fatal("the voucher object must not carry the token")
	}
	reread := s.do(t, http.MethodGet, "/api/v1/authorizations/"+authorizationID, readOnly, "", nil)
	if strings.Contains(fmt.Sprint(reread.body), token) {
		t.Fatal("the token is readable again from getAuthorization")
	}

	redeemed := s.do(t, http.MethodPost, "/api/v1/vouchers:redeem", allPermissions,
		fmt.Sprintf(`{"token":%q,"performedAt":%q,"items":[{"authorizationItemId":%q,"actualQuantity":"1"}]}`,
			token, testNow.Format(time.RFC3339), itemID), nil)
	if redeemed.code != http.StatusCreated {
		t.Fatalf("redeem answered %d %v", redeemed.code, redeemed.body)
	}
	again := s.do(t, http.MethodPost, "/api/v1/vouchers:redeem", allPermissions,
		fmt.Sprintf(`{"token":%q,"performedAt":%q,"items":[{"authorizationItemId":%q,"actualQuantity":"1"}]}`,
			token, testNow.Format(time.RFC3339), itemID), nil)
	if again.code != http.StatusConflict || problemCode(again) != "VOUCHER_ALREADY_REDEEMED" {
		t.Fatalf("second redemption answered %d %s", again.code, problemCode(again))
	}
}

func TestCancelAuthorizationNeedsAReasonAndAnIfMatch(t *testing.T) {
	s := newServer(t)
	requestID := s.seedApprovedRequest(t, s.providerOr, "1")
	created := s.create(t, requestID, allPermissions)
	authorizationID := id(t, created)

	missing := s.do(t, http.MethodPost, "/api/v1/authorizations/"+authorizationID+"/cancel",
		allPermissions, `{"reasonCode":"WITHDRAWN"}`, nil)
	if missing.code != http.StatusPreconditionRequired {
		t.Fatalf("cancel without If-Match answered %d %v", missing.code, missing.body)
	}
	bad := s.do(t, http.MethodPost, "/api/v1/authorizations/"+authorizationID+"/cancel",
		allPermissions, `{"reasonCode":"nope"}`, ifMatch(created.etag))
	if bad.code != http.StatusUnprocessableEntity || problemCode(bad) != "VALIDATION_FAILED" {
		t.Fatalf("cancel with a bad reason code answered %d %s", bad.code, problemCode(bad))
	}
	ok := s.do(t, http.MethodPost, "/api/v1/authorizations/"+authorizationID+"/cancel",
		allPermissions, `{"reasonCode":"MEMBER_WITHDREW"}`, ifMatch(created.etag))
	if ok.code != http.StatusOK || ok.body["status"] != "CANCELLED" {
		t.Fatalf("cancel answered %d %v", ok.code, ok.body)
	}
	// A second attempt with the stale ETag is a precondition failure, not a second release.
	stale := s.do(t, http.MethodPost, "/api/v1/authorizations/"+authorizationID+"/cancel",
		allPermissions, `{"reasonCode":"MEMBER_WITHDREW"}`, ifMatch(created.etag))
	if stale.code != http.StatusConflict && stale.code != http.StatusPreconditionFailed {
		t.Fatalf("second cancel answered %d %v", stale.code, stale.body)
	}
}
