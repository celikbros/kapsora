package cataloghttp_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/catalog/application"
	catalogpg "github.com/celikbros/kapsora/internal/catalog/infrastructure/postgres"
	cataloghttp "github.com/celikbros/kapsora/internal/catalog/transport/http"
	"github.com/celikbros/kapsora/internal/identity"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// permsHeader lets each request choose the permissions its actor holds.
const (
	permsHeader    = "X-Test-Permissions"
	readOnly       = "catalog.read"
	allPermissions = "catalog.read,catalog.manage"
	patchType      = "application/merge-patch+json"
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
	tenant  uuid.UUID
	actor   uuid.UUID
}

func newServer(t *testing.T) *server {
	t.Helper()
	h := dbtest.New(t)
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: catalogpg.New(), Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{
		h: h, denied: &denyRecorder{},
		tenant: h.CreateTenant("HTTP_CATALOG"),
		actor:  h.CreateActor("catalog-http-operator", "Catalog Operator"),
	}
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := cataloghttp.NewHandler(svc, s.denied, logger)

	// Stand-in for RequireTenantContext: the permissions come from a test header.
	fakeContext := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := identity.RequestContext{
				TenantID: s.tenant, MembershipID: uuid.New(),
				Principal:   identity.Principal{ActorID: s.actor},
				Permissions: map[string]struct{}{},
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
	mw := cataloghttp.Middlewares{}
	mount("/api/v1/service-categories", func(rr chi.Router) { handler.CategoryRoutes(rr, mw) })
	mount("/api/v1/service-definitions", func(rr chi.Router) { handler.DefinitionRoutes(rr, mw) })
	mount("/api/v1/code-systems", func(rr chi.Router) { handler.CodeSystemRoutes(rr, mw) })
	s.handler = r
	return s
}

type response struct {
	code int
	etag string
	body map[string]any
}

func (s *server) do(t *testing.T, method, path, perms, body string, headers map[string]string) response {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
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

func problemCode(t *testing.T, r response) string {
	t.Helper()
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

func TestCategoryETagRoundTrip(t *testing.T) {
	s := newServer(t)

	created := s.do(t, http.MethodPost, "/api/v1/service-categories", allPermissions,
		`{"code":"PHYSIO","name":"Fizyoterapi","domain":"HEALTH"}`, nil)
	if created.code != http.StatusCreated {
		t.Fatalf("create = %d (%v)", created.code, created.body)
	}
	if created.etag == "" {
		t.Fatal("create did not return an ETag")
	}
	id, _ := created.body["id"].(string)

	got := s.do(t, http.MethodGet, "/api/v1/service-categories/"+id, readOnly, "", nil)
	if got.code != http.StatusOK || got.etag != created.etag {
		t.Fatalf("get = %d, etag %q, want 200 and %q", got.code, got.etag, created.etag)
	}

	patched := s.do(t, http.MethodPatch, "/api/v1/service-categories/"+id, allPermissions,
		`{"name":"Fizyoterapi ve rehabilitasyon"}`,
		map[string]string{"Content-Type": patchType, "If-Match": created.etag})
	if patched.code != http.StatusOK {
		t.Fatalf("patch = %d (%v)", patched.code, patched.body)
	}
	if patched.etag == created.etag {
		t.Fatal("the ETag did not move after the patch")
	}

	// Replaying the old ETag is a lost update.
	stale := s.do(t, http.MethodPatch, "/api/v1/service-categories/"+id, allPermissions,
		`{"name":"Tekrar"}`, map[string]string{"Content-Type": patchType, "If-Match": created.etag})
	if stale.code != http.StatusPreconditionFailed || problemCode(t, stale) != "ETAG_MISMATCH" {
		t.Fatalf("stale patch = %d %q", stale.code, problemCode(t, stale))
	}

	// A patch without If-Match, or with the wrong media type, never reaches the service.
	missing := s.do(t, http.MethodPatch, "/api/v1/service-categories/"+id, allPermissions,
		`{"name":"Tekrar"}`, map[string]string{"Content-Type": patchType})
	if missing.code != http.StatusPreconditionRequired || problemCode(t, missing) != "IF_MATCH_REQUIRED" {
		t.Fatalf("missing If-Match = %d %q", missing.code, problemCode(t, missing))
	}
	wrongType := s.do(t, http.MethodPatch, "/api/v1/service-categories/"+id, allPermissions,
		`{"name":"Tekrar"}`, map[string]string{"If-Match": patched.etag})
	if wrongType.code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong media type = %d", wrongType.code)
	}
}

func TestCodesAreImmutable(t *testing.T) {
	s := newServer(t)

	category := s.do(t, http.MethodPost, "/api/v1/service-categories", allPermissions,
		`{"code":"PHYSIO","name":"Fizyoterapi","domain":"HEALTH"}`, nil)
	categoryID, _ := category.body["id"].(string)

	definition := s.do(t, http.MethodPost, "/api/v1/service-definitions", allPermissions,
		`{"categoryId":"`+categoryID+`","code":"PHYSIO_SESSION","name":"Fizyoterapi seansı",`+
			`"fulfillmentMode":"SESSION","defaultUnitType":"SESSION"}`, nil)
	if definition.code != http.StatusCreated {
		t.Fatalf("create definition = %d (%v)", definition.code, definition.body)
	}
	definitionID, _ := definition.body["id"].(string)

	for _, tc := range []struct {
		name, path, body, etag, wantField string
	}{
		{"category code", "/api/v1/service-categories/" + categoryID, `{"code":"OTHER"}`, category.etag, "code"},
		{"definition code", "/api/v1/service-definitions/" + definitionID, `{"code":"OTHER"}`, definition.etag, "code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := s.do(t, http.MethodPatch, tc.path, allPermissions, tc.body,
				map[string]string{"Content-Type": patchType, "If-Match": tc.etag})
			if got.code != http.StatusUnprocessableEntity || problemCode(t, got) != "VALIDATION_FAILED" {
				t.Fatalf("patch = %d %q", got.code, problemCode(t, got))
			}
			field, code := firstFieldError(t, got)
			if field != tc.wantField || code != "IMMUTABLE" {
				t.Fatalf("field error = %s/%s, want %s/IMMUTABLE", field, code, tc.wantField)
			}
		})
	}

	// The code system carries two immutable fields.
	system := s.do(t, http.MethodPost, "/api/v1/code-systems", allPermissions,
		`{"code":"SUT","name":"Sağlık Uygulama Tebliği","version":"2026","authority":"SGK","validFrom":"2026-01-01"}`, nil)
	if system.code != http.StatusCreated {
		t.Fatalf("create code system = %d (%v)", system.code, system.body)
	}
	systemID, _ := system.body["id"].(string)
	got := s.do(t, http.MethodPatch, "/api/v1/code-systems/"+systemID, allPermissions,
		`{"version":"2027"}`, map[string]string{"Content-Type": patchType, "If-Match": system.etag})
	field, code := firstFieldError(t, got)
	if got.code != http.StatusUnprocessableEntity || field != "version" || code != "IMMUTABLE" {
		t.Fatalf("patch version = %d %s/%s", got.code, field, code)
	}
}

