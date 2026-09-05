/**
 * End-to-end flows through the typed client for the five things M4's screens found the
 * platform had not decided (WP-I5-05): the service to entitlement mapping and the answer
 * it changes, the enrollment candidates a check names, contact details, and the display
 * names the wire now carries.
 *
 * The mock is a test double of the Go server, so every test below is written against a
 * behaviour the server has and a plausible mock would get wrong: an unmapped service still
 * answering SERVICE_MAPPING_PENDING rather than being quietly treated as covered, a
 * published version's mappings being frozen, a contact coming back masked and never as a
 * value, and a display name being derived on the way out rather than copied onto a row.
 */
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { createKapsoraClient, randomId, type KapsoraClient } from '../client';
import { createOperations } from '../operations';
import { ApiError, unwrap, type Problem } from '../problem';
import { createMockServer } from './node';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

function client(): KapsoraClient {
  return createKapsoraClient({ baseUrl: BASE, csrfToken: () => api.session?.csrfToken ?? null });
}

interface Session {
  c: KapsoraClient;
  tenantId: string;
  actorId: string;
}

async function signIn(username: string): Promise<Session> {
  const c = client();
  const o = createOperations(c);
  await o.session.login(username, PASSWORD);
  const session = await o.session.get();
  const tenantId = session.activeTenantId ?? (await o.session.tenants())[0]!.id;
  if (!session.activeTenantId) await o.session.switchTenant(tenantId);
  return { c, tenantId, actorId: session.actorId };
}

const key = (): string => `mock-${randomId()}`;
const tenant = (s: Session) => ({ 'X-Tenant-ID': s.tenantId });

async function refusal(call: Promise<unknown>): Promise<Problem> {
  try {
    await call;
  } catch (error) {
    if (error instanceof ApiError) return error.problem;
    throw error;
  }
  throw new Error('expected the call to be refused');
}

// --- fixture lookups, so no test depends on a generated id -------------------------------

function definitionByCode(code: string) {
  const row = api.world.serviceDefinitions.find((d) => d.code === code);
  if (!row) throw new Error(`fixture: no service definition ${code}`);
  return row;
}

function versionOfPlan(planCode: string, status: 'PUBLISHED' | 'DRAFT') {
  const plan = api.world.plans.find((p) => p.code === planCode);
  if (!plan) throw new Error(`fixture: no plan ${planCode}`);
  const version = api.world.planVersions.find((v) => v.planId === plan.id && v.status === status);
  if (!version) throw new Error(`fixture: no ${status} version of ${planCode}`);
  return version;
}

function enrollmentCounts(tenantId: string): Map<string, number> {
  const counts = new Map<string, number>();
  for (const e of api.world.enrollments) {
    if (e.tenantId !== tenantId || e.status !== 'ACTIVE') continue;
    counts.set(e.personId, (counts.get(e.personId) ?? 0) + 1);
  }
  return counts;
}

/** The person with exactly one enrollment; the demo principal has two. */
function singlyEnrolledPerson(tenantId: string): string {
  for (const [personId, n] of enrollmentCounts(tenantId)) if (n === 1) return personId;
  throw new Error('fixture: nobody is enrolled exactly once');
}

function doublyEnrolledPerson(tenantId: string): string {
  for (const [personId, n] of enrollmentCounts(tenantId)) if (n > 1) return personId;
  throw new Error('fixture: nobody is enrolled twice');
}

const SERVICE_DATE = '2026-06-15';

async function check(s: Session, body: Record<string, unknown>) {
  const out = await unwrap(
    s.c.POST('/api/v1/eligibility/checks', {
      params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
      // The request shape is the contract's; the cast is only because this helper is
      // generic over the body it is handed.
      body: body as never,
    }),
  );
  return out.data;
}

async function personVersion(s: Session, personId: string): Promise<number> {
  const out = await unwrap(
    s.c.GET('/api/v1/people/{personId}', {
      params: { header: tenant(s), path: { personId } },
    }),
  );
  return out.data.rowVersion;
}

describe('the mapping decides the answer', () => {
  it('answers ELIGIBLE for a mapped service with no entitlement hint at all', async () => {
    const s = await signIn('admin.a');
    const result = await check(s, {
      personId: singlyEnrolledPerson(s.tenantId),
      serviceDate: SERVICE_DATE,
      serviceItems: [{ serviceDefinitionId: definitionByCode('PHYSIO_SESSION').id, quantity: '1' }],
    });

    // Nothing in the request named an entitlement. Only the mapping could have decided it.
    expect(result.outcome).toBe('ELIGIBLE');
    expect(result.items?.[0]?.entitlementCode).toBe('PHYSIO_SESSION');
    expect(result.items?.[0]?.availableQuantity).not.toBeNull();
    expect(result.explanations.map((e) => e.code)).not.toContain('SERVICE_MAPPING_PENDING');
  });

  it('leaves a service nobody mapped as SERVICE_MAPPING_PENDING', async () => {
    const s = await signIn('admin.a');
    const result = await check(s, {
      personId: singlyEnrolledPerson(s.tenantId),
      serviceDate: SERVICE_DATE,
      serviceItems: [
        { serviceDefinitionId: definitionByCode('LAB_PANEL_AMBIGUOUS').id, quantity: '1' },
      ],
    });

    expect(result.outcome).toBe('REVIEW_REQUIRED');
    expect(result.items?.[0]?.explanations.map((e) => e.code)).toEqual(['SERVICE_MAPPING_PENDING']);
  });
});

