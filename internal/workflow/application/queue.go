package application

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/workflow/domain"
)

// NewQueueInput is the createWorkQueue command.
type NewQueueInput struct {
	Code              string
	Name              string
	DomainCode        string
	AssignmentPolicy  string
	SLAMinutes        *int
	EscalationQueueID *uuid.UUID
	Active            *bool
	// RequiredPermission is the permission this queue's work takes; empty means PermissionRead,
	// which is every worklist reader.
	RequiredPermission string
}

// CreateQueue defines a place work waits and the clock it waits against.
func (s *Service) CreateQueue(ctx context.Context, rc identity.RequestContext,
	in NewQueueInput,
) (QueueRecord, error) {
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	required := in.RequiredPermission
	if required == "" {
		required = PermissionRead
	}
	command := domain.NewQueue{
		Code: in.Code, Name: in.Name, DomainCode: in.DomainCode,
		AssignmentPolicy: policyOrDefault(in.AssignmentPolicy),
		SLAMinutes:       in.SLAMinutes, Active: active, RequiredPermission: required,
	}
	if err := command.Validate(); err != nil {
		return QueueRecord{}, err
	}

	scope := scopeOf(rc)
	var created QueueRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		// The escalation target is read through the caller's own boundary, so a queue
		// cannot be made to escalate into one the caller cannot see.
		if in.EscalationQueueID != nil {
			if _, err := s.repo.GetQueue(ctx, tx, rc.TenantID, *in.EscalationQueueID, scope); err != nil {
				return escalationTargetError(err)
			}
		}
		record, err := s.repo.CreateQueue(ctx, tx, rc.TenantID, NewQueueRow{
			Code: command.Code, Name: command.Name, DomainCode: command.DomainCode,
			AssignmentPolicy: command.AssignmentPolicy, SLAMinutes: command.SLAMinutes,
			EscalationQueueID: in.EscalationQueueID, Active: command.Active,
			RequiredPermission: command.RequiredPermission,
			ActorID:            actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}
		created = record
		return s.record(ctx, tx, rc, "work_queue.create", "WORK_QUEUE", record.ID, map[string]any{
			"queue_code": record.Code, "domain_code": record.DomainCode,
			"assignment_policy": record.AssignmentPolicy, "sla_minutes": slaValue(record.SLAMinutes),
			"required_permission": record.RequiredPermission,
		})
	})
	if err != nil {
		return QueueRecord{}, err
	}
	return created, nil
}

// PatchQueue applies a merge patch to a queue. Changing `slaMinutes` changes what items
// raised from now on are given and nothing else: the items already in the queue keep the
// clock they were given, which is the whole point of the snapshot.
func (s *Service) PatchQueue(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, patch domain.QueuePatch, expected int64,
) (QueueRecord, error) {
	if err := patch.Validate(); err != nil {
		return QueueRecord{}, err
	}
	scope := scopeOf(rc)
	var updated QueueRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.GetQueue(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		next := applyQueuePatch(current, patch)
		if next.EscalationQueueID != nil {
			if *next.EscalationQueueID == id {
				return ErrEscalationCycle
			}
			if _, err := s.repo.GetQueue(ctx, tx, rc.TenantID, *next.EscalationQueueID, scope); err != nil {
				return escalationTargetError(err)
			}
		}
		if err := s.repo.UpdateQueue(ctx, tx, rc.TenantID, id, QueueUpdateRow{
			Name: next.Name, AssignmentPolicy: next.AssignmentPolicy,
			SLAMinutes: next.SLAMinutes, EscalationQueueID: next.EscalationQueueID,
			Active: next.Active, RequiredPermission: next.RequiredPermission,
			ActorID: actorPtr(rc.Principal.ActorID),
		}, expected); err != nil {
			return err
		}
		updated, err = s.repo.GetQueue(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, rc, "work_queue.update", "WORK_QUEUE", id, map[string]any{
			"queue_code": updated.Code, "sla_minutes": slaValue(updated.SLAMinutes),
			"active": updated.Active, "required_permission": updated.RequiredPermission,
		})
	})
	if err != nil {
		return QueueRecord{}, err
	}
	return updated, nil
}

// GetQueue reads one queue. A caller outside the boundary is answered "not found": that
// such a queue exists at all is somebody else's business.
func (s *Service) GetQueue(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (QueueRecord, error) {
	scope := scopeOf(rc)
	var record QueueRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		record, err = s.repo.GetQueue(ctx, tx, rc.TenantID, id, scope)
		return err
	})
	if err != nil {
		return QueueRecord{}, err
	}
	return record, nil
}

// ListQueues answers one keyset page of queues.
func (s *Service) ListQueues(ctx context.Context, rc identity.RequestContext,
	filter QueueFilter,
) (QueuePage, error) {
	after, pageSize, err := s.paging(filter.Cursor, filter.Limit)
	if err != nil {
		return QueuePage{}, err
	}
	query := QueueQuery{
		Scope: scopeOf(rc), DomainCode: filter.DomainCode, Active: filter.Active,
		After: after, PageSize: pageSize + 1,
	}
	var rows []QueueRecord
	if err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err = s.repo.ListQueues(ctx, tx, rc.TenantID, query)
		return err
	}); err != nil {
		return QueuePage{}, err
	}
	page := QueuePage{Items: rows}
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		page.NextCursor = s.cursors.Encode(queueCursor(page.Items[len(page.Items)-1]))
	}
	return page, nil
}

// applyQueuePatch folds a merge patch onto the row that was read, so the update writes a
// whole row and a field the caller did not mention keeps the value it had.
func applyQueuePatch(current QueueRecord, patch domain.QueuePatch) QueueRecord {
	next := current
	if patch.Name != nil {
		next.Name = *patch.Name
	}
	if patch.AssignmentPolicy != nil {
		next.AssignmentPolicy = *patch.AssignmentPolicy
	}
	if patch.SLAMinutes != nil {
		next.SLAMinutes = *patch.SLAMinutes
	}
	if patch.EscalationQueueID != nil {
		next.EscalationQueueID = parseQueueID(*patch.EscalationQueueID)
	}
	if patch.Active != nil {
		next.Active = *patch.Active
	}
	if patch.RequiredPermission != nil {
		next.RequiredPermission = *patch.RequiredPermission
	}
	return next
}

// parseQueueID turns the patched text form back into an id; an unparseable one has already
// been refused by the transport, which is where a caller's text is checked.
func parseQueueID(raw *string) *uuid.UUID {
	if raw == nil {
		return nil
	}
	id, err := uuid.Parse(*raw)
	if err != nil {
		return nil
	}
	return &id
}

// escalationTargetError turns "the target is not there" into a field error on the field
// the caller actually sent, rather than a 404 about a queue they did not ask for.
func escalationTargetError(err error) error {
	if errors.Is(err, ErrQueueNotFound) {
		return fieldError("escalationQueueId", "NOT_FOUND", "yönlendirilecek kuyruk bulunamadı")
	}
	return err
}

func policyOrDefault(policy string) string {
	if policy == "" {
		return "MANUAL"
	}
	return policy
}

// slaValue renders an optional SLA for an audit detail: -1 stands for "no clock", because
// a missing key and a zero would both read as "somebody set it to nothing".
func slaValue(minutes *int) int {
	if minutes == nil {
		return -1
	}
	return *minutes
}
