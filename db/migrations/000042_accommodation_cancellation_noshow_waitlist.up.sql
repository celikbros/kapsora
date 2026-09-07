-- 000042: what happens after the promise (WP-I6-03, v1.2 10.6 step 8, 10.7, 12.3, 16.7,
-- 35.2).
--
-- Migration 000041 wrote the booking. This one writes the three things that can happen to
-- one after it is agreed: the member cancels, the member does not come, or somebody else
-- was waiting for the room.
--
-- Three sentences carry the package and all three are here rather than in Go:
--
--   * **A cancellation is judged by the policy the booking was confirmed under.**
--     `accommodation.cancellation.policy_snapshot` is a copy of the booking's own frozen
--     policy, written onto the cancellation row, so the row itself proves what it was
--     judged by. A later edit of the contract cannot change what a member already agreed
--     to, and nothing has to be re-resolved to read a two-year-old cancellation.
--   * **A no-show costs the member nothing until a second person confirms it.** The
--     `no_show` row carries the provider's claim, its evidence and the assessed fee; the
--     booking stays CONFIRMED and no entitlement moves until `status` becomes CONFIRMED,
--     and `ck_no_show_reviewer_differs` refuses the reporter reviewing their own report.
--   * **A freed room reaches the queue in queue order.** `ix_waitlist_queue` is
--     `(priority DESC, created_at ASC)`, which is the order the offer sweep walks, and
--     `uq_waitlist_live_entry` is what stops one person holding two places in it.
--
-- Every money value is `numeric(20,6)` and `payer + member = the whole` is a CHECK on the
-- row, exactly as `accommodation.booking_night` states it: these figures reach a
-- settlement (M7), and a fee two systems disagree about by a kuruş is a fee nobody can
-- invoice.
--
-- Nothing here is ever deleted. `accommodation.cancellation` is append-only --
-- platform.make_append_only says so, the way benefit.entitlement_ledger does -- and a
-- no-show that was wrong is REJECTED rather than removed.

-- ---------------------------------------------------------------------------
-- The permission the waitlist desk holds
-- ---------------------------------------------------------------------------
--
-- NORMAL, because a waitlist entry is a member asking to be told about a room. Its other
-- half is in internal/identity/application/roles.go and a test asserts the two agree:
-- PROVIDER_RESERVATION runs the desk at the property and PROGRAM_MANAGER runs it at the
-- payer. A member needs none of it -- they join under `accommodation.booking.create`,
-- which is the grant they already hold to book for themselves.
INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('accommodation.waitlist.manage', 'Konaklama bekleme listesi yönetimi', 'NORMAL')
ON CONFLICT (code) DO NOTHING;

-- ---------------------------------------------------------------------------
-- accommodation.booking gains the over-stay flag
-- ---------------------------------------------------------------------------
--
-- A guest who stayed longer than the plan promised has already had the extra nights: the
-- check-out cannot refuse, and it must not consume beyond what was authorized either.
-- What it does instead is say so, once, on the booking, for M7's claim to raise as an
-- exception. It is a flag rather than a comparison a reader makes, because what was
-- exceeded is the authorization's approved quantity, and that number is not on this row.
ALTER TABLE accommodation.booking
    ADD COLUMN over_booking boolean NOT NULL DEFAULT false;

-- ---------------------------------------------------------------------------
-- A cancelled stay keeps the fact that it was once confirmed
-- ---------------------------------------------------------------------------
--
-- Migration 000041 stated `confirmed_at IS NOT NULL` as an *equivalence* with the four
-- post-confirmation statuses, which was right while the only cancellable booking was a hold
-- nobody had agreed to. It stops being right here: a member may now call off a stay that was
-- confirmed, and the row that results is CANCELLED **and** was confirmed on a particular day.
--
-- Clearing `confirmed_at` to satisfy the old check would be erasing the fact the whole
-- cancellation hangs off -- the fee is judged against a policy frozen at that moment -- so the
-- equivalence is split into its two halves instead. A status that could only be reached
-- through a confirmation still requires the moment; a status that could only be reached
-- without one still forbids it; and CANCELLED, the one status reachable from both sides, is
-- allowed either.
ALTER TABLE accommodation.booking DROP CONSTRAINT ck_booking_confirmed;
ALTER TABLE accommodation.booking
    ADD CONSTRAINT ck_booking_confirmed CHECK (
        status NOT IN ('CONFIRMED','CHECKED_IN','COMPLETED','NO_SHOW')
        OR confirmed_at IS NOT NULL
    ),
    ADD CONSTRAINT ck_booking_unconfirmed CHECK (
        status NOT IN ('HOLD','PENDING_APPROVAL','EXPIRED') OR confirmed_at IS NULL
    );

