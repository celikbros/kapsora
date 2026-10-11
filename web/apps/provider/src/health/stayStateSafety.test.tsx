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

async function mount(stayId: string) {
  const services = createServices({ baseUrl: 'http://mock.test' });
  const history = createMemoryHistory({ initialEntries: [`/stays/${stayId}`] });
  render(<App services={services} history={history} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'provider.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  await screen.findByTestId('segments-editor');
  return { services, history, user };
}

it('keeps cached equal-version stays and their draft segments and dialogs separate', async () => {
  const a = api.world.inpatientStays.find((row) => row.status === 'ADMITTED')!;
  const b = { ...a, id: api.world.nextId() };
  api.world.inpatientStays.push(b);
  api.world.staySegments.push({
    ...api.world.staySegments.find((row) => row.stayId === a.id)!,
    id: api.world.nextId(),
    stayId: b.id,
    roomCode: 'B-ONLY',
  });
  const { services, history, user } = await mount(a.id);
  await act(async () => history.push(`/stays/${b.id}`));
  await screen.findByDisplayValue('B-ONLY');
  await act(async () => history.push(`/stays/${a.id}`));
  await screen.findByDisplayValue('A-214');
  const room = screen.getByDisplayValue('A-214');
  await user.clear(room);
  await user.type(room, 'A-DRAFT');
  await user.click(screen.getByTestId('extend-button'));
  await user.type(screen.getByLabelText(/^Gerekçe kodu/), 'COMPLICATION');
  expect(screen.getByRole('dialog')).toBeInTheDocument();

  await act(async () => history.push(`/stays/${b.id}`));
  const editor = await screen.findByTestId('segments-editor');
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(within(editor).getByDisplayValue('B-ONLY')).toBeInTheDocument();
  expect(within(editor).queryByDisplayValue('A-DRAFT')).toBeNull();
  const put = vi.spyOn(services.ops.stays, 'putSegments');
  await user.click(screen.getByRole('button', { name: 'Segmentleri kaydet' }));
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
  expect(put.mock.calls[0]![1]).toBe(b.id);
  expect(put.mock.calls[0]![3].items).toEqual([expect.objectContaining({ roomCode: 'B-ONLY' })]);
  expect(await screen.findByText('Segmentler kaydedildi.')).toBeInTheDocument();
});

it('invalidates a dialog after a same-stay status change and allows a fresh eligible command', async () => {
  const stay = api.world.inpatientStays.find((row) => row.status === 'ADMITTED')!;
  const { services, user } = await mount(stay.id);
  await user.click(screen.getByTestId('extend-button'));
  await user.type(screen.getByLabelText(/^Gerekçe kodu/), 'COMPLICATION');
  const tenantId = services.store.getState().activeTenant!.tenant.id;
  const key = ['provider', tenantId, 'stay', stay.id];
  const loaded = services.queryClient.getQueryData<{
    data: { status: string; rowVersion: number };
  }>(key)!;
  await act(async () => {
    services.queryClient.setQueryData(key, {
      ...loaded,
      data: { ...loaded.data, status: 'AUTHORIZED', rowVersion: loaded.data.rowVersion + 1 },
    });
  });
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(screen.getByTestId('extend-button')).toBeInTheDocument();
  await act(async () => services.queryClient.setQueryData(key, loaded));
  expect(screen.queryByRole('dialog')).toBeNull();
  await user.click(screen.getByTestId('extend-button'));
  expect(screen.getByRole('dialog')).toBeInTheDocument();
  expect(screen.getByLabelText(/^Gerekçe kodu/)).toHaveValue('');
  expect(screen.getByRole('button', { name: 'Uzatma iste' })).toBeDisabled();
  await user.type(screen.getByLabelText(/^Gerekçe kodu/), 'FRESH');
  expect(screen.getByRole('button', { name: 'Uzatma iste' })).toBeEnabled();
  await user.click(screen.getByRole('button', { name: 'Uzatma iste' }));
  expect(await screen.findByText('Uzatma talebi oluşturuldu.')).toBeInTheDocument();
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
});

it('does not let an old stay command completion close or announce over a new stay dialog', async () => {
  const a = api.world.inpatientStays.find((row) => row.status === 'ADMITTED')!;
  const b = { ...a, id: api.world.nextId() };
  api.world.inpatientStays.push(b);
  const { services, history, user } = await mount(a.id);
  const originalExtend = services.ops.stays.extend.bind(services.ops.stays);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  vi.spyOn(services.ops.stays, 'extend').mockImplementation(async (...args) => {
    await held;
    return originalExtend(...args);
  });
  await user.click(screen.getByTestId('extend-button'));
  await user.type(screen.getByLabelText(/^Gerekçe kodu/), 'OLD');
  await user.click(screen.getByRole('button', { name: 'Uzatma iste' }));
  await waitFor(() => expect(services.ops.stays.extend).toHaveBeenCalledTimes(1));

  await act(async () => history.push(`/stays/${b.id}`));
  await screen.findByTestId('stay-figures');
  await user.click(screen.getByTestId('extend-button'));
  await user.type(screen.getByLabelText(/^Gerekçe kodu/), 'FRESH');
  await act(async () => release());
  await waitFor(() =>
    expect(
      api.world.stayExtensions.some((row) => row.stayId === a.id && row.reasonCode === 'OLD'),
    ).toBe(true),
  );
  await waitFor(() =>
    expect(
      services.queryClient
        .getMutationCache()
        .getAll()
        .every((mutation) => mutation.state.status !== 'pending'),
    ).toBe(true),
  );
  expect(screen.getByRole('dialog')).toBeInTheDocument();
  expect(screen.getByLabelText(/^Gerekçe kodu/)).toHaveValue('FRESH');
  expect(screen.queryByText('Uzatma talebi oluşturuldu.')).toBeNull();
});
