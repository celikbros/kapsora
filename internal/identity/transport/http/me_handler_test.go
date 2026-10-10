package identityhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/audit"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/idempotency"
	providerapp "github.com/celikbros/kapsora/internal/provider/application"
	providerpg "github.com/celikbros/kapsora/internal/provider/infrastructure/postgres"
	providerhttp "github.com/celikbros/kapsora/internal/provider/transport/http"
	rulesapp "github.com/celikbros/kapsora/internal/rules/application"
	rulespg "github.com/celikbros/kapsora/internal/rules/infrastructure/postgres"
	ruleshttp "github.com/celikbros/kapsora/internal/rules/transport/http"
)

type authzServer struct {
	*server
	svc            *application.Service
	prov           *application.Provisioner
	actor          uuid.UUID
	tenantA        uuid.UUID
	tenantB        uuid.UUID
	invitationRepo *identitypg.InvitationRepository
	invitationKeys *localkey.Provider
}

// newAuthzServer mirrors the production router: session loading, CSRF, the pre-tenant
// routes and a tenant-scoped group with two probe routes.
func newAuthzServer(t *testing.T, recorder ...audit.Recorder) *authzServer {
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
	cursors, err := httpx.NewCursorCodec(signingKey)
	if err != nil {
		t.Fatal(err)
	}
	directoryHandler := identityhttp.NewDirectoryHandler(
		application.NewDirectoryService(identitypg.NewDirectoryRepository(h.App, recorder...), cursors), mw, logger)
	roleHandler := identityhttp.NewRoleAssignmentHandler(
		application.NewRoleAssignmentService(identitypg.NewRoleAssignmentRepository(h.App, recorder...), cursors), mw, logger)
	roleChangeHandler := identityhttp.NewPrivilegedRoleChangeHandler(
		application.NewPrivilegedRoleChangeService(identitypg.NewPrivilegedRoleChangeRepository(h.App, recorder...), cursors), mw, logger)
	ruleService, err := rulesapp.New(rulesapp.Deps{Pool: h.App, Repo: rulespg.New(), Audit: auditpg.New(), Cursors: cursors})
	if err != nil {
		t.Fatal(err)
	}
	ruleHandler := ruleshttp.NewHandler(ruleService, mw, logger)
	invitationKeys, err := localkey.New(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	invitationRepo := identitypg.NewInvitationRepository(h.App, invitationKeys, invitationKeys, recorder...).WithDeliveryEnabled(true)
	invitationHandler := identityhttp.NewInvitationHandler(application.NewInvitationService(invitationRepo), mw, cursors, logger)
	providerService, err := providerapp.New(providerapp.Deps{Pool: h.App, Repo: providerpg.New(), Cipher: invitationKeys,
		Index: invitationKeys, Audit: auditpg.New(), Cursors: cursors})
	if err != nil {
		t.Fatal(err)
	}
	providerHandler := providerhttp.NewHandler(providerService, mw, logger)

	r := chi.NewRouter()
	r.Group(func(anon chi.Router) {
		invitationHandler.AnonymousRecipientRoutes(anon, "http://127.0.0.1:5181")
	})
	r.Route("/api/v1", func(api chi.Router) {
		api.Use(mw.LoadSession)
		api.Post("/session/login", sessionHandler.Login)
		api.Group(func(authed chi.Router) {
			authed.Use(mw.RequireCSRF)
			authed.Get("/session", sessionHandler.GetSession)
			authed.Post("/session/switch-tenant", contextHandler.SwitchTenant)
			authed.Post("/session/step-up", sessionHandler.StepUp)
			authed.Get("/me", contextHandler.GetMe)
			authed.Get("/tenants", contextHandler.ListTenants)
			authed.Route("/invitations", invitationHandler.RecipientRoutes)
		})
		api.Group(func(tenant chi.Router) {
			tenant.Use(mw.RequireCSRF)
			tenant.Use(mw.RequireTenantContext)
			tenant.Route("/admin/users", func(r chi.Router) {
				directoryHandler.Routes(r, idempotency.Middleware(h.App, idempotency.Options{
					CommandCode: "tenant_membership.suspend",
					Scope: func(req *http.Request) (idempotency.Scope, bool) {
						rc, ok := identity.FromContext(req.Context())
						return idempotency.Scope{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, ok
					},
					HashHeaders: []string{"If-Match"},
				}))
				roleScope := func(req *http.Request) (idempotency.Scope, bool) {
					rc, ok := identity.FromContext(req.Context())
					return idempotency.Scope{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, ok
				}
				roleHandler.UserRoutes(r,
					idempotency.Middleware(h.App, idempotency.Options{CommandCode: "access_grant.assign", Scope: roleScope, HashHeaders: []string{"If-Match"}}),
					idempotency.Middleware(h.App, idempotency.Options{CommandCode: "access_grant.revoke", Scope: roleScope, HashHeaders: []string{"If-Match"}}))
				roleChangeHandler.UserRoutes(r, idempotency.Middleware(h.App, idempotency.Options{CommandCode: "role_change.create", Scope: roleScope, HashHeaders: []string{"If-Match"}, BeforeStoredResult: roleChangeHandler.StoredResultGate}))
			})
			tenant.Route("/admin", func(r chi.Router) {
				roleHandler.CatalogRoutes(r)
				roleScope := func(req *http.Request) (idempotency.Scope, bool) {
					rc, ok := identity.FromContext(req.Context())
					return idempotency.Scope{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, ok
				}
				roleChangeHandler.CatalogRoutes(r,
					idempotency.Middleware(h.App, idempotency.Options{CommandCode: "role_change.approve", Scope: roleScope, HashHeaders: []string{"If-Match"}, BeforeStoredResult: roleChangeHandler.StoredResultGate}),
					idempotency.Middleware(h.App, idempotency.Options{CommandCode: "role_change.reject", Scope: roleScope, HashHeaders: []string{"If-Match"}, BeforeStoredResult: roleChangeHandler.StoredResultGate}),
					idempotency.Middleware(h.App, idempotency.Options{CommandCode: "role_change.cancel", Scope: roleScope, HashHeaders: []string{"If-Match"}, BeforeStoredResult: roleChangeHandler.StoredResultGate}))
			})
			tenant.Route("/rule-sets", func(r chi.Router) { ruleHandler.RuleSetRoutes(r, ruleshttp.Middlewares{}) })
			tenant.Route("/providers", func(r chi.Router) { providerHandler.ProviderRoutes(r, providerhttp.Middlewares{}) })
			tenant.Route("/admin/invitations", func(r chi.Router) {
				invitationHandler.ManagerRoutes(r, idempotency.Middleware(h.App, idempotency.Options{
					CommandCode: "tenant_invitation.cancel",
					Scope: func(req *http.Request) (idempotency.Scope, bool) {
						rc, ok := identity.FromContext(req.Context())
						return idempotency.Scope{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, ok
					},
					HashHeaders: []string{"If-Match"},
				}))
			})
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
			tenant.Get("/probe-rule", func(w http.ResponseWriter, r *http.Request) {
				if _, err := identity.Require(r.Context(), "rule.read"); err != nil {
					mw.Deny(w, r, err, "rule.read")
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
			tenant.Get("/probe-provider/{relationshipId}", func(w http.ResponseWriter, r *http.Request) {
				rc, err := identity.Require(r.Context(), "provider.read")
				if err != nil {
					mw.Deny(w, r, err, "provider.read")
					return
				}
				want, err := uuid.Parse(chi.URLParam(r, "relationshipId"))
				if err != nil {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				for _, scope := range rc.Scopes {
					if scope.Type == application.ScopeOrganization && scope.ID.Valid && scope.ID.UUID == want {
						w.WriteHeader(http.StatusNoContent)
						return
					}
				}
				w.WriteHeader(http.StatusForbidden)
			})
		})
	})
	return &authzServer{
		server:         &server{h: h, handler: r, cookies: cookies},
		svc:            svc,
		prov:           prov,
		actor:          actor,
		tenantA:        tenantA,
		tenantB:        tenantB,
		invitationRepo: invitationRepo,
		invitationKeys: invitationKeys,
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

// The app a request names narrows the account to that app's grants, on /me and on every
// tenant route; an app the server does not know is refused rather than ignored.
func TestTheAppHeaderNarrowsWhatAnAccountMayDo(t *testing.T) {
	s := newAuthzServer(t)
	cookie, csrf := s.login(t)
	if rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: cookie, csrf: csrf,
		body: `{"tenantId":"` + s.tenantA.String() + `"}`}); rec.Code != http.StatusOK {
		t.Fatalf("switch: %d %s", rec.Code, rec.Body.String())
	}
	get := func(path string, headers map[string]string) *httptest.ResponseRecorder {
		return s.do(call{method: http.MethodGet, path: path, cookie: cookie, headers: headers})
	}
	probe := func(app string) *httptest.ResponseRecorder {
		return get("/api/v1/probe", map[string]string{identityhttp.TenantHeader: s.tenantA.String(), identity.AppHeader: app})
	}

	for _, rec := range []*httptest.ResponseRecorder{
		get("/api/v1/me", map[string]string{identity.AppHeader: "admin"}),
		probe("admin"),
	} {
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != "APP_HEADER_INVALID" {
			t.Fatalf("unknown app: %d %s", rec.Code, rec.Body.String())
		}
	}

	// The AUDITOR grant is tenant-wide, so it is the backoffice's: the backoffice may probe,
	// the member app and the provider portal may not, and naming no app still may.
	if rec := probe("backoffice"); rec.Code != http.StatusOK {
		t.Fatalf("backoffice probe: %d %s", rec.Code, rec.Body.String())
	}
	for _, app := range []string{"member", "provider"} {
		if rec := probe(app); rec.Code != http.StatusForbidden || decodeBody(t, rec)["code"] != "PERMISSION_DENIED" {
			t.Fatalf("%s probe: %d %s", app, rec.Code, rec.Body.String())
		}
	}
	if rec := probe(""); rec.Code != http.StatusOK {
		t.Fatalf("probe naming no app: %d %s", rec.Code, rec.Body.String())
	}

	// /me answers for the asking app and lists, whichever app asks, where the account works.
	rec := get("/api/v1/me", map[string]string{identity.AppHeader: "member"})
	var me kapsorav1.UserContext
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || len(me.Tenants) != 1 {
		t.Fatalf("/me as the member app = %s err=%v", rec.Body.String(), err)
	}
	tc := me.Tenants[0]
	if len(tc.Permissions) != 0 {
		t.Fatalf("the member app got permissions %v", tc.Permissions)
	}
	if len(tc.Apps) != 1 || string(tc.Apps[0]) != "backoffice" || tc.SelfPersonId != nil {
		t.Fatalf("apps = %v self = %v, want [backoffice] and no person", tc.Apps, tc.SelfPersonId)
	}
}
