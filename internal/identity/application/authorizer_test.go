package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

type authzFixture struct {
	h       *dbtest.Harness
	prov    *application.Provisioner
	authz   *application.Authorizer
	store   *identitypg.SessionStore
	tenantA uuid.UUID
	tenantB uuid.UUID
}

func newAuthzFixture(t *testing.T) *authzFixture {
	t.Helper()
	h := dbtest.New(t)
	ctx := context.Background()
	prov := application.NewProvisioner(identitypg.NewProvisioningRepository(h.App), nil)
	a, err := prov.ProvisionTenant(ctx, application.ProvisionCommand{Code: "AUTHZ_A", LegalName: "Authz A A.Ş.", DisplayName: "Authz A"})
	if err != nil {
		t.Fatalf("provision A: %v", err)
	}
	b, err := prov.ProvisionTenant(ctx, application.ProvisionCommand{Code: "AUTHZ_B", LegalName: "Authz B A.Ş.", DisplayName: "Authz B"})
	if err != nil {
		t.Fatalf("provision B: %v", err)
	}
	store := identitypg.NewSessionStore(h.App)
	return &authzFixture{
		h: h, prov: prov, store: store, tenantA: a, tenantB: b,
		authz: application.NewAuthorizer(identitypg.NewAuthorizationRepository(h.App), store, nil, nil),
	}
}

func (f *authzFixture) grant(t *testing.T, tenant, actor uuid.UUID, role string) {
	t.Helper()
	if _, err := f.prov.GrantRole(context.Background(), application.GrantRoleInput{TenantID: tenant, ActorID: actor, RoleCode: role}); err != nil {
		t.Fatalf("grant %s: %v", role, err)
	}
}

func sessionFor(actor, active uuid.UUID) identity.Session {
	return identity.Session{ID: "test-session", ActorID: actor, ActiveTenantID: uuid.NullUUID{UUID: active, Valid: active != uuid.Nil}}
}

func TestProvisionTenantCreatesRolesCatalogsAndRefusesDuplicates(t *testing.T) {
	f := newAuthzFixture(t)
	ctx, cancel := f.h.Ctx()
	defer cancel()

	var roles, rolePerms, identTypes, relTypes, memTypes, progTypes int
	var status string
	q := f.h.Admin
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(q.QueryRow(ctx, `SELECT count(*) FROM iam.role WHERE tenant_id = $1 AND is_system_role`, f.tenantA).Scan(&roles))
	must(q.QueryRow(ctx, `SELECT count(*) FROM iam.role_permission WHERE tenant_id = $1`, f.tenantA).Scan(&rolePerms))
	must(q.QueryRow(ctx, `SELECT count(*) FROM party.identifier_type WHERE tenant_id = $1`, f.tenantA).Scan(&identTypes))
	must(q.QueryRow(ctx, `SELECT count(*) FROM party.relationship_type WHERE tenant_id = $1`, f.tenantA).Scan(&relTypes))
	must(q.QueryRow(ctx, `SELECT count(*) FROM party.membership_type WHERE tenant_id = $1`, f.tenantA).Scan(&memTypes))
	must(q.QueryRow(ctx, `SELECT count(*) FROM benefit.program_type WHERE tenant_id = $1`, f.tenantA).Scan(&progTypes))
	must(q.QueryRow(ctx, `SELECT status FROM platform.tenant WHERE id = $1`, f.tenantA).Scan(&status))

	templates := application.RoleTemplates()
	wantPerms := 0
	for _, tpl := range templates {
		wantPerms += len(tpl.Permissions)
	}
	cat := application.DefaultBaselineCatalogs()
	if roles != len(templates) || rolePerms != wantPerms {
		t.Fatalf("roles=%d/%d permissions=%d/%d", roles, len(templates), rolePerms, wantPerms)
	}
	if identTypes != len(cat.IdentifierTypes) || relTypes != len(cat.RelationshipTypes) || memTypes != len(cat.MembershipTypes) || progTypes != len(cat.ProgramTypes) {
		t.Fatalf("catalogs: ident=%d rel=%d mem=%d prog=%d", identTypes, relTypes, memTypes, progTypes)
	}
	if status != "ACTIVE" {
		t.Fatalf("tenant status = %s", status)
	}

	_, err := f.prov.ProvisionTenant(context.Background(), application.ProvisionCommand{Code: "AUTHZ_A", LegalName: "x", DisplayName: "x"})
	if !errors.Is(err, application.ErrTenantCodeExists) {
		t.Fatalf("duplicate code: %v", err)
	}
	if _, err := f.prov.ProvisionTenant(context.Background(), application.ProvisionCommand{Code: "bad code!", LegalName: "x", DisplayName: "x"}); err == nil {
		t.Fatal("invalid code accepted")
	}
}

