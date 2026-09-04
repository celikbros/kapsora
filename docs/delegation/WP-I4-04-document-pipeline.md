# WP-I4-04 · Documents: upload, quarantine, scan, secure storage and legal hold

| Field                      | Value                                                                                                                                                      |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M4 (plan increment I4)                                                                                                                                     |
| Size                       | L                                                                                                                                                          |
| Depends on                 | M1 (object storage and ClamAV run natively, WP-I1-06)                                                                                                      |
| Runs in parallel with      | WP-I4-01                                                                                                                                                   |
| Migration numbers assigned | `000028_document_pipeline.up.sql`                                                                                                                          |
| OpenAPI operations owned   | `createUpload`, `completeUpload`, `getDocument`, `listDocuments`, `downloadDocument`, `linkDocument`, `unlinkDocument`, `putLegalHold`, `releaseLegalHold` |
| Read first                 | v1.2 9.15, 19.x, 16.9; ADR-021 (no containers: ClamAV and MinIO are native services); `docs/runbooks/local-native-environment.md`; ADR-015                 |

## 1. Goal

A missing document is the most common reason a request stalls, so uploading one has to be
easy — and a file from outside is the most likely way malware gets in, so it has to be
safe. The whole package is the resolution of those two facts: the file goes to a
quarantine bucket first, is scanned there, and only a clean file is moved to the secure
bucket where the rest of the product can reach it.

**A file is never stored in the database** (v1.2 39.14). The database stores where it is,
what it is, who may see it and what the scanner said.

## 2. Scope

### 2.1 Schema (migration 000028, new `document` schema)

`document.object`: id, tenant_id, `object_key` text, `bucket` text, `classification`
(`INTERNAL`,`CONFIDENTIAL`,`PERSONAL`,`HEALTH`), `original_filename`, `content_type`,
`byte_size` bigint, `sha256 bytea` (32), `scan_status`
(`PENDING`,`SCANNING`,`CLEAN`,`INFECTED`,`FAILED`), `uploaded_by`, `uploaded_at`,
row_version. Unique `(tenant_id, sha256)` where scan_status = 'CLEAN' — the same file
uploaded twice is one object, which also stops a member re-uploading a document being
counted as a new one.

`document.version`: id, tenant_id, object_id, `version_no`, `byte_size`, `content_type`,
`encryption_key_ref` text, `created_at`. Append-only, unique `(tenant_id, object_id, version_no)`.

`document.link`: id, tenant_id, object_id, `aggregate_type`, `aggregate_id`,
`document_type_code`, `purpose`, `required_permission` text NULL, created_by, created_at.
Index on `(tenant_id, aggregate_type, aggregate_id)`. A link is how a request says "this
is the invoice"; the same object may be linked to several aggregates.

`document.scan_result`: id, tenant_id, object_id, version_id, `engine`,
`signature_version`, `outcome` (`CLEAN`,`INFECTED`,`ERROR`), `finding` text NULL,
`scanned_at`. Append-only, unique `(tenant_id, version_id, engine, scanned_at)`.

`document.legal_hold`: id, tenant_id, object_id NULL, person_id NULL, aggregate_type and
id NULL, `reason`, `placed_by`, `placed_at`, `released_at`, `released_by`. Partial unique
index on the active holds. **An object under legal hold is never deleted**, by retention
or by anything else.

RLS, touch triggers, composite keys throughout.

### 2.2 The pipeline

1. `createUpload` (permission `document.upload`) reserves an object row with
   `scan_status = 'PENDING'` and returns a **presigned PUT URL into the quarantine
   bucket** plus the object id. The URL is short-lived (default 15 minutes) and is the
   only way a byte reaches storage: the API never proxies the file body.
2. The client uploads directly to quarantine.
3. `completeUpload` records the size and the digest the client claims, sets
   `scan_status = 'SCANNING'` and enqueues a scan through the outbox.
4. The **worker** streams the object from quarantine to ClamAV over TCP (clamd, native per
   ADR-021), writes a `scan_result`, and then:
   - CLEAN → server-side copy to the secure bucket, delete from quarantine, set CLEAN.
   - INFECTED → **delete from quarantine, never copy**, set INFECTED, write an audit event
     of category SECURITY, and notify the uploader through WP-I4-05.
   - The scanner unreachable → set FAILED and retry with backoff; a file that cannot be
     scanned is never promoted.
5. `downloadDocument` (permission from the link, or `document.read`) answers a
   short-lived presigned GET into the **secure** bucket, and only for `scan_status = 'CLEAN'`.
   Every download writes an `audit.access_event` with the object's classification;
   a HEALTH-classified document is a separate audit event (v1.2 11.10).

### 2.3 What must not be possible

- A file that has not been scanned, or whose scan failed, has no download URL. The
  endpoint answers 409 `DOCUMENT_NOT_SCANNED`, not an empty body.
- An infected file has no bytes anywhere: it is deleted from quarantine and never written
  to secure. The row survives so the incident is on record.
- The API never receives file bytes, so no size limit or content type check is a defence
  against a large upload; the presigned URL carries the size limit and the bucket policy.
- An object under legal hold is never deleted; the retention job must skip it and there is
  a test that it does.

## 3. Tests required

- **The EICAR test file never reaches the secure bucket.** Upload it, run the scan, and
  assert: the object is INFECTED, the quarantine key is gone, the secure bucket has no
  object with that key, the download endpoint refuses, and a SECURITY audit event exists.
  This is v1.2 Phase 5's own acceptance criterion and the single most important test here.
- A clean file: quarantine → scan → secure, download works, and the quarantine copy is gone.
- A scan that errors leaves the object FAILED and downloadable by nobody; a retry that
  succeeds promotes it.
- The same bytes uploaded twice produce one CLEAN object.
- Download of a HEALTH-classified document writes an access event with that classification
  and the reason; download without the link's required permission answers 403.
- Legal hold blocks deletion; releasing it allows it.
- No file bytes in any table (assert the column types).

## 4. Acceptance criteria

- [ ] Malware never reaches the secure bucket and leaves no bytes behind.
- [ ] Nothing is downloadable until it is scanned clean.
- [ ] Every download is audited with the document's classification.
- [ ] A document under legal hold survives retention.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean; schema version 28.
