# WP-I2-01 · Persons: registry, encrypted identifiers, blind-index search, relationships, sponsor memberships

> **Delivered in-house on 2026-09-03.** Decisions taken during delivery: identifier
> search access events use classification `PERSONAL` (the audit enum has no SENSITIVE;
> health-context reads use `HEALTH`); a PATCH identifier `{type, value}` replaces the
> person's rows of that type (one value per type); `normalized_name` uses the
> `last, first middle` form of section 3.2; non-directional relationships are stored with
> the lower uuid as source so mirrored duplicates hit the same exclusion constraint; the
> access event of a missed search is committed before the 404.

| Field | Value |
|---|---|
| Milestone | M2 (plan increment I2) |
| Size | L |
| Depends on | M1 (identity, tenant context, organizations, audit, idempotency, crypto ports) |
| Runs in parallel with | WP-I2-02 (programs/plans) |
| Migration numbers assigned | `000014_person_relationship_versioning.up.sql` (row_version, updated_at, end reason on `party.person_relationship`) |
| OpenAPI operations owned | `listPeople`, `createPerson`, `getPerson`, new: `updatePerson`, `searchPeopleByIdentifier`, `listPersonRelationships`, `createPersonRelationship`, `endPersonRelationship`, `listSponsorMemberships`, `createSponsorMembership`, `updateSponsorMembership`, `listPartyCatalogs` |
| Read first | Handbook; v1.2 11.2, 16.5 (party tables), 45 (member endpoints); ADR-017 (crypto), `internal/organization` as the reference module; the sqlc, RLS and audit conventions in `docs/delegation/README.md` |

## 1. Goal

A tenant can create and maintain the people it serves (members, dependants, principals)
without any sensitive identifier ever existing in plaintext outside the request that
carried it: identifiers are envelope-encrypted per tenant, found through a tenant-salted
HMAC blind index, and shown masked. Family relationships and sponsor memberships hang off
the person with non-overlapping validity periods enforced by the database.

## 2. Scope

### 2.1 Persons

- `POST /api/v1/people` (`member.manage`, `Idempotency-Key` required): body
  `CreatePersonRequest`. Normalise names (trim, collapse spaces; `normalized_name` =
  lower-cased `last first middle` with Turkish-aware folding, see 3.2), validate
  `birthDate` (not in the future, after 1900-01-01), `sexAtBirth` enum. Identifiers: type
  must exist in `party.identifier_type` for the tenant and be ACTIVE; `TCKN` validated with
  the checksum from `organization/domain.ValidateTCKN`; other types trimmed only. At most
  one `primary` per type. Duplicate blind index within the type's uniqueness scope → 409
  `PERSON_IDENTIFIER_TAKEN` with the existing person id in `detail` only when the caller
  holds `member.identifier.search`, otherwise without it.
- `GET /api/v1/people/{personId}` (`member.read`): `Person` with masked identifiers and
  `ETag`. Merged persons answer 200 with `status: MERGED` and a `Location`-style hint field
  `mergedIntoId` (add to the schema).
- `PATCH /api/v1/people/{personId}` (`member.manage`, merge-patch, `If-Match`): names,
  `birthDate`, `sexAtBirth`, `status` (ACTIVE|INACTIVE|DECEASED only; MERGED is set by the
  merge command, which is out of scope for I2), add/remove identifiers by
  `{type, value}` / `{type, remove: true}`; identifier changes go through the same
  validation as create.
- `GET /api/v1/people` (`member.read`): keyset paging on `(created_at desc, id desc)`
  with the signed cursor codec; filters `q` (trigram search on `normalized_name`, min 2
  characters), `status`, `sponsorOrganizationId` (persons with an ACTIVE membership under
  that sponsor). Never accepts an identifier value.
- `POST /api/v1/people/search-by-identifier` (`member.identifier.search`, step-up
  required, no idempotency): body `{type, value, sponsorOrganizationId?}`. The value is
  normalised, blind-indexed with `BlindIndexer.TenantIndex(tenant, PurposePersonIdentifier, type + ':' + value)`
  and looked up by `(tenant_id, identifier_type, scope_key, identifier_hash)`; returns at
  most one `PersonSummary` (or 404). Writes `audit.access_event` with
  classification SENSITIVE and the identifier type only (never the value). Rate limited to
  20/min per actor on top of the tenant limit.

### 2.2 Catalogs

- `GET /api/v1/party/catalogs` (`member.read`): identifier types, relationship types and
  membership types of the tenant (code, display name, flags, scope).
- The baseline rows already exist: WP-I1-02 provisioning seeds `TCKN`/`PASSPORT`
  (sensitive, TENANT), `MEMBER_NO`/`CUSTOMER_NO` (SPONSOR), relationship types `SPOUSE`
  (non-directional), `CHILD`, `PARENT`, `DEPENDENT`, `GUARDIAN`, `DELEGATE` (directional),
  membership types `EMPLOYEE`, `RETIREE`, `MEMBER`, `CUSTOMER`, `INSURED`, `STUDENT`,
  `BENEFICIARY`, `FAMILY` (requires principal). Use these codes; do not add new ones.

### 2.3 Relationships

- `GET /api/v1/people/{personId}/relationships` (`member.read`): both directions,
  with the other person as `PersonSummary`.
