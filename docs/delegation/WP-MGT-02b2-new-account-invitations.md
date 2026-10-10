# MGT-02b B2: new-account invitation acceptance

Status: ACTIVE, 2026-10-09. Planning and decomposition: gpt-6-astra; backend,
frontend and independent review: gpt-6-sol. This freezes the B2 portion of
[the invitation work package](WP-MGT-02b-membership-invitations.md), after B1's
isolated CI and bounded live list/denial checks. It is not acceptance evidence.

## Contract and anonymous boundary

Preserve authenticated `POST /api/v1/invitations/inspect` and `accept-existing`.
Add three anonymous JSON POST operations on the fixed invitation page:

| Operation | Request | Successful response |
| --- | --- | --- |
| `inspect-new` | code | Existing inspect projection, only for a valid pending invitation |
| `accept-new` | code, displayName, password, confirmed=true; explicit Idempotency-Key | Safe membership outcome, loginHandle, recoveryExpiresAt |
| `acceptance-receipt` | code, current password | Same private acceptance outcome |

All paths above are relative to `/api/v1/invitations/`. The outcome contains exactly
tenantId, tenantDisplayName, membershipId, membershipStatus, accessPending, loginHandle
and recoveryExpiresAt. It never creates a session or returns a session cookie.

Anonymous routes must bypass session loading/touch/invalid-cookie cleanup. Require
application/json, `X-Invitation-Request: 1`, and one valid Origin exactly equal to the
configured invitation origin. Never trust Host or forwarding headers to select that
origin. Reject explicit cross-site Fetch Metadata; absence remains supported. Do not
enable permissive CORS. Apply one trusted-client-address bucket, 12/minute with burst 5,
before body/password work. All responses, including middleware failures, are no-store.

Strict JSON rejects unknown fields and trailing data. Bound the body to 16 KiB and the
unchanged password to 1024 UTF-8 bytes; reuse the ordinary minimum length/password policy.
Trim displayName, require valid UTF-8, 1–200 characters and no control characters.
Do not use generic idempotency body hashing or private-response persistence.

## Atomic creation and mode preservation

Reserve forward migration 000057 after confirming 000056 is the current maximum.
Add accepted_mode (EXISTING/NEW), backfill accepted B1 rows to EXISTING, and require a
mode exactly when status is ACCEPTED. Add a nullable 32-byte tenant-keyed acceptance
metadata fingerprint covering canonical display name, consent, operation and version;
never include the password or another code hash. Preserve manager projections and RLS.

Extract account creation into an internal transaction-aware helper, retaining the old
transaction-opening wrapper for its existing callers. Under the invitation's exact
tenant transaction and the existing tenant→invitation lock order, verify active tenant,
proof, pending state and expiry, then create one ACTIVE actor, ordinary Argon2id credential,
ACTIVE zero-grant membership, NEW acceptance and safe transactional audit. A failure
rolls everything back. Generate a normalized login handle from 16 random bytes, independent
of email/name/tenant/actor; handle a unique collision without leaving the transaction aborted.
Store no invitation address in the global actor contact. No login or tenant selection.

B1 accepted inspect/replay requires EXISTING. NEW cannot recover through B1; EXISTING
cannot recover through anonymous receipt. Terminal code-only preview stays generic.

## Private receipt recovery

The deadline is terminal_at + 24 hours, checked on each request independently of cleanup.
Require the proof, NEW mode, active tenant/actor, active valid referenced membership and
verification against the current credential. Apply the shared account lockout (10 failures,
15 minutes) under credential locking. Wrong-password counters must commit even though the
outward result is generic unavailable; a refusal returned inside the transaction callback
must not accidentally roll those counters back. Unverified UUID selectors cannot select
an account for counter changes.

Unknown, wrong-proof, expired, wrong-mode, locked and inactive results perform bounded
dummy password work and return the same unavailable response. Test the dummy-work call,
not brittle timing. Successful recovery may clear failure counters using a dedicated query;
it cannot record a login, change a password, extend the deadline or refresh a cookie.

An accepted-new retry first verifies the current password, then compares original key and
metadata fingerprint. Exact retries return the private outcome; changed metadata/key returns
IDEMPOTENCY_KEY_REUSED only after credential verification. Neither path creates another actor.
Cleanup removes proof, key and acceptance fingerprint after 24 hours, preserving membership.
Logs, ordinary notifications, audits and generic receipts must contain no password, code,
PHC hash or low-level constraint values exposing private input.

