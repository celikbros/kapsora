-- What happens after the promise (WP-I6-03): the cancellation, the check-in and check-out,
-- the no-show and the waitlist.
--
-- Four things about this file are load-bearing and none of them is repeated in Go.
--
-- **Every status write names the status it moves from.** `CheckInBooking` names CONFIRMED,
-- `CheckOutBooking` names CHECKED_IN, `CancelConfirmedBooking` names the two statuses a
-- cancellation may act on. A command replayed by a flaky network finds no row to update and
-- reads the zero row count as "somebody already did this" rather than doing it twice.
--
-- **The lock order is still `stay_date`.** Nothing here moves an inventory counter without
-- `LockBookingInventoryRange` first, which is `ORDER BY stay_date ... FOR UPDATE`. A
-- cancellation and a hold taking the same nights in different orders is how two commands
-- that each work deadlock when they meet.
--
-- **The queue order is `priority DESC, created_at ASC`.** `ListWaitlistQueue` spells it the
-- same way `ix_waitlist_queue` does, and it takes its rows FOR UPDATE SKIP LOCKED so two
-- schedulers that both believe they lead offer different rooms and neither waits.
--
-- **Money is `numeric(20,6)` written as `::text::numeric` and read back as `::text`.** The
-- decimal the member was quoted is the decimal the row holds and the decimal that comes
-- back; nothing here passes through a float.

-- ---------------------------------------------------------------------------
-- Booking transitions this package owns
-- ---------------------------------------------------------------------------

-- name: CancelConfirmedBooking :execrows
-- The cancellation of a stay somebody actually agreed to, which is the one kind that may
-- carry a fee. It names CONFIRMED and PENDING_APPROVAL and never CHECKED_IN: a guest who
-- has arrived cannot cancel, they check out.
UPDATE accommodation.booking
   SET status = 'CANCELLED', cancelled_at = sqlc.arg('cancelled_at'),
       cancel_reason_code = sqlc.arg('cancel_reason_code'), hold_expires_at = NULL,
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('CONFIRMED','PENDING_APPROVAL');

-- name: CheckInBooking :execrows
-- The guest arrived. CONFIRMED and only CONFIRMED: a booking checked in twice would redeem
-- a voucher twice, and the voucher's own status refuses the second one anyway -- this
-- predicate is what makes the refusal arrive before anything is spent.
UPDATE accommodation.booking
   SET status = 'CHECKED_IN', checked_in_at = sqlc.arg('checked_in_at'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'CONFIRMED';

-- name: CheckOutBooking :execrows
-- The guest left. `actual_nights` is what they actually stayed, counted on the property's
-- own calendar, and `over_booking` says the stay ran past what was authorized -- which is a
-- flag for M7's claim and never a refusal, because the nights have already been slept.
UPDATE accommodation.booking
   SET status = 'COMPLETED', checked_out_at = sqlc.arg('checked_out_at'),
       actual_nights = sqlc.arg('actual_nights')::int,
       over_booking = sqlc.arg('over_booking')::boolean,
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'CHECKED_IN';

-- name: MarkBookingNoShow :execrows
-- The payer confirmed the provider's report. CONFIRMED only: a booking that was checked in
-- cannot have been a no-show, and one already cancelled is not one either.
UPDATE accommodation.booking
   SET status = 'NO_SHOW', cancel_reason_code = 'NO_SHOW', updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'CONFIRMED';

-- name: AddInventoryConfirmed :execrows
-- Move `confirmed` by a signed delta over a range, in one statement, with the rows already
-- locked in stay_date order. It is the confirmed-side twin of AddInventoryHeld, and it
-- exists because a cancelled or completed stay gives back a room that is *taken* rather
-- than one that is *held*: decrementing the wrong counter would leave the allotment looking
-- right in total and wrong on every night.
UPDATE accommodation.inventory_day
   SET confirmed = confirmed + sqlc.arg('delta')::int
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND room_type_id = sqlc.arg('room_type_id')
   AND stay_date >= sqlc.arg('from_date')::date
   AND stay_date <= sqlc.arg('to_date')::date;

-- ---------------------------------------------------------------------------
-- accommodation.cancellation
-- ---------------------------------------------------------------------------

-- name: CreateCancellation :one
-- The record of what the cancellation cost and what it was judged by. The policy is copied
-- from the booking rather than referenced, so the row proves which terms were applied even
-- after the contract has been rewritten twice.
INSERT INTO accommodation.cancellation (
    tenant_id, booking_id, cancelled_at, cancelled_by, reason_code, policy_snapshot, free,
    penalty_nights, released_nights, fee_amount, payer_fee, member_fee, currency_code)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('booking_id'), sqlc.arg('cancelled_at'),
        sqlc.narg('cancelled_by'), sqlc.arg('reason_code'), sqlc.arg('policy_snapshot'),
        sqlc.arg('free')::boolean, sqlc.arg('penalty_nights')::int,
        sqlc.arg('released_nights')::int, sqlc.arg('fee_amount')::text::numeric,
        sqlc.arg('payer_fee')::text::numeric, sqlc.arg('member_fee')::text::numeric,
        sqlc.arg('currency_code'))
