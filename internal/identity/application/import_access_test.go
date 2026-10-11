package application_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/celikbros/kapsora/db/migrations"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
)

func TestImportGrantBelongsToProgramManager(t *testing.T) {
	var owners []string
	for _, role := range application.RoleTemplates() {
		if slices.Contains(role.Permissions, "import.execute") {
			owners = append(owners, role.Code)
		}
	}
	if !slices.Equal(owners, []string{"PROGRAM_MANAGER"}) {
		t.Fatalf("import owners = %v, want PROGRAM_MANAGER only", owners)
	}
}

func TestImportAccessForNewAndUpgradedTenants(t *testing.T) {
	f := newAuthzFixture(t)
	ctx, cancel := f.h.Ctx()
	defer cancel()
	actor := f.h.CreateActor("import-manager", "Import Manager")
	f.grant(t, f.tenantA, actor, "PROGRAM_MANAGER")
	resolve := func(app identity.App) identity.RequestContext {
		t.Helper()
		rc, err := f.authz.ResolveTenantContext(ctx, sessionFor(actor, f.tenantA), f.tenantA, app, "import-test")
		if err != nil {
			t.Fatal(err)
		}
		return rc
	}
	if !resolve(identity.AppBackoffice).Has("import.execute") {
		t.Fatal("newly provisioned manager cannot import")
	}
	// Simulate two pre-upgrade tenants, including one with no active manager account.
	f.h.AdminExec(`DELETE FROM iam.role_permission WHERE permission_code = 'import.execute'`)
	if resolve(identity.AppBackoffice).Has("import.execute") {
		t.Fatal("removed grant remains effective")
	}
	customTenant := f.h.CreateTenant("IMPORT_CUSTOM")
	f.h.AdminExec(`INSERT INTO iam.role (tenant_id, code, name, is_system_role)
        VALUES ($1, 'PROGRAM_MANAGER', 'Custom manager', false)`, customTenant)
	upgrade, err := migrations.FS.ReadFile("000049_program_manager_import.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := f.h.Admin.Exec(ctx, string(upgrade)); err != nil {
			t.Fatal(err)
		}
	}
	var grants int
	if err := f.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_permission
        WHERE permission_code = 'import.execute'`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 2 {
		t.Fatalf("upgraded grants = %d, want 2 system roles", grants)
	}
	rc := resolve(identity.AppBackoffice)
	if !rc.Has("import.execute") {
		t.Fatal("existing manager cannot import after upgrade")
	}
	if _, err := identity.RequireStepUp(identity.WithRequestContext(ctx, rc), "import.execute"); !errors.Is(err, identity.ErrStepUpRequired) {
		t.Fatalf("import without step-up: %v", err)
	}
	stepped := sessionFor(actor, f.tenantA)
	stepped.StepUpUntil = time.Now().Add(time.Minute)
	rc, err = f.authz.ResolveTenantContext(ctx, stepped, f.tenantA, identity.AppBackoffice, "import-step-up")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.RequireStepUp(identity.WithRequestContext(ctx, rc), "import.execute"); err != nil {
		t.Fatal(err)
	}
	if resolve(identity.AppProvider).Has("import.execute") || resolve(identity.AppMember).Has("import.execute") {
		t.Fatal("backoffice import grant leaked into another app")
	}
	if _, err := f.authz.ResolveTenantContext(ctx, sessionFor(actor, f.tenantB), f.tenantB, identity.AppBackoffice, "foreign"); !errors.Is(err, application.ErrNoMembership) {
		t.Fatalf("foreign tenant access: %v", err)
	}
	for _, role := range []string{"TENANT_ADMIN", "SPONSOR_HR", "AUDITOR"} {
		other := f.h.CreateActor("import-denied-"+role, "Denied Import")
		f.grant(t, f.tenantA, other, role)
		denied, err := f.authz.ResolveTenantContext(ctx, sessionFor(other, f.tenantA), f.tenantA, identity.AppBackoffice, "denied")
		if err != nil {
			t.Fatal(err)
		}
		if denied.Has("import.execute") {
			t.Fatalf("%s unexpectedly imports", role)
		}
	}
	synced, err := f.prov.SyncSystemRoles(ctx, f.tenantA)
	if err != nil || synced.Changed() {
		t.Fatalf("sync after upgrade: %+v, %v", synced, err)
	}
}
