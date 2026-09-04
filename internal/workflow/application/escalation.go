package application

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/workflow/domain"
)

// EscalateBatchSize is how many overdue items one pass of the job takes per tenant.
const EscalateBatchSize = 200

// EscalateOverdue moves every item past the clock it was given into its queue's escalation
// target, or marks it ESCALATED where its queue names none, tenant by tenant and in
// batches until nothing is left. It is the body of the workflow.escalate scheduler job.
//
// Running it twice escalates once. Three things make that true. The sweep only looks at
// items whose escalated_at is still null, so a second pass does not see what the first one
// finished. The write itself repeats that predicate, so a row another pass took between
// the listing and the write is skipped rather than written twice. And the status event is
// written only when the write actually changed a row, so one escalation leaves one event.
//
// The job never touches due_at. A late item stays late: moving it to somebody else's queue
// is what escalation means, not forgiving the delay.
func (s *Service) EscalateOverdue(ctx context.Context, now time.Time) (int, error) {
	tenants, err := s.activeTenants(ctx)
	if err != nil {
		return 0, err
	}
	escalated := 0
	for _, tenantID := range tenants {
		for {
			// The loop continues on how many rows were taken, not on how many were
			// escalated: a row another pass finished between the listing and the write is
			// skipped, and counting only the writes would end the sweep early with work
			// still waiting.
			listed, count, err := s.escalateBatch(ctx, tenantID, now)
			if err != nil {
				return escalated, err
			}
			escalated += count
			if listed < EscalateBatchSize {
				break
			}
		}
	}
	return escalated, nil
}

// escalateBatch escalates up to one batch inside a single tenant transaction and reports
// how many rows it took and how many of them it actually finished.
func (s *Service) escalateBatch(ctx context.Context, tenantID uuid.UUID, now time.Time) (listed, count int, err error) {
	err = db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID},
		func(ctx context.Context, tx pgx.Tx) error {
			due, err := s.repo.ListEscalatableItems(ctx, tx, tenantID, now, EscalateBatchSize)
			if err != nil {
				return err
			}
			listed = len(due)
			for _, item := range due {
				done, err := s.escalateOne(ctx, tx, tenantID, item, now)
				if err != nil {
					return err
				}
				if done {
					count++
				}
			}
			return nil
		})
	if err != nil {
		return 0, 0, fmt.Errorf("workflow: escalate tenant %s: %w", tenantID, err)
	}
	return listed, count, nil
}

// escalateOne moves or marks one overdue item and records the transition. It reports
// false when another pass finished the row between the listing and the write, which is not
// an error: the item is escalated either way.
func (s *Service) escalateOne(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	item EscalatableItem, now time.Time,
) (bool, error) {
	// The job acts for the tenant, not for a person: there is no actor, and the status
	// event says so by carrying none.
	rc := identity.RequestContext{TenantID: tenantID}
	current := ItemRecord{ID: item.ID, QueueID: item.QueueID, Status: item.Status}

	metadata := map[string]any{"from_queue_id": item.QueueID.String()}
	toStatus := item.Status
	var (
		done bool
		err  error
	)
	if item.EscalationQueueID != nil {
		metadata["to_queue_id"] = item.EscalationQueueID.String()
		done, err = s.repo.MoveItemToQueue(ctx, tx, tenantID, item.ID, *item.EscalationQueueID, item.QueueID, now)
	} else {
		// Nowhere to send it. The item is marked where it stands, which is what puts it
		// on the report an operations lead reads rather than quietly leaving it late in a
		// queue nobody is watching.
		toStatus = domain.StatusEscalated
		done, err = s.repo.MarkItemEscalated(ctx, tx, tenantID, item.ID, now)
	}
	if err != nil || !done {
		return false, err
	}

	if err := s.writeEvent(ctx, tx, rc, current, toStatus, domain.CommandEscalate,
		nil, nil, metadata); err != nil {
		return false, err
	}
	s.logger.Info("work item escalated", "tenant_id", tenantID, "work_item_id", item.ID,
		"from_queue_id", item.QueueID, "to_status", toStatus)
	return true, nil
}

// activeTenants lists the tenants the job walks, outside any tenant transaction.
func (s *Service) activeTenants(ctx context.Context) ([]uuid.UUID, error) {
	conn, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("workflow: begin tenant listing: %w", err)
	}
	defer func() { _ = conn.Rollback(ctx) }()
	return s.repo.ActiveTenants(ctx, conn)
}
