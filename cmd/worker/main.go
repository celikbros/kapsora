// kapsora-worker dispatches outbox events and runs asynchronous jobs.
// In increment I0 it only verifies its dependencies and reports the outbox backlog;
// handlers are registered by modules from I1 onwards.
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

const serviceName = "kapsora-worker"

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
		MaxConns:        cfg.DBMaxConns,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	logger.Info("worker started")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		reportBacklog(ctx, logger, pool)
		select {
		case <-ctx.Done():
			logger.Info("worker stopped")
			return nil
		case <-ticker.C:
		}
	}
}

func reportBacklog(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool) {
	var pending int64
	var oldest *time.Time
	err := pool.QueryRow(ctx,
		`SELECT count(*), min(available_at)
		   FROM system.outbox_event
		  WHERE status IN ('PENDING','FAILED')`).Scan(&pending, &oldest)
	if err != nil {
		logger.Warn("outbox backlog query failed", "error", err)
		return
	}
	age := time.Duration(0)
	if oldest != nil {
		age = time.Since(*oldest)
	}
	logger.Info("outbox backlog", "pending", pending, "oldest_age_seconds", int64(age.Seconds()))
}
