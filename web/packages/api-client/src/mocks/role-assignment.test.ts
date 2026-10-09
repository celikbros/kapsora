import { afterAll, afterEach, beforeAll, expect, it } from 'vitest';
import { createMockServer } from './node';
import type { MockAccount } from './data';

const { api, server } = createMockServer();
const BASE = 'http://mock.test';
type ResponseBody = {
  [key: string]: unknown;
  items: Array<{ id: string; code: string }>;
  grant: {
    id: string;
    roleCode: string;
    organizationRelationshipId: string | null;
    validTo: string | null;
  };
  code: string;
  canAssign: boolean;
  membershipRowVersion: number;
};
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

function setup() {
  const tenant = api.tenantByCode('DEMO_A')!;
  const target: MockAccount = {
    actorId: crypto.randomUUID(),
    username: 'invited.synthetic',
    displayName: 'Örnek Davetli',
    email: '',
    memberships: [{ tenantCode: tenant.code, permissions: [], membershipOnly: true }],
  };
  api.world.accounts.push(target);
  const manager = api.signIn('admin.a', 'backoffice')!;
  manager.stepUpExpiresAt = new Date(Date.now() + 60_000).toISOString();
  return { tenant, target, membership: api.tenantMembership(target, tenant.id), manager };
}

async function call(
  path: string,
  tenantId: string,
  command?: { body: unknown; etag: string; key: string },
) {
  const response = await fetch(BASE + path, {
    method: command ? 'POST' : 'GET',
    headers: {
      'X-Kapsora-App': 'backoffice',
      'X-Tenant-ID': tenantId,
      ...(command
        ? {
            'Content-Type': 'application/json',
            'X-CSRF-Token': api.session!.csrfToken,
            'If-Match': command.etag,
            'Idempotency-Key': command.key,
          }
        : {}),
    },
    ...(command ? { body: JSON.stringify(command.body) } : {}),
  });
  return {
    status: response.status,
    etag: response.headers.get('ETag'),
    body: (await response.json()) as ResponseBody,
  };
}

it('moves a zero-grant invitee into a real app, then ends only its grant and preserves the membership', async () => {
  const { tenant, target, membership } = setup();
  const path = `/api/v1/admin/users/${membership.id}/role-grants`;
  const options = await call('/api/v1/admin/role-assignment-options', tenant.id);
  expect(options.body.items.some((option: { code: string }) => option.code === 'RULE_AUTHOR')).toBe(
    true,
  );
  expect(
    options.body.items.some((option: { code: string }) => option.code === 'TENANT_ADMIN'),
  ).toBe(false);
  const before = await call(path, tenant.id);
  expect(before.body).toMatchObject({ canAssign: true, membershipRowVersion: 1, items: [] });
  const command = {
    body: { roleCode: 'RULE_AUTHOR', scopeType: 'TENANT', reasonCode: 'ONBOARDING' },
    etag: before.etag!,
    key: crypto.randomUUID(),
  };
  const added = await call(path, tenant.id, command);
  expect(added.status).toBe(200);
  expect(added.etag).toBe('"2"');
  expect(added.body.grant.roleCode).toBe('RULE_AUTHOR');
  expect((await call(path, tenant.id, command)).body).toEqual(added.body);
  expect(api.roleGrantEvents).toHaveLength(1);
  expect((await call(path, tenant.id)).body.canAssign).toBe(false);
  api.signIn(target.username, 'backoffice');
  const assignedContext = api.tenantContexts(target, 'backoffice')[0]!;
  expect(assignedContext.apps).toContain('backoffice');
  expect(assignedContext.permissions).toContain('rule.read');
  api.signIn('admin.a', 'backoffice')!.stepUpExpiresAt = new Date(
    Date.now() + 60_000,
  ).toISOString();
  const ended = await call(`${path}/${added.body.grant.id}/revoke`, tenant.id, {
    body: { reasonCode: 'DUTY_ENDED' },
    etag: added.etag!,
    key: crypto.randomUUID(),
  });
  expect(ended.status).toBe(200);
  expect(ended.body.grant.validTo).not.toBeNull();
  expect(api.roleGrantEvents).toHaveLength(2);
  expect(membership.rowVersion).toBe(3);
  expect(membership.status).toBe('ACTIVE');
  api.signIn(target.username, 'backoffice');
  expect(api.tenantContexts(target, 'backoffice')[0]!.apps).toEqual([]);
  expect(api.tenantContexts(target, 'backoffice')[0]!.permissions).toEqual([]);
  expect(target.memberships.filter((grant) => !grant.membershipOnly)).toHaveLength(1);
});

it('rejects zero-permission existing access, stale versions, scope shape, and unsupported role', async () => {
  const { tenant, target, membership } = setup();
  const path = `/api/v1/admin/users/${membership.id}/role-grants`;
  const base = { etag: '"1"', key: crypto.randomUUID() };
  expect(
    (
      await call(path, tenant.id, {
        ...base,
        body: { roleCode: 'TENANT_ADMIN', scopeType: 'TENANT', reasonCode: 'ONBOARDING' },
      })
    ).body.code,
  ).toBe('ROLE_ASSIGNMENT_UNSUPPORTED');
  expect(
    (
      await call(path, tenant.id, {
        ...base,
        body: {
          roleCode: 'RULE_AUTHOR',
          scopeType: 'TENANT',
          organizationRelationshipId: crypto.randomUUID(),
          reasonCode: 'ONBOARDING',
        },
      })
    ).status,
  ).toBe(400);
  target.memberships.push({
    tenantCode: tenant.code,
    permissions: [],
    scopes: [{ type: 'TENANT', id: null }],
  });
  expect((await call(path, tenant.id)).body.canAssign).toBe(false);
  expect(
    (
      await call(path, tenant.id, {
        ...base,
        body: { roleCode: 'RULE_AUTHOR', scopeType: 'TENANT', reasonCode: 'ONBOARDING' },
      })
    ).body.code,
  ).toBe('EXISTING_ACCESS_CONFLICT');
  target.memberships.pop();
  membership.rowVersion += 1;
  expect(
    (
      await call(path, tenant.id, {
        ...base,
        body: { roleCode: 'RULE_AUTHOR', scopeType: 'TENANT', reasonCode: 'ONBOARDING' },
      })
    ).status,
  ).toBe(412);
});

