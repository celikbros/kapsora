// Package identityhttp exposes the session endpoints and the middleware that turns the
// session cookie into an identity.Session on the request context.
package identityhttp

import (
	"net/http"
)

// Cookie names. The __Host- prefix binds the cookie to the exact host over HTTPS with
// Path=/ and no Domain, which browsers enforce; it cannot be used on plain HTTP, so local
// development falls back to the unprefixed name.
const (
	SecureCookieName   = "__Host-kapsora_session"
	InsecureCookieName = "kapsora_session"
)

// CookieConfig controls how the session cookie is written.
type CookieConfig struct {
	// Secure must be true everywhere except local HTTP development.
	Secure bool
}

// Name returns the cookie name for this configuration.
func (c CookieConfig) Name() string {
	if c.Secure {
		return SecureCookieName
	}
	return InsecureCookieName
}

// set writes the session cookie. No Max-Age is set: the cookie is a session cookie in the
// browser, and the server-side expiry in iam.session is the authority.
//
// Secure is a variable rather than a literal because a developer on plain HTTP needs it
// off; config.loadSession refuses that setting outside the local and test environments.
func (c CookieConfig) set(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is enforced by config.loadSession
		Name:     c.Name(),
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   c.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clear expires the session cookie.
func (c CookieConfig) clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is enforced by config.loadSession
		Name:     c.Name(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// read returns the session id from the request cookie, if present.
func (c CookieConfig) read(r *http.Request) string {
	cookie, err := r.Cookie(c.Name())
	if err != nil || cookie.Value == "" {
		return ""
	}
	return cookie.Value
}
