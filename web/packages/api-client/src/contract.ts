import type { KapsoraClient } from './client';
import { randomId } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type Contract = components['schemas']['Contract'];
export type ContractPage = components['schemas']['ContractPage'];
export type ContractStatus = components['schemas']['ContractStatus'];
export type CreateContractRequest = components['schemas']['CreateContractRequest'];
export type UpdateContractRequest = components['schemas']['UpdateContractRequest'];
export type ContractVersion = components['schemas']['ContractVersion'];
export type ContractVersionSummary = components['schemas']['ContractVersionSummary'];
export type ContractVersionStatus = components['schemas']['ContractVersionStatus'];
export type CreateContractVersionRequest = components['schemas']['CreateContractVersionRequest'];
export type UpdateContractVersionRequest = components['schemas']['UpdateContractVersionRequest'];
export type PriceList = components['schemas']['PriceList'];
export type PriceListInput = components['schemas']['PriceListInput'];
export type PriceItem = components['schemas']['PriceItem'];
export type PriceItemInput = components['schemas']['PriceItemInput'];
export type PriceItemPage = components['schemas']['PriceItemPage'];
export type PricingMethod = components['schemas']['PricingMethod'];
export type MemberShareMethod = components['schemas']['MemberShareMethod'];
export type PackageDefinition = components['schemas']['PackageDefinition'];
export type PackageDefinitionInput = components['schemas']['PackageDefinitionInput'];
export type PackageInclusionRule = components['schemas']['PackageInclusionRule'];
export type ProviderQuota = components['schemas']['ProviderQuota'];
export type ProviderQuotaInput = components['schemas']['ProviderQuotaInput'];
export type QuotaPeriodType = components['schemas']['QuotaPeriodType'];
export type PaymentTerm = components['schemas']['PaymentTerm'];
export type PutPaymentTermRequest = components['schemas']['PutPaymentTermRequest'];
export type SettlementMethod = components['schemas']['SettlementMethod'];
export type TaxBehaviour = components['schemas']['TaxBehaviour'];
export type ResolvePriceRequest = components['schemas']['ResolvePriceRequest'];
export type ResolvePriceResult = components['schemas']['ResolvePriceResult'];
export type ResolvedPrice = components['schemas']['ResolvedPrice'];
export type ScoredPriceCandidate = components['schemas']['ScoredPriceCandidate'];
export type PriceMatchTarget = components['schemas']['PriceMatchTarget'];
export type ReviewComment = components['schemas']['ReviewComment'];
export type ReasonCommand = components['schemas']['ReasonCommand'];
export type ServiceDomain = components['schemas']['ServiceDomain'];

/** Filters of the contract list. */
export interface ContractListQuery {
  cursor?: string;
  limit?: number;
  q?: string;
  providerProfileId?: string;
  payerOrganizationId?: string;
  domainCode?: ServiceDomain;
  status?: ContractStatus;
}

/** Paging of the price item list of one price list. */
export interface PriceItemListQuery {
  cursor?: string;
  limit?: number;
}

/**
 * Contracts, their versions and the price sheet of a version.
 *
 * Every money value in these shapes is an exact decimal string (`"2500.000000"`) and never
 * a JSON number: a tariff row is what somebody is invoiced. Format these strings, never
 * parse them. Publishing is maker-checker — the submitter cannot be the publisher and the
 * publisher needs a recent step-up — and anything but a DRAFT version is frozen.
 */
