/**
 * End-to-end flows through the typed client against the inpatient half of the M5 mock world:
 * the admission window, the one-open-stay rule, the one-undecided-extension rule, the
 * segments and the discharge that settles up.
 *
 * The mock is a test double of the Go server and M5 treats a divergence in either direction as
 * a bug, so every test below is written against a behaviour the server has and a plausible
 * mock would get wrong: the fourth day back that is refused where the third is accepted, the
 * second admission at the same hospital that is a 409 while the same case at another hospital
 * is a transfer, the second extension that has to wait for the first, the two segments that
 * meet rather than overlap, and the discharge after three of five days that gives two back.
 *
 * The headline is the same assertion the Go tests make, made here: a sponsor HR user is served
 * every figure of the reconciliation and never the extension's reason text. Remove the
 * projection from inpatient-handlers.ts and this fails.
 */
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { createKapsoraClient, randomId, type KapsoraClient } from '../client';
import { createOperations } from '../operations';
import { ApiError, unwrap, type Problem } from '../problem';
import { createMockServer } from './node';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';
const DAY_MS = 86_400_000;

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

/** The seeded admission, found by its state rather than by a generated id. */
function admittedStay() {
  const row = api.world.inpatientStays.find((r) => r.status === 'ADMITTED');
  if (!row) throw new Error('fixture: no admitted stay');
  return row;
}

function segmentsOf(stayId: string) {
  return api.world.staySegments.filter((g) => g.stayId === stayId);
}

function extensionsOf(stayId: string) {
  return api.world.stayExtensions.filter((e) => e.stayId === stayId);
}

/** A case with no stay of its own, so a create has somewhere to land. */
function freeCase(tenantId: string) {
  const taken = new Set(api.world.inpatientStays.map((r) => r.caseId));
  const row = api.world.healthCases.find((c) => c.tenantId === tenantId && !taken.has(c.id));
  if (!row) throw new Error('fixture: every case already has a stay');
  return row;
}

const iso = (offsetDays: number, hours = 9): string =>
  new Date(
    Math.floor(Date.now() / DAY_MS) * DAY_MS + offsetDays * DAY_MS + hours * 3_600_000,
  ).toISOString();

async function get(s: Session, id: string) {
  return unwrap(
    s.c.GET('/api/v1/inpatient-stays/{stayId}', {
      params: { header: tenant(s), path: { stayId: id } },
    }),
  );
}

async function etagOf(s: Session, id: string): Promise<string> {
  return (await get(s, id)).response.headers.get('ETag')!;
}

async function createStay(
  s: Session,
  body: {
    caseId: string;
    providerOrganizationId: string;
    admissionAt: string;
    estimatedDays: number;
  },
) {
  return unwrap(
    s.c.POST('/api/v1/inpatient-stays', {
      params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
      body,
    }),
  );
}

describe('the inpatient world', () => {
  it('carries an admitted stay with two segments and one decided extension', () => {
    const stay = admittedStay();
    expect(stay.authorizationId).not.toBeNull();
    expect(stay.authorizedDays).toBe('5');
    expect(stay.dischargeAt).toBeNull();

    const segments = segmentsOf(stay.id);
    expect(segments).toHaveLength(2);
    // A transfer rather than an overlap: the ward ends exactly where intensive care begins.
    const ward = segments.find((g) => g.segmentType === 'WARD')!;
    const icu = segments.find((g) => g.segmentType === 'ICU')!;
    expect(ward.endsAt).toBe(icu.startsAt);
    // And the patient is still there, so the last segment has no end.
    expect(icu.endsAt).toBeNull();

    const extensions = extensionsOf(stay.id);
    expect(extensions).toHaveLength(1);
    expect(extensions[0]!.status).toBe('APPROVED');
    expect(extensions[0]!.authorizationId).not.toBeNull();
  });
});

