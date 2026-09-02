// Package health implements /health/live and /health/ready. Liveness only proves the
// process runs; readiness runs dependency checks with a short timeout.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Check probes one dependency. It must respect ctx cancellation.
type Check func(ctx context.Context) error

// Checker runs named checks concurrently.
type Checker struct {
	checks  map[string]Check
	timeout time.Duration
}

// NewChecker builds a checker; checks are added with Add.
func NewChecker(timeout time.Duration) *Checker {
	return &Checker{checks: map[string]Check{}, timeout: timeout}
}

// Add registers a dependency check under name.
func (c *Checker) Add(name string, check Check) *Checker {
	c.checks[name] = check
	return c
}

// Result is the readiness outcome.
type Result struct {
	Status    string            `json:"status"` // UP, DEGRADED, DOWN
	Timestamp time.Time         `json:"timestamp"`
	Checks    map[string]string `json:"checks,omitempty"`
}

// Run executes every check; any failure yields DOWN.
func (c *Checker) Run(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results = make(map[string]string, len(c.checks))
		failed  bool
	)
	for name, check := range c.checks {
		wg.Add(1)
		go func(name string, check Check) {
			defer wg.Done()
			err := check(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = true
				results[name] = "DOWN"
				return
			}
			results[name] = "UP"
		}(name, check)
	}
	wg.Wait()

	status := "UP"
	if failed {
		status = "DOWN"
	}
	return Result{Status: status, Timestamp: time.Now().UTC(), Checks: results}
}

// PostgresCheck pings the pool.
func PostgresCheck(pool *pgxpool.Pool) Check {
	return func(ctx context.Context) error { return pool.Ping(ctx) }
}

// LiveHandler answers as long as the process serves HTTP.
func LiveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, Result{Status: "UP", Timestamp: time.Now().UTC()})
	}
}

// ReadyHandler returns 200 with the check map, or a 503 problem when any check fails.
func ReadyHandler(c *Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res := c.Run(r.Context())
		if res.Status != "UP" {
			httpx.WriteProblem(w, r, httpx.Problem{
				Type:   httpx.ProblemTypeBase + "platform/not-ready",
				Title:  "Servis hazır değil",
				Status: http.StatusServiceUnavailable,
				Code:   "SERVICE_NOT_READY",
				Detail: "Bağımlılıklardan en az biri erişilemez durumda: " + joinDown(res.Checks),
			})
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

func joinDown(checks map[string]string) string {
	names := make([]string, 0, len(checks))
	for name, status := range checks {
		if status != "UP" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
