// Package application is the accommodation vertical's service layer: the properties and
// room types a provider maintains, the daily allotment it opens, and the availability
// search that answers a member with what is free and what it would cost them.
//
// Two rules run through everything here and neither is stated twice.
//
// The daily inventory row is the only place a room is counted, and
// `held + confirmed <= capacity` is a CHECK on that row. This layer never re-implements
// the CHECK; it locks the range, finds the first night a new capacity would break and
// refuses with that date, and if the lock were removed tomorrow the database would still
// refuse — which is why the constraint is where it is.
//
// The member's share is the pricing ladder's answer, computed once. The per-night prices
// go through internal/contract/selection and internal/pricing exactly as a counter quote
// does, and the totals are the ones internal/pricing produced from the rounded lines. This
// package adds no arithmetic of its own to a money figure, and there is no second place a
// total could be summed differently.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/contract/selection"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions. They live here rather than in the transport because whether a provider
// clerk may open somebody else's allotment is a business rule, not a routing detail.
const (
	// PermissionRead is the grant of migration 000040: the properties, the room types,
	// the allotment and the availability search.
	PermissionRead = "accommodation.property.read"
	// PermissionManage is the grant of migration 000008 a provider writes with.
	PermissionManage = "accommodation.inventory.manage"
)

// ScopeOrganization is the iam.access_grant.scope_type of provider-side roles, the same
// constant the eligibility check and the pricing service compare against.
const ScopeOrganization = "ORGANIZATION"

// Errors mapped by the transport layer to problem+json codes.
var (
	// ErrPropertyNotFound is a property this caller cannot see. An unknown id, a foreign
	// tenant's id and another provider's property are deliberately indistinguishable:
	// that a hotel exists at all is not this caller's business.
	ErrPropertyNotFound = errors.New("accommodation: property not found")
	// ErrRoomTypeNotFound is the same refusal one level down, reached through the
	// property the room type belongs to.
	ErrRoomTypeNotFound = errors.New("accommodation: room type not found")
	// ErrVersionMismatch is a write whose If-Match no longer matches the row.
	ErrVersionMismatch = errors.New("accommodation: row version mismatch")
	// ErrInventoryBelowCommitment is a capacity lowered under what is already held or
	// confirmed. It carries the first offending date, because a provider opening a season
	// of ninety nights needs to know which night to look at.
	ErrInventoryBelowCommitment = errors.New("accommodation: capacity is below what is already committed")
	// ErrPropertyScope refuses a provider-scoped actor writing outside its own
	// organization on a create, where there is no existing row to hide behind a 404.
	ErrPropertyScope = errors.New("accommodation: provider-scoped actor may only act for its own organization")
	// ErrPropertyCodeTaken is a second building under one provider's own code. It is a 409
	// rather than a field error because the code is the provider's own name for the
	// building and the caller has to choose another, not correct a malformed one.
	ErrPropertyCodeTaken = errors.New("accommodation: this provider already has a property with that code")
	// ErrRoomTypeCodeTaken is the same refusal one level down, inside one property.
	ErrRoomTypeCodeTaken = errors.New("accommodation: this property already has a room type with that code")
	// ErrPersonRequired is a search by a caller that is not bound to a person and named
	// none. It is not a permission problem and not a validation formality: the search
	// answers what one member may have, and without a member there is no answer.
	ErrPersonRequired = errors.New("accommodation: the search needs a person")
)

// InventoryBelowCommitment is the detail behind ErrInventoryBelowCommitment: the first
// night the requested capacity would fall under, and what stands on it.
type InventoryBelowCommitment struct {
	StayDate  time.Time
	Capacity  int
	Held      int
	Confirmed int
}

// Error implements error so the refusal can be returned directly and still be matched with
// errors.Is(err, ErrInventoryBelowCommitment).
func (e *InventoryBelowCommitment) Error() string { return ErrInventoryBelowCommitment.Error() }

// Is lets errors.Is reach the sentinel.
func (e *InventoryBelowCommitment) Is(target error) bool {
	return target == ErrInventoryBelowCommitment
}

// PropertyRecord is one accommodation.property row as this layer reads it.
type PropertyRecord struct {
	ID                     uuid.UUID
	ProviderOrganizationID uuid.UUID
	LocationID             *uuid.UUID
	Code                   string
	Name                   string
	PropertyType           string
	Timezone               string
	City                   *string
	RegionCode             *string
	Amenities              []string
	CostCenter             *string
	Status                 string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	RowVersion             int64
}

