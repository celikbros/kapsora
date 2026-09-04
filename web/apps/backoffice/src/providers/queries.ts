import type {
  CreatePractitionerRequest,
  CreateProviderLocationRequest,
  CreateProviderRequest,
  PractitionerListQuery,
  PractitionerLocationInput,
  PractitionerRegistrationSearchRequest,
  ProviderCapabilityInput,
  ProviderListQuery,
  ProviderLocationListQuery,
  ReasonCommand,
  UpdatePractitionerRequest,
  UpdateProviderLocationRequest,
  UpdateProviderRequest,
} from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

export const providerKeys = {
  all: (tenantId: string) => ['providers', tenantId] as const,
  list: (tenantId: string, query: ProviderListQuery) =>
    ['providers', tenantId, 'list', query] as const,
  detail: (tenantId: string, id: string) => ['providers', tenantId, 'detail', id] as const,
  locations: (tenantId: string, providerId: string, query: ProviderLocationListQuery) =>
    ['providers', tenantId, 'locations', providerId, query] as const,
  capabilities: (tenantId: string, locationId: string) =>
    ['providers', tenantId, 'capabilities', locationId] as const,
  practitioners: (tenantId: string, providerId: string, query: PractitionerListQuery) =>
    ['providers', tenantId, 'practitioners', providerId, query] as const,
  serviceOptions: (tenantId: string) => ['providers', tenantId, 'service-options'] as const,
};

export function useProviderList(query: ProviderListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: providerKeys.list(tenantId, query),
    queryFn: () => ops.providers.list(tenantId, query),
    placeholderData: (previous) => previous,
  });
}

export function useProvider(providerId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: providerKeys.detail(tenantId, providerId),
    queryFn: () => ops.providers.get(tenantId, providerId),
  });
}

export function useCreateProvider() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateProviderRequest; idempotencyKey: string }) =>
      ops.providers.create(tenantId, input.body, input.idempotencyKey),
    onSuccess: (result) => {
      qc.setQueryData(providerKeys.detail(tenantId, result.data.id), result);
      void qc.invalidateQueries({ queryKey: providerKeys.all(tenantId) });
    },
  });
}

export function useUpdateProvider(providerId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { etag: string; patch: UpdateProviderRequest }) =>
      ops.providers.patch(tenantId, providerId, input.etag, input.patch),
    onSuccess: (result) => {
      qc.setQueryData(providerKeys.detail(tenantId, providerId), result);
      void qc.invalidateQueries({ queryKey: providerKeys.all(tenantId) });
    },
  });
}

/** Which explicit command the operator asked for. Activate carries no reason. */
export type ProviderCommand =
  | { kind: 'activate'; etag: string }
  | { kind: 'suspend'; etag: string; body: ReasonCommand }
  | { kind: 'terminate'; etag: string; body: ReasonCommand };

/**
 * The lifecycle commands. They are separate from the profile patch because the server
 * refuses a status inside a merge-patch: a provider is activated, suspended or terminated,
 * never edited into a new state.
 */
export function useProviderTransition(providerId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (command: ProviderCommand) => {
      if (command.kind === 'activate') {
        return ops.providers.activate(tenantId, providerId, command.etag);
      }
      if (command.kind === 'suspend') {
        return ops.providers.suspend(tenantId, providerId, command.etag, command.body);
      }
      return ops.providers.terminate(tenantId, providerId, command.etag, command.body);
    },
    onSuccess: (result) => {
      qc.setQueryData(providerKeys.detail(tenantId, providerId), result);
      void qc.invalidateQueries({ queryKey: providerKeys.all(tenantId) });
    },
  });
}

export function useProviderLocations(providerId: string, query: ProviderLocationListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: providerKeys.locations(tenantId, providerId, query),
    queryFn: () => ops.providers.listLocations(tenantId, providerId, query),
    placeholderData: (previous) => previous,
  });
}

export function useCreateLocation(providerId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateProviderLocationRequest; idempotencyKey: string }) =>
      ops.providers.createLocation(tenantId, providerId, input.body, input.idempotencyKey),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: providerKeys.all(tenantId) });
    },
  });
}

export function useUpdateLocation() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      locationId: string;
      etag: string;
      patch: UpdateProviderLocationRequest;
    }) => ops.providers.patchLocation(tenantId, input.locationId, input.etag, input.patch),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: providerKeys.all(tenantId) });
    },
  });
}

export function useCapabilities(locationId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: providerKeys.capabilities(tenantId, locationId),
    queryFn: () => ops.providers.listCapabilities(tenantId, locationId),
    enabled: locationId !== '',
  });
}

/** Writes the whole capability set; the If-Match carries the location's ETag. */
export function useReplaceCapabilities(locationId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { etag: string; items: ProviderCapabilityInput[] }) =>
      ops.providers.replaceCapabilities(tenantId, locationId, input.etag, input.items),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: providerKeys.all(tenantId) });
    },
  });
}

export function usePractitioners(providerId: string, query: PractitionerListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: providerKeys.practitioners(tenantId, providerId, query),
    queryFn: () => ops.providers.listPractitioners(tenantId, providerId, query),
    placeholderData: (previous) => previous,
  });
}

export function useCreatePractitioner(providerId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreatePractitionerRequest; idempotencyKey: string }) =>
      ops.providers.createPractitioner(tenantId, providerId, input.body, input.idempotencyKey),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: providerKeys.all(tenantId) });
    },
  });
}

export function useUpdatePractitioner() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      practitionerId: string;
      etag: string;
      patch: UpdatePractitionerRequest;
    }) => ops.providers.patchPractitioner(tenantId, input.practitionerId, input.etag, input.patch),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: providerKeys.all(tenantId) });
    },
  });
}

/** Writes the whole assignment set; the If-Match carries the practitioner's ETag. */
export function useReplacePractitionerLocations() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      practitionerId: string;
      etag: string;
      items: PractitionerLocationInput[];
    }) =>
      ops.providers.replacePractitionerLocations(
        tenantId,
        input.practitionerId,
        input.etag,
        input.items,
      ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: providerKeys.all(tenantId) });
    },
  });
}

/**
 * The registration lookup. It is a mutation rather than a query on purpose: the number
 * must never become part of a cache key, and the call is audited, so it runs only when
 * asked and behind a step-up.
 */
export function useRegistrationSearch() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({
    mutationFn: (body: PractitionerRegistrationSearchRequest) =>
      ops.providers.searchByRegistration(tenantId, body),
  });
}

/**
 * The catalog rows a capability may name. Both lists are small and change rarely, so they
 * are fetched once and shared by every location's editor.
 */
export function useServiceOptions() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: providerKeys.serviceOptions(tenantId),
    queryFn: async () => {
      const [categories, definitions] = await Promise.all([
        ops.catalog.listCategories(tenantId, { limit: 200, active: true }),
        ops.catalog.listDefinitions(tenantId, { limit: 200, active: true }),
      ]);
      return { categories: categories.items, definitions: definitions.items };
    },
    staleTime: 5 * 60_000,
  });
}
