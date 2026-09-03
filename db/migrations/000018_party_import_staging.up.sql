-- 000018: member import staging (WP-I2-05, v1.2 10.1).
-- Files never write to live tables directly: rows are staged, validated, matched by blind
-- index and applied in chunks. Staging keeps identifiers only as blind index + mask +
-- tenant-encrypted cipher, never as plaintext.

ALTER TABLE party.person
    ADD COLUMN source_system    text,
    ADD COLUMN source_record_id text;
CREATE UNIQUE INDEX uq_person_source_record
    ON party.person (tenant_id, source_system, source_record_id)
    WHERE source_system IS NOT NULL AND source_record_id IS NOT NULL;

CREATE TABLE party.import_batch (
    id                          uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                   uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    sponsor_tenant_organization_id uuid NOT NULL,
    plan_id                     uuid,
    source_system               text NOT NULL CHECK (source_system ~ '^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$'),
    source_version              text NOT NULL CHECK (length(source_version) BETWEEN 1 AND 64),
    file_name                   text NOT NULL,
    file_sha256                 bytea NOT NULL CHECK (octet_length(file_sha256) = 32),
    format                      text NOT NULL DEFAULT 'CSV_V1' CHECK (format IN ('CSV_V1')),
    row_count                   integer NOT NULL DEFAULT 0 CHECK (row_count >= 0),
    status                      text NOT NULL DEFAULT 'RECEIVED'
                                CHECK (status IN ('RECEIVED','VALIDATING','REVIEW','READY','APPLYING','APPLIED','FAILED','CANCELLED')),
    valid_count                 integer NOT NULL DEFAULT 0,
    invalid_count               integer NOT NULL DEFAULT 0,
    matched_count               integer NOT NULL DEFAULT 0,
    conflict_count              integer NOT NULL DEFAULT 0,
    created_count               integer NOT NULL DEFAULT 0,
    updated_count               integer NOT NULL DEFAULT 0,
    skipped_count               integer NOT NULL DEFAULT 0,
    error_summary               text,
    created_by                  uuid REFERENCES iam.actor(id),
    created_at                  timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at                  timestamptz NOT NULL DEFAULT clock_timestamp(),
    applied_at                  timestamptz,
    row_version                 bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_import_batch_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_import_batch_sponsor FOREIGN KEY (tenant_id, sponsor_tenant_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_import_batch_plan FOREIGN KEY (tenant_id, plan_id)
        REFERENCES benefit.plan(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT uq_import_batch_source UNIQUE (tenant_id, source_system, source_version, file_sha256)
);
SELECT platform.attach_touch_row('party.import_batch'::regclass);
SELECT platform.enable_tenant_rls('party.import_batch'::regclass);

CREATE TABLE party.import_row (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    batch_id            uuid NOT NULL,
    row_no              integer NOT NULL CHECK (row_no > 0),
    source_record_id    text NOT NULL,
    -- Non-identifying columns of the row (names, dates, codes). Identifier columns are
    -- stripped before storage and kept only in the two columns below.
    payload             jsonb NOT NULL,
    identifiers         jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{type, scope_key, hash(hex), masked}]
    identifier_cipher   bytea,                                -- tenant-encrypted JSON of {type: value}
    status              text NOT NULL DEFAULT 'PENDING'
                        CHECK (status IN ('PENDING','VALID','INVALID','MATCHED','CONFLICT','APPLIED','SKIPPED')),
    matched_person_id   uuid,
    decision            text CHECK (decision IN ('CREATE','UPDATE','SKIP')),
    decided_by          uuid REFERENCES iam.actor(id),
    errors              jsonb NOT NULL DEFAULT '[]'::jsonb,
    applied_person_id   uuid,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_import_row_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_import_row_batch FOREIGN KEY (tenant_id, batch_id)
        REFERENCES party.import_batch(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_import_row_person FOREIGN KEY (tenant_id, matched_person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT uq_import_row_no UNIQUE (tenant_id, batch_id, row_no),
    CONSTRAINT ck_import_row_payload_no_identifiers CHECK (
        NOT (payload ? 'tckn' OR payload ? 'passport' OR payload ? 'member_no' OR payload ? 'employee_no' OR payload ? 'customer_no')
    )
);
CREATE INDEX ix_import_row_batch_status ON party.import_row (tenant_id, batch_id, status, row_no);
SELECT platform.attach_touch_row('party.import_row'::regclass);
SELECT platform.enable_tenant_rls('party.import_row'::regclass);

SELECT platform.grant_app_schema_usage('party');
