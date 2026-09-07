-- The hold and the booking it becomes (WP-I6-02, v1.2 10.6 steps 4-7, 11.13, 13.5).
--
-- Four things about this file are load-bearing and none of them is repeated in Go.
--
-- **The lock order is `stay_date`.** `LockBookingInventoryRange` is the read every hold,
-- every release and every confirmation makes before it writes, and it is `ORDER BY
-- stay_date ... FOR UPDATE`. That single clause is what turns five hundred concurrent
-- holds on the same three nights into a queue rather than a deadlock: every transaction
-- takes the same rows in the same order, so none of them can be holding night two while
-- waiting for night one that somebody else holds while waiting for night two.
--
-- **The counters move by a statement, never by a read-modify-write.** `AddInventoryHeld`
-- and `MoveHeldToConfirmed` are `UPDATE ... SET held = held + $n` over the whole range, so
-- the arithmetic happens in the database with the row already locked. The CHECK
-- `held + confirmed <= capacity` of migration 000040 is the belt: if the availability
-- check above were deleted tomorrow these statements would still fail rather than
-- oversell.
--
-- **A booking is found through its own boundary.** `scope_ids` is the provider boundary
-- (NULL means the whole tenant) and `person_id` is the member boundary; both are applied
-- in the WHERE rather than after the read, because a filter applied after a LIMIT is a
-- filter that has already been defeated.
--
-- **Money is `numeric(20,6)` returned as `::text`.** The decimal the member was shown is
-- the decimal that comes back; nothing here passes through a float.

-- ---------------------------------------------------------------------------
-- Inventory under the lock
-- ---------------------------------------------------------------------------

-- name: LockBookingInventoryRange :many
-- The nights of `[check_in, check_out)`, locked in stay_date order. It is separate from
-- LockRoomTypeInventoryRange only in what it returns -- the availability as well as the
-- counters -- because the hold refuses on `capacity - held - confirmed < 1` and the
-- allotment writer refuses on `held + confirmed > capacity`, and each should read the
-- number it decides on rather than subtract for itself.
SELECT i.stay_date, i.capacity, i.held, i.confirmed,
       (i.capacity - i.held - i.confirmed)::int AS available
  FROM accommodation.inventory_day i
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND i.room_type_id = sqlc.arg('room_type_id')
   AND i.stay_date >= sqlc.arg('from_date')::date
   AND i.stay_date <= sqlc.arg('to_date')::date
 ORDER BY i.stay_date
   FOR UPDATE;

-- name: AddInventoryHeld :execrows
-- Move `held` by a signed delta over the whole range in one statement. The caller has
-- already locked these rows in stay_date order; this statement is the write that lock was
-- taken for. A delta that would take `held` negative or push `held + confirmed` past
-- `capacity` is refused by the row's own constraints, which is the outcome that matters
-- when a caller forgets to look.
UPDATE accommodation.inventory_day
   SET held = held + sqlc.arg('delta')::int
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND room_type_id = sqlc.arg('room_type_id')
   AND stay_date >= sqlc.arg('from_date')::date
   AND stay_date <= sqlc.arg('to_date')::date;

-- name: MoveHeldToConfirmed :execrows
-- Confirmation: the room stops being held and starts being taken. It is one statement
-- rather than a decrement and an increment because the two halves must not be separable --
-- a transaction that ran only the first would give a confirmed room back to the pool.
UPDATE accommodation.inventory_day
   SET held = held - 1, confirmed = confirmed + 1
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND room_type_id = sqlc.arg('room_type_id')
   AND stay_date >= sqlc.arg('from_date')::date
   AND stay_date <= sqlc.arg('to_date')::date;

-- ---------------------------------------------------------------------------
-- Booking
-- ---------------------------------------------------------------------------

