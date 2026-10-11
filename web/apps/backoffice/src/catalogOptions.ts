import { usePermission, useSession, useTenantId } from '@kapsora/auth';
import { useQuery } from '@tanstack/react-query';

import { useOps } from './api';

/**
 * Catalog rows as picker options, shared by the screens that reference a service without
 * managing the catalog itself: contracts, quotes and provider capabilities all need the
 * same three lists, and each fetching its own would be three copies of the same query key.
 */
export const catalogOptionKeys = {
  definitions: (tenantId: string) => ['catalogOptions', tenantId, 'definitions'] as const,
  categories: (tenantId: string) => ['catalogOptions', tenantId, 'categories'] as const,
};

export function useDefinitionOptions() {
  const ops = useOps();
  const tenantId = useTenantId();
  const actorId = useSession((state) => state.session?.actorId ?? '');
  const canRead = usePermission('catalog.read');
  const query = useQuery({
    queryKey: [...catalogOptionKeys.definitions(tenantId), actorId],
    queryFn: async () => {
      const page = await ops.catalog.listDefinitions(tenantId, { active: true, limit: 200 });
      return page.items.map((definition) => ({
        value: definition.id,
        label: `${definition.code} · ${definition.name}`,
      }));
    },
    enabled: canRead,
  });
  if (!canRead) {
    return {
      ...query,
      data: [],
      isPending: false,
      isLoading: false,
      isFetching: false,
      isError: false,
    };
  }
  return query;
}

export function useCategoryOptions() {
  const ops = useOps();
  const tenantId = useTenantId();
  const actorId = useSession((state) => state.session?.actorId ?? '');
  const canRead = usePermission('catalog.read');
  const query = useQuery({
    queryKey: [...catalogOptionKeys.categories(tenantId), actorId],
    queryFn: async () => {
      const page = await ops.catalog.listCategories(tenantId, { limit: 200 });
      return page.items.map((category) => ({
        value: category.id,
        label: `${category.code} · ${category.name}`,
      }));
    },
    enabled: canRead,
  });
  if (!canRead) {
    return {
      ...query,
      data: [],
      isPending: false,
      isLoading: false,
      isFetching: false,
      isError: false,
    };
  }
  return query;
}
