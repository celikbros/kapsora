# WP-I3-01 · Service catalog, external code systems and service code mapping

| Field                      | Value                                                                                                                                                                                                                                                                                                                                                      |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Milestone                  | M3 (plan increment I3)                                                                                                                                                                                                                                                                                                                                     |
| Size                       | M                                                                                                                                                                                                                                                                                                                                                          |
| Depends on                 | M2 (nothing beyond the platform and directory)                                                                                                                                                                                                                                                                                                             |
| Runs in parallel with      | WP-I3-02                                                                                                                                                                                                                                                                                                                                                   |
| Migration numbers assigned | `000019_catalog_code_systems.up.sql`                                                                                                                                                                                                                                                                                                                       |
| OpenAPI operations owned   | `listServiceCategories`, `createServiceCategory`, `getServiceCategory`, `patchServiceCategory`, `listServiceDefinitions`, `createServiceDefinition`, `getServiceDefinition`, `patchServiceDefinition`, `listCodeSystems`, `createCodeSystem`, `patchCodeSystem`, `listCodeValues`, `importCodeValues`, `listServiceCodeMappings`, `putServiceCodeMappings` |
| Read first                 | v1.2 9.7, 16.6 (catalog rows), migration `000006_catalog_and_service_request.up.sql`; ADR-015 problem codes                                                                                                                                                                                                                                                |

## 1. Goal

The catalog is the vocabulary every later module speaks: a request, a contract price, a
claim line and a rule all point at a `service_definition`. This package makes that
vocabulary manageable by an operator and mappable to the external code systems Turkish
health and social benefit work actually uses (SUT, ICD-10, HUV, and tenant-internal
systems), so a service the tenant calls `PHYSIO_SESSION` can be reported under the code a
payer or a regulator expects.

`catalog.service_category` and `catalog.service_definition` already exist (migration 000006) and are unused. This package adds their API, plus the code system tables.

## 2. Scope

### 2.1 Categories and definitions (API over existing tables)

- Categories form a tree through `parent_id`. Creating or re-parenting must refuse a cycle
  (walk the ancestors; 409 `CATEGORY_CYCLE`). Depth is capped at 6 (422 `VALIDATION_FAILED`
  on `parentId`, code `DEPTH_EXCEEDED`) so the tree stays navigable in a picker.
- Definitions carry `categoryId`, `fulfillmentMode`, `defaultUnitType`, `requiresProvider`
  and `active`. Codes are immutable after creation: a wrong code is deactivated and
  replaced, never renamed, because contracts and claims already point at it. `PATCH` that
  includes `code` answers 422 with field code `IMMUTABLE`.
- Deactivating a definition is allowed at any time and does not cascade; it only stops the
  definition being offered in new work. Listing takes `?active=`, `?categoryId=`,
  `?domain=`, `?q=` (trigram over name and code) and the standard keyset cursor.
- Both are `ETag`/`If-Match` merge-patch resources, `catalog.read` to read and
  `catalog.manage` to change.

### 2.2 Code systems and values (migration 000019)

`catalog.code_system`: id, tenant_id, code (`^[A-Z][A-Z0-9_]{1,39}$`), name, `version`
text, `authority` text (e.g. `SGK`, `WHO`, `TENANT`), `licensed` boolean (true when the
content may not be exported to third parties), `status` (`ACTIVE`/`INACTIVE`),
`valid_from` date, `valid_to` date NULL, row_version, timestamps. Unique
`(tenant_id, code, version)`. RLS, touch trigger.

`catalog.code_value`: id, tenant_id, code_system_id (composite FK), code text, display
text, `parent_code` text NULL, `valid_from` date NOT NULL, `valid_to` date NULL, `active`
boolean, `attributes` jsonb NOT NULL DEFAULT `'{}'`. Unique
`(tenant_id, code_system_id, code, valid_from)`. Index `(tenant_id, code_system_id, code)`
and a trigram index on `display` for search. A code system holds tens of thousands of
values, so this table is read-heavy and written only by import.

`catalog.service_code_mapping`: id, tenant_id, service_definition_id, code_system_id, code
text, `valid_from` date, `valid_to` date NULL, `primary` boolean. Composite FKs to both
parents. A definition may map to several systems, but only one row per
`(definition, system)` may be `primary` at a time and periods per
`(definition, system, code)` may not overlap — enforce with an exclusion constraint over
`daterange(valid_from, valid_to, '[)')` (409 `CODE_MAPPING_OVERLAP`).

### 2.3 Import of code values

`POST /api/v1/code-systems/{id}/values:import` (`catalog.manage`, `Idempotency-Key`
required) takes a JSON array of up to 5 000 values per call and upserts on
`(code, valid_from)`. It answers a per-row summary `{created, updated, skipped, errors[]}`
where an error names the array index and a field code; a call is all-or-nothing inside one
transaction, so a rejected batch leaves nothing behind. Larger systems are loaded by
repeating the call; the endpoint is idempotent under the same key.

There is no CSV path here: code systems are machine-to-machine data, and the member import
of WP-I2-05 already carries the operator-facing file workflow.

### 2.4 Reading a code as of a date

`GET /api/v1/code-systems/{id}/values?asOf=&q=&code=` returns the values valid on `asOf`
(default today), so a claim from last year resolves against the codes that were valid
then. This is the only correct way to read the table and the only reader this package
exposes.

## 3. Tests required

- dbtest: category cycle refused (direct and three levels deep), depth cap, code
  immutability, mapping overlap refused, mapping primary uniqueness, RLS isolation on
  every new table, `asOf` returning the historically correct value.
- Import: 5 000 rows in one call, a single bad row rolling the whole batch back, the same
  idempotency key replaying without a second write, an oversized batch answering 422.
- HTTP: ETag round trip, cursor paging stability while rows are inserted, permission
  denial for `catalog.read` without `catalog.manage` on every mutation.

## 4. Acceptance criteria

- [ ] A definition can be created, found by name or code, mapped to two code systems and
      deactivated; its code can never be changed.
- [ ] Overlapping mappings and category cycles are refused with named problem codes.
- [ ] A code system with 50 000 values imports in batches idempotently and reads back
      correctly for a past `asOf` date.
- [ ] OpenAPI, generated code, Spectral and `oasdiff` clean; schema version 19.
