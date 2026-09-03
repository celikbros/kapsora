import { createKapsoraClient, createOperations } from '@kapsora/api-client';
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { createSessionStore } from './store';

const { api, server } = createMockServer({ organizationsPerTenant: 5 });

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

function makeStore() {
  const store = createSessionStore(
    createOperations(
      createKapsoraClient({
        baseUrl: 'http://mock.test',
        csrfToken: () => store.getState().csrfToken,
      }),
    ),
  );
  return store;
}

describe('session store', () => {
  it('bootstraps to anonymous on 401 and shares one in-flight request', async () => {
    const store = makeStore();
    const [a, b] = await Promise.all([store.bootstrap(), store.bootstrap()]);
    expect(a.status).toBe('anonymous');
    expect(b).toBe(a);
    expect(store.getState().csrfToken).toBeNull();
  });

  it('logs in, keeps the CSRF token in memory only and auto-selects a single tenant', async () => {
    const store = makeStore();
    const state = await store.login('admin.a', 'demo parola 2026 kapsora');
    expect(state.status).toBe('authenticated');
    expect(state.csrfToken).toMatch(/^csrf-/);
    expect(state.activeTenant?.tenant.code).toBe('DEMO_A');
    expect(state.me?.displayName).toBe('Ayşe Yönetici');
    expect(Object.keys(localStorage)).toHaveLength(0);
    expect(Object.keys(sessionStorage)).toHaveLength(0);
  });

  it('leaves the tenant unselected for multi-tenant users and switches on request', async () => {
    const store = makeStore();
    const state = await store.login('both.ab', 'demo parola 2026 kapsora');
    expect(state.activeTenant).toBeNull();
    const b = state.me!.tenants.find((t) => t.tenant.code === 'DEMO_B')!;
    const ctx = await store.switchTenant(b.tenant.id);
    expect(ctx.tenant.code).toBe('DEMO_B');
    expect(store.getState().session?.activeTenantId).toBe(b.tenant.id);

    // A fresh store (page reload) re-bootstraps from the server session.
    const again = makeStore();
    const re = await again.bootstrap();
    expect(re.status).toBe('authenticated');
    expect(re.activeTenant?.tenant.code).toBe('DEMO_B');
  });

  it('logs out and clears everything even if the server call fails', async () => {
    const store = makeStore();
    await store.login('admin.a', 'demo parola 2026 kapsora');
    await store.logout();
    expect(store.getState()).toMatchObject({
      status: 'anonymous',
      csrfToken: null,
      me: null,
      activeTenant: null,
    });

    const broken = createSessionStore(
      createOperations(
        createKapsoraClient({ baseUrl: 'http://mock.test', csrfToken: () => 'wrong' }),
      ),
    );
    await broken.login('admin.a', 'demo parola 2026 kapsora').catch(() => undefined);
    await expect(broken.logout()).rejects.toBeDefined();
    expect(broken.getState().status).toBe('anonymous');
  });
});
