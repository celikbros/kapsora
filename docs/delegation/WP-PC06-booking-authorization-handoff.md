# WP-PC06 · Retire a booking authorization when release wins the handoff

| Field | Value |
| --- | --- |
| Status | ISOLATED ACCEPTANCE COMPLETE on bda7984; runtime/owner gates retained |
| Planned | 2026-10-10, gpt-6-astra |
| Scope | Subscriber authorization commit versus booking release; deterministic replay cleanup |
| Migration / grants / public API | None planned |

## 1. Source and proof gate

Read the delegate handbook and the current checkout/conversion acceptance checkpoints.
WP-I6-02 requires release/expiry to return the room and entitlement; WP-I6-03 cancellation
must leave no usable booking promise. The existing bookingdecision.go subscriber promises
at-least-once idempotency and ignores an approval after a booking was released.

The concrete split is approveBooking: CreateForRequest commits its own authorization
transaction, then accommodation locks the booking in a second transaction. If ReleaseHold
wins between them, the later status guard returns nil. The booking is CANCELLED and its
reservation released, but the separately committed authorization can remain ACTIVE and
unlinked. No voucher is issued in this particular interleaving. Redelivery's early
non-held-status exit currently cannot retire it.

Direct ConfirmBooking does NOT call CreateForRequest; it submits/links the reservation
request. The authorization handoff in scope is the decision subscriber. Do not restructure
direct confirmation under the mistaken premise that it contains this external call.

Require the deterministic booking_handoff_race_test.go barrier after committed
CreateForRequest, real ReleaseHold completion, then resumed subscriber. The opt-in
isolated diagnostic must reach HANDOFF_ORPHAN_ACTIVE_AUTHORIZATION with otherwise correct
released ledger/inventory state. Its failure is defect reproduction, not acceptance.
If that assertion is not reached, investigate fixture/path behavior before implementation.

## 2. Minimal correction and transaction boundary

After reproduction, add a narrow internal application operation for retiring only the
proven orphan created by this booking's authorization key. There is no existing
CancelInTx: public authorization.Cancel opens its own transaction; intx.go has voucher
helpers but no cancellation helper. Do not call public Cancel while holding a booking lock.
Use an authorization application port that accepts the caller's tenant transaction, and
reuse module-owned status update/audit/voucher primitives with explicit provenance checks.
Never update authorization tables directly from accommodation infrastructure.

Accommodation locks/rechecks the booking before deciding the outcome:

- Held/pending booking: continue the existing successful handoff under current guards.
- Confirmed/checked-in/completed booking linked to the same authorization: normal replay;
  do not retire it. No generic non-held-status cleanup.
- CANCELLED or EXPIRED booking whose pending hold was released and which has no linked
  authorization: find the authorization by the deterministic booking key and retire only
  the exact proven orphan. Use this path both after CreateForRequest returns and on event
  redelivery before the current early terminal-status exit. The terminal path must never
  invoke CreateForRequest merely to discover whether an authorization exists.

The internal retirement operation checks tenant, key, request ID, person, authorization
identity where known, single lodging item/service, the exact booking reservation ID and
its BOOKING reference/booking ID. Verify it is an unused orphan: no consumed service or
ledger units, no competing booking authorization link, and the original reservation has
no remaining held quantity after the terminal release. Do not cancel by key prefix alone,
by person alone or by a supplied arbitrary authorization ID. Preserve exact original
reservation/account identity and quantity; do not reserve, consume or release again.

For the expected unused ACTIVE orphan, transition authorization to CANCELLED using its
locked current version and record module-owned cancellation audit with a stable reason
for the aborted booking handoff. Revoke any live voucher through the existing module
primitive in the same transaction; the reproduced path should have none. Reject unexpected
redeemed/fulfilled evidence rather than reversing history. Already cancelled matching
orphans are idempotent successes after provenance checks; no new audit/movement. Absence
of an authorization is a clean terminal outcome. Other unexpected state/ownership must
not be silently treated as successful cleanup.

Booking lock, provenance validation, retirement and audit commit together. Retain existing
lock ordering and verify it against terminal release paths; do not hold any booking or
inventory lock across CreateForRequest or another transaction-opening operation. Transient
read/write failures must propagate for outbox retry. On retry, find the same persisted
key even if the previous delivery died after external commit or retirement failed. Do not
use an in-memory flag or depend on retaining the returned authorization ID across retries.
Permanent malformed provenance requires an explicit diagnosable refusal; it never permits
cancelling an unrelated promise. No new sweeper or broad compensation framework is needed.

This is a forward event-handling correction, not an orphan scan/recovery tool. Existing
already-confirmed authorization lifecycle and historical consumption stay unchanged.

## 3. Required isolated evidence

