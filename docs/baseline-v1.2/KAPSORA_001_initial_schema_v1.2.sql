-- KAPSORA - Initial PostgreSQL 18 schema
-- Version: 1.0.0
-- Purpose: Sprint-1/Sprint-2 development baseline. Later migrations add provider,
-- contract, health, accommodation, adjudication, billing, document and integration schemas.
-- Requires PostgreSQL 18.x because uuidv7() is used as the default identifier generator.

BEGIN;

CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE SCHEMA IF NOT EXISTS platform;
CREATE SCHEMA IF NOT EXISTS directory;
CREATE SCHEMA IF NOT EXISTS iam;
CREATE SCHEMA IF NOT EXISTS party;
CREATE SCHEMA IF NOT EXISTS benefit;
CREATE SCHEMA IF NOT EXISTS catalog;
CREATE SCHEMA IF NOT EXISTS service;
CREATE SCHEMA IF NOT EXISTS workflow;
CREATE SCHEMA IF NOT EXISTS system;
CREATE SCHEMA IF NOT EXISTS audit;

CREATE OR REPLACE FUNCTION platform.current_tenant_id()
RETURNS uuid
LANGUAGE sql
STABLE
AS $$
    SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$;

CREATE OR REPLACE FUNCTION platform.current_actor_id()
RETURNS uuid
LANGUAGE sql
STABLE
AS $$
    SELECT NULLIF(current_setting('app.actor_id', true), '')::uuid
$$;

CREATE TABLE platform.tenant (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    code                citext NOT NULL,
    legal_name          text NOT NULL,
    display_name        text NOT NULL,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('PROVISIONING','ACTIVE','SUSPENDED','CLOSED')),
    default_locale      text NOT NULL DEFAULT 'tr-TR',
    default_time_zone   text NOT NULL DEFAULT 'Europe/Istanbul',
    default_currency    char(3) NOT NULL DEFAULT 'TRY',
    data_region         text,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by          uuid,
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by          uuid,
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_tenant_code UNIQUE (code),
    CONSTRAINT ck_tenant_code_format CHECK (code::text ~ '^[A-Za-z0-9][A-Za-z0-9_-]{2,39}$')
);

CREATE TABLE platform.tenant_setting (
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    setting_key         text NOT NULL,
    value_json          jsonb NOT NULL,
    is_sensitive        boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, setting_key)
);

CREATE TABLE platform.number_sequence (
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    sequence_key        text NOT NULL,
    period_key          text NOT NULL DEFAULT '',
    prefix              text NOT NULL,
    next_value          bigint NOT NULL DEFAULT 1 CHECK (next_value > 0),
    padding_width       smallint NOT NULL DEFAULT 8 CHECK (padding_width BETWEEN 1 AND 18),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, sequence_key, period_key)
);

CREATE TABLE directory.organization (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    legal_name          text NOT NULL,
    display_name        text NOT NULL,
    organization_kind   text NOT NULL
                        CHECK (organization_kind IN (
                            'BANK','INSURER','SPONSOR','PROVIDER','VENDOR','PUBLIC_BODY','OTHER'
                        )),
    country_code        char(2) NOT NULL DEFAULT 'TR',
    tax_number_cipher   bytea,
    tax_number_hash     bytea,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('ACTIVE','SUSPENDED','CLOSED')),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT ck_org_tax_hash_len CHECK (tax_number_hash IS NULL OR octet_length(tax_number_hash) = 32)
);

CREATE UNIQUE INDEX uq_org_tax_hash
    ON directory.organization (country_code, tax_number_hash)
    WHERE tax_number_hash IS NOT NULL;
CREATE INDEX ix_org_display_name_trgm
    ON directory.organization USING gin (display_name gin_trgm_ops);

CREATE TABLE directory.organization_identifier (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    organization_id     uuid NOT NULL REFERENCES directory.organization(id) ON DELETE RESTRICT,
    identifier_type     text NOT NULL,
    identifier_value    text NOT NULL,
    issuing_country     char(2),
    valid_period        daterange,
    is_primary          boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_org_identifier UNIQUE (identifier_type, identifier_value)
);

