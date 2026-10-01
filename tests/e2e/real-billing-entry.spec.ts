import { expect, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const paidId = process.env['E2E_EXISTING_PAID_SETTLEMENT_ID'] ?? '';
const label = '\u0130cmal ve \u00d6deme';
type Settlement = components['schemas']['Settlement'];

test.use({ trace: 'off' });
test.skip(process.env['E2E_REAL_API'] !== '1' || !base, 'requires operator-started local system');

// Read-only main entry checks. Never create or update financial records.
for (const username of ['payer.approver', 'financial.reviewer', 'sponsor.hr', 'doctor.a']) {
  test(`billing home and sidebar entry uses permitted lists for ${username}`, async ({
    browser,
  }) => {
    expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
    const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
    page.setDefaultTimeout(15_000);
    const actor = new Actor(page.request, 'backoffice', true);
    const reads: string[] = [];
    const commands: string[] = [];
    const refused: string[] = [];
    try {
      await actor.login(username);
      page.on('request', (request) => {
        const path = new URL(request.url()).pathname;
        if (
          /^\/api\/v1\/(batches|settlements|invoices|reimbursements|work-items)(\/|$)/.test(path)
        ) {
          if (request.method() === 'GET') reads.push(path);
          else commands.push(path);
        }
      });
      page.on('response', (response) => {
        if (
          new URL(response.url()).pathname.startsWith('/api/v1/') &&
          response.request().method() === 'GET' &&
          [403, 404].includes(response.status())
        ) {
          refused.push(new URL(response.url()).pathname);
        }
      });
      await page.goto(base + '/');
      const sidebar = page.getByRole('navigation', { name: 'Ana men\u00fc' });
      const menu = sidebar.getByRole('link', { name: label, exact: true });
      if (username === 'doctor.a') {
        await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
        await expect(page.getByRole('link', { name: label, exact: true })).toHaveCount(0);
        expect(reads).toEqual([]);
        expect(commands).toEqual([]);
        return;
      }
      const dashboard = page.getByTestId('dashboard');
      await expect(dashboard).toBeVisible();
      const claimLinks = dashboard.getByTestId('dashboard-claims').locator('a');
      const workLinks = dashboard.getByTestId('dashboard-workitems').locator('a');
      if (username === 'payer.approver') {
        await expect(claimLinks).toHaveCount(0);
        await expect(workLinks).toHaveCount(1);
        const workLoaded = page.waitForResponse(
          (response) =>
            new URL(response.url()).pathname === '/api/v1/work-items' &&
            response.request().method() === 'GET',
        );
        await workLinks.click();
        expect((await workLoaded).status()).toBe(200);
        await expect(page).toHaveURL(/\/worklist\?view=overdue$/);
        await page.getByRole('link', { name: 'KAPSORA', exact: true }).click();
        await expect(page).toHaveURL(base + '/');
      } else {
        await expect(claimLinks.first()).toBeVisible();
        await expect(workLinks).toHaveCount(username === 'financial.reviewer' ? 1 : 0);
      }
      const path = username === 'payer.approver' ? '/billing/settlements' : '/billing/batches';
      const endpoint = username === 'payer.approver' ? '/api/v1/settlements' : '/api/v1/batches';
      // Click the actual main menu before checking its destination, to reproduce refusals.
      const loaded = page.waitForResponse(
        (response) =>
          ['/api/v1/settlements', '/api/v1/batches'].includes(new URL(response.url()).pathname) &&
          response.request().method() === 'GET',
      );
      await menu.click();
      const response = await loaded;
      expect(response.status(), 'main billing link must open a permitted list').toBe(200);
      expect(new URL(response.url()).pathname).toBe(endpoint);
      await expect(page).toHaveURL(base + path);
      await expect(menu).toHaveAttribute('href', path);
      await page.getByRole('link', { name: 'KAPSORA', exact: true }).click();
      await expect(page).toHaveURL(base + '/');
      const homeLink = page
        .getByRole('link', { name: label, exact: true })
        .filter({ visible: true });
      await expect(homeLink).toHaveCount(2);
      await expect(homeLink.nth(1)).toHaveAttribute('href', path);
      await homeLink.nth(1).click();
      await expect(page).toHaveURL(base + path);
      await expect(
        page.getByTestId(username === 'payer.approver' ? 'settlement-table' : 'batch-table'),
      ).toBeVisible();
      if (username === 'payer.approver') {
        expect(reads.filter((read) => /^\/api\/v1\/(batches|invoices)(\/|$)/.test(read))).toEqual(
          [],
        );
        await expect(
          page.getByTestId('billing-nav').locator('a[href="/billing/batches"]'),
        ).toHaveCount(0);
        if (paidId) {
          const before = await actor.call<Settlement>('GET', '/api/v1/settlements/' + paidId);
          expect(before.data.status).toBe('PAID');
          await page.goto(base + '/billing/settlements/' + paidId);
          await expect(page.getByTestId('settlement-status')).toHaveText('\u00d6dendi');
          await expect(page.getByTestId('payable')).toHaveText('800,00 TRY');
          await expect(page.getByTestId('paid')).toHaveText('800,00 TRY');
          const after = await actor.call<Settlement>('GET', '/api/v1/settlements/' + paidId);
          expect(after.etag).toBe(before.etag);
          expect(after.data.status).toBe(before.data.status);
          expect(after.data.payableAmount).toBe(before.data.payableAmount);
          expect(after.data.paidAmount).toBe(before.data.paidAmount);
          expect(after.data.payments.length).toBe(before.data.payments.length);
        }
      }
      expect(commands).toEqual([]);
      expect(refused, 'visible billing destinations must not request forbidden records').toEqual(
        [],
      );
    } finally {
      await actor.close();
      await page.close();
    }
  });
}

for (const username of ['financial.reviewer', 'payer.approver']) {
  test(`pending settlement controls follow existing permissions for ${username}`, async ({
    browser,
  }) => {
    expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
    const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
    const actor = new Actor(page.request, 'backoffice', true);
    const commands: string[] = [];
    try {
      await actor.login(username);
      const list = await actor.call<components['schemas']['SettlementPage']>(
        'GET',
        '/api/v1/settlements?status=PENDING_APPROVAL&limit=100',
      );
      const pending = list.data.items.find((row) =>
        ['600', '600.00'].includes(String(row.payableAmount)),
      );
      expect(!!pending, 'retained cut settlement must exist; do not create a replacement').toBe(
        true,
      );
      const id = pending!.id;
      const before = await actor.call<Settlement>('GET', '/api/v1/settlements/' + id);
      page.on('request', (request) => {
        const path = new URL(request.url()).pathname;
        if (
          /^\/api\/v1\/(batches|settlements|invoices|reimbursements|work-items)(\/|$)/.test(path) &&
          request.method() !== 'GET'
        )
          commands.push(path);
      });
      await page.goto(base + '/billing/settlements/' + id);
      await expect(page.getByTestId('settlement-status')).toHaveText('Onay bekliyor');
      await expect(page.getByTestId('payable')).toHaveText('600,00 TRY');
      await expect(page.getByTestId('approve-settlement')).toHaveCount(
        username === 'payer.approver' ? 1 : 0,
      );
      await expect(page.getByRole('button', { name: '\u0130ptal et', exact: true })).toHaveCount(
        username === 'payer.approver' ? 1 : 0,
      );
      await expect(page.getByTestId('payment-form')).toHaveCount(0);
      for (const width of [1440, 390]) {
        await page.setViewportSize({ width, height: 1000 });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
          true,
        );
      }
      const after = await actor.call<Settlement>('GET', '/api/v1/settlements/' + id);
      expect(after.etag).toBe(before.etag);
      expect(after.data.status).toBe('PENDING_APPROVAL');
      expect(after.data.payableAmount).toBe(before.data.payableAmount);
      expect(after.data.paidAmount).toBe(before.data.paidAmount);
      expect(after.data.payments.length).toBe(before.data.payments.length);
      expect(commands, 'control inspection must not approve, cancel or pay').toEqual([]);
    } finally {
      await actor.close();
      await page.close();
    }
  });
}

