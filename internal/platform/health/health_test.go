package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReadyHandlerReportsUpAndDown(t *testing.T) {
	up := NewChecker(time.Second).Add("postgresql", func(context.Context) error { return nil })
	rec := httptest.NewRecorder()
	ReadyHandler(up).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var res Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Status != "UP" || res.Checks["postgresql"] != "UP" {
		t.Fatalf("unexpected result %+v", res)
	}

	down := NewChecker(time.Second).
		Add("postgresql", func(context.Context) error { return errors.New("refused") }).
		Add("valkey", func(context.Context) error { return nil })
	rec = httptest.NewRecorder()
	ReadyHandler(down).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestReadyHandlerHonoursTimeout(t *testing.T) {
	slow := NewChecker(50*time.Millisecond).Add("slow", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return nil
		}
	})
	start := time.Now()
	rec := httptest.NewRecorder()
	ReadyHandler(slow).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("readiness did not respect the check timeout")
	}
}
