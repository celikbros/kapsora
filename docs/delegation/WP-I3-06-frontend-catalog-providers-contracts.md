# WP-I3-06 · Backoffice screens: catalog, providers, contracts, rules and the quote

| Field                      | Value                                                                                                |
| -------------------------- | ---------------------------------------------------------------------------------------------------- |
| Milestone                  | M3 (plan increment I3)                                                                               |
| Size                       | L                                                                                                    |
| Depends on                 | contracts of WP-I3-01..05 (mock first, real API when they land)                                      |
| Runs in parallel with      | all M3 packages                                                                                      |
| Migration numbers assigned | none                                                                                                 |
| OpenAPI operations owned   | none (consumes)                                                                                      |
| Read first                 | `DESIGN.md`, `PRODUCT.md` (Impeccable), WP-I2-06 as delivered (its patterns are the house style now) |

## 1. Goal

Give an operator the screens to build the vocabulary, the network, the money and the rules
behind every later transaction — and to see, for one concrete case, what a member would be
charged and why.

## 2. Scope

1. **Hizmet Kataloğu**: category tree with drag-free re-parenting (a parent picker, since a
   tree of six levels does not need drag and drop), definition list with search and filters,
   definition form. Code systems: list, values browsed with an `asOf` date and a search box,
   values imported from a pasted or uploaded JSON batch with a per-row error report.
   Service code mapping is edited on the definition, one row per system with its period.
2. **Sağlayıcılar**: provider list with type, tier, city and status filters; provider detail
   with tabs Profil, Lokasyonlar, Yetkinlikler, Uygulayıcılar; capability editor as a set
   (service or category, period); practitioner form whose registration number is entered
   once, shown masked afterwards, and searched behind a step-up exactly as the member
   identifier search is.
3. **Sözleşmeler**: contract list by parties and status; contract detail with its version
   history; **version editor** — price lists with their season and weekday scope, the price
   item table (the densest screen in the product: service or category or package, location,
   method, amounts, member share, period, priority), packages, quotas and payment terms;
   submit, publish (step-up, blocked for the submitter with the reason) and retire.
   A published version is read-only and shows its configuration hash.
4. **Kurallar**: rule set list; version editor with the rule table (priority, condition,
   actions, explanation), a CEL editor field with compile errors shown inline as the
   operator types, the test-case table, a "run tests" button whose results block or unblock
   submit, and a simulation panel that takes an input and shows the fired rules in order.
   Publishing follows the same maker-checker shape as plans and contracts.
5. **Fiyat sorgusu**: pick a person, a date, a provider and items; show the quote as a
   table of requested / contract / covered / payer / member amounts with the explanation
   list underneath and the ids of the contract version, price item, plan version and rule
   versions used, each a link to the thing it names.

## 3. Design notes

- The price item table is where this milestone is won or lost. It must stay readable at
  thirty rows: money right-aligned and monospaced, the scope of each row (definition,
  category, package, location) legible without opening it, and periods shown compactly.
  Resist a modal per row; edit in place, save the sheet.
- The quote screen is an explanation, not a receipt. The arithmetic must be followable top
  to bottom — contract amount, what the rules changed, what the balance covered, what is
  left for the member — and every step names its source. When the answer is
  `REVIEW_REQUIRED`, say what is ambiguous and link to both conflicting price items rather
  than showing a number nobody should trust.
- CEL is code. Give it a monospaced field, real compile errors, and no autocorrect. Do not
  attempt a visual rule builder in this milestone; an operator writing rules is a trained
  user, and a half-built builder is worse than a good text field.
- Everything else follows the patterns WP-I2-06 established: `ETag` merge-patch forms,
  `ProblemAlert` with stable codes, permission-gated controls that are absent rather than
  disabled, keyset paging with the opaque cursor, decimal strings never parsed into
  JavaScript numbers, nothing in browser storage.

## 4. Tests required

- Extend the MSW mock world with catalog, provider, contract and rule fixtures, including a
  deliberately ambiguous price pair so the `REVIEW_REQUIRED` path has a fixture.
- Vitest flows: category cycle refused by the server and rendered; capability overlap
  refused; contract version submit → publish blocked for the submitter → published by a
  second actor with a step-up; rule version submit blocked without a passing test case;
  quote rendering both a clean answer and an ambiguous one.
- Playwright smoke: catalog to definition, provider search by service, contract publish
  flow, and one quote end to end.
- `pnpm design` clean; i18n keys under `catalog.*`, `providers.*`, `contracts.*`,
  `rules.*`, `pricing.*` complete in Turkish.

## 5. Acceptance criteria

- [ ] An operator can define a service, map it to a code system, give a provider the
      capability to deliver it, price it in a contract version, publish that version with a
      second person, and get a quote that explains the member's share.
- [ ] A published contract or rule version is visibly read-only everywhere.
- [ ] The quote screen names every version and rule behind its numbers.
- [ ] Lint, typecheck, unit, smoke and the Impeccable detector all clean.
