import { useTenantId } from '@kapsora/auth';
import { useQuery } from '@tanstack/react-query';

import { useOps } from '../api';

/**
 * Display names for the ids a request carries. The contract puts only `personId` and
 * `providerOrganizationId` on a request, so a list has to resolve names itself; the
 * query cache dedupes the same id across rows and pages, and a name that has not landed
 * yet renders as an em dash rather than as the id, because an id on a screen is a demo.
 *
 * This is a known cost: fifty rows are fifty cached reads. The right fix is a display
 * name on the request itself, which is a contract change noted in ROADMAP.md.
 */
export function usePersonName(personId: string | null | undefined): string | null | undefined {
  const ops = useOps();
  const tenantId = useTenantId();
  const q = useQuery({
    queryKey: ['person-name', tenantId, personId ?? ''],
    queryFn: () => ops.people.get(tenantId, personId!),
    enabled: Boolean(personId),
    staleTime: 5 * 60_000,
    retry: false,
  });
  if (q.isPending && q.fetchStatus !== 'idle') return undefined;
  return q.data?.data.displayName ?? null;
}

export function useOrganizationName(
  organizationId: string | null | undefined,
): string | null | undefined {
  const ops = useOps();
  const tenantId = useTenantId();
  const q = useQuery({
    queryKey: ['organization-name', tenantId, organizationId ?? ''],
    queryFn: () => ops.organizations.get(tenantId, organizationId!),
    enabled: Boolean(organizationId),
    staleTime: 5 * 60_000,
    retry: false,
  });
  if (q.isPending && q.fetchStatus !== 'idle') return undefined;
  return q.data?.data.displayName ?? null;
}
