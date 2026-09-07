-- 000041: the booking a hold becomes (WP-I6-02, v1.2 10.6 steps 4-7, 11.11, 11.13, 12.3,
-- 13.5, 16.7).
--
-- Migration 000040 put three integers on a row and said the row decides. This migration
-- adds the thing that reads them: a booking, which is a promise of a room for fifteen
-- minutes that either becomes a stay or gives the room back.
--
-- Three sentences carry the package and all three are here rather than in Go:
--
--   * **One live booking of one room type per person and arrival.** The partial unique
--     index below says it. A service check would be a check two concurrent submits both
--     pass; the index refuses the second one whichever order they arrive in.
--   * **A booking's nights are its nights.** `nights` is a column and it is CHECKed equal
--     to `check_out - check_in`, so a service that wrote one and computed the other could
--     not commit the disagreement. The booking_night rows are counted against it by a db
--     test after every command.
--   * **What the member saw is what the booking carries.** `quote_snapshot` is frozen at
--     the hold and `policy_snapshot` at confirmation, and neither is ever recomputed:
--     pricing that changed between the search and the confirmation changes the next
--     booking, not this one.
--
-- No `booking_status_event` table. The audit log and the outbox already carry every
-- transition, as they do for requests and stays, and a third record of the same fact is a
-- third place for it to disagree.
--
-- No new permission. `accommodation.booking.create` and `accommodation.booking.manage` are
-- both grants of migration 000008, and their other half is already in
-- internal/identity/application/roles.go.

