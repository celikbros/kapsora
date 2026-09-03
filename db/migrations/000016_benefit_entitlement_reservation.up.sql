-- 000016: entitlement reservations and manual adjustments (WP-I2-03, plan v2.0 D8).
-- A reservation is the hold a request, booking or authorization places on an account;
-- RESERVE / RELEASE / CONSUME ledger movements reference it. Adjustments are manual
-- corrections that need a second actor's approval before they touch the ledger.

CREATE TABLE benefit.entitlement_reservation (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    entitlement_account_id  uuid NOT NULL,
    reference_type          text NOT NULL
                            CHECK (reference_type IN ('SERVICE_REQUEST','BOOKING','AUTHORIZATION','MANUAL')),
    reference_id            uuid NOT NULL,
    quantity                numeric(20,6) NOT NULL CHECK (quantity > 0),
    consumed_quantity       numeric(20,6) NOT NULL DEFAULT 0
                            CHECK (consumed_quantity >= 0 AND consumed_quantity <= quantity),
    released_quantity       numeric(20,6) NOT NULL DEFAULT 0
                            CHECK (released_quantity >= 0),
    status                  text NOT NULL DEFAULT 'HELD'
                            CHECK (status IN ('HELD','PARTIALLY_CONSUMED','CONSUMED','RELEASED','EXPIRED')),
    expires_at              timestamptz,
    idempotency_key         text NOT NULL,
    created_by              uuid REFERENCES iam.actor(id),
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version             bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_entitlement_reservation_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_reservation_account FOREIGN KEY (tenant_id, entitlement_account_id)
        REFERENCES benefit.entitlement_account(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT uq_entitlement_reservation_key UNIQUE (tenant_id, entitlement_account_id, idempotency_key),
    CONSTRAINT uq_entitlement_reservation_reference UNIQUE (tenant_id, reference_type, reference_id, entitlement_account_id),
    CONSTRAINT ck_reservation_quantities CHECK (consumed_quantity + released_quantity <= quantity),
    CONSTRAINT ck_reservation_terminal CHECK (
        (status = 'CONSUMED' AND consumed_quantity + released_quantity = quantity)
        OR (status IN ('RELEASED','EXPIRED') AND consumed_quantity + released_quantity = quantity)
        OR (status IN ('HELD','PARTIALLY_CONSUMED') AND consumed_quantity + released_quantity < quantity)
    )
);
CREATE INDEX ix_entitlement_reservation_expiry
    ON benefit.entitlement_reservation (tenant_id, expires_at)
    WHERE status IN ('HELD','PARTIALLY_CONSUMED') AND expires_at IS NOT NULL;
CREATE INDEX ix_entitlement_reservation_account
    ON benefit.entitlement_reservation (tenant_id, entitlement_account_id, created_at DESC);
SELECT platform.attach_touch_row('benefit.entitlement_reservation'::regclass);
SELECT platform.enable_tenant_rls('benefit.entitlement_reservation'::regclass);

-- Ledger movements that belong to a hold point at it.
ALTER TABLE benefit.entitlement_ledger
    ADD COLUMN reservation_id uuid,
    ADD CONSTRAINT fk_ledger_reservation FOREIGN KEY (tenant_id, reservation_id)
        REFERENCES benefit.entitlement_reservation(tenant_id, id) ON DELETE RESTRICT,
    ADD CONSTRAINT ck_ledger_reservation_reference CHECK (
        (movement_type IN ('RESERVE','RELEASE') AND reservation_id IS NOT NULL)
        OR (movement_type = 'CONSUME' AND (reservation_id IS NOT NULL OR reason_code = 'DIRECT_CONSUME'))
        OR movement_type IN ('GRANT','REVERSE','EXPIRE','ADJUST')
    );
CREATE INDEX ix_entitlement_ledger_reservation
    ON benefit.entitlement_ledger (tenant_id, reservation_id) WHERE reservation_id IS NOT NULL;

-- Manual adjustments: maker-checker before the ADJUST movement is written.
CREATE TABLE benefit.entitlement_adjustment (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    entitlement_account_id  uuid NOT NULL,
    delta_quantity          numeric(20,6) NOT NULL CHECK (delta_quantity <> 0),
    reason_code             text NOT NULL,
    reason_text             text,
    status                  text NOT NULL DEFAULT 'PENDING'
                            CHECK (status IN ('PENDING','APPROVED','REJECTED')),
    requested_by            uuid NOT NULL REFERENCES iam.actor(id),
    requested_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    decided_by              uuid REFERENCES iam.actor(id),
    decided_at              timestamptz,
    decision_comment        text,
    ledger_entry_id         uuid,
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version             bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_entitlement_adjustment_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_adjustment_account FOREIGN KEY (tenant_id, entitlement_account_id)
        REFERENCES benefit.entitlement_account(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_adjustment_ledger FOREIGN KEY (tenant_id, ledger_entry_id)
        REFERENCES benefit.entitlement_ledger(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_adjustment_maker_checker CHECK (decided_by IS NULL OR decided_by <> requested_by),
    CONSTRAINT ck_adjustment_decision CHECK (
        (status = 'PENDING' AND decided_by IS NULL AND ledger_entry_id IS NULL)
        OR (status = 'REJECTED' AND decided_by IS NOT NULL AND ledger_entry_id IS NULL)
        OR (status = 'APPROVED' AND decided_by IS NOT NULL AND ledger_entry_id IS NOT NULL)
    )
);
CREATE INDEX ix_entitlement_adjustment_pending
    ON benefit.entitlement_adjustment (tenant_id, requested_at) WHERE status = 'PENDING';
SELECT platform.attach_touch_row('benefit.entitlement_adjustment'::regclass);
SELECT platform.enable_tenant_rls('benefit.entitlement_adjustment'::regclass);

-- Plan versions get a real concurrency token (WP-I2-02 used xmin as a stopgap). The
-- guard trigger fires before tg_touch_row (alphabetical order), so the bookkeeping
-- columns are excluded from the immutability comparison explicitly.
ALTER TABLE benefit.plan_version
    ADD COLUMN updated_at  timestamptz NOT NULL DEFAULT clock_timestamp(),
    ADD COLUMN row_version bigint NOT NULL DEFAULT 1;
SELECT platform.attach_touch_row('benefit.plan_version'::regclass);
CREATE OR REPLACE FUNCTION benefit.tg_plan_version_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    mutable text[] := ARRAY['status', 'retire_reason_code', 'retire_reason_text', 'updated_at', 'row_version'];
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.status <> 'DRAFT' THEN
            RAISE EXCEPTION 'plan version % is not a draft and cannot be deleted', OLD.id
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        RETURN OLD;
    END IF;
    IF OLD.status = 'PUBLISHED' THEN
        IF NOT (NEW.status = 'RETIRED' AND (to_jsonb(NEW) - mutable) = (to_jsonb(OLD) - mutable)) THEN
            RAISE EXCEPTION 'published plan version % is immutable', OLD.id
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
    ELSIF OLD.status = 'RETIRED' THEN
        RAISE EXCEPTION 'retired plan version % is immutable', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END
$$;

SELECT platform.grant_app_schema_usage('benefit');
