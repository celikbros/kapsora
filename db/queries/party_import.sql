-- Member import queries (WP-I2-05): staging batches and rows, the reads matching uses
-- and the idempotent writes the apply job performs. Every statement filters on tenant_id
-- explicitly and runs inside db.WithTenantTx, so RLS is the second line of defence.
--
-- Staging rows never hold a plaintext identifier: `payload` carries the non-identifying
-- columns only (the CHECK constraint of migration 000018 enforces it), `identifiers`
-- carries `{type, scope_key, hash, masked}` and `identifier_cipher` the tenant-encrypted
-- envelope the apply step decrypts.

-- name: CreateImportBatch :one
INSERT INTO party.import_batch (tenant_id, sponsor_tenant_organization_id, plan_id, source_system,
                                source_version, file_name, file_sha256, format, row_count, status, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('sponsor_tenant_organization_id'), sqlc.narg('plan_id'),
        sqlc.arg('source_system'), sqlc.arg('source_version'), sqlc.arg('file_name'),
        sqlc.arg('file_sha256'), sqlc.arg('format'), sqlc.arg('row_count'), sqlc.arg('status'),
        sqlc.narg('created_by'))
RETURNING id, created_at, row_version;

-- name: GetImportBatch :one
SELECT id, sponsor_tenant_organization_id, plan_id, source_system, source_version, file_name,
       file_sha256, format, row_count, status, valid_count, invalid_count, matched_count,
       conflict_count, created_count, updated_count, skipped_count, error_summary,
       created_by, created_at, applied_at, row_version
  FROM party.import_batch
 WHERE tenant_id = $1 AND id = $2;

-- name: ListImportBatches :many
-- Keyset pagination on (created_at DESC, id DESC); the caller asks for limit+1 rows.
SELECT id, sponsor_tenant_organization_id, plan_id, source_system, source_version, file_name,
       file_sha256, format, row_count, status, valid_count, invalid_count, matched_count,
       conflict_count, created_count, updated_count, skipped_count, error_summary,
       created_by, created_at, applied_at, row_version
  FROM party.import_batch
 WHERE tenant_id = $1
   AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY created_at DESC, id DESC
 LIMIT sqlc.arg('page_size');

-- name: SetImportBatchStatus :execrows
-- Optimistic transition: the caller names the statuses it is willing to move away from,
-- so two concurrent apply commands cannot both start the job.
UPDATE party.import_batch
   SET status = sqlc.arg('status'),
       error_summary = sqlc.narg('error_summary'),
       applied_at = CASE WHEN sqlc.arg('status')::text = 'APPLIED' THEN clock_timestamp() ELSE applied_at END
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = ANY (sqlc.arg('from_statuses')::text[]);

-- name: SetImportBatchCounters :execrows
UPDATE party.import_batch
   SET valid_count = sqlc.arg('valid_count'),
       invalid_count = sqlc.arg('invalid_count'),
       matched_count = sqlc.arg('matched_count'),
       conflict_count = sqlc.arg('conflict_count'),
       created_count = sqlc.arg('created_count'),
       updated_count = sqlc.arg('updated_count'),
       skipped_count = sqlc.arg('skipped_count')
 WHERE tenant_id = sqlc.arg('tenant_id') AND id = sqlc.arg('id');

-- name: InsertImportRow :batchexec
-- Staging writes one pgx batch per chunk, so a 50 000 row file is a few round trips.
-- The row already carries the verdict of the text-level checks: a row whose columns do
-- not parse is stored INVALID with its field errors and never reaches matching.
INSERT INTO party.import_row (tenant_id, batch_id, row_no, source_record_id, payload,
                              identifiers, identifier_cipher, status, errors)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('batch_id'), sqlc.arg('row_no'), sqlc.arg('source_record_id'),
        sqlc.arg('payload'), sqlc.arg('identifiers'), sqlc.narg('identifier_cipher'),
        sqlc.arg('status'), sqlc.arg('errors'));

-- name: ListImportRowsForProcessing :many
-- Validation, matching and apply walk the batch in row_no order, a chunk at a time.
SELECT id, row_no, source_record_id, payload, identifiers, identifier_cipher, status,
       matched_person_id, decision, errors, applied_person_id, row_version
  FROM party.import_row
 WHERE tenant_id = $1
   AND batch_id = $2
   AND row_no > sqlc.arg('after_row_no')
   AND (sqlc.narg('statuses')::text[] IS NULL OR status = ANY (sqlc.narg('statuses')::text[]))
 ORDER BY row_no
 LIMIT sqlc.arg('page_size');

