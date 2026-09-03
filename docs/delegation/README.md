# KAPSORA Delegate Handbook

You have been given one **work package (WP)** from `docs/delegation/`. This handbook is
everything you need to deliver it. Read it once fully; the WP file tells you what to build,
this file tells you how we build. When the two disagree, ask through the owner before coding.

## 1. Roles and flow

1. The integrator (Claude) writes the WP with fixed interfaces, migration numbers and
   acceptance criteria.
2. You implement it on a branch named `wp/<WP-ID>` (for example `wp/I1-03`), with tests.
3. You deliver code plus a report written with `REPORT_TEMPLATE.md`.
4. The integrator reviews, runs the full suite, and either merges or returns change requests
   through the owner. Never merge your own work.

Questions go to the owner in writing. If you must proceed before an answer, choose the
safest interpretation, implement it, and list the assumption in your report under
"Assumptions and questions". Silent assumptions are the main cause of rejected work.

## 2. Environment

No Docker anywhere (ADR-021). Everything runs as native processes.

| Tool | Version | Notes |
|---|---|---|
| Go | 1.27.x | `go.mod` pins the language version; the toolchain downloads automatically |
| PostgreSQL | 18.x | Local server; you need a superuser or a role with CREATEDB + CREATEROLE for schema tests |
| Node.js + pnpm | 24.x / 10.x | Only for frontend packages (WP-I1-05) |

| Git | 2.4x+ | LF line endings are enforced by `.gitattributes` |

Setup:

```sh
git clone https://github.com/celikbros/kapsora.git && cd kapsora   # private; the owner grants access
cp .env.example .env          # fill CHANGE_ME values with your local PostgreSQL credentials
make tools                    # sqlc, oapi-codegen, oasdiff, golangci-lint, govulncheck
make db-init                  # creates role kapsora_app and database kapsora
make migrate-up               # schema to the latest version
make test-unit && make test-db
```

Windows without GNU make: `.\scripts\dev.ps1 <target>` provides the same targets.
Schema tests create and drop throw-away databases named `kapsora_test_*`; they never touch
your `kapsora` database.

## 3. Non-negotiable rules

These come from the normative specification (v1.2) and the ADRs. Violations are rejected
without discussion.

**Tenant safety**
- Every tenant-scoped query runs inside `db.WithTenantTx` (or `db.WithActorTx` in the
  pre-tenant phase). Never open a raw transaction for tenant data.
- New tenant tables: `tenant_id`, `UNIQUE (tenant_id, id)`, composite foreign keys
  `(tenant_id, x_id)`, `SELECT platform.enable_tenant_rls(...)`, and a negative RLS test.
- Business references (`SR-...`) are display values, never keys or security boundaries.

**Data and SQL**
- No ORM. Queries live in `db/queries/<module>.sql` and are generated with `sqlc`
  (`make sqlc`). Generated code is committed.
- Migrations are forward-only, numbered, `*.up.sql` only, and use the migration number the
  WP assigns to you. Never modify a migration that is already on `main`.
- Money is `numeric(20,6)` with an ISO-4217 code; Go side uses `shopspring/decimal`. No floats.
- `updated_at` and `row_version` are set by database triggers. Your UPDATE statements must
  not assign them; compare `row_version` in `WHERE` for optimistic concurrency.
- Append-only tables (ledger, status events, audit) are never updated or deleted.

**Application structure**
- Module layout: `internal/<module>/{domain,application,infrastructure,transport}`.
  `domain` imports nothing from `net/http`, `pgx` or `chi` (depguard enforces it).
- Cross-module calls go through the other module's `application` port, never its
  `infrastructure` package.
- HTTP handlers contain no business logic: parse, call application service, map errors.
- Status changes happen only through explicit commands (`/submit`, `/cancel`, ...). No
  generic `PATCH status`.
- Route registration pattern: your module exposes
  `transport/http.NewHandler(deps).Routes(r chi.Router)`. Middlewares (session, tenant,
  idempotency, rate limit) are applied by the integrator in `cmd/api`; do not wire them
  yourself unless your WP says so.

**API contract**
- `api/openapi/kapsora-v1.yaml` is the contract. Change the spec first (only the operations
  your WP owns), run `make openapi-generate`, then implement against the generated
  `kapsorav1` strict-server interfaces. Spectral must report zero errors.
- Errors are `application/problem+json` with a stable, machine-readable `code`
  (`httpx.WriteProblem`). Never return stack traces, SQL text or another tenant's existence
  (unknown or foreign resources are `404`).
- Commands take `Idempotency-Key`; mutable resources return `ETag` and require `If-Match`.

