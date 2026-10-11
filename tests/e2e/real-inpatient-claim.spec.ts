import { expect, request as apiRequest, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const stayId = process.env['E2E_INPATIENT_CLAIM_SOURCE'] ?? '';
const overstay = process.env['E2E_INPATIENT_CLAIM_EXPECT_OVERSTAY'] === '1';
const split = process.env['E2E_INPATIENT_SCENARIO'] === 'split';
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || !stayId,
  'requires operator-started demo and a dedicated discharged inpatient source with unspent holds',
);

test('real discharged inpatient source becomes one invoice-ready claim without duplicate usage', async ({
  page,
}) => {
  test.setTimeout(120000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const billing = new Actor(page.request, 'provider', true);
  const doctor = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const hr = new Actor(await apiRequest.newContext(), 'backoffice', true);
  const provider = new Actor(await apiRequest.newContext(), 'provider', true);
  const ids: Record<string, string> = { stayId };
  let overstayAccepted = false;
  const completeOverstayWorkItems = async () => {
    if (!overstay || !ids['claimId']) return;
    const workItems = (
      await doctor.call<S<'WorkItemPage'>>(
        'GET',
        `/api/v1/work-items?aggregateType=CLAIM&aggregateId=${ids['claimId']}`,
      )
    ).data.items;
    for (const item of workItems) {
      const itemPath = `/api/v1/work-items/${item.id}`;
      let task = await doctor.call<S<'WorkItem'>>('GET', itemPath);
      if (task.data.status === 'OPEN')
        task = await doctor.call<S<'WorkItem'>>(
          'POST',
          itemPath + '/claim',
          {},
          { etag: task.etag },
        );
      if (task.data.status === 'CLAIMED')
        await doctor.call(
          'POST',
          itemPath + '/complete',
          { outcomeCode: 'PC04_OVERSTAY_FINISHED' },
          { etag: task.etag },
        );
    }
  };
  const cleanupOverstayFailure = async () => {
    if (!overstay || !ids['claimId']) return;
    const path = `/api/v1/claims/${ids['claimId']}`;
    const current = await doctor.call<S<'Claim'>>('GET', path);
    if (current.data.status === 'DRAFT')
      await billing.call(
        'POST',
        path + '/cancel',
        { reasonCode: 'PC04_OVERSTAY_CLEANUP' },
        { etag: current.etag },
      );
    if (current.data.status === 'PENDING_MEDICAL')
      await doctor.call(
        'POST',
        path + '/reject',
        { reasonCode: 'PC04_OVERSTAY_CLEANUP' },
        { etag: current.etag },
      );
    const source = (
      await provider.call<S<'InpatientStay'>>('GET', `/api/v1/inpatient-stays/${stayId}`)
    ).data;
    const holds = [
      source.authorizationId,
      ...source.extensions.map((e) => e.authorizationId),
    ].filter((id): id is string => !!id);
    for (const id of new Set(holds)) {
      const holdPath = `/api/v1/authorizations/${id}`;
      const hold = await doctor.call<S<'Authorization'>>('GET', holdPath);
      if (hold.data.status === 'ACTIVE' || hold.data.status === 'PARTIALLY_USED')
        await doctor.call(
          'POST',
          holdPath + '/cancel',
          { reasonCode: 'PC04_OVERSTAY_CLEANUP' },
          { etag: hold.etag },
        );
    }
    await completeOverstayWorkItems();
  };
  try {
    await billing.login('billing.a');
    await doctor.login('doctor.a');
    await hr.login('sponsor.hr');
    await provider.login('provider.a');
    const stay = (
      await provider.call<S<'InpatientStay'>>('GET', `/api/v1/inpatient-stays/${stayId}`)
    ).data;
    expect(stay.status).toBe('DISCHARGED');
    expect(stay.overAuthorization).toBe(overstay);
    if (overstay) {
      expect([stay.authorizedDays, stay.actualDays, stay.releasedDays].map(Number)).toEqual([
        1, 2, 0,
      ]);
      expect(stay.extensions).toHaveLength(0);
    } else if (split) {
      expect([stay.authorizedDays, stay.actualDays, stay.releasedDays].map(Number)).toEqual([
        4, 2, 2,
      ]);
      expect(stay.extensions).toHaveLength(2);
    }
    const person = (await hr.call<S<'Person'>>('GET', `/api/v1/people/${stay.personId}`)).data;
    expect(person.displayName).toMatch(/^Deneme Yatis/);
    ids['caseId'] = stay.caseId;
    ids['personId'] = stay.personId;
    const episode = (
      await provider.call<S<'HealthCase'>>('GET', `/api/v1/health-cases/${stay.caseId}`)
    ).data;
    const clinicalText = episode.encounters
      .map((e) => e.notesClinical)
      .filter((s): s is string => !!s);
    const accounts = async () =>
      (
        await hr.call<{ items: S<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${stay.personId}/entitlements`,
        )
      ).data.items;
    const before = await accounts();
    expect(before).toHaveLength(1);
    const actual = Number(stay.actualDays);
    expect(actual).toBeGreaterThan(0);
    expect(before[0]!.reserved).toBe(overstay ? 1 : actual);
    expect(before[0]!.consumed).toBe(0);
    if (overstay) expect(before[0]!.available).toBe(19);
    ids['accountId'] = before[0]!.id;
    const sourcePath = `/api/v1/claims/case-sources/${stay.caseId}`;
    await provider.call('GET', sourcePath, undefined, { expected: 403 });
    const source = await billing.call<S<'ClaimCaseSourceDetail'>>('GET', sourcePath);
    expect(source.data.lines).toHaveLength(1);
    expect(source.data.lines[0]!.serviceCode).toBe('INPATIENT_DAY');
    expect(Number(source.data.lines[0]!.quantity)).toBe(actual);
    for (const marker of clinicalText) expect(JSON.stringify(source.data)).not.toContain(marker);
    expect(JSON.stringify(source.data)).not.toContain(stay.admissionDiagnosisId!);
    const amount = String(actual * 400); // fixture-specific assertion, not application money arithmetic
    await billing.call(
      'POST',
      sourcePath,
      {
        lines: [
          {
            serviceDefinitionId: source.data.lines[0]!.serviceDefinitionId,
            quantity: String(actual - 1),
            lineAmount: amount,
          },
        ],
      },
      { etag: source.etag, expected: 422 },
    );
    expect(await accounts()).toEqual(before);
    await page.goto(base + `/portal/claims/new?caseId=${stay.caseId}`);
    const form = page.getByTestId('case-claim-form');
    await expect(form).toBeVisible();
    await expect(form.locator('[name="lines.0.quantity"]')).toHaveValue(
      source.data.lines[0]!.quantity,
    );
    await form.getByLabel(/Talep edilen tutar/).fill(amount);
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 950 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      );
      await page.screenshot({
        path: `.impeccable/review/inpatient-claim/source-${width}.png`,
        fullPage: true,
      });
    }
    const creating = page.waitForResponse(
      (r) => new URL(r.url()).pathname === sourcePath && r.request().method() === 'POST',
    );
    await form.getByRole('button', { name: 'Taslağı oluştur', exact: true }).click();
    const createdResponse = await creating;
    expect(createdResponse.status()).toBe(201);
    const created = {
      data: (await createdResponse.json()) as S<'Claim'>,
      etag: createdResponse.headers()['etag']!,
    };
    ids['claimId'] = created.data.id;
    const body = createdResponse.request().postDataJSON();
    const headers = createdResponse.request().headers();
    expect(
      await billing.call<S<'Claim'>>('POST', sourcePath, body, {
        etag: headers['if-match']!,
        key: headers['idempotency-key']!,
        expected: 201,
      }),
    ).toEqual(created);
    await billing.call('POST', sourcePath, body, { etag: headers['if-match']!, expected: 409 });
    const path = `/api/v1/claims/${created.data.id}`;
    const sending = page.waitForResponse(
      (r) => new URL(r.url()).pathname === path + '/submit' && r.request().method() === 'POST',
    );
    await page.getByRole('button', { name: 'Gönder', exact: true }).click();
    const sentResponse = await sending;
    expect(sentResponse.status()).toBe(200);
    const submitted = {
      data: (await sentResponse.json()) as S<'Claim'>,
      etag: sentResponse.headers()['etag']!,
    };
    if (overstay) {
      expect(submitted.data.status).toBe('PENDING_MEDICAL');
      expect(await accounts()).toEqual(before);
      const submitHeaders = sentResponse.request().headers();
      const submitBody: unknown = sentResponse.request().postData()
        ? sentResponse.request().postDataJSON()
        : undefined;
      expect(
        await billing.call<S<'Claim'>>('POST', path + '/submit', submitBody, {
          etag: submitHeaders['if-match']!,
          key: submitHeaders['idempotency-key']!,
        }),
      ).toEqual(submitted);
      expect(await accounts()).toEqual(before);
      const readiness = await billing.call<S<'Problem'>>(
        'GET',
        path + '/invoice-readiness',
        undefined,
        { expected: 409 },
      );
      expect(readiness.data.code).toBe('CLAIM_NOT_DECIDED');
      const clinical = await doctor.call<S<'Claim'>>('GET', path);
      expect(clinical.data.exceptions.some((e) => e.code === 'STAY_OVER_AUTHORIZATION')).toBe(true);
      const decided = await doctor.call<S<'Claim'>>(
        'POST',
        path + '/line-decisions',
        {
          decisions: [
            {
              lineNo: 1,
              decision: 'APPROVED',
              approvedQuantity: '2',
              approvedAmount: '800',
              payerAmount: '800',
              memberAmount: '0',
              reasonCode: 'PC04_OVERSTAY_REVIEW',
            },
          ],
        } satisfies S<'DecideClaimLines'>,
        { etag: clinical.etag },
      );
      expect(decided.data.status).toBe('PENDING_MEDICAL');
      const refused = await doctor.call<S<'Problem'>>(
        'POST',
        path + '/approve',
        { reasonCode: 'PC04_OVERSTAY_REVIEW' },
        { etag: decided.etag, expected: 409 },
      );
      expect(refused.data.code).toBe('CLAIM_INPATIENT_ALLOCATION_MISSING');
      const stillPending = await doctor.call<S<'Claim'>>('GET', path);
      expect(stillPending.data.status).toBe('PENDING_MEDICAL');
      expect(await accounts()).toEqual(before);
      const rejected = await doctor.call<S<'Claim'>>(
        'POST',
        path + '/reject',
        { reasonCode: 'PC04_OVERSTAY_REJECTED' },
        { etag: stillPending.etag },
      );
      expect(rejected.data.status).toBe('REJECTED');
      const after = await accounts();
      expect([after[0]!.available, after[0]!.reserved, after[0]!.consumed]).toEqual([20, 0, 0]);
      const ledger = (
        await hr.call<S<'LedgerPage'>>(
          'GET',
          `/api/v1/entitlement-accounts/${before[0]!.id}/ledger?limit=100`,
        )
      ).data.items;
      expect(ledger.map((row) => row.movementType).sort()).toEqual(['GRANT', 'RELEASE', 'RESERVE']);
      expect(ledger.filter((row) => row.movementType === 'CONSUME')).toHaveLength(0);
      expect((await doctor.call<S<'Claim'>>('GET', path)).data.status).toBe('REJECTED');
      await completeOverstayWorkItems();
      await test.info().attach('inpatient-claim-acceptance', {
        contentType: 'application/json',
        body: JSON.stringify({
          ...ids,
          scenario: 'overstay',
          actualDays: actual,
          claimStatus: 'REJECTED',
          approvalError: refused.data.code,
          available: 20,
          reserved: 0,
          consumed: 0,
        }),
      });
      overstayAccepted = true;
      return;
    }
    expect(submitted.data.status).toBe('APPROVED');
    expect(submitted.data.projection).toBe('FINANCIAL');
    expect(submitted.data.lines[0]!.diagnosisId).toBeUndefined();
    expect(submitted.data.lines[0]!.medicalReportId).toBeUndefined();
    const after = await accounts();
    expect([after[0]!.available, after[0]!.reserved, after[0]!.consumed]).toEqual([
      before[0]!.available,
      0,
      actual,
    ]);
    const submitHeaders = sentResponse.request().headers();
    const submitBody: unknown = sentResponse.request().postData()
      ? sentResponse.request().postDataJSON()
      : undefined;
    expect(
      await billing.call<S<'Claim'>>('POST', path + '/submit', submitBody, {
        etag: submitHeaders['if-match']!,
        key: submitHeaders['idempotency-key']!,
      }),
    ).toEqual(submitted);
    expect(await accounts()).toEqual(after);
    const clinical = (await doctor.call<S<'Claim'>>('GET', path)).data;
    expect(clinical.lines[0]!.diagnosisId).toBe(stay.admissionDiagnosisId);
    const ready = (
      await billing.call<S<'ClaimInvoiceReadiness'>>('GET', path + '/invoice-readiness')
    ).data;
    expect(ready.ready).toBe(true);
    expect([ready.approvedTotal, ready.payerTotal, ready.memberTotal].map(Number)).toEqual([
      actual * 400,
      actual * 400,
      0,
    ]);
    const decision = submitted.data.lines[0]!.decision!;
    expect(
      [
        decision.approvedQuantity,
        decision.approvedAmount,
        decision.payerAmount,
        decision.memberAmount,
      ].map(Number),
    ).toEqual([actual, actual * 400, actual * 400, 0]);
    const claims = (
      await billing.call<S<'ClaimPage'>>('GET', `/api/v1/claims?caseId=${stay.caseId}`)
    ).data.items;
    expect(claims.map((c) => c.id)).toEqual([created.data.id]);
    await page.goto(base + `/portal/claims/${created.data.id}`);
    await expect(page.getByTestId('readiness-verdict')).toContainText('hazır');
    for (const marker of clinicalText)
      expect(await page.locator('body').innerText()).not.toContain(marker);
    const ledger = (
      await hr.call<S<'LedgerPage'>>(
        'GET',
        `/api/v1/entitlement-accounts/${before[0]!.id}/ledger?limit=100`,
      )
    ).data.items;
    const holds = [stay.authorizationId, ...stay.extensions.map((e) => e.authorizationId)].filter(
      (id): id is string => !!id,
    );
    let consumed = 0;
    for (const id of holds) {
      const auth = (await doctor.call<S<'Authorization'>>('GET', `/api/v1/authorizations/${id}`))
        .data;
      const items = auth.items.map((i) => i.id);
      const movements = ledger.filter((m) => items.includes(m.referenceId));
      expect(movements.reduce((sum, m) => sum + m.deltaReserved, 0)).toBe(0);
      consumed += movements.reduce((sum, m) => sum + m.deltaConsumed, 0);
      const consumes = movements.filter((m) => m.movementType === 'CONSUME');
      if (split) {
        expect(consumes).toHaveLength(1);
        expect(consumes[0]!.deltaConsumed).toBe(1);
      } else {
        expect(consumes.length).toBeLessThanOrEqual(1);
      }
    }
    expect(consumed).toBe(actual);
    if (split) expect(holds).toHaveLength(2);
    await test.info().attach('inpatient-claim-acceptance', {
      contentType: 'application/json',
      body: JSON.stringify({
        ...ids,
        scenario: split ? 'split' : 'early',
        allocationScope:
          'Consumption receipts prove hold quantities; extension validity dates do not establish chronological day coverage.',
        actualDays: actual,
        claimStatus: submitted.data.status,
        invoiceReady: ready.ready,
        approvedTotal: ready.approvedTotal,
        payerTotal: ready.payerTotal,
        memberTotal: ready.memberTotal,
        available: after[0]!.available,
        reserved: 0,
        consumed: actual,
      }),
    });
  } finally {
    await test.info().attach('inpatient-claim-fixtures', {
      contentType: 'application/json',
      body: JSON.stringify(ids),
    });
    // Keep the specific claim/version as reviewable evidence; never reset its clinical source.
    try {
      if (!overstayAccepted) await cleanupOverstayFailure();
    } finally {
      await Promise.all([billing.close(), doctor.close(), hr.close(), provider.close()]);
    }
  }
});
