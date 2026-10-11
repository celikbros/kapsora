import { afterAll, afterEach, beforeAll, expect, it } from 'vitest';
import type { MockAccount } from './data';
import { createMockServer } from './node';

const { api, server } = createMockServer();
const base = 'http://mock.test';
type ResponseBody = {
  [key: string]: unknown;
  items: Array<{
    code: string;
    configurationHash: string;
    requiresApproval: boolean;
    permissionCodes: string[];
  }>;
  request: {
    id: string;
    status: string;
    permissionSnapshot: Array<{ code: string; sensitivity: string }>;
  };
  appliedGrant: { roleCode: string };
  code: string;
  canRequestAssignment: boolean;
  checkerAvailability: string;
  revokeGrantIds: string[];
  approvalRefusalCode: string | null;
};
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

function fixture(withChecker = true) {
  const tenant = api.tenantByCode('DEMO_A')!;
  const target: MockAccount = {
    actorId: crypto.randomUUID(),
    username: 'b.target',
    displayName: 'Örnek Hedef',
    email: '',
    memberships: [{ tenantCode: tenant.code, permissions: [], membershipOnly: true }],
  };
  api.world.accounts.push(target);
  if (!withChecker)
    api.world.accounts.splice(
      0,
      api.world.accounts.length,
      ...api.world.accounts.filter(
        (account) => account.username === 'admin.a' || account === target,
      ),
    );
  if (withChecker)
    api.world.accounts.push({
      actorId: crypto.randomUUID(),
      username: 'b.checker',
      displayName: 'Örnek Onaylayıcı',
      email: '',
      memberships: [
        {
          tenantCode: tenant.code,
          roleCode: 'MOCK_MANAGER',
          permissions: ['identity.user.read', 'identity.role.manage'],
          scopes: [{ type: 'TENANT', id: null }],
        },
      ],
    });
  const targetId = api.tenantMembership(target, tenant.id).id;
  api.signIn('admin.a', 'backoffice')!.stepUpExpiresAt = new Date(
    Date.now() + 60_000,
  ).toISOString();
  return { tenant, target, targetId };
}
async function call(
  tenantId: string,
  path: string,
  command?: { body: unknown; etag: string; key: string },
) {
  const response = await fetch(`${base}${path}`, {
    method: command ? 'POST' : 'GET',
    headers: {
      'X-Kapsora-App': 'backoffice',
      'X-Tenant-ID': tenantId,
      ...(command
        ? {
            'X-CSRF-Token': api.session!.csrfToken,
            'Content-Type': 'application/json',
            'If-Match': command.etag,
            'Idempotency-Key': command.key,
          }
        : {}),
    },
    ...(command ? { body: JSON.stringify(command.body) } : {}),
  });
  return {
    status: response.status,
    etag: response.headers.get('ETag')!,
    body: (await response.json()) as ResponseBody,
  };
}

