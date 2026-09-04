package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/catalog/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// CreateCodeSystem registers one edition of an external code system.
func (s *Service) CreateCodeSystem(ctx context.Context, rc identity.RequestContext, in domain.NewCodeSystem) (CodeSystemRecord, error) {
	if err := domain.ValidateNewCodeSystem(in); err != nil {
		return CodeSystemRecord{}, err
	}
	var out CodeSystemRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		id, err := s.repo.CreateCodeSystem(ctx, tx, rc.TenantID, NewCodeSystemRow{
			Code: in.Code, Name: in.Name, Version: in.Version, Authority: in.Authority,
			Licensed: in.Licensed, ValidFrom: in.ValidFrom, ValidTo: in.ValidTo,
		})
		if err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "catalog.code_system.create", "code_system", id, map[string]any{
			"code": in.Code, "version": in.Version, "authority": in.Authority, "licensed": in.Licensed,
		})); err != nil {
			return err
		}
		out, err = s.repo.GetCodeSystem(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// GetCodeSystem returns one code system of the caller's tenant.
func (s *Service) GetCodeSystem(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (CodeSystemRecord, error) {
	var out CodeSystemRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.repo.GetCodeSystem(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// ListCodeSystems returns one page ordered by creation time, newest first.
func (s *Service) ListCodeSystems(ctx context.Context, rc identity.RequestContext, f ListFilter) (CodeSystemPage, error) {
	if err := validateListFilter(f); err != nil {
		return CodeSystemPage{}, err
	}
	after, pageSize, err := s.paging(f)
	if err != nil {
		return CodeSystemPage{}, err
	}
	q := CodeSystemQuery{
		Authority: f.Authority, Status: f.Status, Query: domain.LikePattern(f.Query),
		After: after, PageSize: pageSize + 1,
	}

	var rows []CodeSystemRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = s.repo.ListCodeSystems(ctx, tx, rc.TenantID, q)
		return err
	})
	if err != nil {
		return CodeSystemPage{}, err
	}
	page := CodeSystemPage{Items: rows}
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		last := page.Items[pageSize-1]
		page.NextCursor = s.nextCursor(len(rows), pageSize, last.CreatedAt, last.ID)
	}
	return page, nil
}

// UpdateCodeSystem applies a merge-patch under optimistic concurrency; code and version
// are immutable and the transport refuses a body carrying either.
func (s *Service) UpdateCodeSystem(ctx context.Context, rc identity.RequestContext, id uuid.UUID, p domain.CodeSystemPatch) (CodeSystemRecord, error) {
	if err := domain.ValidateCodeSystemPatch(p); err != nil {
		return CodeSystemRecord{}, err
	}
	var out CodeSystemRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.GetCodeSystem(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if current.RowVersion != p.ExpectedVersion {
			return ErrVersionMismatch
		}

		next := CodeSystemUpdateRow{
			Name: current.Name, Authority: current.Authority,
			Licensed: current.Licensed, Status: current.Status, ValidTo: current.ValidTo,
		}
		var fields []string
		if p.Name != nil && *p.Name != current.Name {
			next.Name = *p.Name
			fields = append(fields, "name")
		}
		if p.Authority != nil && *p.Authority != current.Authority {
			next.Authority = *p.Authority
			fields = append(fields, "authority")
		}
		if p.Licensed != nil && *p.Licensed != current.Licensed {
			next.Licensed = *p.Licensed
			fields = append(fields, "licensed")
		}
		if p.Status != nil && *p.Status != current.Status {
			next.Status = *p.Status
			fields = append(fields, "status")
		}
		switch {
		case p.ClearValidTo:
			if current.ValidTo != nil {
				fields = append(fields, "validTo")
			}
			next.ValidTo = nil
		case p.ValidTo != nil:
			if current.ValidTo == nil || !current.ValidTo.Equal(*p.ValidTo) {
				fields = append(fields, "validTo")
			}
			next.ValidTo = p.ValidTo
		}
		if next.ValidTo != nil && !domain.DateOnly(*next.ValidTo).After(domain.DateOnly(current.ValidFrom)) {
			ve := &domain.ValidationError{}
			ve.Add("validTo", "PERIOD", "bitiş tarihi başlangıçtan sonra olmalı")
			return ve
		}

		if err := s.repo.UpdateCodeSystem(ctx, tx, rc.TenantID, id, next, p.ExpectedVersion); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "catalog.code_system.update", "code_system", id, map[string]any{
			"code": current.Code, "version": current.Version,
			"changed_count": len(fields), "changed_fields": changed(fields),
		})); err != nil {
			return err
		}
		out, err = s.repo.GetCodeSystem(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// CodeValueFilter is the API-level request for reading a code system as of a date.
type CodeValueFilter struct {
	// AsOf is the zero value when the caller did not send one; today is used then.
	AsOf   time.Time
	Code   string
	Query  string
	Cursor string
	Limit  int
}

// ListCodeValues returns the values of a system valid on the requested date.
func (s *Service) ListCodeValues(ctx context.Context, rc identity.RequestContext, systemID uuid.UUID, f CodeValueFilter) (CodeValuePage, error) {
	ve := &domain.ValidationError{}
	if err := domain.ValidateSearchTerm("q", f.Query); err != nil {
		var inner *domain.ValidationError
		if errors.As(err, &inner) {
			ve.Fields = append(ve.Fields, inner.Fields...)
		}
	}
	if n := len([]rune(f.Code)); n > 64 {
		ve.Add("code", "LENGTH", "en fazla 64 karakter olmalı")
	}
	if err := ve.OrNil(); err != nil {
		return CodeValuePage{}, err
	}

	asOf := f.AsOf
	if asOf.IsZero() {
		asOf = s.now()
	}
	asOf = domain.DateOnly(asOf)

	after, pageSize, err := s.paging(ListFilter{Cursor: f.Cursor, Limit: f.Limit})
	if err != nil {
		return CodeValuePage{}, err
	}
	q := CodeValueQuery{
		CodeSystemID: systemID, AsOf: asOf, Code: f.Code, Query: domain.LikePattern(f.Query),
		After: after, PageSize: pageSize + 1,
	}

	var rows []CodeValueRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetCodeSystem(ctx, tx, rc.TenantID, systemID); err != nil {
			return err
		}
		var err error
		rows, err = s.repo.ListCodeValues(ctx, tx, rc.TenantID, q)
		return err
	})
	if err != nil {
		return CodeValuePage{}, err
	}
	page := CodeValuePage{Items: rows, AsOf: asOf}
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		last := page.Items[pageSize-1]
		page.NextCursor = s.nextCursor(len(rows), pageSize, last.CreatedAt, last.ID)
	}
	return page, nil
}

