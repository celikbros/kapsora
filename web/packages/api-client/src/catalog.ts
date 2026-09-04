import type { KapsoraClient } from './client';
import { randomId } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type ServiceDomain = components['schemas']['ServiceDomain'];
export type FulfillmentMode = components['schemas']['FulfillmentMode'];
export type ServiceUnitType = components['schemas']['ServiceUnitType'];
export type CodeSystemAuthority = components['schemas']['CodeSystemAuthority'];
export type ServiceCategory = components['schemas']['ServiceCategory'];
export type ServiceCategoryPage = components['schemas']['ServiceCategoryPage'];
export type CreateServiceCategoryRequest = components['schemas']['CreateServiceCategoryRequest'];
export type UpdateServiceCategoryRequest = components['schemas']['UpdateServiceCategoryRequest'];
export type ServiceDefinition = components['schemas']['ServiceDefinition'];
export type ServiceDefinitionPage = components['schemas']['ServiceDefinitionPage'];
export type CreateServiceDefinitionRequest =
  components['schemas']['CreateServiceDefinitionRequest'];
export type UpdateServiceDefinitionRequest =
  components['schemas']['UpdateServiceDefinitionRequest'];
export type ServiceCodeMapping = components['schemas']['ServiceCodeMapping'];
export type ServiceCodeMappingInput = components['schemas']['ServiceCodeMappingInput'];
export type CodeSystem = components['schemas']['CodeSystem'];
export type CodeSystemPage = components['schemas']['CodeSystemPage'];
export type CodeSystemStatus = CodeSystem['status'];
export type CreateCodeSystemRequest = components['schemas']['CreateCodeSystemRequest'];
export type UpdateCodeSystemRequest = components['schemas']['UpdateCodeSystemRequest'];
export type CodeValue = components['schemas']['CodeValue'];
export type CodeValuePage = components['schemas']['CodeValuePage'];
export type CodeValueInput = components['schemas']['CodeValueInput'];
export type CodeValueImportResult = components['schemas']['CodeValueImportResult'];

/** Filters of the service category list. */
export interface ServiceCategoryListQuery {
  cursor?: string;
  limit?: number;
  q?: string;
  parentId?: string;
  domain?: ServiceDomain;
  active?: boolean;
}

/** Filters of the service definition list. */
export interface ServiceDefinitionListQuery {
  cursor?: string;
  limit?: number;
  q?: string;
  categoryId?: string;
  domain?: ServiceDomain;
  active?: boolean;
}

/** Filters of the code system list. */
export interface CodeSystemListQuery {
  cursor?: string;
  limit?: number;
  q?: string;
  authority?: CodeSystemAuthority;
  status?: CodeSystemStatus;
}

/** Filters of the code value list; `asOf` defaults to today on the server. */
export interface CodeValueListQuery {
  cursor?: string;
  limit?: number;
  asOf?: string;
  code?: string;
  q?: string;
}

/**
 * The service catalog: categories, the definitions inside them, the external code systems
 * a definition is reported under, and the values of those systems as of a date. Codes are
 * immutable once created, so a wrong one is retired and replaced rather than renamed.
 */
