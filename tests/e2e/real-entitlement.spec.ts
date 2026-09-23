import { randomUUID } from 'node:crypto';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';

type Schema<K extends keyof components['schemas']> = components['schemas'][K];
type Account = Schema<'EntitlementAccount'>;
import { Actor } from './real-api-actor';
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const password = process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base,
  'requires operator-started demo API, UI and worker',
);

function whole(value: number | string): bigint {
  const text = String(value);
  expect(text).toMatch(/^-?\d+(?:\.0+)?$/);
  return BigInt(text.split('.')[0]!);
}
function balance(account: Account) {
  const available = whole(account.available),
    reserved = whole(account.reserved);
  const consumed = whole(account.consumed),
    expired = whole(account.expired);
  expect(available + reserved + consumed + expired).toBe(whole(account.totalGranted));
  return [available, reserved, consumed];
}

test('real enrollment choice and entitlement ledger: reserve, complete once and release only the remainder', async ({
  page,
}) => {
  test.setTimeout(120_000);
  const admin = new Actor(await apiRequest.newContext(), 'backoffice');
  const doctor = new Actor(await apiRequest.newContext(), 'backoffice');
  const provider = new Actor(await apiRequest.newContext(), 'provider');
  let cleanupAuthorizationId: string | undefined;
  try {
    await admin.login('admin.a');
    await doctor.login('doctor.a');
    await provider.login('provider.a');
    const programs = await admin.call<Schema<'ProgramPage'>>('GET', '/api/v1/programs?limit=100');
    const program = programs.data.items.find((p) => p.code === 'DEMO_BENEFIT')!;
    expect(program).toBeTruthy();
    const plans = await admin.call<{ items: Schema<'Plan'>[] }>(
      'GET',
      `/api/v1/programs/${program.id}/plans`,
    );
    const plan = plans.data.items.find((p) => p.code === 'DEMO_STANDARD' && p.status === 'ACTIVE')!;
    expect(plan).toBeTruthy();
    const today = new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Istanbul' }).format(
      new Date(),
    );
    const year = today.slice(0, 4);
    const end = new Date(today + 'T12:00:00Z');
    end.setUTCDate(end.getUTCDate() + 3);
    const validTo = end.toISOString().slice(0, 10);
    const suffix = randomUUID().replaceAll('-', '').slice(0, 12);
    const person = (
      await admin.call<Schema<'Person'>>(
        'POST',
        '/api/v1/people',
        {
          firstName: 'Deneme',
          lastName: `Cokluplan${suffix}`,
        },
        { expected: 201 },
      )
    ).data;
    // Two independent principal memberships of the same published plan exercise real
    // ambiguous enrollment resolution without publishing or modifying plan configuration.
    const enrollments: Schema<'Enrollment'>[] = [];
    for (const [index, membershipType] of ['EMPLOYEE', 'MEMBER'].entries()) {
      const membership = (
        await admin.call<Schema<'SponsorMembership'>>(
          'POST',
          `/api/v1/people/${person.id}/memberships`,
          {
            sponsorOrganizationId: program.sponsorOrganizationId,
            membershipType,
            validFrom: `${year}-01-01`,
          },
          { expected: 201 },
        )
      ).data;
      const enrollment = (
        await admin.call<Schema<'Enrollment'>>(
          'POST',
          `/api/v1/people/${person.id}/enrollments`,
          {
            sponsorMembershipId: membership.id,
            planId: plan.id,
            validFrom: `${year}-01-0${index + 1}`,
            validTo,
            enrollmentReason: 'PC02_SYNTHETIC_ACCEPTANCE',
          },
          { expected: 201 },
        )
      ).data;
      enrollments.push(enrollment);
    }
    const accounts = async () =>
      (
        await admin.call<{ items: Account[] }>(
          'GET',
          `/api/v1/people/${person.id}/entitlements?asOf=${today}`,
        )
      ).data.items;
    await expect
      .poll(
        async () => (await accounts()).filter((a) => a.definition.code === 'PHYSIO_SESSION').length,
        { timeout: 30_000 },
      )
      .toBe(2);
    const original = await accounts();
    const chosen = enrollments[1]!;
    const chosenAccount = original.find(
      (a) => a.enrollmentId === chosen.id && a.definition.code === 'PHYSIO_SESSION',
    )!;
    expect(balance(chosenAccount)).toEqual([20n, 0n, 0n]);
    const untouched = original.filter((a) => a.id !== chosenAccount.id);

    await page.goto(base + '/portal/');
    await page.getByLabel(/Kullanıcı adı/).fill('provider.a');
    await page.getByLabel(/^Parola/).fill(password);
    await page.getByRole('button', { name: 'Giriş yap', exact: true }).click();
    await page.getByLabel('Ada göre ara').fill(`Cokluplan${suffix}`);
    await page
      .getByTestId('member-candidates')
      .getByRole('button', { name: new RegExp(suffix) })
      .click();
    const ambiguity = page.waitForResponse(
      (r) => new URL(r.url()).pathname === '/api/v1/eligibility/checks',
    );
    await page
      .getByRole('combobox', { name: /^Hizmet/ })
      .selectOption({ label: 'Fizyoterapi seansı' });
    const result = await (await ambiguity).json();
    expect(
      result.enrollmentCandidates.map((e: { enrollmentId: string }) => e.enrollmentId).sort(),
    ).toEqual(enrollments.map((e) => e.id).sort());
    const choice = page.getByRole('combobox', { name: /^Plan kaydı/ });
    await expect(choice).toBeVisible();
    await expect(page.getByRole('button', { name: 'Gönder', exact: true })).toHaveCount(0);
    await choice.selectOption(chosen.id);
    await expect(page.getByRole('button', { name: 'Gönder', exact: true })).toBeVisible();
    // The exclusive enrollment end cannot borrow the previously selected plan or verdict.
    const expired = page.waitForResponse(
      (r) => new URL(r.url()).pathname === '/api/v1/eligibility/checks',
    );
    await page.getByLabel(/^Hizmet tarihi/).fill(validTo);
    const expiry = await (await expired).json();
    expect(expiry.eligible).toBe(false);
    await expect(page.getByRole('button', { name: 'Gönder', exact: true })).toHaveCount(0);
    await page.getByLabel(/^Hizmet tarihi/).fill(today);
    await expect(choice).toHaveValue('');
    await choice.selectOption(chosen.id);
    await page.getByLabel(/^Miktar/).fill('3');
    await expect(page.getByRole('button', { name: 'Gönder', exact: true })).toBeVisible();
    const submitted = page.waitForResponse((r) =>
      /\/service-requests\/[^/]+\/submit$/.test(new URL(r.url()).pathname),
    );
    await page.getByRole('button', { name: 'Gönder', exact: true }).click();
    const serviceRequest = (await (await submitted).json()) as Schema<'ServiceRequest'>;
    expect(serviceRequest.enrollmentId).toBe(chosen.id);
    expect(serviceRequest.status).toBe('PENDING_REVIEW');
    expect(await accounts()).toEqual(original);

    const current = await doctor.call<Schema<'ServiceRequest'>>(
      'GET',
      `/api/v1/service-requests/${serviceRequest.id}`,
    );
    await doctor.call(
      'POST',
      `/api/v1/service-requests/${serviceRequest.id}/approve`,
      { reasonCode: 'PC02_TEST_APPROVAL' },
      { etag: current.etag },
    );
    const reserved = await doctor.call<Schema<'Authorization'>>(
      'POST',
      '/api/v1/authorizations',
      {
        requestId: serviceRequest.id,
        validTo: new Date(Date.now() + 86400000).toISOString(),
      },
      { expected: 201 },
    );
    const authorization = reserved.data;
    cleanupAuthorizationId = authorization.id;
    await page.reload();
    await expect(
      page.getByTestId('request-authorization').getByText(authorization.reference, { exact: true }),
    ).toBeVisible();
    const snapshot = async (expected: bigint[]) => {
      const rows = await accounts();
      expect(balance(rows.find((a) => a.id === chosenAccount.id)!)).toEqual(expected);
      expect(rows.filter((a) => a.id !== chosenAccount.id)).toEqual(untouched);
      return rows;
    };
    const afterReserve = await snapshot([17n, 3n, 0n]);
    const line = authorization.items[0]!;
    const body: Schema<'CreateFulfilment'> = {
      authorizationId: authorization.id,
      performedAt: new Date().toISOString(),
      items: [{ authorizationItemId: line.id, actualQuantity: '2' }],
    };
    const recordKey = randomUUID();
    const recorded = await provider.call<Schema<'Fulfilment'>>(
      'POST',
      '/api/v1/fulfilments',
      body,
      { expected: 201, key: recordKey },
    );
    const replay = await provider.call<Schema<'Fulfilment'>>('POST', '/api/v1/fulfilments', body, {
      expected: 201,
      key: recordKey,
    });
    expect(replay.data.id).toBe(recorded.data.id);
    expect(await accounts()).toEqual(afterReserve); // recording does not consume
    const completeKey = randomUUID();
    await provider.call('POST', `/api/v1/fulfilments/${recorded.data.id}/complete`, undefined, {
      etag: recorded.etag,
      key: completeKey,
    });
    const afterComplete = await snapshot([17n, 1n, 2n]);
    await provider.call('POST', `/api/v1/fulfilments/${recorded.data.id}/complete`, undefined, {
      etag: recorded.etag,
      key: completeKey,
    });
    expect(await accounts()).toEqual(afterComplete);
    await provider.call('POST', `/api/v1/fulfilments/${recorded.data.id}/complete`, undefined, {
      etag: recorded.etag,
      expected: 409,
    });
    await provider.call('POST', '/api/v1/fulfilments', body, { expected: 422 });
    await doctor.call('POST', '/api/v1/fulfilments', body, { expected: 403 });
    expect(await accounts()).toEqual(afterComplete);
    const remaining = await doctor.call<Schema<'Authorization'>>(
      'GET',
      `/api/v1/authorizations/${authorization.id}`,
    );
    await doctor.call(
      'POST',
      `/api/v1/authorizations/${authorization.id}/cancel`,
      { reasonCode: 'PC02_UNUSED_RELEASE' },
      { etag: remaining.etag },
    );
    cleanupAuthorizationId = undefined;
    await snapshot([18n, 0n, 2n]);
    const ledger = await admin.call<Schema<'LedgerPage'>>(
      'GET',
      `/api/v1/entitlement-accounts/${chosenAccount.id}/ledger?limit=100`,
    );
    expect(ledger.data.items.map((entry) => entry.movementType).sort()).toEqual([
      'CONSUME',
      'GRANT',
      'RELEASE',
      'RESERVE',
    ]);
    await test.info().attach('entitlement-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        requestId: serviceRequest.id,
        authorizationId: authorization.id,
        fulfilmentId: recorded.data.id,
        accountId: chosenAccount.id,
        final: { available: '18', reserved: '0', consumed: '2' },
        selection: 'second enrollment',
        otherAccountsUnchanged: true,
        duplicateConsumption: false,
        boundary: 'generic fulfilment API; no clinical case, report or claim acceptance',
      }),
    });
    await page.getByRole('button', { name: 'Çıkış yap', exact: true }).click();
  } finally {
    try {
      // Only this run's unused synthetic hold is released after a failed assertion.
      // Completed consumption remains in the append-only ledger.
      if (cleanupAuthorizationId) {
        const current = await doctor.call<Schema<'Authorization'>>(
          'GET',
          `/api/v1/authorizations/${cleanupAuthorizationId}`,
        );
        if (current.data.status === 'ACTIVE' || current.data.status === 'PARTIALLY_USED') {
          await doctor.call(
            'POST',
            `/api/v1/authorizations/${cleanupAuthorizationId}/cancel`,
            { reasonCode: 'PC02_TEST_CLEANUP' },
            { etag: current.etag },
          );
        }
      }
    } finally {
      await Promise.all([admin.close(), doctor.close(), provider.close()]);
    }
  }
});
