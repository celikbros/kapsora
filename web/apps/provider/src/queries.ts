import type {
  CreateServiceRequest,
  EligibilityCheckRequest,
  ServiceRequestListQuery,
} from '@kapsora/api-client';
import { useSession, useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from './services';

/**
 * The organization this actor works for. A provider-scoped actor carries an ORGANIZATION
 * grant on its tenant context; the server applies it to every read and refuses a write
 * naming any other organization. The screen sends it so a request is raised for the
 * right provider — it never filters with it, because the server already has.
 */
export function useProviderOrganizationId(): string | null {
  const active = useSession((s) => s.activeTenant);
  const grant = (active?.scopes ?? []).find((g) => g.type === 'ORGANIZATION' && g.id);
  return grant?.id ?? null;
}

export function usePeopleByName(q: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const trimmed = q.trim();
  return useQuery({
    queryKey: ['provider', tenantId, 'people', trimmed],
    queryFn: () => ops.people.list(tenantId, { q: trimmed, status: 'ACTIVE', limit: 20 }),
    enabled: trimmed.length >= 2,
  });
}

/** Identifier search: the value goes to the server once and never into a query key. */
export function useIdentifierSearch() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({
    mutationFn: (input: { type: string; value: string }) =>
      ops.people.searchByIdentifier(tenantId, input),
  });
}

export function usePersonEnrollments(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'enrollments', personId],
    queryFn: () => ops.benefit.listPersonEnrollments(tenantId, personId),
    enabled: personId !== '',
  });
}

export function useServiceDefinitions() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'service-definitions'],
    queryFn: () => ops.catalog.listDefinitions(tenantId, { limit: 200 }),
    staleTime: 5 * 60_000,
    select: (page) =>
      page.items.map((d) => ({
        value: d.id,
        label: d.name,
        unitType: d.defaultUnitType,
      })),
  });
}

export function useServiceName(definitionId: string | null | undefined): string | null | undefined {
  const ops = useOps();
  const tenantId = useTenantId();
  const q = useQuery({
    queryKey: ['provider', tenantId, 'service-name', definitionId ?? ''],
    queryFn: () => ops.catalog.getDefinition(tenantId, definitionId!),
    enabled: Boolean(definitionId),
    staleTime: 5 * 60_000,
    retry: false,
  });
  if (q.isPending && q.fetchStatus !== 'idle') return undefined;
  return q.data?.data.name ?? null;
}

/**
 * The live check. It is a query keyed on the form's fields, so the answer follows the
 * question: change the service and the right column asks again. The server records every
 * evaluation, which is what makes "what did the system say" answerable afterwards.
 */
export function useLiveEligibility(body: EligibilityCheckRequest | null) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'eligibility', body],
    queryFn: () => ops.eligibility.check(tenantId, body!),
    enabled: body !== null,
    retry: false,
    staleTime: 60_000,
  });
}

export function useMyRequests(query: ServiceRequestListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'requests', query],
    queryFn: () => ops.requests.list(tenantId, query),
    placeholderData: (previous) => previous,
  });
}

export function useRequest(requestId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'request', requestId],
    queryFn: () => ops.requests.get(tenantId, requestId),
    enabled: requestId !== '',
  });
}

/** Create the draft and submit it in one go: the desk has no use for a draft. */
export function useCreateAndSubmit() {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (body: CreateServiceRequest) => {
      const created = await ops.requests.create(tenantId, body);
      return ops.requests.submit(tenantId, created.data.id, created.etag);
    },
    onSuccess: () => client.invalidateQueries({ queryKey: ['provider', tenantId, 'requests'] }),
  });
}
