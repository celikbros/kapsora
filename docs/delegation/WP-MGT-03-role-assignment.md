# WP-MGT-03 · Assign and revoke scoped tenant access

| Field | Value |
| --- | --- |
| Milestone | Management, after MGT-01 and MGT-02 invitation acceptance |
| Status | A implemented with isolated database acceptance on 2026-10-09; live read acceptance awaits operator reload |
| Planning | gpt-6-astra |
| First delivery | MGT-03A: activate a zero-access human membership with one supported role; revoke that supported access |
| Later delivery | MGT-03B: privileged approval pipeline; other combinations remain conditional |
| Migration numbers assigned | None for A. B uses integrator-allocated 000058 for isolated verification; local schema remains 57 |
| Depends on | Directory, invitations, local sessions/step-up, transactional audit, command idempotency |

## 1. Outcome and boundaries

A tenant role manager opens an ACTIVE invited membership that is waiting for access,
selects an existing supported system role and its allowed scope, confirms the effect,
and completes password step-up. The target's next fresh tenant context exposes the
appropriate app and its first real task. The manager can later revoke that grant;
the target loses that access on the next request while retaining its account and
membership. Assignment and revocation leave tenant-scoped history and atomic audit.

A is a deliberately bounded working increment, not completion of MGT-03. It does not
expose the provisioner as a public API. It does not create/edit roles or permissions,
change role templates, synchronize permissions, create memberships, reactivate users,
link people, change passwords, or give existing live users new access for testing.

Read `docs/delegation/README.md`, HANDOVER sections 3–6, PRODUCT.md, DESIGN.md,
`docs/plan/ROADMAP.md`'s Management sequence, the MGT-02a/02b/02b2 packages,
Master Plan v2.0 and ADR-022. The unchanged baseline v1.2 sections 6.2–6.4, 9.2 and
phase-2 acceptance require scoped administration and maker-checker for privileged
role changes. Master Plan v2.0 and ADR-022 do not cancel that requirement.

## 2. Verified model constraints

Implementation must retain these actual repository facts:

- `iam.role` and `iam.role_permission` are tenant-owned. `iam.permission.sensitivity`
  is NORMAL, SENSITIVE or PRIVILEGED. There is no persisted privileged-role flag,
  grant approval request, grant status or grant row version.
- `iam.access_grant` names a tenant membership and tenant role, with `scope_type`,
  optional `scope_id`, `tstzrange valid_period`, creator and legacy free-text reason.
  The database enforces the membership/role tenant pair and TENANT/null shape.
  General ORGANIZATION scope has no target foreign key.
- A provider ORGANIZATION ID means `directory.tenant_organization.id`, not the global
  organization ID or provider-profile ID. Resolve it inside the selected tenant.
- Migration 000039 adds PERSON scope, its tenant/person foreign key, and unconditional
  unique indexes binding one person to one membership and vice versa. MEMBER's template
  still says TENANT, while real member grants use PERSON. Never derive public MEMBER
  assignment from that template default. Person binding needs its own identity-proof
  and historical uniqueness design and is excluded from both A and B.
- `Provisioner.GrantRole` is an offline provisioning helper. It can create missing
  memberships, its explicit scope is not restricted to the template default, its
  duplicate lookup only checks an unbounded upper period, and its audit is outside
  the mutation transaction with ignored audit errors. Its comment is not proof of a
  safe role/scope policy. Do not wrap or call it from these routes.
- `Grants.For` preserves grant correlation while choosing an app, then unions that
  app's permissions and scopes. Several different provider roles across organizations
  can consequently combine permissions across those organizations. A must not create
  such combinations. App headers are caller-controlled and are not authorization.
- Current grants resolve on every protected request through `ListGrantsForMembership`.
  Directory manager predicates and invitation `accessPending` also use grant validity.
  Closing the effective range therefore removes access without global session deletion.
