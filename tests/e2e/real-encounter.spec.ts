import { randomUUID } from 'node:crypto';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const sourceId = process.env['E2E_ENCOUNTER_SOURCE_CASE'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || !sourceId,
  'requires operator-started demo and a synthetic outpatient source case',
);

test('real primary diagnosis validation and encounter ending unblock case closure without duplicate effect', async ({
  page,
}) => {
  test.setTimeout(120000);
  page.setDefaultTimeout(15000);
  const provider = new Actor(page.request, 'provider', true);
  const hr = new Actor(await apiRequest.newContext(), 'backoffice', true);
  let caseId: string | undefined;
  let encounterId: string | undefined;
  try {
    await provider.login('provider.a');
    await hr.login('sponsor.hr');
    const source = await provider.call<S<'HealthCase'>>('GET', `/api/v1/health-cases/${sourceId}`);
    expect(source.data.status).toBe('CLOSED');
    expect(
      (await hr.call<S<'Person'>>('GET', `/api/v1/people/${source.data.personId}`)).data
        .displayName,
    ).toBe('Deneme Ayaktan');
    const balances = () => hr.call('GET', `/api/v1/people/${source.data.personId}/entitlements`);
    const before = await balances();
    const created = await provider.call<S<'HealthCase'>>(
      'POST',
      '/api/v1/health-cases',
      {
        personId: source.data.personId,
        enrollmentId: source.data.enrollmentId,
        providerOrganizationId: source.data.providerOrganizationId,
        caseType: 'OUTPATIENT',
      } satisfies S<'CreateHealthCase'>,
      { expected: 201 },
    );
    caseId = created.data.id;
    const casePath = `/api/v1/health-cases/${caseId}`;
    const startedAt = new Date(Date.now() - 3600000).toISOString();
    const marker = `Synthetic encounter ${randomUUID()}`;
    const encounter = await provider.call<S<'Encounter'>>(
      'POST',
      casePath + '/encounters',
      {
        encounterType: 'OUTPATIENT',
        startedAt,
        notesClinical: marker,
      } satisfies S<'CreateEncounter'>,
      { expected: 201 },
    );
    encounterId = encounter.data.id;
    const path = `/api/v1/encounters/${encounterId}`;
    const systems = (
      await provider.call<S<'CodeSystemPage'>>('GET', '/api/v1/code-systems?limit=100')
    ).data.items;
    const system = systems.find((s) => s.code === 'ICD10')!;
    const code = async (q: string) =>
      (
        await provider.call<S<'CodeValuePage'>>(
          'GET',
          `/api/v1/code-systems/${system.id}/values?q=${q}`,
        )
      ).data.items.find((c) => c.code === q)!;
    const plain = await code('J06.9');
    const other = await code('F32.1');
    const primary = { codeValueId: plain.id, diagnosisType: 'PRIMARY' as const };
    const valid = await provider.call('PUT', path + '/diagnoses', { items: [primary] });
    const denied = await provider.call<S<'Problem'>>(
      'PUT',
      path + '/diagnoses',
      { items: [primary, { codeValueId: other.id, diagnosisType: 'PRIMARY' }] },
      { expected: 422 },
    );
    expect(denied.data.errors!.some((e) => e.code === 'DUPLICATE_PRIMARY')).toBe(true);
    expect((await provider.call('GET', path + '/diagnoses')).data).toEqual(valid.data);
    // The contract permits zero primary diagnoses; it rejects two, not a secondary-only set.
    await provider.call('PUT', path + '/diagnoses', {
      items: [{ ...primary, diagnosisType: 'SECONDARY' }],
    });
    await provider.call('PUT', path + '/diagnoses', { items: [primary] });
    let currentCase = await provider.call<S<'HealthCase'>>('GET', casePath);
    const blocked = await provider.call<{ code: string }>(
      'POST',
      casePath + '/close',
      {},
      { etag: currentCase.etag, expected: 409 },
    );
    expect(blocked.data.code).toBe('HEALTH_CASE_ENCOUNTER_OPEN');
    expect(await provider.call('GET', casePath)).toEqual(currentCase);
    const endedAt = new Date().toISOString();
    await hr.call('POST', path + '/end', { endedAt }, { etag: encounter.etag, expected: 403 });
    await provider.call('POST', path + '/end', { endedAt }, { etag: '"999999"', expected: 412 });
    await provider.call(
      'POST',
      path + '/end',
      { endedAt: new Date(Date.parse(startedAt) - 1000).toISOString() },
      { etag: encounter.etag, expected: 422 },
    );
    expect(await provider.call('GET', path)).toEqual(encounter);

    await page.goto(base + `/portal/cases/${caseId}`);
    await expect(page.getByRole('button', { name: 'Vakayı kapat', exact: true })).toHaveCount(0);
    await page.getByRole('button', { name: 'Muayeneyi sonlandır', exact: true }).click();
    const form = page.getByTestId('end-encounter-form');
    await expect(form).toBeVisible();
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 950 });
      await form.scrollIntoViewIfNeeded();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      );
      await page.screenshot({
        path: `.impeccable/review/encounter-end-${width}.png`,
        fullPage: true,
      });
    }
    await page.setViewportSize({ width: 1440, height: 950 });
    const attempts: { key: string; etag: string; body: unknown }[] = [];
    await page.route('**' + path + '/end', async (route) => {
      const request = route.request();
      attempts.push({
        key: request.headers()['idempotency-key']!,
        etag: request.headers()['if-match']!,
        body: request.postDataJSON(),
      });
      const response = await route.fetch();
      expect(response.status()).toBe(200);
      if (attempts.length === 1) await route.abort('failed');
      else await route.fulfill({ response });
    });
    await form.getByRole('button', { name: 'Muayeneyi sonlandır', exact: true }).click();
    await expect(form.getByRole('alert')).toBeVisible();
    await expect(form.locator('[name="endedAt"]')).toBeDisabled();
    const committed = await provider.call<S<'Encounter'>>('GET', path);
    expect(committed.data.endedAt).toBeTruthy();
    expect(committed.data.notesClinical).toBe(marker);
    await form.getByRole('button', { name: 'Yeniden dene', exact: true }).click();
    await expect(form).toHaveCount(0);
    expect(attempts).toHaveLength(2);
    expect(attempts[1]).toEqual(attempts[0]);
    expect(await provider.call('GET', path)).toEqual(committed);
    await page.unroute('**' + path + '/end');
    await provider.call(
      'POST',
      path + '/end',
      { endedAt },
      { etag: committed.etag, expected: 409 },
    );
    await page.getByRole('button', { name: 'Vakayı kapat', exact: true }).click();
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Vakayı kapat', exact: true })
      .click();
    await expect(page.getByTestId('case-status')).toHaveText('Kapandı');
    currentCase = await provider.call<S<'HealthCase'>>('GET', casePath);
    expect(currentCase.data.status).toBe('CLOSED');
    await provider.call('PUT', path + '/diagnoses', { items: [primary] }, { expected: 409 });
    await provider.call(
      'POST',
      casePath + '/encounters',
      { encounterType: 'OUTPATIENT', startedAt, endedAt },
      { expected: 409 },
    );
    expect(await balances()).toEqual(before);
    expect(await provider.call('GET', `/api/v1/health-cases/${sourceId}`)).toEqual(source);
    await test.info().attach('encounter-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        caseId,
        encounterId,
        duplicatePrimaryRefused: true,
        endReplayStable: true,
        caseClosed: true,
        accountsUnchanged: true,
      }),
    });
  } finally {
    try {
      if (caseId) {
        if (encounterId) {
          const path = `/api/v1/encounters/${encounterId}`;
          const current = await provider.call<S<'Encounter'>>('GET', path);
          if (!current.data.endedAt)
            await provider.call(
              'POST',
              path + '/end',
              { endedAt: new Date().toISOString() },
              { etag: current.etag },
            );
        }
        const current = await provider.call<S<'HealthCase'>>(
          'GET',
          `/api/v1/health-cases/${caseId}`,
        );
        if (current.data.status === 'OPEN')
          await provider.call(
            'POST',
            `/api/v1/health-cases/${caseId}/close`,
            {},
            { etag: current.etag },
          );
      }
    } finally {
      await Promise.all([provider.close(), hr.close()]);
    }
  }
});
