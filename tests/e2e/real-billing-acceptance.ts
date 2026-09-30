import { mkdir } from 'node:fs/promises';
import { expect, request as apiRequest, test, type Page } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';
import { syntheticPDF } from './synthetic-pdf';

type S<K extends keyof components['schemas']> = components['schemas'][K];
export interface BillingAcceptance {
  title: string;
  environmentPrefix: string;
  referencePrefix: string;
  username: string;
  domain: 'HEALTH' | 'ACCOMMODATION';
  sourceType: 'HEALTH_CASE' | 'BOOKING';
  total: string;
  paidText: string;
  net: string;
  tax: string;
  firstPayment: string;
  finalPayment: string;
  excessPayment: string;
  screenshots: string;
}

// The same billing workflow must carry both clinical and lodging claims without
// changing their provider scope or bypassing the invoice/settlement screens.
export function registerBillingAcceptance(config: BillingAcceptance) {
  const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
  const claimId = process.env[`${config.environmentPrefix}_CLAIM`] ?? '';
  const resumeInvoiceId = process.env[`${config.environmentPrefix}_INVOICE`] ?? '';
  const resumeDocumentId = process.env[`${config.environmentPrefix}_DOCUMENT`] ?? '';
  const resumeBatchId = process.env[`${config.environmentPrefix}_BATCH`] ?? '';
  const resumeSettlementId = process.env[`${config.environmentPrefix}_SETTLEMENT`] ?? '';
  const password = process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora';

  test.use({ trace: 'off' });

  test.skip(
    process.env['E2E_REAL_API'] !== '1' || !base || !claimId,
    'requires operator-started local demo and explicit invoice-ready synthetic claim',
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
    await test.info().attach(`${config.referencePrefix.toLowerCase()}-${name}-ids`, {
      contentType: 'application/json',
      body: JSON.stringify({ ...ids, stage: name }),
    });
  }

  async function stepUpIfAsked(page: Page) {
    const dialog = page.getByRole('dialog', { name: 'Parolan\u0131z\u0131 do\u011frulay\u0131n' });
    if (await dialog.isVisible()) {
      await dialog.getByLabel(/^Parola/).fill(password);
      await dialog.getByRole('button', { name: 'Do\u011frula', exact: true }).click();
      await expect(dialog).toBeHidden({ timeout: 15_000 });
    }
  }

  test(config.title, async ({ browser }) => {
    test.setTimeout(240_000);
    expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
    if (process.env['KAPSORA_API_URL'])
      expect(new URL(process.env['KAPSORA_API_URL']).hostname).toMatch(
        /^(localhost|127\.0\.0\.1)$/,
      );
    const providerPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
    const reviewerPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
    const approverPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
    for (const page of [providerPage, reviewerPage, approverPage]) page.setDefaultTimeout(15_000);
    const billing = new Actor(providerPage.request, 'provider', true);
    const reviewer = new Actor(reviewerPage.request, 'backoffice', true);
    const approver = new Actor(approverPage.request, 'backoffice', true);
    const admin = new Actor(await apiRequest.newContext(), 'backoffice', true);
    const ids: Record<string, string> = { claimId };
    try {
      await billing.login(config.username);
      await reviewer.login('financial.reviewer');
      await approver.login('payer.approver');
      await admin.login('admin.a');
      const claimPath = `/api/v1/claims/${claimId}`;
      const claim = (await billing.call<S<'Claim'>>('GET', claimPath)).data;
      expect(claim.domainCode).toBe(config.domain);
      expect(claim.sourceType).toBe(config.sourceType);
      expect(claim.sourceId).toBeTruthy();
      if (config.sourceType === 'BOOKING') {
        const bookingId = process.env[`${config.environmentPrefix}_BOOKING`];
        expect(bookingId, 'lodging acceptance must name its existing booking').toBeTruthy();
        expect(claim.sourceId).toBe(bookingId);
        const foreignClaim = process.env[`${config.environmentPrefix}_FOREIGN_CLAIM`];
        expect(foreignClaim, 'hotel billing must prove the hospital boundary').toBeTruthy();
        await billing.call('GET', `/api/v1/claims/${foreignClaim}`, undefined, { expected: 404 });
      }
      ids['providerOrganizationId'] = claim.providerOrganizationId;
      if (!resumeInvoiceId) {
        expect(claim.status).toBe('APPROVED');
        const readiness = (
          await billing.call<S<'ClaimInvoiceReadiness'>>('GET', claimPath + '/invoice-readiness')
        ).data;
        expect(readiness.ready).toBe(true);
        expect(readiness.blockers).toEqual([]);
        expect(readiness.currencyCode).toBe('TRY');
        exact(readiness.approvedTotal, config.total);
        exact(readiness.payerTotal, config.total);
        exact(readiness.memberTotal, '0');
      } else {
        expect(['APPROVED', 'INVOICED', 'BATCHED', 'SETTLED']).toContain(claim.status);
      }
      await stage(ids, 'source-verified');

      // Always resume from the emitted stage IDs after a mutation. The stable invoice
      // number also catches a draft created before its claim allocation was saved.
      // A draft batch created before membership still requires its explicit resume ID.
      const invoiceNumber = `${config.referencePrefix}-${claimId}`;
      if (!resumeInvoiceId) {
        const invoicePage = (
          await billing.call<S<'InvoicePage'>>(
            'GET',
            `/api/v1/invoices?providerOrganizationId=${claim.providerOrganizationId}&limit=100`,
          )
        ).data;
        expect(
          invoicePage.nextCursor,
          'invoice preflight must inspect every existing invoice',
        ).toBeNull();
        const invoices = invoicePage.items;
        const linked = (
          await Promise.all(
            invoices.map(
              async (item) =>
                (await billing.call<S<'Invoice'>>('GET', `/api/v1/invoices/${item.id}`)).data,
            ),
          )
        ).filter((item) => item.allocations.some((allocation) => allocation.claimId === claimId));
        expect(
          invoices.filter((item) => item.invoiceNumber === invoiceNumber),
          'existing draft requires explicit invoice resume ID',
        ).toEqual([]);
        expect(
          linked,
          'claim already invoiced; set the explicit invoice resume ID from stage attachment',
        ).toEqual([]);
      }
      let invoiceId = resumeInvoiceId;
      if (!invoiceId) {
        await providerPage.goto(
          base + `/portal/billing/invoices/new?claims=${claimId}&currency=TRY`,
        );
        const header = providerPage.getByTestId('invoice-header');
        await header.locator('[name="domainCode"]').selectOption(config.domain);
        await header.locator('[name="invoiceNumber"]').fill(invoiceNumber);
        await header.locator('[name="lineExtensionAmount"]').fill(config.net);
        await header.locator('[name="taxAmount"]').fill(config.tax);
        await header.locator('[name="payableAmount"]').fill(config.total);
        await header.locator('[name="vatRate"]').fill('20');
        await mkdir(config.screenshots, { recursive: true });
        for (const width of [1440, 390]) {
          await providerPage.setViewportSize({ width, height: 1000 });
          await expect
            .poll(() =>
              providerPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
            )
            .toBe(true);
          await providerPage.screenshot({
            path: `${config.screenshots}/invoice-new-${width}.png`,
            fullPage: true,
          });
        }
        await providerPage.setViewportSize({ width: 1440, height: 1000 });
        const created = providerPage.waitForResponse(
          (r) =>
            new URL(r.url()).pathname === '/api/v1/invoices' && r.request().method() === 'POST',
          { timeout: 15_000 },
        );
        await providerPage.getByRole('button', { name: 'Kaydet', exact: true }).click();
        const response = await created;
        expect(response.status()).toBe(201);
        invoiceId = ((await response.json()) as S<'Invoice'>).id;
        ids['invoiceId'] = invoiceId;
        ids['invoiceNumber'] = invoiceNumber;
        await stage(ids, 'invoice-created');
      }
      ids['invoiceId'] = invoiceId;
      const invoicePath = `/api/v1/invoices/${invoiceId}`;
      let invoice = await billing.call<S<'Invoice'>>('GET', invoicePath);
      expect(invoice.data.providerOrganizationId).toBe(claim.providerOrganizationId);
      expect(invoice.data.currencyCode).toBe('TRY');

      expect(invoice.data.domainCode).toBe(config.domain);
      expect(invoice.data.fiscalYear).toBe(Number(invoice.data.invoiceDate.slice(0, 4)));
      exact(invoice.data.payableAmount, config.total);
      if (invoice.data.status === 'DRAFT') {
        await providerPage.goto(base + `/portal/billing/invoices/${invoiceId}?claims=${claimId}`);
        if (invoice.data.allocations.length === 0) {
          const row = providerPage
            .getByTestId('allocation-row')
            .filter({ hasText: claim.reference });
          await row.getByRole('textbox').fill(config.total);
          await providerPage
            .getByRole('button', { name: 'Da\u011f\u0131t\u0131m\u0131 kaydet', exact: true })
            .click();
          await expect
            .poll(
              async () =>
                (await billing.call<S<'Invoice'>>('GET', invoicePath)).data.allocationTotal,
            )
            .toMatch(new RegExp(`^${config.total}(?:\\.0+)?$`));
        }
        invoice = await billing.call<S<'Invoice'>>('GET', invoicePath);
        expect(invoice.data.allocations).toHaveLength(1);
        expect(invoice.data.allocations[0]!.claimId).toBe(claimId);
        exact(invoice.data.allocationTotal, config.total);
        exact(invoice.data.allocationDifference, '0');
        if (!invoice.data.documentId) {
          if (resumeDocumentId) {
            ids['documentId'] = resumeDocumentId;
          } else {
            const filename = `${config.referencePrefix.toLowerCase()}-${invoiceId.slice(0, 8)}.pdf`;
            const upload = providerPage.getByTestId('document-upload-form');
            await expect(
              upload,
              'billing role must be able to upload its invoice image',
            ).toBeVisible();
            await upload.getByLabel(/Dosya se\u00e7/).setInputFiles({
              name: filename,
              mimeType: 'application/pdf',
              buffer: syntheticPDF(),
            });
            await upload.getByLabel(/Belge t\u00fcr\u00fc/).selectOption('INVOICE');
            const reservation = providerPage.waitForResponse(
              (r) =>
                new URL(r.url()).pathname === '/api/v1/documents' &&
                r.request().method() === 'POST',
              { timeout: 15_000 },
            );
            await upload.getByRole('button', { name: 'Belge y\u00fckle', exact: true }).click();
            const reserved = await reservation;
            expect(reserved.status(), 'document upload reservation').toBe(201);
            ids['documentId'] = (
              (await reserved.json()) as { document: S<'Document'> }
            ).document.id;
            await stage(ids, 'image-reserved');
          }
          const documentPath = `/api/v1/documents/${ids['documentId']}`;
          await expect
            .poll(
              async () => (await billing.call<S<'Document'>>('GET', documentPath)).data.scanStatus,
              {
                timeout: 45_000,
              },
            )
            .toBe('CLEAN');
          invoice = await billing.call<S<'Invoice'>>('GET', invoicePath);
          await providerPage.reload();
          const attached = providerPage.waitForResponse(
            (r) => new URL(r.url()).pathname === invoicePath && r.request().method() === 'PATCH',
          );
          await providerPage.getByRole('button', { name: 'Kaydet', exact: true }).click();
          const attachResponse = await attached;
          const attachBody = await attachResponse.json();
          expect(
            attachResponse.status(),
            `invoice image attachment: ${attachBody.code ?? 'no problem code'}`,
          ).toBe(200);
          await expect
            .poll(
              async () => (await billing.call<S<'Invoice'>>('GET', invoicePath)).data.documentId,
            )
            .toBe(ids['documentId']);
          await stage(ids, 'clean-image-attached');
        }
        invoice = await billing.call<S<'Invoice'>>('GET', invoicePath);
        expect(invoice.data.documentId).toBeTruthy();
        await providerPage.reload();
        const submitted = providerPage.waitForResponse(
          (r) =>
            new URL(r.url()).pathname === invoicePath + '/submit' &&
            r.request().method() === 'POST',
          { timeout: 15_000 },
        );
        await providerPage.getByTestId('invoice-submit').click();
        const submitResponse = await submitted;
        expect(submitResponse.status()).toBe(200);
        const submitHeaders = submitResponse.request().headers();
        expect(submitHeaders['idempotency-key']).toBeTruthy();
        const replay = await billing.call<S<'Invoice'>>(
          'POST',
          invoicePath + '/submit',
          undefined,
          {
            etag: submitHeaders['if-match'],
            key: submitHeaders['idempotency-key'],
          },
        );
        expect(replay.data.id).toBe(invoiceId);
        expect(replay.data.status).toBe('SUBMITTED');
        await expect(providerPage.getByTestId('invoice-status')).toHaveText('G\u00f6nderildi');
        await stage(ids, 'invoice-submitted');
      }
      invoice = await billing.call<S<'Invoice'>>('GET', invoicePath);
      expect(['SUBMITTED', 'IN_BATCH', 'APPROVED', 'SETTLED']).toContain(invoice.data.status);
      expect(invoice.data.allocations.map((a) => a.claimId)).toEqual([claimId]);
      exact(invoice.data.allocationTotal, config.total);
      await billing.call(
        'PATCH',
        invoicePath,
        { notes: 'late mutation' },
        { etag: invoice.etag, expected: 409 },
      );

      let batchId = resumeBatchId || invoice.data.batchId || '';
      if (!batchId) {
        await providerPage.goto(base + '/portal/billing/batches/new');
        const form = providerPage.getByTestId('batch-form');
        await form.locator('[name="domainCode"]').selectOption(config.domain);
        await form.locator('[name="periodFrom"]').fill(invoice.data.invoiceDate);
        await form.locator('[name="periodTo"]').fill(invoice.data.invoiceDate);
        const created = providerPage.waitForResponse(
          (r) => new URL(r.url()).pathname === '/api/v1/batches' && r.request().method() === 'POST',
          { timeout: 15_000 },
        );
        await form.getByRole('button', { name: 'Kaydet', exact: true }).click();
        const response = await created;
        expect(response.status()).toBe(201);
        batchId = ((await response.json()) as S<'Batch'>).id;
        ids['batchId'] = batchId;
        await stage(ids, 'batch-created');
      }
      ids['batchId'] = batchId;
      const batchPath = `/api/v1/batches/${batchId}`;
      let batch = await billing.call<S<'Batch'>>('GET', batchPath);
      expect(batch.data.providerOrganizationId).toBe(claim.providerOrganizationId);
      expect(batch.data.domainCode).toBe(config.domain);
      expect(batch.data.currencyCode).toBe('TRY');
      if (batch.data.status === 'DRAFT') {
        await providerPage.goto(base + `/portal/billing/batches/${batchId}`);
        if (batch.data.invoices.length === 0) {
          await providerPage.locator(`#inv-${invoiceId}`).check();
          const saved = providerPage.waitForResponse(
            (r) =>
              new URL(r.url()).pathname === batchPath + '/invoices' &&
              r.request().method() === 'PUT',
          );
          await providerPage
            .getByRole('button', { name: 'Faturalar\u0131 kaydet', exact: true })
            .click();
          const savedResponse = await saved;
          const savedBody = await savedResponse.json();
          expect(
            savedResponse.status(),
            `batch membership: ${savedBody.code ?? 'no problem code'}`,
          ).toBe(200);
          await expect
            .poll(
              async () => (await billing.call<S<'Batch'>>('GET', batchPath)).data.invoices.length,
            )
            .toBe(1);
        }
        batch = await billing.call<S<'Batch'>>('GET', batchPath);
        expect(batch.data.invoices.map((item) => item.invoiceId)).toEqual([invoiceId]);
        const submitted = providerPage.waitForResponse(
          (r) =>
            new URL(r.url()).pathname === batchPath + '/submit' && r.request().method() === 'POST',
          { timeout: 15_000 },
        );
        await providerPage.getByTestId('batch-submit').click();
        await stepUpIfAsked(providerPage);
        expect((await submitted).status()).toBe(200);
        await stage(ids, 'batch-submitted');
      }
      batch = await billing.call<S<'Batch'>>('GET', batchPath);
      expect(batch.data.invoices.map((item) => item.invoiceId)).toEqual([invoiceId]);
      exact(batch.data.submittedTotal, config.total);
      if (batch.data.status === 'SUBMITTED' || batch.data.status === 'UNDER_REVIEW') {
        await reviewerPage.goto(base + `/billing/batches/${batchId}`);
        if (!batch.data.invoices[0]!.decision) {
          await reviewerPage
            .getByTestId('review-row')
            .filter({ hasText: invoice.data.invoiceNumber })
            .getByRole('button', { name: 'Fatura kararı', exact: true })
            .click();
          const decision = reviewerPage.getByTestId('decision-form');
          await decision.locator('[name="decision"]').selectOption('APPROVE');
          await decision.getByTestId('save-decision').click();
          await expect
            .poll(
              async () =>
                (await reviewer.call<S<'Batch'>>('GET', batchPath)).data.invoices[0]?.decision,
            )
            .toBe('APPROVE');
          await stage(ids, 'invoice-approved-by-reviewer');
        }
        batch = await reviewer.call<S<'Batch'>>('GET', batchPath);
        expect(batch.data.submittedBy).not.toBe(batch.data.invoices[0]!.decidedBy);
        await reviewerPage.getByTestId('decide-batch').click();
        await expect
          .poll(async () => (await reviewer.call<S<'Batch'>>('GET', batchPath)).data.status)
          .toMatch(/^(DECIDED|SETTLING|CLOSED)$/);
        await stage(ids, 'batch-decided');
      }
      batch = await reviewer.call<S<'Batch'>>('GET', batchPath);
      expect(['DECIDED', 'SETTLING', 'CLOSED']).toContain(batch.data.status);
      expect(batch.data.invoices[0]!.decision).toBe('APPROVE');
      exact(batch.data.approvedTotal, config.total);
      for (const amount of [
        batch.data.cutTotal,
        batch.data.returnedTotal,
        batch.data.rejectedTotal,
      ])
        exact(amount, '0');

      let settlementId = resumeSettlementId;
      await expect
        .poll(
          async () => {
            const rows = (
              await approver.call<S<'SettlementPage'>>(
                'GET',
                `/api/v1/settlements?batchId=${batchId}`,
              )
            ).data.items;
            expect(rows.length).toBeLessThanOrEqual(1);
            settlementId = rows[0]?.id ?? settlementId;
            return rows.length;
          },
          { timeout: 45_000 },
        )
        .toBe(1);
      ids['settlementId'] = settlementId;
      await stage(ids, 'worker-settlement-observed');
      const settlementPath = `/api/v1/settlements/${settlementId}`;
      let settlement = await approver.call<S<'Settlement'>>('GET', settlementPath);
      expect(settlement.data.batchId).toBe(batchId);
      expect(settlement.data.versionNo).toBe(1);
      expect(settlement.data.settlementMethod).toBe('BANK_TRANSFER');
      exact(settlement.data.approvedAmount, config.total);
      exact(settlement.data.withheldAmount, '0');
      exact(settlement.data.payableAmount, config.total);
      const due = new Date(batch.data.decidedAt!.slice(0, 10) + 'T00:00:00Z');
      due.setUTCDate(due.getUTCDate() + 30);
      expect(settlement.data.dueDate).toBe(due.toISOString().slice(0, 10));
      if (settlement.data.status === 'PENDING_APPROVAL') {
        // This synthetic fixture is below the configured 50,000 TRY checker threshold.
        // Voluntary step-up proves this actor's password, not a required challenge.
        await approver.call('POST', '/api/v1/session/step-up', { password });
        await approverPage.goto(base + `/billing/settlements/${settlementId}`);
        await approverPage.getByTestId('approve-settlement').click();
        await stepUpIfAsked(approverPage);
        await expect
          .poll(
            async () => (await approver.call<S<'Settlement'>>('GET', settlementPath)).data.status,
          )
          .toBe('APPROVED');
        await stage(ids, 'settlement-approved');
      }
      settlement = await approver.call<S<'Settlement'>>('GET', settlementPath);
      expect(settlement.data.approvedBy).toBeTruthy();
      expect(settlement.data.approvedBy).not.toBe(batch.data.submittedBy);
      expect(settlement.data.approvedBy).not.toBe(batch.data.decidedBy);
      expect(settlement.data.checkedBy).toBeFalsy(); // below checker threshold
      await approverPage.goto(base + `/billing/settlements/${settlementId}`);
      const paymentPath = settlementPath + '/payment-records';
      if (settlement.data.status === 'APPROVED') {
        const ref = `${config.referencePrefix}-PART-${invoiceId}`;
        const form = approverPage.getByTestId('payment-form');
        await form.locator('[name="externalReference"]').fill(ref);
        await form.locator('[name="amount"]').fill(config.firstPayment);
        await form.locator('[name="paidAt"]').fill(new Date().toISOString().slice(0, 10));
        const recorded = approverPage.waitForResponse(
          (r) => new URL(r.url()).pathname === paymentPath && r.request().method() === 'POST',
          { timeout: 15_000 },
        );
        await form.getByTestId('record-payment').click();
        const recordResponse = await recorded;
        expect(recordResponse.status()).toBe(201);
        const paymentKey = recordResponse.request().headers()['idempotency-key'];
        expect(paymentKey).toBeTruthy();
        const paymentBody = recordResponse.request().postDataJSON() as S<'CreatePaymentRecord'>;
        const paymentReplay = await approver.call<S<'Settlement'>>(
          'POST',
          paymentPath,
          paymentBody,
          {
            key: paymentKey,
            expected: 201,
          },
        );
        expect(paymentReplay.data.payments).toHaveLength(1);
        await expect
          .poll(
            async () => (await approver.call<S<'Settlement'>>('GET', settlementPath)).data.status,
          )
          .toBe('PARTIALLY_PAID');
        ids['partialPaymentReference'] = ref;
        await stage(ids, 'partial-payment');
      }
      settlement = await approver.call<S<'Settlement'>>('GET', settlementPath);
      if (settlement.data.status === 'PARTIALLY_PAID') {
        exact(settlement.data.paidAmount, config.firstPayment);
        const over = await approver.call<{ code: string }>(
          'POST',
          paymentPath,
          {
            externalReference: `${config.referencePrefix}-OVER-${invoiceId}`,
            amount: config.excessPayment,
            paidAt: new Date().toISOString(),
          },
          { expected: 409 },
        );
        expect(over.data.code).toBe('PAYMENT_EXCEEDS_SETTLEMENT');
        const existingRef = settlement.data.payments[0]!.externalReference;
        const duplicate = await approver.call<{ code: string }>(
          'POST',
          paymentPath,
          { externalReference: existingRef, amount: '100', paidAt: new Date().toISOString() },
          { expected: 409 },
        );
        expect(duplicate.data.code).toBe('PAYMENT_REFERENCE_TAKEN');
        const form = approverPage.getByTestId('payment-form');
        const finalRef = `${config.referencePrefix}-FINAL-${invoiceId}`;
        await form.locator('[name="externalReference"]').fill(finalRef);
        await form.locator('[name="amount"]').fill(config.finalPayment);
        await form.locator('[name="paidAt"]').fill(new Date().toISOString().slice(0, 10));
        await form.getByTestId('record-payment').click();
        await expect
          .poll(
            async () => (await approver.call<S<'Settlement'>>('GET', settlementPath)).data.status,
          )
          .toBe('PAID');
        ids['finalPaymentReference'] = finalRef;
        await stage(ids, 'final-payment');
      }
      settlement = await approver.call<S<'Settlement'>>('GET', settlementPath);
      expect(settlement.data.status).toBe('PAID');
      exact(settlement.data.payableAmount, config.total);
      exact(settlement.data.paidAmount, config.total);
      expect(settlement.data.payments).toHaveLength(2);
      expect(
        settlement.data.payments
          .map((item) => micros(item.amount))
          .sort((a, b) => (a < b ? -1 : a > b ? 1 : 0)),
      ).toEqual([micros(config.firstPayment), micros(config.finalPayment)]);
      expect(new Set(settlement.data.payments.map((item) => item.externalReference)).size).toBe(2);
      const payments = (await approver.call<S<'PaymentRecordList'>>('GET', paymentPath)).data.items;
      expect(payments.map((item) => item.id)).toEqual(
        settlement.data.payments.map((item) => item.id),
      );
      // These are local notifications published by the commands and delivered by the
      // notification worker; the deferred settlement.approved integration event is separate.
      for (const [eventCode, count] of [
        ['settlement.approved', 1],
        ['payment.recorded', 2],
      ] as const) {
        const query = `/api/v1/notification-messages?eventCode=${eventCode}&recipientType=ORGANIZATION&recipientId=${claim.providerOrganizationId}&limit=100`;
        await expect
          .poll(
            async () => {
              const messages = (await admin.call<S<'NotificationMessagePage'>>('GET', query)).data
                .items;
              return messages.filter(
                (message) =>
                  message.channel === 'INAPP' &&
                  message.safeVariables['reference_no'] === settlement.data.reference &&
                  message.status === 'SENT',
              ).length;
            },
            { timeout: 45_000 },
          )
          .toBe(count);
        const messages = (
          await admin.call<S<'NotificationMessagePage'>>('GET', query)
        ).data.items.filter(
          (message) =>
            message.channel === 'INAPP' &&
            message.safeVariables['reference_no'] === settlement.data.reference,
        );
        expect(messages).toHaveLength(count);
        for (const message of messages) {
          expect(message.safeVariables['deep_link']).toContain(settlementId);
          expect(message.safeVariables['currency']).toBe('TRY');
        }
      }
      await approverPage.reload();
      await expect(approverPage.getByTestId('payment-form')).toHaveCount(0);
      await expect(approverPage.getByTestId('paid')).toContainText(config.paidText);
      await mkdir(config.screenshots, { recursive: true });
      for (const width of [1440, 390]) {
        await approverPage.setViewportSize({ width, height: 1000 });
        await expect
          .poll(() =>
            approverPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
          )
          .toBe(true);
        await approverPage.screenshot({
          path: `${config.screenshots}/paid-${width}.png`,
          fullPage: true,
        });
      }
      await stage(ids, 'paid-and-notified');
    } finally {
      await stage(ids, 'latest-resume-ids');
      await Promise.allSettled([
        billing.close(),
        reviewer.close(),
        approver.close(),
        admin.close(),
      ]);
      await Promise.allSettled([providerPage.close(), reviewerPage.close(), approverPage.close()]);
    }
  });
}
