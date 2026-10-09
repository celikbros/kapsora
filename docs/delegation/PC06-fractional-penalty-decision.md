# PC06 · Fractional penalty decision gate

Status: decision required; no implementation or permission policy authorized by this note.
Planned 2026-10-10 by gpt-6-astra. All six jobs passed in final
[CI 37995074957](https://github.com/celikbros/kapsora/actions/runs/37995074957) on bda7984.
Its separate opt-in diagnostic step reached both compiled assertions
FRACTIONAL_PENALTY_CANCEL_REFUSAL and FRACTIONAL_PENALTY_NOSHOW_REFUSAL at
2026-10-09 21:44:39 UTC (2026-10-10 Europe/Istanbul). These intended failures prove the
refusals, not a passing partial-approval penalty workflow. The normal full accommodation
suite passed 103 functions and skipped only these two opt-in diagnostics; free fractional
cancellation passed. The concrete cap/reporting decision was requested asynchronously
and remains unanswered; this successful CI does not authorize a policy change.

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
