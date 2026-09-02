// kapsora-worker dispatches outbox events and runs asynchronous jobs. Handlers are
// registered by modules as they land (notification delivery, import processing, ...).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/logging"
	"github.com/celikbros/kapsora/internal/platform/outbox"
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

	dispatcher := outbox.New(pool, outbox.Options{Logger: logger})
	// Module handlers are registered here from increment I1 onwards, e.g.
	// dispatcher.Handle("notification.message.requested", notification.Deliver)

	go reportBacklog(ctx, logger, dispatcher)

	logger.Info("worker started")
	err = dispatcher.Run(ctx)
	logger.Info("worker stopped")
	return err
}

func reportBacklog(ctx context.Context, logger *slog.Logger, d *outbox.Dispatcher) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		pending, oldest, err := d.Backlog(ctx)
		if err != nil && ctx.Err() == nil {
			logger.Warn("outbox backlog query failed", "error", err)
		} else if err == nil {
			logger.Info("outbox backlog", "pending", pending, "oldest_age_seconds", int64(oldest.Seconds()))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
