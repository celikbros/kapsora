# WP-PC06 · Bind confirmation policy to the quoted contract

| Field | Value |
| --- | --- |
| Status | ACTIVE; isolated implementation pending |
| Planned | 2026-10-09, gpt-6-astra; legacy compatibility decision accepted by integrator |
| Outcome | New holds confirm under the actual first-night price contract's lodging terms |
| Migration / public API / permissions | None; internal quote JSON version changes only |
| Acceptance | Source and isolated synthetic tests authorized; rollout/live acceptance blocked on legacy inspection and recovery decision |

## 1. Existing requirement and defect

Read the delegate handbook, current HANDOVER/ROADMAP PC-06 checkpoints,
WP-PC06-contract-hold-duration.md and WP-PC06-contract-payer-boundary.md. The integrator
owns documentation integration and CI. No local database tests, migration application,
server control or live compensation is authorized. Local schema remains 57; do not apply
000058 or affect the shared application-role password.

Baseline v1.2 section 11.5 binds contract selection to tenant, payer/program, provider,
service, date and ranking. WP-I6-04 section 2.2 requires the confirmation policy from the
version effective on check-in for the property's provider and person's program. Existing
booking.go policySnapshot and accommodation_booking.sql GetContractVersionForRoomType
comments expressly intend the contract behind the first night's price. No new privilege
or financial-policy choice is introduced by retaining that identity.

quoteRoomTypeWithSelection now retains the actual first-night ContractVersionID, but
snapshotOf discards it. policySnapshot instead calls ContractVersionForProperty; its SQL
chooses provider-wide domain preference/latest effective date/ID without joining the
winning price or payer. Initial confirmation preflight and asynchronous approval both
call this helper. A different contract can therefore supply cancellation/no-show terms.

The preflight policy is discarded, not captured: SetBookingRequest writes the request ID,
MarkPendingApproval writes status/expiry, and only ConfirmBooking writes policy_snapshot
when approval becomes CONFIRMED. Ordinary legacy HOLD/PENDING_APPROVAL records therefore
have no stored policy to recover. REQUESTED is not a booking status: a requested booking
can be HOLD with service_request_id or PENDING_APPROVAL.

## 2. Exact metadata and common resolution

1. Write new internal QuoteSnapshot version 2 with an explicit
   FirstNightContractVersionID JSON field. Copy prepared.selection.ContractVersionID in
   snapshotOf; reject a missing selected identity before any hold booking, inventory or
   entitlement write. The ID must come from server price selection, never request input.
2. Keep DecodeQuoteSnapshot compatible with v1 for existing display, release, expiry,
   cancellation and financial consumers. Do not make all readers reject legacy snapshots.
   Add a separate confirmation-specific resolver that validates the snapshot version and
   required identity. New v2 snapshots require a nonzero valid UUID. Unknown versions and
   corrupt v2 identities are invariant failures, never requests for a v1/latest fallback.
3. Both confirm preflight and approveBooking use the same resolver and exact-version policy
   gateway. Remove the provider-wide latest-contract lookup from both execution paths.
   Terms missing on the selected version remain LODGING_TERMS_MISSING; another version's
   populated terms cannot repair them. Do not reprice or reselect the contract at approval.
4. Preserve the first-night choice across later-night version transitions. Frozen terms of
   the selected version remain authoritative even if another contract is published or the
   selected version is later retired; use the existing exact-version contract read behavior,
   not a new published/latest predicate.
5. Keep public transport projections unchanged: internal version/identity metadata does not
   become a request field or leak into the public quote shape. Preserve money, quantities,
   hold duration, quote TTL, step-up, permissions and all existing policy calculations.

## 3. Approval ordering and transaction discipline

approveBooking currently calls auths.CreateForRequest before reading policy under its
later booking lock. Authorization adoption commits in its own transaction and extends the
entitlement reservation. Replacing only the later lookup would create new committed side
effects before discovering unrecoverable legacy or malformed quote metadata.