- MGT-02a serializes user management by locking the tenant row before the membership,
  then rechecks current authority. New commands must use the same lock order.
- Directory membership `row_version` already has the database touch trigger. The
  provider module's `TouchProviderLocation`/`TouchPractitioner` queries establish the
  existing pattern for advancing a parent's ETag when child rows change.

Primary implementation references are `db/queries/iam.sql`, `db/queries/invitation.sql`,
`internal/identity/application/{roles,authorizer,provisioning,directory_suspend}.go`,
`internal/identity/infrastructure/postgres/{directory,provisioning_repository}.go`,
`internal/identity/transport/http/directory_handler.go`, and
`internal/provider/application/{service,capability}.go`. Review existing plan-version
and entitlement-adjustment maker-checker code before designing B; domain approvals are
examples, not an existing generic IAM approval service.

## 3. MGT-03A decisions

### 3.1 Authority and supported assignments

Require both current TENANT-correlated `identity.user.read` and TENANT-correlated
`identity.role.manage` for every new management read and command. Each permission
must originate on a currently valid TENANT grant of the caller's ACTIVE, currently
valid membership and ACTIVE actor. Do not correlate flattened permissions/scopes.
`identity.user.manage` alone, PROVIDER_ADMIN, an ORGANIZATION-scoped role-management
permission, and provider/member app contexts cannot authorize this workflow.
AppAny is accepted only when the same database predicates succeed, as in MGT-02a.

Add optional `canManageTenantRoles` to tenant context, resolved consistently for `/me`
and tenant switching. It is true only when both predicates hold in AppAny/backoffice;
absence is false. This adds no permission to any system or custom role. Commands also
require the existing session, active tenant, CSRF and fresh password step-up, before
idempotency replay and again as appropriate at actual mutation time.

Use this fixed candidate allowlist, with the exact scope shown:

| Scope | Candidate codes |
| --- | --- |
| TENANT, no scope ID | PROGRAM_MANAGER, CONTRACT_MANAGER, RULE_AUTHOR, MEDICAL_REVIEWER, FINANCIAL_REVIEWER, AUDITOR, SPONSOR_HR |
| ORGANIZATION, one provider relationship | PROVIDER_ADMIN, PROVIDER_STAFF, PROVIDER_BILLING, PROVIDER_RESERVATION |

Every candidate must also be an existing `is_system_role=true` row whose persisted
permission set exactly equals the current `RoleTemplates` set for that code. Require a
nonempty set and known catalog entries, and refuse any candidate containing a PRIVILEGED
permission. Compare at catalog read and inside the command transaction; a name/code or
system flag alone is insufficient. Template sync is additive and can retain old extras.
Do not repair drift or create a missing role. Return `ROLE_CONFIGURATION_UNSUPPORTED`.

Permission equality must be stable through commit. Lock the tenant, target membership,
then candidate `iam.role` row before loading its permission set. All trusted permission
writers, including `SyncSystemRoles`, must first acquire the same tenant lock and then
the affected role rows in UUID order; a role-row lock alone does not prevent every
child-row update/delete. Sync has no target membership to lock. Existing unsupported
or privileged roles do not bypass this writer protocol. Inspect every role-permission
writer and prove concurrent sync/assignment cannot commit against a stale set.

A grant references a live role, not an immutable permission snapshot. For the A
candidate codes, trusted runtime/offline synchronization must refuse any permission-set
change while that role has a current or future grant; acquiring a lock alone does not
make later expansion safe. Missing-role provisioning and synchronization of unused roles
can retain their existing behavior. Do not silently revoke grants or change permissions
to clear this refusal. Deliberate permission/template/catalog migrations are a separate
reviewed upgrade with impact analysis for existing grants; A does not authorize one.
This is an application writer contract, not a claim that unrestricted database-owner
SQL is prevented. Record that operational limit in the implementation report.

