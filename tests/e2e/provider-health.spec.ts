import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'demo parola 2026 kapsora';

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

/**
 * The health vertical from the provider's desk: a case is opened for a member by name, an
 * encounter is recorded with a diagnosis chosen from ICD-10, a report is drafted and its
 * submission refused honestly while its file is not clean, and a claim is drafted from the
 * case and submitted. In-app navigation throughout: a full page load starts a new mock
 * session.
 */
test('a provider opens a case, records a diagnosis, drafts a report and submits a claim', async ({
  page,
}) => {
  await page.goto('/');
  await login(page, 'provider.a');
  await expect(page.getByRole('heading', { name: 'Yeni talep' })).toBeVisible();
  const rail = page.getByRole('navigation', { name: 'Sağlayıcı portalı' });
  await rail.getByRole('link', { name: 'Vakalar' }).click();
  await expect(page.getByRole('heading', { name: 'Vakalar' })).toBeVisible();
  await page.getByRole('link', { name: 'Vaka aç' }).click();

  // The member by name, the enrollment from the ones the person actually has.
  await page.getByLabel('Ada göre ara').fill('Sevgi');
  await page.getByTestId('member-candidates').getByRole('button').first().click();
  await page.getByLabel(/^İlgili hizmet/).selectOption({ index: 1 });
  await page.getByRole('button', { name: 'Vakayı aç' }).click();
  await expect(page).toHaveURL(/\/cases\/[0-9a-f-]+$/);
  await expect(page.getByTestId('case-status')).toHaveText('Açık');

  // An encounter, then its diagnosis: the code is found by name and shown by code.
  await page.getByRole('button', { name: 'Muayene ekle' }).click();
  await page.getByRole('button', { name: 'Muayeneyi kaydet' }).click();
  await expect(page.getByTestId('encounter-row')).toHaveCount(1);
  await page.getByRole('button', { name: 'Tanıları düzenle' }).click();
  await page.getByLabel(/ICD-10 ara/).fill('solunum');
  await page.getByTestId('icd10-matches').getByRole('button').first().click();
  await page.getByRole('button', { name: 'Tanıları kaydet' }).click();
  await expect(page.getByTestId('encounter-table')).toContainText('Üst solunum yolu enfeksiyonu');

  // A report draft: the header saves, a service line saves, and a submission without a
  // clean file is refused with the reason, not swallowed.
  await page.getByRole('button', { name: 'Rapor oluştur' }).click();
  await page
    .getByTestId('report-create-form')
    .getByLabel(/^Rapor türü/)
    .fill('TEDAVI');
  await page.getByRole('button', { name: 'Rapor taslağını aç' }).click();
  await expect(page).toHaveURL(/\/reports\/[0-9a-f-]+$/);
  await expect(page.getByTestId('report-status')).toHaveText('Taslak');
  await page.getByLabel(/^Rapor türü/).fill('TEDAVI');
  await page.getByRole('button', { name: 'Kaydet' }).click();
  await expect(page.getByText('Rapor kaydedildi.', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Hizmet ekle' }).click();
  await page
    .getByRole('combobox', { name: /^Hizmet/ })
    .first()
    .selectOption({ index: 1 });
  await page.getByRole('button', { name: 'Hizmetleri kaydet' }).click();
  await expect(page.getByText('Hizmetler kaydedildi.', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Gönder' }).click();
  await expect(page.getByTestId('report-status')).toHaveText('Taslak');

  // Back on the case, the billing side: a claim drafted from the case and submitted.
  await page.getByRole('link', { name: 'Vaka', exact: true }).first().click();
  await expect(page.getByTestId('case-reports')).toContainText('MR-');
  await page.getByRole('link', { name: 'Yeni claim' }).click();
  await expect(page).toHaveURL(/\/claims\/new/);
  await page
    .getByRole('combobox', { name: /^Hizmet/ })
    .first()
    .selectOption({ index: 1 });
  await page
    .getByLabel(/^İstenen tutar/)
    .first()
    .fill('450');
  await page.getByRole('button', { name: 'Taslağı oluştur' }).click();
  await expect(page).toHaveURL(/\/claims\/[0-9a-f-]+$/);
  await expect(page.getByTestId('claim-status')).toHaveText('Taslak');
  await page.getByRole('button', { name: 'Gönder' }).click();
  await expect(page.getByTestId('claim-status')).not.toHaveText('Taslak');
  await expect(page.getByTestId('claim-lines')).toBeVisible();
});
