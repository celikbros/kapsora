-- 000034: the claim — versions, lines, per-line decisions and the adjustments a
-- settlement will read (WP-I5-04, v1.2 9.14, 10.3 steps 5-7, 12.5, 16.8).
--
-- A claim is the provider's statement of what was delivered and what it costs, and the
-- payer's answer to it line by line. Two sentences carry the whole schema, and both of them
-- are constraints here rather than habits in a service:
--
--   * **a submitted version is never edited.** `claim.claim_version` is the unit that
--     freezes: it carries the snapshot of the lines as they were sent, and a correction is
--     version n+1 with the old version left exactly as it was decided. The unique index on
--     (tenant, claim, version_no) is what makes "version 2" name one row, and the partial
--     unique index below is what stops a second draft being opened beside the first.
--   * **every line carries its own decision, with its own reason and its own stage.**
--     `claim.line_decision` is append-only: a line decided twice has two rows and the latest
--     for a version is the decision. A reviewer who cut a line and a reviewer who later
--     restored it are both on the record, which is what makes an appeal answerable.
--
-- The money is exact decimals throughout — numeric(20,6), never a float — and
-- `payer_amount + member_amount = approved_amount` is a CHECK rather than an assertion in
-- Go, because a service that forgets it leaves no trace of having forgotten.
--
-- What is deliberately not here: an invoice, a batch, a settlement. The statuses from
-- INVOICED on are declared on `claim.claim` because the column is the lifecycle and a
-- lifecycle with a hole in it is a lifecycle nobody can read; the commands that reach them
-- are M7's, and nothing in this migration or in WP-I5-04's service can put a claim into one.

CREATE SCHEMA IF NOT EXISTS claim;

-- ---------------------------------------------------------------------------
-- Permissions (the other half lives in internal/identity/application/roles.go)
-- ---------------------------------------------------------------------------
--
-- `claim.read`, `claim.create`, `claim.submit`, `claim.medical.review` and
-- `claim.financial.review` were seeded by migration 000008. Only the withdrawal was
-- missing: a provider that raised a claim by mistake had no way to take it back, and
-- "delete the draft" is not the same act — a cancelled claim is a fact, and a claim that
-- was never there is a record nobody can reconcile against a fulfilment.
--
-- NORMAL rather than SENSITIVE: cancelling one's own claim reveals nothing about anybody,
-- and the lifecycle refuses a cancellation of anything already decided.
INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('claim.cancel', 'Claim iptal etme', 'NORMAL')
ON CONFLICT (code) DO NOTHING;

