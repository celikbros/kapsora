---
name: KAPSORA
description: A benefits ledger for banks, insurers and providers — quiet, exact, and always able to say which tenant you are acting for.
colors:
  bg: '#F7F8FA'
  surface: '#FFFFFF'
  surface-2: '#EEF1F5'
  surface-3: '#DFE4EB'
  border: '#DCE2EA'
  border-strong: '#B9C3D0'
  fg: '#14202E'
  fg-muted: '#4B5A6B'
  fg-subtle: '#8A97A8'
  primary: '#1F5FA8'
  primary-hover: '#184C88'
  primary-fg: '#FFFFFF'
  primary-soft: '#E3EDF8'
  success: '#1E7F4F'
  success-soft: '#E4F4EB'
  warning: '#9A6700'
  warning-soft: '#FBF1D6'
  danger: '#B42318'
  danger-soft: '#FBE7E4'
  info: '#1F5FA8'
  info-soft: '#E3EDF8'
  focus: '#3B82F6'
typography:
  display:
    fontFamily: 'var(--font-sans)'
    fontSize: '1.75rem'
    fontWeight: 600
    lineHeight: '2.25rem'
    letterSpacing: '-0.01em'
  headline:
    fontFamily: 'var(--font-sans)'
    fontSize: '1.5rem'
    fontWeight: 600
    lineHeight: '2rem'
    letterSpacing: '-0.01em'
  title:
    fontFamily: 'var(--font-sans)'
    fontSize: '1.125rem'
    fontWeight: 600
    lineHeight: '1.75rem'
    letterSpacing: 'normal'
  body:
    fontFamily: 'var(--font-sans)'
    fontSize: '0.875rem'
    fontWeight: 400
    lineHeight: '1.25rem'
    letterSpacing: 'normal'
  label:
    fontFamily: 'var(--font-sans)'
    fontSize: '0.875rem'
    fontWeight: 500
    lineHeight: '1.25rem'
    letterSpacing: 'normal'
  caption:
    fontFamily: 'var(--font-sans)'
    fontSize: '0.75rem'
    fontWeight: 500
    lineHeight: '1rem'
    letterSpacing: '0.02em'
  mono:
    fontFamily: 'var(--font-mono)'
    fontSize: '0.8125rem'
    fontWeight: 400
    lineHeight: '1.25rem'
    letterSpacing: 'normal'
rounded:
  sm: '0.25rem'
  md: '0.375rem'
  lg: '0.5rem'
  xl: '0.75rem'
  full: '9999px'
spacing:
  xs: '0.25rem'
  sm: '0.5rem'
  md: '0.75rem'
  lg: '1rem'
  xl: '1.5rem'
  2xl: '2rem'
components:
  button-primary:
    backgroundColor: '{colors.primary}'
    textColor: '{colors.primary-fg}'
    typography: '{typography.label}'
    rounded: '{rounded.md}'
    padding: '0.5rem 1rem'
  button-primary-hover:
    backgroundColor: '{colors.primary-hover}'
    textColor: '{colors.primary-fg}'
  button-secondary:
    backgroundColor: '{colors.surface}'
    textColor: '{colors.fg}'
    borderColor: '{colors.border-strong}'
    typography: '{typography.label}'
    rounded: '{rounded.md}'
    padding: '0.5rem 1rem'
  button-secondary-hover:
    backgroundColor: '{colors.surface-2}'
    textColor: '{colors.fg}'
  button-danger:
    backgroundColor: '{colors.danger}'
    textColor: '{colors.primary-fg}'
    typography: '{typography.label}'
    rounded: '{rounded.md}'
    padding: '0.5rem 1rem'
  card:
    backgroundColor: '{colors.surface}'
    textColor: '{colors.fg}'
    borderColor: '{colors.border}'
    rounded: '{rounded.lg}'
    padding: '1.5rem'
  input:
    backgroundColor: '{colors.surface}'
    textColor: '{colors.fg}'
    borderColor: '{colors.border-strong}'
    typography: '{typography.body}'
    rounded: '{rounded.md}'
    padding: '0.5rem 0.75rem'
  table-header:
    backgroundColor: '{colors.surface-2}'
    textColor: '{colors.fg-muted}'
    typography: '{typography.caption}'
    padding: '0.5rem 0.75rem'
  badge:
    backgroundColor: '{colors.surface-2}'
    textColor: '{colors.fg-muted}'
    typography: '{typography.caption}'
    rounded: '{rounded.full}'
    padding: '0.125rem 0.5rem'
  tenant-dot:
    rounded: '{rounded.full}'
    size: '0.625rem'
  nav-item:
    backgroundColor: '{colors.bg}'
    textColor: '{colors.fg}'
    typography: '{typography.label}'
    rounded: '{rounded.md}'
    padding: '0.5rem 0.75rem'
  nav-item-active:
    backgroundColor: '{colors.primary-soft}'
    textColor: '{colors.primary-hover}'
