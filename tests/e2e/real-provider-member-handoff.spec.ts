import { expect, test } from '@playwright/test';

const existing = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !existing,
  'requires the operator-started single-door server',
);

// The filename deliberately matches both dedicated projects. Each must reuse the
// same operator origin, authenticate through the shared door and reach its own app.
test('real shared sign-in routes the account to its app and logout clears both tabs', async ({
  page,
  context,
  baseURL,
}, info) => {
  test.setTimeout(60_000);
  const member = info.project.name === 'member';
  const path = member ? '/uye/' : '/portal/';
  const foreignPath = member ? '/portal/' : '/uye/';
  expect(new URL(baseURL!).origin).toBe(new URL(existing).origin);
  await page.goto('/auth/login');
  await page.getByLabel(/^Kullanıcı adı/).fill(member ? 'member.a' : 'provider.a');
  await page
    .getByLabel(/^Parola/)
    .fill(process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora');
  await page.getByRole('button', { name: 'Giriş yap', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(path));
  if (member) await expect(page.getByTestId('remaining-list')).toBeVisible();
  else await expect(page.getByRole('link', { name: 'Vakalar', exact: true })).toBeVisible();
  expect(new URL(page.url()).origin).toBe(new URL(existing).origin);
  const other = await context.newPage();
  await other.goto(foreignPath);
  await expect(other).toHaveURL(new RegExp(path));
  expect(new URL(other.url()).origin).toBe(new URL(existing).origin);
  await page.getByRole('button', { name: 'Çıkış yap', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Giriş yap', exact: true })).toBeVisible();
  await other.goto(path);
  await expect(other.getByRole('button', { name: 'Giriş yap', exact: true })).toBeVisible();
  await expect(other.getByTestId('remaining-list')).toHaveCount(0);
  await expect(other.getByRole('button', { name: 'Çıkış yap', exact: true })).toHaveCount(0);
  await other.close();
});
