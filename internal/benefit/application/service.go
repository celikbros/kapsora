package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Service implements the benefit use cases.
type Service struct {
	pool    *pgxpool.Pool
	repo    Repository
	audit   audit.Recorder
	cursors *httpx.CursorCodec
	now     func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool    *pgxpool.Pool
	Repo    Repository
	Audit   audit.Recorder
	Cursors *httpx.CursorCodec
	// Now overrides the clock in tests; nil means time.Now().UTC().
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil || d.Cursors == nil {
		return nil, errors.New("benefit: pool, repository and cursor codec are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{pool: d.Pool, repo: d.Repo, audit: d.Audit, cursors: d.Cursors, now: d.Now}, nil
}

// record writes one business audit row; detail carries ids, codes and counts only.
func (s *Service) record(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action, resourceType string, resourceID uuid.UUID, detail map[string]any) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID), MembershipID: nullUUID(rc.MembershipID),
		Category: audit.CategoryBusiness, ActionCode: action,
		ResourceType: resourceType, ResourceID: nullUUID(resourceID), Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// recordDenied writes the audit row of a refused business command (maker-checker).
func (s *Service) recordDenied(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action, resourceType string, resourceID uuid.UUID, reason string, detail map[string]any) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID), MembershipID: nullUUID(rc.MembershipID),
		Category: audit.CategorySecurity, ActionCode: action,
		ResourceType: resourceType, ResourceID: nullUUID(resourceID), Outcome: audit.OutcomeDenied,
		ReasonCode: reason, Detail: detail,
	})
}

// requirePerson answers ErrNotFound for a person outside the tenant or unknown, so the
// person-scoped enrollment routes cannot be used to probe for people.
func (s *Service) requirePerson(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) error {
	exists, err := s.repo.PersonExists(ctx, tx, tenantID, personID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func tenantCtx(rc identity.RequestContext) db.TenantContext {
	return db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// datePtr copies an optional contract date, dropping the clock.
func datePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	d := domain.DateOnly(*t)
	return &d
}
