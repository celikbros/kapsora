import { Blob as NodeBlob, File as NodeFile } from 'node:buffer';

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
import { ImportDetailPage } from './ImportDetailPage';
import { ImportListPage } from './ImportListPage';
import { ImportUploadPage } from './ImportUploadPage';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(async () => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
  // jsdom's Blob, File and FormData cannot be serialised into a multipart body by Node's
  // fetch, which is what the mock server parses on the other side, so the upload needs
  // the platform implementations. Recovering FormData through a Request is the only way
  // to reach the constructor jsdom shadowed.
  const probe = await new Request('http://multipart.invalid/', {
    method: 'POST',
    body: new URLSearchParams({ a: 'b' }),
  }).formData();
  globalThis.FormData = Object.getPrototypeOf(probe).constructor as typeof FormData;
  globalThis.Blob = NodeBlob as unknown as typeof Blob;
  globalThis.File = NodeFile as unknown as typeof File;
});
afterEach(() => api.reset());
afterAll(() => server.close());

// The application router is owned by router.tsx; these tests mount the three import
// screens on the same paths so the links between them are exercised as well.
const rootRoute = createRootRoute({ component: () => <Outlet /> });
const routeTree = rootRoute.addChildren([
  createRoute({ getParentRoute: () => rootRoute, path: '/imports', component: ImportListPage }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/imports/new',
    component: ImportUploadPage,
  }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/imports/$importId',
    component: ImportDetailPage,
  }),
]);

function tenantId(): string {
  const tenant = api.world.tenants[0];
  if (!tenant) throw new Error('fixture: no tenant');
  return tenant.id;
}

/** Display name of the seeded sponsor the upload form offers. */
function sponsorName(): string {
  const program = api.world.programs[0];
  if (!program) throw new Error('fixture: no program');
  const relationship = api.world.relationships.find(
    (entry) => entry.id === program.sponsorOrganizationId,
  );
  if (!relationship) throw new Error('fixture: sponsor has no relationship');
  const organization = api.world.organizations.get(relationship.organizationId);
  if (!organization) throw new Error('fixture: sponsor has no organization');
  return organization.displayName;
}

/**
 * Gives two people the same identity number so matching answers CONFLICT, and returns
 * the numbers the file carries: one that matches exactly one person and one that two
 * people share. Calling it twice leaves the same world, so the file stays byte-identical.
 */
function fixtureIdentifiers(): { matched: string; conflicting: string } {
  const withTckn = api.world.people.filter(
    (person) => person.tenantId === tenantId() && person.identifiers.some((i) => i.type === 'TCKN'),
  );
  const [first, second, third] = withTckn;
  if (!first || !second || !third) throw new Error('fixture: not enough people with a TCKN');
  const shared = first.identifiers.find((i) => i.type === 'TCKN');
  const duplicate = second.identifiers.find((i) => i.type === 'TCKN');
  const other = third.identifiers.find((i) => i.type === 'TCKN');
  if (!shared || !duplicate || !other) throw new Error('fixture: person lost its TCKN');
  duplicate.value = shared.value;
  return { matched: other.value, conflicting: shared.value };
}

const HEADER = [
  'source_record_id;first_name;middle_name;last_name;birth_date;sex_at_birth;tckn;member_no;',
  'employee_no;membership_type;principal_member_no;relationship;valid_from;valid_to;plan_code',
].join('');

/** One matched row, one conflicting row, one row with a bad checksum and one new row. */
function fixtureFile(): File {
  const identifiers = fixtureIdentifiers();
  const rows = [
    `R1;Ayşe;;Yılmaz;1980-05-04;FEMALE;${identifiers.matched};M-1;;PRINCIPAL;;;2026-01-01;;`,
    `R2;Belma;;Çakır;1981-06-05;FEMALE;${identifiers.conflicting};M-2;;PRINCIPAL;;;2026-01-01;;`,
    'R3;Bozuk;;Kayıt;1990-01-01;MALE;12345678902;M-3;;PRINCIPAL;;;2026-01-01;;',
    'R4;Yeni;;Üye;1992-02-02;MALE;;M-4;;PRINCIPAL;;;2026-01-01;;',
  ];
  return new File([`${HEADER}\n${rows.join('\n')}\n`], 'uyeler.csv', { type: 'text/csv' });
}

type User = ReturnType<typeof userEvent.setup>;