// NewPropertyRow is a property as it is written.
type NewPropertyRow struct {
	ProviderOrganizationID uuid.UUID
	LocationID             *uuid.UUID
	Code                   string
	Name                   string
	PropertyType           string
	Timezone               string
	City                   *string
	RegionCode             *string
	Amenities              []string
	CostCenter             *string
	Status                 string
	ActorID                uuid.UUID
}

// PropertyUpdateRow is every editable field of a property. There is no partial merge: the
// contract sends them all, so "this hotel no longer has a location" is expressible.
type PropertyUpdateRow struct {
	LocationID   *uuid.UUID
	Name         string
	PropertyType string
	Timezone     string
	City         *string
	RegionCode   *string
	Amenities    []string
	CostCenter   *string
	Status       string
	ActorID      uuid.UUID
}

// PropertyFilter is the list query.
type PropertyFilter struct {
	ProviderOrganizationID *uuid.UUID
	Status                 string
	PropertyType           string
	RegionCode             string
	City                   string
	Cursor                 string
	Limit                  int
}

// RoomTypeRecord is one accommodation.room_type row.
type RoomTypeRecord struct {
	ID                  uuid.UUID
	PropertyID          uuid.UUID
	Code                string
	Name                string
	MaxAdults           int
	MaxChildren         int
	MaxOccupancy        int
	Attributes          []byte
	ServiceDefinitionID uuid.UUID
	Status              string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	RowVersion          int64
}

// RoomTypeContext is a room type together with the building it belongs to, which is where
// its provider and its clock come from.
type RoomTypeContext struct {
	RoomType               RoomTypeRecord
	ProviderOrganizationID uuid.UUID
	PropertyTimezone       string
	PropertyStatus         string
}

// NewRoomTypeRow is a room type as it is written.
type NewRoomTypeRow struct {
	PropertyID          uuid.UUID
	Code                string
	Name                string
	MaxAdults           int
	MaxChildren         int
	MaxOccupancy        int
	Attributes          []byte
	ServiceDefinitionID uuid.UUID
	Status              string
	ActorID             uuid.UUID
}

// RoomTypeUpdateRow is every editable field of a room type. `serviceDefinitionId` is not
// among them and never will be: a room type re-pointed at another service would silently
// change what every booking already taken against it was priced and entitled as.
type RoomTypeUpdateRow struct {
	Name         string
	MaxAdults    int
	MaxChildren  int
	MaxOccupancy int
	Attributes   []byte
	Status       string
	ActorID      uuid.UUID
}

// InventoryDayRecord is one night's allotment with the availability the database computed.
type InventoryDayRecord struct {
	StayDate   time.Time
	Capacity   int
	Held       int
	Confirmed  int
	Available  int
	UpdatedAt  time.Time
	RowVersion int64
}

// InventoryCommitment is what is already promised on one night, read under a row lock.
type InventoryCommitment struct {
	StayDate  time.Time
	Capacity  int
	Held      int
	Confirmed int
}

// AvailabilityProperty is a property the search may answer with, with the provider profile
// its prices hang off.
type AvailabilityProperty struct {
	Property          PropertyRecord
	ProviderProfileID uuid.UUID
}

// AvailabilityRoomType is a candidate room type of one of those properties.
type AvailabilityRoomType struct {
	ID                  uuid.UUID
	PropertyID          uuid.UUID
	Code                string
	Name                string
	MaxAdults           int
	MaxChildren         int
	MaxOccupancy        int
	Attributes          []byte
	ServiceDefinitionID uuid.UUID
	Status              string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	RowVersion          int64
}

// InventorySummary is what the search needs to know about one room type over the stay:
// the fewest rooms free on any night that has an allotment, and how many nights have one
// at all. The second is not decoration — a room type with an allotment on every night but
// one has a healthy minimum and is not available, and only the count says so.
type InventorySummary struct {
	MinAvailable int
	NightCount   int
}

// AvailabilityPropertyQuery is the properties half of a search.
type AvailabilityPropertyQuery struct {
	PropertyID *uuid.UUID
	RegionCode string
	// ScopeIDs binds a provider-scoped caller to its own organizations; nil is a caller
	// that sees the whole tenant.
	ScopeIDs []uuid.UUID
	// PayerOrganizationIDs are the payers behind the person's programs. nil is a search
	// that is not narrowed to one member's programs.
	PayerOrganizationIDs []uuid.UUID
	CheckIn              time.Time
	LastNight            time.Time
	Limit                int
}

