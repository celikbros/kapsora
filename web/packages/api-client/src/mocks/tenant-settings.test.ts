import { afterAll, afterEach, beforeAll, expect, it } from 'vitest';
import { createMockServer } from './node';

const { api, server } = createMockServer();
const path = 'http://mock.test/api/v1/admin/tenant-settings/accommodation';
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

async function call(
  tenantId: string,
  body?: unknown,
  etag?: string,
  key = 'settings-command-00000001',
) {
  const headers: Record<string, string> = {
    'X-Kapsora-App': 'backoffice',
    'X-Tenant-ID': tenantId,
  };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    headers['X-CSRF-Token'] = api.session!.csrfToken;
    headers['If-Match'] = etag ?? '';
    headers['Idempotency-Key'] = key;
  }
  const response = await fetch(path, {
    method: body === undefined ? 'GET' : 'PUT',
    headers,
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  return {
    status: response.status,
    etag: response.headers.get('ETag'),
    body: (await response.json()) as Record<string, unknown> & {
      holdMinutes: { effectiveValue: number; source: string };
      quoteTtlMinutes: { effectiveValue: number; source: string };
      canManage: boolean;
      code: string;
    },
  };
}

function manager() {
  api.signIn('admin.a', 'backoffice')!.stepUpExpiresAt = new Date(
    Date.now() + 60_000,
  ).toISOString();
  return api.tenantByCode('DEMO_A')!.id;
}

it('projects defaults without writes, stores both typed values, and replays the original receipt', async () => {
  const tenantId = manager();
  const initial = await call(tenantId);
  expect(initial.status).toBe(200);
  expect(initial.body.holdMinutes).toMatchObject({ effectiveValue: 15, source: 'DEFAULT' });
  expect(initial.body.quoteTtlMinutes).toMatchObject({ effectiveValue: 60, source: 'DEFAULT' });
  expect(api.accommodationSettings.size).toBe(0);
  const body = { holdMinutes: 30, quoteTtlMinutes: 90, reasonCode: 'OPERATING_REQUIREMENT' };
  const saved = await call(tenantId, body, initial.etag!);
  expect(saved.status).toBe(200);
  expect(saved.body.holdMinutes).toMatchObject({ effectiveValue: 30, source: 'CONFIGURED' });
  expect(api.tenantSettingsEvents).toHaveLength(1);
  const replay = await call(tenantId, body, initial.etag!);
  expect(replay).toEqual(saved);
  expect(api.tenantSettingsEvents).toHaveLength(1);
  expect((await call(tenantId, { ...body, holdMinutes: 31 }, initial.etag!)).body.code).toBe(
    'IDEMPOTENCY_KEY_REUSED',
  );
  expect((await call(tenantId, body, initial.etag!, 'settings-command-00000002')).status).toBe(412);
  const noOp = await call(tenantId, body, saved.etag!, 'settings-command-00000003');
  expect(noOp.etag).toBe(saved.etag);
  expect(api.tenantSettingsEvents).toHaveLength(1);
});

it('rejects invalid input and keeps tenant settings authority separate from directory access', async () => {
  const tenantId = manager();
  const etag = (await call(tenantId)).etag!;
  for (const bad of [
    { holdMinutes: 0, quoteTtlMinutes: 60, reasonCode: 'OPERATING_REQUIREMENT' },
    { holdMinutes: 1.5, quoteTtlMinutes: 60, reasonCode: 'OPERATING_REQUIREMENT' },
    { holdMinutes: '15', quoteTtlMinutes: 60, reasonCode: 'OPERATING_REQUIREMENT' },
    { holdMinutes: 15, quoteTtlMinutes: 1441, reasonCode: 'OPERATING_REQUIREMENT' },
    { holdMinutes: 15, quoteTtlMinutes: 60, reasonCode: 'OTHER' },
    { holdMinutes: 15, quoteTtlMinutes: 60, reasonCode: 'OPERATING_REQUIREMENT', other: true },
  ]) {
    expect((await call(tenantId, bad, etag, crypto.randomUUID())).status).toBe(422);
  }
  expect(api.accommodationSettings.size).toBe(0);
  api.session!.account.memberships[0]!.permissions = ['identity.user.read'];
  expect((await call(tenantId)).status).toBe(403);
  api.session!.account.memberships[0]!.permissions = ['platform.tenant_settings.read'];
  expect((await call(tenantId)).body.canManage).toBe(false);
  expect(
    (
      await call(
        tenantId,
        { holdMinutes: 15, quoteTtlMinutes: 60, reasonCode: 'CORRECT_CONFIGURATION' },
        etag,
      )
    ).status,
  ).toBe(403);
});
