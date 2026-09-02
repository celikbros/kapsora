-- KAPSORA migration 000003: persons, encrypted identifiers, tenant-configurable
-- identifier / relationship / membership type catalogs and sponsor memberships.

CREATE TABLE party.person (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    first_name          text NOT NULL,
    middle_name         text,
    last_name           text NOT NULL,
    normalized_name     text NOT NULL,
    birth_date          date,
    sex_at_birth        text CHECK (sex_at_birth IN ('FEMALE','MALE','INTERSEX','UNKNOWN')),
    preferred_locale    text,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('ACTIVE','INACTIVE','DECEASED','MERGED')),
    merged_into_id      uuid,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by          uuid,
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by          uuid,
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_person_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_person_merge_target
        FOREIGN KEY (tenant_id, merged_into_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_person_merge_state CHECK (
        (status = 'MERGED' AND merged_into_id IS NOT NULL)
        OR (status <> 'MERGED' AND merged_into_id IS NULL)
    ),
    CONSTRAINT ck_person_not_self_merge CHECK (merged_into_id IS NULL OR merged_into_id <> id)
);

CREATE INDEX ix_person_name_trgm
    ON party.person USING gin (normalized_name gin_trgm_ops);
CREATE INDEX ix_person_tenant_birth_date
    ON party.person (tenant_id, birth_date);
SELECT platform.attach_touch_row('party.person');

-- Tenant-configurable identifier types (TCKN, PASSPORT, MEMBER_NO, CUSTOMER_NO, ...).
-- uniqueness_scope drives the scope_key on party.person_identifier (D5).
CREATE TABLE party.identifier_type (
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    code                text NOT NULL,
    display_name        text NOT NULL,
    is_sensitive        boolean NOT NULL DEFAULT true,
    uniqueness_scope    text NOT NULL DEFAULT 'TENANT'
                        CHECK (uniqueness_scope IN ('TENANT','SPONSOR','NONE')),
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('ACTIVE','INACTIVE')),
    metadata            jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, code),
    CONSTRAINT ck_identifier_type_code CHECK (code ~ '^[A-Z][A-Z0-9_]{1,63}$')
);
SELECT platform.attach_touch_updated_at('party.identifier_type');

-- Identifier values are stored as envelope-encrypted ciphertext plus a tenant-bound
-- HMAC-SHA256 blind index for equality search. Plaintext never reaches the database.
--
-- D5: scope_key implements identifier_type.uniqueness_scope. The application derives it:
--   TENANT  -> ''                                   (unique across the tenant)
--   SPONSOR -> sponsor tenant_organization id text  (unique per sponsor)
--   NONE    -> this row's own id text               (never collides)
CREATE TABLE party.person_identifier (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    person_id           uuid NOT NULL,
    identifier_type     text NOT NULL,
    identifier_cipher   bytea NOT NULL,
    identifier_hash     bytea NOT NULL,
    masked_value        text NOT NULL,
    scope_key           text NOT NULL DEFAULT '',
    issuing_country     char(2),
    valid_period        daterange,
    is_primary          boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_person_identifier_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_person_identifier_person
        FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_person_identifier_type
        FOREIGN KEY (tenant_id, identifier_type)
        REFERENCES party.identifier_type(tenant_id, code) ON DELETE RESTRICT,
    CONSTRAINT ck_person_identifier_hash_len CHECK (octet_length(identifier_hash) = 32),
    CONSTRAINT uq_person_identifier_hash UNIQUE (tenant_id, identifier_type, scope_key, identifier_hash)
);

CREATE INDEX ix_person_identifier_person
    ON party.person_identifier (tenant_id, person_id, identifier_type);
CREATE UNIQUE INDEX uq_person_identifier_primary
    ON party.person_identifier (tenant_id, person_id, identifier_type)
    WHERE is_primary;

CREATE TABLE party.relationship_type (
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    code                text NOT NULL,
    display_name        text NOT NULL,
    is_directional      boolean NOT NULL DEFAULT true,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('ACTIVE','INACTIVE')),
    metadata            jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, code),
    CONSTRAINT ck_relationship_type_code CHECK (code ~ '^[A-Z][A-Z0-9_]{1,63}$')
);
SELECT platform.attach_touch_updated_at('party.relationship_type');

