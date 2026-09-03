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
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/health"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/logging"
	"github.com/celikbros/kapsora/internal/platform/ratelimit"
)

const serviceName = "kapsora-api"

// Rate limits (v1.2 section 17.5). Login is limited per client address as well as per
// account (the account lockout in the identity domain).
var (
	loginRateLimit = ratelimit.Policy{PerMinute: 10, Burst: 5}
	apiRateLimit   = ratelimit.Policy{PerMinute: 120, Burst: 60}
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

	identitySvc, err := newIdentityService(cfg, pool, logger)
	if err != nil {
		return err
	}

	checker := health.NewChecker(2 * time.Second)
	checker.Add("postgresql", health.PostgresCheck(pool))

	router := newRouter(routerDeps{
		cfg:      cfg,
		logger:   logger,
		checker:  checker,
		identity: identitySvc,
		limiter:  ratelimit.NewPostgres(pool),
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

func newIdentityService(cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) (*application.Service, error) {
	policy := domain.DefaultPolicy()
	policy.IdleTimeout = cfg.Session.IdleTimeout
	policy.AbsoluteLifetime = cfg.Session.AbsoluteLifetime
	policy.StepUpWindow = cfg.Session.StepUpWindow

	return application.New(application.Deps{
		Credentials: identitypg.NewCredentialRepository(pool),
		Sessions:    identitypg.NewSessionStore(pool),
		Audit:       identitypg.NewAuditSink(pool, auditpg.New(), logger),
		Policy:      policy,
		Lockout:     domain.DefaultLockout(),
	})
}

type routerDeps struct {
	cfg      config.Config
	logger   *slog.Logger
	checker  *health.Checker
	identity *application.Service
	limiter  ratelimit.Limiter
}

func newRouter(d routerDeps) http.Handler {
	cookies := identityhttp.CookieConfig{Secure: d.cfg.Session.CookieSecure}
	signingKey := d.cfg.Session.SigningKey
	sessions := identityhttp.NewMiddleware(d.identity, cookies, signingKey, d.logger)
	sessionHandler := identityhttp.NewHandler(d.identity, cookies, signingKey, d.logger)

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

		api.Route("/session", func(s chi.Router) {
			// Login is rate limited per client address before any password work happens.
			s.With(ratelimit.Middleware(d.limiter, ratelimit.ScopedKey("session.login", anonymousScope), loginRateLimit, d.logger)).
				Post("/login", http.HandlerFunc(sessionHandler.Login))

			// Everything else needs the session cookie and a matching CSRF token.
			s.Group(func(authed chi.Router) {
				authed.Use(sessions.RequireCSRF)
				authed.Use(ratelimit.Middleware(d.limiter, ratelimit.ScopedKey("session", anonymousScope), apiRateLimit, d.logger))
				authed.Get("/", http.HandlerFunc(sessionHandler.GetSession))
				authed.Post("/logout", http.HandlerFunc(sessionHandler.Logout))
				authed.Post("/step-up", http.HandlerFunc(sessionHandler.StepUp))
				authed.Post("/password", http.HandlerFunc(sessionHandler.ChangePassword))
			})
		})

		// Organization, party, benefit and service routes are mounted here as their
		// modules land (WP-I1-02 onwards).
	})
	return r
}

// anonymousScope keys the rate limiter by client address only. Once WP-I1-02 resolves the
// tenant context this becomes a tenant+actor scope for authenticated routes.
func anonymousScope(*http.Request) (ratelimit.Scope, bool) { return ratelimit.Scope{}, false }
