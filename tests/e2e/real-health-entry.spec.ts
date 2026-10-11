import { mkdir } from 'node:fs/promises';
import { expect, test } from '@playwright/test';
import { Actor } from './real-api-actor';

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.use({ trace: 'off' });
test.skip(process.env['E2E_REAL_API'] !== '1' || !base, 'requires operator-started local system');

// Only navigate/read existing lists. No cases, reports, claims or payments are created.
for (const username of ['financial.reviewer', 'doctor.a', 'payer.approver']) {
  test(`health entry opens permitted working flows for ${username}`, async ({ browser }) => {
    expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
    const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
    page.setDefaultTimeout(15000);
    const actor = new Actor(page.request, 'backoffice', true);
    const dataReads: string[] = [];
    try {
      await actor.login(username);
      page.on('request', (request) => {
        const path = new URL(request.url()).pathname;
        if (
          /^\/api\/v1\/(health-cases|medical-reports|claims|service-requests|stays)(\/|$)/.test(
            path,
          )
        )
          dataReads.push(path);
      });
      await page.goto(base + '/health-services');
      const hub = page.getByTestId('health-services');
      await expect(hub).toBeVisible();
      await expect(page.getByText('Yak\u0131nda', { exact: true })).toHaveCount(0);
      expect(dataReads, 'landing must not fetch clinical or claim records').toEqual([]);
      if (username === 'payer.approver') {
        await expect(hub.locator('a')).toHaveCount(0);
        await expect(
          page
            .getByRole('navigation', { name: 'Ana men\u00fc' })
            .getByRole('link', { name: 'Sa\u011fl\u0131k', exact: true }),
        ).toHaveCount(0);
        return;
      }
      await mkdir('.impeccable/review/health-entry', { recursive: true });
      for (const width of [390, 1440]) {
        await page.setViewportSize({ width, height: 1000 });
        await page.evaluate(() => window.scrollTo(0, 0));
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
          true,
        );
        await page.screenshot({
          path: `.impeccable/review/health-entry/${username}-${width}.png`,
          fullPage: true,
        });
      }
      if (username === 'financial.reviewer') {
        await expect(hub.getByTestId('health-report-review')).toHaveCount(0);
        await expect(hub.getByTestId('health-medical-claims')).toHaveCount(0);
        const loaded = page.waitForResponse(
          (r) => new URL(r.url()).pathname === '/api/v1/claims' && r.request().method() === 'GET',
        );
        await hub.getByTestId('health-financial-claims').click();
        const response = await loaded;
        expect(response.status()).toBe(200);
        expect(new URL(response.url()).searchParams.get('status')).toBe('PENDING_FINANCIAL');
        await expect(page).toHaveURL(/\/claims\?status=PENDING_FINANCIAL/);
        await expect(page.locator('select[name="status"]')).toHaveValue('PENDING_FINANCIAL');
      } else {
        await expect(hub.getByTestId('health-financial-claims')).toHaveCount(0);
        await expect(hub.getByTestId('health-medical-claims')).toBeVisible();
        const loaded = page.waitForResponse(
          (r) =>
            new URL(r.url()).pathname === '/api/v1/medical-reports' &&
            r.request().method() === 'GET',
        );
        await hub.getByTestId('health-report-review').click();
        expect((await loaded).status()).toBe(200);
        await expect(page).toHaveURL(/\/medical-reports$/);
      }
    } finally {
      await actor.close();
      await page.close();
    }
  });
}
