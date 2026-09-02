-- KAPSORA migration 000004: program types, programs, plans, immutable plan versions
-- and enrollments.

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
SELECT platform.attach_touch_updated_at('benefit.program_type');

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
        REFERENCES benefit.program_type(tenant_id, code) ON DELETE RESTRICT,
    CONSTRAINT ck_program_code CHECK (code ~ '^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$')
);
CREATE INDEX ix_program_sponsor_status
    ON benefit.program (tenant_id, sponsor_tenant_organization_id, status);
SELECT platform.attach_touch_row('benefit.program');

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
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_plan_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_plan_code_program UNIQUE (tenant_id, program_id, code),
    CONSTRAINT fk_plan_program
        FOREIGN KEY (tenant_id, program_id)
        REFERENCES benefit.program(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_plan_code CHECK (code ~ '^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$')
);
SELECT platform.attach_touch_row('benefit.plan');

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
        (status IN ('PUBLISHED','RETIRED') AND published_at IS NOT NULL AND published_by IS NOT NULL)
        OR status IN ('DRAFT','UNDER_REVIEW')
    )
);

ALTER TABLE benefit.plan_version
    ADD CONSTRAINT ex_published_plan_version_period
    EXCLUDE USING gist (
        tenant_id WITH =,
        plan_id WITH =,
        valid_period WITH &&
    ) WHERE (status = 'PUBLISHED');

-- Published plan versions are immutable (D7 / v1.2 11.3). The only permitted change to a
-- published row is retirement; retired rows never change; drafts may be deleted.
CREATE OR REPLACE FUNCTION benefit.tg_plan_version_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.status <> 'DRAFT' THEN
            RAISE EXCEPTION 'plan version % is not a draft and cannot be deleted', OLD.id
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        RETURN OLD;
    END IF;

    IF OLD.status = 'PUBLISHED' THEN
        IF NOT (NEW.status = 'RETIRED' AND (to_jsonb(NEW) - 'status') = (to_jsonb(OLD) - 'status')) THEN
            RAISE EXCEPTION 'published plan version % is immutable', OLD.id
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
    ELSIF OLD.status = 'RETIRED' THEN
        RAISE EXCEPTION 'retired plan version % is immutable', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tg_plan_version_guard
    BEFORE UPDATE OR DELETE ON benefit.plan_version
    FOR EACH ROW EXECUTE FUNCTION benefit.tg_plan_version_guard();

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

CREATE INDEX ix_enrollment_membership_status
    ON benefit.enrollment (tenant_id, sponsor_membership_id, status);
CREATE INDEX ix_enrollment_plan_period
    ON benefit.enrollment USING gist (tenant_id, plan_id, valid_period);
SELECT platform.attach_touch_row('benefit.enrollment');

SELECT platform.enable_tenant_rls(t)
FROM unnest(ARRAY[
    'benefit.program_type',
    'benefit.program',
    'benefit.plan',
    'benefit.plan_version',
    'benefit.enrollment'
]::regclass[]) AS t;

SELECT platform.grant_app_schema_usage('benefit');
