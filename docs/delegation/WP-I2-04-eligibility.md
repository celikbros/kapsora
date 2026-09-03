# WP-I2-04 · Eligibility API with as-of resolution, explanations and evaluation snapshots

| Field | Value |
|---|---|
| Milestone | M2 (plan increment I2) |
| Size | M |
| Depends on | WP-I2-01, WP-I2-02, WP-I2-03 |
| Runs in parallel with | WP-I2-05 |
| Migration numbers assigned | `000016_benefit_eligibility_evaluation.up.sql` |
| OpenAPI operations owned | `checkEligibility` (exists), new: `getEligibilityEvaluation` |
| Read first | v1.2 10.2, 11.4, 16.5 (`eligibility_evaluation`), contract `EligibilityCheckRequest/Result`; ADR-015 problem codes |

## 1. Goal

Given a person, a service date and requested items, answer whether the person is
eligible, with which plan version and balances, and why, in a form that a provider
counter, the backoffice and the request workflow (I4) can all use. Every evaluation is
stored immutably so a later dispute can show exactly what was known.

## 2. Scope

### 2.1 Resolution (pure function over loaded data, `internal/benefit/eligibility`)

Inputs: tenant, `personId`, optional `programId`, `providerOrganizationId`,
`serviceDate`, `serviceItems[]` (`serviceDefinitionId` is opaque until I3: match items to
entitlement definitions by `context.entitlementCode` when given, otherwise evaluate the
person-level outcome only and mark item results `REVIEW_REQUIRED` with explanation
`SERVICE_MAPPING_PENDING`).

Steps, each producing explanation codes (severity INFO/WARNING/ERROR):
1. Person exists and is ACTIVE (`PERSON_NOT_FOUND`, `PERSON_INACTIVE`).
2. Sponsor membership active on `serviceDate` (`MEMBERSHIP_NONE`, `MEMBERSHIP_SUSPENDED`).
3. Enrollment active on `serviceDate`; when `programId` is given, restrict to that
   program (`ENROLLMENT_NONE`, `ENROLLMENT_SUSPENDED`, `ENROLLMENT_MULTIPLE` → REVIEW_REQUIRED).
4. Plan version published as of `serviceDate` (`PLAN_VERSION_NONE`).
5. Entitlement accounts for the period containing `serviceDate` (lazily opened via
   `EnsureAccounts`), balances including family-shared accounts; per item compare
   requested quantity with `available` (`BALANCE_INSUFFICIENT`, `BALANCE_OVERDRAFT_ALLOWED`).
6. Outcome: all items OK → `ELIGIBLE`; some → `PARTIALLY_ELIGIBLE`; none → `INELIGIBLE`;
   any REVIEW code → `REVIEW_REQUIRED`; missing person/membership data → `MISSING_DATA`.

The result carries `planVersionId`, `ruleSetVersionIds: []` (rules arrive in I3),
`balances[]`, `explanations[]`, `evaluatedAt`, and a new `evaluationId`.

### 2.2 Persistence (migration 000016)

`benefit.eligibility_evaluation`: id, tenant_id, person_id (composite FK), program_id,
enrollment_id, plan_version_id (nullable composite FKs), service_date, outcome,
request_hash bytea (SHA-256 of canonical request), request_snapshot jsonb (ids and
quantities only, never identifiers), result_snapshot jsonb, evaluated_at, evaluated_by,
idempotency_key text NULL; append-only; index `(tenant_id, person_id, service_date desc)`;
unique `(tenant_id, idempotency_key)` where not null. RLS.

### 2.3 API

- `POST /api/v1/eligibility/checks` (`eligibility.check`, optional `Idempotency-Key`):
  runs the resolution inside one read transaction (`REPEATABLE READ`), stores the
  evaluation, writes `audit.access_event` (classification SENSITIVE when
  `context.domain = HEALTH`, NORMAL otherwise) and returns 200. With an idempotency key
  the stored result is replayed for 24 h.
- `GET /api/v1/eligibility/evaluations/{id}` (`eligibility.check`): the stored snapshot.
- Provider-scoped actors (scope type ORGANIZATION) may only check for
  `providerOrganizationId` inside their scope → 403 `PERMISSION_DENIED` otherwise.

## 3. Tests required

- Pure resolver table tests for every explanation code and outcome mapping.
- dbtest: end to end with WP-I2-01..03 fixtures (person, membership, enrollment,
  published version with two definitions, accounts); boundary dates; family-shared
  balance visible for a dependant; evaluation row immutable (UPDATE rejected);
  idempotent replay returns the same `evaluationId`; access event written; no identifier
  in snapshots (assert on JSON).
- HTTP: 422 on bad dates/items, provider scope enforcement.

## 4. Acceptance criteria

- [ ] Result names the plan version used and explains every non-eligible item with a code.
- [ ] Evaluations stored immutably without identifiers; retrievable by id.
- [ ] Health-context checks produce SENSITIVE access audit entries.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean.
