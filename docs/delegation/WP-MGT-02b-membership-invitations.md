# WP-MGT-02b · Consent-based tenant membership invitations

| Field       | Value                                                                                                 |
| ----------- | ----------------------------------------------------------------------------------------------------- |
| Date        | 2026-10-08                                                                                            |
| Status      | B1 implemented; isolated CI and local migration application pending; B2 not started |
| Depends on  | MGT-01 directory; MGT-02a correlated manager capability; ADR-022                                      |
| Delivery    | MGT-02b1 existing-account invitation journey, then MGT-02b2 new-account acceptance                    |
| Migration   | B1: 000056, assigned by integrator on 2026-10-08 after checking current maximum 000055; B2 unassigned |
| Owned scope | Identity invitation contract, persistence, restricted delivery, recipient acceptance and UI           |

## 1. Outcome and bounded delivery

A manager invites a recipient by email. The recipient explicitly joins the named tenant
using their own account. The resulting ACTIVE membership has **zero role grants** and
shows that access awaits assignment. MGT-03 remains the separate role-assignment workflow.
Possession of an invitation never grants tenant administration, clinical or financial access.

Deliver B1 end to end first: manager create/list/detail/cancel, restricted email delivery,
existing-account sign-in and explicit acceptance, directory result and waiting state.
This is a usable addition for people who already have KAPSORA accounts. The recipient page
must say that this increment requires an existing account; do not show a dead create-account
button or claim onboarding is complete. B2 adds recipient-created accounts and private
receipt recovery. Preserve the contracts and transactional boundaries below so B2 is additive.

Do not add reactivation, role changes, administrator-selected passwords, global suspension,
password reset, arbitrary actor-ID linking, bulk invitations or automatic email-to-account
matching. No live existing account is changed as implementation acceptance evidence.

## 2. Manager contract

| Method and path                                        | Request and result                                                      |
| ------------------------------------------------------ | ----------------------------------------------------------------------- |
| GET `/api/v1/admin/invitations`                        | Bounded stable paging/status filter; allowlisted summary                |
| POST `/api/v1/admin/invitations`                       | `{email}` and Idempotency-Key; returns summary and ETag, never the code |
| GET `/api/v1/admin/invitations/{invitationId}`         | Summary plus ETag                                                       |
| POST `/api/v1/admin/invitations/{invitationId}/cancel` | Empty body, strict If-Match and Idempotency-Key; updated summary        |

Reads require the existing correlated TENANT `identity.user.read`; create/cancel require
correlated TENANT `identity.user.manage`, active caller membership, CSRF and fresh password
step-up. Recheck command authority inside its transaction. Use existing capability fields;
no new seed grants or system-role changes. Unknown/foreign IDs are indistinguishable 404s.
Authorization and step-up precede replay. Match suspension's uncertain-retry and explicit
conflict-reload UI behavior, including deferred context-change guards.

Summary allowlist: invitationId, maskedRecipient, status, createdAt, expiresAt,
rowVersion, deliveryStatus. Do not expose global account existence, actor identifiers,
raw recipient address, code/digest, delivery error text or provider response. In the detail,
accepted status is sufficient; the ordinary directory supplies authorized membership data.

States: PENDING, ACCEPTED, CANCELLED, EXPIRED. Initial validity is 48 hours, server chosen.
Reject acceptance after expiresAt even before a cleanup job persists EXPIRED. Re-invitation
is a fresh command after cancellation/expiry; no resend or secret rotation in this slice.
Serialize creation per tenant/contact and permit only one currently pending invitation.
Expire an elapsed pending row transactionally before a new one; do not rely on a partial
index containing current time. Delivery refusal or retry must not disclose account existence.

## 3. Contact and command privacy

Validate a single bounded mailbox; trim whitespace, normalize the domain consistently,
and preserve local-part semantics. Do not apply provider-specific dot/plus stripping.
Use dedicated `crypto.FieldCipher` and tenant-bound `BlindIndexer` purposes for invitation
contact. Database values are encrypted contact and HMAC blind index, never plain email.
Use a minimally identifying masked display; do not copy the address into `iam.actor.email`
as an incidental effect of acceptance.

