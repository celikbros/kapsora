/**
 * End-to-end flows through the typed client against the claim half of the M5 mock world: the
 * submit pipeline, the version model, the per-line decisions by stage, the two projections and
 * invoice readiness.
 *
 * The mock is a test double of the Go server and M5 treats a divergence in either direction as
 * a bug, so every test below is written against a behaviour the server has and a plausible mock
 * would get wrong: the submitted version that answers 409 rather than 412, the line the rules
 * decided at the AUTO stage beside the line that still waits for a person, the over-consumption
 * that moves nothing rather than consuming what was left, the duplicate that names the other
 * claim's reference, and the financial reviewer who cannot reach a claim medical review has not
 * finished.
 *
 * The headline is the same assertion the Go tests make, made here: a sponsor HR user is served
 * every figure of a claim and never a line description, a diagnosis reference, a medical report
 * reference or a medical reviewer's sentence. Remove one line of the projection in
 * claim-handlers.ts and this fails.
 */
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { createKapsoraClient, randomId, type KapsoraClient } from '../client';
import { createOperations } from '../operations';
import { ApiError, unwrap, type Problem } from '../problem';
import { createMockServer } from './node';
import { mockAuthorizations } from './authorization-handlers';
import { currentVersionOf } from './data';

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
const clinical = (s: Session) => ({
  ...tenant(s),
  'X-Access-Purpose': 'CLAIM_REVIEW' as const,
  'X-Access-Reason': 'inceleme',
});

/**
 * Runs a raw client call and returns the problem it was refused with. It unwraps the result
 * itself, so a test reads as "this call is refused, and here is why" rather than as a nest of
 * helpers — and a call that unexpectedly succeeds fails loudly rather than silently.
 */
async function refusal(call: Promise<{ response: Response }>): Promise<Problem> {
  try {
    await unwrap(call as Parameters<typeof unwrap>[0]);
  } catch (error) {
    if (error instanceof ApiError) return error.problem;
    throw error;
  }
  throw new Error('expected the call to be refused');
}

/** A seeded claim found by its state rather than by a generated id. */
function claimIn(status: string) {
  const row = api.world.claims.find((c) => c.status === status);
  if (!row) throw new Error(`fixture: no claim in ${status}`);
  return row;
}

function serviceIdByCode(code: string): string {
  const row = api.world.serviceDefinitions.find((d) => d.code === code);
  if (!row) throw new Error(`fixture: no service ${code}`);
  return row.id;
}

async function getClaim(s: Session, id: string, headers = tenant(s)) {
  return unwrap(
    s.c.GET('/api/v1/claims/{claimId}', { params: { header: headers, path: { claimId: id } } }),
  );
}

async function etagOf(s: Session, id: string): Promise<string> {
  return (await getClaim(s, id)).response.headers.get('ETag')!;
}

type NewLine = {
  lineNo: number;
  serviceDefinitionId: string;
  unitType: string;
  quantity: string;
  lineAmount: string;
  description?: string;
  diagnosisId?: string;
};

/**
 * A day no seeded claim was raised for, so a claim created by a test is not a duplicate of the
 * fixture. The duplicate test makes two claims on this same day, which is the point of it.
 */
function freeServiceDate(): string {
  const seed = claimIn('APPROVED');
  const day = new Date(`${seed.serviceDateFrom}T00:00:00Z`);
  day.setUTCDate(day.getUTCDate() - 1);
  return day.toISOString().slice(0, 10);
}

async function createClaim(s: Session, lines: NewLine[], over: Record<string, unknown> = {}) {
  const seed = claimIn('APPROVED');
  const day = freeServiceDate();
  return unwrap(
    s.c.POST('/api/v1/claims', {
      params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
      body: {
        personId: seed.personId,
        programId: seed.programId,
        enrollmentId: seed.enrollmentId,
        providerOrganizationId: seed.providerOrganizationId,
        caseId: seed.caseId,
        serviceDateFrom: day,
        serviceDateTo: day,
        lines,
        ...over,
      },
    }),
  );
}

async function submit(s: Session, id: string) {
  return unwrap(
    s.c.POST('/api/v1/claims/{claimId}/submit', {
      params: {
        header: { ...tenant(s), 'If-Match': await etagOf(s, id), 'Idempotency-Key': key() },
        path: { claimId: id },
      },
    }),
  );
}

