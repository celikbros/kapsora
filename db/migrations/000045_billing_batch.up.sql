-- 000045: the icmal -- the batch a provider bundles its submitted invoices into, and the
-- payer's decision on each of them (WP-I7-03, v1.2 10.9 steps 4-8, 11.12, 16.8, 12, Faz 8).
--
-- The batch is the document the two sides actually argue about. An invoice is what one
-- provider raised; a batch is "here is everything I am collecting from you for March", and
-- the payer answers it invoice by invoice: approved, cut, returned or rejected. What comes
-- out the other end is a set of decided totals, and those totals are what a settlement pays.
--
-- Three sentences carry this schema, and all three are constraints here rather than habits
-- in a service:
--
--   * **a submitted batch's membership and amounts never change.** Not which invoices are in
--     it, not what each of them was submitted at. A batch a reviewer is halfway through
--     deciding must be the same batch when they finish, and "which documents did the
--     provider send us on the 3rd" is a question a dispute asks years later. It is a trigger
--     rather than an application check because a backfill, an outbox handler and a psql
--     session all reach this table and only one of them runs the service.
--   * **an invoice sits in one live batch.** A partial unique index over a stored `active`
--     column, kept by the trigger below, exactly as `billing.invoice_claim.active` keeps
--     "one claim, one live invoice". Two batches each collecting the same invoice would be
--     one invoice paid twice, and no amount of care in a service prevents that when two
--     callers press the button at the same moment.
--   * **the decided totals reconcile.** `approved + cut + returned + rejected = submitted`
--     is a CHECK on exact decimals the moment the batch is DECIDED. A service that recomputed
--     the totals wrongly fails here rather than opening a settlement for a figure that is not
--     the sum of its parts.
--
-- What is deliberately not here: `billing.batch_status_event`. Audit and the outbox carry the
-- transitions, and a third record of the same fact is a third thing that can disagree.

-- ---------------------------------------------------------------------------
-- Permissions
-- ---------------------------------------------------------------------------
--
-- `batch.create`, `batch.submit` and `batch.review` were seeded by migration 000008 and are
-- held by the provider's billing clerk (the first two) and the payer's financial reviewer
-- (the third). Nothing new is needed and nothing new is added: a package that invented a
-- permission here would invent one the role templates in
-- internal/identity/application/roles.go do not grant to anybody.

-- ---------------------------------------------------------------------------
-- The claim's last status
-- ---------------------------------------------------------------------------
--
-- `CLOSED_UNPAID` joins claim.claim's word list. It is where a claim lands when the payer
-- rejects the invoice that was collecting it: the decision stands, the money will not be
-- paid, and the claim is finished. It is not CANCELLED -- nobody withdrew it -- and it is
-- not REJECTED, which is the payer refusing the *claim* rather than refusing to pay a
-- document that billed it. A settlement summing "what did we decline to pay" needs the two
-- apart.
-- The old word list was written inline in migration 000034 and therefore carries the name
-- PostgreSQL chose for it. It is found by what it says rather than by what it is called,
-- because a constraint dropped by a guessed name is a migration that fails on the one
-- database whose name was generated differently -- and one that failed *silently* would
-- leave a word list that still refuses the status this package writes.
DO $$
DECLARE
    old_check text;
BEGIN
    SELECT c.conname INTO old_check
      FROM pg_constraint c
     WHERE c.conrelid = 'claim.claim'::regclass
       AND c.contype = 'c'
       AND pg_get_constraintdef(c.oid) LIKE '%''AUTO_ADJUDICATED''%';
    IF old_check IS NULL THEN
        RAISE EXCEPTION 'claim.claim has no status check to replace';
    END IF;
    EXECUTE format('ALTER TABLE claim.claim DROP CONSTRAINT %I', old_check);
END
$$;

ALTER TABLE claim.claim
    ADD CONSTRAINT ck_claim_status CHECK (status IN (
        'DRAFT','SUBMITTED','AUTO_ADJUDICATED','PENDING_MEDICAL','PENDING_FINANCIAL',
        'RETURNED','PARTIALLY_APPROVED','APPROVED','REJECTED','INVOICED','BATCHED',
        'SETTLED','CLOSED_UNPAID','CANCELLED'
    ));

