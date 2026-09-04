// Package application implements the provider network use cases: the provider profile and
// its status commands, the locations a provider works from, what those locations can
// deliver, the practitioners registered there and the search the rest of the system asks
// "who can deliver this, here, on this date". Transactions are opened here with
// db.WithTenantTx, so a write and its audit row commit together and RLS is bound for every
// statement.
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
	ErrProviderNotFound         = errors.New("provider: provider profile not found")
	ErrLocationNotFound         = errors.New("provider: location not found")
	ErrPractitionerNotFound     = errors.New("provider: practitioner not found")
	ErrOrganizationNotFound     = errors.New("provider: tenant organization not found")
	ErrProviderProfileExists    = errors.New("provider: organization already has a provider profile")
	ErrLocationCodeTaken        = errors.New("provider: location code already used by this provider")
	ErrRegistrationTaken        = errors.New("provider: registration number already registered with this authority")
	ErrVersionMismatch          = errors.New("provider: row version does not match If-Match")
	ErrCapabilityTargetNotFound = errors.New("provider: capability target not found in the catalog")
	ErrPersonNotFound           = errors.New("provider: person not found")
)

// Scope is the provider boundary of one caller. A nil OrganizationIDs means a tenant-wide
// actor; a non-nil one, empty included, restricts every statement to the organizations the
// actor's role grants name. It travels into the repository rather than being applied by a
// handler so no route can forget it.
type Scope struct {
	OrganizationIDs []uuid.UUID
}

// Restricted reports whether the caller is bound to a set of organizations.
func (s Scope) Restricted() bool { return s.OrganizationIDs != nil }

// ProviderRecord is one provider.provider_profile row joined with its organization.
type ProviderRecord struct {
	ID                   uuid.UUID
	TenantOrganizationID uuid.UUID
	OrganizationName     string
	ProviderType         string
	Status               string
	NetworkTier          *string
	ContractedFrom       *time.Time
	ContractedTo         *time.Time
	Notes                *string
	CreatedAt            time.Time
	RowVersion           int64
}

// NewProviderRow is the insert payload for a provider profile.
type NewProviderRow struct {
	TenantOrganizationID uuid.UUID
	ProviderType         string
	NetworkTier          *string
	ContractedFrom       *time.Time
	ContractedTo         *time.Time
	Notes                *string
}

// ProviderUpdateRow is the update payload for a provider profile; the status is absent
// because only the status commands write it.
type ProviderUpdateRow struct {
	ProviderType   string
	NetworkTier    *string
	ContractedFrom *time.Time
	ContractedTo   *time.Time
	Notes          *string
}

// OrganizationRecord is the relationship a provider profile hangs off.
type OrganizationRecord struct {
	ID          uuid.UUID
	Role        string
	Status      string
	DisplayName string
}

// LocationRecord is one provider.location row as stored.
type LocationRecord struct {
	ID          uuid.UUID
	ProviderID  uuid.UUID
	Code        string
	Name        string
	AddressLine *string
	District    *string
	City        *string
	CountryCode string
	PostalCode  *string
	Latitude    *float64
	Longitude   *float64
	Timezone    string
	Phone       *string
	Status      string
	CreatedAt   time.Time
	RowVersion  int64
}

// NewLocationRow is the insert payload for a location.
type NewLocationRow struct {
	ProviderID  uuid.UUID
	Code        string
	Name        string
	AddressLine *string
	District    *string
	City        *string
	CountryCode string
	PostalCode  *string
	Latitude    *float64
	Longitude   *float64
	Timezone    string
	Phone       *string
}

// LocationUpdateRow is the update payload for a location; Code is absent because it is
// immutable.
type LocationUpdateRow struct {
	Name        string
	AddressLine *string
	District    *string
	City        *string
	CountryCode string
	PostalCode  *string
	Latitude    *float64
	Longitude   *float64
	Timezone    string
	Phone       *string
	Status      string
}

