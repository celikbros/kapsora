// Package db owns the PostgreSQL connection pool and the tenant-bound
// transaction helper. Every tenant-scoped query runs inside WithTenantTx so the
// RLS context (app.tenant_id, app.actor_id) is always set with SET LOCAL
// semantics and never leaks past the transaction (v1.2 section 14.4, 16.11).
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolOptions tunes the pool; zero values mean pgx defaults.
type PoolOptions struct {
	ApplicationName string
	MaxConns        int32
}

// NewPool connects and verifies the connection with a ping.
func NewPool(ctx context.Context, databaseURL string, opts PoolOptions) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if opts.ApplicationName != "" {
		cfg.ConnConfig.RuntimeParams["application_name"] = opts.ApplicationName
	}
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// TenantContext identifies the tenant and actor a transaction acts for.
type TenantContext struct {
	TenantID uuid.UUID
	// ActorID may be uuid.Nil for system jobs; the session variable is then empty.
	ActorID uuid.UUID
}

// ErrNoTenant is returned when a tenant transaction is requested without a tenant.
var ErrNoTenant = errors.New("tenant context requires a tenant id")

// WithTenantTx runs fn inside a transaction bound to tc. The RLS session variables
// are set with set_config(..., is_local => true), which is SET LOCAL: they vanish
// at COMMIT/ROLLBACK, so pooled connections never carry a stale tenant.
func WithTenantTx(ctx context.Context, pool *pgxpool.Pool, tc TenantContext, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if tc.TenantID == uuid.Nil {
		return ErrNoTenant
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tenant tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	if err := BindTenant(ctx, tx, tc); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tenant tx: %w", err)
	}
	return nil
}

// BindTenant sets the RLS session variables on an already open transaction.
func BindTenant(ctx context.Context, tx pgx.Tx, tc TenantContext) error {
	actor := ""
	if tc.ActorID != uuid.Nil {
		actor = tc.ActorID.String()
	}
	_, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id', $1, true), set_config('app.actor_id', $2, true)`,
		tc.TenantID.String(), actor)
	if err != nil {
		return fmt.Errorf("bind tenant context: %w", err)
	}
	return nil
}
