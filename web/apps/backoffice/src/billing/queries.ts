import type {
  BatchListQuery,
  CreateExport,
  ExportListQuery,
  ReconciliationRunListQuery,
  CancelSettlement,
  CreatePaymentRecord,
  DecideReimbursement,
  RecordReimbursementPayment,
  ReimbursementListQuery,
  ReviewBatchInvoice,
  SettlementListQuery,
} from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

/**
 * The payer's half of billing: the icmal it decides invoice by invoice, the settlement it
 * approves and pays, the member's reimbursement it decides. Every figure is the server's
 * string; the screens add nothing up.
 */

export function useBatches(query: BatchListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['billing', tenantId, 'batches', query],
    queryFn: () => ops.billing.listBatches(tenantId, { limit: 100, ...query }),
  });
}

export function useBatch(batchId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['billing', tenantId, 'batch', batchId],
    queryFn: () => ops.billing.getBatch(tenantId, batchId),
  });
}

export function useBatchSummary(batchId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['billing', tenantId, 'batch-summary', batchId],
    queryFn: () => ops.billing.getBatchSummary(tenantId, batchId),
  });
}

function useInvalidateBatch(batchId: string) {
  const client = useQueryClient();
  const tenantId = useTenantId();
  return () =>
    Promise.all([
      client.invalidateQueries({ queryKey: ['billing', tenantId, 'batch', batchId] }),
      client.invalidateQueries({ queryKey: ['billing', tenantId, 'batch-summary', batchId] }),
      client.invalidateQueries({ queryKey: ['billing', tenantId, 'batches'] }),
      client.invalidateQueries({ queryKey: ['billing', tenantId, 'settlements'] }),
    ]);
}

export function useReviewBatchInvoice(batchId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBatch(batchId);
  return useMutation({
    mutationFn: (input: { invoiceId: string; etag: string; body: ReviewBatchInvoice }) =>
      ops.billing.reviewBatchInvoice(tenantId, batchId, input.invoiceId, input.etag, input.body),
    onSuccess: () => invalidate(),
  });
}

export function useDecideBatch(batchId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBatch(batchId);
  return useMutation({
    mutationFn: (etag: string) => ops.billing.decideBatch(tenantId, batchId, etag),
    onSuccess: () => invalidate(),
  });
}

export function useSettlements(query: SettlementListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['billing', tenantId, 'settlements', query],
    queryFn: () => ops.billing.listSettlements(tenantId, { limit: 100, ...query }),
  });
}

export function useSettlement(settlementId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['billing', tenantId, 'settlement', settlementId],
    queryFn: () => ops.billing.getSettlement(tenantId, settlementId),
  });
}

function useInvalidateSettlement(settlementId: string) {
  const client = useQueryClient();
  const tenantId = useTenantId();
  return () =>
    Promise.all([
      client.invalidateQueries({ queryKey: ['billing', tenantId, 'settlement', settlementId] }),
      client.invalidateQueries({ queryKey: ['billing', tenantId, 'settlements'] }),
    ]);
}

export function useApproveSettlement(settlementId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateSettlement(settlementId);
  return useMutation({
    mutationFn: (etag: string) => ops.billing.approveSettlement(tenantId, settlementId, etag),
    onSuccess: () => invalidate(),
  });
}

export function useCancelSettlement(settlementId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateSettlement(settlementId);
  return useMutation({
    mutationFn: (input: { etag: string; body: CancelSettlement }) =>
      ops.billing.cancelSettlement(tenantId, settlementId, input.etag, input.body),
    onSuccess: () => invalidate(),
  });
}

export function useCreatePaymentRecord(settlementId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateSettlement(settlementId);
  return useMutation({
    mutationFn: (body: CreatePaymentRecord) =>
      ops.billing.createPaymentRecord(tenantId, settlementId, body),
    onSuccess: () => invalidate(),
  });
}

