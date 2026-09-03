# WP-I1-02 · Authorization: tenant context, permissions, /me, /tenants, tenant switch, role templates, seed

> **Delivered in-house on 2026-09-03.** Built as specified with two adjustments that follow
> from ADR-022: the demo users are local accounts (`seed demo`, optional
> `KAPSORA_SEED_DEMO_PASSWORD`) rather than Keycloak ids, and the platform-admin subject list
> (section 4.3) was not added because provisioning is reachable only through `cmd/seed`,
> which already refuses production-like environments. The stretch item (bearer-token service
> accounts) was not done.

| Field | Value |
|---|---|
| Milestone | M1 (plan increment I1) |
| Size | L |
| Depends on | Ports in `main`: `internal/identity`, `internal/audit`, `internal/platform/db.WithActorTx`, migration 000009 |
| Runs in parallel with | WP-I1-01 (use a fake `SessionStore`), 03, 04, 05, 06 |
| Migration numbers assigned | none expected; roles are data seeded by code. Ask if you believe one is needed |
| OpenAPI operations owned | `getCurrentUserContext`, `listAccessibleTenants`, `switchTenant` |
| Read first | Handbook; v1.2 sections 6.2-6.4, 14.4, 16.4, 18.2; ADR-004; migrations 000002, 000008, 000009 |

## 1. Goal

Turn a loaded session into an authorized `identity.RequestContext`: the actor's active
tenant is validated against a live membership, permissions are resolved from tenant-owned
roles and access grants, and every tenant-scoped handler can rely on
`identity.Require(ctx, permission)`. Provide the tenant listing and switching endpoints and
the tenant provisioning command that seeds roles from templates.

## 2. Scope

1. `RequestContextMiddleware`: builds `identity.RequestContext` from the session
   (`identity.SessionFromContext`), the `X-Tenant-ID` header and the database.
2. Permission resolution: union of `iam.role_permission` over active `iam.access_grant`
   rows of the membership, valid now; scopes collected from grants with `scope_type <>
   'TENANT'`. Cache per request only (no cross-request cache in this WP).
3. Endpoints `GET /api/v1/me`, `GET /api/v1/tenants`, `POST /api/v1/session/switch-tenant`.
4. Role templates and tenant provisioning command (`platform.tenant.provision`).
5. `cmd/seed`: creates demo tenants and memberships for the Keycloak demo users.
6. Audit: `session.tenant_switch` (AUTHENTICATION), `tenant.provision` (ADMIN),
   `authorization.denied` (SECURITY, outcome DENIED) whenever `Require` fails on a route.
7. Stretch (do only if everything else is done): bearer-token service accounts
   (`Authorization: Bearer <JWT>` from Keycloak client credentials) mapped to
   `iam.actor` of type SERVICE_ACCOUNT by issuer+subject with `ClientType = SERVICE`.

Out of scope: login/session storage (WP-I1-01), UI (WP-I1-05), access review campaigns,
break-glass, JIT privileged access (later milestones).

## 3. Packages and layout

```text
internal/identity/
  application/authorizer.go        ResolveTenantContext(ctx, session, tenantID) (RequestContext, error)
  application/provisioning.go      ProvisionTenant(cmd) — tenant + baseline catalogs + system roles
  application/roles.go             role template table (see section 5)
  infrastructure/postgres/         sqlc-backed repositories for memberships, roles, grants
  transport/http/context_middleware.go   RequestContextMiddleware, RequireTenantHeader
  transport/http/me_handler.go     getCurrentUserContext, listAccessibleTenants, switchTenant
cmd/seed/main.go                   demo tenants DEMO_A / DEMO_B
db/queries/iam.sql
```

## 4. Behaviour

### 4.1 Request context resolution

1. No session in context → the request stays unauthenticated; handlers that call
   `identity.Require` receive `ErrUnauthenticated` → `401 UNAUTHENTICATED`.
