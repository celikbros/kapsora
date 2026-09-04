-- 000022: rule engine (WP-I3-04, v1.2 9.9, 11.7, 16.6; ADR-023 fixes the language as CEL).
-- The decisions that differ per customer live here, versioned. A version reaches review
-- only with a passing test case and is published only by a second person, because these
-- rows decide what a member is owed. Once published a version never changes, and every
-- answer records the version that produced it.

CREATE SCHEMA IF NOT EXISTS rules;

CREATE TABLE rules.rule_set (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    code                text NOT NULL,
    name                text NOT NULL,
    domain_code         text NOT NULL
                        CHECK (domain_code IN (
                            'GENERIC','HEALTH','ACCOMMODATION','ASSISTANCE','EDUCATION',
                            'SPORT','TRANSPORT','CARE','OTHER'
                        )),
    purpose             text NOT NULL
                        CHECK (purpose IN (
                            'ELIGIBILITY','DOCUMENT','PREAUTH','LIMIT','DUPLICATE',
                            'DIAGNOSIS_SERVICE','PRICE','ADJUDICATION'
                        )),
    status              text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','INACTIVE')),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_rule_set_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_rule_set_code UNIQUE (tenant_id, code),
    CONSTRAINT ck_rule_set_code CHECK (code ~ '^[A-Z][A-Z0-9_]{1,39}$')
);
SELECT platform.attach_touch_row('rules.rule_set'::regclass);
SELECT platform.enable_tenant_rls('rules.rule_set'::regclass);

