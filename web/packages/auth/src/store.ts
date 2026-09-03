/**
 * Session state for the browser. Everything lives in memory: the CSRF token, the actor
 * context and the active tenant are never written to localStorage or sessionStorage
 * (WP-I1-05 section 4.7). A reload re-bootstraps from GET /api/v1/session, which is what
 * the HttpOnly cookie is for.
 */
import {
  ApiError,
  type Operations,
  type SessionInfo,
  type TenantContext,
  type UserContext,
} from '@kapsora/api-client';
import { createStore, type StoreApi } from 'zustand/vanilla';

export type SessionStatus = 'idle' | 'loading' | 'anonymous' | 'authenticated';

export interface SessionState {
  status: SessionStatus;
  session: SessionInfo | null;
  me: UserContext | null;
  /** Kept in memory only; read by the API client middleware. */
  csrfToken: string | null;
  activeTenant: TenantContext | null;
  /** Last bootstrap failure that was not a plain 401. */
  bootstrapError: ApiError | null;
}

export interface SessionActions {
  /** Loads the session once; concurrent callers share the same promise. */
  bootstrap(): Promise<SessionState>;
  /** Forces a fresh GET /session + /me. */
  refresh(): Promise<SessionState>;
  login(username: string, password: string): Promise<SessionState>;
  logout(): Promise<void>;
  switchTenant(tenantId: string): Promise<TenantContext>;
  /** Drops to anonymous without calling the server (e.g. after a 401 elsewhere). */
  invalidate(): void;
}

export type SessionStore = StoreApi<SessionState> & SessionActions;

const initial: SessionState = {
  status: 'idle',
  session: null,
  me: null,
  csrfToken: null,
  activeTenant: null,
  bootstrapError: null,
};

function pickActive(me: UserContext, session: SessionInfo): TenantContext | null {
  if (!session.activeTenantId) return null;
  return me.tenants.find((t) => t.tenant.id === session.activeTenantId) ?? null;
}

/** Creates the store bound to one Operations instance (one per app). */
export function createSessionStore(ops: Operations): SessionStore {
  const store = createStore<SessionState>(() => ({ ...initial }));
  let inflight: Promise<SessionState> | null = null;

  async function load(): Promise<SessionState> {
    store.setState({ status: 'loading', bootstrapError: null });
    try {
      const session = await ops.session.get();
      // The token must be in place before /me so a CSRF-guarded server never sees a bare call.
      store.setState({ csrfToken: session.csrfToken, session });
      const me = await ops.session.me();
      const next: SessionState = {
        status: 'authenticated',
        session,
        me,
        csrfToken: session.csrfToken,
        activeTenant: pickActive(me, session),
        bootstrapError: null,
      };
      store.setState(next);
      return next;
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        const next: SessionState = { ...initial, status: 'anonymous' };
        store.setState(next);
        return next;
      }
      const next: SessionState = {
        ...initial,
        status: 'anonymous',
        bootstrapError: err instanceof ApiError ? err : null,
      };
      store.setState(next);
      throw err;
    }
  }

  const actions: SessionActions = {
    bootstrap() {
      const state = store.getState();
      if (state.status === 'authenticated' || state.status === 'anonymous') {
        return Promise.resolve(state);
      }
      if (!inflight) {
        inflight = load().finally(() => {
          inflight = null;
        });
      }
      return inflight;
    },
    refresh() {
      if (!inflight) {
        inflight = load().finally(() => {
          inflight = null;
        });
      }
      return inflight;
    },
    async login(username, password) {
      const session = await ops.session.login(username, password);
      store.setState({ csrfToken: session.csrfToken, session });
      const me = await ops.session.me();
      const next: SessionState = {
        status: 'authenticated',
        session,
        me,
        csrfToken: session.csrfToken,
        activeTenant: pickActive(me, session),
        bootstrapError: null,
      };
      store.setState(next);
      return next;
    },
    async logout() {
      try {
        await ops.session.logout();
      } finally {
        store.setState({ ...initial, status: 'anonymous' });
      }
    },
    async switchTenant(tenantId) {
      const ctx = await ops.session.switchTenant(tenantId);
      const state = store.getState();
      store.setState({
        activeTenant: ctx,
        session: state.session
          ? { ...state.session, activeTenantId: ctx.tenant.id }
          : state.session,
      });
      return ctx;
    },
    invalidate() {
      store.setState({ ...initial, status: 'anonymous' });
    },
  };

  return Object.assign(store, actions);
}
