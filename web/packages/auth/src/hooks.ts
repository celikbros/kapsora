import { createContext, createElement, useContext, type ReactNode } from 'react';
import { useStore } from 'zustand';
import type { TenantContext } from '@kapsora/api-client';
import { hasPermission } from './guards';
import type { SessionState, SessionStore } from './store';

const SessionStoreContext = createContext<SessionStore | null>(null);

/** Provides the app's session store to the hooks below. */
export function SessionProvider(props: { store: SessionStore; children: ReactNode }) {
  return createElement(SessionStoreContext.Provider, { value: props.store }, props.children);
}

/** The store itself, for imperative calls (login, logout, switchTenant). */
export function useSessionStore(): SessionStore {
  const store = useContext(SessionStoreContext);
  if (!store) {
    throw new Error('useSessionStore must be used inside <SessionProvider>');
  }
  return store;
}

/** Reactive session state (or a slice of it). */
export function useSession(): SessionState;
export function useSession<T>(selector: (s: SessionState) => T): T;
export function useSession<T>(selector?: (s: SessionState) => T): T | SessionState {
  const store = useSessionStore();
  return useStore(store, selector ?? ((s) => s as unknown as T));
}

/** Active tenant context; null until one is selected. */
export function useTenant(): TenantContext | null {
  return useSession((s) => s.activeTenant);
}

/** Active tenant id; throws inside tenant-scoped screens that are mounted too early. */
export function useTenantId(): string {
  const tenant = useTenant();
  if (!tenant) {
    throw new Error(
      'useTenantId called without an active tenant; guard the route with requireTenant',
    );
  }
  return tenant.tenant.id;
}

/** Whether the active tenant grants the permission. UI-only; the backend re-validates. */
export function usePermission(permission: string): boolean {
  return useSession((s) => hasPermission(s, permission));
}
