# WP-I1-01 · Identity: login, PostgreSQL sessions, CSRF, step-up

> **Delivered in-house on 2026-09-03, with one design change.** The business owner removed the
> requirement to federate with a customer directory, so the OIDC/Keycloak half of this package
> was replaced by KAPSORA's own password authentication ([ADR-022](../adr/ADR-022.md)):
> Argon2id credentials in `iam.credential`, per-account lockout, and `POST /api/v1/session/login`
> instead of a redirect to an identity provider. Everything else below — the opaque session in
> `iam.session`, the cookie rules, the derived CSRF token, step-up, the audit events — was built
> as written. Sections 1, 2.1-2.2, 4 (browser redirect routes) and 7 (Keycloak realm) are
> historical; the rest still describes the delivered code.

| Field | Value |
|---|---|
| Milestone | M1 (plan increment I1) |
| Size | L |
| Depends on | Ports already in `main`: `internal/identity`, `internal/platform/crypto`, `internal/audit` |
| Runs in parallel with | WP-I1-02, 03, 04, 05, 06 |
| Migration numbers assigned | `000012_iam_session.up.sql` |
| OpenAPI operations owned | `logout`, `stepUpSession` (to be replaced, see 6.3), new `getSession` |
| Read first | Handbook; v1.2 sections 15.3, 18.1-18.4, 19.2; ADR-005, ADR-020, ADR-021 |

## 1. Goal

Browser users authenticate through Keycloak (OIDC authorization code + PKCE) handled by the
Go BFF. The browser never sees OAuth tokens: it holds an opaque session cookie; the session
lives in PostgreSQL. The package provides everything other work packages need to know *who*
is calling: session loading middleware, CSRF protection, logout and step-up.

## 2. Scope

In scope:

1. OIDC client: discovery, authorization URL with PKCE (S256), `state` and `nonce`, token
   exchange, ID-token verification, refresh-token revocation on logout, RP-initiated logout.
2. Actor mapping: on successful login upsert `iam.actor` by `(identity_issuer,
   identity_subject)`; update display name/email; status must be ACTIVE or INVITED
   (INVITED becomes ACTIVE on first login); SUSPENDED/CLOSED actors get a `403
   ACTOR_SUSPENDED` problem page and no session.
3. Session lifecycle in PostgreSQL (`identity.SessionStore` implementation).
4. Cookie handling and CSRF double-submit for cookie-authenticated state-changing calls.
5. Step-up re-authentication.
6. Local Keycloak realm export with demo users.
7. Audit events through `audit.Recorder` (use `audit.NopRecorder` in tests; real recorder
   arrives with WP-I1-04): `session.login` (SUCCESS/FAILURE), `session.logout`,
   `session.step_up`, category AUTHENTICATION.

Out of scope: permissions, tenant selection, `/me`, `/tenants` (WP-I1-02); service-account
bearer tokens (WP-I1-02 stretch); UI (WP-I1-05); scripts that install/run Keycloak
(WP-I1-06, but you must document the manual commands you used).

## 3. Packages and layout

```text
internal/identity/
  identity.go                      (exists; do not change exported API without flagging)
  domain/session.go                pure rules: expiry, idle timeout, step-up window, token generation
  application/login.go             BeginLogin, CompleteLogin, Logout, BeginStepUp, CompleteStepUp
  application/ports.go             OIDCClient, Clock, ActorRepository interfaces
  infrastructure/oidc/client.go    go-oidc + oauth2 implementation
  infrastructure/postgres/session_store.go   identity.SessionStore on iam.session (sqlc)
  infrastructure/postgres/actor_repository.go
  transport/http/handler.go        routes below
  transport/http/middleware.go     SessionMiddleware, RequireCSRF
  transport/http/cookies.go
db/migrations/000012_iam_session.up.sql
db/queries/identity.sql
deploy/keycloak/realm-kapsora.json
docs/runbooks/keycloak-local.md   (manual steps you used; WP-I1-06 automates them)
```

Allowed dependencies: `github.com/coreos/go-oidc/v3`, `golang.org/x/oauth2`, and for tests
`github.com/oauth2-proxy/mockoidc`. Anything else needs justification.

## 4. Routes