-- name: ListImportRows :many
-- The review queue: keyset pagination on row_no with an optional status filter.
SELECT id, row_no, source_record_id, payload, identifiers, identifier_cipher, status,
       matched_person_id, decision, errors, applied_person_id, row_version
  FROM party.import_row
 WHERE tenant_id = $1
   AND batch_id = $2
   AND row_no > sqlc.arg('after_row_no')
   AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
 ORDER BY row_no
 LIMIT sqlc.arg('page_size');

-- name: GetImportRow :one
SELECT id, row_no, source_record_id, payload, identifiers, identifier_cipher, status,
       matched_person_id, decision, errors, applied_person_id, row_version
  FROM party.import_row
 WHERE tenant_id = $1 AND batch_id = $2 AND id = $3;

-- name: FindImportRowBySourceRecord :one
SELECT id, row_no, source_record_id, payload, identifiers, identifier_cipher, status,
       matched_person_id, decision, errors, applied_person_id, row_version
  FROM party.import_row
 WHERE tenant_id = $1 AND batch_id = $2 AND source_record_id = $3;

-- name: UpdateImportRowOutcome :execrows
-- Written by validation, matching and apply; platform.tg_touch_row bumps row_version.
UPDATE party.import_row
   SET status = sqlc.arg('status'),
       payload = sqlc.arg('payload'),
       errors = sqlc.arg('errors'),
       matched_person_id = sqlc.narg('matched_person_id'),
       decision = sqlc.narg('decision'),
       applied_person_id = COALESCE(sqlc.narg('applied_person_id'), applied_person_id)
 WHERE tenant_id = sqlc.arg('tenant_id') AND id = sqlc.arg('id');

-- name: ReviewImportRow :execrows
-- The operator's decision under optimistic concurrency (If-Match on the row).
UPDATE party.import_row
   SET status = sqlc.arg('status'),
       decision = sqlc.arg('decision'),
       matched_person_id = sqlc.narg('matched_person_id'),
       decided_by = sqlc.narg('decided_by')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND batch_id = sqlc.arg('batch_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version');

-- name: ImportBatchCounters :one
-- The reconciliation summary of a batch, derived from the staging rows rather than kept
-- as a running total, so a resumed apply reports the same numbers as a clean one.
SELECT
    count(*) FILTER (WHERE status NOT IN ('INVALID','CONFLICT'))::bigint      AS valid_count,
    count(*) FILTER (WHERE status = 'INVALID')::bigint                        AS invalid_count,
    count(*) FILTER (WHERE matched_person_id IS NOT NULL)::bigint             AS matched_count,
    count(*) FILTER (WHERE status = 'CONFLICT')::bigint                       AS conflict_count,
    count(*) FILTER (WHERE status = 'APPLIED' AND decision = 'CREATE')::bigint AS created_count,
    count(*) FILTER (WHERE status = 'APPLIED' AND decision = 'UPDATE')::bigint AS updated_count,
    count(*) FILTER (WHERE status = 'SKIPPED')::bigint                        AS skipped_count,
    count(*) FILTER (WHERE status IN ('INVALID','CONFLICT'))::bigint          AS review_count,
    count(*) FILTER (WHERE status IN ('VALID','MATCHED'))::bigint             AS pending_apply_count
  FROM party.import_row
 WHERE tenant_id = $1 AND batch_id = $2;

-- name: CountPersonIdentifiersOfType :one
-- The import never replaces an identifier a person already carries; it only fills gaps.
SELECT count(*)::bigint
  FROM party.person_identifier
 WHERE tenant_id = $1 AND person_id = $2 AND identifier_type = $3;

-- name: FindPersonBySourceRecord :one
-- The strongest idempotency key of a re-imported file (unique index of migration 000018).
SELECT id, first_name, middle_name, last_name, birth_date, sex_at_birth, status, row_version
  FROM party.person
 WHERE tenant_id = $1 AND source_system = $2 AND source_record_id = $3;

-- name: GetPersonForImport :one
SELECT id, first_name, middle_name, last_name, birth_date, sex_at_birth, status, row_version
  FROM party.person
 WHERE tenant_id = $1 AND id = $2;

