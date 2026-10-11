import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';

import { App } from '../App';
import { createServices } from '../services';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  vi.restoreAllMocks();
  const permissions = api.world.accounts.find((account) => account.username === 'provider.a')!
    .memberships[0]!.permissions;
  const claimRead = permissions.indexOf('claim.read');
  if (claimRead !== -1) permissions.splice(claimRead, 1);
  api.reset();
});
afterAll(() => server.close());

function fixture() {
  const record = api.world.healthCases.find((c) => c.sensitivity === 'STANDARD')!;
  const report = api.world.medicalReports[0]!;
  report.caseId = record.id;
  const stay = api.world.inpatientStays.find((s) => s.caseId === record.id)!;
  return { record, report, stay };
}

async function mount(
  caseId: string,
  username = 'provider.a',
  services = createServices({ baseUrl: 'http://mock.test' }),
) {
  render(
    <App
      services={services}
      history={createMemoryHistory({ initialEntries: [`/cases/${caseId}`] })}
    />,
  );
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return { services, user };
}

it('shows report and stay read failures without false empty or admission, then recovers existing records', async () => {
  const { record, report, stay } = fixture();
  const services = createServices({ baseUrl: 'http://mock.test' });
  const listReports = services.ops.reports.list.bind(services.ops.reports);
  const listStays = services.ops.stays.list.bind(services.ops.stays);
  let failReports = true;
  let failStays = true;
  vi.spyOn(services.ops.reports, 'list').mockImplementation((...args) =>
    failReports ? Promise.reject(new Error('report read failed')) : listReports(...args),
  );
  vi.spyOn(services.ops.stays, 'list').mockImplementation((...args) =>
    failStays ? Promise.reject(new Error('stay read failed')) : listStays(...args),
  );
  render(
    <App
      services={services}
      history={createMemoryHistory({ initialEntries: [`/cases/${record.id}`] })}
    />,
  );
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'provider.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));

  const reports = await screen.findByTestId('case-reports-chapter');
  const stays = screen.getByTestId('case-stays-chapter');
  await within(reports).findByRole('alert');
  await within(stays).findByRole('alert');
  expect(within(reports).queryByText('Bu vakada rapor yok.')).toBeNull();
  expect(within(stays).queryByText('Bu vakada yatış yok.')).toBeNull();
  expect(within(stays).queryByRole('button', { name: 'Yatış talebi' })).toBeNull();

  failReports = false;
  await user.click(within(reports).getByRole('button', { name: 'Yeniden dene' }));
  expect(await within(reports).findByRole('link', { name: report.reference })).toHaveAttribute(
    'href',
    `/reports/${report.id}`,
  );
  failStays = false;
  await user.click(within(stays).getByRole('button', { name: 'Yeniden dene' }));
  await waitFor(() => expect(within(stays).getByTestId('case-stays')).toBeInTheDocument());
  expect(within(stays).getByRole('link', { name: 'Yatışa git' })).toHaveAttribute(
    'href',
    `/stays/${stay.id}`,
  );
  expect(within(stays).queryByRole('button', { name: 'Yatış talebi' })).toBeNull();
});

