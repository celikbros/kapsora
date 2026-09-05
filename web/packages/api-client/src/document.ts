import { randomId, type KapsoraClient } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type Document = components['schemas']['Document'];
export type DocumentPage = components['schemas']['DocumentPage'];
export type DocumentLink = components['schemas']['DocumentLink'];
export type DocumentUpload = components['schemas']['DocumentUpload'];
export type DocumentDownload = components['schemas']['DocumentDownload'];
export type DocumentClassification = components['schemas']['DocumentClassification'];
export type DocumentScanStatus = components['schemas']['DocumentScanStatus'];
export type CreateUpload = components['schemas']['CreateUpload'];
export type CompleteUpload = components['schemas']['CompleteUpload'];
export type LegalHold = components['schemas']['LegalHold'];

export interface DocumentListQuery {
  cursor?: string;
  limit?: number;
  scanStatus?: DocumentScanStatus;
  classification?: DocumentClassification;
  aggregateType?: string;
  aggregateId?: string;
}

/**
 * Documents: where a file is, what it is, who may see it and what the scanner said.
 *
 * The API never carries file bytes. `createUpload` answers a short-lived presigned URL the
 * browser PUTs the file to directly, and `download` answers another one pointing at the
 * secure bucket. Neither URL should be logged, put in a query key, or kept: they are
 * capabilities with a short life.
 *
 * `downloadable` on a Document is the one place the product decides whether a file can be
 * had: clean, in the secure bucket and not purged. A screen offers the download when that
 * is true and shows the scan state when it is not — never a disabled button, and never an
 * optimistic "uploaded" over a file that is still being scanned.
 */
export function documentOperations(client: KapsoraClient) {
  const header = (tenantId: string) => ({ 'X-Tenant-ID': tenantId });
  const command = (tenantId: string, idempotencyKey: string) => ({
    'X-Tenant-ID': tenantId,
    'Idempotency-Key': idempotencyKey,
  });

  return {
    async list(tenantId: string, query: DocumentListQuery = {}): Promise<DocumentPage> {
      const q: DocumentListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.scanStatus) q.scanStatus = query.scanStatus;
      if (query.classification) q.classification = query.classification;
      if (query.aggregateType) q.aggregateType = query.aggregateType;
      if (query.aggregateId) q.aggregateId = query.aggregateId;
      return (
        await unwrap(
          client.GET('/api/v1/documents', { params: { header: header(tenantId), query: q } }),
        )
      ).data;
    },

    async get(tenantId: string, documentId: string): Promise<Versioned<Document>> {
      const r = await unwrap(
        client.GET('/api/v1/documents/{documentId}', {
          params: { header: header(tenantId), path: { documentId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Reserves the row and answers the presigned PUT the browser uploads to. */
    async createUpload(
      tenantId: string,
      body: CreateUpload,
      idempotencyKey: string = randomId(),
    ): Promise<DocumentUpload> {
      return (
        await unwrap(
          client.POST('/api/v1/documents', {
            params: { header: command(tenantId, idempotencyKey) },
            body,
          }),
        )
      ).data;
    },

    /** Tells the server the bytes are there, which is what enqueues the scan. */
    async completeUpload(
      tenantId: string,
      documentId: string,
      body: CompleteUpload,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Document>> {
      const r = await unwrap(
        client.POST('/api/v1/documents/{documentId}/complete', {
          params: { header: command(tenantId, idempotencyKey), path: { documentId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * A short-lived URL into the secure bucket. Refused unless the scan came back clean,
     * and every call is audited with the document's classification, so it is asked for
     * when somebody actually wants the file — never prefetched to decide whether to show
     * a button.
     */
    async download(
      tenantId: string,
      documentId: string,
      body: { purposeCode?: string; reasonText?: string } = {},
    ): Promise<DocumentDownload> {
      return (
        await unwrap(
          client.POST('/api/v1/documents/{documentId}/download', {
            params: { header: header(tenantId), path: { documentId } },
            body,
          }),
        )
      ).data;
    },

    async link(
      tenantId: string,
      documentId: string,
      body: { aggregateType: string; aggregateId: string; documentTypeCode: string },
      idempotencyKey: string = randomId(),
    ): Promise<DocumentLink> {
      return (
        await unwrap(
          client.POST('/api/v1/documents/{documentId}/links', {
            params: { header: command(tenantId, idempotencyKey), path: { documentId } },
            body,
          }),
        )
      ).data;
    },

    async unlink(tenantId: string, documentId: string, linkId: string): Promise<void> {
      await unwrap(
        client.DELETE('/api/v1/documents/{documentId}/links/{linkId}', {
          params: { header: header(tenantId), path: { documentId, linkId } },
        }),
      );
    },

    /** A document under legal hold survives retention, whatever the retention job says. */
    async putLegalHold(
      tenantId: string,
      body: { documentId?: string; personId?: string; reason: string },
      idempotencyKey: string = randomId(),
    ): Promise<LegalHold> {
      return (
        await unwrap(
          client.POST('/api/v1/legal-holds', {
            params: { header: command(tenantId, idempotencyKey) },
            body,
          }),
        )
      ).data;
    },

    /** Releasing needs the hold's ETag: a hold lifted from a stale view is not a decision. */
    async releaseLegalHold(
      tenantId: string,
      legalHoldId: string,
      etag: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<LegalHold>> {
      const r = await unwrap(
        client.POST('/api/v1/legal-holds/{legalHoldId}/release', {
          params: {
            header: { ...command(tenantId, idempotencyKey), 'If-Match': etag },
            path: { legalHoldId },
          },
        }),
      );
      return versioned(r.data, r.response);
    },
  };
}
