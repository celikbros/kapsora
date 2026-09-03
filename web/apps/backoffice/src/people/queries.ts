import type {
  CreateMembershipRequest,
  CreatePersonRequest,
  CreateRelationshipRequest,
  EndPeriodCommand,
  IdentifierSearchRequest,
  PersonListQuery,
  UpdateMembershipRequest,
  UpdatePersonRequest,
} from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

export const peopleKeys = {
  all: (tenantId: string) => ['people', tenantId] as const,
  list: (tenantId: string, query: PersonListQuery) => ['people', tenantId, 'list', query] as const,
  detail: (tenantId: string, id: string) => ['people', tenantId, 'detail', id] as const,
  relationships: (tenantId: string, id: string) =>
    ['people', tenantId, 'relationships', id] as const,
  memberships: (tenantId: string, id: string) => ['people', tenantId, 'memberships', id] as const,
  catalogs: (tenantId: string) => ['people', tenantId, 'catalogs'] as const,
};

export function usePersonList(query: PersonListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: peopleKeys.list(tenantId, query),
    queryFn: () => ops.people.list(tenantId, query),
    placeholderData: (previous) => previous,
  });
}

export function usePerson(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: peopleKeys.detail(tenantId, personId),
    queryFn: () => ops.people.get(tenantId, personId),
  });
}

/** The tenant's identifier, relationship and membership types; rarely changes. */
export function usePartyCatalogs() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: peopleKeys.catalogs(tenantId),
    queryFn: () => ops.people.catalogs(tenantId),
    staleTime: 5 * 60_000,
  });
}

export function useCreatePerson() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreatePersonRequest; idempotencyKey: string }) =>
      ops.people.create(tenantId, input.body, input.idempotencyKey),
    onSuccess: (result) => {
      qc.setQueryData(peopleKeys.detail(tenantId, result.data.id), result);
      void qc.invalidateQueries({ queryKey: peopleKeys.all(tenantId) });
    },
  });
}

export function useUpdatePerson(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { etag: string; patch: UpdatePersonRequest }) =>
      ops.people.patch(tenantId, personId, input.etag, input.patch),
    onSuccess: (result) => {
      qc.setQueryData(peopleKeys.detail(tenantId, personId), result);
      void qc.invalidateQueries({ queryKey: peopleKeys.all(tenantId) });
    },
  });
}

/**
 * Identifier search. It is a mutation rather than a query on purpose: the value must not
 * become part of a cache key, and the call is audited, so it runs only when asked.
 */
export function useIdentifierSearch() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({
    mutationFn: (body: IdentifierSearchRequest) => ops.people.searchByIdentifier(tenantId, body),
  });
}

export function useRelationships(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: peopleKeys.relationships(tenantId, personId),
    queryFn: () => ops.people.listRelationships(tenantId, personId),
  });
}

export function useCreateRelationship(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateRelationshipRequest; idempotencyKey: string }) =>
      ops.people.createRelationship(tenantId, personId, input.body, input.idempotencyKey),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: peopleKeys.relationships(tenantId, personId) });
    },
  });
}

export function useEndRelationship(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { relationshipId: string; etag: string; body: EndPeriodCommand }) =>
      ops.people.endRelationship(tenantId, personId, input.relationshipId, input.etag, input.body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: peopleKeys.relationships(tenantId, personId) });
    },
  });
}

export function useMemberships(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: peopleKeys.memberships(tenantId, personId),
    queryFn: () => ops.people.listMemberships(tenantId, personId),
  });
}

export function useCreateMembership(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateMembershipRequest; idempotencyKey: string }) =>
      ops.people.createMembership(tenantId, personId, input.body, input.idempotencyKey),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: peopleKeys.memberships(tenantId, personId) });
    },
  });
}

export function useUpdateMembership(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { membershipId: string; etag: string; patch: UpdateMembershipRequest }) =>
      ops.people.patchMembership(tenantId, personId, input.membershipId, input.etag, input.patch),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: peopleKeys.memberships(tenantId, personId) });
    },
  });
}