The candidate allowlist is intentionally explicit. TENANT_ADMIN, PLAN_PUBLISHER,
CONTRACT_PUBLISHER, RULE_APPROVER and PAYER_APPROVER are unavailable in A; MEMBER,
custom roles and unrecognized/new roles are also unavailable. A future template/catalog
change can remove a candidate from availability, but cannot silently admit a new role.
SENSITIVE is distinct from PRIVILEGED: clinical and sensitive-report roles above are
explicit choices, shown with their actual access description, and require step-up.
Do not describe them as harmless/read-only or imply TENANT_ADMIN already sees medicine.

Only ACTIVE HUMAN actors with ACTIVE memberships valid today can receive a grant.
Refuse any self-assignment or self-revocation by comparing global actor IDs, including
another membership belonging to the caller. Refuse assignment if the target has any
grant effective now or at any future instant, of any role/scope, including a grant whose
role has zero permissions, or any historical PERSON grant. Empty validity ranges are
not effective; permission-set emptiness must never exempt an effective grant.
An expired historical non-PERSON grant does not prevent a new assignment. Do not
silently replace existing access. This permits an invited zero-grant member to obtain
one real app without introducing mixed-scope authorization behavior.

Start new access at one server-chosen transaction timestamp, with an unbounded upper
period. A has no caller-supplied start/end, backdating, scheduling, extension, scope edit
or role replacement. Show explicitly that it lasts until removed or the membership
ceases to be usable. Require an explicit reason enum: ONBOARDING or DUTY_ASSIGNMENT.

For ORGANIZATION candidates require an ACTIVE provider relationship valid today in
this tenant, an ACTIVE global organization and an ACTIVE provider profile with
`contracted_from IS NULL OR contracted_from <= CURRENT_DATE` and
`contracted_to IS NULL OR contracted_to > CURRENT_DATE`. Picker and command use the
same predicates and exclusive upper bound, following the business-date convention in
baseline v1.2 section 11.1. Existing profile CRUD does not itself enforce access expiry.
Reject foreign, absent, non-provider, suspended or expired targets. A tenant manager
may select any qualifying provider in this tenant; an organization-scoped administrator
cannot use this route even for their own organization. The selection grants access only
to that relationship. Use a narrow server-owned picker projection, not an unvalidated UUID.
Lock/recheck the chosen relationship/profile before committing and use a consistent
lock order. Do not invent cross-module infrastructure imports.

### 3.2 Revocation, versions, history and lockout

A revokes only a current nonempty grant matching an exact supported role/scope pair,
on an ACTIVE valid HUMAN membership, where it is the target's sole current/future grant
and no PERSON binding exists. Count effective grants with zero-permission roles too;
their scopes still enter the current authorizer. Unsupported, legacy-drifted, privileged,
future and
already-ended grants remain visible as appropriate but have no mutation action.
Use bounded reasons ACCESS_REVIEW, DUTY_ENDED or SECURITY_CONCERN.

Close the grant range at the server timestamp: preserve its original lower bound and
set the exclusive upper bound to the revocation time. Never delete it, reopen it or
edit `granted_by`/legacy reason. No empty or future range may accidentally become
unbounded. A fresh revoke of an ended grant is a state conflict; exact idempotent replay
returns the original result. A later re-assignment creates a new row and history.

Use the membership as the optimistic-concurrency aggregate for all new grant commands.
Require its existing ETag; lock tenant, membership, role rows in UUID order, then
selected scope/grant rows in a fixed order,
and recheck authorization, target state, allowlist, template equality and overlap in
that transaction. After an actual insert/range closure, touch the membership with
`SET membership_status = membership_status WHERE ... AND row_version = expected`.
The existing trigger owns `updated_at` and `row_version`; never assign either directly.
This makes concurrent suspension and role changes invalidate each other's stale views.
Extend the trusted offline grant writer narrowly to use the same tenant→membership
serialization and touch an existing membership when it inserts a grant; preserve its
existing API and seed semantics. Do not run it against live data as part of this task.