-- name: CreatePersonFromImport :one
INSERT INTO party.person (tenant_id, first_name, middle_name, last_name, normalized_name,
                          birth_date, sex_at_birth, source_system, source_record_id,
                          created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('first_name'), sqlc.narg('middle_name'), sqlc.arg('last_name'),
        sqlc.arg('normalized_name'), sqlc.narg('birth_date'), sqlc.narg('sex_at_birth'),
        sqlc.arg('source_system'), sqlc.arg('source_record_id'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, row_version;

-- name: UpdatePersonFromImport :execrows
-- The import is the system of record for the file's columns; it does not carry an
-- If-Match, so the update is unconditional inside the apply transaction.
UPDATE party.person
   SET first_name = sqlc.arg('first_name'),
       middle_name = sqlc.narg('middle_name'),
       last_name = sqlc.arg('last_name'),
       normalized_name = sqlc.arg('normalized_name'),
       birth_date = sqlc.narg('birth_date'),
       sex_at_birth = sqlc.narg('sex_at_birth'),
       source_system = sqlc.arg('source_system'),
       source_record_id = sqlc.arg('source_record_id'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id') AND id = sqlc.arg('id');

-- name: FindPersonIdentifierByHash :one
SELECT id, person_id
  FROM party.person_identifier
 WHERE tenant_id = $1 AND identifier_type = $2 AND scope_key = $3 AND identifier_hash = $4;

-- name: FindImportMembershipBySource :one
SELECT id, person_id, principal_membership_id, membership_type, status,
       lower(valid_period)::date AS valid_from, upper(valid_period)::date AS valid_to
  FROM party.sponsor_membership
 WHERE tenant_id = $1
   AND sponsor_tenant_organization_id = $2
   AND source_system = $3
   AND source_record_id = $4;

-- name: FindImportMembershipByPerson :one
-- Fallback when a person already carries a membership created outside the import.
SELECT id, person_id, principal_membership_id, membership_type, status,
       lower(valid_period)::date AS valid_from, upper(valid_period)::date AS valid_to
  FROM party.sponsor_membership
 WHERE tenant_id = $1
   AND sponsor_tenant_organization_id = $2
   AND person_id = $3
   AND status IN ('PENDING','ACTIVE')
 ORDER BY principal_membership_id NULLS FIRST, created_at
 LIMIT 1;

-- name: CreateImportMembership :one
INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
                                      principal_membership_id, membership_type, external_member_no,
                                      status, valid_period, source_system, source_record_id)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('person_id'), sqlc.arg('sponsor_tenant_organization_id'),
        sqlc.narg('principal_membership_id'), sqlc.arg('membership_type'), sqlc.narg('external_member_no'),
        sqlc.arg('status'),
        daterange(sqlc.arg('valid_from')::date, sqlc.narg('valid_to')::date, '[)'),
        sqlc.arg('source_system'), sqlc.arg('source_record_id'))
RETURNING id;

-- name: FindImportRelationship :one
SELECT id
  FROM party.person_relationship
 WHERE tenant_id = $1
   AND source_person_id = $2
   AND target_person_id = $3
   AND relationship_type = $4;

-- name: CreateImportRelationship :one
INSERT INTO party.person_relationship (tenant_id, source_person_id, target_person_id,
                                       relationship_type, valid_period)
VALUES ($1, $2, $3, $4, daterange(sqlc.arg('valid_from')::date, sqlc.narg('valid_to')::date, '[)'))
RETURNING id;

-- name: FindImportEnrollment :one
SELECT id, plan_id, status
  FROM benefit.enrollment
 WHERE tenant_id = $1 AND sponsor_membership_id = $2 AND plan_id = $3;

-- name: CreateImportEnrollment :one
INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period,
                                source_system, source_record_id)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('sponsor_membership_id'), sqlc.arg('plan_id'),
        sqlc.arg('status'),
        daterange(sqlc.arg('valid_from')::date, sqlc.narg('valid_to')::date, '[)'),
        sqlc.arg('source_system'), sqlc.arg('source_record_id'))
RETURNING id;

-- name: FindPlansByCode :many
-- plan.code is unique per program, so a file code can in theory hit two programs; the
-- import rejects that instead of guessing.
SELECT id, program_id, code, status
  FROM benefit.plan
 WHERE tenant_id = $1 AND code = $2
 ORDER BY id;