Resolve the exact quote identity AND read its policy before CreateForRequest. Do this
outside inventory/booking locks and without opening nested transactions. Prefer using the
already-loaded BookingRecord, a bounded read transaction if room timezone is needed, then
the existing policy gateway after that transaction closes. Do not call a gateway that
opens its own transaction while holding inventory locks. Carry the resolved policy into
the existing booking mutation transaction rather than performing another policy fetch
there. Do not redesign authorization adoption or lock order.

After authorization, lock/re-read the booking as today. Terminal/released bookings remain
no-ops. Before confirming, check that the locked quote's validated version and first-night
identity still match the quote used by preauthorization resolution. A mismatch refuses the
booking write rather than attaching stale policy; it cannot undo already committed
authorization and must not claim to do so. Preserve existing immutable-quote assumptions,
idempotency and status guards. Do not add quote/contract reconstruction or automatic
compensation to this check.

Initial confirm must validate identity and terms before CreateReservation and SetBookingRequest.
Review its transaction boundary: move any required external policy read into a preparation
phase outside its write transaction, then lock/recheck the same booking and quote before
request creation. Preserve authorization/person/provider checks and re-evaluate mutable
status, deadline, quote TTL and step-up under the command's normal protection. A read phase
must never turn a previously inaccessible booking into a successful lookup or permit a
stale precheck to bypass the locked command guards. Keep the shared resolver independent
of transaction ownership so both paths can use it safely.

Remove obsolete ContractVersionForProperty interface, implementation and generated SQL if
scoped call-site inspection confirms no remaining callers; update fake implementations.
If retained temporarily, tests must prove neither confirmation path invokes it. Avoid new
ports where existing reads suffice. Regenerate sqlc for any query changes and compile all
affected fixture/fake callers.

AcceptWaitlistOffer is also a private confirm caller. Prepare its offered booking's exact
policy before the waitlist write transaction, then retain entry/person/provider access,
offer status, expiry and booking identity checks under the existing entry lock. Do not
alter queue order, offer duration or waitlist lifecycle policy as part of this dependency.

## 4. Deliberate legacy compatibility decision

The following source behavior is accepted for this package. It is not a claim that legacy
runtime recovery is complete.

| Record | Behavior |
| --- | --- |
| v1 HOLD, no request | Confirm refuses with existing QUOTE_STALE before request creation; explicit release/search and normal HOLD expiry remain available |
| v1 requested HOLD or PENDING_APPROVAL | Approved decision fails permanently before any new authorization adoption, inventory transition or voucher write |
| Existing confirmed/history v1 | Read and retain existing quote/policy unchanged; terminal worker redelivery remains the existing no-op |
| Legacy held row with an unusual preexisting policy | No provenance inference or special confirmation exemption; do not reconstruct quote identity from it |
| v2 missing/malformed identity or unknown version | Safe invariant refusal; no legacy, latest-contract or matching-amount fallback |

Use a distinct internal legacy-identity error so HTTP confirmation maps it deliberately to
QUOTE_STALE and the async handler wraps it with outbox.Permanent. Do not classify transient
database/gateway failures as permanent; preserve missing-terms semantics. Ordinary errors
retry and eventually dead-letter; missing immutable legacy identity cannot be repaired by
retry. Fanout may still retry if another subscriber has a retryable failure, so repeated
delivery of this refusal must remain free of new side effects.

Do not automatically cancel/reject an approved request, edit snapshots, select a current
contract, release inventory, or compensate authorizations as a compatibility shortcut.
Existing rejected-request handling and explicit release/cancellation commands stay intact.

## 5. Rollout gate and material proof limits

Rollout/live acceptance is blocked until a separately authorized, bounded GET-only legacy
inventory inspection and an explicit recovery decision establish how outstanding records
will be handled. This package authorizes no such live action and no live compensation.

