package application_test

import (
	"slices"
	"testing"

	"github.com/celikbros/kapsora/internal/identity/application"
)

// A tenant provisioned before a role or a permission was added to the templates is brought
// up to date by SyncSystemRoles, and a second sync writes nothing.
func TestSyncSystemRolesAddsWhatATenantProvisionedEarlierLacks(t *testing.T) {
	f := newAuthzFixture(t)
	ctx, cancel := f.h.Ctx()
	defer cancel()

	// Make tenant A look like one provisioned by an older build: no SPONSOR_HR at all, and
	// FINANCIAL_REVIEWER one permission short.
	sponsor, ok := application.RoleTemplateByCode("SPONSOR_HR")
	if !ok || len(sponsor.Permissions) == 0 {
		t.Fatal("SPONSOR_HR template missing or without permissions")
	}
	reviewer, ok := application.RoleTemplateByCode("FINANCIAL_REVIEWER")
	if !ok || len(reviewer.Permissions) == 0 {
		t.Fatal("FINANCIAL_REVIEWER template missing or without permissions")
	}
	f.h.AdminExec(`DELETE FROM iam.role_permission rp USING iam.role r
		WHERE rp.role_id = r.id AND r.tenant_id = $1 AND r.code = 'SPONSOR_HR'`, f.tenantA)
	f.h.AdminExec(`DELETE FROM iam.role WHERE tenant_id = $1 AND code = 'SPONSOR_HR'`, f.tenantA)
	f.h.AdminExec(`DELETE FROM iam.role_permission rp USING iam.role r
		WHERE rp.role_id = r.id AND r.tenant_id = $1 AND r.code = 'FINANCIAL_REVIEWER' AND rp.permission_code = $2`,
		f.tenantA, reviewer.Permissions[0])

	res, err := f.prov.SyncSystemRoles(ctx, f.tenantA)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !slices.Equal(res.RolesCreated, []string{"SPONSOR_HR"}) {
		t.Fatalf("roles created = %v, want [SPONSOR_HR]", res.RolesCreated)
	}
	if want := len(sponsor.Permissions) + 1; res.PermissionsAdded != want {
		t.Fatalf("permissions added = %d, want %d", res.PermissionsAdded, want)
	}

	// The grant the demo seed failed on now goes through.
	actor := f.h.CreateActor("sync-sponsor-hr", "Sponsor HR")
	f.grant(t, f.tenantA, actor, "SPONSOR_HR")

	again, err := f.prov.SyncSystemRoles(ctx, f.tenantA)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if again.Changed() {
		t.Fatalf("second sync changed %+v, want nothing", again)
	}
	// Tenant B was never touched and is already in step.
	other, err := f.prov.SyncSystemRoles(ctx, f.tenantB)
	if err != nil {
		t.Fatalf("sync B: %v", err)
	}
	if other.Changed() {
		t.Fatalf("sync of an up-to-date tenant changed %+v", other)
	}
}
