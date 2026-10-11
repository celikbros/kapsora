package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

const hotelBillingUsername = "hotel.billing.a"
const hotelBillingName = "Demo Otel Faturalama"
const hotelBillingEmail = "hotel.billing.a@demo.test"

type hotelBillingResult struct {
	ActorID        uuid.UUID `json:"actorId"`
	TenantID       uuid.UUID `json:"tenantId"`
	OrganizationID uuid.UUID `json:"organizationId"`
	Username       string    `json:"username"`
	Created        bool      `json:"created"`
}

// hotelBilling is an explicit local acceptance fixture, never part of seed demo.
// It uses the existing billing role in DEMO_A/DEMO_HOTEL only. Existing accounts,
// passwords, role templates and other tenants are never broadened or rewritten.
func (s *seeder) hotelBilling(ctx context.Context, password string) (hotelBillingResult, error) {
	var out hotelBillingResult
	if strings.TrimSpace(password) == "" {
		return out, fmt.Errorf("hotel-billing requires KAPSORA_SEED_DEMO_PASSWORD")
	}
	tenant, err := sqlcgen.New(s.pool).GetTenantIDByCode(ctx, "DEMO_A")
	if err != nil {
		return out, fmt.Errorf("hotel-billing requires existing DEMO_A: %w", err)
	}
	var hotel uuid.UUID
	code := hotelCode
	err = db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenant}, func(ctx context.Context, tx pgx.Tx) error {
		var findErr error
		hotel, findErr = sqlcgen.New(tx).FindTenantOrganizationByCode(ctx, sqlcgen.FindTenantOrganizationByCodeParams{TenantID: tenant, TenantCode: &code})
		if findErr != nil {
			return findErr
		}
		org, findErr := sqlcgen.New(tx).GetTenantOrganization(ctx, sqlcgen.GetTenantOrganizationParams{TenantID: tenant, ID: hotel})
		if findErr != nil {
			return findErr
		}
		if org.RelationshipRole != "PROVIDER" || org.RelationshipStatus != "ACTIVE" || org.OrganizationStatus != "ACTIVE" {
			return fmt.Errorf("hotel-billing requires an active provider organization")
		}
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("hotel-billing requires existing DEMO_HOTEL: %w", err)
	}
	out.TenantID, out.OrganizationID, out.Username = tenant, hotel, hotelBillingUsername
	existing, err := s.credentials.FindByUsername(ctx, hotelBillingUsername)
	if err == nil {
		if existing.DisplayName != hotelBillingName || existing.Email != hotelBillingEmail || existing.Status != identityapp.ActorActive {
			return out, fmt.Errorf("hotel-billing refuses an existing account with different identity or status")
		}
		repository := identitypg.NewAuthorizationRepository(s.pool)
		memberships, err := repository.ListMemberships(ctx, existing.ActorID)
		if err != nil {
			return out, err
		}
		if len(memberships) != 1 || memberships[0].Tenant.ID != tenant {
			return out, fmt.Errorf("hotel-billing refuses existing account with different tenant memberships")
		}
		grants, err := repository.ResolveGrants(ctx, tenant, memberships[0].ID)
		if err != nil {
			return out, err
		}
		if len(grants.Items) != 1 || grants.Items[0].Scope.Type != identityapp.ScopeOrganization ||
			!grants.Items[0].Scope.ID.Valid || grants.Items[0].Scope.ID.UUID != hotel {
			return out, fmt.Errorf("hotel-billing refuses existing account with different grants")
		}
		template, _ := identityapp.RoleTemplateByCode("PROVIDER_BILLING")
		want, got := slices.Clone(template.Permissions), slices.Clone(grants.Items[0].Permissions)
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(want, got) {
			return out, fmt.Errorf("hotel-billing refuses changed billing permissions")
		}
		out.ActorID = existing.ActorID
		return out, nil
	}
	if !errors.Is(err, identityapp.ErrAccountNotFound) {
		return out, err
	}
	actor, err := s.svc.CreateAccount(ctx, hotelBillingUsername, hotelBillingName, hotelBillingEmail, password, false)
	if err != nil {
		return out, err
	}
	out.ActorID, out.Created = actor, true
	_, err = s.provisioner.GrantRole(ctx, identityapp.GrantRoleInput{
		TenantID: tenant, ActorID: actor, RoleCode: "PROVIDER_BILLING",
		ScopeType: identityapp.ScopeOrganization, ScopeID: uuid.NullUUID{UUID: hotel, Valid: true},
		Reason: "local PC06 demo hotel billing acceptance",
	})
	if err != nil {
		return out, fmt.Errorf("hotel-billing created account but grant failed; inspect before retry: %w", err)
	}
	return out, nil
}
