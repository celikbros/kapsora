# KAPSORA — Project Handover

[Documentation index](README.md) · [Roadmap](plan/ROADMAP.md)

Paths in code spans and command examples are relative to the repository root.

Originally written 2026-09-13 at the previous team handover; updated with the approved
readiness corrections and M10 work-package preparation. The original baseline CI was green
on `7f69e26`; use the current branch and CI for the latest delivery status. This file is the fastest path from zero to
working on this codebase as if you had built it. Read it once, fully, before touching code.

## 1. What this is

KAPSORA is an entitlement/benefit orchestration platform. A payer (a bank, insurer, or
corporate sponsor) defines what its members are entitled to — health sessions, lodging
nights, allowances. Providers (hospitals, clinics, hotels) deliver those services. The system
checks eligibility, records requests and claims, settles with providers, and (eventually)
issues fiscal documents and posts to an accounting ledger. It replaces spreadsheets, e-mail
approval chains, and per-provider portals with one auditable record, with tenant isolation
enforced in the database (Postgres RLS), not just in application code.

Three people use it, each in their own app, behind one API:

- **Backoffice** — the payer's own team: programs, plans, organizations, members, review
  queues, claims, settlement, reconciliation, reports.
- **Provider portal** — clinic/hospital/hotel staff: eligibility check, service request,
  claim, invoice, statement. Built for fast entry on a shared desk computer.
- **Member PWA** — the entitled person, on a phone: remaining entitlements, search and book,
  apply, upload a document, ask for reimbursement.

Read [PRODUCT.md](../PRODUCT.md) for the full user/purpose/brand brief and [DESIGN.md](../DESIGN.md) for every settled UI
pattern (colors, components, copy rules) — both are living documents, not historical notes;
keep them current as you build.

## 2. Where things stand

**M1 through M7 are DONE.** Schema is at migration `000048` (`db/migrations/`). Every
milestone's exit criteria were verified by the integrator before closing (see `docs/plan/ROADMAP.md`
§ Status log for the full narrative, milestone by milestone — it is long, but it is the real
history of every non-obvious decision, and reading the last 10–15 entries will save you from
relearning lessons the hard way). On top of the milestone plan, one cross-cutting increment
shipped 2026-09-12: **WP-X1-01, in-product help** — a `HelpHint` mark beside unfamiliar terms
and figures, and a `HelpDrawer` ("?" in every app header) explaining what each page is for.

**M8 (fiscal: GİB e-Belge via İşNet Nettefatura) and M9 (accounting integration) are
DEFERRED TO LAST — by explicit owner decision (04.09.2026), not blocked.** The reasoning on
record: there was a lot of product to build first, and no running system yet to integrate
against. Development continues against a mock fiscal adapter (derived from the GİB standard)
and a Generic File Adapter for accounting, both behind nullable ports, so M8/M9 slot in later
without rework. **Do not chase the Nettefatura NDA or an accounting-program name on your own
initiative — that decision belongs to the business owner.** See ADR-017, ADR-018, ADR-019 for
the integration model those two milestones will implement.

The owner approved closing the small readiness gaps and preparing M10 next. M10 now has
five work packages under `docs/delegation/WP-I10-*`. I10-02 implementation has started: the
k6 harness and four real-API smoke workflows pass; full load and pilot acceptance remain open.
See `docs/runbooks/load-testing.md`. Import testing awaits the role-owner decision because
no real role template grants `import.execute`; the harness does not change grants. M11 is PLANNED. M8/M9 remain deferred.

I10-03 recovery preparation is documented in `docs/runbooks/backup-restore.md`: RPO <=5
minutes, RTO <=2 hours, isolated target and database/document/key reconciliation. No restore
drill has run. Deployment packaging and installer findings are recorded at the end of
`docs/runbooks/deploy-single-server.md`; a named Ubuntu target is still required.

Pilot customer, program, beneficiary group and HR/policy source formats are still external
inputs. Technical preparation can proceed without them; customer-specific integration and
signed acceptance cannot.

