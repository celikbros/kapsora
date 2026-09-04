// Package application implements the worklist use cases: defining the queues work waits
// in, raising items into them, taking one piece of work and putting it down again, saying
// what was decided, and the clock that says when nobody did. Transactions are opened here
// with db.WithTenantTx, so a write, its status event and its audit row commit together and
// RLS is bound for every statement.
//
// Two things this package never does. It never computes a due date from a queue: the
// clock an item was given is stored on the item, and changing the queue's SLA afterwards
// moves nothing. And it never resolves a claim by writing over an owner — a claim is one
// conditional UPDATE, and losing the race is an answer rather than a retry.
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding this package (migration 000027). They live here rather than in the
// transport because who may take a piece of work off somebody else is a business rule,
// not a routing detail.
const (
	PermissionRead        = "worklist.read"
	PermissionClaim       = "worklist.claim"
	PermissionReassign    = "worklist.reassign"
	PermissionQueueManage = "workflow.queue.manage"
	PermissionPolicy      = "workflow.policy.manage"
)

// ScopeWorkQueue is the iam.access_grant.scope_type that binds an actor to a set of
// queues (v1.2 6.3). It is this package's provider boundary: an actor granted a queue
// sees that queue's work and no other.
const ScopeWorkQueue = "WORK_QUEUE"

// Errors mapped by the transport layer to problem codes.
var (
	ErrQueueNotFound    = errors.New("workflow: work queue not found")
	ErrWorkItemNotFound = errors.New("workflow: work item not found")

	// ErrQueueCodeTaken is the unique (tenant_id, code) refusing a second queue under a
	// code somebody already routes work to.
	ErrQueueCodeTaken = errors.New("workflow: the queue code is already in use")
	// ErrQueueInactive refuses raising work into a queue nobody is watching.
	ErrQueueInactive = errors.New("workflow: the queue is not active")
	// ErrEscalationCycle refuses a queue that escalates into itself.
	ErrEscalationCycle = errors.New("workflow: a queue cannot escalate into itself")

	// ErrTransitionInvalid is a command given in a state that does not allow it.
	ErrTransitionInvalid = errors.New("workflow: the work item cannot move that way")
	// ErrNotAssignee refuses releasing or completing somebody else's work. Taking work
	// off another person is a different command with a different permission.
	ErrNotAssignee = errors.New("workflow: the work item is held by somebody else")

	// ErrPolicyOverlap is the exclusion constraint refusing two policies that cover the
	// same action, scope and day.
	ErrPolicyOverlap = errors.New("workflow: two approval policies cover the same period")
	// ErrPolicyNotFound is the lookup finding no policy for an action, amount and day.
	ErrPolicyNotFound = errors.New("workflow: no approval policy applies")

	ErrVersionMismatch = errors.New("workflow: row version does not match If-Match")
)

// AlreadyClaimedError is what a claim that lost its race answers with. It names the actor
// who won, because "somebody else took it" without saying who is what makes two people
// keep clicking.
type AlreadyClaimedError struct {
	WorkItemID uuid.UUID
	// AssigneeActorID is nil only in the race a claim cannot actually lose to: the item
	// left OPEN by a release between the failed update and the re-read.
	AssigneeActorID *uuid.UUID
	Status          string
}

func (e *AlreadyClaimedError) Error() string {
	if e.AssigneeActorID == nil {
		return fmt.Sprintf("workflow: work item %s is %s", e.WorkItemID, e.Status)
	}
	return fmt.Sprintf("workflow: work item %s is %s by %s", e.WorkItemID, e.Status, e.AssigneeActorID)
}

// ErrWorkItemAlreadyClaimed lets callers detect an AlreadyClaimedError with errors.Is.
var ErrWorkItemAlreadyClaimed = errors.New("workflow: the work item has already been claimed")

// Is lets errors.Is(err, ErrWorkItemAlreadyClaimed) match an *AlreadyClaimedError.
func (e *AlreadyClaimedError) Is(target error) bool { return target == ErrWorkItemAlreadyClaimed }

// Scope is the caller's queue boundary. A nil slice means "no restriction"; an empty
// non-nil slice restricts the caller to nothing, which is the safe reading of a grant that
// names no queue.
type Scope struct {
	QueueIDs []uuid.UUID
}

// Restricted reports whether the caller is bound to a set of queues.
func (s Scope) Restricted() bool { return s.QueueIDs != nil }

