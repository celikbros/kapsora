# WP-I7-04 · Settlement that never exceeds the approved total, payment records with an external reference, and the member's reimbursement

| Field                      | Value                                                                                                                                                                                                                                                                                    |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M7 (plan increment I7)                                                                                                                                                                                                                                                                   |
| Size                       | L                                                                                                                                                                                                                                                                                        |
| Depends on                 | WP-I7-03 (decided batches), WP-I3-03 (`contract.payment_term`: due days, settlement method), WP-I2-03 (ledger consume), WP-I4-01 (`REIMBURSEMENT` request type), WP-I4-04 (the receipt document), WP-I6-04 (the member's PERSON scope), WP-I1-02 (step-up), the crypto module (bank account reference encrypted) |
| Runs in parallel with      | WP-I7-05                                                                                                                                                                                                                                                                                 |
| Migration numbers assigned | `000046_billing_settlement_payment_reimbursement.up.sql`                                                                                                                                                                                                                                 |
| OpenAPI operations owned   | `listSettlements`, `getSettlement`, `approveSettlement`, `listPaymentRecords`, `createPaymentRecord`, `listReimbursements`, `createReimbursement`, `getReimbursement`, `submitReimbursement`, `decideReimbursement`, `getMyReimbursements`                                                |
| Read first                 | v1.2 4.3 (KAPSORA pays no bank transfer itself), 9.14, 10.9 step 9, 10.10, 11.12, 16.8 (`billing.settlement`, `billing.payment_record`, `billing.reimbursement`); plan v2.0 §2.3 (the settlement produces the ERP posting and payment order in M9 — this package emits the event and keeps the columns M9 fills nullable), §2.7 (`PaymentConfirmation` flows back from the ERP into `payment_record` in M9; here a person enters it), §2.11 |

## 1. Goal

What the payer owes the provider, and what it pays the member back. Three rules carry the
package: **a settlement is opened from a decided batch and never exceeds its approved
total**; **a payment record is a finance fact with an external reference, and the sum of
records never exceeds the settlement**; **a reimbursement consumes the member's wallet only
when approved, and KAPSORA stores no bank secret — a reference through the cipher, and the
payment itself is somebody else's.**

## 2. Scope

### 2.1 Schema (migration 000046)

`billing.settlement`: id, tenant_id, `reference` (`ST-YYYYMM-XXXXXXXX`), `batch_id`
(composite FK; unique per batch and version), `version_no`, `provider_organization_id`,
`payer_organization_id` NULL, `currency_code`, `approved_amount` (the batch's), `withheld_amount`
(recoveries and earlier cuts netted, exact), `payable_amount` (`approved − withheld`
CHECK, ≥ 0), `due_date` (from the contract's payment term days after the batch's decision;
a term the contract does not carry is `PAYMENT_TERM_MISSING`), `settlement_method` (the
term's), `status` (`DRAFT`, `PENDING_APPROVAL`, `APPROVED`, `POSTED`, `PAID`, `PARTIALLY_PAID`,
`RECONCILED`, `CANCELLED`), `approved_by`, `approved_at`, `checked_by` NULL, `posting_id`
NULL (M9), `paid_amount` (server-maintained sum of records), row_version. **CHECK
`payable_amount <= approved_amount` and a trigger that refuses any change to
`approved_amount` after `DRAFT`.**

`billing.payment_record`: id, tenant_id, `settlement_id`, `external_reference` (the bank or
ERP reference), `amount`, `currency_code`, `paid_at`, `source` (`MANUAL`, `ERP`), `status`
(`RECORDED`, `RECONCILED`, `DISPUTED`), `recorded_by`, `notes`; unique
`(tenant_id, provider_organization_id, external_reference)`; **a deferred constraint trigger
refuses a record that would take `paid_amount` above `payable_amount`**; append-only (a
wrong record is `DISPUTED`, never deleted).

`billing.reimbursement`: id, tenant_id, `reference`, `person_id`, `enrollment_id`,
`service_request_id` (a `REIMBURSEMENT` request of WP-I4-01, which carries the service,
the date, the requested amount and the receipt document), `claim_id` NULL (WP-I7-01's
`REIMBURSEMENT` claim, created at approval), `receipt_document_id`, `requested_amount`,
`approved_amount` NULL, `currency_code`, `bank_account_ref_enc` (an IBAN through the
platform cipher; its last four characters as `bank_account_masked`; **no other column
ever holds it**), `status` (`DRAFT`, `SUBMITTED`, `UNDER_REVIEW`, `APPROVED`, `PARTIALLY_APPROVED`,
`REJECTED`, `PAYMENT_ORDERED`, `PAID`, `CANCELLED`), `duplicate_of_id` NULL, `payment_reference`
NULL, `paid_at` NULL, row_version. Partial unique on `(tenant_id, person_id,
receipt_document_id)` for live rows — one claim per receipt.

### 2.2 Settlement

The outbox handler of `batch.decided` opens one `DRAFT` settlement per decided batch
with the approved total, the withheld amount from open recoveries against the provider
(WP-I7-01's `RECOVERY` adjustments not yet netted; each netted one is marked), the due
date from the payment term, and moves it to `PENDING_APPROVAL`. `approveSettlement`
(`settlement.approve`; step-up; above `billing.settlement_checker_threshold` a second
person who is not the batch's decider — the same maker-checker as WP-I4-03): `APPROVED`,
`settlement.approved` to the outbox (M9's posting listens; nothing is posted here), the
provider told. A settlement is never edited: a wrong one is `CANCELLED` with a reason and
a new version opened on the same batch.

### 2.3 Payment records

`createPaymentRecord` (`settlement.approve` or a new `settlement.record_payment`
permission — add it in BOTH places, granted to the finance role): external reference,
amount, date; the settlement's `paid_amount` moves; `PARTIALLY_PAID` while short of
`payable_amount`, `PAID` when equal, a record that would exceed it refused with
`PAYMENT_EXCEEDS_SETTLEMENT` carrying the remainder. `RECONCILED` is WP-I7-05's job.

### 2.4 Reimbursement

The member (`service_request.create` on their own person) submits a `REIMBURSEMENT`
request with the receipt (a clean document), the service, the date and the amount — this
is WP-I4-01's request with its gate; this package adds `createReimbursement` /
`submitReimbursement` as the member-facing wrapper that also takes the IBAN through the
cipher, and the checks 10.10 names: eligibility on the date, the receipt present and
clean, the amount within the contract's reimbursement ceiling for the service when one
exists, and duplicates (the same person, the same receipt hash or the same
provider-date-amount triple within the tenant's window) as `REIMBURSEMENT_DUPLICATE`
naming the earlier one. `decideReimbursement` (`claim.review` + the financial stage's
permission): approve in full, in part with a reason, or reject; approval creates the
`REIMBURSEMENT` claim (WP-I7-01) already decided and **consumes the approved amount from
the member's entitlement account through the ledger** (a MONEY account; a plan with no
money entitlement for the service is `ENTITLEMENT_ACCOUNT_NOT_FOUND`); then
`PAYMENT_ORDERED` and `reimbursement.approved` to the outbox for the payment adapter (M9;
a `PaymentOrderPort` is declared here with a no-op recording implementation), and a
payment reference entered by finance (`recordReimbursementPayment` — add to the owned
operations) marks it `PAID`. `getMyReimbursements` is the member's own list, resolved
through the PERSON scope.

### 2.5 Notifications

`settlement.approved` (provider: reference, amount, due date), `payment.recorded`
(provider), `reimbursement.decided` (member: amount, no bank detail), `reimbursement.paid`
(member: masked account). Templates seeded, safe variables only; `amount` and `currency`
already exist; nothing carries a reference number longer than the catalogue allows.

### 2.6 The mock

`settlement-handlers.ts`, `reimbursement-handlers.ts`; a world with a pending, an approved
and a partially paid settlement, and reimbursements in every reachable status for the
bound member.

## 3. Tests required

- `payable <= approved` and the freeze of `approved_amount` are the database's; a
  settlement opened from a batch equals its approved total minus the netted recoveries,
  exactly, and a second `batch.decided` delivery opens nothing.
- The sum of payment records never exceeds the settlement, proved with a concurrent
  double record; the status follows the sum.
- The batch's decider cannot approve the settlement above the threshold; step-up is asked.
- Reimbursement: a duplicate receipt is refused naming the earlier one; approval consumes
  exactly the approved amount (ledger conservation); rejection consumes nothing; the IBAN
  appears in no column but `bank_account_ref_enc`, in no audit detail, log or response
  beyond the masked tail (a sweep of the schema for the test IBAN finds only the
  ciphertext).
- The member reads only their own reimbursements; another member's is 404.

## 4. Acceptance criteria

- [ ] Settlement never exceeds the batch's approved total (Faz 8).
- [ ] A payment is a record with an external reference; the sum of records never exceeds
      the settlement; nothing is deleted.
- [ ] A member is reimbursed from their wallet, on approval, with no bank secret stored.
- [ ] OpenAPI (additive), generated code, Spectral and `oasdiff` clean; Turkish for every
      problem code; permissions in both places; schema version 46.
