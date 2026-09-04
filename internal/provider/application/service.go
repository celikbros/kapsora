package application

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/provider/domain"
)

// ScopeOrganization is the iam.access_grant.scope_type of provider-side roles (v1.2 6.3).
const ScopeOrganization = "ORGANIZATION"

// Service implements the provider network use cases.
type Service struct {
	pool    *pgxpool.Pool
	repo    Repository
	cipher  crypto.FieldCipher
	index   crypto.BlindIndexer
	audit   audit.Recorder
	cursors *httpx.CursorCodec
	now     func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool    *pgxpool.Pool
	Repo    Repository
	Cipher  crypto.FieldCipher
	Index   crypto.BlindIndexer
	Audit   audit.Recorder
	Cursors *httpx.CursorCodec
	// Now defaults to time.Now; tests pin it so an as-of search is deterministic.
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil || d.Cursors == nil {
		return nil, errors.New("provider: pool, repository and cursor codec are required")
	}
	if d.Cipher == nil || d.Index == nil {
		return nil, errors.New("provider: field cipher and blind indexer are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{
		pool: d.Pool, repo: d.Repo, cipher: d.Cipher, index: d.Index,
		audit: d.Audit, cursors: d.Cursors, now: d.Now,
	}, nil
}

// ListFilter is the API-level list request shared by the paged readers.
type ListFilter struct {
	Query        string
	Cursor       string
	Limit        int
	ProviderType string
	Status       string
	NetworkTier  string
	City         string
	BranchCode   string
}

// ProviderPage is one page of provider profiles.
type ProviderPage struct {
	Items      []ProviderRecord
	NextCursor string
}

// LocationPage is one page of provider locations.
type LocationPage struct {
	Items      []LocationRecord
	NextCursor string
}

// PractitionerPage is one page of practitioners.
type PractitionerPage struct {
	Items      []PractitionerRecord
	NextCursor string
}

// SearchPage is one page of search hits with the date they were resolved against.
type SearchPage struct {
	Items      []SearchHit
	AsOf       time.Time
	NextCursor string
}

// CapabilityResult is a capability set together with the ETag of the location it belongs
// to; the replacement moves that ETag on.
type CapabilityResult struct {
	Items      []CapabilityRecord
	RowVersion int64
}

// AssignmentResult is an assignment set together with the ETag of its practitioner.
type AssignmentResult struct {
	Items      []AssignmentRecord
	RowVersion int64
}

// scopeOf reads the caller's organization grants. A tenant-wide actor has none and gets an
// unrestricted scope; an actor with such a grant is bound to exactly those organizations,
// and a grant carrying no id binds it to nothing at all rather than to everything.
func scopeOf(rc identity.RequestContext) Scope {
	var ids []uuid.UUID
	for _, s := range rc.Scopes {
		if s.Type != ScopeOrganization {
			continue
		}
		if ids == nil {
			ids = []uuid.UUID{}
		}
		if s.ID.Valid {
			ids = append(ids, s.ID.UUID)
		}
	}
	return Scope{OrganizationIDs: ids}
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

// validateListFilter checks the free-text term and the closed lists a filter may carry.
func validateListFilter(f ListFilter) error {
	ve := &domain.ValidationError{}
	appendFields(ve, domain.ValidateSearchTerm("q", f.Query))
	if f.ProviderType != "" && !domain.Contains(domain.ProviderTypes, f.ProviderType) {
		ve.Add("providerType", "ENUM", "geçersiz sağlayıcı türü")
	}
	if f.NetworkTier != "" && len(f.NetworkTier) > 32 {
		ve.Add("networkTier", "LENGTH", "en fazla 32 karakter olmalı")
	}
	if f.City != "" && len(f.City) > 120 {
		ve.Add("city", "LENGTH", "en fazla 120 karakter olmalı")
	}
	if f.BranchCode != "" && len(f.BranchCode) > 64 {
		ve.Add("branchCode", "LENGTH", "en fazla 64 karakter olmalı")
	}
	return ve.OrNil()
}

// appendFields folds a nested validation error into the collector.
func appendFields(ve *domain.ValidationError, err error) {
	var inner *domain.ValidationError
	if errors.As(err, &inner) {
		ve.Fields = append(ve.Fields, inner.Fields...)
	}
}

// event builds one business audit row. Provider rows carry no personal data beyond a
// practitioner's name, so the detail names ids, codes and counts only.
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
func changed(fields []string) string { return strings.Join(fields, ",") }

// optional turns an empty string into the NULL the column stores.
func optional(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// sameString compares two nullable strings.
func sameString(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// sameDate compares two nullable days.
func sameDate(a, b *time.Time) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return domain.DateOnly(*a).Equal(domain.DateOnly(*b))
	}
}

// sameFloat compares two nullable coordinates.
func sameFloat(a, b *float64) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// dayPtr normalises an optional date to a day.
func dayPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	d := domain.DateOnly(*t)
	return &d
}
