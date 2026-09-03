# KAPSORA Roadmap

> **Türkçe özet.** Bu yol haritası master planın (docs/plan) artımlarını kilometre taşlarına
> ve dış geliştiricilere verilebilecek iş paketlerine (WP) böler. Claude mimar, entegratör ve
> reviewer'dır; iş sahibi kurye ve karar vericidir; delegeler WP'leri uygular ve
> `docs/delegation/REPORT_TEMPLATE.md` formatında rapor verir. Durum tablosu her artım
> sonunda güncellenir. Konteyner yoktur (ADR-021).

Status legend: `DONE` verified and merged · `ACTIVE` work packages issued · `READY` can be
issued next · `PLANNED` designed, not yet broken into work packages · `BLOCKED` waiting on an
external input.

Authoritative sources: [docs/plan/KAPSORA_Master_Plan_v2.0.md](docs/plan/KAPSORA_Master_Plan_v2.0.md)
(Turkish, normative), [docs/adr](docs/adr/README.md), the frozen v1.2 specification under
[docs/baseline-v1.2](docs/baseline-v1.2/). When they disagree, the master plan and ADRs win.

## Working model

| Role | Who | Responsibilities |
|---|---|---|
| Architect / integrator / reviewer | Claude (Claude Code) | Designs interfaces, writes work packages, reviews and merges delegate work, keeps the roadmap and ADRs current, builds cross-cutting pieces itself |
| Product owner / courier | Business owner | Approves scope and decisions, hands work packages to developers, brings back their reports and code, provides external inputs (credentials, vendor documents, pilot data) |
| Delegate developer | External developers | Implement one work package at a time exactly as specified, deliver code + tests + report |

Every work package (WP) is self-contained: goal, scope, interfaces to respect, tests required,
acceptance criteria and the report format. Delegates never need the conversation history.
See [docs/delegation/README.md](docs/delegation/README.md).

## Milestones

| # | Milestone | Plan increment | Status | Exit criteria (summary) |
|---|---|---|---|---|
| M0 | Foundation | I0 | DONE (2026-09-02) | Repo, corrected migrations 1-9, OpenAPI v1, Go skeleton, CI, schema tests green on PostgreSQL 18.4 |
| M1 | Identity, tenants, organizations | I1 | ACTIVE | Login with KAPSORA accounts (ADR-022), tenant switch, permissions enforced, organization CRUD with VKN dedup, audit and idempotency live, three web shells, native local environment documented |
| M2 | People, plans, eligibility, entitlement ledger | I2 | PLANNED | Encrypted identifiers + HMAC search, member import, program/plan/version/enrollment, eligibility API, ledger with reservations; 100 concurrent reserves without double spend |
| M3 | Catalog, providers, contracts, pricing, rules | I3 | PLANNED | Deterministic contract/price selection, published versions immutable, CEL rule sets with test cases and maker-checker publish |
| M4 | Requests, workflow, documents, notifications | I4 | PLANNED | Explicit transitions only, work queues with SLA, quarantine-scan-secure document pipeline, PII-free notifications |
| M5 | Health vertical | I5 | PLANNED | Outpatient claim invoice-ready end to end, inpatient preauthorization with medical review, clinical/financial visibility separation |
| M6 | Accommodation vertical | I6 | PLANNED | Inventory never negative under 500 concurrent holds, hold expiry releases entitlement, cancellation policy snapshots |
| M7 | Claims, invoices, batches, settlement | I7 | PLANNED | Line-level decisions audited, submitted batches immutable, settlement never exceeds approved total |
| M8 | Fiscal: GİB e-documents via İşNet Nettefatura | I8 | BLOCKED (vendor access) | 95% auto-match on mock inbox, real inbox + application response on Nettefatura test environment, 8-day SLA work items |
| M9 | Accounting integration | I9 | BLOCKED (target ERP) | Approved settlement appears in the ERP as purchase invoice + voucher + payment order; ERP payment closes settlement; reconciliation diff zero or explained |
| M10 | Integrations, hardening, pilot | I10 | PLANNED | HR/policy import adapters, load test targets, DR drill, pentest findings closed, UAT signed |
| M11 | MVP+1 | I11 | PLANNED | Outbound e-Fatura/e-Arşiv, assistance and care verticals, push notifications |

