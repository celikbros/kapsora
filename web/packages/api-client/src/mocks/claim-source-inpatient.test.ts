import { afterAll, afterEach, beforeAll, expect, it } from 'vitest';

import { createKapsoraClient, randomId } from '../client';
import { createOperations } from '../operations';
import { ApiError, unwrap } from '../problem';
import { mockAuthorizations } from './authorization-handlers';
import { currentVersionOf } from './data';
import { createMockServer } from './node';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const baseUrl = 'http://mock.test';
const key = () => `inpatient-mock-${randomId()}`;

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

async function session() {
  const c = createKapsoraClient({ baseUrl, csrfToken: () => api.session?.csrfToken ?? null });
  const ops = createOperations(c);
  await ops.session.login('billing.a', 'demo parola 2026 kapsora');
  const signedIn = await ops.session.get();
  const tenantId = signedIn.activeTenantId ?? (await ops.session.tenants())[0]!.id;
  if (!signedIn.activeTenantId) await ops.session.switchTenant(tenantId);
  return { c, ops, tenantId, actorId: signedIn.actorId };
}

function seedDischargedStay(
  tenantId: string,
  actorId: string,
  originalDays: number,
  extensionDays: number,
) {
  const world = api.world;
  const baseCase = world.healthCases.find(
    (c) => c.tenantId === tenantId && c.caseType === 'OUTPATIENT',
  )!;
  const request = world.serviceRequests.find(
    (r) => r.tenantId === tenantId && r.providerOrganizationId === baseCase.providerOrganizationId,
  )!;
  request.status = 'APPROVED';
  const requestItem = currentVersionOf(world, request)!.items[0]!;
  const service = {
    ...world.serviceDefinitions.find((d) => d.id === requestItem.serviceDefinitionId)!,
    id: world.nextId(),
    code: 'INPATIENT_DAY',
    name: 'Yatış günü',
  };
  world.serviceDefinitions.push(service);
  requestItem.serviceDefinitionId = service.id;
  requestItem.unitType = 'NIGHT';
  const row = {
    ...baseCase,
    id: world.nextId(),
    caseType: 'INPATIENT' as const,
    personId: request.personId,
    programId: request.programId,
    enrollmentId: request.enrollmentId,
    providerOrganizationId: request.providerOrganizationId!,
    serviceRequestId: request.id,
  };
  world.healthCases.push(row);
  const encounter = {
    ...world.encounters[0]!,
    id: world.nextId(),
    tenantId,
    caseId: row.id,
    endedAt: '2026-06-15T09:30:00Z',
  };
  world.encounters.push(encounter);
  const diagnosis = {
    ...world.diagnoses[0]!,
    id: world.nextId(),
    tenantId,
    encounterId: encounter.id,
    diagnosisType: 'PRIMARY' as const,
  };
  world.diagnoses.push(diagnosis);
  const total = originalDays + extensionDays;
  const originalId = world.nextId();
  const stay = {
    ...world.inpatientStays[0]!,
    id: world.nextId(),
    tenantId,
    caseId: row.id,
    personId: row.personId,
    providerOrganizationId: row.providerOrganizationId!,
    status: 'DISCHARGED' as const,
    serviceRequestId: request.id,
    authorizationId: originalId,
    admissionDiagnosisId: extensionDays ? null : diagnosis.id,
    admissionAt: '2026-06-15T09:00:00Z',
    dischargeAt: `2026-06-${String(15 + total).padStart(2, '0')}T09:00:00Z`,
    authorizedDays: String(total),
    actualDays: String(total),
    releasedDays: '0',
    overAuthorization: false,
  };
  world.inpatientStays.push(stay);
  const now = new Date().toISOString();
  const addAuthorization = (id: string, days: number) =>
    mockAuthorizations(world).push({
      id,
      tenantId,
      requestId: request.id,
      reference: `AUT-${id.slice(-6)}`,
      status: 'ACTIVE',
      validFrom: now,
      validTo: new Date(Date.now() + 86_400_000).toISOString(),
      approvedAt: now,
      approvedBy: actorId,
      createdAt: now,
      rowVersion: 1,
      consumedTotal: '0',
      reservedTotal: String(days),
      vouchers: [],
      items: [
        {
          id: world.nextId(),
          requestItemId: requestItem.id,
          serviceDefinitionId: service.id,
          approvedQuantity: String(days),
          consumedQuantity: '0',
          memberAmount: '0',
          entitlementReservationId: world.nextId(),
        },
      ],
    });
  addAuthorization(originalId, originalDays);
  let extensionId: string | null = null;
  if (extensionDays > 0) {
    extensionId = world.nextId();
    addAuthorization(extensionId, extensionDays);
    world.stayExtensions.push({
      ...world.stayExtensions[0]!,
      id: world.nextId(),
      tenantId,
      stayId: stay.id,
      sequenceNo: 1,
      additionalDays: extensionDays,
      authorizationId: extensionId,
      status: 'APPROVED',
    });
  }
  return { row, request, stay, diagnosis, service, originalId, extensionId, total };
}