/** A line carrying every clinical field there is, so an absence test proves something. */
function clinicalLine(lineNo: number, code: string, quantity: string, lineAmount: string): NewLine {
  return {
    lineNo,
    serviceDefinitionId: serviceIdByCode(code),
    unitType: code === 'PHYSIO_SESSION' ? 'SESSION' : 'COUNT',
    quantity,
    lineAmount,
    description: 'Sol diz artroskopi sonrası kontrol',
    diagnosisId: api.world.diagnoses[0]!.id,
  };
}

describe('the claim world', () => {
  it('carries a claim in every status a screen has to draw', () => {
    const statuses = new Set(api.world.claims.map((c) => c.status));
    for (const want of [
      'DRAFT',
      'PENDING_MEDICAL',
      'PENDING_FINANCIAL',
      'RETURNED',
      'APPROVED',
      'PARTIALLY_APPROVED',
      'REJECTED',
      'CANCELLED',
    ]) {
      expect(statuses).toContain(want);
    }
  });

  it('carries a returned claim whose superseded version kept its decision', () => {
    const returned = claimIn('RETURNED');
    expect(returned.currentVersionNo).toBe(2);
    const versions = api.world.claimVersions.filter((v) => v.claimId === returned.id);
    const v1 = versions.find((v) => v.versionNo === 1)!;
    const v2 = versions.find((v) => v.versionNo === 2)!;
    expect(v1.status).toBe('SUPERSEDED');
    expect(v2.status).toBe('DRAFT');
    const oldLine = api.world.claimLines.find((l) => l.versionId === v1.id)!;
    const decision = api.world.claimLineDecisions.find((d) => d.lineId === oldLine.id)!;
    expect(decision.decision).toBe('CUT');
    expect(decision.approvedAmount).toBe('400');
    // The correction copied the line, clinical fields and all.
    const copied = api.world.claimLines.find((l) => l.versionId === v2.id)!;
    expect(copied.description).toBe(oldLine.description);
    expect(copied.diagnosisId).toBe(oldLine.diagnosisId);
    expect(api.world.claimLineDecisions.some((d) => d.lineId === copied.id)).toBe(false);
  });
});

describe('the submit pipeline', () => {
  it('decides the priced line at the AUTO stage and leaves the unpriced one for a person', async () => {
    const provider = await signIn('provider.a');
    // GP_VISIT is contracted; LAB_PANEL_AMBIGUOUS deliberately has two equally specific
    // prices, so the ladder cannot choose and the line goes to a person with the reason.
    const created = await createClaim(provider, [
      clinicalLine(1, 'GP_VISIT', '1', '450'),
      clinicalLine(2, 'LAB_PANEL_AMBIGUOUS', '1', '200'),
    ]);
    const submitted = await submit(provider, created.data.id);

    expect(submitted.data.status).toBe('PENDING_FINANCIAL');
    const auto = submitted.data.lines.find((l) => l.lineNo === 1)!;
    expect(auto.decision).not.toBeNull();
    expect(auto.decision!.stage).toBe('AUTO');
    // The rules decided and nobody looked, so no actor is on the row.
    expect(auto.decision!.decidedBy).toBeNull();
    expect(auto.decision!.reasonCode).toBe('AUTO_APPROVED');
    // The halves add up, exactly.
    const d = auto.decision!;
    expect(Number(d.payerAmount) + Number(d.memberAmount)).toBeCloseTo(Number(d.approvedAmount), 6);

    const pending = submitted.data.lines.find((l) => l.lineNo === 2)!;
    expect(pending.decision).toBeNull();
    const exception = submitted.data.exceptions.find((e) => e.lineNo === 2)!;
    expect(exception.code).toBe('PRICE_REVIEW_REQUIRED');
    expect(exception.stage).toBe('FINANCIAL');
  });

  it('raises a duplicate as an exception naming the other claim, and settles a lone claim', async () => {
    const provider = await signIn('provider.a');
    const first = await createClaim(provider, [clinicalLine(1, 'GP_VISIT', '1', '450')]);
    const firstSubmitted = await submit(provider, first.data.id);

    const second = await createClaim(provider, [clinicalLine(1, 'GP_VISIT', '1', '450')]);
    const secondSubmitted = await submit(provider, second.data.id);
    expect(secondSubmitted.data.status).toBe('PENDING_FINANCIAL');
    const exception = secondSubmitted.data.exceptions.find(
      (e) => e.code === 'DUPLICATE_SUSPECTED',
    )!;
    expect(exception).toBeDefined();
    // The other claim's reference: "we think you have already billed this" is only
    // actionable if the provider is told which one.
    expect([firstSubmitted.data.reference, claimIn('APPROVED').reference]).toContain(
      exception.detail,
    );
    expect(exception.stage).toBe('FINANCIAL');
  });

  it('raises an over-consumption as an exception and consumes nothing at all', async () => {
    const provider = await signIn('provider.a');
    const hold = api.world.claimAuthorizations[0]!;
    const item = hold.items[0]!;
    item.consumedQuantity = '2.000000'; // two of four used
    const before = item.consumedQuantity;

    const created = await createClaim(provider, [clinicalLine(1, 'PHYSIO_SESSION', '3', '750')], {
      authorizationId: hold.id,
    });
    const submitted = await submit(provider, created.data.id);

    expect(submitted.data.status).toBe('PENDING_MEDICAL');
    const exception = submitted.data.exceptions.find((e) => e.code === 'AUTHORIZATION_EXCEEDED')!;
    expect(exception).toBeDefined();
    // The figure the hold still had, exactly — "the hold had 2 left", not "too much".
    expect(exception.detail).toBe('2');
    expect(exception.stage).toBe('MEDICAL');
    // Nothing moved. Not three, and not the two that were left.
    expect(item.consumedQuantity).toBe(before);
    expect(submitted.data.lines[0]!.decision).toBeNull();
  });

  it('consumes exactly the claimed quantity when the hold covers it', async () => {
    const provider = await signIn('provider.a');
    const hold = api.world.claimAuthorizations[0]!;
    const item = hold.items[0]!;
    item.consumedQuantity = '0.000000';

    const created = await createClaim(provider, [clinicalLine(1, 'PHYSIO_SESSION', '2', '500')], {
      authorizationId: hold.id,
    });
    await submit(provider, created.data.id);
    expect(item.consumedQuantity).toBe('2');
  });
});

