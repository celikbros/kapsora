package application

import (
	"context"
	"errors"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/provider/domain"
)

// ListCapabilities returns everything a location can deliver, ordered by validity start.
func (s *Service) ListCapabilities(ctx context.Context, rc identity.RequestContext, locationID uuid.UUID) (CapabilityResult, error) {
	var out CapabilityResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		location, err := s.repo.GetLocation(ctx, tx, rc.TenantID, scopeOf(rc), locationID)
		if err != nil {
			return err
		}
		out.RowVersion = location.RowVersion
		out.Items, err = s.repo.ListCapabilities(ctx, tx, rc.TenantID, locationID)
		return err
	})
	return out, err
}

// ReplaceCapabilities swaps the whole capability set of a location under the location's own
// optimistic-concurrency token. The submitted set is checked against itself first, because a
// PUT replaces everything and a self-contradicting payload would otherwise never reach the
// exclusion constraints; those constraints remain the authority against the stored rows.
func (s *Service) ReplaceCapabilities(ctx context.Context, rc identity.RequestContext, locationID uuid.UUID,
	items []domain.CapabilityInput, expected int64,
) (CapabilityResult, error) {
	if err := domain.ValidateCapabilitySet(items); err != nil {
		return CapabilityResult{}, err
	}
	rows := make([]CapabilityRow, 0, len(items))
	for i, it := range items {
		row := CapabilityRow{
			ValidFrom: domain.DateOnly(it.ValidFrom), ValidTo: dayPtr(it.ValidTo), Notes: optional(it.Notes),
		}
		if it.ServiceDefinitionID != "" {
			id, err := uuid.Parse(it.ServiceDefinitionID)
			if err != nil {
				return CapabilityResult{}, itemFieldError(i, "serviceDefinitionId", "FORMAT", "geçerli bir kimlik olmalı")
			}
			row.ServiceDefinitionID = &id
		}
		if it.ServiceCategoryID != "" {
			id, err := uuid.Parse(it.ServiceCategoryID)
			if err != nil {
				return CapabilityResult{}, itemFieldError(i, "serviceCategoryId", "FORMAT", "geçerli bir kimlik olmalı")
			}
			row.ServiceCategoryID = &id
		}
		rows = append(rows, row)
	}

	var out CapabilityResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		location, err := s.repo.GetLocation(ctx, tx, rc.TenantID, scope, locationID)
		if err != nil {
			return err
		}
		if location.RowVersion != expected {
			return ErrVersionMismatch
		}
		if err := s.repo.ReplaceCapabilities(ctx, tx, rc.TenantID, locationID, rows); err != nil {
			return capabilityTargetError(err)
		}
		// The capability rows are children, so the location is touched explicitly to move
		// the ETag the caller holds.
		if err := s.repo.TouchLocation(ctx, tx, rc.TenantID, locationID, expected); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "provider.capability.replace", "provider_location", locationID, map[string]any{
			"provider_id": location.ProviderID, "code": location.Code, "capability_count": len(rows),
		})); err != nil {
			return err
		}
		updated, err := s.repo.GetLocation(ctx, tx, rc.TenantID, scope, locationID)
		if err != nil {
			return err
		}
		out.RowVersion = updated.RowVersion
		out.Items, err = s.repo.ListCapabilities(ctx, tx, rc.TenantID, locationID)
		return err
	})
	if err != nil {
		return CapabilityResult{}, err
	}
	return out, nil
}

// capabilityTargetError turns "the catalog row does not exist" into a field error on the
// request rather than a bare 404 for the location the caller asked about. The offending
// index is not knowable from the constraint, so the whole array is named.
func capabilityTargetError(err error) error {
	if !errors.Is(err, ErrCapabilityTargetNotFound) {
		return err
	}
	ve := &domain.ValidationError{}
	ve.Add("items", "NOT_FOUND", "hizmet tanımı veya kategori bulunamadı")
	return ve
}

// itemFieldError names one element of a request array in a validation error.
func itemFieldError(index int, field, code, message string) error {
	ve := &domain.ValidationError{}
	ve.Add("items["+strconv.Itoa(index)+"]."+field, code, message)
	return ve
}
