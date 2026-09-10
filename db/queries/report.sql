-- WP-I7-05: the provider statement, the daily reconciliation, the operations dashboard and the
-- exports a person is allowed to take out of the system.
--
-- One rule runs through every statement in this file: **the server sums, and Go renders.** Every
-- figure below is a `sum()` over exact decimals inside PostgreSQL, handed back as canonical
-- decimal text through `trim_scale(x)::text`, and there is no read in this module that returns a
-- column of amounts for Go to add up. A total assembled from a page of rows is a total that is
-- wrong the moment there is a second page, and a total that passed through a float is wrong
-- immediately.
--
-- The reads reach into `billing`, `claim`, `workflow`, `directory`, `catalog` and `document`.
-- That is deliberate and it is read-only: a statement is a claim about invoices, batches,
-- settlements and payments at once, and asking four services for four halves of one page would be
-- four round trips that could disagree with each other. The only write in this file that touches
-- another module's table is `MarkSettlementsReconciled`, and it is the reconciliation run's own
-- act -- the database refuses RECONCILED on anything short of fully paid, so it cannot be used
-- for anything else.

-- ---------------------------------------------------------------------------
-- The provider statement
-- ---------------------------------------------------------------------------

-- name: GetProviderStatementTotals :one
-- Every figure of one provider's statement, for one period and one currency, in **one query**.
--
-- Each column is a scalar subquery over its own table with its own natural date, so each one is
-- reproducible by a filter a screen can apply: the invoiced total is the invoices dated in the
-- period, the decision columns are the batches decided in it, and the settlement columns are the
-- settlements *due* in it. A statement that mixed those windows would be a page nobody could tie
-- back to any list.
--
-- `open_balance` is settled minus paid over the same settlements, which is what the provider is
-- actually asking when it opens this screen.
SELECT
    (SELECT trim_scale(COALESCE(sum(i.payable_amount), 0))::text
       FROM billing.invoice i
      WHERE i.tenant_id = sqlc.arg('tenant_id')
        AND i.provider_organization_id = sqlc.arg('provider_organization_id')
        AND i.currency_code = sqlc.arg('currency_code')
        AND i.invoice_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to')
        AND i.status NOT IN ('DRAFT', 'CANCELLED')) AS invoiced_total,
    (SELECT trim_scale(COALESCE(sum(b.approved_total), 0))::text
       FROM billing.batch b
      WHERE b.tenant_id = sqlc.arg('tenant_id')
        AND b.provider_organization_id = sqlc.arg('provider_organization_id')
        AND b.currency_code = sqlc.arg('currency_code')
        AND b.status IN ('DECIDED', 'SETTLING', 'CLOSED')
        AND b.decided_at::date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS approved_total,
    (SELECT trim_scale(COALESCE(sum(b.cut_total), 0))::text
       FROM billing.batch b
      WHERE b.tenant_id = sqlc.arg('tenant_id')
        AND b.provider_organization_id = sqlc.arg('provider_organization_id')
        AND b.currency_code = sqlc.arg('currency_code')
        AND b.status IN ('DECIDED', 'SETTLING', 'CLOSED')
        AND b.decided_at::date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS cut_total,
    (SELECT trim_scale(COALESCE(sum(b.returned_total), 0))::text
       FROM billing.batch b
      WHERE b.tenant_id = sqlc.arg('tenant_id')
        AND b.provider_organization_id = sqlc.arg('provider_organization_id')
        AND b.currency_code = sqlc.arg('currency_code')
        AND b.status IN ('DECIDED', 'SETTLING', 'CLOSED')
        AND b.decided_at::date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS returned_total,
    (SELECT trim_scale(COALESCE(sum(b.rejected_total), 0))::text
       FROM billing.batch b
      WHERE b.tenant_id = sqlc.arg('tenant_id')
        AND b.provider_organization_id = sqlc.arg('provider_organization_id')
        AND b.currency_code = sqlc.arg('currency_code')
        AND b.status IN ('DECIDED', 'SETTLING', 'CLOSED')
        AND b.decided_at::date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS rejected_total,
    (SELECT trim_scale(COALESCE(sum(s.payable_amount), 0))::text
       FROM billing.settlement s
      WHERE s.tenant_id = sqlc.arg('tenant_id')
        AND s.provider_organization_id = sqlc.arg('provider_organization_id')
        AND s.currency_code = sqlc.arg('currency_code')
        AND s.status <> 'CANCELLED'
        AND s.due_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS settled_total,
    (SELECT trim_scale(COALESCE(sum(s.paid_amount), 0))::text
       FROM billing.settlement s
      WHERE s.tenant_id = sqlc.arg('tenant_id')
        AND s.provider_organization_id = sqlc.arg('provider_organization_id')
        AND s.currency_code = sqlc.arg('currency_code')
        AND s.status <> 'CANCELLED'
        AND s.due_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS paid_total,
    (SELECT trim_scale(COALESCE(sum(s.payable_amount - s.paid_amount), 0))::text
       FROM billing.settlement s
      WHERE s.tenant_id = sqlc.arg('tenant_id')
        AND s.provider_organization_id = sqlc.arg('provider_organization_id')
        AND s.currency_code = sqlc.arg('currency_code')
        AND s.status <> 'CANCELLED'
        AND s.due_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS open_balance,
    (SELECT count(*)
       FROM billing.invoice i
      WHERE i.tenant_id = sqlc.arg('tenant_id')
        AND i.provider_organization_id = sqlc.arg('provider_organization_id')
        AND i.currency_code = sqlc.arg('currency_code')
        AND i.invoice_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to')
        AND i.status NOT IN ('DRAFT', 'CANCELLED')) AS invoice_count,
    (SELECT count(*)
       FROM billing.settlement s
      WHERE s.tenant_id = sqlc.arg('tenant_id')
        AND s.provider_organization_id = sqlc.arg('provider_organization_id')
        AND s.currency_code = sqlc.arg('currency_code')
        AND s.status <> 'CANCELLED'
        AND s.due_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS settlement_count,
    (SELECT o.display_name
       FROM directory.tenant_organization t
       JOIN directory.organization o ON o.id = t.organization_id
      WHERE t.tenant_id = sqlc.arg('tenant_id')
        AND t.id = sqlc.arg('provider_organization_id')) AS provider_name;

-- name: ListProviderStatementInvoices :many
-- The invoice rows behind the totals: what was billed, which icmal it went into, what the payer
-- decided and how much of the settlement that carried it has been paid. Ordered oldest first,
-- because a statement is read down a page rather than paged through.
SELECT i.id, i.invoice_number, i.invoice_date, i.status, i.currency_code,
       trim_scale(i.payable_amount)::text AS payable_amount,
       trim_scale(i.tax_amount)::text AS tax_amount,
       b.id AS batch_id, b.reference AS batch_reference, b.status AS batch_status,
       bi.decision AS batch_decision,
       trim_scale(COALESCE(bi.approved_amount, 0))::text AS approved_amount,
       s.id AS settlement_id, s.reference AS settlement_reference, s.due_date,
       trim_scale(COALESCE(s.paid_amount, 0))::text AS settlement_paid_amount
  FROM billing.invoice i
  LEFT JOIN billing.batch b ON b.tenant_id = i.tenant_id AND b.id = i.batch_id
  LEFT JOIN billing.batch_invoice bi
         ON bi.tenant_id = i.tenant_id AND bi.invoice_id = i.id AND bi.batch_id = b.id
  LEFT JOIN billing.settlement s
         ON s.tenant_id = b.tenant_id AND s.batch_id = b.id AND s.status <> 'CANCELLED'
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND i.provider_organization_id = sqlc.arg('provider_organization_id')
   AND i.currency_code = sqlc.arg('currency_code')
   AND i.invoice_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to')
   AND i.status NOT IN ('DRAFT', 'CANCELLED')
 ORDER BY i.invoice_date, i.id
 LIMIT sqlc.arg('row_limit');

-- name: ListProviderStatementSettlements :many
-- The settlement rows behind the totals, with the payments recorded against each. `paid_amount`
-- is the settlement's own stored figure and `payment_count` is how many records make it up:
-- a screen that showed the sum without the count could not tell one transfer from six.
SELECT s.id, s.reference, s.batch_id, s.due_date, s.status, s.currency_code,
       trim_scale(s.approved_amount)::text AS approved_amount,
       trim_scale(s.withheld_amount)::text AS withheld_amount,
       trim_scale(s.payable_amount)::text AS payable_amount,
       trim_scale(s.paid_amount)::text AS paid_amount,
       trim_scale(s.payable_amount - s.paid_amount)::text AS open_amount,
       b.reference AS batch_reference,
       (SELECT count(*) FROM billing.payment_record p
         WHERE p.tenant_id = s.tenant_id AND p.settlement_id = s.id
           AND p.status <> 'DISPUTED') AS payment_count,
       (SELECT max(p.paid_at) FROM billing.payment_record p
         WHERE p.tenant_id = s.tenant_id AND p.settlement_id = s.id
           AND p.status <> 'DISPUTED') AS last_paid_at
  FROM billing.settlement s
  JOIN billing.batch b ON b.tenant_id = s.tenant_id AND b.id = s.batch_id
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.provider_organization_id = sqlc.arg('provider_organization_id')
   AND s.currency_code = sqlc.arg('currency_code')
   AND s.status <> 'CANCELLED'
   AND s.due_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to')
 ORDER BY s.due_date, s.id
 LIMIT sqlc.arg('row_limit');

-- ---------------------------------------------------------------------------
-- Reconciliation
-- ---------------------------------------------------------------------------

-- name: ListReconciliationCurrencies :many
-- The currencies that moved in the period. The job runs one run per currency rather than one run
-- summing all of them, because two currencies added together is not a figure.
SELECT DISTINCT s.currency_code
  FROM billing.settlement s
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.status <> 'CANCELLED'
   AND s.due_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to')
 ORDER BY s.currency_code;

-- name: ListReconciliationProviders :many
-- The providers with any settlement due in the period. A provider with no movement gets no run:
-- a table full of all-zero runs is a table nobody reads.
SELECT DISTINCT s.provider_organization_id
  FROM billing.settlement s
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.currency_code = sqlc.arg('currency_code')
   AND s.status <> 'CANCELLED'
   AND s.due_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to')
 ORDER BY s.provider_organization_id;

-- name: ComputeReconciliationTotals :one
-- Everything one run compares, in **one query**.
--
-- `provider_organization_id` is passed as a nullable argument and every arm reads
-- "this provider, or every provider when none was named". That is what makes the TENANT run and
-- the PROVIDER run the same arithmetic over a different scope rather than two arithmetics that
-- could drift apart -- and it is why a TENANT run's totals are exactly the sum of its PROVIDER
-- runs', which is a property a test can check.
SELECT
    (SELECT trim_scale(COALESCE(sum(i.payable_amount), 0))::text
       FROM billing.invoice i
      WHERE i.tenant_id = sqlc.arg('tenant_id')
        AND i.currency_code = sqlc.arg('currency_code')
        AND (sqlc.narg('provider_organization_id')::uuid IS NULL
             OR i.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
        AND i.invoice_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to')
        AND i.status NOT IN ('DRAFT', 'CANCELLED')) AS invoiced_total,
    (SELECT trim_scale(COALESCE(sum(b.approved_total), 0))::text
       FROM billing.batch b
      WHERE b.tenant_id = sqlc.arg('tenant_id')
        AND b.currency_code = sqlc.arg('currency_code')
        AND (sqlc.narg('provider_organization_id')::uuid IS NULL
             OR b.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
        AND b.status IN ('DECIDED', 'SETTLING', 'CLOSED')
        AND b.decided_at::date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS approved_total,
    (SELECT trim_scale(COALESCE(sum(b.cut_total), 0))::text
       FROM billing.batch b
      WHERE b.tenant_id = sqlc.arg('tenant_id')
        AND b.currency_code = sqlc.arg('currency_code')
        AND (sqlc.narg('provider_organization_id')::uuid IS NULL
             OR b.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
        AND b.status IN ('DECIDED', 'SETTLING', 'CLOSED')
        AND b.decided_at::date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS cut_total,
    (SELECT trim_scale(COALESCE(sum(b.returned_total), 0))::text
       FROM billing.batch b
      WHERE b.tenant_id = sqlc.arg('tenant_id')
        AND b.currency_code = sqlc.arg('currency_code')
        AND (sqlc.narg('provider_organization_id')::uuid IS NULL
             OR b.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
        AND b.status IN ('DECIDED', 'SETTLING', 'CLOSED')
        AND b.decided_at::date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS returned_total,
    (SELECT trim_scale(COALESCE(sum(b.rejected_total), 0))::text
       FROM billing.batch b
      WHERE b.tenant_id = sqlc.arg('tenant_id')
        AND b.currency_code = sqlc.arg('currency_code')
        AND (sqlc.narg('provider_organization_id')::uuid IS NULL
             OR b.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
        AND b.status IN ('DECIDED', 'SETTLING', 'CLOSED')
        AND b.decided_at::date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS rejected_total,
    (SELECT trim_scale(COALESCE(sum(s.payable_amount), 0))::text
       FROM billing.settlement s
      WHERE s.tenant_id = sqlc.arg('tenant_id')
        AND s.currency_code = sqlc.arg('currency_code')
        AND (sqlc.narg('provider_organization_id')::uuid IS NULL
             OR s.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
        AND s.status <> 'CANCELLED'
        AND s.due_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS settled_total,
    (SELECT trim_scale(COALESCE(sum(s.paid_amount), 0))::text
       FROM billing.settlement s
      WHERE s.tenant_id = sqlc.arg('tenant_id')
        AND s.currency_code = sqlc.arg('currency_code')
        AND (sqlc.narg('provider_organization_id')::uuid IS NULL
             OR s.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
        AND s.status <> 'CANCELLED'
        AND s.due_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS paid_total,
    -- What is still open, subtracted by PostgreSQL rather than by Go. It is a column of its own
    -- rather than a difference the service works out, because `ck_billing_reconciliation_run_open`
    -- is going to check it against the other two and an exact decimal is not a thing to compute
    -- twice in two languages.
    (SELECT trim_scale(COALESCE(sum(s.payable_amount - s.paid_amount), 0))::text
       FROM billing.settlement s
      WHERE s.tenant_id = sqlc.arg('tenant_id')
        AND s.currency_code = sqlc.arg('currency_code')
        AND (sqlc.narg('provider_organization_id')::uuid IS NULL
             OR s.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
        AND s.status <> 'CANCELLED'
        AND s.due_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS open_total,
    (SELECT count(*)
       FROM billing.settlement s
      WHERE s.tenant_id = sqlc.arg('tenant_id')
        AND s.currency_code = sqlc.arg('currency_code')
        AND (sqlc.narg('provider_organization_id')::uuid IS NULL
             OR s.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
        AND s.status <> 'CANCELLED'
        AND s.due_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to'))
        AS settlement_count;

-- name: ListReconciliationDifferences :many
-- The settlements the run disagrees with, one row each: the reference a person finds on their own
-- screen, what was expected, what arrived, and which kind of disagreement it is.
--
-- Two kinds are produced here and a third is M9's:
--
--   * `UNDERPAID` -- the due date has passed and the settlement is short. This is the ordinary
--     one and it is what an operator chases;
--   * `PAID_SUM_MISMATCH` -- the settlement's stored `paid_amount` is not the sum of its live
--     payment records. That is KAPSORA disagreeing with itself, and it should never happen:
--     WP-I7-04's deferred trigger exists to stop it, and this is the sweep that would notice if
--     it ever did.
--
-- A settlement that is not yet due and not yet paid is not a difference. It is a settlement
-- waiting for its date, and a run that called it one would raise a work item every night for
-- every unpaid invoice in the system.
SELECT s.id, s.reference, s.due_date, s.status,
       trim_scale(s.payable_amount)::text AS expected_amount,
       trim_scale(s.paid_amount)::text AS actual_amount,
       trim_scale(s.payable_amount - s.paid_amount)::text AS difference_amount,
       CASE
           WHEN s.paid_amount <> COALESCE((
                    SELECT sum(p.amount) FROM billing.payment_record p
                     WHERE p.tenant_id = s.tenant_id AND p.settlement_id = s.id
                       AND p.status <> 'DISPUTED'), 0)
               THEN 'PAID_SUM_MISMATCH'
           ELSE 'UNDERPAID'
       END AS difference_kind
  FROM billing.settlement s
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.currency_code = sqlc.arg('currency_code')
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR s.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND s.status <> 'CANCELLED'
   AND s.due_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to')
   AND s.due_date <= sqlc.arg('as_of')
   AND (s.paid_amount <> s.payable_amount
        OR s.paid_amount <> COALESCE((
               SELECT sum(p.amount) FROM billing.payment_record p
                WHERE p.tenant_id = s.tenant_id AND p.settlement_id = s.id
                  AND p.status <> 'DISPUTED'), 0))
 ORDER BY s.due_date, s.id
 LIMIT sqlc.arg('row_limit');

-- name: NextReconciliationRunNo :one
-- The next attempt at this scope, period and currency. It is read inside the run's own
-- transaction and the unique index is underneath it, so two schedulers that both believe they
-- lead produce runs one and two rather than one run twice.
SELECT COALESCE(max(r.run_no), 0) + 1 AS run_no
  FROM billing.reconciliation_run r
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.scope = sqlc.arg('scope')
   AND r.provider_organization_id IS NOT DISTINCT FROM sqlc.narg('provider_organization_id')::uuid
   AND r.period_from = sqlc.arg('period_from')
   AND r.period_to = sqlc.arg('period_to')
   AND r.currency_code = sqlc.arg('currency_code');

-- name: CreateReconciliationRun :one
-- `erp_total` comes back as the empty string when the ERP has not spoken, rather than as a
-- null the generated code would have to carry as a pointer through five layers. An amount is
-- never the empty string, so the encoding loses nothing and the mapper turns it back into
-- "absent" in exactly one place.
-- The run, written once. Every amount arrives as exact decimal text and the constraints check the
-- arithmetic: `open_total` has to be settled minus paid and `difference` has to be settled minus
-- the ERP's figure or the paid one, so a service that got either wrong fails here.
INSERT INTO billing.reconciliation_run (
    tenant_id, scope, provider_organization_id, period_from, period_to, run_no, currency_code,
    invoiced_total, approved_total, cut_total, returned_total, rejected_total,
    settled_total, paid_total, open_total, erp_total, difference, difference_count,
    differences, status, failure_code, ran_at, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('scope'), sqlc.narg('provider_organization_id'),
        sqlc.arg('period_from'), sqlc.arg('period_to'), sqlc.arg('run_no'),
        sqlc.arg('currency_code'),
        sqlc.arg('invoiced_total')::text::numeric,
        sqlc.arg('approved_total')::text::numeric,
        sqlc.arg('cut_total')::text::numeric,
        sqlc.arg('returned_total')::text::numeric,
        sqlc.arg('rejected_total')::text::numeric,
        sqlc.arg('settled_total')::text::numeric,
        sqlc.arg('paid_total')::text::numeric,
        sqlc.arg('open_total')::text::numeric,
        sqlc.narg('erp_total')::text::numeric,
        sqlc.arg('difference')::text::numeric,
        sqlc.arg('difference_count'),
        sqlc.arg('differences'), sqlc.arg('status'), sqlc.narg('failure_code'),
        sqlc.arg('ran_at'), sqlc.narg('actor_id'))
RETURNING id, scope, provider_organization_id, period_from, period_to, run_no, currency_code,
          trim_scale(invoiced_total)::text AS invoiced_total,
          trim_scale(approved_total)::text AS approved_total,
          trim_scale(cut_total)::text AS cut_total,
          trim_scale(returned_total)::text AS returned_total,
          trim_scale(rejected_total)::text AS rejected_total,
          trim_scale(settled_total)::text AS settled_total,
          trim_scale(paid_total)::text AS paid_total,
          trim_scale(open_total)::text AS open_total,
          concat(trim_scale(erp_total))::text AS erp_total,
          trim_scale(difference)::text AS difference,
          difference_count, differences, status, failure_code, ran_at, created_at;

-- name: GetReconciliationRun :one
-- One run, with the provider's display name when it has one. A run names an organization by id
-- and a screen names it by name; resolving it here is one join rather than a second request.
SELECT r.id, r.scope, r.provider_organization_id, r.period_from, r.period_to, r.run_no,
       r.currency_code,
       trim_scale(r.invoiced_total)::text AS invoiced_total,
       trim_scale(r.approved_total)::text AS approved_total,
       trim_scale(r.cut_total)::text AS cut_total,
       trim_scale(r.returned_total)::text AS returned_total,
       trim_scale(r.rejected_total)::text AS rejected_total,
       trim_scale(r.settled_total)::text AS settled_total,
       trim_scale(r.paid_total)::text AS paid_total,
       trim_scale(r.open_total)::text AS open_total,
       concat(trim_scale(r.erp_total))::text AS erp_total,
       trim_scale(r.difference)::text AS difference,
       r.difference_count, r.differences, r.status, r.failure_code, r.ran_at, r.created_at,
       o.display_name AS provider_name
  FROM billing.reconciliation_run r
  LEFT JOIN directory.tenant_organization t
         ON t.tenant_id = r.tenant_id AND t.id = r.provider_organization_id
  LEFT JOIN directory.organization o ON o.id = t.organization_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.id = sqlc.arg('id')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR r.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]));

-- name: ListReconciliationRuns :many
-- The runs, newest first, on the (created_at DESC, id DESC) keyset every list in this platform
-- pages by. A provider-scoped caller reads only its own runs and never the tenant-wide ones: a
-- TENANT run is every provider's figures added together.
SELECT r.id, r.scope, r.provider_organization_id, r.period_from, r.period_to, r.run_no,
       r.currency_code,
       trim_scale(r.invoiced_total)::text AS invoiced_total,
       trim_scale(r.approved_total)::text AS approved_total,
       trim_scale(r.cut_total)::text AS cut_total,
       trim_scale(r.returned_total)::text AS returned_total,
       trim_scale(r.rejected_total)::text AS rejected_total,
       trim_scale(r.settled_total)::text AS settled_total,
       trim_scale(r.paid_total)::text AS paid_total,
       trim_scale(r.open_total)::text AS open_total,
       concat(trim_scale(r.erp_total))::text AS erp_total,
       trim_scale(r.difference)::text AS difference,
       r.difference_count, r.differences, r.status, r.failure_code, r.ran_at, r.created_at,
       o.display_name AS provider_name
  FROM billing.reconciliation_run r
  LEFT JOIN directory.tenant_organization t
         ON t.tenant_id = r.tenant_id AND t.id = r.provider_organization_id
  LEFT JOIN directory.organization o ON o.id = t.organization_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR r.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
   AND (sqlc.narg('scope')::text IS NULL OR r.scope = sqlc.narg('scope')::text)
   AND (sqlc.narg('status')::text IS NULL OR r.status = sqlc.narg('status')::text)
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR r.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND (sqlc.narg('period_from')::date IS NULL OR r.period_from >= sqlc.narg('period_from')::date)
   AND (sqlc.narg('period_to')::date IS NULL OR r.period_to <= sqlc.narg('period_to')::date)
   AND (sqlc.narg('after_created_at')::timestamptz IS NULL
        OR (r.created_at, r.id) < (sqlc.narg('after_created_at')::timestamptz,
                                   sqlc.narg('after_id')::uuid))
 ORDER BY r.created_at DESC, r.id DESC
 LIMIT sqlc.arg('page_size');

-- name: MarkSettlementsReconciled :execrows
-- **The run marks what it found settled.** A settlement whose paid amount equals its payable
-- amount is RECONCILED afterwards, and this is the only statement in the codebase that writes
-- that status.
--
-- The predicate is the whole guarantee. `paid_amount = payable_amount` is checked here and again
-- by `ck_billing_settlement_reconciled`, so a settlement that is a kuruş short cannot be marked
-- by this statement, by a command, or by anybody with a psql session.
UPDATE billing.settlement s
   SET status = 'RECONCILED', updated_by = sqlc.narg('actor_id')
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.currency_code = sqlc.arg('currency_code')
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR s.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND s.due_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to')
   AND s.status = 'PAID'
   AND s.paid_amount = s.payable_amount;

-- ---------------------------------------------------------------------------
-- The operations dashboard
-- ---------------------------------------------------------------------------
--
-- One query per figure, each one a count and a sum the server computed, each one reproducible by
-- the filter the response carries beside it. Nothing on this dashboard is a percentage, because a
-- percentage is a number two screens can round differently.

-- name: DashboardClaimsByStatus :many
-- How many claims sit in each status and what they are worth. The amount is the claim's approved
-- total through `billing.claim_approved_total`, which is the one place that arithmetic lives.
SELECT c.status,
       count(*) AS claim_count,
       trim_scale(COALESCE(sum(billing.claim_approved_total(c.tenant_id, c.id)), 0))::text
           AS approved_total
  FROM claim.claim c
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND c.status NOT IN ('CANCELLED', 'SETTLED')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR c.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
 GROUP BY c.status
 ORDER BY c.status;

-- name: DashboardClaimAging :many
-- The same open claims, bucketed by how long they have been waiting. The buckets are days and
-- they are computed here rather than in Go: a bucket boundary a client chose would be a boundary
-- the list behind the link does not use.
SELECT CASE
           WHEN sqlc.arg('as_of')::timestamptz - c.created_at < interval '2 days' THEN 'D0_1'
           WHEN sqlc.arg('as_of')::timestamptz - c.created_at < interval '8 days' THEN 'D2_7'
           WHEN sqlc.arg('as_of')::timestamptz - c.created_at < interval '31 days' THEN 'D8_30'
           ELSE 'D31_PLUS'
       END AS bucket,
       count(*) AS claim_count,
       trim_scale(COALESCE(sum(billing.claim_approved_total(c.tenant_id, c.id)), 0))::text
           AS approved_total
  FROM claim.claim c
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND c.status IN ('SUBMITTED', 'AUTO_ADJUDICATED', 'PENDING_MEDICAL', 'PENDING_FINANCIAL',
                    'RETURNED')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR c.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
 GROUP BY 1
 ORDER BY 1;

-- name: DashboardBatchesAwaitingReview :one
-- The icmals waiting for a decision, what they are worth, and how long the oldest has waited. The
-- SLA is the work queue's own: the oldest item's due date is what an operator is judged by, and
-- reading it here is what makes "past SLA" the same number on this screen and in the worklist.
SELECT count(*) AS batch_count,
       trim_scale(COALESCE(sum(b.submitted_total), 0))::text AS submitted_total,
       min(b.submitted_at) AS oldest_submitted_at,
       (SELECT min(w.due_at)
          FROM workflow.work_item w
          JOIN workflow.work_queue q ON q.tenant_id = w.tenant_id AND q.id = w.queue_id
         WHERE w.tenant_id = sqlc.arg('tenant_id')
           AND q.code = sqlc.arg('review_queue_code')
           AND w.status IN ('OPEN', 'CLAIMED', 'ESCALATED')) AS oldest_sla_due_at
  FROM billing.batch b
 WHERE b.tenant_id = sqlc.arg('tenant_id')
   AND b.status IN ('SUBMITTED', 'UNDER_REVIEW')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR b.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]));

-- name: DashboardSettlements :one
-- What is due this week and what is already overdue, as counts and sums of what is still open.
-- Both arms read the same table with the same status filter and differ only in the date, so the
-- two figures cannot be built from two different ideas of what an open settlement is.
SELECT
    count(*) FILTER (
        WHERE s.due_date BETWEEN sqlc.arg('as_of')::date
                             AND sqlc.arg('as_of')::date + 7) AS due_soon_count,
    trim_scale(COALESCE(sum(s.payable_amount - s.paid_amount) FILTER (
        WHERE s.due_date BETWEEN sqlc.arg('as_of')::date
                             AND sqlc.arg('as_of')::date + 7), 0))::text AS due_soon_total,
    count(*) FILTER (WHERE s.due_date < sqlc.arg('as_of')::date) AS overdue_count,
    trim_scale(COALESCE(sum(s.payable_amount - s.paid_amount) FILTER (
        WHERE s.due_date < sqlc.arg('as_of')::date), 0))::text AS overdue_total
  FROM billing.settlement s
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.status IN ('APPROVED', 'POSTED', 'PARTIALLY_PAID')
   AND s.paid_amount < s.payable_amount
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR s.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]));

-- name: DashboardReimbursementsAwaitingDecision :one
-- The members waiting for an answer, and what they asked for. It carries no provider scope:
-- a reimbursement is the payer's business with its own member and a provider has no part in it.
SELECT count(*) AS reimbursement_count,
       trim_scale(COALESCE(sum(r.requested_amount), 0))::text AS requested_total,
       min(r.submitted_at) AS oldest_submitted_at
  FROM billing.reimbursement r
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.status IN ('SUBMITTED', 'UNDER_REVIEW');

-- name: DashboardWorkItemsPastSla :one
-- The work items whose clock has run out, over every queue. `due_at` is the queue's SLA as it
-- stood when the item was raised (migration 000027), so an item is late against the promise made
-- when it arrived rather than against whatever the queue says this morning.
SELECT count(*) AS item_count,
       min(w.due_at) AS oldest_due_at
  FROM workflow.work_item w
 WHERE w.tenant_id = sqlc.arg('tenant_id')
   AND w.status IN ('OPEN', 'CLAIMED', 'ESCALATED')
   AND w.due_at IS NOT NULL
   AND w.due_at < sqlc.arg('as_of');

-- ---------------------------------------------------------------------------
-- report.export
-- ---------------------------------------------------------------------------

-- name: CreateExport :one
-- The queued request. `parameters` is refused by the database if it carries a uuid or a key that
-- names an identifier, so a service that put a provider id in the filter blob fails here rather
-- than storing one.
--
-- The id is passed in rather than defaulted, because the watermark carries it: a stamp naming an
-- id the row does not have would be a stamp nothing can be traced back to, and drawing the id
-- first is what lets the two be written together.
INSERT INTO report.export (
    id, tenant_id, kind, parameters, format, status, provider_organization_id, period_from,
    period_to, currency_code, requested_by, requested_at, expires_at, watermark, created_by,
    updated_by)
VALUES (sqlc.arg('id'), sqlc.arg('tenant_id'), sqlc.arg('kind'), sqlc.arg('parameters'),
        sqlc.arg('format'),
        'QUEUED', sqlc.narg('provider_organization_id'), sqlc.narg('period_from'),
        sqlc.narg('period_to'), sqlc.narg('currency_code'), sqlc.arg('requested_by'),
        sqlc.arg('requested_at'), sqlc.arg('expires_at'), sqlc.arg('watermark'),
        sqlc.arg('requested_by'), sqlc.arg('requested_by'))
RETURNING id, kind, parameters, format, status, provider_organization_id, period_from,
          period_to, currency_code, document_id, row_count, requested_by, requested_at,
          expires_at, watermark, download_count, failure_code, created_at, row_version;

-- name: GetExport :one
-- One export. A caller reads its own unless it holds the tenant-wide grant: an export is a file
-- with somebody's name stamped on every row of it, and "who else pulled the settlement list this
-- morning" is not a question a colleague gets to ask by id.
SELECT e.id, e.kind, e.parameters, e.format, e.status, e.provider_organization_id, e.period_from,
       e.period_to, e.currency_code, e.document_id, e.row_count, e.requested_by, e.requested_at,
       e.expires_at, e.watermark, e.download_count, e.failure_code, e.created_at, e.row_version
  FROM report.export e
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND e.id = sqlc.arg('id')
   AND (sqlc.narg('requested_by')::uuid IS NULL
        OR e.requested_by = sqlc.narg('requested_by')::uuid);

-- name: LockExport :one
-- The same read, holding the row for the length of the command. The worker takes it before it
-- renders and the download takes it before it counts, so two deliveries of one job and two clicks
-- on one link serialise here rather than racing.
SELECT e.id, e.kind, e.parameters, e.format, e.status, e.provider_organization_id, e.period_from,
       e.period_to, e.currency_code, e.document_id, e.row_count, e.requested_by, e.requested_at,
       e.expires_at, e.watermark, e.download_count, e.failure_code, e.created_at, e.row_version
  FROM report.export e
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND e.id = sqlc.arg('id')
   FOR UPDATE;

-- name: ListExports :many
-- The exports, newest first, on the usual keyset. `requested_by` narrows it to one person's own.
SELECT e.id, e.kind, e.parameters, e.format, e.status, e.provider_organization_id, e.period_from,
       e.period_to, e.currency_code, e.document_id, e.row_count, e.requested_by, e.requested_at,
       e.expires_at, e.watermark, e.download_count, e.failure_code, e.created_at, e.row_version
  FROM report.export e
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('requested_by')::uuid IS NULL
        OR e.requested_by = sqlc.narg('requested_by')::uuid)
   AND (sqlc.narg('kind')::text IS NULL OR e.kind = sqlc.narg('kind')::text)
   AND (sqlc.narg('status')::text IS NULL OR e.status = sqlc.narg('status')::text)
   AND (sqlc.narg('after_created_at')::timestamptz IS NULL
        OR (e.created_at, e.id) < (sqlc.narg('after_created_at')::timestamptz,
                                   sqlc.narg('after_id')::uuid))
 ORDER BY e.created_at DESC, e.id DESC
 LIMIT sqlc.arg('page_size');

-- name: MarkExportRunning :execrows
-- QUEUED to RUNNING. The predicate carries the status, so a redelivered job that finds the export
-- already running or already finished changes nothing and is told so by the row count.
UPDATE report.export e
   SET status = 'RUNNING'
 WHERE e.tenant_id = sqlc.arg('tenant_id') AND e.id = sqlc.arg('id') AND e.status = 'QUEUED';

-- name: MarkExportReady :execrows
-- The finished file. The document, the row count and READY are written in one statement, because
-- an export that was READY for a moment without a document would be a link that answered 500.
UPDATE report.export e
   SET status = 'READY', document_id = sqlc.arg('document_id'),
       row_count = sqlc.arg('row_count')
 WHERE e.tenant_id = sqlc.arg('tenant_id') AND e.id = sqlc.arg('id')
   AND e.status IN ('QUEUED', 'RUNNING');

-- name: MarkExportFailed :execrows
-- A render that could not be completed, with the code that says why.
UPDATE report.export e
   SET status = 'FAILED', failure_code = sqlc.arg('failure_code')
 WHERE e.tenant_id = sqlc.arg('tenant_id') AND e.id = sqlc.arg('id')
   AND e.status IN ('QUEUED', 'RUNNING');

-- name: CountExportDownload :one
-- One more download. It is an increment in SQL rather than a read-modify-write in Go: two clicks
-- at the same moment are two downloads, and a counter built from a value Go had already read
-- would record one.
UPDATE report.export e
   SET download_count = e.download_count + 1
 WHERE e.tenant_id = sqlc.arg('tenant_id') AND e.id = sqlc.arg('id')
 RETURNING e.download_count;

-- name: ListExpiredExports :many
-- What the nightly sweep has to tidy: everything past its expiry that still believes it has a
-- file. A FAILED or already EXPIRED export is outside it, so a second pass finds nothing to do.
SELECT e.id, e.kind, e.document_id, e.requested_by, e.expires_at
  FROM report.export e
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND e.status IN ('QUEUED', 'RUNNING', 'READY')
   AND e.expires_at <= sqlc.arg('as_of')
 ORDER BY e.expires_at
 LIMIT sqlc.arg('row_limit');

-- name: MarkExportExpired :execrows
-- The row outlives the file, exactly as a purged document's row outlives its bytes: "this export
-- existed, held this many rows and stopped being downloadable on this day" is an answer somebody
-- will need, and an empty table is not.
UPDATE report.export e
   SET status = 'EXPIRED'
 WHERE e.tenant_id = sqlc.arg('tenant_id') AND e.id = sqlc.arg('id')
   AND e.status IN ('QUEUED', 'RUNNING', 'READY');

-- name: ActiveTenantsForReports :many
-- The tenants the reconciliation and the expiry sweep walk. `platform.tenant` carries no RLS, so
-- it is read outside a tenant transaction like the other cross-tenant jobs.
SELECT t.id FROM platform.tenant t
 WHERE t.status IN ('ACTIVE', 'SUSPENDED')
 ORDER BY t.id;

-- ---------------------------------------------------------------------------
-- The rows an export renders
-- ---------------------------------------------------------------------------
--
-- Each of these answers one kind. They return text rather than typed columns on purpose: the
-- worker's job is to write a file, and a renderer that had to know how to format five different
-- shapes of decimal would be five places for a figure to come out differently from the screen.

-- name: ExportSettlementRows :many
SELECT s.reference, o.display_name AS provider_name, b.reference AS batch_reference,
       s.currency_code, s.due_date, s.status,
       trim_scale(s.approved_amount)::text AS approved_amount,
       trim_scale(s.withheld_amount)::text AS withheld_amount,
       trim_scale(s.payable_amount)::text AS payable_amount,
       trim_scale(s.paid_amount)::text AS paid_amount,
       trim_scale(s.payable_amount - s.paid_amount)::text AS open_amount
  FROM billing.settlement s
  JOIN billing.batch b ON b.tenant_id = s.tenant_id AND b.id = s.batch_id
  JOIN directory.tenant_organization t
    ON t.tenant_id = s.tenant_id AND t.id = s.provider_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR s.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND (sqlc.narg('currency_code')::text IS NULL
        OR s.currency_code = sqlc.narg('currency_code')::text)
   AND (sqlc.narg('period_from')::date IS NULL
        OR s.due_date BETWEEN sqlc.narg('period_from')::date AND sqlc.narg('period_to')::date)
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR s.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
 ORDER BY s.due_date, s.id
 LIMIT sqlc.arg('row_limit');

-- name: ExportBatchRows :many
SELECT b.reference, o.display_name AS provider_name, b.currency_code, b.period_from, b.period_to,
       b.status, b.invoice_count,
       trim_scale(b.submitted_total)::text AS submitted_total,
       trim_scale(b.approved_total)::text AS approved_total,
       trim_scale(b.cut_total)::text AS cut_total,
       trim_scale(b.returned_total)::text AS returned_total,
       trim_scale(b.rejected_total)::text AS rejected_total
  FROM billing.batch b
  JOIN directory.tenant_organization t
    ON t.tenant_id = b.tenant_id AND t.id = b.provider_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE b.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR b.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND (sqlc.narg('currency_code')::text IS NULL
        OR b.currency_code = sqlc.narg('currency_code')::text)
   AND (sqlc.narg('period_from')::date IS NULL
        OR b.period_from >= sqlc.narg('period_from')::date)
   AND (sqlc.narg('period_to')::date IS NULL OR b.period_to <= sqlc.narg('period_to')::date)
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR b.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
 ORDER BY b.period_from, b.id
 LIMIT sqlc.arg('row_limit');

-- name: ExportStatementRows :many
-- The invoice rows of one provider's statement. It is the same shape the screen reads, so a
-- reconciliation argument held over a spreadsheet and one held over the page are held over the
-- same numbers.
SELECT i.invoice_number, i.invoice_date, i.status, i.currency_code,
       trim_scale(i.payable_amount)::text AS payable_amount,
       COALESCE(b.reference, '') AS batch_reference,
       COALESCE(bi.decision, '') AS batch_decision,
       trim_scale(COALESCE(bi.approved_amount, 0))::text AS approved_amount,
       COALESCE(s.reference, '') AS settlement_reference,
       trim_scale(COALESCE(s.paid_amount, 0))::text AS settlement_paid_amount
  FROM billing.invoice i
  LEFT JOIN billing.batch b ON b.tenant_id = i.tenant_id AND b.id = i.batch_id
  LEFT JOIN billing.batch_invoice bi
         ON bi.tenant_id = i.tenant_id AND bi.invoice_id = i.id AND bi.batch_id = b.id
  LEFT JOIN billing.settlement s
         ON s.tenant_id = b.tenant_id AND s.batch_id = b.id AND s.status <> 'CANCELLED'
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND i.provider_organization_id = sqlc.arg('provider_organization_id')
   AND (sqlc.narg('currency_code')::text IS NULL
        OR i.currency_code = sqlc.narg('currency_code')::text)
   AND i.invoice_date BETWEEN sqlc.arg('period_from') AND sqlc.arg('period_to')
   AND i.status NOT IN ('DRAFT', 'CANCELLED')
 ORDER BY i.invoice_date, i.id
 LIMIT sqlc.arg('row_limit');

-- name: ExportReconciliationRows :many
SELECT r.scope, COALESCE(o.display_name, '') AS provider_name, r.period_from, r.period_to,
       r.run_no, r.currency_code, r.status, r.difference_count,
       trim_scale(r.settled_total)::text AS settled_total,
       trim_scale(r.paid_total)::text AS paid_total,
       trim_scale(r.open_total)::text AS open_total,
       trim_scale(r.difference)::text AS difference
  FROM billing.reconciliation_run r
  LEFT JOIN directory.tenant_organization t
         ON t.tenant_id = r.tenant_id AND t.id = r.provider_organization_id
  LEFT JOIN directory.organization o ON o.id = t.organization_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR r.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND (sqlc.narg('currency_code')::text IS NULL
        OR r.currency_code = sqlc.narg('currency_code')::text)
   AND (sqlc.narg('period_from')::date IS NULL
        OR r.period_from >= sqlc.narg('period_from')::date)
   AND (sqlc.narg('period_to')::date IS NULL OR r.period_to <= sqlc.narg('period_to')::date)
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR r.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
 ORDER BY r.period_from DESC, r.id DESC
 LIMIT sqlc.arg('row_limit');

-- name: ExportClaimRows :many
-- The only export whose rows carry prose. `description` is what a member was treated for written
-- in words, which is why this kind needs `report.export.sensitive` and why the line is joined
-- from the claim's *current* version rather than from a snapshot: an export is a picture of today.
SELECT c.reference, o.display_name AS provider_name, c.status, c.domain_code,
       c.service_date_from, c.service_date_to,
       COALESCE(l.description, '') AS description,
       COALESCE(sd.code, '') AS service_code,
       trim_scale(l.line_amount)::text AS line_amount,
       trim_scale(COALESCE(d.approved_amount, 0))::text AS approved_amount,
       l.currency_code
  FROM claim.claim c
  JOIN directory.tenant_organization t
    ON t.tenant_id = c.tenant_id AND t.id = c.provider_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
  JOIN claim.claim_version v
    ON v.tenant_id = c.tenant_id AND v.claim_id = c.id AND v.version_no = c.current_version_no
  JOIN claim.claim_line l ON l.tenant_id = v.tenant_id AND l.version_id = v.id
  LEFT JOIN catalog.service_definition sd
         ON sd.tenant_id = l.tenant_id AND sd.id = l.service_definition_id
  LEFT JOIN LATERAL (
      SELECT ld.approved_amount
        FROM claim.line_decision ld
       WHERE ld.tenant_id = l.tenant_id AND ld.line_id = l.id
       ORDER BY ld.decided_at DESC, ld.id DESC
       LIMIT 1
  ) d ON true
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR c.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND (sqlc.narg('period_from')::date IS NULL
        OR c.service_date_from >= sqlc.narg('period_from')::date)
   AND (sqlc.narg('period_to')::date IS NULL
        OR c.service_date_to <= sqlc.narg('period_to')::date)
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR c.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
 ORDER BY c.service_date_from, c.id, l.line_no
 LIMIT sqlc.arg('row_limit');

-- name: GetExportIdentity :one
-- The two names the watermark carries: the tenant this export leaves, and the person who asked
-- for it. They are read in one round trip inside the command's own transaction, because a
-- watermark assembled from a name cached somewhere else would be a stamp that could be stale.
--
-- `platform.tenant` carries no RLS and `iam.actor` is not tenant-scoped, which is why this join
-- is free to cross both.
SELECT t.code AS tenant_code, a.display_name AS requester_name
  FROM platform.tenant t
  CROSS JOIN iam.actor a
 WHERE t.id = sqlc.arg('tenant_id') AND a.id = sqlc.arg('actor_id');
