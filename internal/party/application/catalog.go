package application

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// Catalogs are the tenant's identifier, relationship and membership types. The baseline
// rows are provisioned with the tenant (WP-I1-02); this endpoint only reads them.
type Catalogs struct {
	IdentifierTypes   []CatalogType
	RelationshipTypes []CatalogType
	MembershipTypes   []CatalogType
}

// Catalogs returns the three party catalogs of the caller's tenant.
func (s *Service) Catalogs(ctx context.Context, rc identity.RequestContext) (Catalogs, error) {
	var out Catalogs
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if out.IdentifierTypes, err = s.repo.ListIdentifierTypes(ctx, tx, rc.TenantID); err != nil {
			return err
		}
		if out.RelationshipTypes, err = s.repo.ListRelationshipTypes(ctx, tx, rc.TenantID); err != nil {
			return err
		}
		out.MembershipTypes, err = s.repo.ListMembershipTypes(ctx, tx, rc.TenantID)
		return err
	})
	return out, err
}
