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

/** A command and its cached reads belong to one exact authorization context. */
function sessionFingerprint(token: string | null): string {
  if (!token) return '';
  let hash = 0xcbf29ce484222325n;
  for (let i = 0; i < token.length; i += 1) {
    hash ^= BigInt(token.charCodeAt(i));
    hash = (hash * 0x100000001b3n) & 0xffffffffffffffffn;
  }
  return hash.toString(16).padStart(16, '0');
}

export function directoryContextKey(state: ReturnType<SessionStore['getState']>): string {
  return [
    state.session?.actorId ?? '',
    sessionFingerprint(state.csrfToken),
    state.session?.expiresAt ?? '',
    state.activeTenant?.tenant.id ?? '',
    state.activeTenant?.canReadTenantUsers === true ? 'allowed' : 'denied',
    state.activeTenant?.canManageTenantUsers === true ? 'manage' : 'read',
    state.activeTenant?.canManageTenantRoles === true ? 'roles' : 'no-roles',
    state.activeTenant?.permissions.join('|') ?? '',
    JSON.stringify(state.activeTenant?.scopes ?? []),
  ].join(':');
}

/** Builds the client, the session store and the query client wired together. */
export function createServices(
  options: { baseUrl?: string; fetch?: typeof fetch } = {},
): AppServices {
  let store: SessionStore | null = null;
  const client = createKapsoraClient({
    baseUrl: options.baseUrl ?? '',
    app: 'backoffice',
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
  let previousDirectoryContext = directoryContextKey(store.getState());
  store.subscribe((state) => {
    const current = directoryContextKey(state);
    if (current === previousDirectoryContext) return;
    previousDirectoryContext = current;
    void queryClient.cancelQueries({ queryKey: ['admin-users'] });
    queryClient.removeQueries({ queryKey: ['admin-users'] });
    void queryClient.cancelQueries({ queryKey: ['admin-invitations'] });
    queryClient.removeQueries({ queryKey: ['admin-invitations'] });
    void queryClient.cancelQueries({ queryKey: ['admin-role-grants'] });
    queryClient.removeQueries({ queryKey: ['admin-role-grants'] });
    void queryClient.cancelQueries({ queryKey: ['admin-role-options'] });
    queryClient.removeQueries({ queryKey: ['admin-role-options'] });
    void queryClient.cancelQueries({ queryKey: ['admin-role-organizations'] });
    queryClient.removeQueries({ queryKey: ['admin-role-organizations'] });
    void queryClient.cancelQueries({ queryKey: ['admin-role-changes'] });
    queryClient.removeQueries({ queryKey: ['admin-role-changes'] });
    void queryClient.cancelQueries({ queryKey: ['admin-role-change-options'] });
    queryClient.removeQueries({ queryKey: ['admin-role-change-options'] });
    void queryClient.cancelQueries({ queryKey: ['admin-role-change-eligibility'] });
    queryClient.removeQueries({ queryKey: ['admin-role-change-eligibility'] });
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
