# WP-I10-02 · Load tests and performance corrections

| Field | Value |
| --- | --- |
| Milestone | M10 |
| Status | ACTIVE — tooling and four real-API smoke flows verified; full acceptance open |
| Depends on | M1–M7; I10-01 for its final import workload |
| Migration numbers | None assigned; index changes require forward migrations |
| API ownership | Existing operations; any contract change is reviewed first |
| Read first | `README.md`, `../baseline-v1.2/KAPSORA_Teknik_Proje_Dokumani_v1.2.md` §§30.4, 32.1 |

## Goal

Produce reproducible evidence of the product's latency, throughput and integrity at the
specified capacity, and fix the measured bottlenecks without weakening tenant boundaries.

## Scope

- Add k6 workloads for reads, normal writes, eligibility, booking holds and imports using
  synthetic tenants and authorized accounts. Exercise CSRF, idempotency and app context.
- Define the operation mix, warm-up, sustained duration, machine sizing, database size and
  resource limits before a measured run. Save the workload version with each result.
- Capacity baseline: 1,000,000 active beneficiaries; 10,000,000 historical core records;
  5,000 member sessions; 200 backoffice/provider users; 300 peak API requests per second;
  100,000 import rows per day. These are target conditions, not claims about the current laptop.
- Thresholds from §30.4: read p95 <300 ms, write p95 <700 ms, eligibility p95 <1.5 s,
  booking hold p95 <1 s, critical notification enqueue <5 s and outbox oldest pending <2 min.
  Exclude third-party latency only where the specification explicitly permits it.
- Profile failed targets before changing queries, indexes or concurrency. Keep exact money,
  nonnegative inventory and entitlement balances, RLS and audit behavior intact.
- If database test provisioning is the measured development bottleneck, assess template
  databases separately; do not alter the running application's credentials or database.

## Required verification and exit evidence

- Each workload has a small local smoke mode and the full acceptance mode. A smoke pass
  never counts as full-capacity acceptance. Run high-load jobs only on a designated target.
- Capture p50/p95/p99, throughput, refusals versus unexpected errors, pool/lock waits,
  resource usage, scan backlog and outbox age. State actual sample sizes and duration.
- Reconcile balances and reservations after contention; prove no oversell or duplicate spend.
- Record baseline, corrections and final results with reproducible commands in the report.
  Unmet thresholds remain open, with measured causes and next actions.
- Performance environment and its resource budget must be named before the full run.

## Delivery status — 2026-09-13

The first tooling slice adds pinned k6 installation, local synthetic fixture discovery,
five workflows, a deliberately small smoke mode and a bounded load profile. Session/CSRF,
step-up, idempotency, cleanup and exact final balance/inventory reconciliation are exercised.
The model tests run in CI. See the [load-testing runbook](../runbooks/load-testing.md).

Real API evidence: read, draft-write/cancel, eligibility and hold/release pass; final room
inventory and exact entitlement balances match the initial snapshot. This is four-flow
functional evidence, not a latency or capacity acceptance result.

The original staging smoke was blocked by the missing real-role grant. On 2026-09-21 the
owner approved import access for PROGRAM_MANAGER; migration 000049 and the provisioning
template now supply it. A PostgreSQL-backed HTTP test covers upload, step-up, apply and
worker replay. The CSV fixture uses PRINCIPAL, matching CSV_V1. The live five-workflow k6
smoke still needs operator-started services; do not confuse the HTTP integration test with
that measurement. Required grants are checked before mutations; the harness never grants
permissions. Reduced smoke reports omitted coverage; load mode requires all five.

Full-capacity data/session provisioning, import apply/adapter coverage, server telemetry,
designated-environment load execution and measured performance corrections remain open.
No M10 exit criterion is closed by the local smoke alone.
