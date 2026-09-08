-- 000043: the claim as the one billable unit of the platform, and the adjustment as a
-- ledger line (WP-I7-01, v1.2 9.14, 10.8, 16.8, 39).
--
-- M5 built the claim for the health vertical and stopped at "approved and invoice-ready".
-- Two facts have to be true for every vertical before an invoice can be raised against a
-- claim, and both of them are constraints here rather than habits in a service:
--
--   * **a claim knows what it came from.** `case_id` answered that for exactly one vertical
--     and answered nothing at all for a stay or a reimbursement. `source_type` and
--     `source_id` answer it for all three, the referenced row is checked per type by a
--     trigger, and a partial unique index makes "one live claim per booking" a guarantee
--     rather than a hope in an outbox handler that is delivered at least once.
--   * **money that moved outside a line decision moved with a split and a reason.** A cut, a
--     recovery, a correction and the fee of a cancelled stay are one kind of row, they carry
--     `payer_amount + member_amount = amount` as a CHECK, and nothing undoes one except a
--     REVERSAL that points back at it. Nothing is edited and nothing is deleted, which is
--     what makes the approved total a figure two systems can agree on years later.
--
-- What is deliberately not here: the invoice. The claim's readiness answers what an invoice
-- would need; WP-I7-02 owns the link, and nothing in this migration knows an invoice exists.

-- ---------------------------------------------------------------------------
-- claim.claim: where the claim came from
-- ---------------------------------------------------------------------------
--
-- Both columns are nullable together, because a claim raised by hand against no episode of
-- care, no stay and no reimbursement request is an ordinary claim: a provider billing a
-- single outpatient visit has nothing to name, and demanding a source would make the source
-- paperwork rather than a record.
--
-- `case_id` stays. It is the column two years of health claims were written under, three
-- indexes and one FK stand on it, and dropping it would mean rewriting readers that have
-- nothing to do with this package. It is backfilled into the pair below and the two are kept
-- consistent by the service; the pair is what M6 and M7 read.
ALTER TABLE claim.claim
    ADD COLUMN source_type text,
    ADD COLUMN source_id   uuid;

-- Every existing row is a health claim, and the ones that named a case name it again in the
-- new pair. A claim with no case had no source and still has none.
--
-- The policy is lifted for the length of the statement because it is FORCEd: a backfill that
-- ran under `tenant_isolation` with no `app.tenant_id` set would match no row at all and
-- would leave every existing claim without the source it is about to be constrained to have
-- — silently, which is the worst way for a migration to be wrong.
ALTER TABLE claim.claim NO FORCE ROW LEVEL SECURITY;
UPDATE claim.claim
   SET source_type = 'HEALTH_CASE', source_id = case_id
 WHERE case_id IS NOT NULL;
ALTER TABLE claim.claim FORCE ROW LEVEL SECURITY;

ALTER TABLE claim.claim
    ADD CONSTRAINT ck_claim_source_type CHECK (
        source_type IS NULL
        OR source_type IN ('HEALTH_CASE', 'BOOKING', 'REIMBURSEMENT')
    ),
    -- Both halves or neither. A type with no id names nothing and an id with no type is an
    -- id nobody can look up, and either one alone would be a source that reads as present.
    ADD CONSTRAINT ck_claim_source_pair CHECK ((source_type IS NULL) = (source_id IS NULL)),
    -- The old column and the new pair say the same thing or the old column says nothing.
    -- A HEALTH_CASE claim whose case_id pointed somewhere else would be two answers to
    -- "which episode of care is this", and the second one is the one a report would read.
    ADD CONSTRAINT ck_claim_case_matches_source CHECK (
        case_id IS NULL
        OR (source_type = 'HEALTH_CASE' AND source_id = case_id)
    );

