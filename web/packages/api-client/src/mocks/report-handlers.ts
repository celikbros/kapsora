/**
 * MSW handlers for the reporting endpoints: the provider statement, the daily reconciliation, the
 * operations dashboard, and the exports a person is allowed to take out of the system (WP-I7-05).
 *
 * The mock is a test double of the Go server and a divergence in either direction is a bug. Five
 * things here are transcriptions rather than re-implementations, because they are the five a screen
 * would be built wrongly against:
 *
 *   - **the statement's totals are the server's.** They are computed here from the whole fixture
 *     rather than from the rows the response happens to carry, exactly as the server sums them
 *     over the whole table rather than over a capped page. A screen that added the rows would
 *     disagree with the server for a busy provider and agree with it here — which is the way a
 *     mock lies;
 *   - **nothing creates a reconciliation run.** There is no POST, here or on the server: a run is
 *     written by the `billing.reconcile` job, and it is append-only afterwards;
 *   - **every dashboard figure carries the filter that reproduces it**, and the filters are the
 *     same lists the server's queries use;
 *   - **an export is queued and rendered afterwards.** `createExport` answers 202 with QUEUED and
 *     the mock's worker moves it to READY on the next tick, so a screen has to be built for the
 *     wait rather than for an answer that is never immediate;
 *   - **the file carries the watermark on every row, the download is counted, and after
 *     `expiresAt` it is refused.** All three are the whole point of the package.
 *
 * Every figure is an exact decimal computed in integer micro-units and rendered in the canonical
 * trimmed form the server's `trim_scale` produces. No amount here passes through a binary float.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  fromMicros,
  renderExportBody,
  toMicros,
  type StoredExport,
  type StoredReconciliationRun,
} from './data';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  guardTenant,
  hasPermission,
  organizationScope,
  parseLimit,
  pathParam,
  problem,
  readJson,
  requireIdempotencyKey,
  validationFailed,
  wait,
  withinScope,
  type FieldError,
  type MockApi,
  type MockSession,
  type Schemas,
} from './handlers';
import { NO_STORE } from './health-handlers';

const PERMISSION_REPORT_READ = 'report.read';
const PERMISSION_REPORT_EXPORT = 'report.export';
const PERMISSION_REPORT_EXPORT_SENSITIVE = 'report.export.sensitive';

/** report.export.kind, as migration 000047 writes it. */
const EXPORT_KINDS = new Set<string>([
  'PROVIDER_STATEMENT',
  'BATCH',
  'SETTLEMENTS',
  'CLAIMS',
  'RECONCILIATION',
]);

const EXPORT_STATUSES = new Set<string>(['QUEUED', 'RUNNING', 'READY', 'FAILED', 'EXPIRED']);
const RUN_SCOPES = new Set<string>(['TENANT', 'PROVIDER']);
const RUN_STATUSES = new Set<string>(['BALANCED', 'DIFFERENCES', 'FAILED']);

/** The kind whose rows carry claim line descriptions, and the only one needing the second grant. */
const SENSITIVE_KIND = 'CLAIMS';

/** The documented default of `report.export_ttl_hours`. The mock has no tenant-settings surface. */
const DEFAULT_EXPORT_TTL_HOURS = 24;

/** The claim statuses each dashboard figure counts, exactly as the server's queries filter them. */
const OPEN_CLAIM_STATUSES = [
  'SUBMITTED',
  'AUTO_ADJUDICATED',
  'PENDING_MEDICAL',
  'PENDING_FINANCIAL',
  'RETURNED',
];
const BATCH_STATUSES = ['SUBMITTED', 'UNDER_REVIEW'];
const SETTLEMENT_STATUSES = ['APPROVED', 'POSTED', 'PARTIALLY_PAID'];
const REIMBURSEMENT_STATUSES = ['SUBMITTED', 'UNDER_REVIEW'];
const WORK_ITEM_STATUSES = ['OPEN', 'CLAIMED', 'ESCALATED'];
const AGING_BUCKETS: Schemas['DashboardAgingFigure']['bucket'][] = [
  'D0_1',
  'D2_7',
  'D8_30',
  'D31_PLUS',
];
const DUE_SOON_DAYS = 7;