---

# Design System: KAPSORA

## Overview

**Creative North Star: The Ledger.**

KAPSORA records who is entitled to what, who provided it, and what it cost — for a
bank, an insurer or a sponsor, and for the providers that serve their members. A ledger
is trusted because every line is plain, every state is visible and nothing is decided
by tone. That is the governing rule, and it settles the three qualities the interface
needs:

1. **Be plain.** Data, labels and actions in the same quiet voice. No hero numbers, no
   gradients, no illustration. Emphasis is reserved for state (pending, suspended,
   conflict) and for the tenant the user is acting for.
2. **Be exact.** Money always carries its ISO code, dates are shown in the tenant's time
   zone, identifiers are masked the same way the server masks them. What the screen
   shows is what the record holds; the client never invents a rounding or a status.
3. **Never let the user act for the wrong tenant.** The active tenant is named in the
   header on every screen, with a colour derived from its code. Switching tenant is a
   deliberate act on its own page, never a dropdown on a form.

**Mood:** calm, precise, institutional without being cold. A back office people work
in for hours, on wide screens, with keyboard and tab order that just work.

**Modes:** the backoffice is _Operate_ (dense lists, forms, review). The provider portal
is _Operate_ with a fast-entry bias. The member app is _Read_ first, _Experience_ only
on the search and booking screens that arrive in later increments.

**Anti-references:** the consumer fintech dashboard (big rounded metric cards, pastel
gradients, celebratory copy); the crowded insurer portal (nested tabs, everything on one
screen); and the default AI look (beige surfaces, thick coloured side borders, italic
serif headlines, vague headings like "Welcome back").

### Open decisions — do not treat as settled

- **Typeface.** Inter via `--font-sans` with a system fallback; it is a neutral default,
  not a chosen identity. Replacing it is a product decision. Reference the variable,
  never hard-code a family.
- **Per-tenant skins.** The tenant colour is derived (hash of the tenant code) and is
  only used for the header badge, the accent stripe and status dots. Whether tenants get
  configurable branding is undecided.

## Colors

**The normative source is `web/packages/config/theme.css`** (Tailwind v4 `@theme`
tokens). The frontmatter mirrors the light values in hex; **if they disagree,
`theme.css` wins** — update it first, then this file.

Light and dark are two value sets of the same names; `data-theme="dark"` on `<html>`
redefines the tokens.

