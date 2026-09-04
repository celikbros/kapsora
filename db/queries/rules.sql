-- Rule engine queries (WP-I3-04): rule sets, their versions, the CEL rules and test cases
-- under a version, and the append-only evaluations that record what was decided and by
-- which version.
--
-- Every statement filters on tenant_id explicitly and runs inside db.WithTenantTx, so RLS
-- is the second line of defence. The jsonb columns cross this boundary as bytes: the
-- application marshals them, because what belongs in an input snapshot is a business rule
-- (ids, dates and quantities only) and not something SQL can be trusted to decide.

-- name: CreateRuleSet :one
INSERT INTO rules.rule_set (tenant_id, code, name, domain_code, purpose)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('code'), sqlc.arg('name'),
        sqlc.arg('domain_code'), sqlc.arg('purpose'))
RETURNING id;

-- name: GetRuleSet :one
-- The version count comes along because a set with no version decides nothing, and a
-- screen should be able to say so without a second round trip.
SELECT s.id, s.code, s.name, s.domain_code, s.purpose, s.status, s.created_at, s.row_version,
       (SELECT count(*) FROM rules.rule_set_version v
         WHERE v.tenant_id = s.tenant_id AND v.rule_set_id = s.id)::int AS version_count
  FROM rules.rule_set s
 WHERE s.tenant_id = $1 AND s.id = $2;

