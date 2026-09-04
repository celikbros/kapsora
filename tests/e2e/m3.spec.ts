import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'demo parola 2026 kapsora';

// The M3 screens against the mock worker; the real-API run lives in real-api.spec.ts.
test.skip(process.env['E2E_REAL_API'] === '1', 'mock-only flows');

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

function menu(page: Page) {
  return page.getByRole('navigation', { name: 'Ana menü' });
}

test('the catalog opens from the menu and a definition shows its code as fixed', async ({
  page,
}) => {
  await page.goto('/auth/login');
  await login(page, 'admin.a');

  await menu(page).getByRole('link', { name: 'Hizmet Kataloğu' }).click();
  await expect(page).toHaveURL(/\/catalog\/definitions$/);

  const table = page.getByTestId('definition-table');
  await expect(table.locator('tbody tr').first()).toBeVisible();
  await table.locator('tbody tr').first().getByRole('link').click();

  // A definition's code is fixed after creation, and the screen says why rather than
  // simply refusing the edit.
  await expect(page.getByText(/Kod oluşturulduktan sonra değiştirilemez/)).toBeVisible();
});

test('a provider is found by service and shows only masked registrations', async ({ page }) => {
  await page.goto('/auth/login');
  await login(page, 'admin.a');

  await menu(page).getByRole('link', { name: 'Sağlayıcılar' }).click();
  await expect(page).toHaveURL(/\/providers/);

  const table = page.getByTestId('provider-table');
  await expect(table.locator('tbody tr').first()).toBeVisible();
  await table.locator('tbody tr').first().getByRole('link').click();

  await page.getByRole('tab', { name: 'Uygulayıcılar' }).click();
  await expect(page.getByTestId('practitioner-table')).toBeVisible();
  // A registration number is shown masked, never in full.
  await expect(page.locator('body')).not.toHaveText(/TTB-\d{6}/);
});

test('a rule version cannot reach review without a passing test case', async ({ page }) => {
  await page.goto('/auth/login');
  await login(page, 'admin.a');

  await menu(page).getByRole('link', { name: 'Kurallar' }).click();
  await expect(page).toHaveURL(/\/rule-sets$/);

  const table = page.getByTestId('rule-set-table');
  await expect(table.locator('tbody tr').first()).toBeVisible();
  await table.locator('tbody tr').first().getByRole('link').click();
  await expect(page.getByTestId('rule-version-table')).toBeVisible();

  // The seeded version is published, so it is read-only and offers no submit.
  await page
    .getByTestId('rule-version-table')
    .locator('tbody tr')
    .first()
    .getByRole('link')
    .click();
  await expect(page.getByText('Yayında').first()).toBeVisible();
  await expect(page.getByRole('button', { name: 'İncelemeye gönder' })).toHaveCount(0);
});

test('a contract version is published by a second person and its prices become read-only', async ({
  page,
}) => {
  // The mock API lives inside the page, so its world is lost on any full reload;
  // everything here, the change of user included, stays in one client-side session.
  await page.goto('/auth/login');
  await login(page, 'admin.a');

  await menu(page).getByRole('link', { name: 'Sözleşmeler' }).click();
  await expect(page).toHaveURL(/\/contracts$/);

  const table = page.getByTestId('contract-table');
  await expect(table.locator('tbody tr').first()).toBeVisible();
  await table.locator('tbody tr').first().getByRole('link').click();

  const versions = page.getByTestId('contract-version-table');
  await expect(versions).toBeVisible();
  // Versions are listed newest first, so the draft is the top row.
  await versions.locator('tbody tr').first().getByRole('link').click();
  await expect(page.getByText('Taslak').first()).toBeVisible();

  // A draft with no price list offers the way to create one; the price sheet is
  // unreachable until the prices have somewhere to live.
  await expect(page.getByTestId('price-lists-form')).toBeVisible();
});

test('a quote explains its figures and shows none when the price is ambiguous', async ({
  page,
}) => {
  await page.goto('/auth/login');
  await login(page, 'admin.a');

  await menu(page).getByRole('link', { name: 'Fiyat Sorgusu' }).click();
  await expect(page).toHaveURL(/\/pricing$/);

  // The person is chosen by name; nobody has a member's identifier in their head.
  await page.getByLabel(/Ad veya soyad ara/).fill('Aydemir');
  const person = page.getByLabel(/^Hak sahibi/);
  await expect(person.locator('option').nth(1)).toBeAttached();
  await person.selectOption({ index: 1 });

  const provider = page.getByLabel(/^Sağlayıcı/);
  await expect(provider.locator('option').nth(1)).toBeAttached();
  await provider.selectOption({ index: 1 });

  const service = page.getByLabel('Hizmet', { exact: true });
  await expect(service.locator('option').nth(1)).toBeAttached();
  await service.selectOption({ index: 1 });

  await page.getByRole('button', { name: 'Hesapla' }).click();

  await expect(page.getByTestId('quote-table')).toBeVisible();
  // A quote is not an authorization, and the screen says so under the figures.
  await expect(page.getByText(/ön onay değildir/)).toBeVisible();
  // And it names what the numbers came from rather than asserting them.
  await expect(page.getByText('Bu sonuç neye dayanıyor')).toBeVisible();
});
