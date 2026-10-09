# WP-MGT-03B · Privileged tenant role changes with two-person approval

| Field | Value |
| --- | --- |
| Milestone | Management, following bounded MGT-03A |
| Status | ACTIVE; implemented on 2026-10-09; integrated isolated acceptance pending |
| Planned | 2026-10-09, gpt-6-astra |
| Review | 2026-10-09, gpt-6-sol; no remaining policy or static design blocker |
| Delivery | Durable request, explicit different checker, atomic grant/revocation and audit |
| Migration number | 000058, allocated by integrator on 2026-10-09; not applied locally |

The owner instructed continuation after the concrete five-role API/UI/migration-file and
isolated-test scope was presented. Automatic approval review accepted the contract-first
OpenAPI write on 2026-10-09. This scope excludes live grant changes and local migration
application. API, persistence, UI and independent static security review are complete.
Integrated isolated evidence is pending: sixteen B HTTP/database test functions and eight
schema cases. Local frontend checks passed 754 tests/92 files and 42 intercepted mobile/
desktop states. No local database test or migration was run.

## 1. Outcome and scope

A tenant role manager proposes granting or revoking a privileged system role. Another
currently authorized human reviews the exact proposed access and explicitly approves or
rejects it. Only approval changes access. The maker, checker and target are three different
global actors; different sessions or memberships of one actor do not satisfy separation.

B admits exactly TENANT_ADMIN, PLAN_PUBLISHER, CONTRACT_PUBLISHER, RULE_APPROVER and
PAYER_APPROVER, each with TENANT scope and no scope ID. Require an existing system role,
a nonempty persisted permission set exactly matching its current RoleTemplates entry,
and at least one catalog PRIVILEGED permission. Unfamiliar/custom/non-system roles,
unexpected scopes and configuration drift are refused. Any PRIVILEGED permission makes
a role privileged; there is no implemented critical-user exception. A's direct commands
stay restricted to A's eleven roles. Do not add B codes to A's direct allowlist.

Assignment is to an ACTIVE, currently valid HUMAN membership of an ACTIVE actor with
no current/future nonempty grant of any kind and no PERSON grant history. Ended historical
non-PERSON access is allowed. Revocation is of that target's sole current/future nonempty
grant, currently effective and matching a B candidate exactly, with no PERSON history.
Count grants with zero permissions. Do not normalize an existing mixed-role membership.

Assignment starts at approval, not submission, at one server timestamp with no upper
bound. Revocation closes the original range at approval, preserving the lower bound
and its inclusivity, creator and legacy reason. A legacy exclusive lower bound must
not be rebuilt as inclusive. No deletion, reopening, replacement, backdating,
scheduling, caller expiry, reactivation, global actor change or session deletion. Finite existing current
grants may be revoked but must remain effective at approval.

Excluded: ORGANIZATION/mixed privileged grants, multiple concurrent roles, MEMBER/PERSON
identity binding, service-account administration, custom roles, role/permission editing,
a generic approval engine, notifications, delegation, JIT/expiry jobs, emergency bypass
and bootstrap of another administrator. No new permissions or template grants. A missing
second human checker remains visible; never change demo/live grants to hide it.

Read WP-MGT-03-role-assignment.md fully, docs/delegation/README.md, HANDOVER current
Management checkpoint and sections 3–6, ROADMAP Management, PRODUCT.md, DESIGN.md,
Master Plan v2.0, baseline v1.2 sections 6.2–6.4, 9.2 and phase-2 acceptance, and ADR-022.
Baseline privileged-role-change maker-checker remains normative after local authentication.
This package freezes routine policy choices; independent security review precedes issue.

## 2. Verified code constraints

- Reuse A's application/role_assignment.go, postgres/role_assignment.go, HTTP handler,
  db/queries/role_assignment.sql and admin route patterns: correlated authority, safe
  projection, tenant lock, membership touch, session recheck and transactional audit.
