// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { useLedger } from './queries';

const { listLedger } = vi.hoisted(() => ({ listLedger: vi.fn() }));

vi.mock('@kapsora/auth', () => ({ useTenantId: () => 'tenant-a' }));
vi.mock('../api', () => ({ useOps: () => ({ entitlements: { listLedger } }) }));

describe('ledger query', () => {
  it('waits for an account and clears prior account movements during a switch', async () => {
    let finishSecond!: (value: unknown) => void;
    listLedger.mockImplementation((_tenant: string, account: string) =>
      account === 'account-a'
        ? Promise.resolve({ items: [{ id: 'movement-a' }] })
        : new Promise((resolve) => {
            finishSecond = resolve;
          }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    const { result, rerender } = renderHook(
      ({ accountId }) => useLedger(accountId, { limit: 20 }),
      { initialProps: { accountId: '' }, wrapper },
    );
    expect(listLedger).not.toHaveBeenCalled();

    rerender({ accountId: 'account-a' });
    await waitFor(() => expect(result.current.data?.items[0]?.id).toBe('movement-a'));
    rerender({ accountId: 'account-b' });
    expect(result.current.isPending).toBe(true);
    expect(result.current.data).toBeUndefined();
    await waitFor(() =>
      expect(listLedger).toHaveBeenCalledWith('tenant-a', 'account-b', { limit: 20 }),
    );
    finishSecond({ items: [{ id: 'movement-b' }] });
    await waitFor(() => expect(result.current.data?.items[0]?.id).toBe('movement-b'));
    client.clear();
  });
});
