# PC06 · Partial-approval penalty decision gate

Status: cap policy approved; cancellation/no-show source/UI implementation and isolated
technical validation are complete on `bcf0798`. All six jobs passed in
[CI run 38079516012](https://github.com/celikbros/kapsora/actions/runs/38079516012): SQL cap
preflight 11/11 without skips (7.741 s), with restored-public-projection and cap-removal
marker controls passing; schema 193 without skips (110.161 s), including migration-60
history preservation and constraints; accommodation 120 without skips (118.066 s), including
the three former diagnostics as positive cases; authorization 31, eligibility 20,
directory/invitation 70, NIGHT 14, checkout 3, handoff 9 and enrollment 6, all without
skips. Web passed 768 tests in 92 files plus 25 mock smoke cases; 106 opt-in live/calendar
cases were skipped. Earlier local web execution had eight timeouts (760/768 across four
files), followed by focused 34/34 at `maxWorkers=1`; latest CI confirms the full suite.
Typecheck, scoped ESLint/Prettier, pure Go tests and vet passed. Migration 60 adds optional exact no-show evidence and has been applied locally after explicit
owner approval. Schema 60 is clean. Pre/post original-field hashes and row counts matched across all 15 preserved
tables: accommodation.booking 18, cancellation 4, no_show 1; IAM access_grant 17, actor 15,
credential 15, permission 105, role 34, role_change_command_receipt 0, role_change_request
0, role_permission 378, session 52, tenant_invitation 0, tenant_invitation_create_receipt
0 and tenant_membership 15. Existing no-show exact-effect fields remain NULL; no business
commands, fee changes, grants or history rewrites occurred. Current operator checkpoint (2026-10-10 23:14 Istanbul): API PID 48468 and panel PID
31384 were started after the cap source commit; the API binary contains `SettleBookingPenalty`,
so the operator restart is complete. Thirteen unique read-only/boundary checks passed:
Management 9, shared-door 2 and two historical no-show GETs. Member/tenant-admin GETs passed
in 281/168 ms; the historical row correctly has no exact-effect evidence, frozen fee identity
is unchanged, and before/after reads matched. Shared-door member passed in 6.7 s; provider's
initial 429-banner attempt was followed by an isolated 5 s retry pass. No business commands,
grants, bookings, payments or no-show reviews were performed. No new paid-layout visual test
ran; the previous visual checkpoint is historical. No local capped-settlement fixture or
command was available, so capped settlement remains isolated-CI evidence only; no live
settlement is accepted.
No live capped settlement has been accepted. Final synthetic UI review covered 12 390/1440
views with all API calls stubbed, detector `[]`, zero errors/overflow/unknown requests, and
no blockers in independent Sol review; this is not live settlement acceptance. MGT-04
settings, formal owner acceptance and calendar gates remain separate; PC-06 is not fully
complete.

**Historical defect reproduction before owner approval:** all six jobs passed on `bda7984`
in [CI 37995074957](https://github.com/celikbros/kapsora/actions/runs/37995074957). Its opt-in
diagnostics reached the expected FRACTIONAL_PENALTY_CANCEL_REFUSAL and
FRACTIONAL_PENALTY_NOSHOW_REFUSAL assertions at 2026-10-09 21:44:39 UTC. Those expected
failures proved the old refusal behavior; the decision was then unanswered. Free fractional
cancellation passed, but no capped penalty workflow existed at that historical point.

**Historical integer partial-approval defect reproduction:** the diagnostic was accepted on
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

## Historical reproduction: proven cases and policy conflict

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

## Historical options considered before owner approval

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

Historical recommendation: option A was recommended with unchanged monetary terms and
exact reporting. The owner subsequently approved that option as recorded in the current
status above. Any alternative fee or payer-share treatment remains a separate policy decision.

## Historical handoff instructions before owner approval

These instructions describe the former decision-gated state and are superseded by the source
implementation and successful CI status above. The preserved acceptance scope includes exact
factor conversion, unchanged preview/fees, retries, tenant/account isolation, zero terminal
reserve, existing fractional bookings and integer partial approvals. Local migration
application and live business actions still require separate authorization.

## Historical independent cancellation reporting correction (2026-10-10)

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
CANCELLATION_EFFECT_HTTP_RESULT assertion with truthful underlying ledger movement. At that
historical run, the three policy-refusal diagnostics remained expected failures, not capped
settlements.

Full accommodation passed 110 functions (159.371 seconds), with only the three separately
exercised opt-in policy diagnostics skipped. Schema passed 192, authorization application
31, eligibility 20 and directory/invitation 70, all without skips. Handoff 9, checkout 3,
NIGHT conversion 14 and enrollment 6 passed without skips. Web passed 762 tests in 92
files and 25 mock browser smoke cases; 106 opt-in live/calendar cases remained skipped.
That historical CI run's lint, security, generated contract and static binary checks passed.

That schema preflight also passed fresh version 59, the bounded 57-to-58 IAM upgrade and
58-to-59 cancellation history preservation. It checks complete/nonnegative evidence and
append-only rejection of later update/delete. The older IAM test explicitly upgrades only
to 58, retaining its original state-preservation assertion.

**Historical migration status at this checkpoint:** local schema was 57 and migrations 58/59
were awaiting separate approval. The later local migration checkpoint records their approved
application; the operator API restart remains pending. No cap, no-show effect, fee redistribution,
settings authority or historical compensation is authorized by this independent reporting correction.

## Historical local migration checkpoint (2026-10-10)

The owner explicitly approved local migrations 58 and 59. Bounded `UpTo(59)` from schema 57
completed successfully; schema 59 is clean (`dirty=false`). Four nullable numeric(20,6)
cancellation-effect columns and the validated all-or-none constraint are present, along with
both IAM role-change tables. Before/after SHA-256 snapshots matched for all 11 existing tables:
accommodation.cancellation (4), iam.access_grant (17), iam.actor (15), iam.credential (15),
iam.permission (105), iam.role (34), iam.role_permission (378), iam.session (52),
iam.tenant_invitation (0), iam.tenant_invitation_create_receipt (0), and iam.tenant_membership
(15). Existing cancellation effects remain NULL. No business commands, grants or history rewrites
were performed. Operator API process 45580 was started 2026-10-09; its requested restart remains
pending, so this checkpoint makes no live API acceptance claim. At that checkpoint, the
penalty-cap decision and MGT-04 settings approval were unapproved.

## Historical post-restart bounded live checkpoint (2026-10-10 20:15 Istanbul)

The operator restarted `dev.ps1 up`; current API listener PID 27972. The retained paid
reimbursement layout check passed read-only against existing record
`01a0ee4d-97a9-77eb-8192-e993b3a6f955` (3.3 s): identical record before/after, zero
blocked writes, and the 390/1440 views looked correct. No payment or financial command ran.
The shared-door provider/member journey also passed in 12.1 s, including expected redirects
and logout in both tabs; it changed no financial state. The full bounded live set was 12
unique current-source cases, all passing: Management 9/9, retained paid layout 1/1, and
shared-door flow 2/2. No suspension, invitation creation, role grant, privileged request,
booking, payment or financial command was performed. DEMO_A inventory contains no
cancellable CONFIRMED/PENDING_APPROVAL booking, so no live exact-effect cancellation
preview or settlement fixture was available. At that historical checkpoint, the three
penalty-refusal diagnostics were isolated source/CI evidence; no successful capped
settlement had been claimed. Schema
remains 59, dirty=false. At that historical checkpoint, the cap decision, MGT-04 settings,
formal owner acceptance and Oct 15/30 calendar gates were still open.

## Approved implementation and current validation (Astra, reviewed by Sol, 2026-10-10)

The owner approved option A: consume the lesser of frozen policy penalty and exact approved
remainder, then release unused mapped units. Preserve the existing monetary fee and
payer/member split. Apply cancellation and confirmed no-show commands to existing confirmed
and new bookings; preserve completed historical movements and frozen money. Source/UI
implementation and isolated technical validation are complete on `bcf0798`; all six CI jobs
passed in [run 38079516012](https://github.com/celikbros/kapsora/actions/runs/38079516012).
The SQL cap preflight passed 11/11 without skips (7.741 s); the restored-public-projection
and cap-removal-marker controls passed. Schema passed 193 without skips (110.161 s), including
migration-60 history preservation and constraints. Accommodation passed 120 without skips
(118.066 s), including the three former diagnostics as positive cases. Authorization 31,
eligibility 20, directory/invitation 70, NIGHT 14, checkout 3, handoff 9 and enrollment 6
also passed without skips. Web passed 768 tests in 92 files plus 25 mock smoke cases; 106
opt-in live/calendar cases were skipped. Migration 60 adds optional exact no-show movement evidence and is applied locally after
explicit owner approval. Schema 60 is clean; pre/post row counts matched across all 15
preserved tables as detailed above. Existing no-show exact-effect fields remain NULL; no
business commands, fee changes, grants or history rewrites occurred. The latest operator restart and 13 read-only/boundary checks are recorded in the current
status above. No live capped settlement
has been accepted. The final synthetic UI review covered 12 390/1440 views with all API calls
stubbed, detector `[]`, zero errors/overflow/unknown requests and no blockers in independent
Sol review; this is not live settlement acceptance. MGT-04 settings, formal owner acceptance
and calendar gates remain separate; PC-06 is not fully complete.

Exact capped settlement is bounded to provable NIGHT-mapped authorization quantities.
MONEY and other entitlement-unit paths retain existing behavior and remain outside this
package's acceptance; missing evidence cannot silently route another unit through NIGHT
conversion.

For a provable NIGHT authorization-backed confirmed booking, the narrow authorization-
owned settlement operation accepts the caller's tenant transaction. Lock the authorization,
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

The cancellation entitlementEffect is extended to capped settlement, with matching exact
evidence on confirmed no-show reports in four canonical decimal strings:

- consumedServiceNights
- releasedServiceNights
- consumedEntitlementUnits
- releasedEntitlementUnits

Cancellation has four nullable numeric(20,6) columns from migration 59; migration 60 adds
matching no-show columns with nonnegative/all-or-none nullability. Cancellation effects are
written on initial insertion; no-show effects are written atomically during confirmed review.
Historical rows and undecided/rejected/disputed reports retain NULL, meaning no recorded exact
evidence rather than zero. Do not backfill from current authorization state. Migration 60 was
applied locally after explicit owner approval; schema 60 is clean.

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

Acceptance must turn the former fractional and integer partial-approval refusal cases into
capped settlements, with marker negative controls; cover factors 1/2/0.5 and cumulative
six-decimal rounding; and include existing confirmed fractional bookings;
free cancellation; matching prospective preview; actual movement deltas; uncertain retries,
rollback and zero terminal reserve; historical exact-evidence omission; unchanged financial
claim projection; and safe no-authorization release without a new factor-evidence gate.
Run SQL only in isolated CI. Update ordinary integer projections and UI tests as contract
changes, never by recording an invented whole night for fractional consumption.

## Current live closure preparation (2026-10-11)

The original lodging technical scope remains accepted; later capped cancellation/no-show
commands have isolated acceptance only. Current read-only inventory of both demo tenants
found no HOLD/PENDING_APPROVAL/CONFIRMED fixture. The attempted operator restart exposed
an API startup blocker: duplicate `/admin` mounts. Database-free reproduction and the
scoped `018e63d` fix pass local router/settings tests, vet and build; corrected-source
reload and its CI are separate gates. No live business action passed in this checkpoint.

The bounded live plan reuses DEMO_A's published NIGHT factor-1 terms and existing actors.
Cancellation approves 0.5 service night under the existing one-night policy, consumes 0.5
unit, releases no terminal remainder and preserves frozen first-night fee shares; prior
partial approval releases the other 2.5 units. Confirmed no-show approves 1.5 service
nights under a larger frozen policy and consumes exactly 1.5 units after the genuine
arrival window closes. Zero-fee no-show proves the cap only; a fee-bearing claim remains
unproven unless actual frozen money is positive. Tests retain private resume fixtures,
exact original command identities, ledger/inventory baselines and frozen money. They do
not alter published terms, grants, clocks, settings or old terminal records.

Formal owner review and retained hotel October 15 / health October 30 UTC reconciliation
remain separate from feature delivery. Keep the scheduler running in the exact windows
recorded in ROADMAP; pre-date skips are pending and no automatic backfill is claimed.

### Bounded opt-in live checker

The new [cancellation checker](../../tests/e2e/real-lodging-cap-cancellation.spec.ts) and
[no-show checker](../../tests/e2e/real-lodging-cap-noshow.spec.ts) require explicit opt-in
and separate persistent fixtures under ignored `tmp/`. Scoped TypeScript, ESLint,
Prettier and discovery pass; independent Sol review found no remaining material blocker.
Both cases skip without opt-in, verified without starting a server. Those two skips are
NOT live acceptance. Review corrected moving-date resume bounds, receipt-retention guards,
atomic fixture writes and document-ready/link polling before relying on a result.

After the operator reload and existing private environment loading, use the same private
files on every retry. Never remove or replace them to obtain a fresh booking:

```powershell
$env:E2E_REAL_API = '1'
$env:E2E_EXISTING_UI_URL = 'http://127.0.0.1:5181'
$env:E2E_LODGING_CAP_CANCELLATION = '1'
$env:E2E_LODGING_CAP_CANCELLATION_FIXTURE = Join-Path (Get-Location) 'tmp/pc06-cap-cancellation-live.json'
pnpm exec playwright test --config tests/e2e/playwright.config.ts real-lodging-cap-cancellation.spec.ts --project chromium --trace off --workers 1
$env:E2E_LODGING_CAP_CANCELLATION = ''

$env:E2E_LODGING_CAP_NOSHOW = '1'
$env:E2E_LODGING_CAP_NOSHOW_FIXTURE = Join-Path (Get-Location) 'tmp/pc06-cap-noshow-live.json'
pnpm exec playwright test --config tests/e2e/playwright.config.ts real-lodging-cap-noshow.spec.ts --project chromium --trace off --workers 1
$env:E2E_LODGING_CAP_NOSHOW = ''
```

The existing single-door URL suppresses Playwright's server launcher. Real no-show evidence
also needs the operator's MinIO/ClamAV and worker; do not bypass scanning. A natural-window
skip retains the same confirmed booking; its retry timestamp is only the documented-default
estimate, and the server's current window remains authoritative. An uncertain upload must
reconcile that same reserved document rather than upload another. A stale run lock needs
inspection of its owner before release. Neither pending path is passed acceptance.

Cancellation receipts normally expire after 24 hours. The checker preserves intent time,
original response and replay proof; it does not resend expired uncertain hold/cancel keys.
Later terminal reads can validate retained balances/ledger/inventory/claim, but unknown
response identity remains pending. Neither checker changes template grants, tenant settings,
frozen fees, accepted paid episodes or historical movements.

### Source acceptance checkpoint

All six jobs passed for `34c31ff` in [CI run 38102458276](https://github.com/celikbros/kapsora/actions/runs/38102458276): Go build/lint/unit, web lint/typecheck/unit/build/smoke, PostgreSQL 18 schema/acceptance, contracts, security and static binaries. Isolated PostgreSQL includes cap preflight/negative control, settings grants/seed/consumer acceptance and accommodation regressions. This is source acceptance; opt-in live cap commands did not run. Secret-safe API configuration checks pass, but API 8090, MinIO 9000 and ClamAV 3310 are unavailable. A read-only consumer query confirms DEMO_A late hours 24 and hotel timezone Europe/Istanbul, with no reusable HOLD/PENDING_APPROVAL/CONFIRMED booking in either demo tenant. The operator must restore the existing services before live testing; no new permission/policy approval is requested for the already-authorized bounded lodging scenario.
