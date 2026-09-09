-- 000044: the invoice as the provider entered it, and its allocation to claims
-- (WP-I7-02, v1.2 9.14, 10.9 steps 1-3 and 8, 11.12, 16.8, 39).
--
-- KAPSORA issues no fiscal document here. The provider raised an invoice somewhere else --
-- on their own e-invoice integrator, on paper, in their own accounting package -- and this
-- schema records what they raised and holds it against the claims the payer has already
-- approved. The e-document of M8 arrives later through `source` and `edocument_id`, which
-- exist now, are nullable, and are written by nothing in this milestone: a column added
-- later would mean invoice ids that meant different things before and after.
--
-- Three sentences carry the whole schema, and all three are constraints here rather than
-- habits in a service:
--
--   * **the invoice's own arithmetic is the database's.** `line_extension + tax = payable`
--     is a CHECK on exact decimals, never a float and never a sum a service is trusted to
--     have got right. A service that rounded the tax independently fails here by a kuruş
--     rather than publishing a document nobody can reconcile.
--   * **an allocation never exceeds what the payer approved, and never lands on a claim
--     nobody approved.** That is a fact about another table, so it is a deferred constraint
--     trigger: deferred because `putInvoiceAllocations` replaces a whole set and the
--     intermediate states of a replacement are nobody's business, and a trigger rather than
--     an application check because a backfill, an outbox handler and a psql session all
--     reach this table and only one of them runs the service.
--   * **a submitted invoice never changes.** Not its amounts, not its number, and above all
--     not which claims it covers. A correction is a *new* invoice that supersedes the old
--     one, and the old one stays exactly as it was submitted, because "what did this
--     provider bill us in March" is a question a dispute asks years later.
--
-- What is deliberately not here: `billing.invoice_status_event`. Audit and the outbox carry
-- the transitions, and a third record of the same fact is a third thing that can disagree.

CREATE SCHEMA IF NOT EXISTS billing;

-- ---------------------------------------------------------------------------
-- Permissions
-- ---------------------------------------------------------------------------
--
-- `invoice.read` and `invoice.manage` were seeded by migration 000008 and are held by the
-- payer's financial reviewer and the provider's billing clerk. Nothing new is needed: the
-- sponsor's HR user reads invoices under the same `invoice.read`, and what it may see of
-- one is decided by the projection rather than by a grant of its own.

