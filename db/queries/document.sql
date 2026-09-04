-- Document object, version, link, scan result and legal hold queries (WP-I4-04,
-- v1.2 9.15, 19.x, 39.14).
--
-- Four properties shape this file.
--
-- **No statement here reads or writes a file body.** There is none to read: the object
-- store holds the bytes and these rows hold the key they are under. The only bytea that
-- appears at all is a 32-byte digest.
--
-- The boundary is the same shape the service request and authorization queries apply, over
-- this package's own resource: `scope_ids` is a nullable uuid[] of tenant organization ids,
-- NULL means the caller sees every document of the tenant and a non-null array binds it to
-- the documents those organizations own plus the tenant's own. It is applied in every read
-- including the single-row ones, so a document outside it is answered 404 rather than 403 —
-- that such a document exists at all is somebody else's business.
--
-- The scan status is never a parameter. PENDING is the insert default, MarkScanning moves
-- to SCANNING, and CLEAN, INFECTED and FAILED each have a statement of their own whose
-- predicate carries the status it may move from. There is deliberately no way for a caller
-- to hand this file a status.
--
-- PromoteDocumentObject is the only statement that writes bucket = 'secure', and its
-- predicate carries the CLEAN verdict that earns it.

-- name: CreateDocumentObject :one
-- The id and the key are given rather than generated: the key contains the id, so they are
-- one decision made in one place.
INSERT INTO document.object (
    id, tenant_id, object_key, classification, original_filename, content_type,
    owner_tenant_organization_id, uploaded_by, created_by, updated_by)
