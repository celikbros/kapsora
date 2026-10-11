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
  api.reset();
});
afterAll(() => server.close());

function fixture(sensitivity: 'STANDARD' | 'SENSITIVE' = 'STANDARD') {
  const record = api.world.healthCases.find((c) => c.sensitivity === sensitivity)!;
  const encounter = api.world.encounters.find((e) => e.caseId === record.id)!;
  const diagnosis = api.world.diagnoses.find((d) => d.encounterId === encounter.id)!;
  const code = api.world.codeValues.find((v) => v.id === diagnosis.codeValueId)!;
  return { record, encounter, code };
}

async function mount(caseId: string, services = createServices({ baseUrl: 'http://mock.test' })) {
  const history = createMemoryHistory({ initialEntries: [`/cases/${caseId}`] });
  render(<App services={services} history={history} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'provider.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return { user, services, history };
}

it('blocks editing through a pending and failed diagnosis read, then retries into the existing set', async () => {
  const { record, encounter, code } = fixture();
  const services = createServices({ baseUrl: 'http://mock.test' });
  const original = services.ops.health.listDiagnoses.bind(services.ops.health);
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  let fail = true;
  vi.spyOn(services.ops.health, 'listDiagnoses').mockImplementation(async (...args) => {
    if (fail) {
      await pending;
      throw new Error('diagnosis read failed');
    }
    return original(...args);
  });
  const put = vi.spyOn(services.ops.health, 'putDiagnoses');
  const { user } = await mount(record.id, services);
  const row = await screen.findByTestId('encounter-row');
  expect(within(row).getByRole('button', { name: 'Tanıları düzenle' })).toBeDisabled();
  expect(screen.queryByTestId('diagnosis-editor')).toBeNull();
  expect(put).not.toHaveBeenCalled();

  await act(async () => release());
  await within(row).findByRole('alert');
  expect(within(row).queryByText('Tanı girilmedi')).toBeNull();
  expect(within(row).getByRole('button', { name: 'Tanıları düzenle' })).toBeDisabled();
  expect(screen.queryByTestId('diagnosis-editor')).toBeNull();

  fail = false;
  await user.click(within(row).getByRole('button', { name: 'Yeniden dene' }));
  await waitFor(() =>
    expect(within(row).getByRole('button', { name: 'Tanıları düzenle' })).toBeEnabled(),
  );
  await user.click(within(row).getByRole('button', { name: 'Tanıları düzenle' }));
  const editor = await screen.findByTestId('diagnosis-editor');
  expect(within(editor).getByText(code.code)).toBeInTheDocument();
  expect(within(editor).getByText(code.display)).toBeInTheDocument();
  expect(put).not.toHaveBeenCalled();
  expect(services.ops.health.listDiagnoses).toHaveBeenCalledWith(expect.any(String), encounter.id);
});

it('closes a stale editor on failed refresh and requires a fresh successful read to reopen', async () => {
  const { record, encounter, code } = fixture();
  const { user, services } = await mount(record.id);
  const row = await screen.findByTestId('encounter-row');
  await waitFor(() =>
    expect(within(row).getByRole('button', { name: 'Tanıları düzenle' })).toBeEnabled(),
  );
  await user.click(within(row).getByRole('button', { name: 'Tanıları düzenle' }));
  expect(
    within(await screen.findByTestId('diagnosis-editor')).getByText(code.code),
  ).toBeInTheDocument();

  const original = services.ops.health.listDiagnoses.bind(services.ops.health);
  let fail = true;
  vi.spyOn(services.ops.health, 'listDiagnoses').mockImplementation((...args) =>
    fail ? Promise.reject(new Error('diagnosis refresh failed')) : original(...args),
  );
  const put = vi.spyOn(services.ops.health, 'putDiagnoses');
  const tenantId = services.store.getState().activeTenant!.tenant.id;
  await act(async () => {
    await services.queryClient.refetchQueries({
      queryKey: ['provider', tenantId, 'diagnoses', encounter.id],
    });
  });
  await within(row).findByRole('alert');
  expect(screen.queryByTestId('diagnosis-editor')).toBeNull();
  expect(within(row).getByRole('button', { name: 'Tanıları düzenle' })).toBeDisabled();
  expect(put).not.toHaveBeenCalled();

  fail = false;
  await user.click(within(row).getByRole('button', { name: 'Yeniden dene' }));
  await waitFor(() =>
    expect(within(row).getByRole('button', { name: 'Tanıları düzenle' })).toBeEnabled(),
  );
  expect(screen.queryByTestId('diagnosis-editor')).toBeNull();
  await user.click(within(row).getByRole('button', { name: 'Tanıları düzenle' }));
  expect(
    within(await screen.findByTestId('diagnosis-editor')).getByText(code.code),
  ).toBeInTheDocument();
  expect(put).not.toHaveBeenCalled();
});

it('does not request diagnoses or expose clinical editing on a financial projection', async () => {
  const { record } = fixture('SENSITIVE');
  const services = createServices({ baseUrl: 'http://mock.test' });
  const list = vi.spyOn(services.ops.health, 'listDiagnoses');
  await mount(record.id, services);
  await screen.findByTestId('financial-note');
  await screen.findByTestId('encounter-table');
  expect(screen.queryByRole('button', { name: 'Tanıları düzenle' })).toBeNull();
  expect(screen.queryByRole('columnheader', { name: 'Tanı' })).toBeNull();
  expect(list).not.toHaveBeenCalled();
});

it('allows editing a successfully loaded empty diagnosis set', async () => {
  const { record } = fixture();
  const services = createServices({ baseUrl: 'http://mock.test' });
  vi.spyOn(services.ops.health, 'listDiagnoses').mockResolvedValue([]);
  const { user } = await mount(record.id, services);
  const row = await screen.findByTestId('encounter-row');
  await waitFor(() =>
    expect(within(row).getByRole('button', { name: 'Tanıları düzenle' })).toBeEnabled(),
  );
  await user.click(within(row).getByRole('button', { name: 'Tanıları düzenle' }));
  const editor = await screen.findByTestId('diagnosis-editor');
  expect(within(editor).getByText('Henüz tanı seçilmedi.')).toBeInTheDocument();
  expect(within(editor).getByRole('button', { name: 'Tanıları kaydet' })).toBeDisabled();
});

it('closes the editor after a successful whole-set save', async () => {
  const { record, code } = fixture();
  const services = createServices({ baseUrl: 'http://mock.test' });
  const put = vi.spyOn(services.ops.health, 'putDiagnoses');
  const { user } = await mount(record.id, services);
  const row = await screen.findByTestId('encounter-row');
  await waitFor(() =>
    expect(within(row).getByRole('button', { name: 'Tanıları düzenle' })).toBeEnabled(),
  );
  await user.click(within(row).getByRole('button', { name: 'Tanıları düzenle' }));
  const editor = await screen.findByTestId('diagnosis-editor');
  expect(within(editor).getByText(code.code)).toBeInTheDocument();
  await user.click(within(editor).getByRole('button', { name: 'Tanıları kaydet' }));
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
  await screen.findByText('Tanılar kaydedildi.');
  await waitFor(() =>
    expect(within(row).getByRole('button', { name: 'Tanıları düzenle' })).toBeEnabled(),
  );
  expect(screen.queryByTestId('diagnosis-editor')).toBeNull();
});

it('does not reopen an editor after navigating away from and back to the case', async () => {
  const standard = fixture('STANDARD');
  const sensitive = fixture('SENSITIVE');
  const { user, history } = await mount(standard.record.id);
  const row = await screen.findByTestId('encounter-row');
  await waitFor(() =>
    expect(within(row).getByRole('button', { name: 'Tanıları düzenle' })).toBeEnabled(),
  );
  await user.click(within(row).getByRole('button', { name: 'Tanıları düzenle' }));
  expect(await screen.findByTestId('diagnosis-editor')).toBeInTheDocument();

  await act(async () => history.push(`/cases/${sensitive.record.id}`));
  await screen.findByTestId('financial-note');
  expect(screen.queryByTestId('diagnosis-editor')).toBeNull();
  expect(screen.queryByText(standard.code.display)).toBeNull();
  await user.click(await screen.findByRole('button', { name: 'Yatış talebi' }));
  const admission = await screen.findByTestId('admit-form');
  expect(
    within(admission).queryByRole('option', { name: new RegExp(standard.code.code) }),
  ).toBeNull();
  await act(async () => history.push(`/cases/${standard.record.id}`));
  await screen.findByTestId('encounter-table');
  expect(screen.queryByTestId('diagnosis-editor')).toBeNull();
});