-- ---------------------------------------------------------------------------
-- billing.invoice
-- ---------------------------------------------------------------------------
--
-- The header, as the provider entered it. Every money column is `numeric(20,6)` and every
-- one of them is what the provider typed: `vat_rate` in particular is recorded and never
-- recomputed, because a rate this platform derived would be this platform's opinion about
-- somebody else's fiscal document.
--
-- `provider_tax_id_hash` is the VKN's blind index, copied from `directory.organization`
-- at creation. It is a copy rather than a join for one reason: the invoice is a record of
-- what was billed *under that tax identity*, and an organization that later corrects its
-- VKN must not silently rewrite which taxpayer issued a document already submitted. The
-- plaintext never appears -- not in this table, not in an audit detail, not in a URL and
-- not in a response body.
CREATE TABLE billing.invoice (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    provider_organization_id uuid NOT NULL,
    -- The sponsor the contract names, when it names one. NULL is ordinary and means the
    -- tenant itself is the payer, which is the common case and is not worth a synthetic
    -- row somebody would have to keep in step.
    payer_organization_id    uuid,
    -- Where the document came from. Only MANUAL is reachable in M7; EDOCUMENT is M8's,
    -- and it is in the word list now so that the column does not have to change when the
    -- integration lands.
    source                   text NOT NULL DEFAULT 'MANUAL'
                             CHECK (source IN ('MANUAL', 'EDOCUMENT')),
    -- M8. Nullable, unwritten, and deliberately carrying no foreign key yet: the table it
    -- will point at does not exist.
    edocument_id             uuid,
    invoice_number           text NOT NULL,
    invoice_date             date NOT NULL,
    -- Derived from the date and stored, because it is half of the uniqueness rule of 11.12
    -- and a partial unique index cannot call a function on another column's value without
    -- becoming an expression index nobody can read. The CHECK below is what keeps the
    -- stored value honest.
    fiscal_year              integer NOT NULL,
    provider_tax_id_hash     bytea NOT NULL,
    currency_code            char(3) NOT NULL DEFAULT 'TRY',
    line_extension_amount    numeric(20,6) NOT NULL,
    tax_amount               numeric(20,6) NOT NULL,
    payable_amount           numeric(20,6) NOT NULL,
    -- As entered, never computed. NULL is ordinary on an exempt document.
    vat_rate                 numeric(5,2),
    domain_code              text NOT NULL DEFAULT 'GENERIC'
                             CHECK (domain_code IN (
                                 'GENERIC','HEALTH','ACCOMMODATION','ASSISTANCE','EDUCATION',
                                 'SPORT','TRANSPORT','CARE','OTHER'
                             )),
    status                   text NOT NULL DEFAULT 'DRAFT'
                             CHECK (status IN (
                                 'DRAFT','SUBMITTED','IN_BATCH','RETURNED','APPROVED',
                                 'PARTIALLY_APPROVED','REJECTED','SETTLED','CANCELLED'
                             )),
    -- The correction chain. Both ends are stored: the new invoice names what it replaces
    -- from the moment it is drafted, and the old one learns its successor only when that
    -- successor is actually submitted -- a draft correction that was abandoned must not
    -- have cancelled the document it was going to replace.
    supersedes_invoice_id    uuid,
    superseded_by_invoice_id uuid,
    -- WP-I7-03's icmal. Nullable, unwritten here and deliberately carrying no foreign key
    -- yet, for the same reason `edocument_id` carries none: the table it will point at does
    -- not exist. It is declared now because `listInvoices` answers "which batch is this
    -- invoice in", and an answer that could not be null would be an answer that had to be
    -- invented.
    batch_id                 uuid,
    submitted_at             timestamptz,
    -- The scanned image, a WP-I4-04 document object. The link row with
    -- `aggregate_type = 'INVOICE'` is what a reviewer opens; this column is what the submit
    -- gate reads, and the service writes both.
    document_id              uuid,
    notes                    text,
    created_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by               uuid,
    updated_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by               uuid,
    row_version              bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_billing_invoice_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT fk_billing_invoice_provider FOREIGN KEY (tenant_id, provider_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_invoice_payer FOREIGN KEY (tenant_id, payer_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_invoice_supersedes FOREIGN KEY (tenant_id, supersedes_invoice_id)
        REFERENCES billing.invoice(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_invoice_superseded_by FOREIGN KEY (tenant_id, superseded_by_invoice_id)
        REFERENCES billing.invoice(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_invoice_document FOREIGN KEY (tenant_id, document_id)
        REFERENCES document.object(tenant_id, id) ON DELETE RESTRICT,
    -- The number the provider printed on the paper. Letters, digits and the separators a
    -- Turkish e-invoice serial actually uses; no control characters, because this string is
    -- rendered on a screen and quoted on a telephone.
    CONSTRAINT ck_billing_invoice_number
        CHECK (invoice_number ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$'),
    CONSTRAINT ck_billing_invoice_currency CHECK (currency_code ~ '^[A-Z]{3}$'),
    -- The stored fiscal year is the invoice date's year and nothing else. Without this the
    -- uniqueness rule of 11.12 could be evaded by typing a different year beside the date.
    CONSTRAINT ck_billing_invoice_fiscal_year
        CHECK (fiscal_year = EXTRACT(YEAR FROM invoice_date)::integer),
    -- The arithmetic. Exact decimals, checked by the database, on every write.
    CONSTRAINT ck_billing_invoice_totals
        CHECK (line_extension_amount + tax_amount = payable_amount),
    CONSTRAINT ck_billing_invoice_amounts_sign CHECK (
        line_extension_amount >= 0 AND tax_amount >= 0 AND payable_amount >= 0
    ),
    CONSTRAINT ck_billing_invoice_vat_rate
        CHECK (vat_rate IS NULL OR (vat_rate >= 0 AND vat_rate <= 100)),
    CONSTRAINT ck_billing_invoice_tax_hash_len
        CHECK (octet_length(provider_tax_id_hash) = 32),
    -- A manual invoice references no e-document; M8's own migration relaxes the other half
    -- when there is a table for `edocument_id` to point at.
    CONSTRAINT ck_billing_invoice_edocument
        CHECK (source <> 'MANUAL' OR edocument_id IS NULL),
    -- A submitted invoice says when it was submitted. The two states in which it was never
    -- submitted are the draft and the draft somebody withdrew.
    CONSTRAINT ck_billing_invoice_submitted CHECK (
        (submitted_at IS NOT NULL) OR status IN ('DRAFT', 'CANCELLED')
    ),
    -- An invoice supersedes something other than itself, and is superseded by something
    -- other than itself. A self-reference would be a chain a reader walks for ever.
    CONSTRAINT ck_billing_invoice_supersede_self CHECK (
        supersedes_invoice_id IS DISTINCT FROM id
        AND superseded_by_invoice_id IS DISTINCT FROM id
    ),
    CONSTRAINT ck_billing_invoice_notes
        CHECK (notes IS NULL OR length(notes) <= 2000)
);

-- **The number is unique in the provider's fiscal year** (11.12).
--
-- Two statuses are outside the index, and for the same reason: neither is a live claim on
-- that number in the payer's books. A CANCELLED invoice was withdrawn, and a RETURNED one
-- was sent back for correction -- and section 2.2 says the correction may carry the number
-- the provider's own books already have. Two different providers billing "2026-000041" is
-- ordinary and is not this index's business.
--
-- What the index cannot say is *who* may reuse a returned invoice's number, because that is
-- a fact about another row: only the invoice that supersedes it. The service checks that and
-- answers INVOICE_NUMBER_TAKEN; this index is what stops two *live* documents sharing a
-- number however they were written, including by a resubmission of the returned one.
CREATE UNIQUE INDEX uq_billing_invoice_number
    ON billing.invoice (tenant_id, provider_organization_id, fiscal_year, invoice_number)
 WHERE status NOT IN ('CANCELLED', 'RETURNED');

-- **One successor per invoice.** Two corrections of one returned invoice would be two
-- documents each claiming to replace it, and whichever was submitted second would silently
-- win the chain.
CREATE UNIQUE INDEX uq_billing_invoice_supersedes
    ON billing.invoice (tenant_id, supersedes_invoice_id)
 WHERE supersedes_invoice_id IS NOT NULL;

CREATE INDEX ix_billing_invoice_provider
    ON billing.invoice (tenant_id, provider_organization_id, status, invoice_date DESC);
CREATE INDEX ix_billing_invoice_status
    ON billing.invoice (tenant_id, status, invoice_date DESC);
-- The keyset the list endpoint pages by.
CREATE INDEX ix_billing_invoice_created
    ON billing.invoice (tenant_id, created_at DESC, id DESC);
CREATE INDEX ix_billing_invoice_document
    ON billing.invoice (tenant_id, document_id) WHERE document_id IS NOT NULL;
CREATE INDEX ix_billing_invoice_batch
    ON billing.invoice (tenant_id, batch_id) WHERE batch_id IS NOT NULL;

SELECT platform.attach_touch_row('billing.invoice'::regclass);
SELECT platform.enable_tenant_rls('billing.invoice'::regclass);

-- ---------------------------------------------------------------------------
-- billing.invoice_claim
-- ---------------------------------------------------------------------------
--
-- Which approved claims this invoice is collecting, and how much of each. It is the join
-- the whole package exists for: without it an invoice is a number somebody typed, and with
-- it the payer can say exactly which of its own decisions the provider is billing.
--
-- `claim_version_no` is the version the allocation was made against. A claim corrected
-- afterwards is a different set of figures, and an invoice that silently followed the
-- correction would be an invoice whose total stopped matching the sum of its parts.
--
-- `claim_status_before` is what the claim was when it was allocated -- APPROVED or
-- PARTIALLY_APPROVED. It is stored because releasing a claim has to put it back where it
-- came from: recomputing "was this fully or partly approved" from the line decisions would
-- be a second implementation of a question WP-I5-04 already answered, and the second
-- implementation is the one that would eventually disagree.
--
-- `active` is "the invoice this row sits on is still live". It is a stored column kept by
-- the trigger below rather than a join, because a partial unique index cannot look at
-- another table -- and the partial unique index is the entire point: **one claim sits on
-- one live invoice**, so a provider cannot bill the same approved claim twice by putting
-- it on two documents.
CREATE TABLE billing.invoice_claim (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id           uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    invoice_id          uuid NOT NULL,
    claim_id            uuid NOT NULL,
    claim_version_no    integer NOT NULL CHECK (claim_version_no > 0),
    allocated_amount    numeric(20,6) NOT NULL CHECK (allocated_amount >= 0),
    currency_code       char(3) NOT NULL DEFAULT 'TRY',
    claim_status_before text NOT NULL
                        CHECK (claim_status_before IN ('APPROVED', 'PARTIALLY_APPROVED')),
    active              boolean NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by          uuid,
    CONSTRAINT uq_billing_invoice_claim_id_tenant UNIQUE (tenant_id, id),
    -- One row per claim per invoice: an invoice that named the same claim twice would
    -- allocate to it twice and the ceiling below would be checked against half the figure.
    CONSTRAINT uq_billing_invoice_claim UNIQUE (tenant_id, invoice_id, claim_id),
    CONSTRAINT fk_billing_invoice_claim_invoice FOREIGN KEY (tenant_id, invoice_id)
        REFERENCES billing.invoice(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_invoice_claim_claim FOREIGN KEY (tenant_id, claim_id)
        REFERENCES claim.claim(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_billing_invoice_claim_currency CHECK (currency_code ~ '^[A-Z]{3}$')
);

-- **One claim, one live invoice.**
CREATE UNIQUE INDEX uq_billing_invoice_claim_live
    ON billing.invoice_claim (tenant_id, claim_id) WHERE active;
CREATE INDEX ix_billing_invoice_claim_invoice
    ON billing.invoice_claim (tenant_id, invoice_id, created_at, id);
SELECT platform.enable_tenant_rls('billing.invoice_claim'::regclass);

-- ---------------------------------------------------------------------------
-- The approved total, in one place
-- ---------------------------------------------------------------------------
--
-- Lines minus adjustments, over the claim's current version, in exact decimals. It is the
-- same arithmetic `InvoiceReadiness` and the provider's earnings view perform in Go, and it
-- is written a second time here for one reason only: the ceiling below has to hold against
-- a psql session and a backfill, which never run that Go. The two are kept honest by a test
-- that compares them on the same claim.
--
-- STABLE and not IMMUTABLE: it reads two tables, and a planner that cached it across a
-- statement would cache a figure a reviewer had just changed.
CREATE OR REPLACE FUNCTION billing.claim_approved_total(p_tenant uuid, p_claim uuid)
RETURNS numeric
LANGUAGE sql STABLE AS $$
    SELECT COALESCE((
        SELECT sum(d.approved_amount)
          FROM claim.claim c
          JOIN claim.claim_version v
            ON v.tenant_id = c.tenant_id AND v.claim_id = c.id
           AND v.version_no = c.current_version_no
          JOIN claim.claim_line l ON l.tenant_id = v.tenant_id AND l.version_id = v.id
          -- The latest decision for a line is the decision; the history stays.
          JOIN LATERAL (
              SELECT ld.approved_amount
                FROM claim.line_decision ld
               WHERE ld.tenant_id = l.tenant_id AND ld.line_id = l.id
               ORDER BY ld.decided_at DESC, ld.id DESC
               LIMIT 1
          ) d ON true
         WHERE c.tenant_id = p_tenant AND c.id = p_claim
    ), 0)
    - COALESCE((
        SELECT sum(a.amount)
          FROM claim.adjustment a
         WHERE a.tenant_id = p_tenant AND a.claim_id = p_claim
    ), 0);
$$;

-- ---------------------------------------------------------------------------
-- The allocation ceiling, deferred
-- ---------------------------------------------------------------------------
--
-- Three facts about another row, which is why none of them can be a CHECK:
--
--   * the claim is one the payer approved -- APPROVED or PARTIALLY_APPROVED. An allocation
--     against a draft, a rejected or an already-invoiced claim is money being collected for
--     something nobody agreed to pay;
--   * the allocation does not exceed what was approved. This is the ceiling: a provider may
--     bill less of an approved claim than was approved (a partial collection is ordinary),
--     and may never bill more;
--   * the allocation is denominated the way the invoice is. A total across currencies is
--     not a total.
--
-- DEFERRABLE INITIALLY DEFERRED because `putInvoiceAllocations` deletes the whole set and
-- writes it again: the moment between the two is not a state anybody should be judged on,
-- and only the state at commit is a state a reader could ever observe.
--
-- The status half is checked at commit, and commit is *before* the submit moves the claims
-- to INVOICED -- because the submit writes no allocation row at all, so this trigger does
-- not fire in that transaction. An update that only flips `active` is likewise skipped: it
-- is the release below, and a release must not be refused for releasing a claim that has
-- meanwhile stopped being approved.
CREATE OR REPLACE FUNCTION billing.tg_invoice_claim_allocation() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    claim_status   text;
    approved_total numeric;
    invoice_currency char(3);
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.invoice_id = OLD.invoice_id
       AND NEW.claim_id = OLD.claim_id
       AND NEW.claim_version_no = OLD.claim_version_no
       AND NEW.allocated_amount = OLD.allocated_amount
       AND NEW.currency_code = OLD.currency_code THEN
        RETURN NULL;
    END IF;

    SELECT c.status INTO claim_status
      FROM claim.claim c
     WHERE c.tenant_id = NEW.tenant_id AND c.id = NEW.claim_id;
    IF claim_status IS NULL THEN
        RAISE EXCEPTION 'claim % not found in tenant %', NEW.claim_id, NEW.tenant_id
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    IF claim_status NOT IN ('APPROVED', 'PARTIALLY_APPROVED') THEN
        RAISE EXCEPTION 'claim % is % and cannot be allocated to an invoice',
            NEW.claim_id, claim_status
            USING ERRCODE = 'check_violation';
    END IF;

    SELECT i.currency_code INTO invoice_currency
      FROM billing.invoice i
     WHERE i.tenant_id = NEW.tenant_id AND i.id = NEW.invoice_id;
    IF invoice_currency IS NULL THEN
        RAISE EXCEPTION 'billing invoice % not found in tenant %', NEW.invoice_id, NEW.tenant_id
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    IF NEW.currency_code <> invoice_currency THEN
        RAISE EXCEPTION 'allocation currency % does not match invoice currency %',
            NEW.currency_code, invoice_currency
            USING ERRCODE = 'check_violation';
    END IF;

    approved_total := billing.claim_approved_total(NEW.tenant_id, NEW.claim_id);
    IF NEW.allocated_amount > approved_total THEN
        RAISE EXCEPTION 'allocation % on claim % exceeds the approved total %',
            NEW.allocated_amount, NEW.claim_id, approved_total
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER tg_billing_invoice_claim_allocation
    AFTER INSERT OR UPDATE ON billing.invoice_claim
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION billing.tg_invoice_claim_allocation();

-- ---------------------------------------------------------------------------
-- The freeze
-- ---------------------------------------------------------------------------
--
-- The immutability of 11.12, as the database's rule rather than the service's. Once an
-- invoice has left DRAFT its figures, its number, its date, its parties, its tax identity
-- and its image are what they were; what may still change is where it is in the lifecycle
-- and which invoice ended up superseding it.
CREATE OR REPLACE FUNCTION billing.tg_invoice_frozen() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status = 'DRAFT' THEN
        RETURN NEW;
    END IF;
    IF NEW.line_extension_amount <> OLD.line_extension_amount
       OR NEW.tax_amount <> OLD.tax_amount
       OR NEW.payable_amount <> OLD.payable_amount
       OR NEW.invoice_number <> OLD.invoice_number
       OR NEW.invoice_date <> OLD.invoice_date
       OR NEW.fiscal_year <> OLD.fiscal_year
       OR NEW.currency_code <> OLD.currency_code
       OR NEW.vat_rate IS DISTINCT FROM OLD.vat_rate
       OR NEW.provider_organization_id <> OLD.provider_organization_id
       OR NEW.payer_organization_id IS DISTINCT FROM OLD.payer_organization_id
       OR NEW.provider_tax_id_hash <> OLD.provider_tax_id_hash
       OR NEW.document_id IS DISTINCT FROM OLD.document_id
       OR NEW.supersedes_invoice_id IS DISTINCT FROM OLD.supersedes_invoice_id
       OR NEW.domain_code <> OLD.domain_code
       OR NEW.source <> OLD.source
       OR NEW.notes IS DISTINCT FROM OLD.notes
    THEN
        RAISE EXCEPTION 'invoice % is % and its header is frozen', OLD.id, OLD.status
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tg_billing_invoice_frozen
    BEFORE UPDATE ON billing.invoice
    FOR EACH ROW EXECUTE FUNCTION billing.tg_invoice_frozen();

-- The other half of the freeze, and the more important one: **a submitted invoice's links
-- never change.** No row is added, none is removed and no amount on one moves once the
-- invoice has left DRAFT.
--
-- The single exception is an update that touches nothing but `active`, which is the release
-- performed by the status trigger below. It moves no money and changes nothing a reader
-- could be paid on: it records that the document these rows sat on has stopped being live.
CREATE OR REPLACE FUNCTION billing.tg_invoice_claim_frozen() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    target         uuid;
    tenant         uuid;
    invoice_status text;
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.invoice_id = OLD.invoice_id
       AND NEW.claim_id = OLD.claim_id
       AND NEW.claim_version_no = OLD.claim_version_no
       AND NEW.allocated_amount = OLD.allocated_amount
       AND NEW.currency_code = OLD.currency_code
       AND NEW.claim_status_before = OLD.claim_status_before THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'DELETE' THEN
        target := OLD.invoice_id;
        tenant := OLD.tenant_id;
    ELSE
        target := NEW.invoice_id;
        tenant := NEW.tenant_id;
    END IF;

    SELECT i.status INTO invoice_status
      FROM billing.invoice i
     WHERE i.tenant_id = tenant AND i.id = target;
    IF invoice_status IS NULL THEN
        RAISE EXCEPTION 'billing invoice % not found in tenant %', target, tenant
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    IF invoice_status <> 'DRAFT' THEN
        RAISE EXCEPTION 'invoice % is % and its allocations are frozen', target, invoice_status
            USING ERRCODE = 'check_violation';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tg_billing_invoice_claim_frozen
    BEFORE INSERT OR UPDATE OR DELETE ON billing.invoice_claim
    FOR EACH ROW EXECUTE FUNCTION billing.tg_invoice_claim_frozen();

-- ---------------------------------------------------------------------------
-- The release
-- ---------------------------------------------------------------------------
--
-- A cancelled or returned invoice stops holding its claims. The rows stay -- nothing here is
-- ever deleted, and "which claims did this cancelled invoice cover" is a question a dispute
-- asks -- but they stop being live, so `uq_billing_invoice_claim_live` lets the claims be
-- put on the correction.
--
-- It is a trigger rather than an UPDATE in the service because a returned invoice is
-- WP-I7-03's command and a cancelled one is this package's, and "the links are released" has
-- to be one rule rather than one per caller.
CREATE OR REPLACE FUNCTION billing.tg_invoice_release_claims() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    live boolean;
BEGIN
    live := NEW.status NOT IN ('CANCELLED', 'RETURNED');
    UPDATE billing.invoice_claim ic
       SET active = live
     WHERE ic.tenant_id = NEW.tenant_id
       AND ic.invoice_id = NEW.id
       AND ic.active <> live;
    RETURN NULL;
END
$$;

CREATE TRIGGER tg_billing_invoice_release_claims
    AFTER UPDATE OF status ON billing.invoice
    FOR EACH ROW WHEN (NEW.status IS DISTINCT FROM OLD.status)
    EXECUTE FUNCTION billing.tg_invoice_release_claims();

SELECT platform.grant_app_schema_usage('billing');
