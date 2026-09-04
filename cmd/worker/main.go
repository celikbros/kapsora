// kapsora-worker dispatches outbox events and runs asynchronous jobs. Handlers are
// registered by modules as they land (entitlement account opening, notification
// delivery, import processing, ...).
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	documentapp "github.com/celikbros/kapsora/internal/document/application"
	documentpg "github.com/celikbros/kapsora/internal/document/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/party/memberimport"
	"github.com/celikbros/kapsora/internal/platform/antivirus"
	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/logging"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
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

	// Documents. This is the only process that ever reads a file body, and it reads one
	// for exactly one reason: to stream it to the virus scanner. The scanner is required
	// here — a worker without one would leave every upload unscanned, and the pipeline
	// refuses to treat a missing verdict as a clean one.
	documents, err := newDocuments(cfg, pool, logger)
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
	// The document pipeline: a file that finished uploading is streamed out of quarantine
	// to ClamAV, and only a CLEAN verdict copies it into the secure bucket. The handler is
	// idempotent — a redelivery re-tidies a decided document rather than re-scanning it —
	// and a scanner that cannot be reached leaves the file FAILED and retryable rather
	// than promoted.
	dispatcher.Handle(documentapp.ScanRequestedEvent, documents.HandleScanRequested)

	go reportBacklog(ctx, logger, dispatcher)

	logger.Info("worker started")
	err = dispatcher.Run(ctx)
	logger.Info("worker stopped")
	return err
}

// newDocuments builds the document service with both the object store and the scanner. A
// worker missing either cannot do the one job it has here, so it refuses to start rather
// than dead-lettering every scan it is handed.
func newDocuments(cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) (*documentapp.Service, error) {
	if !cfg.Documents.Configured() {
		return nil, errors.New("kapsora-worker: the document scan pipeline needs an object store; set KAPSORA_MINIO_ROOT_USER and KAPSORA_MINIO_ROOT_PASSWORD (see .env.example) and start it with scripts/native/up")
	}
	store, err := objectstore.NewS3(objectstore.S3Options{
		Endpoint: cfg.Documents.Endpoint, Region: cfg.Documents.Region,
		AccessKey: cfg.Documents.AccessKey, SecretKey: cfg.Documents.SecretKey,
	})
	if err != nil {
		return nil, err
	}
	scanner, err := antivirus.NewClamd(antivirus.ClamdOptions{
		Address: cfg.Documents.ScannerAddr, Timeout: cfg.Documents.ScannerTimeout,
	})
	if err != nil {
		return nil, err
	}
	return documentapp.New(documentapp.Deps{
		Pool: pool, Repo: documentpg.New(), Store: store, Scanner: scanner,
		Audit: auditpg.New(), Logger: logger,
		Storage: documentapp.Storage{
			QuarantineBucket: cfg.Documents.QuarantineBucket,
			SecureBucket:     cfg.Documents.SecureBucket,
			UploadTTL:        cfg.Documents.UploadURLTTL,
			DownloadTTL:      cfg.Documents.DownloadURLTTL,
			EncryptionKeyRef: cfg.Documents.EncryptionKeyRef,
		},
	})
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
