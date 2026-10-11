package application_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/db/migrations"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
)

func TestProgramManagerNoShowReviewGrantAndMigration(t *testing.T) {
	f := newAuthzFixture(t)
	ctx, cancel := f.h.Ctx()
	defer cancel()

	manager, ok := application.RoleTemplateByCode("PROGRAM_MANAGER")
	if !ok || manager.Scope != application.ScopeTenant {
		t.Fatalf("manager template scope = %q, want TENANT", manager.Scope)
	}
	actor := f.h.CreateActor("payer-noshow-review", "Payer NoShow Review")
	_, err := f.prov.GrantRole(ctx, application.GrantRoleInput{
		TenantID: f.tenantA, ActorID: actor, RoleCode: "PROGRAM_MANAGER",
		ScopeType: application.ScopeTenant,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(app identity.App) identity.RequestContext {
		t.Helper()
		rc, err := f.authz.ResolveTenantContext(ctx, sessionFor(actor, f.tenantA), f.tenantA, app, "payer-noshow-review")
		if err != nil {
			t.Fatal(err)
		}
		return rc
	}
	assertAllowed := func() {
		t.Helper()
		rc := resolve(identity.AppBackoffice)
		for _, code := range []string{"accommodation.property.read", "accommodation.no_show.review"} {
			if !rc.Has(code) {
				t.Errorf("manager role lacks %s", code)
			}
		}
		for _, code := range []string{"accommodation.booking.manage", "accommodation.inventory.manage", "document.link", "health.clinical.read", "health.sensitive.read", "health.medical_report.manage"} {
			if rc.Has(code) {
				t.Errorf("manager role unexpectedly holds %s", code)
			}
		}
		// The resolver omits TENANT grants from the narrowing scopes.
		if len(rc.Scopes) != 0 {
			t.Fatalf("manager scopes = %+v, want tenant scope", rc.Scopes)
		}
		for _, app := range []identity.App{identity.AppProvider, identity.AppMember} {
			if resolve(app).Has("accommodation.no_show.review") {
				t.Fatalf("payer review grant leaked into %s", app)
			}
		}
	}
	assertAllowed()

	// Reproduce two pre-upgrade system roles and a same-code custom role.
	for _, tenant := range []uuid.UUID{f.tenantA, f.tenantB} {
		f.h.AdminExec(`DELETE FROM iam.role_permission p USING iam.role r
            WHERE p.tenant_id = r.tenant_id AND p.role_id = r.id
              AND r.tenant_id = $1 AND r.code = 'PROGRAM_MANAGER'
              AND p.permission_code = 'accommodation.no_show.review'`, tenant)
	}
	if resolve(identity.AppBackoffice).Has("accommodation.no_show.review") {
		t.Fatal("removed database grant remained effective in a fresh request")
	}
	custom := f.h.CreateTenant("CUSTOM_PAYER_NOSHOW")
	f.h.AdminExec(`INSERT INTO iam.role (tenant_id, code, name, is_system_role)
        VALUES ($1, 'PROGRAM_MANAGER', 'Custom manager role', false)`, custom)

	upgrade, err := migrations.FS.ReadFile("000055_program_manager_no_show_review.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := f.h.Admin.Exec(ctx, string(upgrade)); err != nil {
			t.Fatal(err)
		}
	}
	var systemGrants, customGrants int
	if err := f.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_permission p JOIN iam.role r
        ON p.tenant_id = r.tenant_id AND p.role_id = r.id
        WHERE r.tenant_id IN ($1, $2) AND r.code = 'PROGRAM_MANAGER'
          AND r.is_system_role AND p.permission_code = 'accommodation.no_show.review'`, f.tenantA, f.tenantB).Scan(&systemGrants); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_permission p JOIN iam.role r
        ON p.tenant_id = r.tenant_id AND p.role_id = r.id
        WHERE r.tenant_id = $1 AND r.code = 'PROGRAM_MANAGER'
          AND p.permission_code = 'accommodation.no_show.review'`, custom).Scan(&customGrants); err != nil {
		t.Fatal(err)
	}
	if systemGrants != 2 || customGrants != 0 {
		t.Fatalf("system review grants = %d, custom = %d; want 2 and 0", systemGrants, customGrants)
	}
	assertAllowed()
	synced, err := f.prov.SyncSystemRoles(ctx, f.tenantA)
	if err != nil || synced.Changed() {
		t.Fatalf("sync after migration = %+v, %v", synced, err)
	}
}
