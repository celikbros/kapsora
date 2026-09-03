package identityhttp_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

type authzServer struct {
	*server
	prov    *application.Provisioner
	actor   uuid.UUID
	tenantA uuid.UUID
	tenantB uuid.UUID
}

// newAuthzServer mirrors the production router: session loading, CSRF, the pre-tenant
// routes and a tenant-scoped group with two probe routes.
func newAuthzServer(t *testing.T) *authzServer {
	t.Helper()
	h := dbtest.New(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	sessions := identitypg.NewSessionStore(h.App)
	sink := identitypg.NewAuditSink(h.App, auditpg.New(), logger)
	svc, err := application.New(application.Deps{
		Credentials: identitypg.NewCredentialRepository(h.App),
		Sessions:    sessions,
		Audit:       sink,
		Policy:      domain.DefaultPolicy(),
		Lockout:     domain.DefaultLockout(),
	})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := svc.CreateAccount(ctx, username, "Test Kullanıcı", username, testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	prov := application.NewProvisioner(identitypg.NewProvisioningRepository(h.App), sink)
	tenantA, err := prov.ProvisionTenant(ctx, application.ProvisionCommand{Code: "HTTP_A", LegalName: "HTTP A A.Ş.", DisplayName: "HTTP A"})
	if err != nil {
		t.Fatal(err)
	}
	tenantB, err := prov.ProvisionTenant(ctx, application.ProvisionCommand{Code: "HTTP_B", LegalName: "HTTP B A.Ş.", DisplayName: "HTTP B"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prov.GrantRole(ctx, application.GrantRoleInput{TenantID: tenantA, ActorID: actor, RoleCode: "AUDITOR"}); err != nil {
		t.Fatal(err)
	}

	authz := application.NewAuthorizer(identitypg.NewAuthorizationRepository(h.App), sessions, sink, nil)
	cookies := identityhttp.CookieConfig{Secure: true}
	mw := identityhttp.NewMiddleware(svc, cookies, signingKey, logger).WithAuthorizer(authz)
	sessionHandler := identityhttp.NewHandler(svc, cookies, signingKey, logger)
	contextHandler := identityhttp.NewContextHandler(svc, authz, logger)

	r := chi.NewRouter()
	r.Route("/api/v1", func(api chi.Router) {
		api.Use(mw.LoadSession)
		api.Post("/session/login", sessionHandler.Login)
		api.Group(func(authed chi.Router) {
			authed.Use(mw.RequireCSRF)
			authed.Get("/session", sessionHandler.GetSession)
			authed.Post("/session/switch-tenant", contextHandler.SwitchTenant)
			authed.Get("/me", contextHandler.GetMe)
			authed.Get("/tenants", contextHandler.ListTenants)
		})
		api.Group(func(tenant chi.Router) {
			tenant.Use(mw.RequireCSRF)
			tenant.Use(mw.RequireTenantContext)
			tenant.Get("/probe", func(w http.ResponseWriter, r *http.Request) {
				rc, err := identity.Require(r.Context(), "audit.read")
				if err != nil {
					mw.Deny(w, r, err, "audit.read")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{"tenantId": rc.TenantID.String()})
			})
			tenant.Get("/probe-admin", func(w http.ResponseWriter, r *http.Request) {
				if _, err := identity.Require(r.Context(), "organization.manage"); err != nil {
					mw.Deny(w, r, err, "organization.manage")
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
		})
	})
	return &authzServer{
		server:  &server{h: h, handler: r, cookies: cookies},
		prov:    prov,
		actor:   actor,
		tenantA: tenantA,
		tenantB: tenantB,
	}
}

func TestMeAndTenantsMatchTheContractAndListOnlyOwnMemberships(t *testing.T) {
	s := newAuthzServer(t)

	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/me"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("/me without a session: %d", rec.Code)
	}

	cookie, _ := s.login(t)
	rec := s.do(call{method: http.MethodGet, path: "/api/v1/me", cookie: cookie})
	if rec.Code != http.StatusOK {
		t.Fatalf("/me: %d %s", rec.Code, rec.Body.String())
	}
	var me kapsorav1.UserContext
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("/me does not match the generated UserContext: %v\n%s", err, rec.Body.String())
	}
	if me.ActorId.String() != s.actor.String() || me.DisplayName != "Test Kullanıcı" || me.Email == nil {
		t.Fatalf("me = %+v", me)
	}
	if len(me.Tenants) != 1 || me.Tenants[0].Tenant.Id.String() != s.tenantA.String() || me.Tenants[0].Tenant.Code != "HTTP_A" {
		t.Fatalf("tenants = %+v", me.Tenants)
	}
	if !containsString(me.Tenants[0].Permissions, "audit.read") || containsString(me.Tenants[0].Permissions, "organization.manage") {
		t.Fatalf("permissions = %v", me.Tenants[0].Permissions)
	}

	rec = s.do(call{method: http.MethodGet, path: "/api/v1/tenants", cookie: cookie})
	if rec.Code != http.StatusOK {
		t.Fatalf("/tenants: %d", rec.Code)
	}
	var list struct {
		Items []kapsorav1.TenantSummary `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].Code != "HTTP_A" {
		t.Fatalf("/tenants = %s err=%v", rec.Body.String(), err)
	}
}

func TestTenantScopedRoutesNeedAMatchingActiveTenant(t *testing.T) {
	s := newAuthzServer(t)
	cookie, csrf := s.login(t)
	probe := func(tenantHeader string) (int, map[string]any) {
		t.Helper()
		req := call{method: http.MethodGet, path: "/api/v1/probe", cookie: cookie}
		rec := s.do(withHeader(req, identityhttp.TenantHeader, tenantHeader, s))
		return rec.Code, decodeBody(t, rec)
	}

	if code, body := probe(""); code != http.StatusBadRequest || body["code"] != "TENANT_HEADER_REQUIRED" {
		t.Fatalf("no header: %d %v", code, body)
	}
	if code, body := probe("not-a-uuid"); code != http.StatusBadRequest || body["code"] != "TENANT_HEADER_REQUIRED" {
		t.Fatalf("bad header: %d %v", code, body)
	}
	// Before a switch there is no active tenant: the header cannot be trusted alone.
	if code, body := probe(s.tenantA.String()); code != http.StatusForbidden || body["code"] != "TENANT_MISMATCH" {
		t.Fatalf("header without switch: %d %v", code, body)
	}

	// Switching to a tenant without membership, or to a random id, looks identical.
	for _, id := range []string{s.tenantB.String(), uuid.NewString(), "garbage"} {
		rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: cookie, csrf: csrf,
			body: `{"tenantId":"` + id + `"}`})
		if rec.Code != http.StatusForbidden || decodeBody(t, rec)["code"] != "TENANT_ACCESS_DENIED" {
			t.Fatalf("switch to %s: %d %s", id, rec.Code, rec.Body.String())
		}
	}
	// The switch needs CSRF like every other write.
	if rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: cookie, body: `{"tenantId":"` + s.tenantA.String() + `"}`}); rec.Code != http.StatusForbidden {
		t.Fatalf("switch without CSRF: %d", rec.Code)
	}
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: cookie, csrf: csrf,
		body: `{"tenantId":"` + s.tenantA.String() + `"}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("switch: %d %s", rec.Code, rec.Body.String())
	}
	var tc kapsorav1.TenantContext
	if err := json.Unmarshal(rec.Body.Bytes(), &tc); err != nil || tc.Tenant.Code != "HTTP_A" {
		t.Fatalf("switch response = %s err=%v", rec.Body.String(), err)
	}

	if code, body := probe(s.tenantA.String()); code != http.StatusOK || body["tenantId"] != s.tenantA.String() {
		t.Fatalf("probe after switch: %d %v", code, body)
	}
	if code, body := probe(s.tenantB.String()); code != http.StatusForbidden || body["code"] != "TENANT_MISMATCH" {
		t.Fatalf("probe with the other tenant's id: %d %v", code, body)
	}
	// GET /api/v1/session now reports the active tenant.
	sess := decodeBody(t, s.do(call{method: http.MethodGet, path: "/api/v1/session", cookie: cookie}))
	if sess["activeTenantId"] != s.tenantA.String() {
		t.Fatalf("session activeTenantId = %v", sess["activeTenantId"])
	}
}

func TestPermissionDenialsAreAudited(t *testing.T) {
	s := newAuthzServer(t)
	cookie, csrf := s.login(t)
	if rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: cookie, csrf: csrf,
		body: `{"tenantId":"` + s.tenantA.String() + `"}`}); rec.Code != http.StatusOK {
		t.Fatalf("switch: %d", rec.Code)
	}

	rec := s.do(withHeader(call{method: http.MethodGet, path: "/api/v1/probe-admin", cookie: cookie}, identityhttp.TenantHeader, s.tenantA.String(), s))
	if rec.Code != http.StatusForbidden || decodeBody(t, rec)["code"] != "PERMISSION_DENIED" {
		t.Fatalf("denied route: %d %s", rec.Code, rec.Body.String())
	}

	ctx, cancel := s.h.Ctx()
	defer cancel()
	var n int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM audit.event
		 WHERE action_code = 'authorization.denied' AND outcome = 'DENIED' AND tenant_id = $1 AND actor_id = $2
		   AND detail_json->>'permission' = 'organization.manage'`, s.tenantA, s.actor).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("authorization.denied audit rows = %d, want 1", n)
	}
	var switches int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE action_code = 'session.tenant_switch' AND outcome = 'SUCCESS' AND actor_id = $1`, s.actor).Scan(&switches); err != nil {
		t.Fatal(err)
	}
	if switches != 1 {
		t.Fatalf("session.tenant_switch audit rows = %d, want 1", switches)
	}
}

// withHeader adds a header to a call by wrapping the server's transport helper.
func withHeader(c call, name, value string, s *authzServer) call {
	c.headers = map[string]string{name: value}
	_ = s
	return c
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
