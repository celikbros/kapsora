package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// DefaultLinkBase is where a deep link points when nothing else was configured. It is a
// name rather than an address on purpose: a link in a notification has to survive being
// read a week later on a different device, and localhost does not.
const DefaultLinkBase = "https://kapsora.local"

// Service implements the template, message, delivery and preference use cases.
type Service struct {
	pool     *pgxpool.Pool
	repo     Repository
	audit    audit.Recorder
	cursors  *httpx.CursorCodec
	senders  map[string]ChannelSender
	linkBase string
	logger   *slog.Logger
	now      func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool  *pgxpool.Pool
	Repo  Repository
	Audit audit.Recorder
	// Cursors may be nil in a process that answers no list; the worker is one.
	Cursors *httpx.CursorCodec
	// Senders is the channel adapter per channel code. The API process has none: nothing
	// there sends anything, and an adapter wired in would be one it never calls. A worker
	// asked to send on a channel with no adapter answers ErrNoSender rather than
	// recording a delivery that did not happen.
	Senders map[string]ChannelSender
	// LinkBase is prefixed to every deep link. The path itself never carries a query
	// string, so this is the whole of the URL a recipient sees besides the screen they
	// have to sign in to reach.
	LinkBase string
	Logger   *slog.Logger
	// Now defaults to time.Now; tests pin it so quiet hours are deterministic.
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil {
		return nil, errors.New("notification: pool and repository are required")
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
	if d.LinkBase == "" {
		d.LinkBase = DefaultLinkBase
	}
	if strings.Contains(d.LinkBase, "?") || strings.Contains(d.LinkBase, "#") {
		// A base carrying a query string would be a base carrying a token, which is the
		// one thing a notification link may never do.
		return nil, errors.New("notification: the link base must not carry a query string or a fragment")
	}
	return &Service{
		pool: d.Pool, repo: d.Repo, audit: d.Audit, cursors: d.Cursors,
		senders: d.Senders, linkBase: strings.TrimRight(d.LinkBase, "/"),
		logger: d.Logger, now: d.Now,
	}, nil
}

// TemplatePage is one keyset page of templates.
type TemplatePage struct {
	Items      []TemplateRecord
	NextCursor string
}

// MessagePage is one keyset page of messages.
type MessagePage struct {
	Items      []MessageRecord
	NextCursor string
}

// TemplateFilter is the API-level template list request.
type TemplateFilter struct {
	Cursor    string
	Limit     int
	EventCode string
	Channel   string
	Locale    string
	Status    string
}

// MessageFilter is the API-level message log request.
type MessageFilter struct {
	Cursor        string
	Limit         int
	EventCode     string
	Channel       string
	Status        string
	RecipientType string
	RecipientID   *uuid.UUID
}

// withTx runs fn inside a tenant-bound transaction.
func (s *Service) withTx(ctx context.Context, rc identity.RequestContext,
	fn func(ctx context.Context, tx pgx.Tx) error,
) error {
	return db.WithTenantTx(ctx, s.pool,
		db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, fn)
}

// withSystemTx runs fn for a tenant with no actor. The worker acts for the system: there
// is nobody whose permissions writing a message should be attributed to.
func (s *Service) withSystemTx(ctx context.Context, tenantID uuid.UUID,
	fn func(ctx context.Context, tx pgx.Tx) error,
) error {
	return db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID}, fn)
}

// errPagingUnavailable guards the paged reads in a process built without a cursor codec.
var errPagingUnavailable = errors.New("notification: paged reads need a cursor codec")

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

// record writes one business audit row. The detail is ids, codes, counts and statuses
// only. audit.SanitizeDetail drops any key holding a name, an identifier or a secret, so
// nothing here is named around that rule: a rendered subject, a recipient's address and a
// template's variable *values* are deliberately absent rather than renamed past the
// filter — a key that looks like an audit and is silently dropped is worse than no audit.
func (s *Service) record(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action string, resourceID uuid.UUID, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: domain.AggregateType, ResourceID: nullUUID(resourceID),
		Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// recordSystem writes the audit row of something the worker did. It carries no actor
// because there is none: the send happened because an event was delivered, not because
// somebody asked.
func (s *Service) recordSystem(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	action string, resourceID uuid.UUID, outcome audit.Outcome, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(tenantID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: domain.AggregateType, ResourceID: nullUUID(resourceID),
		Outcome: outcome, Detail: detail,
	})
}

// templateCursor and messageCursor are the keyset positions of the two list endpoints,
// both of which page by (created_at DESC, id DESC).
func templateCursor(r TemplateRecord) httpx.Cursor {
	return httpx.Cursor{CreatedAt: r.CreatedAt, ID: r.ID}
}

func messageCursor(r MessageRecord) httpx.Cursor {
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

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// fieldError builds a one-field validation error.
func fieldError(field, code, message string) error {
	ve := &domain.ValidationError{}
	ve.Add(field, code, message)
	return ve
}

// senderError wraps a channel adapter failure so the transport can answer 503 and the
// worker can retry. A provider that is down is a state a send can retry out of.
func senderError(channel string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrSenderUnavailable, channel, err)
}
