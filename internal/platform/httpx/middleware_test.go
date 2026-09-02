package httpx

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRequestIDAcceptsUUIDAndReplacesGarbage(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	valid := uuid.NewString()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(RequestIDHeader, valid)
	h.ServeHTTP(rec, req)
	if seen != valid || rec.Header().Get(RequestIDHeader) != valid {
		t.Fatalf("valid request id not preserved: ctx=%q header=%q", seen, rec.Header().Get(RequestIDHeader))
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(RequestIDHeader, "<script>alert(1)</script>")
	h.ServeHTTP(rec, req)
	if _, err := uuid.Parse(seen); err != nil {
		t.Fatalf("garbage request id was not replaced by a UUID: %q", seen)
	}
	if rec.Header().Get(RequestIDHeader) != seen {
		t.Fatalf("response header %q != context id %q", rec.Header().Get(RequestIDHeader), seen)
	}
}

func TestRecovererWritesProblemWithoutLeakingPanic(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := RequestID(Recoverer(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret database password leaked?")
	})))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
	body := rec.Body.String()
	if strings.Contains(body, "secret") {
		t.Fatalf("panic value leaked into response: %s", body)
	}
	var p Problem
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("invalid problem json: %v", err)
	}
	if p.Code != "INTERNAL_ERROR" || p.TraceID == "" || p.Instance != "/boom" {
		t.Fatalf("unexpected problem: %+v", p)
	}
}

func TestRequestLoggerCountsStatusAndBytes(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := RequestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/things?tckn=123", nil))

	line := buf.String()
	for _, want := range []string{`"status":201`, `"bytes":5`, `"path":"/things"`} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line missing %s: %s", want, line)
		}
	}
	if strings.Contains(line, "tckn") {
		t.Fatalf("query string must not be logged: %s", line)
	}
}
