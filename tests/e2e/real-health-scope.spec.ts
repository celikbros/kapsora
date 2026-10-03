import { execFile } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { promisify } from 'node:util';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type Schema<K extends keyof components['schemas']> = components['schemas'][K];
type FixtureReport = {
  tenantId: string;
  personId: string;
  providerId: string;
  reportId: string;
  rowVersion: number;
  status: string;
};
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || process.env['E2E_HEALTH_SCOPE'] !== '1',
  'requires operator-started demo, loaded .env and explicit closed scope fixture opt-in',
);
const execute = promisify(execFile);
async function seedScope(id: string) {
  const { stdout } = await execute('go', ['run', './cmd/seed', 'health-scope', id], {
    timeout: 60_000,
    windowsHide: true,
  });
  return JSON.parse(stdout) as FixtureReport[];
}

test('real report boundaries hide foreign providers and tenants on reads, writes and screen', async ({
  page,
}) => {
  test.setTimeout(120_000);
  page.setDefaultTimeout(15_000);
  const provider = new Actor(page.request, 'provider', true);
  const doctor = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const fixtureId = process.env['E2E_HEALTH_SCOPE_FIXTURE'] ?? randomUUID();
  // No demo account exists with clinical access to DEMO_B. The local seed creates
  // and reads that tenant's real report; authenticated DEMO_A sessions must hide it.
  const fixtures = await seedScope(fixtureId);
  await test.info().attach('scope-fixture', {
    contentType: 'application/json',
    body: JSON.stringify({ fixtureId, reports: fixtures }),
  });
  expect(fixtures).toHaveLength(2);
  const [sameTenant, otherTenant] = fixtures as [FixtureReport, FixtureReport];
  expect(sameTenant.tenantId).not.toBe(otherTenant.tenantId);
  try {
    await provider.login('provider.a');
    await doctor.login('doctor.a');
    const own = (
      await provider.call<Schema<'MedicalReportPage'>>('GET', '/api/v1/medical-reports?limit=1')
    ).data.items[0]!;
    expect(own).toBeTruthy();
    await provider.call('GET', `/api/v1/medical-reports/${own.id}`);
    expect(own.issuingProviderOrganizationId).not.toBe(sameTenant.providerId);
    const visible = await doctor.call<Schema<'MedicalReport'>>(
      'GET',
      `/api/v1/medical-reports/${sameTenant.reportId}`,
    );
    expect(visible.data.clinicalSummary).toBe(`Synthetic scope acceptance ${fixtureId}`);
    expect(visible.data.status).toBe('CANCELLED');
    expect(
      (
        await doctor.call<Schema<'MedicalReportPage'>>(
          'GET',
          `/api/v1/medical-reports?personId=${sameTenant.personId}`,
        )
      ).data.items.map((r) => r.id),
    ).toEqual([sameTenant.reportId]);

    for (const record of fixtures) {
      expect(record.status).toBe('CANCELLED');
      const path = `/api/v1/medical-reports/${record.reportId}`;
      const options = { expected: 404, etag: `"${record.rowVersion}"` };
      for (const suffix of ['', '/usages']) {
        const denied = await provider.call<{ code: string }>(
          'GET',
          path + suffix,
          undefined,
          options,
        );
        expect(denied.data.code).toBe('MEDICAL_REPORT_NOT_FOUND');
      }
      expect(
        (
          await provider.call<Schema<'MedicalReportPage'>>(
            'GET',
            `/api/v1/medical-reports?personId=${record.personId}`,
          )
        ).data.items,
      ).toEqual([]);
      await provider.call(
        'PATCH',
        path,
        {
          reportType: 'PC03_SCOPE',
          issuedAt: visible.data.issuedAt,
          validFrom: visible.data.validFrom,
          validTo: visible.data.validTo,
          clinicalSummary: 'Unexpected scope write',
        } satisfies Schema<'PatchMedicalReportDraft'>,
        options,
      );
      await provider.call('PUT', path + '/services', { items: [] }, options);
      for (const command of ['submit', 'cancel']) {
        await provider.call('POST', path + '/' + command, {}, options);
      }
      await page.goto(base + `/portal/reports/${record.reportId}`);
      await expect(page.getByRole('alert')).toContainText('Tedavi raporu bulunamadı');
      await expect(page.getByTestId('report-summary')).toHaveCount(0);
      await expect(page.getByTestId('document-upload-form')).toHaveCount(0);
      await expect(page.locator('body')).not.toContainText(
        `Synthetic scope acceptance ${fixtureId}`,
      );
    }
    const foreignPath = `/api/v1/medical-reports/${otherTenant.reportId}`;
    for (const suffix of ['', '/usages']) {
      await doctor.call('GET', foreignPath + suffix, undefined, { expected: 404 });
    }
    expect(
      (
        await doctor.call<Schema<'MedicalReportPage'>>(
          'GET',
          `/api/v1/medical-reports?personId=${otherTenant.personId}`,
        )
      ).data.items,
    ).toEqual([]);
    for (const command of ['start-review', 'approve', 'reject']) {
      await doctor.call(
        'POST',
        foreignPath + '/' + command,
        command === 'reject' ? { rejectReasonCode: 'PC03_SCOPE_REFUSED' } : {},
        { expected: 404, etag: `"${otherTenant.rowVersion}"` },
      );
    }
    const unknown = await provider.call<{ code: string }>(
      'GET',
      `/api/v1/medical-reports/${randomUUID()}`,
      undefined,
      { expected: 404 },
    );
    expect(unknown.data.code).toBe('MEDICAL_REPORT_NOT_FOUND');
    // Repeat setup is read-only for these closed records, and checks their marker,
    // provider, status and row version after every refused mutation.
    expect(await seedScope(fixtureId)).toEqual(fixtures);
    expect(await doctor.call('GET', `/api/v1/medical-reports/${sameTenant.reportId}`)).toEqual(
      visible,
    );
    await test.info().attach('health-report-scope-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        fixtureId,
        reports: fixtures,
        foreignProviderHidden: true,
        foreignTenantHidden: true,
        writesRefused: true,
      }),
    });
  } finally {
    await Promise.all([provider.close(), doctor.close()]);
  }
});
