package ratelimit

import (
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// KeyFunc derives the bucket key for a request; ok=false skips limiting.
type KeyFunc func(r *http.Request) (key string, ok bool)

// Scope names the caller for key derivation; the integrator wires it to identity.
type Scope struct {
	TenantID uuid.UUID
	ActorID  uuid.UUID
}

// ScopedKey builds keys of the form rl:<route>:<tenant>:<actor>, falling back to the
// client address for unauthenticated calls. Query strings and bodies are never used.
func ScopedKey(route string, scope func(r *http.Request) (Scope, bool)) KeyFunc {
	return func(r *http.Request) (string, bool) {
		if s, ok := scope(r); ok && s.TenantID != uuid.Nil {
			return "rl:" + route + ":" + s.TenantID.String() + ":" + s.ActorID.String(), true
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if host == "" {
			return "", false
		}
		return "rl:" + route + ":anon:" + host, true
	}
}

// Middleware rejects requests over the policy with 429 and Retry-After. When the limiter
// itself fails the request is allowed and the failure logged (availability over strictness;
// the WAF/proxy provides the second line of defence).
func Middleware(l Limiter, key KeyFunc, p Policy, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k, ok := key(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			d, err := l.Allow(r.Context(), k, p)
			if err != nil {
				logger.Warn("rate limiter unavailable; allowing request", "error", err)
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(p.PerMinute))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(d.Remaining))
			if !d.Allowed {
				seconds := int(math.Ceil(d.RetryAfter.Seconds()))
				if seconds < 1 {
					seconds = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(seconds))
				httpx.WriteProblem(w, r, httpx.Problem{
					Type:   httpx.ProblemTypeBase + "platform/rate-limited",
					Title:  "İstek sınırı aşıldı",
					Status: http.StatusTooManyRequests,
					Code:   "RATE_LIMITED",
					Detail: "Lütfen " + strconv.Itoa(seconds) + " saniye sonra tekrar deneyin.",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
