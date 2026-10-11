import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';

import { App } from '../App';
import { createServices } from '../services';

const { api, server } = createMockServer();

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  vi.restoreAllMocks();
  api.reset();
});
afterAll(() => server.close());

async function mount(reportId: string, services: ReturnType<typeof createServices>) {
  render(
    <App
      services={services}
      history={createMemoryHistory({ initialEntries: [`/reports/${reportId}`] })}
    />,
  );
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'provider.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

it('surfaces chain read failures, keeps history after a failed refresh, and recovers on retry', async () => {
  const report = api.world.medicalReports.find((candidate) => candidate.status === 'APPROVED')!;
  const later = {
    ...report,
    id: crypto.randomUUID(),
    versionNo: 2,
    rootReportId: report.rootReportId,
    supersedesReportId: report.id,
  };
  api.world.medicalReports.push(later);

  const services = createServices({ baseUrl: 'http://mock.test' });
  const list = services.ops.reports.list.bind(services.ops.reports);
  let failChainReads = true;
  vi.spyOn(services.ops.reports, 'list').mockImplementation((...args) =>
    args[1]?.rootReportId && failChainReads
      ? Promise.reject(new Error('report history unavailable'))
      : list(...args),
  );

  const user = await mount(report.id, services);
  const card = await screen.findByText('Sürümler').then((heading) => heading.parentElement!);
  await within(card).findByRole('alert');
  expect(within(card).getByRole('button', { name: 'Yeniden dene' })).toBeInTheDocument();
  expect(within(card).queryByTestId('report-versions')).toBeNull();
  expect(screen.queryByRole('button', { name: 'Düzeltme oluştur' })).toBeNull();

  failChainReads = false;
  await user.click(within(card).getByRole('button', { name: 'Yeniden dene' }));
  const versions = await within(card).findByTestId('report-versions');
  expect(within(versions).getByText('v2')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Düzeltme oluştur' })).toBeNull();

  failChainReads = true;
  await services.queryClient.invalidateQueries({
    predicate: (query) =>
      query.queryKey[3] === 'chain' && query.queryKey[4] === report.rootReportId,
  });
  await within(card).findByRole('alert');
  expect(within(card).getByTestId('report-versions')).toBeInTheDocument();
  expect(within(versions).getByText('v2')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Düzeltme oluştur' })).toBeNull();

  failChainReads = false;
  await user.click(within(card).getByRole('button', { name: 'Yeniden dene' }));
  await waitFor(() => expect(within(card).queryByRole('alert')).toBeNull());
  expect(within(card).getByTestId('report-versions')).toBeInTheDocument();
});

it('keeps a successful single-version report free of history and enables correction after the chain loads', async () => {
  const report = api.world.medicalReports.find((candidate) => candidate.status === 'APPROVED')!;
  const services = createServices({ baseUrl: 'http://mock.test' });
  const list = services.ops.reports.list.bind(services.ops.reports);
  let chainReadState: 'success' | 'pending' | 'error' = 'success';
  let rejectPendingRead: ((reason: unknown) => void) | undefined;
  vi.spyOn(services.ops.reports, 'list').mockImplementation((...args) => {
    if (!args[1]?.rootReportId) return list(...args);
    if (chainReadState === 'error') return Promise.reject(new Error('history refresh failed'));
    if (chainReadState === 'pending') {
      return new Promise<Awaited<ReturnType<typeof list>>>((_, reject) => {
        rejectPendingRead = reject;
      });
    }
    return list(...args);
  });

  const user = await mount(report.id, services);
  await screen.findByText(report.reference);
  expect(screen.queryByText('Sürümler')).toBeNull();
  expect(await screen.findByRole('button', { name: 'Düzeltme oluştur' })).toBeInTheDocument();

  chainReadState = 'pending';
  const refresh = services.queryClient.invalidateQueries({
    predicate: (query) =>
      query.queryKey[3] === 'chain' && query.queryKey[4] === report.rootReportId,
  });
  await waitFor(() => expect(rejectPendingRead).toBeTypeOf('function'));
  await waitFor(() =>
    expect(screen.queryByRole('button', { name: 'Düzeltme oluştur' })).toBeNull(),
  );

  chainReadState = 'error';
  rejectPendingRead!(new Error('history refresh failed'));
  await refresh;
  const retry = await screen.findByRole('button', { name: 'Yeniden dene' });
  const card = retry.closest('section')!;
  await within(card).findByRole('alert');
  expect(within(card).getByRole('button', { name: 'Yeniden dene' })).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Düzeltme oluştur' })).toBeNull();

  chainReadState = 'success';
  await user.click(within(card).getByRole('button', { name: 'Yeniden dene' }));
  await waitFor(() =>
    expect(screen.queryByRole('button', { name: 'Düzeltme oluştur' })).toBeInTheDocument(),
  );
  expect(screen.queryByText('Sürümler')).toBeNull();
});
