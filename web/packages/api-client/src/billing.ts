import { randomId, type KapsoraClient } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type Invoice = components['schemas']['Invoice'];
export type InvoiceSummary = components['schemas']['InvoiceSummary'];
export type InvoicePage = components['schemas']['InvoicePage'];
export type InvoiceChain = components['schemas']['InvoiceChain'];
export type InvoiceStatus = components['schemas']['InvoiceStatus'];
export type InvoiceAllocation = components['schemas']['InvoiceAllocation'];
export type InvoiceAllocationInput = components['schemas']['InvoiceAllocationInput'];
export type CreateInvoice = components['schemas']['CreateInvoice'];
export type PatchInvoiceDraft = components['schemas']['PatchInvoiceDraft'];
export type PutInvoiceAllocations = components['schemas']['PutInvoiceAllocations'];
export type Batch = components['schemas']['Batch'];
export type BatchPage = components['schemas']['BatchPage'];
export type BatchInvoice = components['schemas']['BatchInvoice'];
export type BatchStatus = components['schemas']['BatchStatus'];
export type BatchDecision = components['schemas']['BatchDecision'];
export type BatchSummary = components['schemas']['BatchSummary'];
export type BatchSummaryRow = components['schemas']['BatchSummaryRow'];
export type CreateBatch = components['schemas']['CreateBatch'];
export type PutBatchInvoices = components['schemas']['PutBatchInvoices'];
export type ReviewBatchInvoice = components['schemas']['ReviewBatchInvoice'];
export type Settlement = components['schemas']['Settlement'];
export type SettlementPage = components['schemas']['SettlementPage'];
export type SettlementStatus = components['schemas']['SettlementStatus'];
export type SettlementRecovery = components['schemas']['SettlementRecovery'];
export type CancelSettlement = components['schemas']['CancelSettlement'];
export type PaymentRecord = components['schemas']['PaymentRecord'];
export type PaymentRecordList = components['schemas']['PaymentRecordList'];
export type PaymentRecordStatus = components['schemas']['PaymentRecordStatus'];
export type CreatePaymentRecord = components['schemas']['CreatePaymentRecord'];
export type Reimbursement = components['schemas']['Reimbursement'];
export type ReimbursementPage = components['schemas']['ReimbursementPage'];
export type ReimbursementStatus = components['schemas']['ReimbursementStatus'];
export type ReimbursementDecision = components['schemas']['ReimbursementDecision'];
export type CreateReimbursement = components['schemas']['CreateReimbursement'];
export type DecideReimbursement = components['schemas']['DecideReimbursement'];
export type RecordReimbursementPayment = components['schemas']['RecordReimbursementPayment'];
export type ProviderEarnings = components['schemas']['ProviderEarnings'];
export type ProviderEarningsCurrency = components['schemas']['ProviderEarningsCurrency'];
export type ClaimAdjustment = components['schemas']['ClaimAdjustment'];
export type ClaimAdjustmentType = components['schemas']['ClaimAdjustmentType'];
export type ClaimAdjustmentSource = components['schemas']['ClaimAdjustmentSource'];
export type ClaimAdjustmentResult = components['schemas']['ClaimAdjustmentResult'];
export type CreateClaimAdjustment = components['schemas']['CreateClaimAdjustment'];

export interface InvoiceListQuery {
  cursor?: string;
  limit?: number;
  providerOrganizationId?: string;
  status?: InvoiceStatus;
  fiscalYear?: number;
  batchId?: string;
  from?: string;
  to?: string;
}

export interface BatchListQuery {
  cursor?: string;
  limit?: number;
  providerOrganizationId?: string;
  payerOrganizationId?: string;
  status?: BatchStatus;
  domainCode?: string;
  currencyCode?: string;
  from?: string;
  to?: string;
}

export interface SettlementListQuery {
  cursor?: string;
  limit?: number;
  providerOrganizationId?: string;
  payerOrganizationId?: string;
  status?: SettlementStatus;
  batchId?: string;
  currencyCode?: string;
  dueFrom?: string;
  dueTo?: string;
}

export interface ReimbursementListQuery {
  cursor?: string;
  limit?: number;
  personId?: string;
  status?: ReimbursementStatus;
  from?: string;
  to?: string;
}

