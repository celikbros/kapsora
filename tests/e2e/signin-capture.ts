import { mkdirSync } from 'node:fs';
import { expect, type Page } from '@playwright/test';

/**
 * The sign-in screen of one app, captured for the design review: the widths the product is
 * built for (a desktop and a 390px phone) in both themes, with the page's own overflow check.
 *
 * It is a capture rather than an assertion of pixels: the review reads the images. The one
 * thing it does assert is the property no screenshot shows well — that nothing spills
 * sideways at phone width.
 */
const OUT = '.impeccable/review';

export async function captureSignIn(page: Page, name: string): Promise<void> {
  mkdirSync(OUT, { recursive: true });
  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme });
    for (const [width, height, label] of [
      [1440, 900, 'desktop'],
      [900, 1000, 'tablet'],
      [390, 844, 'phone'],
    ] as const) {
      await page.setViewportSize({ width, height });
      await page.goto('/auth/login');
      await expect(page.getByRole('heading', { level: 1, name: 'KAPSORA' })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Giriş yap' })).toBeVisible();
      await page.waitForLoadState('networkidle');
      await page.screenshot({
        path: `${OUT}/${name}-signin-${label}-${theme}.png`,
        fullPage: true,
      });
      const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
      expect(scrollWidth, `${name} ${label} ${theme}: yatay taşma`).toBeLessThanOrEqual(width);
    }
  }
}