- access_grant has neither approval state nor row_version. Membership remains the grant
  aggregate ETag; a durable request requires its own version and new schema.
- role_permission is tenant-owned; permission is global and stores sensitivity. There is
  no privileged-role flag. Snapshot code AND sensitivity, not just permission codes.
- Grants.For chooses an app with correlation, then unions its scopes/permissions. B's one
  TENANT grant introduces no cross-organization combination; those remain excluded.
- CanManageTenantRoles correlates each required permission with a current TENANT grant;
  permissions may originate on separate TENANT grants. Flattened context and role names
  cannot authorize this workflow. Only AppAny/backoffice contexts are accepted.
- A's SyncSystemRoles in-use guard covers A codes. Extend its protected-code predicate to
  A+B without expanding A's direct predicate. Preserve offline grant membership touches.
- DirectoryRepository.Suspend currently protects the last identity.user.manage manager
  only. B must protect the last identity.role.manage manager too.
- benefit/application/planversion.go and benefit/ledger/service.go demonstrate explicit
  maker/checker, atomic application/audit and own-file refusal. They are domain-specific,
  not a generic IAM approval service or current-maker authorization implementation.
- platform/idempotency claims and saves responses separately from business transactions.
  It can lose the completed response after a successful mutation. Request state prevents
  duplicate effects; B's transaction receipt below preserves exact retries across that
  gap. Existing middleware alone is not atomic with request/grant application.

## 3. Authority and missing checker behavior

Every read requires current TENANT-correlated identity.user.read AND identity.role.manage,
ACTIVE actor and ACTIVE valid membership. Use canManageTenantRoles for navigation and the
database predicate on every endpoint. Commands also require a HUMAN caller, ordinary active
local session in the selected tenant, CSRF and fresh password step-up under existing policy.

Submission rechecks maker authority and target exclusion under locks. Approval rechecks
maker and checker with the same predicate, using the maker membership captured at creation.
Both must remain ACTIVE HUMAN actors with usable memberships. Maker's old session need not
remain logged in or stepped up: fresh proof was required at submission. Checker must be
freshly stepped up at approval. Session changes never change actor separation.

Rejection requires a current authorized, stepped-up checker distinct from maker and target.
It may close a request whose maker lost authority, target changed or role drifted, because
it applies no access. Cancellation is only for the original maker, still currently
authorized and stepped up. Neither requires the captured target ETag to remain current.
An authorized checker can thus clear stale requests when their maker cannot.

Lifecycle: PENDING -> APPROVED | REJECTED | CANCELLED. No editable draft, reopening,
automatic approval, expiration job or silent cancellation. Approval failure leaves PENDING
and changes nothing. Drift/stale target requires explicit rejection/cancellation and a new
proposal, never automatic refresh of stored evidence.

Allow submission with no other eligible checker. Current reads return checkerAvailability
= AVAILABLE or NO_ELIGIBLE_CHECKER. Compute EXISTS of another global HUMAN actor, excluding
maker and target, with ACTIVE valid membership, both current correlated permissions, local
kapsora identity and ordinary credential. Do not require an online session, count service
actors or claim this proves password knowledge/temporary login availability. Expose no
checker count, contact, login handle or candidate-user directory.

Show “Bu talep için ikinci bir yetkili onaylayıcı bulunmuyor. Talep beklemede kalır.” when
unavailable. Maker cannot approve. A later legitimately authorized checker can act after
refresh; B does not confer that authority. Revoking a tenant administrator requires two
other qualifying human managers because the target cannot check. This decision availability
predicate is stricter than the separate last-manager preservation predicates.

## 4. Exact persistence design

One forward migration adds two tenant-owned tables and integrity support. No existing grant
or permission row is rewritten. Use uuidv7, composite tenant FKs, platform.enable_tenant_rls
and app-role grants following current migrations. Use the integrator-allocated 000058;
do not edit existing migrations. Local schema remains 57 until separate migration acceptance.

