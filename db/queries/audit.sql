-- Audit writes. Both tables are append-only and written inside the caller's transaction.

-- name: InsertAuditEvent :one
INSERT INTO audit.event (
    tenant_id, actor_id, membership_id, event_category, action_code,
    resource_type, resource_id, outcome, request_id, trace_id, source_ip,
    user_agent_hash, reason_code, purpose_code, detail_json, before_hash, after_hash
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10, $11,
    $12, $13, $14, $15, $16, $17
)
RETURNING id, occurred_at;

-- name: InsertAccessEvent :one
INSERT INTO audit.access_event (
    tenant_id, actor_id, membership_id, person_id, resource_type, resource_id,
    access_type, data_classification, purpose_code, reason_text, outcome,
    request_id, trace_id, source_ip
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10, $11,
    $12, $13, $14
)
RETURNING id, occurred_at;
