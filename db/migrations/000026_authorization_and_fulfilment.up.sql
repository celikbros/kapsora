-- 000026: authorizations, fulfilments and vouchers (WP-I4-02, v1.2 9.11, 11.4, 16.7).
--
-- An approval is a promise; these tables are where the promise costs something. An
-- authorization holds entitlement so the same balance cannot be promised twice, a
-- fulfilment records what was actually delivered, and a voucher is the token the member
-- shows at the counter.
--
-- Nothing here materialises a balance. `reserved_total` and `consumed_total` are the
-- authorization's own arithmetic over its lines; the money and the entitlement live in
-- benefit.entitlement_account and benefit.entitlement_ledger, and every movement goes
-- through the ledger's reserve/release/consume so the account lock that stops a double
-- spend is always taken.

INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('authorization.manage', 'Ön onay oluşturma, süre uzatma ve iptal',      'NORMAL'),
    ('fulfilment.record',    'Verilen hizmetin kaydı ve tamamlanması',        'NORMAL'),
    ('voucher.redeem',       'Hizmet kuponunun kullanılması',                 'NORMAL')
ON CONFLICT (code) DO NOTHING;

-- The hold itself. `request_id` is a composite FK so an authorization can never point at
-- another tenant's request, and `idempotency_key` is unique per tenant because creating
-- twice under one key has to return the first authorization rather than take a second set
-- of reservations.
CREATE TABLE service.authorization (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    request_id              uuid NOT NULL,
    authorization_reference text NOT NULL,
    valid_from              timestamptz NOT NULL DEFAULT clock_timestamp(),
    valid_to                timestamptz NOT NULL,
    status                  text NOT NULL DEFAULT 'ACTIVE'
                            CHECK (status IN ('ACTIVE','PARTIALLY_USED','USED','EXPIRED','CANCELLED')),
    -- What this authorization holds and what has been delivered against it, in the unit
    -- of the lines. They are a summary of service.authorization_item, never a balance.
    reserved_total          numeric(20,6) NOT NULL DEFAULT 0 CHECK (reserved_total >= 0),
    consumed_total          numeric(20,6) NOT NULL DEFAULT 0 CHECK (consumed_total >= 0),
    currency_code           char(3) CHECK (currency_code IS NULL OR currency_code ~ '^[A-Z]{3}$'),
    price_quote_id          uuid,
    approved_by             uuid REFERENCES iam.actor(id),
    approved_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    cancel_reason_code      text,
    idempotency_key         text NOT NULL,
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by              uuid,
    updated_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by              uuid,
    row_version             bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_authorization_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_authorization_reference UNIQUE (tenant_id, authorization_reference),
    CONSTRAINT uq_authorization_idempotency UNIQUE (tenant_id, idempotency_key),
    CONSTRAINT fk_authorization_request FOREIGN KEY (tenant_id, request_id)
        REFERENCES service.service_request(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_authorization_price_quote FOREIGN KEY (tenant_id, price_quote_id)
        REFERENCES contract.price_quote(tenant_id, id) ON DELETE RESTRICT,
    -- More cannot be delivered than was promised. The ledger enforces the same thing on
    -- the hold; this is the statement the authorization makes on its own.
    CONSTRAINT ck_authorization_consumed CHECK (consumed_total <= reserved_total),
    CONSTRAINT ck_authorization_window CHECK (valid_to > valid_from),
    -- A cancelled promise says why it was cancelled. "It was withdrawn" without a reason
    -- is what leaves a member with nothing to appeal against.
    CONSTRAINT ck_authorization_cancel_reason
        CHECK (status <> 'CANCELLED' OR cancel_reason_code IS NOT NULL)
);

CREATE INDEX ix_authorization_request
    ON service.authorization (tenant_id, request_id, created_at DESC);
-- The expiry sweep only ever looks at holds that still hold something, so the index
-- carries only those rows and stays small however many authorizations are archived.
CREATE INDEX ix_authorization_expiry
    ON service.authorization (tenant_id, status, valid_to)
    WHERE status IN ('ACTIVE','PARTIALLY_USED');
SELECT platform.attach_touch_row('service.authorization'::regclass);
SELECT platform.enable_tenant_rls('service.authorization'::regclass);

-- One approved line, with the exact hold it took. `entitlement_reservation_id` is what
-- makes release and consume act on this line's own reservation rather than on whatever
-- hold the account happens to carry.
CREATE TABLE service.authorization_item (
    id                          uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                   uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    authorization_id            uuid NOT NULL,
    request_item_id             uuid NOT NULL,
    service_definition_id       uuid NOT NULL,
    approved_quantity           numeric(20,6) NOT NULL CHECK (approved_quantity > 0),
    approved_amount             numeric(20,6) CHECK (approved_amount IS NULL OR approved_amount >= 0),
    member_amount               numeric(20,6) NOT NULL DEFAULT 0 CHECK (member_amount >= 0),
    entitlement_reservation_id  uuid,
    consumed_quantity           numeric(20,6) NOT NULL DEFAULT 0 CHECK (consumed_quantity >= 0),
    created_at                  timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at                  timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version                 bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_authorization_item_id_tenant UNIQUE (tenant_id, id),
    -- One line of a request is authorized once by one authorization; a second promise
    -- against the same line is a second authorization, not a second row here.
    CONSTRAINT uq_authorization_item_request_item
        UNIQUE (tenant_id, authorization_id, request_item_id),
    CONSTRAINT fk_authorization_item_authorization FOREIGN KEY (tenant_id, authorization_id)
        REFERENCES service.authorization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_authorization_item_request_item FOREIGN KEY (tenant_id, request_item_id)
        REFERENCES service.service_request_item(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_authorization_item_service FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_authorization_item_reservation FOREIGN KEY (tenant_id, entitlement_reservation_id)
        REFERENCES benefit.entitlement_reservation(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_authorization_item_consumed CHECK (consumed_quantity <= approved_quantity)
);

CREATE INDEX ix_authorization_item_authorization
    ON service.authorization_item (tenant_id, authorization_id);
SELECT platform.attach_touch_row('service.authorization_item'::regclass);
SELECT platform.enable_tenant_rls('service.authorization_item'::regclass);

-- What was actually delivered, by whom and where. The three provider columns are nullable
-- because a fulfilment recorded from the back office may know none of them yet.
CREATE TABLE service.fulfilment (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    fulfilment_reference    text NOT NULL,
    authorization_id        uuid NOT NULL,
    provider_profile_id     uuid,
    location_id             uuid,
    practitioner_id         uuid,
    performed_at            timestamptz NOT NULL,
    status                  text NOT NULL DEFAULT 'RECORDED'
                            CHECK (status IN ('RECORDED','COMPLETED','CANCELLED')),
    completed_at            timestamptz,
    cancelled_at            timestamptz,
    cancel_reason_code      text,
    recorded_by             uuid REFERENCES iam.actor(id),
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version             bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_fulfilment_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_fulfilment_reference UNIQUE (tenant_id, fulfilment_reference),
    CONSTRAINT fk_fulfilment_authorization FOREIGN KEY (tenant_id, authorization_id)
        REFERENCES service.authorization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_fulfilment_provider FOREIGN KEY (tenant_id, provider_profile_id)
        REFERENCES provider.provider_profile(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_fulfilment_location FOREIGN KEY (tenant_id, location_id)
        REFERENCES provider.location(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_fulfilment_practitioner FOREIGN KEY (tenant_id, practitioner_id)
        REFERENCES provider.practitioner(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_fulfilment_completed
        CHECK ((status = 'COMPLETED') = (completed_at IS NOT NULL)),
    CONSTRAINT ck_fulfilment_cancelled CHECK (
        (status = 'CANCELLED' AND cancelled_at IS NOT NULL AND cancel_reason_code IS NOT NULL)
        OR (status <> 'CANCELLED' AND cancelled_at IS NULL AND cancel_reason_code IS NULL)
    )
);

CREATE INDEX ix_fulfilment_authorization
    ON service.fulfilment (tenant_id, authorization_id, performed_at DESC);
CREATE INDEX ix_fulfilment_worklist
    ON service.fulfilment (tenant_id, status, created_at DESC, id);
SELECT platform.attach_touch_row('service.fulfilment'::regclass);
SELECT platform.enable_tenant_rls('service.fulfilment'::regclass);

CREATE TABLE service.fulfilment_item (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    fulfilment_id           uuid NOT NULL,
    authorization_item_id   uuid NOT NULL,
    service_definition_id   uuid NOT NULL,
    actual_quantity         numeric(20,6) NOT NULL CHECK (actual_quantity > 0),
    actual_amount           numeric(20,6) CHECK (actual_amount IS NULL OR actual_amount >= 0),
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_fulfilment_item_id_tenant UNIQUE (tenant_id, id),
    -- One line of an authorization appears once per fulfilment; delivering the rest later
    -- is a second fulfilment, which is what keeps "what was delivered when" answerable.
    CONSTRAINT uq_fulfilment_item_line
        UNIQUE (tenant_id, fulfilment_id, authorization_item_id),
    CONSTRAINT fk_fulfilment_item_fulfilment FOREIGN KEY (tenant_id, fulfilment_id)
        REFERENCES service.fulfilment(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_fulfilment_item_authorization_item FOREIGN KEY (tenant_id, authorization_item_id)
        REFERENCES service.authorization_item(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_fulfilment_item_service FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT
);

CREATE INDEX ix_fulfilment_item_fulfilment
    ON service.fulfilment_item (tenant_id, fulfilment_id);
SELECT platform.enable_tenant_rls('service.fulfilment_item'::regclass);

-- The token the member shows at the counter. The plaintext is returned once by
-- issueVoucher and never stored: only its SHA-256 digest is kept, exactly as the session
-- cookie is handled in WP-I1-01. `token_masked` is the tail an operator reads back to
-- confirm which voucher is in front of them, and it is not enough to reconstruct one.
CREATE TABLE service.voucher (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    authorization_id        uuid NOT NULL,
    token_hash              bytea NOT NULL CHECK (octet_length(token_hash) = 32),
    token_masked            text NOT NULL,
    valid_from              timestamptz NOT NULL DEFAULT clock_timestamp(),
    valid_to                timestamptz NOT NULL,
    status                  text NOT NULL DEFAULT 'ISSUED'
                            CHECK (status IN ('ISSUED','REDEEMED','EXPIRED','REVOKED')),
    redeemed_at             timestamptz,
    redeemed_by_actor_id    uuid REFERENCES iam.actor(id),
    revoke_reason_code      text,
    issued_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    issued_by               uuid REFERENCES iam.actor(id),
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version             bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_voucher_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_voucher_token_hash UNIQUE (tenant_id, token_hash),
    CONSTRAINT fk_voucher_authorization FOREIGN KEY (tenant_id, authorization_id)
        REFERENCES service.authorization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_voucher_window CHECK (valid_to > valid_from),
    CONSTRAINT ck_voucher_redeemed
        CHECK ((status = 'REDEEMED') = (redeemed_at IS NOT NULL)),
    CONSTRAINT ck_voucher_revoked
        CHECK (status <> 'REVOKED' OR revoke_reason_code IS NOT NULL)
);

CREATE INDEX ix_voucher_authorization
    ON service.voucher (tenant_id, authorization_id, issued_at DESC);

-- At most one live voucher per authorization. Issuing carries no Idempotency-Key on
-- purpose — the middleware persists the response body, and this response is the only
-- place the plaintext token exists — so without this index a double-clicked issue would
-- mint a second usable token for the same promise, and the member would be holding two.
-- The partial predicate still allows a replacement once the first is spent, expired or
-- withdrawn, which is the case a counter actually needs.
CREATE UNIQUE INDEX uq_voucher_one_live_per_authorization
    ON service.voucher (tenant_id, authorization_id)
    WHERE status = 'ISSUED';
SELECT platform.attach_touch_row('service.voucher'::regclass);
SELECT platform.enable_tenant_rls('service.voucher'::regclass);

SELECT platform.grant_app_schema_usage('service');
