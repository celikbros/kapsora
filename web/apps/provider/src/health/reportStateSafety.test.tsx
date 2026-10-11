import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, render, screen, waitFor } from '@testing-library/react';
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

async function mount(reportId: string) {
  const services = createServices({ baseUrl: 'http://mock.test' });
  const history = createMemoryHistory({ initialEntries: [`/reports/${reportId}`] });
  render(<App services={services} history={history} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'provider.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return { services, history, user };
}

function field(name: string) {
  const input = document.querySelector<HTMLInputElement | HTMLTextAreaElement>(`[name="${name}"]`);
  expect(input).not.toBeNull();
  return input!;
}

it.each([1, 2])(
  'keeps cached report drafts separate when B has row version %i',
  async (bVersion) => {
    const a = api.world.medicalReports.find((row) => row.status === 'DRAFT')!;
    const bId = api.world.nextId();
    const b = {
      ...a,
      id: bId,
      rootReportId: bId,
      reference: 'MR-B-ONLY',
      rowVersion: bVersion,
      reportType: 'B_TYPE',
      clinicalSummary: 'B-SUMMARY',
    };
    api.world.medicalReports.push(b);
    const aLine = api.world.medicalReportServices.find((line) => line.reportId === a.id)!;
    api.world.medicalReportServices.push({
      ...aLine,
      id: api.world.nextId(),
      reportId: b.id,
      notes: 'B-NOTE',
    });

    const { services, history, user } = await mount(b.id);
    await screen.findByDisplayValue('B-SUMMARY');
    await act(async () => history.push(`/reports/${a.id}`));
    await screen.findByDisplayValue(a.clinicalSummary!);
    await user.clear(field('clinicalSummary'));
    await user.type(field('clinicalSummary'), 'A-DRAFT');
    await user.clear(field('services.0.notes'));
    await user.type(field('services.0.notes'), 'A-LINE-DRAFT');

    await act(async () => history.push(`/reports/${b.id}`));
    expect(field('reportType')).toHaveValue('B_TYPE');
    expect(field('clinicalSummary')).toHaveValue('B-SUMMARY');
    expect(field('services.0.notes')).toHaveValue('B-NOTE');
    expect(screen.queryByDisplayValue('A-DRAFT')).toBeNull();
    expect(screen.queryByDisplayValue('A-LINE-DRAFT')).toBeNull();

    const put = vi.spyOn(services.ops.reports, 'putServices');
    await user.click(screen.getByRole('button', { name: 'Hizmetleri kaydet' }));
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    expect(put.mock.calls[0]![1]).toBe(b.id);
    expect(put.mock.calls[0]![3].items).toEqual([expect.objectContaining({ notes: 'B-NOTE' })]);
    await screen.findByText('Hizmetler kaydedildi.');

    const patch = vi.spyOn(services.ops.reports, 'patchDraft');
    await user.click(
      screen.getByTestId('report-header-form').querySelector('button[type="submit"]')!,
    );
    await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
    expect(patch.mock.calls[0]![1]).toBe(b.id);
    expect(patch.mock.calls[0]![3]).toEqual(
      expect.objectContaining({ reportType: 'B_TYPE', clinicalSummary: 'B-SUMMARY' }),
    );
    await screen.findByText('Rapor kaydedildi.');
  },
);

it('ignores a correction completion after moving to another report', async () => {
  const a = api.world.medicalReports.find((row) => row.status === 'APPROVED')!;
  const bId = api.world.nextId();
  const b = { ...a, id: bId, rootReportId: bId, reference: 'MR-B-APPROVED' };
  api.world.medicalReports.push(b);
  const { services, history, user } = await mount(a.id);
  const originalCreate = services.ops.reports.create.bind(services.ops.reports);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  vi.spyOn(services.ops.reports, 'create').mockImplementation(async (...args) => {
    await held;
    return originalCreate(...args);
  });

  await user.click(await screen.findByRole('button', { name: 'Düzeltme oluştur' }));
  await waitFor(() => expect(services.ops.reports.create).toHaveBeenCalledTimes(1));
  await act(async () => history.push(`/reports/${b.id}`));
  await screen.findByText(b.reference);
  await act(async () => release());
  await waitFor(() =>
    expect(api.world.medicalReports.some((row) => row.supersedesReportId === a.id)).toBe(true),
  );
  await waitFor(() =>
    expect(
      services.queryClient
        .getMutationCache()
        .getAll()
        .every((mutation) => mutation.state.status !== 'pending'),
    ).toBe(true),
  );
  expect(history.location.pathname).toBe(`/reports/${b.id}`);
  expect(screen.queryByText('Düzeltme sürümü açıldı.')).toBeNull();
});

it('does not reopen a correction after leaving the report page', async () => {
  const report = api.world.medicalReports.find((row) => row.status === 'APPROVED')!;
  const { services, history, user } = await mount(report.id);
  const originalCreate = services.ops.reports.create.bind(services.ops.reports);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  vi.spyOn(services.ops.reports, 'create').mockImplementation(async (...args) => {
    await held;
    return originalCreate(...args);
  });

  await user.click(await screen.findByRole('button', { name: 'Düzeltme oluştur' }));
  await waitFor(() => expect(services.ops.reports.create).toHaveBeenCalledTimes(1));
  await act(async () => history.push('/cases'));
  await screen.findByRole('heading', { name: 'Vakalar' });
  await act(async () => release());
  await waitFor(() =>
    expect(api.world.medicalReports.some((row) => row.supersedesReportId === report.id)).toBe(true),
  );
  await waitFor(() =>
    expect(
      services.queryClient
        .getMutationCache()
        .getAll()
        .every((mutation) => mutation.state.status !== 'pending'),
    ).toBe(true),
  );
  expect(history.location.pathname).toBe('/cases');
  expect(screen.queryByText('Düzeltme sürümü açıldı.')).toBeNull();
});

it('opens the created correction and confirms it while the source report stays active', async () => {
  const report = api.world.medicalReports.find((row) => row.status === 'APPROVED')!;
  const { history, user } = await mount(report.id);
  await user.click(await screen.findByRole('button', { name: 'Düzeltme oluştur' }));
  await waitFor(() => {
    const correction = api.world.medicalReports.find((row) => row.supersedesReportId === report.id);
    expect(correction).toBeDefined();
    expect(history.location.pathname).toBe(`/reports/${correction!.id}`);
  });
  expect(await screen.findByText('Düzeltme sürümü açıldı.')).toBeInTheDocument();
});