Sizes are relative (L = several weeks of one developer). Calendar dates are not promised;
milestones close when their exit criteria are verified by the integrator.

## M1 work packages (issued 2026-09-02)

| WP | Title | Depends on | Parallel with | Size | Owner |
|---|---|---|---|---|---|
| [WP-I1-01](docs/delegation/WP-I1-01-identity-oidc-bff-session.md) | Identity: login, PostgreSQL sessions, CSRF, step-up | ports in repo | 02, 03, 04, 05, 06 | L | Claude · **DONE** |
| [WP-I1-02](docs/delegation/WP-I1-02-authorization-tenant-context.md) | Authorization: tenant context, permissions, /me, /tenants, switch-tenant, role templates, seed | ports in repo | 01, 03, 04, 05, 06 | L | Claude · **DONE** |
| [WP-I1-03](docs/delegation/WP-I1-03-organizations.md) | Organizations: directory CRUD, VKN/TCKN validation, blind-index dedup, ETag, cursor paging | ports in repo | 01, 02, 04, 05, 06 | M | Claude · **DONE** |
| [WP-I1-04](docs/delegation/WP-I1-04-platform-audit-outbox-idempotency.md) | Platform services: audit recorder, outbox dispatcher, idempotency middleware, rate limit, scheduler jobs, keygen | ports in repo | 01, 02, 03, 05, 06 | L | Claude · **DONE** |
| [WP-I1-05](docs/delegation/WP-I1-05-frontend-foundation.md) | Frontend foundation: pnpm workspace, three app shells, generated client, first screens with mocks | OpenAPI only | all | L | Claude · **DONE** |
| [WP-I1-06](docs/delegation/WP-I1-06-native-environment-ops.md) | Native environment and ops: install/run scripts for MinIO, ClamAV, Mailpit; systemd units; runbooks | none | all | M | Claude · **DONE** (Ubuntu VM run open) |

Integration order once packages return: 04 → 01 → 02 → 03 → 05 (06 any time). The
integrator wires middlewares and routes in `cmd/api` and runs the full test suite before
closing M1.

## Cross-cutting tracks

- **Security and privacy:** every WP carries the non-negotiable rules from the handbook
  (RLS through `db.WithTenantTx`, no PII in logs, encrypted identifiers, explicit
  transitions, idempotent commands). Threat review per milestone; pentest before pilot (M10).
- **Contracts:** OpenAPI changes are made before code, regenerated with oapi-codegen, linted
  with Spectral and checked for breaking changes in CI.
- **Data:** migrations are numbered by the integrator inside each WP; never edit merged ones.
- **Operations (container-free, ADR-021):** native processes in development, systemd on
  Linux in production, static Go binaries as release artefacts.
- **Documentation:** ADR for every deviation; runbooks grow with each milestone.

## External inputs the owner provides

| Needed by | Input | Status |
|---|---|---|
| M1 | External developers for WP-I1-01..06 | paused by the owner 2026-09-02; Claude implements in order 04 → 01 → 02 → 03 → 05; delegation can resume later with the same WP files |
| M8 | İşNet Nettefatura web-service application, NDA, test account, API documentation | open |
| M9 | Name of the ledger-keeping accounting program of the pilot customer | open |
| M10 | Pilot customer, program and beneficiary group; HR/policy source formats | open |

## Status log

