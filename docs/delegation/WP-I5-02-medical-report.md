# WP-I5-02 · Medical report: submit, medical review, approved scope, versions

| Field                      | Value                                                                                                                                                                                              |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M5 (plan increment I5)                                                                                                                                                                             |
| Size                       | M                                                                                                                                                                                                  |
| Depends on                 | WP-I5-01 (case, clinical visibility), WP-I4-04 (documents), WP-I4-03 (work queues)                                                                                                                 |
| Runs in parallel with      | WP-I5-03                                                                                                                                                                                           |
| Migration numbers assigned | `000032_medical_report.up.sql`                                                                                                                                                                     |
| OpenAPI operations owned   | `listMedicalReports`, `createMedicalReport`, `getMedicalReport`, `patchMedicalReportDraft`, `putMedicalReportServices`, `submitMedicalReport`, `startMedicalReview`, `approveMedicalReport`, `rejectMedicalReport`, `cancelMedicalReport`, `listMedicalReportUsages` |
| Read first                 | v1.2 10.5, 12.4, 11.10; WP-I5-01; WP-I4-01's version model (a decided version is frozen; a correction is a new version); WP-I4-03 §2.4 approval policy lookup                                     |

## 1. Goal

A treatment report is a doctor saying "this person needs this, for this long". Once a
medical reviewer has approved it, claims and authorizations lean on it, so it has to be
what the reviewer saw: **an approved report is never edited; a change is a new version**,
and every claim that used it can say which version it used.

## 2. Scope

### 2.1 Schema (migration 000032)

`health.medical_report`: id, tenant_id, person_id, case_id NULL, `reference` (unique per
tenant, minted like a request reference), `report_type` and `report_subtype` (code system
values), `issuing_practitioner_id`, `issuing_provider_organization_id`, `issued_at` date,
`valid_from`, `valid_to` CHECK ordering, `version_no` int NOT NULL DEFAULT 1,
`supersedes_report_id` NULL (self composite FK, the previous version), `status`
(`DRAFT`,`SUBMITTED`,`UNDER_REVIEW`,`APPROVED`,`REJECTED`,`CANCELLED`,`EXPIRED`),
`clinical_summary` text NULL (HEALTH; clinical projection only), `review_comment`,
`reject_reason_code`, `reviewed_by`, `reviewed_at`, `submitted_at`, `submitted_by`,
row_version. Unique `(tenant_id, reference, version_no)`; partial unique: at most one
`APPROVED` version per `supersedes` chain root — implement as `root_report_id` NOT NULL
(= own id for version 1) with unique `(tenant_id, root_report_id) WHERE status = 'APPROVED'`.

`health.medical_report_service`: id, tenant_id, report_id, `service_definition_id`
(composite FK into catalog), `covered_quantity numeric(20,6)` NULL, `covered_amount
numeric(20,6)` NULL, `currency_code`, `notes`. Unique `(tenant_id, report_id,
service_definition_id)`.

`health.medical_report_usage`: id, tenant_id, report_id, `used_by_type`
(`SERVICE_REQUEST`,`AUTHORIZATION`,`CLAIM`), `used_by_id`, `used_at`. Append-only. The
trace v1.2 10.5 step 6 asks for.

Documents (the report file, supporting tests) attach through WP-I4-04 links with
`aggregate_type = 'MEDICAL_REPORT'`; the report's `classification` is HEALTH and the link's
`required_permission` is `health.clinical.read`.

### 2.2 Lifecycle, as commands

`DRAFT → SUBMITTED` (provider, `health.medical_report.manage`; refuses without at least
one service line and one linked CLEAN document of the report type) → `UNDER_REVIEW`
(`startMedicalReview`, `health.medical_report.review`; also raised automatically when the
work item is claimed) → `APPROVED` / `REJECTED` (reviewer, reason on reject, comment
optional) / `CANCELLED` (provider, before review). `EXPIRED` is a scheduler job over
`valid_to` (idempotent, tested). No status is writable.

Submitting raises a work item in the medical review queue (WP-I4-03 port), with the
report's reference as the title and no clinical word in it.

### 2.3 Versions

A rejected or approved report is frozen: `patchMedicalReportDraft` and
`putMedicalReportServices` answer 409 `MEDICAL_REPORT_IMMUTABLE`. A correction is
`createMedicalReport` with `supersedesReportId`, which copies the lines into version n+1
as a DRAFT and, on that version's approval, moves the chain's approval to it; the old
version stays exactly as decided and stays readable. The usage rows of the old version are
not moved.

### 2.4 What a claim may lean on

A report is usable by a claim or authorization when `APPROVED`, the service date lies in
`[valid_from, valid_to]`, and the service is one of its `medical_report_service` rows;
`ReportCoverage(ctx, tx, reportID, serviceDefinitionID, date)` is the port WP-I5-04 calls,
and every call writes a usage row.

### 2.5 Visibility

The report's `clinical_summary`, its type/subtype and its linked documents are clinical:
the same two projections as WP-I5-01, the same `CLINICAL_READ_REQUIRED`, the same access
events. The financial projection carries the reference, dates, provider, status and the
covered services with their limits — what a financial reviewer needs to reconcile a claim
against — and nothing that says what was wrong with the person.

### 2.6 Permissions

`health.medical_report.manage` and `health.medical_report.review` are seeded. No new
permission. Confirm both are in `roles.go` for PROVIDER_STAFF and MEDICAL_REVIEWER
respectively (they are) and cover them in the two-halves test.

### 2.7 The mock

`medical-report-handlers.ts` with the lifecycle, the immutability, the version chain and
both projections; a world with a draft, an approved report with two service lines, a
rejected one and its version 2.

## 3. Tests required

- Approved report is immutable: patch and put answer 409; a version 2 approves and the
  chain has exactly one APPROVED; the old version's row is byte-identical before and after.
- Submit refuses without a service line or without a clean report document.
- Coverage: in-window, covered service → usable and a usage row written; out of window or
  another service → not usable, no row.
- Sponsor-HR scan (as in WP-I5-01): the financial projection carries no summary, no
  type/subtype, no document; the clinical one does, with an access event.
- Expiry job idempotent.

## 4. Acceptance criteria

- [ ] An approved report is what the reviewer saw; a correction is a new version and the
      old decision is preserved.
- [ ] A claim can prove which report version it leaned on.
- [ ] Clinical content is invisible without `health.clinical.read`, in the API and the mock.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean; Turkish for every problem
      code; schema version 32.
