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
	"github.com/celikbros/kapsora/internal/workflow/domain"
)

// Service implements the work queue, work item, comment and approval policy use cases.
type Service struct {
	pool    *pgxpool.Pool
	repo    Repository
	audit   audit.Recorder
	cursors *httpx.CursorCodec
	logger  *slog.Logger
	now     func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool  *pgxpool.Pool
	Repo  Repository
	Audit audit.Recorder
	// Cursors may be nil in a process that only runs the escalation job: it never pages.
	Cursors *httpx.CursorCodec
	Logger  *slog.Logger
	// Now defaults to time.Now; tests pin it so a clock is deterministic.
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil {
		return nil, errors.New("workflow: pool and repository are required")
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
		cursors: d.Cursors, logger: d.Logger, now: d.Now,
	}, nil
}

// QueuePage is one keyset page of work queues.
type QueuePage struct {
	Items      []QueueRecord
	NextCursor string
}

// ItemPage is one keyset page of work items.
type ItemPage struct {
	Items      []ItemRecord
	NextCursor string
}

// QueueFilter is the API-level queue list request.
type QueueFilter struct {
	Cursor     string
	Limit      int
	DomainCode string
	Active     *bool
}

// ItemFilter is the API-level work item list request. `AssignedToMe` is resolved against
// the caller rather than taking an actor id, so asking for somebody else's list is a
// question the endpoint cannot be made to answer by accident.
type ItemFilter struct {
	Cursor        string
	Limit         int
	QueueID       *uuid.UUID
	Status        string
	AssignedToMe  bool
	AggregateType string
	AggregateID   *uuid.UUID
	Overdue       *bool
}

// withTx runs fn inside a tenant-bound transaction.
func (s *Service) withTx(ctx context.Context, rc identity.RequestContext,
	fn func(ctx context.Context, tx pgx.Tx) error,
) error {
	return db.WithTenantTx(ctx, s.pool,
		db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, fn)
}

// errPagingUnavailable guards the paged reads in a process built without a cursor codec:
// the scheduler runs the escalation job and never answers a list.
var errPagingUnavailable = errors.New("workflow: paged reads need a cursor codec")

// paging decodes the cursor and clamps the limit; the repository is asked for one row more
// than the page size so the caller learns whether a next page exists.
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

// record writes one business audit row. The detail is ids, codes and counts only.
// audit.SanitizeDetail drops any key holding a name, an identifier or a secret, so nothing
// here is named around that rule: a comment body, a queue's display name and an item's
// title are deliberately absent rather than renamed past the filter.
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

// writeEvent records one transition of a work item.
func (s *Service) writeEvent(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	item ItemRecord, to, transition string, reasonCode, reasonText *string, metadata map[string]any,
) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	return s.repo.CreateStatusEvent(ctx, tx, rc.TenantID, StatusEventRow{
		AggregateID: item.ID, FromStatus: item.Status, ToStatus: to,
		TransitionCode: transition, ReasonCode: reasonCode, ReasonText: reasonText,
		ActorID: actorPtr(rc.Principal.ActorID), Metadata: metadata,
	})
}

// itemCursor is the keyset position of a row on the (created_at DESC, id DESC) order both
// list endpoints page by.
func itemCursor(r ItemRecord) httpx.Cursor {
	return httpx.Cursor{CreatedAt: r.CreatedAt, ID: r.ID}
}

func queueCursor(r QueueRecord) httpx.Cursor {
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