### 4.1 iam.role_change_request

| Column | Type / rule |
| --- | --- |
| id, tenant_id | uuid PK/default uuidv7; tenant FK; UNIQUE (tenant_id,id) |
| operation | text NOT NULL, ASSIGN/REVOKE |
| target_membership_id, target_actor_id | uuid NOT NULL, tenant/member/actor binding below |
| maker_membership_id, maker_actor_id | uuid NOT NULL, tenant/member/actor binding below |
| role_id, role_code | uuid NOT NULL composite tenant-role FK; immutable candidate code |
| scope_type | text NOT NULL CHECK = TENANT; no generic scope_id column |
| permission_snapshot | jsonb NOT NULL, canonical nonempty array of {code,sensitivity} |
| configuration_hash | bytea NOT NULL, exactly 32 bytes |
| target_membership_version | bigint NOT NULL > 0, observed at submission |
| revoke_grant_id | nullable uuid composite tenant-grant FK; required only REVOKE |
| revoke_valid_period | nullable tstzrange, exact original range; required only REVOKE |
| reason_code | ASSIGN: ONBOARDING/DUTY_ASSIGNMENT; REVOKE: ACCESS_REVIEW/DUTY_ENDED/SECURITY_CONCERN |
| status | text NOT NULL default PENDING, four-state enum above |
| decided_by_membership_id, decided_by_actor_id | nullable uuid, required only terminal |
| decided_at, decision_reason_code | nullable timestamptz/text, terminal shape below |
| applied_grant_id | nullable uuid composite tenant-grant FK, required only APPROVED |
| applied_membership_version | nullable bigint, APPROVED only, = target_membership_version + 1 |
| applied_valid_period | nullable tstzrange, APPROVED only, resulting grant range |
| created_at, updated_at, row_version | server defaults; version 1; platform touch trigger |

Actor IDs are server-owned immutable evidence. Add UNIQUE (tenant_id,id,actor_id) to
tenant_membership and triple FKs for target, maker and nullable decider, plus global actor
FKs. This prevents pair mutation/inconsistent actor evidence. Existing unique membership
IDs mean no data cleanup is needed. Do not assume (tenant_id,actor_id) is unique: historical
nonoverlapping memberships are permitted by the existing exclusion constraint.

Partial UNIQUE (tenant_id,target_membership_id) WHERE status='PENDING' prevents conflicting
pending proposals. A request does not block A, suspension or offline trusted grant changes;
those touch the target aggregate and stale approval. It only blocks a second B proposal.

Partial unique indexes on (tenant_id,applied_grant_id), separately for approved ASSIGN and
approved REVOKE, permit one approved grant creation and one later approved removal. Verify
applied/revoke grants belong to captured target, role and TENANT scope in the guarded
transition and service transaction. Revocation applied_grant_id equals revoke_grant_id.

CHECKs explicitly reject NULL loopholes: PENDING has all decision/application fields NULL;
APPROVED requires decider/time/application fields and no decision reason; REJECTED/CANCELLED
require decider/time/reason and no application fields. REJECTED reasons: NOT_JUSTIFIED,
INCORRECT_ACCESS, STALE_REQUEST. CANCELLED: WITHDRAWN. APPROVED/REJECTED decider differs from
maker and target; CANCELLED decider equals maker. Maker always differs from target. ASSIGN
has no revoke reference/range; REVOKE has both and a nonempty range.

An immutable-request guard forbids DELETE, proposal edits, terminal edits and every
transition except PENDING to one terminal state. Coordinate touch/guard trigger ordering
to allow platform-owned updated_at/row_version changes; no-op terminal updates still fail.
ASSIGN applied range starts at a finite inclusive bound and has no upper; REVOKE preserves
the original lower and closes at an exclusive upper. Service predicates remain mandatory.

