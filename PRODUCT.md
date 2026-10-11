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

KAPSORA's primary purpose is health benefit and service orchestration: a payer defines
members' health entitlements, and sponsors, members, clinics and hospitals follow a care
request from eligibility and authorization through outpatient or inpatient care, medical
reports, claim review and provider settlement. Lodging and other benefits are supporting,
secondary programs. Finance and administration support these health journeys; integrations
and performance work follow completion of the health workflows that users need.

It replaces spreadsheets, e-mail approvals and per-provider portals with one auditable
record shared by payer, provider and member, with tenant isolation enforced in the
database (RLS), not just in the UI.

The backoffice Health entry leads to the operator's existing medical or financial review
work and record lists according to their active-tenant permissions. Financial staff enter
financial decisions without being offered clinical report access.

The backoffice Reports entry leads staff with `report.read` to recorded reconciliation runs
and export requests. Requesting or downloading exports remains a separate permission.

Long provider hospital case lists, lodging arrival and departure lists, member booking
history, and backoffice medical report review lists let people request older records as
needed. If an older page cannot be loaded, the records already shown remain available and
the page can be retried.

A hospital case shows report, inpatient-stay and permitted claim read failures in their own
chapters with retry. Loaded records remain visible after a failed refresh. Admission is
available only after a successful stay read establishes no open stay; an interrupted
admission form stays closed when the lookup recovers, until staff explicitly reopen it.

Clinical diagnosis editing starts from a successfully loaded diagnosis set. A pending or
failed read cannot open a blank replacement form; retry restores the existing choices.
A fresh read or a case change closes the old editor, and a successful save leaves it closed.
Case navigation clears local forms and diagnosis choices; admission choices belong only to
the current case. Financial projections do not request or offer clinical diagnoses.

Inpatient segment drafts and extension, discharge and cancellation forms belong to the
selected stay. Changing stays clears those drafts; refreshed status or version changes
close an outdated command form. Staff explicitly reopen an eligible action on the current
record, and a delayed result from a previous stay does not change the new stay's form.

Medical report navigation also clears the previous report's header, service coverage and
document form drafts. A correction response may open its new version only while staff are
still viewing the report they corrected; a late response does not move them away from
another report.
Within the same report, saving service coverage preserves an unsaved header draft; its
later save uses the refreshed report version.

Backoffice approval and rejection decisions belong to the selected report. A report version,
status or eligibility change, or a failed report read, closes the open decision draft.
When the read recovers, the decision stays closed until the reviewer explicitly starts a fresh
decision. A delayed command result cannot show feedback on another report; normal success
feedback remains available while the reviewer stays on the report being decided.

Report version history shows loading and retryable errors while retaining known versions.
A correction is offered only after a successful, settled history read establishes that no
later version exists, under the existing clinical/status/permission rules.

The member home names the next stay or says there is no planned stay when its booking results
are complete. When older bookings remain to be read, it gives neutral guidance and a direct
route to all bookings instead of presenting the first page as the full history.

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

An authorized manager can invite a recipient by email, review a masked
recipient and delivery state, or cancel a pending invitation. The manager never sees the
invitation code or whether the address already belongs to an account. The recipient signs
in normally to accept with their displayed account, or pastes the emailed code on the fixed
page to review the institution and create an account with an explicit consent and chosen
password. A new membership starts without roles and shows that access awaits assignment.
New-account acceptance displays a generated login handle without signing in; the recipient
retains it and uses ordinary login. For 24 hours, a lost acceptance result can be recovered
privately with both the code and current password.

A tenant role manager can give an ACTIVE human membership that has no current or future
access one supported system role. The confirmation names the person, institution, role,
application and access; provider roles also name one provider organization. Sensitive
access is stated explicitly. Password confirmation is required. Removing the supported
current role ends its access while retaining the account, membership and assignment
history. Privileged roles, person binding and combined-role assignments have separate
approval and identity requirements. A waiting user can refresh their access in any app
after assignment without signing in again.

Privileged tenant roles use a separate request and approval flow. A manager proposes
assignment or removal; another currently authorized person explicitly approves or rejects
the recorded access. The requester, approver and target must be different people.
Submitting a request leaves access unchanged. The Management queue shows pending requests
and closed history; detail names the person, institution, role duties and recorded
permissions. The requester can withdraw a pending request. Missing second-approver access
is explained plainly, and the request remains pending until an eligible person can decide.
Approval applies access immediately; rejection or withdrawal closes the request without
changing access. Tenant administration does not confer clinical access automatically.

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
