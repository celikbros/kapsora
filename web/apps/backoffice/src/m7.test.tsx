import { createMockServer } from '@kapsora/api-client/mocks/node';
import { formatMoney, initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
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
