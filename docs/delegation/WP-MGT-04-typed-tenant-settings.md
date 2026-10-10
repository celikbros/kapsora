# WP-MGT-04 · Typed tenant settings

| Field | Value |
| --- | --- |
| Milestone | Management, after MGT-03 acceptance |
| Status | PLANNED; permission and setting-impact gate below precedes implementation |
| Planned | 2026-10-09, gpt-6-astra |
| Reviewed | 2026-10-09, gpt-6-sol; permission and consumer gates retained |
| Delivery | Named operational setting view and validated, auditable changes |
| Migration number | Not allocated; integrator allocates in landing order |

This is a specification only. MGT-03B is implemented with isolated acceptance pending at
planning time; this document does not accept it. No API, schema, permission, grant, local
setting or running process is changed by preparing this package. Implement only after the
integrator records the authorization decision in section 3 and assigns the migration.

## 1. User outcome and bounded scope

A tenant administrator can see the institution's supported lodging timing settings,
distinguish configured values from application defaults, and deliberately change a named
value with validation and an audit record. This makes the currently offline configuration
usable without exposing an arbitrary JSON editor or silently expanding clinical or
financial administration.

Proposed initial allowlist:

| Setting | Typed API field | Default | Accepted input | Operational effect |
| --- | --- | --- | --- | --- |
| `accommodation.hold_minutes` | `holdMinutes` | 15 minutes | Integer 1–1440 | Used when preparing a new room hold; existing persisted hold expiry is unchanged |
| `accommodation.quote_ttl_minutes` | `quoteTtlMinutes` | 60 minutes | Integer 1–1440 | Read at booking confirmation against the stored quote timestamp; changing it can affect confirmation of an already-held booking |

The two settings are independent; impose no invented ordering between hold and quote
lifetimes. A shorter quote lifetime can require a fresh quote before a hold expires.
Do not claim either change affects only newly created bookings: that is true of the stored
hold expiry, but false of the current quote-age check. Operations already preparing a hold
may use the value they read before the setting change; no retroactive expiry rewrite is
part of this package.

Exclude all other keys, even if present in the database: lodging check-in windows,
maximum nights and member step-up threshold; generic pricing quote TTL; billing allocation,
invoice evidence, batch or settlement thresholds and duplicate windows; export/download
retention; inpatient admission windows; service-request review policy; notification
providers; secrets and secret references; authentication/session/rate-limit policy; locale,
time zone, currency, legal identity and branding. No feature flags, published contract
changes, clinical policy, payment authority, custom settings or generic key/value endpoints.

## 2. Verified baseline and unresolved consumer discrepancy

Read the delegate handbook, current HANDOVER Management checkpoint, PRODUCT.md and DESIGN.md;
ROADMAP Management; Master Plan v2.0; baseline v1.2 sections 6.2–6.4, 11.11, 16.10–16.11 and
28.2; ADR-004, ADR-008, ADR-015, ADR-016 and ADR-022. WP-I6-04 section 2.3 specifies the existing
lodging settings. Its reference to baseline section 17.5 as tenant settings is inaccurate:
that baseline section is rate limiting, not a settings-management permission.

Concrete sources:

- `db/migrations/000001_extensions_and_platform.up.sql`: `platform.tenant_setting` has
  `(tenant_id, setting_key)` primary key, `value_json`, `is_sensitive`, timestamps and RLS.
  It has an updated-at trigger but **no row_version**. Do not invent an existing ETag.
- `db/queries/platform.sql`: scalar/list reads and seed `UpsertTenantSetting`; the latter
  has no concurrency or request-path authorization and is not a management command.
- `internal/accommodation/settings/settings.go`: defaults, bounded integer interpretation
  and tenant loading. Invalid/missing stored data currently falls back to the default.
- `internal/accommodation/application/booking.go`: `prepareHold` derives expiry from the
  tenant hold duration; confirmation loads the then-current quote duration.
- `internal/identity/application/roles.go` and the permission catalog: TENANT_ADMIN's
  description mentions institution settings, but **no tenant-settings read or change
  permission exists**. `identity.user.manage` administers users; `identity.role.manage`
  administers access. Neither, nor `integration.manage`, authorizes these settings.
- OpenAPI contains no public tenant settings management operations. The automatic-program
  seed is an offline, bounded configuration tool, not an authorized API precedent.

**Hold precedence discrepancy:** WP-I6-04 and the settings comment describe
`contract.lodging_terms.hold_minutes` as a provider override. The inspected `prepareHold`
uses the tenant value directly; the gateway copies the contract field into the later
policy snapshot, but the hold-duration computation does not use it. Do not display a
promise that contract overrides already work. Before implementing a hold editor, resolve
this discrepancy through an independently scoped consumer fix and proof, or record an
explicit decision to defer the hold field. A settings UI must not quietly redefine
published contract terms or fix booking semantics as an incidental change.

## 3. Permission and implementation gate

