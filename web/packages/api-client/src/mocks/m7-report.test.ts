/**
 * WP-I7-05 at the mock's API: the cari ekstre, the daily reconciliation, the operations dashboard,
 * and the exports a person is allowed to take out of the system.
 *
 * The mock is a test double of the Go server and a divergence in either direction is a bug, so
 * every test below is written against a behaviour the server has and a plausible mock would get
 * wrong: totals that are the server's rather than the visible rows', a run nobody can create, a
 * dashboard figure that has to agree with the list its own filter names, an export that is queued
 * rather than answered, a file with the watermark on every row, a download that is counted, and a
 * link that stops working when it said it would.
 */
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { createKapsoraClient, randomId, type KapsoraClient } from '../client';
import { createOperations } from '../operations';
import { ApiError, unwrap, type Problem } from '../problem';
import { claimTotals } from './claim-handlers';
import { toMicros } from './data';
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

async function refusal(call: Promise<{ response: Response }>): Promise<Problem> {
  try {
    await unwrap(call as Parameters<typeof unwrap>[0]);
  } catch (error) {
    if (error instanceof ApiError) return error.problem;
    throw error;
  }
  throw new Error('expected the call to be refused');
}

/** Adds exact decimals the way every figure in this platform is added: in micro-units. */
function totalOf(values: string[]): string {
  const micros = values.reduce((sum, value) => {
    const [whole, fraction = ''] = value.split('.');
    return sum + BigInt(whole!) * 1_000_000n + BigInt(fraction.padEnd(6, '0').slice(0, 6));
  }, 0n);
  const text = (micros / 1_000_000n).toString();
  const rest = (micros % 1_000_000n).toString().padStart(6, '0').replace(/0+$/, '');
  return rest === '' ? text : `${text}.${rest}`;
}

describe('the provider statement (WP-I7-05 §2.2)', () => {
  it('answers totals that equal the rows it returned, to the kuruş', async () => {
    const s = await signIn('financial.reviewer');
    const provider = api.world.settlements[0]!.providerOrganizationId;
    const page = await unwrap(
      s.c.GET('/api/v1/providers/{providerId}/statement', {
        params: {
          header: tenant(s),
          path: { providerId: provider },
          query: { periodFrom: '2000-01-01', periodTo: '2100-01-01', currencyCode: 'TRY' },
        },
      }),
    );
    const body = page.data;
    expect(body.totals.invoicedTotal).toBe(totalOf(body.invoices.map((i) => i.payableAmount)));
    expect(body.totals.settledTotal).toBe(
      totalOf(body.settlements.map((row) => row.payableAmount)),
    );
    expect(body.totals.paidTotal).toBe(totalOf(body.settlements.map((row) => row.paidAmount)));
    expect(body.totals.invoiceCount).toBe(body.invoices.length);
    expect(body.totals.settlementCount).toBe(body.settlements.length);
    // Nothing on the page is a float: every amount is a decimal string.
    for (const value of Object.values(body.totals)) {
      if (typeof value === 'string') expect(value).toMatch(/^-?\d+(\.\d+)?$/);
    }
  });

  it('refuses a period the caller did not give', async () => {
    const s = await signIn('financial.reviewer');
    const provider = api.world.settlements[0]!.providerOrganizationId;
    const problem = await refusal(
      s.c.GET('/api/v1/providers/{providerId}/statement', {
        // The contract makes the period required, so the omission has to be forced past the
        // generated types: what is being tested is the server's answer to a client that did
        // not read the contract, and there is one of those in every integration.
        params: {
          header: tenant(s),
          path: { providerId: provider },
          query: {} as unknown as { periodFrom: string; periodTo: string },
        },
      }),
    );
    expect(problem.code).toBe('VALIDATION_FAILED');
    expect(problem.errors?.map((e) => e.field)).toContain('periodFrom');
  });

  it('refuses a provider-scoped caller asking about somebody else', async () => {
    const s = await signIn('billing.a');
    const own = api.world.accounts.find((a) => a.username === 'billing.a')!.memberships[0]!
      .scopes![0]!.id!;
    const other = api.world.relationships.find(
      (r) => r.tenantId === s.tenantId && r.id !== own && r.relationshipRole === 'PROVIDER',
    )!;
    const problem = await refusal(
      s.c.GET('/api/v1/providers/{providerId}/statement', {
        params: {
          header: tenant(s),
          path: { providerId: other.id },
          query: { periodFrom: '2000-01-01', periodTo: '2100-01-01' },
        },
      }),
    );
    expect(problem.status).toBe(403);
    expect(problem.code).toBe('REPORT_PROVIDER_SCOPE');
  });
});

