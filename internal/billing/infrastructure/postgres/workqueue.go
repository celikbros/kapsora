package billingpg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	workflowdomain "github.com/celikbros/kapsora/internal/workflow/domain"
)

// WorkItems raises, claims and closes the icmal's review in a queue named by its code, inside
// the caller's transaction.
//
// It is WP-I4-03's work item written from this side of the boundary rather than a call into the
// worklist service, because that service opens a transaction of its own: a work item that
// committed while the submit it belongs to rolled back would be work nobody can explain, and a
// decided batch whose item stayed open would be a queue full of work already done.
//
// The queue is found by code rather than by id. A module raising work cannot know the id of a
// row an operator created, and the code is what the queue is configured under.
type WorkItems struct {
	logger *slog.Logger
}

// NewWorkItems returns the work item port. A nil logger falls back to the default.
func NewWorkItems(logger *slog.Logger) *WorkItems {
	if logger == nil {
		logger = slog.Default()
	}
	return &WorkItems{logger: logger}
}

var _ application.WorkItemPort = (*WorkItems)(nil)

// Raise implements application.WorkItemPort.
//
// A tenant with no queue under that code, or one whose queue has been switched off, raises
// nothing and the command continues. That is deliberate: the icmal has been sent either way,
// refusing the submit would not make anybody watch the queue, and a provider told "your icmal
// cannot be sent because the payer has not configured a work queue" is a provider told about
// somebody else's configuration. The log line is what an operator sees instead.
func (w *WorkItems) Raise(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.RaiseWorkItem,
) error {
	queue, err := sqlcgen.New(tx).GetWorkQueueByCode(ctx, sqlcgen.GetWorkQueueByCodeParams{
		TenantID: tenantID, Code: in.QueueCode,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		w.logger.Warn("billing: no work queue to raise the icmal review into",
			"tenant_id", tenantID, "queue_code", in.QueueCode)
		return nil
	}
	if err != nil {
		return fmt.Errorf("billing: find work queue %s: %w", in.QueueCode, err)
	}
	if !queue.Active {
		w.logger.Warn("billing: the icmal review queue is not active",
			"tenant_id", tenantID, "queue_code", in.QueueCode)
		return nil
	}
	if _, err := sqlcgen.New(tx).CreateWorkItem(ctx, sqlcgen.CreateWorkItemParams{
		TenantID: tenantID, QueueID: queue.ID, AggregateType: in.AggregateType,
		AggregateID: in.AggregateID, Title: workItemTitle(in.Title),
		Priority: workflowdomain.DefaultPriority, ActorID: optUUID(in.ActorID),
	}); err != nil {
		return fmt.Errorf("billing: raise work item: %w", err)
	}
	return nil
}

// Claim implements application.WorkItemPort. An item somebody else is already holding is left
// alone: taking work off another person is a different command with its own permission, and it
// is not something opening an icmal should do behind anybody's back.
func (w *WorkItems) Claim(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	aggregateType string, aggregateID, assignee uuid.UUID, actorID *uuid.UUID,
) error {
	if assignee == uuid.Nil {
		return nil
	}
	if _, err := sqlcgen.New(tx).ClaimWorkItemForAggregate(ctx,
		sqlcgen.ClaimWorkItemForAggregateParams{
			TenantID: tenantID, AggregateType: aggregateType, AggregateID: aggregateID,
			AssigneeActorID: uuid.NullUUID{UUID: assignee, Valid: true},
			ActorID:         optUUID(actorID),
		}); err != nil {
		return fmt.Errorf("billing: claim work item: %w", err)
	}
	return nil
}

// Complete implements application.WorkItemPort. A tenant that never had a queue has no item to
// close, and zero rows affected is the ordinary answer rather than a failure.
func (w *WorkItems) Complete(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	aggregateType string, aggregateID uuid.UUID, outcomeCode string, actorID *uuid.UUID,
) error {
	outcome := outcomeCode
	if _, err := sqlcgen.New(tx).CompleteWorkItemForAggregate(ctx,
		sqlcgen.CompleteWorkItemForAggregateParams{
			TenantID: tenantID, AggregateType: aggregateType, AggregateID: aggregateID,
			OutcomeCode: &outcome, CompletedBy: optUUID(actorID), ActorID: optUUID(actorID),
		}); err != nil {
		return fmt.Errorf("billing: complete work item: %w", err)
	}
	return nil
}

// maxWorkItemTitle mirrors ck_work_item_title. A title here is a reference and two words, so
// this is never reached; cutting rather than failing is still the right answer, because an
// icmal that could not be sent over the length of a label would be a bad trade.
const maxWorkItemTitle = 200

func workItemTitle(s string) string {
	runes := []rune(s)
	if len(runes) <= maxWorkItemTitle {
		return s
	}
	return string(runes[:maxWorkItemTitle])
}