export function catalogOperations(client: KapsoraClient) {
  const header = (tenantId: string) => ({ 'X-Tenant-ID': tenantId });
  const withEtag = (tenantId: string, etag: string) => ({
    'X-Tenant-ID': tenantId,
    'If-Match': etag,
  });
  const mergePatch = {
    headers: { 'Content-Type': 'application/merge-patch+json' },
    bodySerializer: (b: unknown) => JSON.stringify(b),
  };

  return {
    async listCategories(
      tenantId: string,
      query: ServiceCategoryListQuery = {},
    ): Promise<ServiceCategoryPage> {
      const q: ServiceCategoryListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.q) q.q = query.q;
      if (query.parentId) q.parentId = query.parentId;
      if (query.domain) q.domain = query.domain;
      if (query.active !== undefined) q.active = query.active;
      return (
        await unwrap(
          client.GET('/api/v1/service-categories', {
            params: { header: header(tenantId), query: q },
          }),
        )
      ).data;
    },

    async createCategory(
      tenantId: string,
      body: CreateServiceCategoryRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<ServiceCategory>> {
      const r = await unwrap(
        client.POST('/api/v1/service-categories', {
          params: { header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getCategory(tenantId: string, categoryId: string): Promise<Versioned<ServiceCategory>> {
      const r = await unwrap(
        client.GET('/api/v1/service-categories/{categoryId}', {
          params: { header: header(tenantId), path: { categoryId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchCategory(
      tenantId: string,
      categoryId: string,
      etag: string,
      patch: UpdateServiceCategoryRequest,
    ): Promise<Versioned<ServiceCategory>> {
      const r = await unwrap(
        client.PATCH('/api/v1/service-categories/{categoryId}', {
          params: { header: withEtag(tenantId, etag), path: { categoryId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    async listDefinitions(
      tenantId: string,
      query: ServiceDefinitionListQuery = {},
    ): Promise<ServiceDefinitionPage> {
      const q: ServiceDefinitionListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.q) q.q = query.q;
      if (query.categoryId) q.categoryId = query.categoryId;
      if (query.domain) q.domain = query.domain;
      if (query.active !== undefined) q.active = query.active;
      return (
        await unwrap(
          client.GET('/api/v1/service-definitions', {
            params: { header: header(tenantId), query: q },
          }),
        )
      ).data;
    },

    async createDefinition(
      tenantId: string,
      body: CreateServiceDefinitionRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<ServiceDefinition>> {
      const r = await unwrap(
        client.POST('/api/v1/service-definitions', {
          params: { header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getDefinition(
      tenantId: string,
      definitionId: string,
    ): Promise<Versioned<ServiceDefinition>> {
      const r = await unwrap(
        client.GET('/api/v1/service-definitions/{definitionId}', {
          params: { header: header(tenantId), path: { definitionId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchDefinition(
      tenantId: string,
      definitionId: string,
      etag: string,
      patch: UpdateServiceDefinitionRequest,
    ): Promise<Versioned<ServiceDefinition>> {
      const r = await unwrap(
        client.PATCH('/api/v1/service-definitions/{definitionId}', {
          params: { header: withEtag(tenantId, etag), path: { definitionId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    async listCodeMappings(tenantId: string, definitionId: string): Promise<ServiceCodeMapping[]> {
      return (
        await unwrap(
          client.GET('/api/v1/service-definitions/{definitionId}/code-mappings', {
            params: { header: header(tenantId), path: { definitionId } },
          }),
        )
      ).data.items;
    },

    /** Replaces the whole mapping set; If-Match carries the definition's ETag. */
    async replaceCodeMappings(
      tenantId: string,
      definitionId: string,
      etag: string,
      items: ServiceCodeMappingInput[],
    ): Promise<Versioned<ServiceCodeMapping[]>> {
      const r = await unwrap(
        client.PUT('/api/v1/service-definitions/{definitionId}/code-mappings', {
          params: { header: withEtag(tenantId, etag), path: { definitionId } },
          body: { items },
        }),
      );
      return versioned(r.data.items, r.response);
    },

    async listCodeSystems(
      tenantId: string,
      query: CodeSystemListQuery = {},
    ): Promise<CodeSystemPage> {
      const q: CodeSystemListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.q) q.q = query.q;
      if (query.authority) q.authority = query.authority;
      if (query.status) q.status = query.status;
      return (
        await unwrap(
          client.GET('/api/v1/code-systems', { params: { header: header(tenantId), query: q } }),
        )
      ).data;
    },

    async createCodeSystem(
      tenantId: string,
      body: CreateCodeSystemRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<CodeSystem>> {
      const r = await unwrap(
        client.POST('/api/v1/code-systems', {
          params: { header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getCodeSystem(tenantId: string, codeSystemId: string): Promise<Versioned<CodeSystem>> {
      const r = await unwrap(
        client.GET('/api/v1/code-systems/{codeSystemId}', {
          params: { header: header(tenantId), path: { codeSystemId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchCodeSystem(
      tenantId: string,
      codeSystemId: string,
      etag: string,
      patch: UpdateCodeSystemRequest,
    ): Promise<Versioned<CodeSystem>> {
      const r = await unwrap(
        client.PATCH('/api/v1/code-systems/{codeSystemId}', {
          params: { header: withEtag(tenantId, etag), path: { codeSystemId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Values of the system valid on `asOf`; the server defaults it to today. */
    async listCodeValues(
      tenantId: string,
      codeSystemId: string,
      query: CodeValueListQuery = {},
    ): Promise<CodeValuePage> {
      const q: CodeValueListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.asOf) q.asOf = query.asOf;
      if (query.code) q.code = query.code;
      if (query.q) q.q = query.q;
      return (
        await unwrap(
          client.GET('/api/v1/code-systems/{codeSystemId}/values', {
            params: { header: header(tenantId), path: { codeSystemId }, query: q },
          }),
        )
      ).data;
    },

    /** Upserts up to 5000 values in one all-or-nothing call. */
    async importCodeValues(
      tenantId: string,
      codeSystemId: string,
      items: CodeValueInput[],
      idempotencyKey: string = randomId(),
    ): Promise<CodeValueImportResult> {
      return (
        await unwrap(
          client.POST('/api/v1/code-systems/{codeSystemId}/values:import', {
            params: {
              header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey },
              path: { codeSystemId },
            },
            body: { items },
          }),
        )
      ).data;
    },
  };
}
