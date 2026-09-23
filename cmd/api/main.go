// kapsora-api serves the REST API and the browser-facing session endpoints.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	accommodationapp "github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationgw "github.com/celikbros/kapsora/internal/accommodation/infrastructure/gateway"
	accommodationpg "github.com/celikbros/kapsora/internal/accommodation/infrastructure/postgres"
	accommodationhttp "github.com/celikbros/kapsora/internal/accommodation/transport/http"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	audithttp "github.com/celikbros/kapsora/internal/audit/transport/http"
	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
	authorizationpg "github.com/celikbros/kapsora/internal/authorization/infrastructure/postgres"
	authorizationhttp "github.com/celikbros/kapsora/internal/authorization/transport/http"
	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	benefiteligibility "github.com/celikbros/kapsora/internal/benefit/eligibility"
	benefitpg "github.com/celikbros/kapsora/internal/benefit/infrastructure/postgres"
	benefitledger "github.com/celikbros/kapsora/internal/benefit/ledger"
	benefithttp "github.com/celikbros/kapsora/internal/benefit/transport/http"
	billingapp "github.com/celikbros/kapsora/internal/billing/application"
	billinggw "github.com/celikbros/kapsora/internal/billing/infrastructure/gateway"
	billingpg "github.com/celikbros/kapsora/internal/billing/infrastructure/postgres"
	billinghttp "github.com/celikbros/kapsora/internal/billing/transport/http"
	catalogapp "github.com/celikbros/kapsora/internal/catalog/application"
	catalogpg "github.com/celikbros/kapsora/internal/catalog/infrastructure/postgres"
	cataloghttp "github.com/celikbros/kapsora/internal/catalog/transport/http"
	claimapp "github.com/celikbros/kapsora/internal/claim/application"
	claimgw "github.com/celikbros/kapsora/internal/claim/infrastructure/gateway"
	claimpg "github.com/celikbros/kapsora/internal/claim/infrastructure/postgres"
	claimhttp "github.com/celikbros/kapsora/internal/claim/transport/http"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	contractpg "github.com/celikbros/kapsora/internal/contract/infrastructure/postgres"
	contracthttp "github.com/celikbros/kapsora/internal/contract/transport/http"
	documentapp "github.com/celikbros/kapsora/internal/document/application"
	documentpg "github.com/celikbros/kapsora/internal/document/infrastructure/postgres"
	documenthttp "github.com/celikbros/kapsora/internal/document/transport/http"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	healthgw "github.com/celikbros/kapsora/internal/health/infrastructure/gateway"
	healthpg "github.com/celikbros/kapsora/internal/health/infrastructure/postgres"
	healthhttp "github.com/celikbros/kapsora/internal/health/transport/http"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	notificationpg "github.com/celikbros/kapsora/internal/notification/infrastructure/postgres"
	notificationhttp "github.com/celikbros/kapsora/internal/notification/transport/http"
	orgapp "github.com/celikbros/kapsora/internal/organization/application"
	organizationpg "github.com/celikbros/kapsora/internal/organization/infrastructure/postgres"
	organizationhttp "github.com/celikbros/kapsora/internal/organization/transport/http"
	partyapp "github.com/celikbros/kapsora/internal/party/application"
	partypg "github.com/celikbros/kapsora/internal/party/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/party/memberimport"
	partyhttp "github.com/celikbros/kapsora/internal/party/transport/http"
	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/health"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/idempotency"
	"github.com/celikbros/kapsora/internal/platform/logging"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
	"github.com/celikbros/kapsora/internal/platform/ratelimit"
	pricingapp "github.com/celikbros/kapsora/internal/pricing/application"
	pricingpg "github.com/celikbros/kapsora/internal/pricing/infrastructure/postgres"
	pricinghttp "github.com/celikbros/kapsora/internal/pricing/transport/http"
	providerapp "github.com/celikbros/kapsora/internal/provider/application"
	providerpg "github.com/celikbros/kapsora/internal/provider/infrastructure/postgres"
	providerhttp "github.com/celikbros/kapsora/internal/provider/transport/http"
	reportapp "github.com/celikbros/kapsora/internal/report/application"
	reportgw "github.com/celikbros/kapsora/internal/report/infrastructure/gateway"
	reportpg "github.com/celikbros/kapsora/internal/report/infrastructure/postgres"
	reporthttp "github.com/celikbros/kapsora/internal/report/transport/http"
	rulesapp "github.com/celikbros/kapsora/internal/rules/application"
	rulespg "github.com/celikbros/kapsora/internal/rules/infrastructure/postgres"
	ruleshttp "github.com/celikbros/kapsora/internal/rules/transport/http"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestpg "github.com/celikbros/kapsora/internal/servicerequest/infrastructure/postgres"
	servicerequesthttp "github.com/celikbros/kapsora/internal/servicerequest/transport/http"
	workflowapp "github.com/celikbros/kapsora/internal/workflow/application"
	workflowpg "github.com/celikbros/kapsora/internal/workflow/infrastructure/postgres"
	workflowhttp "github.com/celikbros/kapsora/internal/workflow/transport/http"
)

const serviceName = "kapsora-api"

