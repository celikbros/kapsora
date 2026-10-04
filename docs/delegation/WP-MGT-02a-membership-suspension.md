# WP-MGT-02a · Suspend a user's access to the current tenant

| Field | Value |
| --- | --- |
| Milestone | Management, following the MGT-01 tenant user directory |
| Size | M |
| Depends on | MGT-01 directory, local sessions/step-up, audit and command idempotency |
| Parallel ownership | Backend owns OpenAPI/generated contracts, service, repository and capability projection; frontend owns client/UI/i18n; integration owns mocks and verification |
| Migration numbers assigned | none |
| OpenAPI operations owned | New membership suspension command; versioned directory detail and list projection |
| Status | Implementation specification; acceptance evidence must be recorded separately |
| Read first | `docs/delegation/README.md`, `PRODUCT.md`, `DESIGN.md`, ADR-022, baseline v1.2 sections 6.2–6.4 and 9.2, existing identity directory and command middleware |

## 1. Goal

A tenant manager opens an existing user membership, explicitly confirms suspension for
that tenant, and sees the resulting membership status. Subsequent requests cannot use
the suspended membership. The person's global account and other tenant memberships
remain intact.

This package adds one real management command. It does not complete onboarding, role
administration, tenant settings or the remaining Management scope.

## 2. Scope and decisions

### 2.1 Tenant membership, not global account

The command changes only `iam.tenant_membership.membership_status` from `ACTIVE` to
`SUSPENDED`, with the existing row-version mechanism. Preserve membership validity,
role grants and history. Do not change `iam.actor`, credentials, other memberships or
global sessions. Do not call `CreateAccount`, seed provisioning or `RevokeAllForActor`.

Exclude invitations, reactivation, revocation, password reset, global suspension,
role assignment/restoration, provider administration and general settings. Restoring
access can reactivate privileged grants and needs its own specified workflow.

### 2.2 Authorization and lockout prevention

- Require `identity.user.manage` on a currently valid **TENANT-scoped grant** of the
  caller's active membership, retaining permission/scope correlation. Flattened
  permissions and caller-supplied app headers cannot establish this capability.
- Use the existing session, active-tenant, CSRF and step-up checks. Authorization
  and step-up must run before a stored command response can be replayed.
- Expose optional `canManageTenantUsers` in tenant context, derived by the server;
  absent means false. This changes no role templates or permissions.
- Refuse self-suspension independently of the last-manager check.
- Preserve at least one other effective tenant manager: an ACTIVE actor with an
  ACTIVE membership valid today and a TENANT-scoped grant valid now whose role
  carries `identity.user.manage`. Count qualifying service actors as well as humans;
  do not infer qualification from a role name or the existence of any role row.
- Serialize competing membership suspensions for the tenant and lock the target row
  so two concurrent commands cannot each rely on the manager the other suspends.
  Recheck the caller's authority inside the serialized transaction.
  Later role/membership commands that can remove management access must participate in
  this serialization protocol; this package does not introduce those commands.

Tenant/scope isolation, command concurrency, idempotency and audit are existing
repository requirements. Self-suspension refusal and the exact last-effective-manager
predicate are explicit product decisions for this package. Step-up is required here
for the sensitive membership command. This package does not introduce a second-person
approval queue: baseline maker-checker requirements for privileged **role changes**
remain applicable to the later role workflow, which this command does not implement.

### 2.3 Public contract

Extend directory list/detail membership projections with `rowVersion`; detail GET
returns an ETag. The mutation uses the tenant membership ID, never a global actor ID.

| Method | Path | Contract |
| --- | --- | --- |
| GET | `/api/v1/admin/users` | Existing tenant-scoped list, including membership `rowVersion` |
| GET | `/api/v1/admin/users/{membershipId}` | Existing allowlisted detail with membership `rowVersion` and ETag |
| POST | `/api/v1/admin/users/{membershipId}/suspend` | Required tenant header, `If-Match`, `Idempotency-Key` and `{reasonCode}`; returns updated allowlisted detail and ETag |

The bounded reason enum is `ACCESS_REVIEW`, `STAFF_DEPARTURE`,
`SECURITY_CONCERN`. No free-text reason or personal identifier is collected.

Use existing authentication, validation, missing-header and idempotency errors. The
specific domain refusals are:

| Code | Meaning |
| --- | --- |
| `SELF_SUSPENSION_FORBIDDEN` | Target membership belongs to the acting account |
| `LAST_TENANT_MANAGER` | Suspension would leave no other effective tenant manager |
| `MEMBERSHIP_STATE_CONFLICT` | Membership is not ACTIVE |
| `ETAG_MISMATCH` | Supplied version is stale |

Unknown and other-tenant membership IDs return 404 without disclosing global actor
identity. Keep the directory's strict property allowlist; do not expose credentials,
session material, usernames, contact values, other memberships or person-scope IDs.

An exact retry returns the original command result without another status transition,
version increment or success audit event. Changed body or changed `If-Match` under the
same key is not the same command. Add narrowly opt-in header fingerprinting to the
idempotency middleware for this route if needed; do not silently change replay semantics
for every existing command. Never invent a new key or refresh the ETag automatically
after an uncertain result.

