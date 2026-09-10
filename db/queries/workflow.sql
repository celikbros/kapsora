-- Work queue, work item, comment and approval policy queries (WP-I4-03, v1.2 9.10, 11.8).
--
-- Four properties shape this file.
--
-- The boundary is the same shape the authorization and service request queries apply,
-- over this package's own resource: `scope_ids` is a nullable uuid[] of work queue ids,
-- NULL means the caller sees every queue of the tenant and a non-null array binds it to
-- those queues (iam.access_grant.scope_type = 'WORK_QUEUE'). It is applied in every read
-- including the single-row ones, so a caller outside it finds no row and is answered 404
-- rather than 403 — that such an item exists at all is somebody else's business.
--
-- Claiming is one UPDATE whose predicate carries the whole precondition: the item, the
-- tenant, the status and the row_version the caller read. Zero rows affected is the only
-- outcome a loser gets, and it can never be a partial write.
--
-- `sla_minutes_snapshot` and `due_at` are written once, by CreateWorkItem, from the queue
-- as it stands at that moment. No statement in this file recomputes either of them, and
-- the escalation statements deliberately leave due_at alone: a late item stays late.
--
-- Amounts are read as `::text` and written as `::text::numeric`, so an exact decimal never
-- passes through a float in either direction (handbook section 3).

-- name: CreateWorkQueue :one
INSERT INTO workflow.work_queue (
    tenant_id, code, name, domain_code, assignment_policy, sla_minutes,
    escalation_queue_id, active, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('code'), sqlc.arg('name'), sqlc.arg('domain_code'),
        sqlc.arg('assignment_policy'), sqlc.narg('sla_minutes'),
        sqlc.narg('escalation_queue_id'), sqlc.arg('active'), sqlc.narg('actor_id'),
        sqlc.narg('actor_id'))
RETURNING id, created_at, row_version;

-- name: GetWorkQueue :one
SELECT q.id, q.code, q.name, q.domain_code, q.assignment_policy, q.sla_minutes,
       q.escalation_queue_id, q.active, q.created_at, q.row_version
  FROM workflow.work_queue q
 WHERE q.tenant_id = sqlc.arg('tenant_id')
   AND q.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL OR q.id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: GetWorkQueueByCode :one
-- The queue a producing module raises work into. A module that raised work by id would
-- have to be told the id of a queue an operator created; the code is the name the queue
-- is configured under, and it is unique per tenant. No scope filter: the raiser is a
-- module rather than a person, and the boundary a person is bound to is applied when the
-- work is read.
SELECT q.id, q.code, q.name, q.domain_code, q.sla_minutes, q.active
  FROM workflow.work_queue q
 WHERE q.tenant_id = sqlc.arg('tenant_id')
   AND q.code = sqlc.arg('code');

