-- Inpatient stay, extension and segment queries (WP-I5-03, v1.2 10.4, 12.2, 16.7).
--
-- Four properties shape this file.
--
-- **No statement here decides what a caller may see.** Every read returns the whole row,
-- the extension's `reason_text` included, and the application service drops what the caller
-- may not have before the record reaches the wire. The projection is one decision made in
-- one place — the same one WP-I5-01 built — and a second copy of it in SQL would be a
-- second place for it to disagree.
--
-- The provider boundary is `scope_ids`, exactly as health.sql applies it: a nullable uuid[]
-- of tenant organization ids, NULL meaning the caller sees every stay of the tenant and a
-- non-null array binding it to the stays those organizations admitted. It is applied in
-- every read including the single-row ones and the ones that reach an extension or a segment
-- through its stay, so a row outside it is answered 404 rather than 403.
--
-- **No statement here moves a stay that has stopped being movable.** Every `Mark…`
-- statement names the statuses it may act on in its own predicate, so a command racing
-- another one changes nothing and reports it, rather than discharging a cancelled stay or
-- authorizing one somebody has already refused.
--
-- Days are read as `::text` and written as `::text::numeric`, so an exact decimal never
-- passes through a float on the way in or out (handbook section 3). They are read through
-- `trim_scale` as well, so five days comes back as "5" rather than "5.000000": the value is
-- the same exact decimal either way, and one canonical spelling is what lets a caller compare
-- two figures without parsing both first.