describe('the freeze and the correction', () => {
  it('returns the actual hold draw before consuming the corrected version', async () => {
    let provider = await signIn('provider.a');
    const control = await createClaim(provider, [clinicalLine(1, 'PHYSIO_SESSION', '1', '250')]);
    await submit(provider, control.data.id);
    const hold = api.world.claimAuthorizations[0]!;
    const item = hold.items[0]!;
    item.approvedQuantity = '4';
    item.consumedQuantity = '0';
    const created = await createClaim(provider, [clinicalLine(1, 'PHYSIO_SESSION', '2', '500')], {
      authorizationId: hold.id,
    });
    const pending = await submit(provider, created.data.id);
    expect(pending.data.status).toBe('PENDING_FINANCIAL');
    expect(item.consumedQuantity).toBe('2');
    const reviewer = await signIn('financial.reviewer');
    const returned = await unwrap(
      reviewer.c.POST('/api/v1/claims/{claimId}/return', {
        params: {
          header: {
            ...tenant(reviewer),
            'If-Match': await etagOf(reviewer, created.data.id),
            'Idempotency-Key': key(),
          },
          path: { claimId: created.data.id },
        },
        body: { reasonCode: 'AMOUNT_CORRECTION' },
      }),
    );
    expect(returned.data.currentVersionNo).toBe(2);
    expect(item.consumedQuantity).toBe('0');
    provider = await signIn('provider.a');
    await unwrap(
      provider.c.PUT('/api/v1/claims/{claimId}/lines', {
        params: {
          header: {
            ...tenant(provider),
            'If-Match': await etagOf(provider, created.data.id),
            'Idempotency-Key': key(),
          },
          path: { claimId: created.data.id },
        },
        body: { lines: [clinicalLine(1, 'PHYSIO_SESSION', '1', '250')] },
      }),
    );
    await submit(provider, created.data.id);
    expect(item.consumedQuantity).toBe('1');
  });

  it('refuses to edit a submitted version and opens version n+1 on a return', async () => {
    const provider = await signIn('provider.a');
    const created = await createClaim(provider, [
      clinicalLine(1, 'GP_VISIT', '1', '450'),
      clinicalLine(2, 'LAB_PANEL_AMBIGUOUS', '1', '200'),
    ]);
    const submitted = await submit(provider, created.data.id);

    // 409, not 412: the caller may write claims, and this one has stopped being writable.
    const frozen = await refusal(
      provider.c.PUT('/api/v1/claims/{claimId}/lines', {
        params: {
          header: {
            ...tenant(provider),
            'If-Match': submitted.response.headers.get('ETag')!,
            'Idempotency-Key': key(),
          },
          path: { claimId: created.data.id },
        },
        body: { lines: [clinicalLine(1, 'GP_VISIT', '1', '450')] },
      }),
    );
    expect(frozen.code).toBe('CLAIM_VERSION_FROZEN');
    expect(frozen.status).toBe(409);

    const patchFrozen = await refusal(
      provider.c.PATCH('/api/v1/claims/{claimId}', {
        params: {
          header: {
            ...tenant(provider),
            'If-Match': submitted.response.headers.get('ETag')!,
            'Idempotency-Key': key(),
          },
          path: { claimId: created.data.id },
        },
        body: {
          serviceDateFrom: created.data.serviceDateFrom,
          serviceDateTo: created.data.serviceDateTo,
        },
      }),
    );
    expect(patchFrozen.code).toBe('CLAIM_VERSION_FROZEN');

    // The financial reviewer cuts the line it was sent, then returns the claim.
    const reviewer = await signIn('reviewer.a');
    const cut = await unwrap(
      reviewer.c.POST('/api/v1/claims/{claimId}/line-decisions', {
        params: {
          header: {
            ...tenant(reviewer),
            'If-Match': await etagOf(reviewer, created.data.id),
            'Idempotency-Key': key(),
          },
          path: { claimId: created.data.id },
        },
        body: {
          decisions: [
            {
              lineNo: 2,
              decision: 'CUT',
              approvedQuantity: '1',
              approvedAmount: '150',
              payerAmount: '150',
              memberAmount: '0',
              reasonCode: 'TARIFF_EXCEEDED',
            },
          ],
        },
      }),
    );
    expect(cut.data.lines.find((l) => l.lineNo === 2)!.decision!.decision).toBe('CUT');

    const returned = await unwrap(
      reviewer.c.POST('/api/v1/claims/{claimId}/return', {
        params: {
          header: {
            ...tenant(reviewer),
            'If-Match': cut.response.headers.get('ETag')!,
            'Idempotency-Key': key(),
          },
          path: { claimId: created.data.id },
        },
        body: { reasonCode: 'DOCUMENT_MISSING' },
      }),
    );
    expect(returned.data.status).toBe('RETURNED');
    expect(returned.data.currentVersionNo).toBe(2);

    const versions = await unwrap(
      reviewer.c.GET('/api/v1/claims/{claimId}/versions', {
        params: { header: tenant(reviewer), path: { claimId: created.data.id } },
      }),
    );
    const v1 = versions.data.items.find((v) => v.versionNo === 1)!;
    expect(v1.status).toBe('SUPERSEDED');
    expect(v1.returnReasonCode).toBe('DOCUMENT_MISSING');

    // Version 1 still says exactly what it was decided to say.
    const old = await unwrap(
      reviewer.c.GET('/api/v1/claims/{claimId}/versions/{versionNo}', {
        params: { header: tenant(reviewer), path: { claimId: created.data.id, versionNo: 1 } },
      }),
    );
    const oldLine2 = old.data.lines.find((l) => l.lineNo === 2)!;
    expect(oldLine2.decision!.decision).toBe('CUT');
    expect(oldLine2.decision!.approvedAmount).toBe('150');
    expect(oldLine2.decision!.reasonCode).toBe('TARIFF_EXCEEDED');

    // Version 2 is a draft with the lines copied and nothing decided.
    const fresh = await unwrap(
      reviewer.c.GET('/api/v1/claims/{claimId}/versions/{versionNo}', {
        params: { header: tenant(reviewer), path: { claimId: created.data.id, versionNo: 2 } },
      }),
    );
    expect(fresh.data.version.status).toBe('DRAFT');
    expect(fresh.data.lines).toHaveLength(2);
    expect(fresh.data.lines.every((l) => l.decision === null)).toBe(true);
  });
});

