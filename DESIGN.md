---
name: KAPSORA
description: A benefits ledger for banks, insurers and providers — quiet, exact, and always able to say which tenant you are acting for.
colors:
  bg: "#F7F8FA"
  surface: "#FFFFFF"
  surface-2: "#EEF1F5"
  surface-3: "#DFE4EB"
  border: "#DCE2EA"
  border-strong: "#B9C3D0"
  fg: "#14202E"
  fg-muted: "#4B5A6B"
  fg-subtle: "#8A97A8"
  primary: "#1F5FA8"
  primary-hover: "#184C88"
  primary-fg: "#FFFFFF"
  primary-soft: "#E3EDF8"
  success: "#1E7F4F"
  success-soft: "#E4F4EB"
  warning: "#9A6700"
  warning-soft: "#FBF1D6"
  danger: "#B42318"
  danger-soft: "#FBE7E4"
  info: "#1F5FA8"
  info-soft: "#E3EDF8"
  focus: "#3B82F6"
typography:
  display:
    fontFamily: "var(--font-sans)"
    fontSize: "1.75rem"
    fontWeight: 600
    lineHeight: "2.25rem"
    letterSpacing: "-0.01em"
  headline:
    fontFamily: "var(--font-sans)"
    fontSize: "1.5rem"
    fontWeight: 600
    lineHeight: "2rem"
    letterSpacing: "-0.01em"
  title:
    fontFamily: "var(--font-sans)"
    fontSize: "1.125rem"
    fontWeight: 600
    lineHeight: "1.75rem"
    letterSpacing: "normal"
  body:
    fontFamily: "var(--font-sans)"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: "1.25rem"
    letterSpacing: "normal"
  label:
    fontFamily: "var(--font-sans)"
    fontSize: "0.875rem"
    fontWeight: 500
    lineHeight: "1.25rem"
    letterSpacing: "normal"
  caption:
    fontFamily: "var(--font-sans)"
    fontSize: "0.75rem"
    fontWeight: 500
    lineHeight: "1rem"
    letterSpacing: "0.02em"
  mono:
    fontFamily: "var(--font-mono)"
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: "1.25rem"
    letterSpacing: "normal"
rounded:
  sm: "0.25rem"
  md: "0.375rem"
  lg: "0.5rem"
  xl: "0.75rem"
  full: "9999px"
spacing:
  xs: "0.25rem"
  sm: "0.5rem"
  md: "0.75rem"
  lg: "1rem"
  xl: "1.5rem"
  2xl: "2rem"
components:
  button-primary:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.primary-fg}"
    typography: "{typography.label}"
    rounded: "{rounded.md}"
    padding: "0.5rem 1rem"
  button-primary-hover:
    backgroundColor: "{colors.primary-hover}"
    textColor: "{colors.primary-fg}"
  button-secondary:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.fg}"
    borderColor: "{colors.border-strong}"
    typography: "{typography.label}"
    rounded: "{rounded.md}"
    padding: "0.5rem 1rem"
  button-secondary-hover:
    backgroundColor: "{colors.surface-2}"
    textColor: "{colors.fg}"
  button-danger:
    backgroundColor: "{colors.danger}"
    textColor: "{colors.primary-fg}"
    typography: "{typography.label}"
    rounded: "{rounded.md}"
    padding: "0.5rem 1rem"
  card:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.fg}"
    borderColor: "{colors.border}"
    rounded: "{rounded.lg}"
    padding: "1.5rem"
  input:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.fg}"
    borderColor: "{colors.border-strong}"
    typography: "{typography.body}"
    rounded: "{rounded.md}"
    padding: "0.5rem 0.75rem"
  table-header:
    backgroundColor: "{colors.surface-2}"
    textColor: "{colors.fg-muted}"
    typography: "{typography.caption}"
    padding: "0.5rem 0.75rem"
  badge:
    backgroundColor: "{colors.surface-2}"
    textColor: "{colors.fg-muted}"
    typography: "{typography.caption}"
    rounded: "{rounded.full}"
    padding: "0.125rem 0.5rem"
  tenant-dot:
    rounded: "{rounded.full}"
    size: "0.625rem"
  nav-item:
    backgroundColor: "{colors.bg}"
    textColor: "{colors.fg}"
    typography: "{typography.label}"
    rounded: "{rounded.md}"
    padding: "0.5rem 0.75rem"
  nav-item-active:
    backgroundColor: "{colors.primary-soft}"
    textColor: "{colors.primary-hover}"
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

**Modes:** the backoffice is *Operate* (dense lists, forms, review). The provider portal
is *Operate* with a fast-entry bias. The member app is *Read* first, *Experience* only
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

| Role | Light | Dark | Use |
|---|---|---|---|
| `bg` | `#F7F8FA` | `#0F1620` | Page ground and sidebar. Never a card. |
| `surface` | `#FFFFFF` | `#172130` | Cards, panels, inputs, menus, header. |
| `surface-2` | `#EEF1F5` | `#1F2B3D` | Table headers, hover fills, code chips. |
| `surface-3` | `#DFE4EB` | `#2A3850` | Deepest tonal step; rare. |
| `border` | `#DCE2EA` | `#2A3850` | Default separation. |
| `border-strong` | `#B9C3D0` | `#3B4C66` | Interactive edges: inputs, secondary buttons. |
| `fg` | `#14202E` | `#EEF2F7` | Primary text. |
| `fg-muted` | `#4B5A6B` | `#A6B3C4` | Secondary text, labels, captions. |
| `fg-subtle` | `#8A97A8` | `#6E7E93` | Placeholders, disabled. Never for meaning. |
| `primary` | `#1F5FA8` | `#6FA3E8` | The one call to action, active nav, links. |
| `primary-soft` | `#E3EDF8` | `#1E3552` | Active nav background, selected rows. |
| `success` / `warning` / `danger` / `info` | `#1E7F4F` / `#9A6700` / `#B42318` / `#1F5FA8` | `#4CC38A` / `#E5B546` / `#F0736A` / `#6FA3E8` | Status only: badges, alerts, toasts. Each has a `-soft` fill. |
| `focus` | `#3B82F6` | `#3B82F6` | The focus ring, 2px, offset 2px, everywhere. |

Rules: one primary action per screen; status colours never decorate; text on `-soft`
fills uses the strong colour of the same family and stays above AA contrast; the tenant
colour is never used for text.

## Typography

Six roles, one family. Headlines are semibold and slightly tight; body and labels are
regular/medium at 14px, captions at 12px uppercase-free (uppercase only for table
headers, tracked 0.02em). Monospace is for identifiers, codes and trace ids only.

## Layout

Backoffice: 56px header, 256px sidebar on ≥768px, content padding 24px, max content
width unconstrained (tables need it). Cards separate concerns, never nest. Tables are
the primary reading surface: 12px uppercase headers on `surface-2`, 8px/12px cell
padding, hover fill `primary-soft` at 40%, no zebra stripes.

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

## Motion

Toasts slide up 160ms ease-out; nothing else animates. `prefers-reduced-motion`
disables the slide and the spinner rotation.

## Accessibility

WCAG 2.2 AA: skip link, `<main>` landmark, `aria-current` on navigation, labelled
controls, `role="alert"` on errors, visible focus ring, Escape closes dialogs and
menus, keyboard-only flows covered by tests.
