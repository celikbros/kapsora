package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/rules/domain"
)

// Service implements the rule engine use cases.
type Service struct {
	pool     *pgxpool.Pool
	repo     Repository
	audit    audit.Recorder
	cursors  *httpx.CursorCodec
	programs *ProgramCache
	now      func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool    *pgxpool.Pool
	Repo    Repository
	Audit   audit.Recorder
	Cursors *httpx.CursorCodec
	// Programs caches compiled published versions. A published version never changes, so
	// the cache never needs invalidating; a draft is never put in it. A nil cache means
	// "compile every time", which is what a test wants.
	Programs *ProgramCache
	// Now defaults to time.Now.
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil || d.Cursors == nil {
		return nil, errors.New("rules: pool, repository and cursor codec are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{
		pool: d.Pool, repo: d.Repo, audit: d.Audit, cursors: d.Cursors,
		programs: d.Programs, now: d.Now,
	}, nil
}

// ListFilter is the API-level list request of the paged reader.
type ListFilter struct {
	Query      string
	Cursor     string
	Limit      int
	DomainCode string
	Purpose    string
	Status     string
}

// RuleSetPage is one page of rule sets.
type RuleSetPage struct {
	Items      []RuleSetRecord
	NextCursor string
}

// VersionView is a rule set version with the rules and test cases under it. Both are
// carried whole rather than paged: a version that needs paging to be read is a version
// nobody can review, and the schema caps both sets on purpose.
type VersionView struct {
	Version   VersionRecord
	Rules     []RuleRecord
	TestCases []TestCaseRecord
}

// RuleResult and TestCaseResult are child sets with the ETag of their version, which is
// what a set replacement expects in If-Match.
type RuleResult struct {
	Items      []RuleRecord
	RowVersion int64
}

// TestCaseResult is RuleResult for test cases.
type TestCaseResult struct {
	Items      []TestCaseRecord
	RowVersion int64
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

// nextCursor encodes the position of the last item kept on the page.
func (s *Service) nextCursor(createdAt time.Time, id uuid.UUID) string {
	return s.cursors.Encode(httpx.Cursor{CreatedAt: createdAt, ID: id})
}

// record writes one business audit row. Rule rows carry no personal data, so the detail
// names ids, codes and counts only.
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

// recordDenied writes the refusal of a command in its own right. A denial that leaves no
// trace is worse than a noisy error.
func (s *Service) recordDenied(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action, resourceType string, resourceID uuid.UUID, reason string, detail map[string]any,
) error {
	event := audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: resourceType, ResourceID: nullUUID(resourceID),
		Outcome: audit.OutcomeDenied, Detail: detail,
	}
	event.ReasonCode = reason
	return s.audit.Record(ctx, tx, event)
}

func tenantCtx(rc identity.RequestContext) db.TenantContext {
	return db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

// statusError distinguishes "the row is frozen" from "the command does not apply here". A
// published or retired version answers RULE_VERSION_IMMUTABLE, which is the point of the
// whole package: what decided a claim cannot move afterwards.
func statusError(current string) error {
	if current == domain.VersionPublished || current == domain.VersionRetired {
		return ErrVersionImmutable
	}
	return ErrVersionTransition
}

// optString turns an empty string into the NULL the column stores.
func optString(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// deref reads through an optional string.
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
