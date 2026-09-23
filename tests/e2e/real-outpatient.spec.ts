import { randomUUID } from 'node:crypto';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';
import { reviewAndCorrectClaim, seedClaimReviewRule } from './real-claim-review-flow';
import { correctApprovedReport } from './real-report-correction-flow';
import { syntheticPDF } from './synthetic-pdf';

type Schema<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const reportMode = process.env['E2E_REPORT_CORRECTION'] === '1';
const reviewMode = process.env['E2E_CLAIM_REVIEW'] === '1';
const sourceId = process.env['E2E_OUTPATIENT_SOURCE_REQUEST'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || !sourceId,
  'requires the operator-started demo and an explicit synthetic automatic-program source request',
);

test('real outpatient case, diagnosis and scanned report reach a priced claim with one consumption', async ({
  page,
  browser,
}) => {
  test.setTimeout(reviewMode || reportMode ? 240_000 : 120_000);
  const doctorPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const billingPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const financePage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const finance = new Actor(financePage.request, 'backoffice', reviewMode || reportMode);
  let ruleAttempted = false;
  const admin = new Actor(await apiRequest.newContext(), 'backoffice', reviewMode || reportMode);
  const provider = new Actor(page.request, 'provider', reviewMode || reportMode);
  const doctor = new Actor(doctorPage.request, 'backoffice', reviewMode || reportMode);
  const billing = new Actor(billingPage.request, 'provider', reviewMode || reportMode);
  const ids: Record<string, string> = {};
  try {
    await admin.login('admin.a');
    await provider.login('provider.a');
    await doctor.login('doctor.a');
    await billing.login('billing.a');
    if (reviewMode) await finance.login('financial.reviewer');
    const today = new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Istanbul' }).format(
      new Date(),
    );
    const source = (
      await provider.call<Schema<'ServiceRequest'>>('GET', `/api/v1/service-requests/${sourceId}`)
    ).data;
    expect(source.status).toBe('APPROVED');
    const program = (
      await admin.call<Schema<'Program'>>('GET', `/api/v1/programs/${source.programId}`)
    ).data;
    expect(program.code).toMatch(/^PC02_A_/);
    expect(program.name).toBe(`PC02 automatic ${program.id}`);
    expect(program.validTo! >= today).toBe(true);
    const enrollments = (
      await admin.call<{ items: Schema<'Enrollment'>[] }>(
        'GET',
        `/api/v1/people/${source.personId}/enrollments`,
      )
    ).data.items;
    const sourceEnrollment = enrollments.find((e) => e.id === source.enrollmentId)!;
    expect(sourceEnrollment).toBeTruthy();
    const person = (
      await admin.call<Schema<'Person'>>(
        'POST',
        '/api/v1/people',
        { firstName: 'Deneme', lastName: 'Ayaktan' },
        { expected: 201 },
      )
    ).data;
    ids['personId'] = person.id;
    if (reviewMode) {
      const current = await admin.call<Schema<'Person'>>('GET', `/api/v1/people/${person.id}`);
      await admin.call(
        'PATCH',
        `/api/v1/people/${person.id}`,
        { lastName: `Hasarinceleme${person.id.replaceAll('-', '')}` },
        { etag: current.etag },
      );
      ruleAttempted = true;
      ids['reviewRuleVersionId'] = (await seedClaimReviewRule(person.id)).versionId;
    }
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
          planId: sourceEnrollment.planId,
          validFrom: today,
          enrollmentReason: 'PC03_OUTPATIENT_ACCEPTANCE',
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
    await expect.poll(async () => (await accounts()).length, { timeout: 30_000 }).toBe(1);
    const snapshot = async (expected: number[]) => {
      const rows = await accounts();
      expect(rows).toHaveLength(1);
      const account = rows[0]!;
      expect([account.available, account.reserved, account.consumed]).toEqual(expected);
      expect(account.available + account.reserved + account.consumed + account.expired).toBe(
        account.totalGranted,
      );
      ids['accountId'] = account.id;
      return rows;
    };
    await snapshot([20, 0, 0]);
    const services = (
      await provider.call<{ items: Schema<'ServiceDefinition'>[] }>(
        'GET',
        '/api/v1/service-definitions?limit=200',
      )
    ).data.items;
    const service = services.find((s) => s.code === 'PHYSIO_SESSION')!;
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
        items: [{ serviceDefinitionId: service.id, requestedQuantity: '1', unitType: 'SESSION' }],
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
    await doctor.call(
      'POST',
      requestPath + '/approve',
      { reasonCode: 'PC03_TEST_APPROVAL' },
      { etag: pending.etag },
    );
    const authorization = await doctor.call<Schema<'Authorization'>>(
      'POST',
      '/api/v1/authorizations',
      {
        requestId: draft.data.id,
        validTo: new Date(Date.now() + 86400000).toISOString(),
      },
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
    const caseResponse = await opened;
    expect(caseResponse.status()).toBe(201);
    const healthCase = (await caseResponse.json()) as Schema<'HealthCase'>;
    ids['caseId'] = healthCase.id;
    expect(healthCase).toMatchObject({
      personId: person.id,
      enrollmentId: enrollment.id,
      programId: program.id,
      providerOrganizationId: source.providerOrganizationId,
      serviceRequestId: draft.data.id,
      caseType: 'OUTPATIENT',
    });
    await expect(page.getByTestId('case-status')).toHaveText('Açık');
    await page.getByRole('button', { name: 'Muayene ekle', exact: true }).click();
    const encounterForm = page.getByTestId('encounter-form');
    await encounterForm
      .locator('[name="endedAt"]')
      .fill(await encounterForm.locator('[name="startedAt"]').inputValue());
    const clinicalMarker = `Synthetic outpatient ${randomUUID()}`;
    await encounterForm.locator('[name="notesClinical"]').fill(clinicalMarker);
    const examined = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === `/api/v1/health-cases/${healthCase.id}/encounters` &&
        r.request().method() === 'POST',
    );
    await encounterForm.getByRole('button', { name: 'Muayeneyi kaydet', exact: true }).click();
    const encounterResponse = await examined;
    expect(encounterResponse.status()).toBe(201);
    const encounter = (await encounterResponse.json()) as Schema<'Encounter'>;
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
    const diagnosisResponse = await diagnosed;
    expect(diagnosisResponse.status()).toBe(200);
    const diagnoses = ((await diagnosisResponse.json()) as { items: Schema<'Diagnosis'>[] }).items;
    expect(diagnoses).toHaveLength(1);
    expect(diagnoses[0]).toMatchObject({
      code: 'J06.9',
      diagnosisType: 'PRIMARY',
      sensitive: false,
    });

    await page.getByRole('button', { name: 'Rapor oluştur', exact: true }).click();
    const reportForm = page.getByTestId('report-create-form');
    await reportForm.locator('[name="reportType"]').fill('FIZIK_TEDAVI');
    await reportForm.locator('[name="validFrom"]').fill(today);
    await reportForm.locator('[name="validTo"]').fill(today);
    const reportCreated = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === '/api/v1/medical-reports' && r.request().method() === 'POST',
    );
    await reportForm.getByRole('button', { name: 'Rapor taslağını aç', exact: true }).click();
    const reportResponse = await reportCreated;
    expect(reportResponse.status()).toBe(201);
    const report = (await reportResponse.json()) as Schema<'MedicalReport'>;
    ids['reportId'] = report.id;
    expect(report.caseId).toBe(healthCase.id);
    const reportPath = `/api/v1/medical-reports/${report.id}`;
    await expect(page.getByTestId('report-status')).toHaveText('Taslak');
    const header = page.getByTestId('report-header-form');
    await header.locator('[name="clinicalSummary"]').fill(clinicalMarker);
    const savedHeader = page.waitForResponse(
      (r) => new URL(r.url()).pathname === reportPath && r.request().method() === 'PATCH',
    );
    await header.getByRole('button', { name: 'Kaydet', exact: true }).click();
    expect((await savedHeader).status()).toBe(200);
    await page.getByRole('button', { name: 'Hizmet ekle', exact: true }).click();
    await page.locator('[name="services.0.serviceDefinitionId"]').selectOption(service.id);
    await page.locator('[name="services.0.coveredQuantity"]').fill('1');
    const savedServices = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === reportPath + '/services' && r.request().method() === 'PUT',
    );
    await page.getByRole('button', { name: 'Hizmetleri kaydet', exact: true }).click();
    expect((await savedServices).status()).toBe(200);
    const currentReport = await provider.call<Schema<'MedicalReport'>>('GET', reportPath);
    const refused = await provider.call<{ code: string }>(
      'POST',
      reportPath + '/submit',
      {},
      { etag: currentReport.etag, expected: 422 },
    );
    expect(refused.data.code).toBe('MEDICAL_REPORT_DOCUMENT_REQUIRED');
    const upload = page.getByTestId('document-upload-form');
    const filename = `outpatient-${report.id.slice(-8)}.pdf`;
    await upload
      .getByLabel(/Dosya seç/)
      .setInputFiles({ name: filename, mimeType: 'application/pdf', buffer: syntheticPDF() });
    await upload.getByLabel(/Belge türü/).selectOption('MEDICAL_REPORT');
    await upload.getByLabel(/Gizlilik/).selectOption('HEALTH');
    const uploadStarted = page.waitForResponse(
      (r) => new URL(r.url()).pathname === '/api/v1/documents' && r.request().method() === 'POST',
    );
    await upload.getByRole('button', { name: 'Belge yükle', exact: true }).click();
    expect(
      (await uploadStarted).status(),
      'document upload reservation; verify MinIO is running',
    ).toBe(201);
    await expect(
      page
        .getByTestId('documents-table')
        .getByRole('row')
        .filter({ hasText: filename })
        .getByText('Temiz', { exact: true }),
    ).toBeVisible({ timeout: 45_000 });
    await page.getByRole('button', { name: 'Gönder', exact: true }).click();
    await expect(page.getByTestId('report-status')).toHaveText('Gönderildi');
    const queue = (
      await doctor.call<Schema<'WorkItemPage'>>(
        'GET',
        `/api/v1/work-items?aggregateType=MEDICAL_REPORT&aggregateId=${report.id}`,
      )
    ).data.items;
    expect(queue).toHaveLength(1);
    const item = queue[0]!;
    ids['workItemId'] = item.id;
    await doctor.call(
      'POST',
      `/api/v1/work-items/${item.id}/claim`,
      {},
      { etag: `"${item.rowVersion}"` },
    );
    await doctorPage.goto(base + `/medical-reports/${report.id}`);
    await expect(doctorPage.getByTestId('report-summary')).toHaveText(clinicalMarker);
    await doctorPage
      .getByTestId('report-commands')
      .getByRole('button', { name: 'Onayla', exact: true })
      .click();
    await doctorPage
      .getByRole('dialog')
      .getByRole('button', { name: 'Onayla', exact: true })
      .click();
    await expect(doctorPage.getByTestId('report-status')).toHaveText('Onaylandı');
    const held = await doctor.call<Schema<'WorkItem'>>('GET', `/api/v1/work-items/${item.id}`);
    await doctor.call(
      'POST',
      `/api/v1/work-items/${item.id}/complete`,
      { outcomeCode: 'APPROVED' },
      { etag: held.etag },
    );
    await page.reload();
    await expect(page.getByTestId('report-status')).toHaveText('Onaylandı');
    await snapshot([19, 1, 0]);

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
    expect(JSON.stringify(sourceBody)).not.toContain(report.id);
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
          serviceDefinitionId: service.id,
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
    await expect(billingPage.getByTestId('claim-status')).toHaveText('Taslak');
    const claimPath = `/api/v1/claims/${claim.data.id}`;
    // Saving the financial projection must not erase the hidden diagnosis/report links.
    const saved = billingPage.waitForResponse(
      (r) => new URL(r.url()).pathname === claimPath + '/lines' && r.request().method() === 'PUT',
    );
    await billingPage.locator('[name="lines.0.lineAmount"]').fill('400');
    await billingPage.getByRole('button', { name: 'Satırları kaydet', exact: true }).click();
    expect((await saved).status()).toBe(200);
    const beforeSubmit = await billing.call<Schema<'Claim'>>('GET', claimPath);
    const sent = billingPage.waitForResponse(
      (r) => new URL(r.url()).pathname === claimPath + '/submit' && r.request().method() === 'POST',
    );
    await billingPage.getByRole('button', { name: 'Gönder', exact: true }).click();
    const sentResponse = await sent;
    expect(sentResponse.status()).toBe(200);
    let submitted = {
      data: (await sentResponse.json()) as Schema<'Claim'>,
      etag: sentResponse.headers()['etag']!,
    };
    const submitKey = sentResponse.request().headers()['idempotency-key']!;
    const submitBody: unknown = sentResponse.request().postData()
      ? sentResponse.request().postDataJSON()
      : undefined;
    expect(submitted.data.status).toBe(reviewMode ? 'PENDING_MEDICAL' : 'APPROVED');
    const after = await snapshot([19, 0, 1]);
    const usages = await doctor.call<Schema<'MedicalReportUsagePage'>>(
      'GET',
      reportPath + '/usages',
    );
    expect(usages.data.items).toHaveLength(1);
    expect(usages.data.items[0]!.reportId).toBe(report.id);
    expect(
      await billing.call<Schema<'Claim'>>('POST', claimPath + '/submit', submitBody, {
        etag: beforeSubmit.etag,
        key: submitKey,
      }),
    ).toEqual(submitted);
    expect(await accounts()).toEqual(after);
    expect(
      await doctor.call<Schema<'MedicalReportUsagePage'>>('GET', reportPath + '/usages'),
    ).toEqual(usages);
    if (reviewMode) {
      submitted = await reviewAndCorrectClaim({
        base,
        claimId: claim.data.id,
        reportId: report.id,
        doctor,
        finance,
        billing,
        doctorPage,
        financePage,
        billingPage,
        snapshot,
      });
      // Each new version evaluates report coverage anew (WP-I5-02); replay does not.
      const history = (
        await doctor.call<Schema<'MedicalReportUsagePage'>>('GET', reportPath + '/usages')
      ).data.items;
      expect(history).toHaveLength(3);
      expect(history).toEqual(expect.arrayContaining(usages.data.items));
      for (const usage of history)
        expect(usage).toMatchObject({
          reportId: report.id,
          usedByType: 'CLAIM',
          usedById: claim.data.id,
        });
    }
    const ledger = (
      await admin.call<Schema<'LedgerPage'>>(
        'GET',
        `/api/v1/entitlement-accounts/${ids['accountId']}/ledger?limit=100`,
      )
    ).data.items;
    expect(ledger.map((row) => row.movementType).sort()).toEqual(
      reviewMode
        ? ['CONSUME', 'CONSUME', 'CONSUME', 'GRANT', 'RESERVE', 'REVERSE', 'REVERSE']
        : ['CONSUME', 'GRANT', 'RESERVE'],
    );
    expect(ledger.find((row) => row.movementType === 'RESERVE')).toMatchObject({
      deltaAvailable: -1,
      deltaReserved: 1,
      deltaConsumed: 0,
    });
    expect(ledger.find((row) => row.movementType === 'CONSUME')).toMatchObject({
      deltaAvailable: 0,
      deltaReserved: -1,
      deltaConsumed: 1,
      reasonCode: 'CLAIM',
    });
    const clinicalClaim = (await doctor.call<Schema<'Claim'>>('GET', claimPath)).data;
    expect(clinicalClaim.projection).toBe('CLINICAL');
    expect(clinicalClaim.lines[0]).toMatchObject({
      diagnosisId: diagnoses[0]!.id,
      medicalReportId: report.id,
    });
    expect(usages.data.items[0]).toMatchObject({ usedByType: 'CLAIM', usedById: clinicalClaim.id });
    expect(submitted.data.projection).toBe('FINANCIAL');
    expect(submitted.data.lines[0]!.diagnosisId).toBeUndefined();
    expect(submitted.data.lines[0]!.medicalReportId).toBeUndefined();
    expect(submitted.data.lines[0]!.description).toBeUndefined();
    const decision = submitted.data.lines[0]!.decision!;
    expect(decision).toMatchObject({
      stage: reviewMode ? 'FINANCIAL' : 'AUTO',
      decision: 'APPROVED',
      reasonCode: reviewMode ? 'WITHIN_TARIFF' : 'AUTO_APPROVED',
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
    await billing.call('GET', `/api/v1/encounters/${encounter.id}/diagnoses`, undefined, {
      expected: 403,
    });
    // The financial handoff must not grant access to the underlying clinical case.
    await billing.call('GET', `/api/v1/health-cases/${healthCase.id}`, undefined, {
      expected: 403,
    });
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
    if (reportMode)
      await correctApprovedReport({
        base,
        reportId: report.id,
        claimId: claim.data.id,
        provider,
        doctor,
        billing,
        providerPage: page,
        doctorPage,
        snapshot,
      });
    await page.goto(base + `/portal/cases/${healthCase.id}`);
    await page.getByRole('button', { name: 'Vakayı kapat', exact: true }).click();
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Vakayı kapat', exact: true })
      .click();
    await expect(page.getByTestId('case-status')).toHaveText('Kapandı');
    await expect(page.getByRole('button', { name: 'Muayene ekle', exact: true })).toHaveCount(0);
    await test.info().attach('outpatient-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        ...ids,
        reportStatus: reportMode ? 'SUPERSEDED' : 'APPROVED',
        reportCorrection: reportMode,
        claimStatus: 'APPROVED',
        available: 19,
        reserved: 0,
        consumed: 1,
        approvedTotal: '400',
        payerTotal: '400',
        memberTotal: '0',
        invoiceReady: true,
        claimCreation: 'BROWSER',
        reviewCorrection: reviewMode,
        claimVersion: submitted.data.currentVersionNo,
      }),
    });
  } finally {
    // Keep decided synthetic history. Release only unused test holds on failure.
    await test.info().attach('outpatient-fixture-ids', {
      contentType: 'application/json',
      body: JSON.stringify(ids),
    });
    try {
      if (ruleAttempted && ids['personId']) await seedClaimReviewRule(ids['personId'], true);
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
      if (reviewMode && ids['claimId']) {
        // Claim decisions do not automatically complete workflow items. Close only this
        // test episode's items through each queue's authorized reviewer.
        for (const actor of [doctor, finance]) {
          const items = (
            await actor.call<Schema<'WorkItemPage'>>(
              'GET',
              `/api/v1/work-items?aggregateType=CLAIM&aggregateId=${ids['claimId']}&limit=100`,
            )
          ).data.items;
          for (const item of items) {
            const path = `/api/v1/work-items/${item.id}`;
            let current = await actor.call<Schema<'WorkItem'>>('GET', path);
            if (current.data.status === 'OPEN')
              current = await actor.call<Schema<'WorkItem'>>(
                'POST',
                path + '/claim',
                {},
                { etag: current.etag },
              );
            if (current.data.status === 'CLAIMED')
              await actor.call(
                'POST',
                path + '/complete',
                { outcomeCode: 'PC03_TEST_CLEANUP' },
                { etag: current.etag },
              );
          }
        }
      }
      if (ids['authorizationId']) {
        const path = `/api/v1/authorizations/${ids['authorizationId']}`;
        const current = await doctor.call<Schema<'Authorization'>>('GET', path);
        if (current.data.status === 'ACTIVE' || current.data.status === 'PARTIALLY_USED')
          await doctor.call(
            'POST',
            path + '/cancel',
            { reasonCode: 'PC03_TEST_CLEANUP' },
            { etag: current.etag },
          );
      }
      if (ids['reportId']) {
        const path = `/api/v1/medical-reports/${ids['reportId']}`;
        const current = await provider.call<Schema<'MedicalReport'>>('GET', path);
        if (['DRAFT', 'SUBMITTED'].includes(current.data.status))
          await provider.call('POST', path + '/cancel', {}, { etag: current.etag });
        else if (current.data.status === 'UNDER_REVIEW')
          await doctor.call(
            'POST',
            path + '/reject',
            { rejectReasonCode: 'PC03_TEST_CLEANUP' },
            { etag: current.etag },
          );
      }
      if (ids['workItemId']) {
        const path = `/api/v1/work-items/${ids['workItemId']}`;
        let current = await doctor.call<Schema<'WorkItem'>>('GET', path);
        if (current.data.status === 'OPEN')
          current = await doctor.call<Schema<'WorkItem'>>(
            'POST',
            path + '/claim',
            {},
            { etag: current.etag },
          );
        if (current.data.status === 'CLAIMED')
          await doctor.call(
            'POST',
            path + '/complete',
            { outcomeCode: 'PC03_TEST_CLEANUP' },
            { etag: current.etag },
          );
      }
    } finally {
      await Promise.all([
        admin.close(),
        provider.close(),
        doctor.close(),
        billing.close(),
        finance.close(),
      ]);
      await doctorPage.close();
      await billingPage.close();
      await financePage.close();
    }
  }
});