### Smaller open items worth knowing about

- The mock world's `SPONSOR_HR_PERMISSIONS` now matches the real role, including
  `accommodation.property.read`; the mock test compares it with `roles.go`. Keep both
  permission lists aligned whenever a role changes (see §6).
- No theme (light/dark) toggle exists in the provider or member app headers — only the
  backoffice has one. The apps already follow the OS's `prefers-color-scheme` by default
  (`web/packages/config/theme.css`), so this is a nice-to-have, not a defect.
- Three in-product-help terms had no existing on-screen word to reuse, so they were coined:
  "Parola doğrulaması" (step-up), "Dört göz kuralı" (maker-checker), "Oda tutma" (a room
  hold). Confirm the wording with the owner if it matters to them —
  `web/packages/i18n/src/locales/help/tr/terms.json`.
- A handful of older, smaller wire/UI gaps are listed at the end of their own `docs/plan/ROADMAP.md`
  status-log entries (grep the file for `Open:`) — none block anything, all are candidates
  for a quiet afternoon.

## 3. Get it running

Requirements: Go 1.27+, a local PostgreSQL 18, Node 24 + pnpm 10. No Docker, ever (§6).

```sh
cp .env.example .env              # fill CHANGE_ME with your local PostgreSQL credentials
make tools                        # sqlc, oapi-codegen, oasdiff, golangci-lint, govulncheck
make native-install && make native-up   # MinIO, ClamAV, Mailpit as native processes
make db-init                      # role kapsora_app + database kapsora
make migrate-up                   # schema to 000048
make test-unit && make test-db    # should both be green before you write anything
```

Windows without GNU make: `.\scripts\dev.ps1 <target>` mirrors every Makefile target
(`.\scripts\dev.ps1 migrate-up`, etc.).

**Running the whole system, one address** (the way this team worked day to day):
`.\scripts\dev.ps1 up` (or `make up` outside Windows) starts the API, worker, scheduler and
all three web apps behind one door on port 5181, so the single sign-in's shared cookie works
exactly as it will in production. `... down` stops everything it started. Seed demo data
first with `.\scripts\dev.ps1 seed-demo` (needs `native-up` running; password
`demo parola 2026 kapsora` unless `KAPSORA_SEED_DEMO_PASSWORD` is set).

**A backend-free demo** (in-browser mock data, nothing to install): double-click
`scripts\demo\KAPSORA-Demo-Baslat.cmd`. It opens the three apps on ports 5181–5183 against
MSW-mocked data in the browser and a guide page explaining six scenarios to try.
`KAPSORA-Demo-Durdur.cmd` stops it. If a demo tab ever shows a raw `"Not Found"` error where a
proper Turkish message should be, the browser has a stale MSW service-worker registration
for that origin — stop, restart, then hard-reload (Ctrl+Shift+R); DevTools → Application →
Service Workers → Unregister is the fallback.

**One rule the previous team held firm on and you should too:** the person running the
project runs its servers themselves, from their own terminals, in their own environment.
Whoever is coding gives exact commands rather than starting long-running processes on someone
else's behalf.

## 4. Architecture, in one page

- **Go modular monolith** — `cmd/{api,worker,scheduler,migrate,seed,keygen}`. Every domain
  module lives at `internal/<module>/{domain,application,infrastructure,transport}`; `domain`
  imports nothing from `net/http`, `pgx`, or `chi` (enforced by `depguard` in CI lint).
  Cross-module calls go through the other module's `application` port, never its
  `infrastructure` package. HTTP handlers parse, call the application service, map errors —
  no business logic in transport.
