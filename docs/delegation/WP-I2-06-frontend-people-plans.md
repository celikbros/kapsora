# WP-I2-06 · Backoffice screens: people, memberships, programs/plans, entitlements, eligibility

| Field | Value |
|---|---|
| Milestone | M2 (plan increment I2) |
| Size | L |
| Depends on | WP-I2-01..05 contracts (mock first, real API when they land); WP-I1-05 foundation |
| Runs in parallel with | all M2 packages |
| Migration numbers assigned | none |
| OpenAPI operations owned | none (consumes) |
| Read first | `DESIGN.md`, `PRODUCT.md` (Impeccable), WP-I1-05 report, v1.2 46 items 7-11, 15.4/15.5 |

> **Delivered in-house, 03.09.2026.** Screens live under `web/apps/backoffice/src/` in
> `people/`, `benefit/`, `adjustments/` and `imports/`; routes and the main menu are wired
> in `router.tsx` and `nav.ts`. Decisions taken while delivering:
>
> - **Step-up is a promise, not a fire-and-forget.** `useStepUp().run(action)` keeps the
>   promise it handed the caller pending until the retry after the password finishes, so a
>   screen still receives what came back and can act on it. Cancelling resolves undefined.
> - **The mock now matches the server on step-up.** Publishing and retiring a plan version
>   go through `identity.RequireStepUp` in `internal/benefit/transport/http/planversion.go`;
>   the mock did not ask, and now does.
> - **Import screens are gated on `import.execute`**, the permission the server actually
>   checks, not on the member permissions this document first suggested.
> - **A draft plan version's period and notes are editable on its own page.** Submit refuses
>   without a `validFrom` and a draft cannot be deleted, so a version created with the wrong
>   start date would otherwise be stuck.
> - **The submitter is offered no publish button** and is told why; the server still refuses
>   with MAKER_CHECKER_SAME_ACTOR if it is reached another way.
> - **Two published versions may not overlap.** The server refuses rather than closing the
>   older period itself, so an operator retires the running version before the next starts.
>   The Playwright smoke test walks exactly that.
> - **Quantities never become JavaScript numbers.** They are read, edited and sent as
>   decimal strings; a test asserts `2500.500000` survives the round trip intact.
> - **Approve and reject on an adjustment carry no idempotency key**, because the OpenAPI
>   operations do not accept one. They are protected by the ETag they were read with.
> - **Identity numbers stay in component state**, never in a URL, a query key or storage.
>   Tests assert both that and empty `localStorage`/`sessionStorage`.
> - Tests: 85 Vitest specs across the workspace, 6 Playwright smoke flows, Impeccable clean.

## 1. Goal

The backoffice operator can find a member (by name, or by identifier with step-up),
see the person with masked identifiers, family and memberships, enroll them, manage
programs and plans through the maker-checker flow, see entitlement balances with the
ledger, and run an eligibility check that explains its answer. Everything follows
`DESIGN.md`; `pnpm design` (Impeccable detector) stays clean.

## 2. Screens (backoffice)

1. **Hak Sahipleri** list: search by name (`q`), status filter, sponsor filter; "Kimlik
   ile ara" opens a dialog that asks for step-up (password re-entry via
   `POST /session/step-up`) then calls `search-by-identifier`; result opens the person.
2. **Hak sahibi detayı**: header (display name, status badge, masked primary identifier),
   tabs: Kimlik (masked identifiers, add/remove with `member.manage`), Aile (relationships,
   add/end), Üyelikler (sponsor memberships, add/patch), Kayıtlar (enrollments, add/patch),
   Haklar (entitlement accounts with balances; ledger drawer per account), Uygunluk (run
   an eligibility check with service date and items; show outcome, explanations, balances,
   plan version).
3. **Yeni hak sahibi** form: names, birth date, sex, identifiers with instant TCKN
   checksum; server errors mapped to fields.
4. **Programlar ve Planlar**: program list/detail/form; plan list under a program; plan
   version list with status badges; version editor (draft only) with the entitlement
   definition table (inline rows, unit/period/quantity validation); actions Submit,
   Publish (step-up, disabled for the submitter with the reason), Retire; published
   versions read-only with the configuration hash shown.
5. **Üye içe aktarma**: upload (file + sponsor + source system/version), batch status page
   with counters, row review table (status filter, decision actions), apply button with
   confirmation, reconciliation summary.
6. **Hak düzeltmeleri**: pending adjustments queue with approve/reject (step-up).