Serialize competing commands and check the post-change manager population before
committing any removal. Preserve the existing effective `identity.user.manage` tenant
manager invariant, including qualifying service actors, and also preserve at least one
effective TENANT `identity.role.manage` manager if the operation removes such authority.
Count distinct active/valid memberships from correlated grants, not role names; another
grant on the same target can keep it effective. Self-change refusal is separate. A's
allowlist normally makes tenant-management removal unreachable, but do not build a
removal helper that B can later call without these checks. Suspensions and B approval
must share the tenant lock and the same invariants; B must extend suspension protection
for the last role manager before admitting privileged mutations.

Record one ADMIN success event in the mutation transaction, resource `access_grant`,
with action `access_grant.assign` or `access_grant.revoke`, actor, membership/grant IDs,
role code, scope type, safe organization relationship ID where applicable, prior/new
validity and bounded reason. Audit failure rolls back grant and membership version.
Do not copy names, contact data, login handles, raw request bodies or old free-text
`grant_reason` into events. Preserve legacy grant rows and audit records. The UI may
call an ended range “ended”; it must not infer a human revocation from elapsed time alone.
This increment records the decision in audit; a general audit-history browser is excluded.

No migration is needed for A's effective-period history and aggregate version. If code
review establishes a concrete schema necessity, report it and obtain the integrator's
landing number before writing a migration. Never edit migrations 000002/000039/000057.

## 4. Public contract and projection

Contract first: add these operations under `/api/v1/admin`; generated Go and TypeScript
contracts must land together. All reads and responses are no-store.

| Method | Path | Projection or command |
| --- | --- | --- |
| GET | `/role-assignment-options` | Available candidate code/name/description, exact required scope, sorted permission codes and whether sensitive permissions are present |
| GET | `/role-assignment-organizations` | Bounded cursor page of qualifying provider relationship ID, display name and tenant code only |
| GET | `/users/{membershipId}/role-grants` | Bounded cursor page of grant history and aggregate membership version/ETag, plus current assignment eligibility |
| POST | `/users/{membershipId}/role-grants` | `{roleCode, scopeType, organizationRelationshipId?, reasonCode}`; ETag + Idempotency-Key required |
| POST | `/users/{membershipId}/role-grants/{grantId}/revoke` | `{reasonCode}`; aggregate membership ETag + Idempotency-Key required |

Both commands return `{membershipId, membershipRowVersion, grant}` with the new aggregate
ETag. The grant is the same allowlisted projection as the management grant read; do not
return a complete actor or credential object. The frontend can show committed success
from this result even if later list/detail refresh fails.

The management grant projection contains grant ID, role code/name/system flag, scope
type, nullable validity ends, `validityEmpty`, and `canRevoke` with a bounded refusal code.
For supported ORGANIZATION grants only, include relationship ID and display name after
in-tenant resolution. Never expose PERSON/program/location/work-queue scope IDs,
actor IDs, other memberships, contact/login identifiers, `grant_reason`, or raw audit
payloads. Unsupported scopes show their type and no editable target. Keep MGT-01's
ordinary `assignedRoles` projection unchanged: new IDs belong only to the more strongly
authorized management endpoint. Picker strings are not authorization claims.

Pagination defaults/maxima and signed cursor behavior follow the existing directory.
`canAssign` and `canRevoke` are server-derived display aids. Assignment eligibility must
query all current/future grants, not just the displayed page. Page ETags reference the
same membership aggregate; fail/refresh explicitly if pages were loaded at different
versions. Do not cache cursor pages without actor/session/tenant/capability context.

