import { randomUUID } from 'node:crypto';

import { expect, test } from '@playwright/test';

const REAL = process.env['E2E_REAL_API'] === '1';
const PASSWORD = process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora';
test.skip(!REAL, 'requires the real seeded API and running worker');

test('real member import: step-up, invalid row review, apply and member list', async ({ page }) => {
  test.setTimeout(90_000);
  const run = randomUUID().replaceAll('-', '');
  const surname = `Deneme${run.replace(/[^a-f]/g, '')}`;
  const columns =
    'source_record_id,first_name,middle_name,last_name,birth_date,sex_at_birth,tckn,member_no,employee_no,membership_type,principal_member_no,relationship,valid_from,valid_to,plan_code';
  // Source record IDs must be unique too: reusing one updates its existing member.
  const content =
    `${columns}\n` +
    `R1-${run},Sentetik,,${surname},1990-01-01,FEMALE,,${run}A,,PRINCIPAL,,,2026-01-01,,\n` +
    `R2-${run},Hatali,,${surname},1990-01-01,FEMALE,,${run}B,,EMPLOYEE,,,2026-01-01,,\n`;

  await page.goto('/imports/new');
  await page.getByLabel(/Kullanıcı adı/).fill('admin.a');
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap', exact: true }).click();
  await expect(page.getByText(/DEMO_A/)).toBeVisible();
  await page.goto('/imports/new');
  await expect(page.getByRole('heading', { name: 'Dosya yükle', exact: true })).toBeVisible();
  await page.getByLabel(/Dosya \(CSV\)/).setInputFiles({
    name: `synthetic-${run}.csv`,
    mimeType: 'text/csv',
    buffer: Buffer.from(content),
  });
  await page.getByLabel(/Sponsor kurum/).selectOption({ label: 'Demo Sponsor Holding A.Ş.' });
  await page.getByLabel('Kaynak sistem', { exact: false }).fill('BROWSER_IMPORT_TEST');
  await page.getByLabel('Kaynak sürümü', { exact: false }).fill(run);
  await page.getByRole('button', { name: 'Kaydet', exact: true }).click();
  const stepUp = page.getByRole('dialog', { name: 'Parolanızı doğrulayın' });
  await expect(stepUp).toBeVisible();
  await stepUp.getByLabel(/^Parola/).fill(PASSWORD);
  await stepUp.getByRole('button', { name: 'Doğrula', exact: true }).click();
  await expect(page).toHaveURL(/\/imports\/[0-9a-f-]{36}$/);
  await expect(page.getByText('İnceleme bekliyor', { exact: true })).toBeVisible();
  const row = page.getByRole('row').filter({ hasText: `Hatali ${surname}` });
  await expect(row).toBeVisible();
  await row.getByRole('button', { name: 'Atla', exact: true }).click();
  await page.getByRole('button', { name: 'Uygula', exact: true }).click();
  await page
    .getByRole('dialog', { name: 'İçe aktarmayı uygula' })
    .getByRole('button', { name: 'Uygula', exact: true })
    .click();
  const counters = page.getByTestId('import-counters');
  await expect(
    counters
      .locator('div')
      .filter({ has: page.locator('dt', { hasText: /^Oluşturulan$/ }) })
      .locator('dd'),
  ).toHaveText('1', { timeout: 30_000 });
  await expect(
    counters
      .locator('div')
      .filter({ has: page.locator('dt', { hasText: /^Atlanan$/ }) })
      .locator('dd'),
  ).toHaveText('1');
  await expect(page.getByText('Uygulandı', { exact: true }).first()).toBeVisible();

  await page
    .getByRole('navigation', { name: 'Ana menü' })
    .getByRole('link', { name: 'Hak Sahipleri', exact: true })
    .click();
  await page.getByRole('textbox', { name: /^Ad veya soyad ara/ }).fill(surname);
  await page.getByRole('button', { name: 'Ara', exact: true }).click();
  await expect(page.getByRole('link', { name: `Sentetik ${surname}`, exact: true })).toHaveCount(1);
  await expect(page.getByRole('link', { name: `Hatali ${surname}`, exact: true })).toHaveCount(0);
  await page.screenshot({ path: '.impeccable/review/import-member-real.png', fullPage: true });
  await page.getByRole('button', { name: 'Kullanıcı menüsü', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Çıkış yap', exact: true }).click();
  await expect(page).toHaveURL(/\/auth\/logout/);
  await expect(page.getByRole('heading', { name: 'Oturum kapatıldı', exact: true })).toBeVisible();
  await page.goto('/people');
  await expect(page).toHaveURL(/\/auth\/login/);
});