func TestRoleTemplatesUseOnlyKnownPermissions(t *testing.T) {
	f := newAuthzFixture(t)
	ctx, cancel := f.h.Ctx()
	defer cancel()
	codes, err := sqlcgen.New(f.h.App).ListPermissionCodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, c := range codes {
		known[c] = true
	}
	seenRole := map[string]bool{}
	for _, tpl := range application.RoleTemplates() {
		if seenRole[tpl.Code] {
			t.Errorf("role %s listed twice", tpl.Code)
		}
		seenRole[tpl.Code] = true
		if tpl.Scope != application.ScopeTenant && tpl.Scope != application.ScopeOrganization {
			t.Errorf("role %s has scope %q", tpl.Code, tpl.Scope)
		}
		seenPerm := map[string]bool{}
		for _, p := range tpl.Permissions {
			if !known[p] {
				t.Errorf("role %s references unknown permission %s", tpl.Code, p)
			}
			if seenPerm[p] {
				t.Errorf("role %s lists %s twice", tpl.Code, p)
			}
			seenPerm[p] = true
		}
	}
}

func TestResolveTenantContextUnionsRolesAndIgnoresExpiredGrants(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := context.Background()
	actor := f.h.CreateActor("authz-actor", "Authz Actor")
	f.grant(t, f.tenantA, actor, "TENANT_ADMIN")
	f.grant(t, f.tenantA, actor, "AUDITOR")

	// An expired PROGRAM_MANAGER grant must contribute nothing.
	f.h.AdminExec(`
		INSERT INTO iam.access_grant (tenant_id, tenant_membership_id, role_id, scope_type, valid_period)
		SELECT m.tenant_id, m.id, r.id, 'TENANT', tstzrange(clock_timestamp() - interval '2 days', clock_timestamp() - interval '1 day', '[)')
		  FROM iam.tenant_membership m JOIN iam.role r ON r.tenant_id = m.tenant_id
		 WHERE m.tenant_id = $1 AND m.actor_id = $2 AND r.code = 'PROGRAM_MANAGER'`, f.tenantA, actor)

	rc, err := f.authz.ResolveTenantContext(ctx, sessionFor(actor, f.tenantA), f.tenantA, identity.AppAny, "req-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, want := range []string{"identity.role.manage", "organization.manage", "audit.read", "security.audit.read"} {
		if !rc.Has(want) {
			t.Errorf("missing permission %s", want)
		}
	}
	if rc.Has("program.manage") {
		t.Error("expired grant contributed program.manage")
	}
	if rc.TenantID != f.tenantA || rc.Principal.ActorID != actor || rc.MembershipID == uuid.Nil {
		t.Fatalf("context = %+v", rc)
	}
	if rc.Locale != "tr-TR" || rc.TimeZone != "Europe/Istanbul" || rc.RequestID != "req-1" || rc.ClientType != identity.ClientBrowser {
		t.Fatalf("context defaults = %+v", rc)
	}
	if rc.StepUpValid {
		t.Fatal("no step-up was performed")
	}
	stepped := sessionFor(actor, f.tenantA)
	stepped.StepUpUntil = time.Now().Add(time.Minute)
	if rc, _ := f.authz.ResolveTenantContext(ctx, stepped, f.tenantA, identity.AppAny, ""); !rc.StepUpValid {
		t.Fatal("step-up window not reflected")
	}
}

