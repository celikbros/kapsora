-- Catalog queries (WP-I3-01): service categories, service definitions, external code
-- systems, their values and the mapping between a definition and those codes. Every
-- statement filters on tenant_id explicitly and runs inside db.WithTenantTx, so RLS is
-- the second line of defence.

-- name: CreateServiceCategory :one
INSERT INTO catalog.service_category (tenant_id, parent_id, code, name, domain_code, active)
VALUES (sqlc.arg('tenant_id'), sqlc.narg('parent_id'), sqlc.arg('code'), sqlc.arg('name'),
        sqlc.arg('domain_code'), sqlc.arg('active'))
RETURNING id;

-- name: GetServiceCategory :one
SELECT id, parent_id, code, name, domain_code, active, created_at, row_version
  FROM catalog.service_category
 WHERE tenant_id = $1 AND id = $2;

-- name: ListServiceCategories :many
-- Keyset pagination on (created_at DESC, id DESC); the caller asks for limit+1 rows to
-- learn whether a next page exists.
SELECT id, parent_id, code, name, domain_code, active, created_at, row_version
  FROM catalog.service_category
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('parent_id')::uuid IS NULL OR parent_id = sqlc.narg('parent_id')::uuid)
   AND (sqlc.narg('domain_code')::text IS NULL OR domain_code = sqlc.narg('domain_code')::text)
   AND (sqlc.narg('active')::boolean IS NULL OR active = sqlc.narg('active')::boolean)
   -- The caller escapes the user's own wildcards (domain.LikePattern), so the default
   -- backslash escape character makes '%' and '_' literal characters here.
   AND (sqlc.narg('q')::text IS NULL
        OR code ILIKE sqlc.narg('q')::text OR name ILIKE sqlc.narg('q')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY created_at DESC, id DESC
 LIMIT sqlc.arg('page_size');

-- name: LockServiceCategory :one
-- Re-parenting has to read the ancestor chain and then write, and two concurrent
-- re-parents could otherwise close a loop that neither of them saw. The row lock
-- serialises them; the version check below is what catches a stale If-Match.
SELECT id, parent_id, code, name, domain_code, active, created_at, row_version
  FROM catalog.service_category
 WHERE tenant_id = $1 AND id = $2
   FOR UPDATE;

-- name: UpdateServiceCategory :execrows
-- The expected row_version is part of the predicate, so a stale If-Match updates nothing.
-- The touch trigger moves the version and updated_at.
UPDATE catalog.service_category
   SET parent_id = sqlc.narg('parent_id'),
       name = sqlc.arg('name'),
       active = sqlc.arg('active')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version');

-- name: ServiceCategoryAncestors :many
-- The chain from one category up to its root, the category itself at depth 1. Used to
-- refuse a re-parent that would close a loop (409 CATEGORY_CYCLE) and to measure the
-- resulting depth (422 DEPTH_EXCEEDED). The depth guard also stops a runaway recursion
-- if a cycle ever reached the table.
WITH RECURSIVE up AS (
    SELECT c.id, c.parent_id, 1 AS depth
      FROM catalog.service_category c
     WHERE c.tenant_id = sqlc.arg('tenant_id') AND c.id = sqlc.arg('id')
    UNION ALL
    SELECT p.id, p.parent_id, up.depth + 1
      FROM catalog.service_category p
      JOIN up ON p.id = up.parent_id
     WHERE p.tenant_id = sqlc.arg('tenant_id') AND up.depth < 64
)
SELECT id, depth::int AS depth FROM up ORDER BY depth;

-- name: ServiceCategorySubtreeHeight :one
-- How many levels hang below a category, the category itself counting as 1. Re-parenting
-- must keep ancestors + subtree within the depth cap.
WITH RECURSIVE down AS (
    SELECT c.id, 1 AS depth
      FROM catalog.service_category c
     WHERE c.tenant_id = sqlc.arg('tenant_id') AND c.id = sqlc.arg('id')
    UNION ALL
    SELECT ch.id, down.depth + 1
      FROM catalog.service_category ch
      JOIN down ON ch.parent_id = down.id
     WHERE ch.tenant_id = sqlc.arg('tenant_id') AND down.depth < 64
)
SELECT coalesce(max(depth), 0)::int AS height FROM down;

-- name: CreateServiceDefinition :one
INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, description,
                                        fulfillment_mode, default_unit_type, requires_provider, active)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('category_id'), sqlc.arg('code'), sqlc.arg('name'),
        sqlc.narg('description'), sqlc.arg('fulfillment_mode'), sqlc.arg('default_unit_type'),
        sqlc.arg('requires_provider'), sqlc.arg('active'))
