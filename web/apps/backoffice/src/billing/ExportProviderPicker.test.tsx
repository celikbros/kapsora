// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ExportProviderPicker } from './ExportProviderPicker';

const mocks = vi.hoisted(() => ({
  session: {
    activeTenant: { tenant: { id: 'tenant-1' }, permissions: ['invoice.read', 'report.read'] },
    session: { actorId: 'actor-1' },
  },
  invoices: vi.fn(),
  settlements: vi.fn(),
  runs: vi.fn(),
}));

vi.mock('@kapsora/auth', () => ({
  useSession: (selector: (state: typeof mocks.session) => unknown) => selector(mocks.session),
}));
vi.mock('../api', () => ({
  useOps: () => ({
    billing: { listInvoices: mocks.invoices, listSettlements: mocks.settlements },
    report: { listRuns: mocks.runs },
  }),
}));
vi.mock('@kapsora/i18n', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onChange = vi.fn();
  render(
    <QueryClientProvider client={client}>
      <ExportProviderPicker value="" onChange={onChange} placeholder="All" />
    </QueryClientProvider>,
  );
  return onChange;
}

beforeEach(() => {
  mocks.session.activeTenant.tenant.id = 'tenant-1';
  mocks.session.session.actorId = 'actor-1';
  mocks.session.activeTenant.permissions = ['invoice.read', 'report.read'];
  mocks.invoices.mockReset();
  mocks.settlements.mockReset();
  mocks.runs.mockReset();
});
afterEach(cleanup);

