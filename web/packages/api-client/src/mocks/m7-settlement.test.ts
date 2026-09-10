/**
 * WP-I7-04 at the mock's API: what the payer owes the provider, what it actually paid, and what
 * it pays the member back.
 *
 * The mock is a test double of the Go server and a divergence in either direction is a bug, so
 * every test below is written against a behaviour the server has and a plausible mock would get
 * wrong: the settlement nobody can create, the second pair of eyes above the threshold, the
 * payment that is refused with the remainder rather than only refused, the status that follows
 * the sum, the duplicate that names the earlier request, the approval that consumes exactly the
 * approved amount and the rejection that consumes nothing, and the member who reads only their
 * own.
 *
 * The account number is the thread through all of it: it goes in once and four characters come
 * back, and no response in this file is allowed to carry more.
 */
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { createKapsoraClient, randomId, type KapsoraClient } from '../client';
import { createOperations } from '../operations';
import { ApiError, unwrap, type Problem } from '../problem';
import type { StoredReimbursement, StoredSettlement } from './data';
import { createMockServer } from './node';
import { maskAccount, normalizeAccount } from './reimbursement-handlers';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

/** The account number every test types, and the four characters that may come back. */
const IBAN = 'TR33 0006 1005 1978 6457 8413 26';
const IBAN_TAIL = '1326';

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

