import { expect, test } from '@playwright/test';

const REAL = process.env['E2E_REAL_API'] === '1';
const EXISTING = process.env['E2E_EXISTING_UI_URL'];
const PASSWORD = process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora';

test.skip(!REAL || !EXISTING, 'requires the seeded API and operator-started single-door UI');

test('real health request: clinical provider eligibility, medical decision and provider follow-up', async ({
  page,
}) => {
  test.setTimeout(60_000);
  const portal = new URL('/portal/', EXISTING!).href;
  await page.goto(portal);
  await page.getByLabel(/Kullanıcı adı/).fill('provider.a');
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  const catalog = page.waitForResponse(
    (response) => new URL(response.url()).pathname === '/api/v1/service-definitions',
  );
  await page.getByRole('button', { name: 'Giriş yap', exact: true }).click();
  expect((await catalog).status()).toBe(200);
  await expect(page.getByRole('heading', { name: 'Yeni talep', exact: true })).toBeVisible();
  await page.getByLabel('Ada göre ara').fill('Melis');
  await page
    .getByTestId('member-candidates')
    .getByRole('button', { name: /Melis Üye/ })
    .click();

  const eligibilityResponse = () =>
    page.waitForResponse(
      (response) => new URL(response.url()).pathname === '/api/v1/eligibility/checks',
    );
  const initialCheck = eligibilityResponse();
  await page
    .getByRole('combobox', { name: /^Hizmet/ })
    .selectOption({ label: 'Fizyoterapi seansı' });
  const initial = await initialCheck;
  expect(initial.status()).toBe(200);
  const initialResult = await initial.json();
  expect(initialResult.eligible).toBe(true);
  expect(initialResult.enrollmentId).toMatch(/^[0-9a-f-]{36}$/);
  const pane = page.getByTestId('eligibility-pane');
  await expect(pane.getByText('Uygun', { exact: true })).toBeVisible();

  const tooMuchCheck = eligibilityResponse();
  await page.getByLabel(/^Miktar/).fill('100000');
  const tooMuch = await tooMuchCheck;
  expect(tooMuch.status()).toBe(200);
  expect((await tooMuch.json()).eligible).toBe(false);
  await expect(pane.getByText('Uygun değil', { exact: true })).toBeVisible();
  // Restore the original query; the client may reuse its still-fresh cached answer.
  await page.getByLabel(/^Miktar/).fill('1');
  await expect(pane.getByText('Uygun', { exact: true })).toBeVisible();

  const submitted = page.waitForResponse(
    (response) =>
      /\/api\/v1\/service-requests\/[0-9a-f-]+\/submit$/.test(new URL(response.url()).pathname) &&
      response.request().method() === 'POST',
  );
  await page.getByRole('button', { name: 'Gönder', exact: true }).click();
  const response = await submitted;
  expect(response.status()).toBe(200);
  const request = await response.json();
  expect(request.status).toBe('PENDING_REVIEW');
  expect(request.enrollmentId).toBe(initialResult.enrollmentId);
  await expect(page).toHaveURL(new RegExp(`/portal/requests/${request.id}$`));
  await expect(page.getByRole('heading', { name: request.reference, exact: true })).toBeVisible();
  await test.info().attach('submitted-request', {
    body: JSON.stringify({ reference: request.reference, status: request.status }),
    contentType: 'application/json',
  });
  await page.screenshot({ path: '.impeccable/review/health-request-real.png', fullPage: true });
  await page.getByRole('button', { name: 'Çıkış yap', exact: true }).click();
  await expect(page.getByLabel(/Kullanıcı adı/)).toBeVisible();

  await page.goto(new URL(`/requests/${request.id}`, EXISTING!).href);
  await page.getByLabel(/Kullanıcı adı/).fill('doctor.a');
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap', exact: true }).click();
  await expect(page.getByRole('heading', { name: request.reference, exact: true })).toBeVisible();
  await expect(page.getByTestId('request-status')).toHaveText('İncelemede');
  await page.getByRole('button', { name: 'Onayla', exact: true }).click();
  const approval = page.waitForResponse(
    (result) => new URL(result.url()).pathname === `/api/v1/service-requests/${request.id}/approve`,
  );
  await page
    .getByRole('dialog', { name: 'Onayla', exact: true })
    .getByRole('button', { name: 'Onayla', exact: true })
    .click();
  const approved = await approval;
  expect(approved.status()).toBe(200);
  expect((await approved.json()).status).toBe('APPROVED');
  await expect(page.getByTestId('request-status')).toHaveText('Onaylandı');
  await page.getByRole('button', { name: 'Kullanıcı menüsü', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Çıkış yap', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Oturum kapatıldı', exact: true })).toBeVisible();

  await page.goto(new URL(`/portal/requests/${request.id}`, EXISTING!).href);
  await page.getByLabel(/Kullanıcı adı/).fill('provider.a');
  await page.getByLabel(/^Parola/).fill(PASSWORD);
  await page.getByRole('button', { name: 'Giriş yap', exact: true }).click();
  await expect(page.getByRole('heading', { name: request.reference, exact: true })).toBeVisible();
  await expect(page.getByRole('definition').filter({ hasText: /^Onaylandı$/ })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Vaka aç', exact: true })).toBeVisible();
  await page.screenshot({ path: '.impeccable/review/health-approved-real.png', fullPage: true });
  await page.getByRole('button', { name: 'Çıkış yap', exact: true }).click();
  await expect(page.getByLabel(/Kullanıcı adı/)).toBeVisible();
});