describe('the two review stages', () => {
  it('refuses a financial decision while the claim is still in medical review', async () => {
    const medical = claimIn('PENDING_MEDICAL');
    const reviewer = await signIn('reviewer.a'); // holds claim.financial.review only
    const line = api.world.claimLines.find(
      (l) => l.versionId === api.world.claimVersions.find((v) => v.claimId === medical.id)!.id,
    )!;

    const refused = await refusal(
      reviewer.c.POST('/api/v1/claims/{claimId}/line-decisions', {
        params: {
          header: {
            ...tenant(reviewer),
            'If-Match': await etagOf(reviewer, medical.id),
            'Idempotency-Key': key(),
          },
          path: { claimId: medical.id },
        },
        body: {
          decisions: [
            {
              lineNo: line.lineNo,
              decision: 'APPROVED',
              approvedQuantity: line.quantity,
              approvedAmount: line.lineAmount,
              payerAmount: line.lineAmount,
              memberAmount: '0',
              reasonCode: 'WITHIN_TARIFF',
            },
          ],
        },
      }),
    );
    expect(refused.code).toBe('CLAIM_STAGE_MISMATCH');
    expect(refused.status).toBe(409);
  });

  it('moves the claim on to financial review once medical review has decided every line', async () => {
    const medical = claimIn('PENDING_MEDICAL');
    const doctor = await signIn('doctor.a');
    const version = api.world.claimVersions.find((v) => v.claimId === medical.id)!;
    const lines = api.world.claimLines.filter((l) => l.versionId === version.id);
    expect(version.financialRequired).toBe(true);

    const decided = await unwrap(
      doctor.c.POST('/api/v1/claims/{claimId}/line-decisions', {
        params: {
          header: {
            ...clinical(doctor),
            'If-Match': await etagOf(doctor, medical.id),
            'Idempotency-Key': key(),
          },
          path: { claimId: medical.id },
        },
        body: {
          decisions: lines.map((l) => ({
            lineNo: l.lineNo,
            decision: 'APPROVED' as const,
            approvedQuantity: l.quantity,
            approvedAmount: l.lineAmount,
            payerAmount: l.lineAmount,
            memberAmount: '0',
            reasonCode: 'MEDICALLY_NECESSARY',
            reasonText: 'Menisküs onarımı sonrası endikasyon uygundur.',
          })),
          reviewComment: 'Ameliyat notu ve MR bulguları ile uyumlu.',
        },
      }),
    );
    expect(decided.data.status).toBe('PENDING_FINANCIAL');
    // The doctor sees their own words back.
    expect(decided.data.lines[0]!.decision!.reasonText).toBe(
      'Menisküs onarımı sonrası endikasyon uygundur.',
    );
    expect(decided.data.reviewCommentMedical).toBe('Ameliyat notu ve MR bulguları ile uyumlu.');

    // The financial reviewer sees the money and none of the clinical detail.
    const reviewer = await signIn('reviewer.a');
    const financial = await getClaim(reviewer, medical.id);
    expect(financial.data.projection).toBe('FINANCIAL');
    expect(financial.data.reviewCommentMedical).toBeUndefined();
    for (const line of financial.data.lines) {
      expect(line.description).toBeUndefined();
      expect(line.diagnosisId).toBeUndefined();
      expect(line.medicalReportId).toBeUndefined();
      // The reason code survives; the reviewer's own sentence does not.
      expect(line.decision!.reasonCode).toBe('MEDICALLY_NECESSARY');
      expect(line.decision!.reasonText).toBeUndefined();
      expect(line.decision!.approvedAmount).not.toBe('');
    }
  });

  it('keeps both rows when a line is decided twice, and the latest is the decision', async () => {
    const pending = claimIn('PENDING_FINANCIAL');
    const reviewer = await signIn('reviewer.a');
    const version = api.world.claimVersions.find((v) => v.claimId === pending.id)!;
    const line = api.world.claimLines.find((l) => l.versionId === version.id)!;
    const before = api.world.claimLineDecisions.filter((d) => d.lineId === line.id).length;

    const decide = async (reasonCode: string, approved: string) =>
      unwrap(
        reviewer.c.POST('/api/v1/claims/{claimId}/line-decisions', {
          params: {
            header: {
              ...tenant(reviewer),
              'If-Match': await etagOf(reviewer, pending.id),
              'Idempotency-Key': key(),
            },
            path: { claimId: pending.id },
          },
          body: {
            decisions: [
              {
                lineNo: line.lineNo,
                decision: 'CUT' as const,
                approvedQuantity: line.quantity,
                approvedAmount: approved,
                payerAmount: approved,
                memberAmount: '0',
                reasonCode,
              },
            ],
          },
        }),
      );

    await decide('TARIFF_EXCEEDED', '300');
    const second = await decide('APPEAL_UPHELD', '450');
    expect(second.data.lines[0]!.decision!.reasonCode).toBe('APPEAL_UPHELD');
    expect(api.world.claimLineDecisions.filter((d) => d.lineId === line.id)).toHaveLength(
      before + 2,
    );
  });

  it('refuses a decision whose halves do not add up', async () => {
    const pending = claimIn('PENDING_FINANCIAL');
    const reviewer = await signIn('reviewer.a');
    const version = api.world.claimVersions.find((v) => v.claimId === pending.id)!;
    const line = api.world.claimLines.find((l) => l.versionId === version.id)!;

    const refused = await refusal(
      reviewer.c.POST('/api/v1/claims/{claimId}/line-decisions', {
        params: {
          header: {
            ...tenant(reviewer),
            'If-Match': await etagOf(reviewer, pending.id),
            'Idempotency-Key': key(),
          },
          path: { claimId: pending.id },
        },
        body: {
          decisions: [
            {
              lineNo: line.lineNo,
              decision: 'APPROVED',
              approvedQuantity: line.quantity,
              approvedAmount: '450',
              payerAmount: '360',
              memberAmount: '89', // one lira short
              reasonCode: 'WITHIN_TARIFF',
            },
          ],
        },
      }),
    );
    expect(refused.code).toBe('VALIDATION_FAILED');
    expect(refused.errors?.some((e) => e.code === 'SPLIT')).toBe(true);
  });
});

