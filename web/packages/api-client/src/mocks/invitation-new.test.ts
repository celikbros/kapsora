import { afterAll, afterEach, beforeAll, expect, it } from 'vitest';
import { createMockServer } from './node';
import { invitationProofDigest } from './invitation-state';

const { api, server } = createMockServer();
const PASSWORD = 'new account password 2026';
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

async function fixture() {
  const tenant = api.world.tenants[0]!;
  const invitationId = crypto.randomUUID();
  const code = `v1.${tenant.id}.${invitationId}.${'A'.repeat(43)}`;
  api.invitations.rows.set(invitationId, {
    summary: {
      invitationId,
      maskedRecipient: 'n***@***',
      status: 'PENDING',
      createdAt: new Date().toISOString(),
      expiresAt: new Date(Date.now() + 48 * 3_600_000).toISOString(),
      rowVersion: 1,
      deliveryStatus: 'SENT',
    },
    tenantId: tenant.id,
    contactIndex: 'private-contact-index',
    proofDigest: await invitationProofDigest(code),
    acceptedActorId: null,
    acceptedKey: null,
    acceptedOutcome: null,
    terminalAt: null,
  });
  return { tenant, invitationId, code };
}

async function post(
  path: string,
  body: unknown,
  key?: string,
  headers: Record<string, string> = {},
) {
  const response = await fetch(`http://mock.test/api/v1/invitations/${path}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Invitation-Request': '1',
      Origin: 'http://mock.test',
      ...(key ? { 'Idempotency-Key': key } : {}),
      ...headers,
    },
    body: JSON.stringify(body),
  });
  return {
    status: response.status,
    cache: response.headers.get('Cache-Control'),
    body: (await response.json()) as Record<string, unknown>,
  };
}

it('creates one zero-grant account after anonymous consent and requires its chosen password at ordinary login', async () => {
  const { tenant, invitationId, code } = await fixture();
  const before = api.world.accounts.length;
  const preview = await post('inspect-new', { code });
  expect(preview.status).toBe(200);
  expect(preview.body).toMatchObject({
    invitationStatus: 'PENDING',
    tenantDisplayName: tenant.displayName,
  });
  expect(JSON.stringify(preview.body)).not.toContain(code);
  const result = await post(
    'accept-new',
    { code, displayName: 'New Recipient', password: PASSWORD, confirmed: true },
    'new-command-0001',
  );
  expect(result.status).toBe(200);
  expect(result.cache).toBe('no-store');
  expect(api.session).toBeNull();
  expect(api.world.accounts).toHaveLength(before + 1);
  const handle = result.body['loginHandle'] as string;
  expect(handle).toMatch(/^k_[0-9a-f]{32}$/);
  const account = api.world.accounts.find((item) => item.username === handle)!;
  expect(account.email).toBe('');
  expect(account.memberships).toEqual([
    { tenantCode: tenant.code, permissions: [], membershipOnly: true },
  ]);
  expect(api.invitations.rows.get(invitationId)?.acceptedMode).toBe('NEW');
  expect(JSON.stringify(api.invitations.rows.get(invitationId))).not.toContain(PASSWORD);
  expect((await post('inspect-new', { code })).status).toBe(404);
  expect((await post('acceptance-receipt', { code, password: 'wrong password 2026' })).status).toBe(
    404,
  );
  expect((await post('acceptance-receipt', { code, password: PASSWORD })).body).toEqual(
    result.body,
  );
  const wrongLogin = await fetch('http://mock.test/api/v1/session/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: handle, password: 'wrong password 2026' }),
  });
  expect(wrongLogin.status).toBe(401);
  const login = await fetch('http://mock.test/api/v1/session/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: handle, password: PASSWORD }),
  });
  expect(login.status).toBe(200);
  expect(api.session?.account.actorId).toBe(account.actorId);
});

it('keeps an accepted handle private from code-only proof, changed key and wrong password', async () => {
  const { code } = await fixture();
  const payload = { code, displayName: 'New Recipient', password: PASSWORD, confirmed: true };
  const first = await post('accept-new', payload, 'new-command-0001');
  expect(first.status).toBe(200);
  expect((await post('accept-new', payload, 'new-command-0001')).body).toEqual(first.body);
  expect((await post('accept-new', payload, 'changed-command-0001')).status).toBe(409);
  expect(
    (await post('accept-new', { ...payload, password: 'wrong password 2026' }, 'new-command-0001'))
      .status,
  ).toBe(404);
  expect((await post('inspect-new', { code })).status).toBe(404);
  expect(
    api.world.accounts.filter((item) => item.username === first.body['loginHandle']),
  ).toHaveLength(1);
});

it('rejects cross-origin or missing anonymous headers and expires private receipt without revoking membership', async () => {
  const { code, invitationId } = await fixture();
  expect(
    (await post('inspect-new', { code }, undefined, { Origin: 'https://other.test' })).status,
  ).toBe(403);
  expect(
    (await post('inspect-new', { code }, undefined, { 'X-Invitation-Request': '' })).status,
  ).toBe(403);
  const accepted = await post(
    'accept-new',
    { code, displayName: 'New Recipient', password: PASSWORD, confirmed: true },
    'new-command-0001',
  );
  expect(accepted.status).toBe(200);
  const row = api.invitations.rows.get(invitationId)!;
  row.newOutcome!.recoveryExpiresAt = new Date(Date.now() - 1000).toISOString();
  expect((await post('acceptance-receipt', { code, password: PASSWORD })).status).toBe(404);
  expect(row.proofDigest).toBe('');
  expect(row.summary.status).toBe('ACCEPTED');
});

it('shares a generated account lockout across receipt recovery and ordinary login', async () => {
  const { code } = await fixture();
  const accepted = await post(
    'accept-new',
    { code, displayName: 'New Recipient', password: PASSWORD, confirmed: true },
    'new-command-0001',
  );
  const handle = accepted.body['loginHandle'] as string;
  for (let attempt = 0; attempt < 9; attempt++) {
    expect(
      (await post('acceptance-receipt', { code, password: 'wrong password 2026' })).status,
    ).toBe(404);
  }
  const login = await fetch('http://mock.test/api/v1/session/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: handle, password: 'wrong password 2026' }),
  });
  expect(login.status).toBe(403);
  expect((await post('acceptance-receipt', { code, password: PASSWORD })).status).toBe(404);
  expect(api.session).toBeNull();
});

it('rejects six emoji as a short password even though JavaScript counts twelve UTF-16 units', async () => {
  const { code } = await fixture();
  const before = api.world.accounts.length;
  const short = await post(
    'accept-new',
    { code, displayName: 'New Recipient', password: '😀'.repeat(6), confirmed: true },
    'new-command-0001',
  );
  expect(short.status).toBe(422);
  expect(api.world.accounts).toHaveLength(before);
  const enough = await post(
    'accept-new',
    { code, displayName: 'New Recipient', password: '😀'.repeat(12), confirmed: true },
    'new-command-0002',
  );
  expect(enough.status).toBe(200);
});
