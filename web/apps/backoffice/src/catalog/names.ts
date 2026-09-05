import { useTenantId } from '@kapsora/auth';
import { useQuery } from '@tanstack/react-query';

import { useOps } from '../api';

/** The name of a service definition, cached per id; an id on a screen is a demo. */
export function useServiceName(definitionId: string | null | undefined): string | null | undefined {
  const ops = useOps();
  const tenantId = useTenantId();
  const q = useQuery({
    queryKey: ['service-name', tenantId, definitionId ?? ''],
    queryFn: () => ops.catalog.getDefinition(tenantId, definitionId!),
    enabled: Boolean(definitionId),
    staleTime: 5 * 60_000,
    retry: false,
  });
  if (q.isPending && q.fetchStatus !== 'idle') return undefined;
  return q.data?.data.name ?? null;
}

/** Services a request line can name, for the in-place editor's select. */
export function useServiceDefinitionOptions() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['service-definitions', tenantId, 'options'],
    queryFn: () => ops.catalog.listDefinitions(tenantId, { limit: 200 }),
    staleTime: 5 * 60_000,
    select: (page) =>
      page.items.map((d) => ({
        value: d.id,
        label: `${d.code} · ${d.name}`,
        unitType: d.defaultUnitType,
      })),
  });
}
