# WP-PC06 · Bind each lodging hold to one enrollment

| Field | Value |
| --- | --- |
| Status | Bounded isolated acceptance complete on f94c269; runtime acceptance separate |
| Planned | 2026-10-09, gpt-6-astra |
| Outcome | Eligibility, pricing, booking and reservation use one validated enrollment |
| Migration / permissions | None; no grants, role or settings changes |
| Public contract | Existing request fields retained; document ENROLLMENT_MULTIPLE refusal |
| Acceptance | Synthetic isolated CI evidence; runtime verification remains separate |

## 1. Sources and necessity

Read [the delegate handbook](README.md), current HANDOVER/ROADMAP PC-06 checkpoints,
[the payer-boundary package](WP-PC06-contract-payer-boundary.md), and
[the confirmation-binding package](WP-PC06-confirmation-contract-binding.md).
The integrator owns handover, roadmap and CI acceptance updates.

WP-I2-04 section 2.1 specifies ENROLLMENT_MULTIPLE as REVIEW_REQUIRED, with optional
program narrowing. WP-I6-01 section 2.3 requires a quote under the person's enrollment
and provider contract; WP-I6-02 section 2.2 reserves the booked plan's entitlement.
CreateHoldRequest.programId narrows enrollment to the selected program and cannot
broaden a person without enrollment. Neither the public hold nor waitlist join request
accepts enrollmentId. The eligibility API separately supports explicit enrollment choice.
The existing payer package section 3 deliberately preserves omitted-program search as a
union and defers hold enrollment coherence, including several enrollments in one program.

Current GetPersonEnrollmentForStay orders by e.id and limits to one. Eligibility instead
sorts by latest validity start then ID, loads that enrollment's plan/accounts, and reports
ENROLLMENT_MULTIPLE. Its individual item outcomes may still be ELIGIBLE despite the
review-required overall outcome. Accommodation checkEligibility currently consumes those
item balances without asserting the evaluation enrollment. prepareHold saves its own
resolved enrollment but passes the original optional program to pricing and eligibility.
This can mix one plan's evaluated coverage with another plan's booking/reservation and,
when program is omitted, another enrolled program's contract. Static evidence identifies
the risk; it is not a completed SQL/HTTP reproduction or proof of a live bad booking.

There is no source-backed preference for lowest UUID, newest plan, cheapest contract or
best-funded plan. The bounded correction refuses an unresolved funding choice before a
new hold or waitlist entry is written. This applies the existing ambiguity requirement;
it does not introduce an automatic preference or a new plan-selection workflow. If the
isolated reproduction disproves the described path, report it before expanding scope.

## 2. Exact command behavior

Ordinary create-hold and waitlist join resolve active enrollments for the same tenant,
person, first night and optional program using existing active-program/date predicates:

- Zero candidates: retain ENROLLMENT_NOT_FOUND and current privacy behavior.
- Exactly one: use that enrollment and its actual program throughout the operation.
- More than one: return ENROLLMENT_MULTIPLE. Do not select by sort order, mapping,
  available balance or contract ranking. No booking, waitlist row, inventory counter,
  entitlement reservation or related ledger movement may be created by this refusal.

An explicit program succeeds only when that program leaves one candidate. Two active
plans within it remain ambiguous. Ordinary omitted-program commands may therefore refuse
while broad availability still shows candidates. This is intentional: a search does not
commit funding. Retain all existing idempotent replay behavior for commands already saved;
this package does not re-resolve or rewrite historical successful bookings on replay.

Scheduler ExpectedEnrollmentID is a trusted internal selection, never populated from HTTP.
Resolve that exact enrollment directly with tenant, person, requested program, first-night
validity, active enrollment and active program checks. Do not first select the lowest ID
and compare it afterward. A different active enrollment in the same program must neither
block a valid pinned offer nor replace an unavailable pinned enrollment. Missing,
suspended, out-of-period, wrong-person/program or foreign-tenant selections select nothing;
the entry remains waiting under existing scheduler refusal handling. Preserve provider and
person boundaries. Existing active membership/person eligibility checks still apply; do
not redesign eligibility-wide membership rules here.

## 3. Implementation boundary

