/**
 * Route guards for TanStack Router `beforeLoad`. They redirect instead of rendering so
 * a protected screen never mounts for an anonymous user.
 */
import { redirect } from '@tanstack/react-router';
import type { SessionState, SessionStore } from './store';

export const LOGIN_PATH = '/auth/login';
export const TENANT_PICKER_PATH = '/auth/tenant';
export const PASSWORD_PATH = '/auth/password';

/** The part of the router location the guards need. */
export interface GuardLocation {
  pathname: string;
  searchStr?: string;
}

function returnTo(location: GuardLocation): string {
  return `${location.pathname}${location.searchStr ?? ''}`;
}

/** Redirect by href so the guards work in every app regardless of its route tree. */
function redirectTo(path: string, returnToPath?: string): never {
  const href = returnToPath ? `${path}?returnTo=${encodeURIComponent(returnToPath)}` : path;
  throw redirect({ href });
}

/** Only same-origin paths are accepted as return targets, so no open redirect. */
export function safeReturnTo(raw: string | undefined | null, fallback = '/'): string {
  if (!raw) return fallback;
  if (!raw.startsWith('/') || raw.startsWith('//') || raw.startsWith('/\\')) return fallback;
  if (raw.startsWith('/auth/')) return fallback;
  return raw;
}

/** Signed in, otherwise redirect to the login page with the return path. */
export async function requireAuthenticated(
  store: SessionStore,
  location: GuardLocation,
): Promise<SessionState> {
  const state = await store.bootstrap();
  if (state.status !== 'authenticated' || !state.session || !state.me) {
    redirectTo(LOGIN_PATH, returnTo(location));
  }
  if (state.session.mustChangePassword && location.pathname !== PASSWORD_PATH) {
    redirectTo(PASSWORD_PATH);
  }
  return state;
}

/**
 * Signed in with an active tenant. One membership is selected automatically; several
 * memberships send the user to the picker; none shows the "no tenants" page.
 */
export async function requireTenant(
  store: SessionStore,
  location: GuardLocation,
): Promise<SessionState> {
  const state = await requireAuthenticated(store, location);
  if (state.activeTenant) return state;
  const tenants = state.me!.tenants.filter((t) => t.tenant.status === 'ACTIVE');
  if (tenants.length === 1) {
    await store.switchTenant(tenants[0]!.tenant.id);
    return store.getState();
  }
  redirectTo(TENANT_PICKER_PATH, returnTo(location));
}

/** Sends an already signed-in user away from the login page. */
export async function redirectIfAuthenticated(store: SessionStore, target = '/'): Promise<void> {
  const state = await store.bootstrap();
  if (state.status === 'authenticated') {
    redirectTo(target);
  }
}

/** True when the active tenant grants the permission; the backend re-checks anyway. */
export function hasPermission(
  state: Pick<SessionState, 'activeTenant'>,
  permission: string,
): boolean {
  return state.activeTenant?.permissions.includes(permission) ?? false;
}
