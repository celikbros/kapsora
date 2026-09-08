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

	accommodationapp "github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationgw "github.com/celikbros/kapsora/internal/accommodation/infrastructure/gateway"
	accommodationpg "github.com/celikbros/kapsora/internal/accommodation/infrastructure/postgres"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
	authorizationpg "github.com/celikbros/kapsora/internal/authorization/infrastructure/postgres"
	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	claimapp "github.com/celikbros/kapsora/internal/claim/application"
	claimgw "github.com/celikbros/kapsora/internal/claim/infrastructure/gateway"
	claimpg "github.com/celikbros/kapsora/internal/claim/infrastructure/postgres"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	contractpg "github.com/celikbros/kapsora/internal/contract/infrastructure/postgres"
	documentapp "github.com/celikbros/kapsora/internal/document/application"
	documentpg "github.com/celikbros/kapsora/internal/document/infrastructure/postgres"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	healthgw "github.com/celikbros/kapsora/internal/health/infrastructure/gateway"
	healthpg "github.com/celikbros/kapsora/internal/health/infrastructure/postgres"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/notification/infrastructure/channel"
	notificationpg "github.com/celikbros/kapsora/internal/notification/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/party/memberimport"
	"github.com/celikbros/kapsora/internal/platform/antivirus"
	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/logging"
	"github.com/celikbros/kapsora/internal/platform/mail"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
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

	// Notifications. This is the only process that talks to a mail server, and it holds an
	// adapter for all four channels: a worker asked to send on a channel it has no adapter
	// for answers an error rather than recording a delivery that did not happen.
	notifications, err := newNotifications(cfg, pool, keys, logger)
	if err != nil {
		return err
	}

	// The inpatient stay's side of a decided request (WP-I5-03). It lives here rather than
	// in the API because that is the point of the seam: a medical reviewer decides an
	// admission where they decide every request, and the stay moves afterwards, in a
	// different process, without the reviewer's own transaction depending on it.
	//
	// This service is given the stay repository and the authorization gateway and nothing
	// else it does not need: it never raises a request, so the request port is left at its
	// refusing default, and a bug that tried to raise one here would say so rather than
	// quietly working.
	stays, err := newStays(pool, entitlements.Ledger(), logger)
	if err != nil {
		return err
	}

	// The booking's side of the same decided request (WP-I6-02). It is a second subscriber
	// to one event rather than a branch inside the first: an admission and a hotel booking
	// have nothing to say to each other, and each handler quietly recognises none of its own
	// in most of what it is handed.
	//
	// The one thing that makes this handler safe to redeliver is the adoption: the
	// authorization it creates takes over the reservation the hold already placed rather
	// than reserving the nights again, so neither the first delivery nor the fifth can draw
	// a member's plan down twice for one stay.
	bookings, err := newBookings(pool, entitlements.Ledger(), cursors, logger)
	if err != nil {
		return err
	}

	// The billing side of a stay (WP-I7-01). It is given the claim tables and the work item
	// port and nothing else it does not need: it never prices a line — every amount it writes
	// was frozen by the booking — so the pricing, rules and authorization ports are left at
	// their refusing defaults, and a bug that tried to price something here would say so
	// rather than quietly working.
	claims, err := newClaims(pool, cursors, logger)
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
	// Notifications. A module asks for one by publishing an event inside its own business
	// transaction; nothing is rendered, written or sent until that transaction commits and
	// this handler picks the event up. Both handlers are idempotent: the message table is
	// unique on the deduplication key, so a redelivered event finds the message the first
	// delivery wrote and continues from wherever it stopped rather than telling somebody
	// twice.
	dispatcher.Handle(notificationapp.NotifyRequestedEvent, notifications.HandleNotifyRequested)
	dispatcher.Handle(notificationapp.SendRequestedEvent, notifications.HandleSendRequested)
	// A decided service request. Most of them are not admissions and this handler quietly
	// recognises none of its own in them; the ones that are move the stay to AUTHORIZED with
	// a hold taken for the days the reviewer actually approved, or to REJECTED with none.
	// It is idempotent by predicate rather than by flag, so a redelivery finds nothing left
	// to do rather than reserving the same entitlement twice.
	dispatcher.Handle(servicerequestapp.DecidedEvent, stays.HandleServiceRequestDecided)
	// The same event, the other subscriber: an approved RESERVATION request turns its hold
	// into a confirmed stay, and a refused one gives the room and the nights back.
	dispatcher.Handle(servicerequestapp.DecidedEvent, bookings.HandleServiceRequestDecided)
	// A stay that ended. Each of the three is a claim the provider may invoice, and each is
	// raised here rather than inside the command that ended the stay: a desk clerk closing a
	// stay at eleven at night must not be told that the billing side is down.
	//
	// All three are idempotent, and not by trying to be. Each looks for the claim a first
	// delivery made, and `uq_claim_live_booking` is underneath that read for the case where
	// two deliveries look at the same moment — so a redelivered check-out event creates
	// nothing, not a second version and not a second work item.
	dispatcher.Handle(accommodationapp.CheckedOutEvent, claims.HandleBookingCheckedOut)
	dispatcher.Handle(accommodationapp.NoShowConfirmedEvent, claims.HandleBookingNoShowConfirmed)
	// A free cancellation reaches this handler and produces nothing: it is the ordinary case
	// of a member who called off a stay inside the window they were promised.
	dispatcher.Handle(accommodationapp.CancelledEvent, claims.HandleBookingCancelled)

	go reportBacklog(ctx, logger, dispatcher)

	logger.Info("worker started")
	err = dispatcher.Run(ctx)
	logger.Info("worker stopped")
	return err
}

