import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';

import { App } from '../App';
import { createServices } from '../api';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  api.reset();
  vi.restoreAllMocks();
});
afterAll(() => server.close());

function mount() {
  const services = createServices({ baseUrl: 'http://mock.test' });
  const history = createMemoryHistory({ initialEntries: ['/medical-reports'] });
  render(<App services={services} history={history} />);
  return { services, history };
}

async function login() {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'doctor.a');
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

function addNewerSubmittedReports(count: number) {
  const tenantId = api.tenantByCode('DEMO_A')!.id;
  const template = api.world.medicalReports.find((report) => report.tenantId === tenantId);
  expect(template).toBeDefined();
  const targetId = 'f0000000-0000-7000-8000-000000000100';
  const target = {
    ...template!,
    id: targetId,
    rootReportId: targetId,
    reference: 'MR-OLDER-SUBMITTED',
    status: 'SUBMITTED' as const,
    submittedAt: '2029-01-01T00:00:00.000Z',
    createdAt: '2029-01-01T00:00:00.000Z',
  };
  api.world.medicalReports.push(target);
  for (let index = 0; index < count; index++) {
    const id = `f0000000-0000-7000-8000-${index.toString(16).padStart(12, '0')}`;
    api.world.medicalReports.push({
      ...target,
      id,
      rootReportId: id,
      reference: `MR-PAGE-${index.toString().padStart(3, '0')}`,
      createdAt: new Date(Date.UTC(2030, 0, 1, 0, index)).toISOString(),
    });
  }
  return target;
}

it('reaches an older submitted report through the second cursor page and resets on status change', async () => {
  const target = addNewerSubmittedReports(51);
  const { services, history } = mount();
  const list = vi.spyOn(services.ops.reports, 'list');
  const user = await login();

  await waitFor(() =>
    expect(within(screen.getByTestId('report-table')).getAllByRole('row')).toHaveLength(51),
  );
  expect(
    screen.queryByRole('link', { name: `${target.reference} · v${target.versionNo}` }),
  ).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Daha fazla yükle' }));
  await screen.findByRole('link', { name: `${target.reference} · v${target.versionNo}` });
  expect(list).toHaveBeenCalledWith(
    api.tenantByCode('DEMO_A')!.id,
    expect.objectContaining({ status: 'SUBMITTED', limit: 50, cursor: expect.any(String) }),
  );
  await user.selectOptions(screen.getByLabelText('Durum'), 'UNDER_REVIEW');
  await waitFor(() =>
    expect(
      screen.queryByRole('link', { name: `${target.reference} · v${target.versionNo}` }),
    ).toBeNull(),
  );
  expect(list).toHaveBeenLastCalledWith(api.tenantByCode('DEMO_A')!.id, {
    status: 'UNDER_REVIEW',
    limit: 50,
  });
  await user.selectOptions(screen.getByLabelText('Durum'), 'SUBMITTED');
  await user.click(
    await screen.findByRole('link', { name: `${target.reference} · v${target.versionNo}` }),
  );
  await waitFor(() => expect(history.location.pathname).toBe(`/medical-reports/${target.id}`));
});

it('keeps loaded reports visible and retries a failed later page with the same cursor', async () => {
  addNewerSubmittedReports(51);
  const { services } = mount();
  const original = services.ops.reports.list;
  const list = vi.spyOn(services.ops.reports, 'list');
  const user = await login();
  const firstPageLink = within(await screen.findByTestId('report-table')).getAllByRole('link')[0]!;
  const firstPageReference = firstPageLink.textContent;
  let failLaterPage = true;
  list.mockImplementation((tenantId, query, access) => {
    if (query?.cursor && failLaterPage) return Promise.reject(new Error('temporary page failure'));
    return original(tenantId, query, access);
  });

  await user.click(screen.getByRole('button', { name: 'Daha fazla yükle' }));
  expect(
    await screen.findByRole('button', { name: 'Yeniden dene' }, { timeout: 12_000 }),
  ).toBeInTheDocument();
  expect(screen.getByTestId('report-table')).toHaveTextContent(firstPageReference!);
  const failedCursor = list.mock.calls.at(-1)![1]!.cursor;
  failLaterPage = false;
  await user.click(screen.getByRole('button', { name: 'Yeniden dene' }));
  await waitFor(() =>
    expect(within(screen.getByTestId('report-table')).getAllByRole('row').length).toBeGreaterThan(
      51,
    ),
  );
  expect(list.mock.calls.at(-1)![1]!.cursor).toBe(failedCursor);
});

it('offers a retry when the first submitted page fails', async () => {
  addNewerSubmittedReports(1);
  const { services } = mount();
  const original = services.ops.reports.list;
  let failing = true;
  vi.spyOn(services.ops.reports, 'list').mockImplementation((tenantId, query, access) =>
    failing
      ? Promise.reject(new Error('temporary first-page failure'))
      : original(tenantId, query, access),
  );
  const user = await login();

  expect(
    await screen.findByRole('button', { name: 'Yeniden dene' }, { timeout: 12_000 }),
  ).toBeInTheDocument();
  expect(screen.queryByTestId('report-table')).toBeNull();
  failing = false;
  await user.click(screen.getByRole('button', { name: 'Yeniden dene' }));
  expect(await screen.findByTestId('report-table')).toBeInTheDocument();
});

it('does not carry a loaded report page into another tenant', async () => {
  const target = addNewerSubmittedReports(51);
  const tenantB = api.tenantByCode('DEMO_B')!;
  const doctor = api.world.accounts.find((account) => account.username === 'doctor.a')!;
  doctor.memberships.push({
    tenantCode: 'DEMO_B',
    permissions: [...doctor.memberships[0]!.permissions],
  });
  const otherPerson = api.world.people.find((person) => person.tenantId === tenantB.id)!;
  const otherReport = {
    ...target,
    id: 'f0000000-0000-7000-8000-000000000200',
    rootReportId: 'f0000000-0000-7000-8000-000000000200',
    tenantId: tenantB.id,
    personId: otherPerson.id,
    reference: 'MR-TENANT-B',
  };
  api.world.medicalReports.push(otherReport);
  const { services } = mount();
  const list = vi.spyOn(services.ops.reports, 'list');
  const user = await login();
  await user.click((await screen.findAllByRole('button', { name: 'Seç' }))[0]!);
  await user.click(await screen.findByRole('button', { name: 'Daha fazla yükle' }));
  expect(
    await screen.findByRole('link', { name: `${target.reference} · v${target.versionNo}` }),
  ).toBeInTheDocument();

  await act(() => services.store.switchTenant(tenantB.id));
  expect(
    await screen.findByRole('link', {
      name: `${otherReport.reference} · v${otherReport.versionNo}`,
    }),
  ).toBeInTheDocument();
  expect(
    screen.queryByRole('link', { name: `${target.reference} · v${target.versionNo}` }),
  ).toBeNull();
  expect(screen.queryByRole('button', { name: 'Daha fazla yükle' })).toBeNull();
  expect(list).toHaveBeenLastCalledWith(tenantB.id, { status: 'SUBMITTED', limit: 50 });
});
