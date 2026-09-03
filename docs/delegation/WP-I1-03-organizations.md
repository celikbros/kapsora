# WP-I1-03 · Organizations: directory CRUD, VKN/TCKN validation, blind-index dedup, ETag, cursor paging

> **Delivered in-house on 2026-09-03.** Two notes for anyone reading this as a spec: the
> handler is a plain chi handler that (un)marshals the generated contract types rather than
> an implementation of the generated strict-server interface (the interface spans every
> operation in the contract, and the shape is asserted in tests instead); and one migration
> was needed after all — `000013` adds a SECURITY DEFINER count function so a tenant can learn
> that an organization is shared without seeing other tenants' relationship rows.

| Field | Value |
|---|---|
| Milestone | M1 (plan increment I1) |
| Size | M |
| Depends on | Ports in `main`: `internal/identity` (`Require`, `FromContext`), `internal/audit`, `internal/platform/crypto` (+ `localkey`) |
| Runs in parallel with | WP-I1-01, 02, 04, 05, 06 |
| Migration numbers assigned | none expected (schema exists in 000002). Ask if you need one |
| OpenAPI operations owned | `listOrganizations`, `createTenantOrganization`, `getOrganization`, `updateOrganization` |
| Read first | Handbook; v1.2 sections 9.3, 16.3, 16.4, 17.1-17.3; ADR-004, ADR-015; migration 000002; OpenAPI schemas `Organization*` |

## 1. Goal

Tenants manage their relationships with organizations (payers, sponsors, providers,
vendors, partners). Legal entities are shared in a global directory and deduplicated by
country + tax number blind index, so the same hospital contracted by two tenants is one
`directory.organization` row with two `directory.tenant_organization` rows.

## 2. Scope

1. Turkish registry identifier validation: VKN (10 digits) and TCKN (11 digits) checksum
   algorithms (section 5), MERSIS format (16 digits), generic `OTHER`.
2. Create: find-or-create the global organization by `(country_code, tax_number_hash)`
   using `crypto.BlindIndexer.GlobalIndex(PurposeOrganizationTax, normalized)`; store the
   tax number encrypted with `FieldCipher` (`PurposeOrganizationTax`, tenant `uuid.Nil`
   because the row is global); insert the tenant relationship; `409 ORGANIZATION_RELATIONSHIP_EXISTS`
   when the exclusion constraint fires.
3. Read: list with cursor pagination and filters (`role`, `q` trigram search on
   `display_name`), get by relationship id; both scoped by RLS and permission
   `organization.read`.
4. Update (`PATCH`, merge-patch): `displayName` (global row, only when this tenant created
   the organization or it has no other tenant relationships; otherwise `409
   ORGANIZATION_SHARED_READONLY`), `relationshipStatus`, `tenantCode`; `If-Match` against
   `row_version` → `412 ETAG_MISMATCH`; permission `organization.manage`.
5. Masked identifiers in responses: VKN `12******90`, TCKN `123******01`, MERSIS last 4
   visible, OTHER first 2 visible.
6. Audit BUSINESS events `organization.create`, `organization.update` with resource ids
   only (never the tax number).
7. Cursor pagination helper in `internal/platform/httpx/cursor.go`: opaque base64url of
   `{createdAt, id}` plus HMAC-SHA256 signature (key from `KAPSORA_COOKIE_SIGNING_KEY`,
   reuse the config key WP-I1-01 introduces; until it lands read it yourself from the
   environment in `config`). Invalid cursor → `400 CURSOR_INVALID`. Other WPs will reuse
   this helper; keep it generic.

Out of scope: provider profiles, locations, practitioners (M3); organization identifiers
other than the ones above; import.

## 3. Packages and layout

```text
internal/organization/
  domain/organization.go        entity, relationship, validation errors
  domain/identifiers.go         VKN/TCKN/MERSIS validators and masking (pure, well tested)
  application/service.go        Create, Get, List, Update (transactions via db.WithTenantTx)
  application/ports.go          Repository interface
  infrastructure/postgres/      sqlc repository
  transport/http/handler.go     strict-server implementation of the four operations
db/queries/directory.sql
internal/platform/httpx/cursor.go (+ tests)
```

## 4. Contract details

- `POST /api/v1/organizations` body `CreateOrganizationRequest`; for `countryCode = TR`
  exactly one of VKN or TCKN is mandatory (`422 IDENTIFIER_REQUIRED`), checksum failure
  `422 IDENTIFIER_INVALID` with `errors[]` pointing at `identifiers[i].value`.
- Response `Organization`: `id` = relationship id, `organizationId` = global id,
  `relationshipStatus`, `organizationStatus`, masked `identifiers`, `rowVersion`
  (relationship row), `ETag: "<rowVersion>"`.
- `GET /api/v1/organizations?role=&q=&cursor=&limit=`: default 50, max 200; order
  `created_at DESC, id DESC`; `q` minimum 2 characters, maximum 120; trigram `ILIKE`.
- `GET /api/v1/organizations/{organizationId}`: `{organizationId}` is the relationship id;
  unknown or other-tenant ids are `404 RESOURCE_NOT_FOUND`.
- `PATCH` with `application/merge-patch+json`; unknown fields `422`.
- `Idempotency-Key` on create is enforced by the platform middleware (WP-I1-04); your
  handler must be safe to replay: creation inside one transaction, unique constraints do
  the rest.

## 5. Identifier algorithms

Normalize first: trim, remove spaces, digits only for VKN/TCKN/MERSIS.

**TCKN (11 digits):** first digit not 0; let d1..d11.
`d10 = ((d1+d3+d5+d7+d9)*7 - (d2+d4+d6+d8)) mod 10`; `d11 = (d1+...+d10) mod 10`.
Produce test vectors by generating the first 9 digits randomly and computing d10, d11;
also assert well-known invalid patterns (all same digit, leading zero, wrong length).

**VKN (10 digits):** let v1..v9 be the first nine digits, `sum = 0`;
for `i = 1..9`: `tmp = (v_i + 10 - i) mod 10`; `sum += tmp == 9 ? 9 : (tmp * 2^(10-i)) mod 9`;
check digit `= (10 - (sum mod 10)) mod 10` must equal digit 10. Same test strategy.

**MERSIS:** exactly 16 digits, no checksum in this WP.

Never include real tax numbers in tests or fixtures; generated values only.

## 6. Tests required

- Domain: validators with generated valid vectors, invalid vectors, masking.
- Application/infrastructure with `dbtest`: create twice with the same VKN in two tenants
  → one global row, two relationships; same tenant same role → 409; other tenant cannot
  see or patch (`404`); `If-Match` stale → 412; list order and cursor round trip
  (page 1 → cursor → page 2 without duplicates or gaps, 120 rows, limit 50).
- Transport with `httptest` against the generated strict server: validation `422`
  payloads, permission denial `403` using a `RequestContext` without `organization.manage`.
- Cursor helper: tampered cursor rejected.

## 7. Acceptance criteria

- [ ] Global dedup demonstrated in a test (one `directory.organization` for two tenants).
- [ ] Tax numbers never appear in logs, audit detail, responses (only masked) or error
      messages; a test greps the audit detail JSON and log output for the generated VKN.
- [ ] RLS negative tests for list/get/patch.
- [ ] OpenAPI descriptions added for the four operations; Spectral zero errors; generated
      code current.
- [ ] `make ci` and `make test-db` green.
