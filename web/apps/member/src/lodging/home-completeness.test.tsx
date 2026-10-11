import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor } from '@testing-library/react';
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

async function openHome(beforeRender?: (services: ReturnType<typeof createServices>) => void) {
  const services = createServices({ baseUrl: 'http://mock.test' });
  beforeRender?.(services);
  const history = createMemoryHistory({ initialEntries: ['/'] });
  render(<App services={services} history={history} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'member.a');
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return { services, history, user };
}

function mockBookings(
  services: ReturnType<typeof createServices>,
  status: 'CANCELLED' | 'CONFIRMED' | null,
  nextCursor: string | null,
) {
  const realList = services.ops.lodging.listBookings.bind(services.ops.lodging);
  vi.spyOn(services.ops.lodging, 'listBookings').mockImplementation(async (tenantId) => {
    const page = await realList(tenantId, { limit: 50 });
    const item = status
      ? (page.items.find((booking) => booking.status === status) ??
        (status === 'CANCELLED' && page.items[0]
          ? { ...page.items[0], status: 'CANCELLED' as const }
          : undefined))
      : undefined;
    return { items: item ? [item] : [], nextCursor };
  });
}

it('does not claim there are no bookings when a terminal first page has an older cursor', async () => {
  await openHome((services) => {
    mockBookings(services, 'CANCELLED', 'older-bookings');
  });

  expect(await screen.findByText('Tüm rezervasyonlarını görüntüle')).toBeInTheDocument();
  expect(screen.getByText(/Konaklama tarihlerinizi/)).toBeInTheDocument();
  expect(screen.queryByText('Yaklaşan konaklama')).toBeNull();
  expect(screen.queryByText('Planlanmış konaklamanız yok.')).toBeNull();
  expect(screen.queryByTestId('next-booking')).toBeNull();
});

it('does not present a live booking as the authoritative next stay while older pages remain', async () => {
  await openHome((services) => {
    mockBookings(services, 'CONFIRMED', 'older-bookings');
  });

  expect(await screen.findByText('Tüm rezervasyonlarını görüntüle')).toBeInTheDocument();
  expect(screen.queryByTestId('next-booking')).toBeNull();
  expect(screen.queryByText('Onaylandı')).toBeNull();
  expect(screen.getByTestId('home-all-bookings')).toHaveAttribute('href', '/bookings');
});

it('keeps the next stay and lets the member open all bookings after a complete active result', async () => {
  const { services, history, user } = await openHome((services) => {
    mockBookings(services, 'CONFIRMED', null);
  });

  expect(await screen.findByTestId('next-booking')).toBeInTheDocument();
  expect(screen.getByTestId('home-all-bookings')).toHaveAttribute('href', '/bookings');
  await user.click(screen.getByTestId('home-all-bookings'));
  await waitFor(() => expect(history.location.pathname).toBe('/bookings'));
  expect(services.ops.lodging.listBookings).toHaveBeenCalled();
});

it('keeps the no bookings message for a complete empty result and links to history', async () => {
  await openHome((services) => mockBookings(services, null, null));

  expect(await screen.findByText('Planlanmış konaklamanız yok.')).toBeInTheDocument();
  expect(screen.getByTestId('home-all-bookings')).toHaveAttribute('href', '/bookings');
});
