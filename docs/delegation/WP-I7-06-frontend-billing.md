# WP-I7-06 · Screens: the provider's earnings, invoice and batch; the payer's batch review, settlement and payments; the member's reimbursement; the statement and the exports

| Field                      | Value                                                                                                                                                                                                                                                                                           |
| -------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M7 (plan increment I7)                                                                                                                                                                                                                                                                          |
| Size                       | L                                                                                                                                                                                                                                                                                               |
| Depends on                 | contracts of WP-I7-01..05 (mock first, real API when they land)                                                                                                                                                                                                                                 |
| Runs in parallel with      | all M7 packages                                                                                                                                                                                                                                                                                 |
| Migration numbers assigned | none                                                                                                                                                                                                                                                                                            |
| OpenAPI operations owned   | none (consumes)                                                                                                                                                                                                                                                                                 |
| Read first                 | `DESIGN.md` (all settled patterns — fifty-five entries after M6) and `PRODUCT.md`; WP-I5-06 and WP-I6-05 as delivered and their finish-review notes in ROADMAP.md; the three surface briefs under `web/apps/*/.impeccable/surfaces/`; the claim screens (`web/apps/*/src/claims/`) which these extend |

## 1. Goal

Money leaves the plan here, so every screen is a ledger page: what was billed, what was
decided, what is owed, what was paid, in exact figures the server produced, with the
person who decided each one. The milestone is judged by two of these: **a reviewer's
cut, return, reject or approve is visible with its reason on both sides**, and **a
settlement can never be approved above the batch's approved total** — the second is the
server's, but the screens must make the total, the withheld amount and the payable
amount read as one arithmetic line nobody on the client performed.

## 2. Scope

### 2.1 Provider portal (extends the form-first world)

1. **Hakediş** — the earnings view per period and currency: approved, adjusted,
   invoiceable; the invoiceable claims as the pick list for a new invoice.
2. **Fatura** — the manual entry: number, date, totals as typed, the image upload through
   the document panel, the allocation table over the picked claims with the running
   allocation total beside the payable amount and the difference the server named;
   submit; the supersede path from a returned invoice ("Düzelt" opens the new draft with
   the old figures).
3. **İcmal** — build from submitted invoices (the mixed refusal named by field), submit
   with step-up, read the decision per invoice with its reason once decided.
4. **Cari ekstre** — the statement: rows and totals as the server sends them; "Dışa
   aktar" queues an export and shows its state until the download is ready.

### 2.2 Backoffice (extends the request-detail sequence)

1. **İcmal incelemesi** — the batch from the finance queue: invoices with their claims
   and lines (the financial projection; HR never sees a line description), one decision
   per invoice with the amount and the reason, the running totals (approved · cut ·
   returned · rejected = submitted) and Karar ver with the maker-checker refusal named.
2. **Settlement** — the list by status and due date; the detail as the sequence: the
   batch decided, the withheld recoveries listed, the payable amount, approval with
   step-up and the checker rule, the payment records entered with their external
   reference and the remainder the server computes; the reconciliation run that closed it.
3. **Geri ödeme incelemesi** — the member's reimbursement with its receipt, the
   eligibility answer, the duplicate warning naming the earlier one, approve in full or
   in part with a reason, reject; the payment reference entered by finance.
4. **Mutabakat** — the runs by day and provider, differences as rows with their kind;
   "Dışa aktar" with the sensitive gate on CLAIMS.
5. **Ana sayfa** gains the operations dashboard figures, each linking to the list it
   counts.
6. Person detail's Sağlık and Konaklama tabs gain the reimbursements and the invoices
   the person's claims sit on (figures only for HR).

### 2.3 Member PWA (extends the receipt world)

1. **Geri ödeme iste** — the receipt as a photo or file, the service, the date, the
   amount, the IBAN typed once and shown masked after; submit with the duplicate warning
   as the server's sentence.
2. **Geri ödemelerim** — each request as it is: what was asked, what was decided and why,
   what was paid to which masked account and when.

## 3. Design notes

- **An arithmetic line is shown, never performed.** Approved − withheld = payable and
  submitted = approved + cut + returned + rejected appear as the server's figures on one
  line; the screen never adds them.
- **A decision shows its reason where the amount is.** Cut and returned amounts carry
  their reason inline, not in a tooltip.
- **A refusal names the field** (`BATCH_MIXED`), the claim (`ALLOCATION_EXCEEDS_APPROVED`),
  the earlier request (`REIMBURSEMENT_DUPLICATE`), or the remainder
  (`PAYMENT_EXCEEDS_SETTLEMENT`).
- **A secret typed once is shown masked after**: the IBAN field behaves like the voucher
  token in reverse.
- **An export is a state, then a download**: queued, running, ready with its expiry,
  expired.
- Everything else follows DESIGN.md's settled patterns; no new visual world — every
  surface here extends an established one.

## 4. Tests required

- Mock world: earnings, invoices in every status incl. a returned one and its
  correction, batches in every status with mixed decisions, settlements pending, approved
  and partially paid, reimbursements in every status for the bound member, two
  reconciliation runs, an export that becomes ready.
- Vitest flows: the allocation table's running total is the server's and a mismatch shows
  the server's difference; a batch decision writes its reason inline and the totals
  line updates from the server; a settlement above the checker threshold shows the
  second-person rule; a payment record beyond the remainder shows the server's refusal;
  the IBAN is shown masked after submission and appears in no storage; HR sees no line
  description on an invoice; the export's states.
- Playwright: a provider enters an invoice, allocates, submits, builds a batch and
  submits it; a reviewer decides the batch with one cut and reads the totals; a member
  submits a reimbursement.
- `pnpm design` clean; Impeccable's finish review as the skill directs; captures at 390
  and 1440; i18n complete in Turkish. **Update `DESIGN.md` in the same commit as any
  screen that settles a pattern.**

## 5. Acceptance criteria

- [ ] Line-level cut/return/reject/approve is visible with its reason on both sides.
- [ ] No screen ever performs the settlement arithmetic; the figures are the server's.
- [ ] A member can ask to be reimbursed from a phone and read what happened.
- [ ] Lint, typecheck, unit, smoke, the detector and the finish review all clean.
