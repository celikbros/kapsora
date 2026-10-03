import { randomUUID } from 'node:crypto';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';
import { syntheticPDF } from './synthetic-pdf';

type Schema<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || process.env['E2E_HEALTH_WORKLIST'] !== '1',
  'requires the operator-started demo and explicit new medical-report fixture opt-in',
);

test('real medical worklist enforces queue permissions, ownership and report review handoff', async ({
  page,
  browser,
}) => {
  test.setTimeout(120_000);
  const admin = new Actor(await apiRequest.newContext(), 'backoffice');
  const provider = new Actor(page.request, 'provider');
  const doctor = new Actor(await apiRequest.newContext(), 'backoffice');
  const staffPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  staffPage.setDefaultTimeout(15_000);
  const staff = new Actor(staffPage.request, 'backoffice');
  const finance = new Actor(await apiRequest.newContext(), 'backoffice');
  let reportId: string | undefined;
  let itemId: string | undefined;
  try {
    await admin.login('admin.a');
    await provider.login('provider.a');
    await doctor.login('doctor.a');
    await staff.login('staff.member');
    await finance.login('financial.reviewer');
    const people = (
      await admin.call<Schema<'PersonPage'>>('GET', '/api/v1/people?q=Deniz&limit=100')
    ).data.items;
    const person = people.find((p) => p.displayName === 'Deniz Çalışan')!;
    expect(person).toBeTruthy();
    const today = new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Istanbul' }).format(
      new Date(),
    );
    const accounts = async () =>
      (
        await admin.call<{ items: Schema<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${person.id}/entitlements?asOf=${today}`,
        )
      ).data.items;
    const before = await accounts();
    const services = (
      await provider.call<{ items: Schema<'ServiceDefinition'>[] }>(
        'GET',
        '/api/v1/service-definitions?limit=200',
      )
    ).data.items;
    const service = services.find((s) => s.code === 'PHYSIO_SESSION')!;
    const hospitalRequest = (
      await provider.call<Schema<'ServiceRequestPage'>>('GET', '/api/v1/service-requests?limit=1')
    ).data.items[0]!;
    const clinicalMarker = `Synthetic queue acceptance ${randomUUID()}`;
    const draft = await provider.call<Schema<'MedicalReport'>>(
      'POST',
      '/api/v1/medical-reports',
      {
        personId: person.id,
        reportType: 'FIZIK_TEDAVI',
        issuedAt: new Date().toISOString().slice(0, 10),
        issuingProviderOrganizationId: hospitalRequest.providerOrganizationId!,
        validFrom: today,
        validTo: today,
        clinicalSummary: clinicalMarker,
      } satisfies Schema<'CreateMedicalReport'>,
      { expected: 201 },
    );
    reportId = draft.data.id;
    const path = `/api/v1/medical-reports/${reportId}`;
    await provider.call(
      'PUT',
      path + '/services',
      { items: [{ serviceDefinitionId: service.id, coveredQuantity: '1' }] },
      { etag: draft.etag },
    );
    let current = await provider.call<Schema<'MedicalReport'>>('GET', path);
    const missing = await provider.call<{ code: string }>(
      'POST',
      path + '/submit',
      {},
      { etag: current.etag, expected: 422 },
    );
    expect(missing.data.code).toBe('MEDICAL_REPORT_DOCUMENT_REQUIRED');

    await page.goto(base + `/portal/reports/${reportId}`);
    await expect(page.getByTestId('report-status')).toHaveText('Taslak');
    const form = page.getByTestId('document-upload-form');
    const filename = `queue-${reportId.slice(-8)}.pdf`;
    await form
      .getByLabel(/Dosya seç/)
      .setInputFiles({ name: filename, mimeType: 'application/pdf', buffer: syntheticPDF() });
    await form.getByLabel(/Belge türü/).selectOption('MEDICAL_REPORT');
    await form.getByLabel(/Gizlilik/).selectOption('HEALTH');
    await form.getByRole('button', { name: 'Belge yükle', exact: true }).click();
    await expect(
      page
        .getByTestId('documents-table')
        .getByRole('row')
        .filter({ hasText: filename })
        .getByText('Temiz', { exact: true }),
    ).toBeVisible({ timeout: 45_000 });
    const sent = page.waitForResponse((r) => new URL(r.url()).pathname === path + '/submit');
    await page.getByRole('button', { name: 'Gönder', exact: true }).click();
    expect((await sent).status()).toBe(200);
    await expect(page.getByTestId('report-status')).toHaveText('Gönderildi');
    const refuseOwnCommands = async () => {
      const before = await provider.call<Schema<'MedicalReport'>>('GET', path);
      const commands =
        before.data.status === 'SUBMITTED' ? ['start-review'] : ['approve', 'reject'];
      for (const command of commands) {
        const denied = await staff.call<{ code: string }>(
          'POST',
          path + '/' + command,
          command === 'reject' ? { rejectReasonCode: 'PC03_OWN_FILE_REFUSED' } : {},
          { etag: before.etag, expected: 403 },
        );
        expect(denied.data.code).toBe('OWN_FILE_DECISION');
        expect(await provider.call<Schema<'MedicalReport'>>('GET', path)).toEqual(before);
      }
      await staffPage.goto(base + `/medical-reports/${reportId}`);
      await expect(staffPage.getByTestId('own-file-notice')).toBeVisible();
      await expect(staffPage.getByTestId('report-commands').getByRole('button')).toHaveCount(0);
      await expect(staffPage.getByTestId('report-summary')).toHaveText(clinicalMarker);
    };
    const listPath = `/api/v1/work-items?aggregateType=MEDICAL_REPORT&aggregateId=${reportId}`;
    const queue = (await doctor.call<Schema<'WorkItemPage'>>('GET', listPath)).data.items;
    expect(queue).toHaveLength(1);
    const item = queue[0]!;
    itemId = item.id;
    await refuseOwnCommands();
    const itemPath = `/api/v1/work-items/${itemId}`;
    expect(item.status).toBe('OPEN');
    expect(item.title).toContain(draft.data.reference);
    expect(JSON.stringify(item)).not.toContain(clinicalMarker);
    expect(JSON.stringify(item)).not.toContain('FIZIK_TEDAVI');
    expect(JSON.stringify(item)).not.toContain(person.displayName);
    expect((await finance.call<Schema<'WorkItemPage'>>('GET', listPath)).data.items).toEqual([]);
    await finance.call('GET', itemPath, undefined, { expected: 404 });
    await finance.call(
      'POST',
      itemPath + '/claim',
      {},
      { etag: `"${item.rowVersion}"`, expected: 404 },
    );
    await provider.call('GET', listPath, undefined, { expected: 403 });
    await provider.call(
      'POST',
      itemPath + '/claim',
      {},
      { etag: `"${item.rowVersion}"`, expected: 403 },
    );
    const open = await doctor.call<Schema<'WorkItem'>>('GET', itemPath);
    const refused = await staff.call<{ code: string }>(
      'POST',
      itemPath + '/claim',
      {},
      { etag: open.etag, expected: 403 },
    );
    expect(refused.data.code).toBe('OWN_FILE_DECISION');
    expect(await doctor.call<Schema<'WorkItem'>>('GET', itemPath)).toEqual(open);
    expect((await provider.call<Schema<'MedicalReport'>>('GET', path)).data.status).toBe(
      'SUBMITTED',
    );
    await doctor.call('POST', itemPath + '/claim', {}, { etag: '"999999"', expected: 412 });
    const key = randomUUID();
    const claimed = await doctor.call<Schema<'WorkItem'>>(
      'POST',
      itemPath + '/claim',
      {},
      { etag: open.etag, key },
    );
    expect(claimed.data.status).toBe('CLAIMED');
    expect((await provider.call<Schema<'MedicalReport'>>('GET', path)).data.status).toBe(
      'UNDER_REVIEW',
    );
    await refuseOwnCommands();
    expect(
      await doctor.call<Schema<'WorkItem'>>(
        'POST',
        itemPath + '/claim',
        {},
        { etag: open.etag, key },
      ),
    ).toEqual(claimed);
    const taken = await staff.call<{ code: string }>(
      'POST',
      itemPath + '/claim',
      {},
      { etag: open.etag, expected: 409 },
    );
    expect(taken.data.code).toBe('WORK_ITEM_ALREADY_CLAIMED');
    for (const command of ['release', 'complete']) {
      const denied = await staff.call<{ code: string }>(
        'POST',
        itemPath + '/' + command,
        command === 'complete' ? { outcomeCode: 'NO_PERMISSION' } : {},
        { etag: claimed.etag, expected: 409 },
      );
      expect(denied.data.code).toBe('WORK_ITEM_NOT_ASSIGNEE');
    }
    await doctor.call(
      'POST',
      itemPath + '/complete',
      { outcomeCode: 'STALE' },
      { etag: open.etag, expected: 412 },
    );
    const released = await doctor.call<Schema<'WorkItem'>>(
      'POST',
      itemPath + '/release',
      { reasonCode: 'PC02_RELEASE_RECHECK' },
      { etag: claimed.etag },
    );
    expect(released.data.status).toBe('OPEN');
    expect(released.data.dueAt).toBe(open.data.dueAt);
    await staff.call('POST', itemPath + '/claim', {}, { etag: released.etag, expected: 403 });
    expect(await doctor.call<Schema<'WorkItem'>>('GET', itemPath)).toEqual(released);
    await doctor.call('POST', itemPath + '/claim', {}, { etag: released.etag });
    // This report exists only to exercise the queue; reject it instead of leaving
    // synthetic coverage available on the existing staff member's account.
    current = await doctor.call<Schema<'MedicalReport'>>('GET', path);
    await doctor.call(
      'POST',
      path + '/reject',
      { rejectReasonCode: 'PC02_TEST_FINISHED' } satisfies Schema<'RejectMedicalReport'>,
      { etag: current.etag },
    );
    const held = await doctor.call<Schema<'WorkItem'>>('GET', itemPath);
    const completionKey = randomUUID();
    const completed = await doctor.call<Schema<'WorkItem'>>(
      'POST',
      itemPath + '/complete',
      { outcomeCode: 'REJECTED' },
      { etag: held.etag, key: completionKey },
    );
    expect(completed.data.status).toBe('COMPLETED');
    expect(
      await doctor.call<Schema<'WorkItem'>>(
        'POST',
        itemPath + '/complete',
        { outcomeCode: 'REJECTED' },
        { etag: held.etag, key: completionKey },
      ),
    ).toEqual(completed);
    expect(await accounts()).toEqual(before);
    await page.reload();
    await expect(page.getByTestId('report-status')).toHaveText('Reddedildi');
    await test.info().attach('medical-worklist-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        reportId,
        workItemId: itemId,
        ownFileVerified: true,
        ownReportCommandsRefused: true,
        ownReportDecisionControlsAbsent: true,
        ledgerAccountsUnchanged: true,
        reportStatus: 'REJECTED',
        workItemStatus: 'COMPLETED',
      }),
    });
  } finally {
    try {
      if (reportId) {
        const path = `/api/v1/medical-reports/${reportId}`;
        const current = await provider.call<Schema<'MedicalReport'>>('GET', path);
        if (['DRAFT', 'SUBMITTED'].includes(current.data.status))
          await provider.call('POST', path + '/cancel', {}, { etag: current.etag });
        else if (current.data.status === 'UNDER_REVIEW')
          await doctor.call(
            'POST',
            path + '/reject',
            { rejectReasonCode: 'PC02_TEST_CLEANUP' } satisfies Schema<'RejectMedicalReport'>,
            { etag: current.etag },
          );
      }
      if (itemId) {
        const path = `/api/v1/work-items/${itemId}`;
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
            { outcomeCode: 'PC02_TEST_CLEANUP' },
            { etag: current.etag },
          );
      }
    } finally {
      await Promise.all([
        admin.close(),
        provider.close(),
        doctor.close(),
        staff.close(),
        finance.close(),
      ]);
      await staffPage.close();
    }
  }
});