Insert guard validates snapshot array shape, exactly code/sensitivity keys, catalog-code
syntax, sensitivity enum, nonempty/distinct codes in ascending bytewise order and at least
one PRIVILEGED item. Service builds it from persisted rows. Do not FK historical snapshot
items to live permissions or recalculate on reads. configuration_hash is SHA-256 of a
versioned canonical encoding of role ID, code, system=true, scope=TENANT and sorted
code/sensitivity pairs. Pin deterministic fixtures; never hash Go map iteration. Current
template equality is an additional check. Index tenant/status/created_at DESC/id DESC and
tenant/target/created_at DESC/id DESC. No retention/delete endpoint.

### 4.2 iam.role_change_command_receipt

Append-only receipt specific to B, not a generic middleware replacement. Columns: id uuid
default uuidv7, tenant_id, actor_id (global FK), command_code (CREATE/APPROVE/REJECT/CANCEL),
key_hash and request_hash bytea with 32-byte CHECKs, request_id composite tenant FK,
response_status integer (201 CREATE, 200 otherwise), response_etag text, response_body
bytea (serialized JSON, 1–262144 byte CHECK), created_at timestamptz. Required columns are NOT NULL. UNIQUE
(tenant_id,actor_id,command_code,key_hash), UNIQUE (tenant_id,id), tenant RLS and immutable
UPDATE/DELETE guard. Body is the bounded command projection, never arbitrary HTTP input.
Serialize the allowlisted result once before commit; first response and receipt replay
use those same bytes, avoiding JSONB reordering that would break byte-identical retries.
Persist no raw key/body, name, credential, session, contact, cookie or token. No cleanup job.

Use the middleware's operation/path/target/body/If-Match hashing contract; a small shared
hashing helper is allowed. The receipt key_hash is SHA-256 of the trimmed middleware
key; request_hash is exactly the middleware canonical fingerprint, including the original
If-Match. Pass a validated typed command identity to the repository,
not HTTP headers. Same key+fingerprint returns saved status/body/ETag; changed fingerprint
is IDEMPOTENCY_KEY_REUSED. Save receipt with the request/access/audit transaction. Rollback
leaves no success receipt. Process failure before middleware completion then neither
creates another request nor turns a successful exact retry into a stale-version refusal.
Request status/application indexes independently prevent duplicate access effects.

## 5. Transactions, drift and lockout

Use db.WithTenantTx; all user/grant management and sync retain the same tenant lock. B
commands lock tenant first. Read the request under that lock to discover immutable
participants, then use this order: current session FOR SHARE; involved actor rows in UUID
order FOR SHARE; involved memberships in UUID order FOR UPDATE; request FOR UPDATE;
affected roles in UUID order FOR UPDATE; global permission rows in code order FOR SHARE;
referenced grant FOR UPDATE. Reject/cancel need no role/catalog/grant locks. Review A,
suspension and cross-tenant global-actor/session writers for incompatible acquisition.
Never hold a transaction while waiting for a password or human decision.

Under the tenant lock validate current caller authority/session/step-up and immutable actor
separation before consulting a success receipt. Replay authorizes the current caller but
does not rerun target-state, current-maker or already-applied transition checks. It reports
the original result even if the target changed later. Current authority/session/step-up
gates must also precede platform middleware replay; revoked callers and switched sessions
cannot retrieve stored responses. Immutable actor/action rules apply before either replay.

Fresh submission checks target If-Match, target eligibility, candidate configuration and
client catalog hash; REVOKE locks/copies exact valid_period. Insert request, audit and
receipt. Grant rows and target version remain unchanged. Capture actor identities and
snapshot only on server. Request version begins at 1.

Fresh approval checks request If-Match/PENDING and target row_version against captured
target_membership_version. Client cannot replace that target version. Revalidate target
identity/state, current maker/checker authority, role code/system flag/TENANT scope,
current template equality, exact current code+sensitivity snapshot, grant cardinality,
PERSON history and original revoke range. Role label rename alone is not permission drift;
role ID/code/system flag must still match. Missing/extra permissions or sensitivity change
invalidates the proposal even if still privileged. Return ROLE_CHANGE_CONFIGURATION_CHANGED
or ROLE_CHANGE_TARGET_CHANGED and leave pending; require a new proposal.

