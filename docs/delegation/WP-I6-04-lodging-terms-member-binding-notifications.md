# WP-I6-04 · Lodging terms on the contract, the member's person binding, tenant settings, notifications

| Field                      | Value                                                                                                                                                                                                                                                                 |
| -------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M6 (plan increment I6)                                                                                                                                                                                                                                                |
| Size                       | M                                                                                                                                                                                                                                                                     |
| Depends on                 | M1 (grants, scopes), M3 (contract versions, maker-checker publishing), WP-I4-05 and WP-I5-05 (notification events, templates, contacts, safe variables)                                                                                                                |
| Runs in parallel with      | WP-I6-01, WP-I6-02                                                                                                                                                                                                                                                    |
| Migration numbers assigned | `000039_lodging_terms_person_binding.up.sql`                                                                                                                                                                                                                          |
| OpenAPI operations owned   | `getContractVersionLodgingTerms`, `putContractVersionLodgingTerms`, `getMyPerson`; changes `TenantContext` (adds `personId`) — additive only                                                                                                                          |
| Read first                 | v1.2 9.13 (contribution, cancellation), 10.7, 11.11 (hold policy), 17.5 (tenant settings); WP-I1-02 (grants and scopes — the ORGANIZATION scope is the model), WP-I3-03 §2 (a PUBLISHED version is immutable), `internal/notification/application/events.go` (the safe-variable catalogue, `PublishNotification`), WP-I5-03 §2.2 (tenant settings in `platform.tenant_setting`) |

## 1. Goal

The four things WP-I6-01..03 lean on that the platform does not have yet: the contract's
lodging terms (what a cancellation costs, what a no-show costs, how a hold behaves), a
member account that knows which person it is, the tenant settings the vertical reads, and
the messages a booking sends. Small on their own; each blocks a screen without them.

## 2. Scope

### 2.1 Lodging terms on the contract version (migration 000039)

`contract.lodging_terms`: id, tenant_id, `contract_version_id` (composite FK; unique per
version), `free_cancellation_hours_before` int ≥ 0, `penalty_kind` (`NIGHTS`,`PERCENT`),
`penalty_nights` int NULL, `penalty_percent numeric(7,4)` NULL (exactly one of the two
non-null, CHECKed), `no_show_percent numeric(7,4)` (of the member amount; `100` means the
whole stay), `hold_minutes` NULL (overrides the tenant default for this provider),
`min_nights`, `max_nights`, `child_free_under_age` NULL, row_version. It follows the
version's publishing rule exactly as WP-I5-05's mapping does: **writable on a DRAFT version,
frozen by a trigger everywhere else.** `putContractVersionLodgingTerms` replaces the row for
a draft version.

### 2.2 The policy snapshot

`LodgingPolicySnapshot` (schema, reused by WP-I6-02 and WP-I6-03): the terms above plus
`contractVersionId`, `snapshotAt`, `timezone`. WP-I6-02 copies it onto the booking at
confirmation from the contract version in force on `check_in` for the property's provider
and the person's program; WP-I6-03 judges every cancellation and no-show by it. A booking
confirmed under a provider with no lodging terms is refused with 409
`LODGING_TERMS_MISSING` naming the contract version, never confirmed under a default.

### 2.3 Tenant settings

Keys in `platform.tenant_setting`, defaults in Go, all read by 01–03:
`accommodation.hold_minutes` (15), `accommodation.quote_ttl_minutes` (60),
`accommodation.checkin_early_hours` (6), `accommodation.checkin_late_hours` (24),
`accommodation.max_nights` (30), `accommodation.stepup_member_amount` (the member share
above which confirmation needs a step-up; `500`). Seeded for DEMO tenants.

### 2.4 The member's person binding

A member account acts for one person. `iam.access_grant.scope_type` gains `PERSON`
(beside `TENANT`, `ORGANIZATION`, `PROGRAM`, `PROVIDER_LOCATION`, `WORK_QUEUE`; `scope_id` is
the person's id, a composite FK into `party.person`); `TenantContext` gains `personId` (the person
of the caller's PERSON scope, null for every other actor) and `getMyPerson` answers the
member's own person record (the masked identifier, the enrollments, the contacts). Every
member-side read and command in 01–03 (`searchAvailability` for oneself, `createHold`,
`confirmBooking`, `cancelBooking`, `joinWaitlist`, `listBookings`) resolves the person from
the scope on the server and refuses a body that names another person with 403
`PERSON_SCOPE` — a member cannot hold a room for their neighbour by editing a request.
`cmd/seed` binds the demo member accounts; the mock's `member.a` account is bound to the
demo family's principal.

### 2.5 Notifications that go out

Events, templates (`tr-TR`, EMAIL and INAPP, published by the seed, safe variables only),
recipients: `booking.held` (member: the room, the countdown's end, a deep link),
`booking.confirmed` (member and the property: reference, dates, property name, member
amount, deep link — **never the voucher token**), `booking.pending_approval` (member),
`booking.cancelled` (member and property, with the fee), `booking.reminder` (scheduler,
24 hours before `check_in` in the property's zone, once), `booking.no_show_reported`
(member), `booking.offered` (waitlist; member, with the offer's expiry). `property_name`
joins the safe-variable catalogue (a name is a label, not a sentence — the same limits as
`provider_name`); a migration repeats the CHECK. The refusal test is extended to every new
event's variables.

### 2.6 Permissions

`contract.lodging_terms.manage` (new, NORMAL; granted with `contract.manage`). Two places,
one commit, two-halves test. `PERSON` scope grants are created by the seed and by M10's
onboarding, never by a member.

## 3. Tests required

- Lodging terms are writable on a draft and refused on a published version by the trigger
  with the service bypassed; exactly one penalty kind is CHECKed.
- A member with a PERSON scope: `TenantContext.personId` is set, a body naming another
  person is 403, and a member with no binding is refused every member command with a
  problem that says onboarding is missing.
- Every new event produces exactly one message per recipient per channel through the
  outbox, none on a rolled-back command; the renderer refuses a voucher token or an
  identifier smuggled into a variable; the reminder job sends once and only once across
  repeated runs.
- Tenant settings: a tenant with none gets the defaults; a tenant's own values are read.

## 4. Acceptance criteria

- [ ] A booking can be confirmed only under a contract version that says what a
      cancellation and a no-show cost, and that answer is frozen on the booking.
- [ ] A member account acts for exactly one person, enforced on the server.
- [ ] A held, confirmed, cancelled, reminded and offered booking tells the member once,
      with nothing in the message that could be misused.
- [ ] OpenAPI (additive), generated code, Spectral and `oasdiff` clean; Turkish for every
      problem code; schema version 39.
