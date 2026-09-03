// Package application implements the organization directory use cases: create with
// global deduplication, get, list with keyset pagination and merge-patch update with
// optimistic concurrency. Transactions are opened here with db.WithTenantTx so a create
// and its audit row commit together.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/organization/domain"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Errors mapped by the transport layer.
var (
	ErrNotFound           = errors.New("organization: relationship not found")
	ErrRelationshipExists = errors.New("organization: the tenant already has this relationship role with the organization")
	ErrIdentifierTaken    = errors.New("organization: identifier belongs to another organization")
	ErrTenantCodeTaken    = errors.New("organization: tenant code already used")
	ErrVersionMismatch    = errors.New("organization: row version does not match If-Match")
	ErrSharedReadOnly     = errors.New("organization: shared organization can only be renamed by its sole tenant")
)

// NewGlobal creates a directory.organization row; TaxCipher/TaxHash are nil when the
// organization has no tax number (non-TR without one).
type NewGlobal struct {
	LegalName        string
	DisplayName      string
	OrganizationKind string
	CountryCode      string
	TaxCipher        []byte
	TaxHash          []byte
}

// NewRelationship creates a directory.tenant_organization row.
type NewRelationship struct {
	TenantID         uuid.UUID
	OrganizationID   uuid.UUID
	RelationshipRole string
	TenantCode       *string
}

// Record is the joined relationship + organization row as stored.
type Record struct {
	ID                 uuid.UUID
	OrganizationID     uuid.UUID
	LegalName          string
	DisplayName        string
	OrganizationKind   string
	CountryCode        string
	OrganizationStatus string
	RelationshipRole   string
	RelationshipStatus string
	TenantCode         *string
	ValidFrom          time.Time
	ValidTo            *time.Time
	CreatedAt          time.Time
	RowVersion         int64
	TaxCipher          []byte
	Identifiers        []StoredIdentifier
}

// StoredIdentifier is a public identifier row.
type StoredIdentifier struct {
	Type    domain.IdentifierType
	Value   string
	Primary bool
}

// Summary is one list row.
type Summary struct {
	ID                 uuid.UUID
	OrganizationID     uuid.UUID
	DisplayName        string
	OrganizationKind   string
	RelationshipRole   string
	RelationshipStatus string
	CreatedAt          time.Time
}

// ListQuery is the repository-level filter.
type ListQuery struct {
	Role     string
	Query    string
	After    *httpx.Cursor
	PageSize int
}

// Repository is the persistence port; every method runs inside the caller's transaction.
type Repository interface {
	FindGlobalByTaxHash(ctx context.Context, tx pgx.Tx, country string, hash []byte) (uuid.UUID, bool, error)
	CreateGlobal(ctx context.Context, tx pgx.Tx, in NewGlobal) (uuid.UUID, error)
	UpdateGlobalDisplayName(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, name string) error
	RelationshipCount(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) (int, error)
	// AddIdentifier links a public identifier to the organization; ErrIdentifierTaken when
	// another organization already carries it.
	AddIdentifier(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, id domain.Identifier, country string) error
	CreateRelationship(ctx context.Context, tx pgx.Tx, in NewRelationship) (uuid.UUID, error)
	Get(ctx context.Context, tx pgx.Tx, tenantID, relationshipID uuid.UUID) (Record, error)
	List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q ListQuery) ([]Summary, error)
	// UpdateRelationship applies status and tenant code when row_version still equals
	// expected; ErrVersionMismatch otherwise. The trigger bumps row_version.
	UpdateRelationship(ctx context.Context, tx pgx.Tx, tenantID, relationshipID uuid.UUID, status string, tenantCode *string, expected int64) (int64, error)
}