describe('invoice readiness', () => {
  it('answers ready with totals that equal the sum of the line decisions', async () => {
    const approved = claimIn('APPROVED');
    const reviewer = await signIn('reviewer.a');
    const readiness = await unwrap(
      reviewer.c.GET('/api/v1/claims/{claimId}/invoice-readiness', {
        params: { header: tenant(reviewer), path: { claimId: approved.id } },
      }),
    );
    expect(readiness.data.ready).toBe(true);
    expect(readiness.data.blockers).toEqual([]);
    // 450 + 500 approved, 360 + 500 to the payer, 90 + 0 to the member.
    expect(readiness.data.approvedTotal).toBe('950');
    expect(readiness.data.payerTotal).toBe('860');
    expect(readiness.data.memberTotal).toBe('90');
    expect(Number(readiness.data.payerTotal) + Number(readiness.data.memberTotal)).toBe(
      Number(readiness.data.approvedTotal),
    );
    expect(readiness.data.decidedLineCount).toBe(readiness.data.lineCount);
  });

  it('names the blocker when the provider has no tax identity', async () => {
    const approved = claimIn('APPROVED');
    const relationship = api.world.relationships.find(
      (r) => r.id === approved.providerOrganizationId,
    )!;
    const organization = api.world.organizations.get(relationship.organizationId)!;
    // The fixture really carries one, so clearing it is the difference between ready and not.
    expect(organization.taxNumber).toBeTruthy();
    delete organization.taxNumber;

    const reviewer = await signIn('reviewer.a');
    const readiness = await unwrap(
      reviewer.c.GET('/api/v1/claims/{claimId}/invoice-readiness', {
        params: { header: tenant(reviewer), path: { claimId: approved.id } },
      }),
    );
    expect(readiness.data.ready).toBe(false);
    expect(readiness.data.blockers).toEqual(['PROVIDER_TAX_IDENTITY_MISSING']);
  });

  it('refuses the question about a claim nobody has decided', async () => {
    const draft = claimIn('DRAFT');
    const provider = await signIn('provider.a');
    const refused = await refusal(
      provider.c.GET('/api/v1/claims/{claimId}/invoice-readiness', {
        params: { header: tenant(provider), path: { claimId: draft.id } },
      }),
    );
    expect(refused.code).toBe('CLAIM_NOT_DECIDED');
    expect(refused.status).toBe(409);
  });
});