describe('the admission window', () => {
  it('refuses four days back and accepts three', async () => {
    const provider = await signIn('provider.a');
    const episode = freeCase(provider.tenantId);

    const problem = await refusal(
      createStay(provider, {
        caseId: episode.id,
        providerOrganizationId: episode.providerOrganizationId!,
        admissionAt: iso(-4),
        estimatedDays: 3,
      }),
    );
    expect(problem.status).toBe(422);
    expect(problem.code).toBe('ADMISSION_DATE_OUT_OF_WINDOW');

    const accepted = await createStay(provider, {
      caseId: episode.id,
      providerOrganizationId: episode.providerOrganizationId!,
      admissionAt: iso(-3),
      estimatedDays: 3,
    });
    expect(accepted.data.status).toBe('REQUESTED');
    // A new admission is never authorized by the endpoint that asks for it: the reviewer
    // decides the request, and the stay follows.
    expect(accepted.data.authorizationId).toBeNull();
    expect(accepted.data.authorizedDays).toBeNull();
  });

  it('accepts thirty days forward and refuses thirty-one', async () => {
    const provider = await signIn('provider.a');
    const episode = freeCase(provider.tenantId);

    const problem = await refusal(
      createStay(provider, {
        caseId: episode.id,
        providerOrganizationId: episode.providerOrganizationId!,
        admissionAt: iso(31),
        estimatedDays: 2,
      }),
    );
    expect(problem.code).toBe('ADMISSION_DATE_OUT_OF_WINDOW');

    const accepted = await createStay(provider, {
      caseId: episode.id,
      providerOrganizationId: episode.providerOrganizationId!,
      admissionAt: iso(30),
      estimatedDays: 2,
    });
    expect(accepted.response.status).toBe(201);
  });
});

describe('one open stay per case and provider', () => {
  it('refuses a second stay at the same provider and allows one at another', async () => {
    const provider = await signIn('provider.a');
    const stay = admittedStay();

    const problem = await refusal(
      createStay(provider, {
        caseId: stay.caseId,
        providerOrganizationId: stay.providerOrganizationId,
        admissionAt: iso(0),
        estimatedDays: 2,
      }),
    );
    expect(problem.status).toBe(409);
    expect(problem.code).toBe('INPATIENT_STAY_ALREADY_OPEN');

    // The rule is one open stay per case *and provider*. A transfer to another hospital is a
    // second admission, and a rule that forbade it would make a transfer unrecordable.
    const other = api.world.providers.find(
      (p) => p.tenantOrganizationId !== stay.providerOrganizationId,
    );
    if (other) {
      const accepted = await createStay(provider, {
        caseId: stay.caseId,
        providerOrganizationId: other.tenantOrganizationId,
        admissionAt: iso(0),
        estimatedDays: 2,
      });
      expect(accepted.response.status).toBe(201);
    }
  });

  it('lets a cancelled stay be replaced', async () => {
    const provider = await signIn('provider.a');
    const stay = admittedStay();
    await unwrap(
      provider.c.POST('/api/v1/inpatient-stays/{stayId}/cancel', {
        params: {
          header: {
            ...tenant(provider),
            'Idempotency-Key': key(),
            'If-Match': await etagOf(provider, stay.id),
          },
          path: { stayId: stay.id },
        },
        body: { reasonCode: 'ADMISSION_NOT_NEEDED' },
      }),
    );
    const replacement = await createStay(provider, {
      caseId: stay.caseId,
      providerOrganizationId: stay.providerOrganizationId,
      admissionAt: iso(0),
      estimatedDays: 2,
    });
    expect(replacement.response.status).toBe(201);
  });
});

