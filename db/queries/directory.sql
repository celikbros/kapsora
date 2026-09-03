-- Organization directory queries (WP-I1-03). Global rows (directory.organization,
-- directory.organization_identifier) have no RLS; relationship rows are tenant-bound.

-- name: FindOrganizationByTaxHash :one
SELECT id, legal_name, display_name, organization_kind, country_code, status, row_version
  FROM directory.organization
 WHERE country_code = $1 AND tax_number_hash = $2;

-- name: CreateOrganization :one
INSERT INTO directory.organization (legal_name, display_name, organization_kind, country_code, tax_number_cipher, tax_number_hash)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id;

-- name: UpdateOrganizationDisplayName :exec
UPDATE directory.organization SET display_name = $2 WHERE id = $1;

-- name: OrganizationRelationshipCount :one
SELECT directory.organization_relationship_count($1)::integer AS relationship_count;

-- name: AddOrganizationIdentifier :execrows
INSERT INTO directory.organization_identifier (organization_id, identifier_type, identifier_value, issuing_country, is_primary)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (identifier_type, identifier_value) DO NOTHING;

-- name: FindOrganizationIdentifierOwner :one
SELECT organization_id FROM directory.organization_identifier
 WHERE identifier_type = $1 AND identifier_value = $2;

-- name: ListOrganizationIdentifiers :many
SELECT identifier_type, identifier_value, is_primary
  FROM directory.organization_identifier
 WHERE organization_id = $1
 ORDER BY is_primary DESC, identifier_type, identifier_value;

-- name: CreateTenantOrganizationRelationship :one
INSERT INTO directory.tenant_organization (tenant_id, organization_id, relationship_role, tenant_code)
VALUES ($1, $2, $3, $4)
RETURNING id, created_at, row_version, status, valid_period;

-- name: GetTenantOrganization :one
SELECT t.id, t.organization_id, t.relationship_role, t.tenant_code, t.status AS relationship_status,
       lower(t.valid_period)::date AS valid_from, upper(t.valid_period)::date AS valid_to,
       t.created_at, t.row_version,
       o.legal_name, o.display_name, o.organization_kind, o.country_code, o.status AS organization_status,
       o.tax_number_cipher
  FROM directory.tenant_organization t
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE t.tenant_id = $1 AND t.id = $2;

-- name: ListTenantOrganizations :many
-- Keyset pagination on (created_at DESC, id DESC); the caller asks for limit+1 rows to
-- learn whether a next page exists.
SELECT t.id, t.organization_id, t.relationship_role, t.status AS relationship_status,
       t.created_at, o.display_name, o.organization_kind
  FROM directory.tenant_organization t
  JOIN directory.organization o ON o.id = t.organization_id
 WHERE t.tenant_id = $1
   AND (sqlc.narg('role')::text IS NULL OR t.relationship_role = sqlc.narg('role')::text)
   AND (sqlc.narg('q')::text IS NULL OR o.display_name ILIKE '%' || sqlc.narg('q')::text || '%')
   AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (t.created_at, t.id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
 ORDER BY t.created_at DESC, t.id DESC
 LIMIT sqlc.arg('page_size');

-- name: UpdateTenantOrganization :one
-- Optimistic concurrency: the WHERE on row_version makes a stale If-Match update no rows.
-- The trigger platform.tg_touch_row bumps row_version and updated_at.
UPDATE directory.tenant_organization
   SET status = $3,
       tenant_code = $4
 WHERE tenant_id = $1 AND id = $2 AND row_version = $5
RETURNING row_version;