-- The composite foreign key a `source_type` column cannot be given. There is no shape in
-- SQL for "references one of three tables depending on a neighbouring column", so it is a
-- trigger — and a trigger rather than an application check because an outbox handler, a
-- backfill and a psql session all reach this table, and only one of them runs the service.
--
-- The lookup is bounded by `NEW.tenant_id`, so a claim can never name a source of another
-- tenant even if RLS were somehow not bound for the statement.
CREATE OR REPLACE FUNCTION claim.tg_claim_source_exists() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    found boolean;
BEGIN
    IF NEW.source_type IS NULL THEN
        RETURN NEW;
    END IF;
    CASE NEW.source_type
        WHEN 'HEALTH_CASE' THEN
            SELECT EXISTS (
                SELECT 1 FROM health.health_case c
                 WHERE c.tenant_id = NEW.tenant_id AND c.id = NEW.source_id
            ) INTO found;
        WHEN 'BOOKING' THEN
            SELECT EXISTS (
                SELECT 1 FROM accommodation.booking b
                 WHERE b.tenant_id = NEW.tenant_id AND b.id = NEW.source_id
            ) INTO found;
        WHEN 'REIMBURSEMENT' THEN
            -- A reimbursement is a service request of that type (migration 000006). There is
            -- no separate table and there does not need to be one: what the member asked to
            -- be paid back for is the request, and the claim is what the payer answers it
            -- with.
            SELECT EXISTS (
                SELECT 1 FROM service.service_request r
                 WHERE r.tenant_id = NEW.tenant_id AND r.id = NEW.source_id
                   AND r.request_type = 'REIMBURSEMENT'
            ) INTO found;
        ELSE
            found := false;
    END CASE;
    IF NOT found THEN
        RAISE EXCEPTION 'claim source % % not found in tenant %',
            NEW.source_type, NEW.source_id, NEW.tenant_id
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tg_claim_source_exists
    BEFORE INSERT OR UPDATE OF source_type, source_id, tenant_id ON claim.claim
    FOR EACH ROW EXECUTE FUNCTION claim.tg_claim_source_exists();

-- **One live claim per booking.** The lodging claim is created by an outbox handler, and an
-- outbox delivers at least once: the handler is written to find the claim the first delivery
-- made, and this index is what makes that a guarantee rather than a race between two
-- deliveries that both looked first. A cancelled or rejected claim is not live, so a stay
-- whose first claim was refused can be claimed again.
CREATE UNIQUE INDEX uq_claim_live_booking
    ON claim.claim (tenant_id, source_type, source_id)
 WHERE source_type = 'BOOKING' AND status NOT IN ('CANCELLED', 'REJECTED');

-- The read every source-driven handler makes: "is there already a claim for this thing".
CREATE INDEX ix_claim_source
    ON claim.claim (tenant_id, source_type, source_id) WHERE source_type IS NOT NULL;

-- ---------------------------------------------------------------------------
-- claim.adjustment: the split, the line, the source and the reversal
-- ---------------------------------------------------------------------------
--
-- The table is append-only, which is exactly why the columns below are added rather than the
-- rows rewritten. The backfill underneath has to lift the trigger for the length of one
-- statement; that is the only UPDATE this table will ever see, and it runs before the
-- invariant it establishes is asserted.
ALTER TABLE claim.adjustment DISABLE TRIGGER tg_append_only;

ALTER TABLE claim.adjustment
    -- The split. It is two columns and not a percentage because the two shares reach two
    -- different documents — the payer's invoice and the member's statement — and a
    -- percentage would be rounded twice, once for each of them, into two figures that do
    -- not add up to the row they came from.
    --
    -- Neither column carries a `>= 0` CHECK, because the amount does not: a CORRECTION may
    -- go either way and a REVERSAL is by definition the negative of what it reverses. What
    -- is asserted is the only thing that has to be true, which is that the two halves are
    -- the whole.
    ADD COLUMN payer_amount  numeric(20,6) NOT NULL DEFAULT 0,
    ADD COLUMN member_amount numeric(20,6) NOT NULL DEFAULT 0,
    -- The line a line-level adjustment names. NULL is ordinary and is the claim-level case:
    -- a recovery of an overpayment is about the claim, not about one of its lines.
    ADD COLUMN claim_line_id uuid,
    -- Where the money came from, in the same vocabulary for every vertical. A fee row of
    -- WP-I6-03 and a reviewer's cut of WP-I7-03 are the same kind of thing, and a settlement
    -- that had to know which module wrote a row would be a settlement with a module list in
    -- it.
    ADD COLUMN source_type text NOT NULL DEFAULT 'REVIEW',
    ADD COLUMN source_id   uuid,
    -- The only way to undo an adjustment. Nothing here is edited and nothing is deleted: an
    -- adjustment taken back is a second row that says so, and both of them stay on the
    -- record because "who cut this and who put it back" is what an appeal is answered from.
    ADD COLUMN reverses_adjustment_id uuid;

