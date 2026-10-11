-- The typed Management reader uses these queries through its transaction and identity port.
-- name: TenantSettingsLockParent :one
SELECT status FROM platform.tenant WHERE id=$1 FOR UPDATE;

-- name: TenantSettingsReadAdmitted :many
SELECT setting_key,value_json,is_sensitive,row_version,updated_at
FROM platform.tenant_setting
WHERE tenant_id=$1 AND setting_key IN ('accommodation.hold_minutes','accommodation.quote_ttl_minutes')
ORDER BY setting_key FOR UPDATE;

-- name: TenantSettingsInsertOrUpdate :exec
INSERT INTO platform.tenant_setting(tenant_id,setting_key,value_json,is_sensitive)
VALUES($1,$2,$3::jsonb,false)
ON CONFLICT(tenant_id,setting_key) DO UPDATE SET value_json=EXCLUDED.value_json
WHERE NOT platform.tenant_setting.is_sensitive;

-- name: TenantSettingsReceiptGet :one
SELECT request_hash,response_status,response_etag,response_body
FROM platform.tenant_setting_command_receipt
WHERE tenant_id=$1 AND actor_id=$2 AND operation_code='accommodation.update' AND key_hash=$3;

-- name: TenantSettingsReceiptInsert :exec
INSERT INTO platform.tenant_setting_command_receipt
(tenant_id,actor_id,membership_id,operation_code,key_hash,request_hash,response_status,response_etag,response_body)
VALUES($1,$2,$3,'accommodation.update',$4,$5,$6,$7,$8);
