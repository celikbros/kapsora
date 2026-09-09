/**
 * The M7 claim surface through the typed client: the source a claim came from, the adjustment
 * ledger, and the provider's earnings view.
 *
 * The mock is a test double of the Go server and a divergence in either direction is a bug, so
 * every test below is written against a behaviour the server has and a plausible mock would get
 * wrong: the split that has to add up exactly, the reversal that takes its figures off the row
 * it reverses rather than off the request body, the second reversal that is refused, and the
 * invoiced claim that counts towards what a provider earned and not towards what they may still
 * bill.
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

interface Session {
  c: KapsoraClient;
  tenantId: string;
  actorId: string;
}

async function signIn(username: string): Promise<Session> {
  const c = createKapsoraClient({
    baseUrl: BASE,
    csrfToken: () => api.session?.csrfToken ?? null,
  });
  const o = createOperations(c);
  await o.session.login(username, PASSWORD);
  const session = await o.session.get();
  const tenantId = session.activeTenantId ?? (await o.session.tenants())[0]!.id;
  if (!session.activeTenantId) await o.session.switchTenant(tenantId);
  return { c, tenantId, actorId: session.actorId };
}

const tenant = (s: Session) => ({ 'X-Tenant-ID': s.tenantId });

/** A command header: the tenant and a fresh idempotency key, which every POST here needs. */
const command = (s: Session) => ({
  ...tenant(s),
  'Idempotency-Key': `mock-${randomId()}`,
});

async function refusal(call: Promise<{ response: Response }>): Promise<Problem> {
  try {
    await unwrap(call as Parameters<typeof unwrap>[0]);
  } catch (error) {
    if (error instanceof ApiError) return error.problem;
    throw error;
  }
  throw new Error('expected the call to be refused');
}

function claimIn(status: string) {
  const row = api.world.claims.find((c) => c.status === status && c.domainCode === 'HEALTH');
  if (!row) throw new Error(`fixture: no health claim in ${status}`);
  return row;
}

/** The lodging claim the seeded COMPLETED stay produced. */
function lodgingClaim() {
  const row = api.world.claims.find((c) => c.domainCode === 'ACCOMMODATION' && c.sourceId !== null);
  if (!row) throw new Error('fixture: no lodging claim');
  return row;
}

async function readiness(s: Session, claimId: string) {
  return (
    await unwrap(
      s.c.GET('/api/v1/claims/{claimId}/invoice-readiness', {
        params: { header: tenant(s), path: { claimId } },
      }),
    )
  ).data;
}

describe('a claim knows what it came from', () => {
  it('serves the lodging claim of the completed stay with one line per night at the frozen amount', async () => {
    const reviewer = await signIn('reviewer.a');
    const claim = lodgingClaim();
    const body = (
      await unwrap(
        reviewer.c.GET('/api/v1/claims/{claimId}', {
          params: { header: tenant(reviewer), path: { claimId: claim.id } },
        }),
      )
    ).data;

    expect(body.sourceType).toBe('BOOKING');
    expect(body.sourceId).toBe(claim.sourceId);
    expect(body.domainCode).toBe('ACCOMMODATION');
    expect(body.status).toBe('PENDING_FINANCIAL');

    const nights = api.world.bookingNights.filter((n) => n.bookingId === claim.sourceId);
    expect(body.lines).toHaveLength(nights.length);
    // The booking's own frozen figures, copied. A claim that re-priced a night would answer
    // the contract's amount rather than these.
    expect(body.lines.map((l) => l.lineAmount)).toEqual(nights.map((n) => n.unitAmount));
    expect(new Set(body.lines.map((l) => l.unitType))).toEqual(new Set(['NIGHT']));
    // The nights the plan carried are already decided, at the payer amount, by nobody.
    const decided = body.lines.filter((l) => l.decision !== null && l.decision !== undefined);
    expect(decided.length).toBeGreaterThan(0);
    for (const line of decided) {
      expect(line.decision!.stage).toBe('AUTO');
      expect(line.decision!.decidedBy).toBeNull();
      expect(line.decision!.reasonCode).toBe('BOOKING_CONFIRMED');
    }
  });

  it('answers a fee claim through its source rather than through a ledger row', async () => {
    const reviewer = await signIn('reviewer.a');
    const feeClaim = api.world.claims.find(
      (c) => c.domainCode === 'ACCOMMODATION' && c.id !== lodgingClaim().id,
    );
    if (!feeClaim) throw new Error('fixture: no fee claim');
    const rows = (
      await unwrap(
        reviewer.c.GET('/api/v1/claims/{claimId}/adjustments', {
          params: { header: tenant(reviewer), path: { claimId: feeClaim.id } },
        }),
      )
    ).data.items;
    // The provenance is the claim's own source and the line's unit type; the ledger holds
    // only money that moved, and nothing moved beside the fee.
    expect(rows).toHaveLength(0);
    expect(feeClaim.sourceType).toBe('BOOKING');
    const body = (
      await unwrap(
        reviewer.c.GET('/api/v1/claims/{claimId}', {
          params: { header: tenant(reviewer), path: { claimId: feeClaim.id } },
        }),
      )
    ).data;
    expect(body.lines).toHaveLength(1);
    expect(body.lines[0]!.unitType).toBe('NO_SHOW_FEE');
    const booking = api.world.bookings.find((b) => b.id === feeClaim.sourceId);
    expect(booking?.status).toBe('NO_SHOW');
  });

  it('says a health claim came from its case', async () => {
    const reviewer = await signIn('reviewer.a');
    const claim = claimIn('APPROVED');
    const body = (
      await unwrap(
        reviewer.c.GET('/api/v1/claims/{claimId}', {
          params: { header: tenant(reviewer), path: { claimId: claim.id } },
        }),
      )
    ).data;
    expect(body.sourceType).toBe('HEALTH_CASE');
    expect(body.sourceId).toBe(body.caseId);
  });
});

