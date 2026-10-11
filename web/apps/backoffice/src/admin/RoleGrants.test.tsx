import { ApiError } from '@kapsora/api-client';
import { createMockServer } from '@kapsora/api-client/mocks/node';
import type { MockAccount } from '@kapsora/api-client/mocks';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';
import { App } from '../App';
import { createServices } from '../api';

const { api, server } = createMockServer();
beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  api.reset();
  vi.restoreAllMocks();
});
afterAll(() => server.close());

function fixture() {
  const tenant = api.tenantByCode('DEMO_A')!;
  const target: MockAccount = {
    actorId: crypto.randomUUID(),
    username: 'invitee.synthetic',
    displayName: 'Örnek Davetli',
    email: '',
    memberships: [{ tenantCode: tenant.code, permissions: [], membershipOnly: true }],
  };
  api.world.accounts.push(target);
  const membershipId = api.tenantMembership(target, tenant.id).id;
  const services = createServices({ baseUrl: 'http://mock.test' });
  render(
    <App
      services={services}
      history={createMemoryHistory({ initialEntries: [`/admin/users/${membershipId}`] })}
    />,
  );
  return { target, tenant, services };
}

async function signIn() {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'admin.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  await screen.findByTestId('admin-user-detail-page');
  return user;
}

async function selectRuleAuthor(user: ReturnType<typeof userEvent.setup>) {
  const region = await screen.findByTestId('role-grants');
  await user.click(await within(region).findByRole('button', { name: 'Rol atayın' }));
  const dialog = screen.getByRole('dialog', { name: 'Rol atayın' });
  await user.selectOptions(await within(dialog).findByLabelText('Rol'), 'RULE_AUTHOR');
  expect(dialog).toHaveTextContent('Kural taslağı ve test senaryosu');
  expect(dialog).toHaveTextContent('Yönetim paneli');
  await user.selectOptions(within(dialog).getByLabelText('Gerekçe'), 'ONBOARDING');
  await user.click(within(dialog).getByRole('checkbox'));
  return dialog;
}