describe('the check names the enrollment candidates', () => {
  it('lists them on ENROLLMENT_MULTIPLE and resolves when one is chosen', async () => {
    const s = await signIn('admin.a');
    const person = doublyEnrolledPerson(s.tenantId);
    const ambiguous = await check(s, {
      personId: person,
      serviceDate: SERVICE_DATE,
      serviceItems: [{ serviceDefinitionId: definitionByCode('PHYSIO_SESSION').id, quantity: '1' }],
    });

    expect(ambiguous.outcome).toBe('REVIEW_REQUIRED');
    expect(ambiguous.explanations.map((e) => e.code)).toContain('ENROLLMENT_MULTIPLE');
    const candidates = ambiguous.enrollmentCandidates ?? [];
    expect(candidates.length).toBeGreaterThan(1);
    for (const candidate of candidates) {
      // The minimum a desk needs to ask again: an id is not a name.
      expect(candidate.planCode).not.toBe('');
      expect(candidate.planName).not.toBe('');
      expect(candidate.validFrom).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    }

    const chosen = candidates[0]!;
    const resolved = await check(s, {
      personId: person,
      enrollmentId: chosen.enrollmentId,
      serviceDate: SERVICE_DATE,
      serviceItems: [{ serviceDefinitionId: definitionByCode('PHYSIO_SESSION').id, quantity: '1' }],
    });
    expect(resolved.enrollmentId).toBe(chosen.enrollmentId);
    expect(resolved.explanations.map((e) => e.code)).not.toContain('ENROLLMENT_MULTIPLE');
    expect(resolved.enrollmentCandidates ?? []).toHaveLength(0);
  });
});

describe('the mapping endpoints', () => {
  it('lists what the demo world maps and refuses a write to a published version', async () => {
    const s = await signIn('admin.a');
    const published = versionOfPlan('FAM-HEALTH', 'PUBLISHED');
    const listed = await unwrap(
      s.c.GET('/api/v1/plan-versions/{planVersionId}/entitlement-mappings', {
        params: { header: tenant(s), path: { planVersionId: published.id } },
      }),
    );
    expect(listed.data.items.map((m) => m.serviceCode)).toContain('PHYSIO_SESSION');
    for (const mapping of listed.data.items) {
      expect(mapping.entitlementCode).not.toBe('');
      expect(mapping.unitFactor).toMatch(/^[0-9]+(\.[0-9]+)?$/);
    }

    const frozen = await refusal(
      unwrap(
        s.c.PUT('/api/v1/plan-versions/{planVersionId}/entitlement-mappings', {
          params: {
            header: { ...tenant(s), 'If-Match': `"${published.rowVersion}"` },
            path: { planVersionId: published.id },
          },
          body: { items: [] },
        }),
      ),
    );
    expect(frozen.status).toBe(409);
    expect(frozen.code).toBe('PLAN_VERSION_IMMUTABLE');
  });

  it('replaces the set of a draft and refuses a code the version does not define', async () => {
    const s = await signIn('admin.a');
    const draft = versionOfPlan('FAM-HEALTH', 'DRAFT');
    // The draft carries no definitions in the fixture, so give it one to map onto first.
    const withDefinitions = await unwrap(
      s.c.PUT('/api/v1/plan-versions/{planVersionId}/entitlement-definitions', {
        params: {
          header: { ...tenant(s), 'If-Match': `"${draft.rowVersion}"` },
          path: { planVersionId: draft.id },
        },
        body: {
          items: [
            {
              code: 'PHYSIO_SESSION',
              name: 'Fizyoterapi seansı',
              unitType: 'SESSION',
              periodType: 'PLAN_YEAR',
              initialQuantity: 12,
            },
          ],
        },
      }),
    );
    const etag = `"${withDefinitions.data.rowVersion}"`;

    const bad = await refusal(
      unwrap(
        s.c.PUT('/api/v1/plan-versions/{planVersionId}/entitlement-mappings', {
          params: { header: { ...tenant(s), 'If-Match': etag }, path: { planVersionId: draft.id } },
          body: {
            items: [
              {
                serviceDefinitionId: definitionByCode('PHYSIO_SESSION').id,
                entitlementCode: 'NOT_ON_THIS_VERSION',
              },
            ],
          },
        }),
      ),
    );
    expect(bad.status).toBe(422);
    expect(bad.errors?.[0]?.field).toBe('items[0].entitlementCode');

    const written = await unwrap(
      s.c.PUT('/api/v1/plan-versions/{planVersionId}/entitlement-mappings', {
        params: { header: { ...tenant(s), 'If-Match': etag }, path: { planVersionId: draft.id } },
        body: {
          items: [
            {
              serviceDefinitionId: definitionByCode('PHYSIO_SESSION').id,
              entitlementCode: 'PHYSIO_SESSION',
              unitFactor: '2',
            },
          ],
        },
      }),
    );
    expect(written.data.items).toHaveLength(1);
    expect(written.data.items[0]!.unitFactor).toBe('2.000000');
  });
});

