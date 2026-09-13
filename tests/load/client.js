import http from 'k6/http';
import crypto from 'k6/crypto';
import exec from 'k6/execution';
import { Counter, Rate, Trend } from 'k6/metrics';

export const authenticatedContexts = new Counter('authenticated_contexts');
export const latency = new Trend('operation_latency', true);
export const unexpected = new Rate('unexpected_responses');
export const completed = new Counter('workflows_completed');
export const refusals = new Counter('business_refusals');
export const cleanupFailures = new Counter('cleanup_failures');
export const reconciliationFailures = new Counter('reconciliation_failures');
export const measuredRequests = new Counter('measured_api_requests');
export const successfulHolds = new Rate('successful_holds');

export function measuredPhase(mode) {
  if (mode === 'smoke') return true;
  const elapsed = Date.now() - exec.scenario.startTime;
  return elapsed >= 120000 && elapsed < 720000;
}

export function key() {
  return (
    'load-' +
    __ENV.KAPSORA_LOAD_RUN_ID.slice(0, 8) +
    '-' +
    crypto.sha256(crypto.randomBytes(32), 'hex')
  );
}

export function header(response, name) {
  const found = Object.keys(response.headers).find(
    (value) => value.toLowerCase() === name.toLowerCase(),
  );
  return found ? response.headers[found] : undefined;
}

export function request(baseUrl, session, method, path, body, settings = {}) {
  const accepted = settings.accepted ?? [200];
  // Every context carries its own cookie; no setup/VU jar may add another account's.
  http.cookieJar().clear(baseUrl);
  const headers = {
    'X-Kapsora-App': session.app,
    ...(session.cookie ? { Cookie: session.cookie } : {}),
    ...(session.csrf ? { 'X-CSRF-Token': session.csrf } : {}),
    ...(session.tenantId ? { 'X-Tenant-ID': session.tenantId } : {}),
    ...settings.headers,
  };
  if (method !== 'GET' && method !== 'HEAD') headers['Idempotency-Key'] = settings.key ?? key();
  if (!settings.multipart) headers['Content-Type'] = 'application/json';
  const response = http.request(
    method,
    baseUrl + path,
    body === undefined ? null : settings.multipart ? body : JSON.stringify(body),
    {
      headers,
      timeout: '15s',
      redirects: 0,
      responseType: 'text',
      // Never use resource ids, URLs, personal fields or session data as metric tags.
      tags: { name: settings.name ?? 'session', workload: settings.workload ?? 'control' },
      responseCallback: http.expectedStatuses(...accepted),
    },
  );
  const ok = accepted.includes(response.status);
  unexpected.add(!ok);
  if (settings.measured) measuredRequests.add(1);
  if (settings.primary && ok && response.status < 400) {
    latency.add(response.timings.duration, {
      workload: settings.workload,
      phase: settings.measured ? 'measured' : 'warmup',
    });
  }
  if (!ok) {
    // Error messages contain only the fixed operation name and numeric status.
    // In particular, never print a response body or request headers here.
    let code = '';
    try {
      const candidate = response.json().code;
      if (typeof candidate === 'string' && /^[A-Z][A-Z_0-9]{0,79}$/.test(candidate))
        code = ` (${candidate})`;
    } catch {
      /* A non-problem response stays a status-only error. */
    }
    throw new Error(`${settings.name ?? 'session'} returned HTTP ${response.status}${code}.`);
  }
  return response;
}

export function json(response) {
  if (!response.body) throw new Error('Expected a JSON response body.');
  return response.json();
}

export function authenticate(baseUrl, account, password) {
  const session = { app: account.app };
  // Do not let a cookie from another setup account replace that user's session.
  http.cookieJar().clear(baseUrl);
  const response = request(
    baseUrl,
    session,
    'POST',
    '/api/v1/session/login',
    {
      username: account.username,
      password,
    },
    { name: 'session.login' },
  );
  const state = json(response);
  if (state.mustChangePassword || !state.csrfToken)
    throw new Error('The load account is not ready.');
  const cookies = Object.entries(response.cookies);
  if (cookies.length !== 1 || !cookies[0][1][0]?.value)
    throw new Error('Expected one session cookie.');
  session.cookie = `${cookies[0][0]}=${cookies[0][1][0].value}`;
  session.csrf = state.csrfToken;
  session.tenantId = account.tenantId;
  const context = json(
    request(
      baseUrl,
      session,
      'POST',
      '/api/v1/session/switch-tenant',
      { tenantId: account.tenantId },
      { name: 'session.switch-tenant' },
    ),
  );
  session.permissions = context.permissions;
  return session;
}

export function elevate(baseUrl, session, password) {
  if (session.stepUpExpiresAt && Date.now() + 5000 < session.stepUpExpiresAt) return;
  const result = json(
    request(
      baseUrl,
      session,
      'POST',
      '/api/v1/session/step-up',
      { password },
      { name: 'session.step-up' },
    ),
  );
  session.stepUpExpiresAt = Date.parse(result.stepUpExpiresAt);
  if (!Number.isFinite(session.stepUpExpiresAt)) throw new Error('Invalid step-up expiry.');
}