The generic idempotency middleware hashes raw bodies with unkeyed SHA-256 and is unsuitable
unchanged for email/password commands. For create, add a narrowly opt-in keyed request
fingerprint or invitation-specific receipt repository. Its tenant-bound HMAC input includes
operation, canonical normalized email, fixed version and command-relevant headers. Bind the
receipt to tenant, acting account, route and Idempotency-Key. Store only the keyed fingerprint
and allowlisted result. Do not store an additional unkeyed hash of sensitive input. Changed
input under the same key conflicts; replay after manager access loss is denied.

Do not apply generic response caching to recipient acceptance or credential recovery.
Neither passwords nor codes belong in request logs, traces, audit details, errors, browser
URLs, local/session storage, query keys or persisted command payloads. Restrict body capture
at the route/middleware boundary and test that restriction.

## 4. Restricted invitation delivery

Reuse the existing outbox, crypto and SMTP ports, with an identity-owned restricted delivery
handler. Existing general notification records persist rendered content and resolve known
PERSON/ACTOR/ORG recipients; do not relax their secret-free template/link rules or put
invitation secrets in their message body/variables tables.

Generate at least 32 random secret bytes. Persist a verification digest and separately an
encrypted short-lived delivery envelope under a dedicated invitation-delivery purpose.
The envelope contains the recipient and code, is unavailable to manager queries, and is
loaded only by the restricted worker path. The outbox contains invitation ID and delivery
generation only. Render/decrypt immediately before SMTP send, in process memory. Event,
worker error and delivery-status records carry only bounded safe codes and identifiers.

Email links to a fixed configured `/invitation` page; the user enters/pastes the code there.
No token query or fragment. A versioned code can contain tenant UUID, invitation UUID and
random secret: selectors are not authority. Resolve only the selected invitation in the
selected tenant RLS transaction bound to the authenticated session actor, constant-time verify the digest, and return no tenant or
recipient information before verification. Never accept an independent browser tenant header
as authorization. Audit the narrowly scoped pre-membership lookup/acceptance path; no global
bypass query or broad SECURITY DEFINER permission is justified by this flow.

Worker checks state/expiry/generation before send. At-least-once SMTP may deliver a duplicate
if send succeeded before acknowledgement failed; retries use the same still-valid code.
Cancellation cannot recall an in-flight email, but always prevents acceptance. On successful
handoff or terminal state purge the delivery envelope; expire outstanding envelopes through
a bounded cleanup job. Keep audit/status metadata, not secret-bearing payloads. Purge contact
ciphertext/index when no longer needed for the active invitation lifecycle; retained masked
summaries must follow a documented short retention period, not indefinite incidental storage. B1 retains the minimal masked summary for 30 days after the terminal transition, then removes the mask; safe audit/status identifiers remain. Purge contact cipher/index on terminal transition and delivery envelope on handoff or terminal transition. A bounded periodic worker also cleans expired invitations when no user opens them.

Use fake SMTP in automated tests and synthetic `.test` recipients in operator Mailpit when
available. Real external invitation sending is a later explicitly authorized operator action. Keep restricted invitation delivery disabled by default; local development enables only the explicitly configured loopback Mailpit target. Refuse a non-loopback target in this local-only mode. No generic notification delivery configuration should silently enable real invitation email.

## 5. Recipient contract and existing-account acceptance (B1)

| Method and path                            | Contract                                                                                                                           |
| ------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------- |
| POST `/api/v1/invitations/inspect`         | `{code}`; authenticated session/CSRF, no active tenant required; after proof returns tenant display, validity and invitation state |
| POST `/api/v1/invitations/accept-existing` | `{code, confirmed:true}` plus Idempotency-Key; authenticated session/CSRF required, no active tenant required                      |

B1 recipient routes require normal authenticated login and explicit consent; fresh step-up is not required. Rate-limit pre-membership routes; use no-store responses, strict bounded payloads and same-origin
request protection. Malformed, unknown, cancelled and expired proofs have a generic unavailable
response; do not reveal selectors or account existence. No code echo in any response.

