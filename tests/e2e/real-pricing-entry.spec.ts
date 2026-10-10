import { expect, request, test } from '@playwright/test';
import type { PriceQuote } from '../../web/packages/api-client/src/pricing';
import { Actor } from './real-api-actor';

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
type ProviderOption = { providerProfileId: string; organizationName: string };
type ServiceOption = { serviceDefinitionId: string; code: string; name: string };
type OptionPage<T> = { items: T[]; nextCursor?: string | null };
test.use({ trace: 'off' });
test.skip(process.env['E2E_REAL_API'] !== '1' || !base, 'requires operator-started local system');

test('finance price query obtains permitted provider and service choices', async ({ browser }) => {
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  page.setDefaultTimeout(15_000);
  const actor = new Actor(page.request, 'backoffice', true);
  const forbidden: string[] = [];
  try {
    await actor.login('financial.reviewer');
    page.on('response', (response) => {
      const path = new URL(response.url()).pathname;
      if (path.startsWith('/api/v1/') && response.status() === 403) forbidden.push(path);
    });
    await page.goto(base + '/pricing');
    await expect(page.getByRole('heading', { name: 'Fiyat sorgusu', exact: true })).toBeVisible();
    await expect(page.locator('select[name="providerProfileId"] option').nth(1)).toBeAttached();
    await expect(page.locator('select[name^="service-"] option').nth(1)).toBeAttached();
    expect(
      forbidden,
      'permitted pricing form must not read forbidden directory/catalog lists',
    ).toEqual([]);
  } finally {
    await actor.close();
    await page.close();
  }
});

test('finance pricing options expose only bounded input metadata without directory grants', async () => {
  test.setTimeout(90_000);
  const actor = new Actor(await request.newContext(), 'backoffice', true);
  try {
    await actor.login('financial.reviewer');
    await actor.call('GET', '/api/v1/providers?limit=1', undefined, { expected: 403 });
    await actor.call('GET', '/api/v1/service-definitions?limit=1', undefined, { expected: 403 });
    for (const kind of ['providers', 'services'] as const) {
      const ids: string[] = [];
      const cursors = new Set<string>();
      let cursor = '';
      let pageCount = 0;
      do {
        const query = new URLSearchParams({ limit: '1', ...(cursor ? { cursor } : {}) });
        const { data } = await actor.call<OptionPage<ProviderOption | ServiceOption>>(
          'GET',
          '/api/v1/pricing/options/' + kind + '?' + query,
        );
        expect(data.items.length).toBeLessThanOrEqual(1);
        for (const item of data.items) {
          if ('providerProfileId' in item) {
            expect(Object.keys(item).sort()).toEqual(['organizationName', 'providerProfileId']);
            expect(item.organizationName).not.toBe('');
            ids.push(item.providerProfileId);
          } else {
            expect(Object.keys(item).sort()).toEqual(['code', 'name', 'serviceDefinitionId']);
            expect(item.code).not.toBe('');
            ids.push(item.serviceDefinitionId);
          }
        }
        cursor = data.nextCursor ?? '';
        if (cursor) {
          expect(cursors.has(cursor), 'pagination must progress').toBe(false);
          cursors.add(cursor);
        }
        pageCount++;
        expect(pageCount, 'local pricing metadata must remain bounded').toBeLessThan(100);
      } while (cursor);
      expect(ids.length).toBeGreaterThan(0);
      expect(new Set(ids).size).toBe(ids.length);
    }
  } finally {
    await actor.close();
  }
});

test('medical reviewer cannot obtain price input metadata or mount price form reads', async ({
  browser,
}) => {
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const actor = new Actor(page.request, 'backoffice', true);
  const reads: string[] = [];
  try {
    await actor.login('doctor.a');
    for (const kind of ['providers', 'services']) {
      await actor.call('GET', '/api/v1/pricing/options/' + kind, undefined, { expected: 403 });
    }
    page.on('request', (r) => {
      const path = new URL(r.url()).pathname;
      if (/^\/api\/v1\/(people|providers|service-definitions|pricing)(\/|$)/.test(path))
        reads.push(path);
    });
    await page.goto(base + '/pricing');
    await expect(page.getByRole('heading', { name: 'Fiyat sorgusu', exact: true })).toBeVisible();
    await expect(page.locator('form')).toHaveCount(0);
    expect(reads).toEqual([]);
  } finally {
    await actor.close();
    await page.close();
  }
});

