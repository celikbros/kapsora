// Package partypg implements the person repository with sqlc. RLS hides other tenants'
// rows, so an id from another tenant is simply not found.
package partypg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/party/application"
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

// Constraint names mapped to problem codes (migrations 000003 and 000014).
const (
	constraintIdentifierHash    = "uq_person_identifier_hash"
	constraintIdentifierPrimary = "uq_person_identifier_primary"
	constraintExternalMemberNo  = "uq_sponsor_external_member_no"
)

// ListIdentifierTypes implements application.Repository.
func (Repository) ListIdentifierTypes(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]application.CatalogType, error) {
	rows, err := sqlcgen.New(tx).ListIdentifierTypes(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("party: list identifier types: %w", err)
	}
	out := make([]application.CatalogType, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.CatalogType{
			Code: r.Code, DisplayName: r.DisplayName, Status: r.Status,
			IsSensitive: r.IsSensitive, UniquenessScope: r.UniquenessScope,
		})
	}
	return out, nil
}

// GetIdentifierType implements application.Repository.
func (Repository) GetIdentifierType(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, code string) (application.CatalogType, error) {
	row, err := sqlcgen.New(tx).GetIdentifierType(ctx, sqlcgen.GetIdentifierTypeParams{TenantID: tenantID, Code: code})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CatalogType{}, application.ErrCatalogEntryNotFound
	}
	if err != nil {
		return application.CatalogType{}, fmt.Errorf("party: identifier type: %w", err)
	}
	return application.CatalogType{
		Code: row.Code, DisplayName: row.DisplayName, Status: row.Status,
		IsSensitive: row.IsSensitive, UniquenessScope: row.UniquenessScope,
	}, nil
}

// ListRelationshipTypes implements application.Repository.
func (Repository) ListRelationshipTypes(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]application.CatalogType, error) {
	rows, err := sqlcgen.New(tx).ListRelationshipTypes(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("party: list relationship types: %w", err)
	}
	out := make([]application.CatalogType, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.CatalogType{
			Code: r.Code, DisplayName: r.DisplayName, Status: r.Status, IsDirectional: r.IsDirectional,
		})
	}
	return out, nil
}

// GetRelationshipType implements application.Repository.
func (Repository) GetRelationshipType(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, code string) (application.CatalogType, error) {
	row, err := sqlcgen.New(tx).GetRelationshipType(ctx, sqlcgen.GetRelationshipTypeParams{TenantID: tenantID, Code: code})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CatalogType{}, application.ErrCatalogEntryNotFound
	}
	if err != nil {
		return application.CatalogType{}, fmt.Errorf("party: relationship type: %w", err)
	}
	return application.CatalogType{
		Code: row.Code, DisplayName: row.DisplayName, Status: row.Status, IsDirectional: row.IsDirectional,
	}, nil
}

// ListMembershipTypes implements application.Repository.
func (Repository) ListMembershipTypes(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]application.CatalogType, error) {
	rows, err := sqlcgen.New(tx).ListMembershipTypes(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("party: list membership types: %w", err)
	}
	out := make([]application.CatalogType, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.CatalogType{
			Code: r.Code, DisplayName: r.DisplayName, Status: r.Status, RequiresPrincipal: r.RequiresPrincipal,
		})
	}
	return out, nil
}

// GetMembershipType implements application.Repository.
func (Repository) GetMembershipType(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, code string) (application.CatalogType, error) {
	row, err := sqlcgen.New(tx).GetMembershipType(ctx, sqlcgen.GetMembershipTypeParams{TenantID: tenantID, Code: code})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CatalogType{}, application.ErrCatalogEntryNotFound
	}
	if err != nil {
		return application.CatalogType{}, fmt.Errorf("party: membership type: %w", err)
	}
	return application.CatalogType{
		Code: row.Code, DisplayName: row.DisplayName, Status: row.Status, RequiresPrincipal: row.RequiresPrincipal,
	}, nil
}

