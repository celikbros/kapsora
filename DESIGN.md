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
  figures:
    textColor: '{colors.fg}'
    typography: '{typography.body}'
    width: '24rem'
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
is _Operate_ with a fast-entry bias, and from M7 that includes the money it is owed —
earnings, invoices, icmaller and the cari ekstre. The member app is _Read_ on the home and the booking list
and _Operate_ on the search-hold-confirm flow and on the three-step reimbursement; from M6
it is a product rather than a placeholder, and its first viewport is a 390px phone.

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

Light and dark are two value sets of the same names, and dark is reached two ways:
`data-theme="dark"` written on `<html>` by the theme button, and — for a person who never
pressed it — `prefers-color-scheme: dark`, the preference their machine already carries. A
screen nobody asked about should arrive in the theme its owner set everywhere else, so the
operating system is the default and the button is an override in both directions: choosing
light keeps light on a dark machine. The value set is written twice in `theme.css` because
CSS cannot share a declaration block between a selector and a media query; `theme.test.ts`
fails if the two copies ever stop matching.

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
screen. At ≥1024px the search, booking and reimbursement screens split into content and a
22rem receipt column (`grid-cols-[1fr_22rem]`, the receipt `sticky top-4`/`top-20`); below
that the receipt is pinned above the tab bar and the list under it is padded clear of it.

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

**The figures block** is the operator's counterpart to the receipt: a `<dl>` capped at
`max-w-sm` (24rem) on a `[max-content_minmax(0,1fr)]` grid, labels muted in the left
column, values right-aligned monospace `tabular-nums` in the right. One row is emphasised —
label `font-medium`, value semibold and a step larger — and it is the row that always
carries a figure. Under the block sits the sentence "Rakamlar sunucunundur; ekran
toplamaz." It is what earnings, invoice allocations, the icmal totals, the settlement,
the reimbursement, the provider's cari ekstre and a reconciliation run all use — the last
two emphasising Açık bakiye — and it never nests inside a table.

**Status tone is one map, in `@kapsora/api-client`** (`billing-tones.ts`): `invoiceTone`,
`batchTone`, `decisionTone`, `settlementTone`, `reimbursementTone`, each returning the
`Badge` tone union. Every app re-exports it rather than writing its own switch, so a
member and an operator looking at the same record see the same colour.

**A section navigation** is the tab-strip rule applied one level down: the main navigation
names the section once, `BillingNav` names its five lists — İcmal incelemesi · Ödeme
mutabakatları · Geri ödeme incelemesi · Günlük mutabakat · Dışa aktarım — as square
triggers on a bottom line, `aria-[current=page]` colouring the active one `primary`.

**The member tab bar** is three links — Ana sayfa · Ara · Rezervasyonlarım — each a drawn
20px icon over a 12px label, `aria-current` colouring the active one `primary`. Above 768px
the same three links are a tab strip in the header, square with a 2px underline, as the tab
strip rule requires.

## Patterns settled while building

These were decided against real screens, M2 through M7. They are here so the next screen
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

**A tab strip sits on a line, and it is one line at every width.** Triggers are square with
a 2px underline; a rounded corner under a thick border fights the line it sits on, which is
what the detector flagged the first time this component was written. The triggers neither
wrap nor shrink (`whitespace-nowrap`, `shrink-0`): on a phone the strip scrolls sideways
inside its own `overflow-x-auto`, so the active underline always sits on the rule instead of
on a second row floating above it. The main navigation names a section once, and the
section's sub-lists — the billing section's Günlük mutabakat and Dışa aktarım among them —
live in its strip, never as sidebar entries of their own.

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

**A block of figures ends on the one figure that is always there.** Hakediş, the invoice's
allocations, the icmal's totals, the settlement and the reimbursement all read back through
the same `<dl>`: capped at 24rem so the eye travels a short line, labels in a `max-content`
column, values right-aligned and monospaced under one another. Exactly one row is
emphasised — its label `font-medium`, its value semibold and a step larger — and it is the
row whose figure is always present: faturalanabilir, onaylanan, ödenecek. Where the figure
is still a dash, as an undecided reimbursement's onaylanan is, the emphasis is not spent on
it. Under every such block stands "Rakamlar sunucunundur; ekran toplamaz.", because an
operator who believes the screen is adding will audit the screen instead of the record, and
every money string on these surfaces came off the wire exactly as it is printed.

