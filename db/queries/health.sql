-- Health case, encounter and diagnosis queries (WP-I5-01, v1.2 9.12, 10.3, 11.10, 16.14).
--
-- Three properties shape this file.
--
-- **No statement here decides what a caller may see.** Every read returns the whole row,
-- including `notes_clinical`, `branch_code` and `sensitivity`, and the application service
-- drops what the caller may not have before the record reaches the wire. That is on
-- purpose: the projection is one decision made in one place, and a second copy of it in
-- SQL would be a second place for it to disagree.
--
-- The provider boundary is the same shape the service request queries apply: `scope_ids` is
-- a nullable uuid[] of tenant organization ids, NULL means the caller sees every case of
-- the tenant, and a non-null array binds it to the cases those organizations opened. It is
-- applied in every read including the single-row ones and the reads that reach an encounter
-- or a diagnosis through its case, so a row outside it is answered 404 rather than 403 —
-- that such a case exists at all is somebody else's business. Unlike the document queries
-- there is no `IS NULL OR` escape for a case the tenant owns itself: a back-office case
-- naming no provider is not a provider's business either.
--
-- `sensitive` is never a parameter a caller controls. It is computed by
-- ResolveDiagnosisCodeValues from the code value's own category and handed back to the
-- write, and the case's `sensitivity` is recomputed from the diagnoses that are actually
-- stored. There is no statement here that would accept a sensitivity from outside.

