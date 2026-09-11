import { mkdirSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';

/** Captures the payer's billing screens for the finish review (REVIEW_CAPTURE=1 only). */
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
  await expect(page.locator('td, dd', { hasText: /^…$/ })).toHaveCount(0, { timeout: 20_000 });
}

async function dismissToasts(page: Page) {
  for (const close of await page.locator('.k-toast button[aria-label="Kapat"]').all()) {
    await close.click().catch(() => undefined);
  }
  await expect(page.locator('.k-toast')).toHaveCount(0, { timeout: 10_000 });
}

async function capture(page: Page, name: string) {
  mkdirSync(OUT, { recursive: true });
  await dismissToasts(page);
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

test('backoffice: the icmal review with a decision form, the settlement, the reimbursement', async ({
  page,
}) => {
  await page.goto('/billing/batches');
  await login(page, 'financial.reviewer');
  await expect(page.getByTestId('batch-table')).toBeVisible();
  await capture(page, 'billing-batches');

  await page.getByLabel('Durum').selectOption('UNDER_REVIEW');
  await page.getByTestId('batch-row').first().getByRole('link').click();
  await expect(page.getByTestId('review-table')).toBeVisible();
  await page
    .getByTestId('review-row')
    .filter({ hasText: 'Karar bekliyor' })
    .first()
    .getByRole('button', { name: 'Fatura kararı' })
    .click();
  await page
    .getByTestId('decision-form')
    .getByLabel(/^Fatura kararı/)
    .selectOption('CUT');
  await expect(page.getByTestId('batch-totals')).toBeVisible();
  await capture(page, 'billing-batch-review');

  await page.getByTestId('billing-nav').getByRole('link', { name: 'Ödeme mutabakatları' }).click();
  await expect(page.getByTestId('settlement-table')).toBeVisible();
  await capture(page, 'billing-settlements');
  await page.getByLabel('Durum').selectOption('PENDING_APPROVAL');
  await page.getByTestId('settlement-row').first().getByRole('link').click();
  await expect(page.getByTestId('settlement-figures')).toBeVisible();
  await capture(page, 'billing-settlement');

  await page
    .getByTestId('billing-nav')
    .getByRole('link', { name: 'Geri ödeme incelemesi' })
    .click();
  await expect(page.getByTestId('reimbursement-table')).toBeVisible();
  await capture(page, 'billing-reimbursements');
  await page.getByLabel('Durum').selectOption('SUBMITTED');
  await page.getByTestId('reimbursement-row').first().getByRole('link').click();
  await expect(page.getByTestId('reimbursement-decision')).toBeVisible();
  await capture(page, 'billing-reimbursement');

  await page.getByTestId('billing-nav').getByRole('link', { name: 'Günlük mutabakat' }).click();
  await expect(page.getByTestId('run-table')).toBeVisible();
  await capture(page, 'billing-reconciliation');
  // At 390 the strip scrolls and the list the reader is on is in view.
  await page.setViewportSize({ width: 390, height: 844 });
  const strip = page.getByTestId('billing-nav');
  await expect
    .poll(() => strip.evaluate((el) => el.scrollWidth > el.clientWidth && el.scrollLeft > 0))
    .toBe(true);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page
    .getByTestId('run-row')
    .filter({ hasText: 'Fark var' })
    .first()
    .getByRole('link')
    .click();
  await expect(page.getByTestId('difference-row').first()).toBeVisible();
  await capture(page, 'billing-run');

  await page.getByTestId('billing-nav').getByRole('link', { name: 'Dışa aktarım' }).click();
  await expect(page.getByTestId('export-form')).toBeVisible();
  await page.getByTestId('request-export').click();
  await expect(page.getByTestId('download-export').first()).toBeVisible({ timeout: 15_000 });
  await capture(page, 'billing-exports');
  await page.getByTestId('download-export').first().click();
  await expect(page.getByTestId('download-purpose')).toBeVisible();
  await capture(page, 'billing-export-purpose');
  // Still open after the width change, and it refuses an unchosen purpose in place.
  await expect(page.getByTestId('download-purpose')).toBeVisible();
  await page.getByTestId('download-confirm').click();
  await expect(page.getByText('İndirmenin amacını seçin.')).toBeVisible();
  await capture(page, 'billing-export-purpose-error');
  await page.keyboard.press('Escape');
  await expect(page.getByTestId('download-purpose')).toHaveCount(0);

  await page
    .getByRole('navigation', { name: 'Ana menü' })
    .getByRole('link', { name: 'Ana Sayfa' })
    .click();
  await expect(page.getByTestId('dashboard')).toBeVisible();
  await capture(page, 'billing-dashboard');
});
