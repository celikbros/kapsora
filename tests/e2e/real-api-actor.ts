import { setTimeout as delay } from 'node:timers/promises';
import { randomUUID } from 'node:crypto';
import { expect, type APIRequestContext } from '@playwright/test';

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const password = process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora';

/** Separate cookie jars for the real actors. Credentials/bodies never enter test attachments. */
export class Actor {
  private csrf = '';
  private cookie = '';
  private tenant = '';
  constructor(
    private context: APIRequestContext,
    private app: string,
    private retryRateLimit = false,
  ) {}
  async call<T>(
    method: string,
    path: string,
    body?: unknown,
    options: {
      expected?: number;
      etag?: string;
      key?: string;
    } = {},
  ) {
    const requestOptions = {
      method,
      maxRedirects: 0,
      headers: {
        'X-Kapsora-App': this.app,
        ...(this.cookie ? { Cookie: this.cookie } : {}),
        ...(this.csrf ? { 'X-CSRF-Token': this.csrf } : {}),
        ...(this.tenant ? { 'X-Tenant-ID': this.tenant } : {}),
        ...(method !== 'GET' ? { 'Idempotency-Key': options.key ?? randomUUID() } : {}),
        ...(options.etag ? { 'If-Match': options.etag } : {}),
        ...(method === 'PATCH' ? { 'Content-Type': 'application/merge-patch+json' } : {}),
      },
      ...(body === undefined ? {} : { data: body }),
    };
    let response = await this.context.fetch(base + path, requestOptions);
    // Opt-in for long acceptance flows: honor server backpressure with the exact same
    // command key/body/ETag, never by raising limits or replaying other failures.
    for (
      let retry = 0;
      this.retryRateLimit && options.expected !== 429 && response.status() === 429 && retry < 3;
      retry++
    ) {
      const seconds = Number(response.headers()['retry-after']);
      if (!Number.isFinite(seconds) || seconds <= 0 || seconds > 30) break;
      await delay(seconds * 1000);
      response = await this.context.fetch(base + path, requestOptions);
    }
    const cookie = response
      .headersArray()
      .find((h) => h.name.toLowerCase() === 'set-cookie' && !h.value.startsWith('__Host-csrf'));
    if (cookie) this.cookie = cookie.value.split(';')[0]!;
    // Do not include response bodies: a failed auth request may carry sensitive detail.
    expect(response.status(), `${method} ${path.split('?')[0]}`).toBe(options.expected ?? 200);
    const data = response.status() === 204 ? null : await response.json();
    return { data: data as T, etag: response.headers()['etag'] ?? '' };
  }
  async login(username: string) {
    const login = await this.call<{ csrfToken: string }>('POST', '/api/v1/session/login', {
      username,
      password,
    });
    this.csrf = login.data.csrfToken;
    const tenants = await this.call<{ items: { id: string; code: string }[] }>(
      'GET',
      '/api/v1/tenants',
    );
    this.tenant = tenants.data.items.find((t) => t.code === 'DEMO_A')!.id;
    const switched = await this.call<{ csrfToken: string }>(
      'POST',
      '/api/v1/session/switch-tenant',
      { tenantId: this.tenant },
    );
    if (switched.data.csrfToken) this.csrf = switched.data.csrfToken;
  }
  async close() {
    try {
      if (this.csrf)
        await this.call('POST', '/api/v1/session/logout', undefined, { expected: 204 });
    } finally {
      await this.context.dispose();
    }
  }
}