describe('the adjustment ledger', () => {
  it('lists the seeded chain with the reversal after the row it takes back', async () => {
    const reviewer = await signIn('reviewer.a');
    const claim = claimIn('APPROVED');
    const rows = (
      await unwrap(
        reviewer.c.GET('/api/v1/claims/{claimId}/adjustments', {
          params: { header: tenant(reviewer), path: { claimId: claim.id } },
        }),
      )
    ).data.items;

    expect(rows).toHaveLength(2);
    expect(rows[0]!.adjustmentType).toBe('RECOVERY');
    expect(rows[1]!.adjustmentType).toBe('REVERSAL');
    expect(rows[1]!.reversesAdjustmentId).toBe(rows[0]!.id);
    // The pair nets to nothing, so the approved total is where it was.
    const totals = await readiness(reviewer, claim.id);
    expect(totals.adjustmentTotal).toBe('0');
    expect(totals.approvedTotal).toBe(totals.lineTotal);
  });

  it('refuses a split that does not add up, and names the field', async () => {
    const reviewer = await signIn('reviewer.a');
    const claim = claimIn('APPROVED');
    const problem = await refusal(
      reviewer.c.POST('/api/v1/claims/{claimId}/adjustments', {
        params: { header: command(reviewer), path: { claimId: claim.id } },
        body: {
          adjustmentType: 'CUT',
          amount: '100',
          payerAmount: '60',
          memberAmount: '50',
          reasonCode: 'TARIFF_EXCEEDED',
        },
      }),
    );
    expect(problem.status).toBe(422);
    expect(problem.errors?.map((e) => e.field)).toContain('memberAmount');
  });

  it('subtracts a cut from the approved total and gives it back exactly on a reversal', async () => {
    const reviewer = await signIn('reviewer.a');
    const claim = claimIn('APPROVED');
    const before = await readiness(reviewer, claim.id);

    const cut = (
      await unwrap(
        reviewer.c.POST('/api/v1/claims/{claimId}/adjustments', {
          params: { header: command(reviewer), path: { claimId: claim.id } },
          body: {
            adjustmentType: 'CUT',
            amount: '100',
            payerAmount: '80',
            memberAmount: '20',
            reasonCode: 'TARIFF_EXCEEDED',
          },
        }),
      )
    ).data;
    expect(cut.adjustment.sourceType).toBe('REVIEW');
    expect(Number(cut.readiness.approvedTotal)).toBe(Number(before.approvedTotal) - 100);
    expect(Number(cut.readiness.payerTotal) + Number(cut.readiness.memberTotal)).toBe(
      Number(cut.readiness.approvedTotal),
    );

    const reversal = (
      await unwrap(
        reviewer.c.POST('/api/v1/claims/{claimId}/adjustments', {
          params: { header: command(reviewer), path: { claimId: claim.id } },
          body: { reversesAdjustmentId: cut.adjustment.id, reasonCode: 'REVIEW_REVERSED' },
        }),
      )
    ).data;
    // The reversal's figures come off the row it reverses, not off the request.
    expect(reversal.adjustment.amount).toBe('-100');
    expect(reversal.adjustment.payerAmount).toBe('-80');
    expect(reversal.adjustment.memberAmount).toBe('-20');
    expect(reversal.readiness.approvedTotal).toBe(before.approvedTotal);
    expect(reversal.readiness.payerTotal).toBe(before.payerTotal);
    expect(reversal.readiness.memberTotal).toBe(before.memberTotal);

    // One reversal per adjustment, and never a reversal of a reversal.
    const twice = await refusal(
      reviewer.c.POST('/api/v1/claims/{claimId}/adjustments', {
        params: { header: command(reviewer), path: { claimId: claim.id } },
        body: { reversesAdjustmentId: cut.adjustment.id, reasonCode: 'REVIEW_REVERSED' },
      }),
    );
    expect(twice.code).toBe('CLAIM_ADJUSTMENT_REVERSED');
    const chained = await refusal(
      reviewer.c.POST('/api/v1/claims/{claimId}/adjustments', {
        params: { header: command(reviewer), path: { claimId: claim.id } },
        body: { reversesAdjustmentId: reversal.adjustment.id, reasonCode: 'REVIEW_REVERSED' },
      }),
    );
    expect(chained.code).toBe('CLAIM_ADJUSTMENT_NOT_REVERSIBLE');
  });

  it("refuses an adjustment in a currency that is not the claim's, and writes nothing", async () => {
    const reviewer = await signIn('reviewer.a');
    const claim = claimIn('APPROVED');
    const before = (
      await unwrap(
        reviewer.c.GET('/api/v1/claims/{claimId}/adjustments', {
          params: { header: tenant(reviewer), path: { claimId: claim.id } },
        }),
      )
    ).data.items.length;

    const problem = await refusal(
      reviewer.c.POST('/api/v1/claims/{claimId}/adjustments', {
        params: { header: command(reviewer), path: { claimId: claim.id } },
        body: {
          adjustmentType: 'CUT',
          amount: '100',
          payerAmount: '100',
          memberAmount: '0',
          currencyCode: 'EUR',
          reasonCode: 'TARIFF_EXCEEDED',
        },
      }),
    );
    expect(problem.code).toBe('CLAIM_ADJUSTMENT_CURRENCY');

    // Nothing was written, so a caller who mistyped the currency has no row to reverse.
    const after = (
      await unwrap(
        reviewer.c.GET('/api/v1/claims/{claimId}/adjustments', {
          params: { header: tenant(reviewer), path: { claimId: claim.id } },
        }),
      )
    ).data.items.length;
    expect(after).toBe(before);
  });

  it('refuses a reason code outside the closed list and an adjustment on an undecided claim', async () => {
    const reviewer = await signIn('reviewer.a');
    const approved = claimIn('APPROVED');
    const unknownReason = await refusal(
      reviewer.c.POST('/api/v1/claims/{claimId}/adjustments', {
        params: { header: command(reviewer), path: { claimId: approved.id } },
        body: {
          adjustmentType: 'CUT',
          amount: '10',
          payerAmount: '10',
          memberAmount: '0',
          reasonCode: 'BECAUSE_I_SAID_SO',
        },
      }),
    );
    expect(unknownReason.errors?.map((e) => e.field)).toContain('reasonCode');

    const pending = claimIn('PENDING_FINANCIAL');
    const undecided = await refusal(
      reviewer.c.POST('/api/v1/claims/{claimId}/adjustments', {
        params: { header: command(reviewer), path: { claimId: pending.id } },
        body: {
          adjustmentType: 'CUT',
          amount: '10',
          payerAmount: '10',
          memberAmount: '0',
          reasonCode: 'TARIFF_EXCEEDED',
        },
      }),
    );
    expect(undecided.code).toBe('CLAIM_NOT_DECIDED');
  });
});