-- name: ListWorkQueues :many
-- Keyset pagination on (created_at DESC, id DESC); the caller asks for limit+1 rows to
-- learn whether a next page exists.
SELECT q.id, q.code, q.name, q.domain_code, q.assignment_policy, q.sla_minutes,
       q.escalation_queue_id, q.active, q.created_at, q.row_version
  FROM workflow.work_queue q
 WHERE q.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL OR q.id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('domain_code')::text IS NULL OR q.domain_code = sqlc.narg('domain_code')::text)
   AND (sqlc.narg('active')::boolean IS NULL OR q.active = sqlc.narg('active')::boolean)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (q.created_at, q.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY q.created_at DESC, q.id DESC
 LIMIT sqlc.arg('page_size');

-- name: UpdateWorkQueue :execrows
-- The expected row_version is part of the predicate, so a stale If-Match updates nothing.
-- The touch trigger moves the version and updated_at; `code` and `domain_code` are absent
-- because they are what other rows already point at by name.
UPDATE workflow.work_queue
   SET name                = sqlc.arg('name'),
       assignment_policy   = sqlc.arg('assignment_policy'),
       sla_minutes         = sqlc.narg('sla_minutes'),
       escalation_queue_id = sqlc.narg('escalation_queue_id'),
       active              = sqlc.arg('active'),
       updated_by          = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version');

-- name: CreateWorkItem :one
-- The SLA is copied from the queue here and nowhere else. Both the snapshot and the due
-- date are taken from one `now()`, the transaction's own timestamp, so an item's clock
-- starts exactly when the item does; retuning the queue afterwards moves neither.
INSERT INTO workflow.work_item (
    tenant_id, queue_id, aggregate_type, aggregate_id, title, priority,
    created_at, sla_minutes_snapshot, due_at, created_by, updated_by)
SELECT sqlc.arg('tenant_id'), q.id, sqlc.arg('aggregate_type'), sqlc.arg('aggregate_id'),
       sqlc.arg('title'), sqlc.arg('priority'), now(), q.sla_minutes,
       CASE WHEN q.sla_minutes IS NULL THEN NULL
            ELSE now() + make_interval(mins => q.sla_minutes) END,
       sqlc.narg('actor_id'), sqlc.narg('actor_id')
  FROM workflow.work_queue q
 WHERE q.tenant_id = sqlc.arg('tenant_id')
   AND q.id = sqlc.arg('queue_id')
   AND q.active
RETURNING id, queue_id, status, priority, sla_minutes_snapshot, due_at, created_at, row_version;

-- name: GetWorkItem :one
SELECT i.id, i.queue_id, i.aggregate_type, i.aggregate_id, i.title, i.priority,
       i.assignee_actor_id, i.assigned_at, i.due_at, i.sla_minutes_snapshot, i.status,
       i.outcome_code, i.completed_at, i.completed_by, i.escalated_at,
       i.escalated_from_queue_id, i.created_at, i.row_version,
       a.display_name AS assignee_display_name
  FROM workflow.work_item i
  LEFT JOIN iam.actor a ON a.id = i.assignee_actor_id
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND i.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL OR i.queue_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: ListWorkItems :many
-- `assigned_to_me` and `overdue` are the two questions the morning list is: what is mine,
-- and what is late. `overdue` is answered from the item's own due_at, never from the
-- queue's SLA, so an item is late by the clock it was given.
SELECT i.id, i.queue_id, i.aggregate_type, i.aggregate_id, i.title, i.priority,
       i.assignee_actor_id, i.assigned_at, i.due_at, i.sla_minutes_snapshot, i.status,
       i.outcome_code, i.completed_at, i.completed_by, i.escalated_at,
       i.escalated_from_queue_id, i.created_at, i.row_version,
       a.display_name AS assignee_display_name
  FROM workflow.work_item i
  LEFT JOIN iam.actor a ON a.id = i.assignee_actor_id
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL OR i.queue_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('queue_id')::uuid IS NULL OR i.queue_id = sqlc.narg('queue_id')::uuid)
   AND (sqlc.narg('status')::text IS NULL OR i.status = sqlc.narg('status')::text)
   AND (sqlc.narg('assignee_actor_id')::uuid IS NULL
        OR i.assignee_actor_id = sqlc.narg('assignee_actor_id')::uuid)
   AND (sqlc.narg('aggregate_type')::text IS NULL
        OR i.aggregate_type = sqlc.narg('aggregate_type')::text)
   AND (sqlc.narg('aggregate_id')::uuid IS NULL OR i.aggregate_id = sqlc.narg('aggregate_id')::uuid)
   AND (sqlc.narg('overdue')::boolean IS NULL
        OR (sqlc.narg('overdue')::boolean
            AND i.due_at IS NOT NULL AND i.due_at < sqlc.arg('as_of'))
        OR (NOT sqlc.narg('overdue')::boolean
            AND (i.due_at IS NULL OR i.due_at >= sqlc.arg('as_of'))))
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (i.created_at, i.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY i.created_at DESC, i.id DESC
 LIMIT sqlc.arg('page_size');

-- name: ClaimWorkItem :execrows
-- The whole precondition is in the predicate: this item, this tenant, still OPEN, still at
-- the version the caller read. Two callers racing on one item both run this statement; the
-- second one waits on the row lock, re-evaluates the predicate against the row the first
-- one committed, matches nothing and affects zero rows. There is no path here that
-- overwrites an owner.
UPDATE workflow.work_item
   SET status            = 'CLAIMED',
       assignee_actor_id = sqlc.arg('assignee_actor_id'),
       assigned_at       = now(),
       updated_by        = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'OPEN'
   AND row_version = sqlc.arg('row_version');

-- name: ReleaseWorkItem :execrows
-- Back to the queue, owner cleared. due_at is untouched: putting work down does not buy
-- more time to do it.
UPDATE workflow.work_item
   SET status            = 'OPEN',
       assignee_actor_id = NULL,
       assigned_at       = NULL,
       updated_by        = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'CLAIMED'
   AND row_version = sqlc.arg('row_version');

-- name: ReassignWorkItem :execrows
-- Reassignment reaches an OPEN item as well as a CLAIMED one: giving unclaimed work to a
-- named person is the same act as taking it off somebody else.
UPDATE workflow.work_item
   SET status            = 'CLAIMED',
       assignee_actor_id = sqlc.arg('assignee_actor_id'),
       assigned_at       = now(),
       updated_by        = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('OPEN','CLAIMED')
   AND row_version = sqlc.arg('row_version');

-- name: CompleteWorkItem :execrows
UPDATE workflow.work_item
   SET status       = 'COMPLETED',
       outcome_code = sqlc.arg('outcome_code'),
       completed_at = now(),
       completed_by = sqlc.narg('completed_by'),
       updated_by   = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'CLAIMED'
   AND row_version = sqlc.arg('row_version');

-- name: ListEscalatableWorkItems :many
-- The overdue items the escalation job may still act on, with the target their queue
-- names, taken FOR UPDATE SKIP LOCKED so two schedulers that both believe they lead never
-- escalate the same row twice. `escalated_at IS NULL` is what makes a second pass a no-op:
-- moving an item does not change its status and leaves it overdue in its new queue, so
-- without the marker it would be moved again on every run.
SELECT i.id, i.queue_id, i.status, q.escalation_queue_id
  FROM workflow.work_item i
  JOIN workflow.work_queue q ON q.tenant_id = i.tenant_id AND q.id = i.queue_id
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND i.status IN ('OPEN','CLAIMED')
   AND i.escalated_at IS NULL
   AND i.due_at IS NOT NULL
   AND i.due_at < sqlc.arg('as_of')
 ORDER BY i.due_at, i.id
 LIMIT sqlc.arg('page_size')
   FOR UPDATE OF i SKIP LOCKED;

-- name: EscalateWorkItemToQueue :execrows
-- The item moves to the escalation queue and keeps everything else it had: the same
-- status, the same owner if it had one, and the same due_at. It is late, and moving it
-- does not make it less late.
UPDATE workflow.work_item
   SET queue_id                = sqlc.arg('queue_id'),
       escalated_at            = sqlc.arg('escalated_at'),
       escalated_from_queue_id = sqlc.arg('escalated_from_queue_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND escalated_at IS NULL;

-- name: MarkWorkItemEscalated :execrows
-- The queue has nowhere to escalate to, so the item is marked instead of moved. The owner
-- is left exactly as it was: somebody holding late work still holds it.
UPDATE workflow.work_item
   SET status       = 'ESCALATED',
       escalated_at = sqlc.arg('escalated_at')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND escalated_at IS NULL;

-- name: CreateWorkItemStatusEvent :exec
-- Every claim, release, reassignment, completion and escalation leaves one row here.
-- workflow.status_event is append-only (migration 000007): a history that could be edited
-- is not a history.
INSERT INTO workflow.status_event (
    tenant_id, aggregate_type, aggregate_id, from_status, to_status, transition_code,
    reason_code, reason_text, actor_id, metadata_json)
VALUES (sqlc.arg('tenant_id'), 'WORK_ITEM', sqlc.arg('aggregate_id'),
        sqlc.narg('from_status'), sqlc.arg('to_status'), sqlc.arg('transition_code'),
        sqlc.narg('reason_code'), sqlc.narg('reason_text'), sqlc.narg('actor_id'),
        sqlc.arg('metadata_json'));

-- name: CountWorkItemStatusEvents :one
-- How many times one transition has been recorded for one item. The escalation test asks
-- this: running the job twice must leave one event, not two.
SELECT count(*)
  FROM workflow.status_event
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND aggregate_type = 'WORK_ITEM'
   AND aggregate_id = sqlc.arg('aggregate_id')
   AND transition_code = sqlc.arg('transition_code');

-- name: CreateComment :one
INSERT INTO workflow.comment (
    tenant_id, aggregate_type, aggregate_id, work_item_id, visibility, body, author_actor_id)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('aggregate_type'), sqlc.arg('aggregate_id'),
        sqlc.narg('work_item_id'), sqlc.arg('visibility'), sqlc.arg('body'),
        sqlc.narg('author_actor_id'))
RETURNING id, created_at;

-- name: ListWorkItemComments :many
-- The comments of one item, oldest first: a conversation read in the order it happened.
SELECT c.id, c.aggregate_type, c.aggregate_id, c.work_item_id, c.visibility, c.body,
       c.author_actor_id, c.created_at
  FROM workflow.comment c
  JOIN workflow.work_item i ON i.tenant_id = c.tenant_id AND i.id = c.work_item_id
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND c.work_item_id = sqlc.arg('work_item_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL OR i.queue_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('visibilities')::text[] IS NULL
        OR c.visibility = ANY(sqlc.narg('visibilities')::text[]))
 ORDER BY c.created_at, c.id
 LIMIT sqlc.arg('page_size');

-- name: ListApprovalPolicies :many
SELECT p.id, p.action_code, p.scope_code, p.version_no,
       -- An open band edge is NULL in the column and the empty string on the way out, so
       -- the mapping can tell "no limit" from a limit of zero without a second column.
       COALESCE(p.min_amount::text, '')::text AS min_amount,
       COALESCE(p.max_amount::text, '')::text AS max_amount,
       p.required_role_codes, p.required_approver_count, p.valid_from, p.valid_to,
       p.created_at, p.row_version
  FROM workflow.approval_policy p
 WHERE p.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('action_code')::text IS NULL OR p.action_code = sqlc.narg('action_code')::text)
   AND (sqlc.narg('scope_code')::text IS NULL OR p.scope_code = sqlc.narg('scope_code')::text)
 ORDER BY p.action_code, p.scope_code, p.valid_from;

