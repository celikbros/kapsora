package ratelimit

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

func TestMemoryBucketMath(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	m := NewMemory(clock)
	p := Policy{PerMinute: 60, Burst: 3} // 1 token per second

	for i := 0; i < 3; i++ {
		d, _ := m.Allow(context.Background(), "k", p)
		if !d.Allowed || d.Remaining != 2-i {
			t.Fatalf("call %d: %+v", i, d)
		}
	}
	d, _ := m.Allow(context.Background(), "k", p)
	if d.Allowed || d.RetryAfter != time.Second {
		t.Fatalf("4th call should be denied with 1s retry: %+v", d)
	}
	now = now.Add(1500 * time.Millisecond)
	if d, _ = m.Allow(context.Background(), "k", p); !d.Allowed {
		t.Fatalf("after refill: %+v", d)
	}
	// A different key has its own bucket; an empty policy never limits.
	if d, _ = m.Allow(context.Background(), "other", p); !d.Allowed {
		t.Fatalf("other key: %+v", d)
	}
	if d, _ = m.Allow(context.Background(), "k", Policy{}); !d.Allowed {
		t.Fatalf("empty policy must allow")
	}
	now = now.Add(time.Hour)
	if n := m.Purge(time.Minute); n != 2 {
		t.Fatalf("purged %d buckets, want 2", n)
	}
}

func TestPostgresLimiterNeverExceedsBurstUnderConcurrency(t *testing.T) {
	h := dbtest.New(t)
	l := NewPostgres(h.App)
	p := Policy{PerMinute: 6, Burst: 20} // slow refill so the burst is the only budget

	var allowed, denied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := l.Allow(context.Background(), "rl:test:concurrent", p)
			if err != nil {
				t.Errorf("allow: %v", err)
				return
			}
			if d.Allowed {
				allowed.Add(1)
			} else {
				denied.Add(1)
				if d.RetryAfter < time.Second {
					t.Errorf("retry after too small: %s", d.RetryAfter)
				}
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 20 || denied.Load() != 30 {
		t.Fatalf("allowed=%d denied=%d, want 20/30", allowed.Load(), denied.Load())
	}

	ctx, cancel := h.Ctx()
	defer cancel()
	n, err := l.Purge(ctx, -time.Hour) // everything is "idle" relative to the future
	if err != nil || n != 1 {
		t.Fatalf("purge: n=%d err=%v", n, err)
	}
}

func TestMiddlewareReturns429WithRetryAfterAndIgnoresQueryAndBody(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tenant, actor := uuid.New(), uuid.New()
	scope := func(r *http.Request) (Scope, bool) {
		if r.Header.Get("X-Test-Auth") == "1" {
			return Scope{TenantID: tenant, ActorID: actor}, true
		}
		return Scope{}, false
	}
	key := ScopedKey("organizations.list", scope)

	// Keys depend only on route, tenant and actor (or client address).
	r1 := httptest.NewRequest(http.MethodGet, "/x?tckn=1", nil)
	r1.Header.Set("X-Test-Auth", "1")
	r2 := httptest.NewRequest(http.MethodPost, "/x?other=2", nil)
	r2.Header.Set("X-Test-Auth", "1")
	k1, _ := key(r1)
	k2, _ := key(r2)
	if k1 != k2 || k1 != "rl:organizations.list:"+tenant.String()+":"+actor.String() {
		t.Fatalf("keys differ or leak query: %q %q", k1, k2)
	}
	anon := httptest.NewRequest(http.MethodGet, "/x", nil)
	anon.RemoteAddr = "203.0.113.5:1234"
	if k, ok := key(anon); !ok || k != "rl:organizations.list:anon:203.0.113.5" {
		t.Fatalf("anonymous key = %q ok=%v", k, ok)
	}

	h := Middleware(NewMemory(nil), key, Policy{PerMinute: 60, Burst: 2}, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	codes := []int{}
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("X-Test-Auth", "1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
		if rec.Code == http.StatusTooManyRequests {
			if rec.Header().Get("Retry-After") == "" || rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("429 without Retry-After/problem body: %v", rec.Header())
			}
		}
	}
	if codes[0] != 204 || codes[1] != 204 || codes[2] != 429 {
		t.Fatalf("codes = %v", codes)
	}
}
