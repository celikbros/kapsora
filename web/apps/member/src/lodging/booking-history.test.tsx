import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';

import { App } from '../App';
import { createServices } from '../services';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  vi.restoreAllMocks();
  api.reset();
});
afterAll(() => server.close());

it('opens an older booking from a second cursor page without a booking command', async () => {
  const account = api.world.accounts.find((a) => a.username === 'member.a')!;
  const personId = account.memberships[0]!.scopes!.find((s) => s.type === 'PERSON')!.id!;
  const target = api.world.bookings.find(
    (b) => b.personId === personId && b.status === 'CONFIRMED',
  )!;
  const services = createServices({ baseUrl: 'http://mock.test' });
  const realList = services.ops.lodging.listBookings.bind(services.ops.lodging);
  const list = vi
    .spyOn(services.ops.lodging, 'listBookings')
    .mockImplementation(async (tenantId, query) => {
      const page = await realList(tenantId, { limit: 50 });
      const targetView = page.items.find((b) => b.id === target.id)!;
      const newer = page.items.find((b) => b.id !== target.id)!;
      return query?.cursor
        ? { items: [targetView], nextCursor: null }
        : {
            items: Array.from({ length: 50 }, (_, i) => ({
              ...newer,
              id: crypto.randomUUID(),
              reference: `PAGE-ONE-${i}`,
            })),
            nextCursor: 'opaque-page-two',
          };
    });
  const confirm = vi.spyOn(services.ops.lodging, 'confirm');
  const cancel = vi.spyOn(services.ops.lodging, 'cancel');
  const history = createMemoryHistory({ initialEntries: ['/bookings'] });
  render(<App services={services} history={history} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'member.a');
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));

  const rows = await screen.findAllByTestId('booking-row');
  expect(rows).toHaveLength(50);
  expect(screen.queryByText(target.reference)).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Daha fazla yükle' }));
  await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  expect(list.mock.calls[1]![1]).toEqual({ cursor: 'opaque-page-two', limit: 50 });
  const targetRow = within(screen.getByTestId('booking-list'))
    .getByText(target.reference)
    .closest('a')!;
  await user.click(targetRow);
  await waitFor(() => expect(history.location.pathname).toBe(`/bookings/${target.id}`));
  expect(await screen.findByTestId('booking-status')).toBeInTheDocument();
  expect(confirm).not.toHaveBeenCalled();
  expect(cancel).not.toHaveBeenCalled();
});

it('keeps loaded bookings visible when an older page fails, then retries that cursor', async () => {
  const services = createServices({ baseUrl: 'http://mock.test' });
  const realList = services.ops.lodging.listBookings.bind(services.ops.lodging);
  let attempt = 0;
  const list = vi
    .spyOn(services.ops.lodging, 'listBookings')
    .mockImplementation(async (tenantId, query) => {
      const page = await realList(tenantId, { limit: 50 });
      if (!query?.cursor) return { items: page.items.slice(0, 1), nextCursor: 'retry-cursor' };
      attempt++;
      if (attempt === 1) throw new Error('Page unavailable');
      return { items: page.items.slice(1, 2), nextCursor: null };
    });
  const history = createMemoryHistory({ initialEntries: ['/bookings'] });
  render(<App services={services} history={history} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'member.a');
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));

  expect(await screen.findAllByTestId('booking-row')).toHaveLength(1);
  await user.click(screen.getByRole('button', { name: 'Daha fazla yükle' }));
  await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  expect(screen.getAllByTestId('booking-row')).toHaveLength(1);
  expect(screen.getByRole('alert')).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: 'Daha fazla yükle' }));
  await waitFor(() => expect(screen.getAllByTestId('booking-row')).toHaveLength(2));
  expect(list.mock.calls[1]![1]?.cursor).toBe('retry-cursor');
  expect(list.mock.calls[2]![1]?.cursor).toBe('retry-cursor');
  expect(screen.queryByRole('button', { name: 'Daha fazla yükle' })).toBeNull();
});
