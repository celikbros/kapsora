import { createKapsoraClient, createOperations } from '@kapsora/api-client';
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { isRedirect } from '@tanstack/react-router';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import {
  NOT_FOR_APP_PATH,
  hasPermission,
  redirectIfAuthenticated,
  requireAuthenticated,
  requireTenant,
  safeReturnTo,
} from './guards';
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

async function redirectOf(
  p: Promise<unknown>,
): Promise<{ to: string; search: Record<string, string> } | null> {
  try {
    await p;
    return null;
  } catch (err) {
    if (isRedirect(err)) {
      const href = (err.options as { href?: string }).href ?? '';
      const url = new URL(href, 'http://app.test');
      return { to: url.pathname, search: Object.fromEntries(url.searchParams) };
    }
    throw err;
  }
}

describe('guards', () => {
  it('sends anonymous users to login with the return path', async () => {
    const store = makeStore();
    const r = await redirectOf(
      requireAuthenticated(store, { pathname: '/organizations', searchStr: '?role=PROVIDER' }),
    );
    expect(r?.to).toBe('/auth/login');
    expect(r?.search).toEqual({ returnTo: '/organizations?role=PROVIDER' });
  });

  it('lets single-tenant users through and auto-selects the tenant', async () => {
    const store = makeStore();
    await store.login('admin.a', 'demo parola 2026 kapsora');
    const state = await requireTenant(store, { pathname: '/' });
    expect(state.activeTenant?.tenant.code).toBe('DEMO_A');
  });

  it('sends multi-tenant users to the picker until they choose', async () => {
    const store = makeStore();
    await store.login('both.ab', 'demo parola 2026 kapsora');
    const r = await redirectOf(requireTenant(store, { pathname: '/organizations' }));
    expect(r?.to).toBe('/auth/tenant');
    const a = store.getState().me!.tenants[0]!.tenant.id;
    await store.switchTenant(a);
    expect(
      (await requireTenant(store, { pathname: '/organizations' })).activeTenant?.tenant.id,
    ).toBe(a);
  });

  it('keeps signed-in users away from the login page', async () => {
    const store = makeStore();
    expect(await redirectOf(redirectIfAuthenticated(store))).toBeNull();
    await store.login('admin.a', 'demo parola 2026 kapsora');
    expect((await redirectOf(redirectIfAuthenticated(store, '/home')))?.to).toBe('/home');
  });

  it('checks permissions of the active tenant only', async () => {
    const store = makeStore();
    await store.login('reviewer.a', 'demo parola 2026 kapsora');
    expect(hasPermission(store.getState(), 'organization.read')).toBe(true);
    expect(hasPermission(store.getState(), 'organization.manage')).toBe(false);
  });

  it('rejects unsafe return targets', () => {
    expect(safeReturnTo('/organizations?x=1')).toBe('/organizations?x=1');
    expect(safeReturnTo('//evil.example')).toBe('/');
    expect(safeReturnTo('https://evil.example')).toBe('/');
    expect(safeReturnTo('/auth/login')).toBe('/');
    expect(safeReturnTo(undefined, '/home')).toBe('/home');
  });

  it('admits an account only where it has work, and says so where it has none', async () => {
    const store = makeStore();
    await store.login('provider.a', 'demo parola 2026 kapsora');
    expect((await requireTenant(store, { pathname: '/' }, 'provider')).activeTenant).not.toBeNull();
    const r = await redirectOf(requireTenant(store, { pathname: '/claims' }, 'backoffice'));
    expect(r?.to).toBe(NOT_FOR_APP_PATH);
    expect(r?.search).toEqual({});
    expect((await redirectOf(requireTenant(store, { pathname: '/' }, 'member')))?.to).toBe(
      NOT_FOR_APP_PATH,
    );
  });

  it('sends a staff account away from the member app and a member away from the backoffice', async () => {
    const staff = makeStore();
    await staff.login('admin.a', 'demo parola 2026 kapsora');
    expect((await redirectOf(requireTenant(staff, { pathname: '/' }, 'member')))?.to).toBe(
      NOT_FOR_APP_PATH,
    );
    expect(
      (await requireTenant(staff, { pathname: '/' }, 'backoffice')).activeTenant,
    ).not.toBeNull();

    const member = makeStore();
    await member.login('member.a', 'demo parola 2026 kapsora');
    expect(
      (await requireTenant(member, { pathname: '/' }, 'member')).activeTenant?.personId,
    ).toBeTruthy();
    expect((await redirectOf(requireTenant(member, { pathname: '/' }, 'backoffice')))?.to).toBe(
      NOT_FOR_APP_PATH,
    );
  });

  it('still sends a multi-tenant staff account to the picker in the backoffice', async () => {
    const store = makeStore();
    await store.login('both.ab', 'demo parola 2026 kapsora');
    const r = await redirectOf(requireTenant(store, { pathname: '/organizations' }, 'backoffice'));
    expect(r?.to).toBe('/auth/tenant');
  });
});