test('finance computes one existing member quote without moving entitlement or creating financial records', async ({
  browser,
}) => {
  test.skip(
    process.env['E2E_CREATE_QUOTE'] !== '1',
    'explicit single explanatory quote acceptance',
  );
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const actor = new Actor(page.request, 'backoffice', true);
  const admin = new Actor(await request.newContext(), 'backoffice', true);
  const commands: string[] = [];
  const reads: string[] = [];
  const forbidden: string[] = [];
  try {
    await actor.login('financial.reviewer');
    await admin.login('admin.a');
    const people = await actor.call<{ items: { id: string; displayName: string }[] }>(
      'GET',
      '/api/v1/people?q=Melis&limit=20',
    );
    const person = people.data.items.find((p) => p.displayName === 'Melis \u00dcye');
    expect(person, 'reuse existing synthetic demo member').toBeDefined();
    const providers = await admin.call<{ items: { id: string; organizationName: string }[] }>(
      'GET',
      '/api/v1/providers?limit=100&status=ACTIVE',
    );
    const profile = providers.data.items.find((p) => /hastane/i.test(p.organizationName));
    expect(profile, 'reuse existing hospital profile').toBeDefined();
    const services = await actor.call<OptionPage<ServiceOption>>(
      'GET',
      '/api/v1/pricing/options/services?q=PHYSIO_SESSION&limit=100',
    );
    const service = services.data.items.find((s) => s.code === 'PHYSIO_SESSION');
    expect(service).toBeDefined();
    const date = new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Istanbul' }).format(
      new Date(),
    );
    const balances = async () =>
      (
        await admin.call<{ items: unknown[] }>(
          'GET',
          `/api/v1/people/${person!.id}/entitlements?asOf=${date}`,
        )
      ).data;
    const before = await balances();
    expect(before.items.length).toBeGreaterThan(0);
    page.on('request', (r) => {
      const path = new URL(r.url()).pathname;
      if (!path.startsWith('/api/v1/')) return;
      if (r.method() === 'GET') reads.push(path);
      else commands.push(path);
    });
    page.on('response', (r) => {
      if (new URL(r.url()).pathname.startsWith('/api/v1/') && [403, 404].includes(r.status()))
        forbidden.push(new URL(r.url()).pathname);
    });
    await page.goto(base + '/pricing');
    await page.locator('input[name="personSearch"]').fill('Melis');
    await expect(
      page.locator(`select[name="personId"] option[value="${person!.id}"]`),
    ).toBeAttached();
    await page.locator('select[name="personId"]').selectOption(person!.id);
    await expect(
      page.locator(`select[name="providerProfileId"] option[value="${profile!.id}"]`),
    ).toBeAttached();
    await page.locator('select[name="providerProfileId"]').selectOption(profile!.id);
    await page.locator('input[name="serviceSearch"]').fill('PHYSIO_SESSION');
    await expect(
      page.locator(`select[name^="service-"] option[value="${service!.serviceDefinitionId}"]`),
    ).toBeAttached();
    await page.locator('select[name^="service-"]').selectOption(service!.serviceDefinitionId);
    await page.locator('input[name="serviceDate"]').fill(date);
    const response = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === '/api/v1/pricing/quotes' && r.request().method() === 'POST',
    );
    await page.getByRole('button', { name: 'Hesapla', exact: true }).click();
    const reply = await response;
    expect(reply.status()).toBe(200);
    const quote = (await reply.json()) as PriceQuote;
    expect(quote.outcome).toBe('QUOTED');
    expect([quote.contractAmount, quote.payerAmount, quote.memberAmount]).toEqual([
      '400',
      '400',
      '0',
    ]);
    const payload = reply.request().postDataJSON() as {
      providerProfileId: string;
      items: { quantity: string }[];
    };
    expect(payload.providerProfileId).toBe(profile!.id);
    expect(payload.items[0]!.quantity).toBe('1');
    await expect(page.getByTestId('quote-table')).toBeVisible();
    await expect(page.locator('a[href^="/contract-versions/"]')).toHaveCount(0);
    expect(commands).toEqual(['/api/v1/pricing/quotes']);
    expect(
      reads.filter((p) =>
        /^\/api\/v1\/(providers|service-definitions|organizations)(\/|$)/.test(p),
      ),
    ).toEqual([]);
    expect(forbidden).toEqual([]);
    // Read-only snapshots compare every returned balance and row version, without arithmetic.
    expect(await balances()).toEqual(before);
    const replay = await actor.call<PriceQuote>('GET', '/api/v1/pricing/quotes/' + quote.id);
    expect(replay.data.id).toBe(quote.id);
    expect(await balances()).toEqual(before);
    for (const width of [390, 1440]) {
      await page.setViewportSize({ width, height: 900 });
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
      ).toBe(true);
    }
  } finally {
    await actor.close();
    await admin.close();
    await page.close();
  }
});
