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

| Role                              | Who                  | Responsibilities                                                                                                                                                          |
| --------------------------------- | -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Architect / integrator / reviewer | Claude (Claude Code) | Designs interfaces, writes work packages, reviews and merges delegate work, keeps the roadmap and ADRs current, builds cross-cutting pieces itself                        |
| Product owner / courier           | Business owner       | Approves scope and decisions, hands work packages to developers, brings back their reports and code, provides external inputs (credentials, vendor documents, pilot data) |
| Delegate developer                | External developers  | Implement one work package at a time exactly as specified, deliver code + tests + report                                                                                  |

Every work package (WP) is self-contained: goal, scope, interfaces to respect, tests required,
acceptance criteria and the report format. Delegates never need the conversation history.
See [docs/delegation/README.md](docs/delegation/README.md).

## Milestones

| #   | Milestone                                      | Plan increment | Status                               | Exit criteria (summary)                                                                                                                                                                         |
| --- | ---------------------------------------------- | -------------- | ------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| M0  | Foundation                                     | I0             | DONE (2026-09-02)                    | Repo, corrected migrations 1-9, OpenAPI v1, Go skeleton, CI, schema tests green on PostgreSQL 18.4                                                                                              |
| M1  | Identity, tenants, organizations               | I1             | DONE (2026-09-03)                    | Login with KAPSORA accounts (ADR-022), tenant switch, permissions enforced, organization CRUD with VKN dedup, audit and idempotency live, three web shells, native local environment documented |
| M2  | People, plans, eligibility, entitlement ledger | I2             | ACTIVE                               | Encrypted identifiers + HMAC search, member import, program/plan/version/enrollment, eligibility API, ledger with reservations; 100 concurrent reserves without double spend                    |
| M3  | Catalog, providers, contracts, pricing, rules  | I3             | PLANNED                              | Deterministic contract/price selection, published versions immutable, CEL rule sets with test cases and maker-checker publish                                                                   |
| M4  | Requests, workflow, documents, notifications   | I4             | PLANNED                              | Explicit transitions only, work queues with SLA, quarantine-scan-secure document pipeline, PII-free notifications                                                                               |
| M5  | Health vertical                                | I5             | PLANNED                              | Outpatient claim invoice-ready end to end, inpatient preauthorization with medical review, clinical/financial visibility separation                                                             |
| M6  | Accommodation vertical                         | I6             | PLANNED                              | Inventory never negative under 500 concurrent holds, hold expiry releases entitlement, cancellation policy snapshots                                                                            |
| M7  | Claims, invoices, batches, settlement          | I7             | PLANNED                              | Line-level decisions audited, submitted batches immutable, settlement never exceeds approved total                                                                                              |
| M8  | Fiscal: GİB e-documents via İşNet Nettefatura  | I8             | DEFERRED to last (owner, 04.09.2026) | 95% auto-match on mock inbox, real inbox + application response on Nettefatura test environment, 8-day SLA work items                                                                           |
| M9  | Accounting integration                         | I9             | DEFERRED to last (owner, 04.09.2026) | Approved settlement appears in the ERP as purchase invoice + voucher + payment order; ERP payment closes settlement; reconciliation diff zero or explained                                      |
| M10 | Integrations, hardening, pilot                 | I10            | PLANNED                              | HR/policy import adapters, load test targets, DR drill, pentest findings closed, UAT signed                                                                                                     |
| M11 | MVP+1                                          | I11            | PLANNED                              | Outbound e-Fatura/e-Arşiv, assistance and care verticals, push notifications                                                                                                                    |

Sizes are relative (L = several weeks of one developer). Calendar dates are not promised;
milestones close when their exit criteria are verified by the integrator.

## M1 work packages (issued 2026-09-02)

| WP                                                                        | Title                                                                                                            | Depends on    | Parallel with      | Size | Owner                                  |
| ------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- | ------------- | ------------------ | ---- | -------------------------------------- |
| [WP-I1-01](docs/delegation/WP-I1-01-identity-oidc-bff-session.md)         | Identity: login, PostgreSQL sessions, CSRF, step-up                                                              | ports in repo | 02, 03, 04, 05, 06 | L    | Claude · **DONE**                      |
| [WP-I1-02](docs/delegation/WP-I1-02-authorization-tenant-context.md)      | Authorization: tenant context, permissions, /me, /tenants, switch-tenant, role templates, seed                   | ports in repo | 01, 03, 04, 05, 06 | L    | Claude · **DONE**                      |
| [WP-I1-03](docs/delegation/WP-I1-03-organizations.md)                     | Organizations: directory CRUD, VKN/TCKN validation, blind-index dedup, ETag, cursor paging                       | ports in repo | 01, 02, 04, 05, 06 | M    | Claude · **DONE**                      |
| [WP-I1-04](docs/delegation/WP-I1-04-platform-audit-outbox-idempotency.md) | Platform services: audit recorder, outbox dispatcher, idempotency middleware, rate limit, scheduler jobs, keygen | ports in repo | 01, 02, 03, 05, 06 | L    | Claude · **DONE**                      |
| [WP-I1-05](docs/delegation/WP-I1-05-frontend-foundation.md)               | Frontend foundation: pnpm workspace, three app shells, generated client, first screens with mocks                | OpenAPI only  | all                | L    | Claude · **DONE**                      |
| [WP-I1-06](docs/delegation/WP-I1-06-native-environment-ops.md)            | Native environment and ops: install/run scripts for MinIO, ClamAV, Mailpit; systemd units; runbooks              | none          | all                | M    | Claude · **DONE** (Ubuntu VM run open) |

