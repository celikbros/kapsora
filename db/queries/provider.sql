-- Provider network queries (WP-I3-02): provider profiles, the locations they work from,
-- what those locations can deliver, the practitioners registered there and the search the
-- rest of the system asks "who can do this, here, on this date".
--
-- Every statement filters on tenant_id explicitly and runs inside db.WithTenantTx, so RLS
-- is the second line of defence. The scope_ids parameter is the provider boundary: a NULL
-- array means a tenant-wide actor, a non-NULL one restricts every read and write to the
-- organizations the actor's role grants name. It sits in SQL rather than in a handler so
-- no route can forget it.

-- name: CreateProviderProfile :one
INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type,
                                       network_tier, contracted_from, contracted_to, notes)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('tenant_organization_id'), sqlc.arg('provider_type'),
        sqlc.narg('network_tier'), sqlc.narg('contracted_from'), sqlc.narg('contracted_to'),
        sqlc.narg('notes'))
RETURNING id;

-- name: GetProviderProfile :one
SELECT p.id, p.tenant_organization_id, p.provider_type, p.status, p.network_tier,
       p.contracted_from, p.contracted_to, p.notes, p.created_at, p.row_version,
       o.display_name AS organization_name
  FROM provider.provider_profile p
  JOIN directory.tenant_organization t
    ON t.tenant_id = p.tenant_id AND t.id = p.tenant_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE p.tenant_id = sqlc.arg('tenant_id')
   AND p.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: ListProviderProfiles :many