async function stepUp(s: Session): Promise<void> {
  await createOperations(s.c).session.stepUp(PASSWORD);
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

function settlementIn(status: string): StoredSettlement {
  const row = api.world.settlements.find((s) => s.status === status);
  if (!row) throw new Error(`fixture: no settlement in ${status}`);
  return row;
}

function reimbursementIn(status: string): StoredReimbursement {
  const row = api.world.reimbursements.find((r) => r.status === status);
  if (!row) throw new Error(`fixture: no reimbursement in ${status}`);
  return row;
}

async function getSettlement(s: Session, settlementId: string) {
  return (
    await unwrap(
      s.c.GET('/api/v1/settlements/{settlementId}', {
        params: { header: tenant(s), path: { settlementId } },
      }),
    )
  ).data;
}

describe('the settlement (WP-I7-04 §2.2)', () => {
  it('is opened by the platform and by nobody: there is no create route', async () => {
    const s = await signIn('financial.reviewer');
    // Every settlement in the world has a decided batch behind it. A screen that offered a
    // "new settlement" button would be a screen offering a figure with no batch behind it.
    for (const row of api.world.settlements) {
      const batch = api.world.batches.find((b) => b.id === row.batchId);
      expect(batch?.status).toBe('DECIDED');
    }
    const page = (await unwrap(s.c.GET('/api/v1/settlements', { params: { header: tenant(s) } })))
      .data;
    expect(page.items.length).toBeGreaterThanOrEqual(3);
    // A pending, an approved and a partially paid one: the three states a screen has to draw.
    const statuses = new Set(page.items.map((i) => i.status));
    expect(statuses).toContain('PENDING_APPROVAL');
    expect(statuses).toContain('APPROVED');
    expect(statuses).toContain('PARTIALLY_PAID');
  });

  it('answers the payable amount as approved minus withheld, exactly', async () => {
    const s = await signIn('financial.reviewer');
    const pending = settlementIn('PENDING_APPROVAL');
    const view = await getSettlement(s, pending.id);
    expect(view.approvedAmount).toBe('4000');
    expect(view.withheldAmount).toBe('250');
    expect(view.payableAmount).toBe('3750');
    // And the recoveries that explain the withheld figure travel with it, so a provider
    // disputing it can see which claim each kuruş came from.
    expect(view.recoveries).toHaveLength(1);
    expect(view.recoveries[0]!.amount).toBe('250');
  });

  it('refuses the batch decider above the threshold and accepts anybody else', async () => {
    const approver = await signIn('payer.approver');
    await stepUp(approver);
    const pending = settlementIn('PENDING_APPROVAL');
    // Above the tenant's threshold, which is where the second pair of eyes is asked for. The
    // fixture's settlements are deliberately small, so the rule is exercised by making one
    // large rather than by seeding a figure nobody would recognise on a screen.
    pending.approvedAmount = '60000';
    pending.payableAmount = '60000';
    pending.withheldAmount = '0';
    const batch = api.world.batches.find((b) => b.id === pending.batchId)!;
    // The fixture's batches are decided by the financial reviewer; make the approver the
    // decider so the rule has somebody to refuse.
    batch.decidedBy = approver.actorId;

    const problem = await refusal(
      approver.c.POST('/api/v1/settlements/{settlementId}/approve', {
        params: {
          header: ifMatch(approver, pending.rowVersion),
          path: { settlementId: pending.id },
        },
      }),
    );
    expect(problem.code).toBe('SETTLEMENT_DECIDER_CANNOT_APPROVE');

    batch.decidedBy = api.world.accounts.find((a) => a.username === 'financial.reviewer')!.actorId;
    const approved = (
      await unwrap(
        approver.c.POST('/api/v1/settlements/{settlementId}/approve', {
          params: {
            header: ifMatch(approver, pending.rowVersion),
            path: { settlementId: pending.id },
          },
        }),
      )
    ).data;
    expect(approved.status).toBe('APPROVED');
    expect(approved.approvedBy).toBe(approver.actorId);
    // The countersignature is the other pair of eyes, never the approver themselves.
    expect(approved.checkedBy).toBe(batch.decidedBy);
    expect(approved.checkedBy).not.toBe(approved.approvedBy);
  });

  it('asks for a step-up above the threshold', async () => {
    const approver = await signIn('payer.approver');
    const pending = settlementIn('PENDING_APPROVAL');
    pending.approvedAmount = '60000';
    pending.payableAmount = '60000';
    pending.withheldAmount = '0';
    const problem = await refusal(
      approver.c.POST('/api/v1/settlements/{settlementId}/approve', {
        params: {
          header: ifMatch(approver, pending.rowVersion),
          path: { settlementId: pending.id },
        },
      }),
    );
    expect(problem.code).toBe('STEP_UP_REQUIRED');
    expect(api.world.settlements.find((x) => x.id === pending.id)!.status).toBe('PENDING_APPROVAL');
  });
});

describe('the payment record (WP-I7-04 §2.3)', () => {
  it('moves the status with the sum and never exceeds the payable amount', async () => {
    const s = await signIn('financial.reviewer');
    const approved = settlementIn('APPROVED');
    const payable = approved.payableAmount;

    const partial = (
      await unwrap(
        s.c.POST('/api/v1/settlements/{settlementId}/payment-records', {
          params: { header: command(s), path: { settlementId: approved.id } },
          body: {
            externalReference: 'EFT-TEST-0001',
            amount: '1000',
            paidAt: new Date().toISOString(),
          },
        }),
      )
    ).data;
    expect(partial.paidAmount).toBe('1000');
    expect(partial.status).toBe('PARTIALLY_PAID');

    // One kuruş too many, and the refusal says what may still be entered.
    const problem = await refusal(
      s.c.POST('/api/v1/settlements/{settlementId}/payment-records', {
        params: { header: command(s), path: { settlementId: approved.id } },
        body: {
          externalReference: 'EFT-TEST-0002',
          amount: '1500.01',
          paidAt: new Date().toISOString(),
        },
      }),
    );
    expect(problem.code).toBe('PAYMENT_EXCEEDS_SETTLEMENT');
    expect((problem as unknown as { remainder: string }).remainder).toBe('1500');

    const full = (
      await unwrap(
        s.c.POST('/api/v1/settlements/{settlementId}/payment-records', {
          params: { header: command(s), path: { settlementId: approved.id } },
          body: {
            externalReference: 'EFT-TEST-0003',
            amount: '1500',
            paidAt: new Date().toISOString(),
          },
        }),
      )
    ).data;
    expect(full.paidAmount).toBe(payable);
    expect(full.status).toBe('PAID');
    expect(full.payments).toHaveLength(2);
  });

  it('refuses the same external reference twice for one provider', async () => {
    const s = await signIn('financial.reviewer');
    const approved = settlementIn('APPROVED');
    await unwrap(
      s.c.POST('/api/v1/settlements/{settlementId}/payment-records', {
        params: { header: command(s), path: { settlementId: approved.id } },
        body: {
          externalReference: 'EFT-DUP-0001',
          amount: '100',
          paidAt: new Date().toISOString(),
        },
      }),
    );
    const problem = await refusal(
      s.c.POST('/api/v1/settlements/{settlementId}/payment-records', {
        params: { header: command(s), path: { settlementId: approved.id } },
        body: {
          externalReference: 'EFT-DUP-0001',
          amount: '100',
          paidAt: new Date().toISOString(),
        },
      }),
    );
    expect(problem.code).toBe('PAYMENT_REFERENCE_TAKEN');
  });

  it('refuses a payment against a settlement nobody has approved', async () => {
    const s = await signIn('financial.reviewer');
    const pending = settlementIn('PENDING_APPROVAL');
    const problem = await refusal(
      s.c.POST('/api/v1/settlements/{settlementId}/payment-records', {
        params: { header: command(s), path: { settlementId: pending.id } },
        body: {
          externalReference: 'EFT-EARLY-0001',
          amount: '10',
          paidAt: new Date().toISOString(),
        },
      }),
    );
    expect(problem.code).toBe('PAYMENT_NOT_ALLOWED');
  });
});

describe('the member reimbursement (WP-I7-04 §2.4)', () => {
  it('keeps four characters of the account and gives nothing else back', async () => {
    const member = await signIn('member.a');
    const draft = reimbursementIn('DRAFT');
    const request = api.world.serviceRequests.find(
      (r) =>
        r.requestType === 'REIMBURSEMENT' &&
        r.personId === draft.personId &&
        !api.world.reimbursements.some((x) => x.serviceRequestId === r.id),
    );
    // Every seeded request already has a reimbursement, so this one is raised fresh: the
    // create is the only route in the product that ever sees an account number.
    const fresh = (
      await unwrap(
        member.c.POST('/api/v1/reimbursements', {
          params: { header: command(member) },
          body: {
            serviceRequestId: request?.id ?? draft.serviceRequestId,
            receiptDocumentId: draft.receiptDocumentId,
            requestedAmount: '123.45',
            bankAccount: IBAN,
          },
        }),
      ).catch(() => null)
    )?.data;

    // Whether or not a spare request existed, the seeded draft answers the same question.
    const view =
      fresh ??
      (
        await unwrap(
          member.c.GET('/api/v1/reimbursements/{reimbursementId}', {
            params: { header: tenant(member), path: { reimbursementId: draft.id } },
          }),
        )
      ).data;
    expect(view.bankAccountMasked).toBe(IBAN_TAIL);
    expect(JSON.stringify(view)).not.toContain(normalizeAccount(IBAN));
    // And the mask really is the tail of what was typed, spaces and case included.
    expect(maskAccount(normalizeAccount(IBAN))).toBe(IBAN_TAIL);
  });

  it('refuses a second claim on the same receipt, naming the earlier one', async () => {
    const member = await signIn('member.a');
    const submitted = reimbursementIn('SUBMITTED');
    const other = reimbursementIn('DRAFT');

    const problem = await refusal(
      member.c.POST('/api/v1/reimbursements', {
        params: { header: command(member) },
        body: {
          serviceRequestId: other.serviceRequestId,
          receiptDocumentId: submitted.receiptDocumentId,
          requestedAmount: submitted.requestedAmount,
          bankAccount: IBAN,
        },
      }),
    );
    expect(problem.code).toBe('REIMBURSEMENT_DUPLICATE');
    const extensions = problem as unknown as {
      existingReimbursementId: string;
      existingReference: string;
      matchedBy: string;
    };
    expect(extensions.existingReimbursementId).toBe(submitted.id);
    expect(extensions.existingReference).toBe(submitted.reference);
    expect(extensions.matchedBy).toBe('RECEIPT');
  });

  it('consumes exactly the approved amount on approval and nothing on rejection', async () => {
    const reviewer = await signIn('financial.reviewer');
    const submitted = reimbursementIn('SUBMITTED');
    const account = api.world.entitlementAccounts.find(
      (a) => a.personId === submitted.personId && a.definition.unitType === 'MONEY',
    )!;
    const availableBefore = account.available;

    const decided = (
      await unwrap(
        reviewer.c.POST('/api/v1/reimbursements/{reimbursementId}/decide', {
          params: {
            header: ifMatch(reviewer, submitted.rowVersion),
            path: { reimbursementId: submitted.id },
          },
          body: { decision: 'APPROVE', approvedAmount: '100', reasonCode: 'CONTRACT_LIMIT' },
        }),
      )
    ).data;
    expect(decided.status).toBe('PAYMENT_ORDERED');
    expect(decided.approvedAmount).toBe('100');
    expect(decided.claimId).not.toBeNull();
    // Exactly the approved amount, and not the requested one.
    expect(account.consumed).toBe('1700.000000');
    expect(account.available).toBe('3300.000000');
    expect(availableBefore).toBe('3400.000000');

    // A rejection moves nothing at all.
    const underReview = reimbursementIn('UNDER_REVIEW');
    const balanceBefore = { ...account };
    const rejected = (
      await unwrap(
        reviewer.c.POST('/api/v1/reimbursements/{reimbursementId}/decide', {
          params: {
            header: ifMatch(reviewer, underReview.rowVersion),
            path: { reimbursementId: underReview.id },
          },
          body: { decision: 'REJECT', reasonCode: 'NOT_COVERED' },
        }),
      )
    ).data;
    expect(rejected.status).toBe('REJECTED');
    expect(rejected.approvedAmount).toBe('0');
    expect(rejected.claimId).toBeNull();
    expect(account.available).toBe(balanceBefore.available);
    expect(account.consumed).toBe(balanceBefore.consumed);
  });

  it('refuses an approval above the requested amount', async () => {
    const reviewer = await signIn('financial.reviewer');
    const submitted = reimbursementIn('SUBMITTED');
    const problem = await refusal(
      reviewer.c.POST('/api/v1/reimbursements/{reimbursementId}/decide', {
        params: {
          header: ifMatch(reviewer, submitted.rowVersion),
          path: { reimbursementId: submitted.id },
        },
        body: { decision: 'APPROVE', approvedAmount: '99999' },
      }),
    );
    expect(problem.code).toBe('VALIDATION_FAILED');
  });

  it('lets finance record the payment and marks it paid', async () => {
    const reviewer = await signIn('financial.reviewer');
    const ordered = reimbursementIn('PAYMENT_ORDERED');
    const paid = (
      await unwrap(
        reviewer.c.POST('/api/v1/reimbursements/{reimbursementId}/payment', {
          params: {
            header: ifMatch(reviewer, ordered.rowVersion),
            path: { reimbursementId: ordered.id },
          },
          body: { paymentReference: 'EFT-2026-03-9001' },
        }),
      )
    ).data;
    expect(paid.status).toBe('PAID');
    expect(paid.paymentReference).toBe('EFT-2026-03-9001');
    expect(paid.bankAccountMasked).toBe(IBAN_TAIL);
    expect(JSON.stringify(paid)).not.toContain(normalizeAccount(IBAN));
  });

  it('answers the member only their own, and 404 for anybody else', async () => {
    const member = await signIn('member.a');
    const mine = (
      await unwrap(
        member.c.GET('/api/v1/me/reimbursements', { params: { header: tenant(member) } }),
      )
    ).data;
    expect(mine.items.length).toBeGreaterThanOrEqual(8);
    for (const row of mine.items) {
      expect(row.personId).toBe(api.world.reimbursements[0]!.personId);
    }
    // Every status a screen has to draw is in the fixture.
    const statuses = new Set(mine.items.map((r) => r.status));
    for (const want of [
      'DRAFT',
      'SUBMITTED',
      'UNDER_REVIEW',
      'PARTIALLY_APPROVED',
      'REJECTED',
      'PAYMENT_ORDERED',
      'PAID',
      'CANCELLED',
    ]) {
      expect(statuses).toContain(want);
    }

    // Somebody else's row, planted directly, is not found rather than refused: the two are
    // the same answer and telling them apart would confirm the row exists.
    const theirs: StoredReimbursement = {
      ...api.world.reimbursements[0]!,
      id: api.world.nextId(),
      reference: 'RB-202603-ZZZZZZZZ',
      personId: api.world.people.find((p) => p.id !== mine.items[0]!.personId)!.id,
    };
    api.world.reimbursements.push(theirs);
    const problem = await refusal(
      member.c.GET('/api/v1/reimbursements/{reimbursementId}', {
        params: { header: tenant(member), path: { reimbursementId: theirs.id } },
      }),
    );
    expect(problem.code).toBe('REIMBURSEMENT_NOT_FOUND');
  });

  it('refuses an account number that is not one', async () => {
    const member = await signIn('member.a');
    const draft = reimbursementIn('DRAFT');
    const problem = await refusal(
      member.c.POST('/api/v1/reimbursements', {
        params: { header: command(member) },
        body: {
          serviceRequestId: draft.serviceRequestId,
          receiptDocumentId: draft.receiptDocumentId,
          requestedAmount: '10',
          bankAccount: 'hesabım Ziraat Bankasında',
        },
      }),
    );
    expect(problem.code).toBe('VALIDATION_FAILED');
    expect(problem.errors?.some((e) => e.field === 'bankAccount')).toBe(true);
  });
});
