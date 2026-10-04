# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

Three apps — backoffice, provider portal, member PWA — behind one Go API.

## Users

- **Payer / sponsor operator (backoffice)** — works for a bank, insurer or sponsor
  tenant. Defines programs and plans, manages organizations and members, reviews
  service requests and claims, reconciles statements, runs reports. Technically
  literate, works in long sessions on a desktop, cares about audit trails.
- **Reviewer / auditor (backoffice)** — reads and approves; narrower permissions, same
  screens with controls hidden. Needs to see status, history and who did what.
- **Configurator (backoffice)** — sets up what the operators then work inside: the
  service catalog, provider contracts and their price sheets, and the rules that decide
  documents, pre-approval and limits. A small number of trained people who work in the
  densest screens in the product and write CEL by hand. They get real tools rather than
  simplified ones, and a second person publishes what they wrote.
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

The backoffice Health entry leads to the operator's existing medical or financial review
work and record lists according to their active-tenant permissions. Financial staff enter
financial decisions without being offered clinical report access.

The backoffice Reports entry leads staff with `report.read` to recorded reconciliation runs
and export requests. Requesting or downloading exports remains a separate permission.

The main Billing entry opens a list the active role can read: invoice batches, settlements
or reimbursements. Approval, cancellation and payment controls require their own existing
grants; reading a record alone does not enable a financial command.

The provider portal opens the account's permitted work: clinical staff start a request,
billing staff open earnings, and reservation staff open the lodging desk. Navigation and
direct links use the same existing permissions. Switching the account or its tenant scope
clears cached records and open forms before the new context reads data.

Price Query supplies the provider and service labels needed to calculate a quote through
its existing pricing permission. Staff also need member access to choose a person. These
choices do not promise coverage, and calculating a quote moves no entitlement balance.

The wallet entry helps staff allowed to read members and entitlements find a person, open
their balances and inspect account movements without changing those balances.

The backoffice Security entry shows tenant-wide health data access records to staff with
`audit.read`, including denied attempts. It does not represent a general security event log.

Management's user directory is for tenant administrators to inspect who belongs to the
active tenant and which roles are assigned. It separates account and membership status,
validity and assigned-role validity. Unbounded validity and an empty validity period are
distinguished explicitly. Reading that directory does not enable provisioning, role changes
or settings changes, and provider-scoped administration does not imply access
to all tenant users.

Managers with a separate tenant-wide user-management grant can suspend an ACTIVE
membership from its detail. They confirm the current institution and choose a bounded
reason, then re-enter their password. The operation stops that membership's access on
the next protected request while preserving the global account, credentials, historical
roles and access to other institutions. Self-suspension is refused, and at least one
effective tenant manager must remain. Onboarding and restoration of access are separate
workflows.

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
