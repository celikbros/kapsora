import { expect, test, type Page } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import type { Actor } from './real-api-actor';
import { syntheticPDF } from './synthetic-pdf';

type S<K extends keyof components['schemas']> = components['schemas'][K];

/** A fresh episode's decided claim keeps its original evidence when the report is corrected. */
export async function correctApprovedReport(input: {
  base: string;
  reportId: string;
  claimId: string;
  provider: Actor;
  doctor: Actor;
  billing: Actor;
  providerPage: Page;
  doctorPage: Page;
  snapshot: (expected: number[]) => Promise<unknown>;
}) {
  const { base, reportId, claimId, provider, doctor, billing, providerPage, doctorPage, snapshot } =
    input;
  const oldPath = `/api/v1/medical-reports/${reportId}`;
  const claimPath = `/api/v1/claims/${claimId}`;
  const original = await doctor.call<S<'MedicalReport'>>('GET', oldPath);
  expect(original.data.status).toBe('APPROVED');
  const originalClaim = await doctor.call<S<'Claim'>>('GET', claimPath);
  const originalReadiness = await billing.call<S<'ClaimInvoiceReadiness'>>(
    'GET',
    claimPath + '/invoice-readiness',
  );
  const originalUsage = await doctor.call<S<'MedicalReportUsagePage'>>('GET', oldPath + '/usages');
  const originalBalance = await snapshot([19, 0, 1]);
  let correctedId: string | undefined;
  let workId: string | undefined;
  try {
    for (const [method, suffix, body] of [
      [
        'PATCH',
        '',
        {
          reportType: original.data.reportType,
          issuedAt: original.data.issuedAt,
          validFrom: original.data.validFrom,
          validTo: original.data.validTo,
          clinicalSummary: 'Refused mutation',
        },
      ],
      ['PUT', '/services', { items: [] }],
    ] as const) {
      const refused = await provider.call<{ code: string }>(method, oldPath + suffix, body, {
        etag: original.etag,
        expected: 409,
      });
      expect(refused.data.code).toBe('MEDICAL_REPORT_IMMUTABLE');
    }
    expect(await doctor.call<S<'MedicalReport'>>('GET', oldPath)).toEqual(original);
    await providerPage.goto(base + `/portal/reports/${reportId}`);
    const created = providerPage.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === '/api/v1/medical-reports' && r.request().method() === 'POST',
    );
    await providerPage.getByRole('button', { name: 'Düzeltme oluştur', exact: true }).click();
    const response = await created;
    expect(response.status()).toBe(201);
    const correction = (await response.json()) as S<'MedicalReport'>;
    correctedId = correction.id;
    const path = `/api/v1/medical-reports/${correction.id}`;
    expect(correction).toMatchObject({
      status: 'DRAFT',
      versionNo: original.data.versionNo + 1,
      rootReportId: original.data.rootReportId,
      reference: original.data.reference,
      supersedesReportId: reportId,
    });
    expect(correction.documents).toEqual([]);
    expect(correction.services).toHaveLength(original.data.services.length);
    expect(correction.services[0]!.id).not.toBe(original.data.services[0]!.id);
    expect(correction.services[0]!.serviceDefinitionId).toBe(
      original.data.services[0]!.serviceDefinitionId,
    );
    const createBody: unknown = response.request().postDataJSON();
    expect(
      (
        await provider.call<S<'MedicalReport'>>('POST', '/api/v1/medical-reports', createBody, {
          expected: 201,
          key: response.request().headers()['idempotency-key']!,
        })
      ).data,
    ).toEqual(correction);
    await provider.call('POST', '/api/v1/medical-reports', createBody, { expected: 422 });
    const missing = await provider.call<{ code: string }>(
      'POST',
      path + '/submit',
      {},
      { etag: response.headers()['etag']!, expected: 422 },
    );
    expect(missing.data.code).toBe('MEDICAL_REPORT_DOCUMENT_REQUIRED');
    expect(await doctor.call<S<'MedicalReport'>>('GET', oldPath)).toEqual(original);

    await expect(providerPage.getByTestId('report-status')).toHaveText('Taslak');
    const summary = `Synthetic corrected report ${correction.id}`;
    const header = providerPage.getByTestId('report-header-form');
    await header.locator('[name="clinicalSummary"]').fill(summary);
    const capture = async (state: string) => {
      for (const width of [1440, 390]) {
        await providerPage.setViewportSize({ width, height: 950 });
        expect(
          await providerPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
        ).toBe(true);
        await providerPage.screenshot({
          path: `.impeccable/review/report-correction/${state}-${width}.png`,
          fullPage: true,
        });
      }
      await providerPage.setViewportSize({ width: 1440, height: 950 });
    };
    await capture('draft');
    let releaseRefresh = () => {};
    const refreshGate = new Promise<void>((resolve) => {
      releaseRefresh = resolve;
    });
    let seenRefresh = () => {};
    const refreshSeen = new Promise<void>((resolve) => {
      seenRefresh = resolve;
    });
    await providerPage.route(base + path, async (route) => {
      if (route.request().method() === 'GET') {
        seenRefresh();
        await refreshGate;
      }
      await route.continue();
    });
    const saved = providerPage.waitForResponse(
      (r) => new URL(r.url()).pathname === path && r.request().method() === 'PATCH',
    );
    await header.getByRole('button', { name: 'Kaydet', exact: true }).click();
    try {
      expect((await saved).status()).toBe(200);
      await refreshSeen;
      await expect(header.locator('[name="clinicalSummary"]')).toBeDisabled();
      await expect(providerPage.locator('[name="services.0.coveredQuantity"]')).toBeDisabled();
      await expect(
        providerPage.getByRole('button', { name: 'Gönder', exact: true }),
      ).toBeDisabled();
      await capture('saving');
    } finally {
      releaseRefresh();
    }
    await providerPage.locator('[name="services.0.coveredQuantity"]').fill('2');
    const servicesSaved = providerPage.waitForResponse(
      (r) => new URL(r.url()).pathname === path + '/services' && r.request().method() === 'PUT',
    );
    await providerPage.getByRole('button', { name: 'Hizmetleri kaydet', exact: true }).click();
    const servicesResponse = await servicesSaved;
    expect(servicesResponse.status()).toBe(200);
    expect(
      Number(((await servicesResponse.json()) as S<'MedicalReport'>).services[0]!.coveredQuantity),
    ).toBe(2);
    await expect(providerPage.locator('[name="services.0.coveredQuantity"]')).toBeEnabled();
    await providerPage.unroute(base + path);
    const upload = providerPage.getByTestId('document-upload-form');
    const filename = `report-correction-${correction.id.slice(-8)}.pdf`;
    await upload
      .getByLabel(/Dosya seç/)
      .setInputFiles({ name: filename, mimeType: 'application/pdf', buffer: syntheticPDF() });
    await upload.getByLabel(/Belge türü/).selectOption('MEDICAL_REPORT');
    await upload.getByLabel(/Gizlilik/).selectOption('HEALTH');
    const reserved = providerPage.waitForResponse(
      (r) => new URL(r.url()).pathname === '/api/v1/documents' && r.request().method() === 'POST',
    );
    await upload.getByRole('button', { name: 'Belge yükle', exact: true }).click();
    expect((await reserved).status()).toBe(201);
    await expect(
      providerPage
        .getByTestId('documents-table')
        .getByRole('row')
        .filter({ hasText: filename })
        .getByText('Temiz', { exact: true }),
    ).toBeVisible({ timeout: 45000 });
    const submitted = providerPage.waitForResponse(
      (r) => new URL(r.url()).pathname === path + '/submit' && r.request().method() === 'POST',
    );
    await providerPage.getByRole('button', { name: 'Gönder', exact: true }).click();
    expect((await submitted).status()).toBe(200);
    const items = (
      await doctor.call<S<'WorkItemPage'>>(
        'GET',
        `/api/v1/work-items?aggregateType=MEDICAL_REPORT&aggregateId=${correction.id}`,
      )
    ).data.items;
    expect(items).toHaveLength(1);
    workId = items[0]!.id;
    await doctor.call(
      'POST',
      `/api/v1/work-items/${workId}/claim`,
      {},
      { etag: `"${items[0]!.rowVersion}"` },
    );
    await doctorPage.goto(base + `/medical-reports/${correction.id}`);
    await expect(doctorPage.getByTestId('report-summary')).toHaveText(summary);
    await doctorPage
      .getByTestId('report-commands')
      .getByRole('button', { name: 'Onayla', exact: true })
      .click();
    const dialog = doctorPage.getByRole('dialog');
    await dialog.locator('[name="reviewComment"]').fill('Synthetic correction reviewed');
    const approved = doctorPage.waitForResponse(
      (r) => new URL(r.url()).pathname === path + '/approve' && r.request().method() === 'POST',
    );
    await dialog.getByRole('button', { name: 'Onayla', exact: true }).click();
    const approval = await approved;
    expect(approval.status()).toBe(200);
    const approvedReport = (await approval.json()) as S<'MedicalReport'>;
    expect(approvedReport.status).toBe('APPROVED');
    expect(Number(approvedReport.services[0]!.coveredQuantity)).toBe(2);
    expect(approvedReport.documents).toHaveLength(1);
    expect(approvedReport.documents[0]!.objectId).not.toBe(original.data.documents[0]!.objectId);
    const old = (await doctor.call<S<'MedicalReport'>>('GET', oldPath)).data;
    expect(old).toEqual({ ...original.data, status: 'SUPERSEDED', rowVersion: old.rowVersion });
    expect(old.rowVersion).toBeGreaterThan(original.data.rowVersion);
    const chain = (
      await doctor.call<S<'MedicalReportPage'>>(
        'GET',
        `/api/v1/medical-reports?rootReportId=${original.data.rootReportId}`,
      )
    ).data.items;
    expect(chain).toHaveLength(2);
    expect(chain.filter((r) => r.status === 'APPROVED').map((r) => r.id)).toEqual([correction.id]);
    expect(await doctor.call<S<'MedicalReportUsagePage'>>('GET', oldPath + '/usages')).toEqual(
      originalUsage,
    );
    expect(
      (await doctor.call<S<'MedicalReportUsagePage'>>('GET', path + '/usages')).data.items,
    ).toEqual([]);
    expect(await doctor.call<S<'Claim'>>('GET', claimPath)).toEqual(originalClaim);
    expect(
      await billing.call<S<'ClaimInvoiceReadiness'>>('GET', claimPath + '/invoice-readiness'),
    ).toEqual(originalReadiness);
    expect(await snapshot([19, 0, 1])).toEqual(originalBalance);
    expect(
      (
        await doctor.call<S<'MedicalReport'>>(
          'POST',
          path + '/approve',
          approval.request().postDataJSON(),
          {
            etag: approval.request().headers()['if-match']!,
            key: approval.request().headers()['idempotency-key']!,
          },
        )
      ).data,
    ).toEqual(approvedReport);
    expect(await doctor.call<S<'MedicalReport'>>('GET', oldPath)).toEqual({
      data: old,
      etag: `"${old.rowVersion}"`,
    });
    await providerPage.goto(base + `/portal/reports/${reportId}`);
    await expect(
      providerPage.getByRole('button', { name: 'Düzeltme oluştur', exact: true }),
    ).toHaveCount(0);
    await expect(providerPage.getByTestId('report-header-form')).toHaveCount(0);
    await expect(
      providerPage
        .getByTestId('report-versions')
        .locator(`a[href="/portal/reports/${correction.id}"]`),
    ).toBeVisible();
    await providerPage
      .getByTestId('report-versions')
      .locator(`a[href="/portal/reports/${correction.id}"]`)
      .click();
    await expect(providerPage.getByTestId('report-status')).toHaveText('Onaylandı');
    await test.info().attach('report-correction-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        originalReportId: reportId,
        correctedReportId: correction.id,
        claimId,
        oldStatus: 'SUPERSEDED',
        newStatus: 'APPROVED',
        originalUsageCount: originalUsage.data.items.length,
        claimUnchanged: true,
        balanceUnchanged: true,
      }),
    });
  } finally {
    if (correctedId) {
      const path = `/api/v1/medical-reports/${correctedId}`;
      const current = await provider.call<S<'MedicalReport'>>('GET', path);
      if (['DRAFT', 'SUBMITTED'].includes(current.data.status))
        await provider.call('POST', path + '/cancel', {}, { etag: current.etag });
      else if (current.data.status === 'UNDER_REVIEW')
        await doctor.call(
          'POST',
          path + '/reject',
          { rejectReasonCode: 'PC03_TEST_CLEANUP' },
          { etag: current.etag },
        );
      await test.info().attach('report-correction-fixture', {
        contentType: 'application/json',
        body: JSON.stringify({ correctedReportId: correctedId, workId }),
      });
    }
    if (workId) {
      const path = `/api/v1/work-items/${workId}`;
      const item = await doctor.call<S<'WorkItem'>>('GET', path);
      if (item.data.status === 'CLAIMED')
        await doctor.call(
          'POST',
          path + '/complete',
          { outcomeCode: 'PC03_TEST_CLEANUP' },
          { etag: item.etag },
        );
    }
  }
}
