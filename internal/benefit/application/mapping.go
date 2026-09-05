package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// PermissionMappingManage guards writing the service → entitlement mapping of a plan
// version. It is granted with plan.manage, because the mapping is part of the plan's
// configuration rather than a thing of its own: somebody who may write the entitlements
// may say which service draws from them.
const PermissionMappingManage = "entitlement.mapping.manage"

// MaxMappings bounds one replace. A plan version with more than this many mapped services
// is a catalogue, not a plan, and the limit is the contract's maxItems.
const MaxMappings = 500

// Mapping is one service → entitlement mapping as it is read back: the two ids the row
// holds, and the two codes a person reads it by.
type Mapping struct {
	ID                      uuid.UUID
	PlanVersionID           uuid.UUID
	ServiceDefinitionID     uuid.UUID
	ServiceCode             string
	ServiceName             string
	EntitlementDefinitionID uuid.UUID
	EntitlementCode         string
	UnitType                string
	UnitFactor              string
	ValidFrom               *time.Time
	ValidTo                 *time.Time
	RowVersion              int64
}

// MappingInput is one line of a replace. The entitlement is named by its code rather than
// by its id: a code is what the plan's author wrote and what the balance is reported
// under, and an id of a definition inside a draft version is not something anybody types.
type MappingInput struct {
	ServiceDefinitionID uuid.UUID
	EntitlementCode     string
	// UnitFactor is an exact decimal; empty means 1.
	UnitFactor string
	ValidFrom  *time.Time
	ValidTo    *time.Time
}

// MappingRow is one benefit.service_entitlement_mapping row as stored.
type MappingRow struct {
	ID                      uuid.UUID
	PlanVersionID           uuid.UUID
	ServiceDefinitionID     uuid.UUID
	ServiceCode             string
	ServiceName             string
	EntitlementDefinitionID uuid.UUID
	EntitlementCode         string
	UnitType                string
	UnitFactor              string
	ValidFrom               *time.Time
	ValidTo                 *time.Time
	RowVersion              int64
}

// NewMappingRow is the insert payload.
type NewMappingRow struct {
	TenantID                uuid.UUID
	PlanVersionID           uuid.UUID
	ServiceDefinitionID     uuid.UUID
	EntitlementDefinitionID uuid.UUID
	UnitFactor              string
	ValidFrom               *time.Time
	ValidTo                 *time.Time
	ActorID                 uuid.UUID
}

// ServiceDefinitionRow is the catalogue row a mapping names.
type ServiceDefinitionRow struct {
	ID     uuid.UUID
	Code   string
	Name   string
	Active bool
}

// ListMappings returns the mapping set of one plan version.
func (s *Service) ListMappings(ctx context.Context, rc identity.RequestContext,
	versionID uuid.UUID,
) ([]Mapping, error) {
	var out []Mapping
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetPlanVersion(ctx, tx, rc.TenantID, versionID); err != nil {
			return err
		}
		rows, err := s.repo.ListMappings(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		out = mappingViews(rows)
		return nil
	})
	return out, err
}

