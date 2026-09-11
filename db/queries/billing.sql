-- WP-I7-02: the invoice as the provider entered it, and its allocation to claims.
--
-- Every money column arrives and leaves as exact decimal text. `sqlc.arg(x)::text::numeric`
-- on the way in and `trim_scale(x)::text` on the way out, so nothing in this module ever
-- becomes a float and two tenants who typed "1000" and "1000.00" read the same string back.
--
-- The reads below reach into `claim`, `directory` and `document`. That is deliberate and it
-- is read-only: an invoice is a statement about claims of a provider with a tax identity and
-- an image, and asking three services for three halves of one page would be three round trips
-- that could disagree with each other. Every *write* that changes a claim goes through the
-- claim module's own command, because a status transition is a business rule and not a join.

-- name: CreateInvoice :one
-- The header, as entered. `provider_tax_id_hash` is copied from the directory by the caller;
-- it is the VKN's blind index and the plaintext never reaches this statement.
INSERT INTO billing.invoice (
    tenant_id, provider_organization_id, payer_organization_id, source, invoice_number,
    invoice_date, fiscal_year, provider_tax_id_hash, currency_code, line_extension_amount,
    tax_amount, payable_amount, vat_rate, domain_code, supersedes_invoice_id, document_id,
    notes, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('provider_organization_id'),
        sqlc.narg('payer_organization_id'), sqlc.arg('source'), sqlc.arg('invoice_number'),
        sqlc.arg('invoice_date'), sqlc.arg('fiscal_year'), sqlc.arg('provider_tax_id_hash'),
        sqlc.arg('currency_code'), sqlc.arg('line_extension_amount')::text::numeric,
        sqlc.arg('tax_amount')::text::numeric, sqlc.arg('payable_amount')::text::numeric,
        sqlc.narg('vat_rate')::text::numeric, sqlc.arg('domain_code'),
        sqlc.narg('supersedes_invoice_id'), sqlc.narg('document_id'), sqlc.narg('notes'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, provider_organization_id, payer_organization_id, source, edocument_id,
          invoice_number, invoice_date, fiscal_year, currency_code,
          trim_scale(line_extension_amount)::text AS line_extension_amount,
          trim_scale(tax_amount)::text AS tax_amount,
          trim_scale(payable_amount)::text AS payable_amount,
          COALESCE(trim_scale(vat_rate)::text, '')::text AS vat_rate,
          domain_code, status, supersedes_invoice_id, superseded_by_invoice_id, submitted_at,
          document_id, batch_id, notes, created_at, row_version;

-- name: GetInvoice :one
-- One invoice, bounded by the caller's provider scope. An empty scope array means "no
-- restriction"; a scope that names organizations narrows the read in SQL, so an invoice
-- outside it is genuinely not returned and 404 is honest rather than a filtered 200.
SELECT i.id, i.provider_organization_id, i.payer_organization_id, i.source, i.edocument_id,
       i.invoice_number, i.invoice_date, i.fiscal_year, i.currency_code,
       trim_scale(i.line_extension_amount)::text AS line_extension_amount,
       trim_scale(i.tax_amount)::text AS tax_amount,
       trim_scale(i.payable_amount)::text AS payable_amount,
       COALESCE(trim_scale(i.vat_rate)::text, '')::text AS vat_rate,
       i.domain_code, i.status, i.supersedes_invoice_id, i.superseded_by_invoice_id,
       i.submitted_at, i.document_id, i.batch_id, i.notes, i.created_at, i.row_version,
       o.display_name AS provider_name
  FROM billing.invoice i
  JOIN directory.tenant_organization t
    ON t.tenant_id = i.tenant_id AND t.id = i.provider_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND i.id = sqlc.arg('id')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR i.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]));

-- name: LockInvoice :one
-- The same read, holding the row for the length of the command. Every write command takes it
-- first: the allocation replacement, the submit and the cancel all read a state and then act
-- on it, and two callers doing that at once is exactly how an invoice ends up submitted twice.
SELECT i.id, i.provider_organization_id, i.payer_organization_id, i.source, i.edocument_id,
       i.invoice_number, i.invoice_date, i.fiscal_year, i.currency_code,
       trim_scale(i.line_extension_amount)::text AS line_extension_amount,
       trim_scale(i.tax_amount)::text AS tax_amount,
       trim_scale(i.payable_amount)::text AS payable_amount,
       COALESCE(trim_scale(i.vat_rate)::text, '')::text AS vat_rate,
       i.domain_code, i.status, i.supersedes_invoice_id, i.superseded_by_invoice_id,
       i.submitted_at, i.document_id, i.batch_id, i.notes, i.created_at, i.row_version
  FROM billing.invoice i
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND i.id = sqlc.arg('id')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR i.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
   FOR UPDATE;

