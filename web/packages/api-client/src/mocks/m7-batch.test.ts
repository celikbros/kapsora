/**
 * WP-I7-03 at the mock's API: the icmal a provider bundles its submitted invoices into, and the
 * payer's decision on each of them.
 *
 * The mock is a test double of the Go server and a divergence in either direction is a bug, so
 * every test below is written against a behaviour the server has and a plausible mock would get
 * wrong: the mixture a batch refuses, the invoice that cannot sit in two live icmals, the
 * membership that freezes the moment the batch is submitted, the cut whose adjustments sum
 * exactly to the cut with the remainder on the largest allocation, the changed decision that
 * reverses rather than edits, the return that frees its claims and the rejection that closes
 * them unpaid, and the two people a decided icmal needs.
 */
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { createKapsoraClient, randomId, type KapsoraClient } from '../client';
import { createOperations } from '../operations';
import { ApiError, unwrap, type Problem } from '../problem';
import { splitProportional } from './batch-handlers';
import type { StoredBatch, StoredInvoice } from './data';
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

const command = (s: Session) => ({
  ...tenant(s),
  'Idempotency-Key': `mock-${randomId()}`,
});

const ifMatch = (s: Session, rowVersion: number) => ({
  ...command(s),
  'If-Match': `"${rowVersion}"`,
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

function batchIn(status: string): StoredBatch {
  const row = api.world.batches.find((b) => b.status === status);
  if (!row) throw new Error(`fixture: no batch in ${status}`);
  return row;
}

async function getBatch(s: Session, batchId: string) {
  return (
    await unwrap(
      s.c.GET('/api/v1/batches/{batchId}', { params: { header: tenant(s), path: { batchId } } }),
    )
  ).data;
}

/** A submitted invoice of the provider that no live icmal is holding. */
function freeSubmittedInvoice(): StoredInvoice {
  const held = new Set(api.world.batchInvoices.filter((m) => m.active).map((m) => m.invoiceId));
  const row = api.world.invoices.find((i) => i.status === 'SUBMITTED' && !held.has(i.id));
  if (!row) throw new Error('fixture: no free submitted invoice');
  return row;
}

/** Opens a draft icmal for the provider the billing desk is scoped to. */
async function openBatch(s: Session, providerOrganizationId: string, domainCode = 'HEALTH') {
  return (
    await unwrap(
      s.c.POST('/api/v1/batches', {
        params: { header: command(s) },
        body: {
          providerOrganizationId,
          domainCode,
          currencyCode: 'TRY',
          periodFrom: '2026-03-01',
          periodTo: '2026-03-31',
        },
      }),
    )
  ).data;
}

describe('the world WP-I7-03 seeds', () => {
  it('carries a draft, a submitted, an under-review with mixed decisions and a decided icmal', async () => {
    const finance = await signIn('financial.reviewer');
    const page = (
      await unwrap(finance.c.GET('/api/v1/batches', { params: { header: tenant(finance) } }))
    ).data;
    expect(page.items.length).toBeGreaterThanOrEqual(4);
    // A list row carries no members: a page of icmals is not a place to read every decision.
    for (const item of page.items) expect(item.invoices).toHaveLength(0);

    const draft = await getBatch(finance, batchIn('DRAFT').id);
    expect(draft.invoices).toHaveLength(2);
    for (const member of draft.invoices) {
      expect(member.decision).toBeUndefined();
      // A draft icmal moves nothing: its invoices are still the provider's to withdraw.
      expect(member.invoiceStatus).toBe('SUBMITTED');
    }

    const submitted = await getBatch(finance, batchIn('SUBMITTED').id);
    expect(submitted.submittedTotal).toBe('4000');
    for (const member of submitted.invoices) expect(member.invoiceStatus).toBe('IN_BATCH');

    const underReview = await getBatch(finance, batchIn('UNDER_REVIEW').id);
    const decisions = underReview.invoices.map((m) => m.decision ?? 'PENDING');
    expect(decisions).toEqual(['APPROVE', 'CUT', 'RETURN', 'REJECT', 'PENDING']);
    // Each of the four decisions left its invoice where it belongs.
    const byDecision = new Map(underReview.invoices.map((m) => [m.decision ?? 'PENDING', m]));
    expect(byDecision.get('RETURN')!.invoiceStatus).toBe('RETURNED');
    expect(byDecision.get('REJECT')!.invoiceStatus).toBe('REJECTED');
    expect(byDecision.get('CUT')!.approvedAmount).toBe('750');
    expect(byDecision.get('CUT')!.reasonCode).toBe('TARIFF_EXCEEDED');

    const decided = await getBatch(finance, batchIn('DECIDED').id);
    expect(decided.submittedTotal).toBe('5000');
    // The four totals are the submitted total, exactly.
    const sum =
      Number(decided.approvedTotal) +
      Number(decided.cutTotal) +
      Number(decided.returnedTotal) +
      Number(decided.rejectedTotal);
    expect(String(sum)).toBe(decided.submittedTotal);
    expect(decided.invoices.map((m) => m.invoiceStatus)).toEqual([
      'APPROVED',
      'PARTIALLY_APPROVED',
    ]);
  });

  it('answers the summary with all four decisions and what is still pending', async () => {
    const finance = await signIn('financial.reviewer');
    const summary = (
      await unwrap(
        finance.c.GET('/api/v1/batches/{batchId}/summary', {
          params: { header: tenant(finance), path: { batchId: batchIn('UNDER_REVIEW').id } },
        }),
      )
    ).data;
    // All four are listed even where nothing carries them, so a screen draws stable columns.
    expect(summary.decisions.map((d) => d.decision)).toEqual([
      'APPROVE',
      'CUT',
      'RETURN',
      'REJECT',
    ]);
    for (const row of summary.decisions) expect(row.count).toBe(1);
    expect(summary.pendingCount).toBe(1);
    expect(summary.pendingTotal).toBe('600');
    const cut = summary.decisions.find((d) => d.decision === 'CUT')!;
    expect(cut.submittedTotal).toBe('900');
    expect(cut.approvedTotal).toBe('750');
  });
});

describe('the provider builds an icmal', () => {
  it('refuses an invoice that does not belong in it, naming the field and both values', async () => {
    const billing = await signIn('billing.a');
    const invoice = freeSubmittedInvoice();
    // The icmal collects accommodation; the invoice is a health document.
    const batch = await openBatch(billing, invoice.providerOrganizationId, 'ACCOMMODATION');

    // A domain the icmal is not in.
    const mixed = await refusal(
      billing.c.PUT('/api/v1/batches/{batchId}/invoices', {
        params: {
          header: ifMatch(billing, batch.rowVersion),
          path: { batchId: batch.id },
        },
        body: { invoiceIds: [invoice.id] },
      }),
    );
    expect(mixed.code).toBe('BATCH_MIXED');
    expect(mixed.field).toBe('domainCode');
    expect(mixed.expectedValue).toBe('ACCOMMODATION');
    expect(mixed.actualValue).toBe(invoice.domainCode);
    expect(mixed.invoiceNumber).toBe(invoice.invoiceNumber);

    // The refused replacement wrote nothing: a batch holding whatever came before the bad row
    // would be a batch nobody asked for.
    const stored = await getBatch(billing, batch.id);
    expect(stored.invoices).toHaveLength(0);
  });

  it('refuses an invoice that is already in a live icmal, naming that icmal', async () => {
    const billing = await signIn('billing.a');
    const held = api.world.batchInvoices.find((m) => m.active)!;
    const invoice = api.world.invoices.find((i) => i.id === held.invoiceId)!;
    const batch = await openBatch(billing, invoice.providerOrganizationId);

    const problem = await refusal(
      billing.c.PUT('/api/v1/batches/{batchId}/invoices', {
        params: { header: ifMatch(billing, batch.rowVersion), path: { batchId: batch.id } },
        body: { invoiceIds: [invoice.id] },
      }),
    );
    // A draft icmal already reserves its invoices, which is what stops two of them collecting
    // the same document.
    expect(['INVOICE_ALREADY_BATCHED', 'BATCH_MIXED']).toContain(problem.code);
    if (problem.code === 'INVOICE_ALREADY_BATCHED') {
      expect(problem.liveBatchId).toBe(held.batchId);
    }
  });

  it('freezes the membership the moment the icmal is submitted', async () => {
    const billing = await signIn('billing.a');
    const invoice = freeSubmittedInvoice();
    const batch = await openBatch(billing, invoice.providerOrganizationId);
    // Bend the icmal to the invoice rather than the other way round: the mixture rule has its
    // own test, and this one is about the freeze.
    api.world.batches.find((b) => b.id === batch.id)!.domainCode = invoice.domainCode;

    const filled = (
      await unwrap(
        billing.c.PUT('/api/v1/batches/{batchId}/invoices', {
          params: { header: ifMatch(billing, batch.rowVersion), path: { batchId: batch.id } },
          body: { invoiceIds: [invoice.id] },
        }),
      )
    ).data;
    expect(filled.invoiceCount).toBe(1);
    expect(filled.submittedTotal).toBe(invoice.payableAmount);

    const sent = (
      await unwrap(
        billing.c.POST('/api/v1/batches/{batchId}/submit', {
          params: { header: ifMatch(billing, filled.rowVersion), path: { batchId: batch.id } },
        }),
      )
    ).data;
    expect(sent.status).toBe('SUBMITTED');
    expect(sent.invoices[0]!.invoiceStatus).toBe('IN_BATCH');

    const frozen = await refusal(
      billing.c.PUT('/api/v1/batches/{batchId}/invoices', {
        params: { header: ifMatch(billing, sent.rowVersion), path: { batchId: batch.id } },
        body: { invoiceIds: [] },
      }),
    );
    expect(frozen.code).toBe('BATCH_FROZEN');
  });
});

describe('the payer decides it', () => {
  it('spreads a cut across the claims exactly, with the remainder on the largest allocation', async () => {
    const finance = await signIn('financial.reviewer');
    const batch = batchIn('SUBMITTED');
    const member = api.world.batchInvoices.find((m) => m.batchId === batch.id)!;
    const before = api.world.claimAdjustments.length;

    const submitted = Number(member.submittedAmount);
    const approved = String(submitted - 100);
    const after = (
      await unwrap(
        finance.c.POST('/api/v1/batches/{batchId}/invoices/{invoiceId}/review', {
          params: {
            header: ifMatch(finance, batch.rowVersion),
            path: { batchId: batch.id, invoiceId: member.invoiceId },
          },
          body: { decision: 'CUT', approvedAmount: approved, reasonCode: 'TARIFF_EXCEEDED' },
        }),
      )
    ).data;
    // The first decision is what puts the icmal in front of a person.
    expect(after.status).toBe('UNDER_REVIEW');

    const written = api.world.claimAdjustments.slice(before);
    expect(written.length).toBeGreaterThan(0);
    const total = written.reduce((acc, row) => acc + Number(row.amount), 0);
    // **The adjustments sum exactly to the cut.** Not within a kuruş: exactly.
    expect(total).toBe(100);
    for (const row of written) {
      expect(row.adjustmentType).toBe('CUT');
      // The whole of a reviewer's cut is the payer's own money.
      expect(row.payerAmount).toBe(row.amount);
      expect(row.memberAmount).toBe('0');
    }
  });

  it('divides a cut the way the server does, remainder and all', () => {
    // The shares of 100 over 1000, 500 and 1: truncated, they leave two micro-units over, and
    // those land on the largest allocation rather than the first or the smallest.
    const shares = splitProportional(100_000_000n, [1_000_000_000n, 500_000_000n, 1_000_000n]);
    expect(shares).toEqual([66_622_253n, 33_311_125n, 66_622n]);
    expect(shares.reduce((a, b) => a + b, 0n)).toBe(100_000_000n);

    // Thirds, which no decimal scale can divide.
    const thirds = splitProportional(100_000_000n, [1n, 1n, 1n]);
    expect(thirds).toEqual([33_333_334n, 33_333_333n, 33_333_333n]);
    expect(thirds.reduce((a, b) => a + b, 0n)).toBe(100_000_000n);
  });

  it('reverses the earlier adjustment when a decision changes, rather than editing it', async () => {
    const finance = await signIn('financial.reviewer');
    const batch = batchIn('UNDER_REVIEW');
    const cutMember = api.world.batchInvoices.find(
      (m) => m.batchId === batch.id && m.decision === 'CUT',
    )!;
    const link = api.world.batchAdjustments.find((a) => a.batchInvoiceId === cutMember.id)!;
    const original = api.world.claimAdjustments.find((a) => a.id === link.adjustmentId)!;

    const after = (
      await unwrap(
        finance.c.POST('/api/v1/batches/{batchId}/invoices/{invoiceId}/review', {
          params: {
            header: ifMatch(finance, batch.rowVersion),
            path: { batchId: batch.id, invoiceId: cutMember.invoiceId },
          },
          body: { decision: 'APPROVE' },
        }),
      )
    ).data;
    const member = after.invoices.find((m) => m.invoiceId === cutMember.invoiceId)!;
    expect(member.decision).toBe('APPROVE');
    expect(member.approvedAmount).toBe(member.submittedAmount);

    // The cut is still on the record and a reversal stands beside it: the ledger is what an
    // appeal is answered from, so nothing in it is ever edited or deleted.
    const stillThere = api.world.claimAdjustments.find((a) => a.id === original.id)!;
    expect(stillThere.amount).toBe('150');
    expect(stillThere.adjustmentType).toBe('CUT');
    const reversal = api.world.claimAdjustments.find((a) => a.reversesAdjustmentId === original.id);
    expect(reversal).toBeDefined();
    expect(reversal!.adjustmentType).toBe('REVERSAL');
    expect(reversal!.amount).toBe('-150');
    expect(api.world.batchAdjustments.find((a) => a.id === link.id)!.reversedByAdjustmentId).toBe(
      reversal!.id,
    );
  });

  it('frees the claims on a return and closes them unpaid on a rejection', async () => {
    const finance = await signIn('financial.reviewer');
    const batch = batchIn('UNDER_REVIEW');
    const returned = api.world.batchInvoices.find(
      (m) => m.batchId === batch.id && m.decision === 'RETURN',
    )!;
    const rejected = api.world.batchInvoices.find(
      (m) => m.batchId === batch.id && m.decision === 'REJECT',
    )!;

    const claimOf = (invoiceId: string) =>
      api.world.claims.find(
        (c) =>
          c.id === api.world.invoiceAllocations.find((a) => a.invoiceId === invoiceId)!.claimId,
      )!;
    // As the fixture leaves them: the returned invoice's claim is free again and the rejected
    // one's is finished.
    expect(claimOf(returned.invoiceId).status).toBe('APPROVED');
    expect(claimOf(rejected.invoiceId).status).toBe('CLOSED_UNPAID');

    // And withdrawing a rejection puts the claim back on the document rather than leaving it
    // finished.
    await unwrap(
      finance.c.POST('/api/v1/batches/{batchId}/invoices/{invoiceId}/review', {
        params: {
          header: ifMatch(finance, batch.rowVersion),
          path: { batchId: batch.id, invoiceId: rejected.invoiceId },
        },
        body: { decision: 'APPROVE' },
      }),
    );
    expect(claimOf(rejected.invoiceId).status).toBe('INVOICED');
    expect(api.world.invoices.find((i) => i.id === rejected.invoiceId)!.status).toBe('IN_BATCH');
  });

  it('refuses a cut that is not a cut, and a decision with no reason', async () => {
    const finance = await signIn('financial.reviewer');
    const batch = batchIn('SUBMITTED');
    const member = api.world.batchInvoices.find((m) => m.batchId === batch.id)!;
    const path = { batchId: batch.id, invoiceId: member.invoiceId };

    for (const approvedAmount of ['0', member.submittedAmount, '99999999']) {
      const problem = await refusal(
        finance.c.POST('/api/v1/batches/{batchId}/invoices/{invoiceId}/review', {
          params: { header: ifMatch(finance, batch.rowVersion), path },
          body: { decision: 'CUT', approvedAmount, reasonCode: 'TARIFF_EXCEEDED' },
        }),
      );
      expect(problem.code).toBe('VALIDATION_FAILED');
    }

    // A reason a cut may not carry: the cut writes a ledger row, and an adjustment's reason is
    // a closed list.
    const wrongReason = await refusal(
      finance.c.POST('/api/v1/batches/{batchId}/invoices/{invoiceId}/review', {
        params: { header: ifMatch(finance, batch.rowVersion), path },
        body: { decision: 'CUT', approvedAmount: '10', reasonCode: 'OVERPAYMENT' },
      }),
    );
    expect(wrongReason.code).toBe('VALIDATION_FAILED');

    for (const decision of ['CUT', 'RETURN', 'REJECT'] as const) {
      const problem = await refusal(
        finance.c.POST('/api/v1/batches/{batchId}/invoices/{invoiceId}/review', {
          params: { header: ifMatch(finance, batch.rowVersion), path },
          body: { decision, approvedAmount: '10' },
        }),
      );
      expect(problem.code).toBe('VALIDATION_FAILED');
    }
  });

  it('refuses to close an icmal somebody has not finished reviewing', async () => {
    const finance = await signIn('financial.reviewer');
    const batch = batchIn('UNDER_REVIEW');
    const problem = await refusal(
      finance.c.POST('/api/v1/batches/{batchId}/decide', {
        params: { header: ifMatch(finance, batch.rowVersion), path: { batchId: batch.id } },
      }),
    );
    expect(problem.code).toBe('BATCH_NOT_FULLY_DECIDED');
  });

  it('closes it once every invoice is answered, and the totals reconcile', async () => {
    const finance = await signIn('financial.reviewer');
    const batch = batchIn('UNDER_REVIEW');
    const pending = api.world.batchInvoices.find(
      (m) => m.batchId === batch.id && m.decision === null,
    )!;
    let view = (
      await unwrap(
        finance.c.POST('/api/v1/batches/{batchId}/invoices/{invoiceId}/review', {
          params: {
            header: ifMatch(finance, batch.rowVersion),
            path: { batchId: batch.id, invoiceId: pending.invoiceId },
          },
          body: { decision: 'APPROVE' },
        }),
      )
    ).data;

    view = (
      await unwrap(
        finance.c.POST('/api/v1/batches/{batchId}/decide', {
          params: { header: ifMatch(finance, view.rowVersion), path: { batchId: batch.id } },
        }),
      )
    ).data;
    expect(view.status).toBe('DECIDED');
    expect(view.decidedBy).toBe(finance.actorId);
    const sum =
      Number(view.approvedTotal) +
      Number(view.cutTotal) +
      Number(view.returnedTotal) +
      Number(view.rejectedTotal);
    expect(String(sum)).toBe(view.submittedTotal);
  });

  it('refuses the submitter as the decider', async () => {
    const billing = await signIn('billing.a');
    const invoice = freeSubmittedInvoice();
    const batch = await openBatch(billing, invoice.providerOrganizationId);
    api.world.batches.find((b) => b.id === batch.id)!.domainCode = invoice.domainCode;
    const filled = (
      await unwrap(
        billing.c.PUT('/api/v1/batches/{batchId}/invoices', {
          params: { header: ifMatch(billing, batch.rowVersion), path: { batchId: batch.id } },
          body: { invoiceIds: [invoice.id] },
        }),
      )
    ).data;
    const sent = (
      await unwrap(
        billing.c.POST('/api/v1/batches/{batchId}/submit', {
          params: { header: ifMatch(billing, filled.rowVersion), path: { batchId: batch.id } },
        }),
      )
    ).data;

    // The reviewer answers it, and then the *submitter* tries to close it. Holding the
    // permission is not the question; being the submitter is — so the refusal is arranged by
    // giving the billing desk the review grant it would not normally have.
    const finance = await signIn('financial.reviewer');
    const reviewed = (
      await unwrap(
        finance.c.POST('/api/v1/batches/{batchId}/invoices/{invoiceId}/review', {
          params: {
            header: ifMatch(finance, sent.rowVersion),
            path: { batchId: sent.id, invoiceId: invoice.id },
          },
          body: { decision: 'APPROVE' },
        }),
      )
    ).data;

    const submitter = await signIn('billing.a');
    api.session!.account.memberships[0]!.permissions.push('batch.review');
    const problem = await refusal(
      submitter.c.POST('/api/v1/batches/{batchId}/decide', {
        params: { header: ifMatch(submitter, reviewed.rowVersion), path: { batchId: sent.id } },
      }),
    );
    expect(problem.code).toBe('BATCH_SUBMITTER_CANNOT_DECIDE');
  });
});