it.each([
  'TENANT_ADMIN',
  'PLAN_PUBLISHER',
  'CONTRACT_PUBLISHER',
  'RULE_APPROVER',
  'PAYER_APPROVER',
])('keeps %s pending until another human approves and replays exact result', async (roleCode) => {
  const { tenant, target, targetId } = fixture();
  const options = await call(tenant.id, '/api/v1/admin/privileged-role-assignment-options');
  const option = options.body.items.find((item: { code: string }) => item.code === roleCode);
  if (!option) throw new Error(`Missing privileged-role fixture: ${roleCode}`);
  expect(option.requiresApproval).toBe(true);
  const before = await call(tenant.id, `/api/v1/admin/users/${targetId}/role-change-eligibility`);
  expect(before.body).toMatchObject({
    canRequestAssignment: true,
    checkerAvailability: 'AVAILABLE',
  });
  const created = await call(tenant.id, `/api/v1/admin/users/${targetId}/role-change-requests`, {
    body: {
      operation: 'ASSIGN',
      roleCode,
      configurationHash: option.configurationHash,
      reasonCode: 'ONBOARDING',
    },
    etag: before.etag,
    key: `create-${roleCode}`,
  });
  expect(created.status).toBe(201);
  expect(created.body.request.status).toBe('PENDING');
  expect(
    created.body.request.permissionSnapshot.some((item) => item.sensitivity === 'PRIVILEGED'),
  ).toBe(true);
  if (roleCode === 'TENANT_ADMIN')
    expect(created.body.request.permissionSnapshot).toContainEqual({
      code: 'identity.user.manage',
      sensitivity: 'SENSITIVE',
    });
  if (roleCode === 'PAYER_APPROVER')
    expect(created.body.request.permissionSnapshot).toContainEqual({
      code: 'accounting.reconcile',
      sensitivity: 'SENSITIVE',
    });
  expect(target.memberships.filter((grant) => !grant.membershipOnly)).toHaveLength(0);
  expect(api.tenantMembership(target, tenant.id).rowVersion).toBe(1);
  const path = `/api/v1/admin/role-change-requests/${created.body.request.id}`;
  const makerDetail = await call(tenant.id, path);
  expect(makerDetail.body).toMatchObject({ canApprove: false, canReject: false, canCancel: true });
  expect(
    (
      await call(tenant.id, `${path}/approve`, {
        body: {},
        etag: created.etag,
        key: `self-${roleCode}`,
      })
    ).body.code,
  ).toBe('MAKER_CHECKER_SAME_ACTOR');
  api.signIn('b.checker', 'backoffice')!.stepUpExpiresAt = new Date(
    Date.now() + 60_000,
  ).toISOString();
  expect((await call(tenant.id, path)).body).toMatchObject({
    canApprove: true,
    canReject: true,
    canCancel: false,
  });
  const command = { body: {}, etag: created.etag, key: `approve-${roleCode}` };
  const approved = await call(tenant.id, `${path}/approve`, command);
  expect(approved.status).toBe(200);
  expect(approved.body.request.status).toBe('APPROVED');
  expect(approved.body.appliedGrant.roleCode).toBe(roleCode);
  expect(api.tenantMembership(target, tenant.id).rowVersion).toBe(2);
  expect(target.memberships.filter((grant) => !grant.membershipOnly)).toHaveLength(1);
  expect(await call(tenant.id, `${path}/approve`, command)).toEqual(approved);
  expect(api.roleChangeEvents.map((event) => event.action)).toEqual(['create', 'approve']);
  api.signIn(target.username, 'backoffice');
  const context = api.tenantContexts(target, 'backoffice')[0]!;
  expect(context.apps).toContain('backoffice');
  if (roleCode === 'TENANT_ADMIN')
    expect(context.permissions).not.toContain('health.clinical.read');
});

it('allows a pending request without another checker, then safely rejects it without changing access', async () => {
  const { tenant, target, targetId } = fixture(false);
  const option = (await call(tenant.id, '/api/v1/admin/privileged-role-assignment-options')).body
    .items[0];
  if (!option) throw new Error('Missing privileged-role fixture');
  const eligibility = await call(
    tenant.id,
    `/api/v1/admin/users/${targetId}/role-change-eligibility`,
  );
  expect(eligibility.body.checkerAvailability).toBe('NO_ELIGIBLE_CHECKER');
  const created = await call(tenant.id, `/api/v1/admin/users/${targetId}/role-change-requests`, {
    body: {
      operation: 'ASSIGN',
      roleCode: option.code,
      configurationHash: option.configurationHash,
      reasonCode: 'ONBOARDING',
    },
    etag: eligibility.etag,
    key: 'create-no-checker',
  });
  expect(created.status).toBe(201);
  api.world.accounts.push({
    actorId: crypto.randomUUID(),
    username: 'b.checker',
    displayName: 'Örnek Onaylayıcı',
    email: '',
    memberships: [
      {
        tenantCode: tenant.code,
        permissions: ['identity.user.read', 'identity.role.manage'],
        scopes: [{ type: 'TENANT', id: null }],
      },
    ],
  });
  api.signIn('b.checker', 'backoffice')!.stepUpExpiresAt = new Date(
    Date.now() + 60_000,
  ).toISOString();
  const rejected = await call(
    tenant.id,
    `/api/v1/admin/role-change-requests/${created.body.request.id}/reject`,
    { body: { reasonCode: 'NOT_JUSTIFIED' }, etag: created.etag, key: 'reject-no-checker' },
  );
  expect(rejected.body.request.status).toBe('REJECTED');
  expect(target.memberships.filter((grant) => !grant.membershipOnly)).toHaveLength(0);
  expect(api.tenantMembership(target, tenant.id).rowVersion).toBe(1);
});