- **PostgreSQL 18**, UUIDv7 keys, Row-Level Security on every tenant table, composite
  `(tenant_id, id)` foreign keys. Tenant-scoped queries always run inside
  `db.WithTenantTx`/`db.WithActorTx` — never a raw transaction. No ORM: hand-written SQL in
  `db/queries/<module>.sql`, generated into typed Go with `sqlc` (`make sqlc`), committed.
  Migrations are forward-only `*.up.sql` files, numbered by the integrator, **never edited
  once merged** (expand/contract per ADR-016).
- **Own authentication, no external identity provider** (ADR-022 supersedes an earlier
  Keycloak/OIDC plan in ADR-005). Argon2id-hashed local accounts, an opaque session cookie,
  CSRF token issued alongside it. `X-Kapsora-App` on every request narrows a person's grants
  to the app they are acting in — a reviewer who is also a member is only a reviewer in the
  backoffice and only a member in the member app; see `internal/identity`.
- **Transactional outbox** for cross-module events (`internal/platform/outbox`), one handler
  per event type, fanned out with `outbox.All` when more than one module must react to the
  same event; the worker drains it at-least-once.
- **API contract-first**: `api/openapi/kapsora-v1.yaml` is the source of truth. Change the
  spec, run `make openapi-generate` (Go) and `pnpm generate` (TypeScript), then implement
  against the generated strict-server interfaces. Spectral must report zero errors; `oasdiff`
  blocks breaking changes in CI. Errors are `application/problem+json` with a stable `code`;
  mutating commands take `Idempotency-Key`; mutable resources use `ETag`/`If-Match`.
- **Web**: a pnpm workspace, `web/apps/{backoffice,provider,member}` (React 19, TanStack
  Router, Tailwind v4) plus shared `web/packages/{ui,auth,i18n,api-client,config}`. Turkish
  copy is complete; English is a fallback skeleton (`i18next`, formal "siz" throughout).
  Money is always an exact decimal string from the server — the client never adds, never
  rounds; a screen shows the server's arithmetic on one line, it does not perform it.
- **No containers anywhere** (ADR-021). Development runs native processes; production runs
  static Go binaries under systemd with nginx or Caddy in front (`deploy/`). Docker,
  Kubernetes, and Valkey/Redis are explicitly out — do not reintroduce them without a new ADR
  and the owner's sign-off.

Full repo layout, non-negotiable engineering rules (tenant safety, SQL conventions, API
contract, security/privacy, quality gates), and the shared platform packages already built
for you are all spelled out in **`docs/delegation/README.md`** — written for an external
developer picking up one work package with zero conversation history, which is exactly your
situation now. Read it in full; it is short and it is the actual rulebook this codebase was
built against, CI enforces most of it.

## 5. How new work gets specified and built

This project is planned as **work packages (WP)**, one file per package under
`docs/delegation/WP-<id>-<slug>.md`: a fixed goal, scope, interfaces, migration numbers,
required tests, and acceptance criteria, self-contained enough that whoever implements it
needs nothing beyond the file. `docs/delegation/REPORT_TEMPLATE.md` is the report format that
comes back. `docs/plan/ROADMAP.md` is the index: milestones, which WPs make up each one, and the
status log. To plan the next slice of work, write the WP file the same way the ~40 already in
that folder are written — read a couple of recent ones (`WP-I7-06`, `WP-X1-01`) as models
before writing a new one.

**UI work goes through the Impeccable skill** (`/impeccable` or the `impeccable` skill in
this environment): its `shape` step for discovery, `new-work`/craft-floor for building,
a finish review before calling anything done. `PRODUCT.md` and `DESIGN.md` are read at the
start of every UI task and updated in the same commit whenever a screen settles a new
pattern — they are the reason the three apps still look like one product after 90+ screens.
Screenshot captures for review live under `.impeccable/review/` and are **git-ignored on
purpose** — never commit them.

## 6. Rules that came from getting burned once — keep them