it('retries a permitted claim read and retains its row when refresh fails', async () => {
  const record = api.world.healthCases.find((c) => c.sensitivity === 'SENSITIVE')!;
  const claim = api.world.claims.find((c) => c.caseId === record.id)!;
  api.world.accounts
    .find((account) => account.username === 'provider.a')!
    .memberships[0]!.permissions.push('claim.read');
  const services = createServices({ baseUrl: 'http://mock.test' });
  const original = services.ops.claims.list.bind(services.ops.claims);
  let fail = true;
  const list = vi
    .spyOn(services.ops.claims, 'list')
    .mockImplementation((...args) =>
      fail ? Promise.reject(new Error('claim read failed')) : original(...args),
    );
  render(
    <App
      services={services}
      history={createMemoryHistory({ initialEntries: [`/cases/${record.id}`] })}
    />,
  );
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'provider.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  const claims = await screen.findByTestId('case-claims-card');
  await within(claims).findByRole('alert');
  expect(within(claims).queryByText('Claim yok.')).toBeNull();
  fail = false;
  await user.click(within(claims).getByRole('button', { name: 'Yeniden dene' }));
  expect(await within(claims).findByRole('link', { name: claim.reference })).toHaveAttribute(
    'href',
    `/claims/${claim.id}`,
  );
  expect(list).toHaveBeenCalled();

  fail = true;
  const tenantId = services.store.getState().activeTenant!.tenant.id;
  await act(async () => {
    await services.queryClient.refetchQueries({
      queryKey: ['provider', tenantId, 'claims', 'case', record.id],
    });
  });
  await within(claims).findByRole('alert');
  expect(within(claims).getByRole('link', { name: claim.reference })).toBeInTheDocument();
});

it('keeps claims restricted without attempting the claim read or offering retry', async () => {
  const record = api.world.healthCases.find((c) => c.sensitivity === 'STANDARD')!;
  const services = createServices({ baseUrl: 'http://mock.test' });
  const list = vi.spyOn(services.ops.claims, 'list');
  await mount(record.id, 'provider.a', services);
  const claims = await screen.findByTestId('case-claims-card');
  expect(
    within(claims).getByText('Claim kayıtlarını görmek için ek yetki gerekir.'),
  ).toBeInTheDocument();
  expect(within(claims).queryByRole('alert')).toBeNull();
  expect(within(claims).queryByRole('button', { name: 'Yeniden dene' })).toBeNull();
  expect(list).not.toHaveBeenCalled();
});

it('retains loaded report and stay rows on refresh failure and blocks admission until stays recover', async () => {
  const { record, report, stay } = fixture();
  stay.status = 'DISCHARGED';
  const { services, user } = await mount(record.id);
  const reports = await screen.findByTestId('case-reports-chapter');
  const stays = screen.getByTestId('case-stays-chapter');
  await within(reports).findByRole('link', { name: report.reference });
  await within(stays).findByTestId('case-stays');
  expect(within(stays).getByRole('button', { name: 'Yatış talebi' })).toBeInTheDocument();
  await user.click(within(stays).getByRole('button', { name: 'Yatış talebi' }));
  expect(within(stays).getByTestId('admit-form')).toBeInTheDocument();

  vi.spyOn(services.ops.reports, 'list').mockRejectedValue(new Error('report refresh failed'));
  const listStays = services.ops.stays.list.bind(services.ops.stays);
  let failStays = true;
  vi.spyOn(services.ops.stays, 'list').mockImplementation((...args) =>
    failStays ? Promise.reject(new Error('stay refresh failed')) : listStays(...args),
  );
  const tenantId = services.store.getState().activeTenant!.tenant.id;
  await act(async () => {
    await Promise.all([
      services.queryClient.refetchQueries({
        queryKey: ['provider', tenantId, 'reports', 'case', record.id],
      }),
      services.queryClient.refetchQueries({
        queryKey: ['provider', tenantId, 'stays', 'case', record.id],
      }),
    ]);
  });

  await within(reports).findByRole('alert');
  await within(stays).findByRole('alert');
  expect(within(reports).getByRole('link', { name: report.reference })).toBeInTheDocument();
  expect(within(stays).getByTestId('case-stays')).toBeInTheDocument();
  expect(within(stays).queryByRole('button', { name: 'Yatış talebi' })).toBeNull();
  expect(within(stays).queryByTestId('admit-form')).toBeNull();

  failStays = false;
  await user.click(within(stays).getByRole('button', { name: 'Yeniden dene' }));
  await waitFor(() =>
    expect(within(stays).getByRole('button', { name: 'Yatış talebi' })).toBeInTheDocument(),
  );
  expect(within(stays).queryByTestId('admit-form')).toBeNull();
});
