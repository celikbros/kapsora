/**
 * MSW handlers for the icmal: the batch a provider bundles its submitted invoices into, and the
 * payer's decision on each of them (WP-I7-03).
 *
 * The mock is a test double of the Go server and a divergence in either direction is a bug. Five
 * things here are transcriptions rather than re-implementations, because they are the five a
 * screen would be built wrongly against:
 *
 *   - **the mixture rule**: a batch is one provider, one payer, one currency, one domain and
 *     submitted invoices only, and each of the five is refused separately with `BATCH_MIXED`
 *     naming the field and both values;
 *   - **one invoice, one live batch**: an invoice already in a live icmal is refused by name,
 *     with the icmal it is in;
 *   - **the freeze**: once a batch has left DRAFT its membership and its amounts never change.
 *     Only the decisions move, and only while it is UNDER_REVIEW;
 *   - **the cut**: the difference between what was billed and what was approved is spread across
 *     the claims the invoice covers, in proportion to their allocations, exactly, with the
 *     rounding difference on the largest allocation. A changed decision writes a REVERSAL row
 *     beside the cut rather than editing it;
 *   - **two people**: the submitter never decides, and above the tenant's threshold the person
 *     who took the last decision may not close the batch either.
 *
 * Every figure is an exact decimal computed in integer micro-units and rendered in the canonical
 * trimmed form the server's `trim_scale` produces. No amount here passes through a binary float.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  fromMicros,
  toMicros,
  type StoredBatch,
  type StoredBatchInvoice,
  type StoredClaimAdjustment,
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
  hasStepUp,
  organizationScope,
  parseLimit,
  pathParam,
  problem,
  readJson,
  requireIdempotencyKey,
  requireIfMatch,
  validationFailed,
  wait,
  withinScope,
  type FieldError,
  type Schemas,
} from './handlers';
import { NO_STORE } from './health-handlers';

const PERMISSION_READ = 'invoice.read';
const PERMISSION_BATCH_CREATE = 'batch.create';
const PERMISSION_BATCH_SUBMIT = 'batch.submit';
const PERMISSION_BATCH_REVIEW = 'batch.review';

/** billing.batch.status, as migration 000045 writes it. */
const BATCH_STATUSES = new Set<string>([
  'DRAFT',
  'SUBMITTED',
  'UNDER_REVIEW',
  'DECIDED',
  'SETTLING',
  'CLOSED',
  'CANCELLED',
]);

/** The four things a payer may say about one invoice, in the order a summary lists them. */
const DECISIONS: Schemas['BatchDecision'][] = ['APPROVE', 'CUT', 'RETURN', 'REJECT'];

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

/**
 * The closed list a cut adjustment's reason has to be in. It is the claim ledger's, because the
 * cut writes a `claim.adjustment` row and an adjustment's reason is a closed list — a reviewer
 * offered "OVERPAYMENT" as a reason for a cut would be offered a word that means something else.
 */
const CUT_REASONS = new Set<string>([
  'TARIFF_EXCEEDED',
  'CONTRACT_TERMS',
  'NOT_COVERED',
  'DUPLICATE_SERVICE',
  'DOCUMENT_MISSING',
]);

const CURRENCY = /^[A-Z]{3}$/;
const DECIMAL = /^-?[0-9]{1,14}(\.[0-9]{1,6})?$/;
const REASON_CODE = /^[A-Z][A-Z0-9_.:-]{1,79}$/;
const ISO_DATE = /^\d{4}-\d{2}-\d{2}$/;
const REFERENCE_ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';

/** The documented defaults of the three `billing.batch_*` tenant settings. */
const DEFAULT_MIN_INVOICES = 1;
const DEFAULT_MAX_INVOICES = 500;
const DEFAULT_DECISION_THRESHOLD = '100000';

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

/**
 * Divides one amount across a set of weights, exactly.
 *
 * Every share is truncated towards zero and the remainder — never negative, never as large as one
 * micro-unit per weight — is added to the *largest* weight, which is the claim on which a kuruş is
 * least visible as a proportion of what it carries. Ties fall to the first, so the answer is
 * deterministic. Rounding each share independently would leave a few kuruş belonging to nobody,
 * and a cut whose adjustments did not add up to the cut is a settlement nobody can reconcile.
 */
export function splitProportional(total: bigint, weights: bigint[]): bigint[] {
  if (weights.length === 0) return [];
  const sum = weights.reduce((acc, w) => acc + w, 0n);
  let largest = 0;
  for (let i = 1; i < weights.length; i += 1) {
    if (weights[i]! > weights[largest]!) largest = i;
  }
  if (sum === 0n) {
    return weights.map((_, i) => (i === 0 ? total : 0n));
  }
  const shares = weights.map((w) => (total * w) / sum);
  const allocated = shares.reduce((acc, share) => acc + share, 0n);
  shares[largest] = shares[largest]! + (total - allocated);
  return shares;
}

