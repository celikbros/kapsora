import type { KapsoraClient } from './client';
import { randomId } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type EntitlementAccount = components['schemas']['EntitlementAccount'];
export type EntitlementReservation = components['schemas']['EntitlementReservation'];
export type LedgerEntry = components['schemas']['LedgerEntry'];
export type LedgerPage = components['schemas']['LedgerPage'];
export type EntitlementAdjustment = components['schemas']['EntitlementAdjustment'];
export type CreateAdjustmentRequest = components['schemas']['CreateAdjustmentRequest'];
export type ReviewComment = components['schemas']['ReviewComment'];
export type ReasonCommand = components['schemas']['ReasonCommand'];
export type AdjustmentStatus = EntitlementAdjustment['status'];

/** Filters of the ledger page. */
export interface LedgerQuery {
  cursor?: string;
  limit?: number;
}

/** Filters of the adjustment queue. */
export interface AdjustmentListQuery {
  cursor?: string;
  limit?: number;
  status?: AdjustmentStatus;
}

/** One page of adjustments. */
export interface AdjustmentPage {
  items: EntitlementAdjustment[];
  nextCursor?: string | null;
}

/**
 * Entitlement balances, the append-only ledger behind them and the manual adjustments
 * that need a second actor's approval. Quantities are decimal strings: never turn them
 * into JavaScript numbers, format them as text.
 */
export function entitlementOperations(client: KapsoraClient) {
  const header = (tenantId: string) => ({ 'X-Tenant-ID': tenantId });

  return {
    /** Accounts the person can reach on a date: their own and family-shared ones. */
    async listPersonEntitlements(
      tenantId: string,
      personId: string,
      asOf?: string,
    ): Promise<EntitlementAccount[]> {
      return (
        await unwrap(
          client.GET('/api/v1/people/{personId}/entitlements', {
            params: {
              header: header(tenantId),
              path: { personId },
              query: asOf ? { asOf } : {},
            },
          }),
        )
      ).data.items;
    },

    async getAccount(tenantId: string, accountId: string): Promise<Versioned<EntitlementAccount>> {
      const r = await unwrap(
        client.GET('/api/v1/entitlement-accounts/{accountId}', {
          params: { header: header(tenantId), path: { accountId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async listLedger(
      tenantId: string,
      accountId: string,
      query: LedgerQuery = {},
    ): Promise<LedgerPage> {
      const q: LedgerQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      return (
        await unwrap(
          client.GET('/api/v1/entitlement-accounts/{accountId}/ledger', {
            params: { header: header(tenantId), path: { accountId }, query: q },
          }),
        )
      ).data;
    },

    async createAdjustment(
      tenantId: string,
      accountId: string,
      body: CreateAdjustmentRequest,
      idempotencyKey: string = randomId(),
    ): Promise<EntitlementAdjustment> {
      return (
        await unwrap(
          client.POST('/api/v1/entitlement-accounts/{accountId}/adjustments', {
            params: {
              header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey },
              path: { accountId },
            },
            body,
          }),
        )
      ).data;
    },

    async listAdjustments(
      tenantId: string,
      query: AdjustmentListQuery = {},
    ): Promise<AdjustmentPage> {
      const q: AdjustmentListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.status) q.status = query.status;
      return (
        await unwrap(
          client.GET('/api/v1/entitlement-adjustments', {
            params: { header: header(tenantId), query: q },
          }),
        )
      ).data;
    },

    /** Needs a recent step-up and a different actor than the requester. */
    async approveAdjustment(
      tenantId: string,
      adjustmentId: string,
      etag: string,
      body: ReviewComment = {},
    ): Promise<EntitlementAdjustment> {
      return (
        await unwrap(
          client.POST('/api/v1/entitlement-adjustments/{adjustmentId}/approve', {
            params: {
              header: { 'X-Tenant-ID': tenantId, 'If-Match': etag },
              path: { adjustmentId },
            },
            body,
          }),
        )
      ).data;
    },

    async rejectAdjustment(
      tenantId: string,
      adjustmentId: string,
      etag: string,
      body: ReasonCommand,
    ): Promise<EntitlementAdjustment> {
      return (
        await unwrap(
          client.POST('/api/v1/entitlement-adjustments/{adjustmentId}/reject', {
            params: {
              header: { 'X-Tenant-ID': tenantId, 'If-Match': etag },
              path: { adjustmentId },
            },
            body,
          }),
        )
      ).data;
    },
  };
}
