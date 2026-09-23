import { execFile } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { promisify } from 'node:util';
import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type Schema<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || process.env['E2E_AUTOMATIC_REQUEST'] !== '1',
  'requires the operator-started demo, loaded .env and explicit automatic-program opt-in',
);
const execute = promisify(execFile);
async function configure(programId: string, disable = false) {
  const { stdout } = await execute(
    'go',
    ['run', './cmd/seed', 'automatic-program', programId, ...(disable ? ['disable'] : [])],
    { timeout: 60_000, windowsHide: true },
  );
  return JSON.parse(stdout) as { programId: string; planId: string; reviewRequired: boolean };
}

test('real automatic approval is confined to its program, replay-safe and visible to the provider', async ({
  page,
}) => {
  test.setTimeout(120_000);
  const admin = new Actor(await apiRequest.newContext(), 'backoffice');
  const provider = new Actor(await apiRequest.newContext(), 'provider');
  let programId: string | undefined;
  let configured = false;
  const requests: string[] = [];
  try {
    await admin.login('admin.a');
    await provider.login('provider.a');
    const today = new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Istanbul' }).format(
      new Date(),
    );
    const end = new Date(today + 'T12:00:00Z');
    end.setUTCDate(end.getUTCDate() + 2);
    const programs = (
      await admin.call<Schema<'ProgramPage'>>('GET', '/api/v1/programs?q=DEMO_BENEFIT&limit=100')
    ).data.items;
    const baseline = programs.find((p) => p.code === 'DEMO_BENEFIT')!;
    expect(baseline).toBeTruthy();
    const created = await admin.call<Schema<'Program'>>(
      'POST',
      '/api/v1/programs',
      {
        code: `PC02_A_${randomUUID().replaceAll('-', '').toUpperCase()}`,
        name: 'Otomatik karar test hazırlığı',
        programType: baseline.programType,
        sponsorOrganizationId: baseline.sponsorOrganizationId,
        payerOrganizationId: baseline.payerOrganizationId,
        validFrom: today,
        validTo: end.toISOString().slice(0, 10),
      },
      { expected: 201 },
    );
    programId = created.data.id;
    await admin.call(
      'PATCH',
      `/api/v1/programs/${programId}`,
      { name: `PC02 automatic ${programId}` },
      { etag: created.etag },
    );
    configured = true;
    const fixture = await configure(programId);
    expect(fixture.reviewRequired).toBe(false);
    expect(await configure(programId)).toEqual(fixture);
    const person = (
      await admin.call<Schema<'Person'>>(
        'POST',
        '/api/v1/people',
        { firstName: 'Deneme', lastName: 'Otomatikkarar' },
        { expected: 201 },
      )
    ).data;
    const membership = (
      await admin.call<Schema<'SponsorMembership'>>(
        'POST',
        `/api/v1/people/${person.id}/memberships`,
        {
          sponsorOrganizationId: baseline.sponsorOrganizationId,
          membershipType: 'EMPLOYEE',
          validFrom: today,
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
          planId: fixture.planId,
          validFrom: today,
          enrollmentReason: 'PC02_AUTOMATIC_ACCEPTANCE',
        },
        { expected: 201 },
      )
    ).data;
    const accounts = async () =>
      (
        await admin.call<{ items: Schema<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${person.id}/entitlements?asOf=${today}`,
        )
      ).data.items;
    await expect.poll(async () => (await accounts()).length, { timeout: 30_000 }).toBe(1);
    const before = await accounts();
    const catalog = (
      await provider.call<{ items: Schema<'ServiceDefinition'>[] }>(
        'GET',
        '/api/v1/service-definitions?limit=200',
      )
    ).data.items;
    const service = catalog.find((s) => s.code === 'PHYSIO_SESSION')!;
    const existing = (
      await provider.call<Schema<'ServiceRequestPage'>>('GET', '/api/v1/service-requests?limit=100')
    ).data.items;
    const control = existing.find((r) => r.personDisplayName === 'Melis Üye')!;
    expect(control).toBeTruthy();
    const draft = async (personId: string, enrollmentId: string, quantity = '1') => {
      const row = await provider.call<Schema<'ServiceRequest'>>(
        'POST',
        '/api/v1/service-requests',
        {
          personId,
          enrollmentId,
          providerOrganizationId: control.providerOrganizationId!,
          requestType: 'PREAUTHORIZATION',
          channel: 'PROVIDER_PORTAL',
          serviceDate: today,
          items: [
            { serviceDefinitionId: service.id, requestedQuantity: quantity, unitType: 'SESSION' },
          ],
        } satisfies Schema<'CreateServiceRequest'>,
        { expected: 201 },
      );
      requests.push(row.data.id);
      return row;
    };
    const outside = await draft(control.personId, control.enrollmentId);
    expect(
      (
        await provider.call<Schema<'ServiceRequest'>>(
          'POST',
          `/api/v1/service-requests/${outside.data.id}/submit`,
          {},
          { etag: outside.etag },
        )
      ).data.status,
    ).toBe('PENDING_REVIEW');
    const tooMuch = await draft(person.id, enrollment.id, '21');
    expect(
      (
        await provider.call<Schema<'ServiceRequest'>>(
          'POST',
          `/api/v1/service-requests/${tooMuch.data.id}/submit`,
          {},
          { etag: tooMuch.etag },
        )
      ).data.status,
    ).toBe('ELIGIBILITY_FAILED');
    const ready = await draft(person.id, enrollment.id);
    const path = `/api/v1/service-requests/${ready.data.id}`;
    const key = randomUUID();
    const approved = await provider.call<Schema<'ServiceRequest'>>(
      'POST',
      path + '/submit',
      {},
      { etag: ready.etag, key },
    );
    expect(approved.data.status).toBe('APPROVED');
    expect(approved.data.items).toHaveLength(1);
    expect(approved.data.items[0]!.status).toBe('APPROVED');
    expect(Number(approved.data.items[0]!.approvedQuantity)).toBe(1);
    const replay = await provider.call<Schema<'ServiceRequest'>>(
      'POST',
      path + '/submit',
      {},
      { etag: ready.etag, key },
    );
    expect(replay).toEqual(approved);
    await provider.call('POST', path + '/submit', {}, { etag: approved.etag, expected: 409 });
    expect(await accounts()).toEqual(before);
    await page.goto(base + `/portal/requests/${ready.data.id}`);
    await page.getByLabel(/Kullanıcı adı/).fill('provider.a');
    await page
      .getByLabel(/^Parola/)
      .fill(process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora');
    await page.getByRole('button', { name: 'Giriş yap', exact: true }).click();
    await expect(
      page.getByRole('heading', { name: approved.data.reference, exact: true }),
    ).toBeVisible();
    await expect(page.getByRole('main').getByText('Onaylandı', { exact: true }).first()).toHaveText(
      'Onaylandı',
    );
    await expect(page.getByTestId('request-correction')).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Talebi iptal et', exact: true })).toHaveCount(0);
    await test.info().attach('automatic-request-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        programId,
        planId: fixture.planId,
        personId: person.id,
        enrollmentId: enrollment.id,
        requestId: ready.data.id,
        automaticStatus: approved.data.status,
        ledgerAccountsUnchanged: true,
      }),
    });
  } finally {
    // Return this dedicated program to manual review even if request cleanup fails.
    try {
      if (configured && programId)
        expect((await configure(programId, true)).reviewRequired).toBe(true);
    } finally {
      try {
        for (const id of requests) {
          const current = await provider.call<Schema<'ServiceRequest'>>(
            'GET',
            `/api/v1/service-requests/${id}`,
          );
          if (
            [
              'DRAFT',
              'SUBMITTED',
              'PENDING_REVIEW',
              'PENDING_DOCUMENT',
              'ELIGIBILITY_FAILED',
            ].includes(current.data.status)
          ) {
            await provider.call(
              'POST',
              `/api/v1/service-requests/${id}/cancel`,
              { reasonCode: 'PC02_TEST_CLEANUP' },
              { etag: current.etag },
            );
          }
        }
      } finally {
        await Promise.all([admin.close(), provider.close()]);
      }
    }
  }
});
