import type { MedicalReportStatus } from '@kapsora/api-client';
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode } from 'react';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';

import { App } from '../App';
import { createServices } from '../api';
import { resetAccessMemory } from './access';

const { api, server } = createMockServer();

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  vi.restoreAllMocks();
  api.reset();
  resetAccessMemory();
});
afterAll(() => server.close());

function reports() {
  const template = api.world.medicalReports.find((row) => row.status === 'APPROVED')!;
  const make = (reference: string) => {
    const id = api.world.nextId();
    const row = {
      ...template,
      id,
      rootReportId: id,
      reference,
      status: 'UNDER_REVIEW' as MedicalReportStatus,
      reviewComment: null,
      rejectReasonCode: null,
      reviewedAt: null,
      reviewedBy: null,
    };
    api.world.medicalReports.push(row);
    return row;
  };
  return { a: make('MR-REVIEW-A'), b: make('MR-REVIEW-B') };
}

async function mount(reportId: string, strict = false, username = 'doctor.a') {
  const services = createServices({ baseUrl: 'http://mock.test' });
  const history = createMemoryHistory({ initialEntries: [`/medical-reports/${reportId}`] });
  const app = <App services={services} history={history} />;
  render(strict ? <StrictMode>{app}</StrictMode> : app);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return { services, history, user };
}

async function onReport(reference: string) {
  await waitFor(() =>
    expect(document.querySelector('main')?.textContent).toContain(`${reference} · v1`),
  );
}

it('discards a cached A rejection when returning to B with the same row version', async () => {
  const { a, b } = reports();
  const { services, history, user } = await mount(b.id);
  await onReport(b.reference);
  await act(async () => history.push(`/medical-reports/${a.id}`));
  await onReport(a.reference);
  await user.click(screen.getByRole('button', { name: 'Reddet' }));
  const dialog = screen.getByRole('dialog');
  await user.type(within(dialog).getByRole('textbox', { name: /gerekçesi kodu/i }), 'A_ONLY');
  await user.type(within(dialog).getByRole('textbox', { name: /İnceleme notu/ }), 'A comment only');

  await act(async () => history.push(`/medical-reports/${b.id}`));
  await onReport(b.reference);
  expect(screen.queryByRole('dialog')).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Reddet' }));
  expect(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /gerekçesi kodu/i }),
  ).toHaveValue('');
  expect(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /İnceleme notu/ }),
  ).toHaveValue('');

  const reject = vi.spyOn(services.ops.reports, 'reject');
  await user.type(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /gerekçesi kodu/i }),
    'B_ONLY',
  );
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Reddet' }));
  await waitFor(() => expect(reject).toHaveBeenCalledTimes(1));
  expect(reject.mock.calls[0]![1]).toBe(b.id);
  expect(reject.mock.calls[0]![3]).toEqual({ rejectReasonCode: 'B_ONLY', reviewComment: null });
});

it('closes a decision after a same-report refresh changes its version', async () => {
  const { b } = reports();
  const { services, user } = await mount(b.id);
  await onReport(b.reference);
  await user.click(screen.getByRole('button', { name: 'Reddet' }));
  await user.type(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /gerekçesi kodu/i }),
    'OLD_REASON',
  );
  await act(async () => services.queryClient.invalidateQueries({ queryKey: ['reports'] }));
  expect(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /gerekçesi kodu/i }),
  ).toHaveValue('OLD_REASON');
  b.rowVersion += 1;
  await act(async () => services.queryClient.invalidateQueries({ queryKey: ['reports'] }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  b.rowVersion -= 1;
  await act(async () => services.queryClient.invalidateQueries({ queryKey: ['reports'] }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  await user.click(screen.getByRole('button', { name: 'Reddet' }));
  expect(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /gerekçesi kodu/i }),
  ).toHaveValue('');
});

it('keeps a new report dialog open when an old rejection settles', async () => {
  const { a, b } = reports();
  const { services, history, user } = await mount(a.id);
  const originalReject = services.ops.reports.reject.bind(services.ops.reports);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const reject = vi.spyOn(services.ops.reports, 'reject').mockImplementation(async (...args) => {
    if (args[1] === a.id) await held;
    return originalReject(...args);
  });
  await onReport(a.reference);
  await user.click(screen.getByRole('button', { name: 'Reddet' }));
  await user.type(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /gerekçesi kodu/i }),
    'A_ONLY',
  );
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Reddet' }));
  await waitFor(() => expect(reject).toHaveBeenCalledTimes(1));
  await act(async () => history.push(`/medical-reports/${b.id}`));
  await onReport(b.reference);
  await user.click(screen.getByRole('button', { name: 'Reddet' }));
  await user.type(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /gerekçesi kodu/i }),
    'B_ONLY',
  );
  await act(async () => release());
  await waitFor(() =>
    expect(api.world.medicalReports.find((row) => row.id === a.id)?.status).toBe('REJECTED'),
  );
  await waitFor(() =>
    expect(
      services.queryClient
        .getMutationCache()
        .getAll()
        .every((mutation) => mutation.state.status !== 'pending'),
    ).toBe(true),
  );
  expect(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /gerekçesi kodu/i }),
  ).toHaveValue('B_ONLY');
  expect(screen.queryByText('Rapor reddedildi.')).toBeNull();
});

