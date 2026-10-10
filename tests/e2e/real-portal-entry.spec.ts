import { expect, test } from '@playwright/test';

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const password = process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora';
test.use({ trace: 'off' });
test.skip(process.env['E2E_REAL_API'] !== '1' || !base, 'requires operator-started local system');

for (const [username, destination] of [
  ['billing.a', '/portal/billing'],
  ['hotel.billing.a', '/portal/billing'],
  ['reservation.a', '/portal/lodging/desk'],
  ['provider.a', '/portal/'],
] as const) {
  test(`shared sign-in opens permitted provider work for ${username}`, async ({ browser }) => {
    test.setTimeout(90_000);
    expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
    const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
    const refused: string[] = [];
    const clinicalReads: string[] = [];
    const financialReads: string[] = [];
    const commands: string[] = [];
    try {
      page.on('response', (r) => {
        const path = new URL(r.url()).pathname;
        if (path.startsWith('/api/v1/') && [403, 404].includes(r.status())) refused.push(path);
      });
      page.on('request', (r) => {
        const path = new URL(r.url()).pathname;
        if (
          /^\/api\/v1\/(service-requests|eligibility|health-cases|medical-reports|stays|people|service-definitions)(\/|$)/.test(
            path,
          ) &&
          // The reservation desk may resolve guest labels through its existing member.read grant.
          !(username === 'reservation.a' && /^\/api\/v1\/people(\/|$)/.test(path))
        )
          clinicalReads.push(path);
        if (
          /^\/api\/v1\/(claims|invoices|batches|exports)(\/|$)/.test(path) ||
          /^\/api\/v1\/providers\/[^/]+\/(earnings|statement)$/.test(path)
        )
          financialReads.push(path);
        if (
          path.startsWith('/api/v1/') &&
          !path.startsWith('/api/v1/session') &&
          r.method() !== 'GET'
        )
          commands.push(path);
      });
      await page.goto(base + '/auth/login');
      await page.getByLabel(/^Kullan\u0131c\u0131 ad\u0131/).fill(username);
      await page.getByLabel(/^Parola/).fill(password);
      await page.getByRole('button', { name: 'Giri\u015f yap', exact: true }).click();
      await expect(page).toHaveURL(base + destination);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      const nav = page.getByRole('navigation');
      if (username !== 'provider.a') {
        for (const name of ['Yeni talep', 'Taleplerim', 'Uygunluk sorgusu', 'Vakalar']) {
          await expect(nav.getByRole('link', { name, exact: true })).toHaveCount(0);
        }
        expect(clinicalReads).toEqual([]);
        const denied = [
          '/requests',
          '/requests/00000000-0000-0000-0000-000000000001',
          '/eligibility',
          '/cases',
          '/cases/new',
          '/cases/00000000-0000-0000-0000-000000000001',
          '/reports/00000000-0000-0000-0000-000000000001',
          '/stays/00000000-0000-0000-0000-000000000001',
        ];
        if (username === 'reservation.a') {
          denied.push(
            '/claims',
            '/claims/new',
            '/claims/00000000-0000-0000-0000-000000000001',
            '/billing/invoices',
          );
        }
        for (const path of denied) {
          await page.goto(base + '/portal' + path);
          await expect(page.getByTestId('provider-route-denied')).toBeVisible();
        }
        expect(clinicalReads, 'denied direct routes must not mount supporting reads').toEqual([]);
        await page.goto(base + destination);
        await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      } else {
        for (const path of [
          '/claims',
          '/billing',
          '/billing/invoices',
          '/billing/batches',
          '/billing/statement',
          '/lodging/desk',
          '/lodging/inventory',
        ]) {
          await page.goto(base + '/portal' + path);
          await expect(page.getByTestId('provider-route-denied')).toBeVisible();
        }
        expect(financialReads, 'clinical staff must not mount denied financial readers').toEqual(
          [],
        );
        await page.goto(base + destination);
        await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      }
      if (username === 'billing.a' || username === 'hotel.billing.a') {
        await page.goto(base + '/portal/billing/statement');
        if (process.env['E2E_EXISTING_STATEMENT_PERIOD_FROM']) {
          await page
            .locator('input[name="periodFrom"]')
            .fill(process.env['E2E_EXISTING_STATEMENT_PERIOD_FROM']);
        }
        if (process.env['E2E_EXISTING_STATEMENT_PERIOD_TO']) {
          await page
            .locator('input[name="periodTo"]')
            .fill(process.env['E2E_EXISTING_STATEMENT_PERIOD_TO']);
        }
        await expect(page.getByTestId('statement-totals')).toBeVisible();
        if (process.env['E2E_EXISTING_STATEMENT_PERIOD_TO']) {
          await expect(page.getByTestId('statement-settlement').first()).toBeVisible();
        }
        expect(clinicalReads, 'financial reads must not fetch clinical label data').toEqual([]);
      }
      if (username === 'billing.a' && process.env['E2E_EXISTING_HEALTH_CLAIM_ID']) {
        await page.goto(base + '/portal/claims/' + process.env['E2E_EXISTING_HEALTH_CLAIM_ID']);
        await expect(page.getByTestId('claim-lines')).toBeVisible();
        expect(
          clinicalReads,
          'claim detail must use financial projection and ID fallbacks',
        ).toEqual([]);
      }
      expect(refused, 'the offered landing must not read forbidden data').toEqual([]);
      expect(commands, 'entry checks must not create business records').toEqual([]);
      for (const width of [390, 1440]) {
        await page.setViewportSize({ width, height: 900 });
        expect(
          await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
        ).toBe(true);
      }
    } finally {
      const logout = page.getByRole('button', {
        name: '\u00c7\u0131k\u0131\u015f yap',
        exact: true,
      });
      if (await logout.isVisible()) {
        await logout.click();
        await expect(
          page.getByRole('button', { name: 'Giri\u015f yap', exact: true }),
        ).toBeVisible();
      }
      await page.close();
    }
  });
}
