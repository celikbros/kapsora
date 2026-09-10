import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'demo parola 2026 kapsora';

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

/**
 * The member on a phone asks for money back: reads the list, opens the request, uploads
 * the receipt and is told it is being scanned. The browser mock never finishes a scan on
 * its own, so the account step is the unit test's to prove; here the screen proves it does
 * not offer the account before the receipt is clean, and that no account number is on
 * any page.
 */
test('a member opens a reimbursement and waits for the receipt to scan', async ({ page }) => {
  await page.goto('/');
  await login(page, 'member.a');
  await expect(page.getByTestId('remaining-list')).toBeVisible();
  await page.getByTestId('home-reimbursements').click();
  await expect(page.getByRole('heading', { name: 'Geri ödemelerim' })).toBeVisible();
  const list = page.getByTestId('reimbursement-list');
  await expect(list.getByTestId('reimbursement-row').first()).toBeVisible();
  await expect(list).not.toContainText(/TR\d{2}/);

  await page.getByTestId('new-reimbursement').click();
  await expect(page.getByRole('heading', { level: 1, name: 'Geri ödeme iste' })).toBeVisible();
  await page.getByLabel(/^Hizmet\*/).selectOption({ index: 1 });
  await page.getByLabel(/^Hizmet tarihi/).fill(new Date().toISOString().slice(0, 10));
  await page.getByLabel(/^Sağlayıcı/).selectOption({ index: 1 });
  await page.getByLabel(/^Ödediğiniz tutar/).fill('125.50');
  await expect(page.getByTestId('receipt')).toContainText('125,50 TRY');
  await page.getByTestId('create-request').click();

  await page.getByTestId('receipt-file').setInputFiles({
    name: 'fis.pdf',
    mimeType: 'application/pdf',
    buffer: Buffer.from('%PDF-1.4 fis'),
  });
  await expect(page.getByTestId('receipt-scan')).toContainText('Makbuz taranıyor');
  await expect(page.getByTestId('account-form')).toHaveCount(0);
  await expect(page.getByTestId('steps').locator('[aria-current="step"]')).toContainText('Makbuz');
});
