import { useTenantId } from '@kapsora/auth';
import { useQuery } from '@tanstack/react-query';

import { useOps } from '../api';

/**
 * Names for rows that carry only ids. The claim, the case and the report carry no display
 * names on the wire yet, so a list resolves them one cached read at a time; "…" while it
 * loads, null when nobody has it.
 */
export function usePersonName(personId: string | null | undefined): string | null | undefined {
  const ops = useOps();
  const tenantId = useTenantId();
  const q = useQuery({
    queryKey: ['claims', tenantId, 'person-name', personId ?? ''],
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
    queryKey: ['claims', tenantId, 'organization-name', organizationId ?? ''],
    queryFn: () => ops.organizations.get(tenantId, organizationId!),
    enabled: Boolean(organizationId),
    staleTime: 5 * 60_000,
    retry: false,
  });
  if (q.isPending && q.fetchStatus !== 'idle') return undefined;
  return q.data?.data.displayName ?? null;
}
