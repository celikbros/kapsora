import { createMockServer } from '@kapsora/api-client/mocks/node';
import { SessionProvider } from '@kapsora/auth';
import { initI18n } from '@kapsora/i18n';
import { ToastProvider } from '@kapsora/ui';
import { QueryClientProvider } from '@tanstack/react-query';
import {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { ServicesProvider, createServices } from '../api';
import { ProviderCreatePage } from './ProviderCreatePage';
import { ProviderDetailPage } from './ProviderDetailPage';
import { ProviderListPage } from './ProviderListPage';
import { providerListSearch } from './routes';

const { api, server } = createMockServer({ organizationsPerTenant: 12 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => api.reset());
afterAll(() => server.close());

/**
 * The provider routes are wired into `router.tsx` by another engineer, so the tests build
 * the same three routes over a memory history and render the pages through them.
 */
function buildRouter(path: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const listRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/providers',
    validateSearch: providerListSearch,
    component: ProviderListPage,
  });
  const createPageRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/providers/new',
    component: ProviderCreatePage,
  });
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/providers/$providerId',
    component: ProviderDetailPage,
  });
  return createRouter({
    routeTree: rootRoute.addChildren([listRoute, createPageRoute, detailRoute]),
    history: createMemoryHistory({ initialEntries: [path] }),
    defaultPreload: false,
  });
}

async function mount(path: string, username = 'admin.a') {
  const services = createServices({ baseUrl: BASE });
  await services.store.login(username, PASSWORD);
  const router = buildRouter(path);
  render(
    <ServicesProvider services={services}>
      <QueryClientProvider client={services.queryClient}>
        <SessionProvider store={services.store}>
          <ToastProvider>
            <RouterProvider router={router} />
          </ToastProvider>
        </SessionProvider>
      </QueryClientProvider>
    </ServicesProvider>,
  );
  return { services, router, user: userEvent.setup() };
}

/** The seeded provider profile of DEMO_A. */
function seededProvider() {
  const tenantId = api.world.tenants[0]!.id;
  const provider = api.world.providers.find((p) => p.tenantId === tenantId);
  if (!provider) throw new Error('fixture: no provider profile');
  const organization = api.world.relationships.find((r) => r.id === provider.tenantOrganizationId);
  if (!organization) throw new Error('fixture: provider has no relationship');
  const name = api.world.organizations.get(organization.organizationId)?.displayName ?? '';
  return { provider, name };
}

describe('provider list', () => {
  it('lists the network, filters by type and opens one provider', async () => {
    const { provider, name } = seededProvider();
    const { user, router } = await mount('/providers');

    const table = await screen.findByTestId('provider-table');
    expect(within(table).getByText(name)).toBeInTheDocument();

    // A type nobody in the fixture has empties the list and shows the empty state.
    await user.selectOptions(screen.getByLabelText('Sağlayıcı türü'), 'Eczane');
    await waitFor(() => expect(router.state.location.searchStr).toContain('PHARMACY'));
    expect(await screen.findByText('Sağlayıcı bulunamadı.')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Temizle' }));
    await user.click(await screen.findByRole('link', { name }));

    await screen.findByRole('tab', { name: 'Profil' });
    expect(router.state.location.pathname).toBe(`/providers/${provider.id}`);
  });

  it('hides every writing control from an operator who may only read', async () => {
    await mount('/providers', 'reviewer.a');
    await screen.findByTestId('provider-table');
    expect(screen.queryByRole('link', { name: 'Yeni sağlayıcı' })).not.toBeInTheDocument();
  });
});

describe('provider detail', () => {
  it('patches the profile with its ETag', async () => {
    const { provider } = seededProvider();
    const { user } = await mount(`/providers/${provider.id}`);

    const form = await screen.findByTestId('provider-profile-form');
    const tier = within(form).getByLabelText('Ağ kademesi');
    await user.clear(tier);
    await user.type(tier, 'B');
    await user.click(within(form).getByRole('button', { name: 'Kaydet' }));

    expect(await screen.findByText('Sağlayıcı güncellendi.')).toBeInTheDocument();
    expect(api.world.providers.find((p) => p.id === provider.id)?.networkTier).toBe('B');
  });

  it('moves the status through the explicit commands, never a form field', async () => {
    const { provider } = seededProvider();
    const { user } = await mount(`/providers/${provider.id}`);
    await screen.findByRole('tab', { name: 'Profil' });

    // An ACTIVE provider offers suspend and terminate, and nothing offers "activate".
    expect(screen.queryByRole('button', { name: 'Aktifleştir' })).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Durum')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Askıya al' }));
    const dialog = await screen.findByRole('dialog', { name: 'Sağlayıcıyı askıya al' });
    await user.type(within(dialog).getByLabelText(/Sebep/), 'CONTRACT_REVIEW');
    await user.click(within(dialog).getByRole('button', { name: 'Askıya al' }));

    expect(await screen.findByText('Sağlayıcı askıya alındı.')).toBeInTheDocument();
    await waitFor(() =>
      expect(api.world.providers.find((p) => p.id === provider.id)?.status).toBe('SUSPENDED'),
    );
    expect(await screen.findByRole('button', { name: 'Aktifleştir' })).toBeInTheDocument();
  });

  it('says that terminating cannot be undone', async () => {
    const { provider } = seededProvider();
    const { user } = await mount(`/providers/${provider.id}`);
    await screen.findByRole('tab', { name: 'Profil' });

    await user.click(screen.getByRole('button', { name: 'Sonlandır' }));
    const dialog = await screen.findByRole('dialog', { name: 'Sağlayıcıyı sonlandır' });
    expect(within(dialog).getByText(/geri alınamaz/)).toBeInTheDocument();
  });

  it('lists the locations of the provider', async () => {
    const { provider } = seededProvider();
    const { user } = await mount(`/providers/${provider.id}`);
    await user.click(await screen.findByRole('tab', { name: 'Lokasyonlar' }));

    const table = await screen.findByTestId('location-table');
    expect(within(table).getAllByRole('row').length).toBeGreaterThan(1);
    expect(within(table).getByText('Kadıköy Tıp Merkezi')).toBeInTheDocument();
  });
});

