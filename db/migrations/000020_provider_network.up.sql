-- 000020: provider network (WP-I3-02, v1.2 9.3, 16.6).
-- A provider is an organization the tenant already knows (directory.tenant_organization
-- with the PROVIDER role) given a profile, the places it works from, what it can deliver
-- there and who works there. Contracts price against these rows and eligibility answers
-- "yes, at this location" from them.

CREATE SCHEMA IF NOT EXISTS provider;

CREATE TABLE provider.provider_profile (
    id                          uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                   uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    tenant_organization_id      uuid NOT NULL,
    provider_type               text NOT NULL
                                CHECK (provider_type IN (
                                    'HOSPITAL','CLINIC','PHARMACY','LABORATORY','IMAGING',
                                    'HOTEL','AGENCY','TRANSPORT','EDUCATION','SPORT','OTHER'
                                )),
    status                      text NOT NULL DEFAULT 'PENDING'
                                CHECK (status IN ('PENDING','ACTIVE','SUSPENDED','TERMINATED')),
    -- The tenant's own network tiering (A/B/preferred/...). Free text on purpose: every
    -- customer names its tiers differently and none of them is our business.
    network_tier                text,
    contracted_from             date,
    contracted_to               date,
    notes                       text,
    created_at                  timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at                  timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version                 bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_provider_profile_id_tenant UNIQUE (tenant_id, id),
    -- One profile per organization relationship: the relationship is what the tenant
    -- contracts with, so two profiles for it would make "which provider" ambiguous.
    CONSTRAINT uq_provider_profile_organization UNIQUE (tenant_id, tenant_organization_id),
    CONSTRAINT fk_provider_profile_organization FOREIGN KEY (tenant_id, tenant_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_provider_profile_period
        CHECK (contracted_to IS NULL OR contracted_from IS NULL OR contracted_to > contracted_from),
    CONSTRAINT ck_provider_profile_tier CHECK (network_tier IS NULL OR length(network_tier) BETWEEN 1 AND 32)
);
CREATE INDEX ix_provider_profile_type_status
    ON provider.provider_profile (tenant_id, provider_type, status);
SELECT platform.attach_touch_row('provider.provider_profile'::regclass);
SELECT platform.enable_tenant_rls('provider.provider_profile'::regclass);

CREATE TABLE provider.location (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    provider_profile_id uuid NOT NULL,
    code                text NOT NULL,
    name                text NOT NULL,
    address_line        text,
    district            text,
    city                text,
    country_code        char(2) NOT NULL DEFAULT 'TR',
    postal_code         text,
    -- Plain numeric columns rather than PostGIS: the only geo question the MVP asks is
    -- "show it on a map", and a distance search would need an ADR of its own first.
    latitude            numeric(9,6),
    longitude           numeric(9,6),
    timezone            text NOT NULL DEFAULT 'Europe/Istanbul',
    phone               text,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('ACTIVE','SUSPENDED','CLOSED')),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_location_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_location_code UNIQUE (tenant_id, provider_profile_id, code),
    CONSTRAINT fk_location_provider FOREIGN KEY (tenant_id, provider_profile_id)
        REFERENCES provider.provider_profile(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_location_code CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,39}$'),
    CONSTRAINT ck_location_country CHECK (country_code ~ '^[A-Z]{2}$'),
    CONSTRAINT ck_location_latitude CHECK (latitude IS NULL OR latitude BETWEEN -90 AND 90),
    CONSTRAINT ck_location_longitude CHECK (longitude IS NULL OR longitude BETWEEN -180 AND 180),
    CONSTRAINT ck_location_geo_pair
        CHECK (num_nonnulls(latitude, longitude) <> 1)
);
CREATE INDEX ix_location_provider ON provider.location (tenant_id, provider_profile_id, status);
CREATE INDEX ix_location_city ON provider.location (tenant_id, city) WHERE status = 'ACTIVE';
SELECT platform.attach_touch_row('provider.location'::regclass);
SELECT platform.enable_tenant_rls('provider.location'::regclass);

