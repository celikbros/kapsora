import { ApiError } from '@kapsora/api-client';
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';
import { App } from './App';
import { createServices } from './services';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  vi.restoreAllMocks();
  api.reset();
});
afterAll(() => server.close());
function fixture() {
  const account = api.world.accounts.find((a) => a.username === 'provider.a')!;
  const org = account.memberships[0]!.scopes!.find((s) => s.type === 'ORGANIZATION')!.id;
  const row = api.world.serviceRequests.find(
    (r) => r.providerOrganizationId === org && r.status === 'PENDING_REVIEW',
  )!;
  return { account, row };
}
async function mount(id: string) {
  const services = createServices({ baseUrl: 'http://mock.test' });
  render(
    <App
      services={services}
      history={createMemoryHistory({ initialEntries: [`/requests/${id}`] })}
    />,
  );
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'provider.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  await screen.findByRole('heading', { name: fixture().row.reference });
  return { user, services };
}

it('requires a reason, preserves a committed-but-lost attempt across close and replays it once', async () => {
  const { row } = fixture();
  const { user, services } = await mount(row.id);
  const original = services.ops.requests.cancel.bind(services.ops.requests);
  const cancel = vi
    .spyOn(services.ops.requests, 'cancel')
    .mockImplementationOnce(async (...args) => {
      await original(...args);
      throw new Error('response lost after commit');
    });
  await user.click(screen.getByRole('button', { name: 'Talebi iptal et' }));
  let dialog = await screen.findByRole('dialog');
  expect(within(dialog).getByRole('button', { name: 'Talebi iptal et' })).toBeDisabled();
  await user.selectOptions(within(dialog).getByLabelText(/İptal gerekçesi/), 'INPUT_ERROR');
  await user.type(within(dialog).getByLabelText('Açıklama'), 'Yanlış hizmet seçildi.');
  await user.click(within(dialog).getByRole('button', { name: 'Talebi iptal et' }));
  await within(dialog).findByRole('alert');
  const committedVersion = row.rowVersion;
  expect(row.status).toBe('CANCELLED');
  expect(within(dialog).getByLabelText(/İptal gerekçesi/)).toBeDisabled();
  await user.click(within(dialog).getByRole('button', { name: 'Kapat' }));
  await user.click(screen.getByRole('button', { name: 'Talebi iptal et' }));
  dialog = await screen.findByRole('dialog');
  await user.click(within(dialog).getByRole('button', { name: 'Yeniden dene' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(cancel).toHaveBeenCalledTimes(2);
  expect(cancel.mock.calls[1]).toEqual(cancel.mock.calls[0]);
  expect(row.rowVersion).toBe(committedVersion);
  const args = cancel.mock.calls[0]!;
  await expect(
    original(args[0], args[1], args[2], { reasonCode: 'DUPLICATE_REQUEST' }, args[4]),
  ).rejects.toMatchObject({ problem: { code: 'IDEMPOTENCY_KEY_REUSED' } });

  expect(screen.queryByRole('button', { name: 'Talebi iptal et' })).toBeNull();
  expect(screen.queryByTestId('document-upload-form')).toBeNull();
});

it('blocks stale cancellation and requires a successful explicit reload before another command', async () => {
  const { row } = fixture();
  const { user, services } = await mount(row.id);
  const cancel = vi.spyOn(services.ops.requests, 'cancel');
  await user.click(screen.getByRole('button', { name: 'Talebi iptal et' }));
  const dialog = await screen.findByRole('dialog');
  await user.selectOptions(within(dialog).getByLabelText(/İptal gerekçesi/), 'PROVIDER_WITHDRAWN');
  row.rowVersion += 1;
  await user.click(within(dialog).getByRole('button', { name: 'Talebi iptal et' }));
  await within(dialog).findByRole('alert');
  expect(row.status).toBe('PENDING_REVIEW');
  expect(within(dialog).getByRole('button', { name: 'Talebi iptal et' })).toBeDisabled();
  vi.spyOn(services.ops.requests, 'get').mockRejectedValueOnce(new Error('reload unavailable'));
  await user.click(within(dialog).getByRole('button', { name: 'Güncel kaydı yükle' }));
  await waitFor(() => expect(within(dialog).getAllByRole('alert')).toHaveLength(2));
  expect(within(dialog).getByRole('button', { name: 'Talebi iptal et' })).toBeDisabled();
  await user.click(within(dialog).getByRole('button', { name: 'Güncel kaydı yükle' }));
  await waitFor(() =>
    expect(within(dialog).getByRole('button', { name: 'Talebi iptal et' })).toBeEnabled(),
  );
  expect(cancel).toHaveBeenCalledOnce();
  await user.click(within(dialog).getByRole('button', { name: 'Talebi iptal et' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(cancel.mock.calls[1]?.[4]).not.toBe(cancel.mock.calls[0]?.[4]);
  expect(cancel.mock.calls[1]?.[2]).not.toBe(cancel.mock.calls[0]?.[2]);
});

it.each(['APPROVED', 'PARTIALLY_APPROVED', 'REJECTED', 'CANCELLED', 'EXPIRED', 'CLOSED'] as const)(
  'does not offer request cancellation for %s',
  async (status) => {
    const { row } = fixture();
    const reference = row.reference;
    row.status = status;
    // mount's title lookup must not depend on the fixture's former status.
    const services = createServices({ baseUrl: 'http://mock.test' });
    render(
      <App
        services={services}
        history={createMemoryHistory({ initialEntries: [`/requests/${row.id}`] })}
      />,
    );
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'provider.a');
    await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
    await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
    await screen.findByRole('heading', { name: reference });
    expect(screen.queryByRole('button', { name: 'Talebi iptal et' })).toBeNull();
  },
);

it('hides cancellation without its separate permission', async () => {
  const { row, account } = fixture();
  account.memberships[0]!.permissions = account.memberships[0]!.permissions.filter(
    (p) => p !== 'service_request.cancel',
  );
  await mount(row.id);
  expect(screen.queryByRole('button', { name: 'Talebi iptal et' })).toBeNull();
});

it.each([
  [408, 'REQUEST_TIMEOUT'],
  [429, 'RATE_LIMITED'],
  [409, 'IDEMPOTENCY_IN_PROGRESS'],
] as const)(
  'retries transient %s with the same command instead of changing the cancellation',
  async (status, code) => {
    const { row } = fixture();
    const { user, services } = await mount(row.id);
    const cancel = vi.spyOn(services.ops.requests, 'cancel').mockRejectedValueOnce(
      new ApiError({
        type: 'about:blank',
        title: 'Transient failure',
        status,
        code,
        traceId: '',
      }),
    );
    await user.click(screen.getByRole('button', { name: 'Talebi iptal et' }));
    const dialog = await screen.findByRole('dialog');
    await user.selectOptions(within(dialog).getByLabelText(/İptal gerekçesi/), 'INPUT_ERROR');
    await user.click(within(dialog).getByRole('button', { name: 'Talebi iptal et' }));
    await within(dialog).findByRole('alert');
    await user.click(within(dialog).getByRole('button', { name: 'Yeniden dene' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(cancel.mock.calls[1]).toEqual(cancel.mock.calls[0]);
    expect(row.status).toBe('CANCELLED');
  },
);