describe('the extension gate', () => {
  it('refuses a second extension while one is undecided and accepts it once decided', async () => {
    const provider = await signIn('provider.a');
    const stay = admittedStay();
    const extend = async () =>
      unwrap(
        provider.c.POST('/api/v1/inpatient-stays/{stayId}/extensions', {
          params: {
            header: {
              ...tenant(provider),
              'Idempotency-Key': key(),
              'If-Match': await etagOf(provider, stay.id),
            },
            path: { stayId: stay.id },
          },
          body: { additionalDays: 2, reasonCode: 'COMPLICATION' },
        }),
      );

    const first = await extend();
    expect(first.data.extensions.filter((e) => e.status === 'REQUESTED')).toHaveLength(1);

    const problem = await refusal(extend());
    expect(problem.status).toBe(409);
    expect(problem.code).toBe('STAY_EXTENSION_PENDING');
    expect(extensionsOf(stay.id).filter((e) => e.status === 'REQUESTED')).toHaveLength(1);

    // A reviewer decides it on the request page; the mock has no endpoint for that, so the
    // decision is applied where the outbox subscription would apply it.
    const pending = extensionsOf(stay.id).find((e) => e.status === 'REQUESTED')!;
    pending.status = 'APPROVED';
    const second = await extend();
    expect(second.data.extensions.filter((e) => e.status === 'REQUESTED')).toHaveLength(1);
    expect(extensionsOf(stay.id)).toHaveLength(3);
  });

  it('refuses an extension of a stay nobody has decided', async () => {
    const provider = await signIn('provider.a');
    const episode = freeCase(provider.tenantId);
    const created = await createStay(provider, {
      caseId: episode.id,
      providerOrganizationId: episode.providerOrganizationId!,
      admissionAt: iso(0),
      estimatedDays: 3,
    });
    const problem = await refusal(
      unwrap(
        provider.c.POST('/api/v1/inpatient-stays/{stayId}/extensions', {
          params: {
            header: {
              ...tenant(provider),
              'Idempotency-Key': key(),
              'If-Match': created.response.headers.get('ETag')!,
            },
            path: { stayId: created.data.id },
          },
          body: { additionalDays: 1, reasonCode: 'COMPLICATION' },
        }),
      ),
    );
    expect(problem.code).toBe('INPATIENT_STAY_TRANSITION_INVALID');
  });
});

describe('segments', () => {
  const put = async (s: Session, stayId: string, items: unknown[]) =>
    unwrap(
      s.c.PUT('/api/v1/inpatient-stays/{stayId}/segments', {
        params: {
          header: {
            ...tenant(s),
            'Idempotency-Key': key(),
            'If-Match': await etagOf(s, stayId),
          },
          path: { stayId },
        },
        body: { items: items as never },
      }),
    );

  it('refuses two segments that claim the same hours and accepts a transfer', async () => {
    const provider = await signIn('provider.a');
    const stay = admittedStay();
    const start = stay.admissionAt;
    const middle = new Date(Date.parse(start) + DAY_MS).toISOString();
    const end = new Date(Date.parse(start) + 2 * DAY_MS).toISOString();

    const problem = await refusal(
      put(provider, stay.id, [
        { segmentType: 'WARD', startsAt: start, endsAt: end },
        { segmentType: 'ICU', startsAt: middle, endsAt: null },
      ]),
    );
    expect(problem.status).toBe(422);
    expect(problem.errors?.some((e) => e.code === 'OVERLAP')).toBe(true);
    // A refused set is not half written.
    expect(segmentsOf(stay.id)).toHaveLength(2);

    const accepted = await put(provider, stay.id, [
      { segmentType: 'WARD', startsAt: start, endsAt: middle },
      { segmentType: 'ICU', startsAt: middle, endsAt: end },
    ]);
    expect(accepted.data.segments).toHaveLength(2);
  });

  it('lets a companion overlap the patient', async () => {
    const provider = await signIn('provider.a');
    const stay = admittedStay();
    const start = stay.admissionAt;
    const end = new Date(Date.parse(start) + 2 * DAY_MS).toISOString();

    const accepted = await put(provider, stay.id, [
      { segmentType: 'WARD', startsAt: start, endsAt: end },
      { segmentType: 'COMPANION', startsAt: start, endsAt: end },
    ]);
    expect(accepted.data.segments).toHaveLength(2);
    expect(accepted.data.segments.map((g) => g.segmentType).sort()).toEqual(['COMPANION', 'WARD']);
  });
});