test('denied billing direct visits make no protected data requests', async ({ browser }) => {
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const actor = new Actor(page.request, 'backoffice', true);
  const reads: string[] = [];
  try {
    await actor.login('doctor.a');
    page.on('request', (request) => {
      const path = new URL(request.url()).pathname;
      if (/^\/api\/v1\/(batches|settlements|invoices|reimbursements|work-items)(\/|$)/.test(path))
        reads.push(path);
    });
    const id = '00000000-0000-4000-8000-000000000000';
    for (const path of [
      '/billing/batches',
      '/billing/batches/' + id,
      '/billing/settlements',
      '/billing/settlements/' + id,
      '/billing/reimbursements',
      '/billing/reimbursements/' + id,
    ]) {
      await page.goto(base + path);
      await expect(
        page.getByText('Bu i\u015flem i\u00e7in yetkiniz yok.', { exact: true }),
      ).toBeVisible();
    }
    expect(reads).toEqual([]);
  } finally {
    await actor.close();
    await page.close();
  }
});

test('finance reads the existing paid reimbursement without forbidden lookups', async ({
  browser,
}) => {
  const id = process.env['E2E_EXISTING_PAID_REIMBURSEMENT_ID'] ?? '';
  test.skip(!id, 'requires the retained already-paid reimbursement');
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const actor = new Actor(page.request, 'backoffice', true);
  const lookups: string[] = [];
  const commands: string[] = [];
  const refused: string[] = [];
  try {
    await actor.login('financial.reviewer');
    const path = '/api/v1/reimbursements/' + id;
    const before = await actor.call<components['schemas']['Reimbursement']>('GET', path);
    expect(before.data.status).toBe('PAID');
    page.on('request', (request) => {
      const pathname = new URL(request.url()).pathname;
      if (
        /^\/api\/v1\/(organizations|service-definitions|service-categories)(\/|$)/.test(pathname)
      ) {
        lookups.push(pathname);
      }
      if (
        /^\/api\/v1\/(batches|settlements|invoices|reimbursements|work-items)(\/|$)/.test(
          pathname,
        ) &&
        request.method() !== 'GET'
      )
        commands.push(pathname);
    });
    page.on('response', (response) => {
      if (
        new URL(response.url()).pathname.startsWith('/api/v1/') &&
        response.request().method() === 'GET' &&
        [403, 404].includes(response.status())
      ) {
        refused.push(new URL(response.url()).pathname);
      }
    });
    await page.goto(base + '/billing/reimbursements/' + id);
    await expect(page.getByTestId('reimbursement-status')).toHaveText('\u00d6dendi');
    await expect(page.getByTestId('approved')).toHaveText('125,50 TRY');
    await expect(page.getByTestId('reimbursement-decision')).toHaveCount(0);
    await expect(page.getByTestId('reimbursement-payment')).toHaveCount(0);
    await expect(
      page
        .getByTestId('reimbursement-facts')
        .locator('dd')
        .filter({ hasText: /^\u2026$/ }),
    ).toHaveCount(0);
    const after = await actor.call<components['schemas']['Reimbursement']>('GET', path);
    expect(after.etag).toBe(before.etag);
    expect(after.data.status).toBe(before.data.status);
    expect(after.data.approvedAmount).toBe(before.data.approvedAmount);
    expect(lookups, 'finance has no organization/catalog read grants').toEqual([]);
    expect(refused).toEqual([]);
    expect(commands).toEqual([]);
  } finally {
    await actor.close();
    await page.close();
  }
});
