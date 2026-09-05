import type { DocumentClassification, DocumentListQuery } from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

export const documentKeys = {
  all: (tenantId: string) => ['documents', tenantId] as const,
  list: (tenantId: string, query: DocumentListQuery) =>
    ['documents', tenantId, 'list', query] as const,
  one: (tenantId: string, id: string) => ['documents', tenantId, 'one', id] as const,
};

/** Documents linked to one record. A scan in progress keeps the list refreshing. */
export function useLinkedDocuments(aggregateType: string, aggregateId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const query: DocumentListQuery = { aggregateType, aggregateId, limit: 100 };
  return useQuery({
    queryKey: documentKeys.list(tenantId, query),
    queryFn: () => ops.documents.list(tenantId, query),
    enabled: aggregateId !== '',
    // Polling is the honest way to show "taranıyor": the state is on the server and
    // nothing else tells the screen when the verdict lands.
    refetchInterval: (q) =>
      q.state.data?.items.some((d) => d.scanStatus === 'PENDING' || d.scanStatus === 'SCANNING')
        ? 2_000
        : false,
  });
}

/** The file's bytes. jsdom's File has no arrayBuffer(), so a FileReader is the fallback. */
async function bytesOf(file: File): Promise<ArrayBuffer> {
  if (typeof file.arrayBuffer === 'function') return file.arrayBuffer();
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as ArrayBuffer);
    reader.onerror = () => reject(reader.error ?? new Error('read failed'));
    reader.readAsArrayBuffer(file);
  });
}
/** SHA-256 of the file as lowercase hex: the digest the client claims, which the worker re-computes. */
async function sha256Hex(file: File): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', await bytesOf(file));
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('');
}

export interface UploadInput {
  file: File;
  documentTypeCode: string;
  classification: DocumentClassification;
  aggregateType: string;
  aggregateId: string;
}

/**
 * The whole upload, in the order the pipeline demands: reserve the row, PUT the bytes to
 * the quarantine URL the server signed, tell the server the bytes are there, link the
 * document to its record. The API never sees the file; the browser sends it to storage
 * directly. Nothing here reads the file into memory beyond what the PUT needs.
 */
export function useUploadDocument() {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (input: UploadInput) => {
      const reserved = await ops.documents.createUpload(tenantId, {
        originalFilename: input.file.name,
        contentType: input.file.type || 'application/octet-stream',
        byteSize: input.file.size,
        classification: input.classification,
      });
      const target = reserved.upload;
      if (target) {
        const put = await fetch(target.url, {
          method: target.method,
          headers: target.headers as Record<string, string>,
          body: input.file,
        });
        if (!put.ok) {
          throw new Error(`upload refused by storage: ${put.status}`);
        }
      }
      const completed = await ops.documents.completeUpload(tenantId, reserved.document.id, {
        byteSize: input.file.size,
        sha256: await sha256Hex(input.file),
      });
      await ops.documents.link(tenantId, completed.data.id, {
        aggregateType: input.aggregateType,
        aggregateId: input.aggregateId,
        documentTypeCode: input.documentTypeCode,
      });
      return completed.data;
    },
    onSuccess: () => client.invalidateQueries({ queryKey: documentKeys.all(tenantId) }),
  });
}

/** Asked for only when somebody wants the file: every call is audited on the server. */
export function useDownloadDocument() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({
    mutationFn: (documentId: string) => ops.documents.download(tenantId, documentId),
  });
}