-- Keyset pagination on (created_at DESC, id DESC); the caller asks for limit+1 rows to
-- learn whether a next page exists.
SELECT p.id, p.tenant_organization_id, p.provider_type, p.status, p.network_tier,
       p.contracted_from, p.contracted_to, p.notes, p.created_at, p.row_version,
       o.display_name AS organization_name
  FROM provider.provider_profile p
  JOIN directory.tenant_organization t
    ON t.tenant_id = p.tenant_id AND t.id = p.tenant_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE p.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('provider_type')::text IS NULL OR p.provider_type = sqlc.narg('provider_type')::text)
   AND (sqlc.narg('status')::text IS NULL OR p.status = sqlc.narg('status')::text)
   AND (sqlc.narg('network_tier')::text IS NULL OR p.network_tier = sqlc.narg('network_tier')::text)
   -- The caller escapes the user's own wildcards (domain.LikePattern), so the default
   -- backslash escape character makes '%' and '_' literal characters here.
   AND (sqlc.narg('q')::text IS NULL OR o.display_name ILIKE sqlc.narg('q')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (p.created_at, p.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY p.created_at DESC, p.id DESC
 LIMIT sqlc.arg('page_size');

-- name: UpdateProviderProfile :one
-- platform.tg_touch_row bumps row_version and updated_at, so this statement never assigns
-- them. status is deliberately absent: it moves through the explicit commands below.
UPDATE provider.provider_profile
   SET provider_type = sqlc.arg('provider_type'),
       network_tier = sqlc.narg('network_tier'),
       contracted_from = sqlc.narg('contracted_from'),
       contracted_to = sqlc.narg('contracted_to'),
       notes = sqlc.narg('notes')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
RETURNING row_version;

-- name: UpdateProviderStatus :one
-- The only writer of provider_profile.status. The legality of the move is decided by the
-- domain before this runs; the expected row_version keeps a stale If-Match from winning.
UPDATE provider.provider_profile
   SET status = sqlc.arg('status')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
RETURNING row_version;

-- name: CreateProviderLocation :one
INSERT INTO provider.location (tenant_id, provider_profile_id, code, name, address_line,
                               district, city, country_code, postal_code, latitude, longitude,
                               timezone, phone)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('provider_profile_id'), sqlc.arg('code'), sqlc.arg('name'),
        sqlc.narg('address_line'), sqlc.narg('district'), sqlc.narg('city'),
        sqlc.arg('country_code'), sqlc.narg('postal_code'),
        sqlc.narg('latitude')::float8, sqlc.narg('longitude')::float8,
        sqlc.arg('timezone'), sqlc.narg('phone'))
RETURNING id;

-- name: GetProviderLocation :one
SELECT l.id, l.provider_profile_id, l.code, l.name, l.address_line, l.district, l.city,
       l.country_code, l.postal_code, l.latitude, l.longitude, l.timezone, l.phone,
       l.status, l.created_at, l.row_version
  FROM provider.location l
  JOIN provider.provider_profile p ON p.tenant_id = l.tenant_id AND p.id = l.provider_profile_id
 WHERE l.tenant_id = sqlc.arg('tenant_id')
   AND l.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: ListProviderLocations :many
SELECT l.id, l.provider_profile_id, l.code, l.name, l.address_line, l.district, l.city,
       l.country_code, l.postal_code, l.latitude, l.longitude, l.timezone, l.phone,
       l.status, l.created_at, l.row_version
  FROM provider.location l
  JOIN provider.provider_profile p ON p.tenant_id = l.tenant_id AND p.id = l.provider_profile_id
 WHERE l.tenant_id = sqlc.arg('tenant_id')
   AND l.provider_profile_id = sqlc.arg('provider_profile_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('status')::text IS NULL OR l.status = sqlc.narg('status')::text)
   AND (sqlc.narg('city')::text IS NULL OR l.city = sqlc.narg('city')::text)
   AND (sqlc.narg('q')::text IS NULL OR l.code ILIKE sqlc.narg('q')::text OR l.name ILIKE sqlc.narg('q')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (l.created_at, l.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY l.created_at DESC, l.id DESC
 LIMIT sqlc.arg('page_size');

-- name: UpdateProviderLocation :one
-- The code column is deliberately absent: capabilities, contracts and service requests are
-- read against it, so it is immutable.
UPDATE provider.location
   SET name = sqlc.arg('name'),
       address_line = sqlc.narg('address_line'),
       district = sqlc.narg('district'),
       city = sqlc.narg('city'),
       country_code = sqlc.arg('country_code'),
       postal_code = sqlc.narg('postal_code'),
       latitude = sqlc.narg('latitude')::float8,
       longitude = sqlc.narg('longitude')::float8,
       timezone = sqlc.arg('timezone'),
       phone = sqlc.narg('phone'),
       status = sqlc.arg('status')
 WHERE location.tenant_id = sqlc.arg('tenant_id')
   AND location.id = sqlc.arg('id')
   AND location.row_version = sqlc.arg('row_version')
   AND EXISTS (SELECT 1 FROM provider.provider_profile p
                WHERE p.tenant_id = location.tenant_id
                  AND p.id = location.provider_profile_id
                  AND (sqlc.narg('scope_ids')::uuid[] IS NULL
                       OR p.tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[])))
RETURNING row_version;

-- name: TouchProviderLocation :execrows
-- Replacing the capability set changes what the location means to the search, so the touch
-- trigger invalidates the ETag the caller holds.
UPDATE provider.location
   SET name = name
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version');

-- name: ListProviderCapabilities :many
-- The set is short by design, so it is read whole rather than paged. The catalog joins are
-- left joins because exactly one of the two targets is set on every row.
SELECT c.id, c.location_id, c.service_definition_id, c.service_category_id,
       c.valid_from, c.valid_to, c.notes,
       d.code AS service_definition_code, g.code AS service_category_code
  FROM provider.capability c
  LEFT JOIN catalog.service_definition d
    ON d.tenant_id = c.tenant_id AND d.id = c.service_definition_id
  LEFT JOIN catalog.service_category g
    ON g.tenant_id = c.tenant_id AND g.id = c.service_category_id
 WHERE c.tenant_id = $1 AND c.location_id = $2
 ORDER BY c.valid_from, c.id;

-- name: DeleteProviderCapabilities :execrows
DELETE FROM provider.capability
 WHERE tenant_id = $1 AND location_id = $2;

-- name: CreateProviderCapability :batchexec
INSERT INTO provider.capability (tenant_id, location_id, service_definition_id,
                                 service_category_id, valid_from, valid_to, notes)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('location_id'), sqlc.narg('service_definition_id'),
        sqlc.narg('service_category_id'), sqlc.arg('valid_from'), sqlc.narg('valid_to'),
        sqlc.narg('notes'));

-- name: ServiceDefinitionCategoryChain :many
-- The chain from a service definition's own category up to its root. A capability naming
-- any of these categories covers the definition, which is how a category capability keeps
-- covering definitions added under it later without a single provider row being touched.
-- The depth guard stops a runaway recursion if a cycle ever reached the table.
WITH RECURSIVE seed AS (
    SELECT d.category_id AS id
      FROM catalog.service_definition d
     WHERE d.tenant_id = sqlc.arg('tenant_id') AND d.id = sqlc.arg('service_definition_id')
), up AS (
    SELECT c.id, c.parent_id, 1 AS depth
      FROM catalog.service_category c
      JOIN seed s ON s.id = c.id
     WHERE c.tenant_id = sqlc.arg('tenant_id')
    UNION ALL
    SELECT p.id, p.parent_id, up.depth + 1
      FROM catalog.service_category p
      JOIN up ON p.id = up.parent_id
     WHERE p.tenant_id = sqlc.arg('tenant_id') AND up.depth < 64
)
SELECT id, depth::int AS depth FROM up ORDER BY depth;

-- name: SearchProviderLocations :many
-- Which locations can deliver this service on this date. category_ids is the chain the
-- query above resolved, so a category capability anywhere above the definition counts.
-- Non-ACTIVE providers and locations are excluded: the search answers "where can this be
-- delivered now", not "where was it ever possible".
SELECT l.id AS location_id, l.code AS location_code, l.name AS location_name,
       l.city, l.district, l.latitude, l.longitude, l.created_at, p.id AS provider_id, p.provider_type, p.network_tier,
       o.display_name AS organization_name,
       EXISTS (SELECT 1 FROM provider.capability c
                WHERE c.tenant_id = l.tenant_id AND c.location_id = l.id
                  AND c.service_definition_id = sqlc.arg('service_definition_id')::uuid
                  AND c.valid_from <= sqlc.arg('as_of')::date
                  AND (c.valid_to IS NULL OR c.valid_to > sqlc.arg('as_of')::date)) AS matched_definition
  FROM provider.location l
  JOIN provider.provider_profile p ON p.tenant_id = l.tenant_id AND p.id = l.provider_profile_id
  JOIN directory.tenant_organization t
    ON t.tenant_id = p.tenant_id AND t.id = p.tenant_organization_id
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE l.tenant_id = sqlc.arg('tenant_id')
   AND l.status = 'ACTIVE'
   AND p.status = 'ACTIVE'
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('city')::text IS NULL OR l.city = sqlc.narg('city')::text)
   AND (sqlc.narg('q')::text IS NULL OR l.code ILIKE sqlc.narg('q')::text OR l.name ILIKE sqlc.narg('q')::text)
   AND EXISTS (SELECT 1 FROM provider.capability c
                WHERE c.tenant_id = l.tenant_id AND c.location_id = l.id
                  AND c.valid_from <= sqlc.arg('as_of')::date
                  AND (c.valid_to IS NULL OR c.valid_to > sqlc.arg('as_of')::date)
                  AND (c.service_definition_id = sqlc.arg('service_definition_id')::uuid
                       OR c.service_category_id = ANY(sqlc.arg('category_ids')::uuid[])))
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (l.created_at, l.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY l.created_at DESC, l.id DESC
 LIMIT sqlc.arg('page_size');

-- name: CreatePractitioner :one
-- The registration number reaches the database only as an envelope and a tenant-salted
-- blind index; the masked form is the only readable copy.
INSERT INTO provider.practitioner (tenant_id, provider_profile_id, person_id, full_name, title,
                                   branch_code, registration_authority, registration_number_cipher,
                                   registration_number_hash, registration_number_masked,
                                   valid_from, valid_to)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('provider_profile_id'), sqlc.narg('person_id'),
        sqlc.arg('full_name'), sqlc.narg('title'), sqlc.narg('branch_code'),
        sqlc.arg('registration_authority'), sqlc.arg('registration_number_cipher'),
        sqlc.arg('registration_number_hash'), sqlc.arg('registration_number_masked'),
        sqlc.narg('valid_from'), sqlc.narg('valid_to'))
RETURNING id;

-- name: GetPractitioner :one
SELECT r.id, r.provider_profile_id, r.person_id, r.full_name, r.title, r.branch_code,
       r.registration_authority, r.registration_number_masked, r.valid_from, r.valid_to,
       r.status, r.created_at, r.row_version
  FROM provider.practitioner r
  JOIN provider.provider_profile p ON p.tenant_id = r.tenant_id AND p.id = r.provider_profile_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: ListPractitioners :many
SELECT r.id, r.provider_profile_id, r.person_id, r.full_name, r.title, r.branch_code,
       r.registration_authority, r.registration_number_masked, r.valid_from, r.valid_to,
       r.status, r.created_at, r.row_version
  FROM provider.practitioner r
  JOIN provider.provider_profile p ON p.tenant_id = r.tenant_id AND p.id = r.provider_profile_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.provider_profile_id = sqlc.arg('provider_profile_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('status')::text IS NULL OR r.status = sqlc.narg('status')::text)
   AND (sqlc.narg('branch_code')::text IS NULL OR r.branch_code = sqlc.narg('branch_code')::text)
   AND (sqlc.narg('q')::text IS NULL OR r.full_name ILIKE sqlc.narg('q')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (r.created_at, r.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY r.created_at DESC, r.id DESC
 LIMIT sqlc.arg('page_size');

-- name: UpdatePractitioner :one
-- The registration columns are absent: a wrong number is ended and re-registered, never
-- rewritten, because reports already signed under it must keep resolving.
UPDATE provider.practitioner
   SET person_id = sqlc.narg('person_id'),
       full_name = sqlc.arg('full_name'),
       title = sqlc.narg('title'),
       branch_code = sqlc.narg('branch_code'),
       valid_from = sqlc.narg('valid_from'),
       valid_to = sqlc.narg('valid_to'),
       status = sqlc.arg('status')
 WHERE practitioner.tenant_id = sqlc.arg('tenant_id')
   AND practitioner.id = sqlc.arg('id')
   AND practitioner.row_version = sqlc.arg('row_version')
   AND EXISTS (SELECT 1 FROM provider.provider_profile p
                WHERE p.tenant_id = practitioner.tenant_id
                  AND p.id = practitioner.provider_profile_id
                  AND (sqlc.narg('scope_ids')::uuid[] IS NULL
                       OR p.tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[])))
RETURNING row_version;

-- name: TouchPractitioner :execrows
-- Replacing the location assignments changes where the practitioner may sign, so the ETag
-- the caller holds is invalidated.
UPDATE provider.practitioner
   SET full_name = full_name
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version');

-- name: FindPractitionerByRegistrationHash :one
-- Equality search on the tenant-salted blind index; the plaintext never reaches SQL.
SELECT r.id
  FROM provider.practitioner r
  JOIN provider.provider_profile p ON p.tenant_id = r.tenant_id AND p.id = r.provider_profile_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.registration_authority = sqlc.arg('registration_authority')
   AND r.registration_number_hash = sqlc.arg('registration_number_hash')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.tenant_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
 LIMIT 1;

-- name: ListPractitionerLocations :many
SELECT a.id, a.practitioner_id, a.location_id, a.role, a.valid_from, a.valid_to,
       l.code AS location_code, l.name AS location_name
  FROM provider.practitioner_location a
  JOIN provider.location l ON l.tenant_id = a.tenant_id AND l.id = a.location_id
 WHERE a.tenant_id = $1 AND a.practitioner_id = $2
 ORDER BY l.code, a.valid_from, a.role;

-- name: DeletePractitionerLocations :execrows
DELETE FROM provider.practitioner_location
 WHERE tenant_id = $1 AND practitioner_id = $2;

-- name: CreatePractitionerLocation :batchexec
INSERT INTO provider.practitioner_location (tenant_id, practitioner_id, location_id, role,
                                            valid_from, valid_to)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('practitioner_id'), sqlc.arg('location_id'),
        sqlc.arg('role'), sqlc.arg('valid_from'), sqlc.narg('valid_to'));

-- name: CountProviderLocationsOutsideProvider :one
-- Guards a practitioner-location replacement: every location in the submitted set has to
-- belong to the practitioner's own provider, and a location of another provider must not
-- even reveal that it exists.
SELECT count(*)::int AS outside
  FROM unnest(sqlc.arg('location_ids')::uuid[]) AS wanted(id)
 WHERE NOT EXISTS (SELECT 1 FROM provider.location l
                    WHERE l.tenant_id = sqlc.arg('tenant_id')
                      AND l.id = wanted.id
                      AND l.provider_profile_id = sqlc.arg('provider_profile_id'));

-- name: GetProviderOrganization :one
-- The organization relationship a provider profile hangs off. The relationship role and
-- status are read before a profile is created, so a profile can only be given to an
-- organization the tenant actually contracts with as a provider.
SELECT t.id, t.relationship_role, t.status, o.display_name
  FROM directory.tenant_organization t
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE t.tenant_id = $1 AND t.id = $2;

-- name: ProviderPersonExists :one
-- Linking a practitioner to a member has to fail as a field error rather than as a raw
-- foreign key violation, so the row is probed first.
SELECT EXISTS (SELECT 1 FROM party.person WHERE tenant_id = $1 AND id = $2) AS present;
