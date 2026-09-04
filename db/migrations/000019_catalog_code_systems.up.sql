-- 000019: external code systems and service code mapping (WP-I3-01, v1.2 9.7, 16.6).
-- A tenant's own service definitions (migration 000006) have to be reportable under the
-- codes other parties use: SUT and HUV for payers, ICD-10 for diagnoses, and whatever
-- internal system a sponsor already runs. A code is only valid for a period, so a claim
-- from last year must resolve against the codes that were valid then, not today's.

-- catalog.service_category was created in 000006 without the concurrency columns every
-- other editable resource has, which would have forced its API to invent a second
-- optimistic-concurrency mechanism. Give it the standard pair instead.
ALTER TABLE catalog.service_category
    ADD COLUMN updated_at  timestamptz NOT NULL DEFAULT clock_timestamp(),
    ADD COLUMN row_version bigint NOT NULL DEFAULT 1;
SELECT platform.attach_touch_row('catalog.service_category'::regclass);

CREATE TABLE catalog.code_system (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    code                text NOT NULL,
    name                text NOT NULL,
    -- Editions of the same system live side by side: SUT 2024 and SUT 2025 are two rows.
    version             text NOT NULL,
    authority           text NOT NULL
                        CHECK (authority IN ('SGK','SB','WHO','TENANT','OTHER')),
    -- True when the content is licensed and may not leave the tenant (no export, no share).
    licensed            boolean NOT NULL DEFAULT false,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('ACTIVE','INACTIVE')),
    valid_from          date NOT NULL,
    valid_to            date,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_code_system_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_code_system_code UNIQUE (tenant_id, code, version),
    CONSTRAINT ck_code_system_code CHECK (code ~ '^[A-Z][A-Z0-9_]{1,39}$'),
    CONSTRAINT ck_code_system_version CHECK (length(version) BETWEEN 1 AND 32),
    CONSTRAINT ck_code_system_period CHECK (valid_to IS NULL OR valid_to > valid_from)
);
SELECT platform.attach_touch_row('catalog.code_system'::regclass);
SELECT platform.enable_tenant_rls('catalog.code_system'::regclass);

CREATE TABLE catalog.code_value (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    code_system_id      uuid NOT NULL,
    code                text NOT NULL,
    display             text NOT NULL,
    -- Hierarchy inside the system (ICD chapters, SUT groups) as the publisher states it;
    -- a plain code rather than an FK, because a parent may be added after its children.
    parent_code         text,
    valid_from          date NOT NULL,
    valid_to            date,
    active              boolean NOT NULL DEFAULT true,
    attributes          jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_code_value_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_code_value_code UNIQUE (tenant_id, code_system_id, code, valid_from),
    CONSTRAINT fk_code_value_system FOREIGN KEY (tenant_id, code_system_id)
        REFERENCES catalog.code_system(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_code_value_code CHECK (length(code) BETWEEN 1 AND 64),
    CONSTRAINT ck_code_value_period CHECK (valid_to IS NULL OR valid_to > valid_from),
    CONSTRAINT ck_code_value_attributes CHECK (jsonb_typeof(attributes) = 'object')
);
CREATE INDEX ix_code_value_lookup
    ON catalog.code_value (tenant_id, code_system_id, code, valid_from DESC);
CREATE INDEX ix_code_value_display_trgm
    ON catalog.code_value USING gin (display gin_trgm_ops);
SELECT platform.attach_touch_row('catalog.code_value'::regclass);
SELECT platform.enable_tenant_rls('catalog.code_value'::regclass);

CREATE TABLE catalog.service_code_mapping (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    service_definition_id   uuid NOT NULL,
    code_system_id          uuid NOT NULL,
    code                    text NOT NULL,
    valid_from              date NOT NULL,
    valid_to                date,
    is_primary              boolean NOT NULL DEFAULT false,
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version             bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_service_code_mapping_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_service_code_mapping_definition FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_service_code_mapping_system FOREIGN KEY (tenant_id, code_system_id)
        REFERENCES catalog.code_system(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_service_code_mapping_code CHECK (length(code) BETWEEN 1 AND 64),
    CONSTRAINT ck_service_code_mapping_period CHECK (valid_to IS NULL OR valid_to > valid_from),
    -- One definition may not carry two rows for the same system and code over overlapping
    -- periods; that would make "which code applied on this date" ambiguous.
    CONSTRAINT ex_service_code_mapping_overlap EXCLUDE USING gist (
        tenant_id WITH =,
        service_definition_id WITH =,
        code_system_id WITH =,
        code WITH =,
        daterange(valid_from, valid_to, '[)') WITH &&
    )
);
-- At most one primary mapping per definition and system at a time. The period is part of
-- the key, so a mapping that ends may be followed by another primary one.
CREATE INDEX ix_service_code_mapping_definition
    ON catalog.service_code_mapping (tenant_id, service_definition_id, code_system_id);
ALTER TABLE catalog.service_code_mapping
    ADD CONSTRAINT ex_service_code_mapping_primary EXCLUDE USING gist (
        tenant_id WITH =,
        service_definition_id WITH =,
        code_system_id WITH =,
        daterange(valid_from, valid_to, '[)') WITH &&
    ) WHERE (is_primary);
SELECT platform.attach_touch_row('catalog.service_code_mapping'::regclass);
SELECT platform.enable_tenant_rls('catalog.service_code_mapping'::regclass);

SELECT platform.grant_app_schema_usage('catalog');
