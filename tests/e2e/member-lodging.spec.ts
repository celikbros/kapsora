import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'demo parola 2026 kapsora';

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

function isoDaysFromNow(days: number): string {
  return new Date(Date.now() + days * 86_400_000).toISOString().slice(0, 10);
}

/**
 * The member on a phone: reads what is left, searches, sees what they would pay, holds,
 * sees the same amount on the confirmation, confirms, and gets the voucher on request. The
 * amount is asserted as the same string at every step, which is the milestone's own
 * criterion — and none of it is computed on the client.
 */
test('a member searches, holds, confirms and sees the voucher, with the amount on every step', async ({
  page,
}) => {
  await page.goto('/');
  await login(page, 'member.a');
  await expect(page.getByTestId('remaining-list')).toBeVisible();
  await expect(page.getByTestId('remaining-row').first()).toContainText('gece');

  await page.getByTestId('home-search').click();
  await expect(page.getByRole('heading', { name: 'Konaklama ara' })).toBeVisible();
  await page.getByLabel(/^Giriş/).fill(isoDaysFromNow(30));
  await page.getByLabel(/^Çıkış/).fill(isoDaysFromNow(33));
  const where = page.getByLabel(/^Nerede/);
  await where.selectOption({ index: 1 });
  await page.getByRole('button', { name: 'Ara' }).click();

  const row = page
    .getByTestId('room-row')
    .filter({ has: page.getByTestId('room-member-amount') })
    .first();
  await expect(row).toBeVisible();
  const shown = (await row.getByTestId('room-member-amount').textContent())!.trim();
  expect(shown).toMatch(/TRY$/);

  await row.getByRole('button', { name: 'Seç' }).click();
  const receipt = page.getByTestId('receipt');
  await expect(receipt.getByTestId('receipt-member-amount')).toHaveText(shown);
  await receipt.getByRole('button', { name: 'Odayı tut' }).click();

  await expect(page).toHaveURL(/\/bookings\/[0-9a-f-]+$/);
  await expect(page.getByTestId('hold-countdown')).toBeVisible();
  await expect(page.getByTestId('booking-status')).toHaveText('Tutuldu');
  await expect(page.getByTestId('confirm-button')).toContainText(`Ödeyeceğiniz ${shown}`);
  await expect(page.getByTestId('receipt-member-amount')).toHaveText(shown);

  await page.getByTestId('confirm-button').click();
  await expect(page.getByTestId('booking-status')).toHaveText('Onaylandı');
  await page.getByRole('button', { name: 'Kuponu göster' }).click();
  await expect(page.getByTestId('voucher-token')).toBeVisible();
  // The token lives on the screen and nowhere else.
  const stored = await page.evaluate(
    () => window.localStorage.length + window.sessionStorage.length,
  );
  expect(stored).toBe(0);
});

test('the bookings list and the tab bar are the member’s way around', async ({ page }) => {
  await page.goto('/bookings');
  await login(page, 'member.a');
  await expect(page.getByTestId('booking-list')).toBeVisible();
  await expect(page.getByTestId('booking-row').first()).toBeVisible();
  await expect(page.getByTestId('member-tabs')).toBeVisible();
});