Integration order once packages return: 04 → 01 → 02 → 03 → 05 (06 any time). The
integrator wires middlewares and routes in `cmd/api` and runs the full test suite before
closing M1.

## M2 work packages (issued 2026-09-03)

| WP                                                                | Title                                                                                            | Depends on           | Parallel with | Size | Owner             |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ | -------------------- | ------------- | ---- | ----------------- |
| [WP-I2-01](docs/delegation/WP-I2-01-persons.md)                   | Persons: registry, encrypted identifiers, blind-index search, relationships, sponsor memberships | M1                   | 02            | L    | Claude · **DONE** |
| [WP-I2-02](docs/delegation/WP-I2-02-programs-plans-enrollment.md) | Programs, plans, plan versions (maker-checker), entitlement definitions, enrollments             | M1, 01 (enrollments) | 01            | L    | Claude · **DONE** |
| [WP-I2-03](docs/delegation/WP-I2-03-entitlement-ledger.md)        | Entitlement accounts, ledger, reservations, adjustments, reconciliation                          | 02                   | 01            | L    | Claude · **DONE** |
| [WP-I2-04](docs/delegation/WP-I2-04-eligibility.md)               | Eligibility API with as-of resolution, explanations, evaluation snapshots                        | 01, 02, 03           | 05            | M    | Claude · **DONE** |
| [WP-I2-05](docs/delegation/WP-I2-05-member-import.md)             | Member import: staging, validation, matching, review, idempotent apply                           | 01, 02               | 04            | L    | Claude · **DONE** |
| [WP-I2-06](docs/delegation/WP-I2-06-frontend-people-plans.md)     | Backoffice screens: people, memberships, programs/plans, entitlements, eligibility, import       | contracts of 01-05   | all           | L    | Claude · **DONE** |

Integration order: 01 → 02 → 03 → 04 → 05 → 06 (06 starts on mocks as soon as each
contract lands). Migrations: 000014 (01, relationship versioning), 000015 (02), 000016 (03), 000017 (04), 000018 (05).

## M3 work packages (issued 2026-09-04)

| WP                                                                           | Title                                                                                       | Depends on         | Parallel with | Size | Owner             |
| ---------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- | ------------------ | ------------- | ---- | ----------------- |
| [WP-I3-01](docs/delegation/WP-I3-01-catalog-and-code-systems.md)             | Service catalog API, external code systems, service code mapping                            | M2                 | 02            | M    | Claude · **DONE** |
| [WP-I3-02](docs/delegation/WP-I3-02-provider-network.md)                     | Provider profiles, locations, capabilities, practitioners, provider search                  | 01                 | 01            | L    | Claude · **DONE** |
| [WP-I3-03](docs/delegation/WP-I3-03-contracts-and-prices.md)                 | Contracts, versions (maker-checker), price lists, packages, quotas, deterministic selection | 01, 02             | 04            | L    | Claude · **DONE** |
| [WP-I3-04](docs/delegation/WP-I3-04-rule-engine.md)                          | Rule sets, CEL rules, test cases, publish gate, immutable evaluations                       | M2, 01             | 03            | L    | Claude · **DONE** |
| [WP-I3-05](docs/delegation/WP-I3-05-pricing-quote.md)                        | Pricing quote composing eligibility, price selection, rules and balances                    | 01-04              | 06            | M    | Claude · **DONE** |
| [WP-I3-06](docs/delegation/WP-I3-06-frontend-catalog-providers-contracts.md) | Backoffice screens: catalog, providers, contracts, rules, quote                             | contracts of 01-05 | all           | L    | Claude            |

Integration order: 01 → 02 → 03 → 04 → 05 → 06 (06 starts on mocks as soon as each
contract lands). Migrations: 000019 (01), 000020 (02), 000021 (03), 000022 (04), 000023 (05).
ADR-023 fixes the rule expression language (CEL); ADR-022 was already taken by the identity decision.

