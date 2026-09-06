# WP-I6-02 · Hold and booking: no oversell under 500 concurrent holds, expiry that releases, confirmation that reserves once

| Field                      | Value                                                                                                                                                                                                                                                                                  |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M6 (plan increment I6)                                                                                                                                                                                                                                                                 |
| Size                       | L                                                                                                                                                                                                                                                                                      |
| Depends on                 | WP-I6-01 (inventory, quote), WP-I2-03 (entitlement reservation with expiry), WP-I4-01 (RESERVATION requests and the submit gate), WP-I4-02 (authorization, voucher), WP-I5-03 as delivered (the outbox subscription to `service_request.decided`, `stayports`/gateway shape)          |
| Runs in parallel with      | WP-I6-04 (contract terms; a booking carries its policy snapshot from the moment 04 lands, `null` before)                                                                                                                                                                              |
| Migration numbers assigned | `000037_accommodation_booking.up.sql`                                                                                                                                                                                                                                                  |
| OpenAPI operations owned   | `createHold`, `getBooking`, `listBookings`, `confirmBooking`, `releaseHold`, `reissueBookingVoucher`                                                                                                                                                                                   |
| Read first                 | v1.2 10.6 steps 4–7, 11.11, 11.13, 12.3, 13.5 (the booking confirmation transaction, verbatim), 16.7 (`booking`, `booking_night`, `booking_guest`), 35.2; WP-I2-03 (`Reserve`, expiry), WP-I4-02 (`CreateForRequest`, `issueVoucher`, the digest-only token), WP-I5-03 §2.2–2.3 and `internal/health/application/staydecision.go` |

## 1. Goal

A hold is a promise of a room for fifteen minutes, made under a lock; a booking is that
promise kept, with the plan's nights reserved once and a voucher the member holds. Three
rules carry the package: **the inventory row decides, under `FOR UPDATE` in date order, and
never goes negative — 500 concurrent holds on three rooms leave exactly three holds**; **an
expired hold gives back the room and the nights without anybody calling**; and **confirming
reserves the entitlement exactly once — the authorization adopts the hold's reservation
rather than taking a second one.**

## 2. Scope

### 2.1 Schema (migration 000037)

