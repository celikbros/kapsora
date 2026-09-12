# WP-I5-01 · Health case, encounter, diagnosis — and the clinical/financial visibility split

| Field                      | Value                                                                                                                                                                                                                                                                                              |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M5 (plan increment I5)                                                                                                                                                                                                                                                                             |
| Size                       | L                                                                                                                                                                                                                                                                                                  |
| Depends on                 | M2 (persons, enrollment), M3 (catalog code systems, providers, practitioners), M4 (requests, documents, work queues)                                                                                                                                                                              |
| Runs in parallel with      | WP-I5-05                                                                                                                                                                                                                                                                                           |
| Migration numbers assigned | `000031_health_case.up.sql`                                                                                                                                                                                                                                                                        |
| OpenAPI operations owned   | `listHealthCases`, `createHealthCase`, `getHealthCase`, `closeHealthCase`, `createEncounter`, `getEncounter`, `listEncounterDiagnoses`, `putEncounterDiagnoses`, `listHealthAccessLog`                                                                                                             |
| Read first                 | v1.2 9.12, 10.3, 11.10, 16.7 (`health.*` rows), 16.14; ADR-015; WP-I4-01 as delivered (`internal/servicerequest`), WP-I4-04 (`audit.access_event` for HEALTH downloads); `internal/audit/audit.go` (`RecordAccess`, `SanitizeDetail`); the M4 lesson in docs/plan/ROADMAP.md about mock-versus-server divergence |

## 1. Goal

KAPSORA is not an EHR (v1.2 11.10). It keeps the clinical minimum a provision and a claim
need — a case, its encounters, their diagnoses — and it keeps that minimum away from
everybody whose job does not need it. The second half is the whole point of this package:
**a sponsor's HR user cannot see a diagnosis, a psychiatric category, a test result or a
doctor's report, in the API or on any screen, ever.** That is not a screen rule; it is
enforced where the data is read.

## 2. Scope

### 2.1 Schema (migration 000031, new `health` schema)

`health.health_case`: id, tenant_id, person_id (composite FK), program_id, enrollment_id,
`case_type` (`OUTPATIENT`,`INPATIENT`,`CHRONIC`,`MATERNITY`,`OTHER`), `provider_organization_id`
NULL, `opened_at`, `closed_at` NULL, `status` (`OPEN`,`CLOSED`), `sensitivity`
(`STANDARD`,`SENSITIVE`) — see 2.3, `service_request_id` NULL (the request the case was
opened for), `created_by`, `updated_by`, row_version. Indexes `(tenant_id, person_id,
opened_at DESC)` and `(tenant_id, provider_organization_id, status)`.

`health.encounter`: id, tenant_id, case_id, `encounter_type` (`OUTPATIENT`,`INPATIENT`,
`EMERGENCY`,`TELEHEALTH`), `started_at`, `ended_at` NULL CHECK `ended_at >= started_at`,
`location_id` NULL (provider location), `practitioner_id` NULL, `branch_code` NULL (medical
branch from a code system), `notes_clinical` text NULL — the ONE free-text clinical column
in this package, classified HEALTH, never returned without `health.clinical.read`. Index
`(tenant_id, case_id, started_at)`.

`health.diagnosis`: id, tenant_id, encounter_id, `code_system_id` + `code_value_id`
(composite FKs into `catalog.code_system` / `catalog.code_value`; ICD-10 is a code system
like any other, seeded by WP-I5-05), `diagnosis_type` (`PRIMARY`,`SECONDARY`,`SUSPECTED`),
`sensitive` boolean NOT NULL DEFAULT false (derived from the code value's category at write
time; see 2.3), `recorded_at`, `recorded_by`. **Partial unique `(tenant_id, encounter_id)
WHERE diagnosis_type = 'PRIMARY'`**: one primary diagnosis per encounter.

`health.clinical_access_purpose`: a small reference of `purpose_code` values
(`TREATMENT`, `PRE_AUTHORIZATION`, `CLAIM_REVIEW`, `MEDICAL_REVIEW`, `AUDIT`,
`MEMBER_REQUEST`) with a Turkish label; seeded, tenant-independent.

RLS, touch triggers, composite keys throughout; `platform.grant_app_schema_usage('health')`.

### 2.2 Visibility: two projections of one row, decided by permission

Every read of a case, an encounter or a diagnosis is served in one of two projections:

- **Clinical** (`health.clinical.read`): everything.
- **Financial/administrative** (`health.case.read` without `health.clinical.read`): the
  case's identity, type, dates, provider, status and the encounter's dates, location and
  practitioner; **no diagnosis rows, no `branch_code`, no `notes_clinical`, and no field
  from which a diagnosis could be inferred** (no diagnosis count, no "sensitive" flag).

