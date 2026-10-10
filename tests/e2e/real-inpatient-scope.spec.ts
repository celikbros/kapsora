import { execFile } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { promisify } from 'node:util';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
type ScopeStay = {
  tenantId: string;
  providerId: string;
  personId: string;
  caseId: string;
  requestId: string;
  stayId: string;
  rowVersion: number;
  status: string;
};
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || process.env['E2E_INPATIENT_SCOPE'] !== '1',
  'requires operator-started demo, loaded .env and explicit inpatient scope opt-in',
);
const execute = promisify(execFile);
async function seed(id: string): Promise<ScopeStay[]> {
  const { stdout } = await execute('go', ['run', './cmd/seed', 'inpatient-scope', id], {
    timeout: 60_000,
    windowsHide: true,
  });
  return JSON.parse(stdout) as ScopeStay[];
}

test('real inpatient stays hide foreign providers and tenants on reads, commands and screen', async ({
  page,
}) => {
  test.setTimeout(120_000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const provider = new Actor(page.request, 'provider', true);
  const doctor = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const fixtureId = process.env['E2E_INPATIENT_SCOPE_FIXTURE'] ?? randomUUID();
  const fixtures = await seed(fixtureId);
  expect(fixtures).toHaveLength(2);
  const [sameTenant, otherTenant] = fixtures as [ScopeStay, ScopeStay];
  expect(sameTenant.tenantId).not.toBe(otherTenant.tenantId);
  await test.info().attach('inpatient-scope-fixture', {
    contentType: 'application/json',
    body: JSON.stringify({ fixtureId, fixtures }),
  });
  try {
    await provider.login('provider.a');
    await doctor.login('doctor.a');
    const me = (await provider.call<S<'UserContext'>>('GET', '/api/v1/me')).data;
    const ownProvider = me.tenants
      .find((t) => t.tenant.code === 'DEMO_A')!
      .scopes!.find((scope) => scope.type === 'ORGANIZATION')!.id!;
    expect(ownProvider).not.toBe(sameTenant.providerId);
    const visible = await doctor.call<S<'InpatientStay'>>(
      'GET',
      `/api/v1/inpatient-stays/${sameTenant.stayId}`,
    );
    expect(visible.data.status).toBe('CANCELLED');
    expect(visible.data.providerOrganizationId).toBe(sameTenant.providerId);
    expect(visible.data.personId).toBe(sameTenant.personId);
    expect(visible.data.authorizationId).toBeFalsy();
    expect(visible.data.extensions).toEqual([]);
    expect(visible.data.segments).toEqual([]);
    expect(
      (
        await doctor.call<S<'InpatientStayPage'>>(
          'GET',
          `/api/v1/inpatient-stays?caseId=${sameTenant.caseId}`,
        )
      ).data.items.map((row) => row.id),
    ).toEqual([sameTenant.stayId]);

    const unknown = await provider.call<{ code: string }>(
      'GET',
      `/api/v1/inpatient-stays/${randomUUID()}`,
      undefined,
      { expected: 404 },
    );
    expect(unknown.data.code).toBe('INPATIENT_STAY_NOT_FOUND');
    for (const fixture of fixtures) {
      const path = `/api/v1/inpatient-stays/${fixture.stayId}`;
      for (const suffix of ['', '/reconciliation']) {
        const denied = await provider.call<{ code: string }>('GET', path + suffix, undefined, {
          expected: 404,
        });
        expect(denied.data.code).toBe(unknown.data.code);
      }
      for (const filter of [
        `caseId=${fixture.caseId}`,
        `personId=${fixture.personId}`,
        `providerOrganizationId=${fixture.providerId}`,
      ]) {
        expect(
          (await provider.call<S<'InpatientStayPage'>>('GET', `/api/v1/inpatient-stays?${filter}`))
            .data.items,
        ).toEqual([]);
      }
      const options = { expected: 404, etag: `"${fixture.rowVersion}"` };
      await provider.call(
        'POST',
        path + '/extensions',
        { additionalDays: 1, reasonCode: 'PC04_SCOPE_REFUSED' },
        options,
      );
      await provider.call('PUT', path + '/segments', { items: [] }, options);
      await provider.call('POST', path + '/discharge', {}, options);
      await provider.call('POST', path + '/cancel', { reasonCode: 'PC04_SCOPE_REFUSED' }, options);
      await page.goto(base + `/portal/stays/${fixture.stayId}`);
      await expect(page.getByRole('alert')).toBeVisible();
      await expect(page.getByTestId('stay-status')).toHaveCount(0);
      await expect(page.getByTestId('reconciliation')).toHaveCount(0);
      await expect(page.locator('body')).not.toContainText(
        `YatisScope${fixtureId.replaceAll('-', '').toUpperCase()}`,
      );
    }
    await doctor.call('GET', `/api/v1/inpatient-stays/${otherTenant.stayId}`, undefined, {
      expected: 404,
    });
    expect(
      (
        await doctor.call<S<'InpatientStayPage'>>(
          'GET',
          `/api/v1/inpatient-stays?personId=${otherTenant.personId}`,
        )
      ).data.items,
    ).toEqual([]);
    expect(await seed(fixtureId)).toEqual(fixtures);
    expect(await doctor.call('GET', `/api/v1/inpatient-stays/${sameTenant.stayId}`)).toEqual(
      visible,
    );
  } finally {
    await Promise.all([provider.close(), doctor.close()]);
  }
});