After waits choose one database wall-clock timestamp for application and final authority,
target, grant, session and step-up validity checks. Bind that timestamp into the final
predicates so separate clock_timestamp() calls cannot authorize an earlier instant.
Derive business date consistently with A/database conventions.
Natural expiry during a wait refuses access. Use this timestamp for range boundary and
decided_at, and include it as the safe application timestamp in both audit details.
Audit record creation timestamps remain recorder/database-owned: audit.Event currently
has no caller-supplied event-time field. Do not expand that interface solely for B or use
transaction-start CURRENT_TIMESTAMP for waited approval.

Insert/close grant, touch membership once with captured expected version, set APPROVED and
resulting grant/version/range, record approval and grant audits, and save receipt in one
transaction. New granted_by is checker who applied it; request/audit retains maker.
Revocation preserves legacy creator/reason. Approval advances request and membership once;
reject/cancel advances request only. Any audit/receipt/constraint failure rolls all back.
No request command synchronizes role configuration.

Extend in-use permission freeze to five B codes: sync/other trusted permission writers
cannot add/remove permissions with any current/future nonempty grant. Unused roles with
pending proposals may change under the shared lock; approval rejects drift. Sensitivity
is migration-owned today: referenced catalog row locks prevent in-flight sensitivity
updates crossing approval commit. Later deliberate catalog/template migrations require
reviewed existing-grant impact analysis outside B. This is an application writer contract,
not protection against unrestricted DB-owner SQL or an immutable live grant snapshot.

Removal preserves at least one effective TENANT identity.user.manage membership if that
authority is being removed, and one effective TENANT identity.role.manage membership if
that authority is removed. Count distinct ACTIVE valid memberships of ACTIVE actors from
current correlated grants, including service actors and finite grants; other grants may
preserve authority in the general helper. Never count role names, pending approvals or
zero-permission grants as managers. Extend suspension under its tenant lock with the same
last-role-manager check, without requiring its caller to hold role-management permission.
Use LAST_TENANT_MANAGER/LAST_TENANT_ROLE_MANAGER. Self rules are separate. Future natural
expiry does not gain a scheduler guarantee through this package.

ADMIN success events: role_change_request.create/approve/reject/cancel, resource
role_change_request; plus access_grant.assign/revoke on approval, resource access_grant.
Include safe request/membership/grant/role IDs, role code, operation, TENANT scope, snapshot
hash, bounded reason and before/after versions/ranges. No names, contact/login, snapshot
body, free text or legacy grant_reason. Actor/membership event fields identify executor;
request evidence retains maker. Same-actor decision denials follow plan/adjustment's
separate post-rollback denial audit with bounded code and no foreign-resource disclosure.
Surface audit failure. Success replays never add audit.

## 6. Public contract

Contract first in api/openapi/kapsora-v1.yaml; generate Go/TypeScript together. Keep A
operations compatible and separate. All B responses, errors and replays are no-store.
Commands require strict bounded JSON, Idempotency-Key, If-Match, ordinary session/tenant/
CSRF and fresh step-up. Typed hash is lowercase hexadecimal SHA-256.

