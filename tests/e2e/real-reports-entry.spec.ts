import { mkdir } from 'node:fs/promises';
import { expect, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.use({ trace: 'off' });
test.skip(process.env['E2E_REAL_API'] !== '1' || !base, 'requires operator-started local system');

// Sessions and audited reads only. No export, download, payment or scheduler command.
for (const username of ['financial.reviewer', 'sponsor.hr', 'doctor.a']) {
  test(`reports entry respects read and export permissions for ${username}`, async ({
    browser,
  }) => {
    expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
    const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
    page.setDefaultTimeout(15_000);
    const actor = new Actor(page.request, 'backoffice', true);
    const dataReads: string[] = [];
    const commands: string[] = [];
    const refusedReads: string[] = [];
    try {
      await actor.login(username);
      const invoiceProviders =
        username === 'financial.reviewer'
          ? [
              ...new Map(
                (
                  await actor.call<components['schemas']['InvoicePage']>(
                    'GET',
                    '/api/v1/invoices?limit=100',
                  )
                ).data.items.map(
                  (invoice) => [invoice.providerOrganizationId, invoice.providerName] as const,
                ),
              ),
            ]
          : [];
      page.on('request', (request) => {
        const path = new URL(request.url()).pathname;
        if (/^\/api\/v1\/(exports|reconciliation-runs|organizations)(\/|$)/.test(path)) {
          if (request.method() === 'GET') dataReads.push(path);
          else commands.push(path);
        }
      });
      page.on('response', (response) => {
        const path = new URL(response.url()).pathname;
        if (
          path.startsWith('/api/v1/') &&
          response.request().method() === 'GET' &&
          [403, 404].includes(response.status())
        )
          refusedReads.push(path);
      });
      await page.goto(base + '/reports');
      const hub = page.getByTestId('reports-services');
      await expect(hub).toBeVisible();
      expect(dataReads, 'landing must not fetch report or organization records').toEqual([]);
      if (username === 'doctor.a') {
        await expect(
          hub.getByText('Bu i\u015flem i\u00e7in yetkiniz yok.', { exact: true }),
        ).toBeVisible();
        await expect(hub.locator('a')).toHaveCount(0);
        await expect(
          page
            .getByRole('navigation', { name: 'Ana men\u00fc' })
            .getByRole('link', { name: 'Raporlar', exact: true }),
        ).toHaveCount(0);
        await page.getByRole('link', { name: 'KAPSORA', exact: true }).click();
        await expect(page).toHaveURL(base + '/');
        await expect(page.getByRole('link', { name: 'Raporlar', exact: true })).toHaveCount(0);
        expect(commands).toEqual([]);
        expect(refusedReads).toEqual([]);
        return;
      }
      await expect(hub.getByTestId('reports-reconciliation')).toBeVisible();
      await expect(hub.getByTestId('reports-exports')).toBeVisible();
      await expect(
        page
          .getByRole('navigation', { name: 'Ana men\u00fc' })
          .getByRole('link', { name: 'Raporlar', exact: true }),
      ).toBeVisible();
      await mkdir('.impeccable/review/reports-entry', { recursive: true });
      for (const width of [1440, 390]) {
        await page.setViewportSize({ width, height: 1000 });
        await page.evaluate(() => window.scrollTo(0, 0));
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
          true,
        );
        await page.screenshot({
          path: `.impeccable/review/reports-entry/${username}-${width}.png`,
          fullPage: true,
        });
      }
      const runsLoaded = page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === '/api/v1/reconciliation-runs' &&
          r.request().method() === 'GET',
      );
      await hub.getByTestId('reports-reconciliation').click();
      expect((await runsLoaded).status()).toBe(200);
      await expect(page).toHaveURL(/\/billing\/reconciliation$/);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      await page.goto(base + '/reports');
      const exportsLoaded = page.waitForResponse(
        (r) => new URL(r.url()).pathname === '/api/v1/exports' && r.request().method() === 'GET',
      );
      await page.getByTestId('reports-exports').click();
      expect((await exportsLoaded).status()).toBe(200);
      await expect(page).toHaveURL(/\/billing\/exports$/);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      await expect(page.getByTestId('export-form')).toHaveCount(
        username === 'financial.reviewer' ? 1 : 0,
      );
      if (username === 'financial.reviewer') {
        expect(
          invoiceProviders.length,
          'existing hospital/hotel invoices must expose provider choices',
        ).toBeGreaterThan(1);
        await page.locator('select[name="kind"]').selectOption('PROVIDER_STATEMENT');
        const picker = page.getByTestId('export-provider-select');
        for (const [providerId] of invoiceProviders.slice(0, 2)) {
          await expect(picker.locator(`option[value="${providerId}"]`)).toHaveCount(1);
          await picker.selectOption(providerId);
          await expect(picker).toHaveValue(providerId);
        }
        expect(dataReads.filter((p) => p === '/api/v1/organizations')).toEqual([]);
      }
      if (username === 'sponsor.hr') {
        await expect(page.getByTestId('billing-nav').locator('a')).toHaveCount(3);
        await expect(
          page.getByTestId('billing-nav').locator('a[href="/billing/settlements"]'),
        ).toHaveCount(0);
        await expect(
          page.getByTestId('billing-nav').locator('a[href="/billing/reimbursements"]'),
        ).toHaveCount(0);
        expect(dataReads.filter((p) => p === '/api/v1/organizations')).toEqual([]);
        await expect(page.getByTestId('download-confirm')).toHaveCount(0);
      }
      for (const width of [1440, 390]) {
        await page.setViewportSize({ width, height: 1000 });
        await page.evaluate(() => window.scrollTo(0, 0));
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
          true,
        );
        await page.screenshot({
          path: `.impeccable/review/reports-entry/${username}-exports-${width}.png`,
          fullPage: true,
        });
      }
      expect(commands, 'navigation must not create or download exports').toEqual([]);
      expect(refusedReads, 'visible links must not cause refused list reads').toEqual([]);
    } finally {
      await actor.close();
      await page.close();
    }
  });
}
