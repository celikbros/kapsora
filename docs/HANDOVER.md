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

**Current owner priority (reconfirmed 2026-09-22): complete the running product, with
health first.** The detailed plan for the approved sequence is the
[PC-01–PC-06 product completion roadmap](plan/ROADMAP.md#current-product-completion-roadmap-2026-09-22).
Member import is locally verified. PC-02 eligibility/request/authorization is active, then
PC-03 outpatient and PC-04 inpatient health, PC-05 invoice/batch/payment, and PC-06
accommodation plus combined acceptance. The roadmap records task dependencies, 15 health
acceptance scenarios, role handoffs, evidence gates and confirmed source/fixture gaps.
The first live health checkpoint passed: provider catalog access, single-enrollment
eligibility, insufficient-quantity refusal, request submission, medical approval and the
provider's updated status. Migration 000050 fixes the reproduced catalog 403 for system
PROVIDER_STAFF in current/new tenants; no catalog maintenance or billing grant was added.
PC-02 is not complete. The quantity-versus-money quote fix now passes local pricing,
HTTP and eligibility regression tests: monetary entitlements cap money; session/night/count
entitlements gate service quantity, including mapping factors and shared per-quote pools.
The operator restarted `dev.ps1 up` on 2026-09-22 and live confirmation passed: the 400 TRY
physiotherapy quote is now payer 400/member 0. Excess quantity and shared-balance refusal
pass, and quote calls leave account balances/row versions unchanged. The real health
request/medical-approval browser regression passed again (8.1 s total); all six CI checks
passed on pricing code head `9b50aa7`.

The next PC-02 authorization slice is implemented and locally tested. The approved request
now exposes an explicit "Hak ayır" action with an operator-chosen expiry; the provider sees
the resulting reference/status. Published-plan mappings and their factors govern the hold,
fulfilment/claim consumption and unused release. Migration 000051 stores the factor per item;
old rows default to 1 to preserve their actual historical ledger units. Cancel/expiry read the
remaining hold after earlier partial release, and create replays still enforce provider scope.
Authorization application/HTTP tests, fractional rounding, clean migration, 486 frontend tests,
typecheck, lint and all app builds pass. The intercepted-response UI browser test passes
validation, identical uncertain retries, stale-list success, list failure and provider read-only
states; this is not live-ledger proof. The full database test package hit its 10-minute timeout;
the focused clean migration test passed separately.

**Live checkpoint (2026-09-22):** the operator restarted the system and the real
provider → medical approval → authorization → provider follow-up → cancellation test passed
(10.1 s total). A read-only ledger check confirmed one RESERVE and one RELEASE, net zero
deltas; the test's 1-unit hold was fully released and the account ended available 20,
reserved 0, consumed 0 with conservation intact. All six CI checks on `68ba0d5` passed,
including the full schema suite that exceeded the local timeout. Local schema remains 51.

The next confirmed PC-02 defect is fixed too: retrying a failed provider submission now
reuses the same draft, ETag and command keys instead of creating another draft. Uncertain
create responses reuse the create key; simultaneous clicks share the same in-flight request.
Attempts are kept only in the current form session, isolated by actor, tenant and input.
The real test with its first submit aborted passed (7.9 s): one create, two identical submit
attempts, then medical approval, reservation, provider visibility and cancellation. The
second hold also has exactly one reserve/release pair and zero net balance change.

**Request correction checkpoint (2026-09-22):** the provider now selects an enrollment
from eligibility candidates, then waits for the selected enrollment's successful check.
Changing member/service/date clears the selection; failed or ineligible checks cannot send.
The candidate list stays available after selecting a plan. An ambiguous top-level
REVIEW_REQUIRED result is no longer displayed as a final refusal.

Provider draft detail now supports saving the service date and service lines, showing the
reviewer's correction text and resubmitting the same request. ETag conflicts or uncertain
saves require an explicit reload; unsaved input is not overwritten by background reads.
An uncertain submit freezes edits and retries the same command key. This also lets a
provider reopen a known draft from the request list after a reload, without another create.
No draft body or patient data is persisted in browser storage.

The live return/correct/resubmit test passed (9.0 s): request
`SR-20260922-ZPT6KIUF` kept version 1 at one session, saved version 2 at two sessions,
resubmitted and received medical approval, with exactly one create. No entitlement was
reserved by this run. Twelve focused frontend tests passed, including explicit selection,
delayed/failed recheck, unchanged submission retries, preserved history and a real mock
ETag conflict. The plan-selection browser test passed (3.8 s) with intercepted ambiguity
and failure responses; its chosen valid plan was rechecked by the live API. This does not
certify a real multiple-enrollment database fixture. Both screens fit 390px and 1440px. The
full frontend suite passed (494 tests); the subsequent validation fix passed four focused
tests and another live correction run (9.9 s). Invalid fields now explain how to correct them,
and comma decimals are normalized without floating point conversion.

**Real enrollment and consumption checkpoint (2026-09-22):**
`real-entitlement.spec.ts` now prepares a separate synthetic person through public APIs,
with EMPLOYEE and MEMBER memberships in the existing sponsor and two enrollments in the
same published DEMO_STANDARD plan (distinct start dates). The real worker opens both sets
of accounts. No existing person, published plan or permission grant is changed.

The browser requires an explicit choice, selects the second enrollment, refuses service
on the exclusive enrollment end date and requires a fresh choice after returning to today.
This last check reproduced a defect: a scope-keyed old selection reappeared when the form
returned to its previous date. Member/service/date handlers now clear selection explicitly;
the focused regression verifies the round trip.

The selected enrollment alone funds the live three-session authorization. Recording a
two-session fulfilment does not consume; completing it moves the account from 17/3/0 to
17/1/2 (available/reserved/consumed). Replayed record/complete commands do not change
balances or versions; a fresh duplicate complete returns 409, over-fulfilment 422, and a
reviewer without fulfilment.record gets 403. Cancellation releases only the unused one:
final 18/0/2, conservation intact, all other accounts unchanged. The account has exactly
GRANT, RESERVE, CONSUME and RELEASE entries. The test passed twice (7.8 s then 9.3 s total).

This proves generic fulfilment through the public API, not the clinical case/report/claim
journey. Completed consumption remains recorded on the synthetic test account. The harness
releases its unused hold even after an assertion failure when its authorization ID is known.
Each run creates one test person, two memberships and two enrollments; do not run outside
the demo environment. The tested authorization is cancelled, so PC-03 must start with a
fresh episode/hold. Do not bill the two generic fulfilment units again as new claim usage.

**Document and negative-transition checkpoint (2026-09-22):** all six CI checks passed
on preceding head `3563eb7`. The next gate defect is fixed: a REQUIRE_DOCUMENT rule
previously blocked every submit even after the required file was attached. The gate now
accepts only matching SERVICE_REQUEST links to CLEAN, secure, retained objects in the
same tenant and the request's provider boundary. Duplicate objects also require retained
canonical bytes. Required type codes remain visible; accepted document IDs/types are
frozen into the version's gate snapshot so unlinking cannot erase the decision evidence.

The existing explicit flow remains reviewer return → provider resubmit. Upload/scan alone
does not move PENDING_DOCUMENT, and no new transition/permission was introduced. The new
PostgreSQL tests verify 14 document boundary cases, preserved history, unlink/recheck,
automatic approval when configured, and mandatory PREAUTH review even with clean files.
They seed scan verdicts in disposable databases; they do not prove real ClamAV upload.
All service-request application/HTTP database tests pass (110.2 s / 54.0 s), as do 50
focused frontend/mock tests, scoped Go/ESLint checks and TypeScript checks.

The real API/browser negative test passed (5.1 s total): provider review commands 403,
empty rejection reason 422, stale ETag 412, medical rejection and provider cancellation
of both draft and submitted requests, stable same-key replays and 409 on reopening a
closed request. The provider sees the rejection reason and no draft editor. All member
account balances and versions stay unchanged. Rejected request:
`01a0ca50-ed16-70b8-a1a5-c29e8b48766b`. Each run leaves three closed synthetic requests;
failure cleanup cancels any still-undecided request it created.

**Provider cancellation and real document upload checkpoint (2026-09-22):** the operator
restarted the system; all six CI checks on `d48d8da` passed. The existing real negative
test passed again after restart (5.0 s). Provider request detail now offers "Talebi iptal et"
only with service_request.cancel and an undecided status. The dialog names the request,
explains finality, requires a readable reason choice and accepts an optional note.

Pending commands disable controls. Uncertain replies retain the exact body/key/ETag,
including after closing/reopening the dialog; a stale/definite refusal requires explicit
reload. 408, 429 and IDEMPOTENCY_IN_PROGRESS retain the retry. Success removes the editor,
upload form and cancel action. Mock return/reject/cancel now replay successful commands
within actor/tenant/request/command scope and reject same-key different bodies, matching
the real cancellation behavior. No permission grant changed.

The extended live test passed (9.3 s total): a real PDF goes browser → MinIO quarantine →
worker/ClamAV → CLEAN/secure, then downloads with identical bytes and SHA-256. The provider
cancels that test request in the browser; the harness drops the response AFTER the API
commits, retries with the same key/body, and verifies the same response/ETag and unchanged
member account balances/versions. Request `01a0cab4-8c83-75e0-b60e-6db5c2825461`, document
`01a0cab4-8fe0-7b6f-aa41-b63019511013`. This is a synthetic INVOICE attachment, not a clinical
report. There was no matching DOCUMENT rule in this live fixture; the previous document
gate tests are still isolated PostgreSQL evidence, not a combined live rule/upload proof.

Regression: 59 focused frontend/mock tests passed, then the final transient-retry and
different-body checks passed in the 12-test cancellation suite; nine backoffice request
tests also pass. Provider build/typecheck,
API-client typecheck, harness TypeScript and lint pass. Desktop/mobile normal and error
captures at 1440/390 have no overflow; detector returned []; fresh finish reviewer: ship.
Frontend/mock-only change after the operator restart; schema 51, no further restart needed.

**Live document-rule checkpoint (2026-09-22):** all six CI checks on `23ecc5d`
passed. The new opt-in `real-document-gate.spec.ts` passed against the operator's running
system (11.1 s test / 12.7 s total). Its separate synthetic person gets one enrollment in
the existing published plan; the real worker funds that person's accounts. A time-bounded
DOCUMENT rule requires INVOICE only for that exact person. Another person's control
request still reaches PENDING_REVIEW, and repeating setup creates no duplicate version.

Missing evidence blocks submission at PENDING_DOCUMENT and prevents medical approval.
The browser uploads a real PDF to MinIO; the harness pauses only the completion command
to test a genuinely unscanned object. Download is refused, and return/resubmit still
stays PENDING_DOCUMENT. After actual worker/ClamAV processing, CLEAN/secure bytes match
the uploaded SHA-256. Upload alone leaves the status unchanged. Reviewer return followed
by provider browser resubmit reaches PENDING_REVIEW at version 3, preserving version 1
and the required type. All funded account balances/versions remain unchanged.

Request `01a0cadc-7edc-7875-b0ef-fca22b43230e`, document
`01a0cadc-8362-78c3-8dec-9b2ee8ad9e3a`, rule version
`01a0cadc-7a46-75f7-9c1f-47a3440b3e5c`. Cleanup cancelled both created requests and
retired the rule version. The synthetic person, enrollment, clean file and rule history
remain as evidence; there was no reservation or consumption.

Fixture setup is the new local-only `seed document-rule` command, using existing
application services with distinct maker/checker identities, as the plan/contract seed
does. It refuses ordinary people, changes no human grants and never reactivates a retired
version. This is offline fixture provisioning, not authenticated rule-author/publisher UI
permission proof. The full seed package passes with PostgreSQL enabled (13.0 s), including publish-gate
positive/negative cases, repeated setup/retirement and unchanged grants. Scoped Go lint,
vet/build, harness TypeScript and ESLint pass. Schema remains 51; no server restart needed.

PC-02 remains active: finish automatic-decision and scoped queue/ownership/role acceptance,
then proceed to PC-03 case/report/claim. Automatic decisions have isolated PostgreSQL
evidence; this live run proves mandatory PREAUTH review. Clinical reports and infected
file refusal are not certified by the synthetic INVOICE test.

Recovery, deployment and full-capacity benchmarking remain deferred. Do not request an
Ubuntu recovery target or continue I10-03. Clinical/health-claim completion requires
PC-02–PC-04 to pass; the health episode's local financial journey closes at PC-05.

**M1 through M7 are recorded as DONE for original implementation delivery.** This does
not certify the current real-browser health chain; its acceptance is tracked separately
in PC-02–PC-04. Schema is at migration `000051` (`db/migrations/`). Every
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
See `docs/runbooks/load-testing.md`. The owner approved import access for the existing
PROGRAM_MANAGER role in current and new tenants (2026-09-21). Migration 000049 updates
existing system roles; provisioning supplies the same permission to new tenants. Custom
roles and other standard roles are unchanged. Password step-up remains required.
Real PostgreSQL/HTTP integration tests cover upload, apply, worker redelivery and duplicate
refusal. The local database is now at 51 (dirty=false). The first live browser run found an upload retry
defect after password step-up: the challenge was cached and the multipart boundary changed
the request hash. Import authorization now runs before idempotency (including replays),
and multipart hashing ignores only its transport boundary. Integration tests cover the
challenge, retry, replay, expired step-up and changed-content refusal. The upload middleware
also honors the existing 20 MiB file allowance plus 8 MiB multipart overhead instead of
the default 1 MiB request ceiling. Real browser confirmation passed twice consecutively on 2026-09-22 against the
operator-started API and worker: password step-up, upload, invalid-row skip, one created
member, one skipped row, name search, logout and protected-page redirect. The test uses
unique source record IDs per run so it cannot update a previous test's member. M11 is PLANNED. M8/M9 remain deferred.

Pilot customer, program, beneficiary group and HR/policy source formats are still external
inputs. Technical preparation can proceed without them; customer-specific integration and
signed acceptance cannot.

### Smaller open items worth knowing about

- The 2026-09-22 startup log reports no worker handler for `invoice.submitted` and
  `settlement.approved`; those events are deferred one hour. Member-import worker
  processing passed independently. Track these warnings in the invoice/payment flow
  review; this import correction does not resolve them. After the pricing restart on
  2026-09-22, a read-only aggregate confirmed the nine pending rows are seven
  `invoice.submitted` and two `settlement.approved`, all deferred (none currently due).
  They were not deleted or marked as processed.

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
make migrate-up                   # schema to 000051
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

To run the real member-import browser regression against that already running system:

```powershell
$env:E2E_REAL_API = '1'
$env:E2E_EXISTING_UI_URL = 'http://127.0.0.1:5181'
pnpm e2e real-import.spec.ts --project chromium --trace off
```

This command starts no servers. It uploads two synthetic rows in DEMO_A, skips the invalid
row, waits for the worker to apply the valid row, checks the member list and signs out.
Each run that reaches apply creates one synthetic member; use the demo database only. The
existing-UI option is for backoffice tests. Trace capture is disabled in this command
because authentication requests contain credentials.

With the same environment variables, run the health-entry regression:

```powershell
pnpm e2e real-health.spec.ts --project chromium --trace off
```

It uses `/portal/` and backoffice on the existing single door, signing out between clinical
provider and medical reviewer. Each run creates and approves one synthetic request in
DEMO_A. It checks service access, eligible/insufficient quantity, submission, medical
approval and provider follow-up. It creates no authorization, case or claim and does not
certify reservation, consumption or financial correctness. Member-app acceptance is pending.

Enable the verified authorization extension of that same real flow:

```powershell
$env:E2E_HEALTH_AUTHORIZATION = '1'
pnpm e2e real-health.spec.ts --project chromium --trace off
Remove-Item Env:E2E_HEALTH_AUTHORIZATION
```

This creates one real synthetic hold, verifies the provider sees it, then cancels it through
the public API as the medical reviewer to return the unused entitlement. This path passed
after the operator restart on 2026-09-22. If interrupted after creation, reconcile the
test authorization before rerunning. To include a simulated first-submit network failure,
also set `$env:E2E_HEALTH_SUBMIT_RETRY = '1'`; the harness verifies one create and two submit
attempts with identical request URL and key. Remove that environment variable after the run.

Run `pnpm e2e real-entitlement.spec.ts --project chromium --trace off` with the same
existing-UI variables for real ambiguous-enrollment, expiry and generic consumption proof.
The operator's worker must be running: this test waits for enrollment events to fund the
accounts. It creates only synthetic demo records and consumes two sessions on its own
new account. The request/authorization/fulfilment/account IDs are attached to the test result.

Run `pnpm e2e real-request-lifecycle.spec.ts --project chromium --trace off` with the same
existing-UI variables for live rejection/cancellation/replay proof. It uses Melis Üye's
existing demo enrollment, creates four new requests (one cancelled in the browser), closes
them and makes no hold or consumption. It does not edit existing requests. The API actor helper shared with the
entitlement harness isolates sessions and never attaches credentials or response bodies.
Set `$env:E2E_REQUEST_UPLOAD = '1'` to include actual browser PDF upload, worker scan,
secure download and byte/hash comparison before cancellation. Remove the flag afterward.
It leaves one synthetic clean document attached to that closed request. Screenshots under
`.impeccable/review/request-cancel*.png` are ignored; result attachments contain safe IDs
only, never signed storage URLs. This upload scenario does not add a rule or clinical claim.

For combined live rule/upload acceptance, load the local `.env` into the test process
without printing it, then use the existing-UI variables above and explicit opt-in:

```powershell
Get-Content -LiteralPath .env | ForEach-Object {
  if ($_ -cmatch '^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*$') {
    [Environment]::SetEnvironmentVariable($matches[1], $matches[2])
  }
}
$env:E2E_DOCUMENT_GATE = '1'
pnpm e2e real-document-gate.spec.ts --project chromium --trace off
Remove-Item Env:E2E_DOCUMENT_GATE
```

This requires Go and the operator-started worker/MinIO/ClamAV. It creates one synthetic
person/membership/enrollment, two requests, one PDF and one scoped rule version. It closes
the requests and retires the rule in cleanup. If interrupted, use the person ID from the
result with `go run ./cmd/seed document-rule <dedicated-person-id> retire`; the version also
has an expiry. Setup accepts only the dedicated synthetic name/UUID marker and DEMO_A.
No human role is broadened and no tenant-wide document requirement is introduced.

Set `$env:E2E_HEALTH_CORRECTION = '1'` for the real return/correct/resubmit extension:
the reviewer returns the request, the provider changes quantity from one to two, the test
checks immutable version 1, and the reviewer approves version 2. It creates one synthetic
request per run. Remove the variable afterward. Without the authorization extension it
does not reserve any entitlement. `request-selection-ui.spec.ts` reads existing demo data
and intercepts ambiguity/failure replies; it creates no requests or enrollments.

`pnpm e2e authorization-ui.spec.ts --project chromium --trace off` reuses the same existing
UI but intercepts only authorization responses; it reads an existing approved demo request
and writes no real hold. It checks uncertain retry, stale-read success, invalid expiry,
read failure, desktop/mobile layout and provider read-only behavior.


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
6. Follow the current PC-01–PC-06 execution sequence in `docs/plan/ROADMAP.md`; the next
   task is the remaining PC-02 checkpoint blockers. Resume M10/pilot work only after product completion is reprioritized;
   M8/M9 remain deferred by the owner.