RETURNING id, row_version;

-- name: GetServiceDefinition :one
SELECT d.id, d.category_id, d.code, d.name, d.description, d.fulfillment_mode,
       d.default_unit_type, d.requires_provider, d.active, d.created_at, d.row_version,
       c.code AS category_code, c.domain_code
  FROM catalog.service_definition d
  JOIN catalog.service_category c ON c.tenant_id = d.tenant_id AND c.id = d.category_id
 WHERE d.tenant_id = $1 AND d.id = $2;

-- name: ListServiceDefinitions :many
SELECT d.id, d.category_id, d.code, d.name, d.description, d.fulfillment_mode,
       d.default_unit_type, d.requires_provider, d.active, d.created_at, d.row_version,
       c.code AS category_code, c.domain_code
  FROM catalog.service_definition d
  JOIN catalog.service_category c ON c.tenant_id = d.tenant_id AND c.id = d.category_id
 WHERE d.tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('category_id')::uuid IS NULL OR d.category_id = sqlc.narg('category_id')::uuid)
   AND (sqlc.narg('domain_code')::text IS NULL OR c.domain_code = sqlc.narg('domain_code')::text)
   AND (sqlc.narg('active')::boolean IS NULL OR d.active = sqlc.narg('active')::boolean)
   AND (sqlc.narg('q')::text IS NULL
        OR d.code ILIKE sqlc.narg('q')::text OR d.name ILIKE sqlc.narg('q')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (d.created_at, d.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY d.created_at DESC, d.id DESC
 LIMIT sqlc.arg('page_size');

-- name: UpdateServiceDefinition :one
-- platform.tg_touch_row bumps row_version and updated_at, so this statement never
-- assigns them. The code column is deliberately absent: it is immutable.
UPDATE catalog.service_definition
   SET category_id = sqlc.arg('category_id'),
       name = sqlc.arg('name'),
       description = sqlc.narg('description'),
       fulfillment_mode = sqlc.arg('fulfillment_mode'),
       default_unit_type = sqlc.arg('default_unit_type'),
       requires_provider = sqlc.arg('requires_provider'),
       active = sqlc.arg('active')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version')
RETURNING row_version;

-- name: TouchServiceDefinition :execrows
-- Replacing the code mappings of a definition changes what the definition means to the
-- outside world, so the touch trigger invalidates the ETag the caller holds.
UPDATE catalog.service_definition
   SET name = name
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version');

-- name: CreateCodeSystem :one
INSERT INTO catalog.code_system (tenant_id, code, name, version, authority, licensed, valid_from, valid_to)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('code'), sqlc.arg('name'), sqlc.arg('version'),
        sqlc.arg('authority'), sqlc.arg('licensed'), sqlc.arg('valid_from'), sqlc.narg('valid_to'))
RETURNING id, row_version;

-- name: GetCodeSystem :one
SELECT id, code, name, version, authority, licensed, status, valid_from, valid_to,
       created_at, row_version
  FROM catalog.code_system
 WHERE tenant_id = $1 AND id = $2;

-- name: ListCodeSystems :many
SELECT id, code, name, version, authority, licensed, status, valid_from, valid_to,
       created_at, row_version
  FROM catalog.code_system
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND (sqlc.narg('authority')::text IS NULL OR authority = sqlc.narg('authority')::text)
   AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
   AND (sqlc.narg('q')::text IS NULL
        OR code ILIKE sqlc.narg('q')::text OR name ILIKE sqlc.narg('q')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY created_at DESC, id DESC
 LIMIT sqlc.arg('page_size');

-- name: UpdateCodeSystem :one
-- code and version are absent: together they identify the edition every value and every
-- mapping hangs off, so they are immutable.
UPDATE catalog.code_system
   SET name = sqlc.arg('name'),
       authority = sqlc.arg('authority'),
       licensed = sqlc.arg('licensed'),
       status = sqlc.arg('status'),
       valid_to = sqlc.narg('valid_to')
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = sqlc.arg('id')
   AND row_version = sqlc.arg('row_version')
RETURNING row_version;

-- name: UpsertCodeValue :batchone
-- Import upserts on (code, valid_from). A row whose payload is byte-for-byte what is
-- already stored updates nothing and returns no row, which the caller counts as skipped;
-- xmax distinguishes a fresh insert from an update of an existing row.
INSERT INTO catalog.code_value (tenant_id, code_system_id, code, display, parent_code,
                                valid_from, valid_to, active, attributes)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('code_system_id'), sqlc.arg('code'), sqlc.arg('display'),
        sqlc.narg('parent_code'), sqlc.arg('valid_from'), sqlc.narg('valid_to'),
        sqlc.arg('active'), sqlc.arg('attributes'))
ON CONFLICT (tenant_id, code_system_id, code, valid_from) DO UPDATE
   SET display = excluded.display,
       parent_code = excluded.parent_code,
       valid_to = excluded.valid_to,
       active = excluded.active,
       attributes = excluded.attributes
 WHERE (code_value.display, code_value.parent_code, code_value.valid_to,
        code_value.active, code_value.attributes)
       IS DISTINCT FROM (excluded.display, excluded.parent_code, excluded.valid_to,
                         excluded.active, excluded.attributes)
RETURNING id, (xmax = 0)::boolean AS created;

-- name: ListCodeValues :many
-- Reading a code system is always as of a date: a claim from last year has to resolve
-- against the codes that were valid then, not today's.
SELECT id, code_system_id, code, display, parent_code, valid_from, valid_to, active,
       attributes, created_at
  FROM catalog.code_value
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND code_system_id = sqlc.arg('code_system_id')
   AND valid_from <= sqlc.arg('as_of')::date
   AND (valid_to IS NULL OR valid_to > sqlc.arg('as_of')::date)
   AND (sqlc.narg('code')::text IS NULL OR code = sqlc.narg('code')::text)
   AND (sqlc.narg('q')::text IS NULL
        OR code ILIKE sqlc.narg('q')::text OR display ILIKE sqlc.narg('q')::text)
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY created_at DESC, id DESC
 LIMIT sqlc.arg('page_size');

-- name: ListServiceCodeMappings :many
SELECT m.id, m.service_definition_id, m.code_system_id, m.code, m.valid_from, m.valid_to,
       m.is_primary, s.code AS code_system_code, s.version AS code_system_version
  FROM catalog.service_code_mapping m
  JOIN catalog.code_system s ON s.tenant_id = m.tenant_id AND s.id = m.code_system_id
 WHERE m.tenant_id = $1 AND m.service_definition_id = $2
 ORDER BY s.code, m.valid_from, m.code;

-- name: DeleteServiceCodeMappings :execrows
DELETE FROM catalog.service_code_mapping
 WHERE tenant_id = $1 AND service_definition_id = $2;

-- name: CreateServiceCodeMapping :batchexec
INSERT INTO catalog.service_code_mapping (tenant_id, service_definition_id, code_system_id,
                                          code, valid_from, valid_to, is_primary)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('service_definition_id'), sqlc.arg('code_system_id'),
        sqlc.arg('code'), sqlc.arg('valid_from'), sqlc.narg('valid_to'), sqlc.arg('is_primary'));