**Below 768 every list stacks, and a card that can be acted on carries its own command.**
The invoice, icmal, settlement, reimbursement, reconciliation-run and export lists, and the
cari ekstre's two tables, all become the same card — reference
and status badge on the first line, the provider or the hak sahibi on the second, the date
and the money on the third — and the icmal's review table stacks the same way, with the
decision badge and the approved amount on a line of their own and the reason beneath. Where
the wide table had a column of buttons, each stacked card carries its own "Fatura kararı".
One action bar under a list of cards would have to say which card it acts on; the card
already says it.

**A decision form opens by naming what it decides.** The first line inside the form is
"KPS2026000053 için karar · 600,00 TRY" — the reference it will change and the amount at
stake, the amount in monospace. The form is disclosed under a table of rows and the row
that opened it may already be out of view, so a form headed only "Karar" is a form whose
subject the operator has to hold in their head while typing a cut into it.

**One status is one colour, wherever it is shown.** The tone for every billing status lives
in the wire package (`@kapsora/api-client/billing-tones`), and the backoffice, the provider
portal and the member app import it rather than writing their own switch. A member reading
"Kısmen onaylandı" in green and an operator reading the same word in amber would be looking
at two different records as far as either of them can tell, and the map is small enough
that three copies of it would have drifted by the second status the domain adds.

**The member's three steps fill one receipt.** Talep, Makbuz, Hesap — three cards that
appear one at a time as the server accepts the one before, a ribbon of numbered pills above
them saying where the member is, and the receipt beside them collecting what has been agreed
so far. The receipt is the lodging component unchanged: pinned above the thumb on a phone,
a 22rem sticky column at ≥1024. The IBAN is typed once, into a field whose hint says it will
be, and from then on — in the member's own record and on the reviewer's screen alike — it is
"Hesap •••• 1326". An account number redisplayed in full is an account number read over a
shoulder, and neither side of this flow needs the other fourteen characters.

**What happened is its own card, and it is called "Ne oldu".** The settlement and the
reimbursement each end on an ordered list of the moments that produced them — karar, onay,
karşı imza, ödeme — with the timestamp on the right and the bank's own reference in
monospace where there is one. It is the sequence rule given a heading: someone opening a
settled record is asking when, by whom and against which reference, and a card that asks
the question in its title answers it before they have to look for the answer.

**A lookup the caller's grant cannot answer says "—", not "…" forever.** The name and
catalogue reads behind these screens retry nothing and settle to null on a refusal, so a
reviewer without `organization.read`, or without the catalogue grant, sees a dash where the
provider or the service name would be at the same moment everyone else sees the name. An
ellipsis that never resolves is worse than an admission of ignorance: it holds the reviewer
at the screen waiting for a fact their token will never produce.

**The card stays when its button cannot.** While an icmal still has an undecided invoice,
"İcmali karara bağla" keeps its heading and the sentence naming who may not press it — the
person who submitted the icmal, and above the threshold the person who gave the last
decision — and only the button is absent. Absent-not-disabled was never about hiding the
step: an operator who cannot see that the step exists cannot plan the day around it, and one
looking at a greyed button cannot find out why it is grey.

**A dashboard is rows of figures, not tiles.** The operations panel on the home page is
three cards — claims by status and claim aging side by side, and what is waiting across both,
capped so its figures stay near their labels — and each is a list of rows in the figures
block's grammar: the label, the count, the amount, both monospaced and right-aligned. The
list is the grid (`minmax(0,1fr) auto auto`) and every row sits on its tracks as a subgrid,
so the counts and the sums stand under one another down the card; a row with no amount
leaves its track empty rather than sliding its count into it.
There is no hero number and no "tümünü gör" under a card; a row is the way into its list,
when it has one. The as-of time stands beside the heading, because a figure without its
moment is a figure from nobody knows when. The claims under an invoice on the icmal review
are the same row with the count taken out: reference, then amount.

