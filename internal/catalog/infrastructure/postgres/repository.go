// Package catalogpg implements the catalog repository with sqlc. It is stateless: every
// method takes the caller's tenant-bound transaction, so RLS is active for every
// statement and a whole import commits or rolls back as one.
package catalogpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/catalog/application"
	"github.com/celikbros/kapsora/internal/catalog/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Repository is stateless; every method takes the caller's transaction.
type Repository struct{}

// New returns the repository.
func New() *Repository { return &Repository{} }

var _ application.Repository = (*Repository)(nil)

// PostgreSQL error codes mapped to named application errors.
const (
	uniqueViolation     = "23505"
	exclusionViolation  = "23P01"
	foreignKeyViolation = "23503"
)

// CreateCategory implements application.Repository.
func (Repository) CreateCategory(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in application.NewCategoryRow) (uuid.UUID, error) {
	id, err := sqlcgen.New(tx).CreateServiceCategory(ctx, sqlcgen.CreateServiceCategoryParams{
		TenantID: tenantID, ParentID: nullUUID(in.ParentID), Code: in.Code,
		Name: in.Name, DomainCode: in.Domain, Active: in.Active,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch {
			case pgErr.Code == uniqueViolation && pgErr.ConstraintName == "uq_service_category_code":
				return uuid.Nil, application.ErrCategoryCodeTaken
			case pgErr.Code == foreignKeyViolation:
				return uuid.Nil, application.ErrCategoryNotFound
			}
		}
		return uuid.Nil, fmt.Errorf("catalog: create category: %w", err)
	}
	return id, nil
}

