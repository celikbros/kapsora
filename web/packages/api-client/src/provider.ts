import type { KapsoraClient } from './client';
import { randomId } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type Provider = components['schemas']['Provider'];
export type ProviderPage = components['schemas']['ProviderPage'];
export type ProviderType = components['schemas']['ProviderType'];
export type ProviderStatus = components['schemas']['ProviderStatus'];
export type CreateProviderRequest = components['schemas']['CreateProviderRequest'];
export type UpdateProviderRequest = components['schemas']['UpdateProviderRequest'];
export type ProviderLocation = components['schemas']['ProviderLocation'];
export type ProviderLocationPage = components['schemas']['ProviderLocationPage'];
export type ProviderLocationStatus = components['schemas']['ProviderLocationStatus'];
export type CreateProviderLocationRequest = components['schemas']['CreateProviderLocationRequest'];
export type UpdateProviderLocationRequest = components['schemas']['UpdateProviderLocationRequest'];
export type ProviderCapability = components['schemas']['ProviderCapability'];
export type ProviderCapabilityInput = components['schemas']['ProviderCapabilityInput'];
export type Practitioner = components['schemas']['Practitioner'];
export type PractitionerPage = components['schemas']['PractitionerPage'];
export type PractitionerStatus = components['schemas']['PractitionerStatus'];
export type PractitionerRole = components['schemas']['PractitionerRole'];
export type RegistrationAuthority = components['schemas']['RegistrationAuthority'];
export type CreatePractitionerRequest = components['schemas']['CreatePractitionerRequest'];
export type UpdatePractitionerRequest = components['schemas']['UpdatePractitionerRequest'];
export type PractitionerLocation = components['schemas']['PractitionerLocation'];
export type PractitionerLocationInput = components['schemas']['PractitionerLocationInput'];
export type PractitionerRegistrationSearchRequest =
  components['schemas']['PractitionerRegistrationSearchRequest'];
export type ProviderSearchResult = components['schemas']['ProviderSearchResult'];
export type ProviderSearchPage = components['schemas']['ProviderSearchPage'];
export type ReasonCommand = components['schemas']['ReasonCommand'];

/** Filters of the provider list. */
export interface ProviderListQuery {
  cursor?: string;
  limit?: number;
  q?: string;
  providerType?: ProviderType;
  status?: ProviderStatus;
  networkTier?: string;
}

/** Which location can deliver this service, and when. */
export interface ProviderSearchQuery {
  serviceDefinitionId: string;
  cursor?: string;
  limit?: number;
  city?: string;
  asOf?: string;
  q?: string;
}

/** Filters of a provider's location list. */
export interface ProviderLocationListQuery {
  cursor?: string;
  limit?: number;
  q?: string;
  city?: string;
  status?: ProviderLocationStatus;
}

/** Filters of a provider's practitioner list. */
export interface PractitionerListQuery {
  cursor?: string;
  limit?: number;
  q?: string;
  status?: PractitionerStatus;
  branchCode?: string;
}

/**
 * Provider profiles, their locations, what each location can deliver and the
 * practitioners registered there.
 *
 * A practitioner's registration number is a professional identity number: it travels only
 * in a request body, never in a path or a query key, and responses carry the masked form
 * only. `searchByRegistration` needs a recent step-up and is audited.
 */
