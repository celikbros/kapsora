import { mkdir } from 'node:fs/promises';
import { expect, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.use({ trace: 'off' });
test.skip(process.env['E2E_REAL_API'] !== '1' || !base, 'requires operator-started local system');

// Reuse an existing synthetic inpatient member. No enrollment or adjustment commands.
for (const username of ['admin.a', 'sponsor.hr', 'financial.reviewer']) {
  test(`wallet discovery and entitlement deep links respect permissions for ${username}`, async ({
    browser,
  }) => {
    expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
    const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
    page.setDefaultTimeout(15_000);
    const actor = new Actor(page.request, 'backoffice', true);
    const dataReads: string[] = [];
    const commands: string[] = [];
    try {
      await actor.login(username);
      const members = (
        await actor.call<S<'PersonPage'>>('GET', '/api/v1/people?q=Deneme%20Yatis&limit=5')
      ).data.items;
      expect(
        members.length,
        'existing synthetic inpatient acceptance members must be present',
      ).toBeGreaterThan(0);
      const member = members[0]!;
      page.on('request', (request) => {
        const path = new URL(request.url()).pathname;
        if (/^\/api\/v1\/(people|entitlement-accounts|entitlement-adjustments)(\/|$)/.test(path)) {
          if (request.method() === 'GET') dataReads.push(path);
          else commands.push(path);
        }
      });
      await page.goto(base + '/wallets');
      const hub = page.getByTestId('wallets-services');
      await expect(hub).toBeVisible();
      expect(dataReads, 'landing must not fetch member or account records').toEqual([]);
      if (username === 'financial.reviewer') {
        await expect(hub.locator('a')).toHaveCount(0);
        await expect(
          page
            .getByRole('navigation', { name: 'Ana men\u00fc' })
            .getByRole('link', { name: 'Hak C\u00fczdanlar\u0131', exact: true }),
        ).toHaveCount(0);
        await page.goto(base + `/people/${member.id}?tab=entitlements`);
        await expect(page.getByRole('tab', { name: 'Kimlik', exact: true })).toHaveAttribute(
          'data-state',
          'active',
        );
        await expect(page.getByRole('tab', { name: 'Haklar', exact: true })).toHaveCount(0);
        expect(dataReads.filter((path) => /\/entitlements$|\/ledger$/.test(path))).toEqual([]);
        expect(commands).toEqual([]);
        return;
      }
      await expect(hub.getByTestId('wallets-people')).toBeVisible();
      await hub.getByTestId('wallets-people').click();
      await expect(page).toHaveURL(/\/people$/);
      await page.locator('input[name="q"]').fill('Deneme Yatis');
      await page.getByRole('button', { name: 'Ara', exact: true }).click();
      const memberShortcut = page
        .locator(`[data-testid="person-wallet-link"][href*="${member.id}"]`)
        .first();
      await expect(memberShortcut).toBeVisible();
      const accountsLoaded = page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === `/api/v1/people/${member.id}/entitlements` &&
          r.request().method() === 'GET',
      );
      await memberShortcut.click();
      const accountsResponse = await accountsLoaded;
      expect(accountsResponse.status()).toBe(200);
      const accounts = ((await accountsResponse.json()) as { items: S<'EntitlementAccount'>[] })
        .items;
      expect(accounts.length).toBeGreaterThan(0);
      await expect(page).toHaveURL(new RegExp(`/people/${member.id}\\?tab=entitlements$`));
      await expect(page.getByTestId('entitlement-table')).toBeVisible();
      expect(dataReads.filter((path) => path === '/api/v1/entitlement-accounts//ledger')).toEqual(
        [],
      );
      expect(dataReads.filter((path) => /\/ledger$/.test(path))).toEqual([]);
      await page.reload();
      await expect(page.getByTestId('entitlement-table')).toBeVisible();
      await page.getByRole('tab', { name: 'Kimlik', exact: true }).click();
      await expect(page.getByRole('tab', { name: 'Kimlik', exact: true })).toHaveAttribute(
        'data-state',
        'active',
      );
      await page.goBack();
      await expect(page.getByTestId('entitlement-table')).toBeVisible();
      await page.goForward();
      await expect(page.getByRole('tab', { name: 'Kimlik', exact: true })).toHaveAttribute(
        'data-state',
        'active',
      );
      await page.goto(base + `/people/${member.id}?tab=unknown`);
      await expect(page.getByRole('tab', { name: 'Kimlik', exact: true })).toHaveAttribute(
        'data-state',
        'active',
      );
      await page.goto(base + `/people/${member.id}?tab=entitlements`);
      await expect(page.getByTestId('entitlement-table')).toBeVisible();
      const account = accounts[0]!;
      const row = page
        .getByTestId('entitlement-table')
        .getByRole('row')
        .filter({ hasText: account.definition.name })
        .first();
      const ledgerLoaded = page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === `/api/v1/entitlement-accounts/${account.id}/ledger` &&
          r.request().method() === 'GET',
      );
      await row.getByRole('button', { name: 'Hareketler', exact: true }).click();
      expect((await ledgerLoaded).status()).toBe(200);
      await expect(page.getByRole('dialog')).toBeVisible();
      await expect(page.getByTestId('ledger-table')).toBeVisible();
      await expect(
        page
          .getByTestId('ledger-table')
          .getByRole('columnheader', { name: 'Bakiye de\u011fi\u015fimleri', exact: true }),
      ).toBeVisible();
      const heldMovement = page
        .getByTestId('ledger-table')
        .getByRole('row')
        .filter({ hasText: 'Bloke' })
        .first();
      await expect(heldMovement.locator('dl')).toBeVisible();
      await mkdir('.impeccable/review/wallet-entry', { recursive: true });
      for (const width of [1440, 390]) {
        await page.setViewportSize({ width, height: 1000 });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
          true,
        );
        await page.screenshot({
          path: `.impeccable/review/wallet-entry/${username}-ledger-${width}.png`,
          fullPage: true,
        });
      }
      expect(commands, 'read-only walkthrough must not adjust accounts').toEqual([]);
      expect(dataReads.filter((path) => path === '/api/v1/entitlement-accounts//ledger')).toEqual(
        [],
      );
    } finally {
      await actor.close();
      await page.close();
    }
  });
}