async function problemOf(call: Promise<unknown>) {
  try {
    await call;
  } catch (error) {
    if (error instanceof ApiError) return error.problem;
    throw error;
  }
  throw new Error('expected refusal');
}

for (const [originalDays, extensionDays] of [
  [2, 0],
  [5, 1],
] as const) {
  it(`hands off a discharged ${originalDays}+${extensionDays} day stay with exact source and frozen draws`, async () => {
    const s = await session();
    const seeded = seedDischargedStay(s.tenantId, s.actorId, originalDays, extensionDays);
    const summary = await s.ops.claims.listCaseSources(s.tenantId);
    expect(summary.items.find((item) => item.caseId === seeded.row.id)?.serviceDate).toBe(
      '2026-06-15',
    );
    const detail = await s.ops.claims.getCaseSource(s.tenantId, seeded.row.id);
    expect(detail.data.lines).toMatchObject([
      { serviceCode: 'INPATIENT_DAY', quantity: String(seeded.total) },
    ]);
    const wire = JSON.stringify(detail.data);
    expect(wire).not.toContain(seeded.diagnosis.id);
    expect(wire).not.toContain(seeded.stay.id);
    expect(wire).not.toContain('reasonText');
    const charge = {
      lines: [
        {
          serviceDefinitionId: seeded.service.id,
          quantity: String(seeded.total),
          lineAmount: '2400',
        },
      ],
    };
    const partial = { lines: [{ ...charge.lines[0]!, quantity: String(seeded.total - 1) }] };
    expect(
      (
        await problemOf(
          s.ops.claims.createFromCase(s.tenantId, seeded.row.id, partial, detail.etag, key()),
        )
      ).status,
    ).toBe(422);
    const created = await s.ops.claims.createFromCase(
      s.tenantId,
      seeded.row.id,
      charge,
      detail.etag,
      key(),
    );
    expect(created.data.sourceType).toBe('HEALTH_CASE');
    expect(created.data.sourceId).toBe(seeded.row.id);
    expect(created.data.serviceDateFrom).toBe('2026-06-15');
    expect(created.data.serviceDateTo).toBe(seeded.stay.dischargeAt!.slice(0, 10));
    expect(JSON.stringify(created.data)).not.toContain(seeded.diagnosis.id);
    expect(
      (
        await problemOf(
          s.ops.claims.createFromCase(s.tenantId, seeded.row.id, charge, detail.etag, key()),
        )
      ).code,
    ).toBe('CLAIM_CASE_ALREADY_CLAIMED');
    const generic = s.c.POST('/api/v1/claims', {
      params: { header: { 'X-Tenant-ID': s.tenantId, 'Idempotency-Key': key() } },
      body: {
        personId: seeded.row.personId,
        programId: seeded.row.programId,
        enrollmentId: seeded.row.enrollmentId,
        providerOrganizationId: seeded.row.providerOrganizationId!,
        caseId: seeded.row.id,
        serviceDateFrom: '2026-06-15',
        serviceDateTo: '2026-06-15',
        lines: [
          {
            lineNo: 1,
            serviceDefinitionId: seeded.service.id,
            unitType: 'NIGHT',
            quantity: String(seeded.total),
            lineAmount: '2400',
          },
        ],
      },
    });
    expect((await problemOf(unwrap(generic))).status).toBe(422);
    const submitted = await unwrap(
      s.c.POST('/api/v1/claims/{claimId}/submit', {
        params: {
          path: { claimId: created.data.id },
          header: { 'X-Tenant-ID': s.tenantId, 'If-Match': created.etag, 'Idempotency-Key': key() },
        },
      }),
    );
    expect(submitted.data.status).toBe('APPROVED');
    await expect(s.ops.claims.readiness(s.tenantId, created.data.id)).resolves.toMatchObject({
      claimId: created.data.id,
    });
    const version = api.world.claimVersions.find((v) => v.claimId === created.data.id)!;
    const draws = api.world.claimLineAllocations.filter((a) => a.versionId === version.id);
    expect(draws.map((a) => [a.authorizationId, a.plannedQuantity, a.appliedQuantity])).toEqual(
      extensionDays
        ? [
            [seeded.originalId, '5', '5'],
            [seeded.extensionId, '1', '1'],
          ]
        : [[seeded.originalId, '2', '2']],
    );
    expect(
      api.world.claimAuthorizations.find((a) => a.id === seeded.originalId)!.items[0]!
        .consumedQuantity,
    ).toBe(String(originalDays));
    if (seeded.extensionId)
      expect(
        api.world.claimAuthorizations.find((a) => a.id === seeded.extensionId)!.items[0]!
          .consumedQuantity,
      ).toBe(String(extensionDays));
  });
}

