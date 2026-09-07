# WP-I7-03 · The batch (icmal): built by the provider, immutable once submitted, decided invoice by invoice by the payer's finance

| Field                      | Value                                                                                                                                                                                                                                       |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M7 (plan increment I7)                                                                                                                                                                                                                      |
| Size                       | L                                                                                                                                                                                                                                           |
| Depends on                 | WP-I7-02 (submitted invoices), WP-I7-01 (adjustments), WP-I4-03 (work queues, SLA, maker-checker), WP-I4-05 (notifications)                                                                                                                 |
| Runs in parallel with      | WP-I7-05                                                                                                                                                                                                                                    |
| Migration numbers assigned | `000045_billing_batch.up.sql`                                                                                                                                                                                                               |
| OpenAPI operations owned   | `listBatches`, `createBatch`, `getBatch`, `putBatchInvoices`, `submitBatch`, `reviewBatchInvoice`, `decideBatch`, `getBatchSummary`                                                                                                        |
| Read first                 | v1.2 10.9 steps 4–8, 11.12, 16.8 (`billing.batch`, `billing.batch_invoice`), 12 (state machines), Faz 8; WP-I4-03 §2.4 (approval policy, the same person may not decide what they submitted), WP-I5-04 §2.3 (line decisions, the shape to copy for invoice decisions); plan v2.0 §2.3 (the batch decision becomes the commercial invoice's application response in M8 — leave the decision readable as ACCEPT/REJECT per invoice) |

## 1. Goal

The provider bundles its submitted invoices into a batch for one payer, one currency and
one domain; the payer's finance decides each invoice — approved, cut, returned or rejected
— and the decided totals are what settlement pays. Two rules carry the package: **a
submitted batch's invoices never change in place**, and **every decision is at invoice
level with its reason and, where it cuts, an adjustment row, visible to both sides and
audited.**

## 2. Scope

### 2.1 Schema (migration 000045)

`billing.batch`: id, tenant_id, `reference` (`IC-YYYYMM-XXXXXXXX`), `provider_organization_id`,
`payer_organization_id` NULL, `domain_code`, `currency_code`, `period_from`, `period_to`,
`status` (`DRAFT`, `SUBMITTED`, `UNDER_REVIEW`, `DECIDED`, `SETTLING`, `CLOSED`,
`CANCELLED`), `submitted_at`, `submitted_by`, `decided_at`, `decided_by`, `invoice_count`,
`submitted_total`, `approved_total`, `cut_total`, `returned_total`, `rejected_total` (exact,
recomputed by the server after each decision; `approved + cut + returned + rejected =
submitted` CHECK once decided), row_version. Unique reference per tenant.

`billing.batch_invoice`: batch_id, invoice_id, `submitted_amount`, `decision` NULL
(`APPROVE`, `CUT`, `RETURN`, `REJECT`), `approved_amount` NULL, `reason_code` NULL,
`reason_text` NULL, `decided_by`, `decided_at`; unique `(tenant_id, batch_id, invoice_id)`;
**partial unique `(tenant_id, invoice_id) WHERE batch is not CANCELLED`** — an invoice sits
in one live batch; a CHECK ties `decision` to `approved_amount` (`APPROVE` ⇒ equal to
submitted; `CUT` ⇒ below it and above zero; `RETURN`/`REJECT` ⇒ zero) and requires a reason
for anything but `APPROVE`.

A trigger freezes `batch_invoice` membership and `submitted_amount` once the batch is past
`DRAFT`; decisions are the only columns that move, and only while the batch is
`UNDER_REVIEW`. Tenant settings: `billing.batch_min_invoices` (1), `billing.batch_max_invoices`
(500), `billing.batch_decision_threshold` (the approved total above which `decideBatch`
needs a second person, default `100000`).

### 2.2 The provider's side

`createBatch` (`batch.create`, provider scope): payer, domain, currency, period.
`putBatchInvoices` replaces the DRAFT batch's set from the provider's `SUBMITTED` invoices
of that payer, domain and currency (a mismatch is `BATCH_MIXED` naming the field); the
count must be within the tenant's min/max at submit. `submitBatch` (`batch.submit`;
step-up above the threshold): `SUBMITTED`, the invoices `IN_BATCH`, the rows frozen, a
work item raised in the payer's finance queue (WP-I4-03, queue `BATCH_REVIEW`, SLA from
the tenant), `batch.submitted` to the outbox, `batch.submitted` notification to the
payer's finance.

### 2.3 The payer's review

Claiming the work item moves the batch to `UNDER_REVIEW`. `reviewBatchInvoice`
(`batch.review`): one decision per invoice — `APPROVE`; `CUT` with the approved amount and
a reason, which writes a `CUT` adjustment (WP-I7-01) on the invoice's claims in proportion
to their allocations, exact and summing to the cut, the rounding difference on the largest
allocation; `RETURN` with a reason, which sets the invoice `RETURNED` and frees its claims
for a corrected invoice (WP-I7-02's supersede); `REJECT` with a reason, which sets the
invoice `REJECTED` and its claims `CLOSED_UNPAID`. A decision may be changed while the
batch is `UNDER_REVIEW`; the last one stands and every change is an audit row.

`decideBatch` (`batch.review`, not the submitter, and above the threshold a second
reviewer who is not the one who made the last decision — WP-I4-03's rule): every invoice
decided, totals recomputed, `DECIDED`, the work item completed, `batch.decided` to the
outbox (WP-I7-04 opens the settlement on it), the provider told with the totals and each
return's reason. `getBatchSummary` answers the totals and the per-decision counts for
both sides; `getBatch` lists the invoices with their decisions and reasons.

### 2.4 The mock

`batch-handlers.ts`; a world with a draft, a submitted, an under-review with mixed
decisions, and a decided batch.

## 3. Tests required

- Submit freezes membership and amounts: an insert, delete or update on `batch_invoice`
  after submit is refused with the application bypassed.
- Mixed payer, currency or domain is refused at `putBatchInvoices`; min/max counts at
  submit are the tenant's.
- One live batch per invoice, proved with a concurrent double `putBatchInvoices`.
- A `CUT` writes adjustments summing exactly to the cut across the invoice's claims; the
  batch's totals add up to the submitted total after every decision; a changed decision
  reverses the earlier adjustment rather than editing it.
- `RETURN` frees the claims and the corrected invoice can be submitted and batched again;
  `REJECT` closes them unpaid.
- The submitter cannot decide; above the threshold the last reviewer cannot decide; the
  work item completes with the batch.
- Every decision is an audit row with the actor, the reason and the amounts.

## 4. Acceptance criteria

- [ ] Submitted batch invoices are never changed in place (Faz 8).
- [ ] Line-level cut/return/reject/approve is visible to both sides and audited (Faz 8).
- [ ] The decided totals are exact and reconcile to the submitted total.
- [ ] OpenAPI (additive), generated code, Spectral and `oasdiff` clean; Turkish for every
      problem code; schema version 45.
