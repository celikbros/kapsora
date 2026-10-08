-- Authorization queries (WP-I1-02): memberships, permission resolution, roles, grants and
-- tenant provisioning. Membership lookups run under db.WithActorTx (policy
-- actor_self_membership); everything tenant-owned runs under db.WithTenantTx.

-- Management Users reads: each query runs under db.WithTenantTx. The permission and
-- TENANT scope must belong to the same active grant; flattened request-context fields
-- cannot establish this correlation.
-- name: CanReadTenantUsers :one
SELECT EXISTS (
  SELECT 1 FROM iam.tenant_membership m
  JOIN iam.actor a ON a.id = m.actor_id
  JOIN iam.access_grant g ON g.tenant_id = m.tenant_id AND g.tenant_membership_id = m.id
  JOIN iam.role_permission rp ON rp.tenant_id = g.tenant_id AND rp.role_id = g.role_id
 WHERE m.tenant_id = $1 AND m.id = $2 AND m.actor_id = $3
   AND a.status = 'ACTIVE' AND m.membership_status = 'ACTIVE' AND m.valid_period @> CURRENT_DATE
   AND g.scope_type = 'TENANT' AND g.valid_period @> clock_timestamp()
   AND rp.permission_code = 'identity.user.read'
);

-- name: CanManageTenantUsers :one
SELECT EXISTS (
  SELECT 1 FROM iam.tenant_membership m
  JOIN iam.actor a ON a.id = m.actor_id
  JOIN iam.access_grant g ON g.tenant_id = m.tenant_id AND g.tenant_membership_id = m.id
  JOIN iam.role_permission rp ON rp.tenant_id = g.tenant_id AND rp.role_id = g.role_id
 WHERE m.tenant_id = $1 AND m.id = $2 AND m.actor_id = $3
   AND a.status = 'ACTIVE' AND m.membership_status = 'ACTIVE' AND m.valid_period @> CURRENT_DATE
   AND g.scope_type = 'TENANT' AND g.valid_period @> clock_timestamp()
   AND rp.permission_code = 'identity.user.manage'
);

-- name: LockTenantForUserManagement :one
SELECT id FROM platform.tenant WHERE id = $1 FOR UPDATE;

-- name: LockTenantUserForSuspension :one
SELECT id, actor_id, membership_status, row_version
  FROM iam.tenant_membership
 WHERE tenant_id = $1 AND id = $2 FOR UPDATE;

-- name: IsUsableTenantUserManager :one
SELECT EXISTS (
  SELECT 1 FROM iam.tenant_membership m
  JOIN iam.actor a ON a.id = m.actor_id
  JOIN iam.access_grant g ON g.tenant_id = m.tenant_id AND g.tenant_membership_id = m.id
  JOIN iam.role_permission rp ON rp.tenant_id = g.tenant_id AND rp.role_id = g.role_id
 WHERE m.tenant_id = $1 AND m.id = $2
   AND a.status = 'ACTIVE' AND m.membership_status = 'ACTIVE' AND m.valid_period @> CURRENT_DATE
   AND g.scope_type = 'TENANT' AND g.valid_period @> clock_timestamp()
   AND rp.permission_code = 'identity.user.manage'
);

-- name: CountOtherUsableTenantUserManagers :one
SELECT count(DISTINCT m.id) FROM iam.tenant_membership m
  JOIN iam.actor a ON a.id = m.actor_id
  JOIN iam.access_grant g ON g.tenant_id = m.tenant_id AND g.tenant_membership_id = m.id
  JOIN iam.role_permission rp ON rp.tenant_id = g.tenant_id AND rp.role_id = g.role_id
 WHERE m.tenant_id = $1 AND m.id <> $2
   AND a.status = 'ACTIVE' AND m.membership_status = 'ACTIVE' AND m.valid_period @> CURRENT_DATE
   AND g.scope_type = 'TENANT' AND g.valid_period @> clock_timestamp()
   AND rp.permission_code = 'identity.user.manage';

