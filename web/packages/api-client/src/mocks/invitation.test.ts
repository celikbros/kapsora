import { beforeAll, afterAll, afterEach, expect, it } from 'vitest';
import { createMockServer } from './node';
import type { components } from '../generated/kapsora-v1';

type Schemas = components['schemas'];
const { api, server } = createMockServer();
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

function manager() {
  const session = api.signIn('admin.a', 'backoffice')!;
  session.stepUpExpiresAt = new Date(Date.now() + 60_000).toISOString();
  return api.tenantByCode('DEMO_A')!;
}

async function request(
  path: string,
  options: { body?: unknown; empty?: boolean; key?: string; tenant?: string; etag?: string } = {},
) {
  const response = await fetch(`http://mock.test${path}`, {
    method: options.body === undefined && !options.empty ? 'GET' : 'POST',
    headers: {
      'X-Kapsora-App': 'backoffice',
      ...(api.session ? { 'X-CSRF-Token': api.session.csrfToken } : {}),
      ...(options.tenant ? { 'X-Tenant-ID': options.tenant } : {}),
      ...(options.body === undefined && !options.empty
        ? {}
        : {
            'Content-Type': 'application/json',
            'Idempotency-Key': options.key ?? 'invitation-command-0001',
          }),
      ...(options.etag ? { 'If-Match': options.etag } : {}),
    },
    ...(options.body === undefined ? {} : { body: JSON.stringify(options.body) }),
  });
  return {
    status: response.status,
    etag: response.headers.get('ETag'),
    body: (await response.json()) as Schemas['TenantInvitation'] &
      Schemas['TenantInvitationPage'] &
      Schemas['AcceptExistingInvitationResponse'] &
      Schemas['TenantUserDetail'] &
      Omit<Schemas['Problem'], 'status'>,
  };
}

async function invite(email = 'recipient@example.test', key = 'invitation-command-0001') {
  const tenant = manager();
  const created = await request('/api/v1/admin/invitations', {
    tenant: tenant.id,
    body: { email },
    key,
  });
  expect(created.status).toBe(200);
  const code = api.invitations.takeDeliveredCode(created.body.invitationId)!;
  expect(typeof code).toBe('string');
  return { tenant, created, code };
}

it('keeps contact/code private, binds keyed create replay and serializes pending contact', async () => {
  const { tenant, created } = await invite();
  expect(Object.keys(created.body).sort()).toEqual([
    'createdAt',
    'deliveryStatus',
    'expiresAt',
    'invitationId',
    'maskedRecipient',
    'rowVersion',
    'status',
  ]);
  const replay = await request('/api/v1/admin/invitations', {
    tenant: tenant.id,
    body: { email: 'recipient@EXAMPLE.TEST' },
  });
  expect(replay).toEqual(created);
  expect(
    (
      await request('/api/v1/admin/invitations', {
        tenant: tenant.id,
        body: { email: 'changed@example.test' },
      })
    ).body.code,
  ).toBe('IDEMPOTENCY_KEY_REUSED');
  expect(
    (
      await request('/api/v1/admin/invitations', {
        tenant: tenant.id,
        body: { email: 'recipient@example.test' },
        key: 'other-invitation-0001',
      })
    ).body.code,
  ).toBe('INVITATION_PENDING_EXISTS');
  const privateMetadata = JSON.stringify([
    ...api.invitations.rows.values(),
    ...api.invitations.createReceipts.values(),
  ]);
  expect(privateMetadata.includes('recipient@example.test')).toBe(false);
  expect(privateMetadata.includes('v1.')).toBe(false);
  expect(api.invitations.events).toHaveLength(1);
  api.session!.account.memberships[0]!.permissions = ['identity.user.read'];
  expect(
    (
      await request('/api/v1/admin/invitations', {
        tenant: tenant.id,
        body: { email: 'recipient@example.test' },
      })
    ).status,
  ).toBe(403);

  api.reset();
  const firstTenant = manager();
  const concurrent = await Promise.all(
    ['concurrent-invite-0001', 'concurrent-invite-0002'].map((key) =>
      request('/api/v1/admin/invitations', {
        tenant: firstTenant.id,
        body: { email: 'parallel@example.test' },
        key,
      }),
    ),
  );
  expect(concurrent.map((result) => result.status).sort()).toEqual([200, 409]);
  expect(api.invitations.rows.size).toBe(1);
});

it('joins only the authenticated actor with zero grants, preserves tenant B and replays only the same actor/key', async () => {
  const { tenant, created, code } = await invite();
  const account = api.world.accounts.find((item) => item.username === 'admin.b')!;
  const original = structuredClone(account.memberships);
  const session = api.signIn(account.username, 'backoffice')!;
  const before = {
    tenant: session.activeTenantId,
    csrf: session.csrfToken,
    expiresAt: session.expiresAt,
  };
  const inspected = await request('/api/v1/invitations/inspect', { body: { code } });
  expect(inspected.status).toBe(200);
  const outcome = await request('/api/v1/invitations/accept-existing', {
    body: { code, confirmed: true },
    key: 'recipient-accept-0001',
  });
  expect(outcome.status).toBe(200);
  expect(outcome.body.accessPending).toBe(true);
  expect(account.memberships.filter((grant) => grant.tenantCode !== tenant.code)).toEqual(original);
  expect(
    account.memberships.find((grant) => grant.tenantCode === tenant.code)?.permissions,
  ).toEqual([]);
  expect({
    tenant: session.activeTenantId,
    csrf: session.csrfToken,
    expiresAt: session.expiresAt,
  }).toEqual(before);
  expect(
    await request('/api/v1/invitations/accept-existing', {
      body: { code, confirmed: true },
      key: 'recipient-accept-0001',
    }),
  ).toEqual(outcome);
  expect(
    (
      await request('/api/v1/invitations/accept-existing', {
        body: { code, confirmed: true },
        key: 'recipient-accept-0002',
      })
    ).status,
  ).toBe(409);
  expect(api.invitations.events.filter((event) => event.action === 'accept')).toHaveLength(1);
  manager();
  expect(
    (
      await request('/api/v1/invitations/accept-existing', {
        body: { code, confirmed: true },
        key: 'recipient-accept-0001',
      })
    ).body.code,
  ).toBe('INVITATION_UNAVAILABLE');
  const detail = await request(`/api/v1/admin/users/${outcome.body.membershipId}`, {
    tenant: tenant.id,
  });
  expect(detail.body.assignedRoles).toEqual([]);
  expect(
    (await request(`/api/v1/admin/invitations/${created.body.invitationId}`, { tenant: tenant.id }))
      .body.status,
  ).toBe('ACCEPTED');
});

