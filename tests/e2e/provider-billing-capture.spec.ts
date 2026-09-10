import { mkdirSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';

/** Captures the provider's billing screens for the finish review (REVIEW_CAPTURE=1 only). */
const PASSWORD = 'demo parola 2026 kapsora';
const OUT = '.impeccable/review';

test.skip(!process.env['REVIEW_CAPTURE'], 'set REVIEW_CAPTURE=1 to capture review screenshots');

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

async function settled(page: Page) {
  await page.waitForLoadState('networkidle');
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0, { timeout: 15_000 });
  await expect(page.getByText('Yükleniyor…')).toHaveCount(0, { timeout: 15_000 });
  await expect(page.locator('td', { hasText: /^…$/ })).toHaveCount(0, { timeout: 20_000 });
}

async function capture(page: Page, name: string) {
  mkdirSync(OUT, { recursive: true });
  await page.setViewportSize({ width: 1440, height: 900 });
  await settled(page);
  await page.screenshot({ path: `${OUT}/${name}-desktop.png`, fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await settled(page);
  const width = await page.evaluate(() => document.documentElement.scrollWidth);
  if (width > 390) {
    const offenders = await page.evaluate(() =>
      Array.from(document.querySelectorAll('body *'))
        .filter((el) => el.getBoundingClientRect().right > 392)
        .slice(0, 8)
        .map(
          (el) =>
            `${el.tagName.toLowerCase()}.${String(el.className).split(' ').slice(0, 4).join('.')}@${Math.round(el.getBoundingClientRect().right)}`,
        ),
    );
    throw new Error(
      `${name}: the page overflows the 390 viewport (${width}px): ${offenders.join(' | ')}`,
    );
  }
  await page.screenshot({ path: `${OUT}/${name}-mobile.png`, fullPage: true });
  await page.setViewportSize({ width: 1440, height: 900 });
}

test('provider portal: earnings, the invoice, the icmal', async ({ page }) => {
  await page.goto('/billing');
  await login(page, 'billing.a');
  await expect(page.getByRole('heading', { name: 'Hakediş' })).toBeVisible();
  await page.getByLabel(/^Başlangıç/).fill('2026-01-01');
  await expect(page.getByTestId('earnings-currency').first()).toBeVisible();
  await capture(page, 'provider-earnings');

  await page.getByRole('link', { name: 'Faturalar' }).first().click();
  await expect(page.getByTestId('invoice-table')).toBeVisible();
  await capture(page, 'provider-invoices');
  await page.getByLabel('Durum').selectOption('DRAFT');
  await page.getByTestId('invoice-row').first().getByRole('link').first().click();
  await expect(page.getByTestId('allocation-totals')).toBeVisible();
  await capture(page, 'provider-invoice');

  await page
    .getByRole('navigation', { name: 'Sağlayıcı portalı' })
    .getByRole('link', { name: 'Hakediş ve fatura' })
    .click();
  await page.getByRole('link', { name: 'İcmaller' }).first().click();
  await expect(page.getByTestId('batch-table')).toBeVisible();
  await capture(page, 'provider-batches');
  await page.getByLabel('Durum').selectOption('DRAFT');
  await page.getByTestId('batch-row').first().getByRole('link').first().click();
  await expect(page.getByTestId('membership-list')).toBeVisible();
  await capture(page, 'provider-batch');
});
