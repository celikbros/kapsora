import type { AdjustmentListQuery } from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useQuery } from '@tanstack/react-query';

import { useOps } from '../api';

/**
 * Keys for the approval queue. They sit under the `benefit` prefix so the adjustment
 * commands in `../benefit/queries` invalidate this list too, and they carry nothing but
 * ids and the server's opaque cursor.
 */
export const adjustmentKeys = {
  queue: (tenantId: string, query: AdjustmentListQuery) =>
    ['benefit', tenantId, 'adjustments', 'queue', query] as const,
  account: (tenantId: string, accountId: string) =>
    ['benefit', tenantId, 'entitlement-account', accountId] as const,
};

/**
 * One keyset page of adjustments. `useAdjustments` in `../benefit/queries` fetches a
 * status without paging; the queue needs the cursor, so it has its own hook.
 */
export function useAdjustmentQueue(query: AdjustmentListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: adjustmentKeys.queue(tenantId, query),
    queryFn: () => ops.entitlements.listAdjustments(tenantId, query),
    placeholderData: (previous) => previous,
  });
}

/**
 * The account an adjustment lands on. The list carries only the account id, and an
 * approver has to see which entitlement and which balance the delta hits; rows sharing
 * an account share one request through the cache.
 */
export function useEntitlementAccount(accountId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: adjustmentKeys.account(tenantId, accountId),
    queryFn: () => ops.entitlements.getAccount(tenantId, accountId),
    enabled: accountId !== '',
  });
}
