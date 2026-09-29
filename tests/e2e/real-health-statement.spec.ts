import { createHash } from 'node:crypto';
import { mkdir } from 'node:fs/promises';
import { expect, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const invoiceId = process.env['E2E_HEALTH_BILLING_INVOICE'] ?? '';
const settlementId = process.env['E2E_HEALTH_BILLING_SETTLEMENT'] ?? '';
const otherProviderId = process.env['E2E_OTHER_PROVIDER_ID'] ?? '';

test.use({ trace: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || !invoiceId || !settlementId,
  'requires the operator-run local demo and explicit paid invoice/settlement IDs',
);

function micros(value: string): bigint {
  expect(value).toMatch(/^\d+(?:\.\d{1,6})?$/);
  const [whole, fraction = ''] = value.split('.');
  return BigInt(whole!) * 1_000_000n + BigInt(fraction.padEnd(6, '0'));
}

// The export is RFC 4180 CSV. Read cells, including quoted newlines, without printing them.
function csvRows(input: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = '';
  let quoted = false;
  for (let i = 0; i < input.length; i++) {
    const char = input[i]!;
    if (char === '"') {
      if (quoted && input[i + 1] === '"') {
        cell += '"';
        i++;
      } else quoted = !quoted;
    } else if (char === ',' && !quoted) {
      row.push(cell);
      cell = '';
    } else if ((char === '\r' || char === '\n') && !quoted) {
      if (char === '\r' && input[i + 1] === '\n') i++;
      row.push(cell);
      rows.push(row);
      row = [];
      cell = '';
    } else cell += char;
  }
  expect(quoted, 'CSV quotes must close').toBe(false);
  if (row.length || cell) rows.push([...row, cell]);
  return rows;
}

test('paid HEALTH invoice appears on the own-provider statement and in its worker CSV', async ({
  browser,
}) => {
  test.setTimeout(120_000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  if (process.env['KAPSORA_API_URL'])
    expect(new URL(process.env['KAPSORA_API_URL']).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  page.setDefaultTimeout(15_000);
  const billing = new Actor(page.request, 'provider', true);
  try {
    await billing.login('billing.a');
    const invoice = (await billing.call<S<'Invoice'>>('GET', `/api/v1/invoices/${invoiceId}`)).data;
    const settlement = (
      await billing.call<S<'Settlement'>>('GET', `/api/v1/settlements/${settlementId}`)
    ).data;
    expect(invoice.domainCode).toBe('HEALTH');
    expect(invoice.currencyCode).toBe('TRY');
    expect(micros(invoice.payableAmount)).toBe(micros('800'));
    expect(invoice.batchId).toBe(settlement.batchId);
    expect(settlement.status).toBe('PAID');
    expect(micros(settlement.payableAmount)).toBe(micros('800'));
    expect(micros(settlement.paidAmount)).toBe(micros('800'));
    expect(settlement.providerOrganizationId).toBe(invoice.providerOrganizationId);

    const periodFrom = invoice.invoiceDate;
    const periodTo = [periodFrom, settlement.dueDate, new Date().toISOString().slice(0, 10)]
      .sort()
      .at(-1)!;
    expect(periodFrom <= periodTo).toBe(true);
    const statementPath = `/api/v1/providers/${invoice.providerOrganizationId}/statement?periodFrom=${periodFrom}&periodTo=${periodTo}&currencyCode=TRY`;
    const statement = (await billing.call<S<'ProviderStatement'>>('GET', statementPath)).data;
    expect(statement.providerOrganizationId).toBe(invoice.providerOrganizationId);
    expect(statement.currencyCode).toBe('TRY');
    const invoiceRow = statement.invoices.find((row) => row.id === invoiceId);
    const settlementRow = statement.settlements.find((row) => row.id === settlementId);
    expect(invoiceRow).toBeDefined();
    expect(settlementRow).toBeDefined();
    expect(invoiceRow!.settlementId).toBe(settlementId);
    expect(invoiceRow!.batchDecision).toBe('APPROVE');
    expect(micros(invoiceRow!.payableAmount)).toBe(micros('800'));
    expect(micros(invoiceRow!.approvedAmount)).toBe(micros('800'));
    expect(micros(invoiceRow!.settlementPaidAmount)).toBe(micros('800'));
    expect(micros(settlementRow!.paidAmount)).toBe(micros('800'));
    expect(micros(settlementRow!.openAmount)).toBe(0n);

    // The tenant can contain older rows: compare each aggregate with the same server response.
    // Do not assume provider-wide totals equal this fixture's 800 TRY.
    expect(statement.totals.invoiceCount).toBeGreaterThanOrEqual(statement.invoices.length);
    expect(statement.totals.settlementCount).toBeGreaterThanOrEqual(statement.settlements.length);
    expect(micros(statement.totals.openBalance)).toBe(
      micros(statement.totals.settledTotal) - micros(statement.totals.paidTotal),
    );
    if (otherProviderId && otherProviderId !== invoice.providerOrganizationId) {
      const refused = await billing.call<{ code: string }>(
        'GET',
        statementPath.replace(invoice.providerOrganizationId, otherProviderId),
        undefined,
        { expected: 403 },
      );
      expect(refused.data.code).toBe('REPORT_PROVIDER_SCOPE');
    }

    await page.goto(base + '/portal/billing/statement');
    await page.locator('input[name="periodFrom"]').fill(periodFrom);
    await page.locator('input[name="periodTo"]').fill(periodTo);

    const invoiceUiRow = page
      .getByTestId('statement-invoice')
      .filter({ hasText: invoice.invoiceNumber });
    const settlementUiRow = page
      .getByTestId('statement-settlement')
      .filter({ hasText: settlement.reference });
    await expect(invoiceUiRow).toHaveCount(1);
    await expect(settlementUiRow).toHaveCount(1);
    await expect(invoiceUiRow).toContainText('800');
    await expect(settlementUiRow).toContainText('800');
    await expect(settlementUiRow).toContainText('0');
    const totalsText = await page.getByTestId('statement-totals').innerText();
    for (const amount of [statement.totals.invoicedTotal, statement.totals.openBalance])
      expect(totalsText).toContain(
        Number(amount).toLocaleString('tr-TR', {
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        }),
      );

    const created = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === '/api/v1/exports' &&
        response.request().method() === 'POST',
    );
    await page.getByTestId('statement-export-request').click();
    const createResponse = await created;
    const exportId = ((await createResponse.json()) as S<'Export'>).id;
    await test.info().attach('pc05-statement-export-id', {
      contentType: 'application/json',
      body: JSON.stringify({ invoiceId, settlementId, exportId }),
    });
    expect(createResponse.status()).toBe(202);
    const exportPath = `/api/v1/exports/${exportId}`;
    await expect
      .poll(async () => (await billing.call<S<'Export'>>('GET', exportPath)).data.status, {
        timeout: 60_000,
      })
      .toBe('READY');
    const ready = (await billing.call<S<'Export'>>('GET', exportPath)).data;
    expect(ready.kind).toBe('PROVIDER_STATEMENT');
    expect(ready.format).toBe('CSV');
    expect(ready.providerOrganizationId).toBe(invoice.providerOrganizationId);
    expect(ready.periodFrom).toBe(periodFrom);
    expect(ready.periodTo).toBe(periodTo);
    expect(ready.documentId).toBeTruthy();
    expect(ready.downloadCount).toBe(0);
    expect(ready.rowCount).toBeGreaterThanOrEqual(1);
    expect(new Date(ready.expiresAt).getTime()).toBeGreaterThan(Date.now());
    await expect(page.getByTestId('statement-export-download')).toBeVisible();
    const downloaded = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === `${exportPath}/download` &&
        response.request().method() === 'POST',
    );
    await page.getByTestId('statement-export-download').click();
    const downloadResponse = await downloaded;
    expect(downloadResponse.status()).toBe(200);
    const ticket = (await downloadResponse.json()) as S<'ExportDownload'>;
    expect(ticket.watermark).toBe(ready.watermark);
    expect(ticket.downloadCount).toBe(1);
    await expect(page.getByTestId('statement-export-watermark')).toContainText(ready.watermark);
    await mkdir('.impeccable/review/health-billing', { recursive: true });
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 1000 });
      await expect
        .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
        .toBe(true);
      await page.screenshot({
        path: `.impeccable/review/health-billing/statement-${width}.png`,
        fullPage: true,
      });
    }
    await page.setViewportSize({ width: 1440, height: 1000 });
    const file = await page.request.get(ticket.url);
    expect(file.status()).toBe(200);
    const bytes = await file.body();
    expect(bytes.subarray(0, 3)).toEqual(Buffer.from([0xef, 0xbb, 0xbf]));
    const rows = csvRows(bytes.toString('utf8').slice(1));
    expect(rows[0]).toEqual([ready.watermark]);
    expect(rows[1]?.[0]).toBe('Filigran');
    expect(rows.length - 2).toBe(ready.rowCount);
    for (const row of rows.slice(2)) expect(row[0]).toBe(ready.watermark);
    expect(
      rows
        .slice(2)
        .some(
          (row) =>
            row[1] === invoice.invoiceNumber &&
            micros(row[5]!) === micros('800') &&
            micros(row[10]!) === micros('800'),
        ),
    ).toBe(true);
    const after = (await billing.call<S<'Export'>>('GET', exportPath)).data;
    expect(after.downloadCount).toBe(1);
    await test.info().attach('pc05-statement-evidence', {
      contentType: 'application/json',
      body: JSON.stringify({
        invoiceId,
        settlementId,
        exportId,
        rowCount: ready.rowCount,
        byteCount: bytes.length,
        sha256: createHash('sha256').update(bytes).digest('hex'),
      }),
    });
  } finally {
    await billing.close();
    await page.close();
  }
});