There is no safe permission-free read slice: even a read-only Management screen needs an
explicit settings authority. Directory access alone cannot unlock it. This is the essential
policy decision, not a reason to reuse a nearby permission.

Recommended concrete decision for integrator/owner review:

1. Introduce `platform.tenant_settings.read` (NORMAL) and
   `platform.tenant_settings.manage` (SENSITIVE), limited to this package's explicit
   operational allowlist. Grant both only to system TENANT_ADMIN in current and newly
   provisioned tenants. No other system role gains either permission; a future read-only
   role assignment is a separate authorized change.
2. Reads require a current TENANT-correlated read grant. Changes require current
   TENANT-correlated read **and** manage grants, ACTIVE valid human membership, ACTIVE actor,
   ordinary local session, CSRF and fresh password step-up. AppAny/backoffice only; scoped
   ORGANIZATION/PROGRAM/PERSON grants do not combine into tenant authority. Use server
   capabilities for navigation and recheck database authority on every operation/replay.
3. These operational integers use direct stepped-up changes, not privileged role approval.
   This decision confers no right to change any security, financial or clinical policy.
4. Explicitly acknowledge that quote-duration changes affect pending confirmations. Resolve
   hold precedence as described above before admitting `holdMinutes` to the write allowlist.

These are **proposed**, not existing grants or already-authorized migrations. Record the
accepted allowlist and grant decision here before implementation. If authorization does
not cover new permissions/grants, the deliverable remains this reviewed plan; do not ship
a dead editor, invent administrator authority from the role name, or mark MGT-04 complete.
A read-only implementation, if chosen, omits every command and is reported as a partial
slice; it still needs the approved read permission.

Adding TENANT_ADMIN permissions changes its exact role configuration used by MGT-03B.
Coordinate with B's in-use template guard and persisted-template equality checks. Pending
privileged requests must retain their original snapshot and remain PENDING with approval
refused on configuration-hash drift until explicitly rejected/cancelled and reproposed. Do not rewrite pending evidence, bypass the in-use guard,
or use offline demo grants to make the screen visible. Any authorized template expansion
needs a narrowly reviewed forward migration for existing system roles and mirrored templates
for new tenants and mocks; never expand custom roles sharing a display name.

## 4. Proposed typed contract

Freeze OpenAPI first after the gate. Use a bounded feature module with application,
repository and HTTP layers; keep generic platform persistence helpers free of HTTP policy.
Identity supplies correlated authority through its application boundary. No cross-module
infrastructure import. Integrator owns route wiring and generated contract integration.

- `GET /api/v1/admin/tenant-settings/accommodation`: current tenant only, no tenant or key
  query parameter. Return a fixed typed projection for the admitted fields and an opaque
  strong ETag, with `canManage` derived from current correlated authority.
- `PUT /api/v1/admin/tenant-settings/accommodation`: replace the admitted typed set with
  required integer fields plus bounded `reasonCode` (`OPERATING_REQUIREMENT` or
  `CORRECT_CONFIGURATION`). Require Idempotency-Key and If-Match. Reject unknown properties,
  nulls, booleans, decimals, strings, overflow and out-of-range integers. No key name,
  value_json, sensitive flag, tenant ID, default override, audit actor or timestamp input.

Each read field has `effectiveValue`, `defaultValue`, `source` (`CONFIGURED`, `DEFAULT`,
`INVALID_FALLBACK`), bounds and nullable `updatedAt`. A configured value equal to its default
still says CONFIGURED. Match existing consumer interpretation of legacy scalar strings;
new writes canonicalize to JSON integer. Never return malformed raw data. If an allowlisted
row is marked sensitive, withhold its contents and return a stable configuration conflict;
do not clear its flag or let generic scalar extraction disclose it.

A successful save returns the committed typed projection and new ETag. No default-reset
command in this slice: entering 15/60 stores explicit values. Reads create no setting rows,
version rows, audit mutation, or other state. Standard 401/403, 412 stale version, 422 field
validation, 428 missing precondition and the established idempotency conflicts have stable
problem codes and Turkish messages. An unsupported route/key must not reveal stored keys.

## 5. Concurrency, persistence and retry obligations

The implementation design must add versioned concurrency rather than use `updated_at` as
an ETag. Recommended minimal design: forward-add trigger-managed `row_version` to existing
setting rows, and derive one strong aggregate ETag from the supported keys' presence,
versions and interpretation version. Absence is an explicit token component. GET computes
this without inserting rows. Lock a stable tenant parent before reading/comparing/writing
the admitted set, lock existing rows in key order, and write all fields atomically.

All public writers follow the same lock order. Offline writers to admitted keys must be
reviewed for the same protocol, including the seed path, so absent-row creation cannot
race an approved change. Existing offline writers of other keys are outside this aggregate.
A trigger advances versions for actual updates from any path; unrelated keys do not make
the typed resource stale. No wholesale JSON replacement, deletion or reset of tenant data.
Do not assign trigger-managed version/timestamps directly in application UPDATE statements.