RETURNING id, booking_id, cancelled_at, cancelled_by, reason_code, policy_snapshot, free,
          penalty_nights, released_nights, fee_amount::text AS fee_amount,
          payer_fee::text AS payer_fee, member_fee::text AS member_fee, currency_code,
          created_at;

-- name: GetCancellation :one
SELECT id, booking_id, cancelled_at, cancelled_by, reason_code, policy_snapshot, free,
       penalty_nights, released_nights, fee_amount::text AS fee_amount,
       payer_fee::text AS payer_fee, member_fee::text AS member_fee, currency_code,
       created_at
  FROM accommodation.cancellation
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND booking_id = sqlc.arg('booking_id');

-- ---------------------------------------------------------------------------
-- accommodation.no_show
-- ---------------------------------------------------------------------------

-- name: CountCleanBookingDocuments :one
-- The evidence half of the no-show gate: is a document actually linked to this booking, and
-- did the scanner clear it. A link to an object still in quarantine is not evidence a
-- reviewer can open, and a no-show reported without one is a reviewer asked to decide on
-- nothing. `aggregate_type = 'BOOKING'` is the link WP-I4-04 writes for a stay.
SELECT count(*)
  FROM document.link l
  JOIN document.object o ON o.tenant_id = l.tenant_id AND o.id = l.object_id
 WHERE l.tenant_id = sqlc.arg('tenant_id')
   AND l.aggregate_type = 'BOOKING'
   AND l.aggregate_id = sqlc.arg('booking_id')
   AND (sqlc.narg('object_id')::uuid IS NULL OR o.id = sqlc.narg('object_id')::uuid)
   AND o.scan_status = 'CLEAN'
   AND o.purged_at IS NULL;

