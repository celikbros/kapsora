package application

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/catalog/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Service implements the catalog use cases.
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
	// Now defaults to time.Now; tests pin it so an as-of read is deterministic.
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil || d.Cursors == nil {
		return nil, errors.New("catalog: pool, repository and cursor codec are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{pool: d.Pool, repo: d.Repo, audit: d.Audit, cursors: d.Cursors, now: d.Now}, nil
}

// ListFilter is the API-level list request shared by the paged catalog readers.
type ListFilter struct {
	Query  string
	Cursor string
	Limit  int
	// Active is nil when the caller did not ask for one activity state.
	Active *bool
	// ParentID, CategoryID, Domain, Authority and Status are the resource-specific
	// narrowings; an empty value means "no filter".
	ParentID   *uuid.UUID
	CategoryID *uuid.UUID
	Domain     string
	Authority  string
	Status     string
}

// CategoryPage is one page of categories.
type CategoryPage struct {
	Items      []CategoryRecord
	NextCursor string
}

// DefinitionPage is one page of service definitions.
type DefinitionPage struct {
	Items      []DefinitionRecord
	NextCursor string
}

// CodeSystemPage is one page of code systems.
type CodeSystemPage struct {
	Items      []CodeSystemRecord
	NextCursor string
}

// CodeValuePage is one page of code values, with the date they were resolved against.
type CodeValuePage struct {
	Items      []CodeValueRecord
	AsOf       time.Time
	NextCursor string
}

// paging decodes the cursor and clamps the limit; the repository is asked for one row
// more than the page size so the caller learns whether a next page exists.
func (s *Service) paging(f ListFilter) (after *httpx.Cursor, pageSize int, err error) {
	cursor, hasCursor, err := s.cursors.Decode(f.Cursor)
	if err != nil {
		return nil, 0, err
	}
	pageSize = httpx.ClampLimit(f.Limit)
	if hasCursor {
		after = &cursor
	}
	return after, pageSize, nil
}

// nextCursor trims the extra row and encodes the position of the last item kept.
func (s *Service) nextCursor(count, pageSize int, createdAt time.Time, id uuid.UUID) string {
	if count <= pageSize {
		return ""
	}
	return s.cursors.Encode(httpx.Cursor{CreatedAt: createdAt, ID: id})
}

// validateListFilter checks the free-text term and the closed lists a filter may carry.
func validateListFilter(f ListFilter) error {
	ve := &domain.ValidationError{}
	if err := domain.ValidateSearchTerm("q", f.Query); err != nil {
		var inner *domain.ValidationError
		if errors.As(err, &inner) {
			ve.Fields = append(ve.Fields, inner.Fields...)
		}
	}
	if f.Domain != "" && !domain.Contains(domain.ServiceDomains, f.Domain) {
		ve.Add("domain", "ENUM", "geçersiz hizmet alanı")
	}
	if f.Authority != "" && !domain.Contains(domain.CodeSystemAuthorities, f.Authority) {
		ve.Add("authority", "ENUM", "geçersiz otorite")
	}
	if f.Status != "" && !domain.Contains(domain.CodeSystemStatuses, f.Status) {
		ve.Add("status", "ENUM", "geçersiz durum")
	}
	return ve.OrNil()
}

// record writes one business audit row inside the caller's transaction. Catalog rows
// carry no personal data, so the detail may name codes and counts.
func (s *Service) event(rc identity.RequestContext, action, resourceType string, resourceID uuid.UUID, detail map[string]any) audit.Event {
	return audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID), MembershipID: nullUUID(rc.MembershipID),
		Category: audit.CategoryBusiness, ActionCode: action, ResourceType: resourceType,
		ResourceID: nullUUID(resourceID), Outcome: audit.OutcomeSuccess, Detail: detail,
	}
}

func tenantCtx(rc identity.RequestContext) db.TenantContext {
	return db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

// changed joins the patched field names for the audit detail; the values themselves are
// never logged.
func changed(fields []string) string {
	out := ""
	for i, f := range fields {
		if i > 0 {
			out += ","
		}
		out += f
	}
	return out
}
