-- 000021: contracts, versions, price lists, packages, quotas and payment terms
-- (WP-I3-03, v1.2 9.8, 11.5, 11.6, 16.6).
-- A published contract version is what a quote, an authorization and a claim were priced
-- from. It therefore cannot move afterwards, and two published versions of one contract
-- may never cover the same date: "which price applied" must have exactly one answer.

CREATE SCHEMA IF NOT EXISTS contract;

CREATE TABLE contract.contract (
    id                          uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                   uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    code                        text NOT NULL,
    name                        text NOT NULL,
    payer_organization_id       uuid NOT NULL,
    provider_profile_id         uuid NOT NULL,
    sponsor_organization_id     uuid,
    domain_code                 text NOT NULL
                                CHECK (domain_code IN (
                                    'GENERIC','HEALTH','ACCOMMODATION','ASSISTANCE','EDUCATION',
                                    'SPORT','TRANSPORT','CARE','OTHER'
                                )),
    status                      text NOT NULL DEFAULT 'DRAFT'
                                CHECK (status IN ('DRAFT','ACTIVE','SUSPENDED','CLOSED')),
    created_at                  timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at                  timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version                 bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_contract_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_contract_code UNIQUE (tenant_id, code),
    CONSTRAINT fk_contract_payer FOREIGN KEY (tenant_id, payer_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_contract_sponsor FOREIGN KEY (tenant_id, sponsor_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_contract_provider FOREIGN KEY (tenant_id, provider_profile_id)
        REFERENCES provider.provider_profile(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_contract_code CHECK (code ~ '^[A-Z][A-Z0-9_-]{1,39}$')
);
CREATE INDEX ix_contract_parties
    ON contract.contract (tenant_id, provider_profile_id, payer_organization_id, status);
SELECT platform.attach_touch_row('contract.contract'::regclass);
SELECT platform.enable_tenant_rls('contract.contract'::regclass);

CREATE TABLE contract.contract_version (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    contract_id         uuid NOT NULL,
    version_no          integer NOT NULL CHECK (version_no > 0),
    status              text NOT NULL DEFAULT 'DRAFT'
                        CHECK (status IN ('DRAFT','UNDER_REVIEW','PUBLISHED','RETIRED')),
    valid_from          date,
    valid_to            date,
    currency_code       char(3) NOT NULL DEFAULT 'TRY' CHECK (currency_code ~ '^[A-Z]{3}$'),
    notes               text,
    configuration_hash  text,
    submitted_at        timestamptz,
    submitted_by        uuid REFERENCES iam.actor(id),
    published_at        timestamptz,
    published_by        uuid REFERENCES iam.actor(id),
    retire_reason_code  text,
    review_comment      text,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_contract_version_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_contract_version_no UNIQUE (tenant_id, contract_id, version_no),
    CONSTRAINT fk_contract_version_contract FOREIGN KEY (tenant_id, contract_id)
        REFERENCES contract.contract(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_contract_version_period CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to > valid_from),
    -- A published version needs a start date, a hash and a publisher; a draft has none of
    -- them. Stating it here means no code path can publish a half-filled row.
    CONSTRAINT ck_contract_version_published_complete CHECK (
        status <> 'PUBLISHED'
        OR (valid_from IS NOT NULL AND configuration_hash IS NOT NULL AND published_by IS NOT NULL)
    ),
    CONSTRAINT ck_contract_version_retire_reason CHECK (
        status <> 'RETIRED' OR retire_reason_code IS NOT NULL
    ),
    -- Two published versions of one contract may not cover the same date.
    CONSTRAINT ex_contract_version_published_overlap EXCLUDE USING gist (
        tenant_id WITH =,
        contract_id WITH =,
        daterange(valid_from, valid_to, '[)') WITH &&
    ) WHERE (status = 'PUBLISHED')
);
CREATE INDEX ix_contract_version_lookup
    ON contract.contract_version (tenant_id, contract_id, status, valid_from);
SELECT platform.attach_touch_row('contract.contract_version'::regclass);
SELECT platform.enable_tenant_rls('contract.contract_version'::regclass);

-- A price list scopes a set of prices to a season and to days of the week. Health
-- contracts leave both NULL; accommodation (I6) is the reason they exist.
CREATE TABLE contract.price_list (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    contract_version_id uuid NOT NULL,
    code                text NOT NULL,
    name                text NOT NULL,
    priority            integer NOT NULL DEFAULT 100,
    season_from         date,
    season_to           date,
    -- Bit 0 = Monday .. bit 6 = Sunday. NULL means every day.
    weekday_mask        smallint,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_price_list_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_price_list_code UNIQUE (tenant_id, contract_version_id, code),
    CONSTRAINT fk_price_list_version FOREIGN KEY (tenant_id, contract_version_id)
        REFERENCES contract.contract_version(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT ck_price_list_code CHECK (code ~ '^[A-Z][A-Z0-9_-]{1,39}$'),
    CONSTRAINT ck_price_list_season CHECK (
        num_nonnulls(season_from, season_to) <> 1 AND (season_to IS NULL OR season_to > season_from)
    ),
    CONSTRAINT ck_price_list_weekday CHECK (weekday_mask IS NULL OR (weekday_mask > 0 AND weekday_mask < 128))
);
SELECT platform.attach_touch_row('contract.price_list'::regclass);
SELECT platform.enable_tenant_rls('contract.price_list'::regclass);

CREATE TABLE contract.package_definition (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    contract_version_id uuid NOT NULL,
    code                text NOT NULL,
    name                text NOT NULL,
    inclusion_rule      text NOT NULL DEFAULT 'ALL' CHECK (inclusion_rule IN ('ALL','ANY_OF_N')),
    min_lines           integer,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_package_definition_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_package_definition_code UNIQUE (tenant_id, contract_version_id, code),
    CONSTRAINT fk_package_definition_version FOREIGN KEY (tenant_id, contract_version_id)
        REFERENCES contract.contract_version(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT ck_package_definition_code CHECK (code ~ '^[A-Z][A-Z0-9_-]{1,39}$'),
    CONSTRAINT ck_package_definition_min_lines CHECK (
        (inclusion_rule = 'ANY_OF_N') = (min_lines IS NOT NULL)
        AND (min_lines IS NULL OR min_lines > 0)
    )
);
SELECT platform.attach_touch_row('contract.package_definition'::regclass);
SELECT platform.enable_tenant_rls('contract.package_definition'::regclass);

CREATE TABLE contract.package_line (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    package_definition_id   uuid NOT NULL,
    service_definition_id   uuid NOT NULL,
    included_quantity       numeric(20,6) NOT NULL CHECK (included_quantity > 0),
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_package_line_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_package_line_service UNIQUE (tenant_id, package_definition_id, service_definition_id),
    CONSTRAINT fk_package_line_package FOREIGN KEY (tenant_id, package_definition_id)
        REFERENCES contract.package_definition(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_package_line_service FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT
);
SELECT platform.enable_tenant_rls('contract.package_line'::regclass);

-- The row a quote finally lands on. Exactly one of the three targets is set; the
-- specificity ladder in internal/contract/selection depends on that being true.
CREATE TABLE contract.price_item (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    price_list_id           uuid NOT NULL,
    service_definition_id   uuid,
    service_category_id     uuid,
    package_definition_id   uuid,
    location_id             uuid,
    unit_type               text NOT NULL
                            CHECK (unit_type IN ('MONEY','COUNT','NIGHT','SESSION','HOUR','KILOMETER','POINT')),
    pricing_method          text NOT NULL
                            CHECK (pricing_method IN ('FIXED','UNIT','PERCENT_OF_LIST','FORMULA')),
    amount                  numeric(20,6),
    percent                 numeric(9,6),
    formula_key             text,
    min_amount              numeric(20,6),
    max_amount              numeric(20,6),
    member_share_method     text NOT NULL DEFAULT 'NONE'
                            CHECK (member_share_method IN ('NONE','FIXED','PERCENT')),
    member_share_amount     numeric(20,6),
    member_share_percent    numeric(9,6),
    valid_from              date NOT NULL,
    valid_to                date,
    priority                integer NOT NULL DEFAULT 100,
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version             bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_price_item_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_price_item_list FOREIGN KEY (tenant_id, price_list_id)
        REFERENCES contract.price_list(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_price_item_definition FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_price_item_category FOREIGN KEY (tenant_id, service_category_id)
        REFERENCES catalog.service_category(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_price_item_package FOREIGN KEY (tenant_id, package_definition_id)
        REFERENCES contract.package_definition(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_price_item_location FOREIGN KEY (tenant_id, location_id)
        REFERENCES provider.location(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_price_item_target CHECK (
        num_nonnulls(service_definition_id, service_category_id, package_definition_id) = 1
    ),
    CONSTRAINT ck_price_item_period CHECK (valid_to IS NULL OR valid_to > valid_from),
    -- Each method needs its own field and forbids the others', so a row can never be
    -- half-specified in a way the calculator would have to guess about.
    CONSTRAINT ck_price_item_method CHECK (
        CASE pricing_method
            WHEN 'FIXED'           THEN amount IS NOT NULL AND percent IS NULL AND formula_key IS NULL
            WHEN 'UNIT'            THEN amount IS NOT NULL AND percent IS NULL AND formula_key IS NULL
            WHEN 'PERCENT_OF_LIST' THEN percent IS NOT NULL AND amount IS NULL AND formula_key IS NULL
            WHEN 'FORMULA'         THEN formula_key IS NOT NULL AND amount IS NULL AND percent IS NULL
        END
    ),
    CONSTRAINT ck_price_item_member_share CHECK (
        CASE member_share_method
            WHEN 'NONE'    THEN member_share_amount IS NULL AND member_share_percent IS NULL
            WHEN 'FIXED'   THEN member_share_amount IS NOT NULL AND member_share_percent IS NULL
            WHEN 'PERCENT' THEN member_share_percent IS NOT NULL AND member_share_amount IS NULL
        END
    ),
    CONSTRAINT ck_price_item_bounds CHECK (
        (min_amount IS NULL OR min_amount >= 0)
        AND (max_amount IS NULL OR max_amount >= 0)
        AND (min_amount IS NULL OR max_amount IS NULL OR max_amount >= min_amount)
    ),
    CONSTRAINT ck_price_item_amounts CHECK (
        (amount IS NULL OR amount >= 0)
        AND (percent IS NULL OR percent >= 0)
        AND (member_share_amount IS NULL OR member_share_amount >= 0)
        AND (member_share_percent IS NULL OR (member_share_percent >= 0 AND member_share_percent <= 100))
    )
);
CREATE INDEX ix_price_item_definition
    ON contract.price_item (tenant_id, service_definition_id, valid_from)
    WHERE service_definition_id IS NOT NULL;
CREATE INDEX ix_price_item_category
    ON contract.price_item (tenant_id, service_category_id, valid_from)
    WHERE service_category_id IS NOT NULL;
CREATE INDEX ix_price_item_list ON contract.price_item (tenant_id, price_list_id, valid_from);
SELECT platform.attach_touch_row('contract.price_item'::regclass);
SELECT platform.enable_tenant_rls('contract.price_item'::regclass);

-- Capacity the provider granted for a period. This migration only stores it; consuming a
-- quota is authorization's job (I4) and will take the row lock.
CREATE TABLE contract.provider_quota (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    contract_version_id     uuid NOT NULL,
    location_id             uuid,
    service_definition_id   uuid,
    period_type             text NOT NULL CHECK (period_type IN ('DAY','WEEK','MONTH','YEAR','CONTRACT')),
    period_from             date NOT NULL,
    period_to               date NOT NULL,
    capacity                numeric(20,6) NOT NULL CHECK (capacity > 0),
    consumed                numeric(20,6) NOT NULL DEFAULT 0 CHECK (consumed >= 0),
    allow_overdraft         boolean NOT NULL DEFAULT false,
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version             bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_provider_quota_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_provider_quota_version FOREIGN KEY (tenant_id, contract_version_id)
        REFERENCES contract.contract_version(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_provider_quota_location FOREIGN KEY (tenant_id, location_id)
        REFERENCES provider.location(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_provider_quota_service FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_provider_quota_period CHECK (period_to > period_from),
    CONSTRAINT ck_provider_quota_consumed CHECK (allow_overdraft OR consumed <= capacity)
);
-- NULLS NOT DISTINCT so that two tenant-wide quotas for the same period collide instead of
-- both existing; a scope that is "everything" must be unique too.
CREATE UNIQUE INDEX uq_provider_quota_scope
    ON contract.provider_quota (tenant_id, contract_version_id, location_id, service_definition_id, period_from, period_to)
    NULLS NOT DISTINCT;
SELECT platform.attach_touch_row('contract.provider_quota'::regclass);
SELECT platform.enable_tenant_rls('contract.provider_quota'::regclass);

CREATE TABLE contract.payment_term (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    contract_version_id uuid NOT NULL,
    due_days            integer NOT NULL CHECK (due_days >= 0 AND due_days <= 365),
    settlement_method   text NOT NULL CHECK (settlement_method IN ('BANK_TRANSFER','OFFSET','OTHER')),
    tax_behaviour       text NOT NULL CHECK (tax_behaviour IN ('EXCLUSIVE','INCLUSIVE','EXEMPT')),
    vat_rate            numeric(5,2) CHECK (vat_rate IS NULL OR (vat_rate >= 0 AND vat_rate <= 100)),
    late_fee_percent    numeric(5,2) CHECK (late_fee_percent IS NULL OR late_fee_percent >= 0),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_payment_term_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_payment_term_version UNIQUE (tenant_id, contract_version_id),
    CONSTRAINT fk_payment_term_version FOREIGN KEY (tenant_id, contract_version_id)
        REFERENCES contract.contract_version(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT ck_payment_term_vat CHECK (tax_behaviour = 'EXEMPT' OR vat_rate IS NOT NULL)
);
SELECT platform.attach_touch_row('contract.payment_term'::regclass);
SELECT platform.enable_tenant_rls('contract.payment_term'::regclass);

SELECT platform.grant_app_schema_usage('contract');
