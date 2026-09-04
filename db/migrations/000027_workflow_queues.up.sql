-- 000027: work queues, work items, SLA and approval policy (WP-I4-03, v1.2 9.10, 11.8, 16.7).
--
-- Work that needs a person needs a queue, an owner and a clock. These four tables are the
-- list an operator opens in the morning: what is waiting, what is mine, what is late, and
-- who decided what.
--
-- Two properties of the schema carry the whole package.
--
-- `work_item.sla_minutes_snapshot` is a stored copy of the queue's SLA, taken when the
-- item was created, and `due_at` is a stored column derived from it once. Neither is a
-- view over the queue: retuning a queue's SLA must not make yesterday's items late or on
-- time, because the clock an item was given is the clock it is judged by (v1.2 11.8).
--
-- Nothing in the item's shape lets two people own it. `row_version` is what a claim tests
-- against, and the CHECKs below say that an OPEN item has no owner and a CLAIMED one
-- always has one, so a claim that loses its race cannot leave a half-owned row behind.

INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('worklist.read',          'İş listesi ve iş kalemlerini okuma',        'NORMAL'),
    ('worklist.claim',         'İş kalemini üstlenme ve bırakma',           'NORMAL'),
    ('worklist.reassign',      'İş kalemini başkasına atama',               'SENSITIVE'),
    ('workflow.queue.manage',  'İş kuyruğu tanımlama ve güncelleme',        'NORMAL'),
    ('workflow.policy.manage', 'Onay politikası tanımlama ve güncelleme',   'PRIVILEGED')
ON CONFLICT (code) DO NOTHING;