-- name: ListInvoices :many
-- One page of the keyset on (created_at DESC, id DESC), with the allocation total and the
-- allocation count each row carries. The total is summed in SQL here rather than in Go
-- because a list of fifty invoices would otherwise be fifty extra reads; the *gate* still
-- sums in Go from the rows themselves, and a test compares the two.
SELECT i.id, i.provider_organization_id, i.payer_organization_id, i.source, i.edocument_id,
       i.invoice_number, i.invoice_date, i.fiscal_year, i.currency_code,
       trim_scale(i.line_extension_amount)::text AS line_extension_amount,
       trim_scale(i.tax_amount)::text AS tax_amount,
       trim_scale(i.payable_amount)::text AS payable_amount,
       COALESCE(trim_scale(i.vat_rate)::text, '')::text AS vat_rate,
       i.domain_code, i.status, i.supersedes_invoice_id, i.superseded_by_invoice_id,
       i.submitted_at, i.document_id, i.batch_id, i.notes, i.created_at, i.row_version,
       o.display_name AS provider_name,
       trim_scale(a.total)::text AS allocation_total,
       a.allocation_count
  FROM billing.invoice i
  JOIN directory.tenant_organization t
    ON t.tenant_id = i.tenant_id AND t.id = i.provider_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
  JOIN LATERAL (
      SELECT COALESCE(sum(ic.allocated_amount), 0) AS total,
             count(*)::int AS allocation_count
        FROM billing.invoice_claim ic
       WHERE ic.tenant_id = i.tenant_id AND ic.invoice_id = i.id AND ic.active
  ) a ON true
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR i.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR i.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND (sqlc.narg('status')::text IS NULL OR i.status = sqlc.narg('status')::text)
   AND (sqlc.narg('fiscal_year')::int IS NULL OR i.fiscal_year = sqlc.narg('fiscal_year')::int)
   AND (sqlc.narg('date_from')::date IS NULL OR i.invoice_date >= sqlc.narg('date_from')::date)
   AND (sqlc.narg('date_to')::date IS NULL OR i.invoice_date <= sqlc.narg('date_to')::date)
   AND (sqlc.narg('batch_id')::uuid IS NULL OR i.batch_id = sqlc.narg('batch_id')::uuid)
   AND (sqlc.narg('after_created_at')::timestamptz IS NULL
        OR (i.created_at, i.id) < (sqlc.narg('after_created_at')::timestamptz,
                                   sqlc.narg('after_id')::uuid))
 ORDER BY i.created_at DESC, i.id DESC
 LIMIT sqlc.arg('page_size');

-- name: ListInvoiceChain :many
-- The supersede chain an invoice belongs to, oldest first, walked in both directions in one
-- recursive term: a payer's finance user asking about the newest document and one asking
-- about the oldest have the same question, and answering it from a link that only pointed one
-- way would mean walking the chain in a client.
WITH RECURSIVE chain AS (
    SELECT i.id, i.supersedes_invoice_id
      FROM billing.invoice i
     WHERE i.tenant_id = sqlc.arg('tenant_id') AND i.id = sqlc.arg('id')
    UNION
    SELECT n.id, n.supersedes_invoice_id
      FROM billing.invoice n
      JOIN chain c
        ON n.id = c.supersedes_invoice_id OR n.supersedes_invoice_id = c.id
     WHERE n.tenant_id = sqlc.arg('tenant_id')
)
SELECT i.id, i.provider_organization_id, i.payer_organization_id, i.source, i.edocument_id,
       i.invoice_number, i.invoice_date, i.fiscal_year, i.currency_code,
       trim_scale(i.line_extension_amount)::text AS line_extension_amount,
       trim_scale(i.tax_amount)::text AS tax_amount,
       trim_scale(i.payable_amount)::text AS payable_amount,
       COALESCE(trim_scale(i.vat_rate)::text, '')::text AS vat_rate,
       i.domain_code, i.status, i.supersedes_invoice_id, i.superseded_by_invoice_id,
       i.submitted_at, i.document_id, i.batch_id, i.notes, i.created_at, i.row_version,
       o.display_name AS provider_name,
       trim_scale(a.total)::text AS allocation_total,
       a.allocation_count
  FROM billing.invoice i
  JOIN chain ON chain.id = i.id
  JOIN directory.tenant_organization t
    ON t.tenant_id = i.tenant_id AND t.id = i.provider_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
  JOIN LATERAL (
      SELECT COALESCE(sum(ic.allocated_amount), 0) AS total,
             count(*)::int AS allocation_count
        FROM billing.invoice_claim ic
       WHERE ic.tenant_id = i.tenant_id AND ic.invoice_id = i.id AND ic.active
  ) a ON true
 WHERE i.tenant_id = sqlc.arg('tenant_id')
 ORDER BY i.created_at, i.id;

