-- KAPSORA migration 000007: status history, transactional outbox, idempotency records (D3)
-- and partitioned append-only audit tables (D7).

-- Append-only status transition history for every aggregate.
CREATE TABLE workflow.status_event (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    aggregate_type      text NOT NULL,
    aggregate_id        uuid NOT NULL,
    from_status         text,
    to_status           text NOT NULL,
    transition_code     text NOT NULL,
    reason_code         text,
    reason_text         text,
    occurred_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    actor_id            uuid,
    request_id          uuid,
    metadata_json       jsonb NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT uq_status_event_id_tenant UNIQUE (tenant_id, id)
);

CREATE INDEX ix_status_event_aggregate
    ON workflow.status_event (tenant_id, aggregate_type, aggregate_id, occurred_at, id);
SELECT platform.make_append_only('workflow.status_event');

-- Transactional outbox. Written in the business transaction, dispatched by kapsora-worker
-- with SELECT ... FOR UPDATE SKIP LOCKED. Not tenant-RLS protected: the worker processes
-- every tenant; tenant_id is nullable for platform-level events.
CREATE TABLE system.outbox_event (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    aggregate_type      text NOT NULL,
    aggregate_id        uuid NOT NULL,
    event_type          text NOT NULL,
    event_schema_version integer NOT NULL DEFAULT 1 CHECK (event_schema_version > 0),
    payload_json        jsonb NOT NULL,
    headers_json        jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    available_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    status              text NOT NULL DEFAULT 'PENDING'
                        CHECK (status IN ('PENDING','PROCESSING','SUCCEEDED','FAILED','DEAD_LETTER')),
    attempt_count       integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    locked_at           timestamptz,
    locked_by           text,
    last_error_code     text,
    last_error_message  text,
    processed_at        timestamptz,
    deduplication_key   text,
    CONSTRAINT uq_outbox_dedupe UNIQUE NULLS NOT DISTINCT (tenant_id, event_type, deduplication_key),
    CONSTRAINT ck_outbox_event_type CHECK (event_type ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,4}$')
);

CREATE INDEX ix_outbox_dispatch
    ON system.outbox_event (status, available_at, occurred_at)
    WHERE status IN ('PENDING','FAILED');
CREATE INDEX ix_outbox_aggregate
    ON system.outbox_event (tenant_id, aggregate_type, aggregate_id, occurred_at);

-- D3: durable idempotency records for command endpoints. The API middleware inserts an
-- IN_PROGRESS row before executing a command (unique violation => concurrent duplicate),
-- then stores the outcome. Rows expire after 24h by default and are purged by a job.
CREATE TABLE system.idempotency_record (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    actor_id            uuid NOT NULL,
    command_code        text NOT NULL,
    idempotency_key     text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 16 AND 128),
    request_hash        bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    status              text NOT NULL DEFAULT 'IN_PROGRESS'
                        CHECK (status IN ('IN_PROGRESS','SUCCEEDED','FAILED')),
    response_status     smallint,
    response_body       jsonb,
    resource_type       text,
    resource_id         uuid,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at        timestamptz,
    expires_at          timestamptz NOT NULL DEFAULT clock_timestamp() + interval '24 hours',
    CONSTRAINT uq_idempotency_record UNIQUE (tenant_id, actor_id, command_code, idempotency_key),
    CONSTRAINT ck_idempotency_completion CHECK (
        (status = 'IN_PROGRESS' AND completed_at IS NULL)
        OR (status <> 'IN_PROGRESS' AND completed_at IS NOT NULL AND response_status IS NOT NULL)
    )
);

CREATE INDEX ix_idempotency_expiry
    ON system.idempotency_record (expires_at);

-- ---------------------------------------------------------------------------
-- Audit. Range-partitioned by month, append-only, indexes declared on the parent so every
-- partition inherits them. Tenant RLS is not applied: audit rows are written by the
-- platform on behalf of tenants and read through permission-checked, tenant-filtered
-- queries; the application role additionally has no UPDATE/DELETE privilege here.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION audit.ensure_month_partition(parent regclass, month_start date)
RETURNS text LANGUAGE plpgsql AS $$
DECLARE
    p_schema    text;
    p_table     text;
    range_start date := date_trunc('month', month_start)::date;
    range_end   date := (date_trunc('month', month_start) + interval '1 month')::date;
    part_name   text;