CREATE TABLE rules.rule_set_version (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    rule_set_id         uuid NOT NULL,
    version_no          integer NOT NULL CHECK (version_no > 0),
    status              text NOT NULL DEFAULT 'DRAFT'
                        CHECK (status IN ('DRAFT','UNDER_REVIEW','PUBLISHED','RETIRED')),
    valid_from          date,
    valid_to            date,
    -- Declared CEL variables and their types: {"person": "map", "serviceDate": "string"}.
    -- Compilation uses exactly these, so a rule cannot reach data nobody declared.
    input_schema        jsonb NOT NULL DEFAULT '{}'::jsonb,
    content_hash        text,
    notes               text,
    submitted_at        timestamptz,
    submitted_by        uuid REFERENCES iam.actor(id),
    published_at        timestamptz,
    published_by        uuid REFERENCES iam.actor(id),
    retire_reason_code  text,
    review_comment      text,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version         bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_rule_set_version_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_rule_set_version_no UNIQUE (tenant_id, rule_set_id, version_no),
    CONSTRAINT fk_rule_set_version_set FOREIGN KEY (tenant_id, rule_set_id)
        REFERENCES rules.rule_set(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_rule_set_version_period CHECK (valid_to IS NULL OR valid_from IS NULL OR valid_to > valid_from),
    CONSTRAINT ck_rule_set_version_schema CHECK (jsonb_typeof(input_schema) = 'object'),
    CONSTRAINT ck_rule_set_version_published_complete CHECK (
        status <> 'PUBLISHED'
        OR (valid_from IS NOT NULL AND content_hash IS NOT NULL AND published_by IS NOT NULL)
    ),
    CONSTRAINT ck_rule_set_version_retire_reason CHECK (
        status <> 'RETIRED' OR retire_reason_code IS NOT NULL
    ),
    CONSTRAINT ex_rule_set_version_published_overlap EXCLUDE USING gist (
        tenant_id WITH =,
        rule_set_id WITH =,
        daterange(valid_from, valid_to, '[)') WITH &&
    ) WHERE (status = 'PUBLISHED')
);
CREATE INDEX ix_rule_set_version_lookup
    ON rules.rule_set_version (tenant_id, rule_set_id, status, valid_from);
SELECT platform.attach_touch_row('rules.rule_set_version'::regclass);
SELECT platform.enable_tenant_rls('rules.rule_set_version'::regclass);

CREATE TABLE rules.rule (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    rule_set_version_id     uuid NOT NULL,
    code                    text NOT NULL,
    name                    text NOT NULL,
    priority                integer NOT NULL CHECK (priority > 0),
    -- CEL expression, compiled against the version's input_schema (ADR-023).
    condition               text NOT NULL,
    -- Ordered list of {type, payload}; type is one of the closed action list. The engine
    -- returns these and performs none of them.
    actions                 jsonb NOT NULL DEFAULT '[]'::jsonb,
    explanation_code        text NOT NULL,
    explanation_params      jsonb NOT NULL DEFAULT '{}'::jsonb,
    stop_on_match           boolean NOT NULL DEFAULT false,
    active                  boolean NOT NULL DEFAULT true,
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version             bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_rule_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_rule_code UNIQUE (tenant_id, rule_set_version_id, code),
    -- Evaluation order must be total and reproducible, so two rules may not share a
    -- priority. "Whatever the index returned" is not an order.
    CONSTRAINT uq_rule_priority UNIQUE (tenant_id, rule_set_version_id, priority),
    CONSTRAINT fk_rule_version FOREIGN KEY (tenant_id, rule_set_version_id)
        REFERENCES rules.rule_set_version(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT ck_rule_code CHECK (code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    CONSTRAINT ck_rule_condition CHECK (length(condition) BETWEEN 1 AND 8192),
    CONSTRAINT ck_rule_explanation CHECK (explanation_code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    CONSTRAINT ck_rule_actions CHECK (jsonb_typeof(actions) = 'array'),
    CONSTRAINT ck_rule_params CHECK (jsonb_typeof(explanation_params) = 'object')
);
SELECT platform.attach_touch_row('rules.rule'::regclass);
SELECT platform.enable_tenant_rls('rules.rule'::regclass);

CREATE TABLE rules.rule_test_case (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    rule_set_version_id     uuid NOT NULL,
    code                    text NOT NULL,
    description             text,
    input                   jsonb NOT NULL,
    expected_outcome        text NOT NULL
                            CHECK (expected_outcome IN ('APPROVED','REJECTED','REVIEW_REQUIRED','PARTIALLY_APPROVED')),
    expected_explanations   text[] NOT NULL DEFAULT '{}',
    expected_actions        jsonb,
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version             bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_rule_test_case_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_rule_test_case_code UNIQUE (tenant_id, rule_set_version_id, code),
    CONSTRAINT fk_rule_test_case_version FOREIGN KEY (tenant_id, rule_set_version_id)
        REFERENCES rules.rule_set_version(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT ck_rule_test_case_code CHECK (code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    CONSTRAINT ck_rule_test_case_input CHECK (jsonb_typeof(input) = 'object')
);
SELECT platform.attach_touch_row('rules.rule_test_case'::regclass);
SELECT platform.enable_tenant_rls('rules.rule_test_case'::regclass);

-- What was decided, when, by which version. Append-only: a dispute two years later reads
-- these rows and must see what was true then, not what the table would say today.
CREATE TABLE rules.evaluation (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    subject_type            text NOT NULL,
    subject_id              uuid,
    rule_set_version_id     uuid NOT NULL,
    input_hash              bytea NOT NULL CHECK (octet_length(input_hash) = 32),
    -- Ids, dates and quantities only. Never an identity number, never a name.
    input_snapshot          jsonb NOT NULL,
    outcome                 text NOT NULL
                            CHECK (outcome IN ('APPROVED','REJECTED','REVIEW_REQUIRED','PARTIALLY_APPROVED')),
    duration_ms             integer CHECK (duration_ms IS NULL OR duration_ms >= 0),
    evaluated_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    evaluated_by            uuid REFERENCES iam.actor(id),
    CONSTRAINT uq_rule_evaluation_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_rule_evaluation_version FOREIGN KEY (tenant_id, rule_set_version_id)
        REFERENCES rules.rule_set_version(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_rule_evaluation_subject CHECK (subject_type ~ '^[A-Z][A-Z0-9_]{1,39}$'),
    CONSTRAINT ck_rule_evaluation_snapshot CHECK (jsonb_typeof(input_snapshot) = 'object')
);
CREATE INDEX ix_rule_evaluation_subject
    ON rules.evaluation (tenant_id, subject_type, subject_id, evaluated_at DESC);
SELECT platform.make_append_only('rules.evaluation'::regclass);
SELECT platform.enable_tenant_rls('rules.evaluation'::regclass);

CREATE TABLE rules.evaluation_result (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    evaluation_id       uuid NOT NULL,
    sequence            integer NOT NULL CHECK (sequence > 0),
    rule_id             uuid,
    rule_code           text NOT NULL,
    matched             boolean NOT NULL,
    action_type         text,
    action_payload      jsonb,
    explanation_code    text NOT NULL,
    severity            text NOT NULL CHECK (severity IN ('INFO','WARNING','ERROR')),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT uq_rule_evaluation_result_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_rule_evaluation_result_sequence UNIQUE (tenant_id, evaluation_id, sequence),
    CONSTRAINT fk_rule_evaluation_result_evaluation FOREIGN KEY (tenant_id, evaluation_id)
        REFERENCES rules.evaluation(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_rule_evaluation_result_action CHECK (
        action_type IS NULL OR action_type IN (
            'APPROVE','REJECT','WARN','REQUIRE_DOCUMENT','REQUIRE_PREAUTH',
            'REQUIRE_MEDICAL_REVIEW','REQUIRE_FINANCIAL_REVIEW','PARTIAL_APPROVE',
            'RESERVE_ENTITLEMENT','ADJUST_PRICE','SET_LIMIT'
        )
    )
);
SELECT platform.make_append_only('rules.evaluation_result'::regclass);
SELECT platform.enable_tenant_rls('rules.evaluation_result'::regclass);

SELECT platform.grant_app_schema_usage('rules');
