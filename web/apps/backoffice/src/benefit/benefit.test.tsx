import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { App } from '../App';
import { createServices } from '../api';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
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
  return { services, history };
}

async function login(username: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

/** both.ab belongs to two tenants, so it lands on the picker first. */
async function loginToDemoA(username: string) {
  const user = await login(username);
  const picker = screen.queryByRole('heading', { name: 'Çalışma alanı seçin' });
  if (picker) {
    const rowA = screen.getAllByRole('listitem').find((li) => li.textContent?.includes('DEMO_A'));
    if (!rowA) throw new Error('fixture: DEMO_A not offered');
    await user.click(within(rowA).getByRole('button', { name: 'Seç' }));
  }
  return user;
}

const tenantId = () => api.world.tenants[0]!.id;

/** The seeded draft version of the first plan in DEMO_A. */
function draftVersion() {
  const version = api.world.planVersions.find(
    (v) => v.tenantId === tenantId() && v.status === 'DRAFT',
  );
  if (!version) throw new Error('fixture: no draft plan version');
  return version;
}

function publishedVersion() {
  const version = api.world.planVersions.find(
    (v) => v.tenantId === tenantId() && v.status === 'PUBLISHED',
  );
  if (!version) throw new Error('fixture: no published plan version');
  return version;
}

describe('programs and plans', () => {
  it('lists programs and opens one with its plans', async () => {
    const { history } = mount('/programs');
    const user = await login('admin.a');
    const table = await screen.findByTestId('program-table');
    await waitFor(() => expect(within(table).getAllByRole('row').length).toBeGreaterThan(1));

    const program = api.world.programs.find((p) => p.tenantId === tenantId())!;
    await user.click(await screen.findByRole('link', { name: program.name }));
    await screen.findByRole('heading', { name: 'Planlar' });
    expect(history.location.pathname).toBe(`/programs/${program.id}`);
    expect(await screen.findByTestId('plan-table')).toBeInTheDocument();
  });

  it('creates a program and lands on its detail page', async () => {
    mount('/programs/new');
    const user = await login('admin.a');
    await screen.findByRole('heading', { name: 'Yeni program' });

    await user.type(screen.getByLabelText(/^Kod/), 'YENI_PRG');
    await user.type(screen.getByLabelText(/^Ad/), 'Yeni Test Programı');
    const sponsorSelect = screen.getByLabelText(/Sponsor kurum/);
    await waitFor(() =>
      expect(within(sponsorSelect).getAllByRole('option').length).toBeGreaterThan(1),
    );
    await user.selectOptions(sponsorSelect, within(sponsorSelect).getAllByRole('option')[1]!);
    const payerSelect = screen.getByLabelText(/Ödeyici kurum/);
    await user.selectOptions(payerSelect, within(payerSelect).getAllByRole('option')[1]!);
    await user.click(screen.getByRole('button', { name: 'Kaydet' }));

    await screen.findByRole('heading', { name: 'Program' });
    expect(screen.getByRole('heading', { level: 1 }).textContent).toContain('Yeni Test Programı');
  });

  it('refuses a code the program list already uses', async () => {
    mount('/programs/new');
    const user = await login('admin.a');
    await screen.findByRole('heading', { name: 'Yeni program' });
    const taken = api.world.programs.find((p) => p.tenantId === tenantId())!;

    await user.type(screen.getByLabelText(/^Kod/), taken.code);
    await user.type(screen.getByLabelText(/^Ad/), 'Kopya');
    const sponsorSelect = screen.getByLabelText(/Sponsor kurum/);
    await waitFor(() =>
      expect(within(sponsorSelect).getAllByRole('option').length).toBeGreaterThan(1),
    );
    await user.selectOptions(sponsorSelect, within(sponsorSelect).getAllByRole('option')[1]!);
    const payerSelect = screen.getByLabelText(/Ödeyici kurum/);
    await user.selectOptions(payerSelect, within(payerSelect).getAllByRole('option')[1]!);
    await user.click(screen.getByRole('button', { name: 'Kaydet' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveAttribute('data-problem-code', 'PROGRAM_CODE_TAKEN');
  });
});

describe('plan version maker-checker', () => {
  it('edits a draft, submits it, then refuses to let the submitter publish', async () => {
    const version = draftVersion();
    mount(`/plan-versions/${version.id}`);
    const user = await login('admin.a');
    await screen.findByRole('heading', { name: 'Plan sürümü' });

    // The seeded draft has no period and no definitions, so submit is not possible yet.
    const periodForm = screen.getByTestId('version-period-form');
    await user.type(within(periodForm).getByLabelText(/^Başlangıç/), '2027-03-01');
    await user.click(within(periodForm).getByRole('button', { name: 'Kaydet' }));
    await waitFor(() =>
      expect(api.world.planVersions.find((v) => v.id === version.id)?.validFrom).toBe('2027-03-01'),
    );

    const definitions = screen.getByTestId('definitions-form');
    await user.type(within(definitions).getByLabelText(/^Kod/), 'TEST_MONEY');
    await user.type(within(definitions).getByLabelText(/^Ad/), 'Test Bakiyesi');
    const quantity = within(definitions).getByLabelText(/Başlangıç miktarı/);
    await user.clear(quantity);
    await user.type(quantity, '2500.500000');
    await user.click(within(definitions).getByRole('button', { name: 'Kaydet' }));
    await waitFor(() =>
      expect(api.world.planVersions.find((v) => v.id === version.id)?.definitions).toHaveLength(1),
    );
    // The quantity reached the server as text, with all six decimals intact.
    expect(
      api.world.planVersions.find((v) => v.id === version.id)?.definitions[0]?.initialQuantity,
    ).toBe('2500.500000');

    await user.click(screen.getByRole('button', { name: 'İncelemeye gönder' }));
    const confirm = await screen.findByRole('dialog', { name: 'İncelemeye gönder' });
    await user.click(within(confirm).getByRole('button', { name: 'İncelemeye gönder' }));

    await screen.findByText('İncelemede');
    // The submitter is told why publishing is not theirs to do, and gets no button for it.
    expect(await screen.findByText(/gönderen kişi olamaz/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Yayınla' })).not.toBeInTheDocument();
  });

  it('lets a second person publish after a password re-entry', async () => {
    const version = draftVersion();
    // The seeded v1 is published open-ended, so close it or the new period overlaps it.
    const live = publishedVersion();
    live.validTo = '2027-01-01';
    // Put the version in review, submitted by admin.a, without going through the screen.
    version.status = 'UNDER_REVIEW';
    version.validFrom = '2027-03-01';
    version.submittedBy = api.world.accounts[0]!.actorId;
    version.definitions = [
      {
        id: 'def-1',
        code: 'TEST_MONEY',
        name: 'Test Bakiyesi',
        unitType: 'MONEY',
        currencyCode: 'TRY',
        familyShared: false,
        allowOverdraft: false,
        initialQuantity: 1000,
        periodType: 'CALENDAR_YEAR',
        periodLength: null,
        rolloverPolicy: 'NONE',
        rolloverCap: null,
        status: 'ACTIVE',
      },
    ];

    mount(`/plan-versions/${version.id}`);
    const user = await loginToDemoA('both.ab');
    await screen.findByRole('heading', { name: 'Plan sürümü' });

    await user.click(screen.getByRole('button', { name: 'Yayınla' }));
    const confirm = await screen.findByRole('dialog', { name: 'Yayınla' });
    await user.click(within(confirm).getByRole('button', { name: 'Yayınla' }));

    const stepUp = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
    await user.type(within(stepUp).getByLabelText(/^Parola/), PASSWORD);
    await user.click(within(stepUp).getByRole('button', { name: 'Doğrula' }));

    await waitFor(() =>
      expect(api.world.planVersions.find((v) => v.id === version.id)?.status).toBe('PUBLISHED'),
    );
    await screen.findByText('Yayında');
    expect(await screen.findByText(/Yapılandırma özeti/)).toBeInTheDocument();
  });

  it('shows a published version read-only, with no definition editor', async () => {
    const version = publishedVersion();
    mount(`/plan-versions/${version.id}`);
    await login('admin.a');
    await screen.findByRole('heading', { name: 'Plan sürümü' });

    expect(await screen.findByTestId('definition-table')).toBeInTheDocument();
    expect(screen.queryByLabelText(/Başlangıç miktarı/)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'İncelemeye gönder' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Kullanımdan çıkar' })).toBeInTheDocument();
  });
});
