-- Service → entitlement mapping (WP-I5-05 section 2.1): the sentence that says which
-- balance a catalogue service spends, and how much of it.
--
-- Every write here runs against a DRAFT plan version. That is not asserted in SQL because
-- the trigger of migration 000035 asserts it for every statement, including the ones
-- nobody has written yet.

-- name: ListServiceEntitlementMappings :many
-- The set of one plan version, with the two codes a person reads it by. Ordered by the
-- service code so the list a screen shows is stable between calls.
SELECT m.id, m.plan_version_id, m.service_definition_id, m.entitlement_definition_id,
       m.unit_factor::text AS unit_factor,
       m.valid_from, m.valid_to, m.created_at, m.row_version,
       sd.code AS service_code, sd.name AS service_name,
       ed.code AS entitlement_code, ed.unit_type AS unit_type
  FROM benefit.service_entitlement_mapping m
  JOIN catalog.service_definition sd
       ON sd.tenant_id = m.tenant_id AND sd.id = m.service_definition_id
  JOIN benefit.entitlement_definition ed
       ON ed.tenant_id = m.tenant_id AND ed.id = m.entitlement_definition_id
 WHERE m.tenant_id = sqlc.arg('tenant_id')
   AND m.plan_version_id = sqlc.arg('plan_version_id')
 ORDER BY sd.code, m.id;

-- name: DeleteServiceEntitlementMappings :execrows
-- The whole set of one version. A replace deletes and re-inserts rather than merging: a
-- merge would leave behind a mapping the caller believed they had removed.
DELETE FROM benefit.service_entitlement_mapping
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND plan_version_id = sqlc.arg('plan_version_id');

-- name: CreateServiceEntitlementMapping :one
INSERT INTO benefit.service_entitlement_mapping (
    tenant_id, plan_version_id, service_definition_id, entitlement_definition_id,
    unit_factor, valid_from, valid_to, created_by, updated_by)
VALUES (sqlc.arg('tenant_id'), sqlc.arg('plan_version_id'), sqlc.arg('service_definition_id'),
        sqlc.arg('entitlement_definition_id'), sqlc.arg('unit_factor')::text::numeric,
        sqlc.narg('valid_from'), sqlc.narg('valid_to'),
        sqlc.narg('actor_id'), sqlc.narg('actor_id'))
RETURNING id;

-- name: ListEligibilityMappings :many
-- What the eligibility resolver reads: the entitlement code a service draws from and the
-- factor it draws it at, for the plan version the check resolved. The service date filter
-- is here rather than in Go because a mapping outside its own window is not a mapping.
SELECT m.service_definition_id, ed.code AS entitlement_code,
       m.unit_factor::text AS unit_factor
  FROM benefit.service_entitlement_mapping m
  JOIN benefit.entitlement_definition ed
       ON ed.tenant_id = m.tenant_id AND ed.id = m.entitlement_definition_id
 WHERE m.tenant_id = sqlc.arg('tenant_id')
   AND m.plan_version_id = sqlc.arg('plan_version_id')
   AND (m.valid_from IS NULL OR m.valid_from <= sqlc.arg('service_date')::date)
   AND (m.valid_to IS NULL OR m.valid_to > sqlc.arg('service_date')::date);

-- name: ListEntitlementDefinitionsForMapping :many
-- The definitions a mapping of this version may point at, by code. The put command
-- resolves the caller's entitlement code against this list, so a code that belongs to a
-- different version is a field error rather than a foreign key violation.
SELECT id, code, unit_type
  FROM benefit.entitlement_definition
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND plan_version_id = sqlc.arg('plan_version_id')
 ORDER BY code;

-- name: ListActiveServiceDefinitionsByID :many
-- The catalogue rows a mapping names, for the existence and active checks.
SELECT id, code, name, default_unit_type, active
  FROM catalog.service_definition
 WHERE tenant_id = sqlc.arg('tenant_id')
   AND id = ANY(sqlc.arg('ids')::uuid[]);

-- name: ListDraftPlanVersionsForSeed :many
-- Every draft plan version of a tenant, used by the seed to attach mappings to the
-- versions a demo world happens to have.
SELECT v.id, v.plan_id, p.code AS plan_code
  FROM benefit.plan_version v
  JOIN benefit.plan p ON p.tenant_id = v.tenant_id AND p.id = v.plan_id
 WHERE v.tenant_id = sqlc.arg('tenant_id')
   AND v.status = 'DRAFT'
 ORDER BY p.code, v.version_no;
