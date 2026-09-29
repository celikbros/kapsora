import { execFile } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { promisify } from 'node:util';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const existingProgram = process.env['E2E_INPATIENT_PROGRAM'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || process.env['E2E_INPATIENT'] !== '1',
  'requires the operator-started local demo, loaded .env and explicit inpatient opt-in',
);
const execute = promisify(execFile);
const day = (date: Date) =>
  new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Istanbul' }).format(date);
const local = (date: Date) => new Date(date.getTime() + 3 * 3600000).toISOString().slice(0, 16);

// Admission setup uses real APIs; admission, segments, extension and discharge use the
// provider browser. Review decisions reach the stay only through the actual worker.
// This is clinical acceptance, not an inpatient claim/invoice acceptance test.
test('real admission and extension reconcile early discharge against each original hold', async ({
  page,
}) => {
  test.setTimeout(240000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const admin = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const provider = new Actor(page.request, 'provider', true);
  const doctor = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const ids: Record<string, string> = {};
  let accepted = false;
  try {
    await admin.login('admin.a');
    await provider.login('provider.a');
    await doctor.login('doctor.a');
    const me = (await provider.call<S<'UserContext'>>('GET', '/api/v1/me')).data;
    const providerScopes = me.tenants
      .find((t) => t.tenant.code === 'DEMO_A')!
      .scopes!.filter((scope) => scope.type === 'ORGANIZATION' && scope.id);
    expect(providerScopes).toHaveLength(1);
    const providerOrganizationId = providerScopes[0]!.id!;
    const now = new Date();
    const today = day(now);
    const admission = new Date(now.getTime() - 25 * 3600000);
    admission.setSeconds(0, 0);
    let start = day(new Date(now.getTime() - 3 * 86400000));
    const end = day(new Date(now.getTime() + 14 * 86400000));
    const baseline = (
      await admin.call<S<'ProgramPage'>>('GET', '/api/v1/programs?q=DEMO_BENEFIT&limit=100')
    ).data.items.find((p) => p.code === 'DEMO_BENEFIT')!;
    expect(baseline).toBeTruthy();
    const program = existingProgram
      ? await admin.call<S<'Program'>>('GET', `/api/v1/programs/${existingProgram}`)
      : await admin.call<S<'Program'>>(
          'POST',
          '/api/v1/programs',
          {
            code: `PC04_${randomUUID().replaceAll('-', '').toUpperCase()}`,
            name: 'PC04 inpatient fixture preparation',
            programType: baseline.programType,
            sponsorOrganizationId: baseline.sponsorOrganizationId,
            payerOrganizationId: baseline.payerOrganizationId,
            validFrom: start,
            validTo: end,
          },
          { expected: 201 },
        );
    ids['programId'] = program.data.id;
    if (existingProgram) {
      expect(program.data.code).toMatch(/^PC04_[A-F0-9]{32}$/);
      expect(program.data.name).toBe(`PC04 inpatient ${program.data.id}`);
      expect(program.data.validFrom! <= day(admission)).toBe(true);
      expect(program.data.validTo! >= today).toBe(true);
      start = program.data.validFrom!;
    } else {
      await admin.call(
        'PATCH',
        `/api/v1/programs/${program.data.id}`,
        { name: `PC04 inpatient ${program.data.id}` },
        { etag: program.etag },
      );
    }
    const configure = async () => {
      const { stdout } = await execute(
        'go',
        ['run', './cmd/seed', 'inpatient-program', program.data.id],
        { timeout: 60000, windowsHide: true },
      );
      return JSON.parse(stdout) as {
        programId: string;
        planId: string;
        contractId: string;
        serviceDefinitionId: string;
      };
    };
    const fixture = await configure();
    expect(fixture.programId).toBe(program.data.id);
    expect(await configure()).toEqual(fixture);
    ids['planId'] = fixture.planId;
    ids['contractId'] = fixture.contractId;
    const person = (
      await admin.call<S<'Person'>>(
        'POST',
        '/api/v1/people',
        { firstName: 'Deneme', lastName: `Yatis${randomUUID().slice(0, 8)}` },
        { expected: 201 },
      )
    ).data;
    ids['personId'] = person.id;
    const membership = (
      await admin.call<S<'SponsorMembership'>>(
        'POST',
        `/api/v1/people/${person.id}/memberships`,
        {
          sponsorOrganizationId: baseline.sponsorOrganizationId,
          membershipType: 'EMPLOYEE',
          validFrom: start,
        },
        { expected: 201 },
      )
    ).data;
    const enrollment = (
      await admin.call<S<'Enrollment'>>(
        'POST',
        `/api/v1/people/${person.id}/enrollments`,
        {
          sponsorMembershipId: membership.id,
          planId: fixture.planId,
          validFrom: start,
          enrollmentReason: 'PC04_INPATIENT_ACCEPTANCE',
        },
        { expected: 201 },
      )
    ).data;
    ids['enrollmentId'] = enrollment.id;
    const accounts = async () =>
      (
        await admin.call<{ items: S<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${person.id}/entitlements?asOf=${today}`,
        )
      ).data.items;
    await expect.poll(async () => (await accounts()).length, { timeout: 30000 }).toBe(1);
    const balance = async (want: number[]) => {
      const rows = await accounts();
      expect(rows).toHaveLength(1);
      const account = rows[0]!;
      ids['accountId'] = account.id;
      expect([account.available, account.reserved, account.consumed]).toEqual(want);
      expect(account.available + account.reserved + account.consumed + account.expired).toBe(
        account.totalGranted,
      );
      return rows;
    };
    await balance([20, 0, 0]);
    const healthCase = (
      await provider.call<S<'HealthCase'>>(
        'POST',
        '/api/v1/health-cases',
        {
          personId: person.id,
          enrollmentId: enrollment.id,
          caseType: 'INPATIENT',
          providerOrganizationId,
          openedAt: admission.toISOString(),
        } satisfies S<'CreateHealthCase'>,
        { expected: 201 },
      )
    ).data;
    ids['caseId'] = healthCase.id;
    const encounter = (
      await provider.call<S<'Encounter'>>(
        'POST',
        `/api/v1/health-cases/${healthCase.id}/encounters`,
        {
          encounterType: 'INPATIENT',
          startedAt: admission.toISOString(),
          endedAt: admission.toISOString(),
          notesClinical: `Synthetic inpatient ${randomUUID()}`,
        } satisfies S<'CreateEncounter'>,
        { expected: 201 },
      )
    ).data;
    const system = (
      await provider.call<S<'CodeSystemPage'>>('GET', '/api/v1/code-systems?limit=100')
    ).data.items.find((s) => s.code === 'ICD10')!;
    const code = (
      await provider.call<S<'CodeValuePage'>>(
        'GET',
        `/api/v1/code-systems/${system.id}/values?q=J06.9`,
      )
    ).data.items.find((c) => c.code === 'J06.9')!;
    const diagnosis = (
      await provider.call<{ items: S<'Diagnosis'>[] }>(
        'PUT',
        `/api/v1/encounters/${encounter.id}/diagnoses`,
        {
          items: [{ codeValueId: code.id, diagnosisType: 'PRIMARY' }],
        },
      )
    ).data.items[0]!;
    await page.goto(base + `/portal/cases/${healthCase.id}`);
    await page.getByRole('button', { name: 'Yatış talebi', exact: true }).click();
    const form = page.getByTestId('admit-form');
    await form.locator('[name="admissionAt"]').fill(local(admission));
    await form.locator('[name="estimatedDays"]').fill('5');
    await form.locator('[name="admissionDiagnosisId"]').selectOption(diagnosis.id);
    const creating = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === '/api/v1/inpatient-stays' && r.request().method() === 'POST',
    );
    await form.getByRole('button', { name: 'Ön onay iste', exact: true }).click();
    const created = await creating;
    expect(created.status()).toBe(201);
    const first = (await created.json()) as S<'InpatientStay'>;
    ids['stayId'] = first.id;
    ids['requestId'] = first.serviceRequestId;
    expect(first.status).toBe('REQUESTED');
    const stayPath = `/api/v1/inpatient-stays/${first.id}`;
    const getStay = () => provider.call<S<'InpatientStay'>>('GET', stayPath);
    await provider.call(
      'POST',
      '/api/v1/inpatient-stays',
      {
        caseId: healthCase.id,
        providerOrganizationId: healthCase.providerOrganizationId,
        admissionAt: admission.toISOString(),
        estimatedDays: 5,
      },
      { expected: 409 },
    );
    const decide = async (requestId: string, decision: 'approve' | 'reject') => {
      const path = `/api/v1/service-requests/${requestId}`;
      const pending = await doctor.call<S<'ServiceRequest'>>('GET', path);
      expect(pending.data.status).toBe('PENDING_REVIEW');
      await doctor.call(
        'POST',
        path + '/' + decision,
        { reasonCode: 'PC04_TEST_DECISION' },
        { etag: pending.etag },
      );
    };
    await decide(first.serviceRequestId, 'approve');
    await expect
      .poll(async () => (await getStay()).data.status, { timeout: 30000 })
      .toBe('AUTHORIZED');
    let current = await getStay();
    ids['authorizationId'] = current.data.authorizationId!;
    await balance([15, 5, 0]);
    await page.reload();
    for (const [index, kind] of ['WARD', 'COMPANION'].entries()) {
      await page.getByRole('button', { name: 'Segment ekle', exact: true }).click();
      await page.locator(`[name="segments.${index}.type"]`).selectOption(kind);
      await page.locator(`[name="segments.${index}.startsAt"]`).fill(local(admission));
    }
    const saving = page.waitForResponse(
      (r) => new URL(r.url()).pathname === stayPath + '/segments' && r.request().method() === 'PUT',
    );
    await page.getByRole('button', { name: 'Segmentleri kaydet', exact: true }).click();
    expect((await saving).status()).toBe(200);
    await expect(page.getByTestId('stay-status')).toHaveText('Yatan');
    current = await getStay();
    const overlap = await provider.call<S<'Problem'>>(
      'PUT',
      stayPath + '/segments',
      {
        items: [
          { segmentType: 'WARD', startsAt: admission.toISOString() },
          { segmentType: 'ICU', startsAt: admission.toISOString() },
        ],
      },
      { etag: current.etag, expected: 422 },
    );
    expect(overlap.data.errors?.some((error) => error.code === 'OVERLAP')).toBe(true);
    expect(await getStay()).toEqual(current);
    const extendInBrowser = async (days: string) => {
      await page.getByTestId('extend-button').click();
      const dialog = page.getByRole('dialog');
      await dialog.locator('[name="additionalDays"]').fill(days);
      await dialog.locator('[name="reasonCode"]').fill('PC04_TEST_EXTENSION');
      const response = page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === stayPath + '/extensions' && r.request().method() === 'POST',
      );
      await dialog.getByRole('button', { name: 'Uzatma iste', exact: true }).click();
      const result = await response;
      expect(result.status()).toBe(200);
      return (await result.json()) as S<'InpatientStay'>;
    };
    const extended = await extendInBrowser('3');
    expect(extended.extensions).toHaveLength(1);
    await expect(page.getByTestId('extension-pending')).toBeVisible();
    await expect(page.getByTestId('extend-button')).toHaveCount(0);
    current = await getStay();
    await provider.call(
      'POST',
      stayPath + '/extensions',
      { additionalDays: 1, reasonCode: 'PC04_DUPLICATE_EXTENSION' },
      { etag: current.etag, expected: 409 },
    );
    await decide(extended.extensions[0]!.serviceRequestId, 'approve');
    await expect
      .poll(async () => (await getStay()).data.extensions[0]!.status, { timeout: 30000 })
      .toBe('APPROVED');
    current = await getStay();
    ids['extensionAuthorizationId'] = current.data.extensions[0]!.authorizationId!;
    expect(ids['extensionAuthorizationId']).not.toBe(ids['authorizationId']);
    expect(Number(current.data.authorizedDays)).toBe(8);
    await balance([12, 8, 0]);
    await page.reload();
    const second = await extendInBrowser('1');
    await decide(second.extensions[1]!.serviceRequestId, 'reject');
    await expect
      .poll(async () => (await getStay()).data.extensions[1]!.status, { timeout: 30000 })
      .toBe('REJECTED');
    await balance([12, 8, 0]);
    await page.reload();
    const dischargeAt = new Date();
    dischargeAt.setSeconds(0, 0);
    await page.getByRole('button', { name: 'Taburcu et', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await dialog.locator('[name="dischargeAt"]').fill(local(dischargeAt));
    const discharging = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === stayPath + '/discharge' && r.request().method() === 'POST',
    );
    await dialog.getByRole('button', { name: 'Taburcu et', exact: true }).click();
    const dischargeResponse = await discharging;
    expect(dischargeResponse.status()).toBe(200);
    const discharged = {
      data: (await dischargeResponse.json()) as S<'InpatientStay'>,
      etag: dischargeResponse.headers()['etag']!,
    };
    expect(discharged.data.status).toBe('DISCHARGED');
    expect(
      [
        discharged.data.authorizedDays,
        discharged.data.actualDays,
        discharged.data.releasedDays,
      ].map(Number),
    ).toEqual([8, 2, 6]);
    expect(discharged.data.overAuthorization).toBe(false);
    expect(discharged.data.segments).toHaveLength(2);
    for (const segment of discharged.data.segments)
      expect(segment.endsAt).toBe(discharged.data.dischargeAt);
    const after = await balance([18, 2, 0]);
    const replay = await provider.call(
      'POST',
      stayPath + '/discharge',
      dischargeResponse.request().postDataJSON(),
      {
        etag: dischargeResponse.request().headers()['if-match']!,
        key: dischargeResponse.request().headers()['idempotency-key']!,
      },
    );
    expect(replay).toEqual(discharged);
    await provider.call(
      'POST',
      stayPath + '/discharge',
      { dischargeAt: dischargeAt.toISOString() },
      { etag: discharged.etag, expected: 409 },
    );
    expect(await accounts()).toEqual(after);
    const ledger = (
      await admin.call<S<'LedgerPage'>>(
        'GET',
        `/api/v1/entitlement-accounts/${ids['accountId']}/ledger?limit=100`,
      )
    ).data.items;
    expect(ledger.map((r) => r.movementType).sort()).toEqual([
      'GRANT',
      'RELEASE',
      'RELEASE',
      'RESERVE',
      'RESERVE',
    ]);
    for (const [key, held] of [
      ['authorizationId', 2],
      ['extensionAuthorizationId', 0],
    ] as const) {
      const authorization = (
        await doctor.call<S<'Authorization'>>('GET', `/api/v1/authorizations/${ids[key]}`)
      ).data;
      const itemIds = authorization.items.map((item) => item.id);
      const movements = ledger.filter((row) => itemIds.includes(row.referenceId));
      expect(movements.reduce((n, row) => n + row.deltaReserved, 0)).toBe(held);
      expect(movements.filter((row) => row.movementType === 'RELEASE')).toHaveLength(1);
    }
    const reconciliation = (
      await provider.call<S<'StayReconciliation'>>('GET', stayPath + '/reconciliation')
    ).data;
    expect(
      [reconciliation.authorizedDays, reconciliation.actualDays, reconciliation.releasedDays].map(
        Number,
      ),
    ).toEqual([8, 2, 6]);
    await expect(page.getByTestId('stay-status')).toHaveText('Taburcu');
    await expect(page.getByTestId('reconciliation')).toContainText('Mutabakat');
    accepted = true;
    await test.info().attach('inpatient-clinical-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        ...ids,
        available: 18,
        reserved: 2,
        consumed: 0,
        authorizedDays: 8,
        actualDays: 2,
        releasedDays: 6,
        originalHeld: 2,
        extensionHeld: 0,
        invoiceReadyVerified: false,
      }),
    });
  } finally {
    await test.info().attach('inpatient-fixture-ids', {
      contentType: 'application/json',
      body: JSON.stringify(ids),
    });
    try {
      // Preserve successful evidence and its two-day hold for the later billing step.
      // A failed run releases only the test's own known authorizations.
      if (!accepted && ids['stayId']) {
        const stay = (
          await provider.call<S<'InpatientStay'>>('GET', `/api/v1/inpatient-stays/${ids['stayId']}`)
        ).data;
        const holds = [
          stay.authorizationId,
          ...stay.extensions.map((e) => e.authorizationId),
        ].filter((id): id is string => !!id);
        for (const id of new Set(holds)) {
          const path = `/api/v1/authorizations/${id}`;
          const auth = await doctor.call<S<'Authorization'>>('GET', path);
          if (auth.data.status === 'ACTIVE' || auth.data.status === 'PARTIALLY_USED')
            await doctor.call(
              'POST',
              path + '/cancel',
              { reasonCode: 'PC04_TEST_CLEANUP' },
              { etag: auth.etag },
            );
        }
      }
    } finally {
      await Promise.all([admin.close(), provider.close(), doctor.close()]);
    }
  }
});