-- name: CreateNoShow :one
INSERT INTO accommodation.no_show (
    tenant_id, booking_id, reported_by_actor_id, reported_at, evidence_document_id,
    assessed_fee_amount, payer_amount, member_amount, currency_code, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('booking_id'), sqlc.narg('reported_by_actor_id'),
        sqlc.arg('reported_at'), sqlc.narg('evidence_document_id'),
        sqlc.arg('assessed_fee_amount')::text::numeric, sqlc.arg('payer_amount')::text::numeric,
        sqlc.arg('member_amount')::text::numeric, sqlc.arg('currency_code'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, booking_id, reported_by_actor_id, reported_at, evidence_document_id,
          assessed_fee_amount::text AS assessed_fee_amount, payer_amount::text AS payer_amount,
          member_amount::text AS member_amount, currency_code, status, reviewed_by,
          reviewed_at, review_comment, consumed_nights, created_at, updated_at, row_version;

-- name: GetNoShow :one
SELECT id, booking_id, reported_by_actor_id, reported_at, evidence_document_id,
       assessed_fee_amount::text AS assessed_fee_amount, payer_amount::text AS payer_amount,
       member_amount::text AS member_amount, currency_code, status, reviewed_by,
       reviewed_at, review_comment, consumed_nights, created_at, updated_at, row_version
  FROM accommodation.no_show
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND booking_id = sqlc.arg('booking_id');

-- name: LockNoShow :one
-- The report FOR UPDATE, for the review that is about to decide it. The maker-checker rule
-- is not in this WHERE: the reporter's actor id comes back with the row and the service
-- compares it, so the refusal can say what it is instead of being an empty result.
SELECT id, booking_id, reported_by_actor_id, reported_at, evidence_document_id,
       assessed_fee_amount::text AS assessed_fee_amount, payer_amount::text AS payer_amount,
       member_amount::text AS member_amount, currency_code, status, reviewed_by,
       reviewed_at, review_comment, consumed_nights, created_at, updated_at, row_version
  FROM accommodation.no_show
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   FOR UPDATE;

-- name: ReviewNoShow :execrows
-- The payer's answer. The REPORTED predicate is the whole of its idempotency, and
-- `reviewed_by <> reported_by_actor_id` is a CHECK on the row underneath the service's own
-- refusal: the person who said nobody came may never be the person who decides it costs the
-- member anything.
UPDATE accommodation.no_show
   SET status = sqlc.arg('status'), reviewed_by = sqlc.arg('reviewed_by'),
       reviewed_at = sqlc.arg('reviewed_at'), review_comment = sqlc.narg('review_comment'),
       consumed_nights = sqlc.arg('consumed_nights')::int, updated_by = sqlc.narg('reviewed_by')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'REPORTED';

-- ---------------------------------------------------------------------------
-- accommodation.waitlist_entry
-- ---------------------------------------------------------------------------

-- name: CreateWaitlistEntry :one
-- `created_at` is written by the caller rather than defaulted, and that is not a formality:
-- it is the second key of the queue order, and `ReturnWaitlistEntryToQueue` moves it to send
-- an entry to the back. Both have to come from the same clock, or an entry requeued by the
-- service would sort against an entry stamped by the database and the queue would order
-- itself by whichever of the two happened to be ahead.
INSERT INTO accommodation.waitlist_entry (
    tenant_id, person_id, enrollment_id, property_id, room_type_id, check_in, check_out,
    adults, children, priority, created_at, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('person_id'), sqlc.arg('enrollment_id'),
        sqlc.arg('property_id'), sqlc.narg('room_type_id'), sqlc.arg('check_in')::date,
        sqlc.arg('check_out')::date, sqlc.arg('adults')::int, sqlc.arg('children')::int,
        sqlc.arg('priority')::int, sqlc.arg('created_at'), sqlc.narg('actor_id'),
        sqlc.narg('actor_id'))
RETURNING id, person_id, enrollment_id, property_id, room_type_id, check_in, check_out,
          adults, children, priority, status, offered_booking_id, offer_expires_at,
          created_at, updated_at, row_version;

-- name: GetWaitlistEntry :one
-- One entry inside the caller's own boundaries: `person_id` is the member boundary and
-- `scope_ids` the provider one, both applied in the WHERE so an entry outside either is not
-- found rather than forbidden.
SELECT w.id, w.person_id, w.enrollment_id, w.property_id, w.room_type_id, w.check_in,
       w.check_out, w.adults, w.children, w.priority, w.status, w.offered_booking_id,
       w.offer_expires_at, w.created_at, w.updated_at, w.row_version
  FROM accommodation.waitlist_entry w
  JOIN accommodation.property p ON p.tenant_id = w.tenant_id AND p.id = w.property_id
 WHERE w.tenant_id = sqlc.arg('tenant_id')
   AND w.id = sqlc.arg('id')
   AND (sqlc.narg('person_id')::uuid IS NULL OR w.person_id = sqlc.narg('person_id')::uuid)
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: LockWaitlistEntry :one
-- The same row FOR UPDATE, for a command about to move it. No boundary: the offer sweep
-- acts for the tenant and for no provider, and a scope here would leave its entries
-- unreachable.
SELECT id, person_id, enrollment_id, property_id, room_type_id, check_in, check_out,
       adults, children, priority, status, offered_booking_id, offer_expires_at,
       created_at, updated_at, row_version
  FROM accommodation.waitlist_entry
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   FOR UPDATE;

-- name: ListWaitlistEntries :many
-- The queue as a screen reads it, in the order the sweep walks it, so the list a member is
-- shown their place in is the list the offer actually comes out of.
SELECT w.id, w.person_id, w.enrollment_id, w.property_id, w.room_type_id, w.check_in,
       w.check_out, w.adults, w.children, w.priority, w.status, w.offered_booking_id,
       w.offer_expires_at, w.created_at, w.updated_at, w.row_version
  FROM accommodation.waitlist_entry w
  JOIN accommodation.property p ON p.tenant_id = w.tenant_id AND p.id = w.property_id
 WHERE w.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('person_id')::uuid IS NULL OR w.person_id = sqlc.narg('person_id')::uuid)
   AND (sqlc.narg('property_id')::uuid IS NULL OR w.property_id = sqlc.narg('property_id')::uuid)
   AND (sqlc.narg('status')::text IS NULL OR w.status = sqlc.narg('status')::text)
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
 ORDER BY w.priority DESC, w.created_at ASC, w.id ASC
 LIMIT sqlc.arg('page_size');

-- name: ListWaitlistQueue :many
-- The sweep's own read: the WAITING entries of one tenant, highest priority first and then
-- whoever asked first, taken FOR UPDATE SKIP LOCKED so two schedulers that both believe
-- they lead take different entries and neither waits on the other.
--
-- The ORDER BY is spelled exactly as `ix_waitlist_queue` is. A sweep that ordered by
-- `created_at` alone would be a plan clause nobody honoured, and the test that proves
-- priority wins is the one that would go red.
SELECT id, person_id, enrollment_id, property_id, room_type_id, check_in, check_out,
       adults, children, priority, status, offered_booking_id, offer_expires_at,
       created_at, updated_at, row_version
  FROM accommodation.waitlist_entry
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND status = 'WAITING'
 ORDER BY priority DESC, created_at ASC, id ASC
 LIMIT sqlc.arg('page_size')
   FOR UPDATE SKIP LOCKED;

-- name: ListExpiredWaitlistOffers :many
-- Offers nobody accepted. They are found by their own deadline rather than by the hold's,
-- because the hold may have been swept away already and the entry would then wait forever
-- for a booking that no longer holds anything.
SELECT id
  FROM accommodation.waitlist_entry
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND status = 'OFFERED'
   AND offer_expires_at <= sqlc.arg('before')
 ORDER BY offer_expires_at
 LIMIT sqlc.arg('page_size')
   FOR UPDATE SKIP LOCKED;

-- name: OfferWaitlistEntry :execrows
-- The room reached this entry. WAITING is the predicate, so a second sweep pass over an
-- entry that has already been offered one changes nothing.
UPDATE accommodation.waitlist_entry
   SET status = 'OFFERED', offered_booking_id = sqlc.arg('offered_booking_id'),
       offer_expires_at = sqlc.arg('offer_expires_at')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'WAITING';

-- name: ReturnWaitlistEntryToQueue :execrows
-- An offer nobody took. The entry goes back to WAITING *behind* everybody who was already
-- queued, and that is what moving `created_at` to now does: the queue is ordered by it, so
-- a member who let a room go waits again rather than being offered the next one first.
UPDATE accommodation.waitlist_entry
   SET status = 'WAITING', offered_booking_id = NULL, offer_expires_at = NULL,
       created_at = sqlc.arg('requeued_at')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'OFFERED';

-- name: AcceptWaitlistEntry :execrows
UPDATE accommodation.waitlist_entry
   SET status = 'ACCEPTED', offer_expires_at = NULL, updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'OFFERED';

-- name: CancelWaitlistEntry :execrows
-- The member giving up their place. Both live statuses, because an entry that was offered a
-- room the member does not want is still an entry they may leave; the hold behind it is
-- released by the command, not by this statement.
UPDATE accommodation.waitlist_entry
   SET status = 'CANCELLED', offered_booking_id = NULL, offer_expires_at = NULL,
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('WAITING','OFFERED');

-- name: ListPropertyRoomTypeIDs :many
-- The active room types of a property, in a stable order. The offer sweep walks them for an
-- entry that named no room type: "any room of this hotel" is what that member asked for,
-- and the sweep has to try each one rather than guess.
SELECT id
  FROM accommodation.room_type
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND property_id = sqlc.arg('property_id')
   AND status = 'ACTIVE'
 ORDER BY code, id;
