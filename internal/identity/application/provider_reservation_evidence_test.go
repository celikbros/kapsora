package application_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/db/migrations"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
)

func TestProviderReservationEvidenceGrantAndMigration(t *testing.T) {
	f := newAuthzFixture(t)
	ctx, cancel := f.h.Ctx()
	defer cancel()

	reservation, ok := application.RoleTemplateByCode("PROVIDER_RESERVATION")
	if !ok || reservation.Scope != application.ScopeOrganization {
		t.Fatalf("reservation template scope = %q, want ORGANIZATION", reservation.Scope)
	}
	actor := f.h.CreateActor("reservation-evidence", "Reservation Evidence")
	provider := f.h.CreateTenantOrganization(f.tenantA, "Hotel Provider", "PROVIDER")
	_, err := f.prov.GrantRole(ctx, application.GrantRoleInput{
		TenantID: f.tenantA, ActorID: actor, RoleCode: "PROVIDER_RESERVATION",
		ScopeType: application.ScopeOrganization,
		ScopeID:   uuid.NullUUID{UUID: provider, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(app identity.App) identity.RequestContext {
		t.Helper()
		rc, err := f.authz.ResolveTenantContext(ctx, sessionFor(actor, f.tenantA), f.tenantA, app, "reservation-evidence")
		if err != nil {
			t.Fatal(err)
		}
		return rc
	}
	assertAllowed := func() {
		t.Helper()
		rc := resolve(identity.AppProvider)
		for _, code := range []string{"accommodation.booking.manage", "document.upload", "document.read", "document.booking_evidence.link"} {
			if !rc.Has(code) {
				t.Errorf("reservation role lacks %s", code)
			}
		}
		for _, code := range []string{"document.link", "health.clinical.read", "health.sensitive.read", "health.medical_report.manage", "document.legal_hold.manage"} {
			if rc.Has(code) {
				t.Errorf("reservation role unexpectedly holds %s", code)
			}
		}
		if len(rc.Scopes) != 1 || rc.Scopes[0].Type != application.ScopeOrganization || !rc.Scopes[0].ID.Valid || rc.Scopes[0].ID.UUID != provider {
			t.Fatalf("reservation scopes = %+v, want only its provider", rc.Scopes)
		}
		if resolve(identity.AppBackoffice).Has("document.booking_evidence.link") {
			t.Fatal("provider evidence grant leaked into backoffice")
		}
	}
	assertAllowed()

	// Reproduce two pre-upgrade system roles and a same-code custom role.
	for _, tenant := range []uuid.UUID{f.tenantA, f.tenantB} {
		f.h.AdminExec(`DELETE FROM iam.role_permission p USING iam.role r
            WHERE p.tenant_id = r.tenant_id AND p.role_id = r.id
              AND r.tenant_id = $1 AND r.code = 'PROVIDER_RESERVATION'
              AND p.permission_code = 'document.booking_evidence.link'`, tenant)
	}
	if resolve(identity.AppProvider).Has("document.booking_evidence.link") {
		t.Fatal("removed database grant remained effective in a fresh request")
	}
	custom := f.h.CreateTenant("CUSTOM_RESERVATION_EVIDENCE")
	f.h.AdminExec(`INSERT INTO iam.role (tenant_id, code, name, is_system_role)
        VALUES ($1, 'PROVIDER_RESERVATION', 'Custom reservation role', false)`, custom)

	upgrade, err := migrations.FS.ReadFile("000054_provider_reservation_booking_evidence.up.sql")
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
        WHERE r.tenant_id IN ($1, $2) AND r.code = 'PROVIDER_RESERVATION'
          AND r.is_system_role AND p.permission_code = 'document.booking_evidence.link'`, f.tenantA, f.tenantB).Scan(&systemGrants); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_permission p JOIN iam.role r
        ON p.tenant_id = r.tenant_id AND p.role_id = r.id
        WHERE r.tenant_id = $1 AND r.code = 'PROVIDER_RESERVATION'
          AND p.permission_code = 'document.booking_evidence.link'`, custom).Scan(&customGrants); err != nil {
		t.Fatal(err)
	}
	if systemGrants != 2 || customGrants != 0 {
		t.Fatalf("system evidence grants = %d, custom = %d; want 2 and 0", systemGrants, customGrants)
	}
	assertAllowed()
	synced, err := f.prov.SyncSystemRoles(ctx, f.tenantA)
	if err != nil || synced.Changed() {
		t.Fatalf("sync after migration = %+v, %v", synced, err)
	}
}
