-- 000023: pricing quotes (WP-I3-05, v1.2 11.5, 11.6).
-- A quote is the number somebody was given before anything was booked. It is stored so
-- the number quoted and the number later claimed can be compared, and it grants nothing:
-- no reservation, no authorization, no ledger movement.

INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('pricing.quote', 'Fiyat teklifi hesaplama ve okuma', 'NORMAL')
ON CONFLICT (code) DO NOTHING;

CREATE TABLE contract.price_quote (
    id                          uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                   uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    person_id                   uuid NOT NULL,
    program_id                  uuid,
    provider_profile_id         uuid NOT NULL,
    location_id                 uuid,
    service_date                date NOT NULL,
    currency_code               char(3) NOT NULL CHECK (currency_code ~ '^[A-Z]{3}$'),
    outcome                     text NOT NULL
                                CHECK (outcome IN ('QUOTED','PARTIAL','REVIEW_REQUIRED','NOT_ELIGIBLE')),
    -- The five figures the operator and the member both need to see separately
    -- (v1.2 11.6). Kept exact; rounding happened once, before they were written.
    requested_amount            numeric(20,6) NOT NULL DEFAULT 0 CHECK (requested_amount >= 0),
    contract_amount             numeric(20,6) NOT NULL DEFAULT 0 CHECK (contract_amount >= 0),
    covered_amount              numeric(20,6) NOT NULL DEFAULT 0 CHECK (covered_amount >= 0),
    payer_amount                numeric(20,6) NOT NULL DEFAULT 0 CHECK (payer_amount >= 0),
    member_amount               numeric(20,6) NOT NULL DEFAULT 0 CHECK (member_amount >= 0),
    request_hash                bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    -- Ids and quantities only. Never an identity number, never a name.
    request_snapshot            jsonb NOT NULL,
    result_snapshot             jsonb NOT NULL,
    contract_version_id         uuid,
    plan_version_id             uuid,
    eligibility_evaluation_id   uuid,
    expires_at                  timestamptz NOT NULL,
    quoted_at                   timestamptz NOT NULL DEFAULT clock_timestamp(),
    quoted_by                   uuid REFERENCES iam.actor(id),
    idempotency_key             text,
    CONSTRAINT uq_price_quote_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_price_quote_person FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_price_quote_program FOREIGN KEY (tenant_id, program_id)
        REFERENCES benefit.program(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_price_quote_provider FOREIGN KEY (tenant_id, provider_profile_id)
        REFERENCES provider.provider_profile(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_price_quote_location FOREIGN KEY (tenant_id, location_id)
        REFERENCES provider.location(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_price_quote_contract_version FOREIGN KEY (tenant_id, contract_version_id)
        REFERENCES contract.contract_version(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_price_quote_plan_version FOREIGN KEY (tenant_id, plan_version_id)
        REFERENCES benefit.plan_version(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_price_quote_snapshots CHECK (
        jsonb_typeof(request_snapshot) = 'object' AND jsonb_typeof(result_snapshot) = 'object'
    ),
    CONSTRAINT ck_price_quote_expiry CHECK (expires_at > quoted_at),
    -- A quote that says REVIEW_REQUIRED must not carry a member figure: an operator who
    -- sees a number will read it as an answer, and here there is no answer yet.
    CONSTRAINT ck_price_quote_review_has_no_figures CHECK (
        outcome <> 'REVIEW_REQUIRED' OR (payer_amount = 0 AND member_amount = 0)
    )
);
CREATE INDEX ix_price_quote_person
    ON contract.price_quote (tenant_id, person_id, service_date DESC);
CREATE UNIQUE INDEX uq_price_quote_idempotency
    ON contract.price_quote (tenant_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
SELECT platform.make_append_only('contract.price_quote'::regclass);
SELECT platform.enable_tenant_rls('contract.price_quote'::regclass);

CREATE TABLE contract.price_quote_item (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    price_quote_id          uuid NOT NULL,
    line_no                 integer NOT NULL CHECK (line_no > 0),
    service_definition_id   uuid,
    package_definition_id   uuid,
    price_item_id           uuid,
    quantity                numeric(20,6) NOT NULL CHECK (quantity > 0),
    requested_amount        numeric(20,6) NOT NULL DEFAULT 0 CHECK (requested_amount >= 0),
    contract_amount         numeric(20,6) NOT NULL DEFAULT 0 CHECK (contract_amount >= 0),
    covered_amount          numeric(20,6) NOT NULL DEFAULT 0 CHECK (covered_amount >= 0),
    payer_amount            numeric(20,6) NOT NULL DEFAULT 0 CHECK (payer_amount >= 0),
    member_amount           numeric(20,6) NOT NULL DEFAULT 0 CHECK (member_amount >= 0),
    outcome                 text NOT NULL
                            CHECK (outcome IN ('QUOTED','PARTIAL','REVIEW_REQUIRED','NOT_ELIGIBLE')),
    explanations            jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_price_quote_item_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_price_quote_item_line UNIQUE (tenant_id, price_quote_id, line_no),
    CONSTRAINT fk_price_quote_item_quote FOREIGN KEY (tenant_id, price_quote_id)
        REFERENCES contract.price_quote(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_price_quote_item_definition FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_price_quote_item_package FOREIGN KEY (tenant_id, package_definition_id)
        REFERENCES contract.package_definition(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_price_quote_item_price FOREIGN KEY (tenant_id, price_item_id)
        REFERENCES contract.price_item(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_price_quote_item_target
        CHECK (num_nonnulls(service_definition_id, package_definition_id) = 1),
    CONSTRAINT ck_price_quote_item_explanations CHECK (jsonb_typeof(explanations) = 'array')
);
SELECT platform.make_append_only('contract.price_quote_item'::regclass);
SELECT platform.enable_tenant_rls('contract.price_quote_item'::regclass);
