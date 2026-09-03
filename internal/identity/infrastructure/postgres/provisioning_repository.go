package identitypg

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// ProvisioningRepository creates tenants, roles, memberships and grants. It works under the
// application role: the tenant row itself is not RLS-protected, and once it exists the
// transaction is bound to it so every tenant-owned insert passes the tenant policy.
type ProvisioningRepository struct {
	pool *pgxpool.Pool
}

// NewProvisioningRepository returns a repository backed by pool.
func NewProvisioningRepository(pool *pgxpool.Pool) *ProvisioningRepository {
	return &ProvisioningRepository{pool: pool}
}

var _ application.ProvisioningRepository = (*ProvisioningRepository)(nil)

const uniqueViolation = "23505"

// ProvisionTenant implements application.ProvisioningRepository.
func (r *ProvisioningRepository) ProvisionTenant(ctx context.Context, cmd application.ProvisionCommand, roles []application.RoleTemplate, catalogs application.BaselineCatalogs) (uuid.UUID, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("identity: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)

	tenantID, err := q.CreateTenant(ctx, sqlcgen.CreateTenantParams{
		Code: cmd.Code, LegalName: cmd.LegalName, DisplayName: cmd.DisplayName,
		DefaultLocale: cmd.Locale, DefaultTimeZone: cmd.TimeZone, DefaultCurrency: cmd.Currency,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return uuid.Nil, application.ErrTenantCodeExists
		}
		return uuid.Nil, fmt.Errorf("identity: create tenant: %w", err)
	}
	if err := db.BindTenant(ctx, tx, db.TenantContext{TenantID: tenantID, ActorID: cmd.RequestedBy}); err != nil {
		return uuid.Nil, err
	}

	for _, e := range catalogs.IdentifierTypes {
		if err := q.SeedIdentifierType(ctx, sqlcgen.SeedIdentifierTypeParams{TenantID: tenantID, Code: e.Code, DisplayName: e.DisplayName, IsSensitive: e.Flag, UniquenessScope: e.Scope}); err != nil {
			return uuid.Nil, fmt.Errorf("identity: seed identifier type %s: %w", e.Code, err)
		}
	}
	for _, e := range catalogs.RelationshipTypes {
		if err := q.SeedRelationshipType(ctx, sqlcgen.SeedRelationshipTypeParams{TenantID: tenantID, Code: e.Code, DisplayName: e.DisplayName, IsDirectional: e.Flag}); err != nil {
			return uuid.Nil, fmt.Errorf("identity: seed relationship type %s: %w", e.Code, err)
		}
	}
	for _, e := range catalogs.MembershipTypes {
		if err := q.SeedMembershipType(ctx, sqlcgen.SeedMembershipTypeParams{TenantID: tenantID, Code: e.Code, DisplayName: e.DisplayName, RequiresPrincipal: e.Flag}); err != nil {
			return uuid.Nil, fmt.Errorf("identity: seed membership type %s: %w", e.Code, err)
		}
	}
	for _, e := range catalogs.ProgramTypes {
		if err := q.SeedProgramType(ctx, sqlcgen.SeedProgramTypeParams{TenantID: tenantID, Code: e.Code, DisplayName: e.DisplayName}); err != nil {
			return uuid.Nil, fmt.Errorf("identity: seed program type %s: %w", e.Code, err)
		}
	}

	for _, tpl := range roles {
		desc := tpl.Description
		roleID, err := q.CreateRole(ctx, sqlcgen.CreateRoleParams{TenantID: tenantID, Code: tpl.Code, Name: tpl.Name, Description: &desc, IsSystemRole: true})
		if err != nil {
			return uuid.Nil, fmt.Errorf("identity: create role %s: %w", tpl.Code, err)
		}
		for _, perm := range tpl.Permissions {
			if err := q.AddRolePermission(ctx, sqlcgen.AddRolePermissionParams{TenantID: tenantID, RoleID: roleID, PermissionCode: perm}); err != nil {
				return uuid.Nil, fmt.Errorf("identity: role %s permission %s: %w", tpl.Code, perm, err)
			}
		}
	}

	if err := q.ActivateTenant(ctx, tenantID); err != nil {
		return uuid.Nil, fmt.Errorf("identity: activate tenant: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("identity: commit: %w", err)
	}
	return tenantID, nil
}

// GrantRole implements application.ProvisioningRepository.
func (r *ProvisioningRepository) GrantRole(ctx context.Context, in application.GrantRoleInput) (bool, error) {
	created := false
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: in.TenantID, ActorID: in.GrantedBy.UUID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		role, err := q.GetRoleByCode(ctx, sqlcgen.GetRoleByCodeParams{TenantID: in.TenantID, Code: in.RoleCode})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("identity: role %s does not exist in tenant", in.RoleCode)
		}
		if err != nil {
			return fmt.Errorf("identity: find role: %w", err)
		}

		var membershipID uuid.UUID
		m, err := q.FindMembershipAnyStatus(ctx, sqlcgen.FindMembershipAnyStatusParams{TenantID: in.TenantID, ActorID: in.ActorID})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			membershipID, err = q.CreateMembership(ctx, sqlcgen.CreateMembershipParams{TenantID: in.TenantID, ActorID: in.ActorID, CreatedBy: in.GrantedBy})
			if err != nil {
				return fmt.Errorf("identity: create membership: %w", err)
			}
		case err != nil:
			return fmt.Errorf("identity: find membership: %w", err)
		case m.MembershipStatus != "ACTIVE":
			return fmt.Errorf("identity: membership is %s; reactivate it before granting roles", m.MembershipStatus)
		default:
			membershipID = m.ID
		}

		_, err = q.FindAccessGrant(ctx, sqlcgen.FindAccessGrantParams{
			TenantID: in.TenantID, TenantMembershipID: membershipID, RoleID: role.ID, ScopeType: in.ScopeType, ScopeID: in.ScopeID,
		})
		if err == nil {
			return nil // identical open-ended grant already exists
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("identity: find grant: %w", err)
		}
		var reason *string
		if in.Reason != "" {
			reason = &in.Reason
		}
		if _, err := q.CreateAccessGrant(ctx, sqlcgen.CreateAccessGrantParams{
			TenantID: in.TenantID, TenantMembershipID: membershipID, RoleID: role.ID,
			ScopeType: in.ScopeType, ScopeID: in.ScopeID, GrantedBy: in.GrantedBy, GrantReason: reason,
		}); err != nil {
			return fmt.Errorf("identity: create grant: %w", err)
		}
		created = true
		return nil
	})
	return created, err
}

// EnsureProviderOrganization implements application.ProvisioningRepository.
func (r *ProvisioningRepository) EnsureProviderOrganization(ctx context.Context, tenantID uuid.UUID, tenantCode, displayName string) (uuid.UUID, error) {
	var id uuid.UUID
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		existing, err := q.FindTenantOrganizationByCode(ctx, sqlcgen.FindTenantOrganizationByCodeParams{TenantID: tenantID, TenantCode: &tenantCode})
		if err == nil {
			id = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("identity: find organization: %w", err)
		}
		orgID, err := q.CreateGlobalOrganization(ctx, sqlcgen.CreateGlobalOrganizationParams{LegalName: displayName, DisplayName: displayName, OrganizationKind: "PROVIDER", CountryCode: "TR"})
		if err != nil {
			return fmt.Errorf("identity: create organization: %w", err)
		}
		id, err = q.CreateTenantOrganization(ctx, sqlcgen.CreateTenantOrganizationParams{TenantID: tenantID, OrganizationID: orgID, RelationshipRole: "PROVIDER", TenantCode: &tenantCode})
		if err != nil {
			return fmt.Errorf("identity: create tenant organization: %w", err)
		}
		return nil
	})
	return id, err
}
