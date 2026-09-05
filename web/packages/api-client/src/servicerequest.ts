import type { KapsoraClient } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { randomId } from './client';
import { versioned, type Versioned } from './versioned';

export type ServiceRequest = components['schemas']['ServiceRequest'];
export type ServiceRequestItem = components['schemas']['ServiceRequestItem'];
export type ServiceRequestPage = components['schemas']['ServiceRequestPage'];
export type ServiceRequestVersion = components['schemas']['ServiceRequestVersion'];
export type ServiceRequestVersionSummary = components['schemas']['ServiceRequestVersionSummary'];
export type ServiceRequestStatus = components['schemas']['ServiceRequestStatus'];
export type ServiceRequestType = components['schemas']['ServiceRequestType'];
export type ServiceRequestChannel = components['schemas']['ServiceRequestChannel'];
export type CreateServiceRequest = components['schemas']['CreateServiceRequest'];
export type UpdateServiceRequest = components['schemas']['UpdateServiceRequest'];
export type ServiceRequestItems = components['schemas']['ServiceRequestItems'];
export type ServiceRequestDecision = components['schemas']['ServiceRequestDecision'];
export type ReasonCommand = components['schemas']['ReasonCommand'];

export interface ServiceRequestListQuery {
  cursor?: string;
  limit?: number;
  status?: ServiceRequestStatus;
  personId?: string;
  programId?: string;
  providerOrganizationId?: string;
  channel?: ServiceRequestChannel;
  serviceDateFrom?: string;
  serviceDateTo?: string;
}

/**
 * Service requests: what somebody asked for, and everything that happened to the asking.
 *
 * There is no operation here that writes a status, because the server has none. Every move
 * is its own command with its own precondition and its own reason, which is why they are
 * separate methods rather than one `patch({status})` — a screen cannot accidentally offer
 * a transition by putting a value in a dropdown.
 *
 * A submit is not "it is now submitted": the server evaluates eligibility and the rules
 * inside the command and answers with PENDING_REVIEW, PENDING_DOCUMENT or
 * ELIGIBILITY_FAILED. Callers must read the status they get back rather than assuming one.
 *
 * Quantities and amounts are exact decimal strings. Format them; never parse them.
 */
export function serviceRequestOperations(client: KapsoraClient) {
  const header = (tenantId: string) => ({ 'X-Tenant-ID': tenantId });
  const withEtag = (tenantId: string, etag: string) => ({
    'X-Tenant-ID': tenantId,
    'If-Match': etag,
  });
  const command = (tenantId: string, etag: string, idempotencyKey: string) => ({
    'X-Tenant-ID': tenantId,
    'If-Match': etag,
    'Idempotency-Key': idempotencyKey,
  });
  const mergePatch = {
    headers: { 'Content-Type': 'application/merge-patch+json' },
    bodySerializer: (b: unknown) => JSON.stringify(b),
  };

  return {
    async list(tenantId: string, query: ServiceRequestListQuery = {}): Promise<ServiceRequestPage> {
      const q: ServiceRequestListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.status) q.status = query.status;
      if (query.personId) q.personId = query.personId;
      if (query.programId) q.programId = query.programId;
      if (query.providerOrganizationId) q.providerOrganizationId = query.providerOrganizationId;
      if (query.channel) q.channel = query.channel;
      if (query.serviceDateFrom) q.serviceDateFrom = query.serviceDateFrom;
      if (query.serviceDateTo) q.serviceDateTo = query.serviceDateTo;
      return (
        await unwrap(
          client.GET('/api/v1/service-requests', {
            params: { header: header(tenantId), query: q },
          }),
        )
      ).data;
    },

    async get(tenantId: string, requestId: string): Promise<Versioned<ServiceRequest>> {
      const r = await unwrap(
        client.GET('/api/v1/service-requests/{requestId}', {
          params: { header: header(tenantId), path: { requestId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async create(
      tenantId: string,
      body: CreateServiceRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<ServiceRequest>> {
      const r = await unwrap(
        client.POST('/api/v1/service-requests', {
          params: { header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Only a draft is editable, and only its own fields — never its status. */
    async patch(
      tenantId: string,
      requestId: string,
      etag: string,
      body: UpdateServiceRequest,
    ): Promise<Versioned<ServiceRequest>> {
      const r = await unwrap(
        client.PATCH('/api/v1/service-requests/{requestId}', {
          params: { header: withEtag(tenantId, etag), path: { requestId } },
          body,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Replaces the whole line set of the editable version; the set is the unit, not the row. */
    async putItems(
      tenantId: string,
      requestId: string,
      etag: string,
      body: ServiceRequestItems,
    ): Promise<Versioned<ServiceRequest>> {
      const r = await unwrap(
        client.PUT('/api/v1/service-requests/{requestId}/items', {
          params: { header: withEtag(tenantId, etag), path: { requestId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Submit. The status that comes back is the decision, not "SUBMITTED": read it.
     */
    async submit(
      tenantId: string,
      requestId: string,
      etag: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<ServiceRequest>> {
      const r = await unwrap(
        client.POST('/api/v1/service-requests/{requestId}/submit', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { requestId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Return for correction. This is an invitation to fix something, and the reason is
     * what the person correcting it will read — it is not the same act as a rejection and
     * a screen must not present it as one.
     */
    async returnForCorrection(
      tenantId: string,
      requestId: string,
      etag: string,
      body: ReasonCommand,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<ServiceRequest>> {
      const r = await unwrap(
        client.POST('/api/v1/service-requests/{requestId}/return', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { requestId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Reject. The request is finished, and the reason is on the record permanently. */
    async reject(
      tenantId: string,
      requestId: string,
      etag: string,
      body: ReasonCommand,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<ServiceRequest>> {
      const r = await unwrap(
        client.POST('/api/v1/service-requests/{requestId}/reject', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { requestId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Approve everything as asked. Omitting items means every line at what it requested. */
    async approve(
      tenantId: string,
      requestId: string,
      etag: string,
      body: ServiceRequestDecision,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<ServiceRequest>> {
      const r = await unwrap(
        client.POST('/api/v1/service-requests/{requestId}/approve', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { requestId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** A partial approval has to name every line: silence is not a decision. */
    async partiallyApprove(
      tenantId: string,
      requestId: string,
      etag: string,
      body: ServiceRequestDecision,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<ServiceRequest>> {
      const r = await unwrap(
        client.POST('/api/v1/service-requests/{requestId}/partially-approve', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { requestId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async cancel(
      tenantId: string,
      requestId: string,
      etag: string,
      body: ReasonCommand,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<ServiceRequest>> {
      const r = await unwrap(
        client.POST('/api/v1/service-requests/{requestId}/cancel', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { requestId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** The history: what was asked in each version, and why each was sent back. */
    async listVersions(
      tenantId: string,
      requestId: string,
    ): Promise<ServiceRequestVersionSummary[]> {
      const r = await unwrap(
        client.GET('/api/v1/service-requests/{requestId}/versions', {
          params: { header: header(tenantId), path: { requestId } },
        }),
      );
      return r.data.items;
    },

    async getVersion(
      tenantId: string,
      requestId: string,
      versionNo: number,
    ): Promise<ServiceRequestVersion> {
      return (
        await unwrap(
          client.GET('/api/v1/service-requests/{requestId}/versions/{versionNo}', {
            params: { header: header(tenantId), path: { requestId, versionNo } },
          }),
        )
      ).data;
    },
  };
}
