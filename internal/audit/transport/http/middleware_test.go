package audithttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

func TestRequestMetaCapturesCorrelationData(t *testing.T) {
	var got audit.RequestMeta
	h := httpx.RequestID(RequestMeta(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = audit.RequestMetaFrom(r.Context())
	})))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "[::ffff:203.0.113.7]:4444"
	req.Header.Set("traceparent", "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01")
	req.Header.Set("User-Agent", "kapsora-test/1.0")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !got.RequestID.Valid {
		t.Fatal("request id missing")
	}
	if got.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id = %q", got.TraceID)
	}
	if got.SourceIP.String() != "203.0.113.7" {
		t.Fatalf("source ip = %s", got.SourceIP)
	}
	if len(got.UserAgentHash) != 32 {
		t.Fatalf("user agent hash length = %d", len(got.UserAgentHash))
	}

	// Garbage traceparent and missing UA are tolerated.
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("traceparent", "nonsense")
	req.Header.Del("User-Agent")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.TraceID != "" || got.UserAgentHash != nil {
		t.Fatalf("unexpected meta for garbage input: %+v", got)
	}
}
