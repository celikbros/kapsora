/**
 * MSW handlers for the invoice a provider raised elsewhere, and its allocation to claims
 * (WP-I7-02).
 *
 * The mock is a test double of the Go server and a divergence in either direction is a bug.
 * Five things here are transcriptions rather than re-implementations, because they are the five
 * a screen would be built wrongly against:
 *
 *   - **the header adds up**: `lineExtensionAmount + taxAmount` must equal `payableAmount`
 *     exactly. There is no tolerance on the header — the tolerance is about the allocations —
 *     and a document whose own halves do not make its total is one somebody mistyped;
 *   - **the ceiling**: an allocation may never exceed what the payer approved for the claim, and
 *     may be less, because a provider collecting part of a claim is ordinary. The approved total
 *     is `claimTotals` from the claim handlers — the same function the readiness endpoint and
 *     the earnings view use, because a second implementation would be a second answer;
 *   - **one claim, one live invoice**: a claim already sitting on a live document is refused by
 *     name, with the invoice it is on;
 *   - **the tolerance**: the sum of the allocations equals the payable amount within the
 *     tenant's `billing.allocation_tolerance`, and the refusal carries both figures, the
 *     difference and the tolerance that was applied;
 *   - **the freeze**: once an invoice has left DRAFT its links and its figures never change. A
 *     correction is a new invoice in a supersede chain, and the old one is cancelled when the
 *     correction is *submitted*, never when it is drafted.
 *
 * As on the server, the projection is applied to the stored row before it becomes a body: the
 * claim's line description is possibly clinical (v1.2 §2.11), so a caller without
 * `health.clinical.read` — the sponsor's HR user, and the payer's finance user too — never
 * receives it.
 *
 * Every figure is an exact decimal computed in integer micro-units and rendered in the
 * canonical trimmed form the server's `trim_scale` produces. No amount here passes through a
 * binary float.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import { claimTotals } from './claim-handlers';
import {
  fromMicros,
  toMicros,
  type StoredClaim,
  type StoredInvoice,
  type StoredInvoiceClaim,
} from './data';
import type { MockApi, MockSession } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  hasPermission,
  organizationScope,
  parseLimit,
  pathParam,
  problem,
  readJson,
  requireIdempotencyKey,
  requireIfMatch,
  requireMergePatch,
  validationFailed,
  wait,
  withinScope,
  type FieldError,
  type Schemas,
} from './handlers';
import { NO_STORE } from './health-handlers';

const PERMISSION_READ = 'invoice.read';
const PERMISSION_MANAGE = 'invoice.manage';
const PERMISSION_CLINICAL_READ = 'health.clinical.read';

/** billing.invoice.status, as migration 000044 writes it. */
const INVOICE_STATUSES = new Set<string>([
  'DRAFT',
  'SUBMITTED',
  'IN_BATCH',
  'RETURNED',
  'APPROVED',
  'PARTIALLY_APPROVED',
  'REJECTED',
  'SETTLED',
  'CANCELLED',
]);

/** The two claim statuses an allocation may name: the two the payer has answered. */
const INVOICEABLE_CLAIM_STATUSES = new Set<string>(['APPROVED', 'PARTIALLY_APPROVED']);

/** The statuses a correction may replace. A SUBMITTED invoice is in front of a reviewer. */
const SUPERSEDABLE = new Set<string>(['RETURNED', 'REJECTED']);

const DOMAIN_CODES = new Set<string>([
  'GENERIC',
  'HEALTH',
  'ACCOMMODATION',
  'ASSISTANCE',
  'EDUCATION',
  'SPORT',
  'TRANSPORT',
  'CARE',
  'OTHER',
]);

const INVOICE_NUMBER = /^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$/;
const CURRENCY = /^[A-Z]{3}$/;
const DECIMAL = /^-?[0-9]{1,14}(\.[0-9]{1,6})?$/;

/** The documented defaults of `billing.allocation_tolerance` and `billing.invoice_requires_image`. */
const DEFAULT_TOLERANCE = '0.01';
const DEFAULT_REQUIRES_IMAGE = true;

/**
 * Renders micro-units in the canonical trimmed decimal the server's `trim_scale` produces, so
 * the mock and the server agree digit for digit.
 */
function amount(micros: bigint): string {
  const text = fromMicros(micros);
  if (!text.includes('.')) return text;
  const trimmed = text.replace(/0+$/, '').replace(/\.$/, '');
  return trimmed === '' || trimmed === '-' ? '0' : trimmed;
}