export function contractOperations(client: KapsoraClient) {
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
    async list(tenantId: string, query: ContractListQuery = {}): Promise<ContractPage> {
      const q: ContractListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.q) q.q = query.q;
      if (query.providerProfileId) q.providerProfileId = query.providerProfileId;
      if (query.payerOrganizationId) q.payerOrganizationId = query.payerOrganizationId;
      if (query.domainCode) q.domainCode = query.domainCode;
      if (query.status) q.status = query.status;
      return (
        await unwrap(
          client.GET('/api/v1/contracts', { params: { header: header(tenantId), query: q } }),
        )
      ).data;
    },

    async create(
      tenantId: string,
      body: CreateContractRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Contract>> {
      const r = await unwrap(
        client.POST('/api/v1/contracts', {
          params: { header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async get(tenantId: string, contractId: string): Promise<Versioned<Contract>> {
      const r = await unwrap(
        client.GET('/api/v1/contracts/{contractId}', {
          params: { header: header(tenantId), path: { contractId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patch(
      tenantId: string,
      contractId: string,
      etag: string,
      patch: UpdateContractRequest,
    ): Promise<Versioned<Contract>> {
      const r = await unwrap(
        client.PATCH('/api/v1/contracts/{contractId}', {
          params: { header: withEtag(tenantId, etag), path: { contractId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Drafts are omitted for a caller holding only `contract.read`. */
    async listVersions(tenantId: string, contractId: string): Promise<ContractVersionSummary[]> {
      return (
        await unwrap(
          client.GET('/api/v1/contracts/{contractId}/versions', {
            params: { header: header(tenantId), path: { contractId } },
          }),
        )
      ).data.items;
    },

    async createVersion(
      tenantId: string,
      contractId: string,
      body: CreateContractVersionRequest = {},
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<ContractVersion>> {
      const r = await unwrap(
        client.POST('/api/v1/contracts/{contractId}/versions', {
          params: {
            header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey },
            path: { contractId },
          },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getVersion(
      tenantId: string,
      contractVersionId: string,
    ): Promise<Versioned<ContractVersion>> {
      const r = await unwrap(
        client.GET('/api/v1/contract-versions/{contractVersionId}', {
          params: { header: header(tenantId), path: { contractVersionId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchVersion(
      tenantId: string,
      contractVersionId: string,
      etag: string,
      patch: UpdateContractVersionRequest,
    ): Promise<Versioned<ContractVersion>> {
      const r = await unwrap(
        client.PATCH('/api/v1/contract-versions/{contractVersionId}', {
          params: { header: withEtag(tenantId, etag), path: { contractVersionId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    async submitVersion(
      tenantId: string,
      contractVersionId: string,
      etag: string,
      body: ReviewComment = {},
    ): Promise<Versioned<ContractVersion>> {
      const r = await unwrap(
        client.POST('/api/v1/contract-versions/{contractVersionId}/submit', {
          params: { header: withEtag(tenantId, etag), path: { contractVersionId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Needs a recent step-up and a different actor than the submitter. */
    async publishVersion(
      tenantId: string,
      contractVersionId: string,
      etag: string,
      body: ReviewComment = {},
    ): Promise<Versioned<ContractVersion>> {
      const r = await unwrap(
        client.POST('/api/v1/contract-versions/{contractVersionId}/publish', {
          params: { header: withEtag(tenantId, etag), path: { contractVersionId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Needs a recent step-up; the version stays readable for what was priced from it. */
    async retireVersion(
      tenantId: string,
      contractVersionId: string,
      etag: string,
      body: ReasonCommand,
    ): Promise<Versioned<ContractVersion>> {
      const r = await unwrap(
        client.POST('/api/v1/contract-versions/{contractVersionId}/retire', {
          params: { header: withEtag(tenantId, etag), path: { contractVersionId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Replaces the whole price list set; If-Match carries the version's ETag. */
    async replacePriceLists(
      tenantId: string,
      contractVersionId: string,
      etag: string,
      items: PriceListInput[],
    ): Promise<Versioned<PriceList[]>> {
      const r = await unwrap(
        client.PUT('/api/v1/contract-versions/{contractVersionId}/price-lists', {
          params: { header: withEtag(tenantId, etag), path: { contractVersionId } },
          body: { items },
        }),
      );
      return versioned(r.data.items, r.response);
    },

    /** The ETag is the owning price list's, which a replacement expects in If-Match. */
    async listPriceItems(
      tenantId: string,
      priceListId: string,
      query: PriceItemListQuery = {},
    ): Promise<Versioned<PriceItemPage>> {
      const q: PriceItemListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      const r = await unwrap(
        client.GET('/api/v1/price-lists/{priceListId}/items', {
          params: { header: header(tenantId), path: { priceListId }, query: q },
        }),
      );
      return versioned(r.data, r.response);
    },

    async replacePriceItems(
      tenantId: string,
      priceListId: string,
      etag: string,
      items: PriceItemInput[],
    ): Promise<Versioned<PriceItem[]>> {
      const r = await unwrap(
        client.PUT('/api/v1/price-lists/{priceListId}/items', {
          params: { header: withEtag(tenantId, etag), path: { priceListId } },
          body: { items },
        }),
      );
      return versioned(r.data.items, r.response);
    },

    async listPackageDefinitions(
      tenantId: string,
      contractVersionId: string,
    ): Promise<Versioned<PackageDefinition[]>> {
      const r = await unwrap(
        client.GET('/api/v1/contract-versions/{contractVersionId}/package-definitions', {
          params: { header: header(tenantId), path: { contractVersionId } },
        }),
      );
      return versioned(r.data.items, r.response);
    },

    async replacePackageDefinitions(
      tenantId: string,
      contractVersionId: string,
      etag: string,
      items: PackageDefinitionInput[],
    ): Promise<Versioned<PackageDefinition[]>> {
      const r = await unwrap(
        client.PUT('/api/v1/contract-versions/{contractVersionId}/package-definitions', {
          params: { header: withEtag(tenantId, etag), path: { contractVersionId } },
          body: { items },
        }),
      );
      return versioned(r.data.items, r.response);
    },

    async listProviderQuotas(
      tenantId: string,
      contractVersionId: string,
    ): Promise<Versioned<ProviderQuota[]>> {
      const r = await unwrap(
        client.GET('/api/v1/contract-versions/{contractVersionId}/provider-quotas', {
          params: { header: header(tenantId), path: { contractVersionId } },
        }),
      );
      return versioned(r.data.items, r.response);
    },

    async replaceProviderQuotas(
      tenantId: string,
      contractVersionId: string,
      etag: string,
      items: ProviderQuotaInput[],
    ): Promise<Versioned<ProviderQuota[]>> {
      const r = await unwrap(
        client.PUT('/api/v1/contract-versions/{contractVersionId}/provider-quotas', {
          params: { header: withEtag(tenantId, etag), path: { contractVersionId } },
          body: { items },
        }),
      );
      return versioned(r.data.items, r.response);
    },

    /** A version with no payment term answers 404: "not agreed yet", not "agreed as zero". */
    async getPaymentTerm(
      tenantId: string,
      contractVersionId: string,
    ): Promise<Versioned<PaymentTerm>> {
      const r = await unwrap(
        client.GET('/api/v1/contract-versions/{contractVersionId}/payment-term', {
          params: { header: header(tenantId), path: { contractVersionId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async putPaymentTerm(
      tenantId: string,
      contractVersionId: string,
      etag: string,
      body: PutPaymentTermRequest,
    ): Promise<Versioned<PaymentTerm>> {
      const r = await unwrap(
        client.PUT('/api/v1/contract-versions/{contractVersionId}/payment-term', {
          params: { header: withEtag(tenantId, etag), path: { contractVersionId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Which contracted price applies. Changes no state. Two equally specific candidates
     * answer REVIEW_REQUIRED with reason PRICE_AMBIGUOUS and never a winner.
     */
    async resolvePrice(tenantId: string, body: ResolvePriceRequest): Promise<ResolvePriceResult> {
      return (
        await unwrap(
          client.POST('/api/v1/prices:resolve', { params: { header: header(tenantId) }, body }),
        )
      ).data;
    },
  };
}