// scopeOf reads the caller's queue grants. A tenant-wide actor has none.
func scopeOf(rc identity.RequestContext) Scope {
	var ids []uuid.UUID
	for _, s := range rc.Scopes {
		if s.Type != ScopeWorkQueue {
			continue
		}
		if ids == nil {
			ids = []uuid.UUID{}
		}
		if s.ID.Valid {
			ids = append(ids, s.ID.UUID)
		}
	}
	return Scope{QueueIDs: ids}
}

// QueueRecord is one workflow.work_queue row.
type QueueRecord struct {
	ID                uuid.UUID
	Code              string
	Name              string
	DomainCode        string
	AssignmentPolicy  string
	SLAMinutes        *int
	EscalationQueueID *uuid.UUID
	Active            bool
	CreatedAt         time.Time
	RowVersion        int64
}

// NewQueueRow is the insert payload of a queue.
type NewQueueRow struct {
	Code              string
	Name              string
	DomainCode        string
	AssignmentPolicy  string
	SLAMinutes        *int
	EscalationQueueID *uuid.UUID
	Active            bool
	ActorID           *uuid.UUID
}

// QueueUpdateRow is the whole mutable half of a queue, after the merge patch has been
// applied to the row that was read. The code and the domain are not here: they are what
// other rows point at.
type QueueUpdateRow struct {
	Name              string
	AssignmentPolicy  string
	SLAMinutes        *int
	EscalationQueueID *uuid.UUID
	Active            bool
	ActorID           *uuid.UUID
}

// QueueQuery is the repository-level queue filter.
type QueueQuery struct {
	Scope      Scope
	DomainCode string
	Active     *bool
	After      *httpx.Cursor
	PageSize   int
}

// ItemRecord is one workflow.work_item row. `SLAMinutesSnapshot` and `DueAt` are the
// item's own clock, copied from its queue when it was raised; neither is read back from
// the queue anywhere in this package.
type ItemRecord struct {
	ID                   uuid.UUID
	QueueID              uuid.UUID
	AggregateType        string
	AggregateID          uuid.UUID
	Title                string
	Priority             int
	AssigneeActorID      *uuid.UUID
	AssignedAt           *time.Time
	DueAt                *time.Time
	SLAMinutesSnapshot   *int
	Status               string
	OutcomeCode          *string
	CompletedAt          *time.Time
	CompletedBy          *uuid.UUID
	EscalatedAt          *time.Time
	EscalatedFromQueueID *uuid.UUID
	CreatedAt            time.Time
	RowVersion           int64
}

// Overdue reports whether the item is past the clock it was given, at the moment asked
// about. An item with no clock is never overdue.
func (r ItemRecord) Overdue(now time.Time) bool {
	return r.DueAt != nil && r.DueAt.Before(now)
}

// NewItemRow is the insert payload of a work item. There is no SLA field: the snapshot is
// copied from the queue by the insert itself, so no caller can hand an item a clock of its
// own choosing.
type NewItemRow struct {
	QueueID       uuid.UUID
	AggregateType string
	AggregateID   uuid.UUID
	Title         string
	Priority      int
	ActorID       *uuid.UUID
}

// ItemQuery is the repository-level work item filter.
type ItemQuery struct {
	Scope           Scope
	QueueID         *uuid.UUID
	Status          string
	AssigneeActorID *uuid.UUID
	AggregateType   string
	AggregateID     *uuid.UUID
	Overdue         *bool
	AsOf            time.Time
	After           *httpx.Cursor
	PageSize        int
}

// EscalatableItem is one overdue item the job may still act on, with the target its queue
// names. A nil EscalationQueueID means the queue has nowhere to send it, and the item is
// marked ESCALATED where it stands.
type EscalatableItem struct {
	ID                uuid.UUID
	QueueID           uuid.UUID
	Status            string
	EscalationQueueID *uuid.UUID
}

// CommentRecord is one append-only workflow.comment row.
type CommentRecord struct {
	ID            uuid.UUID
	AggregateType string
	AggregateID   uuid.UUID
	WorkItemID    *uuid.UUID
	Visibility    string
	Body          string
	AuthorActorID *uuid.UUID
	CreatedAt     time.Time
}

// NewCommentRow is the insert payload of a comment.
type NewCommentRow struct {
	AggregateType string
	AggregateID   uuid.UUID
	WorkItemID    *uuid.UUID
	Visibility    string
	Body          string
	AuthorActorID *uuid.UUID
}

