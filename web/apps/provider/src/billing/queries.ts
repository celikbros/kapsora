import type {
  BatchListQuery,
  CreateBatch,
  CreateInvoice,
  EarningsQuery,
  InvoiceListQuery,
  PatchInvoiceDraft,
  PutBatchInvoices,
  PutInvoiceAllocations,
} from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../services';

/**
 * The provider's money: what it earned, the invoice it enters against that, the batch it
 * sends. Every read is scoped by the server to the provider's own organization; every
 * figure is the server's string and nothing here adds two of them.
 */

export function useEarnings(providerId: string | null, query: EarningsQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'billing', 'earnings', providerId, query],
    queryFn: () => ops.billing.earnings(tenantId, providerId!, query),
    enabled: providerId !== null,
  });
}

export function useInvoices(query: InvoiceListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'billing', 'invoices', query],
    queryFn: () => ops.billing.listInvoices(tenantId, { limit: 100, ...query }),
  });
}

export function useInvoice(invoiceId: string | null) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'billing', 'invoice', invoiceId],
    queryFn: () => ops.billing.getInvoice(tenantId, invoiceId!),
    enabled: invoiceId !== null,
  });
}

export function useInvoiceChain(invoiceId: string | null) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'billing', 'invoice-chain', invoiceId],
    queryFn: () => ops.billing.invoiceChain(tenantId, invoiceId!),
    enabled: invoiceId !== null,
  });
}

function useInvalidateBilling() {
  const client = useQueryClient();
  const tenantId = useTenantId();
  return () => client.invalidateQueries({ queryKey: ['provider', tenantId, 'billing'] });
}

export function useCreateInvoice() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBilling();
  return useMutation({
    mutationFn: (body: CreateInvoice) => ops.billing.createInvoice(tenantId, body),
    onSuccess: () => invalidate(),
  });
}

export function usePatchInvoice(invoiceId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBilling();
  return useMutation({
    mutationFn: ({ etag, body }: { etag: string; body: PatchInvoiceDraft }) =>
      ops.billing.patchInvoiceDraft(tenantId, invoiceId, etag, body),
    onSuccess: () => invalidate(),
  });
}

export function usePutAllocations(invoiceId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBilling();
  return useMutation({
    mutationFn: ({ etag, body }: { etag: string; body: PutInvoiceAllocations }) =>
      ops.billing.putInvoiceAllocations(tenantId, invoiceId, etag, body),
    onSuccess: () => invalidate(),
  });
}

export function useSubmitInvoice(invoiceId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBilling();
  return useMutation({
    mutationFn: (etag: string) => ops.billing.submitInvoice(tenantId, invoiceId, etag),
    onSuccess: () => invalidate(),
  });
}

export function useCancelInvoice(invoiceId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBilling();
  return useMutation({
    mutationFn: (etag: string) => ops.billing.cancelInvoice(tenantId, invoiceId, etag),
    onSuccess: () => invalidate(),
  });
}

export function useBatches(query: BatchListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'billing', 'batches', query],
    queryFn: () => ops.billing.listBatches(tenantId, { limit: 100, ...query }),
  });
}

export function useBatch(batchId: string | null) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'billing', 'batch', batchId],
    queryFn: () => ops.billing.getBatch(tenantId, batchId!),
    enabled: batchId !== null,
  });
}

export function useBatchSummary(batchId: string | null) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'billing', 'batch-summary', batchId],
    queryFn: () => ops.billing.getBatchSummary(tenantId, batchId!),
    enabled: batchId !== null,
  });
}

export function useCreateBatch() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBilling();
  return useMutation({
    mutationFn: (body: CreateBatch) => ops.billing.createBatch(tenantId, body),
    onSuccess: () => invalidate(),
  });
}

export function usePutBatchInvoices(batchId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBilling();
  return useMutation({
    mutationFn: ({ etag, body }: { etag: string; body: PutBatchInvoices }) =>
      ops.billing.putBatchInvoices(tenantId, batchId, etag, body),
    onSuccess: () => invalidate(),
  });
}

export function useSubmitBatch(batchId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateBilling();
  return useMutation({
    mutationFn: (etag: string) => ops.billing.submitBatch(tenantId, batchId, etag),
    onSuccess: () => invalidate(),
  });
}
