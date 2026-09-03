-- KAPSORA migration 000012: local password credentials and browser sessions (WP-I1-01).
--
-- KAPSORA authenticates its own users (ADR-022). A login-capable actor is an iam.actor row
-- with identity_issuer = 'kapsora' and identity_subject = the normalised user name, so the
-- existing UNIQUE (identity_issuer, identity_subject) already enforces user-name uniqueness
-- and an external identity provider can be added later beside it without a data migration.

CREATE TABLE iam.credential (
    actor_id             uuid PRIMARY KEY REFERENCES iam.actor(id) ON DELETE CASCADE,
    -- PHC-encoded Argon2id hash; the plaintext password never reaches the database.
    password_hash        text NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    password_updated_at  timestamptz NOT NULL DEFAULT clock_timestamp(),
    must_change_password boolean NOT NULL DEFAULT false,
    failed_attempts      integer NOT NULL DEFAULT 0 CHECK (failed_attempts >= 0),
    locked_until         timestamptz,
    last_login_at        timestamptz,
    created_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at           timestamptz NOT NULL DEFAULT clock_timestamp()
);

SELECT platform.attach_touch_updated_at('iam.credential');

-- Browser sessions. The cookie holds an opaque random id; only its SHA-256 hash is stored,
-- so a database leak cannot be replayed as a login.
--
-- No RLS: a session belongs to an actor, not to a tenant, and is addressed only by the
-- unguessable id hash. active_tenant_id is a preference; every request still re-checks
-- membership and permissions (WP-I1-02).
--
-- The CSRF token is not stored either: it is derived per session with
-- HMAC-SHA256(cookie signing key, session id) and returned by GET /api/v1/session.
CREATE TABLE iam.session (
    id_hash          bytea PRIMARY KEY CHECK (octet_length(id_hash) = 32),
    actor_id         uuid NOT NULL REFERENCES iam.actor(id) ON DELETE CASCADE,
    active_tenant_id uuid REFERENCES platform.tenant(id) ON DELETE SET NULL,
    client_type      text NOT NULL DEFAULT 'BROWSER' CHECK (client_type IN ('BROWSER')),
    user_agent_hash  bytea CHECK (user_agent_hash IS NULL OR octet_length(user_agent_hash) = 32),
    source_ip        inet,
    created_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_seen_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at       timestamptz NOT NULL,
    step_up_until    timestamptz,
    revoked_at       timestamptz,
    CONSTRAINT ck_session_expiry CHECK (expires_at > created_at)
);

CREATE INDEX ix_session_actor ON iam.session (actor_id) WHERE revoked_at IS NULL;
CREATE INDEX ix_session_expiry ON iam.session (expires_at);

SELECT platform.grant_app_schema_usage('iam');
