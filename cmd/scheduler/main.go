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

	accommodationapp "github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationpg "github.com/celikbros/kapsora/internal/accommodation/infrastructure/postgres"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
	authorizationpg "github.com/celikbros/kapsora/internal/authorization/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	documentapp "github.com/celikbros/kapsora/internal/document/application"
	documentpg "github.com/celikbros/kapsora/internal/document/infrastructure/postgres"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	healthpg "github.com/celikbros/kapsora/internal/health/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/idempotency"
	"github.com/celikbros/kapsora/internal/platform/logging"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	"github.com/celikbros/kapsora/internal/platform/ratelimit"
	"github.com/celikbros/kapsora/internal/platform/scheduler"
	workflowapp "github.com/celikbros/kapsora/internal/workflow/application"
	workflowpg "github.com/celikbros/kapsora/internal/workflow/infrastructure/postgres"
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

	// The escalation job is what makes an SLA mean anything: work nobody picked up in time
	// moves to whoever is meant to catch it. No cursor codec here either — this process
	// answers no list.
	workflows, err := workflowapp.New(workflowapp.Deps{
		Pool: pool, Repo: workflowpg.New(), Audit: auditpg.New(), Logger: logger,
	})
	if err != nil {
		return err
	}

	// The treatment report expiry job. A report is valid to the end of its last day, and
	// after that a claim may not lean on it; the sweep is what makes that true without every
	// reader having to compare dates for itself. No cursor codec, and no work item port:
	// this process answers no list and submits nothing.
	reports, err := healthapp.New(healthapp.Deps{
		Pool: pool, Repo: healthpg.New(), Reports: healthpg.NewReports(),
		Audit: auditpg.New(), Logger: logger,
	})
	if err != nil {
		return err
	}

	// The accommodation sweeps. Neither of them can confirm a booking, and both say so by
	// construction: this service is given no request port, no authorization port and no
	// policy port, so the only things it can do are give a room back and tell somebody they
	// are arriving tomorrow. A process that could confirm would be a process in which an
	// unattended job could agree a stay on somebody's behalf.
	bookings, err := accommodationapp.New(accommodationapp.Deps{
		Pool: pool, Repo: accommodationpg.New(), Bookings: accommodationpg.NewBookings(),
		Ledger: entitlements.Ledger(), Audit: auditpg.New(), Logger: logger,
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
	registry.Register(scheduler.AuthorizationExpiring(authorizations))
	registry.Register(scheduler.WorkflowEscalate(workflows))
	registry.Register(scheduler.MedicalReportExpire(reports))
	registry.Register(scheduler.AccommodationHoldExpire(bookings))
	registry.Register(scheduler.AccommodationBookingReminder(bookings))
	// Document retention runs only when an object store and a retention period are both
	// configured. Purging real documents after a number nobody chose would be worse than
	// keeping them, so keeping them is the default; when the sweep does run, every
	// document a legal hold covers is skipped whatever the period says.
	if cfg.Documents.Configured() && cfg.Documents.RetentionDays > 0 {
		documents, err := newDocuments(cfg, pool, logger)
		if err != nil {
			return err
		}
		registry.Register(scheduler.DocumentRetention(documents,
			time.Duration(cfg.Documents.RetentionDays)*24*time.Hour))
	} else {
		logger.Info("document retention disabled",
			"object_store_configured", cfg.Documents.Configured(),
			"retention_days", cfg.Documents.RetentionDays)
	}
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

// newDocuments builds the document service the retention sweep drives. No scanner and no
// cursor codec: this process scans nothing and answers no list, and asking it for either
// would only add a dependency it has no reason to hold.
func newDocuments(cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) (*documentapp.Service, error) {
	store, err := objectstore.NewS3(objectstore.S3Options{
		Endpoint: cfg.Documents.Endpoint, Region: cfg.Documents.Region,
		AccessKey: cfg.Documents.AccessKey, SecretKey: cfg.Documents.SecretKey,
	})
	if err != nil {
		return nil, err
	}
	return documentapp.New(documentapp.Deps{
		Pool: pool, Repo: documentpg.New(), Store: store, Audit: auditpg.New(), Logger: logger,
		Storage: documentapp.Storage{
			QuarantineBucket: cfg.Documents.QuarantineBucket,
			SecureBucket:     cfg.Documents.SecureBucket,
			UploadTTL:        cfg.Documents.UploadURLTTL,
			DownloadTTL:      cfg.Documents.DownloadURLTTL,
			EncryptionKeyRef: cfg.Documents.EncryptionKeyRef,
		},
	})
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
