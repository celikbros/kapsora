-- KAPSORA migration 000013: cross-tenant relationship count for the shared directory
-- (WP-I1-03).
--
-- A global organization's display name may only be edited by a tenant while no OTHER tenant
-- has a relationship with it; afterwards the row is shared and read-only (v1.2 16.3). Under
-- RLS a tenant cannot see other tenants' relationship rows, so the count runs with the
-- definer's rights. It returns a number only: no tenant learns who else contracts the
-- organization.

CREATE OR REPLACE FUNCTION directory.organization_relationship_count(org uuid)
RETURNS integer
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
    SELECT count(*)::integer
      FROM directory.tenant_organization
     WHERE organization_id = org
       AND status IN ('PENDING', 'ACTIVE', 'SUSPENDED')
$$;

REVOKE ALL ON FUNCTION directory.organization_relationship_count(uuid) FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'kapsora_app') THEN
        GRANT EXECUTE ON FUNCTION directory.organization_relationship_count(uuid) TO kapsora_app;
    END IF;
END
$$;
