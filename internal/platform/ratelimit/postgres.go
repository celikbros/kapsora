package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Postgres keeps buckets in system.rate_limit_bucket so every API instance shares them.
// Each decision is one short transaction with a row lock; the database clock is used so
// instances with skewed clocks agree.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres returns a shared limiter.
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Allow implements Limiter.
func (l *Postgres) Allow(ctx context.Context, key string, p Policy) (Decision, error) {
	if p.Burst <= 0 || p.PerMinute <= 0 {
		return Decision{Allowed: true}, nil
	}
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return Decision{}, fmt.Errorf("ratelimit: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := sqlcgen.New(tx)
	if err := q.EnsureRateLimitBucket(ctx, sqlcgen.EnsureRateLimitBucketParams{BucketKey: key, Tokens: float64(p.Burst)}); err != nil {
		return Decision{}, fmt.Errorf("ratelimit: ensure bucket: %w", err)
	}
	row, err := q.LockRateLimitBucket(ctx, key)
	if err != nil {
		return Decision{}, fmt.Errorf("ratelimit: lock bucket: %w", err)
	}
	tokens, d := apply(row.Tokens, row.UpdatedAt, row.DbNow, p)
	if err := q.UpdateRateLimitBucket(ctx, sqlcgen.UpdateRateLimitBucketParams{BucketKey: key, Tokens: tokens, UpdatedAt: row.DbNow}); err != nil {
		return Decision{}, fmt.Errorf("ratelimit: update bucket: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Decision{}, fmt.Errorf("ratelimit: commit: %w", err)
	}
	return d, nil
}

// Purge deletes buckets idle for longer than idle; the scheduler job ratelimit.purge calls it.
func (l *Postgres) Purge(ctx context.Context, idle time.Duration) (int64, error) {
	return sqlcgen.New(l.pool).PurgeIdleRateLimitBuckets(ctx, time.Now().Add(-idle))
}

var _ Limiter = (*Postgres)(nil)

// ensure pgx import is used for the transaction type in signatures of future helpers.
var _ pgx.Tx = nil
