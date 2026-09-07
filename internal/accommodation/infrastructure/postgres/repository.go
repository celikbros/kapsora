// Package postgres implements the accommodation repository over sqlc-generated queries.
//
// Every method takes the caller's transaction, which the service has already bound to the
// tenant, so RLS is active for every statement; none of them opens one of its own. The
// provider boundary travels as `scopeIDs`, a nil slice meaning "the whole tenant" and a
// non-nil one meaning "these organizations and no others", and it is passed to the query
// rather than applied afterwards in Go — a filter applied after the read is a filter a
// LIMIT has already defeated.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/accommodation/application"
	accdomain "github.com/celikbros/kapsora/internal/accommodation/domain"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	contractdomain "github.com/celikbros/kapsora/internal/contract/domain"
	"github.com/celikbros/kapsora/internal/contract/selection"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// PostgreSQL error codes this package turns into named application errors. A unique
// violation on a code is a 409 the caller can act on; anything else stays an error the
// transport logs and answers 500 with, because a constraint nobody predicted is a bug and
// not a message for a clerk.
const uniqueViolation = "23505"

// Repository is stateless; the transaction carries everything.
type Repository struct{}

// New returns the repository.
func New() Repository { return Repository{} }

var _ application.Repository = Repository{}

// ---------------------------------------------------------------------------
// Property
// ---------------------------------------------------------------------------

// CreateProperty implements application.Repository.
func (Repository) CreateProperty(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewPropertyRow,
) (application.PropertyRecord, error) {
	amenities, err := marshalAmenities(in.Amenities)
	if err != nil {
		return application.PropertyRecord{}, err
	}
	row, err := sqlcgen.New(tx).CreateProperty(ctx, sqlcgen.CreatePropertyParams{
		TenantID: tenantID, ProviderOrganizationID: in.ProviderOrganizationID,
		LocationID: nullUUID(in.LocationID), Code: in.Code, Name: in.Name,
		PropertyType: in.PropertyType, Timezone: in.Timezone, City: in.City,
		RegionCode: in.RegionCode, Amenities: amenities, CostCenter: in.CostCenter,
		Status: in.Status, ActorID: actorUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) &&
			pgErr.Code == uniqueViolation && pgErr.ConstraintName == "uq_property_code" {
			return application.PropertyRecord{}, application.ErrPropertyCodeTaken
		}
		return application.PropertyRecord{}, fmt.Errorf("accommodation: create property: %w", err)
	}
	return propertyOf(propertyColumns{
		ID: row.ID, ProviderOrganizationID: row.ProviderOrganizationID, LocationID: row.LocationID,
		Code: row.Code, Name: row.Name, PropertyType: row.PropertyType, Timezone: row.Timezone,
		City: row.City, RegionCode: row.RegionCode, Amenities: row.Amenities,
		CostCenter: row.CostCenter, Status: row.Status, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt, RowVersion: row.RowVersion,
	})
}

