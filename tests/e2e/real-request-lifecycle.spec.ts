import { createHash, randomUUID } from 'node:crypto';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';
import { syntheticPDF } from './synthetic-pdf';

type Schema<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base,
  'requires operator-started demo API and UI',
);

// Creates only new requests under the demo member's existing enrollment. It never
// changes plan/rule settings or grants, reserves rights, or edits an existing request.
test('real request rejection and cancellation are final, reasoned and safe to replay', async ({
  page,
}) => {
  test.setTimeout(90_000);
  const admin = new Actor(await apiRequest.newContext(), 'backoffice');
  const doctor = new Actor(await apiRequest.newContext(), 'backoffice');
  const provider = new Actor(await apiRequest.newContext(), 'provider');
  const createdIds: string[] = [];
  try {
    await admin.login('admin.a');
    await doctor.login('doctor.a');
    await provider.login('provider.a');
    const mine = await provider.call<Schema<'ServiceRequestPage'>>(
      'GET',
      '/api/v1/service-requests?limit=100',
    );
    const template = mine.data.items.find((r) => r.personDisplayName === 'Melis Üye');
    expect(template, 'seeded demo member request fixture').toBeTruthy();
    const catalog = await provider.call<{ items: Schema<'ServiceDefinition'>[] }>(
      'GET',
      '/api/v1/service-definitions?limit=200',
    );
    const physio = catalog.data.items.find((d) => d.code === 'PHYSIO_SESSION')!;
    expect(physio).toBeTruthy();
    const today = new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Istanbul' }).format(
      new Date(),
    );
    const accounts = async () =>
      (
        await admin.call<{ items: Schema<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${template!.personId}/entitlements?asOf=${today}`,
        )
      ).data.items;
    const before = await accounts();
    const create = async () => {
      const result = await provider.call<Schema<'ServiceRequest'>>(
        'POST',
        '/api/v1/service-requests',
        {
          personId: template!.personId,
          enrollmentId: template!.enrollmentId,
          providerOrganizationId: template!.providerOrganizationId!,
          requestType: 'PREAUTHORIZATION',
          channel: 'PROVIDER_PORTAL',
          serviceDate: today,
          items: [{ serviceDefinitionId: physio.id, requestedQuantity: '1', unitType: 'SESSION' }],
        } satisfies Schema<'CreateServiceRequest'>,
        { expected: 201 },
      );
      createdIds.push(result.data.id);
      return result;
    };
    const draft = await create();
    const path = `/api/v1/service-requests/${draft.data.id}`;
    const submitted = await provider.call<Schema<'ServiceRequest'>>(
      'POST',
      path + '/submit',
      {},
      { etag: draft.etag },
    );
    expect(submitted.data.status).toBe('PENDING_REVIEW');
    // Creating/submitting does not grant permission to make a medical decision.
    for (const command of ['reject', 'approve', 'return']) {
      await provider.call(
        'POST',
        path + '/' + command,
        { reasonCode: 'PC02_NOT_REVIEWER' },
        { etag: submitted.etag, expected: 403 },
      );
    }
    await doctor.call(
      'POST',
      path + '/reject',
      { reasonCode: '' },
      { etag: submitted.etag, expected: 422 },
    );
    await doctor.call(
      'POST',
      path + '/reject',
      { reasonCode: 'PC02_REJECTED' },
      { etag: draft.etag, expected: 412 },
    );
    const key = randomUUID();
    const reason = {
      reasonCode: 'PC02_REJECTED',
      reasonText: 'Sentetik kabul testi: tıbbi değerlendirme reddi.',
    };
    const rejected = await doctor.call<Schema<'ServiceRequest'>>('POST', path + '/reject', reason, {
      etag: submitted.etag,
      key,
    });
    expect(rejected.data.status).toBe('REJECTED');
    expect(rejected.data.rejectReasonCode).toBe(reason.reasonCode);
    expect(rejected.data.items.every((item) => item.status === 'REJECTED')).toBe(true);
    const replay = await doctor.call<Schema<'ServiceRequest'>>('POST', path + '/reject', reason, {
      etag: submitted.etag,
      key,
    });
    expect(replay).toEqual(rejected);
    for (const command of ['approve', 'return', 'reject']) {
      await doctor.call('POST', path + '/' + command, reason, {
        etag: rejected.etag,
        expected: 409,
      });
    }
    await provider.call('POST', path + '/submit', {}, { etag: rejected.etag, expected: 409 });
    const observed = await provider.call<Schema<'ServiceRequest'>>('GET', path);
    expect(observed).toEqual(rejected);

    const cancelled: string[] = [];
    for (const send of [false, true]) {
      const opened = await create();
      const target = `/api/v1/service-requests/${opened.data.id}`;
      const current = send
        ? await provider.call<Schema<'ServiceRequest'>>(
            'POST',
            target + '/submit',
            {},
            { etag: opened.etag },
          )
        : opened;
      const cancelKey = randomUUID();
      const cancelReason = {
        reasonCode: 'PC02_WITHDRAWN',
        reasonText: 'Sentetik kabul testi: sağlayıcı talebi geri çekti.',
      };
      const result = await provider.call<Schema<'ServiceRequest'>>(
        'POST',
        target + '/cancel',
        cancelReason,
        { etag: current.etag, key: cancelKey },
      );
      expect(result.data.status).toBe('CANCELLED');
      expect(result.data.items.every((item) => item.status === 'CANCELLED')).toBe(true);
      expect(
        await provider.call('POST', target + '/cancel', cancelReason, {
          etag: current.etag,
          key: cancelKey,
        }),
      ).toEqual(result);
      await provider.call('POST', target + '/cancel', cancelReason, {
        etag: result.etag,
        expected: 409,
      });
      await provider.call('POST', target + '/submit', {}, { etag: result.etag, expected: 409 });
      cancelled.push(opened.data.id);
    }
    expect(await accounts()).toEqual(before);

    await page.goto(base + '/portal/');
    await page.getByLabel(/Kullanıcı adı/).fill('provider.a');
    await page
      .getByLabel(/^Parola/)
      .fill(process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora');
    await page.getByRole('button', { name: 'Giriş yap', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Yeni talep', exact: true })).toBeVisible();
    await page.goto(base + `/portal/requests/${draft.data.id}`);
    await expect(
      page.getByRole('heading', { name: rejected.data.reference, exact: true }),
    ).toBeVisible();
    await expect(page.getByText('PC02_REJECTED', { exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: /Gönder|Kaydet/ })).toHaveCount(0);
    const uiDraft = await create();
    const uiPath = `/api/v1/service-requests/${uiDraft.data.id}`;
    await provider.call('POST', uiPath + '/submit', {}, { etag: uiDraft.etag });
    await page.goto(base + `/portal/requests/${uiDraft.data.id}`);
    await expect(
      page.getByRole('heading', { name: uiDraft.data.reference, exact: true }),
    ).toBeVisible();
    let documentId: string | undefined;
    if (process.env['E2E_REQUEST_UPLOAD'] === '1') {
      const filename = `pc02-${randomUUID().slice(0, 8)}.pdf`;
      const bytes = syntheticPDF();
      const form = page.getByTestId('document-upload-form');
      await form
        .getByLabel(/Dosya seç/)
        .setInputFiles({ name: filename, mimeType: 'application/pdf', buffer: bytes });
      await form.getByLabel(/Belge türü/).fill('INVOICE');
      const completed = page.waitForResponse(
        (r) =>
          /\/api\/v1\/documents\/[^/]+\/complete$/.test(new URL(r.url()).pathname) &&
          r.request().method() === 'POST',
      );
      await form.getByRole('button', { name: 'Belge yükle', exact: true }).click();
      const response = await completed;
      expect(response.status()).toBe(200);
      const uploaded = (await response.json()) as Schema<'Document'>;
      documentId = uploaded.id;
      // The completed upload is not a clean verdict. Only the real worker may promote it.
      expect(['SCANNING', 'CLEAN']).toContain(uploaded.scanStatus);
      const row = page
        .getByTestId('documents-table')
        .getByRole('row')
        .filter({ hasText: filename });
      await expect(row.getByText('Temiz', { exact: true })).toBeVisible({ timeout: 45_000 });
      await expect(row.getByRole('button', { name: 'İndir', exact: true })).toBeVisible();
      const stored = (
        await provider.call<Schema<'Document'>>('GET', `/api/v1/documents/${documentId}`)
      ).data;
      expect(stored.scanStatus).toBe('CLEAN');
      expect(stored.bucket).toBe('secure');
      expect(stored.sha256).toBe(createHash('sha256').update(bytes).digest('hex'));
      expect(
        stored.links.some(
          (link) =>
            link.aggregateType === 'SERVICE_REQUEST' &&
            link.aggregateId === uiDraft.data.id &&
            link.documentTypeCode === 'INVOICE',
        ),
      ).toBe(true);
      const ticket = (
        await provider.call<Schema<'DocumentDownload'>>(
          'POST',
          `/api/v1/documents/${documentId}/download`,
          {},
        )
      ).data;
      const downloaded = await page.request.get(ticket.url);
      expect(downloaded.status()).toBe(200);
      expect(await downloaded.body()).toEqual(bytes);
    }
    const attempts: { key: string | undefined; body: string | null }[] = [];
    await page.route('**' + uiPath + '/cancel', async (route) => {
      attempts.push({
        key: route.request().headers()['idempotency-key'],
        body: route.request().postData(),
      });
      if (attempts.length === 1) {
        const committed = await route.fetch();
        expect(committed.status()).toBe(200);
        await route.abort('failed'); // server committed, browser did not receive the response
      } else await route.continue();
    });
    await page.getByRole('button', { name: 'Talebi iptal et', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await expect(
      dialog.getByRole('button', { name: 'Talebi iptal et', exact: true }),
    ).toBeDisabled();
    await dialog.getByLabel(/İptal gerekçesi/).selectOption('PROVIDER_WITHDRAWN');
    await dialog.getByLabel('Açıklama', { exact: true }).fill('Sentetik kabul testi.');
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 900 });
      await page.evaluate(() => window.scrollTo(0, 0));
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      );
      await page.screenshot({
        path: `.impeccable/review/request-cancel-${width}.png`,
        fullPage: true,
      });
    }
    await dialog.getByRole('button', { name: 'Talebi iptal et', exact: true }).click();
    await expect(dialog.getByRole('alert')).toBeVisible();
    await expect(dialog.getByLabel(/İptal gerekçesi/)).toBeDisabled();
    const committed = await provider.call<Schema<'ServiceRequest'>>('GET', uiPath);
    expect(committed.data.status).toBe('CANCELLED');
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 900 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      );
      await page.screenshot({
        path: `.impeccable/review/request-cancel-error-${width}.png`,
        fullPage: true,
      });
    }
    await dialog.getByRole('button', { name: 'Yeniden dene', exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Talebi iptal et', exact: true })).toHaveCount(0);
    await expect(page.getByTestId('document-upload-form')).toHaveCount(0);
    expect(attempts).toHaveLength(2);
    expect(attempts[1]).toEqual(attempts[0]);
    expect(await provider.call('GET', uiPath)).toEqual(committed);
    expect(await accounts()).toEqual(before);
    cancelled.push(uiDraft.data.id);
    await test.info().attach('request-lifecycle-acceptance', {
      body: JSON.stringify({
        rejected: draft.data.id,
        documentId,
        browserCancellation: uiDraft.data.id,
        cancelled,
        balancesAndVersionsUnchanged: true,
      }),
      contentType: 'application/json',
    });
  } finally {
    // A failed assertion must not leave this test's undecided requests in the queue.
    try {
      for (const id of createdIds) {
        const current = await provider.call<Schema<'ServiceRequest'>>(
          'GET',
          `/api/v1/service-requests/${id}`,
        );
        if (
          ['DRAFT', 'PENDING_REVIEW', 'PENDING_DOCUMENT', 'ELIGIBILITY_FAILED'].includes(
            current.data.status,
          )
        ) {
          await provider.call(
            'POST',
            `/api/v1/service-requests/${id}/cancel`,
            { reasonCode: 'PC02_TEST_CLEANUP' },
            { etag: current.etag },
          );
        }
      }
    } finally {
      await Promise.all([admin.close(), doctor.close(), provider.close()]);
    }
  }
});
