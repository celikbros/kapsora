import { randomUUID } from 'node:crypto';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type Schema<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const sourceId = process.env['E2E_STANDARD_OUTPATIENT_SOURCE_REQUEST'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || !sourceId,
  'requires the operator-started single-door demo and explicit accepted PC-02 source request',
);

test('real standard outpatient reaches invoice readiness without a treatment report', async ({
  page,
  browser,
}) => {
  test.setTimeout(180_000);
  const billingPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const admin = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const provider = new Actor(page.request, 'provider', true);
  const reviewer = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const billing = new Actor(billingPage.request, 'provider', true);
  const ids: Record<string, string> = {};
  try {
    expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
    await admin.login('admin.a');
    await provider.login('provider.a');
    await reviewer.login('doctor.a');
    await billing.login('billing.a');
    const today = new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Istanbul' }).format(
      new Date(),
    );
    const source = (
      await provider.call<Schema<'ServiceRequest'>>('GET', `/api/v1/service-requests/${sourceId}`)
    ).data;
    expect(source.id).toBe(sourceId);
    expect(source.status).toBe('APPROVED');
    expect(source.personDisplayName).toMatch(/^Deneme /);
    expect(source.providerOrganizationId).toBeTruthy();
    const program = (
      await admin.call<Schema<'Program'>>('GET', `/api/v1/programs/${source.programId}`)
    ).data;
    expect(program.code).toMatch(/^PC02_A_/);
    expect(program.name).toBe(`PC02 automatic ${program.id}`);
    expect(program.validFrom! <= today && program.validTo! >= today).toBe(true);
    expect(program.validFrom! <= source.serviceDate && program.validTo! >= source.serviceDate).toBe(
      true,
    );
    const sourceEnrollments = (
      await admin.call<{ items: Schema<'Enrollment'>[] }>(
        'GET',
        `/api/v1/people/${source.personId}/enrollments`,
      )
    ).data.items;
    const sourceEnrollment = sourceEnrollments.find((e) => e.id === source.enrollmentId);
    expect(sourceEnrollment).toBeTruthy();

    const person = (
      await admin.call<Schema<'Person'>>(
        'POST',
        '/api/v1/people',
        { firstName: 'Deneme', lastName: `Standartayaktan${randomUUID().slice(0, 8)}` },
        { expected: 201 },
      )
    ).data;
    ids['personId'] = person.id;
    const membership = (
      await admin.call<Schema<'SponsorMembership'>>(
        'POST',
        `/api/v1/people/${person.id}/memberships`,
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
        `/api/v1/people/${person.id}/enrollments`,
        {
          sponsorMembershipId: membership.id,
          planId: sourceEnrollment!.planId,
          validFrom: today,
          enrollmentReason: 'PC03_STANDARD_OUTPATIENT_ACCEPTANCE',
        },
        { expected: 201 },
      )
    ).data;
    ids['enrollmentId'] = enrollment.id;
    const accounts = async () =>
      (
        await admin.call<{ items: Schema<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${person.id}/entitlements?asOf=${today}`,
        )
      ).data.items;
    const snapshot = async (want: number[]) => {
      const rows = await accounts();
      expect(rows).toHaveLength(1);
      const account = rows[0]!;
      ids['accountId'] = account.id;
      expect([account.available, account.reserved, account.consumed]).toEqual(want);
      expect(account.available + account.reserved + account.consumed + account.expired).toBe(
        account.totalGranted,
      );
      return rows;
    };
    await expect.poll(async () => (await accounts()).length, { timeout: 30_000 }).toBe(1);
    await snapshot([20, 0, 0]);
    const services = (
      await provider.call<{ items: Schema<'ServiceDefinition'>[] }>(
        'GET',
        '/api/v1/service-definitions?limit=200',
      )
    ).data.items;
    const service = services.find((s) => s.code === 'PHYSIO_SESSION');
    expect(service).toBeTruthy();
    const draft = await provider.call<Schema<'ServiceRequest'>>(
      'POST',
      '/api/v1/service-requests',
      {
        personId: person.id,
        enrollmentId: enrollment.id,
        providerOrganizationId: source.providerOrganizationId!,
        requestType: 'PREAUTHORIZATION',
        channel: 'PROVIDER_PORTAL',
        serviceDate: today,
        items: [{ serviceDefinitionId: service!.id, requestedQuantity: '1', unitType: 'SESSION' }],
      } satisfies Schema<'CreateServiceRequest'>,
      { expected: 201 },
    );
    ids['requestId'] = draft.data.id;
    const requestPath = `/api/v1/service-requests/${draft.data.id}`;
    const pending = await provider.call<Schema<'ServiceRequest'>>(
      'POST',
      requestPath + '/submit',
      {},
      { etag: draft.etag },
    );
    expect(pending.data.status).toBe('PENDING_REVIEW');
    await reviewer.call(
      'POST',
      requestPath + '/approve',
      { reasonCode: 'PC03_STANDARD_TEST_APPROVAL' },
      { etag: pending.etag },
    );
    const authorization = await reviewer.call<Schema<'Authorization'>>(
      'POST',
      '/api/v1/authorizations',
      { requestId: draft.data.id, validTo: new Date(Date.now() + 86400000).toISOString() },
      { expected: 201 },
    );
    ids['authorizationId'] = authorization.data.id;
    await snapshot([19, 1, 0]);

    await page.goto(base + `/portal/requests/${draft.data.id}`);
    await page.getByRole('link', { name: 'Vaka aç', exact: true }).click();
    const opened = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === '/api/v1/health-cases' && r.request().method() === 'POST',
    );
    await page.getByRole('button', { name: 'Vakayı aç', exact: true }).click();
    const openedResponse = await opened;
    expect(openedResponse.status()).toBe(201);
    const healthCase = (await openedResponse.json()) as Schema<'HealthCase'>;
    ids['caseId'] = healthCase.id;
    expect(healthCase).toMatchObject({
      personId: person.id,
      enrollmentId: enrollment.id,
      programId: program.id,
      providerOrganizationId: source.providerOrganizationId,
      serviceRequestId: draft.data.id,
      caseType: 'OUTPATIENT',
    });
    await expect(page.getByTestId('case-claims-card')).toContainText(
      'Claim kayıtlarını görmek için ek yetki gerekir.',
    );
    await expect(page.getByRole('link', { name: 'Yeni claim' })).toHaveCount(0);
    await page.getByRole('button', { name: 'Muayene ekle', exact: true }).click();
    const encounterForm = page.getByTestId('encounter-form');
    await encounterForm
      .locator('[name="endedAt"]')
      .fill(await encounterForm.locator('[name="startedAt"]').inputValue());
    const clinicalMarker = `Synthetic standard outpatient ${randomUUID()}`;
    await encounterForm.locator('[name="notesClinical"]').fill(clinicalMarker);
    const examined = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === `/api/v1/health-cases/${healthCase.id}/encounters` &&
        r.request().method() === 'POST',
    );
    await encounterForm.getByRole('button', { name: 'Muayeneyi kaydet', exact: true }).click();
    const examinedResponse = await examined;
    expect(examinedResponse.status()).toBe(201);
    const encounter = (await examinedResponse.json()) as Schema<'Encounter'>;
    ids['encounterId'] = encounter.id;
    await page.getByRole('button', { name: 'Tanıları düzenle', exact: true }).click();
    await page.locator('[name="icd10"]').fill('J06.9');
    await page
      .getByTestId('icd10-matches')
      .getByRole('button')
      .filter({ hasText: 'J06.9' })
      .click();
    const diagnosed = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === `/api/v1/encounters/${encounter.id}/diagnoses` &&
        r.request().method() === 'PUT',
    );
    await page.getByRole('button', { name: 'Tanıları kaydet', exact: true }).click();
    const diagnosedResponse = await diagnosed;
    expect(diagnosedResponse.status()).toBe(200);
    const diagnoses = ((await diagnosedResponse.json()) as { items: Schema<'Diagnosis'>[] }).items;
    expect(diagnoses).toHaveLength(1);
    expect(diagnoses[0]).toMatchObject({
      code: 'J06.9',
      diagnosisType: 'PRIMARY',
      sensitive: false,
    });
    await page.getByRole('button', { name: 'Vakayı kapat', exact: true }).click();
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Vakayı kapat', exact: true })
      .click();
    await expect(page.getByTestId('case-status')).toHaveText('Kapandı');
    expect(
      (
        await provider.call<Schema<'MedicalReportPage'>>(
          'GET',
          `/api/v1/medical-reports?caseId=${healthCase.id}`,
        )
      ).data.items,
    ).toHaveLength(0);

    await billingPage.goto(base + '/portal/claims');
    await billingPage.locator('a[href="/portal/claims/new"]').click();
    const sourcePath = `/api/v1/claims/case-sources/${healthCase.id}`;
    const sourceLoaded = billingPage.waitForResponse(
      (r) => new URL(r.url()).pathname === sourcePath && r.request().method() === 'GET',
    );
    await billingPage
      .getByLabel('Faturalandırılacak vaka', { exact: true })
      .selectOption(healthCase.id);
    const sourceResponse = await sourceLoaded;
    expect(sourceResponse.status()).toBe(200);
    const sourceBody = (await sourceResponse.json()) as Schema<'ClaimCaseSourceDetail'>;
    expect(sourceBody.source.caseId).toBe(healthCase.id);
    expect(sourceBody.lines).toHaveLength(1);
    expect(sourceBody.lines[0]!.serviceDefinitionId).toBe(service!.id);
    expect(JSON.stringify(sourceBody)).not.toContain(diagnoses[0]!.id);
    expect(JSON.stringify(sourceBody)).not.toContain(clinicalMarker);
    await provider.call('GET', sourcePath, undefined, { expected: 403 });
    const form = billingPage.getByTestId('case-claim-form');
    await form.getByLabel(/Talep edilen tutar/).fill('400');
    const created = billingPage.waitForResponse(
      (r) => new URL(r.url()).pathname === sourcePath && r.request().method() === 'POST',
    );
    await form.getByRole('button', { name: 'Taslağı oluştur', exact: true }).click();
    const createdResponse = await created;
    expect(createdResponse.status()).toBe(201);
    const claim = {
      data: (await createdResponse.json()) as Schema<'Claim'>,
      etag: createdResponse.headers()['etag']!,
    };
    ids['claimId'] = claim.data.id;
    const createBody = createdResponse.request().postDataJSON() as Schema<'CreateClaimFromCase'>;
    const createHeaders = createdResponse.request().headers();
    expect(createBody).toEqual({
      lines: [
        {
          serviceDefinitionId: service!.id,
          quantity: sourceBody.lines[0]!.quantity,
          lineAmount: '400',
        },
      ],
    });
    expect(
      await billing.call<Schema<'Claim'>>('POST', sourcePath, createBody, {
        expected: 201,
        etag: createHeaders['if-match']!,
        key: createHeaders['idempotency-key']!,
      }),
    ).toEqual(claim);
    await billing.call('POST', sourcePath, createBody, {
      expected: 409,
      etag: createHeaders['if-match']!,
    });
    const claimPath = `/api/v1/claims/${claim.data.id}`;
    const beforeSubmit = await billing.call<Schema<'Claim'>>('GET', claimPath);
    const sent = billingPage.waitForResponse(
      (r) => new URL(r.url()).pathname === claimPath + '/submit' && r.request().method() === 'POST',
    );
    await billingPage.getByRole('button', { name: 'Gönder', exact: true }).click();
    const sentResponse = await sent;
    expect(sentResponse.status()).toBe(200);
    const submitted = {
      data: (await sentResponse.json()) as Schema<'Claim'>,
      etag: sentResponse.headers()['etag']!,
    };
    expect(submitted.data.status).toBe('APPROVED');
    const submitBody: unknown = sentResponse.request().postData()
      ? sentResponse.request().postDataJSON()
      : undefined;
    const after = await snapshot([19, 0, 1]);
    expect(
      await billing.call<Schema<'Claim'>>('POST', claimPath + '/submit', submitBody, {
        etag: beforeSubmit.etag,
        key: sentResponse.request().headers()['idempotency-key']!,
      }),
    ).toEqual(submitted);
    expect(await accounts()).toEqual(after);
    const clinicalClaim = (await reviewer.call<Schema<'Claim'>>('GET', claimPath)).data;
    expect(clinicalClaim.projection).toBe('CLINICAL');
    expect(clinicalClaim.lines).toHaveLength(1);
    expect(clinicalClaim.lines[0]).toMatchObject({ diagnosisId: diagnoses[0]!.id });
    expect(clinicalClaim.lines[0]!.medicalReportId ?? null).toBeNull();
    expect(submitted.data.projection).toBe('FINANCIAL');
    expect(submitted.data.lines[0]!.diagnosisId).toBeUndefined();
    expect(submitted.data.lines[0]!.medicalReportId).toBeUndefined();
    const decision = submitted.data.lines[0]!.decision!;
    expect(decision).toMatchObject({
      stage: 'AUTO',
      decision: 'APPROVED',
      reasonCode: 'AUTO_APPROVED',
    });
    expect(
      [
        decision.approvedQuantity,
        decision.contractAmount,
        decision.approvedAmount,
        decision.payerAmount,
        decision.memberAmount,
      ].map(Number),
    ).toEqual([1, 400, 400, 400, 0]);
    const readiness = (
      await billing.call<Schema<'ClaimInvoiceReadiness'>>('GET', claimPath + '/invoice-readiness')
    ).data;
    expect(readiness).toMatchObject({
      ready: true,
      blockers: [],
      currencyCode: 'TRY',
      lineCount: 1,
      decidedLineCount: 1,
    });
    expect(
      [readiness.approvedTotal, readiness.payerTotal, readiness.memberTotal].map(Number),
    ).toEqual([400, 400, 0]);
    await billingPage.goto(base + `/portal/claims/${claim.data.id}`);
    await expect(billingPage.getByTestId('claim-status')).toHaveText('Onaylandı');
    await expect(billingPage.getByTestId('readiness-verdict')).toContainText('hazır');
    expect(await billingPage.locator('body').innerText()).not.toContain(clinicalMarker);
    const ledger = (
      await admin.call<Schema<'LedgerPage'>>(
        'GET',
        `/api/v1/entitlement-accounts/${ids['accountId']}/ledger?limit=100`,
      )
    ).data.items;
    expect(ledger.map((row) => row.movementType).sort()).toEqual(['CONSUME', 'GRANT', 'RESERVE']);
    await test.info().attach('standard-outpatient-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        ...ids,
        sourceRequestId: sourceId,
        reportCreated: false,
        claimStatus: 'APPROVED',
        available: 19,
        reserved: 0,
        consumed: 1,
        approvedTotal: '400',
        payerTotal: '400',
        memberTotal: '0',
        invoiceReady: true,
      }),
    });
  } finally {
    await test.info().attach('standard-outpatient-fixture-ids', {
      contentType: 'application/json',
      body: JSON.stringify(ids),
    });
    try {
      if (ids['claimId']) {
        const path = `/api/v1/claims/${ids['claimId']}`;
        const current = await billing.call<Schema<'Claim'>>('GET', path);
        if (
          ['DRAFT', 'PENDING_MEDICAL', 'PENDING_FINANCIAL', 'RETURNED'].includes(
            current.data.status,
          )
        )
          await billing.call(
            'POST',
            path + '/cancel',
            { reasonCode: 'PC03_TEST_CLEANUP' },
            { etag: current.etag },
          );
      }
      if (ids['authorizationId']) {
        const path = `/api/v1/authorizations/${ids['authorizationId']}`;
        const current = await reviewer.call<Schema<'Authorization'>>('GET', path);
        if (current.data.status === 'ACTIVE' || current.data.status === 'PARTIALLY_USED')
          await reviewer.call(
            'POST',
            path + '/cancel',
            { reasonCode: 'PC03_TEST_CLEANUP' },
            { etag: current.etag },
          );
      }
    } finally {
      await Promise.all([admin.close(), provider.close(), reviewer.close(), billing.close()]);
      await billingPage.close();
    }
  }
});
