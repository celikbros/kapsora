import { mkdir } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { expect, type Page, type Response } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import type { Actor } from './real-api-actor';

type Schema<K extends keyof components['schemas']> = components['schemas'][K];
const execute = promisify(execFile);
export async function seedClaimReviewRule(personId: string, retire = false) {
  const { stdout } = await execute(
    'go',
    ['run', './cmd/seed', 'claim-review-rule', personId, ...(retire ? ['retire'] : [])],
    { timeout: 60_000, windowsHide: true },
  );
  return JSON.parse(stdout) as { versionId: string; status: string };
}

/** Real UI commands with public-API evidence; runs only on its dedicated synthetic episode. */
export async function reviewAndCorrectClaim(input: {
  base: string;
  claimId: string;
  reportId: string;
  doctor: Actor;
  finance: Actor;
  billing: Actor;
  doctorPage: Page;
  financePage: Page;
  billingPage: Page;
  snapshot: (expected: number[]) => Promise<unknown>;
}) {
  const {
    base,
    claimId,
    reportId,
    doctor,
    finance,
    billing,
    doctorPage,
    financePage,
    billingPage,
    snapshot,
  } = input;
  const path = `/api/v1/claims/${claimId}`;
  const medicalNote = `Synthetic clinical review ${claimId}`;
  const read = (actor: Actor) => actor.call<Schema<'Claim'>>('GET', path);
  const commandResponse = (page: Page, command: string) =>
    page.waitForResponse(
      (r) => new URL(r.url()).pathname === path + command && r.request().method() === 'POST',
    );
  const replay = async (actor: Actor, response: Response) => {
    expect(response.status()).toBe(200);
    const request = response.request();
    const expected = {
      data: (await response.json()) as Schema<'Claim'>,
      etag: response.headers()['etag']!,
    };
    const usagesBefore = await doctor.call<Schema<'MedicalReportUsagePage'>>(
      'GET',
      `/api/v1/medical-reports/${reportId}/usages`,
    );
    const before = await snapshot(expected.data.status === 'RETURNED' ? [19, 1, 0] : [19, 0, 1]);
    expect(
      await actor.call<Schema<'Claim'>>(
        'POST',
        new URL(request.url()).pathname,
        request.postData() ? request.postDataJSON() : undefined,
        { etag: request.headers()['if-match']!, key: request.headers()['idempotency-key']! },
      ),
    ).toEqual(expected);
    expect(await snapshot(expected.data.status === 'RETURNED' ? [19, 1, 0] : [19, 0, 1])).toEqual(
      before,
    );
    expect(
      await doctor.call<Schema<'MedicalReportUsagePage'>>(
        'GET',
        `/api/v1/medical-reports/${reportId}/usages`,
      ),
    ).toEqual(usagesBefore);
    return expected;
  };
  const returnClaim = async (actor: Actor, page: Page, version: number) => {
    await page.goto(base + `/claims/${claimId}`);
    await page.getByRole('button', { name: 'İade et', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await dialog.locator('[name="reasonCode"]').fill('AMOUNT_CORRECTION');
    await dialog.locator('[name="reasonText"]').fill('Deneme tutarını düzeltiniz.');
    const response = commandResponse(page, '/return');
    await dialog.getByRole('button', { name: 'İade et', exact: true }).click();
    const returned = await replay(actor, await response);
    expect(returned.data).toMatchObject({ status: 'RETURNED', currentVersionNo: version + 1 });
    const previous = (
      await doctor.call<Schema<'ClaimVersion'>>('GET', path + `/versions/${version}`)
    ).data;
    expect(previous.version).toMatchObject({
      status: 'SUPERSEDED',
      returnReasonCode: 'AMOUNT_CORRECTION',
    });
    if (version === 2) {
      expect(previous.lines[0]!.decision).toMatchObject({
        stage: 'MEDICAL',
        reasonText: medicalNote,
      });
      const financialHistory = await finance.call<Schema<'ClaimVersion'>>(
        'GET',
        path + '/versions/2',
      );
      expect(JSON.stringify(financialHistory)).not.toContain(medicalNote);
    }
  };
  const correct = async (amount: string, version: number) => {
    await billingPage.goto(base + `/portal/claims/${claimId}`);
    await expect(billingPage.getByTestId('claim-status')).toHaveText('İade edildi');
    const capture = async (state: string) => {
      if (process.env['E2E_REVIEW_CAPTURE'] !== '1' || version !== 2) return;
      await mkdir('.impeccable/review/claim-correction', { recursive: true });
      for (const width of [1440, 390]) {
        await billingPage.setViewportSize({ width, height: 950 });
        await expect(billingPage.locator('[name="lines.0.lineAmount"]')).toBeVisible();
        expect(
          await billingPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
        ).toBe(true);
        await billingPage.screenshot({
          path: `.impeccable/review/claim-correction/${state}-${width}.png`,
          fullPage: true,
        });
      }
      await billingPage.setViewportSize({ width: 1440, height: 950 });
    };
    await capture('returned');
    let releaseRefresh = () => {};
    const refreshGate = new Promise<void>((resolve) => {
      releaseRefresh = resolve;
    });
    let seenRefresh = () => {};
    const refreshSeen = new Promise<void>((resolve) => {
      seenRefresh = resolve;
    });
    await billingPage.route(base + path, async (route) => {
      if (route.request().method() === 'GET') {
        seenRefresh();
        await refreshGate;
      }
      await route.continue();
    });
    const saved = billingPage.waitForResponse(
      (r) => new URL(r.url()).pathname === path + '/lines' && r.request().method() === 'PUT',
    );
    await billingPage.locator('[name="lines.0.lineAmount"]').fill(amount);
    await billingPage.getByRole('button', { name: 'Satırları kaydet', exact: true }).click();
    try {
      expect((await saved).status()).toBe(200);
      await refreshSeen;
      await expect(billingPage.getByRole('button', { name: 'Gönder', exact: true })).toBeDisabled();
      await expect(billingPage.locator('[name="lines.0.lineAmount"]')).toBeDisabled();
      await capture('saving');
    } finally {
      releaseRefresh();
    }
    const sent = commandResponse(billingPage, '/submit');
    await billingPage.getByRole('button', { name: 'Gönder', exact: true }).click();
    const submitted = await replay(billing, await sent);
    await billingPage.unroute(base + path);
    expect(submitted.data).toMatchObject({ status: 'PENDING_MEDICAL', currentVersionNo: version });
  };
  const decideMedical = async () => {
    await doctorPage.goto(base + `/claims/${claimId}`);
    await expect(doctorPage.getByTestId('claim-lines')).toHaveAttribute(
      'data-projection',
      'CLINICAL',
    );
    await doctorPage.locator('[name="decisions.1.reasonCode"]').fill('CLINICALLY_VALID');
    await doctorPage.locator('[name="decisions.1.reasonText"]').fill(medicalNote);
    await doctorPage.locator('[name="reviewComment"]').fill(medicalNote);
    const saved = commandResponse(doctorPage, '/line-decisions');
    await doctorPage.getByTestId('save-decisions').click();
    expect((await replay(doctor, await saved)).data.status).toBe('PENDING_FINANCIAL');
  };

  const pending = await read(doctor);
  expect(pending.data.status).toBe('PENDING_MEDICAL');
  const decision = {
    decisions: [
      {
        lineNo: 1,
        decision: 'APPROVED',
        approvedQuantity: '1',
        approvedAmount: '400',
        payerAmount: '400',
        memberAmount: '0',
        reasonCode: 'WITHIN_TARIFF',
      },
    ],
  };
  await finance.call('POST', path + '/line-decisions', decision, {
    etag: pending.etag,
    expected: 409,
  });
  await doctor.call(
    'POST',
    path + '/return',
    { reasonCode: 'AMOUNT_CORRECTION' },
    { etag: `"${pending.data.rowVersion - 1}"`, expected: 412 },
  );
  await billing.call('GET', path + '/invoice-readiness', undefined, { expected: 409 });
  expect(await read(doctor)).toEqual(pending);
  await snapshot([19, 0, 1]);
  await returnClaim(doctor, doctorPage, 1);
  await correct('450', 2);
  await decideMedical();
  await financePage.goto(base + `/claims/${claimId}`);
  await expect(financePage.getByTestId('claim-lines')).toHaveAttribute(
    'data-projection',
    'FINANCIAL',
  );
  expect(await financePage.locator('body').innerText()).not.toContain(medicalNote);
  expect(JSON.stringify(await read(finance))).not.toContain(medicalNote);
  await returnClaim(finance, financePage, 2);
  await correct('400', 3);
  await decideMedical();
  await financePage.goto(base + `/claims/${claimId}`);
  for (const [field, value] of Object.entries({
    approvedQuantity: '1',
    approvedAmount: '400',
    payerAmount: '400',
    memberAmount: '0',
    reasonCode: 'WITHIN_TARIFF',
  }))
    await financePage.locator(`[name="decisions.1.${field}"]`).fill(value);
  const saved = commandResponse(financePage, '/line-decisions');
  await financePage.getByTestId('save-decisions').click();
  await replay(finance, await saved);
  await financePage.getByRole('button', { name: 'Onayla', exact: true }).click();
  const dialog = financePage.getByRole('dialog');
  await dialog.locator('[name="reasonCode"]').fill('WITHIN_TARIFF');
  const approved = commandResponse(financePage, '/approve');
  await dialog.getByRole('button', { name: 'Onayla', exact: true }).click();
  expect((await replay(finance, await approved)).data.status).toBe('APPROVED');
  const versions = (await doctor.call<Schema<'ClaimVersionList'>>('GET', path + '/versions')).data
    .items;
  expect(versions.map((v) => [v.versionNo, v.status]).sort()).toEqual([
    [1, 'SUPERSEDED'],
    [2, 'SUPERSEDED'],
    [3, 'SUBMITTED'],
  ]);
  expect(
    Number(
      (await doctor.call<Schema<'ClaimVersion'>>('GET', path + '/versions/1')).data.lines[0]!
        .lineAmount,
    ),
  ).toBe(400);
  expect(
    Number(
      (await doctor.call<Schema<'ClaimVersion'>>('GET', path + '/versions/2')).data.lines[0]!
        .lineAmount,
    ),
  ).toBe(450);
  return read(billing);
}