-- name: UpdateInvoiceDraft :execrows
-- The header of a DRAFT. `from_statuses` and `expected_row_version` carry the whole
-- precondition, so a caller whose If-Match was stale writes nothing and is told so; the
-- freeze trigger is underneath this and refuses the same write from anywhere else.
UPDATE billing.invoice
   SET payer_organization_id = sqlc.narg('payer_organization_id'),
       invoice_number        = sqlc.arg('invoice_number'),
       invoice_date          = sqlc.arg('invoice_date'),
       fiscal_year           = sqlc.arg('fiscal_year'),
       currency_code         = sqlc.arg('currency_code'),
       line_extension_amount = sqlc.arg('line_extension_amount')::text::numeric,
       tax_amount            = sqlc.arg('tax_amount')::text::numeric,
       payable_amount        = sqlc.arg('payable_amount')::text::numeric,
       vat_rate              = sqlc.narg('vat_rate')::text::numeric,
       domain_code           = sqlc.arg('domain_code'),
       document_id           = sqlc.narg('document_id'),
       notes                 = sqlc.narg('notes'),
       updated_by            = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT'
   AND row_version = sqlc.arg('expected_row_version');

-- name: SetInvoiceStatus :execrows
-- One move through the lifecycle, with the whole precondition in the WHERE clause.
UPDATE billing.invoice
   SET status       = sqlc.arg('status'),
       submitted_at = COALESCE(billing.invoice.submitted_at, sqlc.narg('submitted_at')),
       updated_by   = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = ANY(sqlc.arg('from_statuses')::text[])
   AND row_version = sqlc.arg('expected_row_version');

-- name: SetInvoiceSupersededBy :execrows
-- The old invoice learns its successor, and is cancelled by the same statement. It happens
-- when the correction is submitted and never when it is drafted: a correction somebody
-- abandoned must not have cancelled the document it was going to replace.
UPDATE billing.invoice
   SET status                   = 'CANCELLED',
       superseded_by_invoice_id = sqlc.arg('superseded_by_invoice_id'),
       updated_by               = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status IN ('DRAFT', 'RETURNED', 'SUBMITTED', 'REJECTED');

-- name: ListInvoiceAllocations :many
-- The claims one invoice covers, with what each of them is worth now. `approved_total` is
-- recomputed by `billing.claim_approved_total`, which is the same arithmetic the deferred
-- ceiling trigger applies -- so the figure a screen shows is the figure an allocation would
-- be refused against.
--
-- `claim_description` is the provider's own words on the claim's lines. It is possibly
-- clinical, and this query returns it unprojected: the application layer clears it for a
-- caller who has not earned the clinical projection, in one place, on the record.
SELECT ic.claim_id, ic.claim_version_no,
       trim_scale(ic.allocated_amount)::text AS allocated_amount,
       ic.currency_code, ic.claim_status_before, ic.active,
       c.reference AS claim_reference, c.status AS claim_status,
       trim_scale(billing.claim_approved_total(ic.tenant_id, ic.claim_id))::text
           AS approved_total,
       COALESCE((SELECT string_agg(l.description, ' · ' ORDER BY l.line_no)
          FROM claim.claim_version v
          JOIN claim.claim_line l ON l.tenant_id = v.tenant_id AND l.version_id = v.id
         WHERE v.tenant_id = c.tenant_id AND v.claim_id = c.id
           AND v.version_no = c.current_version_no
           AND l.description IS NOT NULL), '')::text AS claim_description
  FROM billing.invoice_claim ic
  JOIN claim.claim c ON c.tenant_id = ic.tenant_id AND c.id = ic.claim_id
 WHERE ic.tenant_id = sqlc.arg('tenant_id')
   AND ic.invoice_id = sqlc.arg('invoice_id')
 ORDER BY ic.created_at, ic.id;

