package tenantsettingshttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/tenantsettings/application"
)

type stubRepository struct{ saves int }

func (*stubRepository) Read(context.Context, identity.RequestContext) (application.Response, error) {
	return application.Response{Status: 200, ETag: `"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"`, Body: []byte(`{"canManage":true}`)}, nil
}
func (s *stubRepository) Save(context.Context, identity.RequestContext, application.Command) (application.Response, error) {
	s.saves++
	return application.Response{Status: 200, ETag: `"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"`, Body: []byte(`{"canManage":true}`)}, nil
}
func (*stubRepository) Preflight(context.Context, identity.RequestContext, application.Command) (*application.Response, error) {
	return nil, nil
}

func TestRoutesRequireSettingsAuthorityAndPreconditions(t *testing.T) {
	repo := &stubRepository{}
	h := NewHandler(application.NewService(repo))
	router := chi.NewRouter()
	h.Routes(router, func(next http.Handler) http.Handler { return next })
	request := func(method, body string, rc *identity.RequestContext) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", `"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"`)
		req.Header.Set("Idempotency-Key", "settings-http-0001")
		if rc != nil {
			req = req.WithContext(identity.WithRequestContext(req.Context(), *rc))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	body := `{"holdMinutes":15,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT"}`
	if w := request(http.MethodGet, "", nil); w.Code != 401 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("anonymous GET %d %v", w.Code, w.Header())
	}
	rc := identity.RequestContext{Principal: identity.Principal{ActorID: uuid.New()}, Permissions: map[string]struct{}{"identity.user.read": {}}}
	if w := request(http.MethodGet, "", &rc); w.Code != 403 {
		t.Fatalf("directory-only GET %d", w.Code)
	}
	rc.Permissions = map[string]struct{}{"platform.tenant_settings.read": {}}
	if w := request(http.MethodGet, "", &rc); w.Code != 200 || w.Header().Get("ETag") == "" {
		t.Fatalf("settings read %d %v", w.Code, w.Header())
	}
	if w := request(http.MethodPut, body, &rc); w.Code != 403 || repo.saves != 0 {
		t.Fatalf("read-only PUT %d saves=%d", w.Code, repo.saves)
	}
	rc.Permissions["platform.tenant_settings.manage"] = struct{}{}
	rc.StepUpValid = true
	if w := request(http.MethodPut, body, &rc); w.Code != 200 || repo.saves != 1 {
		t.Fatalf("authorized PUT %d saves=%d body=%s", w.Code, repo.saves, w.Body.String())
	}
	if w := request(http.MethodPut, `{"holdMinutes":15,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT","unknown":true}`, &rc); w.Code != 422 || repo.saves != 1 {
		t.Fatalf("invalid PUT %d saves=%d", w.Code, repo.saves)
	}
}

func TestStrictAccommodationBody(t *testing.T) {
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{`{"holdMinutes":1,"quoteTtlMinutes":1440,"reasonCode":"OPERATING_REQUIREMENT"}`, true},
		{`{"holdMinutes":1440,"quoteTtlMinutes":1,"reasonCode":"CORRECT_CONFIGURATION"}`, true},
		{`{"holdMinutes":0,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT"}`, false},
		{`{"holdMinutes":1441,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT"}`, false},
		{`{"holdMinutes":1.0,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT"}`, false},
		{`{"holdMinutes":true,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT"}`, false},
		{`{"holdMinutes":"15","quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT"}`, false},
		{`{"holdMinutes":null,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT"}`, false},
		{`{"holdMinutes":999999999999999999999999999,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT"}`, false},
		{`{"holdMinutes":15,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT","settingKey":"other"}`, false},
		{`{"holdMinutes":15,"holdMinutes":16,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT"}`, false},
		{`{"holdMinutes":15,"quoteTtlMinutes":60,"reasonCode":"OTHER"}`, false},
		{`{"holdMinutes":15,"reasonCode":"OPERATING_REQUIREMENT"}`, false},
		{`{"holdMinutes":15,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT"} {}`, false},
	} {
		_, err := strictBody([]byte(tc.body))
		if (err == nil) != tc.ok {
			t.Errorf("body %s valid=%t err=%v", tc.body, tc.ok, err)
		}
	}
}
