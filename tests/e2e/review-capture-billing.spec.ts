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
});
