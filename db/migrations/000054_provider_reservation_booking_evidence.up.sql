-- The desk may attach nonclinical no-show evidence to its own confirmed booking.
-- The application enforces the target/type/provider boundary; generic link/unlink is excluded.
INSERT INTO iam.permission (code, description, sensitivity) VALUES
    ('document.booking_evidence.link', 'Kendi otelinin onaylı rezervasyonuna gelmeme kanıtı bağlama', 'NORMAL')
ON CONFLICT (code) DO NOTHING;

-- Upgrade system roles in existing tenants; custom roles are deliberately excluded.
DO $migration$
DECLARE
    tenant uuid;
    previous_tenant text := current_setting('app.tenant_id', true);
BEGIN
    FOR tenant IN SELECT id FROM platform.tenant LOOP
        PERFORM set_config('app.tenant_id', tenant::text, true);
        INSERT INTO iam.role_permission (tenant_id, role_id, permission_code)
        SELECT tenant_id, id, 'document.booking_evidence.link'
          FROM iam.role
         WHERE tenant_id = tenant AND code = 'PROVIDER_RESERVATION' AND is_system_role
        ON CONFLICT (tenant_id, role_id, permission_code) DO NOTHING;
    END LOOP;
    PERFORM set_config('app.tenant_id', coalesce(previous_tenant, ''), true);
END
$migration$;
