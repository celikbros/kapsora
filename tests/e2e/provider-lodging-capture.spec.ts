import { mkdirSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';

/** Captures the property desk's screens for the finish review (REVIEW_CAPTURE=1 only). */
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

test('provider portal: the allotment and the door', async ({ page }) => {
  await page.goto('/lodging/inventory');
  await login(page, 'reservation.a');
  await page.getByLabel(/^Tesis/).selectOption({ index: 1 });
  await page.getByLabel(/^Oda tipi/).selectOption({ index: 1 });
  await expect(page.getByTestId('inventory-table')).toBeVisible();
  await capture(page, 'provider-inventory');

  await page
    .getByRole('navigation', { name: 'Sağlayıcı portalı' })
    .getByRole('link', { name: 'Gelenler / Gidenler' })
    .click();
  await expect(page.getByTestId('arrival-row').first()).toBeVisible();
  await page.getByTestId('arrival-row').first().getByRole('button', { name: 'Gelmedi' }).click();
  await expect(page.getByTestId('no-show-form')).toBeVisible();
  await capture(page, 'provider-desk');
});