### 2.4 Audit and session effect

Write one ADMIN success event in the same transaction as the membership change, with
the tenant membership target, acting actor, prior/new status and bounded reason code.
Audit failure rolls back the command. Keep sensitive identifiers and contact values out
of detail payloads and logs; use established denial auditing for refused access.

Membership authorization is resolved per request. Test that a session already open
before suspension loses access to this tenant on its next protected request and can
still use another valid tenant. Do not claim cancellation of commands already accepted
by the server or deletion of all global sessions. Client capability projections are
display aids; fresh server authorization remains decisive.

## 3. UI and mock behavior

Extend the existing admin detail, query hooks, typed client and shared dialog components.
The action is available only with `canManageTenantUsers === true`, a loaded version and
an ACTIVE membership. Confirmation names the person and current tenant, explains the
tenant-specific effect and requires a reason. Use formal Turkish and the existing
DESIGN.md confirmation/retry patterns; no global-account controls or placeholder links.

Freeze the submitted tenant, membership, body, ETag, idempotency key and authenticated
context on first confirmation. Keep that snapshot across network uncertainty, step-up
and dialog close/reopen; freeze editing and offer an identical retry. A definitive
conflict/refusal requires explicit reload before a new command. Show confirmed server
success even if a subsequent read fails.

Include the management capability in both directory query context and component remount
keys. Actor, session, tenant or read/manage-capability changes discard forms and pending
actions. Before every deferred dispatch, compare the captured context with the live
store; after every response, check again before cache writes, invalidation or feedback.
Unmount/cancel alone is insufficient because `useStepUp.confirm` can already have
captured its pending action. Guard late shared-auth step-up results as well: they must
not overwrite a newer actor/tenant session. A changed step-up expiry is not itself an
identity change and must not invalidate a legitimate retry.

Mocks must persist membership status/version per tenant rather than setting every GET
to ACTIVE or mutating a global account. List, detail, context resolution and command
authorization must agree. Model replay, stale versions, step-up and domain refusals.

## 4. Implementation paths and order

1. OpenAPI and generated Go/TypeScript contracts; correlated management capability.
2. `db/queries/iam.sql`, identity application/repository/HTTP code, API wiring and the
   narrowly scoped idempotency option. No migration or new grant is assigned.
3. `web/packages/api-client/src/admin.ts`, admin detail/query/access components and
   translations; bounded shared-auth stale-completion guard.
4. Mock handlers/data and focused tests; integrate generated contracts before typecheck.
5. Synthetic desktop/mobile visual review; record verified evidence separately and
   update product/design/current checkpoint documentation to match actual results.

Use `docs/delegation/REPORT_TEMPLATE.md` for the implementation report. The Impeccable
skill is unavailable in this session; apply the documented PRODUCT.md/DESIGN.md
constraints and report that limitation rather than claiming skill execution.

## 5. Tests required

- Isolated PostgreSQL: shared global actor in two tenants; suspend A while B's account,
  credentials, grants and access remain unchanged; audit rollback and single transition.
- Authorization: missing/scoped-only permission, forged/omitted app header, expired
  grants, inactive/expired memberships, foreign target, stale step-up and session checks.
- Self and last-manager refusals, qualifying service manager, and concurrent suspensions
  preserving a manager; next-request denial for an already-open target session.
- HTTP/idempotency: exact replay, changed body, changed `If-Match`, stale ETag, invalid
  reason/state and strict response allowlist. Existing default middleware behavior stays
  covered when the new option is disabled.
- UI/mock: explicit tenant confirmation, reason selection, double-submit prevention,
  frozen uncertain retry including reopen, step-up replay, definitive-conflict reload,
  and delayed responses after actor/tenant/manage-capability changes.
- Shared auth: a late step-up response cannot replace a newer authenticated context or
  execute a cancelled old-context command; normal same-context step-up still completes.
- Synthetic 390px and 1440px review of detail, confirmation, step-up, conflict and result;
  keyboard operation, readable Turkish and no horizontal overflow.
- Required generation, formatting, lint, typecheck/build and focused Go/frontend tests;
  execute database tests against isolated test data and report any skips honestly.

No existing live member is suspended to validate this package. Do not create live grants,
change servers, or rerun accepted financial journeys. Local service reload/real read
verification remains an operator action and separate evidence, not an implied result.

## 6. Acceptance criteria

- [ ] An authorized manager can suspend an ACTIVE membership through the real contract.
- [ ] Self/last-manager and tenant/scope boundaries hold under concurrent commands.
- [ ] Exact retries are safe; altered requests cannot reuse an accepted command key.
- [ ] One transactional audit event accompanies the status change.
- [ ] Other tenant access and global account/credential/session state remain unchanged.
- [ ] Deferred step-up and response handling cannot act in or restore an obsolete context.
- [ ] Focused isolated tests and synthetic visual review pass with evidence recorded.
- [ ] No role/grant/schema change or existing live-membership mutation is used as proof.
