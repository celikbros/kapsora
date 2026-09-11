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

// EnsureOrganizationTaxIdentity implements application.ProvisioningRepository.
//
// The relationship is read first so the write names an organization this tenant actually has
// a relationship with: directory.organization is global and carries no RLS of its own, and a
// helper that took a bare organization id would be a helper that could write across tenants.
func (r *ProvisioningRepository) EnsureOrganizationTaxIdentity(ctx context.Context, tenantID,
	relationshipID uuid.UUID, cipher, hash []byte,
) (bool, error) {
	if len(cipher) == 0 || len(hash) == 0 {
		return false, fmt.Errorf("identity: a tax identity needs both a ciphertext and an index")
	}
	written := false
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, err := q.GetTenantOrganization(ctx, sqlcgen.GetTenantOrganizationParams{TenantID: tenantID, ID: relationshipID})
		if err != nil {
			return fmt.Errorf("identity: find organization relationship: %w", err)
		}
		rows, err := q.SetOrganizationTaxIdentityIfAbsent(ctx, sqlcgen.SetOrganizationTaxIdentityIfAbsentParams{
			ID: row.OrganizationID, TaxNumberCipher: cipher, TaxNumberHash: hash,
		})
		if err != nil {
			return fmt.Errorf("identity: set organization tax identity: %w", err)
		}
		written = rows > 0
		return nil
	})
	return written, err
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

// SyncSystemRoles implements application.ProvisioningRepository. It is ProvisionTenant's role
// step for a tenant that already exists: every template role the tenant lacks is created,
// and every template permission a role lacks is added. It only adds. A permission a template
// no longer lists stays on the role until somebody takes it off, because removing access
// from a running tenant is a decision for a person and not a side effect of an upgrade.
func (r *ProvisioningRepository) SyncSystemRoles(ctx context.Context, tenantID uuid.UUID, roles []application.RoleTemplate) (application.RoleSyncResult, error) {
	var res application.RoleSyncResult
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		for _, tpl := range roles {
			_, err := q.GetRoleByCode(ctx, sqlcgen.GetRoleByCodeParams{TenantID: tenantID, Code: tpl.Code})
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				res.RolesCreated = append(res.RolesCreated, tpl.Code)
			case err != nil:
				return fmt.Errorf("identity: find role %s: %w", tpl.Code, err)
			}
			desc := tpl.Description
			roleID, err := q.CreateRole(ctx, sqlcgen.CreateRoleParams{TenantID: tenantID, Code: tpl.Code, Name: tpl.Name, Description: &desc, IsSystemRole: true})
			if err != nil {
				return fmt.Errorf("identity: create role %s: %w", tpl.Code, err)
			}
			for _, perm := range tpl.Permissions {
				n, err := q.AddRolePermissionCounted(ctx, sqlcgen.AddRolePermissionCountedParams{TenantID: tenantID, RoleID: roleID, PermissionCode: perm})
				if err != nil {
					return fmt.Errorf("identity: role %s permission %s: %w", tpl.Code, perm, err)
				}
				res.PermissionsAdded += int(n)
			}
		}
		return nil
	})
	if err != nil {
		return application.RoleSyncResult{}, err
	}
	return res, nil
}
