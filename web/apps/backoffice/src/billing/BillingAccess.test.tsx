// @vitest-environment jsdom
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { App } from '../App';
import { createServices } from '../api';
import { billingLanding, visibleNavEntries } from '../nav';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  cleanup();
  api.reset();
  vi.restoreAllMocks();
});
afterAll(() => server.close());

function mount(path: string) {
  const services = createServices({ baseUrl: BASE });
  render(<App services={services} history={createMemoryHistory({ initialEntries: [path] })} />);
  return services;
}

async function login(username: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

function entry(permissions: string[]) {
  return visibleNavEntries(permissions).find((item) => item.key === 'billing');
}

describe('Billing role access', () => {
  it('chooses a reachable landing from the active permission set', () => {
    expect(billingLanding(['invoice.read', 'settlement.read'])).toBe('/billing/batches');
    expect(entry(['invoice.read'])?.path).toBe('/billing/batches');
    expect(entry(['settlement.read'])?.path).toBe('/billing/settlements');
    expect(entry(['claim.financial.review'])?.path).toBe('/billing/reimbursements');
    expect(entry([])).toBeUndefined();
    expect(entry(['settlement.read'])?.match).toBe('/billing');
  });

  it.each([
    ['payer.approver', '/billing/batches', 'listBatches'],
    ['sponsor.hr', '/billing/settlements', 'listSettlements'],
    ['doctor.a', '/billing/reimbursements', 'listReimbursements'],
  ] as const)(
    'denies %s direct list visit without a read request',
    async (username, path, method) => {
      const services = mount(path);
      const read = vi.spyOn(services.ops.billing, method);
      await login(username);
      expect(await screen.findByText('Bu işlem için yetkiniz yok.')).toBeTruthy();
      expect(read).not.toHaveBeenCalled();
    },
  );

  it.each([
    ['payer.approver', 'batches', 'getBatch'],
    ['sponsor.hr', 'settlements', 'getSettlement'],
    ['doctor.a', 'reimbursements', 'getReimbursement'],
  ] as const)(
    'denies %s direct detail visit without a read request',
    async (username, kind, method) => {
      const id =
        kind === 'batches'
          ? api.world.batches[0]!.id
          : kind === 'settlements'
            ? api.world.settlements[0]!.id
            : api.world.reimbursements[0]!.id;
      const services = mount(`/billing/${kind}/${id}`);
      const read = vi.spyOn(services.ops.billing, method);
      await login(username);
      expect(await screen.findByText('Bu işlem için yetkiniz yok.')).toBeTruthy();
      expect(read).not.toHaveBeenCalled();
    },
  );

  it('updates the shared Billing target when the active grant set changes', async () => {
    const services = mount('/');
    await login('payer.approver');
    const menu = await screen.findByRole('navigation', { name: 'Ana menü' });
    const billing = within(menu).getByRole('link', { name: 'İcmal ve Ödeme' });
    expect(billing.getAttribute('href')).toBe('/billing/settlements');
    services.store.setState((state) => ({
      activeTenant: {
        ...state.activeTenant!,
        permissions: [...state.activeTenant!.permissions, 'invoice.read'],
      },
    }));
    await waitFor(() => expect(billing.getAttribute('href')).toBe('/billing/batches'));
  });

  it('lets HR read an invoice batch while withholding review controls', async () => {
    const pending = api.world.batches.find((batch) => batch.status === 'UNDER_REVIEW')!;
    mount(`/billing/batches/${pending.id}`);
    await login('sponsor.hr');
    expect(await screen.findByTestId('review-table')).toBeTruthy();
    expect(screen.queryByTestId('decision-form')).toBeNull();
    expect(screen.queryByTestId('decide-batch')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Fatura kararı' })).toBeNull();
  });

  it('shows a review-only reimbursement without unauthorized member or receipt links', async () => {
    const finance = api.world.accounts.find(
      (account) => account.username === 'financial.reviewer',
    )!;
    finance.memberships[0]!.permissions = ['claim.financial.review'];
    const row = api.world.reimbursements.find((item) => item.status === 'SUBMITTED')!;
    const services = mount(`/billing/reimbursements/${row.id}`);
    const people = vi.spyOn(services.ops.people, 'get');
    const documents = vi.spyOn(services.ops.documents, 'list');
    await login('financial.reviewer');
    const facts = await screen.findByTestId('reimbursement-facts');
    expect(facts).toHaveTextContent(row.personId);
    expect(within(facts).queryByRole('link')).toBeNull();
    expect(people).not.toHaveBeenCalled();
    expect(documents).not.toHaveBeenCalled();
  });

  it('closes an open invoice decision when batch.review is lost', async () => {
    const pending = api.world.batches.find((batch) => batch.status === 'UNDER_REVIEW')!;
    const services = mount(`/billing/batches/${pending.id}`);
    const review = vi.spyOn(services.ops.billing, 'reviewBatchInvoice');
    const user = await login('financial.reviewer');
    const table = await screen.findByTestId('review-table');
    const row = within(table)
      .getAllByTestId('review-row')
      .find((item) => within(item).queryByRole('button', { name: 'Fatura kararı' }))!;
    await user.click(within(row).getByRole('button', { name: 'Fatura kararı' }));
    expect(await screen.findByTestId('decision-form')).toBeTruthy();
    services.store.setState((state) => ({
      activeTenant: {
        ...state.activeTenant!,
        permissions: state.activeTenant!.permissions.filter(
          (permission) => permission !== 'batch.review',
        ),
      },
    }));
    await waitFor(() => expect(screen.queryByTestId('decision-form')).toBeNull());
    expect(screen.queryByTestId('decide-batch')).toBeNull();
    expect(review).not.toHaveBeenCalled();
  });

  it('hides settlement approval after grant loss and leaves recovery references unlinked', async () => {
    const pending = api.world.settlements.find(
      (settlement) => settlement.status === 'PENDING_APPROVAL',
    )!;
    const services = mount(`/billing/settlements/${pending.id}`);
    const approve = vi.spyOn(services.ops.billing, 'approveSettlement');
    const user = await login('payer.approver');
    expect(await screen.findByTestId('approve-settlement')).toBeTruthy();
    const recoveries = screen.getByTestId('recovery-list');
    expect(within(recoveries).queryAllByRole('link')).toHaveLength(0);
    await user.click(screen.getByRole('button', { name: 'İptal et' }));
    expect(await screen.findByTestId('cancel-form')).toBeTruthy();
    services.store.setState((state) => ({
      activeTenant: {
        ...state.activeTenant!,
        permissions: state.activeTenant!.permissions.filter(
          (permission) => permission !== 'settlement.approve',
        ),
      },
    }));
    await waitFor(() => expect(screen.queryByTestId('cancel-form')).toBeNull());
    expect(screen.queryByTestId('approve-settlement')).toBeNull();
    expect(approve).not.toHaveBeenCalled();
  });

  it.each([
    [['settlement.read'], false],
    [['settlement.read', 'settlement.approve'], true],
    [['settlement.read', 'settlement.record_payment'], true],
  ] as const)(
    'shows payment form only with a supported payment grant: %j',
    async (permissions, visible) => {
      const finance = api.world.accounts.find(
        (account) => account.username === 'financial.reviewer',
      )!;
      finance.memberships[0]!.permissions = [...permissions];
      const approved = api.world.settlements.find(
        (settlement) => settlement.status === 'APPROVED',
      )!;
      mount(`/billing/settlements/${approved.id}`);
      await login('financial.reviewer');
      expect(await screen.findByTestId('settlement-figures')).toBeTruthy();
      expect(screen.queryByTestId('payment-form') !== null).toBe(visible);
    },
  );

  it('closes a populated payment form when both payment grants are lost', async () => {
    const approved = api.world.settlements.find((settlement) => settlement.status === 'APPROVED')!;
    const services = mount(`/billing/settlements/${approved.id}`);
    const record = vi.spyOn(services.ops.billing, 'createPaymentRecord');
    const user = await login('financial.reviewer');
    const form = await screen.findByTestId('payment-form');
    await user.type(within(form).getByLabelText(/^Dış referans/), 'DRAFT-ONLY');
    services.store.setState((state) => ({
      activeTenant: {
        ...state.activeTenant!,
        permissions: state.activeTenant!.permissions.filter(
          (permission) =>
            permission !== 'settlement.record_payment' && permission !== 'settlement.approve',
        ),
      },
    }));
    await waitFor(() => expect(screen.queryByTestId('payment-form')).toBeNull());
    expect(record).not.toHaveBeenCalled();
  });

  it('re-reads detail for a new actor without showing the previous actor figures while pending', async () => {
    const pending = api.world.settlements.find(
      (settlement) => settlement.status === 'PENDING_APPROVAL',
    )!;
    const services = mount(`/billing/settlements/${pending.id}`);
    const original = services.ops.billing.getSettlement.bind(services.ops.billing);
    const read = vi.spyOn(services.ops.billing, 'getSettlement');
    await login('payer.approver');
    expect(await screen.findByTestId('settlement-figures')).toBeTruthy();
    let release: (() => Promise<void>) | null = null;
    read.mockImplementationOnce(
      (tenantId, settlementId) =>
        new Promise((resolve, reject) => {
          release = async () => {
            try {
              resolve(await original(tenantId, settlementId));
            } catch (error) {
              reject(error);
            }
          };
        }),
    );
    services.store.setState((state) => ({ session: { ...state.session!, actorId: 'next-actor' } }));
    await waitFor(() => expect(read).toHaveBeenCalledTimes(2));
    expect(screen.queryByTestId('settlement-figures')).toBeNull();
    await act(async () => {
      await release?.();
    });
    expect(await screen.findByTestId('settlement-figures')).toBeTruthy();
  });
});
