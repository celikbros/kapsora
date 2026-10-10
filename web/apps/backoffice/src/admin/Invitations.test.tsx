import { ApiError } from '@kapsora/api-client';
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';
import { App } from '../App';
import { createServices } from '../api';

const { api, server } = createMockServer();
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => api.reset());
afterAll(() => server.close());

function mount(path: string) {
  const services = createServices({ baseUrl: BASE });
  const history = createMemoryHistory({ initialEntries: [path] });
  render(<App services={services} history={history} />);
  activeHistory = history;
  return { services, history };
}

let activeHistory: ReturnType<typeof createMemoryHistory>;

async function login(username: string) {
  const user = userEvent.setup();
  if (activeHistory.location.pathname === '/invitation') {
    await screen.findByTestId('invitation-anonymous-page');
    await user.click(screen.getByRole('link', { name: /Hesabım var/ }));
  }
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

async function invitationFor(tenantIndex: number) {
  const tenant = api.world.tenants[tenantIndex]!;
  const id = crypto.randomUUID();
  const code = `v1.${tenant.id}.${id}.${'A'.repeat(43)}`;
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(code));
  const proofDigest = Array.from(new Uint8Array(digest), (byte) =>
    byte.toString(16).padStart(2, '0'),
  ).join('');
  api.invitations.rows.set(id, {
    summary: {
      invitationId: id,
      maskedRecipient: 'm***@***',
      status: 'PENDING',
      createdAt: new Date().toISOString(),
      expiresAt: new Date(Date.now() + 48 * 3_600_000).toISOString(),
      rowVersion: 1,
      deliveryStatus: 'SENT',
    },
    tenantId: tenant.id,
    contactIndex: 'test-contact',
    proofDigest,
    acceptedActorId: null,
    acceptedKey: null,
    acceptedOutcome: null,
    terminalAt: null,
  });
  return { tenant, id, code };
}

it('lets a manager create and cancel an invitation without exposing the address or code', async () => {
  const { services } = mount('/admin/invitations');
  const user = await login('admin.a');
  expect(await screen.findByTestId('admin-invitations-page')).toBeInTheDocument();
  const create = vi.spyOn(services.ops.admin, 'createInvitation');
  await user.type(screen.getByLabelText(/E-posta adresi/), 'recipient@example.test');
  await user.click(screen.getByRole('button', { name: 'Davet oluştur' }));
  const stepUp = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await user.type(within(stepUp).getByLabelText(/^Parola/), PASSWORD);
  await user.click(within(stepUp).getByRole('button', { name: 'Doğrula' }));
  const detail = await screen.findByTestId('admin-invitation-detail-page');
  expect(create).toHaveBeenCalledTimes(2);
  expect(create.mock.calls[0]).toEqual(create.mock.calls[1]);
  expect(detail).toHaveTextContent('r***@***');
  expect(detail).not.toHaveTextContent('recipient@example.test');
  expect(detail).not.toHaveTextContent('v1.');
  await user.click(within(detail).getByRole('button', { name: 'Daveti iptal et' }));
  const dialog = await screen.findByRole('dialog', { name: 'Daveti iptal et' });
  await user.click(within(dialog).getByRole('checkbox'));
  await user.click(within(dialog).getByRole('button', { name: 'Daveti iptal et' }));
  await waitFor(() => expect(detail).toHaveTextContent('İptal edildi'));
  expect(api.invitations.events.map((event) => event.action)).toEqual(['create', 'cancel']);
});

it('treats disabled invitation delivery as a definite refusal', async () => {
  const { services } = mount('/admin/invitations');
  const user = await login('admin.a');
  await screen.findByTestId('admin-invitations-page');
  const create = vi.spyOn(services.ops.admin, 'createInvitation').mockRejectedValueOnce(
    new ApiError({
      type: 'about:blank',
      title: 'Service unavailable',
      status: 503,
      code: 'INVITATION_DELIVERY_DISABLED',
      traceId: '',
    }),
  );
  await user.type(screen.getByLabelText(/E-posta adresi/), 'recipient@example.test');
  await user.click(screen.getByRole('button', { name: 'Davet oluştur' }));
  expect(await screen.findByText(/Kurum davet gönderimi etkin değil/)).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Davet oluştur' })).toBeDisabled();
  expect(screen.queryByText(/Sonuç belirsiz/)).not.toBeInTheDocument();
  expect(create).toHaveBeenCalledTimes(1);
});