func TestReadPermissionCannotMutate(t *testing.T) {
	s := newServer(t)

	category := s.do(t, http.MethodPost, "/api/v1/service-categories", allPermissions,
		`{"code":"PHYSIO","name":"Fizyoterapi","domain":"HEALTH"}`, nil)
	categoryID, _ := category.body["id"].(string)
	definition := s.do(t, http.MethodPost, "/api/v1/service-definitions", allPermissions,
		`{"categoryId":"`+categoryID+`","code":"PHYSIO_SESSION","name":"Fizyoterapi seansı",`+
			`"fulfillmentMode":"SESSION","defaultUnitType":"SESSION"}`, nil)
	definitionID, _ := definition.body["id"].(string)
	system := s.do(t, http.MethodPost, "/api/v1/code-systems", allPermissions,
		`{"code":"SUT","name":"Sağlık Uygulama Tebliği","version":"2026","authority":"SGK","validFrom":"2026-01-01"}`, nil)
	systemID, _ := system.body["id"].(string)

	mutations := []struct {
		name, method, path, body string
		headers                  map[string]string
	}{
		{"create category", http.MethodPost, "/api/v1/service-categories",
			`{"code":"X","name":"X kategorisi","domain":"HEALTH"}`, nil},
		{"patch category", http.MethodPatch, "/api/v1/service-categories/" + categoryID, `{"name":"X"}`,
			map[string]string{"Content-Type": patchType, "If-Match": category.etag}},
		{"create definition", http.MethodPost, "/api/v1/service-definitions",
			`{"categoryId":"` + categoryID + `","code":"X","name":"X hizmeti","fulfillmentMode":"SESSION","defaultUnitType":"SESSION"}`, nil},
		{"patch definition", http.MethodPatch, "/api/v1/service-definitions/" + definitionID, `{"name":"X"}`,
			map[string]string{"Content-Type": patchType, "If-Match": definition.etag}},
		{"put code mappings", http.MethodPut, "/api/v1/service-definitions/" + definitionID + "/code-mappings",
			`{"items":[]}`, map[string]string{"If-Match": definition.etag}},
		{"create code system", http.MethodPost, "/api/v1/code-systems",
			`{"code":"ICD10","name":"ICD-10","version":"2026","authority":"WHO","validFrom":"2026-01-01"}`, nil},
		{"patch code system", http.MethodPatch, "/api/v1/code-systems/" + systemID, `{"name":"X"}`,
			map[string]string{"Content-Type": patchType, "If-Match": system.etag}},
		{"import code values", http.MethodPost, "/api/v1/code-systems/" + systemID + "/values:import",
			`{"items":[{"code":"A","display":"A","validFrom":"2026-01-01"}]}`, nil},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			got := s.do(t, tc.method, tc.path, readOnly, tc.body, tc.headers)
			if got.code != http.StatusForbidden {
				t.Fatalf("%s with catalog.read = %d (%v)", tc.name, got.code, got.body)
			}
		})
	}
	if len(s.denied.permissions) < len(mutations) {
		t.Fatalf("the denier recorded %d denials, want at least %d", len(s.denied.permissions), len(mutations))
	}

	// Reading stays available with catalog.read alone.
	if got := s.do(t, http.MethodGet, "/api/v1/service-definitions", readOnly, "", nil); got.code != http.StatusOK {
		t.Fatalf("list definitions with catalog.read = %d", got.code)
	}
	if got := s.do(t, http.MethodGet, "/api/v1/code-systems/"+systemID+"/values", readOnly, "", nil); got.code != http.StatusOK {
		t.Fatalf("list code values with catalog.read = %d", got.code)
	}
}