CREATE TABLE party.person_relationship (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    source_person_id    uuid NOT NULL,
    target_person_id    uuid NOT NULL,
    relationship_type   text NOT NULL,
    valid_period        daterange NOT NULL,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('ACTIVE','SUSPENDED','ENDED')),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_person_relationship_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_relationship_source
        FOREIGN KEY (tenant_id, source_person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_relationship_target
        FOREIGN KEY (tenant_id, target_person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_relationship_type
        FOREIGN KEY (tenant_id, relationship_type)
        REFERENCES party.relationship_type(tenant_id, code) ON DELETE RESTRICT,
    CONSTRAINT ck_relationship_distinct CHECK (source_person_id <> target_person_id)
);

ALTER TABLE party.person_relationship
    ADD CONSTRAINT ex_person_relationship_period
    EXCLUDE USING gist (
        tenant_id WITH =,
        source_person_id WITH =,
        target_person_id WITH =,
        relationship_type WITH =,
        valid_period WITH &&
    ) WHERE (status IN ('ACTIVE','SUSPENDED'));

CREATE INDEX ix_relationship_source_period
    ON party.person_relationship (tenant_id, source_person_id, relationship_type);
CREATE INDEX ix_relationship_target_period
    ON party.person_relationship (tenant_id, target_person_id, relationship_type);

CREATE TABLE party.membership_type (
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    code                text NOT NULL,
    display_name        text NOT NULL,
    requires_principal  boolean NOT NULL DEFAULT false,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('ACTIVE','INACTIVE')),
    metadata            jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, code),
    CONSTRAINT ck_membership_type_code CHECK (code ~ '^[A-Z][A-Z0-9_]{1,63}$')
);
SELECT platform.attach_touch_updated_at('party.membership_type');

CREATE TABLE party.sponsor_membership (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    person_id           uuid NOT NULL,
    sponsor_tenant_organization_id uuid NOT NULL,
    principal_membership_id uuid,
    membership_type     text NOT NULL,
    external_member_no  text,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('PENDING','ACTIVE','SUSPENDED','ENDED')),
    valid_period        daterange NOT NULL,
    source_system       text,
    source_record_id    text,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_sponsor_membership_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_sponsor_membership_person
        FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_sponsor_membership_org
        FOREIGN KEY (tenant_id, sponsor_tenant_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_sponsor_membership_type
        FOREIGN KEY (tenant_id, membership_type)
        REFERENCES party.membership_type(tenant_id, code) ON DELETE RESTRICT,
    CONSTRAINT fk_principal_membership
        FOREIGN KEY (tenant_id, principal_membership_id)
        REFERENCES party.sponsor_membership(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_principal_membership_not_self CHECK (principal_membership_id IS NULL OR principal_membership_id <> id)
);

ALTER TABLE party.sponsor_membership
    ADD CONSTRAINT ex_sponsor_membership_period
    EXCLUDE USING gist (
        tenant_id WITH =,
        person_id WITH =,
        sponsor_tenant_organization_id WITH =,
        membership_type WITH =,
        valid_period WITH &&
    ) WHERE (status IN ('PENDING','ACTIVE'));

CREATE UNIQUE INDEX uq_sponsor_external_member_no
    ON party.sponsor_membership (tenant_id, sponsor_tenant_organization_id, external_member_no)
    WHERE external_member_no IS NOT NULL AND status IN ('PENDING','ACTIVE');
CREATE INDEX ix_sponsor_membership_person
    ON party.sponsor_membership (tenant_id, person_id, status);
CREATE INDEX ix_sponsor_membership_source
    ON party.sponsor_membership (tenant_id, source_system, source_record_id)
    WHERE source_record_id IS NOT NULL;
SELECT platform.attach_touch_row('party.sponsor_membership');

SELECT platform.enable_tenant_rls(t)
FROM unnest(ARRAY[
    'party.person',
    'party.identifier_type',
    'party.person_identifier',
    'party.relationship_type',
    'party.person_relationship',
    'party.membership_type',
    'party.sponsor_membership'
]::regclass[]) AS t;

SELECT platform.grant_app_schema_usage('party');
