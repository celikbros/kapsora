import { mkdir } from 'node:fs/promises';
import { expect, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type AccessPage = components['schemas']['HealthAccessLogPage'];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.use({ trace: 'off' });
test.skip(process.env['E2E_REAL_API'] !== '1' || !base, 'requires operator-started local system');

// Read historical access events only; do not open clinical records to manufacture events.
for (const username of ['admin.a', 'financial.reviewer', 'doctor.a', 'sponsor.hr']) {
  test(`health access audit entry uses existing permissions for ${username}`, async ({
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
      page.on('request', (request) => {
        const path = new URL(request.url()).pathname;
        if (
          /^\/api\/v1\/(health-access-log|people|health-cases|medical-reports|organizations|providers)(\/|$)/.test(
            path,
          )
        ) {
          if (request.method() === 'GET') dataReads.push(path);
          else commands.push(path);
        }
      });
      const loaded =
        username === 'admin.a'
          ? page.waitForResponse(
              (r) =>
                new URL(r.url()).pathname === '/api/v1/health-access-log' &&
                r.request().method() === 'GET',
            )
          : null;
      await page.goto(base + '/security');
      const audit = page.getByTestId('health-access-log-page');
      await expect(audit).toBeVisible();
      if (username !== 'admin.a') {
        await expect(
          audit.getByText('Bu i\u015flem i\u00e7in yetkiniz yok.', { exact: true }),
        ).toBeVisible();
        await expect(audit.getByTestId('health-access-log-row')).toHaveCount(0);
        await expect(
          page.getByRole('navigation', { name: 'Ana men\u00fc' }).getByRole('link', {
            name: 'Sa\u011fl\u0131k verisi eri\u015fim kay\u0131tlar\u0131',
            exact: true,
          }),
        ).toHaveCount(0);
        expect(
          dataReads,
          'denied entry must make no member, clinical, directory or audit requests',
        ).toEqual([]);
        expect(commands).toEqual([]);
        return;
      }
      const response = await loaded!;
      expect(response.status()).toBe(200);
      expect(new URL(response.url()).searchParams.has('personId')).toBe(false);
      const first = (await response.json()) as AccessPage;
      expect(
        first.items.length,
        'existing historical health audit rows must be available',
      ).toBeGreaterThan(0);
      await expect(audit.getByTestId('health-access-log-row')).toHaveCount(first.items.length);
      if (first.nextCursor) {
        const nextLoaded = page.waitForResponse(
          (r) =>
            new URL(r.url()).pathname === '/api/v1/health-access-log' &&
            new URL(r.url()).searchParams.get('cursor') === first.nextCursor &&
            r.request().method() === 'GET',
        );
        await audit.getByTestId('health-access-log-next').click();
        const nextResponse = await nextLoaded;
        expect(nextResponse.status()).toBe(200);
        const second = (await nextResponse.json()) as AccessPage;
        await expect(audit.getByTestId('health-access-log-row')).toHaveCount(second.items.length);
        expect(second.items[0]?.id).not.toBe(first.items[0]?.id);
        await audit.getByTestId('health-access-log-previous').click();
        await expect(audit.getByTestId('health-access-log-row')).toHaveCount(first.items.length);
        await expect(audit.getByTestId('health-access-log-next')).toBeEnabled();
        await audit.getByTestId('health-access-log-next').click();
        await expect(audit.getByTestId('health-access-log-row')).toHaveCount(second.items.length);
        await audit.getByTestId('health-access-log-previous').click();
        await expect(audit.getByTestId('health-access-log-row')).toHaveCount(first.items.length);
      }
      await mkdir('.impeccable/review/security-entry', { recursive: true });
      for (const width of [1440, 390]) {
        await page.setViewportSize({ width, height: 1000 });
        await page.evaluate(() => window.scrollTo(0, 0));
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
          true,
        );
        await expect(audit.getByTestId('health-access-log-row')).toHaveCount(first.items.length);
        await page.screenshot({
          path: `.impeccable/review/security-entry/${username}-${width}.png`,
          fullPage: false,
        });
      }
      expect(
        dataReads.every((path) => path === '/api/v1/health-access-log'),
        'audit entry must not require member/directory/clinical access',
      ).toBe(true);
      expect(commands, 'audit entry must not mutate records').toEqual([]);
    } finally {
      await actor.close();
      await page.close();
    }
  });
}

// Visual evidence is wholly synthetic: the intercepted endpoint never reaches the API.
test('synthetic audit presentation fits mobile and desktop', async ({ browser }) => {
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const actor = new Actor(page.request, 'backoffice', true);
  const synthetic: AccessPage = {
    items: ['SUCCESS', 'DENIED'].map((outcome, index) => ({
      id: `00000000-0000-4000-8000-00000000000${index}`,
      actorId: '00000000-0000-4000-8000-000000000100',
      personId: '00000000-0000-4000-8000-000000000200',
      resourceId: '00000000-0000-4000-8000-000000000300',
      resourceType: 'SYNTHETIC_RESOURCE',
      occurredAt: '2026-10-01T12:00:00Z',
      accessType: 'VIEW' as const,
      outcome: outcome as 'SUCCESS' | 'DENIED',
      purposeCode: 'SYNTHETIC_TEST',
      reasonText: 'Synthetic visual test only; no real person, resource or health data.',
    })),
    nextCursor: null,
  };
  try {
    await actor.login('admin.a');
    await page.route('**/api/v1/health-access-log?*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(synthetic),
      }),
    );
    await page.goto(base + '/security');
    const rows = page.getByTestId('health-access-log-row');
    await expect(rows).toHaveCount(2);
    await mkdir('.impeccable/review/security-entry', { recursive: true });
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 1000 });
      await expect(rows).toHaveCount(2);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      );
      await page.screenshot({
        path: `.impeccable/review/security-entry/synthetic-${width}.png`,
        fullPage: false,
      });
    }
  } finally {
    await actor.close();
    await page.close();
  }
});