-- name: CreateBooking :one
INSERT INTO accommodation.booking (
    tenant_id, reference, person_id, enrollment_id, program_id, property_id, room_type_id,
    check_in, check_out, nights, adults, children, status, hold_expires_at,
    entitlement_reservation_id, quote_snapshot, channel, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('reference'), sqlc.arg('person_id'),
        sqlc.arg('enrollment_id'), sqlc.arg('program_id'), sqlc.arg('property_id'),
        sqlc.arg('room_type_id'), sqlc.arg('check_in')::date, sqlc.arg('check_out')::date,
        sqlc.arg('nights')::int, sqlc.arg('adults')::int, sqlc.arg('children')::int,
        sqlc.arg('status'), sqlc.narg('hold_expires_at'),
        sqlc.narg('entitlement_reservation_id'), sqlc.arg('quote_snapshot'),
        sqlc.arg('channel'), sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, reference, person_id, enrollment_id, program_id, property_id, room_type_id,
          check_in, check_out, nights, adults, children, status, hold_expires_at,
          entitlement_reservation_id, service_request_id, authorization_id, voucher_id,
          quote_snapshot, policy_snapshot, channel, confirmed_at, checked_in_at,
          checked_out_at, cancelled_at, cancel_reason_code, actual_nights, over_booking,
          created_at, updated_at, row_version;

-- name: GetBooking :one
-- The single read, with both boundaries applied. `person_id` NULL is a caller that is not
-- bound to a member; `scope_ids` NULL is one that is not bound to a provider. A booking
-- outside either is not found rather than forbidden: that a member is going to Antalya is
-- not another provider's business.
SELECT b.id, b.reference, b.person_id, b.enrollment_id, b.program_id, b.property_id,
       b.room_type_id, b.check_in, b.check_out, b.nights, b.adults, b.children, b.status,
       b.hold_expires_at, b.entitlement_reservation_id, b.service_request_id,
       b.authorization_id, b.voucher_id, b.quote_snapshot, b.policy_snapshot, b.channel,
       b.confirmed_at, b.checked_in_at, b.checked_out_at, b.cancelled_at,
       b.cancel_reason_code, b.actual_nights, b.over_booking, b.created_at, b.updated_at,
       b.row_version
  FROM accommodation.booking b
  JOIN accommodation.property p
    ON p.tenant_id = b.tenant_id AND p.id = b.property_id
 WHERE b.tenant_id = sqlc.arg('tenant_id')
   AND b.id = sqlc.arg('id')
   AND (sqlc.narg('person_id')::uuid IS NULL OR b.person_id = sqlc.narg('person_id')::uuid)
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: LockBooking :one
-- The same row FOR UPDATE, for a command that is about to move it. The boundaries are not
-- applied here: every caller of this has already found the booking through GetBooking or
-- reached it from the outbox, and re-applying a scope inside the lock would mean the
-- expiry sweep -- which acts for the tenant and for no provider -- could not take it.
SELECT b.id, b.reference, b.person_id, b.enrollment_id, b.program_id, b.property_id,
       b.room_type_id, b.check_in, b.check_out, b.nights, b.adults, b.children, b.status,
       b.hold_expires_at, b.entitlement_reservation_id, b.service_request_id,
       b.authorization_id, b.voucher_id, b.quote_snapshot, b.policy_snapshot, b.channel,
       b.confirmed_at, b.checked_in_at, b.checked_out_at, b.cancelled_at,
       b.cancel_reason_code, b.actual_nights, b.over_booking, b.created_at, b.updated_at,
       b.row_version
  FROM accommodation.booking b
 WHERE b.tenant_id = sqlc.arg('tenant_id')
   AND b.id = sqlc.arg('id')
   FOR UPDATE;

-- name: GetBookingByRequest :one
-- The booking a decided service request belongs to. Most decided requests belong to none,
-- and the subscriber reads that as "not mine" rather than as an error.
SELECT b.id, b.reference, b.person_id, b.enrollment_id, b.program_id, b.property_id,
       b.room_type_id, b.check_in, b.check_out, b.nights, b.adults, b.children, b.status,
       b.hold_expires_at, b.entitlement_reservation_id, b.service_request_id,
       b.authorization_id, b.voucher_id, b.quote_snapshot, b.policy_snapshot, b.channel,
       b.confirmed_at, b.checked_in_at, b.checked_out_at, b.cancelled_at,
       b.cancel_reason_code, b.actual_nights, b.over_booking, b.created_at, b.updated_at,
       b.row_version
  FROM accommodation.booking b
 WHERE b.tenant_id = sqlc.arg('tenant_id')
   AND b.service_request_id = sqlc.arg('service_request_id');

-- name: ListBookings :many
-- Keyset pagination on (created_at DESC, id DESC), the same shape every list in this
-- repository uses; the caller asks for limit+1 rows to learn whether a next page exists.
SELECT b.id, b.reference, b.person_id, b.enrollment_id, b.program_id, b.property_id,
       b.room_type_id, b.check_in, b.check_out, b.nights, b.adults, b.children, b.status,
       b.hold_expires_at, b.entitlement_reservation_id, b.service_request_id,
       b.authorization_id, b.voucher_id, b.quote_snapshot, b.policy_snapshot, b.channel,
       b.confirmed_at, b.checked_in_at, b.checked_out_at, b.cancelled_at,
       b.cancel_reason_code, b.actual_nights, b.over_booking, b.created_at, b.updated_at,
       b.row_version
  FROM accommodation.booking b
  JOIN accommodation.property p
    ON p.tenant_id = b.tenant_id AND p.id = b.property_id
 WHERE b.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('person_id')::uuid IS NULL OR b.person_id = sqlc.narg('person_id')::uuid)
   AND (sqlc.narg('property_id')::uuid IS NULL OR b.property_id = sqlc.narg('property_id')::uuid)
   AND (sqlc.narg('status')::text IS NULL OR b.status = sqlc.narg('status')::text)
   AND (sqlc.narg('check_in_from')::date IS NULL OR b.check_in >= sqlc.narg('check_in_from')::date)
   AND (sqlc.narg('check_in_to')::date IS NULL OR b.check_in <= sqlc.narg('check_in_to')::date)
   AND (sqlc.narg('after_at')::timestamptz IS NULL
        OR (b.created_at, b.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
 ORDER BY b.created_at DESC, b.id DESC
 LIMIT sqlc.arg('page_size');

-- name: SetBookingRequest :execrows
-- The RESERVATION request the confirmation raised, written in the same transaction that
-- raised it. The HOLD predicate is what makes a replayed confirmation a no-op rather than
-- a second request.
UPDATE accommodation.booking
   SET service_request_id = sqlc.arg('service_request_id'), updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'HOLD'
   AND service_request_id IS NULL;

-- name: SetBookingReservation :execrows
-- The plan's hold, written onto the booking the statement above inserted. It is a second
-- statement rather than a column of the insert because the reservation references the
-- booking: the row has to exist before anything can point at it, and the whole pair is one
-- transaction so neither half can be committed without the other.
UPDATE accommodation.booking
   SET entitlement_reservation_id = sqlc.arg('entitlement_reservation_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id');

-- name: ConfirmBooking :execrows
-- The approval, applied under the booking's own lock. Every write is guarded by the status
-- predicate rather than by a flag: the outbox delivers at least once, and the second
-- delivery has to find nothing to do rather than confirm a confirmed booking.
UPDATE accommodation.booking
   SET status = 'CONFIRMED', confirmed_at = sqlc.arg('confirmed_at'),
       authorization_id = sqlc.arg('authorization_id'),
       policy_snapshot = sqlc.narg('policy_snapshot'),
       hold_expires_at = NULL, updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('HOLD','PENDING_APPROVAL');

-- name: MarkBookingPendingApproval :execrows
-- A reviewer has the request and the room is not lost while they decide: the status moves
-- and the countdown is pushed out to the request's own SLA.
UPDATE accommodation.booking
   SET status = 'PENDING_APPROVAL', hold_expires_at = sqlc.arg('hold_expires_at'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'HOLD';

-- name: CancelBooking :execrows
UPDATE accommodation.booking
   SET status = 'CANCELLED', cancelled_at = sqlc.arg('cancelled_at'),
       cancel_reason_code = sqlc.arg('cancel_reason_code'), hold_expires_at = NULL,
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('HOLD','PENDING_APPROVAL');

-- name: ExpireBooking :execrows
-- The sweep's own write. The predicate is the whole of its idempotency: a second run finds
-- the row already EXPIRED, updates nothing, and the caller reads the zero row count as
-- "somebody else already did this" rather than as a failure.
UPDATE accommodation.booking
   SET status = 'EXPIRED', hold_expires_at = NULL
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'HOLD';

-- name: SetBookingVoucher :execrows
-- The voucher row this booking's proof of entitlement is. It is the id and never the
-- token: the plaintext exists in one response body and in no column anywhere.
UPDATE accommodation.booking
   SET voucher_id = sqlc.arg('voucher_id'), updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id');

-- name: ListExpiredHolds :many
-- The sweep's read: holds past their deadline, taken FOR UPDATE SKIP LOCKED so two
-- schedulers that both believe they lead cannot expire the same booking twice, and neither
-- waits on the other.
SELECT b.id
  FROM accommodation.booking b
 WHERE b.tenant_id = sqlc.arg('tenant_id')
   AND b.status = 'HOLD'
   AND b.hold_expires_at <= sqlc.arg('before')
 ORDER BY b.hold_expires_at
 LIMIT sqlc.arg('page_size')
   FOR UPDATE SKIP LOCKED;

-- name: ListBookingsForReminder :many
-- The bookings whose check-in falls on one civil date, with the property's own zone and
-- name, for the day-before reminder. The zone comes back with the row because a night
-- belongs to the calendar hanging on the wall of that building: a sweep that used one
-- tenant-wide zone would remind a member in Berlin on the wrong day.
SELECT b.id, b.reference, b.person_id, b.check_in, p.name AS property_name, p.timezone
  FROM accommodation.booking b
  JOIN accommodation.property p
    ON p.tenant_id = b.tenant_id AND p.id = b.property_id
 WHERE b.tenant_id = sqlc.arg('tenant_id')
   AND b.status = 'CONFIRMED'
   AND b.check_in = sqlc.arg('check_in')::date
 ORDER BY b.id
 LIMIT sqlc.arg('page_size');

-- ---------------------------------------------------------------------------
-- Booking nights and guests
-- ---------------------------------------------------------------------------

-- name: CreateBookingNight :exec
INSERT INTO accommodation.booking_night (
    tenant_id, booking_id, stay_date, room_type_id, unit_amount, payer_amount,
    member_amount, currency_code)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('booking_id'), sqlc.arg('stay_date')::date,
        sqlc.arg('room_type_id'), sqlc.arg('unit_amount')::text::numeric,
        sqlc.arg('payer_amount')::text::numeric, sqlc.arg('member_amount')::text::numeric,
        sqlc.arg('currency_code'));

-- name: ListBookingNights :many
SELECT n.stay_date, n.room_type_id, n.unit_amount::text AS unit_amount,
       n.payer_amount::text AS payer_amount, n.member_amount::text AS member_amount,
       n.currency_code
  FROM accommodation.booking_night n
 WHERE n.tenant_id = sqlc.arg('tenant_id')
   AND n.booking_id = sqlc.arg('booking_id')
 ORDER BY n.stay_date;

-- name: CountBookingNights :one
SELECT count(*)::int AS nights
  FROM accommodation.booking_night n
 WHERE n.tenant_id = sqlc.arg('tenant_id')
   AND n.booking_id = sqlc.arg('booking_id');

-- name: CreateBookingGuest :exec
INSERT INTO accommodation.booking_guest (
    tenant_id, booking_id, person_id, display_name, guest_type, is_minor)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('booking_id'), sqlc.narg('person_id'),
        sqlc.arg('display_name'), sqlc.arg('guest_type'), sqlc.arg('is_minor'));

-- name: ListBookingGuests :many
SELECT g.id, g.person_id, g.display_name, g.guest_type, g.is_minor
  FROM accommodation.booking_guest g
 WHERE g.tenant_id = sqlc.arg('tenant_id')
   AND g.booking_id = sqlc.arg('booking_id')
 ORDER BY g.created_at, g.id;

-- ---------------------------------------------------------------------------
-- What a hold needs to know before it takes one
-- ---------------------------------------------------------------------------

-- name: GetBookingRoomTypeContext :one
-- The room type with everything the hold decides on: the building it is in, the provider
-- behind it, the clock the nights are counted against, and the catalogue service the
-- request line and the entitlement both hang off.
SELECT rt.id AS room_type_id, rt.code AS room_type_code, rt.name AS room_type_name,
       rt.max_adults, rt.max_children, rt.max_occupancy, rt.service_definition_id,
       rt.status AS room_type_status,
       sd.code AS service_definition_code, sd.default_unit_type, sd.active AS service_active,
       p.id AS property_id, p.name AS property_name, p.timezone,
       p.provider_organization_id, p.status AS property_status
  FROM accommodation.room_type rt
  JOIN accommodation.property p
    ON p.tenant_id = rt.tenant_id AND p.id = rt.property_id
  JOIN catalog.service_definition sd
    ON sd.tenant_id = rt.tenant_id AND sd.id = rt.service_definition_id
 WHERE rt.tenant_id = sqlc.arg('tenant_id')
   AND rt.id = sqlc.arg('room_type_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: GetPersonEnrollmentForStay :one
-- The enrollment the stay is booked under: the person's own active enrollment covering the
-- first night, narrowed to one program when the caller named one. A person with none has
-- no plan to book against, and the hold refuses rather than guessing.
SELECT e.id AS enrollment_id, pr.id AS program_id
  FROM benefit.enrollment e
  JOIN party.sponsor_membership m ON m.tenant_id = e.tenant_id AND m.id = e.sponsor_membership_id
  JOIN benefit.plan pl ON pl.tenant_id = e.tenant_id AND pl.id = e.plan_id
  JOIN benefit.program pr ON pr.tenant_id = pl.tenant_id AND pr.id = pl.program_id
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND m.person_id = sqlc.arg('person_id')
   AND e.status = 'ACTIVE'
   AND pr.status = 'ACTIVE'
   AND e.valid_period @> sqlc.arg('service_date')::date
   AND (sqlc.narg('program_id')::uuid IS NULL OR pr.id = sqlc.narg('program_id')::uuid)
 ORDER BY e.id
 LIMIT 1;

-- name: GetContractVersionForRoomType :one
-- The contract version the room type's price came from on the first night, which is the
-- version whose lodging terms a confirmation freezes. It is read rather than passed in
-- because the terms have to be the ones behind the price the member was quoted, and a
-- caller that could name a version could freeze somebody else's policy onto this stay.
SELECT cv.id AS contract_version_id
  FROM contract.contract_version cv
  JOIN contract.contract c
    ON c.tenant_id = cv.tenant_id AND c.id = cv.contract_id
 WHERE cv.tenant_id = sqlc.arg('tenant_id')
   AND c.provider_profile_id = sqlc.arg('provider_profile_id')
   AND c.status = 'ACTIVE'
   AND cv.status = 'PUBLISHED'
   AND cv.valid_from <= sqlc.arg('service_date')::date
   AND (cv.valid_to IS NULL OR cv.valid_to > sqlc.arg('service_date')::date)
 ORDER BY (c.domain_code = 'ACCOMMODATION') DESC, cv.valid_from DESC, cv.id
 LIMIT 1;

-- name: GetProviderProfileForProperty :one
SELECT pp.id AS provider_profile_id
  FROM provider.provider_profile pp
  JOIN accommodation.property p
    ON p.tenant_id = pp.tenant_id AND p.provider_organization_id = pp.tenant_organization_id
 WHERE pp.tenant_id = sqlc.arg('tenant_id')
   AND p.id = sqlc.arg('property_id')
   AND pp.status = 'ACTIVE'
 ORDER BY pp.id
 LIMIT 1;


-- name: GetBookingEntitlementCode :one
-- Which entitlement a room type's service draws on, under the plan version in force for
-- this enrollment on the first night (WP-I5-05's `benefit.service_entitlement_mapping`).
--
-- The mapping and not the service's own code. A room type is a catalogue service and the
-- entitlement it spends is the tenant's configuration: OTEL_GECE may be mapped to
-- KONAKLAMA_GECE, or to a shared family budget, or to nothing at all. Matching on the code
-- would work for whichever tenant happened to name them the same and quietly reserve the
-- wrong balance -- or none -- for every other one.
SELECT d.code AS entitlement_code
  FROM benefit.enrollment e
  JOIN benefit.plan_version v
    ON v.tenant_id = e.tenant_id
   AND v.plan_id = e.plan_id
   AND v.status = 'PUBLISHED'
   AND v.valid_period @> sqlc.arg('service_date')::date
  JOIN benefit.service_entitlement_mapping m
    ON m.tenant_id = v.tenant_id
   AND m.plan_version_id = v.id
   AND m.service_definition_id = sqlc.arg('service_definition_id')
  JOIN benefit.entitlement_definition d
    ON d.tenant_id = m.tenant_id
   AND d.id = m.entitlement_definition_id
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND e.id = sqlc.arg('enrollment_id')
   AND (m.valid_from IS NULL OR m.valid_from <= sqlc.arg('service_date')::date)
   AND (m.valid_to IS NULL OR m.valid_to > sqlc.arg('service_date')::date)
 LIMIT 1;
