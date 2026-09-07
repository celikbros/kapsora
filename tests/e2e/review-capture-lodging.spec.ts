import { mkdirSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';

/** Captures the payer's lodging screens for the finish review (REVIEW_CAPTURE=1 only). */
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

test('backoffice: bookings, a booking with its no-show review, the waiting list, the properties, the terms', async ({
  page,
}) => {
  await page.goto('/lodging/bookings');
  await login(page, 'admin.a');
  await expect(page.getByTestId('booking-table')).toBeVisible();
  await capture(page, 'lodging-bookings');

  // The booking somebody reported as a no-show: the review sits on its detail. Walk the
  // rows until one carries the review; the first row is the fallback so the capture is
  // always a detail.
  const rows = page.getByTestId('booking-row');
  const count = await rows.count();
  let opened = false;
  for (let i = 0; i < count && !opened; i += 1) {
    await rows.nth(i).getByRole('link').click();
    await expect(page.getByTestId('booking-sequence')).toBeVisible();
    if ((await page.getByTestId('no-show-review').count()) > 0) {
      opened = true;
    } else {
      await page.goBack();
      await expect(page.getByTestId('booking-table')).toBeVisible();
    }
  }
  if (!opened) {
    await rows.first().getByRole('link').click();
    await expect(page.getByTestId('booking-sequence')).toBeVisible();
  }
  await capture(page, 'lodging-booking');
  // Back to the list through its breadcrumb; the section nav lives on the lists.
  await page.getByRole('link', { name: 'Rezervasyonlar' }).first().click();
  await expect(page.getByTestId('booking-table')).toBeVisible();

  await page.getByTestId('lodging-nav').getByRole('link', { name: 'Bekleme listesi' }).click();
  await expect(page.getByTestId('waitlist-table')).toBeVisible();
  await capture(page, 'lodging-waitlist');

  await page.getByTestId('lodging-nav').getByRole('link', { name: 'Tesisler' }).click();
  await expect(page.getByTestId('property-table')).toBeVisible();
  await capture(page, 'lodging-properties');

  // The lodging terms on a contract version: the first contract's first version.
  await page
    .getByRole('navigation', { name: 'Ana menü' })
    .getByRole('link', { name: 'Sözleşmeler' })
    .click();
  await expect(page.getByTestId('contract-table')).toBeVisible();
  await page
    .getByTestId('contract-table')
    .locator('tbody tr')
    .first()
    .getByRole('link')
    .first()
    .click();
  await expect(page.getByTestId('contract-version-table')).toBeVisible();
  await page
    .getByTestId('contract-version-table')
    .locator('tbody tr')
    .first()
    .getByRole('link')
    .first()
    .click();
  await expect(page.getByTestId('lodging-terms')).toBeVisible();
  await capture(page, 'lodging-terms');
});
