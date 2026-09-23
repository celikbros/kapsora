import { createHash } from 'node:crypto';
import { expect, test, type Page } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import type { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];

/** Harmless antivirus test bytes, assembled in memory, never a real malicious file. */
export async function refuseUnsafeReport(input: {
  page: Page;
  provider: Actor;
  doctor: Actor;
  reportId: string;
  snapshot: (expected: number[]) => Promise<unknown>;
}) {
  const { page, provider, doctor, reportId, snapshot } = input;
  const path = `/api/v1/medical-reports/${reportId}`;
  const before = await provider.call<S<'MedicalReport'>>('GET', path);
  const balance = await snapshot([19, 1, 0]);
  // Split the signature so local source scanners do not mistake this test for a file upload.
  const bytes = Buffer.from(
    'X5O!P%@AP[4\\PZX54(P^)7CC)7}$' + 'EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*',
  );
  const filename = `antivirus-test-${reportId.slice(-8)}.txt`;
  const form = page.getByTestId('document-upload-form');
  await form
    .getByLabel(/Dosya seç/)
    .setInputFiles({ name: filename, mimeType: 'text/plain', buffer: bytes });
  await form.getByLabel(/Belge türü/).selectOption('MEDICAL_REPORT');
  await form.getByLabel(/Gizlilik/).selectOption('HEALTH');
  const reserved = page.waitForResponse(
    (r) => new URL(r.url()).pathname === '/api/v1/documents' && r.request().method() === 'POST',
  );
  await form.getByRole('button', { name: 'Belge yükle', exact: true }).click();
  const response = await reserved;
  expect(response.status()).toBe(201);
  const documentId = ((await response.json()) as S<'DocumentUpload'>).document.id;
  const row = page.getByTestId('documents-table').getByRole('row').filter({ hasText: filename });
  await expect(row.getByText('Zararlı yazılım bulundu', { exact: true })).toBeVisible({
    timeout: 45000,
  });
  await expect(row.getByRole('button', { name: 'İndir', exact: true })).toHaveCount(0);
  const infected = (await provider.call<S<'Document'>>('GET', `/api/v1/documents/${documentId}`))
    .data;
  expect(infected).toMatchObject({
    scanStatus: 'INFECTED',
    bucket: 'quarantine',
    downloadable: false,
    sha256: createHash('sha256').update(bytes).digest('hex'),
  });
  expect(
    infected.links.some(
      (link) => link.aggregateId === reportId && link.documentTypeCode === 'MEDICAL_REPORT',
    ),
  ).toBe(true);
  const download = await provider.call<{ code: string }>(
    'POST',
    `/api/v1/documents/${documentId}/download`,
    {},
    { expected: 409 },
  );
  expect(download.data.code).toBe('DOCUMENT_INFECTED');
  const current = await provider.call<S<'MedicalReport'>>('GET', path);
  const refused = await provider.call<{ code: string }>(
    'POST',
    path + '/submit',
    {},
    { etag: current.etag, expected: 422 },
  );
  expect(refused.data.code).toBe('MEDICAL_REPORT_DOCUMENT_REQUIRED');
  const after = await provider.call<S<'MedicalReport'>>('GET', path);
  expect(after).toEqual(current);
  expect(after.data.status).toBe('DRAFT');
  expect(after.data.services).toEqual(before.data.services);
  expect(
    (
      await doctor.call<S<'WorkItemPage'>>(
        'GET',
        `/api/v1/work-items?aggregateType=MEDICAL_REPORT&aggregateId=${reportId}`,
      )
    ).data.items,
  ).toEqual([]);
  expect(
    (await doctor.call<S<'MedicalReportUsagePage'>>('GET', path + '/usages')).data.items,
  ).toEqual([]);
  expect(await snapshot([19, 1, 0])).toEqual(balance);
  await expect(page.getByRole('heading', { name: 'Eksik belgeler', exact: true })).toBeVisible();
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 950 });
    await page.evaluate(() => window.scrollTo(0, 0));
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
    await page.screenshot({
      path: `.impeccable/review/unsafe-report/rejected-${width}.png`,
      fullPage: true,
    });
  }
  const tableViewport = page.getByTestId('documents-table').locator('..');
  await tableViewport.evaluate((element) => {
    element.scrollLeft = element.scrollWidth;
  });
  await row.getByText('Zararlı yazılım bulundu', { exact: true }).scrollIntoViewIfNeeded();
  await page.screenshot({ path: '.impeccable/review/unsafe-report/rejected-status-390.png' });
  await tableViewport.evaluate((element) => {
    element.scrollLeft = 0;
  });
  await page.setViewportSize({ width: 1440, height: 950 });
  await test.info().attach('unsafe-report-acceptance', {
    contentType: 'application/json',
    body: JSON.stringify({
      reportId,
      documentId,
      scanStatus: infected.scanStatus,
      downloadable: false,
      reportStatus: after.data.status,
      queueEmpty: true,
      usageEmpty: true,
      balanceUnchanged: true,
    }),
  });
  // Keep the incident linked as evidence. The caller next adds a clean PDF and completes
  // the same episode, proving a rejected upload does not permanently strand the report.
}