| Method/path under /api/v1/admin | Contract |
| --- | --- |
| GET /privileged-role-assignment-options | Configured B options: code/name/description, scopeType=TENANT, sorted permissionCodes, hasSensitivePermissions, configurationHash, requiresApproval=true |
| GET /users/{membershipId}/role-change-eligibility | Target ETag/version, canRequestAssignment/refusal, bounded currently eligible revoke grant IDs, checkerAvailability relative to caller/target |
| POST /users/{membershipId}/role-change-requests | Target membership If-Match. ASSIGN body {operation,roleCode,configurationHash,reasonCode}; REVOKE {operation,grantId,configurationHash,reasonCode}. 201 command result/request ETag |
| GET /role-change-requests | Bounded signed-cursor page; status optional default PENDING, membershipId optional; directory limits |
| GET /role-change-requests/{requestId} | Static proposal/result plus current action eligibility and checkerAvailability; request ETag |
| POST /role-change-requests/{requestId}/approve | Request If-Match; body {}; 200 command result/request ETag |
| POST /role-change-requests/{requestId}/reject | Request If-Match; {reasonCode}; 200 command result/request ETag |
| POST /role-change-requests/{requestId}/cancel | Request If-Match; {reasonCode:"WITHDRAWN"}; 200 command result/request ETag |

Revoke hash comes from options for its exact grant role; eligibility excludes roles absent
from options. No client permission arrays, actor IDs, scope IDs, validity timestamps or
status. Unknown-field rejection also rejects scopeType: TENANT is fixed server-side.

Static command result: {request,appliedGrant?,membershipRowVersion?}. Request projection:
id, operation, status, targetMembershipId, makerMembershipId, roleCode, scopeType,
permissionSnapshot, configurationHash, targetMembershipVersion, revokeGrantId/validity
when relevant, reasonCode, createdAt, rowVersion, decidedAt, decidedByMembershipId,
decisionReasonCode and approved resulting range/grant/version. Specify nullable fields in
OpenAPI. No global actor IDs or names. Grant uses A's safe identity/role/scope/range fields,
omitting canRevoke/refusal action fields from the durable result. Command results omit
live eligibility flags so stored replay is a truthful original receipt. Detail wraps static
request with canApprove/canReject/canCancel and bounded refusals. Lists omit permission
arrays; detail always shows immutable snapshot evidence. Use authorized directory detail
for display names, never persist them in request/receipt/query keys/URLs. Failed name reads
show safe membership references/reload, never substitute another person.

Approval requires refreshed detail, not list-only action. If-Match identifies request,
not membership or role. Captured target version is immutable evidence; no second client
If-Match substitutes a new target version at approval. Eligibility reads query all relevant
grants and pending requests, not a history page; possible revoke list has at most one item.

Unknown/foreign request, membership or grant returns generic 404. Existing malformed/header
400 and ETAG_MISMATCH 412 apply. New codes: ROLE_CHANGE_PENDING_EXISTS (409),
ROLE_CHANGE_NOT_PENDING (409), ROLE_CHANGE_TARGET_CHANGED (409),
ROLE_CHANGE_CONFIGURATION_CHANGED (409), ROLE_CHANGE_MAKER_UNAUTHORIZED (409),
MAKER_CHECKER_SAME_ACTOR (403), ROLE_CHANGE_CANCEL_FORBIDDEN (403). Reuse
SELF_ROLE_CHANGE_FORBIDDEN for either target actor, existing unsupported/state/last-manager
codes and normal 401/403/step-up errors. NO_ELIGIBLE_CHECKER is a display refusal, not failed
submission or an approval exception. Specify deterministic refusal priority; no foreign
selector leaks through detailed conflicts.

## 7. Product flow and ownership

Add active Management “Rol değişikliği talepleri” with pending/default and terminal history,
bounded paging, mobile cards, clear empty/error states and direct detail. Gate readers with
canManageTenantRoles. User detail offers “Onaya sunulacak rol atayın” on eligible zero-access
targets and “Kaldırma talebi oluşturun” on eligible privileged grants. A's immediate commands
and catalog remain clearly separate.

Submission confirms person, institution, actual role duties, access duration after approval
or removal effect, bounded reason and second-person requirement. “Talep oluşturuldu” must
not imply granted access. Detail shows operation, maker, time, proposed access, state and
current blocker. TENANT_ADMIN is administration, not automatic clinical access. Human
descriptions are primary; exact permission evidence is expandable. Pending revocation
never says access has already ended.

