-- 000047: what the two sides compared and what differed, and the file a person is allowed to
-- take out of the system (WP-I7-05, v1.2 9.14, 9.18, 16.8, 16.9, Faz 8, plan v2.0 §2.7).
--
-- Two sentences carry this migration, and both are constraints here rather than habits in a
-- service, because a backfill, a scheduler and a psql session all reach these tables and only
-- one of them runs the service:
--
--   * **a reconciliation run is an immutable record of what was compared.** It is append-only
--     through `platform.make_append_only`, its arithmetic is a CHECK rather than an assertion
--     about the job that wrote it, and a run that says BALANCED cannot carry a difference. A
--     run somebody could edit afterwards would be a record of what we wish had been compared.
--   * **an export carries no identifier in its filters.** The ids an export is *scoped* by are
--     typed columns with foreign keys behind them; `parameters` is the free-form remainder, and
--     a uuid or a key called `...Id` in it is refused by the database. That is what makes "the
--     parameters hold no identifier" a fact rather than a promise a mapper keeps.
--
-- And one thing this migration adds to a table it does not own: `billing.settlement` may only
-- be RECONCILED when it is fully paid. WP-I7-04 wrote the PAID and PARTIALLY_PAID halves of
-- "the status follows the sum"; this is the third, and it is here because the reconciliation
-- run is the only thing that writes the status and the only way to be sure of that is to make
-- the wrong write impossible.
--
-- What is deliberately not here: a `report.export_download` table. `audit.access_event` already
-- records who downloaded what and when, `download_count` is the counter a screen reads, and a
-- third record of the same fact is a third thing that can disagree with the other two.

-- ---------------------------------------------------------------------------
-- Permissions
-- ---------------------------------------------------------------------------
--
-- `report.read` was seeded by migration 000008 and is held by everybody who reads a figure:
-- the tenant's administrator, the programme manager, both finance roles and the auditor. The
-- statement, the reconciliation runs and the operations dashboard are all read under it,
-- because they are the same numbers those roles already see one screen at a time.
--
-- Taking those numbers *out* is a second grant. `report.export` is NORMAL: what leaves under it
-- is a list of references, statuses and exact decimals, which is what the person could already
-- read one page at a time -- the export is a convenience, not a new disclosure.
--
-- `report.export.sensitive` is SENSITIVE and guards exactly one kind: CLAIMS, whose rows carry
-- line descriptions. A description is what a member was treated for written in words, and a
-- spreadsheet of ten thousand of them leaving the building is a different act from a reviewer
-- opening one claim. It is a grant of its own rather than a stricter reading of `report.export`
-- so that a tenant can give its finance clerk every other export and not that one -- which is
-- impossible while the two are one permission.
--
-- Neither is PRIVILEGED. PRIVILEGED is for the grants that override a protection (break glass,
-- retention); these two are ordinary work for the roles that hold them, and marking them
-- PRIVILEGED would make the word mean nothing.
--
-- The other half of this insert lives in internal/identity/application/roles.go. A permission in
-- one place and not the other is a permission nobody can hold or one nobody can be given, and
-- db/tests/report_test.go checks both halves agree.
INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('report.export', 'Rapor dışa aktarma (CSV) talebi ve indirme', 'NORMAL'),
    ('report.export.sensitive',
     'Hasar dosyası satır açıklamalarını içeren dışa aktarma', 'SENSITIVE')
ON CONFLICT (code) DO NOTHING;