2. Session present, route is tenant-scoped (everything under `/api/v1/` except `/session*`,
   `/me`, `/tenants`): header `X-Tenant-ID` is mandatory (`400 TENANT_HEADER_REQUIRED`),
   must be a UUID, and must equal `session.ActiveTenantID` (`403 TENANT_MISMATCH`). Then,
   inside `db.WithActorTx`, load the membership for (actor, tenant): must be ACTIVE and
   valid today (`403 TENANT_ACCESS_DENIED`; do not reveal whether the tenant exists).
3. Inside `db.WithTenantTx`, load permissions and scopes for the membership; build
   `RequestContext` with `RequestID` (from `httpx.RequestIDFrom`), `Locale`/`TimeZone`
   from tenant defaults, `StepUpValid = session.StepUpUntil > now`,
   `ClientType = BROWSER`. Attach with `identity.WithRequestContext`.
4. Map `identity.ErrPermissionDenied` → `403 PERMISSION_DENIED`, `ErrStepUpRequired` →
   `403 STEP_UP_REQUIRED` in a shared helper `identityhttp.WriteAuthError(w, r, err)` that
   other WPs will reuse.

### 4.2 Endpoints

- `GET /api/v1/me` → `UserContext`: actor, display name, email, and one `TenantContext`
  per active membership (tenant summary + permissions + scopes). Uses `db.WithActorTx`
  to list memberships, then `db.WithTenantTx` per tenant for permissions.
- `GET /api/v1/tenants` → `TenantSummary` list of active memberships in ACTIVE tenants,
  ordered by display name (`sqlcgen.ListTenantsForActor` exists).
- `POST /api/v1/session/switch-tenant` `{tenantId}` → verifies membership as in 4.1,
  calls `SessionStore.SetActiveTenant`, audits, returns the `TenantContext`. Requires CSRF
  (middleware from WP-I1-01; in your tests call the handler directly).

### 4.3 Provisioning

`ProvisionTenant(code, legalName, displayName, locale, tz, currency)` in one transaction:

1. Insert `platform.tenant` (status PROVISIONING → ACTIVE at the end).
2. Seed baseline catalogs: identifier types `TCKN` (sensitive, TENANT), `PASSPORT`
   (sensitive, TENANT), `MEMBER_NO` (not sensitive, SPONSOR), `CUSTOMER_NO` (SPONSOR);
   relationship types `SPOUSE`, `CHILD`, `PARENT`, `DEPENDENT`, `GUARDIAN`, `DELEGATE`;
   membership types `EMPLOYEE`, `RETIREE`, `MEMBER`, `CUSTOMER`, `INSURED`, `STUDENT`,
   `BENEFICIARY`, `FAMILY` (requires_principal true); program types `EMPLOYEE_BENEFIT`,
   `MEMBER_PROGRAM`, `SOCIAL_SUPPORT`, `CUSTOMER_PRIVILEGE`, `INSURANCE_ASSISTANCE`,
   `STUDENT_SUPPORT`.
3. Create system roles from the template table (`is_system_role = true`) with their
   permissions.
4. Audit `tenant.provision`.

Provisioning is a platform-level action. Until platform admin roles exist, guard it with
config `KAPSORA_PLATFORM_ADMIN_SUBJECTS` (comma-separated issuer subjects) and expose it
only through `cmd/seed` and an internal application call; no public endpoint in this WP.

## 5. Role templates

Permission codes are those in migration 000008. `scope` tells whether the role is normally
granted with an ORGANIZATION scope (provider-side roles).

