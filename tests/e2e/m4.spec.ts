import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'demo parola 2026 kapsora';

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

/**
 * M4 backoffice: a request from draft to approved without any screen writing a status.
 * The submit lands on whatever the gate decides; only a request under review can be
 * approved, so the flow says which it found rather than assuming.
 */
test('a draft is submitted and a request under review is approved', async ({ page }) => {
  await page.goto('/requests');
  await login(page, 'admin.a');
  const table = page.getByTestId('request-table');
  await expect(table).toBeVisible();
  // Filters go through the page: a full navigation would start a new mock session.
  await page.getByLabel('Durum').selectOption('DRAFT');
  await expect(table.locator('tbody tr').first()).toContainText('Taslak');
  await table.getByRole('link').first().click();
  await expect(page.getByTestId('request-commands')).toBeVisible();

  // Submit is its own command with its own confirmation.
  await page.getByRole('button', { name: 'Gönder' }).click();
  const confirm = page.getByRole('dialog', { name: 'Gönder' });
  await confirm.getByRole('button', { name: 'Gönder' }).click();
  await expect(confirm).toBeHidden();
  // The status that comes back is the gate's decision, and never SUBMITTED.
  await expect(page.getByRole('button', { name: 'Gönder' })).toBeHidden();
  // The version list may legitimately say a version was sent; the request itself never rests there.
  await expect(page.getByTestId('request-status')).not.toHaveText('Gönderildi');

  // Approval: on a request the gate sent to review.
  await page.getByRole('link', { name: 'Talepler ve Onaylar' }).click();
  await page.getByLabel('Durum').selectOption('PENDING_REVIEW');
  await expect(page.getByTestId('request-table').locator('tbody tr').first()).toContainText(
    'İncelemede',
  );
  await page.getByTestId('request-table').getByRole('link').first().click();
  await page.getByRole('button', { name: 'Onayla', exact: true }).click();
  const approve = page.getByRole('dialog', { name: 'Onayla' });
  await approve.getByRole('button', { name: 'Onayla' }).click();
  await expect(approve).toBeHidden();
  await expect(page.getByTestId('request-commands')).toContainText('İptal et');
  await expect(page.getByTestId('request-commands')).not.toContainText('Onayla');
});

test('the worklist offers a claim and a rejected request offers nothing', async ({ page }) => {
  await page.goto('/worklist');
  await login(page, 'admin.a');
  await page.getByRole('button', { name: 'Sahipsiz' }).click();
  const table = page.getByTestId('worklist-table');
  await expect(table).toBeVisible();
  await table.getByRole('button', { name: 'Üstlen' }).first().click();
  await page.getByRole('button', { name: 'Bende' }).click();
  await expect(page.getByTestId('worklist-table')).toContainText('siz');

  await page.getByRole('link', { name: 'Talepler ve Onaylar' }).click();
  await page.getByLabel('Durum').selectOption('REJECTED');
  await expect(page.getByTestId('request-table').locator('tbody tr').first()).toContainText(
    'Reddedildi',
  );
  await page.getByTestId('request-table').getByRole('link').first().click();
  await expect(page.getByTestId('rejected-panel')).toBeVisible();
  await expect(page.getByTestId('request-commands')).toHaveCount(0);
});