-- name: DeleteInvoiceAllocations :exec
-- The first half of "replace the whole set". The freeze trigger refuses it on anything that
-- has left DRAFT, so this statement can never quietly empty a submitted invoice.
DELETE FROM billing.invoice_claim
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND invoice_id = sqlc.arg('invoice_id');

-- name: CreateInvoiceAllocation :exec
-- The second half. The deferred constraint trigger checks the ceiling, the claim's status and
-- the currency at commit, which is why the intermediate states of a replacement are nobody's
-- business.
INSERT INTO billing.invoice_claim (
    tenant_id, invoice_id, claim_id, claim_version_no, allocated_amount, currency_code,
    claim_status_before, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('invoice_id'), sqlc.arg('claim_id'),
        sqlc.arg('claim_version_no'), sqlc.arg('allocated_amount')::text::numeric,
        sqlc.arg('currency_code'), sqlc.arg('claim_status_before'), sqlc.narg('actor_id'));

-- name: FindInvoiceByNumber :one
-- Which invoice, if any, already carries this number in this provider's fiscal year.
--
-- The unique index refuses two *live* documents sharing a number. This read answers the half
-- the index cannot: a RETURNED invoice's number may be reused, but only by the invoice that
-- supersedes it -- so the service needs to know which invoice is holding it rather than only
-- that somebody is.
SELECT i.id, i.status
  FROM billing.invoice i
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND i.provider_organization_id = sqlc.arg('provider_organization_id')
   AND i.fiscal_year = sqlc.arg('fiscal_year')
   AND i.invoice_number = sqlc.arg('invoice_number')
   AND i.status <> 'CANCELLED'
   AND i.id <> sqlc.arg('excluding_id')
 ORDER BY i.created_at, i.id
 LIMIT 1;

-- name: GetInvoiceProviderTaxIdentity :one
-- The provider's VKN blind index, and its display name. The number itself is envelope
-- encrypted in a column this statement does not read: an invoice records *that* it was
-- raised under a tax identity, and the plaintext has no business anywhere near it.
SELECT t.id, o.display_name, o.tax_number_hash, t.relationship_role, t.status
  FROM directory.tenant_organization t
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE t.tenant_id = sqlc.arg('tenant_id')
   AND t.id = sqlc.arg('id');

