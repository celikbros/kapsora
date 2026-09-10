import { mkdirSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';

/** Captures the member's reimbursement screens for the finish review (REVIEW_CAPTURE=1 only). */
const PASSWORD = 'demo parola 2026 kapsora';
const OUT = '.impeccable/review';

test.skip(!process.env['REVIEW_CAPTURE'], 'set REVIEW_CAPTURE=1 to capture review screenshots');

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

async function settled(page: Page) {
  await page.waitForLoadState('networkidle');
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0, { timeout: 15_000 });
  await expect(page.getByText('Yükleniyor…')).toHaveCount(0, { timeout: 15_000 });
  await expect(page.locator('span, dd', { hasText: /^…$/ })).toHaveCount(0, { timeout: 20_000 });
}

async function capture(page: Page, name: string) {
  mkdirSync(OUT, { recursive: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await settled(page);
  const width = await page.evaluate(() => document.documentElement.scrollWidth);
  if (width > 390) {
    const offenders = await page.evaluate(() =>
      Array.from(document.querySelectorAll('body *'))
        .filter((el) => el.getBoundingClientRect().right > 392)
        .slice(0, 8)
        .map(
          (el) =>
            `${el.tagName.toLowerCase()}.${String(el.className).split(' ').slice(0, 4).join('.')}@${Math.round(el.getBoundingClientRect().right)}`,
        ),
    );
    throw new Error(
      `${name}: the page overflows the 390 viewport (${width}px): ${offenders.join(' | ')}`,
    );
  }
  await page.screenshot({ path: `${OUT}/${name}-mobile.png`, fullPage: true });
  await page.setViewportSize({ width: 1440, height: 900 });
  await settled(page);
  await page.screenshot({ path: `${OUT}/${name}-desktop.png`, fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
}

test('member: the reimbursement list, one decided, the new form up to the scan', async ({
  page,
}) => {
  await page.goto('/');
  await login(page, 'member.a');
  await expect(page.getByTestId('remaining-list')).toBeVisible();
  await page.getByTestId('home-reimbursements').click();
  await expect(page.getByTestId('reimbursement-list')).toBeVisible();
  await capture(page, 'member-reimbursements');

  await page.getByTestId('reimbursement-row').filter({ hasText: 'Ödendi' }).first().click();
  await expect(page.getByTestId('reimbursement-status')).toBeVisible();
  await capture(page, 'member-reimbursement');

  await page
    .getByRole('link', { name: /Geri ödemelerim/ })
    .first()
    .click();
  await page.getByTestId('new-reimbursement').click();
  await page.getByLabel(/^Hizmet\*/).selectOption({ index: 1 });
  await page.getByLabel(/^Hizmet tarihi/).fill(new Date().toISOString().slice(0, 10));
  await page.getByLabel(/^Sağlayıcı/).selectOption({ index: 1 });
  await page.getByLabel(/^Ödediğiniz tutar/).fill('125.50');
  await capture(page, 'member-reimbursement-request');
  await page.getByTestId('create-request').click();
  await page.getByTestId('receipt-file').setInputFiles({
    name: 'fis.pdf',
    mimeType: 'application/pdf',
    buffer: Buffer.from('%PDF-1.4 fis'),
  });
  await expect(page.getByTestId('receipt-scan')).toContainText('Makbuz taranıyor');
  await capture(page, 'member-reimbursement-scan');
});
