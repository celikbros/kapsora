-- 000017: immutable eligibility evaluation snapshots (WP-I2-04).
-- Every /eligibility/checks call stores what it saw and what it answered, without any
-- identifier: ids, dates, quantities, versions and explanation codes only.

CREATE TABLE benefit.eligibility_evaluation (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    person_id           uuid NOT NULL,
    program_id          uuid,
    enrollment_id       uuid,
    plan_version_id     uuid,
    provider_tenant_organization_id uuid,
    service_date        date NOT NULL,
    outcome             text NOT NULL
                        CHECK (outcome IN ('ELIGIBLE','PARTIALLY_ELIGIBLE','INELIGIBLE','REVIEW_REQUIRED','MISSING_DATA')),
    request_hash        bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    request_snapshot    jsonb NOT NULL,
    result_snapshot     jsonb NOT NULL,
    data_classification text NOT NULL DEFAULT 'PERSONAL' CHECK (data_classification IN ('PERSONAL','HEALTH')),
    idempotency_key     text,
    evaluated_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    evaluated_by        uuid REFERENCES iam.actor(id),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_eligibility_evaluation_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_evaluation_person FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_evaluation_program FOREIGN KEY (tenant_id, program_id)
        REFERENCES benefit.program(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_evaluation_enrollment FOREIGN KEY (tenant_id, enrollment_id)
        REFERENCES benefit.enrollment(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_evaluation_plan_version FOREIGN KEY (tenant_id, plan_version_id)
        REFERENCES benefit.plan_version(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_evaluation_provider FOREIGN KEY (tenant_id, provider_tenant_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT
);
CREATE INDEX ix_eligibility_evaluation_person
    ON benefit.eligibility_evaluation (tenant_id, person_id, service_date DESC, evaluated_at DESC);
CREATE UNIQUE INDEX uq_eligibility_evaluation_key
    ON benefit.eligibility_evaluation (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
SELECT platform.make_append_only('benefit.eligibility_evaluation');
SELECT platform.enable_tenant_rls('benefit.eligibility_evaluation'::regclass);
SELECT platform.grant_app_schema_usage('benefit');