// Rate limits (v1.2 section 17.5). Login is limited per client address as well as per
// account (the account lockout in the identity domain).
var (
	loginRateLimit = ratelimit.Policy{PerMinute: 10, Burst: 5}
	apiRateLimit   = ratelimit.Policy{PerMinute: 120, Burst: 60}
	// Identifier search is the only endpoint that turns a plaintext identifier into a
	// person, so it gets its own per-actor budget on top of the tenant limit (WP-I2-01).
	identifierSearchRateLimit = ratelimit.Policy{PerMinute: 20, Burst: 10}
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
	if len(cfg.Session.SigningKey) == 0 {
		return errors.New("KAPSORA_COOKIE_SIGNING_KEY is required by kapsora-api; generate one with `go run ./cmd/keygen`")
	}
	logger := logging.New(cfg)
	slog.SetDefault(logger)
	if !cfg.Session.CookieSecure {
		logger.Warn("session cookie is not marked Secure; local HTTP development only")
	}

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

	ident, err := newIdentity(cfg, pool, logger)
	if err != nil {
		return err
	}

	// Field encryption and blind indexes for sensitive identifiers (ADR-020: local key
	// for development and single-node pilots; a KMS-backed provider replaces it later).
	keys, err := localkey.NewFromEnv()
	if err != nil {
		return err
	}
	cursors, err := httpx.NewCursorCodec(cfg.Session.SigningKey)
	if err != nil {
		return err
	}
	orgSvc, err := orgapp.New(orgapp.Deps{
		Pool: pool, Repo: organizationpg.New(), Cipher: keys, Index: keys,
		Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		return err
	}
	partySvc, err := partyapp.New(partyapp.Deps{
		Pool: pool, Repo: partypg.New(), Cipher: keys, Index: keys,
		Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		return err
	}
	benefitSvc, err := benefitapp.New(benefitapp.Deps{
		Pool: pool, Repo: benefitpg.New(), Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		return err
	}
	entitlementSvc, err := benefitledger.New(benefitledger.Deps{
		Pool: pool, Audit: auditpg.New(), Cursors: cursors, Logger: logger,
	})
	if err != nil {
		return err
	}
	catalogSvc, err := catalogapp.New(catalogapp.Deps{
		Pool: pool, Repo: catalogpg.New(), Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		return err
	}
	providerSvc, err := providerapp.New(providerapp.Deps{
		Pool: pool, Repo: providerpg.New(), Cipher: keys, Index: keys,
		Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		return err
	}
	contractSvc, err := contractapp.New(contractapp.Deps{
		Pool: pool, Repo: contractpg.New(), Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		return err
	}
	// The compiled rule programs are cached per rule set version. A published version
	// never changes, so an entry can never go stale; the service refuses to cache a draft.
	pricingPrograms := rulesapp.NewProgramCache(rulesapp.DefaultCacheSize)
	rulesSvc, err := rulesapp.New(rulesapp.Deps{
		Pool: pool, Repo: rulespg.New(), Audit: auditpg.New(), Cursors: cursors,
		Programs: pricingPrograms,
	})
	if err != nil {
		return err
	}
	// The pricing quote sits on top of the contracts, the rules and the entitlement
	// balances and writes to none of them: it reads a consistent picture of all three and
	// stores the number it arrived at. It shares the rule program cache with the rule
	// engine, because a published version compiled once is the same program either caller
	// evaluates.
	pricingSvc, err := pricingapp.New(pricingapp.Deps{
		Pool: pool, Repo: pricingpg.New(), Audit: auditpg.New(),
		Programs: pricingPrograms, Logger: logger,
	})
	if err != nil {
		return err
	}
	// A service request sits on top of the plan, the catalog and the rules and writes to
	// none of them. It shares the rule program cache with the engine and the quote: a
	// published version compiled once is the same program whoever evaluates it.
	serviceRequestSvc, err := servicerequestapp.New(servicerequestapp.Deps{
		Pool: pool, Repo: servicerequestpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Programs: pricingPrograms, Logger: logger,
	})
	if err != nil {
		return err
	}
	// An authorization is where an approval starts costing something. It drives the
	// entitlement ledger of WP-I2-03 and writes no balance itself, so the account lock
	// that makes concurrent holds safe is the one the ledger already takes.
	authorizationSvc, err := authorizationapp.New(authorizationapp.Deps{
		Pool: pool, Repo: authorizationpg.New(), Ledger: entitlementSvc.Ledger(),
		Audit: auditpg.New(), Cursors: cursors, Logger: logger,
	})
	if err != nil {
		return err
	}
	// The health case and the treatment report. It is the module that decides which half of
	// a clinical record a caller is shown, and it decides it in one place — so nothing else
	// here is allowed to hold a health service of its own. Stays is left at its default:
	// WP-I5-03 owns the inpatient stay and until it lands "no stay is open" is the only
	// honest answer.
	//
	// It is built before the worklist because the two are wired to each other: a submitted
	// report raises a work item through healthpg.WorkItems, which writes it in the report
	// command's own transaction, and a reviewer claiming that item starts the review through
	// the claim hook below. Neither package imports the other; this is where they meet.
	//
	// The inpatient stay (WP-I5-03) is wired here too, and it is the only place in the
	// process where the health module meets the request and the authorization modules. It
	// meets them through two narrow ports: it may raise a preauthorization and it may take,
	// extend and give back a hold, and there is no method on either that would let it decide
	// anything about entitlement. The decision itself arrives the other way round — through
	// the outbox, in kapsora-worker — so nothing in this process moves a stay.
	healthSvc, err := healthapp.New(healthapp.Deps{
		Pool: pool, Repo: healthpg.New(), Reports: healthpg.NewReports(),
		StayRepo:       healthpg.NewStays(),
		Requests:       healthgw.NewRequests(serviceRequestSvc),
		Authorizations: healthgw.NewAuthorizations(authorizationSvc),
		WorkItems:      healthpg.NewWorkItems(logger), Audit: auditpg.New(),
		Cursors: cursors, Logger: logger,
	})
	if err != nil {
		return err
	}
	// The worklist is where work that needs a person waits. It writes no business state
	// of any other module: it holds the queues, the items raised into them and the clock
	// each item was given, and every other package reaches it by raising an item.
	workflowSvc, err := workflowapp.New(workflowapp.Deps{
		Pool: pool, Repo: workflowpg.New(), Audit: auditpg.New(), Cursors: cursors,
		ClaimHook: healthSvc, Logger: logger,
	})
	if err != nil {
		return err
	}
	// The claim. It is the only place in the process where the pricing ladder, the rule
	// engine, the authorization hold, the approval policy and the report coverage port meet,
	// and it meets each of them through a narrow port: it may price without storing a quote,
	// evaluate the published ADJUDICATION rules, draw a line's quantity out of a hold and
	// give back what a refused claim was holding, ask which roles may approve an amount, and
	// ask whether a report covers a service. There is no method on any of them that would let
	// a claim decide something the module it belongs to owns.
	//
	// The report coverage port is the health service itself: WP-I5-02's `ReportCoverage` runs
	// inside the caller's transaction and writes the usage row that says a claim leaned on a
	// report, so a claim that rolls back has used nothing.
	claimSvc, err := claimapp.New(claimapp.Deps{
		Pool: pool, Repo: claimpg.New(),
		Pricing:        claimgw.NewPricing(pricingSvc),
		Rules:          claimgw.NewRules(logger),
		Authorizations: claimgw.NewAuthorizations(authorizationSvc),
		Reports:        healthSvc,
		Policies:       claimgw.NewPolicies(workflowSvc),
		WorkItems:      claimgw.NewWorkItems(logger),
		Audit:          auditpg.New(), Cursors: cursors, Logger: logger,
	})
	if err != nil {
		return err
	}
	// The invoice. It knows about exactly one other module, through one port: the claim's
	// INVOICED transition and the way back. Everything else an invoice needs -- the
	// provider's tax identity, the approved total of a claim, whether the image was scanned
	// clean -- it reads for itself, because those are three halves of one screen and asking
	// three services for them would be three round trips that could disagree.
	//
	// The port runs inside the invoice's own transaction, so an invoice that is SUBMITTED
	// and claims that never moved is a state no reader observes.
	billingSvc, err := billingapp.New(billingapp.Deps{
		Pool: pool, Repo: billingpg.New(), Batches: billingpg.NewBatchRepository(),
		Settlements: billingpg.NewSettlementRepository(),
		Claims:      billinggw.NewClaims(claimSvc),
		// The icmal's review queue, written from the billing side of the boundary rather than
		// through the worklist service, because that service opens a transaction of its own.
		WorkItems: billingpg.NewWorkItems(logger),
		// The member's wallet, for the reimbursement: an approval consumes exactly the
		// approved amount inside the decision's own transaction (WP-I7-04 §2.4).
		Entitlements: billinggw.NewEntitlements(entitlementSvc.Ledger()),
		// The payment adapter of M9. This one records the order and does nothing, which is
		// what a platform that transfers no money should do (v1.2 §4.3).
		Payments: billingapp.RecordingPaymentOrders{},
		// The platform cipher, the same one a person identifier is written through. The
		// member's IBAN reaches the database as its envelope or it does not reach it at all.
		Cipher: keys,
		Audit:  auditpg.New(), Cursors: cursors, Logger: logger,
	})
	if err != nil {
		return err
	}
	memberImports, err := memberimport.New(memberimport.Deps{
		Pool: pool, Cipher: keys, Index: keys, Audit: auditpg.New(), Cursors: cursors, Logger: logger,
	})
	if err != nil {
		return err
	}

	// Notifications. This process holds no channel adapter at all: publishing a template
	// and resending a message both write a row and an outbox event, and the worker is the
	// only thing that talks to a mail server. An adapter wired in here would be one the API
	// never calls, and an HTTP request must never be able to wait on a relay.
	notificationSvc, err := notificationapp.New(notificationapp.Deps{
		Pool: pool, Repo: notificationpg.New(nil), Audit: auditpg.New(), Cursors: cursors,
		LinkBase: cfg.Notifications.LinkBase, Logger: logger,
	})
	if err != nil {
		return err
	}

	// Documents. The API hands out presigned URLs and never touches a file body: this
	// process needs the object store to sign them, and no scanner at all — scanning is the
	// worker's job, and a scanner wired in here would be one this process never calls.
	documentSvc, err := newDocuments(cfg, pool, cursors, logger)
	if err != nil {
		return err
	}

	// Reporting (WP-I7-05): the cari ekstre, the reconciliation runs, the operations dashboard
	// and the exports.
	//
	// It is given the document store so a finished export can be downloaded through WP-I4-04's
	// own path -- which is what writes the file's access event and refuses a purged one -- and
	// deliberately no work item port: the reconciliation runs in the scheduler, and an API
	// process that could write a run would be an API process in which a request could quietly
	// mark settlements RECONCILED.
	reportSvc, err := reportapp.New(reportapp.Deps{
		Pool: pool, Repo: reportpg.New(),
		Documents: reportgw.NewDocuments(documentSvc),
		Audit:     auditpg.New(), Cursors: cursors, Logger: logger,
	})
	if err != nil {
		return err
	}
	// The eligibility service shares the entitlement movement engine, so a check that
	// opens an account lazily and a reservation on the same account run the same code.
	eligibilitySvc, err := benefiteligibility.New(benefiteligibility.Deps{
		Pool: pool, Audit: auditpg.New(), Ledger: entitlementSvc.Ledger(), Logger: logger,
	})
	if err != nil {
		return err
	}

	// The accommodation vertical. It leans on the eligibility service rather than
	// re-deriving what a member is entitled to: every availability search runs one check
	// and keeps its evaluation id, so months later "what did the system show them" is a
	// row somebody can read rather than a guess. The prices go through the same contract
	// selection and the same calculator a counter quote uses, per night, summed once.
	//
	// The booking half reaches three other modules through narrow ports and nothing wider:
	// it may raise a RESERVATION request and hand it to WP-I4-01's gate, take an
	// authorization for an approved one, and freeze WP-I6-04's lodging terms. The movement
	// engine is the one this process already holds, not a second one — two ledgers over one
	// database would take two different account locks for one account, which is exactly the
	// way a no-double-spend rule stops holding.
	accommodationSvc, err := accommodationapp.New(accommodationapp.Deps{
		Pool: pool, Repo: accommodationpg.New(), Bookings: accommodationpg.NewBookings(),
		Ledger:         entitlementSvc.Ledger(),
		Eligibility:    eligibilitySvc,
		Requests:       accommodationgw.NewRequests(serviceRequestSvc),
		Authorizations: accommodationgw.NewAuthorizations(authorizationSvc),
		Policies:       accommodationgw.NewPolicies(contractSvc),
		// A disputed no-show is work somebody has to look at, raised into WP-I4-03's
		// reservation review queue inside the review's own transaction.
		WorkItems: accommodationpg.NewWorkItems(logger),
		Audit:     auditpg.New(), Cursors: cursors, Logger: logger,
	})
	if err != nil {
		return err
	}

	checker, err := newReadiness(health.PostgresCheck(pool), cfg.Documents)
	if err != nil {
		return err
	}

	router := newRouter(routerDeps{
		cfg:            cfg,
		pool:           pool,
		logger:         logger,
		checker:        checker,
		ident:          ident,
		orgs:           orgSvc,
		party:          partySvc,
		benefit:        benefitSvc,
		catalog:        catalogSvc,
		providers:      providerSvc,
		contracts:      contractSvc,
		rules:          rulesSvc,
		pricing:        pricingSvc,
		requests:       serviceRequestSvc,
		authorizations: authorizationSvc,
		workflows:      workflowSvc,
		documents:      documentSvc,
		health:         healthSvc,
		claims:         claimSvc,
		billing:        billingSvc,
		reports:        reportSvc,
		accommodation:  accommodationSvc,
		notifications:  notificationSvc,
		entitlements:   entitlementSvc,
		eligibility:    eligibilitySvc,
		imports:        memberImports,
		limiter:        ratelimit.NewPostgres(pool),
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", cfg.HTTPAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	logger.Info("http server stopped")
	return nil
}

// newDocuments builds the document service for a process that serves the API. The scanner
// is deliberately absent: only kapsora-worker scans, and ScanObject refuses to run without
// one rather than ever treating a missing scanner as a clean verdict.
func newDocuments(cfg config.Config, pool *pgxpool.Pool, cursors *httpx.CursorCodec,
	logger *slog.Logger,
) (*documentapp.Service, error) {
	if !cfg.Documents.Configured() {
		// The document routes are part of the contract, so the API cannot serve a version
		// of itself without them. Refusing at start-up is the honest answer; the
		// alternative is an endpoint that exists and 500s.
		return nil, errors.New("kapsora-api: the document API needs an object store; set KAPSORA_MINIO_ROOT_USER and KAPSORA_MINIO_ROOT_PASSWORD (see .env.example) and start it with scripts/native/up")
	}
	store, err := objectstore.NewS3(objectstore.S3Options{
		Endpoint: cfg.Documents.Endpoint, Region: cfg.Documents.Region,
		AccessKey: cfg.Documents.AccessKey, SecretKey: cfg.Documents.SecretKey,
	})
	if err != nil {
		return nil, err
	}
	return documentapp.New(documentapp.Deps{
		Pool: pool, Repo: documentpg.New(), Store: store, Audit: auditpg.New(),
		Cursors: cursors, Logger: logger,
		Storage: documentapp.Storage{
			QuarantineBucket: cfg.Documents.QuarantineBucket,
			SecureBucket:     cfg.Documents.SecureBucket,
			UploadTTL:        cfg.Documents.UploadURLTTL,
			DownloadTTL:      cfg.Documents.DownloadURLTTL,
			EncryptionKeyRef: cfg.Documents.EncryptionKeyRef,
		},
	})
}

// identityDeps bundles the identity module's services for the router.
type identityDeps struct {
	service *application.Service
	authz   *application.Authorizer
}

func newIdentity(cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) (identityDeps, error) {
	policy := domain.DefaultPolicy()
	policy.IdleTimeout = cfg.Session.IdleTimeout
	policy.AbsoluteLifetime = cfg.Session.AbsoluteLifetime
	policy.StepUpWindow = cfg.Session.StepUpWindow

	sessions := identitypg.NewSessionStore(pool)
	sink := identitypg.NewAuditSink(pool, auditpg.New(), logger)

	svc, err := application.New(application.Deps{
		Credentials: identitypg.NewCredentialRepository(pool),
		Sessions:    sessions,
		Audit:       sink,
		Policy:      policy,
		Lockout:     domain.DefaultLockout(),
	})
	if err != nil {
		return identityDeps{}, err
	}
	authz := application.NewAuthorizer(identitypg.NewAuthorizationRepository(pool), sessions, sink, nil)
	return identityDeps{service: svc, authz: authz}, nil
}

type routerDeps struct {
	cfg            config.Config
	pool           *pgxpool.Pool
	logger         *slog.Logger
	checker        *health.Checker
	ident          identityDeps
	orgs           *orgapp.Service
	party          *partyapp.Service
	benefit        *benefitapp.Service
	catalog        *catalogapp.Service
	providers      *providerapp.Service
	contracts      *contractapp.Service
	rules          *rulesapp.Service
	pricing        *pricingapp.Service
	requests       *servicerequestapp.Service
	authorizations *authorizationapp.Service
	workflows      *workflowapp.Service
	documents      *documentapp.Service
	health         *healthapp.Service
	claims         *claimapp.Service
	billing        *billingapp.Service
	reports        *reportapp.Service
	accommodation  *accommodationapp.Service
	notifications  *notificationapp.Service
	entitlements   *benefitledger.Service
	eligibility    *benefiteligibility.Service
	imports        *memberimport.Service
	limiter        ratelimit.Limiter
}

func newRouter(d routerDeps) http.Handler {
	cookies := identityhttp.CookieConfig{Secure: d.cfg.Session.CookieSecure}
	signingKey := d.cfg.Session.SigningKey
	sessions := identityhttp.NewMiddleware(d.ident.service, cookies, signingKey, d.logger).WithAuthorizer(d.ident.authz)
	sessionHandler := identityhttp.NewHandler(d.ident.service, cookies, signingKey, d.logger)
	contextHandler := identityhttp.NewContextHandler(d.ident.service, d.ident.authz, d.logger)

	r := chi.NewRouter()
	r.Use(httpx.RequestID)
	// Client IP is taken from RemoteAddr; proxy headers are only trusted once the
	// ingress/WAF configuration is known (chi's RealIP is spoofable, GHSA-3fxj-6jh8-hvhx).
	r.Use(audithttp.RequestMeta)
	r.Use(httpx.Recoverer(d.logger))
	r.Use(httpx.RequestLogger(d.logger))
	r.Use(middleware.Timeout(30 * time.Second))

	r.NotFound(httpx.NotFoundHandler())
	r.MethodNotAllowed(httpx.MethodNotAllowedHandler())

	r.Get("/health/live", health.LiveHandler())
	r.Get("/health/ready", health.ReadyHandler(d.checker))

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(sessions.LoadSession)

		// Login is rate limited per client address before any password work happens.
		api.With(ratelimit.Middleware(d.limiter, ratelimit.ScopedKey("session.login", anonymousScope), loginRateLimit, d.logger)).
			Post("/session/login", sessionHandler.Login)

		// Pre-tenant routes: the session cookie plus a matching CSRF token on writes.
		api.Group(func(authed chi.Router) {
			authed.Use(sessions.RequireCSRF)
			authed.Use(ratelimit.Middleware(d.limiter, ratelimit.ScopedKey("session", sessionScope), apiRateLimit, d.logger))
			authed.Get("/session", sessionHandler.GetSession)
			authed.Post("/session/logout", sessionHandler.Logout)
			authed.Post("/session/step-up", sessionHandler.StepUp)
			authed.Post("/session/password", sessionHandler.ChangePassword)
			authed.Post("/session/switch-tenant", contextHandler.SwitchTenant)
			authed.Get("/me", contextHandler.GetMe)
			authed.Get("/tenants", contextHandler.ListTenants)
		})

		// Tenant-scoped routes: everything above plus a validated X-Tenant-ID and a
		// resolved identity.RequestContext. Party, benefit and service modules mount
		// here as they land.
		api.Group(func(tenant chi.Router) {
			tenant.Use(sessions.RequireCSRF)
			tenant.Use(sessions.RequireTenantContext)
			tenant.Use(ratelimit.Middleware(d.limiter, ratelimit.ScopedKey("api", tenantScope), apiRateLimit, d.logger))

			orgHandler := organizationhttp.NewHandler(d.orgs, sessions, d.logger)
			tenant.Route("/organizations", func(r chi.Router) {
				orgHandler.Routes(r, d.idempotent("organization.create"))
			})

			// The party handler answers getMyPerson, which needs the member's enrollments
			// beside their name; the benefit service is the reader it asks.
			partyHandler := partyhttp.NewHandler(d.party, sessions, d.logger).WithEnrollments(d.benefit)
			benefitHandler := benefithttp.NewHandler(d.benefit, sessions, d.logger)
			entitlementHandler := benefithttp.NewEntitlementHandler(d.entitlements, sessions, d.logger)
			entitlementMW := benefithttp.EntitlementMiddlewares{
				CreateAdjustment: d.idempotent("entitlement_adjustment.create"),
			}
			benefitMW := benefithttp.Middlewares{
				CreateProgram:     d.idempotent("program.create"),
				CreatePlan:        d.idempotent("plan.create"),
				CreatePlanVersion: d.idempotent("plan_version.create"),
				CreateEnrollment:  d.idempotent("enrollment.create"),
			}
			// /people carries the party routes and the benefit module's person-scoped
			// enrollment routes; the sub-patterns do not collide.
			tenant.Route("/people", func(r chi.Router) {
				partyHandler.Routes(r, partyhttp.Middlewares{
					CreatePerson:       d.idempotent("person.create"),
					CreateRelationship: d.idempotent("relationship.create"),
					CreateMembership:   d.idempotent("membership.create"),
					Search: ratelimit.Middleware(d.limiter,
						ratelimit.ScopedKey("member.identifier.search", tenantScope), identifierSearchRateLimit, d.logger),
				})
				benefitHandler.PersonRoutes(r, benefitMW)
				entitlementHandler.PersonRoutes(r)
			})
			tenant.Route("/party", partyHandler.CatalogRoutes)
			// /me/person is tenant-scoped even though /me is not: which person an account
			// acts for is a fact about one tenant's grants, and the same actor may be a
			// member in one tenant and a reviewer in another. It is registered as a flat
			// pattern rather than a chi.Route("/me", ...) subtree, which would shadow the
			// pre-tenant GET /me above it.
			partyHandler.MyPersonRoutes(tenant)

			tenant.Route("/programs", func(r chi.Router) { benefitHandler.ProgramRoutes(r, benefitMW) })
			tenant.Route("/plans", func(r chi.Router) { benefitHandler.PlanRoutes(r, benefitMW) })
			tenant.Route("/plan-versions", benefitHandler.PlanVersionRoutes)
			tenant.Route("/enrollments", benefitHandler.EnrollmentRoutes)
			tenant.Route("/entitlement-accounts", func(r chi.Router) {
				entitlementHandler.AccountRoutes(r, entitlementMW)
			})
			tenant.Route("/entitlement-adjustments", entitlementHandler.AdjustmentRoutes)

			// The catalog is the vocabulary every later module speaks, so it mounts
			// beside the benefit routes rather than under them.
			catalogHandler := cataloghttp.NewHandler(d.catalog, sessions, d.logger)
			catalogMW := cataloghttp.Middlewares{
				CreateCategory:   d.idempotent("catalog_category.create"),
				CreateDefinition: d.idempotent("service_definition.create"),
				CreateCodeSystem: d.idempotent("code_system.create"),
				// A 5000 row code value batch is far larger than any other command body,
				// so the replay record hashes a bigger request than the default allows.
				ImportCodeValues: d.idempotentLarge("code_value.import", codeValueImportBodyLimit),
			}
			tenant.Route("/service-categories", func(r chi.Router) { catalogHandler.CategoryRoutes(r, catalogMW) })
			tenant.Route("/service-definitions", func(r chi.Router) { catalogHandler.DefinitionRoutes(r, catalogMW) })
			tenant.Route("/code-systems", func(r chi.Router) { catalogHandler.CodeSystemRoutes(r, catalogMW) })

			// The provider network sits beside the catalog it points at: locations and
			// practitioners are addressed directly, because a UI reaches them from a
			// search result rather than by walking down from a provider.
			providerHandler := providerhttp.NewHandler(d.providers, sessions, d.logger)
			providerMW := providerhttp.Middlewares{
				CreateProvider:     d.idempotent("provider_profile.create"),
				CreateLocation:     d.idempotent("provider_location.create"),
				CreatePractitioner: d.idempotent("practitioner.create"),
				// Searching a registration number turns a professional identity number
				// into a practitioner, so it gets the same per-actor budget as the member
				// identifier search.
				SearchRegistration: ratelimit.Middleware(d.limiter,
					ratelimit.ScopedKey("provider.practitioner.search", tenantScope), identifierSearchRateLimit, d.logger),
			}
			// The claim handler is built here rather than beside the claim routes because
			// one of its endpoints lives under /providers: a provider's earnings are summed
			// from claims and asked about a provider, and both halves of that sentence are
			// true. chi mounts one subrouter per prefix, so the route has to be registered
			// inside this closure.
			claimHandler := claimhttp.NewHandler(d.claims, sessions, d.logger)
			// The reporting endpoints (WP-I7-05). The statement hangs off the provider
			// because that is whose statement it is; the runs, the dashboard and the exports
			// have prefixes of their own.
			//
			// `createExport` takes a mandatory idempotency key: an export replayed by a flaky
			// network must queue one job and produce one watermarked file. `downloadExport`
			// takes the optional variant, because a download really repeated is a second
			// download -- counted and audited twice on purpose -- while a client that sends a
			// key gets its first answer back rather than a second access event for one click.
			reportHandler := reporthttp.NewHandler(d.reports, sessions, d.logger)
			reportingMW := reporthttp.Middlewares{
				CreateExport:   d.idempotent("report.export.create"),
				DownloadExport: d.idempotentOptional("report.export.download"),
			}

			tenant.Route("/providers", func(r chi.Router) {
				providerHandler.ProviderRoutes(r, providerMW)
				claimHandler.EarningsRoutes(r)
				reportHandler.StatementRoutes(r)
			})
			tenant.Route("/reconciliation-runs", reportHandler.ReconciliationRoutes)
			tenant.Route("/operations", reportHandler.DashboardRoutes)
			tenant.Route("/exports", func(r chi.Router) {
				reportHandler.ExportRoutes(r, reportingMW)
			})
			tenant.Route("/provider-locations", providerHandler.LocationRoutes)
			tenant.Route("/practitioners", func(r chi.Router) { providerHandler.PractitionerRoutes(r, providerMW) })

			// Contracts sit on top of the provider network and the catalog: a price
			// item names a service or a category and may be tied to one location, and
			// only a published version is ever read by a quote, an authorization or a
			// claim.
			contractHandler := contracthttp.NewHandler(d.contracts, sessions, d.logger)
			contractMW := contracthttp.Middlewares{
				CreateContract:        d.idempotent("contract.create"),
				CreateContractVersion: d.idempotent("contract_version.create"),
			}
			tenant.Route("/contracts", func(r chi.Router) { contractHandler.ContractRoutes(r, contractMW) })
			tenant.Route("/contract-versions", contractHandler.VersionRoutes)
			tenant.Route("/price-lists", contractHandler.PriceListRoutes)
			// The price lookup is a single literal path segment rather than a
			// sub-resource, so it is registered on the tenant router itself.
			contractHandler.PriceRoutes(tenant)

			// The rule engine sits beside the contracts: both are configuration a
			// second person approves, and a published version of either is what a later
			// decision is measured against. Simulation and the test run write nothing, so
			// neither carries an Idempotency-Key.
			rulesHandler := ruleshttp.NewHandler(d.rules, sessions, d.logger)
			rulesMW := ruleshttp.Middlewares{
				CreateRuleSet:        d.idempotent("rule_set.create"),
				CreateRuleSetVersion: d.idempotent("rule_set_version.create"),
			}
			tenant.Route("/rule-sets", func(r chi.Router) { rulesHandler.RuleSetRoutes(r, rulesMW) })
			tenant.Route("/rule-set-versions", rulesHandler.VersionRoutes)
			tenant.Route("/rule-evaluations", rulesHandler.EvaluationRoutes)

			// A quote is priced against the contracts, the rules and the balances above
			// it and changes none of them. Its replay contract lives in
			// contract.price_quote.idempotency_key, so the Idempotency-Key middleware is
			// not applied here either.
			pricingHandler := pricinghttp.NewHandler(d.pricing, sessions, d.logger)
			tenant.Route("/pricing", pricingHandler.Routes)

			// A request is how anything gets asked for. Every move through its lifecycle is
			// a command of its own with its own permission and its own reason, so each one
			// is a route rather than a status a caller could write, and each one carries an
			// Idempotency-Key: a retried browser submit must not decide a request twice.
			requestHandler := servicerequesthttp.NewHandler(d.requests, sessions, d.logger)
			requestMW := servicerequesthttp.Middlewares{
				Create:           d.idempotent("service_request.create"),
				Submit:           d.idempotent("service_request.submit"),
				Return:           d.idempotent("service_request.return"),
				Reject:           d.idempotent("service_request.reject"),
				Approve:          d.idempotent("service_request.approve"),
				PartiallyApprove: d.idempotent("service_request.partially_approve"),
				Cancel:           d.idempotent("service_request.cancel"),
			}
			tenant.Route("/service-requests", func(r chi.Router) { requestHandler.Routes(r, requestMW) })

			// An approval becomes a promise here, and the promise costs entitlement. Every
			// command carries an Idempotency-Key: a retried browser submit must not reserve
			// the same balance twice, and a retried redemption must not spend a voucher
			// twice. /vouchers:redeem is mounted on the tenant router itself because the
			// contract path is one literal segment rather than a sub-resource. issueVoucher
			// is the one command with no Idempotency-Key: the middleware stores the response
			// body for replay, and that response is the only place a voucher's plaintext
			// ever exists.
			authorizationHandler := authorizationhttp.NewHandler(d.authorizations, sessions, d.logger)
			authorizationMW := authorizationhttp.Middlewares{
				CreateAuthorization: d.idempotent("authorization.create"),
				ExtendAuthorization: d.idempotent("authorization.extend"),
				CancelAuthorization: d.idempotent("authorization.cancel"),
				CreateFulfilment:    d.idempotent("fulfilment.create"),
				CompleteFulfilment:  d.idempotent("fulfilment.complete"),
				CancelFulfilment:    d.idempotent("fulfilment.cancel"),
				RedeemVoucher:       d.idempotent("voucher.redeem"),
			}
			tenant.Route("/authorizations", func(r chi.Router) {
				authorizationHandler.AuthorizationRoutes(r, authorizationMW)
			})
			tenant.Route("/fulfilments", func(r chi.Router) {
				authorizationHandler.FulfilmentRoutes(r, authorizationMW)
			})
			authorizationHandler.VoucherRoutes(tenant, authorizationMW)

			// The worklist: what is waiting, what is mine, what is late. Claiming carries
			// an Idempotency-Key like every other command, but it is If-Match that makes
			// two people unable to own one item — the key answers a retry, the version
			// answers a race.
			workflowHandler := workflowhttp.NewHandler(d.workflows, sessions, d.logger)
			workflowMW := workflowhttp.Middlewares{
				CreateQueue:  d.idempotent("work_queue.create"),
				ClaimItem:    d.idempotent("work_item.claim"),
				ReleaseItem:  d.idempotent("work_item.release"),
				ReassignItem: d.idempotent("work_item.reassign"),
				CompleteItem: d.idempotent("work_item.complete"),
				AddComment:   d.idempotent("work_item.comment"),
				PutPolicies:  d.idempotent("approval_policy.put"),
			}
			tenant.Route("/work-queues", func(r chi.Router) {
				workflowHandler.QueueRoutes(r, workflowMW)
			})
			tenant.Route("/work-items", func(r chi.Router) {
				workflowHandler.ItemRoutes(r, workflowMW)
			})
			tenant.Route("/approval-policies", func(r chi.Router) {
				workflowHandler.PolicyRoutes(r, workflowMW)
			})

			// Documents. Every byte travels between the client and the object store
			// directly: createUpload answers a presigned PUT into the quarantine bucket and
			// downloadDocument a presigned GET out of the secure one, and nothing in
			// between ever holds a file. downloadDocument carries no Idempotency-Key —
			// it changes no state, and minting the same URL twice is what a retry should
			// do — but it is a POST, because it hands out a bearer credential and writes an
			// access event with the reason it was asked for.
			documentHandler := documenthttp.NewHandler(d.documents, sessions, d.logger)
			documentMW := documenthttp.Middlewares{
				CreateUpload:     d.idempotent("document.upload_create"),
				CompleteUpload:   d.idempotent("document.upload_complete"),
				LinkDocument:     d.idempotent("document.link_create"),
				PutLegalHold:     d.idempotent("legal_hold.place"),
				ReleaseLegalHold: d.idempotent("legal_hold.release"),
			}
			tenant.Route("/documents", func(r chi.Router) {
				documentHandler.DocumentRoutes(r, documentMW)
			})
			tenant.Route("/legal-holds", func(r chi.Router) {
				documentHandler.LegalHoldRoutes(r, documentMW)
			})

			// The health case. Every read below is served in one of two projections and the
			// projection is chosen in the application service, so a screen never has to be
			// trusted to drop a field: what a sponsor's HR user may not see never leaves
			// this process. The routes carry X-Access-Purpose and X-Access-Reason, which is
			// how a sensitive read says why it happened; the access event carries them on.
			healthHandler := healthhttp.NewHandler(d.health, sessions, d.logger)
			healthMW := healthhttp.Middlewares{
				CreateCase:      d.idempotent("health_case.create"),
				CloseCase:       d.idempotent("health_case.close"),
				CreateEncounter: d.idempotent("health_encounter.create"),
				EndEncounter:    d.idempotent("health_encounter.end"),
				PutDiagnoses:    d.idempotent("health_diagnosis.put"),
			}
			tenant.Route("/health-cases", func(r chi.Router) {
				healthHandler.CaseRoutes(r, healthMW)
			})
			tenant.Route("/encounters", func(r chi.Router) {
				healthHandler.EncounterRoutes(r, healthMW)
			})
			tenant.Route("/health-access-log", healthHandler.AccessLogRoutes)

			// The inpatient stay. Every command takes If-Match and an idempotency key: an
			// admission replayed by a flaky network reserves entitlement once, and a
			// discharge replayed releases once. There is no approve route — the reviewer
			// decides the admission on the request page, and the stay follows through the
			// outbox subscription kapsora-worker registers.
			stayMW := healthhttp.StayMiddlewares{
				CreateStay:  d.idempotent("inpatient_stay.create"),
				ExtendStay:  d.idempotent("inpatient_stay.extend"),
				PutSegments: d.idempotent("inpatient_stay.segments.put"),
				Discharge:   d.idempotent("inpatient_stay.discharge"),
				CancelStay:  d.idempotent("inpatient_stay.cancel"),
			}
			tenant.Route("/inpatient-stays", func(r chi.Router) {
				healthHandler.StayRoutes(r, stayMW)
			})

			// The treatment report. Everything that moves one takes If-Match, and every
			// command that changes state is idempotency-keyed: a submission replayed by a
			// flaky network raises one work item, not two.
			reportMW := healthhttp.ReportMiddlewares{
				CreateReport: d.idempotent("medical_report.create"),
				PatchReport:  d.idempotent("medical_report.update"),
				PutServices:  d.idempotent("medical_report.services.put"),
				SubmitReport: d.idempotent("medical_report.submit"),
				StartReview:  d.idempotent("medical_report.start_review"),
				Decide:       d.idempotent("medical_report.decide"),
				CancelReport: d.idempotent("medical_report.cancel"),
			}
			tenant.Route("/medical-reports", func(r chi.Router) {
				healthHandler.ReportRoutes(r, reportMW)
			})

			// The claim. Every command takes If-Match and an idempotency key: a submit
			// replayed by a flaky network consumes a hold once and raises one work item, and
			// a decision replayed writes one decision rather than two rows a reviewer would
			// have to explain. Every read is served in the projection the caller has earned,
			// chosen in the application service — so a sponsor's HR user reading a claim
			// never receives a line description, whatever the screen asks for.
			claimMW := claimhttp.Middlewares{
				CreateClaim:  d.idempotent("claim.create"),
				PatchClaim:   d.idempotent("claim.update"),
				PutLines:     d.idempotent("claim.lines.put"),
				SubmitClaim:  d.idempotent("claim.submit"),
				DecideLines:  d.idempotent("claim.lines.decide"),
				ApproveClaim: d.idempotent("claim.approve"),
				RejectClaim:  d.idempotent("claim.reject"),
				ReturnClaim:  d.idempotent("claim.return"),
				CancelClaim:  d.idempotent("claim.cancel"),
				// An adjustment replayed by a flaky network must be one row: two would take
				// the same money off the same claim twice.
				CreateAdjustment: d.idempotent("claim.adjust"),
			}
			tenant.Route("/claims", func(r chi.Router) {
				claimHandler.Routes(r, claimMW)
			})

			// The invoice a provider raised elsewhere, and the claims it collects. Every
			// command takes If-Match and an idempotency key, and both matter more here than
			// almost anywhere else in the platform: a submit replayed by a flaky network
			// must move one invoice, move its claims once and publish one event, and a
			// cancel replayed must release one set of claims rather than dragging a claim
			// that has meanwhile gone onto the correction back off it.
			invoiceHandler := billinghttp.NewHandler(d.billing, sessions, d.logger)
			invoiceMW := billinghttp.Middlewares{
				CreateInvoice:  d.idempotent("invoice.create"),
				PatchInvoice:   d.idempotent("invoice.update"),
				PutAllocations: d.idempotent("invoice.allocations.put"),
				SubmitInvoice:  d.idempotent("invoice.submit"),
				CancelInvoice:  d.idempotent("invoice.cancel"),
			}
			tenant.Route("/invoices", func(r chi.Router) {
				invoiceHandler.Routes(r, invoiceMW)
			})

			// The icmal: one provider's submitted invoices for one payer, one currency and
			// one period, decided invoice by invoice by the payer's finance. It is served by
			// the same handler as the invoice because the two are one module and one
			// transaction boundary -- a decision cuts the claims of an invoice, and neither
			// half can commit without the other.
			//
			// Every command takes If-Match and an idempotency key. A submit replayed must
			// raise one work item, a decision replayed must reverse one adjustment and write
			// one, and a decide replayed must publish one event that a settlement opens on.
			batchMW := billinghttp.BatchMiddlewares{
				CreateBatch:        d.idempotent("batch.create"),
				PutBatchInvoices:   d.idempotent("batch.invoices.put"),
				SubmitBatch:        d.idempotent("batch.submit"),
				ReviewBatchInvoice: d.idempotent("batch.invoice.review"),
				DecideBatch:        d.idempotent("batch.decide"),
			}
			tenant.Route("/batches", func(r chi.Router) {
				invoiceHandler.BatchRoutes(r, batchMW)
			})

			// The settlement, its payment records and the member's reimbursement. Served by
			// the same handler as the invoice and the icmal for the same reason: the three are
			// one module and one transaction boundary -- an approved reimbursement consumes a
			// wallet and writes a claim, and neither half can commit without the other.
			//
			// Every command takes an idempotency key and every one that changes an existing
			// row takes If-Match. An approval replayed must release one settlement once, a
			// payment record replayed must record one transfer once, and a decision replayed
			// must consume one member's wallet once.
			settlementMW := billinghttp.SettlementMiddlewares{
				ApproveSettlement:          d.idempotent("settlement.approve"),
				CancelSettlement:           d.idempotent("settlement.cancel"),
				CreatePaymentRecord:        d.idempotent("settlement.payment.record"),
				CreateReimbursement:        d.idempotent("reimbursement.create"),
				SubmitReimbursement:        d.idempotent("reimbursement.submit"),
				DecideReimbursement:        d.idempotent("reimbursement.decide"),
				RecordReimbursementPayment: d.idempotent("reimbursement.payment.record"),
			}
			tenant.Route("/settlements", func(r chi.Router) {
				invoiceHandler.SettlementRoutes(r, settlementMW)
			})
			tenant.Route("/reimbursements", func(r chi.Router) {
				invoiceHandler.ReimbursementRoutes(r, settlementMW)
			})
			// The member's own list, resolved through the PERSON scope and nowhere else.
			tenant.Route("/me/reimbursements", func(r chi.Router) {
				invoiceHandler.MyReimbursementRoutes(r)
			})

			// The accommodation vertical: the buildings, the room types, the daily
			// allotment and the availability search. The three prefixes sit side by side
			// because a provider clerk opening a season walks all of them in one sitting.
			//
			// The search is a POST because it carries a body, and it changes nothing: it
			// reserves no room and moves no balance. It does write one thing — the
			// immutable eligibility evaluation that records what the member was shown —
			// and that is why it takes an idempotency key: a search replayed by a flaky
			// network should leave one record of one answer, not two.
			accommodationHandler := accommodationhttp.NewHandler(d.accommodation, sessions, d.logger)
			accommodationMW := accommodationhttp.Middlewares{
				CreateProperty: d.idempotent("accommodation.property.create"),
				PatchProperty:  d.idempotent("accommodation.property.update"),
				CreateRoomType: d.idempotent("accommodation.room_type.create"),
				PatchRoomType:  d.idempotent("accommodation.room_type.update"),
				PutInventory:   d.idempotentOptional("accommodation.inventory.put"),
				Search:         d.idempotentOptional("accommodation.availability.search"),
			}
			tenant.Route("/accommodation/properties", func(r chi.Router) {
				accommodationHandler.PropertyRoutes(r, accommodationMW)
			})
			tenant.Route("/accommodation/room-types", func(r chi.Router) {
				accommodationHandler.RoomTypeRoutes(r, accommodationMW)
			})
			tenant.Route("/accommodation/availability", func(r chi.Router) {
				accommodationHandler.AvailabilityRoutes(r, accommodationMW)
			})
			// The hold and the booking. Every command that changes state carries an
			// Idempotency-Key — a retried hold must not set aside two rooms and a retried
			// confirmation must not raise two requests — with exactly one exception, and it
			// is the same exception WP-I4-02 makes: the voucher route is not wrapped,
			// because the middleware persists response bodies for replay and that response
			// is the only place in the system a usable voucher token exists. Wrapping it
			// would write the token into a column.
			bookingMW := accommodationhttp.BookingMiddlewares{
				CreateHold:  d.idempotent("accommodation.booking.hold"),
				Confirm:     d.idempotent("accommodation.booking.confirm"),
				ReleaseHold: d.idempotent("accommodation.booking.release"),
			}
			tenant.Route("/accommodation/holds", func(r chi.Router) {
				accommodationHandler.HoldRoutes(r, bookingMW)
			})
			// What happens after the promise (WP-I6-03). The check-in route is the second
			// exception to the Idempotency-Key rule, and it is the same reason: its request
			// body carries a usable voucher token, and the middleware persists request and
			// response bodies for replay. A replayed check-in is refused by the voucher's
			// own status instead, which is a better answer than a stored one.
			afterMW := accommodationhttp.AfterMiddlewares{
				Cancel:              d.idempotent("accommodation.booking.cancel"),
				CheckOut:            d.idempotent("accommodation.booking.check_out"),
				ReportNoShow:        d.idempotent("accommodation.booking.no_show.report"),
				ReviewNoShow:        d.idempotent("accommodation.booking.no_show.review"),
				JoinWaitlist:        d.idempotent("accommodation.waitlist.join"),
				CancelWaitlistEntry: d.idempotent("accommodation.waitlist.cancel"),
				AcceptWaitlistOffer: d.idempotent("accommodation.waitlist.accept"),
			}
			tenant.Route("/accommodation/bookings", func(r chi.Router) {
				accommodationHandler.BookingRoutes(r, bookingMW)
				accommodationHandler.AfterBookingRoutes(r, afterMW)
			})
			tenant.Route("/accommodation/waitlist", func(r chi.Router) {
				accommodationHandler.WaitlistRoutes(r, afterMW)
			})

			// Notifications. Templates are configuration, the message log is a record of
			// what members were actually told, and preferences are what they asked for;
			// the three sit side by side because an operator answering "was this member
			// told, and why not" walks all three. Nothing here sends anything: publish and
			// resend write a row and an outbox event, and the worker does the rest.
			notificationHandler := notificationhttp.NewHandler(d.notifications, sessions, d.logger)
			notificationMW := notificationhttp.Middlewares{
				CreateTemplate:  d.idempotent("notification_template.create"),
				PublishTemplate: d.idempotent("notification_template.publish"),
				ResendMessage:   d.idempotent("notification_message.resend"),
				PutPreferences:  d.idempotent("notification_preference.put"),
			}
			tenant.Route("/notification-templates", func(r chi.Router) {
				notificationHandler.TemplateRoutes(r, notificationMW)
			})
			tenant.Route("/notification-messages", func(r chi.Router) {
				notificationHandler.MessageRoutes(r, notificationMW)
			})
			tenant.Route("/notification-preferences", func(r chi.Router) {
				notificationHandler.PreferenceRoutes(r, notificationMW)
			})

			// Eligibility checks change no business state and carry their own replay
			// contract in benefit.eligibility_evaluation.idempotency_key, so the
			// Idempotency-Key middleware is not applied to them.
			eligibilityHandler := benefithttp.NewEligibilityHandler(d.eligibility, sessions, d.logger)
			tenant.Route("/eligibility", eligibilityHandler.Routes)

			// Member import: the upload is idempotent by key as well as by file hash, so a
			// retried browser submit cannot create a second batch.
			importHandler := partyhttp.NewImportHandler(d.imports, sessions, d.logger)
			tenant.Route("/imports/members", func(r chi.Router) {
				importHandler.Routes(r, d.idempotentLarge("member_import.create", 28<<20))
			})
		})
	})
	return r
}

// codeValueImportBodyLimit is the largest code value import body the API accepts; the
// contract caps the batch at 5000 rows (WP-I3-01 section 2.3).
const codeValueImportBodyLimit = 8 << 20

// idempotent builds the Idempotency-Key middleware for one command code.
func (d routerDeps) idempotent(commandCode string) func(http.Handler) http.Handler {
	return idempotency.Middleware(d.pool, idempotency.Options{
		CommandCode: commandCode,
		Scope:       idempotencyScope,
		Logger:      d.logger,
	})
}

// idempotentOptional honours an Idempotency-Key when one is sent and does not demand one.
// It is for the two commands whose repetition is harmless by construction: a PUT that sets
// a range of nights to a capacity, and the availability search, which is a query written as
// a POST because it carries a body. Demanding a key from a search would be demanding one
// from a GET.
func (d routerDeps) idempotentOptional(commandCode string) func(http.Handler) http.Handler {
	return idempotency.Middleware(d.pool, idempotency.Options{
		CommandCode: commandCode,
		Scope:       idempotencyScope,
		Optional:    true,
		Logger:      d.logger,
	})
}

// idempotentLarge is idempotent with a raised request body ceiling, for the bulk imports.
func (d routerDeps) idempotentLarge(commandCode string, maxRequestBytes int64) func(http.Handler) http.Handler {
	return idempotency.Middleware(d.pool, idempotency.Options{
		CommandCode:     commandCode,
		Scope:           idempotencyScope,
		MaxRequestBytes: maxRequestBytes,
		Logger:          d.logger,
	})
}

// anonymousScope keys the rate limiter by client address only (login).
func anonymousScope(*http.Request) (ratelimit.Scope, bool) { return ratelimit.Scope{}, false }

// sessionScope keys by actor once a session exists (tenant is not chosen yet).
func sessionScope(r *http.Request) (ratelimit.Scope, bool) {
	s, ok := identity.SessionFromContext(r.Context())
	if !ok {
		return ratelimit.Scope{}, false
	}
	return ratelimit.Scope{TenantID: s.ActorID, ActorID: s.ActorID}, true
}

// idempotencyScope binds Idempotency-Key records to the resolved tenant and actor.
func idempotencyScope(r *http.Request) (idempotency.Scope, bool) {
	rc, ok := identity.FromContext(r.Context())
	if !ok {
		return idempotency.Scope{}, false
	}
	return idempotency.Scope{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, true
}

// tenantScope keys by tenant and actor for tenant-scoped routes.
func tenantScope(r *http.Request) (ratelimit.Scope, bool) {
	rc, ok := identity.FromContext(r.Context())
	if !ok {
		return ratelimit.Scope{}, false
	}
	return ratelimit.Scope{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, true
}