**A figure links to a list only when that list shows the same number.** The lists filter by
one status and no date window, so a claims row for a single status opens the claims list on
that status, the past-SLA row opens the worklist's overdue view, and every figure bounded by
an aging bucket, a due window or two statuses keeps its label as plain text. A link that
lands on a list counting a different set of rows teaches the operator that the dashboard and
the lists disagree, and then neither is trusted; no link is better than that one.

**An export is a state first and a download second.** A requested file is Sırada, then
Hazırlanıyor, then Hazır with its Geçerlilik sonu beside it — or Başarısız, or Süresi doldu —
a badge toned from one map, and İndir exists only on Hazır. Each row says what the file
covers in words: the provider and the period where it has them, "Kapsamdaki tüm kayıtlar"
where it has neither, so two settlement exports asked for a week apart are told apart without
opening either. The request form shows the period fields only for the kinds a period bounds —
the statement, the icmal, the reconciliation — because a date field the file ignores is a
promise the file breaks. The cari ekstre carries the same block under its own period fields:
Dışa aktar, the state as a `role="status"` line, then İndir.

**An audited download asks why before the link exists.** İndir on the exports list opens
İndirme amacı — the access purposes from the reference list, an optional reason, and the
sentence that the file is watermarked and the download is written to the audit log — on
every download, because every download is its own access. The link is minted with that
purpose and opened in a new tab, and afterwards the Filigran the file carries is printed
under the button, so the operator knows what the copy on their disk says about them. The
provider downloading its own cari ekstre is the one caller with a single possible purpose,
and it is sent without a question: a dialog with one choice is a click-through.

**A password field can be read before it is sent.** Every password input is the shared
`PasswordInput`: a drawn eye at the field's end, 20px at the 1.75 stroke, a real button whose
state is `aria-pressed` and whose name is the verb alone, "Göster" or "Gizle", because the
field's own label already says what is shown and a second control called "Parola" beside it
would be two things answering to one name. Showing changes the type and nothing else; the
value never leaves the input. The sign-in screens and the step-up dialog use it.

**The way in says what this is.** The sign-in page is two columns on a wide screen: the product
name as the page's one h1, "Hak ve fayda defteri" under it, one 16px sentence of what KAPSORA
keeps, and the three apps as a 1px-ruled definition list. The app this screen belongs to carries
"Buradasınız" in primary, and it is the row a narrow screen keeps when the other two fall away —
the member app's first viewport is a phone, and "which app am I in" may not be what drops. The
sign-in card sits beside it, is the only raised surface, and holds the h2. Both columns start at
the same top edge: the card grows with the demo list and the panel does not, so centring them
against each other would push the product name half a screen down. No gradient, no
illustration, no hero number: a ledger introduces itself by saying what it holds. The three apps
share the layout, so whichever door the single sign-in hands a person to looks like the one they
left.

**Help explains the unfamiliar, never the obvious.** A circled question drawn in the icon
stroke, 18px in a 24px box, muted (`fg-muted`, never `fg-subtle`: 2.97:1 on white is under
the 3:1 a control needs) until hovered or focused, sits after a label, a heading or a column
header — beside the heading, never inside it, because a button inside a heading is read as
part of its name — only where a reader would otherwise have to ask someone: a domain term (icmal,
mahsup, kontenjan), a figure the server computed, a field whose consequence its label does
not carry. It never sits on a control that names its own action, and it never carries a
refusal's reason — that stays inline in the product's own sentence, per the rule above. A
press, Enter, Space or a mouse hover opens a 20rem raised card with the term in the label
weight and one to three sentences in muted 14px; a finger has no hover, so the press is the
way in on a phone. A hover-opened card leaves focus where it was; a pressed one takes it.
The terms live once, under `help:terms`, shared by the three apps; a `FormField` takes the
mark through `help`, beside the label and never inside it, because a button inside a label
would answer to the field's name. The mark's own name is "Yardım: <term>", never
"Açıklama": that is the product's word for a claim line's description, and a guard that
looks for the description column must not find a help mark instead.

