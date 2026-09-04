package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/provider/domain"
)

// SearchFilter is the API-level eligibility search.
type SearchFilter struct {
	ServiceDefinitionID uuid.UUID
	City                string
	Query               string
	// AsOf is the day the capability has to be valid on; the zero value means today.
	AsOf   time.Time
	Cursor string
	Limit  int
}

// Search answers which provider locations can deliver a service on a date. The category
// tree is resolved here rather than expanded when a capability is written, so a definition
// added under a covered category today is found by a capability recorded last year.
func (s *Service) Search(ctx context.Context, rc identity.RequestContext, f SearchFilter) (SearchPage, error) {
	ve := &domain.ValidationError{}
	if f.ServiceDefinitionID == uuid.Nil {
		ve.Add("serviceDefinitionId", "REQUIRED", "zorunlu alan")
	}
	appendFields(ve, domain.ValidateSearchTerm("q", f.Query))
	if len(f.City) > 120 {
		ve.Add("city", "LENGTH", "en fazla 120 karakter olmalı")
	}
	if err := ve.OrNil(); err != nil {
		return SearchPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return SearchPage{}, err
	}
	asOf := domain.DateOnly(f.AsOf)
	if f.AsOf.IsZero() {
		asOf = domain.DateOnly(s.now())
	}

	page := SearchPage{AsOf: asOf}
	var rows []SearchHit
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		chain, err := s.repo.CategoryChain(ctx, tx, rc.TenantID, f.ServiceDefinitionID)
		if err != nil {
			return err
		}
		scope := domain.ResolveCapabilityTargets(f.ServiceDefinitionID.String(), uuidStrings(chain))
		if scope.Empty() {
			// A definition with no category chain does not exist in this tenant; the
			// caller gets a field error rather than an empty page it would read as "no
			// provider can do this".
			ve := &domain.ValidationError{}
			ve.Add("serviceDefinitionId", "NOT_FOUND", "hizmet tanımı bulunamadı")
			return ve
		}
		rows, err = s.repo.SearchLocations(ctx, tx, rc.TenantID, scopeOf(rc), SearchQuery{
			ServiceDefinitionID: f.ServiceDefinitionID, CategoryIDs: chain,
			City: f.City, Query: domain.LikePattern(f.Query), AsOf: asOf,
			After: after, PageSize: pageSize + 1,
		})
		return err
	})
	if err != nil {
		return SearchPage{}, err
	}
	page.Items = rows
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		last := page.Items[pageSize-1]
		page.NextCursor = s.nextCursor(last.CreatedAt, last.ID())
	}
	return page, nil
}

// ID is the keyset identity of a search hit: the location row the cursor points at.
func (h SearchHit) ID() uuid.UUID { return h.LocationID }

// uuidStrings hands the category chain to the domain, which works in ids as text so it
// stays free of database types.
func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}
