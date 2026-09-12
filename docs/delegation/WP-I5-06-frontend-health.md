# WP-I5-06 · Screens: the health case, medical review, inpatient stay, the claim — and what HR never sees

| Field                      | Value                                                                                                                                                                                     |
| -------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M5 (plan increment I5)                                                                                                                                                                    |
| Size                       | L                                                                                                                                                                                         |
| Depends on                 | contracts of WP-I5-01..05 (mock first, real API when they land)                                                                                                                           |
| Runs in parallel with      | all M5 packages                                                                                                                                                                           |
| Migration numbers assigned | none                                                                                                                                                                                      |
| OpenAPI operations owned   | none (consumes)                                                                                                                                                                           |
| Read first                 | `DESIGN.md` (all of **Patterns settled while building**) and `PRODUCT.md`; WP-I4-06 as delivered and its finish-review notes in docs/plan/ROADMAP.md (2026-09-05); the provider surface brief under `web/apps/provider/.impeccable/surfaces/` |

## 1. Goal

The health vertical as people use it: a provider records what happened and asks for what
comes next; a medical reviewer decides on clinical grounds; a financial reviewer decides
on money; a sponsor's HR sees that a claim exists and what it cost, and nothing about why.
The last user is the one the milestone is judged by.

## 2. Scope

### 2.1 Provider portal (extends the form-first world of WP-I4-06)

1. **Vaka** — open a case from a request or standalone; the case as a story: encounters
   with their dates, the primary diagnosis chosen from ICD-10 by name and code, the
   documents. Recording a sensitive diagnosis shows the sensitivity the moment it is chosen.
2. **Tedavi raporu** — draft, services and limits as a table edited in place, the report
   file uploaded through the request-style document panel, submit; the report's state
   after review, and its versions.
3. **Yatış** — the admission form (date inside the tenant's window, estimated days, the
   documents the gate asks for), the stay with its segments, **Uzat** offered only when no
   extension is undecided, discharge with the reconciliation shown as exact figures.
4. **Claim** — lines edited in place with prices withheld until the server prices them,
   submit, the per-line decision and reason as they come back, **Düzelt** opening version
   2 with version 1 read-only beside it, and the invoice-readiness panel with its blockers.

### 2.2 Backoffice

1. **Tıbbi inceleme** — the medical reviewer's screen for a report or a claim: the
   clinical projection (diagnosis, description, summary, the linked documents), the
   decision per line with a clinical reason, and the access-purpose prompt when the case is
   sensitive. The prompt is a real dialog with the purpose list; a reviewer who declines
   sees the financial projection and is told why.
2. **Mali inceleme** — the financial reviewer's screen for the same claim: prices,
   contract amounts, member share, the duplicate suspicion with the other claim's
   reference, the cut with its amount — and **no description column, no diagnosis** — not
   hidden by CSS, absent because the API did not send it.
3. **Sponsor İK görünümü** — a person's health claims and cases as `SPONSOR_HR` sees them:
   references, dates, provider, amounts, status. The screen has no place where a diagnosis
   could go. The test for this screen is a scan of the rendered DOM for any clinical string
   the mock world seeded.
4. **Erişim kaydı** — who looked at a person's clinical data and why, from
   `listHealthAccessLog`, on the person's detail page for `audit.read`.

## 3. Design notes

- **Absence is the design.** The financial screen and the HR screen are not the clinical
  screen with fields hidden; they are laid out for what they carry. A column that would be
  empty for every row does not exist.
- **The purpose prompt is not a nag.** It appears once per case per session, names the
  case's sensitivity in plain words, offers the purpose list and a reason field, and says
  that the access is recorded. Declining is a real button that leads somewhere.
- **A version beside a version.** Claim and report corrections show the decided version
  read-only next to the draft, line by line, so what changed is visible without a diff.
- **Money is withheld until priced.** A claim line's amounts are the server's; the draft
  shows the provider's asked amount as asked and nothing computed.
- Everything else follows DESIGN.md's settled patterns, including the ones the M4 finish
  review added (status next to the reference, the deadline under the title below `md`,
  "…" while a name loads, the tenant whole in every header).

## 4. Tests required

- Mock world: a standard and a sensitive case, a draft/approved/rejected report with a
  version 2, an admitted stay with a decided extension, claims in every reachable status
  with mixed line decisions, a `sponsor.hr` account, a `medical.reviewer` and a
  `financial.reviewer`.
- Vitest flows: the HR DOM scan (no clinical string from the world appears on any HR
  screen); the financial screen has no description column; the purpose prompt gates a
  sensitive case and the decline path; Düzelt produces version 2 beside version 1; Uzat is
  absent while an extension is undecided.
- Playwright: provider records a case with a diagnosis, submits a report, opens a claim
  and submits it; a medical reviewer decides it; sponsor HR opens the same person and the
  page contains no diagnosis text.
- `pnpm design` clean; Impeccable's finish review run as the skill directs, with its
  captures at 1440 and 390; i18n under `health.*`, `claims.*`, `review.*` complete in
  Turkish. **Update `DESIGN.md` in the same commit as any screen that settles a pattern.**

## 5. Acceptance criteria

- [ ] Sponsor HR cannot see clinical detail in the UI, proven by a DOM scan against the
      seeded world, and cannot reach it through any link on those screens.
- [ ] The medical and financial screens differ in what they carry, not in what they hide.
- [ ] A provider can take an outpatient episode from case to invoice-ready claim, and an
      admission from preauthorization to discharge, without seeing another provider's
      anything.
- [ ] Lint, typecheck, unit, smoke, the detector and the finish review all clean.
