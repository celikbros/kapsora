-- 000032: the treatment report — a doctor saying "this person needs this, for this long"
-- (WP-I5-02, v1.2 10.5, 12.4, 11.10).
--
-- One property shapes every column below: **an approved report is never edited**. Claims
-- and authorizations lean on what a medical reviewer approved, so the row a claim points at
-- has to still be the row the reviewer read. A correction is therefore not an update — it
-- is a new version of the same chain, and the version that was decided keeps its decision,
-- its lines and its usages exactly as they were.
--
-- The chain is `root_report_id`: version 1 carries its own id there, every later version
-- carries the same value, and the partial unique index below says the chain has at most one
-- APPROVED version at any moment. That is what makes "which report is in force for this
-- person and this service" a question with one answer, without any reader having to walk
-- the `supersedes_report_id` links to find out.
--
-- Which is also why the status list carries SUPERSEDED, which the work package's own
-- enumeration does not. Something has to happen to version 1 when version 2 is approved:
-- the index forbids two APPROVED versions, and the alternatives — deleting the old row,
-- rewriting it, or refusing the correction — each destroy the thing the package exists to
-- protect. SUPERSEDED is the word WP-I4-01 already uses for exactly this
-- (`service.service_request_version`), and it moves nothing but the status: the reviewer,
-- the moment, the comment, the summary, the lines and the usage rows of the old version are
-- untouched, so "what did the reviewer approve on the fifth" is still answerable.
--
-- `clinical_summary` and `review_comment` are the two free-text clinical columns here. They
-- are classified HEALTH and are served only in the clinical projection — the same projection
-- machinery migration 000031 introduced, decided in one function in the application service.
-- Neither ever reaches a notification variable, an audit detail, a work-item title or a log
-- line; the work item a submission raises carries the report's reference and nothing else.

