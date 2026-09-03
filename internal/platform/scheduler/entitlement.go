package scheduler

import (
	"context"
	"time"

	"github.com/celikbros/kapsora/internal/benefit/ledger"
)

// EntitlementReservationExpire releases expired holds every minute (WP-I2-03 section
// 2.6). The service walks the tenants in batches of ledger.ExpireBatchSize and derives
// the movement key from the reservation, so a re-run after a crash changes nothing.
func EntitlementReservationExpire(svc *ledger.Service) Job {
	return Job{
		Code:  "entitlement.reservation.expire",
		Every: time.Minute,
		Run: func(ctx context.Context) (Metrics, error) {
			released, err := svc.ExpireReservations(ctx, time.Now().UTC())
			return Metrics{"released": released}, err
		},
	}
}

// EntitlementReconcile proves daily that every account balance equals the sum of its
// ledger movements (v1.2 section 31.2). An account that disagrees is frozen, audited and
// announced on the outbox in one transaction.
func EntitlementReconcile(svc *ledger.Service) Job {
	return Job{
		Code:  "entitlement.reconcile",
		Every: 24 * time.Hour,
		Run: func(ctx context.Context) (Metrics, error) {
			m, err := svc.Reconcile(ctx, time.Now().UTC())
			return Metrics{"tenants": m.Tenants, "accounts": m.Accounts, "drifts": m.Drifts}, err
		},
	}
}
