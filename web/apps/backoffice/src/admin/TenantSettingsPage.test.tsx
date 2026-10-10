import { ApiError } from '@kapsora/api-client';
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';
import { App } from '../App';
import { createServices, directoryContextKey } from '../api';

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

async function fixture(username = 'admin.a') {
  const services = createServices({ baseUrl: 'http://mock.test' });
  render(
    <App
      services={services}
      history={createMemoryHistory({ initialEntries: ['/admin/tenant-settings'] })}
    />,
  );
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  await screen.findByTestId('tenant-settings-page');
  return { user, services };
}

async function prepare(user: ReturnType<typeof userEvent.setup>) {
  expect(await screen.findAllByText('Varsayılan kullanılıyor')).toHaveLength(2);
  const hold = screen.getByLabelText('Oda tutma süresi (dakika)');
  await user.clear(hold);
  await user.type(hold, '30');
  await user.selectOptions(screen.getByLabelText('Değişiklik gerekçesi'), 'OPERATING_REQUIREMENT');
  await user.click(screen.getByRole('button', { name: 'Değişikliği gözden geçir' }));
  return screen.getByRole('dialog', { name: 'Konaklama sürelerini onaylayın' });
}

it('shows sources and effect, then preserves the exact command through password cancellation', async () => {
  const { user, services } = await fixture();
  const save = vi.spyOn(services.ops.admin, 'setAccommodationTenantSettings');
  expect(screen.getByText(/Önceden kaydedilmiş tutma bitiş zamanı değişmez/)).toBeInTheDocument();
  const confirm = await prepare(user);
  expect(confirm).toHaveTextContent('bekleyen rezervasyon onaylarında');
  expect(confirm).toHaveTextContent('İşletim gereksinimi');
  await user.click(within(confirm).getByRole('button', { name: 'Parola ile onayla' }));
  const step = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await user.click(within(step).getByRole('button', { name: 'Vazgeç' }));
  expect(screen.getByLabelText('Oda tutma süresi (dakika)')).toBeDisabled();
  await user.click(screen.getByRole('button', { name: 'Aynı isteği yeniden dene' }));
  const reopened = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await user.type(within(reopened).getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(within(reopened).getByRole('button', { name: 'Doğrula' }));
  await waitFor(() => expect(api.tenantSettingsEvents).toHaveLength(1));
  expect(save).toHaveBeenCalledTimes(3);
  expect(save.mock.calls[0]).toEqual(save.mock.calls[1]);
  expect(save.mock.calls[1]).toEqual(save.mock.calls[2]);
  expect(screen.getByLabelText('Oda tutma süresi (dakika)')).toHaveValue(30);
}, 25_000);

it('requires explicit reload after a stale version', async () => {
  const { user, services } = await fixture();
  const original = services.ops.admin.setAccommodationTenantSettings;
  vi.spyOn(services.ops.admin, 'setAccommodationTenantSettings')
    .mockRejectedValueOnce(
      new ApiError({
        type: 'about:blank',
        title: 'Version conflict',
        status: 412,
        code: 'TENANT_SETTINGS_VERSION_CONFLICT',
        traceId: '',
      }),
    )
    .mockImplementation(original);
  const confirm = await prepare(user);
  await user.click(within(confirm).getByRole('button', { name: 'Parola ile onayla' }));
  expect(
    await screen.findByText(/Yeni değişiklik hazırlamak için güncel durumu yükleyin/),
  ).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Değişikliği gözden geçir' })).toBeDisabled();
  await user.click(screen.getByRole('button', { name: 'Güncel ayarları yükle' }));
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Değişikliği gözden geçir' })).toBeEnabled(),
  );
}, 25_000);

it('shows a settings-only reader the management entry without editable controls', async () => {
  const tenant = api.tenantByCode('DEMO_A')!;
  api.world.accounts.push({
    actorId: crypto.randomUUID(),
    username: 'settings.reader',
    displayName: 'Ayar Okuyucu',
    email: 'settings.reader@example.invalid',
    memberships: [
      {
        tenantCode: tenant.code,
        permissions: ['platform.tenant_settings.read'],
        scopes: [{ type: 'TENANT', id: null }],
      },
    ],
  });
  await fixture('settings.reader');
  expect(
    await screen.findByText('Bu ayarları görüntüleyebilirsiniz. Değişiklik yapma yetkiniz yok.'),
  ).toBeInTheDocument();
  expect(
    screen.queryByRole('button', { name: 'Değişikliği gözden geçir' }),
  ).not.toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Kurum ayarları' })).toBeInTheDocument();
}, 20_000);

