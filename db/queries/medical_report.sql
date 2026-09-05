-- Medical report queries (WP-I5-02, v1.2 10.5, 12.4, 11.10).
--
-- Three properties shape this file, and two of them are the same ones health.sql carries.
--
-- **No statement here decides what a caller may see.** Every read returns the whole row,
-- `clinical_summary`, `report_type` and `review_comment` included, and the application
-- service drops what the caller may not have before the record reaches the wire. The
-- projection is one decision made in one place; a second copy of it in SQL would be a
-- second place for it to disagree. Each read carries the report's case sensitivity along
-- with the row, because that is what the projection decides on and a second round trip for
-- it would be a second chance to forget it.
--
-- The provider boundary is `scope_ids`: a nullable uuid[] of tenant organization ids, NULL
-- meaning the caller sees every report of the tenant and a non-null array binding it to the
-- reports those organizations issued. It is applied in every read including the single-row
-- ones, so a report outside it is answered 404 rather than 403.
--
-- And **no statement here rewrites a decided report**. The four `Mark…` statements below
-- each name the status they may act on in their own predicate, so a command that arrives
-- against a report somebody else has already decided changes nothing and reports it, rather
-- than overwriting the decision a claim is leaning on. `UpdateMedicalReportDraft` names
-- DRAFT for the same reason: the freeze is a predicate, not a Go `if` somebody can forget.

-- name: CreateMedicalReport :one
-- The id is supplied rather than defaulted: version 1 is its own chain root, so the insert
-- has to know the id before the row exists.
INSERT INTO health.medical_report (
    id, tenant_id, person_id, case_id, reference, version_no, root_report_id,
    supersedes_report_id, report_type, report_subtype, issuing_practitioner_id,
    issuing_provider_organization_id, issued_at, valid_from, valid_to, clinical_summary,
    created_by, updated_by)
