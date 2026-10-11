-- MGT-03B privileged role change requests. All tenant statements run in WithTenantTx.

-- name: ListPrivilegedRoleCandidates :many
SELECT id, code, name, is_system_role FROM iam.role
 WHERE tenant_id = $1 AND code = ANY($2::text[]) ORDER BY code COLLATE "C";

-- name: ListPrivilegedRolePermissions :many
SELECT rp.permission_code, p.sensitivity FROM iam.role_permission rp
 JOIN iam.permission p ON p.code = rp.permission_code
 WHERE rp.tenant_id = $1 AND rp.role_id = $2
 ORDER BY rp.permission_code COLLATE "C" FOR SHARE OF p;

-- name: RoleChangeCheckerAvailable :one
SELECT EXISTS (
 SELECT 1 FROM iam.tenant_membership m JOIN iam.actor a ON a.id = m.actor_id
 JOIN iam.credential c ON c.actor_id = a.id
 WHERE m.tenant_id = $1 AND a.id <> $2 AND a.id <> $3
   AND a.actor_type = 'HUMAN' AND a.status = 'ACTIVE' AND a.identity_issuer = 'kapsora'
   AND m.membership_status = 'ACTIVE' AND m.valid_period @> ($4::timestamptz)::date
   AND EXISTS (SELECT 1 FROM iam.access_grant g JOIN iam.role_permission rp
                 ON rp.tenant_id = g.tenant_id AND rp.role_id = g.role_id
               WHERE g.tenant_id = m.tenant_id AND g.tenant_membership_id = m.id
                 AND g.scope_type = 'TENANT' AND g.valid_period @> $4::timestamptz
                 AND rp.permission_code = 'identity.user.read')
   AND EXISTS (SELECT 1 FROM iam.access_grant g JOIN iam.role_permission rp
                 ON rp.tenant_id = g.tenant_id AND rp.role_id = g.role_id
               WHERE g.tenant_id = m.tenant_id AND g.tenant_membership_id = m.id
                 AND g.scope_type = 'TENANT' AND g.valid_period @> $4::timestamptz
                 AND rp.permission_code = 'identity.role.manage')
);

-- name: RoleChangePendingForTarget :one
SELECT EXISTS (SELECT 1 FROM iam.role_change_request
 WHERE tenant_id = $1 AND target_membership_id = $2 AND status = 'PENDING');

-- name: RoleChangeGet :one
SELECT * FROM iam.role_change_request WHERE tenant_id = $1 AND id = $2;

-- name: RoleChangeGetForUpdate :one
SELECT * FROM iam.role_change_request WHERE tenant_id = $1 AND id = $2 FOR UPDATE;

-- name: RoleChangeList :many
SELECT id, operation, status, target_membership_id, maker_membership_id, role_code,
       scope_type, reason_code, created_at, row_version, decided_at
  FROM iam.role_change_request
 WHERE tenant_id = sqlc.arg('tenant_id') AND status = sqlc.arg('status')
   AND (sqlc.narg('membership_id')::uuid IS NULL OR target_membership_id = sqlc.narg('membership_id')::uuid)
   AND (sqlc.narg('after_at')::timestamptz IS NULL OR
        (created_at, id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::uuid))
 ORDER BY created_at DESC, id DESC LIMIT sqlc.arg('page_limit');

-- name: RoleChangeInsert :one
INSERT INTO iam.role_change_request (
 tenant_id, operation, target_membership_id, target_actor_id, maker_membership_id,
 maker_actor_id, role_id, role_code, scope_type, permission_snapshot, configuration_hash,
 target_membership_version, revoke_grant_id, revoke_valid_period, reason_code)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'TENANT',$9,$10,$11,$12,$13,$14) RETURNING *;

-- name: RoleChangeDecide :one
UPDATE iam.role_change_request SET status = $3, decided_by_membership_id = $4,
 decided_by_actor_id = $5, decided_at = $6, decision_reason_code = $7,
 applied_grant_id = $8, applied_membership_version = $9, applied_valid_period = $10
 WHERE tenant_id = $1 AND id = $2 AND status = 'PENDING' AND row_version = $11
 RETURNING *;

-- name: RoleChangeReceiptGet :one
SELECT request_hash, request_id, response_status, response_etag, response_body
 FROM iam.role_change_command_receipt
 WHERE tenant_id = $1 AND actor_id = $2 AND command_code = $3 AND key_hash = $4;