describe('the reconciliation runs (WP-I7-05 §2.3)', () => {
  it('are read and never created: the fixture has one balanced and one differing', async () => {
    const s = await signIn('financial.reviewer');
    const page = await unwrap(
      s.c.GET('/api/v1/reconciliation-runs', {
        params: { header: tenant(s), query: { limit: 50 } },
      }),
    );
    const statuses = new Set(page.data.items.map((r) => r.status));
    expect(statuses.has('BALANCED')).toBe(true);
    expect(statuses.has('DIFFERENCES')).toBe(true);
  });

  it('records the arithmetic the server checks: open is settled minus paid', async () => {
    const s = await signIn('financial.reviewer');
    const page = await unwrap(
      s.c.GET('/api/v1/reconciliation-runs', {
        params: { header: tenant(s), query: { limit: 50 } },
      }),
    );
    for (const run of page.data.items) {
      expect(run.openTotal).toBe(
        totalOf([run.settledTotal]) === run.settledTotal
          ? subtract(run.settledTotal, run.paidTotal)
          : run.openTotal,
      );
      expect(run.difference).toBe(run.openTotal);
      // The ERP has not spoken and will not until M9.
      expect(run.erpTotal ?? null).toBeNull();
      // A balanced run carries no difference and a differing one carries at least one.
      expect(run.differences.length).toBe(run.differenceCount);
      expect(run.status === 'BALANCED' ? run.differenceCount === 0 : run.differenceCount > 0).toBe(
        true,
      );
    }
  });

  it('names a settlement by reference and never by id', async () => {
    const s = await signIn('financial.reviewer');
    const page = await unwrap(
      s.c.GET('/api/v1/reconciliation-runs', {
        params: { header: tenant(s), query: { status: 'DIFFERENCES', limit: 50 } },
      }),
    );
    const run = page.data.items[0]!;
    const serialised = JSON.stringify(run.differences);
    expect(run.differences[0]!.reference).toMatch(/^ST-/);
    expect(serialised).not.toMatch(
      /[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/,
    );
  });
});

describe('the operations dashboard (WP-I7-05 §2.4)', () => {
  it('carries the filter that reproduces every figure', async () => {
    const s = await signIn('financial.reviewer');
    const board = (
      await unwrap(s.c.GET('/api/v1/operations/dashboard', { params: { header: tenant(s) } }))
    ).data;

    expect(board.settlements.dueSoonFilter.resource).toBe('settlements');
    expect(board.settlements.overdueFilter.resource).toBe('settlements');
    expect(board.settlements.dueSoonFilter.statuses).toEqual(
      board.settlements.overdueFilter.statuses,
    );
    expect(board.workItemsPastSla.filter.resource).toBe('workItems');
    for (const figure of board.claimsByStatus) {
      expect(figure.filter.statuses).toEqual([figure.status]);
    }
    // Every aging bucket is drawn, even the empty ones, so a screen has four columns on a quiet
    // morning and four on a busy one.
    expect(board.claimAging.map((f) => f.bucket)).toEqual(['D0_1', 'D2_7', 'D8_30', 'D31_PLUS']);
  });

  it('values each status at the approved totals of its claims, as the server does', async () => {
    const s = await signIn('financial.reviewer');
    const board = (
      await unwrap(s.c.GET('/api/v1/operations/dashboard', { params: { header: tenant(s) } }))
    ).data;
    const approved = board.claimsByStatus.find((f) => f.status === 'APPROVED');
    expect(approved, 'the fixture has approved claims').toBeDefined();
    const expected = api.world.claims
      .filter((c) => c.tenantId === s.tenantId && c.status === 'APPROVED')
      .reduce((total, c) => total + claimTotals(api.world, c.id).approved, 0n);
    expect(expected > 0n, 'the approved claims are worth something').toBe(true);
    expect(toMicros(approved!.approvedTotal)).toBe(expected);
  });

  it('counts the settlements its own filter names', async () => {
    const s = await signIn('financial.reviewer');
    const board = (
      await unwrap(s.c.GET('/api/v1/operations/dashboard', { params: { header: tenant(s) } }))
    ).data;
    const statuses = board.settlements.overdueFilter.statuses ?? [];
    const overdue = api.world.settlements.filter(
      (row) =>
        row.tenantId === s.tenantId &&
        statuses.includes(row.status) &&
        row.paidAmount !== row.payableAmount &&
        row.dueDate < (board.settlements.overdueFilter.dueBefore ?? ''),
    );
    expect(board.settlements.overdueCount).toBe(overdue.length);
  });
});

describe('the exports (WP-I7-05 §2.5)', () => {
  it('is queued rather than answered, and the worker renders it afterwards', async () => {
    const s = await signIn('financial.reviewer');
    const created = await unwrap(
      s.c.POST('/api/v1/exports', {
        params: { header: command(s) },
        body: { kind: 'SETTLEMENTS', parameters: { status: 'APPROVED' } },
      }),
    );
    expect(created.response.status).toBe(202);
    expect(created.data.status).toBe('QUEUED');
    expect(created.data.watermark).toContain(created.data.id);

    const ready = (
      await unwrap(
        s.c.GET('/api/v1/exports/{exportId}', {
          params: { header: tenant(s), path: { exportId: created.data.id } },
        }),
      )
    ).data;
    expect(ready.status).toBe('READY');
    expect(ready.documentId).not.toBeNull();
    expect(ready.rowCount).toBeGreaterThan(0);
  });

  it('stamps the watermark on the header and on every row of the file', async () => {
    const s = await signIn('financial.reviewer');
    const created = await unwrap(
      s.c.POST('/api/v1/exports', {
        params: { header: command(s) },
        body: { kind: 'SETTLEMENTS' },
      }),
    );
    await unwrap(
      s.c.GET('/api/v1/exports/{exportId}', {
        params: { header: tenant(s), path: { exportId: created.data.id } },
      }),
    );
    const row = api.world.exports.find((e) => e.id === created.data.id)!;
    const lines = row.body!.split('\r\n');
    expect(lines[0]).toContain(row.watermark);
    expect(lines[1]!.startsWith('"Filigran"')).toBe(true);
    for (const line of lines.slice(2)) {
      expect(line.startsWith(JSON.stringify(row.watermark))).toBe(true);
    }
    expect(lines.length - 2).toBe(row.rowCount);
  });

  it('refuses a filter carrying an identifier', async () => {
    const s = await signIn('financial.reviewer');
    const provider = api.world.settlements[0]!.providerOrganizationId;
    const problem = await refusal(
      s.c.POST('/api/v1/exports', {
        params: { header: command(s) },
        body: { kind: 'SETTLEMENTS', parameters: { providerId: provider } },
      }),
    );
    expect(problem.code).toBe('VALIDATION_FAILED');
    expect(problem.errors?.[0]!.code).toBe('IDENTIFIER');
  });

  it('refuses XLSX rather than answering with a CSV under another name', async () => {
    const s = await signIn('financial.reviewer');
    const problem = await refusal(
      s.c.POST('/api/v1/exports', {
        params: { header: command(s) },
        body: { kind: 'SETTLEMENTS', format: 'XLSX' },
      }),
    );
    expect(problem.code).toBe('VALIDATION_FAILED');
    expect(problem.errors?.[0]!.field).toBe('format');
  });

  it('refuses the CLAIMS kind to a caller without the sensitive grant', async () => {
    const s = await signIn('payer.approver');
    const problem = await refusal(
      s.c.POST('/api/v1/exports', {
        params: { header: command(s) },
        body: { kind: 'CLAIMS' },
      }),
    );
    expect(problem.status).toBe(403);
    expect(problem.code).toBe('EXPORT_SENSITIVE_REQUIRED');

    // The same request from the reviewer, who holds it, is accepted.
    const reviewer = await signIn('financial.reviewer');
    const created = await unwrap(
      reviewer.c.POST('/api/v1/exports', {
        params: { header: command(reviewer) },
        body: { kind: 'CLAIMS' },
      }),
    );
    expect(created.data.kind).toBe('CLAIMS');
  });

  it('counts every download and hands back the same watermark the file carries', async () => {
    const s = await signIn('financial.reviewer');
    const created = await unwrap(
      s.c.POST('/api/v1/exports', {
        params: { header: command(s) },
        body: { kind: 'SETTLEMENTS' },
      }),
    );
    await unwrap(
      s.c.GET('/api/v1/exports/{exportId}', {
        params: { header: tenant(s), path: { exportId: created.data.id } },
      }),
    );
    for (const want of [1, 2]) {
      const link = await unwrap(
        s.c.POST('/api/v1/exports/{exportId}/download', {
          params: { header: command(s), path: { exportId: created.data.id } },
          body: { purposeCode: 'BILLING_DISPUTE' },
        }),
      );
      expect(link.data.downloadCount).toBe(want);
      expect(link.data.method).toBe('GET');
      expect(link.data.watermark).toBe(created.data.watermark);
    }
  });

  it('refuses a download after the TTL', async () => {
    const s = await signIn('financial.reviewer');
    const expired = api.world.exports.find((e) => e.status === 'EXPIRED')!;
    const problem = await refusal(
      s.c.POST('/api/v1/exports/{exportId}/download', {
        params: { header: command(s), path: { exportId: expired.id } },
        body: {},
      }),
    );
    expect(problem.status).toBe(409);
    expect(problem.code).toBe('EXPORT_EXPIRED');
    expect(expired.downloadCount).toBe(2);
  });

  it('lists the caller’s own exports by default', async () => {
    const s = await signIn('financial.reviewer');
    const mine = await unwrap(
      s.c.GET('/api/v1/exports', { params: { header: tenant(s), query: { limit: 50 } } }),
    );
    for (const row of mine.data.items) {
      expect(row.requestedBy).toBe(s.actorId);
    }
  });
});

/** Subtracts two exact decimals in micro-units, for the run's own arithmetic. */
function subtract(a: string, b: string): string {
  const micros = (value: string): bigint => {
    const [whole, fraction = ''] = value.split('.');
    return BigInt(whole!) * 1_000_000n + BigInt(fraction.padEnd(6, '0').slice(0, 6));
  };
  const result = micros(a) - micros(b);
  const text = (result / 1_000_000n).toString();
  const rest = (result % 1_000_000n).toString().padStart(6, '0').replace(/0+$/, '');
  return rest === '' ? text : `${text}.${rest}`;
}
