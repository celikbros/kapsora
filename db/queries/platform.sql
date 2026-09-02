-- Platform queries. Tenant lookups are global (no RLS); everything else runs inside a
-- tenant-bound transaction.

-- name: GetTenantByID :one
SELECT id, code, legal_name, display_name, status, default_locale, default_time_zone,
       default_currency, data_region, created_at, updated_at, row_version
  FROM platform.tenant
 WHERE id = $1;

-- name: GetTenantByCode :one
SELECT id, code, legal_name, display_name, status, default_locale, default_time_zone,
       default_currency, data_region, created_at, updated_at, row_version
  FROM platform.tenant
 WHERE code = $1;

-- name: ListTenantsForActor :many
SELECT t.id, t.code, t.legal_name, t.display_name, t.status, t.default_locale,
       t.default_time_zone, t.default_currency, t.data_region, t.created_at, t.updated_at,
       t.row_version
  FROM platform.tenant t
  JOIN iam.tenant_membership m ON m.tenant_id = t.id
 WHERE m.actor_id = $1
   AND m.membership_status = 'ACTIVE'
   AND m.valid_period @> CURRENT_DATE
   AND t.status = 'ACTIVE'
 ORDER BY t.display_name;

-- name: NextReferenceNumber :one
-- Atomically reserves the next value of a tenant sequence. Row lock serialises callers.
INSERT INTO platform.number_sequence (tenant_id, sequence_key, period_key, prefix)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id, sequence_key, period_key)
DO UPDATE SET next_value = platform.number_sequence.next_value + 1
RETURNING prefix, next_value - 1 AS reserved_value, padding_width;
