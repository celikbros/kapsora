package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// The service → entitlement mappings the demo world uses (WP-I5-05 section 2.1). Each
// line is "one unit of this service draws this much of that entitlement", named by codes
// on both sides so it survives every id in the database changing.
//
// The factor is the whole reason the table is not a two-column join: one physiotherapy
// session is one SESSION of the entitlement, but a GP visit is priced in money and draws
// from the money entitlement, and a scan draws more than one unit of it.
var demoMappings = []struct {
	ServiceCode     string
	EntitlementCode string
	UnitFactor      string
}{
	{ServiceCode: "PHYSIO_SESSION", EntitlementCode: "PHYSIO_SESSION", UnitFactor: "1"},
	{ServiceCode: "GP_VISIT", EntitlementCode: "HEALTH_MONEY", UnitFactor: "1"},
	{ServiceCode: "MRI_SCAN", EntitlementCode: "HEALTH_MONEY", UnitFactor: "1"},
}

// ensureEntitlementMappings attaches the demo mappings to every DRAFT plan version the
// tenant has.
//
// Draft versions only, and that is the rule rather than a limitation: a mapping belongs to
// a plan version and a published version is immutable, so the only version whose mapping
// can be written is one nobody is being served by yet. A tenant whose versions are all
// published gets nothing here and is told so, which is the honest answer — the mapping for
// a published plan is set by publishing a new version, not by editing the old one.
//
// It is worth saying plainly what this does *not* do today: `seed demo` builds tenants,
// logins and grants and no programs, plans, catalogue or people at all, so on a fresh demo
// database this step finds no draft versions and maps nothing. It is written against the
// data rather than against a fixture so that it does the right thing the moment a demo
// plan world exists; the mock world under web/packages/api-client carries the same three
// mappings today, which is what makes a mapped service answer "Uygun" on the screens.
func (s *seeder) ensureEntitlementMappings(ctx context.Context, tenantID uuid.UUID) error {
	rc := identity.RequestContext{TenantID: tenantID}
	plan, err := s.readMappingPlan(ctx, tenantID)
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		fmt.Printf("mapping %-22s nothing to map in this tenant\n", "entitlements")
		return nil
	}

	mapped := 0
	for versionID, items := range plan {
		current, err := s.benefits.GetPlanVersion(ctx, rc, versionID)
		if err != nil {
			return fmt.Errorf("read plan version %s: %w", versionID, err)
		}
		if _, err := s.benefits.ReplaceMappings(ctx, rc, versionID, items, current.RowVersion); err != nil {
			return fmt.Errorf("map plan version %s: %w", versionID, err)
		}
		mapped += len(items)
	}
	fmt.Printf("mapping %-22s %d mappings over %d draft version(s)\n", "entitlements", mapped, len(plan))
	return nil
}

// readMappingPlan works out, in one tenant-bound transaction, which mapping applies to
// which draft version. Every read here goes through db.WithTenantTx rather than the bare
// pool: RLS is forced on these tables, and a query outside a bound transaction would
// quietly return nothing at all rather than fail.
func (s *seeder) readMappingPlan(ctx context.Context, tenantID uuid.UUID) (map[uuid.UUID][]benefitapp.MappingInput, error) {
	out := map[uuid.UUID][]benefitapp.MappingInput{}
	err := db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID},
		func(ctx context.Context, tx pgx.Tx) error {
			q := sqlcgen.New(tx)
			versions, err := q.ListDraftPlanVersionsForSeed(ctx, tenantID)
			if err != nil {
				return fmt.Errorf("list draft plan versions: %w", err)
			}
			if len(versions) == 0 {
				return nil
			}
			services, err := serviceDefinitionsByCode(ctx, tx, tenantID)
			if err != nil {
				return err
			}
			for _, version := range versions {
				definitions, err := q.ListEntitlementDefinitionsForMapping(ctx,
					sqlcgen.ListEntitlementDefinitionsForMappingParams{
						TenantID: tenantID, PlanVersionID: version.ID,
					})
				if err != nil {
					return fmt.Errorf("list entitlement definitions: %w", err)
				}
				known := make(map[string]bool, len(definitions))
				for _, d := range definitions {
					known[d.Code] = true
				}
				var items []benefitapp.MappingInput
				for _, m := range demoMappings {
					serviceID, ok := services[m.ServiceCode]
					if !ok || !known[m.EntitlementCode] {
						continue
					}
					items = append(items, benefitapp.MappingInput{
						ServiceDefinitionID: serviceID, EntitlementCode: m.EntitlementCode,
						UnitFactor: m.UnitFactor,
					})
				}
				if len(items) > 0 {
					out[version.ID] = items
				}
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// serviceDefinitionsByCode reads the tenant's catalogue once, keyed by the code the
// mapping table names services by.
func serviceDefinitionsByCode(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (map[string]uuid.UUID, error) {
	rows, err := tx.Query(ctx,
		`SELECT code, id FROM catalog.service_definition WHERE tenant_id = $1 AND active`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list service definitions: %w", err)
	}
	defer rows.Close()
	out := map[string]uuid.UUID{}
	for rows.Next() {
		var code string
		var id uuid.UUID
		if err := rows.Scan(&code, &id); err != nil {
			return nil, fmt.Errorf("scan service definition: %w", err)
		}
		out[code] = id
	}
	return out, rows.Err()
}
