// kapsora-scheduler triggers periodic jobs. Several replicas may run; exactly one
// becomes leader by holding a PostgreSQL advisory lock on a dedicated connection.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/logging"
)

const (
	serviceName = "kapsora-scheduler"
	// leaderLockKey is the advisory lock id; stable across releases.
	leaderLockKey = int64(7301_0001)
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
		MaxConns:        2,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	logger.Info("scheduler started, waiting for leadership")
	for {
		if err := leadOnce(ctx, logger, pool); err != nil && ctx.Err() == nil {
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

// leadOnce holds the lock on one connection and runs the tick loop until the
// context ends or the connection breaks. Advisory locks are session-scoped, so
// losing the connection releases leadership automatically.
func leadOnce(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool) error {
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

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Job registry (hold expiry, SLA escalation, partition creation, ...) is
			// wired from increment I2 onwards. The heartbeat also proves the lock
			// connection is alive.
			if err := conn.Ping(ctx); err != nil {
				return err
			}
			logger.Debug("scheduler tick")
		}
	}
}
