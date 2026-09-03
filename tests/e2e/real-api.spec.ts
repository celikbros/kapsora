import { expect, test } from '@playwright/test';

/**
 * Runs only with E2E_REAL_API=1: the dev server proxies /api to the Go API (seeded with
 * `seed demo`) and the mock worker is off. Proves that switching VITE_API_MOCK=false
 * changes configuration only.
 */
const REAL = process.env['E2E_REAL_API'] === '1';
const PASSWORD = process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora';

test.skip(!REAL, 'set E2E_REAL_API=1 (and start the API) to run against the real backend');

test('login, tenant context, organization list and create against the Go API', async ({ page }) => {
  await page.goto('/organizations');
  await expect(page).toHaveURL(/\/auth\/login/);
  await page.getByLabel(/Kullanıcı adı/).fill('admin.a');
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
  await expect(page.getByRole('heading', { name: 'Kurumlar' })).toBeVisible();
  await expect(page.getByText(/DEMO_A/)).toBeVisible();

  await page.getByRole('link', { name: 'Yeni kurum' }).click();
  await page.getByLabel(/Ticari unvan/).fill('E2E Gerçek API Kliniği Ltd. Şti.');
  await page.getByLabel(/Görünen ad/).fill(`E2E Gerçek ${Date.now()}`);
  // A VKN with a valid check digit, generated in the browser from the shared helper.
  const vkn = await page.evaluate(() => {
    const digits: number[] = [];
    for (let i = 0; i < 9; i++) digits.push(Math.floor(Math.random() * 10));
    let sum = 0;
    for (let i = 1; i <= 9; i++) {
      const tmp = (digits[i - 1]! + 10 - i) % 10;
      sum += tmp === 9 ? 9 : (tmp * 2 ** (10 - i)) % 9;
    }
    digits.push((10 - (sum % 10)) % 10);
    return digits.join('');
  });
  await page.getByLabel(/^Değer/).fill(vkn);
  await page.getByRole('button', { name: 'Kaydet' }).click();
  await expect(page).toHaveURL(/\/organizations\/[0-9a-f-]+$/);
  await expect(page.getByText(`${vkn.slice(0, 2)}******${vkn.slice(8)}`)).toBeVisible();
  const storage = await page.evaluate(
    () => Object.keys(localStorage).length + Object.keys(sessionStorage).length,
  );
  expect(storage).toBe(0);
});