-- name: RoleChangeReceiptInsert :exec
INSERT INTO iam.role_change_command_receipt
 (tenant_id, actor_id, command_code, key_hash, request_hash, request_id,
  response_status, response_etag, response_body)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9);

-- name: RoleChangeLockMembership :one
SELECT m.id, m.actor_id, m.membership_status, m.row_version,
       m.valid_period @> ($3::timestamptz)::date AS membership_valid,
       a.actor_type, a.status AS actor_status
  FROM iam.tenant_membership m JOIN iam.actor a ON a.id = m.actor_id
 WHERE m.tenant_id = $1 AND m.id = $2 FOR UPDATE OF m;

-- name: RoleChangeCurrentAuthorityAt :one
SELECT EXISTS (
 SELECT 1 FROM iam.tenant_membership m JOIN iam.actor a ON a.id = m.actor_id
 WHERE m.tenant_id = $1 AND m.id = $2 AND m.actor_id = $3
   AND a.status = 'ACTIVE' AND a.actor_type = 'HUMAN'
   AND m.membership_status = 'ACTIVE' AND m.valid_period @> ($4::timestamptz)::date
   AND EXISTS (SELECT 1 FROM iam.access_grant g JOIN iam.role_permission rp
                 ON rp.tenant_id = g.tenant_id AND rp.role_id = g.role_id
               WHERE g.tenant_id = m.tenant_id AND g.tenant_membership_id = m.id
                 AND g.scope_type = 'TENANT' AND g.valid_period @> $4::timestamptz
                 AND rp.permission_code = 'identity.user.read')
   AND EXISTS (SELECT 1 FROM iam.access_grant g JOIN iam.role_permission rp
                 ON rp.tenant_id = g.tenant_id AND rp.role_id = g.role_id
               WHERE g.tenant_id = m.tenant_id AND g.tenant_membership_id = m.id
                 AND g.scope_type = 'TENANT' AND g.valid_period @> $4::timestamptz
                 AND rp.permission_code = 'identity.role.manage')
);

-- name: RoleChangeGetGrantForUpdate :one
SELECT id, tenant_membership_id, role_id, scope_type, scope_id, valid_period,
       valid_period @> $4::timestamptz AS current_grant
  FROM iam.access_grant WHERE tenant_id = $1 AND tenant_membership_id = $2 AND id = $3 FOR UPDATE;

-- name: RoleChangeCountCurrentFutureGrantsAt :one
SELECT count(*) FROM iam.access_grant
 WHERE tenant_id = $1 AND tenant_membership_id = $2 AND NOT isempty(valid_period)
   AND (upper_inf(valid_period) OR upper(valid_period) > $3::timestamptz
        OR (upper(valid_period) = $3::timestamptz AND upper_inc(valid_period)));

-- name: RoleChangeHasPersonHistory :one
SELECT EXISTS (SELECT 1 FROM iam.access_grant
 WHERE tenant_id = $1 AND tenant_membership_id = $2 AND scope_type = 'PERSON');

-- name: RoleChangeInsertGrant :one
INSERT INTO iam.access_grant (tenant_id, tenant_membership_id, role_id, scope_type,
                              valid_period, granted_by)
VALUES ($1,$2,$3,'TENANT',tstzrange($4::timestamptz,NULL,'[)'),$5) RETURNING id,valid_period;

-- name: RoleChangeEndGrant :one
UPDATE iam.access_grant
 SET valid_period = tstzrange(lower(valid_period), $4::timestamptz,
                             CASE WHEN lower_inc(valid_period) THEN '[)' ELSE '()' END)
 WHERE tenant_id = $1 AND tenant_membership_id = $2 AND id = $3
   AND valid_period = $5 AND valid_period @> $4::timestamptz
 RETURNING valid_period;

-- name: RoleChangeCountManagersAt :one
SELECT count(DISTINCT m.id) FROM iam.tenant_membership m JOIN iam.actor a ON a.id = m.actor_id
 JOIN iam.access_grant g ON g.tenant_id = m.tenant_id AND g.tenant_membership_id = m.id
 JOIN iam.role_permission rp ON rp.tenant_id = g.tenant_id AND rp.role_id = g.role_id
 WHERE m.tenant_id = $1 AND m.membership_status = 'ACTIVE' AND m.valid_period @> ($3::timestamptz)::date
   AND a.status = 'ACTIVE' AND g.scope_type = 'TENANT' AND g.valid_period @> $3::timestamptz
   AND rp.permission_code = $2;
