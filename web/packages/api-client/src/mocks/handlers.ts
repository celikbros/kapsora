/**
 * MSW handlers for every operation in the contract. They emulate the Go API closely
 * enough that the screens behave the same with `VITE_API_MOCK=false`: same problem codes,
 * same header rules (tenant, CSRF, idempotency, If-Match), same masking and dedup.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';
import type { components } from '../generated/kapsora-v1';
import { isValidTCKN, isValidVKN, normalizeDigits } from '../identifiers';
import {
  buildWorld,
  toOrganization,
  toOrganizationSummary,
  toPerson,
  toPersonSummary,
  type MockAccount,
  type MockWorld,
  type StoredOrganization,
  type StoredRelationship,
} from './data';

// Handlers match any origin so the same list serves the browser worker and the node server.
const ANY = '*';

type Schemas = components['schemas'];
type Problem = Schemas['Problem'];
type FieldError = NonNullable<Problem['errors']>[number];

/** Session of the mock; one browser tab = one session (memory only, like the real cookie). */
interface MockSession {
  account: MockAccount;
  activeTenantId: string | null;
  csrfToken: string;
  expiresAt: string;
  stepUpExpiresAt: string | null;
}

export interface MockOptions {
  seed?: number;
  organizationsPerTenant?: number;
  /** Simulated latency in milliseconds (0 in tests). */
  delayMs?: number;
  /** Start signed in as this user (handy for Storybook-like demos and tests). */
  initialUser?: string;
}

/** Everything the handlers share; exported so tests can reset or inspect it. */
export class MockApi {
  world: MockWorld;
  session: MockSession | null = null;
  private idempotency = new Map<string, { status: number; body: unknown; etag: string | null }>();
  private traceCounter = 0;
  readonly delayMs: number;

  constructor(private readonly options: MockOptions = {}) {
    this.world = buildWorld(options);
    this.delayMs = options.delayMs ?? 0;
    if (options.initialUser) {
      this.signIn(options.initialUser);
    }
  }

  reset(): void {
    this.world = buildWorld(this.options);
    this.session = null;
    this.idempotency.clear();
    if (this.options.initialUser) {
      this.signIn(this.options.initialUser);
    }
  }

  traceId(): string {
    this.traceCounter += 1;
    return `mock-${String(this.traceCounter).padStart(6, '0')}`;
  }

  signIn(username: string): MockSession | null {
    const account = this.world.accounts.find((a) => a.username === username);
    if (!account) return null;
    const memberships = account.memberships;
    const single = memberships.length === 1 ? this.tenantByCode(memberships[0]!.tenantCode) : null;
    this.session = {
      account,
      activeTenantId: single?.id ?? null,
      csrfToken: `csrf-${Math.random().toString(36).slice(2)}${Math.random().toString(36).slice(2)}`,
      expiresAt: new Date(Date.now() + 8 * 3_600_000).toISOString(),
      stepUpExpiresAt: null,
    };
    return this.session;
  }

  tenantByCode(code: string) {
    return this.world.tenants.find((t) => t.code === code);
  }

  tenantContexts(account: MockAccount): Schemas['TenantContext'][] {
    return account.memberships
      .map((m) => ({
        tenant: this.tenantByCode(m.tenantCode)!,
        permissions: m.permissions,
        scopes: [],
      }))
      .sort((a, b) => a.tenant.displayName.localeCompare(b.tenant.displayName, 'tr'));
  }

  sessionInfo(): Schemas['SessionInfo'] {
    const s = this.session!;
    return {
      actorId: s.account.actorId,
      displayName: s.account.displayName,
      activeTenantId: s.activeTenantId,
      csrfToken: s.csrfToken,
      expiresAt: s.expiresAt,
      stepUpExpiresAt: s.stepUpExpiresAt,
      mustChangePassword: false,
    };
  }

  rememberIdempotent(key: string, status: number, body: unknown, etag: string | null): void {
    this.idempotency.set(key, { status, body, etag });
  }

  replay(key: string) {
    return this.idempotency.get(key);
  }
}

const PROBLEM_BASE = 'https://errors.kapsora.example/';

