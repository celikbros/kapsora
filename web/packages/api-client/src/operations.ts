import { benefitOperations } from './benefit';
import { catalogOperations } from './catalog';
import type { KapsoraClient } from './client';
import { contractOperations } from './contract';
import { eligibilityOperations } from './eligibility';
import { entitlementOperations } from './entitlements';
import { memberImportOperations } from './imports';
import { peopleOperations } from './people';
import { pricingOperations } from './pricing';
import { serviceRequestOperations } from './servicerequest';
import { workflowOperations } from './workflow';
import { documentOperations } from './document';
import { notificationOperations } from './notification';
import { providerOperations } from './provider';
import { ruleOperations } from './rules';
import { healthOperations } from './health';
import { medicalReportOperations } from './medicalreport';
import { inpatientOperations } from './inpatient';
import { claimOperations } from './claim';
import { accommodationOperations } from './accommodation';
import { randomId } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type SessionInfo = components['schemas']['SessionInfo'];
export type UserContext = components['schemas']['UserContext'];
export type TenantContext = components['schemas']['TenantContext'];
export type TenantSummary = components['schemas']['TenantSummary'];
export type Organization = components['schemas']['Organization'];
export type OrganizationSummary = components['schemas']['OrganizationSummary'];
export type OrganizationPage = components['schemas']['OrganizationPage'];
export type CreateOrganizationRequest = components['schemas']['CreateOrganizationRequest'];
export type UpdateOrganizationRequest = components['schemas']['UpdateOrganizationRequest'];
export type RelationshipRole = CreateOrganizationRequest['relationshipRole'];

/** Session and identity operations; none of them needs a tenant header. */
export function sessionOperations(client: KapsoraClient) {
  return {
    async get(): Promise<SessionInfo> {
      return (await unwrap(client.GET('/api/v1/session'))).data;
    },
    async login(username: string, password: string): Promise<SessionInfo> {
      return (await unwrap(client.POST('/api/v1/session/login', { body: { username, password } })))
        .data;
    },
    async logout(): Promise<void> {
      await unwrap(client.POST('/api/v1/session/logout'));
    },
    async switchTenant(tenantId: string): Promise<TenantContext> {
      return (await unwrap(client.POST('/api/v1/session/switch-tenant', { body: { tenantId } })))
        .data;
    },
    async stepUp(password: string): Promise<void> {
      await unwrap(client.POST('/api/v1/session/step-up', { body: { password } }));
    },
    async me(): Promise<UserContext> {
      return (await unwrap(client.GET('/api/v1/me'))).data;
    },
    async tenants(): Promise<TenantSummary[]> {
      return (await unwrap(client.GET('/api/v1/tenants'))).data.items;
    },
  };
}

/** Filters of the organization list. */
export interface OrganizationListQuery {
  cursor?: string;
  limit?: number;
  q?: string;
  role?: RelationshipRole;
}

/**
 * Organization operations. The tenant id is passed per call because the contract makes
 * X-Tenant-ID a required header and the server compares it with the session.
 */
export function organizationOperations(client: KapsoraClient) {
  return {
    async list(tenantId: string, query: OrganizationListQuery = {}): Promise<OrganizationPage> {
      const q: OrganizationListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.q) q.q = query.q;
      if (query.role) q.role = query.role;
      return (
        await unwrap(
          client.GET('/api/v1/organizations', {
            params: { header: { 'X-Tenant-ID': tenantId }, query: q },
          }),
        )
      ).data;
    },
    async get(tenantId: string, organizationId: string): Promise<Versioned<Organization>> {
      const r = await unwrap(
        client.GET('/api/v1/organizations/{organizationId}', {
          params: { header: { 'X-Tenant-ID': tenantId }, path: { organizationId } },
        }),
      );
      return versioned(r.data, r.response);
    },
    async create(
      tenantId: string,
      body: CreateOrganizationRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Organization>> {
      const r = await unwrap(
        client.POST('/api/v1/organizations', {
          params: { header: { 'X-Tenant-ID': tenantId, 'Idempotency-Key': idempotencyKey } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },
    async update(
      tenantId: string,
      organizationId: string,
      etag: string,
      patch: UpdateOrganizationRequest,
    ): Promise<Versioned<Organization>> {
      const r = await unwrap(
        client.PATCH('/api/v1/organizations/{organizationId}', {
          params: {
            header: { 'X-Tenant-ID': tenantId, 'If-Match': etag },
            path: { organizationId },
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

/** Everything the apps call, built once per app. */
export function createOperations(client: KapsoraClient) {
  return {
    session: sessionOperations(client),
    organizations: organizationOperations(client),
    people: peopleOperations(client),
    benefit: benefitOperations(client),
    entitlements: entitlementOperations(client),
    eligibility: eligibilityOperations(client),
    imports: memberImportOperations(client),
    catalog: catalogOperations(client),
    providers: providerOperations(client),
    contracts: contractOperations(client),
    rules: ruleOperations(client),
    pricing: pricingOperations(client),
    requests: serviceRequestOperations(client),
    worklist: workflowOperations(client),
    documents: documentOperations(client),
    notifications: notificationOperations(client),
    health: healthOperations(client),
    reports: medicalReportOperations(client),
    stays: inpatientOperations(client),
    claims: claimOperations(client),
    lodging: accommodationOperations(client),
  };
}

export type Operations = ReturnType<typeof createOperations>;
