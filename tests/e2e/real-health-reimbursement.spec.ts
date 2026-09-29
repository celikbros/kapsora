import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';
import { syntheticPDF } from './synthetic-pdf';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const resumeRequestId = process.env['E2E_HEALTH_REIMBURSEMENT_REQUEST'] ?? '';
const resumeDocumentId = process.env['E2E_HEALTH_REIMBURSEMENT_DOCUMENT'] ?? '';
const resumeReimbursementId = process.env['E2E_HEALTH_REIMBURSEMENT_ID'] ?? '';
const amount = '125.50';

test.use({ trace: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base,
  'requires operator-started local UI, API, worker and explicit opt-in',
);

function money(value: string | number): bigint {
  const text = String(value);
  expect(text).toMatch(/^\d+(?:\.\d{1,6})?$/);
  const [whole, fraction = ''] = text.split('.');
  return BigInt(whole!) * 1_000_000n + BigInt(fraction.padEnd(6, '0'));
}

function todayIstanbul(): string {
  return new Intl.DateTimeFormat('en-CA', {
    timeZone: 'Europe/Istanbul',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).format(new Date());
}

// Synthetic, valid TR check digits. The complete value stays only in the input/request body.
function syntheticIBAN(): string {
  const bban = Array.from(randomBytes(22), (byte) => String(byte % 10)).join('');
  let remainder = 0;
  for (const digit of `${bban}292700`) remainder = (remainder * 10 + Number(digit)) % 97;
  return `TR${String(98 - remainder).padStart(2, '0')}${bban}`;
}

async function stage(ids: Record<string, string>, name: string) {
  await test.info().attach(`pc05-reimbursement-${name}`, {
    contentType: 'application/json',
    body: JSON.stringify({ ...ids, stage: name }),
  });
}

test('member receipt and request gate lead to reviewed reimbursement, one money consumption and local payment', async ({
  browser,
}) => {
  test.setTimeout(240_000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  if (process.env['KAPSORA_API_URL'])
    expect(new URL(process.env['KAPSORA_API_URL']).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const memberPage = await browser.newPage({
    locale: 'tr-TR',
    timezoneId: 'Europe/Istanbul',
    viewport: { width: 390, height: 844 },
  });
  const reviewerPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  for (const page of [memberPage, reviewerPage]) page.setDefaultTimeout(15_000);
  const member = new Actor(memberPage.request, 'member', true);
  const reviewer = new Actor(reviewerPage.request, 'backoffice', true);
  const admin = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const ids: Record<string, string> = {};
  try {
    await member.login('member.a');
    await reviewer.login('financial.reviewer');
    await admin.login('admin.a');
    const me = (await member.call<S<'MyPerson'>>('GET', '/api/v1/me/person')).data;
    const personId = me.person.id;
    ids['personId'] = personId;
    let serviceDate = todayIstanbul();
    let resumeRequest: S<'ServiceRequest'> | null = null;
    if (resumeRequestId) {
      resumeRequest = (
        await member.call<S<'ServiceRequest'>>('GET', `/api/v1/service-requests/${resumeRequestId}`)
      ).data;
      serviceDate = resumeRequest.serviceDate;
    }
    const enrollment = me.enrollments.find(
      (row) =>
        row.status === 'ACTIVE' &&
        row.validFrom <= serviceDate &&
        (!row.validTo || row.validTo > serviceDate),
    );
    expect(enrollment, 'member.a needs an active DEMO_STANDARD enrollment today').toBeDefined();
    ids['enrollmentId'] = enrollment!.id;
    const definitions = (
      await member.call<{ items: S<'ServiceDefinition'>[] }>(
        'GET',
        '/api/v1/service-definitions?active=true&limit=200',
      )
    ).data.items;
    const service = definitions.find((row) => row.code === 'GP_VISIT');
    expect(service).toBeDefined();
    const providers = (
      await member.call<{ items: S<'Provider'>[] }>(
        'GET',
        '/api/v1/providers?status=ACTIVE&limit=200',
      )
    ).data.items;
    const provider = providers.find((row) => row.organizationName === 'Demo Hastane');
    expect(provider).toBeDefined();
    ids['providerId'] = provider!.tenantOrganizationId;
    if (resumeRequest) {
      expect(resumeRequest.requestType).toBe('REIMBURSEMENT');
      expect(resumeRequest.personId).toBe(personId);
      expect(resumeRequest.enrollmentId).toBe(enrollment!.id);
      expect(resumeRequest.providerOrganizationId).toBe(provider!.tenantOrganizationId);
      expect(resumeRequest.items).toHaveLength(1);
      expect(resumeRequest.items[0]!.serviceDefinitionId).toBe(service!.id);
      expect(money(resumeRequest.items[0]!.requestedAmount!)).toBe(money(amount));
    }
    if (!resumeRequestId && !resumeReimbursementId) {
      let cursor = '';
      do {
        const page = (
          await member.call<S<'ReimbursementPage'>>(
            'GET',
            `/api/v1/me/reimbursements?limit=100${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`,
          )
        ).data;
        const prior = page.items.find(
          (row) =>
            row.personId === personId &&
            row.providerOrganizationId === provider!.tenantOrganizationId &&
            row.serviceDate === serviceDate &&
            money(row.requestedAmount) === money(amount) &&
            row.status !== 'CANCELLED',
        );
        if (prior)
          throw new Error(
            `matching reimbursement exists; resume with request ${prior.serviceRequestId}, document ${prior.receiptDocumentId}, reimbursement ${prior.id}`,
          );
        cursor = page.nextCursor ?? '';
      } while (cursor);
    }
    const accounts = async () =>
      (
        await admin.call<{ items: S<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${personId}/entitlements?asOf=${serviceDate}`,
        )
      ).data.items;
    const account = (await accounts()).find(
      (row) =>
        row.enrollmentId === enrollment!.id &&
        row.definition.code === 'HEALTH_MONEY' &&
        row.definition.unitType === 'MONEY' &&
        row.definition.currencyCode === 'TRY',
    );
    expect(account, 'GP_VISIT needs this enrollment’s HEALTH_MONEY TRY account').toBeDefined();
    ids['accountId'] = account!.id;
    expect(money(account!.available)).toBeGreaterThan(money(amount));
    const before = {
      available: money(account!.available),
      reserved: money(account!.reserved),
      consumed: money(account!.consumed),
    };

    let requestId = resumeRequestId;
    let documentId = resumeDocumentId;
    let reimbursementId = resumeReimbursementId;
    if (!requestId && documentId)
      throw new Error('resume document requires E2E_HEALTH_REIMBURSEMENT_REQUEST');
    if (reimbursementId && (!requestId || !documentId))
      throw new Error('resume reimbursement requires its explicit request and document IDs');

    if (!requestId) {
      await memberPage.goto(base + '/uye/reimbursements/new');
      const form = memberPage.getByTestId('request-form');
      await form.locator('[name="serviceDefinitionId"]').selectOption(service!.id);
      await form.locator('[name="serviceDate"]').fill(serviceDate);
      await form
        .locator('[name="providerOrganizationId"]')
        .selectOption(provider!.tenantOrganizationId);
      await form.locator('[name="amount"]').fill(amount);
      const created = memberPage.waitForResponse(
        (response) =>
          new URL(response.url()).pathname === '/api/v1/service-requests' &&
          response.request().method() === 'POST',
      );
      await memberPage.getByTestId('create-request').click();
      const response = await created;
      requestId = ((await response.json()) as S<'ServiceRequest'>).id;
      ids['requestId'] = requestId;
      await stage(ids, 'request-created');
      expect(response.status()).toBe(201);
      await expect(memberPage.getByTestId('account-form')).toHaveCount(0);
      await expect(memberPage.getByTestId('submit-request')).toHaveCount(0);
      const reserved = memberPage.waitForResponse(
        (reply) =>
          new URL(reply.url()).pathname === '/api/v1/documents' &&
          reply.request().method() === 'POST',
      );
      await memberPage.getByTestId('receipt-file').setInputFiles({
        name: `pc05-receipt-${randomUUID()}.pdf`,
        mimeType: 'application/pdf',
        buffer: syntheticPDF(),
      });
      const reserveResponse = await reserved;
      documentId = ((await reserveResponse.json()) as { document: S<'Document'> }).document.id;
      ids['documentId'] = documentId;
      await stage(ids, 'receipt-reserved');
      expect(reserveResponse.status()).toBe(201);
    }
    if (requestId && !documentId) {
      // A previous run may have stopped after creating the request. Resume that exact row.
      const bytes = syntheticPDF();
      const reserved = await member.call<S<'DocumentUpload'>>(
        'POST',
        '/api/v1/documents',
        {
          originalFilename: `pc05-receipt-${randomUUID()}.pdf`,
          contentType: 'application/pdf',
          byteSize: bytes.length,
          classification: 'PERSONAL',
        },
        { expected: 201 },
      );
      documentId = reserved.data.document.id;
      ids['requestId'] = requestId;
      ids['documentId'] = documentId;
      await stage(ids, 'receipt-reserved');
      if (reserved.data.upload) {
        const ticket = reserved.data.upload;
        expect(new URL(ticket.url).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
        const put = await memberPage.request.put(ticket.url, {
          headers: ticket.headers,
          data: bytes,
        });
        expect(put.ok()).toBe(true);
        await member.call<S<'Document'>>('POST', `/api/v1/documents/${documentId}/complete`, {
          byteSize: bytes.length,
          sha256: createHash('sha256').update(bytes).digest('hex'),
        });
      }
      await member.call(
        'POST',
        `/api/v1/documents/${documentId}/links`,
        {
          aggregateType: 'SERVICE_REQUEST',
          aggregateId: requestId,
          documentTypeCode: 'RECEIPT',
        },
        { expected: 201 },
      );
      await stage(ids, 'receipt-linked');
    }
    ids['requestId'] = requestId;
    ids['documentId'] = documentId;
    const requestPath = `/api/v1/service-requests/${requestId}`;
    const documentPath = `/api/v1/documents/${documentId}`;
    await expect
      .poll(async () => (await member.call<S<'Document'>>('GET', documentPath)).data.scanStatus, {
        timeout: 60_000,
      })
      .toBe('CLEAN');
    await expect
      .poll(async () => {
        const document = (await member.call<S<'Document'>>('GET', documentPath)).data;
        return document.links.some(
          (link) =>
            link.aggregateType === 'SERVICE_REQUEST' &&
            link.aggregateId === requestId &&
            link.documentTypeCode === 'RECEIPT',
        );
      })
      .toBe(true);
    const request = await member.call<S<'ServiceRequest'>>('GET', requestPath);
    expect(request.data.requestType).toBe('REIMBURSEMENT');
    expect(request.data.personId).toBe(personId);
    expect(request.data.enrollmentId).toBe(enrollment!.id);
    expect(request.data.providerOrganizationId).toBe(provider!.tenantOrganizationId);
    if (request.data.status === 'DRAFT') {
      if (resumeRequestId) {
        await member.call<S<'ServiceRequest'>>(
          'POST',
          requestPath + '/submit',
          {},
          { etag: request.etag },
        );
      } else {
        await expect(memberPage.getByTestId('receipt-scan')).toBeVisible();
        const submitted = memberPage.waitForResponse(
          (response) =>
            new URL(response.url()).pathname === requestPath + '/submit' &&
            response.request().method() === 'POST',
        );
        await memberPage.getByTestId('submit-request').click();
        expect((await submitted).status()).toBe(200);
      }
      await stage(ids, 'request-submitted');
    }
    const gated = (await member.call<S<'ServiceRequest'>>('GET', requestPath)).data;
    expect(['PENDING_REVIEW', 'APPROVED', 'PARTIALLY_APPROVED']).toContain(gated.status);
    if (!resumeRequestId) await expect(memberPage.getByTestId('account-form')).toBeVisible();
    const iban = syntheticIBAN();
    if (!reimbursementId) {
      if (resumeRequestId) {
        const created = await member.call<S<'Reimbursement'>>(
          'POST',
          '/api/v1/reimbursements',
          {
            serviceRequestId: requestId,
            receiptDocumentId: documentId,
            requestedAmount: amount,
            bankAccount: iban,
            currencyCode: 'TRY',
          },
          { expected: 201 },
        );
        reimbursementId = created.data.id;
      } else {
        const created = memberPage.waitForResponse(
          (response) =>
            new URL(response.url()).pathname === '/api/v1/reimbursements' &&
            response.request().method() === 'POST',
        );
        await memberPage.getByTestId('account-form').locator('[name="bankAccount"]').fill(iban);
        await memberPage.getByTestId('create-reimbursement').click();
        const response = await created;
        reimbursementId = ((await response.json()) as S<'Reimbursement'>).id;
        ids['reimbursementId'] = reimbursementId;
        await stage(ids, 'reimbursement-created');
        expect(response.status()).toBe(201);
      }
      ids['reimbursementId'] = reimbursementId;
      await stage(ids, 'reimbursement-created');
    }
    ids['reimbursementId'] = reimbursementId;
    const reimbursementPath = `/api/v1/reimbursements/${reimbursementId}`;
    let reimbursement = await member.call<S<'Reimbursement'>>('GET', reimbursementPath);
    expect(reimbursement.data.personId).toBe(personId);
    expect(reimbursement.data.serviceRequestId).toBe(requestId);
    expect(reimbursement.data.receiptDocumentId).toBe(documentId);
    expect(money(reimbursement.data.requestedAmount)).toBe(money(amount));
    expect(reimbursement.data.currencyCode).toBe('TRY');
    expect(reimbursement.data.bankAccountMasked).toHaveLength(4);
    if (reimbursement.data.status === 'DRAFT') {
      const duplicate = await member.call<{ code: string }>(
        'POST',
        '/api/v1/reimbursements',
        {
          serviceRequestId: requestId,
          receiptDocumentId: documentId,
          requestedAmount: amount,
          bankAccount: syntheticIBAN(),
          currencyCode: 'TRY',
        },
        { expected: 409 },
      );
      expect(duplicate.data.code).toBe('REIMBURSEMENT_DUPLICATE');
      const unchanged = (await accounts()).find((row) => row.id === account!.id)!;
      expect(money(unchanged.consumed)).toBe(before.consumed);
      await memberPage.goto(base + `/uye/reimbursements/${reimbursementId}`);
      const submitted = memberPage.waitForResponse(
        (response) =>
          new URL(response.url()).pathname === reimbursementPath + '/submit' &&
          response.request().method() === 'POST',
      );
      await memberPage.getByTestId('submit-reimbursement').click();
      expect((await submitted).status()).toBe(200);
      await stage(ids, 'reimbursement-submitted');
    }
    reimbursement = await member.call<S<'Reimbursement'>>('GET', reimbursementPath);
    expect([
      'SUBMITTED',
      'UNDER_REVIEW',
      'APPROVED',
      'PARTIALLY_APPROVED',
      'PAYMENT_ORDERED',
      'PAID',
    ]).toContain(reimbursement.data.status);
    if (['SUBMITTED', 'UNDER_REVIEW'].includes(reimbursement.data.status)) {
      const unchanged = (await accounts()).find((row) => row.id === account!.id)!;
      expect(money(unchanged.consumed)).toBe(before.consumed);
      await reviewerPage.goto(base + `/billing/reimbursements/${reimbursementId}`);
      const decision = reviewerPage.getByTestId('reimbursement-decision');
      await decision.locator('[name="choice"]').selectOption('APPROVE');
      const decided = reviewerPage.waitForResponse(
        (response) =>
          new URL(response.url()).pathname === reimbursementPath + '/decide' &&
          response.request().method() === 'POST',
      );
      await decision.getByTestId('decide-reimbursement').click();
      expect((await decided).status()).toBe(200);
      await stage(ids, 'approved');
    }
    reimbursement = await reviewer.call<S<'Reimbursement'>>('GET', reimbursementPath);
    expect(['APPROVED', 'PAYMENT_ORDERED', 'PAID']).toContain(reimbursement.data.status);
    expect(reimbursement.data.decidedBy).toBeTruthy();
    expect(money(reimbursement.data.approvedAmount!)).toBe(money(amount));
    expect(reimbursement.data.claimId).toBeTruthy();
    const afterApproval = (await accounts()).find((row) => row.id === account!.id)!;
    if (!resumeReimbursementId) {
      expect(money(afterApproval.available)).toBe(before.available - money(amount));
      expect(money(afterApproval.consumed)).toBe(before.consumed + money(amount));
      expect(money(afterApproval.reserved)).toBe(before.reserved);
    }
    const ledger = (
      await admin.call<S<'LedgerPage'>>(
        'GET',
        `/api/v1/entitlement-accounts/${account!.id}/ledger?limit=100`,
      )
    ).data;
    expect(ledger.nextCursor).toBeFalsy();
    const consumption = ledger.items.filter(
      (row) => row.referenceId === requestId && row.movementType === 'CONSUME',
    );
    expect(consumption).toHaveLength(1);
    expect(money(consumption[0]!.deltaConsumed)).toBe(money(amount));
    if (reimbursement.data.status !== 'PAID') {
      await reviewerPage.goto(base + `/billing/reimbursements/${reimbursementId}`);
      const paymentReference = `PC05-REIMB-${reimbursementId}`;
      const paid = reviewerPage.waitForResponse(
        (response) =>
          new URL(response.url()).pathname === reimbursementPath + '/payment' &&
          response.request().method() === 'POST',
      );
      await reviewerPage
        .getByTestId('reimbursement-payment')
        .locator('[name="paymentReference"]')
        .fill(paymentReference);
      await reviewerPage.getByTestId('pay-reimbursement').click();
      expect((await paid).status()).toBe(200);
      await stage(ids, 'local-payment-recorded');
    }
    reimbursement = await member.call<S<'Reimbursement'>>('GET', reimbursementPath);
    expect(reimbursement.data.status).toBe('PAID');
    expect(reimbursement.data.paymentReference).toBe(`PC05-REIMB-${reimbursementId}`);
    expect(reimbursement.data.paidAt).toBeTruthy();
    const afterPayment = (await accounts()).find((row) => row.id === account!.id)!;
    expect(money(afterPayment.available)).toBe(money(afterApproval.available));
    expect(money(afterPayment.consumed)).toBe(money(afterApproval.consumed));
    await memberPage.goto(base + `/uye/reimbursements/${reimbursementId}`);
    await expect(memberPage.getByTestId('reimbursement-status')).toHaveText('Ödendi');
    await expect(memberPage.getByTestId('reimbursement-sequence')).toContainText(
      reimbursement.data.paymentReference!,
    );
    await stage(ids, 'paid-and-verified');
  } finally {
    await stage(ids, 'latest-resume-ids');
    await Promise.allSettled([member.close(), reviewer.close(), admin.close()]);
    await Promise.allSettled([memberPage.close(), reviewerPage.close()]);
  }
});
