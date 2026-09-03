import { createMockServer } from '@kapsora/api-client/mocks/node';
import { SessionProvider } from '@kapsora/auth';
import { initI18n } from '@kapsora/i18n';
import { ToastProvider } from '@kapsora/ui';
import { QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { ServicesProvider, createServices, type AppServices } from '../api';
import { AdjustmentQueuePage } from './AdjustmentQueuePage';

const { api, server } = createMockServer({ organizationsPerTenant: 4 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => api.reset());
afterAll(() => server.close());

function Harness({ services, children }: { services: AppServices; children: ReactNode }) {
  return (
    <ServicesProvider services={services}>
      <QueryClientProvider client={services.queryClient}>
        <SessionProvider store={services.store}>
          <ToastProvider>{children}</ToastProvider>
        </SessionProvider>
      </QueryClientProvider>
    </ServicesProvider>
  );
}

/**
 * The queue is not on a route yet, so the page is mounted directly under the same
 * providers the app gives it. The session is established first, because a tenant-scoped
 * screen may not render before an active tenant exists.
 */
async function mount(username = 'admin.a') {
  const services = createServices({ baseUrl: BASE });
  await services.store.login(username, PASSWORD);
  render(
    <Harness services={services}>
      <AdjustmentQueuePage />
    </Harness>,
  );
  return { services, user: userEvent.setup() };
}

function actorId(username: string): string {
  const account = api.world.accounts.find((a) => a.username === username);
  if (!account) throw new Error(`fixture: no actor ${username}`);
  return account.actorId;
}

/** Seeds one PENDING adjustment; the world is rebuilt between tests, so this runs per test. */
function seedAdjustment(options: { requester: string; delta: string; minutesAgo?: number }) {
  const tenantId = api.world.tenants[0]!.id;
  const account = api.world.entitlementAccounts.find((a) => a.tenantId === tenantId);
  if (!account) throw new Error('fixture: no entitlement account in the first tenant');
  const adjustment = {
    id: api.world.nextId(),
    tenantId,
    accountId: account.id,
    deltaQuantity: options.delta,
    reasonCode: 'CORRECTION',
    reasonText: 'Sözleşme eki gereği',
    status: 'PENDING' as const,
    requestedBy: actorId(options.requester),
    requestedAt: new Date(Date.now() - (options.minutesAgo ?? 0) * 60_000).toISOString(),
    decidedBy: null,
    decidedAt: null,
    decisionComment: null,
    ledgerEntryId: null,
    rowVersion: 1,
  };
  api.world.adjustments.push(adjustment);
  return { adjustment, account };
}

/** The queue row that names this actor as the requester. */
async function rowOf(requester: string): Promise<HTMLElement> {
  const table = await screen.findByTestId('adjustment-table');
  const id = actorId(requester);
  const row = within(table)
    .getAllByRole('row')
    .find((candidate) => candidate.textContent?.includes(id));
  if (!row) throw new Error(`no queue row requested by ${requester}`);
  return row;
}

async function stepUp(user: ReturnType<typeof userEvent.setup>) {
  const dialog = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await user.type(within(dialog).getByLabelText(/^Parola/), PASSWORD);
  await user.click(within(dialog).getByRole('button', { name: 'Doğrula' }));
}

describe('adjustment approval queue', () => {
  it('lists what each pending adjustment would move, and onto which account', async () => {
    const { account } = seedAdjustment({ requester: 'both.ab', delta: '-1.500000' });
    await mount();

    const table = await screen.findByTestId('adjustment-table');
    await waitFor(() => expect(table.textContent).toContain(account.definition.name));
    const row = await rowOf('both.ab');
    // The delta is decimal text with its sign spelled out, never a parsed number.
    expect(row.textContent).toContain('-1.5');
    expect(row.textContent).toContain('CORRECTION');
    expect(row.textContent).toContain('Onay bekliyor');
    expect(within(row).getByRole('button', { name: 'Onayla' })).toBeInTheDocument();
    expect(within(row).getByRole('button', { name: 'Reddet' })).toBeInTheDocument();
  });

  it('shows an adding delta with a plus sign', async () => {
    seedAdjustment({ requester: 'both.ab', delta: '12.000000' });
    await mount();
    const row = await rowOf('both.ab');
    expect(row.textContent).toContain('+12');
  });

  it('offers no decision on the operator’s own request and says why', async () => {
    seedAdjustment({ requester: 'admin.a', delta: '-2.000000', minutesAgo: 5 });
    seedAdjustment({ requester: 'both.ab', delta: '3.000000' });
    await mount();

    const own = await rowOf('admin.a');
    expect(within(own).queryByRole('button', { name: 'Onayla' })).not.toBeInTheDocument();
    expect(within(own).queryByRole('button', { name: 'Reddet' })).not.toBeInTheDocument();
    expect(own.textContent).toContain('Kendi talebinizi onaylayamazsınız');

    const other = await rowOf('both.ab');
    expect(within(other).getByRole('button', { name: 'Onayla' })).toBeInTheDocument();
  });

  it('asks for the password, then applies the approved delta to the balance', async () => {
    const { adjustment, account } = seedAdjustment({ requester: 'both.ab', delta: '-1.500000' });
    const before = account.available;
    const { user } = await mount();

    const row = await rowOf('both.ab');
    await user.click(within(row).getByRole('button', { name: 'Onayla' }));
    const dialog = await screen.findByRole('dialog', { name: 'Onayla' });
    // The confirmation restates the delta and the account before anything moves.
    expect(dialog.textContent).toContain('-1.5');
    await user.type(within(dialog).getByLabelText('Karar notu'), 'Tutanak ekli');
    await user.click(within(dialog).getByRole('button', { name: 'Onayla' }));

    await stepUp(user);

    expect(await screen.findByText('Düzeltme onaylandı ve bakiyeye işlendi.')).toBeInTheDocument();
    await waitFor(() => expect(adjustment.status).toBe('APPROVED'));
    expect(adjustment.decidedBy).toBe(actorId('admin.a'));
    expect(adjustment.decisionComment).toBe('Tutanak ekli');
    // The ledger, not the browser, computed the new balance.
    expect(
      api.world.ledgerEntries.some(
        (e) => e.referenceId === adjustment.id && e.movementType === 'ADJUST',
      ),
    ).toBe(true);
    expect(account.available).not.toBe(before);
    await screen.findByText('Bekleyen düzeltme yok.');
  });

  it('renders the server refusal when the queue no longer reflects the requester', async () => {
    const { adjustment } = seedAdjustment({ requester: 'both.ab', delta: '4.000000' });
    const { user } = await mount();

    const row = await rowOf('both.ab');
    await user.click(within(row).getByRole('button', { name: 'Onayla' }));
    const dialog = await screen.findByRole('dialog', { name: 'Onayla' });
    // The row was listed before this operator became the requester; the server decides.
    adjustment.requestedBy = actorId('admin.a');
    await user.click(within(dialog).getByRole('button', { name: 'Onayla' }));
    await stepUp(user);

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveAttribute('data-problem-code', 'MAKER_CHECKER_SAME_ACTOR');
    expect(await screen.findByText(/Kendi talebinizi onaylayamazsınız/)).toBeInTheDocument();
    expect(adjustment.status).toBe('PENDING');
  });

  it('rejects only with a reason, and nothing moves', async () => {
    const { adjustment, account } = seedAdjustment({ requester: 'both.ab', delta: '-6.000000' });
    const before = account.available;
    const { user } = await mount();

    const row = await rowOf('both.ab');
    await user.click(within(row).getByRole('button', { name: 'Reddet' }));
    const dialog = await screen.findByRole('dialog', { name: 'Reddet' });
    const submit = within(dialog).getByRole('button', { name: 'Reddet' });
    expect(submit).toBeDisabled();

    await user.type(within(dialog).getByLabelText(/Sebep kodu/), 'NOT_SUPPORTED');
    await user.type(within(dialog).getByLabelText('Açıklama'), 'Belge eksik');
    await user.click(submit);
    await stepUp(user);

    expect(await screen.findByText('Düzeltme reddedildi.')).toBeInTheDocument();
    await waitFor(() => expect(adjustment.status).toBe('REJECTED'));
    expect(account.available).toBe(before);
  });

  it('pages through the queue with the server cursor', async () => {
    for (let i = 0; i < 26; i++) {
      seedAdjustment({ requester: 'both.ab', delta: '1.000000', minutesAgo: i });
    }
    const { user } = await mount();

    const table = await screen.findByTestId('adjustment-table');
    await waitFor(() => expect(within(table).getAllByRole('row')).toHaveLength(26));
    await user.click(screen.getByRole('button', { name: 'Sonraki sayfa' }));
    await waitFor(() =>
      expect(within(screen.getByTestId('adjustment-table')).getAllByRole('row')).toHaveLength(2),
    );
    expect(screen.getByText('Sayfa 2')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Önceki sayfa' }));
    await waitFor(() =>
      expect(within(screen.getByTestId('adjustment-table')).getAllByRole('row')).toHaveLength(26),
    );
  });

  it('hides both decisions from an operator without entitlement.adjust', async () => {
    seedAdjustment({ requester: 'admin.a', delta: '5.000000' });
    await mount('reviewer.a');
    const row = await rowOf('admin.a');
    expect(within(row).queryByRole('button', { name: 'Onayla' })).not.toBeInTheDocument();
    expect(within(row).queryByRole('button', { name: 'Reddet' })).not.toBeInTheDocument();
  });

  it('keeps nothing in browser storage', async () => {
    seedAdjustment({ requester: 'both.ab', delta: '-1.500000' });
    const { user } = await mount();
    const row = await rowOf('both.ab');
    await user.click(within(row).getByRole('button', { name: 'Onayla' }));
    await screen.findByRole('dialog', { name: 'Onayla' });
    expect(Object.keys(localStorage)).toHaveLength(0);
    expect(Object.keys(sessionStorage)).toHaveLength(0);
  });
});
