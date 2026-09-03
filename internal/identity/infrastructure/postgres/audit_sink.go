package identitypg

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity/application"
)

// NewAuditSink returns an application.AuditSink that writes each authentication event in
// its own short transaction. Login happens before a tenant is chosen, so this is not a
// tenant-bound transaction; audit.event carries a nullable tenant_id for exactly this case.
//
// A failed audit write is logged and swallowed: it must not stop a user from logging out.
func NewAuditSink(pool *pgxpool.Pool, recorder audit.Recorder, logger *slog.Logger) application.AuditSink {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, ev audit.Event) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("audit: begin failed", "action", ev.ActionCode, "error", err)
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if err := recorder.Record(ctx, tx, ev); err != nil {
			logger.Error("audit: write failed", "action", ev.ActionCode, "error", err)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			logger.Error("audit: commit failed", "action", ev.ActionCode, "error", err)
			return err
		}
		return nil
	}
}
