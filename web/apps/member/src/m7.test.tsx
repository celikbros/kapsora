import { createMockServer } from '@kapsora/api-client/mocks/node';
import { formatMoney, initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { App } from './App';
import { createServices } from './services';

/**
 * The member's reimbursement against the mock world, as member.a: the list of what they
 * asked for, and the three steps that make a new one — the request, the receipt that is
 * scanned before anything else happens, the account typed once and shown as four
 * characters — then the submit the server accepts only on a clean receipt.
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

function boundPersonId(): string {
  const account = api.world.accounts.find((a) => a.username === 'member.a')!;
  return account.memberships[0]!.scopes!.find((s) => s.type === 'PERSON')!.id!;
}

describe('geri ödemelerim', () => {
  it('is reached from home and lists every request of the person, newest first', async () => {
    mount('/');
    const user = await login();
    await user.click(await screen.findByTestId('home-reimbursements'));
    const list = await screen.findByTestId('reimbursement-list');
    const own = api.world.reimbursements.filter((r) => r.personId === boundPersonId());
    const rows = within(list).getAllByTestId('reimbursement-row');
    expect(rows.length).toBe(own.length);
    const newest = [...own].sort((a, b) => b.createdAt.localeCompare(a.createdAt))[0]!;
    expect(rows[0]).toHaveTextContent(newest.reference);
    // The account never appears on the list, and nothing on the page looks like one.
    expect(list.textContent).not.toMatch(/TR\d{2}/);
  });

  it('shows a decided request with what was agreed and four characters of the account', async () => {
    const paid = api.world.reimbursements.find(
      (r) => r.personId === boundPersonId() && r.status === 'PAID',
    )!;
    mount(`/reimbursements/${paid.id}`);
    await login();
    expect(await screen.findByTestId('reimbursement-status')).toHaveTextContent('Ödendi');
    const receipt = screen.getByTestId('receipt');
    expect(receipt).toHaveTextContent(formatMoney(paid.approvedAmount!, 'TRY'));
    expect(receipt).toHaveTextContent(`Hesap •••• ${paid.bankAccountMasked}`);
    expect(receipt.textContent).not.toMatch(/TR\d{2}/);
    expect(screen.getByTestId('reimbursement-sequence')).toHaveTextContent(paid.paymentReference!);
  });
});

describe('a new reimbursement', () => {
  it(
    'opens the request, waits for the receipt to scan clean, takes the account once, and submits',
    { timeout: 40_000 },
    async () => {
      mount('/reimbursements/new');
      const user = await login();
      const definition = api.world.serviceDefinitions.find((d) => d.active)!;
      // The provider the seeded reimbursements name, so the same organization is on both lists.
      const provider = api.world.providers.find(
        (p) =>
          p.status === 'ACTIVE' &&
          p.tenantOrganizationId === api.world.reimbursements[0]!.providerOrganizationId,
      )!;

      // 1. The request.
      await user.selectOptions(await screen.findByLabelText(/^Hizmet\*/), definition.id);
      fireEvent.change(screen.getByLabelText(/^Hizmet tarihi/), {
        target: { value: new Date().toISOString().slice(0, 10) },
      });
      const picker = screen.getByLabelText(/^Sağlayıcı/);
      await waitFor(() => expect(within(picker).getAllByRole('option').length).toBeGreaterThan(1));
      await user.selectOptions(picker, provider.tenantOrganizationId);
      await user.type(screen.getByLabelText(/^Ödediğiniz tutar/), '125.50');
      expect(screen.getByTestId('receipt')).toHaveTextContent(formatMoney('125.50', 'TRY'));
      await user.click(screen.getByTestId('create-request'));

      // 2. The receipt: uploaded, then scanning until the world says otherwise.
      const file = new File(['%PDF-1.4 fis'], 'fis.pdf', { type: 'application/pdf' });
      await user.upload(await screen.findByTestId('receipt-file'), file);
      const scan = await screen.findByTestId('receipt-scan');
      await waitFor(() => expect(scan).toHaveTextContent('Makbuz taranıyor'), { timeout: 10_000 });
      expect(screen.queryByTestId('account-form')).not.toBeInTheDocument();
      const uploaded = api.world.documents.find((d) => d.originalFilename === 'fis.pdf')!;
      expect(api.world.advanceScan(uploaded.id, 'CLEAN')).not.toBeNull();
      await waitFor(
        () => expect(screen.getByTestId('receipt-scan')).toHaveTextContent('Makbuz hazır'),
        { timeout: 15_000 },
      );

      // 3. The account, typed once.
      const form = await screen.findByTestId('account-form');
      await user.type(within(form).getByLabelText(/^IBAN/), 'TR33 0006 1005 1978 6457 8413 26');
      await user.click(screen.getByTestId('create-reimbursement'));

      // The draft, as a receipt: four characters of the account and the submit.
      expect(await screen.findByTestId('reimbursement-status')).toHaveTextContent('Taslak');
      const receipt = screen.getByTestId('receipt');
      expect(receipt).toHaveTextContent('Hesap •••• 1326');
      expect(receipt.textContent).not.toContain('TR33');
      expect(receipt.textContent).not.toContain('8413');
      // Typed once and kept nowhere on the device.
      const stored =
        JSON.stringify({ ...window.localStorage }) + JSON.stringify({ ...window.sessionStorage });
      expect(stored).not.toContain('8413');
      expect(stored).not.toContain('TR33');
      await user.click(screen.getByTestId('submit-reimbursement'));
      await waitFor(() =>
        expect(screen.getByTestId('reimbursement-status')).toHaveTextContent('Gönderildi'),
      );
      const created = api.world.reimbursements.find((r) => r.receiptDocumentId === uploaded.id)!;
      expect(created.status).toBe('SUBMITTED');
      expect(created.requestedAmount).toBe('125.5');
      expect(JSON.stringify(created)).not.toContain('TR33');
    },
  );
});