-- ---------------------------------------------------------------------------
-- accommodation.cancellation
-- ---------------------------------------------------------------------------
--
-- One row per cancelled booking, append-only. It is a table and not three columns on the
-- booking because it carries the policy the cancellation was judged by, and a policy
-- copied onto the booking would be indistinguishable from the policy the booking was
-- confirmed under -- which is the one thing this row exists to prove was used.
--
-- `free` is stored rather than derived. Whether the member was inside the free window
-- depended on the moment they cancelled and on the property's own zone, and recomputing
-- it a year later from a snapshot and a timestamp is exactly the arithmetic this row
-- exists to make unnecessary.
CREATE TABLE accommodation.cancellation (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id       uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    booking_id      uuid NOT NULL,
    cancelled_at    timestamptz NOT NULL,
    cancelled_by    uuid,
    reason_code     text NOT NULL,
    -- The booking's own frozen policy, copied here so the row proves what it was judged
    -- by. It is NOT NULL: a cancellation with no policy behind it would be a fee nobody
    -- could explain, and the command refuses before it ever reaches this table.
    policy_snapshot jsonb NOT NULL,
    free            boolean NOT NULL,
    penalty_nights  integer NOT NULL DEFAULT 0 CHECK (penalty_nights >= 0),
    released_nights integer NOT NULL DEFAULT 0 CHECK (released_nights >= 0),
    fee_amount      numeric(20,6) NOT NULL DEFAULT 0 CHECK (fee_amount >= 0),
    payer_fee       numeric(20,6) NOT NULL DEFAULT 0 CHECK (payer_fee >= 0),
    member_fee      numeric(20,6) NOT NULL DEFAULT 0 CHECK (member_fee >= 0),
    currency_code   char(3) NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_cancellation_id_tenant UNIQUE (tenant_id, id),
    -- One cancellation per booking. Running the command twice is one cancellation, and
    -- this index is what makes that a guarantee rather than a hope: the status predicate
    -- on the booking's own update refuses the second attempt, and if that predicate were
    -- removed tomorrow the second insert would still fail here.
    CONSTRAINT uq_cancellation_booking UNIQUE (tenant_id, booking_id),
    CONSTRAINT fk_cancellation_booking FOREIGN KEY (tenant_id, booking_id)
        REFERENCES accommodation.booking(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_cancellation_reason CHECK (reason_code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    CONSTRAINT ck_cancellation_currency CHECK (currency_code ~ '^[A-Z]{3}$'),
    -- The invariant every money row in this system carries: the two shares are the whole.
    CONSTRAINT ck_cancellation_fee_split CHECK (payer_fee + member_fee = fee_amount),
    -- A free cancellation costs nothing and takes no night. Both halves, because either
    -- one alone would let a row say "free" and charge for it anyway.
    CONSTRAINT ck_cancellation_free_costs_nothing CHECK (
        NOT free OR (fee_amount = 0 AND penalty_nights = 0)
    ),
    CONSTRAINT ck_cancellation_policy_snapshot CHECK (jsonb_typeof(policy_snapshot) = 'object')
);
CREATE INDEX ix_cancellation_booking ON accommodation.cancellation (tenant_id, booking_id);
CREATE INDEX ix_cancellation_at ON accommodation.cancellation (tenant_id, cancelled_at DESC);

SELECT platform.enable_tenant_rls('accommodation.cancellation'::regclass);
-- Append-only, stated by the database. A cancellation is the record of what a member was
-- told they would be charged; a row somebody could edit afterwards is not a record of
-- anything.
SELECT platform.make_append_only('accommodation.cancellation');

-- ---------------------------------------------------------------------------
-- accommodation.no_show
-- ---------------------------------------------------------------------------
--
-- The provider's claim that nobody came, and the payer's answer to it.
--
-- `booking_id` is unique: a provider reports a no-show once, and a report that was refused
-- is REJECTED rather than deleted, so a second one cannot quietly replace it.
--
-- `evidence_document_id` points at a WP-I4-04 document object linked to the booking. It is
-- nullable in the column and required by the command, and the two are not in conflict: a
-- report whose evidence retention later purged would otherwise become a row nobody could
-- write the review onto.
CREATE TABLE accommodation.no_show (
    id                   uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id            uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    booking_id           uuid NOT NULL,
    reported_by_actor_id uuid,
    reported_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    evidence_document_id uuid,
    assessed_fee_amount  numeric(20,6) NOT NULL DEFAULT 0 CHECK (assessed_fee_amount >= 0),
    payer_amount         numeric(20,6) NOT NULL DEFAULT 0 CHECK (payer_amount >= 0),
    member_amount        numeric(20,6) NOT NULL DEFAULT 0 CHECK (member_amount >= 0),
    currency_code        char(3) NOT NULL,
    status               text NOT NULL DEFAULT 'REPORTED'
                         CHECK (status IN ('REPORTED','CONFIRMED','DISPUTED','REJECTED')),
    reviewed_by          uuid,
    reviewed_at          timestamptz,
    review_comment       text,
    -- What the confirmation actually took off the plan, in nights. It is recorded because
    -- the policy is a percentage and the entitlement is whole nights: the row has to say
    -- what was consumed rather than leave a reader to redo the rounding.
    consumed_nights      integer NOT NULL DEFAULT 0 CHECK (consumed_nights >= 0),
    created_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by           uuid,
    updated_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by           uuid,
    row_version          bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_no_show_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_no_show_booking UNIQUE (tenant_id, booking_id),
    CONSTRAINT fk_no_show_booking FOREIGN KEY (tenant_id, booking_id)
        REFERENCES accommodation.booking(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_no_show_evidence FOREIGN KEY (tenant_id, evidence_document_id)
        REFERENCES document.object(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_no_show_currency CHECK (currency_code ~ '^[A-Z]{3}$'),
    CONSTRAINT ck_no_show_fee_split CHECK (payer_amount + member_amount = assessed_fee_amount),
    -- A decided report has a decider and a moment; an undecided one has neither. Both
    -- halves, because a row that said CONFIRMED with no reviewer would be a fee nobody
    -- signed for.
    CONSTRAINT ck_no_show_reviewed CHECK (
        (status = 'REPORTED') = (reviewed_at IS NULL)
        AND (reviewed_at IS NULL) = (reviewed_by IS NULL)
    ),
    -- The maker-checker rule, in the database. A provider clerk may report that nobody
    -- came and may never be the person who decides it costs the member anything: the
    -- service refuses it with a message a person can read, and this refuses it whatever
    -- reaches the table.
    CONSTRAINT ck_no_show_reviewer_differs CHECK (
        reviewed_by IS NULL OR reported_by_actor_id IS NULL
        OR reviewed_by <> reported_by_actor_id
    ),
    CONSTRAINT ck_no_show_comment CHECK (
        review_comment IS NULL OR length(review_comment) BETWEEN 1 AND 2000
    )
);
CREATE INDEX ix_no_show_status ON accommodation.no_show (tenant_id, status, reported_at DESC);

SELECT platform.attach_touch_row('accommodation.no_show'::regclass);
SELECT platform.enable_tenant_rls('accommodation.no_show'::regclass);

-- ---------------------------------------------------------------------------
-- accommodation.waitlist_entry
-- ---------------------------------------------------------------------------
--
-- One member waiting for a room that is full. `room_type_id` NULL means any room of the
-- property, which is what a member who wants the hotel rather than the suite is actually
-- asking for.
--
-- `priority` is an integer a plan may raise -- a programme that gives its own people the
-- first refusal -- and it is the first key of the queue order. FIFO within a priority is
-- the second, and it is `created_at` rather than the id, so an entry that was offered a
-- room and did not take it goes to the back of the queue by moving one timestamp.
CREATE TABLE accommodation.waitlist_entry (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id          uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    person_id          uuid NOT NULL,
    enrollment_id      uuid NOT NULL,
    property_id        uuid NOT NULL,
    room_type_id       uuid,
    check_in           date NOT NULL,
    check_out          date NOT NULL,
    adults             integer NOT NULL CHECK (adults BETWEEN 1 AND 20),
    children           integer NOT NULL DEFAULT 0 CHECK (children BETWEEN 0 AND 20),
    priority           integer NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 1000),
    status             text NOT NULL DEFAULT 'WAITING'
                       CHECK (status IN ('WAITING','OFFERED','ACCEPTED','EXPIRED','CANCELLED')),
    offered_booking_id uuid,
    offer_expires_at   timestamptz,
    created_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by         uuid,
    updated_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by         uuid,
    row_version        bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_waitlist_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_waitlist_person FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_waitlist_enrollment FOREIGN KEY (tenant_id, enrollment_id)
        REFERENCES benefit.enrollment(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_waitlist_property FOREIGN KEY (tenant_id, property_id)
        REFERENCES accommodation.property(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_waitlist_room_type FOREIGN KEY (tenant_id, room_type_id)
        REFERENCES accommodation.room_type(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_waitlist_booking FOREIGN KEY (tenant_id, offered_booking_id)
        REFERENCES accommodation.booking(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_waitlist_period CHECK (check_out > check_in),
    -- An offer is a booking and a deadline, together or not at all: an entry that said
    -- OFFERED with no booking would be a member told to come and collect nothing. An entry
    -- that accepted its offer keeps the booking and loses the deadline, because the
    -- countdown is over.
    CONSTRAINT ck_waitlist_offer CHECK (
        CASE status
            WHEN 'OFFERED' THEN offered_booking_id IS NOT NULL AND offer_expires_at IS NOT NULL
            WHEN 'ACCEPTED' THEN offered_booking_id IS NOT NULL
            ELSE true
        END
    )
);

-- One live place in the queue per person, property and arrival.
--
-- It is a partial unique index rather than a check in the service for the reason migration
-- 000041 gives for the booking's: the two callers it separates arrive at the same instant,
-- both read "nothing yet", and the index is what refuses the second insert. A cancelled or
-- expired entry is history and must not stop the member joining again.
CREATE UNIQUE INDEX uq_waitlist_live_entry
    ON accommodation.waitlist_entry (tenant_id, person_id, property_id, check_in)
 WHERE status IN ('WAITING','OFFERED');

-- The order the offer sweep walks: highest priority first, then whoever asked first. The
-- index and the sweep's own ORDER BY are spelled the same way, because a queue that
-- ignored priority would be a plan clause nobody honoured.
CREATE INDEX ix_waitlist_queue
    ON accommodation.waitlist_entry (tenant_id, property_id, room_type_id, priority DESC, created_at ASC)
 WHERE status = 'WAITING';
CREATE INDEX ix_waitlist_person ON accommodation.waitlist_entry (tenant_id, person_id, created_at DESC);
-- The sweep's other read: the offers whose deadline has passed, which go back to WAITING
-- behind everybody who was already in the queue.
CREATE INDEX ix_waitlist_offer_expiry ON accommodation.waitlist_entry (tenant_id, offer_expires_at)
 WHERE status = 'OFFERED';

SELECT platform.attach_touch_row('accommodation.waitlist_entry'::regclass);
SELECT platform.enable_tenant_rls('accommodation.waitlist_entry'::regclass);

-- The grant is repeated because ON ALL TABLES is a snapshot: migration 000041's call
-- covered the tables that existed then, and the three above did not.
SELECT platform.grant_app_schema_usage('accommodation');