-- ---------------------------------------------------------------------------
-- billing.batch
-- ---------------------------------------------------------------------------
--
-- One provider, one payer, one currency, one domain, one period. All five are on the header
-- rather than derived from the members, and that is the whole point of `putBatchInvoices`
-- refusing a mixture: a total across currencies is not a total, and a batch spanning two
-- payers is two conversations in one document.
--
-- The five totals are recomputed by the server after every decision and written here. They
-- are stored rather than summed on read because a settlement opens on `batch.decided` and
-- has to be able to say what it is settling without walking the members -- and because the
-- CHECK below can only hold on a stored figure.
CREATE TABLE billing.batch (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    -- `IC-YYYYMM-XXXXXXXX`. The tail is forty random bits rather than a sequence, for the
    -- reason a claim reference's tail is: a sequential reference tells a competitor how many
    -- batches a tenant settled last month.
    reference                text NOT NULL,
    provider_organization_id uuid NOT NULL,
    -- The sponsor the contract names. NULL is ordinary and means the tenant itself is the
    -- payer, which is the common case and is not worth a synthetic row somebody would have
    -- to keep in step.
    payer_organization_id    uuid,
    domain_code              text NOT NULL DEFAULT 'GENERIC'
                             CHECK (domain_code IN (
                                 'GENERIC','HEALTH','ACCOMMODATION','ASSISTANCE','EDUCATION',
                                 'SPORT','TRANSPORT','CARE','OTHER'
                             )),
    currency_code            char(3) NOT NULL DEFAULT 'TRY',
    period_from              date NOT NULL,
    period_to                date NOT NULL,
    status                   text NOT NULL DEFAULT 'DRAFT'
                             CHECK (status IN (
                                 'DRAFT','SUBMITTED','UNDER_REVIEW','DECIDED','SETTLING',
                                 'CLOSED','CANCELLED'
                             )),
    submitted_at             timestamptz,
    submitted_by             uuid REFERENCES iam.actor(id),
    decided_at               timestamptz,
    decided_by               uuid REFERENCES iam.actor(id),
    invoice_count            integer NOT NULL DEFAULT 0 CHECK (invoice_count >= 0),
    submitted_total          numeric(20,6) NOT NULL DEFAULT 0,
    approved_total           numeric(20,6) NOT NULL DEFAULT 0,
    cut_total                numeric(20,6) NOT NULL DEFAULT 0,
    returned_total           numeric(20,6) NOT NULL DEFAULT 0,
    rejected_total           numeric(20,6) NOT NULL DEFAULT 0,
    created_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by               uuid,
    updated_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by               uuid,
    row_version              bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_billing_batch_id_tenant UNIQUE (tenant_id, id),
    -- The reference a provider quotes on the telephone, unique inside the tenant.
    CONSTRAINT uq_billing_batch_reference UNIQUE (tenant_id, reference),
    CONSTRAINT fk_billing_batch_provider FOREIGN KEY (tenant_id, provider_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_batch_payer FOREIGN KEY (tenant_id, payer_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_billing_batch_reference CHECK (reference ~ '^IC-[0-9]{6}-[A-Z2-7]{8}$'),
    CONSTRAINT ck_billing_batch_currency CHECK (currency_code ~ '^[A-Z]{3}$'),
    CONSTRAINT ck_billing_batch_period CHECK (period_from <= period_to),
    -- A submitted batch says when and by whom. The two states in which it was never
    -- submitted are the draft and the draft somebody withdrew.
    CONSTRAINT ck_billing_batch_submitted CHECK (
        ((submitted_at IS NULL) = (submitted_by IS NULL))
        AND (submitted_at IS NOT NULL OR status IN ('DRAFT', 'CANCELLED'))
    ),
    -- And a decided one says when and by whom. `decided_by` is the second pair of eyes above
    -- the threshold and the reviewer below it; either way it is a person, never a process.
    CONSTRAINT ck_billing_batch_decided CHECK (
        ((decided_at IS NULL) = (decided_by IS NULL))
        AND (decided_at IS NOT NULL OR status NOT IN ('DECIDED', 'SETTLING', 'CLOSED'))
    ),
    CONSTRAINT ck_billing_batch_amounts_sign CHECK (
        submitted_total >= 0 AND approved_total >= 0 AND cut_total >= 0
        AND returned_total >= 0 AND rejected_total >= 0
    ),
    -- **The decided totals reconcile.** Exact decimals, checked by the database, the moment
    -- the batch reaches a status a settlement can open on.
    CONSTRAINT ck_billing_batch_totals CHECK (
        status NOT IN ('DECIDED', 'SETTLING', 'CLOSED')
        OR approved_total + cut_total + returned_total + rejected_total = submitted_total
    )
);

CREATE INDEX ix_billing_batch_provider
    ON billing.batch (tenant_id, provider_organization_id, status, period_from DESC);
CREATE INDEX ix_billing_batch_status
    ON billing.batch (tenant_id, status, period_from DESC);
-- The keyset the list endpoint pages by.
CREATE INDEX ix_billing_batch_created
    ON billing.batch (tenant_id, created_at DESC, id DESC);

SELECT platform.attach_touch_row('billing.batch'::regclass);
SELECT platform.enable_tenant_rls('billing.batch'::regclass);

-- The invoice's `batch_id` was declared by migration 000044 with no foreign key, because the
-- table it points at did not exist. It does now.
ALTER TABLE billing.invoice
    ADD CONSTRAINT fk_billing_invoice_batch FOREIGN KEY (tenant_id, batch_id)
        REFERENCES billing.batch(tenant_id, id) ON DELETE RESTRICT;

-- ---------------------------------------------------------------------------
-- billing.batch_invoice
-- ---------------------------------------------------------------------------
--
-- One invoice in one batch, with the payer's answer to it.
--
-- `submitted_amount` is a copy of the invoice's payable amount at the moment the batch was
-- submitted. It is a copy rather than a join for the same reason the invoice copies the
-- provider's tax identity: the batch is a record of what was *submitted*, and a total that
-- silently followed a document somebody corrected afterwards would be a total that stopped
-- matching the sum of its parts.
--
-- `active` is "the batch this row sits on is not cancelled". It is a stored column kept by
-- the trigger below rather than a join, because a partial unique index cannot look at
-- another table -- and the partial unique index is the point: **one invoice, one live
-- batch**.
CREATE TABLE billing.batch_invoice (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id        uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    batch_id         uuid NOT NULL,
    invoice_id       uuid NOT NULL,
    submitted_amount numeric(20,6) NOT NULL CHECK (submitted_amount >= 0),
    decision         text CHECK (decision IN ('APPROVE', 'CUT', 'RETURN', 'REJECT')),
    approved_amount  numeric(20,6),
    reason_code      text,
    reason_text      text,
    decided_by       uuid REFERENCES iam.actor(id),
    decided_at       timestamptz,
    active           boolean NOT NULL DEFAULT true,
    created_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by       uuid,
    CONSTRAINT uq_billing_batch_invoice_id_tenant UNIQUE (tenant_id, id),
    -- One row per invoice per batch: a batch that named the same invoice twice would count
    -- it twice in its own submitted total.
    CONSTRAINT uq_billing_batch_invoice UNIQUE (tenant_id, batch_id, invoice_id),
    CONSTRAINT fk_billing_batch_invoice_batch FOREIGN KEY (tenant_id, batch_id)
        REFERENCES billing.batch(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_batch_invoice_invoice FOREIGN KEY (tenant_id, invoice_id)
        REFERENCES billing.invoice(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_billing_batch_invoice_reason_code
        CHECK (reason_code IS NULL OR reason_code ~ '^[A-Z][A-Z0-9_.:-]{1,79}$'),
    CONSTRAINT ck_billing_batch_invoice_reason_text
        CHECK (reason_text IS NULL OR length(reason_text) <= 1000),
    -- **The decision and its amount are one fact.** An undecided row carries none of the
    -- five decision columns; a decided one carries the decision, the amount it approves, the
    -- person and the moment -- and a reason for anything that is not a plain approval,
    -- because a cut, a return and a rejection are what a provider disputes and a dispute is
    -- answerable only from a code somebody can count.
    --
    -- The amount is tied to the word: APPROVE is the whole submitted amount, CUT is strictly
    -- between zero and it, and RETURN and REJECT are nothing. Without this a screen could
    -- record "approved" beside a figure that was not what was billed.
    CONSTRAINT ck_billing_batch_invoice_decision CHECK (
        (decision IS NULL AND approved_amount IS NULL AND reason_code IS NULL
         AND reason_text IS NULL AND decided_by IS NULL AND decided_at IS NULL)
        OR (
            decision IS NOT NULL AND approved_amount IS NOT NULL
            AND decided_by IS NOT NULL AND decided_at IS NOT NULL
            AND (decision <> 'APPROVE' OR approved_amount = submitted_amount)
            AND (decision <> 'CUT'
                 OR (approved_amount > 0 AND approved_amount < submitted_amount))
            AND (decision NOT IN ('RETURN', 'REJECT') OR approved_amount = 0)
            AND (decision = 'APPROVE' OR reason_code IS NOT NULL)
        )
    )
);

-- **One invoice, one live batch.**
CREATE UNIQUE INDEX uq_billing_batch_invoice_live
    ON billing.batch_invoice (tenant_id, invoice_id) WHERE active;
CREATE INDEX ix_billing_batch_invoice_batch
    ON billing.batch_invoice (tenant_id, batch_id, created_at, id);
SELECT platform.enable_tenant_rls('billing.batch_invoice'::regclass);

-- ---------------------------------------------------------------------------
-- billing.batch_invoice_adjustment
-- ---------------------------------------------------------------------------
--
-- Which `claim.adjustment` rows a CUT wrote, and which reversal took each of them back.
--
-- It exists because "a changed decision reverses the earlier adjustment rather than editing
-- it" has to be performable, and performing it means finding the rows the earlier decision
-- wrote. `claim.adjustment.source_id` cannot carry the link -- migration 000043 reserves it
-- for the two system sources and CHECKs that a REVIEW row has none -- and matching on the
-- amount and the reason code would be a join by coincidence, which is exactly the kind of
-- thing that reverses the wrong row on the day two reviewers cut the same claim.
--
-- It is append-only in practice: `reversed_by_adjustment_id` is written once, when the
-- decision changes, and nothing else on the row ever moves.
CREATE TABLE billing.batch_invoice_adjustment (
    id                        uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                 uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    batch_invoice_id          uuid NOT NULL,
    claim_id                  uuid NOT NULL,
    adjustment_id             uuid NOT NULL,
    amount                    numeric(20,6) NOT NULL CHECK (amount >= 0),
    reversed_by_adjustment_id uuid,
    created_at                timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by                uuid,
    CONSTRAINT uq_billing_batch_invoice_adjustment_id_tenant UNIQUE (tenant_id, id),
    -- One row per adjustment: two links to one adjustment would be two claims on one
    -- reversal.
    CONSTRAINT uq_billing_batch_invoice_adjustment UNIQUE (tenant_id, adjustment_id),
    CONSTRAINT fk_billing_batch_invoice_adjustment_member
        FOREIGN KEY (tenant_id, batch_invoice_id)
        REFERENCES billing.batch_invoice(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_batch_invoice_adjustment_claim FOREIGN KEY (tenant_id, claim_id)
        REFERENCES claim.claim(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_batch_invoice_adjustment_adjustment
        FOREIGN KEY (tenant_id, adjustment_id)
        REFERENCES claim.adjustment(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_batch_invoice_adjustment_reversal
        FOREIGN KEY (tenant_id, reversed_by_adjustment_id)
        REFERENCES claim.adjustment(tenant_id, id) ON DELETE RESTRICT
);

-- The rows a changed decision has to take back: this member's, not yet reversed.
CREATE INDEX ix_billing_batch_invoice_adjustment_live
    ON billing.batch_invoice_adjustment (tenant_id, batch_invoice_id)
 WHERE reversed_by_adjustment_id IS NULL;
SELECT platform.enable_tenant_rls('billing.batch_invoice_adjustment'::regclass);

-- ---------------------------------------------------------------------------
-- The freeze
-- ---------------------------------------------------------------------------
--
-- The immutability of Faz 8, as the database's rule rather than the service's.
--
-- Once a batch has left DRAFT its membership and its amounts are what they were. What may
-- still move is the decision -- and only while the batch is UNDER_REVIEW, because a decision
-- taken on a batch that has already been decided is a decision arriving after the answer.
--
-- `active` is exempt: an update that touches nothing but that column is the release below,
-- which moves no money and only records that the batch these rows sat on has been cancelled.
CREATE OR REPLACE FUNCTION billing.tg_batch_invoice_frozen() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    target       uuid;
    tenant       uuid;
    batch_status text;
    membership_changed boolean;
    decision_changed   boolean;
BEGIN
    IF TG_OP = 'DELETE' THEN
        target := OLD.batch_id;
        tenant := OLD.tenant_id;
    ELSE
        target := NEW.batch_id;
        tenant := NEW.tenant_id;
    END IF;

    SELECT b.status INTO batch_status
      FROM billing.batch b
     WHERE b.tenant_id = tenant AND b.id = target;
    IF batch_status IS NULL THEN
        RAISE EXCEPTION 'billing batch % not found in tenant %', target, tenant
            USING ERRCODE = 'foreign_key_violation';
    END IF;

    IF TG_OP = 'UPDATE' THEN
        membership_changed :=
            NEW.batch_id <> OLD.batch_id
            OR NEW.invoice_id <> OLD.invoice_id
            OR NEW.submitted_amount <> OLD.submitted_amount;
        decision_changed :=
            NEW.decision IS DISTINCT FROM OLD.decision
            OR NEW.approved_amount IS DISTINCT FROM OLD.approved_amount
            OR NEW.reason_code IS DISTINCT FROM OLD.reason_code
            OR NEW.reason_text IS DISTINCT FROM OLD.reason_text
            OR NEW.decided_by IS DISTINCT FROM OLD.decided_by
            OR NEW.decided_at IS DISTINCT FROM OLD.decided_at;
        IF NOT membership_changed AND NOT decision_changed THEN
            -- The release, or a no-op. Neither is anybody's business.
            RETURN NEW;
        END IF;
        IF membership_changed AND batch_status <> 'DRAFT' THEN
            RAISE EXCEPTION 'batch % is % and its membership is frozen', target, batch_status
                USING ERRCODE = 'check_violation';
        END IF;
        IF decision_changed AND batch_status NOT IN ('DRAFT', 'UNDER_REVIEW') THEN
            RAISE EXCEPTION 'batch % is % and its decisions cannot move', target, batch_status
                USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;

    -- An insert or a delete is membership, always.
    IF batch_status <> 'DRAFT' THEN
        RAISE EXCEPTION 'batch % is % and its membership is frozen', target, batch_status
            USING ERRCODE = 'check_violation';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tg_billing_batch_invoice_frozen
    BEFORE INSERT OR UPDATE OR DELETE ON billing.batch_invoice
    FOR EACH ROW EXECUTE FUNCTION billing.tg_batch_invoice_frozen();

-- ---------------------------------------------------------------------------
-- The release
-- ---------------------------------------------------------------------------
--
-- A cancelled batch stops holding its invoices. The rows stay -- nothing here is ever
-- deleted, and "which invoices did this cancelled batch cover" is a question a dispute asks
-- -- but they stop being live, so `uq_billing_batch_invoice_live` lets the invoices go into
-- another batch.
CREATE OR REPLACE FUNCTION billing.tg_batch_release_invoices() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    live boolean;
BEGIN
    live := NEW.status <> 'CANCELLED';
    UPDATE billing.batch_invoice bi
       SET active = live
     WHERE bi.tenant_id = NEW.tenant_id
       AND bi.batch_id = NEW.id
       AND bi.active <> live;
    RETURN NULL;
END
$$;

CREATE TRIGGER tg_billing_batch_release_invoices
    AFTER UPDATE OF status ON billing.batch
    FOR EACH ROW WHEN (NEW.status IS DISTINCT FROM OLD.status)
    EXECUTE FUNCTION billing.tg_batch_release_invoices();

SELECT platform.grant_app_schema_usage('billing');