-- ---------------------------------------------------------------------------
-- accommodation.booking
-- ---------------------------------------------------------------------------
--
-- `entitlement_reservation_id` is the hold this booking took on the plan, and it is the
-- whole of the no-double-reserve rule: the reservation is taken once, at the hold, and the
-- authorization that confirmation produces ADOPTS it -- `service.authorization_item`
-- points at this same row -- rather than reserving the nights a second time. A booking
-- whose confirmation had reserved again would show a member two nights gone from a plan
-- they used once, and the ledger's own conservation test would still pass, because both
-- reservations would be real. That is why the adoption is a column here and not a comment.
--
-- `voucher_id` is nullable and stays that way for a booking nobody confirmed. What it
-- never holds is a token: the plaintext exists in exactly one response body and nowhere
-- else in this system, and `service.voucher` stores its SHA-256 digest.
CREATE TABLE accommodation.booking (
    id                         uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                  uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    reference                  text NOT NULL,
    person_id                  uuid NOT NULL,
    enrollment_id              uuid NOT NULL,
    program_id                 uuid NOT NULL,
    property_id                uuid NOT NULL,
    room_type_id               uuid NOT NULL,
    check_in                   date NOT NULL,
    check_out                  date NOT NULL,
    nights                     integer NOT NULL,
    adults                     integer NOT NULL CHECK (adults BETWEEN 1 AND 20),
    children                   integer NOT NULL DEFAULT 0 CHECK (children BETWEEN 0 AND 20),
    status                     text NOT NULL DEFAULT 'HOLD'
                               CHECK (status IN ('HOLD','PENDING_APPROVAL','CONFIRMED',
                                                 'CHECKED_IN','COMPLETED','CANCELLED',
                                                 'NO_SHOW','EXPIRED')),
    hold_expires_at            timestamptz,
    entitlement_reservation_id uuid,
    service_request_id         uuid,
    authorization_id           uuid,
    voucher_id                 uuid,
    quote_snapshot             jsonb NOT NULL,
    policy_snapshot            jsonb,
    channel                    text NOT NULL
                               CHECK (channel IN ('BACKOFFICE','PROVIDER_PORTAL','MEMBER_PORTAL',
                                                  'API','BATCH_IMPORT','CALL_CENTER')),
    confirmed_at               timestamptz,
    checked_in_at              timestamptz,
    checked_out_at             timestamptz,
    cancelled_at               timestamptz,
    cancel_reason_code         text,
    actual_nights              integer,
    created_at                 timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by                 uuid,
    updated_at                 timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by                 uuid,
    row_version                bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_booking_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_booking_reference UNIQUE (tenant_id, reference),
    CONSTRAINT fk_booking_person FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_booking_enrollment FOREIGN KEY (tenant_id, enrollment_id)
        REFERENCES benefit.enrollment(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_booking_program FOREIGN KEY (tenant_id, program_id)
        REFERENCES benefit.program(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_booking_property FOREIGN KEY (tenant_id, property_id)
        REFERENCES accommodation.property(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_booking_room_type FOREIGN KEY (tenant_id, room_type_id)
        REFERENCES accommodation.room_type(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_booking_reservation FOREIGN KEY (tenant_id, entitlement_reservation_id)
        REFERENCES benefit.entitlement_reservation(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_booking_request FOREIGN KEY (tenant_id, service_request_id)
        REFERENCES service.service_request(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_booking_authorization FOREIGN KEY (tenant_id, authorization_id)
        REFERENCES service.authorization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_booking_voucher FOREIGN KEY (tenant_id, voucher_id)
        REFERENCES service.voucher(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_booking_reference CHECK (reference ~ '^BK-[0-9]{8}-[0-9A-Z]{8}$'),
    -- A stay of no nights is not a stay, and the night count is not a second opinion: it
    -- is written by the service and CHECKed against the dates, so the two can never
    -- disagree in a committed row.
    CONSTRAINT ck_booking_period CHECK (check_out > check_in),
    CONSTRAINT ck_booking_nights CHECK (nights = (check_out - check_in)),
    -- A held booking has a deadline; a booking that has moved past the hold does not need
    -- one and is not required to have lost it. What is refused is the opposite: a HOLD
    -- with no expiry would be a room set aside forever that no sweep would ever find.
    CONSTRAINT ck_booking_hold_expiry CHECK (
        status <> 'HOLD' OR hold_expires_at IS NOT NULL
    ),
    -- A confirmed stay has a moment it was confirmed, and one that was never confirmed has
    -- none. Both halves, because both are what the outbox subscriber writes and a
    -- redelivery that half-wrote its answer is exactly what this refuses.
    CONSTRAINT ck_booking_confirmed CHECK (
        (status IN ('CONFIRMED','CHECKED_IN','COMPLETED','NO_SHOW')) = (confirmed_at IS NOT NULL)
    ),
    CONSTRAINT ck_booking_cancelled CHECK (
        (status = 'CANCELLED') = (cancelled_at IS NOT NULL)
    ),
    CONSTRAINT ck_booking_checked_in CHECK (
        checked_in_at IS NOT NULL OR status NOT IN ('CHECKED_IN','COMPLETED')
    ),
    CONSTRAINT ck_booking_checked_out CHECK (
        checked_out_at IS NULL OR checked_in_at IS NOT NULL
    ),
    CONSTRAINT ck_booking_actual_nights CHECK (actual_nights IS NULL OR actual_nights >= 0),
    CONSTRAINT ck_booking_cancel_reason CHECK (
        cancel_reason_code IS NULL OR cancel_reason_code ~ '^[A-Z][A-Z0-9_]{1,63}$'
    ),
    CONSTRAINT ck_booking_quote_snapshot CHECK (jsonb_typeof(quote_snapshot) = 'object'),
    CONSTRAINT ck_booking_policy_snapshot CHECK (
        policy_snapshot IS NULL OR jsonb_typeof(policy_snapshot) = 'object'
    )
);

-- One live booking of one room type per person and arrival.
--
-- It is a partial unique index rather than a check in the service because the two callers
-- it exists to separate arrive at the same instant: a double-clicked confirm, or two tabs.
-- Both read "nothing yet", both would pass a service check, and the index is what refuses
-- the second insert. The predicate lists the four live statuses -- a cancelled, expired or
-- completed booking is history and must not stop the member booking the same room again.
CREATE UNIQUE INDEX uq_booking_live_per_person_arrival
    ON accommodation.booking (tenant_id, person_id, room_type_id, check_in)
 WHERE status IN ('HOLD','PENDING_APPROVAL','CONFIRMED','CHECKED_IN');

-- The reads: a member's own list, a property's arrivals, the expiry sweep and the
-- reminder sweep. The sweeps are partial indexes because they walk a handful of rows out
-- of a table that grows forever, and a full scan every minute is a full scan every minute.
CREATE INDEX ix_booking_person ON accommodation.booking (tenant_id, person_id, check_in DESC);
CREATE INDEX ix_booking_property ON accommodation.booking (tenant_id, property_id, check_in);
CREATE INDEX ix_booking_request ON accommodation.booking (tenant_id, service_request_id)
    WHERE service_request_id IS NOT NULL;
CREATE INDEX ix_booking_hold_expiry ON accommodation.booking (tenant_id, hold_expires_at)
    WHERE status = 'HOLD';
CREATE INDEX ix_booking_reminder ON accommodation.booking (tenant_id, check_in)
    WHERE status IN ('CONFIRMED','PENDING_APPROVAL');

SELECT platform.attach_touch_row('accommodation.booking'::regclass);
SELECT platform.enable_tenant_rls('accommodation.booking'::regclass);

-- ---------------------------------------------------------------------------
-- accommodation.booking_night
-- ---------------------------------------------------------------------------
--
-- One row per night of the stay, carrying the amounts the member was shown. They are a
-- copy of the quote and not a reference to a price list, because a price list changes and
-- an agreed stay does not: what these rows say is what somebody agreed to pay, and it has
-- to be readable a year later without the contract being re-resolved.
--
-- `payer_amount + member_amount = unit_amount` is a CHECK on the row, which is the same
-- invariant internal/pricing produces line by line. It is asserted here because these
-- three numbers reach an invoice, and an invoice that is a kuruş out is an invoice nobody
-- can reconcile and nobody can explain.
CREATE TABLE accommodation.booking_night (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id     uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    booking_id    uuid NOT NULL,
    stay_date     date NOT NULL,
    room_type_id  uuid NOT NULL,
    unit_amount   numeric(20,6) NOT NULL CHECK (unit_amount >= 0),
    payer_amount  numeric(20,6) NOT NULL CHECK (payer_amount >= 0),
    member_amount numeric(20,6) NOT NULL CHECK (member_amount >= 0),
    currency_code char(3) NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_booking_night_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_booking_night_date UNIQUE (tenant_id, booking_id, stay_date),
    CONSTRAINT fk_booking_night_booking FOREIGN KEY (tenant_id, booking_id)
        REFERENCES accommodation.booking(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_booking_night_room_type FOREIGN KEY (tenant_id, room_type_id)
        REFERENCES accommodation.room_type(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_booking_night_split CHECK (payer_amount + member_amount = unit_amount),
    CONSTRAINT ck_booking_night_currency CHECK (currency_code ~ '^[A-Z]{3}$')
);
CREATE INDEX ix_booking_night_booking ON accommodation.booking_night (tenant_id, booking_id, stay_date);

SELECT platform.enable_tenant_rls('accommodation.booking_night'::regclass);

-- ---------------------------------------------------------------------------
-- accommodation.booking_guest
-- ---------------------------------------------------------------------------
--
-- Who is sleeping in the room. `person_id` is set for a member or a dependant the tenant
-- already knows; `display_name` is the snapshot of a guest it does not, and it is a name
-- and only ever a name.
--
-- There is no identifier column here and there never will be one. A national id, a
-- passport number or a telephone number belongs in party.person behind the field cipher,
-- and a column on this table that a provider clerk types into would be plaintext personal
-- data in a table nobody thought of as holding any. `is_minor` is a boolean rather than a
-- date of birth for the same reason: the room needs to know a child is in it, and nothing
-- here needs to know when they were born.
CREATE TABLE accommodation.booking_guest (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id    uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    booking_id   uuid NOT NULL,
    person_id    uuid,
    display_name text NOT NULL,
    guest_type   text NOT NULL CHECK (guest_type IN ('MEMBER','DEPENDANT','GUEST')),
    is_minor     boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_booking_guest_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_booking_guest_booking FOREIGN KEY (tenant_id, booking_id)
        REFERENCES accommodation.booking(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_booking_guest_person FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_booking_guest_name CHECK (length(display_name) BETWEEN 1 AND 200),
    -- A guest the tenant knows is a person; a guest it does not has no person id. The
    -- pairing is asserted so nobody can file a stranger under a member's row.
    CONSTRAINT ck_booking_guest_person_type CHECK (
        (guest_type = 'GUEST' AND person_id IS NULL)
        OR (guest_type <> 'GUEST' AND person_id IS NOT NULL)
    )
);
-- One person appears once in one booking. Two rows for the same guest would be an
-- occupancy count that is wrong by one, on a room whose maximum occupancy is a CHECK.
CREATE UNIQUE INDEX uq_booking_guest_person
    ON accommodation.booking_guest (tenant_id, booking_id, person_id)
 WHERE person_id IS NOT NULL;
CREATE INDEX ix_booking_guest_booking ON accommodation.booking_guest (tenant_id, booking_id);

SELECT platform.enable_tenant_rls('accommodation.booking_guest'::regclass);

-- The grant is repeated because ON ALL TABLES is a snapshot: migration 000040's call
-- covered the tables that existed then, and the three above did not.
SELECT platform.grant_app_schema_usage('accommodation');
