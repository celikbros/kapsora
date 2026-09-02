-- KAPSORA migration 000001: extensions, schemas, platform helpers and tenant tables.
-- Requires PostgreSQL 18.x because uuidv7() is the default identifier generator.
-- Forward-only migration policy: see docs/adr/ADR-016.

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

-- ---------------------------------------------------------------------------
-- Session context. Application code sets app.tenant_id and app.actor_id with
-- SET LOCAL inside every transaction; RLS policies read them through these.
-- An unset value yields NULL, so every tenant policy fails closed.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION platform.current_tenant_id()
RETURNS uuid LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$;

CREATE OR REPLACE FUNCTION platform.current_actor_id()
RETURNS uuid LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.actor_id', true), '')::uuid
$$;

-- ---------------------------------------------------------------------------
-- Row maintenance. The database owns updated_at and row_version: application
-- UPDATE statements never assign them; they only compare row_version in WHERE
-- for optimistic concurrency (ETag / If-Match).
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION platform.tg_touch_row()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := clock_timestamp();
    NEW.row_version := OLD.row_version + 1;
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION platform.tg_touch_updated_at()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END
$$;

-- Append-only guard for ledger, status history and audit tables.
CREATE OR REPLACE FUNCTION platform.tg_forbid_update_delete()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'append-only table: % is not allowed on %.%',
        TG_OP, TG_TABLE_SCHEMA, TG_TABLE_NAME
        USING ERRCODE = 'integrity_constraint_violation';
END
$$;

-- ---------------------------------------------------------------------------
-- Helpers used by every migration so tenant tables get identical protection.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION platform.enable_tenant_rls(tbl regclass)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format(
        'CREATE POLICY tenant_isolation ON %s '
        'USING (tenant_id = platform.current_tenant_id()) '
        'WITH CHECK (tenant_id = platform.current_tenant_id())',
        tbl);
END
$$;

CREATE OR REPLACE FUNCTION platform.attach_touch_row(tbl regclass)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format(
        'CREATE TRIGGER tg_touch_row BEFORE UPDATE ON %s '
        'FOR EACH ROW EXECUTE FUNCTION platform.tg_touch_row()', tbl);
END
$$;

CREATE OR REPLACE FUNCTION platform.attach_touch_updated_at(tbl regclass)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format(
        'CREATE TRIGGER tg_touch_updated_at BEFORE UPDATE ON %s '
        'FOR EACH ROW EXECUTE FUNCTION platform.tg_touch_updated_at()', tbl);
END
$$;

CREATE OR REPLACE FUNCTION platform.make_append_only(tbl regclass)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format(
        'CREATE TRIGGER tg_append_only BEFORE UPDATE OR DELETE ON %s '
        'FOR EACH ROW EXECUTE FUNCTION platform.tg_forbid_update_delete()', tbl);
END
$$;

-- Application role privileges are granted only when the role exists. Creating the
-- role is an environment concern (deploy/compose init script, ops runbook), never a
-- migration. The role must NOT have BYPASSRLS and must not own tables.
CREATE OR REPLACE FUNCTION platform.grant_app_schema_usage(
    schema_name text,
    privileges text DEFAULT 'SELECT, INSERT, UPDATE, DELETE')
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'kapsora_app') THEN
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO kapsora_app', schema_name);
        EXECUTE format('GRANT %s ON ALL TABLES IN SCHEMA %I TO kapsora_app', privileges, schema_name);
    END IF;
END
$$;

-- ---------------------------------------------------------------------------
-- Platform tables
-- ---------------------------------------------------------------------------
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
SELECT platform.attach_touch_row('platform.tenant');

CREATE TABLE platform.tenant_setting (
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    setting_key         text NOT NULL,
    value_json          jsonb NOT NULL,
    is_sensitive        boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, setting_key),
    CONSTRAINT ck_tenant_setting_key CHECK (setting_key ~ '^[a-z][a-z0-9_.]{1,119}$')
);
SELECT platform.attach_touch_updated_at('platform.tenant_setting');

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
SELECT platform.attach_touch_updated_at('platform.number_sequence');

CREATE TABLE platform.feature_flag (
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    flag_key            text NOT NULL,
    enabled             boolean NOT NULL DEFAULT false,
    rollout_json        jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, flag_key),
    CONSTRAINT ck_feature_flag_key CHECK (flag_key ~ '^[a-z][a-z0-9_.]{1,79}$')
);
SELECT platform.attach_touch_updated_at('platform.feature_flag');

SELECT platform.enable_tenant_rls(t)
FROM unnest(ARRAY[
    'platform.tenant_setting',
    'platform.number_sequence',
    'platform.feature_flag'
]::regclass[]) AS t;

SELECT platform.grant_app_schema_usage('platform');
