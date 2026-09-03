# Product

<!-- impeccable:product-schema 1 -->

## Platform

web (three apps: backoffice, provider portal, member PWA; one Go API behind them)

## Users

- **Payer / sponsor operator (backoffice)** — works for a bank, insurer or sponsor
  tenant. Defines programs and plans, manages organizations and members, reviews
  service requests and claims, reconciles statements, runs reports. Technically
  literate, works in long sessions on a desktop, cares about audit trails.
- **Reviewer / auditor (backoffice)** — reads and approves; narrower permissions, same
  screens with controls hidden. Needs to see status, history and who did what.
- **Provider staff (provider portal)** — clinics, hospitals, hotels and other service
  providers. Fast entry: eligibility check, service request, claim, statement. Often on
  a shared desk computer between patients or guests.
- **Member (member PWA)** — the entitled person. Checks their entitlements, finds a
  provider, books, applies, uploads a document, reads notifications. Mobile first, low
  patience, Turkish.

No role leads; the backoffice ships first (I1), the provider portal from I4, the member
app from I6. Every user acts inside exactly one tenant at a time and must always be able
to see which one.

## Product Purpose

KAPSORA manages entitlement programs end to end: a payer defines what its members are
entitled to (health sessions, lodging nights, allowances), providers deliver those
services, and the system checks eligibility, records requests and claims, settles with
providers, issues fiscal documents through the GİB e-Belge integrator and posts to the
accounting ledger.

It replaces spreadsheets, e-mail approvals and per-provider portals with one auditable
record shared by payer, provider and member, with tenant isolation enforced in the
database (RLS), not just in the UI.

Success is a payer operator onboarding a provider, a provider checking eligibility and
submitting a request, and a member seeing the result, without anyone leaving the
product or asking whose data they are looking at.

## Aesthetic direction

The Ledger (see `DESIGN.md`): plain, exact, institutional. Dense tables and forms in the
backoffice; a lighter, task-first layout in the provider portal; a calm reading
experience on mobile for members. Turkish copy throughout, formal "siz", short
sentences, error messages that say what to do next.

## Brand personality

Trustworthy, precise, unhurried. It never celebrates; it confirms. It never hides a
state; a pending, suspended or conflicting record is visible at a glance. It respects
the operator's time: keyboard first, no modal chains, no decorative delay.

## Constraints

- Contract-first: screens are built on `api/openapi/kapsora-v1.yaml`; types are
  generated, not hand-written.
- Security: no personal data or tokens in browser storage; CSRF token in memory;
  `Cache-Control: no-store` on authenticated pages; CSP without inline scripts.
- Accessibility: WCAG 2.2 AA target; keyboard navigation and screen-reader labels are
  tested, not assumed.
- Localisation: Turkish complete, English skeleton; dates in the tenant time zone,
  money with ISO currency codes.
- No containers anywhere (ADR-021); the frontend is static files served by nginx or
  Caddy next to the Go API.