async function confirmPassword(user: ReturnType<typeof userEvent.setup>) {
  const step = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await user.type(within(step).getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(within(step).getByRole('button', { name: 'Doğrula' }));
}

it('assigns and revokes one supported role from a zero-grant membership', async () => {
  const { services } = fixture();
  const user = await signIn();
  const assign = vi.spyOn(services.ops.admin, 'assignRoleGrant');
  const dialog = await selectRuleAuthor(user);
  await user.click(within(dialog).getByRole('button', { name: 'Rol atayın' }));
  await confirmPassword(user);
  expect(await screen.findByText('Rol atandı.')).toBeInTheDocument();
  expect(assign).toHaveBeenCalledTimes(2);
  expect(assign.mock.calls[0]).toEqual(assign.mock.calls[1]);
  const region = screen.getByTestId('role-grants');
  await user.click(await within(region).findByRole('button', { name: 'Kaldırın' }));
  const revoke = screen.getByRole('dialog', { name: 'Kaldırın' });
  await user.selectOptions(within(revoke).getByLabelText('Gerekçe'), 'DUTY_ENDED');
  await user.click(within(revoke).getByRole('checkbox'));
  await user.click(within(revoke).getByRole('button', { name: 'Kaldırın' }));
  expect(await screen.findByText('Rol ataması kaldırıldı.')).toBeInTheDocument();
  expect(api.roleGrantEvents.map((event) => event.action)).toEqual(['assign', 'revoke']);
}, 20_000);

it('keeps the frozen command after an uncertain result through close and reopen', async () => {
  const { services } = fixture();
  const user = await signIn();
  const original = services.ops.admin.assignRoleGrant;
  const assign = vi
    .spyOn(services.ops.admin, 'assignRoleGrant')
    .mockRejectedValueOnce(
      new ApiError({
        type: 'about:blank',
        title: 'Network error',
        status: 0,
        code: 'NETWORK_ERROR',
        traceId: '',
      }),
    )
    .mockImplementation(original);
  const dialog = await selectRuleAuthor(user);
  await user.click(within(dialog).getByRole('button', { name: 'Rol atayın' }));
  expect(await within(dialog).findByText(/Sonuç belirsiz/)).toBeInTheDocument();
  await user.click(within(dialog).getByRole('button', { name: 'Vazgeç' }));
  await user.click(screen.getByRole('button', { name: 'Aynı isteği yeniden dene' }));
  const reopened = screen.getByRole('dialog', { name: 'Rol atayın' });
  expect(within(reopened).getByLabelText('Rol')).toBeDisabled();
  await user.click(within(reopened).getByRole('button', { name: 'Aynı isteği yeniden dene' }));
  await confirmPassword(user);
  expect(await screen.findByText('Rol atandı.')).toBeInTheDocument();
  expect(assign).toHaveBeenCalledTimes(3);
  expect(assign.mock.calls[0]).toEqual(assign.mock.calls[1]);
  expect(assign.mock.calls[1]).toEqual(assign.mock.calls[2]);
}, 20_000);

it('drops a deferred password confirmation after capability changes A to B to A', async () => {
  const { services } = fixture();
  const user = await signIn();
  const assign = vi.spyOn(services.ops.admin, 'assignRoleGrant');
  const originalStepUp = services.ops.session.stepUp;
  let finish!: () => void;
  const delayed = new Promise<void>((resolve) => {
    finish = resolve;
  });
  vi.spyOn(services.ops.session, 'stepUp').mockImplementationOnce(async (password) => {
    await delayed;
    return originalStepUp(password);
  });
  const dialog = await selectRuleAuthor(user);
  await user.click(within(dialog).getByRole('button', { name: 'Rol atayın' }));
  const step = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await user.type(within(step).getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(within(step).getByRole('button', { name: 'Doğrula' }));
  const active = services.store.getState().activeTenant!;
  act(() => services.store.setState({ activeTenant: { ...active, canManageTenantRoles: false } }));
  act(() => services.store.setState({ activeTenant: { ...active, canManageTenantRoles: true } }));
  await act(async () => {
    finish();
    await delayed;
  });
  await waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
  expect(screen.queryByText('Rol atandı.')).not.toBeInTheDocument();
  expect(api.roleGrantEvents).toHaveLength(0);
}, 20_000);

it('keeps a new command busy when an obsolete response finishes after A to B to A', async () => {
  const { services } = fixture();
  const user = await signIn();
  api.session!.stepUpExpiresAt = new Date(Date.now() + 60_000).toISOString();
  const original = services.ops.admin.assignRoleGrant;
  let finishOld!: (value: Awaited<ReturnType<typeof original>>) => void;
  let finishNew!: () => void;
  const oldResponse = new Promise<Awaited<ReturnType<typeof original>>>((resolve) => {
    finishOld = resolve;
  });
  const newGate = new Promise<void>((resolve) => {
    finishNew = resolve;
  });
  const assign = vi
    .spyOn(services.ops.admin, 'assignRoleGrant')
    .mockImplementationOnce(() => oldResponse)
    .mockImplementationOnce(async (...args) => {
      await newGate;
      return original(...args);
    });
  const first = await selectRuleAuthor(user);
  await user.click(within(first).getByRole('button', { name: 'Rol atayın' }));
  await waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
  const session = services.store.getState().session!;
  act(() => services.store.setState({ session: { ...session, actorId: crypto.randomUUID() } }));
  act(() => services.store.setState({ session }));
  const second = await selectRuleAuthor(user);
  await user.click(within(second).getByRole('button', { name: 'Rol atayın' }));
  await waitFor(() => expect(assign).toHaveBeenCalledTimes(2));
  await act(async () => {
    finishOld({} as Awaited<ReturnType<typeof original>>);
    await oldResponse;
  });
  expect(within(second).getByRole('button', { name: 'Aynı isteği yeniden dene' })).toBeDisabled();
  await act(async () => {
    finishNew();
    await newGate;
  });
  expect(await screen.findByText('Rol atandı.')).toBeInTheDocument();
  expect(api.roleGrantEvents).toHaveLength(1);
}, 20_000);

it('discards a late command from an earlier session of the same actor', async () => {
  const { services } = fixture();
  const user = await signIn();
  api.session!.stepUpExpiresAt = new Date(Date.now() + 60_000).toISOString();
  const original = services.ops.admin.assignRoleGrant;
  let finish!: (value: Awaited<ReturnType<typeof original>>) => void;
  const late = new Promise<Awaited<ReturnType<typeof original>>>((resolve) => {
    finish = resolve;
  });
  const assign = vi
    .spyOn(services.ops.admin, 'assignRoleGrant')
    .mockImplementationOnce(() => late)
    .mockImplementation(original);
  const dialog = await selectRuleAuthor(user);
  await user.click(within(dialog).getByRole('button', { name: 'Rol atayın' }));
  await waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
  const prior = services.store.getState().session!;
  const newToken = `csrf-${crypto.randomUUID()}`;
  api.session!.csrfToken = newToken;
  act(() =>
    services.store.setState({ session: { ...prior, csrfToken: newToken }, csrfToken: newToken }),
  );
  await act(async () => {
    finish({} as Awaited<ReturnType<typeof original>>);
    await late;
  });
  expect(screen.queryByText('Rol atandı.')).not.toBeInTheDocument();
  const fresh = await selectRuleAuthor(user);
  await user.click(within(fresh).getByRole('button', { name: 'Rol atayın' }));
  expect(await screen.findByText('Rol atandı.')).toBeInTheDocument();
  expect(api.roleGrantEvents).toHaveLength(1);
}, 20_000);

it('shows committed success even if the following list refresh fails', async () => {
  const { services } = fixture();
  const user = await signIn();
  const invalidate = services.queryClient.invalidateQueries.bind(services.queryClient);
  vi.spyOn(services.queryClient, 'invalidateQueries')
    .mockImplementationOnce(() => Promise.reject(new Error('refresh failed')))
    .mockImplementation(invalidate);
  const dialog = await selectRuleAuthor(user);
  await user.click(within(dialog).getByRole('button', { name: 'Rol atayın' }));
  await confirmPassword(user);
  expect(await screen.findByText('Rol atandı.')).toBeInTheDocument();
  expect(await screen.findByText(/İşlem kaydedildi; güncel liste yüklenemedi/)).toBeInTheDocument();
  expect(api.roleGrantEvents).toHaveLength(1);
}, 20_000);

it('explains server refusal when any current grant already occupies the membership', async () => {
  const { target, tenant } = fixture();
  target.memberships.push({
    tenantCode: tenant.code,
    permissions: [],
    scopes: [{ type: 'TENANT', id: null }],
  });
  await signIn();
  const region = await screen.findByTestId('role-grants');
  expect(
    await within(region).findByText(/mevcut veya gelecekte başlayacak başka bir erişim var/),
  ).toBeInTheDocument();
  expect(within(region).queryByRole('button', { name: 'Rol atayın' })).not.toBeInTheDocument();
}, 20_000);
