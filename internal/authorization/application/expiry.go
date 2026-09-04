package application

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/authorization/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// ExpireBatchSize is how many authorizations one pass of the job expires per tenant.
const ExpireBatchSize = 200

// ExpireAuthorizations releases the holds of every authorization past its end and marks
// it EXPIRED, tenant by tenant and in batches, until nothing is left. It is the body of
// the authorization.expire scheduler job.
//
// Running it twice releases once. Two things make that true: the sweep only looks at
// authorizations that still hold entitlement, so a second pass does not see the rows the
// first one finished, and every release carries an idempotency key derived from the line,
// so even a movement that were somehow attempted twice would be applied once.
//
// This job is the only owner of these holds. Their reservations are created without an
// `expires_at`, deliberately: the ledger's own reservation sweep would otherwise release
// them at a moment this package did not choose, and an extension that moved the
// authorization's end forward could not move that moment with it.
func (s *Service) ExpireAuthorizations(ctx context.Context, now time.Time) (int, error) {
	tenants, err := s.activeTenants(ctx)
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, tenantID := range tenants {
		for {
			// The loop continues on how many rows were taken, not on how many were
			// expired: a row another pass finished between the listing and the write is
			// skipped, and counting only the writes would end the sweep early with work
			// still waiting.
			listed, count, err := s.expireBatch(ctx, tenantID, now)
			if err != nil {
				return expired, err
			}
			expired += count
			if listed < ExpireBatchSize {
				break
			}
		}
	}
	return expired, nil
}

// expireBatch expires up to one batch inside a single tenant transaction and reports how
// many rows it took and how many of them it actually finished.
func (s *Service) expireBatch(ctx context.Context, tenantID uuid.UUID, now time.Time) (listed, count int, err error) {
	err = db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID},
		func(ctx context.Context, tx pgx.Tx) error {
			ids, err := s.repo.ListExpirableAuthorizations(ctx, tx, tenantID, now, ExpireBatchSize)
			if err != nil {
				return err
			}
			listed = len(ids)
			for _, id := range ids {
				released, err := s.releaseHolds(ctx, tx, tenantID, uuid.Nil, id, domain.StatusExpired)
				if err != nil {
					return err
				}
				marked, err := s.repo.MarkAuthorizationExpired(ctx, tx, tenantID, id)
				if err != nil {
					return err
				}
				if !marked {
					// Another pass finished this row between the listing and here. It is
					// not an error: the entitlement is back either way.
					continue
				}
				s.logger.Info("authorization expired", "tenant_id", tenantID, "authorization_id", id,
					"released", released.String())
				count++
			}
			return nil
		})
	if err != nil {
		return 0, 0, fmt.Errorf("authorization: expire tenant %s: %w", tenantID, err)
	}
	return listed, count, nil
}

// activeTenants lists the tenants the job walks, outside any tenant transaction.
func (s *Service) activeTenants(ctx context.Context) ([]uuid.UUID, error) {
	conn, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("authorization: begin tenant listing: %w", err)
	}
	defer func() { _ = conn.Rollback(ctx) }()
	return s.repo.ActiveTenants(ctx, conn)
}
