# WP-I5-04 · Health claim: versions, lines, auto-adjudication, medical and financial review

| Field                      | Value                                                                                                                                                                                                                                                                                                                  |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M5 (plan increment I5)                                                                                                                                                                                                                                                                                                 |
| Size                       | L                                                                                                                                                                                                                                                                                                                      |
| Depends on                 | WP-I5-01, WP-I5-02 (report coverage port), WP-I5-03 (stay reconciliation), WP-I4-02 (authorization consume), WP-I3-05 (pricing), WP-I3-04 (rules), WP-I4-03 (queues, approval policy)                                                                                                                                  |
| Runs in parallel with      | WP-I5-06 (mock first)                                                                                                                                                                                                                                                                                                  |
| Migration numbers assigned | `000034_health_claim.up.sql`                                                                                                                                                                                                                                                                                           |
| OpenAPI operations owned   | `listClaims`, `createClaim`, `getClaim`, `patchClaimDraft`, `putClaimLines`, `submitClaim`, `returnClaim`, `decideClaimLines`, `approveClaim`, `rejectClaim`, `cancelClaim`, `listClaimVersions`, `getClaimVersion`, `getClaimInvoiceReadiness`                                                                            |
| Read first                 | v1.2 9.14 (claim part only), 10.3 steps 5–7, 12.5, 16.8 (`claim.*` rows), 11.10; WP-I4-01 (the version model to copy), WP-I4-02 (`Consume`), WP-I3-05 (`internal/pricing` — the quote arithmetic, `AccountKey`), WP-I4-03 §2.4 (approval policy lookup); ROADMAP.md's M4 notes on the mock and on names being ids |

## 1. Goal

A claim is the provider's statement of what was delivered and what it costs, and the
payer's answer to it line by line. Two rules carry the package: **a submitted version is
never edited — a correction is a new version and the old decision is kept — and every line
carries its own decision with its own reason.** The invoice, the batch and settlement are
M7; this package ends at "approved and invoice-ready", which is what the outpatient
criterion asks for.

## 2. Scope

### 2.1 Schema (migration 000034, `claim` schema)

`claim.claim`: id, tenant_id, `reference` (unique per tenant), person_id, program_id,
enrollment_id, `provider_organization_id` (NOT NULL: a claim always has a provider),
`domain_code` (`HEALTH` now; the column exists for M6/M7), case_id NULL, `fulfilment_id`
NULL (WP-I4-02), `authorization_id` NULL, `current_version_no`, `status` (12.5:
`DRAFT`,`SUBMITTED`,`AUTO_ADJUDICATED`,`PENDING_MEDICAL`,`PENDING_FINANCIAL`,`RETURNED`,
`PARTIALLY_APPROVED`,`APPROVED`,`REJECTED`,`INVOICED`,`BATCHED`,`SETTLED`,`CANCELLED`),
`service_date_from`, `service_date_to`, `channel`, `reject_reason_code`, `return_reason_code`,
`review_comment_medical` text NULL (HEALTH; clinical projection), `review_comment_financial`
text NULL, `closed_at`, row_version. Statuses from `INVOICED` on are declared here and
unreachable here; M7 owns the commands that reach them.

`claim.claim_version`: id, tenant_id, claim_id, `version_no`, `status` (`DRAFT`,
`SUBMITTED`,`SUPERSEDED`), `submitted_at/by`, `returned_at/by`, `return_reason_code/text`,
`snapshot jsonb` (the lines as submitted, frozen), row_version. Unique `(tenant_id,
claim_id, version_no)`.

`claim.claim_line`: id, tenant_id, version_id, `line_no`, `service_definition_id`,
`unit_type`, `quantity numeric(20,6)`, `unit_amount numeric(20,6)` NULL, `line_amount
numeric(20,6)`, `currency_code`, `diagnosis_id` NULL (into `health.diagnosis` — clinical
projection only), `medical_report_id` NULL, `practitioner_id` NULL, `description` text
NULL — **the free-text line description v1.2 marks as possibly clinical: it belongs to the
clinical projection**, the financial reviewer sees it, sponsor HR does not. Unique
`(tenant_id, version_id, line_no)`.

`claim.line_decision`: id, tenant_id, line_id, `decided_in_version_no`, `decision`
(`APPROVED`,`PARTIALLY_APPROVED`,`REJECTED`,`CUT`), `approved_quantity`, `approved_amount`,
`contract_amount` NULL, `payer_amount`, `member_amount`, `reason_code`, `reason_text`,
`decided_by` NULL (null = the rules), `decided_at`, `stage` (`AUTO`,`MEDICAL`,`FINANCIAL`).
Append-only: a line decided twice has two rows, and the latest for a version is the
decision. Money as exact decimals; `payer_amount + member_amount = approved_amount`
CHECKed.

`claim.adjustment`: id, tenant_id, claim_id, version_no, `adjustment_type` (`CUT`,
`RECOVERY`,`CORRECTION`), `amount numeric(20,6)`, `reason_code`, `reason_text`, `created_by`.
Append-only. Declared here; M7's settlement reads it.

