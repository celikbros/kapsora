# WP-I7-05 · The provider statement and reconciliation, operational dashboards, and exports that carry a watermark and an audit trail

| Field                      | Value                                                                                                                                                                                                                                       |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M7 (plan increment I7)                                                                                                                                                                                                                      |
| Size                       | M                                                                                                                                                                                                                                           |
| Depends on                 | WP-I7-02, WP-I7-03, WP-I7-04 (the figures), WP-I4-04 (the document store for export files), WP-I4-03 (work queues, for the dashboard counts), the scheduler                                                                                 |
| Runs in parallel with      | WP-I7-03, WP-I7-04 (contracts first; the figures land as they land)                                                                                                                                                                         |
| Migration numbers assigned | `000047_billing_reconciliation_and_exports.up.sql`                                                                                                                                                                                          |
| OpenAPI operations owned   | `getProviderStatement`, `listReconciliationRuns`, `getReconciliationRun`, `getOperationsDashboard`, `createExport`, `getExport`, `downloadExport`, `listExports`                                                                            |
| Read first                 | v1.2 9.14 ("cari/mutabakat raporu"), 9.18 (reporting), 16.8 (`billing.reconciliation_run`), 16.9 (`audit.access_event`), Faz 8 ("async export permission, watermark, TTL ve download audit"); plan v2.0 §2.7 (the daily reconciliation compares KAPSORA's settlement totals with the ERP's ledger in M9 — here the run compares KAPSORA with itself and with the payment records a person entered, and leaves the ERP side as a nullable input), WP-I4-04 (documents: the export file is one, with its own class and TTL) |

## 1. Goal

The numbers both sides argue about, on one page, and a way to take them out of the
system that is permissioned, watermarked, time-limited and audited. Two rules carry the
package: **a reconciliation run is an immutable record of what was compared and what
differed**, and **an export is a document like any other — produced by the worker, held
with a TTL, downloaded only with the permission and only as an audited access.**

## 2. Scope

### 2.1 Schema (migration 000047)

`billing.reconciliation_run`: id, tenant_id, `scope` (`PROVIDER`, `TENANT`),
`provider_organization_id` NULL, `period_from`, `period_to`, `run_no` (unique with scope
and period), `currency_code`, `invoiced_total`, `approved_total`, `cut_total`,
`returned_total`, `rejected_total`, `settled_total`, `paid_total`, `open_total`, `erp_total`
NULL (M9), `difference` (settled − paid, or settled − erp when the ERP figure exists),
`difference_count`, `differences` jsonb (each: settlement reference, expected, actual,
kind), `status` (`BALANCED`, `DIFFERENCES`, `FAILED`), `ran_at`; **append-only through
`platform.make_append_only`**. The run also marks settlements whose paid amount equals
their payable amount `RECONCILED`.

`report.export` (new `report` schema): id, tenant_id, `kind` (`PROVIDER_STATEMENT`,
`BATCH`, `SETTLEMENTS`, `CLAIMS`, `RECONCILIATION`), `parameters` jsonb (the filters as
given, no identifiers), `format` (`CSV`, `XLSX`), `status` (`QUEUED`, `RUNNING`, `READY`,
`FAILED`, `EXPIRED`), `document_id` NULL (WP-I4-04, class `EXPORT`), `row_count`,
`requested_by`, `requested_at`, `expires_at` (the tenant's `report.export_ttl_hours`,
default 24), `watermark` (the requester's display name, the tenant, the moment, and the
export id — the string that is stamped on every page/first row of the file), `download_count`.

Permissions (BOTH places, two-halves test): `report.export` (NORMAL; PROGRAM_MANAGER,
finance) and `report.export.sensitive` (SENSITIVE; the CLAIMS kind, which carries line
descriptions) — `report.read` already exists.

### 2.2 The provider statement

`getProviderStatement` (`report.read` on the payer side, the provider's own scope on
theirs): for a provider, a period and a currency — the invoices with their batch,
decision, approved and paid amounts; the settlements with due dates and payments; the
totals of the columns above; the open balance. Every figure is a server sum of exact
decimals, computed in one query, never assembled on the client.

### 2.3 Reconciliation

A daily scheduler job (`billing.reconcile`, 02:00 tenant time) runs one `TENANT` run for
the previous day per currency and one `PROVIDER` run per provider with any movement;
`listReconciliationRuns`/`getReconciliationRun` read them; a `DIFFERENCES` run raises a
work item in the finance queue (WP-I4-03, `RECONCILIATION_DIFFERENCE`) once per run.

### 2.4 The dashboard

`getOperationsDashboard` (`report.read`): counts and sums the operator reads every
morning — claims by status and aging bucket, batches awaiting review with the oldest SLA,
settlements due this week and overdue, reimbursements awaiting decision, work items past
SLA — each figure one server query, each with the link filter that reproduces it. No
client arithmetic; no percentages the server did not compute.

### 2.5 Exports

`createExport` (`report.export`; `report.export.sensitive` for CLAIMS): queues the job;
the worker renders the file (CSV with UTF-8 BOM, or XLSX through the existing
spreadsheet dependency if one is present, else CSV only and the contract says so), stamps
the watermark on every row (a leading column) and on the sheet header, stores it through
WP-I4-04 as a document of class `EXPORT` with the TTL, and marks `READY`.
`downloadExport` streams it through the document download path, refuses after
`expires_at` (`EXPORT_EXPIRED`), counts the download, and **writes an
`audit.access_event` with the export id, the kind and the requester** — the download is
the access that is audited. A nightly job expires stale exports and deletes their files.

### 2.6 The mock

`report-handlers.ts`: the statement, one balanced and one differing run, the dashboard,
and an export that goes READY after a tick.

## 3. Tests required

- The statement's totals equal the sums of its rows to the kuruş, in one query; a
  provider reads only its own.
- A run is append-only (update/delete refused with the application bypassed); a
  settlement fully paid is marked `RECONCILED` by the run and never by a command; a
  difference produces exactly one work item.
- The dashboard's counts equal the lists they link to, on a seeded world.
- Export: created without the permission is 403; CLAIMS without the sensitive grant is
  403; the file carries the watermark on every row; download after the TTL is refused;
  every download is an access event; the parameters column holds no identifier.
- The nightly expiry removes the file and marks `EXPIRED`.

## 4. Acceptance criteria

- [ ] Async export works with permission, watermark, TTL and download audit (Faz 8).
- [ ] The provider statement and the daily reconciliation answer the open balance and its
      differences as immutable records.
- [ ] OpenAPI (additive), generated code, Spectral and `oasdiff` clean; Turkish for every
      problem code; permissions in both places; schema version 47.
