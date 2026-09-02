-- KAPSORA migration 000011: fix outbox deduplication semantics and let the scheduler
-- create audit partitions without table-creation privileges.
--
-- 1. The baseline constraint uq_outbox_dedupe was UNIQUE NULLS NOT DISTINCT over
--    (tenant_id, event_type, deduplication_key). With NULLS NOT DISTINCT two events of the
--    same type for the same tenant *without* a deduplication key collided, so only the first
--    event of each type could ever be published. Deduplication must apply only when a key is
--    supplied; a NULL tenant (platform-level events) with the same key must still deduplicate.

ALTER TABLE system.outbox_event DROP CONSTRAINT uq_outbox_dedupe;

CREATE UNIQUE INDEX uq_outbox_dedupe
    ON system.outbox_event (tenant_id, event_type, deduplication_key)
    NULLS NOT DISTINCT
    WHERE deduplication_key IS NOT NULL;

-- 2. audit.ensure_month_partition creates tables, which the application role may not do.
--    Run it with the definer's rights, pin the search path, and grant execution to the
--    application role only (scheduler job audit.ensure_partitions).

ALTER FUNCTION audit.ensure_month_partition(regclass, date)
    SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp;

REVOKE ALL ON FUNCTION audit.ensure_month_partition(regclass, date) FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'kapsora_app') THEN
        GRANT EXECUTE ON FUNCTION audit.ensure_month_partition(regclass, date) TO kapsora_app;
    END IF;
END
$$;
