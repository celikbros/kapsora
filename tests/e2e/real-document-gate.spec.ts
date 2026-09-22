import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { createHash } from 'node:crypto';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';
import { syntheticPDF } from './synthetic-pdf';

type Schema<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || process.env['E2E_DOCUMENT_GATE'] !== '1',
  'requires operator-started local demo, loaded .env and explicit document fixture opt-in',
);
const execute = promisify(execFile);
async function seedRule(personId: string, retire = false) {
  const { stdout } = await execute(
    'go',
    ['run', './cmd/seed', 'document-rule', personId, ...(retire ? ['retire'] : [])],
    { timeout: 60_000, windowsHide: true },
  );
  return JSON.parse(stdout) as { personId: string; versionId: string; status: string };
}

test('real scoped document rule blocks missing/unscanned evidence and accepts a clean browser upload on resubmission', async ({
  page,
}) => {
  test.setTimeout(150_000);
  const admin = new Actor(await apiRequest.newContext(), 'backoffice');
  const doctor = new Actor(await apiRequest.newContext(), 'backoffice');
  const provider = new Actor(await apiRequest.newContext(), 'provider');
  const requestIds: string[] = [];
  let personId: string | undefined;
  let ruleAttempted = false;
  let rulePublished = false;
  let releaseUpload = () => {};
  try {
    await admin.login('admin.a');
    await doctor.login('doctor.a');
    await provider.login('provider.a');
    const today = new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Istanbul' }).format(
      new Date(),
    );
    const programs = (await admin.call<Schema<'ProgramPage'>>('GET', '/api/v1/programs?limit=100'))
      .data.items;
    const program = programs.find((p) => p.code === 'DEMO_BENEFIT')!;
    const plans = (
      await admin.call<{ items: Schema<'Plan'>[] }>('GET', `/api/v1/programs/${program.id}/plans`)
    ).data.items;
    const plan = plans.find((p) => p.code === 'DEMO_STANDARD' && p.status === 'ACTIVE')!;
    const created = await admin.call<Schema<'Person'>>(
      'POST',
      '/api/v1/people',
      { firstName: 'Deneme', lastName: 'BelgekuralHazirlik' },
      { expected: 201 },
    );
    personId = created.data.id;
    await admin.call(
      'PATCH',
      `/api/v1/people/${personId}`,
      { lastName: `Belgekural${personId.replaceAll('-', '')}` },
      { etag: created.etag },
    );
    const membership = (
      await admin.call<Schema<'SponsorMembership'>>(
        'POST',
        `/api/v1/people/${personId}/memberships`,
        {
          sponsorOrganizationId: program.sponsorOrganizationId,
          membershipType: 'EMPLOYEE',
          validFrom: today,
        },
        { expected: 201 },
      )
    ).data;
    const enrollment = (
      await admin.call<Schema<'Enrollment'>>(
        'POST',
        `/api/v1/people/${personId}/enrollments`,
        {
          sponsorMembershipId: membership.id,
          planId: plan.id,
          validFrom: today,
          enrollmentReason: 'PC02_DOCUMENT_GATE_ACCEPTANCE',
        },
        { expected: 201 },
      )
    ).data;
    const accounts = async () =>
      (
        await admin.call<{ items: Schema<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${personId}/entitlements?asOf=${today}`,
        )
      ).data.items;
    await expect
      .poll(async () => (await accounts()).some((a) => a.definition.code === 'PHYSIO_SESSION'), {
        timeout: 30_000,
      })
      .toBe(true);
    const before = await accounts();
    const catalog = (
      await provider.call<{ items: Schema<'ServiceDefinition'>[] }>(
        'GET',
        '/api/v1/service-definitions?limit=200',
      )
    ).data.items;
    const definition = catalog.find((d) => d.code === 'PHYSIO_SESSION')!;
    const requests = (
      await provider.call<Schema<'ServiceRequestPage'>>('GET', '/api/v1/service-requests?limit=100')
    ).data.items;
    const control = requests.find((r) => r.personDisplayName === 'Melis Üye')!;
    expect(control).toBeTruthy();
    const makeRequest = async (person: string, enrollmentId: string) => {
      const draft = await provider.call<Schema<'ServiceRequest'>>(
        'POST',
        '/api/v1/service-requests',
        {
          personId: person,
          enrollmentId,
          providerOrganizationId: control.providerOrganizationId!,
          requestType: 'PREAUTHORIZATION',
          channel: 'PROVIDER_PORTAL',
          serviceDate: today,
          items: [
            { serviceDefinitionId: definition.id, requestedQuantity: '1', unitType: 'SESSION' },
          ],
        } satisfies Schema<'CreateServiceRequest'>,
        { expected: 201 },
      );
      requestIds.push(draft.data.id);
      return provider.call<Schema<'ServiceRequest'>>(
        'POST',
        `/api/v1/service-requests/${draft.data.id}/submit`,
        {},
        { etag: draft.etag },
      );
    };
    ruleAttempted = true;
    const rule = await seedRule(personId);
    rulePublished = rule.status === 'PUBLISHED';
    expect(rule.status).toBe('PUBLISHED');
    expect(await seedRule(personId)).toEqual(rule);
    const other = await makeRequest(control.personId, control.enrollmentId);
    expect(other.data.status).toBe('PENDING_REVIEW'); // same tenant, different person: unchanged
    const missing = await makeRequest(personId, enrollment.id);
    expect(missing.data.status).toBe('PENDING_DOCUMENT');
    expect(missing.data.requiredDocumentTypes).toEqual(['INVOICE']);
    const path = `/api/v1/service-requests/${missing.data.id}`;
    const initialVersion = (
      await provider.call<Schema<'ServiceRequestVersion'>>('GET', path + '/versions/1')
    ).data;
    await doctor.call(
      'POST',
      path + '/approve',
      { reasonCode: 'PC02_NO_BYPASS' },
      { etag: missing.etag, expected: 409 },
    );

    await page.goto(base + `/portal/requests/${missing.data.id}`);
    await page.getByLabel(/Kullanıcı adı/).fill('provider.a');
    await page
      .getByLabel(/^Parola/)
      .fill(process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora');
    await page.getByRole('button', { name: 'Giriş yap', exact: true }).click();
    await expect(
      page.getByRole('heading', { name: missing.data.reference, exact: true }),
    ).toBeVisible();
    // Pause only the browser's complete-upload command, after its real PUT to MinIO.
    // This gives a deterministic unscanned object without inventing a scan result.
    let heldDocument = (_id: string) => {};
    const held = new Promise<string>((resolve) => {
      heldDocument = resolve;
    });
    const resume = new Promise<void>((resolve) => {
      releaseUpload = resolve;
    });
    await page.route('**/api/v1/documents/*/complete', async (route) => {
      heldDocument(new URL(route.request().url()).pathname.split('/')[4]!);
      await resume;
      await route.continue();
    });
    const bytes = syntheticPDF();
    const filename = `gate-${personId.slice(0, 8)}.pdf`;
    const form = page.getByTestId('document-upload-form');
    await form
      .getByLabel(/Dosya seç/)
      .setInputFiles({ name: filename, mimeType: 'application/pdf', buffer: bytes });
    await form.getByLabel(/Belge türü/).selectOption('INVOICE');
    await form.getByRole('button', { name: 'Belge yükle', exact: true }).click();
    const documentId = await held;
    const unscanned = (
      await provider.call<Schema<'Document'>>('GET', `/api/v1/documents/${documentId}`)
    ).data;
    expect(unscanned.scanStatus).toBe('PENDING');
    expect(unscanned.bucket).toBe('quarantine');
    const refusal = await provider.call<{ code: string }>(
      'POST',
      `/api/v1/documents/${documentId}/download`,
      {},
      { expected: 409 },
    );
    expect(refusal.data.code).toBe('DOCUMENT_NOT_SCANNED');
    const link = (
      await provider.call<Schema<'DocumentLink'>>(
        'POST',
        `/api/v1/documents/${documentId}/links`,
        {
          aggregateType: 'SERVICE_REQUEST',
          aggregateId: missing.data.id,
          documentTypeCode: 'INVOICE',
        },
        { expected: 201 },
      )
    ).data;
    const returned = await doctor.call<Schema<'ServiceRequest'>>(
      'POST',
      path + '/return',
      {
        reasonCode: 'PC02_DOCUMENT_RECHECK',
        reasonText: 'Belgenin tarama sonucunu kontrol ediniz.',
      },
      { etag: missing.etag },
    );
    const stillMissing = await provider.call<Schema<'ServiceRequest'>>(
      'POST',
      path + '/submit',
      {},
      { etag: returned.etag },
    );
    expect(stillMissing.data.status).toBe('PENDING_DOCUMENT');
    expect(stillMissing.data.currentVersionNo).toBe(2);
    await provider.call('DELETE', `/api/v1/documents/${documentId}/links/${link.id}`, undefined, {
      expected: 204,
    });
    releaseUpload();
    const row = page.getByTestId('documents-table').getByRole('row').filter({ hasText: filename });
    await expect(row.getByText('Temiz', { exact: true })).toBeVisible({ timeout: 45_000 });
    const clean = (
      await provider.call<Schema<'Document'>>('GET', `/api/v1/documents/${documentId}`)
    ).data;
    expect(clean.scanStatus).toBe('CLEAN');
    expect(clean.bucket).toBe('secure');
    expect(clean.sha256).toBe(createHash('sha256').update(bytes).digest('hex'));
    expect((await provider.call<Schema<'ServiceRequest'>>('GET', path)).data.status).toBe(
      'PENDING_DOCUMENT',
    );
    await doctor.call(
      'POST',
      path + '/return',
      {
        reasonCode: 'PC02_DOCUMENT_COMPLETED',
        reasonText: 'Temiz belge eklendi. Talebi yeniden gönderiniz.',
      },
      { etag: stillMissing.etag },
    );
    await page.reload();
    const correction = page.getByTestId('request-correction');
    await expect(correction).toBeVisible();
    const sent = page.waitForResponse(
      (response) => new URL(response.url()).pathname === path + '/submit',
    );
    await correction.getByRole('button', { name: 'Talebi gönder', exact: true }).click();
    const sentResponse = await sent;
    expect(sentResponse.status()).toBe(200);
    const accepted = (await sentResponse.json()) as Schema<'ServiceRequest'>;
    expect(accepted.status).toBe('PENDING_REVIEW');
    expect(accepted.currentVersionNo).toBe(3);
    expect(accepted.requiredDocumentTypes).toEqual(['INVOICE']);
    const prior = (
      await provider.call<Schema<'ServiceRequestVersion'>>('GET', path + '/versions/1')
    ).data;
    expect(prior.items).toEqual(initialVersion.items);
    expect(prior.submittedAt).toBe(initialVersion.submittedAt);
    expect(await accounts()).toEqual(before);
    await test.info().attach('document-gate-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        personId,
        requestId: missing.data.id,
        ruleVersionId: rule.versionId,
        documentId,
        controlRequestId: other.data.id,
        states: ['PENDING_DOCUMENT', 'PENDING_DOCUMENT', 'PENDING_REVIEW'],
        ledgerAccountsUnchanged: true,
      }),
    });
  } finally {
    releaseUpload();
    try {
      for (const id of requestIds) {
        const current = await provider.call<Schema<'ServiceRequest'>>(
          'GET',
          `/api/v1/service-requests/${id}`,
        );
        if (
          [
            'DRAFT',
            'SUBMITTED',
            'PENDING_REVIEW',
            'PENDING_DOCUMENT',
            'ELIGIBILITY_FAILED',
          ].includes(current.data.status)
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
      try {
        if (ruleAttempted && personId) {
          const cleanup = await seedRule(personId, true);
          if (rulePublished) expect(cleanup.status).toBe('RETIRED');
          else expect(cleanup.status).not.toBe('PUBLISHED');
        }
      } finally {
        await Promise.all([admin.close(), doctor.close(), provider.close()]);
      }
    }
  }
});
