import { execFile } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';

import { Actor } from './real-api-actor';
import { calendarState, eligibleUtcDay, matchingRunPair, micros } from './pc05-calendar-model';

type S<K extends keyof components['schemas']> = components['schemas'][K];
type Payment = { id: string; amount: string; status: 'RECORDED' | 'RECONCILED' | 'DISPUTED' };
type Target = {
  label: 'health' | 'hotel';
  providerUsername: string;
  settlementId: string;
  providerOrganizationId: string;
  dueDate: string;
  currencyCode: string;
  payableAmount: string;
  paidAmount: string;
  payments: Payment[];
};
type JobWindow = { startedAt: string; finishedAt: string };

const runFile = promisify(execFile);
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const privateConfig = process.env['E2E_PC05_CALENDAR_CONFIG'] ?? '';
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Actor's transport assertions include paths; this checker keeps private IDs out of failures. */
async function safeGet<T>(actor: Actor, path: string, expected = 200): Promise<T> {
  try {
    return (await actor.call<T>('GET', path, undefined, { expected })).data;
  } catch {
    throw new Error('Read-only PC05 API check failed');
  }
}

test.use({ trace: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' ||
    !base ||
    !privateConfig ||
    !process.env['KAPSORA_DATABASE_URL'],
  'requires an existing local UI/API, read-only DB access, and explicit ignored private baseline config',
);

function ensure(ok: unknown, message: string): asserts ok {
  if (!ok) throw new Error(message);
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** Parse private evidence without ever embedding its values in a failure message. */
async function baseline(label: Target['label']): Promise<Target> {
  let input: unknown;
  try {
    input = JSON.parse(await readFile(privateConfig, 'utf8')) as unknown;
  } catch {
    throw new Error('Private PC05 calendar baseline is unreadable');
  }
  ensure(
    record(input) && Array.isArray(input['targets']),
    'Invalid private PC05 calendar baseline',
  );
  const targets = input['targets'] as unknown[];
  ensure(targets.length === 2, 'Private baseline must contain both retained cases');
  const target = targets.find((item) => record(item) && item['label'] === label);
  ensure(record(target), 'Private baseline is missing a retained case');
  ensure(
    typeof target['providerUsername'] === 'string' && target['providerUsername'].length > 0,
    'Invalid private provider actor',
  );
  ensure(
    typeof target['settlementId'] === 'string' && uuid.test(target['settlementId']),
    'Invalid private settlement identity',
  );
  ensure(
    typeof target['providerOrganizationId'] === 'string' &&
      uuid.test(target['providerOrganizationId']),
    'Invalid private provider identity',
  );
  ensure(typeof target['dueDate'] === 'string', 'Invalid private due period');
  eligibleUtcDay(target['dueDate']);
  ensure(
    typeof target['currencyCode'] === 'string' && /^[A-Z]{3}$/.test(target['currencyCode']),
    'Invalid private currency',
  );
  ensure(
    typeof target['payableAmount'] === 'string' && typeof target['paidAmount'] === 'string',
    'Invalid private amount',
  );
  ensure(
    micros(target['payableAmount']) === micros(target['paidAmount']),
    'Private baseline was not fully paid',
  );
  ensure(
    Array.isArray(target['payments']) && target['payments'].length > 0,
    'Private baseline has no payment records',
  );
  const payments = target['payments'] as unknown[];
  let paid = 0n;
  const ids = new Set<string>();
  for (const item of payments) {
    ensure(
      record(item) && typeof item['id'] === 'string' && uuid.test(item['id']),
      'Invalid private payment identity',
    );
    ensure(!ids.has(item['id']), 'Duplicate private payment identity');
    ids.add(item['id']);
    ensure(typeof item['amount'] === 'string', 'Invalid private payment amount');
    ensure(
      item['status'] === 'RECORDED' ||
        item['status'] === 'RECONCILED' ||
        item['status'] === 'DISPUTED',
      'Invalid private payment status',
    );
    if (item['status'] !== 'DISPUTED') paid += micros(item['amount']);
  }
  ensure(paid === micros(target['paidAmount']), 'Private baseline payment total is inconsistent');
  return target as Target;
}

async function jobWindows(eligible: string): Promise<JobWindow[]> {
  const helper = fileURLToPath(new URL('./pc05-calendar-job.go', import.meta.url));
  try {
    const { stdout } = await runFile('go', ['run', helper, eligible], {
      cwd: resolve(fileURLToPath(new URL('../..', import.meta.url))),
      windowsHide: true,
      maxBuffer: 64 * 1024,
      timeout: 25_000,
    });
    const result = JSON.parse(stdout) as unknown;
    ensure(
      record(result) && Array.isArray(result['windows']),
      'Invalid scheduler evidence response',
    );
    return result['windows'] as JobWindow[];
  } catch {
    throw new Error('Read-only scheduler job evidence could not be checked');
  }
}

async function runs(
  actor: Actor,
  scope: 'PROVIDER' | 'TENANT',
  target: Target,
): Promise<S<'ReconciliationRun'>[]> {
  const items: S<'ReconciliationRun'>[] = [];
  let cursor: string | null = null;
  const seen = new Set<string>();
  for (let pageNo = 0; pageNo < 10; pageNo++) {
    const query = new URLSearchParams({
      scope,
      periodFrom: target.dueDate,
      periodTo: target.dueDate,
      limit: '100',
    });
    if (scope === 'PROVIDER') query.set('providerOrganizationId', target.providerOrganizationId);
    if (cursor) query.set('cursor', cursor);
    const page = await safeGet<S<'ReconciliationRunPage'>>(
      actor,
      `/api/v1/reconciliation-runs?${query}`,
    );
    items.push(...page.items);
    if (!page.nextCursor) return items;
    ensure(!seen.has(page.nextCursor), 'Reconciliation cursor repeated');
    seen.add(page.nextCursor);
    cursor = page.nextCursor;
  }
  throw new Error('Reconciliation run list exceeded bounded read');
}

function checkArithmetic(run: S<'ReconciliationRun'>): void {
  ensure(
    micros(run.openTotal) === micros(run.settledTotal) - micros(run.paidTotal),
    'Reconciliation open total is inconsistent',
  );
  ensure(
    micros(run.difference) === micros(run.settledTotal) - micros(run.erpTotal ?? run.paidTotal),
    'Reconciliation difference is inconsistent',
  );
  for (const difference of run.differences) {
    ensure(
      micros(difference.difference) === micros(difference.expected) - micros(difference.actual),
      'Reconciliation line difference is inconsistent',
    );
  }
}

for (const label of ['hotel', 'health'] as const) {
  test(`retained ${label} payment is reconciled by its natural UTC-day job`, async () => {
    test.setTimeout(90_000);
    ensure(
      new URL(base).hostname === 'localhost' || new URL(base).hostname === '127.0.0.1',
      'Existing UI must be local',
    );
    const target = await baseline(label);
    const state = calendarState(new Date(), target.dueDate);
    test.skip(
      state === 'before',
      'PENDING: target due period has not reached its first eligible UTC scheduler day',
    );
    const windows = await jobWindows(eligibleUtcDay(target.dueDate));
    test.skip(
      state === 'eligible-day' && windows.length === 0,
      'PENDING: eligible UTC day is open but successful scheduler execution is not yet recorded',
    );
    ensure(
      windows.length > 0,
      'NOT VERIFIED: eligible UTC day passed without a successful billing.reconcile job',
    );

    const finance = new Actor(await apiRequest.newContext(), 'backoffice', true);
    const provider = new Actor(await apiRequest.newContext(), 'provider', true);
    const otherLabel = label === 'hotel' ? 'health' : 'hotel';
    const other = new Actor(await apiRequest.newContext(), 'provider', true);
    try {
      await finance.login('financial.reviewer');
      await provider.login(target.providerUsername);
      await other.login((await baseline(otherLabel)).providerUsername);
      const path = `/api/v1/settlements/${target.settlementId}`;
      const settlement = await safeGet<S<'Settlement'>>(finance, path);
      ensure(settlement.id === target.settlementId, 'Retained settlement identity changed');
      ensure(
        settlement.providerOrganizationId === target.providerOrganizationId,
        'Retained provider identity changed',
      );
      ensure(
        settlement.dueDate === target.dueDate && settlement.currencyCode === target.currencyCode,
        'Retained due period or currency changed',
      );
      ensure(
        micros(settlement.payableAmount) === micros(target.payableAmount),
        'Retained payable amount changed',
      );
      ensure(
        micros(settlement.paidAmount) === micros(target.paidAmount),
        'Retained paid amount changed',
      );
      ensure(
        settlement.status === 'RECONCILED',
        'Retained fully paid settlement is not RECONCILED',
      );
      ensure(
        settlement.payments.length === target.payments.length,
        'Retained payment count changed',
      );
      const actualPayments = new Map(settlement.payments.map((item) => [item.id, item]));
      let paid = 0n;
      for (const expected of target.payments) {
        const payment = actualPayments.get(expected.id);
        ensure(payment !== undefined, 'Retained payment identity changed');
        ensure(
          micros(payment.amount) === micros(expected.amount) && payment.status === expected.status,
          'Retained payment amount or status changed',
        );
        ensure(
          payment.settlementId === settlement.id &&
            payment.providerOrganizationId === target.providerOrganizationId &&
            payment.currencyCode === target.currencyCode,
          'Retained payment scope changed',
        );
        if (payment.status !== 'DISPUTED') paid += micros(payment.amount);
      }
      ensure(
        paid === micros(settlement.paidAmount),
        'Retained payment sum no longer equals paid amount',
      );
      ensure(
        JSON.stringify(await safeGet<S<'Settlement'>>(provider, path)) ===
          JSON.stringify(settlement),
        'Provider and finance settlement reads disagree',
      );
      await safeGet(other, path, 404);

      const providerRuns = await runs(finance, 'PROVIDER', target);
      const tenantRuns = await runs(finance, 'TENANT', target);
      const pair = matchingRunPair(providerRuns, tenantRuns, windows, target);
      ensure(
        pair !== undefined,
        'No matching provider and tenant runs within a successful eligible-day job',
      );
      checkArithmetic(pair.own);
      checkArithmetic(pair.tenant);
      ensure(
        !pair.own.differences.some((item) => item.reference === settlement.reference),
        'Retained paid settlement remains a reconciliation difference',
      );
      ensure(
        micros(pair.own.settledTotal) >= micros(settlement.payableAmount) &&
          micros(pair.own.paidTotal) >= micros(settlement.paidAmount),
        'Provider run omits retained settlement amount',
      );
      ensure(
        micros(pair.tenant.settledTotal) >= micros(pair.own.settledTotal) &&
          micros(pair.tenant.paidTotal) >= micros(pair.own.paidTotal),
        'Tenant run does not include provider totals',
      );
      ensure(
        JSON.stringify(
          await safeGet<S<'ReconciliationRun'>>(
            provider,
            `/api/v1/reconciliation-runs/${pair.own.id}`,
          ),
        ) === JSON.stringify(pair.own),
        'Provider run read differs from finance read',
      );
      await safeGet(provider, `/api/v1/reconciliation-runs/${pair.tenant.id}`, 404);
      await safeGet(other, `/api/v1/reconciliation-runs/${pair.own.id}`, 404);
    } finally {
      await Promise.allSettled([finance.close(), provider.close(), other.close()]);
    }
  });
}
