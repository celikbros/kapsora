import { createMockServer } from '@kapsora/api-client/mocks/node';
import { formatMoney, initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
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
afterEach(() => {
  vi.restoreAllMocks();
  api.reset();
});
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
  it.each(['HEALTH', 'ACCOMMODATION'])(
    'requires and stores the chosen %s domain on a new invoice',
    async (domainCode) => {
      mount('/billing/invoices/new?currency=TRY');
      const user = await login();
      const header = await screen.findByTestId('invoice-header');
      const field = (name: string) => header.querySelector(`[name="${name}"]`)!;
      fireEvent.change(field('invoiceNumber'), { target: { value: `PC05-${domainCode}` } });
      fireEvent.change(field('lineExtensionAmount'), { target: { value: '100' } });
      fireEvent.change(field('taxAmount'), { target: { value: '0' } });
      fireEvent.change(field('payableAmount'), { target: { value: '100' } });
      const save = screen.getByRole('button', { name: 'Kaydet' });
      expect(save).toBeDisabled();
      const before = api.world.invoices.length;
      fireEvent.submit(header);
      expect(api.world.invoices).toHaveLength(before);
      await user.selectOptions(field('domainCode'), domainCode);
      await user.click(save);
      await waitFor(() => expect(api.world.invoices).toHaveLength(before + 1));
      expect(
        api.world.invoices.find((i) => i.invoiceNumber === `PC05-${domainCode}`)?.domainCode,
      ).toBe(domainCode);
      expect(await screen.findByTestId('invoice-status')).toHaveTextContent('Taslak');
    },
  );

  it('lets the provider repair an existing generic draft domain', async () => {
    const draft = api.world.invoices.find(
      (i) => i.status === 'DRAFT' && i.supersedesInvoiceId === null,
    )!;
    draft.domainCode = 'GENERIC';
    mount(`/billing/invoices/${draft.id}`);
    const user = await login();
    const header = await screen.findByTestId('invoice-header');
    const domain = header.querySelector('[name="domainCode"]')!;
    expect(domain).toHaveValue('GENERIC');
    await user.selectOptions(domain, 'HEALTH');
    await user.click(screen.getByRole('button', { name: 'Ba\u015fl\u0131\u011f\u0131 kaydet' }));
    await waitFor(() =>
      expect(api.world.invoices.find((i) => i.id === draft.id)?.domainCode).toBe('HEALTH'),
    );
  });

  it('preserves the accommodation domain when correcting a returned invoice', async () => {
    const returned = api.world.invoices.find(
      (i) => i.status === 'RETURNED' && !i.supersededByInvoiceId,
    )!;
    returned.domainCode = 'ACCOMMODATION';
    for (const invoice of api.world.invoices) {
      if (invoice.supersedesInvoiceId === returned.id) invoice.status = 'CANCELLED';
    }
    const beforeIds = new Set(api.world.invoices.map((i) => i.id));
    mount(`/billing/invoices/${returned.id}`);
    const user = await login();
    const header = await screen.findByTestId('invoice-header');
    expect(header.querySelector('[name="domainCode"]')).toBeDisabled();
    await user.click(screen.getByTestId('invoice-correct'));
    await waitFor(() =>
      expect(
        api.world.invoices.find(
          (i) => i.supersedesInvoiceId === returned.id && !beforeIds.has(i.id),
        ),
      ).toBeDefined(),
    );
    expect(
      api.world.invoices.find((i) => i.supersedesInvoiceId === returned.id && !beforeIds.has(i.id))
        ?.domainCode,
    ).toBe('ACCOMMODATION');
  });

  it('hydrates invoice candidates in small batches and keeps earlier rows', async () => {
    const draft = api.world.invoices.find((invoice) => invoice.status === 'DRAFT')!;
    const candidates = api.world.claims.filter(
      (claim) =>
        (claim.status === 'APPROVED' || claim.status === 'PARTIALLY_APPROVED') &&
        !api.world.invoiceAllocations.some(
          (allocation) => allocation.invoiceId === draft.id && allocation.claimId === claim.id,
        ),
    );
    expect(candidates.length).toBeGreaterThanOrEqual(4);
    const firstFour = candidates.slice(0, 4);
    const { services } = mount(
      `/billing/invoices/${draft.id}?claims=${firstFour.map((claim) => claim.id).join(',')}`,
    );
    const getClaim = vi.spyOn(services.ops.claims, 'get');
    const getReadiness = vi.spyOn(services.ops.claims, 'readiness');
    const user = await login();
    await screen.findByText(firstFour[1]!.reference);
    await waitFor(() => {
      expect(getClaim).toHaveBeenCalledTimes(2);
      expect(getReadiness).toHaveBeenCalledTimes(2);
    });
    expect(getClaim.mock.calls.map((call) => call[1])).toEqual(
      firstFour.slice(0, 2).map((claim) => claim.id),
    );
    expect(getReadiness.mock.calls.map((call) => call[1])).toEqual(
      firstFour.slice(0, 2).map((claim) => claim.id),
    );
    await user.click(screen.getByTestId('allocation-show-more'));
    await screen.findByText(firstFour[3]!.reference);
    expect(screen.getByText(firstFour[0]!.reference)).toBeInTheDocument();
    expect(getClaim.mock.calls.map((call) => call[1])).toContain(firstFour[3]!.id);
    expect(getClaim).toHaveBeenCalledTimes(getReadiness.mock.calls.length);
    expect(getClaim.mock.calls.length).toBeLessThanOrEqual(5);
    const hydratedBeforeSave = getClaim.mock.calls.length;
    await user.click(screen.getByRole('button', { name: 'Dağıtımı kaydet' }));
    await waitFor(() => expect(getClaim.mock.calls.length).toBeGreaterThan(hydratedBeforeSave));
  });

  it('shows every persisted allocation on a read-only invoice without claim hydration', async () => {
    const submitted = api.world.invoices.find((invoice) => invoice.status === 'SUBMITTED')!;
    const existing = api.world.invoiceAllocations.filter((row) => row.invoiceId === submitted.id);
    expect(existing).toHaveLength(2);
    const existingIds = new Set(existing.map((row) => row.claimId));
    const extraClaims = api.world.claims
      .filter(
        (claim) =>
          claim.providerOrganizationId === submitted.providerOrganizationId &&
          !existingIds.has(claim.id),
      )
      .slice(0, 2);
    expect(extraClaims).toHaveLength(2);
    for (const [index, claim] of extraClaims.entries()) {
      api.world.invoiceAllocations.push({
        ...existing[0]!,
        id: `00000000-0000-4000-8000-${String(index + 1).padStart(12, '0')}`,
        claimId: claim.id,
        allocatedAmount: '0',
      });
    }
    const { services } = mount(`/billing/invoices/${submitted.id}`);
    const getClaim = vi.spyOn(services.ops.claims, 'get');
    const getReadiness = vi.spyOn(services.ops.claims, 'readiness');
    await login();
    await waitFor(() => expect(screen.getAllByTestId('allocation-row')).toHaveLength(4));
    expect(getClaim).not.toHaveBeenCalled();
    expect(getReadiness).not.toHaveBeenCalled();
    expect(screen.queryByTestId('allocation-show-more')).not.toBeInTheDocument();
  });

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
    // The real API freezes invoiceCount at submit; draft membership lives in invoices.
    draft.invoiceCount = 0;
    mount(`/billing/batches/${draft.id}`);
    const user = await login();
    expect(await screen.findByTestId('batch-status')).toHaveTextContent('Taslak');
    const list = await screen.findByTestId('membership-list');
    const members = api.world.batchInvoices.filter((m) => m.batchId === draft.id);
    expect(within(list).getAllByRole('checkbox', { checked: true }).length).toBe(members.length);
    expect(screen.getByTestId('batch-submit')).toBeEnabled();
    await user.click(screen.getByTestId('batch-submit'));
    await waitFor(() => expect(screen.getByTestId('batch-status')).toHaveTextContent('Gönderildi'));
    const rows = within(await screen.findByTestId('decision-table')).getAllByTestId('decision-row');
    expect(rows.length).toBe(members.length);
    expect(rows[0]).toHaveTextContent('Karar bekliyor');
  });
});

