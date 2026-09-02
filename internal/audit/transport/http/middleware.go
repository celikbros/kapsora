// Package audithttp attaches request correlation data (request id, trace id, client
// address, user-agent hash) to the context so the audit recorder can copy it onto rows.
package audithttp

import (
	"crypto/sha256"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// RequestMeta must run after httpx.RequestID.
func RequestMeta(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta := audit.RequestMeta{TraceID: traceIDFrom(r.Header.Get("traceparent"))}
		if id, err := uuid.Parse(httpx.RequestIDFrom(r.Context())); err == nil {
			meta.RequestID = uuid.NullUUID{UUID: id, Valid: true}
		}
		if addr, ok := clientAddr(r.RemoteAddr); ok {
			meta.SourceIP = addr
		}
		if ua := r.UserAgent(); ua != "" {
			sum := sha256.Sum256([]byte(ua))
			meta.UserAgentHash = sum[:]
		}
		next.ServeHTTP(w, r.WithContext(audit.WithRequestMeta(r.Context(), meta)))
	})
}

// traceIDFrom extracts the 32-hex trace id from a W3C traceparent header.
func traceIDFrom(traceparent string) string {
	parts := strings.Split(traceparent, "-")
	if len(parts) >= 3 && len(parts[1]) == 32 {
		return strings.ToLower(parts[1])
	}
	return ""
}

func clientAddr(remote string) (netip.Addr, bool) {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}