-- Everything written before this migration was a reviewer's cut of the payer's own money:
-- `writeCut` is the only writer WP-I5-04 shipped, it runs at a review stage, and the figure
-- it records is what the payer took off a line it accepted. The member's share of such a cut
-- is nothing, which is what the split says once it is written down.
ALTER TABLE claim.adjustment NO FORCE ROW LEVEL SECURITY;
UPDATE claim.adjustment
   SET payer_amount = amount, member_amount = 0, source_type = 'REVIEW';
ALTER TABLE claim.adjustment FORCE ROW LEVEL SECURITY;

ALTER TABLE claim.adjustment ENABLE TRIGGER tg_append_only;

ALTER TABLE claim.adjustment
    ALTER COLUMN payer_amount DROP DEFAULT,
    ALTER COLUMN member_amount DROP DEFAULT,
    ALTER COLUMN source_type DROP DEFAULT;

ALTER TABLE claim.adjustment
    -- The invariant the whole settlement rests on, in the same words `claim.line_decision`
    -- and `accommodation.booking_night` use. A service that rounded the two halves
    -- independently fails here by a kuruş rather than silently publishing an invoice nobody
    -- can reconcile.
    ADD CONSTRAINT ck_claim_adjustment_split CHECK (payer_amount + member_amount = amount),
    ADD CONSTRAINT ck_claim_adjustment_source_type CHECK (
        source_type IN ('CANCELLATION', 'NO_SHOW', 'REVIEW', 'MANUAL', 'RECOVERY')
    ),
    -- The two system sources name the fee row they came from; the three a person raises name
    -- nothing, because there is nothing to name. Both halves, so a manual row cannot smuggle
    -- an id nobody checks and a fee row cannot arrive without one.
    ADD CONSTRAINT ck_claim_adjustment_source_id CHECK (
        (source_type IN ('CANCELLATION', 'NO_SHOW')) = (source_id IS NOT NULL)
    ),
    -- Who did it. It is NULL exactly for the two sources no person is behind: the fee
    -- adjustments are written by the outbox handler of a check-out, a confirmed no-show or a
    -- penalised cancellation, and naming the reviewer who confirmed the no-show would say
    -- they wrote a claim line they have never seen. Every other row names an actor, which is
    -- the half of `created_by NOT NULL` that is true.
    ADD CONSTRAINT ck_claim_adjustment_actor CHECK (
        created_by IS NOT NULL OR source_type IN ('CANCELLATION', 'NO_SHOW')
    ),
    -- A REVERSAL points at what it reverses and nothing else does. Both halves: a CUT
    -- carrying a `reverses_adjustment_id` would be a cut that quietly undid something.
    ADD CONSTRAINT ck_claim_adjustment_reversal CHECK (
        (adjustment_type = 'REVERSAL') = (reverses_adjustment_id IS NOT NULL)
    ),
    ADD CONSTRAINT fk_claim_adjustment_line FOREIGN KEY (tenant_id, claim_line_id)
        REFERENCES claim.claim_line(tenant_id, id) ON DELETE RESTRICT,
    ADD CONSTRAINT fk_claim_adjustment_reverses FOREIGN KEY (tenant_id, reverses_adjustment_id)
        REFERENCES claim.adjustment(tenant_id, id) ON DELETE RESTRICT;

-- REVERSAL joins the word list. It is not a fourth kind of money — it is the negative of one
-- of the three — but it has to be nameable, because a settlement summing the column has to be
-- able to say which row put a cut back.
ALTER TABLE claim.adjustment DROP CONSTRAINT ck_claim_adjustment_sign;
-- The old word list was written inline in migration 000034 and therefore carries the name
-- PostgreSQL chose for it. It is found by what it says rather than by what it is called,
-- because a constraint dropped by a guessed name is a migration that fails on the one
-- database whose name was generated differently.
DO $$
DECLARE
    old_check text;