-- ---------------------------------------------------------------------------
-- health.medical_report
-- ---------------------------------------------------------------------------
--
-- `reference` names the chain rather than the version: every version of one report shares
-- it, and (reference, version_no) is what identifies a version. A member quoting "MR-2026…"
-- over the telephone is quoting the report, and asking them for a version number as well
-- would be asking them to know something the product has never shown them.
--
-- `report_type` and `report_subtype` are code system values, held the way
-- `encounter.branch_code` is: a text code with a shape CHECK, governed by a code system
-- WP-I5-05 seeds. A composite foreign key into `catalog.code_value` would tie a report to
-- one tenant's copy of a national code list, and the list is not the tenant's.
--
-- They are clinical. "Rapor türü: ONKOLOJI_TEDAVI" is a diagnosis anybody can read off a
-- list, so both live on the clinical side of the projection with the summary.
CREATE TABLE health.medical_report (
    id                               uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                        uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    person_id                        uuid NOT NULL,
    -- NULL for a report written outside an episode of care the platform holds: a report a
    -- member brings from a hospital the tenant has no case with is still a report.
    case_id                          uuid,
    reference                        text NOT NULL,
    version_no                       integer NOT NULL DEFAULT 1 CHECK (version_no > 0),
    -- The chain. Version 1 points at itself, which is what lets a single index rather than
    -- a recursive query answer "does this chain already have an approved version".
    root_report_id                   uuid NOT NULL,
    supersedes_report_id             uuid,
    report_type                      text NOT NULL,
    report_subtype                   text,
    issuing_practitioner_id          uuid,
    issuing_provider_organization_id uuid,
    issued_at                        date NOT NULL,
    valid_from                       date NOT NULL,
    valid_to                         date NOT NULL,
    status                           text NOT NULL DEFAULT 'DRAFT'
                                     CHECK (status IN ('DRAFT','SUBMITTED','UNDER_REVIEW',
                                                       'APPROVED','REJECTED','CANCELLED',
                                                       'EXPIRED','SUPERSEDED')),
    clinical_summary                 text,
    review_comment                   text,
    reject_reason_code               text,
    reviewed_by                      uuid REFERENCES iam.actor(id),
    reviewed_at                      timestamptz,
    submitted_at                     timestamptz,
    submitted_by                     uuid REFERENCES iam.actor(id),
    created_at                       timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by                       uuid,
    updated_at                       timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by                       uuid,
    row_version                      bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_medical_report_id_tenant UNIQUE (tenant_id, id),
    -- One version number per reference. Two rows claiming to be version 2 of the same
    -- report is a chain nobody can read in order.
    CONSTRAINT uq_medical_report_reference_version UNIQUE (tenant_id, reference, version_no),
    CONSTRAINT fk_medical_report_person FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_medical_report_case FOREIGN KEY (tenant_id, case_id)
        REFERENCES health.health_case(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_medical_report_practitioner FOREIGN KEY (tenant_id, issuing_practitioner_id)
        REFERENCES provider.practitioner(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_medical_report_provider FOREIGN KEY (tenant_id, issuing_provider_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    -- Both self composite foreign keys, so a version can neither supersede nor be rooted in
    -- another tenant's report. A version 1 satisfies the root key against its own row,
    -- which PostgreSQL checks after the row exists.
    CONSTRAINT fk_medical_report_root FOREIGN KEY (tenant_id, root_report_id)
        REFERENCES health.medical_report(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_medical_report_supersedes FOREIGN KEY (tenant_id, supersedes_report_id)
        REFERENCES health.medical_report(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_medical_report_reference CHECK (reference ~ '^MR-[0-9]{8}-[A-Z2-7]{8}$'),
    CONSTRAINT ck_medical_report_type CHECK (report_type ~ '^[A-Z][A-Z0-9_.-]{0,63}$'),
    CONSTRAINT ck_medical_report_subtype
        CHECK (report_subtype IS NULL OR report_subtype ~ '^[A-Z][A-Z0-9_.-]{0,63}$'),
    CONSTRAINT ck_medical_report_period CHECK (valid_to >= valid_from),
    CONSTRAINT ck_medical_report_summary
        CHECK (clinical_summary IS NULL OR length(clinical_summary) <= 4000),
    CONSTRAINT ck_medical_report_review_comment
        CHECK (review_comment IS NULL OR length(review_comment) <= 4000),
    CONSTRAINT ck_medical_report_reject_reason
        CHECK (reject_reason_code IS NULL OR reject_reason_code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    -- Version 1 supersedes nothing and is its own root; every later version does both.
    -- Half of either pair is a chain that cannot be walked.
    CONSTRAINT ck_medical_report_chain
        CHECK ((version_no = 1) = (supersedes_report_id IS NULL)),
    CONSTRAINT ck_medical_report_root_self
        CHECK (version_no > 1 OR root_report_id = id),
    -- A decided report says who decided it and when. "It was approved" with neither is a
    -- row a claim can lean on and nobody can answer for.
    CONSTRAINT ck_medical_report_decided CHECK (
        status NOT IN ('APPROVED','REJECTED','SUPERSEDED')
        OR (reviewed_at IS NOT NULL AND reviewed_by IS NOT NULL)
    ),
    -- A rejection says why, in a code. A rejection nobody can count is a rejection nobody
    -- can improve on.
    CONSTRAINT ck_medical_report_rejection
        CHECK (status <> 'REJECTED' OR reject_reason_code IS NOT NULL),
    -- Everything past the draft has been handed over, so it knows when.
    CONSTRAINT ck_medical_report_submitted
        CHECK (status IN ('DRAFT','CANCELLED') OR submitted_at IS NOT NULL)
);

-- The whole version rule, as one index: a chain has at most one APPROVED version. Approving
-- version 2 supersedes version 1 inside the same transaction, so the moment between the two
-- never exists for any reader.
CREATE UNIQUE INDEX uq_medical_report_chain_approved
    ON health.medical_report (tenant_id, root_report_id)
 WHERE status = 'APPROVED';
-- The chain in order, for the version list a screen shows beside a report.
CREATE INDEX ix_medical_report_chain
    ON health.medical_report (tenant_id, root_report_id, version_no);
CREATE INDEX ix_medical_report_person
    ON health.medical_report (tenant_id, person_id, issued_at DESC);
CREATE INDEX ix_medical_report_provider
    ON health.medical_report (tenant_id, issuing_provider_organization_id, status);
-- The keyset the list endpoint pages by.
CREATE INDEX ix_medical_report_created
    ON health.medical_report (tenant_id, created_at DESC, id DESC);
-- The expiry sweep: approved reports whose validity has run out, oldest first. Partial, so
-- a tenant whose reports are all current costs the job one empty scan, and so that a second
-- pass of the job finds nothing the first one finished.
CREATE INDEX ix_medical_report_expiry
    ON health.medical_report (tenant_id, valid_to)
 WHERE status = 'APPROVED';
SELECT platform.attach_touch_row('health.medical_report'::regclass);
SELECT platform.enable_tenant_rls('health.medical_report'::regclass);

-- ---------------------------------------------------------------------------
-- health.medical_report_service
-- ---------------------------------------------------------------------------
--
-- What the report says the person needs, and how much of it. `covered_quantity` and
-- `covered_amount` are numeric(20,6) for the same reason every quantity in this schema is:
-- a limit that depended on binary rounding would be a limit two systems disagree about.
--
-- Both are optional and either may stand alone: a report may say "twenty sessions" without
-- naming a lira figure, or "up to 15.000 TRY" without counting sessions. A row with neither
-- is still meaningful — it says the service is covered by this report at all, which is the
-- question `ReportCoverage` actually asks.
--
-- `notes` is clinical. It is the line a doctor writes about *this* service for *this*
-- person, and "sol dizde artroskopi sonrası" is a diagnosis in a sentence, so it is served
-- only in the clinical projection like the summary above it.
CREATE TABLE health.medical_report_service (
    id                    uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id             uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    report_id             uuid NOT NULL,
    service_definition_id uuid NOT NULL,
    covered_quantity      numeric(20,6),
    covered_amount        numeric(20,6),
    currency_code         text,
    notes                 text,
    created_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by            uuid,
    updated_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by            uuid,
    row_version           bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_medical_report_service_id_tenant UNIQUE (tenant_id, id),
    -- One line per service. Two lines for one service would make "how much of this is
    -- covered" a question with two answers, and the claim would have to guess.
    CONSTRAINT uq_medical_report_service_line
        UNIQUE (tenant_id, report_id, service_definition_id),
    CONSTRAINT fk_medical_report_service_report FOREIGN KEY (tenant_id, report_id)
        REFERENCES health.medical_report(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_medical_report_service_definition FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_medical_report_service_quantity
        CHECK (covered_quantity IS NULL OR covered_quantity > 0),
    CONSTRAINT ck_medical_report_service_amount
        CHECK (covered_amount IS NULL OR covered_amount > 0),
    CONSTRAINT ck_medical_report_service_currency
        CHECK (currency_code IS NULL OR currency_code ~ '^[A-Z]{3}$'),
    -- An amount without a currency is a number, not money.
    CONSTRAINT ck_medical_report_service_money
        CHECK (covered_amount IS NULL OR currency_code IS NOT NULL),
    CONSTRAINT ck_medical_report_service_notes
        CHECK (notes IS NULL OR length(notes) <= 1000)
);

CREATE INDEX ix_medical_report_service_report
    ON health.medical_report_service (tenant_id, report_id, service_definition_id);
SELECT platform.attach_touch_row('health.medical_report_service'::regclass);
SELECT platform.enable_tenant_rls('health.medical_report_service'::regclass);

-- ---------------------------------------------------------------------------
-- health.medical_report_usage
-- ---------------------------------------------------------------------------
--
-- The trace v1.2 10.5 step 6 asks for: which request, authorization or claim leaned on this
-- report version. It is the answer to "prove the claim you paid was covered by a report a
-- doctor signed", and it names the version rather than the chain — a claim settled against
-- version 1 was settled against version 1 whatever version 3 later says.
--
-- Append-only, because a usage that could be edited afterwards is not evidence of anything.
-- The rows of a superseded version are deliberately left where they are: they record what
-- happened, and what happened does not move.
--
-- `used_by_id` carries no foreign key, for the same reason `document.link.aggregate_id`
-- does not: the three things that may use a report live in three schemas, and a column per
-- one of them would mean a migration every time a fourth appears.
CREATE TABLE health.medical_report_usage (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id    uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    report_id    uuid NOT NULL,
    used_by_type text NOT NULL
                 CHECK (used_by_type IN ('SERVICE_REQUEST','AUTHORIZATION','CLAIM')),
    used_by_id   uuid NOT NULL,
    -- `used_at` is this row's own created_at: a usage is written once, at the moment the
    -- claim leaned on the report, and a second timestamp saying the same thing would be a
    -- second thing to keep in step.
    used_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by   uuid,
    CONSTRAINT uq_medical_report_usage_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_medical_report_usage_report FOREIGN KEY (tenant_id, report_id)
        REFERENCES health.medical_report(tenant_id, id) ON DELETE RESTRICT
);

CREATE INDEX ix_medical_report_usage_report
    ON health.medical_report_usage (tenant_id, report_id, used_at DESC, id DESC);
CREATE INDEX ix_medical_report_usage_subject
    ON health.medical_report_usage (tenant_id, used_by_type, used_by_id);
SELECT platform.make_append_only('health.medical_report_usage'::regclass);
SELECT platform.enable_tenant_rls('health.medical_report_usage'::regclass);

SELECT platform.grant_app_schema_usage('health');