Strict JSON, bounded body, unknown-field/trailing-data rejection, exact enum/scope shape
validation and normal header checks apply. Reject a TENANT assignment carrying any
organization ID, an ORGANIZATION assignment lacking one, and every other scope.
Unknown or foreign membership/grant/organization selectors return a generic 404 without
revealing global identity. State conflicts use 409; stale versions use existing
`ETAG_MISMATCH` 412; malformed inputs use 400. Freeze named domain codes in OpenAPI:
`SELF_ROLE_CHANGE_FORBIDDEN`, `ROLE_CONFIGURATION_UNSUPPORTED`,
`ROLE_ASSIGNMENT_UNSUPPORTED`, `MEMBERSHIP_STATE_CONFLICT`, `EXISTING_ACCESS_CONFLICT`,
`GRANT_STATE_CONFLICT`, `LAST_TENANT_MANAGER`, `LAST_TENANT_ROLE_MANAGER`.
Do not expose a misleading approval URL for privileged selections before B exists.

Use the existing opt-in If-Match idempotency fingerprinting. Tenant, actor, target,
operation, body and original ETag define the command. Replays require current authority,
active session and step-up, but do not rerun successful state transitions or duplicate
audit. Changed body/ETag under the same key is not a retry. Concurrent equivalent grants
under distinct keys produce one transition and a clear conflict, not duplicate access.

## 5. UI and app activation

Extend the existing backoffice user detail with “Rol atayın” and grant-specific
“Kaldırın” actions, gated by `canManageTenantRoles` and loaded server eligibility.
The selection names the role, actual access, app, current tenant and organization when
applicable. Confirmation names the target and explains access duration/effect. Never
render raw permission codes as the primary user-facing explanation. Formal Turkish;
English resources retain the usual fallback skeleton. No generic permission matrix,
custom role editor, disabled placeholder approval queue or global-account controls.

Follow `SuspendMembership`'s exact retry/context pattern: freeze submitted body, ETag,
key, tenant, target and authenticated context before step-up; uncertain outcomes retain
that snapshot through close/reopen. Disable edits/double-submit while uncertain. A
409/412 requires explicit reload and a deliberate new attempt. Deferred step-up callbacks
and late responses cannot dispatch, write cache or show feedback after session/actor/
tenant/capability changes, including A→B→A. Check context at dispatch and completion.

Provide a refresh-access action on the existing zero-grant waiting view in each app.
It re-fetches the ordinary authenticated context; it does not poll, re-login, inspect
invitation secrets or request a grant itself. After assignment the person sees the
existing app switch/entry behavior. After revocation an already-open session's next
protected request is denied and refreshing context restores the waiting state when no
other app access exists. Do not delete sessions or claim to cancel requests already
accepted before revocation.

Mocks must use grant validity and membership aggregate versions for catalog, directory,
context/apps, waiting state and every new command. Do not grant TENANT_ADMIN to a demo
invitee or broaden system templates to make a screen pass. Use synthetic fixtures only.
Apply PRODUCT.md/DESIGN.md and available UI-review skills; if Impeccable remains
unavailable, report that limitation and perform documented visual/keyboard review.

## 6. MGT-03B and conditional follow-on

A must refuse privileged assignment/revocation rather than silently bypass the baseline.
For B, conservatively classify any role containing a catalog PRIVILEGED permission as
privileged, regardless of target actor. “Critical user” has no implemented classification;
that missing concept must not become an exception permitting direct grants. Treat
privileged revocation as a privileged role change too, consistent with phase-2 acceptance.
No production/live bootstrap or emergency bypass is implied.

Before B is issued, Astra must freeze its exact schema, contracts and decision gates in
a separate addendum. Its minimum safe design is a durable, tenant-owned role-change
request with immutable proposed operation/role permission snapshot/scope/target/reason,
requester, timestamps, row version and terminal decision; composite tenant FKs, RLS and
unique application prevent duplicate effects. Submission does not grant access. An
explicit step-up approval by another currently authorized actor applies the change and
records approval plus grant/audit atomically. Maker cannot check; neither actor may be
the target. Revalidate maker/checker authority, target state, scope, exact permission
snapshot, self/last-manager rules and aggregate ETag at approval. Drift/stale targets
require a new proposal, not an automatic refresh. Reject/cancel produce no grant. Commands
need normal idempotency/versioning and all transitions need durable audit.