describe("the provider's earnings", () => {
  it('answers per currency with the invoiceable total the invoice will be checked against', async () => {
    const reviewer = await signIn('reviewer.a');
    const claim = claimIn('APPROVED');
    const earnings = (
      await unwrap(
        reviewer.c.GET('/api/v1/providers/{providerId}/earnings', {
          params: {
            header: tenant(reviewer),
            path: { providerId: claim.providerOrganizationId },
          },
        }),
      )
    ).data;

    expect(earnings.providerOrganizationId).toBe(claim.providerOrganizationId);
    expect(earnings.currencies.length).toBeGreaterThan(0);
    const bucket = earnings.currencies[0]!;
    // The invoiceable total is the approved total of the claims nobody is already collecting.
    // **Both halves matter, and WP-I7-02 added the second one**: the status keeps out a draft
    // and a rejection, and the invoice link keeps out a claim already sitting on a live
    // document — which is still APPROVED, and which a provider offered it twice would put on
    // two invoices.
    const onLiveInvoice = new Set(
      api.world.invoiceAllocations
        .filter((link) => link.tenantId === reviewer.tenantId && link.active)
        .map((link) => link.claimId),
    );
    const billableClaims = api.world.claims.filter(
      (c) =>
        c.tenantId === reviewer.tenantId &&
        c.providerOrganizationId === claim.providerOrganizationId &&
        (c.status === 'APPROVED' || c.status === 'PARTIALLY_APPROVED'),
    );
    expect(billableClaims.length).toBeGreaterThan(0);
    expect(bucket.invoiceableClaimIds).toHaveLength(
      billableClaims.filter((c) => !onLiveInvoice.has(c.id)).length,
    );
    for (const id of bucket.invoiceableClaimIds) {
      expect(onLiveInvoice.has(id)).toBe(false);
    }
    // And the world actually exercises the rule: at least one decided claim is held by a live
    // invoice, so a mock that ignored the link would answer a different figure here.
    expect(billableClaims.some((c) => onLiveInvoice.has(c.id))).toBe(true);
    expect(Number(bucket.invoiceableTotal)).toBeLessThan(Number(bucket.approvedTotal));

    // An adjustment moves the invoiceable total by exactly what it took off.
    await unwrap(
      reviewer.c.POST('/api/v1/claims/{claimId}/adjustments', {
        params: { header: command(reviewer), path: { claimId: claim.id } },
        body: {
          adjustmentType: 'CUT',
          amount: '50',
          payerAmount: '50',
          memberAmount: '0',
          reasonCode: 'TARIFF_EXCEEDED',
        },
      }),
    );
    const after = (
      await unwrap(
        reviewer.c.GET('/api/v1/providers/{providerId}/earnings', {
          params: {
            header: tenant(reviewer),
            path: { providerId: claim.providerOrganizationId },
          },
        }),
      )
    ).data;
    const bucketAfter = after.currencies.find((c) => c.currencyCode === bucket.currencyCode)!;
    expect(Number(bucketAfter.invoiceableTotal)).toBe(Number(bucket.invoiceableTotal) - 50);
  });

  it('counts an invoiced claim towards what was earned and not towards what may still be billed', async () => {
    const reviewer = await signIn('reviewer.a');
    const claim = claimIn('APPROVED');
    const before = (
      await unwrap(
        reviewer.c.GET('/api/v1/providers/{providerId}/earnings', {
          params: {
            header: tenant(reviewer),
            path: { providerId: claim.providerOrganizationId },
          },
        }),
      )
    ).data.currencies[0]!;

    // WP-I7-02 will move it; the fixture moves it directly, which is exactly the state this
    // endpoint has to answer correctly about.
    api.world.claims.find((c) => c.id === claim.id)!.status = 'INVOICED';

    const after = (
      await unwrap(
        reviewer.c.GET('/api/v1/providers/{providerId}/earnings', {
          params: {
            header: tenant(reviewer),
            path: { providerId: claim.providerOrganizationId },
          },
        }),
      )
    ).data.currencies.find((c) => c.currencyCode === before.currencyCode)!;

    expect(after.approvedTotal).toBe(before.approvedTotal);
    expect(Number(after.invoiceableTotal)).toBeLessThan(Number(before.invoiceableTotal));
    expect(after.invoiceableClaimIds).not.toContain(claim.id);
  });

  it('refuses a provider-scoped caller asking about somebody else', async () => {
    const provider = await signIn('provider.a');
    const other = api.world.relationships.find(
      (r) =>
        r.tenantId === provider.tenantId &&
        r.relationshipRole === 'PROVIDER' &&
        !api.world.claims.some((c) => c.providerOrganizationId === r.id),
    );
    if (!other) return;
    const problem = await refusal(
      provider.c.GET('/api/v1/providers/{providerId}/earnings', {
        params: { header: tenant(provider), path: { providerId: other.id } },
      }),
    );
    expect(problem.code).toBe('CLAIM_PROVIDER_SCOPE');
  });
});
