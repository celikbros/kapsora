import type {
  ImportBatchStatus,
  ImportListQuery,
  ImportRowQuery,
  ReasonCommand,
  ReviewRowInput,
  UploadImportInput,
} from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

export const importKeys = {
  all: (tenantId: string) => ['imports', tenantId] as const,
  list: (tenantId: string, query: ImportListQuery) => ['imports', tenantId, 'list', query] as const,
  detail: (tenantId: string, importId: string) =>
    ['imports', tenantId, 'detail', importId] as const,
  rows: (tenantId: string, importId: string, query: ImportRowQuery) =>
    ['imports', tenantId, 'rows', importId, query] as const,
};

/** Statuses a worker job is still moving; the batch page polls until one settles. */
const TRANSIENT: readonly ImportBatchStatus[] = ['RECEIVED', 'VALIDATING', 'APPLYING'];
const POLL_MS = 3_000;

/** Whether a screen may talk to the API at all; the server re-checks every call. */
export interface QueryGate {
  enabled?: boolean;
}

export function useImportList(query: ImportListQuery, gate: QueryGate = {}) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: importKeys.list(tenantId, query),
    queryFn: () => ops.imports.list(tenantId, query),
    enabled: gate.enabled ?? true,
    placeholderData: (previous) => previous,
  });
}

/**
 * One batch with its ETag. Validation and apply run as worker jobs, so the query polls
 * itself while the batch sits in a transient status and stops as soon as it settles.
 */
export function useImportBatch(importId: string, gate: QueryGate = {}) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: importKeys.detail(tenantId, importId),
    queryFn: () => ops.imports.get(tenantId, importId),
    enabled: (gate.enabled ?? true) && importId !== '',
    refetchInterval: (query) => {
      const status = query.state.data?.data.status;
      return status !== undefined && TRANSIENT.includes(status) ? POLL_MS : false;
    },
  });
}

export function useImportRows(importId: string, query: ImportRowQuery, gate: QueryGate = {}) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: importKeys.rows(tenantId, importId, query),
    queryFn: () => ops.imports.listRows(tenantId, importId, query),
    enabled: (gate.enabled ?? true) && importId !== '',
    placeholderData: (previous) => previous,
  });
}

/**
 * The multipart upload. One idempotency key per form instance, so a retried submit
 * replays the stored answer instead of staging the file twice.
 */
export function useUploadImport() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: UploadImportInput; idempotencyKey: string }) =>
      ops.imports.create(tenantId, input.body, input.idempotencyKey),
    onSuccess: (result) => {
      qc.setQueryData(importKeys.detail(tenantId, result.data.id), {
        data: result.data,
        etag: result.etag,
      });
      void qc.invalidateQueries({ queryKey: importKeys.all(tenantId) });
    },
  });
}

/** Records the operator's decision for one staged row; carries the row's own ETag. */
export function useReviewImportRow(importId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { rowId: string; etag: string; body: ReviewRowInput }) =>
      ops.imports.reviewRow(tenantId, importId, input.rowId, input.etag, input.body),
    onSuccess: () => {
      // A decision moves the row and the batch counters, so both are stale.
      void qc.invalidateQueries({ queryKey: importKeys.all(tenantId) });
    },
  });
}

/** Apply and cancel: the two commands that end a batch, each with the ETag it saw. */
export function useImportCommands(importId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  const settle = () => void qc.invalidateQueries({ queryKey: importKeys.all(tenantId) });

  const apply = useMutation({
    mutationFn: (input: { etag: string }) => ops.imports.apply(tenantId, importId, input.etag),
    onSuccess: settle,
  });
  const cancel = useMutation({
    mutationFn: (input: { etag: string; body: ReasonCommand }) =>
      ops.imports.cancel(tenantId, importId, input.etag, input.body),
    onSuccess: settle,
  });
  return { apply, cancel };
}
