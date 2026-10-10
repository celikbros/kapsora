package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

func TestHotelBillingFixtureIsScopedIdempotentAndRefusesBroaderIdentity(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s := newDemoSeeder(t, h.App)
	password := "local hotel acceptance password 2026"
	// Missing prerequisites must not create a partially configured login.
	if _, err := s.hotelBilling(ctx, password); err == nil {
		t.Fatal("accepted missing DEMO_A")
	}
	tenant, err := s.provisioner.ProvisionTenant(ctx, identityapp.ProvisionCommand{Code: "DEMO_A", LegalName: "Demo A", DisplayName: "Demo A"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.hotelBilling(ctx, password); err == nil {
		t.Fatal("accepted missing hotel")
	}
	if _, err := s.credentials.FindByUsername(ctx, hotelBillingUsername); !errors.Is(err, identityapp.ErrAccountNotFound) {
		t.Fatal("missing hotel created an account")
	}
	hotel, err := s.provisioner.EnsureProviderOrganization(ctx, tenant, hotelCode, "Demo Hotel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.hotelBilling(ctx, ""); err == nil {
		t.Fatal("accepted missing password")
	}
	first, err := s.hotelBilling(ctx, password)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.TenantID != tenant || first.OrganizationID != hotel {
		t.Fatal("unexpected fixture identity or scope")
	}
	before, err := s.credentials.FindByUsername(ctx, hotelBillingUsername)
	if err != nil {
		t.Fatal(err)
	}
	// A rerun neither rotates credentials nor adds another grant.
	again, err := s.hotelBilling(ctx, "different password must not replace existing credentials")
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.credentials.FindByUsername(ctx, hotelBillingUsername)
	if err != nil {
		t.Fatal(err)
	}
	if again.Created || again.ActorID != first.ActorID || before.PasswordHash != after.PasswordHash {
		t.Fatal("rerun changed account or password")
	}
	repository := identitypg.NewAuthorizationRepository(h.App)
	memberships, err := repository.ListMemberships(ctx, first.ActorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 1 || memberships[0].Tenant.ID != tenant {
		t.Fatal("unexpected tenant access")
	}
	grants, err := repository.ResolveGrants(ctx, tenant, memberships[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants.Items) != 1 || grants.Items[0].Scope.Type != "ORGANIZATION" || grants.Items[0].Scope.ID.UUID != hotel {
		t.Fatal("unexpected grants")
	}
	for _, permission := range grants.Items[0].Permissions {
		if permission == "health.clinical.read" || permission == "health.sensitive.read" || permission == "settlement.approve" {
			t.Fatalf("unwanted permission %s", permission)
		}
	}
	hospital, err := s.provisioner.EnsureProviderOrganization(ctx, tenant, hospitalCode, "Demo Hospital")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.provisioner.GrantRole(ctx, identityapp.GrantRoleInput{TenantID: tenant, ActorID: first.ActorID, RoleCode: "PROVIDER_BILLING", ScopeType: "ORGANIZATION", ScopeID: uuid.NullUUID{UUID: hospital, Valid: true}, Reason: "isolated refusal fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.hotelBilling(ctx, password); err == nil {
		t.Fatal("accepted account with hospital access")
	}
	// Guard failures inspect but never revoke or overwrite an operator's grants.
	grants, err = repository.ResolveGrants(ctx, tenant, memberships[0].ID)
	if err != nil || len(grants.Items) != 2 {
		t.Fatal("refusal modified existing grants")
	}
}

func TestHotelBillingFixtureRefusesUsernameCollision(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()
	s := newDemoSeeder(t, h.App)
	tenant, err := s.provisioner.ProvisionTenant(ctx, identityapp.ProvisionCommand{Code: "DEMO_A", LegalName: "Demo A", DisplayName: "Demo A"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.provisioner.EnsureProviderOrganization(ctx, tenant, hotelCode, "Demo Hotel"); err != nil {
		t.Fatal(err)
	}
	id, err := s.svc.CreateAccount(ctx, hotelBillingUsername, "Existing unrelated user", "other@demo.test", "local acceptance password 2026", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.hotelBilling(ctx, "local acceptance password 2026"); err == nil {
		t.Fatal("adopted an unrelated account")
	}
	memberships, err := identitypg.NewAuthorizationRepository(h.App).ListMemberships(ctx, id)
	if err != nil || len(memberships) != 0 {
		t.Fatal("collision received a grant")
	}
}
