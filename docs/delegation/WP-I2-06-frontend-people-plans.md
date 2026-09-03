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
