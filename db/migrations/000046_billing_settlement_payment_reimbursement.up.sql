-- 000046: what the payer owes the provider, what it actually paid, and what it pays the
-- member back (WP-I7-04, v1.2 4.3, 9.14, 10.9 step 9, 10.10, 11.12, 16.8, Faz 8).
--
-- KAPSORA transfers no money. It says what is owed, records what somebody else transferred,
-- and hands a payment order to an adapter that is not written yet. That is the whole of
-- v1.2 4.3 and it is why `billing.payment_record` is a *record* rather than a payment.
--
-- Four sentences carry this schema, and every one of them is a constraint here rather than a
-- habit in a service, because a backfill, an outbox handler and a psql session all reach
-- these tables and only one of them runs the service:
--
--   * **a settlement never exceeds the batch it opened on.** `payable_amount <=
--     approved_amount` is a CHECK on exact decimals, and `approved_amount` is frozen the
--     moment the settlement leaves DRAFT by the trigger below. A settlement that could be
--     edited afterwards would be a settlement whose figure stopped being the batch's.
--   * **one live settlement per batch.** A partial unique index over the batch, so a second
--     `batch.decided` delivery cannot open a second settlement however carefully the handler
--     looks first. A cancelled one is outside the index, which is exactly what lets the
--     replacement version be opened on the same batch.
--   * **the sum of the payment records never exceeds what is payable.** A deferred constraint
--     trigger, checked at commit against the settlement's own stored `paid_amount`, so two
--     concurrent records cannot each see a stale sum and both be accepted.
--   * **the member's IBAN exists in exactly one column.** `bank_account_ref_enc` holds the
--     platform cipher's envelope and `bank_account_masked` holds four characters. There is no
--     third column it could be in, no plaintext CHECK that would have to see it, and nothing
--     in this schema that indexes it.
--
-- What is deliberately not here: a `billing.settlement_status_event` and a
-- `billing.reimbursement_status_event`. Audit and the outbox carry the transitions, and a
-- third record of the same fact is a third thing that can disagree.

-- ---------------------------------------------------------------------------
-- Permissions
-- ---------------------------------------------------------------------------
--
-- `settlement.read` and `settlement.approve` were seeded by migration 000008 and are held by
-- the payer's financial reviewer (the first) and the payer's approver (both).
-- `settlement.record_payment` is new: entering the bank's reference against a settlement is
-- an ordinary finance-clerk task and not the second pair of eyes, so it is a grant of its
-- own rather than a second use of `settlement.approve`. A tenant that wants one person to do
-- both gives them both; a tenant that wants the approver never to touch the payment file
-- takes this one away, and neither is possible while the two are one permission.
--
-- It is NORMAL rather than PRIVILEGED because the figure it may write is bounded by the
-- database: a record beyond `payable_amount` is refused whoever enters it.
--
-- Nothing new is seeded for the reimbursement. The member reaches their own through
-- `service_request.create` / `service_request.read`, which is WP-I4-01's gate on the request
-- this wraps, and the review is `claim.financial.review` -- the financial stage's own
-- permission, because approving a reimbursement *is* deciding a claim and inventing a second
-- permission for it would be inventing one nobody's role template grants.
--
-- The other half of this insert lives in internal/identity/application/roles.go. A permission
-- in one place and not the other is a permission nobody can hold or one nobody can be given,
-- and db/tests/billing_settlement_test.go checks both halves agree.
INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('settlement.record_payment', 'Settlement ödeme kaydı girişi (banka/ERP referansı)', 'NORMAL')
ON CONFLICT (code) DO NOTHING;