describe('capabilities', () => {
  it('refuses a second capability that overlaps the first for the same service', async () => {
    const { provider } = seededProvider();
    const { user } = await mount(`/providers/${provider.id}`);
    await user.click(await screen.findByRole('tab', { name: 'Yetkinlikler' }));
    await screen.findByTestId('capability-form');

    await user.click(screen.getByRole('button', { name: 'Satır ekle' }));

    // The new row names the service the second seeded row already covers, over a period
    // that overlaps it, so the server answers 409.
    const services = screen.getAllByLabelText(/^Hizmet/);
    const added = services[services.length - 1]!;
    await user.selectOptions(added, 'MRI_SCAN · MR Çekimi');
    const starts = screen.getAllByLabelText(/^Başlangıç/);
    await user.type(starts[starts.length - 1]!, '2026-06-01');

    await user.click(screen.getByRole('button', { name: 'Kaydet' }));
    expect(
      await screen.findByText('Aynı hizmet için çakışan bir yetkinlik dönemi var.'),
    ).toBeInTheDocument();
  });

  it('saves the set when the periods do not collide', async () => {
    const { provider } = seededProvider();
    const { user } = await mount(`/providers/${provider.id}`);
    await user.click(await screen.findByRole('tab', { name: 'Yetkinlikler' }));
    await screen.findByTestId('capability-form');

    const before = api.world.providerCapabilities.length;
    await user.click(screen.getAllByRole('button', { name: 'Kaldır' })[0]!);
    await user.click(screen.getByRole('button', { name: 'Kaydet' }));

    expect(await screen.findByText('Yetkinlikler kaydedildi.')).toBeInTheDocument();
    await waitFor(() => expect(api.world.providerCapabilities.length).toBe(before - 1));
  });
});