export function providerOperations(client: KapsoraClient) {
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
    async list(tenantId: string, query: ProviderListQuery = {}): Promise<ProviderPage> {
      const q: ProviderListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.q) q.q = query.q;
      if (query.providerType) q.providerType = query.providerType;
      if (query.status) q.status = query.status;
      if (query.networkTier) q.networkTier = query.networkTier;
      return (
        await unwrap(
          client.GET('/api/v1/providers', { params: { header: header(tenantId), query: q } }),
        )
      ).data;
    },

    async create(
      tenantId: string,
      body: CreateProviderRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Provider>> {
      const r = await unwrap(
        client.POST('/api/v1/providers', {
          params: { header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Which provider locations can deliver a service on a date. */
    async search(tenantId: string, query: ProviderSearchQuery): Promise<ProviderSearchPage> {
      const q: ProviderSearchQuery = { serviceDefinitionId: query.serviceDefinitionId };
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.city) q.city = query.city;
      if (query.asOf) q.asOf = query.asOf;
      if (query.q) q.q = query.q;
      return (
        await unwrap(
          client.GET('/api/v1/providers/search', {
            params: { header: header(tenantId), query: q },
          }),
        )
      ).data;
    },

    async get(tenantId: string, providerId: string): Promise<Versioned<Provider>> {
      const r = await unwrap(
        client.GET('/api/v1/providers/{providerId}', {
          params: { header: header(tenantId), path: { providerId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patch(
      tenantId: string,
      providerId: string,
      etag: string,
      patch: UpdateProviderRequest,
    ): Promise<Versioned<Provider>> {
      const r = await unwrap(
        client.PATCH('/api/v1/providers/{providerId}', {
          params: { header: withEtag(tenantId, etag), path: { providerId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** PENDING or SUSPENDED to ACTIVE; the status never moves through a patch. */
    async activate(
      tenantId: string,
      providerId: string,
      etag: string,
    ): Promise<Versioned<Provider>> {
      const r = await unwrap(
        client.POST('/api/v1/providers/{providerId}/activate', {
          params: { header: withEtag(tenantId, etag), path: { providerId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async suspend(
      tenantId: string,
      providerId: string,
      etag: string,
      body: ReasonCommand,
    ): Promise<Versioned<Provider>> {
      const r = await unwrap(
        client.POST('/api/v1/providers/{providerId}/suspend', {
          params: { header: withEtag(tenantId, etag), path: { providerId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Final: a terminated provider accepts no further transition. */
    async terminate(
      tenantId: string,
      providerId: string,
      etag: string,
      body: ReasonCommand,
    ): Promise<Versioned<Provider>> {
      const r = await unwrap(
        client.POST('/api/v1/providers/{providerId}/terminate', {
          params: { header: withEtag(tenantId, etag), path: { providerId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async listLocations(
      tenantId: string,
      providerId: string,
      query: ProviderLocationListQuery = {},
    ): Promise<ProviderLocationPage> {
      const q: ProviderLocationListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.q) q.q = query.q;
      if (query.city) q.city = query.city;
      if (query.status) q.status = query.status;
      return (
        await unwrap(
          client.GET('/api/v1/providers/{providerId}/locations', {
            params: { header: header(tenantId), path: { providerId }, query: q },
          }),
        )
      ).data;
    },

    async createLocation(
      tenantId: string,
      providerId: string,
      body: CreateProviderLocationRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<ProviderLocation>> {
      const r = await unwrap(
        client.POST('/api/v1/providers/{providerId}/locations', {
          params: {
            header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey },
            path: { providerId },
          },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getLocation(tenantId: string, locationId: string): Promise<Versioned<ProviderLocation>> {
      const r = await unwrap(
        client.GET('/api/v1/provider-locations/{locationId}', {
          params: { header: header(tenantId), path: { locationId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchLocation(
      tenantId: string,
      locationId: string,
      etag: string,
      patch: UpdateProviderLocationRequest,
    ): Promise<Versioned<ProviderLocation>> {
      const r = await unwrap(
        client.PATCH('/api/v1/provider-locations/{locationId}', {
          params: { header: withEtag(tenantId, etag), path: { locationId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    async listCapabilities(tenantId: string, locationId: string): Promise<ProviderCapability[]> {
      return (
        await unwrap(
          client.GET('/api/v1/provider-locations/{locationId}/capabilities', {
            params: { header: header(tenantId), path: { locationId } },
          }),
        )
      ).data.items;
    },

    /** Replaces the whole capability set; If-Match carries the location's ETag. */
    async replaceCapabilities(
      tenantId: string,
      locationId: string,
      etag: string,
      items: ProviderCapabilityInput[],
    ): Promise<Versioned<ProviderCapability[]>> {
      const r = await unwrap(
        client.PUT('/api/v1/provider-locations/{locationId}/capabilities', {
          params: { header: withEtag(tenantId, etag), path: { locationId } },
          body: { items },
        }),
      );
      return versioned(r.data.items, r.response);
    },

    async listPractitioners(
      tenantId: string,
      providerId: string,
      query: PractitionerListQuery = {},
    ): Promise<PractitionerPage> {
      const q: PractitionerListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.q) q.q = query.q;
      if (query.status) q.status = query.status;
      if (query.branchCode) q.branchCode = query.branchCode;
      return (
        await unwrap(
          client.GET('/api/v1/providers/{providerId}/practitioners', {
            params: { header: header(tenantId), path: { providerId }, query: q },
          }),
        )
      ).data;
    },

    async createPractitioner(
      tenantId: string,
      providerId: string,
      body: CreatePractitionerRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Practitioner>> {
      const r = await unwrap(
        client.POST('/api/v1/providers/{providerId}/practitioners', {
          params: {
            header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey },
            path: { providerId },
          },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getPractitioner(
      tenantId: string,
      practitionerId: string,
    ): Promise<Versioned<Practitioner>> {
      const r = await unwrap(
        client.GET('/api/v1/practitioners/{practitionerId}', {
          params: { header: header(tenantId), path: { practitionerId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchPractitioner(
      tenantId: string,
      practitionerId: string,
      etag: string,
      patch: UpdatePractitionerRequest,
    ): Promise<Versioned<Practitioner>> {
      const r = await unwrap(
        client.PATCH('/api/v1/practitioners/{practitionerId}', {
          params: { header: withEtag(tenantId, etag), path: { practitionerId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Replaces the whole assignment set; If-Match carries the practitioner's ETag. */
    async replacePractitionerLocations(
      tenantId: string,
      practitionerId: string,
      etag: string,
      items: PractitionerLocationInput[],
    ): Promise<Versioned<PractitionerLocation[]>> {
      const r = await unwrap(
        client.PUT('/api/v1/practitioners/{practitionerId}/locations', {
          params: { header: withEtag(tenantId, etag), path: { practitionerId } },
          body: { items },
        }),
      );
      return versioned(r.data.items, r.response);
    },

    /**
     * Finds the practitioner behind a registration number. The number travels in the body
     * so it can never reach a log, a proxy or a browser history; the answer carries the
     * masked form only. Needs a recent step-up.
     */
    async searchByRegistration(
      tenantId: string,
      body: PractitionerRegistrationSearchRequest,
    ): Promise<Practitioner> {
      return (
        await unwrap(
          client.POST('/api/v1/practitioners/search-by-registration', {
            params: { header: header(tenantId) },
            body,
          }),
        )
      ).data;
    },
  };
}