Maker sees pending/cancel, never approve/reject; target sees no decision controls. Checker
gets explicit approve/reject when server permits. Confirmation says approval changes access
immediately. Stale proposal offers explicit cancel/reject and deliberate new proposal,
never auto-rebase. Formal Turkish and English fallback skeleton; no placeholder queue.

Apply A/SuspendMembership frozen command pattern to all four commands: body, original ETag,
key, request/target, actor/session/tenant/capability frozen before step-up. Uncertain retries
survive close/reopen; no edits/double submit. Context loss blocks dispatch; definitive
409/412 needs explicit reload/new attempt. 408/429/IDEMPOTENCY_IN_PROGRESS stays uncertain.
Late step-up/response cannot dispatch, cache or report success after context epoch changes,
including A->B->A. Show committed result despite failed refresh, then refresh live eligibility
separately. No forms, requests or credentials in browser storage.

Ownership after independent review:

- Backend Sol: OpenAPI, assigned migration, SQL/sqlc, identity service/repository/HTTP,
  receipt hashing integration, suspension guard, writer freeze, audits and isolated tests.
- Frontend Sol: integrated generated-client consumers, queue/detail and user actions,
  retry/context guards, translations, focused UI evidence. Start against frozen contracts.
- Integration: assign mocks/generated artifacts explicitly, CI filter, compatibility,
  acceptance, independent Sol security review, docs and landing. Useful bounded generation/
  documentation checks may use Luna; planning changes use Astra.

Do not concurrently edit OpenAPI/generated/mocks without ownership. No new runtime
dependency expected. Follow PRODUCT.md/DESIGN.md and document the settled pattern at
landing. Impeccable is absent in the current catalog; report the limitation and perform
documented visual/keyboard review rather than claim that skill was run.

## 8. Required evidence and acceptance

Use three synthetic humans in isolated fixtures; never broaden an existing demo grant.
New integration names must enter CI's Directory|Invitation filter (for example
TestDirectoryRoleChange...) or explicitly extend it and prove these tests ran unskipped.

- Each of five roles: submit changes no grants/context/membership version; maker and target
  approval refused; distinct checker applies exactly one grant, one membership touch and
  create/approve/grant audits. Target ordinary context gains app and real permitted read;
  TENANT_ADMIN still lacks clinical access.
- Privileged sole-grant revoke: pending leaves access; approval preserves row/lower boundary
  and removes next-request access; ordinary context returns waiting. Other-tenant access,
  credentials and sessions unchanged. Reject/cancel affect no grant or target version.
- Separation across sessions/historical memberships; DB guards/triple FKs/NULL CHECKs,
  inconsistent pairs, proposal edits, terminal edits/deletes, malformed snapshots,
  cross-tenant participants/grants/roles, and direct app-role RLS denial on both tables.
- Current authority before middleware/receipt replay: revoked/expired grant, suspended
  actor/membership, wrong/revoked/expired/switched session, expired/missing step-up, forged
  app headers and unrelated scope/permission splits. Current-maker loss blocks approval;
  maker logout alone does not. Authorized checker can reject stale maker proposal.
- NO_ELIGIBLE_CHECKER for no second or target-only checker; submission stays pending with
  zero effect. Service managers count only toward manager preservation, not human checker
  availability. Availability is not online-session proof.
- Missing/extra permissions, sensitivity change, non-system/custom/unknown/MEMBER, altered
  role identity/scope, zero-permission/future/mixed/PERSON history refusals. Configuration
  changes after catalog read, after submission and during blocked approval; no auto-refresh.
- Actual PostgreSQL barrier races: assign/approve, approve/approve, approve/reject,
  approve/cancel, revoke/revoke, approval/suspend, approval/offline grant, approval/sync.
  At most one terminal/application effect; no two pending proposals for one target.
  Changed target invalidates captured version even if checker knows its newer ETag.
