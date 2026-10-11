import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
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

it('loads cases beyond the first page, keeps rows on a failed next page, and scopes status changes', async () => {
  const base = api.world.healthCases.find((c) => c.sensitivity === 'STANDARD')!;
  const open = Array.from({ length: 55 }, (_, i) => ({
    ...base,
    id: crypto.randomUUID(),
    status: 'OPEN' as const,
    openedAt: new Date(Date.UTC(2026, 0, i + 1)).toISOString(),
  }));
  const closed = { ...base, id: crypto.randomUUID(), status: 'CLOSED' as const };
  api.world.healthCases.push(...open, closed);

  const services = createServices({ baseUrl: 'http://mock.test' });
  const original = services.ops.health.listCases.bind(services.ops.health);
  let failNextPage = true;
  let firstPageItem: Awaited<ReturnType<typeof original>>['items'][number] | undefined;
  const list = vi.spyOn(services.ops.health, 'listCases').mockImplementation(async (...args) => {
    if (args[1]?.cursor && failNextPage) {
      failNextPage = false;
      throw new Error('next page unavailable');
    }
    const page = await original(...args);
    if (args[1]?.cursor && firstPageItem) {
      return { ...page, items: [firstPageItem, ...page.items] };
    }
    firstPageItem = page.items[0];
    return page;
  });
  render(<App services={services} history={createMemoryHistory({ initialEntries: ['/cases'] })} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'provider.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));

  const table = await screen.findByTestId('case-table');
  expect(within(table).getAllByRole('row')).toHaveLength(51);
  expect(table.querySelector(`a[href="/cases/${open[0]!.id}"]`)).toBeNull();
  expect(list.mock.calls[0]?.[1]).toEqual({ status: 'OPEN', limit: 50 });

  await user.click(screen.getByRole('button', { name: 'Daha fazla yükle' }));
  await screen.findByRole('alert');
  expect(within(table).getAllByRole('row')).toHaveLength(51);
  const attemptedCursor = list.mock.calls[1]?.[1]?.cursor;
  expect(attemptedCursor).toBeTruthy();

  await user.click(screen.getByRole('button', { name: 'Yeniden dene' }));
  await waitFor(() =>
    expect(table.querySelector(`a[href="/cases/${open[0]!.id}"]`)).not.toBeNull(),
  );
  expect(list.mock.calls[2]?.[1]?.cursor).toBe(attemptedCursor);
  expect(within(table).getAllByRole('row')).toHaveLength(58);
  expect(table.querySelectorAll(`a[href="/cases/${firstPageItem!.id}"]`)).toHaveLength(1);

  await user.selectOptions(screen.getByLabelText('Durum'), 'CLOSED');
  await waitFor(() =>
    expect(
      screen.getByTestId('case-table').querySelector(`a[href="/cases/${closed.id}"]`),
    ).not.toBeNull(),
  );
  expect(
    screen.getByTestId('case-table').querySelector(`a[href="/cases/${open[0]!.id}"]`),
  ).toBeNull();
  expect(list.mock.lastCall?.[1]).toEqual({ status: 'CLOSED', limit: 50 });
});