- `POST /api/v1/people/{personId}/relationships` (`member.relationship.manage`,
  idempotent): `{targetPersonId, relationshipType (catalog code, e.g. SPOUSE, CHILD), validFrom, validTo?}`; the exclusion
  constraint (`ex_person_relationship_period`) maps to 409
  `RELATIONSHIP_OVERLAP`; self-relationship 422.
- `POST /api/v1/people/{personId}/relationships/{relationshipId}/end`
  (`member.relationship.manage`, `If-Match`): sets `valid_period` upper bound and status
  ENDED with a reason code.

### 2.4 Sponsor memberships

- `GET /api/v1/people/{personId}/memberships` (`member.read`).
- `POST /api/v1/people/{personId}/memberships` (`membership.manage`, idempotent):
  `{sponsorOrganizationId (a tenant_organization with role SPONSOR or PAYER),
  membershipType, principalMembershipId?, externalMemberNo?, validFrom, validTo?}`;
  `requires_principal` enforced; `uq_sponsor_external_member_no` → 409
  `MEMBER_NO_TAKEN`; overlap → 409 `MEMBERSHIP_OVERLAP`.
- `PATCH /api/v1/people/{personId}/memberships/{membershipId}` (`membership.manage`,
  merge-patch, `If-Match`): status (ACTIVE|SUSPENDED|ENDED), validTo, externalMemberNo.

## 3. Interfaces and rules

### 3.1 Package layout

`internal/party/{domain,application,infrastructure/postgres,transport/http}` mirroring
`internal/organization`. Queries in `db/queries/party.sql` (sqlc). Audit action codes:
`person.create`, `person.update`, `person.identifier.add`, `person.identifier.remove`,
`person.identifier.search` (access event), `relationship.create`, `relationship.end`,
`membership.create`, `membership.update`. Audit detail carries ids, types and counts only.

### 3.2 Identifier handling

- Normalisation: trim, remove spaces and dashes, upper-case; TCKN must be 11 digits with a
  valid checksum.
- Blind index input: `type + ':' + normalizedValue`; `scope_key` per D5: `TENANT` → `''`,
  `SPONSOR` → sponsor tenant_organization id (from the membership named in the request or
  the person's active principal membership; when none, reject with 422
  `IDENTIFIER_SCOPE_REQUIRED`), `NONE` → the row id.
- Cipher: `FieldCipher.Encrypt(ctx, tenantID, PurposePersonIdentifier, value)`; the
  plaintext is decrypted only to produce a mask when the stored `masked_value` is empty
  (never for output). Masks: TCKN `123******01`, MEMBER_NO/EMPLOYEE_NO last 3 visible,
  PASSPORT first 2 visible, other `**`.
- `normalized_name`: NFKC, Turkish lower-casing (`İ`→`i`, `I`→`ı`), diacritics kept, single
  spaces, format `last, first middle`.

### 3.3 Errors

Problem codes: `PERSON_NOT_FOUND` (404), `PERSON_IDENTIFIER_TAKEN`,
`RELATIONSHIP_OVERLAP`, `MEMBERSHIP_OVERLAP`, `MEMBER_NO_TAKEN` (409),
`IDENTIFIER_TYPE_UNKNOWN`, `IDENTIFIER_INVALID`, `IDENTIFIER_SCOPE_REQUIRED`,
`PRINCIPAL_REQUIRED` (422 field errors), `STEP_UP_REQUIRED` (403, existing).

### 3.4 OpenAPI

Add the operations above with request/response schemas (`UpdatePersonRequest`,
`IdentifierSearchRequest`, `PersonRelationship`, `CreateRelationshipRequest`,
`SponsorMembership`, `CreateMembershipRequest`, `UpdateMembershipRequest`,
`PartyCatalogs`), descriptions in the same voice as the organization operations, and
regenerate `api/generated`. `oasdiff` must not report breaking changes.

## 4. Tests required

- Domain: name normalisation (Turkish cases), identifier normalisation/masking, TCKN.
- Application (dbtest): create with TCKN → stored `identifier_cipher` decrypts to the
  value and `identifier_hash` equals the recomputed index; the plaintext appears nowhere in
  `party.*` rows, `audit.event`, `audit.access_event` or logs (capture the logger); search
  by identifier finds the person and writes an access event; same TCKN in another tenant
  is a different blind index; MEMBER_NO with SPONSOR scope is unique per sponsor, not per
  tenant; relationship and membership overlap rejected; dependant without principal 422;
  PATCH with stale `If-Match` 412; list paging over 120 persons; other tenant's person 404.
- HTTP: validation payloads, step-up enforcement on search, permission denials audited.
- db/tests: no new migration expected; if one is added, bump `expectedSchemaVersion`.

## 5. Acceptance criteria

- [ ] No plaintext identifier in the database, audit tables, logs, URLs or metrics (test).
- [ ] Identifier search only with `member.identifier.search` + step-up, audited as SENSITIVE access.
- [ ] Overlapping relationships and memberships rejected by the database, mapped to 409.
- [ ] Baseline party catalogs provisioned for new tenants and for `seed demo`.
- [ ] OpenAPI updated, generated code current, Spectral 0 errors, `oasdiff` no breaking changes.
