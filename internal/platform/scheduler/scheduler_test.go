package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRegistryValidation(t *testing.T) {
	reg := NewRegistry()
	ok := Job{Code: "test.job", Every: time.Minute, Run: func(context.Context) (Metrics, error) { return nil, nil }}
	reg.Register(ok)
	for name, bad := range map[string]Job{
		"bad code":    {Code: "Bad", Every: time.Minute, Run: ok.Run},
		"no run":      {Code: "test.norun", Every: time.Minute},
		"short every": {Code: "test.short", Every: time.Millisecond, Run: ok.Run},
		"duplicate":   ok,
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: expected panic", name)
				}
			}()
			reg.Register(bad)
		}()
	}
	if len(reg.Jobs()) != 1 {
		t.Fatalf("jobs = %d, want 1", len(reg.Jobs()))
	}
}

func TestRunnerExecutesOncePerSlotAndRecordsRuns(t *testing.T) {
	h := dbtest.New(t)
	reg := NewRegistry()
	var okRuns, failRuns atomic.Int32
	reg.Register(Job{Code: "test.ok", Every: time.Minute, Run: func(context.Context) (Metrics, error) {
		okRuns.Add(1)
		return Metrics{"rows": 3}, nil
	}})
	reg.Register(Job{Code: "test.fail", Every: time.Minute, Run: func(context.Context) (Metrics, error) {
		failRuns.Add(1)
		return nil, errors.New("boom")
	}})
	reg.Register(Job{Code: "test.panic", Every: time.Minute, Run: func(context.Context) (Metrics, error) {
		panic("kaboom")
	}})

	runner := NewRunner(h.App, reg, quiet(), time.Minute)
	now := time.Date(2026, 9, 2, 12, 30, 15, 0, time.UTC)

	if n := runner.Tick(context.Background(), now); n != 3 {
		t.Fatalf("first tick executed %d jobs, want 3", n)
	}
	if n := runner.Tick(context.Background(), now.Add(20*time.Second)); n != 0 {
		t.Fatalf("same slot executed %d jobs, want 0", n)
	}
	if n := runner.Tick(context.Background(), now.Add(time.Minute)); n != 3 {
		t.Fatalf("next slot executed %d jobs, want 3", n)
	}
	if okRuns.Load() != 2 || failRuns.Load() != 2 {
		t.Fatalf("runs ok=%d fail=%d, want 2/2", okRuns.Load(), failRuns.Load())
	}

	ctx, cancel := h.Ctx()
	defer cancel()
	last, err := JobRunHistory(ctx, h.App, "test.ok")
	if err != nil || last.Status != "SUCCEEDED" || last.FinishedAt == nil {
		t.Fatalf("ok history: %+v err=%v", last, err)
	}
	if last, _ := JobRunHistory(ctx, h.App, "test.fail"); last.Status != "FAILED" {
		t.Fatalf("fail history: %+v", last)
	}
	if last, _ := JobRunHistory(ctx, h.App, "test.panic"); last.Status != "FAILED" {
		t.Fatalf("panic history: %+v", last)
	}
	var metrics string
	if err := h.Admin.QueryRow(ctx, `SELECT metrics_json::text FROM system.job_run WHERE job_code = 'test.ok' ORDER BY scheduled_for LIMIT 1`).Scan(&metrics); err != nil {
		t.Fatal(err)
	}
	if metrics == "{}" {
		t.Fatalf("metrics not recorded")
	}

	// A slot already claimed by another leader is skipped.
	other := NewRunner(h.App, reg, quiet(), time.Minute)
	if n := other.Tick(context.Background(), now); n != 0 {
		t.Fatalf("second runner re-ran %d jobs for a claimed slot", n)
	}
}

func TestAuditEnsurePartitionsRunsAsApplicationRole(t *testing.T) {
	h := dbtest.New(t)
	job := AuditEnsurePartitions(h.App)
	m, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("ensure partitions: %v", err)
	}
	if m["partitions"] == nil {
		t.Fatalf("no partitions reported: %v", m)
	}
	ctx, cancel := h.Ctx()
	defer cancel()
	var n int
	if err := h.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid WHERE i.inhparent = 'audit.event'::regclass`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 3 { // default + current + next (+ maybe month after next)
		t.Fatalf("audit.event has %d partitions, want at least 3", n)
	}
}
