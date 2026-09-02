-- KAPSORA migration 000009: actors may read their own memberships before a tenant is
-- selected (login, GET /api/v1/tenants, tenant switch validation).
--
-- Policies on a table are permissive and OR-ed: tenant_isolation keeps every other access
-- tenant-bound; this SELECT-only policy exposes rows whose actor_id equals app.actor_id,
-- which db.WithActorTx sets. Writes still require a tenant context.

CREATE POLICY actor_self_membership ON iam.tenant_membership
    FOR SELECT
    USING (actor_id = platform.current_actor_id());
