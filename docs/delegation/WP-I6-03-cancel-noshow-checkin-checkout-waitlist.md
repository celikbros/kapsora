# WP-I6-03 · Cancellation by policy snapshot, no-show, check-in and check-out, waitlist

| Field                      | Value                                                                                                                                                                                                                                                             |
| -------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M6 (plan increment I6)                                                                                                                                                                                                                                            |
| Size                       | L                                                                                                                                                                                                                                                                 |
| Depends on                 | WP-I6-02 (booking, voucher, reservation adoption), WP-I6-04 (cancellation terms, the policy snapshot), WP-I4-02 (`redeemVoucher`, `Consume`, `ReleaseUnused`, fulfilment), WP-I4-04 (documents, for no-show evidence), WP-I4-03 (queues, maker-checker)          |
| Runs in parallel with      | WP-I6-05 (mock first)                                                                                                                                                                                                                                             |
| Migration numbers assigned | `000042_accommodation_cancellation_noshow_waitlist.up.sql` (renumbered from 000038; the number must exceed 000041)                                                                                                                                                                                                        |
| OpenAPI operations owned   | `cancelBooking`, `previewCancellation`, `checkInBooking`, `checkOutBooking`, `reportNoShow`, `reviewNoShow`, `listWaitlist`, `joinWaitlist`, `cancelWaitlistEntry`, `acceptWaitlistOffer`                                                                        |
| Read first                 | v1.2 10.6 step 8, 10.7 (verbatim), 12.3, 16.7 (`waitlist_entry`, `no_show`), 35.2; WP-I5-03 §2.4 (release at discharge, across every hold), `internal/health/application/inpatientstay.go` `releaseUnusedDays`; WP-I4-02 fulfilment; WP-I4-03 §2.4 approval policy |

## 1. Goal

What happens after the promise: the member cancels, or does not come, or comes and leaves
early; and the person behind them in the queue gets the room. Three rules carry the
package: **a cancellation is judged by the policy the booking was confirmed under, frozen
on the booking, and never by today's contract**; **every fee and every release is a
ledger movement with a reversing entry, and nothing is deleted**; **a no-show is the
provider's claim with evidence, reviewed before it costs the member anything.**

## 2. Scope

### 2.1 Schema (migration 000042)

`accommodation.no_show`: id, tenant_id, `booking_id` (unique), `reported_by_actor_id`,
`reported_at`, `evidence_document_id` NULL (WP-I4-04 link, `aggregate_type = 'BOOKING'`),
`assessed_fee_amount`, `payer_amount`, `member_amount` (exact decimals, `payer + member =
assessed` CHECK), `currency_code`, `status` (`REPORTED`,`CONFIRMED`,`DISPUTED`,`REJECTED`),
`reviewed_by`, `reviewed_at`, `review_comment`, row_version.

`accommodation.waitlist_entry`: id, tenant_id, person_id, enrollment_id, `property_id`,
`room_type_id` NULL (any room of the property when null), `check_in`, `check_out`, `adults`,
`children`, `priority int` (0 default; a plan may raise it), `status` (`WAITING`,`OFFERED`,
`ACCEPTED`,`EXPIRED`,`CANCELLED`), `offered_booking_id` NULL, `offer_expires_at` NULL,
`created_at`, row_version. Partial unique `(tenant_id, person_id, property_id, check_in)
WHERE status IN ('WAITING','OFFERED')`; the queue order index is `(tenant_id, property_id,
room_type_id, priority DESC, created_at ASC)`.

`accommodation.cancellation`: id, tenant_id, booking_id, `cancelled_at`, `cancelled_by`,
`reason_code`, `policy_snapshot jsonb` (copied from the booking, so the row proves what it
was judged by), `free` boolean, `penalty_nights`, `fee_amount`, `payer_fee`, `member_fee`,
`released_nights`, `currency_code`. Append-only.

### 2.2 Cancellation by snapshot

`previewCancellation` answers, for a `CONFIRMED` (or `PENDING_APPROVAL`) booking and a
moment, what a cancellation now would cost: `free`, `penaltyNights`, `feeAmount`,
`payerFee`, `memberFee`, `releasedNights` — from the booking's `policy_snapshot` (WP-I6-04
§2.2: hours-before-arrival for free cancellation, then a penalty as nights or as a percent
of the member amount, and the no-show rate), evaluated in the property's zone. The member
sees this before they confirm the cancellation; the command returns the same figures.

`cancelBooking` in one transaction: refuses after check-in; writes the `cancellation` row;
releases the inventory of every night (`confirmed −1`, or `held −1` while pending) under the
lock order; on the entitlement, releases `nights − penaltyNights` through WP-I4-02's
`ReleaseUnused` across every hold the booking stands on and **consumes `penaltyNights`**
(the plan paid for the room the member did not use; the ledger says so with a CONSUME whose
reason is `CANCELLATION_PENALTY`); the money fee is recorded on the row for M7's
settlement, never charged here (KAPSORA holds no card, v1.2 10.6 step 6); status
`CANCELLED`; the voucher retired. Running it twice is one cancellation.