describe('the sponsor HR scan', () => {
  it('sees every figure of a claim that has all five clinical fields, and none of them', async () => {
    // The claim really carries them: the doctor reads it first, and what they read is what
    // the sponsor's HR user must not.
    const rejected = claimIn('REJECTED');
    const doctor = await signIn('doctor.a');
    const clinicalView = await getClaim(doctor, rejected.id, clinical(doctor));
    expect(clinicalView.data.projection).toBe('CLINICAL');
    const clinicalLineRow = clinicalView.data.lines[0]!;
    expect(clinicalLineRow.description).toBeTruthy();
    expect(clinicalLineRow.diagnosisId).toBeTruthy();
    expect(clinicalLineRow.decision!.stage).toBe('MEDICAL');
    expect(clinicalLineRow.decision!.reasonText).toBeTruthy();

    const hr = await signIn('sponsor.hr');
    const single = await getClaim(hr, rejected.id);
    const page = await unwrap(
      hr.c.GET('/api/v1/claims', { params: { header: tenant(hr), query: { limit: 50 } } }),
    );
    expect(page.data.items.length).toBeGreaterThan(0);

    for (const view of [single.data, ...page.data.items]) {
      expect(view.projection).toBe('FINANCIAL');
      expect(view.reviewCommentMedical).toBeUndefined();
      for (const line of view.lines) {
        expect(line.description).toBeUndefined();
        expect(line.diagnosisId).toBeUndefined();
        expect(line.medicalReportId).toBeUndefined();
        if (line.decision?.stage === 'MEDICAL') {
          expect(line.decision.reasonText).toBeUndefined();
        }
        // The money is not clinical, and hiding it would make the screen useless.
        expect(line.lineAmount).toBeTruthy();
        expect(line.quantity).toBeTruthy();
      }
    }
  });

  it('drops a report exception from the financial projection and keeps a duplicate one', async () => {
    const pending = claimIn('PENDING_MEDICAL');
    const version = api.world.claimVersions.find((v) => v.claimId === pending.id)!;
    version.exceptions = [
      { lineNo: 1, code: 'REPORT_NOT_APPROVED', stage: 'MEDICAL', detail: 'MR-2026-0001' },
      { lineNo: 1, code: 'DUPLICATE_SUSPECTED', stage: 'FINANCIAL', detail: 'CLM-OTHER' },
    ];

    const doctor = await signIn('doctor.a');
    const clinicalView = await getClaim(doctor, pending.id, clinical(doctor));
    expect(clinicalView.data.exceptions.map((e) => e.code)).toEqual([
      'REPORT_NOT_APPROVED',
      'DUPLICATE_SUSPECTED',
    ]);

    const hr = await signIn('sponsor.hr');
    const financialView = await getClaim(hr, pending.id);
    // That a line leans on a treatment report at all is a fact about the patient.
    expect(financialView.data.exceptions.map((e) => e.code)).toEqual(['DUPLICATE_SUSPECTED']);
    expect(financialView.data.exceptions[0]!.detail).toBe('CLM-OTHER');
  });

  it('writes an access event for the clinical read and none for the financial one', async () => {
    const before = api.world.healthAccessEvents.filter((e) => e.resourceType === 'CLAIM').length;
    const hr = await signIn('sponsor.hr');
    await getClaim(hr, claimIn('APPROVED').id);
    expect(api.world.healthAccessEvents.filter((e) => e.resourceType === 'CLAIM')).toHaveLength(
      before,
    );

    const doctor = await signIn('doctor.a');
    await getClaim(doctor, claimIn('APPROVED').id, clinical(doctor));
    const events = api.world.healthAccessEvents.filter((e) => e.resourceType === 'CLAIM');
    expect(events.length).toBe(before + 1);
    expect(events[events.length - 1]!.outcome).toBe('SUCCESS');
    expect(events[events.length - 1]!.purposeCode).toBe('CLAIM_REVIEW');
  });
});

