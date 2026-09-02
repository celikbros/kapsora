package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	"github.com/celikbros/kapsora/internal/platform/ratelimit"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// AuditEnsurePartitions creates next month's partitions for both audit tables (daily).
// audit.ensure_month_partition is SECURITY DEFINER (migration 000011) so the application
// role may call it without CREATE privileges.
func AuditEnsurePartitions(pool *pgxpool.Pool) Job {
	return Job{
		Code:  "audit.ensure_partitions",
		Every: 24 * time.Hour,
		Run: func(ctx context.Context) (Metrics, error) {
			nextMonth := time.Now().UTC().AddDate(0, 1, 0)
			created := []string{}
			for _, table := range []string{"audit.event", "audit.access_event"} {
				var name string
				if err := pool.QueryRow(ctx, `SELECT audit.ensure_month_partition($1::regclass, $2::date)`, table, nextMonth).Scan(&name); err != nil {
					return nil, fmt.Errorf("ensure partition for %s: %w", table, err)
				}
				created = append(created, name)
			}
			return Metrics{"partitions": created}, nil
		},
	}
}

// OutboxRecoverStale returns abandoned PROCESSING events to PENDING (every 5 minutes).
func OutboxRecoverStale(d *outbox.Dispatcher) Job {
	return Job{
		Code:  "outbox.recover_stale",
		Every: 5 * time.Minute,
		Run: func(ctx context.Context) (Metrics, error) {
			n, err := d.RecoverStale(ctx)
			return Metrics{"recovered": n}, err
		},
	}
}

// IdempotencyPurge deletes expired idempotency records for every active tenant (hourly).
// Records are RLS-scoped, so the purge iterates tenants and runs inside each tenant context.
func IdempotencyPurge(pool *pgxpool.Pool, purge func(ctx context.Context, pool *pgxpool.Pool, tenantID uuidLike, before time.Time) (int64, error)) Job {
	return Job{
		Code:  "idempotency.purge",
		Every: time.Hour,
		Run: func(ctx context.Context) (Metrics, error) {
			rows, err := pool.Query(ctx, `SELECT id FROM platform.tenant WHERE status IN ('ACTIVE','SUSPENDED')`)
			if err != nil {
				return nil, fmt.Errorf("list tenants: %w", err)
			}
			defer rows.Close()
			var tenants []uuidLike
			for rows.Next() {
				var id uuidLike
				if err := rows.Scan(&id); err != nil {
					return nil, err
				}
				tenants = append(tenants, id)
			}
			var total int64
			for _, tenant := range tenants {
				n, err := purge(ctx, pool, tenant, time.Now())
				if err != nil {
					return Metrics{"deleted": total}, fmt.Errorf("tenant %s: %w", tenant, err)
				}
				total += n
			}
			return Metrics{"deleted": total, "tenants": len(tenants)}, nil
		},
	}
}

// RateLimitPurge drops buckets idle for more than a day (hourly).
func RateLimitPurge(l *ratelimit.Postgres) Job {
	return Job{
		Code:  "ratelimit.purge",
		Every: time.Hour,
		Run: func(ctx context.Context) (Metrics, error) {
			n, err := l.Purge(ctx, 24*time.Hour)
			return Metrics{"deleted": n}, err
		},
	}
}

// SessionCleanup removes expired BFF sessions (every 15 minutes).
func SessionCleanup(store identity.SessionStore) Job {
	return Job{
		Code:  "session.cleanup",
		Every: 15 * time.Minute,
		Run: func(ctx context.Context) (Metrics, error) {
			n, err := store.DeleteExpired(ctx, time.Now())
			return Metrics{"deleted": n}, err
		},
	}
}

// JobRunHistory is a small helper for operators and tests.
func JobRunHistory(ctx context.Context, pool *pgxpool.Pool, code string) (sqlcgen.LastJobRunRow, error) {
	return sqlcgen.New(pool).LastJobRun(ctx, code)
}
