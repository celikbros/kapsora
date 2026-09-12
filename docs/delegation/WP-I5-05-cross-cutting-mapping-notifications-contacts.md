# WP-I5-05 · Cross-cutting: service→entitlement mapping, check candidates, ICD-10, notification wiring, contact details

| Field                      | Value                                                                                                                                                                                                                                                                    |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Milestone                  | M5 (plan increment I5)                                                                                                                                                                                                                                                   |
| Size                       | M                                                                                                                                                                                                                                                                        |
| Depends on                 | M2 (eligibility, entitlement definitions), M3 (catalog), M4 (requests, authorization, notifications)                                                                                                                                                                     |
| Runs in parallel with      | WP-I5-01                                                                                                                                                                                                                                                                 |
| Migration numbers assigned | `000035_service_entitlement_mapping_and_contacts.up.sql`                                                                                                                                                                                                                 |
| OpenAPI operations owned   | `listEntitlementMappings`, `putEntitlementMappings`, `listPersonContacts`, `putPersonContacts`; changes `EligibilityCheckResult` (adds `enrollmentCandidates`) and `ServiceRequest`/`WorkItem` (adds display names) — additive only                                        |
| Read first                 | docs/plan/ROADMAP.md, every "Open" entry dated 2026-09-04/05 — this package is those entries; `internal/benefit/eligibility/resolver.go` (`SERVICE_MAPPING_PENDING`); `internal/notification/application` (`Publish`, the safe-variable catalogue); WP-I4-01/02 command paths |

## 1. Goal

The screens of M4 found five things the platform had not decided, and each one makes the
provider's desk lie a little: every request "needs review" because no service is mapped to
an entitlement; a member with two enrollments cannot be asked about; nobody is told when a
request is decided; nobody can be told at all, because no member has a phone or an email
on record; and the worklist names a colleague by UUID. This package closes them.

## 2. Scope

### 2.1 Service → entitlement mapping (migration 000035)

`benefit.service_entitlement_mapping`: id, tenant_id, `plan_version_id`,
`service_definition_id` (composite FK into catalog), `entitlement_definition_id` (into the
plan version's definitions), `unit_factor numeric(20,6) NOT NULL DEFAULT 1` (one session
may draw two units), `valid_from`, `valid_to`. Unique `(tenant_id, plan_version_id,
service_definition_id)`; the mapping is part of the plan version and follows its
publishing: **a mapping on a PUBLISHED version is immutable** (WP-I2-02's rule), edited on a
draft version only. `putEntitlementMappings` replaces the set for a draft version.

The eligibility resolver uses the mapping before falling back to the context hints:
a service with a mapping is judged against its entitlement's balance; only an unmapped one
is `SERVICE_MAPPING_PENDING`. WP-I4-01's submit gate and WP-I5-04's claim pricing read the
same mapping. Seed the demo plan versions' mappings so the mock world and the seed both
turn "İnceleme gerekli" into "Uygun" for a mapped service.

### 2.2 The check names the enrollment candidates

When `ENROLLMENT_MULTIPLE` is the answer, `EligibilityCheckResult` gains
`enrollmentCandidates: [{ enrollmentId, planCode, planName, validFrom, validTo }]` — the
minimum a desk needs to ask again with `enrollmentId` in the request (add it to
`EligibilityCheckRequest` as optional, honoured when present). A provider still cannot list
enrollments; it can only choose among the ones the check already had to look at.

### 2.3 ICD-10 as a code system

Seed `catalog.code_system` `ICD10` with its 22 chapters as hierarchy and a small but real
set of values (enough for the demo and the tests: the chapters and ~60 codes incl. F00–F99
psychiatry, Z30–Z39 reproductive, and a handful of common outpatient codes), and mark the
chapters WP-I5-01 treats as sensitive (`F`, `O`/`Z30–Z39`, `Q`) with a `sensitive`
attribute on the value. Importing the full list is the customer's licence question
(v1.2 36.2); the shape is what this seeds.

### 2.4 Notifications actually go out

Wire `notification/application.Publish` through the outbox for: `service_request.decided`
(approved / partially approved / rejected / returned → the member and the provider),
`service_request.pending_document` (the provider), `authorization.approved` (the member),
`authorization.expiring` (3 days before `valid_to`, scheduler), `medical_report.decided`
and `claim.decided` (WP-I5-02/04 raise the events; this package registers the templates
and recipients). Templates for each event in `tr-TR`, EMAIL and INAPP, published by the
seed, using only the safe-variable catalogue (reference, status word, dates, provider and
program names, deep link). **A diagnosis, a description or a comment never reaches a
template** — the existing refusal test is extended with each new event's variables.

### 2.5 Contact details

`party.person_contact`: id, tenant_id, person_id, `channel` (`EMAIL`,`SMS`), `value_enc`
(envelope-encrypted like identifiers, WP-I2-01's `crypto.FieldCipher`), `value_masked`,
`verified_at` NULL, `primary` boolean, row_version. Unique one primary per `(person,
channel)`. `listPersonContacts` returns masked values only; `putPersonContacts` writes.
`notification`'s `RecipientAddress` port resolves PERSON+EMAIL/SMS from here; an unverified
contact is still an address (verification is M10's onboarding). Import (WP-I2-05) gains
optional `email`/`phone` columns in CSV_V1 that land here.

### 2.6 Names on the wire, once

Additive: `ServiceRequest` gains `personDisplayName`, `providerDisplayName`; `WorkItem`
gains `assigneeDisplayName`; the claim-conflict 409 (`WORK_ITEM_ALREADY_CLAIMED`) carries
`assigneeActorId` and `assigneeDisplayName` as extension members (extend `httpx.Problem`
with an optional `extensions map[string]any`, serialized flat). The screens that resolve
names row by row switch to the fields; the per-row hooks stay for anything else.

### 2.7 Permissions

`entitlement.mapping.manage` (new, granted with `plan.manage`); `member.contact.read` and
`member.contact.manage` (new, SENSITIVE; PROGRAM_MANAGER and the member's own portal later).
Two places, one commit, two-halves test.

## 3. Tests required

- A mapped service answers `ELIGIBLE` with the entitlement's balance; the same service on
  a plan without a mapping is still `SERVICE_MAPPING_PENDING`; a mapping cannot be changed
  on a published version.
- `ENROLLMENT_MULTIPLE` lists the candidates; asking again with one of them resolves.
- Every wired event produces exactly one message per recipient through the outbox, none
  on a rolled-back command, and the render refuses a diagnosis smuggled into a variable.
- A person with an SMS contact receives an SMS message row (still `SUPPRESSED
  CHANNEL_NOT_DELIVERABLE` — the provider is not chosen — but no longer `NO_ADDRESS`);
  a contact value never appears in plaintext in any table or log (WP-I2-01's scan).
- The request list and worklist screens' tests assert names from the wire, no per-row
  reads for them.

## 4. Acceptance criteria

- [ ] A provider's check can say "Uygun" for a mapped service, and a request for it can
      be auto-approved.
- [ ] A decided request tells the member and the provider, once, with nothing clinical.
- [ ] Members can have contact details, stored the way identifiers are.
- [ ] The worklist names the colleague who won.
- [ ] OpenAPI (additive only, `oasdiff` no breaking), generated code, Spectral clean;
      Turkish for every problem code; schema version 35.