// CapabilityRecord is one provider.capability row joined with the catalog row it names.
type CapabilityRecord struct {
	ID                    uuid.UUID
	LocationID            uuid.UUID
	ServiceDefinitionID   *uuid.UUID
	ServiceDefinitionCode *string
	ServiceCategoryID     *uuid.UUID
	ServiceCategoryCode   *string
	ValidFrom             time.Time
	ValidTo               *time.Time
	Notes                 *string
}

// CapabilityRow is one row of a capability replacement as it reaches the repository.
type CapabilityRow struct {
	ServiceDefinitionID *uuid.UUID
	ServiceCategoryID   *uuid.UUID
	ValidFrom           time.Time
	ValidTo             *time.Time
	Notes               *string
}

// PractitionerRecord is one provider.practitioner row. The registration number appears
// only as its masked form: the envelope is never read back, because nothing in the API
// needs the plaintext.
type PractitionerRecord struct {
	ID                    uuid.UUID
	ProviderID            uuid.UUID
	PersonID              *uuid.UUID
	FullName              string
	Title                 *string
	BranchCode            *string
	RegistrationAuthority string
	MaskedRegistration    string
	ValidFrom             *time.Time
	ValidTo               *time.Time
	Status                string
	CreatedAt             time.Time
	RowVersion            int64
}

// NewPractitionerRow is the insert payload for a practitioner. Cipher and Hash are already
// derived by the service; the plaintext never reaches this layer.
type NewPractitionerRow struct {
	ProviderID            uuid.UUID
	PersonID              *uuid.UUID
	FullName              string
	Title                 *string
	BranchCode            *string
	RegistrationAuthority string
	Cipher                []byte
	Hash                  []byte
	Masked                string
	ValidFrom             *time.Time
	ValidTo               *time.Time
}

// PractitionerUpdateRow is the update payload; the registration columns are absent because
// a registration is ended and re-registered rather than rewritten.
type PractitionerUpdateRow struct {
	PersonID   *uuid.UUID
	FullName   string
	Title      *string
	BranchCode *string
	ValidFrom  *time.Time
	ValidTo    *time.Time
	Status     string
}

// AssignmentRecord is one provider.practitioner_location row joined with its location.
type AssignmentRecord struct {
	ID             uuid.UUID
	PractitionerID uuid.UUID
	LocationID     uuid.UUID
	LocationCode   string
	LocationName   string
	Role           string
	ValidFrom      time.Time
	ValidTo        *time.Time
}

// AssignmentRow is one row of an assignment replacement as it reaches the repository.
type AssignmentRow struct {
	LocationID uuid.UUID
	Role       string
	ValidFrom  time.Time
	ValidTo    *time.Time
}

// SearchHit is one location the search found, with the provider it belongs to.
type SearchHit struct {
	ProviderID       uuid.UUID
	OrganizationName string
	ProviderType     string
	NetworkTier      *string
	LocationID       uuid.UUID
	LocationCode     string
	LocationName     string
	City             *string
	District         *string
	Latitude         *float64
	Longitude        *float64
	// MatchedVia is DEFINITION when the location names the service definition itself and
	// CATEGORY when the coverage comes from a category above it in the tree.
	MatchedVia string
	CreatedAt  time.Time
}

// ProviderQuery is the repository-level provider filter.
type ProviderQuery struct {
	ProviderType string
	Status       string
	NetworkTier  string
	Query        string
	After        *httpx.Cursor
	PageSize     int
}

// LocationQuery is the repository-level location filter.
type LocationQuery struct {
	ProviderID uuid.UUID
	Status     string
	City       string
	Query      string
	After      *httpx.Cursor
	PageSize   int
}

// PractitionerQuery is the repository-level practitioner filter.
type PractitionerQuery struct {
	ProviderID uuid.UUID
	Status     string
	BranchCode string
	Query      string
	After      *httpx.Cursor
	PageSize   int
}