-- name: ListRuleSets :many
-- Keyset pagination on (created_at DESC, id DESC); the caller asks for limit+1 rows to
-- learn whether a next page exists.
SELECT s.id, s.code, s.name, s.domain_code, s.purpose, s.status, s.created_at, s.row_version,
       (SELECT count(*) FROM rules.rule_set_version v
         WHERE v.tenant_id = s.tenant_id AND v.rule_set_id = s.id)::int AS version_count
  FROM rules.rule_set s
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('domain_code')::text IS NULL OR s.domain_code = sqlc.narg('domain_code')::text)
   AND (sqlc.narg('purpose')::text IS NULL OR s.purpose = sqlc.narg('purpose')::text)
   AND (sqlc.narg('status')::text IS NULL OR s.status = sqlc.narg('status')::text)
   -- The caller escapes the user's own wildcards, so the default backslash escape
   -- character makes '%' and '_' literal characters here.
   AND (sqlc.narg('q')::text IS NULL
        OR s.code ILIKE sqlc.narg('q')::text OR s.name ILIKE sqlc.narg('q')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (s.created_at, s.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY s.created_at DESC, s.id DESC
 LIMIT sqlc.arg('page_size');

-- name: UpdateRuleSet :one
-- platform.tg_touch_row bumps row_version and updated_at, so this statement never assigns
-- them. The code, the domain and the purpose are absent: they are what the set is, and
-- every published version was written against them.
UPDATE rules.rule_set
   SET name = sqlc.arg('name'),
       status = sqlc.arg('status')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version')
RETURNING row_version;

-- name: NextRuleSetVersionNo :one
SELECT (coalesce(max(version_no), 0) + 1)::int AS next_version_no
  FROM rules.rule_set_version
 WHERE tenant_id = $1 AND rule_set_id = $2;

-- name: CreateRuleSetVersion :one
INSERT INTO rules.rule_set_version (tenant_id, rule_set_id, version_no, valid_from,
                                    valid_to, input_schema, notes)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('rule_set_id'), sqlc.arg('version_no'),
        sqlc.narg('valid_from'), sqlc.narg('valid_to'), sqlc.arg('input_schema'),
        sqlc.narg('notes'))
RETURNING id;

-- name: GetRuleSetVersion :one
-- The two counts are what the submit gate is about: a version with no test case cannot be
-- reviewed, and a screen should be able to show that before trying.
SELECT v.id, v.rule_set_id, v.version_no, v.status, v.valid_from, v.valid_to,
       v.input_schema, v.content_hash, v.notes, v.submitted_at, v.submitted_by,
       v.published_at, v.published_by, v.retire_reason_code, v.review_comment,
       v.created_at, v.row_version,
       s.code AS rule_set_code,
       (SELECT count(*) FROM rules.rule r
         WHERE r.tenant_id = v.tenant_id AND r.rule_set_version_id = v.id)::int AS rule_count,
       (SELECT count(*) FROM rules.rule_test_case c
         WHERE c.tenant_id = v.tenant_id AND c.rule_set_version_id = v.id)::int AS test_case_count
  FROM rules.rule_set_version v
  JOIN rules.rule_set s ON s.tenant_id = v.tenant_id AND s.id = v.rule_set_id
 WHERE v.tenant_id = $1 AND v.id = $2;

-- name: ListRuleSetVersions :many
-- Highest version number first: a screen picking between "what decides now" and "what
-- decided then" wants the newest at the top.
SELECT v.id, v.rule_set_id, v.version_no, v.status, v.valid_from, v.valid_to,
       v.input_schema, v.content_hash, v.notes, v.submitted_at, v.submitted_by,
       v.published_at, v.published_by, v.retire_reason_code, v.review_comment,
       v.created_at, v.row_version,
       s.code AS rule_set_code,
       (SELECT count(*) FROM rules.rule r
         WHERE r.tenant_id = v.tenant_id AND r.rule_set_version_id = v.id)::int AS rule_count,
       (SELECT count(*) FROM rules.rule_test_case c
         WHERE c.tenant_id = v.tenant_id AND c.rule_set_version_id = v.id)::int AS test_case_count
  FROM rules.rule_set_version v
  JOIN rules.rule_set s ON s.tenant_id = v.tenant_id AND s.id = v.rule_set_id
 WHERE v.tenant_id = $1 AND v.rule_set_id = $2
 ORDER BY v.version_no DESC;

-- name: GetRuleSetVersionForUpdate :one
-- Every command on a version takes this row lock first, so two publishes of the same
-- version cannot both read UNDER_REVIEW and both proceed.
SELECT v.id, v.rule_set_id, v.version_no, v.status, v.valid_from, v.valid_to,
       v.input_schema, v.content_hash, v.notes, v.submitted_at, v.submitted_by,
       v.published_at, v.published_by, v.retire_reason_code, v.review_comment,
       v.created_at, v.row_version
  FROM rules.rule_set_version v
 WHERE v.tenant_id = $1 AND v.id = $2
   FOR UPDATE;

-- name: UpdateRuleSetVersionDraft :execrows
UPDATE rules.rule_set_version
   SET valid_from = sqlc.narg('valid_from'),
       valid_to = sqlc.narg('valid_to'),
       input_schema = sqlc.arg('input_schema'),
       notes = sqlc.narg('notes')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT';

-- name: TouchRuleSetVersion :execrows
-- Rules and test cases are child rows of the version, so writing any of them has to move
-- the ETag the caller holds for the version itself.
UPDATE rules.rule_set_version
   SET notes = notes
 WHERE tenant_id = $1 AND id = $2;

-- name: SubmitRuleSetVersion :execrows
UPDATE rules.rule_set_version
   SET status = 'UNDER_REVIEW',
       submitted_at = clock_timestamp(),
       submitted_by = sqlc.arg('actor_id'),
       review_comment = sqlc.narg('review_comment')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT';

-- name: PublishRuleSetVersion :execrows
-- ex_rule_set_version_published_overlap is the authority on two published versions of one
-- set covering the same date; it surfaces as SQLSTATE 23P01.
UPDATE rules.rule_set_version
   SET status = 'PUBLISHED',
       published_at = clock_timestamp(),
       published_by = sqlc.arg('actor_id'),
       content_hash = sqlc.arg('content_hash'),
       review_comment = coalesce(sqlc.narg('review_comment'), review_comment)
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'UNDER_REVIEW';

-- name: RetireRuleSetVersion :execrows
UPDATE rules.rule_set_version
   SET status = 'RETIRED',
       retire_reason_code = sqlc.arg('reason_code'),
       review_comment = coalesce(sqlc.narg('reason_text'), review_comment)
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'PUBLISHED';

-- name: ListRules :many
-- Ascending priority: 1 runs first, and uq_rule_priority keeps the order total, so this
-- is the evaluation order rather than "whatever the index returned".
SELECT r.id, r.rule_set_version_id, r.code, r.name, r.priority, r.condition, r.actions,
       r.explanation_code, r.explanation_params, r.stop_on_match, r.active,
       r.created_at, r.row_version
  FROM rules.rule r
 WHERE r.tenant_id = $1 AND r.rule_set_version_id = $2
 ORDER BY r.priority, r.code;

-- name: DeleteRules :execrows
DELETE FROM rules.rule
 WHERE tenant_id = $1 AND rule_set_version_id = $2;

-- name: CreateRule :batchexec
INSERT INTO rules.rule (tenant_id, rule_set_version_id, code, name, priority, condition,
                        actions, explanation_code, explanation_params, stop_on_match, active)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('rule_set_version_id'), sqlc.arg('code'),
        sqlc.arg('name'), sqlc.arg('priority'), sqlc.arg('condition'),
        sqlc.arg('actions'), sqlc.arg('explanation_code'), sqlc.arg('explanation_params'),
        sqlc.arg('stop_on_match'), sqlc.arg('active'));

