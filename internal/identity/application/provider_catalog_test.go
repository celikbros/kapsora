package application_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/db/migrations"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
)

func TestProviderCatalogAccessForNewAndUpgradedTenants(t *testing.T) {
	f := newAuthzFixture(t)
	ctx, cancel := f.h.Ctx()
	defer cancel()
	actor := f.h.CreateActor("catalog-provider", "Catalog Provider")
	hospital := f.h.CreateTenantOrganization(f.tenantA, "Synthetic Hospital", "PROVIDER")
	_, err := f.prov.GrantRole(ctx, application.GrantRoleInput{
		TenantID: f.tenantA, ActorID: actor, RoleCode: "PROVIDER_STAFF",
		ScopeType: application.ScopeOrganization, ScopeID: uuid.NullUUID{UUID: hospital, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(app identity.App) identity.RequestContext {
		t.Helper()
		rc, err := f.authz.ResolveTenantContext(ctx, sessionFor(actor, f.tenantA), f.tenantA, app, "catalog-provider")
		if err != nil {
			t.Fatal(err)
		}
		return rc
	}
	if !resolve(identity.AppProvider).Has("catalog.read") {
		t.Fatal("new provider staff cannot read services")
	}
	// Simulate existing tenants before the upgrade; preserve every other role's grants.
	f.h.AdminExec(`DELETE FROM iam.role_permission p USING iam.role r
        WHERE p.tenant_id=r.tenant_id AND p.role_id=r.id
          AND r.code='PROVIDER_STAFF' AND p.permission_code='catalog.read'`)
	if resolve(identity.AppProvider).Has("catalog.read") {
		t.Fatal("removed grant remains effective")
	}
	custom := f.h.CreateTenant("CUSTOM_CATALOG")
	f.h.AdminExec(`INSERT INTO iam.role (tenant_id, code, name, is_system_role)
        VALUES ($1, 'PROVIDER_STAFF', 'Custom provider', false)`, custom)
	var before int
	countOther := `SELECT count(*) FROM iam.role_permission p JOIN iam.role r
        ON p.tenant_id=r.tenant_id AND p.role_id=r.id WHERE r.code<>'PROVIDER_STAFF'`
	if err := f.h.Admin.QueryRow(ctx, countOther).Scan(&before); err != nil {
		t.Fatal(err)
	}
	upgrade, err := migrations.FS.ReadFile("000050_provider_staff_catalog_read.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := f.h.Admin.Exec(ctx, string(upgrade)); err != nil {
			t.Fatal(err)
		}
	}
	var granted, after int
	if err := f.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_permission p JOIN iam.role r
        ON p.tenant_id=r.tenant_id AND p.role_id=r.id
        WHERE r.code='PROVIDER_STAFF' AND p.permission_code='catalog.read'`).Scan(&granted); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Admin.QueryRow(ctx, countOther).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if granted != 2 || before != after {
		t.Fatalf("staff grants=%d want 2 system roles; other grants %d -> %d", granted, before, after)
	}
	rc := resolve(identity.AppProvider)
	if _, err := identity.Require(identity.WithRequestContext(ctx, rc), "catalog.read"); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"catalog.manage", "organization.manage", "claim.create", "claim.submit", "health.medical_report.review"} {
		if rc.Has(forbidden) {
			t.Errorf("staff unexpectedly holds %s", forbidden)
		}
	}
	if resolve(identity.AppBackoffice).Has("catalog.read") || resolve(identity.AppMember).Has("catalog.read") {
		t.Fatal("provider catalog grant leaked into another app")
	}
	if _, err := f.authz.ResolveTenantContext(ctx, sessionFor(actor, f.tenantB), f.tenantB, identity.AppProvider, "foreign"); !errors.Is(err, application.ErrNoMembership) {
		t.Fatalf("foreign tenant access: %v", err)
	}
	synced, err := f.prov.SyncSystemRoles(ctx, f.tenantA)
	if err != nil || synced.Changed() {
		t.Fatalf("sync after migration changed grants: %+v %v", synced, err)
	}
}