// CreatePerson implements application.Repository.
func (Repository) CreatePerson(ctx context.Context, tx pgx.Tx, in application.NewPersonRow) (uuid.UUID, error) {
	row, err := sqlcgen.New(tx).CreatePerson(ctx, sqlcgen.CreatePersonParams{
		TenantID: in.TenantID, FirstName: in.FirstName, MiddleName: in.MiddleName, LastName: in.LastName,
		NormalizedName: in.NormalizedName, BirthDate: date(in.BirthDate), SexAtBirth: in.SexAtBirth,
		ActorID: nullUUID(in.ActorID),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("party: create person: %w", err)
	}
	return row.ID, nil
}

// GetPerson implements application.Repository.
func (Repository) GetPerson(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) (application.PersonRow, error) {
	row, err := sqlcgen.New(tx).GetPerson(ctx, sqlcgen.GetPersonParams{TenantID: tenantID, ID: personID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PersonRow{}, application.ErrPersonNotFound
	}
	if err != nil {
		return application.PersonRow{}, fmt.Errorf("party: get person: %w", err)
	}
	return application.PersonRow{
		ID: row.ID, FirstName: row.FirstName, MiddleName: row.MiddleName, LastName: row.LastName,
		BirthDate: datePtr(row.BirthDate), SexAtBirth: row.SexAtBirth, Status: row.Status,
		MergedIntoID: uuidPtr(row.MergedIntoID), CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// UpdatePerson implements application.Repository.
func (Repository) UpdatePerson(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID, in application.PersonUpdateRow, expected int64) error {
	_, err := sqlcgen.New(tx).UpdatePerson(ctx, sqlcgen.UpdatePersonParams{
		TenantID: tenantID, ID: personID, RowVersion: expected,
		FirstName: in.FirstName, MiddleName: in.MiddleName, LastName: in.LastName,
		NormalizedName: in.NormalizedName, BirthDate: date(in.BirthDate), SexAtBirth: in.SexAtBirth,
		Status: in.Status, ActorID: nullUUID(in.ActorID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		return fmt.Errorf("party: update person: %w", err)
	}
	return nil
}

// ListPeople implements application.Repository.
func (Repository) ListPeople(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q application.ListQuery) ([]application.Summary, error) {
	params := sqlcgen.ListPeopleParams{TenantID: tenantID, PageSize: int32(q.PageSize)} //nolint:gosec // page size is clamped to 201
	if q.Status != "" {
		params.Status = &q.Status
	}
	if q.Pattern != "" {
		params.Q = &q.Pattern
	}
	if q.SponsorID != uuid.Nil {
		params.SponsorID = uuid.NullUUID{UUID: q.SponsorID, Valid: true}
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListPeople(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("party: list people: %w", err)
	}
	out := make([]application.Summary, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.Summary{
			ID: r.ID, FirstName: r.FirstName, MiddleName: r.MiddleName, LastName: r.LastName,
			Status: r.Status, MaskedPrimaryIdentifier: r.MaskedPrimaryIdentifier, CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// AddIdentifier implements application.Repository. A blind-index collision inside the
// type's uniqueness scope is reported with the owning person so the transport can decide
// whether the caller may learn it.
func (Repository) AddIdentifier(ctx context.Context, tx pgx.Tx, in application.NewIdentifier) error {
	q := sqlcgen.New(tx)
	// The insert runs inside a savepoint: a constraint violation would otherwise abort the
	// whole transaction and the owner lookup below could not run.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("party: identifier savepoint: %w", err)
	}
	err = sqlcgen.New(sp).AddPersonIdentifier(ctx, sqlcgen.AddPersonIdentifierParams{
		ID: in.ID, TenantID: in.TenantID, PersonID: in.PersonID, IdentifierType: in.Type,
		IdentifierCipher: in.Cipher, IdentifierHash: in.Hash, MaskedValue: in.MaskedValue,
		ScopeKey: in.ScopeKey, IsPrimary: in.Primary,
	})
	if err == nil {
		if err := sp.Commit(ctx); err != nil {
			return fmt.Errorf("party: release identifier savepoint: %w", err)
		}
		return nil
	}
	if rbErr := sp.Rollback(ctx); rbErr != nil {
		return fmt.Errorf("party: roll back identifier savepoint: %w", rbErr)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case constraintIdentifierHash:
			owner, findErr := q.FindPersonByIdentifierHash(ctx, sqlcgen.FindPersonByIdentifierHashParams{
				TenantID: in.TenantID, IdentifierType: in.Type, IdentifierHash: in.Hash, ScopeKey: &in.ScopeKey,
			})
			if findErr != nil && !errors.Is(findErr, pgx.ErrNoRows) {
				return fmt.Errorf("party: identifier owner: %w", findErr)
			}
			return &application.IdentifierTakenError{ExistingPersonID: owner}
		case constraintIdentifierPrimary:
			return &application.IdentifierTakenError{ExistingPersonID: in.PersonID}
		}
	}
	return fmt.Errorf("party: add identifier: %w", err)
}

// ListIdentifiers implements application.Repository.
func (Repository) ListIdentifiers(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) ([]application.StoredIdentifier, error) {
	rows, err := sqlcgen.New(tx).ListPersonIdentifiers(ctx, sqlcgen.ListPersonIdentifiersParams{TenantID: tenantID, PersonID: personID})
	if err != nil {
		return nil, fmt.Errorf("party: list identifiers: %w", err)
	}
	out := make([]application.StoredIdentifier, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.StoredIdentifier{
			ID: r.ID, Type: r.IdentifierType, Cipher: r.IdentifierCipher,
			MaskedValue: r.MaskedValue, ScopeKey: r.ScopeKey, Primary: r.IsPrimary,
		})
	}
	return out, nil
}

// RemoveIdentifiers implements application.Repository.
func (Repository) RemoveIdentifiers(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID, typeCode string) (int64, error) {
	n, err := sqlcgen.New(tx).DeletePersonIdentifiers(ctx, sqlcgen.DeletePersonIdentifiersParams{
		TenantID: tenantID, PersonID: personID, IdentifierType: typeCode,
	})
	if err != nil {
		return 0, fmt.Errorf("party: remove identifiers: %w", err)
	}
	return n, nil
}

// FindPersonByIdentifierHash implements application.Repository.
func (Repository) FindPersonByIdentifierHash(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, typeCode string, scopeKey *string, hash []byte) (uuid.UUID, bool, error) {
	id, err := sqlcgen.New(tx).FindPersonByIdentifierHash(ctx, sqlcgen.FindPersonByIdentifierHashParams{
		TenantID: tenantID, IdentifierType: typeCode, IdentifierHash: hash, ScopeKey: scopeKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("party: find by identifier: %w", err)
	}
	return id, true, nil
}

// ActiveSponsorOrganizations implements application.Repository.
func (Repository) ActiveSponsorOrganizations(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) ([]uuid.UUID, error) {
	ids, err := sqlcgen.New(tx).ActiveSponsorOrganizations(ctx, sqlcgen.ActiveSponsorOrganizationsParams{TenantID: tenantID, PersonID: personID})
	if err != nil {
		return nil, fmt.Errorf("party: active sponsors: %w", err)
	}
	return ids, nil
}

func date(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: *t, Valid: true}
}

func datePtr(d pgtype.Date) *time.Time {
	if !d.Valid || d.InfinityModifier != pgtype.Finite {
		return nil
	}
	t := d.Time
	return &t
}

func dateValue(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return d.Time
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}

func nullUUIDPtr(p *uuid.UUID) uuid.NullUUID {
	if p == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *p, Valid: true}
}
