-- 000025: the service request lifecycle (WP-I4-01, v1.2 9.11, 11.8, 11.9).
--
-- The three request tables have existed since 000006 and were never used. This migration
-- gives them what the lifecycle needs: the record of which eligibility evaluation and
-- which rule versions decided a submit, the reasons a request was returned or rejected,
-- and the two aggregates that hang off a decision — a cancellation and an appeal.
--
-- There is no status column added anywhere, and no status column is made writable. Every
-- move through this lifecycle is a command with its own precondition and reason
-- (v1.2 11.8); the columns below record why, never what.

ALTER TABLE service.service_request
    -- What decided the submit. Both are nullable: a draft has decided nothing yet.
    ADD COLUMN eligibility_evaluation_id uuid,
    ADD COLUMN rule_evaluation_id        uuid,
    -- The document types a rule asked for, in the order the rule named them. Empty is not
    -- null: an empty array means "asked, nothing required", null means "not yet asked".
    ADD COLUMN required_document_types   text[],
    ADD COLUMN return_reason_code        text,
    ADD COLUMN reject_reason_code        text,
    ADD COLUMN review_comment            text;

ALTER TABLE service.service_request
    ADD CONSTRAINT fk_service_request_eligibility
        FOREIGN KEY (tenant_id, eligibility_evaluation_id)
        REFERENCES benefit.eligibility_evaluation(tenant_id, id) ON DELETE RESTRICT,
    ADD CONSTRAINT fk_service_request_rule_evaluation
        FOREIGN KEY (tenant_id, rule_evaluation_id)
        REFERENCES rules.evaluation(tenant_id, id) ON DELETE RESTRICT,
    -- A returned request says why it was returned; a rejected one says why it was
    -- rejected. Neither reason is optional, because "it came back" without a reason is
    -- what makes a member give up rather than correct.
    ADD CONSTRAINT ck_service_request_reject_reason
        CHECK (status <> 'REJECTED' OR reject_reason_code IS NOT NULL),
    ADD CONSTRAINT ck_service_request_document_types
        CHECK (status <> 'PENDING_DOCUMENT'
               OR (required_document_types IS NOT NULL AND array_length(required_document_types, 1) > 0));

-- A returned request is corrected in a new version, so the reason belongs to the version
-- that was sent back rather than to the request as a whole.
ALTER TABLE service.service_request_version
    ADD COLUMN returned_at        timestamptz,
    ADD COLUMN returned_by        uuid REFERENCES iam.actor(id),
    ADD COLUMN return_reason_code text,
    ADD COLUMN return_reason_text text,
    ADD CONSTRAINT ck_service_request_version_return
        CHECK (returned_at IS NULL OR return_reason_code IS NOT NULL);

-- Cancelling something that was promised has consequences: a fee may be due and a
-- reservation may be released. The policy that decided both is snapshot here, because the
-- contract it came from may be retired long before anyone asks why the fee was that.
CREATE TABLE service.cancellation (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    aggregate_type      text NOT NULL CHECK (aggregate_type IN ('SERVICE_REQUEST','AUTHORIZATION','BOOKING')),
    aggregate_id        uuid NOT NULL,
    aggregate_version   integer NOT NULL DEFAULT 1 CHECK (aggregate_version > 0),
    policy_snapshot     jsonb NOT NULL DEFAULT '{}'::jsonb,
    fee_amount          numeric(20,6) NOT NULL DEFAULT 0 CHECK (fee_amount >= 0),
    released_amount     numeric(20,6) NOT NULL DEFAULT 0 CHECK (released_amount >= 0),
    currency_code       char(3) CHECK (currency_code IS NULL OR currency_code ~ '^[A-Z]{3}$'),
    reason_code         text NOT NULL,
    reason_text         text,
    cancelled_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    cancelled_by        uuid REFERENCES iam.actor(id),
    CONSTRAINT uq_cancellation_id_tenant UNIQUE (tenant_id, id),
    -- One cancellation per version of a thing: cancelling twice is the same cancellation,
    -- not a second one, and an idempotent retry must not produce two fees.
    CONSTRAINT uq_cancellation_aggregate UNIQUE (tenant_id, aggregate_type, aggregate_id, aggregate_version),
    CONSTRAINT ck_cancellation_policy CHECK (jsonb_typeof(policy_snapshot) = 'object'),
    CONSTRAINT ck_cancellation_fee_currency CHECK (fee_amount = 0 OR currency_code IS NOT NULL)
);
CREATE INDEX ix_cancellation_aggregate
    ON service.cancellation (tenant_id, aggregate_type, aggregate_id);
SELECT platform.make_append_only('service.cancellation'::regclass);
SELECT platform.enable_tenant_rls('service.cancellation'::regclass);

-- An appeal is a member or a provider saying the decision was wrong. It does not reopen
-- the decision it appeals: the original stays exactly as it was decided, and the appeal
-- carries its own outcome.
CREATE TABLE service.appeal (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    service_request_id  uuid NOT NULL,
    appealed_version_no integer NOT NULL CHECK (appealed_version_no > 0),
    appellant_actor_id  uuid REFERENCES iam.actor(id),
    reason_code         text NOT NULL,
    reason_text         text,
    status              text NOT NULL DEFAULT 'OPEN'
                        CHECK (status IN ('OPEN','UPHELD','OVERTURNED','WITHDRAWN')),
    outcome_reason_code text,
    due_at              timestamptz,
    decided_at          timestamptz,
    decided_by          uuid REFERENCES iam.actor(id),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_appeal_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_appeal_request FOREIGN KEY (tenant_id, service_request_id)
        REFERENCES service.service_request(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_appeal_decided CHECK (
        (status = 'OPEN' AND decided_at IS NULL)
        OR (status <> 'OPEN' AND decided_at IS NOT NULL AND outcome_reason_code IS NOT NULL)
    )
);
-- One open appeal per request: a second one is the same complaint, and two open appeals
-- would give the same decision two answers.
CREATE UNIQUE INDEX uq_appeal_open_per_request
    ON service.appeal (tenant_id, service_request_id)
    WHERE status = 'OPEN';
CREATE INDEX ix_appeal_due ON service.appeal (tenant_id, status, due_at);
SELECT platform.attach_touch_row('service.appeal'::regclass);
SELECT platform.enable_tenant_rls('service.appeal'::regclass);

SELECT platform.grant_app_schema_usage('service');
