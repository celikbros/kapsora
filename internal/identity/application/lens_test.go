package application_test

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
)

// onlyIn is a permission of role that neither of the others carries, so an assertion on it
// cannot pass because some other grant happened to bring the same code.
func onlyIn(t *testing.T, role string, others ...string) string {
	t.Helper()
	tpl, ok := application.RoleTemplateByCode(role)
	if !ok {
		t.Fatalf("no role template %s", role)
	}
	for _, p := range tpl.Permissions {
		shared := false
		for _, o := range others {
			other, _ := application.RoleTemplateByCode(o)
			if slices.Contains(other.Permissions, p) {
				shared = true
				break
			}
		}
		if !shared {
			return p
		}
	}
	t.Fatalf("%s has no permission of its own against %v", role, others)
	return ""
}

// One person who is a medical reviewer, a clerk at a hospital and a member, all in one
// tenant, holds three kinds of grant. Each app sees its own and nothing else; a request that
// names no app still gets all three, as every caller did before the apps were told apart.
func TestEachAppSeesOnlyItsOwnGrants(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := context.Background()

	person := f.createPerson(t, f.tenantA, "Deniz", "Calisan")
	actor := f.h.CreateActor("staff-member", "Deniz Çalışan")
	f.grant(t, f.tenantA, actor, "MEDICAL_REVIEWER")
	hospital, err := f.prov.EnsureProviderOrganization(ctx, f.tenantA, "HOSP", "Test Hastane")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.prov.GrantRole(ctx, application.GrantRoleInput{
		TenantID: f.tenantA, ActorID: actor, RoleCode: "PROVIDER_STAFF",
		ScopeType: application.ScopeOrganization, ScopeID: uuid.NullUUID{UUID: hospital, Valid: true},
	}); err != nil {
		t.Fatalf("grant the hospital role: %v", err)
	}
	if _, err := f.prov.GrantRole(ctx, application.GrantRoleInput{
		TenantID: f.tenantA, ActorID: actor, RoleCode: "MEMBER",
		ScopeType: application.ScopePerson, ScopeID: uuid.NullUUID{UUID: person, Valid: true},
	}); err != nil {
		t.Fatalf("grant the member binding: %v", err)
	}

	staffOnly := onlyIn(t, "MEDICAL_REVIEWER", "PROVIDER_STAFF", "MEMBER")
	clerkOnly := onlyIn(t, "PROVIDER_STAFF", "MEDICAL_REVIEWER", "MEMBER")
	memberOnly := onlyIn(t, "MEMBER", "MEDICAL_REVIEWER", "PROVIDER_STAFF")

	resolve := func(app identity.App) identity.RequestContext {
		t.Helper()
		rc, err := f.authz.ResolveTenantContext(ctx, sessionFor(actor, f.tenantA), f.tenantA, app, "req")
		if err != nil {
			t.Fatalf("resolve %q: %v", app, err)
		}
		if rc.App != app {
			t.Fatalf("context app = %q, want %q", rc.App, app)
		}
		return rc
	}
	holds := func(rc identity.RequestContext, want map[string]bool) {
		t.Helper()
		for p, has := range want {
			if rc.Has(p) != has {
				t.Errorf("app %q: has %s = %t, want %t", rc.App, p, rc.Has(p), has)
			}
		}
	}

	// The backoffice: the staff role only. Not narrowed to the hospital, not acting for the
	// person -- but it knows who the person is.
	back := resolve(identity.AppBackoffice)
	holds(back, map[string]bool{staffOnly: true, clerkOnly: false, memberOnly: false})
	if len(back.Scopes) != 0 {
		t.Errorf("backoffice scopes = %+v, want none", back.Scopes)
	}
	if _, bound := back.PersonScope(); bound {
		t.Error("the backoffice acts for the member's person")
	}
	if back.SelfPersonID != (uuid.NullUUID{UUID: person, Valid: true}) {
		t.Errorf("backoffice self person = %+v, want %s", back.SelfPersonID, person)
	}

	// The provider portal: the hospital role, narrowed to the hospital.
	prov := resolve(identity.AppProvider)
	holds(prov, map[string]bool{staffOnly: false, clerkOnly: true, memberOnly: false})
	if len(prov.Scopes) != 1 || prov.Scopes[0].Type != application.ScopeOrganization || prov.Scopes[0].ID.UUID != hospital {
		t.Errorf("provider scopes = %+v, want the hospital", prov.Scopes)
	}
	if _, bound := prov.PersonScope(); bound {
		t.Error("the provider portal acts for the member's person")
	}

	// The member app: the binding only, acting for the person.
	mem := resolve(identity.AppMember)
	holds(mem, map[string]bool{staffOnly: false, clerkOnly: false, memberOnly: true})
	if got, bound := mem.PersonScope(); !bound || got != person {
		t.Errorf("member app person = %s bound = %t, want %s", got, bound, person)
	}

	// No app named: everything, as before.
	all := resolve(identity.AppAny)
	holds(all, map[string]bool{staffOnly: true, clerkOnly: true, memberOnly: true})

	// /me lists all three apps whichever app asks, and answers for the asking app.
	contexts, err := f.authz.TenantContexts(ctx, actor, identity.AppBackoffice)
	if err != nil || len(contexts) != 1 {
		t.Fatalf("contexts = %+v err = %v", contexts, err)
	}
	c := contexts[0]
	if want := []identity.App{identity.AppBackoffice, identity.AppProvider, identity.AppMember}; !slices.Equal(c.Apps, want) {
		t.Errorf("apps = %v, want %v", c.Apps, want)
	}
	if c.PersonID.Valid || c.SelfPersonID.UUID != person || slices.Contains(c.Permissions, memberOnly) {
		t.Errorf("backoffice /me context = %+v", c)
	}
}

