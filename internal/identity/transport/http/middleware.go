package identityhttp

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// CSRFHeader carries the token returned by GET /api/v1/session.
const CSRFHeader = "X-CSRF-Token"

// Middleware loads the session and enforces CSRF on cookie-authenticated writes.
type Middleware struct {
	svc     *application.Service
	authz   *application.Authorizer
	cookies CookieConfig
	signing []byte
	logger  *slog.Logger
}

// NewMiddleware wires the session middleware. signingKey derives the per-session CSRF token.
func NewMiddleware(svc *application.Service, cookies CookieConfig, signingKey []byte, logger *slog.Logger) *Middleware {
	if logger == nil {
		logger = slog.Default()
	}
	return &Middleware{svc: svc, cookies: cookies, signing: signingKey, logger: logger}
}

// LoadSession puts a valid session on the context. It never rejects a request on its own:
// authorization is the caller's job (WP-I1-02), and public endpoints stay reachable. An
// expired or unknown cookie is cleared so the browser stops sending it.
func (m *Middleware) LoadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := m.cookies.read(r)
		if id == "" {
			next.ServeHTTP(w, r)
			return
		}
		session, err := m.svc.LoadSession(r.Context(), id)
		if err != nil {
			if !errors.Is(err, identity.ErrSessionNotFound) {
				m.logger.Error("session lookup failed", "error", err)
			}
			m.cookies.clear(w)
			next.ServeHTTP(w, r)
			return
		}
		session.CSRFToken = domain.DeriveCSRFToken(m.signing, session.ID)
		next.ServeHTTP(w, r.WithContext(identity.WithSession(r.Context(), session)))
	})
}

// RequireCSRF rejects cookie-authenticated state-changing requests without a matching
// token. Requests with no session cookie are untouched: a bearer-token client (WP-I1-02)
// is not vulnerable to CSRF because the browser never attaches its credential
// automatically.
func (m *Middleware) RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		session, ok := identity.SessionFromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if !domain.CSRFTokenMatches(session.CSRFToken, r.Header.Get(CSRFHeader)) {
			httpx.WriteProblem(w, r, httpx.Problem{
				Type:   httpx.ProblemTypeBase + "identity/csrf-token-invalid",
				Title:  "Güvenlik doğrulaması başarısız",
				Status: http.StatusForbidden,
				Code:   "CSRF_TOKEN_INVALID",
				Detail: "Oturum bilgisi yenilenmeli; sayfayı yenileyip tekrar deneyin.",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