| Role                                      | Light                                         | Dark                                          | Use                                                           |
| ----------------------------------------- | --------------------------------------------- | --------------------------------------------- | ------------------------------------------------------------- |
| `bg`                                      | `#F7F8FA`                                     | `#0F1620`                                     | Page ground and sidebar. Never a card.                        |
| `surface`                                 | `#FFFFFF`                                     | `#172130`                                     | Cards, panels, inputs, menus, header.                         |
| `surface-2`                               | `#EEF1F5`                                     | `#1F2B3D`                                     | Table headers, hover fills, code chips.                       |
| `surface-3`                               | `#DFE4EB`                                     | `#2A3850`                                     | Deepest tonal step; rare.                                     |
| `border`                                  | `#DCE2EA`                                     | `#2A3850`                                     | Default separation.                                           |
| `border-strong`                           | `#B9C3D0`                                     | `#3B4C66`                                     | Interactive edges: inputs, secondary buttons.                 |
| `fg`                                      | `#14202E`                                     | `#EEF2F7`                                     | Primary text.                                                 |
| `fg-muted`                                | `#4B5A6B`                                     | `#A6B3C4`                                     | Secondary text, labels, captions.                             |
| `fg-subtle`                               | `#8A97A8`                                     | `#6E7E93`                                     | Placeholders, disabled. Never for meaning.                    |
| `primary`                                 | `#1F5FA8`                                     | `#6FA3E8`                                     | The one call to action, active nav, links.                    |
| `primary-soft`                            | `#E3EDF8`                                     | `#1E3552`                                     | Active nav background, selected rows.                         |
| `success` / `warning` / `danger` / `info` | `#1E7F4F` / `#9A6700` / `#B42318` / `#1F5FA8` | `#4CC38A` / `#E5B546` / `#F0736A` / `#6FA3E8` | Status only: badges, alerts, toasts. Each has a `-soft` fill. |
| `focus`                                   | `#3B82F6`                                     | `#3B82F6`                                     | The focus ring, 2px, offset 2px, everywhere.                  |

Rules: one primary action per screen; status colours never decorate; text on `-soft`
fills uses the strong colour of the same family and stays above AA contrast; the tenant
colour is never used for text.

## Typography

Six roles, one family. Headlines are semibold and slightly tight; body and labels are
regular/medium at 14px, captions at 12px uppercase-free (uppercase only for table
headers, tracked 0.02em). Monospace is for identifiers, codes and trace ids only.

## Layout

Backoffice: 56px header, 256px sidebar on ≥768px, content padding 24px, max content
width unconstrained (tables need it). Below 768px the same navigation is a drawer opened
from a drawn menu icon at the header's left; Escape, the backdrop and a chosen link close
it. Cards separate concerns, never nest, and may shrink (`min-w-0`), because a grid child
that cannot shrink makes its widest line the page's width. Tables are the primary reading
surface: 12px uppercase headers on `surface-2`, 8px/12px cell padding, hover fill
`primary-soft` at 40%, no zebra stripes. A table scrolls inside its own container, and
that container is positioned, so an sr-only header label cannot widen the document.

Forms: two columns on ≥768px, label above control, hint below, error below hint in
`danger`, required marked with `*` and an sr-only word. Client validation gives instant
feedback; the server's 422 field errors are mapped onto the same fields and win.

## Components

Built on Radix primitives (Dialog, Toast, DropdownMenu, Label) styled with the tokens;
native `<select>` in forms. `ProblemAlert` renders every problem+json document with the
Turkish message for the code and the trace id. Status is a `Badge` with tone from the
status, never a coloured row. The tenant is a `Badge` in the header with a 10px colour
dot and a 3px accent line under the header — the only place the tenant colour appears
as a line.

## Patterns settled while building

These were decided against real screens in M2 and M3. They are here so the next screen
does not re-argue them.

**A dense editable set is a table that edits in place.** Price items, entitlement
definitions, rules, capabilities, mappings. The work is comparing rows against each other
— which service, at which location, for how much — and a modal per row hides the very
thing being compared. Add and remove are buttons under the table; the whole set saves at
once, because the meaningful unit is the sheet, not the row.

**Money is right-aligned and monospaced, and it is never a number.** Amounts arrive as
decimal strings and are rendered as they arrive. The client does no arithmetic on money
and no rounding: the one rounding in the system happens once, on the server, at the end of
a calculation. Coordinates are the single exception where a float is correct — they are
geography, not money.

**A screen withholds a number rather than guessing one.** When a quote cannot be priced
because two prices tie, it shows no member figure at all and says what is ambiguous. An
operator who is shown a number reads it as the answer.