// GetProperty implements application.Repository.
func (Repository) GetProperty(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scopeIDs []uuid.UUID,
) (application.PropertyRecord, error) {
	row, err := sqlcgen.New(tx).GetProperty(ctx, sqlcgen.GetPropertyParams{
		TenantID: tenantID, ID: id, ScopeIds: scopeIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PropertyRecord{}, application.ErrPropertyNotFound
	}
	if err != nil {
		return application.PropertyRecord{}, fmt.Errorf("accommodation: read property: %w", err)
	}
	return propertyOf(propertyColumns{
		ID: row.ID, ProviderOrganizationID: row.ProviderOrganizationID, LocationID: row.LocationID,
		Code: row.Code, Name: row.Name, PropertyType: row.PropertyType, Timezone: row.Timezone,
		City: row.City, RegionCode: row.RegionCode, Amenities: row.Amenities,
		CostCenter: row.CostCenter, Status: row.Status, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt, RowVersion: row.RowVersion,
	})
}

// ListProperties implements application.Repository.
func (Repository) ListProperties(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	scopeIDs []uuid.UUID, f application.PropertyFilter, after *httpx.Cursor, limit int,
) ([]application.PropertyRecord, error) {
	params := sqlcgen.ListPropertiesParams{
		TenantID: tenantID, ScopeIds: scopeIDs,
		ProviderOrganizationID: nullUUID(f.ProviderOrganizationID),
		Status:                 optionalString(f.Status),
		PropertyType:           optionalString(f.PropertyType),
		RegionCode:             optionalString(f.RegionCode),
		City:                   optionalString(f.City),
		PageSize:               int32(limit), //nolint:gosec // clamped by httpx.ClampLimit
	}
	if after != nil {
		at := after.CreatedAt
		params.AfterAt = &at
		params.AfterID = uuid.NullUUID{UUID: after.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListProperties(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("accommodation: list properties: %w", err)
	}
	out := make([]application.PropertyRecord, 0, len(rows))
	for _, row := range rows {
		record, err := propertyOf(propertyColumns{
			ID: row.ID, ProviderOrganizationID: row.ProviderOrganizationID, LocationID: row.LocationID,
			Code: row.Code, Name: row.Name, PropertyType: row.PropertyType, Timezone: row.Timezone,
			City: row.City, RegionCode: row.RegionCode, Amenities: row.Amenities,
			CostCenter: row.CostCenter, Status: row.Status, CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt, RowVersion: row.RowVersion,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

// UpdateProperty implements application.Repository.
func (Repository) UpdateProperty(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.PropertyUpdateRow, expected int64, scopeIDs []uuid.UUID,
) (int64, error) {
	amenities, err := marshalAmenities(in.Amenities)
	if err != nil {
		return 0, err
	}
	affected, err := sqlcgen.New(tx).UpdateProperty(ctx, sqlcgen.UpdatePropertyParams{
		TenantID: tenantID, ID: id, ExpectedRowVersion: expected, ScopeIds: scopeIDs,
		LocationID: nullUUID(in.LocationID), Name: in.Name, PropertyType: in.PropertyType,
		Timezone: in.Timezone, City: in.City, RegionCode: in.RegionCode,
		Amenities: amenities, CostCenter: in.CostCenter, Status: in.Status,
		ActorID: actorUUID(in.ActorID),
	})
	if err != nil {
		return 0, fmt.Errorf("accommodation: update property: %w", err)
	}
	return affected, nil
}

// ---------------------------------------------------------------------------
// Room type
// ---------------------------------------------------------------------------

// CreateRoomType implements application.Repository.
func (Repository) CreateRoomType(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewRoomTypeRow,
) (application.RoomTypeRecord, error) {
	row, err := sqlcgen.New(tx).CreateRoomType(ctx, sqlcgen.CreateRoomTypeParams{
		TenantID: tenantID, PropertyID: in.PropertyID, Code: in.Code, Name: in.Name,
		MaxAdults: int32(in.MaxAdults), MaxChildren: int32(in.MaxChildren), //nolint:gosec // bounded by validation
		MaxOccupancy: int32(in.MaxOccupancy), //nolint:gosec // bounded by validation
		Attributes:   in.Attributes, ServiceDefinitionID: in.ServiceDefinitionID,
		Status: in.Status, ActorID: actorUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) &&
			pgErr.Code == uniqueViolation && pgErr.ConstraintName == "uq_room_type_code" {
			return application.RoomTypeRecord{}, application.ErrRoomTypeCodeTaken
		}
		return application.RoomTypeRecord{}, fmt.Errorf("accommodation: create room type: %w", err)
	}
	return application.RoomTypeRecord{
		ID: row.ID, PropertyID: row.PropertyID, Code: row.Code, Name: row.Name,
		MaxAdults: int(row.MaxAdults), MaxChildren: int(row.MaxChildren),
		MaxOccupancy: int(row.MaxOccupancy), Attributes: row.Attributes,
		ServiceDefinitionID: row.ServiceDefinitionID, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, RowVersion: row.RowVersion,
	}, nil
}

// GetRoomType implements application.Repository.
func (Repository) GetRoomType(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scopeIDs []uuid.UUID,
) (application.RoomTypeContext, error) {
	row, err := sqlcgen.New(tx).GetRoomType(ctx, sqlcgen.GetRoomTypeParams{
		TenantID: tenantID, ID: id, ScopeIds: scopeIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RoomTypeContext{}, application.ErrRoomTypeNotFound
	}
	if err != nil {
		return application.RoomTypeContext{}, fmt.Errorf("accommodation: read room type: %w", err)
	}
	return application.RoomTypeContext{
		RoomType: application.RoomTypeRecord{
			ID: row.ID, PropertyID: row.PropertyID, Code: row.Code, Name: row.Name,
			MaxAdults: int(row.MaxAdults), MaxChildren: int(row.MaxChildren),
			MaxOccupancy: int(row.MaxOccupancy), Attributes: row.Attributes,
			ServiceDefinitionID: row.ServiceDefinitionID, Status: row.Status,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, RowVersion: row.RowVersion,
		},
		ProviderOrganizationID: row.ProviderOrganizationID,
		PropertyTimezone:       row.PropertyTimezone,
		PropertyStatus:         row.PropertyStatus,
	}, nil
}

// ListRoomTypes implements application.Repository.
func (Repository) ListRoomTypes(ctx context.Context, tx pgx.Tx, tenantID, propertyID uuid.UUID,
	status string, scopeIDs []uuid.UUID,
) ([]application.RoomTypeRecord, error) {
	rows, err := sqlcgen.New(tx).ListRoomTypes(ctx, sqlcgen.ListRoomTypesParams{
		TenantID: tenantID, PropertyID: propertyID, Status: optionalString(status),
		ScopeIds: scopeIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: list room types: %w", err)
	}
	out := make([]application.RoomTypeRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.RoomTypeRecord{
			ID: row.ID, PropertyID: row.PropertyID, Code: row.Code, Name: row.Name,
			MaxAdults: int(row.MaxAdults), MaxChildren: int(row.MaxChildren),
			MaxOccupancy: int(row.MaxOccupancy), Attributes: row.Attributes,
			ServiceDefinitionID: row.ServiceDefinitionID, Status: row.Status,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, RowVersion: row.RowVersion,
		})
	}
	return out, nil
}

// UpdateRoomType implements application.Repository.
func (Repository) UpdateRoomType(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.RoomTypeUpdateRow, expected int64, scopeIDs []uuid.UUID,
) (int64, error) {
	affected, err := sqlcgen.New(tx).UpdateRoomType(ctx, sqlcgen.UpdateRoomTypeParams{
		TenantID: tenantID, ID: id, ExpectedRowVersion: expected, ScopeIds: scopeIDs,
		Name: in.Name, MaxAdults: int32(in.MaxAdults), //nolint:gosec // bounded by validation
		MaxChildren: int32(in.MaxChildren), MaxOccupancy: int32(in.MaxOccupancy), //nolint:gosec // bounded by validation
		Attributes: in.Attributes, Status: in.Status, ActorID: actorUUID(in.ActorID),
	})
	if err != nil {
		return 0, fmt.Errorf("accommodation: update room type: %w", err)
	}
	return affected, nil
}

// ---------------------------------------------------------------------------
// Inventory
// ---------------------------------------------------------------------------

// GetInventoryRange implements application.Repository.
func (Repository) GetInventoryRange(ctx context.Context, tx pgx.Tx, tenantID,
	roomTypeID uuid.UUID, from, to time.Time,
) ([]application.InventoryDayRecord, error) {
	rows, err := sqlcgen.New(tx).GetRoomTypeInventoryRange(ctx, sqlcgen.GetRoomTypeInventoryRangeParams{
		TenantID: tenantID, RoomTypeID: roomTypeID, FromDate: dateOf(from), ToDate: dateOf(to),
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: read inventory range: %w", err)
	}
	out := make([]application.InventoryDayRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.InventoryDayRecord{
			StayDate: dateTime(row.StayDate), Capacity: int(row.Capacity),
			Held: int(row.Held), Confirmed: int(row.Confirmed), Available: int(row.Available),
			UpdatedAt: row.UpdatedAt, RowVersion: row.RowVersion,
		})
	}
	return out, nil
}

// LockInventoryRange implements application.Repository.
func (Repository) LockInventoryRange(ctx context.Context, tx pgx.Tx, tenantID,
	roomTypeID uuid.UUID, from, to time.Time,
) ([]application.InventoryCommitment, error) {
	rows, err := sqlcgen.New(tx).LockRoomTypeInventoryRange(ctx, sqlcgen.LockRoomTypeInventoryRangeParams{
		TenantID: tenantID, RoomTypeID: roomTypeID, FromDate: dateOf(from), ToDate: dateOf(to),
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: lock inventory range: %w", err)
	}
	out := make([]application.InventoryCommitment, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.InventoryCommitment{
			StayDate: dateTime(row.StayDate), Capacity: int(row.Capacity),
			Held: int(row.Held), Confirmed: int(row.Confirmed),
		})
	}
	return out, nil
}

// SetInventoryCapacity implements application.Repository.
func (Repository) SetInventoryCapacity(ctx context.Context, tx pgx.Tx, tenantID,
	roomTypeID uuid.UUID, from, to time.Time, capacity int,
) (int64, error) {
	affected, err := sqlcgen.New(tx).SetRoomTypeInventoryCapacity(ctx,
		sqlcgen.SetRoomTypeInventoryCapacityParams{
			TenantID: tenantID, RoomTypeID: roomTypeID,
			Capacity: int32(capacity), //nolint:gosec // bounded by validation
			FromDate: dateOf(from), ToDate: dateOf(to),
		})
	if err != nil {
		return 0, fmt.Errorf("accommodation: set inventory capacity: %w", err)
	}
	return affected, nil
}

// ---------------------------------------------------------------------------
// Availability search
// ---------------------------------------------------------------------------

// ListAvailabilityProperties implements application.Repository.
func (Repository) ListAvailabilityProperties(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.AvailabilityPropertyQuery,
) ([]application.AvailabilityProperty, error) {
	rows, err := sqlcgen.New(tx).ListAvailabilityProperties(ctx, sqlcgen.ListAvailabilityPropertiesParams{
		TenantID: tenantID, PropertyID: nullUUID(q.PropertyID),
		RegionCode: optionalString(q.RegionCode), ScopeIds: q.ScopeIDs,
		LastNight: dateOf(q.LastNight), CheckIn: dateOf(q.CheckIn),
		PayerOrganizationIds: q.PayerOrganizationIDs,
		PageSize:             int32(q.Limit), //nolint:gosec // a package constant
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: list availability properties: %w", err)
	}
	out := make([]application.AvailabilityProperty, 0, len(rows))
	for _, row := range rows {
		record, err := propertyOf(propertyColumns{
			ID: row.ID, ProviderOrganizationID: row.ProviderOrganizationID, LocationID: row.LocationID,
			Code: row.Code, Name: row.Name, PropertyType: row.PropertyType, Timezone: row.Timezone,
			City: row.City, RegionCode: row.RegionCode, Amenities: row.Amenities,
			CostCenter: row.CostCenter, Status: row.Status, CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt, RowVersion: row.RowVersion,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, application.AvailabilityProperty{
			Property: record, ProviderProfileID: row.ProviderProfileID,
		})
	}
	return out, nil
}

// ListRoomTypesForAvailability implements application.Repository.
func (Repository) ListRoomTypesForAvailability(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	propertyIDs []uuid.UUID, adults, children, guests int,
) ([]application.AvailabilityRoomType, error) {
	rows, err := sqlcgen.New(tx).ListRoomTypesForAvailability(ctx,
		sqlcgen.ListRoomTypesForAvailabilityParams{
			TenantID: tenantID, PropertyIds: nonNil(propertyIDs),
			Adults: int32(adults), Children: int32(children), Guests: int32(guests), //nolint:gosec // bounded by validation
		})
	if err != nil {
		return nil, fmt.Errorf("accommodation: list room types for availability: %w", err)
	}
	out := make([]application.AvailabilityRoomType, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.AvailabilityRoomType{
			ID: row.ID, PropertyID: row.PropertyID, Code: row.Code, Name: row.Name,
			MaxAdults: int(row.MaxAdults), MaxChildren: int(row.MaxChildren),
			MaxOccupancy: int(row.MaxOccupancy), Attributes: row.Attributes,
			ServiceDefinitionID: row.ServiceDefinitionID, Status: row.Status,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, RowVersion: row.RowVersion,
		})
	}
	return out, nil
}

// SummariseInventory implements application.Repository.
func (Repository) SummariseInventory(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	roomTypeIDs []uuid.UUID, from, to time.Time,
) (map[uuid.UUID]application.InventorySummary, error) {
	rows, err := sqlcgen.New(tx).SummariseInventoryOverRange(ctx,
		sqlcgen.SummariseInventoryOverRangeParams{
			TenantID: tenantID, RoomTypeIds: nonNil(roomTypeIDs),
			FromDate: dateOf(from), ToDate: dateOf(to),
		})
	if err != nil {
		return nil, fmt.Errorf("accommodation: summarise inventory: %w", err)
	}
	out := make(map[uuid.UUID]application.InventorySummary, len(rows))
	for _, row := range rows {
		out[row.RoomTypeID] = application.InventorySummary{
			MinAvailable: int(row.MinAvailable), NightCount: int(row.NightCount),
		}
	}
	return out, nil
}

// ListPriceCandidates implements application.Repository. It is the contract module's own
// candidate mapping over a range query: the same selection.Candidate, the same PriceDetail,
// plus the provider and the version period this search has to filter by per night.
func (Repository) ListPriceCandidates(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.PriceCandidateQuery,
) ([]application.PriceCandidate, error) {
	rows, err := sqlcgen.New(tx).ListAccommodationPriceCandidates(ctx,
		sqlcgen.ListAccommodationPriceCandidatesParams{
			TenantID: tenantID, ProviderProfileIds: nonNil(q.ProviderProfileIDs),
			ServiceDefinitionIds: nonNil(q.ServiceDefinitionIDs),
			CategoryIds:          nonNil(q.CategoryIDs), PackageIds: nonNil(q.PackageIDs),
			LastNight: dateOf(q.LastNight), CheckIn: dateOf(q.CheckIn),
		})
	if err != nil {
		return nil, fmt.Errorf("accommodation: list price candidates: %w", err)
	}
	out := make([]application.PriceCandidate, 0, len(rows))
	for _, r := range rows {
		candidate := selection.Candidate{
			PriceItemID: r.PriceItemID, PriceListID: r.PriceListID,
			ContractVersionID: r.ContractVersionID, LocationID: uuidValue(r.LocationID),
			ValidFrom: dateTime(r.ValidFrom), ValidTo: dateTime(r.ValidTo),
			SeasonFrom: dateTime(r.SeasonFrom), SeasonTo: dateTime(r.SeasonTo),
			WeekdayMask:  weekdayMask(r.WeekdayMask),
			ItemPriority: int(r.ItemPriority), ListPriority: int(r.ListPriority),
		}
		switch {
		case r.ServiceDefinitionID.Valid:
			candidate.Target = selection.TargetDefinition
			candidate.DefinitionID = r.ServiceDefinitionID.UUID
		case r.PackageDefinitionID.Valid:
			candidate.Target = selection.TargetPackage
			candidate.PackageID = r.PackageDefinitionID.UUID
		case r.ServiceCategoryID.Valid:
			candidate.Target = selection.TargetCategory
			candidate.CategoryID = r.ServiceCategoryID.UUID
		}
		out = append(out, application.PriceCandidate{
			Candidate: candidate,
			Detail: contractapp.PriceDetail{
				ContractID: r.ContractID, ContractCode: r.ContractCode, VersionNo: int(r.VersionNo),
				CurrencyCode: r.CurrencyCode, PriceListCode: r.PriceListCode,
				LocationID: uuidPtr(r.LocationID), UnitType: r.UnitType,
				PricingMethod: r.PricingMethod,
				Amount:        decimal(r.Amount), Percent: decimal(r.Percent), FormulaKey: r.FormulaKey,
				MinAmount: decimal(r.MinAmount), MaxAmount: decimal(r.MaxAmount),
				MemberShareMethod:  r.MemberShareMethod,
				MemberShareAmount:  decimal(r.MemberShareAmount),
				MemberSharePercent: decimal(r.MemberSharePercent),
			},
			ProviderProfileID: r.ProviderProfileID,
			VersionValidFrom:  dateTime(r.VersionValidFrom),
			VersionValidTo:    dateTime(r.VersionValidTo),
		})
	}
	return out, nil
}

// CategoryPath implements application.Repository by walking catalog.service_category
// upward from the definition's own category; the nearest ancestor comes first, which is the
// order the specificity ladder scores.
func (Repository) CategoryPath(ctx context.Context, tx pgx.Tx, tenantID,
	definitionID uuid.UUID,
) ([]uuid.UUID, error) {
	rows, err := sqlcgen.New(tx).ServiceDefinitionCategoryChain(ctx,
		sqlcgen.ServiceDefinitionCategoryChainParams{
			TenantID: tenantID, ServiceDefinitionID: definitionID,
		})
	if err != nil {
		return nil, fmt.Errorf("accommodation: category path: %w", err)
	}
	out := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out, nil
}

// PackagesContaining implements application.Repository.
func (Repository) PackagesContaining(ctx context.Context, tx pgx.Tx, tenantID,
	definitionID uuid.UUID,
) ([]uuid.UUID, error) {
	rows, err := sqlcgen.New(tx).ListPackagesContainingDefinition(ctx,
		sqlcgen.ListPackagesContainingDefinitionParams{
			TenantID: tenantID, ServiceDefinitionID: definitionID,
		})
	if err != nil {
		return nil, fmt.Errorf("accommodation: packages containing definition: %w", err)
	}
	return rows, nil
}

// ListPersonProgramPayers implements application.Repository.
func (Repository) ListPersonProgramPayers(ctx context.Context, tx pgx.Tx, tenantID,
	personID uuid.UUID, day time.Time, programID *uuid.UUID,
) ([]uuid.UUID, error) {
	rows, err := sqlcgen.New(tx).ListPersonProgramPayers(ctx, sqlcgen.ListPersonProgramPayersParams{
		TenantID: tenantID, PersonID: personID, ServiceDate: dateOf(day),
		ProgramID: nullUUID(programID),
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: list person program payers: %w", err)
	}
	return rows, nil
}

// OrganizationIsProvider implements application.Repository.
func (Repository) OrganizationIsProvider(ctx context.Context, tx pgx.Tx, tenantID,
	organizationID uuid.UUID,
) (bool, error) {
	var present bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM provider.provider_profile
			 WHERE tenant_id = $1 AND tenant_organization_id = $2)`,
		tenantID, organizationID).Scan(&present)
	if err != nil {
		return false, fmt.Errorf("accommodation: organization is provider: %w", err)
	}
	return present, nil
}

// LocationBelongsToOrganization implements application.Repository.
func (Repository) LocationBelongsToOrganization(ctx context.Context, tx pgx.Tx, tenantID,
	locationID, organizationID uuid.UUID,
) (bool, error) {
	var present bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM provider.location l
			  JOIN provider.provider_profile p
			    ON p.tenant_id = l.tenant_id AND p.id = l.provider_profile_id
			 WHERE l.tenant_id = $1 AND l.id = $2 AND p.tenant_organization_id = $3)`,
		tenantID, locationID, organizationID).Scan(&present)
	if err != nil {
		return false, fmt.Errorf("accommodation: location belongs to organization: %w", err)
	}
	return present, nil
}

// ServiceDefinitionUnit implements application.Repository.
func (Repository) ServiceDefinitionUnit(ctx context.Context, tx pgx.Tx, tenantID,
	definitionID uuid.UUID,
) (string, bool, bool, error) {
	var unit string
	var active bool
	err := tx.QueryRow(ctx, `
		SELECT default_unit_type, active
		  FROM catalog.service_definition
		 WHERE tenant_id = $1 AND id = $2`, tenantID, definitionID).Scan(&unit, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, fmt.Errorf("accommodation: read service definition: %w", err)
	}
	return unit, active, true, nil
}

// ---------------------------------------------------------------------------
// Mapping helpers
// ---------------------------------------------------------------------------

// propertyColumns is the shape every property-returning query has in common, so the row to
// record mapping is written once. Four queries return the same twelve columns; four copies
// of the amenity decoding would be four places for it to disagree.
type propertyColumns struct {
	ID                     uuid.UUID
	ProviderOrganizationID uuid.UUID
	LocationID             uuid.NullUUID
	Code                   string
	Name                   string
	PropertyType           string
	Timezone               string
	City                   *string
	RegionCode             *string
	Amenities              []byte
	CostCenter             *string
	Status                 string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	RowVersion             int64
}

func propertyOf(c propertyColumns) (application.PropertyRecord, error) {
	amenities, err := unmarshalAmenities(c.Amenities)
	if err != nil {
		return application.PropertyRecord{}, err
	}
	return application.PropertyRecord{
		ID: c.ID, ProviderOrganizationID: c.ProviderOrganizationID,
		LocationID: uuidPtr(c.LocationID), Code: c.Code, Name: c.Name,
		PropertyType: c.PropertyType, Timezone: c.Timezone, City: c.City,
		RegionCode: c.RegionCode, Amenities: amenities, CostCenter: c.CostCenter,
		Status: c.Status, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		RowVersion: c.RowVersion,
	}, nil
}

// marshalAmenities writes the array. An empty set is `[]` and never `null`: the column has
// a CHECK saying it is an array, and a null would be a property nobody could read back.
func marshalAmenities(keys []string) ([]byte, error) {
	if keys == nil {
		keys = []string{}
	}
	raw, err := json.Marshal(keys)
	if err != nil {
		return nil, fmt.Errorf("accommodation: encode amenities: %w", err)
	}
	return raw, nil
}

// unmarshalAmenities reads it back, dropping any key the catalogue no longer knows. A key
// removed from the Go list is a key the product stopped supporting, and returning it would
// put a value on the wire that the contract's enum does not admit.
func unmarshalAmenities(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	var keys []string
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, fmt.Errorf("accommodation: decode amenities: %w", err)
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if accdomain.KnownAmenity(key) {
			out = append(out, key)
		}
	}
	return out, nil
}

// decimal is the contract module's own canonicalisation, so a price read here is spelled
// exactly as the same price read by a counter quote. Two spellings of one amount would be
// two amounts to anybody comparing a quote with a claim.
func decimal(raw string) string { return contractdomain.CanonicalDecimal(raw) }

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil || *id == uuid.Nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func actorUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}

func uuidValue(n uuid.NullUUID) uuid.UUID {
	if !n.Valid {
		return uuid.Nil
	}
	return n.UUID
}

func weekdayMask(n *int16) uint8 {
	if n == nil || *n < 0 || *n > 127 {
		return 0
	}
	return uint8(*n)
}

func dateOf(t time.Time) pgtype.Date {
	if t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: accdomain.Day(t), Valid: true}
}

func dateTime(d pgtype.Date) time.Time {
	if !d.Valid || d.InfinityModifier != pgtype.Finite {
		return time.Time{}
	}
	return accdomain.Day(d.Time)
}

func nonNil(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
