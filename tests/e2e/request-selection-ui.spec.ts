import { expect, test } from '@playwright/test';

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base,
  'requires operator-started UI; only ambiguity responses are intercepted',
);

test('request plan selection: explicit choice, recheck, failed choice and responsive layout', async ({
  page,
}) => {
  let enrollmentId = '';
  let creates = 0;
  page.on('request', (request) => {
    if (
      new URL(request.url()).pathname === '/api/v1/service-requests' &&
      request.method() === 'POST'
    )
      creates += 1;
  });
  await page.route('**/api/v1/eligibility/checks', async (route) => {
    const input = route.request().postDataJSON();
    if (input.enrollmentId === '00000000-0000-4000-8000-000000000001') {
      await route.fulfill({
        status: 503,
        contentType: 'application/problem+json',
        body: JSON.stringify({
          status: 503,
          title: 'Uygunluk sorgusu tamamlanamadı',
          code: 'TEMPORARY_UNAVAILABLE',
          type: 'about:blank',
        }),
      });
      return;
    }
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    const result = await response.json();
    if (!input.enrollmentId) {
      enrollmentId = result.enrollmentId;
      result.enrollmentId = null;
      result.eligible = false;
      result.outcome = 'REVIEW_REQUIRED';
      result.explanations = [
        {
          code: 'ENROLLMENT_MULTIPLE',
          severity: 'WARNING',
          message: 'Birden fazla geçerli plan kaydı bulundu.',
        },
      ];
      result.enrollmentCandidates = [
        {
          enrollmentId,
          planCode: 'DEMO_HEALTH',
          planName: 'Demo sağlık planı',
          validFrom: '2026-01-01',
          validTo: '2027-01-01',
        },
        {
          enrollmentId: '00000000-0000-4000-8000-000000000001',
          planCode: 'DEMO_EXTRA',
          planName: 'Demo ek sağlık planı',
          validFrom: '2026-01-01',
          validTo: '2027-01-01',
        },
      ];
    } else expect(input.enrollmentId).toBe(enrollmentId);
    await route.fulfill({ response, json: result });
  });
  await page.goto(base + '/portal/');
  await page.getByLabel(/Kullanıcı adı/).fill('provider.a');
  await page
    .getByLabel(/^Parola/)
    .fill(process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora');
  await page.getByRole('button', { name: 'Giriş yap', exact: true }).click();
  await page.getByLabel('Ada göre ara').fill('Melis');
  await page
    .getByTestId('member-candidates')
    .getByRole('button', { name: /Melis Üye/ })
    .click();
  await page
    .getByRole('combobox', { name: /^Hizmet/ })
    .selectOption({ label: 'Fizyoterapi seansı' });
  const choice = page.getByRole('combobox', { name: /^Plan kaydı/ });
  await expect(choice).toBeVisible();
  await expect(
    page.getByTestId('eligibility-pane').getByText('İnceleme gerekli', { exact: true }),
  ).toBeVisible();
  await expect(page.getByRole('button', { name: 'Gönder', exact: true })).toHaveCount(0);
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
    await page.screenshot({
      path: `.impeccable/review/request-selection-${width}.png`,
      fullPage: true,
    });
  }
  await choice.selectOption(enrollmentId);
  await expect(page.getByRole('button', { name: 'Gönder', exact: true })).toBeVisible();
  await expect(choice).toHaveValue(enrollmentId);
  await choice.selectOption('00000000-0000-4000-8000-000000000001');
  await expect(page.getByRole('alert')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Gönder', exact: true })).toHaveCount(0);
  await choice.selectOption(enrollmentId);
  await expect(page.getByRole('button', { name: 'Gönder', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Çıkış yap', exact: true }).click();
  expect(creates).toBe(0);
});
