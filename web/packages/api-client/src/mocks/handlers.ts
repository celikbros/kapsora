/**
 * MSW handlers for every operation in the contract. They emulate the Go API closely
 * enough that the screens behave the same with `VITE_API_MOCK=false`: same problem codes,
 * same header rules (tenant, CSRF, idempotency, If-Match), same masking and dedup.
 */
import { HttpResponse, http, type HttpHandler, type PathParams } from 'msw';

import { benefitHandlers } from './benefit-handlers';
import { catalogHandlers } from './catalog-handlers';
import { claimHandlers } from './claim-handlers';
import { contractHandlers } from './contract-handlers';
import { documentHandlers } from './document-handlers';
import { eligibilityHandlers } from './eligibility-handlers';
import { entitlementHandlers } from './entitlement-handlers';
import { healthHandlers } from './health-handlers';
import { importHandlers } from './import-handlers';
import { inpatientHandlers } from './inpatient-handlers';
import { medicalReportHandlers } from './medical-report-handlers';
import { notificationHandlers } from './notification-handlers';
import { pricingHandlers } from './pricing-handlers';
import { providerHandlers } from './provider-handlers';
import { rulesHandlers } from './rules-handlers';
import { serviceRequestHandlers } from './servicerequest-handlers';
import { workflowHandlers } from './workflow-handlers';
import type { components } from '../generated/kapsora-v1';
import { isValidTCKN, isValidVKN, normalizeDigits } from '../identifiers';
import {
  IDENTIFIER_TYPE_CATALOG,
  MEMBERSHIP_TYPE_CATALOG,
  RELATIONSHIP_TYPE_CATALOG,
  buildWorld,
  toOrganization,
  toOrganizationSummary,
  toPerson,
  toPersonContact,
  toPersonRelationship,
  toPersonSummary,
  toSponsorMembership,
  type MockAccount,
  type MockWorld,
  type StoredMembership,
  type StoredOrganization,
  type StoredPersonContact,
  type StoredPersonRelationship,
  type StoredRelationship,
} from './data';

// Handlers match any origin so the same list serves the browser worker and the node server.
export const ANY = '*';

export type Schemas = components['schemas'];
type Problem = Schemas['Problem'];
export type FieldError = NonNullable<Problem['errors']>[number];