// PriceCandidateQuery loads every contracted price that could bear on the search, once.
type PriceCandidateQuery struct {
	ProviderProfileIDs   []uuid.UUID
	ServiceDefinitionIDs []uuid.UUID
	CategoryIDs          []uuid.UUID
	PackageIDs           []uuid.UUID
	CheckIn              time.Time
	LastNight            time.Time
}

// PriceCandidate is one loaded price item ready to be scored, with the two things the
// single-date query decides in its WHERE and this one has to leave to the caller: which
// provider it belongs to, and over which days its contract version was published.
type PriceCandidate struct {
	Candidate         selection.Candidate
	Detail            contractapp.PriceDetail
	ProviderProfileID uuid.UUID
	VersionValidFrom  time.Time
	// VersionValidTo is the zero value for an open-ended version.
	VersionValidTo time.Time
}

// Repository is the persistence port. Every method runs inside the caller's transaction,
// which the service has already bound to the tenant, so RLS is active for every statement
// and no method here can be called outside one.
type Repository interface {
	CreateProperty(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewPropertyRow) (PropertyRecord, error)
	GetProperty(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scopeIDs []uuid.UUID) (PropertyRecord, error)
	ListProperties(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scopeIDs []uuid.UUID,
		f PropertyFilter, after *httpx.Cursor, limit int) ([]PropertyRecord, error)
	UpdateProperty(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in PropertyUpdateRow,
		expected int64, scopeIDs []uuid.UUID) (int64, error)

	CreateRoomType(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewRoomTypeRow) (RoomTypeRecord, error)
	GetRoomType(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scopeIDs []uuid.UUID) (RoomTypeContext, error)
	ListRoomTypes(ctx context.Context, tx pgx.Tx, tenantID, propertyID uuid.UUID,
		status string, scopeIDs []uuid.UUID) ([]RoomTypeRecord, error)
	UpdateRoomType(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in RoomTypeUpdateRow,
		expected int64, scopeIDs []uuid.UUID) (int64, error)

	GetInventoryRange(ctx context.Context, tx pgx.Tx, tenantID, roomTypeID uuid.UUID,
		from, to time.Time) ([]InventoryDayRecord, error)
	// LockInventoryRange reads the nights of the range FOR UPDATE in stay_date order, the
	// same order WP-I6-02 takes a hold in, so opening a season and taking a hold cannot
	// deadlock each other.
	LockInventoryRange(ctx context.Context, tx pgx.Tx, tenantID, roomTypeID uuid.UUID,
		from, to time.Time) ([]InventoryCommitment, error)
	SetInventoryCapacity(ctx context.Context, tx pgx.Tx, tenantID, roomTypeID uuid.UUID,
		from, to time.Time, capacity int) (int64, error)

	ListAvailabilityProperties(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		q AvailabilityPropertyQuery) ([]AvailabilityProperty, error)
	ListRoomTypesForAvailability(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		propertyIDs []uuid.UUID, adults, children, guests int) ([]AvailabilityRoomType, error)
	SummariseInventory(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		roomTypeIDs []uuid.UUID, from, to time.Time) (map[uuid.UUID]InventorySummary, error)
	ListPriceCandidates(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		q PriceCandidateQuery) ([]PriceCandidate, error)
	// CategoryPath is the definition's own category first, then each ancestor; the
	// specificity ladder scores a nearer ancestor above a further one.
	CategoryPath(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]uuid.UUID, error)
	PackagesContaining(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]uuid.UUID, error)
	// ListPersonProgramPayers is the payer organizations behind the programs this person
	// is enrolled in on the day, which is what narrows a search to the hotels somebody
	// contracted for them.
	ListPersonProgramPayers(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID,
		day time.Time, programID *uuid.UUID) ([]uuid.UUID, error)

	// OrganizationIsProvider reports whether the tenant organization has a provider
	// profile, so a property naming a sponsor is a field error rather than a hotel
	// nothing can ever be priced against.
	OrganizationIsProvider(ctx context.Context, tx pgx.Tx, tenantID, organizationID uuid.UUID) (bool, error)
	// LocationBelongsToOrganization checks the optional provider location is this
	// provider's own.
	LocationBelongsToOrganization(ctx context.Context, tx pgx.Tx, tenantID, locationID,
		organizationID uuid.UUID) (bool, error)
	// ServiceDefinitionUnit answers whether the definition exists in this tenant, whether
	// it is active, and what its default unit is. A room type priced as a service the
	// tenant does not have is a field error, not a foreign key violation.
	ServiceDefinitionUnit(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) (
		unitType string, active bool, found bool, err error)
}