-- name: GetInpatientWindowSetting :one
-- How far back and how far forward an admission may be dated, in days. Two keys, one
-- statement, because a tenant that has set one and not the other is ordinary: a missing row
-- is not an error and the service falls back to its documented defaults.
SELECT (value_json #>> '{}')::text AS value
  FROM platform.tenant_setting
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND setting_key = sqlc.arg('setting_key');

-- name: CreateInpatientStay :one
-- `expected_discharge_at` is derived by the caller from the admission and the estimate. It
-- is passed in rather than computed here because the authorization's validity is set from
-- the same value, and two places computing it is one place for them to differ.
INSERT INTO health.inpatient_stay (
    tenant_id, case_id, provider_organization_id, location_id, attending_practitioner_id,
    admission_at, estimated_days, expected_discharge_at, service_request_id,
    admission_diagnosis_id, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('case_id'), sqlc.arg('provider_organization_id'),
        sqlc.narg('location_id'), sqlc.narg('attending_practitioner_id'),
        sqlc.arg('admission_at'), sqlc.arg('estimated_days'), sqlc.arg('expected_discharge_at'),
        sqlc.arg('service_request_id'), sqlc.narg('admission_diagnosis_id'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, case_id, provider_organization_id, location_id, attending_practitioner_id,
          admission_at, estimated_days, expected_discharge_at, discharge_at, status,
          service_request_id, authorization_id, admission_diagnosis_id,
          COALESCE(trim_scale(authorized_days)::text, '')::text AS authorized_days,
          COALESCE(trim_scale(actual_days)::text, '')::text AS actual_days,
          COALESCE(trim_scale(released_days)::text, '')::text AS released_days, over_authorization, cancel_reason_code,
          created_at, row_version;

-- name: GetInpatientStay :one
SELECT s.id, s.case_id, s.provider_organization_id, s.location_id, s.attending_practitioner_id,
       s.admission_at, s.estimated_days, s.expected_discharge_at, s.discharge_at, s.status,
       s.service_request_id, s.authorization_id, s.admission_diagnosis_id,
       COALESCE(trim_scale(s.authorized_days)::text, '')::text AS authorized_days,
       COALESCE(trim_scale(s.actual_days)::text, '')::text AS actual_days,
       COALESCE(trim_scale(s.released_days)::text, '')::text AS released_days, s.over_authorization, s.cancel_reason_code,
       s.created_at, s.row_version,
       -- The case's own sensitivity, carried with the row because that is what the
       -- projection decides on; a second round trip for it would be a second chance to
       -- forget it.
       c.sensitivity AS case_sensitivity, c.person_id
  FROM health.inpatient_stay s
  JOIN health.health_case c ON c.tenant_id = s.tenant_id AND c.id = s.case_id
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR s.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: LockInpatientStay :one
-- The read a command makes before it writes, so two commands on one stay serialise. The
-- lock is taken on the stay alone: locking the case as well would make an unrelated
-- encounter write wait behind a discharge.
SELECT s.id, s.case_id, s.provider_organization_id, s.location_id, s.attending_practitioner_id,
       s.admission_at, s.estimated_days, s.expected_discharge_at, s.discharge_at, s.status,
       s.service_request_id, s.authorization_id, s.admission_diagnosis_id,
       COALESCE(trim_scale(s.authorized_days)::text, '')::text AS authorized_days,
       COALESCE(trim_scale(s.actual_days)::text, '')::text AS actual_days,
       COALESCE(trim_scale(s.released_days)::text, '')::text AS released_days, s.over_authorization, s.cancel_reason_code,
       s.created_at, s.row_version,
       c.sensitivity AS case_sensitivity, c.person_id
  FROM health.inpatient_stay s
  JOIN health.health_case c ON c.tenant_id = s.tenant_id AND c.id = s.case_id
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR s.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   FOR UPDATE OF s;

-- name: LockInpatientStayByRequest :one
-- How the outbox subscriber finds what a decision moved. There is no scope here on purpose:
-- the subscriber is the worker acting for the tenant, not a person acting for a provider,
-- and a stay it could not see is a stay whose decision would silently do nothing.
SELECT s.id, s.case_id, s.provider_organization_id, s.location_id, s.attending_practitioner_id,
       s.admission_at, s.estimated_days, s.expected_discharge_at, s.discharge_at, s.status,
       s.service_request_id, s.authorization_id, s.admission_diagnosis_id,
       COALESCE(trim_scale(s.authorized_days)::text, '')::text AS authorized_days,
       COALESCE(trim_scale(s.actual_days)::text, '')::text AS actual_days,
       COALESCE(trim_scale(s.released_days)::text, '')::text AS released_days, s.over_authorization, s.cancel_reason_code,
       s.created_at, s.row_version,
       c.sensitivity AS case_sensitivity, c.person_id
  FROM health.inpatient_stay s
  JOIN health.health_case c ON c.tenant_id = s.tenant_id AND c.id = s.case_id
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.service_request_id = sqlc.arg('service_request_id')
   FOR UPDATE OF s;

-- name: ListInpatientStays :many
-- Keyset pagination on (admission_at DESC, id DESC); the caller asks for limit+1 rows to
-- learn whether a next page exists.
SELECT s.id, s.case_id, s.provider_organization_id, s.location_id, s.attending_practitioner_id,
       s.admission_at, s.estimated_days, s.expected_discharge_at, s.discharge_at, s.status,
       s.service_request_id, s.authorization_id, s.admission_diagnosis_id,
       COALESCE(trim_scale(s.authorized_days)::text, '')::text AS authorized_days,
       COALESCE(trim_scale(s.actual_days)::text, '')::text AS actual_days,
       COALESCE(trim_scale(s.released_days)::text, '')::text AS released_days, s.over_authorization, s.cancel_reason_code,
       s.created_at, s.row_version,
       c.sensitivity AS case_sensitivity, c.person_id
  FROM health.inpatient_stay s
  JOIN health.health_case c ON c.tenant_id = s.tenant_id AND c.id = s.case_id
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR s.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('case_id')::uuid IS NULL OR s.case_id = sqlc.narg('case_id')::uuid)
   AND (sqlc.narg('person_id')::uuid IS NULL OR c.person_id = sqlc.narg('person_id')::uuid)
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR s.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND (sqlc.narg('status')::text IS NULL OR s.status = sqlc.narg('status')::text)
   AND (sqlc.narg('admitted_from')::timestamptz IS NULL
        OR s.admission_at >= sqlc.narg('admitted_from')::timestamptz)
   AND (sqlc.narg('admitted_to')::timestamptz IS NULL
        OR s.admission_at < sqlc.narg('admitted_to')::timestamptz)
   AND (sqlc.narg('after_at')::timestamptz IS NULL
        OR (s.admission_at, s.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
 ORDER BY s.admission_at DESC, s.id DESC
 LIMIT sqlc.arg('page_size');

-- name: CountOpenInpatientStays :one
-- What stops a case being closed over a stay that is still running (WP-I5-01's StayPort).
-- The three statuses are the same three the partial unique index calls open, because "still
-- running" has to mean one thing.
SELECT count(*)
  FROM health.inpatient_stay s
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.case_id = sqlc.arg('case_id')
   AND s.status IN ('REQUESTED','AUTHORIZED','ADMITTED');

-- name: AuthorizeInpatientStay :execrows
-- The approval half of the outbox subscriber. REQUESTED is in the predicate, so a
-- redelivered decision finds nothing to do rather than reauthorizing a stay that has since
-- been admitted or discharged.
UPDATE health.inpatient_stay
   SET status = 'AUTHORIZED',
       authorization_id = sqlc.arg('authorization_id'),
       authorized_days = sqlc.arg('authorized_days')::text::numeric,
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'REQUESTED';

-- name: RejectInpatientStay :execrows
UPDATE health.inpatient_stay
   SET status = 'REJECTED', updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'REQUESTED';

-- name: AdmitInpatientStay :execrows
-- The patient is in a bed. It is not a command of its own: recording where somebody
-- actually is *is* the admission, so the segment write raises it.
UPDATE health.inpatient_stay
   SET status = 'ADMITTED', updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'AUTHORIZED';

-- name: ExtendInpatientStayAuthorization :execrows
-- The approved half of an extension, on the stay: more promised days and a later expected
-- discharge. `authorized_days` is added to rather than replaced, because the extension's
-- own authorization holds the added days and the original one holds the first lot.
UPDATE health.inpatient_stay
   SET authorized_days = coalesce(authorized_days, 0) + sqlc.arg('additional_days')::text::numeric,
       expected_discharge_at = sqlc.arg('expected_discharge_at'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('AUTHORIZED','ADMITTED');

-- name: DischargeInpatientStay :execrows
-- The whole reconciliation in one statement, so a stay is never momentarily discharged
-- without its figures. The predicate names both live statuses and the row version, so
-- discharging twice updates nothing the second time — which is what makes "running
-- discharge twice releases nothing twice" a property of the schema rather than of a flag.
UPDATE health.inpatient_stay
   SET status = 'DISCHARGED',
       discharge_at = sqlc.arg('discharge_at'),
       actual_days = sqlc.arg('actual_days')::text::numeric,
       released_days = sqlc.arg('released_days')::text::numeric,
       over_authorization = sqlc.arg('over_authorization'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('AUTHORIZED','ADMITTED')
   AND row_version = sqlc.arg('expected_row_version');

-- name: CancelInpatientStay :execrows
UPDATE health.inpatient_stay
   SET status = 'CANCELLED',
       cancel_reason_code = sqlc.arg('reason_code'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('REQUESTED','AUTHORIZED','ADMITTED')
   AND row_version = sqlc.arg('expected_row_version');

-- name: CreateStayExtension :one
INSERT INTO health.stay_extension (
    tenant_id, stay_id, sequence_no, additional_days, reason_code, reason_text,
    service_request_id, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('stay_id'), sqlc.arg('sequence_no'),
        sqlc.arg('additional_days'), sqlc.arg('reason_code'), sqlc.narg('reason_text'),
        sqlc.arg('service_request_id'), sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, stay_id, sequence_no, additional_days, reason_code, reason_text,
          service_request_id, authorization_id, status, created_at, row_version;

-- name: ListStayExtensions :many
SELECT e.id, e.stay_id, e.sequence_no, e.additional_days, e.reason_code, e.reason_text,
       e.service_request_id, e.authorization_id, e.status, e.created_at, e.row_version
  FROM health.stay_extension e
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND e.stay_id = sqlc.arg('stay_id')
 ORDER BY e.sequence_no;

-- name: LockStayExtensionByRequest :one
-- The extension half of the outbox subscriber; no scope, for the same reason the stay's
-- lookup has none.
SELECT e.id, e.stay_id, e.sequence_no, e.additional_days, e.reason_code, e.reason_text,
       e.service_request_id, e.authorization_id, e.status, e.created_at, e.row_version
  FROM health.stay_extension e
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND e.service_request_id = sqlc.arg('service_request_id')
   FOR UPDATE;

-- name: NextStayExtensionSequence :one
-- The next sequence number of a stay. It is a max rather than a counter because the
-- extensions of one stay are few and the row is locked by the command that reads it.
SELECT coalesce(max(sequence_no), 0)::int + 1 AS next_no
  FROM health.stay_extension
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND stay_id = sqlc.arg('stay_id');

-- name: CountPendingStayExtensions :one
-- The service's half of the one-undecided-extension rule. The database's half is the
-- partial unique index, and both exist on purpose: the caller is told plainly, and the
-- index refuses whatever writes it.
SELECT count(*)
  FROM health.stay_extension
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND stay_id = sqlc.arg('stay_id')
   AND status = 'REQUESTED';

-- name: ApproveStayExtension :execrows
UPDATE health.stay_extension
   SET status = 'APPROVED',
       authorization_id = sqlc.arg('authorization_id'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'REQUESTED';

-- name: RejectStayExtension :execrows
UPDATE health.stay_extension
   SET status = 'REJECTED', updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'REQUESTED';

-- name: CancelStayExtensions :execrows
-- Every undecided extension of a stay that is being cancelled. An extension outliving the
-- admission it extends would be a request nobody can decide against anything.
UPDATE health.stay_extension
   SET status = 'CANCELLED', updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND stay_id = sqlc.arg('stay_id')
   AND status = 'REQUESTED';

-- name: DeleteStaySegments :exec
-- The segment set is replaced as a whole: a segment id is not something anything else hangs
-- off, and a diff would be a second way to end up with a gap nobody meant.
DELETE FROM health.stay_segment
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND stay_id = sqlc.arg('stay_id');

-- name: CreateStaySegment :one
INSERT INTO health.stay_segment (
    tenant_id, stay_id, segment_type, starts_at, ends_at, room_code, bed_code,
    created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('stay_id'), sqlc.arg('segment_type'),
        sqlc.arg('starts_at'), sqlc.narg('ends_at'), sqlc.narg('room_code'),
        sqlc.narg('bed_code'), sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, stay_id, segment_type, starts_at, ends_at, room_code, bed_code,
          created_at, row_version;

-- name: ListStaySegments :many
SELECT g.id, g.stay_id, g.segment_type, g.starts_at, g.ends_at, g.room_code, g.bed_code,
       g.created_at, g.row_version
  FROM health.stay_segment g
 WHERE g.tenant_id = sqlc.arg('tenant_id')
   AND g.stay_id = sqlc.arg('stay_id')
 ORDER BY g.starts_at, g.id;

-- name: EndOpenStaySegments :execrows
-- Discharge ends every segment nobody ended. A segment left open past the discharge would
-- price a night the patient was not there for.
UPDATE health.stay_segment
   SET ends_at = sqlc.arg('ends_at'), updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND stay_id = sqlc.arg('stay_id')
   AND ends_at IS NULL
   AND sqlc.arg('ends_at')::timestamptz > starts_at;

-- name: GetInpatientStayCase :one
-- The case a stay is being opened against, with everything the create command has to check:
-- that it is open, whose it is, and which provider it belongs to.
SELECT c.id, c.person_id, c.program_id, c.enrollment_id, c.case_type,
       c.provider_organization_id, c.status, c.sensitivity
  FROM health.health_case c
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND c.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR c.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: GetInpatientAdmissionService :one
-- The catalogue definition the admission is requested against. It is found by code rather
-- than named by the caller: which service an admission is booked as is the tenant's
-- configuration, and a provider that could choose it could book a ward night as a session.
SELECT d.id, d.code, d.default_unit_type, d.requires_provider, d.active
  FROM catalog.service_definition d
 WHERE d.tenant_id = sqlc.arg('tenant_id')
   AND d.code = sqlc.arg('code');

-- name: GetStayAdmissionDiagnosis :one
-- The admission diagnosis has to belong to an encounter of this stay's own case. A
-- diagnosis borrowed from another case would put another person's condition on this
-- admission.
SELECT g.id, e.case_id
  FROM health.diagnosis g
  JOIN health.encounter e ON e.tenant_id = g.tenant_id AND e.id = g.encounter_id
 WHERE g.tenant_id = sqlc.arg('tenant_id')
   AND g.id = sqlc.arg('id');
