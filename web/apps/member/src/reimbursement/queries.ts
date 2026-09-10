import type { CreateReimbursement, CreateServiceRequest } from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../services';

/**
 * The member's reimbursement: what they paid, the receipt that proves it, the account the
 * money returns to. The server resolves the person from the account's PERSON scope; the
 * account number is sent once and comes back as four characters.
 */

const POLL_MS = 3_000;

export function useMyPerson() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['member', tenantId, 'me-person'],
    queryFn: () => ops.lodging.myPerson(tenantId),
    staleTime: 5 * 60_000,
  });
}

export function useMyReimbursements() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['member', tenantId, 'reimbursements'],
    queryFn: () => ops.billing.myReimbursements(tenantId),
  });
}

export function useReimbursement(reimbursementId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['member', tenantId, 'reimbursement', reimbursementId],
    queryFn: () => ops.billing.getReimbursement(tenantId, reimbursementId),
  });
}

/** The services a member may claim money back for: the active catalogue, by name. */
export function useServiceDefinitions() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['member', tenantId, 'definitions'],
    queryFn: () => ops.catalog.listDefinitions(tenantId, { active: true, limit: 200 }),
    staleTime: 5 * 60_000,
  });
}

export function useProviders() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['member', tenantId, 'providers'],
    queryFn: () => ops.providers.list(tenantId, { status: 'ACTIVE', limit: 200 }),
    staleTime: 5 * 60_000,
  });
}

export function useCreateRequest() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({
    mutationFn: (body: CreateServiceRequest) => ops.requests.create(tenantId, body),
  });
}

async function bytesOf(file: File): Promise<ArrayBuffer> {
  if (typeof file.arrayBuffer === 'function') return file.arrayBuffer();
  return new Response(file).arrayBuffer();
}

async function sha256Hex(file: File): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', await bytesOf(file));
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('');
}

/**
 * The receipt: reserve, put, complete, link to the request. The file goes to storage and
 * only its digest and size come back here; the scan that follows is the server's.
 */
export function useUploadReceipt() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({
    mutationFn: async (input: { file: File; requestId: string }) => {
      const reserved = await ops.documents.createUpload(tenantId, {
        originalFilename: input.file.name,
        contentType: input.file.type || 'application/octet-stream',
        byteSize: input.file.size,
        classification: 'PERSONAL',
      });
      const target = reserved.upload;
      if (target) {
        const put = await fetch(target.url, {
          method: target.method,
          headers: target.headers as Record<string, string>,
          body: input.file,
        });
        if (!put.ok) throw new Error(`upload refused by storage: ${put.status}`);
      }
      const completed = await ops.documents.completeUpload(tenantId, reserved.document.id, {
        byteSize: input.file.size,
        sha256: await sha256Hex(input.file),
      });
      await ops.documents.link(tenantId, completed.data.id, {
        aggregateType: 'SERVICE_REQUEST',
        aggregateId: input.requestId,
        documentTypeCode: 'RECEIPT',
      });
      return completed.data;
    },
  });
}

/** One document, re-read while the scan is still running. */
export function useDocument(documentId: string | null) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['member', tenantId, 'document', documentId],
    queryFn: () => ops.documents.get(tenantId, documentId!),
    enabled: documentId !== null,
    refetchInterval: (query) => {
      const status = query.state.data?.data.scanStatus;
      return status === 'PENDING' || status === 'SCANNING' ? POLL_MS : false;
    },
  });
}

function useInvalidateReimbursements() {
  const client = useQueryClient();
  const tenantId = useTenantId();
  return (reimbursementId?: string) =>
    Promise.all([
      client.invalidateQueries({ queryKey: ['member', tenantId, 'reimbursements'] }),
      reimbursementId
        ? client.invalidateQueries({
            queryKey: ['member', tenantId, 'reimbursement', reimbursementId],
          })
        : Promise.resolve(),
    ]);
}

export function useCreateReimbursement() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateReimbursements();
  return useMutation({
    mutationFn: (body: CreateReimbursement) => ops.billing.createReimbursement(tenantId, body),
    onSuccess: () => invalidate(),
  });
}

export function useSubmitReimbursement(reimbursementId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateReimbursements();
  return useMutation({
    mutationFn: (etag: string) => ops.billing.submitReimbursement(tenantId, reimbursementId, etag),
    onSuccess: () => invalidate(reimbursementId),
  });
}
