-- Property, room type, daily inventory and the availability search (WP-I6-01, v1.2 9.13,
-- 10.6, 11.11, 16.7).
--
-- Three properties shape this file.
--
-- **`accommodation.inventory_day` is the only place a room is counted.** There is no
-- statement here that derives availability from bookings, and none that stores a
-- room-type-level total. `available` is `capacity - held - confirmed` and it is computed
-- in SQL rather than on a screen, so two screens cannot disagree about it.
--
-- **A missing inventory row is not zero and not unlimited: it is no allotment.** The range
-- read below returns the minimum availability *and the number of rows it saw*, because
-- those are two different facts. A room type with capacity on twenty-nine of thirty nights
-- has a healthy minimum and is not available for the stay, and only the count says so.
--
-- **The provider boundary is `scope_ids`**, the same nullable `uuid[]` of tenant
-- organization ids the health and service-request queries apply: NULL means the caller
-- sees the whole tenant, a non-null array binds it to those organizations. It is applied
-- on the single-row reads too, so another provider's room type is 404 rather than 403 --
-- that it exists at all is not this caller's business.
--
-- Money is `numeric(20,6)` and never touches a float: the candidate read below returns
-- every amount as `::text`, exactly as db/queries/contract.sql does, so the decimal the
-- contract states is the decimal the quote sums.

-- ---------------------------------------------------------------------------
-- Property
-- ---------------------------------------------------------------------------

