# WP-I2-02 · Programs, plans, plan versions (maker-checker publish), entitlement definitions, enrollments

| Field | Value |
|---|---|
| Milestone | M2 (plan increment I2) |
| Size | L |
| Depends on | M1; WP-I2-01 for memberships (enrollment needs a sponsor membership) |
| Runs in parallel with | WP-I2-01 (except the enrollment endpoints) |
| Migration numbers assigned | `000014_benefit_plan_version_review.up.sql` (adds `submitted_by`, `submitted_at`, `review_comment` to `benefit.plan_version`; `published_by <> submitted_by` CHECK) |
| OpenAPI operations owned | `listPrograms`, `createProgram`, `getProgram`, `updateProgram`, `listPlans`, `createPlan`, `getPlan`, `updatePlan`, `listPlanVersions`, `createPlanVersion`, `getPlanVersion`, `updatePlanVersion`, `submitPlanVersion`, `publishPlanVersion`, `retirePlanVersion`, `listEnrollments`, `createEnrollment`, `updateEnrollment` |
| Read first | v1.2 11.3, 16.5 (benefit tables), 45; migrations 000004/000005; `benefit.tg_plan_version_guard` |

## 1. Goal

A payer or sponsor operator defines what members are entitled to: a program (sponsor +
payer + type), plans under it, versioned plan configurations with entitlement definitions,
published through maker-checker and immutable afterwards, and enrollments that bind a
sponsor membership to a plan for a period. Decisions elsewhere snapshot the plan version
that was valid on the service date.

## 2. Scope

### 2.1 Programs and plans

- Programs: CRUD with `program.read` / `program.manage`; fields code (unique per tenant,
  `^[A-Z][A-Z0-9_-]{1,39}$`), name, program type (catalog `benefit.program_type`, baseline
  provisioned: `HEALTH`, `WELLBEING`, `ACCOMMODATION`, `ASSISTANCE`, `MIXED`), sponsor and
  payer organizations (tenant_organization ids with roles SPONSOR/PAYER), `validFrom/To`,
  status transitions DRAFT→ACTIVE→SUSPENDED↔ACTIVE→CLOSED.
- Plans: CRUD under a program with `plan.manage`; code unique per program; status
  DRAFT→ACTIVE→RETIRED.

### 2.2 Plan versions and entitlement definitions

- `POST /plans/{planId}/versions` creates a DRAFT with `version_no = max+1`, optional
  `copyFromVersionId` (copies entitlement definitions).
- Draft editing: `PATCH /plan-versions/{id}` (validity period, notes) and the nested
  entitlement definition list (`PUT` the full list on the draft:
  `{code, name, unitType, currencyCode?, periodType, periodLength?, initialQuantity,
  allowOverdraft, rolloverPolicy, rolloverCap?, familyShared}`); DB CHECKs from 000005 map
  to 422 field errors.
- Maker-checker: `POST /plan-versions/{id}/submit` (`plan.manage`) sets UNDER_REVIEW and
  `submitted_by`; `POST /plan-versions/{id}/publish` (`plan.publish`, step-up) sets
  PUBLISHED, `published_by`, `published_at`, `configuration_hash` (SHA-256 of the
  canonical JSON of definitions + period). The publisher must differ from the submitter
  (CHECK in migration 000014 + 403 `MAKER_CHECKER_SAME_ACTOR`). Overlapping published
  periods → 409 `PLAN_VERSION_OVERLAP` (exclusion constraint). Any write to a PUBLISHED
  version other than retire → 409 `PLAN_VERSION_IMMUTABLE` (trigger errcode).
- `POST /plan-versions/{id}/retire` (`plan.publish`): PUBLISHED→RETIRED with a reason;
  a retired version stays readable.
- `GET /plan-versions/{id}` returns definitions, hash, status, review metadata.

### 2.3 Enrollments

- `POST /people/{personId}/enrollments` (`enrollment.manage`, idempotent):
  `{sponsorMembershipId, planId, validFrom, validTo?, enrollmentReason?}`; membership
  must belong to the person and be ACTIVE for `validFrom`; plan ACTIVE with a PUBLISHED
  version covering `validFrom` (else 422 `PLAN_NOT_PUBLISHED`); overlap → 409
  `ENROLLMENT_OVERLAP`.
- `PATCH /enrollments/{id}` (`If-Match`): status ACTIVE|SUSPENDED|ENDED, `validTo`.
- `GET /people/{personId}/enrollments`, `GET /enrollments?planId=&status=` with paging.
- On enrollment creation, the service emits an outbox event `benefit.enrollment.created`
  (payload: ids only); WP-I2-03 consumes it to open entitlement accounts.

## 3. Interfaces and rules

- Package `internal/benefit/{domain,application,infrastructure/postgres,transport/http}`;
  queries `db/queries/benefit.sql`. Audit action codes `program.*`, `plan.*`,
  `plan_version.submit|publish|retire`, `enrollment.*`.
- Selection helper exported for later packages:
  `benefit.ResolvePlanVersion(ctx, tx, tenantID, planID, asOf date) (PlanVersion, error)`
  returning the PUBLISHED version whose `valid_period` contains `asOf`, or
  `ErrNoPublishedVersion`.
- Canonical JSON for the hash: sorted keys, definitions sorted by code, numbers as
  strings with trailing zeros trimmed.

## 4. Tests required

- db/tests: migration 000014 applied, `expectedSchemaVersion` 14; publisher = submitter
  rejected by CHECK.
- Application (dbtest): full lifecycle draft→review→publish→retire; second publish for an
  overlapping period 409; editing a published version 409; hash stable across
  re-serialisation; enrollment requires a published version as of `validFrom`; overlap
  409; `ResolvePlanVersion` picks the right version at period boundaries (inclusive lower,
  exclusive upper); outbox event written in the same transaction as the enrollment.
- HTTP: maker-checker same-actor 403 with audit, step-up on publish, validation payloads.

## 5. Acceptance criteria

- [ ] Published versions immutable and non-overlapping (DB-enforced, tested).
- [ ] Publish requires a different actor than submit and step-up.
- [ ] Enrollment overlap rejected by the database; plan must be published as of start.
- [ ] `ResolvePlanVersion` documented and covered at boundaries.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean.
