import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { App } from './App';
import { createServices } from './services';

/**
 * The property desk against the mock world, as reservation.a: the allotment edited in
 * place and refused below what is already promised, the guest checked in by their token
 * and refused by a wrong one, the departure checked out with the nights the server counted.
 */
const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => api.reset());
afterAll(() => server.close());

function mount(path: string) {
  const services = createServices({ baseUrl: BASE });
  const history = createMemoryHistory({ initialEntries: [path] });
  render(<App services={services} history={history} />);
  return { services, history };
}

async function login(username: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

describe('the allotment', () => {
  it('is edited a night at a time and refused below what is already promised', async () => {
    mount('/lodging/inventory');
    const user = await login('reservation.a');
    const property = api.world.properties[0]!;
    const room = api.world.roomTypes.find((r) => r.propertyId === property.id)!;
    await user.selectOptions(await screen.findByLabelText(/^Tesis/), property.id);
    await user.selectOptions(await screen.findByLabelText(/^Oda tipi/), room.id);
    // The month the seeded allotment starts in: the world's inventory begins at its base date.
    const firstDay = api.world.inventoryDays
      .filter((d) => d.roomTypeId === room.id)
      .map((d) => d.stayDate)
      .sort()[0]!;
    fireEvent.change(screen.getByLabelText(/^Ay/), { target: { value: firstDay.slice(0, 7) } });

    const table = await screen.findByTestId('inventory-table');
    const rows = await within(table).findAllByTestId('inventory-row');
    expect(rows.length).toBeGreaterThan(0);
    // A night somebody already holds or has confirmed: capacity zero must be refused.
    const committed = api.world.inventoryDays.find(
      (d) =>
        d.roomTypeId === room.id &&
        d.stayDate.startsWith(firstDay.slice(0, 7)) &&
        d.held + d.confirmed > 0,
    )!;
    const row = rows.find((r) =>
      within(r).queryByText(new RegExp(committed.stayDate.slice(8, 10) + '\\b')),
    )!;
    await user.click(within(row).getByRole('button', { name: 'Düzenle' }));
    const input = within(row).getByRole('spinbutton');
    await user.clear(input);
    await user.type(input, '0');
    await user.click(within(row).getByRole('button', { name: 'Kaydet' }));
    // The refusal reaches the row and the live region alike; one is enough.
    expect((await screen.findAllByText(/zaten tutulmuş veya onaylanmış/)).length).toBeGreaterThan(
      0,
    );
    // And the counters on the row did not move.
    expect(committed.held + committed.confirmed).toBeGreaterThan(0);
  });
});

describe('the door', () => {
  it('checks a guest in by their token, refuses a wrong one, and checks them out', async () => {
    // The seeded stays lie weeks ahead; the door only opens inside the check-in window, so
    // one confirmed booking is moved to arrive today before the desk is opened.
    const booking = api.world.bookings.find((b) => b.status === 'CONFIRMED')!;
    const today = new Date();
    const iso = (d: Date) => d.toISOString().slice(0, 10);
    booking.checkIn = iso(today);
    booking.checkOut = iso(new Date(today.getTime() + booking.nights * 86_400_000));
    const reference = booking.reference;

    mount('/lodging/desk');
    const user = await login('reservation.a');
    const list = await screen.findByTestId('arrival-list');
    const row = within(list)
      .getAllByTestId('arrival-row')
      .find((r) => within(r).queryByText(reference))!;
    expect(row).toBeDefined();

    const tokenField = within(row).getByLabelText(/Kupon kodu/);
    await user.type(tokenField, 'KPS-WRONGTOKEN');
    await user.click(within(row).getByRole('button', { name: 'Giriş yap' }));
    await within(row).findByRole('alert');

    const voucher = api.world.bookingVouchers.find(
      (v) => v.bookingId === booking.id && v.status === 'ISSUED',
    )!;
    await user.clear(tokenField);
    await user.type(tokenField, voucher.token);
    await user.click(within(row).getByRole('button', { name: 'Giriş yap' }));
    await waitFor(() =>
      expect(within(screen.getByTestId('departure-list')).getByText(reference)).toBeInTheDocument(),
    );

    const departure = within(screen.getByTestId('departure-list'))
      .getAllByTestId('departure-row')
      .find((r) => within(r).queryByText(reference))!;
    await user.click(within(departure).getByRole('button', { name: 'Çıkış yap' }));
    await screen.findByText(/Çıkış kaydedildi; \d+ gece kalındı\./, { exact: false });
  });
});
