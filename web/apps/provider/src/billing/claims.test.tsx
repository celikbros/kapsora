// @vitest-environment jsdom
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { SessionProvider } from '@kapsora/auth';
import { QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, renderHook, waitFor } from '@testing-library/react';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';
import type { ReactNode } from 'react';

import { ServicesProvider, createServices } from '../services';
import { useClaimSummaries } from './claims';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => {
  cleanup();
  api.reset();
  vi.restoreAllMocks();
});
afterAll(() => server.close());

it('stops nested claim reads after an actor switch cancels the old summary loop', async () => {
  const services = createServices({ baseUrl: 'http://mock.test' });
  await services.store.login('billing.a', 'demo parola 2026 kapsora');
  let resolveFirst!: (value: Awaited<ReturnType<typeof services.ops.claims.get>>) => void;
  const first = new Promise<Awaited<ReturnType<typeof services.ops.claims.get>>>((resolve) => {
    resolveFirst = resolve;
  });
  const get = vi.spyOn(services.ops.claims, 'get').mockImplementationOnce(() => first);
  const readiness = vi.spyOn(services.ops.claims, 'readiness');
  const wrapper = ({ children }: { children: ReactNode }) => (
    <ServicesProvider services={services}>
      <QueryClientProvider client={services.queryClient}>
        <SessionProvider store={services.store}>{children}</SessionProvider>
      </QueryClientProvider>
    </ServicesProvider>
  );
  const { unmount } = renderHook(() => useClaimSummaries(['first-claim', 'second-claim']), {
    wrapper,
  });
  await waitFor(() => expect(get).toHaveBeenCalledTimes(1));
  services.store.setState((state) => ({
    session: { ...state.session!, actorId: 'different-actor' },
  }));
  unmount();
  await act(async () =>
    resolveFirst({ data: { reference: 'old', status: 'APPROVED' } } as Awaited<
      ReturnType<typeof services.ops.claims.get>
    >),
  );
  expect(get).toHaveBeenCalledTimes(1);
  expect(readiness).not.toHaveBeenCalled();
  expect(services.queryClient.getQueryCache().findAll()).toHaveLength(0);
});