CREATE TABLE iam.actor (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    identity_subject    text NOT NULL,
    identity_issuer     text NOT NULL,
    actor_type          text NOT NULL
                        CHECK (actor_type IN ('HUMAN','SERVICE_ACCOUNT','SYSTEM')),
    display_name        text NOT NULL,
    email               citext,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('INVITED','ACTIVE','SUSPENDED','CLOSED')),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_actor_subject UNIQUE (identity_issuer, identity_subject)
);

CREATE TABLE iam.tenant_membership (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    actor_id            uuid NOT NULL REFERENCES iam.actor(id) ON DELETE RESTRICT,
    membership_status   text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (membership_status IN ('PENDING','ACTIVE','SUSPENDED','REVOKED')),
    valid_period        daterange NOT NULL DEFAULT daterange(CURRENT_DATE, NULL, '[)'),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by          uuid,
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_tenant_membership_id UNIQUE (tenant_id, id)
);

ALTER TABLE iam.tenant_membership
    ADD CONSTRAINT ex_actor_tenant_membership_period
    EXCLUDE USING gist (
        tenant_id WITH =,
        actor_id WITH =,
        valid_period WITH &&
    ) WHERE (membership_status IN ('PENDING','ACTIVE'));

-- Roles are tenant-owned. Platform-provided role templates are copied into each tenant
-- during provisioning; this keeps access grants and RLS constraints tenant-safe.
CREATE TABLE iam.role (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    code                text NOT NULL,
    name                text NOT NULL,
    description         text,
    is_system_role      boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_role_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_role_code_tenant UNIQUE (tenant_id, code)
);

CREATE TABLE iam.permission (
    code                text PRIMARY KEY,
    description         text NOT NULL,
    sensitivity         text NOT NULL DEFAULT 'NORMAL'
                        CHECK (sensitivity IN ('NORMAL','SENSITIVE','PRIVILEGED'))
);

CREATE TABLE iam.role_permission (
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    role_id             uuid NOT NULL,
    permission_code     text NOT NULL REFERENCES iam.permission(code) ON DELETE RESTRICT,
    PRIMARY KEY (tenant_id, role_id, permission_code),
    CONSTRAINT fk_role_permission_role
        FOREIGN KEY (tenant_id, role_id)
        REFERENCES iam.role(tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE iam.access_grant (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    tenant_membership_id uuid NOT NULL,
    role_id             uuid NOT NULL,
    scope_type          text NOT NULL DEFAULT 'TENANT'
                        CHECK (scope_type IN ('TENANT','ORGANIZATION','PROGRAM','PROVIDER_LOCATION','WORK_QUEUE')),
    scope_id            uuid,
    valid_period        tstzrange NOT NULL DEFAULT tstzrange(clock_timestamp(), NULL, '[)'),
    granted_by          uuid,
    grant_reason        text,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_access_grant_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_access_grant_membership
        FOREIGN KEY (tenant_id, tenant_membership_id)
        REFERENCES iam.tenant_membership(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_access_grant_role
        FOREIGN KEY (tenant_id, role_id)
        REFERENCES iam.role(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_access_grant_scope CHECK (
        (scope_type = 'TENANT' AND scope_id IS NULL)
        OR (scope_type <> 'TENANT' AND scope_id IS NOT NULL)
    )
);

CREATE INDEX ix_access_grant_actor_scope
    ON iam.access_grant (tenant_id, tenant_membership_id, scope_type, scope_id);

CREATE TABLE directory.tenant_organization (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    organization_id     uuid NOT NULL REFERENCES directory.organization(id) ON DELETE RESTRICT,
    relationship_role   text NOT NULL
                        CHECK (relationship_role IN ('PAYER','SPONSOR','PROVIDER','VENDOR','PARTNER')),
    tenant_code         text,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('PENDING','ACTIVE','SUSPENDED','TERMINATED')),
    valid_period        daterange NOT NULL DEFAULT daterange(CURRENT_DATE, NULL, '[)'),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_tenant_org_id UNIQUE (tenant_id, id)
);

ALTER TABLE directory.tenant_organization
    ADD CONSTRAINT ex_tenant_org_role_period
    EXCLUDE USING gist (
        tenant_id WITH =,
        organization_id WITH =,
        relationship_role WITH =,
        valid_period WITH &&
    ) WHERE (status IN ('PENDING','ACTIVE'));

CREATE INDEX ix_tenant_org_role_status
    ON directory.tenant_organization (tenant_id, relationship_role, status);

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
    )
);

CREATE INDEX ix_person_name_trgm
    ON party.person USING gin (normalized_name gin_trgm_ops);
CREATE INDEX ix_person_tenant_birth_date
    ON party.person (tenant_id, birth_date);

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

CREATE TABLE party.person_identifier (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    person_id           uuid NOT NULL,
    identifier_type     text NOT NULL,
    identifier_cipher   bytea NOT NULL,
    identifier_hash     bytea NOT NULL,
    masked_value        text NOT NULL,
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
    CONSTRAINT uq_person_identifier_hash UNIQUE (tenant_id, identifier_type, identifier_hash)
);

CREATE INDEX ix_person_identifier_person
    ON party.person_identifier (tenant_id, person_id, identifier_type);

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

CREATE TABLE benefit.program_type (
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    code                text NOT NULL,
    display_name        text NOT NULL,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('ACTIVE','INACTIVE')),
    metadata            jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, code),
    CONSTRAINT ck_program_type_code CHECK (code ~ '^[A-Z][A-Z0-9_]{1,63}$')
);