it('labels a retained invitation whose masked recipient was purged', async () => {
  const { id } = await invitationFor(0);
  api.invitations.rows.get(id)!.summary.maskedRecipient = '';
  mount(`/admin/invitations/${id}`);
  await login('admin.a');
  expect(await screen.findByRole('heading', { name: 'Alıcı bilgisi silindi' })).toBeInTheDocument();
});

it('keeps the exact cancel command after an uncertain result and reloads after stale ETag', async () => {
  const { tenant, id } = await invitationFor(0);
  const { services } = mount(`/admin/invitations/${id}`);
  const user = await login('admin.a');
  const detail = await screen.findByTestId('admin-invitation-detail-page');
  expect(services.store.getState().activeTenant?.tenant.id).toBe(tenant.id);
  const cancel = vi
    .spyOn(services.ops.admin, 'cancelInvitation')
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
        title: 'Stale',
        status: 412,
        code: 'ETAG_MISMATCH',
        traceId: '',
      }),
    );
  await user.click(within(detail).getByRole('button', { name: 'Daveti iptal et' }));
  let dialog = await screen.findByRole('dialog', { name: 'Daveti iptal et' });
  await user.click(within(dialog).getByRole('checkbox'));
  await user.click(within(dialog).getByRole('button', { name: 'Daveti iptal et' }));
  expect(await within(dialog).findByText(/Sonuç belirsiz/)).toBeInTheDocument();
  await user.click(within(dialog).getByRole('button', { name: 'Vazgeç' }));
  await user.click(within(detail).getByRole('button', { name: 'Daveti iptal et' }));
  dialog = await screen.findByRole('dialog', { name: 'Daveti iptal et' });
  await user.click(within(dialog).getByRole('button', { name: 'Aynı isteği yeniden dene' }));
  expect(cancel).toHaveBeenCalledTimes(2);
  expect(cancel.mock.calls[0]).toEqual(cancel.mock.calls[1]);
  expect(
    await within(dialog).findByRole('button', { name: 'Daveti yeniden yükle' }),
  ).toBeInTheDocument();
  await user.click(within(dialog).getByRole('button', { name: 'Daveti yeniden yükle' }));
  expect(await within(dialog).findByRole('button', { name: 'Daveti iptal et' })).toBeDisabled();
});

it('lets an account without backoffice grants reach the fixed invitation page and join without a tenant switch', async () => {
  const { tenant, id, code } = await invitationFor(1);
  const { services, history } = mount('/invitation');
  const user = await login('member.a');
  expect(history.location.pathname).toBe('/invitation');
  expect(await screen.findByTestId('invitation-page')).toBeInTheDocument();
  expect(services.store.getState().activeTenant?.tenant.id).not.toBe(tenant.id);
  await user.type(screen.getByLabelText('Davet kodu'), code);
  await user.click(screen.getByRole('button', { name: 'Daveti görüntüle' }));
  const consent = await screen.findByTestId('invitation-consent');
  expect(consent).toHaveTextContent(tenant.displayName);
  expect(within(consent).getByRole('button', { name: 'Kuruma katıl' })).toBeDisabled();
  await user.click(within(consent).getByRole('checkbox'));
  await user.click(within(consent).getByRole('button', { name: 'Kuruma katıl' }));
  expect(
    await screen.findByText('Üyeliğiniz oluşturuldu. Erişim yetkilerinizin atanması bekleniyor.'),
  ).toBeInTheDocument();
  expect(api.invitations.rows.get(id)?.summary.status).toBe('ACCEPTED');
  expect(
    api.world.accounts
      .find((account) => account.username === 'member.a')
      ?.memberships.some(
        (grant) => grant.tenantCode === tenant.code && grant.permissions.length === 0,
      ),
  ).toBe(true);
  expect(services.store.getState().activeTenant?.tenant.id).not.toBe(tenant.id);
  expect(history.location.href).not.toContain(code);
  expect(JSON.stringify(localStorage)).not.toContain(code);
  expect(JSON.stringify(sessionStorage)).not.toContain(code);
  await services.store.logout();
  await history.push('/auth/login');
  await login('member.a');
  expect(await screen.findByText('Yetki ataması bekleyen kurumlar')).toBeInTheDocument();
  expect(screen.getByText(tenant.displayName)).toBeInTheDocument();
  expect(history.location.pathname).toBe('/auth/apps');
});

