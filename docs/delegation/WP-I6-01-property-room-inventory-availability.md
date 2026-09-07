# WP-I6-01 · Property, room type, daily inventory, availability search and the contribution quote

| Field                      | Value                                                                                                                                                                                                        |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Milestone                  | M6 (plan increment I6)                                                                                                                                                                                       |
| Size                       | M                                                                                                                                                                                                            |
| Depends on                 | M2 (eligibility, entitlement units incl. `NIGHT`), M3 (catalog, provider network, contracts and price items, pricing quote), WP-I5-05 (service→entitlement mapping)                                          |
| Runs in parallel with      | WP-I6-04                                                                                                                                                                                                     |
| Migration numbers assigned | `000040_accommodation_property_inventory.up.sql` (renumbered from 000036 on 2026-09-06: WP-I6-04 landed first as 000039, and golang-migrate never applies a lower number to a database already past it)                                                                                                                                                             |
| OpenAPI operations owned   | `listProperties`, `createProperty`, `getProperty`, `patchProperty`, `listRoomTypes`, `createRoomType`, `patchRoomType`, `getRoomTypeInventory`, `putRoomTypeInventory`, `searchAvailability`                 |
| Read first                 | v1.2 9.13, 10.6 steps 1–3 and 5, 11.11, 16.7 (`accommodation.property`, `room_type`, `inventory_day`), 35.2, 36.3; WP-I3-02 (provider, location), WP-I3-03 (price items with validity), WP-I3-05 (`Calculate`, the one rounding), WP-I5-05 §2.1 (mapping) |

## 1. Goal

A provider says what it has — properties, room types, and how many rooms of each it will
give this payer on each night — and a member asks what is free between two dates and what
it would cost them. Two rules carry the package: **the daily inventory row is the only
place a room is counted, and `held + confirmed ≤ capacity` is a CHECK the database keeps**;
and **the member's share is the pricing ladder's answer, computed once on the server**, shown
before anybody commits to anything.

## 2. Scope

### 2.1 Schema (migration 000040, `accommodation` schema)

`accommodation.property`: id, tenant_id, `provider_organization_id` (composite FK into
`directory.tenant_organization`), `location_id` NULL (composite FK into `provider.location`),
`code`, `name`, `property_type` (`HOTEL`,`RESORT`,`GUESTHOUSE`,`SOCIAL_FACILITY`,`OTHER`),
`timezone` (IANA name; the property's night starts and ends in its own zone), `city`,
`region_code`, `amenities jsonb` (a closed list of keys validated in the domain), `cost_center`
NULL (an internal social facility bills a cost center rather than a provider, v1.2 9.13),
`status` (`ACTIVE`,`INACTIVE`), row_version. Unique `(tenant_id, provider_organization_id,
code)`.

`accommodation.room_type`: id, tenant_id, `property_id`, `code`, `name`, `max_adults`,
`max_children`, `max_occupancy` CHECK `≥ max_adults`, `attributes jsonb`,
`service_definition_id` (composite FK into `catalog.service_definition`; the `NIGHT`-unit
service this room is priced and entitled as — one room type, one service), `status`,
row_version. Unique `(tenant_id, property_id, code)`.

`accommodation.inventory_day`: `tenant_id`, `room_type_id`, `stay_date`, `capacity int ≥ 0`,
`held int ≥ 0`, `confirmed int ≥ 0`, `updated_at`, `row_version`. **Primary key
`(tenant_id, room_type_id, stay_date)`; CHECK `held + confirmed <= capacity`.** This is the
hot row of the vertical: WP-I6-02 locks it `FOR UPDATE` in `stay_date` order, and a
capacity lowered under an existing hold is refused by the CHECK, not by a service.

RLS on all three, `attach_touch_row`, composite tenant FKs, `grant_app_schema_usage('accommodation')`.
`accommodation.*` permissions already seeded (`accommodation.inventory.manage`,
`accommodation.booking.create`, `accommodation.booking.manage`); this package adds
`accommodation.property.read` (NORMAL; MEMBER, PROVIDER_RESERVATION, PROGRAM_MANAGER,
SPONSOR_HR) — two places, one commit, two-halves test.

### 2.2 Inventory as an allotment

`putRoomTypeInventory` sets `capacity` for a date range in one transaction (a provider
opens a season with one call), never touching `held`/`confirmed`; a date whose CHECK would
fail is refused with 409 `INVENTORY_BELOW_COMMITMENT` naming the first offending date.
`getRoomTypeInventory` answers a range as rows with the three counters and
`available = capacity − held − confirmed` computed on the server, so no screen subtracts.

### 2.3 Availability search

`searchAvailability` (`POST /api/v1/accommodation/availability/search`): `checkIn`,
`checkOut` (dates, `checkOut > checkIn`, ≤ 30 nights), `adults`, `children`, and either
`propertyId` or `regionCode`; `personId` optional for a desk acting for a member, taken
from the caller's own person binding for a member (see WP-I6-04 §2.4). It answers, per
room type: the property, the room type with its occupancy, `nights` (the server's count of
`[checkIn, checkOut)`), `available` (the minimum of the daily availability over the range —
a room type missing an inventory row on any night is not available), and the **quote**:
`nightlyAmounts[]`, `totalAmount`, `payerAmount`, `memberAmount`, `currencyCode`, computed
through WP-I3-05's calculator for the room type's service on each stay date under the
person's enrollment and the provider's contract, then summed on the server once — exact
decimal strings on the wire, nothing rounded twice. Where a night cannot be priced the room
type carries `quote: null` and a reason code rather than a guess. The eligibility half:
`entitlement` with the remaining nights of the plan's `NIGHT` entitlement (through the
mapping of WP-I5-05) and `eligible: boolean` for the whole range.

Every search is an eligibility evaluation on the record (WP-I2-04's immutable evaluation),
so "what did the system show them" is answerable later.

### 2.4 The mock

`accommodation-handlers.ts` with the three aggregates, a seeded world (two properties, four
room types, ninety days of inventory with a full weekend, a season with two price levels),
the search and the quote through the mock's own pricing helpers.

## 3. Tests required

- The CHECK: a capacity lowered below `held + confirmed` is refused by the database with the
  application layer bypassed.
- Night arithmetic on the boundaries: one night, a month end, a DST change in the property's
  zone, `checkOut == checkIn` refused, thirty-one nights refused.
- A room type with capacity on every night but one is unavailable for the range.
- The quote equals the calculator's per-night answers summed once; `payer + member == total`
  on every search; a night with a price tie answers `quote: null` with `PRICE_AMBIGUOUS`.
- A member searching sees only ACTIVE properties of providers under a contract with their
  program; a provider's inventory call for another provider's room type is 404.
- Two-halves test for `accommodation.property.read`.

## 4. Acceptance criteria

- [ ] A provider can open a season's allotment in one call and the database refuses any
      capacity below what is already held or confirmed.
- [ ] A member sees, before holding anything, what is free and what they would pay.
- [ ] OpenAPI (additive), generated code, Spectral and `oasdiff` clean; Turkish for every
      problem code; schema version 40.
