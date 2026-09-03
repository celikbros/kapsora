package identityhttp_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

const (
	testPassword = "çilekli düğün pastası 2026"
	username     = "ahmet@example.test"
)

var signingKey = []byte("0123456789abcdef0123456789abcdef")

type server struct {
	h       *dbtest.Harness
	handler http.Handler
	cookies identityhttp.CookieConfig
}

func newServer(t *testing.T, secureCookie bool) *server {
	t.Helper()
	h := dbtest.New(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	svc, err := application.New(application.Deps{
		Credentials: identitypg.NewCredentialRepository(h.App),
		Sessions:    identitypg.NewSessionStore(h.App),
		Policy:      domain.DefaultPolicy(),
		Lockout:     domain.DefaultLockout(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateAccount(context.Background(), username, "Test Kullanıcı", username, testPassword, false); err != nil {
		t.Fatal(err)
	}

	cookies := identityhttp.CookieConfig{Secure: secureCookie}
	mw := identityhttp.NewMiddleware(svc, cookies, signingKey, logger)
	handler := identityhttp.NewHandler(svc, cookies, signingKey, logger)

	r := chi.NewRouter()
	r.Route("/api/v1/session", func(s chi.Router) {
		s.Use(mw.LoadSession)
		s.Post("/login", handler.Login)
		s.Group(func(authed chi.Router) {
			authed.Use(mw.RequireCSRF)
			authed.Get("/", handler.GetSession)
			authed.Post("/logout", handler.Logout)
			authed.Post("/step-up", handler.StepUp)
			authed.Post("/password", handler.ChangePassword)
		})
	})
	return &server{h: h, handler: r, cookies: cookies}
}

type call struct {
	method, path, body string
	cookie             *http.Cookie
	csrf               string
	headers            map[string]string
}

func (s *server) do(c call) *httptest.ResponseRecorder {
	var body io.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
	}
	req := httptest.NewRequest(c.method, c.path, body)
	req.Header.Set("Content-Type", "application/json")
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.csrf != "" {
		req.Header.Set(identityhttp.CSRFHeader, c.csrf)
	}
	for k, v := range c.headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("cookie %q not set; got %v", name, rec.Result().Cookies())
	return nil
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

// login authenticates. existing simulates the browser resending a session cookie it
// already holds; pass nil for a fresh client.
func (s *server) login(t *testing.T, existing ...*http.Cookie) (*http.Cookie, string) {
	t.Helper()
	c := call{method: http.MethodPost, path: "/api/v1/session/login",
		body: `{"username":"` + username + `","password":"` + testPassword + `"}`}
	if len(existing) > 0 {
		c.cookie = existing[0]
	}
	rec := s.do(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookie(t, rec, s.cookies.Name())
	csrf, _ := decodeBody(t, rec)["csrfToken"].(string)
	if csrf == "" {
		t.Fatal("login response carries no csrfToken")
	}
	return cookie, csrf
}

func TestLoginSetsAHardenedCookieAndReturnsTheCSRFToken(t *testing.T) {
	s := newServer(t, true)
	cookie, csrf := s.login(t)

	if cookie.Name != identityhttp.SecureCookieName {
		t.Fatalf("cookie name = %q", cookie.Name)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("cookie is not hardened: %+v", cookie)
	}
	if cookie.MaxAge != 0 || !cookie.Expires.IsZero() {
		t.Fatalf("session cookie must not be persistent: %+v", cookie)
	}
	if len(cookie.Value) < 43 {
		t.Fatalf("session id looks too short: %q", cookie.Value)
	}
	// The CSRF token is derived from the session id, never equal to it.
	if csrf == cookie.Value {
		t.Fatal("the CSRF token must not be the session id")
	}
	if csrf != domain.DeriveCSRFToken(signingKey, cookie.Value) {
		t.Fatal("the CSRF token is not the derived one")
	}
}

func TestLocalDevelopmentUsesTheUnprefixedCookieName(t *testing.T) {
	s := newServer(t, false)
	cookie, _ := s.login(t)
	if cookie.Name != identityhttp.InsecureCookieName || cookie.Secure {
		t.Fatalf("insecure configuration cookie = %+v", cookie)
	}
}

func TestWrongPasswordReturns401AndNoCookie(t *testing.T) {
	s := newServer(t, true)
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/login",
		body: `{"username":"` + username + `","password":"tamamen başka bir parola"}`})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q", ct)
	}
	if body := decodeBody(t, rec); body["code"] != "INVALID_CREDENTIALS" {
		t.Fatalf("body = %v", body)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatalf("a failed login set cookies: %v", rec.Result().Cookies())
	}
}

func TestGetSessionRequiresTheCookie(t *testing.T) {
	s := newServer(t, true)

	rec := s.do(call{method: http.MethodGet, path: "/api/v1/session"})
	if rec.Code != http.StatusUnauthorized || decodeBody(t, rec)["code"] != "UNAUTHENTICATED" {
		t.Fatalf("without a cookie: %d %s", rec.Code, rec.Body.String())
	}

	cookie, csrf := s.login(t)
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/session", cookie: cookie})
	if rec.Code != http.StatusOK {
		t.Fatalf("with a cookie: %d %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["csrfToken"] != csrf {
		t.Fatalf("csrfToken changed between calls: %v vs %v", body["csrfToken"], csrf)
	}
	if body["activeTenantId"] != nil || body["stepUpExpiresAt"] != nil {
		t.Fatalf("unexpected session body: %v", body)
	}
	// A page load reports the same profile fields the login did.
	if body["displayName"] != "Test Kullanıcı" || body["mustChangePassword"] != false {
		t.Fatalf("profile fields missing on reload: %v", body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("session responses must not be cached")
	}
}

func TestStateChangingCallsNeedTheCSRFToken(t *testing.T) {
	s := newServer(t, true)
	cookie, csrf := s.login(t)

	rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/logout", cookie: cookie})
	if rec.Code != http.StatusForbidden || decodeBody(t, rec)["code"] != "CSRF_TOKEN_INVALID" {
		t.Fatalf("without a token: %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/session/logout", cookie: cookie, csrf: csrf + "x"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("with a wrong token: %d", rec.Code)
	}
	// A token from another session is rejected too.
	otherCookie, otherCSRF := s.login(t)
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/session/logout", cookie: cookie, csrf: otherCSRF})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("with another session's token: %d", rec.Code)
	}
	_ = otherCookie

	rec = s.do(call{method: http.MethodPost, path: "/api/v1/session/logout", cookie: cookie, csrf: csrf})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("with the right token: %d %s", rec.Code, rec.Body.String())
	}
	cleared := sessionCookie(t, rec, s.cookies.Name())
	if cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Fatalf("logout did not clear the cookie: %+v", cleared)
	}
	// The session is really gone.
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/session", cookie: cookie})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("session survived logout: %d", rec.Code)
	}
}

func TestStaleCookieIsClearedAndTreatedAsAnonymous(t *testing.T) {
	s := newServer(t, true)
	stale := &http.Cookie{Name: s.cookies.Name(), Value: "there-is-no-such-session-id-at-all"}

	rec := s.do(call{method: http.MethodGet, path: "/api/v1/session", cookie: stale})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	cleared := sessionCookie(t, rec, s.cookies.Name())
	if cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Fatalf("stale cookie not cleared: %+v", cleared)
	}
}