export function batchHandlers(api: MockApi): HttpHandler[] {
  const world = () => api.world;

  const batchOf = (tenantId: string, id: string): StoredBatch | undefined =>
    world().batches.find((b) => b.tenantId === tenantId && b.id === id);

  const membersOf = (batchId: string): StoredBatchInvoice[] =>
    world()
      .batchInvoices.filter((m) => m.batchId === batchId)
      .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id));

  const invoiceOf = (tenantId: string, id: string): StoredInvoice | undefined =>
    world().invoices.find((i) => i.tenantId === tenantId && i.id === id);

  /** The batch an invoice is already live in, or null. */
  const liveBatchOf = (tenantId: string, invoiceId: string): string | null =>
    world().batchInvoices.find(
      (m) => m.tenantId === tenantId && m.invoiceId === invoiceId && m.active,
    )?.batchId ?? null;

  const providerNameOf = (tenantId: string, providerId: string): string | undefined => {
    const relationship = world().relationships.find(
      (r) => r.id === providerId && r.tenantId === tenantId,
    );
    if (!relationship) return undefined;
    return world().organizations.get(relationship.organizationId)?.displayName;
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
   * The tenant's bounds and threshold. The mock has no tenant-settings surface, so the documented
   * defaults are what it answers — which is the case every screen actually meets, and the one the
   * server's `settings.Defaults()` returns.
   */
  const minInvoices = (): number => DEFAULT_MIN_INVOICES;
  const maxInvoices = (): number => DEFAULT_MAX_INVOICES;
  const decisionThreshold = (): bigint => toMicros(DEFAULT_DECISION_THRESHOLD);

  const nextReference = (): string => {
    let tail = '';
    for (let i = 0; i < 8; i += 1) {
      tail += REFERENCE_ALPHABET[Math.floor(world().random() * REFERENCE_ALPHABET.length)];
    }
    const now = new Date();
    const month = `${now.getUTCFullYear()}${String(now.getUTCMonth() + 1).padStart(2, '0')}`;
    return `IC-${month}-${tail}`;
  };

  const memberView = (member: StoredBatchInvoice): Schemas['BatchInvoice'] => {
    const invoice = invoiceOf(member.tenantId, member.invoiceId);
    const view: Schemas['BatchInvoice'] = {
      id: member.id,
      invoiceId: member.invoiceId,
      invoiceNumber: invoice?.invoiceNumber ?? '',
      invoiceDate: invoice?.invoiceDate ?? '1970-01-01',
      invoiceStatus: invoice?.status ?? 'DRAFT',
      currencyCode: invoice?.currencyCode ?? 'TRY',
      submittedAmount: amount(toMicros(member.submittedAmount)),
      active: member.active,
      createdAt: member.createdAt,
    };
    // The decision fields are absent on a member nobody has answered: an empty string beside
    // "decision" would read as a decision somebody made.
    if (member.decision) view.decision = member.decision;
    if (member.approvedAmount !== null) {
      view.approvedAmount = amount(toMicros(member.approvedAmount));
    }
    if (member.reasonCode) view.reasonCode = member.reasonCode;
    if (member.reasonText) view.reasonText = member.reasonText;
    if (member.decidedBy) {
      view.decidedBy = member.decidedBy;
      const account = world().accounts.find((a) => a.actorId === member.decidedBy);
      if (account) view.decidedByDisplayName = account.displayName;
    }
    if (member.decidedAt) view.decidedAt = member.decidedAt;
    return view;
  };

  /** `withMembers` is false for a list row: a page of fifty icmals is not a place to read five
   * hundred invoice decisions, and the detail is one request away. */
  const batchView = (row: StoredBatch, withMembers = true): Schemas['Batch'] => ({
    id: row.id,
    reference: row.reference,
    providerOrganizationId: row.providerOrganizationId,
    ...(providerNameOf(row.tenantId, row.providerOrganizationId)
      ? { providerName: providerNameOf(row.tenantId, row.providerOrganizationId)! }
      : {}),
    ...(row.payerOrganizationId ? { payerOrganizationId: row.payerOrganizationId } : {}),
    domainCode: row.domainCode,
    currencyCode: row.currencyCode,
    periodFrom: row.periodFrom,
    periodTo: row.periodTo,
    status: row.status,
    ...(row.submittedAt ? { submittedAt: row.submittedAt } : {}),
    ...(row.submittedBy ? { submittedBy: row.submittedBy } : {}),
    ...(row.decidedAt ? { decidedAt: row.decidedAt } : {}),
    ...(row.decidedBy ? { decidedBy: row.decidedBy } : {}),
    invoiceCount: row.invoiceCount,
    submittedTotal: amount(toMicros(row.submittedTotal)),
    approvedTotal: amount(toMicros(row.approvedTotal)),
    cutTotal: amount(toMicros(row.cutTotal)),
    returnedTotal: amount(toMicros(row.returnedTotal)),
    rejectedTotal: amount(toMicros(row.rejectedTotal)),
    invoices: withMembers ? membersOf(row.id).map(memberView) : [],
    createdAt: row.createdAt,
    rowVersion: row.rowVersion,
  });

  const notFound = (): Response => problem(api, 404, 'BATCH_NOT_FOUND', 'İcmal bulunamadı');

  const frozen = (): Response =>
    problem(api, 409, 'BATCH_FROZEN', 'Gönderilmiş icmalin içeriği değiştirilemez', {
      detail:
        'İcmal gönderildikten sonra hangi faturaları kapsadığı ve tutarları donar; değişiklik için yeni bir icmal açın.',
    });

  const transitionInvalid = (): Response =>
    problem(api, 409, 'BATCH_TRANSITION_INVALID', 'İcmal bu durumda bu işleme uygun değil');

  const mixed = (
    field: string,
    invoice: StoredInvoice | undefined,
    expected: string,
    actual: string,
  ): Response =>
    problem(api, 422, 'BATCH_MIXED', 'Bu fatura bu icmale girmiyor', {
      detail:
        'Bir icmal tek sağlayıcı, tek ödeyici, tek para birimi ve tek alan kodundan oluşur; içine yalnızca gönderilmiş faturalar girer.',
      extensions: {
        field,
        ...(invoice ? { invoiceId: invoice.id, invoiceNumber: invoice.invoiceNumber } : {}),
        ...(expected ? { expectedValue: expected } : {}),
        ...(actual ? { actualValue: actual } : {}),
      },
    });

  /** Everything a write command establishes before it acts: the row, the scope and the ETag. */
  function guardWrite(
    request: Request,
    tenantId: string,
    session: MockSession,
    batchId: string,
  ): { row: StoredBatch } | { error: Response } {
    const row = batchOf(tenantId, batchId);
    if (!row) return { error: notFound() };
    const scope = organizationScope(api, session, tenantId);
    if (scope !== null && !withinScope(scope, row.providerOrganizationId)) {
      // 404 rather than 403: that an icmal exists at all is somebody else's business.
      return { error: notFound() };
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

  /** The active claim links of one invoice, which is what a cut is spread over. */
  const allocationsOf = (invoiceId: string, activeOnly: boolean): StoredInvoiceClaim[] =>
    world()
      .invoiceAllocations.filter((a) => a.invoiceId === invoiceId && (!activeOnly || a.active))
      .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id));

  /** Moves the links of a cancelled or returned invoice out of the way, as the trigger does. */
  const releaseLinks = (invoice: StoredInvoice): void => {
    const live = invoice.status !== 'CANCELLED' && invoice.status !== 'RETURNED';
    for (const row of world().invoiceAllocations.filter((a) => a.invoiceId === invoice.id)) {
      row.active = live;
    }
  };

  /** Writes one CUT adjustment per claim and the link that lets a change take it back. */
  const writeCut = (
    tenantId: string,
    member: StoredBatchInvoice,
    invoice: StoredInvoice,
    cut: bigint,
    reasonCode: string,
    reasonText: string | null,
    actorId: string,
  ): void => {
    const links = allocationsOf(invoice.id, true);
    const shares = splitProportional(
      cut,
      links.map((l) => toMicros(l.allocatedAmount)),
    );
    links.forEach((link, index) => {
      const share = shares[index]!;
      // A share that rounded to nothing is not an adjustment: a zero row would say the payer
      // took nothing off this claim.
      if (share === 0n) return;
      const adjustment: StoredClaimAdjustment = {
        id: world().nextId(),
        tenantId,
        claimId: link.claimId,
        versionNo: link.claimVersionNo,
        claimLineId: null,
        adjustmentType: 'CUT',
        amount: amount(share),
        // The whole of a reviewer's cut is the payer's own money: it is what the payer took off
        // what it would have paid the provider, and the member's share of it is nothing.
        payerAmount: amount(share),
        memberAmount: '0',
        currencyCode: link.currencyCode,
        reasonCode,
        reasonText,
        sourceType: 'REVIEW',
        sourceId: null,
        reversesAdjustmentId: null,
        createdBy: actorId,
        createdAt: new Date().toISOString(),
      };
      world().claimAdjustments.push(adjustment);
      world().batchAdjustments.push({
        id: world().nextId(),
        tenantId,
        batchInvoiceId: member.id,
        claimId: link.claimId,
        adjustmentId: adjustment.id,
        amount: adjustment.amount,
        reversedByAdjustmentId: null,
      });
    });
  };

  /**
   * Takes back whatever the member's current decision did.
   *
   * Every branch is the exact inverse of the matching branch of `applyDecision`, and the pairing
   * is why they sit next to each other. Nothing here edits: a cut is taken back by a REVERSAL row
   * beside it, because the ledger is what an appeal is answered from.
   */
  const undoDecision = (
    tenantId: string,
    member: StoredBatchInvoice,
    actorId: string,
  ): Response | null => {
    const invoice = invoiceOf(tenantId, member.invoiceId);
    if (!invoice) return notFound();
    switch (member.decision) {
      case null:
      case 'APPROVE':
        return null;
      case 'CUT': {
        for (const link of world().batchAdjustments.filter(
          (a) => a.batchInvoiceId === member.id && a.reversedByAdjustmentId === null,
        )) {
          const original = world().claimAdjustments.find((a) => a.id === link.adjustmentId);
          if (!original) continue;
          const reversal: StoredClaimAdjustment = {
            ...original,
            id: world().nextId(),
            adjustmentType: 'REVERSAL',
            amount: amount(-toMicros(original.amount)),
            payerAmount: amount(-toMicros(original.payerAmount)),
            memberAmount: amount(-toMicros(original.memberAmount)),
            reasonCode: 'REVIEW_REVERSED',
            reasonText: null,
            sourceType: 'MANUAL',
            reversesAdjustmentId: original.id,
            createdBy: actorId,
            createdAt: new Date().toISOString(),
          };
          world().claimAdjustments.push(reversal);
          link.reversedByAdjustmentId = reversal.id;
        }
        return null;
      }
      case 'RETURN':
      case 'REJECT': {
        invoice.status = 'IN_BATCH';
        invoice.rowVersion += 1;
        releaseLinks(invoice);
        for (const link of allocationsOf(invoice.id, false)) {
          const claim = world().claims.find((c) => c.id === link.claimId);
          if (!claim) continue;
          claim.status = 'INVOICED';
          claim.rowVersion += 1;
        }
        return null;
      }
      default:
        return null;
    }
  };

  /** Performs what the new decision means for the invoice and its claims. */
  const applyDecision = (
    tenantId: string,
    member: StoredBatchInvoice,
    decision: Schemas['BatchDecision'],
    submitted: bigint,
    approved: bigint,
    reasonCode: string,
    reasonText: string | null,
    actorId: string,
  ): Response | null => {
    const invoice = invoiceOf(tenantId, member.invoiceId);
    if (!invoice) return notFound();
    switch (decision) {
      case 'APPROVE':
        // Nothing moves yet. The invoice becomes APPROVED when the whole batch is decided, so a
        // reviewer who changes their mind halfway through has not already told a provider that a
        // document was accepted.
        return null;
      case 'CUT':
        writeCut(tenantId, member, invoice, submitted - approved, reasonCode, reasonText, actorId);
        return null;
      case 'RETURN': {
        invoice.status = 'RETURNED';
        invoice.rowVersion += 1;
        releaseLinks(invoice);
        for (const link of allocationsOf(invoice.id, false)) {
          const claim = world().claims.find((c) => c.id === link.claimId);
          if (!claim || claim.status !== 'INVOICED') continue;
          claim.status = link.claimStatusBefore;
          claim.rowVersion += 1;
        }
        return null;
      }
      case 'REJECT': {
        invoice.status = 'REJECTED';
        invoice.rowVersion += 1;
        for (const link of allocationsOf(invoice.id, true)) {
          const claim = world().claims.find((c) => c.id === link.claimId);
          if (!claim) continue;
          claim.status = 'CLOSED_UNPAID';
          claim.rowVersion += 1;
        }
        return null;
      }
      default:
        return null;
    }
  };

  /** The four totals, recomputed from the decisions exactly as the server recomputes them. */
  const totalsOf = (
    members: StoredBatchInvoice[],
  ): { submitted: bigint; approved: bigint; cut: bigint; returned: bigint; rejected: bigint } => {
    let submitted = 0n;
    let approved = 0n;
    let cut = 0n;
    let returned = 0n;
    let rejected = 0n;
    for (const member of members) {
      const value = toMicros(member.submittedAmount);
      submitted += value;
      switch (member.decision) {
        case 'APPROVE':
          approved += value;
          break;
        case 'CUT':
          approved += toMicros(member.approvedAmount ?? '0');
          cut += value - toMicros(member.approvedAmount ?? '0');
          break;
        case 'RETURN':
          returned += value;
          break;
        case 'REJECT':
          rejected += value;
          break;
        default:
          break;
      }
    }
    return { submitted, approved, cut, returned, rejected };
  };

  return [
    http.get(`${ANY}/api/v1/batches`, async ({ request }) => {
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
      if (status !== null && !BATCH_STATUSES.has(status)) {
        return validationFailed(api, [
          { field: 'status', code: 'ENUM', message: 'tanımlı bir icmal durumu olmalı' },
        ]);
      }
      const provider = url.searchParams.get('providerOrganizationId');
      const payer = url.searchParams.get('payerOrganizationId');
      const domainCode = url.searchParams.get('domainCode');
      const currencyCode = url.searchParams.get('currencyCode');
      const from = url.searchParams.get('from');
      const to = url.searchParams.get('to');
      const scope = organizationScope(api, g.session, g.tenantId);

      const rows = world()
        .batches.filter((row) => {
          if (row.tenantId !== g.tenantId) return false;
          if (scope !== null && !withinScope(scope, row.providerOrganizationId)) return false;
          if (provider && row.providerOrganizationId !== provider) return false;
          if (payer && row.payerOrganizationId !== payer) return false;
          if (status && row.status !== status) return false;
          if (domainCode && row.domainCode !== domainCode) return false;
          if (currencyCode && row.currencyCode !== currencyCode) return false;
          // The filter is an overlap, not a containment: an icmal covering March is in the answer
          // to "what happened in the second half of March".
          if (from && row.periodTo < from) return false;
          if (to && row.periodFrom > to) return false;
          return true;
        })
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      const page = rows.slice(offset, offset + limit);
      const body: Schemas['BatchPage'] = {
        items: page.map((row) => batchView(row, false)),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.post(`${ANY}/api/v1/batches`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_BATCH_CREATE, true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const key = `${g.tenantId}:batch:${request.headers.get('Idempotency-Key')!}`;
      const replay = api.replay(key);
      if (replay) {
        return HttpResponse.json(replay.body as Schemas['Batch'], {
          status: replay.status,
          headers: replay.etag ? { ETag: replay.etag, ...NO_STORE } : NO_STORE,
        });
      }
      const body = await readJson<Schemas['CreateBatch']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

      const scope = organizationScope(api, g.session, g.tenantId);
      if (scope !== null && !withinScope(scope, body.providerOrganizationId)) {
        return problem(api, 403, 'INVOICE_PROVIDER_SCOPE', 'Bu sağlayıcı adına işlem yapamazsınız');
      }

      const errors: FieldError[] = [];
      const currencyCode = body.currencyCode ?? 'TRY';
      const domainCode = body.domainCode ?? 'GENERIC';
      if (!CURRENCY.test(currencyCode)) {
        errors.push({
          field: 'currencyCode',
          code: 'FORMAT',
          message: 'üç harfli para birimi kodu olmalı',
        });
      }
      if (!DOMAIN_CODES.has(domainCode)) {
        errors.push({ field: 'domainCode', code: 'ENUM', message: 'tanımlı bir alan kodu olmalı' });
      }
      if (!ISO_DATE.test(body.periodFrom)) {
        errors.push({
          field: 'periodFrom',
          code: 'FORMAT',
          message: 'YYYY-MM-DD biçiminde olmalı',
        });
      }
      if (!ISO_DATE.test(body.periodTo)) {
        errors.push({ field: 'periodTo', code: 'FORMAT', message: 'YYYY-MM-DD biçiminde olmalı' });
      }
      if (errors.length === 0 && body.periodTo < body.periodFrom) {
        errors.push({
          field: 'periodTo',
          code: 'RANGE',
          message: 'dönem bitişi başlangıcından önce olamaz',
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      if (!isProvider(g.tenantId, body.providerOrganizationId)) {
        return problem(
          api,
          422,
          'INVOICE_PROVIDER_UNKNOWN',
          "Bu kurum tenant'ın sağlayıcısı değil",
        );
      }

      const row: StoredBatch = {
        id: world().nextId(),
        tenantId: g.tenantId,
        reference: nextReference(),
        providerOrganizationId: body.providerOrganizationId,
        payerOrganizationId: body.payerOrganizationId ?? null,
        domainCode,
        currencyCode,
        periodFrom: body.periodFrom,
        periodTo: body.periodTo,
        status: 'DRAFT',
        submittedAt: null,
        submittedBy: null,
        decidedAt: null,
        decidedBy: null,
        invoiceCount: 0,
        submittedTotal: '0',
        approvedTotal: '0',
        cutTotal: '0',
        returnedTotal: '0',
        rejectedTotal: '0',
        createdAt: new Date().toISOString(),
        rowVersion: 1,
      };
      world().batches.push(row);
      const out = batchView(row);
      api.rememberIdempotent(key, 201, out, etagOf(row.rowVersion));
      return HttpResponse.json(out, {
        status: 201,
        headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
      });
    }),

    http.get(`${ANY}/api/v1/batches/:batchId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      const row = batchOf(g.tenantId, pathParam(params, 'batchId'));
      if (!row) return notFound();
      const scope = organizationScope(api, g.session, g.tenantId);
      if (scope !== null && !withinScope(scope, row.providerOrganizationId)) return notFound();
      return HttpResponse.json(batchView(row), {
        headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
      });
    }),

    http.get(`${ANY}/api/v1/batches/:batchId/summary`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      const row = batchOf(g.tenantId, pathParam(params, 'batchId'));
      if (!row) return notFound();
      const scope = organizationScope(api, g.session, g.tenantId);
      if (scope !== null && !withinScope(scope, row.providerOrganizationId)) return notFound();

      const members = membersOf(row.id);
      let pendingCount = 0;
      let pendingTotal = 0n;
      const buckets = new Map<string, { count: number; submitted: bigint; approved: bigint }>();
      for (const decision of DECISIONS) {
        buckets.set(decision, { count: 0, submitted: 0n, approved: 0n });
      }
      for (const member of members) {
        const value = toMicros(member.submittedAmount);
        if (!member.decision) {
          pendingCount += 1;
          pendingTotal += value;
          continue;
        }
        const bucket = buckets.get(member.decision)!;
        bucket.count += 1;
        bucket.submitted += value;
        bucket.approved += toMicros(member.approvedAmount ?? '0');
      }
      const body: Schemas['BatchSummary'] = {
        batch: batchView(row, false),
        pendingCount,
        pendingTotal: amount(pendingTotal),
        // All four are always listed, with zeroes where nothing carries them, so a screen can
        // draw a stable set of columns.
        decisions: DECISIONS.map((decision) => {
          const bucket = buckets.get(decision)!;
          return {
            decision,
            count: bucket.count,
            submittedTotal: amount(bucket.submitted),
            approvedTotal: amount(bucket.approved),
          };
        }),
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.put(`${ANY}/api/v1/batches/:batchId/invoices`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_BATCH_CREATE, true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const guard = guardWrite(request, g.tenantId, g.session, pathParam(params, 'batchId'));
      if ('error' in guard) return guard.error;
      const { row } = guard;
      if (row.status !== 'DRAFT') return frozen();

      const body = await readJson<Schemas['PutBatchInvoices']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

      // Duplicates are collapsed rather than refused: naming the same invoice twice is a client's
      // mistake and not a different icmal.
      const ordered = [...new Set(body.invoiceIds)];
      const written: StoredBatchInvoice[] = [];
      let total = 0n;
      for (const invoiceId of ordered) {
        const invoice = invoiceOf(g.tenantId, invoiceId);
        if (!invoice) return mixed('invoiceId', undefined, '', '');
        if (invoice.providerOrganizationId !== row.providerOrganizationId) {
          return mixed(
            'providerOrganizationId',
            invoice,
            row.providerOrganizationId,
            invoice.providerOrganizationId,
          );
        }
        if ((invoice.payerOrganizationId ?? '') !== (row.payerOrganizationId ?? '')) {
          return mixed(
            'payerOrganizationId',
            invoice,
            row.payerOrganizationId ?? '',
            invoice.payerOrganizationId ?? '',
          );
        }
        if (invoice.currencyCode !== row.currencyCode) {
          return mixed('currencyCode', invoice, row.currencyCode, invoice.currencyCode);
        }
        if (invoice.domainCode !== row.domainCode) {
          return mixed('domainCode', invoice, row.domainCode, invoice.domainCode);
        }
        if (invoice.status !== 'SUBMITTED') {
          return mixed('status', invoice, 'SUBMITTED', invoice.status);
        }
        const live = liveBatchOf(g.tenantId, invoiceId);
        if (live && live !== row.id) {
          return problem(api, 409, 'INVOICE_ALREADY_BATCHED', 'Fatura başka bir icmalde', {
            detail: 'Bir fatura aynı anda yalnızca bir açık icmalde yer alır.',
            extensions: {
              invoiceId,
              invoiceNumber: invoice.invoiceNumber,
              liveBatchId: live,
            },
          });
        }
        total += toMicros(invoice.payableAmount);
        written.push({
          id: world().nextId(),
          tenantId: g.tenantId,
          batchId: row.id,
          invoiceId,
          submittedAmount: invoice.payableAmount,
          decision: null,
          approvedAmount: null,
          reasonCode: null,
          reasonText: null,
          decidedBy: null,
          decidedAt: null,
          active: true,
          createdAt: new Date().toISOString(),
        });
      }

      // Replace the whole set: an endpoint that added one invoice at a time would make "the icmal
      // now covers exactly these" a sequence of requests.
      api.world.batchInvoices = world().batchInvoices.filter((m) => m.batchId !== row.id);
      world().batchInvoices.push(...written);
      row.invoiceCount = written.length;
      row.submittedTotal = amount(total);
      // The ETag is a statement about the icmal *and what it covers*.
      row.rowVersion += 1;
      return HttpResponse.json(batchView(row), {
        headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
      });
    }),

    http.post(`${ANY}/api/v1/batches/:batchId/submit`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_BATCH_SUBMIT, true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const key = `${g.tenantId}:batch-submit:${request.headers.get('Idempotency-Key')!}`;
      const replay = api.replay(key);
      if (replay) {
        return HttpResponse.json(replay.body as Schemas['Batch'], {
          status: replay.status,
          headers: replay.etag ? { ETag: replay.etag, ...NO_STORE } : NO_STORE,
        });
      }
      const guard = guardWrite(request, g.tenantId, g.session, pathParam(params, 'batchId'));
      if ('error' in guard) return guard.error;
      const { row } = guard;
      if (row.status !== 'DRAFT') return transitionInvalid();

      const members = membersOf(row.id);
      if (members.length < minInvoices() || members.length > maxInvoices()) {
        return problem(
          api,
          409,
          'BATCH_SIZE_OUT_OF_RANGE',
          'İcmaldeki fatura sayısı kurumun sınırları dışında',
          {
            detail: 'Bir icmal, kurumun belirlediği en az ve en çok fatura sayısı arasında olmalı.',
            extensions: {
              invoiceCount: members.length,
              minInvoices: minInvoices(),
              maxInvoices: maxInvoices(),
            },
          },
        );
      }

      let total = 0n;
      for (const member of members) total += toMicros(member.submittedAmount);
      if (total > decisionThreshold() && !hasStepUp(g.session)) {
        return problem(
          api,
          403,
          'STEP_UP_REQUIRED',
          'Bu işlem için parolanızı yeniden doğrulayın',
          {
            detail:
              'İcmalin toplam tutarı kurumun eşiğinin üzerinde; göndermeden önce kimliğinizi doğrulayın.',
          },
        );
      }

      // Every invoice has to move: one that stayed SUBMITTED while the icmal collecting it went
      // out would be an invoice a second icmal could pick up.
      for (const member of members) {
        const invoice = invoiceOf(g.tenantId, member.invoiceId);
        if (!invoice || invoice.status !== 'SUBMITTED') {
          return mixed('status', invoice, 'SUBMITTED', invoice?.status ?? '');
        }
      }
      for (const member of members) {
        const invoice = invoiceOf(g.tenantId, member.invoiceId)!;
        invoice.status = 'IN_BATCH';
        invoice.batchId = row.id;
        invoice.rowVersion += 1;
      }
      row.status = 'SUBMITTED';
      row.submittedAt = new Date().toISOString();
      row.submittedBy = g.session.account.actorId;
      row.invoiceCount = members.length;
      row.submittedTotal = amount(total);
      row.rowVersion += 1;

      const out = batchView(row);
      api.rememberIdempotent(key, 200, out, etagOf(row.rowVersion));
      return HttpResponse.json(out, {
        headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
      });
    }),

    http.post(
      `${ANY}/api/v1/batches/:batchId/invoices/:invoiceId/review`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_BATCH_REVIEW, true);
        if ('error' in g) return g.error;
        const missing = requireIdempotencyKey(api, request);
        if (missing) return missing;
        const guard = guardWrite(request, g.tenantId, g.session, pathParam(params, 'batchId'));
        if ('error' in guard) return guard.error;
        const { row } = guard;
        if (row.status !== 'SUBMITTED' && row.status !== 'UNDER_REVIEW') {
          return transitionInvalid();
        }
        const invoiceId = pathParam(params, 'invoiceId');
        const member = membersOf(row.id).find((m) => m.invoiceId === invoiceId);
        if (!member) {
          return problem(api, 404, 'BATCH_INVOICE_NOT_FOUND', 'Bu fatura bu icmalde değil');
        }
        const body = await readJson<Schemas['ReviewBatchInvoice']>(request);
        if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

        const submitted = toMicros(member.submittedAmount);
        const errors: FieldError[] = [];
        const decision = body.decision;
        if (!DECISIONS.includes(decision)) {
          return validationFailed(api, [
            {
              field: 'decision',
              code: 'ENUM',
              message: 'APPROVE, CUT, RETURN ya da REJECT olmalı',
            },
          ]);
        }
        const reasonCode = (body.reasonCode ?? '').trim();
        if (decision !== 'APPROVE' && reasonCode === '') {
          errors.push({
            field: 'reasonCode',
            code: 'REQUIRED',
            message: 'kesinti, iade ve ret için gerekçe kodu zorunlu',
          });
        } else if (reasonCode !== '' && !REASON_CODE.test(reasonCode)) {
          errors.push({
            field: 'reasonCode',
            code: 'FORMAT',
            message: 'büyük harf, rakam ve _ . : - içerebilir',
          });
        }
        let approved = submitted;
        if (decision === 'RETURN' || decision === 'REJECT') approved = 0n;
        if (decision === 'CUT') {
          const raw = (body.approvedAmount ?? '').trim();
          if (!DECIMAL.test(raw)) {
            errors.push({
              field: 'approvedAmount',
              code: 'FORMAT',
              message: 'kesin ondalık bir sayı olmalı',
            });
          } else {
            approved = toMicros(raw);
            if (approved <= 0n || approved >= submitted) {
              errors.push({
                field: 'approvedAmount',
                code: 'RANGE',
                message: 'kesinti tutarı sıfırdan büyük ve fatura tutarından küçük olmalı',
              });
            }
          }
          if (errors.length === 0 && !CUT_REASONS.has(reasonCode)) {
            errors.push({
              field: 'reasonCode',
              code: 'ENUM',
              message: 'kesinti gerekçesi tanımlı kesinti kodlarından biri olmalı',
            });
          }
        }
        if (errors.length > 0) return validationFailed(api, errors);

        const actorId = g.session.account.actorId;
        // Taking the first decision is what puts the icmal in front of a person.
        if (row.status === 'SUBMITTED') {
          row.status = 'UNDER_REVIEW';
          row.rowVersion += 1;
        }
        const undone = undoDecision(g.tenantId, member, actorId);
        if (undone) return undone;
        const applied = applyDecision(
          g.tenantId,
          member,
          decision,
          submitted,
          approved,
          reasonCode,
          body.reasonText ?? null,
          actorId,
        );
        if (applied) return applied;

        member.decision = decision;
        member.approvedAmount = amount(approved);
        member.reasonCode = reasonCode === '' ? null : reasonCode;
        member.reasonText = body.reasonText ?? null;
        member.decidedBy = actorId;
        member.decidedAt = new Date().toISOString();

        return HttpResponse.json(batchView(row), {
          headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
        });
      },
    ),

    http.post(`${ANY}/api/v1/batches/:batchId/decide`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_BATCH_REVIEW, true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const guard = guardWrite(request, g.tenantId, g.session, pathParam(params, 'batchId'));
      if ('error' in guard) return guard.error;
      const { row } = guard;
      if (row.status !== 'UNDER_REVIEW') return transitionInvalid();

      const actorId = g.session.account.actorId;
      // The submitter never decides, at any amount.
      if (row.submittedBy && row.submittedBy === actorId) {
        return problem(
          api,
          403,
          'BATCH_SUBMITTER_CANNOT_DECIDE',
          'İcmali gönderen kişi kararı veremez',
          { detail: 'İcmali gönderen ile karara bağlayan farklı kişiler olmalı.' },
        );
      }

      const members = membersOf(row.id);
      if (members.some((m) => !m.decision)) {
        return problem(
          api,
          409,
          'BATCH_NOT_FULLY_DECIDED',
          'İcmaldeki her fatura karara bağlanmalı',
          {
            detail:
              'İcmali kapatmadan önce kalan faturaları onaylayın, kesin, iade edin ya da reddedin.',
          },
        );
      }
      const totals = totalsOf(members);

      // Above the threshold the person who took the *last* decision may not be the one who closes
      // the batch. It is about the last decider rather than about any decider: a reviewer who
      // worked through fifty invoices should not be blocked by having touched one of them.
      if (totals.approved > decisionThreshold()) {
        const decided = members.filter((m) => m.decidedAt !== null);
        const last = decided.sort((a, b) => a.decidedAt!.localeCompare(b.decidedAt!)).at(-1);
        if (last && last.decidedBy === actorId) {
          return problem(
            api,
            403,
            'BATCH_SECOND_REVIEWER_REQUIRED',
            'Bu tutarda ikinci bir inceleyici gerekiyor',
            { detail: 'Eşiğin üzerindeki icmallerde son kararı veren kişi icmali kapatamaz.' },
          );
        }
      }

      // Where each decision leaves its invoice. A return and a rejection already moved theirs.
      for (const member of members) {
        const invoice = invoiceOf(g.tenantId, member.invoiceId);
        if (!invoice) continue;
        if (member.decision === 'APPROVE') invoice.status = 'APPROVED';
        else if (member.decision === 'CUT') invoice.status = 'PARTIALLY_APPROVED';
        else continue;
        invoice.rowVersion += 1;
      }

      row.status = 'DECIDED';
      row.decidedAt = new Date().toISOString();
      row.decidedBy = actorId;
      row.approvedTotal = amount(totals.approved);
      row.cutTotal = amount(totals.cut);
      row.returnedTotal = amount(totals.returned);
      row.rejectedTotal = amount(totals.rejected);
      row.rowVersion += 1;

      return HttpResponse.json(batchView(row), {
        headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
      });
    }),
  ];
}
