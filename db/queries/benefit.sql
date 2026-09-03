-- Benefit queries (WP-I2-02): programs, plans, plan versions with maker-checker review,
-- entitlement definitions and enrollments. Every statement filters on tenant_id
-- explicitly and runs inside db.WithTenantTx, so RLS is the second line of defence.
--
-- benefit.plan_version carries no row_version column (migration 000004 is on main and
-- must not change), so its optimistic-concurrency token is the system column xmin: it
-- changes on every update of the row and is exposed as the contract rowVersion / ETag.
-- Statements that only touch child rows call TouchPlanVersion so the token still moves.

-- name: GetProgramType :one
SELECT code, display_name, status
  FROM benefit.program_type
 WHERE tenant_id = $1 AND code = $2;

-- name: GetBenefitOrganization :one
SELECT t.id, t.relationship_role, t.status, o.display_name
  FROM directory.tenant_organization t
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE t.tenant_id = $1 AND t.id = $2;

-- name: CreateProgram :one
INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id, payer_tenant_organization_id,
                             code, name, program_type, valid_period)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('sponsor_organization_id'), sqlc.arg('payer_organization_id'),
        sqlc.arg('code'), sqlc.arg('name'), sqlc.arg('program_type'),
        daterange(sqlc.narg('valid_from')::date, sqlc.narg('valid_to')::date, '[)'))
RETURNING id, row_version;

-- name: GetProgram :one
SELECT p.id, p.code, p.name, p.program_type, p.status,
       p.sponsor_tenant_organization_id, p.payer_tenant_organization_id,
       lower(p.valid_period)::date AS valid_from, upper(p.valid_period)::date AS valid_to,
       p.created_at, p.row_version,
       so.display_name AS sponsor_display_name,
       po.display_name AS payer_display_name,
       (SELECT count(*) FROM benefit.plan pl
         WHERE pl.tenant_id = p.tenant_id AND pl.program_id = p.id) AS plan_count
  FROM benefit.program p
  JOIN directory.tenant_organization sto
       ON sto.tenant_id = p.tenant_id AND sto.id = p.sponsor_tenant_organization_id
  JOIN directory.organization so ON so.id = sto.organization_id
  JOIN directory.tenant_organization pto
       ON pto.tenant_id = p.tenant_id AND pto.id = p.payer_tenant_organization_id
  JOIN directory.organization po ON po.id = pto.organization_id
 WHERE p.tenant_id = $1 AND p.id = $2;

