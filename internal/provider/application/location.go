package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/provider/domain"
)

// CreateLocation adds a place the provider works from. The provider is read through the
// scoped getter first, so a caller bound to another organization gets a 404 for the
// provider rather than a foreign key error for the location.
func (s *Service) CreateLocation(ctx context.Context, rc identity.RequestContext, providerID uuid.UUID, in domain.NewLocation) (LocationRecord, error) {
	if err := domain.ValidateNewLocation(&in); err != nil {
		return LocationRecord{}, err
	}

	var out LocationRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		if _, err := s.repo.GetProvider(ctx, tx, rc.TenantID, scope, providerID); err != nil {
			return err
		}
		id, err := s.repo.CreateLocation(ctx, tx, rc.TenantID, NewLocationRow{
			ProviderID: providerID, Code: in.Code, Name: in.Name,
			AddressLine: optional(in.AddressLine), District: optional(in.District),
			City: optional(in.City), CountryCode: in.CountryCode, PostalCode: optional(in.PostalCode),
			Latitude: in.Latitude, Longitude: in.Longitude, Timezone: in.Timezone,
			Phone: optional(in.Phone),
		})
		if err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "provider.location.create", "provider_location", id, map[string]any{
			"provider_id": providerID, "code": in.Code, "city": in.City,
		})); err != nil {
			return err
		}
		out, err = s.repo.GetLocation(ctx, tx, rc.TenantID, scope, id)
		return err
	})
	return out, err
}

// GetLocation returns one location visible to the caller.
func (s *Service) GetLocation(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (LocationRecord, error) {
	var out LocationRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.repo.GetLocation(ctx, tx, rc.TenantID, scopeOf(rc), id)
		return err
	})
	return out, err
}

// ListLocations returns one page of a provider's locations, newest first.
func (s *Service) ListLocations(ctx context.Context, rc identity.RequestContext, providerID uuid.UUID, f ListFilter) (LocationPage, error) {
	if err := validateListFilter(f); err != nil {
		return LocationPage{}, err
	}
	ve := &domain.ValidationError{}
	if f.Status != "" && !domain.Contains(domain.LocationStatuses, f.Status) {
		ve.Add("status", "ENUM", "geçersiz lokasyon durumu")
	}
	if err := ve.OrNil(); err != nil {
		return LocationPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return LocationPage{}, err
	}
	q := LocationQuery{
		ProviderID: providerID, Status: f.Status, City: f.City,
		Query: domain.LikePattern(f.Query), After: after, PageSize: pageSize + 1,
	}

	var rows []LocationRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		if _, err := s.repo.GetProvider(ctx, tx, rc.TenantID, scope, providerID); err != nil {
			return err
		}
		rows, err = s.repo.ListLocations(ctx, tx, rc.TenantID, scope, q)
		return err
	})
	if err != nil {
		return LocationPage{}, err
	}
	page := LocationPage{Items: rows}
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		last := page.Items[pageSize-1]
		page.NextCursor = s.nextCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

// UpdateLocation applies a merge-patch under optimistic concurrency.
func (s *Service) UpdateLocation(ctx context.Context, rc identity.RequestContext, id uuid.UUID, p domain.LocationPatch) (LocationRecord, error) {
	if err := domain.ValidateLocationPatch(p); err != nil {
		return LocationRecord{}, err
	}

	var out LocationRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		current, err := s.repo.GetLocation(ctx, tx, rc.TenantID, scope, id)
		if err != nil {
			return err
		}
		if current.RowVersion != p.ExpectedVersion {
			return ErrVersionMismatch
		}

		next := LocationUpdateRow{
			Name: current.Name, AddressLine: current.AddressLine, District: current.District,
			City: current.City, CountryCode: current.CountryCode, PostalCode: current.PostalCode,
			Latitude: current.Latitude, Longitude: current.Longitude, Timezone: current.Timezone,
			Phone: current.Phone, Status: current.Status,
		}
		var fields []string
		if p.Name != nil && *p.Name != current.Name {
			next.Name = *p.Name
			fields = append(fields, "name")
		}
		next.AddressLine = mergeText(p.AddressLine, p.ClearAddressLine, current.AddressLine, "addressLine", &fields)
		next.District = mergeText(p.District, p.ClearDistrict, current.District, "district", &fields)
		next.City = mergeText(p.City, p.ClearCity, current.City, "city", &fields)
		next.PostalCode = mergeText(p.PostalCode, p.ClearPostalCode, current.PostalCode, "postalCode", &fields)
		next.Phone = mergeText(p.Phone, p.ClearPhone, current.Phone, "phone", &fields)
		if p.CountryCode != nil && *p.CountryCode != current.CountryCode {
			next.CountryCode = *p.CountryCode
			fields = append(fields, "countryCode")
		}
		if p.Timezone != nil && *p.Timezone != current.Timezone {
			next.Timezone = *p.Timezone
			fields = append(fields, "timezone")
		}
		if p.Status != nil && *p.Status != current.Status {
			next.Status = *p.Status
			fields = append(fields, "status")
		}
		next.Latitude = mergeFloat(p.Latitude, p.ClearLatitude, current.Latitude, "latitude", &fields)
		next.Longitude = mergeFloat(p.Longitude, p.ClearLongitude, current.Longitude, "longitude", &fields)
		// A pin needs both halves; the database repeats the rule as ck_location_geo_pair.
		if err := domain.ValidateCoordinatePair(next.Latitude, next.Longitude); err != nil {
			return err
		}

		if err := s.repo.UpdateLocation(ctx, tx, rc.TenantID, scope, id, next, p.ExpectedVersion); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "provider.location.update", "provider_location", id, map[string]any{
			"provider_id": current.ProviderID, "code": current.Code,
			"changed_count": len(fields), "changed_fields": changed(fields),
		})); err != nil {
			return err
		}
		out, err = s.repo.GetLocation(ctx, tx, rc.TenantID, scope, id)
		return err
	})
	return out, err
}

// mergeText applies one nullable text field of a merge-patch and records whether it moved.
func mergeText(value *string, clear bool, current *string, field string, fields *[]string) *string {
	switch {
	case clear:
		if current != nil {
			*fields = append(*fields, field)
		}
		return nil
	case value != nil:
		next := optional(*value)
		if !sameString(next, current) {
			*fields = append(*fields, field)
		}
		return next
	default:
		return current
	}
}

// mergeFloat is mergeText for a coordinate.
func mergeFloat(value *float64, clear bool, current *float64, field string, fields *[]string) *float64 {
	switch {
	case clear:
		if current != nil {
			*fields = append(*fields, field)
		}
		return nil
	case value != nil:
		if !sameFloat(value, current) {
			*fields = append(*fields, field)
		}
		return value
	default:
		return current
	}
}
