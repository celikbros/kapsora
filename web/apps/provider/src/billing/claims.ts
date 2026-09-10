import { useTenantId } from '@kapsora/auth';
import { useQueries } from '@tanstack/react-query';

import { useOps } from '../services';

export interface ClaimSummary {
  reference: string;
  status: string;
  approvedTotal: string | null;
}

/**
 * The invoiceable claims the earnings view names by id, read one by one for their
 * reference, status and approved total (the readiness answer). Read only for the ids
 * the invoice does not already carry.
 */
export function useClaimSummaries(ids: string[]): Map<string, ClaimSummary> {
  const ops = useOps();
  const tenantId = useTenantId();
  const results = useQueries({
    queries: ids.map((id) => ({
      queryKey: ['provider', tenantId, 'billing', 'claim-summary', id],
      queryFn: async (): Promise<[string, ClaimSummary]> => {
        const [claim, readiness] = await Promise.all([
          ops.claims.get(tenantId, id),
          ops.claims.readiness(tenantId, id).catch(() => null),
        ]);
        return [
          id,
          {
            reference: claim.data.reference,
            status: claim.data.status,
            approvedTotal: readiness?.approvedTotal ?? null,
          },
        ];
      },
      staleTime: 60_000,
    })),
  });
  const map = new Map<string, ClaimSummary>();
  for (const r of results) {
    if (r.data) map.set(r.data[0], r.data[1]);
  }
  return map;
}
