-- Clinical staff must read service definitions and diagnosis codes to request care.
-- Catalog maintenance stays separate. Upgrade only the existing system role, using
-- each tenant's context; custom roles and the other provider roles are untouched.
DO $migration$
DECLARE
    tenant uuid;
    previous_tenant text := current_setting('app.tenant_id', true);
BEGIN
    FOR tenant IN SELECT id FROM platform.tenant LOOP
        PERFORM set_config('app.tenant_id', tenant::text, true);
        INSERT INTO iam.role_permission (tenant_id, role_id, permission_code)
        SELECT tenant_id, id, 'catalog.read'
          FROM iam.role
         WHERE tenant_id = tenant AND code = 'PROVIDER_STAFF' AND is_system_role
        ON CONFLICT (tenant_id, role_id, permission_code) DO NOTHING;
    END LOOP;
    PERFORM set_config('app.tenant_id', coalesce(previous_tenant, ''), true);
END
$migration$;
