package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	providerdomain "github.com/celikbros/kapsora/internal/provider/domain"
)

type OptionFilter struct {
	Query  string
	Cursor string
	Limit  int
}

type ProviderOption struct {
	ProviderProfileID uuid.UUID `json:"providerProfileId"`
	OrganizationName  string    `json:"organizationName"`
}

type ServiceOption struct {
	ServiceDefinitionID uuid.UUID `json:"serviceDefinitionId"`
	Code                string    `json:"code"`
	Name                string    `json:"name"`
}

type ProviderOptionPage struct {
	Items      []ProviderOption `json:"items"`
	NextCursor *string          `json:"nextCursor"`
}

type ServiceOptionPage struct {
	Items      []ServiceOption `json:"items"`
	NextCursor *string         `json:"nextCursor"`
}

func (s *Service) optionQuery(f OptionFilter) (OptionQuery, error) {
	if err := providerdomain.ValidateSearchTerm("q", f.Query); err != nil {
		return OptionQuery{}, err
	}
	if f.Limit < 0 || f.Limit > 100 {
		return OptionQuery{}, fieldError("limit", "RANGE", "1-100 olmalı")
	}
	after, hasAfter, err := s.cursors.Decode(f.Cursor)
	if err != nil {
		return OptionQuery{}, err
	}
	limit := f.Limit
	if limit == 0 {
		limit = 50
	}
	q := OptionQuery{Pattern: providerdomain.LikePattern(f.Query), PageSize: int32(limit + 1)}
	if hasAfter {
		q.After = &after
	}
	return q, nil
}

func organizationScope(rc identity.RequestContext) []uuid.UUID {
	var ids []uuid.UUID
	for _, scope := range rc.Scopes {
		if scope.Type != ScopeOrganization {
			continue
		}
		if ids == nil {
			ids = []uuid.UUID{}
		}
		if scope.ID.Valid && scope.ID.UUID != uuid.Nil {
			ids = append(ids, scope.ID.UUID)
		}
	}
	return ids
}

func (s *Service) ListProviderOptions(ctx context.Context, rc identity.RequestContext, f OptionFilter) (ProviderOptionPage, error) {
	page := ProviderOptionPage{Items: []ProviderOption{}}
	if rc.TenantID == uuid.Nil {
		return page, db.ErrNoTenant
	}
	q, err := s.optionQuery(f)
	if err != nil {
		return page, err
	}
	q.OrganizationIDs = organizationScope(rc)
	if q.OrganizationIDs != nil && len(q.OrganizationIDs) == 0 {
		return page, nil
	}
	var rows []ProviderOptionRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = s.repo.ListProviderOptions(ctx, tx, rc.TenantID, q)
		return err
	})
	if err != nil {
		return page, err
	}
	limit := int(q.PageSize) - 1
	if len(rows) > limit {
		last := rows[limit-1]
		cursor := s.cursors.Encode(httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ProviderProfileID})
		page.NextCursor = &cursor
		rows = rows[:limit]
	}
	for _, row := range rows {
		page.Items = append(page.Items, ProviderOption{ProviderProfileID: row.ProviderProfileID, OrganizationName: row.OrganizationName})
	}
	return page, nil
}

func (s *Service) ListServiceOptions(ctx context.Context, rc identity.RequestContext, f OptionFilter) (ServiceOptionPage, error) {
	page := ServiceOptionPage{Items: []ServiceOption{}}
	q, err := s.optionQuery(f)
	if err != nil {
		return page, err
	}
	var rows []ServiceOptionRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = s.repo.ListServiceOptions(ctx, tx, rc.TenantID, q)
		return err
	})
	if err != nil {
		return page, err
	}
	limit := int(q.PageSize) - 1
	if len(rows) > limit {
		last := rows[limit-1]
		cursor := s.cursors.Encode(httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ServiceDefinitionID})
		page.NextCursor = &cursor
		rows = rows[:limit]
	}
	for _, row := range rows {
		page.Items = append(page.Items, ServiceOption{ServiceDefinitionID: row.ServiceDefinitionID, Code: row.Code, Name: row.Name})
	}
	return page, nil
}
