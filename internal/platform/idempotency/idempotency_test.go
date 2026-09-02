package idempotency

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

const testKey = "0123456789abcdef-key"

type fixture struct {
	h       *dbtest.Harness
	tenant  uuid.UUID
	actor   uuid.UUID
	calls   atomic.Int32
	handler http.Handler
}

func newFixture(t *testing.T, opts Options, handler func(w http.ResponseWriter, r *http.Request)) *fixture {
	t.Helper()
	f := &fixture{h: dbtest.New(t), actor: uuid.New()}
	f.tenant = f.h.CreateTenant("IDEMP_" + strings.ToUpper(uuid.NewString()[:6]))
	opts.Scope = func(r *http.Request) (Scope, bool) { return Scope{TenantID: f.tenant, ActorID: f.actor}, true }
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	if opts.CommandCode == "" {
		opts.CommandCode = "test.command"
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		handler(w, r)
	})
	f.handler = Middleware(f.h.App, opts)(inner)
	return f
}

func (f *fixture) do(method, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/things", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(KeyHeader, key)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func createdHandler(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	_ = json.NewDecoder(r.Body).Decode(&in)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", `"1"`)
	w.Header().Set("Location", "/api/v1/things/0192a000-0000-7000-8000-000000000001")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "0192a000-0000-7000-8000-000000000001", "echo": in["name"]})
}

func TestMissingKeyRequiredUnlessOptional(t *testing.T) {
	f := newFixture(t, Options{}, createdHandler)
	rec := f.do(http.MethodPost, `{"name":"a"}`, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "IDEMPOTENCY_KEY_REQUIRED") {
		t.Fatalf("missing key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(http.MethodPost, `{}`, "short"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "IDEMPOTENCY_KEY_INVALID") {
		t.Fatalf("short key: %d %s", rec.Code, rec.Body.String())
	}

	opt := newFixture(t, Options{Optional: true}, createdHandler)
	if rec := opt.do(http.MethodPost, `{"name":"a"}`, ""); rec.Code != http.StatusCreated {
		t.Fatalf("optional without key: %d", rec.Code)
	}
}

func TestReplayReturnsStoredResponseAndRunsHandlerOnce(t *testing.T) {
	f := newFixture(t, Options{}, createdHandler)

	first := f.do(http.MethodPost, `{"name":"alpha","n":1}`, testKey)
	if first.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	// Same payload with different key order and whitespace replays.
	second := f.do(http.MethodPost, ` { "n" : 1 , "name" : "alpha" } `, testKey)
	if second.Code != http.StatusCreated {
		t.Fatalf("replay status: %d %s", second.Code, second.Body.String())
	}
	if second.Header().Get(ReplayedHeader) != "true" {
		t.Fatalf("replay header missing")
	}
	if second.Header().Get("ETag") != `"1"` || second.Header().Get("Location") == "" {
		t.Fatalf("replayed headers missing: %v", second.Header())
	}
	if strings.TrimSpace(second.Body.String()) != strings.TrimSpace(first.Body.String()) {
		t.Fatalf("replayed body differs:\n%s\n%s", first.Body.String(), second.Body.String())
	}
	if f.calls.Load() != 1 {
		t.Fatalf("handler called %d times, want 1", f.calls.Load())
	}

	// Different payload with the same key is rejected.
	third := f.do(http.MethodPost, `{"name":"beta","n":1}`, testKey)
	if third.Code != http.StatusConflict || !strings.Contains(third.Body.String(), "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("reuse: %d %s", third.Code, third.Body.String())
	}
	if f.calls.Load() != 1 {
		t.Fatalf("handler called %d times after reuse, want 1", f.calls.Load())
	}
}

func TestConcurrentDuplicateGetsInProgress(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	f := newFixture(t, Options{}, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	})

	var wg sync.WaitGroup
	wg.Add(1)
	var firstCode int
	go func() {
		defer wg.Done()
		firstCode = f.do(http.MethodPost, `{}`, testKey).Code
	}()
	<-entered

	dup := f.do(http.MethodPost, `{}`, testKey)
	if dup.Code != http.StatusConflict || !strings.Contains(dup.Body.String(), "IDEMPOTENCY_IN_PROGRESS") || dup.Header().Get("Retry-After") == "" {
		t.Fatalf("duplicate while in progress: %d %s %v", dup.Code, dup.Body.String(), dup.Header())
	}
	close(release)
	wg.Wait()
	if firstCode != http.StatusNoContent {
		t.Fatalf("first request: %d", firstCode)
	}
	if replay := f.do(http.MethodPost, `{}`, testKey); replay.Code != http.StatusNoContent || replay.Header().Get(ReplayedHeader) != "true" {
		t.Fatalf("replay after completion: %d", replay.Code)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("handler called %d times, want 1", f.calls.Load())
	}
}

func TestServerErrorsAreNotPinned(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	f := newFixture(t, Options{}, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if rec := f.do(http.MethodPost, `{}`, testKey); rec.Code != http.StatusInternalServerError {
		t.Fatalf("first: %d", rec.Code)
	}
	fail.Store(false)
	if rec := f.do(http.MethodPost, `{}`, testKey); rec.Code != http.StatusNoContent || rec.Header().Get(ReplayedHeader) != "" {
		t.Fatalf("retry after 500 should execute again: %d replayed=%q", rec.Code, rec.Header().Get(ReplayedHeader))
	}
	if f.calls.Load() != 2 {
		t.Fatalf("handler called %d times, want 2", f.calls.Load())
	}
}

func TestClientErrorsAreReplayed(t *testing.T) {
	f := newFixture(t, Options{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code":"VALIDATION_FAILED"}`))
	})
	f.do(http.MethodPost, `{}`, testKey)
	rec := f.do(http.MethodPost, `{}`, testKey)
	if rec.Code != http.StatusUnprocessableEntity || rec.Header().Get(ReplayedHeader) != "true" || !strings.Contains(rec.Body.String(), "VALIDATION_FAILED") {
		t.Fatalf("422 replay: %d %s", rec.Code, rec.Body.String())
	}
	if f.calls.Load() != 1 {
		t.Fatalf("handler called %d times, want 1", f.calls.Load())
	}
}

func TestStaleInProgressRecordIsTakenOver(t *testing.T) {
	f := newFixture(t, Options{StaleInProgress: time.Millisecond}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	// Simulate a crashed process: an IN_PROGRESS row older than the stale window.
	f.h.AdminExec(`INSERT INTO system.idempotency_record (tenant_id, actor_id, command_code, idempotency_key, request_hash, created_at)
		VALUES ($1, $2, 'test.command', $3, $4, clock_timestamp() - interval '1 minute')`,
		f.tenant, f.actor, testKey, requestHashForTest(f.tenant, `{}`))
	rec := f.do(http.MethodPost, `{}`, testKey)
	if rec.Code != http.StatusNoContent || f.calls.Load() != 1 {
		t.Fatalf("stale takeover: %d calls=%d", rec.Code, f.calls.Load())
	}
}

func requestHashForTest(tenant uuid.UUID, body string) []byte {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/things", nil)
	return requestHash(req, Scope{TenantID: tenant}, []byte(body))
}

func TestPurgeExpired(t *testing.T) {
	f := newFixture(t, Options{TTL: time.Millisecond}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	f.do(http.MethodPost, `{}`, testKey)
	time.Sleep(5 * time.Millisecond)
	n, err := PurgeExpired(context.Background(), f.h.App, f.tenant, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("purged %d (%v), want 1", n, err)
	}
}
