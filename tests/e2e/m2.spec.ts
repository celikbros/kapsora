import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'demo parola 2026 kapsora';

// The M2 screens against the mock worker; the real-API run lives in real-api.spec.ts.
test.skip(process.env['E2E_REAL_API'] === '1', 'mock-only flows');

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

/** The seeded family principal in DEMO_A: a person with relations, memberships and rights. */
const PRINCIPAL = 'Kaan Aydemir';

/** Opens the seeded principal from the member list. */
async function openPrincipal(page: Page) {
  await page
    .getByRole('navigation', { name: 'Ana menü' })
    .getByRole('link', { name: 'Hak Sahipleri' })
    .click();
  await expect(page).toHaveURL(/\/people$/);
  await page.getByLabel(/Ad veya soyad ara/).fill('Aydemir');
  await page.getByRole('button', { name: 'Ara', exact: true }).click();
  await page.getByRole('link', { name: PRINCIPAL }).click();
  await expect(page).toHaveURL(/\/people\/[0-9a-f-]{36}$/);
}

/** Walks the menu to the version table of the first plan of the first program. */
async function openVersionList(page: Page) {
  await page
    .getByRole('navigation', { name: 'Ana menü' })
    .getByRole('link', { name: 'Programlar ve Planlar' })
    .click();
  await expect(page).toHaveURL(/\/programs$/);
  await page.getByTestId('program-table').locator('tbody tr').first().getByRole('link').click();
  await page.getByTestId('plan-table').locator('tbody tr').first().getByRole('link').click();
  await expect(page.getByTestId('version-table')).toBeVisible();
}

/** Opens one version row; versions are listed newest first, so row 0 is the latest. */
async function openVersion(page: Page, row: number) {
  await openVersionList(page);
  await page.getByTestId('version-table').locator('tbody tr').nth(row).getByRole('link').click();
  await expect(page.getByRole('heading', { name: 'Plan sürümü' })).toBeVisible();
}

/**
 * Fills the password dialog when it appears. A step-up stays valid for a while, so a
 * second sensitive action in the same minute is not asked again.
 */
async function confirmStepUpIfAsked(page: Page) {
  const dialog = page.getByRole('dialog', { name: 'Parolanızı doğrulayın' });
  // The dialog only opens once the server has refused, so give the round trip time to
  // land rather than checking the instant after the click.
  const asked = await dialog
    .waitFor({ state: 'visible', timeout: 5_000 })
    .then(() => true)
    .catch(() => false);
  if (!asked) return;
  await dialog.getByLabel(/^Parola/).fill(PASSWORD);
  await dialog.getByRole('button', { name: 'Doğrula' }).click();
  await expect(dialog).toBeHidden();
}

/** both.ab belongs to two tenants, so it stops at the picker on the way in. */
async function loginToDemoA(page: Page, username: string) {
  await login(page, username);
  await page.waitForURL(/\/auth\/tenant|127\.0\.0\.1:\d+\/$/);
  if (page.url().includes('/auth/tenant')) {
    await page
      .getByRole('listitem')
      .filter({ hasText: 'DEMO_A' })
      .getByRole('button', { name: 'Seç' })
      .click();
  }
  await expect(page.getByText('Demo Banka A.Ş. · DEMO_A')).toBeVisible();
}

test('member list opens a person and walks the detail tabs', async ({ page }) => {
  await page.goto('/auth/login');
  await login(page, 'admin.a');
  await openPrincipal(page);

  await expect(page.getByRole('tab', { name: 'Kimlik' })).toBeVisible();
  await page.getByRole('tab', { name: 'Aile' }).click();
  await expect(page.getByTestId('relationship-table')).toBeVisible();
  await page.getByRole('tab', { name: 'Üyelikler' }).click();
  await expect(page.getByTestId('membership-table')).toBeVisible();
  await page.getByRole('tab', { name: 'Haklar' }).click();
  await expect(page.getByTestId('entitlement-table')).toBeVisible();

  // Identity numbers are only ever shown masked.
  await expect(page.locator('body')).not.toHaveText(/\b\d{11}\b/);
});