| Role code | Scope | Permissions |
|---|---|---|
| TENANT_ADMIN | TENANT | identity.user.read, identity.user.manage, identity.role.manage, identity.access_review, organization.read, organization.manage, program.read, catalog.read, provider.read, contract.read, rule.read, report.read, audit.read, notification.manage, integration.manage |
| PROGRAM_MANAGER | TENANT | organization.read, member.read, member.manage, member.relationship.manage, membership.manage, enrollment.manage, eligibility.check, program.read, program.manage, plan.manage, entitlement.read, catalog.read, catalog.manage, report.read |
| PLAN_PUBLISHER | TENANT | program.read, plan.publish, entitlement.read, entitlement.adjust |
| CONTRACT_MANAGER | TENANT | organization.read, provider.read, provider.manage, provider.practitioner.manage, contract.read, contract.manage, catalog.read |
| CONTRACT_PUBLISHER | TENANT | contract.read, contract.publish |
| RULE_AUTHOR | TENANT | rule.read, rule.draft, catalog.read, program.read |
| RULE_APPROVER | TENANT | rule.read, rule.publish |
| MEDICAL_REVIEWER | TENANT | member.read, service_request.read, service_request.review, health.case.read, health.clinical.read, health.medical_report.review, claim.read, claim.medical.review, document.read |
| FINANCIAL_REVIEWER | TENANT | member.read, service_request.read, claim.read, claim.financial.review, invoice.read, invoice.manage, batch.review, settlement.read, fiscal.edocument.read, fiscal.edocument.match, accounting.posting.read, document.read, report.read |
| PAYER_APPROVER | TENANT | settlement.read, settlement.approve, fiscal.response.send, accounting.posting.send, accounting.reconcile, report.read |
| AUDITOR | TENANT | report.read, audit.read, security.audit.read, entitlement.read |
| PROVIDER_ADMIN | ORGANIZATION | identity.user.read, identity.user.manage, provider.read, provider.manage, provider.practitioner.manage |
| PROVIDER_STAFF | ORGANIZATION | member.read, eligibility.check, service_request.read, service_request.create, service_request.submit, service_request.cancel, health.case.read, health.case.manage, health.clinical.read, health.medical_report.manage, document.upload, document.read |
| PROVIDER_BILLING | ORGANIZATION | claim.read, claim.create, claim.submit, invoice.read, invoice.manage, batch.create, batch.submit, settlement.read, fiscal.edocument.read, document.read |
| PROVIDER_RESERVATION | ORGANIZATION | accommodation.inventory.manage, accommodation.booking.manage, member.read, eligibility.check |
| MEMBER | TENANT | eligibility.check, service_request.read, service_request.create, service_request.submit, service_request.cancel, accommodation.booking.create, document.upload, document.read, entitlement.read |

Keep the table in Go (`application/roles.go`) as the single source; write a test that every
permission code in the table exists in `iam.permission`.

## 6. Seed (`cmd/seed`)

Idempotent; safe to re-run. Creates tenants `DEMO_A` ("Demo Kurum A") and `DEMO_B` via
`ProvisionTenant`, actors for the fixed Keycloak demo user ids from WP-I1-01 (issuer =
`KAPSORA_OIDC_ISSUER_URL`), memberships and grants:

| Actor | DEMO_A | DEMO_B |
|---|---|---|
| admin.a | TENANT_ADMIN, PROGRAM_MANAGER | - |
| reviewer.a | FINANCIAL_REVIEWER | - |
| provider.a | PROVIDER_STAFF scoped to organization "Demo Hastane" (create it as PROVIDER) | - |
| admin.b | - | TENANT_ADMIN |
| both.ab | AUDITOR | AUDITOR |

Uses the owner connection (`KAPSORA_MIGRATE_DATABASE_URL`); refuses to run when
`Config.IsProductionLike()`.

## 7. Tests required

- `dbtest`: permission resolution for a membership with two roles and an expired grant;
  tenant mismatch; suspended membership; `ListTenantsForActor` sees only own memberships
  (relies on migration 000009).
- Handler tests with a fake `SessionStore` and `httptest`: 400/403/401 paths, `/me`
  shape against the generated types, `switch-tenant` happy path and denial.
- Provisioning test: roles and catalogs created; re-provisioning the same code fails with
  `409 TENANT_CODE_EXISTS`.
- Role template test (section 5).

## 8. Acceptance criteria

- [ ] With WP-I1-01 merged (integrator will combine), a logged-in demo user lists exactly
      the tenants of their memberships and cannot call a tenant-scoped route with another
      tenant's id.
- [ ] `identity.Require` denials are audited with outcome DENIED and return
      `403 PERMISSION_DENIED` without leaking resource existence.
- [ ] `cmd/seed` runs twice without error and leaves one set of demo data.
- [ ] Tests in section 7 pass; `make ci` and `make test-db` green.
- [ ] OpenAPI unchanged except descriptions for the three owned operations (Spectral warns
      about missing descriptions today; add them).