func TestResolveTenantContextRejectsMismatchMissingAndSuspendedMembership(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := context.Background()
	actor := f.h.CreateActor("authz-actor", "Authz Actor")
	f.grant(t, f.tenantA, actor, "AUDITOR")

	// Header names B while the session is on A.
	if _, err := f.authz.ResolveTenantContext(ctx, sessionFor(actor, f.tenantA), f.tenantB, identity.AppAny, ""); !errors.Is(err, application.ErrTenantMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	// No active tenant on the session at all.
	if _, err := f.authz.ResolveTenantContext(ctx, sessionFor(actor, uuid.Nil), f.tenantA, identity.AppAny, ""); !errors.Is(err, application.ErrTenantMismatch) {
		t.Fatalf("no active tenant: %v", err)
	}
	// Session claims B but the actor has no membership there.
	if _, err := f.authz.ResolveTenantContext(ctx, sessionFor(actor, f.tenantB), f.tenantB, identity.AppAny, ""); !errors.Is(err, application.ErrNoMembership) {
		t.Fatalf("no membership: %v", err)
	}
	// A suspended membership is as good as none.
	f.h.AdminExec(`UPDATE iam.tenant_membership SET membership_status = 'SUSPENDED' WHERE tenant_id = $1 AND actor_id = $2`, f.tenantA, actor)
	if _, err := f.authz.ResolveTenantContext(ctx, sessionFor(actor, f.tenantA), f.tenantA, identity.AppAny, ""); !errors.Is(err, application.ErrNoMembership) {
		t.Fatalf("suspended membership: %v", err)
	}
	tenants, err := f.authz.ListTenants(ctx, actor)
	if err != nil || len(tenants) != 0 {
		t.Fatalf("suspended membership still listed: %v err=%v", tenants, err)
	}
}

func TestListTenantsAndContextsSeeOnlyOwnMemberships(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := context.Background()
	alice := f.h.CreateActor("alice", "Alice")
	bob := f.h.CreateActor("bob", "Bob")
	f.grant(t, f.tenantA, alice, "AUDITOR")
	f.grant(t, f.tenantB, alice, "TENANT_ADMIN")
	f.grant(t, f.tenantB, bob, "AUDITOR")

	aliceTenants, err := f.authz.ListTenants(ctx, alice)
	if err != nil || len(aliceTenants) != 2 {
		t.Fatalf("alice tenants = %v err=%v", aliceTenants, err)
	}
	if aliceTenants[0].DisplayName != "Authz A" || aliceTenants[1].DisplayName != "Authz B" {
		t.Fatalf("order = %v", aliceTenants)
	}
	bobTenants, err := f.authz.ListTenants(ctx, bob)
	if err != nil || len(bobTenants) != 1 || bobTenants[0].ID != f.tenantB {
		t.Fatalf("bob tenants = %v err=%v", bobTenants, err)
	}

	contexts, err := f.authz.TenantContexts(ctx, alice, identity.AppAny)
	if err != nil || len(contexts) != 2 {
		t.Fatalf("contexts = %v err=%v", contexts, err)
	}
	if !contains(contexts[0].Permissions, "audit.read") || !contains(contexts[1].Permissions, "identity.role.manage") {
		t.Fatalf("permissions per tenant wrong: %+v", contexts)
	}
}

func TestSwitchTenantUpdatesSessionAndReturnsScopes(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := context.Background()
	actor := f.h.CreateActor("staff", "Staff")
	hospital, err := f.prov.EnsureProviderOrganization(ctx, f.tenantA, "HOSP", "Test Hastane")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.prov.GrantRole(ctx, application.GrantRoleInput{
		TenantID: f.tenantA, ActorID: actor, RoleCode: "PROVIDER_STAFF",
		ScopeType: application.ScopeOrganization, ScopeID: uuid.NullUUID{UUID: hospital, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	id, _ := domain.NewToken()
	now := time.Now()
	session := identity.Session{ID: id, ActorID: actor, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := f.store.Create(ctx, session); err != nil {
		t.Fatal(err)
	}

	tc, err := f.authz.SwitchTenant(ctx, session, f.tenantA, identity.AppAny)
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	if !contains(tc.Permissions, "service_request.create") || len(tc.Scopes) != 1 || tc.Scopes[0].Type != application.ScopeOrganization || tc.Scopes[0].ID.UUID != hospital {
		t.Fatalf("context = %+v", tc)
	}
	stored, err := f.store.Get(ctx, id)
	if err != nil || !stored.ActiveTenantID.Valid || stored.ActiveTenantID.UUID != f.tenantA {
		t.Fatalf("active tenant not stored: %+v err=%v", stored, err)
	}
	if _, err := f.authz.SwitchTenant(ctx, session, f.tenantB, identity.AppAny); !errors.Is(err, application.ErrNoMembership) {
		t.Fatalf("switch to a tenant without membership: %v", err)
	}
	if _, err := f.authz.SwitchTenant(ctx, session, uuid.New(), identity.AppAny); !errors.Is(err, application.ErrNoMembership) {
		t.Fatalf("switch to an unknown tenant must look identical: %v", err)
	}
}

func TestGrantRoleIsIdempotentAndValidatesScope(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := context.Background()
	actor := f.h.CreateActor("grantee", "Grantee")

	created, err := f.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: f.tenantA, ActorID: actor, RoleCode: "AUDITOR"})
	if err != nil || !created {
		t.Fatalf("first grant: created=%v err=%v", created, err)
	}
	created, err = f.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: f.tenantA, ActorID: actor, RoleCode: "AUDITOR"})
	if err != nil || created {
		t.Fatalf("second grant: created=%v err=%v", created, err)
	}
	if _, err := f.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: f.tenantA, ActorID: actor, RoleCode: "NOPE"}); err == nil {
		t.Fatal("unknown role accepted")
	}
	if _, err := f.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: f.tenantA, ActorID: actor, RoleCode: "PROVIDER_STAFF"}); err == nil {
		t.Fatal("organization-scoped role granted without a scope id")
	}
	if _, err := f.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: f.tenantA, ActorID: actor, RoleCode: "AUDITOR", ScopeType: application.ScopeTenant, ScopeID: uuid.NullUUID{UUID: uuid.New(), Valid: true}}); err == nil {
		t.Fatal("tenant-scoped role granted with a scope id")
	}
	ctx2, cancel := f.h.Ctx()
	defer cancel()
	var memberships int
	if err := f.h.Admin.QueryRow(ctx2, `SELECT count(*) FROM iam.tenant_membership WHERE actor_id = $1`, actor).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if memberships != 1 {
		t.Fatalf("memberships = %d, want 1", memberships)
	}
}
