import { createKapsoraClient, createOperations, type Operations } from '@kapsora/api-client';
import { createSessionStore, type SessionStore } from '@kapsora/auth';
import { QueryClient } from '@tanstack/react-query';
import { createContext, useContext, type ReactNode } from 'react';

/** Everything a screen needs to talk to the API. One instance per app (or per test). */
export interface AppServices {
  ops: Operations;
  store: SessionStore;
  queryClient: QueryClient;
}

/** Builds the client, the session store and the query client wired together. */
export function createServices(
  options: { baseUrl?: string; fetch?: typeof fetch } = {},
): AppServices {
  let store: SessionStore | null = null;
  const client = createKapsoraClient({
    baseUrl: options.baseUrl ?? '',
    ...(options.fetch ? { fetch: options.fetch } : {}),
    csrfToken: () => store?.getState().csrfToken ?? null,
  });
  const ops = createOperations(client);
  store = createSessionStore(ops);
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: 1, staleTime: 15_000, refetchOnWindowFocus: false },
    },
  });
  return { ops, store, queryClient };
}

const ServicesContext = createContext<AppServices | null>(null);

export function ServicesProvider({
  services,
  children,
}: {
  services: AppServices;
  children: ReactNode;
}) {
  return <ServicesContext.Provider value={services}>{children}</ServicesContext.Provider>;
}

export function useServices(): AppServices {
  const s = useContext(ServicesContext);
  if (!s) throw new Error('useServices must be used inside <ServicesProvider>');
  return s;
}

export function useOps(): Operations {
  return useServices().ops;
}