-- name: ListInvoiceableClaims :many
-- The claims an allocation may name, answered for exactly the ids the caller sent.
--
-- `live_invoice_id` is the invoice a claim is already sitting on, when there is one. It is
-- answered rather than filtered out, so the refusal can name the document instead of saying
-- that a claim the caller can see in their own earnings view does not exist.
SELECT c.id AS claim_id, c.reference, c.status, c.current_version_no,
       c.provider_organization_id,
       COALESCE((
           SELECT min(l.currency_code)
             FROM claim.claim_version v
             JOIN claim.claim_line l ON l.tenant_id = v.tenant_id AND l.version_id = v.id
            WHERE v.tenant_id = c.tenant_id AND v.claim_id = c.id
              AND v.version_no = c.current_version_no
       ), 'TRY')::text AS currency_code,
       trim_scale(billing.claim_approved_total(c.tenant_id, c.id))::text AS approved_total,
       -- The nil uuid rather than NULL, because "no live invoice" is the ordinary answer and
       -- a nullable column here would make every caller unwrap a pointer to ask a boolean.
       COALESCE((SELECT ic.invoice_id
          FROM billing.invoice_claim ic
         WHERE ic.tenant_id = c.tenant_id AND ic.claim_id = c.id AND ic.active
         LIMIT 1), '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS live_invoice_id
  FROM claim.claim c
 WHERE c.tenant_id = sqlc.arg('tenant_id')
   AND c.id = ANY(sqlc.arg('claim_ids')::uuid[])
 ORDER BY c.created_at, c.id;

-- name: CountCleanInvoiceDocuments :one
-- The image half of the submit gate: is the document this invoice names a document of this
-- tenant that the scanner cleared and nobody has purged. A file still in quarantine is not an
-- invoice image a reviewer can open.
SELECT count(*)
  FROM document.object o
 WHERE o.tenant_id = sqlc.arg('tenant_id')
   AND o.id = sqlc.arg('id')
   AND o.scan_status = 'CLEAN'
   AND o.purged_at IS NULL;

-- name: LinkInvoiceDocument :exec
-- How the record says "this is the invoice" (WP-I4-04). The link is what a reviewer opens;
-- `billing.invoice.document_id` is what the submit gate reads. Both are written, and the
-- conflict clause makes a redelivered command write one link rather than failing.
INSERT INTO document.link (
    tenant_id, object_id, aggregate_type, aggregate_id, document_type_code, purpose,
    required_permission, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('object_id'), 'INVOICE', sqlc.arg('aggregate_id'),
        'INVOICE', sqlc.narg('purpose'), 'invoice.read', sqlc.narg('actor_id'))
ON CONFLICT (tenant_id, object_id, aggregate_type, aggregate_id, document_type_code)
DO NOTHING;

-- ---------------------------------------------------------------------------
-- WP-I7-03: the icmal, and the payer's decision on each invoice in it
-- ---------------------------------------------------------------------------
--
-- The same two habits as above: every money column arrives as `text::numeric` and leaves as
-- `trim_scale(...)::text`, so nothing here becomes a float; and every write carries its whole
-- precondition in the WHERE clause, so a caller whose If-Match was stale writes nothing.
--
-- The statements never repeat a rule the schema already holds. The freeze is a trigger, the
-- one-live-batch-per-invoice rule is a partial unique index, and the totals reconcile through
-- a CHECK: what is below is only the shape of each command.

-- name: CreateBatch :one
INSERT INTO billing.batch (
    tenant_id, reference, provider_organization_id, payer_organization_id, domain_code,
    currency_code, period_from, period_to, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('reference'), sqlc.arg('provider_organization_id'),
        sqlc.narg('payer_organization_id'), sqlc.arg('domain_code'), sqlc.arg('currency_code'),
        sqlc.arg('period_from'), sqlc.arg('period_to'), sqlc.narg('actor_id'),
        sqlc.narg('actor_id'))
RETURNING id, reference, provider_organization_id, payer_organization_id, domain_code,
          currency_code, period_from, period_to, status, submitted_at, submitted_by,
          decided_at, decided_by, invoice_count,
          trim_scale(submitted_total)::text AS submitted_total,
          trim_scale(approved_total)::text AS approved_total,
          trim_scale(cut_total)::text AS cut_total,
          trim_scale(returned_total)::text AS returned_total,
          trim_scale(rejected_total)::text AS rejected_total,
          created_at, row_version;

-- name: GetBatch :one
-- One batch, bounded by the caller's provider scope, exactly as GetInvoice is: an empty scope
-- array means "no restriction", and a batch outside a scope that names organizations is
-- genuinely not returned, so 404 is honest rather than a filtered 200.
SELECT b.id, b.reference, b.provider_organization_id, b.payer_organization_id, b.domain_code,
       b.currency_code, b.period_from, b.period_to, b.status, b.submitted_at, b.submitted_by,
       b.decided_at, b.decided_by, b.invoice_count,
       trim_scale(b.submitted_total)::text AS submitted_total,
       trim_scale(b.approved_total)::text AS approved_total,
       trim_scale(b.cut_total)::text AS cut_total,
       trim_scale(b.returned_total)::text AS returned_total,
       trim_scale(b.rejected_total)::text AS rejected_total,
       b.created_at, b.row_version,
       o.display_name AS provider_name
  FROM billing.batch b
  JOIN directory.tenant_organization t
    ON t.tenant_id = b.tenant_id AND t.id = b.provider_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE b.tenant_id = sqlc.arg('tenant_id')
   AND b.id = sqlc.arg('id')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR b.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]));