it('discards inspected proof and blocks late consent after the account context changes', async () => {
  const { code } = await invitationFor(1);
  const { services } = mount('/invitation');
  const user = await login('member.a');
  await screen.findByTestId('invitation-page');
  await user.type(screen.getByLabelText('Davet kodu'), code);
  await user.click(screen.getByRole('button', { name: 'Daveti görüntüle' }));
  expect(await screen.findByTestId('invitation-consent')).toBeInTheDocument();
  const accept = vi.spyOn(services.ops.admin, 'acceptExistingInvitation');
  const current = services.store.getState().session!;
  act(() => services.store.setState({ session: { ...current, actorId: crypto.randomUUID() } }));
  await waitFor(() => expect(screen.queryByTestId('invitation-consent')).not.toBeInTheDocument());
  expect(screen.getByLabelText('Davet kodu')).toHaveValue('');
  expect(accept).not.toHaveBeenCalled();
});

it('unlocks the new account view while an old acceptance response is still pending', async () => {
  const { code, tenant } = await invitationFor(1);
  const { services } = mount('/invitation');
  const user = await login('member.a');
  await screen.findByTestId('invitation-page');
  await user.type(screen.getByLabelText('Davet kodu'), code);
  await user.click(screen.getByRole('button', { name: 'Daveti görüntüle' }));
  const consent = await screen.findByTestId('invitation-consent');
  const original = services.ops.admin.acceptExistingInvitation;
  let finish!: (result: Awaited<ReturnType<typeof original>>) => void;
  const pending = new Promise<Awaited<ReturnType<typeof original>>>((resolve) => {
    finish = resolve;
  });
  const accept = vi
    .spyOn(services.ops.admin, 'acceptExistingInvitation')
    .mockImplementationOnce(() => pending);
  await user.click(within(consent).getByRole('checkbox'));
  await user.click(within(consent).getByRole('button', { name: 'Kuruma katıl' }));
  await waitFor(() => expect(accept).toHaveBeenCalledTimes(1));
  const old = services.store.getState().session!;
  act(() => services.store.setState({ session: { ...old, actorId: crypto.randomUUID() } }));
  await waitFor(() => expect(screen.queryByTestId('invitation-consent')).not.toBeInTheDocument());
  expect(screen.getByLabelText('Davet kodu')).toBeEnabled();
  expect(screen.getByLabelText('Davet kodu')).toHaveValue('');
  finish({
    tenantId: tenant.id,
    tenantDisplayName: tenant.displayName,
    membershipId: crypto.randomUUID(),
    membershipStatus: 'ACTIVE',
    accessPending: true,
  });
  expect(screen.queryByText(/Üyeliğiniz oluşturuldu/)).not.toBeInTheDocument();
});

it('explains an invitation already accepted by this account without a second command', async () => {
  const { id, code } = await invitationFor(1);
  const member = api.world.accounts.find((account) => account.username === 'member.a')!;
  const row = api.invitations.rows.get(id)!;
  row.summary.status = 'ACCEPTED';
  row.acceptedActorId = member.actorId;
  const { services } = mount('/invitation');
  const user = await login('member.a');
  await screen.findByTestId('invitation-page');
  const accept = vi.spyOn(services.ops.admin, 'acceptExistingInvitation');
  await user.type(screen.getByLabelText('Davet kodu'), code);
  await user.click(screen.getByRole('button', { name: 'Daveti görüntüle' }));
  expect(await screen.findByText(/Bu davet bu hesap tarafından kabul edilmiş/)).toBeInTheDocument();
  expect(screen.queryByTestId('invitation-consent')).not.toBeInTheDocument();
  expect(accept).not.toHaveBeenCalled();
});