/** A uuid anywhere in a value, and a key that names an identifier: the server's two tests. */
const UUID_ANYWHERE = /[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/;
const IDENTIFIER_KEY = /^([A-Za-z0-9_]*(Id|_id)|id)$/;
const CURRENCY = /^[A-Z]{3}$/;
const ISO_DATE = /^\d{4}-\d{2}-\d{2}$/;

/** Renders micro-units in the canonical trimmed decimal the server's `trim_scale` produces. */
function amount(micros: bigint): string {
  const text = fromMicros(micros);
  if (!text.includes('.')) return text;
  const trimmed = text.replace(/0+$/, '').replace(/\.$/, '');
  return trimmed === '' || trimmed === '-' ? '0' : trimmed;
}

function sum(values: string[]): string {
  return amount(values.reduce((total, value) => total + toMicros(value), 0n));
}

function dayOf(iso: string): string {
  return iso.slice(0, 10);
}

export function reportHandlers(api: MockApi): HttpHandler[] {
  const world = () => api.world;

  const providerNameOf = (tenantId: string, providerId: string): string => {
    const relationship = world().relationships.find(
      (r) => r.id === providerId && r.tenantId === tenantId,
    );
    if (!relationship) return '';
    return world().organizations.get(relationship.organizationId)?.displayName ?? '';
  };

  const runView = (row: StoredReconciliationRun): Schemas['ReconciliationRun'] => ({
    id: row.id,
    scope: row.scope,
    providerOrganizationId: row.providerOrganizationId,
    providerName: row.providerName,
    periodFrom: row.periodFrom,
    periodTo: row.periodTo,
    runNo: row.runNo,
    currencyCode: row.currencyCode,
    invoicedTotal: row.invoicedTotal,
    approvedTotal: row.approvedTotal,
    cutTotal: row.cutTotal,
    returnedTotal: row.returnedTotal,
    rejectedTotal: row.rejectedTotal,
    settledTotal: row.settledTotal,
    paidTotal: row.paidTotal,
    openTotal: row.openTotal,
    erpTotal: row.erpTotal,
    difference: row.difference,
    differenceCount: row.differenceCount,
    differences: row.differences,
    status: row.status,
    failureCode: row.failureCode,
    ranAt: row.ranAt,
    createdAt: row.createdAt,
  });

  const exportView = (row: StoredExport): Schemas['Export'] => ({
    id: row.id,
    kind: row.kind,
    format: row.format,
    status: row.status,
    parameters: row.parameters,
    providerOrganizationId: row.providerOrganizationId,
    periodFrom: row.periodFrom,
    periodTo: row.periodTo,
    currencyCode: row.currencyCode,
    documentId: row.documentId,
    rowCount: row.rowCount,
    requestedBy: row.requestedBy,
    requestedAt: row.requestedAt,
    expiresAt: row.expiresAt,
    watermark: row.watermark,
    downloadCount: row.downloadCount,
    failureCode: row.failureCode,
    createdAt: row.createdAt,
    rowVersion: row.rowVersion,
  });

  /**
   * The mock's worker. It renders the rows of the kind, stamps the watermark on every one of them
   * and moves the export to READY — which is what a screen polling `getExport` is waiting for.
   *
   * It runs on read rather than on a timer so a test is deterministic: the first read after the
   * request finds QUEUED, and the next one finds the file.
   */
  const render = (row: StoredExport): void => {
    if (row.status !== 'QUEUED') return;
    const columns = ['Referans', 'Vade', 'Ödenecek', 'Ödenen'];
    const rows = world()
      .settlements.filter((s) => s.tenantId === row.tenantId)
      .filter(
        (s) =>
          !row.providerOrganizationId || s.providerOrganizationId === row.providerOrganizationId,
      )
      .map((s) => [s.reference, s.dueDate, s.payableAmount, s.paidAmount]);
    row.body = renderExportBody(row.watermark, columns, rows);
    row.rowCount = rows.length;
    row.documentId = row.documentId ?? row.id;
    row.status = 'READY';
    row.rowVersion += 1;
  };

  /** Resolves the export a route names. Somebody else's is not found rather than refused. */
  const resolveExport = (
    tenantId: string,
    session: MockSession,
    id: string,
  ): StoredExport | Response => {
    const row = world().exports.find((e) => e.tenantId === tenantId && e.id === id);
    if (!row) return problem(api, 404, 'EXPORT_NOT_FOUND', 'Dışa aktarma bulunamadı');
    void session;
    render(row);
    return row;
  };

  const expired = (row: StoredExport): boolean => Date.parse(row.expiresAt) <= Date.now();

  return [
    http.get(`${ANY}/api/v1/providers/:providerId/statement`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_READ, false);
      if ('error' in g) return g.error;
      const providerId = pathParam(params, 'providerId');
      const scope = organizationScope(api, g.session, g.tenantId);
      if (scope !== null && !withinScope(scope, providerId)) {
        return problem(
          api,
          403,
          'REPORT_PROVIDER_SCOPE',
          'Bu sağlayıcının raporlarını göremezsiniz',
        );
      }
      const url = new URL(request.url);
      const from = url.searchParams.get('periodFrom');
      const to = url.searchParams.get('periodTo');
      const currency = url.searchParams.get('currencyCode') ?? 'TRY';
      const errors: FieldError[] = [];
      if (!from || !ISO_DATE.test(from)) {
        errors.push({ field: 'periodFrom', code: 'REQUIRED', message: 'dönem başlangıcı gerekli' });
      }
      if (!to || !ISO_DATE.test(to)) {
        errors.push({ field: 'periodTo', code: 'REQUIRED', message: 'dönem bitişi gerekli' });
      }
      if (!CURRENCY.test(currency)) {
        errors.push({
          field: 'currencyCode',
          code: 'FORMAT',
          message: 'üç büyük harfli para birimi kodu olmalı',
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);
      const periodFrom = from!;
      const periodTo = to!;

      const invoices = world().invoices.filter(
        (i) =>
          i.tenantId === g.tenantId &&
          i.providerOrganizationId === providerId &&
          i.currencyCode === currency &&
          i.invoiceDate >= periodFrom &&
          i.invoiceDate <= periodTo &&
          i.status !== 'DRAFT' &&
          i.status !== 'CANCELLED',
      );
      const batches = world().batches.filter(
        (b) =>
          b.tenantId === g.tenantId &&
          b.providerOrganizationId === providerId &&
          b.currencyCode === currency &&
          (b.status === 'DECIDED' || b.status === 'SETTLING' || b.status === 'CLOSED') &&
          b.decidedAt !== null &&
          dayOf(b.decidedAt) >= periodFrom &&
          dayOf(b.decidedAt) <= periodTo,
      );
      const settlements = world().settlements.filter(
        (s) =>
          s.tenantId === g.tenantId &&
          s.providerOrganizationId === providerId &&
          s.currencyCode === currency &&
          s.status !== 'CANCELLED' &&
          s.dueDate >= periodFrom &&
          s.dueDate <= periodTo,
      );

      const body: Schemas['ProviderStatement'] = {
        providerOrganizationId: providerId,
        providerName: providerNameOf(g.tenantId, providerId),
        periodFrom,
        periodTo,
        currencyCode: currency,
        totals: {
          invoicedTotal: sum(invoices.map((i) => i.payableAmount)),
          approvedTotal: sum(batches.map((b) => b.approvedTotal)),
          cutTotal: sum(batches.map((b) => b.cutTotal)),
          returnedTotal: sum(batches.map((b) => b.returnedTotal)),
          rejectedTotal: sum(batches.map((b) => b.rejectedTotal)),
          settledTotal: sum(settlements.map((s) => s.payableAmount)),
          paidTotal: sum(settlements.map((s) => s.paidAmount)),
          openBalance: amount(
            settlements.reduce(
              (total, s) => total + toMicros(s.payableAmount) - toMicros(s.paidAmount),
              0n,
            ),
          ),
          invoiceCount: invoices.length,
          settlementCount: settlements.length,
        },
        invoices: invoices
          .slice()
          .sort((a, b) => a.invoiceDate.localeCompare(b.invoiceDate) || a.id.localeCompare(b.id))
          .map((i) => {
            const batch = world().batches.find((b) => b.id === i.batchId);
            const membership = world().batchInvoices.find(
              (m) => m.invoiceId === i.id && m.batchId === i.batchId,
            );
            const settlement = world().settlements.find(
              (s) => s.batchId === i.batchId && s.status !== 'CANCELLED',
            );
            return {
              id: i.id,
              invoiceNumber: i.invoiceNumber,
              invoiceDate: i.invoiceDate,
              status: i.status,
              currencyCode: i.currencyCode,
              payableAmount: i.payableAmount,
              taxAmount: i.taxAmount,
              batchId: i.batchId,
              batchReference: batch?.reference ?? null,
              batchStatus: batch?.status ?? null,
              batchDecision: membership?.decision ?? null,
              approvedAmount: membership?.approvedAmount ?? '0',
              settlementId: settlement?.id ?? null,
              settlementReference: settlement?.reference ?? null,
              dueDate: settlement?.dueDate ?? null,
              settlementPaidAmount: settlement?.paidAmount ?? '0',
            };
          }),
        settlements: settlements
          .slice()
          .sort((a, b) => a.dueDate.localeCompare(b.dueDate) || a.id.localeCompare(b.id))
          .map((s) => ({
            id: s.id,
            reference: s.reference,
            batchId: s.batchId,
            batchReference: s.batchReference,
            dueDate: s.dueDate,
            status: s.status,
            currencyCode: s.currencyCode,
            approvedAmount: s.approvedAmount,
            withheldAmount: s.withheldAmount,
            payableAmount: s.payableAmount,
            paidAmount: s.paidAmount,
            openAmount: amount(toMicros(s.payableAmount) - toMicros(s.paidAmount)),
            paymentCount: world().paymentRecords.filter(
              (p) => p.settlementId === s.id && p.status !== 'DISPUTED',
            ).length,
            lastPaidAt:
              world()
                .paymentRecords.filter((p) => p.settlementId === s.id && p.status !== 'DISPUTED')
                .map((p) => p.paidAt)
                .sort()
                .at(-1) ?? null,
          })),
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/reconciliation-runs`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_READ, false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');

      const scopeFilter = url.searchParams.get('scope');
      if (scopeFilter !== null && !RUN_SCOPES.has(scopeFilter)) {
        return validationFailed(api, [
          { field: 'scope', code: 'ENUM', message: 'tanımlı bir mutabakat kapsamı olmalı' },
        ]);
      }
      const status = url.searchParams.get('status');
      if (status !== null && !RUN_STATUSES.has(status)) {
        return validationFailed(api, [
          { field: 'status', code: 'ENUM', message: 'tanımlı bir mutabakat durumu olmalı' },
        ]);
      }
      const provider = url.searchParams.get('providerOrganizationId');
      const periodFrom = url.searchParams.get('periodFrom');
      const periodTo = url.searchParams.get('periodTo');
      const scope = organizationScope(api, g.session, g.tenantId);

      const rows = world()
        .reconciliationRuns.filter((row) => {
          if (row.tenantId !== g.tenantId) return false;
          // A provider-scoped caller reads its own PROVIDER runs and never the tenant-wide ones:
          // a TENANT run is every provider's figures added together.
          if (scope !== null) {
            if (row.providerOrganizationId === null) return false;
            if (!withinScope(scope, row.providerOrganizationId)) return false;
          }
          if (scopeFilter && row.scope !== scopeFilter) return false;
          if (status && row.status !== status) return false;
          if (provider && row.providerOrganizationId !== provider) return false;
          if (periodFrom && row.periodFrom < periodFrom) return false;
          if (periodTo && row.periodTo > periodTo) return false;
          return true;
        })
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      const page = rows.slice(offset, offset + limit);
      const body: Schemas['ReconciliationRunPage'] = {
        items: page.map(runView),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/reconciliation-runs/:runId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_READ, false);
      if ('error' in g) return g.error;
      const id = pathParam(params, 'runId');
      const row = world().reconciliationRuns.find((r) => r.tenantId === g.tenantId && r.id === id);
      if (!row) {
        return problem(api, 404, 'RECONCILIATION_RUN_NOT_FOUND', 'Mutabakat kaydı bulunamadı');
      }
      const scope = organizationScope(api, g.session, g.tenantId);
      if (
        scope !== null &&
        (row.providerOrganizationId === null || !withinScope(scope, row.providerOrganizationId))
      ) {
        return problem(api, 404, 'RECONCILIATION_RUN_NOT_FOUND', 'Mutabakat kaydı bulunamadı');
      }
      return HttpResponse.json(runView(row), { headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/operations/dashboard`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_READ, false);
      if ('error' in g) return g.error;
      const scope = organizationScope(api, g.session, g.tenantId);
      const asOf = new Date();
      const inScope = (providerOrganizationId: string): boolean =>
        scope === null || withinScope(scope, providerOrganizationId);

      const claims = world().claims.filter(
        (c) =>
          c.tenantId === g.tenantId &&
          c.status !== 'CANCELLED' &&
          c.status !== 'SETTLED' &&
          inScope(c.providerOrganizationId),
      );
      const byStatus = new Map<string, typeof claims>();
      for (const claim of claims) {
        byStatus.set(claim.status, [...(byStatus.get(claim.status) ?? []), claim]);
      }
      const claimsByStatus: Schemas['DashboardClaimStatusFigure'][] = [...byStatus.entries()]
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([status, rows]) => ({
          status,
          claimCount: rows.length,
          approvedTotal: sum(rows.map(() => '0')),
          filter: { resource: 'claims', statuses: [status] },
        }));

      const open = claims.filter((c) => OPEN_CLAIM_STATUSES.includes(c.status));
      const bucketOf = (createdAt: string): Schemas['DashboardAgingFigure']['bucket'] => {
        const days = (asOf.getTime() - Date.parse(createdAt)) / 86_400_000;
        if (days < 2) return 'D0_1';
        if (days < 8) return 'D2_7';
        if (days < 31) return 'D8_30';
        return 'D31_PLUS';
      };
      const claimAging: Schemas['DashboardAgingFigure'][] = AGING_BUCKETS.map((bucket) => {
        const rows = open.filter((c) => bucketOf(c.createdAt) === bucket);
        return {
          bucket,
          claimCount: rows.length,
          approvedTotal: '0',
          filter: { resource: 'claims', statuses: OPEN_CLAIM_STATUSES, agingBucket: bucket },
        };
      });

      const batches = world().batches.filter(
        (b) =>
          b.tenantId === g.tenantId &&
          BATCH_STATUSES.includes(b.status) &&
          inScope(b.providerOrganizationId),
      );
      const settlements = world().settlements.filter(
        (s) =>
          s.tenantId === g.tenantId &&
          SETTLEMENT_STATUSES.includes(s.status) &&
          toMicros(s.paidAmount) < toMicros(s.payableAmount) &&
          inScope(s.providerOrganizationId),
      );
      const today = asOf.toISOString().slice(0, 10);
      const weekEnd = new Date(asOf.getTime() + DUE_SOON_DAYS * 86_400_000)
        .toISOString()
        .slice(0, 10);
      const dueSoon = settlements.filter((s) => s.dueDate >= today && s.dueDate <= weekEnd);
      const overdue = settlements.filter((s) => s.dueDate < today);
      const openOf = (rows: typeof settlements): string =>
        amount(
          rows.reduce((total, s) => total + toMicros(s.payableAmount) - toMicros(s.paidAmount), 0n),
        );

      const reimbursements = world().reimbursements.filter(
        (r) => r.tenantId === g.tenantId && REIMBURSEMENT_STATUSES.includes(r.status),
      );
      const workItems = world().workItems.filter(
        (w) =>
          w.tenantId === g.tenantId &&
          WORK_ITEM_STATUSES.includes(w.status) &&
          w.dueAt !== null &&
          w.dueAt !== undefined &&
          Date.parse(w.dueAt) < asOf.getTime(),
      );

      const body: Schemas['OperationsDashboard'] = {
        asOf: asOf.toISOString(),
        claimsByStatus,
        claimAging,
        batchesAwaitingReview: {
          batchCount: batches.length,
          submittedTotal: sum(batches.map((b) => b.submittedTotal)),
          oldestSubmittedAt:
            batches
              .map((b) => b.submittedAt)
              .filter((v): v is string => typeof v === 'string')
              .sort()
              .at(0) ?? null,
          oldestSlaDueAt:
            world()
              .workItems.filter(
                (w) => w.tenantId === g.tenantId && WORK_ITEM_STATUSES.includes(w.status),
              )
              .map((w) => w.dueAt)
              .filter((v): v is string => typeof v === 'string')
              .sort()
              .at(0) ?? null,
          filter: { resource: 'batches', statuses: BATCH_STATUSES },
        },
        settlements: {
          dueSoonCount: dueSoon.length,
          dueSoonTotal: openOf(dueSoon),
          overdueCount: overdue.length,
          overdueTotal: openOf(overdue),
          dueSoonFilter: {
            resource: 'settlements',
            statuses: SETTLEMENT_STATUSES,
            dueFrom: today,
            dueTo: weekEnd,
          },
          overdueFilter: {
            resource: 'settlements',
            statuses: SETTLEMENT_STATUSES,
            dueBefore: today,
          },
        },
        reimbursements: {
          reimbursementCount: reimbursements.length,
          requestedTotal: sum(reimbursements.map((r) => r.requestedAmount)),
          oldestSubmittedAt:
            reimbursements
              .map((r) => r.submittedAt)
              .filter((v): v is string => typeof v === 'string')
              .sort()
              .at(0) ?? null,
          filter: { resource: 'reimbursements', statuses: REIMBURSEMENT_STATUSES },
        },
        workItemsPastSla: {
          itemCount: workItems.length,
          oldestDueAt:
            workItems
              .map((w) => w.dueAt)
              .filter((v): v is string => typeof v === 'string')
              .sort()
              .at(0) ?? null,
          filter: {
            resource: 'workItems',
            statuses: WORK_ITEM_STATUSES,
            overdueAt: asOf.toISOString(),
          },
        },
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/exports`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_READ, false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const kind = url.searchParams.get('kind');
      if (kind !== null && !EXPORT_KINDS.has(kind)) {
        return validationFailed(api, [
          { field: 'kind', code: 'ENUM', message: 'tanımlı bir dışa aktarma türü olmalı' },
        ]);
      }
      const status = url.searchParams.get('status');
      if (status !== null && !EXPORT_STATUSES.has(status)) {
        return validationFailed(api, [
          { field: 'status', code: 'ENUM', message: 'tanımlı bir dışa aktarma durumu olmalı' },
        ]);
      }
      const mine = url.searchParams.get('mine') !== 'false';

      const rows = world()
        .exports.filter((row) => {
          if (row.tenantId !== g.tenantId) return false;
          if (mine && row.requestedBy !== g.session.account.actorId) return false;
          if (kind && row.kind !== kind) return false;
          if (status && row.status !== status) return false;
          return true;
        })
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      const page = rows.slice(offset, offset + limit);
      const body: Schemas['ExportPage'] = {
        items: page.map(exportView),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.post(`${ANY}/api/v1/exports`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_EXPORT, true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const body = await readJson<Schemas['CreateExport']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

      const errors: FieldError[] = [];
      if (!EXPORT_KINDS.has(body.kind)) {
        errors.push({ field: 'kind', code: 'ENUM', message: 'tanımlı bir tür olmalı' });
      }
      const format = body.format ?? 'CSV';
      if (format === 'XLSX') {
        errors.push({
          field: 'format',
          code: 'UNSUPPORTED',
          message: 'bu sürümde yalnızca CSV üretilir; XLSX henüz desteklenmiyor',
        });
      } else if (format !== 'CSV') {
        errors.push({ field: 'format', code: 'ENUM', message: 'geçerli bir dosya biçimi olmalı' });
      }
      if (body.kind === 'PROVIDER_STATEMENT') {
        if (!body.providerOrganizationId) {
          errors.push({
            field: 'providerOrganizationId',
            code: 'REQUIRED',
            message: 'cari ekstre için sağlayıcı verilmeli',
          });
        }
        if (!body.periodFrom || !body.periodTo) {
          errors.push({
            field: 'periodFrom',
            code: 'REQUIRED',
            message: 'cari ekstre için dönem verilmeli',
          });
        }
      }
      // **The filters carry no identifier.** The server's CHECK says the same thing; this is the
      // half that turns it into a field error rather than a 500.
      for (const [key, value] of Object.entries(body.parameters ?? {})) {
        if (IDENTIFIER_KEY.test(key) || UUID_ANYWHERE.test(String(value))) {
          errors.push({
            field: `parameters.${key}`,
            code: 'IDENTIFIER',
            message: 'dışa aktarma filtreleri kimlik taşıyamaz; kapsamı ayrı alanlarda verin',
          });
        }
      }
      if (errors.length > 0) return validationFailed(api, errors);

      if (
        body.kind === SENSITIVE_KIND &&
        !hasPermission(api, g.session, g.tenantId, PERMISSION_REPORT_EXPORT_SENSITIVE)
      ) {
        return problem(
          api,
          403,
          'EXPORT_SENSITIVE_REQUIRED',
          'Bu dışa aktarma için ek yetki gerekli',
          {
            detail:
              'Hasar dosyası satır açıklamalarını dışa aktarmak report.export.sensitive ' +
              'yetkisi ister.',
          },
        );
      }
      const scope = organizationScope(api, g.session, g.tenantId);
      if (
        body.providerOrganizationId &&
        scope !== null &&
        !withinScope(scope, body.providerOrganizationId)
      ) {
        return problem(
          api,
          403,
          'REPORT_PROVIDER_SCOPE',
          'Bu sağlayıcının raporlarını göremezsiniz',
        );
      }

      const id = world().nextId();
      const requestedAt = new Date().toISOString();
      const tenantCode = world().tenants.find((t) => t.id === g.tenantId)?.code ?? g.tenantId;
      const row: StoredExport = {
        id,
        tenantId: g.tenantId,
        kind: body.kind,
        format: 'CSV',
        status: 'QUEUED',
        parameters: (body.parameters ?? {}) as Record<string, unknown>,
        providerOrganizationId: body.providerOrganizationId ?? null,
        periodFrom: body.periodFrom ?? null,
        periodTo: body.periodTo ?? null,
        currencyCode: body.currencyCode ?? null,
        documentId: null,
        rowCount: 0,
        requestedBy: g.session.account.actorId,
        requestedAt,
        expiresAt: new Date(
          Date.parse(requestedAt) + DEFAULT_EXPORT_TTL_HOURS * 3_600_000,
        ).toISOString(),
        watermark: `KAPSORA ${tenantCode} · ${g.session.account.displayName} · ${requestedAt} · ${id}`,
        downloadCount: 0,
        failureCode: null,
        body: null,
        createdAt: requestedAt,
        rowVersion: 1,
      };
      world().exports.push(row);
      return HttpResponse.json(exportView(row), { status: 202, headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/exports/:exportId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_READ, false);
      if ('error' in g) return g.error;
      const found = resolveExport(g.tenantId, g.session, pathParam(params, 'exportId'));
      if (found instanceof Response) return found;
      return HttpResponse.json(exportView(found), { headers: NO_STORE });
    }),

    http.post(`${ANY}/api/v1/exports/:exportId/download`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_EXPORT, true);
      if ('error' in g) return g.error;
      const found = resolveExport(g.tenantId, g.session, pathParam(params, 'exportId'));
      if (found instanceof Response) return found;

      if (found.status === 'FAILED') {
        return problem(api, 409, 'EXPORT_FAILED', 'Dışa aktarma tamamlanamadı');
      }
      if (found.status === 'EXPIRED' || expired(found)) {
        return problem(api, 409, 'EXPORT_EXPIRED', 'Dışa aktarmanın süresi doldu', {
          detail: 'Dosyalar sınırlı süre saklanır; raporu yeniden oluşturun.',
        });
      }
      if (found.status !== 'READY' || found.documentId === null) {
        return problem(api, 409, 'EXPORT_NOT_READY', 'Dışa aktarma henüz hazır değil');
      }
      found.downloadCount += 1;
      const body: Schemas['ExportDownload'] = {
        url: `https://files.kapsora.example/exports/${found.id}?signature=mock`,
        method: 'GET',
        expiresAt: new Date(Date.now() + 5 * 60_000).toISOString(),
        watermark: found.watermark,
        downloadCount: found.downloadCount,
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),
  ];
}
