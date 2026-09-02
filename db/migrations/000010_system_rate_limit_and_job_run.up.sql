-- KAPSORA migration 000010: PostgreSQL-backed rate limit buckets and scheduler job runs
-- (WP-I1-04; ADR-021: no Valkey, PostgreSQL holds counters and coordination state).

-- Token buckets keyed by tenant + actor/client + route. Not tenant-RLS protected: the key
-- already scopes the row and the limiter runs before a tenant context exists for
-- unauthenticated requests. Never put a sensitive identifier into bucket_key.
CREATE TABLE system.rate_limit_bucket (
    bucket_key          text PRIMARY KEY CHECK (char_length(bucket_key) BETWEEN 1 AND 512),
    tokens              double precision NOT NULL,
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX ix_rate_limit_bucket_updated
    ON system.rate_limit_bucket (updated_at);

-- One row per scheduled execution of a periodic job; the unique constraint prevents two
-- leaders (or a restarted leader) from running the same slot twice.
CREATE TABLE system.job_run (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    job_code            text NOT NULL CHECK (job_code ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$'),
    scheduled_for       timestamptz NOT NULL,
    started_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at         timestamptz,
    status              text NOT NULL DEFAULT 'RUNNING'
                        CHECK (status IN ('RUNNING','SUCCEEDED','FAILED')),
    error_code          text,
    metrics_json        jsonb NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT uq_job_run_schedule UNIQUE (job_code, scheduled_for),
    CONSTRAINT ck_job_run_finish CHECK (
        (status = 'RUNNING' AND finished_at IS NULL)
        OR (status <> 'RUNNING' AND finished_at IS NOT NULL)
    )
);

CREATE INDEX ix_job_run_code_time
    ON system.job_run (job_code, scheduled_for DESC);

SELECT platform.grant_app_schema_usage('system');
