// Package scheduler runs periodic jobs on the leader instance of kapsora-scheduler.
// Each (job, time slot) executes at most once cluster-wide thanks to the unique
// constraint on system.job_run; jobs run sequentially and never overlap themselves.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

var jobCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$`)

// Metrics are small counters a job reports (rows deleted, partitions created, ...).
type Metrics map[string]any

// Job is a periodic unit of work. Run must be idempotent and respect ctx.
type Job struct {
	Code  string
	Every time.Duration
	Run   func(ctx context.Context) (Metrics, error)
}

// Registry holds jobs in registration order.
type Registry struct {
	mu   sync.Mutex
	jobs []Job
	seen map[string]bool
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry { return &Registry{seen: map[string]bool{}} }

// Register adds a job; invalid codes, intervals or duplicates panic at startup.
func (r *Registry) Register(j Job) {
	if !jobCodePattern.MatchString(j.Code) {
		panic(fmt.Sprintf("scheduler: invalid job code %q", j.Code))
	}
	if j.Every < time.Second || j.Run == nil {
		panic(fmt.Sprintf("scheduler: job %s needs an interval of at least 1s and a Run function", j.Code))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen[j.Code] {
		panic(fmt.Sprintf("scheduler: job %s registered twice", j.Code))
	}
	r.seen[j.Code] = true
	r.jobs = append(r.jobs, j)
}

// Jobs returns a copy of the registered jobs.
func (r *Registry) Jobs() []Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Job(nil), r.jobs...)
}

// Runner executes due jobs and records system.job_run rows.
type Runner struct {
	pool     *pgxpool.Pool
	registry *Registry
	logger   *slog.Logger
	timeout  time.Duration

	mu      sync.Mutex
	lastRun map[string]time.Time
}

// NewRunner creates a runner; jobTimeout bounds one execution (default 10 minutes).
func NewRunner(pool *pgxpool.Pool, registry *Registry, logger *slog.Logger, jobTimeout time.Duration) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	if jobTimeout <= 0 {
		jobTimeout = 10 * time.Minute
	}
	return &Runner{pool: pool, registry: registry, logger: logger, timeout: jobTimeout, lastRun: map[string]time.Time{}}
}

// Tick runs every job whose current slot (now truncated to its interval) has not run yet.
// It returns the number of jobs executed by this runner.
func (r *Runner) Tick(ctx context.Context, now time.Time) int {
	executed := 0
	for _, job := range r.registry.Jobs() {
		if ctx.Err() != nil {
			return executed
		}
		slot := now.UTC().Truncate(job.Every)
		if r.alreadyRan(job.Code, slot) {
			continue
		}
		if r.runSlot(ctx, job, slot) {
			executed++
		}
	}
	return executed
}

// Loop calls Tick every interval until ctx ends.
func (r *Runner) Loop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	r.Tick(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.Tick(ctx, now)
		}
	}
}

func (r *Runner) alreadyRan(code string, slot time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastRun[code].Equal(slot)
}

func (r *Runner) markRan(code string, slot time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastRun[code] = slot
}

func (r *Runner) runSlot(ctx context.Context, job Job, slot time.Time) bool {
	q := sqlcgen.New(r.pool)
	runID, err := q.StartJobRun(ctx, sqlcgen.StartJobRunParams{JobCode: job.Code, ScheduledFor: slot})
	if errors.Is(err, pgx.ErrNoRows) {
		// Another leader (or a previous life of this one) owns the slot.
		r.markRan(job.Code, slot)
		return false
	}
	if err != nil {
		r.logger.Error("scheduler: start job run failed", "job", job.Code, "error", err)
		return false
	}
	r.markRan(job.Code, slot)

	start := time.Now()
	metrics, runErr := r.execute(ctx, job)
	status, errCode := "SUCCEEDED", (*string)(nil)
	if runErr != nil {
		status = "FAILED"
		code := "JOB_FAILED"
		errCode = &code
		r.logger.Error("scheduler: job failed", "job", job.Code, "slot", slot, "error", runErr)
	} else {
		r.logger.Info("scheduler: job finished", "job", job.Code, "slot", slot, "duration_ms", time.Since(start).Milliseconds())
	}
	if metrics == nil {
		metrics = Metrics{}
	}
	metrics["duration_ms"] = time.Since(start).Milliseconds()
	payload, _ := json.Marshal(metrics)

	if err := q.FinishJobRun(ctx, sqlcgen.FinishJobRunParams{ID: runID, Status: status, ErrorCode: errCode, MetricsJson: payload}); err != nil {
		r.logger.Error("scheduler: finish job run failed", "job", job.Code, "error", err)
	}
	return true
}

func (r *Runner) execute(ctx context.Context, job Job) (m Metrics, err error) {
	jctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("scheduler: job panicked", "job", job.Code, "panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
			err = fmt.Errorf("job panic: %v", rec)
		}
	}()
	return job.Run(jctx)
}
