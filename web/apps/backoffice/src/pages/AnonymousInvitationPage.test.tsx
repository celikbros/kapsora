import { ApiError } from '@kapsora/api-client';
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';
import { App } from '../App';
import { createServices } from '../api';

const { api, server } = createMockServer();
const PASSWORD = 'new account password 2026';
beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  vi.restoreAllMocks();
  api.reset();
});
afterAll(() => server.close());

async function fixture() {
  const tenant = api.world.tenants[0]!;
  const invitationId = crypto.randomUUID();
  const code = `v1.${tenant.id}.${invitationId}.${'B'.repeat(43)}`;
  const proofDigest = Array.from(
    new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(code))),
    (byte) => byte.toString(16).padStart(2, '0'),
  ).join('');
  api.invitations.rows.set(invitationId, {
    summary: {
      invitationId,
      maskedRecipient: 'n***@***',
      status: 'PENDING',
      createdAt: new Date().toISOString(),
      expiresAt: new Date(Date.now() + 48 * 3_600_000).toISOString(),
      rowVersion: 1,
      deliveryStatus: 'SENT',
    },
    tenantId: tenant.id,
    contactIndex: 'private-contact-index',
    proofDigest,
    acceptedActorId: null,
    acceptedKey: null,
    acceptedOutcome: null,
    terminalAt: null,
  });
  return { tenant, invitationId, code };
}

function mount() {
  const browserOriginFetch: typeof fetch = (input, init) => {
    const request = new Request(input, init);
    if (
      /\/api\/v1\/invitations\/(inspect-new|accept-new|acceptance-receipt)$/.test(
        new URL(request.url).pathname,
      )
    )
      request.headers.set('Origin', new URL(request.url).origin);
    return fetch(request);
  };
  const services = createServices({ baseUrl: 'http://mock.test', fetch: browserOriginFetch });
  const history = createMemoryHistory({ initialEntries: ['/invitation'] });
  render(<App services={services} history={history} />);
  return { services, history };
}

async function preview(code: string) {
  const user = userEvent.setup();
  await screen.findByTestId('invitation-anonymous-page');
  fireEvent.change(screen.getByLabelText('Davet kodu'), { target: { value: code } });
  await user.click(screen.getByRole('button', { name: 'Daveti görüntüle' }));
  return { user, consent: await screen.findByTestId('invitation-new-consent') };
}

async function fillConsent(user: ReturnType<typeof userEvent.setup>, consent: HTMLElement) {
  await user.type(within(consent).getByLabelText('Adınız'), 'New Recipient');
  await user.type(within(consent).getByLabelText('Parola belirleyin'), PASSWORD);
  await user.click(within(consent).getByRole('checkbox'));
}

it('reviews the tenant while anonymous, creates one account, and shows a selectable username without signing in', async () => {
  const { tenant, code } = await fixture();
  const { services, history } = mount();
  const { user, consent } = await preview(code);
  expect(consent).toHaveTextContent(tenant.displayName);
  await fillConsent(user, consent);
  await user.click(within(consent).getByRole('button', { name: 'Hesap oluştur ve katıl' }));
  const result = await screen.findByTestId('invitation-new-result');
  expect((within(result).getByLabelText('Kullanıcı adınız') as HTMLInputElement).value).toMatch(
    /^k_/,
  );
  expect(services.store.getState().status).toBe('anonymous');
  expect(api.session).toBeNull();
  expect(history.location.href).not.toContain(code);
  expect(JSON.stringify(localStorage)).not.toContain(code);
  expect(JSON.stringify(sessionStorage)).not.toContain(PASSWORD);
  expect(within(result).getByRole('link', { name: 'Giriş yap' })).toHaveAttribute(
    'href',
    '/auth/login',
  );
});

it('re-enters the password and retries the same command after a lost acceptance response', async () => {
  const { code } = await fixture();
  const { services } = mount();
  const { user, consent } = await preview(code);
  const original = services.ops.admin.acceptNewInvitation;
  const accept = vi
    .spyOn(services.ops.admin, 'acceptNewInvitation')
    .mockImplementationOnce(async (...args) => {
      await original(...args);
      throw new ApiError({
        type: 'about:blank',
        title: 'Network error',
        status: 0,
        code: 'NETWORK_ERROR',
        traceId: '',
      });
    });
  await fillConsent(user, consent);
  await user.click(within(consent).getByRole('button', { name: 'Hesap oluştur ve katıl' }));
  expect(await within(consent).findByText(/Sonuç belirsiz/)).toBeInTheDocument();
  expect(within(consent).getByLabelText('Parola belirleyin')).toHaveValue('');
  await user.type(within(consent).getByLabelText('Parola belirleyin'), PASSWORD);
  await user.click(within(consent).getByRole('button', { name: 'Aynı isteği yeniden dene' }));
  expect(await screen.findByTestId('invitation-new-result')).toBeInTheDocument();
  expect(accept).toHaveBeenCalledTimes(2);
  expect(accept.mock.calls[0]).toEqual(accept.mock.calls[1]);
  expect(api.world.accounts.filter((account) => account.username.startsWith('k_'))).toHaveLength(1);
});

