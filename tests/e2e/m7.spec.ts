import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'demo parola 2026 kapsora';

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

/**
 * The payer decides an icmal: a cut with a reason on the one invoice still waiting, then
 * the close, with the totals the server computed; then the settlement list through the
 * section's own strip.
 */
test('the reviewer takes a cut, closes the icmal, and reads the settlements', async ({ page }) => {
  await page.goto('/billing/batches');
  await login(page, 'financial.reviewer');
  await expect(page.getByRole('heading', { name: 'İcmal incelemesi' })).toBeVisible();
  await page.getByLabel('Durum').selectOption('UNDER_REVIEW');
  const table = page.getByTestId('batch-table');
  await expect(table.getByTestId('batch-row').first()).toContainText('İncelemede');
  await table.getByTestId('batch-row').first().getByRole('link').click();
  await expect(page).toHaveURL(/\/billing\/batches\/[0-9a-f-]+$/);
  await expect(page.getByTestId('pending-count')).toContainText('1 fatura karar bekliyor');
  await expect(page.getByTestId('decide-batch')).toHaveCount(0);

  const pending = page.getByTestId('review-row').filter({ hasText: 'Karar bekliyor' }).first();
  await pending.getByRole('button', { name: 'Fatura kararı' }).click();
  const form = page.getByTestId('decision-form');
  await form.getByLabel(/^Fatura kararı/).selectOption('CUT');
  await form.getByLabel(/^Onaylanan tutar/).fill('500');
  await form.getByLabel(/^Gerekçe/).selectOption('TARIFF_EXCEEDED');
  await page.getByTestId('save-decision').click();
  await expect(page.getByTestId('pending-count')).toContainText('0 fatura karar bekliyor');

  await page.getByTestId('decide-batch').click();
  await expect(page.getByTestId('batch-status')).toHaveText('Kararlaştırıldı');
  await expect(page.getByTestId('batch-totals').getByTestId('total-cut')).toContainText('TRY');

  await page.getByTestId('billing-nav').getByRole('link', { name: 'Ödeme mutabakatları' }).click();
  await expect(page.getByTestId('settlement-table')).toBeVisible();
  await page.getByTestId('settlement-row').first().getByRole('link').click();
  await expect(page.getByTestId('settlement-figures')).toBeVisible();
  await expect(page.getByTestId('payable')).toContainText('TRY');

  await page.getByTestId('billing-nav').getByRole('link', { name: 'Günlük mutabakat' }).click();
  await expect(page.getByTestId('run-table')).toBeVisible();
  await page
    .getByTestId('run-row')
    .filter({ hasText: 'Fark var' })
    .first()
    .getByRole('link')
    .click();
  await expect(page.getByTestId('run-status')).toHaveText('Fark var');
  await expect(page.getByTestId('difference-row').first()).toBeVisible();

  await page.getByTestId('billing-nav').getByRole('link', { name: 'Dışa aktarım' }).click();
  await expect(page.getByTestId('export-form')).toBeVisible();
  await page.getByTestId('request-export').click();
  await expect(page.getByTestId('download-export').first()).toBeVisible({ timeout: 15_000 });
});