- Expiry while waiting for tenant lock: caller grant, target membership, revoke grant and
  step-up. Finite/infinite/empty boundary checks; coherent read projection/ETag under touch.
- Sync refuses expansion of in-use B role; unused-role sync can stale a pending request.
  Concurrent permission/catalog change cannot cross commit undetected. Preserve A behavior.
- Last-user and last-role-manager checks for revoke/suspend: duplicate grants, finite
  periods, distinct memberships, service managers and user-manage-only suspender. Custom
  synthetic manager roles are caller/invariant fixtures only, never B assignable roles.
- Same-key retry returns original status/body/ETag with no duplicate audit/version. Changed
  body/path/ETag refuses; fault after business commit/before middleware completion recovers
  from durable receipt, including expired/absent middleware cache. Different-key races
  cannot apply twice. Audit/receipt failure rolls back request/grant/both versions. A 409
  after a successful mutation is not evidence of exact-response replay.
- Strict request/response allowlists, no PERSON/global actor/contact/login leakage, signed
  cursors/bounds, capability every page, foreign 404, exact aggregate ETag/no-store on errors
  and replay. Receipt is bounded and has no PII/dynamic authority claims.
- UI/MSW persistent lifecycle, three-actor separation, missing checker, immutable snapshot,
  target/config conflict and next-request context. Step-up cancellation, double click,
  uncertain reopen/retry, failed-refresh success, session/actor/tenant/capability guards.
  Synthetic 390px/1440px visual/keyboard review of queue/detail/confirm/blocker/terminal
  states with no overflow. Mocks use real validity and aggregate-version semantics.

Run generation freshness, Spectral/OpenAPI compatibility, scoped Go format/vet/lint/unit,
affected frontend tests/typecheck/lint/build, Directory/Invitation/RoleAssignment regression
and isolated migration/upgrade/DB tests. Upgrade from actual preceding migration and prove
existing-row preservation. Report skipped DB evidence as skipped, never as acceptance.

Do not run shared local dbtest: it resets the operator app-role password. Use isolated CI
or an independently isolated cluster/role coordinated with integrator. No live provisioning,
migration, server start/stop/restart or seed validation. HANDOVER section 3 leaves server
operation with the operator. Read-only live checks after operator restart prove routing/
denial only; a single live administrator cannot prove two-person mutation.

Follow REPORT_TEMPLATE.md, separating implementation, isolated mutation, synthetic browser,
read-only live evidence and operator steps. A+B closes only these bounded role workflows;
mixed/custom/PERSON/service/scheduled work remains conditional. Acceptance requires all
isolated/backend/UI gates, independent review, unchanged live grants/templates and preserved
existing data. No owner policy decision currently blocks issue; migration allocation and
review are integration gates, and live second-checker availability is a runtime limitation.

## 9. Integration evidence checkpoint (2026-10-09)

Implementation head `676856a` passed five of six CI jobs in
[run 37939046798](https://github.com/celikbros/kapsora/actions/runs/37939046798).
All eight schema cases passed without skips, including the actual 57-to-58 upgrade,
existing IAM preservation, actor attribution and tenant RLS. The HTTP mutation step found
that JSONB textual normalization was incorrectly treated as configuration drift. Both
approval and detail now compare the full semantic snapshot while retaining exact hash,
role identity and template checks; a DB-independent regression verifies representation
changes versus real permission/sensitivity/order/field changes. Integrated runtime
acceptance awaits the corrected head and the added authority/lock/receipt evidence.

No shared local DB test, local migration or live role command was executed. Natural target
membership expiry during a short lock wait cannot be proved deterministically with its
DATE-range model without crossing midnight; a changed-validity-under-lock case is separate
from natural timestamp expiry of caller/maker/revoke grants and step-up. B REVOKE cannot
remove the last role manager when maker and checker are distinct currently authorized
humans; each retains role.manage. Suspension and final user-manager revocation have their
own direct isolated invariant cases. These limits must remain explicit at acceptance.
