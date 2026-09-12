# WP-X1-01 · In-product help: the unfamiliar explained where it stands, and a page that says what it is for

| Field                      | Value                                                                                                                                                                                                       |
| -------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | cross-cutting (X1); independent of M8/M9                                                                                                                                                                    |
| Size                       | M                                                                                                                                                                                                           |
| Depends on                 | the three app shells and the shared `ui` package as delivered through M7                                                                                                                                    |
| Runs in parallel with      | anything; touches no API                                                                                                                                                                                    |
| Migration numbers assigned | none                                                                                                                                                                                                        |
| OpenAPI operations owned   | none                                                                                                                                                                                                        |
| Read first                 | `DESIGN.md` (the Ledger and its settled patterns, especially "a decision shows its reason where the amount is"), `PRODUCT.md` (the five kinds of people), the three app shells, `web/packages/ui/src/FormField.tsx` |

## 1. Goal

A person who has never seen KAPSORA should be able to read any screen without asking a
colleague: what a word means, what a status will do next, what this page is for and what
they can do on it. The help is judged by one sentence from the owner: **"her butonun, her
kutunun, her listenin ne ifade ettiğini bilmeli"** — and by its corollary: the obvious is
never explained. A help mark next to "Kaydet" is noise, and noise is the way help systems
die.

## 2. Scope

### 2.1 The unfamiliar, where it stands (`HelpHint`)

A small drawn mark — a circled question, one stroke weight — beside a label, a heading or
a column header. Opening it shows a short explanation: a title and one to three sentences.

- Opens on press, on Enter or Space, and on hover from a mouse; never on touch-hover,
  because the member app is a phone. Escape closes it; focus returns to the mark.
- The mark is a real button whose accessible name is "Açıklama: <title>". The popover is a
  dialog labelled by its title.
- It explains **concepts**: a domain term (icmal, mutabakat, hakediş, kesinti, mahsup, hak
  cüzdanı, ön onay), a figure whose arithmetic is the server's, a field whose consequence
  is not in its label, a column whose meaning is not its heading.
- It never explains what a label already says, never carries a refusal's reason (that is
  inline, per DESIGN.md), and never sits on a primary action.
- `FormField` gains `help`; any label or heading can place the mark directly.
- Terms are shared across the three apps and live once, under `help:terms.<key>`.

### 2.2 The page, explained (`HelpDrawer`)

A "?" in every app header, next to the account. It opens a drawer from the right — full
width on a phone — that tells what this page is for, what each section shows, what each
action does and what each status word means, in page order.

- The content is keyed by route. Each app owns a route table (`help.ts`) that maps its
  route patterns to page keys; the drawer shows the current page's content, or the app's
  general help when no page matches.
- Content lives in the `help` i18n namespace, one file per app plus one for terms, so the
  words are data and not code, and a tenant can be given its own wording later.
- A test in each app fails when a route in its table has no content.

### 2.3 Content

Turkish, formal "siz", short sentences, the product's own vocabulary as DESIGN.md fixes it.
The English skeleton is empty and falls back to Turkish per key.

Every page of the three apps as delivered through M7. Terms: the list in
`help/tr/terms.json`.

## 3. Design notes

- **Explain the unfamiliar, never the obvious.** A mark earns its place only when the
  reader would otherwise have to ask someone. Fewer marks, all of them useful.
- **Help is not a reason.** Why a button is disabled and why a decision was made stay
  inline in the product's own sentence; the help mark explains what the thing *is*.
- **Hover is a shortcut for the mouse, not the only way in.** Press, keyboard and touch
  open the same content.
- **The drawer is a reading surface, not a modal chain.** One drawer, one scroll, no
  nested navigation; it closes with Escape, the backdrop or its own close button.
- Everything else follows DESIGN.md; no new visual world.

## 4. Tests required

- `ui`: the mark opens on click and on keyboard, carries the accessible name, closes on
  Escape; hover opens for a mouse pointer and not for touch; `FormField` renders the mark
  after the label without nesting a button in the label; the drawer renders every section
  of a page's content; route matching picks the exact pattern and falls back.
- Each app: the header has the help button; opening it on a known route shows that
  page's title; every route in `help.ts` has content in the `help` namespace.
- `pnpm design` clean; the Impeccable finish review; captures at 390 and 1440, light and
  dark; i18n complete in Turkish. **Update `DESIGN.md` in the same commit.**

## 5. Acceptance criteria

- [ ] Every page in the three apps has page help reachable from the header in one press.
- [ ] Every shared domain term has one explanation, reachable from at least one screen
      where the term first appears.
- [ ] No help mark sits on an obvious control; none carries a refusal's reason.
- [ ] The help works by keyboard and on a 390px phone without hover.
- [ ] Lint, typecheck, unit, smoke, the detector and the finish review all clean.