-- name: ListPrograms :many
-- Keyset pagination on (created_at DESC, id DESC); the caller asks for limit+1 rows to
-- learn whether a next page exists.
SELECT p.id, p.code, p.name, p.program_type, p.status,
       p.sponsor_tenant_organization_id, p.payer_tenant_organization_id,
       lower(p.valid_period)::date AS valid_from, upper(p.valid_period)::date AS valid_to,
       p.created_at, p.row_version,
       so.display_name AS sponsor_display_name,
       po.display_name AS payer_display_name,
       (SELECT count(*) FROM benefit.plan pl
         WHERE pl.tenant_id = p.tenant_id AND pl.program_id = p.id) AS plan_count
  FROM benefit.program p
  JOIN directory.tenant_organization sto
       ON sto.tenant_id = p.tenant_id AND sto.id = p.sponsor_tenant_organization_id
  JOIN directory.organization so ON so.id = sto.organization_id
  JOIN directory.tenant_organization pto
       ON pto.tenant_id = p.tenant_id AND pto.id = p.payer_tenant_organization_id
  JOIN directory.organization po ON po.id = pto.organization_id
 WHERE p.tenant_id = $1
   AND (sqlc.narg('status')::text IS NULL OR p.status = sqlc.narg('status')::text)
   AND (sqlc.narg('q')::text IS NULL
        OR p.code ILIKE sqlc.narg('q')::text OR p.name ILIKE sqlc.narg('q')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (p.created_at, p.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY p.created_at DESC, p.id DESC
 LIMIT sqlc.arg('page_size');

-- name: UpdateProgram :one
-- Optimistic concurrency: a stale If-Match matches no row. platform.tg_touch_row bumps
-- row_version and updated_at, so this statement never assigns them.
UPDATE benefit.program
   SET name = sqlc.arg('name'),
       status = sqlc.arg('status'),
       valid_period = daterange(sqlc.narg('valid_from')::date, sqlc.narg('valid_to')::date, '[)')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version')
RETURNING row_version;

-- name: CreatePlan :one
INSERT INTO benefit.plan (tenant_id, program_id, code, name)
VALUES ($1, $2, $3, $4)
RETURNING id, row_version;

-- name: GetPlan :one
SELECT id, program_id, code, name, status, created_at, row_version
  FROM benefit.plan
 WHERE tenant_id = $1 AND id = $2;

-- name: ListPlans :many
SELECT id, program_id, code, name, status, created_at, row_version
  FROM benefit.plan
 WHERE tenant_id = $1 AND program_id = $2
 ORDER BY code;

-- name: UpdatePlan :one
UPDATE benefit.plan
   SET name = sqlc.arg('name'),
       status = sqlc.arg('status')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version')
RETURNING row_version;

-- name: NextPlanVersionNo :one
SELECT (coalesce(max(version_no), 0) + 1)::int AS next_version_no
  FROM benefit.plan_version
 WHERE tenant_id = $1 AND plan_id = $2;

-- name: CreatePlanVersion :one
INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, valid_period, notes, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('plan_id'), sqlc.arg('version_no'),
        daterange(sqlc.narg('valid_from')::date, sqlc.narg('valid_to')::date, '[)'),
        sqlc.narg('notes'), sqlc.narg('actor_id'))
RETURNING id;

-- name: GetPlanVersion :one
SELECT v.id, v.plan_id, v.version_no, v.status,
       lower(v.valid_period)::date AS valid_from, upper(v.valid_period)::date AS valid_to,
       v.configuration_hash, v.published_at, v.published_by, v.submitted_at, v.submitted_by,
       v.review_comment, v.retire_reason_code, v.retire_reason_text, v.notes, v.created_at,
       v.xmin::text::bigint AS row_version
  FROM benefit.plan_version v
 WHERE v.tenant_id = $1 AND v.id = $2;

-- name: ListPlanVersions :many
SELECT v.id, v.plan_id, v.version_no, v.status,
       lower(v.valid_period)::date AS valid_from, upper(v.valid_period)::date AS valid_to,
       v.configuration_hash, v.published_at, v.published_by, v.submitted_at, v.submitted_by,
       v.review_comment, v.retire_reason_code, v.retire_reason_text, v.notes, v.created_at,
       v.xmin::text::bigint AS row_version
  FROM benefit.plan_version v
 WHERE v.tenant_id = $1 AND v.plan_id = $2
 ORDER BY v.version_no DESC;

-- name: ResolvePlanVersion :one
-- The PUBLISHED version whose validity period contains the service date (lower bound
-- inclusive, upper bound exclusive). The exclusion constraint guarantees at most one.
SELECT v.id, v.plan_id, v.version_no, v.status,
       lower(v.valid_period)::date AS valid_from, upper(v.valid_period)::date AS valid_to,
       v.configuration_hash, v.published_at, v.published_by, v.submitted_at, v.submitted_by,
       v.review_comment, v.retire_reason_code, v.retire_reason_text, v.notes, v.created_at,
       v.xmin::text::bigint AS row_version
  FROM benefit.plan_version v
 WHERE v.tenant_id = $1 AND v.plan_id = $2 AND v.status = 'PUBLISHED'
   AND v.valid_period @> sqlc.arg('as_of')::date;

-- name: GetPlanVersionForUpdate :one
-- Locks the version row for the state commands; the caller compares row_version (xmin)
-- against If-Match before it writes.
SELECT v.id, v.plan_id, v.version_no, v.status,
       lower(v.valid_period)::date AS valid_from, upper(v.valid_period)::date AS valid_to,
       v.configuration_hash, v.published_at, v.published_by, v.submitted_at, v.submitted_by,
       v.review_comment, v.retire_reason_code, v.retire_reason_text, v.notes, v.created_at,
       v.xmin::text::bigint AS row_version
  FROM benefit.plan_version v
 WHERE v.tenant_id = $1 AND v.id = $2
   FOR UPDATE;

-- name: UpdatePlanVersionDraft :execrows
UPDATE benefit.plan_version
   SET valid_period = daterange(sqlc.narg('valid_from')::date, sqlc.narg('valid_to')::date, '[)'),
       notes = sqlc.narg('notes')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT';

-- name: TouchPlanVersion :execrows
-- A no-op UPDATE moves xmin, so replacing the entitlement definitions of a draft also
-- invalidates the ETag the caller holds.
UPDATE benefit.plan_version
   SET notes = notes
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT';

-- name: SubmitPlanVersion :execrows
UPDATE benefit.plan_version
   SET status = 'UNDER_REVIEW',
       submitted_at = clock_timestamp(),
       submitted_by = sqlc.arg('actor_id'),
       review_comment = sqlc.narg('review_comment')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT';

-- name: PublishPlanVersion :execrows
-- ck_plan_version_maker_checker is the second line behind the service-side check.
UPDATE benefit.plan_version
   SET status = 'PUBLISHED',
       published_at = clock_timestamp(),
       published_by = sqlc.arg('actor_id'),
       configuration_hash = sqlc.arg('configuration_hash'),
       review_comment = coalesce(sqlc.narg('review_comment'), review_comment)
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'UNDER_REVIEW';

-- name: RetirePlanVersion :execrows
UPDATE benefit.plan_version
   SET status = 'RETIRED',
       retire_reason_code = sqlc.arg('reason_code'),
       retire_reason_text = sqlc.narg('reason_text')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'PUBLISHED';

-- name: ListEntitlementDefinitions :many
-- Quantities cross the boundary as exact decimal text; no float ever holds a quantity.
SELECT id, code, name, unit_type, currency_code, period_type, period_length,
       initial_quantity::text AS initial_quantity,
       allow_overdraft, rollover_policy,
       coalesce(rollover_cap::text, '')::text AS rollover_cap,
       family_shared, status
  FROM benefit.entitlement_definition
 WHERE tenant_id = $1 AND plan_version_id = $2
 ORDER BY code;

-- name: DeleteEntitlementDefinitions :execrows
DELETE FROM benefit.entitlement_definition
 WHERE tenant_id = $1 AND plan_version_id = $2;

-- name: CreateEntitlementDefinition :one
INSERT INTO benefit.entitlement_definition (
    tenant_id, plan_version_id, code, name, unit_type, currency_code, period_type,
    period_length, initial_quantity, allow_overdraft, rollover_policy, rollover_cap, family_shared)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('plan_version_id'), sqlc.arg('code'), sqlc.arg('name'),
        sqlc.arg('unit_type'), sqlc.narg('currency_code'), sqlc.arg('period_type'),
        sqlc.narg('period_length'), sqlc.arg('initial_quantity')::text::numeric,
        sqlc.arg('allow_overdraft'), sqlc.arg('rollover_policy'),
        sqlc.narg('rollover_cap')::text::numeric, sqlc.arg('family_shared'))