1. Replace the single arbitrary query result with bounded ambiguity detection (at most
   two ordinary candidates suffices). Add exact enrollment filtering for the internal
   scheduler path, retaining all existing scope predicates. Keep SQL in db/queries,
   update repository/ports/fakes and regenerate sqlc. No migration is required.
2. Share the command resolution semantics between prepareHold and JoinWaitlist. Add a
   typed ambiguity error; do not turn query failures into no-enrollment answers.
3. After resolving a hold, load its pricing world with the resolved ProgramID and call
   eligibility with both resolved ProgramID and exact EnrollmentID. Preserve the general
   search helper's existing no-enrollment behavior; a private hold-specific argument or
   helper is preferable to adding a public search field. Assert the returned evaluation
   identifies the selected enrollment before accepting its items as funding evidence.
   A missing/different identity must refuse, never fall back to another enrollment.
4. Keep pricing candidates bounded to enrollment-derived payers of that resolved program.
   Preserve ranking, dates, exact winning contract-version binding, contract hold duration,
   quote snapshot format, existing quantity calculations and partial member-paid nights. This package validates unit-factor NIGHT mappings only; see section 8.
5. The persisted booking enrollment/program, referenced eligibility evaluation's
   enrollment/plan version, quote coverage and reservation account must agree. Continue
   eligibility outside inventory locks, using existing transaction boundaries; this
   package does not claim serializable enrollment policy across concurrent administration.
6. Preserve omitted-program availability payer union exactly. Do not change general
   eligibility precedence or response semantics, contract selector ranking or search UI.

## 4. Public refusal contract and existing presentation

Use HTTP 422 application/problem+json for ENROLLMENT_MULTIPLE on createHold and joinWaitlist,
consistent with their existing enrollment-not-found validation response. Suggested type:
accommodation/enrollment-multiple. Turkish title: "Birden fazla geçerli plan kaydı var".
Detail: "Bu tarihler için kullanılacak plan kaydı kesinleştirilemedi. Kurum yetkilinize başvurun."
Do not claim selecting a program always resolves multiple plans within that program.

Update only the owned OpenAPI 422 response descriptions and request documentation needed
to explain ambiguity; retain all request/response shapes and permissions. Regenerate as
required and verify contract compatibility. Add the problem code to the established
frontend Turkish message mapping and English resource skeleton if required by current
repository standards. Existing error presentation suffices: no candidates response,
new enrollment input, picker, navigation or new UI flow. Do not expose candidate IDs or
other member/tenant information in the refusal.

## 5. Tests first and isolated evidence

Write focused regressions before production changes. Use synthetic fixtures and production
SQL/HTTP wiring in isolated CI; do not run local PostgreSQL-backed tests. Freeze time,
set deterministic UUID and enrollment validity ordering deliberately, and publish fixture
plan/contract versions through existing supported setup. Different mappings, balances,
prices and hold terms make accidental cross-plan reuse observable.

| Case | Required evidence |
| --- | --- |
| Two programs, omitted program | Opposing UUID and validity-start ordering; both funded, distinct payers and higher-ranked alternate price. Hold returns ENROLLMENT_MULTIPLE with no hold effects; join has no queue effects |
| Explicit program | Same person selects A leaving one enrollment; booking, evaluation, quote coverage, selected contract and reservation all belong to A; B cannot supply price or hold terms |
| Same program, two plans | Explicit program still refuses ordinary hold and join with ENROLLMENT_MULTIPLE, regardless of differing balances/mappings and eligibility's preferred order |
| Exact scheduler enrollment | Join while A is unique, then add a same-program enrollment whose lower ID would win old SQL; scheduler offers using saved A and evaluates/reserves A |
| Pinned selection unavailable | Suspended, expired, future, wrong-person/program and foreign-tenant enrollment cannot be replaced; no offered booking or reservation, queue stays waiting |
| Pinned plan cannot fund | Another enrollment has funds but selected A has no usable mapping/balance; no fallback or successful offer |
| Single enrollment | Omitted program still succeeds with matching evaluation, unit-factor NIGHT mapping, coverage, reservation and immutable quote metadata |
| Search compatibility | Omitted-program availability preserves A+B payer union; explicit A remains narrowed; do not assert broad search resolves a unique funding enrollment |
| Boundaries and retries | Existing tenant/provider/person refusals, unknown program behavior and exact successful command replay remain intact |
| Eligibility identity | Application-level regression rejects a missing/different returned evaluation enrollment rather than freezing its balances |

