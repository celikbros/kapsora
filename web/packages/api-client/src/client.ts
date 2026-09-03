import createClient, { type Middleware } from 'openapi-fetch';
import type { paths } from './generated/kapsora-v1';

/** Options for the shared fetch client. */
export interface ClientOptions {
  /** Empty string means same origin (the dev server proxies /api to the Go API). */
  baseUrl?: string;
  /** Injected in tests; defaults to the global fetch. */
  fetch?: typeof globalThis.fetch;
  /** Correlation id per request; defaults to a random UUID. */
  requestId?: () => string;
  /**
   * CSRF token from GET /api/v1/session, kept in memory by the auth package. Added to
   * every state-changing request; null while anonymous.
   */
  csrfToken?: () => string | null;
}

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);

/** The typed openapi-fetch client for the KAPSORA v1 contract. */
export type KapsoraClient = ReturnType<typeof createClient<paths>>;

/**
 * Builds the client every package shares. The middleware adds the headers that do not
 * depend on the caller (X-Request-ID, Accept). Tenant, CSRF and idempotency headers are
 * part of the operation signatures in operations.ts because the contract requires them
 * per operation and the generated types enforce that.
 */
export function createKapsoraClient(options: ClientOptions = {}): KapsoraClient {
  const client = createClient<paths>({
    baseUrl: options.baseUrl ?? '',
    credentials: 'same-origin',
    ...(options.fetch ? { fetch: options.fetch } : {}),
  });
  const requestId = options.requestId ?? randomId;
  const middleware: Middleware = {
    onRequest({ request }) {
      if (!request.headers.has('X-Request-ID')) {
        request.headers.set('X-Request-ID', requestId());
      }
      if (!request.headers.has('Accept')) {
        request.headers.set('Accept', 'application/json, application/problem+json');
      }
      if (!SAFE_METHODS.has(request.method) && !request.headers.has('X-CSRF-Token')) {
        const token = options.csrfToken?.();
        if (token) {
          request.headers.set('X-CSRF-Token', token);
        }
      }
      return request;
    },
  };
  client.use(middleware);
  return client;
}

/** Random UUID for request and idempotency keys. */
export function randomId(): string {
  return globalThis.crypto.randomUUID();
}
