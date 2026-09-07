# WP-I7-01 · Claims across verticals: the lodging claim from the door, adjustments as ledger lines, the provider's earnings view

| Field                      | Value                                                                                                                                                                                                                                                              |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Milestone                  | M7 (plan increment I7)                                                                                                                                                                                                                                             |
| Size                       | M                                                                                                                                                                                                                                                                  |
| Depends on                 | WP-I5-04 (claim, versions, lines, line decisions, adjustments, readiness), WP-I6-03 (check-out fulfilment, cancellation and no-show fees), WP-I4-02 (fulfilments)                                                                                                  |
| Runs in parallel with      | WP-I7-02                                                                                                                                                                                                                                                           |
| Migration numbers assigned | `000043_claim_sources_and_adjustments.up.sql`                                                                                                                                                                                                                      |
| OpenAPI operations owned   | `listClaimAdjustments`, `createClaimAdjustment`, `getProviderEarnings`; changes `Claim` (adds `sourceType`, `sourceId`) and `CreateClaim` (accepts `bookingId`) — additive only                                                                                    |
| Read first                 | v1.2 9.14, 10.8, 16.8 (`adjudication.adjustment`), 39; WP-I5-04 as delivered (`internal/claim/`, migration 000034: `claim.claim.domain_code`, nullable `case_id`, `claim.adjustment`), WP-I6-03 §2.2–2.4 (the fee rows on `accommodation.cancellation` and `no_show`), `internal/accommodation/application/stay.go` `settleStay` |

## 1. Goal

M5 built the claim for the health vertical and stopped at "approved and invoice-ready".
Before an invoice can be entered against claims, two things have to be true for every
vertical: **a completed stay produces a claim exactly as a treated encounter does**, and
**every cut, recovery or correction is an adjustment row with a payer/member split, never
an edit of a decided figure.** This package makes the claim the one billable unit of the
platform and gives the provider the view they will reconcile their invoice against.

## 2. Scope

### 2.1 Schema (migration 000043)

`claim.claim` gains `source_type` (`HEALTH_CASE`, `BOOKING`, `REIMBURSEMENT`; CHECK) and
`source_id` (the case, booking or reimbursement request id; composite FK enforced per type
by a trigger that reads `source_type`), with the existing `case_id` kept and backfilled
(`source_type = 'HEALTH_CASE'`, `source_id = case_id` for every existing row); partial
unique `(tenant_id, source_type, source_id) WHERE source_type = 'BOOKING' AND status NOT IN
('CANCELLED', 'REJECTED')` — one live claim per booking.

`claim.adjustment` gains `payer_amount` and `member_amount` (exact, `payer + member =
amount` CHECK), `claim_line_id` NULL (a line-level adjustment names its line),
`source_type`/`source_id` (`CANCELLATION`, `NO_SHOW`, `REVIEW`, `MANUAL`, `RECOVERY`) so a
fee row of WP-I6-03 and a reviewer's cut of WP-I7-03 are the same kind of thing, and
`created_by` NOT NULL. `adjustment_type` keeps its values; a `REVERSAL` row reverses another
by `reverses_adjustment_id` — nothing is deleted.

### 2.2 The lodging claim

On the outbox event that WP-I6-03's check-out publishes (`booking.checked_out`; add it if
the package left only the status change — the claim must not be created inside the
check-out transaction), one claim is created for the booking: `domain_code =
'ACCOMMODATION'`, `source_type = 'BOOKING'`, the provider the property's, one line per
night slept at the booking's own `booking_night.unit_amount` (the frozen quote, never
re-priced), quantity `1 NIGHT` each, `requested_amount` the night's amount, and the
authorization the booking's. Lines for the nights the plan carried are decided APPROVED
at the line's payer amount by the system at creation (the decision was taken at
confirmation and carries `reason_code = 'BOOKING_CONFIRMED'`); a night the plan did not
carry is a line with `payer_amount = 0` and the member's amount, so the invoice can still
carry it where the contract bills the member's share through the payer. The claim lands
in `PENDING_FINANCIAL` for the financial reviewer's pass, exactly as a health claim does
after its medical stage; its readiness rules are the ones WP-I5-04 wrote.

A confirmed no-show (WP-I6-03 `reviewNoShow` → `NO_SHOW`) and a penalised cancellation
produce the same claim shape with one line (`NO_SHOW_FEE` / `CANCELLATION_FEE`, quantity
`1`, the assessed fee split as the row already carries) and an adjustment row of source
`NO_SHOW` / `CANCELLATION` linking back to the fee row. The claim is created by the outbox
handler of `booking.no_show_confirmed` / `booking.cancelled` (published by WP-I6-03; add
them where missing).

### 2.3 Adjustments as commands

`createClaimAdjustment` (`claim.review` on the payer side): on a decided claim, a `CUT`,
`RECOVERY` or `CORRECTION` with its payer/member split, its reason code from a closed list
and an optional line; the claim's approved total is recomputed from lines minus
adjustments on the server and exposed on readiness as `approvedTotal` and
`adjustmentTotal`. A `REVERSAL` of an existing adjustment is the only way to undo one.
`listClaimAdjustments` lists them with the actor and the reversal chain. Every adjustment is
an audit row and a `claim.adjusted` outbox event.

### 2.4 The provider's earnings view

`getProviderEarnings` (`GET /api/v1/providers/{providerId}/earnings?from&to&currency`,
`claim.read` with the provider's own scope or the payer's): per currency, the claims
decided in the period grouped by status, with `approvedTotal`, `adjustmentTotal`,
`invoiceableTotal` (approved, not yet on an invoice — WP-I7-02 fills the link), and the
list of invoiceable claim ids. It is the figure the provider's invoice will be checked
against, computed once on the server as exact decimals.

### 2.5 The mock

`claim-handlers.ts` grows the source types, adjustments and the earnings view; the world
gains a lodging claim from the seeded COMPLETED booking, a no-show claim, and adjustments
on a health claim.

## 3. Tests required

- A check-out produces exactly one claim with one line per night slept at the frozen
  night amount; a second check-out event (at-least-once delivery) produces nothing new.
- The partial unique index: two live claims for one booking are refused with the
  application bypassed.
- A no-show confirmation and a penalised cancellation each produce a claim whose line
  equals the fee row's split; a free cancellation produces nothing.
- `payer + member = amount` on adjustments is the database's; a reversal restores the
  approved total exactly; a reversal of a reversal is refused.
- Approved total = lines − adjustments, exact, on readiness, after every command.
- Earnings: the invoiceable total equals the sum of approved totals of claims not yet
  linked, per currency, and a claim in two currencies is refused at creation.

## 4. Acceptance criteria

- [ ] Every completed stay, confirmed no-show and penalised cancellation is a claim the
      provider can invoice, with the figures the booking froze.
- [ ] A cut is an adjustment row with a split and a reason, reversible only by a reversal.
- [ ] The provider's earnings view answers the invoiceable total the invoice must match.
- [ ] OpenAPI (additive), generated code, Spectral and `oasdiff` clean; Turkish for every
      problem code; schema version 43.
