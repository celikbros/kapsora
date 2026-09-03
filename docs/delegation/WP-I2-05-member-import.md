# WP-I2-05 · Member import: staging, validation, matching, review queue, idempotent apply

| Field | Value |
|---|---|
| Milestone | M2 (plan increment I2) |
| Size | L |
| Depends on | WP-I2-01, WP-I2-02 |
| Runs in parallel with | WP-I2-04 |
| Migration numbers assigned | `000017_party_import_staging.up.sql` |
| OpenAPI operations owned | `createMemberImport`, `getMemberImport`, `listMemberImportRows`, `reviewMemberImportRow`, `applyMemberImport`, `cancelMemberImport` |
| Read first | v1.2 10.1 (bulk import), 11.2, 16.5; WP-I2-01 identifier rules; `internal/platform/outbox` and scheduler job runner |

## 1. Goal

A sponsor's member file (HR export) becomes persons, memberships, relationships and
enrollments without touching live tables until every row is validated and matched, with
conflicts parked for a human, and with a guarantee that re-processing the same file, or a
newer version of it, never creates duplicates.

## 2. Scope

### 2.1 Migration 000017

- `party.import_batch`: id, tenant_id, sponsor_tenant_organization_id, plan_id (optional
  default enrollment plan), source_system text, source_version text, file_name, file_sha256
  bytea, format (`CSV_V1`), row_count, status (`RECEIVED`, `VALIDATING`, `REVIEW`,
  `APPLYING`, `APPLIED`, `FAILED`, `CANCELLED`), counters (valid/invalid/matched/created/
  skipped), created_by, applied_at; unique `(tenant_id, source_system, source_version, file_sha256)`.
- `party.import_row`: id, tenant_id, batch_id (composite FK), row_no, source_record_id,
  payload jsonb (identifiers removed, see 2.3), identifier_hash bytea per identifier
  (array of `{type, scope_key, hash, masked}`), status (`PENDING`, `VALID`, `INVALID`,
  `MATCHED`, `CONFLICT`, `APPLIED`, `SKIPPED`), matched_person_id, errors jsonb,
  decision (`CREATE`, `UPDATE`, `SKIP`, null), decided_by; unique
  `(tenant_id, batch_id, row_no)`; index on `(tenant_id, batch_id, status)`. RLS.

### 2.2 Format `CSV_V1`

UTF-8, header row, `;` or `,` detected; columns: `source_record_id`, `first_name`,
`middle_name`, `last_name`, `birth_date` (ISO), `sex_at_birth`, `tckn`, `member_no`,
`employee_no`, `membership_type` (PRINCIPAL/DEPENDANT), `principal_member_no`,
`relationship` (SPOUSE/CHILD_OF/...), `valid_from`, `valid_to`, `plan_code`. Unknown
columns rejected; max 50 000 rows; max 20 MB.

### 2.3 Pipeline

1. `POST /api/v1/imports/members` (`import.execute`, step-up, multipart file +
   `sponsorOrganizationId`, `sourceSystem`, `sourceVersion`, `planId?`): hash the file,
   reject a duplicate `(source_system, source_version, sha256)` with 409
   `IMPORT_DUPLICATE`; parse and stage rows synchronously up to 5 000 rows, beyond that
   enqueue a worker job (`import.stage`) and answer 202. Identifiers are normalised, blind
   indexed and masked on the way in; **the plaintext is not stored in the staging row**
   (the encrypted value needed at apply time is stored in a separate
   `identifier_cipher` column encrypted with the tenant key).
2. Validation per row (worker job `import.validate`): required fields, TCKN checksum,
   dates, principal reference resolvable inside the batch or in live memberships,
   plan_code exists and published as of `valid_from`.
3. Matching: by blind index (TCKN within tenant, MEMBER_NO within sponsor); match → `MATCHED`
   with a proposed `UPDATE` (diff of names/dates) or `SKIP` when nothing changes; no
   match → `VALID` with `CREATE`; ambiguous (two persons) → `CONFLICT`.
4. Review: `GET /imports/members/{id}/rows?status=` (paged) and
   `POST /imports/members/{id}/rows/{rowId}/review` `{decision, matchedPersonId?}`.
   Batch status becomes `REVIEW` when any row is `CONFLICT` or `INVALID`; the operator
   may apply with invalid rows skipped.
5. `POST /imports/members/{id}/apply` (`import.execute`, step-up): worker job applies rows
   in transactional chunks of 500 in `row_no` order: create/update person, identifiers,
   memberships (principal rows before dependants), relationships, enrollment when
   `plan_code`/`planId` present; every write carries `source_system`/`source_record_id`
   (memberships/enrollments have the columns; persons record it in `party.person_identifier`
   of type `SOURCE_RECORD`? — no: add `source_system`, `source_record_id` to
   `party.person` in migration 000017). Re-applying the same batch is a no-op; a new
   `source_version` of the same file updates instead of duplicating.
6. Report: `GET /imports/members/{id}` returns counters, per-status counts and the
   reconciliation summary (rows in file vs created/updated/skipped/conflict).

## 3. Tests required

- Parser: delimiters, BOM, quoted fields, invalid rows with line numbers.
- dbtest: 1 000-row synthetic file (generated TCKNs) staged and applied; second apply of
  the same file → zero changes; new version with 10 changed rows → 10 updates, 0 creates;
  a dependant referencing a principal in the same file; conflict row parked and resolved;
  no plaintext identifier in `import_row.payload`, logs or audit; RLS on both tables.
- HTTP: multipart limits, duplicate batch 409, step-up.

## 4. Acceptance criteria

- [ ] Same `source_system + source_record_id + source_version` re-processed → no duplicate (test).
- [ ] Staging never contains plaintext identifiers.
- [ ] Conflicts go to review; apply is transactional per chunk and resumable.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean.
