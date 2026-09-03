// Package organizationpg implements the organization repository with sqlc.
package organizationpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/organization/application"
	"github.com/celikbros/kapsora/internal/organization/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Repository is stateless; every method takes the caller's transaction.
type Repository struct{}

// New returns the repository.
func New() *Repository { return &Repository{} }

var _ application.Repository = (*Repository)(nil)

const (
	uniqueViolation    = "23505"
	exclusionViolation = "23P01"
)

// FindGlobalByTaxHash implements application.Repository.
func (Repository) FindGlobalByTaxHash(ctx context.Context, tx pgx.Tx, country string, hash []byte) (uuid.UUID, bool, error) {
	row, err := sqlcgen.New(tx).FindOrganizationByTaxHash(ctx, sqlcgen.FindOrganizationByTaxHashParams{CountryCode: country, TaxNumberHash: hash})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("organization: find by tax hash: %w", err)
	}
	return row.ID, true, nil
}

// CreateGlobal implements application.Repository.
func (Repository) CreateGlobal(ctx context.Context, tx pgx.Tx, in application.NewGlobal) (uuid.UUID, error) {
	id, err := sqlcgen.New(tx).CreateOrganization(ctx, sqlcgen.CreateOrganizationParams{
		LegalName: in.LegalName, DisplayName: in.DisplayName, OrganizationKind: in.OrganizationKind,
		CountryCode: in.CountryCode, TaxNumberCipher: in.TaxCipher, TaxNumberHash: in.TaxHash,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("organization: create global: %w", err)
	}
	return id, nil
}

// UpdateGlobalDisplayName implements application.Repository.
func (Repository) UpdateGlobalDisplayName(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, name string) error {
	if err := sqlcgen.New(tx).UpdateOrganizationDisplayName(ctx, sqlcgen.UpdateOrganizationDisplayNameParams{ID: orgID, DisplayName: name}); err != nil {
		return fmt.Errorf("organization: rename: %w", err)
	}
	return nil
}

// RelationshipCount implements application.Repository.
func (Repository) RelationshipCount(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) (int, error) {
	n, err := sqlcgen.New(tx).OrganizationRelationshipCount(ctx, orgID)
	if err != nil {
		return 0, fmt.Errorf("organization: relationship count: %w", err)
	}
	return int(n), nil
}

// AddIdentifier implements application.Repository.
func (Repository) AddIdentifier(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, id domain.Identifier, country string) error {
	q := sqlcgen.New(tx)
	var issuing *string
	if country != "" {
		issuing = &country
	}
	n, err := q.AddOrganizationIdentifier(ctx, sqlcgen.AddOrganizationIdentifierParams{
		OrganizationID: orgID, IdentifierType: string(id.Type), IdentifierValue: id.Value, IssuingCountry: issuing, IsPrimary: id.Primary,
	})
	if err != nil {
		return fmt.Errorf("organization: add identifier: %w", err)
	}
	if n == 1 {
		return nil
	}
	owner, err := q.FindOrganizationIdentifierOwner(ctx, sqlcgen.FindOrganizationIdentifierOwnerParams{IdentifierType: string(id.Type), IdentifierValue: id.Value})
	if err != nil {
		return fmt.Errorf("organization: identifier owner: %w", err)
	}
	if owner != orgID {
		return application.ErrIdentifierTaken
	}
	return nil
}

// CreateRelationship implements application.Repository.
func (Repository) CreateRelationship(ctx context.Context, tx pgx.Tx, in application.NewRelationship) (uuid.UUID, error) {
	row, err := sqlcgen.New(tx).CreateTenantOrganizationRelationship(ctx, sqlcgen.CreateTenantOrganizationRelationshipParams{
		TenantID: in.TenantID, OrganizationID: in.OrganizationID, RelationshipRole: in.RelationshipRole, TenantCode: in.TenantCode,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch {
			case pgErr.Code == exclusionViolation:
				return uuid.Nil, application.ErrRelationshipExists
			case pgErr.Code == uniqueViolation && pgErr.ConstraintName == "uq_tenant_org_code":
				return uuid.Nil, application.ErrTenantCodeTaken
			}
		}
		return uuid.Nil, fmt.Errorf("organization: create relationship: %w", err)
	}
	return row.ID, nil
}

// Get implements application.Repository. RLS hides other tenants' rows, so an id from
// another tenant is simply not found.
func (Repository) Get(ctx context.Context, tx pgx.Tx, tenantID, relationshipID uuid.UUID) (application.Record, error) {
	q := sqlcgen.New(tx)
	row, err := q.GetTenantOrganization(ctx, sqlcgen.GetTenantOrganizationParams{TenantID: tenantID, ID: relationshipID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Record{}, application.ErrNotFound
	}
	if err != nil {
		return application.Record{}, fmt.Errorf("organization: get: %w", err)
	}
	ids, err := q.ListOrganizationIdentifiers(ctx, row.OrganizationID)
	if err != nil {
		return application.Record{}, fmt.Errorf("organization: identifiers: %w", err)
	}
	rec := application.Record{
		ID: row.ID, OrganizationID: row.OrganizationID, LegalName: row.LegalName, DisplayName: row.DisplayName,
		OrganizationKind: row.OrganizationKind, CountryCode: row.CountryCode, OrganizationStatus: row.OrganizationStatus,
		RelationshipRole: row.RelationshipRole, RelationshipStatus: row.RelationshipStatus, TenantCode: row.TenantCode,
		ValidFrom: dateValue(row.ValidFrom), ValidTo: datePtr(row.ValidTo), CreatedAt: row.CreatedAt,
		RowVersion: row.RowVersion, TaxCipher: row.TaxNumberCipher,
		Identifiers: make([]application.StoredIdentifier, 0, len(ids)),
	}
	for _, id := range ids {
		rec.Identifiers = append(rec.Identifiers, application.StoredIdentifier{Type: domain.IdentifierType(id.IdentifierType), Value: id.IdentifierValue, Primary: id.IsPrimary})
	}
	return rec, nil
}

// List implements application.Repository.
func (Repository) List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q application.ListQuery) ([]application.Summary, error) {
	params := sqlcgen.ListTenantOrganizationsParams{TenantID: tenantID, PageSize: int32(q.PageSize)} //nolint:gosec // page size is clamped to 201
	if q.Role != "" {
		params.Role = &q.Role
	}
	if q.Query != "" {
		params.Q = &q.Query
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListTenantOrganizations(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("organization: list: %w", err)
	}
	out := make([]application.Summary, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.Summary{
			ID: r.ID, OrganizationID: r.OrganizationID, DisplayName: r.DisplayName, OrganizationKind: r.OrganizationKind,
			RelationshipRole: r.RelationshipRole, RelationshipStatus: r.RelationshipStatus, CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// UpdateRelationship implements application.Repository.
func (Repository) UpdateRelationship(ctx context.Context, tx pgx.Tx, tenantID, relationshipID uuid.UUID, status string, tenantCode *string, expected int64) (int64, error) {
	v, err := sqlcgen.New(tx).UpdateTenantOrganization(ctx, sqlcgen.UpdateTenantOrganizationParams{
		TenantID: tenantID, ID: relationshipID, Status: status, TenantCode: tenantCode, RowVersion: expected,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, application.ErrVersionMismatch
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == "uq_tenant_org_code" {
			return 0, application.ErrTenantCodeTaken
		}
		return 0, fmt.Errorf("organization: update relationship: %w", err)
	}
	return v, nil
}

func dateValue(d pgtype.Date) time.Time {
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