**Every page says what it is for.** The "?" in every app header — a ghost icon button
between the theme control and the account — opens a drawer from the right, 26rem and full
width below 640px, one scroll, closed by Escape, the backdrop or its own mark. It carries
the page's name, one paragraph of purpose, then its sections, actions and statuses as
1px-ruled definition lists in page order: what each shows, what each does and who may, what
each status means and what happens next. The content is data, not code: one `help` locale
file per app plus the shared terms, keyed by route through each app's `help.ts`, and a test
in each app fails when a route has no page written. When nothing is written for a route the
drawer shows the app's own help rather than nothing.

**A demo account list exists only where the demo does.** On a development server — with the
in-browser sample data or against a local API loaded by the demo seed — every sign-in screen
lists all the demo accounts under the form, grouped by the app they work in, this screen's own
app first (the fourth group is the account that works in two, named by both apps rather than
counted): the name, what the account does, the username in mono. The app is said once, in the
group heading — muted rather than subtle, because it is the meaning now and not a placeholder —
and never again on every row. A row names itself from its own content, so a reader hears the
person, the work and the username rather than a label that replaces them. One press signs in, and the single
sign-in takes the account to its app. The list is gated on the development build, so no built
bundle carries it and a real deployment can never show who may sign in.

**An amount not yet decided is "—", never "0,00".** An invoice whose icmal has not reached a
decision shows its onaylanan as a dash — on the cari ekstre, the icmal review and the
reimbursement list alike — and so does an ERP total that has not arrived. A zero is a
decision: printed for an undecided line it tells a provider their invoice was refused. Where
the server sends a real zero, the zero is printed.

**One sign-in, and the account goes where it has work.** The server lists, per tenant, the apps an
account's grants belong to (`apps`: a PERSON grant is the member app, an ORGANIZATION grant the
provider portal, a tenant-wide role the backoffice), and each app asks under its own name, so it
gets only its own grants. Whichever sign-in screen a person uses: one app is where they land, a
plain page load away if it is another app; several apps open the chooser — one card, "Nereden
devam etmek istersiniz?", the current app first with a primary "Devam et" and the others as quiet
"Aç" links; no app says so and offers only signing in as somebody else. An account with no work
in the app it opened by address meets the same card, forwarded when it has exactly one app.
Nothing of an app renders for an account with no work in it. The tenant picker lists only the
tenants that fit the app.

**A page the account may not see says so, and offers the way home.** When the load of a page
itself is refused (PERMISSION_DENIED, TENANT_ACCESS_DENIED) the alert reads "Bu sayfayı görme
yetkiniz yok.", one line says why and that a person who signed in with another account should
continue from home, and the only action is "Ana sayfaya dön" to the app's own root — never
"Yeniden dene", because retrying a refusal changes nothing. A refused action keeps "Bu işlem
için yetkiniz yok."; every other failure of a page keeps its retry.

**A tab notices when its account changed.** The session is shared by every tab and app. When a
tab comes back to the front it asks whose session it is: if another tab signed in as somebody
else it goes to that account's home with an info toast, and if the session ended it goes to
sign-in with one. A page left open never goes on showing one account's screen with another
account's session behind it.

**Your own file is readable, never decidable.** When a file's person is the reviewer's own
(`selfPersonId` on the tenant context — a reviewer who is also a member), the decision controls
are absent, not disabled, and one quiet note stands where they would be: "Bu dosya size ait.
Kendi dosyanıza karar veremezsiniz; başka bir değerlendirici karar vermeli." It uses the sunken
surface and muted text, never a warning colour: nothing is wrong, the decision is simply someone
else's. The server refuses it anyway (403 OWN_FILE_DECISION, audited), so the note is the
sentence, not the lock. The same note serves requests, claims, medical reports, refunds and
balance adjustments.

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
