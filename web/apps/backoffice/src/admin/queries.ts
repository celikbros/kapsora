import { useSession, useTenantId } from '@kapsora/auth';
import { useQuery } from '@tanstack/react-query';
import type { TenantInvitationQuery, TenantUserQuery } from '@kapsora/api-client';
import { directoryContextKey, useOps } from '../api';

/** User records are keyed by the exact authenticated context, never just tenant. */
function useDirectoryContext() {
  return useSession(directoryContextKey);
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
    queryFn: () => ops.admin.getUserVersioned(tenantId, membershipId),
    enabled: membershipId !== '',
    retry: false,
  });
}

export function useTenantInvitations(query: TenantInvitationQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  const context = useDirectoryContext();
  return useQuery({
    queryKey: ['admin-invitations', context, 'list', query],
    queryFn: () => ops.admin.listInvitations(tenantId, query),
    retry: false,
  });
}

export function useTenantInvitation(invitationId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const context = useDirectoryContext();
  return useQuery({
    queryKey: ['admin-invitations', context, 'detail', invitationId],
    queryFn: () => ops.admin.getInvitation(tenantId, invitationId),
    enabled: invitationId !== '',
    retry: false,
  });
}