A cancellation of a `HOLD` is `releaseHold` (WP-I6-02); of `PENDING_APPROVAL`, the request
is cancelled through WP-I4-01 and the hold released.

### 2.3 Check-in, check-out

`checkInBooking` (provider at the property, `accommodation.booking.manage`, location scope):
takes the voucher token (typed or scanned), redeems it through WP-I4-02 (`redeemVoucher`;
digest match, window `[check_in − tenant setting accommodation.checkin_early_hours, check_in
+ accommodation.checkin_late_hours]` in the property's zone), status `CHECKED_IN`. A token
that does not match this booking is 404, never "wrong booking".

`checkOutBooking`: `checked_out_at`, `actual_nights = max(1, nights actually stayed)`
computed in the property's zone on the calendar day, the fulfilment written through
WP-I4-02 for `actual_nights` (consumes them), the unused nights released across the
booking's holds exactly as a discharge does (WP-I5-03 §2.4, `releaseUnusedDays`), the
inventory of the unused nights freed (`confirmed −1`), status `COMPLETED`. An over-stay
(`actual_nights > nights`) is flagged `over_booking = true` for the claim (M7) and consumes
nothing beyond what was authorized.

### 2.4 No-show

`reportNoShow` (provider, after `check_in + late hours`, booking `CONFIRMED`): evidence
document required (a clean WP-I4-04 document linked to the booking), the assessed fee from
the policy snapshot's no-show rate, status `REPORTED`; the booking itself stays `CONFIRMED`
until review. `reviewNoShow` (`accommodation.booking.manage` on the payer side — a role the
provider does not hold; the same person may not report and confirm, checked as in WP-I4-01's
approval): `CONFIRMED` → booking `NO_SHOW`, inventory freed, nights consumed as
`NO_SHOW_PENALTY` up to the policy, the rest released, fee recorded for M7; `REJECTED` →
booking stays `CONFIRMED` and may still be cancelled or checked in; `DISPUTED` raises a work
item in the payer's review queue.

### 2.5 Waitlist

`joinWaitlist` (member for their own person, or a desk): the entry, FIFO within priority.
The offer job (scheduler, every five minutes, and on every release): for each property and
room type, walk `WAITING` entries in queue order; when every night of an entry's range has
`available ≥ 1`, create a hold on the entry's behalf (WP-I6-02's `createHold`, with the
member's quote), mark the entry `OFFERED` with `offer_expires_at = hold_expires_at`, and tell
the member (WP-I6-04's `booking.offered`). `acceptWaitlistOffer` confirms that hold; an
offer nobody accepted expires with its hold and the entry returns to `WAITING` behind those
who were waiting when it was offered (its `created_at` moves to now). `cancelWaitlistEntry`
by the member.

### 2.6 Permissions

`accommodation.booking.manage` covers check-in/out and no-show reporting at the property and
the review on the payer side; the two sides are told apart by scope (ORGANIZATION grant vs
none), and a test proves a provider cannot review its own report. `accommodation.waitlist.manage`
(new, NORMAL; PROVIDER_RESERVATION, PROGRAM_MANAGER) for the desk; a member joins under
`accommodation.booking.create`. Two places, two-halves test.

### 2.7 The mock

Cancellation preview and command with the snapshot, check-in by token, check-out with
actual nights, no-show report and review, the waitlist with an advanceable offer job; a
world with a booking inside its free window, one past it, a reported no-show and a waiting
entry.

## 3. Tests required

- **Snapshot, not today's contract**: confirm under a policy, change the contract's terms,
  cancel — the fee is the snapshot's; the `cancellation` row carries the snapshot it used.
- **Free vs penalized**: inside the window everything is released and the fee is zero;
  outside it `penaltyNights` are consumed, the rest released, `payer + member == fee` exact,
  and ledger conservation holds. Twice releases nothing twice.
- **Check-in by token**: the right token checks in, a rotated one is 404, a token outside
  the window is refused with the window named.
- **Early check-out releases** the unused nights (inventory and entitlement, across every
  hold); an over-stay flags and consumes nothing extra.
- **No-show is reviewed**: a report without evidence is refused; the reporter cannot confirm;
  confirmation consumes up to the policy and frees the room; rejection leaves the booking
  bookable.
- **Waitlist FIFO with priority**: three entries, one room freed, the highest priority then
  the earliest gets the offer; an unaccepted offer expires and the room passes to the next.
- The partial unique index on live waitlist entries, with a concurrent double join.

## 4. Acceptance criteria

- [ ] A cancellation produces the fee and the release the booking's own policy snapshot
      says, whatever the contract says today.
- [ ] Check-in redeems the voucher and check-out consumes what was used and releases what
      was not, once.
- [ ] A no-show never costs the member anything before a second person confirms it.
- [ ] A freed room reaches the waitlist in queue order without anybody watching.
- [ ] OpenAPI (additive), generated code, Spectral and `oasdiff` clean; Turkish for every
      problem code; schema version 42.