// An app the account has no grant in gets no permission at all: the header narrows, it
// never lends.
func TestAnAppWithoutGrantsGetsNothing(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := context.Background()
	actor := f.h.CreateActor("reviewer-only", "Rıza Değerlendirici")
	f.grant(t, f.tenantA, actor, "FINANCIAL_REVIEWER")

	for _, app := range []identity.App{identity.AppProvider, identity.AppMember} {
		rc, err := f.authz.ResolveTenantContext(ctx, sessionFor(actor, f.tenantA), f.tenantA, app, "req")
		if err != nil {
			t.Fatalf("resolve %q: %v", app, err)
		}
		if len(rc.Permissions) != 0 || len(rc.Scopes) != 0 || rc.SelfPersonID.Valid {
			t.Errorf("app %q got %d permissions, scopes %+v, self %+v", app, len(rc.Permissions), rc.Scopes, rc.SelfPersonID)
		}
	}
	contexts, err := f.authz.TenantContexts(ctx, actor, identity.AppMember)
	if err != nil || len(contexts) != 1 {
		t.Fatalf("contexts = %+v err = %v", contexts, err)
	}
	if !slices.Equal(contexts[0].Apps, []identity.App{identity.AppBackoffice}) {
		t.Errorf("apps = %v, want [backoffice]", contexts[0].Apps)
	}
}

func TestParseAppRefusesAnUnknownApp(t *testing.T) {
	for raw, want := range map[string]identity.App{"": identity.AppAny, "backoffice": identity.AppBackoffice, "provider": identity.AppProvider, "member": identity.AppMember} {
		if got, ok := identity.ParseApp(raw); !ok || got != want {
			t.Errorf("ParseApp(%q) = %q, %t", raw, got, ok)
		}
	}
	for _, raw := range []string{"Backoffice", "admin", "member "} {
		if _, ok := identity.ParseApp(raw); ok {
			t.Errorf("ParseApp(%q) accepted", raw)
		}
	}
}