**An explanation sits next to the thing it explains.** Per-line reasons go directly
beneath that line's figures, not in a list at the foot of the page. A result that names
its sources — contract version, plan version, rule versions — links to each of them.

**Status decides the page.** On a version screen, a draft is editable, anything past draft
is read-only and shows the hash it was published under, and the commands available are
only the ones that status allows. The screen never offers a button the server will refuse.

**A control the operator cannot use is absent, not disabled.** A disabled button is a
question the operator cannot answer.

**Hidden rather than refused, where existence is itself information.** A read-only caller
does not see an unagreed draft contract version at all: 403 would confirm that terms are
being renegotiated. The same rule sends another provider's row to 404.

**When the machine has better words, use them.** A rule condition that does not compile
shows the compiler's own message, because it says where the expression broke. A
translated "it did not compile" would be worse than the untranslated truth.

**People are chosen by name, never by identifier.** Nobody has a member's UUID in their
head, and a form that asks for one is a demo rather than a tool. Identity numbers are
entered once, shown masked afterwards, and searched only behind a password.

**Code is treated as code.** The CEL condition field is monospaced with spellcheck and
autocorrect off, and there is no visual rule builder: an operator writing rules is a
trained user, and a half-built builder is worse than a good text field.

**A tab strip sits on a line.** Triggers are square with a 2px underline; a rounded corner
under a thick border fights the line it sits on, which is what the detector flagged the
first time this component was written.

**A record read to understand a decision is laid out as the sequence that produced it.**
The request detail is not a form of its fields; it is what was asked for, what the system
said, what a person decided, and what is still missing, in that order down the page. Anyone
opening it is reconstructing how the thing got to where it is, and a field grid makes them
do that reconstruction themselves.

**Two refusals that ask for different things must not look the same.** A returned request
is an invitation to correct something and its panel says what to fix and offers the way to
fix it. A rejected one is finished: it says why, and offers nothing but a new request.
Giving both the same red banner is how a correctable mistake gets read as a final refusal —
and the person who reads it that way stops, which is the whole cost.

**A state is shown as it is, never as what it is about to be.** A file being scanned says
"taranıyor" with a spinner; it does not say "yüklendi" because the upload finished. The
download appears only where the server says `downloadable`, and where it is absent the
screen says which state is in the way. An optimistic label on an unfinished process is a
lie the operator only discovers by clicking.

**A lost race names the winner.** When two operators reach for one work item, the loser is
told who holds it, by name. "Somebody else took it" leaves two people clicking the same
button; naming the owner ends the question. The same applies to any conflict where the
resolving fact is already in the refusal.

**A list read for hours is dense and boring on purpose.** The worklist is a table with the
columns an operator sorts by — no cards, no avatars, no progress rings. Decoration costs
rows, and rows are what the work is.

**A name being fetched is "…"; a name nobody has is "—".** Lists resolve people, providers
and services by id one cached read at a time, so a cell is empty for a moment after the
rows land. That moment reads as "unknown" if it shows the same dash unknown shows. It does
not: the fetch shows an ellipsis and only a lookup that came back empty shows the dash.

**On a narrow screen the verdict sits next to the reference, and the deadline under the
title.** A wide table scrolls inside its own box; what the scroller hides is detail, never
the answer. Status is the second column of every list, the actions of a worklist row come
right after its title, and below `md` a work item's due time and "Gecikti" repeat under the
title so the clock is in the first viewport whatever is scrolled away.

**The header keeps the tenant whole.** Below `sm` the wordmark stays as the way home, the
theme toggle steps aside, the user menu says "Hesap", and the tenant badge drops its
" · CODE" suffix rather than clipping a glyph. An operator can always see which tenant they
are in, in every viewport, and a truncated tenant is not "seeing".

**A control never changes what it is while somebody is using it.** The document panel's type field used to be a list of the types still missing, and what is missing is only known once the linked documents arrive. On a request whose named types were already attached the field was a dropdown for one frame and a text box the next, under the cursor. A control's kind is decided by data that is already there when the screen renders — the request's own required types — and a later query may change the options, the wording or the state, never the control. The rule generalises: anything a second query decides may narrow a field, never replace it.

