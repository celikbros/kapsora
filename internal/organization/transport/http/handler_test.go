package organizationhttp_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/identity"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/organization/application"
	"github.com/celikbros/kapsora/internal/organization/domain/domaintest"
	organizationpg "github.com/celikbros/kapsora/internal/organization/infrastructure/postgres"
	organizationhttp "github.com/celikbros/kapsora/internal/organization/transport/http"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// permsHeader lets a test choose the caller's permissions per request.
const permsHeader = "X-Test-Permissions"

type denyRecorder struct{ permissions []string }

func (d *denyRecorder) Deny(w http.ResponseWriter, r *http.Request, err error, permission string) {
	d.permissions = append(d.permissions, permission)
	identityhttp.WriteAuthError(w, r, err, nil)
}

type server struct {
	h       *dbtest.Harness
	handler http.Handler
	denied  *denyRecorder
	tenant  uuid.UUID
	rand    *rand.Rand
}

func newServer(t *testing.T) *server {
	t.Helper()
	h := dbtest.New(t)
	keys, _ := localkey.New([]byte("0123456789abcdef0123456789abcdef"))
	cursors, _ := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	svc, err := application.New(application.Deps{Pool: h.App, Repo: organizationpg.New(), Cipher: keys, Index: keys, Cursors: cursors})
	if err != nil {
		t.Fatal(err)
	}
	tenant := h.CreateTenant("HTTP_ORG")
	actor := h.CreateActor("org-http", "Org HTTP")
	denied := &denyRecorder{}
	handler := organizationhttp.NewHandler(svc, denied, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Stand-in for RequireTenantContext: the permissions come from a test header.
	fakeContext := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := identity.RequestContext{TenantID: tenant, MembershipID: uuid.New(), Principal: identity.Principal{ActorID: actor}, Permissions: map[string]struct{}{}}
			for _, p := range strings.Split(r.Header.Get(permsHeader), ",") {
				if p != "" {
					rc.Permissions[p] = struct{}{}
				}
			}
			next.ServeHTTP(w, r.WithContext(identity.WithRequestContext(r.Context(), rc)))
		})
	}
	passthrough := func(next http.Handler) http.Handler { return next }

	r := chi.NewRouter()
	r.Route("/api/v1/organizations", func(rr chi.Router) {
		rr.Use(fakeContext)
		handler.Routes(rr, passthrough)
	})
	return &server{h: h, handler: r, denied: denied, tenant: tenant, rand: rand.New(rand.NewPCG(9, 10))}
}

type call struct {
	method, path, body, contentType, ifMatch, perms string
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
		c.perms = "organization.read,organization.manage"
	}
	req.Header.Set(permsHeader, c.perms)
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func (s *server) createBody(name, vkn string) string {
	b, _ := json.Marshal(map[string]any{
		"legalName": name + " A.Ş.", "displayName": name, "organizationKind": "PROVIDER", "relationshipRole": "PROVIDER",
		"identifiers": []map[string]any{{"type": "VKN", "value": vkn, "primary": true}},
	})
	return string(b)
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func TestCreateGetAndETag(t *testing.T) {
	s := newServer(t)
	vkn := domaintest.GenerateVKN(s.rand)

	rec := s.do(call{method: http.MethodPost, path: "/api/v1/organizations", body: s.createBody("Şehir Hastanesi", vkn)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != `"1"` || !strings.HasPrefix(rec.Header().Get("Location"), "/api/v1/organizations/") {
		t.Fatalf("headers = %v", rec.Header())
	}
	var org kapsorav1.Organization
	decode(t, rec, &org)
	if org.DisplayName != "Şehir Hastanesi" || org.RowVersion != 1 || len(org.Identifiers) != 1 || org.Identifiers[0].MaskedValue == vkn {
		t.Fatalf("organization = %+v", org)
	}
	if strings.Contains(rec.Body.String(), vkn) {
		t.Fatal("full VKN in response body")
	}

	rec = s.do(call{method: http.MethodGet, path: "/api/v1/organizations/" + org.Id.String()})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d etag=%q", rec.Code, rec.Header().Get("ETag"))
	}
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/organizations/" + uuid.NewString()})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/organizations/not-a-uuid"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("malformed id must look like unknown: %d", rec.Code)
	}
}

func TestPermissionDenialsGoThroughTheDenier(t *testing.T) {
	s := newServer(t)
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/organizations", body: s.createBody("Yetkisiz", domaintest.GenerateVKN(s.rand)), perms: "organization.read"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create without manage: %d", rec.Code)
	}
	var p map[string]any
	decode(t, rec, &p)
	if p["code"] != "PERMISSION_DENIED" || len(s.denied.permissions) != 1 || s.denied.permissions[0] != "organization.manage" {
		t.Fatalf("denial = %v recorded=%v", p, s.denied.permissions)
	}
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/organizations", perms: "audit.read"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("list without read: %d", rec.Code)
	}
}

