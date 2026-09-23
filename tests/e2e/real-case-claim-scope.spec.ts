import { execFile } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { promisify } from 'node:util';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
type Fixture = {
  requestId: string;
  requestVersion: number;
  providerId: string;
  caseId: string;
  encounterId: string;
  claimId: string;
  caseVersion: number;
  encounterVersion: number;
  claimVersion: number;
};
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const sourceId = process.env['E2E_CASE_SCOPE_SOURCE_CLAIM'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || !sourceId,
  'requires operator-started demo, loaded .env and explicit synthetic source claim',
);
const execute = promisify(execFile);
async function seed(id: string): Promise<Fixture> {
  const { stdout } = await execute('go', ['run', './cmd/seed', 'health-case-scope', sourceId, id], {
    timeout: 60_000,
    windowsHide: true,
  });
  return JSON.parse(stdout) as Fixture;
}

test('real provider boundaries hide foreign requests, cases, encounters and claims without mutations', async ({
  page,
  browser,
}) => {
  test.setTimeout(120_000);
  const billingPage = await browser.newPage();
  const provider = new Actor(page.request, 'provider', true);
  const billing = new Actor(billingPage.request, 'provider', true);
  const doctor = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const hr = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const fixtureId = process.env['E2E_CASE_SCOPE_FIXTURE'] ?? randomUUID();
  try {
    await provider.login('provider.a');
    await billing.login('billing.a');
    await doctor.login('doctor.a');
    await hr.login('sponsor.hr');
    const source = await billing.call<S<'Claim'>>('GET', `/api/v1/claims/${sourceId}`);
    const balances = () => hr.call('GET', `/api/v1/people/${source.data.personId}/entitlements`);
    const before = await balances();
    const fixture = await seed(fixtureId);
    await test.info().attach('case-claim-scope-fixture', {
      contentType: 'application/json',
      body: JSON.stringify({ fixtureId, ...fixture }),
    });
    expect(fixture.providerId).not.toBe(source.data.providerOrganizationId);
    const casePath = `/api/v1/health-cases/${fixture.caseId}`;
    const encounterPath = `/api/v1/encounters/${fixture.encounterId}`;
    const claimPath = `/api/v1/claims/${fixture.claimId}`;
    const visibleCase = await doctor.call<S<'HealthCase'>>('GET', casePath);
    const visibleEncounter = await doctor.call<S<'Encounter'>>('GET', encounterPath);
    const visibleClaim = await doctor.call<S<'Claim'>>('GET', claimPath);
    const marker = `Synthetic case scope ${fixtureId}`;
    expect(visibleCase.data.status).toBe('CLOSED');
    expect(visibleEncounter.data.notesClinical).toBe(marker);
    expect(visibleClaim.data.status).toBe('CANCELLED');
    expect(visibleClaim.data.lines[0]!.description).toBe(marker);
    await provider.call('GET', `/api/v1/health-cases/${source.data.caseId}`);
    for (const path of [casePath, encounterPath, encounterPath + '/diagnoses'])
      await provider.call('GET', path, undefined, { expected: 404 });
    const caseOptions = { expected: 404, etag: `"${fixture.caseVersion}"` };
    await provider.call('POST', casePath + '/close', {}, caseOptions);
    await provider.call(
      'POST',
      casePath + '/encounters',
      { encounterType: 'OUTPATIENT', startedAt: new Date().toISOString() },
      caseOptions,
    );
    await provider.call('PUT', encounterPath + '/diagnoses', { items: [] }, { expected: 404 });
    expect(
      (
        await provider.call<S<'HealthCasePage'>>(
          'GET',
          `/api/v1/health-cases?providerOrganizationId=${fixture.providerId}`,
        )
      ).data.items,
    ).toEqual([]);
    for (const suffix of ['', '/versions', '/versions/1', '/invoice-readiness'])
      await billing.call('GET', claimPath + suffix, undefined, { expected: 404 });
    await billing.call('GET', `/api/v1/claims/case-sources/${fixture.caseId}`, undefined, {
      expected: 404,
    });
    const claimOptions = { expected: 404, etag: `"${fixture.claimVersion}"` };
    await billing.call(
      'PATCH',
      claimPath,
      {
        serviceDateFrom: visibleClaim.data.serviceDateFrom,
        serviceDateTo: visibleClaim.data.serviceDateTo,
        channel: 'PROVIDER_PORTAL',
      },
      claimOptions,
    );
    const line = visibleClaim.data.lines[0]!;
    await billing.call(
      'PUT',
      claimPath + '/lines',
      {
        lines: [
          {
            lineNo: 1,
            serviceDefinitionId: line.serviceDefinitionId,
            unitType: line.unitType,
            quantity: '1',
            lineAmount: line.lineAmount,
          },
        ],
      },
      claimOptions,
    );
    for (const command of ['submit', 'cancel'])
      await billing.call(
        'POST',
        claimPath + '/' + command,
        { reasonCode: 'PC03_SCOPE_REFUSED' },
        claimOptions,
      );
    expect(
      (
        await billing.call<S<'ClaimPage'>>(
          'GET',
          `/api/v1/claims?providerOrganizationId=${fixture.providerId}`,
        )
      ).data.items,
    ).toEqual([]);
    await page.goto(base + `/portal/cases/${fixture.caseId}`);
    await expect(page.getByRole('alert')).toContainText('Sağlık vakası bulunamadı');
    await expect(page.locator('body')).not.toContainText(marker);
    await expect(page.getByRole('button', { name: 'Vakayı kapat', exact: true })).toHaveCount(0);
    await billingPage.goto(base + `/portal/claims/${fixture.claimId}`);
    await expect(billingPage.getByRole('alert')).toContainText('Hasar dosyası bulunamadı');
    await expect(billingPage.locator('body')).not.toContainText(marker);
    const requestPath = `/api/v1/service-requests/${fixture.requestId}`;
    const visibleRequest = await doctor.call<S<'ServiceRequest'>>('GET', requestPath);
    expect(visibleRequest.data.status).toBe('CANCELLED');
    expect(visibleRequest.data.providerOrganizationId).toBe(fixture.providerId);
    for (const suffix of ['', '/versions', '/versions/1']) {
      const denied = await provider.call<{ code: string }>('GET', requestPath + suffix, undefined, {
        expected: 404,
      });
      expect(denied.data.code).toBe('SERVICE_REQUEST_NOT_FOUND');
    }
    const requestOptions = { expected: 404, etag: `"${fixture.requestVersion}"` };
    await provider.call(
      'PATCH',
      requestPath,
      { serviceDate: visibleRequest.data.serviceDate },
      requestOptions,
    );
    await provider.call(
      'PUT',
      requestPath + '/items',
      {
        items: [
          {
            serviceDefinitionId: visibleRequest.data.items[0]!.serviceDefinitionId,
            requestedQuantity: '1',
            unitType: visibleRequest.data.items[0]!.unitType,
          },
        ],
      },
      requestOptions,
    );
    for (const command of ['submit', 'cancel'])
      await provider.call(
        'POST',
        requestPath + '/' + command,
        command === 'submit' ? {} : { reasonCode: 'PC02_SCOPE_REFUSED' },
        requestOptions,
      );
    expect(
      (
        await provider.call<S<'ServiceRequestPage'>>(
          'GET',
          `/api/v1/service-requests?providerOrganizationId=${fixture.providerId}`,
        )
      ).data.items,
    ).toEqual([]);
    await page.goto(base + `/portal/requests/${fixture.requestId}`);
    await expect(page.getByRole('alert')).toContainText('Hizmet talebi bulunamadı');
    await expect(page.locator('body')).not.toContainText(visibleRequest.data.reference);
    expect(await doctor.call('GET', requestPath)).toEqual(visibleRequest);
    expect(await seed(fixtureId)).toEqual(fixture);
    expect(await doctor.call('GET', casePath)).toEqual(visibleCase);
    expect(await doctor.call('GET', encounterPath)).toEqual(visibleEncounter);
    expect(await doctor.call('GET', claimPath)).toEqual(visibleClaim);
    expect(await billing.call('GET', `/api/v1/claims/${sourceId}`)).toEqual(source);
    expect(await balances()).toEqual(before);
  } finally {
    await Promise.all([provider.close(), billing.close(), doctor.close(), hr.close()]);
    await billingPage.close();
  }
});