CREATE TABLE benefit.program (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    sponsor_tenant_organization_id uuid NOT NULL,
    payer_tenant_organization_id uuid NOT NULL,
    code                text NOT NULL,
    name                text NOT NULL,
    program_type        text NOT NULL,
    status              text NOT NULL DEFAULT 'DRAFT'
                        CHECK (status IN ('DRAFT','ACTIVE','SUSPENDED','CLOSED')),
    valid_period        daterange NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_program_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_program_code_tenant UNIQUE (tenant_id, code),
    CONSTRAINT fk_program_sponsor
        FOREIGN KEY (tenant_id, sponsor_tenant_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_program_payer
        FOREIGN KEY (tenant_id, payer_tenant_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_program_type
        FOREIGN KEY (tenant_id, program_type)
        REFERENCES benefit.program_type(tenant_id, code) ON DELETE RESTRICT
);

CREATE TABLE benefit.plan (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    program_id          uuid NOT NULL,
    code                text NOT NULL,
    name                text NOT NULL,
    status              text NOT NULL DEFAULT 'DRAFT'
                        CHECK (status IN ('DRAFT','ACTIVE','RETIRED')),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_plan_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_plan_code_program UNIQUE (tenant_id, program_id, code),
    CONSTRAINT fk_plan_program
        FOREIGN KEY (tenant_id, program_id)
        REFERENCES benefit.program(tenant_id, id) ON DELETE RESTRICT
);

CREATE TABLE benefit.plan_version (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    plan_id             uuid NOT NULL,
    version_no          integer NOT NULL CHECK (version_no > 0),
    status              text NOT NULL DEFAULT 'DRAFT'
                        CHECK (status IN ('DRAFT','UNDER_REVIEW','PUBLISHED','RETIRED')),
    valid_period        daterange NOT NULL,
    configuration_hash  bytea,
    published_at        timestamptz,
    published_by        uuid,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by          uuid,
    CONSTRAINT uq_plan_version_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_plan_version_no UNIQUE (tenant_id, plan_id, version_no),
    CONSTRAINT fk_plan_version_plan
        FOREIGN KEY (tenant_id, plan_id)
        REFERENCES benefit.plan(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_plan_version_publish CHECK (
        (status = 'PUBLISHED' AND published_at IS NOT NULL AND published_by IS NOT NULL)
        OR status <> 'PUBLISHED'
    )
);

ALTER TABLE benefit.plan_version
    ADD CONSTRAINT ex_published_plan_version_period
    EXCLUDE USING gist (
        tenant_id WITH =,
        plan_id WITH =,
        valid_period WITH &&
    ) WHERE (status = 'PUBLISHED');

CREATE TABLE benefit.enrollment (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    sponsor_membership_id uuid NOT NULL,
    plan_id             uuid NOT NULL,
    status              text NOT NULL DEFAULT 'ACTIVE'
                        CHECK (status IN ('PENDING','ACTIVE','SUSPENDED','ENDED')),
    valid_period        daterange NOT NULL,
    enrollment_reason   text,
    source_system       text,
    source_record_id    text,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_enrollment_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_enrollment_membership
        FOREIGN KEY (tenant_id, sponsor_membership_id)
        REFERENCES party.sponsor_membership(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_enrollment_plan
        FOREIGN KEY (tenant_id, plan_id)
        REFERENCES benefit.plan(tenant_id, id) ON DELETE RESTRICT
);

ALTER TABLE benefit.enrollment
    ADD CONSTRAINT ex_enrollment_period
    EXCLUDE USING gist (
        tenant_id WITH =,
        sponsor_membership_id WITH =,
        plan_id WITH =,
        valid_period WITH &&
    ) WHERE (status IN ('PENDING','ACTIVE'));

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
    CONSTRAINT ck_entitlement_currency CHECK (
        (unit_type = 'MONEY' AND currency_code IS NOT NULL)
        OR (unit_type <> 'MONEY' AND currency_code IS NULL)
    ),
    CONSTRAINT ck_entitlement_period_length CHECK (
        (period_type = 'ROLLING_DAYS' AND period_length IS NOT NULL AND period_length > 0)
        OR (period_type <> 'ROLLING_DAYS')
    )
);

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

CREATE TABLE catalog.service_category (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    parent_id           uuid,
    code                text NOT NULL,
    name                text NOT NULL,
    domain_code         text NOT NULL
                        CHECK (domain_code IN ('GENERIC','HEALTH','ACCOMMODATION','ASSISTANCE','EDUCATION','SPORT','TRANSPORT','OTHER')),
    active              boolean NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_service_category_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_service_category_code UNIQUE (tenant_id, code),
    CONSTRAINT fk_service_category_parent
        FOREIGN KEY (tenant_id, parent_id)
        REFERENCES catalog.service_category(tenant_id, id) ON DELETE RESTRICT
);

CREATE TABLE catalog.service_definition (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    category_id         uuid NOT NULL,
    code                text NOT NULL,
    name                text NOT NULL,
    description         text,
    fulfillment_mode    text NOT NULL
                        CHECK (fulfillment_mode IN ('APPOINTMENT','RESERVATION','WORK_ORDER','MEMBERSHIP','SESSION','VOUCHER','REIMBURSEMENT','DIRECT')),
    default_unit_type   text NOT NULL
                        CHECK (default_unit_type IN ('MONEY','COUNT','NIGHT','SESSION','HOUR','KILOMETER','POINT')),
    requires_provider   boolean NOT NULL DEFAULT true,
    active              boolean NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_service_definition_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_service_definition_code UNIQUE (tenant_id, code),
    CONSTRAINT fk_service_definition_category
        FOREIGN KEY (tenant_id, category_id)
        REFERENCES catalog.service_category(tenant_id, id) ON DELETE RESTRICT
);

CREATE INDEX ix_service_definition_name_trgm
    ON catalog.service_definition USING gin (name gin_trgm_ops);

CREATE TABLE service.service_request (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    request_reference   text NOT NULL,
    request_type        text NOT NULL
                        CHECK (request_type IN ('DIRECT_SERVICE','PREAUTHORIZATION','RESERVATION','REIMBURSEMENT')),
    person_id           uuid NOT NULL,
    program_id          uuid NOT NULL,
    enrollment_id       uuid NOT NULL,
    provider_tenant_organization_id uuid,
    service_date        date NOT NULL,
    requested_start_at  timestamptz,
    requested_end_at    timestamptz,
    channel             text NOT NULL
                        CHECK (channel IN ('BACKOFFICE','PROVIDER_PORTAL','MEMBER_PORTAL','API','BATCH_IMPORT','CALL_CENTER')),
    status              text NOT NULL DEFAULT 'DRAFT'
                        CHECK (status IN (
                            'DRAFT','SUBMITTED','ELIGIBILITY_FAILED','PENDING_DOCUMENT',
                            'PENDING_REVIEW','APPROVED','PARTIALLY_APPROVED','REJECTED',
                            'CANCELLED','EXPIRED','CLOSED'
                        )),
    current_version_no  integer NOT NULL DEFAULT 1 CHECK (current_version_no > 0),
    supersedes_request_id uuid,
    submitted_at        timestamptz,
    closed_at           timestamptz,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by          uuid,
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by          uuid,
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_service_request_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_service_request_reference UNIQUE (tenant_id, request_reference),
    CONSTRAINT fk_service_request_person
        FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_service_request_program
        FOREIGN KEY (tenant_id, program_id)
        REFERENCES benefit.program(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_service_request_enrollment
        FOREIGN KEY (tenant_id, enrollment_id)
        REFERENCES benefit.enrollment(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_service_request_provider
        FOREIGN KEY (tenant_id, provider_tenant_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_service_request_supersedes
        FOREIGN KEY (tenant_id, supersedes_request_id)
        REFERENCES service.service_request(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_service_request_times CHECK (
        requested_end_at IS NULL OR requested_start_at IS NULL OR requested_end_at > requested_start_at
    ),
    CONSTRAINT ck_service_request_submission CHECK (
        (status = 'DRAFT' AND submitted_at IS NULL)
        OR (status <> 'DRAFT' AND submitted_at IS NOT NULL)
    )
);

CREATE INDEX ix_service_request_worklist
    ON service.service_request (tenant_id, status, created_at DESC, id);
CREATE INDEX ix_service_request_person_date
    ON service.service_request (tenant_id, person_id, service_date DESC);
CREATE INDEX ix_service_request_provider_status
    ON service.service_request (tenant_id, provider_tenant_organization_id, status, service_date DESC);

CREATE TABLE service.service_request_item (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    service_request_id  uuid NOT NULL,
    line_no             integer NOT NULL CHECK (line_no > 0),
    service_definition_id uuid NOT NULL,
    requested_quantity  numeric(20,6) NOT NULL CHECK (requested_quantity > 0),
    unit_type           text NOT NULL,
    requested_amount    numeric(20,6),
    currency_code       char(3),
    status              text NOT NULL DEFAULT 'REQUESTED'
                        CHECK (status IN ('REQUESTED','APPROVED','PARTIALLY_APPROVED','REJECTED','CANCELLED')),
    approved_quantity   numeric(20,6),
    approved_amount     numeric(20,6),
    decision_reason_code text,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_service_request_item_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_service_request_line UNIQUE (tenant_id, service_request_id, line_no),
    CONSTRAINT fk_service_request_item_request
        FOREIGN KEY (tenant_id, service_request_id)
        REFERENCES service.service_request(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_service_request_item_service
        FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_service_item_amount_currency CHECK (
        (requested_amount IS NULL AND currency_code IS NULL)
        OR (requested_amount IS NOT NULL AND requested_amount >= 0 AND currency_code IS NOT NULL)
    ),
    CONSTRAINT ck_service_item_approved_values CHECK (
        approved_quantity IS NULL OR approved_quantity >= 0
    )
);

CREATE INDEX ix_service_request_item_request
    ON service.service_request_item (tenant_id, service_request_id, line_no);

CREATE TABLE workflow.status_event (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    aggregate_type      text NOT NULL,
    aggregate_id        uuid NOT NULL,
    from_status         text,
    to_status           text NOT NULL,
    transition_code     text NOT NULL,
    reason_code         text,
    reason_text         text,
    occurred_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    actor_id            uuid,
    request_id          uuid,
    metadata_json       jsonb NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT uq_status_event_id_tenant UNIQUE (tenant_id, id)
);

CREATE INDEX ix_status_event_aggregate
    ON workflow.status_event (tenant_id, aggregate_type, aggregate_id, occurred_at, id);

CREATE TABLE system.outbox_event (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    aggregate_type      text NOT NULL,
    aggregate_id        uuid NOT NULL,
    event_type          text NOT NULL,
    event_schema_version integer NOT NULL DEFAULT 1 CHECK (event_schema_version > 0),
    payload_json        jsonb NOT NULL,
    headers_json        jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    available_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    status              text NOT NULL DEFAULT 'PENDING'
                        CHECK (status IN ('PENDING','PROCESSING','SUCCEEDED','FAILED','DEAD_LETTER')),
    attempt_count       integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    locked_at           timestamptz,
    locked_by           text,
    last_error_code     text,
    last_error_message  text,
    processed_at        timestamptz,
    deduplication_key   text,
    CONSTRAINT uq_outbox_dedupe UNIQUE NULLS NOT DISTINCT (tenant_id, event_type, deduplication_key)
);

CREATE INDEX ix_outbox_dispatch
    ON system.outbox_event (status, available_at, occurred_at)
    WHERE status IN ('PENDING','FAILED');
CREATE INDEX ix_outbox_aggregate
    ON system.outbox_event (tenant_id, aggregate_type, aggregate_id, occurred_at);

CREATE TABLE audit.event (
    id                  uuid NOT NULL DEFAULT uuidv7(),
    occurred_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    tenant_id           uuid,
    actor_id            uuid,
    event_category      text NOT NULL
                        CHECK (event_category IN ('ACCESS','AUTHENTICATION','BUSINESS','ADMIN','SECURITY','EXPORT','PRIVACY')),
    action_code         text NOT NULL,
    resource_type       text,
    resource_id         uuid,
    outcome             text NOT NULL CHECK (outcome IN ('SUCCESS','DENIED','FAILURE')),
    request_id          uuid,
    trace_id            text,
    source_ip           inet,
    user_agent_hash     bytea,
    reason_code         text,
    detail_json         jsonb NOT NULL DEFAULT '{}'::jsonb,
    before_hash         bytea,
    after_hash          bytea,
    PRIMARY KEY (occurred_at, id)
) PARTITION BY RANGE (occurred_at);

CREATE TABLE audit.event_default
    PARTITION OF audit.event DEFAULT;

CREATE INDEX ix_audit_default_tenant_time
    ON audit.event_default (tenant_id, occurred_at DESC, id);
CREATE INDEX ix_audit_default_resource
    ON audit.event_default (tenant_id, resource_type, resource_id, occurred_at DESC);
CREATE INDEX ix_audit_default_actor
    ON audit.event_default (tenant_id, actor_id, occurred_at DESC);

-- Tenant Row-Level Security. The migration/maintenance role must have BYPASSRLS;
-- application connections must set app.tenant_id and app.actor_id with SET LOCAL
-- inside each transaction.
DO $$
DECLARE
    tbl regclass;
BEGIN
    FOREACH tbl IN ARRAY ARRAY[
        'platform.tenant_setting'::regclass,
        'platform.number_sequence'::regclass,
        'iam.tenant_membership'::regclass,
        'iam.role'::regclass,
        'iam.role_permission'::regclass,
        'iam.access_grant'::regclass,
        'directory.tenant_organization'::regclass,
        'party.person'::regclass,
        'party.identifier_type'::regclass,
        'party.person_identifier'::regclass,
        'party.relationship_type'::regclass,
        'party.person_relationship'::regclass,
        'party.membership_type'::regclass,
        'party.sponsor_membership'::regclass,
        'benefit.program_type'::regclass,
        'benefit.program'::regclass,
        'benefit.plan'::regclass,
        'benefit.plan_version'::regclass,
        'benefit.enrollment'::regclass,
        'benefit.entitlement_definition'::regclass,
        'benefit.entitlement_account'::regclass,
        'benefit.entitlement_ledger'::regclass,
        'catalog.service_category'::regclass,
        'catalog.service_definition'::regclass,
        'service.service_request'::regclass,
        'service.service_request_item'::regclass,
        'workflow.status_event'::regclass
    ]
    LOOP
        EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', tbl);
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', tbl);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %s USING (tenant_id = platform.current_tenant_id()) WITH CHECK (tenant_id = platform.current_tenant_id())',
            tbl
        );
    END LOOP;
END $$;

-- Tenant provisioning MUST seed baseline configurable identifier, relationship, membership and program type codes.
-- Customer-specific type codes are added as data, not by altering CHECK constraints or deploying schema migrations.
-- Global reference code systems (for example ICD-10) are kept in a separate read-only reference schema in later migrations.
-- Business service catalog rows are tenant-owned so all composite foreign keys remain tenant-safe.

COMMIT;
