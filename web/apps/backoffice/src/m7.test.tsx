import { createMockServer } from '@kapsora/api-client/mocks/node';
import { formatMoney, initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { App } from './App';
import { createServices } from './api';

/**
 * The payer's billing screens against the mock world: the icmal decided invoice by invoice
 * with the server's totals line, the settlement approved and paid with the figures the
 * server keeps, the member's reimbursement decided in part and paid.
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

describe('the icmal review', () => {
  it('lists what providers sent, without their drafts', async () => {
    mount('/billing/batches');
    await login('financial.reviewer');
    const table = await screen.findByTestId('batch-table');
    const rows = within(table).getAllByTestId('batch-row');
    expect(rows.length).toBe(api.world.batches.filter((b) => b.status !== 'DRAFT').length);
    expect(table).not.toHaveTextContent('Taslak');
  });

  it('takes a cut on the pending invoice and closes the icmal with the totals the server computed', async () => {
    const batch = api.world.batches.find((b) => b.status === 'UNDER_REVIEW')!;
    const pending = api.world.batchInvoices.find((m) => m.batchId === batch.id && !m.decision)!;
    mount(`/billing/batches/${batch.id}`);
    const user = await login('financial.reviewer');
    expect(await screen.findByTestId('pending-count')).toHaveTextContent('1 fatura karar bekliyor');
    expect(screen.queryByTestId('decide-batch')).not.toBeInTheDocument();

    const rows = within(screen.getByTestId('review-table')).getAllByTestId('review-row');
    const row = rows.find((r) => within(r).queryByText('Karar bekliyor'))!;
    await user.click(within(row).getByRole('button', { name: 'Fatura kararı' }));
    const form = await screen.findByTestId('decision-form');
    await user.selectOptions(within(form).getByLabelText(/^Fatura kararı/), 'CUT');
    await user.type(within(form).getByLabelText(/^Onaylanan tutar/), '500');
    await user.selectOptions(within(form).getByLabelText(/^Gerekçe/), 'TARIFF_EXCEEDED');
    await user.click(screen.getByTestId('save-decision'));
    await waitFor(() =>
      expect(screen.getByTestId('pending-count')).toHaveTextContent('0 fatura karar bekliyor'),
    );
    expect(pending.decision).toBe('CUT');
    expect(pending.approvedAmount).toBe('500');

    await user.click(screen.getByTestId('decide-batch'));
    await waitFor(() =>
      expect(screen.getByTestId('batch-status')).toHaveTextContent('Kararlaştırıldı'),
    );
    const totals = await screen.findByTestId('batch-totals');
    expect(within(totals).getByTestId('total-cut')).toHaveTextContent(
      formatMoney(batch.cutTotal, 'TRY'),
    );
    expect(within(totals).getByTestId('total-approved')).toHaveTextContent(
      formatMoney(batch.approvedTotal, 'TRY'),
    );
    // The per-decision line counts the cut invoice once, at what was left of it.
    expect(within(totals).getByTestId('decision-CUT')).toHaveTextContent('1');
  });
});

describe('the settlement', () => {
  it('shows the payable figure the server computed, is approved, and records the payment', async () => {
    const pending = api.world.settlements.find((s) => s.status === 'PENDING_APPROVAL')!;
    mount(`/billing/settlements/${pending.id}`);
    const user = await login('payer.approver');
    expect(await screen.findByTestId('settlement-status')).toHaveTextContent('Onay bekliyor');
    expect(screen.getByTestId('payable')).toHaveTextContent(
      formatMoney(pending.payableAmount, 'TRY'),
    );
    expect(screen.getByTestId('recovery-list')).toBeInTheDocument();

    await user.click(screen.getByTestId('approve-settlement'));
    await waitFor(() =>
      expect(screen.getByTestId('settlement-status')).toHaveTextContent('Onaylandı'),
    );

    const form = await screen.findByTestId('payment-form');
    await user.type(within(form).getByLabelText(/^Dış referans/), 'EFT-2026-09-0001');
    await user.type(within(form).getByLabelText(/^Tutar/), pending.payableAmount);
    fireEvent.change(within(form).getByLabelText(/^Ödeme tarihi/), {
      target: { value: '2026-09-10' },
    });
    await user.click(screen.getByTestId('record-payment'));
    await waitFor(() =>
      expect(screen.getByTestId('settlement-status')).toHaveTextContent('Ödendi'),
    );
    expect(screen.getByTestId('paid')).toHaveTextContent(formatMoney(pending.paidAmount, 'TRY'));
    expect(screen.queryByTestId('payment-form')).not.toBeInTheDocument();
  });
});

describe('the reimbursement', () => {
  it('shows four characters of the account, approves in part with a reason, and is paid', async () => {
    const submitted = api.world.reimbursements.find((r) => r.status === 'SUBMITTED')!;
    mount(`/billing/reimbursements/${submitted.id}`);
    const user = await login('financial.reviewer');
    expect(await screen.findByTestId('reimbursement-status')).toHaveTextContent('Gönderildi');
    expect(screen.getByTestId('account-masked')).toHaveTextContent(
      `Hesap •••• ${submitted.bankAccountMasked}`,
    );
    expect(screen.getByTestId('account-masked').textContent).not.toMatch(/TR\d{2}/);

    const form = screen.getByTestId('reimbursement-decision');
    await user.selectOptions(within(form).getByLabelText(/^Karar/), 'PARTIAL');
    await user.type(within(form).getByLabelText(/^Onaylanan tutar/), '100');
    await user.type(within(form).getByLabelText(/^Gerekçe/), 'CONTRACT_LIMIT');
    await user.click(screen.getByTestId('decide-reimbursement'));
    await waitFor(() =>
      // A partial approval orders the payment at once: the status the server answers.
      expect(screen.getByTestId('reimbursement-status')).toHaveTextContent('Ödeme emri verildi'),
    );
    expect(screen.getByTestId('approved')).toHaveTextContent(formatMoney('100', 'TRY'));

    await user.type(screen.getByLabelText(/^Ödeme referansı/), 'EFT-2026-09-0002');
    await user.click(screen.getByTestId('pay-reimbursement'));
    await waitFor(() =>
      expect(screen.getByTestId('reimbursement-status')).toHaveTextContent('Ödendi'),
    );
  });
});

describe('the operations dashboard', () => {
  it('sits on the home page, values the claims, and links only where the list reproduces the figure', async () => {
    const { history } = mount('/');
    const user = await login('financial.reviewer');
    const dashboard = await screen.findByTestId('dashboard');
    const batches = within(dashboard).getByTestId('dashboard-batches');
    const waiting = api.world.batches.filter(
      (b) => b.status === 'SUBMITTED' || b.status === 'UNDER_REVIEW',
    ).length;
    expect(batches).toHaveTextContent(String(waiting));
    // Two statuses make the figure; the list takes one, so the label is not a link.
    expect(within(batches).queryByRole('link')).toBeNull();
    // Approved claims are worth what the server says, not nothing.
    const approved = within(dashboard).getByTestId('claims-APPROVED');
    // The amount cell itself, not a substring: '1.500,00 TRY' contains '0,00 TRY'.
    expect(approved.lastElementChild!.textContent).not.toBe(formatMoney('0', 'TRY'));
    await user.click(within(approved).getByRole('link'));
    await waitFor(() => expect(history.location.pathname).toBe('/claims'));
    expect(history.location.search).toContain('APPROVED');
  });
});

describe('the reconciliation', () => {
  it('lists the runs and opens the differing day with each difference by reference', async () => {
    mount('/billing/reconciliation');
    const user = await login('financial.reviewer');
    const table = await screen.findByTestId('run-table');
    expect(within(table).getAllByTestId('run-row').length).toBe(
      api.world.reconciliationRuns.length,
    );
    const differing = api.world.reconciliationRuns.find(
      (r) => r.scope === 'TENANT' && r.differenceCount > 0,
    )!;
    const row = within(table)
      .getAllByTestId('run-row')
      .find((r) => r.textContent?.includes('Fark var') && r.textContent.includes('Kurum'))!;
    await user.click(within(row).getByRole('link'));
    expect(await screen.findByTestId('run-status')).toHaveTextContent('Fark var');
    expect(screen.getByTestId('run-open')).toHaveTextContent(
      formatMoney(differing.openTotal, differing.currencyCode),
    );
    const differences = within(screen.getByTestId('difference-table')).getAllByTestId(
      'difference-row',
    );
    expect(differences.length).toBe(differing.differenceCount);
    expect(differences[0]).toHaveTextContent(differing.differences[0]!.reference);
    expect(differences[0]!.textContent).not.toMatch(/[0-9a-f]{8}-[0-9a-f]{4}/);
  });
});

describe('the exports', () => {
  it('queues a file, shows it ready, and opens it through an audited download with the watermark', async () => {
    const opened = vi.fn();
    window.open = opened as unknown as typeof window.open;
    mount('/billing/exports');
    const user = await login('financial.reviewer');
    const form = await screen.findByTestId('export-form');
    await user.selectOptions(within(form).getByLabelText(/^Rapor/), 'SETTLEMENTS');
    await user.click(screen.getByTestId('request-export'));
    const table = await screen.findByTestId('export-table');
    await waitFor(
      () => {
        const rows = within(table).getAllByTestId('export-row');
        expect(rows.some((r) => within(r).queryByTestId('download-export'))).toBe(true);
      },
      { timeout: 10_000 },
    );
    const ready = within(table)
      .getAllByTestId('export-row')
      .find((r) => within(r).queryByTestId('download-export'))!;
    await user.click(within(ready).getByTestId('download-export'));
    const purpose = await screen.findByTestId('download-purpose');
    expect(opened).not.toHaveBeenCalled();
    // Nothing is chosen for the reader: confirming without a purpose is refused in place.
    await user.click(screen.getByTestId('download-confirm'));
    expect(await within(purpose).findByText('İndirmenin amacını seçin.')).toBeInTheDocument();
    expect(opened).not.toHaveBeenCalled();
    await user.selectOptions(within(purpose).getByLabelText(/^İndirme amacı/), 'AUDIT');
    await user.click(screen.getByTestId('download-confirm'));
    await waitFor(() => expect(opened).toHaveBeenCalledTimes(1));
    const watermark = await within(ready).findByTestId('export-watermark');
    expect(watermark.textContent).toContain('Fuat Mali Değerlendirici');
  });
});

describe('the settlement above the checker threshold', () => {
  it('asks for the password and then names the second-person rule when the decider approves', async () => {
    const pending = api.world.settlements.find((x) => x.status === 'PENDING_APPROVAL')!;
    // Above the tenant's threshold, decided by the approver themselves: the rule has somebody
    // to refuse. The fixture's figures are small on purpose, so the case is made here.
    pending.approvedAmount = '60000';
    pending.payableAmount = '60000';
    pending.withheldAmount = '0';
    const approver = api.world.accounts.find((a) => a.username === 'payer.approver')!;
    api.world.batches.find((b) => b.id === pending.batchId)!.decidedBy = approver.actorId;

    mount(`/billing/settlements/${pending.id}`);
    const user = await login('payer.approver');
    await user.click(await screen.findByTestId('approve-settlement'));
    const dialog = await screen.findByRole('dialog');
    await user.type(within(dialog).getByLabelText(/^Parola/), PASSWORD);
    await user.keyboard('{Enter}');
    expect(await screen.findByText(/İcmali karara bağlayan kişi/)).toBeInTheDocument();
    expect(screen.getByTestId('settlement-status')).toHaveTextContent('Onay bekliyor');
    expect(pending.status).toBe('PENDING_APPROVAL');
  });
});

describe('a payment beyond the remainder', () => {
  it('is refused by the server and the figures stay as they were', async () => {
    const approved = api.world.settlements.find((x) => x.status === 'APPROVED')!;
    const paidBefore = approved.paidAmount;
    mount(`/billing/settlements/${approved.id}`);
    const user = await login('financial.reviewer');
    const form = await screen.findByTestId('payment-form');
    await user.type(within(form).getByLabelText(/^Dış referans/), 'EFT-2026-09-0999');
    await user.type(within(form).getByLabelText(/^Tutar/), '999999');
    fireEvent.change(within(form).getByLabelText(/^Ödeme tarihi/), {
      target: { value: '2026-09-10' },
    });
    await user.click(screen.getByTestId('record-payment'));
    expect(await within(form).findByRole('alert')).toBeInTheDocument();
    expect(approved.paidAmount).toBe(paidBefore);
    expect(screen.getByTestId('settlement-status')).toHaveTextContent('Onaylandı');
    expect(screen.getByTestId('paid')).toHaveTextContent(formatMoney(paidBefore, 'TRY'));
  });
});