BEGIN
    SELECT n.nspname, c.relname INTO p_schema, p_table
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE c.oid = parent;

    part_name := format('%s_%s', p_table, to_char(range_start, 'YYYYMM'));

    IF to_regclass(format('%I.%I', p_schema, part_name)) IS NULL THEN
        EXECUTE format(
            'CREATE TABLE %I.%I PARTITION OF %I.%I FOR VALUES FROM (%L) TO (%L)',
            p_schema, part_name, p_schema, p_table,
            (range_start::timestamp AT TIME ZONE 'UTC'),
            (range_end::timestamp AT TIME ZONE 'UTC'));
    END IF;
    RETURN part_name;
END
$$;

CREATE TABLE audit.event (
    id                  uuid NOT NULL DEFAULT uuidv7(),
    occurred_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    tenant_id           uuid,
    actor_id            uuid,
    membership_id       uuid,
    event_category      text NOT NULL
                        CHECK (event_category IN ('ACCESS','AUTHENTICATION','BUSINESS','ADMIN','SECURITY','EXPORT','PRIVACY')),
    action_code         text NOT NULL,
    resource_type       text,
    resource_id         uuid,
    outcome             text NOT NULL CHECK (outcome IN ('SUCCESS','DENIED','FAILURE')),
    request_id          uuid,
    trace_id            text,
    source_ip           inet,
    user_agent_hash     bytea,
    reason_code         text,
    purpose_code        text,
    detail_json         jsonb NOT NULL DEFAULT '{}'::jsonb,
    before_hash         bytea,
    after_hash          bytea,
    PRIMARY KEY (occurred_at, id)
) PARTITION BY RANGE (occurred_at);

CREATE INDEX ix_audit_event_tenant_time
    ON audit.event (tenant_id, occurred_at DESC, id);
CREATE INDEX ix_audit_event_resource
    ON audit.event (tenant_id, resource_type, resource_id, occurred_at DESC);
CREATE INDEX ix_audit_event_actor
    ON audit.event (tenant_id, actor_id, occurred_at DESC);

CREATE TABLE audit.event_default PARTITION OF audit.event DEFAULT;
SELECT audit.ensure_month_partition('audit.event', CURRENT_DATE);
SELECT audit.ensure_month_partition('audit.event', (CURRENT_DATE + interval '1 month')::date);
SELECT platform.make_append_only('audit.event');

-- Who looked at whom: person, clinical, document and export access. Separate from
-- audit.event so privacy reporting and retention can differ.
CREATE TABLE audit.access_event (
    id                  uuid NOT NULL DEFAULT uuidv7(),
    occurred_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    tenant_id           uuid NOT NULL,
    actor_id            uuid NOT NULL,
    membership_id       uuid,
    person_id           uuid,
    resource_type       text NOT NULL,
    resource_id         uuid,
    access_type         text NOT NULL
                        CHECK (access_type IN ('VIEW','SEARCH','DOWNLOAD','EXPORT','PRINT','BREAK_GLASS')),
    data_classification text NOT NULL
                        CHECK (data_classification IN ('INTERNAL','CONFIDENTIAL','PERSONAL','HEALTH')),
    purpose_code        text,
    reason_text         text,
    outcome             text NOT NULL CHECK (outcome IN ('SUCCESS','DENIED')),
    request_id          uuid,
    trace_id            text,
    source_ip           inet,
    PRIMARY KEY (occurred_at, id)
) PARTITION BY RANGE (occurred_at);

CREATE INDEX ix_access_event_person_time
    ON audit.access_event (tenant_id, person_id, occurred_at DESC);
CREATE INDEX ix_access_event_actor_time
    ON audit.access_event (tenant_id, actor_id, occurred_at DESC);

CREATE TABLE audit.access_event_default PARTITION OF audit.access_event DEFAULT;
SELECT audit.ensure_month_partition('audit.access_event', CURRENT_DATE);
SELECT audit.ensure_month_partition('audit.access_event', (CURRENT_DATE + interval '1 month')::date);
SELECT platform.make_append_only('audit.access_event');

SELECT platform.enable_tenant_rls(t)
FROM unnest(ARRAY[
    'workflow.status_event',
    'system.idempotency_record'
]::regclass[]) AS t;

SELECT platform.grant_app_schema_usage('workflow');
SELECT platform.grant_app_schema_usage('system');
SELECT platform.grant_app_schema_usage('audit', 'SELECT, INSERT');
