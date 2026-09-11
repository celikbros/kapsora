import { randomId, type KapsoraClient } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type ProviderStatement = components['schemas']['ProviderStatement'];
export type StatementInvoice = components['schemas']['StatementInvoice'];
export type StatementSettlement = components['schemas']['StatementSettlement'];
export type ReconciliationRun = components['schemas']['ReconciliationRun'];
export type ReconciliationDifference = components['schemas']['ReconciliationDifference'];
export type ReconciliationRunPage = components['schemas']['ReconciliationRunPage'];
export type OperationsDashboard = components['schemas']['OperationsDashboard'];
export type DashboardFilter = components['schemas']['DashboardFilter'];
export type Export = components['schemas']['Export'];
export type ExportPage = components['schemas']['ExportPage'];
export type ExportKind = components['schemas']['ExportKind'];
export type ExportStatus = components['schemas']['ExportStatus'];
export type CreateExport = components['schemas']['CreateExport'];
export type ExportDownload = components['schemas']['ExportDownload'];

export interface StatementQuery {
  periodFrom: string;
  periodTo: string;
  currencyCode?: string;
}

export interface ReconciliationRunListQuery {
  cursor?: string;
  limit?: number;
  scope?: 'TENANT' | 'PROVIDER';
  status?: string;
  providerOrganizationId?: string;
  periodFrom?: string;
  periodTo?: string;
}

export interface ExportListQuery {
  cursor?: string;
  limit?: number;
  kind?: ExportKind;
  status?: ExportStatus;
  mine?: boolean;
}

/**
 * What the numbers look like from the outside: the provider's statement, the daily
 * reconciliation, the operations dashboard, and the exports that leave the building with a
 * watermark and an audit row. Every figure is the server's exact decimal string.
 */
export function reportOperations(client: KapsoraClient) {
  const read = (tenantId: string) => ({ 'X-Tenant-ID': tenantId });
  const create = (tenantId: string, idempotencyKey: string) => ({
    'X-Tenant-ID': tenantId,
    'Idempotency-Key': idempotencyKey,
  });

  return {
    async statement(
      tenantId: string,
      providerId: string,
      query: StatementQuery,
    ): Promise<ProviderStatement> {
      const q: StatementQuery = { periodFrom: query.periodFrom, periodTo: query.periodTo };
      if (query.currencyCode) q.currencyCode = query.currencyCode;
      return (
        await unwrap(
          client.GET('/api/v1/providers/{providerId}/statement', {
            params: { header: read(tenantId), path: { providerId }, query: q },
          }),
        )
      ).data;
    },

    async listRuns(
      tenantId: string,
      query: ReconciliationRunListQuery = {},
    ): Promise<ReconciliationRunPage> {
      const q: Record<string, string | number> = {};
      for (const [k, v] of Object.entries(query)) {
        if (v !== undefined && v !== '') q[k] = v;
      }
      return (
        await unwrap(
          client.GET('/api/v1/reconciliation-runs', {
            params: { header: read(tenantId), query: q },
          }),
        )
      ).data;
    },

    async getRun(tenantId: string, runId: string): Promise<ReconciliationRun> {
      return (
        await unwrap(
          client.GET('/api/v1/reconciliation-runs/{runId}', {
            params: { header: read(tenantId), path: { runId } },
          }),
        )
      ).data;
    },

    async dashboard(tenantId: string): Promise<OperationsDashboard> {
      return (
        await unwrap(
          client.GET('/api/v1/operations/dashboard', { params: { header: read(tenantId) } }),
        )
      ).data;
    },

    async listExports(tenantId: string, query: ExportListQuery = {}): Promise<ExportPage> {
      const q: Record<string, string | number | boolean> = {};
      for (const [k, v] of Object.entries(query)) {
        if (v !== undefined && v !== '') q[k] = v;
      }
      return (
        await unwrap(
          client.GET('/api/v1/exports', { params: { header: read(tenantId), query: q } }),
        )
      ).data;
    },

    async createExport(
      tenantId: string,
      body: CreateExport,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Export>> {
      const r = await unwrap(
        client.POST('/api/v1/exports', {
          params: { header: create(tenantId, idempotencyKey) },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getExport(tenantId: string, exportId: string): Promise<Versioned<Export>> {
      const r = await unwrap(
        client.GET('/api/v1/exports/{exportId}', {
          params: { header: read(tenantId), path: { exportId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /** The download is the audited access: one row per call, and the link comes back once. */
    async downloadExport(
      tenantId: string,
      exportId: string,
      body: { purposeCode?: string; reasonText?: string } = {},
      idempotencyKey: string = randomId(),
    ): Promise<ExportDownload> {
      return (
        await unwrap(
          client.POST('/api/v1/exports/{exportId}/download', {
            params: { header: create(tenantId, idempotencyKey), path: { exportId } },
            body,
          }),
        )
      ).data;
    },
  };
}