Sign in through ordinary login, then explicitly confirm joining with the currently displayed
account. Code remains in memory; a full reload/sign-in navigation can ask the user to paste
it again instead of persisting a bearer secret. Reset pending UI when account/session changes.
The accepted actor comes exclusively from authenticated session, never body or email lookup.
Check that the actor remains ACTIVE inside the transaction. Preserve credentials, sessions,
existing memberships and historical grants. No automatic tenant switch.

Lock the invitation; require valid proof/PENDING/unexpired, and atomically insert a new ACTIVE
membership with no grants, consume the invitation, and write a safe transactional audit event.
Protect concurrent membership creation with the existing tenant/actor uniqueness constraint.
If this actor already has an ACTIVE valid membership, consume the invitation as an idempotent
join result without altering it. SUSPENDED/REVOKED/invalid membership is not reactivated: return
a bounded refusal to the authenticated recipient. Do not reinterpret a failed join as a grant.
Concurrent cancellation/acceptance has one winner. Audit/outbox failure rolls the command back.

After acceptance, an exact retry by the same authenticated actor can obtain the safe outcome;
a different actor cannot claim/replay the accepted invitation. The receipt does not confer
access and does not replay a session cookie. Show “Üyeliğiniz oluşturuldu. Erişim yetkilerinizin
atanması bekleniyor.” Offer return to existing account/tenant selection; do not send a zero-grant
member to an unauthorized business dashboard. Waiting-state behavior must also work on login.

## 6. Recipient-created account and lost-response recovery (B2)

Add POST `/api/v1/invitations/accept-new` with code, displayName, chosen password, explicit
consent and Idempotency-Key. Do not ask for a username/email-availability check. Generate a
nonidentifying high-entropy login handle within existing username validation limits, normalized
as ADR-022 requires. New account and credential use ordinary Argon2id policy with no temporary
password. The invitation address is not copied to a plaintext global contact column.

Atomic unit: lock/verify invitation, insert actor, credential, zero-grant membership, accepted
outcome reference, and audit. Refactor a transaction-aware internal account-creation port;
`CreateHumanAccount` currently opens its own transaction and must not be called as an
independently committed step. No duplicate actor/credential on retry or audit failure. Define
same-origin pre-auth CSRF protection and rate limits explicitly before exposing this endpoint;
ordinary authenticated CSRF middleware cannot be assumed to protect anonymous signup.

Acceptance returns generated login handle and safe membership outcome, but **does not log in**,
create a session, set a session cookie, or persist/replay plaintext credentials. Direct the
recipient to ordinary login. Generic idempotency only replays selected response headers and
does not replay Set-Cookie; do not solve this by persisting opaque session credentials.

Provide a private POST `/api/v1/invitations/acceptance-receipt` with `{code,password}`. Within
an explicit recovery window (24 hours from acceptance), require both the high-entropy proof
and verification against the accepted new actor's current credential, with per-address and
per-account lockout protection and dummy Argon2 work for unknown results. Only then return
the generated handle and safe outcome. This handles a lost first response without creating
another account or disclosing handles to code-only holders. Repeating accept-new after commit
must use this verification path before returning its existing outcome, never return cached
private output based only on Idempotency-Key. Changed payload/key does not recreate or modify
an accepted account. No password change in any retry/recovery path.

After acceptance, code-only inspect discloses no actor/handle. Recovery is limited to accounts
created by this invitation; existing-account recipients recover through their existing login.
After the bounded receipt window, discard the proof digest used for recovery; retain nonsecret
audit links. Recovery expiration does not undo membership. UI must tell the recipient to retain
the displayed login handle. A separate general account-recovery feature is outside this slice.

## 7. Persistence and implementation sequence

Migration 000056 is reserved by the integrator for B1 after checking the current maximum 000055. Add forward-only schema with tenant IDs, composite keys,
RLS, version triggers, constrained states and invitation/receipt uniqueness. Delivery-envelope
access is private to its repository. B1 needs no credential-table behavior change; B2 schema
extension must preserve accepted B1 invitations and not synthesize new accounts for them.

