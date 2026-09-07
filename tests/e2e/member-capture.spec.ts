import { mkdirSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';

/**
 * Captures the member PWA's screens for the Impeccable finish review, at 390 first and
 * 1440 second, into `.impeccable/review/`. Runs only when asked (REVIEW_CAPTURE=1).
 */
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
  await expect(page.locator('span, dd', { hasText: /^…$/ })).toHaveCount(0, { timeout: 20_000 });
  await expect(page.locator('dd', { hasText: / · …$/ })).toHaveCount(0, { timeout: 20_000 });
}

async function capture(page: Page, name: string) {
  mkdirSync(OUT, { recursive: true });
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
  await settled(page);
  await page.screenshot({ path: `${OUT}/${name}-desktop.png`, fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
}

function isoDaysFromNow(days: number): string {
  return new Date(Date.now() + days * 86_400_000).toISOString().slice(0, 10);
}

test('member: home, search with the receipt, the hold, the confirmed booking', async ({ page }) => {
  await page.goto('/');
  await login(page, 'member.a');
  await expect(page.getByTestId('remaining-list')).toBeVisible();
  await capture(page, 'member-home');

  await page.getByTestId('home-search').click();
  await page.getByLabel(/^Giriş/).fill(isoDaysFromNow(30));
  await page.getByLabel(/^Çıkış/).fill(isoDaysFromNow(33));
  await page.getByLabel(/^Nerede/).selectOption({ index: 1 });
  await page.getByRole('button', { name: 'Ara' }).click();
  const row = page
    .getByTestId('room-row')
    .filter({ has: page.getByTestId('room-member-amount') })
    .first();
  await row.getByRole('button', { name: 'Seç' }).click();
  await expect(page.getByTestId('receipt-member-amount')).toBeVisible();
  await capture(page, 'member-search');

  await page.getByTestId('receipt').getByRole('button', { name: 'Odayı tut' }).click();
  await expect(page.getByTestId('hold-countdown')).toBeVisible();
  await capture(page, 'member-hold');

  await page.getByTestId('confirm-button').click();
  await expect(page.getByTestId('booking-status')).toHaveText('Onaylandı');
  await page.getByRole('button', { name: 'Kuponu göster' }).click();
  await expect(page.getByTestId('voucher-token')).toBeVisible();
  await page.getByRole('button', { name: 'İptal edersem ne öderim?' }).click();
  await expect(page.getByTestId('cancellation-preview')).toBeVisible();
  // The confirmation toast is a moment, not the screen; let it go before the capture.
  await page
    .getByRole('region', { name: /Notifications/ })
    .getByRole('button')
    .first()
    .click()
    .catch(() => undefined);
  await capture(page, 'member-booking');

  await page.getByTestId('member-tabs').getByRole('link', { name: 'Rezervasyonlarım' }).click();
  await expect(page.getByTestId('booking-list')).toBeVisible();
  await capture(page, 'member-bookings');
});
