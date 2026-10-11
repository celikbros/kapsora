// @vitest-environment jsdom
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { App } from './App';
import { createServices } from './api';
import { SOON_PATHS, visibleNavEntries } from './nav';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  cleanup();
  api.reset();
  vi.restoreAllMocks();
});
afterAll(() => server.close());

function mount() {
  const services = createServices({ baseUrl: BASE });
  const history = createMemoryHistory({ initialEntries: ['/security'] });
  render(<App services={services} history={history} />);
  const original = services.ops.health.listAccessLog.bind(services.ops.health);
  const read = vi.spyOn(services.ops.health, 'listAccessLog');
  return { services, read, original };
}

async function login(username: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

function seedNextPage() {
  const seed = api.world.healthAccessEvents[0]!;
  for (let i = 0; i < 51; i++) {
    api.world.healthAccessEvents.push({
      ...seed,
      id: `00000000-0000-4000-8000-${String(i).padStart(12, '0')}`,
      occurredAt: new Date(Date.parse(seed.occurredAt) + (i + 1) * 1000).toISOString(),
    });
  }
}

describe('standalone health access log', () => {
  it('is available with audit.read alone and lists tenant events without member lookup', async () => {
    const admin = api.world.accounts.find((account) => account.username === 'admin.a')!;
    admin.memberships[0]!.permissions = ['audit.read'];
    const { services, read } = mount();
    const people = vi.spyOn(services.ops.people, 'list');
    const cases = vi.spyOn(services.ops.health, 'listCases');
    const caseDetail = vi.spyOn(services.ops.health, 'getCase');
    const organizations = vi.spyOn(services.ops.organizations, 'list');
    await login('admin.a');
    expect(await screen.findByTestId('health-access-log-page')).toBeTruthy();
    expect((await screen.findAllByTestId('health-access-log-row')).length).toBeGreaterThan(0);
    expect(read).toHaveBeenCalledWith(expect.any(String), { limit: 50 });
    expect(people).not.toHaveBeenCalled();
    expect(cases).not.toHaveBeenCalled();
    expect(caseDetail).not.toHaveBeenCalled();
    expect(organizations).not.toHaveBeenCalled();
    expect(visibleNavEntries(['audit.read']).some((entry) => entry.key === 'security')).toBe(true);
    expect(SOON_PATHS).not.toContain('/security');
  });

  it.each(['financial.reviewer', 'sponsor.hr', 'doctor.a'])(
    'denies %s without requesting the access log',
    async (username) => {
      const { read } = mount();
      await login(username);
      expect(await screen.findByTestId('health-access-log-page')).toBeTruthy();
      expect(screen.queryAllByTestId('health-access-log-row')).toHaveLength(0);
      expect(read).not.toHaveBeenCalled();
    },
  );

  it('pages forward and back in groups of fifty and retries an error', async () => {
    seedNextPage();
    const { read, original } = mount();
    read.mockRejectedValue(new Error('temporary'));
    const user = await login('admin.a');
    expect(
      await screen.findByTestId('health-access-log-retry', {}, { timeout: 5_000 }),
    ).toBeTruthy();
    read.mockImplementation(original);
    await user.click(screen.getByTestId('health-access-log-retry'));
    await waitFor(() => expect(screen.getAllByTestId('health-access-log-row')).toHaveLength(50));
    await user.click(screen.getByTestId('health-access-log-next'));
    await waitFor(() =>
      expect(read).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({ limit: 50, cursor: expect.any(String) }),
      ),
    );
    await waitFor(() => expect(screen.getAllByTestId('health-access-log-row')).toHaveLength(3));
    await user.click(screen.getByTestId('health-access-log-previous'));
    await waitFor(() => expect(screen.getAllByTestId('health-access-log-row')).toHaveLength(50));
    await user.click(screen.getByTestId('health-access-log-next'));
    await waitFor(() => expect(screen.getAllByTestId('health-access-log-row')).toHaveLength(3));
  });

  it('starts at the first page when actor or tenant context changes', async () => {
    seedNextPage();
    const { services, read } = mount();
    const user = await login('admin.a');
    await waitFor(() => expect(screen.getAllByTestId('health-access-log-row')).toHaveLength(50));
    await user.click(screen.getByTestId('health-access-log-next'));
    await waitFor(() => expect(screen.getAllByTestId('health-access-log-row')).toHaveLength(3));
    const initial = read.mock.calls.length;
    services.store.setState((state) => ({
      session: { ...state.session!, actorId: 'other-actor' },
    }));
    await waitFor(() => expect(read.mock.calls.length).toBeGreaterThan(initial));
    expect(read.mock.calls.at(-1)?.[1]).toEqual({ limit: 50 });
    await waitFor(() => expect(screen.getAllByTestId('health-access-log-row')).toHaveLength(50));
    const afterActor = read.mock.calls.length;
    services.store.setState((state) => ({
      activeTenant: {
        ...state.activeTenant!,
        tenant: { ...state.activeTenant!.tenant, id: 'other-tenant' },
      },
    }));
    await waitFor(() => expect(read.mock.calls.length).toBeGreaterThan(afterActor));
    expect(read.mock.calls.at(-1)?.[0]).toBe('other-tenant');
    expect(read.mock.calls.at(-1)?.[1]).toEqual({ limit: 50 });
    await waitFor(() => expect(screen.queryAllByTestId('health-access-log-row')).toHaveLength(0));
  });

  it('removes rows immediately when audit.read is revoked', async () => {
    const { services, read } = mount();
    await login('admin.a');
    await screen.findAllByTestId('health-access-log-row');
    const before = read.mock.calls.length;
    services.store.setState((state) => ({
      activeTenant: {
        ...state.activeTenant!,
        permissions: state.activeTenant!.permissions.filter(
          (permission) => permission !== 'audit.read',
        ),
      },
    }));
    await waitFor(() => expect(screen.queryAllByTestId('health-access-log-row')).toHaveLength(0));
    expect(read).toHaveBeenCalledTimes(before);
  });
});
