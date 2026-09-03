import type { KapsoraClient } from './client';
import { randomId } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type Person = components['schemas']['Person'];
export type PersonSummary = components['schemas']['PersonSummary'];
export type PersonPage = components['schemas']['PersonPage'];
export type CreatePersonRequest = components['schemas']['CreatePersonRequest'];
export type UpdatePersonRequest = components['schemas']['UpdatePersonRequest'];
export type IdentifierSearchRequest = components['schemas']['IdentifierSearchRequest'];
export type PersonRelationship = components['schemas']['PersonRelationship'];
export type CreateRelationshipRequest = components['schemas']['CreateRelationshipRequest'];
export type EndPeriodCommand = components['schemas']['EndPeriodCommand'];
export type SponsorMembership = components['schemas']['SponsorMembership'];
export type CreateMembershipRequest = components['schemas']['CreateMembershipRequest'];
export type UpdateMembershipRequest = components['schemas']['UpdateMembershipRequest'];
export type PartyCatalogs = components['schemas']['PartyCatalogs'];
export type PartyCatalogEntry = components['schemas']['PartyCatalogEntry'];
export type PersonStatus = PersonSummary['status'];

/** Filters of the person list. An identifier value is never a list filter. */
export interface PersonListQuery {
  cursor?: string;
  limit?: number;
  q?: string;
  status?: PersonStatus;
  sponsorOrganizationId?: string;
}

/**
 * People, their family relationships and their sponsor memberships. Identifier values
 * only ever travel in the body of the search command; everything else works with ids.
 */
export function peopleOperations(client: KapsoraClient) {
  return {
    async list(tenantId: string, query: PersonListQuery = {}): Promise<PersonPage> {
      const q: PersonListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.q) q.q = query.q;
      if (query.status) q.status = query.status;
      if (query.sponsorOrganizationId) q.sponsorOrganizationId = query.sponsorOrganizationId;
      return (
        await unwrap(
          client.GET('/api/v1/people', {
            params: { header: { 'X-Tenant-ID': tenantId }, query: q },
          }),
        )
      ).data;
    },

    async get(tenantId: string, personId: string): Promise<Versioned<Person>> {
      const r = await unwrap(
        client.GET('/api/v1/people/{personId}', {
          params: { header: { 'X-Tenant-ID': tenantId }, path: { personId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async create(
      tenantId: string,
      body: CreatePersonRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Person>> {
      const r = await unwrap(
        client.POST('/api/v1/people', {
          params: { header: { 'X-Tenant-ID': tenantId, 'Idempotency-Key': idempotencyKey } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async patch(
      tenantId: string,
      personId: string,
      etag: string,
      patch: UpdatePersonRequest,
    ): Promise<Versioned<Person>> {
      const r = await unwrap(
        client.PATCH('/api/v1/people/{personId}', {
          params: { header: { 'X-Tenant-ID': tenantId, 'If-Match': etag }, path: { personId } },
          body: patch,
          headers: { 'Content-Type': 'application/merge-patch+json' },
          bodySerializer: (b) => JSON.stringify(b),
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Finds the person carrying an identifier. Needs the search permission and a recent
     * step-up; the value stays in the request body and is never put in a URL or cached.
     */
    async searchByIdentifier(
      tenantId: string,
      body: IdentifierSearchRequest,
    ): Promise<PersonSummary> {
      return (
        await unwrap(
          client.POST('/api/v1/people/search-by-identifier', {
            params: { header: { 'X-Tenant-ID': tenantId } },
            body,
          }),
        )
      ).data;
    },

    async catalogs(tenantId: string): Promise<PartyCatalogs> {
      return (
        await unwrap(
          client.GET('/api/v1/party/catalogs', { params: { header: { 'X-Tenant-ID': tenantId } } }),
        )
      ).data;
    },

    async listRelationships(tenantId: string, personId: string): Promise<PersonRelationship[]> {
      return (
        await unwrap(
          client.GET('/api/v1/people/{personId}/relationships', {
            params: { header: { 'X-Tenant-ID': tenantId }, path: { personId } },
          }),
        )
      ).data.items;
    },

    async createRelationship(
      tenantId: string,
      personId: string,
      body: CreateRelationshipRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<PersonRelationship>> {
      const r = await unwrap(
        client.POST('/api/v1/people/{personId}/relationships', {
          params: {
            header: { 'X-Tenant-ID': tenantId, 'Idempotency-Key': idempotencyKey },
            path: { personId },
          },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async endRelationship(
      tenantId: string,
      personId: string,
      relationshipId: string,
      etag: string,
      body: EndPeriodCommand,
    ): Promise<Versioned<PersonRelationship>> {
      const r = await unwrap(
        client.POST('/api/v1/people/{personId}/relationships/{relationshipId}/end', {
          params: {
            header: { 'X-Tenant-ID': tenantId, 'If-Match': etag },
            path: { personId, relationshipId },
          },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async listMemberships(tenantId: string, personId: string): Promise<SponsorMembership[]> {
      return (
        await unwrap(
          client.GET('/api/v1/people/{personId}/memberships', {
            params: { header: { 'X-Tenant-ID': tenantId }, path: { personId } },
          }),
        )
      ).data.items;
    },

    async createMembership(
      tenantId: string,
      personId: string,
      body: CreateMembershipRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<SponsorMembership>> {
      const r = await unwrap(
        client.POST('/api/v1/people/{personId}/memberships', {
          params: {
            header: { 'X-Tenant-ID': tenantId, 'Idempotency-Key': idempotencyKey },
            path: { personId },
          },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchMembership(
      tenantId: string,
      personId: string,
      membershipId: string,
      etag: string,
      patch: UpdateMembershipRequest,
    ): Promise<Versioned<SponsorMembership>> {
      const r = await unwrap(
        client.PATCH('/api/v1/people/{personId}/memberships/{membershipId}', {
          params: {
            header: { 'X-Tenant-ID': tenantId, 'If-Match': etag },
            path: { personId, membershipId },
          },
          body: patch,
          headers: { 'Content-Type': 'application/merge-patch+json' },
          bodySerializer: (b) => JSON.stringify(b),
        }),
      );
      return versioned(r.data, r.response);
    },
  };
}
