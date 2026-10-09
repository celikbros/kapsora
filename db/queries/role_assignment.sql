-- MGT-03A role assignment. All statements run in tenant-bound transactions.

-- name: CanManageTenantRoles :one
SELECT EXISTS (
 SELECT 1 FROM iam.tenant_membership m JOIN iam.actor a ON a.id = m.actor_id
 WHERE m.tenant_id = $1 AND m.id = $2 AND m.actor_id = $3
   AND a.status = 'ACTIVE' AND m.membership_status = 'ACTIVE' AND m.valid_period @> clock_timestamp()::date
   AND EXISTS (SELECT 1 FROM iam.access_grant g JOIN iam.role_permission rp
                 ON rp.tenant_id = g.tenant_id AND rp.role_id = g.role_id
                WHERE g.tenant_id = m.tenant_id AND g.tenant_membership_id = m.id
                  AND g.scope_type = 'TENANT' AND g.valid_period @> clock_timestamp()
                  AND rp.permission_code = 'identity.user.read')
   AND EXISTS (SELECT 1 FROM iam.access_grant g JOIN iam.role_permission rp
                 ON rp.tenant_id = g.tenant_id AND rp.role_id = g.role_id
                WHERE g.tenant_id = m.tenant_id AND g.tenant_membership_id = m.id
                  AND g.scope_type = 'TENANT' AND g.valid_period @> clock_timestamp()
                  AND rp.permission_code = 'identity.role.manage')
);

-- name: GetRoleAssignmentTarget :one
SELECT m.id, m.actor_id, m.membership_status, m.row_version,
       m.valid_period @> clock_timestamp()::date AS membership_valid,
       a.actor_type, a.status AS actor_status
  FROM iam.tenant_membership m JOIN iam.actor a ON a.id = m.actor_id
 WHERE m.tenant_id = $1 AND m.id = $2;

-- name: LockRoleAssignmentTarget :one
SELECT m.id, m.actor_id, m.membership_status, m.row_version,
       m.valid_period @> clock_timestamp()::date AS membership_valid,
       a.actor_type, a.status AS actor_status
  FROM iam.tenant_membership m JOIN iam.actor a ON a.id = m.actor_id
 WHERE m.tenant_id = $1 AND m.id = $2 FOR UPDATE OF m FOR SHARE OF a;

-- name: LockRoleAssignmentTargetRead :one
-- Keep aggregate ETag and child-grant projections in one stable read. All trusted
-- grant writers lock and touch this membership before commit.
SELECT m.id, m.actor_id, m.membership_status, m.row_version,
       m.valid_period @> clock_timestamp()::date AS membership_valid,
       a.actor_type, a.status AS actor_status
  FROM iam.tenant_membership m JOIN iam.actor a ON a.id = m.actor_id
 WHERE m.tenant_id = $1 AND m.id = $2 FOR SHARE OF m;

-- name: GetRoleAssignmentCandidate :one
SELECT id, code, name, is_system_role FROM iam.role WHERE tenant_id = $1 AND code = $2;

-- name: LockRoleAssignmentCandidate :one
SELECT id, code, name, is_system_role FROM iam.role WHERE tenant_id = $1 AND id = $2 FOR UPDATE;

-- name: ListRoleAssignmentPermissions :many
SELECT rp.permission_code, p.sensitivity
  FROM iam.role_permission rp JOIN iam.permission p ON p.code = rp.permission_code
 WHERE rp.tenant_id = $1 AND rp.role_id = $2 ORDER BY rp.permission_code;

-- name: HasCurrentFutureOrPersonGrant :one
SELECT EXISTS (SELECT 1 FROM iam.access_grant g
 WHERE g.tenant_id = $1 AND g.tenant_membership_id = $2
   AND (g.scope_type = 'PERSON' OR
        (NOT isempty(g.valid_period) AND (upper_inf(g.valid_period) OR upper(g.valid_period) > clock_timestamp()))));

-- name: CountCurrentFutureGrants :one
SELECT count(*) FROM iam.access_grant g
 WHERE g.tenant_id = $1 AND g.tenant_membership_id = $2
   AND NOT isempty(g.valid_period)
   AND (upper_inf(g.valid_period) OR upper(g.valid_period) > clock_timestamp());

