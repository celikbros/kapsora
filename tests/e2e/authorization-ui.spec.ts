import { test, expect } from '@playwright/test';
import type { Authorization, CreateAuthorization, ServiceRequest } from '@kapsora/api-client';
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base,
  'requires the operator-started seeded UI; authorization responses are intercepted',
);
test('authorization UI: validation, uncertain retry, failure recovery and provider visibility', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  const password = process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora';
  let failedList = false;
  let staleList = true;
  let created: Authorization | null = null;
  const sends: { key: string | undefined; body: CreateAuthorization }[] = [];
  async function login(user: string, path: string) {
    await page.goto(base + path);
    await page.getByLabel(/Kullanıcı adı/).fill(user);
    await page.getByLabel(/^Parola/).fill(password);
    const ready = page.waitForResponse(
      (r) => new URL(r.url()).pathname === '/api/v1/session/login',
    );
    await page.getByRole('button', { name: 'Giriş yap', exact: true }).click();
    return (await ready).json();
  }
  await login('doctor.a', '/requests');
  await page.getByRole('heading', { name: 'Talepler', exact: true }).waitFor();
  const session = await page.evaluate(async () => await (await fetch('/api/v1/session')).json());
  const response = await page.evaluate(async (tenant) => {
    const r = await fetch('/api/v1/service-requests?status=APPROVED&limit=100', {
      headers: { 'X-Tenant-ID': tenant, 'X-Kapsora-App': 'backoffice' },
    });
    return { status: r.status, body: await r.json() };
  }, session.activeTenantId);
  if (response.status !== 200) throw new Error('Request read failed: ' + response.status);
  const request = response.body.items.find(
    (r: ServiceRequest) => r.personDisplayName === 'Melis Üye',
  );
  if (!request) throw new Error('No approved demo request found');
  await page.route('**/api/v1/authorizations?*', (route) =>
    route.fulfill({
      status: failedList ? 503 : 200,
      contentType: failedList ? 'application/problem+json' : 'application/json',
      body: JSON.stringify(
        failedList
          ? {
              type: 'about:blank',
              status: 503,
              title: 'Geçici bağlantı sorunu',
              code: 'TEMPORARY_UNAVAILABLE',
              traceId: 'ui-review',
            }
          : { items: created && !staleList ? [created] : [], nextCursor: null },
      ),
    }),
  );
  await page.route('**/api/v1/authorizations', async (route) => {
    const r = route.request();
    if (r.method() !== 'POST') throw new Error('Unexpected request');
    sends.push({ key: r.headers()['idempotency-key'], body: r.postDataJSON() });
    if (sends.length === 1) {
      await route.abort('failed');
      return;
    }
    const now = new Date().toISOString();
    created = {
      id: '00000000-0000-4000-8000-000000000001',
      requestId: request.id,
      reference: 'AUT-20260922-UIREVIEW',
      status: 'ACTIVE',
      validFrom: now,
      validTo: sends[0]!.body.validTo,
      reservedTotal: '1',
      consumedTotal: '0',
      approvedAt: now,
      createdAt: now,
      rowVersion: 1,
      items: [],
      vouchers: [],
    };
    await route.fulfill({
      status: 201,
      contentType: 'application/json',
      headers: { ETag: '"1"' },
      body: JSON.stringify(created),
    });
  });
  await page.goto(base + '/requests/' + request.id);
  const panel = page.getByTestId('request-authorization');
  await expect(panel.getByRole('button', { name: 'Hak ayır', exact: true })).toBeVisible();
  await page.screenshot({ path: '.impeccable/review/authorization-desktop.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await panel.scrollIntoViewIfNeeded();
  await page.screenshot({ path: '.impeccable/review/authorization-mobile.png', fullPage: true });
  if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth))
    throw new Error('Mobile overflow');
  await panel.getByLabel(/Geçerlilik sonu/).fill('2026-01-01T12:00');
  await panel.getByRole('button', { name: 'Hak ayır', exact: true }).click();
  await expect(panel.getByText('Şimdiden sonraki bir tarih ve saat seçin.')).toBeVisible();
  if (sends.length) throw new Error('Invalid date sent');
  const date = new Date(Date.now() + 2 * 86400000);
  date.setMinutes(date.getMinutes() - date.getTimezoneOffset());
  await panel.getByLabel(/Geçerlilik sonu/).fill(date.toISOString().slice(0, 16));
  await panel.getByRole('button', { name: 'Hak ayır', exact: true }).click();
  await expect(panel.getByRole('button', { name: 'Hak ayırmayı yeniden dene' })).toBeEnabled();
  await expect(panel.getByLabel(/Geçerlilik sonu/)).toBeDisabled();
  await panel.getByRole('button', { name: 'Hak ayırmayı yeniden dene' }).click();
  await expect(panel.getByText('AUT-20260922-UIREVIEW')).toBeVisible();
  if (JSON.stringify(sends[0]) !== JSON.stringify(sends[1]))
    throw new Error('Retry changed payload or key');
  await expect(panel.getByRole('button', { name: 'Hak ayır', exact: true })).toHaveCount(0);
  await page.screenshot({
    path: '.impeccable/review/authorization-reserved-mobile.png',
    fullPage: true,
  });
  failedList = true;
  await page.reload();
  await expect(panel.getByRole('button', { name: 'Yeniden dene', exact: true })).toBeVisible({
    timeout: 20000,
  });
  await expect(panel.getByRole('button', { name: 'Hak ayır', exact: true })).toHaveCount(0);
  await page.screenshot({
    path: '.impeccable/review/authorization-error-mobile.png',
    fullPage: true,
  });
  failedList = false;
  staleList = false;
  await page.getByRole('button', { name: 'Kullanıcı menüsü', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Çıkış yap', exact: true }).click();
  await login('provider.a', '/portal/requests/' + request.id);
  await expect(panel.getByText('AUT-20260922-UIREVIEW')).toBeVisible();
  await expect(panel.getByRole('button', { name: 'Hak ayır', exact: true })).toHaveCount(0);
  await page.screenshot({
    path: '.impeccable/review/authorization-provider-mobile.png',
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.screenshot({
    path: '.impeccable/review/authorization-provider-desktop.png',
    fullPage: true,
  });
  await page.getByRole('button', { name: 'Çıkış yap', exact: true }).click();
  expect(sends).toHaveLength(2);
});
