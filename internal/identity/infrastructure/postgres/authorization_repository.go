package identitypg

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// AuthorizationRepository resolves memberships and grants under the RLS rules: membership
// lookups in an actor transaction (policy actor_self_membership, migration 000009), grant
// resolution in the tenant transaction.
type AuthorizationRepository struct {
	pool *pgxpool.Pool
}

// NewAuthorizationRepository returns a repository backed by pool.
func NewAuthorizationRepository(pool *pgxpool.Pool) *AuthorizationRepository {
	return &AuthorizationRepository{pool: pool}
}

var _ application.AuthorizationRepository = (*AuthorizationRepository)(nil)

// FindActiveMembership implements application.AuthorizationRepository.
func (r *AuthorizationRepository) FindActiveMembership(ctx context.Context, actorID, tenantID uuid.UUID) (application.Membership, error) {
	var out application.Membership
	err := db.WithActorTx(ctx, r.pool, actorID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := sqlcgen.New(tx).FindActiveMembership(ctx, sqlcgen.FindActiveMembershipParams{ActorID: actorID, TenantID: tenantID})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNoMembership
		}
		if err != nil {
			return fmt.Errorf("identity: find membership: %w", err)
		}
		out = application.Membership{ID: row.ID, Tenant: application.TenantSummary{
			ID: row.TenantID, Code: row.Code, DisplayName: row.DisplayName, Status: row.Status,
			DefaultLocale: row.DefaultLocale, DefaultTimeZone: row.DefaultTimeZone,
		}}
		return nil
	})
	return out, err
}

// ListMemberships implements application.AuthorizationRepository.
func (r *AuthorizationRepository) ListMemberships(ctx context.Context, actorID uuid.UUID) ([]application.Membership, error) {
	var out []application.Membership
	err := db.WithActorTx(ctx, r.pool, actorID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := sqlcgen.New(tx).ListMembershipsForActor(ctx, actorID)
		if err != nil {
			return fmt.Errorf("identity: list memberships: %w", err)
		}
		out = make([]application.Membership, 0, len(rows))
		for _, row := range rows {
			out = append(out, application.Membership{ID: row.ID, Tenant: application.TenantSummary{
				ID: row.TenantID, Code: row.Code, DisplayName: row.DisplayName, Status: row.Status,
				DefaultLocale: row.DefaultLocale, DefaultTimeZone: row.DefaultTimeZone,
			}})
		}
		return nil
	})
	return out, err
}

// ResolveGrants implements application.AuthorizationRepository.
func (r *AuthorizationRepository) ResolveGrants(ctx context.Context, tenantID, membershipID uuid.UUID) (application.Grants, error) {
	var out application.Grants
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := sqlcgen.New(tx).ListGrantsForMembership(ctx, sqlcgen.ListGrantsForMembershipParams{TenantID: tenantID, TenantMembershipID: membershipID})
		if err != nil {
			return fmt.Errorf("identity: list grants: %w", err)
		}
		// Rows arrive ordered by grant, one per permission of its role; fold them back into
		// one Grant each so the scope stays attached to the permissions it came with.
		var last uuid.UUID
		for _, row := range rows {
			if len(out.Items) == 0 || row.ID != last {
				out.Items = append(out.Items, application.Grant{Scope: identity.Scope{Type: row.ScopeType, ID: row.ScopeID}})
				last = row.ID
			}
			if row.PermissionCode != nil {
				g := &out.Items[len(out.Items)-1]
				g.Permissions = append(g.Permissions, *row.PermissionCode)
			}
		}
		return nil
	})
	return out, err
}