Work order: contract/schema/privacy tests; manager commands and restricted delivery; B1
recipient transaction plus waiting UI; integrated mocks/desktop-mobile verification; then B2
atomic account port and receipt recovery with its own focused review. Backend owns OpenAPI,
generated contracts, migrations/sqlc, services and worker wiring. Frontend can implement from
the frozen contract after it lands, without changing shared auth semantics speculatively.
Update PRODUCT/DESIGN and handover/roadmap with actual evidence, distinguishing B1 from B2.

Keep local operator servers under HANDOVER section 3. Do not run the shared-db test harness
that resets application credentials; isolated CI/disposable PostgreSQL supplies repository
proof. Applying a new migration to the operator database is a separate concrete landing step,
not implied by writing this specification. No role grants or real user invitations are needed
for development acceptance.

## 8. Required acceptance evidence

- Negative RLS, cross-tenant selectors, scoped-only/expired manager denial, revoked caller on
  replay; manager response allowlists and no global account-existence disclosure.
- Same create key/input one invitation/outbox; changed email conflicts; tenant-keyed fingerprint
  differs across tenants; no unkeyed email/password hashes or plaintext in receipts/logs/audit.
- Parallel create same recipient; expired pending replacement; cancellation/acceptance race;
  worker retry same code; cancellation/expiry invalidates in-flight delivery; envelope cleanup.
- Existing acceptance preserves global credential/session and tenant B, zero role grants,
  already-active result, suspended membership refusal, actor/session change while UI pending,
  same actor exact retry and other actor denial; audit failure rolls back. The recipient proof path must remain narrow when a caller supplies an arbitrary tenant selector.
- B2 atomic account/credential/member/outcome/audit rollback; concurrent accepts one account;
  generated handles; lost response recovered by code plus correct password only; incorrect,
  unknown, expired and locked receipt attempts remain generic; no Set-Cookie or password replay.
- Contract lint/compatibility, generation drift, targeted Go tests, isolated non-skipped DB tests,
  relevant frontend tests, full workspace lint/type/build and CI. Mock behavior must preserve
  the same tenant/contact privacy and accepted-invitation terminal semantics.
- Desktop/mobile recipient and manager flow: uncertain retries, cancellation, expired code,
  existing login, zero-grant waiting state, and B2 receipt recovery. Synthetic interception
  proves UI behavior only; fake SMTP/isolated database tests prove delivery and atomicity.

No acceptance box is complete merely because this specification exists.

## 9. B1 implementation checkpoint (2026-10-09)

B1 is implemented for existing-account recipients: manager step-up/create/list/detail/cancel,
session-proven inspect and explicit accept-existing, encrypted contact and delivery data,
tenant-keyed HMAC receipts, and zero-grant membership waiting states across backoffice,
provider and member apps. There is no automatic tenant switch. Loopback SMTP is disabled by
default and external SMTP is unavailable.

Local checks to date: 708/83 full frontend tests before the final follow-up; 23 targeted
UI/mock/auth checks and five mock-retention checks passed; 18 synthetic 1440/390 views showed
zero actual API calls, overflow or browser errors. Manual Playwright review used product
standards because Impeccable was unavailable. Full workspace lint, typecheck and formatting,
all three app builds, tracked-package Go vet and nonbreaking contract compatibility pass.
Spectral passes with zero errors and eleven existing warnings. All six CI jobs passed on
`89798a1` ([run 37847598222](https://github.com/celikbros/kapsora/actions/runs/37847598222)):
713 frontend tests, 25 smoke tests and seven Directory/seven Invitation cases without
skips, with six Invitation cases exercising PostgreSQL. Migration 000056 is applied
locally at schema 56, dirty=false. Only local Mailpit loopback delivery is enabled in the
ignored `.env`; defaults remain disabled. After the operator's 2026-10-09 restart, the
opt-in live checker passed 2/2 in 2.7 seconds for bounded admin list/capabilities and
financial-reviewer denial. Existing-row detail is conditional; no live invitation or
membership acceptance was issued. B2 new-account acceptance and private username recovery
are implemented under [the focused B2 package](WP-MGT-02b2-new-account-invitations.md);
isolated CI and local migration landing are pending. MGT-03 roles and MGT-04 settings
remain later work.