test('an eligibility check answers with its explanations', async ({ page }) => {
  await page.goto('/auth/login');
  await login(page, 'admin.a');
  await openPrincipal(page);
  await page.getByRole('tab', { name: 'Uygunluk' }).click();

  const codeSelect = page.getByLabel(/Hak kodu/);
  await expect(codeSelect.locator('option').nth(1)).toBeAttached();
  await codeSelect.selectOption({ index: 1 });
  await page.getByRole('button', { name: 'Sorgula' }).click();

  await expect(page.getByTestId('eligibility-items')).toBeVisible();
  await expect(page.getByText(/Değerlendirme no/)).toBeVisible();
});

test('a plan version is submitted by one person and published by another', async ({ page }) => {
  // The mock API lives inside the page, so its world is lost on any full reload. Everything
  // here, the change of user included, therefore stays inside one client-side session.
  await page.goto('/auth/login');
  await login(page, 'admin.a');
  await openVersion(page, 0);

  const periodForm = page.getByTestId('version-period-form');
  await periodForm.getByLabel(/^Başlangıç/).fill('2028-01-01');
  await periodForm.getByRole('button', { name: 'Kaydet' }).click();
  // Wait for the saved period before touching the definitions: they are saved under the
  // ETag of the version as it stands, and the page has to see the new one first.
  await expect(page.getByText('Plan güncellendi.').first()).toBeVisible();

  const definitions = page.getByTestId('definitions-form');
  await definitions.getByLabel(/^Kod/).fill('SMOKE_MONEY');
  await definitions.getByLabel(/^Ad/).fill('Duman Testi Bakiyesi');
  await definitions.getByLabel(/Başlangıç miktarı/).fill('750.250000');
  await definitions.getByRole('button', { name: 'Kaydet' }).click();
  await expect(page.getByText('Hak tanımları kaydedildi.').first()).toBeVisible();

  await page.getByRole('button', { name: 'İncelemeye gönder' }).click();
  await page
    .getByRole('dialog', { name: 'İncelemeye gönder' })
    .getByRole('button', { name: 'İncelemeye gönder' })
    .click();
  await expect(page.getByText('İncelemede', { exact: true })).toBeVisible();
  // The submitter is not offered the publish button.
  await expect(page.getByRole('button', { name: 'Yayınla' })).toHaveCount(0);

  // Hand the version over to someone else, without leaving the page.
  await page.getByRole('button', { name: 'Kullanıcı menüsü' }).click();
  await page.getByRole('menuitem', { name: 'Çıkış yap' }).click();
  await expect(page.getByRole('heading', { name: 'Oturum kapatıldı' })).toBeVisible();
  await page.getByRole('link', { name: 'Yeniden giriş yap' }).click();
  await loginToDemoA(page, 'both.ab');

  // The version already live runs open-ended, so it has to be taken out of use before the
  // new one can start; the server refuses two published versions over the same dates.
  await openVersion(page, 1);
  await page.getByRole('button', { name: 'Kullanımdan çıkar' }).click();
  await page
    .getByRole('dialog', { name: 'Kullanımdan çıkar' })
    .getByRole('button', { name: 'Kullanımdan çıkar' })
    .click();
  await confirmStepUpIfAsked(page);
  await expect(page.getByText('Kullanımdan çıktı', { exact: true })).toBeVisible();

  await openVersion(page, 0);
  await expect(page.getByText('İncelemede', { exact: true })).toBeVisible();

  await page.getByRole('button', { name: 'Yayınla' }).click();
  await page
    .getByRole('dialog', { name: 'Yayınla' })
    .getByRole('button', { name: 'Yayınla' })
    .click();
  await confirmStepUpIfAsked(page);

  await expect(page.getByText('Yayında', { exact: true })).toBeVisible();
  await expect(page.getByText(/Yapılandırma özeti/)).toBeVisible();
  // The published version is frozen: no editor, no submit.
  await expect(page.getByTestId('definitions-form')).toHaveCount(0);
  await expect(page.getByTestId('definition-table')).toBeVisible();
});