## UI, ownership and exit evidence

The anonymous fixed page offers existing-account sign-in, new-account consent and direct
receipt recovery. Preview names the institution before consent. After dispatch, clear the
password and require re-entry for uncertainty/recovery; pin only the original key, proof
and nonsecret metadata in component memory. Clear all secrets/results on mode, session or
account changes and unmount, including deferred responses and A→B→A changes.
Show the generated handle with a retain instruction and recovery deadline, then an ordinary
login link with no secret query parameters. Never automatically log in. Preserve B1 and
the three apps' zero-grant waiting state. Mocks must enforce the new credential on ordinary
login too, without retaining a plaintext chosen password.

Backend owns schema/sqlc, transaction/security services, OpenAPI/generated contracts and
Go/isolated DB tests. Frontend owns wrappers, page/routes, mocks, translations and UI tests.
Independent review examines mode preservation, atomic rollback, Origin/session boundaries,
counter commits, current-password recovery and secret handling. The integrator owns landing,
cross-workspace checks and evidence. Operator server ownership remains HANDOVER §3.

Required isolated evidence: accepted-B1 upgrade; one actor/credential/member and no grants,
sessions or email; injected creation/audit rollback; concurrent new/new, new/existing and
cancel races; password-required retry and changed metadata/key; password changes, lockout
counter persistence/concurrency and dummy-work paths; Origin/header/media/body/no-store and
limiter-before-work; 24-hour cleanup and secret sentinels. The existing Directory|Invitation
CI filter must run every new integration test without skips. Do not run the shared local
dbtest harness, which resets the application-role password.

UI evidence covers preview/create/private receipt, password re-entry, retained handle,
zero-grant login, context/mode changes, unavailable outcomes, memory clearing and B1
regression, plus synthetic 1440px/390px review. Contract lint/compatibility, generated drift,
workspace type/lint/format/build and final CI must pass. Local migration application follows
isolated verification as a concrete landing step. No external delivery or role grants.

## Implementation review checkpoint (2026-10-09)

Backend/frontend implementation and independent code review are complete. Scoped Go tests
with the integration database disabled and scoped lint pass; bindings regenerate unchanged.
The 14-view synthetic browser review passed at 1440px/390px, with no real API commands.
The opt-in live boundary checker is `tests/e2e/management-invitations-new-boundary.spec.ts`:
enable `E2E_REAL_API=1`, `E2E_MANAGEMENT_INVITATIONS_NEW_BOUNDARY=1`, and the operator's
numeric-loopback `E2E_EXISTING_UI_URL`. Its five requests use only invalid synthetic proof
and test Origin/no-store/no-cookie isolation and preservation of B1 authentication gates.
It does not create invitations, accounts or memberships.

Eight new Invitation cases cover atomic/private acceptance, audit rollback, mode separation,
Origin/body limits, concurrent acceptance/lockout, address limiting, exact sibling routes,
cancel/new and existing/new races. Six require the isolated CI database; two are pure HTTP
boundary tests. All eight passed without skips in GitHub's isolated PostgreSQL job on
`02fbf13` ([run 37889255894](https://github.com/celikbros/kapsora/actions/runs/37889255894)),
alongside the 56-to-57 upgrade and B1 regressions. Local migration 57 is applied,
dirty=false. All six code CI jobs passed on `2a7a426`
([run 37889748595](https://github.com/celikbros/kapsora/actions/runs/37889748595)), including
725 frontend tests in 87 files and 25 browser smoke tests. Normal smoke has 102 opt-in
live/calendar skips, including this package's live boundary check. Local Go unit
packages also pass. The module now requires Go 1.27.2 after the earlier standard-library
vulnerability finding. Bounded live boundary checks await the operator restart; the
current API still answers RESOURCE_NOT_FOUND for an invalid synthetic inspect-new probe.
Audit failure injection verifies rollback of actor, credential, membership and consumption;
separate insertion-stage failpoints have not been injected. Dummy-work call sites were
statically reviewed, without direct invocation instrumentation or timing claims. These
limits must remain explicit when reporting acceptance evidence.
