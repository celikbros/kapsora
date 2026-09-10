package application

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/report/domain"
)

// Service implements the reporting use cases.
type Service struct {
	pool      *pgxpool.Pool
	repo      Repository
	documents Documents
	workItems WorkItems
	audit     audit.Recorder
	cursors   *httpx.CursorCodec
	logger    *slog.Logger
	now       func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool *pgxpool.Pool
	Repo Repository
	// Documents may be left unset in a process that neither renders nor downloads an export.
	// Its default refuses every call rather than doing nothing quietly.
	Documents Documents
	// WorkItems may be left unset in a process that runs no reconciliation. Its default
	// refuses, because a run that found differences and raised nothing would be a difference
	// nobody ever sees.
	WorkItems WorkItems
	Audit     audit.Recorder
	// Cursors may be nil in a process that answers no list.
	Cursors *httpx.CursorCodec
	Logger  *slog.Logger
	// Now defaults to time.Now; tests pin it so a clock is deterministic.
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil {
		return nil, errors.New("report: pool and repository are required")
	}
	if d.Documents == nil {
		d.Documents = noDocuments{}
	}
	if d.WorkItems == nil {
		d.WorkItems = noWorkItems{}
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
		pool: d.Pool, repo: d.Repo, documents: d.Documents, workItems: d.WorkItems,
		audit: d.Audit, cursors: d.Cursors, logger: d.Logger, now: d.Now,
	}, nil
}

// withTx runs fn inside a tenant-bound transaction for a person.
func (s *Service) withTx(ctx context.Context, rc identity.RequestContext,
	fn func(ctx context.Context, tx pgx.Tx) error,
) error {
	return db.WithTenantTx(ctx, s.pool,
		db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, fn)
}

// withSystemTx runs fn for a tenant with no actor. The nightly jobs act for the system: there is
// nobody whose permissions the write should be attributed to.
func (s *Service) withSystemTx(ctx context.Context, tenantID uuid.UUID,
	fn func(ctx context.Context, tx pgx.Tx) error,
) error {
	return db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID}, fn)
}

// errPagingUnavailable guards the paged reads in a process built without a cursor codec.
var errPagingUnavailable = errors.New("report: paged reads need a cursor codec")

// paging decodes the cursor and clamps the limit; the repository is asked for one row more than
// the page size so the caller learns whether a next page exists.
func (s *Service) paging(cursor string, limit int) (after *httpx.Cursor, pageSize int, err error) {
	if s.cursors == nil {
		return nil, 0, errPagingUnavailable
	}
	decoded, hasCursor, err := s.cursors.Decode(cursor)
	if err != nil {
		return nil, 0, err
	}
	pageSize = httpx.ClampLimit(limit)
	if hasCursor {
		after = &decoded
	}
	return after, pageSize, nil
}

// record writes one business audit row. Every key is snake_case: audit.SanitizeDetail drops any
// other key silently, and a detail that looked like an audit and was not would be worse than no
// detail at all.
func (s *Service) record(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action, resourceType string, resourceID uuid.UUID, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: resourceType, ResourceID: nullUUID(resourceID),
		Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// recordSystem writes the business audit row a job produces: one with a tenant and no actor,
// because the reconciliation and the expiry sweep act for nobody in particular.
func (s *Service) recordSystem(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	action, resourceType string, resourceID uuid.UUID, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(tenantID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: resourceType, ResourceID: nullUUID(resourceID),
		Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// exportCursor is the keyset position of a row on the (created_at DESC, id DESC) order both list
// endpoints page by.
func exportCursor(e Export) httpx.Cursor { return httpx.Cursor{CreatedAt: e.CreatedAt, ID: e.ID} }

func runCursor(r ReconciliationRun) httpx.Cursor {
	return httpx.Cursor{CreatedAt: r.CreatedAt, ID: r.ID}
}

func actorPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func optionalPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// fieldError builds a one-field validation error.
func fieldError(field, code, message string) error {
	ve := &domain.ValidationError{}
	ve.Add(field, code, message)
	return ve
}