Convert the reproduced race into a normal regression after correction and retain a
runner-only restored-source control proving the original marker failure. One final full
CI can cover passing normal tests and expected old-source failure; no duplicate old-only
full run is required.

- Release wins after committed authorization: booking stays CANCELLED, matching unused
  authorization becomes CANCELLED, reservation stays released, no voucher becomes usable,
  one original RESERVE/RELEASE pair, zero consumption, conserved account and freed inventory.
- Retry the identical delivery after cleanup: same authorization, no additional audit,
  voucher, inventory, reserve or release effects.
- Simulate process interruption after authorization commit before booking transaction;
  terminal release and fresh delivery must find/retire the persisted orphan by provenance.
- Inject one cleanup transaction failure: it rolls back and returns retryable error;
  next delivery completes retirement without historical ledger changes.
- Successful concurrent confirmation wins instead: linked authorization remains ACTIVE;
  another delivery does not revoke it. Confirmed and later completed replay are preserved.
- Wrong tenant/request/person/reservation/service/key evidence is refused with no mutation;
  absent authorization is benign. A consumed or redeemed promise is not silently cancelled.
- EXPIRED terminal outcome gets equivalent handling where an existing supported path can
  produce it; do not manufacture a new pending-expiry policy to make a fixture pass.
- Existing uncertain authorization-commit retry, mapped partial approval, contract binding,
  enrollment scope and checkout/conservation tests continue to pass.

Use deterministic channels/barriers and real production authorization persistence, not
sleep-based races or a fake that omits the first committed transaction. Report status,
links, ledger quantities/movement counts, vouchers and audit records separately. Expected
negative-control failures must be at the intended orphan-status assertion.

## 4. Limits and delivery

Likely files: accommodation/application/bookingdecision.go, bookingports.go and gateway;
authorization/application/intx.go or a focused internal helper with repository/query
support only where existing reads cannot establish provenance; focused race/guard tests.
Regenerate sqlc if SQL changes; no migration is presumed. Keep public contracts unchanged.

No local DB tests/writes, grants, server restart, live cleanup, generic cancellation
rewrite, broad saga, unrelated status race fix, historical orphan inventory or recovery.
No new user-facing action or permission. Local Go tests blank
KAPSORA_TEST_ADMIN_DATABASE_URL. Isolated race acceptance does not certify every possible
cross-module transaction interleaving or close the operator/owner/calendar gates.


## 5. Final isolated acceptance (2026-10-10)

The race diagnostic on `03bfa6c` reached the compiled
HANDOFF_ORPHAN_ACTIVE_AUTHORIZATION assertion in
[CI 37993738359](https://github.com/celikbros/kapsora/actions/runs/37993738359), after
real cancellation released the reservation and inventory. Production correction
`bda7984d3d78f2c217b826ca32b172f3905d7a46` then passed all six jobs in
[final CI 37995074957](https://github.com/celikbros/kapsora/actions/runs/37995074957).

Nine ordinary TestBookingHandoff functions passed without skips in 10.365 seconds:
release after committed authorization, transient confirmation-status failure/retry,
interrupted delivery, retirement write failure/rollback/retry, wrong provenance, no
existing authorization, permanent bad provenance, consumed/redeemed refusal and
confirmed-then-cancelled decision replay. The runner-only old terminal no-op control
compiled and reached the original orphan assertion; source was restored before later
regressions. Deterministic channel barriers replace timing sleeps.

The internal authorization operation accepts the caller's tenant transaction. Booking
lock, exact provenance checks, versioned cancellation, BOOKING_HANDOFF_ABORTED audit and
module-owned live-voucher revocation commit atomically. A matching cancelled orphan is
idempotent; no second reservation, consumption or release is created. Malformed private
v3 conversion refuses rather than falling back to factor 1. Unexpected consumed or
redeemed evidence is not reversed. Already-linked booking replay remains unchanged.
No public contract, schema, permission or local database was changed by this package.

The same final run passed 103 accommodation functions (148.177 seconds), with only two
explicit opt-in penalty diagnostics skipped in that ordinary suite. Both diagnostics
were separately exercised and reached their intended refusals; they do not certify a
working partial-approval penalty flow. Checkout passed three functions, NIGHT conversion
14, eligibility 20, authorization 31, schema 191 and directory/invitations 70 without skips.
Web passed 756 tests in 92 files and 25 mock smoke cases; 106 live/calendar cases stayed
opt-in/skipped. Existing payer, frozen-policy, enrollment and conversion controls passed.

This certifies the reproduced cancellation handoff and listed guards, not every
cross-module interleaving or a separately forced pending-expiry lifecycle. No historical
orphan scan, live cleanup or operator restart occurred. Settings/penalty decisions, local
migration 58, owner acceptance and retained October 15/30 observations remain separate.
