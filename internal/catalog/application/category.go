package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/catalog/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// CreateCategory validates the command, refuses a parent that would close a loop or push
// the tree past the depth cap, and commits the row with its audit trace.
func (s *Service) CreateCategory(ctx context.Context, rc identity.RequestContext, in domain.NewCategory) (CategoryRecord, error) {
	if err := domain.ValidateNewCategory(in); err != nil {
		return CategoryRecord{}, err
	}
	var parentID *uuid.UUID
	if in.ParentID != "" {
		id, err := uuid.Parse(in.ParentID)
		if err != nil {
			ve := &domain.ValidationError{}
			ve.Add("parentId", "FORMAT", "geçerli bir kimlik olmalı")
			return CategoryRecord{}, ve
		}
		parentID = &id
	}

	var out CategoryRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if parentID != nil {
			// A new category is a leaf, so its own subtree height is one.
			if err := s.checkPlacement(ctx, tx, rc.TenantID, uuid.Nil, *parentID, 1); err != nil {
				return err
			}
		}
		id, err := s.repo.CreateCategory(ctx, tx, rc.TenantID, NewCategoryRow{
			ParentID: parentID, Code: in.Code, Name: in.Name, Domain: in.Domain, Active: in.Active,
		})
		if err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "catalog.category.create", "service_category", id, map[string]any{
			"code": in.Code, "domain": in.Domain, "has_parent": parentID != nil,
		})); err != nil {
			return err
		}
		out, err = s.repo.GetCategory(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// GetCategory returns one category of the caller's tenant.
func (s *Service) GetCategory(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (CategoryRecord, error) {
	var out CategoryRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.repo.GetCategory(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// ListCategories returns one page ordered by creation time, newest first.
func (s *Service) ListCategories(ctx context.Context, rc identity.RequestContext, f ListFilter) (CategoryPage, error) {
	if err := validateListFilter(f); err != nil {
		return CategoryPage{}, err
	}
	after, pageSize, err := s.paging(f)
	if err != nil {
		return CategoryPage{}, err
	}
	q := CategoryQuery{
		ParentID: f.ParentID, Domain: f.Domain, Active: f.Active,
		Query: domain.LikePattern(f.Query), After: after, PageSize: pageSize + 1,
	}

	var rows []CategoryRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = s.repo.ListCategories(ctx, tx, rc.TenantID, q)
		return err
	})
	if err != nil {
		return CategoryPage{}, err
	}
	page := CategoryPage{Items: rows}
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		last := page.Items[pageSize-1]
		page.NextCursor = s.nextCursor(len(rows), pageSize, last.CreatedAt, last.ID)
	}
	return page, nil
}

// UpdateCategory applies a merge-patch under optimistic concurrency. The row is locked
// first because a re-parent reads the ancestor chain before it writes, and two concurrent
// re-parents could otherwise close a loop neither of them saw.
func (s *Service) UpdateCategory(ctx context.Context, rc identity.RequestContext, id uuid.UUID, p domain.CategoryPatch) (CategoryRecord, error) {
	if err := domain.ValidateCategoryPatch(p); err != nil {
		return CategoryRecord{}, err
	}
	var newParent *uuid.UUID
	if p.ParentID != nil {
		parsed, err := uuid.Parse(*p.ParentID)
		if err != nil {
			ve := &domain.ValidationError{}
			ve.Add("parentId", "FORMAT", "geçerli bir kimlik olmalı")
			return CategoryRecord{}, ve
		}
		newParent = &parsed
	}

	var out CategoryRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockCategory(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if current.RowVersion != p.ExpectedVersion {
			return ErrVersionMismatch
		}

		next := CategoryUpdateRow{
			ParentID: current.ParentID, Name: current.Name, Active: current.Active,
			ExpectedVersion: p.ExpectedVersion,
		}
		var fields []string
		if p.Name != nil && *p.Name != current.Name {
			next.Name = *p.Name
			fields = append(fields, "name")
		}
		if p.Active != nil && *p.Active != current.Active {
			next.Active = *p.Active
			fields = append(fields, "active")
		}
		switch {
		case p.ClearParent:
			if current.ParentID != nil {
				fields = append(fields, "parentId")
			}
			next.ParentID = nil
		case newParent != nil:
			if *newParent == id {
				return domain.ErrCategoryCycle
			}
			if current.ParentID == nil || *current.ParentID != *newParent {
				fields = append(fields, "parentId")
			}
			height, err := s.repo.CategorySubtreeHeight(ctx, tx, rc.TenantID, id)
			if err != nil {
				return err
			}
			if err := s.checkPlacement(ctx, tx, rc.TenantID, id, *newParent, height); err != nil {
				return err
			}
			next.ParentID = newParent
		}

		if err := s.repo.UpdateCategory(ctx, tx, rc.TenantID, id, next); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "catalog.category.update", "service_category", id, map[string]any{
			"code": current.Code, "changed_count": len(fields), "changed_fields": changed(fields),
		})); err != nil {
			return err
		}
		out, err = s.repo.GetCategory(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// checkPlacement walks the ancestors of the proposed parent and hands the decision to the
// domain: the subject in that chain is a cycle, too many levels is a field error.
func (s *Service) checkPlacement(ctx context.Context, tx pgx.Tx, tenantID, subject, parent uuid.UUID, subtreeHeight int) error {
	ancestors, err := s.repo.CategoryAncestors(ctx, tx, tenantID, parent)
	if err != nil {
		return err
	}
	if len(ancestors) == 0 {
		ve := &domain.ValidationError{}
		ve.Add("parentId", "NOT_FOUND", "üst kategori bulunamadı")
		return ve
	}
	chain := make([]string, 0, len(ancestors))
	for _, a := range ancestors {
		chain = append(chain, a.String())
	}
	subjectID := ""
	if subject != uuid.Nil {
		subjectID = subject.String()
	}
	return domain.ValidateCategoryPlacement(subjectID, chain, subtreeHeight)
}
