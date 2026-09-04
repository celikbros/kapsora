// kapsora-scheduler triggers periodic jobs. Several replicas may run; exactly one
// becomes leader by holding a PostgreSQL advisory lock on a dedicated connection, and
// every (job, slot) executes at most once cluster-wide through system.job_run.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
	authorizationpg "github.com/celikbros/kapsora/internal/authorization/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/idempotency"
	"github.com/celikbros/kapsora/internal/platform/logging"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	"github.com/celikbros/kapsora/internal/platform/ratelimit"
	"github.com/celikbros/kapsora/internal/platform/scheduler"
)

const (
	serviceName = "kapsora-scheduler"
	// leaderLockKey is the advisory lock id; stable across releases.
	leaderLockKey = int64(7301_0001)
	tickInterval  = time.Minute
)

func main() {
	if err := run(); err != nil {
		slog.Error("process exited with error", "service", serviceName, "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(serviceName)
	if err != nil {
		return err
	}
	logger := logging.New(cfg)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL, db.PoolOptions{
		ApplicationName: serviceName,
		MaxConns:        4,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	entitlements, err := ledger.New(ledger.Deps{Pool: pool, Audit: auditpg.New(), Logger: logger})
	if err != nil {
		return err
	}

	// The authorization expiry job releases what a promise no longer holds. It drives the
	// same movement engine, so a hold released here leaves the ledger exactly as a
	// cancellation would. No cursor codec: this process answers no list, and asking it
	// for one would only add a key the scheduler has no reason to hold.
	authorizations, err := authorizationapp.New(authorizationapp.Deps{
		Pool: pool, Repo: authorizationpg.New(), Ledger: entitlements.Ledger(),
		Audit: auditpg.New(), Logger: logger,
	})
	if err != nil {
		return err
	}

	registry := scheduler.NewRegistry()
	registry.Register(scheduler.AuditEnsurePartitions(pool))
	registry.Register(scheduler.OutboxRecoverStale(outbox.New(pool, outbox.Options{Logger: logger})))
	registry.Register(scheduler.IdempotencyPurge(pool, idempotency.PurgeExpired))
	registry.Register(scheduler.RateLimitPurge(ratelimit.NewPostgres(pool)))
	registry.Register(scheduler.EntitlementReservationExpire(entitlements))
	registry.Register(scheduler.EntitlementReconcile(entitlements))
	registry.Register(scheduler.AuthorizationExpire(authorizations))
	// scheduler.SessionCleanup(store) is registered once the identity session store
	// (WP-I1-01) exists.
	runner := scheduler.NewRunner(pool, registry, logger, 10*time.Minute)

	logger.Info("scheduler started, waiting for leadership", "jobs", len(registry.Jobs()))
	for {
		if err := leadOnce(ctx, logger, pool, runner); err != nil && ctx.Err() == nil {
			logger.Warn("leadership loop ended with error; retrying", "error", err)
		}
		select {
		case <-ctx.Done():
			logger.Info("scheduler stopped")
			return nil
		case <-time.After(5 * time.Second):
		}
	}
}

// leadOnce holds the lock on one connection and runs the tick loop until the context
// ends or the connection breaks. Advisory locks are session-scoped, so losing the
// connection releases leadership automatically.
func leadOnce(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool, runner *scheduler.Runner) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, leaderLockKey).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	logger.Info("leadership acquired")
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = conn.Exec(releaseCtx, `SELECT pg_advisory_unlock($1)`, leaderLockKey)
		logger.Info("leadership released")
	}()

	leaderCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// Heartbeat on the lock connection: if it breaks, leadership is gone and the
		// tick loop must stop until the lock is re-acquired.
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-leaderCtx.Done():
				return
			case <-ticker.C:
				if err := conn.Ping(leaderCtx); err != nil {
					logger.Warn("leader connection lost", "error", err)
					cancel()
					return
				}
			}
		}
	}()

	runner.Loop(leaderCtx, tickInterval)
	return nil
}
