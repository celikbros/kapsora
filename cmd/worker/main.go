// kapsora-worker dispatches outbox events and runs asynchronous jobs. Handlers are
// registered by modules as they land (entitlement account opening, notification
// delivery, import processing, ...).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/party/memberimport"
	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
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

	entitlements, err := ledger.New(ledger.Deps{Pool: pool, Audit: auditpg.New(), Logger: logger})
	if err != nil {
		return err
	}

	// The import jobs decrypt the staged identifiers of a row to write the person, so the
	// worker needs the same key provider as the API. The cursor codec is only used by the
	// paged reads of the HTTP layer, but the service requires one.
	keys, err := localkey.NewFromEnv()
	if err != nil {
		return err
	}
	cursors, err := httpx.NewCursorCodec(cfg.Session.SigningKey)
	if err != nil {
		return err
	}
	memberImports, err := memberimport.New(memberimport.Deps{
		Pool: pool, Cipher: keys, Index: keys, Audit: auditpg.New(), Cursors: cursors, Logger: logger,
	})
	if err != nil {
		return err
	}

	dispatcher := outbox.New(pool, outbox.Options{Logger: logger})
	// A new enrollment opens its entitlement accounts here rather than in the request
	// that created it: the accounts follow the plan configuration, and the handler is
	// idempotent, so a redelivery finds them already open.
	dispatcher.Handle(benefitapp.EnrollmentCreatedEvent, entitlements.HandleEnrollmentCreated)
	// Member import: a file too large to validate inside the upload request, and the
	// apply of an accepted batch, both run here in resumable chunks.
	dispatcher.Handle(memberimport.StagedEvent, memberImports.HandleStaged)
	dispatcher.Handle(memberimport.ApplyEvent, memberImports.HandleApply)

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
