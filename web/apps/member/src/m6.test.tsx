import { createMockServer } from '@kapsora/api-client/mocks/node';
import { formatMoney, initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { App } from './App';
import { createServices } from './services';

/**
 * The member's lodging screens against the mock world, as member.a — an account bound to
 * one person whose plan has two nights left. The milestone's own criterion is the first
 * test: the amount the member will pay is on screen, as the server's string, before they
 * confirm anything.
 */
const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  api.reset();
  window.localStorage.clear();
  window.sessionStorage.clear();
});
afterAll(() => server.close());

function mount(path: string) {
  const services = createServices({ baseUrl: BASE });
  const history = createMemoryHistory({ initialEntries: [path] });
  render(<App services={services} history={history} />);
  return { services, history };
}

async function login(username = 'member.a') {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

/** The person member.a acts for, read from the account's PERSON scope. */
function boundPersonId(): string {
  const account = api.world.accounts.find((a) => a.username === 'member.a');
  expect(account, 'member.a is not seeded').toBeDefined();
  const scope = account!.memberships[0]!.scopes?.find((s) => s.type === 'PERSON');
  expect(scope?.id, 'member.a is bound to nobody').toBeTruthy();
  return scope!.id!;
}

function ownBooking(status: string) {
  const personId = boundPersonId();
  const found = api.world.bookings.find((b) => b.personId === personId && b.status === status);
  expect(found, `member.a has no ${status} booking`).toBeDefined();
  return found!;
}

describe('home', () => {
  it('leads with the remaining nights and the next stay, with the amount the member pays', async () => {
    mount('/');
    await login();
    const list = await screen.findByTestId('remaining-list');
    const rows = within(list).getAllByTestId('remaining-row');
    // Nights first, and the figure is the server's with its unit word.
    expect(rows[0]).toHaveTextContent(/gece/);
    const next = ownBooking('CONFIRMED');
    const card = await screen.findByTestId('next-booking');
    expect(card).toHaveTextContent('Onaylandı');
    expect(card).toHaveTextContent(formatMoney(next.quoteSnapshot.memberAmount, 'TRY'));
    expect(screen.getByTestId('home-search')).toHaveTextContent('Konaklama ara');
  });
});

describe('search, hold, confirm', () => {
  it('shows what the member pays before the hold, then on the hold, then on the button', async () => {
    const { services } = mount('/search');
    const user = await login();
    const property = api.world.properties[0]!;
    const room = api.world.roomTypes.find((r) => r.propertyId === property.id)!;
    const days = api.world.inventoryDays
      .filter((d) => d.roomTypeId === room.id)
      .map((d) => d.stayDate)
      .sort();
    const checkIn = days[30]!;
    const checkOut = days[33]!;

    await user.type(await screen.findByLabelText(/^Giriş/), checkIn);
    await user.type(screen.getByLabelText(/^Çıkış/), checkOut);
    await user.selectOptions(screen.getByLabelText(/^Nerede/), `property:${property.id}`);
    await user.click(screen.getByRole('button', { name: 'Ara' }));

    const rows = await screen.findAllByTestId('room-row');
    expect(rows.length).toBeGreaterThan(0);
    // The plan has two nights left and the stay is three: the row says so on its own line.
    expect(screen.getByTestId('entitlement-line')).toHaveTextContent(/Kalan hakkınız/);
    const firstWithQuote = rows.find((r) => within(r).queryByTestId('room-member-amount'))!;
    const shown = within(firstWithQuote).getByTestId('room-member-amount').textContent;

    // The same figure the server answers directly: no client arithmetic in between.
    const tenantId = services.store.getState().activeTenant!.tenant.id;
    const answer = await services.ops.lodging.search(tenantId, {
      checkIn,
      checkOut,
      adults: 2,
      children: 0,
      propertyId: property.id,
    });
    const quoted = answer.results.find((r) => r.quote)!.quote!;
    expect(shown).toBe(formatMoney(quoted.memberAmount, quoted.currencyCode));
    expect(answer.eligible).toBe(false);

    await user.click(within(firstWithQuote).getByRole('button', { name: 'Seç' }));
    const receipt = screen.getByTestId('receipt');
    expect(within(receipt).getByTestId('receipt-member-amount')).toHaveTextContent(shown!);

    await user.click(within(receipt).getByRole('button', { name: 'Odayı tut' }));
    // The hold: the countdown reads the server's deadline, and Onayla carries the amount.
    await screen.findByTestId('hold-countdown');
    expect(screen.getByTestId('booking-status')).toHaveTextContent('Tutuldu');
    const confirm = screen.getByTestId('confirm-button');
    expect(confirm).toHaveTextContent(`Ödeyeceğiniz ${shown}`);
    expect(screen.getByTestId('receipt-member-amount')).toHaveTextContent(shown!);
    expect(screen.getByTestId('receipt')).toHaveTextContent(
      /Planınız 2 geceyi karşılıyor, 1 gece sizin/,
    );

    await user.click(confirm);
    await waitFor(() =>
      expect(screen.getByTestId('booking-status')).toHaveTextContent('Onaylandı'),
    );
  });

  it('shows the way back when the hold expired while the screen slept', async () => {
    const hold = ownBooking('HOLD');
    hold.holdExpiresAt = new Date(Date.now() - 1_000).toISOString();
    mount(`/bookings/${hold.id}`);
    await login();
    await screen.findByText('Süresi doldu', { selector: 'p' });
    expect(screen.getByRole('link', { name: 'Aramaya dön' })).toBeInTheDocument();
    expect(screen.queryByTestId('confirm-button')).toBeNull();
  });
});

describe('the confirmed booking', () => {
  it('shows the voucher once, on request, and keeps it in no storage', async () => {
    const booking = ownBooking('CONFIRMED');
    mount(`/bookings/${booking.id}`);
    const user = await login();
    expect(screen.queryByTestId('voucher-token')).toBeNull();
    await user.click(await screen.findByRole('button', { name: 'Kuponu göster' }));
    const token = (await screen.findByTestId('voucher-token')).textContent!;
    expect(token.length).toBeGreaterThan(4);
    expect(window.localStorage.length).toBe(0);
    expect(window.sessionStorage.length).toBe(0);
    await user.click(screen.getByRole('button', { name: 'Kodu gizle' }));
    expect(screen.queryByTestId('voucher-token')).toBeNull();
  });

  it('answers what cancelling costs, from the server, before offering to cancel', async () => {
    const booking = ownBooking('CONFIRMED');
    const { services } = mount(`/bookings/${booking.id}`);
    const user = await login();
    expect(screen.queryByRole('button', { name: 'İptali onayla' })).toBeNull();
    await user.click(await screen.findByRole('button', { name: 'İptal edersem ne öderim?' }));
    const preview = await screen.findByTestId('cancellation-preview');
    const tenantId = services.store.getState().activeTenant!.tenant.id;
    const quote = (await services.ops.lodging.previewCancellation(tenantId, booking.id)).quote;
    if (quote.free) {
      expect(preview).toHaveTextContent(/ücretsiz/);
    } else {
      expect(preview).toHaveTextContent(formatMoney(quote.memberFee, quote.currencyCode));
    }
    expect(within(preview).getByRole('button', { name: 'İptali onayla' })).toBeInTheDocument();
    await user.click(within(preview).getByRole('button', { name: 'İptali onayla' }));
    await waitFor(() =>
      expect(screen.getByTestId('booking-status')).toHaveTextContent('İptal edildi'),
    );
  });
});