-- ---------------------------------------------------------------------------
-- claim.claim
-- ---------------------------------------------------------------------------
--
-- The header. It names the person, the plan they were enrolled under and — always — the
-- provider that delivered the service: `provider_organization_id` is NOT NULL because a
-- claim nobody delivered is not a claim, it is a note.
--
-- `domain_code` exists now and is 'HEALTH' for everything WP-I5-04 writes. It is a column
-- rather than a later ALTER because M6 and M7 settle accommodation and assistance through
-- the same table, and a schema that had to be migrated to hold them would be a schema whose
-- claim ids meant different things before and after.
--
-- `review_comment_medical` is HEALTH-classified: it is a doctor's sentence about a patient,
-- and it is served only in the clinical projection. `review_comment_financial` is about
-- money and is served to everybody who may read the claim at all. They are two columns for
-- exactly that reason — one column with a visibility flag would be one column somebody
-- eventually selects without the flag.
CREATE TABLE claim.claim (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    reference                text NOT NULL,
    person_id                uuid NOT NULL,
    program_id               uuid NOT NULL,
    enrollment_id            uuid NOT NULL,
    provider_organization_id uuid NOT NULL,
    domain_code              text NOT NULL DEFAULT 'HEALTH'
                             CHECK (domain_code IN (
                                 'GENERIC','HEALTH','ACCOMMODATION','ASSISTANCE','EDUCATION',
                                 'SPORT','TRANSPORT','CARE','OTHER'
                             )),
    -- The episode of care the claim belongs to, when there is one. NULL is ordinary: a
    -- claim for a single outpatient visit needs no case, and demanding one would make the
    -- case a piece of paperwork rather than a record of an illness.
    case_id                  uuid,
    -- What was delivered against the promise (WP-I4-02), and the promise itself.
    fulfilment_id            uuid,
    authorization_id         uuid,
    current_version_no       integer NOT NULL DEFAULT 1 CHECK (current_version_no > 0),
    status                   text NOT NULL DEFAULT 'DRAFT'
                             CHECK (status IN (
                                 'DRAFT','SUBMITTED','AUTO_ADJUDICATED','PENDING_MEDICAL',
                                 'PENDING_FINANCIAL','RETURNED','PARTIALLY_APPROVED',
                                 'APPROVED','REJECTED','INVOICED','BATCHED','SETTLED',
                                 'CANCELLED'
                             )),
    service_date_from        date NOT NULL,
    service_date_to          date NOT NULL,
    -- The same word list a service request's channel uses (migration 000006): "how did
    -- this reach us" is one question, and two vocabularies for it would be two answers a
    -- report has to reconcile.
    channel                  text NOT NULL DEFAULT 'PROVIDER_PORTAL'
                             CHECK (channel IN ('BACKOFFICE','PROVIDER_PORTAL','MEMBER_PORTAL',
                                                'API','BATCH_IMPORT','CALL_CENTER')),
    reject_reason_code       text,
    return_reason_code       text,
    -- Clinical. Served only in the clinical projection; never a notification variable,
    -- never an audit detail, never a work item title.
    review_comment_medical   text,
    review_comment_financial text,
    closed_at                timestamptz,
    created_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by               uuid,
    updated_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by               uuid,
    row_version              bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_claim_id_tenant UNIQUE (tenant_id, id),
    -- The reference a provider quotes on the telephone. Unique inside the tenant, because
    -- two claims answering to "CLM-2026-0042" is a conversation nobody can have.
    CONSTRAINT uq_claim_reference UNIQUE (tenant_id, reference),
    CONSTRAINT fk_claim_person FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_claim_program FOREIGN KEY (tenant_id, program_id)
        REFERENCES benefit.program(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_claim_enrollment FOREIGN KEY (tenant_id, enrollment_id)
        REFERENCES benefit.enrollment(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_claim_provider FOREIGN KEY (tenant_id, provider_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_claim_case FOREIGN KEY (tenant_id, case_id)
        REFERENCES health.health_case(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_claim_fulfilment FOREIGN KEY (tenant_id, fulfilment_id)
        REFERENCES service.fulfilment(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_claim_authorization FOREIGN KEY (tenant_id, authorization_id)
        REFERENCES service.authorization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_claim_reference CHECK (reference ~ '^[A-Z][A-Z0-9-]{3,63}$'),
    CONSTRAINT ck_claim_period CHECK (service_date_to >= service_date_from),
    -- A rejected claim says why it was rejected and a returned one says why it came back.
    -- Neither reason is optional: "it came back" with no reason is what makes a provider
    -- resubmit the same thing rather than correct it.
    CONSTRAINT ck_claim_reject_reason
        CHECK (status <> 'REJECTED' OR reject_reason_code IS NOT NULL),
    CONSTRAINT ck_claim_return_reason
        CHECK (status <> 'RETURNED' OR return_reason_code IS NOT NULL),
    CONSTRAINT ck_claim_reason_codes CHECK (
        (reject_reason_code IS NULL OR reject_reason_code ~ '^[A-Z][A-Z0-9_.:-]{1,79}$')
        AND (return_reason_code IS NULL OR return_reason_code ~ '^[A-Z][A-Z0-9_.:-]{1,79}$')
    ),
    CONSTRAINT ck_claim_review_comments CHECK (
        (review_comment_medical IS NULL OR length(review_comment_medical) <= 2000)
        AND (review_comment_financial IS NULL OR length(review_comment_financial) <= 2000)
    ),
    -- A claim is closed exactly when it has stopped moving. Both halves are asserted
    -- because both are what a settlement run reads to know there is nothing more to wait
    -- for, and a half-written close is what a redelivered command would leave behind.
    CONSTRAINT ck_claim_closed CHECK (
        (status IN ('REJECTED','CANCELLED','SETTLED')) = (closed_at IS NOT NULL)
    )
);

CREATE INDEX ix_claim_person ON claim.claim (tenant_id, person_id, service_date_from DESC);
CREATE INDEX ix_claim_provider ON claim.claim (tenant_id, provider_organization_id, status);
CREATE INDEX ix_claim_case ON claim.claim (tenant_id, case_id) WHERE case_id IS NOT NULL;
CREATE INDEX ix_claim_authorization
    ON claim.claim (tenant_id, authorization_id) WHERE authorization_id IS NOT NULL;
-- The keyset the list endpoint pages by.
CREATE INDEX ix_claim_created ON claim.claim (tenant_id, created_at DESC, id DESC);
SELECT platform.attach_touch_row('claim.claim'::regclass);
SELECT platform.enable_tenant_rls('claim.claim'::regclass);

-- ---------------------------------------------------------------------------
-- claim.claim_version
-- ---------------------------------------------------------------------------
--
-- The unit that freezes, copied from WP-I4-01's service request version because a
-- correction has to behave the same way on both aggregates: a provider who has learned that
-- a returned request comes back as a new version must not have to learn a different rule
-- for a returned claim.
--
-- `snapshot` is the lines exactly as they were submitted. It exists because the lines
-- themselves belong to the version and could, in principle, be read back — but a snapshot
-- is what survives a schema change, and "what did this provider actually send us in March"
-- is a question a dispute asks years later. Nothing personal goes into it: ids, codes,
-- quantities, amounts and dates only, which is why there is no name and no identifier
-- anywhere in the document the service writes.
CREATE TABLE claim.claim_version (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id          uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    claim_id           uuid NOT NULL,
    version_no         integer NOT NULL CHECK (version_no > 0),
    status             text NOT NULL DEFAULT 'DRAFT'
                       CHECK (status IN ('DRAFT','SUBMITTED','SUPERSEDED')),
    submitted_at       timestamptz,
    submitted_by       uuid REFERENCES iam.actor(id),
    returned_at        timestamptz,
    returned_by        uuid REFERENCES iam.actor(id),
    return_reason_code text,
    return_reason_text text,
    snapshot           jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by         uuid,
    updated_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by         uuid,
    row_version        bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_claim_version_id_tenant UNIQUE (tenant_id, id),
    -- "Version 2 of this claim" has to name one row.
    CONSTRAINT uq_claim_version_no UNIQUE (tenant_id, claim_id, version_no),
    CONSTRAINT fk_claim_version_claim FOREIGN KEY (tenant_id, claim_id)
        REFERENCES claim.claim(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_claim_version_snapshot CHECK (jsonb_typeof(snapshot) = 'object'),
    -- A submitted version says when it was submitted, and a draft has not been.
    CONSTRAINT ck_claim_version_submitted CHECK (
        (status = 'DRAFT') = (submitted_at IS NULL)
    ),
    CONSTRAINT ck_claim_version_returned CHECK (
        returned_at IS NULL OR return_reason_code IS NOT NULL
    ),
    CONSTRAINT ck_claim_version_return_reason CHECK (
        (return_reason_code IS NULL OR return_reason_code ~ '^[A-Z][A-Z0-9_.:-]{1,79}$')
        AND (return_reason_text IS NULL OR length(return_reason_text) <= 1000)
    )
);

-- **One draft per claim.** A second draft beside the first is two people correcting the
-- same claim in two directions, and whichever was submitted second would silently win.
CREATE UNIQUE INDEX uq_claim_version_draft
    ON claim.claim_version (tenant_id, claim_id)
 WHERE status = 'DRAFT';
CREATE INDEX ix_claim_version_claim
    ON claim.claim_version (tenant_id, claim_id, version_no DESC);
SELECT platform.attach_touch_row('claim.claim_version'::regclass);
SELECT platform.enable_tenant_rls('claim.claim_version'::regclass);

-- ---------------------------------------------------------------------------
-- claim.claim_line
-- ---------------------------------------------------------------------------
--
-- One thing that was delivered. The lines hang off the *version*, not the claim, which is
-- the whole of "a submitted version is never edited": correcting a claim writes new lines
-- under a new version and leaves the old ones where the decision that was made about them
-- can still find them.
--
-- Three of its columns are clinical and the projection drops all three:
--
--   * `diagnosis_id` — an id beside a name is an invitation to go and look it up;
--   * `medical_report_id` — that a line leans on a treatment report at all is a fact about
--     the patient;
--   * `description` — the free-text line description v1.2 marks as possibly clinical.
--     "Sol diz artroskopi sonrası kontrol" is a diagnosis in a sentence, and a provider
--     typing a line description is not thinking about who will read it.
CREATE TABLE claim.claim_line (
    id                    uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id             uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    version_id            uuid NOT NULL,
    line_no               integer NOT NULL CHECK (line_no > 0),
    service_definition_id uuid NOT NULL,
    unit_type             text NOT NULL,
    quantity              numeric(20,6) NOT NULL CHECK (quantity > 0),
    -- What the provider says one unit costs. NULL when the provider quoted a total rather
    -- than a rate, which is ordinary on a package line.
    unit_amount           numeric(20,6) CHECK (unit_amount IS NULL OR unit_amount >= 0),
    line_amount           numeric(20,6) NOT NULL CHECK (line_amount >= 0),
    currency_code         char(3) NOT NULL DEFAULT 'TRY' CHECK (currency_code ~ '^[A-Z]{3}$'),
    diagnosis_id          uuid,
    medical_report_id     uuid,
    practitioner_id       uuid,
    description           text,
    created_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by            uuid,
    updated_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by            uuid,
    row_version           bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_claim_line_id_tenant UNIQUE (tenant_id, id),
    -- "Line 2 of this version" has to name one row: every decision below points at a line
    -- by id, and a duplicated line number would make the reviewer's screen ambiguous.
    CONSTRAINT uq_claim_line_no UNIQUE (tenant_id, version_id, line_no),
    CONSTRAINT fk_claim_line_version FOREIGN KEY (tenant_id, version_id)
        REFERENCES claim.claim_version(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_claim_line_service FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_claim_line_diagnosis FOREIGN KEY (tenant_id, diagnosis_id)
        REFERENCES health.diagnosis(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_claim_line_report FOREIGN KEY (tenant_id, medical_report_id)
        REFERENCES health.medical_report(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_claim_line_practitioner FOREIGN KEY (tenant_id, practitioner_id)
        REFERENCES provider.practitioner(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_claim_line_unit_type CHECK (unit_type ~ '^[A-Z][A-Z0-9_]{1,31}$'),
    CONSTRAINT ck_claim_line_description CHECK (description IS NULL OR length(description) <= 1000)
);

CREATE INDEX ix_claim_line_version ON claim.claim_line (tenant_id, version_id, line_no);
CREATE INDEX ix_claim_line_service ON claim.claim_line (tenant_id, service_definition_id);
CREATE INDEX ix_claim_line_report
    ON claim.claim_line (tenant_id, medical_report_id) WHERE medical_report_id IS NOT NULL;
SELECT platform.attach_touch_row('claim.claim_line'::regclass);
SELECT platform.enable_tenant_rls('claim.claim_line'::regclass);

-- ---------------------------------------------------------------------------
-- claim.line_decision
-- ---------------------------------------------------------------------------
--
-- What the payer answered about one line, who answered it and at which stage. It is
-- **append-only**: a line decided twice has two rows and the latest for a version is the
-- decision. That is not a storage detail — it is the record an appeal is answered from. A
-- reviewer who cut a line to eighty per cent and a second reviewer who restored it are two
-- facts, and an UPDATE would leave only the second.
--
-- `decided_by` NULL means the rules decided it, which is why the column is nullable and why
-- `stage = 'AUTO'` exists: "the system decided this and nobody looked" has to be visible on
-- the row rather than inferred from an absence somewhere else.
--
-- `payer_amount + member_amount = approved_amount` is a CHECK. It is the invariant the
-- whole settlement rests on, and the arithmetic that produces it rounds once — so a service
-- that rounded the two halves independently would fail here by a kuruş rather than silently
-- publish an invoice nobody can reconcile.
CREATE TABLE claim.line_decision (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id          uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    line_id            uuid NOT NULL,
    decided_in_version_no integer NOT NULL CHECK (decided_in_version_no > 0),
    decision           text NOT NULL
                       CHECK (decision IN ('APPROVED','PARTIALLY_APPROVED','REJECTED','CUT')),
    approved_quantity  numeric(20,6) NOT NULL DEFAULT 0 CHECK (approved_quantity >= 0),
    approved_amount    numeric(20,6) NOT NULL DEFAULT 0 CHECK (approved_amount >= 0),
    -- What the contract said the line costs, before anything was cut from it. NULL when the
    -- pricing ladder found no contracted price at all, which is itself a reason for review.
    contract_amount    numeric(20,6) CHECK (contract_amount IS NULL OR contract_amount >= 0),
    payer_amount       numeric(20,6) NOT NULL DEFAULT 0 CHECK (payer_amount >= 0),
    member_amount      numeric(20,6) NOT NULL DEFAULT 0 CHECK (member_amount >= 0),
    reason_code        text NOT NULL,
    -- The reviewer's own words. On a MEDICAL decision this is clinical text and the
    -- financial projection drops it; the service never puts it in a notification variable,
    -- an audit detail or a work item title.
    reason_text        text,
    decided_by         uuid REFERENCES iam.actor(id),
    decided_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    stage              text NOT NULL CHECK (stage IN ('AUTO','MEDICAL','FINANCIAL')),
    CONSTRAINT uq_line_decision_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_line_decision_line FOREIGN KEY (tenant_id, line_id)
        REFERENCES claim.claim_line(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_line_decision_reason_code CHECK (reason_code ~ '^[A-Z][A-Z0-9_.:-]{1,79}$'),
    CONSTRAINT ck_line_decision_reason_text
        CHECK (reason_text IS NULL OR length(reason_text) <= 1000),
    -- The invariant the settlement rests on.
    CONSTRAINT ck_line_decision_split CHECK (payer_amount + member_amount = approved_amount),
    -- A rejected line approves nothing. Anything else would be a rejection that still paid.
    CONSTRAINT ck_line_decision_rejected CHECK (
        decision <> 'REJECTED'
        OR (approved_quantity = 0 AND approved_amount = 0)
    ),
    -- The rules decide at AUTO and only at AUTO; a person decides at the other two. Both
    -- halves are asserted because "nobody looked at this" is the claim the AUTO stage makes,
    -- and an actor on an AUTO row would quietly make that claim false.
    CONSTRAINT ck_line_decision_actor CHECK (
        (stage = 'AUTO') = (decided_by IS NULL)
    )
);

-- The latest decision for a line, which is the decision. The order is the one every read
-- uses, so the "latest per line" lookup is an index scan rather than a sort of the history.
CREATE INDEX ix_line_decision_line
    ON claim.line_decision (tenant_id, line_id, decided_at DESC, id DESC);
CREATE INDEX ix_line_decision_stage
    ON claim.line_decision (tenant_id, stage, decided_at DESC);
SELECT platform.make_append_only('claim.line_decision'::regclass);
SELECT platform.enable_tenant_rls('claim.line_decision'::regclass);

-- ---------------------------------------------------------------------------
-- claim.adjustment
-- ---------------------------------------------------------------------------
--
-- Money that moved for a reason that is not a line: a cut applied to the claim as a whole, a
-- recovery of something already paid, a correction of an arithmetic mistake. It is declared
-- here and written by nothing in WP-I5-04 except the cut a review records, because M7's
-- settlement is what reads it — and a settlement that had to be given its own table would be
-- a settlement that could not explain a figure the claim screen had already shown.
--
-- Append-only, for the reason a ledger is: an adjustment reversed is a second adjustment,
-- never the first one edited.
CREATE TABLE claim.adjustment (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id       uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    claim_id        uuid NOT NULL,
    version_no      integer NOT NULL CHECK (version_no > 0),
    adjustment_type text NOT NULL CHECK (adjustment_type IN ('CUT','RECOVERY','CORRECTION')),
    amount          numeric(20,6) NOT NULL,
    currency_code   char(3) NOT NULL DEFAULT 'TRY' CHECK (currency_code ~ '^[A-Z]{3}$'),
    reason_code     text NOT NULL,
    reason_text     text,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by      uuid REFERENCES iam.actor(id),
    CONSTRAINT uq_claim_adjustment_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_claim_adjustment_claim FOREIGN KEY (tenant_id, claim_id)
        REFERENCES claim.claim(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_claim_adjustment_reason CHECK (reason_code ~ '^[A-Z][A-Z0-9_.:-]{1,79}$'),
    CONSTRAINT ck_claim_adjustment_reason_text
        CHECK (reason_text IS NULL OR length(reason_text) <= 1000),
    -- A cut takes money away and a recovery takes money back; a correction may go either
    -- way. Signing them here means a settlement can sum the column without a CASE.
    CONSTRAINT ck_claim_adjustment_sign CHECK (
        adjustment_type = 'CORRECTION' OR amount >= 0
    )
);

CREATE INDEX ix_claim_adjustment_claim
    ON claim.adjustment (tenant_id, claim_id, version_no, created_at);
SELECT platform.make_append_only('claim.adjustment'::regclass);
SELECT platform.enable_tenant_rls('claim.adjustment'::regclass);

-- The grant is over the tables that exist when it runs, so a schema that has just been
-- created with five of them has to ask.
SELECT platform.grant_app_schema_usage('claim');