The existing `identity.role.manage` permission can authorize distinct maker/checker
actors; do not invent new permissions or grant a second live administrator merely to
make a test/demo possible. If only one qualifying administrator exists, display the
concrete lack of a second checker and leave the request pending or unavailable according
to B's frozen contract. Isolated tests can create two authorized synthetic actors.

Further conditional work includes mixed-role/multi-organization access (first fix and
verify downstream permission/scope correlation), scheduled grants/expiry manager
invariants, service-account administration, custom-role policy and person binding.
These are not automatically included in B. Before admitting every new combination,
prove that its effective downstream authorization matches what the confirmation shows.

## 7. Work ownership and evidence

Planning/decomposition is Astra. Delegate independent substantial backend/frontend
implementation and security review explicitly to gpt-6-sol; use gpt-6-luna for useful
bounded documentation/generation checks. Do not delegate solely to occupy slots.

Backend owns new OpenAPI/sqlc/Go contracts, correlated capability, queries, service,
repository/HTTP wiring, transactional audit, trusted-writer aggregate touch, coordinated
permission-writer locking/in-use-role refusal and isolated
DB/HTTP evidence. Frontend owns typed wrappers, admin detail/dialogs, waiting refresh,
auth-context guards, translations and UI tests. Integration owns coordinated mocks,
cross-workspace generation/build, security review, CI, evidence and landing. Keep edits
within those owners until contracts are integrated. No general role-template changes.

Required proof:

- Accept a new-account invitation in an isolated fixture: one ACTIVE zero-grant
  membership and waiting state; assign RULE_AUTHOR through the public command; ordinary
  context exposes backoffice and a real allowed rule read; revoke, then next-request
  denial and waiting state. Repeat existing-account acceptance with other tenant access
  unchanged. No global actor/contact/credential/session mutation accompanies grant work.
- A separate synthetic provider flow assigns a supported ORGANIZATION role and proves
  positive provider work plus negative reads/actions for another provider. Test global
  organization and provider-profile IDs cannot stand in for relationship IDs.
- Correlated authority under normal, omitted and forged app headers; permission split
  across unrelated grants, expired grants, inactive memberships/actors, missing step-up,
  stale session and revoked caller denial before stored replay. No bare user-manage access.
- Every supported candidate, privileged/custom/MEMBER/unknown refusal, extra/missing
  persisted role permissions, zero-permission and changed sensitivity. Assignment never
  synchronizes roles. Concurrent sync/assignment uses the shared lock; later sync of an
  in-use candidate role cannot add/remove permissions, while unused-role sync still works.
  One-existing-grant, future-grant, mixed-scope, effective zero-permission ORGANIZATION
  grant and historical PERSON refusals.
- Cross-tenant selectors/RLS and strict response property allowlists, including grant
  reads with hidden PERSON identifiers and unsupported roles, and bounded pagination.
- Exact safe retries; changed ETag/body/key; concurrent assign/assign, assign/suspend,
  revoke/revoke and target/caller changes; audit failure rollback; one version bump and
  event per transition; ended-row preservation; trusted grant writer moves aggregate ETag.
- Self protection and post-change last-manager predicates, including duplicate grants,
  finite validity and qualifying service managers. Verify the privileged path is not
  reachable in A; do not claim A demonstrates implemented privileged approval.
- UI/mock permission denial, role/organization confirmation, step-up, double-submit,
  frozen uncertain retry/reopen, definite-conflict reload, success with failed refresh,
  actor/session/tenant/capability switches and late step-up/response guards; refresh access
  in all three waiting screens. Keyboard and synthetic 390px/1440px review, no overflow.
