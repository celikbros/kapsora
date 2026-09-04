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

/** both.ab belongs to two tenants, so it stops at the picker. */
async function loginToDemoA(username: string) {
  const user = await login(username);
  const picker = screen.queryByRole('heading', { name: 'Çalışma alanı seçin' });
  if (picker) {
    const row = screen.getAllByRole('listitem').find((li) => li.textContent?.includes('DEMO_A'));
    if (!row) throw new Error('fixture: DEMO_A not offered');
    await user.click(within(row).getByRole('button', { name: 'Seç' }));
  }
  return user;
}

const tenantId = () => api.world.tenants[0]!.id;

function theContract() {
  const contract = api.world.contracts.find((c) => c.tenantId === tenantId());
  if (!contract) throw new Error('fixture: no contract seeded');
  return contract;
}

function versionOf(status: string) {
  const version = api.world.contractVersions.find(
    (v) => v.tenantId === tenantId() && v.status === status,
  );
  if (!version) throw new Error(`fixture: no ${status} contract version`);
  return version;
}

describe('contracts', () => {
  it('lists contracts and opens one with its version history', async () => {
    const { history } = mount('/contracts');
    const user = await login('admin.a');
    const table = await screen.findByTestId('contract-table');
    await waitFor(() => expect(within(table).getAllByRole('row').length).toBeGreaterThan(1));

    const contract = theContract();
    await user.click(await screen.findByRole('link', { name: contract.name }));
    await screen.findByTestId('contract-version-table');
    expect(history.location.pathname).toBe(`/contracts/${contract.id}`);
  });

  it('shows a published version read-only with its configuration hash', async () => {
    const version = versionOf('PUBLISHED');
    mount(`/contract-versions/${version.id}`);
    await login('admin.a');

    await screen.findByText('Yayında');
    expect(await screen.findByTestId('price-items-table')).toBeInTheDocument();
    // Read-only: the sheet is a table, not a form.
    expect(screen.queryByTestId('price-items-form')).not.toBeInTheDocument();
    expect(screen.getByText(/Yapılandırma özeti/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'İncelemeye gönder' })).not.toBeInTheDocument();
  });

  it('offers a draft the way to create the list its prices live in', async () => {
    const version = versionOf('DRAFT');
    mount(`/contract-versions/${version.id}`);
    await login('admin.a');

    await screen.findByText('Taslak');
    // The price sheet is unreachable until the version has a list to hold it, so the
    // draft must offer the way to create one rather than showing an empty sheet.
    expect(await screen.findByTestId('price-lists-form')).toBeInTheDocument();
  });

  it('refuses to submit a version that prices nothing', async () => {
    const version = versionOf('DRAFT');
    mount(`/contract-versions/${version.id}`);
    const user = await login('admin.a');
    await screen.findByText('Taslak');

    await user.click(screen.getByRole('button', { name: 'İncelemeye gönder' }));
    const confirm = await screen.findByRole('dialog', { name: 'İncelemeye gönder' });
    await user.click(within(confirm).getByRole('button', { name: 'İncelemeye gönder' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveAttribute('data-problem-code', 'VALIDATION_FAILED');
    // And it stays a draft.
    expect(api.world.contractVersions.find((v) => v.id === version.id)?.status).toBe('DRAFT');
  });

  it('tells the submitter why the publish is not theirs to do', async () => {
    const version = versionOf('DRAFT');
    version.status = 'UNDER_REVIEW';
    version.submittedBy = api.world.accounts[0]!.actorId;

    mount(`/contract-versions/${version.id}`);
    await login('admin.a');

    await screen.findByText('İncelemede');
    expect(await screen.findByText(/gönderen kişi olamaz/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Yayınla' })).not.toBeInTheDocument();
  });

  it('lets a second person publish after a password re-entry', async () => {
    const version = versionOf('DRAFT');
    // Put it in review, submitted by admin.a, and clear the way for its period.
    const live = versionOf('PUBLISHED');
    live.validTo = version.validFrom;
    version.status = 'UNDER_REVIEW';
    version.submittedBy = api.world.accounts[0]!.actorId;

    mount(`/contract-versions/${version.id}`);
    const user = await loginToDemoA('both.ab');
    await screen.findByText('İncelemede');

    await user.click(screen.getByRole('button', { name: 'Yayınla' }));
    const confirm = await screen.findByRole('dialog', { name: 'Yayınla' });
    await user.click(within(confirm).getByRole('button', { name: 'Yayınla' }));

    const stepUp = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
    await user.type(within(stepUp).getByLabelText(/^Parola/), PASSWORD);
    await user.click(within(stepUp).getByRole('button', { name: 'Doğrula' }));

    await waitFor(() =>
      expect(api.world.contractVersions.find((v) => v.id === version.id)?.status).toBe('PUBLISHED'),
    );
  });
});
