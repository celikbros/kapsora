import { mkdirSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';

/**
 * The member app's help at its own viewport, a 390px phone: the page help drawer over the
 * home page — full width there — and the help mark beside "Kalan haklarınız", opened by a
 * press because a finger has no hover. Both themes.
 */
const PASSWORD = 'demo parola 2026 kapsora';
const OUT = '.impeccable/review';

test.skip(!process.env['REVIEW_CAPTURE'], 'set REVIEW_CAPTURE=1 to capture review screenshots');

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

test('member: the page help drawer and the entitlements mark on a phone', async ({ page }) => {
  mkdirSync(OUT, { recursive: true });
  await page.goto('/');
  await login(page, 'member.a');
  await expect(page.getByText('Kalan haklarınız')).toBeVisible();

  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme });

    await page.getByRole('button', { name: 'Yardım' }).click();
    const drawer = page.getByRole('dialog', { name: 'Ana sayfa' });
    await expect(drawer).toBeVisible();
    await page.screenshot({ path: `${OUT}/member-help-drawer-phone-${theme}.png` });
    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();

    await page.getByRole('button', { name: 'Yardım: Hak cüzdanı' }).click();
    const hint = page.getByRole('dialog');
    await expect(hint).toBeVisible();
    await page.screenshot({ path: `${OUT}/member-help-hint-phone-${theme}.png` });
    await page.keyboard.press('Escape');
    await expect(hint).toBeHidden();
  }
});