- Generation freshness, OpenAPI compatibility/lint, scoped Go formatting/vet/lint/tests,
  frontend tests/typecheck/lint/build and existing invitation/directory regression gates.
  Include new integration test names in CI's isolated Directory|Invitation filter or
  extend that filter explicitly; report skipped database tests as skipped.

Do not run the shared local dbtest harness: it resets the application-role password.
Run database evidence in isolated CI or an independently isolated cluster/role agreed
with the integrator. The operator owns servers per HANDOVER section 3; do not restart
services, apply migrations, alter live roles/users or run live fixture provisioning.
Read-only live boundary checks may follow an operator restart after implementation;
they do not prove mutation acceptance. Follow `REPORT_TEMPLATE.md`, distinguish A from
B and report actual evidence, remaining gates and any normative interpretation above.

## 8. Acceptance checklist

- [x] A new invited zero-grant human obtains one real supported app/task through a
      tenant-authorized, stepped-up, audited public assignment.
- [x] Scoped access is enforced and revocation takes effect on the next request.
- [x] No privileged/custom/PERSON/mixed-role bypass or template permission expansion exists.
- [x] History, aggregate ETags, replay, suspension races and audit rollback are verified.
- [x] Isolated backend and UI/mock evidence plus visual checks pass; CI includes DB cases.
- [x] A's delivered scope and B's pending approval pipeline are reported separately.

These checks refer to isolated PostgreSQL and synthetic UI evidence within A's bounded
scope. They do not certify live grant mutation or B's privileged last-manager variants.

## 9. A implementation checkpoint (2026-10-09)

The public role-options/provider-picker/grant-history reads and versioned assign/revoke
commands are implemented. User detail offers confirmed role assignment/removal, exact
uncertain retries, conflict reload and guarded late callbacks. All three waiting screens
refresh ordinary authenticated context explicitly. Current session/step-up and correlated
TENANT authority are rechecked after lock waits immediately before the grant write.
Permission writers share the tenant lock and refuse changing in-use candidate templates.

Local Go unit/compile, scoped vet/lint and the database-independent Chi dispatch test pass.
Eighteen new database-backed cases compile but skip locally under the mandated blank
admin URL. They cover new/existing invitation activation, real rule/provider endpoints,
all eleven templates, role/permission sensitivity drift, private/future/empty-access
boundaries, provider eligibility, replay including tenant switching, offline ETag touch,
audit rollback and assign/suspend/revoke/sync races. All eighteen ran without skips and
passed on `e26bcc8` in the PostgreSQL job of
[CI run 37925743677](https://github.com/celikbros/kapsora/actions/runs/37925743677).
All six CI jobs passed on that code head, including 741 frontend tests, 25 browser smoke
tests and seven Directory/fifteen Invitation HTTP regressions without skips. Normal smoke
skipped 104 opt-in live/calendar cases. The database-independent dispatch case also passed.
The synthetic browser review passes 36 views at
1440px/390px across all apps, with no overflow, page errors or real API requests.
The full frontend suite passes 741 tests in 90 files; workspace format/type/lint and
all app builds pass. Generated bindings regenerate unchanged, contract compatibility
is nonbreaking and Spectral has zero errors with eleven pre-existing warnings.

The opt-in `tests/e2e/management-role-grants-readonly.spec.ts` is ready for an operator
reload and does not perform role mutations. Schema is unchanged at 57. A cannot reach
privileged last-manager revocation: duplicate/finite/service-manager variants remain
code-reviewed, with their direct proof assigned to B. A distinct same-tenant membership
for the same actor may exist in a nonoverlapping historical period under the exclusion
constraint; self-change compares global actor identity, with a dedicated isolated case.
The provider-relationship ID is now verified in both command and history projections;
historical same-actor membership self-protection has direct isolated coverage. The
[B package](WP-MGT-03b-privileged-role-approval.md), planned by Astra and independently
reviewed by Sol, is authorized and in implementation after the owner's response to the
concrete scope question. Its distinct maker/checker approval
pipeline and MGT-04 remain separate deliveries.
