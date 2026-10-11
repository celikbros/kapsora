-- Bulk member/enrollment maintenance belongs to the existing PROGRAM_MANAGER role.
-- Upgrade existing system roles too; changing the provisioning template alone leaves
-- older tenants unable to use the import screens. Custom roles are not changed.
DO $migration$
DECLARE
    tenant uuid;
    previous_tenant text := current_setting('app.tenant_id', true);
BEGIN
    FOR tenant IN SELECT id FROM platform.tenant LOOP
        PERFORM set_config('app.tenant_id', tenant::text, true);
        INSERT INTO iam.role_permission (tenant_id, role_id, permission_code)
        SELECT tenant_id, id, 'import.execute'
          FROM iam.role
         WHERE tenant_id = tenant AND code = 'PROGRAM_MANAGER' AND is_system_role
        ON CONFLICT (tenant_id, role_id, permission_code) DO NOTHING;
    END LOOP;
    PERFORM set_config('app.tenant_id', coalesce(previous_tenant, ''), true);
END
$migration$;