it('recovers a lost username with code and current password without a successful preview', async () => {
  const { code } = await fixture();
  const { history } = mount();
  const { user, consent } = await preview(code);
  await fillConsent(user, consent);
  await user.click(within(consent).getByRole('button', { name: 'Hesap oluştur ve katıl' }));
  const handle = ((await screen.findByLabelText('Kullanıcı adınız')) as HTMLInputElement).value;
  await user.click(screen.getByRole('link', { name: 'Giriş yap' }));
  // A separate anonymous visit has no previous preview or accepted outcome in memory.
  await history.push('/invitation');
  await screen.findByTestId('invitation-anonymous-page');
  await user.click(screen.getByRole('button', { name: 'Kullanıcı adımı yeniden göster' }));
  const form = await screen.findByTestId('invitation-receipt-form');
  fireEvent.change(within(form).getByLabelText('Davet kodu'), { target: { value: code } });
  await user.type(within(form).getByLabelText('Güncel parola'), 'wrong password 2026');
  await user.click(within(form).getByRole('button', { name: 'Kullanıcı adımı yeniden göster' }));
  expect(await screen.findByRole('alert')).toHaveTextContent(/kullanılamıyor/);
  expect(within(form).getByLabelText('Güncel parola')).toHaveValue('');
  await user.type(within(form).getByLabelText('Güncel parola'), PASSWORD);
  await user.click(within(form).getByRole('button', { name: 'Kullanıcı adımı yeniden göster' }));
  expect(await screen.findByLabelText('Kullanıcı adınız')).toHaveValue(handle);
});

it('does not restore a late inspect result after the code changes A → B → A', async () => {
  const { code } = await fixture();
  const { services } = mount();
  await screen.findByTestId('invitation-anonymous-page');
  const original = services.ops.admin.inspectNewInvitation;
  let finish!: (result: Awaited<ReturnType<typeof original>>) => void;
  const deferred = new Promise<Awaited<ReturnType<typeof original>>>((resolve) => {
    finish = resolve;
  });
  vi.spyOn(services.ops.admin, 'inspectNewInvitation').mockImplementationOnce(() => deferred);
  const user = userEvent.setup();
  const input = screen.getByLabelText('Davet kodu');
  fireEvent.change(input, { target: { value: code } });
  await user.click(screen.getByRole('button', { name: 'Daveti görüntüle' }));
  fireEvent.change(input, { target: { value: 'different-code' } });
  fireEvent.change(input, { target: { value: code } });
  await act(async () =>
    finish({
      tenantDisplayName: 'Late tenant',
      invitationStatus: 'PENDING',
      expiresAt: new Date(Date.now() + 1000).toISOString(),
    }),
  );
  await waitFor(() =>
    expect(screen.queryByTestId('invitation-new-consent')).not.toBeInTheDocument(),
  );
});

it('clears proof and password when changing to private username recovery', async () => {
  const { code } = await fixture();
  mount();
  const { user, consent } = await preview(code);
  await fillConsent(user, consent);
  await user.click(screen.getByRole('button', { name: 'Kullanıcı adımı yeniden göster' }));
  const form = await screen.findByTestId('invitation-receipt-form');
  expect(within(form).getByLabelText('Davet kodu')).toHaveValue('');
  expect(within(form).getByLabelText('Güncel parola')).toHaveValue('');
  expect(screen.queryByTestId('invitation-new-consent')).not.toBeInTheDocument();
});

it('counts Unicode characters, not UTF-16 units, before enabling account creation', async () => {
  const { code } = await fixture();
  mount();
  const { user, consent } = await preview(code);
  await user.type(within(consent).getByLabelText('Adınız'), 'New Recipient');
  await user.click(within(consent).getByRole('checkbox'));
  const password = within(consent).getByLabelText('Parola belirleyin');
  const create = within(consent).getByRole('button', { name: 'Hesap oluştur ve katıl' });
  fireEvent.change(password, { target: { value: '😀'.repeat(6) } });
  expect(create).toBeDisabled();
  fireEvent.change(password, { target: { value: '😀'.repeat(12) } });
  expect(create).toBeEnabled();
});