ListExpiredHolds and ExpireBooking handle HOLD only. PENDING_APPROVAL does not automatically
expire after its 72-hour timestamp. The member pending page currently renders waiting text
without a release/cancel action. APIs allow release/free pending cancellation, but this is
not an automatic or visible member recovery path. A permanent refusal can therefore leave
pending room inventory and a waiting member until deliberate recovery. Do not describe
dead-lettering as completed recovery or promise that the expiry sweep resolves pending rows.

Old failed attempts may already have committed authorization adoption before the old policy
lookup failed. The new ordering prevents new adoption on identity refusal; it neither finds
nor repairs existing orphaned authorizations. The existing race between external
authorization adoption and the later booking status recheck is likewise not solved here.
Do not claim a new cross-module atomicity or compensation guarantee.

Multi-enrollment coherence remains separate: this patch preserves the actual quote winner;
it does not align differing enrollment selectors or expand the payer-filter proof. Pending
expiry policy and member recovery UX are separate decisions, not incidental fixes.

## 6. Required isolated evidence

Use actual PostgreSQL-backed booking/HTTP fixtures in isolated CI, synthetic draft terms
published normally, and deterministic clocks. Pure resolver tests supplement, not replace,
end-to-end application/repository proof. No published real contract or live booking is altered.

| Case | Required assertion |
| --- | --- |
| Exact first-night contract | A wins the first night's price while same-provider B wins the old broad lookup and has different cancellation/no-show terms; both preflight and async completion use A |
| Same-payer distractor | A/B share a payer so payer filtering alone cannot make the regression pass; the old lookup fails the test |
| Different-payer distractor | New payer-filter boundary remains; another payer's policy cannot supply confirmation terms |
| Version transition/publication | Later-night version and unrelated publication after hold never replace the first-night identity; totals remain unchanged |
| Selected version retired | Frozen selected-version terms remain authoritative according to the existing exact-version read; no latest fallback |
| Missing selected terms | B has terms but A does not; preflight refuses before request creation and direct async path refuses before authorization adoption |
| Valid v2 persistence | Actual booking quote JSON contains version 2 and selected identity; public transport output has no new internal fields |
| Identity validation | Nil UUID, malformed UUID/JSON, missing v2 field and unknown versions refuse safely; no booking writes at hold creation for absent selected identity |
| Legacy fresh HOLD | Existing QUOTE_STALE response; zero reservation request, authorization, voucher or inventory change from confirmation |
| Legacy requested HOLD/pending | Permanent failure, including repeated delivery, before auth port call/adoption; no new authorization, voucher, booking status or inventory change |
| Historical compatibility | v1 read/render, release, HOLD expiry and confirmed policy/cancellation consumers remain usable; terminal async redelivery does not validate/rewrite old provenance |
| Pending compatibility limit | Test records the deliberate no-write refusal; do not pretend the normal expiry sweep closes PENDING_APPROVAL |
| Locked identity/status recheck | Changed identity cannot be confirmed under prepared policy; released/terminal booking is not revived; document the existing external-adoption race rather than claiming compensation |
| Transient errors | Policy read failure before adoption remains retryable where appropriate; successful retry uses the same exact version |
| Replay/lifecycle | Delayed approval and redelivery preserve one adoption, one inventory transition and existing voucher behavior; quantities and financial totals are unchanged |

Test both public preflight and asynchronous final write, not only a helper returning a UUID.
Assert no-effects using database counts/ledger state and auth-port call recording where
appropriate. Include a mutation/negative control restoring the broad lookup so exact
contract assertions demonstrably fail. Existing booking oversell/expiry/adoption and
cancellation/no-show scoped regressions remain required.

Run focused pure tests and package compilation locally with database tests explicitly
disabled; verify formatting, generation and scoped vet/lint. Integrator owns required
isolated PostgreSQL/CI runs. Report exact commands, test names, skips and code head. No
runtime or all-legacy acceptance claim follows from green isolated tests.