// PolicyRecord is one workflow.approval_policy row. Both amounts are carried as the exact
// text the numeric column holds.
type PolicyRecord struct {
	ID                    uuid.UUID
	ActionCode            string
	ScopeCode             string
	VersionNo             int
	MinAmount             *string
	MaxAmount             *string
	RequiredRoleCodes     []string
	RequiredApproverCount int
	ValidFrom             time.Time
	ValidTo               *time.Time
	CreatedAt             time.Time
	RowVersion            int64
}

// NewPolicyRow is the insert payload of one policy of a written set.
type NewPolicyRow struct {
	ActionCode            string
	ScopeCode             string
	VersionNo             int
	MinAmount             *string
	MaxAmount             *string
	RequiredRoleCodes     []string
	RequiredApproverCount int
	ValidFrom             time.Time
	ValidTo               *time.Time
	ActorID               *uuid.UUID
}

// PolicyLookup is the question the commands that need an approval policy ask: for this
// action, at this amount, on this day, which roles may approve and how many are needed.
type PolicyLookup struct {
	ActionCode string
	ScopeCode  string
	Amount     string
	AsOf       time.Time
}

// StatusEventRow is one append-only workflow.status_event row.
type StatusEventRow struct {
	AggregateID    uuid.UUID
	FromStatus     string
	ToStatus       string
	TransitionCode string
	ReasonCode     *string
	ReasonText     *string
	ActorID        *uuid.UUID
	Metadata       map[string]any
}

// Repository is the persistence port; every method runs inside the caller's transaction,
// which db.WithTenantTx has already bound to the tenant so RLS is active. The queue
// boundary is a repository concern too: the scope is passed down rather than checked
// above, so a row outside it is genuinely not there rather than fetched and then hidden.
type Repository interface {
	CreateQueue(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewQueueRow) (QueueRecord, error)
	GetQueue(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (QueueRecord, error)
	ListQueues(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q QueueQuery) ([]QueueRecord, error)
	UpdateQueue(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in QueueUpdateRow, expected int64) error

	CreateItem(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewItemRow) (ItemRecord, error)
	GetItem(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (ItemRecord, error)
	ListItems(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q ItemQuery) ([]ItemRecord, error)
	// ClaimItem is the whole of claiming: one UPDATE whose predicate carries the status
	// and the row_version the caller read. It reports whether it took the item, and a
	// false is never a partial write.
	ClaimItem(ctx context.Context, tx pgx.Tx, tenantID, id, actorID uuid.UUID, expected int64) (bool, error)
	ReleaseItem(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, actorID *uuid.UUID, expected int64) (bool, error)
	ReassignItem(ctx context.Context, tx pgx.Tx, tenantID, id, assigneeID uuid.UUID, actorID *uuid.UUID, expected int64) (bool, error)
	CompleteItem(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, outcomeCode string, actorID *uuid.UUID, expected int64) (bool, error)

	// ListEscalatableItems takes the overdue items past their clock FOR UPDATE SKIP
	// LOCKED, so two schedulers that both believe they lead cannot escalate one row
	// twice. Items already escalated are not among them.
	ListEscalatableItems(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, before time.Time, limit int) ([]EscalatableItem, error)
	// MoveItemToQueue moves an overdue item to its queue's escalation target, leaving its
	// status, its owner and its due date alone.
	MoveItemToQueue(ctx context.Context, tx pgx.Tx, tenantID, id, queueID, fromQueueID uuid.UUID, at time.Time) (bool, error)
	// MarkItemEscalated marks an overdue item whose queue names no target.
	MarkItemEscalated(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, at time.Time) (bool, error)

	CreateStatusEvent(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in StatusEventRow) error

	CreateComment(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewCommentRow) (CommentRecord, error)
	ListItemComments(ctx context.Context, tx pgx.Tx, tenantID, workItemID uuid.UUID, scope Scope, visibilities []string, limit int) ([]CommentRecord, error)

	ListPolicies(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, actionCode, scopeCode string) ([]PolicyRecord, error)
	// NextPolicyVersion reads the version the next written set gets. It is read before
	// the delete, so replacing a set keeps counting rather than starting again.
	NextPolicyVersion(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, actionCode string) (int, error)
	DeletePolicies(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, actionCode string) (int64, error)
	CreatePolicy(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewPolicyRow) (PolicyRecord, error)
	ResolvePolicy(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in PolicyLookup) (PolicyRecord, error)

	// ActiveTenants lists the tenants the escalation job walks. platform.tenant carries
	// no RLS, so it is read outside a tenant transaction like the other cross-tenant jobs.
	ActiveTenants(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error)
}
