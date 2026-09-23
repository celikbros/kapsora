-- Financial handoff: every join stays within the tenant and the case's provider.
-- Clinical references in LockClaimSource are internal only, never source response fields.
-- name: ListClaimSources :many
SELECT c.id, c.opened_at, c.row_version, r.request_reference, r.service_date,
       concat_ws(' ', p.first_name, p.middle_name, p.last_name)::text AS person_display_name
FROM health.health_case c
JOIN service.service_request r ON r.tenant_id=c.tenant_id AND r.id=c.service_request_id
  AND r.person_id=c.person_id AND r.program_id=c.program_id AND r.enrollment_id=c.enrollment_id
  AND r.provider_tenant_organization_id=c.provider_organization_id
JOIN party.person p ON p.tenant_id=c.tenant_id AND p.id=c.person_id
WHERE c.tenant_id=sqlc.arg('tenant_id') AND c.case_type='OUTPATIENT'
  AND (sqlc.narg('scope_ids')::uuid[] IS NULL OR c.provider_organization_id=ANY(sqlc.narg('scope_ids')::uuid[]))
  AND r.status IN ('APPROVED','PARTIALLY_APPROVED')
  AND EXISTS (SELECT 1 FROM service.authorization a WHERE a.tenant_id=c.tenant_id AND a.request_id=r.id
    AND a.status IN ('ACTIVE','PARTIALLY_USED') AND a.valid_to>sqlc.arg('now')::timestamptz)
  AND NOT EXISTS (SELECT 1 FROM claim.claim cl WHERE cl.tenant_id=c.tenant_id AND cl.case_id=c.id AND cl.status<>'CANCELLED')
  AND (sqlc.narg('after_id')::uuid IS NULL OR (c.opened_at,c.id)<(sqlc.narg('after_time')::timestamptz,sqlc.narg('after_id')::uuid))
ORDER BY c.opened_at DESC,c.id DESC LIMIT sqlc.arg('page_size');

-- name: LockClaimSource :one
SELECT c.id, c.person_id, c.program_id, c.enrollment_id, c.provider_organization_id,
       c.opened_at, c.row_version, r.request_reference, r.service_date,
       concat_ws(' ', p.first_name,p.middle_name,p.last_name)::text AS person_display_name,
       EXISTS (SELECT 1 FROM claim.claim cl WHERE cl.tenant_id=c.tenant_id AND cl.case_id=c.id AND cl.status<>'CANCELLED') AS already_raised,
       COALESCE((SELECT jsonb_agg(a.id) FROM service.authorization a WHERE a.tenant_id=c.tenant_id AND a.request_id=r.id
         AND a.status IN ('ACTIVE','PARTIALLY_USED') AND a.valid_to>sqlc.arg('now')::timestamptz),'[]'::jsonb) AS authorization_ids,
       COALESCE((SELECT jsonb_agg(d.id) FROM health.encounter e JOIN health.diagnosis d ON d.tenant_id=e.tenant_id AND d.encounter_id=e.id
         WHERE e.tenant_id=c.tenant_id AND e.case_id=c.id AND e.ended_at IS NOT NULL AND d.diagnosis_type='PRIMARY'),'[]'::jsonb) AS diagnosis_ids,
       EXISTS (SELECT 1 FROM health.encounter e WHERE e.tenant_id=c.tenant_id AND e.case_id=c.id AND e.ended_at IS NULL) AS ongoing
FROM health.health_case c
JOIN service.service_request r ON r.tenant_id=c.tenant_id AND r.id=c.service_request_id
  AND r.person_id=c.person_id AND r.program_id=c.program_id AND r.enrollment_id=c.enrollment_id
  AND r.provider_tenant_organization_id=c.provider_organization_id
JOIN party.person p ON p.tenant_id=c.tenant_id AND p.id=c.person_id
WHERE c.tenant_id=sqlc.arg('tenant_id') AND c.id=sqlc.arg('id') AND c.case_type='OUTPATIENT'
  AND r.status IN ('APPROVED','PARTIALLY_APPROVED')
  AND (sqlc.narg('scope_ids')::uuid[] IS NULL OR c.provider_organization_id=ANY(sqlc.narg('scope_ids')::uuid[]))
FOR UPDATE OF c;

-- name: ClaimSourceLines :many
SELECT ai.service_definition_id, sd.code AS service_code, sd.name AS service_name, ri.unit_type,
       (ai.approved_quantity-ai.consumed_quantity)::text AS quantity,
       COALESCE((SELECT jsonb_agg(m.id) FROM health.medical_report m
         JOIN health.medical_report_service ms ON ms.tenant_id=m.tenant_id AND ms.report_id=m.id
         WHERE m.tenant_id=ai.tenant_id AND m.case_id=sqlc.arg('case_id') AND m.person_id=sqlc.arg('person_id')
           AND m.issuing_provider_organization_id=sqlc.arg('provider_id') AND m.status='APPROVED'
           AND m.valid_from<=sqlc.arg('service_date')::date AND m.valid_to>=sqlc.arg('service_date')::date
           AND ms.service_definition_id=ai.service_definition_id),'[]'::jsonb) AS report_ids
FROM service.authorization_item ai
JOIN catalog.service_definition sd ON sd.tenant_id=ai.tenant_id AND sd.id=ai.service_definition_id
JOIN service.service_request_item ri ON ri.tenant_id=ai.tenant_id AND ri.id=ai.request_item_id
WHERE ai.tenant_id=sqlc.arg('tenant_id') AND ai.authorization_id=sqlc.arg('authorization_id')
  AND ai.approved_quantity>ai.consumed_quantity
ORDER BY ri.line_no;

-- name: ClaimSourceAlreadyRaised :one
-- Called AFTER acquiring the case lock: a new READ COMMITTED snapshot sees a competing
-- handoff that committed while LockClaimSource was waiting.
SELECT EXISTS (SELECT 1 FROM claim.claim cl WHERE cl.tenant_id=sqlc.arg('tenant_id')
 AND cl.case_id=sqlc.arg('case_id') AND cl.status<>'CANCELLED') AS already_raised;