it('retries an uncertain response with the original body, ETag and key', async () => {
  const { user, services } = await fixture();
  const original = services.ops.admin.setAccommodationTenantSettings;
  const save = vi
    .spyOn(services.ops.admin, 'setAccommodationTenantSettings')
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
  const confirm = await prepare(user);
  await user.click(within(confirm).getByRole('button', { name: 'Parola ile onayla' }));
  expect(await screen.findByText(/Kaydetme sonucunu doğrulayamadık/)).toBeInTheDocument();
  expect(screen.getByLabelText('Oda tutma süresi (dakika)')).toBeDisabled();
  await user.click(screen.getByRole('button', { name: 'Aynı isteği yeniden dene' }));
  const step = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await user.type(within(step).getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(within(step).getByRole('button', { name: 'Doğrula' }));
  await waitFor(() => expect(api.tenantSettingsEvents).toHaveLength(1));
  expect(save.mock.calls[0]).toEqual(save.mock.calls[1]);
  expect(save.mock.calls[1]).toEqual(save.mock.calls[2]);
}, 25_000);

it('keeps committed values visible and editing locked when post-save refresh fails', async () => {
  const { user, services } = await fixture();
  const confirm = await prepare(user);
  api.session!.stepUpExpiresAt = new Date(Date.now() + 60_000).toISOString();
  const original = services.ops.admin.getAccommodationTenantSettings;
  vi.spyOn(services.ops.admin, 'getAccommodationTenantSettings')
    .mockRejectedValueOnce(new Error('refresh unavailable'))
    .mockImplementation(original);
  await user.click(within(confirm).getByRole('button', { name: 'Parola ile onayla' }));
  expect(await screen.findByText('Konaklama ayarları kaydedildi.')).toBeInTheDocument();
  expect(screen.getByLabelText('Oda tutma süresi (dakika)')).toHaveValue(30);
  expect(screen.getByLabelText('Oda tutma süresi (dakika)')).toBeDisabled();
  await user.click(screen.getByRole('button', { name: 'Güncel ayarları yükle' }));
  await waitFor(() => expect(screen.getByLabelText('Oda tutma süresi (dakika)')).toBeEnabled());
}, 25_000);

it('discards a frozen command and late read after the tenant context switches away and back', async () => {
  const { user, services } = await fixture();
  const tenantId = services.store.getState().activeTenant!.tenant.id;
  const originalRead = services.ops.admin.getAccommodationTenantSettings;
  const baseline = await originalRead(tenantId);
  let releaseOldRead!: (value: typeof baseline) => void;
  const oldRead = new Promise<typeof baseline>((resolve) => {
    releaseOldRead = resolve;
  });
  const read = vi
    .spyOn(services.ops.admin, 'getAccommodationTenantSettings')
    .mockImplementationOnce(() => oldRead)
    .mockImplementation(originalRead);
  const save = vi.spyOn(services.ops.admin, 'setAccommodationTenantSettings');

  const confirm = await prepare(user);
  await user.click(within(confirm).getByRole('button', { name: 'Parola ile onayla' }));
  await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  expect(save).toHaveBeenCalledTimes(1); // step-up refusal, no write

  const context = directoryContextKey(services.store.getState());
  void services.queryClient.invalidateQueries({
    queryKey: ['admin-tenant-settings', context],
    exact: true,
  });
  await waitFor(() => expect(read).toHaveBeenCalledTimes(1));

  const active = services.store.getState().activeTenant!;
  const away = {
    ...active,
    tenant: api.tenantByCode('DEMO_B')!,
    permissions: [],
    canReadTenantSettings: false,
    canManageTenantSettings: false,
  };
  act(() => services.store.setState({ activeTenant: away }));
  await waitFor(() => expect(screen.queryByTestId('tenant-settings-page')).not.toBeInTheDocument());
  act(() => services.store.setState({ activeTenant: active }));
  expect(await screen.findByLabelText('Oda tutma süresi (dakika)')).toHaveValue(15);
  expect(
    screen.queryByRole('button', { name: 'Aynı isteği yeniden dene' }),
  ).not.toBeInTheDocument();
  expect(screen.queryByRole('dialog', { name: 'Parolanızı doğrulayın' })).not.toBeInTheDocument();
  await act(async () => {
    releaseOldRead({
      ...baseline,
      data: {
        ...baseline.data,
        holdMinutes: { ...baseline.data.holdMinutes, effectiveValue: 999 },
      },
    });
    await Promise.resolve();
  });
  expect(screen.getByLabelText('Oda tutma süresi (dakika)')).toHaveValue(15);
  expect(save).toHaveBeenCalledTimes(1);
  expect(api.tenantSettingsEvents).toHaveLength(0);
}, 25_000);
