// Package workflowpg implements the workflow repository with sqlc. It is stateless: every
// method takes the caller's tenant-bound transaction, so RLS is active for every statement
// and nothing here can read another tenant's work.
//
// The queue boundary lives here rather than above: every read takes the caller's scope and
// hands it to SQL, so a row outside it is genuinely not returned. That is what lets the
// application layer answer 404 without ever having held the row.
//
// One method deliberately reports a boolean rather than an error. ClaimItem, ReleaseItem,
// ReassignItem and CompleteItem each run one conditional UPDATE, and "it changed nothing"
// is an outcome the caller has to interpret — who won the claim, whether the version was
// stale — not a failure this layer can name.
package workflowpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	"github.com/celikbros/kapsora/internal/workflow/application"
)

// PostgreSQL error codes and the constraint names this package maps to named errors.
const (
	uniqueViolation    = "23505"
	exclusionViolation = "23P01"

	constraintQueueCode    = "uq_work_queue_code"
	constraintPolicyPeriod = "ex_approval_policy_overlap"
)

// Repository implements application.Repository.
type Repository struct{}

// New returns the repository.
func New() *Repository { return &Repository{} }

var _ application.Repository = (*Repository)(nil)

// CreateQueue implements application.Repository.
func (Repository) CreateQueue(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewQueueRow,
) (application.QueueRecord, error) {
	row, err := sqlcgen.New(tx).CreateWorkQueue(ctx, sqlcgen.CreateWorkQueueParams{
		TenantID: tenantID, Code: in.Code, Name: in.Name, DomainCode: in.DomainCode,
		AssignmentPolicy: in.AssignmentPolicy, SlaMinutes: int32Ptr(in.SLAMinutes),
		EscalationQueueID: optUUID(in.EscalationQueueID), Active: in.Active,
		RequiredPermission: in.RequiredPermission, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation &&
			pgErr.ConstraintName == constraintQueueCode {
			return application.QueueRecord{}, application.ErrQueueCodeTaken
		}
		return application.QueueRecord{}, fmt.Errorf("workflow: create work queue: %w", err)
	}
	return application.QueueRecord{
		ID: row.ID, Code: in.Code, Name: in.Name, DomainCode: in.DomainCode,
		AssignmentPolicy: in.AssignmentPolicy, SLAMinutes: in.SLAMinutes,
		EscalationQueueID: in.EscalationQueueID, Active: in.Active,
		RequiredPermission: in.RequiredPermission,
		CreatedAt:          row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// GetQueue implements application.Repository.
func (Repository) GetQueue(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.QueueRecord, error) {
	row, err := sqlcgen.New(tx).GetWorkQueue(ctx, sqlcgen.GetWorkQueueParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.QueueIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.QueueRecord{}, application.ErrQueueNotFound
	}
	if err != nil {
		return application.QueueRecord{}, fmt.Errorf("workflow: get work queue: %w", err)
	}
	return queueOf(queueRow(row)), nil
}

// ListQueues implements application.Repository.
func (Repository) ListQueues(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.QueueQuery,
) ([]application.QueueRecord, error) {
	params := sqlcgen.ListWorkQueuesParams{
		TenantID: tenantID, ScopeIds: q.Scope.QueueIDs,
		DomainCode: optionalString(q.DomainCode), Active: q.Active,
		PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListWorkQueues(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("workflow: list work queues: %w", err)
	}
	out := make([]application.QueueRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, queueOf(listedQueueRow(row)))
	}
	return out, nil
}

// UpdateQueue implements application.Repository.
func (Repository) UpdateQueue(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.QueueUpdateRow, expected int64,
) error {
	affected, err := sqlcgen.New(tx).UpdateWorkQueue(ctx, sqlcgen.UpdateWorkQueueParams{
		TenantID: tenantID, ID: id, Name: in.Name, AssignmentPolicy: in.AssignmentPolicy,
		SlaMinutes: int32Ptr(in.SLAMinutes), EscalationQueueID: optUUID(in.EscalationQueueID),
		Active: in.Active, RequiredPermission: in.RequiredPermission,
		ActorID: optUUID(in.ActorID), RowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("workflow: update work queue: %w", err)
	}
	if affected != 1 {
		return application.ErrVersionMismatch
	}
	return nil
}

// CreateItem implements application.Repository. The insert reads the queue in the same
// statement, so the SLA snapshot and the due date come from the queue as it stands at that
// instant and cannot be handed in by a caller.
func (Repository) CreateItem(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewItemRow,
) (application.ItemRecord, error) {
	row, err := sqlcgen.New(tx).CreateWorkItem(ctx, sqlcgen.CreateWorkItemParams{
		TenantID: tenantID, QueueID: in.QueueID, AggregateType: in.AggregateType,
		AggregateID: in.AggregateID, Title: in.Title, Priority: int32(in.Priority), //nolint:gosec // bounded by domain.MaxPriority
		ActorID: optUUID(in.ActorID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The insert selects from the queue, so no row means the queue is gone or has
		// been deactivated since the caller read it.
		return application.ItemRecord{}, application.ErrQueueInactive
	}
	if err != nil {
		return application.ItemRecord{}, fmt.Errorf("workflow: create work item: %w", err)
	}
	return application.ItemRecord{
		ID: row.ID, QueueID: row.QueueID, AggregateType: in.AggregateType,
		AggregateID: in.AggregateID, Title: in.Title, Priority: int(row.Priority),
		DueAt: row.DueAt, SLAMinutesSnapshot: intPtr(row.SlaMinutesSnapshot),
		Status: row.Status, CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// GetItem implements application.Repository.
func (Repository) GetItem(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.ItemRecord, error) {
	row, err := sqlcgen.New(tx).GetWorkItem(ctx, sqlcgen.GetWorkItemParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.QueueIDs, Permissions: scope.Permissions,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ItemRecord{}, application.ErrWorkItemNotFound
	}
	if err != nil {
		return application.ItemRecord{}, fmt.Errorf("workflow: get work item: %w", err)
	}
	return itemOf(itemRow(row)), nil
}

// ListItems implements application.Repository.
func (Repository) ListItems(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.ItemQuery,
) ([]application.ItemRecord, error) {
	asOf := q.AsOf
	params := sqlcgen.ListWorkItemsParams{
		TenantID: tenantID, ScopeIds: q.Scope.QueueIDs, Permissions: q.Scope.Permissions,
		QueueID: optUUID(q.QueueID),
		Status:  optionalString(q.Status), AssigneeActorID: optUUID(q.AssigneeActorID),
		AggregateType: optionalString(q.AggregateType), AggregateID: optUUID(q.AggregateID),
		Overdue: q.Overdue, AsOf: &asOf, PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListWorkItems(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("workflow: list work items: %w", err)
	}
	out := make([]application.ItemRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, itemOf(listedItemRow(row)))
	}
	return out, nil
}

// ClaimItem implements application.Repository.
func (Repository) ClaimItem(ctx context.Context, tx pgx.Tx, tenantID, id, actorID uuid.UUID,
	scope application.Scope, expected int64,
) (bool, error) {
	affected, err := sqlcgen.New(tx).ClaimWorkItem(ctx, sqlcgen.ClaimWorkItemParams{
		TenantID: tenantID, ID: id, AssigneeActorID: uuid.NullUUID{UUID: actorID, Valid: true},
		ActorID: uuid.NullUUID{UUID: actorID, Valid: true}, RowVersion: expected,
		// The same rule as the read: work in a queue this caller could not be shown is work
		// they cannot take off the people who can do it.
		Permissions: scope.Permissions,
	})
	if err != nil {
		return false, fmt.Errorf("workflow: claim work item: %w", err)
	}
	return affected == 1, nil
}

// ReleaseItem implements application.Repository.
func (Repository) ReleaseItem(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	actorID *uuid.UUID, expected int64,
) (bool, error) {
	affected, err := sqlcgen.New(tx).ReleaseWorkItem(ctx, sqlcgen.ReleaseWorkItemParams{
		TenantID: tenantID, ID: id, ActorID: optUUID(actorID), RowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("workflow: release work item: %w", err)
	}
	return affected == 1, nil
}

// ReassignItem implements application.Repository.
func (Repository) ReassignItem(ctx context.Context, tx pgx.Tx, tenantID, id, assigneeID uuid.UUID,
	actorID *uuid.UUID, expected int64,
) (bool, error) {
	affected, err := sqlcgen.New(tx).ReassignWorkItem(ctx, sqlcgen.ReassignWorkItemParams{
		TenantID: tenantID, ID: id,
		AssigneeActorID: uuid.NullUUID{UUID: assigneeID, Valid: true},
		ActorID:         optUUID(actorID), RowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("workflow: reassign work item: %w", err)
	}
	return affected == 1, nil
}

// CompleteItem implements application.Repository.
func (Repository) CompleteItem(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	outcomeCode string, actorID *uuid.UUID, expected int64,
) (bool, error) {
	affected, err := sqlcgen.New(tx).CompleteWorkItem(ctx, sqlcgen.CompleteWorkItemParams{
		TenantID: tenantID, ID: id, OutcomeCode: &outcomeCode,
		CompletedBy: optUUID(actorID), ActorID: optUUID(actorID), RowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("workflow: complete work item: %w", err)
	}
	return affected == 1, nil
}

// ListEscalatableItems implements application.Repository.
func (Repository) ListEscalatableItems(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	before time.Time, limit int,
) ([]application.EscalatableItem, error) {
	rows, err := sqlcgen.New(tx).ListEscalatableWorkItems(ctx, sqlcgen.ListEscalatableWorkItemsParams{
		TenantID: tenantID, AsOf: &before, PageSize: pageSize(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("workflow: list escalatable work items: %w", err)
	}
	out := make([]application.EscalatableItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.EscalatableItem{
			ID: row.ID, QueueID: row.QueueID, Status: row.Status,
			EscalationQueueID: uuidPtr(row.EscalationQueueID),
		})
	}
	return out, nil
}

// MoveItemToQueue implements application.Repository.
func (Repository) MoveItemToQueue(ctx context.Context, tx pgx.Tx, tenantID, id, queueID,
	fromQueueID uuid.UUID, at time.Time,
) (bool, error) {
	affected, err := sqlcgen.New(tx).EscalateWorkItemToQueue(ctx, sqlcgen.EscalateWorkItemToQueueParams{
		TenantID: tenantID, ID: id, QueueID: queueID, EscalatedAt: &at,
		EscalatedFromQueueID: uuid.NullUUID{UUID: fromQueueID, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("workflow: escalate work item: %w", err)
	}
	return affected == 1, nil
}

// MarkItemEscalated implements application.Repository.
func (Repository) MarkItemEscalated(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	at time.Time,
) (bool, error) {
	affected, err := sqlcgen.New(tx).MarkWorkItemEscalated(ctx, sqlcgen.MarkWorkItemEscalatedParams{
		TenantID: tenantID, ID: id, EscalatedAt: &at,
	})
	if err != nil {
		return false, fmt.Errorf("workflow: mark work item escalated: %w", err)
	}
	return affected == 1, nil
}

// CreateStatusEvent implements application.Repository.
func (Repository) CreateStatusEvent(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.StatusEventRow,
) error {
	metadata, err := json.Marshal(in.Metadata)
	if err != nil {
		return fmt.Errorf("workflow: encode status event metadata: %w", err)
	}
	if err := sqlcgen.New(tx).CreateWorkItemStatusEvent(ctx, sqlcgen.CreateWorkItemStatusEventParams{
		TenantID: tenantID, AggregateID: in.AggregateID,
		FromStatus: optionalString(in.FromStatus), ToStatus: in.ToStatus,
		TransitionCode: in.TransitionCode, ReasonCode: in.ReasonCode, ReasonText: in.ReasonText,
		ActorID: optUUID(in.ActorID), MetadataJson: metadata,
	}); err != nil {
		return fmt.Errorf("workflow: create status event: %w", err)
	}
	return nil
}

// CreateComment implements application.Repository.
func (Repository) CreateComment(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewCommentRow,
) (application.CommentRecord, error) {
	row, err := sqlcgen.New(tx).CreateComment(ctx, sqlcgen.CreateCommentParams{
		TenantID: tenantID, AggregateType: in.AggregateType, AggregateID: in.AggregateID,
		WorkItemID: optUUID(in.WorkItemID), Visibility: in.Visibility, Body: in.Body,
		AuthorActorID: optUUID(in.AuthorActorID),
	})
	if err != nil {
		return application.CommentRecord{}, fmt.Errorf("workflow: create comment: %w", err)
	}
	return application.CommentRecord{
		ID: row.ID, AggregateType: in.AggregateType, AggregateID: in.AggregateID,
		WorkItemID: in.WorkItemID, Visibility: in.Visibility, Body: in.Body,
		AuthorActorID: in.AuthorActorID, CreatedAt: row.CreatedAt,
	}, nil
}

// ListItemComments implements application.Repository.
func (Repository) ListItemComments(ctx context.Context, tx pgx.Tx, tenantID, workItemID uuid.UUID,
	scope application.Scope, visibilities []string, limit int,
) ([]application.CommentRecord, error) {
	rows, err := sqlcgen.New(tx).ListWorkItemComments(ctx, sqlcgen.ListWorkItemCommentsParams{
		TenantID: tenantID, WorkItemID: uuid.NullUUID{UUID: workItemID, Valid: true},
		ScopeIds: scope.QueueIDs, Visibilities: visibilities, PageSize: pageSize(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("workflow: list work item comments: %w", err)
	}
	out := make([]application.CommentRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, commentOf(row))
	}
	return out, nil
}

// ListPolicies implements application.Repository.
func (Repository) ListPolicies(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actionCode, scopeCode string,
) ([]application.PolicyRecord, error) {
	rows, err := sqlcgen.New(tx).ListApprovalPolicies(ctx, sqlcgen.ListApprovalPoliciesParams{
		TenantID: tenantID, ActionCode: optionalString(actionCode),
		ScopeCode: optionalString(scopeCode),
	})
	if err != nil {
		return nil, fmt.Errorf("workflow: list approval policies: %w", err)
	}
	out := make([]application.PolicyRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, policyOf(listedPolicyRow(row)))
	}
	return out, nil
}

// NextPolicyVersion implements application.Repository.
func (Repository) NextPolicyVersion(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actionCode string,
) (int, error) {
	current, err := sqlcgen.New(tx).MaxApprovalPolicyVersion(ctx,
		sqlcgen.MaxApprovalPolicyVersionParams{TenantID: tenantID, ActionCode: actionCode})
	if err != nil {
		return 0, fmt.Errorf("workflow: read approval policy version: %w", err)
	}
	return int(current) + 1, nil
}

// DeletePolicies implements application.Repository.
func (Repository) DeletePolicies(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actionCode string,
) (int64, error) {
	affected, err := sqlcgen.New(tx).DeleteApprovalPolicies(ctx,
		sqlcgen.DeleteApprovalPoliciesParams{TenantID: tenantID, ActionCode: actionCode})
	if err != nil {
		return 0, fmt.Errorf("workflow: delete approval policies: %w", err)
	}
	return affected, nil
}

// CreatePolicy implements application.Repository.
func (Repository) CreatePolicy(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewPolicyRow,
) (application.PolicyRecord, error) {
	row, err := sqlcgen.New(tx).CreateApprovalPolicy(ctx, sqlcgen.CreateApprovalPolicyParams{
		TenantID: tenantID, ActionCode: in.ActionCode, ScopeCode: in.ScopeCode,
		VersionNo: int32(in.VersionNo), //nolint:gosec // a counter of edits, never near the limit
		MinAmount: in.MinAmount, MaxAmount: in.MaxAmount,
		RequiredRoleCodes:     in.RequiredRoleCodes,
		RequiredApproverCount: int32(in.RequiredApproverCount), //nolint:gosec // validated above 0 and small
		ValidFrom:             dateParam(&in.ValidFrom), ValidTo: dateParam(in.ValidTo),
		ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == exclusionViolation &&
			pgErr.ConstraintName == constraintPolicyPeriod {
			return application.PolicyRecord{}, application.ErrPolicyOverlap
		}
		return application.PolicyRecord{}, fmt.Errorf("workflow: create approval policy: %w", err)
	}
	return application.PolicyRecord{
		ID: row.ID, ActionCode: in.ActionCode, ScopeCode: in.ScopeCode,
		VersionNo: in.VersionNo,
		MinAmount: trimDecimalPtr(row.MinAmount), MaxAmount: trimDecimalPtr(row.MaxAmount),
		RequiredRoleCodes:     roleCodes(in.RequiredRoleCodes),
		RequiredApproverCount: in.RequiredApproverCount,
		ValidFrom:             in.ValidFrom, ValidTo: in.ValidTo,
		CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// ResolvePolicy implements application.Repository.
func (Repository) ResolvePolicy(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.PolicyLookup,
) (application.PolicyRecord, error) {
	asOf := in.AsOf
	row, err := sqlcgen.New(tx).ResolveApprovalPolicy(ctx, sqlcgen.ResolveApprovalPolicyParams{
		TenantID: tenantID, ActionCode: in.ActionCode, ScopeCode: optionalString(in.ScopeCode),
		AsOf: dateParam(&asOf), Amount: in.Amount,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PolicyRecord{}, application.ErrPolicyNotFound
	}
	if err != nil {
		return application.PolicyRecord{}, fmt.Errorf("workflow: resolve approval policy: %w", err)
	}
	return policyOf(resolvedPolicyRow(row)), nil
}

// ActiveTenants implements application.Repository.
func (Repository) ActiveTenants(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM platform.tenant WHERE status IN ('ACTIVE','SUSPENDED')`)
	if err != nil {
		return nil, fmt.Errorf("workflow: list tenants: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("workflow: scan tenant: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workflow: list tenants: %w", err)
	}
	return out, nil
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func int32Ptr(n *int) *int32 {
	if n == nil {
		return nil
	}
	value := int32(*n) //nolint:gosec // bounded by domain.MaxSLAMinutes
	return &value
}

func dateParam(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: *t, Valid: true}
}

func pageSize(n int) int32 {
	if n <= 0 {
		return 1
	}
	return int32(n) //nolint:gosec // clamped by httpx.ClampLimit before it reaches here
}
