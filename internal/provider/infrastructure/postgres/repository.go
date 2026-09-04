// Package providerpg implements the provider network repository with sqlc. It is
// stateless: every method takes the caller's tenant-bound transaction, so RLS is active for
// every statement, and every method takes the caller's Scope, so the provider boundary is
// part of the SQL rather than something a handler has to remember.
package providerpg

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	"github.com/celikbros/kapsora/internal/provider/application"
	"github.com/celikbros/kapsora/internal/provider/domain"
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

// GetOrganization implements application.Repository.
func (Repository) GetOrganization(ctx context.Context, tx pgx.Tx, tenantID, organizationID uuid.UUID) (application.OrganizationRecord, error) {
	row, err := sqlcgen.New(tx).GetProviderOrganization(ctx, sqlcgen.GetProviderOrganizationParams{
		TenantID: tenantID, ID: organizationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.OrganizationRecord{}, application.ErrOrganizationNotFound
	}
	if err != nil {
		return application.OrganizationRecord{}, fmt.Errorf("provider: get organization: %w", err)
	}
	return application.OrganizationRecord{
		ID: row.ID, Role: row.RelationshipRole, Status: row.Status, DisplayName: row.DisplayName,
	}, nil
}

// CreateProvider implements application.Repository.
func (Repository) CreateProvider(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in application.NewProviderRow) (uuid.UUID, error) {
	id, err := sqlcgen.New(tx).CreateProviderProfile(ctx, sqlcgen.CreateProviderProfileParams{
		TenantID: tenantID, TenantOrganizationID: in.TenantOrganizationID, ProviderType: in.ProviderType,
		NetworkTier: in.NetworkTier, ContractedFrom: dateValue(in.ContractedFrom),
		ContractedTo: dateValue(in.ContractedTo), Notes: in.Notes,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch {
			case pgErr.Code == uniqueViolation && pgErr.ConstraintName == "uq_provider_profile_organization":
				return uuid.Nil, application.ErrProviderProfileExists
			case pgErr.Code == foreignKeyViolation:
				return uuid.Nil, application.ErrOrganizationNotFound
			}
		}
		return uuid.Nil, fmt.Errorf("provider: create profile: %w", err)
	}
	return id, nil
}

// GetProvider implements application.Repository.
func (Repository) GetProvider(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope application.Scope, id uuid.UUID) (application.ProviderRecord, error) {
	row, err := sqlcgen.New(tx).GetProviderProfile(ctx, sqlcgen.GetProviderProfileParams{
		TenantID: tenantID, ID: id, ScopeIds: scopeIDs(scope),
	})
	// A provider outside the caller's scope is filtered by the same predicate as a
	// provider of another tenant, so both answer "not found" and neither leaks.
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ProviderRecord{}, application.ErrProviderNotFound
	}
	if err != nil {
		return application.ProviderRecord{}, fmt.Errorf("provider: get profile: %w", err)
	}
	return application.ProviderRecord{
		ID: row.ID, TenantOrganizationID: row.TenantOrganizationID, OrganizationName: row.OrganizationName,
		ProviderType: row.ProviderType, Status: row.Status, NetworkTier: row.NetworkTier,
		ContractedFrom: datePtr(row.ContractedFrom), ContractedTo: datePtr(row.ContractedTo),
		Notes: row.Notes, CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// ListProviders implements application.Repository.
func (Repository) ListProviders(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope application.Scope, q application.ProviderQuery) ([]application.ProviderRecord, error) {
	params := sqlcgen.ListProviderProfilesParams{
		TenantID: tenantID, ScopeIds: scopeIDs(scope), PageSize: pageSize(q.PageSize),
		ProviderType: optionalString(q.ProviderType), Status: optionalString(q.Status),
		NetworkTier: optionalString(q.NetworkTier), Q: optionalString(q.Query),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListProviderProfiles(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("provider: list profiles: %w", err)
	}
	out := make([]application.ProviderRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.ProviderRecord{
			ID: r.ID, TenantOrganizationID: r.TenantOrganizationID, OrganizationName: r.OrganizationName,
			ProviderType: r.ProviderType, Status: r.Status, NetworkTier: r.NetworkTier,
			ContractedFrom: datePtr(r.ContractedFrom), ContractedTo: datePtr(r.ContractedTo),
			Notes: r.Notes, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// UpdateProvider implements application.Repository.
func (Repository) UpdateProvider(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope application.Scope, id uuid.UUID, in application.ProviderUpdateRow, expected int64) error {
	_, err := sqlcgen.New(tx).UpdateProviderProfile(ctx, sqlcgen.UpdateProviderProfileParams{
		TenantID: tenantID, ID: id, RowVersion: expected, ScopeIds: scopeIDs(scope),
		ProviderType: in.ProviderType, NetworkTier: in.NetworkTier,
		ContractedFrom: dateValue(in.ContractedFrom), ContractedTo: dateValue(in.ContractedTo),
		Notes: in.Notes,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		return fmt.Errorf("provider: update profile: %w", err)
	}
	return nil
}

// UpdateProviderStatus implements application.Repository.
func (Repository) UpdateProviderStatus(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope application.Scope, id uuid.UUID, status string, expected int64) error {
	_, err := sqlcgen.New(tx).UpdateProviderStatus(ctx, sqlcgen.UpdateProviderStatusParams{
		TenantID: tenantID, ID: id, RowVersion: expected, ScopeIds: scopeIDs(scope), Status: status,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		return fmt.Errorf("provider: update profile status: %w", err)
	}
	return nil
}

// CreateLocation implements application.Repository.
func (Repository) CreateLocation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in application.NewLocationRow) (uuid.UUID, error) {
	id, err := sqlcgen.New(tx).CreateProviderLocation(ctx, sqlcgen.CreateProviderLocationParams{
		TenantID: tenantID, ProviderProfileID: in.ProviderID, Code: in.Code, Name: in.Name,
		AddressLine: in.AddressLine, District: in.District, City: in.City,
		CountryCode: in.CountryCode, PostalCode: in.PostalCode,
		Latitude: in.Latitude, Longitude: in.Longitude, Timezone: in.Timezone, Phone: in.Phone,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch {
			case pgErr.Code == uniqueViolation && pgErr.ConstraintName == "uq_location_code":
				return uuid.Nil, application.ErrLocationCodeTaken
			case pgErr.Code == foreignKeyViolation:
				return uuid.Nil, application.ErrProviderNotFound
			}
		}
		return uuid.Nil, fmt.Errorf("provider: create location: %w", err)
	}
	return id, nil
}

// GetLocation implements application.Repository.
func (Repository) GetLocation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope application.Scope, id uuid.UUID) (application.LocationRecord, error) {
	row, err := sqlcgen.New(tx).GetProviderLocation(ctx, sqlcgen.GetProviderLocationParams{
		TenantID: tenantID, ID: id, ScopeIds: scopeIDs(scope),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.LocationRecord{}, application.ErrLocationNotFound
	}
	if err != nil {
		return application.LocationRecord{}, fmt.Errorf("provider: get location: %w", err)
	}
	return locationRecord(row.ID, row.ProviderProfileID, row.Code, row.Name, row.AddressLine, row.District,
		row.City, row.CountryCode, row.PostalCode, row.Latitude, row.Longitude, row.Timezone,
		row.Phone, row.Status, row.CreatedAt, row.RowVersion), nil
}

// ListLocations implements application.Repository.
func (Repository) ListLocations(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope application.Scope, q application.LocationQuery) ([]application.LocationRecord, error) {
	params := sqlcgen.ListProviderLocationsParams{
		TenantID: tenantID, ProviderProfileID: q.ProviderID, ScopeIds: scopeIDs(scope),
		Status: optionalString(q.Status), City: optionalString(q.City), Q: optionalString(q.Query),
		PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListProviderLocations(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("provider: list locations: %w", err)
	}
	out := make([]application.LocationRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, locationRecord(r.ID, r.ProviderProfileID, r.Code, r.Name, r.AddressLine, r.District,
			r.City, r.CountryCode, r.PostalCode, r.Latitude, r.Longitude, r.Timezone,
			r.Phone, r.Status, r.CreatedAt, r.RowVersion))
	}
	return out, nil
}

// UpdateLocation implements application.Repository.
func (Repository) UpdateLocation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope application.Scope, id uuid.UUID, in application.LocationUpdateRow, expected int64) error {
	_, err := sqlcgen.New(tx).UpdateProviderLocation(ctx, sqlcgen.UpdateProviderLocationParams{
		TenantID: tenantID, ID: id, RowVersion: expected, ScopeIds: scopeIDs(scope),
		Name: in.Name, AddressLine: in.AddressLine, District: in.District, City: in.City,
		CountryCode: in.CountryCode, PostalCode: in.PostalCode, Latitude: in.Latitude,
		Longitude: in.Longitude, Timezone: in.Timezone, Phone: in.Phone, Status: in.Status,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		return fmt.Errorf("provider: update location: %w", err)
	}
	return nil
}

// TouchLocation implements application.Repository.
func (Repository) TouchLocation(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, expected int64) error {
	n, err := sqlcgen.New(tx).TouchProviderLocation(ctx, sqlcgen.TouchProviderLocationParams{
		TenantID: tenantID, ID: id, RowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("provider: touch location: %w", err)
	}
	if n == 0 {
		return application.ErrVersionMismatch
	}
	return nil
}

// ListCapabilities implements application.Repository.
func (Repository) ListCapabilities(ctx context.Context, tx pgx.Tx, tenantID, locationID uuid.UUID) ([]application.CapabilityRecord, error) {
	rows, err := sqlcgen.New(tx).ListProviderCapabilities(ctx, sqlcgen.ListProviderCapabilitiesParams{
		TenantID: tenantID, LocationID: locationID,
	})
	if err != nil {
		return nil, fmt.Errorf("provider: list capabilities: %w", err)
	}
	out := make([]application.CapabilityRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.CapabilityRecord{
			ID: r.ID, LocationID: r.LocationID,
			ServiceDefinitionID: uuidPtr(r.ServiceDefinitionID), ServiceDefinitionCode: r.ServiceDefinitionCode,
			ServiceCategoryID: uuidPtr(r.ServiceCategoryID), ServiceCategoryCode: r.ServiceCategoryCode,
			ValidFrom: dateTime(r.ValidFrom), ValidTo: datePtr(r.ValidTo), Notes: r.Notes,
		})
	}
	return out, nil
}

// ReplaceCapabilities implements application.Repository. The exclusion constraints of
// migration 000020 are the authority on overlap, so the conflict is mapped from the
// PostgreSQL error rather than pre-checked against the stored rows.
func (Repository) ReplaceCapabilities(ctx context.Context, tx pgx.Tx, tenantID, locationID uuid.UUID, rows []application.CapabilityRow) error {
	q := sqlcgen.New(tx)
	if _, err := q.DeleteProviderCapabilities(ctx, sqlcgen.DeleteProviderCapabilitiesParams{
		TenantID: tenantID, LocationID: locationID,
	}); err != nil {
		return fmt.Errorf("provider: clear capabilities: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	params := make([]sqlcgen.CreateProviderCapabilityParams, 0, len(rows))
	for _, r := range rows {
		params = append(params, sqlcgen.CreateProviderCapabilityParams{
			TenantID: tenantID, LocationID: locationID,
			ServiceDefinitionID: nullUUID(r.ServiceDefinitionID), ServiceCategoryID: nullUUID(r.ServiceCategoryID),
			ValidFrom: dateValue(&r.ValidFrom), ValidTo: dateValue(r.ValidTo), Notes: r.Notes,
		})
	}
	firstErr := execBatch(q.CreateProviderCapability(ctx, params))
	if firstErr != nil {
		var pgErr *pgconn.PgError
		if errors.As(firstErr, &pgErr) {
			switch pgErr.Code {
			case exclusionViolation:
				return domain.ErrCapabilityOverlap
			case foreignKeyViolation:
				return application.ErrCapabilityTargetNotFound
			}
		}
		return fmt.Errorf("provider: replace capabilities: %w", firstErr)
	}
	return nil
}

// CreatePractitioner implements application.Repository.
func (Repository) CreatePractitioner(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in application.NewPractitionerRow) (uuid.UUID, error) {
	id, err := sqlcgen.New(tx).CreatePractitioner(ctx, sqlcgen.CreatePractitionerParams{
		TenantID: tenantID, ProviderProfileID: in.ProviderID, PersonID: nullUUID(in.PersonID),
		FullName: in.FullName, Title: in.Title, BranchCode: in.BranchCode,
		RegistrationAuthority: in.RegistrationAuthority, RegistrationNumberCipher: in.Cipher,
		RegistrationNumberHash: in.Hash, RegistrationNumberMasked: in.Masked,
		ValidFrom: dateValue(in.ValidFrom), ValidTo: dateValue(in.ValidTo),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch {
			case pgErr.Code == uniqueViolation && pgErr.ConstraintName == "uq_practitioner_registration":
				return uuid.Nil, application.ErrRegistrationTaken
			case pgErr.Code == foreignKeyViolation && pgErr.ConstraintName == "fk_practitioner_person":
				return uuid.Nil, application.ErrPersonNotFound
			case pgErr.Code == foreignKeyViolation:
				return uuid.Nil, application.ErrProviderNotFound
			}
		}
		return uuid.Nil, fmt.Errorf("provider: create practitioner: %w", err)
	}
	return id, nil
}

// GetPractitioner implements application.Repository.
func (Repository) GetPractitioner(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope application.Scope, id uuid.UUID) (application.PractitionerRecord, error) {
	row, err := sqlcgen.New(tx).GetPractitioner(ctx, sqlcgen.GetPractitionerParams{
		TenantID: tenantID, ID: id, ScopeIds: scopeIDs(scope),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PractitionerRecord{}, application.ErrPractitionerNotFound
	}
	if err != nil {
		return application.PractitionerRecord{}, fmt.Errorf("provider: get practitioner: %w", err)
	}
	return practitionerRecord(row.ID, row.ProviderProfileID, row.PersonID, row.FullName, row.Title,
		row.BranchCode, row.RegistrationAuthority, row.RegistrationNumberMasked, row.ValidFrom,
		row.ValidTo, row.Status, row.CreatedAt, row.RowVersion), nil
}

// ListPractitioners implements application.Repository.
func (Repository) ListPractitioners(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope application.Scope, q application.PractitionerQuery) ([]application.PractitionerRecord, error) {
	params := sqlcgen.ListPractitionersParams{
		TenantID: tenantID, ProviderProfileID: q.ProviderID, ScopeIds: scopeIDs(scope),
		Status: optionalString(q.Status), BranchCode: optionalString(q.BranchCode),
		Q: optionalString(q.Query), PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListPractitioners(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("provider: list practitioners: %w", err)
	}
	out := make([]application.PractitionerRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, practitionerRecord(r.ID, r.ProviderProfileID, r.PersonID, r.FullName, r.Title,
			r.BranchCode, r.RegistrationAuthority, r.RegistrationNumberMasked, r.ValidFrom,
			r.ValidTo, r.Status, r.CreatedAt, r.RowVersion))
	}
	return out, nil
}

// UpdatePractitioner implements application.Repository.
func (Repository) UpdatePractitioner(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope application.Scope, id uuid.UUID, in application.PractitionerUpdateRow, expected int64) error {
	_, err := sqlcgen.New(tx).UpdatePractitioner(ctx, sqlcgen.UpdatePractitionerParams{
		TenantID: tenantID, ID: id, RowVersion: expected, ScopeIds: scopeIDs(scope),
		PersonID: nullUUID(in.PersonID), FullName: in.FullName, Title: in.Title,
		BranchCode: in.BranchCode, ValidFrom: dateValue(in.ValidFrom), ValidTo: dateValue(in.ValidTo),
		Status: in.Status,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
			return application.ErrPersonNotFound
		}
		return fmt.Errorf("provider: update practitioner: %w", err)
	}
	return nil
}

// TouchPractitioner implements application.Repository.
func (Repository) TouchPractitioner(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, expected int64) error {
	n, err := sqlcgen.New(tx).TouchPractitioner(ctx, sqlcgen.TouchPractitionerParams{
		TenantID: tenantID, ID: id, RowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("provider: touch practitioner: %w", err)
	}
	if n == 0 {
		return application.ErrVersionMismatch
	}
	return nil
}

// FindPractitionerByRegistration implements application.Repository.
func (Repository) FindPractitionerByRegistration(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope application.Scope, authority string, hash []byte) (uuid.UUID, bool, error) {
	id, err := sqlcgen.New(tx).FindPractitionerByRegistrationHash(ctx, sqlcgen.FindPractitionerByRegistrationHashParams{
		TenantID: tenantID, RegistrationAuthority: authority, RegistrationNumberHash: hash,
		ScopeIds: scopeIDs(scope),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("provider: find practitioner by registration: %w", err)
	}
	return id, true, nil
}

// ListAssignments implements application.Repository.
func (Repository) ListAssignments(ctx context.Context, tx pgx.Tx, tenantID, practitionerID uuid.UUID) ([]application.AssignmentRecord, error) {
	rows, err := sqlcgen.New(tx).ListPractitionerLocations(ctx, sqlcgen.ListPractitionerLocationsParams{
		TenantID: tenantID, PractitionerID: practitionerID,
	})
	if err != nil {
		return nil, fmt.Errorf("provider: list practitioner locations: %w", err)
	}
	out := make([]application.AssignmentRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.AssignmentRecord{
			ID: r.ID, PractitionerID: r.PractitionerID, LocationID: r.LocationID,
			LocationCode: r.LocationCode, LocationName: r.LocationName, Role: r.Role,
			ValidFrom: dateTime(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
		})
	}
	return out, nil
}

// ReplaceAssignments implements application.Repository.
func (Repository) ReplaceAssignments(ctx context.Context, tx pgx.Tx, tenantID, practitionerID uuid.UUID, rows []application.AssignmentRow) error {
	q := sqlcgen.New(tx)
	if _, err := q.DeletePractitionerLocations(ctx, sqlcgen.DeletePractitionerLocationsParams{
		TenantID: tenantID, PractitionerID: practitionerID,
	}); err != nil {
		return fmt.Errorf("provider: clear practitioner locations: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	params := make([]sqlcgen.CreatePractitionerLocationParams, 0, len(rows))
	for _, r := range rows {
		params = append(params, sqlcgen.CreatePractitionerLocationParams{
			TenantID: tenantID, PractitionerID: practitionerID, LocationID: r.LocationID,
			Role: r.Role, ValidFrom: dateValue(&r.ValidFrom), ValidTo: dateValue(r.ValidTo),
		})
	}
	firstErr := execBatch(q.CreatePractitionerLocation(ctx, params))
	if firstErr != nil {
		var pgErr *pgconn.PgError
		if errors.As(firstErr, &pgErr) {
			switch pgErr.Code {
			case exclusionViolation:
				return domain.ErrAssignmentOverlap
			case foreignKeyViolation:
				return application.ErrLocationNotFound
			}
		}
		return fmt.Errorf("provider: replace practitioner locations: %w", firstErr)
	}
	return nil
}

// CountLocationsOutsideProvider implements application.Repository.
func (Repository) CountLocationsOutsideProvider(ctx context.Context, tx pgx.Tx, tenantID, providerID uuid.UUID, locationIDs []uuid.UUID) (int, error) {
	n, err := sqlcgen.New(tx).CountProviderLocationsOutsideProvider(ctx, sqlcgen.CountProviderLocationsOutsideProviderParams{
		TenantID: tenantID, ProviderProfileID: providerID, LocationIds: locationIDs,
	})
	if err != nil {
		return 0, fmt.Errorf("provider: count foreign locations: %w", err)
	}
	return int(n), nil
}

// CategoryChain implements application.Repository.
func (Repository) CategoryChain(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := sqlcgen.New(tx).ServiceDefinitionCategoryChain(ctx, sqlcgen.ServiceDefinitionCategoryChainParams{
		TenantID: tenantID, ServiceDefinitionID: definitionID,
	})
	if err != nil {
		return nil, fmt.Errorf("provider: category chain: %w", err)
	}
	out := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out, nil
}

// SearchLocations implements application.Repository.
func (Repository) SearchLocations(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope application.Scope, q application.SearchQuery) ([]application.SearchHit, error) {
	params := sqlcgen.SearchProviderLocationsParams{
		TenantID: tenantID, ScopeIds: scopeIDs(scope), ServiceDefinitionID: q.ServiceDefinitionID,
		CategoryIds: q.CategoryIDs, AsOf: dateValue(&q.AsOf), City: optionalString(q.City),
		Q: optionalString(q.Query), PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).SearchProviderLocations(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("provider: search locations: %w", err)
	}
	out := make([]application.SearchHit, 0, len(rows))
	for _, r := range rows {
		matched := "CATEGORY"
		if r.MatchedDefinition {
			matched = "DEFINITION"
		}
		out = append(out, application.SearchHit{
			ProviderID: r.ProviderID, OrganizationName: r.OrganizationName, ProviderType: r.ProviderType,
			NetworkTier: r.NetworkTier, LocationID: r.LocationID, LocationCode: r.LocationCode,
			LocationName: r.LocationName, City: r.City, District: r.District,
			Latitude: numericPtr(r.Latitude), Longitude: numericPtr(r.Longitude),
			MatchedVia: matched, CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// PersonExists implements application.Repository.
func (Repository) PersonExists(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) (bool, error) {
	present, err := sqlcgen.New(tx).ProviderPersonExists(ctx, sqlcgen.ProviderPersonExistsParams{
		TenantID: tenantID, ID: personID,
	})
	if err != nil {
		return false, fmt.Errorf("provider: person exists: %w", err)
	}
	return present, nil
}

// batchExecutor is the shape both pipelined inserts share.
type batchExecutor interface {
	Exec(func(int, error))
	Close() error
}

// execBatch runs a pipelined insert inside the caller's transaction and returns the first
// error; any failure aborts the whole transaction, so a replacement is all-or-nothing.
func execBatch(batch batchExecutor) error {
	var firstErr error
	batch.Exec(func(_ int, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	if err := batch.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func locationRecord(id, providerID uuid.UUID, code, name string, addressLine, district, city *string,
	country string, postalCode *string, lat, lng pgtype.Numeric, timezone string, phone *string,
	status string, createdAt time.Time, rowVersion int64,
) application.LocationRecord {
	return application.LocationRecord{
		ID: id, ProviderID: providerID, Code: code, Name: name, AddressLine: addressLine,
		District: district, City: city, CountryCode: country, PostalCode: postalCode,
		Latitude: numericPtr(lat), Longitude: numericPtr(lng), Timezone: timezone, Phone: phone,
		Status: status, CreatedAt: createdAt, RowVersion: rowVersion,
	}
}

func practitionerRecord(id, providerID uuid.UUID, personID uuid.NullUUID, fullName string,
	title, branchCode *string, authority, masked string, validFrom, validTo pgtype.Date,
	status string, createdAt time.Time, rowVersion int64,
) application.PractitionerRecord {
	return application.PractitionerRecord{
		ID: id, ProviderID: providerID, PersonID: uuidPtr(personID), FullName: fullName,
		Title: title, BranchCode: branchCode, RegistrationAuthority: authority,
		MaskedRegistration: masked, ValidFrom: datePtr(validFrom), ValidTo: datePtr(validTo),
		Status: status, CreatedAt: createdAt, RowVersion: rowVersion,
	}
}

// scopeIDs hands the provider boundary to SQL. A nil slice means "no restriction", which is
// what the queries test with `scope_ids IS NULL`; an empty non-nil slice restricts the
// caller to nothing, which is the safe reading of a grant that names no organization.
func scopeIDs(scope application.Scope) []uuid.UUID { return scope.OrganizationIDs }

// pageSize keeps the int32 conversion in one place; the caller has already clamped it to at
// most httpx.MaxPageSize+1.
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

// numericPtr converts a numeric(9,6) coordinate to the float the contract carries. The
// column holds at most nine digits, so the conversion is exact within float64.
func numericPtr(n pgtype.Numeric) *float64 {
	if !n.Valid || n.NaN || n.Int == nil {
		return nil
	}
	value := new(big.Float).SetInt(n.Int)
	if n.Exp != 0 {
		value.Mul(value, big.NewFloat(pow10(n.Exp)))
	}
	f, _ := value.Float64()
	return &f
}

// pow10 returns 10^exp for the small exponents a numeric(9,6) can carry.
func pow10(exp int32) float64 {
	result := 1.0
	step := 10.0
	if exp < 0 {
		exp = -exp
		step = 0.1
	}
	for range exp {
		result *= step
	}
	return result
}