## M4 work packages (issued 2026-09-04)

| WP                                                                                 | Title                                                                                 | Depends on                            | Parallel with | Size | Owner  |
| ---------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- | ------------------------------------- | ------------- | ---- | ------ |
| [WP-I4-01](docs/delegation/WP-I4-01-service-requests.md)                           | Service requests: versions, items, explicit transitions, the submit gate              | M2, M3                                | 04            | L    | **delivered** |
| [WP-I4-02](docs/delegation/WP-I4-02-authorization-and-fulfilment.md)               | Authorization reserving entitlement, fulfilment consuming it, vouchers                | 01, WP-I2-03                          | 03            | L    | Claude |
| [WP-I4-03](docs/delegation/WP-I4-03-workflow-and-worklist.md)                      | Work queues, work items, SLA snapshot and escalation, approval policy                 | 01                                    | 02            | L    | Claude |
| [WP-I4-04](docs/delegation/WP-I4-04-document-pipeline.md)                          | Documents: upload, quarantine, ClamAV scan, secure storage, legal hold                | M1 (native ClamAV and object storage) | 01            | L    | Claude |
| [WP-I4-05](docs/delegation/WP-I4-05-notifications.md)                              | Notification templates, messages, delivery, preferences                               | 01, WP-I1-04 outbox                   | 03            | M    | Claude |
| [WP-I4-06](docs/delegation/WP-I4-06-frontend-requests-worklist-provider-portal.md) | Screens: requests, worklist, documents, notifications — and the first provider portal | contracts of 01-05                    | all           | L    | Claude |

Integration order: 01 → 02 → 03 → 04 → 05 → 06 (06 starts on mocks as soon as each
contract lands). Migrations: 000025 (01), 000026 (02), 000027 (03), 000028 (04), 000029 (05).
The provider portal ships its first real screens here; `web/apps/provider` has been an
empty shell since M1.

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

| Needed by | Input                                                                           | Status                                                                                                                                                                                                             |
| --------- | ------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| M1        | External developers for WP-I1-01..06                                            | paused by the owner 2026-09-02; Claude implements in order 04 → 01 → 02 → 03 → 05; delegation can resume later with the same WP files                                                                              |
| M8        | İşNet Nettefatura web-service application, NDA, test account, API documentation | **not needed yet** — deferred to last by the owner on 04.09.2026: there is a lot to build first and no running product to integrate. Development continues against the mock adapter derived from the GİB standard. |
| M9        | Name of the ledger-keeping accounting program of the pilot customer             | **not needed yet** — deferred with M8. The Generic File Adapter carries the work until a program is named.                                                                                                         |
| M10       | Pilot customer, program and beneficiary group; HR/policy source formats         | open                                                                                                                                                                                                               |

## Status log

