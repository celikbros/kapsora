import { expect, test, type Page } from '@playwright/test';

const PASSWORD = 'demo parola 2026 kapsora';

// These flows assert mock data; the real-API run is in real-api.spec.ts.
test.skip(process.env['E2E_REAL_API'] === '1', 'mock-only flows');

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

test('anonymous visit redirects to login and back after signing in', async ({ page }) => {
  await page.goto('/organizations?role=PROVIDER');
  await expect(page).toHaveURL(/\/auth\/login\?returnTo=/);
  await login(page, 'admin.a');
  await expect(page).toHaveURL(/\/organizations\?role=PROVIDER$/);
  await expect(page.getByRole('heading', { name: 'Kurumlar' })).toBeVisible();
  // Tenant badge shows the active tenant.
  await expect(page.getByText('Demo Banka A.Ş. · DEMO_A')).toBeVisible();
});

test('multi-tenant user sees the picker, then the list renders 50 rows and paginates', async ({
  page,
}) => {
  await page.goto('/auth/login');
  await login(page, 'both.ab');
  await expect(page).toHaveURL(/\/auth\/tenant/);
  await expect(page.getByRole('heading', { name: 'Çalışma alanı seçin' })).toBeVisible();
  const rows = page.getByRole('listitem').filter({ hasText: 'DEMO_B' });
  await rows.getByRole('button', { name: 'Seç' }).click();
  await expect(page.getByText('Demo Sigorta A.Ş. · DEMO_B')).toBeVisible();

  await page
    .getByRole('navigation', { name: 'Ana menü' })
    .getByRole('link', { name: 'Kurumlar' })
    .click();
  await expect(page).toHaveURL(/\/organizations$/);
  const table = page.getByTestId('organization-table');
  await expect(table.locator('tbody tr')).toHaveCount(50);
  await expect(page.getByText('Sayfa 1')).toBeVisible();

  await page.getByRole('button', { name: 'Sonraki sayfa' }).click();
  await expect(page).toHaveURL(/cursor=/);
  await expect(page.getByText('Sayfa 2')).toBeVisible();
  await expect(table.locator('tbody tr')).toHaveCount(50);
  await page.getByRole('button', { name: 'Önceki sayfa' }).click();
  await expect(page.getByText('Sayfa 1')).toBeVisible();
});

test('nothing personal lands in browser storage', async ({ page }) => {
  await page.goto('/auth/login');
  await login(page, 'admin.a');
  await expect(page.getByRole('heading', { name: 'Ana Sayfa' })).toBeVisible();
  await page
    .getByRole('navigation', { name: 'Ana menü' })
    .getByRole('link', { name: 'Kurumlar' })
    .click();
  await expect(page.getByTestId('organization-table')).toBeVisible();
  const storage = await page.evaluate(() => ({
    local: Object.keys(localStorage),
    session: Object.keys(sessionStorage),
  }));
  expect(storage.local).toEqual([]);
  expect(storage.session).toEqual([]);
});