-- ---------------------------------------------------------------------------
-- billing.reconciliation_run
-- ---------------------------------------------------------------------------
--
-- One comparison, for one scope, one period and one currency: what was invoiced, what the payer
-- approved, what it cut, returned and rejected, what was settled, what was actually paid, what
-- is still open -- and, from M9, what the accounting system says. Every one of those is a sum of
-- exact decimals the database computed; nothing here was added up in Go.
--
-- `erp_total` is nullable and unwritten today. It carries no foreign key and no unit of its own
-- for the reason `billing.settlement.posting_id` does: the system it will come from does not
-- exist yet, and a column added later would mean runs that meant different things before and
-- after. `difference` is defined against it -- settled minus paid while it is null, settled minus
-- the ERP's figure once it is not -- and that definition is the CHECK below rather than an
-- arithmetic the job is trusted to have got right.
--
-- The run is **append-only**. A settlement that was found short in March is found short in March
-- for ever; a run somebody could correct afterwards would be a record of the argument's outcome
-- rather than of the argument.
CREATE TABLE billing.reconciliation_run (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    -- TENANT is the payer's own daily total; PROVIDER is one provider's. Both exist because
    -- they answer different questions: the first is "did yesterday balance", the second is
    -- "which provider is the reason it did not".
    scope                    text NOT NULL CHECK (scope IN ('TENANT', 'PROVIDER')),
    provider_organization_id uuid,
    period_from              date NOT NULL,
    period_to                date NOT NULL,
    -- Which attempt at reconciling this period. A run is never edited: a second run of the same
    -- day is run number two, and both stay, because "we looked again and it balanced" is itself
    -- part of the record.
    run_no                   integer NOT NULL DEFAULT 1 CHECK (run_no > 0),
    currency_code            char(3) NOT NULL DEFAULT 'TRY',
    invoiced_total           numeric(20,6) NOT NULL DEFAULT 0,
    approved_total           numeric(20,6) NOT NULL DEFAULT 0,
    cut_total                numeric(20,6) NOT NULL DEFAULT 0,
    returned_total           numeric(20,6) NOT NULL DEFAULT 0,
    rejected_total           numeric(20,6) NOT NULL DEFAULT 0,
    settled_total            numeric(20,6) NOT NULL DEFAULT 0,
    paid_total               numeric(20,6) NOT NULL DEFAULT 0,
    open_total               numeric(20,6) NOT NULL DEFAULT 0,
    -- M9: what the accounting system says it posted for this period.
    erp_total                numeric(20,6),
    difference               numeric(20,6) NOT NULL DEFAULT 0,
    difference_count         integer NOT NULL DEFAULT 0 CHECK (difference_count >= 0),
    -- Each entry: the settlement's reference, what was expected, what arrived and which kind of
    -- disagreement it is. References rather than ids, because this column is read by a person
    -- looking for the row on their own screen, and it is the only part of a run that is prose.
    differences              jsonb NOT NULL DEFAULT '[]'::jsonb,
    status                   text NOT NULL
                             CHECK (status IN ('BALANCED', 'DIFFERENCES', 'FAILED')),
    -- Why a run could not be completed. It exists so a FAILED run says something; a run that
    -- only said FAILED would send an operator to the logs of a job that ran at two in the
    -- morning three weeks ago.
    failure_code             text,
    ran_at                   timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by               uuid,
    CONSTRAINT uq_billing_reconciliation_run_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_billing_reconciliation_run_provider
        FOREIGN KEY (tenant_id, provider_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_billing_reconciliation_run_currency CHECK (currency_code ~ '^[A-Z]{3}$'),
    CONSTRAINT ck_billing_reconciliation_run_period CHECK (period_from <= period_to),
    -- A PROVIDER run names a provider and a TENANT run names none. A tenant-wide run carrying
    -- one organization would be a total nobody could reproduce.
    CONSTRAINT ck_billing_reconciliation_run_scope CHECK (
        (scope = 'PROVIDER') = (provider_organization_id IS NOT NULL)
    ),
    CONSTRAINT ck_billing_reconciliation_run_signs CHECK (
        invoiced_total >= 0 AND approved_total >= 0 AND cut_total >= 0
        AND returned_total >= 0 AND rejected_total >= 0
        AND settled_total >= 0 AND paid_total >= 0
    ),
    -- **What is open is what was settled and not paid.** Both halves of the reconciliation's
    -- arithmetic are here so that a job which got one of them wrong fails at the constraint
    -- rather than writing a run two people would read differently.
    CONSTRAINT ck_billing_reconciliation_run_open CHECK (
        open_total = settled_total - paid_total
    ),
    CONSTRAINT ck_billing_reconciliation_run_difference CHECK (
        difference = settled_total - COALESCE(erp_total, paid_total)
    ),
    -- **A balanced run carries no difference and a differing run carries at least one.** The
    -- count and the array say the same thing, and they are made to agree here rather than by the
    -- writer, because a run whose count and list disagreed would be a run whose screen and whose
    -- work item disagreed.
    CONSTRAINT ck_billing_reconciliation_run_differences CHECK (
        jsonb_typeof(differences) = 'array'
        AND jsonb_array_length(differences) = difference_count
        AND (
            (status = 'BALANCED' AND difference_count = 0)
            OR (status = 'DIFFERENCES' AND difference_count > 0)
            OR status = 'FAILED'
        )
    ),
    -- A failed run says why, and nothing else carries a failure code.
    CONSTRAINT ck_billing_reconciliation_run_failure CHECK (
        (status = 'FAILED') = (failure_code IS NOT NULL)
    ),
    CONSTRAINT ck_billing_reconciliation_run_failure_code
        CHECK (failure_code IS NULL OR failure_code ~ '^[A-Z][A-Z0-9_.:-]{1,79}$')
);

-- **One run number per scope, period and currency.** The provider column is folded into the key
-- through COALESCE rather than left to NULL's "distinct from everything": two TENANT runs both
-- calling themselves run one of the third of March would be two answers to "what did we find".
CREATE UNIQUE INDEX uq_billing_reconciliation_run_no
    ON billing.reconciliation_run (
        tenant_id, scope,
        COALESCE(provider_organization_id, '00000000-0000-0000-0000-000000000000'::uuid),
        period_from, period_to, currency_code, run_no);

CREATE INDEX ix_billing_reconciliation_run_period
    ON billing.reconciliation_run (tenant_id, period_from DESC, period_to DESC, id DESC);
CREATE INDEX ix_billing_reconciliation_run_status
    ON billing.reconciliation_run (tenant_id, status, ran_at DESC);
-- The keyset the list endpoint pages by.
CREATE INDEX ix_billing_reconciliation_run_created
    ON billing.reconciliation_run (tenant_id, created_at DESC, id DESC);

SELECT platform.enable_tenant_rls('billing.reconciliation_run'::regclass);
-- Append-only, and therefore no touch_row and no row_version: there is no update for either to
-- describe.
SELECT platform.make_append_only('billing.reconciliation_run'::regclass);

-- ---------------------------------------------------------------------------
-- RECONCILED is what the run says, not what a command says
-- ---------------------------------------------------------------------------
--
-- WP-I7-04's `ck_billing_settlement_paid_status` made PAID mean "the whole payable amount
-- arrived" and PARTIALLY_PAID mean "some of it did". This is the third arm of the same rule:
-- RECONCILED is above PAID, so a settlement that is short cannot be marked reconciled by
-- anybody -- not by the run, not by a command, and not by a psql session.
--
-- It is an ALTER on somebody else's table on purpose. The status belongs to the settlement and
-- the meaning of this value belongs to the reconciliation, and the honest place for a rule that
-- spans them is next to the thing that introduced it.
ALTER TABLE billing.settlement
    ADD CONSTRAINT ck_billing_settlement_reconciled
    CHECK (status <> 'RECONCILED' OR paid_amount = payable_amount);

-- ---------------------------------------------------------------------------
-- The report schema
-- ---------------------------------------------------------------------------
CREATE SCHEMA IF NOT EXISTS report;

-- Whether a filter blob is free of identifiers. It is a function rather than an inline CHECK
-- because a CHECK may not contain a subquery, and because the rule is worth being able to point
-- at: an export's `parameters` may say `{"status":"APPROVED","from":"2026-03-01"}` and may never
-- say which provider, which person or which claim.
--
-- Two tests, both over the rendered text so that a nested object cannot hide from either:
--
--   * no uuid anywhere in it. Every identifier in this platform is a uuid, so this one test
--     catches a provider id, a claim id and a person id at once;
--   * no key that names an identifier. `providerId`, `claim_id` and a bare `id` are refused;
--     `valid`, `paid` and `period` are not, which is why the pattern asks for the camel-case
--     capital or the underscore rather than for the two letters.
--
-- IMMUTABLE because it reads nothing but its argument, which is what lets a CHECK call it.
CREATE OR REPLACE FUNCTION report.parameters_are_anonymous(p jsonb) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$
    SELECT jsonb_typeof(p) = 'object'
       AND p::text !~
           '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'
       AND p::text !~ '"([A-Za-z0-9_]*(Id|_id)|id)"[[:space:]]*:';
$$;

-- ---------------------------------------------------------------------------
-- report.export
-- ---------------------------------------------------------------------------
--
-- One request to take numbers out of the system, and everything that has to be true about it
-- afterwards: who asked, what they asked for, what the worker produced, the watermark stamped
-- on every row of it, when it stops being downloadable and how many times it was.
--
-- The file itself is not here. It is a WP-I4-04 document of class `EXPORT`, which means the
-- retention sweep, the legal hold and the scan status all apply to it exactly as they apply to
-- an invoice image -- and it means this table has one nullable `document_id` rather than a
-- second file pipeline of its own.
--
-- The scope columns are typed and the filter blob is not. That split is the whole of the
-- identifier rule: which provider, which period and which currency an export covers are
-- columns with a foreign key and a CHECK behind them, and `parameters` is the remainder a
-- screen sent -- statuses, buckets, a free-text search -- which is refused if it carries an id.
CREATE TABLE report.export (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    kind                     text NOT NULL
                             CHECK (kind IN ('PROVIDER_STATEMENT', 'BATCH', 'SETTLEMENTS',
                                             'CLAIMS', 'RECONCILIATION')),
    -- The filters as the caller gave them, minus every identifier. Read by the worker to
    -- reproduce the list, and by a screen to say what a finished file contains.
    parameters               jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- XLSX is in the word list because the column would otherwise have to change when a
    -- spreadsheet writer lands; nothing produces one today and `createExport` refuses it, which
    -- is what the contract says as well.
    format                   text NOT NULL DEFAULT 'CSV' CHECK (format IN ('CSV', 'XLSX')),
    status                   text NOT NULL DEFAULT 'QUEUED'
                             CHECK (status IN ('QUEUED', 'RUNNING', 'READY', 'FAILED',
                                               'EXPIRED')),
    -- The scope, as columns. An export of one provider's statement names that provider here and
    -- nowhere else.
    provider_organization_id uuid,
    period_from              date,
    period_to                date,
    currency_code            char(3),
    document_id              uuid,
    row_count                integer NOT NULL DEFAULT 0 CHECK (row_count >= 0),
    requested_by             uuid NOT NULL REFERENCES iam.actor(id),
    requested_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    -- The tenant's `report.export_ttl_hours`, applied when the export was requested. It is a
    -- stored moment rather than a duration read at download time for the reason a settlement's
    -- approved amount is a copy: the answer to "may I still open this" must not change because
    -- somebody edited a setting this afternoon.
    expires_at               timestamptz NOT NULL,
    -- The requester, the tenant, the moment and this export's id, in one line. It is stored
    -- rather than rebuilt at render time so that the string on the file and the string on the
    -- screen are the same string, and so that a file found on somebody's laptop can be traced
    -- back to the row that produced it.
    watermark                text NOT NULL,
    download_count           integer NOT NULL DEFAULT 0 CHECK (download_count >= 0),
    failure_code             text,
    created_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by               uuid,
    updated_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by               uuid,
    row_version              bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_report_export_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_report_export_provider FOREIGN KEY (tenant_id, provider_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_report_export_document FOREIGN KEY (tenant_id, document_id)
        REFERENCES document.object(tenant_id, id) ON DELETE RESTRICT,
    -- **The parameters hold no identifier.**
    CONSTRAINT ck_report_export_parameters
        CHECK (report.parameters_are_anonymous(parameters)),
    CONSTRAINT ck_report_export_currency
        CHECK (currency_code IS NULL OR currency_code ~ '^[A-Z]{3}$'),
    CONSTRAINT ck_report_export_period CHECK (
        (period_from IS NULL) = (period_to IS NULL)
        AND (period_from IS NULL OR period_from <= period_to)
    ),
    -- A statement is one provider's over one period. The other kinds may name a provider and
    -- may not; this one has no meaning without one.
    CONSTRAINT ck_report_export_statement_scope CHECK (
        kind <> 'PROVIDER_STATEMENT'
        OR (provider_organization_id IS NOT NULL AND period_from IS NOT NULL)
    ),
    -- A READY export has a file; only a READY or an expired one may name a document. A QUEUED
    -- row pointing at a document would be a file nobody produced.
    CONSTRAINT ck_report_export_document CHECK (
        (status <> 'READY' OR document_id IS NOT NULL)
        AND (document_id IS NULL OR status IN ('READY', 'EXPIRED'))
    ),
    -- A failed export says why, and nothing else carries a failure code.
    CONSTRAINT ck_report_export_failure CHECK (
        (status = 'FAILED') = (failure_code IS NOT NULL)
    ),
    CONSTRAINT ck_report_export_failure_code
        CHECK (failure_code IS NULL OR failure_code ~ '^[A-Z][A-Z0-9_.:-]{1,79}$'),
    -- An export that expired before it was asked for would be an export nobody could ever open.
    CONSTRAINT ck_report_export_expiry CHECK (expires_at > requested_at),
    CONSTRAINT ck_report_export_watermark
        CHECK (length(btrim(watermark)) BETWEEN 8 AND 500)
);

-- The keyset the list endpoint pages by.
CREATE INDEX ix_report_export_created
    ON report.export (tenant_id, created_at DESC, id DESC);
-- What the requester's own list reads, and what the nightly expiry sweep reads.
CREATE INDEX ix_report_export_requester
    ON report.export (tenant_id, requested_by, created_at DESC, id DESC);
CREATE INDEX ix_report_export_expiry
    ON report.export (tenant_id, status, expires_at);

SELECT platform.attach_touch_row('report.export'::regclass);
SELECT platform.enable_tenant_rls('report.export'::regclass);

-- Both schemas, and `billing` for the same reason every migration since 000044 repeats it:
-- `GRANT ... ON ALL TABLES` grants on the tables that exist when it runs, so a table added
-- afterwards is a table the application role cannot read. A reconciliation run nobody but the
-- schema owner could write would be a job that failed at two in the morning every night.
SELECT platform.grant_app_schema_usage('billing');
SELECT platform.grant_app_schema_usage('report');