// ReplaceMappings rewrites the whole mapping set of a DRAFT version.
//
// A replace rather than a merge, for the reason every other set replacement in this
// codebase is one: a merge leaves behind the mapping the caller believed they had
// removed, and a mapping nobody meant to keep is a balance being spent by mistake.
//
// The published-version rule is not restated here. The trigger of migration 000035
// refuses every write to a mapping of a non-draft version, and lockDraft refuses the
// command before it gets there; the two together mean the rule survives both a bug in
// this function and a hand-written INSERT.
func (s *Service) ReplaceMappings(ctx context.Context, rc identity.RequestContext,
	versionID uuid.UUID, items []MappingInput, expectedVersion int64,
) ([]Mapping, error) {
	normalized, err := normalizeMappings(items)
	if err != nil {
		return nil, err
	}

	var out []Mapping
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, expectedVersion)
		if err != nil {
			return err
		}
		definitions, err := s.repo.ListMappableDefinitions(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		byCode := make(map[string]uuid.UUID, len(definitions))
		for _, d := range definitions {
			byCode[d.Code] = d.ID
		}
		services, err := s.repo.ListServiceDefinitionsByID(ctx, tx, rc.TenantID, serviceIDs(normalized))
		if err != nil {
			return err
		}
		byID := make(map[uuid.UUID]ServiceDefinitionRow, len(services))
		for _, d := range services {
			byID[d.ID] = d
		}

		ve := &domain.ValidationError{}
		for i, item := range normalized {
			field := fmt.Sprintf("items[%d]", i)
			if _, ok := byCode[item.EntitlementCode]; !ok {
				ve.Add(field+".entitlementCode", "UNKNOWN",
					"bu hak kodu plan sürümünde tanımlı değil")
			}
			definition, ok := byID[item.ServiceDefinitionID]
			switch {
			case !ok:
				ve.Add(field+".serviceDefinitionId", "UNKNOWN", "hizmet tanımı bulunamadı")
			case !definition.Active:
				ve.Add(field+".serviceDefinitionId", "INACTIVE", "hizmet tanımı pasif")
			}
		}
		if ve.Len() > 0 {
			return ve
		}

		if _, err := s.repo.DeleteMappings(ctx, tx, rc.TenantID, versionID); err != nil {
			return err
		}
		for _, item := range normalized {
			if _, err := s.repo.CreateMapping(ctx, tx, NewMappingRow{
				TenantID: rc.TenantID, PlanVersionID: versionID,
				ServiceDefinitionID:     item.ServiceDefinitionID,
				EntitlementDefinitionID: byCode[item.EntitlementCode],
				UnitFactor:              item.UnitFactor,
				ValidFrom:               item.ValidFrom, ValidTo: item.ValidTo,
				ActorID: rc.Principal.ActorID,
			}); err != nil {
				return err
			}
		}
		// The mappings are child rows; touching the version moves its ETag too, so a
		// caller holding the old one cannot follow this write with a stale command.
		if err := s.repo.TouchPlanVersion(ctx, tx, rc.TenantID, versionID); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "plan_version.mappings.replace", "plan_version", versionID,
			map[string]any{
				"plan_id": current.PlanID, "version_no": current.VersionNo,
				"mapping_count": len(normalized),
			}); err != nil {
			return err
		}
		rows, err := s.repo.ListMappings(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		out = mappingViews(rows)
		return nil
	})
	return out, err
}

// normalizeMappings validates the submitted set without touching the database: the shape
// of a code, the exactness of a factor, the order of a period and the absence of a
// duplicate service. Everything that needs the version's own data is checked inside the
// transaction, where the version is locked.
func normalizeMappings(items []MappingInput) ([]MappingInput, error) {
	ve := &domain.ValidationError{}
	if len(items) > MaxMappings {
		ve.Add("items", "RANGE", fmt.Sprintf("en fazla %d eşleşme gönderilebilir", MaxMappings))
		return nil, ve.OrNil()
	}
	out := make([]MappingInput, 0, len(items))
	seen := make(map[uuid.UUID]int, len(items))
	for i, item := range items {
		field := fmt.Sprintf("items[%d]", i)
		next := MappingInput{
			ServiceDefinitionID: item.ServiceDefinitionID,
			EntitlementCode:     strings.ToUpper(strings.TrimSpace(item.EntitlementCode)),
			UnitFactor:          strings.TrimSpace(item.UnitFactor),
			ValidFrom:           datePtr(item.ValidFrom), ValidTo: datePtr(item.ValidTo),
		}
		if item.ServiceDefinitionID == uuid.Nil {
			ve.Add(field+".serviceDefinitionId", "REQUIRED", "hizmet tanımı zorunlu")
		} else if first, dup := seen[item.ServiceDefinitionID]; dup {
			ve.Add(field+".serviceDefinitionId", "DUPLICATE",
				fmt.Sprintf("bu hizmet %d. satırda zaten eşlendi", first))
		} else {
			seen[item.ServiceDefinitionID] = i
		}
		if next.EntitlementCode == "" {
			ve.Add(field+".entitlementCode", "REQUIRED", "hak kodu zorunlu")
		}
		if next.UnitFactor == "" {
			next.UnitFactor = "1"
		}
		factor, err := domain.ParseQuantity(next.UnitFactor)
		switch {
		case err != nil:
			ve.Add(field+".unitFactor", "FORMAT", "kesin ondalık bir sayı olmalı")
		case !factor.IsPositive():
			ve.Add(field+".unitFactor", "RANGE", "birim katsayısı sıfırdan büyük olmalı")
		default:
			next.UnitFactor = factor.String()
		}
		domain.ValidatePeriod(ve, field+".valid", next.ValidFrom, next.ValidTo)
		out = append(out, next)
	}
	return out, ve.OrNil()
}

func serviceIDs(items []MappingInput) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(items))
	seen := make(map[uuid.UUID]bool, len(items))
	for _, item := range items {
		if item.ServiceDefinitionID == uuid.Nil || seen[item.ServiceDefinitionID] {
			continue
		}
		seen[item.ServiceDefinitionID] = true
		out = append(out, item.ServiceDefinitionID)
	}
	return out
}

func mappingViews(rows []MappingRow) []Mapping {
	out := make([]Mapping, 0, len(rows))
	for _, r := range rows {
		out = append(out, Mapping(r))
	}
	return out
}