-- name: LockBatch :one
-- The same read, holding the row for the length of the command. Every write takes it first:
-- the membership replacement, the submit, each decision and the decide all read a state and
-- then act on it, and two callers doing that at once is how a batch ends up decided twice.
--
-- The provider's display name is joined in for the same reason `GetBatch` joins it: the submit
-- and the decide both send a notification naming the provider, and a name the command did not
-- read is an empty variable the notification gate refuses. `FOR UPDATE OF b` keeps the lock on
-- the batch alone, so the join does not take a row lock on the directory.
SELECT b.id, b.reference, b.provider_organization_id, b.payer_organization_id, b.domain_code,
       b.currency_code, b.period_from, b.period_to, b.status, b.submitted_at, b.submitted_by,
       b.decided_at, b.decided_by, b.invoice_count,
       trim_scale(b.submitted_total)::text AS submitted_total,
       trim_scale(b.approved_total)::text AS approved_total,
       trim_scale(b.cut_total)::text AS cut_total,
       trim_scale(b.returned_total)::text AS returned_total,
       trim_scale(b.rejected_total)::text AS rejected_total,
       b.created_at, b.row_version,
       COALESCE(o.display_name, '')::text AS provider_name
  FROM billing.batch b
  LEFT JOIN directory.tenant_organization t
         ON t.tenant_id = b.tenant_id AND t.id = b.provider_organization_id
  LEFT JOIN directory.organization o ON o.id = t.organization_id
 WHERE b.tenant_id = sqlc.arg('tenant_id')
   AND b.id = sqlc.arg('id')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR b.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
   FOR UPDATE OF b;

-- name: ListBatches :many
-- One page of the keyset on (created_at DESC, id DESC).
SELECT b.id, b.reference, b.provider_organization_id, b.payer_organization_id, b.domain_code,
       b.currency_code, b.period_from, b.period_to, b.status, b.submitted_at, b.submitted_by,
       b.decided_at, b.decided_by, b.invoice_count,
       trim_scale(b.submitted_total)::text AS submitted_total,
       trim_scale(b.approved_total)::text AS approved_total,
       trim_scale(b.cut_total)::text AS cut_total,
       trim_scale(b.returned_total)::text AS returned_total,
       trim_scale(b.rejected_total)::text AS rejected_total,
       b.created_at, b.row_version,
       o.display_name AS provider_name
  FROM billing.batch b
  JOIN directory.tenant_organization t
    ON t.tenant_id = b.tenant_id AND t.id = b.provider_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE b.tenant_id = sqlc.arg('tenant_id')
   AND (cardinality(sqlc.arg('scope_ids')::uuid[]) = 0
        OR b.provider_organization_id = ANY(sqlc.arg('scope_ids')::uuid[]))
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR b.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND (sqlc.narg('payer_organization_id')::uuid IS NULL
        OR b.payer_organization_id = sqlc.narg('payer_organization_id')::uuid)
   AND (sqlc.narg('status')::text IS NULL OR b.status = sqlc.narg('status')::text)
   AND (sqlc.narg('domain_code')::text IS NULL OR b.domain_code = sqlc.narg('domain_code')::text)
   AND (sqlc.narg('currency_code')::text IS NULL
        OR b.currency_code = sqlc.narg('currency_code')::text)
   AND (sqlc.narg('date_from')::date IS NULL OR b.period_to >= sqlc.narg('date_from')::date)
   AND (sqlc.narg('date_to')::date IS NULL OR b.period_from <= sqlc.narg('date_to')::date)
   AND (sqlc.narg('after_created_at')::timestamptz IS NULL
        OR (b.created_at, b.id) < (sqlc.narg('after_created_at')::timestamptz,
                                   sqlc.narg('after_id')::uuid))
 ORDER BY b.created_at DESC, b.id DESC
 LIMIT sqlc.arg('page_size');

-- name: SubmitBatch :execrows
-- DRAFT to SUBMITTED, with the count and the submitted total the server computed from the
-- members it just froze. Both figures are written here rather than summed on read, because a
-- settlement opening on `batch.decided` has to be able to say what it is settling without
-- walking the membership.
UPDATE billing.batch
   SET status          = 'SUBMITTED',
       submitted_at    = sqlc.arg('submitted_at'),
       submitted_by    = sqlc.arg('submitted_by'),
       invoice_count   = sqlc.arg('invoice_count'),
       submitted_total = sqlc.arg('submitted_total')::text::numeric,
       updated_by      = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT'
   AND row_version = sqlc.arg('expected_row_version');

