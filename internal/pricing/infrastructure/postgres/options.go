package pricingpg

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	"github.com/celikbros/kapsora/internal/pricing/application"
)

func (Repository) ListProviderOptions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q application.OptionQuery) ([]application.ProviderOptionRecord, error) {
	active := "ACTIVE"
	params := sqlcgen.ListProviderProfilesParams{
		TenantID: tenantID, ScopeIds: q.OrganizationIDs, Status: &active, PageSize: q.PageSize,
	}
	if q.Pattern != "" {
		params.Q = &q.Pattern
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListProviderProfiles(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("pricing: list provider options: %w", err)
	}
	out := make([]application.ProviderOptionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.ProviderOptionRecord{ProviderProfileID: row.ID, OrganizationName: row.OrganizationName, CreatedAt: row.CreatedAt})
	}
	return out, nil
}

func (Repository) ListServiceOptions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q application.OptionQuery) ([]application.ServiceOptionRecord, error) {
	active := true
	params := sqlcgen.ListServiceDefinitionsParams{TenantID: tenantID, Active: &active, PageSize: q.PageSize}
	if q.Pattern != "" {
		params.Q = &q.Pattern
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListServiceDefinitions(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("pricing: list service options: %w", err)
	}
	out := make([]application.ServiceOptionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.ServiceOptionRecord{ServiceDefinitionID: row.ID, Code: row.Code, Name: row.Name, CreatedAt: row.CreatedAt})
	}
	return out, nil
}
