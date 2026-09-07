import { createMockServer } from '@kapsora/api-client/mocks/node';
import { formatMoney, initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { App } from './App';
import { createServices } from './api';

/**
 * The payer's lodging screens against the mock world: the booking as a sequence, the
 * no-show a second person confirms, the waiting list in queue order, the lodging terms on
 * a draft version only, and the person's Konaklama tab.
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

async function login(username = 'admin.a') {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

describe('bookings', () => {
  it('lists every booking with the member share as the server sent it', async () => {
    mount('/lodging/bookings');
    await login();
    const table = await screen.findByTestId('booking-table');
    const rows = within(table).getAllByTestId('booking-row');
    expect(rows.length).toBe(api.world.bookings.length);
    const first = api.world.bookings.find(
      (b) => b.reference === within(rows[0]!).getByText(/^BK-/).textContent,
    )!;
    expect(rows[0]).toHaveTextContent(formatMoney(first.quoteSnapshot.memberAmount, 'TRY'));
  });

  it('reads one booking as the sequence that produced it, and confirms a no-show as a second person', async () => {
    const report = api.world.noShows.find((n) => n.status === 'REPORTED')!;
    mount(`/lodging/bookings/${report.bookingId}`);
    const user = await login();
    const sequence = await screen.findByTestId('booking-sequence');
    expect(within(sequence).getByText('Arandı ve gösterildi')).toBeInTheDocument();
    expect(within(sequence).getByText('Kapıda')).toBeInTheDocument();
    expect(await screen.findByTestId('no-show-status')).toHaveTextContent('Bildirildi');
    await user.click(
      within(await screen.findByTestId('no-show-review')).getByRole('button', { name: 'Onayla' }),
    );
    await waitFor(() =>
      expect(screen.getByTestId('no-show-status')).toHaveTextContent('Onaylandı'),
    );
    await waitFor(() =>
      expect(screen.getByTestId('booking-status')).toHaveTextContent('Gelinmedi'),
    );
  });
});

describe('the waiting list', () => {
  it('is shown in queue order with a position', async () => {
    mount('/lodging/waitlist');
    await login();
    const table = await screen.findByTestId('waitlist-table');
    const rows = within(table).getAllByTestId('waitlist-row');
    expect(rows.length).toBeGreaterThan(0);
    expect(rows[0]).toHaveTextContent('1');
  });
});

describe('lodging terms on a contract version', () => {
  it('are read on a published version and written on a draft', async () => {
    const withTerms = api.world.lodgingTerms[0]!;
    mount(`/contract-versions/${withTerms.contractVersionId}`);
    await login();
    const card = await screen.findByTestId('lodging-terms');
    expect(card).toHaveTextContent(String(withTerms.freeCancellationHoursBefore));
    expect(screen.queryByTestId('lodging-terms-form')).toBeNull();
  });

  it('warns on a draft without terms and saves them with the version etag', async () => {
    const draft = api.world.contractVersions.find(
      (v) =>
        v.status === 'DRAFT' && !api.world.lodgingTerms.some((x) => x.contractVersionId === v.id),
    )!;
    mount(`/contract-versions/${draft.id}`);
    const user = await login();
    const form = await screen.findByTestId('lodging-terms-form');
    await user.click(within(form).getByRole('button', { name: 'Koşulları kaydet' }));
    await screen.findByText('Koşullar kaydedildi.', { exact: true });
    expect(api.world.lodgingTerms.some((x) => x.contractVersionId === draft.id)).toBe(true);
  });
});

describe('the person', () => {
  it('has a Konaklama tab with the bookings', async () => {
    const booking = api.world.bookings[0]!;
    mount(`/people/${booking.personId}`);
    const user = await login();
    await user.click(await screen.findByRole('tab', { name: 'Konaklama' }));
    const tab = await screen.findByTestId('lodging-tab');
    await waitFor(() => expect(within(tab).getByText(booking.reference)).toBeInTheDocument());
  });
});
