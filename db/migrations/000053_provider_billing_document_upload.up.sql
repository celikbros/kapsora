-- Billing clerks need to upload the scanned invoice image before submitting an invoice.
-- The document service binds an organization-scoped upload to that organization.
-- Upgrade existing system roles without changing custom roles.
DO $migration$
DECLARE
    tenant uuid;
    previous_tenant text := current_setting('app.tenant_id', true);
BEGIN
    FOR tenant IN SELECT id FROM platform.tenant LOOP
        PERFORM set_config('app.tenant_id', tenant::text, true);
        INSERT INTO iam.role_permission (tenant_id, role_id, permission_code)
        SELECT tenant_id, id, 'document.upload'
          FROM iam.role
         WHERE tenant_id = tenant AND code = 'PROVIDER_BILLING' AND is_system_role
        ON CONFLICT (tenant_id, role_id, permission_code) DO NOTHING;
    END LOOP;
    PERFORM set_config('app.tenant_id', coalesce(previous_tenant, ''), true);
END
$migration$;
