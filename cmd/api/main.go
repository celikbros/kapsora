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

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	audithttp "github.com/celikbros/kapsora/internal/audit/transport/http"
	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	benefitpg "github.com/celikbros/kapsora/internal/benefit/infrastructure/postgres"
	benefithttp "github.com/celikbros/kapsora/internal/benefit/transport/http"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	orgapp "github.com/celikbros/kapsora/internal/organization/application"
	organizationpg "github.com/celikbros/kapsora/internal/organization/infrastructure/postgres"
	organizationhttp "github.com/celikbros/kapsora/internal/organization/transport/http"
	partyapp "github.com/celikbros/kapsora/internal/party/application"
	partypg "github.com/celikbros/kapsora/internal/party/infrastructure/postgres"
	partyhttp "github.com/celikbros/kapsora/internal/party/transport/http"
	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/health"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/idempotency"
	"github.com/celikbros/kapsora/internal/platform/logging"
	"github.com/celikbros/kapsora/internal/platform/ratelimit"
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

	checker := health.NewChecker(2 * time.Second)
	checker.Add("postgresql", health.PostgresCheck(pool))

	router := newRouter(routerDeps{
		cfg:     cfg,
		pool:    pool,
		logger:  logger,
		checker: checker,
		ident:   ident,
		orgs:    orgSvc,
		party:   partySvc,
		benefit: benefitSvc,
		limiter: ratelimit.NewPostgres(pool),
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
	cfg     config.Config
	pool    *pgxpool.Pool
	logger  *slog.Logger
	checker *health.Checker
	ident   identityDeps
	orgs    *orgapp.Service
	party   *partyapp.Service
	benefit *benefitapp.Service
	limiter ratelimit.Limiter
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

			partyHandler := partyhttp.NewHandler(d.party, sessions, d.logger)
			benefitHandler := benefithttp.NewHandler(d.benefit, sessions, d.logger)
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
			})
			tenant.Route("/party", partyHandler.CatalogRoutes)

			tenant.Route("/programs", func(r chi.Router) { benefitHandler.ProgramRoutes(r, benefitMW) })
			tenant.Route("/plans", func(r chi.Router) { benefitHandler.PlanRoutes(r, benefitMW) })
			tenant.Route("/plan-versions", benefitHandler.PlanVersionRoutes)
			tenant.Route("/enrollments", benefitHandler.EnrollmentRoutes)
		})
	})
	return r
}

// idempotent builds the Idempotency-Key middleware for one command code.
func (d routerDeps) idempotent(commandCode string) func(http.Handler) http.Handler {
	return idempotency.Middleware(d.pool, idempotency.Options{
		CommandCode: commandCode,
		Scope:       idempotencyScope,
		Logger:      d.logger,
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