-- name: SuspendTenantUser :one
UPDATE iam.tenant_membership SET membership_status = 'SUSPENDED'
 WHERE tenant_id = $1 AND id = $2 AND membership_status = 'ACTIVE' AND row_version = $3
 RETURNING row_version;

-- name: ListTenantUsers :many
SELECT m.id, a.display_name, a.actor_type, a.status AS actor_status,
       m.membership_status,
       CASE WHEN isempty(m.valid_period) OR lower_inf(m.valid_period) OR lower(m.valid_period) = '-infinity'::date
            THEN '' ELSE lower(m.valid_period)::text END AS valid_from,
       CASE WHEN isempty(m.valid_period) OR upper_inf(m.valid_period) OR upper(m.valid_period) = 'infinity'::date
            THEN '' ELSE upper(m.valid_period)::text END AS valid_to,
       isempty(m.valid_period) AS validity_empty, m.created_at, m.row_version
  FROM iam.tenant_membership m JOIN iam.actor a ON a.id = m.actor_id
 WHERE m.tenant_id = $1
   AND (sqlc.narg('membership_status')::text IS NULL OR m.membership_status = sqlc.narg('membership_status'))
   AND (sqlc.narg('after_at')::timestamptz IS NULL
        OR (m.created_at, m.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
 ORDER BY m.created_at DESC, m.id DESC
 LIMIT sqlc.arg('page_limit');

-- name: GetTenantUser :one
SELECT m.id, a.display_name, a.actor_type, a.status AS actor_status,
       m.membership_status,
       CASE WHEN isempty(m.valid_period) OR lower_inf(m.valid_period) OR lower(m.valid_period) = '-infinity'::date
            THEN '' ELSE lower(m.valid_period)::text END AS valid_from,
       CASE WHEN isempty(m.valid_period) OR upper_inf(m.valid_period) OR upper(m.valid_period) = 'infinity'::date
            THEN '' ELSE upper(m.valid_period)::text END AS valid_to,
       isempty(m.valid_period) AS validity_empty, m.created_at, m.row_version
  FROM iam.tenant_membership m JOIN iam.actor a ON a.id = m.actor_id
 WHERE m.tenant_id = $1 AND m.id = $2;

-- name: ListTenantUserRoles :many
SELECT role.code, role.name, role.is_system_role, g.scope_type,
       CASE WHEN lower_inf(g.valid_period) OR lower(g.valid_period) = '-infinity'::timestamptz
            THEN '' ELSE to_char(lower(g.valid_period) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END AS valid_from,
       CASE WHEN upper_inf(g.valid_period) OR upper(g.valid_period) = 'infinity'::timestamptz
            THEN '' ELSE to_char(upper(g.valid_period) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END AS valid_to,
       isempty(g.valid_period) AS validity_empty
  FROM iam.access_grant g
  JOIN iam.role role ON role.tenant_id = g.tenant_id AND role.id = g.role_id
 WHERE g.tenant_id = $1 AND g.tenant_membership_id = $2
 ORDER BY role.code, lower(g.valid_period), g.id;

-- name: FindActiveMembership :one
SELECT m.id, m.tenant_id, t.code, t.display_name, t.status, t.default_locale, t.default_time_zone
  FROM iam.tenant_membership m
  JOIN platform.tenant t ON t.id = m.tenant_id
 WHERE m.actor_id = $1
   AND m.tenant_id = $2
   AND m.membership_status = 'ACTIVE'
   AND m.valid_period @> CURRENT_DATE
   AND t.status = 'ACTIVE';

-- name: ListMembershipsForActor :many
SELECT m.id, m.tenant_id, t.code, t.display_name, t.status, t.default_locale, t.default_time_zone
  FROM iam.tenant_membership m
  JOIN platform.tenant t ON t.id = m.tenant_id
 WHERE m.actor_id = $1
   AND m.membership_status = 'ACTIVE'
   AND m.valid_period @> CURRENT_DATE
   AND t.status = 'ACTIVE'
 ORDER BY t.display_name, t.id;

-- name: ListGrantsForMembership :many
-- Every grant of the membership valid now, one row per permission of its role, so the caller
-- can tell which permissions came with which scope and apply only the grants of the app a
-- request comes from. A role with no permissions still yields its grant, with a null code.
SELECT g.id, g.scope_type, g.scope_id, rp.permission_code
  FROM iam.access_grant g
  LEFT JOIN iam.role_permission rp ON rp.tenant_id = g.tenant_id AND rp.role_id = g.role_id
 WHERE g.tenant_id = $1
   AND g.tenant_membership_id = $2
   AND g.valid_period @> clock_timestamp()
 ORDER BY g.id, rp.permission_code;

-- name: ListPermissionCodes :many
SELECT code FROM iam.permission ORDER BY code;

-- ---------------------------------------------------------------- provisioning

-- name: CreateTenant :one
INSERT INTO platform.tenant (code, legal_name, display_name, status, default_locale, default_time_zone, default_currency)
VALUES ($1, $2, $3, 'PROVISIONING', $4, $5, $6)
RETURNING id;

-- name: ActivateTenant :exec
UPDATE platform.tenant SET status = 'ACTIVE' WHERE id = $1 AND status = 'PROVISIONING';

-- name: GetTenantIDByCode :one
SELECT id FROM platform.tenant WHERE code = $1;

-- name: CreateRole :one
INSERT INTO iam.role (tenant_id, code, name, description, is_system_role)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id, code) DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description
RETURNING id;

-- name: AddRolePermission :exec
INSERT INTO iam.role_permission (tenant_id, role_id, permission_code)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: AddRolePermissionCounted :execrows
-- AddRolePermission, reporting whether a row was written (1) or was already there (0).
INSERT INTO iam.role_permission (tenant_id, role_id, permission_code)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: GetRoleByCode :one
SELECT id, code, name, is_system_role FROM iam.role WHERE tenant_id = $1 AND code = $2;

-- name: SeedIdentifierType :exec
INSERT INTO party.identifier_type (tenant_id, code, display_name, is_sensitive, uniqueness_scope)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id, code) DO NOTHING;

-- name: SeedRelationshipType :exec
INSERT INTO party.relationship_type (tenant_id, code, display_name, is_directional)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id, code) DO NOTHING;

-- name: SeedMembershipType :exec
INSERT INTO party.membership_type (tenant_id, code, display_name, requires_principal)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id, code) DO NOTHING;

