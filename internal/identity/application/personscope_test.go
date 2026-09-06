package application_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
)

// createPerson writes a person straight through the admin pool. The party service is not
// wired in this package and does not need to be: what is under test is the grant, not how
// a person is created.
func (f *authzFixture) createPerson(t *testing.T, tenantID uuid.UUID, first, last string) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var id uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, first, last, strings.ToLower(first+" "+last)).Scan(&id); err != nil {
		t.Fatalf("create person: %v", err)
	}
	return id
}

// TestPersonScopeGrantBindsTheContextToOnePerson is the acceptance criterion of WP-I6-04
// section 2.4 in one test: a member account acts for exactly one person, the server says
// which, and no other actor acquires a person by accident.
func TestPersonScopeGrantBindsTheContextToOnePerson(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := context.Background()

	person := f.createPerson(t, f.tenantA, "Melis", "Uye")
	member := f.h.CreateActor("member-a", "Melis Üye")
	reviewer := f.h.CreateActor("reviewer-a", "Rıza Değerlendirici")

	if _, err := f.prov.GrantRole(ctx, application.GrantRoleInput{
		TenantID: f.tenantA, ActorID: member, RoleCode: "MEMBER",
		ScopeType: application.ScopePerson,
		ScopeID:   uuid.NullUUID{UUID: person, Valid: true},
	}); err != nil {
		t.Fatalf("grant the member binding: %v", err)
	}
	f.grant(t, f.tenantA, reviewer, "MEDICAL_REVIEWER")

	// The request context of a member carries the person.
	rc, err := f.authz.ResolveTenantContext(ctx, sessionFor(member, f.tenantA), f.tenantA, "req-1")
	if err != nil {
		t.Fatalf("resolve the member context: %v", err)
	}
	got, bound := rc.PersonScope()
	if !bound || got != person {
		t.Fatalf("member context: person = %s bound = %t, want %s", got, bound, person)
	}
	// And the permissions of the role still arrive: a scoped grant narrows what a role
	// applies to, it does not withhold the role.
	if !rc.Has("accommodation.booking.create") {
		t.Fatal("a PERSON-scoped MEMBER grant carried no permissions")
	}

	// Every other actor has none. This is the assertion that would go red if PersonID were
	// filled from, say, the first scope of any kind.
	reviewerRC, err := f.authz.ResolveTenantContext(ctx, sessionFor(reviewer, f.tenantA), f.tenantA, "req-2")
	if err != nil {
		t.Fatalf("resolve the reviewer context: %v", err)
	}
	if _, bound := reviewerRC.PersonScope(); bound {
		t.Fatalf("a reviewer acquired a person: %+v", reviewerRC.PersonID)
	}

	// /me says the same thing as the request context, for the same actor.
	contexts, err := f.authz.TenantContexts(ctx, member)
	if err != nil {
		t.Fatalf("tenant contexts: %v", err)
	}
	if len(contexts) != 1 {
		t.Fatalf("member is in %d tenants, want 1", len(contexts))
	}
	if !contexts[0].PersonID.Valid || contexts[0].PersonID.UUID != person {
		t.Fatalf("/me person = %+v, want %s", contexts[0].PersonID, person)
	}
	// So does switch-tenant, which builds the same view down a different path. It needs a
	// real session because it writes the active tenant back onto one.
	sessionID, _ := domain.NewToken()
	now := time.Now()
	if err := f.store.Create(ctx, identity.Session{
		ID: sessionID, ActorID: member, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	switched, err := f.authz.SwitchTenant(ctx, identity.Session{ID: sessionID, ActorID: member}, f.tenantA)
	if err != nil {
		t.Fatalf("switch tenant: %v", err)
	}
	if switched.PersonID != contexts[0].PersonID {
		t.Fatalf("switch-tenant person = %+v, /me person = %+v", switched.PersonID, contexts[0].PersonID)
	}
}

// TestPersonScopeGrantIsRefusedWhenItWouldBeAmbiguousOrForeign proves the three refusals
// that make the binding trustworthy are the database's and not the application's. Each is
// attempted through the ordinary grant path; none of them is prevented by a Go check, so
// deleting the constraint is what these tests detect.
func TestPersonScopeGrantIsRefusedWhenItWouldBeAmbiguousOrForeign(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := context.Background()

	person := f.createPerson(t, f.tenantA, "Melis", "Uye")
	other := f.createPerson(t, f.tenantA, "Kerem", "Uye")
	foreign := f.createPerson(t, f.tenantB, "Yabanci", "Kisi")

	member := f.h.CreateActor("member-b", "Melis Üye")
	second := f.h.CreateActor("member-c", "Başka Üye")

	bind := func(actor, personID uuid.UUID) error {
		_, err := f.prov.GrantRole(ctx, application.GrantRoleInput{
			TenantID: f.tenantA, ActorID: actor, RoleCode: "MEMBER",
			ScopeType: application.ScopePerson,
			ScopeID:   uuid.NullUUID{UUID: personID, Valid: true},
		})
		return err
	}

	// A person of another tenant is not a person of this one.
	if err := bind(member, foreign); err == nil {
		t.Fatal("a PERSON grant naming another tenant's person was accepted")
	}
	// Neither is a person that does not exist.
	if err := bind(member, uuid.New()); err == nil {
		t.Fatal("a PERSON grant naming no person at all was accepted")
	}
	if err := bind(member, person); err != nil {
		t.Fatalf("the legitimate binding was refused: %v", err)
	}
	// One account, one person: a second person for the same membership is refused.
	if err := bind(member, other); err == nil {
		t.Fatal("an account was bound to a second person")
	}
	// One person, one account: the same person cannot be handed to a second account.
	if err := bind(second, person); err == nil {
		t.Fatal("a person was bound to a second account")
	}
	// The scope is also not a place to put a random id: TENANT still carries none.
	if _, err := f.prov.GrantRole(ctx, application.GrantRoleInput{
		TenantID: f.tenantA, ActorID: second, RoleCode: "MEMBER",
		ScopeType: application.ScopeTenant,
		ScopeID:   uuid.NullUUID{UUID: other, Valid: true},
	}); err == nil {
		t.Fatal("a TENANT-scoped grant carried a scope id")
	}
}

// TestPersonScopeGrantSurvivesRoleTemplateChecks keeps the two halves of the permission in
// step: ScopePerson is the identity package's constant, not a second spelling.
func TestPersonScopeGrantSurvivesRoleTemplateChecks(t *testing.T) {
	if application.ScopePerson != identity.ScopePerson {
		t.Fatalf("two spellings of the person scope: %q and %q", application.ScopePerson, identity.ScopePerson)
	}
	if application.ScopePerson != "PERSON" {
		t.Fatalf("the person scope is %q; migration 000039 CHECKs 'PERSON'", application.ScopePerson)
	}
}