-- name: CreateHealthCase :one
INSERT INTO health.health_case (
    tenant_id, person_id, program_id, enrollment_id, case_type,
    provider_organization_id, opened_at, service_request_id, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('person_id'), sqlc.arg('program_id'),
        sqlc.arg('enrollment_id'), sqlc.arg('case_type'),
        sqlc.narg('provider_organization_id'), sqlc.arg('opened_at'),
        sqlc.narg('service_request_id'), sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, person_id, program_id, enrollment_id, case_type, provider_organization_id,
          opened_at, closed_at, status, sensitivity, service_request_id, created_at, row_version;

-- name: GetHealthCase :one
SELECT c.id, c.person_id, c.program_id, c.enrollment_id, c.case_type,
       c.provider_organization_id, c.opened_at, c.closed_at, c.status, c.sensitivity,
       c.service_request_id, c.created_at, c.row_version
  FROM health.health_case c
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND c.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR c.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: LockHealthCase :one
-- The read a command makes before it writes, so two commands on one case serialise.
SELECT c.id, c.person_id, c.program_id, c.enrollment_id, c.case_type,
       c.provider_organization_id, c.opened_at, c.closed_at, c.status, c.sensitivity,
       c.service_request_id, c.created_at, c.row_version
  FROM health.health_case c
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND c.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR c.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   FOR UPDATE;

-- name: ListHealthCases :many
-- Keyset pagination on (opened_at DESC, id DESC); the caller asks for limit+1 rows to learn
-- whether a next page exists.
SELECT c.id, c.person_id, c.program_id, c.enrollment_id, c.case_type,
       c.provider_organization_id, c.opened_at, c.closed_at, c.status, c.sensitivity,
       c.service_request_id, c.created_at, c.row_version
  FROM health.health_case c
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR c.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('person_id')::uuid IS NULL OR c.person_id = sqlc.narg('person_id')::uuid)
   AND (sqlc.narg('program_id')::uuid IS NULL OR c.program_id = sqlc.narg('program_id')::uuid)
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR c.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status')::text)
   AND (sqlc.narg('case_type')::text IS NULL OR c.case_type = sqlc.narg('case_type')::text)
   AND (sqlc.narg('opened_from')::timestamptz IS NULL OR c.opened_at >= sqlc.narg('opened_from')::timestamptz)
   AND (sqlc.narg('opened_to')::timestamptz IS NULL OR c.opened_at < sqlc.narg('opened_to')::timestamptz)
   AND (sqlc.narg('after_at')::timestamptz IS NULL
        OR (c.opened_at, c.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
 ORDER BY c.opened_at DESC, c.id DESC
 LIMIT sqlc.arg('page_size');

-- name: CloseHealthCase :execrows
-- The whole precondition is the predicate: a case that is already closed, or that somebody
-- else has moved since the caller read it, matches no row and updates nothing.
UPDATE health.health_case
   SET status = 'CLOSED', closed_at = sqlc.arg('closed_at'), updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'OPEN'
   AND row_version = sqlc.arg('expected_row_version');

-- name: SetHealthCaseSensitivity :exec
-- Recomputed from the diagnoses that are actually stored, never sent by a caller.
UPDATE health.health_case
   SET sensitivity = sqlc.arg('sensitivity'), updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND sensitivity <> sqlc.arg('sensitivity');

-- name: CountOpenEncounters :one
-- What stops a case being closed over an encounter nobody ended.
SELECT count(*)
  FROM health.encounter e
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND e.case_id = sqlc.arg('case_id')
   AND e.ended_at IS NULL;

-- name: CreateEncounter :one
INSERT INTO health.encounter (
    tenant_id, case_id, encounter_type, started_at, ended_at, location_id,
    practitioner_id, branch_code, notes_clinical, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('case_id'), sqlc.arg('encounter_type'),
        sqlc.arg('started_at'), sqlc.narg('ended_at'), sqlc.narg('location_id'),
        sqlc.narg('practitioner_id'), sqlc.narg('branch_code'), sqlc.narg('notes_clinical'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, case_id, encounter_type, started_at, ended_at, location_id, practitioner_id,
          branch_code, notes_clinical, created_at, row_version;

-- name: GetEncounter :one
-- The join is the boundary: an encounter is reached through its case, so a case outside the
-- caller's provider scope takes its encounters with it.
SELECT e.id, e.case_id, e.encounter_type, e.started_at, e.ended_at, e.location_id,
       e.practitioner_id, e.branch_code, e.notes_clinical, e.created_at, e.row_version
  FROM health.encounter e
  JOIN health.health_case c ON c.tenant_id = e.tenant_id AND c.id = e.case_id
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND e.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR c.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: ListCaseEncounters :many
SELECT e.id, e.case_id, e.encounter_type, e.started_at, e.ended_at, e.location_id,
       e.practitioner_id, e.branch_code, e.notes_clinical, e.created_at, e.row_version
  FROM health.encounter e
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND e.case_id = sqlc.arg('case_id')
 ORDER BY e.started_at, e.id;

-- name: ListEncounterDiagnoses :many
-- The code and its display come from the catalogue rather than being copied onto the
-- diagnosis: a code system is versioned and a display that was frozen here would drift.
SELECT d.id, d.encounter_id, d.code_system_id, d.code_value_id, d.diagnosis_type,
       d.sensitive, d.recorded_at, d.recorded_by, s.code AS code_system_code,
       v.code AS code, v.display AS display
  FROM health.diagnosis d
  JOIN catalog.code_system s ON s.tenant_id = d.tenant_id AND s.id = d.code_system_id
  JOIN catalog.code_value v ON v.tenant_id = d.tenant_id AND v.id = d.code_value_id
 WHERE d.tenant_id = sqlc.arg('tenant_id')
   AND d.encounter_id = sqlc.arg('encounter_id')
 ORDER BY d.diagnosis_type, d.recorded_at, d.id;

-- name: DeleteEncounterDiagnoses :exec
-- putEncounterDiagnoses replaces the set as a whole: the set is the unit, and a diagnosis
-- id is not something anything else hangs off.
DELETE FROM health.diagnosis
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND encounter_id = sqlc.arg('encounter_id');

-- name: CreateDiagnosis :one
INSERT INTO health.diagnosis (
    tenant_id, encounter_id, code_system_id, code_value_id, diagnosis_type,
    sensitive, recorded_at, recorded_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('encounter_id'), sqlc.arg('code_system_id'),
        sqlc.arg('code_value_id'), sqlc.arg('diagnosis_type'), sqlc.arg('sensitive'),
        sqlc.arg('recorded_at'), sqlc.narg('actor_id'))
RETURNING id;

-- name: ResolveDiagnosisCodeValues :many
-- What a write needs to know about the codes it was given: that they exist, which system
-- they belong to, and whether the publisher's own categorisation marks them sensitive.
-- `attributes` is the jsonb migration 000019 already gives every code value, so the fact
-- lives with the code rather than in a list somewhere in Go.
SELECT v.id, v.code_system_id, v.code, v.display, v.active,
       -- Written as an EXISTS rather than a bare boolean expression because sqlc types a
       -- computed column as interface{} and an untyped column is a column somebody scans
       -- wrongly. The predicate is the same one either way.
       EXISTS (SELECT 1 FROM catalog.code_value x
                WHERE x.tenant_id = v.tenant_id AND x.id = v.id
                  AND x.attributes @> '{"sensitive": true}'::jsonb) AS sensitive,
       s.code AS code_system_code
  FROM catalog.code_value v
  JOIN catalog.code_system s ON s.tenant_id = v.tenant_id AND s.id = v.code_system_id
 WHERE v.tenant_id = sqlc.arg('tenant_id')
   AND v.id = ANY(sqlc.arg('ids')::uuid[]);

-- name: CaseHasSensitiveDiagnosis :one
-- The case's sensitivity, recomputed over every encounter it holds.
SELECT EXISTS (
    SELECT 1
      FROM health.diagnosis d
      JOIN health.encounter e ON e.tenant_id = d.tenant_id AND e.id = d.encounter_id
     WHERE d.tenant_id = sqlc.arg('tenant_id')
       AND e.case_id = sqlc.arg('case_id')
       AND d.sensitive
);

-- name: ListHealthAccessEvents :many
-- Who looked at a person's clinical data and why. audit.access_event carries no RLS policy
-- (it is written by the platform for every tenant), so the tenant predicate here is the
-- isolation rather than a filter on top of one.
SELECT a.id, a.occurred_at, a.actor_id, a.membership_id, a.person_id, a.resource_type,
       a.resource_id, a.access_type, a.purpose_code, a.reason_text, a.outcome
  FROM audit.access_event a
 WHERE a.tenant_id = sqlc.arg('tenant_id')
   AND a.data_classification = 'HEALTH'
   AND (sqlc.narg('person_id')::uuid IS NULL OR a.person_id = sqlc.narg('person_id')::uuid)
   AND (sqlc.narg('after_at')::timestamptz IS NULL
        OR (a.occurred_at, a.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
 ORDER BY a.occurred_at DESC, a.id DESC
 LIMIT sqlc.arg('page_size');

-- name: GetHealthCaseEnrollment :one
-- The enrollment a case is opened under, with the person it belongs to and the program of
-- its plan. An enrollment belongs to exactly one program, so the caller does not have to
-- name one — a provider-scoped actor may read neither programs nor enrollments and could
-- not repeat an id it has never seen.
SELECT e.id, e.plan_id, p.program_id, e.status, m.person_id
  FROM benefit.enrollment e
  JOIN party.sponsor_membership m ON m.tenant_id = e.tenant_id AND m.id = e.sponsor_membership_id
  JOIN benefit.plan p ON p.tenant_id = e.tenant_id AND p.id = e.plan_id
 WHERE e.tenant_id = sqlc.arg('tenant_id') AND e.id = sqlc.arg('id');

-- name: GetHealthCaseServiceRequest :one
-- The request a case may be opened from, with the one thing this module has to decide about
-- it: whether any line of any of its versions names a service in the HEALTH domain. A case
-- opened from an accommodation booking is not a health case.
SELECT r.id, r.person_id, r.program_id, r.enrollment_id, r.request_type,
       EXISTS (
           SELECT 1
             FROM service.service_request_version v
             JOIN service.service_request_item i
               ON i.tenant_id = v.tenant_id AND i.service_request_version_id = v.id
             JOIN catalog.service_definition d
               ON d.tenant_id = i.tenant_id AND d.id = i.service_definition_id
             JOIN catalog.service_category c
               ON c.tenant_id = d.tenant_id AND c.id = d.category_id
            WHERE v.tenant_id = r.tenant_id
              AND v.service_request_id = r.id
              AND c.domain_code = 'HEALTH'
       ) AS has_health_service
  FROM service.service_request r
 WHERE r.tenant_id = sqlc.arg('tenant_id') AND r.id = sqlc.arg('id');

-- name: HealthProviderOrganizationExists :one
-- The provider a case names has to be an organization this tenant deals with in a provider
-- role.
SELECT EXISTS (
    SELECT 1
      FROM directory.tenant_organization o
     WHERE o.tenant_id = sqlc.arg('tenant_id')
       AND o.id = sqlc.arg('id')
       AND o.relationship_role = 'PROVIDER'
);

-- name: ClinicalAccessPurposeExists :one
SELECT EXISTS (
    SELECT 1 FROM health.clinical_access_purpose WHERE purpose_code = sqlc.arg('purpose_code')
);

-- name: ListClinicalAccessPurposes :many
SELECT purpose_code, display_name FROM health.clinical_access_purpose ORDER BY sort_order;

-- name: EndEncounter :execrows
-- The application holds the parent case lock, shared by close/create/diagnosis commands.
UPDATE health.encounter
   SET ended_at = sqlc.arg('ended_at'), updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id') AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('expected_version') AND ended_at IS NULL;