async function mount(path: string) {
  const services = createServices({ baseUrl: BASE });
  await services.store.login('admin.a', PASSWORD);
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

async function fillAndSubmit(user: User, file: File, sourceVersion: string) {
  await user.upload(screen.getByLabelText(/Dosya \(CSV\)/), file);
  const sponsor = screen.getByLabelText(/Sponsor kurum/);
  await waitFor(() => expect(within(sponsor).getAllByRole('option').length).toBeGreaterThan(1));
  await user.selectOptions(sponsor, within(sponsor).getByRole('option', { name: sponsorName() }));
  await user.type(screen.getByLabelText(/Kaynak sistem/), 'HR');
  await user.type(screen.getByLabelText(/Kaynak sürümü/), sourceVersion);
  await user.click(screen.getByRole('button', { name: 'Kaydet' }));
}

async function confirmPassword(user: User) {
  const dialog = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await user.type(within(dialog).getByLabelText(/^Parola/), PASSWORD);
  await user.click(within(dialog).getByRole('button', { name: 'Doğrula' }));
}

/** Uploads the fixture file and leaves the test on the batch page. */
async function uploadFixtureFile() {
  const mounted = await mount('/imports/new');
  await screen.findByRole('heading', { name: 'Dosya yükle' });
  await fillAndSubmit(mounted.user, fixtureFile(), 'v1');
  await confirmPassword(mounted.user);
  await screen.findByTestId('import-counters');
  return mounted;
}

describe('member import upload', () => {
  it('stages a file after the password prompt and lands on the batch', async () => {
    const { history } = await uploadFixtureFile();
    expect(history.location.pathname).toMatch(/^\/imports\/[^/]+$/);
    expect(screen.getByTestId('import-counters')).toHaveTextContent('Dosyadaki satır');
    expect(await screen.findByText('İnceleme bekliyor')).toBeInTheDocument();
  });

  it('refuses the same file under the same source version', async () => {
    const { user } = await uploadFixtureFile();
    await user.click(screen.getByRole('link', { name: 'Üye içe aktarma' }));
    await user.click(await screen.findByRole('link', { name: 'Yeni içe aktarma' }));
    await screen.findByRole('heading', { name: 'Dosya yükle' });

    // The step-up window is still open, so this attempt goes straight to the server.
    await fillAndSubmit(user, fixtureFile(), 'v1');
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveAttribute('data-problem-code', 'IMPORT_DUPLICATE');
  });
});

describe('member import review', () => {
  it('opens on the rows that need a decision and records one', async () => {
    const { user } = await uploadFixtureFile();
    expect(screen.getByLabelText('Durum', { selector: 'select' })).toHaveValue('CONFLICT');

    const table = await screen.findByTestId('import-row-table');
    expect(within(table).getAllByRole('row').length).toBe(2);

    // A conflict needs the person named before the update decision is allowed.
    const candidate = within(table).getByLabelText('Hangi kayıt güncellenecek');
    const option = within(candidate).getAllByRole('option')[1];
    if (!option) throw new Error('the conflicting row carries no candidate');
    await user.selectOptions(candidate, option);
    await user.click(within(table).getByRole('button', { name: 'Güncelle' }));
    expect(await screen.findByText('Karar kaydedildi.')).toBeInTheDocument();

    // With the conflict resolved the filter falls back to the row that is still invalid,
    // which the server only lets the operator skip.
    await waitFor(() =>
      expect(screen.getByLabelText('Durum', { selector: 'select' })).toHaveValue('INVALID'),
    );
    const invalid = await screen.findByTestId('import-row-table');
    expect(within(invalid).queryAllByRole('button', { name: 'Güncelle' })).toHaveLength(0);
    expect(within(invalid).getByRole('button', { name: 'Atla' })).toBeInTheDocument();
  });

  it('never shows a raw identity number and keeps storage empty', async () => {
    await uploadFixtureFile();
    const identifiers = fixtureIdentifiers();
    await screen.findByTestId('import-row-table');
    expect(document.body.textContent).not.toContain(identifiers.conflicting);
    expect(document.body.textContent).not.toContain(identifiers.matched);
    expect(screen.getAllByText(/\*{3,}/).length).toBeGreaterThan(0);
    expect(Object.keys(localStorage)).toHaveLength(0);
    expect(Object.keys(sessionStorage)).toHaveLength(0);
  });
});

describe('member import commands', () => {
  it('applies the batch after confirming what it will act on', async () => {
    const { user } = await uploadFixtureFile();
    const table = await screen.findByTestId('import-row-table');
    const candidate = within(table).getByLabelText('Hangi kayıt güncellenecek');
    const option = within(candidate).getAllByRole('option')[1];
    if (!option) throw new Error('the conflicting row carries no candidate');
    await user.selectOptions(candidate, option);
    await user.click(within(table).getByRole('button', { name: 'Güncelle' }));
    await screen.findByText('Karar kaydedildi.');

    await user.click(screen.getByRole('button', { name: 'Uygula' }));
    const dialog = await screen.findByRole('dialog', { name: 'İçe aktarmayı uygula' });
    expect(within(dialog).getByTestId('apply-summary')).toHaveTextContent('Hatalı');
    await user.click(within(dialog).getByRole('button', { name: 'Uygula' }));

    await waitFor(() =>
      expect(screen.getByTestId('import-counters')).toHaveTextContent('Oluşturulan'),
    );
    expect(screen.getAllByText('Uygulandı').length).toBeGreaterThan(0);
  });

  it('cancels a batch with a reason', async () => {
    const { user } = await uploadFixtureFile();
    await user.click(screen.getByRole('button', { name: 'İptal et' }));
    const dialog = await screen.findByRole('dialog', { name: 'İçe aktarmayı iptal et' });
    await user.type(within(dialog).getByLabelText(/İptal sebebi/), 'WRONG_FILE');
    await user.click(within(dialog).getByRole('button', { name: 'İptal et' }));
    expect(await screen.findByText('İçe aktarma iptal edildi.')).toBeInTheDocument();
    expect(await screen.findByText('İptal edildi')).toBeInTheDocument();
  });
});

describe('member import list', () => {
  it('lists the batches, filters by status and opens one', async () => {
    const { user } = await uploadFixtureFile();
    await user.click(screen.getByRole('link', { name: 'Üye içe aktarma' }));
    const table = await screen.findByTestId('import-table');
    expect(within(table).getAllByRole('row').length).toBe(2);

    await user.selectOptions(screen.getByLabelText('Durum', { selector: 'select' }), 'APPLIED');
    await screen.findByText('İçe aktarma yok.');
    await user.click(screen.getByRole('button', { name: 'Temizle' }));

    await user.click(await screen.findByRole('link', { name: 'uyeler.csv' }));
    await screen.findByTestId('import-counters');
  });
});