VALUES (sqlc.arg('id'), sqlc.arg('tenant_id'), sqlc.arg('person_id'), sqlc.narg('case_id'),
        sqlc.arg('reference'), sqlc.arg('version_no'), sqlc.arg('root_report_id'),
        sqlc.narg('supersedes_report_id'), sqlc.arg('report_type'), sqlc.narg('report_subtype'),
        sqlc.narg('issuing_practitioner_id'), sqlc.narg('issuing_provider_organization_id'),
        sqlc.arg('issued_at'), sqlc.arg('valid_from'), sqlc.arg('valid_to'),
        sqlc.narg('clinical_summary'), sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, person_id, case_id, reference, version_no, root_report_id, supersedes_report_id,
          report_type, report_subtype, issuing_practitioner_id, issuing_provider_organization_id,
          issued_at, valid_from, valid_to, status, clinical_summary, review_comment,
          reject_reason_code, reviewed_by, reviewed_at, submitted_at, submitted_by,
          created_at, row_version;

-- name: GetMedicalReport :one
SELECT r.id, r.person_id, r.case_id, r.reference, r.version_no, r.root_report_id,
       r.supersedes_report_id, r.report_type, r.report_subtype, r.issuing_practitioner_id,
       r.issuing_provider_organization_id, r.issued_at, r.valid_from, r.valid_to, r.status,
       r.clinical_summary, r.review_comment, r.reject_reason_code, r.reviewed_by,
       r.reviewed_at, r.submitted_at, r.submitted_by, r.created_at, r.row_version,
       -- The case's own sensitivity, or STANDARD for a report written outside a case the
       -- platform holds. It travels with the row because the projection decides on it.
       COALESCE(c.sensitivity, 'STANDARD')::text AS case_sensitivity
  FROM health.medical_report r
  LEFT JOIN health.health_case c ON c.tenant_id = r.tenant_id AND c.id = r.case_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR r.issuing_provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: LockMedicalReport :one
-- The read every command makes before it writes, so two commands on one report serialise.
-- FOR UPDATE OF r, because the left-joined case is read here and not written.
SELECT r.id, r.person_id, r.case_id, r.reference, r.version_no, r.root_report_id,
       r.supersedes_report_id, r.report_type, r.report_subtype, r.issuing_practitioner_id,
       r.issuing_provider_organization_id, r.issued_at, r.valid_from, r.valid_to, r.status,
       r.clinical_summary, r.review_comment, r.reject_reason_code, r.reviewed_by,
       r.reviewed_at, r.submitted_at, r.submitted_by, r.created_at, r.row_version,
       COALESCE(c.sensitivity, 'STANDARD')::text AS case_sensitivity
  FROM health.medical_report r
  LEFT JOIN health.health_case c ON c.tenant_id = r.tenant_id AND c.id = r.case_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR r.issuing_provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   FOR UPDATE OF r;

-- name: ListMedicalReports :many
-- Keyset pagination on (created_at DESC, id DESC); the caller asks for limit+1 rows to
-- learn whether a next page exists.
SELECT r.id, r.person_id, r.case_id, r.reference, r.version_no, r.root_report_id,
       r.supersedes_report_id, r.report_type, r.report_subtype, r.issuing_practitioner_id,
       r.issuing_provider_organization_id, r.issued_at, r.valid_from, r.valid_to, r.status,
       r.clinical_summary, r.review_comment, r.reject_reason_code, r.reviewed_by,
       r.reviewed_at, r.submitted_at, r.submitted_by, r.created_at, r.row_version,
       COALESCE(c.sensitivity, 'STANDARD')::text AS case_sensitivity
  FROM health.medical_report r
  LEFT JOIN health.health_case c ON c.tenant_id = r.tenant_id AND c.id = r.case_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR r.issuing_provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('person_id')::uuid IS NULL OR r.person_id = sqlc.narg('person_id')::uuid)
   AND (sqlc.narg('case_id')::uuid IS NULL OR r.case_id = sqlc.narg('case_id')::uuid)
   AND (sqlc.narg('root_report_id')::uuid IS NULL
        OR r.root_report_id = sqlc.narg('root_report_id')::uuid)
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR r.issuing_provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND (sqlc.narg('status')::text IS NULL OR r.status = sqlc.narg('status')::text)
   AND (sqlc.narg('report_type')::text IS NULL OR r.report_type = sqlc.narg('report_type')::text)
   AND (sqlc.narg('valid_on')::date IS NULL
        OR sqlc.narg('valid_on')::date BETWEEN r.valid_from AND r.valid_to)
   AND (sqlc.narg('after_at')::timestamptz IS NULL
        OR (r.created_at, r.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
 ORDER BY r.created_at DESC, r.id DESC
 LIMIT sqlc.arg('page_size');

-- name: ListMedicalReportChain :many
-- Every version of one chain, oldest first. This is what makes "which version did the claim
-- lean on" readable beside "which version is in force now".
SELECT r.id, r.person_id, r.case_id, r.reference, r.version_no, r.root_report_id,
       r.supersedes_report_id, r.report_type, r.report_subtype, r.issuing_practitioner_id,
       r.issuing_provider_organization_id, r.issued_at, r.valid_from, r.valid_to, r.status,
       r.clinical_summary, r.review_comment, r.reject_reason_code, r.reviewed_by,
       r.reviewed_at, r.submitted_at, r.submitted_by, r.created_at, r.row_version,
       COALESCE(c.sensitivity, 'STANDARD')::text AS case_sensitivity
  FROM health.medical_report r
  LEFT JOIN health.health_case c ON c.tenant_id = r.tenant_id AND c.id = r.case_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.root_report_id = sqlc.arg('root_report_id')
 ORDER BY r.version_no;

-- name: UpdateMedicalReportDraft :execrows
-- The header of a draft. The predicate carries the whole precondition — this report, still
-- a draft, still at the version the caller read — so a report somebody has meanwhile
-- submitted or approved is not edited and the caller is told nothing changed.
UPDATE health.medical_report
   SET case_id                          = sqlc.narg('case_id'),
       report_type                      = sqlc.arg('report_type'),
       report_subtype                   = sqlc.narg('report_subtype'),
       issuing_practitioner_id          = sqlc.narg('issuing_practitioner_id'),
       issuing_provider_organization_id = sqlc.narg('issuing_provider_organization_id'),
       issued_at                        = sqlc.arg('issued_at'),
       valid_from                       = sqlc.arg('valid_from'),
       valid_to                         = sqlc.arg('valid_to'),
       clinical_summary                 = sqlc.narg('clinical_summary'),
       updated_by                       = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT'
   AND row_version = sqlc.arg('expected_row_version');

-- name: TouchMedicalReport :execrows
-- Bumps the row version of a draft whose service lines were replaced. The lines are part of
-- what the report says, so replacing them has to move the ETag; a caller holding the old
-- one is holding a report that no longer says what it said.
UPDATE health.medical_report
   SET updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT'
   AND row_version = sqlc.arg('expected_row_version');

-- name: MarkMedicalReportSubmitted :execrows
UPDATE health.medical_report
   SET status       = 'SUBMITTED',
       submitted_at = sqlc.arg('submitted_at'),
       submitted_by = sqlc.narg('actor_id'),
       updated_by   = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT'
   AND row_version = sqlc.arg('expected_row_version');

-- name: MarkMedicalReportUnderReview :execrows
-- No expected row version: this transition is also raised by the worklist when a reviewer
-- claims the item, and a claim has read the work item rather than the report. The status
-- predicate is the whole precondition, so claiming twice moves the report once.
UPDATE health.medical_report
   SET status     = 'UNDER_REVIEW',
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'SUBMITTED';

-- name: MarkMedicalReportApproved :execrows
UPDATE health.medical_report
   SET status         = 'APPROVED',
       review_comment = sqlc.narg('review_comment'),
       reviewed_at    = sqlc.arg('reviewed_at'),
       reviewed_by    = sqlc.arg('reviewed_by'),
       updated_by     = sqlc.arg('reviewed_by')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'UNDER_REVIEW'
   AND row_version = sqlc.arg('expected_row_version');

-- name: MarkMedicalReportRejected :execrows
UPDATE health.medical_report
   SET status             = 'REJECTED',
       review_comment     = sqlc.narg('review_comment'),
       reject_reason_code = sqlc.arg('reject_reason_code'),
       reviewed_at        = sqlc.arg('reviewed_at'),
       reviewed_by        = sqlc.arg('reviewed_by'),
       updated_by         = sqlc.arg('reviewed_by')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'UNDER_REVIEW'
   AND row_version = sqlc.arg('expected_row_version');

-- name: MarkMedicalReportCancelled :execrows
UPDATE health.medical_report
   SET status     = 'CANCELLED',
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('DRAFT','SUBMITTED')
   AND row_version = sqlc.arg('expected_row_version');

-- name: SupersedeMedicalReport :execrows
-- The one update an approved report accepts, and it moves nothing but the status: the
-- reviewer, the moment, the comment, the summary and the lines stay exactly as they were
-- decided. It runs in the same transaction as the successor's approval, so the moment when
-- a chain has two approved versions — or none — never exists for any reader.
UPDATE health.medical_report
   SET status = 'SUPERSEDED'
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'APPROVED';

-- name: ListExpirableMedicalReports :many
-- Approved reports whose validity ran out before today. The sweep only looks at APPROVED
-- rows, so a second pass finds nothing the first one finished — which is the whole of why
-- the job is idempotent.
SELECT id
  FROM health.medical_report
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND status = 'APPROVED'
   AND valid_to < sqlc.arg('as_of')::date
 ORDER BY valid_to
 LIMIT sqlc.arg('page_size');

-- name: MarkMedicalReportExpired :execrows
UPDATE health.medical_report
   SET status = 'EXPIRED'
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'APPROVED';

-- name: ListMedicalReportServices :many
-- The lines, with the catalogue code each one names. The amounts are exact decimals as text
-- the whole way: a float here would make a limit two systems disagree about.
SELECT s.id, s.report_id, s.service_definition_id, d.code AS service_code, d.name AS service_name,
       COALESCE(s.covered_quantity::text, '')::text AS covered_quantity,
       COALESCE(s.covered_amount::text, '')::text   AS covered_amount,
       s.currency_code, s.notes, s.row_version
  FROM health.medical_report_service s
  JOIN catalog.service_definition d
    ON d.tenant_id = s.tenant_id AND d.id = s.service_definition_id
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.report_id = sqlc.arg('report_id')
 ORDER BY d.code;

-- name: DeleteMedicalReportServices :execrows
DELETE FROM health.medical_report_service
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND report_id = sqlc.arg('report_id');

-- name: CreateMedicalReportService :batchexec
INSERT INTO health.medical_report_service (
    tenant_id, report_id, service_definition_id, covered_quantity, covered_amount,
    currency_code, notes, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('report_id'), sqlc.arg('service_definition_id'),
        sqlc.narg('covered_quantity')::text::numeric,
        sqlc.narg('covered_amount')::text::numeric,
        sqlc.narg('currency_code'), sqlc.narg('notes'), sqlc.narg('actor_id'),
        sqlc.narg('actor_id'));

-- name: CountMedicalReportServices :one
SELECT count(*) FROM health.medical_report_service
 WHERE tenant_id = sqlc.arg('tenant_id') AND report_id = sqlc.arg('report_id');

-- name: GetMedicalReportCoverage :one
-- Everything `ReportCoverage` needs about one report and one service, in one statement: the
-- status, the window and the line — or no line, which is an answer too. Nothing is decided
-- here; the application service turns the three facts into a usable-or-not with a reason
-- code, because a caller that is told "no" deserves to be told which of the three it was.
SELECT r.id, r.reference, r.version_no, r.root_report_id, r.person_id, r.status,
       r.valid_from, r.valid_to,
       s.id AS service_line_id,
       COALESCE(s.covered_quantity::text, '')::text AS covered_quantity,
       COALESCE(s.covered_amount::text, '')::text   AS covered_amount,
       s.currency_code
  FROM health.medical_report r
  LEFT JOIN health.medical_report_service s
    ON s.tenant_id = r.tenant_id
   AND s.report_id = r.id
   AND s.service_definition_id = sqlc.arg('service_definition_id')
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.id = sqlc.arg('report_id');

-- name: CreateMedicalReportUsage :one
INSERT INTO health.medical_report_usage (tenant_id, report_id, used_by_type, used_by_id,
                                         used_at, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('report_id'), sqlc.arg('used_by_type'),
        sqlc.arg('used_by_id'), sqlc.arg('used_at'), sqlc.narg('actor_id'))
RETURNING id, report_id, used_by_type, used_by_id, used_at;

-- name: ListMedicalReportUsages :many
SELECT u.id, u.report_id, u.used_by_type, u.used_by_id, u.used_at
  FROM health.medical_report_usage u
 WHERE u.tenant_id = sqlc.arg('tenant_id')
   AND u.report_id = sqlc.arg('report_id')
   AND (sqlc.narg('after_at')::timestamptz IS NULL
        OR (u.used_at, u.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
 ORDER BY u.used_at DESC, u.id DESC
 LIMIT sqlc.arg('page_size');

-- name: CountCleanMedicalReportDocuments :one
-- The submit gate's second half: is the report file actually there, and did the scanner
-- clear it. A link to an object still in quarantine is not a document a reviewer can open,
-- and a report submitted without one is a reviewer asked to decide on nothing.
--
-- The type accepted is the report's own `report_type` or the generic MEDICAL_REPORT code:
-- a provider that tags the file with the report's type has said which report it is, and one
-- that tags it MEDICAL_REPORT has said it is the report. Neither is a guess about content.
SELECT count(*)
  FROM document.link l
  JOIN document.object o ON o.tenant_id = l.tenant_id AND o.id = l.object_id
 WHERE l.tenant_id = sqlc.arg('tenant_id')
   AND l.aggregate_type = 'MEDICAL_REPORT'
   AND l.aggregate_id = sqlc.arg('report_id')
   AND l.document_type_code IN (sqlc.arg('report_type'), 'MEDICAL_REPORT')
   AND o.scan_status = 'CLEAN'
   AND o.purged_at IS NULL;

-- name: ListMedicalReportDocuments :many
-- The report's attachments. They are clinical — a document list naming "PSIKIYATRI_RAPORU"
-- is a diagnosis on a filename — so they are served only in the clinical projection, and the
-- download itself is guarded a second time by the link's own required_permission.
SELECT l.id, l.object_id, l.document_type_code, l.purpose, l.required_permission,
       o.original_filename, o.content_type, o.scan_status, o.classification, l.created_at
  FROM document.link l
  JOIN document.object o ON o.tenant_id = l.tenant_id AND o.id = l.object_id
 WHERE l.tenant_id = sqlc.arg('tenant_id')
   AND l.aggregate_type = 'MEDICAL_REPORT'
   AND l.aggregate_id = sqlc.arg('report_id')
 ORDER BY l.created_at DESC, l.id DESC;

-- name: GetMedicalReportServiceDefinition :one
-- A service line names a definition of this tenant's catalogue and nothing else.
SELECT d.id, d.code, d.name, d.active
  FROM catalog.service_definition d
 WHERE d.tenant_id = sqlc.arg('tenant_id') AND d.id = sqlc.arg('id');

-- name: GetMedicalReportPerson :one
-- The person a report is written for, and the case it hangs off, have to be the same person.
SELECT c.id, c.person_id, c.status, c.sensitivity
  FROM health.health_case c
 WHERE c.tenant_id = sqlc.arg('tenant_id') AND c.id = sqlc.arg('id');
