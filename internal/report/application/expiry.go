package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/report/domain"
)

// expirySweepLimit is how many exports one pass of one tenant tidies. It is a bound rather than
// "all of them" so a tenant with a backlog does not hold the nightly job for an hour; the next
// pass takes the rest.
const expirySweepLimit = 200

// ExpireExports removes the file of every export past its TTL and marks the row EXPIRED.
//
// **The row outlives the file.** Marking rather than deleting is what makes "this export existed,
// carried this many rows and stopped being downloadable on this day" answerable — and it is what
// makes the sweep idempotent: a second pass finds nothing left to do rather than deleting a key
// twice.
//
// A file under legal hold keeps its bytes and its row is marked EXPIRED all the same. Those are
// two different promises and both are kept: the export stopped being downloadable when it said it
// would, and the bytes stay because something is holding them. The sweep counts the held ones
// rather than swallowing them, because "nothing expired" and "everything was held" are different
// answers.
func (s *Service) ExpireExports(ctx context.Context, now time.Time) (ExpiryReport, error) {
	tenants, err := s.activeTenants(ctx)
	if err != nil {
		return ExpiryReport{}, err
	}

	report := ExpiryReport{Tenants: len(tenants)}
	for _, tenantID := range tenants {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		tenantReport, err := s.expireTenant(ctx, tenantID, now.UTC())
		report.Expired += tenantReport.Expired
		report.Held += tenantReport.Held
		if err != nil {
			s.logger.Error("report: export expiry failed for a tenant",
				"tenant_id", tenantID, "error", err)
		}
	}
	return report, nil
}

func (s *Service) expireTenant(ctx context.Context, tenantID uuid.UUID, now time.Time,
) (ExpiryReport, error) {
	var due []ExpiringExport
	if err := s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		due, err = s.repo.ListExpiredExports(ctx, tx, tenantID, now, expirySweepLimit)
		return err
	}); err != nil {
		return ExpiryReport{}, err
	}

	report := ExpiryReport{}
	for _, export := range due {
		held, err := s.expireOne(ctx, tenantID, export)
		if err != nil {
			return report, err
		}
		report.Expired++
		if held {
			report.Held++
		}
	}
	return report, nil
}

// expireOne removes one export's bytes and marks its row. The file is removed before the row is
// marked, so a crash between the two leaves an export that is still EXPIRED-able on the next pass
// rather than a row that says EXPIRED beside a file that is still there.
func (s *Service) expireOne(ctx context.Context, tenantID uuid.UUID, export ExpiringExport,
) (held bool, err error) {
	if export.DocumentID != nil {
		purged, err := s.documents.Purge(ctx, tenantID, *export.DocumentID)
		if err != nil {
			return false, err
		}
		held = !purged
	}
	return held, s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		marked, err := s.repo.MarkExportExpired(ctx, tx, tenantID, export.ID)
		if err != nil || !marked {
			return err
		}
		return s.recordSystem(ctx, tx, tenantID, "report.export.expire", domain.AggregateExport,
			export.ID, map[string]any{
				"kind": export.Kind, "legal_hold": held,
				"had_file": export.DocumentID != nil,
			})
	})
}
