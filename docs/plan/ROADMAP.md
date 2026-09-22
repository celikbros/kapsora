# KAPSORA Roadmap

> **Türkçe özet.** Bu yol haritası master planın (docs/plan) artımlarını kilometre taşlarına
> ve dış geliştiricilere verilebilecek iş paketlerine (WP) böler. Claude mimar, entegratör ve
> reviewer'dır; iş sahibi kurye ve karar vericidir; delegeler WP'leri uygular ve
> `docs/delegation/REPORT_TEMPLATE.md` formatında rapor verir. Durum tablosu her artım
> sonunda güncellenir. Konteyner yoktur (ADR-021).

Status legend: `DONE` verified and merged · `ACTIVE` work packages issued · `READY` can be
issued next · `PLANNED` designed, not yet broken into work packages · `BLOCKED` waiting on an
external input.

Authoritative sources: [docs/plan/KAPSORA_Master_Plan_v2.0.md](KAPSORA_Master_Plan_v2.0.md)
(Turkish, normative), [docs/adr](../adr/README.md), the frozen v1.2 specification under
[docs/baseline-v1.2](../baseline-v1.2/). When they disagree, the master plan and ADRs win.

## Current product completion roadmap (2026-09-22)

This is the owner's approved execution order: finish the running product, with health
first. It takes precedence over the older "next" instructions below. The M0–M11 tables
record original implementation deliveries; a historical `DONE` does not certify today's
complete browser workflow. This section tracks that separate acceptance work. It reuses
the existing work-package contracts rather than starting their implementation again.

### Baseline and delivery sequence

- Local schema: 000051 (dirty=false); operator restart and live authorization confirmation passed. API, worker, scheduler and the three apps run through the
  operator's single door at `http://127.0.0.1:5181`; API port 8090. PostgreSQL, MinIO,
  ClamAV and Mailpit are the existing native dependencies.
