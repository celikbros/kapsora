import type { KapsoraClient } from './client';
import { randomId } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type MemberImportBatch = components['schemas']['MemberImportBatch'];
export type MemberImportRow = components['schemas']['MemberImportRow'];
export type ReasonCommand = components['schemas']['ReasonCommand'];
export type ImportBatchStatus = MemberImportBatch['status'];
export type ImportRowStatus = MemberImportRow['status'];
export type ImportRowDecision = NonNullable<MemberImportRow['decision']>;

/** The multipart command behind the upload. */
export interface UploadImportInput {
  file: File | Blob;
  fileName: string;
  sponsorOrganizationId: string;
  sourceSystem: string;
  sourceVersion: string;
  planId?: string;
}

/** Result of the upload: 201 when validation finished inline, 202 when it was queued. */
export interface UploadImportResult extends Versioned<MemberImportBatch> {
  queued: boolean;
}

export interface ImportListQuery {
  cursor?: string;
  limit?: number;
  status?: ImportBatchStatus;
}

export interface ImportRowQuery {
  cursor?: string;
  limit?: number;
  status?: ImportRowStatus;
}

export interface ImportBatchPage {
  items: MemberImportBatch[];
  nextCursor?: string | null;
}

export interface ImportRowPage {
  items: MemberImportRow[];
  nextCursor?: string | null;
}

/** The decision an operator records for a row of the review queue. */
export interface ReviewRowInput {
  decision: ImportRowDecision;
  matchedPersonId?: string;
}

/**
 * Member import: upload a sponsor file, watch it validate, resolve the rows a human has
 * to decide on, then apply. The file is sent as multipart and is never stored by the API.
 */
export function memberImportOperations(client: KapsoraClient) {
  const header = (tenantId: string) => ({ 'X-Tenant-ID': tenantId });
  const withEtag = (tenantId: string, etag: string) => ({
    'X-Tenant-ID': tenantId,
    'If-Match': etag,
  });

  return {
    async create(
      tenantId: string,
      input: UploadImportInput,
      idempotencyKey: string = randomId(),
    ): Promise<UploadImportResult> {
      const form = new FormData();
      form.append('file', input.file, input.fileName);
      form.append('sponsorOrganizationId', input.sponsorOrganizationId);
      form.append('sourceSystem', input.sourceSystem);
      form.append('sourceVersion', input.sourceVersion);
      if (input.planId) form.append('planId', input.planId);
      const r = await unwrap(
        client.POST('/api/v1/imports/members', {
          params: { header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey } },
          // Let the browser set the multipart boundary itself.
          body: form as unknown as never,
          bodySerializer: (b: unknown) => b as FormData,
        }),
      );
      return { ...versioned(r.data, r.response), queued: r.response.status === 202 };
    },

    async list(tenantId: string, query: ImportListQuery = {}): Promise<ImportBatchPage> {
      const q: ImportListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.status) q.status = query.status;
      return (
        await unwrap(
          client.GET('/api/v1/imports/members', { params: { header: header(tenantId), query: q } }),
        )
      ).data;
    },

    async get(tenantId: string, importId: string): Promise<Versioned<MemberImportBatch>> {
      const r = await unwrap(
        client.GET('/api/v1/imports/members/{importId}', {
          params: { header: header(tenantId), path: { importId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async listRows(
      tenantId: string,
      importId: string,
      query: ImportRowQuery = {},
    ): Promise<ImportRowPage> {
      const q: ImportRowQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.status) q.status = query.status;
      return (
        await unwrap(
          client.GET('/api/v1/imports/members/{importId}/rows', {
            params: { header: header(tenantId), path: { importId }, query: q },
          }),
        )
      ).data;
    },

    async reviewRow(
      tenantId: string,
      importId: string,
      rowId: string,
      etag: string,
      input: ReviewRowInput,
    ): Promise<Versioned<MemberImportRow>> {
      const body: ReviewRowInput = { decision: input.decision };
      if (input.matchedPersonId) body.matchedPersonId = input.matchedPersonId;
      const r = await unwrap(
        client.POST('/api/v1/imports/members/{importId}/rows/{rowId}/review', {
          params: { header: withEtag(tenantId, etag), path: { importId, rowId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Queues the apply job; the batch answers 202 in APPLYING. Needs a step-up. */
    async apply(
      tenantId: string,
      importId: string,
      etag: string,
    ): Promise<Versioned<MemberImportBatch>> {
      const r = await unwrap(
        client.POST('/api/v1/imports/members/{importId}/apply', {
          params: { header: withEtag(tenantId, etag), path: { importId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async cancel(
      tenantId: string,
      importId: string,
      etag: string,
      body: ReasonCommand,
    ): Promise<Versioned<MemberImportBatch>> {
      const r = await unwrap(
        client.POST('/api/v1/imports/members/{importId}/cancel', {
          params: { header: withEtag(tenantId, etag), path: { importId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },
  };
}
