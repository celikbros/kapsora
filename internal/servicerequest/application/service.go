package application

import (
	"context"
	"crypto/rand"
	"encoding/base32"
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
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	rulesapp "github.com/celikbros/kapsora/internal/rules/application"
	"github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// Service implements the service request use cases.
type Service struct {
	pool     *pgxpool.Pool
	repo     Repository
	audit    audit.Recorder
	cursors  *httpx.CursorCodec
	programs *rulesapp.ProgramCache
	logger   *slog.Logger
	now      func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool    *pgxpool.Pool
	Repo    Repository
	Audit   audit.Recorder
	Cursors *httpx.CursorCodec
	// Programs caches compiled published rule set versions. A published version never
	// changes, so an entry can never go stale; a nil cache means "compile every time".
	Programs *rulesapp.ProgramCache
	Logger   *slog.Logger
	// Now defaults to time.Now; tests pin it so a submit is deterministic.
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil || d.Cursors == nil {
		return nil, errors.New("servicerequest: pool, repository and cursor codec are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{
		pool: d.Pool, repo: d.Repo, audit: d.Audit, cursors: d.Cursors,
		programs: d.Programs, logger: d.Logger, now: d.Now,
	}, nil
}

// RequestView is a request with the lines of its current version.
type RequestView struct {
	Request RequestRecord
	Items   []ItemRecord
}

// VersionView is one version with the lines it carried.
type VersionView struct {
	Version VersionRecord
	Items   []ItemRecord
}

// RequestPage is one page of requests with their current lines.
type RequestPage struct {
	Items      []RequestView
	NextCursor string
}

// ListFilter is the API-level list request.
type ListFilter struct {
	Cursor                 string
	Limit                  int
	Status                 string
	PersonID               *uuid.UUID
	ProgramID              *uuid.UUID
	ProviderOrganizationID *uuid.UUID
	Channel                string
	ServiceDateFrom        *time.Time
	ServiceDateTo          *time.Time
	CreatedFrom            *time.Time
	CreatedTo              *time.Time
}

// withTx runs fn inside a tenant-bound transaction.
func (s *Service) withTx(ctx context.Context, rc identity.RequestContext,
	fn func(ctx context.Context, tx pgx.Tx) error,
) error {
	return db.WithTenantTx(ctx, s.pool,
		db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, fn)
}

// paging decodes the cursor and clamps the limit; the repository is asked for one row more
// than the page size so the caller learns whether a next page exists.
func (s *Service) paging(cursor string, limit int) (after *httpx.Cursor, pageSize int, err error) {
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

// loadView reads a request with the lines of the version it is currently on.
func (s *Service) loadView(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record RequestRecord,
) (RequestView, error) {
	version, err := s.repo.GetVersionByNo(ctx, tx, tenantID, record.ID, record.CurrentVersionNo)
	if err != nil {
		return RequestView{}, err
	}
	items, err := s.repo.ListItems(ctx, tx, tenantID, version.ID)
	if err != nil {
		return RequestView{}, err
	}
	return RequestView{Request: record, Items: items}, nil
}

// reload re-reads a request after a write, so the caller is answered with the row that is
// actually in the database rather than with what the command believed it wrote.
func (s *Service) reload(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (RequestView, error) {
	record, err := s.repo.GetRequest(ctx, tx, tenantID, id, scope)
	if err != nil {
		return RequestView{}, err
	}
	return s.loadView(ctx, tx, tenantID, record)
}

// record writes one business audit row. A request carries no personal data beyond the ids
// it names, so the detail is ids, codes, counts and statuses only.
func (s *Service) record(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action string, resourceID uuid.UUID, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: "service_request", ResourceID: nullUUID(resourceID),
		Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// transition writes the status event every move owes and the audit row beside it. Nothing
// in this package changes a status without going through here: a history that some
// commands write and others do not is a history nobody can rely on.
func (s *Service) transition(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	requestID uuid.UUID, from, to, command string, reasonCode string, reasonText *string,
	metadata map[string]any,
) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	if err := s.repo.AppendStatusEvent(ctx, tx, rc.TenantID, StatusEventRow{
		AggregateID: requestID, FromStatus: from, ToStatus: to, TransitionCode: command,
		ReasonCode: optString(reasonCode), ReasonText: reasonText,
		ActorID: actorPtr(rc.Principal.ActorID), Metadata: metadata,
	}); err != nil {
		return err
	}
	detail := map[string]any{"from": from, "to": to, "transition": command}
	if reasonCode != "" {
		detail["reason_code"] = reasonCode
	}
	for k, v := range metadata {
		detail[k] = v
	}
	return s.record(ctx, tx, rc, "service_request."+strings.ToLower(command), requestID, detail)
}

// GetStatusHistory returns the transitions of one request, oldest first. It is not an
// endpoint of this work package; the review screens and the tests read it.
func (s *Service) GetStatusHistory(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID,
) ([]StatusEventRecord, error) {
	var out []StatusEventRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetRequest(ctx, tx, rc.TenantID, id, scopeOf(rc)); err != nil {
			return err
		}
		events, err := s.repo.ListStatusEvents(ctx, tx, rc.TenantID, id)
		out = events
		return err
	})
	return out, err
}

// referenceAlphabet is Crockford-free base32 without padding: uppercase letters and digits
// only, which is what somebody has to read out over a telephone.
var referenceEncoding = base32.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567").WithPadding(base32.NoPadding)

// newReference builds a request reference of the form SR-20260904-XXXXXXXX. The random
// tail rather than a counter is deliberate: a per-tenant counter would leak how much work
// a sponsor is doing to anybody who can raise two requests and subtract.
func newReference(now time.Time) (string, error) {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("servicerequest: generate reference: %w", err)
	}
	return fmt.Sprintf("SR-%s-%s", now.UTC().Format("20060102"), referenceEncoding.EncodeToString(buf)), nil
}

func optString(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func trimmedPtr(s *string) *string {
	if s == nil {
		return nil
	}
	return optString(strings.TrimSpace(*s))
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

// fieldError builds a one-field validation error.
func fieldError(field, code, message string) error {
	ve := &domain.ValidationError{}
	ve.Add(field, code, message)
	return ve
}
