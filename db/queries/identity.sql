-- Identity queries: local password credentials and BFF sessions (WP-I1-01).
-- Sessions are addressed by the SHA-256 hash of the opaque cookie value.

-- name: CreateLocalActor :one
INSERT INTO iam.actor (identity_issuer, identity_subject, actor_type, display_name, email, status)
VALUES ($1, $2, 'HUMAN', $3, $4, 'ACTIVE')
RETURNING id;

-- name: CreateCredential :exec
INSERT INTO iam.credential (actor_id, password_hash, must_change_password)
VALUES ($1, $2, $3);

-- name: FindAccountByUsername :one
SELECT a.id, a.identity_subject, a.display_name, a.email, a.status,
       c.password_hash, c.must_change_password, c.failed_attempts, c.locked_until
  FROM iam.actor a
  JOIN iam.credential c ON c.actor_id = a.id
 WHERE a.identity_issuer = $1
   AND a.identity_subject = $2
   AND a.actor_type = 'HUMAN';

-- name: FindAccountByActorID :one
SELECT a.id, a.identity_subject, a.display_name, a.email, a.status,
       c.password_hash, c.must_change_password, c.failed_attempts, c.locked_until
  FROM iam.actor a
  JOIN iam.credential c ON c.actor_id = a.id
 WHERE a.id = $1;

-- name: RegisterLoginFailure :exec
-- Increments and locks in one statement so parallel attempts cannot overshoot the
-- threshold. $2 is the threshold, $3 the instant the lock would end.
UPDATE iam.credential
   SET failed_attempts = failed_attempts + 1,
       locked_until = CASE WHEN failed_attempts + 1 >= $2 THEN $3 ELSE locked_until END
 WHERE actor_id = $1;

-- name: RegisterLoginSuccess :exec
UPDATE iam.credential
   SET failed_attempts = 0, locked_until = NULL, last_login_at = $2
 WHERE actor_id = $1;

-- name: SetCredentialPassword :exec
UPDATE iam.credential
   SET password_hash = $2, password_updated_at = $3, must_change_password = false,
       failed_attempts = 0, locked_until = NULL
 WHERE actor_id = $1;

-- name: CreateSession :exec
INSERT INTO iam.session (
    id_hash, actor_id, active_tenant_id, user_agent_hash, source_ip,
    created_at, last_seen_at, expires_at, step_up_until
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetSession :one
-- Revoked and absolutely expired sessions are invisible; the idle timeout is applied in Go.
SELECT id_hash, actor_id, active_tenant_id, created_at, last_seen_at, expires_at, step_up_until
  FROM iam.session
 WHERE id_hash = $1
   AND revoked_at IS NULL
   AND expires_at > clock_timestamp();

-- name: TouchSession :exec
UPDATE iam.session SET last_seen_at = $2 WHERE id_hash = $1 AND revoked_at IS NULL;

-- name: SetSessionActiveTenant :execrows
UPDATE iam.session SET active_tenant_id = $2 WHERE id_hash = $1 AND revoked_at IS NULL;

-- name: SetSessionStepUp :execrows
UPDATE iam.session SET step_up_until = $2 WHERE id_hash = $1 AND revoked_at IS NULL;

-- name: DeleteSession :execrows
DELETE FROM iam.session WHERE id_hash = $1;

-- name: DeleteSessionsByActor :execrows
DELETE FROM iam.session WHERE actor_id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM iam.session WHERE expires_at < $1;
