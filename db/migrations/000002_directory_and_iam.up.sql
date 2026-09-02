-- KAPSORA migration 000002: global organization directory, actors, tenant membership,
-- tenant-owned roles, permission catalog and access grants.

-- Global legal entities shared across tenants. Deduplicated by country + tax number hash
-- (HMAC-SHA256 blind index). The tax number itself is stored encrypted (D4).
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
    CONSTRAINT ck_org_tax_hash_len CHECK (tax_number_hash IS NULL OR octet_length(tax_number_hash) = 32),
    CONSTRAINT ck_org_tax_pair CHECK (
        (tax_number_cipher IS NULL AND tax_number_hash IS NULL)
        OR (tax_number_cipher IS NOT NULL AND tax_number_hash IS NOT NULL)
    )
);

CREATE UNIQUE INDEX uq_org_tax_hash
    ON directory.organization (country_code, tax_number_hash)
    WHERE tax_number_hash IS NOT NULL;
CREATE INDEX ix_org_display_name_trgm
    ON directory.organization USING gin (display_name gin_trgm_ops);
SELECT platform.attach_touch_row('directory.organization');

-- Other public registry identifiers (MERSIS, provider registry numbers, ...). Tax numbers
-- live on the organization row, not here.
CREATE TABLE directory.organization_identifier (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    organization_id     uuid NOT NULL REFERENCES directory.organization(id) ON DELETE RESTRICT,
    identifier_type     text NOT NULL,
    identifier_value    text NOT NULL,
    issuing_country     char(2),
    valid_period        daterange,
    is_primary          boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_org_identifier UNIQUE (identifier_type, identifier_value),
    CONSTRAINT ck_org_identifier_type CHECK (identifier_type ~ '^[A-Z][A-Z0-9_]{1,63}$')
);
CREATE INDEX ix_org_identifier_org
    ON directory.organization_identifier (organization_id);

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
SELECT platform.attach_touch_updated_at('iam.actor');

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
CREATE INDEX ix_tenant_membership_actor
    ON iam.tenant_membership (actor_id, tenant_id, membership_status);
SELECT platform.attach_touch_row('iam.tenant_membership');

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
    CONSTRAINT uq_role_code_tenant UNIQUE (tenant_id, code),
    CONSTRAINT ck_role_code CHECK (code ~ '^[A-Z][A-Z0-9_]{1,63}$')
);

-- Global permission catalog. Rows are seeded by migration 000008 and referenced by code.
CREATE TABLE iam.permission (
    code                text PRIMARY KEY,
    description         text NOT NULL,
    sensitivity         text NOT NULL DEFAULT 'NORMAL'
                        CHECK (sensitivity IN ('NORMAL','SENSITIVE','PRIVILEGED')),
    CONSTRAINT ck_permission_code CHECK (code ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$')
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

-- Tenant view of an organization: role in this tenant, tenant-specific code and status.
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
    CONSTRAINT uq_tenant_org_id UNIQUE (tenant_id, id),
    CONSTRAINT ck_tenant_org_code CHECK (tenant_code IS NULL OR tenant_code ~ '^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$')
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
CREATE INDEX ix_tenant_org_organization
    ON directory.tenant_organization (organization_id);
-- D12: tenant-specific organization codes are unique within the tenant.
CREATE UNIQUE INDEX uq_tenant_org_code
    ON directory.tenant_organization (tenant_id, tenant_code)
    WHERE tenant_code IS NOT NULL;
SELECT platform.attach_touch_row('directory.tenant_organization');

SELECT platform.enable_tenant_rls(t)
FROM unnest(ARRAY[
    'iam.tenant_membership',
    'iam.role',
    'iam.role_permission',
    'iam.access_grant',
    'directory.tenant_organization'
]::regclass[]) AS t;

SELECT platform.grant_app_schema_usage('directory');
SELECT platform.grant_app_schema_usage('iam');