-- name: SeedProgramType :exec
INSERT INTO benefit.program_type (tenant_id, code, display_name)
VALUES ($1, $2, $3)
ON CONFLICT (tenant_id, code) DO NOTHING;

-- ---------------------------------------------------------------- memberships and grants

-- name: FindMembershipAnyStatus :one
SELECT id, membership_status FROM iam.tenant_membership WHERE tenant_id = $1 AND actor_id = $2
 ORDER BY created_at DESC LIMIT 1;

-- name: CreateMembership :one
INSERT INTO iam.tenant_membership (tenant_id, actor_id, membership_status, created_by)
VALUES ($1, $2, 'ACTIVE', $3)
RETURNING id;

-- name: FindAccessGrant :one
SELECT id FROM iam.access_grant
 WHERE tenant_id = $1 AND tenant_membership_id = $2 AND role_id = $3
   AND scope_type = $4 AND scope_id IS NOT DISTINCT FROM $5
   AND upper_inf(valid_period);

-- name: CreateAccessGrant :one
INSERT INTO iam.access_grant (tenant_id, tenant_membership_id, role_id, scope_type, scope_id, granted_by, grant_reason)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id;

-- name: CreateTenantOrganization :one
-- Seed helper: a global organization plus its tenant relationship in one statement pair is
-- WP-I1-03's job; this only supports demo data and does not deduplicate by tax number.
INSERT INTO directory.tenant_organization (tenant_id, organization_id, relationship_role, tenant_code)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: CreateGlobalOrganization :one
INSERT INTO directory.organization (legal_name, display_name, organization_kind, country_code)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: FindTenantOrganizationByCode :one
SELECT id FROM directory.tenant_organization WHERE tenant_id = $1 AND tenant_code = $2;