Browser flow (not in OpenAPI; document them in `docs/api/bff-auth.md`):

| Method | Path | Behaviour |
|---|---|---|
| GET | `/auth/login?returnTo=/path` | Creates PKCE verifier, state, nonce; stores them in a signed, HttpOnly, 10-minute cookie `__Host-kapsora_oidc`; redirects to Keycloak. `returnTo` must be a same-origin relative path; otherwise use `/`. |
| GET | `/auth/callback?code&state` | Validates state cookie, exchanges code, verifies ID token (issuer, audience, nonce, expiry), upserts actor, creates session, sets session cookie, deletes the state cookie, redirects to `returnTo`. Any failure renders a minimal problem page (no token or error details beyond a code) and audits `session.login` FAILURE. |
| GET | `/auth/logout` | Deletes session, revokes refresh token at Keycloak, clears cookie, redirects to Keycloak `end_session_endpoint` with `id_token_hint` and `post_logout_redirect_uri`. |
| GET | `/auth/step-up?returnTo=` | Like login but with `prompt=login` and `max_age=0`; on callback, when `auth_time` is within 60 seconds, sets `step_up_until = now + 10m` on the existing session (no new session). |

API (OpenAPI):

| Operation | Path | Behaviour |
|---|---|---|
| `getSession` (new) | `GET /api/v1/session` | Returns `{actorId, displayName, activeTenantId, csrfToken, expiresAt, stepUpExpiresAt}`. This is how the frontend obtains the CSRF token. 401 when no valid session. |
| `logout` (exists) | `POST /api/v1/session/logout` | Deletes session + revokes refresh token, clears cookie, 204. Requires CSRF. |
| `stepUpSession` (exists) | remove | Replace with the browser redirect flow above; update the spec accordingly and mention it in the report. |

## 5. Session rules (domain)

- Session id: 32 random bytes, base64url in the cookie; store only `sha256(id)`.
- CSRF token: 32 random bytes, base64url; store `sha256(token)`; compare in constant time.
- Idle timeout 30 minutes (`last_seen_at`), absolute 8 hours (`expires_at`), step-up window
  10 minutes (v1.2 18.2). Privileged-role shorter timeouts come with WP-I1-02; expose the
  numbers as `Config` values so they can be tuned per deployment.
- Touch `last_seen_at` at most once per 60 seconds to avoid a write per request.
- Refresh token and ID token are stored encrypted with `crypto.FieldCipher`
  (`crypto.PurposeSessionToken`, tenant `uuid.Nil`).
- Cookie name `__Host-kapsora_session`, `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`.
  When `KAPSORA_COOKIE_SECURE=false` (local HTTP only), use the name `kapsora_session`
  without the `__Host-` prefix and log a warning at startup; refuse this setting when
  `Config.IsProductionLike()`.
- `SessionMiddleware`: loads the session from the cookie, rejects expired or revoked ones
  (clear cookie, continue unauthenticated), touches, puts it in context with
  `identity.WithSession`. It never fails the request by itself; authorization is WP-I1-02.
- `RequireCSRF`: for cookie-authenticated requests with method other than GET/HEAD/OPTIONS,
  header `X-CSRF-Token` must match; otherwise `403 CSRF_TOKEN_INVALID`. Bearer-token
  requests (no cookie) skip the check.

## 6. Data

### 6.1 Migration `000012_iam_session.up.sql`

```sql
CREATE TABLE iam.session (
    id_hash              bytea PRIMARY KEY CHECK (octet_length(id_hash) = 32),
    actor_id             uuid NOT NULL REFERENCES iam.actor(id) ON DELETE CASCADE,
    active_tenant_id     uuid REFERENCES platform.tenant(id) ON DELETE SET NULL,
    csrf_token_hash      bytea NOT NULL CHECK (octet_length(csrf_token_hash) = 32),
    client_type          text NOT NULL DEFAULT 'BROWSER' CHECK (client_type IN ('BROWSER')),
    idp_session_id       text,
    refresh_token_cipher bytea,
    id_token_cipher      bytea,
    user_agent_hash      bytea,
    source_ip            inet,
    created_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_seen_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at           timestamptz NOT NULL,
    step_up_until        timestamptz,
    revoked_at           timestamptz
);
CREATE INDEX ix_session_actor ON iam.session (actor_id);
CREATE INDEX ix_session_expiry ON iam.session (expires_at);
SELECT platform.grant_app_schema_usage('iam');
```