describe('contact details', () => {
  it('comes back masked, never as a value, and refuses what nobody could send to', async () => {
    const s = await signIn('admin.a');
    const person = api.world.people.find((p) => p.tenantId === s.tenantId)!;

    const listed = await unwrap(
      s.c.GET('/api/v1/people/{personId}/contacts', {
        params: { header: tenant(s), path: { personId: person.id } },
      }),
    );
    const body = JSON.stringify(listed.data);
    for (const contact of api.world.personContacts) {
      expect(body).not.toContain(contact.value);
    }

    const etag = `"${await personVersion(s, person.id)}"`;
    const written = await unwrap(
      s.c.PUT('/api/v1/people/{personId}/contacts', {
        params: { header: { ...tenant(s), 'If-Match': etag }, path: { personId: person.id } },
        body: {
          items: [
            { channel: 'EMAIL', value: '  Yeni.Adres@Example.INVALID ', primary: true },
            { channel: 'SMS', value: '+90 555 111 22 33', primary: true },
          ],
        },
      }),
    );
    expect(written.data.items).toHaveLength(2);
    const emailed = written.data.items.find((c) => c.channel === 'EMAIL')!;
    expect(emailed.maskedValue).toBe('y*********@example.invalid');
    expect(emailed.verifiedAt).toBeNull();
    expect(JSON.stringify(written.data)).not.toContain('yeni.adres@example.invalid');

    // The child write moved the person's ETag, so the caller's old one is stale.
    const stale = await refusal(
      unwrap(
        s.c.PUT('/api/v1/people/{personId}/contacts', {
          params: { header: { ...tenant(s), 'If-Match': etag }, path: { personId: person.id } },
          body: { items: [] },
        }),
      ),
    );
    expect(stale.status).toBe(412);

    const fresh = `"${await personVersion(s, person.id)}"`;
    const bad = await refusal(
      unwrap(
        s.c.PUT('/api/v1/people/{personId}/contacts', {
          params: { header: { ...tenant(s), 'If-Match': fresh }, path: { personId: person.id } },
          body: { items: [{ channel: 'EMAIL', value: 'kimse' }] },
        }),
      ),
    );
    expect(bad.status).toBe(422);
    expect(bad.errors?.[0]?.code).toBe('CONTACT_INVALID');
    expect(JSON.stringify(bad)).not.toContain('kimse');
  });

  it('is not something a provider may read', async () => {
    const s = await signIn('provider.a');
    const person = api.world.people.find((p) => p.tenantId === s.tenantId)!;
    const denied = await refusal(
      unwrap(
        s.c.GET('/api/v1/people/{personId}/contacts', {
          params: { header: tenant(s), path: { personId: person.id } },
        }),
      ),
    );
    expect(denied.status).toBe(403);
    expect(denied.code).toBe('PERMISSION_DENIED');
  });
});

describe('names on the wire', () => {
  it('carries the member and the provider on a request, and the assignee on a work item', async () => {
    const s = await signIn('admin.a');
    const requests = await unwrap(
      s.c.GET('/api/v1/service-requests', { params: { header: tenant(s), query: { limit: 50 } } }),
    );
    expect(requests.data.items.length).toBeGreaterThan(0);
    for (const request of requests.data.items) {
      const person = api.world.people.find((p) => p.id === request.personId)!;
      const expected = [person.firstName, person.middleName, person.lastName]
        .filter(Boolean)
        .join(' ');
      expect(request.personDisplayName).toBe(expected);
      if (request.providerOrganizationId) {
        expect(request.providerDisplayName).not.toBeNull();
      } else {
        expect(request.providerDisplayName).toBeNull();
      }
    }

    const items = await unwrap(
      s.c.GET('/api/v1/work-items', { params: { header: tenant(s), query: { limit: 50 } } }),
    );
    expect(items.data.items.filter((i) => i.assigneeActorId).length).toBeGreaterThan(0);
    for (const item of items.data.items) {
      const account = api.world.accounts.find((a) => a.actorId === item.assigneeActorId);
      expect(item.assigneeDisplayName ?? null).toBe(account?.displayName ?? null);
    }
  });
});
