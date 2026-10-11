import { afterAll, afterEach, beforeAll, expect, it } from 'vitest';
import { createMockServer } from './node';
import type { components } from '../generated/kapsora-v1';

type Schemas = components['schemas'];

const { api, server } = createMockServer();
const BASE = 'http://mock.test';
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

function fixture() {
  const manager = api.world.accounts.find((account) => account.username === 'admin.a')!;
  const target = api.world.accounts.find((account) => account.username === 'reviewer.a')!;
  const tenantA = api.tenantByCode('DEMO_A')!;
  const tenantB = api.tenantByCode('DEMO_B')!;
  const session = api.signIn(manager.username, 'backoffice')!;
  session.stepUpExpiresAt = new Date(Date.now() + 60_000).toISOString();
  return { manager, target, tenantA, tenantB, session };
}

async function request(
  path: string,
  tenantId: string,
  command?: { key?: string; etag?: string; reasonCode?: string },
) {
  const headers: Record<string, string> = {
    'X-Kapsora-App': 'backoffice',
    'X-Tenant-ID': tenantId,
  };
  if (command) {
    headers['Content-Type'] = 'application/json';
    headers['X-CSRF-Token'] = api.session!.csrfToken;
    headers['Idempotency-Key'] = command.key ?? 'mock-suspension-command-0001';
    headers['If-Match'] = command.etag ?? '"1"';
  }
  const response = await fetch(BASE + path, {
    method: command ? 'POST' : 'GET',
    headers,
    ...(command
      ? { body: JSON.stringify({ reasonCode: command.reasonCode ?? 'ACCESS_REVIEW' }) }
      : {}),
  });
  return {
    status: response.status,
    etag: response.headers.get('ETag'),
    body: (await response.json()) as Schemas['TenantUserDetail'] &
      Schemas['TenantUserPage'] &
      Schemas['Problem'],
  };
}

it('suspends only one tenant membership and replays once with the original version', async () => {
  const { manager, target, tenantA, tenantB } = fixture();
  target.memberships.push({ tenantCode: tenantB.code, permissions: ['identity.user.read'] });
  const originalGrants = structuredClone(target.memberships);
  const memberA = api.tenantMembership(target, tenantA.id);
  const memberB = api.tenantMembership(target, tenantB.id);
  const path = `/api/v1/admin/users/${memberA.id}/suspend`;
  const first = await request(path, tenantA.id, {});
  expect(first.status).toBe(200);
  expect(first.etag).toBe('"2"');
  expect(first.body.membership.membershipStatus).toBe('SUSPENDED');
  expect(first.body.membership.rowVersion).toBe(2);
  const replay = await request(path, tenantA.id, {});
  expect(replay).toEqual(first);
  expect(api.membershipSuspensionEvents).toHaveLength(1);
  expect(memberA.rowVersion).toBe(2);
  expect(memberB.status).toBe('ACTIVE');
  expect(target.memberships).toEqual(originalGrants);
  expect(target.actorStatus).toBeUndefined();
  const suspended = await request('/api/v1/admin/users?status=SUSPENDED', tenantA.id);
  expect(suspended.body.items.map((item: { id: string }) => item.id)).toEqual([memberA.id]);
  expect((await request(path, tenantA.id, { etag: '"2"' })).body.code).toBe(
    'IDEMPOTENCY_KEY_REUSED',
  );
  expect((await request(path, tenantA.id, { reasonCode: 'STAFF_DEPARTURE' })).body.code).toBe(
    'IDEMPOTENCY_KEY_REUSED',
  );
  manager.memberships[0]!.permissions = ['identity.user.read'];
  expect((await request(path, tenantA.id, {})).status).toBe(403);
  expect(api.membershipSuspensionEvents).toHaveLength(1);
  const targetSession = api.signIn(target.username, 'backoffice')!;
  expect(targetSession.activeTenantId).toBe(tenantB.id);
  expect(api.tenantContexts(target).map((context) => context.tenant.id)).toEqual([tenantB.id]);
  targetSession.activeTenantId = tenantA.id; // Model an already-open session's old tenant.
  expect((await request('/api/v1/admin/users', tenantA.id)).body.code).toBe('TENANT_ACCESS_DENIED');
  targetSession.activeTenantId = tenantB.id;
  expect((await request('/api/v1/admin/users', tenantB.id)).status).toBe(200);
});

it('refuses missing step-up, self, foreign membership, stale version and invalid reason', async () => {
  const { manager, target, tenantA, tenantB, session } = fixture();
  const member = api.tenantMembership(target, tenantA.id);
  const path = `/api/v1/admin/users/${member.id}/suspend`;
  session.stepUpExpiresAt = null;
  expect((await request(path, tenantA.id, {})).body.code).toBe('STEP_UP_REQUIRED');
  session.stepUpExpiresAt = new Date(Date.now() + 60_000).toISOString();
  expect((await request(path, tenantA.id, { etag: '"7"' })).status).toBe(412);
  expect((await request(path, tenantA.id, { reasonCode: 'FREE_TEXT' })).body.code).toBe(
    'SUSPENSION_REASON_INVALID',
  );
  const own = api.tenantMembership(manager, tenantA.id);
  expect((await request(`/api/v1/admin/users/${own.id}/suspend`, tenantA.id, {})).body.code).toBe(
    'SELF_SUSPENSION_FORBIDDEN',
  );
  const foreign = api.tenantMembership(
    api.world.accounts.find((account) => account.username === 'admin.b')!,
    tenantB.id,
  );
  expect((await request(`/api/v1/admin/users/${foreign.id}/suspend`, tenantA.id, {})).status).toBe(
    404,
  );
  expect(member.status).toBe('ACTIVE');
  expect(api.membershipSuspensionEvents).toHaveLength(0);
});

it('correlates management permission with the same active tenant-wide grant', async () => {
  const { manager, target, tenantA } = fixture();
  manager.memberships = [
    { tenantCode: tenantA.code, permissions: ['identity.user.read'] },
    {
      tenantCode: tenantA.code,
      permissions: ['identity.user.manage'],
      scopes: [{ type: 'ORGANIZATION', id: null }],
    },
  ];
  expect(api.tenantContexts(manager, 'backoffice')[0]!.canManageTenantUsers).toBe(false);
  const member = api.tenantMembership(target, tenantA.id);
  expect((await request(`/api/v1/admin/users/${member.id}/suspend`, tenantA.id, {})).status).toBe(
    403,
  );
  manager.memberships[1]!.scopes = [];
  manager.memberships[1]!.validTo = new Date(Date.now() - 1000).toISOString();
  expect(api.tenantContexts(manager, 'backoffice')[0]!.canManageTenantUsers).toBe(false);
  expect((await request(`/api/v1/admin/users/${member.id}/suspend`, tenantA.id, {})).status).toBe(
    403,
  );
  expect(api.membershipSuspensionEvents).toHaveLength(0);
});
