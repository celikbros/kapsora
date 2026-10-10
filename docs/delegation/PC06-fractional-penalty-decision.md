# PC06 · Partial-approval penalty decision gate

Status: penalty-cap decision required. The independent cancellation reporting correction
below is implemented; it does not implement the conditional cap or no-show design.
Planned 2026-10-10 by gpt-6-astra. All six jobs passed in final
[CI 37995074957](https://github.com/celikbros/kapsora/actions/runs/37995074957) on bda7984.
Its separate opt-in diagnostic step reached both compiled assertions
FRACTIONAL_PENALTY_CANCEL_REFUSAL and FRACTIONAL_PENALTY_NOSHOW_REFUSAL at
2026-10-09 21:44:39 UTC (2026-10-10 Europe/Istanbul). These intended failures prove the
refusals, not a passing partial-approval penalty workflow. The normal full accommodation
suite passed 103 functions and skipped only these two opt-in diagnostics; free fractional
cancellation passed. The concrete cap/reporting decision was requested asynchronously
and remains unanswered; this successful CI does not authorize a policy change.

The subsequent integer partial-approval diagnostic is accepted as defect reproduction on
`56b64b824cf07b843d3fa8d780dc17f2e3174c10` in
[CI 38042481624](https://github.com/celikbros/kapsora/actions/runs/38042481624), all six jobs
successful. TestFractionalPenaltyReproductionIntegerPartialNoShow reached its compiled
INTEGER_PARTIAL_PENALTY_NOSHOW_REFUSAL at 2026-10-10 09:46:50 UTC: two-night factor-2 hold,
one approved service night, frozen two-night no-show penalty, account 2/2/0, reservation
remaining 2, booking CONFIRMED/report REPORTED and confirmed inventory after rollback.
This proves a partial-approval conflict beyond fractional quantities; a whole-night-only
admission policy would not resolve it. The ordinary accommodation suite passed 103
functions (129.473 seconds), skipping only the three opt-in conflict diagnostics, which
were separately exercised and required their exact expected failures. No domain rule,
fee, grant, schema or operator process was changed by that test commit.

## Proven cases and policy conflict

A two-night hold at factor 2, approved for 0.5 service night, retains 1 entitlement unit;
a one-night cancellation penalty refuses and leaves the booking CONFIRMED. Approval for
1.5 nights retains 3 units; a two-night 100-percent no-show penalty refuses and leaves the
report REPORTED. Transactions roll back rather than silently over-consuming.

WP-I6-03 section 2.2 requires the frozen penalty preview and command to agree and consumes
penaltyNights; section 2.4 consumes up to the policy and releases the rest. Current
policy.go caps penalty by frozen covered nights, not exact approved quantity.
Authorization Consume deliberately rejects over-consumption, and accommodation gateway
Consume explicitly refuses silently taking less because its recorded penalty would then
disagree with the ledger. These sources establish the inconsistency but do not authorize
silently redefining penalty quantity or rounding approved fractional service quantities.

Existing cancellation penalty/released-night fields and no-show consumedNights are integers.
A fractional cap needs an honest exact consumed-service/ledger representation; recording
1 consumed night while consuming 0.5 would not be acceptable. Assess additive exact
projection/storage versus a documented distinction between policy nights and actual
entitlement consumption after the policy choice; no schema/API change is pre-authorized.

Fees are separate from entitlement quantity. Night-based cancellation sums frozen nightly
fee/payer/member shares; percent cancellation uses the frozen member amount. No-show fee
also uses the frozen member amount and remains member-paid under current policy. The
assessed fee feeds later settlement. Do not proportionally reduce fees, transfer payer
share to the member, alter published terms or change downstream claim/settlement amounts
as an incidental ledger correction.

## Concrete options requiring a decision

**A — Keep fractional lodging approvals and cap entitlement penalties.** Consume the lesser
of the policy's penalty service quantity and the authorization's exact remaining approved
service quantity, then release all unused mapped units. Preserve the frozen published fee
and payer/member split; make policy penalty versus actual entitlement consumption explicit
and exact in preview/record/reporting. Apply prospectively to cancellation/no-show commands
for existing fractional confirmed bookings as well as new ones; never rewrite completed
historical movements or existing fee rows. This fixes both demonstrated stuck cases, but
requires explicit acceptance of cap semantics and the exact-reporting contract.

**B — Require whole-night quantities for future lodging approval.** Keep generic health
and other service review decimal quantities unchanged. Reject fractional lodging approval
before commitment with a clear contract and UI message. This prevents new fractional
bookings but does not repair the two existing fractional cases, and integer partial
approval can also leave a policy penalty above the approved amount. A separate explicit
existing-booking resolution/cap policy is therefore still necessary. No retroactive
round-up/down, authorization increase, automatic reserve or administrative rewrite follows
from selecting this option.

Recommended decision to present: A, with unchanged published monetary terms and exact
reporting, because it resolves already-supported partial approvals without narrowing generic
review. It is a recommendation, not authorization. If the owner also wants whole-night
future approvals, that may be layered later but cannot substitute for the existing-booking
resolution decision. Alternative fee or payer-share treatment is a distinct financial
policy decision requiring its own specification and acceptance evidence.

## Work while the gate is pending

Keep the proved refusal/conservation diagnostics and complete handoff/checkout acceptance.
Do not replace authorization Consume's over-consumption guard or add a catch-and-cap
fallback. Once a decision arrives, Astra specifies the bounded affected API/storage/UI
contract and Sol implements/tests real cancellation/no-show transactions, exact factor
conversion, unchanged preview/fees, retries, tenant/account isolation and final zero
reserved quantities. Include existing fractional bookings and integer partial approvals,
not only freshly configured fractional fixtures. No local DB, server, migration or live
compensation action is authorized by this planning note.

## Independent cancellation reporting correction (2026-10-10)

Astra separated a reporting defect from the pending business-rule decision. A two-night
hold at factor 2, approved for 0.5 service night, already cancels freely and returns one
entitlement unit. Its old preview/result said two nights were returned. Source `2504ff5`
corrects that successful cancellation's representation without changing settlement policy.

Cancellation preview/result/record now carry optional `entitlementEffect` with the four
exact service-night and entitlement-unit strings described below. Preview is prospective;
the command measures actual Consume/ReleaseUnused service returns and original reservation
counter deltas while holding authorization, item and reservation locks in that order.
Evidence requires the same tenant, request/person, single matching service item and
original BOOKING reservation. Only provable NIGHT evidence qualifies. Missing or ambiguous
evidence omits the projection and preserves existing safe settlement. Excessive penalties
still return the frozen fee preview without an effect and still refuse in the command.

Migration `000059_accommodation_cancellation_entitlement_effect.up.sql` adds four nullable,
nonnegative, all-or-none numeric(20,6) cancellation columns. It inserts evidence with the
append-only record and leaves historical rows NULL; it adds no no-show columns or grants.
The legacy `releasedNights` is the whole portion of actual released service when evidence
exists. Policy `penaltyNights`, monetary fee and payer/member shares retain frozen values.

The member renders service nights and entitlement units separately as exact server
strings. The immediate completed command shows its recorded effect in past tense.
Booking GET does not include the cancellation record, so a reload uses neutral missing-
evidence wording rather than calling the record old or inventing a movement. Persisted
evidence is verified through the repository and SQL; historical member read is not claimed.
The redundant cancellation success toast is removed so mobile tabs remain unobstructed.
A paid command still shows its recorded member fee inline, separately from entitlement
movement; the final focused member suite passed 16/16 after this regression correction.
The free-cancellation captures do not claim visual proof of the paid branch.

Local Go compilation/pure tests and scoped vet passed with database tests disabled.
Focused frontend tests passed 80/80; the whole frontend passed 761/761 in 92 files with
maxWorkers=2 (357.99 seconds). The initial concurrent run had 13 timeouts; lower worker
concurrency passed without increasing assertion timeouts. Workspace typecheck, lint and build passed. Spectral
reported zero errors and 11 existing warnings; oasdiff reported no breaking changes.
The old local golangci-lint could not read Go 1.27 export data, so current CI owns that
check. Synthetic UI review captured ten 390/1440 views with all 42 API requests intercepted,
zero unexpected requests/browser errors/overflow and visible keyboard focus. The single
detector reported no findings; fresh finish review scored both fixes resolved, disposition
ship for those fixes. These captures are not live business acceptance.

[CI 38058250522](https://github.com/celikbros/kapsora/actions/runs/38058250522) is the
isolated SQL/source acceptance run for final source `3051466` (the HTTP fixture mounts
the production cancellation routes). All six jobs passed. Its eight-function preflight
passed without skips (10.403 seconds) and covers public HTTP
preview/result, persisted history, frozen fee shares, factors 0.5/2, six-decimal cumulative
rounding, wrong provenance and recomputation after prior consumption/reviewer approval.
The restored old result projection compiled and reached the expected
CANCELLATION_EFFECT_HTTP_RESULT assertion with truthful underlying ledger movement. The three policy-refusal diagnostics
remain required expected failures, not passing capped settlements.

Full accommodation passed 110 functions (159.371 seconds), with only the three separately
exercised opt-in policy diagnostics skipped. Schema passed 192, authorization application
31, eligibility 20 and directory/invitation 70, all without skips. Handoff 9, checkout 3,
NIGHT conversion 14 and enrollment 6 passed without skips. Web passed 762 tests in 92
files and 25 mock browser smoke cases; 106 opt-in live/calendar cases remained skipped.
The current CI lint, security, generated contract and static binary checks all passed.

The schema preflight also passes fresh version 59, the bounded 57-to-58 IAM upgrade and
58-to-59 cancellation history preservation. It checks complete/nonnegative evidence and
append-only rejection of later update/delete. The older IAM test explicitly upgrades only
to 58, retaining its original state-preservation assertion.

Local schema remains 57. Applying migrations 58 and 59 locally requires the separate
explicit migration approval; operator-owned processes are untouched. No cap, no-show
effect, fee redistribution, settings authority or historical compensation is authorized
by this independent reporting correction.

## Conditional implementation design (Astra, reviewed by Sol, 2026-10-10)

This prepares option A for review. Explicit policy approval remains pending; technical
preparation does not authorize implementation.

Exact capped settlement is bounded to provable NIGHT-mapped authorization quantities.
MONEY and other entitlement-unit paths retain existing behavior and remain outside this
package's acceptance; missing evidence cannot silently route another unit through NIGHT
conversion.

For a provable NIGHT authorization-backed confirmed booking, add a narrow authorization-
owned settlement operation accepting the caller's tenant transaction. Lock the authorization,
matching item and original reservation. Compute exact consumed service quantity as the
lesser of frozen policy penalty and approved remaining quantity; then release all unused
entitlement. Keep generic Consume's over-consumption refusal. Booking status, inventory,
settlement evidence, audit and existing outbox effects commit or roll back together.

Return actual service and entitlement-unit deltas from the locked reservation/ledger
movements. Quantity multiplication rounds at six decimals; multiplication alone and
ReleaseUnused's service return do not establish actual ledger effects after prior release,
closed reservations or idempotent replay. Verify no unused reservation remainder remains
before reporting a successful authorization-backed terminal settlement. Do not manufacture
movements for a no-op or record policy quantities as actual ledger consumption.

### Additive exact contract and persistence

Extend the independently implemented cancellation entitlementEffect to capped settlement
and add matching evidence to confirmed no-show reports, with four canonical decimal strings:

- consumedServiceNights
- releasedServiceNights
- consumedEntitlementUnits
- releasedEntitlementUnits

Cancellation already has four nullable numeric(20,6) columns in migration 59. Add matching
no-show columns only after the cap decision, with nonnegative/all-or-none nullability. Cancellation effects
are written on initial insertion; no-show effects are written atomically during confirmed
review. Historical rows and undecided/rejected/disputed reports retain NULL, meaning no
recorded exact evidence rather than zero. Do not require historical confirmed rows to have
new evidence or backfill them from current authorization state. A forward migration is
needed; numbering is allocated at integration and local application remains separate.

Keep integer penaltyNights as the frozen policy/financial charge. When exact evidence is
present, legacy releasedNights and consumedNights represent the whole-number portion of
actual service effect. This is lossy compatibility: document it explicitly in OpenAPI and
make the exact fields authoritative in member/backoffice views and mocks. For example,
0.5 released service night at factor 2 means one returned entitlement unit, not zero;
the legacy integer is zero but must not drive the displayed exact return.

### Preview, release compatibility and money

Cancellation preview reads current tenant-scoped authorization remaining quantity, stored
factor and reservation evidence without writing. Its exact effect is prospective; the
command recomputes under locks. Unchanged state must match preview/result. Do not reuse
stale preview quantities after authorization state changes or derive them from original
coveredNights. Use exact decimal helpers in mocks rather than JavaScript floating point.
The frozen-policy monetary preview stays unchanged.

Mandatory new exact reporting is bounded to provable authorization-backed confirmed
settlements. Pending/unfunded or unprovable legacy bookings without an adopted authorization
retain their existing safe release path. Omit optional exact effect where exact service
evidence cannot be established; do not invent a factor or block safe release solely to
populate reporting. New terminal commands on existing fractional confirmed bookings use
their persisted authorization factor; completed history is neither recalculated nor repaired.

Cancellation fee/payerFee/memberFee and no-show assessed fee/payer/member amounts remain
exactly as frozen-policy calculation produces them. GetBookingCancellationForClaim and
GetBookingNoShowForClaim select money/status/free only; retain their projections and prove
identical downstream claim amounts. A capped entitlement quantity must never incidentally
reprice the charge or transfer its payer/member shares.

### Bounded ownership and acceptance

Implementation affects accommodation cancellation/no-show settlement and ports, the
internal authorization settlement operation, accommodation cancellation queries/PostgreSQL
mapping, one forward migration, OpenAPI/generated Go/TypeScript contracts, member lodging
words, backoffice booking detail, translations and exact mocks. No new grants, public
administrative correction tool, generic Consume rewrite or historical scan is included.

Acceptance must cover both proved fractional refusals and integer partial approvals;
factors 1/2/0.5 and cumulative six-decimal rounding; existing confirmed fractional bookings;
free cancellation; matching prospective preview; actual movement deltas; uncertain retries,
rollback and zero terminal reserve; historical exact-evidence omission; unchanged financial
claim projection; and safe no-authorization release without a new factor-evidence gate.
Run SQL only in isolated CI. Update ordinary integer projections and UI tests as contract
changes, never by recording an invented whole night for fractional consumption.