-- name: HasPersonGrantHistory :one
SELECT EXISTS (SELECT 1 FROM iam.access_grant g
 WHERE g.tenant_id = $1 AND g.tenant_membership_id = $2 AND g.scope_type = 'PERSON');

-- name: ListRoleAssignmentOrganizations :many
SELECT rel.id, rel.created_at, o.display_name, rel.tenant_code
  FROM directory.tenant_organization rel
  JOIN directory.organization o ON o.id = rel.organization_id
  JOIN provider.provider_profile p ON p.tenant_id = rel.tenant_id AND p.tenant_organization_id = rel.id
 WHERE rel.tenant_id = sqlc.arg('tenant_id')
   AND rel.relationship_role = 'PROVIDER' AND rel.status = 'ACTIVE'
   AND rel.valid_period @> clock_timestamp()::date AND o.status = 'ACTIVE'
   AND p.status = 'ACTIVE'
   AND (p.contracted_from IS NULL OR p.contracted_from <= clock_timestamp()::date)
   AND (p.contracted_to IS NULL OR p.contracted_to > clock_timestamp()::date)
   AND (sqlc.narg('after_at')::timestamptz IS NULL OR
        (rel.created_at, rel.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
 ORDER BY rel.created_at DESC, rel.id DESC LIMIT sqlc.arg('page_limit');

-- name: LockEligibleRoleAssignmentOrganization :one
SELECT rel.id, o.display_name, rel.tenant_code
  FROM directory.tenant_organization rel
  JOIN directory.organization o ON o.id = rel.organization_id
  JOIN provider.provider_profile p ON p.tenant_id = rel.tenant_id AND p.tenant_organization_id = rel.id
 WHERE rel.tenant_id = $1 AND rel.id = $2
   AND rel.relationship_role = 'PROVIDER' AND rel.status = 'ACTIVE'
   AND rel.valid_period @> clock_timestamp()::date AND o.status = 'ACTIVE'
   AND p.status = 'ACTIVE'
   AND (p.contracted_from IS NULL OR p.contracted_from <= clock_timestamp()::date)
   AND (p.contracted_to IS NULL OR p.contracted_to > clock_timestamp()::date)
 FOR UPDATE OF rel, o, p;

-- name: ListTenantRoleGrantHistory :many
SELECT g.id, g.created_at, role.code AS role_code, role.name AS role_name,
       role.is_system_role, g.scope_type, g.scope_id,
       CASE WHEN g.scope_type = 'ORGANIZATION' AND rel.id IS NOT NULL THEN rel.id ELSE NULL END AS organization_relationship_id,
       CASE WHEN g.scope_type = 'ORGANIZATION' AND rel.id IS NOT NULL THEN o.display_name ELSE NULL END AS organization_display_name,
       CASE WHEN isempty(g.valid_period) OR lower_inf(g.valid_period) THEN '' ELSE
         to_char(lower(g.valid_period) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END AS valid_from,
       CASE WHEN isempty(g.valid_period) OR upper_inf(g.valid_period) THEN '' ELSE
         to_char(upper(g.valid_period) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END AS valid_to,
       isempty(g.valid_period) AS validity_empty,
       g.valid_period @> clock_timestamp() AS current_grant,
       g.role_id
  FROM iam.access_grant g
  JOIN iam.role role ON role.tenant_id = g.tenant_id AND role.id = g.role_id
  LEFT JOIN directory.tenant_organization rel ON rel.tenant_id = g.tenant_id
       AND rel.id = g.scope_id AND rel.relationship_role = 'PROVIDER' AND g.scope_type = 'ORGANIZATION'
  LEFT JOIN directory.organization o ON o.id = rel.organization_id
 WHERE g.tenant_id = sqlc.arg('tenant_id') AND g.tenant_membership_id = sqlc.arg('membership_id')
   AND (sqlc.narg('after_at')::timestamptz IS NULL OR
        (g.created_at, g.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
 ORDER BY g.created_at DESC, g.id DESC LIMIT sqlc.arg('page_limit');

-- name: GetTenantRoleGrant :one
SELECT g.id, g.created_at, role.code AS role_code, role.name AS role_name,
       role.is_system_role, g.scope_type, g.scope_id, g.role_id,
       CASE WHEN g.scope_type = 'ORGANIZATION' AND rel.id IS NOT NULL THEN rel.id ELSE NULL END AS organization_relationship_id,
       CASE WHEN g.scope_type = 'ORGANIZATION' AND rel.id IS NOT NULL THEN o.display_name ELSE NULL END AS organization_display_name,
       CASE WHEN isempty(g.valid_period) OR lower_inf(g.valid_period) THEN '' ELSE
         to_char(lower(g.valid_period) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END AS valid_from,
       CASE WHEN isempty(g.valid_period) OR upper_inf(g.valid_period) THEN '' ELSE
         to_char(upper(g.valid_period) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END AS valid_to,
       isempty(g.valid_period) AS validity_empty,
       g.valid_period @> clock_timestamp() AS current_grant
  FROM iam.access_grant g
  JOIN iam.role role ON role.tenant_id = g.tenant_id AND role.id = g.role_id
  LEFT JOIN directory.tenant_organization rel ON rel.tenant_id = g.tenant_id
       AND rel.id = g.scope_id AND rel.relationship_role = 'PROVIDER' AND g.scope_type = 'ORGANIZATION'
  LEFT JOIN directory.organization o ON o.id = rel.organization_id
 WHERE g.tenant_id = $1 AND g.tenant_membership_id = $2 AND g.id = $3;

-- name: GetTenantRoleGrantRoleID :one
SELECT role_id FROM iam.access_grant WHERE tenant_id = $1 AND tenant_membership_id = $2 AND id = $3;

-- name: LockTenantRoleGrant :one
SELECT id, role_id, scope_type, scope_id, valid_period @> clock_timestamp() AS current_grant,
       isempty(valid_period) AS validity_empty
  FROM iam.access_grant WHERE tenant_id = $1 AND tenant_membership_id = $2 AND id = $3 FOR UPDATE;

-- name: InsertTenantRoleGrant :one
INSERT INTO iam.access_grant (tenant_id, tenant_membership_id, role_id, scope_type, scope_id, valid_period, granted_by)
VALUES ($1, $2, $3, $4, $5, tstzrange($6::timestamptz, NULL, '[)'), $7) RETURNING id;

-- name: EndTenantRoleGrant :execrows
UPDATE iam.access_grant SET valid_period = tstzrange(lower(valid_period), $4::timestamptz, '[)')
 WHERE tenant_id = $1 AND tenant_membership_id = $2 AND id = $3
   AND NOT isempty(valid_period) AND valid_period @> $4::timestamptz;

-- name: TouchRoleAssignmentMembership :one
UPDATE iam.tenant_membership SET membership_status = membership_status
 WHERE tenant_id = $1 AND id = $2 AND row_version = $3 RETURNING row_version;

-- name: CountCurrentTenantManagersByPermission :one
SELECT count(DISTINCT m.id) FROM iam.tenant_membership m
 JOIN iam.actor a ON a.id = m.actor_id
 JOIN iam.access_grant g ON g.tenant_id = m.tenant_id AND g.tenant_membership_id = m.id
 JOIN iam.role_permission rp ON rp.tenant_id = g.tenant_id AND rp.role_id = g.role_id
 WHERE m.tenant_id = $1 AND m.membership_status = 'ACTIVE'
   AND m.valid_period @> clock_timestamp()::date AND a.status = 'ACTIVE'
   AND g.scope_type = 'TENANT' AND g.valid_period @> clock_timestamp()
   AND rp.permission_code = $2;

-- name: HasCurrentOrFutureGrantForRole :one
SELECT EXISTS (SELECT 1 FROM iam.access_grant
 WHERE tenant_id = $1 AND role_id = $2 AND NOT isempty(valid_period)
   AND (upper_inf(valid_period) OR upper(valid_period) > clock_timestamp()));

-- name: RoleAssignmentNow :one
SELECT clock_timestamp()::timestamptz;

-- name: LockRoleAssignmentSession :one
SELECT actor_id, active_tenant_id, last_seen_at, expires_at, step_up_until, revoked_at
  FROM iam.session WHERE id_hash = $1 FOR SHARE;

-- name: LockRoleAssignmentActor :one
SELECT id, status FROM iam.actor WHERE id = $1 FOR SHARE;