## 2b. Contract facts settled during WP-I2-01..05

These are the shapes the screens must build on; they were decided while the backend
landed, so do not re-invent them.

- **Identifier search** is `POST /api/v1/people/search-by-identifier` with
  `{type, value, sponsorOrganizationId?}`; it needs `member.identifier.search` and a valid
  step-up, answers a single `PersonSummary` or 404, and is rate limited (20/min per actor).
  The value never appears in a URL, a query key or storage.
- **Person patch** is merge-patch with `If-Match`. An identifier entry `{type, value}`
  replaces the person's rows of that type; `{type, remove: true}` deletes them.
- **Relationship types** come from `GET /api/v1/party/catalogs` (SPOUSE, CHILD, PARENT,
  DEPENDENT, GUARDIAN, DELEGATE); membership types include FAMILY, which requires a
  principal membership. Overlapping periods answer 409 RELATIONSHIP_OVERLAP /
  MEMBERSHIP_OVERLAP; a taken member number answers 409 MEMBER_NO_TAKEN.
- **Plan versions**: submit (`plan.manage`) then publish (`plan.publish` + step-up) by a
  different actor, else 403 MAKER_CHECKER_SAME_ACTOR. Published versions are read-only and
  show `configurationHash`; retire needs a reason. Overlapping published periods answer
  409 PLAN_VERSION_OVERLAP.
- **Enrollments** need an ACTIVE membership and a published plan version covering
  `validFrom` (422 PLAN_NOT_PUBLISHED on `planId`).
- **Entitlements**: `GET /people/{id}/entitlements?asOf=` returns own and family-shared
  accounts with a `shared` flag; quantities are decimal strings in JSON, so keep them as
  strings in the client and never parse them into a JavaScript number. Ledger is paged
  newest first. A frozen account (status FROZEN) refuses reserves; show it clearly.
- **Adjustments** are maker-checker: create (PENDING), then approve or reject by another
  actor with step-up; approve moves the balance.
- **Eligibility**: the result carries `evaluationId`, `planVersionId`, `explanations[]`
  (INFO/WARNING/ERROR) and `items[]` per requested line. Per-item entitlement hints travel
  in `context.entitlementCodes` (positional, one per item) because `serviceItems` accepts
  no extra properties. An `Idempotency-Key` replays the stored evaluation; the stored
  snapshot is retrievable at `GET /eligibility/evaluations/{id}`.
- **Member import** statuses are RECEIVED, VALIDATING, REVIEW, READY, APPLYING, APPLIED,
  FAILED, CANCELLED; row statuses PENDING, VALID, INVALID, MATCHED, CONFLICT, APPLIED,
  SKIPPED with decisions CREATE, UPDATE, SKIP. Upload is multipart and needs step-up; a
  duplicate file answers 409 IMPORT_DUPLICATE; apply answers 202 and the batch page polls.

## 3. Rules

- Step-up dialog component in `@kapsora/auth` (`useStepUp()`): retries the guarded
  action once after success; shows `STEP_UP_REQUIRED` problems inline.
- ETag/If-Match on every patch; 412 conflict dialog reused from organizations.
- Identifier values are never kept in component state after submit; search inputs are
  cleared on close; no identifier in URLs or query keys (use the returned person id).
- MSW handlers for every new operation with synthetic data (valid TCKNs, family trees,
  two published versions per plan, ledger with 30 movements).
- i18n keys under `people.*`, `programs.*`, `plans.*`, `entitlements.*`, `eligibility.*`,
  `imports.*`; Turkish complete, English skeleton.

## 4. Tests required

- Vitest flows with the full app + MSW: name search → detail; identifier search requires
  step-up; add identifier with bad checksum shows instant error; enrollment form
  requires a published plan (mock 422 mapped); plan version submit/publish with
  same-actor block; eligibility check renders explanations; import review decision.
- Playwright smoke: people list, person detail tabs, plan publish flow, eligibility check.
- `pnpm design` clean; storage still empty after the flows.

## 5. Acceptance criteria

- [ ] Screens 1-6 work on mocks and, with `VITE_API_MOCK=false`, against the real API for the operations that exist.
- [ ] No identifier in storage, URLs, query keys or logs (tests).
- [ ] Step-up enforced client-side for identifier search, publish, approvals; server remains the authority.
- [ ] Impeccable detector clean; DESIGN.md respected (no new tokens without updating it).
