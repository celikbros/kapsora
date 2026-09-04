// Package application implements the catalog use cases: the service category tree, the
// service definitions every later module points at, the external code systems those
// definitions are reported under, the import of code values and the mapping between the
// two. Transactions are opened here with db.WithTenantTx, so a write and its audit row
// commit together and RLS is bound for every statement.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Errors mapped by the transport layer to problem codes.
var (
	ErrCategoryNotFound    = errors.New("catalog: service category not found")
	ErrDefinitionNotFound  = errors.New("catalog: service definition not found")
	ErrCodeSystemNotFound  = errors.New("catalog: code system not found")
	ErrCategoryCodeTaken   = errors.New("catalog: category code already used in this tenant")
	ErrDefinitionCodeTaken = errors.New("catalog: service definition code already used in this tenant")
	ErrCodeSystemTaken     = errors.New("catalog: code system code and version already registered")
	ErrVersionMismatch     = errors.New("catalog: row version does not match If-Match")
)

// CategoryRecord is one catalog.service_category row as stored. RowVersion is the
// token, because the table predates the platform row_version convention.
type CategoryRecord struct {
	ID         uuid.UUID
	ParentID   *uuid.UUID
	Code       string
	Name       string
	Domain     string
	Active     bool
	CreatedAt  time.Time
	RowVersion int64
}

// NewCategoryRow is the insert payload for a category.
type NewCategoryRow struct {
	ParentID *uuid.UUID
	Code     string
	Name     string
	Domain   string
	Active   bool
}

// CategoryUpdateRow is the update payload for a category, with the version it expects.
type CategoryUpdateRow struct {
	ParentID        *uuid.UUID
	Name            string
	Active          bool
	ExpectedVersion int64
}

// DefinitionRecord is one catalog.service_definition row joined with its category.
type DefinitionRecord struct {
	ID               uuid.UUID
	CategoryID       uuid.UUID
	CategoryCode     string
	Domain           string
	Code             string
	Name             string
	Description      *string
	FulfillmentMode  string
	DefaultUnitType  string
	RequiresProvider bool
	Active           bool
	CreatedAt        time.Time
	RowVersion       int64
}

// NewDefinitionRow is the insert payload for a service definition.
type NewDefinitionRow struct {
	CategoryID       uuid.UUID
	Code             string
	Name             string
	Description      *string
	FulfillmentMode  string
	DefaultUnitType  string
	RequiresProvider bool
	Active           bool
}

// DefinitionUpdateRow is the update payload for a service definition; Code is absent
// because it is immutable.
type DefinitionUpdateRow struct {
	CategoryID       uuid.UUID
	Name             string
	Description      *string
	FulfillmentMode  string
	DefaultUnitType  string
	RequiresProvider bool
	Active           bool
}

// CodeSystemRecord is one catalog.code_system row as stored.
type CodeSystemRecord struct {
	ID         uuid.UUID
	Code       string
	Name       string
	Version    string
	Authority  string
	Licensed   bool
	Status     string
	ValidFrom  time.Time
	ValidTo    *time.Time
	CreatedAt  time.Time
	RowVersion int64
}

// NewCodeSystemRow is the insert payload for a code system edition.
type NewCodeSystemRow struct {
	Code      string
	Name      string
	Version   string
	Authority string
	Licensed  bool
	ValidFrom time.Time
	ValidTo   *time.Time
}

// CodeSystemUpdateRow is the update payload for a code system; code and version are
// absent because they identify the edition.
type CodeSystemUpdateRow struct {
	Name      string
	Authority string
	Licensed  bool
	Status    string
	ValidTo   *time.Time
}

// CodeValueRecord is one catalog.code_value row as stored.
type CodeValueRecord struct {
	ID           uuid.UUID
	CodeSystemID uuid.UUID
	Code         string
	Display      string
	ParentCode   *string
	ValidFrom    time.Time
	ValidTo      *time.Time
	Active       bool
	Attributes   []byte
	CreatedAt    time.Time
}

// CodeValueRow is one row of an import batch as it reaches the repository.
type CodeValueRow struct {
	Code       string
	Display    string
	ParentCode *string
	ValidFrom  time.Time
	ValidTo    *time.Time
	Active     bool
	Attributes []byte
}