export function invoiceHandlers(api: MockApi): HttpHandler[] {
  const world = () => api.world;

  const invoiceOf = (tenantId: string, id: string): StoredInvoice | undefined =>
    world().invoices.find((i) => i.tenantId === tenantId && i.id === id);

  const allocationsOf = (invoiceId: string): StoredInvoiceClaim[] =>
    world()
      .invoiceAllocations.filter((a) => a.invoiceId === invoiceId)
      .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id));

  const claimOf = (tenantId: string, id: string): StoredClaim | undefined =>
    world().claims.find((c) => c.tenantId === tenantId && c.id === id);

  /** The invoice a claim already sits on, or null. */
  const liveInvoiceOf = (tenantId: string, claimId: string): string | null =>
    world().invoiceAllocations.find(
      (a) => a.tenantId === tenantId && a.claimId === claimId && a.active,
    )?.invoiceId ?? null;

  const providerNameOf = (tenantId: string, providerId: string): string | undefined => {
    const relationship = world().relationships.find(
      (r) => r.id === providerId && r.tenantId === tenantId,
    );
    if (!relationship) return undefined;
    return world().organizations.get(relationship.organizationId)?.displayName;
  };

  /**
   * The provider's tax identity, answered as a boolean. The number itself is never read: on the
   * server it is envelope-encrypted and its blind index is a hash, and neither belongs in a
   * response — so the mock does not carry a path that could return one either.
   */
  const providerHasTaxIdentity = (tenantId: string, providerId: string): boolean => {
    const relationship = world().relationships.find(
      (r) => r.id === providerId && r.tenantId === tenantId,
    );
    if (!relationship) return false;
    return Boolean(world().organizations.get(relationship.organizationId)?.taxNumber);
  };

  const isProvider = (tenantId: string, providerId: string): boolean =>
    world().relationships.some(
      (r) =>
        r.id === providerId &&
        r.tenantId === tenantId &&
        r.relationshipRole === 'PROVIDER' &&
        (r.relationshipStatus === 'ACTIVE' || r.relationshipStatus === 'PENDING'),
    );

  /**
   * The tenant's tolerance and image policy. The mock has no tenant-settings surface, so the
   * documented defaults are what it answers — which is the case every screen actually meets,
   * and the one the server's `settings.Defaults()` returns.
   */
  const tolerance = (): bigint => toMicros(DEFAULT_TOLERANCE);
  const requiresImage = (): boolean => DEFAULT_REQUIRES_IMAGE;

  /** The sum of the *active* allocations, which is what the submit gate compares. */
  const allocationTotal = (invoiceId: string): bigint => {
    let total = 0n;
    for (const row of allocationsOf(invoiceId)) {
      if (row.active) total += toMicros(row.allocatedAmount);
    }
    return total;
  };

  /**
   * The projection decision, applied on the record before anything serialises it. It is the
   * same rule WP-I5-01 wrote down, asked here about STANDARD sensitivity: an invoice hangs off
   * no episode of care, and what is clinical about it is the line description of the claims it
   * covers.
   */
  const projectionOf = (session: MockSession, tenantId: string): Schemas['HealthProjection'] =>
    hasPermission(api, session, tenantId, PERMISSION_CLINICAL_READ) ? 'CLINICAL' : 'FINANCIAL';

  /** The provider's own words on a claim's lines, joined — possibly clinical, so projected. */
  const claimDescription = (claim: StoredClaim): string | null => {
    const version = world().claimVersions.find(
      (v) => v.claimId === claim.id && v.versionNo === claim.currentVersionNo,
    );
    if (!version) return null;
    const parts = world()
      .claimLines.filter((l) => l.versionId === version.id && l.description)
      .sort((a, b) => a.lineNo - b.lineNo)
      .map((l) => l.description!);
    return parts.length === 0 ? null : parts.join(' · ');
  };

  const allocationView = (
    row: StoredInvoiceClaim,
    tenantId: string,
    projection: Schemas['HealthProjection'],
  ): Schemas['InvoiceAllocation'] => {
    const claim = claimOf(tenantId, row.claimId);
    const view: Schemas['InvoiceAllocation'] = {
      claimId: row.claimId,
      claimReference: claim?.reference ?? '',
      claimVersionNo: row.claimVersionNo,
      allocatedAmount: amount(toMicros(row.allocatedAmount)),
      currencyCode: row.currencyCode,
      approvedTotal: amount(claimTotals(world(), row.claimId).approved),
      claimStatus: claim?.status ?? 'DRAFT',
      active: row.active,
    };
    // Cleared rather than not rendered: a record that no longer carries the answer cannot leak
    // it, however it is later serialised.
    if (projection === 'CLINICAL' && claim) {
      const description = claimDescription(claim);
      if (description) view.claimDescription = description;
    }
    return view;
  };

  const invoiceView = (
    row: StoredInvoice,
    projection: Schemas['HealthProjection'],
  ): Schemas['Invoice'] => {
    const total = allocationTotal(row.id);
    const payable = toMicros(row.payableAmount);
    return {
      id: row.id,
      providerOrganizationId: row.providerOrganizationId,
      ...(providerNameOf(row.tenantId, row.providerOrganizationId)
        ? { providerName: providerNameOf(row.tenantId, row.providerOrganizationId)! }
        : {}),
      ...(row.payerOrganizationId ? { payerOrganizationId: row.payerOrganizationId } : {}),
      source: row.source,
      ...(row.edocumentId ? { edocumentId: row.edocumentId } : {}),
      invoiceNumber: row.invoiceNumber,
      invoiceDate: row.invoiceDate,
      fiscalYear: row.fiscalYear,
      currencyCode: row.currencyCode,
      lineExtensionAmount: amount(toMicros(row.lineExtensionAmount)),
      taxAmount: amount(toMicros(row.taxAmount)),
      payableAmount: amount(payable),
      ...(row.vatRate ? { vatRate: row.vatRate } : {}),
      domainCode: row.domainCode,
      status: row.status,
      ...(row.supersedesInvoiceId ? { supersedesInvoiceId: row.supersedesInvoiceId } : {}),
      ...(row.supersededByInvoiceId ? { supersededByInvoiceId: row.supersededByInvoiceId } : {}),
      ...(row.submittedAt ? { submittedAt: row.submittedAt } : {}),
      ...(row.documentId ? { documentId: row.documentId } : {}),
      ...(row.batchId ? { batchId: row.batchId } : {}),
      ...(row.notes ? { notes: row.notes } : {}),
      allocations: allocationsOf(row.id).map((a) => allocationView(a, row.tenantId, projection)),
      allocationTotal: amount(total),
      allocationDifference: amount(payable - total),
      projection,
      createdAt: row.createdAt,
      rowVersion: row.rowVersion,
    };
  };

  const summaryView = (row: StoredInvoice): Schemas['InvoiceSummary'] => ({
    id: row.id,
    providerOrganizationId: row.providerOrganizationId,
    ...(providerNameOf(row.tenantId, row.providerOrganizationId)
      ? { providerName: providerNameOf(row.tenantId, row.providerOrganizationId)! }
      : {}),
    ...(row.payerOrganizationId ? { payerOrganizationId: row.payerOrganizationId } : {}),
    source: row.source,
    invoiceNumber: row.invoiceNumber,
    invoiceDate: row.invoiceDate,
    fiscalYear: row.fiscalYear,
    currencyCode: row.currencyCode,
    lineExtensionAmount: amount(toMicros(row.lineExtensionAmount)),
    taxAmount: amount(toMicros(row.taxAmount)),
    payableAmount: amount(toMicros(row.payableAmount)),
    status: row.status,
    ...(row.supersedesInvoiceId ? { supersedesInvoiceId: row.supersedesInvoiceId } : {}),
    ...(row.supersededByInvoiceId ? { supersededByInvoiceId: row.supersededByInvoiceId } : {}),
    ...(row.submittedAt ? { submittedAt: row.submittedAt } : {}),
    ...(row.documentId ? { documentId: row.documentId } : {}),
    ...(row.batchId ? { batchId: row.batchId } : {}),
    allocationTotal: amount(allocationTotal(row.id)),
    allocationCount: allocationsOf(row.id).filter((a) => a.active).length,
    createdAt: row.createdAt,
    rowVersion: row.rowVersion,
  });

  /**
   * The header rules, applied to the row that *results* from a create or a patch — which is what
   * makes "the three amounts travel together" true: a caller sending a new tax amount alone
   * produces a header that no longer adds up, and is told which field.
   */
  function validateHeader(row: {
    invoiceNumber: string;
    invoiceDate: string;
    currencyCode: string;
    lineExtensionAmount: string;
    taxAmount: string;
    payableAmount: string;
    vatRate: string | null;
    domainCode: string;
  }): FieldError[] {
    const errors: FieldError[] = [];
    if (!INVOICE_NUMBER.test(row.invoiceNumber)) {
      errors.push({
        field: 'invoiceNumber',
        code: 'FORMAT',
        message: 'harf, rakam ve . _ / - dışında karakter içeremez; en fazla 64 karakter',
      });
    }
    if (!/^\d{4}-\d{2}-\d{2}$/.test(row.invoiceDate)) {
      errors.push({ field: 'invoiceDate', code: 'FORMAT', message: 'YYYY-MM-DD biçiminde olmalı' });
    }
    if (!CURRENCY.test(row.currencyCode)) {
      errors.push({
        field: 'currencyCode',
        code: 'FORMAT',
        message: 'üç harfli para birimi kodu olmalı',
      });
    }
    if (!DOMAIN_CODES.has(row.domainCode)) {
      errors.push({ field: 'domainCode', code: 'ENUM', message: 'tanımlı bir alan kodu olmalı' });
    }
    for (const field of ['lineExtensionAmount', 'taxAmount', 'payableAmount'] as const) {
      const value = row[field];
      if (!DECIMAL.test(value)) {
        errors.push({ field, code: 'FORMAT', message: 'kesin ondalık bir sayı olmalı' });
      } else if (toMicros(value) < 0n) {
        errors.push({ field, code: 'RANGE', message: 'negatif olamaz' });
      }
    }
    if (row.vatRate !== null) {
      if (!DECIMAL.test(row.vatRate)) {
        errors.push({ field: 'vatRate', code: 'FORMAT', message: 'kesin ondalık bir sayı olmalı' });
      } else if (toMicros(row.vatRate) < 0n || toMicros(row.vatRate) > toMicros('100')) {
        errors.push({ field: 'vatRate', code: 'RANGE', message: '0 ile 100 arasında olmalı' });
      }
    }
    if (errors.length === 0) {
      const sum = toMicros(row.lineExtensionAmount) + toMicros(row.taxAmount);
      if (sum !== toMicros(row.payableAmount)) {
        errors.push({
          field: 'payableAmount',
          code: 'SUM',
          message: 'mal/hizmet toplamı ile vergi toplamının tam olarak eşiti olmalı',
        });
      }
    }
    return errors;
  }

  /**
   * The uniqueness rule of v1.2 11.12, and who may reuse a number.
   *
   * A cancelled invoice's number is free to anybody. A returned one's is free only to the
   * invoice that supersedes it — section 2.2's "the same number is allowed only for the
   * superseded chain" — so this answers *which* invoice is holding the number rather than only
   * that somebody is.
   */
  const numberHeldBy = (row: StoredInvoice): StoredInvoice | undefined =>
    world().invoices.find(
      (other) =>
        other.id !== row.id &&
        other.tenantId === row.tenantId &&
        other.providerOrganizationId === row.providerOrganizationId &&
        other.fiscalYear === row.fiscalYear &&
        other.invoiceNumber === row.invoiceNumber &&
        other.status !== 'CANCELLED',
    );

  const numberTaken = (row: StoredInvoice): boolean => {
    const holder = numberHeldBy(row);
    return holder !== undefined && holder.id !== row.supersedesInvoiceId;
  };

  const numberTakenProblem = (): Response =>
    problem(
      api,
      409,
      'INVOICE_NUMBER_TAKEN',
      'Bu fatura numarası bu mali yılda zaten kullanılmış',
      {
        detail:
          'Bir fatura numarası sağlayıcının mali yılında tektir; iptal edilen fatura numarasını serbest bırakır.',
      },
    );

  const documentIsClean = (tenantId: string, documentId: string): boolean =>
    world().documents.some(
      (d) =>
        d.tenantId === tenantId &&
        d.id === documentId &&
        d.scanStatus === 'CLEAN' &&
        d.purgedAt === null,
    );

  /** Everything a write command establishes before it acts: the row, the scope and the ETag. */
  function guardWrite(
    request: Request,
    tenantId: string,
    session: MockSession,
    invoiceId: string,
  ): { row: StoredInvoice } | { error: Response } {
    const row = invoiceOf(tenantId, invoiceId);
    if (!row) return { error: problem(api, 404, 'INVOICE_NOT_FOUND', 'Fatura bulunamadı') };
    const scope = organizationScope(api, session, tenantId);
    if (scope !== null && !withinScope(scope, row.providerOrganizationId)) {
      // 404 rather than 403: that an invoice exists at all is somebody else's business.
      return { error: problem(api, 404, 'INVOICE_NOT_FOUND', 'Fatura bulunamadı') };
    }
    const expected = requireIfMatch(api, request);
    if (typeof expected !== 'number') return { error: expected };
    if (expected !== row.rowVersion) {
      return {
        error: problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti', {
          detail: 'Güncel sürümü alıp değişikliğinizi yeniden uygulayın.',
        }),
      };
    }
    return { row };
  }

  const frozen = (): Response =>
    problem(api, 409, 'INVOICE_FROZEN', 'Gönderilmiş fatura değiştirilemez', {
      detail:
        'Düzeltme için faturayı iptal edin ya da iade edilmesini isteyip yerine yeni fatura açın.',
    });

  /** Moves the links of a cancelled or returned invoice out of the way, as the trigger does. */
  const releaseLinks = (invoice: StoredInvoice): void => {
    const live = invoice.status !== 'CANCELLED' && invoice.status !== 'RETURNED';
    for (const row of allocationsOf(invoice.id)) row.active = live;
  };

  return [
    http.get(`${ANY}/api/v1/invoices`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');

      const status = url.searchParams.get('status');
      if (status !== null && !INVOICE_STATUSES.has(status)) {
        return validationFailed(api, [
          { field: 'status', code: 'ENUM', message: 'tanımlı bir fatura durumu olmalı' },
        ]);
      }
      const providerFilter = url.searchParams.get('providerOrganizationId');
      const fiscalYear = url.searchParams.get('fiscalYear');
      const from = url.searchParams.get('from');
      const to = url.searchParams.get('to');
      const batchId = url.searchParams.get('batchId');
      const scope = organizationScope(api, g.session, g.tenantId);

      const rows = world()
        .invoices.filter((row) => {
          if (row.tenantId !== g.tenantId) return false;
          if (scope !== null && !withinScope(scope, row.providerOrganizationId)) return false;
          if (providerFilter && row.providerOrganizationId !== providerFilter) return false;
          if (status && row.status !== status) return false;
          if (fiscalYear && row.fiscalYear !== Number(fiscalYear)) return false;
          if (from && row.invoiceDate < from) return false;
          if (to && row.invoiceDate > to) return false;
          if (batchId && row.batchId !== batchId) return false;
          return true;
        })
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      const page = rows.slice(offset, offset + limit);
      const body: Schemas['InvoicePage'] = {
        items: page.map(summaryView),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.post(`${ANY}/api/v1/invoices`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_MANAGE, true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const key = `${g.tenantId}:${request.headers.get('Idempotency-Key')!}`;
      const replay = api.replay(key);
      if (replay) {
        return HttpResponse.json(replay.body as Schemas['Invoice'], {
          status: replay.status,
          headers: replay.etag ? { ETag: replay.etag, ...NO_STORE } : NO_STORE,
        });
      }
      const body = await readJson<Schemas['CreateInvoice']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

      const scope = organizationScope(api, g.session, g.tenantId);
      if (scope !== null && !withinScope(scope, body.providerOrganizationId)) {
        return problem(api, 403, 'INVOICE_PROVIDER_SCOPE', 'Bu sağlayıcı adına işlem yapamazsınız');
      }
      const draft: StoredInvoice = {
        id: world().nextId(),
        tenantId: g.tenantId,
        providerOrganizationId: body.providerOrganizationId,
        payerOrganizationId: body.payerOrganizationId ?? null,
        source: 'MANUAL',
        edocumentId: null,
        invoiceNumber: body.invoiceNumber,
        invoiceDate: body.invoiceDate,
        fiscalYear: Number(String(body.invoiceDate).slice(0, 4)),
        currencyCode: body.currencyCode ?? 'TRY',
        lineExtensionAmount: body.lineExtensionAmount,
        taxAmount: body.taxAmount,
        payableAmount: body.payableAmount,
        vatRate: body.vatRate ?? null,
        domainCode: body.domainCode ?? 'GENERIC',
        status: 'DRAFT',
        supersedesInvoiceId: body.supersedesInvoiceId ?? null,
        supersededByInvoiceId: null,
        submittedAt: null,
        documentId: body.documentId ?? null,
        batchId: null,
        notes: body.notes ?? null,
        createdAt: new Date().toISOString(),
        rowVersion: 1,
      };
      const errors = validateHeader(draft);
      if (errors.length > 0) return validationFailed(api, errors);

      if (!isProvider(g.tenantId, draft.providerOrganizationId)) {
        return problem(
          api,
          422,
          'INVOICE_PROVIDER_UNKNOWN',
          "Bu kurum tenant'ın sağlayıcısı değil",
        );
      }
      if (!providerHasTaxIdentity(g.tenantId, draft.providerOrganizationId)) {
        return problem(
          api,
          422,
          'PROVIDER_TAX_ID_MISSING',
          'Sağlayıcının vergi kimliği tanımlı değil',
          {
            detail: 'Fatura kaydı için kurumun VKN bilgisi kurum dizininde tanımlanmalı.',
          },
        );
      }
      if (draft.documentId && !documentIsClean(g.tenantId, draft.documentId)) {
        return problem(
          api,
          422,
          'INVOICE_DOCUMENT_UNUSABLE',
          'Fatura görüntüsü kullanılabilir değil',
          { detail: "Belge bu tenant'a ait, taranmış ve temiz çıkmış olmalı." },
        );
      }
      if (numberTaken(draft)) return numberTakenProblem();

      let superseded: StoredInvoice | undefined;
      if (draft.supersedesInvoiceId) {
        superseded = invoiceOf(g.tenantId, draft.supersedesInvoiceId);
        if (!superseded) return problem(api, 404, 'INVOICE_NOT_FOUND', 'Fatura bulunamadı');
        if (superseded.providerOrganizationId !== draft.providerOrganizationId) {
          return validationFailed(api, [
            {
              field: 'supersedesInvoiceId',
              code: 'REFERENCE',
              message: 'düzeltilen fatura aynı sağlayıcıya ait olmalı',
            },
          ]);
        }
        if (!SUPERSEDABLE.has(superseded.status)) {
          return problem(
            api,
            409,
            'INVOICE_NOT_SUPERSEDABLE',
            'Bu fatura düzeltilebilir durumda değil',
            {
              detail:
                'Yalnızca iade edilmiş ya da reddedilmiş bir fatura yeni faturayla düzeltilir.',
            },
          );
        }
        // **One successor per invoice** (`uq_billing_invoice_supersedes` on the server). Two
        // corrections of one returned invoice would be two documents each claiming to replace
        // it, and whichever was submitted second would silently win the chain.
        const supersededId = superseded.id;
        const rival = world().invoices.find(
          (i) =>
            i.tenantId === g.tenantId &&
            i.supersedesInvoiceId === supersededId &&
            i.status !== 'CANCELLED',
        );
        if (rival) {
          return problem(
            api,
            409,
            'INVOICE_NOT_SUPERSEDABLE',
            'Bu fatura için zaten bir düzeltme açılmış',
            { detail: 'Bir faturanın aynı anda yalnızca bir düzeltmesi olur.' },
          );
        }
      }

      world().invoices.push(draft);
      if (draft.documentId) {
        world().documentLinks.push({
          tenantId: g.tenantId,
          id: world().nextId(),
          documentId: draft.documentId,
          aggregateType: 'INVOICE',
          aggregateId: draft.id,
          documentTypeCode: 'INVOICE',
          purpose: null,
          requiredPermission: PERMISSION_READ,
          createdBy: g.session.account.actorId,
          createdAt: draft.createdAt,
        });
      }
      // The correction starts with what the returned invoice covered, so a provider corrects a
      // figure rather than rebuilding the pick list. A claim that has meanwhile stopped being
      // invoiceable is left off rather than refusing the whole correction.
      if (superseded) {
        for (const row of allocationsOf(superseded.id)) {
          const claim = claimOf(g.tenantId, row.claimId);
          if (!claim || !INVOICEABLE_CLAIM_STATUSES.has(claim.status)) continue;
          if (liveInvoiceOf(g.tenantId, row.claimId)) continue;
          world().invoiceAllocations.push({
            id: world().nextId(),
            tenantId: g.tenantId,
            invoiceId: draft.id,
            claimId: row.claimId,
            claimVersionNo: claim.currentVersionNo,
            allocatedAmount: row.allocatedAmount,
            currencyCode: draft.currencyCode,
            claimStatusBefore: claim.status,
            active: true,
            createdAt: draft.createdAt,
          });
        }
      }

      const out = invoiceView(draft, projectionOf(g.session, g.tenantId));
      api.rememberIdempotent(key, 201, out, etagOf(draft.rowVersion));
      return HttpResponse.json(out, {
        status: 201,
        headers: { ETag: etagOf(draft.rowVersion), ...NO_STORE },
      });
    }),

    http.get(`${ANY}/api/v1/invoices/:invoiceId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      const row = invoiceOf(g.tenantId, pathParam(params, 'invoiceId'));
      if (!row) return problem(api, 404, 'INVOICE_NOT_FOUND', 'Fatura bulunamadı');
      const scope = organizationScope(api, g.session, g.tenantId);
      if (scope !== null && !withinScope(scope, row.providerOrganizationId)) {
        return problem(api, 404, 'INVOICE_NOT_FOUND', 'Fatura bulunamadı');
      }
      return HttpResponse.json(invoiceView(row, projectionOf(g.session, g.tenantId)), {
        headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
      });
    }),

    http.patch(`${ANY}/api/v1/invoices/:invoiceId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_MANAGE, true);
      if ('error' in g) return g.error;
      const wrongType = requireMergePatch(api, request);
      if (wrongType) return wrongType;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const guard = guardWrite(request, g.tenantId, g.session, pathParam(params, 'invoiceId'));
      if ('error' in guard) return guard.error;
      const { row } = guard;
      if (row.status !== 'DRAFT') return frozen();

      const body = await readJson<Schemas['PatchInvoiceDraft']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const merged = {
        ...row,
        ...(body.invoiceNumber !== undefined && body.invoiceNumber !== null
          ? { invoiceNumber: body.invoiceNumber }
          : {}),
        ...(body.invoiceDate !== undefined && body.invoiceDate !== null
          ? {
              invoiceDate: body.invoiceDate,
              fiscalYear: Number(String(body.invoiceDate).slice(0, 4)),
            }
          : {}),
        ...(body.currencyCode !== undefined && body.currencyCode !== null
          ? { currencyCode: body.currencyCode }
          : {}),
        ...(body.lineExtensionAmount !== undefined && body.lineExtensionAmount !== null
          ? { lineExtensionAmount: body.lineExtensionAmount }
          : {}),
        ...(body.taxAmount !== undefined && body.taxAmount !== null
          ? { taxAmount: body.taxAmount }
          : {}),
        ...(body.payableAmount !== undefined && body.payableAmount !== null
          ? { payableAmount: body.payableAmount }
          : {}),
        ...(body.domainCode !== undefined && body.domainCode !== null
          ? { domainCode: body.domainCode }
          : {}),
        ...(body.vatRate !== undefined ? { vatRate: body.vatRate ?? null } : {}),
        ...(body.documentId !== undefined ? { documentId: body.documentId ?? null } : {}),
        ...(body.notes !== undefined ? { notes: body.notes ?? null } : {}),
        ...(body.payerOrganizationId !== undefined
          ? { payerOrganizationId: body.payerOrganizationId ?? null }
          : {}),
      };
      const errors = validateHeader(merged);
      if (errors.length > 0) return validationFailed(api, errors);
      if (merged.documentId && !documentIsClean(g.tenantId, merged.documentId)) {
        return problem(
          api,
          422,
          'INVOICE_DOCUMENT_UNUSABLE',
          'Fatura görüntüsü kullanılabilir değil',
        );
      }
      if (numberTaken(merged)) return numberTakenProblem();

      Object.assign(row, merged);
      row.rowVersion += 1;
      return HttpResponse.json(invoiceView(row, projectionOf(g.session, g.tenantId)), {
        headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
      });
    }),

    http.put(`${ANY}/api/v1/invoices/:invoiceId/allocations`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_MANAGE, true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const guard = guardWrite(request, g.tenantId, g.session, pathParam(params, 'invoiceId'));
      if ('error' in guard) return guard.error;
      const { row } = guard;
      if (row.status !== 'DRAFT') return frozen();

      const body = await readJson<Schemas['PutInvoiceAllocations']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const items = body.allocations ?? [];

      const errors: FieldError[] = [];
      const seen = new Set<string>();
      items.forEach((item, i) => {
        if (seen.has(item.claimId)) {
          errors.push({
            field: `allocations[${i}].claimId`,
            code: 'DUPLICATE',
            message: 'aynı dosya bir faturada bir kez yer alır',
          });
        }
        seen.add(item.claimId);
        if (!DECIMAL.test(item.allocatedAmount)) {
          errors.push({
            field: `allocations[${i}].allocatedAmount`,
            code: 'FORMAT',
            message: 'kesin ondalık bir sayı olmalı',
          });
        } else if (toMicros(item.allocatedAmount) < 0n) {
          errors.push({
            field: `allocations[${i}].allocatedAmount`,
            code: 'RANGE',
            message: 'negatif olamaz',
          });
        }
      });
      if (errors.length > 0) return validationFailed(api, errors);

      const written: StoredInvoiceClaim[] = [];
      for (const item of items) {
        const claim = claimOf(g.tenantId, item.claimId);
        if (!claim || claim.providerOrganizationId !== row.providerOrganizationId) {
          return problem(
            api,
            422,
            'CLAIM_NOT_INVOICEABLE',
            'Hasar dosyası faturalanabilir durumda değil',
            {
              detail:
                'Yalnızca onaylanmış ya da kısmen onaylanmış, bu sağlayıcıya ait dosyalar faturaya bağlanır.',
              extensions: {
                claimId: item.claimId,
                ...(claim ? { claimReference: claim.reference, claimStatus: claim.status } : {}),
              },
            },
          );
        }
        if (!INVOICEABLE_CLAIM_STATUSES.has(claim.status)) {
          return problem(
            api,
            422,
            'CLAIM_NOT_INVOICEABLE',
            'Hasar dosyası faturalanabilir durumda değil',
            {
              extensions: {
                claimId: claim.id,
                claimReference: claim.reference,
                claimStatus: claim.status,
              },
            },
          );
        }
        const live = liveInvoiceOf(g.tenantId, claim.id);
        if (live && live !== row.id) {
          return problem(api, 409, 'CLAIM_ALREADY_INVOICED', 'Hasar dosyası başka bir faturada', {
            detail: 'Bir dosya aynı anda yalnızca bir açık faturada yer alır.',
            extensions: { claimId: claim.id, claimReference: claim.reference, liveInvoiceId: live },
          });
        }
        const approved = claimTotals(world(), claim.id).approved;
        const allocated = toMicros(item.allocatedAmount);
        if (allocated > approved) {
          return problem(
            api,
            422,
            'ALLOCATION_EXCEEDS_APPROVED',
            'Tahsis onaylanan tutarı aşıyor',
            {
              detail: 'Bir dosyaya onaylanan tutardan fazlası fatura edilemez; daha azı olağandır.',
              extensions: {
                claimId: claim.id,
                claimReference: claim.reference,
                allocatedAmount: amount(allocated),
                approvedTotal: amount(approved),
              },
            },
          );
        }
        written.push({
          id: world().nextId(),
          tenantId: g.tenantId,
          invoiceId: row.id,
          claimId: claim.id,
          claimVersionNo: claim.currentVersionNo,
          allocatedAmount: amount(allocated),
          currencyCode: row.currencyCode,
          claimStatusBefore: claim.status,
          active: true,
          createdAt: new Date().toISOString(),
        });
      }

      // Replace the whole set: an endpoint that added one link at a time would make "the invoice
      // now covers exactly these" a sequence of requests.
      api.world.invoiceAllocations = world().invoiceAllocations.filter(
        (a) => a.invoiceId !== row.id,
      );
      world().invoiceAllocations.push(...written);
      // The ETag is a statement about the invoice *and what it covers*.
      row.rowVersion += 1;
      return HttpResponse.json(invoiceView(row, projectionOf(g.session, g.tenantId)), {
        headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
      });
    }),

    http.post(`${ANY}/api/v1/invoices/:invoiceId/submit`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_MANAGE, true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const key = `${g.tenantId}:submit:${request.headers.get('Idempotency-Key')!}`;
      const replay = api.replay(key);
      if (replay) {
        return HttpResponse.json(replay.body as Schemas['Invoice'], {
          status: replay.status,
          headers: replay.etag ? { ETag: replay.etag, ...NO_STORE } : NO_STORE,
        });
      }
      const guard = guardWrite(request, g.tenantId, g.session, pathParam(params, 'invoiceId'));
      if ('error' in guard) return guard.error;
      const { row } = guard;
      if (row.status !== 'DRAFT') {
        return problem(
          api,
          409,
          'INVOICE_TRANSITION_INVALID',
          'Fatura bu durumda bu işleme uygun değil',
        );
      }
      const links = allocationsOf(row.id);
      if (links.length === 0) {
        return problem(
          api,
          422,
          'INVOICE_NOTHING_ALLOCATED',
          'Faturaya hiçbir hasar dosyası bağlanmamış',
        );
      }

      // Gate one: the sum, within the tenant's tolerance.
      const total = allocationTotal(row.id);
      const payable = toMicros(row.payableAmount);
      const difference = payable - total;
      const magnitude = difference < 0n ? -difference : difference;
      if (magnitude > tolerance()) {
        return problem(
          api,
          409,
          'ALLOCATION_MISMATCH',
          'Tahsis toplamı fatura tutarıyla uyuşmuyor',
          {
            detail:
              "Faturanın ödenecek tutarı ile bağlı dosyaların toplamı, tenant'ın belirlediği tolerans içinde eşit olmalı.",
            extensions: {
              payableAmount: amount(payable),
              allocationTotal: amount(total),
              difference: amount(difference),
              tolerance: amount(tolerance()),
            },
          },
        );
      }
      // Gate two: the image, when the tenant asks for one.
      if (requiresImage() && (!row.documentId || !documentIsClean(g.tenantId, row.documentId))) {
        return problem(api, 409, 'INVOICE_IMAGE_REQUIRED', 'Fatura görüntüsü gerekli', {
          detail: 'Faturayı göndermeden önce düzenlediğiniz belgenin taranmış hâlini ekleyin.',
        });
      }
      // Gate three: every allocated claim is still what it was when it was allocated.
      for (const link of links) {
        const claim = claimOf(g.tenantId, link.claimId);
        if (!claim || !INVOICEABLE_CLAIM_STATUSES.has(claim.status)) {
          return problem(
            api,
            422,
            'CLAIM_NOT_INVOICEABLE',
            'Hasar dosyası faturalanabilir durumda değil',
            {
              extensions: {
                claimId: link.claimId,
                ...(claim ? { claimReference: claim.reference, claimStatus: claim.status } : {}),
              },
            },
          );
        }
      }

      row.status = 'SUBMITTED';
      row.submittedAt = new Date().toISOString();
      row.rowVersion += 1;
      // The claims move onto the document: from this moment the money is being collected, and
      // the earnings view stops offering them.
      for (const link of links) {
        const claim = claimOf(g.tenantId, link.claimId)!;
        claim.status = 'INVOICED';
        claim.rowVersion += 1;
      }
      // The correction takes effect now and not when it was drafted.
      if (row.supersedesInvoiceId) {
        const old = invoiceOf(g.tenantId, row.supersedesInvoiceId);
        if (old) {
          old.status = 'CANCELLED';
          old.supersededByInvoiceId = row.id;
          old.rowVersion += 1;
          releaseLinks(old);
        }
      }

      const out = invoiceView(row, projectionOf(g.session, g.tenantId));
      api.rememberIdempotent(key, 200, out, etagOf(row.rowVersion));
      return HttpResponse.json(out, {
        headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
      });
    }),

    http.post(`${ANY}/api/v1/invoices/:invoiceId/cancel`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_MANAGE, true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const guard = guardWrite(request, g.tenantId, g.session, pathParam(params, 'invoiceId'));
      if ('error' in guard) return guard.error;
      const { row } = guard;
      if (row.status !== 'DRAFT' && row.status !== 'RETURNED') {
        return problem(
          api,
          409,
          'INVOICE_TRANSITION_INVALID',
          'Fatura bu durumda bu işleme uygun değil',
        );
      }
      const links = allocationsOf(row.id);
      row.status = 'CANCELLED';
      row.rowVersion += 1;
      releaseLinks(row);
      // The claims go back where they came from. A claim that has meanwhile gone onto the
      // correction and been submitted there belongs to that document now.
      for (const link of links) {
        const claim = claimOf(g.tenantId, link.claimId);
        if (!claim || claim.status !== 'INVOICED') continue;
        const live = liveInvoiceOf(g.tenantId, claim.id);
        if (live && live !== row.id) continue;
        claim.status = link.claimStatusBefore;
        claim.rowVersion += 1;
      }
      return HttpResponse.json(invoiceView(row, projectionOf(g.session, g.tenantId)), {
        headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
      });
    }),

    http.get(`${ANY}/api/v1/invoices/:invoiceId/versions`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      const row = invoiceOf(g.tenantId, pathParam(params, 'invoiceId'));
      if (!row) return problem(api, 404, 'INVOICE_NOT_FOUND', 'Fatura bulunamadı');
      const scope = organizationScope(api, g.session, g.tenantId);
      if (scope !== null && !withinScope(scope, row.providerOrganizationId)) {
        return problem(api, 404, 'INVOICE_NOT_FOUND', 'Fatura bulunamadı');
      }
      // The chain is walked in both directions, so asking about the newest document and asking
      // about the oldest give the same answer.
      const chain = new Map<string, StoredInvoice>([[row.id, row]]);
      let grew = true;
      while (grew) {
        grew = false;
        for (const candidate of world().invoices) {
          if (candidate.tenantId !== g.tenantId || chain.has(candidate.id)) continue;
          const linked = [...chain.values()].some(
            (member) =>
              member.supersedesInvoiceId === candidate.id ||
              candidate.supersedesInvoiceId === member.id,
          );
          if (linked) {
            chain.set(candidate.id, candidate);
            grew = true;
          }
        }
      }
      const body: Schemas['InvoiceChain'] = {
        items: [...chain.values()]
          .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id))
          .map(summaryView),
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),
  ];
}
