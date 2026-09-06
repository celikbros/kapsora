import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'demo parola 2026 kapsora';

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

/** The clinical words the mock world seeds for the demo member; none may reach HR's screen. */
const CLINICAL = [
  'Üst solunum yolu enfeksiyonu',
  'depresif',
  'menisküs',
  'artroskopi',
  'Kontrol muayenesi',
  'Kontrol MR',
];

test('a medical reviewer decides a claim line by line on clinical grounds', async ({ page }) => {
  await page.goto('/claims');
  await login(page, 'doctor.a');
  await page.getByLabel('Durum').selectOption('PENDING_MEDICAL');
  const table = page.getByTestId('claim-table');
  await expect(table.locator('tbody tr').first()).toContainText('Tıbbi incelemede');
  // The newest pending claim is on the sensitive case and asks for a purpose first; the
  // standard one behind it is the plain flow this test is about.
  await table.getByRole('link').last().click();
  await expect(page.getByTestId('claim-lines')).toHaveAttribute('data-projection', 'CLINICAL');
  await expect(page.getByRole('columnheader', { name: 'Açıklama' })).toBeVisible();
  await expect(page.getByRole('columnheader', { name: 'Ödeyen' })).toHaveCount(0);
  for (const field of await page.getByLabel(/^Gerekçe kodu/).all()) {
    await field.fill('CLINICALLY_INDICATED');
  }
  await page.getByTestId('save-decisions').click();
  await expect(page.getByText('Satır kararları kaydedildi.', { exact: true })).toBeVisible();
  await expect(page.getByTestId('claim-status')).toHaveText('Mali incelemede');
});

test('sponsor HR opens the same member and finds no diagnosis anywhere on the page', async ({
  page,
}) => {
  await page.goto('/people');
  await login(page, 'sponsor.hr');
  await page.getByRole('link', { name: 'Kaan Aydemir' }).first().click();
  await expect(page).toHaveURL(/\/people\/[0-9a-f-]+$/);
  await page.getByRole('tab', { name: 'Sağlık' }).click();
  await expect(page.getByTestId('person-claims')).toBeVisible();
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
  const body = await page.locator('body').innerText();
  for (const word of CLINICAL) expect(body, `HR screen carries "${word}"`).not.toContain(word);
  await expect(page.getByRole('tab', { name: 'Erişim kaydı' })).toHaveCount(0);

  // The claim HR reaches from here is the financial projection: no description column.
  await page.getByTestId('person-claims').getByRole('link').first().click();
  await expect(page.getByTestId('claim-lines')).toHaveAttribute('data-projection', 'FINANCIAL');
  await expect(page.getByRole('columnheader', { name: 'Açıklama' })).toHaveCount(0);
  const claimBody = await page.locator('body').innerText();
  for (const word of CLINICAL)
    expect(claimBody, `HR claim page carries "${word}"`).not.toContain(word);
});
