-- Only the two accommodation timing settings are admitted to this management API.
INSERT INTO iam.permission(code,description,sensitivity) VALUES
 ('platform.tenant_settings.read','Kurum konaklama ayarlarini okuma','NORMAL'),
 ('platform.tenant_settings.manage','Kurum konaklama ayarlarini degistirme','SENSITIVE')
ON CONFLICT (code) DO NOTHING;

DO $migration$
DECLARE tenant uuid; previous_tenant text := current_setting('app.tenant_id', true);
BEGIN
 FOR tenant IN SELECT id FROM platform.tenant LOOP
  PERFORM set_config('app.tenant_id', tenant::text, true);
  INSERT INTO iam.role_permission(tenant_id,role_id,permission_code)
   SELECT r.tenant_id,r.id,p.code FROM iam.role r
   CROSS JOIN (VALUES ('platform.tenant_settings.read'),('platform.tenant_settings.manage')) p(code)
   WHERE r.tenant_id=tenant AND r.code='TENANT_ADMIN' AND r.is_system_role
   ON CONFLICT (tenant_id,role_id,permission_code) DO NOTHING;
 END LOOP;
 PERFORM set_config('app.tenant_id',coalesce(previous_tenant,''),true);
END $migration$;

ALTER TABLE platform.tenant_setting ADD COLUMN row_version bigint NOT NULL DEFAULT 1;
DROP TRIGGER tg_touch_updated_at ON platform.tenant_setting;
SELECT platform.attach_touch_row('platform.tenant_setting'::regclass);

CREATE TABLE platform.tenant_setting_command_receipt (
 tenant_id uuid NOT NULL REFERENCES platform.tenant(id) ON DELETE RESTRICT,
 actor_id uuid NOT NULL REFERENCES iam.actor(id) ON DELETE RESTRICT,
 membership_id uuid NOT NULL,
 operation_code text NOT NULL CHECK (operation_code='accommodation.update'),
 key_hash bytea NOT NULL CHECK (octet_length(key_hash)=32),
 request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
 response_status integer NOT NULL CHECK (response_status=200),
 response_etag text NOT NULL CHECK (char_length(response_etag) BETWEEN 2 AND 130),
 response_body bytea NOT NULL CHECK (octet_length(response_body) BETWEEN 2 AND 8192),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,actor_id,operation_code,key_hash),
 CONSTRAINT fk_setting_receipt_membership FOREIGN KEY (tenant_id,membership_id)
   REFERENCES iam.tenant_membership(tenant_id,id) ON DELETE RESTRICT
);
SELECT platform.enable_tenant_rls('platform.tenant_setting_command_receipt'::regclass);
SELECT platform.make_append_only('platform.tenant_setting_command_receipt'::regclass);
CREATE FUNCTION platform.tg_settings_receipt_actor() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.actor_id IS DISTINCT FROM platform.current_actor_id() THEN
  RAISE EXCEPTION 'setting receipt actor mismatch' USING ERRCODE='42501';
 END IF;
 IF NOT EXISTS (SELECT 1 FROM iam.tenant_membership m
                WHERE m.tenant_id=NEW.tenant_id AND m.id=NEW.membership_id AND m.actor_id=NEW.actor_id) THEN
  RAISE EXCEPTION 'setting receipt membership mismatch' USING ERRCODE='23503';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER tg_settings_receipt_actor BEFORE INSERT ON platform.tenant_setting_command_receipt
 FOR EACH ROW EXECUTE FUNCTION platform.tg_settings_receipt_actor();
SELECT platform.grant_app_schema_usage('platform');
