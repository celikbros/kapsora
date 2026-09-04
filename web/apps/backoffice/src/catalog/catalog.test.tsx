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
import { CategoryTreePage } from './CategoryTreePage';
import { CodeSystemDetailPage } from './CodeSystemDetailPage';
import { CodeSystemListPage } from './CodeSystemListPage';
import { DefinitionCreatePage } from './DefinitionCreatePage';
import { DefinitionDetailPage } from './DefinitionDetailPage';
import { DefinitionListPage } from './DefinitionListPage';

const { api, server } = createMockServer({ organizationsPerTenant: 4 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => api.reset());
afterAll(() => server.close());

// The application router is owned by router.tsx; these tests mount the catalog screens on
// the paths they will be wired to, so the links between them are exercised as well.
const rootRoute = createRootRoute({ component: () => <Outlet /> });
const routeTree = rootRoute.addChildren([
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/catalog/categories',
    component: CategoryTreePage,
  }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/catalog/definitions',
    component: DefinitionListPage,
  }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/catalog/definitions/new',
    component: DefinitionCreatePage,
  }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/catalog/definitions/$definitionId',
    component: DefinitionDetailPage,
  }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/catalog/code-systems',
    component: CodeSystemListPage,
  }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/catalog/code-systems/$codeSystemId',
    component: CodeSystemDetailPage,
  }),
]);

function tenantId(): string {
  const tenant = api.world.tenants[0];
  if (!tenant) throw new Error('fixture: no tenant');
  return tenant.id;
}

function category(code: string) {
  const found = api.world.serviceCategories.find(
    (entry) => entry.tenantId === tenantId() && entry.code === code,
  );
  if (!found) throw new Error(`fixture: no category ${code}`);
  return found;
}

function definition(code: string) {
  const found = api.world.serviceDefinitions.find(
    (entry) => entry.tenantId === tenantId() && entry.code === code,
  );
  if (!found) throw new Error(`fixture: no service definition ${code}`);
  return found;
}

function sutSystem() {
  const found = api.world.codeSystems.find((entry) => entry.tenantId === tenantId());
  if (!found) throw new Error('fixture: no code system');
  return found;
}

async function mount(path: string, username = 'admin.a') {
  const services = createServices({ baseUrl: BASE });
  await services.store.login(username, PASSWORD);
  await services.store.switchTenant(tenantId());
  const history = createMemoryHistory({ initialEntries: [path] });
  const router = createRouter({ routeTree, history });
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
  return { services, history, user: userEvent.setup() };
}

