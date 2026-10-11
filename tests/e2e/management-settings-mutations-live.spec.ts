import { randomUUID } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { mkdir, readFile, rename, writeFile } from 'node:fs/promises';
import { setTimeout as delay } from 'node:timers/promises';
import { dirname, isAbsolute, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, request, test, type APIRequestContext } from '@playwright/test';

type Field = {
  effectiveValue: number;
  defaultValue: number;
  source: 'CONFIGURED' | 'DEFAULT' | 'INVALID_FALLBACK';
};
type Settings = { holdMinutes: Field; quoteTtlMinutes: Field; canManage: boolean };
type Reply = { status: number; data: Record<string, unknown>; etag: string; replayed: boolean };
type Intent = {
  key: string;
  body: string;
  etag: string;
  createdAt: string;
  response?: { etag: string; data: Settings };
};
type Baseline = { holdMinutes: number; quoteTtlMinutes: number; etag: string };
type Fixture = {
  version: 1;
  base: string;
  tenantId: string;
  baseline: Baseline;
  bookingFingerprint: string;
  change: Intent;
  replayVerified?: boolean;
  restore?: Intent;
  negative?: Record<string, Intent>;
  complete?: boolean;
};
type DbPreflight = {
  canonical: boolean;
  hold: number | null;
  quote: number | null;
  pending: number;
  bookingFingerprint: string;
};

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const fixturePath = process.env['E2E_MANAGEMENT_SETTINGS_FIXTURE'] ?? '';
const settingsPath = '/api/v1/admin/tenant-settings/accommodation';
const localUrl = (() => {
  try {
    const url = new URL(base);
    return url.protocol === 'http:' && url.hostname === '127.0.0.1' && url.port === '5181';
  } catch {
    return false;
  }
})();
test.use({ trace: 'off', screenshot: 'off', video: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' ||
    process.env['E2E_MANAGEMENT_SETTINGS_MUTATIONS'] !== '1' ||
    !localUrl ||
    !fixturePath,
  'requires exact mutation opt-in, operator UI at 127.0.0.1:5181, and private fixture',
);

function validateLocalDatabase(): URL {
  const raw = process.env['KAPSORA_MIGRATE_DATABASE_URL'] ?? '';
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new Error('Local migration database URL is required for read-only preflight');
  }
  expect(url.protocol).toBe('postgres:');
  expect(['localhost', '127.0.0.1']).toContain(url.hostname);
  expect(url.pathname).toBe('/kapsora');
  return url;
}

function databasePreflight(tenantId: string): DbPreflight {
  expect(tenantId).toMatch(/^[0-9a-f-]{36}$/i);
  const database = validateLocalDatabase();
  const psql = process.env['E2E_PSQL_PATH'] ?? 'C:\\Program Files\\PostgreSQL\\18\\bin\\psql.exe';
  const sql = `BEGIN READ ONLY;
SELECT json_build_object(
  'canonical', count(*) FILTER (WHERE s.setting_key IN ('accommodation.hold_minutes','accommodation.quote_ttl_minutes') AND NOT s.is_sensitive AND jsonb_typeof(s.value_json)='number' AND s.value_json::text ~ '^[1-9][0-9]{0,3}$' AND (s.value_json::text)::integer BETWEEN 1 AND 1440)=2,
  'hold', max((s.value_json::text)::integer) FILTER (WHERE s.setting_key='accommodation.hold_minutes' AND jsonb_typeof(s.value_json)='number' AND s.value_json::text ~ '^[1-9][0-9]{0,3}$'),
  'quote', max((s.value_json::text)::integer) FILTER (WHERE s.setting_key='accommodation.quote_ttl_minutes' AND jsonb_typeof(s.value_json)='number' AND s.value_json::text ~ '^[1-9][0-9]{0,3}$'),
  'pending', (SELECT count(*) FROM accommodation.booking WHERE tenant_id='${tenantId}'::uuid AND status IN ('HOLD','PENDING_APPROVAL')),
  'bookingFingerprint', (SELECT md5(coalesce(string_agg(row_to_json(b)::text, ',' ORDER BY b.id),'')) FROM accommodation.booking b WHERE b.tenant_id='${tenantId}'::uuid AND b.status IN ('CONFIRMED','NO_SHOW'))
) FROM platform.tenant_setting s WHERE s.tenant_id='${tenantId}'::uuid AND s.setting_key IN ('accommodation.hold_minutes','accommodation.quote_ttl_minutes');
COMMIT;`;
  try {
    const output = execFileSync(
      psql,
      ['-X', '-q', '-A', '-t', '-w', '-v', 'ON_ERROR_STOP=1', '-c', sql],
      {
        encoding: 'utf8',
        timeout: 20_000,
        env: {
          ...process.env,
          PGHOST: database.hostname,
          PGPORT: database.port || '5432',
          PGUSER: decodeURIComponent(database.username),
          PGPASSWORD: decodeURIComponent(database.password),
          PGDATABASE: database.pathname.slice(1),
          PGSSLMODE: database.searchParams.get('sslmode') ?? 'disable',
        },
        stdio: ['ignore', 'pipe', 'pipe'],
      },
    );
    return JSON.parse(output.trim()) as DbPreflight;
  } catch {
    // psql errors can contain a connection URL. Never include stdout/stderr or child errors.
    throw new Error('Local read-only settings/database preflight failed');
  }
}

