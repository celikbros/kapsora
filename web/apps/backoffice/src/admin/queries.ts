import { useSession, useTenantId } from '@kapsora/auth';
import { useQuery } from '@tanstack/react-query';
import type { TenantUserQuery } from '@kapsora/api-client';
import { useOps } from '../api';

/** User records are keyed by the exact authenticated context, never just tenant. */
function useDirectoryContext() {
  return useSession((s) =>
    [
      s.session?.actorId ?? '',
      s.session?.expiresAt ?? '',
      s.activeTenant?.tenant.id ?? '',
      s.activeTenant?.canReadTenantUsers === true ? 'allowed' : 'denied',
      s.activeTenant?.permissions.join('|') ?? '',
      JSON.stringify(s.activeTenant?.scopes ?? []),
    ].join(':'),
  );
}

export function useTenantUsers(query: TenantUserQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  const context = useDirectoryContext();
  return useQuery({
    queryKey: ['admin-users', context, 'list', query],
    queryFn: () => ops.admin.listUsers(tenantId, query),
    retry: false,
  });
}

export function useTenantUser(membershipId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const context = useDirectoryContext();
  return useQuery({
    queryKey: ['admin-users', context, 'detail', membershipId],
    queryFn: () => ops.admin.getUser(tenantId, membershipId),
    enabled: membershipId !== '',
    retry: false,
  });
}