/** Session of the mock; one browser tab = one session (memory only, like the real cookie). */
export interface MockSession {
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
        // The access grants that narrow the permissions above. A provider-side role is an
        // ORGANIZATION grant, and it is what every provider boundary in the API is read
        // from — there is no second, client-side rule that could disagree with it.
        scopes: (m.scopes ?? []).map((g) => ({ type: g.type, id: g.id })),
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

export function problem(
  api: MockApi,
  status: number,
  code: string,
  title: string,
  extra: {
    detail?: string;
    errors?: FieldError[];
    instance?: string;
    /**
     * RFC 9457 extension members, serialised flat beside the standard ones exactly as the
     * Go server serialises them (WP-I5-05 section 2.6). A problem type may carry the one
     * fact that makes it actionable — who holds the work item you tried to claim — rather
     * than forcing a second request.
     */
    extensions?: Record<string, unknown>;
  } = {},
): Response {
  const body: Problem & Record<string, unknown> = {
    type: `${PROBLEM_BASE}${code.toLowerCase().replace(/_/g, '-')}`,
    title,
    status,
    code,
    traceId: api.traceId(),
    ...(extra.detail ? { detail: extra.detail } : {}),
    ...(extra.errors ? { errors: extra.errors } : {}),
    ...(extra.instance ? { instance: extra.instance } : {}),
    ...(extra.extensions ?? {}),
  };
  return HttpResponse.json(body, {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

/**
 * Puts a contact value into the one form the platform stores it in, so the same address
 * written two ways is the same address: an e-mail is trimmed and lower-cased, a telephone
 * number keeps a leading + and loses the typography.
 */
export function normalizeContact(channel: string, raw: string): string {
  const value = (raw ?? '').trim();
  if (channel === 'EMAIL') return value.toLowerCase();
  if (channel !== 'SMS') return value;
  const plus = value.startsWith('+') ? '+' : '';
  return plus + value.replace(/[^0-9]/g, '');
}

/**
 * Refuses the value nobody could ever send to, and nothing more. An address is proved by
 * sending to it, which is M10's onboarding; refusing more here would refuse real ones.
 */
export function validContact(channel: string, value: string): boolean {
  if (channel === 'EMAIL') {
    const at = value.lastIndexOf('@');
    if (at <= 0 || value.length > 254) return false;
    const host = value.slice(at + 1);
    return (
      !/[\s,;"<>()[\]\\]/.test(value) &&
      host.includes('.') &&
      !host.startsWith('.') &&
      !host.endsWith('.')
    );
  }
  if (channel !== 'SMS') return false;
  const digits = value.startsWith('+') ? value.slice(1) : value;
  return /^[0-9]{7,15}$/.test(digits);
}

export function unauthenticated(api: MockApi) {
  return problem(api, 401, 'UNAUTHENTICATED', 'Oturum bulunamadı');
}

export type Guarded = { session: MockSession; tenantId: string } | { error: Response };

/** Applies the same checks the Go middleware chain does, in the same order. */
export function guardTenant(
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

export function guardSession(
  api: MockApi,
  request: Request,
  mutation: boolean,
): MockSession | Response {
  const session = api.session;
  if (!session) return unauthenticated(api);
  if (mutation && request.headers.get('X-CSRF-Token') !== session.csrfToken) {
    return problem(api, 403, 'CSRF_TOKEN_INVALID', 'CSRF doğrulaması başarısız');
  }
  return session;
}

// pathParam narrows one MSW path parameter to a string: the router types repeated
// segments as an array, which none of these routes has.
export function pathParam(params: PathParams, name: string): string {
  const value = params[name];
  return Array.isArray(value) ? (value[0] ?? '') : ((value as string | undefined) ?? '');
}

export async function readJson<T>(request: Request): Promise<T | null> {
  try {
    return (await request.json()) as T;
  } catch {
    return null;
  }
}

export function encodeCursor(offset: number): string {
  return btoa(`kapsora-mock:${offset}`).replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_');
}

export function decodeCursor(raw: string | null): number | null {
  if (!raw) return 0;
  try {
    const text = atob(raw.replace(/-/g, '+').replace(/_/g, '/'));
    const m = /^kapsora-mock:(\d+)$/.exec(text);
    return m ? Number(m[1]) : null;
  } catch {
    return null;
  }
}

export function parseLimit(url: URL): number | 'invalid' {
  const raw = url.searchParams.get('limit');
  if (raw === null || raw === '') return 50;
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 1) return 'invalid';
  return Math.min(n, 200);
}

export async function wait(api: MockApi): Promise<void> {
  if (api.delayMs > 0) {
    await new Promise((r) => setTimeout(r, api.delayMs));
  }
}

export function etagOf(version: number): string {
  return `"${version}"`;
}

/** The 404 every route answers for a row the caller may not see or that does not exist. */
export function notFound(api: MockApi): Response {
  return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
}

/** Rejects a body that is not a merge-patch document. */
export function requireMergePatch(api: MockApi, request: Request): Response | null {
  const type = (request.headers.get('Content-Type') ?? '').toLowerCase();
  return type.startsWith('application/merge-patch+json')
    ? null
    : problem(
        api,
        415,
        'UNSUPPORTED_MEDIA_TYPE',
        'Content-Type application/merge-patch+json olmalı',
      );
}

/** True when `date` falls inside the half-open period [from, to). */
export function withinPeriod(date: string, from: string | null, to: string | null): boolean {
  if (from !== null && date < from) return false;
  return to === null || date < to;
}

/** Two half-open periods overlap when each starts before the other ends. */
export function periodsOverlap(
  aFrom: string,
  aTo: string | null,
  bFrom: string,
  bTo: string | null,
): boolean {
  return (aTo === null || bFrom < aTo) && (bTo === null || aFrom < bTo);
}

export function parseIfMatch(raw: string | null): number | null {
  if (!raw) return null;
  const m = /^(?:W\/)?"(\d+)"$/.exec(raw.trim());
  return m ? Number(m[1]) : null;
}

/**
 * True when the session carries `permission` in the tenant it is acting for. Used where a
 * route is readable by two grants and answers differently for each — a draft contract
 * version, for instance, is invisible to a caller holding only `contract.read`.
 */
export function hasPermission(
  api: MockApi,
  session: MockSession,
  tenantId: string,
  permission: string,
): boolean {
  const tenant = api.world.tenants.find((t) => t.id === tenantId);
  if (!tenant) return false;
  const membership = session.account.memberships.find((m) => m.tenantCode === tenant.code);
  return membership?.permissions.includes(permission) ?? false;
}

/** True while the session's step-up (password re-entry) is still fresh. */
export function hasStepUp(session: MockSession): boolean {
  return session.stepUpExpiresAt !== null && Date.parse(session.stepUpExpiresAt) > Date.now();
}

export function stepUpRequired(api: MockApi): Response {
  return problem(
    api,
    403,
    'STEP_UP_REQUIRED',
    'Bu işlem için parola ile yeniden doğrulama gerekli',
  );
}

/**
 * The Idempotency-Key check of every route the server wraps in `d.idempotent(...)`. The
 * length bound is the middleware's, not a guess: a key too short to be unique is refused
 * rather than accepted and quietly useless.
 */
export function requireIdempotencyKey(api: MockApi, request: Request): Response | null {
  const key = request.headers.get('Idempotency-Key');
  if (!key) {
    return problem(api, 400, 'IDEMPOTENCY_KEY_REQUIRED', 'Idempotency-Key başlığı zorunludur');
  }
  if (key.length < 16 || key.length > 128) {
    return problem(
      api,
      400,
      'IDEMPOTENCY_KEY_INVALID',
      'Idempotency-Key 16-128 karakter olmalıdır',
    );
  }
  return null;
}

/**
 * The If-Match of a route that has one: 428 when it is absent, and the number it names
 * otherwise. `W/"3"` and `"3"` are both accepted, exactly as the server accepts them.
 */
export function requireIfMatch(api: MockApi, request: Request): number | Response {
  const expected = parseIfMatch(request.headers.get('If-Match'));
  if (expected === null || expected < 1) {
    return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli', {
      detail: 'GET yanıtındaki ETag değerini If-Match olarak gönderin.',
    });
  }
  return expected;
}

/**
 * The caller's provider boundary, read from its ORGANIZATION access grants. `null` means
 * unrestricted, exactly as a nil slice does on the server; an empty array is a grant that
 * names nothing and therefore matches nothing, which is the safe reading of it.
 */
export function organizationScope(
  api: MockApi,
  session: MockSession,
  tenantId: string,
): string[] | null {
  const tenant = api.world.tenants.find((t) => t.id === tenantId);
  if (!tenant) return [];
  const membership = session.account.memberships.find((m) => m.tenantCode === tenant.code);
  const grants = (membership?.scopes ?? []).filter((g) => g.type === 'ORGANIZATION');
  if (grants.length === 0) return null;
  return grants.filter((g) => g.id !== null).map((g) => g.id!);
}

/** True when a row naming `organizationId` is inside the caller's provider boundary. */
export function withinScope(scope: string[] | null, organizationId: string | null): boolean {
  if (scope === null) return true;
  return organizationId !== null && scope.includes(organizationId);
}

/** The 422 every module answers with a list of field errors. */
export function validationFailed(api: MockApi, errors: FieldError[]): Response {
  return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
}

/** Hex SHA-256 of a File/Blob's content, used to dedupe member import uploads. */
export async function sha256Hex(file: File | Blob): Promise<string> {
  const bytes = await crypto.subtle.digest('SHA-256', await file.arrayBuffer());
  return [...new Uint8Array(bytes)].map((b) => b.toString(16).padStart(2, '0')).join('');
}

/** End of the benefit period a definition opens on `validFrom`, per its period type. */
export function benefitPeriodEnd(
  definition: { periodType: string; periodLength: number | null },
  validFrom: string,
): string | null {
  const start = new Date(`${validFrom}T00:00:00.000Z`);
  switch (definition.periodType) {
    case 'CALENDAR_YEAR':
      return `${start.getUTCFullYear() + 1}-01-01`;
    case 'ROLLING_DAYS': {
      const days = definition.periodLength ?? 365;
      const end = new Date(start.getTime() + days * 86_400_000);
      return end.toISOString().slice(0, 10);
    }
    default:
      return null;
  }
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

  /** One person's contact details, masked, in the order a screen shows them. */
  const contactsOf = (tenantId: string, personId: string): Schemas['PersonContact'][] =>
    world()
      .personContacts.filter((c) => c.tenantId === tenantId && c.personId === personId)
      .sort(
        (a, b) =>
          a.channel.localeCompare(b.channel) ||
          Number(b.primary) - Number(a.primary) ||
          a.createdAt.localeCompare(b.createdAt),
      )
      .map(toPersonContact);

  const peopleHandlers: HttpHandler[] = [
    http.get(`${ANY}/api/v1/people`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'member.read', false);
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
      const status = url.searchParams.get('status');
      const sponsorOrganizationId = url.searchParams.get('sponsorOrganizationId');
      const rows = world()
        .people.filter((p) => p.tenantId === g.tenantId)
        .filter((p) => !status || p.status === status)
        .filter(
          (p) =>
            !sponsorOrganizationId ||
            world().memberships.some(
              (m) =>
                m.personId === p.id &&
                m.sponsorOrganizationId === sponsorOrganizationId &&
                m.status === 'ACTIVE',
            ),
        )
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
      const g = guardTenant(api, request, 'member.manage', true);
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
      const g = guardTenant(api, request, 'member.read', false);
      if ('error' in g) return g.error;
      const p = world().people.find(
        (x) => x.id === params['personId'] && x.tenantId === g.tenantId,
      );
      if (!p) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      return HttpResponse.json(toPerson(p), { headers: { ETag: etagOf(p.rowVersion) } });
    }),

    http.patch(`${ANY}/api/v1/people/:personId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'member.manage', true);
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
      const person = world().people.find(
        (x) => x.id === params['personId'] && x.tenantId === g.tenantId,
      );
      if (!person) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const patch = await readJson<Schemas['UpdatePersonRequest']>(request);
      if (!patch || typeof patch !== 'object')
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors: FieldError[] = [];
      if (patch.firstName !== undefined && patch.firstName.trim().length === 0)
        errors.push({ field: 'firstName', code: 'REQUIRED', message: 'Ad zorunlu' });
      if (patch.lastName !== undefined && patch.lastName.trim().length === 0)
        errors.push({ field: 'lastName', code: 'REQUIRED', message: 'Soyad zorunlu' });
      for (const [i, entry] of (patch.identifiers ?? []).entries()) {
        if (entry.remove) continue;
        if (entry.type === 'TCKN' && entry.value !== undefined && !isValidTCKN(entry.value)) {
          errors.push({
            field: `identifiers[${i}].value`,
            code: 'IDENTIFIER_INVALID',
            message: 'TCKN kontrol basamağı hatalı',
          });
        }
      }
      if (errors.length > 0)
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      if (person.rowVersion !== expected) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti', {
          detail: 'Güncel sürümü alıp değişikliğinizi yeniden uygulayın.',
        });
      }
      for (const entry of patch.identifiers ?? []) {
        if (entry.remove) {
          person.identifiers = person.identifiers.filter((i) => i.type !== entry.type);
          continue;
        }
        if (entry.value === undefined) continue;
        if (entry.type === 'TCKN') {
          const taken = world().people.some(
            (other) =>
              other.id !== person.id &&
              other.tenantId === g.tenantId &&
              other.identifiers.some(
                (i) =>
                  i.type === 'TCKN' && normalizeDigits(i.value) === normalizeDigits(entry.value!),
              ),
          );
          if (taken)
            return problem(api, 409, 'PERSON_IDENTIFIER_TAKEN', 'Kimlik başka bir kişide kayıtlı');
        }
        person.identifiers = person.identifiers.filter((i) => i.type !== entry.type);
        person.identifiers.push({
          type: entry.type,
          value: entry.value,
          primary: entry.primary ?? false,
        });
      }
      if (patch.firstName !== undefined) person.firstName = patch.firstName;
      if (patch.lastName !== undefined) person.lastName = patch.lastName;
      if ('middleName' in patch) person.middleName = patch.middleName ?? null;
      if ('birthDate' in patch) person.birthDate = patch.birthDate ?? null;
      if ('sexAtBirth' in patch) person.sexAtBirth = patch.sexAtBirth ?? null;
      if (patch.status !== undefined) person.status = patch.status;
      person.rowVersion += 1;
      return HttpResponse.json(toPerson(person), { headers: { ETag: etagOf(person.rowVersion) } });
    }),

    http.post(`${ANY}/api/v1/people/search-by-identifier`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'member.identifier.search', true);
      if ('error' in g) return g.error;
      if (!hasStepUp(g.session)) return stepUpRequired(api);
      const body = await readJson<Schemas['IdentifierSearchRequest']>(request);
      if (!body?.type || !body.value)
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'value', code: 'REQUIRED' }],
        });
      const normalized = normalizeDigits(body.value);
      const found = world().people.find(
        (p) =>
          p.tenantId === g.tenantId &&
          p.identifiers.some(
            (i) => i.type === body.type && normalizeDigits(i.value) === normalized,
          ),
      );
      if (!found) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      return HttpResponse.json(toPersonSummary(found));
    }),

    http.get(`${ANY}/api/v1/party/catalogs`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'member.read', false);
      if ('error' in g) return g.error;
      const body: Schemas['PartyCatalogs'] = {
        identifierTypes: IDENTIFIER_TYPE_CATALOG,
        membershipTypes: MEMBERSHIP_TYPE_CATALOG,
        relationshipTypes: RELATIONSHIP_TYPE_CATALOG,
      };
      return HttpResponse.json(body);
    }),

    // Contact details (WP-I5-05 section 2.5). The value goes in once and comes back only
    // as a mask: there is no endpoint here, or anywhere, that returns an address.
    http.get(`${ANY}/api/v1/people/:personId/contacts`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'member.contact.read', false);
      if ('error' in g) return g.error;
      const personId = pathParam(params, 'personId');
      const person = world().people.find((p) => p.id === personId && p.tenantId === g.tenantId);
      if (!person) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      return HttpResponse.json({ items: contactsOf(g.tenantId, personId) });
    }),

    http.put(`${ANY}/api/v1/people/:personId/contacts`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'member.contact.manage', true);
      if ('error' in g) return g.error;
      const expected = requireIfMatch(api, request);
      if (typeof expected !== 'number') return expected;
      const personId = pathParam(params, 'personId');
      const person = world().people.find((p) => p.id === personId && p.tenantId === g.tenantId);
      if (!person) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      if (person.rowVersion !== expected)
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      const body = await readJson<{ items: Schemas['PersonContactInput'][] }>(request);
      if (!body?.items) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

      const errors: FieldError[] = [];
      const primaries = new Set<string>();
      const normalized = body.items.map((c, i) => {
        const field = `items[${i}]`;
        const value = normalizeContact(c.channel, c.value);
        if (c.channel !== 'EMAIL' && c.channel !== 'SMS') {
          errors.push({ field: `${field}.channel`, code: 'CONTACT_CHANNEL_UNKNOWN' });
        } else if (!validContact(c.channel, value)) {
          // The refusal names the shape and never echoes the value: a 422 carrying an
          // address is an address in every log that ever records the response.
          errors.push({ field: `${field}.value`, code: 'CONTACT_INVALID' });
        }
        if (c.primary) {
          if (primaries.has(c.channel))
            errors.push({ field: `${field}.primary`, code: 'CONTACT_PRIMARY_TWICE' });
          primaries.add(c.channel);
        }
        return { ...c, value };
      });
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }

      // A replace and not a merge: a merge leaves behind the number somebody asked to
      // have removed, and a notification to it is what this table exists to avoid.
      const kept = world().personContacts.filter(
        (c) => !(c.tenantId === g.tenantId && c.personId === personId),
      );
      const written: StoredPersonContact[] = normalized.map((c) => ({
        id: world().nextId(),
        tenantId: g.tenantId,
        personId,
        channel: c.channel,
        value: c.value,
        verifiedAt: c.verified ? new Date().toISOString() : null,
        primary: c.primary ?? false,
        createdAt: new Date().toISOString(),
        rowVersion: 1,
      }));
      world().personContacts = [...kept, ...written];
      // The contacts are child rows of the person; writing them moves the person's ETag.
      person.rowVersion += 1;
      return HttpResponse.json({ items: contactsOf(g.tenantId, personId) });
    }),

    http.get(`${ANY}/api/v1/people/:personId/relationships`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'member.read', false);
      if ('error' in g) return g.error;
      const personId = pathParam(params, 'personId');
      const person = world().people.find((p) => p.id === personId && p.tenantId === g.tenantId);
      if (!person) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const items = world()
        .personRelationships.filter(
          (r) =>
            r.tenantId === g.tenantId &&
            (r.sourcePersonId === personId || r.targetPersonId === personId),
        )
        .map((r) => toPersonRelationship(world(), r, person.id));
      return HttpResponse.json({ items });
    }),

    http.post(`${ANY}/api/v1/people/:personId/relationships`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'member.manage', true);
      if ('error' in g) return g.error;
      if (!request.headers.get('Idempotency-Key'))
        return problem(api, 400, 'IDEMPOTENCY_KEY_REQUIRED', 'Idempotency-Key başlığı gerekli');
      const personId = pathParam(params, 'personId');
      const person = world().people.find((p) => p.id === personId && p.tenantId === g.tenantId);
      if (!person) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const body = await readJson<Schemas['CreateRelationshipRequest']>(request);
      if (!body?.relationshipType || !body.targetPersonId || !body.validFrom)
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'relationshipType', code: 'REQUIRED' }],
        });
      if (body.targetPersonId === personId)
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'targetPersonId', code: 'SELF_REFERENCE' }],
        });
      const type = RELATIONSHIP_TYPE_CATALOG.find((t) => t.code === body.relationshipType);
      if (!type || type.status !== 'ACTIVE')
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'relationshipType', code: 'ENUM' }],
        });
      const target = world().people.find(
        (p) => p.id === body.targetPersonId && p.tenantId === g.tenantId,
      );
      if (!target) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const overlap = world().personRelationships.some(
        (r) =>
          r.tenantId === g.tenantId &&
          r.status !== 'ENDED' &&
          r.relationshipType === body.relationshipType &&
          ((r.sourcePersonId === personId && r.targetPersonId === body.targetPersonId) ||
            (r.sourcePersonId === body.targetPersonId && r.targetPersonId === personId)) &&
          (r.validTo === null || r.validTo > body.validFrom!) &&
          (!body.validTo || r.validFrom < body.validTo),
      );
      if (overlap)
        return problem(api, 409, 'RELATIONSHIP_OVERLAP', 'Bu dönem için ilişki zaten var');
      const rel: StoredPersonRelationship = {
        id: world().nextId(),
        tenantId: g.tenantId,
        sourcePersonId: personId,
        targetPersonId: body.targetPersonId,
        relationshipType: body.relationshipType,
        status: 'ACTIVE',
        validFrom: body.validFrom,
        validTo: body.validTo ?? null,
        endReasonCode: null,
        rowVersion: 1,
      };
      world().personRelationships.push(rel);
      return HttpResponse.json(toPersonRelationship(world(), rel, personId), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.post(
      `${ANY}/api/v1/people/:personId/relationships/:relationshipId/end`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'member.manage', true);
        if ('error' in g) return g.error;
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null)
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        const personId = pathParam(params, 'personId');
        const rel = world().personRelationships.find(
          (r) =>
            r.id === params['relationshipId'] &&
            r.tenantId === g.tenantId &&
            (r.sourcePersonId === personId || r.targetPersonId === personId),
        );
        if (!rel) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        if (rel.rowVersion !== expected)
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        const body = await readJson<Schemas['EndPeriodCommand']>(request);
        if (!body?.endsOn || !body.reasonCode)
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: [{ field: 'reasonCode', code: 'REQUIRED' }],
          });
        rel.status = 'ENDED';
        rel.validTo = body.endsOn;
        rel.endReasonCode = body.reasonCode;
        rel.rowVersion += 1;
        return HttpResponse.json(toPersonRelationship(world(), rel, personId), {
          headers: { ETag: etagOf(rel.rowVersion) },
        });
      },
    ),

    http.get(`${ANY}/api/v1/people/:personId/memberships`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'member.read', false);
      if ('error' in g) return g.error;
      const person = world().people.find(
        (p) => p.id === params['personId'] && p.tenantId === g.tenantId,
      );
      if (!person) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const items = world()
        .memberships.filter((m) => m.tenantId === g.tenantId && m.personId === person.id)
        .sort((a, b) => (a.validFrom < b.validFrom ? 1 : -1))
        .map((m) => toSponsorMembership(world(), m));
      return HttpResponse.json({ items });
    }),

    http.post(`${ANY}/api/v1/people/:personId/memberships`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'member.manage', true);
      if ('error' in g) return g.error;
      if (!request.headers.get('Idempotency-Key'))
        return problem(api, 400, 'IDEMPOTENCY_KEY_REQUIRED', 'Idempotency-Key başlığı gerekli');
      const person = world().people.find(
        (p) => p.id === params['personId'] && p.tenantId === g.tenantId,
      );
      if (!person) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const body = await readJson<Schemas['CreateMembershipRequest']>(request);
      if (!body?.membershipType || !body.sponsorOrganizationId || !body.validFrom)
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'membershipType', code: 'REQUIRED' }],
        });
      const type = MEMBERSHIP_TYPE_CATALOG.find((t) => t.code === body.membershipType);
      if (!type || type.status !== 'ACTIVE')
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'membershipType', code: 'ENUM' }],
        });
      if (type.requiresPrincipal && !body.principalMembershipId)
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'principalMembershipId', code: 'REQUIRED' }],
        });
      if (body.principalMembershipId) {
        const principal = world().memberships.find(
          (m) => m.id === body.principalMembershipId && m.tenantId === g.tenantId,
        );
        if (!principal)
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: [{ field: 'principalMembershipId', code: 'INVALID_REFERENCE' }],
          });
      }
      if (
        body.externalMemberNo &&
        world().memberships.some(
          (m) =>
            m.tenantId === g.tenantId &&
            m.sponsorOrganizationId === body.sponsorOrganizationId &&
            m.externalMemberNo === body.externalMemberNo,
        )
      ) {
        return problem(api, 409, 'MEMBER_NO_TAKEN', 'Üye numarası zaten kullanılıyor');
      }
      const validTo = body.validTo ?? null;
      const overlap = world().memberships.some(
        (m) =>
          m.tenantId === g.tenantId &&
          m.personId === person.id &&
          m.sponsorOrganizationId === body.sponsorOrganizationId &&
          m.membershipType === body.membershipType &&
          m.status !== 'ENDED' &&
          (m.validTo === null || m.validTo > body.validFrom) &&
          (!validTo || m.validFrom < validTo),
      );
      if (overlap) return problem(api, 409, 'MEMBERSHIP_OVERLAP', 'Bu dönem için üyelik zaten var');
      const membership: StoredMembership = {
        id: world().nextId(),
        tenantId: g.tenantId,
        personId: person.id,
        sponsorOrganizationId: body.sponsorOrganizationId,
        membershipType: body.membershipType,
        principalMembershipId: body.principalMembershipId ?? null,
        externalMemberNo: body.externalMemberNo ?? null,
        status: body.status ?? 'ACTIVE',
        validFrom: body.validFrom,
        validTo,
        sourceSystem: null,
        rowVersion: 1,
      };
      world().memberships.push(membership);
      return HttpResponse.json(toSponsorMembership(world(), membership), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.patch(
      `${ANY}/api/v1/people/:personId/memberships/:membershipId`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'member.manage', true);
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
        const membership = world().memberships.find(
          (m) =>
            m.id === params['membershipId'] &&
            m.personId === params['personId'] &&
            m.tenantId === g.tenantId,
        );
        if (!membership) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        if (membership.rowVersion !== expected)
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        const patch = (await readJson<Schemas['UpdateMembershipRequest']>(request)) ?? {};
        if (
          'externalMemberNo' in patch &&
          patch.externalMemberNo &&
          world().memberships.some(
            (m) =>
              m.id !== membership.id &&
              m.tenantId === g.tenantId &&
              m.sponsorOrganizationId === membership.sponsorOrganizationId &&
              m.externalMemberNo === patch.externalMemberNo,
          )
        ) {
          return problem(api, 409, 'MEMBER_NO_TAKEN', 'Üye numarası zaten kullanılıyor');
        }
        if ('externalMemberNo' in patch)
          membership.externalMemberNo = patch.externalMemberNo ?? null;
        if (patch.status !== undefined) membership.status = patch.status;
        if ('validTo' in patch) membership.validTo = patch.validTo ?? null;
        membership.rowVersion += 1;
        return HttpResponse.json(toSponsorMembership(world(), membership), {
          headers: { ETag: etagOf(membership.rowVersion) },
        });
      },
    ),
  ];

  return [
    ...sessionHandlers,
    ...organizationHandlers,
    ...peopleHandlers,
    ...eligibilityHandlers(api),
    // Programs, plans, plan versions and enrollments live in their own module.
    ...benefitHandlers(api),
    ...entitlementHandlers(api),
    ...importHandlers(api),
    // M3: catalog, providers, contracts and prices, rules, quotes.
    ...catalogHandlers(api),
    ...providerHandlers(api),
    ...contractHandlers(api),
    ...rulesHandlers(api),
    ...pricingHandlers(api),
    // M4: the request lifecycle, the worklist, the document pipeline and notifications.
    ...serviceRequestHandlers(api),
    ...workflowHandlers(api),
    ...documentHandlers(api),
    ...notificationHandlers(api),
    // M5: the health case, and the projection that decides which half of it a caller sees.
    ...healthHandlers(api),
    // The treatment report reuses that same projection, so a screen built against one is
    // built against both.
    ...medicalReportHandlers(api),
    ...inpatientHandlers(api),
    ...claimHandlers(api),
  ];
}

export type { StoredOrganization };
