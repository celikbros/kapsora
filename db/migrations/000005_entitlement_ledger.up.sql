-- KAPSORA migration 000005: entitlement definitions, accounts with materialized balances
-- and the append-only entitlement ledger. Reservations arrive in increment I2 (D8).

CREATE TABLE benefit.entitlement_definition (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    plan_version_id     uuid NOT NULL,
    code                text NOT NULL,
    name                text NOT NULL,
    unit_type           text NOT NULL
                        CHECK (unit_type IN ('MONEY','COUNT','NIGHT','SESSION','HOUR','KILOMETER','POINT')),
    currency_code       char(3),
    period_type         text NOT NULL
                        CHECK (period_type IN ('CALENDAR_YEAR','PLAN_YEAR','ROLLING_DAYS','LIFETIME','CUSTOM')),
    period_length       integer,
    initial_quantity    numeric(20,6) NOT NULL CHECK (initial_quantity >= 0),
    allow_overdraft     boolean NOT NULL DEFAULT false,
    rollover_policy     text NOT NULL DEFAULT 'NONE'
                        CHECK (rollover_policy IN ('NONE','FULL','CAPPED')),
    rollover_cap        numeric(20,6),
    family_shared       boolean NOT NULL DEFAULT false,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('ACTIVE','INACTIVE')),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_entitlement_definition_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_entitlement_definition_code UNIQUE (tenant_id, plan_version_id, code),
    CONSTRAINT fk_entitlement_definition_plan_version
        FOREIGN KEY (tenant_id, plan_version_id)
        REFERENCES benefit.plan_version(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_entitlement_definition_code CHECK (code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    CONSTRAINT ck_entitlement_currency CHECK (
        (unit_type = 'MONEY' AND currency_code IS NOT NULL)
        OR (unit_type <> 'MONEY' AND currency_code IS NULL)
    ),
    CONSTRAINT ck_entitlement_period_length CHECK (
        (period_type = 'ROLLING_DAYS' AND period_length IS NOT NULL AND period_length > 0)
        OR (period_type <> 'ROLLING_DAYS')
    ),
    CONSTRAINT ck_entitlement_rollover_cap CHECK (
        (rollover_policy = 'CAPPED' AND rollover_cap IS NOT NULL AND rollover_cap >= 0)
        OR (rollover_policy <> 'CAPPED' AND rollover_cap IS NULL)
    )
);

-- Materialized balances. The ledger is the source of truth; these columns are updated in
-- the same transaction as the ledger row, under SELECT ... FOR UPDATE on this row.
-- available_quantity may go negative only when the definition allows overdraft; that rule
-- is enforced by the application, the conservation CHECK below always holds.
CREATE TABLE benefit.entitlement_account (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    enrollment_id       uuid NOT NULL,
    entitlement_definition_id uuid NOT NULL,
    benefit_period      daterange NOT NULL,
    total_granted       numeric(20,6) NOT NULL DEFAULT 0,
    available_quantity  numeric(20,6) NOT NULL DEFAULT 0,
    reserved_quantity   numeric(20,6) NOT NULL DEFAULT 0,
    consumed_quantity   numeric(20,6) NOT NULL DEFAULT 0,
    expired_quantity    numeric(20,6) NOT NULL DEFAULT 0,
    status              text NOT NULL DEFAULT 'OPEN'
                        CHECK (status IN ('OPEN','FROZEN','CLOSED')),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_entitlement_account_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_entitlement_account_period UNIQUE (
        tenant_id, enrollment_id, entitlement_definition_id, benefit_period
    ),
    CONSTRAINT fk_entitlement_account_enrollment
        FOREIGN KEY (tenant_id, enrollment_id)
        REFERENCES benefit.enrollment(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_entitlement_account_definition
        FOREIGN KEY (tenant_id, entitlement_definition_id)
        REFERENCES benefit.entitlement_definition(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_entitlement_account_nonnegative CHECK (
        total_granted >= 0
        AND reserved_quantity >= 0
        AND consumed_quantity >= 0
        AND expired_quantity >= 0
    ),
    CONSTRAINT ck_entitlement_account_balance CHECK (
        total_granted = available_quantity + reserved_quantity + consumed_quantity + expired_quantity
    )
);

CREATE INDEX ix_entitlement_account_enrollment_status
    ON benefit.entitlement_account (tenant_id, enrollment_id, status);
SELECT platform.attach_touch_row('benefit.entitlement_account');

-- Append-only. Corrections are written as REVERSE movements; rows are never edited.
CREATE TABLE benefit.entitlement_ledger (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    entitlement_account_id uuid NOT NULL,
    movement_type       text NOT NULL
                        CHECK (movement_type IN ('GRANT','RESERVE','RELEASE','CONSUME','REVERSE','EXPIRE','ADJUST')),
    effective_at        timestamptz NOT NULL,
    delta_total         numeric(20,6) NOT NULL DEFAULT 0,
    delta_available     numeric(20,6) NOT NULL DEFAULT 0,
    delta_reserved      numeric(20,6) NOT NULL DEFAULT 0,
    delta_consumed      numeric(20,6) NOT NULL DEFAULT 0,
    delta_expired       numeric(20,6) NOT NULL DEFAULT 0,
    reference_type      text NOT NULL,
    reference_id        uuid NOT NULL,
    idempotency_key     text NOT NULL,
    reason_code         text,
    reason_text         text,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by          uuid,
    CONSTRAINT uq_entitlement_ledger_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_entitlement_ledger_account
        FOREIGN KEY (tenant_id, entitlement_account_id)
        REFERENCES benefit.entitlement_account(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT uq_entitlement_ledger_idempotency UNIQUE (
        tenant_id, entitlement_account_id, idempotency_key
    ),
    CONSTRAINT ck_entitlement_ledger_not_empty CHECK (
        delta_total <> 0 OR delta_available <> 0 OR delta_reserved <> 0
        OR delta_consumed <> 0 OR delta_expired <> 0
    ),
    CONSTRAINT ck_entitlement_ledger_conservation CHECK (
        delta_total = delta_available + delta_reserved + delta_consumed + delta_expired
    )
);

CREATE INDEX ix_entitlement_ledger_account_effective
    ON benefit.entitlement_ledger (tenant_id, entitlement_account_id, effective_at, id);
CREATE INDEX ix_entitlement_ledger_reference
    ON benefit.entitlement_ledger (tenant_id, reference_type, reference_id);
SELECT platform.make_append_only('benefit.entitlement_ledger');

SELECT platform.enable_tenant_rls(t)
FROM unnest(ARRAY[
    'benefit.entitlement_definition',
    'benefit.entitlement_account',
    'benefit.entitlement_ledger'
]::regclass[]) AS t;

SELECT platform.grant_app_schema_usage('benefit');
