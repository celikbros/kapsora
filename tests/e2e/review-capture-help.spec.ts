import { mkdirSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';

/**
 * The in-product help, captured for the design review: the page help drawer over the
 * worklist and a help mark opened on its column header, at a desktop and a 390px phone, in
 * both themes. See review-capture.spec.ts for the ground rules of these captures.
 */
const PASSWORD = 'demo parola 2026 kapsora';
const OUT = '.impeccable/review';

test.skip(!process.env['REVIEW_CAPTURE'], 'set REVIEW_CAPTURE=1 to capture review screenshots');

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

test('backoffice: the page help drawer and a help mark on the worklist', async ({ page }) => {
  mkdirSync(OUT, { recursive: true });
  await page.goto('/worklist');
  await login(page, 'admin.a');
  await expect(page.getByRole('heading', { level: 1, name: 'İş listem' })).toBeVisible();

  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme });
    for (const [width, height, label] of [
      [1440, 900, 'desktop'],
      [390, 844, 'phone'],
    ] as const) {
      await page.setViewportSize({ width, height });
      await page.getByRole('button', { name: 'Yardım' }).click();
      const drawer = page.getByRole('dialog', { name: 'İş listem' });
      await expect(drawer).toBeVisible();
      await expect(drawer.getByRole('heading', { level: 3, name: 'Durumlar' })).toBeVisible();
      await page.screenshot({ path: `${OUT}/help-drawer-${label}-${theme}.png` });
      await page.keyboard.press('Escape');
      await expect(drawer).toBeHidden();
    }

    // The mark on the "Termin" column header, opened by a press so a phone can follow.
    await page.setViewportSize({ width: 1440, height: 900 });
    const mark = page.getByRole('button', { name: /^Açıklama: / }).first();
    await mark.click();
    const hint = page.getByRole('dialog');
    await expect(hint).toBeVisible();
    await page.screenshot({ path: `${OUT}/help-hint-desktop-${theme}.png` });
    await page.keyboard.press('Escape');
    await expect(hint).toBeHidden();
  }
});
