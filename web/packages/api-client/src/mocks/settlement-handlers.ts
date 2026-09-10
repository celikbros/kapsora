/**
 * MSW handlers for the settlement: what the payer owes one provider for one decided icmal, and
 * the payment records entered against it (WP-I7-04 sections 2.2 and 2.3).
 *
 * The mock is a test double of the Go server and a divergence in either direction is a bug. Four
 * things here are transcriptions rather than re-implementations, because they are the four a
 * screen would be built wrongly against:
 *
 *   - **nothing creates a settlement.** There is no POST /settlements, here or on the server: a
 *     settlement is opened by the outbox handler of `batch.decided`, and one somebody could
 *     raise by hand would be a figure with no batch behind it;
 *   - **the second pair of eyes**: above `billing.settlement_checker_threshold` the person who
 *     decided the batch may not be the one who approves its settlement, and the approval asks
 *     for a step-up as well;
 *   - **the ceiling**: the sum of the payment records never exceeds `payableAmount`, and the
 *     refusal carries the remainder a clerk may still enter rather than only the fact that this
 *     one was too much;
 *   - **the status follows the sum**: PARTIALLY_PAID while short of the payable amount, PAID
 *     when it is reached, and nothing is ever deleted.
 *
 * Every figure is an exact decimal computed in integer micro-units and rendered in the canonical
 * trimmed form the server's `trim_scale` produces. No amount here passes through a binary float.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import { fromMicros, toMicros, type StoredPaymentRecord, type StoredSettlement } from './data';
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
  stepUpRequired,
  validationFailed,
  wait,
  withinScope,
  type FieldError,
  type MockApi,
  type MockSession,
  type Schemas,
} from './handlers';
import { NO_STORE } from './health-handlers';

const PERMISSION_SETTLEMENT_READ = 'settlement.read';
const PERMISSION_SETTLEMENT_APPROVE = 'settlement.approve';
const PERMISSION_SETTLEMENT_RECORD_PAYMENT = 'settlement.record_payment';

/** billing.settlement.status, as migration 000046 writes it. */
const SETTLEMENT_STATUSES = new Set<string>([
  'DRAFT',
  'PENDING_APPROVAL',
  'APPROVED',
  'POSTED',
  'PAID',
  'PARTIALLY_PAID',
  'RECONCILED',
  'CANCELLED',
]);

const PAYMENT_SOURCES = new Set<string>(['MANUAL', 'ERP']);

const CURRENCY = /^[A-Z]{3}$/;
const DECIMAL = /^[0-9]{1,14}(\.[0-9]{1,6})?$/;
const EXTERNAL_REFERENCE = /^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$/;
const REASON_CODE = /^[A-Z][A-Z0-9_.:-]{1,79}$/;

/**
 * The documented default of `billing.settlement_checker_threshold`. The mock has no
 * tenant-settings surface, so the documented default is what it answers — which is the case
 * every screen actually meets, and the one the server's `settings.Defaults()` returns.
 */
const DEFAULT_CHECKER_THRESHOLD = '50000';

/** Renders micro-units in the canonical trimmed decimal the server's `trim_scale` produces. */
function amount(micros: bigint): string {
  const text = fromMicros(micros);
  if (!text.includes('.')) return text;
  const trimmed = text.replace(/0+$/, '').replace(/\.$/, '');
  return trimmed === '' || trimmed === '-' ? '0' : trimmed;
}

