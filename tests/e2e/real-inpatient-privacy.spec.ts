import { randomUUID } from 'node:crypto';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const sourceId = process.env['E2E_INPATIENT_PRIVACY_SOURCE'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || !sourceId,
  'requires operator-started demo and explicit synthetic inpatient source stay',
);

test('real inpatient privacy, sensitive access and admission dates preserve boundaries', async ({
  browser,
}) => {
  test.setTimeout(180000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const providerPage = await browser.newPage();
  const hrPage = await browser.newPage();
  const provider = new Actor(providerPage.request, 'provider', true);
  const hr = new Actor(hrPage.request, 'backoffice', true);
  const doctor = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const finance = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const auditor = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const ids: Record<string, string> = {};
  const marker = `Synthetic inpatient privacy ${randomUUID()}`;
  const access = { purpose: 'MEDICAL_REVIEW', reason: 'Sentetik yatis gizlilik dogrulamasi' };
  let source: S<'InpatientStay'> | undefined;
  let balancesBefore: S<'EntitlementAccount'>[] = [];
  const balances = async () =>
    (
      await hr.call<{ items: S<'EntitlementAccount'>[] }>(
        'GET',
        `/api/v1/people/${source!.personId}/entitlements`,
      )
    ).data.items;
  try {
    await provider.login('provider.a');
    await hr.login('sponsor.hr');
    await doctor.login('doctor.a');
    await finance.login('financial.reviewer');
    await auditor.login('both.ab');
    source = (await provider.call<S<'InpatientStay'>>('GET', `/api/v1/inpatient-stays/${sourceId}`))
      .data;
    expect(source.status).toBe('DISCHARGED');
    const person = (await hr.call<S<'Person'>>('GET', `/api/v1/people/${source.personId}`)).data;
    expect(person.displayName).toMatch(/^Deneme Yatis/);
    const sourceCase = (
      await provider.call<S<'HealthCase'>>('GET', `/api/v1/health-cases/${source.caseId}`)
    ).data;
    balancesBefore = await balances();
    const admissionAt = new Date().toISOString();
    const created = await provider.call<S<'HealthCase'>>(
      'POST',
      '/api/v1/health-cases',
      {
        personId: source.personId,
        enrollmentId: sourceCase.enrollmentId,
        providerOrganizationId: source.providerOrganizationId,
        caseType: 'INPATIENT',
        openedAt: admissionAt,
      } satisfies S<'CreateHealthCase'>,
      { expected: 201 },
    );
    ids['caseId'] = created.data.id;
    const casePath = `/api/v1/health-cases/${created.data.id}`;
    const encounter = await provider.call<S<'Encounter'>>(
      'POST',
      casePath + '/encounters',
      {
        encounterType: 'INPATIENT',
        startedAt: admissionAt,
        endedAt: admissionAt,
        notesClinical: marker,
      } satisfies S<'CreateEncounter'>,
      { expected: 201 },
    );
    const systems = (
      await provider.call<S<'CodeSystemPage'>>('GET', '/api/v1/code-systems?limit=100')
    ).data.items;
    const system = systems.find((s) => s.code === 'ICD10')!;
    expect(system).toBeTruthy();
    const diagnosisCode = async (code: string) =>
      (
        await provider.call<S<'CodeValuePage'>>(
          'GET',
          `/api/v1/code-systems/${system.id}/values?q=${code}`,
        )
      ).data.items.find((c) => c.code === code)!;
    const normal = await diagnosisCode('J06.9');
    const diagnoses = await provider.call<{ items: S<'Diagnosis'>[] }>(
      'PUT',
      `/api/v1/encounters/${encounter.data.id}/diagnoses`,
      {
        items: [{ codeValueId: normal.id, diagnosisType: 'PRIMARY' }],
      },
      { etag: encounter.etag },
    );
    const admission = {
      caseId: created.data.id,
      providerOrganizationId: source.providerOrganizationId,
      admissionAt,
      estimatedDays: 2,
      admissionDiagnosisId: diagnoses.data.items[0]!.id,
    };
    for (const days of [-4000, 4000]) {
      await provider.call(
        'POST',
        '/api/v1/inpatient-stays',
        {
          ...admission,
          admissionAt: new Date(Date.now() + days * 86400000).toISOString(),
        },
        { expected: 422 },
      );
    }
    expect(
      (
        await provider.call<S<'InpatientStayPage'>>(
          'GET',
          `/api/v1/inpatient-stays?caseId=${created.data.id}`,
        )
      ).data.items,
    ).toEqual([]);
    expect(await balances()).toEqual(balancesBefore);
    const stay = await provider.call<S<'InpatientStay'>>(
      'POST',
      '/api/v1/inpatient-stays',
      admission,
      { expected: 201 },
    );
    ids['stayId'] = stay.data.id;
    const stayPath = `/api/v1/inpatient-stays/${stay.data.id}`;
    const getStay = () => provider.call<S<'InpatientStay'>>('GET', stayPath);
    const approve = async (id: string) => {
      const path = `/api/v1/service-requests/${id}`;
      const request = await doctor.call<S<'ServiceRequest'>>('GET', path);
      expect(request.data.status).toBe('PENDING_REVIEW');
      await doctor.call(
        'POST',
        path + '/approve',
        { reasonCode: 'PC04_PRIVACY' },
        { etag: request.etag },
      );
    };
    await approve(stay.data.serviceRequestId);
    await expect
      .poll(async () => (await getStay()).data.status, { timeout: 30000 })
      .toBe('AUTHORIZED');
    let current = await getStay();
    await provider.call('GET', stayPath + '/reconciliation', undefined, { expected: 409 });
    const extension = await provider.call<S<'InpatientStay'>>(
      'POST',
      stayPath + '/extensions',
      {
        additionalDays: 2,
        reasonCode: 'PC04_PRIVACY',
        reasonText: marker,
      },
      { etag: current.etag },
    );
    await approve(extension.data.extensions[0]!.serviceRequestId);
    await expect
      .poll(async () => (await getStay()).data.extensions[0]!.status, { timeout: 30000 })
      .toBe('APPROVED');
    current = await getStay();
    expect(current.data.admissionDiagnosisId).toBeTruthy();
    expect(current.data.extensions[0]!.reasonText).toBe(marker);
    const assertHidden = (value: unknown) => {
      const serialized = JSON.stringify(value);
      expect(serialized).not.toContain(marker);
      expect(serialized).not.toContain('admissionDiagnosisId');
      expect(serialized).not.toContain('reasonText');
    };
    const limited = (await hr.call<S<'InpatientStay'>>('GET', stayPath)).data;
    expect(limited.projection).toBe('FINANCIAL');
    expect(Number(limited.authorizedDays)).toBe(4);
    assertHidden(limited);
    const listed = (
      await hr.call<S<'InpatientStayPage'>>(
        'GET',
        `/api/v1/inpatient-stays?caseId=${created.data.id}`,
      )
    ).data;
    expect(listed.items.map((r) => r.id)).toEqual([stay.data.id]);
    assertHidden(listed);
    await hr.call(
      'POST',
      stayPath + '/cancel',
      { reasonCode: 'PC04_REFUSAL' },
      { etag: current.etag, expected: 403 },
    );
    await finance.call('GET', stayPath, undefined, { expected: 403 });
    await finance.call('GET', stayPath + '/reconciliation', undefined, { expected: 403 });
    // Make only this new episode sensitive after recording an extension with actual clinical text.
    const sensitive = await diagnosisCode('F32.1');
    expect(sensitive.attributes['sensitive']).toBe(true);
    // Keep the admission's referenced diagnosis immutable; add a separate encounter.
    const sensitiveEncounter = await provider.call<S<'Encounter'>>(
      'POST',
      casePath + '/encounters',
      {
        encounterType: 'INPATIENT',
        startedAt: admissionAt,
        endedAt: admissionAt,
        notesClinical: marker,
      } satisfies S<'CreateEncounter'>,
      { expected: 201 },
    );
    await provider.call(
      'PUT',
      `/api/v1/encounters/${sensitiveEncounter.data.id}/diagnoses`,
      {
        items: [{ codeValueId: sensitive.id, diagnosisType: 'PRIMARY' }],
      },
      { etag: sensitiveEncounter.etag },
    );
    const audit = async () =>
      (
        await auditor.call<S<'HealthAccessLogPage'>>(
          'GET',
          `/api/v1/health-access-log?personId=${source!.personId}&limit=100`,
        )
      ).data.items;
    const denied = await doctor.call<S<'Problem'>>('GET', stayPath, undefined, { expected: 428 });
    expect(denied.data.code).toBe('ACCESS_PURPOSE_REQUIRED');
    const beforeDecline = await audit();
    const declined = await doctor.call<S<'InpatientStay'>>('GET', stayPath, undefined, {
      access: { projection: 'FINANCIAL' },
    });
    expect(declined.data.projection).toBe('FINANCIAL');
    assertHidden(declined.data);
    expect(await audit()).toEqual(beforeDecline);
    const clinical = await doctor.call<S<'InpatientStay'>>('GET', stayPath, undefined, { access });
    expect(clinical.data.projection).toBe('CLINICAL');
    expect(clinical.data.extensions[0]!.reasonText).toBe(marker);
    const events = await audit();
    expect(
      events.some(
        (e) =>
          e.resourceId === stay.data.id &&
          e.outcome === 'SUCCESS' &&
          e.purposeCode === access.purpose &&
          e.reasonText === access.reason,
      ),
    ).toBe(true);
    expect(events.some((e) => e.resourceId === stay.data.id && e.outcome === 'DENIED')).toBe(true);
    expect((await getStay()).data.projection).toBe('FINANCIAL');
    await providerPage.goto(base + `/portal/stays/${stay.data.id}`);
    await expect(providerPage.getByTestId('stay-status')).toBeVisible();
    await expect(providerPage.getByTestId('extensions')).toBeVisible();
    expect(await providerPage.locator('body').innerText()).not.toContain(marker);
    await hrPage.goto(base + `/people/${source.personId}`);
    await hrPage.getByRole('tab', { name: 'Sağlık', exact: true }).click();
    await expect(hrPage.getByTestId('person-cases')).toContainText('Yatan');
    expect(await hrPage.locator('body').innerText()).not.toContain(marker);
    for (const [name, page] of [
      ['provider', providerPage],
      ['hr', hrPage],
    ] as const) {
      for (const width of [1440, 390]) {
        await page.setViewportSize({ width, height: 950 });
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
          true,
        );
        await page.screenshot({
          path: `.impeccable/review/inpatient-privacy/${name}-${width}.png`,
          fullPage: true,
        });
      }
    }
    expect(
      (await provider.call<S<'InpatientStay'>>('GET', `/api/v1/inpatient-stays/${sourceId}`)).data,
    ).toEqual(source);
    await test.info().attach('inpatient-privacy-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        ...ids,
        sourceStayId: sourceId,
        hrClinicalFieldsAbsent: true,
        providerSensitiveFallback: true,
        financialRoleDenied: true,
        purposeDeclineNoAccess: true,
        purposeRecorded: true,
        dateBoundaries: true,
      }),
    });
  } finally {
    try {
      if (ids['stayId']) {
        const path = `/api/v1/inpatient-stays/${ids['stayId']}`;
        const record = await provider.call<S<'InpatientStay'>>('GET', path);
        if (['REQUESTED', 'AUTHORIZED', 'ADMITTED'].includes(record.data.status)) {
          await provider.call(
            'POST',
            path + '/cancel',
            { reasonCode: 'PC04_PRIVACY_CLEANUP' },
            { etag: record.etag },
          );
        }
        // Release each known test hold explicitly; this is fixture cleanup, not proof of stay cancellation.
        for (const id of [
          record.data.authorizationId,
          ...record.data.extensions.map((e) => e.authorizationId),
        ].filter((id): id is string => !!id)) {
          const authPath = `/api/v1/authorizations/${id}`;
          const auth = await doctor.call<S<'Authorization'>>('GET', authPath);
          if (['ACTIVE', 'PARTIALLY_USED'].includes(auth.data.status))
            await doctor.call(
              'POST',
              authPath + '/cancel',
              { reasonCode: 'PC04_PRIVACY_CLEANUP' },
              { etag: auth.etag },
            );
        }
      }
      if (ids['caseId']) {
        const path = `/api/v1/health-cases/${ids['caseId']}`;
        const record = await provider.call<S<'HealthCase'>>('GET', path);
        if (record.data.status === 'OPEN')
          await provider.call(
            'POST',
            path + '/close',
            { reasonText: 'Synthetic inpatient privacy complete' },
            { etag: record.etag },
          );
      }
      if (source && balancesBefore.length) {
        const figures = (rows: S<'EntitlementAccount'>[]) =>
          rows.map((r) => [r.id, r.available, r.reserved, r.consumed, r.expired]);
        expect(figures(await balances())).toEqual(figures(balancesBefore));
      }
      await test.info().attach('inpatient-privacy-fixtures', {
        contentType: 'application/json',
        body: JSON.stringify(ids),
      });
    } finally {
      await Promise.all([
        provider.close(),
        hr.close(),
        doctor.close(),
        finance.close(),
        auditor.close(),
      ]);
      await Promise.all([providerPage.close(), hrPage.close()]);
    }
  }
});
