import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { createKapsoraClient } from '../client';
import { randomVKN } from '../identifiers';
import { createOperations } from '../operations';
import type { ApiError } from '../problem';
import { createMockServer } from './node';

const { api, server } = createMockServer({ organizationsPerTenant: 120 });
const BASE = 'http://mock.test';

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

function ops() {
  const client = createKapsoraClient({
    baseUrl: BASE,
    csrfToken: () => api.session?.csrfToken ?? null,
  });
  return createOperations(client);
}

async function signInAdminA() {
  const o = ops();
  await o.session.login('admin.a', 'demo parola 2026 kapsora');
  const session = await o.session.get();
  return { o, tenantId: session.activeTenantId! };
}

describe('session flow', () => {
  it('answers 401 before login, then carries CSRF and tenants', async () => {
    const o = ops();
    const err = (await o.session.get().catch((e: unknown) => e)) as ApiError;
    expect(err.status).toBe(401);
    expect(err.problem.code).toBe('UNAUTHENTICATED');

    const bad = (await o.session.login('admin.a', 'short').catch((e: unknown) => e)) as ApiError;
    expect(bad.problem.code).toBe('INVALID_CREDENTIALS');

    const info = await o.session.login('both.ab', 'demo parola 2026 kapsora');
    expect(info.activeTenantId).toBeNull();
    expect(info.csrfToken).toMatch(/^csrf-/);
    const me = await o.session.me();
    expect(me.tenants.map((t) => t.tenant.code)).toEqual(['DEMO_A', 'DEMO_B']);
    const ctx = await o.session.switchTenant(me.tenants[1]!.tenant.id);
    expect(ctx.tenant.code).toBe('DEMO_B');
    expect((await o.session.get()).activeTenantId).toBe(ctx.tenant.id);
    await o.session.logout();
    const after = (await o.session.get().catch((e: unknown) => e)) as ApiError;
    expect(after.status).toBe(401);
  });

  it('rejects mutations without the CSRF token', async () => {
    const o = ops();
    await o.session.login('admin.a', 'demo parola 2026 kapsora');
    const bare = createOperations(createKapsoraClient({ baseUrl: BASE }));
    const err = (await bare.session.logout().catch((e: unknown) => e)) as ApiError;
    expect(err.problem.code).toBe('CSRF_TOKEN_INVALID');
  });
});

describe('organizations', () => {
  it('pages 120 rows in three pages of 50 and filters', async () => {
    const { o, tenantId } = await signInAdminA();
    const seen = new Set<string>();
    let cursor: string | undefined;
    let pages = 0;
    do {
      const page = await o.organizations.list(tenantId, cursor ? { cursor } : {});
      pages += 1;
      for (const item of page.items) seen.add(item.id);
      cursor = page.nextCursor ?? undefined;
    } while (cursor);
    expect(pages).toBe(3);
    expect(seen.size).toBe(120);

    const providers = await o.organizations.list(tenantId, { role: 'PROVIDER', limit: 200 });
    expect(providers.items.every((i) => i.relationshipRole === 'PROVIDER')).toBe(true);
    expect(providers.items.length).toBeGreaterThan(30);

    const bad = (await o.organizations
      .list(tenantId, { cursor: 'bogus' })
      .catch((e: unknown) => e)) as ApiError;
    expect(bad.problem.code).toBe('CURSOR_INVALID');
  });

  it('creates, dedups by tax number, replays idempotently and patches with ETag', async () => {
    const { o, tenantId } = await signInAdminA();
    const vkn = randomVKN();
    const body = {
      legalName: 'Yeni Klinik Ltd. Şti.',
      displayName: 'Yeni Klinik',
      organizationKind: 'PROVIDER' as const,
      relationshipRole: 'PROVIDER' as const,
      identifiers: [{ type: 'VKN' as const, value: vkn, primary: true }],
    };
    const created = await o.organizations.create(tenantId, body, 'key-1');
    expect(created.etag).toBe('"1"');
    expect(created.data.identifiers[0]!.maskedValue).toBe(
      `${vkn.slice(0, 2)}******${vkn.slice(8)}`,
    );
    expect(JSON.stringify(created.data)).not.toContain(vkn);

    const replay = await o.organizations.create(tenantId, body, 'key-1');
    expect(replay.data.id).toBe(created.data.id);

    const dup = (await o.organizations
      .create(tenantId, body, 'key-2')
      .catch((e: unknown) => e)) as ApiError;
    expect(dup.problem.code).toBe('ORGANIZATION_RELATIONSHIP_EXISTS');

    const invalid = (await o.organizations
      .create(
        tenantId,
        { ...body, identifiers: [{ type: 'VKN', value: '1234567891', primary: true }] },
        'key-3',
      )
      .catch((e: unknown) => e)) as ApiError;
    expect(invalid.fieldErrors().get('identifiers[0].value')?.code).toBe('IDENTIFIER_INVALID');

    const patched = await o.organizations.update(tenantId, created.data.id, '"1"', {
      tenantCode: 'KLN-1',
      relationshipStatus: 'SUSPENDED',
    });
    expect(patched.etag).toBe('"2"');
    expect(patched.data.tenantCode).toBe('KLN-1');

    const stale = (await o.organizations
      .update(tenantId, created.data.id, '"1"', { tenantCode: null })
      .catch((e: unknown) => e)) as ApiError;
    expect(stale.status).toBe(412);
    expect(stale.problem.code).toBe('ETAG_MISMATCH');

    const cleared = await o.organizations.update(tenantId, created.data.id, '"2"', {
      tenantCode: null,
    });
    expect(cleared.data.tenantCode).toBeUndefined();
  });

  it('hides other tenants and enforces the tenant header', async () => {
    const { o, tenantId } = await signInAdminA();
    const otherTenant = api.world.tenants.find((t) => t.id !== tenantId)!;
    const foreign = api.world.relationships.find((r) => r.tenantId === otherTenant.id)!;
    const notFound = (await o.organizations
      .get(tenantId, foreign.id)
      .catch((e: unknown) => e)) as ApiError;
    expect(notFound.status).toBe(404);
    const mismatch = (await o.organizations
      .list(otherTenant.id)
      .catch((e: unknown) => e)) as ApiError;
    expect(mismatch.problem.code).toBe('TENANT_MISMATCH');
  });
});
