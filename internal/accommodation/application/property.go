package application

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// NewPropertyInput is a property as the API states it.
type NewPropertyInput struct {
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

// PropertyPatchInput is the whole editable surface of a property. Every field is sent
// every time, so clearing a location or a cost centre is expressible.
type PropertyPatchInput struct {
	LocationID   *uuid.UUID
	Name         string
	PropertyType string
	Timezone     string
	City         *string
	RegionCode   *string
	Amenities    []string
	CostCenter   *string
	Status       string
}

// PropertyPage is one page of properties.
type PropertyPage struct {
	Items      []PropertyRecord
	NextCursor string
}

// ListProperties returns a page of properties, narrowed to the caller's own provider
// organizations when it has any.
func (s *Service) ListProperties(ctx context.Context, rc identity.RequestContext,
	f PropertyFilter,
) (PropertyPage, error) {
	if err := validatePropertyFilter(f); err != nil {
		return PropertyPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return PropertyPage{}, err
	}

	var out PropertyPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := s.repo.ListProperties(ctx, tx, rc.TenantID, scopeOf(rc), f, after, pageSize+1)
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			if s.cursors != nil {
				out.NextCursor = s.cursors.Encode(httpx.Cursor{
					CreatedAt: rows[pageSize-1].CreatedAt, ID: rows[pageSize-1].ID,
				})
			}
			rows = rows[:pageSize]
		}
		out.Items = rows
		return nil
	})
	if err != nil {
		return PropertyPage{}, err
	}
	return out, nil
}

// GetProperty reads one property. Another provider's property is not found rather than
// forbidden: that a hotel exists at all is not this caller's business.
func (s *Service) GetProperty(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID,
) (PropertyRecord, error) {
	var out PropertyRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.repo.GetProperty(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		return PropertyRecord{}, err
	}
	return out, nil
}

// CreateProperty opens a building.
//
// A create is the one write with no existing row to hide behind a 404, so a provider clerk
// naming somebody else's organization is refused with a scope error rather than told the
// organization does not exist — it plainly does, they simply may not write to it.
func (s *Service) CreateProperty(ctx context.Context, rc identity.RequestContext,
	in NewPropertyInput,
) (PropertyRecord, error) {
	normalised, err := validateNewProperty(&in)
	if err != nil {
		return PropertyRecord{}, err
	}
	if !insideScope(rc, in.ProviderOrganizationID) {
		return PropertyRecord{}, ErrPropertyScope
	}

	var out PropertyRecord
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.checkPropertyTargets(ctx, tx, rc.TenantID,
			in.ProviderOrganizationID, in.LocationID); err != nil {
			return err
		}
		record, err := s.repo.CreateProperty(ctx, tx, rc.TenantID, NewPropertyRow{
			ProviderOrganizationID: in.ProviderOrganizationID, LocationID: in.LocationID,
			Code: in.Code, Name: in.Name, PropertyType: in.PropertyType,
			Timezone: in.Timezone, City: in.City, RegionCode: in.RegionCode,
			Amenities: normalised, CostCenter: in.CostCenter, Status: in.Status,
			ActorID: rc.Principal.ActorID,
		})
		if err != nil {
			return err
		}
		out = record
		return s.record(ctx, tx, rc, ActionPropertyCreate, ResourceProperty, record.ID,
			map[string]any{
				"code": record.Code, "propertyType": record.PropertyType,
				"providerOrganizationId": record.ProviderOrganizationID.String(),
			})
	})
	if err != nil {
		return PropertyRecord{}, err
	}
	return out, nil
}

// PatchProperty rewrites the editable half of a property under an If-Match.
func (s *Service) PatchProperty(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, in PropertyPatchInput, expected int64,
) (PropertyRecord, error) {
	normalised, err := validatePropertyPatch(&in)
	if err != nil {
		return PropertyRecord{}, err
	}

	var out PropertyRecord
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		scopes := scopeOf(rc)
		// The read first, so a property this caller may not see is 404 rather than a
		// version mismatch that would confirm it exists.
		current, err := s.repo.GetProperty(ctx, tx, rc.TenantID, id, scopes)
		if err != nil {
			return err
		}
		if err := s.checkPropertyTargets(ctx, tx, rc.TenantID,
			current.ProviderOrganizationID, in.LocationID); err != nil {
			return err
		}
		affected, err := s.repo.UpdateProperty(ctx, tx, rc.TenantID, id, PropertyUpdateRow{
			LocationID: in.LocationID, Name: in.Name, PropertyType: in.PropertyType,
			Timezone: in.Timezone, City: in.City, RegionCode: in.RegionCode,
			Amenities: normalised, CostCenter: in.CostCenter, Status: in.Status,
			ActorID: rc.Principal.ActorID,
		}, expected, scopes)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrVersionMismatch
		}
		out, err = s.repo.GetProperty(ctx, tx, rc.TenantID, id, scopes)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, rc, ActionPropertyUpdate, ResourceProperty, id,
			map[string]any{"code": out.Code, "status": out.Status})
	})
	if err != nil {
		return PropertyRecord{}, err
	}
	return out, nil
}

