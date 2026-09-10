import { createMockServer } from '@kapsora/api-client/mocks/node';
import { formatMoney, initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { App } from './App';
import { createServices } from './services';

/**
 * The provider's billing desk against the mock world, as billing.a: the earnings the
 * invoice is checked against, the invoice with the server's allocation difference beside its
 * total and the refusal when they differ, the returned invoice's correction, and the icmal
 * built from submitted invoices and sent.
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

async function login(username = 'billing.a') {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

function providerId(): string {
  const account = api.world.accounts.find((a) => a.username === 'billing.a')!;
  return account.memberships[0]!.scopes!.find((s) => s.type === 'ORGANIZATION')!.id!;
}

describe('earnings', () => {
  it('shows the invoiceable figure the server computed and opens a new invoice on it', async () => {
    const { services } = mount('/billing');
    await login();
    fireEvent.change(await screen.findByLabelText(/^Başlangıç/), {
      target: { value: '2026-01-01' },
    });
    const card = await screen.findByTestId('earnings-currency');
    const tenantId = services.store.getState().activeTenant!.tenant.id;
    const earnings = await services.ops.billing.earnings(tenantId, providerId(), {
      from: '2026-01-01',
      to: new Date().toISOString().slice(0, 10),
    });
    const currency = earnings.currencies.find((c) => c.currencyCode === 'TRY')!;
    expect(within(card).getByTestId('invoiceable-total')).toHaveTextContent(
      formatMoney(currency.invoiceableTotal, 'TRY'),
    );
    expect(within(card).getByRole('button', { name: 'Yeni fatura' })).toBeInTheDocument();
  });
});

describe('the invoice', () => {
  it('shows the allocation difference the server computed and is refused while it is not zero', async () => {
    const mismatch = api.world.invoices.find(
      (i) => i.status === 'DRAFT' && i.supersedesInvoiceId === null,
    )!;
    mount(`/billing/invoices/${mismatch.id}`);
    const user = await login();
    expect(await screen.findByTestId('invoice-status')).toHaveTextContent('Taslak');
    const totals = await screen.findByTestId('allocation-totals');
    expect(within(totals).getByTestId('allocation-difference').textContent).not.toBe(
      formatMoney('0', 'TRY'),
    );
    await user.click(screen.getByTestId('invoice-submit'));
    expect(await screen.findByRole('alert')).toBeInTheDocument();
    expect(screen.getByTestId('invoice-status')).toHaveTextContent('Taslak');
  });

  it('offers a correction on a returned invoice and shows the chain', async () => {
    const returned = api.world.invoices.find((i) => i.status === 'RETURNED')!;
    mount(`/billing/invoices/${returned.id}`);
    await login();
    expect(await screen.findByTestId('invoice-status')).toHaveTextContent('İade edildi');
    expect(screen.getByTestId('invoice-correct')).toBeInTheDocument();
    const chain = await screen.findByTestId('invoice-chain');
    expect(within(chain).getAllByRole('listitem').length).toBeGreaterThan(1);
  });
});

describe('the icmal', () => {
  it('lists the submitted invoices in the draft and sends it', async () => {
    const draft = api.world.batches.find((b) => b.status === 'DRAFT')!;
    mount(`/billing/batches/${draft.id}`);
    const user = await login();
    expect(await screen.findByTestId('batch-status')).toHaveTextContent('Taslak');
    const list = await screen.findByTestId('membership-list');
    const members = api.world.batchInvoices.filter((m) => m.batchId === draft.id);
    expect(within(list).getAllByRole('checkbox', { checked: true }).length).toBe(members.length);
    await user.click(screen.getByTestId('batch-submit'));
    await waitFor(() => expect(screen.getByTestId('batch-status')).toHaveTextContent('Gönderildi'));
    const rows = within(await screen.findByTestId('decision-table')).getAllByTestId('decision-row');
    expect(rows.length).toBe(members.length);
    expect(rows[0]).toHaveTextContent('Karar bekliyor');
  });
});
