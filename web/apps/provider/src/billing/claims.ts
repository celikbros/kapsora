import { usePermission, useTenantId } from '@kapsora/auth';
import { useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../services';

export interface ClaimSummary {
  reference: string;
  status: string;
  approvedTotal: string | null;
}

/** Fetch the visible claim rows in order. Earlier rows stay cached as more are opened. */
export function useClaimSummaries(ids: string[]): {
  summaries: Map<string, ClaimSummary>;
  error: unknown;
} {
  const ops = useOps();
  const tenantId = useTenantId();
  const canRead = usePermission('claim.read');
  const queryClient = useQueryClient();
  const result = useQuery({
    queryKey: ['provider', tenantId, 'billing', 'claim-summaries', ids],
    enabled: canRead && ids.length > 0,
    queryFn: async ({ signal }): Promise<[string, ClaimSummary][]> => {
      const rows: [string, ClaimSummary][] = [];
      for (const id of ids) {
        if (signal.aborted) break;
        const summary = await queryClient.fetchQuery({
          queryKey: ['provider', tenantId, 'billing', 'claim-summary', id],
          queryFn: async ({ signal: innerSignal }): Promise<ClaimSummary> => {
            const claim = await ops.claims.get(tenantId, id);
            if (innerSignal.aborted) throw new DOMException('Session changed', 'AbortError');
            const readiness = await ops.claims.readiness(tenantId, id);
            return {
              reference: claim.data.reference,
              status: claim.data.status,
              approvedTotal: readiness.approvedTotal,
            };
          },
          staleTime: 60_000,
        });
        if (signal.aborted) break;
        rows.push([id, summary]);
      }
      return rows;
    },
    placeholderData: (previous) => previous,
    staleTime: 60_000,
  });
  return {
    summaries: new Map(canRead ? (result.data ?? []) : []),
    error: canRead ? result.error : null,
  };
}