it('offers only current provider relationships and binds a provider grant to the selected relationship', async () => {
  const { tenant, target, membership } = setup();
  const path = `/api/v1/admin/users/${membership.id}/role-grants`;
  const picker = await call('/api/v1/admin/role-assignment-organizations', tenant.id);
  expect(picker.body.items.length).toBeGreaterThan(0);
  const chosen = picker.body.items[0]!.id;
  const rel = api.world.relationships.find((row) => row.id === chosen)!;
  const profile = api.world.providers.find((row) => row.tenantOrganizationId === chosen)!;
  const body = (organizationRelationshipId: string) => ({
    roleCode: 'PROVIDER_STAFF',
    scopeType: 'ORGANIZATION',
    organizationRelationshipId,
    reasonCode: 'DUTY_ASSIGNMENT',
  });
  expect(
    (
      await call(path, tenant.id, {
        body: body(rel.organizationId),
        etag: '"1"',
        key: 'provider-synthetic-001',
      })
    ).status,
  ).toBe(404);
  expect(
    (
      await call(path, tenant.id, {
        body: body(profile.id),
        etag: '"1"',
        key: 'provider-synthetic-002',
      })
    ).status,
  ).toBe(404);
  const added = await call(path, tenant.id, {
    body: body(chosen),
    etag: '"1"',
    key: 'provider-synthetic-003',
  });
  expect(added.status).toBe(200);
  expect(added.body.grant.organizationRelationshipId).toBe(chosen);
  api.signIn(target.username, 'provider');
  const context = api.tenantContexts(target, 'provider')[0]!;
  expect(context.apps).toContain('provider');
  expect(context.scopes).toContainEqual({ type: 'ORGANIZATION', id: chosen });
  expect(context.scopes).not.toContainEqual({ type: 'ORGANIZATION', id: rel.organizationId });
  const otherId = crypto.randomUUID();
  api.world.relationships.push({ ...rel, id: otherId, tenantCode: 'OTHER' });
  api.world.providers.push({ ...profile, id: crypto.randomUUID(), tenantOrganizationId: otherId });
  const source = api.world.serviceRequests.find((row) => row.tenantId === tenant.id)!;
  const ownRequestId = crypto.randomUUID();
  const otherRequestId = crypto.randomUUID();
  api.world.serviceRequests.push({ ...source, id: ownRequestId, providerOrganizationId: chosen });
  api.world.serviceRequests.push({
    ...source,
    id: otherRequestId,
    providerOrganizationId: otherId,
  });
  const headers = { 'X-Kapsora-App': 'provider', 'X-Tenant-ID': tenant.id };
  const list = await fetch(`${BASE}/api/v1/service-requests`, { headers });
  expect(list.status).toBe(200);
  const page = (await list.json()) as { items: Array<{ id: string }> };
  expect(page.items.map((row) => row.id)).toContain(ownRequestId);
  expect(page.items.map((row) => row.id)).not.toContain(otherRequestId);
  expect(
    (await fetch(`${BASE}/api/v1/service-requests/${otherRequestId}`, { headers })).status,
  ).toBe(404);
});

it('refuses assignment to a service actor even if its membership has no grant', async () => {
  const { tenant, target, membership } = setup();
  target.actorType = 'SERVICE_ACCOUNT';
  const path = `/api/v1/admin/users/${membership.id}/role-grants`;
  expect((await call(path, tenant.id)).body).toMatchObject({
    canAssign: false,
    assignmentRefusalCode: 'MEMBERSHIP_STATE_CONFLICT',
  });
  expect(
    (
      await call(path, tenant.id, {
        body: { roleCode: 'RULE_AUTHOR', scopeType: 'TENANT', reasonCode: 'ONBOARDING' },
        etag: '"1"',
        key: 'service-target-00001',
      })
    ).body.code,
  ).toBe('MEMBERSHIP_STATE_CONFLICT');
});

it('requires a strong version and JSON media type and hides foreign selectors', async () => {
  const { tenant, membership } = setup();
  const path = `${BASE}/api/v1/admin/users/${membership.id}/role-grants`;
  const headers = {
    'X-Kapsora-App': 'backoffice',
    'X-Tenant-ID': tenant.id,
    'X-CSRF-Token': api.session!.csrfToken,
    'Idempotency-Key': 'strong-version-00001',
    'Content-Type': 'application/json',
  };
  const body = JSON.stringify({
    roleCode: 'RULE_AUTHOR',
    scopeType: 'TENANT',
    reasonCode: 'ONBOARDING',
  });
  const weak = await fetch(path, {
    method: 'POST',
    headers: { ...headers, 'If-Match': 'W/"1"' },
    body,
  });
  expect(weak.status).toBe(428);
  const media = await fetch(path, {
    method: 'POST',
    headers: { ...headers, 'If-Match': '"1"', 'Content-Type': 'text/plain' },
    body,
  });
  expect(media.status).toBe(415);
  const foreign = await call(`/api/v1/admin/users/${crypto.randomUUID()}/role-grants`, tenant.id);
  expect(foreign.status).toBe(404);
  expect(foreign.body.code).toBe('RESOURCE_NOT_FOUND');
});