-- A queue is a place work waits and a clock it waits against. `escalation_queue_id` is a
-- self composite FK so a queue can never escalate into another tenant's queue, and the
-- CHECK forbids the one-step cycle; a longer cycle is possible and harmless, because the
-- escalation job moves an item at most once (see work_item.escalated_at).
CREATE TABLE workflow.work_queue (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    code                text NOT NULL,
    name                text NOT NULL,
    domain_code         text NOT NULL
                        CHECK (domain_code IN (
                            'GENERIC','HEALTH','ACCOMMODATION','ASSISTANCE','EDUCATION',
                            'SPORT','TRANSPORT','CARE','OTHER'
                        )),
    assignment_policy   text NOT NULL DEFAULT 'MANUAL'
                        CHECK (assignment_policy IN ('MANUAL','ROUND_ROBIN','LEAST_LOADED')),
    -- NULL means the queue keeps no clock, and its items are never due and never late.
    sla_minutes         integer CHECK (sla_minutes IS NULL OR sla_minutes > 0),
    escalation_queue_id uuid,
    active              boolean NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by          uuid,
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by          uuid,
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_work_queue_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_work_queue_code UNIQUE (tenant_id, code),
    CONSTRAINT fk_work_queue_escalation FOREIGN KEY (tenant_id, escalation_queue_id)
        REFERENCES workflow.work_queue(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_work_queue_code CHECK (code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    CONSTRAINT ck_work_queue_name CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    CONSTRAINT ck_work_queue_not_self_escalation
        CHECK (escalation_queue_id IS NULL OR escalation_queue_id <> id)
);

CREATE INDEX ix_work_queue_domain ON workflow.work_queue (tenant_id, domain_code, code);
SELECT platform.attach_touch_row('workflow.work_queue'::regclass);
SELECT platform.enable_tenant_rls('workflow.work_queue'::regclass);

-- One piece of work waiting for one person. `aggregate_type` and `aggregate_id` are
-- deliberately untyped by a foreign key: an item is raised over a service request today
-- and over a claim, a booking or a medical report later, and a column per aggregate would
-- mean a migration every time a module starts producing work.
CREATE TABLE workflow.work_item (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    queue_id                uuid NOT NULL,
    aggregate_type          text NOT NULL,
    aggregate_id            uuid NOT NULL,
    title                   text NOT NULL,
    priority                integer NOT NULL DEFAULT 100,
    assignee_actor_id       uuid REFERENCES iam.actor(id),
    assigned_at             timestamptz,
    due_at                  timestamptz,
    -- The queue's SLA as it stood when this item was raised. It is copied, never read
    -- through: this column and due_at are what the item is judged by, whatever the queue
    -- says today.
    sla_minutes_snapshot    integer CHECK (sla_minutes_snapshot IS NULL OR sla_minutes_snapshot > 0),
    status                  text NOT NULL DEFAULT 'OPEN'
                            CHECK (status IN ('OPEN','CLAIMED','COMPLETED','CANCELLED','ESCALATED')),
    outcome_code            text,
    completed_at            timestamptz,
    completed_by            uuid REFERENCES iam.actor(id),
    -- When the escalation job last acted on this item, and the queue it took it from.
    -- Without a marker the job could not tell a moved item from one that has never been
    -- escalated: moving does not change the status, and the item is still overdue in its
    -- new queue, so it would be moved again on every pass.
    escalated_at            timestamptz,
    escalated_from_queue_id uuid,
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by              uuid,
    updated_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by              uuid,
    row_version             bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_work_item_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_work_item_queue FOREIGN KEY (tenant_id, queue_id)
        REFERENCES workflow.work_queue(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_work_item_escalated_from FOREIGN KEY (tenant_id, escalated_from_queue_id)
        REFERENCES workflow.work_queue(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_work_item_title CHECK (length(btrim(title)) BETWEEN 1 AND 200),
    CONSTRAINT ck_work_item_priority CHECK (priority BETWEEN 1 AND 1000),
    -- An owner and the moment of taking ownership are one fact, so they arrive and leave
    -- together; an OPEN item has no owner at all, which is what makes "nobody owns this"
    -- readable without joining the history.
    CONSTRAINT ck_work_item_assignment
        CHECK ((assignee_actor_id IS NULL) = (assigned_at IS NULL)),
    CONSTRAINT ck_work_item_open CHECK (status <> 'OPEN' OR assignee_actor_id IS NULL),
    CONSTRAINT ck_work_item_claimed CHECK (status <> 'CLAIMED' OR assignee_actor_id IS NOT NULL),
    -- A completed item says when it was completed and what it decided. "It was closed"
    -- with neither is a row nobody can report on.
    CONSTRAINT ck_work_item_completed CHECK (
        (status = 'COMPLETED' AND completed_at IS NOT NULL AND outcome_code IS NOT NULL)
        OR (status <> 'COMPLETED' AND completed_at IS NULL)
    ),
    -- A due date exists exactly when a clock was given, so "is this late" is a comparison
    -- rather than a lookup back to the queue.
    CONSTRAINT ck_work_item_due CHECK ((due_at IS NULL) = (sla_minutes_snapshot IS NULL))
);

-- The worklist itself: one queue, the items still live in it, most urgent first. The
-- partial predicate keeps finished work out of the index, so the list stays the same size
-- however much has been done.
CREATE INDEX ix_work_item_worklist
    ON workflow.work_item (tenant_id, queue_id, status, priority DESC, due_at)
    WHERE status IN ('OPEN','CLAIMED');
CREATE INDEX ix_work_item_assignee
    ON workflow.work_item (tenant_id, assignee_actor_id, status);
-- The escalation sweep reads across queues, which the worklist index cannot serve: its
-- leading column after the tenant is the queue. This one carries only the rows the sweep
-- can still act on, so a tenant whose work is all done costs the job one empty scan.
CREATE INDEX ix_work_item_escalation
    ON workflow.work_item (tenant_id, due_at)
    WHERE status IN ('OPEN','CLAIMED') AND escalated_at IS NULL;
SELECT platform.attach_touch_row('workflow.work_item'::regclass);
SELECT platform.enable_tenant_rls('workflow.work_item'::regclass);

-- Which roles may approve an action, and how many of them the amount requires. The table
-- is owned here and read by the commands that need it — WP-I4-01's approve, WP-I2-03's
-- adjustment approval — so that "who may approve six thousand lira" is one answer given
-- in one place rather than a threshold repeated in every command that has one.
CREATE TABLE workflow.approval_policy (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    action_code             text NOT NULL,
    scope_code              text NOT NULL,
    version_no              integer NOT NULL DEFAULT 1 CHECK (version_no > 0),
    -- The band the policy covers, in the currency of the action. NULL is an open end:
    -- no minimum, or no maximum.
    min_amount              numeric(20,6) CHECK (min_amount IS NULL OR min_amount >= 0),
    max_amount              numeric(20,6) CHECK (max_amount IS NULL OR max_amount >= 0),
    -- Empty is not null: an empty array means "no role is required", null would mean
    -- "not decided yet", and a policy that has not decided is not a policy.
    required_role_codes     text[] NOT NULL DEFAULT '{}',
    required_approver_count integer NOT NULL DEFAULT 1 CHECK (required_approver_count > 0),
    valid_from              date NOT NULL,
    valid_to                date,
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by              uuid,
    updated_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by              uuid,
    row_version             bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_approval_policy_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT ck_approval_policy_action CHECK (action_code ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){0,3}$'),
    CONSTRAINT ck_approval_policy_scope CHECK (scope_code ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    CONSTRAINT ck_approval_policy_band
        CHECK (min_amount IS NULL OR max_amount IS NULL OR max_amount >= min_amount),
    CONSTRAINT ck_approval_policy_period CHECK (valid_to IS NULL OR valid_to > valid_from),
    -- Two policies for the same action and scope may not cover the same day. Without this
    -- "how many approvals does this need" would have two answers, and which one a command
    -- got would depend on the order the rows happened to come back in.
    CONSTRAINT ex_approval_policy_overlap EXCLUDE USING gist (
        tenant_id WITH =,
        action_code WITH =,
        scope_code WITH =,
        daterange(valid_from, valid_to, '[)') WITH &&
    )
);

CREATE INDEX ix_approval_policy_action
    ON workflow.approval_policy (tenant_id, action_code, valid_from);
SELECT platform.attach_touch_row('workflow.approval_policy'::regclass);
SELECT platform.enable_tenant_rls('workflow.approval_policy'::regclass);

-- What people said about the work. Append-only: a comment that could be edited after the
-- decision it influenced is not a record of why the decision was made.
--
-- `visibility` is who the comment was written for, and it is recorded here so that the
-- health package (M5) has something to judge content against: a comment a provider or a
-- member can read may not carry clinical detail. This package stores the visibility; it
-- does not inspect the body.
CREATE TABLE workflow.comment (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id       uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    aggregate_type  text NOT NULL,
    aggregate_id    uuid NOT NULL,
    work_item_id    uuid,
    visibility      text NOT NULL DEFAULT 'INTERNAL'
                    CHECK (visibility IN ('INTERNAL','PROVIDER','MEMBER')),
    body            text NOT NULL,
    author_actor_id uuid REFERENCES iam.actor(id),
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_comment_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_comment_work_item FOREIGN KEY (tenant_id, work_item_id)
        REFERENCES workflow.work_item(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_comment_body CHECK (length(btrim(body)) BETWEEN 1 AND 4000)
);

CREATE INDEX ix_comment_aggregate
    ON workflow.comment (tenant_id, aggregate_type, aggregate_id, created_at, id);
CREATE INDEX ix_comment_work_item
    ON workflow.comment (tenant_id, work_item_id, created_at, id)
    WHERE work_item_id IS NOT NULL;
SELECT platform.make_append_only('workflow.comment'::regclass);
SELECT platform.enable_tenant_rls('workflow.comment'::regclass);

SELECT platform.grant_app_schema_usage('workflow');
