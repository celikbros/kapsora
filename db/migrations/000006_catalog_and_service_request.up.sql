-- KAPSORA migration 000006: service catalog, generic service requests with immutable
-- submitted versions (D6), and request items.

CREATE TABLE catalog.service_category (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    parent_id           uuid,
    code                text NOT NULL,
    name                text NOT NULL,
    -- D10: CARE added for the post-MVP care vertical; closed list by design.
    domain_code         text NOT NULL
                        CHECK (domain_code IN (
                            'GENERIC','HEALTH','ACCOMMODATION','ASSISTANCE','EDUCATION',
                            'SPORT','TRANSPORT','CARE','OTHER'
                        )),
    active              boolean NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_service_category_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_service_category_code UNIQUE (tenant_id, code),
    CONSTRAINT fk_service_category_parent
        FOREIGN KEY (tenant_id, parent_id)
        REFERENCES catalog.service_category(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_service_category_code CHECK (code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    CONSTRAINT ck_service_category_not_self_parent CHECK (parent_id IS NULL OR parent_id <> id)
);

CREATE TABLE catalog.service_definition (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    category_id         uuid NOT NULL,
    code                text NOT NULL,
    name                text NOT NULL,
    description         text,
    fulfillment_mode    text NOT NULL
                        CHECK (fulfillment_mode IN (
                            'APPOINTMENT','RESERVATION','WORK_ORDER','MEMBERSHIP',
                            'SESSION','VOUCHER','REIMBURSEMENT','DIRECT'
                        )),
    default_unit_type   text NOT NULL
                        CHECK (default_unit_type IN ('MONEY','COUNT','NIGHT','SESSION','HOUR','KILOMETER','POINT')),
    requires_provider   boolean NOT NULL DEFAULT true,
    active              boolean NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_service_definition_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_service_definition_code UNIQUE (tenant_id, code),
    CONSTRAINT fk_service_definition_category
        FOREIGN KEY (tenant_id, category_id)
        REFERENCES catalog.service_category(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_service_definition_code CHECK (code ~ '^[A-Z][A-Z0-9_]{1,63}$')
);

CREATE INDEX ix_service_definition_name_trgm
    ON catalog.service_definition USING gin (name gin_trgm_ops);
CREATE INDEX ix_service_definition_category
    ON catalog.service_definition (tenant_id, category_id, active);
SELECT platform.attach_touch_row('catalog.service_definition');

-- Service request header. Editable content lives on the current DRAFT version; submitted
-- versions are immutable snapshots (D6). Status is changed only by explicit commands.
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
    -- D1: submitted_at means "was submitted at least once". A draft has none; a cancelled
    -- request may or may not have been submitted; every other status requires it.
    CONSTRAINT ck_service_request_submission CHECK (
        (status = 'DRAFT' AND submitted_at IS NULL)
        OR status = 'CANCELLED'
        OR (status NOT IN ('DRAFT','CANCELLED') AND submitted_at IS NOT NULL)
    ),
    CONSTRAINT ck_service_request_closed CHECK (
        (status IN ('CLOSED','CANCELLED','EXPIRED','REJECTED') AND closed_at IS NOT NULL)
        OR (status NOT IN ('CLOSED','CANCELLED','EXPIRED','REJECTED') AND closed_at IS NULL)
    )
);

CREATE INDEX ix_service_request_worklist
    ON service.service_request (tenant_id, status, created_at DESC, id);
CREATE INDEX ix_service_request_person_date
    ON service.service_request (tenant_id, person_id, service_date DESC);
CREATE INDEX ix_service_request_provider_status
    ON service.service_request (tenant_id, provider_tenant_organization_id, status, service_date DESC);
SELECT platform.attach_touch_row('service.service_request');

-- One row per version of the request content. Exactly one DRAFT version may exist per
-- request; submitting freezes it (snapshot_json holds header + items as submitted).
CREATE TABLE service.service_request_version (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    service_request_id  uuid NOT NULL,
    version_no          integer NOT NULL CHECK (version_no > 0),
    status              text NOT NULL DEFAULT 'DRAFT'
                        CHECK (status IN ('DRAFT','SUBMITTED','SUPERSEDED')),
    snapshot_json       jsonb,
    submitted_at        timestamptz,
    submitted_by        uuid,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by          uuid,
    CONSTRAINT uq_service_request_version_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_service_request_version_no UNIQUE (tenant_id, service_request_id, version_no),
    CONSTRAINT fk_service_request_version_request
        FOREIGN KEY (tenant_id, service_request_id)
        REFERENCES service.service_request(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_service_request_version_submit CHECK (
        (status = 'DRAFT' AND submitted_at IS NULL AND snapshot_json IS NULL)
        OR (status <> 'DRAFT' AND submitted_at IS NOT NULL AND snapshot_json IS NOT NULL)
    )
);

CREATE UNIQUE INDEX uq_service_request_single_draft
    ON service.service_request_version (tenant_id, service_request_id)
    WHERE status = 'DRAFT';

CREATE OR REPLACE FUNCTION service.tg_request_version_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.status <> 'DRAFT' THEN
            RAISE EXCEPTION 'service request version % is not a draft and cannot be deleted', OLD.id
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        RETURN OLD;
    END IF;

    IF OLD.status = 'SUBMITTED' THEN
        IF NOT (NEW.status = 'SUPERSEDED' AND (to_jsonb(NEW) - 'status') = (to_jsonb(OLD) - 'status')) THEN
            RAISE EXCEPTION 'submitted service request version % is immutable', OLD.id
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
    ELSIF OLD.status = 'SUPERSEDED' THEN
        RAISE EXCEPTION 'superseded service request version % is immutable', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tg_request_version_guard
    BEFORE UPDATE OR DELETE ON service.service_request_version
    FOR EACH ROW EXECUTE FUNCTION service.tg_request_version_guard();

-- Items belong to a version. Requested values freeze when the version leaves DRAFT;
-- decision columns (status, approved_*, decision_reason_code) stay writable so reviewers
-- can record line-level outcomes.
CREATE TABLE service.service_request_item (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    service_request_version_id uuid NOT NULL,
    line_no             integer NOT NULL CHECK (line_no > 0),
    service_definition_id uuid NOT NULL,
    requested_quantity  numeric(20,6) NOT NULL CHECK (requested_quantity > 0),
    -- D9: same closed list as catalog and entitlement unit types.
    unit_type           text NOT NULL
                        CHECK (unit_type IN ('MONEY','COUNT','NIGHT','SESSION','HOUR','KILOMETER','POINT')),
    requested_amount    numeric(20,6),
    currency_code       char(3),
    status              text NOT NULL DEFAULT 'REQUESTED'
                        CHECK (status IN ('REQUESTED','APPROVED','PARTIALLY_APPROVED','REJECTED','CANCELLED')),
    approved_quantity   numeric(20,6),
    approved_amount     numeric(20,6),
    decision_reason_code text,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_service_request_item_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_service_request_line UNIQUE (tenant_id, service_request_version_id, line_no),
    CONSTRAINT fk_service_request_item_version
        FOREIGN KEY (tenant_id, service_request_version_id)
        REFERENCES service.service_request_version(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_service_request_item_service
        FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_service_item_amount_currency CHECK (
        (requested_amount IS NULL AND currency_code IS NULL)
        OR (requested_amount IS NOT NULL AND requested_amount >= 0 AND currency_code IS NOT NULL)
    ),
    CONSTRAINT ck_service_item_approved_values CHECK (
        (approved_quantity IS NULL OR approved_quantity >= 0)
        AND (approved_amount IS NULL OR approved_amount >= 0)
    )
);

CREATE INDEX ix_service_request_item_version
    ON service.service_request_item (tenant_id, service_request_version_id, line_no);
SELECT platform.attach_touch_row('service.service_request_item');

CREATE OR REPLACE FUNCTION service.tg_request_item_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    v_version_id uuid := COALESCE(NEW.service_request_version_id, OLD.service_request_version_id);
    v_status     text;
    -- Columns reviewers may change after submission; everything else is frozen.
    mutable_cols text[] := ARRAY['status','approved_quantity','approved_amount',
                                 'decision_reason_code','updated_at','row_version'];
BEGIN
    SELECT status INTO v_status
    FROM service.service_request_version
    WHERE id = v_version_id;

    IF v_status IS DISTINCT FROM 'DRAFT' THEN
        IF TG_OP IN ('INSERT','DELETE') THEN
            RAISE EXCEPTION 'service request version % is not a draft; items cannot be added or removed', v_version_id
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        IF (to_jsonb(NEW) - mutable_cols) <> (to_jsonb(OLD) - mutable_cols) THEN
            RAISE EXCEPTION 'requested values of item % are frozen after submission', OLD.id
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tg_request_item_guard
    BEFORE INSERT OR UPDATE OR DELETE ON service.service_request_item
    FOR EACH ROW EXECUTE FUNCTION service.tg_request_item_guard();

SELECT platform.enable_tenant_rls(t)
FROM unnest(ARRAY[
    'catalog.service_category',
    'catalog.service_definition',
    'service.service_request',
    'service.service_request_version',
    'service.service_request_item'
]::regclass[]) AS t;

SELECT platform.grant_app_schema_usage('catalog');
SELECT platform.grant_app_schema_usage('service');