func TestStepUpAndPasswordChangeThroughHTTP(t *testing.T) {
	s := newServer(t, true)
	cookie, csrf := s.login(t)

	rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/step-up", cookie: cookie, csrf: csrf,
		body: `{"password":"yanlış parola dene"}`})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/session/step-up", cookie: cookie, csrf: csrf,
		body: `{"password":"` + testPassword + `"}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("step-up: %d %s", rec.Code, rec.Body.String())
	}
	if decodeBody(t, rec)["stepUpExpiresAt"] == nil {
		t.Fatal("step-up response carries no expiry")
	}

	rec = s.do(call{method: http.MethodPost, path: "/api/v1/session/password", cookie: cookie, csrf: csrf,
		body: `{"currentPassword":"` + testPassword + `","newPassword":"kısa"}`})
	if rec.Code != http.StatusUnprocessableEntity || decodeBody(t, rec)["code"] != "PASSWORD_POLICY_VIOLATION" {
		t.Fatalf("weak password: %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/session/password", cookie: cookie, csrf: csrf,
		body: `{"currentPassword":"` + testPassword + `","newPassword":"başka bir uzun parola 42"}`})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("change password: %d %s", rec.Code, rec.Body.String())
	}
	// The caller's own session keeps working with the same cookie.
	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/session", cookie: cookie}); rec.Code != http.StatusOK {
		t.Fatalf("session dropped after password change: %d", rec.Code)
	}
}

// A session whose account was suspended after login must stop working at the next request.
func TestSuspendedAccountEndsTheSessionOnTheNextRequest(t *testing.T) {
	s := newServer(t, true)
	cookie, _ := s.login(t)
	s.h.AdminExec(`UPDATE iam.actor SET status = 'SUSPENDED' WHERE identity_subject = $1`, username)

	rec := s.do(call{method: http.MethodGet, path: "/api/v1/session", cookie: cookie})
	if rec.Code != http.StatusForbidden || decodeBody(t, rec)["code"] != "ACTOR_SUSPENDED" {
		t.Fatalf("suspended account: %d %s", rec.Code, rec.Body.String())
	}
	cleared := sessionCookie(t, rec, s.cookies.Name())
	if cleared.Value != "" {
		t.Fatalf("cookie not cleared: %+v", cleared)
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var n int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.session`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the session row survived: %d", n)
	}
}

func TestMalformedBodiesAreRejected(t *testing.T) {
	s := newServer(t, true)
	for name, body := range map[string]string{
		"not json":      `{"username":`,
		"unknown field": `{"username":"a","password":"b","admin":true}`,
		"empty":         ``,
	} {
		rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/login", body: body})
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != "INVALID_REQUEST_BODY" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// A browser that logs in while holding a session must not leave the old one behind: that
// is what makes session fixation possible.
func TestLoggingInAgainReplacesTheBrowsersOldSession(t *testing.T) {
	s := newServer(t, true)
	first, _ := s.login(t)
	second, _ := s.login(t, first)

	if first.Value == second.Value {
		t.Fatal("a new login must issue a new session id")
	}
	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/session", cookie: first}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the previous session is still usable: %d", rec.Code)
	}
	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/session", cookie: second}); rec.Code != http.StatusOK {
		t.Fatalf("the new session does not work: %d", rec.Code)
	}
}

// A second device logging in leaves the first device's session alone.
func TestASecondDeviceDoesNotEndTheFirstOnesSession(t *testing.T) {
	s := newServer(t, true)
	phone, _ := s.login(t)
	laptop, _ := s.login(t)

	for name, cookie := range map[string]*http.Cookie{"phone": phone, "laptop": laptop} {
		if rec := s.do(call{method: http.MethodGet, path: "/api/v1/session", cookie: cookie}); rec.Code != http.StatusOK {
			t.Errorf("%s session is not usable: %d", name, rec.Code)
		}
	}
}
