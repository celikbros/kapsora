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
  receipt:
    backgroundColor: '{colors.surface}'
    textColor: '{colors.fg}'
    borderColor: '{colors.border}'
    rounded: '{rounded.lg}'
    padding: '1rem'
  tab-bar:
    backgroundColor: '{colors.surface}'
    textColor: '{colors.fg-muted}'
    borderColor: '{colors.border}'
    typography: '{typography.caption}'
    height: '3.5rem'
  tab-bar-active:
    textColor: '{colors.primary}'
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
is _Operate_ with a fast-entry bias. The member app is _Read_ on the home and the booking
list and _Operate_ on the search-hold-confirm flow; from M6 it is a product rather than a
placeholder, and its first viewport is a 390px phone.

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

Member app: no sidebar. A 56px header carries the wordmark, the tenant badge and the exit;
the three tabs stand in that header on ≥768px and are a fixed bottom bar below it, inside
`env(safe-area-inset-bottom)`. Content is padded 16px and capped at `max-w-5xl` on a wide
screen. At ≥1024px the search and booking screens split into content and a 22rem receipt
column (`grid-cols-[1fr_22rem]`, the receipt `sticky top-20`); below that the receipt is
pinned above the tab bar and the list under it is padded clear of it.

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

**The receipt** (`member/src/lodging/Receipt.tsx`) is the surface's signature component: a
titled `<dl>` of label/value lines, a rule, then the total as the largest line and the one
action beneath it. Money lines are monospace, right-aligned and `tabular-nums`; a line may
carry a muted note under its value. It is the same component pinned (phone) and in column
(desktop), never two.

**The member tab bar** is three links — Ana sayfa · Ara · Rezervasyonlarım — each a drawn
20px icon over a 12px label, `aria-current` colouring the active one `primary`. Above 768px
the same three links are a tab strip in the header, square with a 2px underline, as the tab
strip rule requires.

## Patterns settled while building

These were decided against real screens, M2 through M6. They are here so the next screen
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

**A commitment that costs money keeps the amount on screen before, during and after it.**
The member's booking is a receipt that fills in as they choose — dates, room, nights, total,
plan payı — with **Ödeyeceğiniz** as the last and largest line and the one action under it.
It is one component in one place, pinned above the tab bar on a phone and a sticky column on
a wide screen, so the figure is never a screen away from the button. The consumer-travel
pattern of photo cards and a total revealed at the end is refused outright: a member is
agreeing to their share of a plan, and a share they cannot see is not agreed to.

**A command that costs money carries the amount on itself.** Onayla is two lines — the verb,
then "Ödeyeceğiniz 4.940,00 TRY" in monospace beneath it — because the button is the last
thing the thumb touches and the last place the figure can still stop someone. A cancellation
that costs a fee says the fee in the toast it produces, for the same reason.

**A countdown renders the server's deadline and nothing else.** The hold shows `holdExpiresAt`
through a `<time role="timer">`, ticking a local clock only to redraw it, so a screen that
slept, a tab that was restored and a second device all agree on the same moment; a timer
started on click would drift away from the server that will actually release the room. The
exact expiry is repeated underneath as a date and time, because a number counting down is
not a thing anyone can write down.

**When the deadline passes, the action is replaced by the way back.** The expired hold does
not keep Onayla in a disabled state or leave it live for the server to refuse: the panel
becomes a sentence saying the hold has lapsed and a primary link to search again. This is the
absent-not-disabled rule at its sharpest — the moment a screen has nothing to offer, it owes
the member the next move rather than the corpse of the last one.

**A one-time secret is shown on request, hidden again, and stored nowhere.** The voucher
token appears only after the member asks for it, "Gizle" puts it away, and nothing is written
to browser storage; the code lives in the tab's memory and a reissue is a fresh request to the
server. It is set large in monospace with wide tracking because its whole job is to be read
aloud or typed at a desk, and a code shown by default is a code shown to whoever is standing
behind the member on a bus.

**One structure per viewport, and it is built once.** `useMinWidth` decides the table or the
stacked rows, the header links or the bottom tab bar, the receipt column or the pinned
receipt — and only the chosen one is rendered, so a test, a screen reader and a keyboard meet
one set of controls instead of two with one hidden by CSS. The shared halves are lifted into a
single `controls` expression and placed into whichever structure renders, which is what keeps
the two arms from drifting apart as the screen grows.

**A stacked row carries its status beside its reference.** Where a wide table becomes rows on
a phone, the identifier and the badge share the first line, the people and places the second,
the dates and the money the third. What a horizontal scroller hides is always detail; the
answer to "what state is this in" is never something an operator has to drag sideways to see.

**A remaining entitlement is a name, a dotted leader, a numeral and its unit word.** The home
screen's first block is what the member has left — nights sorted to the top, because that is
the question they opened the app with — as a four-column baseline grid where the leader is
`aria-hidden` decoration and the figure is monospace, large and right-aligned against the unit.
The wallet's benefit period sits under its name in the muted voice, so two accounts with the
same definition name stay distinct without a code being shown to a member.

**A refusal names the night.** Inventory that would fall below what is already held is refused
as `INVENTORY_BELOW_COMMITMENT` with the offending date, and a search that cannot be held is
`ROOM_UNAVAILABLE` naming the first night with no room; both reach the screen through
`ProblemAlert`, which renders the server's own Turkish sentence and the trace id. The client
never writes a friendlier version of a refusal it did not compute — a rewritten "bir şeyler
ters gitti" would drop the one fact that tells the member which date to change.

**What undoing costs is answered before undoing is offered.** İptal et is not on the
confirmed booking until the member asks "İptal edersem ne öderim" and the server's preview has
answered in sentences — free until a date, or a fee with its amount and the nights it covers,
and how many nights come back to the plan. Only then does the danger button appear, beside a
"Vazgeç" that clears the preview. A destructive command whose price is discovered afterwards
is a trap, however clearly the policy was written somewhere else.

**A policy is shown as labelled rows in Turkish, never as a compact code line.** The lodging
terms read back as a definition list — free-cancellation hours, penalty kind with its value,
no-show percent, hold minutes, night bounds — and the member's copy of the same terms is a
short list of sentences. `NIGHTS · 1 · %100` is what the record holds; it is not what a person
can act on, and the screen owes them the sentence.

**A loading state carries the words of the section it is in.** Each panel that is still
fetching shows a spinner beside "Yükleniyor" under its own heading, marked `aria-busy`, rather
than a bare spinner floating next to content that has already settled. A lone spinner in a page
of finished panels reads as something being wrong; a spinner with a label reads as a panel
still arriving, which is what it is.

**An icon is drawn, at one stroke weight, and never a character standing in for one.** The
member tabs are three 20px SVG paths at 1.6 stroke with round caps, `aria-hidden` because the
label under each one is the name. A typographic glyph pressed into service as an icon inherits
the text metrics and the font's own idea of a shape, and it goes wrong in exactly the place —
a 390px bottom bar — where the icon is doing the most work.

## Motion

Toasts slide up 160ms ease-out. The member surface adds the system's one authored moment:
`.receipt-line` settles in 260ms on an exponential ease-out
(`cubic-bezier(0.16, 1, 0.3, 1)`) from an already-visible resting state — opacity 0.35 and
6px down, so a line reads as settling and never as missing. Nothing else animates.
`prefers-reduced-motion` disables the slide, the settle and the spinner rotation.

## Accessibility

WCAG 2.2 AA: skip link, `<main>` landmark, `aria-current` on navigation, labelled
controls, `role="alert"` on errors, visible focus ring, Escape closes dialogs and
menus, keyboard-only flows covered by tests.