// ImportCodeValues upserts a whole batch on (code, validFrom) in one transaction. The
// batch is validated before a single row is written, so a rejected call leaves nothing
// behind; the Idempotency-Key middleware replays the summary of a repeated call.
func (s *Service) ImportCodeValues(ctx context.Context, rc identity.RequestContext, systemID uuid.UUID, items []domain.CodeValueInput) (UpsertSummary, error) {
	if err := domain.ValidateImportBatch(items); err != nil {
		return UpsertSummary{}, err
	}
	rows := make([]CodeValueRow, 0, len(items))
	for _, it := range items {
		rows = append(rows, CodeValueRow{
			Code: it.Code, Display: it.Display, ParentCode: optional(it.ParentCode),
			ValidFrom: it.ValidFrom, ValidTo: it.ValidTo, Active: it.Active, Attributes: it.Attributes,
		})
	}

	var summary UpsertSummary
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		system, err := s.repo.GetCodeSystem(ctx, tx, rc.TenantID, systemID)
		if err != nil {
			return err
		}
		summary, err = s.repo.UpsertCodeValues(ctx, tx, rc.TenantID, systemID, rows)
		if err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, s.event(rc, "catalog.code_value.import", "code_system", systemID, map[string]any{
			"code": system.Code, "version": system.Version, "rows": len(rows),
			"created": summary.Created, "updated": summary.Updated, "skipped": summary.Skipped,
		}))
	})
	if err != nil {
		return UpsertSummary{}, err
	}
	return summary, nil
}
