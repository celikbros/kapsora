package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/workflow/domain"
)

// RaiseInput is one piece of work handed to a queue. There is no HTTP route for it: work
// is raised by the module that produced it — a request that needs a reviewer, a claim that
// needs an adjudicator — so that every item can say why it exists.
type RaiseInput struct {
	QueueID       uuid.UUID
	AggregateType string
	AggregateID   uuid.UUID
	Title         string
	Priority      int
}

// Raise puts one piece of work into a queue and gives it the queue's clock.
//
// The clock is copied here and nowhere else. `sla_minutes_snapshot` and `due_at` are
// written by the insert from the queue as it stands at this moment, and no later read
// goes back to the queue for either: retuning a queue must not make yesterday's items
// late, or make late ones on time (v1.2 11.8).
func (s *Service) Raise(ctx context.Context, rc identity.RequestContext, in RaiseInput) (ItemRecord, error) {
	priority := in.Priority
	if priority == 0 {
		priority = domain.DefaultPriority
	}
	command := domain.NewItem{
		QueueID: in.QueueID.String(), AggregateType: in.AggregateType,
		AggregateID: in.AggregateID.String(), Title: in.Title, Priority: priority,
	}
	if err := command.Validate(); err != nil {
		return ItemRecord{}, err
	}

	scope := scopeOf(rc)
	var created ItemRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		// The queue is read through the caller's own boundary first, so raising work into
		// a queue the caller cannot see is a "not found" rather than a row it could never
		// read back.
		queue, err := s.repo.GetQueue(ctx, tx, rc.TenantID, in.QueueID, scope)
		if err != nil {
			return err
		}
		if !queue.Active {
			return ErrQueueInactive
		}
		record, err := s.repo.CreateItem(ctx, tx, rc.TenantID, NewItemRow{
			QueueID: in.QueueID, AggregateType: in.AggregateType, AggregateID: in.AggregateID,
			Title: in.Title, Priority: priority, ActorID: actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}
		created = record
		return s.record(ctx, tx, rc, "work_item.raise", "WORK_ITEM", record.ID, map[string]any{
			"queue_id": record.QueueID, "aggregate_type": record.AggregateType,
			"aggregate_id": record.AggregateID, "priority": record.Priority,
			"sla_minutes": slaValue(record.SLAMinutesSnapshot),
		})
	})
	if err != nil {
		return ItemRecord{}, err
	}
	return created, nil
}

// GetItem reads one work item. A caller outside the queue boundary is answered "not
// found" rather than "forbidden": that such an item exists is somebody else's business.
func (s *Service) GetItem(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (ItemRecord, error) {
	scope := scopeOf(rc)
	var record ItemRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		record, err = s.repo.GetItem(ctx, tx, rc.TenantID, id, scope)
		return err
	})
	if err != nil {
		return ItemRecord{}, err
	}
	return record, nil
}

// ListItems answers one keyset page of work items: the morning list.
func (s *Service) ListItems(ctx context.Context, rc identity.RequestContext,
	filter ItemFilter,
) (ItemPage, error) {
	after, pageSize, err := s.paging(filter.Cursor, filter.Limit)
	if err != nil {
		return ItemPage{}, err
	}
	query := ItemQuery{
		Scope: scopeOf(rc), QueueID: filter.QueueID, Status: filter.Status,
		AggregateType: filter.AggregateType, AggregateID: filter.AggregateID,
		Overdue: filter.Overdue, AsOf: s.now(), After: after, PageSize: pageSize + 1,
	}
	if filter.AssignedToMe {
		query.AssigneeActorID = actorPtr(rc.Principal.ActorID)
	}
	var rows []ItemRecord
	if err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err = s.repo.ListItems(ctx, tx, rc.TenantID, query)
		return err
	}); err != nil {
		return ItemPage{}, err
	}
	page := ItemPage{Items: rows}
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		page.NextCursor = s.cursors.Encode(itemCursor(page.Items[len(page.Items)-1]))
	}
	return page, nil
}