`accommodation.booking`: id, tenant_id, `reference` (`BK-YYYYMMDD-XXXXXXXX`, unique per
tenant), person_id, enrollment_id, program_id, `property_id`, `room_type_id`, `check_in`,
`check_out` CHECK `>`, `nights int` (written by the service as `check_out − check_in` and
CHECKed equal to it), `adults`, `children`, `status` (12.3: `HOLD`,`PENDING_APPROVAL`,
`CONFIRMED`,`CHECKED_IN`,`COMPLETED`,`CANCELLED`,`NO_SHOW`,`EXPIRED`), `hold_expires_at`,
`entitlement_reservation_id` NULL (WP-I2-03's row the hold took), `service_request_id` NULL,
`authorization_id` NULL, `voucher_id` NULL, `quote_snapshot jsonb` (what WP-I6-01 showed —
nightly amounts, payer, member, total, currency, evaluation id — frozen at hold), `policy_snapshot
jsonb` NULL (WP-I6-04 fills it at confirmation), `channel`, `confirmed_at`, `checked_in_at`,
`checked_out_at`, `cancelled_at`, `cancel_reason_code`, `actual_nights` NULL, row_version.
Partial unique `(tenant_id, person_id, room_type_id, check_in) WHERE status IN ('HOLD',
'PENDING_APPROVAL','CONFIRMED','CHECKED_IN')`: one live booking of one room type per person
and arrival.

`accommodation.booking_night`: booking_id, `stay_date`, room_type_id, `unit_amount`,
`payer_amount`, `member_amount`, `currency_code`; unique `(tenant_id, booking_id, stay_date)`.
A db test proves the count of nights equals `booking.nights` after every command.

`accommodation.booking_guest`: booking_id, `person_id` NULL (a member or a dependant),
`display_name` (a snapshot for a non-member guest; a name, never an identifier),
`guest_type` (`MEMBER`,`DEPENDANT`,`GUEST`), `is_minor`; unique `(tenant_id, booking_id,
person_id) WHERE person_id IS NOT NULL`. Occupancy is validated against the room type.

RLS, touch triggers, composite keys. A `booking_status_event` table is not added: the audit
log and the outbox carry the transitions, as they do for requests and stays.

### 2.2 The hold, under the lock

`createHold` in one transaction, exactly the shape of v1.2 13.5: lock the `inventory_day`
rows of `[check_in, check_out)` **in `stay_date` order** with `FOR UPDATE` (the order is what
makes 500 concurrent holds a queue rather than a deadlock), refuse with 409 `ROOM_UNAVAILABLE`
naming the first full night if any row has `capacity − held − confirmed < 1`, increment
`held` on every row, take the entitlement reservation for `nights` on the plan's `NIGHT`
account with `expires_at = hold_expires_at` (WP-I2-03's reservation expiry; the same key as
the hold), write the booking as `HOLD` with the quote snapshot, and return it with the
seconds left. `hold_expires_at = now + tenant setting accommodation.hold_minutes` (default
15; `platform.tenant_setting`).

The expiry job (scheduler, every minute): every `HOLD` past `hold_expires_at` becomes
`EXPIRED`, its `held` counters are decremented under the same lock order, and its
reservation is released — the job is the release, not a request that may never come, and
it is idempotent under any number of runs. `releaseHold` is the member giving the room back
early; same effect, on the record as a command.

### 2.3 Confirmation: one path to an authorization

`confirmBooking` (from `HOLD`, before expiry; step-up when the quote's `memberAmount` is
above the tenant's threshold, WP-I1-02): submits a `RESERVATION` service request through
WP-I4-01's `SubmitInTx` with one line of `nights × NIGHT` for the room type's service, the
property as the provider, and the quote's amounts as requested amounts; the gate runs.
Auto-approval or a person's approval arrives through the outbox event
`service_request.decided`, which this package subscribes to exactly as WP-I5-03 does:

- `APPROVED` → an authorization is created for the nights through WP-I4-02 **adopting the
  hold's reservation** (new port `AdoptReservation`: `authorization_item.entitlement_reservation_id`
  points at the hold's row, whose expiry is moved to `check_out + 1 day`; nothing is reserved
  twice, and ledger conservation holds); `held → confirmed` on every night under the lock
  order; the voucher is issued on the authorization (WP-I4-02, digest stored, token returned
  once on the confirmation's own response through a short-lived pickup, never in a
  notification); status `CONFIRMED`, `confirmed_at`, the policy snapshot (WP-I6-04) frozen.
- `PENDING_REVIEW`/`PENDING_DOCUMENT` → `PENDING_APPROVAL`; the hold's expiry is extended to
  the request's own SLA so the room is not lost while a reviewer decides.
- `REJECTED` → the hold is released (inventory and reservation) and the booking is
  `CANCELLED` with the request's reason.

Pricing is never recomputed at confirmation: the quote snapshot is what the member saw and
what the booking nights carry. A stale quote (the search older than the tenant's
`accommodation.quote_ttl_minutes`, default 60) is refused with 409 `QUOTE_STALE` and the
member is sent back to search.

`reissueBookingVoucher` rotates the voucher (the old digest is retired) for a `CONFIRMED`
booking, on the member's own booking or by `accommodation.booking.manage`; every issue is
an audit row.

### 2.4 Visibility

A member reads and holds for their own person only (WP-I6-04 §2.4's person binding); a
provider reads bookings at its own properties; the payer reads all. `listBookings` filters
by person, property, status and arrival range.

### 2.5 The mock

`booking-handlers.ts` with the hold (a real countdown), the expiry (advanceable in tests),
confirmation through the mock's request gate, the voucher token shown once, and a world
with a hold, a confirmed booking with a voucher, a checked-in and a completed one.

## 3. Tests required

- **No oversell**: 500 concurrent `createHold` calls on one room type with capacity 3 over
  the same three nights leave exactly three `HOLD` rows, `held = 3` on every night, 497
  `ROOM_UNAVAILABLE`, no deadlock, and the CHECK never fired (it is the belt; the lock is the
  braces).
- **Expiry releases**: a hold past its time is `EXPIRED` by the job, `held` is back, the
  reservation is `EXPIRED`/released, the ledger is conserved, and a second run changes nothing.
- **Confirmation reserves once**: after `confirmBooking` → approval, the account's reserved
  quantity moved by exactly `nights` in total (hold + confirmation), the authorization's
  item points at the hold's reservation, `held → confirmed` on every night, and the voucher's
  digest exists while its token appears in no table, audit row or log.
- **Rejection releases**: a rejected RESERVATION request cancels the booking and gives back
  inventory and entitlement, once.
- **Stale quote refused**; **one live booking per person, room type and arrival** (the
  partial unique index, proved with a concurrent double-submit).
- The night count equals the booking-night rows after every command.

## 4. Acceptance criteria

- [ ] 500 concurrent holds leave inventory non-negative and never oversold, proved on a real
      PostgreSQL with the lock order, not by luck.
- [ ] Hold expiry releases the room and the entitlement without an API call.
- [ ] A confirmed booking has one authorization, one reservation and one voucher, and the
      member saw the exact amounts before confirming.
- [ ] OpenAPI (additive), generated code, Spectral and `oasdiff` clean; Turkish for every
      problem code; schema version 37.