// checkPropertyTargets refuses a property naming an organization that is not a provider,
// or a location that is not that provider's. Both are field errors rather than raw foreign
// key violations, because a clerk who pasted the wrong id needs to be told which field.
func (s *Service) checkPropertyTargets(ctx context.Context, tx pgx.Tx, tenantID,
	organizationID uuid.UUID, locationID *uuid.UUID,
) error {
	isProvider, err := s.repo.OrganizationIsProvider(ctx, tx, tenantID, organizationID)
	if err != nil {
		return err
	}
	if !isProvider {
		return fieldError("providerOrganizationId", "NOT_FOUND",
			"bu kurum için sağlayıcı profili yok")
	}
	if locationID == nil {
		return nil
	}
	ok, err := s.repo.LocationBelongsToOrganization(ctx, tx, tenantID, *locationID, organizationID)
	if err != nil {
		return err
	}
	if !ok {
		return fieldError("locationId", "NOT_FOUND", "lokasyon bu sağlayıcıya ait değil")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// codePattern mirrors ck_property_code and ck_room_type_code.
func validCode(code string) bool {
	if code == "" || len(code) > 40 {
		return false
	}
	for i, r := range code {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '_' || r == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

func validateNewProperty(in *NewPropertyInput) ([]string, error) {
	ve := &domain.ValidationError{}
	if in.ProviderOrganizationID == uuid.Nil {
		ve.Add("providerOrganizationId", "REQUIRED", "sağlayıcı kurumu zorunlu")
	}
	in.Code = strings.TrimSpace(in.Code)
	if !validCode(in.Code) {
		ve.Add("code", "FORMAT", "A-Z, 0-9, _ ve - içeren en fazla 40 karakter olmalı")
	}
	amenities := validatePropertyBody(ve, &in.Name, &in.PropertyType, &in.Timezone,
		in.Amenities, &in.Status, in.City, in.RegionCode, in.CostCenter)
	return amenities, ve.OrNil()
}

func validatePropertyPatch(in *PropertyPatchInput) ([]string, error) {
	ve := &domain.ValidationError{}
	amenities := validatePropertyBody(ve, &in.Name, &in.PropertyType, &in.Timezone,
		in.Amenities, &in.Status, in.City, in.RegionCode, in.CostCenter)
	return amenities, ve.OrNil()
}

// validatePropertyBody holds the rules a create and a patch share, so the two cannot drift
// apart — a timezone the create refuses and the patch accepts would be a hotel whose
// nights are counted against the wrong calendar from its second edit onwards.
//
//nolint:gocritic // the pointers are how the two callers get their trimmed values back
func validatePropertyBody(ve *domain.ValidationError, name, propertyType, timezone *string,
	amenities []string, status *string, city, regionCode, costCenter *string,
) []string {
	*name = strings.TrimSpace(*name)
	if *name == "" || len([]rune(*name)) > 200 {
		ve.Add("name", "RANGE", "1-200 karakter olmalı")
	}
	if !domain.InList(*propertyType, domain.PropertyTypes) {
		ve.Add("propertyType", "ENUM", "tanımlı bir tesis türü olmalı")
	}
	*timezone = strings.TrimSpace(*timezone)
	if !domain.ValidTimezone(*timezone) {
		ve.Add("timezone", "ENUM", "geçerli bir IANA saat dilimi olmalı")
	}
	if !domain.InList(*status, domain.Statuses) {
		ve.Add("status", "ENUM", "ACTIVE veya INACTIVE olmalı")
	}
	if city != nil && len([]rune(*city)) > 100 {
		ve.Add("city", "RANGE", "en fazla 100 karakter olabilir")
	}
	if regionCode != nil && !validRegionCode(*regionCode) {
		ve.Add("regionCode", "FORMAT", "A-Z, 0-9, _ ve - içeren en fazla 16 karakter olmalı")
	}
	if costCenter != nil && !validCostCenter(*costCenter) {
		ve.Add("costCenter", "FORMAT", "A-Z, 0-9, ., _ ve - içeren en fazla 32 karakter olmalı")
	}
	return domain.NormaliseAmenities("amenities", amenities, ve)
}

func validRegionCode(code string) bool {
	if code == "" || len(code) > 16 {
		return false
	}
	for i, r := range code {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '_' || r == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

func validCostCenter(code string) bool {
	if code == "" || len(code) > 32 {
		return false
	}
	for i, r := range code {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '_' || r == '-' || r == '.') && i > 0:
		default:
			return false
		}
	}
	return true
}

func validatePropertyFilter(f PropertyFilter) error {
	ve := &domain.ValidationError{}
	if f.Status != "" && !domain.InList(f.Status, domain.Statuses) {
		ve.Add("status", "ENUM", "ACTIVE veya INACTIVE olmalı")
	}
	if f.PropertyType != "" && !domain.InList(f.PropertyType, domain.PropertyTypes) {
		ve.Add("propertyType", "ENUM", "tanımlı bir tesis türü olmalı")
	}
	return ve.OrNil()
}
