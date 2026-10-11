import { createKapsoraClient, createOperations, type Operations } from '@kapsora/api-client';
import { createSessionStore, type SessionStore } from '@kapsora/auth';
import { QueryClient } from '@tanstack/react-query';
import { createContext, useContext, type ReactNode } from 'react';

export interface AppServices {
  ops: Operations;
  store: SessionStore;
  queryClient: QueryClient;
}

export function sessionFingerprint(state: ReturnType<SessionStore['getState']>): string {
  return `${state.session?.actorId ?? ''}:${JSON.stringify(state.activeTenant ?? null)}`;
}

/** Client + session store + query client, wired the same way as the backoffice. */
export function createServices(
  options: { baseUrl?: string; fetch?: typeof fetch } = {},
): AppServices {
  let store: SessionStore | null = null;
  const client = createKapsoraClient({
    baseUrl: options.baseUrl ?? '',
    app: 'provider',
    ...(options.fetch ? { fetch: options.fetch } : {}),
    csrfToken: () => store?.getState().csrfToken ?? null,
  });
  const ops = createOperations(client);
  store = createSessionStore(ops);
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: 1, staleTime: 15_000, refetchOnWindowFocus: false } },
  });
  let context = sessionFingerprint(store.getState());
  store.subscribe((state) => {
    const next = sessionFingerprint(state);
    if (next !== context) {
      context = next;
      // Most portal reads predate actor-scoped keys. Clear even in-flight entries before
      // another actor or grant/scope snapshot can render their cached person or clinical data.
      queryClient.clear();
    }
  });
  return {
    ops,
    store,
    queryClient,
  };
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