func TestImportAndMappingOverContract(t *testing.T) {
	s := newServer(t)

	category := s.do(t, http.MethodPost, "/api/v1/service-categories", allPermissions,
		`{"code":"PHYSIO","name":"Fizyoterapi","domain":"HEALTH"}`, nil)
	categoryID, _ := category.body["id"].(string)
	definition := s.do(t, http.MethodPost, "/api/v1/service-definitions", allPermissions,
		`{"categoryId":"`+categoryID+`","code":"PHYSIO_SESSION","name":"Fizyoterapi seansı",`+
			`"fulfillmentMode":"SESSION","defaultUnitType":"SESSION"}`, nil)
	definitionID, _ := definition.body["id"].(string)
	system := s.do(t, http.MethodPost, "/api/v1/code-systems", allPermissions,
		`{"code":"SUT","name":"Sağlık Uygulama Tebliği","version":"2026","authority":"SGK","validFrom":"2026-01-01"}`, nil)
	systemID, _ := system.body["id"].(string)

	imported := s.do(t, http.MethodPost, "/api/v1/code-systems/"+systemID+"/values:import", allPermissions,
		`{"items":[{"code":"P701010","display":"Fizik tedavi","validFrom":"2026-01-01","attributes":{"unit":"SESSION"}}]}`, nil)
	if imported.code != http.StatusOK {
		t.Fatalf("import = %d (%v)", imported.code, imported.body)
	}
	if imported.body["created"] != float64(1) {
		t.Fatalf("import summary = %v", imported.body)
	}

	values := s.do(t, http.MethodGet, "/api/v1/code-systems/"+systemID+"/values?asOf=2026-06-01", readOnly, "", nil)
	if values.code != http.StatusOK {
		t.Fatalf("read values = %d (%v)", values.code, values.body)
	}
	if values.body["asOf"] != "2026-06-01" {
		t.Fatalf("asOf echoed as %v", values.body["asOf"])
	}

	// Two rows for the same code over overlapping periods are refused with the named code.
	overlap := s.do(t, http.MethodPut, "/api/v1/service-definitions/"+definitionID+"/code-mappings", allPermissions,
		`{"items":[{"codeSystemId":"`+systemID+`","code":"P701010","validFrom":"2026-01-01"},`+
			`{"codeSystemId":"`+systemID+`","code":"P701010","validFrom":"2026-06-01"}]}`,
		map[string]string{"If-Match": definition.etag})
	if overlap.code != http.StatusConflict || problemCode(t, overlap) != "CODE_MAPPING_OVERLAP" {
		t.Fatalf("overlapping mappings = %d %q", overlap.code, problemCode(t, overlap))
	}

	stored := s.do(t, http.MethodPut, "/api/v1/service-definitions/"+definitionID+"/code-mappings", allPermissions,
		`{"items":[{"codeSystemId":"`+systemID+`","code":"P701010","validFrom":"2026-01-01","primary":true}]}`,
		map[string]string{"If-Match": definition.etag})
	if stored.code != http.StatusOK {
		t.Fatalf("put mappings = %d (%v)", stored.code, stored.body)
	}
	if stored.etag == definition.etag {
		t.Fatal("replacing the mapping set must move the definition ETag")
	}
}

func TestCategoryCycleAnswersNamedProblem(t *testing.T) {
	s := newServer(t)

	root := s.do(t, http.MethodPost, "/api/v1/service-categories", allPermissions,
		`{"code":"L1","name":"Birinci seviye","domain":"HEALTH"}`, nil)
	rootID, _ := root.body["id"].(string)
	child := s.do(t, http.MethodPost, "/api/v1/service-categories", allPermissions,
		`{"code":"L2","name":"İkinci seviye","domain":"HEALTH","parentId":"`+rootID+`"}`, nil)
	childID, _ := child.body["id"].(string)

	got := s.do(t, http.MethodPatch, "/api/v1/service-categories/"+rootID, allPermissions,
		`{"parentId":"`+childID+`"}`, map[string]string{"Content-Type": patchType, "If-Match": root.etag})
	if got.code != http.StatusConflict || problemCode(t, got) != "CATEGORY_CYCLE" {
		t.Fatalf("cycle = %d %q (%v)", got.code, problemCode(t, got), got.body)
	}
}
