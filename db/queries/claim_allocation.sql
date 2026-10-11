-- name: ClaimInpatientStayPlan :one
SELECT s.id, s.authorization_id, s.status, s.over_authorization,
       COALESCE(s.actual_days::text, '')::text AS actual_days,
       COALESCE(s.authorized_days::text, '')::text AS authorized_days
  FROM health.inpatient_stay s
 WHERE s.tenant_id = sqlc.arg('tenant_id')
   AND s.id = sqlc.arg('stay_id')
   AND s.case_id = sqlc.arg('case_id')
   AND s.provider_organization_id = sqlc.arg('provider_organization_id')
   AND s.authorization_id = sqlc.arg('authorization_id');

-- name: ClaimInpatientExtensions :many
SELECT e.authorization_id,
       COALESCE((SELECT sum(ai.approved_quantity)::text
                   FROM service.authorization_item ai
                  WHERE ai.tenant_id = e.tenant_id
                    AND ai.authorization_id = e.authorization_id), '')::text AS approved_days
  FROM health.stay_extension e
 WHERE e.tenant_id = sqlc.arg('tenant_id')
   AND e.stay_id = sqlc.arg('stay_id')
   AND e.status = 'APPROVED'
 ORDER BY e.sequence_no;

-- name: CreateClaimLineAllocation :exec
INSERT INTO claim.claim_line_authorization_allocation
    (tenant_id, version_id, line_id, authorization_id, allocation_order,
     planned_quantity, applied_quantity, idempotency_key)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('version_id'), sqlc.arg('line_id'),
        sqlc.arg('authorization_id'), sqlc.arg('allocation_order'),
        sqlc.arg('planned_quantity'), sqlc.arg('applied_quantity'), sqlc.arg('idempotency_key'));

-- name: ListClaimVersionAllocations :many
SELECT a.version_id, a.line_id, a.authorization_id, a.allocation_order,
       a.planned_quantity::text AS planned_quantity,
       a.applied_quantity::text AS applied_quantity, a.idempotency_key,
       l.service_definition_id
  FROM claim.claim_line_authorization_allocation a
  JOIN claim.claim_line l ON l.tenant_id = a.tenant_id AND l.id = a.line_id
 WHERE a.tenant_id = sqlc.arg('tenant_id') AND a.version_id = sqlc.arg('version_id')
 ORDER BY l.line_no, a.allocation_order;



-- name: ClaimCaseType :one
SELECT case_type FROM health.health_case
 WHERE tenant_id = sqlc.arg('tenant_id') AND id = sqlc.arg('case_id');