-- name: CreateProperty :one
INSERT INTO accommodation.property (
    tenant_id, provider_organization_id, location_id, code, name, property_type,
    timezone, city, region_code, amenities, cost_center, status, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('provider_organization_id'), sqlc.narg('location_id'),
        sqlc.arg('code'), sqlc.arg('name'), sqlc.arg('property_type'),
        sqlc.arg('timezone'), sqlc.narg('city'), sqlc.narg('region_code'),
        sqlc.arg('amenities'), sqlc.narg('cost_center'), sqlc.arg('status'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, provider_organization_id, location_id, code, name, property_type, timezone,
          city, region_code, amenities, cost_center, status, created_at, updated_at, row_version;

-- name: GetProperty :one
SELECT p.id, p.provider_organization_id, p.location_id, p.code, p.name, p.property_type,
       p.timezone, p.city, p.region_code, p.amenities, p.cost_center, p.status,
       p.created_at, p.updated_at, p.row_version
  FROM accommodation.property p
 WHERE p.tenant_id = sqlc.arg('tenant_id')
   AND p.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: ListProperties :many
-- Keyset pagination on (created_at DESC, id DESC); the caller asks for limit+1 rows to
-- learn whether a next page exists.
SELECT p.id, p.provider_organization_id, p.location_id, p.code, p.name, p.property_type,
       p.timezone, p.city, p.region_code, p.amenities, p.cost_center, p.status,
       p.created_at, p.updated_at, p.row_version
  FROM accommodation.property p
 WHERE p.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('provider_organization_id')::uuid IS NULL
        OR p.provider_organization_id = sqlc.narg('provider_organization_id')::uuid)
   AND (sqlc.narg('status')::text IS NULL OR p.status = sqlc.narg('status')::text)
   AND (sqlc.narg('property_type')::text IS NULL OR p.property_type = sqlc.narg('property_type')::text)
   AND (sqlc.narg('region_code')::text IS NULL OR p.region_code = sqlc.narg('region_code')::text)
   AND (sqlc.narg('city')::text IS NULL OR p.city = sqlc.narg('city')::text)
   AND (sqlc.narg('after_at')::timestamptz IS NULL
        OR (p.created_at, p.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
 ORDER BY p.created_at DESC, p.id DESC
 LIMIT sqlc.arg('page_size');

-- name: UpdateProperty :execrows
-- Every field is sent every time (the contract's PatchProperty says so), so this is plain
-- assignment and not a merge: a merge would make "this hotel no longer has a location" and
-- "this hotel has a cost centre no more" inexpressible, and a property that cannot be
-- un-set is a property somebody edits by hand in the database.
--
-- The whole precondition is the predicate: a property somebody else has moved since the
-- caller read it, or one outside the caller's provider scope, matches no row and updates
-- nothing.
UPDATE accommodation.property
   SET location_id   = sqlc.narg('location_id'),
       name          = sqlc.arg('name'),
       property_type = sqlc.arg('property_type'),
       timezone      = sqlc.arg('timezone'),
       city          = sqlc.narg('city'),
       region_code   = sqlc.narg('region_code'),
       amenities     = sqlc.arg('amenities'),
       cost_center   = sqlc.narg('cost_center'),
       status        = sqlc.arg('status'),
       updated_by    = sqlc.narg('actor_id')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('expected_row_version')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- ---------------------------------------------------------------------------
-- Room type
-- ---------------------------------------------------------------------------

-- name: CreateRoomType :one
INSERT INTO accommodation.room_type (
    tenant_id, property_id, code, name, max_adults, max_children, max_occupancy,
    attributes, service_definition_id, status, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('property_id'), sqlc.arg('code'), sqlc.arg('name'),
        sqlc.arg('max_adults'), sqlc.arg('max_children'), sqlc.arg('max_occupancy'),
        sqlc.arg('attributes'), sqlc.arg('service_definition_id'), sqlc.arg('status'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id, property_id, code, name, max_adults, max_children, max_occupancy,
          attributes, service_definition_id, status, created_at, updated_at, row_version;

-- name: GetRoomType :one
-- The property is joined rather than trusted, so the provider boundary reaches a room type
-- through the building it belongs to: another provider's room type is no row at all.
SELECT r.id, r.property_id, r.code, r.name, r.max_adults, r.max_children, r.max_occupancy,
       r.attributes, r.service_definition_id, r.status, r.created_at, r.updated_at, r.row_version,
       p.provider_organization_id, p.timezone AS property_timezone, p.status AS property_status
  FROM accommodation.room_type r
  JOIN accommodation.property p ON p.tenant_id = r.tenant_id AND p.id = r.property_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.id = sqlc.arg('id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]));

-- name: ListRoomTypes :many
SELECT r.id, r.property_id, r.code, r.name, r.max_adults, r.max_children, r.max_occupancy,
       r.attributes, r.service_definition_id, r.status, r.created_at, r.updated_at, r.row_version
  FROM accommodation.room_type r
  JOIN accommodation.property p ON p.tenant_id = r.tenant_id AND p.id = r.property_id
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.property_id = sqlc.arg('property_id')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND (sqlc.narg('status')::text IS NULL OR r.status = sqlc.narg('status')::text)
 ORDER BY r.code, r.id;

-- name: UpdateRoomType :execrows
-- Plain assignment for the same reason UpdateProperty is. `service_definition_id` is not
-- here and never will be: a room type re-pointed at another service would silently change
-- what every existing booking of it was priced and entitled as.
UPDATE accommodation.room_type r
   SET name          = sqlc.arg('name'),
       max_adults    = sqlc.arg('max_adults'),
       max_children  = sqlc.arg('max_children'),
       max_occupancy = sqlc.arg('max_occupancy'),
       attributes    = sqlc.arg('attributes'),
       status        = sqlc.arg('status'),
       updated_by    = sqlc.narg('actor_id')
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.id = sqlc.arg('id')
   AND r.row_version = sqlc.arg('expected_row_version')
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR EXISTS (SELECT 1 FROM accommodation.property p
                    WHERE p.tenant_id = r.tenant_id AND p.id = r.property_id
                      AND p.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[])));

-- ---------------------------------------------------------------------------
-- Daily inventory
-- ---------------------------------------------------------------------------

-- name: GetRoomTypeInventoryRange :many
-- `available` is computed here and nowhere else, so no screen subtracts. The rows that
-- exist are returned; the caller fills the gaps with "no allotment", which is a different
-- statement from "zero free" and has to stay one.
SELECT i.stay_date, i.capacity, i.held, i.confirmed,
       (i.capacity - i.held - i.confirmed)::int AS available,
       i.updated_at, i.row_version
  FROM accommodation.inventory_day i
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND i.room_type_id = sqlc.arg('room_type_id')
   AND i.stay_date >= sqlc.arg('from_date')::date
   AND i.stay_date <= sqlc.arg('to_date')::date
 ORDER BY i.stay_date;

-- name: LockRoomTypeInventoryRange :many
-- The read the allotment writer makes before it writes, in `stay_date` order -- the same
-- order WP-I6-02 locks these rows in, so a season being opened and a hold being taken
-- cannot deadlock each other. It returns what is already committed on each night, which is
-- what decides whether a new capacity is below what has been promised.
SELECT i.stay_date, i.capacity, i.held, i.confirmed
  FROM accommodation.inventory_day i
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND i.room_type_id = sqlc.arg('room_type_id')
   AND i.stay_date >= sqlc.arg('from_date')::date
   AND i.stay_date <= sqlc.arg('to_date')::date
 ORDER BY i.stay_date
   FOR UPDATE;

-- name: SetRoomTypeInventoryCapacity :execrows
-- A whole season in one statement: one row per day of the range, capacity set, `held` and
-- `confirmed` untouched on the days that already exist. The CHECK on the table is the last
-- word -- if a day's commitment exceeds the new capacity this statement fails and the
-- transaction takes the range with it, which is the only correct outcome for an allotment
-- that was meant to apply to the whole season.
INSERT INTO accommodation.inventory_day (tenant_id, room_type_id, stay_date, capacity)
SELECT sqlc.arg('tenant_id')::uuid, sqlc.arg('room_type_id')::uuid, d::date, sqlc.arg('capacity')::int
  FROM generate_series(sqlc.arg('from_date')::date, sqlc.arg('to_date')::date, interval '1 day') AS d
ON CONFLICT (tenant_id, room_type_id, stay_date)
DO UPDATE SET capacity = EXCLUDED.capacity;

-- ---------------------------------------------------------------------------
-- Availability search
-- ---------------------------------------------------------------------------

-- name: ListAvailabilityProperties :many
-- The properties a search may answer with: ACTIVE buildings of ACTIVE providers that hold
-- an ACTIVE contract, published over the stay, with one of the payer organizations behind
-- the person's programs. `payer_organization_ids` NULL is a back-office search that is not
-- narrowed to one member's programs; a member's own search always carries the array, so a
-- hotel nobody contracted for their program is not on the list at all.
SELECT p.id, p.provider_organization_id, p.location_id, p.code, p.name, p.property_type,
       p.timezone, p.city, p.region_code, p.amenities, p.cost_center, p.status,
       p.created_at, p.updated_at, p.row_version,
       pp.id AS provider_profile_id
  FROM accommodation.property p
  JOIN provider.provider_profile pp
    ON pp.tenant_id = p.tenant_id AND pp.tenant_organization_id = p.provider_organization_id
 WHERE p.tenant_id = sqlc.arg('tenant_id')
   AND p.status = 'ACTIVE'
   AND pp.status = 'ACTIVE'
   AND (sqlc.narg('property_id')::uuid IS NULL OR p.id = sqlc.narg('property_id')::uuid)
   AND (sqlc.narg('region_code')::text IS NULL OR p.region_code = sqlc.narg('region_code')::text)
   AND (sqlc.narg('scope_ids')::uuid[] IS NULL
        OR p.provider_organization_id = ANY(sqlc.narg('scope_ids')::uuid[]))
   AND EXISTS (
        SELECT 1
          FROM contract.contract c
          JOIN contract.contract_version v
            ON v.tenant_id = c.tenant_id AND v.contract_id = c.id
         WHERE c.tenant_id = p.tenant_id
           AND c.provider_profile_id = pp.id
           AND c.status = 'ACTIVE'
           AND v.status = 'PUBLISHED'
           AND v.valid_from <= sqlc.arg('last_night')::date
           AND (v.valid_to IS NULL OR v.valid_to > sqlc.arg('check_in')::date)
           AND (sqlc.narg('payer_organization_ids')::uuid[] IS NULL
                OR c.payer_organization_id = ANY(sqlc.narg('payer_organization_ids')::uuid[])))
 ORDER BY p.name, p.id
 LIMIT sqlc.arg('page_size');

-- name: ListRoomTypesForAvailability :many
-- The ACTIVE room types of those buildings that could hold the party at all. The occupancy
-- filter is here rather than in Go because a room that cannot take the guests is not an
-- answer with a caveat, it is not an answer.
SELECT r.id, r.property_id, r.code, r.name, r.max_adults, r.max_children, r.max_occupancy,
       r.attributes, r.service_definition_id, r.status,
       r.created_at, r.updated_at, r.row_version
  FROM accommodation.room_type r
 WHERE r.tenant_id = sqlc.arg('tenant_id')
   AND r.property_id = ANY(sqlc.arg('property_ids')::uuid[])
   AND r.status = 'ACTIVE'
   AND r.max_adults >= sqlc.arg('adults')
   AND r.max_children >= sqlc.arg('children')
   AND r.max_occupancy >= sqlc.arg('guests')
 ORDER BY r.property_id, r.code, r.id;

-- name: SummariseInventoryOverRange :many
-- The two facts the search needs about each room type over the stay, in one pass: the
-- smallest number of rooms free on any night it has a row for, and how many nights it has
-- a row for at all. The second is not decoration -- a room type with an allotment on every
-- night but one has a perfectly healthy minimum and is not available, and nothing but the
-- count says so.
SELECT i.room_type_id,
       min(i.capacity - i.held - i.confirmed)::int AS min_available,
       count(*)::int AS night_count
  FROM accommodation.inventory_day i
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND i.room_type_id = ANY(sqlc.arg('room_type_ids')::uuid[])
   AND i.stay_date >= sqlc.arg('from_date')::date
   AND i.stay_date <= sqlc.arg('to_date')::date
 GROUP BY i.room_type_id;

-- name: ListAccommodationPriceCandidates :many
-- Every contracted price that could apply to any of these room types on any night of the
-- stay, loaded once for the whole search. It is db/queries/contract.sql's
-- ListPriceCandidates widened in two ways and narrowed in none: the service date becomes a
-- half-open range, and the provider becomes a set, because a search over a region asks
-- about several hotels and thirty nights and one round trip per pair would be a thousand.
--
-- The version's own period comes back with the row, so the caller can decide per night
-- which candidates were in force -- the filtering the single-date query does in its WHERE.
-- Season, weekday, item period, location and the specificity ladder stay in
-- internal/contract/selection, which is the only place any of them is judged.
SELECT i.id AS price_item_id, i.price_list_id, l.contract_version_id,
       i.service_definition_id, i.service_category_id, i.package_definition_id,
       i.location_id, i.valid_from, i.valid_to, i.priority AS item_priority,
       i.unit_type, i.pricing_method,
       coalesce(i.amount::text, '')::text AS amount,
       coalesce(i.percent::text, '')::text AS percent,
       i.formula_key,
       coalesce(i.min_amount::text, '')::text AS min_amount,
       coalesce(i.max_amount::text, '')::text AS max_amount,
       i.member_share_method,
       coalesce(i.member_share_amount::text, '')::text AS member_share_amount,
       coalesce(i.member_share_percent::text, '')::text AS member_share_percent,
       l.code AS price_list_code, l.priority AS list_priority,
       l.season_from, l.season_to, l.weekday_mask,
       v.version_no, v.currency_code, v.valid_from AS version_valid_from, v.valid_to AS version_valid_to,
       c.id AS contract_id, c.code AS contract_code, c.provider_profile_id
  FROM contract.price_item i
  JOIN contract.price_list l ON l.tenant_id = i.tenant_id AND l.id = i.price_list_id
  JOIN contract.contract_version v ON v.tenant_id = l.tenant_id AND v.id = l.contract_version_id
  JOIN contract.contract c ON c.tenant_id = v.tenant_id AND c.id = v.contract_id
 WHERE i.tenant_id = sqlc.arg('tenant_id')
   AND v.status = 'PUBLISHED'
   AND c.status = 'ACTIVE'
   AND c.provider_profile_id = ANY(sqlc.arg('provider_profile_ids')::uuid[])
   AND v.valid_from <= sqlc.arg('last_night')::date
   AND (v.valid_to IS NULL OR v.valid_to > sqlc.arg('check_in')::date)
   AND (i.service_definition_id = ANY(sqlc.arg('service_definition_ids')::uuid[])
        OR i.service_category_id = ANY(sqlc.arg('category_ids')::uuid[])
        OR i.package_definition_id = ANY(sqlc.arg('package_ids')::uuid[]))
 ORDER BY i.id;

-- name: ListPersonProgramPayers :many
-- The payer organizations behind the programs this person is actually enrolled in on the
-- stay's first night. It is what narrows a search to the hotels somebody contracted for
-- this member, and it is a read of the enrollment rather than of the request: a member
-- cannot widen it by naming a program.
SELECT DISTINCT pr.payer_tenant_organization_id AS payer_organization_id
  FROM benefit.enrollment e
  JOIN party.sponsor_membership m ON m.tenant_id = e.tenant_id AND m.id = e.sponsor_membership_id
  JOIN benefit.plan pl ON pl.tenant_id = e.tenant_id AND pl.id = e.plan_id
  JOIN benefit.program pr ON pr.tenant_id = pl.tenant_id AND pr.id = pl.program_id
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND m.person_id = sqlc.arg('person_id')
   AND e.status = 'ACTIVE'
   AND pr.status = 'ACTIVE'
   AND e.valid_period @> sqlc.arg('service_date')::date
   AND (sqlc.narg('program_id')::uuid IS NULL OR pr.id = sqlc.narg('program_id')::uuid);
