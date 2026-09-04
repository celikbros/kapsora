# WP-I3-02 · Provider network: profiles, locations, capabilities and practitioners

| Field                      | Value                                                                                                                                                                                                                                                                                                               |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M3 (plan increment I3)                                                                                                                                                                                                                                                                                              |
| Size                       | L                                                                                                                                                                                                                                                                                                                   |
| Depends on                 | M1 (`directory.tenant_organization`), WP-I3-01 (capabilities point at catalog rows)                                                                                                                                                                                                                                 |
| Runs in parallel with      | WP-I3-01                                                                                                                                                                                                                                                                                                            |
| Migration numbers assigned | `000020_provider_network.up.sql`                                                                                                                                                                                                                                                                                    |
| OpenAPI operations owned   | `listProviders`, `createProvider`, `getProvider`, `patchProvider`, `listProviderLocations`, `createProviderLocation`, `patchProviderLocation`, `listProviderCapabilities`, `putProviderCapabilities`, `listPractitioners`, `createPractitioner`, `patchPractitioner`, `putPractitionerLocations`, `searchProviders` |
| Read first                 | v1.2 9.3, 16.6 (provider rows), WP-I1-03 organizations, WP-I2-01 for the encryption and blind-index helpers; ADR-015                                                                                                                                                                                                |

## 1. Goal

A contract is signed with a provider, a service is delivered at a location, and a report is
signed by a practitioner. Until those three exist, a contract price has nothing to attach
to and an eligibility answer cannot say "yes, at this hospital". This package builds the
provider side of the network on top of the organization directory M1 already delivered:
a provider is a `directory.tenant_organization` with the PROVIDER role, given a profile,
locations, what it can do and who works there.

## 2. Scope

### 2.1 Schema (migration 000020, new `provider` schema)

`provider.provider_profile`: id, tenant_id, `tenant_organization_id` (composite FK, unique
per tenant — one profile per organization relationship), `provider_type`
(`HOSPITAL`,`CLINIC`,`PHARMACY`,`LABORATORY`,`IMAGING`,`HOTEL`,`AGENCY`,`TRANSPORT`,`EDUCATION`,`SPORT`,`OTHER`),
`status` (`PENDING`,`ACTIVE`,`SUSPENDED`,`TERMINATED`), `network_tier` text NULL (the
tenant's own tiering, e.g. `A`/`B`), `contracted_from` date, `contracted_to` date NULL,
row_version, timestamps. Status transitions are explicit commands, never a status PATCH:
PENDING→ACTIVE, ACTIVE→SUSPENDED, SUSPENDED→ACTIVE, any→TERMINATED. Terminating is final.

`provider.location`: id, tenant_id, provider_profile_id, `code`, `name`, `address_line`,
`district`, `city`, `country_code` (ISO-3166-1 alpha-2), `postal_code`, `latitude`,
`longitude` (both `numeric(9,6)` NULL — plain columns, no PostGIS; ADR to be written if
geo search ever needs it), `timezone` (IANA, default `Europe/Istanbul`), `phone`,
`status` (`ACTIVE`,`SUSPENDED`,`CLOSED`), row_version, timestamps. Unique
`(tenant_id, provider_profile_id, code)`.

`provider.capability`: id, tenant_id, location_id, exactly one of `service_definition_id`
or `service_category_id` (CHECK: `num_nonnulls(...) = 1`), `valid_from` date NOT NULL,
`valid_to` date NULL, `notes` text. Composite FKs into `catalog`. Exclusion constraint: no
two rows for the same `(tenant_id, location_id, service_definition_id)` or the same
`(tenant_id, location_id, service_category_id)` may overlap in
`daterange(valid_from, valid_to, '[)')` (409 `CAPABILITY_OVERLAP`). A category capability
means "everything under this category, including categories added later" — resolution
walks the tree at read time, so a new definition under a covered category is covered
without touching the provider's rows.

`provider.practitioner`: id, tenant_id, provider_profile_id, `person_id` NULL (composite FK
into `party.person` when the practitioner is also a member), `full_name`, `title`,
`branch_code` text NULL (specialty; a `catalog.code_value` when the tenant maintains one),
`registration_authority` text (e.g. `TTB`, `SB`), `registration_number_encrypted` bytea,
`registration_number_index` bytea, `registration_number_masked` text, `valid_from`,
`valid_to`, `status`, row_version, timestamps. The registration number is a professional
identity number and is treated exactly like a TCKN: encrypted with `crypto.FieldCipher`,
searched through `crypto.BlindIndexer.TenantIndex` under a new purpose
`crypto.PurposePractitionerRegistration`, never stored, logged or returned in plaintext.
Unique `(tenant_id, registration_authority, registration_number_index)`.

`provider.practitioner_location`: id, tenant_id, practitioner_id, location_id, `role`
(`ATTENDING`,`CONSULTANT`,`TECHNICIAN`,`ADMINISTRATIVE`), `valid_from`, `valid_to` NULL.
Exclusion constraint on overlapping periods per `(practitioner, location, role)`.

Every table: RLS through `platform.enable_tenant_rls`, touch trigger, composite unique
`(tenant_id, id)` so children can use composite FKs.

### 2.2 API shape

- Providers, locations and practitioners are `ETag`/`If-Match` merge-patch resources.
  Permissions `provider.read`, `provider.manage` and `provider.practitioner.manage` are
  already seeded (migration 000008); practitioner writes take the third one because a
  registration number is sensitive.
- Capabilities and practitioner-location assignments are replaced as a set
  (`PUT .../capabilities`, `PUT .../locations`) rather than patched row by row, because
  what matters is the resulting coverage and a set replacement makes the overlap check one
  decision instead of many. The response is the resulting list.
- `GET /api/v1/providers/search?serviceDefinitionId=&city=&asOf=&q=` answers the question
  the rest of the system actually asks: which locations can deliver this service on this
  date. It resolves category capabilities through the tree, filters by provider and
  location status, and pages with the standard cursor. This is the endpoint eligibility
  (I3-05) and service requests (I4) call.
- A provider-scoped actor (access grant of scope type ORGANIZATION) sees only its own
  provider through every one of these endpoints; the filter is applied in the repository,
  not the handler, so no route can forget it.

### 2.3 What this package does not do

No provider portal screens (WP-I3-06 covers backoffice only; the provider portal starts in
I4), no document upload for licences (I4 owns the document pipeline — the profile carries
dates only), and no shared cross-tenant provider network (v1.2 9.3 lists it as future
work; the composite tenant key leaves the door open).

## 3. Tests required

- dbtest: capability overlap refused for both definition and category rows; category
  capability covering a definition added afterwards; practitioner registration number
  round trip through cipher and blind index, with a plaintext search hit and a
  cross-tenant miss; practitioner-location overlap refused; RLS on all five tables.
- Transitions: every legal provider status move accepted, every illegal one refused with
  `PROVIDER_TRANSITION_INVALID`; terminated is terminal.
- Search: a location with a category capability and a location with a definition
  capability both returned; a location whose capability expired before `asOf` excluded; a
  suspended provider excluded; paging stable.
- Scope: a provider-scoped actor listing providers sees exactly one; requesting another
  provider's location answers 404, not 403, so the existence of another provider does not
  leak.

## 4. Acceptance criteria

- [ ] A provider with two locations, a category capability and three practitioners can be
      created, searched by service and city, suspended and terminated.
- [ ] No plaintext registration number exists in the database, the logs or any response;
      search by the number still works and is audited as an identifier access.
- [ ] Overlapping capabilities and practitioner assignments are refused.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean; schema version 20.
