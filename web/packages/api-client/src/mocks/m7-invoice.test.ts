/**
 * WP-I7-02 at the mock's API: the invoice a provider raised elsewhere, the claims it collects,
 * the tolerance the submit is gated on and the supersede chain a correction lives in.
 *
 * The mock is a test double of the Go server and a divergence in either direction is a bug, so
 * every test below is written against a behaviour the server has and a plausible mock would get
 * wrong: the header that has to add up exactly, the allocation that may be less than what was
 * approved and never more, the claim that cannot sit on two live invoices, the submit refused
 * with both figures and the difference, the links that freeze the moment the invoice leaves
 * DRAFT, the old invoice cancelled only when its correction is *submitted*, and the line
 * description the sponsor's HR user must never receive.
 */
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { createKapsoraClient, randomId, type KapsoraClient } from '../client';
import { createOperations } from '../operations';
import { ApiError, unwrap, type Problem } from '../problem';
import type { StoredClaim, StoredInvoice } from './data';
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

/** A command header: the tenant and a fresh idempotency key, which every command here needs. */
const command = (s: Session) => ({
  ...tenant(s),
  'Idempotency-Key': `mock-${randomId()}`,
});

/** A command header with a caller-chosen key, for the replay test. */
const commandWithKey = (s: Session, key: string) => ({
  ...tenant(s),
  'Idempotency-Key': key,
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

function invoiceIn(status: string, predicate: (row: StoredInvoice) => boolean = () => true) {
  const row = api.world.invoices.find((i) => i.status === status && predicate(i));
  if (!row) throw new Error(`fixture: no invoice in ${status}`);
  return row;
}

/** The seeded draft whose allocations do not add up to what it bills. */
function mismatchDraft() {
  return invoiceIn('DRAFT', (i) => i.supersedesInvoiceId === null);
}

/** The seeded correction: a draft that names the returned invoice it replaces. */
function correctionDraft() {
  return invoiceIn('DRAFT', (i) => i.supersedesInvoiceId !== null);
}

/** A decided claim of the seeded provider that nobody is already collecting. */
function freeClaim(providerOrganizationId: string): StoredClaim {
  const held = new Set(api.world.invoiceAllocations.filter((a) => a.active).map((a) => a.claimId));
  const row = api.world.claims.find(
    (c) =>
      c.providerOrganizationId === providerOrganizationId &&
      (c.status === 'APPROVED' || c.status === 'PARTIALLY_APPROVED') &&
      !held.has(c.id),
  );
  if (!row) throw new Error('fixture: no free decided claim');
  return row;
}

async function getInvoice(s: Session, invoiceId: string) {
  return (
    await unwrap(
      s.c.GET('/api/v1/invoices/{invoiceId}', {
        params: { header: tenant(s), path: { invoiceId } },
      }),
    )
  ).data;
}

/** The header the create tests send. It adds up, so a refusal is about something else. */
function header(providerOrganizationId: string, invoiceNumber: string, payable: string) {
  return {
    providerOrganizationId,
    invoiceNumber,
    invoiceDate: '2026-03-17',
    lineExtensionAmount: payable,
    taxAmount: '0',
    payableAmount: payable,
  };
}

describe('the world WP-I7-02 seeds', () => {
  it('carries a submitted invoice on two claims, a draft that does not add up, and a returned invoice with its correction', async () => {
    const finance = await signIn('financial.reviewer');
    const page = (
      await unwrap(finance.c.GET('/api/v1/invoices', { params: { header: tenant(finance) } }))
    ).data;
    expect(page.items.length).toBeGreaterThanOrEqual(4);

    const submitted = invoiceIn('SUBMITTED');
    const body = await getInvoice(finance, submitted.id);
    expect(body.allocations).toHaveLength(2);
    // It adds up exactly, which is the only reason it could have been submitted.
    expect(body.allocationTotal).toBe(body.payableAmount);
    expect(body.allocationDifference).toBe('0');
    // And its claims are on a document somebody is collecting.
    for (const allocation of body.allocations) {
      expect(allocation.claimStatus).toBe('INVOICED');
      expect(allocation.active).toBe(true);
    }

    const mismatch = await getInvoice(finance, mismatchDraft().id);
    expect(mismatch.status).toBe('DRAFT');
    expect(Number(mismatch.allocationDifference)).toBeGreaterThan(0);
    expect(mismatch.allocationTotal).not.toBe(mismatch.payableAmount);

    // The returned invoice keeps its link and the link is inactive: nothing is deleted, and
    // the claim is free to go onto the correction.
    const returned = await getInvoice(finance, invoiceIn('RETURNED').id);
    expect(returned.allocations).toHaveLength(1);
    expect(returned.allocations[0]!.active).toBe(false);
    expect(returned.supersededByInvoiceId).toBeUndefined();

    const correction = await getInvoice(finance, correctionDraft().id);
    expect(correction.supersedesInvoiceId).toBe(returned.id);
    expect(correction.allocations[0]!.claimId).toBe(returned.allocations[0]!.claimId);
    expect(correction.allocations[0]!.active).toBe(true);
  });

  it('never renders the provider tax identity in any form', async () => {
    const finance = await signIn('financial.reviewer');
    const response = await finance.c.GET('/api/v1/invoices/{invoiceId}', {
      params: { header: tenant(finance), path: { invoiceId: invoiceIn('SUBMITTED').id } },
    });
    const text = JSON.stringify(response.data);
    // Neither the number nor a hash of it: on the server the column is a blind index no
    // response reads, and a mock that carried either would be a mock a screen could be built
    // against wrongly.
    expect(text).not.toContain('taxNumber');
    expect(text).not.toContain('providerTaxIdHash');
    expect(text.toLowerCase()).not.toContain('vkn');
  });
});

describe('creating an invoice', () => {
  it('refuses a header whose halves do not make its total, naming payableAmount', async () => {
    const billing = await signIn('billing.a');
    const provider = mismatchDraft().providerOrganizationId;
    const problem = await refusal(
      billing.c.POST('/api/v1/invoices', {
        params: { header: command(billing) },
        body: {
          ...header(provider, 'KPS2026900001', '1200.12'),
          lineExtensionAmount: '1000.10',
          taxAmount: '200.02',
          payableAmount: '1200.13',
        },
      }),
    );
    expect(problem.status).toBe(422);
    expect(problem.code).toBe('VALIDATION_FAILED');
    expect(problem.errors?.map((e) => e.field)).toContain('payableAmount');
    expect(problem.errors?.find((e) => e.field === 'payableAmount')?.code).toBe('SUM');
  });

  it('accepts the same figures when they add up exactly', async () => {
    const billing = await signIn('billing.a');
    const provider = mismatchDraft().providerOrganizationId;
    const created = (
      await unwrap(
        billing.c.POST('/api/v1/invoices', {
          params: { header: command(billing) },
          body: {
            ...header(provider, 'KPS2026900002', '1200.12'),
            lineExtensionAmount: '1000.10',
            taxAmount: '200.02',
            payableAmount: '1200.12',
          },
        }),
      )
    ).data;
    expect(created.status).toBe('DRAFT');
    expect(created.payableAmount).toBe('1200.12');
    expect(created.fiscalYear).toBe(2026);
    expect(created.source).toBe('MANUAL');
    // M8's columns exist and are unwritten.
    expect(created.edocumentId).toBeUndefined();
    expect(created.batchId).toBeUndefined();
  });

  it('refuses a number already used by a live invoice of the provider in that fiscal year', async () => {
    const billing = await signIn('billing.a');
    const existing = mismatchDraft();
    const problem = await refusal(
      billing.c.POST('/api/v1/invoices', {
        params: { header: command(billing) },
        body: {
          ...header(existing.providerOrganizationId, existing.invoiceNumber, '100'),
          invoiceDate: existing.invoiceDate,
        },
      }),
    );
    expect(problem.status).toBe(409);
    expect(problem.code).toBe('INVOICE_NUMBER_TAKEN');
    expect(problem.title).toBe('Bu fatura numarası bu mali yılda zaten kullanılmış');
  });

  it('frees the number when the invoice is cancelled', async () => {
    const billing = await signIn('billing.a');
    const existing = mismatchDraft();
    await unwrap(
      billing.c.POST('/api/v1/invoices/{invoiceId}/cancel', {
        params: {
          header: ifMatch(billing, existing.rowVersion),
          path: { invoiceId: existing.id },
        },
      }),
    );
    const created = (
      await unwrap(
        billing.c.POST('/api/v1/invoices', {
          params: { header: command(billing) },
          body: {
            ...header(existing.providerOrganizationId, existing.invoiceNumber, '100'),
            invoiceDate: existing.invoiceDate,
          },
        }),
      )
    ).data;
    expect(created.invoiceNumber).toBe(existing.invoiceNumber);
  });

  it('refuses a provider that carries no tax identity', async () => {
    const finance = await signIn('financial.reviewer');
    // Every seeded provider carries a VKN, because every seeded provider is one somebody could
    // actually invoice. The state under test is a directory correction that has not happened
    // yet, so it is made here — the world is rebuilt after each test.
    const provider = mismatchDraft().providerOrganizationId;
    const relationship = api.world.relationships.find((r) => r.id === provider)!;
    delete api.world.organizations.get(relationship.organizationId)!.taxNumber;

    const problem = await refusal(
      finance.c.POST('/api/v1/invoices', {
        params: { header: command(finance) },
        body: header(provider, 'KPS2026900003', '100'),
      }),
    );
    expect(problem.status).toBe(422);
    expect(problem.code).toBe('PROVIDER_TAX_ID_MISSING');
    expect(problem.title).toBe('Sağlayıcının vergi kimliği tanımlı değil');
    // The refusal says a number is missing and nothing else: no VKN, no blind index.
    expect(JSON.stringify(problem)).not.toContain('taxNumber');
  });

  it('refuses a kurum that is not a provider of the tenant', async () => {
    const finance = await signIn('financial.reviewer');
    const sponsor = api.world.relationships.find(
      (r) => r.tenantId === finance.tenantId && r.relationshipRole === 'SPONSOR',
    )!;
    const problem = await refusal(
      finance.c.POST('/api/v1/invoices', {
        params: { header: command(finance) },
        body: header(sponsor.id, 'KPS2026900005', '100'),
      }),
    );
    expect(problem.status).toBe(422);
    expect(problem.code).toBe('INVOICE_PROVIDER_UNKNOWN');
  });

  it('refuses a provider-scoped caller acting for somebody else', async () => {
    const billing = await signIn('billing.a');
    const other = api.world.relationships.find(
      (r) =>
        r.tenantId === billing.tenantId &&
        r.relationshipRole === 'PROVIDER' &&
        r.id !== mismatchDraft().providerOrganizationId,
    );
    if (!other) throw new Error('fixture: only one provider');
    const problem = await refusal(
      billing.c.POST('/api/v1/invoices', {
        params: { header: command(billing) },
        body: header(other.id, 'KPS2026900004', '100'),
      }),
    );
    expect(problem.status).toBe(403);
    expect(problem.code).toBe('INVOICE_PROVIDER_SCOPE');
  });
});

describe('allocating claims to a draft', () => {
  it('allows less than the approved total and refuses a kuruş more, naming the claim', async () => {
    const finance = await signIn('financial.reviewer');
    const draft = mismatchDraft();
    const claim = freeClaim(draft.providerOrganizationId);
    const readiness = (
      await unwrap(
        finance.c.GET('/api/v1/claims/{claimId}/invoice-readiness', {
          params: { header: tenant(finance), path: { claimId: claim.id } },
        }),
      )
    ).data;
    const approved = readiness.approvedTotal;

    const problem = await refusal(
      finance.c.PUT('/api/v1/invoices/{invoiceId}/allocations', {
        params: { header: ifMatch(finance, draft.rowVersion), path: { invoiceId: draft.id } },
        body: {
          allocations: [{ claimId: claim.id, allocatedAmount: `${Number(approved) + 0.01}` }],
        },
      }),
    );
    expect(problem.status).toBe(422);
    expect(problem.code).toBe('ALLOCATION_EXCEEDS_APPROVED');
    expect(problem.title).toBe('Tahsis onaylanan tutarı aşıyor');
    // The refusal names the claim and both figures: "constraint violated" is not something a
    // billing clerk can act on.
    expect((problem as Record<string, unknown>).claimId).toBe(claim.id);
    expect((problem as Record<string, unknown>).claimReference).toBe(claim.reference);
    expect((problem as Record<string, unknown>).approvedTotal).toBe(approved);

    // Billing part of a claim is ordinary and has to work.
    const half = `${Number(approved) / 2}`;
    const after = (
      await unwrap(
        finance.c.PUT('/api/v1/invoices/{invoiceId}/allocations', {
          params: { header: ifMatch(finance, draft.rowVersion), path: { invoiceId: draft.id } },
          body: { allocations: [{ claimId: claim.id, allocatedAmount: half }] },
        }),
      )
    ).data;
    expect(after.allocations).toHaveLength(1);
    expect(after.allocationTotal).toBe(half);
  });

  it('refuses a claim that is already on a live invoice, naming that invoice', async () => {
    const finance = await signIn('financial.reviewer');
    const draft = mismatchDraft();
    const submitted = invoiceIn('SUBMITTED');
    const taken = api.world.invoiceAllocations.find(
      (a) => a.invoiceId === submitted.id && a.active,
    )!;
    const problem = await refusal(
      finance.c.PUT('/api/v1/invoices/{invoiceId}/allocations', {
        params: { header: ifMatch(finance, draft.rowVersion), path: { invoiceId: draft.id } },
        body: { allocations: [{ claimId: taken.claimId, allocatedAmount: '1' }] },
      }),
    );
    // The claim moved to INVOICED when the invoice was submitted, so it is refused as not
    // invoiceable rather than as already-invoiced — both are true and the status is the one a
    // clerk can see for themselves.
    expect(problem.status).toBe(422);
    expect(problem.code).toBe('CLAIM_NOT_INVOICEABLE');
    expect((problem as Record<string, unknown>).claimStatus).toBe('INVOICED');
  });

  it('refuses a claim held by another draft with CLAIM_ALREADY_INVOICED', async () => {
    const finance = await signIn('financial.reviewer');
    const correction = correctionDraft();
    const held = api.world.invoiceAllocations.find(
      (a) => a.invoiceId === correction.id && a.active,
    )!;
    const mine = (
      await unwrap(
        finance.c.POST('/api/v1/invoices', {
          params: { header: command(finance) },
          body: header(correction.providerOrganizationId, 'KPS2026900010', '500'),
        }),
      )
    ).data;
    const problem = await refusal(
      finance.c.PUT('/api/v1/invoices/{invoiceId}/allocations', {
        params: { header: ifMatch(finance, mine.rowVersion), path: { invoiceId: mine.id } },
        body: { allocations: [{ claimId: held.claimId, allocatedAmount: '500' }] },
      }),
    );
    expect(problem.status).toBe(409);
    expect(problem.code).toBe('CLAIM_ALREADY_INVOICED');
    expect((problem as Record<string, unknown>).liveInvoiceId).toBe(correction.id);
  });

  it('refuses the same claim twice on one document', async () => {
    const finance = await signIn('financial.reviewer');
    const draft = mismatchDraft();
    const claim = freeClaim(draft.providerOrganizationId);
    const problem = await refusal(
      finance.c.PUT('/api/v1/invoices/{invoiceId}/allocations', {
        params: { header: ifMatch(finance, draft.rowVersion), path: { invoiceId: draft.id } },
        body: {
          allocations: [
            { claimId: claim.id, allocatedAmount: '10' },
            { claimId: claim.id, allocatedAmount: '10' },
          ],
        },
      }),
    );
    expect(problem.status).toBe(422);
    expect(problem.errors?.[0]!.code).toBe('DUPLICATE');
  });
});

describe('submitting an invoice', () => {
  it('refuses a mismatch beyond the tolerance with both figures and the difference', async () => {
    const finance = await signIn('financial.reviewer');
    const draft = mismatchDraft();
    const before = await getInvoice(finance, draft.id);
    const problem = await refusal(
      finance.c.POST('/api/v1/invoices/{invoiceId}/submit', {
        params: { header: ifMatch(finance, draft.rowVersion), path: { invoiceId: draft.id } },
      }),
    );
    expect(problem.status).toBe(409);
    expect(problem.code).toBe('ALLOCATION_MISMATCH');
    expect(problem.title).toBe('Tahsis toplamı fatura tutarıyla uyuşmuyor');
    const carried = problem as Record<string, unknown>;
    expect(carried.payableAmount).toBe(before.payableAmount);
    expect(carried.allocationTotal).toBe(before.allocationTotal);
    expect(carried.difference).toBe(before.allocationDifference);
    expect(carried.tolerance).toBe('0.01');
  });

  it('goes through when the difference is inside the tolerance, and moves the claims to INVOICED', async () => {
    const finance = await signIn('financial.reviewer');
    const draft = mismatchDraft();
    const claim = api.world.invoiceAllocations.find((a) => a.invoiceId === draft.id && a.active)!;
    // Correct the header down to a kuruş above what the claim covers: inside the tolerance.
    const allocated = Number(claim.allocatedAmount);
    const patched = (
      await unwrap(
        finance.c.PATCH('/api/v1/invoices/{invoiceId}', {
          params: { header: ifMatch(finance, draft.rowVersion), path: { invoiceId: draft.id } },
          body: {
            lineExtensionAmount: `${allocated + 0.01}`,
            taxAmount: '0',
            payableAmount: `${allocated + 0.01}`,
          },
          headers: { 'Content-Type': 'application/merge-patch+json' },
        }),
      )
    ).data;
    expect(patched.allocationDifference).toBe('0.01');

    const submitted = (
      await unwrap(
        finance.c.POST('/api/v1/invoices/{invoiceId}/submit', {
          params: {
            header: ifMatch(finance, patched.rowVersion),
            path: { invoiceId: draft.id },
          },
        }),
      )
    ).data;
    expect(submitted.status).toBe('SUBMITTED');
    expect(submitted.submittedAt).toBeTruthy();
    expect(submitted.allocations[0]!.claimStatus).toBe('INVOICED');
    expect(api.world.claims.find((c) => c.id === claim.claimId)!.status).toBe('INVOICED');
  });

  it('freezes the links and the figures once it has left DRAFT', async () => {
    const finance = await signIn('financial.reviewer');
    const submitted = invoiceIn('SUBMITTED');
    const patchProblem = await refusal(
      finance.c.PATCH('/api/v1/invoices/{invoiceId}', {
        params: {
          header: ifMatch(finance, submitted.rowVersion),
          path: { invoiceId: submitted.id },
        },
        body: { invoiceNumber: 'KPS2026999999' },
        headers: { 'Content-Type': 'application/merge-patch+json' },
      }),
    );
    expect(patchProblem.status).toBe(409);
    expect(patchProblem.code).toBe('INVOICE_FROZEN');

    const allocationProblem = await refusal(
      finance.c.PUT('/api/v1/invoices/{invoiceId}/allocations', {
        params: {
          header: ifMatch(finance, submitted.rowVersion),
          path: { invoiceId: submitted.id },
        },
        body: { allocations: [] },
      }),
    );
    expect(allocationProblem.code).toBe('INVOICE_FROZEN');

    const cancelProblem = await refusal(
      finance.c.POST('/api/v1/invoices/{invoiceId}/cancel', {
        params: {
          header: ifMatch(finance, submitted.rowVersion),
          path: { invoiceId: submitted.id },
        },
      }),
    );
    expect(cancelProblem.status).toBe(409);
    expect(cancelProblem.code).toBe('INVOICE_TRANSITION_INVALID');
  });

  it('refuses a submit with no image when the tenant requires one', async () => {
    const finance = await signIn('financial.reviewer');
    const draft = mismatchDraft();
    const claim = api.world.invoiceAllocations.find((a) => a.invoiceId === draft.id && a.active)!;
    const cleared = (
      await unwrap(
        finance.c.PATCH('/api/v1/invoices/{invoiceId}', {
          params: { header: ifMatch(finance, draft.rowVersion), path: { invoiceId: draft.id } },
          body: {
            documentId: null,
            lineExtensionAmount: claim.allocatedAmount,
            taxAmount: '0',
            payableAmount: claim.allocatedAmount,
          },
          headers: { 'Content-Type': 'application/merge-patch+json' },
        }),
      )
    ).data;
    expect(cleared.documentId).toBeUndefined();
    const problem = await refusal(
      finance.c.POST('/api/v1/invoices/{invoiceId}/submit', {
        params: { header: ifMatch(finance, cleared.rowVersion), path: { invoiceId: draft.id } },
      }),
    );
    expect(problem.status).toBe(409);
    expect(problem.code).toBe('INVOICE_IMAGE_REQUIRED');
    expect(problem.title).toBe('Fatura görüntüsü gerekli');
  });

  it('answers a replayed submit with the same body under the same idempotency key', async () => {
    const finance = await signIn('financial.reviewer');
    const draft = mismatchDraft();
    const claim = api.world.invoiceAllocations.find((a) => a.invoiceId === draft.id && a.active)!;
    const patched = (
      await unwrap(
        finance.c.PATCH('/api/v1/invoices/{invoiceId}', {
          params: { header: ifMatch(finance, draft.rowVersion), path: { invoiceId: draft.id } },
          body: {
            lineExtensionAmount: claim.allocatedAmount,
            taxAmount: '0',
            payableAmount: claim.allocatedAmount,
          },
          headers: { 'Content-Type': 'application/merge-patch+json' },
        }),
      )
    ).data;

    const key = `mock-${randomId()}`;
    const params = {
      header: { ...commandWithKey(finance, key), 'If-Match': `"${patched.rowVersion}"` },
      path: { invoiceId: draft.id },
    };
    const first = (await unwrap(finance.c.POST('/api/v1/invoices/{invoiceId}/submit', { params })))
      .data;
    const replay = (await unwrap(finance.c.POST('/api/v1/invoices/{invoiceId}/submit', { params })))
      .data;
    // The same body, not a second submit: a replayed command must move one invoice.
    expect(replay).toEqual(first);
    expect(api.world.invoices.filter((i) => i.status === 'SUBMITTED')).toHaveLength(2);
  });
});

describe('the supersede chain', () => {
  it('cancels the old invoice when the correction is submitted, and not when it is drafted', async () => {
    const finance = await signIn('financial.reviewer');
    const returned = invoiceIn('RETURNED');
    const correction = correctionDraft();

    // Before: the correction exists and the returned invoice is untouched.
    expect((await getInvoice(finance, returned.id)).status).toBe('RETURNED');

    const claim = api.world.invoiceAllocations.find(
      (a) => a.invoiceId === correction.id && a.active,
    )!;
    const patched = (
      await unwrap(
        finance.c.PATCH('/api/v1/invoices/{invoiceId}', {
          params: {
            header: ifMatch(finance, correction.rowVersion),
            path: { invoiceId: correction.id },
          },
          body: {
            lineExtensionAmount: claim.allocatedAmount,
            taxAmount: '0',
            payableAmount: claim.allocatedAmount,
          },
          headers: { 'Content-Type': 'application/merge-patch+json' },
        }),
      )
    ).data;
    const submitted = (
      await unwrap(
        finance.c.POST('/api/v1/invoices/{invoiceId}/submit', {
          params: {
            header: ifMatch(finance, patched.rowVersion),
            path: { invoiceId: correction.id },
          },
        }),
      )
    ).data;

    const after = await getInvoice(finance, returned.id);
    expect(after.status).toBe('CANCELLED');
    expect(after.supersededByInvoiceId).toBe(submitted.id);
    // Nothing was deleted: the cancelled invoice still says which claim it covered.
    expect(after.allocations).toHaveLength(1);
  });

  it('answers the chain in order from either end', async () => {
    const finance = await signIn('financial.reviewer');
    const returned = invoiceIn('RETURNED');
    const correction = correctionDraft();
    for (const from of [returned.id, correction.id]) {
      const chain = (
        await unwrap(
          finance.c.GET('/api/v1/invoices/{invoiceId}/versions', {
            params: { header: tenant(finance), path: { invoiceId: from } },
          }),
        )
      ).data;
      expect(chain.items.map((i) => i.id)).toEqual([returned.id, correction.id]);
    }
  });

  it("lets only the correction reuse a returned invoice's number", async () => {
    const finance = await signIn('financial.reviewer');
    const returned = invoiceIn('RETURNED');
    // The seeded correction already stands, and one invoice has one successor. Withdraw it, so
    // this test is about the number rather than about the chain.
    const seededCorrection = correctionDraft();
    await unwrap(
      finance.c.POST('/api/v1/invoices/{invoiceId}/cancel', {
        params: {
          header: ifMatch(finance, seededCorrection.rowVersion),
          path: { invoiceId: seededCorrection.id },
        },
      }),
    );

    // An unrelated invoice may not pick the number up: the returned invoice is still the
    // document that number belongs to in the provider's books.
    const taken = await refusal(
      finance.c.POST('/api/v1/invoices', {
        params: { header: command(finance) },
        body: {
          ...header(returned.providerOrganizationId, returned.invoiceNumber, '100'),
          invoiceDate: returned.invoiceDate,
        },
      }),
    );
    expect(taken.status).toBe(409);
    expect(taken.code).toBe('INVOICE_NUMBER_TAKEN');

    // **And neither may a correction of somebody else's document.** With one returned invoice
    // in the world, "the correction may reuse the number" and "any correction may reuse any
    // returned number" are the same sentence — so a second returned invoice is put in front of
    // the rule, and a correction of the first carrying the second's number is refused.
    const other = (
      await unwrap(
        finance.c.POST('/api/v1/invoices', {
          params: { header: command(finance) },
          body: {
            ...header(returned.providerOrganizationId, 'KPS2026900050', '100'),
            invoiceDate: returned.invoiceDate,
          },
        }),
      )
    ).data;
    // WP-I7-03's reviewer is what returns an invoice; that command is not this package's, so
    // the status is moved the way the seeded fixture moves it.
    api.world.invoices.find((i) => i.id === other.id)!.status = 'RETURNED';

    const before = api.world.invoices.length;
    const wrong = await refusal(
      finance.c.POST('/api/v1/invoices', {
        params: { header: command(finance) },
        body: {
          ...header(returned.providerOrganizationId, other.invoiceNumber, '100'),
          invoiceDate: returned.invoiceDate,
          supersedesInvoiceId: returned.id,
        },
      }),
    );
    expect(wrong.status).toBe(409);
    expect(wrong.code).toBe('INVOICE_NUMBER_TAKEN');
    // Nothing was written, and the invoice whose number was asked for is untouched.
    expect(api.world.invoices).toHaveLength(before);
    expect(api.world.invoices.find((i) => i.id === other.id)!.status).toBe('RETURNED');

    // The correction may carry the number of the invoice it is actually correcting.
    const correction = (
      await unwrap(
        finance.c.POST('/api/v1/invoices', {
          params: { header: command(finance) },
          body: {
            ...header(returned.providerOrganizationId, returned.invoiceNumber, '100'),
            invoiceDate: returned.invoiceDate,
            supersedesInvoiceId: returned.id,
          },
        }),
      )
    ).data;
    expect(correction.invoiceNumber).toBe(returned.invoiceNumber);
    expect(correction.supersedesInvoiceId).toBe(returned.id);
  });

  it('refuses a second correction of one returned invoice', async () => {
    const finance = await signIn('financial.reviewer');
    const returned = invoiceIn('RETURNED');
    const problem = await refusal(
      finance.c.POST('/api/v1/invoices', {
        params: { header: command(finance) },
        body: {
          ...header(returned.providerOrganizationId, 'KPS2026900021', '100'),
          supersedesInvoiceId: returned.id,
        },
      }),
    );
    expect(problem.status).toBe(409);
    expect(problem.code).toBe('INVOICE_NOT_SUPERSEDABLE');
  });

  it('refuses a correction of an invoice that is not returned or rejected', async () => {
    const finance = await signIn('financial.reviewer');
    const submitted = invoiceIn('SUBMITTED');
    const problem = await refusal(
      finance.c.POST('/api/v1/invoices', {
        params: { header: command(finance) },
        body: {
          ...header(submitted.providerOrganizationId, 'KPS2026900020', '100'),
          supersedesInvoiceId: submitted.id,
        },
      }),
    );
    expect(problem.status).toBe(409);
    expect(problem.code).toBe('INVOICE_NOT_SUPERSEDABLE');
  });
});

describe('cancelling an invoice', () => {
  it('releases the claims a draft was holding and keeps the record', async () => {
    const finance = await signIn('financial.reviewer');
    const draft = mismatchDraft();
    const link = api.world.invoiceAllocations.find((a) => a.invoiceId === draft.id && a.active)!;
    const before = api.world.claims.find((c) => c.id === link.claimId)!.status;

    const cancelled = (
      await unwrap(
        finance.c.POST('/api/v1/invoices/{invoiceId}/cancel', {
          params: { header: ifMatch(finance, draft.rowVersion), path: { invoiceId: draft.id } },
        }),
      )
    ).data;
    expect(cancelled.status).toBe('CANCELLED');
    expect(cancelled.allocations).toHaveLength(1);
    expect(cancelled.allocations[0]!.active).toBe(false);
    expect(api.world.claims.find((c) => c.id === link.claimId)!.status).toBe(before);

    // And the claim can be put on another invoice.
    const next = (
      await unwrap(
        finance.c.POST('/api/v1/invoices', {
          params: { header: command(finance) },
          body: header(draft.providerOrganizationId, 'KPS2026900030', link.allocatedAmount),
        }),
      )
    ).data;
    const allocated = (
      await unwrap(
        finance.c.PUT('/api/v1/invoices/{invoiceId}/allocations', {
          params: { header: ifMatch(finance, next.rowVersion), path: { invoiceId: next.id } },
          body: {
            allocations: [{ claimId: link.claimId, allocatedAmount: link.allocatedAmount }],
          },
        }),
      )
    ).data;
    expect(allocated.allocations).toHaveLength(1);
  });
});

describe('who may see what', () => {
  it("serves the sponsor's HR user the invoice with no claim line description", async () => {
    const submitted = invoiceIn('SUBMITTED');
    // The mock holds one session at a time, so the two reads are made in sequence.
    //
    // No seeded role holds both `invoice.read` and `health.clinical.read`, and that is itself
    // the design: the payer's financial reviewer reconciles a bill and has no reason to read
    // what a surgeon wrote about a knee. The pairing is made here so the assertion below is a
    // test of the projection rather than of a field nobody populates.
    api.world.accounts
      .find((a) => a.username === 'doctor.a')!
      .memberships[0]!.permissions.push('invoice.read');
    const doctor = await signIn('doctor.a');
    const clinical = (
      await unwrap(
        doctor.c.GET('/api/v1/invoices/{invoiceId}', {
          params: { header: tenant(doctor), path: { invoiceId: submitted.id } },
        }),
      )
    ).data;
    expect(clinical.projection).toBe('CLINICAL');
    expect(clinical.allocations.some((a) => Boolean(a.claimDescription))).toBe(true);

    const hr = await signIn('sponsor.hr');
    const body = (
      await unwrap(
        hr.c.GET('/api/v1/invoices/{invoiceId}', {
          params: { header: tenant(hr), path: { invoiceId: submitted.id } },
        }),
      )
    ).data;
    expect(body.projection).toBe('FINANCIAL');
    for (const allocation of body.allocations) {
      expect(allocation.claimDescription).toBeUndefined();
    }
    // Written against the bytes as well: an assertion about the decoded object would pass if
    // the description came back under another key.
    const description = clinical.allocations.find((a) => a.claimDescription)!.claimDescription!;
    expect(JSON.stringify(body)).not.toContain(description);

    // Everything a bill *is* survives the projection.
    expect(body.payableAmount).toBe(clinical.payableAmount);
    expect(body.allocationTotal).toBe(clinical.allocationTotal);
    for (const allocation of body.allocations) {
      expect(allocation.claimReference).not.toBe('');
      expect(Number(allocation.allocatedAmount)).toBeGreaterThan(0);
    }
  });

  it('shows a provider-scoped caller only its own invoices', async () => {
    const billing = await signIn('billing.a');
    const page = (
      await unwrap(billing.c.GET('/api/v1/invoices', { params: { header: tenant(billing) } }))
    ).data;
    const mine = api.world.accounts.find((a) => a.username === 'billing.a')!.memberships[0]!
      .scopes![0]!.id;
    expect(page.items.length).toBeGreaterThan(0);
    for (const row of page.items) {
      expect(row.providerOrganizationId).toBe(mine);
    }
  });

  it('refuses a caller holding neither invoice grant', async () => {
    const member = await signIn('member.a');
    const problem = await refusal(
      member.c.GET('/api/v1/invoices', { params: { header: tenant(member) } }),
    );
    expect(problem.status).toBe(403);
  });
});

describe("the provider's earnings after WP-I7-02", () => {
  it('stops offering a claim the moment it goes onto a live invoice, and offers it again when that invoice is withdrawn', async () => {
    const finance = await signIn('financial.reviewer');
    const draft = mismatchDraft();
    const provider = draft.providerOrganizationId;
    const claim = freeClaim(provider);

    const earningsOf = async () => {
      const body = (
        await unwrap(
          finance.c.GET('/api/v1/providers/{providerId}/earnings', {
            params: { header: tenant(finance), path: { providerId: provider } },
          }),
        )
      ).data;
      return body.currencies.find((c) => c.currencyCode === 'TRY')!;
    };

    const before = await earningsOf();
    expect(before.invoiceableClaimIds).toContain(claim.id);

    const created = (
      await unwrap(
        finance.c.POST('/api/v1/invoices', {
          params: { header: command(finance) },
          body: header(provider, 'KPS2026900040', '100'),
        }),
      )
    ).data;
    await unwrap(
      finance.c.PUT('/api/v1/invoices/{invoiceId}/allocations', {
        params: { header: ifMatch(finance, created.rowVersion), path: { invoiceId: created.id } },
        body: { allocations: [{ claimId: claim.id, allocatedAmount: '1' }] },
      }),
    );

    // The claim is still APPROVED — the invoice is only a draft — and it is no longer on
    // offer. Keying invoiceability off the status alone would put it on a second document.
    expect(api.world.claims.find((c) => c.id === claim.id)!.status).toBe(claim.status);
    const during = await earningsOf();
    expect(during.invoiceableClaimIds).not.toContain(claim.id);
    expect(Number(during.approvedTotal)).toBe(Number(before.approvedTotal));
    expect(Number(during.invoiceableTotal)).toBeLessThan(Number(before.invoiceableTotal));

    const current = await getInvoice(finance, created.id);
    await unwrap(
      finance.c.POST('/api/v1/invoices/{invoiceId}/cancel', {
        params: {
          header: ifMatch(finance, current.rowVersion),
          path: { invoiceId: created.id },
        },
      }),
    );
    const after = await earningsOf();
    expect(after.invoiceableClaimIds).toContain(claim.id);
    expect(Number(after.invoiceableTotal)).toBe(Number(before.invoiceableTotal));
  });
});
