# WP-I3-04 · Rule engine: sets, versions, CEL rules, test cases, publishing and evaluations

| Field                      | Value                                                                                                                                                                                                                                                                                                     |
| -------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M3 (plan increment I3)                                                                                                                                                                                                                                                                                    |
| Size                       | L                                                                                                                                                                                                                                                                                                         |
| Depends on                 | M2 (eligibility shapes the input), WP-I3-01                                                                                                                                                                                                                                                               |
| Runs in parallel with      | WP-I3-03                                                                                                                                                                                                                                                                                                  |
| Migration numbers assigned | `000022_rules_engine.up.sql`                                                                                                                                                                                                                                                                              |
| OpenAPI operations owned   | `listRuleSets`, `createRuleSet`, `getRuleSet`, `patchRuleSet`, `listRuleSetVersions`, `createRuleSetVersion`, `getRuleSetVersion`, `putRules`, `putRuleTestCases`, `runRuleTests`, `simulateRuleSetVersion`, `submitRuleSetVersion`, `publishRuleSetVersion`, `retireRuleSetVersion`, `getRuleEvaluation` |
| Read first                 | v1.2 9.9, 11.7, 16.6 (rules rows); WP-I2-04 eligibility (its explanation model is the one rules extend); ADR-015. New ADR required: **ADR-022 rule expression language**                                                                                                                                  |

## 1. Goal

The decisions that differ per customer — which document a claim needs, when a pre-approval
is required, what age a dependant may be, whether a diagnosis matches a service — must be
configurable without a deployment, and must be auditable years later. This package gives
those decisions a home: versioned, tested before publishing, immutable once published, and
recorded with the exact version that produced every answer.

## 2. Scope

### 2.1 Expression language (write ADR-022 first)

Rules are **CEL** (`github.com/google/cel-go`), not a home-made DSL and not embedded
scripting. CEL is a good fit for the constraints v1.2 11.7 sets: it is total (no unbounded
loops), has no I/O of any kind, evaluates deterministically over its input, and compiles
once and runs many times. The ADR records that choice against the alternatives (a JSON
decision table — too weak for the health rules; Lua or Starlark — too strong, both would
need sandboxing we would then own).

Hard limits enforced in the evaluator wrapper, not by convention:

- Compile with a fixed environment: only the declared input variables, plus a small
  helper set (`date`, `age`, `overlaps`, `sum`, `has`). No `net`, no `os`, nothing that
  reaches outside.
- A per-rule evaluation budget via `cel.EvalOptions` interruption and a context timeout of
  50 ms; exceeding it is `RULE_TIMEOUT`, an ERROR explanation, never a silent skip.
- Program compilation is cached per `(rule_set_version_id, rule_id)`; a published version
  is immutable, so the cache never needs invalidation.

### 2.2 Schema (migration 000022, new `rules` schema)

`rules.rule_set`: id, tenant_id, `code`, `name`, `domain_code`, `purpose`
(`ELIGIBILITY`,`DOCUMENT`,`PREAUTH`,`LIMIT`,`DUPLICATE`,`DIAGNOSIS_SERVICE`,`PRICE`,`ADJUDICATION`),
`status`, row_version, timestamps. Unique `(tenant_id, code)`.

`rules.rule_set_version`: id, tenant_id, rule_set_id, `version_no`, `status`
(`DRAFT`,`UNDER_REVIEW`,`PUBLISHED`,`RETIRED`), `valid_from`, `valid_to` NULL,
`input_schema` jsonb NOT NULL (the declared variables and their CEL types),
`content_hash` text NULL, submit/publish/retire metadata as in WP-I3-03. Exclusion
constraint: no two PUBLISHED versions of one set overlap in
`daterange(valid_from, valid_to, '[)')`.

`rules.rule`: id, tenant_id, rule_set_version_id, `code`, `name`, `priority` int NOT NULL,
`condition` text (CEL), `actions` jsonb (an ordered list, see 2.3), `explanation_code`
text NOT NULL, `explanation_params` jsonb, `stop_on_match` boolean NOT NULL DEFAULT false,
`active` boolean. Unique `(tenant_id, rule_set_version_id, code)` and unique
`(tenant_id, rule_set_version_id, priority)` — **two rules may not share a priority**,
because evaluation order must be total and reproducible, not "whatever the index returned".