it('keeps a multi-hold shortage unapplied and refuses positive approval', async () => {
  const s = await session();
  const seeded = seedDischargedStay(s.tenantId, s.actorId, 5, 1);
  const detail = await s.ops.claims.getCaseSource(s.tenantId, seeded.row.id);
  const created = await s.ops.claims.createFromCase(
    s.tenantId,
    seeded.row.id,
    {
      lines: [{ serviceDefinitionId: seeded.service.id, quantity: '6', lineAmount: '2400' }],
    },
    detail.etag,
    key(),
  );
  api.world.claimAuthorizations.find(
    (a) => a.id === seeded.extensionId,
  )!.items[0]!.consumedQuantity = '1';
  const submitted = await unwrap(
    s.c.POST('/api/v1/claims/{claimId}/submit', {
      params: {
        path: { claimId: created.data.id },
        header: { 'X-Tenant-ID': s.tenantId, 'If-Match': created.etag, 'Idempotency-Key': key() },
      },
    }),
  );
  expect(submitted.data.status).toBe('PENDING_MEDICAL');
  const version = api.world.claimVersions.find((v) => v.claimId === created.data.id)!;
  expect(
    api.world.claimLineAllocations
      .filter((a) => a.versionId === version.id)
      .map((a) => a.appliedQuantity),
  ).toEqual(['0', '0']);
  expect(
    api.world.claimAuthorizations.find((a) => a.id === seeded.originalId)!.items[0]!
      .consumedQuantity,
  ).toBe('0');
  // A legacy or inconsistent decided row cannot claim invoice readiness without the receipt.
  api.world.claims.find((c) => c.id === created.data.id)!.status = 'APPROVED';
  expect((await problemOf(s.ops.claims.readiness(s.tenantId, created.data.id))).code).toBe(
    'CLAIM_INPATIENT_ALLOCATION_MISSING',
  );
});
