import type {
  CreateOrganizationRequest,
  OrganizationListQuery,
  UpdateOrganizationRequest,
} from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useOps } from '../api';

export const organizationKeys = {
  all: (tenantId: string) => ['organizations', tenantId] as const,
  list: (tenantId: string, query: OrganizationListQuery) =>
    ['organizations', tenantId, 'list', query] as const,
  detail: (tenantId: string, id: string) => ['organizations', tenantId, 'detail', id] as const,
};

export function useOrganizationList(query: OrganizationListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: organizationKeys.list(tenantId, query),
    queryFn: () => ops.organizations.list(tenantId, query),
    placeholderData: (prev) => prev,
  });
}

export function useOrganization(id: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: organizationKeys.detail(tenantId, id),
    queryFn: () => ops.organizations.get(tenantId, id),
  });
}

export function useCreateOrganization() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateOrganizationRequest; idempotencyKey: string }) =>
      ops.organizations.create(tenantId, input.body, input.idempotencyKey),
    onSuccess: (result) => {
      qc.setQueryData(organizationKeys.detail(tenantId, result.data.id), result);
      void qc.invalidateQueries({ queryKey: organizationKeys.all(tenantId) });
    },
  });
}

export function useUpdateOrganization(id: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { etag: string; patch: UpdateOrganizationRequest }) =>
      ops.organizations.update(tenantId, id, input.etag, input.patch),
    onSuccess: (result) => {
      qc.setQueryData(organizationKeys.detail(tenantId, id), result);
      void qc.invalidateQueries({ queryKey: organizationKeys.all(tenantId) });
    },
  });
}
