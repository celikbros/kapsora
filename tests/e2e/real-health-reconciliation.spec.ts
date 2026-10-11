import { mkdir } from 'node:fs/promises';
import { expect, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const runId = process.env['E2E_RECONCILIATION_PROVIDER_RUN'] ?? '';
const tenantRunId = process.env['E2E_RECONCILIATION_TENANT_RUN'] ?? '';

test.use({ trace: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || !runId || !tenantRunId,
  'requires existing operator-run demo and explicit historical scheduler run IDs',
);

function micros(value: string): bigint {
  expect(value).toMatch(/^-?\d+(?:\.\d{1,6})?$/);
  const negative = value.startsWith('-');
  const [whole, fraction = ''] = value.replace(/^-/, '').split('.');
  const amount = BigInt(whole!) * 1_000_000n + BigInt(fraction.padEnd(6, '0'));
  return negative ? -amount : amount;
}

// Read-only historical scheduler evidence. This does not run today's job or mark a
// newly paid future-due settlement RECONCILED; that predicate has isolated DB coverage.
test('historical reconciliation amounts, differences and provider boundary remain readable', async ({
  browser,
}) => {
  test.setTimeout(60_000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const providerPage = await browser.newPage();
  page.setDefaultTimeout(15_000);
  const finance = new Actor(page.request, 'backoffice', true);
  const provider = new Actor(providerPage.request, 'provider', true);
  try {
    await finance.login('financial.reviewer');
    await provider.login('billing.a');
    const path = `/api/v1/reconciliation-runs/${runId}`;
    const run = (await finance.call<S<'ReconciliationRun'>>('GET', path)).data;
    const tenant = (
      await finance.call<S<'ReconciliationRun'>>(
        'GET',
        `/api/v1/reconciliation-runs/${tenantRunId}`,
      )
    ).data;
    expect(run.scope).toBe('PROVIDER');
    expect(tenant.scope).toBe('TENANT');
    expect(run.periodFrom).toBe(tenant.periodFrom);
    expect(run.periodTo).toBe(tenant.periodTo);
    expect(run.status).toBe('DIFFERENCES');
    expect(run.currencyCode).toBe('TRY');
    expect(run.differences.length).toBeGreaterThan(0);
    expect(run.differenceCount).toBeGreaterThanOrEqual(run.differences.length);
    expect(micros(run.openTotal)).toBe(micros(run.settledTotal) - micros(run.paidTotal));
    expect(micros(run.difference)).toBe(
      micros(run.settledTotal) - micros(run.erpTotal ?? run.paidTotal),
    );
    for (const difference of run.differences) {
      expect(micros(difference.difference)).toBe(
        micros(difference.expected) - micros(difference.actual),
      );
    }
    expect((await provider.call<S<'ReconciliationRun'>>('GET', path)).data).toEqual(run);
    await provider.call('GET', `/api/v1/reconciliation-runs/${tenantRunId}`, undefined, {
      expected: 404,
    });
    await page.goto(base + `/billing/reconciliation/${runId}`);
    await expect(page.getByTestId('run-status')).toBeVisible();
    await expect(page.getByTestId('difference-row')).toHaveCount(run.differences.length);
    await mkdir('.impeccable/review/health-billing', { recursive: true });
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 1000 });
      for (const difference of run.differences) {
        await expect(
          page.getByTestId('difference-row').filter({ hasText: difference.reference }),
        ).toBeVisible();
      }
      await expect
        .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
        .toBe(true);
      await page.screenshot({
        path: `.impeccable/review/health-billing/reconciliation-${width}.png`,
        fullPage: true,
      });
    }
    expect((await finance.call<S<'ReconciliationRun'>>('GET', path)).data).toEqual(run);
    await test.info().attach('pc05-historical-reconciliation-evidence', {
      contentType: 'application/json',
      body: JSON.stringify({
        runId,
        tenantRunId,
        periodFrom: run.periodFrom,
        periodTo: run.periodTo,
        status: run.status,
        settledTotal: run.settledTotal,
        paidTotal: run.paidTotal,
        difference: run.difference,
        differenceCount: run.differenceCount,
      }),
    });
  } finally {
    await Promise.allSettled([finance.close(), provider.close()]);
    await Promise.allSettled([page.close(), providerPage.close()]);
  }
});