- **A permission exists in two places, and they can drift.** The real grant is
  `internal/identity/application/roles.go`; the browser demo's mirror is
  `web/packages/api-client/src/mocks/data.ts`. Changing one without the other is a silent
  bug — the repaired SPONSOR_HR gap above was exactly that. `db/tests` has equality tests
  pinning some of these; run `go test ./db/tests/ -run 'Permission|Grant|Role'` after any
  `roles.go` change, with `.env` loaded (`set -a; . ./.env; set +a` first, or the DB tests
  skip silently and prove nothing).
- **Never `git add -A` / `git add .`** while any background agent or long-running task might
  have half-written files in the tree. Stage explicitly, by path. A sweep once pushed a
  subagent's mid-edit files straight to `main`; recovery was a forward revert, not a
  clean history.
- **Migrations are numbered in landing order, not in the order work was issued.** If package
  A is written before package B but B's migration lands first, B gets the lower number.
  `golang-migrate` only applies numbers above the schema's current version — a lower number
  assigned after the fact silently never runs on an already-upgraded database.
- **No plaintext identifier or contact value outside `*_enc`/hash columns, ever** — not in
  logs, not in audit detail, not in a URL, not in a query key, not in browser storage.
  Sensitive identifiers go through `crypto.FieldCipher` + `crypto.BlindIndexer`
  (`internal/platform/crypto`). Tokens and PII never touch `localStorage`/`sessionStorage`.
- **`.env` is git-ignored and is never printed** — load it with
  `set -a; . ./.env; set +a` (POSIX) and, if you must confirm something is set, show the key
  name and length, never the value.
- **This machine's shell culture matters.** A PowerShell `-match` on `[A-Za-z]` silently
  drops the capital `I` under a Turkish locale (`tr-TR` folds it to the dotless `ı`) — every
  `.env` key containing `I` (`KAPSORA_MIGRATE_...`, `KAPSORA_MINIO_...`) was once silently
  skipped this way. `scripts/dev.ps1`'s `.env` loader uses `-cmatch` for exactly this reason;
  follow the same pattern in any new script that pattern-matches identifiers.
- **A browser test that opens a Radix Popover/Popper-based component can hang ~20s per
  test** under jsdom: its selector engine (`nwsapi` 2.2.27) answers `:modal` by recursively
  asking itself, tens of millions of calls per open popover. Already fixed once, generally,
  in `web/packages/config/vitest.setup.ts` (it short-circuits the top-layer selectors before
  any test runs) — if a new shared test setup file appears without that guard, a new
  Popover-based component will silently make CI slow again.
- **Own-file / self-review rule**: a person who is both a reviewer and the subject of a
  record cannot decide their own case — the UI shows a plain notice instead of decision
  controls, the server refuses it if bypassed. This pattern (and the "a page you can't see
  says so plainly" pattern, and the single-sign-in "one door" pattern across the three
  origins) are already built; read `DESIGN.md`'s pattern list before reinventing any of them.

## 7. Working language

Product-facing copy (everything a payer, provider, or member reads on screen) is Turkish,
formal "siz". Code, identifiers, comments, commit messages, and everything under `docs/` are
English — that split is deliberate and consistent across ~50 commits; don't mix them.

## 8. First week, in order

1. `make test-unit && make test-db` green locally — if not, something in your environment
   differs from what CI assumes; fix that before writing code.
2. Run the backend-free demo (`KAPSORA-Demo-Baslat.cmd`) and click through the six scenarios
   in its guide page. It's the fastest way to see the whole product's shape.
3. Read `docs/delegation/README.md` fully, then `docs/plan/ROADMAP.md`'s status log from the bottom up
   (oldest first) through at least M5 — the decisions compound.
4. Read `DESIGN.md` top to bottom once. You will not remember all of it; you will remember
   enough to know when to go back and check it.
5. Get the real system running end to end (`.\scripts\dev.ps1 up`, seeded), and sign in as
   two or three different demo accounts to feel the permission boundaries first-hand.
6. Continue with the approved M10 packages in `docs/plan/ROADMAP.md`. Collect the missing
   pilot/source inputs for customer-specific work; M8/M9 remain deferred by the owner.
