import { mkdir } from 'node:fs/promises';
import { expect, test, type Page } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';
import { syntheticPDF } from './synthetic-pdf';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const claimId = process.env['E2E_HEALTH_DECISIONS_CLAIM'] ?? '';
const firstInvoiceId = process.env['E2E_HEALTH_DECISIONS_INVOICE'] ?? '';
const firstDocumentId = process.env['E2E_HEALTH_DECISIONS_DOCUMENT'] ?? '';
const firstBatchId = process.env['E2E_HEALTH_DECISIONS_BATCH'] ?? '';
const correctedInvoiceId = process.env['E2E_HEALTH_DECISIONS_CORRECTION'] ?? '';
const correctedDocumentId = process.env['E2E_HEALTH_DECISIONS_CORRECTION_DOCUMENT'] ?? '';
const cutBatchId = process.env['E2E_HEALTH_DECISIONS_CUT_BATCH'] ?? '';

test.use({ trace: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base,
  'requires operator-started local demo and a separate untouched invoice-ready HEALTH claim',
);

function micros(value: string): bigint {
  expect(value).toMatch(/^\d+(?:\.\d{1,6})?$/);
  const [whole, fraction = ''] = value.split('.');
  return BigInt(whole!) * 1_000_000n + BigInt(fraction.padEnd(6, '0'));
}

function exact(value: string, wanted: string) {
  expect(micros(value)).toBe(micros(wanted));
}

async function stage(ids: Record<string, string>, name: string) {
  await test.info().attach(`pc05-decisions-${name}-ids`, {
    contentType: 'application/json',
    body: JSON.stringify({ ...ids, stage: name }),
  });
}

async function createInvoice(
  page: Page,
  claim: S<'Claim'>,
  ids: Record<string, string>,
  amounts = { net: '666.67', tax: '133.33', payable: '800' },
) {
  const invoiceNumber = `PC05-DEC-${claim.id}`;
  await page.goto(base + `/portal/billing/invoices/new?claims=${claim.id}&currency=TRY`);
  const form = page.getByTestId('invoice-header');
  await form.locator('[name="domainCode"]').selectOption('HEALTH');
  await form.locator('[name="invoiceNumber"]').fill(invoiceNumber);
  await form.locator('[name="lineExtensionAmount"]').fill(amounts.net);
  await form.locator('[name="taxAmount"]').fill(amounts.tax);
  await form.locator('[name="payableAmount"]').fill(amounts.payable);
  await form.locator('[name="vatRate"]').fill('20');
  const created = page.waitForResponse(
    (r) => new URL(r.url()).pathname === '/api/v1/invoices' && r.request().method() === 'POST',
  );
  await page.getByRole('button', { name: 'Kaydet', exact: true }).click();
  const response = await created;
  expect(response.status()).toBe(201);
  const id = ((await response.json()) as S<'Invoice'>).id;
  ids['invoiceId'] = id;
  ids['invoiceNumber'] = invoiceNumber;
  await stage(ids, 'first-invoice-created');
  return id;
}

async function prepareInvoice(
  page: Page,
  billing: Actor,
  id: string,
  claim: S<'Claim'>,
  ids: Record<string, string>,
  resumeDocumentId: string,
  prefix: string,
  expectedAmount = '800',
) {
  const path = `/api/v1/invoices/${id}`;
  let invoice = (await billing.call<S<'Invoice'>>('GET', path)).data;
  expect(invoice.providerOrganizationId).toBe(claim.providerOrganizationId);
  expect(invoice.domainCode).toBe('HEALTH');
  expect(invoice.currencyCode).toBe('TRY');
  exact(invoice.payableAmount, expectedAmount);
  if (invoice.status !== 'DRAFT') return invoice;
  await page.goto(base + `/portal/billing/invoices/${id}?claims=${claim.id}`);
  if (invoice.allocations.length === 0) {
    const row = page.getByTestId('allocation-row').filter({ hasText: claim.reference });
    await row.getByRole('textbox').fill(expectedAmount);
    await page.getByRole('button', { name: 'Dağıtımı kaydet', exact: true }).click();
    await expect
      .poll(async () => (await billing.call<S<'Invoice'>>('GET', path)).data.allocations.length)
      .toBe(1);
  }
  invoice = (await billing.call<S<'Invoice'>>('GET', path)).data;
  expect(invoice.allocations.map((a) => a.claimId)).toEqual([claim.id]);
  exact(invoice.allocationTotal, expectedAmount);
  exact(invoice.allocationDifference, '0');
  if (!invoice.documentId) {
    let documentId = resumeDocumentId;
    if (!documentId) {
      const upload = page.getByTestId('document-upload-form');
      await expect(upload).toBeVisible();
      await upload.getByLabel(/Dosya seç/).setInputFiles({
        name: `pc05-decisions-${id.slice(0, 8)}.pdf`,
        mimeType: 'application/pdf',
        buffer: syntheticPDF(),
      });
      await upload.getByLabel(/Belge türü/).selectOption('INVOICE');
      const reserved = page.waitForResponse(
        (r) => new URL(r.url()).pathname === '/api/v1/documents' && r.request().method() === 'POST',
      );
      await upload.getByRole('button', { name: 'Belge yükle', exact: true }).click();
      const response = await reserved;
      expect(response.status()).toBe(201);
      documentId = ((await response.json()) as { document: S<'Document'> }).document.id;
      ids[`${prefix}DocumentId`] = documentId;
      await stage(ids, `${prefix}-document-reserved`);
    }
    ids[`${prefix}DocumentId`] = documentId;
    await expect
      .poll(
        async () =>
          (await billing.call<S<'Document'>>('GET', `/api/v1/documents/${documentId}`)).data
            .scanStatus,
        { timeout: 45_000 },
      )
      .toBe('CLEAN');
    await page.reload();
    const attached = page.waitForResponse(
      (r) => new URL(r.url()).pathname === path && r.request().method() === 'PATCH',
    );
    await page.getByRole('button', { name: 'Kaydet', exact: true }).click();
    expect((await attached).status()).toBe(200);
    await expect
      .poll(async () => (await billing.call<S<'Invoice'>>('GET', path)).data.documentId)
      .toBe(documentId);
    await stage(ids, `${prefix}-clean-document-attached`);
  }
  await page.reload();
  await expect(page.getByTestId('invoice-submit')).toBeVisible();
  await mkdir('.impeccable/review/health-billing', { recursive: true });
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
      .toBe(true);
    await page.screenshot({
      path: `.impeccable/review/health-billing/${prefix}-invoice-${width}.png`,
      fullPage: true,
    });
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  const submitted = page.waitForResponse(
    (r) => new URL(r.url()).pathname === path + '/submit' && r.request().method() === 'POST',
  );
  await page.getByTestId('invoice-submit').click();
  expect((await submitted).status()).toBe(200);
  await stage(ids, `${prefix}-invoice-submitted`);
  return (await billing.call<S<'Invoice'>>('GET', path)).data;
}

async function prepareBatch(
  page: Page,
  billing: Actor,
  invoice: S<'Invoice'>,
  resumeId: string,
  ids: Record<string, string>,
  prefix: string,
) {
  let batchId = resumeId || invoice.batchId || '';
  if (!batchId) {
    await page.goto(base + '/portal/billing/batches/new');
    const form = page.getByTestId('batch-form');
    await form.locator('[name="periodFrom"]').fill(invoice.invoiceDate);
    await form.locator('[name="periodTo"]').fill(invoice.invoiceDate);
    const created = page.waitForResponse(
      (r) => new URL(r.url()).pathname === '/api/v1/batches' && r.request().method() === 'POST',
    );
    await form.getByRole('button', { name: 'Kaydet', exact: true }).click();
    const response = await created;
    expect(response.status()).toBe(201);
    batchId = ((await response.json()) as S<'Batch'>).id;
    ids[`${prefix}BatchId`] = batchId;
    await stage(ids, `${prefix}-batch-created`);
  }
  ids[`${prefix}BatchId`] = batchId;
  const path = `/api/v1/batches/${batchId}`;
  let batch = (await billing.call<S<'Batch'>>('GET', path)).data;
  expect(batch.providerOrganizationId).toBe(invoice.providerOrganizationId);
  expect(batch.domainCode).toBe('HEALTH');
  expect(batch.currencyCode).toBe('TRY');
  if (batch.status === 'DRAFT') {
    await page.goto(base + `/portal/billing/batches/${batchId}`);
    if (batch.invoices.length === 0) {
      await page.locator(`#inv-${invoice.id}`).check();
      const saved = page.waitForResponse(
        (r) => new URL(r.url()).pathname === path + '/invoices' && r.request().method() === 'PUT',
      );
      await page.getByRole('button', { name: 'Faturaları kaydet', exact: true }).click();
      expect((await saved).status()).toBe(200);
    }
    batch = (await billing.call<S<'Batch'>>('GET', path)).data;
    expect(batch.invoices.map((item) => item.invoiceId)).toEqual([invoice.id]);
    const submitted = page.waitForResponse(
      (r) => new URL(r.url()).pathname === path + '/submit' && r.request().method() === 'POST',
    );
    await page.getByTestId('batch-submit').click();
    expect((await submitted).status()).toBe(200);
    await stage(ids, `${prefix}-batch-submitted`);
  }
  batch = (await billing.call<S<'Batch'>>('GET', path)).data;
  expect(batch.invoices.map((item) => item.invoiceId)).toEqual([invoice.id]);
  exact(batch.submittedTotal, invoice.payableAmount);
  return batch;
}

async function reviewInUI(
  page: Page,
  reviewer: Actor,
  batchId: string,
  invoiceNumber: string,
  decision: 'RETURN' | 'CUT' | 'REJECT',
  reasonCode: string,
  ids: Record<string, string>,
) {
  const path = `/api/v1/batches/${batchId}`;
  let batch = (await reviewer.call<S<'Batch'>>('GET', path)).data;
  if (batch.status === 'SUBMITTED' || batch.status === 'UNDER_REVIEW') {
    await page.goto(base + `/billing/batches/${batchId}`);
    if (!batch.invoices[0]?.decision) {
      await page
        .getByTestId('review-row')
        .filter({ hasText: invoiceNumber })
        .getByRole('button', { name: 'Fatura kararı', exact: true })
        .click();
      const form = page.getByTestId('decision-form');
      await form.locator('[name="decision"]').selectOption(decision);
      if (decision === 'CUT') await form.locator('[name="approvedAmount"]').fill('600');
      if (decision === 'CUT') {
        await form.locator('[name="reasonCode"]').selectOption(reasonCode);
      } else {
        await form.locator('[name="reasonCode"]').fill(reasonCode);
      }
      const saved = page.waitForResponse(
        (r) => new URL(r.url()).pathname.endsWith('/review') && r.request().method() === 'POST',
      );
      await form.getByTestId('save-decision').click();
      expect((await saved).status()).toBe(200);
      await stage(ids, `${decision.toLowerCase()}-review-saved`);
    }
    batch = (await reviewer.call<S<'Batch'>>('GET', path)).data;
    expect(batch.invoices[0]?.decision).toBe(decision);
    expect(batch.invoices[0]?.reasonCode).toBe(reasonCode);
    expect(batch.submittedBy).not.toBe(batch.invoices[0]?.decidedBy);
    await page.getByTestId('decide-batch').click();
    await expect
      .poll(async () => (await reviewer.call<S<'Batch'>>('GET', path)).data.status)
      .toMatch(/^(DECIDED|SETTLING|CLOSED)$/);
    await stage(ids, `${decision.toLowerCase()}-batch-decided`);
  }
  batch = (await reviewer.call<S<'Batch'>>('GET', path)).data;
  expect(batch.invoices[0]?.decision).toBe(decision);
  expect(batch.invoices[0]?.reasonCode).toBe(reasonCode);
  expect(batch.submittedBy).not.toBe(batch.decidedBy);
  return batch;
}

test('returned health invoice is corrected, then cut to an exact 600 TRY settlement', async ({
  browser,
}) => {
  test.skip(!claimId, 'requires explicit untouched/resume 800 TRY claim');
  test.setTimeout(300_000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  if (process.env['KAPSORA_API_URL'])
    expect(new URL(process.env['KAPSORA_API_URL']).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const providerPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const reviewerPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  providerPage.setDefaultTimeout(15_000);
  reviewerPage.setDefaultTimeout(15_000);
  const billing = new Actor(providerPage.request, 'provider', true);
  const reviewer = new Actor(reviewerPage.request, 'backoffice', true);
  const ids: Record<string, string> = { claimId };
  try {
    await billing.login('billing.a');
    await reviewer.login('financial.reviewer');
    const claimPath = `/api/v1/claims/${claimId}`;
    const claim = (await billing.call<S<'Claim'>>('GET', claimPath)).data;
    expect(claim.domainCode).toBe('HEALTH');
    expect(claim.sourceType).toBe('HEALTH_CASE');
    ids['providerOrganizationId'] = claim.providerOrganizationId;
    if (!firstInvoiceId) {
      expect(claim.status).toBe('APPROVED');
      const readiness = (
        await billing.call<S<'ClaimInvoiceReadiness'>>('GET', claimPath + '/invoice-readiness')
      ).data;
      expect(readiness.ready).toBe(true);
      expect(readiness.blockers).toEqual([]);
      exact(readiness.approvedTotal, '800');
      exact(readiness.payerTotal, '800');
      exact(readiness.memberTotal, '0');
      const listed = (
        await billing.call<S<'InvoicePage'>>(
          'GET',
          `/api/v1/invoices?providerOrganizationId=${claim.providerOrganizationId}&fiscalYear=${new Date().getFullYear()}&limit=100`,
        )
      ).data;
      expect(listed.nextCursor, 'preflight requires an exhaustive invoice page').toBeNull();
      expect(
        listed.items.some((i) => i.invoiceNumber === `PC05-DEC-${claimId}`),
        'existing draft requires E2E_HEALTH_DECISIONS_INVOICE from stage attachment',
      ).toBe(false);
    }
    await stage(ids, 'claim-verified');

    const originalId = firstInvoiceId || (await createInvoice(providerPage, claim, ids));
    ids['invoiceId'] = originalId;
    let original = await prepareInvoice(
      providerPage,
      billing,
      originalId,
      claim,
      ids,
      firstDocumentId,
      'first',
    );
    const returnedBatch = await prepareBatch(
      providerPage,
      billing,
      original,
      firstBatchId,
      ids,
      'return',
    );
    const firstBatchPath = `/api/v1/batches/${returnedBatch.id}`;
    if (returnedBatch.status === 'SUBMITTED' && !returnedBatch.invoices[0]?.decision) {
      const attempt = await reviewer.call<{ code: string }>(
        'POST',
        `${firstBatchPath}/invoices/${originalId}/review`,
        { decision: 'RETURN', reasonCode: '' },
        { etag: (await reviewer.call<S<'Batch'>>('GET', firstBatchPath)).etag, expected: 422 },
      );
      expect(attempt.data.code).toBe('VALIDATION_FAILED');
      expect((await reviewer.call<S<'Batch'>>('GET', firstBatchPath)).data.status).toBe(
        'SUBMITTED',
      );
    }
    const decidedReturn = await reviewInUI(
      reviewerPage,
      reviewer,
      returnedBatch.id,
      original.invoiceNumber,
      'RETURN',
      'DOCUMENT_MISSING',
      ids,
    );
    exact(decidedReturn.returnedTotal, '800');
    exact(decidedReturn.approvedTotal, '0');
    original = (await billing.call<S<'Invoice'>>('GET', `/api/v1/invoices/${originalId}`)).data;
    expect(correctedInvoiceId ? ['RETURNED', 'CANCELLED'] : ['RETURNED']).toContain(
      original.status,
    );
    expect(original.allocations.map((a) => a.claimId)).toEqual([claimId]);
    if (!correctedInvoiceId) {
      expect((await billing.call<S<'Claim'>>('GET', claimPath)).data.status).toBe('APPROVED');
      const released = (
        await billing.call<S<'ClaimInvoiceReadiness'>>('GET', claimPath + '/invoice-readiness')
      ).data;
      expect(released.ready).toBe(true);
      exact(released.approvedTotal, '800');
      await stage(ids, 'claim-released-and-old-invoice-preserved');
    }

    let correctionId = correctedInvoiceId;
    if (!correctionId) {
      await providerPage.goto(base + `/portal/billing/invoices/${originalId}`);
      const created = providerPage.waitForResponse(
        (r) => new URL(r.url()).pathname === '/api/v1/invoices' && r.request().method() === 'POST',
      );
      await providerPage.getByTestId('invoice-correct').click();
      const response = await created;
      expect(response.status()).toBe(201);
      correctionId = ((await response.json()) as S<'Invoice'>).id;
      ids['correctionInvoiceId'] = correctionId;
      await stage(ids, 'correction-created');
    }
    ids['correctionInvoiceId'] = correctionId;
    let correction = (await billing.call<S<'Invoice'>>('GET', `/api/v1/invoices/${correctionId}`))
      .data;
    expect(correction.supersedesInvoiceId).toBe(originalId);
    expect(correction.invoiceNumber).toBe(original.invoiceNumber);
    expect(correction.domainCode).toBe('HEALTH');
    correction = await prepareInvoice(
      providerPage,
      billing,
      correctionId,
      claim,
      ids,
      correctedDocumentId,
      'correction',
    );
    expect(
      (await billing.call<S<'Invoice'>>('GET', `/api/v1/invoices/${originalId}`)).data.status,
    ).toBe('CANCELLED');
    await providerPage.goto(base + `/portal/billing/invoices/${correctionId}`);
    await expect(providerPage.getByTestId('invoice-chain').locator('li')).toHaveCount(2);
    await expect(providerPage.getByTestId('invoice-chain')).toContainText(original.invoiceNumber);
    await expect(providerPage.getByTestId('invoice-chain')).toContainText('İptal edildi');

    const cutBatch = await prepareBatch(providerPage, billing, correction, cutBatchId, ids, 'cut');
    if (cutBatch.status === 'SUBMITTED' && !cutBatch.invoices[0]?.decision) {
      const path = `/api/v1/batches/${cutBatch.id}`;
      const attempt = await reviewer.call<{ code: string }>(
        'POST',
        `${path}/invoices/${correctionId}/review`,
        { decision: 'CUT', approvedAmount: '600', reasonCode: '' },
        { etag: (await reviewer.call<S<'Batch'>>('GET', path)).etag, expected: 422 },
      );
      expect(attempt.data.code).toBe('VALIDATION_FAILED');
      expect((await reviewer.call<S<'Batch'>>('GET', path)).data.status).toBe('SUBMITTED');
    }
    const decidedCut = await reviewInUI(
      reviewerPage,
      reviewer,
      cutBatch.id,
      correction.invoiceNumber,
      'CUT',
      'TARIFF_EXCEEDED',
      ids,
    );
    exact(decidedCut.submittedTotal, '800');
    exact(decidedCut.approvedTotal, '600');
    exact(decidedCut.cutTotal, '200');
    exact(decidedCut.returnedTotal, '0');
    exact(decidedCut.rejectedTotal, '0');
    correction = (await billing.call<S<'Invoice'>>('GET', `/api/v1/invoices/${correctionId}`)).data;
    expect(correction.status).toBe('PARTIALLY_APPROVED');
    let settlementId = '';
    await expect
      .poll(
        async () => {
          const settlements = (
            await reviewer.call<S<'SettlementPage'>>(
              'GET',
              `/api/v1/settlements?batchId=${cutBatch.id}`,
            )
          ).data.items;
          expect(settlements.length).toBeLessThanOrEqual(1);
          settlementId = settlements[0]?.id ?? '';
          return settlements.length;
        },
        { timeout: 45_000 },
      )
      .toBe(1);
    ids['settlementId'] = settlementId;
    await stage(ids, 'worker-settlement-observed');
    const settlement = (
      await reviewer.call<S<'Settlement'>>('GET', `/api/v1/settlements/${settlementId}`)
    ).data;
    expect(settlement.batchId).toBe(cutBatch.id);
    exact(settlement.approvedAmount, '600');
    exact(settlement.withheldAmount, '0');
    exact(settlement.payableAmount, '600');
    expect(settlement.status).toBe('PENDING_APPROVAL');
    await stage(ids, 'return-correction-cut-settled');
  } finally {
    await stage(ids, 'latest-resume-ids');
    await Promise.allSettled([billing.close(), reviewer.close()]);
    await Promise.allSettled([providerPage.close(), reviewerPage.close()]);
  }
});

// Separate approved outpatient claim: rejection closes it unpaid; it does not release
// the already delivered health service back into the entitlement wallet.
test('rejected health invoice closes its 400 TRY claim unpaid with zero payable', async ({
  browser,
}) => {
  const rejectClaimId = process.env['E2E_HEALTH_REJECT_CLAIM'] ?? '';
  const resumeInvoice = process.env['E2E_HEALTH_REJECT_INVOICE'] ?? '';
  const resumeDocument = process.env['E2E_HEALTH_REJECT_DOCUMENT'] ?? '';
  const resumeBatch = process.env['E2E_HEALTH_REJECT_BATCH'] ?? '';
  test.skip(!rejectClaimId, 'requires explicit separate 400 TRY approved HEALTH claim');
  test.setTimeout(180_000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const providerPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const reviewerPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  providerPage.setDefaultTimeout(15_000);
  reviewerPage.setDefaultTimeout(15_000);
  const billing = new Actor(providerPage.request, 'provider', true);
  const reviewer = new Actor(reviewerPage.request, 'backoffice', true);
  const ids: Record<string, string> = { claimId: rejectClaimId };
  try {
    await billing.login('billing.a');
    await reviewer.login('financial.reviewer');
    const claimPath = `/api/v1/claims/${rejectClaimId}`;
    const claim = (await billing.call<S<'Claim'>>('GET', claimPath)).data;
    expect(claim.domainCode).toBe('HEALTH');
    if (!resumeInvoice) {
      expect(claim.status).toBe('APPROVED');
      const ready = (
        await billing.call<S<'ClaimInvoiceReadiness'>>('GET', claimPath + '/invoice-readiness')
      ).data;
      expect(ready.ready).toBe(true);
      exact(ready.payerTotal, '400');
      const listed = (
        await billing.call<S<'InvoicePage'>>(
          'GET',
          `/api/v1/invoices?providerOrganizationId=${claim.providerOrganizationId}&fiscalYear=${new Date().getFullYear()}&limit=100`,
        )
      ).data;
      expect(listed.nextCursor).toBeNull();
      expect(
        listed.items.some((i) => i.invoiceNumber === `PC05-DEC-${rejectClaimId}`),
        'resume the existing invoice instead of creating another',
      ).toBe(false);
    }
    const id =
      resumeInvoice ||
      (await createInvoice(providerPage, claim, ids, {
        net: '333.33',
        tax: '66.67',
        payable: '400',
      }));
    ids['invoiceId'] = id;
    const invoice = await prepareInvoice(
      providerPage,
      billing,
      id,
      claim,
      ids,
      resumeDocument,
      'reject',
      '400',
    );
    const batch = await prepareBatch(providerPage, billing, invoice, resumeBatch, ids, 'reject');
    const decided = await reviewInUI(
      reviewerPage,
      reviewer,
      batch.id,
      invoice.invoiceNumber,
      'REJECT',
      'NOT_COVERED',
      ids,
    );
    exact(decided.submittedTotal, '400');
    exact(decided.approvedTotal, '0');
    exact(decided.rejectedTotal, '400');
    exact(decided.returnedTotal, '0');
    expect((await billing.call<S<'Invoice'>>('GET', `/api/v1/invoices/${id}`)).data.status).toBe(
      'REJECTED',
    );
    expect((await billing.call<S<'Claim'>>('GET', claimPath)).data.status).toBe('CLOSED_UNPAID');
    const refusedReadiness = await billing.call<{ code: string }>(
      'GET',
      claimPath + '/invoice-readiness',
      undefined,
      { expected: 409 },
    );
    expect(refusedReadiness.data.code).toBe('CLAIM_NOT_DECIDED');
    let settlementId = '';
    await expect
      .poll(
        async () => {
          const rows = (
            await reviewer.call<S<'SettlementPage'>>(
              'GET',
              `/api/v1/settlements?batchId=${batch.id}`,
            )
          ).data.items;
          expect(rows.length).toBeLessThanOrEqual(1);
          settlementId = rows[0]?.id ?? '';
          return rows.length;
        },
        { timeout: 45_000 },
      )
      .toBe(1);
    ids['settlementId'] = settlementId;
    const settlement = (
      await reviewer.call<S<'Settlement'>>('GET', `/api/v1/settlements/${settlementId}`)
    ).data;
    exact(settlement.approvedAmount, '0');
    exact(settlement.payableAmount, '0');
    exact(settlement.paidAmount, '0');
    await stage(ids, 'rejected-closed-unpaid-zero-payable');
  } finally {
    await stage(ids, 'latest-reject-resume-ids');
    await Promise.allSettled([billing.close(), reviewer.close()]);
    await Promise.allSettled([providerPage.close(), reviewerPage.close()]);
  }
});
