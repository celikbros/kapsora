import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';
import { App } from '../App';
import { createServices } from '../api';
import { visibleNavEntries } from '../nav';

const { api, server } = createMockServer();
const BASE = 'http://mock.test';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => api.reset());
afterAll(() => server.close());

function mount(path: string) {
  const services = createServices({ baseUrl: BASE });
  render(<App services={services} history={createMemoryHistory({ initialEntries: [path] })} />);
  return services;
}

async function login(username: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

it('opens a tenant membership detail with assigned roles and a working back link', async () => {
  mount('/admin');
  const user = await login('admin.a');
  const page = await screen.findByTestId('admin-users-page');
  const rows = within(page).getAllByTestId('admin-user-row');
  expect(rows.length).toBeGreaterThan(0);
  expect(page).not.toHaveTextContent('admin.a@example.invalid');
  await user.click(within(rows[0]!).getByRole('link'));
  const detail = await screen.findByTestId('admin-user-detail-page');
  expect(within(detail).getByText('Atanmış roller')).toBeInTheDocument();
  expect(within(detail).getAllByTestId('admin-assigned-role').length).toBeGreaterThan(0);
  expect(detail).toHaveTextContent('erişim sağladıkları anlamına gelmez');
  await user.click(within(detail).getByRole('link', { name: /Kullanıcılara dön/ }));
  expect(await screen.findByTestId('admin-users-page')).toBeInTheDocument();
});

it('keeps finance out of nav and mounts no list or detail query on direct visits', async () => {
  const services = mount('/admin/users/00000000-0000-7000-8000-000000000001');
  const read = vi.spyOn(services.ops.admin, 'getUser');
  await login('financial.reviewer');
  expect(await screen.findByText(/Bu işlem için yetkiniz yok/)).toBeInTheDocument();
  expect(read).not.toHaveBeenCalled();
  expect(
    visibleNavEntries(services.store.getState().activeTenant?.permissions ?? [], false).some(
      (entry) => entry.key === 'admin',
    ),
  ).toBe(false);
});

it('clears old membership rows and pending reads when the correlated capability is revoked', async () => {
  const services = mount('/admin');
  const user = await login('admin.a');
  expect((await screen.findAllByTestId('admin-user-row')).length).toBeGreaterThan(0);
  const active = services.store.getState().activeTenant!;
  expect(active.canReadTenantUsers).toBe(true);
  const original = services.ops.admin.listUsers;
  let finish!: (page: Awaited<ReturnType<typeof original>>) => void;
  const deferred = new Promise<Awaited<ReturnType<typeof original>>>((resolve) => {
    finish = resolve;
  });
  const read = vi.spyOn(services.ops.admin, 'listUsers').mockImplementationOnce(() => deferred);
  await user.selectOptions(screen.getByLabelText('Üyelik durumu'), 'SUSPENDED');
  await waitFor(() => expect(read).toHaveBeenCalledTimes(1));
  act(() => services.store.setState({ activeTenant: { ...active, canReadTenantUsers: false } }));
  expect(await screen.findByText(/Bu işlem için yetkiniz yok/)).toBeInTheDocument();
  finish({ items: [], nextCursor: null });
  await waitFor(() =>
    expect(
      services.queryClient.getQueryCache().findAll({ queryKey: ['admin-users'] }),
    ).toHaveLength(0),
  );
  expect(screen.queryByTestId('admin-user-row')).not.toBeInTheDocument();
  expect(visibleNavEntries(active.permissions, false).some((entry) => entry.key === 'admin')).toBe(
    false,
  );
});

it('resets paging on status change and offers a retry after a read error', async () => {
  const template = api.world.accounts.find((account) => account.username === 'admin.a')!;
  for (let n = 0; n < 51; n++) {
    api.world.accounts.push({
      ...template,
      actorId: `f0000000-0000-7000-8000-${n.toString(16).padStart(12, '0')}`,
      username: `synthetic-${n}`,
      displayName: `Synthetic ${n}`,
      memberships: [{ tenantCode: 'DEMO_A', permissions: [] }],
    });
  }
  const services = mount('/admin');
  const original = services.ops.admin.listUsers;
  vi.spyOn(services.ops.admin, 'listUsers').mockRejectedValueOnce(new Error('temporary'));
  const user = await login('admin.a');
  expect(await screen.findByRole('button', { name: 'Yeniden dene' })).toBeInTheDocument();
  vi.mocked(services.ops.admin.listUsers).mockImplementation(original);
  await user.click(screen.getByRole('button', { name: 'Yeniden dene' }));
  await waitFor(() => expect(screen.getAllByTestId('admin-user-row')).toHaveLength(50));
  await user.click(screen.getByRole('button', { name: 'Sonraki sayfa' }));
  await waitFor(() => expect(screen.getAllByTestId('admin-user-row').length).toBeLessThan(50));
  await user.selectOptions(screen.getByLabelText('Üyelik durumu'), 'REVOKED');
  expect(await screen.findByText('Bu duruma uygun üyelik bulunamadı.')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Önceki sayfa' })).not.toBeInTheDocument();
});

it('fails closed when the correlated capability is absent from session context', async () => {
  const original = api.tenantContexts.bind(api);
  vi.spyOn(api, 'tenantContexts').mockImplementation((account, app) =>
    original(account, app).map((tenant) => {
      const withoutCapability = { ...tenant };
      delete withoutCapability.canReadTenantUsers;
      return withoutCapability;
    }),
  );
  const services = mount('/admin');
  const read = vi.spyOn(services.ops.admin, 'listUsers');
  await login('admin.a');
  expect(await screen.findByText(/Bu işlem için yetkiniz yok/)).toBeInTheDocument();
  expect(read).not.toHaveBeenCalled();
  expect(
    visibleNavEntries(services.store.getState().activeTenant?.permissions ?? [], false).some(
      (entry) => entry.key === 'admin',
    ),
  ).toBe(false);
  vi.restoreAllMocks();
});

it('drops a delayed read when actor context changes even if the capability stays present', async () => {
  const services = mount('/admin');
  const user = await login('admin.a');
  expect((await screen.findAllByTestId('admin-user-row')).length).toBeGreaterThan(0);
  const state = services.store.getState();
  const original = services.ops.admin.listUsers;
  let finish!: (page: Awaited<ReturnType<typeof original>>) => void;
  const deferred = new Promise<Awaited<ReturnType<typeof original>>>((resolve) => {
    finish = resolve;
  });
  const read = vi
    .spyOn(services.ops.admin, 'listUsers')
    .mockImplementationOnce(() => deferred)
    .mockResolvedValueOnce({ items: [], nextCursor: null });
  await user.selectOptions(screen.getByLabelText('Üyelik durumu'), 'SUSPENDED');
  await waitFor(() => expect(read).toHaveBeenCalledTimes(1));
  const newActorId = 'f0000000-0000-7000-8000-000000000099';
  act(() =>
    services.store.setState({
      session: { ...state.session!, actorId: newActorId },
    }),
  );
  await waitFor(() => expect(read).toHaveBeenCalledTimes(2));
  expect(await screen.findByText('Bu duruma uygun üyelik bulunamadı.')).toBeInTheDocument();
  finish({
    items: [
      {
        id: 'f0000000-0000-7000-8000-000000000001',
        displayName: 'Stale actor membership',
        actorType: 'HUMAN',
        actorStatus: 'ACTIVE',
        membershipStatus: 'SUSPENDED',
        validFrom: null,
        validTo: null,
        validityEmpty: false,
      },
    ],
    nextCursor: null,
  });
  await waitFor(() =>
    expect(
      services.queryClient.getQueryCache().findAll({ queryKey: ['admin-users'] }),
    ).toHaveLength(1),
  );
  expect(screen.queryByText('Stale actor membership')).not.toBeInTheDocument();
  const keys = services.queryClient
    .getQueryCache()
    .findAll({ queryKey: ['admin-users'] })
    .map((query) => JSON.stringify(query.queryKey));
  expect(
    keys.every((key) => key.includes(newActorId) && !key.includes(state.session!.actorId)),
  ).toBe(true);
});