- 2026-09-02 · M0 closed: migrations 1-9, 16 schema tests, health endpoints verified on local PostgreSQL 18.4.
- 2026-09-02 · Docker/Kubernetes removed (ADR-021); Valkey deferred, PostgreSQL-backed sessions.
- 2026-09-02 · M1 work packages WP-I1-01..06 issued; shared ports (`identity`, `audit`, `crypto`, `dbtest`) and migration 000009 merged.
- 2026-09-02 · Private repository `github.com/celikbros/kapsora` created; issues #1-#6 track the M1 work packages; CI green on `main` (build/lint/unit, PostgreSQL 18 schema tests, OpenAPI lint, secrets + dependency scan, static binaries). M0 exit criteria fully met.
- 2026-09-02 · Owner paused external developers; WP files remain the specifications and Claude implements them in-house. Migration numbers renumbered: WP-I1-04 → 000010, outbox dedupe fix → 000011, WP-I1-01 → 000012.
- 2026-09-03 · WP-I1-06 delivered: `scripts/native/{install,up,down,status}.{ps1,sh}` with pinned versions and SHA-256 in `versions.json` (MinIO, mc, Mailpit, ClamAV on Windows; distribution packages on Linux), hardened systemd units for api/worker/scheduler/migrate (+ MinIO), nginx and Caddy configurations, `deploy/install.sh`, runbooks (local environment, local accounts, single-server deployment, backup/restore). Windows: install 11 s from cache, first `up` 227 s (signature download), later `up` 16 s, all services healthy, `down` clean; shellcheck and PSScriptAnalyzer clean. Ubuntu VM run and `systemd-analyze verify` on a real host remain to be done at first deployment. **M1 complete.**
- 2026-09-03 · WP-I1-05 delivered: pnpm workspace with `api-client` (types generated from the contract, openapi-fetch wrapper adding request id, CSRF, tenant and idempotency headers, problem+json parsing, MSW mocks for every operation), `auth` (in-memory session store, route guards, tenant colour), `i18n` (Turkish complete, English skeleton, tenant-zone dates, ISO-code money), `ui` (Radix + Tailwind v4 design system) and three apps: backoffice (login, tenant picker, shell with the full v1.2 navigation, profile, organization list/detail/create/edit with ETag conflict dialog), provider and member shells. 45 unit tests, 3 Playwright smoke tests on the mock API, same screens verified against the Go API through the Vite proxy. Storybook deferred.
- 2026-09-03 · WP-I1-03 delivered: organization directory with global dedup by tax-number blind index (one legal entity, one relationship per tenant), VKN/TCKN checksum validation, masked identifiers, keyset cursor pagination (signed cursors), merge-patch update with ETag/If-Match, shared-name protection (migration 000013), audited create/update; first tenant-scoped module wired behind RequireTenantContext + idempotency.
- 2026-09-03 · WP-I1-02 delivered: request context from session + validated X-Tenant-ID + live membership, permission union over valid grants with scopes, `/me`, `/tenants`, `switch-tenant`, 16 system role templates, tenant provisioning with baseline catalogs, audited denials, `seed demo` (DEMO_A/DEMO_B, five demo users, idempotent). No migration needed. Verified end to end.
- 2026-09-03 · WP-I1-01 delivered, with a scope change the owner made: KAPSORA authenticates its own users, no Keycloak and no JDK ([ADR-022](docs/adr/ADR-022.md) supersedes ADR-005). Argon2id credentials, opaque session cookie whose digest is what the database stores, derived CSRF token, per-account lockout plus per-address rate limit, step-up and password change. Verified end to end against the running API. Migration 000012; schema version 12.
- 2026-09-02 · WP-I1-04 delivered in-house: audit recorder, outbox dispatcher (exactly-once under two concurrent dispatchers, retries, dead-letter, stale recovery), idempotency middleware, PostgreSQL rate limiter, scheduler job runner with four standard jobs, keygen. Found and fixed a baseline schema defect: the outbox dedupe constraint blocked every second event of a type (migration 000011).