-- name: StartBatchReview :execrows
-- SUBMITTED to UNDER_REVIEW: the payer's reviewer has taken the work item and the batch is in
-- front of somebody. There is no row_version, because this is not a command of its own -- it
-- is the first decision's side effect, and that decision already holds the batch's row lock.
UPDATE billing.batch
   SET status     = 'UNDER_REVIEW',
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'SUBMITTED';

-- name: DecideBatch :execrows
-- UNDER_REVIEW to DECIDED, with the four totals the server recomputed from the decisions.
-- `ck_billing_batch_totals` refuses the write unless they add up to the submitted total, so a
-- service that got the arithmetic wrong fails here rather than opening a settlement on a
-- figure that is not the sum of its parts.
UPDATE billing.batch
   SET status         = 'DECIDED',
       decided_at     = sqlc.arg('decided_at'),
       decided_by     = sqlc.arg('decided_by'),
       approved_total = sqlc.arg('approved_total')::text::numeric,
       cut_total      = sqlc.arg('cut_total')::text::numeric,
       returned_total = sqlc.arg('returned_total')::text::numeric,
       rejected_total = sqlc.arg('rejected_total')::text::numeric,
       updated_by     = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'UNDER_REVIEW'
   AND row_version = sqlc.arg('expected_row_version');

-- name: DeleteBatchInvoices :exec
-- The first half of "replace the whole set". The freeze trigger refuses it on anything that
-- has left DRAFT, so this statement can never quietly empty a submitted batch.
DELETE FROM billing.batch_invoice
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND batch_id = sqlc.arg('batch_id');

-- name: CreateBatchInvoice :exec
INSERT INTO billing.batch_invoice (
    tenant_id, batch_id, invoice_id, submitted_amount, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('batch_id'), sqlc.arg('invoice_id'),
        sqlc.arg('submitted_amount')::text::numeric, sqlc.narg('actor_id'));

-- name: ListBatchInvoices :many
-- The invoices one batch covers, with the payer's answer to each and enough of the invoice
-- itself for a reviewer to read the row without a second request.
SELECT bi.id, bi.invoice_id,
       trim_scale(bi.submitted_amount)::text AS submitted_amount,
       COALESCE(bi.decision, '')::text AS decision,
       COALESCE(trim_scale(bi.approved_amount)::text, '')::text AS approved_amount,
       COALESCE(bi.reason_code, '')::text AS reason_code,
       bi.reason_text, bi.decided_by, bi.decided_at, bi.active, bi.created_at,
       i.invoice_number, i.invoice_date, i.status AS invoice_status, i.currency_code,
       trim_scale(i.payable_amount)::text AS payable_amount,
       a.display_name AS decided_by_display_name
  FROM billing.batch_invoice bi
  JOIN billing.invoice i ON i.tenant_id = bi.tenant_id AND i.id = bi.invoice_id
  LEFT JOIN iam.actor a ON a.id = bi.decided_by
 WHERE bi.tenant_id = sqlc.arg('tenant_id')
   AND bi.batch_id = sqlc.arg('batch_id')
 ORDER BY bi.created_at, bi.id;

-- name: LockBatchInvoice :one
-- The member a decision is about, held for the length of the command.
SELECT bi.id, bi.invoice_id,
       trim_scale(bi.submitted_amount)::text AS submitted_amount,
       COALESCE(bi.decision, '')::text AS decision,
       COALESCE(trim_scale(bi.approved_amount)::text, '')::text AS approved_amount,
       COALESCE(bi.reason_code, '')::text AS reason_code,
       bi.reason_text, bi.decided_by, bi.decided_at, bi.active, bi.created_at
  FROM billing.batch_invoice bi
 WHERE bi.tenant_id = sqlc.arg('tenant_id')
   AND bi.batch_id = sqlc.arg('batch_id')
   AND bi.invoice_id = sqlc.arg('invoice_id')
   FOR UPDATE;

