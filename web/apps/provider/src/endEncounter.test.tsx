import { ApiError } from '@kapsora/api-client';
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';
import { App } from './App';
import { createServices } from './services';

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
async function mount() {
  const row = api.world.healthCases.find((c) => c.sensitivity === 'STANDARD')!;
  row.status = 'OPEN';
  const encounter = api.world.encounters.find((e) => e.caseId === row.id)!;
  encounter.endedAt = null;
  const services = createServices({ baseUrl: 'http://mock.test' });
  render(
    <App
      services={services}
      history={createMemoryHistory({ initialEntries: [`/cases/${row.id}`] })}
    />,
  );
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'provider.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  await user.click(await screen.findByRole('button', { name: 'Muayeneyi sonlandır' }));
  const form = await screen.findByTestId('end-encounter-form');
  return { user, form, encounter, services };
}

it('replays the original end time and command after a committed response is lost', async () => {
  const { user, form, encounter, services } = await mount();
  const notes = encounter.notesClinical;
  const original = services.ops.health.endEncounter.bind(services.ops.health);
  const command = vi
    .spyOn(services.ops.health, 'endEncounter')
    .mockImplementationOnce(async (...args) => {
      await original(...args);
      throw new Error('response lost after commit');
    });
  expect(screen.queryByRole('button', { name: 'Vakayı kapat' })).toBeNull();
  await user.click(within(form).getByRole('button', { name: 'Muayeneyi sonlandır' }));
  await within(form).findByRole('alert');
  const version = encounter.rowVersion;
  expect(within(form).getByLabelText(/Bitiş/)).toBeDisabled();
  await user.click(within(form).getByRole('button', { name: 'Yeniden dene' }));
  await waitFor(() => expect(screen.queryByTestId('end-encounter-form')).toBeNull());
  expect(command).toHaveBeenCalledTimes(2);
  expect(command.mock.calls[1]).toEqual(command.mock.calls[0]);
  expect(encounter.rowVersion).toBe(version);
  expect(encounter.notesClinical).toBe(notes);
  expect(screen.getByRole('button', { name: 'Vakayı kapat' })).toBeInTheDocument();
  const args = command.mock.calls[0]!;
  await expect(
    original(args[0], args[1], args[2], { endedAt: '2026-01-01T00:00:00Z' }, args[4]),
  ).rejects.toMatchObject({ problem: { code: 'IDEMPOTENCY_KEY_REUSED' } });
});

it('requires a valid end time and reloads after a stale version before allowing a fresh command', async () => {
  const { user, form, services } = await mount();
  const input = within(form).getByLabelText(/Bitiş/);
  fireEvent.change(input, { target: { value: '2000-01-01T00:00' } });
  expect(within(form).getByRole('button', { name: 'Muayeneyi sonlandır' })).toBeDisabled();
  expect(within(form).getByText('Bitiş zamanı başlangıçtan önce olamaz.')).toBeInTheDocument();
  fireEvent.change(input, { target: { value: '2026-12-01T12:00' } });
  const command = vi.spyOn(services.ops.health, 'endEncounter').mockRejectedValueOnce(
    new ApiError({
      type: 'about:blank',
      title: 'Changed',
      traceId: 'test',
      status: 412,
      code: 'ETAG_MISMATCH',
    }),
  );
  await user.click(within(form).getByRole('button', { name: 'Muayeneyi sonlandır' }));
  await within(form).findByRole('alert');
  expect(within(form).getByRole('button', { name: 'Yeniden dene' })).toBeDisabled();
  await user.click(within(form).getByRole('button', { name: 'Kaydı yenile' }));
  await waitFor(() => expect(input).toBeEnabled());
  await user.click(within(form).getByRole('button', { name: 'Muayeneyi sonlandır' }));
  await waitFor(() => expect(screen.queryByTestId('end-encounter-form')).toBeNull());
  expect(command.mock.calls[0]![4]).not.toBe(command.mock.calls[1]![4]);
});