describe('category tree', () => {
  it('shows the tree under its roots and creates a child', async () => {
    const { user } = await mount('/catalog/categories');
    const table = await screen.findByTestId('category-table');
    await waitFor(() =>
      expect(within(table).getAllByRole('row').length).toBeGreaterThan(
        api.world.serviceCategories.length,
      ),
    );
    expect(within(table).getByText('Ayakta Tedavi')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Yeni kategori' }));
    const dialog = await screen.findByRole('dialog', { name: 'Yeni kategori' });
    await user.type(within(dialog).getByLabelText(/^Kod/), 'HEALTH_DENTAL');
    await user.type(within(dialog).getByLabelText(/^Ad/), 'Diş Tedavisi');
    await user.selectOptions(
      within(dialog).getByLabelText(/^Üst kategori/),
      `${category('HEALTH').code} · ${category('HEALTH').name}`,
    );
    await user.click(within(dialog).getByRole('button', { name: 'Kaydet' }));

    expect(await within(table).findByText('Diş Tedavisi')).toBeInTheDocument();
  });

  it('renders the cycle the server refuses when a parent moves under its own child', async () => {
    const { user } = await mount('/catalog/categories');
    const table = await screen.findByTestId('category-table');
    const row = within(table).getByRole('row', { name: /Sağlık Hizmetleri/ });
    await user.click(within(row).getByRole('button', { name: 'Düzenle' }));

    const dialog = await screen.findByRole('dialog', { name: 'Kategoriyi düzenle' });
    await waitFor(() =>
      expect(within(dialog).getByLabelText(/^Ad/)).toHaveValue('Sağlık Hizmetleri'),
    );
    const child = category('HEALTH_OUTPATIENT');
    await user.selectOptions(
      within(dialog).getByLabelText(/^Üst kategori/),
      `${child.code} · ${child.name}`,
    );
    await user.click(within(dialog).getByRole('button', { name: 'Kaydet' }));

    expect(
      await within(dialog).findByText('Kategori kendi alt ağacına bağlanamaz.'),
    ).toBeInTheDocument();
    // Nothing moved: the tree is exactly as it was.
    expect(category('HEALTH').parentId).toBeNull();
  });

  it('hides the controls an operator without catalog.manage cannot use', async () => {
    await mount('/catalog/categories', 'reviewer.a');
    await screen.findByTestId('category-table');
    expect(screen.queryByRole('button', { name: 'Yeni kategori' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Düzenle' })).not.toBeInTheDocument();
  });
});

describe('service definitions', () => {
  it('filters the list and opens a definition', async () => {
    const { user, history } = await mount('/catalog/definitions');
    const table = await screen.findByTestId('definition-table');
    await waitFor(() => expect(within(table).getAllByRole('row').length).toBeGreaterThan(2));

    await user.type(screen.getByLabelText(/^Ad veya kod ara/), 'MRI');
    await user.click(screen.getByRole('button', { name: 'Ara' }));
    await waitFor(() => expect(within(table).getAllByRole('row')).toHaveLength(2));

    await user.click(screen.getByRole('link', { name: 'MR Çekimi' }));
    await screen.findByRole('tab', { name: 'Hizmet tanımı' });
    expect(history.location.pathname).toBe(`/catalog/definitions/${definition('MRI_SCAN').id}`);
  });

  it('shows the code as read-only and says why', async () => {
    await mount(`/catalog/definitions/${definition('MRI_SCAN').id}`);
    await screen.findByRole('tab', { name: 'Hizmet tanımı' });
    const code = screen.getByLabelText(/^Kod/);
    expect(code).toHaveValue('MRI_SCAN');
    expect(code).toHaveAttribute('readonly');
    expect(screen.getByText(/Kod oluşturulduktan sonra değiştirilemez/)).toBeInTheDocument();
  });

  it('creates a definition and lands on it', async () => {
    const { user, history } = await mount('/catalog/definitions/new');
    await screen.findByTestId('definition-create-form');
    await user.type(screen.getByLabelText(/^Kod/), 'DENTAL_CHECK');
    await user.type(screen.getByLabelText(/^Ad/), 'Diş Kontrolü');
    await waitFor(() =>
      expect(
        within(screen.getByLabelText(/^Kategori/)).getAllByRole('option').length,
      ).toBeGreaterThan(1),
    );
    await user.selectOptions(
      screen.getByLabelText(/^Kategori/),
      `${category('HEALTH_OUTPATIENT').code} · ${category('HEALTH_OUTPATIENT').name}`,
    );
    await user.click(screen.getByRole('button', { name: 'Kaydet' }));

    await screen.findByRole('tab', { name: 'Hizmet tanımı' });
    const created = definition('DENTAL_CHECK');
    expect(history.location.pathname).toBe(`/catalog/definitions/${created.id}`);
  });

  it('recovers when the record changed under the operator', async () => {
    const { user } = await mount(`/catalog/definitions/${definition('MRI_SCAN').id}`);
    await screen.findByTestId('definition-form');
    // Somebody else saved between the read and this write.
    definition('MRI_SCAN').rowVersion += 1;

    const name = screen.getByLabelText(/^Ad/);
    await user.clear(name);
    await user.type(name, 'MR Çekimi (revize)');
    await user.click(screen.getByRole('button', { name: 'Kaydet' }));

    expect(await screen.findByText('Kayıt siz düzenlerken değişti.')).toBeInTheDocument();
  });
});

describe('code mappings', () => {
  it('refuses an overlapping period and saves the corrected set', async () => {
    const { user } = await mount(`/catalog/definitions/${definition('PHYSIO_SESSION').id}`);
    await user.click(await screen.findByRole('tab', { name: 'Dış kod eşleştirmeleri' }));
    await screen.findByTestId('mappings-form');
    await waitFor(() => expect(screen.getAllByRole('group')).toHaveLength(1));

    await user.click(screen.getByRole('button', { name: 'Eşleştirme ekle' }));
    const rows = () => screen.getAllByRole('group');
    await waitFor(() => expect(rows()).toHaveLength(2));

    const added = rows()[1]!;
    const system = sutSystem();
    await waitFor(() => expect(within(added).getAllByRole('option').length).toBeGreaterThan(1));
    await user.selectOptions(
      within(added).getByRole('combobox'),
      `${system.code} ${system.version}`,
    );
    // The same code over an overlapping period as the seeded row.
    await user.type(within(added).getByRole('textbox'), '520030');
    await user.click(screen.getByRole('button', { name: 'Kaydet' }));

    expect(
      await screen.findByText('Aynı kod için çakışan bir geçerlilik dönemi var.'),
    ).toBeInTheDocument();
    expect(api.world.codeMappings).toHaveLength(1);

    // A distinct code in the same system is a second reporting code, not a conflict.
    const code = within(rows()[1]!).getByRole('textbox');
    await user.clear(code);
    await user.type(code, '520999');
    await user.click(screen.getByRole('button', { name: 'Kaydet' }));

    expect(await screen.findByText('Eşleştirmeler kaydedildi.')).toBeInTheDocument();
    await waitFor(() => expect(api.world.codeMappings).toHaveLength(2));
  });
});

describe('code systems', () => {
  it('lists the systems and opens one', async () => {
    const { user, history } = await mount('/catalog/code-systems');
    const table = await screen.findByTestId('code-system-table');
    expect(within(table).getByText('SUT')).toBeInTheDocument();

    await user.click(screen.getByRole('link', { name: 'Sağlık Uygulama Tebliği' }));
    await screen.findByTestId('code-value-table');
    expect(history.location.pathname).toBe(`/catalog/code-systems/${sutSystem().id}`);
  });

  it('resolves the codes against the date the operator asks for', async () => {
    const { user } = await mount(`/catalog/code-systems/${sutSystem().id}`);
    const asOf = await screen.findByLabelText(/^Şu tarihte geçerli/);
    expect(
      screen.getByText(/Geçmiş bir claim, o gün geçerli olan kodlara göre çözümlenir/),
    ).toBeInTheDocument();

    await user.clear(asOf);
    await user.type(asOf, '2026-06-01');
    const table = await screen.findByTestId('code-value-table');
    await waitFor(() => expect(within(table).getByText('520031')).toBeInTheDocument());
    expect(within(table).getByText('520030')).toBeInTheDocument();

    // The old session code was retired at the start of 2027, so a later date drops it.
    await user.clear(asOf);
    await user.type(asOf, '2027-06-01');
    await waitFor(() => expect(within(table).queryByText('520031')).not.toBeInTheDocument());
    expect(within(table).getByText('520030')).toBeInTheDocument();
  });

  it('imports a pasted batch and reports the row it refused', async () => {
    const { user } = await mount(`/catalog/code-systems/${sutSystem().id}`);
    await screen.findByTestId('code-value-table');
    await user.click(screen.getByRole('button', { name: 'Kod yükle' }));
    const dialog = await screen.findByRole('dialog', { name: 'Kod listesi yükle' });
    const paste = within(dialog).getByLabelText(/^Kodlar/);

    // The same code twice for the same start date: all or nothing, so nothing is written.
    const before = api.world.codeValues.length;
    await user.click(paste);
    await user.paste(
      JSON.stringify([
        { code: '901010', display: 'Diş muayenesi', validFrom: '2026-01-01' },
        { code: '901010', display: 'Diş muayenesi (kopya)', validFrom: '2026-01-01' },
      ]),
    );
    await user.click(within(dialog).getByRole('button', { name: 'Kod yükle' }));

    const result = await screen.findByTestId('import-result');
    expect(within(result).getByText(/Aynı değer birden çok kez verilmiş/)).toBeInTheDocument();
    expect(api.world.codeValues).toHaveLength(before);

    await user.clear(paste);
    await user.click(paste);
    await user.paste(
      JSON.stringify({
        items: [{ code: '901010', display: 'Diş muayenesi', validFrom: '2026-01-01' }],
      }),
    );
    await user.click(within(dialog).getByRole('button', { name: 'Kod yükle' }));

    expect(
      await screen.findByText('1 kod eklendi, 0 güncellendi, 0 değişmedi.'),
    ).toBeInTheDocument();
    await waitFor(() => expect(api.world.codeValues).toHaveLength(before + 1));
  });

  it('gives a reader the codes without the controls that change them', async () => {
    await mount(`/catalog/code-systems/${sutSystem().id}`, 'reviewer.a');
    await screen.findByTestId('code-value-table');
    expect(screen.queryByRole('button', { name: 'Kod yükle' })).not.toBeInTheDocument();
    expect(screen.queryByTestId('code-system-form')).not.toBeInTheDocument();
  });

  it('keeps nothing in browser storage', async () => {
    await mount('/catalog/code-systems');
    await screen.findByTestId('code-system-table');
    expect(Object.keys(localStorage)).toHaveLength(0);
    expect(Object.keys(sessionStorage)).toHaveLength(0);
  });
});