Authority and step-up are rechecked under the transaction's security locking convention,
including concurrent suspension/revocation. Update, safe audit event and a durable command
receipt commit together. The current idempotency middleware alone cannot bridge a process
failure after business commit and before response persistence. Follow B's durable-receipt
principle, without using its IAM request tables as a generic settings service.

Scope the receipt by tenant, actor, operation/resource and command key, with request hash
covering exact body and original If-Match. Same command returns the original safe response,
status and ETag even after another authorized update; never reapply it or increment versions.
Different content under the same key is refused. Check current authority before returning a
receipt. Saving the same already-canonical configured values succeeds without changing
versions or adding a change audit, but still records its replayable command result. Changing
DEFAULT/INVALID_FALLBACK to CONFIGURED, or canonicalizing a legacy scalar string, is an
explicit configuration change even when the effective integer is equal. Normalize only
on that explicit save, never on read.

Audit only admitted numeric before/after values, source changes, key codes, reason code and resource
versions with standard actor/tenant metadata. No generic raw JSON, request body, secrets or
contact data. New receipt tables require composite tenant FKs where relevant, forced RLS,
app-role grants and negative tenant tests. Migration numbering belongs to the integrator.

## 6. UI and failure behavior

Management gains `Kurum ayarları` only under the server read capability. The existing
Management landing must admit settings-only readers without requiring user-directory
permission. Keep users, role requests and settings access independent; direct links enforce
the same boundary. Show institution, `Konaklama`, meaningful field labels, units, default
versus configured source, effect text, explicit loading/error/empty behavior and keyboard
accessible controls. Never display raw setting keys as the primary user explanation.

A read-only operator sees values and explanatory text without editable controls. Invalid
legacy data says the application default is in use; it is never silently repaired. A read
failure leaves no saveable draft. Supported changes show a before/after confirmation,
institution, pending-booking effect where applicable and the bounded reason before password
confirmation. Follow PRODUCT/DESIGN and the required Impeccable shape/craft/finish workflow;
report the skill's absence if unavailable instead of claiming it was run.

Freeze body, original ETag and command key before password confirmation. Pending/uncertain
commands disable editing; network loss, 408, 429 and IDEMPOTENCY_IN_PROGRESS preserve an
identical explicit retry through dialog close/reopen and cancelled password prompts.
Definitive conflicts require a successful explicit reload before a new command. Never
silently merge stale settings or resubmit changed input under the old key. Successful
committed values remain visible if refresh fails, with a separate retry for current state.
Session/account/tenant/capability changes clear drafts and cache, prevent deferred dispatch
and suppress late responses, including switching away and back. No browser storage.

## 7. Evidence required before acceptance

- Unit/HTTP: accepted integer boundaries, all rejected types, unknown properties/keys,
  defaults, legacy configured scalar, invalid fallback, sensitive-row refusal, no-op and
  reason validation. Consumer interpretation and UI projection agree.
- PostgreSQL: missing-row reads write nothing; tenant isolation; atomic two-field update,
  absent-row concurrency, stale ETag, unrelated-key preservation, audit rollback, same-key
  receipt after committed-response loss, intervening update, mismatch and cross-actor replay.
  Simultaneous suspension/revocation cannot permit an unauthorized write or receipt read.
- Authorization: anonymous, directory-only, user-manager-only, role-manager-only,
  organization-scoped, split-scope, expired/suspended membership, service actor, provider and
  member contexts denied as appropriate. Read-only permission can read but cannot mutate.
- Consumer proof in disposable databases: a new hold uses the changed admitted duration;
  an already persisted expiry is untouched; quote confirmation uses the changed duration
  and has the documented pending-booking effect. Hold override precedence has its own proof
  if the separately scoped correction is included before this package lands.
- Template migration: approved existing/new TENANT_ADMIN grants match; no unrelated/custom
  role expansion; mocks equal real templates. Pending B snapshots are unchanged and drift
  refuses approval, without weakening direct-role or privileged-role guards.
- UI: loading/failure/read-only, defaults/invalid state, validation, password cancellation,
  lost committed response and identical retry, stale explicit reload, refresh failure after
  success, context change during step-up/in-flight response, 390px/1440px layout and keyboard.
- Generate Go/TypeScript contracts and sqlc; OpenAPI lint, focused backend/schema/frontend
  tests, typecheck, lint and app builds, then required CI. State skipped tests explicitly;
  intercepted browser responses are UI evidence, not database or consumer proof.

Use isolated test databases and synthetic fixtures. Do not start/restart servers, apply a
local migration, alter existing demo settings/grants/contracts or run live writes for this
work package without the corresponding authorization. A later live check requires the
operator's reload and a separately bounded synthetic scenario. Report exact code head,
commands and evidence; update handover/roadmap/product/design only at integration under
integrator ownership. MGT-04 is complete only after its admitted read/change contract and
consumer/security evidence pass, not when this plan or a read-only panel exists.
