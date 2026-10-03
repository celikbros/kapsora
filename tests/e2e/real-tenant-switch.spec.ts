import { setTimeout as delay } from 'node:timers/promises';
import { expect, test, type Page } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.use({ trace: 'off' });
test.skip(process.env['E2E_REAL_API'] !== '1' || !base, 'requires operator-started real demo');

async function choose(page: Page, code: string) {
  const response = page.waitForResponse(
    (r) => new URL(r.url()).pathname === '/api/v1/session/switch-tenant',
  );
  await page
    .getByRole('listitem')
    .filter({ hasText: code })
    .getByRole('button', { name: 'Seç', exact: true })
    .click();
  const switched = await response;
  expect(switched.status()).toBe(200);
  const id = (switched.request().postDataJSON() as { tenantId: string }).tenantId;
  await expect(page.locator('[title^="Aktif kurum:"]')).toHaveAttribute('title', new RegExp(code));
  return id;
}
async function picker(page: Page) {
  await page.getByRole('button', { name: 'Kullanıcı menüsü', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Kurum değiştir', exact: true }).click();
  await expect(page).toHaveURL(/auth\/tenant/);
}
async function exportsFor(page: Page, tenantId: string) {
  await page.goto(base + '/billing/exports');
  const response = page.waitForResponse((r) => {
    const url = new URL(r.url());
    return url.pathname === '/api/v1/exports' && url.searchParams.get('mine') === 'false';
  });
  await page.getByLabel('Yalnızca benimkiler', { exact: true }).uncheck();
  const result = await response;
  expect(result.status()).toBe(200);
  expect(result.request().headers()['x-tenant-id']).toBe(tenantId);
  return ((await result.json()) as S<'ExportPage'>).items;
}

test('real tenant switching replaces export context and hides the previous tenant record', async ({
  page,
}) => {
  test.setTimeout(120000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  await page.goto(base + '/auth/login');
  await page.getByLabel(/^Kullanıcı adı/).fill('both.ab');
  await page
    .getByLabel(/^Parola/)
    .fill(process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora');
  // Combined acceptance shares the real per-address login limit. Only retry a
  // rejected login, honoring the server's delay without changing its limits.
  for (let attempt = 0; ; attempt++) {
    const response = page.waitForResponse(
      (r) =>
        r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/v1/session/login',
    );
    await page.getByRole('button', { name: 'Giriş yap', exact: true }).click();
    const result = await response;
    if (result.status() !== 429 || attempt === 3) {
      expect(result.status(), 'browser login').toBe(200);
      break;
    }
    const seconds = Number(result.headers()['retry-after']);
    expect(Number.isFinite(seconds) && seconds > 0 && seconds <= 30, 'bounded Retry-After').toBe(
      true,
    );
    await delay(seconds * 1000);
  }
  await expect(page).toHaveURL(/auth\/tenant/);
  const tenantA = await choose(page, 'DEMO_A');
  const exportsA = await exportsFor(page, tenantA);
  expect(exportsA.length, 'use an existing accepted export, never create one here').toBeGreaterThan(
    0,
  );
  const knownId = exportsA[0]!.id;
  // The UI's secure localhost cookie is sent by the browser fetch implementation.
  // Keep the read in that context, rather than the Node API client's cookie policy.
  const readKnown = (tenantId: string) =>
    page.evaluate(
      async ({ tenantId, knownId }) => {
        const response = await fetch(`/api/v1/exports/${knownId}`, {
          credentials: 'include',
          headers: { 'X-Tenant-ID': tenantId, 'X-Kapsora-App': 'backoffice' },
        });
        return response.status;
      },
      { tenantId, knownId },
    );
  expect(await readKnown(tenantA)).toBe(200);
  await picker(page);
  const tenantB = await choose(page, 'DEMO_B');
  expect(tenantB).not.toBe(tenantA);
  const exportsB = await exportsFor(page, tenantB);
  const idsA = new Set(exportsA.map((e) => e.id));
  expect(exportsB.some((e) => idsA.has(e.id))).toBe(false);
  expect(await readKnown(tenantB)).toBe(404);
  for (const width of [390, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(page.locator('[title^="Aktif kurum:"]')).toBeVisible();
    await expect(page.locator('[title^="Aktif kurum:"]')).toHaveAttribute('title', /DEMO_B/);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
  }
  await picker(page);
  expect(await choose(page, 'DEMO_A')).toBe(tenantA);
  expect(await readKnown(tenantA)).toBe(200);
  await page.getByRole('button', { name: 'Kullanıcı menüsü', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Çıkış yap', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Oturum kapatıldı', exact: true })).toBeVisible();
  expect(await readKnown(tenantA)).toBe(401);
  await page.getByRole('link', { name: 'Yeniden giriş yap', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Giriş yap', exact: true })).toBeVisible();
});
