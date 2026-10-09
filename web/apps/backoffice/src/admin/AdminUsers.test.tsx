import { ApiError } from '@kapsora/api-client';
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
  const history = await within(detail).findByTestId('role-grants');
  expect(within(history).getByText('Rol atamaları')).toBeInTheDocument();
  expect(history).toHaveTextContent('geçerlilikleri');
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
        rowVersion: 1,
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

async function openSuspend(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('link', { name: 'Refik İnceleyici' }));
  const detail = await screen.findByTestId('admin-user-detail-page');
  await user.click(within(detail).getByRole('button', { name: 'Üyeliği askıya al' }));
  return screen.getByRole('dialog', { name: 'Kurum üyeliğini askıya al' });
}

async function chooseSuspension(user: ReturnType<typeof userEvent.setup>) {
  const dialog = await openSuspend(user);
  await user.selectOptions(within(dialog).getByLabelText('Gerekçe'), 'ACCESS_REVIEW');
  await user.click(within(dialog).getByRole('checkbox'));
  return dialog;
}

it('requires a reason and tenant-specific confirmation before suspending a membership', async () => {
  const services = mount('/admin');
  const user = await login('admin.a');
  const suspend = vi.spyOn(services.ops.admin, 'suspendUser');
  const dialog = await openSuspend(user);
  expect(dialog).toHaveTextContent('Demo Banka');
  expect(within(dialog).getByRole('button', { name: 'Üyeliği askıya al' })).toBeDisabled();
  await user.selectOptions(within(dialog).getByLabelText('Gerekçe'), 'ACCESS_REVIEW');
  expect(within(dialog).getByRole('button', { name: 'Üyeliği askıya al' })).toBeDisabled();
  await user.click(within(dialog).getByRole('checkbox'));
  await user.click(within(dialog).getByRole('button', { name: 'Üyeliği askıya al' }));
  expect(suspend).toHaveBeenCalledTimes(1);
  const stepUp = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await user.type(within(stepUp).getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(within(stepUp).getByRole('button', { name: 'Doğrula' }));
  await waitFor(() =>
    expect(screen.getByTestId('admin-user-detail-page')).toHaveTextContent('Askıda'),
  );
  expect(suspend).toHaveBeenCalledTimes(2);
  expect(suspend.mock.calls[0]).toEqual(suspend.mock.calls[1]);
});

it('keeps the same command on an uncertain response and requires reload after a stale version', async () => {
  const services = mount('/admin');
  const user = await login('admin.a');
  const original = services.ops.admin.suspendUser;
  const suspend = vi
    .spyOn(services.ops.admin, 'suspendUser')
    .mockRejectedValueOnce(
      new ApiError({
        type: 'about:blank',
        title: 'Network error',
        status: 0,
        code: 'NETWORK_ERROR',
        traceId: '',
      }),
    )
    .mockRejectedValueOnce(
      new ApiError({
        type: 'about:blank',
        title: 'Conflict',
        status: 412,
        code: 'ETAG_MISMATCH',
        traceId: '',
      }),
    )
    .mockImplementation(original);
  const dialog = await chooseSuspension(user);
  await user.click(within(dialog).getByRole('button', { name: 'Üyeliği askıya al' }));
  expect(await within(dialog).findByText(/Sonuç belirsiz/)).toBeInTheDocument();
  await user.click(within(dialog).getByRole('button', { name: 'Vazgeç' }));
  await user.click(screen.getByTestId('admin-user-detail-page').querySelector('button')!);
  const reopened = screen.getByRole('dialog', { name: 'Kurum üyeliğini askıya al' });
  await user.click(within(reopened).getByRole('button', { name: 'Aynı isteği yeniden dene' }));
  expect(suspend).toHaveBeenCalledTimes(2);
  expect(suspend.mock.calls[0]).toEqual(suspend.mock.calls[1]);
  expect(
    await within(reopened).findByRole('button', { name: 'Üyeliği yeniden yükle' }),
  ).toBeInTheDocument();
  await user.click(within(reopened).getByRole('button', { name: 'Üyeliği yeniden yükle' }));
  expect(await within(reopened).findByRole('button', { name: 'Üyeliği askıya al' })).toBeDisabled();
  expect(suspend).toHaveBeenCalledTimes(2);
});

it('keeps the directory readable but hides suspension when manage capability is absent', async () => {
  const original = api.tenantContexts.bind(api);
  vi.spyOn(api, 'tenantContexts').mockImplementation((account, app) =>
    original(account, app).map((tenant) => {
      const withoutManage = { ...tenant };
      delete withoutManage.canManageTenantUsers;
      return withoutManage;
    }),
  );
  const services = mount('/admin');
  const suspend = vi.spyOn(services.ops.admin, 'suspendUser');
  const user = await login('admin.a');
  await user.click(await screen.findByRole('link', { name: 'Refik İnceleyici' }));
  expect(await screen.findByTestId('admin-user-detail-page')).toHaveTextContent('Refik İnceleyici');
  expect(screen.queryByRole('button', { name: 'Üyeliği askıya al' })).not.toBeInTheDocument();
  expect(suspend).not.toHaveBeenCalled();
  vi.restoreAllMocks();
});

it('does not replay a delayed step-up after manage capability changes', async () => {
  const services = mount('/admin');
  const user = await login('admin.a');
  const suspend = vi.spyOn(services.ops.admin, 'suspendUser');
  const originalStepUp = services.ops.session.stepUp;
  const getSession = vi.spyOn(services.ops.session, 'get');
  let finish!: () => void;
  const deferred = new Promise<void>((resolve) => {
    finish = resolve;
  });
  const password = vi
    .spyOn(services.ops.session, 'stepUp')
    .mockImplementationOnce(async (value) => {
      await deferred;
      return originalStepUp(value);
    });
  const dialog = await chooseSuspension(user);
  await user.click(within(dialog).getByRole('button', { name: 'Üyeliği askıya al' }));
  const stepUp = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await user.type(within(stepUp).getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(within(stepUp).getByRole('button', { name: 'Doğrula' }));
  await waitFor(() => expect(password).toHaveBeenCalledTimes(1));
  const active = services.store.getState().activeTenant!;
  act(() => services.store.setState({ activeTenant: { ...active, canManageTenantUsers: false } }));
  expect(screen.queryByRole('button', { name: 'Üyeliği askıya al' })).not.toBeInTheDocument();
  await act(async () => {
    finish();
    await password.mock.results[0]!.value;
  });
  expect(services.store.getState().activeTenant?.canManageTenantUsers).toBe(false);
  expect(getSession).not.toHaveBeenCalled();
  expect(suspend).toHaveBeenCalledTimes(1);
});

it('retries the pinned command after password cancellation and an in-progress response', async () => {
  const services = mount('/admin');
  const user = await login('admin.a');
  const original = services.ops.admin.suspendUser;
  const suspend = vi
    .spyOn(services.ops.admin, 'suspendUser')
    .mockRejectedValueOnce(
      new ApiError({
        type: 'about:blank',
        title: 'In progress',
        status: 409,
        code: 'IDEMPOTENCY_IN_PROGRESS',
        traceId: '',
      }),
    )
    .mockImplementation(original);
  const dialog = await chooseSuspension(user);
  await user.click(within(dialog).getByRole('button', { name: 'Üyeliği askıya al' }));
  expect(
    await within(dialog).findByRole('button', { name: 'Aynı isteği yeniden dene' }),
  ).toBeInTheDocument();
  await user.click(within(dialog).getByRole('button', { name: 'Aynı isteği yeniden dene' }));
  const stepUp = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await user.click(within(stepUp).getByRole('button', { name: 'Vazgeç' }));
  expect(within(dialog).getByRole('button', { name: 'Aynı isteği yeniden dene' })).toBeEnabled();
  await user.click(within(dialog).getByRole('button', { name: 'Vazgeç' }));
  await user.click(screen.getByRole('button', { name: 'Üyeliği askıya al' }));
  const reopened = screen.getByRole('dialog', { name: 'Kurum üyeliğini askıya al' });
  await user.click(within(reopened).getByRole('button', { name: 'Aynı isteği yeniden dene' }));
  expect(await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' })).toBeInTheDocument();
  expect(suspend).toHaveBeenCalledTimes(3);
  expect(suspend.mock.calls[0]).toEqual(suspend.mock.calls[1]);
  expect(suspend.mock.calls[1]).toEqual(suspend.mock.calls[2]);
});