// GetCategory implements application.Repository.
func (Repository) GetCategory(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.CategoryRecord, error) {
	row, err := sqlcgen.New(tx).GetServiceCategory(ctx, sqlcgen.GetServiceCategoryParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CategoryRecord{}, application.ErrCategoryNotFound
	}
	if err != nil {
		return application.CategoryRecord{}, fmt.Errorf("catalog: get category: %w", err)
	}
	return application.CategoryRecord{
		ID: row.ID, ParentID: uuidPtr(row.ParentID), Code: row.Code, Name: row.Name,
		Domain: row.DomainCode, Active: row.Active, CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// LockCategory implements application.Repository.
func (Repository) LockCategory(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.CategoryRecord, error) {
	row, err := sqlcgen.New(tx).LockServiceCategory(ctx, sqlcgen.LockServiceCategoryParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CategoryRecord{}, application.ErrCategoryNotFound
	}
	if err != nil {
		return application.CategoryRecord{}, fmt.Errorf("catalog: lock category: %w", err)
	}
	return application.CategoryRecord{
		ID: row.ID, ParentID: uuidPtr(row.ParentID), Code: row.Code, Name: row.Name,
		Domain: row.DomainCode, Active: row.Active, CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// ListCategories implements application.Repository.
func (Repository) ListCategories(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q application.CategoryQuery) ([]application.CategoryRecord, error) {
	params := sqlcgen.ListServiceCategoriesParams{
		TenantID: tenantID, ParentID: nullUUID(q.ParentID), PageSize: pageSize(q.PageSize),
	}
	params.DomainCode = optionalString(q.Domain)
	params.Q = optionalString(q.Query)
	params.Active = q.Active
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListServiceCategories(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("catalog: list categories: %w", err)
	}
	out := make([]application.CategoryRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.CategoryRecord{
			ID: r.ID, ParentID: uuidPtr(r.ParentID), Code: r.Code, Name: r.Name,
			Domain: r.DomainCode, Active: r.Active, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// UpdateCategory implements application.Repository. The expected version is part of the
// predicate, so a row someone else moved in the meantime is simply not updated.
func (Repository) UpdateCategory(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in application.CategoryUpdateRow) error {
	n, err := sqlcgen.New(tx).UpdateServiceCategory(ctx, sqlcgen.UpdateServiceCategoryParams{
		TenantID: tenantID, ID: id, ParentID: nullUUID(in.ParentID), Name: in.Name, Active: in.Active,
		RowVersion: in.ExpectedVersion,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
			return application.ErrCategoryNotFound
		}
		return fmt.Errorf("catalog: update category: %w", err)
	}
	if n == 0 {
		return application.ErrCategoryNotFound
	}
	return nil
}

// CategoryAncestors implements application.Repository.
func (Repository) CategoryAncestors(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) ([]uuid.UUID, error) {
	rows, err := sqlcgen.New(tx).ServiceCategoryAncestors(ctx, sqlcgen.ServiceCategoryAncestorsParams{TenantID: tenantID, ID: id})
	if err != nil {
		return nil, fmt.Errorf("catalog: category ancestors: %w", err)
	}
	out := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out, nil
}

// CategorySubtreeHeight implements application.Repository.
func (Repository) CategorySubtreeHeight(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (int, error) {
	h, err := sqlcgen.New(tx).ServiceCategorySubtreeHeight(ctx, sqlcgen.ServiceCategorySubtreeHeightParams{TenantID: tenantID, ID: id})
	if err != nil {
		return 0, fmt.Errorf("catalog: category subtree height: %w", err)
	}
	return int(h), nil
}

// CreateDefinition implements application.Repository.
func (Repository) CreateDefinition(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in application.NewDefinitionRow) (uuid.UUID, error) {
	row, err := sqlcgen.New(tx).CreateServiceDefinition(ctx, sqlcgen.CreateServiceDefinitionParams{
		TenantID: tenantID, CategoryID: in.CategoryID, Code: in.Code, Name: in.Name,
		Description: in.Description, FulfillmentMode: in.FulfillmentMode,
		DefaultUnitType: in.DefaultUnitType, RequiresProvider: in.RequiresProvider, Active: in.Active,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch {
			case pgErr.Code == uniqueViolation && pgErr.ConstraintName == "uq_service_definition_code":
				return uuid.Nil, application.ErrDefinitionCodeTaken
			case pgErr.Code == foreignKeyViolation:
				return uuid.Nil, application.ErrCategoryNotFound
			}
		}
		return uuid.Nil, fmt.Errorf("catalog: create definition: %w", err)
	}
	return row.ID, nil
}

// GetDefinition implements application.Repository.
func (Repository) GetDefinition(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.DefinitionRecord, error) {
	row, err := sqlcgen.New(tx).GetServiceDefinition(ctx, sqlcgen.GetServiceDefinitionParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.DefinitionRecord{}, application.ErrDefinitionNotFound
	}
	if err != nil {
		return application.DefinitionRecord{}, fmt.Errorf("catalog: get definition: %w", err)
	}
	return application.DefinitionRecord{
		ID: row.ID, CategoryID: row.CategoryID, CategoryCode: row.CategoryCode, Domain: row.DomainCode,
		Code: row.Code, Name: row.Name, Description: row.Description, FulfillmentMode: row.FulfillmentMode,
		DefaultUnitType: row.DefaultUnitType, RequiresProvider: row.RequiresProvider, Active: row.Active,
		CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// ListDefinitions implements application.Repository.
func (Repository) ListDefinitions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q application.DefinitionQuery) ([]application.DefinitionRecord, error) {
	params := sqlcgen.ListServiceDefinitionsParams{
		TenantID: tenantID, CategoryID: nullUUID(q.CategoryID), PageSize: pageSize(q.PageSize),
	}
	params.DomainCode = optionalString(q.Domain)
	params.Q = optionalString(q.Query)
	params.Active = q.Active
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListServiceDefinitions(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("catalog: list definitions: %w", err)
	}
	out := make([]application.DefinitionRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.DefinitionRecord{
			ID: r.ID, CategoryID: r.CategoryID, CategoryCode: r.CategoryCode, Domain: r.DomainCode,
			Code: r.Code, Name: r.Name, Description: r.Description, FulfillmentMode: r.FulfillmentMode,
			DefaultUnitType: r.DefaultUnitType, RequiresProvider: r.RequiresProvider, Active: r.Active,
			CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// UpdateDefinition implements application.Repository.
func (Repository) UpdateDefinition(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in application.DefinitionUpdateRow, expected int64) error {
	_, err := sqlcgen.New(tx).UpdateServiceDefinition(ctx, sqlcgen.UpdateServiceDefinitionParams{
		TenantID: tenantID, ID: id, RowVersion: expected, CategoryID: in.CategoryID, Name: in.Name,
		Description: in.Description, FulfillmentMode: in.FulfillmentMode,
		DefaultUnitType: in.DefaultUnitType, RequiresProvider: in.RequiresProvider, Active: in.Active,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
			return application.ErrCategoryNotFound
		}
		return fmt.Errorf("catalog: update definition: %w", err)
	}
	return nil
}

// TouchDefinition implements application.Repository.
func (Repository) TouchDefinition(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, expected int64) error {
	n, err := sqlcgen.New(tx).TouchServiceDefinition(ctx, sqlcgen.TouchServiceDefinitionParams{
		TenantID: tenantID, ID: id, RowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("catalog: touch definition: %w", err)
	}
	if n == 0 {
		return application.ErrVersionMismatch
	}
	return nil
}

// CreateCodeSystem implements application.Repository.
func (Repository) CreateCodeSystem(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in application.NewCodeSystemRow) (uuid.UUID, error) {
	row, err := sqlcgen.New(tx).CreateCodeSystem(ctx, sqlcgen.CreateCodeSystemParams{
		TenantID: tenantID, Code: in.Code, Name: in.Name, Version: in.Version,
		Authority: in.Authority, Licensed: in.Licensed,
		ValidFrom: dateValue(&in.ValidFrom), ValidTo: dateValue(in.ValidTo),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == "uq_code_system_code" {
			return uuid.Nil, application.ErrCodeSystemTaken
		}
		return uuid.Nil, fmt.Errorf("catalog: create code system: %w", err)
	}
	return row.ID, nil
}

// GetCodeSystem implements application.Repository.
func (Repository) GetCodeSystem(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.CodeSystemRecord, error) {
	row, err := sqlcgen.New(tx).GetCodeSystem(ctx, sqlcgen.GetCodeSystemParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CodeSystemRecord{}, application.ErrCodeSystemNotFound
	}
	if err != nil {
		return application.CodeSystemRecord{}, fmt.Errorf("catalog: get code system: %w", err)
	}
	return codeSystemRecord(row.ID, row.Code, row.Name, row.Version, row.Authority, row.Status,
		row.Licensed, row.ValidFrom, row.ValidTo, row.CreatedAt, row.RowVersion), nil
}

// ListCodeSystems implements application.Repository.
func (Repository) ListCodeSystems(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q application.CodeSystemQuery) ([]application.CodeSystemRecord, error) {
	params := sqlcgen.ListCodeSystemsParams{TenantID: tenantID, PageSize: pageSize(q.PageSize)}
	params.Authority = optionalString(q.Authority)
	params.Status = optionalString(q.Status)
	params.Q = optionalString(q.Query)
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListCodeSystems(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("catalog: list code systems: %w", err)
	}
	out := make([]application.CodeSystemRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, codeSystemRecord(r.ID, r.Code, r.Name, r.Version, r.Authority, r.Status,
			r.Licensed, r.ValidFrom, r.ValidTo, r.CreatedAt, r.RowVersion))
	}
	return out, nil
}

// UpdateCodeSystem implements application.Repository.
func (Repository) UpdateCodeSystem(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in application.CodeSystemUpdateRow, expected int64) error {
	_, err := sqlcgen.New(tx).UpdateCodeSystem(ctx, sqlcgen.UpdateCodeSystemParams{
		TenantID: tenantID, ID: id, RowVersion: expected, Name: in.Name, Authority: in.Authority,
		Licensed: in.Licensed, Status: in.Status, ValidTo: dateValue(in.ValidTo),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		return fmt.Errorf("catalog: update code system: %w", err)
	}
	return nil
}

// UpsertCodeValues implements application.Repository. The rows go out as one pipelined
// batch inside the caller's transaction, so 5000 values cost one round trip rather than
// 5000, and any failure aborts the whole transaction.
func (Repository) UpsertCodeValues(ctx context.Context, tx pgx.Tx, tenantID, systemID uuid.UUID, rows []application.CodeValueRow) (application.UpsertSummary, error) {
	if len(rows) == 0 {
		return application.UpsertSummary{}, nil
	}
	params := make([]sqlcgen.UpsertCodeValueParams, 0, len(rows))
	for _, r := range rows {
		params = append(params, sqlcgen.UpsertCodeValueParams{
			TenantID: tenantID, CodeSystemID: systemID, Code: r.Code, Display: r.Display,
			ParentCode: r.ParentCode, ValidFrom: dateValue(&r.ValidFrom), ValidTo: dateValue(r.ValidTo),
			Active: r.Active, Attributes: r.Attributes,
		})
	}

	var summary application.UpsertSummary
	var firstErr error
	batch := sqlcgen.New(tx).UpsertCodeValue(ctx, params)
	batch.QueryRow(func(_ int, row sqlcgen.UpsertCodeValueRow, err error) {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// The stored row already carries exactly these values, so nothing was written.
			summary.Skipped++
		case err != nil:
			if firstErr == nil {
				firstErr = err
			}
		case row.Created:
			summary.Created++
		default:
			summary.Updated++
		}
	})
	if err := batch.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if firstErr != nil {
		var pgErr *pgconn.PgError
		if errors.As(firstErr, &pgErr) && pgErr.Code == foreignKeyViolation {
			return application.UpsertSummary{}, application.ErrCodeSystemNotFound
		}
		return application.UpsertSummary{}, fmt.Errorf("catalog: import code values: %w", firstErr)
	}
	return summary, nil
}

// ListCodeValues implements application.Repository.
func (Repository) ListCodeValues(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q application.CodeValueQuery) ([]application.CodeValueRecord, error) {
	params := sqlcgen.ListCodeValuesParams{
		TenantID: tenantID, CodeSystemID: q.CodeSystemID, AsOf: dateValue(&q.AsOf), PageSize: pageSize(q.PageSize),
	}
	params.Code = optionalString(q.Code)
	params.Q = optionalString(q.Query)
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListCodeValues(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("catalog: list code values: %w", err)
	}
	out := make([]application.CodeValueRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.CodeValueRecord{
			ID: r.ID, CodeSystemID: r.CodeSystemID, Code: r.Code, Display: r.Display,
			ParentCode: r.ParentCode, ValidFrom: dateTime(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
			Active: r.Active, Attributes: r.Attributes, CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// ListMappings implements application.Repository.
func (Repository) ListMappings(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]application.MappingRecord, error) {
	rows, err := sqlcgen.New(tx).ListServiceCodeMappings(ctx, sqlcgen.ListServiceCodeMappingsParams{
		TenantID: tenantID, ServiceDefinitionID: definitionID,
	})
	if err != nil {
		return nil, fmt.Errorf("catalog: list code mappings: %w", err)
	}
	out := make([]application.MappingRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.MappingRecord{
			ID: r.ID, ServiceDefinitionID: r.ServiceDefinitionID, CodeSystemID: r.CodeSystemID,
			CodeSystemCode: r.CodeSystemCode, CodeSystemVersion: r.CodeSystemVersion, Code: r.Code,
			ValidFrom: dateTime(r.ValidFrom), ValidTo: datePtr(r.ValidTo), Primary: r.IsPrimary,
		})
	}
	return out, nil
}

// ReplaceMappings implements application.Repository. The exclusion constraints of
// migration 000019 are the authority on overlap, so the conflict is mapped from the
// PostgreSQL error rather than pre-checked against the stored rows.
func (Repository) ReplaceMappings(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID, rows []application.MappingRow) error {
	q := sqlcgen.New(tx)
	if _, err := q.DeleteServiceCodeMappings(ctx, sqlcgen.DeleteServiceCodeMappingsParams{
		TenantID: tenantID, ServiceDefinitionID: definitionID,
	}); err != nil {
		return fmt.Errorf("catalog: clear code mappings: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	params := make([]sqlcgen.CreateServiceCodeMappingParams, 0, len(rows))
	for _, r := range rows {
		params = append(params, sqlcgen.CreateServiceCodeMappingParams{
			TenantID: tenantID, ServiceDefinitionID: definitionID, CodeSystemID: r.CodeSystemID,
			Code: r.Code, ValidFrom: dateValue(&r.ValidFrom), ValidTo: dateValue(r.ValidTo), IsPrimary: r.Primary,
		})
	}
	var firstErr error
	batch := q.CreateServiceCodeMapping(ctx, params)
	batch.Exec(func(_ int, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	if err := batch.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if firstErr != nil {
		var pgErr *pgconn.PgError
		if errors.As(firstErr, &pgErr) {
			switch pgErr.Code {
			case exclusionViolation:
				return domain.ErrMappingOverlap
			case foreignKeyViolation:
				return application.ErrCodeSystemNotFound
			}
		}
		return fmt.Errorf("catalog: replace code mappings: %w", firstErr)
	}
	return nil
}

func codeSystemRecord(id uuid.UUID, code, name, version, authority, status string, licensed bool,
	validFrom, validTo pgtype.Date, createdAt time.Time, rowVersion int64,
) application.CodeSystemRecord {
	return application.CodeSystemRecord{
		ID: id, Code: code, Name: name, Version: version, Authority: authority, Status: status,
		Licensed: licensed, ValidFrom: dateTime(validFrom), ValidTo: datePtr(validTo),
		CreatedAt: createdAt, RowVersion: rowVersion,
	}
}

// pageSize keeps the int32 conversion in one place; the caller has already clamped it to
// at most httpx.MaxPageSize+1.
func pageSize(n int) int32 {
	if n <= 0 {
		return 1
	}
	return int32(n) //nolint:gosec // clamped by httpx.ClampLimit before it reaches here
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}

func dateValue(t *time.Time) pgtype.Date {
	if t == nil || t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: domain.DateOnly(*t), Valid: true}
}

func dateTime(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return d.Time
}

func datePtr(d pgtype.Date) *time.Time {
	if !d.Valid || d.InfinityModifier != pgtype.Finite {
		return nil
	}
	t := d.Time
	return &t
}