// ClaimItem takes one piece of work, or reports who already has it.
//
// The whole claim is one conditional UPDATE: this item, still OPEN, still at the version
// the caller read. Two callers racing on one item both run it; the second waits on the row
// lock, re-evaluates the predicate against the row the first one committed and changes
// nothing. Zero rows affected is therefore never a partial write, and the answer to it is
// to read the row back and say who won — never to try again with a fresh version, which
// would be a claim nobody asked for.
func (s *Service) ClaimItem(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, expected int64,
) (ItemRecord, error) {
	if rc.Principal.ActorID == uuid.Nil {
		return ItemRecord{}, errors.New("workflow: claiming needs an actor")
	}
	scope := scopeOf(rc)
	var claimed ItemRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		// The item is read once, and only to decide whether this caller may see it at
		// all. Its status is deliberately not consulted: a pre-read that decided
		// claimability would be a second, weaker guard, and a race that slipped past it
		// would be decided by whichever caller happened to read last.
		if _, err := s.repo.GetItem(ctx, tx, rc.TenantID, id, scope); err != nil {
			return err
		}
		took, err := s.repo.ClaimItem(ctx, tx, rc.TenantID, id, rc.Principal.ActorID, expected)
		if err != nil {
			return err
		}
		if !took {
			return s.claimLost(ctx, tx, rc, id, scope)
		}
		claimed, err = s.repo.GetItem(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		// The update matched only an OPEN row, so that is what it moved from, whatever a
		// read before it might have said.
		from := ItemRecord{ID: id, QueueID: claimed.QueueID, Status: domain.StatusOpen}
		if err := s.writeEvent(ctx, tx, rc, from, domain.StatusClaimed, domain.CommandClaim,
			nil, nil, map[string]any{"assignee_actor_id": rc.Principal.ActorID.String()}); err != nil {
			return err
		}
		return s.record(ctx, tx, rc, "work_item.claim", "WORK_ITEM", id, map[string]any{
			"queue_id": claimed.QueueID, "assignee_actor_id": rc.Principal.ActorID,
		})
	})
	if err != nil {
		return ItemRecord{}, err
	}
	return claimed, nil
}

// claimLost reads the row back to say what actually happened to it. The re-read is a new
// statement, so in READ COMMITTED it sees the row the winner committed.
func (s *Service) claimLost(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID, scope Scope,
) error {
	latest, err := s.repo.GetItem(ctx, tx, rc.TenantID, id, scope)
	if err != nil {
		return err
	}
	return notClaimable(id, latest)
}

// notClaimable says why an item could not be taken. Still OPEN means nobody took it and
// the caller's If-Match is simply stale — saying "somebody claimed it" there would name
// nobody. CLAIMED means somebody won, and the answer names them.
func notClaimable(id uuid.UUID, latest ItemRecord) error {
	switch latest.Status {
	case domain.StatusOpen:
		return ErrVersionMismatch
	case domain.StatusClaimed:
		return &AlreadyClaimedError{
			WorkItemID: id, AssigneeActorID: latest.AssigneeActorID, Status: latest.Status,
		}
	default:
		return ErrTransitionInvalid
	}
}

// ReleaseItem puts a piece of work back in its queue. Only the person holding it may put
// it down; taking work off somebody else is reassignment, which is a different command
// with a different permission.
func (s *Service) ReleaseItem(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, reasonCode, reasonText string, expected int64,
) (ItemRecord, error) {
	if err := domain.ValidateReason("reason", reasonCode, reasonText); err != nil {
		return ItemRecord{}, err
	}
	scope := scopeOf(rc)
	var released ItemRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.GetItem(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		if _, ok := domain.Target(domain.CommandRelease, current.Status); !ok {
			return ErrTransitionInvalid
		}
		if current.AssigneeActorID == nil || *current.AssigneeActorID != rc.Principal.ActorID {
			return ErrNotAssignee
		}
		done, err := s.repo.ReleaseItem(ctx, tx, rc.TenantID, id, actorPtr(rc.Principal.ActorID), expected)
		if err != nil {
			return err
		}
		if !done {
			return ErrVersionMismatch
		}
		released, err = s.repo.GetItem(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		if err := s.writeEvent(ctx, tx, rc, current, domain.StatusOpen, domain.CommandRelease,
			optionalPtr(reasonCode), optionalPtr(reasonText),
			map[string]any{"previous_assignee_actor_id": rc.Principal.ActorID.String()}); err != nil {
			return err
		}
		return s.record(ctx, tx, rc, "work_item.release", "WORK_ITEM", id, map[string]any{
			"queue_id": released.QueueID, "reason_code": reasonCode,
		})
	})
	if err != nil {
		return ItemRecord{}, err
	}
	return released, nil
}