-- name: ListRuleTestCases :many
SELECT c.id, c.rule_set_version_id, c.code, c.description, c.input, c.expected_outcome,
       c.expected_explanations, c.expected_actions, c.created_at, c.row_version
  FROM rules.rule_test_case c
 WHERE c.tenant_id = $1 AND c.rule_set_version_id = $2
 ORDER BY c.code;

-- name: DeleteRuleTestCases :execrows
DELETE FROM rules.rule_test_case
 WHERE tenant_id = $1 AND rule_set_version_id = $2;

-- name: CreateRuleTestCase :batchexec
INSERT INTO rules.rule_test_case (tenant_id, rule_set_version_id, code, description, input,
                                  expected_outcome, expected_explanations, expected_actions)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('rule_set_version_id'), sqlc.arg('code'),
        sqlc.narg('description'), sqlc.arg('input'), sqlc.arg('expected_outcome'),
        sqlc.arg('expected_explanations'), sqlc.narg('expected_actions'));

-- name: CreateRuleEvaluation :one
-- Append-only (platform.make_append_only): a dispute two years later reads this row and
-- must see what was true then. input_snapshot holds ids, dates and quantities only.
INSERT INTO rules.evaluation (tenant_id, subject_type, subject_id, rule_set_version_id,
                              input_hash, input_snapshot, outcome, duration_ms, evaluated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('subject_type'), sqlc.narg('subject_id'),
        sqlc.arg('rule_set_version_id'), sqlc.arg('input_hash'), sqlc.arg('input_snapshot'),
        sqlc.arg('outcome'), sqlc.narg('duration_ms'), sqlc.narg('evaluated_by'))
RETURNING id, evaluated_at;

-- name: CreateRuleEvaluationResult :batchexec
INSERT INTO rules.evaluation_result (tenant_id, evaluation_id, sequence, rule_id, rule_code,
                                     matched, action_type, action_payload,
                                     explanation_code, severity)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('evaluation_id'), sqlc.arg('sequence'),
        sqlc.narg('rule_id'), sqlc.arg('rule_code'), sqlc.arg('matched'),
        sqlc.narg('action_type'), sqlc.narg('action_payload'),
        sqlc.arg('explanation_code'), sqlc.arg('severity'));

-- name: GetRuleEvaluation :one
SELECT e.id, e.subject_type, e.subject_id, e.rule_set_version_id, e.input_hash,
       e.input_snapshot, e.outcome, e.duration_ms, e.evaluated_at, e.evaluated_by,
       v.rule_set_id, v.version_no, s.code AS rule_set_code
  FROM rules.evaluation e
  JOIN rules.rule_set_version v ON v.tenant_id = e.tenant_id AND v.id = e.rule_set_version_id
  JOIN rules.rule_set s ON s.tenant_id = v.tenant_id AND s.id = v.rule_set_id
 WHERE e.tenant_id = $1 AND e.id = $2;

-- name: ListRuleEvaluationResults :many
SELECT r.sequence, r.rule_id, r.rule_code, r.matched, r.action_type, r.action_payload,
       r.explanation_code, r.severity
  FROM rules.evaluation_result r
 WHERE r.tenant_id = $1 AND r.evaluation_id = $2
 ORDER BY r.sequence;

-- name: CountRuleEvaluations :one
-- Used by the test that proves a simulation writes nothing: the count before and after
-- must be equal.
SELECT count(*)::int AS evaluation_count
  FROM rules.evaluation
 WHERE tenant_id = $1;