The projection is applied in the application service, once, on the record before it is
mapped to the wire — never by a screen dropping fields. A caller holding only
`health.case.read` who asks for `listEncounterDiagnoses` gets 403 `CLINICAL_READ_REQUIRED`,
not an empty list: an empty list would tell them the encounter has no diagnosis.

### 2.3 Sensitive categories need an extra permission and a reason

v1.2 11.10: psychiatry, genetics, reproductive health and similar categories require an
additional permission and an access reason. Add permission `health.sensitive.read`. A code
value carries `sensitive = true` through its code system's category (WP-I5-05 seeds the ICD
chapters this applies to). A case whose encounters carry any sensitive diagnosis is
`sensitivity = 'SENSITIVE'` (maintained on diagnosis write).

Reading a SENSITIVE case's clinical projection requires `health.clinical.read` **and**
`health.sensitive.read` **and** a `purpose_code` from `health.clinical_access_purpose` in
the request (`X-Access-Purpose` header, optional `X-Access-Reason` free text ≤ 200 chars).
Without the purpose the answer is 428 `ACCESS_PURPOSE_REQUIRED`. Without the permission the
whole case is served in the financial projection — its sensitivity is itself sensitive.

### 2.4 Every clinical read is an access event

Every clinical-projection read writes `audit.access_event` with `data_classification =
'HEALTH'`, `access_type = 'VIEW'` (or `SEARCH` for a list), the `person_id`, the purpose
code and reason text, `outcome = 'SUCCESS'`; a refused sensitive read writes one with
`outcome = 'DENIED'`. `listHealthAccessLog` (`audit.read`) answers who looked at a person's
clinical data and why, which is the member's own right to know. The financial projection
writes no access event: it carries nothing clinical.

### 2.5 Cases, encounters and the request

A case is opened from a service request of type `DIRECT_SERVICE` or `PREAUTHORIZATION`
on a HEALTH-domain service, or standalone by a provider. Closing a case needs every
encounter ended and no open inpatient stay (WP-I5-03 adds that check through a port it
owns; here the port exists and defaults to "none open").

`putEncounterDiagnoses` replaces the encounter's diagnosis set as a whole (the set is the
unit) and refuses a second PRIMARY.

### 2.6 Permissions and roles

`health.case.read`, `health.case.manage`, `health.clinical.read` are seeded (000008).
New: `health.sensitive.read` (SENSITIVE). Grant it to MEDICAL_REVIEWER only. Add a role
template **`SPONSOR_HR`** ("Sponsor İK", tenant scope) holding `member.read`,
`service_request.read`, `health.case.read`, `entitlement.read`, `report.read` — and
deliberately NOT `health.clinical.read`. This role exists so the acceptance criterion has a
subject. Both places (migration seed and `roles.go`), same commit, with the two-halves
test the M4 packages established.

### 2.7 The mock

The MSW mock (`web/packages/api-client/src/mocks/`) is a test double of the Go server; M4
found divergences in both directions and treats each as a bug. Add a `health-handlers.ts`
that serves both projections by the actor's permissions, refuses diagnoses with
`CLINICAL_READ_REQUIRED`, demands the purpose header on a sensitive case, and seeds a world
with a standard case, a sensitive case and a `sponsor.hr` account.

## 3. Tests required

- **The sponsor HR cannot see a diagnosis** — the Phase 6 criterion, at the API: as
  `SPONSOR_HR`, list and read a case whose encounter has a PRIMARY diagnosis with clinical
  notes; assert the wire body contains no diagnosis, no branch code, no clinical notes, and
  no field that reveals a diagnosis exists; `listEncounterDiagnoses` is 403. Then scan the
  serialized JSON for the diagnosis code and the notes text and assert neither appears.
- A sensitive case: `health.clinical.read` alone gets the financial projection; with
  `health.sensitive.read` but no purpose → 428; with both → the clinical projection and a
  HEALTH access event carrying the purpose and person; the refusal wrote a DENIED event.
- One PRIMARY diagnosis per encounter (constraint and 422).
- Case closure refused with an unended encounter.
- RLS on every table; the access log lists reads by person and never includes the
  financial-projection reads.
- Both projections in the mock, verified by the same sponsor-HR scan.

## 4. Acceptance criteria

- [ ] A sponsor HR user cannot see clinical detail in the API, and a screen cannot leak
      what the API never sent.
- [ ] Sensitive categories need the extra permission and a stated purpose, and both the
      look and the refusal are on the record.
- [ ] Every clinical read is an access event a member could be shown.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean; every problem code has a
      Turkish message; schema version 31.