// ReassignItem gives a piece of work to somebody else. The person it was taken from is
// recorded in the status event, because "why is this not mine any more" is a question
// somebody will ask and the item itself no longer knows the answer.
func (s *Service) ReassignItem(ctx context.Context, rc identity.RequestContext,
	id, assigneeID uuid.UUID, reasonCode, reasonText string, expected int64,
) (ItemRecord, error) {
	if assigneeID == uuid.Nil {
		return ItemRecord{}, fieldError("assigneeActorId", "REQUIRED", "atanacak kullanıcı zorunlu")
	}
	if err := domain.ValidateReason("reason", reasonCode, reasonText); err != nil {
		return ItemRecord{}, err
	}
	scope := scopeOf(rc)
	var reassigned ItemRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.GetItem(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		if _, ok := domain.Target(domain.CommandReassign, current.Status); !ok {
			return ErrTransitionInvalid
		}
		done, err := s.repo.ReassignItem(ctx, tx, rc.TenantID, id, assigneeID,
			actorPtr(rc.Principal.ActorID), expected)
		if err != nil {
			return err
		}
		if !done {
			return ErrVersionMismatch
		}
		reassigned, err = s.repo.GetItem(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		metadata := map[string]any{"assignee_actor_id": assigneeID.String()}
		if current.AssigneeActorID != nil {
			metadata["previous_assignee_actor_id"] = current.AssigneeActorID.String()
		}
		if err := s.writeEvent(ctx, tx, rc, current, domain.StatusClaimed, domain.CommandReassign,
			optionalPtr(reasonCode), optionalPtr(reasonText), metadata); err != nil {
			return err
		}
		detail := map[string]any{
			"queue_id": reassigned.QueueID, "assignee_actor_id": assigneeID,
			"reason_code": reasonCode,
		}
		if current.AssigneeActorID != nil {
			detail["previous_assignee_actor_id"] = *current.AssigneeActorID
		}
		return s.record(ctx, tx, rc, "work_item.reassign", "WORK_ITEM", id, detail)
	})
	if err != nil {
		return ItemRecord{}, err
	}
	return reassigned, nil
}

// CompleteItem finishes a piece of work with what it decided. Only the person holding it
// may finish it: an outcome recorded by somebody who never had the item is an outcome
// nobody can be asked about.
func (s *Service) CompleteItem(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, outcomeCode, comment string, expected int64,
) (ItemRecord, error) {
	if err := domain.ValidateOutcome(outcomeCode); err != nil {
		return ItemRecord{}, err
	}
	scope := scopeOf(rc)
	var completed ItemRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.GetItem(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		if _, ok := domain.Target(domain.CommandComplete, current.Status); !ok {
			return ErrTransitionInvalid
		}
		if current.AssigneeActorID == nil || *current.AssigneeActorID != rc.Principal.ActorID {
			return ErrNotAssignee
		}
		done, err := s.repo.CompleteItem(ctx, tx, rc.TenantID, id, outcomeCode,
			actorPtr(rc.Principal.ActorID), expected)
		if err != nil {
			return err
		}
		if !done {
			return ErrVersionMismatch
		}
		completed, err = s.repo.GetItem(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		if comment != "" {
			if _, err := s.addComment(ctx, tx, rc, completed, domain.NewComment{
				Visibility: "INTERNAL", Body: comment,
			}); err != nil {
				return err
			}
		}
		if err := s.writeEvent(ctx, tx, rc, current, domain.StatusCompleted, domain.CommandComplete,
			optionalPtr(outcomeCode), nil, map[string]any{"outcome_code": outcomeCode}); err != nil {
			return err
		}
		return s.record(ctx, tx, rc, "work_item.complete", "WORK_ITEM", id, map[string]any{
			"queue_id": completed.QueueID, "outcome_code": outcomeCode,
			"overdue": completed.Overdue(timeOrZero(completed.CompletedAt)),
		})
	})
	if err != nil {
		return ItemRecord{}, err
	}
	return completed, nil
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
