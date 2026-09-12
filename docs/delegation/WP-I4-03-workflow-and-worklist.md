# WP-I4-03 · Work queues, work items, SLA and approval policy

| Field                      | Value                                                                                                                                                                                                                                                          |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M4 (plan increment I4)                                                                                                                                                                                                                                         |
| Size                       | L                                                                                                                                                                                                                                                              |
| Depends on                 | WP-I4-01 (requests raise the work)                                                                                                                                                                                                                             |
| Runs in parallel with      | WP-I4-02                                                                                                                                                                                                                                                       |
| Migration numbers assigned | `000027_workflow_queues.up.sql`                                                                                                                                                                                                                                |
| OpenAPI operations owned   | `listWorkQueues`, `createWorkQueue`, `patchWorkQueue`, `listWorkItems`, `getWorkItem`, `claimWorkItem`, `releaseWorkItem`, `reassignWorkItem`, `completeWorkItem`, `listWorkItemComments`, `addWorkItemComment`, `listApprovalPolicies`, `putApprovalPolicies` |
| Read first                 | v1.2 9.10, 11.8, 16.7; `workflow.status_event` in migration 000007; ADR-015                                                                                                                                                                                    |

## 1. Goal

Work that needs a person needs a queue, an owner and a clock. This package is the list an
operator opens in the morning: what is waiting, what is mine, what is late, and who
decided what.

## 2. Scope

### 2.1 Schema (migration 000027)

`workflow.work_queue`: id, tenant_id, `code`, `name`, `domain_code`, `assignment_policy`
(`MANUAL`,`ROUND_ROBIN`,`LEAST_LOADED`), `sla_minutes` int NULL, `escalation_queue_id`
(self composite FK), `active`. Unique `(tenant_id, code)`.

**`required_permission`** (migration 000048, NOT NULL, defaults to `worklist.read`, FK to
`iam.permission`): the permission this queue's work takes. `listWorkItems`, `getWorkItem` and
`claimWorkItem` all apply it, so a queue is seen by the people who can do its work rather than
by everybody who can read a worklist — and nobody can claim work off the people who can do it.
The five queues the platform raises work into carry the permission that work actually takes
(medical review, financial review, batch review, reconciliation, reservation review).

`workflow.work_item`: id, tenant_id, queue_id, `aggregate_type` text, `aggregate_id` uuid,
`title`, `priority` int NOT NULL DEFAULT 100, `assignee_actor_id` NULL,
`assigned_at`, `due_at`, `sla_minutes_snapshot` int, `status`
(`OPEN`,`CLAIMED`,`COMPLETED`,`CANCELLED`,`ESCALATED`), `outcome_code`, `completed_at`,
`completed_by`, row_version. Indexes: a partial worklist index on
`(tenant_id, queue_id, status, priority DESC, due_at)` where status in ('OPEN','CLAIMED'),
and `(tenant_id, assignee_actor_id, status)`.

**`sla_minutes_snapshot` is copied from the queue when the item is created** (v1.2 11.8).
Changing a queue's SLA must not silently make yesterday's items late or on time; the clock
an item was given is the clock it is judged by.

`workflow.approval_policy`: id, tenant_id, `action_code`, `scope_code`, `version_no`,
`min_amount numeric(20,6)` NULL, `max_amount` NULL, `required_role_codes text[]`,
`required_approver_count` int NOT NULL DEFAULT 1, `valid_from`, `valid_to`. Exclusion
constraint: no two policies for the same `(action_code, scope_code)` may overlap in
`daterange(valid_from, valid_to, '[)')`.

`workflow.comment`: id, tenant_id, `aggregate_type`, `aggregate_id`, work_item_id NULL,
`visibility` (`INTERNAL`,`PROVIDER`,`MEMBER`), `body` text, author, created_at.
Append-only. **A comment visible to a provider or a member may not carry clinical
detail**; that is a rule the health package (M5) will enforce on content, and this package
records the visibility it will be judged against.

RLS, touch triggers, composite keys throughout.

### 2.2 Claiming is optimistic, never a race

`claimWorkItem` is `UPDATE ... SET assignee = $me, status = 'CLAIMED' WHERE id = $1 AND
tenant_id = $2 AND status = 'OPEN' AND row_version = $expected`. Zero rows affected means
somebody else took it, and the answer is 409 `WORK_ITEM_ALREADY_CLAIMED` naming the
current assignee — never a silent overwrite (v1.2 11.8). `releaseWorkItem` puts it back to
OPEN; `reassignWorkItem` needs `worklist.reassign` and records the previous assignee in a
status event.

### 2.3 SLA and escalation

- `due_at` is `created_at + sla_minutes_snapshot` when the queue has an SLA.
- The **escalation job** is a scheduler job that moves overdue OPEN and CLAIMED items to
  the queue's `escalation_queue_id`, or marks them ESCALATED when there is none, and
  writes a status event. **It is idempotent**: an item already escalated is skipped, and
  running the job twice produces one escalation and one event. There is a test.
- The job never changes `due_at`: a late item stays late.

### 2.4 Approval policy

`putApprovalPolicies` writes the set for an action. The policy is read by the commands
that need it (WP-I4-01's approve, WP-I2-03's adjustment approval) to answer two questions:
which roles may approve, and how many approvals are needed for this amount. This package
owns the table and the lookup; it does not change any existing command — wiring the lookup
into a command is that command's own change, in its own package.

### 2.5 Permissions this package adds

The permission catalogue (migration 000008) and the role templates
(`internal/identity/application/roles.go`) are two separate places. A permission seeded
into the first but missing from the second is a permission nobody can ever hold — that
already happened once, with `pricing.quote`. Add both, in this package's migration and in
the same commit.

New: `worklist.read`, `worklist.claim`, `worklist.reassign`,
`workflow.queue.manage`, `workflow.policy.manage`. Grant the first two to every role that
does review work (PROGRAM_MANAGER, MEDICAL_REVIEWER, FINANCIAL_REVIEWER, PAYER_APPROVER),
`worklist.reassign` and the two manage permissions to TENANT_ADMIN.

## 3. Tests required

- **Concurrency**: 20 goroutines claim the same work item; exactly one succeeds and the
  other 19 get 409 with the winner's actor id. No item ends with two assignees.
- The SLA snapshot: change the queue's `sla_minutes` after an item exists and the item's
  `due_at` does not move.
- Escalation: an overdue item escalates once; running the job again does nothing; a
  not-yet-due item is untouched; an item in a queue with no escalation target is marked
  ESCALATED rather than moved.
- Approval policy overlap refused by the constraint; the lookup picks the policy whose
  amount band and period contain the request.
- Comments are append-only and carry their visibility; RLS on every table.

## 4. Acceptance criteria

- [ ] Two people cannot own the same work item, and the loser is told who won.
- [ ] An item is judged by the SLA it was given, not the one the queue has today.
- [ ] The escalation job is safe to run on any schedule and any number of times.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean; schema version 27.