`rules.rule_test_case`: id, tenant_id, rule_set_version_id, `code`, `description`,
`input` jsonb, `expected_outcome` text, `expected_explanations` text[], `expected_actions`
jsonb NULL. Unique `(tenant_id, rule_set_version_id, code)`.

`rules.evaluation`: id, tenant_id, `subject_type` text, `subject_id` uuid NULL,
`rule_set_version_id` (composite FK), `input_hash` bytea, `input_snapshot` jsonb,
`outcome` text, `evaluated_at`, `evaluated_by`, `duration_ms` int. Append-only
(`platform.make_append_only`). Index `(tenant_id, subject_type, subject_id, evaluated_at desc)`.

`rules.evaluation_result`: id, tenant_id, evaluation_id, `sequence` int, `rule_id`,
`rule_code`, `matched` boolean, `action_type`, `action_payload` jsonb,
`explanation_code`, `severity`. Append-only, unique `(tenant_id, evaluation_id, sequence)`.

### 2.3 Actions

An action is what a matched rule does, and the closed list is the one v1.2 11.7 names:
`APPROVE`, `REJECT`, `WARN`, `REQUIRE_DOCUMENT`, `REQUIRE_PREAUTH`, `REQUIRE_MEDICAL_REVIEW`,
`REQUIRE_FINANCIAL_REVIEW`, `PARTIAL_APPROVE`, `RESERVE_ENTITLEMENT`, `ADJUST_PRICE`,
`SET_LIMIT`. Each carries a typed payload validated on write, so a malformed action is
rejected at authoring time and not at three in the morning. The engine itself performs
none of them: it returns them, and the caller (eligibility, authorization, adjudication)
decides what to do. That separation is what keeps the engine free of side effects.

Outcome folding, once all rules have run in priority order: any `REJECT` → `REJECTED`; else
any `REQUIRE_*` → `REVIEW_REQUIRED`; else any `PARTIAL_APPROVE` → `PARTIALLY_APPROVED`;
else `APPROVED`. `stop_on_match` ends the pass early and is recorded in the results.

### 2.4 Publishing gate

`submit` refuses unless the version has **at least one test case and every test case
passes** (422, field `testCases`, code `TESTS_REQUIRED` or `TESTS_FAILING` with the failing
case codes). `publish` needs a different actor and a step-up, exactly as contract and plan
versions. This is v1.2 11.7's first line and the reason the test-case table exists at all.

`POST .../versions/{id}/tests:run` (`rules.manage`) runs the cases against the draft and
returns per-case actual versus expected. `POST .../versions/{id}:simulate` runs one
supplied input against a draft or published version and returns the full trace **without
writing an evaluation row and without any side effect** — v1.2 11.7's "simulation creates
no transaction".

### 2.5 Evaluation API and reuse

`internal/rules` exposes `Evaluate(ctx, versionID, input) (Evaluation, error)` for other
modules; the only HTTP surface is `getRuleEvaluation` plus simulate. Eligibility
(WP-I2-04) gains a real `ruleSetVersionIds` on its result once a tenant has a published
ELIGIBILITY set — wire that in WP-I3-05 rather than here, so this package stays a library
plus its authoring API.

## 3. Tests required

- Evaluator: priority order respected; `stop_on_match`; a rule whose condition throws
  yields an ERROR explanation and does not abort the pass; the 50 ms budget produces
  `RULE_TIMEOUT`; the same input twice produces byte-identical results and the same hash.
- Sandbox: a condition attempting an undeclared identifier fails at compile time, not at
  evaluation; the environment offers no I/O function (assert the declaration list).
- dbtest: duplicate priority refused; submit without tests refused; submit with a failing
  test refused, naming it; publish by the submitter refused; published version immutable
  in every child table; overlap exclusion; evaluation and result rows reject UPDATE; RLS.
- Simulation writes nothing (row counts before and after are equal).

## 4. Acceptance criteria

- [ ] A rule set version cannot reach review without a passing test case, and cannot be
      published by the person who submitted it.
- [ ] A published version never changes, and every evaluation names the version that
      produced it and the rules that fired, in order, with explanation codes.
- [ ] Simulation of production data produces no row and no side effect.
- [ ] ADR-022 written; OpenAPI, generated code, Spectral and `oasdiff` clean; schema
      version 22.
