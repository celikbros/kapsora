import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'demo parola 2026 kapsora';

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

/**
 * The provider's billing desk: the invoice whose allocations do not make its total is
 * refused by the server with the difference already on the screen, and the icmal built
 * from submitted invoices is sent. Navigation stays in the app: a full page load starts a
 * new mock session.
 */
test('the billing desk sees the allocation difference, is refused, and sends an icmal', async ({
  page,
}) => {
  await page.goto('/billing/invoices');
  await login(page, 'billing.a');
  await expect(page.getByRole('heading', { name: 'Faturalar' })).toBeVisible();
  await page.getByLabel('Durum').selectOption('DRAFT');
  const table = page.getByTestId('invoice-table');
  await expect(table.getByTestId('invoice-row').first()).toContainText('Taslak');
  // The draft that does not add up is the older of the two drafts; walk the rows until the
  // difference on the detail is not zero.
  const rows = table.getByTestId('invoice-row');
  const count = await rows.count();
  let found = false;
  for (let i = 0; i < count && !found; i += 1) {
    await rows.nth(i).getByRole('link').first().click();
    await expect(page.getByTestId('allocation-totals')).toBeVisible();
    const difference = (await page.getByTestId('allocation-difference').textContent())!.trim();
    if (!difference.startsWith('0,00')) {
      found = true;
    } else {
      await page.getByRole('link', { name: 'Faturalar' }).first().click();
      await page.getByLabel('Durum').selectOption('DRAFT');
      await expect(table.getByTestId('invoice-row').first()).toBeVisible();
    }
  }
  expect(found).toBe(true);
  await page.getByTestId('invoice-submit').click();
  await expect(page.getByRole('alert').first()).toBeVisible();
  await expect(page.getByTestId('invoice-status')).toHaveText('Taslak');

  await page
    .getByRole('navigation', { name: 'Sağlayıcı portalı' })
    .getByRole('link', { name: 'Hakediş ve fatura' })
    .click();
  await expect(page.getByRole('heading', { name: 'Hakediş' })).toBeVisible();
  await page.getByRole('link', { name: 'İcmaller' }).first().click();
  await expect(page.getByRole('heading', { name: 'İcmaller' })).toBeVisible();
  await page.getByLabel('Durum').selectOption('DRAFT');
  await page.getByTestId('batch-row').first().getByRole('link').first().click();
  await expect(page.getByTestId('batch-status')).toHaveText('Taslak');
  await expect(page.getByTestId('membership-list').getByRole('checkbox').first()).toBeChecked();
  await page.getByTestId('batch-submit').click();
  await expect(page.getByTestId('batch-status')).toHaveText('Gönderildi');
  await expect(page.getByTestId('decision-row').first()).toContainText('Karar bekliyor');
});