For successful holds assert exact booking enrollment/program, stored evaluation enrollment
and planVersionId, frozen covered/member-paid nights and amounts, winning first-night
contract identity/hold duration, entitlement definition/account enrollment, mapped reserved
quantity for a factor-1 NIGHT mapping, one reservation and one RESERVE movement. Exercise release/cancellation and prove
available + reserved + consumed conservation with no other enrollment's account changed.
For ambiguity inspect booking/queue counts, held counters, reservation rows and ledger
movements before/after. Evaluation snapshots and idempotent account opening are not
booking effects; do not assert eligibility itself is universally read-only.

Run the new regressions against old production behavior in an isolated temporary CI
checkout or a narrowly restored old-source negative control. Record intended failures:
ordinary ambiguous command succeeds/chooses silently, or same-program pinned scheduler
fails to offer its still-valid saved enrollment. Ensure fixtures reach those assertions;
compile failures, unavailable services and unrelated constraints are not negative proof.
Restore source and regenerated outputs, then require passing final tests. Existing
payer and confirmation-binding regressions remain mandatory; report exact test names,
code head, commands, no-skip counts and negative-control assertions.

Local validation is limited to pure tests, affected-package compilation, formatting,
regeneration consistency, scoped vet/lint and contract checks. Explicitly blank
KAPSORA_TEST_ADMIN_DATABASE_URL in every local Go-test invocation. The shared application
role password must not be reset. The integrator runs required isolated CI gates and
reviews the bounded patch before recording technical acceptance.

## 6. Exclusions and remaining gates

No local database writes/tests, migrations, grants, settings administration, server
start/stop/restart, live bookings or live compensation. No enrollment-choice UI/API,
preferred-plan policy, search ranking redesign, account-mapping redesign, legacy snapshot
repair, historical booking rewrite or unrelated eligibility cleanup. External
authorization/status atomicity, other-tenant legacy recovery, migration 58 application,
Management runtime reads, owner acceptance and retained calendar checks remain separate.

This package passed bounded isolated acceptance and local pure/compile and mock checks.
Runtime verification awaits the existing operator-controlled reload process; successful isolated CI must not be
reported as live acceptance. Source review alone does not establish any affected live row.

## 7. Required account-path correction discovered during review

This is necessary to satisfy section 3, not a general ledger selection redesign.
Eligibility's evaluate currently loads ResolveAccounts(person, day), and accountsByCode
chooses the highest available same-code balance. Accommodation reserveNights also loads
person-wide accounts and takes the first matching code. ListPersonEntitlementAccounts
filters account period and person/principal relationship; it does not even restrict all
returned accounts to active enrollment status/date. Thus pinning only eligibility's
EnrollmentID or the booking program leaves cross-plan balance borrowing possible.

Use the existing EnsureAccounts(selectedEnrollment, day).Accounts as the authoritative
allowed account-ID set. It enumerates active entitlement definitions of the selected
published plan version and resolves the correct account holder per definition. Narrow
both pinned eligibility balances and the lodging reservation account to this set before
matching entitlement codes. Eligibility already has this EnsureResult; reuse it. For
reservation, a small ledger-layer helper may call EnsureAccounts and intersect the
person/date ResolveAccounts result with the exact ensured IDs. This reuses tenant/person
reachability, account periods and Shared metadata without duplicating family SQL. Caller
intersection with the already obtained EnsureResult is also valid; avoid an unnecessary
new public API. Keep account-open checks and existing code-level selection precedence
inside the narrowed set. Never select the highest balance across unrelated plans.

Exact family semantics come from accounts.go accountHolder and FindPrincipalEnrollment:
nonshared definitions use the selected enrollment. A shared definition for a dependant
uses that selected enrollment's sponsor membership principal, in the same plan, on the
service date, following the existing PENDING/ACTIVE principal lookup. With no qualifying
principal enrollment it retains the selected dependant enrollment's own account. Preserve
these rules, including existing lookup precedence, rather than redesigning family policy.
Do not filter solely by account.EnrollmentID == selected: legitimate shared holders differ.
Do not permit account.Shared alone: another principal plan/program, another membership
link or old definition with the same code is not this selected plan's entitlement.
Exact ensured IDs also constrain the definition and account period, beyond mere code.