describe('discharge and reconciliation', () => {
  const discharge = async (s: Session, stayId: string, dischargeAt: string) =>
    unwrap(
      s.c.POST('/api/v1/inpatient-stays/{stayId}/discharge', {
        params: {
          header: {
            ...tenant(s),
            'Idempotency-Key': key(),
            'If-Match': await etagOf(s, stayId),
          },
          path: { stayId },
        },
        body: { dischargeAt },
      }),
    );

  it('releases the days that were reserved and not used, once', async () => {
    const provider = await signIn('provider.a');
    const stay = admittedStay();
    // Five authorized; three used.
    const dischargeAt = new Date(Date.parse(stay.admissionAt) + 3 * DAY_MS).toISOString();

    const out = await discharge(provider, stay.id, dischargeAt);
    expect(out.data.status).toBe('DISCHARGED');
    expect(out.data.actualDays).toBe('3');
    expect(out.data.releasedDays).toBe('2');
    expect(out.data.overAuthorization).toBe(false);
    // And every open segment was ended at the discharge moment.
    expect(out.data.segments.every((g) => g.endsAt !== null)).toBe(true);

    const recon = await unwrap(
      provider.c.GET('/api/v1/inpatient-stays/{stayId}/reconciliation', {
        params: { header: tenant(provider), path: { stayId: stay.id } },
      }),
    );
    expect(recon.data.authorizedDays).toBe('5');
    expect(recon.data.actualDays).toBe('3');
    expect(recon.data.releasedDays).toBe('2');

    // Running it twice releases nothing twice.
    const problem = await refusal(discharge(provider, stay.id, dischargeAt));
    expect(problem.code).toBe('INPATIENT_STAY_TRANSITION_INVALID');
    expect(api.world.inpatientStays.find((r) => r.id === stay.id)!.releasedDays).toBe('2');
  });

  it('flags an admission that ran over and releases nothing', async () => {
    const provider = await signIn('provider.a');
    const stay = admittedStay();
    const dischargeAt = new Date(Date.parse(stay.admissionAt) + 7 * DAY_MS).toISOString();

    const out = await discharge(provider, stay.id, dischargeAt);
    expect(out.data.actualDays).toBe('7');
    expect(out.data.releasedDays).toBe('0');
    expect(out.data.overAuthorization).toBe(true);
  });

  it('counts a same-day admission as one day', async () => {
    const provider = await signIn('provider.a');
    const stay = admittedStay();
    const dischargeAt = new Date(Date.parse(stay.admissionAt) + 6 * 3_600_000).toISOString();

    const out = await discharge(provider, stay.id, dischargeAt);
    expect(out.data.actualDays).toBe('1');
  });

  it('answers a live stay 409 rather than an empty reconciliation', async () => {
    const provider = await signIn('provider.a');
    const stay = admittedStay();
    const problem = await refusal(
      unwrap(
        provider.c.GET('/api/v1/inpatient-stays/{stayId}/reconciliation', {
          params: { header: tenant(provider), path: { stayId: stay.id } },
        }),
      ),
    );
    expect(problem.status).toBe(409);
    expect(problem.code).toBe('INPATIENT_STAY_NOT_DISCHARGED');
  });
});

describe('the projection', () => {
  it('serves the sponsor HR user every figure and never the extension reason text', async () => {
    const hr = await signIn('sponsor.hr');
    const stay = admittedStay();
    const reasonText = extensionsOf(stay.id)[0]!.reasonText!;

    const out = await get(hr, stay.id);
    expect(out.data.projection).toBe('FINANCIAL');
    // Everything a claims reviewer needs to reconcile a bill.
    expect(out.data.authorizedDays).toBe('5');
    expect(out.data.segments).toHaveLength(2);
    expect(out.data.extensions).toHaveLength(1);
    expect(out.data.extensions[0]!.additionalDays).toBe(1);
    // And nothing that says what was wrong with the person. The whole body is scanned,
    // because a field added later would otherwise slip past a property-by-property check.
    const body = JSON.stringify(out.data);
    expect(body).not.toContain(reasonText);
    expect(out.data.extensions[0]!.reasonText).toBeUndefined();
    expect(out.data.admissionDiagnosisId).toBeUndefined();
  });

  it('serves the clinical half to a reviewer that says why', async () => {
    const reviewer = await signIn('doctor.a');
    const stay = admittedStay();
    const reasonText = extensionsOf(stay.id)[0]!.reasonText!;

    const out = await unwrap(
      reviewer.c.GET('/api/v1/inpatient-stays/{stayId}', {
        params: {
          header: { ...tenant(reviewer), 'X-Access-Purpose': 'PRE_AUTHORIZATION' },
          path: { stayId: stay.id },
        },
      }),
    );
    expect(out.data.projection).toBe('CLINICAL');
    expect(out.data.extensions[0]!.reasonText).toBe(reasonText);
  });
});