func TestValidationPayloads(t *testing.T) {
	s := newServer(t)
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/organizations", body: s.createBody("Hatalı VKN", "1234567891")})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad vkn: %d %s", rec.Code, rec.Body.String())
	}
	var p struct {
		Code   string `json:"code"`
		Errors []struct {
			Field string `json:"field"`
			Code  string `json:"code"`
		} `json:"errors"`
	}
	decode(t, rec, &p)
	if p.Code != "VALIDATION_FAILED" || len(p.Errors) != 1 || p.Errors[0].Field != "identifiers[0].value" || p.Errors[0].Code != "IDENTIFIER_INVALID" {
		t.Fatalf("problem = %+v", p)
	}
	if rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("content type = %q", rec.Header().Get("Content-Type"))
	}

	rec = s.do(call{method: http.MethodPost, path: "/api/v1/organizations", body: `{"legalName":"X A.Ş.","unknown":1}`})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/organizations?cursor=bogus"})
	var cp map[string]any
	decode(t, rec, &cp)
	if rec.Code != http.StatusBadRequest || cp["code"] != "CURSOR_INVALID" {
		t.Fatalf("bad cursor: %d %v", rec.Code, cp)
	}
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/organizations?limit=abc"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad limit: %d", rec.Code)
	}
}

func TestPatchPreconditionsAndMergeSemantics(t *testing.T) {
	s := newServer(t)
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/organizations", body: s.createBody("Güncellenecek", domaintest.GenerateVKN(s.rand))})
	var org kapsorav1.Organization
	decode(t, rec, &org)
	path := "/api/v1/organizations/" + org.Id.String()
	patchType := "application/merge-patch+json"

	rec = s.do(call{method: http.MethodPatch, path: path, contentType: "application/json", ifMatch: `"1"`, body: `{"tenantCode":"A"}`})
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content type: %d", rec.Code)
	}
	rec = s.do(call{method: http.MethodPatch, path: path, contentType: patchType, body: `{"tenantCode":"A"}`})
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: %d", rec.Code)
	}
	rec = s.do(call{method: http.MethodPatch, path: path, contentType: patchType, ifMatch: `"7"`, body: `{"tenantCode":"A"}`})
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodPatch, path: path, contentType: patchType, ifMatch: `"1"`, body: `{"tenantCode":"A","bogus":true}`})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown patch field: %d", rec.Code)
	}
	rec = s.do(call{method: http.MethodPatch, path: path, contentType: patchType, ifMatch: `W/"1"`, body: `{"tenantCode":"HOSP-1","relationshipStatus":"SUSPENDED"}`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("patch: %d etag=%q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	decode(t, rec, &org)
	if org.TenantCode == nil || *org.TenantCode != "HOSP-1" || string(org.RelationshipStatus) != "SUSPENDED" {
		t.Fatalf("patched = %+v", org)
	}
	rec = s.do(call{method: http.MethodPatch, path: path, contentType: patchType, ifMatch: `"2"`, body: `{"tenantCode":null}`})
	var cleared kapsorav1.Organization
	decode(t, rec, &cleared)
	if rec.Code != http.StatusOK || cleared.TenantCode != nil {
		t.Fatalf("null clears tenant code: %d %+v", rec.Code, cleared)
	}
}

func TestListRoundTripThroughCursors(t *testing.T) {
	s := newServer(t)
	for i := 0; i < 3; i++ {
		if rec := s.do(call{method: http.MethodPost, path: "/api/v1/organizations", body: s.createBody("Liste "+string(rune('A'+i)), domaintest.GenerateVKN(s.rand))}); rec.Code != http.StatusCreated {
			t.Fatalf("create %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 5; pages++ {
		path := "/api/v1/organizations?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		rec := s.do(call{method: http.MethodGet, path: path})
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		var page kapsorav1.OrganizationPage
		decode(t, rec, &page)
		for _, it := range page.Items {
			if seen[it.Id.String()] {
				t.Fatalf("duplicate %s", it.Id)
			}
			seen[it.Id.String()] = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 3 {
		t.Fatalf("listed %d relationships, want 3", len(seen))
	}
}