Apply narrowing to explicitly pinned eligibility checks used by this hold path; preserve
unpinned general eligibility behavior in this bounded package. Keep public ledger/person
account listing unchanged. Authorization's adopted-reservation path already bypasses its
broad account resolver (authorization.go, create path with AdoptReservationID), so it should
adopt the exact lodging reservation unchanged; no general authorization account refactor
is needed. Verify adoption posts no second reservation and retains the original account.

Add isolated tests with deliberately colliding entitlement codes:

- Explicit A enrollment with less balance than B: eligibility uses A's coverage and the
  reservation spends A, even when B sorts first or has the larger balance.
- Selected dependant A legitimately uses its principal's A shared account, whose enrollment
  ID differs from the booking's; quote, reservation, adoption and release remain coherent.
- The same principal has funded B (different plan/program, same code), while A has no funds:
  neither eligibility nor reservation may borrow B. Include another principal membership
  link or obsolete definition where practical to prove exact IDs, not Shared, authorize use.
- A family-shared definition without a same-plan principal enrollment retains the existing
  own-account fallback; nonshared definitions never spend the principal's balance.

For family cases, section 5's account-enrollment assertion means the exact authorized
holder derived from the selected enrollment, not literal equality with the dependant's
booking enrollment. Assert the selected plan/version definition and the ensured account
ID, as well as unchanged unrelated accounts, one RESERVE, exact release and conservation.
Use negative controls that remove only the account-ID narrowing to expose borrowed
coverage or wrong-account reservation. No local database test, schema or runtime action
is added to the package's authorization.


## 8. Quantity-conversion boundary

Acceptance in this package uses NIGHT mappings with factor 1. It proves enrollment and
account identity, quote/reservation alignment and conservation within that explicit scope;
it does not certify nonunit conversion factors or monetary-entitlement lodging coverage.
Review identified a separate source concern: accommodation treats available entitlement
quantity as a night count and reserves CoveredNights without applying a nonunit service
mapping factor. That path is not reproduced or corrected here. Record it as a separate
follow-up requiring an isolated factor-greater-than-one fixture before implementation.
Do not state that this patch establishes general mapping-factor correctness or expand it
into a quantity/money conversion redesign. Existing factor handling elsewhere is unchanged.

## 9. Isolated acceptance checkpoint — 2026-10-09

All six jobs passed on final source head `f94c269b8ee43950e45ec73b327d9b01f82f80e1`
in [CI run 37984416048](https://github.com/celikbros/kapsora/actions/runs/37984416048).
The early enrollment preflight passed all six new SQL/HTTP functions (8.520 s), the
eligibility suite passed 20 top-level functions (15.697 s), and the full accommodation
suite passed 76 top-level functions (124.245 s), all without skips. The shared-account
case `TestPinnedEligibilityUsesOnlySelectedSharedPlanAccounts` proves exact selected-plan
principal account narrowing directly in eligibility; it is not a complete dependent
booking/adoption journey. Frontend CI passed 756 tests in 92 files and 25 mock smoke
tests; 106 opt-in live/calendar tests remain skipped.

Four isolated runner-only negative controls compiled and failed at their required semantic
assertions: removing ambiguity detection created an ambiguous hold (201); removing the
exact enrollment predicate blocked the still-valid pinned same-program offer; removing
selected-account narrowing mixed the frozen hold coverage; and that same removal borrowed
the other principal plan's balance of 20 instead of 4. Each source mutation was restored.
The existing payer and exact-confirmation-policy negative controls also passed their
detection gates. Normal-source regressions ran before these controls.

The first isolated run also found a real zero-ID boundary error: nullable UUID conversion
turned a nonnil zero enrollment selection into an omitted filter. `f94c269` rejects zero
explicit enrollment/program choices before SQL, and the final SQL regression passes.
No local database test, live hold, waitlist mutation, migration or role grant was performed.
Factor-1 NIGHT coverage is the acceptance boundary; the separate conversion plan and
later booking-held submission coherence are still open.
