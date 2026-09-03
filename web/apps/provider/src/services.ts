import { createKapsoraClient, createOperations, type Operations } from '@kapsora/api-client';
import { createSessionStore, type SessionStore } from '@kapsora/auth';
import { QueryClient } from '@tanstack/react-query';

export interface AppServices {
  ops: Operations;
  store: SessionStore;
  queryClient: QueryClient;
}

/** Client + session store + query client, wired the same way as the backoffice. */
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
  return {
    ops,
    store,
    queryClient: new QueryClient({
      defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } },
    }),
  };
}
