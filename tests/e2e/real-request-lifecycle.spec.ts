import { randomUUID } from 'node:crypto';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

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
    await test.info().attach('request-lifecycle-acceptance', {
      body: JSON.stringify({
        rejected: draft.data.id,
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