**A name being fetched is only a name.** Since the request and work-item lists carry `personDisplayName`, `providerDisplayName` and `assigneeDisplayName` on the wire, the "…" of a pending row is rare rather than usual, and the ellipsis stays for the cells no list carries yet. A screen asks for a name row by row only when the list does not have one.

**The projection the server sent decides which columns a page has.** A claim arrives as
`CLINICAL` or `FINANCIAL` and its lines table is built for the half that came: diagnosis and
description for the medical reviewer, contract, approved, payer and member amounts for the
financial one. Neither is the other with columns hidden — a column that would be empty for
every row does not exist — and the same rule lays out the case's encounters, where a branch
or a diagnosis heading is there because the field arrived. Where the financial half is what
came, the page says so in a line at the top rather than leaving the absence to be noticed.

**A column the caller may not read does not exist.** The claim and the case carry the
provider's id and not its name, and resolving that name needs `organization.read`; without
the permission the provider column is absent from the header and from every row, instead of
a dash repeated down the table. Where a code is on the wire it is the identity — monospace,
always shown — and the looked-up name is an appendix to it. A permission decides a column,
never a cell.

**A sensitive record asks why before its clinical half opens, once per record, in memory
only.** The server answers 428 and the screen puts a real dialog in front of the page: a
purpose from the reference list, a reason, and the sentence that the look is recorded. The
answer lives for the tab's lifetime and nowhere else, because a stated purpose is not a
credential to store and asking again on every refetch would turn a question into a
click-through. Declining is a button that leads somewhere — the financial half, with a
sentence saying why the diagnosis is not on the page — and not a dead end.

**A sensitive category says so the moment it is chosen, before anything is saved.** The
ICD-10 search marks the sensitive codes in its own result list, the chosen set keeps the mark
per row, and a warning-soft line under the set says what choosing one means. What it changes
— who may read this case from now on — belongs to the person recording it while their hand is
still on the control, not to a dialog they meet three screens later.

**The medical stage answers yes or no; the money is the financial stage's.** The medical
reviewer's decision offers approved or rejected and no amount field appears at all; only the
financial stage offers partial approval and a cut, and only there are figures typed. A
refused line carries zeros the client writes as strings, and the line that reminds the
financial reviewer that payer plus member equals the approved amount is a sentence, not a
sum the screen performs: nothing here adds, rounds or checks arithmetic on money.

**A decided version stands above its correction, never beside it.** A returned claim shows
the decided version and then the draft that replaces it, stacked down one card, sharing the
first columns — line, service, description, quantity, asked — in the same places, so the eye
compares by dropping straight down. Two line tables in half a viewport each are crushed to
where neither can be read, which is the opposite of what a comparison is for.

**Below `md` the decision sits under its line, and only one structure is rendered.**
`useMinWidth(768)` chooses the table or the stacked blocks; the controls are built once and
placed into whichever is rendered, so a test, a screen reader and a keyboard meet one set of
controls rather than two with one hidden by CSS. A wide table may scroll, but what the
scroller hides is the detail and never the answer: the decision, its figures and its reason
move under that line's own numbers.

**A chapter header carries its chapter's state.** On the case, the report section's heading
shows the latest report's status, the admission's shows the open stay's, the claims' shows
how many are still open, each with its single action on the same line. The case is read as a
spine — what happened, what was written, who was admitted, what was billed — and the person
opening it needs to see from the first viewport which chapter still wants work. A heading
that is only a noun makes them open every section to find out.

## Motion

Toasts slide up 160ms ease-out; nothing else animates. `prefers-reduced-motion`
disables the slide and the spinner rotation.

## Accessibility

WCAG 2.2 AA: skip link, `<main>` landmark, `aria-current` on navigation, labelled
controls, `role="alert"` on errors, visible focus ring, Escape closes dialogs and
menus, keyboard-only flows covered by tests.