it('financial case handoff preserves private links, scopes sources and replays one draft', async () => {
  const s = await signIn('billing.a');
  const world = api.world;
  const original = world.healthCases.find(
    (c) => c.tenantId === s.tenantId && c.caseType === 'OUTPATIENT',
  )!;
  const request = world.serviceRequests.find(
    (r) =>
      r.tenantId === s.tenantId && r.providerOrganizationId === original.providerOrganizationId,
  )!;
  request.status = 'APPROVED';
  const item = currentVersionOf(world, request)!.items[0]!;
  const row = {
    ...original,
    id: world.nextId(),
    personId: request.personId,
    programId: request.programId,
    enrollmentId: request.enrollmentId,
    serviceRequestId: request.id,
  };
  world.healthCases.push(row);
  const encounter = {
    ...world.encounters[0]!,
    id: world.nextId(),
    tenantId: s.tenantId,
    caseId: row.id,
    endedAt: new Date().toISOString(),
  };
  world.encounters.push(encounter);
  const diagnosis = {
    ...world.diagnoses[0]!,
    id: world.nextId(),
    tenantId: s.tenantId,
    encounterId: encounter.id,
    diagnosisType: 'PRIMARY' as const,
  };
  world.diagnoses.push(diagnosis);
  const report = {
    ...world.medicalReports[0]!,
    id: world.nextId(),
    tenantId: s.tenantId,
    caseId: row.id,
    personId: row.personId,
    issuingProviderOrganizationId: row.providerOrganizationId,
    status: 'APPROVED' as const,
    validFrom: request.serviceDate,
    validTo: request.serviceDate,
  };
  world.medicalReports.push(report);
  world.medicalReportServices.push({
    id: world.nextId(),
    tenantId: s.tenantId,
    reportId: report.id,
    serviceDefinitionId: item.serviceDefinitionId,
    coveredQuantity: '2',
    coveredAmount: null,
    currencyCode: null,
    notes: null,
  });
  const authId = world.nextId();
  const now = new Date().toISOString();
  mockAuthorizations(world).push({
    id: authId,
    tenantId: s.tenantId,
    requestId: request.id,
    reference: 'AUT-HANDOFF',
    status: 'ACTIVE',
    validFrom: now,
    validTo: new Date(Date.now() + 86400000).toISOString(),
    approvedAt: now,
    approvedBy: s.actorId,
    createdAt: now,
    rowVersion: 1,
    consumedTotal: '0',
    reservedTotal: '2',
    vouchers: [],
    items: [
      {
        id: world.nextId(),
        requestItemId: item.id,
        serviceDefinitionId: item.serviceDefinitionId,
        approvedQuantity: '2',
        consumedQuantity: '0',
        memberAmount: '0',
        entitlementReservationId: world.nextId(),
      },
    ],
  });
  const ops = createOperations(s.c);
  expect(
    (await ops.claims.listCaseSources(s.tenantId)).items.some((c) => c.caseId === row.id),
  ).toBe(true);
  const detail = await ops.claims.getCaseSource(s.tenantId, row.id);
  expect(JSON.stringify(detail.data)).not.toContain(report.id);
  expect(JSON.stringify(detail.data)).not.toContain(diagnosis.id);
  const body = {
    lines: [{ serviceDefinitionId: item.serviceDefinitionId, quantity: '1', lineAmount: '400.50' }],
  };
  const command = key();
  const created = await ops.claims.createFromCase(s.tenantId, row.id, body, detail.etag, command);
  const replay = await ops.claims.createFromCase(s.tenantId, row.id, body, detail.etag, command);
  expect(replay).toEqual(created);
  expect(created.data.projection).toBe('FINANCIAL');
  expect(JSON.stringify(created.data)).not.toContain(report.id);
  await expect(
    ops.claims.createFromCase(
      s.tenantId,
      row.id,
      { lines: [{ ...body.lines[0]!, lineAmount: '500' }] },
      detail.etag,
      command,
    ),
  ).rejects.toMatchObject({ problem: { status: 409 } });
  await expect(
    ops.claims.createFromCase(s.tenantId, row.id, body, detail.etag, key()),
  ).rejects.toMatchObject({ problem: { code: 'CLAIM_CASE_ALREADY_CLAIMED' } });
  const version = world.claimVersions.find((v) => v.claimId === created.data.id)!;
  const saved = await unwrap(
    s.c.PUT('/api/v1/claims/{claimId}/lines', {
      params: {
        path: { claimId: created.data.id },
        header: { ...tenant(s), 'If-Match': created.etag, 'Idempotency-Key': key() },
      },
      body: {
        lines: [
          {
            lineNo: 1,
            serviceDefinitionId: item.serviceDefinitionId,
            unitType: item.unitType,
            quantity: '2',
            lineAmount: '500',
          },
        ],
      },
    }),
  );
  expect(saved.response.status).toBe(200);
  expect(world.claimLines.find((l) => l.versionId === version.id)).toMatchObject({
    diagnosisId: diagnosis.id,
    medicalReportId: report.id,
    quantity: '2',
  });
  expect(
    (await ops.claims.listCaseSources(s.tenantId)).items.some((c) => c.caseId === row.id),
  ).toBe(false);
  const outsider = {
    ...row,
    id: world.nextId(),
    providerOrganizationId: world.relationships.find(
      (r) =>
        r.tenantId === s.tenantId &&
        r.relationshipRole === 'PROVIDER' &&
        r.id !== row.providerOrganizationId,
    )!.id,
  };
  world.healthCases.push(outsider);
  await expect(ops.claims.getCaseSource(s.tenantId, outsider.id)).rejects.toMatchObject({
    problem: { status: 404 },
  });
});
