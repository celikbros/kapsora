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
    username: 'b.ui.target',
    displayName: 'Örnek Hedef',
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
  return { target, membershipId, tenant, services };
}
async function signIn() {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'admin.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  await screen.findByTestId('admin-user-detail-page');
  return user;
}
async function prepare(user: ReturnType<typeof userEvent.setup>) {
  const section = await screen.findByTestId('role-change-proposal');
  await user.click(
    await within(section).findByRole('button', { name: 'Onaya sunulacak rol atayın' }),
  );
  const dialog = screen.getByRole('dialog', { name: 'Onaya sunulacak rol atayın' });
  await user.selectOptions(within(dialog).getByLabelText('Rol'), 'RULE_APPROVER');
  expect(dialog).toHaveTextContent('Kural sürümlerini ikinci yetkili olarak yayımlar.');
  await user.selectOptions(within(dialog).getByLabelText('Gerekçe'), 'DUTY_ASSIGNMENT');
  await user.click(within(dialog).getByRole('checkbox'));
  return dialog;
}
async function confirmPassword(user: ReturnType<typeof userEvent.setup>) {
  const step = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await user.type(within(step).getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(within(step).getByRole('button', { name: 'Doğrula' }));
}

it('creates a pending privileged proposal without granting access and opens the maker detail without decision controls', async () => {
  const { target, services } = fixture();
  const user = await signIn();
  const create = vi.spyOn(services.ops.admin, 'createRoleChange');
  const dialog = await prepare(user);
  await user.click(within(dialog).getByRole('button', { name: 'Talep oluştur' }));
  await confirmPassword(user);
  expect(await screen.findByText('Talep oluşturuldu. Erişim henüz değişmedi.')).toBeInTheDocument();
  expect(create).toHaveBeenCalledTimes(2);
  expect(create.mock.calls[0]).toEqual(create.mock.calls[1]);
  expect(target.memberships.filter((grant) => !grant.membershipOnly)).toHaveLength(0);
  await user.click(screen.getByRole('link', { name: /Talebi aç/ }));
  await screen.findByTestId('role-change-detail-page');
  expect(
    screen.getByText('Talep beklerken erişim başlamaz. Onaylanırsa erişim başlar.'),
  ).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Onayla' })).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Talebi iptal et' })).toBeInTheDocument();
}, 20_000);

it('keeps a frozen request available after password confirmation is cancelled and the dialog closes', async () => {
  const { services } = fixture();
  const user = await signIn();
  const create = vi.spyOn(services.ops.admin, 'createRoleChange');
  const dialog = await prepare(user);
  await user.click(within(dialog).getByRole('button', { name: 'Talep oluştur' }));
  const step = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await user.click(within(step).getByRole('button', { name: 'Vazgeç' }));
  expect(create).toHaveBeenCalledTimes(1); // initial STEP_UP_REQUIRED; no mutation
  expect(await within(dialog).findByText(/İşlem uygulanmadı/)).toBeInTheDocument();
  await user.click(within(dialog).getByRole('button', { name: 'Vazgeç' }));
  await user.click(await screen.findByRole('button', { name: 'Aynı isteği yeniden dene' }));
  const reopened = screen.getByRole('dialog', { name: 'Onaya sunulacak rol atayın' });
  expect(within(reopened).getByLabelText('Rol')).toBeDisabled();
  await user.click(within(reopened).getByRole('button', { name: 'Aynı isteği yeniden dene' }));
  await confirmPassword(user);
  expect(await screen.findByText('Talep oluşturuldu. Erişim henüz değişmedi.')).toBeInTheDocument();
}, 20_000);

it('retries the exact frozen proposal after an uncertain reply and a closed dialog', async () => {
  const { services } = fixture();
  const user = await signIn();
  const original = services.ops.admin.createRoleChange;
  const create = vi
    .spyOn(services.ops.admin, 'createRoleChange')
    .mockImplementationOnce(original)
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
  const dialog = await prepare(user);
  await user.click(within(dialog).getByRole('button', { name: 'Talep oluştur' }));
  await confirmPassword(user);
  expect(await within(dialog).findByText(/Sonuç belirsiz/)).toBeInTheDocument();
  await user.click(within(dialog).getByRole('button', { name: 'Vazgeç' }));
  await user.click(await screen.findByRole('button', { name: 'Aynı isteği yeniden dene' }));
  const reopened = screen.getByRole('dialog', { name: 'Onaya sunulacak rol atayın' });
  expect(within(reopened).getByLabelText('Rol')).toBeDisabled();
  await user.click(within(reopened).getByRole('button', { name: 'Aynı isteği yeniden dene' }));
  expect(await screen.findByText('Talep oluşturuldu. Erişim henüz değişmedi.')).toBeInTheDocument();
  expect(create).toHaveBeenCalledTimes(3);
  expect(create.mock.calls[0]).toEqual(create.mock.calls[1]);
  expect(create.mock.calls[1]).toEqual(create.mock.calls[2]);
  expect(api.roleChangeRequests).toHaveLength(1);
}, 20_000);