// SearchQuery is the resolved eligibility search: the definition itself plus every
// category on the chain above it, as of one day.
type SearchQuery struct {
	ServiceDefinitionID uuid.UUID
	CategoryIDs         []uuid.UUID
	City                string
	Query               string
	AsOf                time.Time
	After               *httpx.Cursor
	PageSize            int
}

// Repository is the persistence port; every method runs inside the caller's transaction,
// which db.WithTenantTx has already bound to the tenant so RLS is active. Scope is a
// parameter of every method rather than a field of the repository, so a new query cannot
// be added without deciding what a provider-scoped actor sees through it.
type Repository interface {
	GetOrganization(ctx context.Context, tx pgx.Tx, tenantID, organizationID uuid.UUID) (OrganizationRecord, error)

	CreateProvider(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewProviderRow) (uuid.UUID, error)
	GetProvider(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, id uuid.UUID) (ProviderRecord, error)
	ListProviders(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, q ProviderQuery) ([]ProviderRecord, error)
	UpdateProvider(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, id uuid.UUID, in ProviderUpdateRow, expected int64) error
	// UpdateProviderStatus is the only writer of provider_profile.status; the domain has
	// already decided the move is legal.
	UpdateProviderStatus(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, id uuid.UUID, status string, expected int64) error

	CreateLocation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewLocationRow) (uuid.UUID, error)
	GetLocation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, id uuid.UUID) (LocationRecord, error)
	ListLocations(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, q LocationQuery) ([]LocationRecord, error)
	UpdateLocation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, id uuid.UUID, in LocationUpdateRow, expected int64) error
	// TouchLocation bumps row_version without changing a business field, so replacing the
	// capability set invalidates the ETag the caller holds.
	TouchLocation(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, expected int64) error

	ListCapabilities(ctx context.Context, tx pgx.Tx, tenantID, locationID uuid.UUID) ([]CapabilityRecord, error)
	// ReplaceCapabilities deletes the current set and inserts rows; the exclusion
	// constraints of migration 000020 surface as domain.ErrCapabilityOverlap.
	ReplaceCapabilities(ctx context.Context, tx pgx.Tx, tenantID, locationID uuid.UUID, rows []CapabilityRow) error

	CreatePractitioner(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewPractitionerRow) (uuid.UUID, error)
	GetPractitioner(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, id uuid.UUID) (PractitionerRecord, error)
	ListPractitioners(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, q PractitionerQuery) ([]PractitionerRecord, error)
	UpdatePractitioner(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, id uuid.UUID, in PractitionerUpdateRow, expected int64) error
	TouchPractitioner(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, expected int64) error
	// FindPractitionerByRegistration answers the blind-index search; found is false when
	// no practitioner in the caller's scope carries the number.
	FindPractitionerByRegistration(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, authority string, hash []byte) (id uuid.UUID, found bool, err error)

	ListAssignments(ctx context.Context, tx pgx.Tx, tenantID, practitionerID uuid.UUID) ([]AssignmentRecord, error)
	ReplaceAssignments(ctx context.Context, tx pgx.Tx, tenantID, practitionerID uuid.UUID, rows []AssignmentRow) error
	// CountLocationsOutsideProvider counts submitted locations that do not belong to the
	// practitioner's own provider, so the answer never reveals which one is foreign.
	CountLocationsOutsideProvider(ctx context.Context, tx pgx.Tx, tenantID, providerID uuid.UUID, locationIDs []uuid.UUID) (int, error)

	// CategoryChain returns the catalog categories from a service definition's own
	// category up to its root; an empty result means the definition does not exist.
	CategoryChain(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]uuid.UUID, error)
	SearchLocations(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, q SearchQuery) ([]SearchHit, error)
	// PersonExists reports whether a party.person row of this tenant exists, so linking a
	// practitioner to a member fails as a field error rather than a foreign key error.
	PersonExists(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) (bool, error)
}