it('keeps privileged access until a second human approves revocation, then preserves grant history', async () => {
  const { tenant, target, targetId } = fixture();
  const option = (
    await call(tenant.id, '/api/v1/admin/privileged-role-assignment-options')
  ).body.items.find((item) => item.code === 'RULE_APPROVER')!;
  const grantId = crypto.randomUUID();
  const originalFrom = '2026-01-01T00:00:00Z';
  target.memberships.push({
    tenantCode: tenant.code,
    grantId,
    roleCode: option.code,
    isSystemRole: true,
    permissions: [...option.permissionCodes],
    scopes: [{ type: 'TENANT', id: null }],
    validFrom: originalFrom,
    validTo: null,
  });
  const eligibility = await call(
    tenant.id,
    `/api/v1/admin/users/${targetId}/role-change-eligibility`,
  );
  expect(eligibility.body.revokeGrantIds).toEqual([grantId]);
  const created = await call(tenant.id, `/api/v1/admin/users/${targetId}/role-change-requests`, {
    body: {
      operation: 'REVOKE',
      grantId,
      configurationHash: option.configurationHash,
      reasonCode: 'DUTY_ENDED',
    },
    etag: eligibility.etag,
    key: 'revoke-create-00001',
  });
  expect(created.status).toBe(201);
  expect(target.memberships.at(-1)?.validTo).toBeNull();
  expect(api.tenantMembership(target, tenant.id).rowVersion).toBe(1);
  api.signIn('b.checker', 'backoffice')!.stepUpExpiresAt = new Date(
    Date.now() + 60_000,
  ).toISOString();
  const decided = await call(
    tenant.id,
    `/api/v1/admin/role-change-requests/${created.body.request.id}/approve`,
    { body: {}, etag: created.etag, key: 'revoke-approve-00001' },
  );
  expect(decided.body.request.status).toBe('APPROVED');
  expect(decided.body.appliedGrant.roleCode).toBe('RULE_APPROVER');
  expect(target.memberships.at(-1)?.grantId).toBe(grantId);
  expect(target.memberships.at(-1)?.validFrom).toBe(originalFrom);
  expect(target.memberships.at(-1)?.validTo).not.toBeNull();
  expect(api.tenantMembership(target, tenant.id).rowVersion).toBe(2);
  api.signIn(target.username, 'backoffice');
  expect(api.tenantContexts(target, 'backoffice')[0]!.apps).toEqual([]);
});

it('leaves a stale proposal pending and lets an authorized checker reject it', async () => {
  const { tenant, target, targetId } = fixture();
  const option = (await call(tenant.id, '/api/v1/admin/privileged-role-assignment-options')).body
    .items[0]!;
  const eligibility = await call(
    tenant.id,
    `/api/v1/admin/users/${targetId}/role-change-eligibility`,
  );
  const created = await call(tenant.id, `/api/v1/admin/users/${targetId}/role-change-requests`, {
    body: {
      operation: 'ASSIGN',
      roleCode: option.code,
      configurationHash: option.configurationHash,
      reasonCode: 'ONBOARDING',
    },
    etag: eligibility.etag,
    key: 'stale-create-00001',
  });
  api.tenantMembership(target, tenant.id).rowVersion += 1;
  api.signIn('b.checker', 'backoffice')!.stepUpExpiresAt = new Date(
    Date.now() + 60_000,
  ).toISOString();
  const path = `/api/v1/admin/role-change-requests/${created.body.request.id}`;
  expect((await call(tenant.id, path)).body).toMatchObject({
    canApprove: false,
    canReject: true,
    approvalRefusalCode: 'ROLE_CHANGE_TARGET_CHANGED',
  });
  expect(
    (
      await call(tenant.id, `${path}/approve`, {
        body: {},
        etag: created.etag,
        key: 'stale-approve-00001',
      })
    ).body.code,
  ).toBe('ROLE_CHANGE_TARGET_CHANGED');
  expect((await call(tenant.id, path)).body.request.status).toBe('PENDING');
  const rejected = await call(tenant.id, `${path}/reject`, {
    body: { reasonCode: 'STALE_REQUEST' },
    etag: created.etag,
    key: 'stale-reject-00001',
  });
  expect(rejected.body.request.status).toBe('REJECTED');
  expect(target.memberships.filter((grant) => !grant.membershipOnly)).toHaveLength(0);
});
