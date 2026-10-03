import { afterAll, afterEach, beforeAll, expect, it } from 'vitest';
import { createKapsoraClient } from '../client';
import { createOperations } from '../operations';
import { currentVersionOf, toMicros } from './data';
import { createMockServer } from './node';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const operations = () =>
  createOperations(
    createKapsoraClient({
      baseUrl: 'http://mock.test',
      csrfToken: () => api.session?.csrfToken ?? null,
    }),
  );
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

function fixture() {
  const session = api.signIn('doctor.a')!;
  const tenantId = session.activeTenantId!;
  const request = api.world.serviceRequests.find(
    (r) => r.tenantId === tenantId && r.status === 'APPROVED',
  )!;
  const body = { requestId: request.id, validTo: new Date(Date.now() + 86400000).toISOString() };
  return { tenantId, request, body };
}

it('reserves mapped quantity once, then returns the same reference on retry and scoped list', async () => {
  const { tenantId, request, body } = fixture();
  const line = currentVersionOf(api.world, request)!.items[0]!;
  const enrollment = api.world.enrollments.find((e) => e.id === request.enrollmentId)!;
  const version = api.world.planVersions.find(
    (v) => v.planId === enrollment.planId && v.status === 'PUBLISHED',
  )!;
  const mapping = api.world.entitlementMappings.find(
    (m) => m.planVersionId === version.id && m.serviceDefinitionId === line.serviceDefinitionId,
  )!;
  mapping.unitFactor = '2';
  const reserved = () =>
    api.world.entitlementAccounts.reduce((n, a) => n + toMicros(a.reserved), 0n);
  const before = reserved();
  const first = await operations().authorizations.create(
    tenantId,
    body,
    'authorization-mapped-once',
  );
  const replay = await operations().authorizations.create(
    tenantId,
    body,
    'authorization-mapped-once',
  );
  expect(replay.data.id).toBe(first.data.id);
  expect(first.etag).toBeTruthy();
  expect(reserved() - before).toBe(toMicros(line.approvedQuantity!) * 2n);
  expect(
    (await operations().authorizations.list(tenantId, { requestId: request.id })).items.map(
      (a) => a.id,
    ),
  ).toEqual([first.data.id]);
  api.signIn('provider.a');
  expect(
    (await operations().authorizations.list(tenantId, { requestId: request.id })).items,
  ).toHaveLength(1);
  await expect(
    operations().authorizations.create(tenantId, body, 'provider-cannot-reserve'),
  ).rejects.toMatchObject({ status: 403 });
  // A provider cannot discover another provider's authorization even knowing its id.
  request.providerOrganizationId = api.world.nextId();
  expect(
    (await operations().authorizations.list(tenantId, { requestId: request.id })).items,
  ).toHaveLength(0);
});

it('leaves every balance unchanged when the hold cannot be funded', async () => {
  const { tenantId, request, body } = fixture();
  currentVersionOf(api.world, request)!.items[0]!.approvedQuantity = '100000';
  const before = structuredClone(api.world.entitlementAccounts);
  await expect(
    operations().authorizations.create(tenantId, body, 'authorization-insufficient'),
  ).rejects.toMatchObject({ status: 409 });
  expect(api.world.entitlementAccounts).toEqual(before);
  expect(
    (await operations().authorizations.list(tenantId, { requestId: request.id })).items,
  ).toHaveLength(0);
});