-- What a location can deliver, and when. A row names either one service definition or a
-- whole category; a category row covers definitions added under it later, which is why
-- resolution walks the tree at read time instead of expanding here.
CREATE TABLE provider.capability (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    location_id             uuid NOT NULL,
    service_definition_id   uuid,
    service_category_id     uuid,
    valid_from              date NOT NULL,
    valid_to                date,
    notes                   text,
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version             bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_capability_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_capability_location FOREIGN KEY (tenant_id, location_id)
        REFERENCES provider.location(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_capability_definition FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_capability_category FOREIGN KEY (tenant_id, service_category_id)
        REFERENCES catalog.service_category(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_capability_target
        CHECK (num_nonnulls(service_definition_id, service_category_id) = 1),
    CONSTRAINT ck_capability_period CHECK (valid_to IS NULL OR valid_to > valid_from),
    CONSTRAINT ex_capability_definition_overlap EXCLUDE USING gist (
        tenant_id WITH =,
        location_id WITH =,
        service_definition_id WITH =,
        daterange(valid_from, valid_to, '[)') WITH &&
    ) WHERE (service_definition_id IS NOT NULL),
    CONSTRAINT ex_capability_category_overlap EXCLUDE USING gist (
        tenant_id WITH =,
        location_id WITH =,
        service_category_id WITH =,
        daterange(valid_from, valid_to, '[)') WITH &&
    ) WHERE (service_category_id IS NOT NULL)
);
CREATE INDEX ix_capability_definition
    ON provider.capability (tenant_id, service_definition_id, valid_from)
    WHERE service_definition_id IS NOT NULL;
CREATE INDEX ix_capability_category
    ON provider.capability (tenant_id, service_category_id, valid_from)
    WHERE service_category_id IS NOT NULL;
SELECT platform.attach_touch_row('provider.capability'::regclass);
SELECT platform.enable_tenant_rls('provider.capability'::regclass);

-- A practitioner's registration number is a professional identity number. It is treated
-- exactly like a TCKN: encrypted at rest, searchable only through a tenant-salted blind
-- index, and never stored, logged or returned in plaintext.
CREATE TABLE provider.practitioner (
    id                              uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                       uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    provider_profile_id             uuid NOT NULL,
    person_id                       uuid,
    full_name                       text NOT NULL,
    title                           text,
    branch_code                     text,
    registration_authority          text NOT NULL
                                    CHECK (registration_authority IN ('TTB','SB','TDB','TEB','OTHER')),
    registration_number_cipher      bytea NOT NULL,
    registration_number_hash        bytea NOT NULL,
    registration_number_masked      text NOT NULL,
    valid_from                      date,
    valid_to                        date,
    status                          text NOT NULL DEFAULT 'ACTIVE'
                                    CHECK (status IN ('ACTIVE','SUSPENDED','ENDED')),
    created_at                      timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at                      timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version                     bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_practitioner_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_practitioner_provider FOREIGN KEY (tenant_id, provider_profile_id)
        REFERENCES provider.provider_profile(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_practitioner_person FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_practitioner_hash_len CHECK (octet_length(registration_number_hash) = 32),
    CONSTRAINT ck_practitioner_period CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to > valid_from),
    -- One registration number belongs to one practitioner within its issuing body.
    CONSTRAINT uq_practitioner_registration
        UNIQUE (tenant_id, registration_authority, registration_number_hash)
);
CREATE INDEX ix_practitioner_provider
    ON provider.practitioner (tenant_id, provider_profile_id, status);
CREATE INDEX ix_practitioner_name_trgm
    ON provider.practitioner USING gin (full_name gin_trgm_ops);
SELECT platform.attach_touch_row('provider.practitioner'::regclass);
SELECT platform.enable_tenant_rls('provider.practitioner'::regclass);

CREATE TABLE provider.practitioner_location (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    practitioner_id     uuid NOT NULL,
    location_id         uuid NOT NULL,
    role                text NOT NULL
                        CHECK (role IN ('ATTENDING','CONSULTANT','TECHNICIAN','ADMINISTRATIVE')),
    valid_from          date NOT NULL,
    valid_to            date,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_practitioner_location_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_practitioner_location_practitioner FOREIGN KEY (tenant_id, practitioner_id)
        REFERENCES provider.practitioner(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_practitioner_location_location FOREIGN KEY (tenant_id, location_id)
        REFERENCES provider.location(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT ck_practitioner_location_period CHECK (valid_to IS NULL OR valid_to > valid_from),
    CONSTRAINT ex_practitioner_location_overlap EXCLUDE USING gist (
        tenant_id WITH =,
        practitioner_id WITH =,
        location_id WITH =,
        role WITH =,
        daterange(valid_from, valid_to, '[)') WITH &&
    )
);
CREATE INDEX ix_practitioner_location_location
    ON provider.practitioner_location (tenant_id, location_id, valid_from);
SELECT platform.attach_touch_row('provider.practitioner_location'::regclass);
SELECT platform.enable_tenant_rls('provider.practitioner_location'::regclass);

SELECT platform.grant_app_schema_usage('provider');
