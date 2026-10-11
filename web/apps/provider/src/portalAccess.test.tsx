// @vitest-environment jsdom
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { App } from './App';
import { billingDestination, canOpen, portalLanding, portalNav } from './access';
import { createServices } from './services';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
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

function mount(path: string) {
  const services = createServices({ baseUrl: 'http://mock.test' });
  const history = createMemoryHistory({ initialEntries: [path] });
  render(<App services={services} history={history} />);
  return { services, history };
}

async function login(username: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

function permissions(username: string): string[] {
  return api.world.accounts.find((account) => account.username === username)!.memberships[0]!
    .permissions;
}

describe('provider portal route access', () => {
  it('uses the same permission model for each role landing and its navigation', () => {
    expect(portalLanding(permissions('provider.a'))).toBe('/');
    expect(portalLanding(permissions('billing.a'))).toBe('/billing');
    expect(portalLanding(permissions('reservation.a'))).toBe('/lodging/desk');
    expect(billingDestination(['invoice.read'])).toBe('/billing/invoices');
    expect(billingDestination(['report.read'])).toBe('/billing/statement');
    expect(portalNav(permissions('billing.a')).map((entry) => entry.key)).toEqual([
      'claims',
      'billing',
    ]);
    expect(portalNav(permissions('reservation.a')).map((entry) => entry.key)).toEqual([
      'inventory',
      'desk',
    ]);
    expect(
      canOpen('newRequest', [
        'service_request.create',
        'member.read',
        'catalog.read',
        'eligibility.check',
      ]),
    ).toBe(false);
  });

  it.each([
    ['provider.a', '/', 'newRequest'],
    ['billing.a', '/billing', 'billing'],
    ['reservation.a', '/lodging/desk', 'desk'],
  ] as const)('lands %s on %s with a reachable matching menu', async (username, path, key) => {
    const { history } = mount('/');
    await login(username);
    await waitFor(() => expect(history.location.pathname).toBe(path));
    const nav = screen.getByRole('navigation', { name: /Sağlayıcı/ });
    const links = within(nav).getAllByRole('link');
    expect(links.some((link) => link.getAttribute('href')?.endsWith(path))).toBe(true);
    if (key !== 'newRequest')
      expect(links.some((link) => link.getAttribute('href') === '/')).toBe(false);
  });

  it('blocks a denied direct route before its list hook runs, even with a cached old answer', async () => {
    const { services } = mount('/claims');
    const list = vi.spyOn(services.ops.claims, 'list');
    await login('reservation.a');
    expect(await screen.findByTestId('provider-route-denied')).toBeTruthy();
    expect(list).not.toHaveBeenCalled();
    services.queryClient.setQueryData(
      ['provider', services.store.getState().activeTenant!.tenant.id, 'claims', ''],
      { items: [{ id: 'private-claim' }] },
    );
    expect(screen.queryByText('private-claim')).toBeNull();
  });

  it.each([
    ['billing.a', '/requests/request-private', 'request'],
    ['billing.a', '/cases/case-private', 'case'],
    ['reservation.a', '/claims/claim-private', 'claim'],
    ['reservation.a', '/billing/invoices/invoice-private', 'invoice'],
  ] as const)(
    'denies %s direct %s detail without a protected GET',
    async (username, path, kind) => {
      const { services } = mount(path);
      const read =
        kind === 'request'
          ? vi.spyOn(services.ops.requests, 'get')
          : kind === 'case'
            ? vi.spyOn(services.ops.health, 'getCase')
            : kind === 'claim'
              ? vi.spyOn(services.ops.claims, 'get')
              : vi.spyOn(services.ops.billing, 'getInvoice');
      await login(username);
      expect(await screen.findByTestId('provider-route-denied')).toBeTruthy();
      expect(read).not.toHaveBeenCalled();
    },
  );

  it('blocks an incomplete request desk and clears same-tenant cache when grants change', async () => {
    const staff = api.world.accounts.find((account) => account.username === 'provider.a')!;
    staff.memberships[0]!.permissions = staff.memberships[0]!.permissions.filter(
      (permission) => permission !== 'catalog.read',
    );
    const { services, history } = mount('/');
    const catalog = vi.spyOn(services.ops.catalog, 'listDefinitions');
    await login('provider.a');
    await waitFor(() => expect(history.location.pathname).toBe('/requests'));
    expect(catalog).not.toHaveBeenCalled();
    const tenant = services.store.getState().activeTenant!;
    const oldKey = ['provider', tenant.tenant.id, 'old-private-row'];
    services.queryClient.setQueryData(oldKey, { person: 'old actor' });
    services.store.setState((state) => ({
      activeTenant: {
        ...state.activeTenant!,
        permissions: [...state.activeTenant!.permissions, 'catalog.read'],
      },
    }));
    expect(services.queryClient.getQueryData(oldKey)).toBeUndefined();
    services.queryClient.setQueryData(oldKey, { person: 'old actor' });
    services.store.setState((state) => ({
      session: { ...state.session!, actorId: 'another-actor' },
    }));
    expect(services.queryClient.getQueryData(oldKey)).toBeUndefined();
  });

  it('keeps invoice.read detail readable without claim, document, or invoice.manage grants', async () => {
    const billing = api.world.accounts.find((account) => account.username === 'billing.a')!;
    billing.memberships[0]!.permissions = ['invoice.read'];
    const draft = api.world.invoices.find((invoice) => invoice.status === 'DRAFT')!;
    const { services } = mount(`/billing/invoices/${draft.id}`);
    const claims = vi.spyOn(services.ops.claims, 'get');
    const earnings = vi.spyOn(services.ops.billing, 'earnings');
    const documents = vi.spyOn(services.ops.documents, 'list');
    await login('billing.a');
    expect(await screen.findByTestId('invoice-status')).toBeTruthy();
    expect(screen.queryByTestId('invoice-submit')).toBeNull();
    expect(claims).not.toHaveBeenCalled();
    expect(earnings).not.toHaveBeenCalled();
    expect(documents).not.toHaveBeenCalled();
  });

  it('shows an invoice.read draft batch without batch.create or batch.submit controls', async () => {
    const billing = api.world.accounts.find((account) => account.username === 'billing.a')!;
    billing.memberships[0]!.permissions = ['invoice.read'];
    const draft = api.world.batches.find((batch) => batch.status === 'DRAFT')!;
    mount(`/billing/batches/${draft.id}`);
    await login('billing.a');
    expect(await screen.findByTestId('batch-status')).toBeTruthy();
    expect(screen.queryByTestId('batch-submit')).toBeNull();
    expect(screen.queryByRole('button', { name: /Kaydet/i })).toBeNull();
  });
});
