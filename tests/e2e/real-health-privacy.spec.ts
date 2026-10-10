import { randomUUID } from 'node:crypto';
import { expect, request as apiRequest, test, type Page } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const sourceId = process.env['E2E_PRIVACY_SOURCE_CASE'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || !sourceId,
  'requires the operator-started demo and an explicit synthetic outpatient source case',
);

test('real HR/financial projections and sensitive report purposes preserve clinical boundaries', async ({
  browser,
}) => {
  test.setTimeout(180000);
  const hrPage = await browser.newPage();
  const doctorPage = await browser.newPage();
  const financePage = await browser.newPage();
  for (const page of [hrPage, doctorPage, financePage]) page.setDefaultTimeout(15000);
  const capture = async (page: Page, state: string) => {
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 950 });
      await page.evaluate(() => window.scrollTo(0, 0));
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      );
      await page.screenshot({
        path: `.impeccable/review/health-privacy/${state}-${width}.png`,
        fullPage: true,
      });
    }
    await page.setViewportSize({ width: 1440, height: 950 });
  };
  const hr = new Actor(hrPage.request, 'backoffice', true);
  const doctor = new Actor(doctorPage.request, 'backoffice', true);
  const finance = new Actor(financePage.request, 'backoffice', true);
  const provider = new Actor(await apiRequest.newContext(), 'provider', true);
  const auditor = new Actor(await apiRequest.newContext(), 'backoffice', true);
  let caseId: string | undefined;
  const reportIds: string[] = [];
  const marker = `Synthetic privacy ${randomUUID()}`;
  try {
    await hr.login('sponsor.hr');
    await doctor.login('doctor.a');
    await finance.login('financial.reviewer');
    await provider.login('provider.a');
    await auditor.login('both.ab');
    const source = await doctor.call<S<'HealthCase'>>('GET', `/api/v1/health-cases/${sourceId}`);
    const person = (await hr.call<S<'Person'>>('GET', `/api/v1/people/${source.data.personId}`))
      .data;
    expect(person.displayName).toBe('Deneme Ayaktan');
    expect(source.data.status).toBe('CLOSED');
    const balances = async () =>
      (
        await hr.call<{ items: S<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${person.id}/entitlements`,
        )
      ).data.items;
    const initialBalances = await balances();
    expect(initialBalances.length).toBeGreaterThan(0);
    const reports = (
      await doctor.call<S<'MedicalReportPage'>>('GET', `/api/v1/medical-reports?caseId=${sourceId}`)
    ).data.items;
    const approved = reports.find((r) => r.status === 'APPROVED')!;
    expect(approved).toBeTruthy();
    const report = (
      await doctor.call<S<'MedicalReport'>>('GET', `/api/v1/medical-reports/${approved.id}`)
    ).data;
    expect(report.clinicalSummary).toMatch(/^Synthetic outpatient /);
    const claims = (await hr.call<S<'ClaimPage'>>('GET', `/api/v1/claims?caseId=${sourceId}`)).data
      .items;
    const claim = claims.find((c) => c.caseId === sourceId && c.status === 'APPROVED')!;
    expect(claim).toBeTruthy();
    const forbidden = [
      report.clinicalSummary!,
      ...source.data.encounters.map((e) => e.notesClinical!).filter(Boolean),
    ];
    const assertHidden = (value: unknown) => {
      for (const text of [...forbidden, marker]) expect(JSON.stringify(value)).not.toContain(text);
    };
    const limitedCase = (await hr.call<S<'HealthCase'>>('GET', `/api/v1/health-cases/${sourceId}`))
      .data;
    expect(limitedCase.projection).toBe('FINANCIAL');
    assertHidden(limitedCase);
    for (const encounter of limitedCase.encounters) {
      expect(encounter.notesClinical == null).toBe(true);
      expect(encounter.branchCode == null).toBe(true);
    }
    for (const encounter of source.data.encounters) {
      await hr.call('GET', `/api/v1/encounters/${encounter.id}/diagnoses`, undefined, {
        expected: 403,
      });
    }
    const limitedReport = (
      await hr.call<S<'MedicalReport'>>('GET', `/api/v1/medical-reports/${report.id}`)
    ).data;
    expect(limitedReport.projection).toBe('FINANCIAL');
    expect(limitedReport.documents).toEqual([]);
    assertHidden(limitedReport);
    expect(limitedReport.clinicalSummary == null).toBe(true);
    expect(limitedReport.reportType == null).toBe(true);
    await hr.call('GET', `/api/v1/health-access-log?personId=${person.id}`, undefined, {
      expected: 403,
    });
    for (const document of report.documents) {
      await hr.call(
        'POST',
        `/api/v1/documents/${document.objectId}/download`,
        {},
        { expected: 403 },
      );
    }
    for (const [actor, page] of [
      [hr, hrPage],
      [finance, financePage],
    ] as const) {
      const financial = (await actor.call<S<'Claim'>>('GET', `/api/v1/claims/${claim.id}`)).data;
      expect(financial.projection).toBe('FINANCIAL');
      assertHidden(financial);
      for (const line of financial.lines) {
        expect(line.description == null).toBe(true);
        expect(line.diagnosisId == null).toBe(true);
        expect(line.medicalReportId == null).toBe(true);
      }
      await page.goto(base + `/claims/${claim.id}`);
      await expect(page.getByTestId('claim-lines')).toHaveAttribute('data-projection', 'FINANCIAL');
      await expect(page.getByRole('columnheader', { name: 'Tanı', exact: true })).toHaveCount(0);
      await expect(page.getByRole('columnheader', { name: 'Açıklama', exact: true })).toHaveCount(
        0,
      );
      assertHidden(await page.locator('body').innerText());
    }
    await hrPage.goto(base + `/people/${person.id}`);
    await hrPage.getByRole('tab', { name: 'Sağlık', exact: true }).click();
    await expect(hrPage.getByTestId('person-claims')).toBeVisible();
    await expect(hrPage.getByTestId('person-cases')).toBeVisible();
    await expect(hrPage.getByRole('tab', { name: 'Erişim kaydı', exact: true })).toHaveCount(0);
    assertHidden(await hrPage.locator('body').innerText());

    await capture(hrPage, 'hr-health');

    // A separate, unfunded episode; never alter the already accepted source's history.
    const created = await provider.call<S<'HealthCase'>>(
      'POST',
      '/api/v1/health-cases',
      {
        personId: person.id,
        enrollmentId: source.data.enrollmentId,
        caseType: 'OUTPATIENT',
        providerOrganizationId: source.data.providerOrganizationId,
      } satisfies S<'CreateHealthCase'>,
      { expected: 201 },
    );
    caseId = created.data.id;
    const casePath = `/api/v1/health-cases/${caseId}`;
    const now = new Date().toISOString();
    const encounter = await provider.call<S<'Encounter'>>(
      'POST',
      casePath + '/encounters',
      {
        encounterType: 'OUTPATIENT',
        startedAt: now,
        endedAt: now,
        notesClinical: marker,
      } satisfies S<'CreateEncounter'>,
      { expected: 201 },
    );
    for (let n = 0; n < 2; n++) {
      const draft = await provider.call<S<'MedicalReport'>>(
        'POST',
        '/api/v1/medical-reports',
        {
          personId: person.id,
          caseId,
          reportType: 'FIZIK_TEDAVI',
          issuedAt: now.slice(0, 10),
          validFrom: now.slice(0, 10),
          validTo: now.slice(0, 10),
          clinicalSummary: `${marker} ${n}`,
          issuingProviderOrganizationId: source.data.providerOrganizationId,
        } satisfies S<'CreateMedicalReport'>,
        { expected: 201 },
      );
      reportIds.push(draft.data.id);
    }
    const systems = (
      await provider.call<S<'CodeSystemPage'>>('GET', '/api/v1/code-systems?limit=100')
    ).data.items;
    const icd = systems.find((s) => s.code === 'ICD10')!;
    expect(icd).toBeTruthy();
    const codes = (
      await provider.call<S<'CodeValuePage'>>(
        'GET',
        `/api/v1/code-systems/${icd.id}/values?q=F32.1`,
      )
    ).data.items;
    const diagnosis = codes.find((c) => c.code === 'F32.1')!;
    expect(diagnosis.attributes['sensitive']).toBe(true);
    await provider.call(
      'PUT',
      `/api/v1/encounters/${encounter.data.id}/diagnoses`,
      {
        items: [{ codeValueId: diagnosis.id, diagnosisType: 'PRIMARY' }],
      },
      { etag: encounter.etag },
    );
    const paths = reportIds.map((id) => `/api/v1/medical-reports/${id}`);
    const purpose = { purpose: 'MEDICAL_REVIEW', reason: 'Sentetik gizlilik doğrulaması' };
    const sensitive = (
      await doctor.call<S<'HealthCase'>>('GET', casePath, undefined, { access: purpose })
    ).data;
    expect(sensitive.sensitivity).toBe('SENSITIVE');
    expect((await provider.call<S<'HealthCase'>>('GET', casePath)).data.projection).toBe(
      'FINANCIAL',
    );
    for (const path of paths) {
      const missing = await doctor.call<{ code: string }>('GET', path, undefined, {
        expected: 428,
      });
      expect(missing.data.code).toBe('ACCESS_PURPOSE_REQUIRED');
      const limited = (await hr.call<S<'MedicalReport'>>('GET', path)).data;
      expect(limited.projection).toBe('FINANCIAL');
      assertHidden(limited);
    }
    const audit = async () =>
      (
        await auditor.call<S<'HealthAccessLogPage'>>(
          'GET',
          `/api/v1/health-access-log?personId=${person.id}&limit=100`,
        )
      ).data.items;
    await hrPage.goto(base + `/medical-reports/${reportIds[0]}`);
    await expect(hrPage.getByTestId('report-status')).toBeVisible();
    await expect(hrPage.getByTestId('report-summary')).toHaveCount(0);
    await expect(hrPage.getByTestId('purpose-dialog')).toHaveCount(0);
    assertHidden(await hrPage.locator('body').innerText());
    const beforeDecline = await audit();
    await doctor.call('GET', paths[0]!, undefined, { access: { projection: 'FINANCIAL' } });
    expect(await audit()).toEqual(beforeDecline);
    await doctorPage.goto(base + `/medical-reports/${reportIds[0]}`);
    await expect(doctorPage.getByTestId('purpose-dialog')).toBeVisible();
    await capture(doctorPage, 'purpose');
    await doctorPage.getByTestId('purpose-decline').click();
    await expect(doctorPage.getByTestId('report-status')).toBeVisible();
    await expect(doctorPage.getByTestId('report-summary')).toHaveCount(0);
    assertHidden(await doctorPage.locator('body').innerText());
    // Reload starts a new tab runtime, asking again; purpose is not browser storage.
    await doctorPage.reload();
    await expect(doctorPage.getByTestId('purpose-dialog')).toBeVisible();
    await doctorPage.locator('[name="reason"]').fill(purpose.reason);
    await doctorPage.getByTestId('purpose-confirm').click();
    await expect(doctorPage.getByTestId('report-summary')).toHaveText(`${marker} 0`);
    // A second sensitive report has its own purpose gate. SPA reuse is covered in m5.test.tsx.
    await doctorPage.goto(base + '/medical-reports/' + reportIds[1]);
    await expect(doctorPage.getByTestId('purpose-dialog')).toBeVisible();
    await expect(doctorPage.getByTestId('report-summary')).toHaveCount(0);
    await doctorPage.getByTestId('purpose-decline').click();
    await expect(doctorPage.getByTestId('report-status')).toBeVisible();
    assertHidden(await doctorPage.locator('body').innerText());
    const events = await audit();
    expect(
      events.some(
        (e) =>
          e.resourceId === reportIds[0] &&
          e.outcome === 'SUCCESS' &&
          e.purposeCode === purpose.purpose &&
          e.reasonText === purpose.reason,
      ),
    ).toBe(true);
    expect(events.some((e) => e.resourceId === reportIds[1] && e.outcome === 'DENIED')).toBe(true);
    expect(events.filter((e) => e.resourceId === reportIds[1] && e.outcome === 'SUCCESS')).toEqual(
      [],
    );
    expect(await doctor.call<S<'HealthCase'>>('GET', `/api/v1/health-cases/${sourceId}`)).toEqual(
      source,
    );
    await test.info().attach('health-privacy-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        sourceCaseId: sourceId,
        claimId: claim.id,
        caseId,
        reportIds,
        hrClinicalFieldsAbsent: true,
        financialClinicalFieldsAbsent: true,
        purposeDeclineNoAccess: true,
        purposeRecorded: true,
      }),
    });
  } finally {
    try {
      for (const id of reportIds) {
        const path = `/api/v1/medical-reports/${id}`;
        const report = await provider.call<S<'MedicalReport'>>('GET', path);
        if (report.data.status === 'DRAFT')
          await provider.call('POST', path + '/cancel', {}, { etag: report.etag });
      }
      if (caseId) {
        const path = `/api/v1/health-cases/${caseId}`;
        const record = await provider.call<S<'HealthCase'>>('GET', path);
        if (record.data.status === 'OPEN')
          await provider.call(
            'POST',
            path + '/close',
            { reasonText: 'Synthetic privacy acceptance complete' },
            { etag: record.etag },
          );
      }
      await test.info().attach('privacy-fixtures', {
        contentType: 'application/json',
        body: JSON.stringify({ caseId, reportIds }),
      });
    } finally {
      await Promise.all([
        hr.close(),
        doctor.close(),
        finance.close(),
        provider.close(),
        auditor.close(),
      ]);
      await Promise.all([hrPage.close(), doctorPage.close(), financePage.close()]);
    }
  }
});
