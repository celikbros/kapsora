import assert from 'node:assert/strict';
import test from 'node:test';

import { calendarState, eligibleUtcDay, matchingRunPair, micros } from './pc05-calendar-model.ts';

test('a due period is first eligible on the following UTC day, including month boundaries', () => {
  assert.equal(eligibleUtcDay('2026-10-14'), '2026-10-15');
  assert.equal(eligibleUtcDay('2026-10-29'), '2026-10-30');
  assert.equal(eligibleUtcDay('2026-12-31'), '2027-01-01');
  assert.equal(calendarState(new Date('2026-10-14T23:59:59Z'), '2026-10-14'), 'before');
  assert.equal(calendarState(new Date('2026-10-15T00:00:00Z'), '2026-10-14'), 'eligible-day');
  assert.equal(calendarState(new Date('2026-10-30T12:00:00Z'), '2026-10-14'), 'after');
  assert.throws(() => eligibleUtcDay('2026-02-30'));
});

test('decimal comparison uses exact millionths rather than floating point', () => {
  assert.equal(micros('125.50'), micros('125.500000'));
  assert.equal(micros('600'), micros('400.00') + micros('200'));
  assert.equal(micros('-0.000001'), -1n);
  assert.throws(() => micros('0.0000001'));
});

test('a successful job window matches exact provider and tenant runs even with unrelated differences', () => {
  type SyntheticRun = {
    scope: 'PROVIDER' | 'TENANT';
    providerOrganizationId: string | null;
    currencyCode: string;
    periodFrom: string;
    periodTo: string;
    status: string;
    ranAt: string;
  };
  const target = {
    providerOrganizationId: 'provider-a',
    currencyCode: 'TRY',
    dueDate: '2026-10-14',
  };
  const window = [{ startedAt: '2026-10-15T03:00:00Z', finishedAt: '2026-10-15T03:02:00Z' }];
  const own: SyntheticRun = {
    scope: 'PROVIDER',
    providerOrganizationId: 'provider-a',
    currencyCode: 'TRY',
    periodFrom: '2026-10-14',
    periodTo: '2026-10-14',
    status: 'DIFFERENCES',
    ranAt: '2026-10-15T03:01:00Z',
  };
  const tenant: SyntheticRun = { ...own, scope: 'TENANT', providerOrganizationId: null };
  assert.deepEqual(matchingRunPair([own], [tenant], window, target), { own, tenant });
  assert.equal(matchingRunPair([own], [tenant], [], target), undefined);
  assert.equal(
    matchingRunPair([{ ...own, ranAt: '2026-10-15T03:03:00Z' }], [tenant], window, target),
    undefined,
  );
  assert.equal(
    matchingRunPair([{ ...own, providerOrganizationId: 'provider-b' }], [tenant], window, target),
    undefined,
  );
  assert.equal(
    matchingRunPair([own], [{ ...tenant, scope: 'PROVIDER' }], window, target),
    undefined,
  );
  assert.equal(
    matchingRunPair([own], [{ ...tenant, status: 'FAILED' }], window, target),
    undefined,
  );
});