export function settlementHandlers(api: MockApi): HttpHandler[] {
  const world = () => api.world;

  const settlementOf = (tenantId: string, id: string): StoredSettlement | undefined =>
    world().settlements.find((s) => s.tenantId === tenantId && s.id === id);

  const paymentsOf = (settlementId: string): StoredPaymentRecord[] =>
    world()
      .paymentRecords.filter((p) => p.settlementId === settlementId)
      .sort((a, b) => a.paidAt.localeCompare(b.paidAt) || a.id.localeCompare(b.id));

  const providerNameOf = (tenantId: string, providerId: string): string | undefined => {
    const relationship = world().relationships.find(
      (r) => r.id === providerId && r.tenantId === tenantId,
    );
    if (!relationship) return undefined;
    return world().organizations.get(relationship.organizationId)?.displayName;
  };

  const checkerThreshold = (): bigint => toMicros(DEFAULT_CHECKER_THRESHOLD);

  const paymentView = (row: StoredPaymentRecord): Schemas['PaymentRecord'] => ({
    id: row.id,
    settlementId: row.settlementId,
    providerOrganizationId: row.providerOrganizationId,
    externalReference: row.externalReference,
    amount: row.amount,
    currencyCode: row.currencyCode,
    paidAt: row.paidAt,
    source: row.source,
    status: row.status,
    recordedBy: row.recordedBy,
    notes: row.notes,
    createdAt: row.createdAt,
    rowVersion: row.rowVersion,
  });

  /**
   * The wire shape. A list row carries neither the recoveries nor the payments: a page of fifty
   * settlements is not a place to read five hundred bank references, and the detail is one
   * request away — which is exactly what the server answers.
   */
  const settlementView = (row: StoredSettlement, full: boolean): Schemas['Settlement'] => {
    const view: Schemas['Settlement'] = {
      id: row.id,
      reference: row.reference,
      batchId: row.batchId,
      batchReference: row.batchReference,
      versionNo: row.versionNo,
      providerOrganizationId: row.providerOrganizationId,
      payerOrganizationId: row.payerOrganizationId,
      currencyCode: row.currencyCode,
      approvedAmount: row.approvedAmount,
      withheldAmount: row.withheldAmount,
      payableAmount: row.payableAmount,
      paidAmount: row.paidAmount,
      dueDate: row.dueDate,
      settlementMethod: row.settlementMethod,
      status: row.status,
      approvedBy: row.approvedBy,
      approvedAt: row.approvedAt,
      checkedBy: row.checkedBy,
      postingId: row.postingId,
      cancelReasonCode: row.cancelReasonCode,
      recoveries: full
        ? world()
            .settlementRecoveries.filter((r) => r.settlementId === row.id)
            .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id))
            .map((r) => ({
              id: r.id,
              claimId: r.claimId,
              adjustmentId: r.adjustmentId,
              amount: r.amount,
              createdAt: r.createdAt,
            }))
        : [],
      payments: full ? paymentsOf(row.id).map(paymentView) : [],
      createdAt: row.createdAt,
      rowVersion: row.rowVersion,
    };
    const name = providerNameOf(row.tenantId, row.providerOrganizationId);
    if (name) view.providerName = name;
    return view;
  };

  /** Resolves the settlement a route names, applying the caller's provider boundary. */
  const resolve = (
    tenantId: string,
    session: MockSession,
    id: string,
  ): StoredSettlement | Response => {
    const row = settlementOf(tenantId, id);
    if (!row) {
      return problem(api, 404, 'SETTLEMENT_NOT_FOUND', 'Mutabakat bulunamadı');
    }
    const scope = organizationScope(api, session, tenantId);
    if (scope !== null && !withinScope(scope, row.providerOrganizationId)) {
      return problem(api, 404, 'SETTLEMENT_NOT_FOUND', 'Mutabakat bulunamadı');
    }
    return row;
  };

  const transitionInvalid = (): Response =>
    problem(
      api,
      409,
      'SETTLEMENT_TRANSITION_INVALID',
      'Mutabakat bu durumda bu işleme uygun değil',
    );

  return [
    http.get(`${ANY}/api/v1/settlements`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_SETTLEMENT_READ, false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');

      const status = url.searchParams.get('status');
      if (status !== null && !SETTLEMENT_STATUSES.has(status)) {
        return validationFailed(api, [
          { field: 'status', code: 'ENUM', message: 'tanımlı bir settlement durumu olmalı' },
        ]);
      }
      const provider = url.searchParams.get('providerOrganizationId');
      const payer = url.searchParams.get('payerOrganizationId');
      const batchId = url.searchParams.get('batchId');
      const currencyCode = url.searchParams.get('currencyCode');
      const dueFrom = url.searchParams.get('dueFrom');
      const dueTo = url.searchParams.get('dueTo');
      const scope = organizationScope(api, g.session, g.tenantId);

      const rows = world()
        .settlements.filter((row) => {
          if (row.tenantId !== g.tenantId) return false;
          if (scope !== null && !withinScope(scope, row.providerOrganizationId)) return false;
          if (provider && row.providerOrganizationId !== provider) return false;
          if (payer && row.payerOrganizationId !== payer) return false;
          if (batchId && row.batchId !== batchId) return false;
          if (status && row.status !== status) return false;
          if (currencyCode && row.currencyCode !== currencyCode) return false;
          if (dueFrom && row.dueDate < dueFrom) return false;
          if (dueTo && row.dueDate > dueTo) return false;
          return true;
        })
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      const page = rows.slice(offset, offset + limit);
      const body: Schemas['SettlementPage'] = {
        items: page.map((row) => settlementView(row, false)),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/settlements/:settlementId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_SETTLEMENT_READ, false);
      if ('error' in g) return g.error;
      const found = resolve(g.tenantId, g.session, pathParam(params, 'settlementId'));
      if (found instanceof Response) return found;
      return HttpResponse.json(settlementView(found, true), {
        headers: { ETag: etagOf(found.rowVersion), ...NO_STORE },
      });
    }),

    http.post(`${ANY}/api/v1/settlements/:settlementId/approve`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_SETTLEMENT_APPROVE, true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const found = resolve(g.tenantId, g.session, pathParam(params, 'settlementId'));
      if (found instanceof Response) return found;
      const expected = requireIfMatch(api, request);
      if (expected instanceof Response) return expected;
      if (expected !== found.rowVersion) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      }
      if (found.status !== 'PENDING_APPROVAL') return transitionInvalid();

      const payable = toMicros(found.payableAmount);
      const threshold = checkerThreshold();
      // Step-up first, then the second pair of eyes: a caller who is refused for both should be
      // told the one they can act on, and re-entering a password is the one.
      if (payable > threshold && !hasStepUp(g.session)) return stepUpRequired(api);

      const actorId = g.session.account.actorId;
      const batch = world().batches.find(
        (b) => b.tenantId === g.tenantId && b.id === found.batchId,
      );
      if (payable > threshold && batch?.decidedBy && batch.decidedBy === actorId) {
        return problem(
          api,
          403,
          'SETTLEMENT_DECIDER_CANNOT_APPROVE',
          'İcmali karara bağlayan kişi bu tutarda mutabakatı onaylayamaz',
          {
            detail:
              'Eşiğin üzerindeki mutabakatlarda icmali karara bağlayan kişi ödemeyi serbest ' +
              'bırakamaz; ikinci bir onaylayıcı gerekir.',
          },
        );
      }

      found.status = 'APPROVED';
      found.approvedBy = actorId;
      found.approvedAt = new Date().toISOString();
      // The countersignature is the *other* pair of eyes: the batch's decider, whose decision
      // this approval is the second signature on. Below the threshold there is one person, and
      // recording them twice would say a check happened that did not.
      found.checkedBy = payable > threshold ? (batch?.decidedBy ?? null) : null;
      found.rowVersion += 1;
      return HttpResponse.json(settlementView(found, true), {
        headers: { ETag: etagOf(found.rowVersion), ...NO_STORE },
      });
    }),

    http.post(`${ANY}/api/v1/settlements/:settlementId/cancel`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_SETTLEMENT_APPROVE, true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const found = resolve(g.tenantId, g.session, pathParam(params, 'settlementId'));
      if (found instanceof Response) return found;
      const expected = requireIfMatch(api, request);
      if (expected instanceof Response) return expected;
      if (expected !== found.rowVersion) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      }
      const body = await readJson<Schemas['CancelSettlement']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors: FieldError[] = [];
      if (!REASON_CODE.test(body.reasonCode ?? '')) {
        errors.push({
          field: 'reasonCode',
          code: 'FORMAT',
          message: 'gerekçe kodu BÜYÜK_HARF biçiminde olmalı',
        });
      }
      if (body.reasonText != null && body.reasonText.length > 1000) {
        errors.push({
          field: 'reasonText',
          code: 'MAX_LENGTH',
          message: 'gerekçe metni en fazla 1000 karakter olabilir',
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      if (found.status === 'CANCELLED') return transitionInvalid();
      if (toMicros(found.paidAmount) > 0n) {
        return problem(
          api,
          409,
          'SETTLEMENT_HAS_PAYMENTS',
          'Ödeme kaydı bulunan mutabakat iptal edilemez',
          {
            detail:
              'Yanlış girilmiş bir ödeme kaydı itirazlı (DISPUTED) işaretlenir; mutabakat ' +
              'silinmez.',
          },
        );
      }

      found.status = 'CANCELLED';
      found.cancelReasonCode = body.reasonCode;
      found.rowVersion += 1;
      // The recoveries this settlement had netted are open again: a recovery marked against a
      // cancelled settlement would be money the payer never took back and never could again.
      world().settlementRecoveries = world().settlementRecoveries.filter(
        (r) => r.settlementId !== found.id,
      );
      return HttpResponse.json(settlementView(found, true), {
        headers: { ETag: etagOf(found.rowVersion), ...NO_STORE },
      });
    }),

    http.get(
      `${ANY}/api/v1/settlements/:settlementId/payment-records`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_SETTLEMENT_READ, false);
        if ('error' in g) return g.error;
        const found = resolve(g.tenantId, g.session, pathParam(params, 'settlementId'));
        if (found instanceof Response) return found;
        const body: Schemas['PaymentRecordList'] = {
          items: paymentsOf(found.id).map(paymentView),
        };
        return HttpResponse.json(body, { headers: NO_STORE });
      },
    ),

    http.post(
      `${ANY}/api/v1/settlements/:settlementId/payment-records`,
      async ({ request, params }) => {
        await wait(api);
        // Either grant reaches this route: the finance clerk's own and the approver's. A tenant
        // that wants the approver never to touch the payment file takes the first away, which is
        // only possible while the two are separate permissions.
        let g = guardTenant(api, request, PERMISSION_SETTLEMENT_RECORD_PAYMENT, true);
        if ('error' in g) {
          const fallback = guardTenant(api, request, PERMISSION_SETTLEMENT_APPROVE, true);
          if (!('error' in fallback)) g = fallback;
        }
        if ('error' in g) return g.error;
        const missing = requireIdempotencyKey(api, request);
        if (missing) return missing;
        const found = resolve(g.tenantId, g.session, pathParam(params, 'settlementId'));
        if (found instanceof Response) return found;
        const body = await readJson<Schemas['CreatePaymentRecord']>(request);
        if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

        const errors: FieldError[] = [];
        if (!EXTERNAL_REFERENCE.test(body.externalReference ?? '')) {
          errors.push({
            field: 'externalReference',
            code: 'FORMAT',
            message: 'banka referansı harf, rakam ve . _ / - karakterlerinden oluşmalı',
          });
        }
        if (!DECIMAL.test(body.amount ?? '')) {
          errors.push({ field: 'amount', code: 'FORMAT', message: 'tutar ondalık sayı olmalı' });
        } else if (toMicros(body.amount) <= 0n) {
          errors.push({ field: 'amount', code: 'RANGE', message: 'tutar sıfırdan büyük olmalı' });
        }
        if (body.currencyCode != null && !CURRENCY.test(body.currencyCode)) {
          errors.push({
            field: 'currencyCode',
            code: 'FORMAT',
            message: 'üç harfli para birimi kodu olmalı',
          });
        }
        if (!body.paidAt) {
          errors.push({ field: 'paidAt', code: 'REQUIRED', message: 'ödeme tarihi zorunlu' });
        }
        if (body.source != null && !PAYMENT_SOURCES.has(body.source)) {
          errors.push({
            field: 'source',
            code: 'ENUM',
            message: 'kaynak MANUAL ya da ERP olmalı',
          });
        }
        if (errors.length > 0) return validationFailed(api, errors);

        if (
          found.status !== 'APPROVED' &&
          found.status !== 'POSTED' &&
          found.status !== 'PARTIALLY_PAID'
        ) {
          return problem(
            api,
            409,
            'PAYMENT_NOT_ALLOWED',
            'Bu mutabakata şu anda ödeme kaydedilemez',
            {
              detail:
                'Ödeme kaydı yalnızca onaylanmış, muhasebeleştirilmiş ya da kısmen ödenmiş ' +
                'bir mutabakata girilir.',
            },
          );
        }
        if (body.currencyCode != null && body.currencyCode !== found.currencyCode) {
          return validationFailed(api, [
            {
              field: 'currencyCode',
              code: 'MISMATCH',
              message: 'ödeme para birimi mutabakatın para birimiyle aynı olmalı',
            },
          ]);
        }
        if (
          world().paymentRecords.some(
            (p) =>
              p.tenantId === g.tenantId &&
              p.providerOrganizationId === found.providerOrganizationId &&
              p.externalReference === body.externalReference,
          )
        ) {
          return problem(
            api,
            409,
            'PAYMENT_REFERENCE_TAKEN',
            'Bu banka referansı bu sağlayıcı için zaten kayıtlı',
            {
              detail:
                'Aynı havalenin iki kez girilmesi mutabakatı ödenmiş gösterir; referansı ' +
                'kontrol edin.',
            },
          );
        }

        const payable = toMicros(found.payableAmount);
        const paid = toMicros(found.paidAmount);
        const next = paid + toMicros(body.amount);
        if (next > payable) {
          return problem(
            api,
            409,
            'PAYMENT_EXCEEDS_SETTLEMENT',
            'Ödeme kaydı mutabakat tutarını aşıyor',
            {
              detail:
                'Bir mutabakata kaydedilen ödemelerin toplamı ödenecek tutarı geçemez; ' +
                'kalan tutar kadar kayıt girebilirsiniz.',
              extensions: {
                settlementId: found.id,
                amount: body.amount,
                paidAmount: found.paidAmount,
                payableAmount: found.payableAmount,
                remainder: amount(payable - paid),
                currencyCode: found.currencyCode,
              },
            },
          );
        }

        const now = new Date().toISOString();
        world().paymentRecords.push({
          id: world().nextId(),
          tenantId: g.tenantId,
          settlementId: found.id,
          providerOrganizationId: found.providerOrganizationId,
          externalReference: body.externalReference,
          amount: amount(toMicros(body.amount)),
          currencyCode: found.currencyCode,
          paidAt: body.paidAt,
          source: body.source ?? 'MANUAL',
          status: 'RECORDED',
          recordedBy: g.session.account.actorId,
          notes: body.notes ?? null,
          createdAt: now,
          rowVersion: 1,
        });

        // The stored figure is recomputed from the rows rather than incremented, so it is the
        // sum of what is actually there — which is what the server's deferred trigger checks.
        const live = paymentsOf(found.id)
          .filter((p) => p.status !== 'DISPUTED')
          .reduce((acc, p) => acc + toMicros(p.amount), 0n);
        found.paidAmount = amount(live);
        found.status = live >= payable ? 'PAID' : live > 0n ? 'PARTIALLY_PAID' : found.status;
        found.rowVersion += 1;
        return HttpResponse.json(settlementView(found, true), {
          status: 201,
          headers: { ETag: etagOf(found.rowVersion), ...NO_STORE },
        });
      },
    ),
  ];
}