- 2026-09-04 · WP-I4-01 delivered: the service request lifecycle. Thirteen operations, every state change a command with its own permission, precondition and reason, and no writable status column anywhere. A submit evaluates eligibility and the rule set, records which evaluation decided it, and lands on PENDING_REVIEW, PENDING_DOCUMENT or ELIGIBILITY_FAILED; it never rests on SUBMITTED. A returned request is corrected in a new version and the decided one is frozen. The provider boundary is applied in SQL on every read and every lock, answering 404 rather than 403. Migration 000025; schema version 25.
- 2026-09-04 · Two things the delivery report did not mention, found by reading rather than trusting. The empty provider grant: `scopeOf` returns an empty non-nil slice for an ORGANIZATION grant naming no organization, and the whole boundary then rests on that slice reaching Postgres as an empty array rather than NULL — as NULL it would read as tenant-wide and the emptiest possible grant would see everything. A test now asserts it, and was proved alive by making `scopeOf` leave the slice nil and watching it fail. And the mock had drifted from the contract in four ways at once: quantities and amounts as JS numbers, requests resting at SUBMITTED, REJECTED rows with no reason, PENDING_DOCUMENT rows naming no documents — the last two are states migration 000025's own check constraints forbid. The mock is a test double of the Go server; a divergence is a bug that lets a screen pass its tests and fail in production.
- 2026-09-04 · Open, outside any package: six problem codes the server can emit have no Turkish message (`PERSON_NOT_FOUND`, `PLAN_NOT_FOUND`, `ENROLLMENT_NOT_FOUND`, `RATE_LIMITED`, `TENANT_CODE_EXISTS`, `SERVICE_NOT_READY`). Nothing looks broken today only because those handlers happen to write Turkish `title`s and the UI falls back to them, which makes an exceptional path load-bearing and means the wording cannot change without a server deploy. Nothing checks this: the i18n test covers the mapping mechanism, not the coverage. Needs a guard whose source of truth is the Go tree, not a grep.
- 2026-09-04 · M3 opened: six work packages WP-I3-01..06 written from plan v2.0 I3 and the v1.2 Phase 4 acceptance criteria; migration numbers 000019-000023 assigned. The milestone turns on two rules the baseline states plainly and this plan refuses to soften: one service date selects exactly one contract price or answers REVIEW_REQUIRED, never a coin flip; and a rule version reaches review only with a passing test case and is published only by a second person.
- 2026-09-04 · M4 opened: six work packages WP-I4-01..06 written from plan v2.0 I4 and the v1.2 Phase 5 acceptance criteria; migration numbers 000025-000029 assigned. Three rules from the baseline are written into the specs rather than left to the implementation: no endpoint anywhere writes a status, so every move is a command with its own precondition and reason; return and reject are different things, because collapsing them makes a correctable mistake read as a refusal; and a work item is judged by the SLA it was given, not the one its queue has today.
- 2026-09-04 · Owner: the Nettefatura web-service application and the accounting-program choice move to the end of the queue. Neither blocks anything now, and there is no running product to integrate with yet; M8 and M9 keep their place in the increment order but their external inputs are not chased until the work in front of them is done.
- 2026-09-04 · M3 closed. WP-I3-06 delivered the backoffice screens for the catalog and its code systems, the provider network, contracts with their price sheets, the rule sets with their publishing gate, and the price quote. 152 Vitest specs and 11 Playwright flows pass, Impeccable reports no anti-patterns, and CI is green on all six jobs. Three screens arrived navigating around the router because Link is typed against the registered route tree; they now use it, which also meant declaring routes one by one rather than mapping over a list, since a .map() erases the path literals and turns every link into an unchecked string.
- 2026-09-04 · M3 backend delivered (WP-I3-01..05): the service catalog with external code systems, the provider network with encrypted practitioner registrations, contracts with maker-checker publishing and deterministic price selection, the CEL rule engine with its publishing gate, and the pricing quote. Migrations 000019-000024; schema version 24.
- 2026-09-04 · Three CI guards were found to have never checked anything, each proved dead and then repaired: the Spectral rule requiring the tenant header compared against an unresolved $ref that Spectral resolves before a rule runs; the generated-code drift check ran oapi-codegen from the repository root while its output path points outside it, so git diff compared the committed file with itself; and .gitleaks.toml used the plural [[allowlists]] form, which gitleaks silently ignores when extending the default config. Each was verified by making it fail on purpose before and after the fix. The lesson is written down here because a guard that reports success over an empty set is worse than no guard: it is trusted.
- 2026-09-03 · WP-I2-06 delivered, closing M2: backoffice screens for members (list, create, detail tabs for identity, family, memberships, enrollments, entitlements with ledger, and eligibility), programs and plans, the plan version editor with maker-checker publishing, the entitlement adjustment approval queue, and member import from upload through review to apply. The identifier search and every publish, retire, approve, reject, upload and apply ask for the password again. Identity numbers stay in component state and are only ever shown masked; nothing goes into browser storage. Quantities stay decimal strings end to end. Two mock-versus-server divergences were corrected in the mock rather than worked around. 85 Vitest specs and 6 Playwright smoke flows pass, Impeccable reports no anti-patterns.
- 2026-09-03 · WP-I2-05 delivered: member import with CSV_V1 parsing (delimiter and byte-order-mark tolerant, line-accurate errors), staging that never holds a plaintext identifier, validation and blind-index matching, a review queue for conflicts and invalid rows, and idempotent apply in transactional chunks (re-applying changes nothing; a new source version updates instead of duplicating). Migration 000018; schema version 18.
- 2026-09-03 · WP-I2-04 delivered: eligibility check with as-of resolution (person, membership, enrollment, published plan version, balances including family-shared accounts), eleven explanation codes, per-item results, immutable evaluation snapshots without identifiers, idempotent replay and provider-scope enforcement. Migration 000017; schema version 17.
- 2026-09-03 · M2 in progress: WP-I2-01 persons (encrypted identifiers, blind-index search with step-up and access audit, relationships, sponsor memberships), WP-I2-02 programs/plans/plan versions with maker-checker publish and enrollments, WP-I2-03 entitlement accounts, append-only ledger, reservations, maker-checker adjustments, expiry and reconciliation jobs. 100 concurrent reserves: 50 succeed, 50 refused, no double spend; overdraft and same-key variants verified. Migrations 000014-000016; schema version 16.
- 2026-09-03 · M1 closed (WP-I1-01..06 delivered; issue #7 tracks the Ubuntu VM run). M2 opened: six work packages WP-I2-01..06 written from plan v2.0 I2 and the v1.2 Phase 3 acceptance criteria; migration numbers 000014-000017 assigned.

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
