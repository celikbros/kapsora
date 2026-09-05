package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/benefit/application"
)

// seedServiceDefinition writes one catalogue row a mapping can point at, with a category
// above it because catalog.service_definition needs one.
func (f *fixture) seedServiceDefinition(t *testing.T, tenant uuid.UUID, code string, active bool) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var categoryID uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'MAPPING_CAT', 'Eşleşme', 'HEALTH')
		ON CONFLICT (tenant_id, code) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`, tenant).Scan(&categoryID); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	var id uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type, active)
		VALUES ($1, $2, $3, $3, 'APPOINTMENT', 'COUNT', $4) RETURNING id`,
		tenant, categoryID, code, active).Scan(&id); err != nil {
		t.Fatalf("seed service definition %s: %v", code, err)
	}
	return id
}

// TestMappingsAreWrittenOnADraftAndFrozenOncePublished is the rule the table lives inside:
// a mapping is part of a plan version, and a published plan version is immutable (WP-I2-02,
// restated by WP-I5-05 section 2.1).
//
// The refusal is asserted twice, at both layers that enforce it. ReplaceMappings refuses
// because the version is not a draft; the trigger of migration 000035 refuses a write that
// reaches the table another way. Either alone would be a rule with one place to forget it.
func TestMappingsAreWrittenOnADraftAndFrozenOncePublished(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	maker, checker := f.rc(f.tenantA, f.maker), f.rc(f.tenantA, f.checker)
	maker.Permissions[application.PermissionMappingManage] = struct{}{}
	_, planID := f.activePlan(t, "MAP")
	physio := f.seedServiceDefinition(t, f.tenantA, "MAP_PHYSIO", true)

	version, err := f.svc.CreatePlanVersion(ctx, maker, planID, application.NewPlanVersionInput{
		ValidFrom: dayPtr(t, "2026-01-01"),
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	version, err = f.svc.ReplaceDefinitions(ctx, maker, version.ID, sampleDefinitions(), version.RowVersion)
	if err != nil {
		t.Fatalf("replace definitions: %v", err)
	}

	mappings, err := f.svc.ReplaceMappings(ctx, maker, version.ID, []application.MappingInput{
		{ServiceDefinitionID: physio, EntitlementCode: "CHECKUP", UnitFactor: "2"},
	}, version.RowVersion)
	if err != nil {
		t.Fatalf("replace mappings: %v", err)
	}
	if len(mappings) != 1 {
		t.Fatalf("mappings = %d, want 1", len(mappings))
	}
	if mappings[0].EntitlementCode != "CHECKUP" || mappings[0].ServiceCode != "MAP_PHYSIO" {
		t.Fatalf("mapping = %+v", mappings[0])
	}
	if mappings[0].UnitFactor != "2" {
		t.Fatalf("unitFactor = %q, want the exact decimal 2", mappings[0].UnitFactor)
	}

	// The child write moved the version's ETag, so the caller's old one is stale.
	current, err := f.svc.GetPlanVersion(ctx, maker, version.ID)
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	if current.RowVersion == version.RowVersion {
		t.Fatal("replacing the mappings left the version's row_version where it was")
	}

	if _, err := f.svc.SubmitPlanVersion(ctx, maker, version.ID, nil, current.RowVersion); err != nil {
		t.Fatalf("submit: %v", err)
	}
	current, err = f.svc.GetPlanVersion(ctx, maker, version.ID)
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	published, err := f.svc.PublishPlanVersion(ctx, checker, version.ID, nil, current.RowVersion)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Layer one: the command refuses.
	_, err = f.svc.ReplaceMappings(ctx, maker, version.ID, []application.MappingInput{
		{ServiceDefinitionID: physio, EntitlementCode: "DENTAL"},
	}, published.RowVersion)
	if !errors.Is(err, application.ErrVersionImmutable) {
		t.Fatalf("replace on a published version = %v, want ErrVersionImmutable", err)
	}

	// Layer two: the table refuses, whatever the application did or did not check.
	ctxDB, cancel := f.h.Ctx()
	defer cancel()
	if _, err := f.h.Admin.Exec(ctxDB, `
		UPDATE benefit.service_entitlement_mapping SET unit_factor = 9
		 WHERE tenant_id = $1 AND plan_version_id = $2`, f.tenantA, version.ID); err == nil {
		t.Fatal("the guard trigger allowed a mapping of a published version to be changed")
	}
	if _, err := f.h.Admin.Exec(ctxDB, `
		DELETE FROM benefit.service_entitlement_mapping
		 WHERE tenant_id = $1 AND plan_version_id = $2`, f.tenantA, version.ID); err == nil {
		t.Fatal("the guard trigger allowed a mapping of a published version to be deleted")
	}

	// And the set survived both attempts, which is what "immutable" has to mean.
	after, err := f.svc.ListMappings(ctx, maker, version.ID)
	if err != nil {
		t.Fatalf("list mappings: %v", err)
	}
	if len(after) != 1 || after[0].UnitFactor != "2" {
		t.Fatalf("mappings after the refused writes = %+v", after)
	}
}

// TestMappingRefusesACodeOrServiceThatIsNotThere: every way of naming something that does
// not belong to this version is a field error rather than a foreign key violation or a
// silently dropped line.
func TestMappingRefusesACodeOrServiceThatIsNotThere(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	maker := f.rc(f.tenantA, f.maker)
	maker.Permissions[application.PermissionMappingManage] = struct{}{}
	_, planID := f.activePlan(t, "MAPBAD")
	physio := f.seedServiceDefinition(t, f.tenantA, "MAPBAD_PHYSIO", true)
	retired := f.seedServiceDefinition(t, f.tenantA, "MAPBAD_RETIRED", false)

	version, err := f.svc.CreatePlanVersion(ctx, maker, planID, application.NewPlanVersionInput{
		ValidFrom: dayPtr(t, "2026-01-01"),
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	version, err = f.svc.ReplaceDefinitions(ctx, maker, version.ID, sampleDefinitions(), version.RowVersion)
	if err != nil {
		t.Fatalf("replace definitions: %v", err)
	}

	for name, items := range map[string][]application.MappingInput{
		"an entitlement code this version does not define": {
			{ServiceDefinitionID: physio, EntitlementCode: "NOT_HERE"},
		},
		"a service definition that does not exist": {
			{ServiceDefinitionID: uuid.New(), EntitlementCode: "DENTAL"},
		},
		"a service definition that is inactive": {
			{ServiceDefinitionID: retired, EntitlementCode: "DENTAL"},
		},
		"one service mapped twice": {
			{ServiceDefinitionID: physio, EntitlementCode: "DENTAL"},
			{ServiceDefinitionID: physio, EntitlementCode: "CHECKUP"},
		},
		"a unit factor that is not a positive decimal": {
			{ServiceDefinitionID: physio, EntitlementCode: "DENTAL", UnitFactor: "0"},
		},
	} {
		if _, err := f.svc.ReplaceMappings(ctx, maker, version.ID, items, version.RowVersion); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}

	// None of the refusals left a row behind: a replace either writes the whole set or
	// nothing at all.
	after, err := f.svc.ListMappings(ctx, maker, version.ID)
	if err != nil {
		t.Fatalf("list mappings: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("mappings after five refusals = %+v", after)
	}
}
