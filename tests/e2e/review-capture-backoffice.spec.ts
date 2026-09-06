import { mkdirSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';

/** Backoffice captures for the finish review; see review-capture.spec.ts. */
const PASSWORD = 'demo parola 2026 kapsora';
const OUT = '.impeccable/review';

test.skip(!process.env['REVIEW_CAPTURE'], 'set REVIEW_CAPTURE=1 to capture review screenshots');

async function login(page: Page, username: string) {
  await page.getByLabel(/Kullanıcı adı/).fill(username);
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap' }).click();
}

async function settled(page: Page) {
  // Per-row lookups start after the first paint, so network idle comes too early; wait
  // until nothing on the page says it is still loading.
  await page.waitForLoadState('networkidle');
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0, { timeout: 15_000 });
  await expect(page.getByText('Yükleniyor…')).toHaveCount(0, { timeout: 15_000 });
  // Per-row name lookups render '…' until they land.
  await expect(page.locator('td', { hasText: /^…$/ })).toHaveCount(0, { timeout: 20_000 });
}

async function capture(page: Page, name: string) {
  mkdirSync(OUT, { recursive: true });
  await page.setViewportSize({ width: 1440, height: 900 });
  await settled(page);
  await page.screenshot({ path: `${OUT}/${name}-desktop.png`, fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await settled(page);
  // A page that is wider than its viewport is a defect the reviewer must see at 390, not
  // a wider capture that hides it.
  const width = await page.evaluate(() => document.documentElement.scrollWidth);
  if (width > 390) {
    // Name the culprits: the elements whose right edge passes the viewport.
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
}

test('backoffice: request list, a request under review, the worklist, the message log', async ({
  page,
}) => {
  await page.goto('/requests');
  await login(page, 'admin.a');
  await expect(page.getByTestId('request-table')).toBeVisible();
  await capture(page, 'requests');

  await page.getByLabel('Durum').selectOption('PENDING_REVIEW');
  await expect(page.getByTestId('request-table').locator('tbody tr').first()).toContainText(
    'İncelemede',
  );
  await page.getByTestId('request-table').getByRole('link').first().click();
  await expect(page.getByTestId('request-commands')).toBeVisible();
  await capture(page, 'request-detail');

  await page.getByRole('link', { name: 'Talepler ve Onaylar' }).click();
  await page.getByLabel('Durum').selectOption('REJECTED');
  await expect(page.getByTestId('request-table').locator('tbody tr').first()).toContainText(
    'Reddedildi',
  );
  await page.getByTestId('request-table').getByRole('link').first().click();
  await expect(page.getByTestId('rejected-panel')).toBeVisible();
  await capture(page, 'request-rejected');

  await page.getByRole('link', { name: 'İş Listem' }).click();
  await page.getByRole('button', { name: 'Tümü' }).click();
  await expect(page.getByTestId('worklist-table')).toBeVisible();
  await capture(page, 'worklist');

  await page.getByRole('link', { name: 'Bildirimler' }).click();
  await page.getByRole('tab', { name: 'Gönderi kaydı' }).click();
  await expect(page.getByTestId('message-table')).toBeVisible();
  await capture(page, 'notifications');
});

test('backoffice: the medical reviewer’s claim and report, the financial reviewer’s claim', async ({
  page,
}) => {
  await page.goto('/claims');
  await login(page, 'doctor.a');
  await expect(page.getByTestId('claim-table')).toBeVisible();
  await capture(page, 'claims');
  await page.getByLabel('Durum').selectOption('PENDING_MEDICAL');
  await expect(page.getByTestId('claim-table').locator('tbody tr').first()).toContainText(
    'Tıbbi incelemede',
  );
  // The newest pending claim is the sensitive one; the standard one is behind it.
  await page.getByTestId('claim-table').getByRole('link').last().click();
  await expect(page.getByTestId('claim-lines')).toHaveAttribute('data-projection', 'CLINICAL');
  await capture(page, 'claim-medical');

  await page.getByRole('link', { name: 'Tıbbi inceleme' }).click();
  await page.getByLabel('Durum').selectOption('APPROVED');
  await page.getByTestId('report-table').getByRole('link').first().click();
  await expect(page.getByTestId('report-summary')).toBeVisible();
  await capture(page, 'report-review');
});

test('backoffice: the purpose prompt on a sensitive case, and the declined half', async ({
  page,
}) => {
  await page.goto('/claims');
  await login(page, 'doctor.a');
  await page.getByLabel('Durum').selectOption('PENDING_MEDICAL');
  // The sensitive claim is the newest of the two pending medical ones.
  await page.getByTestId('claim-table').getByRole('link').first().click();
  await expect(page.getByTestId('purpose-dialog')).toBeVisible();
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.screenshot({ path: `${OUT}/purpose-dialog-desktop.png`, fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: `${OUT}/purpose-dialog-mobile.png`, fullPage: true });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByTestId('purpose-decline').click();
  await expect(page.getByTestId('declined-note')).toBeVisible();
  await capture(page, 'claim-declined');
});

test('backoffice: the financial reviewer’s claim', async ({ page }) => {
  await page.goto('/claims');
  await login(page, 'financial.reviewer');
  await page.getByLabel('Durum').selectOption('PENDING_FINANCIAL');
  await expect(page.getByTestId('claim-table').locator('tbody tr').first()).toContainText(
    'Mali incelemede',
  );
  await page.getByTestId('claim-table').getByRole('link').first().click();
  await expect(page.getByTestId('claim-lines')).toHaveAttribute('data-projection', 'FINANCIAL');
  await capture(page, 'claim-financial');
});

test('backoffice: what sponsor HR sees, and the access log', async ({ page }) => {
  await page.goto('/people');
  await login(page, 'sponsor.hr');
  await page.getByRole('link', { name: 'Kaan Aydemir' }).first().click();
  await page.getByRole('tab', { name: 'Sağlık' }).click();
  await expect(page.getByTestId('person-claims')).toBeVisible();
  await capture(page, 'person-health-hr');
});

test('backoffice: the access log', async ({ page }) => {
  await page.goto('/people');
  await login(page, 'admin.a');
  await page.getByRole('link', { name: 'Kaan Aydemir' }).first().click();
  await page.getByRole('tab', { name: 'Erişim kaydı' }).click();
  await expect(page.getByTestId('access-log-tab')).toBeVisible();
  await capture(page, 'person-access-log');
});