BEGIN
    SELECT c.conname INTO old_check
      FROM pg_constraint c
     WHERE c.conrelid = 'claim.adjustment'::regclass
       AND c.contype = 'c'
       AND pg_get_constraintdef(c.oid) LIKE '%adjustment_type%'
       AND c.conname <> 'ck_claim_adjustment_reversal';
    IF old_check IS NULL THEN
        RAISE EXCEPTION 'claim.adjustment has no adjustment_type check to replace';
    END IF;
    EXECUTE format('ALTER TABLE claim.adjustment DROP CONSTRAINT %I', old_check);
END
$$;

ALTER TABLE claim.adjustment
    ADD CONSTRAINT ck_claim_adjustment_type CHECK (
        adjustment_type IN ('CUT', 'RECOVERY', 'CORRECTION', 'REVERSAL')
    ),
    -- A cut takes money away and a recovery takes money back; a correction and a reversal may
    -- go either way. Signing them here means a settlement can sum the column without a CASE.
    ADD CONSTRAINT ck_claim_adjustment_sign CHECK (
        adjustment_type IN ('CORRECTION', 'REVERSAL') OR amount >= 0
    );

-- **One reversal per adjustment.** Two reversals of one cut would give the money back twice,
-- and the approved total would depend on how many times somebody pressed the button.
CREATE UNIQUE INDEX uq_claim_adjustment_reversal
    ON claim.adjustment (tenant_id, reverses_adjustment_id)
 WHERE reverses_adjustment_id IS NOT NULL;

CREATE INDEX ix_claim_adjustment_line
    ON claim.adjustment (tenant_id, claim_line_id) WHERE claim_line_id IS NOT NULL;
CREATE INDEX ix_claim_adjustment_source
    ON claim.adjustment (tenant_id, source_type, source_id) WHERE source_id IS NOT NULL;

-- What a CHECK cannot say, because it is about another row: a reversal reverses an
-- adjustment of its own claim, and it never reverses a reversal.
--
-- The second half is the one that matters. A reversal of a reversal is not a redo — it is a
-- reader having to walk a chain of unknown length to find out what a claim is worth, and the
-- answer differing by where they stopped. One adjustment, one reversal, and a reviewer who
-- has changed their mind twice writes a new adjustment rather than un-undoing one.
--
-- The line, when one is named, belongs to the claim as well: an adjustment against another
-- claim's line would be money moved on a document nobody can find it from.
CREATE OR REPLACE FUNCTION claim.tg_adjustment_links() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    reversed_claim uuid;
    reversed_type  text;
    line_claim     uuid;
BEGIN
    IF NEW.reverses_adjustment_id IS NOT NULL THEN
        SELECT a.claim_id, a.adjustment_type INTO reversed_claim, reversed_type
          FROM claim.adjustment a
         WHERE a.tenant_id = NEW.tenant_id AND a.id = NEW.reverses_adjustment_id;
        IF reversed_claim IS NULL THEN
            RAISE EXCEPTION 'claim adjustment % not found in tenant %',
                NEW.reverses_adjustment_id, NEW.tenant_id
                USING ERRCODE = 'foreign_key_violation';
        END IF;
        IF reversed_claim <> NEW.claim_id THEN
            RAISE EXCEPTION 'claim adjustment % belongs to another claim',
                NEW.reverses_adjustment_id
                USING ERRCODE = 'check_violation';
        END IF;
        IF reversed_type = 'REVERSAL' THEN
            RAISE EXCEPTION 'a reversal cannot be reversed'
                USING ERRCODE = 'check_violation';
        END IF;
    END IF;
    IF NEW.claim_line_id IS NOT NULL THEN
        SELECT v.claim_id INTO line_claim
          FROM claim.claim_line l
          JOIN claim.claim_version v ON v.tenant_id = l.tenant_id AND v.id = l.version_id
         WHERE l.tenant_id = NEW.tenant_id AND l.id = NEW.claim_line_id;
        IF line_claim IS DISTINCT FROM NEW.claim_id THEN
            RAISE EXCEPTION 'claim line % belongs to another claim', NEW.claim_line_id
                USING ERRCODE = 'check_violation';
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tg_claim_adjustment_links
    BEFORE INSERT ON claim.adjustment
    FOR EACH ROW EXECUTE FUNCTION claim.tg_adjustment_links();

-- The grant is over the tables that exist when it runs; nothing new is created here, but the
-- function objects above are, and a deployment that re-runs the grant is a deployment whose
-- app role can still read what it could yesterday.
SELECT platform.grant_app_schema_usage('claim');