describe('cari ekstre', () => {
  it('shows the period totals the server computed and the invoices behind them', async () => {
    const { services } = mount('/billing/statement');
    await login();
    fireEvent.change(await screen.findByLabelText(/^Dönem başı/), {
      target: { value: '2026-01-01' },
    });
    const totals = await screen.findByTestId('statement-totals');
    const tenantId = services.store.getState().activeTenant!.tenant.id;
    const statement = await services.ops.report.statement(tenantId, providerId(), {
      periodFrom: '2026-01-01',
      periodTo: new Date().toISOString().slice(0, 10),
    });
    expect(within(totals).getByTestId('total-invoiced')).toHaveTextContent(
      formatMoney(statement.totals.invoicedTotal, statement.currencyCode),
    );
    expect(within(totals).getByTestId('total-open')).toHaveTextContent(
      formatMoney(statement.totals.openBalance, statement.currencyCode),
    );
    const rows = within(screen.getByTestId('statement-invoices')).getAllByTestId(
      'statement-invoice',
    );
    expect(rows.length).toBe(statement.invoices.length);
  });
});

describe('the statement as a file', () => {
  it('queues the statement of the provider, shows it ready, and downloads it with the watermark', async () => {
    const opened = vi.fn();
    window.open = opened as unknown as typeof window.open;
    mount('/billing/statement');
    const user = await login();
    await user.click(await screen.findByTestId('statement-export-request'));
    await waitFor(() =>
      expect(screen.getByTestId('statement-export-status')).toHaveTextContent('Hazır'),
    );
    const created = api.world.exports.find(
      (x) =>
        x.kind === 'PROVIDER_STATEMENT' &&
        x.downloadCount === 0 &&
        x.providerOrganizationId === providerId(),
    )!;
    expect(created).toBeDefined();
    await user.click(screen.getByTestId('statement-export-download'));
    await waitFor(() => expect(opened).toHaveBeenCalledTimes(1));
    expect(await screen.findByTestId('statement-export-watermark')).toHaveTextContent(
      'Burak Faturalama',
    );
  });
});
