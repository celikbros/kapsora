package benefitpg

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// integrityConstraintTrigger is the SQLSTATE the guard trigger of migration 000035 raises
// when a write reaches the mappings of a version that is no longer a draft. The
// application checks the same thing first; this is what answers when somebody reaches the
// table another way.
const integrityConstraintTrigger = "23000"

// ListMappings implements application.Repository.
func (Repository) ListMappings(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]application.MappingRow, error) {
	rows, err := sqlcgen.New(tx).ListServiceEntitlementMappings(ctx, sqlcgen.ListServiceEntitlementMappingsParams{
		TenantID: tenantID, PlanVersionID: versionID,
	})
	if err != nil {
		return nil, fmt.Errorf("benefit: list entitlement mappings: %w", err)
	}
	out := make([]application.MappingRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.MappingRow{
			ID: r.ID, PlanVersionID: r.PlanVersionID,
			ServiceDefinitionID: r.ServiceDefinitionID, ServiceCode: r.ServiceCode,
			ServiceName:             r.ServiceName,
			EntitlementDefinitionID: r.EntitlementDefinitionID,
			EntitlementCode:         r.EntitlementCode, UnitType: r.UnitType,
			UnitFactor: domain.TrimDecimal(r.UnitFactor),
			ValidFrom:  datePtr(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
			RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// DeleteMappings implements application.Repository.
func (Repository) DeleteMappings(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (int64, error) {
	n, err := sqlcgen.New(tx).DeleteServiceEntitlementMappings(ctx, sqlcgen.DeleteServiceEntitlementMappingsParams{
		TenantID: tenantID, PlanVersionID: versionID,
	})
	if err != nil {
		return 0, mappingError("delete entitlement mappings", err)
	}
	return n, nil
}

// CreateMapping implements application.Repository.
func (Repository) CreateMapping(ctx context.Context, tx pgx.Tx, in application.NewMappingRow) (uuid.UUID, error) {
	id, err := sqlcgen.New(tx).CreateServiceEntitlementMapping(ctx, sqlcgen.CreateServiceEntitlementMappingParams{
		TenantID: in.TenantID, PlanVersionID: in.PlanVersionID,
		ServiceDefinitionID:     in.ServiceDefinitionID,
		EntitlementDefinitionID: in.EntitlementDefinitionID,
		UnitFactor:              in.UnitFactor,
		ValidFrom:               date(in.ValidFrom), ValidTo: date(in.ValidTo),
		ActorID: nullUUID(in.ActorID),
	})
	if err != nil {
		return uuid.Nil, mappingError("create entitlement mapping", err)
	}
	return id, nil
}

// ListMappableDefinitions implements application.Repository.
func (Repository) ListMappableDefinitions(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]application.DefinitionCode, error) {
	rows, err := sqlcgen.New(tx).ListEntitlementDefinitionsForMapping(ctx,
		sqlcgen.ListEntitlementDefinitionsForMappingParams{TenantID: tenantID, PlanVersionID: versionID})
	if err != nil {
		return nil, fmt.Errorf("benefit: list mappable definitions: %w", err)
	}
	out := make([]application.DefinitionCode, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.DefinitionCode{ID: r.ID, Code: r.Code, UnitType: r.UnitType})
	}
	return out, nil
}

// ListServiceDefinitionsByID implements application.Repository.
func (Repository) ListServiceDefinitionsByID(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	ids []uuid.UUID,
) ([]application.ServiceDefinitionRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := sqlcgen.New(tx).ListActiveServiceDefinitionsByID(ctx,
		sqlcgen.ListActiveServiceDefinitionsByIDParams{TenantID: tenantID, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("benefit: list service definitions: %w", err)
	}
	out := make([]application.ServiceDefinitionRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.ServiceDefinitionRow{
			ID: r.ID, Code: r.Code, Name: r.Name, Active: r.Active,
		})
	}
	return out, nil
}

// mappingError turns the guard trigger's refusal into the named error the transport maps
// to 409, so a write that reached the table around the application check still answers
// "this version is published" rather than "internal error".
func mappingError(what string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == integrityConstraintTrigger {
		return application.ErrVersionImmutable
	}
	return fmt.Errorf("benefit: %s: %w", what, err)
}