**Security and privacy**
- Never log, return or store in audit detail: TCKN, passport numbers, full names combined
  with identifiers, diagnoses, document contents, tokens, cookies, secrets, request bodies.
- Sensitive identifiers are stored through `crypto.FieldCipher` + `crypto.BlindIndexer`.
  Plaintext never reaches SQL, URLs, logs or metrics.
- Secrets come from the environment only. No credentials, keys or `.env` files in git.
- Permission checks use `identity.Require(ctx, "<permission.code>")` with codes from
  migration 000008. Hiding a button in the UI is not authorization.
- Health data permissions (`health.*`) are separate from everything else; do not reuse
  generic `member.read` for clinical fields.

**Quality gates**
- `gofmt`, `go vet`, `golangci-lint run ./...` clean; `make ci` passes locally.
- Tests: unit tests for domain logic; PostgreSQL tests through `internal/platform/dbtest`
  for every repository; a negative authorization/RLS test for every new endpoint or table;
  `httptest` tests for handlers. Coverage target: 80% in domain packages.
- Conventional Commits (`feat(identity): ...`, `fix(db): ...`, `test(org): ...`).
- Code, comments, identifiers and commit messages in English. User-facing strings
  (problem titles, UI text) in Turkish with an English resource skeleton where i18n exists.
- New third-party dependencies require justification in the report; prefer the standard
  library. Dependencies listed as "allowed" in a WP need no justification.

## 4. Shared building blocks already in the repository

| Package | What you get |
|---|---|
| `internal/platform/config` | Typed `KAPSORA_*` environment loading; add your keys to `Config` with defaults and validation |
| `internal/platform/logging` | JSON `slog` logger; log only ids, codes, counts, durations |
| `internal/platform/db` | `NewPool`, `WithTenantTx`, `WithActorTx`, `BindTenant` |
| `internal/platform/dbmigrate` | Embedded migration runner (`migrate up/version`) |
| `internal/platform/dbtest` | Throw-away PostgreSQL databases and helpers for integration tests |
| `internal/platform/httpx` | `WriteProblem`, `Problem`, `RequestID` middleware, `Recoverer`, `RequestLogger` |
| `internal/platform/health` | Readiness checker; register your dependency check if you add one |
| `internal/platform/crypto` + `crypto/localkey` | `FieldCipher`, `BlindIndexer` and the local implementation |
| `internal/platform/sqlcgen` | Generated platform queries (tenants, reference numbers) |
| `internal/identity` | `Principal`, `Session`, `SessionStore`, `RequestContext`, `FromContext`, `Require`, `RequireStepUp`, `WithSession`/`SessionFromContext` |
| `internal/audit` | `Recorder` port, `Event`, `AccessEvent`, `NopRecorder` |
| `api/generated/kapsorav1` | Generated types and strict-server interfaces from the OpenAPI contract |

## 5. Definition of Done (per WP)

- [ ] Every acceptance criterion in the WP is demonstrably met (tests or reproducible steps).
- [ ] All tests green: `make test-unit`, `make test-db`, plus your WP-specific tests.
- [ ] `make ci` passes (format, vet, lint, generated code up to date).
- [ ] No rule in section 3 is violated; deviations are listed and justified in the report.
- [ ] OpenAPI, migrations, sqlc queries and docs updated where your WP touches them.
- [ ] Report written with `REPORT_TEMPLATE.md`, including exact commands to verify.

## 6. Submitting work

Preferred: push branch `wp/<WP-ID>` to `https://github.com/celikbros/kapsora` and open a
pull request titled `<WP-ID>: <short title>`; the PR template asks for the report. Each WP
also has a GitHub issue labelled `work-package`; reference it with `closes #<n>`.

Alternative (no repository access): from your branch run
`git format-patch main --stdout > <WP-ID>.patch`, put the patch and `REPORT.md` in a zip
named `<WP-ID>-<yyyymmdd>.zip`, and give it to the owner. Do not send zips of the whole
working tree, binaries, `node_modules` or `.env` files.

Keep the branch rebased on the latest `main` the owner gives you. Small, well-named commits
make review faster; one giant commit slows it down.

## 7. What not to do

- Do not work outside your WP's packages and files. If you believe another package must
  change, describe the change in the report instead of making it.
- Do not add or edit migrations except the number(s) assigned to you.
- Do not change OpenAPI operations that belong to another WP.
- Do not introduce Docker, containers, Valkey/Redis, an ORM, Kafka, or a frontend state
  library other than the ones listed in the frontend WP.
- Do not use real personal data, real tax numbers or production-like documents in tests
  or fixtures. Generate synthetic values; the WPs explain how.
