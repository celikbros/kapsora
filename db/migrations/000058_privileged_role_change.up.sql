-- MGT-03B: privileged TENANT role changes require a durable, distinct checker.
-- Historical nonoverlapping memberships of one actor remain legal.
ALTER TABLE iam.tenant_membership
    ADD CONSTRAINT uq_tenant_membership_actor_evidence UNIQUE (tenant_id, id, actor_id);

CREATE TABLE iam.role_change_request (
    id                         uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                  uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    operation                  text NOT NULL CHECK (operation IN ('ASSIGN','REVOKE')),
    target_membership_id       uuid NOT NULL,
    target_actor_id            uuid NOT NULL REFERENCES iam.actor(id) ON DELETE RESTRICT,
    maker_membership_id        uuid NOT NULL,
    maker_actor_id             uuid NOT NULL REFERENCES iam.actor(id) ON DELETE RESTRICT,
    role_id                    uuid NOT NULL,
    role_code                  text NOT NULL,
    scope_type                 text NOT NULL CHECK (scope_type = 'TENANT'),
    permission_snapshot        jsonb NOT NULL,
    configuration_hash         bytea NOT NULL CHECK (octet_length(configuration_hash) = 32),
    target_membership_version  bigint NOT NULL CHECK (target_membership_version > 0),
    revoke_grant_id            uuid,
    revoke_valid_period        tstzrange,
    reason_code                text NOT NULL,
    status                     text NOT NULL DEFAULT 'PENDING'
                               CHECK (status IN ('PENDING','APPROVED','REJECTED','CANCELLED')),
    decided_by_membership_id   uuid,
    decided_by_actor_id        uuid REFERENCES iam.actor(id) ON DELETE RESTRICT,
    decided_at                 timestamptz,
    decision_reason_code       text,
    applied_grant_id           uuid,
    applied_membership_version bigint,
    applied_valid_period       tstzrange,
    created_at                 timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at                 timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version                bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
    CONSTRAINT uq_role_change_request_id UNIQUE (tenant_id, id),
    CONSTRAINT fk_role_change_target FOREIGN KEY (tenant_id, target_membership_id, target_actor_id)
        REFERENCES iam.tenant_membership(tenant_id, id, actor_id) ON DELETE RESTRICT,
    CONSTRAINT fk_role_change_maker FOREIGN KEY (tenant_id, maker_membership_id, maker_actor_id)
        REFERENCES iam.tenant_membership(tenant_id, id, actor_id) ON DELETE RESTRICT,
    CONSTRAINT fk_role_change_decider FOREIGN KEY (tenant_id, decided_by_membership_id, decided_by_actor_id)
        REFERENCES iam.tenant_membership(tenant_id, id, actor_id) ON DELETE RESTRICT,
    CONSTRAINT fk_role_change_role FOREIGN KEY (tenant_id, role_id)
        REFERENCES iam.role(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_role_change_revoke_grant FOREIGN KEY (tenant_id, revoke_grant_id)
        REFERENCES iam.access_grant(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_role_change_applied_grant FOREIGN KEY (tenant_id, applied_grant_id)
        REFERENCES iam.access_grant(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_role_change_actors CHECK (maker_actor_id <> target_actor_id),
    CONSTRAINT ck_role_change_operation CHECK (
        (operation = 'ASSIGN' AND revoke_grant_id IS NULL AND revoke_valid_period IS NULL
         AND reason_code IN ('ONBOARDING','DUTY_ASSIGNMENT'))
        OR (operation = 'REVOKE' AND revoke_grant_id IS NOT NULL AND revoke_valid_period IS NOT NULL
            AND NOT isempty(revoke_valid_period)
            AND reason_code IN ('ACCESS_REVIEW','DUTY_ENDED','SECURITY_CONCERN'))),
    CONSTRAINT ck_role_change_terminal_shape CHECK (
        (status = 'PENDING' AND decided_by_membership_id IS NULL AND decided_by_actor_id IS NULL
         AND decided_at IS NULL AND decision_reason_code IS NULL AND applied_grant_id IS NULL
         AND applied_membership_version IS NULL AND applied_valid_period IS NULL)
        OR (status = 'APPROVED' AND decided_by_membership_id IS NOT NULL AND decided_by_actor_id IS NOT NULL
            AND decided_at IS NOT NULL AND decision_reason_code IS NULL AND applied_grant_id IS NOT NULL
            AND applied_membership_version IS NOT NULL
            AND applied_membership_version = target_membership_version + 1
            AND applied_valid_period IS NOT NULL AND NOT isempty(applied_valid_period))
        OR (status = 'REJECTED' AND decided_by_membership_id IS NOT NULL AND decided_by_actor_id IS NOT NULL
            AND decided_at IS NOT NULL AND decision_reason_code IS NOT NULL
            AND decision_reason_code IN ('NOT_JUSTIFIED','INCORRECT_ACCESS','STALE_REQUEST')
            AND applied_grant_id IS NULL AND applied_membership_version IS NULL AND applied_valid_period IS NULL)
        OR (status = 'CANCELLED' AND decided_by_membership_id IS NOT NULL AND decided_by_actor_id IS NOT NULL
            AND decided_at IS NOT NULL AND decision_reason_code IS NOT NULL AND decision_reason_code = 'WITHDRAWN'
            AND applied_grant_id IS NULL AND applied_membership_version IS NULL AND applied_valid_period IS NULL)),
    CONSTRAINT ck_role_change_decider CHECK (
        status = 'PENDING' OR
        (status IN ('APPROVED','REJECTED') AND decided_by_actor_id <> maker_actor_id
         AND decided_by_actor_id <> target_actor_id) OR
        (status = 'CANCELLED' AND decided_by_actor_id = maker_actor_id)),
    CONSTRAINT ck_role_change_applied_range CHECK (
        status <> 'APPROVED' OR
        (operation = 'ASSIGN' AND NOT lower_inf(applied_valid_period)
         AND lower_inc(applied_valid_period) AND upper_inf(applied_valid_period)) OR
        (operation = 'REVOKE' AND applied_grant_id = revoke_grant_id
         AND lower(applied_valid_period) IS NOT DISTINCT FROM lower(revoke_valid_period)
         AND lower_inf(applied_valid_period) = lower_inf(revoke_valid_period)
         AND lower_inc(applied_valid_period) = lower_inc(revoke_valid_period)
         AND NOT upper_inf(applied_valid_period) AND NOT upper_inc(applied_valid_period)))
);

CREATE UNIQUE INDEX uq_role_change_pending_target
    ON iam.role_change_request (tenant_id, target_membership_id) WHERE status = 'PENDING';
CREATE UNIQUE INDEX uq_role_change_approved_assign_grant
    ON iam.role_change_request (tenant_id, applied_grant_id)
    WHERE status = 'APPROVED' AND operation = 'ASSIGN';
CREATE UNIQUE INDEX uq_role_change_approved_revoke_grant
    ON iam.role_change_request (tenant_id, applied_grant_id)
    WHERE status = 'APPROVED' AND operation = 'REVOKE';
CREATE INDEX ix_role_change_request_page
    ON iam.role_change_request (tenant_id, status, created_at DESC, id DESC);
CREATE INDEX ix_role_change_request_target
    ON iam.role_change_request (tenant_id, target_membership_id, created_at DESC, id DESC);

CREATE FUNCTION iam.tg_role_change_request_insert_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    item jsonb;
    code text;
    sensitivity text;
    previous_code text;
    privileged boolean := false;
BEGIN
    IF NEW.status <> 'PENDING' OR jsonb_typeof(NEW.permission_snapshot) <> 'array'
       OR jsonb_array_length(NEW.permission_snapshot) = 0 THEN
        RAISE EXCEPTION 'invalid role change proposal' USING ERRCODE = 'check_violation';
    END IF;
    FOR item IN SELECT value FROM jsonb_array_elements(NEW.permission_snapshot) LOOP
        IF jsonb_typeof(item) <> 'object' OR NOT (item ? 'code' AND item ? 'sensitivity')
           OR (SELECT count(*) FROM jsonb_object_keys(item)) <> 2
           OR jsonb_typeof(item->'code') <> 'string'
           OR jsonb_typeof(item->'sensitivity') <> 'string' THEN
            RAISE EXCEPTION 'invalid role change permission snapshot' USING ERRCODE = 'check_violation';
        END IF;
        code := item->>'code';
        sensitivity := item->>'sensitivity';
        IF code !~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$'
           OR sensitivity NOT IN ('NORMAL','SENSITIVE','PRIVILEGED')
           OR (previous_code IS NOT NULL AND code COLLATE "C" <= previous_code COLLATE "C") THEN
            RAISE EXCEPTION 'invalid role change permission snapshot' USING ERRCODE = 'check_violation';
        END IF;
        privileged := privileged OR sensitivity = 'PRIVILEGED';
        previous_code := code;
    END LOOP;
    IF NOT privileged THEN
        RAISE EXCEPTION 'role change proposal has no privileged permission' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.operation = 'REVOKE' AND NOT EXISTS (
        SELECT 1 FROM iam.access_grant g WHERE g.tenant_id = NEW.tenant_id AND g.id = NEW.revoke_grant_id
          AND g.tenant_membership_id = NEW.target_membership_id AND g.role_id = NEW.role_id
          AND g.scope_type = 'TENANT' AND g.scope_id IS NULL
          AND g.valid_period = NEW.revoke_valid_period) THEN
        RAISE EXCEPTION 'role change revoke evidence mismatches grant' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER tg_role_change_request_insert_guard BEFORE INSERT ON iam.role_change_request
    FOR EACH ROW EXECUTE FUNCTION iam.tg_role_change_request_insert_guard();

-- Alphabetically after tg_touch_row: compare only platform-owned updated_at/version
-- in addition to the one legal PENDING -> terminal transition.
CREATE FUNCTION iam.tg_role_change_request_update_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'role change requests are immutable' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.status <> 'PENDING' OR NEW.status NOT IN ('APPROVED','REJECTED','CANCELLED')
       OR NEW.row_version <> OLD.row_version + 1
       OR NEW.updated_at < OLD.updated_at
       OR (to_jsonb(NEW) - ARRAY['status','decided_by_membership_id','decided_by_actor_id','decided_at',
                                  'decision_reason_code','applied_grant_id','applied_membership_version',
                                  'applied_valid_period','updated_at','row_version'])
          IS DISTINCT FROM
           (to_jsonb(OLD) - ARRAY['status','decided_by_membership_id','decided_by_actor_id','decided_at',
                                  'decision_reason_code','applied_grant_id','applied_membership_version',
                                  'applied_valid_period','updated_at','row_version']) THEN
        RAISE EXCEPTION 'role change request evidence is immutable' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF NEW.decided_by_actor_id IS DISTINCT FROM platform.current_actor_id() THEN
        RAISE EXCEPTION 'role change decider context mismatch' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF NEW.status = 'APPROVED' AND NOT EXISTS (
        SELECT 1 FROM iam.access_grant g WHERE g.tenant_id = NEW.tenant_id AND g.id = NEW.applied_grant_id
          AND g.tenant_membership_id = NEW.target_membership_id AND g.role_id = NEW.role_id
          AND g.scope_type = 'TENANT' AND g.scope_id IS NULL AND g.valid_period = NEW.applied_valid_period) THEN
        RAISE EXCEPTION 'role change applied grant mismatches proposal' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;
SELECT platform.attach_touch_row('iam.role_change_request'::regclass);
CREATE TRIGGER zz_role_change_request_update_guard BEFORE UPDATE OR DELETE ON iam.role_change_request
    FOR EACH ROW EXECUTE FUNCTION iam.tg_role_change_request_update_guard();
SELECT platform.enable_tenant_rls('iam.role_change_request'::regclass);

CREATE TABLE iam.role_change_command_receipt (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id       uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    actor_id        uuid NOT NULL REFERENCES iam.actor(id) ON DELETE RESTRICT,
    command_code    text NOT NULL CHECK (command_code IN ('CREATE','APPROVE','REJECT','CANCEL')),
    key_hash        bytea NOT NULL CHECK (octet_length(key_hash) = 32),
    request_hash    bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    request_id      uuid NOT NULL,
    response_status integer NOT NULL CHECK (response_status BETWEEN 200 AND 201),
    response_etag   text NOT NULL CHECK (response_etag ~ '^"[1-9][0-9]*"$'),
    response_body   bytea NOT NULL CHECK (octet_length(response_body) BETWEEN 1 AND 262144),
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_role_change_command_receipt_id UNIQUE (tenant_id, id),
    CONSTRAINT uq_role_change_command_receipt_key UNIQUE (tenant_id, actor_id, command_code, key_hash),
    CONSTRAINT fk_role_change_command_request FOREIGN KEY (tenant_id, request_id)
        REFERENCES iam.role_change_request(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_role_change_command_receipt_status CHECK (
        (command_code = 'CREATE' AND response_status = 201)
        OR (command_code <> 'CREATE' AND response_status = 200))
);
SELECT platform.make_append_only('iam.role_change_command_receipt'::regclass);
SELECT platform.enable_tenant_rls('iam.role_change_command_receipt'::regclass);
SELECT platform.grant_app_schema_usage('iam');