No RLS: sessions are keyed by the unguessable hash and belong to an actor, not a tenant.
Add a `DeleteExpired(before)` query; the scheduler job that calls it is WP-I1-04's.

### 6.2 Configuration keys (add to `config.Config` with validation)

`KAPSORA_OIDC_ISSUER_URL`, `KAPSORA_OIDC_CLIENT_ID`, `KAPSORA_OIDC_CLIENT_SECRET`,
`KAPSORA_PUBLIC_BASE_URL` (e.g. `http://localhost:8080`), `KAPSORA_COOKIE_SECURE`
(default true), `KAPSORA_COOKIE_SIGNING_KEY` (64 hex chars, for the state cookie),
`KAPSORA_LOCAL_MASTER_KEY` (already documented), `KAPSORA_SESSION_IDLE_MINUTES` (30),
`KAPSORA_SESSION_ABSOLUTE_HOURS` (8), `KAPSORA_STEP_UP_MINUTES` (10).

### 6.3 OpenAPI

Add `getSession`; remove `stepUpSession`; keep `logout`. Document the `/auth/*` browser
routes in `docs/api/bff-auth.md`. Run `make openapi-generate` and `make openapi-lint`.

## 7. Keycloak local realm

Provide `deploy/keycloak/realm-kapsora.json` importable with
`kc.bat start-dev --http-port=8081 --import-realm` (JDK 21). Contents:

- Realm `kapsora`; client `kapsora-bff` (confidential, standard flow, PKCE S256 required,
  redirect URIs `http://localhost:8080/auth/callback`, post-logout redirect
  `http://localhost:8080/*`, web origin `http://localhost:5173`), client secret
  `local-only-change-me`.
- Demo users with **fixed ids** (WP-I1-02 seeds memberships against these ids), password
  `Demo1234!`, email verified:

| Username | Id | Intended tenant/role (WP-I1-02) |
|---|---|---|
| `admin.a` | `0192a000-0000-7000-8000-00000000a001` | DEMO_A tenant admin |
| `reviewer.a` | `0192a000-0000-7000-8000-00000000a002` | DEMO_A financial reviewer |
| `provider.a` | `0192a000-0000-7000-8000-00000000a003` | DEMO_A provider staff |
| `admin.b` | `0192a000-0000-7000-8000-00000000b001` | DEMO_B tenant admin |
| `both.ab` | `0192a000-0000-7000-8000-00000000ab01` | member of DEMO_A and DEMO_B |

Keycloak issues `sub` = user id, so `iam.actor.identity_subject` equals these values.

## 8. Tests required

- Domain: expiry/idle/step-up rules with an injected clock; token generation length and
  randomness; constant-time CSRF comparison.
- Application with `mockoidc`: full login round trip creates actor and session; wrong or
  replayed `state` fails; wrong `nonce` fails; suspended actor gets no session; logout
  revokes and deletes; step-up only when `auth_time` is fresh.
- Infrastructure with `dbtest`: session store CRUD, expiry filtering, `DeleteByActor`,
  `DeleteExpired`; tokens are not readable in plaintext in the table.
- Transport with `httptest`: cookie attributes, `RequireCSRF` accept/reject, `getSession`
  401 without cookie, `logout` clears the cookie.

## 9. Acceptance criteria

- [ ] Manual: with Keycloak started from the realm file, `GET /auth/login` on the running
      API ends with a session cookie and `GET /api/v1/session` returns the actor; logout
      ends both the BFF and Keycloak sessions. Steps documented in the runbook.
- [ ] No OAuth token appears in cookies, logs, responses or the database in plaintext.
- [ ] All tests in section 8 pass; `make ci` and `make test-db` green.
- [ ] OpenAPI updated and regenerated; Spectral zero errors.
- [ ] Audit events emitted for login success/failure, logout and step-up.

## 10. Report additions

In `REPORT.md` section 7 include the exact Keycloak start command, the JDK version used,
and the curl sequence you ran for the manual acceptance check.