RLS, touch triggers where mutable, composite keys; `grant_app_schema_usage('claim')`.

### 2.2 Submit: price, then rules, then route

`submitClaim` in one transaction, after freezing the snapshot:

1. **Price** every line through WP-I3-05's calculator (contract price, plan share,
   member share) for the claim's provider, service date and enrollment; a line the
   ladder cannot price is `REVIEW_REQUIRED` with the pricing explanation attached.
2. **Rules**: evaluate the tenant's published `CLAIM` rule set (WP-I3-04) with the claim
   and its priced lines as input; actions may `REQUIRE_MEDICAL_REVIEW`,
   `REQUIRE_FINANCIAL_REVIEW`, `REJECT_LINE`, `CUT_LINE`, `AUTO_APPROVE`.
3. **Cross-checks that are not rules**: a line against an authorization consumes it (WP-I4-02
   `Consume`; over-consumption is an exception, never silent); a line naming a medical
   report calls WP-I5-02's coverage port (out of scope → medical review); a stay's
   reconciliation flag `over_authorization` (WP-I5-03) → medical review; a duplicate
   (same person, same service, same date, another claim not REJECTED/CANCELLED) → financial
   review with `DUPLICATE_SUSPECTED`.
4. **Route**: no review needed and the approval policy (WP-I4-03 §2.4) allows the amount
   → `AUTO_ADJUDICATED` then `APPROVED` with AUTO line decisions; medical needed →
   `PENDING_MEDICAL` and a work item in the medical queue; else financial needed →
   `PENDING_FINANCIAL`. Medical review precedes financial when both are needed.

Every step writes a `line_decision` with `stage = 'AUTO'` where it decided something.

### 2.3 Review, as two different screens' worth of data

`decideClaimLines` takes per-line decisions for the version and records them with the
caller's stage: a MEDICAL_REVIEWER (`claim.medical.review`) decides on clinical grounds and
sees the clinical projection; a FINANCIAL_REVIEWER (`claim.financial.review`) decides on
money and sees the financial projection — **different fields, as v1.2 11.10 states**. The
approval policy's `required_role_codes` and amount band decide which stage may finish the
claim; `approveClaim` / `rejectClaim` / `returnClaim` are the claim-level commands, each
with a reason. `returnClaim` opens version n+1 as a DRAFT exactly as WP-I4-01 does.

### 2.4 Invoice-ready

`getClaimInvoiceReadiness` answers, for an `APPROVED` or `PARTIALLY_APPROVED` claim, the
approved total, the payer total, the member total (exact decimals, summed on the server
once) and the list of what M7's invoice will need — provider tax identity present,
currency single, no undecided line — as `ready: boolean` with named blockers. M7 consumes
it; this package proves it exists and is true.

### 2.5 Visibility

Two projections, as WP-I5-01: the clinical one adds `diagnosis_id`, `description`,
`medical_report_id` and `review_comment_medical`; sponsor HR gets neither the lines'
descriptions nor any diagnosis reference. Every clinical-projection read is an access
event with `resource_type = 'CLAIM'`.

### 2.6 Permissions

`claim.read`, `claim.create`, `claim.submit`, `claim.medical.review`,
`claim.financial.review` are seeded; add `claim.cancel` (NORMAL). Verify all six in
`roles.go` (PROVIDER_BILLING creates/submits/cancels; both reviewers review; PROGRAM_MANAGER
reads; SPONSOR_HR reads the financial projection) with the two-halves test.

### 2.7 The mock

`claim-handlers.ts` mirroring the submit pipeline with the mock's own pricing and rules
(reuse `pricing-handlers`/`rules-handlers` helpers), the version model, line decisions by
stage, the two projections, and readiness; a world with claims in every reachable status.

## 3. Tests required

- **A correction is a new version and the old decision is kept**: submit, financial
  reviewer cuts line 2, return; version 2 is DRAFT with the lines copied, version 1 is
  SUPERSEDED and its line decisions are unchanged and still readable.
- **Outpatient end to end, invoice-ready**: a case with one encounter, a fulfilment against
  an authorization, a claim with two lines, one auto-approved and one needing financial
  review, decided, approved; readiness answers `ready: true` with totals that equal the sum
  of the line decisions and `payer + member == approved` on every line; the authorization's
  consumed total moved by exactly the approved quantity.
- Over-consumption of an authorization is an exception line, never a silent consume.
- Duplicate suspicion routes to financial review with the other claim's reference.
- Medical before financial when both are needed; the financial reviewer cannot decide a
  clinical line's reason and sees no description.
- The sponsor-HR scan over a claim with descriptions and diagnoses.
- Ledger conservation after every flow (WP-I4-02's assertion).

## 4. Acceptance criteria

- [ ] An outpatient claim goes from provider draft to invoice-ready with every line's
      decision, reason and stage on the record.
- [ ] A correction never edits a decided version.
- [ ] Medical and financial reviewers see different fields; sponsor HR sees no clinical one.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean; Turkish for every problem
      code; schema version 34.
