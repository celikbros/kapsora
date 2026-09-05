-- 000031: the clinical minimum — a case, its encounters, their diagnoses (WP-I5-01,
-- v1.2 9.12, 10.3, 11.10, 16.7, 16.14).
--
-- KAPSORA is not an EHR. This schema holds exactly what a provision and a claim need, and
-- nothing a hospital record would hold beyond it: there is one free-text clinical column in
-- the whole schema, `encounter.notes_clinical`, and every other clinical fact is a coded
-- reference into `catalog.code_value`.
--
-- The property the package exists for is not written here, because it cannot be: **which
-- half of a row a caller is shown is decided by permission, in the application service,
-- before the row reaches the wire.** What the schema does contribute is the two columns
-- that make the decision possible without reading a diagnosis — `health_case.sensitivity`,
-- maintained on diagnosis write, and `diagnosis.sensitive`, derived from the code value's
-- own category — so that "is this case sensitive" is answerable without opening it, and
-- "which categories are sensitive" stays a property of the code system rather than a list
-- somewhere in Go.
--
-- The sensitivity of a code value lives in `catalog.code_value.attributes` (migration
-- 000019), which is exactly the jsonb the publisher's own categorisation belongs in. No
-- column is added there: WP-I5-05 seeds the ICD-10 chapters this applies to, and a second
-- place to state the same fact is a second place for it to be wrong.

CREATE SCHEMA IF NOT EXISTS health;

-- health.case.read, health.case.manage and health.clinical.read are already in the
-- catalogue (migration 000008). This one is new: a psychiatric, genetic or reproductive
-- health category is not something a clinical reader is thereby entitled to, so it is a
-- grant of its own and it is SENSITIVE.
INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('health.sensitive.read', 'Hassas tanı kategorilerini (psikiyatri, genetik, üreme sağlığı) görme', 'SENSITIVE')
ON CONFLICT (code) DO NOTHING;

-- Why somebody is opening clinical data. It is a closed reference rather than free text
-- because "why" has to be countable: a data protection review asks how many CLAIM_REVIEW
-- reads happened last month, and it cannot ask that of a sentence.
--
-- No tenant_id: the purposes are the same everywhere, and a tenant that could add one
-- could add "OTHER" and make the whole column meaningless.
CREATE TABLE health.clinical_access_purpose (
    purpose_code text PRIMARY KEY,
    display_name text NOT NULL,
    sort_order   integer NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT ck_clinical_access_purpose_code CHECK (purpose_code ~ '^[A-Z][A-Z0-9_]{1,39}$'),
    CONSTRAINT ck_clinical_access_purpose_display CHECK (length(btrim(display_name)) BETWEEN 1 AND 120)
);

INSERT INTO health.clinical_access_purpose (purpose_code, display_name, sort_order) VALUES
    ('TREATMENT',         'Tedavi',              10),
    ('PRE_AUTHORIZATION', 'Ön onay',             20),
    ('CLAIM_REVIEW',      'Hasar incelemesi',    30),
    ('MEDICAL_REVIEW',    'Tıbbi değerlendirme', 40),
    ('AUDIT',             'Denetim',             50),
    ('MEMBER_REQUEST',    'Hak sahibi talebi',   60);