export function useReimbursements(query: ReimbursementListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['billing', tenantId, 'reimbursements', query],
    queryFn: () => ops.billing.listReimbursements(tenantId, { limit: 100, ...query }),
  });
}

export function useReimbursement(reimbursementId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['billing', tenantId, 'reimbursement', reimbursementId],
    queryFn: () => ops.billing.getReimbursement(tenantId, reimbursementId),
  });
}

function useInvalidateReimbursement(reimbursementId: string) {
  const client = useQueryClient();
  const tenantId = useTenantId();
  return () =>
    Promise.all([
      client.invalidateQueries({
        queryKey: ['billing', tenantId, 'reimbursement', reimbursementId],
      }),
      client.invalidateQueries({ queryKey: ['billing', tenantId, 'reimbursements'] }),
    ]);
}

export function useDecideReimbursement(reimbursementId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateReimbursement(reimbursementId);
  return useMutation({
    mutationFn: (input: { etag: string; body: DecideReimbursement }) =>
      ops.billing.decideReimbursement(tenantId, reimbursementId, input.etag, input.body),
    onSuccess: () => invalidate(),
  });
}

export function useRecordReimbursementPayment(reimbursementId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateReimbursement(reimbursementId);
  return useMutation({
    mutationFn: (input: { etag: string; body: RecordReimbursementPayment }) =>
      ops.billing.recordReimbursementPayment(tenantId, reimbursementId, input.etag, input.body),
    onSuccess: () => invalidate(),
  });
}

// --- WP-I7-05: reconciliation, the dashboard, exports ----------------------------------

export function useReconciliationRuns(query: ReconciliationRunListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['billing', tenantId, 'reconciliation-runs', query],
    queryFn: () => ops.report.listRuns(tenantId, { limit: 100, ...query }),
  });
}

export function useReconciliationRun(runId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['billing', tenantId, 'reconciliation-run', runId],
    queryFn: () => ops.report.getRun(tenantId, runId),
  });
}

export function useDashboard() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['billing', tenantId, 'dashboard'],
    queryFn: () => ops.report.dashboard(tenantId),
    staleTime: 30_000,
  });
}

const EXPORT_POLL_MS = 4_000;

/** The list, re-read while the worker is still rendering something on it. */
export function useExports(query: ExportListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['billing', tenantId, 'exports', query],
    // A pending export is looked at one by one: the list is a snapshot, and the row the
    // worker is rendering answers its own state.
    queryFn: async () => {
      const page = await ops.report.listExports(tenantId, { limit: 100, ...query });
      const items = await Promise.all(
        page.items.map(async (x) =>
          x.status === 'QUEUED' || x.status === 'RUNNING'
            ? (await ops.report.getExport(tenantId, x.id)).data
            : x,
        ),
      );
      return { ...page, items };
    },
    refetchInterval: (q) =>
      q.state.data?.items.some((x) => x.status === 'QUEUED' || x.status === 'RUNNING')
        ? EXPORT_POLL_MS
        : false,
  });
}

export function useCreateExport() {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateExport) => ops.report.createExport(tenantId, body),
    onSuccess: () => client.invalidateQueries({ queryKey: ['billing', tenantId, 'exports'] }),
  });
}

/**
 * One download of one export. The export is named per call, so a single dialog on the page
 * can serve every row — and a row re-rendered by a width change loses nothing.
 */
export function useDownloadExport() {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: { exportId: string; purposeCode: string; reasonText?: string }) =>
      ops.report.downloadExport(tenantId, input.exportId, {
        purposeCode: input.purposeCode,
        ...(input.reasonText ? { reasonText: input.reasonText } : {}),
      }),
    onSuccess: () => client.invalidateQueries({ queryKey: ['billing', tenantId, 'exports'] }),
  });
}

/** One invoice, read when a reviewer opens a decision on it: its claims and allocations. */
export function useInvoice(invoiceId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['billing', tenantId, 'invoice', invoiceId],
    queryFn: () => ops.billing.getInvoice(tenantId, invoiceId),
  });
}
