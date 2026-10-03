import { expect, test } from '@playwright/test';

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const reimbursementId = process.env['E2E_EXISTING_REIMBURSEMENT_ID'] ?? '';
const password = process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora';

test.use({ trace: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || !reimbursementId,
  'requires the operator-started UI and an explicit existing reimbursement ID',
);

test('member opens the existing paid reimbursement from home and returns without writing', async ({
  browser,
}) => {
  test.setTimeout(90_000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  page.setDefaultTimeout(15_000);
  const forbiddenSupportingCalls: string[] = [];
  const nonSessionMutations: string[] = [];
  const reimbursementReads: { path: string; status: number }[] = [];

  page.on('request', (request) => {
    const url = new URL(request.url());
    if (!url.pathname.startsWith('/api/v1/')) return;
    if (
      request.method() !== 'GET' &&
      !/^\/api\/v1\/session\/(login|logout|switch-tenant)$/.test(url.pathname)
    ) {
      nonSessionMutations.push(`${request.method()} ${url.pathname}`);
    }
  });
  page.on('response', (response) => {
    const request = response.request();
    const path = new URL(response.url()).pathname;
    if (!path.startsWith('/api/v1/')) return;
    if (request.method() === 'GET' && [403, 404].includes(response.status())) {
      forbiddenSupportingCalls.push(`${path} ${response.status()}`);
    }
    if (
      request.method() === 'GET' &&
      (path === '/api/v1/me/reimbursements' || path === `/api/v1/reimbursements/${reimbursementId}`)
    ) {
      reimbursementReads.push({ path, status: response.status() });
    }
  });

  try {
    await page.goto(`${base}/uye/`);
    await page.getByLabel(/^Kullan\u0131c\u0131 ad\u0131/).fill('member.a');
    await page.getByLabel(/^Parola/).fill(password);
    await page.getByRole('button', { name: 'Giri\u015f yap', exact: true }).click();
    await expect(page).toHaveURL(/\/uye\/?$/);
    await expect(page.getByTestId('home-reimbursements')).toBeVisible();

    for (const width of [390, 1440]) {
      await page.setViewportSize({ width, height: 900 });
      await page.goto(`${base}/uye/`);
      await expect(page.getByTestId('remaining-list')).toBeVisible();
      await page.getByTestId('home-reimbursements').click();
      await expect(page.getByRole('heading', { name: 'Geri \u00f6demelerim' })).toBeVisible();
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
      ).toBe(true);

      const list = page.getByTestId('reimbursement-list');
      const selected = list.locator(
        `[data-testid="reimbursement-row"][href$="/${reimbursementId}"]`,
      );
      await expect(selected).toBeVisible();
      await expect(selected).toContainText('125,50 TRY');
      await expect(selected).toContainText('\u00d6dendi');
      await selected.click();

      await expect(page.getByTestId('reimbursement-status')).toHaveText('\u00d6dendi');
      await expect(page.getByTestId('receipt-member-amount')).toHaveText('125,50 TRY');
      await expect(page.getByTestId('reimbursement-sequence')).toBeVisible();
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
      ).toBe(true);

      await page.locator('a[href$="/reimbursements"]').click();
      await expect(page.getByTestId('reimbursement-list')).toBeVisible();
      await page.getByRole('link', { name: '\u2190 Ana sayfa', exact: true }).click();
      await expect(page.getByTestId('home-reimbursements')).toBeVisible();
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
      ).toBe(true);
    }

    expect(forbiddenSupportingCalls).toEqual([]);
    expect(nonSessionMutations).toEqual([]);
    expect(reimbursementReads.some((read) => read.path === '/api/v1/me/reimbursements')).toBe(true);
    expect(
      reimbursementReads.some(
        (read) => read.path === `/api/v1/reimbursements/${reimbursementId}` && read.status === 200,
      ),
    ).toBe(true);
  } finally {
    const logout = page.getByRole('button', { name: '\u00c7\u0131k\u0131\u015f yap', exact: true });
    if (await logout.isVisible().catch(() => false)) {
      await logout.click();
      await expect(page.getByLabel(/^Kullan\u0131c\u0131 ad\u0131/)).toBeVisible();
    }
    await page.close();
  }
});