describe('export provider choices from permitted records', () => {
  it('loads one invoice page at a time, deduplicates providers, and uses IDs where names are absent', async () => {
    mocks.invoices
      .mockResolvedValueOnce({
        items: [{ providerOrganizationId: 'provider-a', providerName: 'Hospital A' }],
        nextCursor: 'page-2',
      })
      .mockResolvedValueOnce({
        items: [{ providerOrganizationId: 'provider-a' }, { providerOrganizationId: 'provider-b' }],
        nextCursor: 'page-2',
      });
    const onChange = mount();
    const picker = await screen.findByTestId('export-provider-select');
    await waitFor(() =>
      expect(within(picker).getByRole('option', { name: 'Hospital A' })).toBeTruthy(),
    );
    expect(mocks.invoices).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByTestId('load-more-export-providers'));
    await waitFor(() =>
      expect(within(picker).getByRole('option', { name: 'provider-b' })).toBeTruthy(),
    );
    expect(within(picker).getAllByRole('option', { name: 'Hospital A' })).toHaveLength(1);
    expect(screen.queryByTestId('load-more-export-providers')).toBeNull();
    await userEvent.selectOptions(picker, 'provider-b');
    expect(onChange).toHaveBeenCalledWith('provider-b');
    expect(mocks.invoices).toHaveBeenCalledTimes(2);
    expect(mocks.settlements).not.toHaveBeenCalled();
    expect(mocks.runs).not.toHaveBeenCalled();
  });

  it('keeps loaded providers on a later-page failure and offers retry', async () => {
    mocks.invoices
      .mockResolvedValueOnce({
        items: [{ providerOrganizationId: 'provider-a', providerName: 'Hospital A' }],
        nextCursor: 'page-2',
      })
      .mockRejectedValueOnce(new Error('temporary'))
      .mockResolvedValueOnce({
        items: [{ providerOrganizationId: 'provider-b', providerName: 'Hotel B' }],
        nextCursor: null,
      });
    mount();
    const picker = await screen.findByTestId('export-provider-select');
    await waitFor(() =>
      expect(within(picker).getByRole('option', { name: 'Hospital A' })).toBeTruthy(),
    );
    await userEvent.click(screen.getByTestId('load-more-export-providers'));
    await waitFor(() => expect(screen.getByTestId('retry-export-providers')).toBeTruthy());
    expect(within(picker).getByRole('option', { name: 'Hospital A' })).toBeTruthy();
    expect(screen.queryByTestId('load-more-export-providers')).toBeNull();
    await userEvent.click(screen.getByTestId('retry-export-providers'));
    await waitFor(() =>
      expect(within(picker).getByRole('option', { name: 'Hotel B' })).toBeTruthy(),
    );
    expect(within(picker).getByRole('option', { name: 'Hospital A' })).toBeTruthy();
    expect(mocks.invoices).toHaveBeenCalledTimes(3);
    expect(mocks.invoices.mock.calls[1]?.[1]).toEqual({ limit: 100, cursor: 'page-2' });
    expect(mocks.invoices.mock.calls[2]?.[1]).toEqual({ limit: 100, cursor: 'page-2' });
  });

  it('uses settlement records when invoice access is absent', async () => {
    mocks.session.activeTenant.permissions = ['settlement.read', 'report.read'];
    mocks.settlements.mockResolvedValue({
      items: [{ providerOrganizationId: 'provider-s', providerName: 'Provider S' }],
      nextCursor: null,
    });
    mount();
    const picker = await screen.findByTestId('export-provider-select');
    await waitFor(() =>
      expect(within(picker).getByRole('option', { name: 'Provider S' })).toBeTruthy(),
    );
    expect(mocks.settlements).toHaveBeenCalledWith('tenant-1', { limit: 100 });
    expect(mocks.invoices).not.toHaveBeenCalled();
    expect(mocks.runs).not.toHaveBeenCalled();
  });

  it('does not share provider choices after actor or tenant changes', async () => {
    mocks.invoices
      .mockResolvedValueOnce({
        items: [{ providerOrganizationId: 'provider-a' }],
        nextCursor: null,
      })
      .mockResolvedValueOnce({
        items: [{ providerOrganizationId: 'provider-b' }],
        nextCursor: null,
      })
      .mockResolvedValueOnce({
        items: [{ providerOrganizationId: 'provider-c' }],
        nextCursor: null,
      });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const picker = () => <ExportProviderPicker value="" onChange={vi.fn()} placeholder="All" />;
    const view = render(<QueryClientProvider client={client}>{picker()}</QueryClientProvider>);
    await waitFor(() => expect(screen.getByRole('option', { name: 'provider-a' })).toBeTruthy());
    mocks.session.session.actorId = 'actor-2';
    view.rerender(<QueryClientProvider client={client}>{picker()}</QueryClientProvider>);
    await waitFor(() => expect(screen.getByRole('option', { name: 'provider-b' })).toBeTruthy());
    expect(screen.queryByRole('option', { name: 'provider-a' })).toBeNull();
    mocks.session.activeTenant.tenant.id = 'tenant-2';
    view.rerender(<QueryClientProvider client={client}>{picker()}</QueryClientProvider>);
    await waitFor(() => expect(screen.getByRole('option', { name: 'provider-c' })).toBeTruthy());
    expect(screen.queryByRole('option', { name: 'provider-b' })).toBeNull();
    expect(mocks.invoices.mock.calls.map((call) => call[0])).toEqual([
      'tenant-1',
      'tenant-1',
      'tenant-2',
    ]);
  });

  it('uses provider reconciliation runs for report-only exporters', async () => {
    mocks.session.activeTenant.permissions = ['report.read'];
    mocks.runs.mockResolvedValue({
      items: [
        { providerOrganizationId: 'provider-c', providerName: null },
        { providerOrganizationId: null },
      ],
      nextCursor: null,
    });
    mount();
    const picker = await screen.findByTestId('export-provider-select');
    await waitFor(() =>
      expect(within(picker).getByRole('option', { name: 'provider-c' })).toBeTruthy(),
    );
    expect(mocks.runs).toHaveBeenCalledWith('tenant-1', { limit: 100, scope: 'PROVIDER' });
    expect(mocks.invoices).not.toHaveBeenCalled();
    expect(mocks.settlements).not.toHaveBeenCalled();
  });
});
