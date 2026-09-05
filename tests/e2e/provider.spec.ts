import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'demo parola 2026 kapsora';

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

/**
 * The provider portal, as a desk would use it: check while typing, submit, then attach
 * the document a request is waiting for. The scan verdict belongs to the worker, so the
 * flow asserts the honest in-between state rather than a download that cannot exist yet.
 */
test('a provider checks eligibility live, submits, and uploads the document a request waits for', async ({
  page,
}) => {
  await page.goto('/');
  await login(page, 'provider.a');
  await expect(page.getByRole('heading', { name: 'Yeni talep' })).toBeVisible();
  const pane = page.getByTestId('eligibility-pane');
  await expect(pane).toContainText('cevap burada görünür');

  // The member by name. The world seeds one member with exactly one active enrollment —
  // the demo family's spouse — which is what lets the check resolve the enrollment; anybody
  // with two answers ENROLLMENT_MULTIPLE and the desk cannot choose.
  await page.getByLabel('Ada göre ara').fill('Sevgi');
  await page.getByTestId('member-candidates').getByRole('button').first().click();
  await expect(page.getByText(/^Bulunan:/)).toBeVisible();
  const service = page.getByRole('combobox', { name: /^Hizmet/ });
  await service.selectOption({ index: 1 });
  // The heading itself says 'Uygunluk'; wait for the answer, not the word.
  await expect(pane).not.toContainText('Hesaplanıyor');
  await expect(pane).not.toContainText('cevap burada görünür');

  await page.getByRole('button', { name: 'Gönder' }).click();
  await expect(page).toHaveURL(/\/requests\/[0-9a-f-]+$/);
  await expect(page.getByText(/^SR-/).first()).toBeVisible();

  // The request that waits on us, and the upload from it.
  // In-app navigation: a full page load starts a new mock session.
  await page
    .getByRole('navigation', { name: 'Sağlayıcı portalı' })
    .getByRole('link', { name: 'Taleplerim' })
    .click();
  const table = page.getByTestId('my-requests-table');
  await expect(table).toBeVisible();
  await table.locator('tr[data-status="PENDING_DOCUMENT"]').first().getByRole('link').click();
  const form = page.getByTestId('document-upload-form');
  await expect(form).toBeVisible();
  await form.getByLabel(/Dosya seç/).setInputFiles({
    name: 'fatura.pdf',
    mimeType: 'application/pdf',
    buffer: Buffer.from('%PDF-1.4 fatura'),
  });
  const typeField = form.getByLabel(/Belge türü/);
  if ((await typeField.evaluate((el) => el.tagName)) === 'SELECT') {
    await typeField.selectOption({ index: 1 });
  } else {
    await typeField.fill('INVOICE');
  }
  await form.getByRole('button', { name: 'Belge yükle' }).click();
  const row = page.getByTestId('documents-table').locator('tr', { hasText: 'fatura.pdf' });
  await expect(row).toContainText(/sıraya alındı|Taranıyor/);
  await expect(row.getByRole('button', { name: 'İndir' })).toHaveCount(0);
});