-- One episode of care: why a person is in front of a provider, and for how long.
--
-- `sensitivity` is the column the visibility split turns on. It is maintained from the
-- diagnoses of the case's encounters and is never sent by a caller, because a caller that
-- could set it could clear it. It is itself clinical: a caller without the sensitive grant
-- is not told a case is sensitive, it is simply served the financial projection.
--
-- `service_request_id` is the request the case was opened for and is NULL for a case a
-- provider opened standalone. The composite FK is what keeps the two in one tenant.
CREATE TABLE health.health_case (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    person_id                uuid NOT NULL,
    program_id               uuid NOT NULL,
    enrollment_id            uuid NOT NULL,
    case_type                text NOT NULL
                             CHECK (case_type IN ('OUTPATIENT','INPATIENT','CHRONIC','MATERNITY','OTHER')),
    provider_organization_id uuid,
    opened_at                timestamptz NOT NULL DEFAULT clock_timestamp(),
    closed_at                timestamptz,
    status                   text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN','CLOSED')),
    sensitivity              text NOT NULL DEFAULT 'STANDARD'
                             CHECK (sensitivity IN ('STANDARD','SENSITIVE')),
    service_request_id       uuid,
    created_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by               uuid,
    updated_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by               uuid,
    row_version              bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_health_case_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_health_case_person FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_health_case_program FOREIGN KEY (tenant_id, program_id)
        REFERENCES benefit.program(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_health_case_enrollment FOREIGN KEY (tenant_id, enrollment_id)
        REFERENCES benefit.enrollment(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_health_case_provider FOREIGN KEY (tenant_id, provider_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_health_case_service_request FOREIGN KEY (tenant_id, service_request_id)
        REFERENCES service.service_request(tenant_id, id) ON DELETE RESTRICT,
    -- A closure is one fact with two halves: the status and the moment. Half of it is a
    -- row nobody can answer "when did this end" from.
    CONSTRAINT ck_health_case_closure CHECK (
        (status = 'CLOSED') = (closed_at IS NOT NULL)
    ),
    CONSTRAINT ck_health_case_period CHECK (closed_at IS NULL OR closed_at >= opened_at)
);

CREATE INDEX ix_health_case_person
    ON health.health_case (tenant_id, person_id, opened_at DESC);
CREATE INDEX ix_health_case_provider
    ON health.health_case (tenant_id, provider_organization_id, status);
-- The keyset the list endpoint pages by. The two indexes above serve a filtered list; an
-- unfiltered one would sort the tenant's whole history without this.
CREATE INDEX ix_health_case_opened
    ON health.health_case (tenant_id, opened_at DESC, id DESC);
SELECT platform.attach_touch_row('health.health_case'::regclass);
SELECT platform.enable_tenant_rls('health.health_case'::regclass);

-- One contact inside a case. `notes_clinical` is the only free-text clinical column in the
-- package: it is classified HEALTH and is never returned without health.clinical.read.
-- `branch_code` is the medical branch from a code system and is clinical too — a branch of
-- "onkoloji" is a diagnosis anybody can read off the roster.
CREATE TABLE health.encounter (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id      uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    case_id        uuid NOT NULL,
    encounter_type text NOT NULL
                   CHECK (encounter_type IN ('OUTPATIENT','INPATIENT','EMERGENCY','TELEHEALTH')),
    started_at     timestamptz NOT NULL,
    ended_at       timestamptz,
    location_id    uuid,
    practitioner_id uuid,
    branch_code    text,
    notes_clinical text,
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by     uuid,
    updated_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by     uuid,
    row_version    bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_health_encounter_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_health_encounter_case FOREIGN KEY (tenant_id, case_id)
        REFERENCES health.health_case(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_health_encounter_location FOREIGN KEY (tenant_id, location_id)
        REFERENCES provider.location(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_health_encounter_practitioner FOREIGN KEY (tenant_id, practitioner_id)
        REFERENCES provider.practitioner(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_health_encounter_period CHECK (ended_at IS NULL OR ended_at >= started_at),
    CONSTRAINT ck_health_encounter_branch CHECK (branch_code IS NULL OR branch_code ~ '^[A-Z][A-Z0-9_.-]{0,63}$'),
    CONSTRAINT ck_health_encounter_notes CHECK (notes_clinical IS NULL OR length(notes_clinical) <= 4000)
);

CREATE INDEX ix_health_encounter_case
    ON health.encounter (tenant_id, case_id, started_at);
SELECT platform.attach_touch_row('health.encounter'::regclass);
SELECT platform.enable_tenant_rls('health.encounter'::regclass);

-- What was diagnosed, as a code in a code system. ICD-10 is a code system like any other
-- (seeded by WP-I5-05); nothing here knows the name of a single diagnosis.
--
-- `sensitive` is derived at write time from the code value's own category
-- (`catalog.code_value.attributes->>'sensitive'`) rather than looked up on read. A read
-- that had to join the catalogue to decide whether it may return a row would be a read
-- that returns the row first and hides it second, and the case's own sensitivity would
-- have to be recomputed on every list.
CREATE TABLE health.diagnosis (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id      uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    encounter_id   uuid NOT NULL,
    code_system_id uuid NOT NULL,
    code_value_id  uuid NOT NULL,
    diagnosis_type text NOT NULL
                   CHECK (diagnosis_type IN ('PRIMARY','SECONDARY','SUSPECTED')),
    sensitive      boolean NOT NULL DEFAULT false,
    recorded_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    recorded_by    uuid REFERENCES iam.actor(id),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    row_version    bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_health_diagnosis_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_health_diagnosis_encounter FOREIGN KEY (tenant_id, encounter_id)
        REFERENCES health.encounter(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_health_diagnosis_code_system FOREIGN KEY (tenant_id, code_system_id)
        REFERENCES catalog.code_system(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_health_diagnosis_code_value FOREIGN KEY (tenant_id, code_value_id)
        REFERENCES catalog.code_value(tenant_id, id) ON DELETE RESTRICT
);

-- One primary diagnosis per encounter. Two of them is not a richer record, it is a record
-- nobody can bill or review: every downstream rule asks "what was this for" and expects
-- one answer.
CREATE UNIQUE INDEX uq_health_diagnosis_primary
    ON health.diagnosis (tenant_id, encounter_id)
 WHERE diagnosis_type = 'PRIMARY';
CREATE INDEX ix_health_diagnosis_encounter
    ON health.diagnosis (tenant_id, encounter_id, recorded_at);
SELECT platform.attach_touch_row('health.diagnosis'::regclass);
SELECT platform.enable_tenant_rls('health.diagnosis'::regclass);

SELECT platform.grant_app_schema_usage('health');
