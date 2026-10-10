package application_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/claim/application"
	claimhttp "github.com/celikbros/kapsora/internal/claim/transport/http"
	"github.com/celikbros/kapsora/internal/identity"
)

type sourceDenier struct{}

func (sourceDenier) Deny(w http.ResponseWriter, _ *http.Request, _ error, _ string) {
	w.WriteHeader(http.StatusForbidden)
}

func TestCaseSourceHTTPProjectionAndPermission(t *testing.T) {
	f, _, report := sourceFixture(t)
	router := chi.NewRouter()
	claimhttp.NewHandler(f.claims, sourceDenier{}, nil).Routes(router, claimhttp.Middlewares{})
	call := func(rc identity.RequestContext, method, path, body, etag string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(identity.WithRequestContext(req.Context(), rc))
		req.Header.Set("Content-Type", "application/json")
		if etag != "" {
			req.Header.Set("If-Match", etag)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	path := "/case-sources/" + f.caseID.String()
	rc := f.providerRC()
	denied := rc
	denied.Permissions = map[string]struct{}{}
	body := fmt.Sprintf(`{"lines":[{"serviceDefinitionId":%q,"quantity":"1","lineAmount":"250"}]}`, f.physio.String())
	for _, route := range []struct{ method, path, body string }{{"GET", "/case-sources", ""}, {"GET", path, ""}, {"POST", path, body}} {
		if out := call(denied, route.method, route.path, route.body, `"1"`); out.Code != 403 {
			t.Fatalf("permission %s %s: %d", route.method, route.path, out.Code)
		}
	}
	foreign := rc
	foreign.Scopes = []identity.Scope{{Type: application.ScopeOrganization, ID: uuid.NullUUID{UUID: f.otherOrg, Valid: true}}}
	if out := call(foreign, "GET", path, "", ""); out.Code != 404 {
		t.Fatalf("foreign detail %d", out.Code)
	}
	if out := call(foreign, "POST", path, body, `"1"`); out.Code != 404 {
		t.Fatalf("foreign create %d", out.Code)
	}
	detail := call(rc, "GET", path, "", "")
	if detail.Code != 200 || detail.Header().Get("ETag") == "" || detail.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("detail status=%d", detail.Code)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(detail.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || payload["source"] == nil || payload["lines"] == nil {
		t.Fatal("unexpected source fields")
	}
	for _, secret := range []string{report.String(), f.diagnosisID.String(), "diagnosisId", "medicalReportId", "authorizationId"} {
		if strings.Contains(detail.Body.String(), secret) {
			t.Fatal("clinical source reference leaked")
		}
	}
	if out := call(rc, "POST", path, body, ""); out.Code != 428 {
		t.Fatalf("missing ETag=%d", out.Code)
	}
	created := call(rc, "POST", path, body, detail.Header().Get("ETag"))
	if created.Code != 201 {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	for _, secret := range []string{report.String(), f.diagnosisID.String(), "diagnosisId", "medicalReportId"} {
		if strings.Contains(created.Body.String(), secret) {
			t.Fatal("clinical claim reference leaked")
		}
	}
	if out := call(rc, "POST", path, body, detail.Header().Get("ETag")); out.Code != 409 {
		t.Fatalf("duplicate=%d", out.Code)
	}
}
