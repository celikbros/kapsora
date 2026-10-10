import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const sourceId = process.env['E2E_CLAIM_EXCEPTION_SOURCE_CASE'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || !sourceId,
  'requires operator-started demo and explicit synthetic outpatient source case',
);

test('real duplicate, authorization and report exceptions block invoice readiness without extra consumption', async ({
  page,
  browser,
}) => {
  test.setTimeout(180000);
  page.setDefaultTimeout(15000);
  const financePage = await browser.newPage();
  financePage.setDefaultTimeout(15000);
  const doctor = new Actor(page.request, 'backoffice', true);
  const finance = new Actor(financePage.request, 'backoffice', true);
  const billing = new Actor(await apiRequest.newContext(), 'provider', true);
  const provider = new Actor(await apiRequest.newContext(), 'provider', true);
  const admin = new Actor(await apiRequest.newContext(), 'backoffice', true);
  let authorizationId: string | undefined;
  let requestId: string | undefined;
  const claimIds: string[] = [];
  try {
    await doctor.login('doctor.a');
    await finance.login('financial.reviewer');
    await billing.login('billing.a');
    await provider.login('provider.a');
    await admin.login('admin.a');
    const source = await provider.call<S<'HealthCase'>>('GET', `/api/v1/health-cases/${sourceId}`);
    expect(source.data.status).toBe('CLOSED');
    expect(
      (await admin.call<S<'Person'>>('GET', `/api/v1/people/${source.data.personId}`)).data
        .displayName,
    ).toBe('Deneme Ayaktan');
    const original = (
      await doctor.call<S<'ClaimPage'>>('GET', `/api/v1/claims?caseId=${sourceId}`)
    ).data.items.find((c) => c.status === 'APPROVED')!;
    expect(original).toBeTruthy();
    const originalView = await doctor.call<S<'Claim'>>('GET', `/api/v1/claims/${original.id}`);
    const reportId = originalView.data.lines[0]!.medicalReportId!;
    expect(reportId).toBeTruthy();
    const report = await doctor.call<S<'MedicalReport'>>(
      'GET',
      `/api/v1/medical-reports/${reportId}`,
    );
    expect(report.data.status).toBe('APPROVED');
    const usages = () => doctor.call('GET', `/api/v1/medical-reports/${reportId}/usages`);
    const usageBefore = await usages();
    const accounts = async () =>
      (
        await admin.call<{ items: S<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${source.data.personId}/entitlements`,
        )
      ).data.items;
    const before = await accounts();
    const today = new Date().toISOString().slice(0, 10);
    const serviceId = originalView.data.lines[0]!.serviceDefinitionId;
    const draft = await provider.call<S<'ServiceRequest'>>(
      'POST',
      '/api/v1/service-requests',
      {
        personId: source.data.personId,
        enrollmentId: source.data.enrollmentId,
        providerOrganizationId: source.data.providerOrganizationId!,
        requestType: 'PREAUTHORIZATION',
        channel: 'PROVIDER_PORTAL',
        serviceDate: today,
        items: [{ serviceDefinitionId: serviceId, requestedQuantity: '1', unitType: 'SESSION' }],
      } satisfies S<'CreateServiceRequest'>,
      { expected: 201 },
    );
    requestId = draft.data.id;
    const pending = await provider.call<S<'ServiceRequest'>>(
      'POST',
      `/api/v1/service-requests/${requestId}/submit`,
      {},
      { etag: draft.etag },
    );
    expect(pending.data.status).toBe('PENDING_REVIEW');
    await doctor.call(
      'POST',
      `/api/v1/service-requests/${requestId}/approve`,
      { reasonCode: 'PC03_EXCEPTION_FIXTURE' },
      { etag: pending.etag },
    );
    const hold = await doctor.call<S<'Authorization'>>(
      'POST',
      '/api/v1/authorizations',
      { requestId, validTo: new Date(Date.now() + 86400000).toISOString() },
      { expected: 201 },
    );
    authorizationId = hold.data.id;
    const reserved = await accounts();
    expect(reserved[0]!.reserved - before[0]!.reserved).toBe(1);
    const services = (
      await provider.call<S<'ServiceDefinitionPage'>>(
        'GET',
        '/api/v1/service-definitions?limit=200',
      )
    ).data.items;
    const other = services.find(
      (s) => s.domain === 'HEALTH' && s.id !== serviceId && s.defaultUnitType === 'COUNT',
    )!;
    expect(other).toBeTruthy();
    const tomorrow = new Date(Date.parse(report.data.validTo) + 86400000)
      .toISOString()
      .slice(0, 10);
    const scenarios = [
      {
        code: 'DUPLICATE_SUSPECTED',
        stage: 'PENDING_FINANCIAL',
        day: original.serviceDateFrom,
        line: {
          serviceDefinitionId: serviceId,
          quantity: '1',
          unitType: 'SESSION',
          lineAmount: '400',
        },
      },
      {
        code: 'AUTHORIZATION_EXCEEDED',
        stage: 'PENDING_MEDICAL',
        day: today,
        authorizationId,
        line: {
          serviceDefinitionId: serviceId,
          quantity: '2',
          unitType: 'SESSION',
          lineAmount: '800',
        },
      },
      {
        code: 'REPORT_OUT_OF_WINDOW',
        stage: 'PENDING_MEDICAL',
        day: tomorrow,
        line: {
          serviceDefinitionId: serviceId,
          quantity: '1',
          unitType: 'SESSION',
          lineAmount: '400',
          medicalReportId: reportId,
        },
      },
      {
        code: 'SERVICE_NOT_IN_REPORT',
        stage: 'PENDING_MEDICAL',
        day: original.serviceDateFrom,
        line: {
          serviceDefinitionId: other.id,
          quantity: '1',
          unitType: other.defaultUnitType,
          lineAmount: '400',
          medicalReportId: reportId,
        },
      },
    ];
    const labels: Record<string, string> = {
      DUPLICATE_SUSPECTED: 'Mükerrer şüphesi',
      AUTHORIZATION_EXCEEDED: 'Ön onay aşıldı',
      REPORT_OUT_OF_WINDOW: 'Rapor geçerlilik aralığı dışında',
      SERVICE_NOT_IN_REPORT: 'Hizmet raporun kapsamında değil',
    };
    for (const scenario of scenarios) {
      const scenarioAccounts = await accounts();
      const draft = await billing.call<S<'Claim'>>(
        'POST',
        '/api/v1/claims',
        {
          personId: source.data.personId,
          enrollmentId: source.data.enrollmentId,
          programId: source.data.programId,
          providerOrganizationId: source.data.providerOrganizationId!,
          serviceDateFrom: scenario.day,
          serviceDateTo: scenario.day,
          ...(scenario.authorizationId ? { authorizationId: scenario.authorizationId } : {}),
          lines: [{ lineNo: 1, ...scenario.line }],
        } satisfies S<'CreateClaim'>,
        { expected: 201 },
      );
      claimIds.push(draft.data.id);
      const path = `/api/v1/claims/${draft.data.id}`;
      const submitted = await billing.call<S<'Claim'>>(
        'POST',
        path + '/submit',
        {},
        { etag: draft.etag },
      );
      expect(submitted.data.status).toBe(scenario.stage);
      const clinical = await doctor.call<S<'Claim'>>('GET', path);
      const exception = clinical.data.exceptions.find((e) => e.code === scenario.code)!;
      expect(exception).toBeTruthy();
      if (scenario.code === 'DUPLICATE_SUSPECTED')
        expect(exception.detail).toBe(original.reference);
      if (scenario.code === 'AUTHORIZATION_EXCEEDED') expect(Number(exception.detail)).toBe(1);
      await billing.call('GET', path + '/invoice-readiness', undefined, { expected: 409 });
      expect(await accounts()).toEqual(scenarioAccounts);
      expect(await usages()).toEqual(usageBefore);
      const reviewer = scenario.stage === 'PENDING_MEDICAL' ? doctor : finance;
      const reviewerPage = scenario.stage === 'PENDING_MEDICAL' ? page : financePage;
      await reviewerPage.goto(base + `/claims/${draft.data.id}`);
      await expect(reviewerPage.getByTestId('claim-exceptions')).toContainText(
        labels[scenario.code]!,
      );
      await reviewer.call(
        'POST',
        path + '/reject',
        { reasonCode: 'PC03_EXCEPTION_CHECKED' },
        { etag: clinical.etag },
      );
    }
    const currentHold = await doctor.call<S<'Authorization'>>(
      'GET',
      `/api/v1/authorizations/${authorizationId}`,
    );
    await doctor.call(
      'POST',
      `/api/v1/authorizations/${authorizationId}/cancel`,
      { reasonCode: 'PC03_TEST_FINISHED' },
      { etag: currentHold.etag },
    );
    const after = await accounts();
    expect(after.map(({ rowVersion: _version, ...account }) => account)).toEqual(
      before.map(({ rowVersion: _version, ...account }) => account),
    );
    expect(await doctor.call('GET', `/api/v1/claims/${original.id}`)).toEqual(originalView);
    expect(await provider.call('GET', `/api/v1/health-cases/${sourceId}`)).toEqual(source);
    await test.info().attach('claim-exceptions-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        claimIds,
        requestId,
        authorizationId,
        sourceId,
        exceptions: scenarios.map((s) => s.code),
        extraConsumption: 0,
        reportUsageUnchanged: true,
      }),
    });
  } finally {
    await test.info().attach('claim-exception-fixtures', {
      contentType: 'application/json',
      body: JSON.stringify({ claimIds, requestId, authorizationId }),
    });
    try {
      for (const id of claimIds) {
        const path = `/api/v1/claims/${id}`;
        const current = await doctor.call<S<'Claim'>>('GET', path);
        if (current.data.status === 'DRAFT')
          await billing.call(
            'POST',
            path + '/cancel',
            { reasonCode: 'PC03_TEST_CLEANUP' },
            { etag: current.etag },
          );
        if (['PENDING_MEDICAL', 'PENDING_FINANCIAL'].includes(current.data.status))
          await (current.data.status === 'PENDING_MEDICAL' ? doctor : finance).call(
            'POST',
            path + '/reject',
            { reasonCode: 'PC03_TEST_CLEANUP' },
            { etag: current.etag },
          );
        for (const reviewer of [doctor, finance]) {
          const items = (
            await reviewer.call<S<'WorkItemPage'>>(
              'GET',
              `/api/v1/work-items?aggregateType=CLAIM&aggregateId=${id}`,
            )
          ).data.items;
          for (const item of items) {
            const itemPath = `/api/v1/work-items/${item.id}`;
            let current = await reviewer.call<S<'WorkItem'>>('GET', itemPath);
            if (current.data.status === 'OPEN')
              current = await reviewer.call(
                'POST',
                itemPath + '/claim',
                {},
                { etag: current.etag },
              );
            if (current.data.status === 'CLAIMED')
              await reviewer.call(
                'POST',
                itemPath + '/complete',
                { outcomeCode: 'PC03_TEST_FINISHED' },
                { etag: current.etag },
              );
          }
        }
      }
      if (authorizationId) {
        const path = `/api/v1/authorizations/${authorizationId}`;
        const current = await doctor.call<S<'Authorization'>>('GET', path);
        if (current.data.status === 'ACTIVE')
          await doctor.call(
            'POST',
            path + '/cancel',
            { reasonCode: 'PC03_TEST_CLEANUP' },
            { etag: current.etag },
          );
      } else if (requestId) {
        const path = `/api/v1/service-requests/${requestId}`;
        const current = await provider.call<S<'ServiceRequest'>>('GET', path);
        if (['DRAFT', 'PENDING_REVIEW'].includes(current.data.status))
          await provider.call(
            'POST',
            path + '/cancel',
            { reasonCode: 'PC03_TEST_CLEANUP' },
            { etag: current.etag },
          );
      }
    } finally {
      await Promise.all([
        doctor.close(),
        finance.close(),
        billing.close(),
        provider.close(),
        admin.close(),
      ]);
      await financePage.close();
    }
  }
});
