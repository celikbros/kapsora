# WP-I10-05 · Pilot migration and user acceptance

| Field | Value |
| --- | --- |
| Milestone | M10 |
| Status | PLANNED; pilot owner and group not yet named |
| Depends on | I10-01–04 evidence; M1–M7 product flows |
| Migration numbers | None assigned; pilot data loading is not a schema migration |
| API ownership | Existing operations and approved integration contracts |
| Read first | `README.md`, `../plan/ROADMAP.md`, baseline §§40, 48 |

## Goal

A named pilot institution accepts the agreed benefit journeys and reconciled starting data,
with an operator-ready support and recovery process.

## Inputs and preparation

- Identify institution, program, benefit/sector scope, beneficiary group, provider users,
  acceptance owners and synthetic source samples. Do not place actual personal records in git.
- Agree the covered flows, success measures and exclusions before loading pilot data.
- Rehearse migration through I10-01 with synthetic data, then reconcile people, enrollments,
  opening balances, outstanding requests and amounts. Every difference has an explanation
  and an accountable acceptance decision; reruns do not duplicate records.
- Prepare training, support contacts, rollback criteria and the operator's go-live checklist
  in existing runbooks. Execute migration/go-live only for the designated environment.

## User journeys to prove against the real API

1. Institution: person/enrollment → plan → eligibility → entitlement balance.
2. Healthcare: provider request → document scan → medical/financial review → claim.
3. Accommodation: availability → hold → confirmation → arrival or cancellation/no-show,
   including partial cover and concurrent requests for the last room.
4. Finance: invoice → batch decisions → settlement → payment record → reconciliation/export.
5. Member: reimbursement request → clean receipt → submission → review and payment.
6. Permission boundaries: HR cannot read clinical details; a reviewer cannot decide their
   own record; users cannot cross tenant or application boundaries.

Browser evidence must include completed document scans and successful submission, not only
the mock refusal while a file stays pending. Existing unit evidence remains complementary.

## Acceptance and exclusions

- Signed UAT identifies participants, environment/version, outcomes and unresolved issues.
- I10-02 performance, I10-03 restoration and I10-04 security evidence are linked explicitly.
- Data reconciliation is exact or has approved, explained differences.
- M8 e-document and M9 accounting integrations remain deferred by the owner. Clearly state
  the financial integration limits of a pilot that precedes them. Technical M10 preparation
  does not prove full MVP completion where the master plan requires their fiscal criteria.
- M11 features are outside this package. Missing customer inputs keep customer acceptance
  open; they do not stop preparation of scenarios, tooling and synthetic rehearsals.