// UpsertSummary counts what one import batch did.
type UpsertSummary struct {
	Created int
	Updated int
	Skipped int
}

// MappingRecord is one catalog.service_code_mapping row joined with its code system.
type MappingRecord struct {
	ID                  uuid.UUID
	ServiceDefinitionID uuid.UUID
	CodeSystemID        uuid.UUID
	CodeSystemCode      string
	CodeSystemVersion   string
	Code                string
	ValidFrom           time.Time
	ValidTo             *time.Time
	Primary             bool
}

// MappingRow is one row of a mapping replacement as it reaches the repository.
type MappingRow struct {
	CodeSystemID uuid.UUID
	Code         string
	ValidFrom    time.Time
	ValidTo      *time.Time
	Primary      bool
}

// CategoryQuery is the repository-level category filter.
type CategoryQuery struct {
	ParentID *uuid.UUID
	Domain   string
	Active   *bool
	Query    string
	After    *httpx.Cursor
	PageSize int
}

// DefinitionQuery is the repository-level definition filter.
type DefinitionQuery struct {
	CategoryID *uuid.UUID
	Domain     string
	Active     *bool
	Query      string
	After      *httpx.Cursor
	PageSize   int
}

// CodeSystemQuery is the repository-level code system filter.
type CodeSystemQuery struct {
	Authority string
	Status    string
	Query     string
	After     *httpx.Cursor
	PageSize  int
}

// CodeValueQuery is the repository-level code value filter; AsOf is always set, because
// a code system is only ever read as of a date.
type CodeValueQuery struct {
	CodeSystemID uuid.UUID
	AsOf         time.Time
	Code         string
	Query        string
	After        *httpx.Cursor
	PageSize     int
}

// Repository is the persistence port; every method runs inside the caller's transaction,
// which db.WithTenantTx has already bound to the tenant so RLS is active.
type Repository interface {
	CreateCategory(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewCategoryRow) (uuid.UUID, error)
	GetCategory(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (CategoryRecord, error)
	// LockCategory reads the row FOR UPDATE so two concurrent re-parents cannot
	// between the optimistic-concurrency check and the update that follows it.
	LockCategory(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (CategoryRecord, error)
	ListCategories(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q CategoryQuery) ([]CategoryRecord, error)
	UpdateCategory(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in CategoryUpdateRow) error
	// CategoryAncestors returns the chain from id up to its root, id itself first.
	CategoryAncestors(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) ([]uuid.UUID, error)
	// CategorySubtreeHeight returns how many levels hang below id, id counting as one.
	CategorySubtreeHeight(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (int, error)

	CreateDefinition(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewDefinitionRow) (uuid.UUID, error)
	GetDefinition(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (DefinitionRecord, error)
	ListDefinitions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q DefinitionQuery) ([]DefinitionRecord, error)
	UpdateDefinition(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in DefinitionUpdateRow, expected int64) error
	// TouchDefinition bumps row_version without changing a business field, so replacing
	// the mapping set invalidates the ETag the caller holds.
	TouchDefinition(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, expected int64) error

	CreateCodeSystem(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewCodeSystemRow) (uuid.UUID, error)
	GetCodeSystem(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (CodeSystemRecord, error)
	ListCodeSystems(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q CodeSystemQuery) ([]CodeSystemRecord, error)
	UpdateCodeSystem(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in CodeSystemUpdateRow, expected int64) error

	// UpsertCodeValues applies the whole batch in the caller's transaction; any error
	// leaves the transaction to be rolled back, so an import is all-or-nothing.
	UpsertCodeValues(ctx context.Context, tx pgx.Tx, tenantID, systemID uuid.UUID, rows []CodeValueRow) (UpsertSummary, error)
	ListCodeValues(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q CodeValueQuery) ([]CodeValueRecord, error)

	ListMappings(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]MappingRecord, error)
	// ReplaceMappings deletes the current set and inserts rows; the exclusion constraints
	// of migration 000019 surface as domain.ErrMappingOverlap.
	ReplaceMappings(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID, rows []MappingRow) error
}