class LocalActor {
  private csrf = '';
  private cookie = '';
  private tenantId = '';
  constructor(private readonly context: APIRequestContext) {}
  async call(
    method: string,
    path: string,
    body?: string,
    etag?: string,
    key?: string,
  ): Promise<Reply> {
    const requestOptions = {
      method,
      maxRedirects: 0,
      timeout: 20_000,
      headers: {
        'X-Kapsora-App': 'backoffice',
        ...(this.cookie ? { Cookie: this.cookie } : {}),
        ...(this.csrf ? { 'X-CSRF-Token': this.csrf } : {}),
        ...(this.tenantId ? { 'X-Tenant-ID': this.tenantId } : {}),
        ...(etag ? { 'If-Match': etag } : {}),
        ...(key
          ? { 'Idempotency-Key': key }
          : method === 'POST'
            ? { 'Idempotency-Key': randomUUID() }
            : {}),
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      ...(body === undefined ? {} : { data: body }),
    };
    const send = async () => {
      try {
        return await this.context.fetch(base + path, requestOptions);
      } catch {
        throw new Error(`Transport failed: ${method} ${path}`);
      }
    };
    let response = await send();
    for (let retry = 0; response.status() === 429 && retry < 3; retry++) {
      const seconds = Number(response.headers()['retry-after']);
      if (!Number.isFinite(seconds) || seconds < 1 || seconds > 30) break;
      await delay(seconds * 1000);
      response = await send();
    }
    const sessionCookie = response.headersArray().find((header) => {
      if (header.name.toLowerCase() !== 'set-cookie') return false;
      return !header.value.startsWith('__Host-csrf') && !header.value.startsWith('csrf');
    });
    if (sessionCookie) this.cookie = sessionCookie.value.split(';')[0]!;
    let data: Record<string, unknown> = {};
    if (response.status() !== 204) {
      try {
        data = (await response.json()) as Record<string, unknown>;
      } catch {
        throw new Error(`Invalid JSON response: ${method} ${path}`);
      }
    }
    return {
      status: response.status(),
      data,
      etag: response.headers()['etag'] ?? '',
      replayed: response.headers()['idempotent-replayed'] === 'true',
    };
  }
  async login(username: string) {
    const password = process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora';
    const login = await this.call(
      'POST',
      '/api/v1/session/login',
      JSON.stringify({ username, password }),
    );
    expect(login.status, 'login status').toBe(200);
    this.csrf = String(login.data['csrfToken'] ?? '');
    const tenants = await this.call('GET', '/api/v1/tenants');
    expect(tenants.status).toBe(200);
    const items = tenants.data['items'] as { id: string; code: string }[];
    const tenant = items.find((item) => item.code === 'DEMO_A');
    expect(tenant, 'operator demo tenant').toBeDefined();
    this.tenantId = tenant!.id;
    const switched = await this.call(
      'POST',
      '/api/v1/session/switch-tenant',
      JSON.stringify({ tenantId: this.tenantId }),
    );
    expect(switched.status).toBe(200);
    this.csrf = String(switched.data['csrfToken'] ?? this.csrf);
  }
  tenant() {
    return this.tenantId;
  }
  async stepUp() {
    const password = process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora';
    const result = await this.call('POST', '/api/v1/session/step-up', JSON.stringify({ password }));
    expect(result.status, 'fresh password step-up').toBe(200);
  }
  async command(intent: Intent) {
    return this.call('PUT', settingsPath, intent.body, intent.etag, intent.key);
  }
  async close() {
    try {
      if (this.csrf) await this.call('POST', '/api/v1/session/logout');
    } finally {
      await this.context.dispose();
    }
  }
}

function settings(reply: Reply): Settings {
  expect(reply.status).toBe(200);
  expect(reply.etag).toMatch(/^"[0-9a-f]{64}"$/);
  return reply.data as Settings;
}
function intent(hold: number, quote: number, etag: string, key: string = randomUUID()): Intent {
  return {
    key,
    etag,
    body: JSON.stringify({
      holdMinutes: hold,
      quoteTtlMinutes: quote,
      reasonCode: 'CORRECT_CONFIGURATION',
    }),
    createdAt: new Date().toISOString(),
  };
}
function expectProblem(reply: Reply, status: number, code: string) {
  expect(reply.status).toBe(status);
  expect(reply.data['code']).toBe(code);
}

test('bounded accommodation setting change replays and restores the configured baseline', async () => {
  test.setTimeout(120_000);
  const fixtureRoot = fileURLToPath(new URL('../../tmp/', import.meta.url));
  const path = resolve(fixturePath);
  const rel = relative(fixtureRoot, path);
  expect(
    rel && !rel.startsWith('..') && !isAbsolute(rel),
    'fixture must be private under repo tmp/',
  ).toBe(true);
  await mkdir(dirname(path), { recursive: true });
  let fixture: Fixture | undefined;
  try {
    fixture = JSON.parse(await readFile(path, 'utf8')) as Fixture;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT')
      throw new Error('Private fixture could not be read', { cause: error });
  }
  const save = async () => {
    expect(fixture).toBeDefined();
    const temporary = `${path}.${randomUUID()}.tmp`;
    await writeFile(temporary, JSON.stringify(fixture), { flag: 'wx', mode: 0o600 });
    await rename(temporary, path);
  };
  const admin = new LocalActor(await request.newContext());
  const reviewer = new LocalActor(await request.newContext());
  try {
    await admin.login('admin.a');
    await reviewer.login('financial.reviewer');
    const first = await admin.call('GET', settingsPath);
    const view = settings(first);
    expect(view.canManage).toBe(true);
    const preflight = databasePreflight(admin.tenant());
    expect(preflight.canonical, 'both stored rows must be canonical configured numeric JSON').toBe(
      true,
    );
    expect(view.holdMinutes.source).toBe('CONFIGURED');
    expect(view.quoteTtlMinutes.source).toBe('CONFIGURED');
    expect(preflight.hold).toBe(view.holdMinutes.effectiveValue);
    expect(preflight.quote).toBe(view.quoteTtlMinutes.effectiveValue);
    const currentlyBaseline =
      view.holdMinutes.effectiveValue === fixture?.baseline.holdMinutes &&
      view.quoteTtlMinutes.effectiveValue === fixture?.baseline.quoteTtlMinutes;
    const previouslyChanged = fixture
      ? (JSON.parse(fixture.change.body) as { holdMinutes: number; quoteTtlMinutes: number })
      : undefined;
    const currentlyChanged =
      view.holdMinutes.effectiveValue === previouslyChanged?.holdMinutes &&
      view.quoteTtlMinutes.effectiveValue === previouslyChanged?.quoteTtlMinutes;
    if (fixture && !currentlyBaseline && !currentlyChanged)
      throw new Error('Journaled settings differ from both baseline and pending change');
    if (!fixture || (!fixture.complete && currentlyBaseline))
      expect(
        preflight.pending,
        'no existing HOLD/PENDING_APPROVAL booking before timing change',
      ).toBe(0);
    if (!fixture) {
      const hold =
        view.holdMinutes.effectiveValue === 1440 ? 1439 : view.holdMinutes.effectiveValue + 1;
      fixture = {
        version: 1,
        base,
        tenantId: admin.tenant(),
        baseline: {
          holdMinutes: view.holdMinutes.effectiveValue,
          quoteTtlMinutes: view.quoteTtlMinutes.effectiveValue,
          etag: first.etag,
        },
        bookingFingerprint: preflight.bookingFingerprint,
        change: intent(hold, view.quoteTtlMinutes.effectiveValue, first.etag),
      };
      await save(); // Durable exact key/body/ETag before the first business command.
    }
    expect(fixture.version).toBe(1);
    expect(fixture.base).toBe(base);
    expect(fixture.tenantId).toBe(admin.tenant());
    if (!currentlyChanged) expect(preflight.bookingFingerprint).toBe(fixture.bookingFingerprint);
    if (fixture.complete) {
      expect(view.holdMinutes.effectiveValue).toBe(fixture.baseline.holdMinutes);
      expect(view.quoteTtlMinutes.effectiveValue).toBe(fixture.baseline.quoteTtlMinutes);
      return;
    }
    await admin.stepUp();
    const changed = await admin.command(fixture.change);
    const changedView = settings(changed);
    const requested = JSON.parse(fixture.change.body) as {
      holdMinutes: number;
      quoteTtlMinutes: number;
    };
    expect(changedView.holdMinutes.effectiveValue).toBe(requested.holdMinutes);
    expect(changedView.quoteTtlMinutes.effectiveValue).toBe(requested.quoteTtlMinutes);
    if (fixture.change.response) {
      expect(changed.replayed).toBe(true);
      expect(changed.etag).toBe(fixture.change.response.etag);
      expect(changedView).toEqual(fixture.change.response.data);
    } else {
      fixture.change.response = { etag: changed.etag, data: changedView };
      await save();
    }
    const replay = await admin.command(fixture.change);
    expect(replay.replayed).toBe(true);
    expect(replay.etag).toBe(changed.etag);
    expect(settings(replay)).toEqual(changedView);
    fixture.replayVerified = true;
    await save();

    // Restore promptly; an interrupted run resumes the exact recorded command.
    if (!fixture.restore) {
      fixture.restore = intent(
        fixture.baseline.holdMinutes,
        fixture.baseline.quoteTtlMinutes,
        changed.etag,
      );
      await save();
    }
    const restored = await admin.command(fixture.restore);
    const restoredView = settings(restored);
    expect(restoredView.holdMinutes.effectiveValue).toBe(fixture.baseline.holdMinutes);
    expect(restoredView.quoteTtlMinutes.effectiveValue).toBe(fixture.baseline.quoteTtlMinutes);
    if (fixture.restore.response) {
      expect(restored.replayed).toBe(true);
      expect(restored.etag).toBe(fixture.restore.response.etag);
    } else {
      fixture.restore.response = { etag: restored.etag, data: restoredView };
      await save();
    }
    const afterRestore = databasePreflight(admin.tenant());
    expect(afterRestore.canonical).toBe(true);
    expect(afterRestore.hold).toBe(fixture.baseline.holdMinutes);
    expect(afterRestore.quote).toBe(fixture.baseline.quoteTtlMinutes);
    expect(afterRestore.pending, 'no HOLD/PENDING_APPROVAL booking during timing change').toBe(0);
    expect(afterRestore.bookingFingerprint).toBe(fixture.bookingFingerprint);

    fixture.negative ??= {};
    const negatives = fixture.negative;
    if (!negatives['stale']) {
      negatives['stale'] = intent(
        fixture.baseline.holdMinutes,
        fixture.baseline.quoteTtlMinutes,
        fixture.baseline.etag,
      );
      await save();
    }
    expectProblem(await admin.command(negatives['stale']!), 412, 'ETAG_MISMATCH');
    if (!negatives['changedBody']) {
      negatives['changedBody'] = intent(
        fixture.baseline.holdMinutes,
        fixture.baseline.quoteTtlMinutes,
        fixture.change.etag,
        fixture.change.key,
      );
      await save();
    }
    expectProblem(await admin.command(negatives['changedBody']!), 409, 'IDEMPOTENCY_KEY_REUSED');
    if (!negatives['reviewer']) {
      negatives['reviewer'] = intent(
        fixture.baseline.holdMinutes,
        fixture.baseline.quoteTtlMinutes,
        restored.etag,
      );
      await save();
    }
    expectProblem(await reviewer.command(negatives['reviewer']!), 403, 'PERMISSION_DENIED');
    const final = await admin.call('GET', settingsPath);
    expect(settings(final)).toEqual(restoredView);
    expect(final.etag).toBe(restored.etag);
    fixture.complete = true;
    await save();
  } finally {
    await Promise.allSettled([admin.close(), reviewer.close()]);
  }
});