-- name: SetBatchInvoiceDecision :execrows
-- The payer's answer to one invoice. `ck_billing_batch_invoice_decision` ties the amount to
-- the word and demands a reason for anything but a plain approval; the freeze trigger refuses
-- the write unless the batch is UNDER_REVIEW. Neither rule is repeated here.
UPDATE billing.batch_invoice
   SET decision        = sqlc.arg('decision'),
       approved_amount = sqlc.arg('approved_amount')::text::numeric,
       reason_code     = sqlc.narg('reason_code'),
       reason_text     = sqlc.narg('reason_text'),
       decided_by      = sqlc.arg('decided_by'),
       decided_at      = sqlc.arg('decided_at')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id');

-- name: ListBatchCandidateInvoices :many
-- The invoices a `putBatchInvoices` may name, answered for exactly the ids the caller sent,
-- with the batch each of them is already live in when there is one.
--
-- `live_batch_id` is answered rather than filtered out, so the refusal can name the icmal
-- instead of saying that an invoice the provider can see in their own list does not exist.
SELECT i.id, i.provider_organization_id, i.payer_organization_id, i.status, i.currency_code,
       i.domain_code, i.invoice_number, i.invoice_date,
       trim_scale(i.payable_amount)::text AS payable_amount,
       COALESCE((SELECT bi.batch_id
          FROM billing.batch_invoice bi
         WHERE bi.tenant_id = i.tenant_id AND bi.invoice_id = i.id AND bi.active
         LIMIT 1), '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS live_batch_id
  FROM billing.invoice i
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND i.id = ANY(sqlc.arg('invoice_ids')::uuid[])
 ORDER BY i.created_at, i.id;

-- name: SetInvoiceBatch :execrows
-- The invoice joins the icmal. There is no row_version: the concurrency control of this
-- transition is the batch's own If-Match and the row lock the submit already holds, and
-- demanding an ETag per invoice would make a batch of fifty unsubmittable whenever anybody
-- had touched any one of them.
UPDATE billing.invoice
   SET status     = 'IN_BATCH',
       batch_id   = sqlc.arg('batch_id'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'SUBMITTED';

-- name: SetInvoiceReviewStatus :execrows
-- Where one decision leaves the invoice. The whole precondition is in the predicate, so a
-- decision arriving against an invoice somebody else has meanwhile moved changes nothing.
UPDATE billing.invoice
   SET status     = sqlc.arg('status'),
       updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = ANY(sqlc.arg('from_statuses')::text[]);

-- name: CreateBatchInvoiceAdjustment :exec
-- The link between one CUT decision and the `claim.adjustment` row it wrote, so a changed
-- decision can take back exactly what it wrote rather than guessing by amount and reason.
INSERT INTO billing.batch_invoice_adjustment (
    tenant_id, batch_invoice_id, claim_id, adjustment_id, amount, created_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('batch_invoice_id'), sqlc.arg('claim_id'),
        sqlc.arg('adjustment_id'), sqlc.arg('amount')::text::numeric, sqlc.narg('actor_id'));

-- name: ListLiveBatchInvoiceAdjustments :many
-- What this member's current decision has written and nobody has taken back.
SELECT a.id, a.claim_id, a.adjustment_id, trim_scale(a.amount)::text AS amount
  FROM billing.batch_invoice_adjustment a
 WHERE a.tenant_id = sqlc.arg('tenant_id')
   AND a.batch_invoice_id = sqlc.arg('batch_invoice_id')
   AND a.reversed_by_adjustment_id IS NULL
 ORDER BY a.created_at, a.id;

-- name: SetBatchInvoiceAdjustmentReversed :exec
-- The row learns which reversal took it back. It is written once and never cleared: an
-- adjustment put back twice would give the money back twice.
UPDATE billing.batch_invoice_adjustment
   SET reversed_by_adjustment_id = sqlc.arg('reversed_by_adjustment_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND reversed_by_adjustment_id IS NULL;

-- name: TouchDraftBatch :execrows
-- The icmal's row version moves even though no column of the header changed: the ETag a caller
-- holds is a statement about the batch *and what it covers*, and a membership replaced under a
-- stale If-Match is exactly the race this guards. It is the same move `putInvoiceAllocations`
-- makes on the invoice, for the same reason.
UPDATE billing.batch
   SET updated_by = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND status = 'DRAFT'
   AND row_version = sqlc.arg('expected_row_version');
