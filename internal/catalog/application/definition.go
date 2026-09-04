package application

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/catalog/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// CreateDefinition validates the command and stores the definition in its category.
func (s *Service) CreateDefinition(ctx context.Context, rc identity.RequestContext, in domain.NewDefinition) (DefinitionRecord, error) {
	if err := domain.ValidateNewDefinition(in); err != nil {
		return DefinitionRecord{}, err
	}
	categoryID, err := uuid.Parse(in.CategoryID)
	if err != nil {
		ve := &domain.ValidationError{}
		ve.Add("categoryId", "FORMAT", "geçerli bir kimlik olmalı")
		return DefinitionRecord{}, ve
	}

	var out DefinitionRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetCategory(ctx, tx, rc.TenantID, categoryID); err != nil {
			return categoryReference(err)
		}
		id, err := s.repo.CreateDefinition(ctx, tx, rc.TenantID, NewDefinitionRow{
			CategoryID: categoryID, Code: in.Code, Name: in.Name, Description: optional(in.Description),
			FulfillmentMode: in.FulfillmentMode, DefaultUnitType: in.DefaultUnitType,
			RequiresProvider: in.RequiresProvider, Active: in.Active,
		})
		if err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "catalog.service_definition.create", "service_definition", id, map[string]any{
			"code": in.Code, "category_id": categoryID, "fulfillment_mode": in.FulfillmentMode,
			"default_unit_type": in.DefaultUnitType,
		})); err != nil {
			return err
		}
		out, err = s.repo.GetDefinition(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// GetDefinition returns one service definition of the caller's tenant.
func (s *Service) GetDefinition(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (DefinitionRecord, error) {
	var out DefinitionRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.repo.GetDefinition(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// ListDefinitions returns one page ordered by creation time, newest first.
func (s *Service) ListDefinitions(ctx context.Context, rc identity.RequestContext, f ListFilter) (DefinitionPage, error) {
	if err := validateListFilter(f); err != nil {
		return DefinitionPage{}, err
	}
	after, pageSize, err := s.paging(f)
	if err != nil {
		return DefinitionPage{}, err
	}
	q := DefinitionQuery{
		CategoryID: f.CategoryID, Domain: f.Domain, Active: f.Active,
		Query: domain.LikePattern(f.Query), After: after, PageSize: pageSize + 1,
	}

	var rows []DefinitionRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = s.repo.ListDefinitions(ctx, tx, rc.TenantID, q)
		return err
	})
	if err != nil {
		return DefinitionPage{}, err
	}
	page := DefinitionPage{Items: rows}
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		last := page.Items[pageSize-1]
		page.NextCursor = s.nextCursor(len(rows), pageSize, last.CreatedAt, last.ID)
	}
	return page, nil
}

// UpdateDefinition applies a merge-patch under optimistic concurrency. The code is never
// part of the patch: the transport refuses a body carrying it before this is reached.
func (s *Service) UpdateDefinition(ctx context.Context, rc identity.RequestContext, id uuid.UUID, p domain.DefinitionPatch) (DefinitionRecord, error) {
	if err := domain.ValidateDefinitionPatch(p); err != nil {
		return DefinitionRecord{}, err
	}
	var newCategory *uuid.UUID
	if p.CategoryID != nil {
		parsed, err := uuid.Parse(*p.CategoryID)
		if err != nil {
			ve := &domain.ValidationError{}
			ve.Add("categoryId", "FORMAT", "geçerli bir kimlik olmalı")
			return DefinitionRecord{}, ve
		}
		newCategory = &parsed
	}

	var out DefinitionRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.GetDefinition(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if current.RowVersion != p.ExpectedVersion {
			return ErrVersionMismatch
		}

		next := DefinitionUpdateRow{
			CategoryID: current.CategoryID, Name: current.Name, Description: current.Description,
			FulfillmentMode: current.FulfillmentMode, DefaultUnitType: current.DefaultUnitType,
			RequiresProvider: current.RequiresProvider, Active: current.Active,
		}
		var fields []string
		if newCategory != nil && *newCategory != current.CategoryID {
			if _, err := s.repo.GetCategory(ctx, tx, rc.TenantID, *newCategory); err != nil {
				return categoryReference(err)
			}
			next.CategoryID = *newCategory
			fields = append(fields, "categoryId")
		}
		if p.Name != nil && *p.Name != current.Name {
			next.Name = *p.Name
			fields = append(fields, "name")
		}
		switch {
		case p.ClearDescription:
			if current.Description != nil {
				fields = append(fields, "description")
			}
			next.Description = nil
		case p.Description != nil:
			if current.Description == nil || *current.Description != *p.Description {
				fields = append(fields, "description")
			}
			next.Description = optional(*p.Description)
		}
		if p.FulfillmentMode != nil && *p.FulfillmentMode != current.FulfillmentMode {
			next.FulfillmentMode = *p.FulfillmentMode
			fields = append(fields, "fulfillmentMode")
		}
		if p.DefaultUnitType != nil && *p.DefaultUnitType != current.DefaultUnitType {
			next.DefaultUnitType = *p.DefaultUnitType
			fields = append(fields, "defaultUnitType")
		}
		if p.RequiresProvider != nil && *p.RequiresProvider != current.RequiresProvider {
			next.RequiresProvider = *p.RequiresProvider
			fields = append(fields, "requiresProvider")
		}
		if p.Active != nil && *p.Active != current.Active {
			next.Active = *p.Active
			fields = append(fields, "active")
		}

		if err := s.repo.UpdateDefinition(ctx, tx, rc.TenantID, id, next, p.ExpectedVersion); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "catalog.service_definition.update", "service_definition", id, map[string]any{
			"code": current.Code, "changed_count": len(fields), "changed_fields": changed(fields),
		})); err != nil {
			return err
		}
		out, err = s.repo.GetDefinition(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// categoryReference turns "the category does not exist" into a field error on the request
// rather than a 404 for the definition the caller asked about.
func categoryReference(err error) error {
	if errors.Is(err, ErrCategoryNotFound) {
		ve := &domain.ValidationError{}
		ve.Add("categoryId", "NOT_FOUND", "kategori bulunamadı")
		return ve
	}
	return err
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
