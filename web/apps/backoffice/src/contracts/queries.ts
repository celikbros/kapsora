import type {
  ContractListQuery,
  CreateContractRequest,
  CreateContractVersionRequest,
  PackageDefinitionInput,
  PriceItemInput,
  PriceItemListQuery,
  PriceListInput,
  ProviderQuotaInput,
  PutPaymentTermRequest,
  ReasonCommand,
  ResolvePriceRequest,
  ReviewComment,
  UpdateContractRequest,
  UpdateContractVersionRequest,
} from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

export const contractKeys = {
  all: (tenantId: string) => ['contracts', tenantId] as const,
  list: (tenantId: string, query: ContractListQuery) =>
    ['contracts', tenantId, 'list', query] as const,
  one: (tenantId: string, id: string) => ['contracts', tenantId, 'one', id] as const,
  versions: (tenantId: string, contractId: string) =>
    ['contracts', tenantId, 'versions', contractId] as const,
  version: (tenantId: string, id: string) => ['contracts', tenantId, 'version', id] as const,
  priceItems: (tenantId: string, listId: string, query: PriceItemListQuery) =>
    ['contracts', tenantId, 'priceItems', listId, query] as const,
  packages: (tenantId: string, versionId: string) =>
    ['contracts', tenantId, 'packages', versionId] as const,
  quotas: (tenantId: string, versionId: string) =>
    ['contracts', tenantId, 'quotas', versionId] as const,
  paymentTerm: (tenantId: string, versionId: string) =>
    ['contracts', tenantId, 'paymentTerm', versionId] as const,
};

export function useContracts(query: ContractListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: contractKeys.list(tenantId, query),
    queryFn: () => ops.contracts.list(tenantId, query),
    placeholderData: (previous) => previous,
  });
}

export function useContract(contractId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: contractKeys.one(tenantId, contractId),
    queryFn: () => ops.contracts.get(tenantId, contractId),
    enabled: contractId !== '',
  });
}

export function useCreateContract() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateContractRequest; idempotencyKey: string }) =>
      ops.contracts.create(tenantId, input.body, input.idempotencyKey),
    onSuccess: () => void qc.invalidateQueries({ queryKey: contractKeys.all(tenantId) }),
  });
}

export function useUpdateContract(contractId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { etag: string; patch: UpdateContractRequest }) =>
      ops.contracts.patch(tenantId, contractId, input.etag, input.patch),
    onSuccess: () => void qc.invalidateQueries({ queryKey: contractKeys.all(tenantId) }),
  });
}

export function useContractVersions(contractId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: contractKeys.versions(tenantId, contractId),
    queryFn: () => ops.contracts.listVersions(tenantId, contractId),
    enabled: contractId !== '',
  });
}

export function useContractVersion(versionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: contractKeys.version(tenantId, versionId),
    queryFn: () => ops.contracts.getVersion(tenantId, versionId),
    enabled: versionId !== '',
  });
}

export function useCreateContractVersion(contractId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateContractVersionRequest; idempotencyKey: string }) =>
      ops.contracts.createVersion(tenantId, contractId, input.body, input.idempotencyKey),
    onSuccess: () => void qc.invalidateQueries({ queryKey: contractKeys.all(tenantId) }),
  });
}

/**
 * Everything that can be done to one version. They share an invalidation because a price
 * sheet write moves the version's own ETag: the sheet is part of the version, not a
 * document beside it.
 */
export function useVersionCommands(versionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  const invalidate = () => void qc.invalidateQueries({ queryKey: contractKeys.all(tenantId) });

  const patch = useMutation({
    mutationFn: (input: { etag: string; patch: UpdateContractVersionRequest }) =>
      ops.contracts.patchVersion(tenantId, versionId, input.etag, input.patch),
    onSuccess: invalidate,
  });
  const submit = useMutation({
    mutationFn: (input: { etag: string; body?: ReviewComment }) =>
      ops.contracts.submitVersion(tenantId, versionId, input.etag, input.body ?? {}),
    onSuccess: invalidate,
  });
  const publish = useMutation({
    mutationFn: (input: { etag: string; body?: ReviewComment }) =>
      ops.contracts.publishVersion(tenantId, versionId, input.etag, input.body ?? {}),
    onSuccess: invalidate,
  });
  const retire = useMutation({
    mutationFn: (input: { etag: string; body: ReasonCommand }) =>
      ops.contracts.retireVersion(tenantId, versionId, input.etag, input.body),
    onSuccess: invalidate,
  });
  const priceLists = useMutation({
    mutationFn: (input: { etag: string; items: PriceListInput[] }) =>
      ops.contracts.replacePriceLists(tenantId, versionId, input.etag, input.items),
    onSuccess: invalidate,
  });
  const packages = useMutation({
    mutationFn: (input: { etag: string; items: PackageDefinitionInput[] }) =>
      ops.contracts.replacePackageDefinitions(tenantId, versionId, input.etag, input.items),
    onSuccess: invalidate,
  });
  const quotas = useMutation({
    mutationFn: (input: { etag: string; items: ProviderQuotaInput[] }) =>
      ops.contracts.replaceProviderQuotas(tenantId, versionId, input.etag, input.items),
    onSuccess: invalidate,
  });
  const paymentTerm = useMutation({
    mutationFn: (input: { etag: string; body: PutPaymentTermRequest }) =>
      ops.contracts.putPaymentTerm(tenantId, versionId, input.etag, input.body),
    onSuccess: invalidate,
  });

  return { patch, submit, publish, retire, priceLists, packages, quotas, paymentTerm };
}

export function usePriceItems(priceListId: string, query: PriceItemListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: contractKeys.priceItems(tenantId, priceListId, query),
    queryFn: () => ops.contracts.listPriceItems(tenantId, priceListId, query),
    enabled: priceListId !== '',
    placeholderData: (previous) => previous,
  });
}

export function useReplacePriceItems(priceListId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { etag: string; items: PriceItemInput[] }) =>
      ops.contracts.replacePriceItems(tenantId, priceListId, input.etag, input.items),
    onSuccess: () => void qc.invalidateQueries({ queryKey: contractKeys.all(tenantId) }),
  });
}

export function usePackageDefinitions(versionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: contractKeys.packages(tenantId, versionId),
    queryFn: () => ops.contracts.listPackageDefinitions(tenantId, versionId),
    enabled: versionId !== '',
  });
}

export function useProviderQuotas(versionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: contractKeys.quotas(tenantId, versionId),
    queryFn: () => ops.contracts.listProviderQuotas(tenantId, versionId),
    enabled: versionId !== '',
  });
}

export function usePaymentTerm(versionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: contractKeys.paymentTerm(tenantId, versionId),
    queryFn: () => ops.contracts.getPaymentTerm(tenantId, versionId),
    enabled: versionId !== '',
  });
}

/** Price resolution is a question, never a cached answer: the ladder is what it explains. */
export function useResolvePrice() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({
    mutationFn: (body: ResolvePriceRequest) => ops.contracts.resolvePrice(tenantId, body),
  });
}