RETURNING id;

-- name: GetEnrollmentMembership :one
SELECT id, person_id, status,
       lower(valid_period)::date AS valid_from, upper(valid_period)::date AS valid_to
  FROM party.sponsor_membership
 WHERE tenant_id = $1 AND id = $2;

-- name: CreateEnrollment :one
INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status,
                                valid_period, enrollment_reason)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('sponsor_membership_id'), sqlc.arg('plan_id'),
        sqlc.arg('status'),
        daterange(sqlc.arg('valid_from')::date, sqlc.narg('valid_to')::date, '[)'),
        sqlc.narg('enrollment_reason'))
RETURNING id, row_version;

-- name: GetEnrollment :one
SELECT e.id, e.sponsor_membership_id, e.plan_id, e.status,
       lower(e.valid_period)::date AS valid_from, upper(e.valid_period)::date AS valid_to,
       e.enrollment_reason, e.source_system, e.created_at, e.row_version,
       m.person_id, p.code AS plan_code, p.program_id
  FROM benefit.enrollment e
  JOIN party.sponsor_membership m ON m.tenant_id = e.tenant_id AND m.id = e.sponsor_membership_id
  JOIN benefit.plan p ON p.tenant_id = e.tenant_id AND p.id = e.plan_id
 WHERE e.tenant_id = $1 AND e.id = $2;

-- name: ListPersonEnrollments :many
SELECT e.id, e.sponsor_membership_id, e.plan_id, e.status,
       lower(e.valid_period)::date AS valid_from, upper(e.valid_period)::date AS valid_to,
       e.enrollment_reason, e.source_system, e.created_at, e.row_version,
       m.person_id, p.code AS plan_code, p.program_id
  FROM benefit.enrollment e
  JOIN party.sponsor_membership m ON m.tenant_id = e.tenant_id AND m.id = e.sponsor_membership_id
  JOIN benefit.plan p ON p.tenant_id = e.tenant_id AND p.id = e.plan_id
 WHERE e.tenant_id = $1 AND m.person_id = $2
 ORDER BY e.created_at DESC, e.id DESC;

-- name: ListEnrollments :many
SELECT e.id, e.sponsor_membership_id, e.plan_id, e.status,
       lower(e.valid_period)::date AS valid_from, upper(e.valid_period)::date AS valid_to,
       e.enrollment_reason, e.source_system, e.created_at, e.row_version,
       m.person_id, p.code AS plan_code, p.program_id
  FROM benefit.enrollment e
  JOIN party.sponsor_membership m ON m.tenant_id = e.tenant_id AND m.id = e.sponsor_membership_id
  JOIN benefit.plan p ON p.tenant_id = e.tenant_id AND p.id = e.plan_id
 WHERE e.tenant_id = $1
   AND (sqlc.narg('plan_id')::uuid IS NULL OR e.plan_id = sqlc.narg('plan_id')::uuid)
   AND (sqlc.narg('status')::text IS NULL OR e.status = sqlc.narg('status')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (e.created_at, e.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY e.created_at DESC, e.id DESC
 LIMIT sqlc.arg('page_size');

-- name: UpdateEnrollment :one
UPDATE benefit.enrollment
   SET status = sqlc.arg('status'),
       valid_period = daterange(lower(valid_period), sqlc.narg('valid_to')::date, '[)')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version')
RETURNING row_version;

-- name: PersonExists :one
-- The person-scoped enrollment routes answer 404 for an unknown person; the row itself
-- is never read, so no personal data leaves party.
SELECT EXISTS (
    SELECT 1 FROM party.person WHERE tenant_id = $1 AND id = $2
) AS person_exists;