VALUES (sqlc.arg('id'), sqlc.arg('tenant_id'), sqlc.arg('object_key'),
        sqlc.arg('classification'), sqlc.arg('original_filename'), sqlc.arg('content_type'),
        sqlc.narg('owner_tenant_organization_id'), sqlc.narg('actor_id'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, object_key, bucket, classification, original_filename, content_type,
          byte_size, sha256, scan_status, owner_tenant_organization_id,
          duplicate_of_object_id, uploaded_by, uploaded_at, purged_at, created_at, row_version;

-- name: GetDocumentObject :one
SELECT o.id, o.object_key, o.bucket, o.classification, o.original_filename, o.content_type,
       o.byte_size, o.sha256, o.scan_status, o.owner_tenant_organization_id,
       o.duplicate_of_object_id, o.uploaded_by, o.uploaded_at, o.purged_at, o.created_at,
       o.row_version
  FROM document.object o
 WHERE o.tenant_id = sqlc.arg('tenant_id')
   AND o.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR o.owner_tenant_organization_id IS NULL
        OR o.owner_tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: LockDocumentObject :one
-- The worker's read. It carries no scope: the worker acts for the system rather than for a
-- person, and a scan skipped because nobody happened to hold the document's organization
-- would leave a file unscanned.
SELECT o.id, o.object_key, o.bucket, o.classification, o.original_filename, o.content_type,
       o.byte_size, o.sha256, o.scan_status, o.owner_tenant_organization_id,
       o.duplicate_of_object_id, o.uploaded_by, o.uploaded_at, o.purged_at, o.created_at,
       o.row_version
  FROM document.object o
 WHERE o.tenant_id = sqlc.arg('tenant_id')
   AND o.id = sqlc.arg('id')
   FOR UPDATE;

-- name: ListDocumentObjects :many
-- Keyset pagination on (created_at DESC, id DESC); the caller asks for limit+1 rows to
-- learn whether a next page exists. Filtering by a record is an EXISTS over the links,
-- because a document may belong to several records and a join would return it once per one.
SELECT o.id, o.object_key, o.bucket, o.classification, o.original_filename, o.content_type,
       o.byte_size, o.sha256, o.scan_status, o.owner_tenant_organization_id,
       o.duplicate_of_object_id, o.uploaded_by, o.uploaded_at, o.purged_at, o.created_at,
       o.row_version
  FROM document.object o
 WHERE o.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR o.owner_tenant_organization_id IS NULL
        OR o.owner_tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('scan_status')::text IS NULL OR o.scan_status = sqlc.narg('scan_status')::text)
   AND (sqlc.narg('classification')::text IS NULL
        OR o.classification = sqlc.narg('classification')::text)
   AND (sqlc.narg('aggregate_type')::text IS NULL OR EXISTS (
            SELECT 1 FROM document.link l
             WHERE l.tenant_id = o.tenant_id AND l.object_id = o.id
               AND l.aggregate_type = sqlc.narg('aggregate_type')::text
               AND (sqlc.narg('aggregate_id')::uuid IS NULL
                    OR l.aggregate_id = sqlc.narg('aggregate_id')::uuid)))
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (o.created_at, o.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY o.created_at DESC, o.id DESC
 LIMIT sqlc.arg('page_size');

-- name: FindCleanDocumentByDigest :one
-- The canonical stored copy of one digest. Duplicates are excluded: they are pointers at
-- this row, and returning one would send the caller to a pointer instead of the file.
SELECT o.id, o.object_key, o.bucket, o.classification, o.original_filename, o.content_type,
       o.byte_size, o.sha256, o.scan_status, o.owner_tenant_organization_id,
       o.duplicate_of_object_id, o.uploaded_by, o.uploaded_at, o.purged_at, o.created_at,
       o.row_version
  FROM document.object o
 WHERE o.tenant_id = sqlc.arg('tenant_id')
   AND o.sha256 = sqlc.arg('sha256')
   AND o.scan_status = 'CLEAN'
   AND o.duplicate_of_object_id IS NULL
   AND o.purged_at IS NULL
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR o.owner_tenant_organization_id IS NULL
        OR o.owner_tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
 LIMIT 1;

-- name: MarkDocumentObjectScanning :execrows
-- What the client claims about bytes the API never saw. The predicate carries PENDING, so
-- a second completeUpload changes nothing and the caller is told so.
UPDATE document.object
   SET byte_size   = sqlc.arg('byte_size'),
       sha256      = sqlc.arg('sha256'),
       scan_status = 'SCANNING'
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND scan_status = 'PENDING';

-- name: PromoteDocumentObject :execrows
-- The only statement in the schema that writes bucket = 'secure'. Its predicate carries the
-- statuses a verdict may arrive from, and the digest it writes is the one the worker
-- computed from the bytes it scanned.
UPDATE document.object
   SET scan_status = 'CLEAN',
       bucket      = 'secure',
       object_key  = sqlc.arg('object_key'),
       sha256      = sqlc.arg('sha256'),
       byte_size   = sqlc.arg('byte_size')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND scan_status IN ('SCANNING','FAILED');

-- name: MarkDocumentObjectDuplicate :execrows
-- The losing side of a race for one digest. It keeps no bytes of its own: it carries the
-- canonical key, so there is one stored file however many rows name it.
UPDATE document.object
   SET scan_status            = 'CLEAN',
       bucket                 = 'secure',
       object_key             = sqlc.arg('object_key'),
       duplicate_of_object_id = sqlc.arg('duplicate_of_object_id'),
       sha256                 = sqlc.arg('sha256'),
       byte_size              = sqlc.arg('byte_size')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND scan_status IN ('SCANNING','FAILED');

-- name: MarkDocumentObjectInfected :execrows
-- The bucket stays quarantine and no statement will ever move it: an INFECTED row is a
-- record of an incident, not a file.
UPDATE document.object
   SET scan_status = 'INFECTED',
       sha256      = sqlc.arg('sha256'),
       byte_size   = sqlc.arg('byte_size')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND scan_status IN ('SCANNING','FAILED');

-- name: MarkDocumentObjectFailed :execrows
UPDATE document.object
   SET scan_status = 'FAILED'
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND scan_status IN ('SCANNING','FAILED');

-- name: MarkDocumentObjectPurged :execrows
UPDATE document.object
   SET purged_at = sqlc.arg('purged_at')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND purged_at IS NULL;

-- name: ListPurgeableDocuments :many
-- Stored documents older than the cutoff whose bytes are still there. The legal hold is
-- deliberately not part of the predicate: skipping is a decision the sweep makes and
-- reports, not one a WHERE clause makes silently.
SELECT o.id, o.object_key, o.bucket, o.classification, o.original_filename, o.content_type,
       o.byte_size, o.sha256, o.scan_status, o.owner_tenant_organization_id,
       o.duplicate_of_object_id, o.uploaded_by, o.uploaded_at, o.purged_at, o.created_at,
       o.row_version
  FROM document.object o
 WHERE o.tenant_id = sqlc.arg('tenant_id')
   AND o.scan_status = 'CLEAN'
   AND o.purged_at IS NULL
   AND o.uploaded_at < sqlc.arg('before')
 ORDER BY o.uploaded_at
 LIMIT sqlc.arg('page_size');

-- name: CreateDocumentVersion :one
INSERT INTO document.version (
    tenant_id, object_id, version_no, byte_size, content_type, encryption_key_ref, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('object_id'), sqlc.arg('version_no'),
        sqlc.arg('byte_size'), sqlc.arg('content_type'), sqlc.narg('encryption_key_ref'),
        sqlc.narg('actor_id'))
RETURNING id, object_id, version_no, byte_size, content_type, encryption_key_ref, created_at;

-- name: GetLatestDocumentVersion :one
SELECT v.id, v.object_id, v.version_no, v.byte_size, v.content_type, v.encryption_key_ref,
       v.created_at
  FROM document.version v
 WHERE v.tenant_id = sqlc.arg('tenant_id')
   AND v.object_id = sqlc.arg('object_id')
 ORDER BY v.version_no DESC
 LIMIT 1;

-- name: CreateDocumentScanResult :one
-- scanned_at is part of the unique key, so two attempts on one version are two rows rather
-- than a conflict: a file that failed and then passed keeps both verdicts.
INSERT INTO document.scan_result (
    tenant_id, object_id, version_id, engine, signature_version, outcome, finding)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('object_id'), sqlc.arg('version_id'),
        sqlc.arg('engine'), sqlc.narg('signature_version'), sqlc.arg('outcome'),
        sqlc.narg('finding'))
RETURNING id, object_id, version_id, engine, signature_version, outcome, finding, scanned_at;

-- name: ListDocumentScanResults :many
SELECT r.id, r.object_id, r.version_id, r.engine, r.signature_version, r.outcome,
       r.finding, r.scanned_at
  FROM document.scan_result r
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.object_id = sqlc.arg('object_id')
 ORDER BY r.scanned_at DESC, r.id DESC;

-- name: CreateDocumentLink :one
INSERT INTO document.link (
    tenant_id, object_id, aggregate_type, aggregate_id, document_type_code, purpose,
    required_permission, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('object_id'), sqlc.arg('aggregate_type'),
        sqlc.arg('aggregate_id'), sqlc.arg('document_type_code'), sqlc.narg('purpose'),
        sqlc.narg('required_permission'), sqlc.narg('actor_id'))
RETURNING id, object_id, aggregate_type, aggregate_id, document_type_code, purpose,
          required_permission, created_by, created_at;

-- name: ListDocumentLinks :many
SELECT l.id, l.object_id, l.aggregate_type, l.aggregate_id, l.document_type_code, l.purpose,
       l.required_permission, l.created_by, l.created_at
  FROM document.link l
 WHERE l.tenant_id = sqlc.arg('tenant_id')
   AND l.object_id = sqlc.arg('object_id')
 ORDER BY l.created_at, l.id;

-- name: DeleteDocumentLink :execrows
DELETE FROM document.link
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND object_id = sqlc.arg('object_id')
   AND id = sqlc.arg('id');

-- name: CreateDocumentLegalHold :one
INSERT INTO document.legal_hold (
    tenant_id, object_id, person_id, aggregate_type, aggregate_id, reason, placed_by)
VALUES (sqlc.arg('tenant_id'), sqlc.narg('object_id'), sqlc.narg('person_id'),
        sqlc.narg('aggregate_type'), sqlc.narg('aggregate_id'), sqlc.arg('reason'),
        sqlc.narg('actor_id'))
RETURNING id, object_id, person_id, aggregate_type, aggregate_id, reason, placed_by,
          placed_at, released_at, released_by, row_version;

-- name: GetDocumentLegalHold :one
SELECT h.id, h.object_id, h.person_id, h.aggregate_type, h.aggregate_id, h.reason,
       h.placed_by, h.placed_at, h.released_at, h.released_by, h.row_version
  FROM document.legal_hold h
 WHERE h.tenant_id = sqlc.arg('tenant_id')
   AND h.id = sqlc.arg('id');

-- name: ReleaseDocumentLegalHold :execrows
-- The expected row_version is part of the predicate, so a stale If-Match releases nothing,
-- and released_at IS NULL means a second release is refused rather than overwriting who
-- lifted the hold first.
UPDATE document.legal_hold
   SET released_at = sqlc.arg('released_at'),
       released_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND released_at IS NULL
   AND row_version = sqlc.arg('row_version');

-- name: DocumentHasActiveLegalHold :one
-- Whether anything holds this document: a hold on the document itself, or one on a record
-- it is linked to. Both count, because a hold over a case is a hold over the case's
-- documents.
SELECT EXISTS (
    SELECT 1
      FROM document.legal_hold h
     WHERE h.tenant_id = sqlc.arg('tenant_id')
       AND h.released_at IS NULL
       AND (h.object_id = sqlc.arg('object_id')
            OR (h.aggregate_id IS NOT NULL AND EXISTS (
                    SELECT 1 FROM document.link l
                     WHERE l.tenant_id = h.tenant_id
                       AND l.object_id = sqlc.arg('object_id')
                       AND l.aggregate_type = h.aggregate_type
                       AND l.aggregate_id = h.aggregate_id)))
) AS held;