-- ---------------------------------------------------------------------------
-- billing.settlement
-- ---------------------------------------------------------------------------
--
-- One decided batch, one provider, one currency, one figure. The settlement is opened by the
-- outbox handler of `batch.decided` and never by a person: what is owed is the arithmetic of
-- the decisions the payer already took, and a settlement somebody could raise by hand would
-- be a figure with no batch behind it.
--
-- The three money columns are stored rather than derived. `approved_amount` is the batch's
-- approved total copied at the moment the settlement opened -- a copy for the same reason
-- `billing.batch_invoice.submitted_amount` is a copy, because the settlement is a record of
-- what was settled and not a view that silently follows something somebody corrected
-- afterwards. `withheld_amount` is the sum of the open recoveries netted into this
-- settlement, and each of the adjustments it came from is marked in
-- `billing.settlement_recovery` so nothing is netted twice. `payable_amount` is the
-- difference, and the CHECK is what makes "a settlement never exceeds the batch's approved
-- total" a fact about the database.
--
-- `posting_id` and the ERP's side of the story are M9's. They are nullable, unwritten here
-- and deliberately carry no foreign key: the table they will point at does not exist, and a
-- column added later would mean settlement ids that meant different things before and after.
CREATE TABLE billing.settlement (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    -- `ST-YYYYMM-XXXXXXXX`. The tail is forty random bits rather than a sequence, for the
    -- reason a batch reference's tail is: a sequential reference tells a competitor how much
    -- a tenant settled last month.
    reference                text NOT NULL,
    batch_id                 uuid NOT NULL,
    -- Which attempt at settling this batch. A settlement is never edited: a wrong one is
    -- CANCELLED with a reason and version 2 is opened on the same batch.
    version_no               integer NOT NULL DEFAULT 1 CHECK (version_no > 0),
    provider_organization_id uuid NOT NULL,
    payer_organization_id    uuid,
    currency_code            char(3) NOT NULL DEFAULT 'TRY',
    approved_amount          numeric(20,6) NOT NULL,
    withheld_amount          numeric(20,6) NOT NULL DEFAULT 0,
    payable_amount           numeric(20,6) NOT NULL,
    -- The contract's payment term, applied to the day the batch was decided. A contract that
    -- carries no term is `PAYMENT_TERM_MISSING` in the service: this column is NOT NULL
    -- because a settlement with no due date is a settlement nobody can chase.
    due_date                 date NOT NULL,
    settlement_method        text NOT NULL
                             CHECK (settlement_method IN ('BANK_TRANSFER','OFFSET','OTHER')),
    status                   text NOT NULL DEFAULT 'DRAFT'
                             CHECK (status IN (
                                 'DRAFT','PENDING_APPROVAL','APPROVED','POSTED','PAID',
                                 'PARTIALLY_PAID','RECONCILED','CANCELLED'
                             )),
    approved_by              uuid REFERENCES iam.actor(id),
    approved_at              timestamptz,
    -- The second pair of eyes above the tenant's threshold, when one was asked for. It is
    -- separate from `approved_by` because below the threshold there is only one person and
    -- recording them twice would say a check happened that did not.
    checked_by               uuid REFERENCES iam.actor(id),
    -- M9: the accounting posting this settlement produced.
    posting_id               uuid,
    -- Server-maintained: the sum of this settlement's live payment records. It is stored
    -- rather than summed on read because the ceiling below can only be deferred against a
    -- stored figure, and because "how much of this have we paid" is asked on every list row.
    paid_amount              numeric(20,6) NOT NULL DEFAULT 0,
    cancel_reason_code       text,
    cancel_reason_text       text,
    created_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by               uuid,
    updated_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by               uuid,
    row_version              bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_billing_settlement_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_billing_settlement_reference UNIQUE (tenant_id, reference),
    -- One row per attempt. Two settlements calling themselves version 2 of one batch would be
    -- two answers to "what replaced the cancelled one".
    CONSTRAINT uq_billing_settlement_batch_version UNIQUE (tenant_id, batch_id, version_no),
    CONSTRAINT fk_billing_settlement_batch FOREIGN KEY (tenant_id, batch_id)
        REFERENCES billing.batch(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_settlement_provider FOREIGN KEY (tenant_id, provider_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_settlement_payer FOREIGN KEY (tenant_id, payer_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_billing_settlement_reference CHECK (reference ~ '^ST-[0-9]{6}-[A-Z2-7]{8}$'),
    CONSTRAINT ck_billing_settlement_currency CHECK (currency_code ~ '^[A-Z]{3}$'),
    CONSTRAINT ck_billing_settlement_amounts_sign CHECK (
        approved_amount >= 0 AND withheld_amount >= 0 AND payable_amount >= 0
        AND paid_amount >= 0
    ),
    -- **The settlement never exceeds the batch's approved total.** Both halves: the
    -- arithmetic that produced the payable figure, and the inequality on its own, so that a
    -- withheld amount somebody wrote as a negative number cannot lift the payable figure
    -- above what was approved.
    CONSTRAINT ck_billing_settlement_payable CHECK (
        payable_amount = approved_amount - withheld_amount
        AND payable_amount <= approved_amount
    ),
    -- And the payments never exceed the settlement. This is the plain half; the deferred
    -- trigger below is the half that also makes `paid_amount` equal the sum of the records.
    CONSTRAINT ck_billing_settlement_paid CHECK (paid_amount <= payable_amount),
    -- **The status follows the sum.** PAID means the whole payable amount arrived,
    -- PARTIALLY_PAID means some of it did, and neither can be written beside a figure that
    -- says otherwise. A settlement of nought is PAID the moment it is approved and never
    -- PARTIALLY_PAID, which is why the second arm needs the payable amount to be positive.
    CONSTRAINT ck_billing_settlement_paid_status CHECK (
        (status <> 'PAID' OR paid_amount = payable_amount)
        AND (status <> 'PARTIALLY_PAID'
             OR (paid_amount > 0 AND paid_amount < payable_amount))
    ),
    -- An approved settlement says when and by whom. The three statuses in which nobody has
    -- approved it are the draft, the one waiting for approval and the one somebody cancelled.
    CONSTRAINT ck_billing_settlement_approved CHECK (
        ((approved_at IS NULL) = (approved_by IS NULL))
        AND (approved_at IS NOT NULL
             OR status IN ('DRAFT', 'PENDING_APPROVAL', 'CANCELLED'))
    ),
    -- The checker is somebody else. A row naming the same person twice would record a second
    -- pair of eyes that never looked.
    CONSTRAINT ck_billing_settlement_checker CHECK (
        checked_by IS NULL OR checked_by IS DISTINCT FROM approved_by
    ),
    -- A cancelled settlement says why, and nothing else carries a cancellation reason.
    CONSTRAINT ck_billing_settlement_cancel CHECK (
        (status = 'CANCELLED') = (cancel_reason_code IS NOT NULL)
    ),
    CONSTRAINT ck_billing_settlement_cancel_code
        CHECK (cancel_reason_code IS NULL OR cancel_reason_code ~ '^[A-Z][A-Z0-9_.:-]{1,79}$'),
    CONSTRAINT ck_billing_settlement_cancel_text
        CHECK (cancel_reason_text IS NULL OR length(cancel_reason_text) <= 1000)
);

-- **One live settlement per batch.** The handler of `batch.decided` looks first and stops
-- when it finds one; this index is what makes that a guarantee rather than a race between two
-- deliveries that both looked at the same moment. A CANCELLED settlement is outside it, which
-- is exactly what lets the replacement version be opened on the same batch.
CREATE UNIQUE INDEX uq_billing_settlement_live_batch
    ON billing.settlement (tenant_id, batch_id) WHERE status <> 'CANCELLED';

CREATE INDEX ix_billing_settlement_provider
    ON billing.settlement (tenant_id, provider_organization_id, status, due_date);
CREATE INDEX ix_billing_settlement_status
    ON billing.settlement (tenant_id, status, due_date);
-- The keyset the list endpoint pages by.
CREATE INDEX ix_billing_settlement_created
    ON billing.settlement (tenant_id, created_at DESC, id DESC);

SELECT platform.attach_touch_row('billing.settlement'::regclass);
SELECT platform.enable_tenant_rls('billing.settlement'::regclass);

-- ---------------------------------------------------------------------------
-- The freeze of the approved amount
-- ---------------------------------------------------------------------------
--
-- Once a settlement has left DRAFT its figure is the batch's, for ever. Not the approved
-- amount, not the withheld amount, not the difference, not the currency and not the batch it
-- opened on. A settlement whose figure could move afterwards would be a settlement that could
-- quietly stop being what the payer decided.
--
-- What may still move is the lifecycle, the approval, the paid amount and the cancellation --
-- and the due date, which is the one thing a person may legitimately negotiate after the fact.
CREATE OR REPLACE FUNCTION billing.tg_settlement_frozen() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status = 'DRAFT' THEN
        RETURN NEW;
    END IF;
    IF NEW.approved_amount <> OLD.approved_amount
       OR NEW.withheld_amount <> OLD.withheld_amount
       OR NEW.payable_amount <> OLD.payable_amount
       OR NEW.currency_code <> OLD.currency_code
       OR NEW.batch_id <> OLD.batch_id
       OR NEW.version_no <> OLD.version_no
       OR NEW.provider_organization_id <> OLD.provider_organization_id
       OR NEW.payer_organization_id IS DISTINCT FROM OLD.payer_organization_id
    THEN
        RAISE EXCEPTION 'settlement % is % and its figures are frozen', OLD.id, OLD.status
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER tg_billing_settlement_frozen
    BEFORE UPDATE ON billing.settlement
    FOR EACH ROW EXECUTE FUNCTION billing.tg_settlement_frozen();

-- ---------------------------------------------------------------------------
-- billing.settlement_recovery
-- ---------------------------------------------------------------------------
--
-- Which `claim.adjustment` RECOVERY rows this settlement netted, and for how much.
--
-- It exists so that "netted once" is a fact rather than a hope. A recovery is an amount the
-- payer is taking back off a provider; without this table the only way to know whether one
-- had already been withheld would be to sum the settlements and hope none of them had been
-- cancelled halfway through. `uq_billing_settlement_recovery_adjustment` is the whole point:
-- one adjustment is netted into one live settlement and no other.
--
-- It is append-only. A cancelled settlement's rows are deleted rather than kept, because the
-- recovery goes back to being open and a row that stayed would keep it marked for ever; that
-- deletion is the one write this table sees after an insert, and it happens in the same
-- transaction as the cancellation.
CREATE TABLE billing.settlement_recovery (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id     uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    settlement_id uuid NOT NULL,
    claim_id      uuid NOT NULL,
    adjustment_id uuid NOT NULL,
    amount        numeric(20,6) NOT NULL CHECK (amount >= 0),
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by    uuid,
    CONSTRAINT uq_billing_settlement_recovery_id_tenant UNIQUE (tenant_id, id),
    -- One adjustment, one settlement. Two rows would net the same recovery twice.
    CONSTRAINT uq_billing_settlement_recovery_adjustment UNIQUE (tenant_id, adjustment_id),
    CONSTRAINT fk_billing_settlement_recovery_settlement FOREIGN KEY (tenant_id, settlement_id)
        REFERENCES billing.settlement(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_settlement_recovery_claim FOREIGN KEY (tenant_id, claim_id)
        REFERENCES claim.claim(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_settlement_recovery_adjustment FOREIGN KEY (tenant_id, adjustment_id)
        REFERENCES claim.adjustment(tenant_id, id) ON DELETE RESTRICT
);

CREATE INDEX ix_billing_settlement_recovery_settlement
    ON billing.settlement_recovery (tenant_id, settlement_id, created_at, id);
SELECT platform.enable_tenant_rls('billing.settlement_recovery'::regclass);

-- ---------------------------------------------------------------------------
-- billing.payment_record
-- ---------------------------------------------------------------------------
--
-- A finance fact with an external reference: somebody outside this system moved money, and
-- this is what they said about it. KAPSORA transferred nothing (v1.2 4.3).
--
-- It is append-only. A record entered wrongly becomes DISPUTED and stays on the table, with
-- its reference, because "the bank says it sent this and we say it did not" is exactly the
-- conversation the row exists to support -- and a deleted row is one nobody can have that
-- conversation about.
--
-- `provider_organization_id` is a copy of the settlement's, and it is a copy so that the
-- uniqueness rule can be written at all: **one external reference per provider**. A bank
-- reference quoted twice is the same transfer entered twice, and the second entry is how a
-- settlement comes to look paid when half of it was not.
CREATE TABLE billing.payment_record (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    settlement_id            uuid NOT NULL,
    provider_organization_id uuid NOT NULL,
    -- The bank's or the ERP's own reference. It is what makes this a record of something
    -- rather than an assertion.
    external_reference       text NOT NULL,
    amount                   numeric(20,6) NOT NULL CHECK (amount > 0),
    currency_code            char(3) NOT NULL DEFAULT 'TRY',
    paid_at                  timestamptz NOT NULL,
    -- MANUAL is a person typing what the bank statement says. ERP is M9's
    -- `PaymentConfirmation` flowing back from the accounting system; it is in the word list
    -- now so the column does not have to change when that integration lands.
    source                   text NOT NULL DEFAULT 'MANUAL'
                             CHECK (source IN ('MANUAL', 'ERP')),
    status                   text NOT NULL DEFAULT 'RECORDED'
                             CHECK (status IN ('RECORDED', 'RECONCILED', 'DISPUTED')),
    recorded_by              uuid REFERENCES iam.actor(id),
    notes                    text,
    created_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by               uuid,
    updated_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by               uuid,
    row_version              bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_billing_payment_record_id_tenant UNIQUE (tenant_id, id),
    -- **One external reference per provider.**
    CONSTRAINT uq_billing_payment_record_reference
        UNIQUE (tenant_id, provider_organization_id, external_reference),
    CONSTRAINT fk_billing_payment_record_settlement FOREIGN KEY (tenant_id, settlement_id)
        REFERENCES billing.settlement(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_payment_record_provider
        FOREIGN KEY (tenant_id, provider_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_billing_payment_record_reference
        CHECK (external_reference ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$'),
    CONSTRAINT ck_billing_payment_record_currency CHECK (currency_code ~ '^[A-Z]{3}$'),
    CONSTRAINT ck_billing_payment_record_notes
        CHECK (notes IS NULL OR length(notes) <= 2000)
);

CREATE INDEX ix_billing_payment_record_settlement
    ON billing.payment_record (tenant_id, settlement_id, paid_at, id);
CREATE INDEX ix_billing_payment_record_created
    ON billing.payment_record (tenant_id, created_at DESC, id DESC);

SELECT platform.attach_touch_row('billing.payment_record'::regclass);
SELECT platform.enable_tenant_rls('billing.payment_record'::regclass);

-- ---------------------------------------------------------------------------
-- The payment ceiling, deferred
-- ---------------------------------------------------------------------------
--
-- Two facts about another row, which is why neither can be a CHECK on `billing.payment_record`:
--
--   * the settlement's stored `paid_amount` is exactly the sum of its live records. DISPUTED
--     rows are outside the sum -- a disputed record is money nobody agrees arrived -- and
--     RECORDED and RECONCILED are inside it;
--   * that sum does not exceed `payable_amount`. The plain CHECK on the settlement says the
--     same thing about the stored figure; this is what stops the stored figure and the rows
--     drifting apart.
--
-- DEFERRABLE INITIALLY DEFERRED because the service writes the record and moves the
-- settlement's figure in one transaction, and the moment between the two is not a state
-- anybody should be judged on. Deferring is also what makes the concurrent double record
-- safe: both transactions take the settlement row with SELECT ... FOR UPDATE before they
-- write, so the second one recomputes against the first one's committed figure.
--
-- The currency half is a plain comparison and could have been anywhere; it is here because a
-- record denominated differently from the settlement is the same kind of mistake as one that
-- is too large, and one refusal is easier to read than two.
CREATE OR REPLACE FUNCTION billing.tg_payment_record_ceiling() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    target      uuid;
    tenant      uuid;
    settlement  record;
    live_total  numeric;
BEGIN
    IF TG_OP = 'DELETE' THEN
        target := OLD.settlement_id;
        tenant := OLD.tenant_id;
    ELSE
        target := NEW.settlement_id;
        tenant := NEW.tenant_id;
    END IF;

    SELECT s.payable_amount, s.paid_amount, s.currency_code, s.provider_organization_id
      INTO settlement
      FROM billing.settlement s
     WHERE s.tenant_id = tenant AND s.id = target;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'settlement % not found in tenant %', target, tenant
            USING ERRCODE = 'foreign_key_violation';
    END IF;

    IF TG_OP <> 'DELETE' THEN
        IF NEW.currency_code <> settlement.currency_code THEN
            RAISE EXCEPTION 'payment record currency % does not match settlement currency %',
                NEW.currency_code, settlement.currency_code
                USING ERRCODE = 'check_violation';
        END IF;
        IF NEW.provider_organization_id <> settlement.provider_organization_id THEN
            RAISE EXCEPTION 'payment record names provider %, the settlement names %',
                NEW.provider_organization_id, settlement.provider_organization_id
                USING ERRCODE = 'check_violation';
        END IF;
    END IF;

    SELECT COALESCE(sum(p.amount), 0) INTO live_total
      FROM billing.payment_record p
     WHERE p.tenant_id = tenant AND p.settlement_id = target
       AND p.status <> 'DISPUTED';

    IF live_total > settlement.payable_amount THEN
        RAISE EXCEPTION 'payment records on settlement % total %, the payable amount is %',
            target, live_total, settlement.payable_amount
            USING ERRCODE = 'check_violation';
    END IF;
    IF live_total <> settlement.paid_amount THEN
        RAISE EXCEPTION 'settlement % records total % but its paid amount says %',
            target, live_total, settlement.paid_amount
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER tg_billing_payment_record_ceiling
    AFTER INSERT OR UPDATE OR DELETE ON billing.payment_record
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION billing.tg_payment_record_ceiling();

-- The other end of the same invariant. A statement that moved `paid_amount` without touching
-- a record would otherwise leave the stored figure and the rows disagreeing, and the
-- disagreement would only surface the next time somebody entered a record.
CREATE OR REPLACE FUNCTION billing.tg_settlement_paid_total() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    live_total numeric;
BEGIN
    SELECT COALESCE(sum(p.amount), 0) INTO live_total
      FROM billing.payment_record p
     WHERE p.tenant_id = NEW.tenant_id AND p.settlement_id = NEW.id
       AND p.status <> 'DISPUTED';
    IF live_total <> NEW.paid_amount THEN
        RAISE EXCEPTION 'settlement % records total % but its paid amount says %',
            NEW.id, live_total, NEW.paid_amount
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER tg_billing_settlement_paid_total
    AFTER INSERT OR UPDATE OF paid_amount ON billing.settlement
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION billing.tg_settlement_paid_total();

-- ---------------------------------------------------------------------------
-- billing.reimbursement
-- ---------------------------------------------------------------------------
--
-- The member paid for something themselves and wants the money back. The request is
-- WP-I4-01's `REIMBURSEMENT` service request, with its own gate and its own eligibility; this
-- table is what the payer's finance side needs beside it -- the receipt, the amount, the
-- decision, and a reference to an account KAPSORA cannot read.
--
-- **The IBAN.** `bank_account_ref_enc` holds the platform cipher's envelope under
-- `crypto.PurposeBankAccount`, and `bank_account_masked` holds the last four characters. That
-- is every place in this schema an account number exists. There is no blind index -- nothing
-- searches for a member by their bank account, and an index would be the one structure that
-- made two members' accounts comparable without anybody decrypting either. There is no CHECK
-- on the ciphertext beyond its length either, because a constraint that could recognise an
-- IBAN would be a constraint that had seen one.
--
-- `receipt_sha256` is the digest of the receipt document, copied from `document.object` at
-- submission. It is a copy so that the duplicate check is one read of this table rather than
-- a join across every member's documents, and because a document purged by retention must not
-- take the duplicate history with it.
CREATE TABLE billing.reimbursement (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
    -- `RB-YYYYMM-XXXXXXXX`, on the same shape and for the same reason as the others.
    reference                text NOT NULL,
    person_id                uuid NOT NULL,
    enrollment_id            uuid NOT NULL,
    -- WP-I4-01's request of type REIMBURSEMENT: what the member asked for, when, and for
    -- which service. It is the source of this row and the source of the claim approval
    -- creates.
    service_request_id       uuid NOT NULL,
    -- WP-I7-01's REIMBURSEMENT claim, created at approval and never before: a claim raised at
    -- submission would be a claim the payer never agreed to.
    claim_id                 uuid,
    receipt_document_id      uuid NOT NULL,
    receipt_sha256           bytea NOT NULL,
    -- Copied from the request so the duplicate rule and the ceiling can both be answered from
    -- this table.
    --
    -- The provider is NOT NULL, and that is a real restriction rather than an oversight: an
    -- approved reimbursement creates a `claim.claim`, whose `provider_organization_id` is
    -- NOT NULL because a claim with no provider is a claim nobody can settle. A member who
    -- paid somebody the directory has never heard of cannot be reimbursed through this table
    -- until that somebody is in the directory, and the service says so by name rather than
    -- letting the row be written and the approval fail six weeks later.
    service_definition_id    uuid NOT NULL,
    service_date             date NOT NULL,
    provider_organization_id uuid NOT NULL,
    requested_amount         numeric(20,6) NOT NULL CHECK (requested_amount > 0),
    -- NULL until somebody decides. A rejection writes zero rather than leaving it null,
    -- because "rejected" and "approved for nothing" are the same money and a report that had
    -- to treat them differently would be a report with a special case in it.
    approved_amount          numeric(20,6),
    currency_code            char(3) NOT NULL DEFAULT 'TRY',
    bank_account_ref_enc     bytea NOT NULL,
    bank_account_masked      text NOT NULL,
    status                   text NOT NULL DEFAULT 'DRAFT'
                             CHECK (status IN (
                                 'DRAFT','SUBMITTED','UNDER_REVIEW','APPROVED',
                                 'PARTIALLY_APPROVED','REJECTED','PAYMENT_ORDERED','PAID',
                                 'CANCELLED'
                             )),
    -- The earlier request this one repeats, when the duplicate check found one. It is stored
    -- rather than only reported, because "we refused this as a duplicate of that" is a
    -- decision a member appeals.
    duplicate_of_id          uuid,
    decision_reason_code     text,
    decision_reason_text     text,
    decided_by               uuid REFERENCES iam.actor(id),
    decided_at               timestamptz,
    submitted_at             timestamptz,
    payment_reference        text,
    paid_at                  timestamptz,
    created_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by               uuid,
    updated_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by               uuid,
    row_version              bigint NOT NULL DEFAULT 1,
    CONSTRAINT uq_billing_reimbursement_id_tenant UNIQUE (tenant_id, id),
    CONSTRAINT uq_billing_reimbursement_reference UNIQUE (tenant_id, reference),
    -- One reimbursement per request: the request is the thing the member submitted, and two
    -- rows against it would be two answers to one question.
    CONSTRAINT uq_billing_reimbursement_request UNIQUE (tenant_id, service_request_id),
    CONSTRAINT fk_billing_reimbursement_person FOREIGN KEY (tenant_id, person_id)
        REFERENCES party.person(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_reimbursement_enrollment FOREIGN KEY (tenant_id, enrollment_id)
        REFERENCES benefit.enrollment(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_reimbursement_request FOREIGN KEY (tenant_id, service_request_id)
        REFERENCES service.service_request(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_reimbursement_claim FOREIGN KEY (tenant_id, claim_id)
        REFERENCES claim.claim(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_reimbursement_document FOREIGN KEY (tenant_id, receipt_document_id)
        REFERENCES document.object(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_reimbursement_service FOREIGN KEY (tenant_id, service_definition_id)
        REFERENCES catalog.service_definition(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_reimbursement_provider
        FOREIGN KEY (tenant_id, provider_organization_id)
        REFERENCES directory.tenant_organization(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_billing_reimbursement_duplicate FOREIGN KEY (tenant_id, duplicate_of_id)
        REFERENCES billing.reimbursement(tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT ck_billing_reimbursement_reference CHECK (reference ~ '^RB-[0-9]{6}-[A-Z2-7]{8}$'),
    CONSTRAINT ck_billing_reimbursement_currency CHECK (currency_code ~ '^[A-Z]{3}$'),
    CONSTRAINT ck_billing_reimbursement_receipt_hash
        CHECK (octet_length(receipt_sha256) = 32),
    -- The envelope is never empty and is never plaintext-sized by accident: the cipher's
    -- envelope carries a version byte, a key id and a nonce before a single byte of an IBAN,
    -- so anything shorter than that is not one.
    CONSTRAINT ck_billing_reimbursement_bank_ref
        CHECK (octet_length(bank_account_ref_enc) >= 16),
    -- **Four characters, and only four.** A mask that could be longer would be a mask that
    -- could one day hold the whole number.
    CONSTRAINT ck_billing_reimbursement_bank_mask
        CHECK (bank_account_masked ~ '^[0-9A-Z]{4}$'),
    -- Approving more than was asked for is not a partial approval and not a full one; it is a
    -- figure nobody requested.
    CONSTRAINT ck_billing_reimbursement_approved CHECK (
        approved_amount IS NULL
        OR (approved_amount >= 0 AND approved_amount <= requested_amount)
    ),
    -- **A decision is one fact.** Every status past the review carries the amount, the person
    -- and the moment; a rejection and a partial approval also carry the reason, because those
    -- are the two a member appeals and an appeal is answerable only from a code somebody can
    -- count.
    CONSTRAINT ck_billing_reimbursement_decision CHECK (
        status NOT IN ('APPROVED','PARTIALLY_APPROVED','REJECTED','PAYMENT_ORDERED','PAID')
        OR (approved_amount IS NOT NULL AND decided_by IS NOT NULL AND decided_at IS NOT NULL)
    ),
    CONSTRAINT ck_billing_reimbursement_decision_reason CHECK (
        status NOT IN ('PARTIALLY_APPROVED','REJECTED') OR decision_reason_code IS NOT NULL
    ),
    CONSTRAINT ck_billing_reimbursement_reason_code
        CHECK (decision_reason_code IS NULL
               OR decision_reason_code ~ '^[A-Z][A-Z0-9_.:-]{1,79}$'),
    CONSTRAINT ck_billing_reimbursement_reason_text
        CHECK (decision_reason_text IS NULL OR length(decision_reason_text) <= 1000),
    -- A rejected reimbursement approves nothing, and an approved one approves something. The
    -- two are the same column and this is what keeps them apart.
    CONSTRAINT ck_billing_reimbursement_rejected CHECK (
        (status <> 'REJECTED' OR approved_amount = 0)
        AND (status NOT IN ('APPROVED','PAYMENT_ORDERED','PAID') OR approved_amount > 0)
    ),
    -- A submitted reimbursement says when. The two states in which it was never submitted are
    -- the draft and the draft somebody withdrew.
    CONSTRAINT ck_billing_reimbursement_submitted CHECK (
        submitted_at IS NOT NULL OR status IN ('DRAFT', 'CANCELLED')
    ),
    -- A paid one says with which reference and when; nothing before PAID carries either.
    CONSTRAINT ck_billing_reimbursement_paid CHECK (
        (status = 'PAID') = (payment_reference IS NOT NULL AND paid_at IS NOT NULL)
    ),
    CONSTRAINT ck_billing_reimbursement_payment_reference
        CHECK (payment_reference IS NULL
               OR payment_reference ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$'),
    -- A claim exists exactly from the approval onwards. A claim on a rejected reimbursement
    -- would be a claim for money nobody agreed to pay.
    CONSTRAINT ck_billing_reimbursement_claim CHECK (
        claim_id IS NULL
        OR status IN ('APPROVED','PARTIALLY_APPROVED','PAYMENT_ORDERED','PAID')
    ),
    CONSTRAINT ck_billing_reimbursement_duplicate_self
        CHECK (duplicate_of_id IS DISTINCT FROM id)
);

-- **One live claim per receipt.** The same person cannot have two open reimbursements against
-- one receipt document, however carefully the service looks first. A rejected or cancelled one
-- is outside the index: a receipt refused on a technicality can be submitted again.
CREATE UNIQUE INDEX uq_billing_reimbursement_live_receipt
    ON billing.reimbursement (tenant_id, person_id, receipt_document_id)
 WHERE status NOT IN ('REJECTED', 'CANCELLED');

-- The two reads the duplicate check makes: this person's receipts by digest, and this
-- person's spending at one provider on one day.
CREATE INDEX ix_billing_reimbursement_receipt_hash
    ON billing.reimbursement (tenant_id, person_id, receipt_sha256);
CREATE INDEX ix_billing_reimbursement_triple
    ON billing.reimbursement (tenant_id, person_id, service_date, provider_organization_id);
-- The member's own list, and the keyset both lists page by.
CREATE INDEX ix_billing_reimbursement_person
    ON billing.reimbursement (tenant_id, person_id, created_at DESC, id DESC);
CREATE INDEX ix_billing_reimbursement_status
    ON billing.reimbursement (tenant_id, status, created_at DESC, id DESC);

SELECT platform.attach_touch_row('billing.reimbursement'::regclass);
SELECT platform.enable_tenant_rls('billing.reimbursement'::regclass);

-- ---------------------------------------------------------------------------
-- notification: masked_account joins the safe-variable catalogue
-- ---------------------------------------------------------------------------
--
-- `reimbursement.paid` has to be able to say which account the money went to, and the only
-- form of that answer this platform will ever hold is four characters. Folding it into
-- `reference_no` would have worked and would have been a lie: a reference is a document
-- number people quote, and a slot that carried both would be a slot nobody could reason about.
--
-- Its rule is the narrowest in the catalogue -- exactly four upper-case alphanumerics, which
-- is what `ck_billing_reimbursement_bank_mask` stores -- so there is no shape of a whole
-- account number, a name or a sentence that fits in it. The Go side
-- (internal/notification/domain) states the same rule as a value rule, and the CHECK is
-- repeated here rather than relaxed, exactly as migration 000039 did for `property_name`,
-- because the catalogue is what makes "there is no slot an IBAN could be supplied under" a
-- fact about the schema and not only about the code.
ALTER TABLE notification.template
    DROP CONSTRAINT ck_notification_template_variables,
    ADD CONSTRAINT ck_notification_template_variables CHECK (
        cardinality(declared_variables) <= 12
        AND declared_variables <@ ARRAY[
            'given_name','reference_no','status_code','event_date','expires_at',
            'amount','currency','provider_name','program_name','property_name',
            'masked_account','deep_link']::text[]
    );

SELECT platform.grant_app_schema_usage('billing');
