import type { KapsoraClient } from './client';
import { randomId } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import {
  asDecimals,
  type CreateAdjustmentRequest as AdjustmentBody,
  type EntitlementAccount as Account,
  type EntitlementAdjustment as Adjustment,
  type LedgerPage as Ledger,
} from './decimals';
import { versioned, type Versioned } from './versioned';

// Balances, deltas and adjustment amounts are decimal strings; see decimals.ts.
export type {
  CreateAdjustmentRequest,
  EntitlementAccount,
  EntitlementAdjustment,
  EntitlementReservation,
  LedgerEntry,
  LedgerPage,
} from './decimals';
export type ReviewComment = components['schemas']['ReviewComment'];
export type ReasonCommand = components['schemas']['ReasonCommand'];
export type AdjustmentStatus = Adjustment['status'];

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
  items: Adjustment[];
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
    ): Promise<Account[]> {
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
      ).data.items.map((a) => asDecimals<Account>(a));
    },

    async getAccount(tenantId: string, accountId: string): Promise<Versioned<Account>> {
      const r = await unwrap(
        client.GET('/api/v1/entitlement-accounts/{accountId}', {
          params: { header: header(tenantId), path: { accountId } },
        }),
      );
      return versioned(asDecimals<Account>(r.data), r.response);
    },

    async listLedger(
      tenantId: string,
      accountId: string,
      query: LedgerQuery = {},
    ): Promise<Ledger> {
      const q: LedgerQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      return (
        await unwrap(
          client.GET('/api/v1/entitlement-accounts/{accountId}/ledger', {
            params: { header: header(tenantId), path: { accountId }, query: q },
          }),
        )
      ).data as unknown as Ledger;
    },

    async createAdjustment(
      tenantId: string,
      accountId: string,
      body: AdjustmentBody,
      idempotencyKey: string = randomId(),
    ): Promise<Adjustment> {
      return (
        await unwrap(
          client.POST('/api/v1/entitlement-accounts/{accountId}/adjustments', {
            params: {
              header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey },
              path: { accountId },
            },
            body: asDecimals<never>(body),
          }),
        )
      ).data as unknown as Adjustment;
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
      ).data as unknown as AdjustmentPage;
    },

    /** Needs a recent step-up and a different actor than the requester. */
    async approveAdjustment(
      tenantId: string,
      adjustmentId: string,
      etag: string,
      body: ReviewComment = {},
    ): Promise<Adjustment> {
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
      ).data as unknown as Adjustment;
    },

    async rejectAdjustment(
      tenantId: string,
      adjustmentId: string,
      etag: string,
      body: ReasonCommand,
    ): Promise<Adjustment> {
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
      ).data as unknown as Adjustment;
    },
  };
}