- PC-01 passed twice in the real browser on 2026-09-22 (15.8 s total). All six GitHub
  checks passed on `fe89f12`. [PR #11](https://github.com/celikbros/kapsora/pull/11) is
  still a draft, stacked on `wp/I10-02-load-performance`; local acceptance is not a
  merge or production release. Recovery PR #10 is outside this work.
- The first live health-entry browser path passed on 2026-09-22: provider eligibility and
  submission, medical approval, then provider follow-up. This is partial PC-02 evidence,
  not complete health-chain acceptance. See the checkpoint below.
- Quantity/money pricing passes local regression and live confirmation after the operator's
  2026-09-22 restart. All six CI jobs passed on code head `9b50aa7`. Authorization, enrollment choice, request correction and retry coverage now have live
  evidence; document/automatic gate and remaining scoped acceptance are tracked below.

| Order | Stage | Current status | Depends on | Result required to close |
| --- | --- | --- | --- | --- |
| PC-01 | Member import | VERIFIED locally; PR open | Existing identity/member setup | Upload → password step-up → invalid-row skip → one created member → search → logout; no duplicate effect |
| PC-02 | Eligibility, service request and authorization | ACTIVE; provider-to-medical approval verified, remaining gates open | Verified member/scenario prerequisites | A real provider can request covered care; approvals/refusals and the authorization/entitlement effects agree |
| PC-03 | Outpatient care, reports and health claims | QUEUED | PC-02 | Real case → encounter/diagnosis → clean report → review → claim ready for invoicing |
| PC-04 | Inpatient care | QUEUED | PC-03 and admission configuration | Preauthorization → admission → extension → discharge → invoice-ready claim; entitlement reconciles |
| PC-05 | Invoice, batch, settlement and payment | QUEUED | An invoice-ready health claim from PC-03/04 | The same episode reaches invoice, payer decision and a reconciled local payment record |
| PC-06 | Accommodation and combined product acceptance | QUEUED | PC-05; existing lodging implementation | Booking and its financial consequences work; cross-app regression and owner walkthrough complete |

**Health completion has two explicit checkpoints.** Clinical and health-claim acceptance
closes after PC-02–PC-04 and all health privacy gates pass. The health episode's financial
journey closes after PC-05. Neither checkpoint includes deferred fiscal/ERP integrations.

### PC-01 — member import: completed evidence

- [x] PROGRAM_MANAGER receives `import.execute` through provisioning and migration 000049;
  tenant/app boundaries and password step-up are tested.
- [x] Multipart retry after step-up succeeds, identical uploads replay, changed content
  conflicts, and worker redelivery does not create duplicate members.
- [x] The real browser uploads two synthetic rows, skips the invalid one, verifies one
  created/one skipped, finds the member and signs out. Source IDs differ on every run.
- Evidence: [real browser regression](../../tests/e2e/real-import.spec.ts),
  [HTTP integration](../../internal/party/transport/http/memberimport_test.go),
  [handover command](../HANDOVER.md#3-get-it-running). Keep this as a regression;
  do not reopen its completed scope without a new failure.

### PC-02 — eligibility, service request and authorization

| Task | Work and acceptance |
| --- | --- |
| PC-02.1 | Establish a repeatable synthetic health scenario: active membership/enrollment (the import test did not create a plan enrollment), valid published plan, funded entitlement, mapped health service, provider scope/capability, valid contract/price, rules and review queue. Inspect existing data first; add only missing scenario configuration through supported services. Never edit published versions or reset the demo database. |
| PC-02.2 | Reconcile the real role matrix with the mock. Verify provider access to member search and selectable services. Resolve the service-catalog permission mismatch using the intended least-privilege API/role design; test allowed and denied roles and keep mocks aligned. Do not grant every mock permission to provider staff. |
| PC-02.3 | Extend the real browser harness to exercise backoffice, `/portal/` and `/uye/` on the existing server. Separate actors/sessions, use stable accessible selectors and unique synthetic source IDs, and avoid retaining credential-bearing traces. |
| PC-02.4 | Query by member and service/date. Cover eligible, ineligible and review-required outcomes; expired enrollment, insufficient balance and multiple candidate enrollments. Let the operator choose a valid enrollment when ambiguous and re-run the check. |
| PC-02.5 | Submit one request and follow it from provider to worklist. Exercise automatic and manual decisions, missing documents, return → correction → resubmission, rejection and cancellation. The provider must see the reason and next permitted action. |
| PC-02.6 | Trace the actual request → authorization → fulfillment path. Prove when an entitlement is reserved, consumed or released according to the existing contract. Determine which action is automatic and which requires an explicit command; implement a missing handoff only after confirming that contract. An APPROVED badge alone is insufficient evidence. |
| PC-02.7 | Retry submission/decision and stale edits, including create-success/submit-failure recovery without a second draft; verify no duplicate request, authorization or consumption. Test the queue's ownership and role rules, other-provider/tenant refusal, and audit/notification effects required by the scenario. |

**Exit:** one complete successful real-provider path plus its rejection/return path;
authorization provenance and before/after balances match; all newly found blocking defects
have regression coverage. Keep the resulting request/authorization as PC-03's input.

References: [eligibility](../delegation/WP-I2-04-eligibility.md), existing M4 packages,
[provider flow](../../tests/e2e/provider.spec.ts),
[request UI](../../web/apps/provider/src/NewRequestPage.tsx).

### PC-03 — outpatient care, report and claim

| Task | Work and acceptance |
| --- | --- |
| PC-03.1 | Provider opens the case from the accepted request, retaining the same member/enrollment/provider. Record an encounter and ICD-10 diagnosis; check primary-diagnosis rules and closure preconditions. |
| PC-03.2 | Draft the medical report with its service/date/quantity scope. Upload a synthetic document through the actual browser path: quarantine → scanner worker → CLEAN → authorized access. Show a useful pending/rejected state; refuse submission without required clean evidence. Do not use a pre-seeded CLEAN file as proof of scanning. |
| PC-03.3 | Medical reviewer takes the work item and approves or rejects. Verify approved coverage/usage; a correction creates a new draft version and preserves the decided version, reviewer and history. Report return is not a supported command; return/resubmit belongs to requests and claims. |
| PC-03.4 | Hand off to the provider billing actor to create/submit the health claim using the case, authorization and report where required. Cover an automatic-priced case and a case needing medical then financial review. Verify per-line decisions, reasons, contract price, approved total and exact payer/member shares. |
| PC-03.5 | Cover duplicate suspicion, insufficient authorization, expired/out-of-scope report, return/correction and stale versions. Readiness must name unresolved blockers and permit invoicing only after every required decision. No service, report usage or entitlement may be counted twice. |
| PC-03.6 | Test clinical/financial projections and sensitive access with real roles at both API and DOM level. HR must receive no diagnosis/report narrative; cross-provider/tenant access is refused; purpose accept/decline and audited access work. Include a reviewer who is also the subject of the record. |

**Exit:** a standard outpatient episode and a report-dependent episode reach invoice
readiness through the real applications. All PC-03 exceptions/privacy gates pass. Save
safe record IDs, totals and statuses for PC-05; a pre-existing invoice is not evidence
that this newly tested episode flowed through billing.

References: existing [M5 packages](#m5-work-packages-issued-2026-09-05),
[report HTTP tests](../../internal/health/transport/http/report_test.go),
[claim integration tests](../../internal/claim/application/claim_test.go),
[current mock provider test](../../tests/e2e/provider-health.spec.ts).

### PC-04 — inpatient care

| Task | Work and acceptance |
| --- | --- |
| PC-04.1 | Verify live admission prerequisites. The checked-in demo seed has no `INPATIENT_DAY`; provide its appropriate health entitlement/mapping, unit and contract price through a new valid configuration if absent. Do not borrow the lodging allowance or mutate a published plan version. |
| PC-04.2 | Open the inpatient case and admission preauthorization; review it and observe the worker's decision event advance the stay. Refuse a second open stay for the same case/provider; repeating the event must not reserve again. |
| PC-04.3 | Record permitted bed/companion segments; refuse prohibited overlaps. Request an extension, prevent a second undecided extension, and exercise approval/refusal with the correct additional authorization. |
| PC-04.4 | Discharge with actual dates. Verify partial-day calculation and unused reservation release across both original and extension authorizations. Verify consumption in the subsequent claim/fulfillment path. Overstay is surfaced for review. Repeated discharge/event delivery has no extra ledger effect. |
| PC-04.5 | Exercise cancellation/date boundaries, preserve clinical privacy, and take the resulting inpatient claim through required review to invoice readiness. |

**Exit:** admission with an approved extension and early discharge reconciles each
authorization separately; a refused/invalid admission or extension behaves correctly;
the inpatient claim is invoice-ready. PC-02–PC-04 health acceptance is then complete.

References: [inpatient contract](../delegation/WP-I5-03-inpatient-preauthorization.md),
[existing integration tests](../../internal/health/application/inpatientstay_test.go).

### Health acceptance checklist

These are planned checks, not claims of passing tests. A row closes only with linked
real-system evidence and the relevant automated regression. UI paths require browser
evidence; race, duplicate-delivery and ledger invariants may use focused database tests.

| ID | Scenario | Stage | State |
| --- | --- | --- | --- |
| H01 | Provider finds a member, selects service/enrollment and gets the correct eligibility result | PC-02 | Verified for demo: single and real multiple-enrollment selection passed |
| H02 | Invalid date/enrollment or insufficient balance is explained; ambiguous enrollment is selectable | PC-02 | Verified for demo: insufficient quantity, real ambiguity and exclusive enrollment end passed |
| H03 | Automatic/manual request decision and return/correct/resubmit reach the provider | PC-02 | Partial: live approval/correction/rejection/cancellation passed; document and automatic gates pass PostgreSQL tests, real upload/gate still pending |
| H04 | Authorization/fulfillment and exact entitlement effects agree; retries do not duplicate | PC-02 | Generic path verified: live reserve/record/complete/replay/release; clinical claim consumption remains PC-03 |
| H05 | Same episode reaches case, encounter and valid diagnosis | PC-03 | Pending |
| H06 | Browser-uploaded evidence is scanned CLEAN; missing/unsafe evidence cannot pass submission | PC-03 | Partial: real PDF upload/ClamAV/secure download passed; clinical report and live missing/unsafe gate remain |
| H07 | Report review, coverage and immutable correction history work | PC-03 | Pending |
| H08 | Clinical provider → billing → medical → financial handoff reaches invoice-ready claim | PC-03 | Pending |
| H09 | Duplicate/report/authorization blockers and corrected claim history are accurate | PC-03 | Pending |
| H10 | HR/financial projections exclude forbidden clinical fields in API and DOM | PC-03/04 | Pending |
| H11 | Sensitive purpose, access audit, self-review and other-provider/tenant boundaries hold | PC-03/04 | Pending |
| H12 | Admission approval advances the stay once and refuses duplicate open admission | PC-04 | Pending |
| H13 | Extension and segment rules hold; refusal leaves balances correct | PC-04 | Pending |
| H14 | Early discharge, partial days and overstay reconcile original/extension authorizations | PC-04 | Pending |
| H15 | Inpatient claim becomes invoice-ready without duplicate consumption | PC-04 | Pending |

### PC-05 — health invoice, batch, settlement and payment

1. Carry the accepted health claims into provider billing. Validate invoice header,
   fiscal year/number, allocations, tolerance and required image; block an unready claim.
   Submit once, freeze the submitted content, and verify correction/cancel rules.
2. Create and submit a batch of compatible invoices. Financial review records approval,
   cut, return and rejection with reasons and exact totals. Enforce reviewer/submitter
   separation and any configured second-person threshold. Returned invoices release
   their claims for supported correction, preserving the old record.
3. Observe the real worker create one settlement from the batch decision (seed helpers do not prove this delivery). Check contract due date, then approve with password step-up and the required distinct approver.
   Record partial then final payment; refuse overpayment and duplicate external reference.
   The payable, paid and outstanding amounts reconcile without editing append-only history.
4. Verify the member reimbursement branch with synthetic receipt/account information:
   clean-document gate, duplicate/ceiling checks, review, approved-only entitlement
   consumption and truthful local payment status. Actual bank transfer/ERP posting is deferred.
5. Compare provider statement and daily reconciliation; verify permitted export generation,
   download authorization, watermark/expiry and the corresponding audit record.
6. Classify the observed `invoice.submitted` / `settlement.approved` no-handler warnings
   against their contracts. Confirm required local actions and notifications work;
   repair missing in-scope consumers if found. Keep deferred integration events visible
   and documented rather than adding a discard handler just to remove a warning.

**Exit:** the same health episode has traceable claim → invoice → batch → settlement →
payment records, with exact reconciled totals and rejection/correction paths proven.
This is local product financial acceptance, not a claim of fiscal submission or bank transfer.
References: [M7 work packages](#m7-work-packages-issued-2026-09-07).

### PC-06 — accommodation and combined acceptance

1. Verify the member's person scope, property/room inventory, valid lodging terms and
   explicit member contribution. Search → quote → hold → approval/confirmation → voucher.
   Check stale quote/expiry, inventory bounds and single entitlement reservation.
2. Exercise provider check-in/out, free/penalized cancellation, reviewed no-show and
   waiting-list offer/expiry. Verify inventory and entitlement release/consumption once.
3. Follow a completed stay and a chargeable cancellation/no-show into claims and the
   accepted PC-05 billing path. Check a free cancellation creates no charge.
4. Run selected import, health, billing, lodging and reimbursement regressions together
   with isolated synthetic records. Check tenant switching, role-specific navigation,
   shared sign-in/logout, useful error states and critical desktop/mobile actions.
5. Finish an owner walkthrough using the same accepted scenarios. Update this roadmap
   and HANDOVER with passed paths, remaining nonblocking issues and precise exclusions.
   Keep merge/release status separate from local acceptance; do not merge automatically.

**Exit:** all in-scope core journeys pass, no open blocker causes a dead end, wrong money/
entitlement result, unauthorized clinical disclosure or duplicate operation. Record any
accepted minor issue explicitly. References: [M6 packages](#m6-work-packages-issued-2026-09-06).

### Roles and shared prerequisites

| Actor | Responsibility in the live scenario | Boundary to verify |
| --- | --- | --- |
| `admin.a` | Program/member/scenario configuration | Administrative access is not automatic clinical review access |
| `provider.a` | Hospital eligibility, requests, case, encounter and report | Only its provider scope; do not inherit mock billing powers |
| `billing.a` | Hospital claim, invoice and batch | Same hospital, distinct billing role |
| `doctor.a` | Medical review | Clinical purpose and self-review rules; no financial decision by this role alone |
| `financial.reviewer` | Financial claim/invoice/batch review | Financial projection and decision limits |
| `payer.approver` | Settlement approval/payment actions as permitted | Step-up, maker-checker and payable ceiling |
| `sponsor.hr` | Sponsor's allowed member/financial view | No diagnosis or report narrative in response or DOM |
| `staff.member` | Reviewer and beneficiary test case | Cannot decide own case even when a review grant exists |
| `member.a` / `reservation.a` | Member journey / hotel desk | Person scope / hotel provider scope |

Use synthetic data only. Do not print credentials, clinical payloads or identifiers in
test output; keep screenshots/results ignored. Real business configuration and approvals
stay in the application/API, never in direct database bypasses. Existing roles are the
starting point; a permission change must be narrowly justified, with real/mock parity and
negative tests. The operator owns long-running servers; request a restart only after a
backend change is concrete and locally checked.

### Known findings, uncertainty and scope control

| Finding as of 2026-09-22 | Evidence / confidence | Planned action |
| --- | --- | --- |
| Import and initial health request/medical approval are verified; remaining health chain is not | Two live import runs and the new real health-entry regression | Preserve regressions; close remaining H01–H15 gates |
| Provider catalog 403 fixed; broader mock billing grants still differ from real clinical staff | Live reproduction; migration 000050 and provisioning fix; positive/negative role tests and browser catalog 200 | Catalog portion of PC-02.2 verified; retain separate billing role and reconcile remaining mock drift |
| Candidate selection, date reset and correction/resubmit verified | Real two-enrollment browser path and return/correction passed | Remaining automatic/document/negative PC-02.5 gates |
| Explicit request authorization handoff is implemented and verified | Real provider visibility and reserve/consume/release checks pass; approval and reservation remain separate commands | Verify clinical episode/claim usage at PC-03 |
| Session quantity no longer caps money | Live PHYSIO_SESSION quote is contract/payer/member 400/400/0 TRY with unchanged balances | Preserve regression; verify same-episode claim pricing at PC-03 |
| Mapping-aware authorization is implemented | Factor-2 and fractional integration tests pass; real selected-enrollment factor-1 reserve/consume/release passes | Keep clinical usage and inpatient partial release acceptance separate |
| Checked-in seed lacks admission service; seeded CLEAN report bypasses upload scanning | `cmd/seed/business.go`, `businessplan.go`, `staffmember.go`; current DB configuration not audited | PC-04.1 admission fixtures; PC-03.2 genuine upload/scan |
| Dedicated provider/member projects still use their test ports; single-door provider/backoffice handoff now works | `real-health.spec.ts` runs under chromium with explicit `/portal/` routes and logout between actors | PC-02.3 partially verified; member handoff and dedicated-project URL generalization remain pending |
| `invoice.submitted` has no consumer; `settlement.approved` is the deferred M9 posting boundary | Observed startup warnings plus worker/port inspection; settlement notification is published separately, so local failure is not established | PC-05.6 document event handling policy and test required local effects; do not invent automatic batch creation |
| Historical UI list/detail gaps may already have changed | Dated status-log notes are not current reproduction evidence | Check while exercising their scenario; create fixes only for reproduced gaps |

One end-to-end journey is active at a time. Finish its blocking fixes and regression
before starting the next stage. Read-only investigations and isolated tests may proceed
in parallel. If an external dependency blocks a path, name the exact blocker and finish
independent work within the current stage; do not silently mark it complete or switch
to recovery/deployment. Routine reversible fixes follow the approved scope. Business
rule changes or genuinely new access decisions are brought back with a concrete proposal.

### PC-02 checkpoint — 2026-09-22

- **Delivered:** `PROVIDER_STAFF` can read service/diagnosis catalogs in the provider app.
  Reproduced `GET /service-definitions` 403 before the fix, then 200 in the real browser.
  Migration 000050 updates only existing system roles, idempotently and within each tenant;
  provisioning matches. Custom roles, other app grants and billing/maintenance remain unchanged.
- **Real browser:** [real-health.spec.ts](../../tests/e2e/real-health.spec.ts) passed
  (one test, 6.1 s total) against the operator-started API/UI with local schema 50.
  Provider selects the synthetic member and physiotherapy, obtains one eligible enrollment,
  receives ineligible for quantity 100000, restores 1 and submits. The doctor approves;
  a fresh provider login sees APPROVED and the case link. Reference: `SR-20260922-CNZOXYS7`.
  Screenshots were reviewed and remain ignored under `.impeccable/review/`.
- **Configuration:** supported APIs resolved the seeded active enrollment, mapped service,
  hospital profile and a published contract price. The quote probe supplies HEALTH and the
  eligibility result's entitlement codes as required by the pricing contract. It reproduced
  the quantity/money mismatch above; a found contract is not financial acceptance.
- **Regression:** provider new/existing tenant authorization and repeatable upgrade tests,
  fresh migration-to-50 test, catalog read-versus-write HTTP tests, and all database
  permission/grant/role tests passed (the latter 50.889 s, no skips). Mock M5 Vitest: 27 passed.
  Go vet, scoped golangci-lint, ESLint, Prettier and diff checks passed for the changed scope.
- **Still open:** unit-aware quote, explicit authorization/fulfillment UI and ledger evidence;
  enrollment selection, date/automatic-decision branches, return/correction/resubmission,
  failed-submit draft recovery, complete role parity, queue and cross-provider negatives.
  PC-03–PC-06 have not started. An approval badge is not a reserved entitlement.
- **Next work order:** fix pricing units with money/quantity regressions; expose and verify
  the contracted authorization handoff; close remaining PC-02 exceptions, then begin PC-03.
  The remaining list now has reproduced access and pricing evidence, but authorization UI
  and outpatient fixtures still need their first execution. A calendar finish date would
  be speculative; reassess health completion effort after PC-02 closure and the first PC-03 run.

### PC-02 pricing correction — 2026-09-22

- **Problem:** a quantity entitlement was fed into the monetary cap. A 20-session balance
  could produce payer 20/member 380 for a 400 TRY service, even when one session was covered.
- **Correction:** the eligibility resolver supplies matched account identity, unit and mapped
  draw quantity internally. Pricing reads the published service mapping for the service date
  and uses its factor; caller hints cannot override a mapping. MONEY retains the monetary
  cap. Non-money accounts gate the requested quantity and leave contract price, co-payment,
  PRICE rules and currency rounding intact. Lines share a pool by actual account ID; a
  quantity-short line is refused, not converted into an invented monetary allowance.
- **Evidence:** new database regressions initially failed and now pass. A 20-session account,
  500 TRY price and 20% co-payment produces 400/100; a factor of 2 rejects eleven services,
  and two lines drawing twelve units each cannot both use the same twenty-unit balance.
  Ledger rows, account/reservation counts and all account balances remain identical.
- **Validation:** `go test ./internal/pricing/... ./internal/benefit/eligibility -count=1`
  passed with the real test database (pricing application 75.363 s, HTTP 19.063 s,
  eligibility 56.061 s; no skips). Pure cases cover all four pricing methods, rules/share,
  fractional/zero quantities, overdraft, inactive eligibility and undecided lines.
  The existing money-cap, replay, expiry, rounding and provider/tenant boundary tests pass.
  Focused quantity tests passed again after the final shortage-explanation adjustment.
  Go vet, scoped golangci-lint (0 issues), API build and diff checks pass.
- **Delivery boundary:** no migration, new dependency or wire shape change. Existing stored
  quotes and idempotent replays retain their original immutable result; use a new quote to
  verify the correction. Local schema remains 50. The operator restarted `dev.ps1 up`
  at 11:18 local time; the new live quote below verifies the running correction.
- **Live confirmation:** on code head `9b50aa7`, a new PHYSIO_SESSION quote with quantity 1
  and HEALTH context only (no entitlement-code hint) returns QUOTED, contract 400 TRY,
  payer 400, member 0. Quote ID: `01a0c837-774a-7900-97d9-108a745248d5`.
  Quantity 100000 returns NOT_ELIGIBLE/payer 0. Two lines of 11 share the existing 20-session
  balance: the first is QUOTED, the second NOT_ELIGIBLE/BALANCE_INSUFFICIENT, total PARTIAL.
  Every account's available/reserved/consumed/expired/total and row version is identical
  before and after these three new quotes. This was checked through supported APIs.
  The operator-server browser regression passed again (one test, 8.1 s total) for provider
  eligibility/submission, medical approval and provider follow-up. All six CI jobs passed
  on `9b50aa7`. The displayed terminal backlog was separately classified read-only:
  seven invoice.submitted and two settlement.approved pending events, all deferred and none
  due at inspection; retain these for PC-05 rather than marking them processed.
- **Next:** implement the authorization handoff together with
  mapping-aware reservation/consumption and retry/release evidence. Source review found
  generic authorization still assumes matching service/entitlement codes and factor 1.
  No reservation UI or complete H04 acceptance is delivered by this pricing correction.

### PC-02 authorization handoff — 2026-09-22

- **Delivered:** approved backoffice requests have explicit entitlement reservation with a
  required operator-selected expiry and `authorization.manage` permission. Providers see
  reference, expiry and status. Failed reads offer no creation; uncertain submissions keep
  the same body/key, and confirmed success survives a temporarily stale list. The mobile
  header's theme button now hides as intended, removing the observed 390px overflow.
- **Ledger:** resolve the selected enrollment's published plan on the service date, prefer
  explicit service mappings to the legacy code fallback, and reserve the mapped quantity.
  Migration 000051 stores each authorization item's factor; existing rows retain factor 1.
  Service counters remain service quantities. Fulfilment/claim consumption and unused release
  use entitlement units with cumulative rounding. Cancel/expiry release the actual remaining
  hold after an earlier discharge release. Replayed creation rechecks provider scope.
- **Evidence:** full authorization application/domain/HTTP tests pass (application 84.746 s,
  HTTP 20.485 s), including mapping factor 2, insufficient-balance rollback, retry conservation,
  consumption, partial unused release followed by cancel and scoped replay refusal. Fractional
  consume/release conserves a 0.333333-unit hold. Health and claim application regressions
  passed earlier in this slice. The clean-schema test passes at 51; the broad database package
  exceeded its default 10-minute limit and is not reported as passed.
- **UI evidence:** 486 frontend tests, typecheck, lint and three production builds pass.
  `authorization-ui.spec.ts` passed (7.6 s) with intercepted authorization replies against
  the existing UI, checking past-date refusal, frozen uncertain retry, stale-list success,
  failed-list refusal and provider read-only states. 1440px/390px captures inspected;
  Impeccable detector returned no findings and the delegated finish review closed as ship.
  The original real provider-to-medical-approval flow passed again (6.4 s) after the UI change;
  this default run creates no authorization and does not replace the pending opt-in check.
- **Live boundary:** local migration is 51, dirty=false. Operator restart is still required;
  `E2E_HEALTH_AUTHORIZATION=1` extends `real-health.spec.ts` with real reservation, provider
  follow-up and cancellation of the synthetic hold. This opt-in path is prepared, not yet
  executed against the new running Go code. No PC-02 or complete-health closure is claimed.
- **Next:** restart/live confirmation, then enrollment selection, returned-request correction
  and failed-submit recovery before proceeding to PC-03 outpatient care.

### PC-02 live authorization and submit retry — 2026-09-22

- **Live handoff passed:** after the operator restart, the real provider/medical reviewer
  browser test reserves one session, displays the authorization to a fresh provider login,
  then cancels it through the public API (10.1 s total). Authorization `AUT-20260922-SAVI7HNY`
  is CANCELLED; its hold is RELEASED with quantity 1, consumed 0, released 1. A tenant-scoped
  read-only query confirms exactly one RESERVE and one RELEASE, net zero deltas, account
  available/reserved/consumed 20/0/0 and conservation true.
- **CI:** all six checks passed at `68ba0d5`, including the complete PostgreSQL schema suite.
  This resolves the uncertainty left by the prior local 10-minute timeout; it does not
  certify the remaining PC-02 product gates.
- **Retry defect fixed:** provider `useCreateAndSubmit` previously created a new draft on
  every retry. The form now retains the draft/ETag and separate create/submit idempotency
  keys per actor, tenant and input; concurrent identical clicks share one request. An
  uncertain create retries its original body/key. A known successful submit stays confirmed.
  Changing and returning to earlier input reuses that input's attempt. The cache is local
  to the mounted form; reload recovery and stale-version editing remain separate work.
- **Retry evidence:** four coordinator cases plus the provider screen regression pass
  (nine tests across two files). The real browser run with
  `E2E_HEALTH_AUTHORIZATION=1 E2E_HEALTH_SUBMIT_RETRY=1` passed in 7.9 s: first submit is
  deliberately aborted, the retry uses the same URL/key and exactly one draft is created.
  The remainder of the real medical/authorization/provider/cancel flow passes. Its
  `AUT-20260922-IGYTFDAM` hold also has exactly one reserve and one release, net zero,
  final account 20/0/0 and conservation true. Provider typecheck, lint and build pass.
- **Next confirmed gaps:** `NewRequestPage` does not offer the server's enrollment
  candidates; provider `RequestPage` shows return reasons without correction/resubmit
  controls. Complete these PC-02.4/PC-02.5 paths before PC-03 outpatient care. The successful
  test holds were cancelled deliberately; no live clinical consumption is claimed.

### PC-02 plan selection and returned-request correction — 2026-09-22

- **Selection:** provider new-request keeps the eligibility candidate list, requires an
  explicit choice when ambiguous and rechecks the selected enrollment before enabling
  submission. Person/service/date changes discard the previous choice; pending, failed,
  missing-data and ineligible results offer no submit. REVIEW_REQUIRED at result level
  is displayed as review, including ambiguity, rather than a final refusal.
- **Correction:** DRAFT request detail exposes the existing API's header and whole-line
  editing commands, reviewer's return explanation, save then submit. Roles still gate
  editing/submitting separately. Saved ETags chain across header/line writes; partial or
  uncertain saves require an explicit current-record reload. Background changes do not
  overwrite unsaved input. An uncertain submit retains its key and freezes edits. A known
  draft can be reopened from the list after reload; no browser storage of draft/PII was added.
- **Live evidence:** `E2E_HEALTH_CORRECTION=1` real browser test passed (9.0 s total).
  `SR-20260922-ZPT6KIUF` travelled provider submit → medical return → provider save/resubmit
  → medical approval → provider follow-up. Exactly one request was created; version 1 kept
  quantity 1 while version 2 had quantity 2. No hold/case/claim was created by this run.
- **UI/negative evidence:** 12 focused tests pass: two valid mock enrollment candidates,
  explicit second choice, delayed and failed recheck, same-ID correction, unchanged history,
  same-key submit retry and actual mock ETag refusal with explicit reload. The separate
  `request-selection-ui.spec.ts` passed (3.8 s) against the running UI, intercepting only
  ambiguity and failure replies while rechecking the valid choice against the real API.
  It creates no enrollment/request and is not real multi-enrollment database acceptance.
  Both changed screens were captured without page overflow at 390px and 1440px.
- **Delivery:** frontend-only; schema 51, no permission/contract changes or new dependency.
  Provider typecheck, lint and production build pass (existing Vite chunk-size advisory).
  Prior commit `8589fc7` has all six CI checks green. Full frontend suite passed (494 tests);
  after the field-validation review fix, all four request-flow tests, provider build/typecheck
  and lint passed. The live correction run passed again (9.9 s), including invalid-field
  feedback. The review fix adds accessible field errors and string-only comma decimal
  normalization.
- **Next:** real multi-enrollment/invalid-date fixtures, automatic/document/negative
  lifecycle gates and live fulfilment/consumption. H02/H03 and PC-02 remain partial;
  PC-03 clinical acceptance is still queued.

### PC-02 real enrollment selection and generic consumption — 2026-09-22

- **Real fixture:** public person/membership/enrollment APIs create a unique synthetic person
  with EMPLOYEE and MEMBER memberships under the existing sponsor, each enrolled in the
  same published DEMO_STANDARD plan with distinct start dates. The actual worker funds
  both account sets. This is real ambiguous enrollment, not two newly published plans;
  existing configuration and existing people's balances are untouched.
- **Browser/date proof:** provider sees both candidates, chooses the second, cannot submit
  on the exclusive enrollment end date, then must choose again on returning to the original
  date. This reproduced and fixed selection resurrecting from the earlier scope: member,
  service and date changes now explicitly clear it. The request stores the chosen enrollment.
  Eligibility and request submission leave every balance and row version unchanged.
- **Ledger proof:** medical reviewer approves and creates a three-session authorization;
  the provider sees it in request detail. Only the chosen account changes from 20/0/0 to
  17/3/0. Provider records two sessions through the fulfilment API with no balance effect,
  then completes them, leaving 17/1/2. Same-key create/complete replays preserve IDs,
  balances and versions. Fresh duplicate complete is 409; over-fulfilment is 422; medical
  reviewer without fulfilment.record is 403. Cancelling the authorization releases only
  the unused session: 18/0/2, conservation true. All other accounts are byte-for-byte
  unchanged, and the selected account has one GRANT, RESERVE, CONSUME and RELEASE.
- **Evidence:** `real-entitlement.spec.ts` passed twice (7.8 s and 9.3 s total), without
  intercepted responses. Latest request `01a0c9b8-9d26-7d79-8347-46027d2eb335`,
  authorization `01a0c9b8-9daa-7897-9b36-fdee8a3994c2`,
  fulfilment `01a0c9b8-a0d2-7220-914f-08648b743ba8`,
  account `01a0c9b8-95b2-7e2b-8999-e275aa28cffa`. The 13 focused provider tests,
  provider typecheck/build, lint and harness TypeScript check pass. All six preceding
  CI checks on `2a046fd` passed. Current code change is frontend-only; schema remains 51.
- **Boundary/cleanup:** no clinical case, medical report or claim was created. Generic
  fulfilment and claim submission are distinct consumption commands; the consumed units
  are not reusable as new claim usage. Each run leaves synthetic person/membership/enrollment
  records and its completed consumption, while cancelling the remaining hold. A known hold
  is also cancelled in failure cleanup. PC-03 needs a fresh episode with an active hold.
- **Next:** remaining automatic/document/rejection/cancellation request gates and scoped
  acceptance, then PC-03 clinical case/report/claim. H01/H02 now have real demo evidence;
  H04 has generic API evidence. PC-02 is still active, not a complete-health sign-off.

### PC-02 document gate and negative transitions — 2026-09-22

- **Fixed:** a DOCUMENT rule no longer requests an already supplied clean attachment on
  every resubmit. Matching type, SERVICE_REQUEST aggregate/ID, tenant, provider boundary,
  CLEAN verdict, secure bucket and retained bytes are required. A duplicate's canonical
  object must also be retained. The complete type requirements stay on the request, and
  accepted attachment IDs/types are frozen into the version for later explanation.
- **Lifecycle:** existing reviewer return → provider resubmit opens a new immutable
  submission; upload completion alone does not change PENDING_DOCUMENT. No endpoint,
  status transition, role grant, schema change or automatic scan-event consumer was added.
- **PostgreSQL proof:** 14 boundary cases plus return/resubmit/history/unlink, configured
  automatic approval and PREAUTH precedence passed. Full service-request application and
  HTTP suites passed in 110.2 s / 54.0 s with database tests enabled. Entitlement accounts
  and ledger stay untouched. These disposable database fixtures seed scan verdicts;
  real upload/ClamAV acceptance is still pending.
- **Live negative proof:** `real-request-lifecycle.spec.ts` passed in 5.1 s. Provider
  cannot review (403); empty rejection reason fails (422), stale ETag fails (412), valid
  medical rejection closes every line. Provider can cancel draft and pending-review
  requests. Same-key replay preserves the exact response/ETag; closed requests refuse
  further transitions (409). Fresh provider UI shows the rejection reason without an
  editor. All member account balances and row versions are unchanged. Rejected request
  `01a0ca50-ed16-70b8-a1a5-c29e8b48766b`; cancellations
  `01a0ca50-edfe-7f6c-a774-a6fcfe83ddcd`, `01a0ca50-ee51-704f-a82b-1ed6c6ed3aaa`.
- **Regression/delivery:** real and mock gates match; 50 focused frontend tests pass,
  as do TypeScript, ESLint and scoped Go lint. The repository-wide Go lint command hits
  pre-existing duplicate main functions in ignored `tmp` utilities, so lint was run on
  `./internal/servicerequest/...` successfully. All six preceding CI checks passed on
  `3563eb7`. Schema 51; operator restart needed to load the new Go document gate.
- **Next:** provider request cancellation control (API proven, portal action absent),
  actual upload/scan plus gate acceptance and remaining scoped queue checks, then PC-03.
  H03/PC-02 remain open; no complete-health claim and no demo configuration/grants changed.

### PC-02 provider cancellation and actual upload — 2026-09-22

- **Restart/CI:** the operator restarted the system and real negative transitions passed
  again (5.0 s). All six CI jobs passed on `d48d8da`.
- **Portal cancellation:** undecided request detail now offers the existing cancel command
  with its separate permission, readable reason choices, optional note and explicit finality.
  Pending controls freeze; unknown outcomes retain body/key/ETag across dialog reopening.
  Explicit reload resolves stale/definite refusal before another command. Transient 408,
  429 and IDEMPOTENCY_IN_PROGRESS permit the same retry. Confirmed closure removes editing,
  upload and cancellation. No new role grants or backend/schema changes.
- **Live UI proof:** the extended `real-request-lifecycle.spec.ts` passed in 9.3 s with
  E2E_REQUEST_UPLOAD=1. A fourth request is cancelled in the provider browser, the first
  response is intentionally lost after server commit, and the retry returns the same
  outcome/ETag without changing account balances/versions. Safe request ID:
  `01a0cab4-8c83-75e0-b60e-6db5c2825461`.
- **Real file proof:** browser uploads an entirely synthetic PDF to signed MinIO quarantine
  storage, actual worker/ClamAV promotes it to CLEAN/secure, the UI offers download and
  downloaded bytes/hash equal the original. Document `01a0cab4-8fe0-7b6f-aa41-b63019511013`.
  One clean synthetic INVOICE attachment remains on the closed request. No document rule
  matched this fixture; clinical report submission and combined rule/upload acceptance
  are not claimed. The current seed has no RULE_AUTHOR/RULE_APPROVER actors; a safe scoped
  acceptance fixture is needed, without widening existing grants or a tenant-wide test rule.
- **Regression/design:** mock reason commands now match successful real idempotent replay
  and reject same-key changed payloads after enforcing actor/tenant/provider access. The
  59 focused tests passed; final cancellation suite (12 tests) also passes transient errors,
  stale reload failure, permission and terminal-status hiding; nine backoffice request tests
  also pass. Provider build/typecheck,
  API-client/harness typechecks and lint pass. 1440/390 normal/error captures have no page
  overflow; detector []; independent finish review: ship. Existing Vite chunk-size advisory.
- **Next:** remaining live rule/document and scoped queue acceptance, then PC-03 outpatient
  case/report/claim. Frontend-only delivery needs no new restart. PC-02 remains active.

### Progress, evidence and timing

- The coding agent implements, tests and records evidence; the operator controls servers;
  the owner decides new business scope and reviews the finished journeys.
- Each stage closes with: scenario IDs and date, tested commit/environment, safe record
  IDs, expected/actual states and amounts, regression command/result, unresolved issues,
  and the next task. Store the summary in this section/status log and link HANDOVER to it.
- Use browser tests for actual handoffs, focused Go/PostgreSQL tests for authorization,
  money, ledger and retry invariants, and Vitest for changed UI behavior. Run relevant
  format/lint/type checks and inspect CI on the delivered head. Do not rerun unrelated
  suites merely to fill time; UI design changes also follow the existing design workflow.
- The first execution checkpoint is the PC-02 baseline: which of H01–H04 pass and which
  concrete defects block them. At that checkpoint give a remaining-work estimate for
  PC-03/04 based on the observed defect list and fixture readiness, then update it after
  the first outpatient run. There is no defensible health finish date yet, and no
  unmeasured percentage is reported as progress.
- Health exit: PC-02–PC-04/H01–H15 verified. Financial exit: PC-05 verified. Product
  acceptance: PC-06 verified. External customer sign-off is a later, separately named gate.
- Deferred by the owner: recovery/restore work, deployment, full-capacity benchmarking,
  fiscal/Nettefatura and ERP integration (M8/M9). Customer-specific import adapters and
  signed pilot acceptance are outside this pass and still need external business inputs.
  Routine negative authorization checks remain in every stage.
  No Ubuntu target, vendor credentials or pilot data are prerequisites for this synthetic
  product-completion plan; real customer onboarding will require those business inputs later.


## Working model

| Role                              | Who                  | Responsibilities                                                                                                                                                          |
| --------------------------------- | -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Architect / integrator / reviewer | Claude (Claude Code) | Designs interfaces, writes work packages, reviews and merges delegate work, keeps the roadmap and ADRs current, builds cross-cutting pieces itself                        |
| Product owner / courier           | Business owner       | Approves scope and decisions, hands work packages to developers, brings back their reports and code, provides external inputs (credentials, vendor documents, pilot data) |
| Delegate developer                | External developers  | Implement one work package at a time exactly as specified, deliver code + tests + report                                                                                  |

Every work package (WP) is self-contained: goal, scope, interfaces to respect, tests required,
acceptance criteria and the report format. Delegates never need the conversation history.
See [docs/delegation/README.md](../delegation/README.md).

## Milestones

| #   | Milestone                                      | Plan increment | Status                               | Exit criteria (summary)                                                                                                                                                                         |
| --- | ---------------------------------------------- | -------------- | ------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| M0  | Foundation                                     | I0             | DONE (2026-09-02)                    | Repo, corrected migrations 1-9, OpenAPI v1, Go skeleton, CI, schema tests green on PostgreSQL 18.4                                                                                              |
| M1  | Identity, tenants, organizations               | I1             | DONE (2026-09-03)                    | Login with KAPSORA accounts (ADR-022), tenant switch, permissions enforced, organization CRUD with VKN dedup, audit and idempotency live, three web shells, native local environment documented |
| M2  | People, plans, eligibility, entitlement ledger | I2             | DONE (2026-09-03)                    | Encrypted identifiers + HMAC search, member import, program/plan/version/enrollment, eligibility API, ledger with reservations; 100 concurrent reserves without double spend                    |
| M3  | Catalog, providers, contracts, pricing, rules  | I3             | DONE (2026-09-04)                    | Deterministic contract/price selection, published versions immutable, CEL rule sets with test cases and maker-checker publish                                                                   |
| M4  | Requests, workflow, documents, notifications   | I4             | DONE (05.09.2026)                    | Explicit transitions only, work queues with SLA, quarantine-scan-secure document pipeline, PII-free notifications                                                                               |
| M5  | Health vertical                                | I5             | DONE (2026-09-06)                    | Outpatient claim invoice-ready end to end, inpatient preauthorization with medical review, clinical/financial visibility separation                                                             |
| M6  | Accommodation vertical                         | I6             | DONE (2026-09-07)                    | Inventory never negative under 500 concurrent holds, hold expiry releases entitlement, cancellation policy snapshots                                                                            |
| M7  | Claims, invoices, batches, settlement          | I7             | DONE (2026-09-11)                    | Line-level decisions audited, submitted batches immutable, settlement never exceeds approved total                                                                                              |
| M8  | Fiscal: GİB e-documents via İşNet Nettefatura  | I8             | DEFERRED to last (owner, 04.09.2026) | 95% auto-match on mock inbox, real inbox + application response on Nettefatura test environment, 8-day SLA work items                                                                           |
| M9  | Accounting integration                         | I9             | DEFERRED to last (owner, 04.09.2026) | Approved settlement appears in the ERP as purchase invoice + voucher + payment order; ERP payment closes settlement; reconciliation diff zero or explained                                      |
| M10 | Integrations, hardening, pilot                 | I10            | ACTIVE                              | HR/policy import adapters, load test targets, DR drill, pentest findings closed, UAT signed                                                                                                     |
| M11 | MVP+1                                          | I11            | PLANNED                              | Outbound e-Fatura/e-Arşiv, assistance and care verticals, push notifications                                                                                                                    |

Sizes are relative (L = several weeks of one developer). Calendar dates are not promised;
milestones close when their exit criteria are verified by the integrator.

## M1 work packages (issued 2026-09-02)

| WP                                                                        | Title                                                                                                            | Depends on    | Parallel with      | Size | Owner                                  |
| ------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- | ------------- | ------------------ | ---- | -------------------------------------- |
| [WP-I1-01](../delegation/WP-I1-01-identity-oidc-bff-session.md)         | Identity: login, PostgreSQL sessions, CSRF, step-up                                                              | ports in repo | 02, 03, 04, 05, 06 | L    | Claude · **DONE**                      |
| [WP-I1-02](../delegation/WP-I1-02-authorization-tenant-context.md)      | Authorization: tenant context, permissions, /me, /tenants, switch-tenant, role templates, seed                   | ports in repo | 01, 03, 04, 05, 06 | L    | Claude · **DONE**                      |
| [WP-I1-03](../delegation/WP-I1-03-organizations.md)                     | Organizations: directory CRUD, VKN/TCKN validation, blind-index dedup, ETag, cursor paging                       | ports in repo | 01, 02, 04, 05, 06 | M    | Claude · **DONE**                      |
| [WP-I1-04](../delegation/WP-I1-04-platform-audit-outbox-idempotency.md) | Platform services: audit recorder, outbox dispatcher, idempotency middleware, rate limit, scheduler jobs, keygen | ports in repo | 01, 02, 03, 05, 06 | L    | Claude · **DONE**                      |
| [WP-I1-05](../delegation/WP-I1-05-frontend-foundation.md)               | Frontend foundation: pnpm workspace, three app shells, generated client, first screens with mocks                | OpenAPI only  | all                | L    | Claude · **DONE**                      |
| [WP-I1-06](../delegation/WP-I1-06-native-environment-ops.md)            | Native environment and ops: install/run scripts for MinIO, ClamAV, Mailpit; systemd units; runbooks              | none          | all                | M    | Claude · **DONE** (Ubuntu VM run open) |

Integration order once packages return: 04 → 01 → 02 → 03 → 05 (06 any time). The
integrator wires middlewares and routes in `cmd/api` and runs the full test suite before
closing M1.

## M2 work packages (issued 2026-09-03)

| WP                                                                | Title                                                                                            | Depends on           | Parallel with | Size | Owner             |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ | -------------------- | ------------- | ---- | ----------------- |
| [WP-I2-01](../delegation/WP-I2-01-persons.md)                   | Persons: registry, encrypted identifiers, blind-index search, relationships, sponsor memberships | M1                   | 02            | L    | Claude · **DONE** |
| [WP-I2-02](../delegation/WP-I2-02-programs-plans-enrollment.md) | Programs, plans, plan versions (maker-checker), entitlement definitions, enrollments             | M1, 01 (enrollments) | 01            | L    | Claude · **DONE** |
| [WP-I2-03](../delegation/WP-I2-03-entitlement-ledger.md)        | Entitlement accounts, ledger, reservations, adjustments, reconciliation                          | 02                   | 01            | L    | Claude · **DONE** |
| [WP-I2-04](../delegation/WP-I2-04-eligibility.md)               | Eligibility API with as-of resolution, explanations, evaluation snapshots                        | 01, 02, 03           | 05            | M    | Claude · **DONE** |
| [WP-I2-05](../delegation/WP-I2-05-member-import.md)             | Member import: staging, validation, matching, review, idempotent apply                           | 01, 02               | 04            | L    | Claude · **DONE** |
| [WP-I2-06](../delegation/WP-I2-06-frontend-people-plans.md)     | Backoffice screens: people, memberships, programs/plans, entitlements, eligibility, import       | contracts of 01-05   | all           | L    | Claude · **DONE** |

Integration order: 01 → 02 → 03 → 04 → 05 → 06 (06 starts on mocks as soon as each
contract lands). Migrations: 000014 (01, relationship versioning), 000015 (02), 000016 (03), 000017 (04), 000018 (05).

## M3 work packages (issued 2026-09-04)

| WP                                                                           | Title                                                                                       | Depends on         | Parallel with | Size | Owner             |
| ---------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- | ------------------ | ------------- | ---- | ----------------- |
| [WP-I3-01](../delegation/WP-I3-01-catalog-and-code-systems.md)             | Service catalog API, external code systems, service code mapping                            | M2                 | 02            | M    | Claude · **DONE** |
| [WP-I3-02](../delegation/WP-I3-02-provider-network.md)                     | Provider profiles, locations, capabilities, practitioners, provider search                  | 01                 | 01            | L    | Claude · **DONE** |
| [WP-I3-03](../delegation/WP-I3-03-contracts-and-prices.md)                 | Contracts, versions (maker-checker), price lists, packages, quotas, deterministic selection | 01, 02             | 04            | L    | Claude · **DONE** |
| [WP-I3-04](../delegation/WP-I3-04-rule-engine.md)                          | Rule sets, CEL rules, test cases, publish gate, immutable evaluations                       | M2, 01             | 03            | L    | Claude · **DONE** |
| [WP-I3-05](../delegation/WP-I3-05-pricing-quote.md)                        | Pricing quote composing eligibility, price selection, rules and balances                    | 01-04              | 06            | M    | Claude · **DONE** |
| [WP-I3-06](../delegation/WP-I3-06-frontend-catalog-providers-contracts.md) | Backoffice screens: catalog, providers, contracts, rules, quote                             | contracts of 01-05 | all           | L    | Claude            |

Integration order: 01 → 02 → 03 → 04 → 05 → 06 (06 starts on mocks as soon as each
contract lands). Migrations: 000019 (01), 000020 (02), 000021 (03), 000022 (04), 000023 (05).
ADR-023 fixes the rule expression language (CEL); ADR-022 was already taken by the identity decision.

## M5 work packages (issued 2026-09-05)

| WP                                                                                   | Title                                                                                        | Depends on                        | Parallel with | Size | Owner  |
| ------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------- | --------------------------------- | ------------- | ---- | ------ |
| [WP-I5-01](../delegation/WP-I5-01-health-case-encounter-visibility.md)             | Health case, encounter, diagnosis; the clinical/financial visibility split; sensitive access | M2, M3, M4                        | 05            | L    | Claude |
| [WP-I5-02](../delegation/WP-I5-02-medical-report.md)                               | Medical report: submit, medical review, approved scope, versions                             | 01, WP-I4-03, WP-I4-04            | 03            | M    | Claude |
| [WP-I5-03](../delegation/WP-I5-03-inpatient-preauthorization.md)                   | Inpatient stay: preauthorization, extension, segments, discharge reconciliation              | 01, WP-I4-01, WP-I4-02            | 02            | L    | Claude |
| [WP-I5-04](../delegation/WP-I5-04-health-claim.md)                                 | Health claim: versions, lines, auto-adjudication, medical and financial review               | 01, 02, 03, WP-I3-04/05, WP-I4-02 | 06            | L    | Claude |
| [WP-I5-05](../delegation/WP-I5-05-cross-cutting-mapping-notifications-contacts.md) | Service→entitlement mapping, check candidates, ICD-10, notification wiring, contacts, names  | M2, M3, M4                        | 01            | M    | Claude |
| [WP-I5-06](../delegation/WP-I5-06-frontend-health.md)                              | Screens: case, medical/financial review, stay, claim — and what HR never sees                | contracts of 01-05                | all           | L    | Claude |

## M7 work packages (issued 2026-09-07)

| WP                                                                               | Title                                                                                          | Depends on             | Parallel with | Size | Owner  |
| -------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------- | ---------------------- | ------------- | ---- | ------ |
| [WP-I7-01](../delegation/WP-I7-01-claims-across-verticals-and-adjustments.md)  | The lodging claim from the door, adjustments as ledger lines, the provider's earnings view     | WP-I5-04, WP-I6-03     | 02            | M    | Claude |
| [WP-I7-02](../delegation/WP-I7-02-invoice-manual-entry-and-allocation.md)      | The invoice as entered, allocation validated, immutable once submitted, corrected by supersede | 01, WP-I3-03, WP-I4-04 | 01, 05        | L    | Claude |
| [WP-I7-03](../delegation/WP-I7-03-batch-and-review.md)                         | The batch: immutable once submitted, decided invoice by invoice with reasons and adjustments   | 02, WP-I4-03           | 05            | L    | Claude |
| [WP-I7-04](../delegation/WP-I7-04-settlement-payment-records-reimbursement.md) | Settlement never above the approved total, payment records, the member's reimbursement         | 03, WP-I2-03, WP-I4-01 | 05            | L    | Claude |
| [WP-I7-05](../delegation/WP-I7-05-reconciliation-reports-and-exports.md)       | Provider statement, daily reconciliation, the dashboard, watermarked and audited exports       | 02, 03, 04             | 03, 04        | M    | Claude |
| [WP-I7-06](../delegation/WP-I7-06-frontend-billing.md)                         | Screens: earnings, invoice, batch, review, settlement, payments, reimbursement, statement      | contracts of 01-05     | all           | L    | Claude |

Order of work: 01 → 02 → 03 → 04 → 05 → 06. Migrations 000043–000047, assigned in landing
order (the M6 lesson: a lower number assigned later never runs on an upgraded database).

## M6 work packages (issued 2026-09-06)

| WP                                                                                 | Title                                                                                       | Depends on                 | Parallel with | Size | Owner  |
| ---------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- | -------------------------- | ------------- | ---- | ------ |
| [WP-I6-01](../delegation/WP-I6-01-property-room-inventory-availability.md)       | Property, room type, daily inventory with the balance CHECK, availability search, the quote | M2, M3, WP-I5-05           | 04            | M    | Claude |
| [WP-I6-02](../delegation/WP-I6-02-hold-and-booking.md)                           | Hold under the lock, expiry that releases, confirmation that reserves once, the voucher     | 01, WP-I2-03, WP-I4-01/02  | 04            | L    | Claude |
| [WP-I6-03](../delegation/WP-I6-03-cancel-noshow-checkin-checkout-waitlist.md)    | Cancellation by policy snapshot, no-show under review, check-in/out, waitlist               | 02, 04, WP-I4-02/03/04     | 05            | L    | Claude |
| [WP-I6-04](../delegation/WP-I6-04-lodging-terms-member-binding-notifications.md) | Lodging terms on the contract, the member's PERSON scope, tenant settings, booking messages | M1, M3, WP-I4-05, WP-I5-05 | 01, 02        | M    | Claude |
| [WP-I6-05](../delegation/WP-I6-05-frontend-lodging-member-pwa.md)                | Screens: the member PWA's first product, the property's desk, the payer's bookings          | contracts of 01-04         | all           | L    | Claude |

Order of work: 04 → 01 → 02 → 03 → 05 (the member binding and the terms first, so every
later package is built against the real scope and the real snapshot rather than a stub).

## M4 work packages (issued 2026-09-04)

| WP                                                                                 | Title                                                                                 | Depends on                            | Parallel with | Size | Owner         |
| ---------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- | ------------------------------------- | ------------- | ---- | ------------- |
| [WP-I4-01](../delegation/WP-I4-01-service-requests.md)                           | Service requests: versions, items, explicit transitions, the submit gate              | M2, M3                                | 04            | L    | **delivered** |
| [WP-I4-02](../delegation/WP-I4-02-authorization-and-fulfilment.md)               | Authorization reserving entitlement, fulfilment consuming it, vouchers                | 01, WP-I2-03                          | 03            | L    | **delivered** |
| [WP-I4-03](../delegation/WP-I4-03-workflow-and-worklist.md)                      | Work queues, work items, SLA snapshot and escalation, approval policy                 | 01                                    | 02            | L    | **delivered** |
| [WP-I4-04](../delegation/WP-I4-04-document-pipeline.md)                          | Documents: upload, quarantine, ClamAV scan, secure storage, legal hold                | M1 (native ClamAV and object storage) | 01            | L    | **delivered** |
| [WP-I4-05](../delegation/WP-I4-05-notifications.md)                              | Notification templates, messages, delivery, preferences                               | 01, WP-I1-04 outbox                   | 03            | M    | **delivered** |
| [WP-I4-06](../delegation/WP-I4-06-frontend-requests-worklist-provider-portal.md) | Screens: requests, worklist, documents, notifications — and the first provider portal | contracts of 01-05                    | all           | L    | **delivered** |

Integration order: 01 → 02 → 03 → 04 → 05 → 06 (06 starts on mocks as soon as each
contract lands). Migrations: 000025 (01), 000026 (02), 000027 (03), 000028 (04), 000029 (05).
The provider portal ships its first real screens here; `web/apps/provider` has been an
empty shell since M1.

## X1 work packages (issued 2026-09-12, cross-cutting)

| WP                                                          | Title                                                                                        | Depends on              | Parallel with | Size | Owner  |
| ----------------------------------------------------------- | -------------------------------------------------------------------------------------------- | ----------------------- | ------------- | ---- | ------ |
| [WP-X1-01](../delegation/WP-X1-01-in-product-help.md)     | In-product help: the unfamiliar explained where it stands, and every page says what it is for | the three shells, `ui`  | anything      | M    | Claude |

## M10 work packages (prepared 2026-09-13)

The owner approved the readiness corrections and M10 preparation. I10-02 tooling started;
milestone acceptance remains open. This is historical preparation, not the current task
queue: follow the [product completion roadmap](#current-product-completion-roadmap-2026-09-22)
first. M8/M9 remain deferred. If M10 is resumed, 01 needs its source contract, and 05 closes
only against a named pilot and completed evidence.

| WP | Scope | Status / external input |
| --- | --- | --- |
| [WP-I10-01](../delegation/WP-I10-01-imports-webhooks.md) | HR/policy import adapters and webhooks | PLANNED; source format and consumer to be named |
| [WP-I10-02](../delegation/WP-I10-02-load-performance.md) | k6 workloads, measured load and corrections | ACTIVE: harness and four-flow API smoke; import role corrected; live five-flow smoke and full-capacity evidence pending |
| [WP-I10-03](../delegation/WP-I10-03-operations-recovery.md) | Ubuntu deployment verification, backup and restore drill | DEFERRED by owner; finish working product first |
| [WP-I10-04](../delegation/WP-I10-04-security.md) | Security review, negative tests and finding closure | READY for local review; external test scope to be named |
| [WP-I10-05](../delegation/WP-I10-05-pilot-acceptance.md) | Pilot data reconciliation and signed UAT | PLANNED; pilot institution, program and users required |

### Readiness corrections delivered with this preparation

- The API probes PostgreSQL, both required object-store buckets and clamd. Dependency
  failure produces 503; recovery restores readiness. Liveness remains independent.
- The demo SPONSOR_HR grant matches the server and is compared with it in the mock tests.
- Added 94 Turkish problem messages: the six recorded in September 4's note plus 88 later
  literal response codes found by a Go AST-based coverage test. The latter keep the existing
  server wording except two implementation terms replaced with product-facing wording.
  The guard covers literal `Problem.Code` values and package-local problem-helper calls;
  dynamically computed domain codes still require their own mapping tests.
- The three sign-in screens place grouped demo accounts beside the form on desktop.

The older status-log entries below are historical. Their readiness, SPONSOR_HR and literal
problem-message gaps are closed by these changes; other recorded gaps are not implicitly closed.

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

- 2026-09-22 · Owner approved the product-completion sequence; detailed PC-01–PC-06 plan prepared:
  preserve verified import; eligibility/request/authorization; outpatient health;
  inpatient health; health invoice/batch/payment; accommodation and combined acceptance.
  Added task-level dependencies, 15 pending health acceptance scenarios, real role
  handoffs, confirmed source/fixture findings, evidence gates and scope exclusions to
  this roadmap. This was a planning/source-review update, not a new live health test.
  All six CI jobs on the preceding implementation head `fe89f12` passed; PR #11 remains
  draft/unmerged. Next execution task: PC-02.1 scenario prerequisites and PC-02.2 role parity.

- 2026-09-22 · Real member-import browser acceptance passed twice consecutively (15.8 s
  total) on the operator's single-door server with the real API and worker. Both runs
  signed in as admin.a in DEMO_A, completed password step-up, uploaded two synthetic
  rows, skipped the invalid row, applied one member, verified exactly one created and
  one skipped, found the member by name, signed out and proved a protected page returns
  to login. Test source record IDs are unique per run, matching the server's source-based
  update semantics. The test locators now match the search field and logout confirmation.
  All six CI jobs passed on the backend correction `da15390`. Startup warnings for
  unhandled `invoice.submitted` and `settlement.approved` remain for the financial-flow
  review. Next product flow: real eligibility and service-request submission.

- 2026-09-21 · Real browser import testing exposed a password-step-up retry failure.
  Upload now checks current authorization before the idempotency middleware, so challenges
  are not cached and cached successes cannot bypass step-up. Multipart hashes preserve
  file bytes, fields and headers while ignoring transport boundaries; changed-content
  reuse remains a conflict. The HTTP integration test now includes real idempotency,
  replay and expired-step-up coverage. The middleware upload ceiling matches the existing
  20 MiB file limit plus 8 MiB envelope allowance. Both affected Go test packages, vet,
  scoped lint and the new Playwright test's lint pass. Live browser retest awaits the
  operator restarting the API. Recovery and full-load work remain deferred.

- 2026-09-21 · Product completion, import access: owner approved `import.execute` on
  PROGRAM_MANAGER for existing/new tenants. Migration 000049 updates only existing system
  roles; the provisioning template and demo parity checks match. Authorization tests prove
  step-up, app and tenant boundaries and no grant to other roles. A PostgreSQL-backed HTTP
  test uploads/applies a member, executes worker redelivery without duplication and refuses
  duplicate upload. The earlier CSV test fixture now uses PRINCIPAL as the API requires.
  Local migration applied at schema 49. Live browser confirmation remains pending while
  API/web/dependencies are stopped; recovery and deployment preparation remain deferred.

- 2026-09-13 ? Owner reprioritization: stop recovery work and complete the working product
  first. I10-03 is deferred; do not request a recovery server now. Next work addresses
  observed real-API functional gaps in everyday user flows, beginning with role/access
  consistency and member/import operations, then service, booking and financial flows.
  Operations preparation and full-capacity benchmarking must not displace product completion.

- 2026-09-13 · I10-02 tooling started: pinned k6 2.2.0 installation, local synthetic fixture discovery, read/write/eligibility/hold/import workflows, one-iteration smoke and a bounded arrival-rate load profile. Four real-API workflows and final inventory/exact-balance reconciliation pass. Import staging requires a role decision: the real templates grant no `import.execute`, although the browser demo administrator has it. No grants were changed. Partial smoke explicitly lists omitted coverage; full load requires all five. Full-capacity provisioning, import apply/adapter coverage, server telemetry and measured load acceptance remain open. See `docs/runbooks/load-testing.md` and WP-I10-02.

- 2026-09-12 · WP-X1-01 delivered: in-product help, built under Impeccable. Two shapes in the shared `ui` package: `HelpHint`, a circled question drawn in the icon stroke that opens a 20rem card with a term's title and one to three sentences — on press, Enter, Space or a mouse hover, never a touch hover; a hover-opened card leaves focus where it was — and `HelpDrawer`, opened by a "?" in every app header, a drawer from the right (full width below 640px) that says what the page is for and lists its sections, actions and statuses in page order. The words are data: a `help` i18n namespace with one file per app plus the shared terms (45 terms; 90 pages — 59 backoffice in two files, 23 provider, 8 member), keyed by route through each app's `help.ts`; a test in each app fails when a route has no page written, and `HelpHint` renders nothing for a term nobody has written. 37 marks placed across the apps (12 backoffice operations, 10 backoffice setup, 10 provider, 5 member) on terms, server-computed figures and fields whose consequence the label does not carry; none on a control that names its own action. `FormField` gained `help`, beside the label and never inside it. Content written by five Opus agents in parallel from the pages and DESIGN.md's vocabulary; three terms had no on-screen noun and were named (Parola doğrulaması, Dört göz kuralı, Oda tutma). Found on the way: jsdom's selector engine (nwsapi 2.2.27) answers `:modal` by asking itself — forty million `matches()` calls per open popover, twenty seconds per test — so the shared test setup answers the top-layer selectors first (62s → 4s) and polyfills `PointerEvent`; the request detail rendered a missing `eligibility.outcomes.NOT_ELIGIBLE` key for an ineligible request (`INELIGIBLE`); the adjustment queue's native `title` tooltip became a mark. Two capture specs for the review (backoffice worklist drawer and mark; member home drawer and mark at 390). Open: the finish review's verdict on captures; a theme button for the provider and member apps; tenant-specific wording of the help.
- 2026-09-10 · WP-I7-05 delivered: the provider statement, the daily reconciliation, the dashboard and the exports (migration 000047: `billing.reconciliation_run` append-only with its arithmetic as CHECKs, a settlement RECONCILED only when `paid_amount = payable_amount`, the `report` schema with `report.export` and `report.parameters_are_anonymous(jsonb)` refusing an identifier in the filters). The statement's totals are the database's to the kuruş and a provider reads only its own; the reconciliation runs one TENANT run as the sum of the PROVIDER runs, marks what is paid, raises one work item per difference and inserts rather than updates on a second sweep; the dashboard's counts equal the lists they link to and are bounded by the caller's provider scope; an export is requested with `report.export` (CLAIMS also needs `report.export.sensitive`), rendered by the worker as CSV with the watermark — tenant, person, moment, export id — on the sheet header and on every row (formulas defused), stored through WP-I4-04 as an EXPORT document with the tenant's TTL (`report.export_ttl_hours`, default 24), and **the download is the access that is audited**: every download counts and writes an `audit.access_event`, a download after the TTL is refused with `EXPORT_EXPIRED` and audited as a denial, an hourly sweep deletes the file and marks the row. New ports: `objectstore.Store.Write` and `document.StoreRendered`/`PurgeObject` (the first platform-produced file). Permissions in both places: `report.export` (PROGRAM_MANAGER, FINANCIAL_REVIEWER, PAYER_APPROVER, AUDITOR), `report.export.sensitive` (FINANCIAL_REVIEWER, AUDITOR); PROVIDER_BILLING gained `report.read` for its own statement. Verified by mutation: the sensitive grant skipped, the expiry ignored on download, the watermark dropped from the rows, the statement's provider scope removed, the download audit row dropped — all red. Accepted: CSV only (XLSX refused with a field error, no spreadsheet dependency); the reconciliation runs on the daily slot rather than 02:00 tenant time (the scheduler has intervals, not cron); the expiry sweep is hourly so a short TTL holds; `PAID_SUM_MISMATCH` is a sweep-level safety net WP-I7-04's trigger makes unreachable in a test.
- 2026-09-11 · M7 closed: six packages, migrations 000043–000047, schema version 47. The rules the milestone was judged by hold in the API, in the mock and on every screen: invoice allocations are validated against the invoice and the claims (the allocation difference is the server's and a mismatch is refused); a submitted batch's invoices are never changed in place (a trigger freezes membership and amounts); every cut, return, rejection and approval is visible with its reason on both sides and audited; a settlement never exceeds its batch's approved total (a CHECK); exports are permissioned, watermarked on every row, time-limited and audited per download. The claim is the one billable unit across verticals, and the member's reimbursement consumes the wallet only on approval with no bank secret stored. The ERP posting and the e-document stay behind nullable ports for M8 and M9. Next: M8/M9 remain deferred to the end by the owner; the next increment is chosen with them.
- 2026-09-11 · WP-I7-06 (2/2) delivered, closing the package: the screens on WP-I7-05's contract. Provider: Cari ekstre (the period's totals as the server keeps them, the invoices with batch, decision and settlement, the settlements with the open amount) and "Dışa aktar", which queues the statement, shows its state and downloads it with the watermark shown. Backoffice: the section strip gained Günlük mutabakat (runs by day and scope, one run's figures and each difference by settlement reference with its kind) and Dışa aktarım (the request form showing a period only for the kinds that carry one, every export with what it covers, polling while it renders, and a download that asks for its purpose before the link is minted); the operations dashboard sits on Ana sayfa as figures rows; the icmal review's decision form lists the invoice's claims, each opening the claim with the reader's projection. Decisions: PROVIDER_BILLING now holds `report.export` in both places, reversing WP-I7-05's choice to withhold it — the spec (§2.1.4) asks for the statement's export, the service already scopes a provider to its own rows with tests on both sides, and the file is watermarked and audited per download; `report.export.sensitive` stays off. A dashboard figure links to a list only when that list reproduces it exactly (a single claim status; work items past SLA to the worklist's overdue view): the list endpoints take one status and no date window, so multi-status and windowed figures are plain text. Found on the way: the mock dashboard valued every claim at zero (a hard-coded '0' where the server sums `billing.claim_approved_total`) — fixed, with a test proved red against the old mock and green after. Tests gained the settlement above the checker threshold (step-up, then the second-person refusal named), a payment beyond the remainder refused with the figures unchanged, the IBAN absent from browser storage, the provider's statement export, and the download's purpose dialog. Finish review under Impeccable: sixteen fixes and two scoped items in the first pass, then four verdict passes that closed the tab strip at 390, the dashboard's column alignment, the download purpose and the captures themselves; one was a real defect — the download dialog lived in a row and closed when the table became a list below 768 — and now lives on the page; disposition ship (scoped: the home page's navigation card wall, no UI Checkbox component). DESIGN.md gained this half's patterns: a tab strip is one line at every width, a dashboard is figures rows, a figure links only to a list that reproduces it, an export is a state then a download, an audited download asks why, an undecided amount reads —. Open: the list endpoints should take `status` as a list and a due window so every dashboard figure can link; a settlement does not name the reconciliation run that closed it; the reimbursement review does not show the eligibility answer; the person's tabs do not yet list the invoices their claims sit on; the member's submit is proved in unit tests only (the browser mock never finishes a scan); the home page's navigation card wall predates M7; there is no UI Checkbox component.
- 2026-09-10 · WP-I7-06 (1/2) delivered: the M7 screens for the contracts of 01–04, built under Impeccable. Provider: Hakediş per currency with the invoiceable figure the invoice is checked against, the invoice as the provider enters it (header, image, the allocation editor with the server's total and difference beside the payable amount, Gönder refused while they differ, Düzelt opening the correction and the chain), the icmal built from submitted invoices and sent with a step-up. Backoffice: the section strip İcmal incelemesi · Ödeme mutabakatları · Geri ödeme incelemesi; the icmal decided invoice by invoice (the decision form names its invoice, a cut needs a reason from the tenant's list) with the server's totals line and a close that is absent until every invoice is answered; the settlement as figures, recoveries and sequence, approved with a step-up, cancelled with a reason, paid by external reference with the remaining amount the server's; the reimbursement with four characters of the account and never more, decided in full, in part or rejected, and paid; the person's Geri ödemeler tab. Member: Geri ödemelerim, a three-step request filling one receipt (talep → makbuz taranana kadar bekle → IBAN typed once, shown as `Hesap •••• 1326` from then on), Gönder on a draft only once the receipt is clean. One status→tone map for every app (`@kapsora/api-client`); MEMBER gained `catalog.read`, `provider.read`, `document.link` in both places; the mock gained `GET /me/person`. Finish review: 8 fixes, then 5 follow-ups and 1 craft point over two verdict passes, all resolved, disposition ship (scoped: the sliced claim id on a settlement's recoveries, raw enum codes in the M4 documents panel, no reimbursement tab in the member bar). The documenter found that `border-border`, `bg-surface-muted` and `bg-*-subtle` were never theme tokens (the theme has `--color-line`, `--color-surface-sunken`, `--color-*-soft`), so every card since M6 drew a currentColor border — renamed in 30 files; DESIGN.md +8 patterns, `.impeccable/design.json` sidecar. Open on the wire: `Reimbursement` carries no `providerName`/`serviceName` (the batch does) and `SettlementRecovery` no `claimReference`, so screens without the catalogue or organization grant show `—`. Next (2/2): the provider statement, the reconciliation, the dashboard and the exports screens on WP-I7-05's contract.
- 2026-09-10 · WP-I7-04 delivered: settlement, payment records and the member's reimbursement (migration 000046). A settlement opens from `batch.decided` by an idempotent handler (one live settlement per batch by partial unique index), nets open RECOVERY adjustments whole and oldest first, takes the due date from the contract's payment term (a contract without one dead-letters the event for replay), and keeps `payable = approved − withheld ≤ approved` as a CHECK with the approved amount frozen after DRAFT; approval asks a step-up and, above the tenant's checker threshold, refuses the batch's decider; a wrong settlement is cancelled and versioned. Payment records are append-only with a unique external reference per provider, and a deferred trigger keeps their sum equal to `paid_amount` and within the payable amount, the status following the sum; `settlement.record_payment` in both places. The member's reimbursement wraps a REIMBURSEMENT request with the IBAN through the platform cipher and a four-character mask — a schema-wide sweep proves the plaintext exists nowhere — refuses duplicates by receipt or provider-date-amount naming the earlier one, the contract's ceiling, an unscanned receipt and a service date outside the enrollment; approval creates the REIMBURSEMENT claim already decided and consumes exactly the approved amount from the member's MONEY entitlement, then orders the payment through a declared port (no-op until M9). Four notifications with `masked_account` joining the safe-variable catalogue. Verified by mutation: the decider approving above the threshold, step-up skipped, no recovery netted, the ceiling ignored, duplicates accepted — all red. Accepted: `cancelSettlement` as a twelfth operation; a reimbursement needs a known provider at draft time; `checked_by` records the batch's decider the approval countersigns; the ceiling is the contract's price-item maximum.
- 2026-09-10 · WP-I7-03 delivered: the batch (migration 000045) — built by the provider from submitted invoices of one payer, domain and currency (a mixture refused by field), within the tenant's min/max, submitted with a step-up above the threshold; membership and amounts frozen by trigger, one live batch per invoice; the payer's finance decides invoice by invoice — APPROVE, CUT (WP-I7-01 CUT adjustments spread across the invoice's claims in proportion to their allocations, exact, the remainder on the largest, recorded in `billing.batch_invoice_adjustment` so a changed decision reverses rather than edits), RETURN (the invoice RETURNED, its claims released for the correction), REJECT (claims CLOSED_UNPAID); `decideBatch` refuses the submitter and, above the threshold, the last reviewer, recomputes the totals (approved + cut + returned + rejected = submitted, a CHECK), completes the finance work item and publishes `batch.decided`; `batch.submitted`/`batch.decided` notifications. Verified by mutation: the remainder dropped or misplaced, a decision edited instead of reversed, the submitter deciding, the second-person rule, a mixed currency, frozen membership (agent); RETURN not releasing, REJECT not closing, size limits ignored, an undecided invoice not blocking (mine) — all red. Found on the way: WP-I7-02's audit detail keys were camelCase and `audit.SanitizeDetail` dropped them silently; renamed to snake_case. oasdiff reports 20 ERR, all from `CLOSED_UNPAID` joining the `ClaimStatus` enum (as `property_name` did); the batch surface itself is additive-clean. Open: `batch.submitted`/`batch.decided` have no outbox handler until WP-I7-04.
- 2026-09-09 · WP-I7-02 delivered: the invoice as the provider enters it (migration 000044, the `billing` schema): the number unique per provider and fiscal year, the header arithmetic and the allocation ceiling the database's (deferred trigger), the links and amounts frozen once submitted, the claims moved to INVOICED and back on cancel; the submit gate is the tenant's tolerance and, when the tenant asks, the invoice image; a returned invoice is corrected by a new one in a supersede chain and its number is free only to that correction; the sponsor's HR reads the invoice with no claim line description; `invoice.submitted` on the outbox. `invoiceableTotal` now keys off the link table. Verified by mutation: a negative difference ignored by the tolerance, a provider without a tax id, the image gate, the fiscal year — all red; a correction taking another returned invoice's number survived and gained its test (two returned invoices in the fixture; one alone could not tell the rules apart). Open: the mock's SPONSOR_HR permission list lacks `accommodation.property.read` (drift since 000040); `returnInvoice` is WP-I7-03's and releases the claims through `ReleaseFromInvoice`.
- 2026-09-08 · WP-I7-01 delivered: the claim is the one billable unit across verticals (migration 000043: `source_type`/`source_id` with a per-type existence trigger, one live claim per booking). A check-out produces one claim with one line per night slept at the booking's frozen night amount, the plan-carried nights decided at their payer share and the rest at zero, landing in PENDING_FINANCIAL; a confirmed no-show and a penalised cancellation produce a one-line fee claim, a free cancellation nothing — all through outbox events (`booking.checked_out`, `booking.no_show_confirmed`, `booking.cancelled` carrying `free`), idempotent under redelivery. Adjustments carry a payer/member split the database checks, a source and a line, are undone only by a REVERSAL row, and the approved total is lines minus adjustments; `createClaimAdjustment` is the financial reviewer's. The provider's earnings view answers the invoiceable total per currency, bounded by the decision moment. Deviations accepted: WP-I5-04's CUT row beside a line decision removed (it double-counted); no zero-value link rows in the ledger — a fee claim's provenance is its source. Verified by mutation: unit amount for the payer share, every night treated as covered, a reversal of a reversal, a foreign-currency adjustment (the last was untested and gained its test). Open: `invoiceableTotal` keys off status until WP-I7-02's invoice links exist.
- 2026-09-07 · M7 opened: six packages, migrations 000043–000047. The milestone is judged by v1.2 Faz 8: invoice allocations validated against the invoice and the claims; a submitted batch's invoices never changed in place; every cut, return, reject and approve visible with its reason and audited; settlement never above the batch's approved total; exports permissioned, watermarked, time-limited and audited. The claim becomes the one billable unit across verticals (a completed stay, a confirmed no-show and a penalised cancellation are claims), and the member's reimbursement consumes the wallet only on approval with no bank secret stored. The ERP posting and the e-document arrive through ports left nullable for M8 and M9.
- 2026-09-07 · M6 closed: five packages, migrations 000039–000042, schema version 42. The rules the milestone was judged by hold in the API, in the mock and on every screen: 500 concurrent holds on three rooms leave exactly three (the inventory CHECK under the lock in date order); an expired hold gives back the room and the entitlement without a call; every cancellation and no-show is judged by the policy snapshot frozen on the booking; and the member sees the exact amount they will pay before, during and after confirming, as the server's string. Next: M7, claims, invoices, batches and settlement.
- 2026-09-07 · WP-I6-05 delivered: the member PWA is a product — home with the remaining entitlements first (name · leader · numeral · unit, the wallet's period under the name), search with the rooms' nights and "Ödeyeceğiniz" as the server's string, the receipt that fills in and stays pinned with the one action on it, the hold with the server's countdown and Onayla carrying the amount, the voucher shown once and stored nowhere, the cancellation's cost answered before İptal et is offered, the bottom tab bar; the property desk's allotment a night at a time (refused below the commitment with the server's sentence), arrivals checked in by token, departures checked out, the no-show reported with evidence; the payer's bookings as a sequence, the no-show reviewed by a second person, the waiting list in queue order, the properties, the lodging terms on a draft version only, the person's Konaklama tab. Concept locked by the owner through the structured question (receipt · remaining first · tab bar); DESIGN.md gained thirteen settled patterns; the Impeccable finish review scored all eight material fixes and the three regressions of the first batch resolved, and the last scoped item (the leader on a wrapped wallet note) resolved in a third pass. Two seed defects fixed on the way: every seeded booking read 0,00 because a night count went through the micro-unit multiply, and the desk role lacked the document permissions a no-show report needs (roles.go and the mock). Open: the api-client's `CoverageLine` still counts covered nights from the quote's nightly amounts on the search page while the booking page reads `coveredNights`; the search result should carry `coveredNights` too. Playwright gained a member project (390 viewport); captures at 390 and 1440 under `.impeccable/review/`.
- 2026-09-07 · WP-I6-03 delivered: cancellation, no_show and waitlist_entry (migration 000042), every cancellation judged by the policy snapshot frozen on the booking — proved by changing the contract's terms after confirmation — with the penalty consumed before the remainder is released so the ledger agrees with the fee; check-in by the voucher token inside the tenant's window in the property's zone (a rotated token is 409 VOUCHER_REVOKED, a stranger's 404); check-out consuming the nights slept and releasing the rest across the booking's authorization, an over-stay flagged and never consuming beyond the cover; no-show reported by the property with evidence and confirmed only by a second person (the database refuses the reporter as reviewer), nights rounded up; the waitlist offered in queue order by a five-minute sweep that places a real hold, an unaccepted offer returning to the back of the queue. `accommodation.waitlist.manage` in both places. Migration 000042 also splits 000041's confirmed_at rule so a cancelled booking keeps the moment it was confirmed. The mock world binds `member.a` to a person (PERSON grant, `/me` carries personId, every member operation resolves the person from the session and refuses PERSON_SCOPE). Verified by mutation: free window measured after arrival, penalty never consumed, check-in window never closing, expired offer never requeued — all red; the no-show rounding survived and gained its own unit test. Open: the offer sweep runs on the schedule only, not on every release.
- 2026-09-07 · WP-I6-02 delivered: booking, booking_night and booking_guest (migration 000041), the hold under `FOR UPDATE` in stay_date order — 500 concurrent holds on capacity 3 leave exactly three, 497 `ROOM_UNAVAILABLE`, no deadlock, and swapping the order for `random()` produces 64 deadlocks in the same test — the expiry sweep that gives back the room and the reservation once, confirmation through a RESERVATION request whose authorization adopts the hold's reservation (new `AdoptReservation` on the ledger port: nothing is reserved twice), the voucher minted on demand and returned once (never parked anywhere: the approval arrives through the outbox after the confirmation response, so the pickup is `POST /bookings/{id}/voucher`, outside the idempotency middleware), the stale-quote refusal, the step-up threshold, `booking.*` notifications and the reminder sweep in each property's zone. Partial cover is one rule across 01 and 02: the quote counts the nights the plan carries, the snapshot freezes `coveredNights`, and the hold, the request line and the authorization all take exactly that many; a stay the plan carries no night of is refused. Two defects outside the package were found by its tests and fixed: WP-I4-01's gate never decided the lines of a request it approved outright (every auto-approved request reached WP-I4-02 with nothing approved), and the gate re-checked eligibility against a balance the hold had already drawn (`AlreadyHeld` on the resolver's item, supplied only from the booking's own record). Verified by mutation: stale quote accepted, second reservation at confirmation, step-up equality, give-back forgetting inventory, person boundary removed (first survived — the member boundary was untested; now a second member reads, lists, releases and confirms 404), gate ignoring the hold. Open: the pending-approval hold is a 72-hour constant because no request-level SLA exists yet; an expired hold sends no message.
- 2026-09-06 · WP-I6-01 delivered: the accommodation schema (migration 000040; the packages had assigned 000036, renumbered because 000039 had already landed and golang-migrate applies only numbers above the current version — M6 now runs 000040–000042) and the ten operations over it. Three tables and two sentences: `accommodation.inventory_day` is the only place a room is counted, and `held + confirmed <= capacity` is a CHECK the database keeps — proved with the application layer bypassed, so WP-I6-02's `FOR UPDATE` will be the second lock rather than the only one. A provider opens a season in one call and a capacity below what is already promised is refused with the first offending date as an RFC 9457 extension member, the whole range with it. The availability search answers the server's minimum over the range — a room type without an allotment on even one night is not available, whatever its minimum says — and the contribution quote goes through WP-I3-05's own calculator per night and is summed once, so `payer + member` is exactly the total on every room type and every night. The plan's `NIGHT` entitlement limits how many nights it carries and not how much money: a member with two nights left over a three-night stay is shown the third as their own. A night nobody can price carries `quote: null` and a reason code rather than a guess, and a tie is `PRICE_AMBIGUOUS`. Every search is an immutable eligibility evaluation. A member searching for themself is resolved through WP-I6-04's PERSON scope and a body naming anybody else is refused; a desk names the person. `accommodation.property.read` in both places with the two-halves test. Nights are counted on the calendar in the property's own zone, which a Europe/Berlin daylight-saving test pins.
- 2026-09-06 · M6 opened: five packages, migrations 000039–000042 (000039 for WP-I6-04, which lands first; 000040–000042 for 01–03). The milestone is judged by v1.2 Faz 7: no oversell under 500 concurrent holds (the `inventory_day` CHECK plus `FOR UPDATE` in date order), hold expiry that releases the room and the entitlement without a call, a policy snapshot frozen on the booking that judges every cancellation and no-show, and the member seeing the net amount before confirming. Two platform gaps are closed on the way: a member account bound to a person (`PERSON` grant scope) and lodging terms on the contract version.
- 2026-09-06 · M5 closed: six packages, migrations 000031-000035, schema version 35. The rule the milestone was judged by holds in the API, in the mock and on every screen — a sponsor's HR user cannot see a diagnosis, proved by a DOM scan against every clinical string the world seeds — and a decided version of a report or a claim is never edited. Next: M6, the accommodation vertical.
- 2026-09-06 · WP-I5-06 delivered, closing M5: the health vertical's screens, built under Impeccable. Two structural decisions were put to the owner and locked: in the provider portal the case is the spine — one new rail entry, Vakalar, and the report, the admission and the claim hang off the case as its chapters, with Claim'ler as the billing desk's own list; in the backoffice one `/claims/:id` page whose sections are decided by the projection the server sent, never by hiding columns. Provider: the case as a story (encounters, the ICD-10 diagnosis chosen by name and code with its sensitivity said the moment it is chosen, documents), the report drafted and edited in place, the stay with segments, Uzat only while nothing is undecided, discharge with the server's exact figures, and the claim with prices withheld until the server prices them, per-line decisions, Düzelt opening version 2 under a read-only version 1, and invoice readiness with named blockers. Backoffice: the medical reviewer's claim (diagnosis, description, a clinical yes or no per line and never a figure), the financial reviewer's claim (prices, contract amounts, member share, the duplicate's reference, cut), the purpose prompt as a real dialog with a real decline path, the report review, the sponsor HR view of a person with no place a diagnosis could go, and the access log. Turkish complete under `health.*`, `claims.*`, `review.*`.
- 2026-09-06 · Two things the screens needed that the platform did not have, both closed in the same commit. The decline path: a reviewer who holds the sensitive grant could only be asked for a purpose (428) — there was no way to say "the financial half only". `X-Access-Projection: FINANCIAL` is now an optional header on every operation that takes a purpose; it answers the financial projection, needs no purpose and writes nothing to the access log, because choosing not to look is not a look. And the client layer for the four M5 contracts, with `AccessContext` carrying purpose, reason and projection.
- 2026-09-06 · The finish review found eight material things over the first captures and every one was real: HR's table promised amounts it never showed; the narrowed case kept a diagnosis column of dashes; below `md` the decision sat inside a horizontal scroller; the save button shipped disabled with nothing saying why; the four money inputs had no visible labels; the correction's two versions were crushed side by side with different column orders; an exception's detail was a bare value; and the world seeded no sensitive claim and no access-log row, so two of the four backoffice deliverables could only be unit-tested. All eight fixed in one batch and recaptured; a `useMinWidth` hook renders one structure per viewport rather than two with one hidden. The verdict pass scored seven resolved and one partial and found three small regressions of its own (a total that contradicted its badge, a subtitle that contradicted its banner, an unlabelled count); a second pass scored all of them resolved and ended at ship — at its scope, the fix list, not the whole surface. Four clauses no capture can show stay proved by tests alone: the discharge reconciliation, Uzat while an extension is undecided, invoice readiness with blockers, and the decision on an undecided report. DESIGN.md gained eight patterns from the built world, written by the documenter and checked against the source.
- 2026-09-06 · Open, found by the screens and written down: the claim, the case and the report carry no display names and no totals on the wire, so lists resolve the person one read at a time and HR's approved total is one readiness read per decided claim; an exception's detail names the other claim by reference, not id, so the duplicate cannot be opened from the panel; the access log names the actor by id because the event carries no name; a claim cannot be opened without a case, because a provider may list neither enrollments nor programs and the check's answer carries no program; the report's submit is refused until its file scans clean and the browser mock cannot advance a scan, so the e2e proves the refusal rather than the submission.
- 2026-09-06 · WP-I5-04 delivered, closing the M5 backend: the health claim. Migration 000034 gives the claim schema five tables, two of them append-only, and puts the arithmetic in the database rather than in a comment: `payer_amount + member_amount = approved_amount` is a CHECK, an AUTO decision is exactly the one with no decider, and a rejected line approves nothing. Submit prices every line through the existing ladder, evaluates the tenant's published rule set, then runs the cross-checks that are not rules — an authorization consumed, a report's coverage, a stay that ran over, a duplicate — and routes: medical review before financial when both are needed, straight to approved when neither is. A correction is a new version and the decided one is frozen; the old decisions cannot even be deleted, because the trigger refuses it. The two reviewers are served different fields rather than the same fields with some hidden: the description, the diagnosis, the report reference and a medical reason live in the clinical projection alone, and sponsor HR sees none of them. `claim.decided` now has the publisher WP-I5-05 left it.
- 2026-09-06 · Verified by defeating it: the first decision winning instead of the latest, the financial projection keeping the diagnosis reference, an undecided line no longer blocking the invoice, a member share stored with its fraction cut off, and a return quietly deleting the superseded version's decisions. Three were caught by tests, one by the database CHECK and one by the append-only trigger — which is where those two belong. The undecided-line blocker was caught by nothing: it is the one of section 2.4's three blockers no command can produce, since approving decides every line, so it now has a test that writes the row the way a repair script would and proves the blocker fires.
- 2026-09-06 · The schema tests crossed Go's default ten-minute limit for the first time (951s: every test provisions a database and replays all thirty-five migrations). Both test steps in CI and both Makefile targets now name `-timeout 30m`. That is a stay of execution rather than a fix — the honest answer is a template database or parallel provisioning, and it belongs to whichever milestone next finds itself waiting on this. Also open: one vitest run out of twelve failed while the Go suite was saturating the machine and the run was not captured; seven consecutive clean runs followed and CI is green, so it is recorded rather than diagnosed.
- 2026-09-05 · WP-I5-03 delivered: the inpatient stay. Migration 000033 puts the two rules that matter in the database rather than only in the service — a partial unique index allows one open stay per case and provider, another allows one undecided extension per stay, and an exclusion constraint refuses overlapping segments while letting a companion overlap the patient by definition. Twenty concurrent creates leave exactly one stay. The admission is a PREAUTHORIZATION request like any other, and this package learns the reviewer's answer from the outbox event `service_request.decided` rather than from a call, so a reviewer deciding an admission on the request page never learns that a stay exists. Discharge counts a started day as a day, releases what was promised and not spent, and flags a stay that ran over rather than refusing the discharge, because the person has already had the days.
- 2026-09-05 · Two things the delivery's own tests could not have caught, found by mutating them and fixed here. First, `TestStayFinancialProjectionCarriesEveryFigureAndNoClinicalWord` asserted that the admission diagnosis is absent from the financial projection over a fixture that never set one: nil was nil, and the assertion passed with the clearing removed. The fixture now admits a real diagnosis, and the test fails without the clearing. Second, nothing covered the day count itself — every fixture discharged on a whole number of days, where ceiling and floor agree — so the one line that decides what a hospital is paid was proved by nothing; there is a domain test for the partial days and the window's two ends now.
- 2026-09-05 · And one real defect, found the same way. An approved extension reserves its added days on an authorization of its own, so a stay stands on more than one hold, but discharge released only the stay's first hold: authorised five, extended by three, discharged after two, and the reconciliation gave back five of the six unused days while the extension kept the sixth reserved against a bed nobody was in. No aggregate assertion would ever have noticed, because the account's totals are right whichever hold the days come out of. The release now walks every hold the stay stands on, oldest first, and the test asserts the outstanding reservation hold by hold as well as in total.
- 2026-09-05 · WP-I5-02 delivered: the treatment report. Migration 000032 gives a report a chain rather than a history — `root_report_id` is its own id for version 1, a correction names what it supersedes, and a partial unique index allows the chain exactly one APPROVED version. That index and the package's own rule (a decided version is never edited, and the old one stays exactly as decided) cannot both hold with the seven statuses the spec listed, so approving a correction moves the earlier version to SUPERSEDED and changes nothing else about it: reviewer, moment, comment, summary, service lines and usage rows are all still there, and the test asserts them column by column. Submit refuses a report with no service line or no clean document of its type, a coverage call is the only way a claim may lean on a report and writes the usage row that proves which version it leaned on, and the clinical half — the summary, the review comment, the type and subtype, the linked documents — is the same projection WP-I5-01 decided, from the same function rather than a copy. A medical reviewer who claims the work item has started the review, through a hook that runs inside the claim's own transaction and does nothing for a caller without the review grant; neither module imports the other. `medical_report.decided` now has the publisher WP-I5-05 left it, carrying four safe variables and no clinical word.
- 2026-09-05 · Verified by defeating it: coverage that ignores the service line, a usable coverage that writes no usage row, a financial projection that keeps the report type, superseding that also wipes the old version's review comment, and the mock's freeze removed — five mutations, five distinct failures, each naming the thing it protects. Worth recording from the delivery: `.gitignore` carries `coverage.*`, which silently excluded a source file named `coverage.go`; it is `reportcoverage.go` now.
- 2026-09-05 · The smoke run found a defect the unit tests could not: the document panel's type field was a list of the types still _missing_, and what is missing is only known once the linked documents arrive. On a request whose named types were already attached the field was a dropdown for one frame and a text box the next — a control changing what it is under the cursor. It now follows the request's own required types, which are loaded before the panel renders, in both the provider and the backoffice copy. The regression test is deterministic rather than timed: it reads the control's tag before the documents query settles and after, and fails with INPUT instead of SELECT on the old rule. DESIGN.md gained the pattern.
- 2026-09-05 · WP-I5-05 delivered, closing five of the six things M4's screens found. Migration 000035: `benefit.service_entitlement_mapping` (exact `unit_factor`, unique per plan version and service, a composite FK that forbids naming another version's definition, and a guard trigger that refuses every write unless the plan version is DRAFT — the rule is in the database because an application service that forgets it would leave no trace) and `party.person_contact` (`value_enc` in the same envelope as a TCKN, a masked column, one primary per person and channel; the value is returned by no endpoint at all). The resolver reads the mapping before the context hint and multiplies the requested quantity by the exact factor, so a mapped service now answers Uygun instead of SERVICE_MAPPING_PENDING; `ENROLLMENT_MULTIPLE` names its candidates and the check accepts the chosen one, which bumped the canonical request hash to version 2 because a question that names a plan is a different question. Notifications actually go out: six events wired from the decide, return, reject and gate commands, authorization approval and a new hourly expiry sweep, every message deduped by what happened rather than by a clock, with templates seeded in Turkish for EMAIL and INAPP from the ten-name safe-variable catalogue and the refusal test extended to each. ICD-10 is seeded as a code system with its chapters and about sixty real codes, sensitivity carried on the leaf where WP-I5-01 reads it. `ServiceRequest` and `WorkItem` carry the names their screens needed, so the worklist names the colleague who won a claim instead of showing a UUID, and the 409 carries the winner as RFC 9457 extension members — `httpx.Problem` serialises byte for byte as before when it has none. Three permissions in both places with the two-halves test.
- 2026-09-05 · Verified by defeating it rather than by reading it. Four Go mutations, each caught by exactly one test: the resolver ignoring the unit factor, the contact mask returning the address, the request response dropping the person's name, and a second publish per recipient. Three more against the mock and the screens: the mock ignoring the mapping, contacts behind `member.read` instead of `member.contact.read`, and the list cell falling back to a per-row read. The API process is wired with no field cipher on purpose and cannot read a contact; the address is resolved when the worker materialises the message, which is the only process that holds the key. Open and honest: `seed demo` builds tenants and logins only, so the mapping seed has no plan version to attach to until a demo plan world exists — the mock world carries the three mappings that make the screens say Uygun today; `medical_report.decided` and `claim.decided` have templates and recipients but no publisher until WP-I5-02/04; and the half of the rollback test that proves a rolled-back transaction leaves no message rests on the publisher taking the caller's transaction, which no mutation here proves.
- 2026-09-05 · WP-I5-01 delivered: the health case, its encounters and diagnoses, and the visibility split the milestone is judged by. Nine operations; migration 000031 (`health.health_case`, `encounter`, `diagnosis`, `clinical_access_purpose` with six seeded purposes, `health.sensitive.read` as a SENSITIVE permission, `SPONSOR_HR` as a role — both places, two-halves test). The rule that carries it: a case has no settable sensitivity; it is derived from the categories its diagnoses belong to (`catalog.code_value.attributes.sensitive`) and recomputed on every diagnosis write, both ways. Which half of a row a caller sees is decided in the application service from what the caller holds, not from the row: `health.case.read` alone is the financial projection; `health.clinical.read` opens the clinical one on a STANDARD case; a SENSITIVE case narrows a clinical reader without `health.sensitive.read` back to the financial projection rather than refusing, because a refusal would itself say the case is sensitive; a reader with the grant must state a purpose from the seeded list (`X-Access-Purpose`, `X-Access-Reason` percent-encoded) or gets 428, and every clinical read and every refusal is one row in the access log, by person. Verified by defeating it: with both projection functions neutered, `TestSponsorHRCannotSeeADiagnosis` fails on the first clinical note in a list body; the DENIED assertion counts rows (one per refusal), not presence. The mock (`health-handlers.ts`, 18 tests) is the same server, including the narrowing and the 428. Open: ICD-10 itself is WP-I5-05, so the world today marks sensitivity through a seeded attribute on a handful of code values, not the chapter list.
- 2026-09-05 · M5 opened: six work packages WP-I5-01..06 written from plan v2.0 I5, v1.2 9.12, 10.3–10.5, 11.10, 12.4–12.5 and the Phase 6 acceptance criteria; migration numbers 000031–000035 assigned. Two rules carry the milestone and are written into the specs rather than left to the code: clinical detail is a projection decided where the row is read, so a sponsor's HR cannot see a diagnosis in the API and no screen can leak what the API never sent; and a decided version — of a report, a claim — is never edited, a correction is a new version beside it. The fifth package is the five things M4's screens found the platform had not decided, closed as one piece of work.
- 2026-09-05 · Impeccable finish review of the M4 screens, run as the skill directs: a fresh reviewer on sixteen captures (both apps, 1440 and 390, full page, settled). Round one was `recapture` — and four of its six invalid captures were a real defect, not staging: mobile pages 423–780px wide because a grid child cannot shrink below its content and an sr-only table header widened the document. Round two was `fix` with eight findings; four rounds later it is `ship`, scoped to the fix list. What it changed: a mobile drawer in the shell (both apps were unnavigable at 390), a Karar block on the request, no upload form on a closed request, drawn icons for the theme toggle, three verdicts in the eligibility pane rather than two, status columns next to the reference, Turkish for every enum a person reads, a name being fetched shown as '…' rather than the unknown dash. Two findings stay partial by contract and are logged above: the worklist names another operator by id, and a request does not carry who decided it.
- 2026-09-05 · WP-I4-06 delivered, closing M4. Backoffice: Talepler (list and the detail as a story — what was asked, what the system said, who decided what, what is still missing), İş listem, Belgeler on the request, Bildirimler (templates and the message log with its suppressed rows). Provider portal, its first real screens: the form-first home where eligibility is computed live as the form fills, Taleplerim, the request with its upload, and the standalone check. Built under Impeccable: the structure of the portal was chosen from a dealt hand (seed bbc91507, the owner locked 'Form önce'), the direction contract sits in the provider surface brief, and the detector reports 0 findings on both apps. 25 Vitest files / 195 tests, 15 Playwright flows across the two apps.
- 2026-09-05 · The first real consumer of WP-I4-01's contract found it impossible for its main user: `CreateServiceRequest` required `programId`, and a provider-scoped actor may read neither programs nor enrollments, so it could not repeat an id it had never seen. An enrollment belongs to exactly one program, so the server now derives it and refuses a caller that names a different one; the check's own answer hands the portal the `enrollmentId`. Contract, Go, mock and a test that pins both halves.
- 2026-09-05 · The mock's provider permissions were not the Go role template's: `member.read` and `eligibility.check` were missing, which would have let the portal fail in the mock and work in production — the divergence class this project treats as a bug, in the other direction for once. The list is now the template's, exactly.
- 2026-09-05 · Open, found by the screens and written down rather than papered over:
  - CLOSED by WP-I5-05 (`assigneeDisplayName`, and the 409 carries the winner). The contract carries no actor directory, so a lost claim can name its winner only by actor id (`WorkItem.assigneeActorId`; the 409's detail is prose). DESIGN.md's rule is by name; the work item needs an `assigneeDisplayName`, and the refusal an extension member.
  - CLOSED by WP-I5-05 (`personDisplayName`, `providerDisplayName`). `ServiceRequest` carries ids only — no person, provider or service names — so every list row resolves names through cached reads. Fifty rows are fifty reads. Display names belong on the request.
  - CLOSED by WP-I5-05 (`enrollmentCandidates`, and the check accepts the choice). A member with two active enrollments answers `ENROLLMENT_MULTIPLE` and the desk cannot choose, because it may read no enrollments. The check's answer should list the candidates so the caller can ask again with one.
  - CLOSED by WP-I5-05 (migration 000035). The service→entitlement mapping was never built (M2 left `SERVICE_MAPPING_PENDING` as the honest answer; M3's catalog did not add it), so a provider's request always lands in review. The pane says 'İnceleme gerekli', which is true; the mapping is what would make it say 'Uygun'.
  - `listWorkItems` pages newest-first (see 2026-09-04); the worklist screen shipped on it with the overdue filter as the operator's tool, and the cursor change is still owed.
  - The documents panel exists twice — backoffice and provider — because the apps share no services abstraction. Extract when a third consumer appears, not before.
  - `/health/ready` still ignores the object store and clamd (see 2026-09-04).
- 2026-09-05 · WP-I4-05 delivered, closing the M4 backend: notification templates with a publishing gate, messages rendered only from a published template and a closed catalogue of ten safe variables, delivery attempts as rows, and per-recipient preferences with quiet hours. A variable outside the catalogue is refused at render time and nothing is written; no value may carry a run of eight or more digits, which is what a TCKN and a VKN look like; a deep link is a path with no query string, so there is nowhere for a token to ride along. A second publish for one (event, channel, locale) retires the first inside the same transaction, so the event is never momentarily without a template. Migration 000029; schema version 29.
- 2026-09-05 · An SMS with no provider behind it and a push with no device registry both ended as SENT. That is the exact record this package exists to prevent — its own criterion is that a member who was not told can be shown to have not been told, and why — so an adapter can now say it accepted a message and delivered nothing, and the message becomes SUPPRESSED naming CHANNEL_NOT_DELIVERABLE while the attempt stays on record. In-app is the deliberate exception: it is delivered by being written, because the message log is what the screen reads. The schema caught the first attempt at this — the reason was not in the column's CHECK, so Postgres refused the update rather than storing a status nobody had declared.
- 2026-09-05 · CLOSED by WP-I5-05 (`party.person_contact`). The reason SMS cannot work at all today: the database holds no contact details for a person or an organization. `iam.actor.email` is the only contact column anywhere, so EMAIL reaches an actor and nothing else, and an SMS is suppressed as NO_ADDRESS before it reaches its adapter. The tests say so rather than pretending otherwise. Where member contact details live is a decision M5 or the pilot onboarding has to make.
- 2026-09-05 · CLOSED by WP-I5-05 (six events wired). Nothing published a notification yet. `application.Publish` is the entry point and no module calls it, so the approve, return and reject commands of WP-I4-01 and the authorization of WP-I4-02 tell nobody. Wiring those events is a follow-up, and it is the point at which the safe-variable catalogue gets its real test.
- 2026-09-04 · WP-I4-04 delivered: the document pipeline. A file goes to a quarantine bucket through a presigned PUT, is scanned there by ClamAV, and only a clean file is copied to the secure bucket the rest of the product can reach. The API never receives file bytes and the database never stores them — a test allows the `document` schema exactly one binary column, `object.sha256`, and pins it to 32 octets, so adding a bytea column later fails. Two native clients were written for this: an S3 signer over the standard library and a thin clamd INSTREAM client. Migration 000028; schema version 28.
- 2026-09-04 · The EICAR criterion was verified by breaking each of its five guarantees in turn. The one worth recording: an end-state check that the secure bucket is empty passed even when the infected file HAD been copied there, because the infected path defensively removes the secure key afterwards. Emptiness at the end is not the same as never having been written. The test now wraps the store in a recorder and asserts zero writes aimed at the secure bucket during the scan; planting a copy-then-delete makes it fail, naming the copied key. The agent found this in its own work and said so, which is how it got fixed.
- 2026-09-04 · The live tests were checked for the other way a guard dies: a skip that reads as a pass. `TestEicarAgainstLiveClamAV` and the S3 round trip really do run against the native ClamAV and MinIO here, not a fallback. The fake-scanner EICAR test is the one that runs everywhere, so the criterion stays covered on a machine with no native services.
- 2026-09-04 · The API, worker and scheduler were started against live services rather than only tested: an object store nobody gave a key for now stops start-up with a message naming the variables, instead of an endpoint that exists and 500s. Open and not built: `/health/ready` still reports ready when the object store or clamd has gone down after start-up, so a store that dies mid-run is invisible to a probe. Small, and worth doing before anything depends on that probe.
- 2026-09-04 · WP-I4-03 delivered: work queues, work items with a snapshot SLA, an idempotent escalation job, approval policies with a non-overlap constraint, and comments carrying the visibility they will be judged against. Claiming is one optimistic UPDATE whose predicate is the whole precondition; twenty goroutines reaching for one item leave one owner, nineteen refusals each naming the winner by id, and one CLAIM event. An item is judged by the clock it was given: retuning a queue moves no existing due date. Migration 000027; schema version 27.
- 2026-09-04 · The claim guard was verified by defeating it rather than by reading it: with the UPDATE's `status = 'OPEN' AND row_version = $expected` predicate neutralised, the test reports twenty winners. The escalation sweep was verified the same way — without `escalated_at IS NULL` a second pass escalates again. Both were restored and re-proved. The agent had found the same class of defect in its own first draft: a pre-read that short-circuited on status was silently serialising the racers, so the concurrency test passed even with the UPDATE's guard removed. A test that cannot fail is worse than no test.
- 2026-09-04 · Open, for WP-I4-06 to settle: `listWorkItems` pages newest-first, because `httpx.Cursor` carries only `(created_at, id)` and every keyset list in the product shares that shape. For a list an operator works down it is the wrong end — filtering by `overdue` still shows the least-overdue first. The `priority` column and its index exist; ordering by them needs a wider cursor. Written into the WP-I4-06 spec rather than half-fixed here, because flipping only this one endpoint would break the shared convention without delivering priority ordering.
- 2026-09-04 · WP-I4-02 delivered: authorization reserving entitlement, fulfilment consuming it, and the voucher the member shows at the counter. The package calls the ledger's reserve, release and consume and writes no balance itself. Fifty concurrent authorizations against a balance covering twenty leave exactly twenty promises, twenty holds and an account whose reserved quantity equals their sum; a reserve that fails on the second line leaves no authorization and no hold at all. Expiry is an idempotent scheduler job. Migration 000026; schema version 26.
- 2026-09-04 · Issuing a voucher deliberately carries no Idempotency-Key: the middleware persists the response body into `system.idempotency_record.response_body`, and the issue response is the only place the plaintext token ever exists, so keying that route would write the token into a column. The hole this opens — a double-clicked issue minting a second usable token for one promise — is closed at the database instead, by a partial unique index allowing one ISSUED voucher per authorization, with a service check to give the second click a decent answer rather than a constraint violation. Both layers were proved: removing the check leaves the index refusing.
- 2026-09-04 · Two audit writes recorded a `masked_token` detail that never reached a column: `audit.SanitizeDetail` drops every key containing "token", silently and by design, so a comment claiming the masked tail was recorded described something that never happened. Removed rather than renamed around the rule — the voucher is already identified by the audit row's resource id. A sweep of every audit call site found no other package writing a key the sanitiser eats. The voucher secrecy test itself was proved alive twice: it names the exact table when a token is planted through a key the sanitiser permits.
- 2026-09-04 · Nineteen problem codes this package can emit had no Turkish message; they are now in `tr.json` and `en.json`. The wider gap logged above is unchanged — six older codes are still missing and nothing yet guards against the next one.
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
- 2026-09-03 · WP-I1-01 delivered, with a scope change the owner made: KAPSORA authenticates its own users, no Keycloak and no JDK ([ADR-022](../adr/ADR-022.md) supersedes ADR-005). Argon2id credentials, opaque session cookie whose digest is what the database stores, derived CSRF token, per-account lockout plus per-address rate limit, step-up and password change. Verified end to end against the running API. Migration 000012; schema version 12.
- 2026-09-02 · WP-I1-04 delivered in-house: audit recorder, outbox dispatcher (exactly-once under two concurrent dispatchers, retries, dead-letter, stale recovery), idempotency middleware, PostgreSQL rate limiter, scheduler job runner with four standard jobs, keygen. Found and fixed a baseline schema defect: the outbox dedupe constraint blocked every second event of a type (migration 000011).
