import type {
  BatchListQuery,
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