it('shows normal success feedback for a decision on the current report', async () => {
  const { b } = reports();
  const { user } = await mount(b.id, true);
  await onReport(b.reference);
  await user.click(screen.getByRole('button', { name: 'Reddet' }));
  await user.type(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /gerekçesi kodu/i }),
    'B_ONLY',
  );
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Reddet' }));
  await waitFor(() => expect(b.status).toBe('REJECTED'));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(await screen.findByText('Rapor reddedildi.')).toBeInTheDocument();
});

it('does not reopen a decision after a failed read recovers', async () => {
  const { b } = reports();
  const { services, user } = await mount(b.id);
  await onReport(b.reference);
  await user.click(screen.getByRole('button', { name: 'Reddet' }));
  const originalGet = services.ops.reports.get.bind(services.ops.reports);
  let fail = true;
  vi.spyOn(services.ops.reports, 'get').mockImplementation((...args) =>
    fail ? Promise.reject(new Error('temporary read failure')) : originalGet(...args),
  );
  await act(async () => services.queryClient.invalidateQueries({ queryKey: ['reports'] }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  fail = false;
  await user.click(await screen.findByRole('button', { name: 'Yeniden dene' }));
  await onReport(b.reference);
  expect(screen.queryByRole('dialog')).toBeNull();
});

it('closes a decision when status eligibility changes', async () => {
  const { b } = reports();
  const { services, user } = await mount(b.id);
  await onReport(b.reference);
  await user.click(screen.getByRole('button', { name: 'Reddet' }));
  b.status = 'APPROVED';
  await act(async () => services.queryClient.invalidateQueries({ queryKey: ['reports'] }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  b.status = 'UNDER_REVIEW';
  await act(async () => services.queryClient.invalidateQueries({ queryKey: ['reports'] }));
  expect(screen.queryByRole('dialog')).toBeNull();
  await user.click(await screen.findByRole('button', { name: 'Reddet' }));
  expect(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /gerekçesi kodu/i }),
  ).toHaveValue('');
});

it('approves the current report and shows normal success feedback', async () => {
  const { b } = reports();
  const { services, user } = await mount(b.id);
  const approve = vi.spyOn(services.ops.reports, 'approve');
  await onReport(b.reference);
  await user.click(screen.getByRole('button', { name: 'Onayla' }));
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Onayla' }));
  await waitFor(() => expect(approve).toHaveBeenCalledTimes(1));
  expect(approve.mock.calls[0]![1]).toBe(b.id);
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(await screen.findByText('Rapor onaylandı.')).toBeInTheDocument();
});

it('ignores a rejection result after leaving the report route', async () => {
  const { a } = reports();
  const { services, history, user } = await mount(a.id);
  const originalReject = services.ops.reports.reject.bind(services.ops.reports);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const reject = vi.spyOn(services.ops.reports, 'reject').mockImplementation(async (...args) => {
    await held;
    return originalReject(...args);
  });
  await onReport(a.reference);
  await user.click(screen.getByRole('button', { name: 'Reddet' }));
  await user.type(
    within(screen.getByRole('dialog')).getByRole('textbox', { name: /gerekçesi kodu/i }),
    'A_ONLY',
  );
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Reddet' }));
  await waitFor(() => expect(reject).toHaveBeenCalledTimes(1));
  await act(async () => history.push('/medical-reports'));
  await screen.findByRole('combobox', { name: 'Durum' });
  await act(async () => release());
  await waitFor(() =>
    expect(api.world.medicalReports.find((row) => row.id === a.id)?.status).toBe('REJECTED'),
  );
  await waitFor(() =>
    expect(
      services.queryClient
        .getMutationCache()
        .getAll()
        .every((mutation) => mutation.state.status !== 'pending'),
    ).toBe(true),
  );
  expect(screen.queryByText('Rapor reddedildi.')).toBeNull();
});
