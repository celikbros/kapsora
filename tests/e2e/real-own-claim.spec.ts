import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || process.env['E2E_OWN_CLAIM'] !== '1',
  'requires operator-started demo and explicit own-claim fixture opt-in',
);

test('real reviewer cannot decide, approve, reject or return their own claim', async ({ page }) => {
  test.setTimeout(120000);
  page.setDefaultTimeout(15000);
  const admin = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const billing = new Actor(await apiRequest.newContext(), 'provider', true);
  const doctor = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const staff = new Actor(page.request, 'backoffice', true);
  let claimId: string | undefined;
  try {
    await admin.login('admin.a');
    await billing.login('billing.a');
    await doctor.login('doctor.a');
    await staff.login('staff.member');
    const person = (
      await admin.call<S<'PersonPage'>>('GET', '/api/v1/people?q=Deniz&limit=100')
    ).data.items.find((p) => p.displayName === 'Deniz Çalışan')!;
    expect(person).toBeTruthy();
    const enrollment = (
      await admin.call<S<'EnrollmentPage'>>('GET', `/api/v1/people/${person.id}/enrollments`)
    ).data.items.find((e) => e.status === 'ACTIVE')!;
    expect(enrollment.programId).toBeTruthy();
    const reports = (
      await doctor.call<S<'MedicalReportPage'>>(
        'GET',
        `/api/v1/medical-reports?personId=${person.id}&limit=100`,
      )
    ).data.items;
    const report = reports.find((r) => r.status === 'REJECTED' && r.issuingProviderOrganizationId)!;
    expect(
      report,
      'run the own-worklist acceptance first to obtain a closed synthetic report',
    ).toBeTruthy();
    const clinicalReport = (
      await doctor.call<S<'MedicalReport'>>('GET', `/api/v1/medical-reports/${report.id}`)
    ).data;
    expect(clinicalReport.clinicalSummary).toMatch(/^Synthetic queue acceptance /);
    const service = clinicalReport.services[0]!.serviceDefinitionId;
    const balances = () => admin.call('GET', `/api/v1/people/${person.id}/entitlements`);
    const before = await balances();
    const usageBefore = await doctor.call('GET', `/api/v1/medical-reports/${report.id}/usages`);
    const today = new Date().toISOString().slice(0, 10);
    const created = await billing.call<S<'Claim'>>(
      'POST',
      '/api/v1/claims',
      {
        personId: person.id,
        enrollmentId: enrollment.id,
        programId: enrollment.programId!,
        providerOrganizationId: report.issuingProviderOrganizationId!,
        serviceDateFrom: today,
        serviceDateTo: today,
        lines: [
          {
            lineNo: 1,
            serviceDefinitionId: service,
            unitType: 'SESSION',
            quantity: '1',
            lineAmount: '400',
            medicalReportId: report.id,
          },
        ],
      } satisfies S<'CreateClaim'>,
      { expected: 201 },
    );
    claimId = created.data.id;
    const path = `/api/v1/claims/${claimId}`;
    const submitted = await billing.call<S<'Claim'>>(
      'POST',
      path + '/submit',
      {},
      { etag: created.etag },
    );
    expect(submitted.data.status).toBe('PENDING_MEDICAL');
    const original = await doctor.call<S<'Claim'>>('GET', path);
    expect(original.data.exceptions.some((e) => e.code === 'REPORT_NOT_APPROVED')).toBe(true);
    const originalVersion = await doctor.call('GET', path + '/versions/1');
    const commands: [string, unknown][] = [
      [
        'line-decisions',
        {
          decisions: [
            {
              lineNo: 1,
              decision: 'REJECTED',
              approvedQuantity: '0',
              approvedAmount: '0',
              payerAmount: '0',
              memberAmount: '0',
              reasonCode: 'PC03_OWN_REFUSED',
            },
          ],
        } satisfies S<'DecideClaimLines'>,
      ],
      ...['approve', 'reject', 'return'].map(
        (command) => [command, { reasonCode: 'PC03_OWN_REFUSED' }] as [string, unknown],
      ),
    ];
    for (const [command, body] of commands) {
      const denied = await staff.call<{ code: string }>('POST', path + '/' + command, body, {
        etag: original.etag,
        expected: 403,
      });
      expect(denied.data.code).toBe('OWN_FILE_DECISION');
      expect(await doctor.call('GET', path)).toEqual(original);
      expect(await doctor.call('GET', path + '/versions/1')).toEqual(originalVersion);
    }
    await page.goto(base + `/claims/${claimId}`);
    await expect(page.getByTestId('own-file-notice')).toBeVisible();
    await expect(page.getByTestId('claim-commands').getByRole('button')).toHaveCount(0);
    await expect(page.locator('[name="decisions.1.reasonCode"]')).toHaveCount(0);
    await billing.call('GET', path + '/invoice-readiness', undefined, { expected: 409 });
    await doctor.call(
      'POST',
      path + '/reject',
      { reasonCode: 'PC03_TEST_FINISHED' },
      { etag: original.etag },
    );
    expect((await billing.call<S<'Claim'>>('GET', path)).data.status).toBe('REJECTED');
    expect(await balances()).toEqual(before);
    expect(await doctor.call('GET', `/api/v1/medical-reports/${report.id}/usages`)).toEqual(
      usageBefore,
    );
    await test.info().attach('own-claim-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        claimId,
        reportId: report.id,
        ownCommandsRefused: true,
        ownControlsAbsent: true,
        accountsUnchanged: true,
        reportUsageUnchanged: true,
      }),
    });
  } finally {
    try {
      if (claimId) {
        const path = `/api/v1/claims/${claimId}`;
        const current = await billing.call<S<'Claim'>>('GET', path);
        if (current.data.status === 'DRAFT')
          await billing.call(
            'POST',
            path + '/cancel',
            { reasonCode: 'PC03_TEST_CLEANUP' },
            { etag: current.etag },
          );
        else if (current.data.status === 'PENDING_MEDICAL')
          await doctor.call(
            'POST',
            path + '/reject',
            { reasonCode: 'PC03_TEST_CLEANUP' },
            { etag: current.etag },
          );
        const queue = (
          await doctor.call<S<'WorkItemPage'>>(
            'GET',
            `/api/v1/work-items?aggregateType=CLAIM&aggregateId=${claimId}`,
          )
        ).data.items;
        for (const item of queue) {
          const itemPath = `/api/v1/work-items/${item.id}`;
          let currentItem = await doctor.call<S<'WorkItem'>>('GET', itemPath);
          if (currentItem.data.status === 'OPEN')
            currentItem = await doctor.call(
              'POST',
              itemPath + '/claim',
              {},
              { etag: currentItem.etag },
            );
          if (currentItem.data.status === 'CLAIMED')
            await doctor.call(
              'POST',
              itemPath + '/complete',
              { outcomeCode: 'PC03_TEST_FINISHED' },
              { etag: currentItem.etag },
            );
        }
      }
    } finally {
      await Promise.all([admin.close(), billing.close(), doctor.close(), staff.close()]);
    }
  }
});
