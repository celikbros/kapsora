package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	rulesapp "github.com/celikbros/kapsora/internal/rules/application"
)

// DefaultQuoteTTL is how long a quote holds when the tenant has set no
// pricing.quote_ttl_hours. Prices move, so a quote that never expired would eventually be
// waved at a counter as if it were still the answer.
const DefaultQuoteTTL = 72 * time.Hour

// maxQuoteItems mirrors the contract's maxItems on the request.
const maxQuoteItems = 100

// Service answers what a service costs and stores the answer.
//
// One quote is one REPEATABLE READ transaction: the eligibility data, the contract
// prices, the published rules and the balances are all read from a single point in time,
// and the quote row, its lines and the access audit event commit together with them. The
// isolation level is what makes the stored explanation true — under READ COMMITTED a
// price list could be replaced between the selection and the rule evaluation, and the
// quote would name a version it did not actually use.
type Service struct {
	pool     *pgxpool.Pool
	repo     Repository
	audit    audit.Recorder
	programs *rulesapp.ProgramCache
	logger   *slog.Logger
	now      func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool  *pgxpool.Pool
	Repo  Repository
	Audit audit.Recorder
	// Programs caches compiled published rule set versions. A published version never
	// changes, so an entry can never go stale; nil means "compile every time", which is
	// what a test wants.
	Programs *rulesapp.ProgramCache
	Logger   *slog.Logger
	// Now overrides the clock in tests; nil means time.Now().UTC().
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil {
		return nil, errors.New("pricing: pool and repository are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		pool: d.Pool, repo: d.Repo, audit: d.Audit,
		programs: d.Programs, logger: d.Logger, now: d.Now,
	}, nil
}

// withQuoteTx runs fn in a REPEATABLE READ transaction bound to the tenant, so RLS is
// active for every statement inside it. db.WithTenantTx cannot be used here because it
// opens the pool's default isolation; the binding it performs is db.BindTenant, which is
// exported for exactly this case.
func (s *Service) withQuoteTx(ctx context.Context, rc identity.RequestContext,
	fn func(ctx context.Context, tx pgx.Tx) error,
) error {
	if rc.TenantID == uuid.Nil {
		return db.ErrNoTenant
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return fmt.Errorf("pricing: begin quote tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	if err := db.BindTenant(ctx, tx, tenantCtx(rc)); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return serializationError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return serializationError(fmt.Errorf("pricing: commit quote tx: %w", err))
	}
	return nil
}

// quoteTTL is the tenant's configured quote lifetime, falling back to DefaultQuoteTTL. A
// setting that is present but unusable — zero, negative or not a number — is treated as
// absent rather than failing the quote: a bad setting should not stop a counter working.
func (s *Service) quoteTTL(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) time.Duration {
	hours, found, err := s.repo.QuoteTTLHours(ctx, tx, tenantID)
	switch {
	case err != nil:
		s.logger.Warn("pricing: quote ttl setting unreadable, using the default",
			"tenant_id", tenantID, "error", err)
		return DefaultQuoteTTL
	case !found || hours <= 0:
		return DefaultQuoteTTL
	default:
		return time.Duration(hours) * time.Hour
	}
}

// recordAccess writes the access audit row of a quote: who asked what this person's
// service would cost, classified HEALTH when the request was made in a clinical context.
func (s *Service) recordAccess(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	personID, quoteID uuid.UUID, classification audit.Classification, outcome string,
	accessType audit.AccessType,
) error {
	return s.audit.RecordAccess(ctx, tx, audit.AccessEvent{
		TenantID: rc.TenantID, ActorID: rc.Principal.ActorID, MembershipID: nullUUID(rc.MembershipID),
		PersonID: nullUUID(personID), ResourceType: ResourceQuote, ResourceID: nullUUID(quoteID),
		AccessType: accessType, Classification: classification,
		PurposeCode: PurposeQuote, ReasonText: "outcome=" + outcome,
		Outcome: audit.OutcomeSuccess,
	})
}

// checkProviderScope enforces the provider boundary: an actor whose grants are scoped to
// organizations may only quote for, and read the quotes of, a provider inside that scope.
// A tenant-wide actor has no ORGANIZATION scope and is unaffected.
func checkProviderScope(rc identity.RequestContext, providerOrganizationID uuid.UUID) error {
	scoped := false
	for _, scope := range rc.Scopes {
		if scope.Type != ScopeOrganization || !scope.ID.Valid {
			continue
		}
		scoped = true
		if scope.ID.UUID == providerOrganizationID {
			return nil
		}
	}
	if !scoped {
		return nil
	}
	return ErrProviderScope
}

// serializationError turns PostgreSQL's concurrency refusals into an error the transport
// can tell a caller to retry. REPEATABLE READ buys a consistent read of every price, rule
// and balance behind a quote, and the price of that is a transaction that occasionally
// has to be run again.
func serializationError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01") {
		return ErrSerializationFailure
	}
	return err
}

func tenantCtx(rc identity.RequestContext) db.TenantContext {
	return db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func actorPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func uuidPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// fieldError builds a one-field validation error, which the transport answers 422 with.
func fieldError(field, code, message string) error {
	ve := &benefitdomain.ValidationError{}
	ve.Add(field, code, message)
	return ve
}
