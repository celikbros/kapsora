package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/document/domain"
)

// PurgeReport is what one retention sweep did.
type PurgeReport struct {
	Purged int
	// Held counts documents the sweep left alone because a legal hold covers them. It is
	// reported rather than logged away: "nothing was purged" and "everything was held" are
	// different answers, and an operator needs to be able to tell them apart.
	Held int
}

// PurgeExpired removes the bytes of stored documents older than the cutoff, skipping every
// one a legal hold covers. **An object under legal hold is never deleted** — that is the
// whole reason document.legal_hold exists, and this is the sweep the promise is made
// against (WP-I4-04 section 2.3).
//
// The row outlives the bytes. Purging sets purged_at rather than deleting anything, so
// "this document existed and was removed on this day" stays answerable, and a second sweep
// finds nothing left to do rather than deleting a key twice.
func (s *Service) PurgeExpired(ctx context.Context, before time.Time, limit int) (PurgeReport, error) {
	if limit <= 0 {
		limit = 200
	}
	var tenants []uuid.UUID
	if err := s.readTenants(ctx, &tenants); err != nil {
		return PurgeReport{}, err
	}

	report := PurgeReport{}
	for _, tenantID := range tenants {
		tenantReport, err := s.purgeTenant(ctx, tenantID, before, limit)
		report.Purged += tenantReport.Purged
		report.Held += tenantReport.Held
		if err != nil {
			return report, err
		}
	}
	return report, nil
}

func (s *Service) readTenants(ctx context.Context, out *[]uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	*out, err = s.repo.ActiveTenants(ctx, tx)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// purgeTenant sweeps one tenant. Each document is decided and purged in its own
// transaction, so a hold placed halfway through the sweep is seen by the documents after
// it rather than by none of them.
func (s *Service) purgeTenant(ctx context.Context, tenantID uuid.UUID, before time.Time, limit int) (PurgeReport, error) {
	var candidates []ObjectRecord
	err := s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		candidates, err = s.repo.ListPurgeable(ctx, tx, tenantID, before, limit)
		return err
	})
	if err != nil {
		return PurgeReport{}, err
	}

	report := PurgeReport{}
	for _, object := range candidates {
		purged, err := s.purgeOne(ctx, tenantID, object)
		switch {
		case errors.Is(err, ErrLegalHold):
			report.Held++
		case err != nil:
			return report, err
		case purged:
			report.Purged++
		}
	}
	return report, nil
}

// purgeOne removes the bytes of one document unless something holds it. The hold is read
// inside the same transaction that marks the row purged, so a hold placed while the sweep
// is running either wins the row or arrives after it was already recorded as purged —
// never in between.
func (s *Service) purgeOne(ctx context.Context, tenantID uuid.UUID, object ObjectRecord) (bool, error) {
	purged := false
	err := s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockObject(ctx, tx, tenantID, object.ID)
		if err != nil {
			return err
		}
		if current.PurgedAt != nil {
			return nil
		}
		held, err := s.repo.HeldByLegalHold(ctx, tx, tenantID, object.ID)
		if err != nil {
			return err
		}
		if held {
			return ErrLegalHold
		}
		// A duplicate points at somebody else's bytes; purging it must not take the
		// canonical copy away from the object that owns it.
		if current.DuplicateOfObjectID == nil {
			if err := s.store.Remove(ctx, s.storage.SecureBucket, current.ObjectKey); err != nil {
				return storeError("purge secure object", err)
			}
		}
		if err := s.repo.MarkPurged(ctx, tx, tenantID, current.ID, s.now()); err != nil {
			return err
		}
		purged = true
		return s.audit.Record(ctx, tx, auditEvent(tenantID, current.ID, "document.retention.purge",
			map[string]any{
				"classification": current.Classification,
				"duplicate":      current.DuplicateOfObjectID != nil,
			}))
	})
	if err != nil {
		return false, err
	}
	return purged, nil
}

// auditEvent builds a system-written business audit row: one with a tenant and no actor,
// because the sweep and the scanner act for nobody in particular.
func auditEvent(tenantID, resourceID uuid.UUID, action string, detail map[string]any) audit.Event {
	return audit.Event{
		TenantID: nullUUID(tenantID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: domain.AggregateType,
		ResourceID: nullUUID(resourceID), Outcome: audit.OutcomeSuccess, Detail: detail,
	}
}