function problem(
  api: MockApi,
  status: number,
  code: string,
  title: string,
  extra: { detail?: string; errors?: FieldError[]; instance?: string } = {},
): Response {
  const body: Problem = {
    type: `${PROBLEM_BASE}${code.toLowerCase().replace(/_/g, '-')}`,
    title,
    status,
    code,
    traceId: api.traceId(),
    ...(extra.detail ? { detail: extra.detail } : {}),
    ...(extra.errors ? { errors: extra.errors } : {}),
    ...(extra.instance ? { instance: extra.instance } : {}),
  };
  return HttpResponse.json(body, {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

function unauthenticated(api: MockApi) {
  return problem(api, 401, 'UNAUTHENTICATED', 'Oturum bulunamadı');
}

type Guarded = { session: MockSession; tenantId: string } | { error: Response };

/** Applies the same checks the Go middleware chain does, in the same order. */
function guardTenant(
  api: MockApi,
  request: Request,
  permission: string,
  mutation: boolean,
): Guarded {
  const session = api.session;
  if (!session) return { error: unauthenticated(api) };
  if (mutation && request.headers.get('X-CSRF-Token') !== session.csrfToken) {
    return { error: problem(api, 403, 'CSRF_TOKEN_INVALID', 'CSRF doğrulaması başarısız') };
  }
  const header = request.headers.get('X-Tenant-ID');
  if (!header || !/^[0-9a-f-]{36}$/i.test(header)) {
    return {
      error: problem(api, 400, 'TENANT_HEADER_REQUIRED', 'Tenant başlığı gerekli', {
        detail: 'X-Tenant-ID başlığı geçerli bir UUID olmalıdır.',
      }),
    };
  }
  if (!session.activeTenantId) {
    return { error: problem(api, 403, 'TENANT_ACCESS_DENIED', 'Aktif tenant seçilmedi') };
  }
  if (header !== session.activeTenantId) {
    return { error: problem(api, 403, 'TENANT_MISMATCH', 'Tenant başlığı oturumla uyuşmuyor') };
  }
  const tenant = api.world.tenants.find((t) => t.id === header)!;
  const membership = session.account.memberships.find((m) => m.tenantCode === tenant.code);
  if (!membership?.permissions.includes(permission)) {
    return {
      error: problem(api, 403, 'PERMISSION_DENIED', 'Bu işlem için yetkiniz yok', {
        detail: permission,
      }),
    };
  }
  return { session, tenantId: header };
}

function guardSession(api: MockApi, request: Request, mutation: boolean): MockSession | Response {
  const session = api.session;
  if (!session) return unauthenticated(api);
  if (mutation && request.headers.get('X-CSRF-Token') !== session.csrfToken) {
    return problem(api, 403, 'CSRF_TOKEN_INVALID', 'CSRF doğrulaması başarısız');
  }
  return session;
}

async function readJson<T>(request: Request): Promise<T | null> {
  try {
    return (await request.json()) as T;
  } catch {
    return null;
  }
}

function encodeCursor(offset: number): string {
  return btoa(`kapsora-mock:${offset}`).replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_');
}

function decodeCursor(raw: string | null): number | null {
  if (!raw) return 0;
  try {
    const text = atob(raw.replace(/-/g, '+').replace(/_/g, '/'));
    const m = /^kapsora-mock:(\d+)$/.exec(text);
    return m ? Number(m[1]) : null;
  } catch {
    return null;
  }
}

function parseLimit(url: URL): number | 'invalid' {
  const raw = url.searchParams.get('limit');
  if (raw === null || raw === '') return 50;
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 1) return 'invalid';
  return Math.min(n, 200);
}

async function wait(api: MockApi): Promise<void> {
  if (api.delayMs > 0) {
    await new Promise((r) => setTimeout(r, api.delayMs));
  }
}

function etagOf(version: number): string {
  return `"${version}"`;
}

function parseIfMatch(raw: string | null): number | null {
  if (!raw) return null;
  const m = /^(?:W\/)?"(\d+)"$/.exec(raw.trim());
  return m ? Number(m[1]) : null;
}

const ORGANIZATION_KINDS = new Set([
  'BANK',
  'INSURER',
  'SPONSOR',
  'PROVIDER',
  'VENDOR',
  'PUBLIC_BODY',
  'OTHER',
]);
const RELATIONSHIP_ROLES = new Set(['PAYER', 'SPONSOR', 'PROVIDER', 'VENDOR', 'PARTNER']);
const IDENTIFIER_TYPES = new Set(['VKN', 'TCKN', 'MERSIS', 'PROVIDER_REGISTRY', 'OTHER']);
const TENANT_CODE = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$/;

function validateCreate(body: Schemas['CreateOrganizationRequest']): FieldError[] {
  const errors: FieldError[] = [];
  const len = (s: unknown) => (typeof s === 'string' ? [...s].length : 0);
  if (len(body.legalName) < 2 || len(body.legalName) > 300)
    errors.push({ field: 'legalName', code: 'LENGTH', message: '2-300 karakter olmalı' });
  if (len(body.displayName) < 2 || len(body.displayName) > 200)
    errors.push({ field: 'displayName', code: 'LENGTH', message: '2-200 karakter olmalı' });
  if (!ORGANIZATION_KINDS.has(body.organizationKind))
    errors.push({ field: 'organizationKind', code: 'ENUM', message: 'geçersiz kurum türü' });
  if (!RELATIONSHIP_ROLES.has(body.relationshipRole))
    errors.push({ field: 'relationshipRole', code: 'ENUM', message: 'geçersiz ilişki rolü' });
  const country = body.countryCode ?? 'TR';
  if (!/^[A-Z]{2}$/.test(country))
    errors.push({ field: 'countryCode', code: 'FORMAT', message: 'iki harfli ülke kodu olmalı' });
  if (body.tenantCode !== undefined && !TENANT_CODE.test(body.tenantCode))
    errors.push({
      field: 'tenantCode',
      code: 'FORMAT',
      message: "harf, rakam, '.', '_' ve '-' ile 1-80 karakter olmalı",
    });
  const ids = Array.isArray(body.identifiers) ? body.identifiers : [];
  if (ids.length === 0 || ids.length > 10)
    errors.push({ field: 'identifiers', code: 'LENGTH', message: '1-10 tanımlayıcı verilmeli' });
  let tax = 0;
  ids.forEach((id, i) => {
    if (!IDENTIFIER_TYPES.has(id.type)) {
      errors.push({
        field: `identifiers[${i}].type`,
        code: 'ENUM',
        message: 'geçersiz tanımlayıcı türü',
      });
      return;
    }
    if (id.type === 'VKN' || id.type === 'TCKN') tax += 1;
    const ok =
      id.type === 'VKN'
        ? isValidVKN(id.value)
        : id.type === 'TCKN'
          ? isValidTCKN(id.value)
          : id.value.trim().length > 0 && id.value.length <= 80;
    if (!ok)
      errors.push({
        field: `identifiers[${i}].value`,
        code: 'IDENTIFIER_INVALID',
        message: 'tanımlayıcı doğrulanamadı',
      });
  });
  if (tax > 1)
    errors.push({
      field: 'identifiers',
      code: 'IDENTIFIER_CONFLICT',
      message: 'yalnız bir VKN veya TCKN verilebilir',
    });
  else if (tax === 0 && country === 'TR' && ids.length > 0)
    errors.push({
      field: 'identifiers',
      code: 'IDENTIFIER_REQUIRED',
      message: 'Türkiye için VKN veya TCKN zorunludur',
    });
  return errors;
}

/** Builds the handler list bound to one MockApi instance. */
export function createHandlers(api: MockApi): HttpHandler[] {
  const world = () => api.world;

  const health = () =>
    HttpResponse.json({
      status: 'UP',
      timestamp: new Date().toISOString(),
      checks: { database: 'UP', mock: 'UP' },
    } satisfies Schemas['HealthStatus']);

  const organizationHandlers: HttpHandler[] = [
    http.get(`${ANY}/api/v1/organizations`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'organization.read', false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'limit', code: 'FORMAT', message: '1-200 arası tam sayı olmalı' }],
        });
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const role = url.searchParams.get('role');
      const q = (url.searchParams.get('q') ?? '').trim().toLocaleLowerCase('tr');
      const rows = world()
        .relationships.filter((r) => r.tenantId === g.tenantId)
        .filter((r) => !role || r.relationshipRole === role)
        .map((r) => ({ rel: r, org: world().organizations.get(r.organizationId)! }))
        .filter(
          ({ rel, org }) =>
            !q ||
            org.displayName.toLocaleLowerCase('tr').includes(q) ||
            (rel.tenantCode ?? '').toLocaleLowerCase('tr').includes(q),
        )
        .sort((a, b) =>
          a.rel.createdAt < b.rel.createdAt
            ? 1
            : a.rel.createdAt > b.rel.createdAt
              ? -1
              : a.rel.id < b.rel.id
                ? 1
                : -1,
        );
      const page = rows.slice(offset, offset + limit);
      const body: Schemas['OrganizationPage'] = {
        items: page.map(({ rel, org }) => toOrganizationSummary(rel, org)),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body);
    }),

    http.post(`${ANY}/api/v1/organizations`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'organization.manage', true);
      if ('error' in g) return g.error;
      const key = request.headers.get('Idempotency-Key');
      if (!key)
        return problem(api, 400, 'IDEMPOTENCY_KEY_REQUIRED', 'Idempotency-Key başlığı gerekli');
      const replay = api.replay(`${g.tenantId}:${key}`);
      if (replay) {
        return HttpResponse.json(replay.body as Schemas['Organization'], {
          status: replay.status,
          headers: replay.etag ? { ETag: replay.etag } : {},
        });
      }
      const body = await readJson<Schemas['CreateOrganizationRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors = validateCreate(body);
      if (errors.length > 0)
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });

      const tax = body.identifiers.find((i) => i.type === 'VKN' || i.type === 'TCKN')!;
      const taxValue = normalizeDigits(tax.value);
      let org = [...world().organizations.values()].find((o) => o.taxNumber?.value === taxValue);
      if (!org) {
        org = {
          organizationId: world().nextId(),
          legalName: body.legalName,
          displayName: body.displayName,
          organizationKind: body.organizationKind,
          countryCode: body.countryCode ?? 'TR',
          organizationStatus: 'ACTIVE',
          taxNumber: { type: tax.type as 'VKN' | 'TCKN', value: taxValue },
          otherIdentifiers: [],
        };
        world().organizations.set(org.organizationId, org);
      }
      for (const id of body.identifiers) {
        if (id.type === 'VKN' || id.type === 'TCKN') continue;
        const owner = [...world().organizations.values()].find((o) =>
          o.otherIdentifiers.some((x) => x.type === id.type && x.value === id.value),
        );
        if (owner && owner.organizationId !== org.organizationId) {
          return problem(
            api,
            409,
            'ORGANIZATION_IDENTIFIER_TAKEN',
            'Tanımlayıcı başka bir kuruma kayıtlı',
          );
        }
        if (!owner)
          org.otherIdentifiers.push({
            type: id.type as 'MERSIS',
            value: id.value,
            primary: id.primary ?? false,
          });
      }
      if (
        world().relationships.some(
          (r) =>
            r.tenantId === g.tenantId &&
            r.organizationId === org!.organizationId &&
            r.relationshipRole === body.relationshipRole &&
            r.relationshipStatus !== 'TERMINATED',
        )
      ) {
        return problem(
          api,
          409,
          'ORGANIZATION_RELATIONSHIP_EXISTS',
          'Bu kurumla bu rolde ilişki zaten var',
        );
      }
      if (
        body.tenantCode &&
        world().relationships.some(
          (r) => r.tenantId === g.tenantId && r.tenantCode === body.tenantCode,
        )
      ) {
        return problem(api, 409, 'TENANT_CODE_TAKEN', 'Bu kurum kodu zaten kullanılıyor');
      }
      const now = new Date().toISOString();
      const rel: StoredRelationship = {
        id: world().nextId(),
        tenantId: g.tenantId,
        organizationId: org.organizationId,
        relationshipRole: body.relationshipRole,
        relationshipStatus: 'ACTIVE',
        tenantCode: body.tenantCode ?? null,
        validFrom: now.slice(0, 10),
        validTo: null,
        createdAt: now,
        rowVersion: 1,
      };
      world().relationships.push(rel);
      const out = toOrganization(rel, org);
      api.rememberIdempotent(`${g.tenantId}:${key}`, 201, out, etagOf(1));
      return HttpResponse.json(out, {
        status: 201,
        headers: { ETag: etagOf(1), Location: `/api/v1/organizations/${rel.id}` },
      });
    }),

    http.get(`${ANY}/api/v1/organizations/:organizationId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'organization.read', false);
      if ('error' in g) return g.error;
      const rel = world().relationships.find(
        (r) => r.id === params['organizationId'] && r.tenantId === g.tenantId,
      );
      if (!rel) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      return HttpResponse.json(
        toOrganization(rel, world().organizations.get(rel.organizationId)!),
        { headers: { ETag: etagOf(rel.rowVersion) } },
      );
    }),

    http.patch(`${ANY}/api/v1/organizations/:organizationId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'organization.manage', true);
      if ('error' in g) return g.error;
      const ct = request.headers.get('Content-Type') ?? '';
      if (!ct.toLowerCase().startsWith('application/merge-patch+json')) {
        return problem(
          api,
          415,
          'UNSUPPORTED_MEDIA_TYPE',
          'Content-Type application/merge-patch+json olmalı',
        );
      }
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null)
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      const rel = world().relationships.find(
        (r) => r.id === params['organizationId'] && r.tenantId === g.tenantId,
      );
      if (!rel) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const patch = await readJson<Record<string, unknown>>(request);
      if (!patch || typeof patch !== 'object')
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors: FieldError[] = [];
      for (const [k, v] of Object.entries(patch)) {
        if (k === 'displayName') {
          if (typeof v !== 'string' || [...v].length < 2 || [...v].length > 200)
            errors.push({ field: k, code: 'LENGTH', message: '2-200 karakter olmalı' });
        } else if (k === 'relationshipStatus') {
          if (v !== 'ACTIVE' && v !== 'SUSPENDED' && v !== 'TERMINATED')
            errors.push({
              field: k,
              code: 'ENUM',
              message: 'ACTIVE, SUSPENDED veya TERMINATED olmalı',
            });
        } else if (k === 'tenantCode') {
          if (v !== null && (typeof v !== 'string' || !TENANT_CODE.test(v)))
            errors.push({
              field: k,
              code: 'FORMAT',
              message: "harf, rakam, '.', '_' ve '-' ile 1-80 karakter olmalı",
            });
        } else {
          errors.push({ field: k, code: 'UNKNOWN_FIELD', message: 'bilinmeyen alan' });
        }
      }
      if (errors.length > 0)
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      if (rel.rowVersion !== expected) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti', {
          detail: 'Güncel sürümü alıp değişikliğinizi yeniden uygulayın.',
        });
      }
      const org = world().organizations.get(rel.organizationId)!;
      if (typeof patch['displayName'] === 'string' && patch['displayName'] !== org.displayName) {
        const shared = world().relationships.filter(
          (r) => r.organizationId === org.organizationId && r.relationshipStatus !== 'TERMINATED',
        ).length;
        if (shared > 1)
          return problem(
            api,
            409,
            'ORGANIZATION_SHARED_READONLY',
            'Kurum birden fazla tenant ile paylaşılıyor; adı burada değiştirilemez',
          );
        org.displayName = patch['displayName'];
      }
      if (typeof patch['relationshipStatus'] === 'string')
        rel.relationshipStatus = patch[
          'relationshipStatus'
        ] as StoredRelationship['relationshipStatus'];
      if ('tenantCode' in patch) {
        const code = patch['tenantCode'] as string | null;
        if (
          code &&
          world().relationships.some(
            (r) => r.id !== rel.id && r.tenantId === g.tenantId && r.tenantCode === code,
          )
        ) {
          return problem(api, 409, 'TENANT_CODE_TAKEN', 'Bu kurum kodu zaten kullanılıyor');
        }
        rel.tenantCode = code;
      }
      rel.rowVersion += 1;
      return HttpResponse.json(toOrganization(rel, org), {
        headers: { ETag: etagOf(rel.rowVersion) },
      });
    }),
  ];

  const sessionHandlers: HttpHandler[] = [
    http.get(`${ANY}/health/live`, health),
    http.get(`${ANY}/health/ready`, health),

    http.get(`${ANY}/api/v1/session`, async () => {
      await wait(api);
      if (!api.session) return unauthenticated(api);
      return HttpResponse.json(api.sessionInfo());
    }),

    http.post(`${ANY}/api/v1/session/login`, async ({ request }) => {
      await wait(api);
      const body = await readJson<{ username?: string; password?: string }>(request);
      if (!body || typeof body.username !== 'string' || typeof body.password !== 'string') {
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      }
      // Any password of 12+ characters signs in a known demo user; unknown users and
      // short passwords answer like the real API (coarse 401).
      if (body.password.length < 12 || body.username === 'locked.user') {
        return body.username === 'locked.user'
          ? problem(api, 403, 'ACCOUNT_LOCKED', 'Hesap geçici olarak kilitlendi')
          : problem(api, 401, 'INVALID_CREDENTIALS', 'Kullanıcı adı veya parola hatalı');
      }
      const session = api.signIn(body.username.trim().toLowerCase());
      if (!session)
        return problem(api, 401, 'INVALID_CREDENTIALS', 'Kullanıcı adı veya parola hatalı');
      return HttpResponse.json(api.sessionInfo());
    }),

    http.post(`${ANY}/api/v1/session/logout`, async ({ request }) => {
      await wait(api);
      const s = guardSession(api, request, true);
      if (s instanceof Response) return s;
      api.session = null;
      return new HttpResponse(null, { status: 204 });
    }),

    http.post(`${ANY}/api/v1/session/switch-tenant`, async ({ request }) => {
      await wait(api);
      const s = guardSession(api, request, true);
      if (s instanceof Response) return s;
      const body = await readJson<{ tenantId?: string }>(request);
      const tenant = api.world.tenants.find((t) => t.id === body?.tenantId);
      const membership = tenant && s.account.memberships.find((m) => m.tenantCode === tenant.code);
      if (!tenant || !membership)
        return problem(api, 403, 'TENANT_ACCESS_DENIED', 'Bu tenant için üyeliğiniz yok');
      s.activeTenantId = tenant.id;
      const ctx: Schemas['TenantContext'] = {
        tenant,
        permissions: membership.permissions,
        scopes: [],
      };
      return HttpResponse.json(ctx);
    }),

    http.post(`${ANY}/api/v1/session/step-up`, async ({ request }) => {
      await wait(api);
      const s = guardSession(api, request, true);
      if (s instanceof Response) return s;
      const body = await readJson<{ password?: string }>(request);
      if (!body?.password || body.password.length < 12)
        return problem(api, 401, 'INVALID_CREDENTIALS', 'Parola hatalı');
      s.stepUpExpiresAt = new Date(Date.now() + 10 * 60_000).toISOString();
      return HttpResponse.json({ stepUpExpiresAt: s.stepUpExpiresAt });
    }),

    http.post(`${ANY}/api/v1/session/password`, async ({ request }) => {
      await wait(api);
      const s = guardSession(api, request, true);
      if (s instanceof Response) return s;
      const body = await readJson<{ currentPassword?: string; newPassword?: string }>(request);
      if (!body?.newPassword || body.newPassword.length < 12) {
        return problem(api, 422, 'PASSWORD_POLICY_VIOLATION', 'Parola politikaya uymuyor', {
          errors: [{ field: 'newPassword', code: 'LENGTH', message: 'en az 12 karakter' }],
        });
      }
      return new HttpResponse(null, { status: 204 });
    }),

    http.get(`${ANY}/api/v1/me`, async () => {
      await wait(api);
      if (!api.session) return unauthenticated(api);
      const a = api.session.account;
      const body: Schemas['UserContext'] = {
        actorId: a.actorId,
        displayName: a.displayName,
        email: a.email,
        tenants: api.tenantContexts(a),
      };
      return HttpResponse.json(body);
    }),

    http.get(`${ANY}/api/v1/tenants`, async () => {
      await wait(api);
      if (!api.session) return unauthenticated(api);
      return HttpResponse.json({
        items: api.tenantContexts(api.session.account).map((c) => c.tenant),
      });
    }),
  ];

  const peopleHandlers: HttpHandler[] = [
    http.get(`${ANY}/api/v1/people`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'person.read', false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid')
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'limit', code: 'FORMAT' }],
        });
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const q = (url.searchParams.get('q') ?? '').trim().toLocaleLowerCase('tr');
      const rows = world()
        .people.filter((p) => p.tenantId === g.tenantId)
        .map(toPersonSummary)
        .filter((p) => !q || p.displayName.toLocaleLowerCase('tr').includes(q));
      const body: Schemas['PersonPage'] = {
        items: rows.slice(offset, offset + limit),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body);
    }),
    http.post(`${ANY}/api/v1/people`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'person.manage', true);
      if ('error' in g) return g.error;
      if (!request.headers.get('Idempotency-Key'))
        return problem(api, 400, 'IDEMPOTENCY_KEY_REQUIRED', 'Idempotency-Key başlığı gerekli');
      const body = await readJson<Schemas['CreatePersonRequest']>(request);
      if (!body?.firstName || !body.lastName)
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'firstName', code: 'REQUIRED' }],
        });
      const now = new Date().toISOString();
      const person = {
        id: world().nextId(),
        tenantId: g.tenantId,
        firstName: body.firstName,
        middleName: body.middleName ?? null,
        lastName: body.lastName,
        birthDate: body.birthDate ?? null,
        sexAtBirth: body.sexAtBirth ?? null,
        status: 'ACTIVE' as const,
        identifiers: (body.identifiers ?? []).map((i) => ({
          type: i.type,
          value: i.value,
          primary: i.primary ?? false,
        })),
        rowVersion: 1,
        createdAt: now,
      };
      world().people.push(person);
      return HttpResponse.json(toPerson(person), { status: 201, headers: { ETag: etagOf(1) } });
    }),
    http.get(`${ANY}/api/v1/people/:personId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'person.read', false);
      if ('error' in g) return g.error;
      const p = world().people.find(
        (x) => x.id === params['personId'] && x.tenantId === g.tenantId,
      );
      if (!p) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      return HttpResponse.json(toPerson(p), { headers: { ETag: etagOf(p.rowVersion) } });
    }),
  ];

  const eligibilityHandlers: HttpHandler[] = [
    http.post(`${ANY}/api/v1/eligibility/checks`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'eligibility.check', true);
      if ('error' in g) return g.error;
      const body = await readJson<Schemas['EligibilityCheckRequest']>(request);
      if (!body?.personId || !body.serviceDate)
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'personId', code: 'REQUIRED' }],
        });
      const person = world().people.find(
        (p) => p.id === body.personId && p.tenantId === g.tenantId,
      );
      const result: Schemas['EligibilityCheckResult'] = person
        ? {
            eligible: true,
            outcome: 'ELIGIBLE',
            evaluatedAt: new Date().toISOString(),
            explanations: [
              { code: 'ENROLLMENT_ACTIVE', message: 'Aktif kayıt bulundu', severity: 'INFO' },
            ],
            balances: [{ entitlementCode: 'PHYSIO_SESSION', available: 8, unit: 'SESSION' }],
            planVersionId: null,
            ruleSetVersionIds: [],
          }
        : {
            eligible: false,
            outcome: 'MISSING_DATA',
            evaluatedAt: new Date().toISOString(),
            explanations: [
              { code: 'PERSON_NOT_FOUND', message: 'Hak sahibi bulunamadı', severity: 'ERROR' },
            ],
          };
      return HttpResponse.json(result);
    }),
  ];

  const findRequest = (id: string | readonly string[] | undefined, tenantId: string) =>
    world().serviceRequests.find((r) => r.id === id && r.tenantId === tenantId);

  const serviceRequestHandlers: HttpHandler[] = [
    http.get(`${ANY}/api/v1/service-requests`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'service_request.read', false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid')
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'limit', code: 'FORMAT' }],
        });
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const status = url.searchParams.get('status');
      const personId = url.searchParams.get('personId');
      const rows = world().serviceRequests.filter(
        (r) =>
          r.tenantId === g.tenantId &&
          (!status || r.status === status) &&
          (!personId || r.personId === personId),
      );
      const body: Schemas['ServiceRequestPage'] = {
        items: rows.slice(offset, offset + limit).map(({ tenantId: _t, ...r }) => r),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body);
    }),
    http.post(`${ANY}/api/v1/service-requests`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'service_request.manage', true);
      if ('error' in g) return g.error;
      if (!request.headers.get('Idempotency-Key'))
        return problem(api, 400, 'IDEMPOTENCY_KEY_REQUIRED', 'Idempotency-Key başlığı gerekli');
      const body = await readJson<Schemas['CreateServiceRequest']>(request);
      if (!body?.personId || !body.items?.length)
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'items', code: 'LENGTH' }],
        });
      const now = new Date().toISOString();
      const sr = {
        tenantId: g.tenantId,
        id: world().nextId(),
        reference: `SR-2026-${2000 + world().serviceRequests.length}`,
        personId: body.personId,
        programId: body.programId,
        enrollmentId: body.enrollmentId,
        providerOrganizationId: body.providerOrganizationId ?? null,
        requestType: body.requestType,
        channel: body.channel,
        status: 'DRAFT' as const,
        serviceDate: body.serviceDate,
        requestedStartAt: body.requestedStartAt ?? null,
        requestedEndAt: body.requestedEndAt ?? null,
        submittedAt: null,
        createdAt: now,
        rowVersion: 1,
        items: body.items.map((it, i) => ({
          id: world().nextId(),
          lineNo: i + 1,
          serviceDefinitionId: it.serviceDefinitionId,
          unitType: it.unitType,
          requestedQuantity: it.requestedQuantity,
          requestedAmount: it.requestedAmount ?? null,
          currencyCode: it.currencyCode ?? null,
          status: 'REQUESTED' as const,
          approvedQuantity: null,
          approvedAmount: null,
          decisionReasonCode: null,
        })),
      };
      world().serviceRequests.push(sr);
      const { tenantId: _t, ...out } = sr;
      return HttpResponse.json(out, { status: 201, headers: { ETag: etagOf(1) } });
    }),
    http.get(`${ANY}/api/v1/service-requests/:requestId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'service_request.read', false);
      if ('error' in g) return g.error;
      const sr = findRequest(params['requestId'], g.tenantId);
      if (!sr) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const { tenantId: _t, ...out } = sr;
      return HttpResponse.json(out, { headers: { ETag: etagOf(sr.rowVersion) } });
    }),
    http.patch(`${ANY}/api/v1/service-requests/:requestId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'service_request.manage', true);
      if ('error' in g) return g.error;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null)
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      const sr = findRequest(params['requestId'], g.tenantId);
      if (!sr) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      if (sr.rowVersion !== expected)
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      if (sr.status !== 'DRAFT')
        return problem(api, 409, 'SERVICE_REQUEST_NOT_DRAFT', 'Yalnız taslaklar düzenlenebilir');
      const patch = (await readJson<Schemas['UpdateServiceRequest']>(request)) ?? {};
      if (patch.serviceDate) sr.serviceDate = patch.serviceDate;
      if ('providerOrganizationId' in patch)
        sr.providerOrganizationId = patch.providerOrganizationId ?? null;
      sr.rowVersion += 1;
      const { tenantId: _t, ...out } = sr;
      return HttpResponse.json(out, { headers: { ETag: etagOf(sr.rowVersion) } });
    }),
    ...(['submit', 'cancel'] as const).map((action) =>
      http.post(
        `${ANY}/api/v1/service-requests/:requestId/${action}`,
        async ({ request, params }) => {
          await wait(api);
          const g = guardTenant(api, request, 'service_request.manage', true);
          if ('error' in g) return g.error;
          if (!request.headers.get('Idempotency-Key'))
            return problem(api, 400, 'IDEMPOTENCY_KEY_REQUIRED', 'Idempotency-Key başlığı gerekli');
          const expected = parseIfMatch(request.headers.get('If-Match'));
          if (expected === null)
            return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
          const sr = findRequest(params['requestId'], g.tenantId);
          if (!sr) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
          if (sr.rowVersion !== expected)
            return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
          if (action === 'submit') {
            if (sr.status !== 'DRAFT')
              return problem(
                api,
                409,
                'SERVICE_REQUEST_NOT_DRAFT',
                'Yalnız taslaklar gönderilebilir',
              );
            sr.status = 'SUBMITTED';
            sr.submittedAt = new Date().toISOString();
          } else {
            const body = await readJson<Schemas['ReasonCommand']>(request);
            if (!body?.reasonCode)
              return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
                errors: [{ field: 'reasonCode', code: 'REQUIRED' }],
              });
            sr.status = 'CANCELLED';
          }
          sr.rowVersion += 1;
          const { tenantId: _t, ...out } = sr;
          return HttpResponse.json(out, { headers: { ETag: etagOf(sr.rowVersion) } });
        },
      ),
    ),
  ];

  return [
    ...sessionHandlers,
    ...organizationHandlers,
    ...peopleHandlers,
    ...eligibilityHandlers,
    ...serviceRequestHandlers,
  ];
}

export type { StoredOrganization };