-- name: MaxApprovalPolicyVersion :one
-- The version the next written set gets. It survives the delete below because it is read
-- first: replacing a set is an edit of the same policy, and the number says how many
-- edits it has had.
SELECT coalesce(max(version_no), 0)::int AS version_no
  FROM workflow.approval_policy
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND action_code = sqlc.arg('action_code');

-- name: DeleteApprovalPolicies :execrows
DELETE FROM workflow.approval_policy
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND action_code = sqlc.arg('action_code');

-- name: CreateApprovalPolicy :one
INSERT INTO workflow.approval_policy (
    tenant_id, action_code, scope_code, version_no, min_amount, max_amount,
    required_role_codes, required_approver_count, valid_from, valid_to,
    created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('action_code'), sqlc.arg('scope_code'),
        sqlc.arg('version_no'), sqlc.narg('min_amount')::text::numeric,
        sqlc.narg('max_amount')::text::numeric, sqlc.arg('required_role_codes'),
        sqlc.arg('required_approver_count'), sqlc.arg('valid_from'), sqlc.narg('valid_to'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
-- The amounts come back as the column holds them, not as the caller sent them: the write
-- and the read of one policy must not disagree about how "1000.500000" is written.
RETURNING id, COALESCE(min_amount::text, '')::text AS min_amount,
          COALESCE(max_amount::text, '')::text AS max_amount, created_at, row_version;

-- name: ResolveApprovalPolicy :one
-- Which roles may approve this action for this amount on this day, and how many of them.
-- The exclusion constraint already guarantees one policy per (action, scope) per day, so
-- the ordering only decides between scopes: the narrowest band that contains the amount
-- wins, which is what makes a high-value scope override a general one.
SELECT p.id, p.action_code, p.scope_code, p.version_no,
       -- An open band edge is NULL in the column and the empty string on the way out, so
       -- the mapping can tell "no limit" from a limit of zero without a second column.
       COALESCE(p.min_amount::text, '')::text AS min_amount,
       COALESCE(p.max_amount::text, '')::text AS max_amount,
       p.required_role_codes, p.required_approver_count, p.valid_from, p.valid_to,
       p.created_at, p.row_version
  FROM workflow.approval_policy p
 WHERE p.tenant_id = sqlc.arg('tenant_id')
   AND p.action_code = sqlc.arg('action_code')
   AND (sqlc.narg('scope_code')::text IS NULL OR p.scope_code = sqlc.narg('scope_code')::text)
   AND daterange(p.valid_from, p.valid_to, '[)') @> sqlc.arg('as_of')::date
   AND (p.min_amount IS NULL OR p.min_amount <= sqlc.arg('amount')::text::numeric)
   AND (p.max_amount IS NULL OR p.max_amount >= sqlc.arg('amount')::text::numeric)
 ORDER BY p.min_amount DESC NULLS LAST, p.max_amount ASC NULLS LAST, p.scope_code
 LIMIT 1;

-- name: ClaimWorkItemForAggregate :execrows
-- The work item a *producing module* takes on behalf of the person who has just acted on the
-- thing it was raised for (WP-I7-03: the payer's reviewer opening an icmal).
--
-- It is addressed by the aggregate rather than by id for the same reason GetWorkQueueByCode
-- is addressed by code: a module raising and then closing its own work cannot know the id of
-- a row it never read back, and the aggregate is what the item is *about*. There is no
-- row_version, because the caller holds the aggregate's own row lock and the aggregate's
-- If-Match is what this transition is guarded by.
--
-- Still OPEN is the whole precondition: an item somebody else is already holding is not taken
-- off them here. Nothing is raised when there is no item at all -- a tenant that has not
-- configured the queue has no work to claim, and the business command carries on.
UPDATE workflow.work_item
   SET status            = 'CLAIMED',
       assignee_actor_id = sqlc.arg('assignee_actor_id'),
       assigned_at       = now(),
       updated_by        = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND aggregate_type = sqlc.arg('aggregate_type')
   AND aggregate_id = sqlc.arg('aggregate_id')
   AND status = 'OPEN';

-- name: CompleteWorkItemForAggregate :execrows
-- The other half: the module that raised the work says it is finished, because the thing the
-- work was about has been decided. OPEN as well as CLAIMED, so a decision taken by somebody
-- who never picked the item up still closes it rather than leaving a queue full of work
-- nobody has to do.
UPDATE workflow.work_item
   SET status       = 'COMPLETED',
       outcome_code = sqlc.arg('outcome_code'),
       completed_at = now(),
       completed_by = sqlc.narg('completed_by'),
       updated_by   = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND aggregate_type = sqlc.arg('aggregate_type')
   AND aggregate_id = sqlc.arg('aggregate_id')
   AND status IN ('OPEN', 'CLAIMED');
