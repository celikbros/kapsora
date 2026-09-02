-- Platform coordination tables: transactional outbox, idempotency records, rate limit
-- buckets and scheduler job runs (WP-I1-04).

-- ---------------------------------------------------------------- outbox

-- name: InsertOutboxEvent :one
INSERT INTO system.outbox_event (
    tenant_id, aggregate_type, aggregate_id, event_type, event_schema_version,
    payload_json, headers_json, available_at, deduplication_key
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (tenant_id, event_type, deduplication_key) WHERE deduplication_key IS NOT NULL DO NOTHING
RETURNING id;

-- name: ClaimOutboxEvents :many
WITH candidates AS (
    SELECT id
      FROM system.outbox_event
     WHERE status IN ('PENDING', 'FAILED')
       AND available_at <= clock_timestamp()
     ORDER BY occurred_at, id
     LIMIT $1
       FOR UPDATE SKIP LOCKED
)
UPDATE system.outbox_event AS e
   SET status = 'PROCESSING',
       locked_at = clock_timestamp(),
       locked_by = $2,
       attempt_count = e.attempt_count + 1
  FROM candidates c
 WHERE e.id = c.id
RETURNING e.id, e.tenant_id, e.aggregate_type, e.aggregate_id, e.event_type,
          e.event_schema_version, e.payload_json, e.headers_json, e.occurred_at,
          e.attempt_count;

-- name: MarkOutboxSucceeded :exec
UPDATE system.outbox_event
   SET status = 'SUCCEEDED', processed_at = clock_timestamp(),
       locked_at = NULL, locked_by = NULL,
       last_error_code = NULL, last_error_message = NULL
 WHERE id = $1;

-- name: MarkOutboxFailed :exec
UPDATE system.outbox_event
   SET status = $2, available_at = $3,
       locked_at = NULL, locked_by = NULL,
       last_error_code = $4, last_error_message = $5
 WHERE id = $1;

-- name: DeferOutboxEvent :exec
UPDATE system.outbox_event
   SET status = 'PENDING', available_at = $2,
       locked_at = NULL, locked_by = NULL,
       attempt_count = GREATEST(attempt_count - 1, 0),
       last_error_code = $3
 WHERE id = $1;

-- name: RecoverStaleOutboxEvents :execrows
UPDATE system.outbox_event
   SET status = 'PENDING', locked_at = NULL, locked_by = NULL
 WHERE status = 'PROCESSING'
   AND locked_at < $1;

-- name: OutboxBacklog :one
SELECT count(*)::bigint AS pending,
       coalesce(min(available_at), clock_timestamp())::timestamptz AS oldest_available_at
  FROM system.outbox_event
 WHERE status IN ('PENDING', 'FAILED');

-- name: GetOutboxEvent :one
SELECT id, status, attempt_count, available_at, last_error_code, processed_at
  FROM system.outbox_event
 WHERE id = $1;

-- ---------------------------------------------------------------- idempotency

-- name: TryInsertIdempotencyRecord :one
INSERT INTO system.idempotency_record (
    tenant_id, actor_id, command_code, idempotency_key, request_hash, expires_at
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (tenant_id, actor_id, command_code, idempotency_key) DO NOTHING
RETURNING id;

-- name: GetIdempotencyRecord :one
SELECT id, request_hash, status, response_status, response_body,
       resource_type, resource_id, created_at, completed_at, expires_at
  FROM system.idempotency_record
 WHERE tenant_id = $1 AND actor_id = $2 AND command_code = $3 AND idempotency_key = $4;

-- name: CompleteIdempotencyRecord :exec
UPDATE system.idempotency_record
   SET status = $2, response_status = $3, response_body = $4,
       resource_type = $5, resource_id = $6, completed_at = clock_timestamp()
 WHERE id = $1;

-- name: DeleteIdempotencyRecord :exec
DELETE FROM system.idempotency_record WHERE id = $1;

-- name: PurgeExpiredIdempotencyRecords :execrows
DELETE FROM system.idempotency_record WHERE expires_at < $1;

-- ---------------------------------------------------------------- rate limit

-- name: EnsureRateLimitBucket :exec
INSERT INTO system.rate_limit_bucket (bucket_key, tokens, updated_at)
VALUES ($1, $2, clock_timestamp())
ON CONFLICT (bucket_key) DO NOTHING;

-- name: LockRateLimitBucket :one
SELECT tokens, updated_at, clock_timestamp()::timestamptz AS db_now
  FROM system.rate_limit_bucket
 WHERE bucket_key = $1
   FOR UPDATE;

-- name: UpdateRateLimitBucket :exec
UPDATE system.rate_limit_bucket
   SET tokens = $2, updated_at = $3
 WHERE bucket_key = $1;

-- name: PurgeIdleRateLimitBuckets :execrows
DELETE FROM system.rate_limit_bucket WHERE updated_at < $1;

-- ---------------------------------------------------------------- job runs

-- name: StartJobRun :one
INSERT INTO system.job_run (job_code, scheduled_for)
VALUES ($1, $2)
ON CONFLICT (job_code, scheduled_for) DO NOTHING
RETURNING id;

-- name: FinishJobRun :exec
UPDATE system.job_run
   SET finished_at = clock_timestamp(), status = $2, error_code = $3, metrics_json = $4
 WHERE id = $1;

-- name: LastJobRun :one
SELECT scheduled_for, started_at, finished_at, status
  FROM system.job_run
 WHERE job_code = $1
 ORDER BY scheduled_for DESC
 LIMIT 1;
