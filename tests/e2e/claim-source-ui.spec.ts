import { expect, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type Schema<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base,
  'requires the operator-started UI; handoff responses are intercepted and no claim is written',
);
const caseId = '11111111-1111-4111-8111-111111111111';
const serviceId = '22222222-2222-4222-8222-222222222222';
const claimId = '33333333-3333-4333-8333-333333333333';
const source: Schema<'ClaimCaseSourceDetail'> = {
  source: {
    caseId,
    openedAt: '2026-09-23T06:00:00Z',
    serviceDate: '2026-09-23',
    requestReference: 'SR-DEMO-AYAKTAN',
    personDisplayName: 'Deneme Ayaktan',
    rowVersion: 7,
  },
  lines: [
    {
      serviceDefinitionId: serviceId,
      serviceCode: 'PHYSIO_SESSION',
      serviceName: 'Fizyoterapi seansı',
      unitType: 'SESSION',
      quantity: '1',
    },
  ],
};

test('billing selects a financial case source and retries an uncertain creation unchanged', async ({
  page,
}) => {
  const actor = new Actor(page.request, 'provider');
  const attempts: { body: unknown; key: string | undefined; etag: string | undefined }[] = [];
  let listFailed = true;
  let sourceFailed = true;
  let forbiddenReads = 0;
  await page.route(/\/api\/v1\/claims\/case-sources(?:\?.*)?$/, async (route) => {
    if (listFailed) {
      listFailed = false;
      await route.fulfill({
        status: 503,
        json: { status: 503, code: 'UNAVAILABLE', title: 'Liste yüklenemedi' },
      });
    } else await route.fulfill({ json: { items: [source.source] } });
  });
  await page.route(`**/api/v1/claims/case-sources/${caseId}`, async (route) => {
    if (route.request().method() === 'GET') {
      if (sourceFailed) {
        sourceFailed = false;
        await route.fulfill({
          status: 409,
          json: {
            status: 409,
            code: 'CLAIM_SOURCE_NOT_READY',
            title: 'Vaka faturalamaya hazır değil',
          },
        });
      } else await route.fulfill({ json: source, headers: { ETag: '"7"' } });
      return;
    }
    attempts.push({
      body: route.request().postDataJSON() as unknown,
      key: route.request().headers()['idempotency-key'],
      etag: route.request().headers()['if-match'],
    });
    if (attempts.length === 1)
      await route.fulfill({
        status: 503,
        json: { status: 503, code: 'UNAVAILABLE', title: 'Sonuç doğrulanamadı' },
      });
    else
      await route.fulfill({
        status: 201,
        json: { id: claimId, rowVersion: 1 },
        headers: { ETag: '"1"' },
      });
  });
  await page.route(`**/api/v1/claims/${claimId}`, (route) =>
    route.fulfill({
      status: 404,
      json: { status: 404, code: 'NOT_FOUND', title: 'Yalnızca form testi' },
    }),
  );
  page.on('request', (request) => {
    if (
      /\/api\/v1\/(health-cases|medical-reports|encounters|service-definitions|people)(\/|\?)/.test(
        request.url(),
      )
    )
      forbiddenReads++;
  });
  try {
    await actor.login('billing.a');
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(base + '/portal/claims/new');
    await expect(page.getByText('Liste yüklenemedi', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Yeniden dene', exact: true }).click();
    await page.getByLabel('Faturalandırılacak vaka', { exact: true }).selectOption(caseId);
    await expect(page.getByText('Vaka faturalamaya hazır değil', { exact: true })).toBeVisible();
    await expect(page.getByTestId('case-claim-form')).toHaveCount(0);
    await page.getByRole('button', { name: 'Yeniden dene', exact: true }).click();
    const form = page.getByTestId('case-claim-form');
    await expect(form.getByRole('button', { name: 'Taslağı oluştur', exact: true })).toBeDisabled();
    await form.getByLabel(/Talep edilen tutar/).fill('abc');
    await expect(form.getByLabel(/Talep edilen tutar/)).toHaveAttribute('aria-invalid', 'true');
    await form.getByLabel(/Miktar/).fill('0');
    await expect(form.getByLabel(/Miktar/)).toHaveAttribute('aria-invalid', 'true');
    await form.getByLabel(/Miktar/).fill('1');
    await form.getByLabel(/Talep edilen tutar/).fill('400,50');
    await page.screenshot({ path: '.impeccable/review/claim-source-desktop.png', fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    ).toBe(true);
    await page.screenshot({ path: '.impeccable/review/claim-source-mobile.png', fullPage: true });
    await form.getByRole('button', { name: 'Taslağı oluştur', exact: true }).click();
    await expect(
      page.getByText('İşlemin sonucu doğrulanamadı. Aynı istekle yeniden deneyin.', {
        exact: true,
      }),
    ).toBeVisible();
    await expect(form.getByLabel(/Talep edilen tutar/)).toBeDisabled();
    await expect(page.getByLabel('Faturalandırılacak vaka', { exact: true })).toBeDisabled();
    await form.getByRole('button', { name: 'Yeniden dene', exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/portal/claims/${claimId}$`));
    expect(attempts).toHaveLength(2);
    expect(attempts[1]).toEqual(attempts[0]);
    expect(attempts[0]!.key).toBeTruthy();
    expect(attempts[0]!.etag).toBe('"7"');
    expect(attempts[0]!.body).toEqual({
      lines: [{ serviceDefinitionId: serviceId, quantity: '1', lineAmount: '400.50' }],
    });
    expect(forbiddenReads).toBe(0);
  } finally {
    await actor.close();
  }
});
