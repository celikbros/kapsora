package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	workflowdomain "github.com/celikbros/kapsora/internal/workflow/domain"
)

// WorkItems raises work into a queue named by its code, inside the caller's transaction.
//
// It is WP-I4-03's work item written from this side of the boundary rather than a call into
// the worklist service, because that service opens a transaction of its own: a work item that
// committed while the review it belongs to rolled back would be work nobody can explain, and
// a review that committed without its item would be a dispute nobody is watching.
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
// nothing and the command continues. That is deliberate: the dispute has been recorded either
// way, refusing the review would not make anybody watch the queue, and a member told "your
// objection cannot be recorded because the payer has not configured a work queue" is a member
// told about somebody else's configuration. The log line is what an operator sees instead.
func (w *WorkItems) Raise(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.RaiseWorkItem,
) error {
	queue, err := sqlcgen.New(tx).GetWorkQueueByCode(ctx, sqlcgen.GetWorkQueueByCodeParams{
		TenantID: tenantID, Code: in.QueueCode,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		w.logger.Warn("accommodation: no work queue to raise the dispute into",
			"tenant_id", tenantID, "queue_code", in.QueueCode)
		return nil
	}
	if err != nil {
		return fmt.Errorf("accommodation: find work queue %s: %w", in.QueueCode, err)
	}
	if !queue.Active {
		w.logger.Warn("accommodation: the reservation review queue is not active",
			"tenant_id", tenantID, "queue_code", in.QueueCode)
		return nil
	}
	if _, err := sqlcgen.New(tx).CreateWorkItem(ctx, sqlcgen.CreateWorkItemParams{
		TenantID: tenantID, QueueID: queue.ID, AggregateType: in.AggregateType,
		AggregateID: in.AggregateID, Title: workItemTitle(in.Title),
		Priority: workflowdomain.DefaultPriority, ActorID: nullUUID(in.ActorID),
	}); err != nil {
		return fmt.Errorf("accommodation: raise work item: %w", err)
	}
	return nil
}

// maxWorkItemTitle mirrors ck_work_item_title. A title here is a reference and two words, so
// this is never reached; cutting rather than failing is still the right answer, because a
// review that could not be recorded over the length of a label would be a bad trade.
const maxWorkItemTitle = 200

func workItemTitle(s string) string {
	runes := []rune(s)
	if len(runes) <= maxWorkItemTitle {
		return s
	}
	return string(runes[:maxWorkItemTitle])
}