describe('practitioners', () => {
  it('lists them with the registration masked', async () => {
    const { provider } = seededProvider();
    const { user } = await mount(`/providers/${provider.id}`);
    await user.click(await screen.findByRole('tab', { name: 'Uygulayıcılar' }));

    const table = await screen.findByTestId('practitioner-table');
    expect(within(table).getByText('Elif Şahin')).toBeInTheDocument();
    expect(within(table).getAllByText(/\*{3,}/).length).toBeGreaterThan(0);
  });

  it('asks for the password before a registration search and never shows the number', async () => {
    const { provider } = seededProvider();
    const registration = api.world.practitioners[0]!.registrationNumber;
    const { user, router } = await mount(`/providers/${provider.id}`);
    await user.click(await screen.findByRole('tab', { name: 'Uygulayıcılar' }));
    await screen.findByTestId('practitioner-table');

    await user.click(screen.getByRole('button', { name: 'Sicil ile ara' }));
    const dialog = await screen.findByRole('dialog', { name: 'Sicil numarasıyla ara' });
    await user.type(within(dialog).getByLabelText(/Sicil numarası/), registration);
    await user.click(within(dialog).getByRole('button', { name: 'Ara' }));

    // The server refuses without a step-up, so the password dialog takes over.
    const stepUp = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
    await user.type(within(stepUp).getByLabelText(/^Parola/), PASSWORD);
    await user.click(within(stepUp).getByRole('button', { name: 'Doğrula' }));

    // The retried search hands back the practitioner, and the screen opens them.
    const found = await screen.findByRole('dialog', { name: 'Uygulayıcı' });
    expect(within(found).getByDisplayValue('Elif Şahin')).toBeInTheDocument();

    // The plaintext number is nowhere: not in the text, not in a field, not in the URL.
    expect(document.body.textContent).not.toContain(registration);
    const values = Array.from(document.querySelectorAll('input')).map((input) => input.value);
    expect(values).not.toContain(registration);
    expect(router.state.location.href).not.toContain(registration);
  });

  it('writes the assignment set and refuses an overlapping one', async () => {
    const { provider } = seededProvider();
    const { user } = await mount(`/providers/${provider.id}`);
    await user.click(await screen.findByRole('tab', { name: 'Uygulayıcılar' }));
    const table = await screen.findByTestId('practitioner-table');

    const row = within(table).getByText('Elif Şahin').closest('tr');
    if (!row) throw new Error('fixture: practitioner row not found');
    await user.click(within(row).getByRole('button', { name: 'Görev yerleri' }));

    const dialog = await screen.findByRole('dialog', { name: 'Görev yerleri' });
    await user.click(within(dialog).getByRole('button', { name: 'Görev yeri ekle' }));

    // The same location and role over an overlapping period is refused by the server.
    const locations = within(dialog).getAllByLabelText(/^Lokasyon/);
    await user.selectOptions(locations[locations.length - 1]!, 'IST-01 · Kadıköy Tıp Merkezi');
    const starts = within(dialog).getAllByLabelText(/^Başlangıç/);
    await user.type(starts[starts.length - 1]!, '2026-03-01');

    await user.click(within(dialog).getByRole('button', { name: 'Kaydet' }));
    expect(
      await screen.findByText('Aynı lokasyon ve görev için çakışan bir görevlendirme var.'),
    ).toBeInTheDocument();
  });

  it('offers no practitioner writing to an operator without the grant', async () => {
    const { provider } = seededProvider();
    const { user } = await mount(`/providers/${provider.id}`, 'reviewer.a');
    await user.click(await screen.findByRole('tab', { name: 'Uygulayıcılar' }));
    await screen.findByTestId('practitioner-table');

    expect(screen.queryByRole('button', { name: 'Sicil ile ara' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Yeni uygulayıcı' })).not.toBeInTheDocument();
  });
});

describe('provider creation', () => {
  it('creates a profile on a PROVIDER relationship that has none yet', async () => {
    const tenantId = api.world.tenants[0]!.id;
    const free = api.world.relationships.find(
      (r) =>
        r.tenantId === tenantId &&
        r.relationshipRole === 'PROVIDER' &&
        !api.world.providers.some((p) => p.tenantOrganizationId === r.id),
    );
    if (!free) throw new Error('fixture: every provider relationship already has a profile');
    const name = api.world.organizations.get(free.organizationId)?.displayName ?? '';

    const { user, router } = await mount('/providers/new');
    await screen.findByRole('heading', { name: 'Yeni sağlayıcı' });

    await waitFor(() =>
      expect(within(screen.getByLabelText(/Kurum/)).getAllByRole('option').length).toBeGreaterThan(
        1,
      ),
    );
    await user.selectOptions(screen.getByLabelText(/Kurum/), name);
    await user.selectOptions(screen.getByLabelText(/Sağlayıcı türü/), 'Klinik');
    await user.click(screen.getByRole('button', { name: 'Kaydet' }));

    expect(await screen.findByText('Sağlayıcı oluşturuldu.')).toBeInTheDocument();
    await waitFor(() =>
      expect(router.state.location.pathname).toMatch(/^\/providers\/[0-9a-f-]+$/),
    );
    // A new profile always starts PENDING; it is activated by an explicit command.
    const created = api.world.providers.find((p) => p.tenantOrganizationId === free.id);
    expect(created?.status).toBe('PENDING');
  });
});

describe('browser storage', () => {
  it('keeps nothing in localStorage or sessionStorage', async () => {
    const { provider } = seededProvider();
    await mount(`/providers/${provider.id}`);
    await screen.findByRole('tab', { name: 'Profil' });
    expect(Object.keys(localStorage)).toHaveLength(0);
    expect(Object.keys(sessionStorage)).toHaveLength(0);
  });
});