it('refuses stale cancel and terminal proofs, permits an expired replacement and preserves suspended membership', async () => {
  const { tenant, created, code } = await invite();
  const path = `/api/v1/admin/invitations/${created.body.invitationId}/cancel`;
  expect((await request(path, { tenant: tenant.id, empty: true, etag: '"9"' })).status).toBe(412);
  const cancelled = await request(path, { tenant: tenant.id, empty: true, etag: '"1"' });
  expect(cancelled.body.status).toBe('CANCELLED');
  expect(cancelled.body.deliveryStatus).toBe('SENT');
  expect(await request(path, { tenant: tenant.id, empty: true, etag: '"1"' })).toEqual(cancelled);
  api.signIn('admin.b', 'backoffice');
  expect((await request('/api/v1/invitations/inspect', { body: { code } })).body.code).toBe(
    'INVITATION_UNAVAILABLE',
  );
  const fresh = await invite('recipient@example.test', 'replacement-invite-0001');
  const stored = api.invitations.rows.get(fresh.created.body.invitationId)!;
  stored.summary.expiresAt = new Date(Date.now() - 1).toISOString();
  api.signIn('admin.b', 'backoffice');
  expect(
    (
      await request('/api/v1/invitations/accept-existing', {
        body: { code: fresh.code, confirmed: true },
      })
    ).body.code,
  ).toBe('INVITATION_UNAVAILABLE');
  const replacement = await invite('recipient@example.test', 'replacement-invite-0002');
  expect(stored.summary.status).toBe('EXPIRED');
  const target = api.world.accounts.find((account) => account.username === 'reviewer.a')!;
  api.tenantMembership(target, tenant.id).status = 'SUSPENDED';
  api.signIn(target.username, 'backoffice');
  expect(
    (
      await request('/api/v1/invitations/accept-existing', {
        body: { code: replacement.code, confirmed: true },
      })
    ).body.code,
  ).toBe('INVITATION_MEMBERSHIP_CONFLICT');
  expect(api.tenantMembership(target, tenant.id).status).toBe('SUSPENDED');
});

it('keeps foreign selectors generic and preserves already active roles instead of reporting pending access', async () => {
  const { tenant, created, code } = await invite();
  api.signIn('admin.b', 'backoffice');
  const foreign = await request(`/api/v1/admin/invitations/${created.body.invitationId}`, {
    tenant: api.tenantByCode('DEMO_B')!.id,
  });
  expect(foreign.status).toBe(404);
  const changed = code.replace(tenant.id, api.tenantByCode('DEMO_B')!.id);
  expect(
    (await request('/api/v1/invitations/inspect', { body: { code: changed } })).body.code,
  ).toBe('INVITATION_UNAVAILABLE');
  manager();
  const original = structuredClone(api.session!.account.memberships);
  const accepted = await request('/api/v1/invitations/accept-existing', {
    body: { code, confirmed: true },
  });
  expect(accepted.body.accessPending).toBe(false);
  expect(api.session!.account.memberships).toEqual(original);
});

it('expires accepted proof recovery without undoing membership and removes terminal recipient display after retention', async () => {
  const { tenant, created, code } = await invite();
  api.signIn('admin.b', 'backoffice');
  const joined = await request('/api/v1/invitations/accept-existing', {
    body: { code, confirmed: true },
    key: 'retention-accept-0001',
  });
  expect(joined.status).toBe(200);
  const membershipBefore = structuredClone(api.session!.account.memberships);
  const row = api.invitations.rows.get(created.body.invitationId)!;
  row.terminalAt = Date.now() - 25 * 3_600_000;
  expect((await request('/api/v1/invitations/inspect', { body: { code } })).body.code).toBe(
    'INVITATION_UNAVAILABLE',
  );
  expect(
    (
      await request('/api/v1/invitations/accept-existing', {
        body: { code, confirmed: true },
        key: 'retention-accept-0001',
      })
    ).body.code,
  ).toBe('INVITATION_UNAVAILABLE');
  expect(api.session!.account.memberships).toEqual(membershipBefore);
  row.terminalAt = Date.now() - 31 * 24 * 3_600_000;
  manager();
  const detail = await request(`/api/v1/admin/invitations/${created.body.invitationId}`, {
    tenant: tenant.id,
  });
  expect(detail.body.maskedRecipient).toBe('');
  expect(detail.body.status).toBe('ACCEPTED');
  expect(detail.body.deliveryStatus).toBe('SENT');
});
