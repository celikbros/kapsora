-- A discharged inpatient stay is an explicit claim source with its own exact case link.
ALTER TABLE claim.claim DROP CONSTRAINT ck_claim_source_type;
ALTER TABLE claim.claim ADD CONSTRAINT ck_claim_source_type CHECK (
    source_type IS NULL OR source_type IN
    ('HEALTH_CASE', 'INPATIENT_STAY', 'BOOKING', 'REIMBURSEMENT')
);
ALTER TABLE claim.claim DROP CONSTRAINT ck_claim_case_matches_source;
ALTER TABLE claim.claim ADD CONSTRAINT ck_claim_case_matches_source CHECK (
    case_id IS NULL OR source_type = 'INPATIENT_STAY'
    OR (source_type = 'HEALTH_CASE' AND source_id = case_id)
);
CREATE OR REPLACE FUNCTION claim.tg_claim_source_exists() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    found boolean;
BEGIN
    IF NEW.source_type IS NULL THEN RETURN NEW; END IF;
    CASE NEW.source_type
        WHEN 'HEALTH_CASE' THEN
            SELECT EXISTS (SELECT 1 FROM health.health_case c
                WHERE c.tenant_id = NEW.tenant_id AND c.id = NEW.source_id) INTO found;
        WHEN 'INPATIENT_STAY' THEN
            SELECT EXISTS (SELECT 1 FROM health.inpatient_stay s
                WHERE s.tenant_id = NEW.tenant_id AND s.id = NEW.source_id
                  AND s.case_id = NEW.case_id
                  AND s.provider_organization_id = NEW.provider_organization_id
                  AND s.authorization_id = NEW.authorization_id) INTO found;
        WHEN 'BOOKING' THEN
            SELECT EXISTS (SELECT 1 FROM accommodation.booking b
                WHERE b.tenant_id = NEW.tenant_id AND b.id = NEW.source_id) INTO found;
        WHEN 'REIMBURSEMENT' THEN
            SELECT EXISTS (SELECT 1 FROM service.service_request r
                WHERE r.tenant_id = NEW.tenant_id AND r.id = NEW.source_id
                  AND r.request_type = 'REIMBURSEMENT') INTO found;
        ELSE found := false;
    END CASE;
    IF NOT found THEN
        RAISE EXCEPTION 'claim source % % not found in tenant %',
            NEW.source_type, NEW.source_id, NEW.tenant_id
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    RETURN NEW;
END
$$;
DROP TRIGGER tg_claim_source_exists ON claim.claim;
CREATE TRIGGER tg_claim_source_exists
    BEFORE INSERT OR UPDATE OF source_type, source_id, tenant_id, case_id,
                               provider_organization_id, authorization_id
    ON claim.claim FOR EACH ROW EXECUTE FUNCTION claim.tg_claim_source_exists();

CREATE UNIQUE INDEX uq_claim_live_inpatient_stay
    ON claim.claim (tenant_id, source_id)
    WHERE source_type = 'INPATIENT_STAY' AND status <> 'CANCELLED';

-- A submitted claim records each authorization draw separately from its billed service units.
CREATE TABLE claim.claim_line_authorization_allocation (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    version_id uuid NOT NULL,
    line_id uuid NOT NULL,
    authorization_id uuid NOT NULL,
    allocation_order integer NOT NULL CHECK (allocation_order > 0),
    planned_quantity numeric(20,6) NOT NULL CHECK (planned_quantity > 0),
    applied_quantity numeric(20,6) NOT NULL CHECK (applied_quantity >= 0 AND applied_quantity <= planned_quantity
        AND (applied_quantity = 0 OR applied_quantity = planned_quantity)),
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_claim_allocation_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_claim_allocation_version FOREIGN KEY (tenant_id, version_id)
        REFERENCES claim.claim_version(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_claim_allocation_line FOREIGN KEY (tenant_id, line_id)
        REFERENCES claim.claim_line(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_claim_allocation_authorization FOREIGN KEY (tenant_id, authorization_id)
        REFERENCES service.authorization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT uq_claim_allocation_line_auth UNIQUE (tenant_id, line_id, authorization_id),
    CONSTRAINT uq_claim_allocation_line_order UNIQUE (tenant_id, line_id, allocation_order),
    CONSTRAINT uq_claim_allocation_key UNIQUE (tenant_id, idempotency_key),
    CONSTRAINT ck_claim_allocation_key CHECK (
        idempotency_key = 'claim-line:' || line_id::text || ':authorization:' || authorization_id::text)
);

CREATE INDEX ix_claim_allocation_version
    ON claim.claim_line_authorization_allocation (tenant_id, version_id, line_id, allocation_order);
CREATE INDEX ix_claim_allocation_authorization
    ON claim.claim_line_authorization_allocation (tenant_id, authorization_id);

CREATE FUNCTION claim.guard_claim_allocation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'claim allocations are immutable';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM claim.claim_line l
        JOIN claim.claim_version v ON v.tenant_id = l.tenant_id AND v.id = l.version_id
        WHERE l.tenant_id = NEW.tenant_id AND l.id = NEW.line_id
          AND v.id = NEW.version_id AND v.status = 'DRAFT'
    ) THEN
        RAISE EXCEPTION 'claim allocation requires a line in a draft version';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER guard_claim_allocation BEFORE INSERT OR UPDATE OR DELETE
    ON claim.claim_line_authorization_allocation FOR EACH ROW
    EXECUTE FUNCTION claim.guard_claim_allocation();

SELECT platform.enable_tenant_rls('claim.claim_line_authorization_allocation'::regclass);
SELECT platform.grant_app_schema_usage('claim');
