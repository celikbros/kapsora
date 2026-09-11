import { createContext, createElement, useContext, useEffect, useRef, type ReactNode } from 'react';
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

/**
 * The person this account is in the active tenant (selfPersonId), whichever app it is in, or
 * null. A reviewer's screens compare it with a file's person: a file of their own is one they
 * may read but not decide.
 */
export function useSelfPersonId(): string | null {
  return useSession((s) => s.activeTenant?.selfPersonId ?? null);
}

/** Whether the active tenant grants the permission. UI-only; the backend re-validates. */
export function usePermission(permission: string): boolean {
  return useSession((s) => hasPermission(s, permission));
}

/**
 * Re-checks whose session this is whenever the tab comes back to the front, and calls
 * `onChange` when another tab signed out or signed in as somebody else. Without it a page
 * left open would go on showing — and acting on — the previous account's screen with the new
 * account's session behind it.
 */
export function useAccountWatch(onChange: (result: 'changed' | 'signed-out') => void): void {
  const store = useSessionStore();
  const latest = useRef(onChange);
  useEffect(() => {
    latest.current = onChange;
  }, [onChange]);
  useEffect(() => {
    let checking = false;
    const check = () => {
      if (document.visibilityState !== 'visible' || checking) return;
      checking = true;
      void store
        .checkAccount()
        .then((result) => {
          if (result !== 'same') latest.current(result);
        })
        .finally(() => {
          checking = false;
        });
    };
    document.addEventListener('visibilitychange', check);
    window.addEventListener('focus', check);
    return () => {
      document.removeEventListener('visibilitychange', check);
      window.removeEventListener('focus', check);
    };
  }, [store]);
}