export interface EarningsQuery {
  from?: string;
  to?: string;
  currency?: string;
}

/**
 * Money leaving the plan: the provider's earnings and the invoice it enters against them,
 * the batch the payer decides, the settlement that pays it, the payment records that close
 * it, and the member's reimbursement.
 *
 * Every amount is an exact decimal string the server produced. A screen shows an
 * arithmetic line — approved − withheld = payable; submitted = approved + cut + returned +
 * rejected — it never performs it. The IBAN is sent once on a create and comes back only
 * as a masked tail.
 */
export function billingOperations(client: KapsoraClient) {
  const read = (tenantId: string) => ({ 'X-Tenant-ID': tenantId });
  const create = (tenantId: string, idempotencyKey: string) => ({
    'X-Tenant-ID': tenantId,
    'Idempotency-Key': idempotencyKey,
  });
  const command = (tenantId: string, etag: string, idempotencyKey: string) => ({
    'X-Tenant-ID': tenantId,
    'If-Match': etag,
    'Idempotency-Key': idempotencyKey,
  });

  return {
    // --- Earnings and adjustments ---------------------------------------------------------

    async earnings(
      tenantId: string,
      providerId: string,
      query: EarningsQuery = {},
    ): Promise<ProviderEarnings> {
      const q: EarningsQuery = {};
      if (query.from) q.from = query.from;
      if (query.to) q.to = query.to;
      if (query.currency) q.currency = query.currency;
      return (
        await unwrap(
          client.GET('/api/v1/providers/{providerId}/earnings', {
            params: { header: read(tenantId), path: { providerId }, query: q },
          }),
        )
      ).data;
    },

    async listAdjustments(tenantId: string, claimId: string): Promise<ClaimAdjustment[]> {
      return (
        await unwrap(
          client.GET('/api/v1/claims/{claimId}/adjustments', {
            params: { header: read(tenantId), path: { claimId } },
          }),
        )
      ).data.items;
    },

    async createAdjustment(
      tenantId: string,
      claimId: string,
      body: CreateClaimAdjustment,
      idempotencyKey: string = randomId(),
    ): Promise<ClaimAdjustmentResult> {
      return (
        await unwrap(
          client.POST('/api/v1/claims/{claimId}/adjustments', {
            params: { header: create(tenantId, idempotencyKey), path: { claimId } },
            body,
          }),
        )
      ).data;
    },

    // --- Invoices ---------------------------------------------------------------------------

    async listInvoices(tenantId: string, query: InvoiceListQuery = {}): Promise<InvoicePage> {
      const q: InvoiceListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.providerOrganizationId) q.providerOrganizationId = query.providerOrganizationId;
      if (query.status) q.status = query.status;
      if (query.fiscalYear) q.fiscalYear = query.fiscalYear;
      if (query.batchId) q.batchId = query.batchId;
      if (query.from) q.from = query.from;
      if (query.to) q.to = query.to;
      return (
        await unwrap(
          client.GET('/api/v1/invoices', { params: { header: read(tenantId), query: q } }),
        )
      ).data;
    },

    async createInvoice(
      tenantId: string,
      body: CreateInvoice,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Invoice>> {
      const r = await unwrap(
        client.POST('/api/v1/invoices', {
          params: { header: create(tenantId, idempotencyKey) },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getInvoice(tenantId: string, invoiceId: string): Promise<Versioned<Invoice>> {
      const r = await unwrap(
        client.GET('/api/v1/invoices/{invoiceId}', {
          params: { header: read(tenantId), path: { invoiceId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchInvoiceDraft(
      tenantId: string,
      invoiceId: string,
      etag: string,
      body: PatchInvoiceDraft,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Invoice>> {
      const r = await unwrap(
        client.PATCH('/api/v1/invoices/{invoiceId}', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { invoiceId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async putInvoiceAllocations(
      tenantId: string,
      invoiceId: string,
      etag: string,
      body: PutInvoiceAllocations,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Invoice>> {
      const r = await unwrap(
        client.PUT('/api/v1/invoices/{invoiceId}/allocations', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { invoiceId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async submitInvoice(
      tenantId: string,
      invoiceId: string,
      etag: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Invoice>> {
      const r = await unwrap(
        client.POST('/api/v1/invoices/{invoiceId}/submit', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { invoiceId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async cancelInvoice(
      tenantId: string,
      invoiceId: string,
      etag: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Invoice>> {
      const r = await unwrap(
        client.POST('/api/v1/invoices/{invoiceId}/cancel', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { invoiceId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /** The supersede chain an invoice belongs to, oldest first. */
    async invoiceChain(tenantId: string, invoiceId: string): Promise<InvoiceSummary[]> {
      return (
        await unwrap(
          client.GET('/api/v1/invoices/{invoiceId}/versions', {
            params: { header: read(tenantId), path: { invoiceId } },
          }),
        )
      ).data.items;
    },

    // --- Batches ----------------------------------------------------------------------------

    async listBatches(tenantId: string, query: BatchListQuery = {}): Promise<BatchPage> {
      const q: BatchListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.providerOrganizationId) q.providerOrganizationId = query.providerOrganizationId;
      if (query.payerOrganizationId) q.payerOrganizationId = query.payerOrganizationId;
      if (query.status) q.status = query.status;
      if (query.domainCode) q.domainCode = query.domainCode;
      if (query.currencyCode) q.currencyCode = query.currencyCode;
      if (query.from) q.from = query.from;
      if (query.to) q.to = query.to;
      return (
        await unwrap(
          client.GET('/api/v1/batches', { params: { header: read(tenantId), query: q } }),
        )
      ).data;
    },

    async createBatch(
      tenantId: string,
      body: CreateBatch,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Batch>> {
      const r = await unwrap(
        client.POST('/api/v1/batches', {
          params: { header: create(tenantId, idempotencyKey) },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getBatch(tenantId: string, batchId: string): Promise<Versioned<Batch>> {
      const r = await unwrap(
        client.GET('/api/v1/batches/{batchId}', {
          params: { header: read(tenantId), path: { batchId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async getBatchSummary(tenantId: string, batchId: string): Promise<BatchSummary> {
      return (
        await unwrap(
          client.GET('/api/v1/batches/{batchId}/summary', {
            params: { header: read(tenantId), path: { batchId } },
          }),
        )
      ).data;
    },

    async putBatchInvoices(
      tenantId: string,
      batchId: string,
      etag: string,
      body: PutBatchInvoices,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Batch>> {
      const r = await unwrap(
        client.PUT('/api/v1/batches/{batchId}/invoices', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { batchId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async submitBatch(
      tenantId: string,
      batchId: string,
      etag: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Batch>> {
      const r = await unwrap(
        client.POST('/api/v1/batches/{batchId}/submit', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { batchId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /** One decision on one invoice of a batch under review; the last one stands. */
    async reviewBatchInvoice(
      tenantId: string,
      batchId: string,
      invoiceId: string,
      etag: string,
      body: ReviewBatchInvoice,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Batch>> {
      const r = await unwrap(
        client.POST('/api/v1/batches/{batchId}/invoices/{invoiceId}/review', {
          params: {
            header: command(tenantId, etag, idempotencyKey),
            path: { batchId, invoiceId },
          },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async decideBatch(
      tenantId: string,
      batchId: string,
      etag: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Batch>> {
      const r = await unwrap(
        client.POST('/api/v1/batches/{batchId}/decide', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { batchId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    // --- Settlements and payment records ---------------------------------------------------

    async listSettlements(
      tenantId: string,
      query: SettlementListQuery = {},
    ): Promise<SettlementPage> {
      const q: SettlementListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.providerOrganizationId) q.providerOrganizationId = query.providerOrganizationId;
      if (query.payerOrganizationId) q.payerOrganizationId = query.payerOrganizationId;
      if (query.status) q.status = query.status;
      if (query.batchId) q.batchId = query.batchId;
      if (query.currencyCode) q.currencyCode = query.currencyCode;
      if (query.dueFrom) q.dueFrom = query.dueFrom;
      if (query.dueTo) q.dueTo = query.dueTo;
      return (
        await unwrap(
          client.GET('/api/v1/settlements', { params: { header: read(tenantId), query: q } }),
        )
      ).data;
    },

    async getSettlement(tenantId: string, settlementId: string): Promise<Versioned<Settlement>> {
      const r = await unwrap(
        client.GET('/api/v1/settlements/{settlementId}', {
          params: { header: read(tenantId), path: { settlementId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async approveSettlement(
      tenantId: string,
      settlementId: string,
      etag: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Settlement>> {
      const r = await unwrap(
        client.POST('/api/v1/settlements/{settlementId}/approve', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { settlementId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async cancelSettlement(
      tenantId: string,
      settlementId: string,
      etag: string,
      body: CancelSettlement,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Settlement>> {
      const r = await unwrap(
        client.POST('/api/v1/settlements/{settlementId}/cancel', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { settlementId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async listPaymentRecords(tenantId: string, settlementId: string): Promise<PaymentRecord[]> {
      return (
        await unwrap(
          client.GET('/api/v1/settlements/{settlementId}/payment-records', {
            params: { header: read(tenantId), path: { settlementId } },
          }),
        )
      ).data.items;
    },

    /** A payment as finance recorded it; the settlement comes back with its new paid amount. */
    async createPaymentRecord(
      tenantId: string,
      settlementId: string,
      body: CreatePaymentRecord,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Settlement>> {
      const r = await unwrap(
        client.POST('/api/v1/settlements/{settlementId}/payment-records', {
          params: { header: create(tenantId, idempotencyKey), path: { settlementId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    // --- Reimbursements ---------------------------------------------------------------------

    async listReimbursements(
      tenantId: string,
      query: ReimbursementListQuery = {},
    ): Promise<ReimbursementPage> {
      const q: ReimbursementListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.personId) q.personId = query.personId;
      if (query.status) q.status = query.status;
      if (query.from) q.from = query.from;
      if (query.to) q.to = query.to;
      return (
        await unwrap(
          client.GET('/api/v1/reimbursements', {
            params: { header: read(tenantId), query: q },
          }),
        )
      ).data;
    },

    /** The member's own, resolved through the PERSON scope. */
    async myReimbursements(
      tenantId: string,
      status?: ReimbursementStatus,
    ): Promise<ReimbursementPage> {
      return (
        await unwrap(
          client.GET('/api/v1/me/reimbursements', {
            params: {
              header: read(tenantId),
              query: status ? { status, limit: 50 } : { limit: 50 },
            },
          }),
        )
      ).data;
    },

    /** The IBAN travels once in this body and comes back only as a masked tail. */
    async createReimbursement(
      tenantId: string,
      body: CreateReimbursement,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Reimbursement>> {
      const r = await unwrap(
        client.POST('/api/v1/reimbursements', {
          params: { header: create(tenantId, idempotencyKey) },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getReimbursement(
      tenantId: string,
      reimbursementId: string,
    ): Promise<Versioned<Reimbursement>> {
      const r = await unwrap(
        client.GET('/api/v1/reimbursements/{reimbursementId}', {
          params: { header: read(tenantId), path: { reimbursementId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async submitReimbursement(
      tenantId: string,
      reimbursementId: string,
      etag: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Reimbursement>> {
      const r = await unwrap(
        client.POST('/api/v1/reimbursements/{reimbursementId}/submit', {
          params: {
            header: command(tenantId, etag, idempotencyKey),
            path: { reimbursementId },
          },
        }),
      );
      return versioned(r.data, r.response);
    },

    async decideReimbursement(
      tenantId: string,
      reimbursementId: string,
      etag: string,
      body: DecideReimbursement,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Reimbursement>> {
      const r = await unwrap(
        client.POST('/api/v1/reimbursements/{reimbursementId}/decide', {
          params: {
            header: command(tenantId, etag, idempotencyKey),
            path: { reimbursementId },
          },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async recordReimbursementPayment(
      tenantId: string,
      reimbursementId: string,
      etag: string,
      body: RecordReimbursementPayment,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Reimbursement>> {
      const r = await unwrap(
        client.POST('/api/v1/reimbursements/{reimbursementId}/payment', {
          params: {
            header: command(tenantId, etag, idempotencyKey),
            path: { reimbursementId },
          },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },
  };
}
