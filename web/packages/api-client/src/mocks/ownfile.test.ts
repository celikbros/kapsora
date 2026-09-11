import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { createKapsoraClient } from '../client';
import { createOperations } from '../operations';
import { ApiError, type Problem } from '../problem';
import { createMockServer } from './node';

/**
 * The mock applies the server's own-file rule: a reviewer who is also a member cannot decide a
 * file of their own person, from the backoffice or with no app named, and a reviewer who is
 * somebody else can.
 */
const { api, server } = createMockServer({ organizationsPerTenant: 4 });
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

async function signIn(username: string, app?: 'backoffice' | 'member') {
  const c = createKapsoraClient({
    baseUrl: 'http://mock.test',
    csrfToken: () => api.session?.csrfToken ?? null,
    ...(app ? { app } : {}),
  });
  const o = createOperations(c);
  await o.session.login(username, PASSWORD);
  const tenantId = (await o.session.tenants())[0]!.id;
  const me = await o.session.me();
  if (!(await o.session.get()).activeTenantId) await o.session.switchTenant(tenantId);
  return { o, tenantId, me };
}

async function refusal(call: Promise<unknown>): Promise<Problem> {
  try {
    await call;
  } catch (error) {
    if (error instanceof ApiError) return error.problem;
    throw error;
  }
  throw new Error('expected the call to be refused');
}

function ownPendingRequestId(): string {
  const account = api.world.accounts.find((a) => a.username === 'staff.member')!;
  const self = account.memberships
    .flatMap((m) => m.scopes ?? [])
    .find((g) => g.type === 'PERSON')!.id!;
  return api.world.serviceRequests.find(
    (r) => r.personId === self && r.status === 'PENDING_REVIEW',
  )!.id;
}

describe('the own-file rule in the mock', () => {
  it('refuses a reviewer their own request, in the backoffice and with no app named', async () => {
    const id = ownPendingRequestId();
    for (const app of ['backoffice', undefined] as const) {
      const { o, tenantId, me } = await signIn('staff.member', app);
      expect(me.tenants[0]!.selfPersonId).toBeTruthy();
      const current = await o.requests.get(tenantId, id);
      const problem = await refusal(
        o.requests.approve(tenantId, id, current.etag, { reasonCode: 'COVERED' }),
      );
      expect(problem.status).toBe(403);
      expect(problem.code).toBe('OWN_FILE_DECISION');
      expect(api.world.serviceRequests.find((r) => r.id === id)!.status).toBe('PENDING_REVIEW');
      await o.session.logout();
    }
  });

  it('lets a reviewer who is somebody else decide the same request', async () => {
    const id = ownPendingRequestId();
    const { o, tenantId } = await signIn('doctor.a', 'backoffice');
    const current = await o.requests.get(tenantId, id);
    const approved = await o.requests.approve(tenantId, id, current.etag, {
      reasonCode: 'COVERED',
    });
    expect(approved.data.status).toBe('APPROVED');
  });
});
