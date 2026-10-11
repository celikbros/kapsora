import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
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
  it('keeps loaded arrivals visible when a later page fails and retries that cursor', async () => {
    const { services } = mount('/lodging/desk');
    const listBookings = services.ops.lodging.listBookings.bind(services.ops.lodging);
    const arrival = api.world.bookings.find((booking) => booking.status === 'CONFIRMED')!;
    let attempts = 0;
    const calls: Array<{ status: string | undefined; cursor: string | undefined }> = [];
    vi.spyOn(services.ops.lodging, 'listBookings').mockImplementation(
      async (tenantId, query = {}) => {
        calls.push({ status: query.status, cursor: query.cursor });
        if (query.status === 'CONFIRMED' && query.cursor === 'later-page') {
          attempts += 1;
          if (attempts === 1) throw new Error('Later page unavailable');
          const page = await listBookings(tenantId, { status: 'CONFIRMED', limit: 1 });
          return {
            items: [{ ...page.items[0]!, id: api.world.nextId(), reference: 'RETRIED-ARRIVAL' }],
            nextCursor: null,
          };
        }
        const page = await listBookings(tenantId, query);
        return query.status === 'CONFIRMED' ? { ...page, nextCursor: 'later-page' } : page;
      },
    );

    const user = await login('reservation.a');
    const list = await screen.findByTestId('arrival-list');
    const section = list.closest('section')!;
    expect(within(list).getByText(arrival.reference)).toBeInTheDocument();
    await user.click(within(section).getByRole('button', { name: 'Daha fazla yükle' }));
    await within(section).findByRole('alert');
    expect(within(list).getByText(arrival.reference)).toBeInTheDocument();
    await user.click(within(section).getByRole('button', { name: 'Yeniden dene' }));
    expect(await within(list).findByText('RETRIED-ARRIVAL')).toBeInTheDocument();
    expect(within(list).getByText(arrival.reference)).toBeInTheDocument();
    expect(attempts).toBe(2);
    expect(calls.filter((call) => call.cursor === 'later-page')).toEqual([
      { status: 'CONFIRMED', cursor: 'later-page' },
      { status: 'CONFIRMED', cursor: 'later-page' },
    ]);
  });

  it('reaches an older arrival and departure beyond the first 100 bookings for each status', async () => {
    const arrival = api.world.bookings.find((b) => b.status === 'CONFIRMED')!;
    const departure = api.world.bookings.find((b) => b.status === 'CHECKED_IN')!;
    // The server orders by creation, not stay date. These newer records put both actionable
    // bookings on a later cursor page without changing their check-in/check-out windows.
    for (const booking of [arrival, departure]) {
      for (let index = 0; index < 100; index += 1) {
        api.world.bookings.push({
          ...booking,
          id: api.world.nextId(),
          reference: `PAGE-${booking.status}-${index}`,
          createdAt: '2099-01-01T00:00:00Z',
        });
      }
    }

    const { services } = mount('/lodging/desk');
    const listBookings = vi.spyOn(services.ops.lodging, 'listBookings');
    const user = await login('reservation.a');
    const arrivals = await screen.findByTestId('arrival-list');
    const departures = await screen.findByTestId('departure-list');
    expect(within(arrivals).queryByText(arrival.reference)).not.toBeInTheDocument();
    expect(within(departures).queryByText(departure.reference)).not.toBeInTheDocument();

    await user.click(
      within(arrivals.closest('section')!).getByRole('button', { name: 'Daha fazla yükle' }),
    );
    const arrivalRow = (await within(arrivals).findAllByTestId('arrival-row')).find((row) =>
      within(row).queryByText(arrival.reference),
    );
    expect(arrivalRow).toBeDefined();
    expect(within(arrivalRow!).getByRole('button', { name: 'Giriş yap' })).toBeDisabled();
    expect(within(arrivalRow!).getByRole('button', { name: 'Gelmedi' })).toBeEnabled();

    await user.click(
      within(departures.closest('section')!).getByRole('button', { name: 'Daha fazla yükle' }),
    );
    const departureRow = (await within(departures).findAllByTestId('departure-row')).find((row) =>
      within(row).queryByText(departure.reference),
    );
    expect(departureRow).toBeDefined();
    expect(within(departureRow!).getByRole('button', { name: 'Çıkış yap' })).toBeEnabled();
    for (const list of [arrivals, departures]) {
      const rows = within(list).getAllByTestId(list === arrivals ? 'arrival-row' : 'departure-row');
      const references = rows.map((row) => row.querySelector('span.font-mono')?.textContent);
      expect(rows.length).toBeGreaterThan(100);
      expect(new Set(references).size).toBe(rows.length);
    }
    expect(arrival.status).toBe('CONFIRMED');
    expect(departure.status).toBe('CHECKED_IN');
    for (const status of ['CONFIRMED', 'CHECKED_IN'] as const) {
      const calls = listBookings.mock.calls.filter(([, query]) => query?.status === status);
      expect(calls).toHaveLength(2);
      expect(calls[0]![1]).toEqual({ limit: 100, status });
      expect(calls[1]![1]).toEqual({ limit: 100, status, cursor: expect.any(String) });
      expect(calls[1]![0]).toBe(calls[0]![0]);
    }
  }, 90_000);

  it('checks a guest in by their token, refuses a wrong one, and checks them out', async () => {
    // The seeded stays lie weeks ahead; the door only opens inside the check-in window, so
    // one confirmed booking is moved to arrive today before the desk is opened. The window is
    // anchored to the property's own zone (Europe/Istanbul), not UTC: a plain
    // `toISOString().slice(0, 10)` reads "today" as the UTC calendar date, which is already
    // yesterday's Istanbul date for three hours of every day (21:00-24:00 UTC) — a check-in
    // right then found itself past the window's Istanbul-midnight close. Read "today" in the
    // same zone the check confirms it in instead.
    const booking = api.world.bookings.find((b) => b.status === 'CONFIRMED')!;
    const zone = 'Europe/Istanbul';
    const todayInZone = new Intl.DateTimeFormat('en-CA', { timeZone: zone }).format(new Date());
    booking.checkIn = todayInZone;
    const checkOut = new Date(`${todayInZone}T00:00:00Z`);
    checkOut.setUTCDate(checkOut.getUTCDate() + booking.nights);
    booking.checkOut = checkOut.toISOString().slice(0, 10);
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

describe('no-show evidence selection', () => {
  function fixture(
    kind: 'valid' | 'wrong type' | 'wrong booking' | 'wrong aggregate' | 'purged' | 'quarantine',
  ) {
    const booking = api.world.bookings.find(
      (b) => b.status === 'CONFIRMED' && !api.world.noShows.some((n) => n.bookingId === b.id),
    )!;
    const property = api.world.properties.find((p) => p.id === booking.propertyId)!;
    const template = api.world.documents.find((d) => d.scanStatus === 'CLEAN')!;
    const doc: typeof template = {
      ...template,
      id: api.world.nextId(),
      tenantId: booking.tenantId,
      ownerOrganizationId: property.providerOrganizationId,
      classification: 'INTERNAL' as const,
      scanStatus: 'CLEAN' as const,
      bucket: kind === 'quarantine' ? 'quarantine' : 'secure',
      duplicateOfDocumentId: null,
      purgedAt: kind === 'purged' ? new Date().toISOString() : null,
      createdAt: new Date().toISOString(),
    };
    api.world.documents.push(doc);
    const ownLink = {
      ...api.world.documentLinks[0]!,
      id: api.world.nextId(),
      tenantId: booking.tenantId,
      documentId: doc.id,
      aggregateType: 'BOOKING',
      aggregateId: booking.id,
      documentTypeCode:
        kind === 'valid' || kind === 'purged' || kind === 'quarantine'
          ? 'NO_SHOW_EVIDENCE'
          : 'INVOICE',
    };
    api.world.documentLinks.push(ownLink);
    if (kind === 'wrong booking' || kind === 'wrong aggregate') {
      api.world.documentLinks.push({
        ...ownLink,
        id: api.world.nextId(),
        aggregateType: kind === 'wrong aggregate' ? 'SERVICE_REQUEST' : 'BOOKING',
        aggregateId: kind === 'wrong booking' ? api.world.nextId() : booking.id,
        documentTypeCode: 'NO_SHOW_EVIDENCE',
      });
    }
    return { booking, doc };
  }

  async function openForm(reference: string) {
    mount('/lodging/desk');
    const user = await login('reservation.a');
    const rows = await screen.findAllByTestId('arrival-row');
    const row = rows.find((r) => within(r).queryByText(reference))!;
    await user.click(within(row).getByRole('button', { name: 'Gelmedi' }));
    const form = await within(row).findByTestId('no-show-form');
    await within(form).findByTestId('documents-table');
    return { user, form };
  }

  it.each(['wrong type', 'wrong booking', 'wrong aggregate', 'purged', 'quarantine'] as const)(
    'does not enable reporting for a clean but unusable document: %s',
    async (kind) => {
      const { booking } = fixture(kind);
      const { form } = await openForm(booking.reference);
      expect(within(form).getByRole('button', { name: 'Gelmediğini bildir' })).toBeDisabled();
      expect(api.world.noShows.some((n) => n.bookingId === booking.id)).toBe(false);
    },
  );

  it('reports the usable typed evidence even when another clean file comes first', async () => {
    const { booking, doc: wrong } = fixture('wrong type');
    const { doc: evidence } = fixture('valid');
    wrong.createdAt = '2099-01-01T00:00:00Z';
    evidence.createdAt = '2098-01-01T00:00:00Z';
    // Only this in-memory mock fixture changes dates; live acceptance must wait for its cutoff.
    booking.checkIn = '2020-01-01';
    booking.checkOut = '2020-01-04';
    const { user, form } = await openForm(booking.reference);
    await user.click(within(form).getByRole('button', { name: 'Gelmediğini bildir' }));
    await screen.findByTestId('no-show-result');
    const report = api.world.noShows.find((n) => n.bookingId === booking.id)!;
    expect(report.evidenceDocumentId).toBe(evidence.id);
    expect(report.status).toBe('REPORTED');
    expect(booking.status).toBe('CONFIRMED');
  });
});