// newBookings builds the accommodation service the decided-request subscription needs: the
// booking tables, WP-I4-02 behind the narrow port that takes a hold and mints a voucher, and
// WP-I6-04's lodging terms.
//
// It is given no request port, because it raises none — a booking is confirmed from the API
// and the decision comes back here — so that port is left at its refusing default and a bug
// that tried to raise a request would say so rather than quietly working.
//
// The movement engine is the one this process already holds. Two ledgers over one database
// would take two different account locks for the same account, which is exactly the way a
// no-double-spend rule stops holding.
func newBookings(pool *pgxpool.Pool, movements *ledger.Ledger,
	cursors *httpx.CursorCodec, logger *slog.Logger,
) (*accommodationapp.Service, error) {
	authorizations, err := authorizationapp.New(authorizationapp.Deps{
		Pool: pool, Repo: authorizationpg.New(), Ledger: movements,
		Audit: auditpg.New(), Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	contracts, err := contractapp.New(contractapp.Deps{
		Pool: pool, Repo: contractpg.New(), Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		return nil, err
	}
	return accommodationapp.New(accommodationapp.Deps{
		Pool: pool, Repo: accommodationpg.New(), Bookings: accommodationpg.NewBookings(),
		Ledger:         movements,
		Authorizations: accommodationgw.NewAuthorizations(authorizations),
		Policies:       accommodationgw.NewPolicies(contracts),
		Audit:          auditpg.New(), Cursors: cursors, Logger: logger,
	})
}

// newClaims builds the claim service the three booking subscriptions need.
//
// A lodging claim copies the amounts the booking froze and decides nothing on the merits, so
// this service is deliberately given no pricing ladder, no rule engine and no authorization
// gateway. Those ports refuse by default, which is the honest behaviour of a process that has
// no business asking any of them: a claim raised here that tried to price a night would be
// re-pricing a stay somebody already agreed to.
func newClaims(pool *pgxpool.Pool, cursors *httpx.CursorCodec, logger *slog.Logger,
) (*claimapp.Service, error) {
	return claimapp.New(claimapp.Deps{
		Pool: pool, Repo: claimpg.New(),
		// The work item is what puts the claim in front of the financial reviewer. Without
		// it a completed stay would become a claim nobody was watching.
		WorkItems: claimgw.NewWorkItems(logger),
		Audit:     auditpg.New(), Cursors: cursors, Logger: logger,
	})
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

// newNotifications builds the notification service with an adapter for every channel. The
// SMTP client does not connect here: a relay that is down at start-up must not stop the
// worker, because the messages it cannot send stay queued and are retried, which is
// exactly what should happen.
// newNotifications builds the notification service. It is handed the key provider because
// this is the process that resolves a member's address: a PERSON recipient's e-mail or
// telephone number lives encrypted in party.person_contact, and the repository decrypts it
// at send time rather than the pipeline ever holding it.
func newNotifications(cfg config.Config, pool *pgxpool.Pool, keys crypto.FieldCipher,
	logger *slog.Logger,
) (*notificationapp.Service, error) {
	smtp, err := mail.NewSMTP(mail.SMTPOptions{
		Address: cfg.Notifications.SMTPAddr, From: cfg.Notifications.SMTPFrom,
		Username: cfg.Notifications.SMTPUsername, Password: cfg.Notifications.SMTPPassword,
		StartTLS: cfg.Notifications.SMTPStartTLS, Timeout: cfg.Notifications.SMTPTimeout,
	})
	if err != nil {
		return nil, err
	}
	email, err := channel.NewEmail(smtp)
	if err != nil {
		return nil, err
	}
	return notificationapp.New(notificationapp.Deps{
		Pool: pool, Repo: notificationpg.New(keys), Audit: auditpg.New(),
		LinkBase: cfg.Notifications.LinkBase, Logger: logger,
		Senders: map[string]notificationapp.ChannelSender{
			domain.ChannelEmail: email,
			// SMS has no provider yet and PUSH has no device registry; both record the
			// attempt so the message is in the log with a delivery row saying it was never
			// handed to anybody. INAPP is delivered by being recorded: the message log is
			// what a screen reads.
			domain.ChannelSMS:   channel.NewSMS(logger),
			domain.ChannelPush:  channel.NewPush(logger),
			domain.ChannelInApp: channel.NewInApp(logger),
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

// newStays builds the health service the decided-request subscription needs: the stay's own
// tables, and WP-I4-02 behind the narrow port that takes, extends and gives back a hold.
//
// The movement engine is the one this process already holds, not a second one. Two ledgers
// over one database would take two different account locks for the same account, which is
// exactly the way a no-double-spend rule stops holding.
func newStays(pool *pgxpool.Pool, movements authorizationapp.Ledger,
	logger *slog.Logger,
) (*healthapp.Service, error) {
	authorizations, err := authorizationapp.New(authorizationapp.Deps{
		Pool: pool, Repo: authorizationpg.New(), Ledger: movements,
		Audit: auditpg.New(), Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	return healthapp.New(healthapp.Deps{
		Pool: pool, Repo: healthpg.New(), Reports: healthpg.NewReports(),
		StayRepo: healthpg.NewStays(), Authorizations: healthgw.NewAuthorizations(authorizations),
		Audit: auditpg.New(), Logger: logger,
	})
}
