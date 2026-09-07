# WP-I7-02 · The invoice as the provider enters it, and its allocation to claims

| Field                      | Value                                                                                                                                                                                                                                                          |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M7 (plan increment I7)                                                                                                                                                                                                                                         |
| Size                       | L                                                                                                                                                                                                                                                              |
| Depends on                 | WP-I7-01 (claims with approved totals, earnings), WP-I3-03 (`contract.payment_term`), WP-I4-04 (documents: the invoice image), M1 (organization VKN identifier)                                                                                                 |
| Runs in parallel with      | WP-I7-01 (contract first), WP-I7-05                                                                                                                                                                                                                            |
| Migration numbers assigned | `000044_billing_invoice.up.sql`                                                                                                                                                                                                                                |
| OpenAPI operations owned   | `listInvoices`, `createInvoice`, `getInvoice`, `patchInvoiceDraft`, `putInvoiceAllocations`, `submitInvoice`, `cancelInvoice`, `listInvoiceVersions`                                                                                                           |
| Read first                 | v1.2 9.14, 10.9 steps 1–3 and 8, 11.12, 16.8 (`billing.invoice`, `billing.invoice_claim`), 39; plan v2.0 §2.3 (what KAPSORA does not do), §2.8 ("Değişen mevcut tablolar": `billing.invoice` gains `source`, `edocument_id`, a GİB status projection in M8 — leave the columns nullable and unused now), §2.11 (line descriptions of a health invoice may carry clinical text: the sponsor's HR never sees them) |

## 1. Goal

The provider tells the payer what they are billing and which approved claims it covers.
Two rules carry the package: **the invoice's total and the sum of its allocations agree
within the tenant's tolerance or the invoice cannot be submitted**, and **once submitted,
the invoice's links never change — a correction is a new invoice that supersedes the old
one, and the old one stays.** KAPSORA produces no fiscal document here (that is the
e-document of M8, arriving through a port this package declares); it records what the
provider issued elsewhere and holds it against the claims.

## 2. Scope

### 2.1 Schema (migration 000044, `billing` schema)

`billing.invoice`: id, tenant_id, `provider_organization_id` (composite FK), `payer_organization_id`
NULL (the sponsor when the contract names one; else the tenant), `source` (`MANUAL`,
`EDOCUMENT`; only MANUAL is reachable now), `edocument_id` NULL (M8), `invoice_number`,
`invoice_date`, `fiscal_year` (derived from the date, stored), `provider_tax_id_hash` (the
VKN's blind index from the organization's identifier at creation; never the plaintext),
`currency_code`, `line_extension_amount`, `tax_amount`, `payable_amount` (exact;
`line_extension + tax = payable` CHECK), `vat_rate` NULL (as entered; not computed),
`domain_code`, `status` (`DRAFT`, `SUBMITTED`, `IN_BATCH`, `RETURNED`, `APPROVED`,
`PARTIALLY_APPROVED`, `REJECTED`, `SETTLED`, `CANCELLED`), `supersedes_invoice_id` NULL,
`superseded_by_invoice_id` NULL, `submitted_at`, `document_id` NULL (the scanned invoice
image, a clean WP-I4-04 document with `aggregate_type = 'INVOICE'`), `notes`, row_version.
**Unique `(tenant_id, provider_organization_id, fiscal_year, invoice_number) WHERE status
<> 'CANCELLED'`** (11.12: the number is unique in the provider's fiscal year).

`billing.invoice_claim`: invoice_id, claim_id, `claim_version_no`, `allocated_amount`,
`currency_code`; unique `(tenant_id, invoice_id, claim_id)`; partial unique
`(tenant_id, claim_id) WHERE active` so one claim sits on one live invoice; a deferred
constraint trigger refuses an allocation above the claim's approved total and any
allocation on a claim that is not `APPROVED`/`PARTIALLY_APPROVED`, with the currency
matching the invoice's.

`billing.invoice_status_event` is not added; audit and outbox carry transitions. A trigger
freezes `invoice_claim` rows and the invoice's amounts once `status <> 'DRAFT'` (the
immutability of 11.12 is the database's).

Tenant settings: `billing.allocation_tolerance` (an exact decimal amount, default `0.01`),
`billing.invoice_requires_image` (default true).

### 2.2 Commands

`createInvoice` (`invoice.manage`, provider scope): the header as entered; refuses a
provider whose organization has no VKN identifier (`PROVIDER_TAX_ID_MISSING`) and a
duplicate number in the fiscal year (`INVOICE_NUMBER_TAKEN`). `patchInvoiceDraft` edits the
header of a DRAFT. `putInvoiceAllocations` replaces the set of claim allocations of a DRAFT
from the provider's invoiceable claims (WP-I7-01's earnings view is the pick list); an
allocation above the claim's approved total is `ALLOCATION_EXCEEDS_APPROVED` naming the
claim. `submitInvoice`: the sum of allocations equals `payable_amount` within the tolerance
(`ALLOCATION_MISMATCH` carrying both figures and the difference), the image is attached
when the tenant requires it (`INVOICE_IMAGE_REQUIRED`), every allocated claim is still
invoiceable; then `SUBMITTED`, the claims move to `INVOICED`, the rows freeze, and
`invoice.submitted` goes to the outbox. `cancelInvoice` on a DRAFT or a RETURNED invoice
releases its claims.

A RETURNED invoice (WP-I7-03's reviewer) is corrected by `createInvoice` with
`supersedes: <id>`: the new draft starts with the old header and allocations, the old one
becomes `CANCELLED` with `superseded_by_invoice_id` set when the new one is submitted, and
its number may be reused only if the provider's own numbering says so (a different number
is the normal case; the same number is allowed only for the superseded chain).

`listInvoiceVersions` answers the supersede chain in order. `listInvoices` filters by
provider, status, fiscal year, date range, batch (from WP-I7-03), and answers each row
with its allocation total and the batch it sits in.

### 2.3 Visibility

The provider reads its own invoices; the payer's finance (`invoice.read`) reads all; the
sponsor's HR reads headers and totals of invoices for its members' claims but never a
claim line description — the same projection rule as WP-I5-01, applied to the invoice's
claim links.

### 2.4 The mock

`invoice-handlers.ts`, a world with a submitted invoice on two claims, a draft with a
mismatch, a returned one and its correction.

## 3. Tests required

- Unique number per provider and fiscal year, proved with the application bypassed; the
  same number across two providers is fine; a cancelled invoice frees its number.
- `line_extension + tax = payable` is the database's; an allocation above the approved
  total is refused by the deferred trigger with the service bypassed.
- Submit with a mismatch beyond the tolerance is refused with both figures; within the
  tolerance succeeds; the tolerance is the tenant's setting.
- After submit, updating an allocation or an amount is refused by the trigger; the claims
  read `INVOICED`; cancelling is refused.
- Supersede: the chain reads in order; the old invoice's claims move to the new draft;
  the old one is `CANCELLED` only when the new one is submitted.
- HR projection: the sponsor's HR reads the invoice with no line description.
- Idempotency: a replayed `submitInvoice` with the same key answers the same body.

## 4. Acceptance criteria

- [ ] Invoice allocation totals are validated against the invoice and the claims, with
      the tolerance the tenant set (Faz 8).
- [ ] A submitted invoice's links and figures never change; a correction is a new invoice
      in a chain the payer can read.
- [ ] OpenAPI (additive), generated code, Spectral and `oasdiff` clean; Turkish for every
      problem code; schema version 44.
