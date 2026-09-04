import type {
  CodeSystemListQuery,
  CodeValueInput,
  CodeValueListQuery,
  CreateCodeSystemRequest,
  CreateServiceCategoryRequest,
  CreateServiceDefinitionRequest,
  ServiceCategory,
  ServiceCodeMappingInput,
  ServiceDefinitionListQuery,
  UpdateCodeSystemRequest,
  UpdateServiceCategoryRequest,
  UpdateServiceDefinitionRequest,
} from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

export const catalogKeys = {
  all: (tenantId: string) => ['catalog', tenantId] as const,
  categoryTree: (tenantId: string) => ['catalog', tenantId, 'category-tree'] as const,
  category: (tenantId: string, id: string) => ['catalog', tenantId, 'category', id] as const,
  definitions: (tenantId: string, query: ServiceDefinitionListQuery) =>
    ['catalog', tenantId, 'definitions', query] as const,
  definition: (tenantId: string, id: string) => ['catalog', tenantId, 'definition', id] as const,
  mappings: (tenantId: string, definitionId: string) =>
    ['catalog', tenantId, 'mappings', definitionId] as const,
  codeSystems: (tenantId: string, query: CodeSystemListQuery) =>
    ['catalog', tenantId, 'code-systems', query] as const,
  codeSystem: (tenantId: string, id: string) => ['catalog', tenantId, 'code-system', id] as const,
  codeValues: (tenantId: string, codeSystemId: string, query: CodeValueListQuery) =>
    ['catalog', tenantId, 'code-values', codeSystemId, query] as const,
};

/** One page of categories; the tree walks the server's cursor until it runs out. */
const TREE_PAGE_SIZE = 200;
/** Stops a broken cursor from spinning forever; 5000 categories is far beyond the model. */
const TREE_MAX_PAGES = 25;

/**
 * Every category of the tenant, in one array. A tree cannot be drawn from a single page,
 * so this follows the opaque cursor the server hands back until there is no next page.
 */
export function useCategoryTree() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: catalogKeys.categoryTree(tenantId),
    queryFn: async (): Promise<ServiceCategory[]> => {
      const items: ServiceCategory[] = [];
      let cursor: string | undefined;
      for (let page = 0; page < TREE_MAX_PAGES; page += 1) {
        const result = await ops.catalog.listCategories(tenantId, {
          limit: TREE_PAGE_SIZE,
          ...(cursor ? { cursor } : {}),
        });
        items.push(...result.items);
        if (!result.nextCursor) break;
        cursor = result.nextCursor;
      }
      return items;
    },
  });
}

/**
 * One category, read fresh. The tree list carries no ETag, so the edit form reads the
 * category again and patches against the version it was actually shown.
 */
export function useServiceCategory(categoryId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: catalogKeys.category(tenantId, categoryId),
    queryFn: () => ops.catalog.getCategory(tenantId, categoryId),
    enabled: categoryId !== '',
  });
}

export function useCreateCategory() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateServiceCategoryRequest; idempotencyKey: string }) =>
      ops.catalog.createCategory(tenantId, input.body, input.idempotencyKey),
    onSuccess: () => void qc.invalidateQueries({ queryKey: catalogKeys.all(tenantId) }),
  });
}

export function useUpdateCategory(categoryId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { etag: string; patch: UpdateServiceCategoryRequest }) =>
      ops.catalog.patchCategory(tenantId, categoryId, input.etag, input.patch),
    onSuccess: () => void qc.invalidateQueries({ queryKey: catalogKeys.all(tenantId) }),
  });
}

export function useServiceDefinitions(query: ServiceDefinitionListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: catalogKeys.definitions(tenantId, query),
    queryFn: () => ops.catalog.listDefinitions(tenantId, query),
    placeholderData: (previous) => previous,
  });
}

export function useServiceDefinition(definitionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: catalogKeys.definition(tenantId, definitionId),
    queryFn: () => ops.catalog.getDefinition(tenantId, definitionId),
    enabled: definitionId !== '',
  });
}

export function useCreateDefinition() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateServiceDefinitionRequest; idempotencyKey: string }) =>
      ops.catalog.createDefinition(tenantId, input.body, input.idempotencyKey),
    onSuccess: (result) => {
      qc.setQueryData(catalogKeys.definition(tenantId, result.data.id), result);
      void qc.invalidateQueries({ queryKey: catalogKeys.all(tenantId) });
    },
  });
}

export function useUpdateDefinition(definitionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { etag: string; patch: UpdateServiceDefinitionRequest }) =>
      ops.catalog.patchDefinition(tenantId, definitionId, input.etag, input.patch),
    onSuccess: (result) => {
      qc.setQueryData(catalogKeys.definition(tenantId, definitionId), result);
      void qc.invalidateQueries({ queryKey: catalogKeys.all(tenantId) });
    },
  });
}

export function useCodeMappings(definitionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: catalogKeys.mappings(tenantId, definitionId),
    queryFn: () => ops.catalog.listCodeMappings(tenantId, definitionId),
    enabled: definitionId !== '',
  });
}

/**
 * Writes the whole mapping set. `If-Match` carries the definition's ETag, so a set saved
 * against a stale read is refused rather than silently overwriting someone else's rows.
 */
export function useReplaceCodeMappings(definitionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { etag: string; items: ServiceCodeMappingInput[] }) =>
      ops.catalog.replaceCodeMappings(tenantId, definitionId, input.etag, input.items),
    onSuccess: () => void qc.invalidateQueries({ queryKey: catalogKeys.all(tenantId) }),
  });
}

export function useCodeSystems(query: CodeSystemListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: catalogKeys.codeSystems(tenantId, query),
    queryFn: () => ops.catalog.listCodeSystems(tenantId, query),
    placeholderData: (previous) => previous,
  });
}

export function useCodeSystem(codeSystemId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: catalogKeys.codeSystem(tenantId, codeSystemId),
    queryFn: () => ops.catalog.getCodeSystem(tenantId, codeSystemId),
    enabled: codeSystemId !== '',
  });
}

export function useCreateCodeSystem() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateCodeSystemRequest; idempotencyKey: string }) =>
      ops.catalog.createCodeSystem(tenantId, input.body, input.idempotencyKey),
    onSuccess: () => void qc.invalidateQueries({ queryKey: catalogKeys.all(tenantId) }),
  });
}

export function useUpdateCodeSystem(codeSystemId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { etag: string; patch: UpdateCodeSystemRequest }) =>
      ops.catalog.patchCodeSystem(tenantId, codeSystemId, input.etag, input.patch),
    onSuccess: (result) => {
      qc.setQueryData(catalogKeys.codeSystem(tenantId, codeSystemId), result);
      void qc.invalidateQueries({ queryKey: catalogKeys.all(tenantId) });
    },
  });
}

/**
 * Values valid on a date. `asOf` is part of the cache key on purpose: a claim from last
 * year resolves against the codes that were valid then, not against today's list.
 */
export function useCodeValues(codeSystemId: string, query: CodeValueListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: catalogKeys.codeValues(tenantId, codeSystemId, query),
    queryFn: () => ops.catalog.listCodeValues(tenantId, codeSystemId, query),
    enabled: codeSystemId !== '',
    placeholderData: (previous) => previous,
  });
}

export function useImportCodeValues(codeSystemId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { items: CodeValueInput[]; idempotencyKey: string }) =>
      ops.catalog.importCodeValues(tenantId, codeSystemId, input.items, input.idempotencyKey),
    onSuccess: () => void qc.invalidateQueries({ queryKey: catalogKeys.all(tenantId) }),
  });
}