it('drops a deferred step-up result after capability changes away and back', async () => {
  const { services } = fixture();
  const user = await signIn();
  const create = vi.spyOn(services.ops.admin, 'createRoleChange');
  const originalStepUp = services.ops.session.stepUp;
  let finish!: () => void;
  const delayed = new Promise<void>((resolve) => {
    finish = resolve;
  });
  vi.spyOn(services.ops.session, 'stepUp').mockImplementationOnce(async (password) => {
    await delayed;
    return originalStepUp(password);
  });
  const dialog = await prepare(user);
  await user.click(within(dialog).getByRole('button', { name: 'Talep oluştur' }));
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
  await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
  expect(api.roleChangeRequests).toHaveLength(0);
  expect(screen.queryByText('Talep oluşturuldu. Erişim henüz değişmedi.')).not.toBeInTheDocument();
}, 20_000);

it('shows the pending queue and lets a distinct checker approve the immutable proposal', async () => {
  const tenant = api.tenantByCode('DEMO_A')!;
  const target: MockAccount = {
    actorId: crypto.randomUUID(),
    username: 'b.ui.target',
    displayName: 'Örnek Hedef',
    email: '',
    memberships: [{ tenantCode: tenant.code, permissions: [], membershipOnly: true }],
  };
  const checker: MockAccount = {
    actorId: crypto.randomUUID(),
    username: 'b.ui.checker',
    displayName: 'Örnek Onaylayıcı',
    email: '',
    memberships: [
      {
        tenantCode: tenant.code,
        roleCode: 'MOCK_MANAGER',
        permissions: ['identity.user.read', 'identity.role.manage'],
        scopes: [{ type: 'TENANT', id: null }],
      },
    ],
  };
  api.world.accounts.push(target, checker);
  const membershipId = api.tenantMembership(target, tenant.id).id;
  api.signIn('admin.a', 'backoffice')!.stepUpExpiresAt = new Date(
    Date.now() + 60_000,
  ).toISOString();
  const headers = { 'X-Kapsora-App': 'backoffice', 'X-Tenant-ID': tenant.id };
  const options = (await (
    await fetch('http://mock.test/api/v1/admin/privileged-role-assignment-options', { headers })
  ).json()) as { items: Array<{ code: string; configurationHash: string }> };
  const eligibility = await fetch(
    `http://mock.test/api/v1/admin/users/${membershipId}/role-change-eligibility`,
    { headers },
  );
  const created = await fetch(
    `http://mock.test/api/v1/admin/users/${membershipId}/role-change-requests`,
    {
      method: 'POST',
      headers: {
        ...headers,
        'X-CSRF-Token': api.session!.csrfToken,
        'If-Match': eligibility.headers.get('ETag')!,
        'Idempotency-Key': crypto.randomUUID(),
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        operation: 'ASSIGN',
        roleCode: 'RULE_APPROVER',
        configurationHash: options.items.find((item) => item.code === 'RULE_APPROVER')!
          .configurationHash,
        reasonCode: 'DUTY_ASSIGNMENT',
      }),
    },
  );
  expect(created.status).toBe(201);
  api.session = null;
  const services = createServices({ baseUrl: 'http://mock.test' });
  render(
    <App
      services={services}
      history={createMemoryHistory({ initialEntries: ['/admin/role-change-requests'] })}
    />,
  );
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), checker.username);
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  const queue = await screen.findByTestId('role-change-queue-page');
  expect(await within(queue).findByText('Örnek Hedef')).toBeInTheDocument();
  await user.click(within(queue).getByRole('link', { name: /Rol atama · Kural Onaylayıcı/ }));
  const detail = await screen.findByTestId('role-change-detail-page');
  expect(
    within(detail).getByText('Talep beklerken erişim başlamaz. Onaylanırsa erişim başlar.'),
  ).toBeInTheDocument();
  await user.click(within(detail).getByText('Tam izin kanıtını göster'));
  expect(within(detail).getByText('rule.publish')).toBeInTheDocument();
  await user.click(within(detail).getByRole('button', { name: 'Onayla' }));
  const confirm = screen.getByRole('dialog', { name: 'Onayla' });
  expect(confirm).toHaveTextContent('Rol atama · Kural Onaylayıcı');
  expect(confirm).toHaveTextContent('Kural sürümlerini ikinci yetkili olarak yayımlar.');
  expect(confirm).toHaveTextContent('Onay verdiğiniz anda bu rolün erişimi başlar.');
  await user.click(within(confirm).getByRole('checkbox'));
  await user.click(within(confirm).getByRole('button', { name: 'Onayla' }));
  await confirmPassword(user);
  expect(
    await screen.findByText('Talep onaylandı; erişim değişikliği uygulandı.'),
  ).toBeInTheDocument();
  expect(target.memberships.filter((grant) => !grant.membershipOnly)).toHaveLength(1);
  expect(api.roleChangeEvents.map((event) => event.action)).toEqual(['create', 'approve']);
}, 20_000);
