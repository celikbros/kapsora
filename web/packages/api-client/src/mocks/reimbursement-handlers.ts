/**
 * MSW handlers for the member's reimbursement: they paid for something themselves and want the
 * money back (WP-I7-04 section 2.4, v1.2 10.10).
 *
 * The mock is a test double of the Go server and a divergence in either direction is a bug. Five
 * things here are transcriptions rather than re-implementations, because they are the five a
 * screen would be built wrongly against:
 *
 *   - **the account number is written once and read back never.** `createReimbursement` takes an
 *     IBAN, keeps four characters of it and throws the rest away. There is no field in any
 *     response, and this file never stores the number at all — a fixture holding a real one is a
 *     fixture somebody eventually copies into a screen;
 *   - **whose reimbursement this is, is the server's answer.** It comes from the PERSON grant; a
 *     body naming the caller's own person is accepted and one naming anybody else is 403;
 *   - **the duplicate rule names the earlier request.** The same receipt, or the same provider,
 *     day and amount, and the refusal carries the reference of the one it repeats;
 *   - **approval consumes the member's money entitlement, on approval, for exactly the approved
 *     amount.** Not the requested amount and not at submission; a rejection consumes nothing;
 *   - **the member reads only their own.** Another member's is 404, which is the same answer an
 *     id that does not exist gets.
 *
 * Every figure is an exact decimal computed in integer micro-units. No amount here passes
 * through a binary float.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import { fromMicros, toMicros, type StoredReimbursement } from './data';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  parseLimit,
  pathParam,
  personScope,
  problem,
  readJson,
  requireIdempotencyKey,
  requireIfMatch,
  validationFailed,
  wait,
  type FieldError,
  type MockApi,
  type Schemas,
} from './handlers';
import { NO_STORE } from './health-handlers';

const PERMISSION_CLAIM_FINANCIAL_REVIEW = 'claim.financial.review';
const PERMISSION_SERVICE_REQUEST_READ = 'service_request.read';
const PERMISSION_SERVICE_REQUEST_CREATE = 'service_request.create';
const PERMISSION_SETTLEMENT_RECORD_PAYMENT = 'settlement.record_payment';

/** billing.reimbursement.status, as migration 000046 writes it. */
const REIMBURSEMENT_STATUSES = new Set<string>([
  'DRAFT',
  'SUBMITTED',
  'UNDER_REVIEW',
  'APPROVED',
  'PARTIALLY_APPROVED',
  'REJECTED',
  'PAYMENT_ORDERED',
  'PAID',
  'CANCELLED',
]);

const CURRENCY = /^[A-Z]{3}$/;
const DECIMAL = /^[0-9]{1,14}(\.[0-9]{1,6})?$/;
const REASON_CODE = /^[A-Z][A-Z0-9_.:-]{1,79}$/;
const EXTERNAL_REFERENCE = /^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$/;
/**
 * Two letters, two digits and up to thirty more alphanumerics. It is deliberately loose: this
 * platform is not the authority on which country issues which length, and what matters is that
 * the value is an account number rather than a sentence somebody pasted.
 */
const IBAN = /^[A-Z]{2}[0-9]{2}[A-Z0-9]{10,30}$/;

/** The documented default of `billing.reimbursement_duplicate_window_days`. */
const DEFAULT_WINDOW_DAYS = 180;

/** Renders micro-units in the canonical trimmed decimal the server's `trim_scale` produces. */
function amount(micros: bigint): string {
  const text = fromMicros(micros);
  if (!text.includes('.')) return text;
  const trimmed = text.replace(/0+$/, '').replace(/\.$/, '');
  return trimmed === '' || trimmed === '-' ? '0' : trimmed;
}

/**
 * Strips the spaces a person types into an IBAN and upper-cases it, once, at the edge — so the
 * mask and the stored reference are taken from one string and the mask can never describe an
 * account other than the one that was submitted.
 */
export function normalizeAccount(raw: string): string {
  return raw.replace(/[\s-]/g, '').toUpperCase();
}

/**
 * The last four characters, and the only form of an account number this product may hold. It is
 * exported so a test can assert that nothing else in the mock produces one.
 */
export function maskAccount(normalized: string): string {
  return normalized.length < 4 ? '' : normalized.slice(-4);
}

export function reimbursementHandlers(api: MockApi): HttpHandler[] {
  const world = () => api.world;

  const rowOf = (tenantId: string, id: string): StoredReimbursement | undefined =>
    world().reimbursements.find((r) => r.tenantId === tenantId && r.id === id);

  const view = (row: StoredReimbursement): Schemas['Reimbursement'] => {
    const out: Schemas['Reimbursement'] = {
      id: row.id,
      reference: row.reference,
      personId: row.personId,
      enrollmentId: row.enrollmentId,
      serviceRequestId: row.serviceRequestId,
      claimId: row.claimId,
      receiptDocumentId: row.receiptDocumentId,
      serviceDefinitionId: row.serviceDefinitionId,
      serviceDate: row.serviceDate,
      providerOrganizationId: row.providerOrganizationId,
      requestedAmount: row.requestedAmount,
      approvedAmount: row.approvedAmount,
      currencyCode: row.currencyCode,
      // Four characters. There is no field on this type the number could go in, which is the
      // version of the promise a compiler can check.
      bankAccountMasked: row.bankAccountMasked,
      status: row.status,
      duplicateOfId: row.duplicateOfId,
      decisionReasonCode: row.decisionReasonCode,
      decidedBy: row.decidedBy,
      decidedAt: row.decidedAt,
      submittedAt: row.submittedAt,
      paymentReference: row.paymentReference,
      paidAt: row.paidAt,
      createdAt: row.createdAt,
      rowVersion: row.rowVersion,
    };
    if (row.duplicateOfId) {
      const earlier = world().reimbursements.find((r) => r.id === row.duplicateOfId);
      if (earlier) out.duplicateOfReference = earlier.reference;
    }
    return out;
  };

  /**
   * The duplicate rule of v1.2 10.10, both halves: the same person's earlier reimbursement
   * carrying the same receipt digest, or the same person's earlier one at the same provider on
   * the same day for the same amount, inside the tenant's window.
   *
   * Rejected and cancelled rows are outside it: a receipt refused on a technicality is not a
   * duplicate of anything, and refusing the corrected resubmission would be the platform
   * arguing with itself.
   */
  const findDuplicate = (
    tenantId: string,
    personId: string,
    excludeId: string | null,
    receiptSha256: string,
    providerOrganizationId: string,
    serviceDate: string,
    requestedAmount: string,
  ): { row: StoredReimbursement; byReceipt: boolean } | null => {
    const windowFrom = new Date(Date.now() - DEFAULT_WINDOW_DAYS * 86_400_000).toISOString();
    const candidates = world()
      .reimbursements.filter(
        (r) =>
          r.tenantId === tenantId &&
          r.personId === personId &&
          r.id !== excludeId &&
          r.status !== 'REJECTED' &&
          r.status !== 'CANCELLED' &&
          r.createdAt >= windowFrom,
      )
      .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id));
    const byReceipt = candidates.find((r) => r.receiptSha256 === receiptSha256);
    if (byReceipt) return { row: byReceipt, byReceipt: true };
    const byTriple = candidates.find(
      (r) =>
        r.providerOrganizationId === providerOrganizationId &&
        r.serviceDate === serviceDate &&
        toMicros(r.requestedAmount) === toMicros(requestedAmount),
    );
    return byTriple ? { row: byTriple, byReceipt: false } : null;
  };

  const duplicateProblem = (match: { row: StoredReimbursement; byReceipt: boolean }): Response =>
    problem(
      api,
      409,
      'REIMBURSEMENT_DUPLICATE',
      'Bu belge için daha önce geri ödeme talebi yapılmış',
      {
        detail:
          'Aynı fiş ya da aynı sağlayıcı, tarih ve tutar için açık bir başvurunuz zaten var; ' +
          'önceki başvurunun numarası yanıtta yer alıyor.',
        extensions: {
          existingReimbursementId: match.row.id,
          existingReference: match.row.reference,
          existingServiceDate: match.row.serviceDate,
          matchedBy: match.byReceipt ? 'RECEIPT' : 'PROVIDER_DATE_AMOUNT',
        },
      },
    );

  const transitionInvalid = (): Response =>
    problem(
      api,
      409,
      'REIMBURSEMENT_TRANSITION_INVALID',
      'Geri ödeme başvurusu bu durumda bu işleme uygun değil',
    );

  const notFoundProblem = (): Response =>
    problem(api, 404, 'REIMBURSEMENT_NOT_FOUND', 'Geri ödeme başvurusu bulunamadı');

  /** The member's own money account, which is what an approval draws on. */
  const moneyAccountOf = (tenantId: string, personId: string, currencyCode: string) =>
    world()
      .entitlementAccounts.filter(
        (a) =>
          a.tenantId === tenantId &&
          a.personId === personId &&
          a.status === 'OPEN' &&
          a.definition.unitType === 'MONEY' &&
          (a.definition.currencyCode ?? 'TRY') === currencyCode,
      )
      .sort((a, b) => (toMicros(b.available) > toMicros(a.available) ? 1 : -1))
      .at(0);

  const page = (rows: StoredReimbursement[], offset: number, limit: number) => {
    const slice = rows.slice(offset, offset + limit);
    const body: Schemas['ReimbursementPage'] = {
      items: slice.map(view),
      nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
    };
    return HttpResponse.json(body, { headers: NO_STORE });
  };

  return [
    http.get(`${ANY}/api/v1/reimbursements`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CLAIM_FINANCIAL_REVIEW, false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const status = url.searchParams.get('status');
      if (status !== null && !REIMBURSEMENT_STATUSES.has(status)) {
        return validationFailed(api, [
          { field: 'status', code: 'ENUM', message: 'tanımlı bir geri ödeme durumu olmalı' },
        ]);
      }
      const personId = url.searchParams.get('personId');
      const from = url.searchParams.get('from');
      const to = url.searchParams.get('to');
      const rows = world()
        .reimbursements.filter((r) => {
          if (r.tenantId !== g.tenantId) return false;
          if (personId && r.personId !== personId) return false;
          if (status && r.status !== status) return false;
          if (from && r.serviceDate < from) return false;
          if (to && r.serviceDate > to) return false;
          return true;
        })
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      return page(rows, offset, limit);
    }),

    http.get(`${ANY}/api/v1/me/reimbursements`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_SERVICE_REQUEST_READ, false);
      if ('error' in g) return g.error;
      const person = personScope(api, g.session, g.tenantId);
      if (!person) {
        return problem(api, 403, 'PERSON_BINDING_MISSING', 'Bu hesap bir kişiye bağlı değil', {
          detail: 'Üye ekranları için hesabın bir kişiye bağlanmış olması gerekir.',
        });
      }
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const status = url.searchParams.get('status');
      if (status !== null && !REIMBURSEMENT_STATUSES.has(status)) {
        return validationFailed(api, [
          { field: 'status', code: 'ENUM', message: 'tanımlı bir geri ödeme durumu olmalı' },
        ]);
      }
      const rows = world()
        .reimbursements.filter(
          (r) =>
            r.tenantId === g.tenantId && r.personId === person && (!status || r.status === status),
        )
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      return page(rows, offset, limit);
    }),

    http.post(`${ANY}/api/v1/reimbursements`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_SERVICE_REQUEST_CREATE, true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const person = personScope(api, g.session, g.tenantId);
      if (!person) {
        return problem(api, 403, 'PERSON_BINDING_MISSING', 'Bu hesap bir kişiye bağlı değil');
      }
      const body = await readJson<Schemas['CreateReimbursement']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      // The answer is the server's: a body naming the caller's own person is fine, and one
      // naming anybody else is refused before a single row is read.
      if (body.personId && body.personId !== person) {
        return problem(api, 403, 'PERSON_SCOPE', 'Yalnızca kendi adınıza işlem yapabilirsiniz', {
          detail: 'Bu hesap yalnızca bağlı olduğu kişi adına işlem yapabilir.',
        });
      }

      const errors: FieldError[] = [];
      if (!DECIMAL.test(body.requestedAmount ?? '')) {
        errors.push({
          field: 'requestedAmount',
          code: 'FORMAT',
          message: 'tutar ondalık sayı olmalı',
        });
      } else if (toMicros(body.requestedAmount) <= 0n) {
        errors.push({
          field: 'requestedAmount',
          code: 'RANGE',
          message: 'tutar sıfırdan büyük olmalı',
        });
      }
      if (body.currencyCode != null && !CURRENCY.test(body.currencyCode)) {
        errors.push({
          field: 'currencyCode',
          code: 'FORMAT',
          message: 'üç harfli para birimi kodu olmalı',
        });
      }
      const account = normalizeAccount(body.bankAccount ?? '');
      if (!IBAN.test(account)) {
        errors.push({
          field: 'bankAccount',
          code: 'FORMAT',
          message: 'IBAN geçerli biçimde olmalı',
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const serviceRequest = world().serviceRequests.find(
        (r) => r.tenantId === g.tenantId && r.id === body.serviceRequestId,
      );
      if (
        !serviceRequest ||
        serviceRequest.requestType !== 'REIMBURSEMENT' ||
        serviceRequest.personId !== person ||
        !serviceRequest.providerOrganizationId
      ) {
        // One refusal for four mistakes, on purpose: telling a member which of them it was
        // would answer a question about somebody else's data.
        return problem(
          api,
          422,
          'REIMBURSEMENT_REQUEST_UNUSABLE',
          'Bu talep geri ödemeye uygun değil',
          {
            detail:
              'Geri ödeme, kendi adınıza açılmış ve bir sağlayıcı adı taşıyan bir ' +
              'REIMBURSEMENT talebine bağlanır.',
          },
        );
      }
      const receipt = world().documents.find(
        (d) => d.tenantId === g.tenantId && d.id === body.receiptDocumentId,
      );
      if (!receipt || receipt.scanStatus !== 'CLEAN' || receipt.purgedAt !== null) {
        return problem(
          api,
          422,
          'REIMBURSEMENT_RECEIPT_UNUSABLE',
          'Fiş ya da fatura belgesi kullanılabilir değil',
          { detail: 'Belge bu kuruma ait, taranmış ve temiz çıkmış olmalı.' },
        );
      }
      const enrollment = world().enrollments.find(
        (e) => e.tenantId === g.tenantId && e.id === serviceRequest.enrollmentId,
      );
      if (
        !enrollment ||
        enrollment.status !== 'ACTIVE' ||
        enrollment.validFrom > serviceRequest.serviceDate ||
        (enrollment.validTo !== null && enrollment.validTo <= serviceRequest.serviceDate)
      ) {
        return problem(
          api,
          422,
          'REIMBURSEMENT_NOT_ELIGIBLE',
          'Hizmet tarihinde kapsam dışındasınız',
          { detail: 'Geri ödeme, paranın harcandığı gün geçerli olan bir üyeliğe bağlanır.' },
        );
      }

      const requested = amount(toMicros(body.requestedAmount));
      const digest = receipt.sha256 ?? receipt.id;
      const duplicate = findDuplicate(
        g.tenantId,
        person,
        null,
        digest,
        serviceRequest.providerOrganizationId,
        serviceRequest.serviceDate,
        requested,
      );
      if (duplicate) return duplicateProblem(duplicate);

      const now = new Date().toISOString();
      const definitionId =
        world().serviceRequestVersions.find((v) => v.serviceRequestId === serviceRequest.id)
          ?.items[0]?.serviceDefinitionId ?? '';
      const row: StoredReimbursement = {
        id: world().nextId(),
        tenantId: g.tenantId,
        reference: nextReference(api),
        personId: person,
        enrollmentId: serviceRequest.enrollmentId,
        serviceRequestId: serviceRequest.id,
        claimId: null,
        receiptDocumentId: receipt.id,
        receiptSha256: digest,
        serviceDefinitionId: definitionId,
        serviceDate: serviceRequest.serviceDate,
        providerOrganizationId: serviceRequest.providerOrganizationId,
        requestedAmount: requested,
        approvedAmount: null,
        currencyCode: body.currencyCode ?? 'TRY',
        // The number itself is dropped here and nowhere is it kept: the mock cannot encrypt,
        // so it stores an opaque token instead of pretending to.
        bankAccountRefEnc: `enc:${world().nextId()}`,
        bankAccountMasked: maskAccount(account),
        status: 'DRAFT',
        duplicateOfId: null,
        decisionReasonCode: null,
        decisionReasonText: null,
        decidedBy: null,
        decidedAt: null,
        submittedAt: null,
        paymentReference: null,
        paidAt: null,
        createdAt: now,
        rowVersion: 1,
      };
      world().reimbursements.push(row);
      return HttpResponse.json(view(row), {
        status: 201,
        headers: {
          ETag: etagOf(row.rowVersion),
          Location: `/api/v1/reimbursements/${row.id}`,
          ...NO_STORE,
        },
      });
    }),

    http.get(`${ANY}/api/v1/reimbursements/:reimbursementId`, async ({ request, params }) => {
      await wait(api);
      // The reviewer's permission first; a caller who does not hold it falls back to the
      // member's own route, which is narrowed by the PERSON grant.
      let g = guardTenant(api, request, PERMISSION_CLAIM_FINANCIAL_REVIEW, false);
      let person: string | null = null;
      if ('error' in g) {
        const member = guardTenant(api, request, PERMISSION_SERVICE_REQUEST_READ, false);
        if ('error' in member) return member.error;
        person = personScope(api, member.session, member.tenantId);
        if (!person) {
          return problem(api, 403, 'PERSON_BINDING_MISSING', 'Bu hesap bir kişiye bağlı değil');
        }
        g = member;
      }
      const row = rowOf(g.tenantId, pathParam(params, 'reimbursementId'));
      if (!row || (person !== null && row.personId !== person)) return notFoundProblem();
      return HttpResponse.json(view(row), {
        headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
      });
    }),

    http.post(
      `${ANY}/api/v1/reimbursements/:reimbursementId/submit`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_SERVICE_REQUEST_CREATE, true);
        if ('error' in g) return g.error;
        const missing = requireIdempotencyKey(api, request);
        if (missing) return missing;
        const person = personScope(api, g.session, g.tenantId);
        if (!person) {
          return problem(api, 403, 'PERSON_BINDING_MISSING', 'Bu hesap bir kişiye bağlı değil');
        }
        const row = rowOf(g.tenantId, pathParam(params, 'reimbursementId'));
        if (!row || row.personId !== person) return notFoundProblem();
        const expected = requireIfMatch(api, request);
        if (expected instanceof Response) return expected;
        if (expected !== row.rowVersion) {
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        }
        if (row.status !== 'DRAFT') return transitionInvalid();

        // The duplicate check runs again, and it is not a repeat: between the draft and the
        // submission somebody else may have claimed the same receipt, and the moment that
        // matters is the moment it is actually claimed.
        const duplicate = findDuplicate(
          g.tenantId,
          person,
          row.id,
          row.receiptSha256,
          row.providerOrganizationId,
          row.serviceDate,
          row.requestedAmount,
        );
        if (duplicate) return duplicateProblem(duplicate);

        row.status = 'SUBMITTED';
        row.submittedAt = new Date().toISOString();
        row.rowVersion += 1;
        return HttpResponse.json(view(row), {
          headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
        });
      },
    ),

    http.post(
      `${ANY}/api/v1/reimbursements/:reimbursementId/decide`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_CLAIM_FINANCIAL_REVIEW, true);
        if ('error' in g) return g.error;
        const missing = requireIdempotencyKey(api, request);
        if (missing) return missing;
        const row = rowOf(g.tenantId, pathParam(params, 'reimbursementId'));
        if (!row) return notFoundProblem();
        const expected = requireIfMatch(api, request);
        if (expected instanceof Response) return expected;
        if (expected !== row.rowVersion) {
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        }
        const body = await readJson<Schemas['DecideReimbursement']>(request);
        if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        if (row.status !== 'SUBMITTED' && row.status !== 'UNDER_REVIEW') {
          return transitionInvalid();
        }

        const errors: FieldError[] = [];
        if (body.decision !== 'APPROVE' && body.decision !== 'REJECT') {
          errors.push({
            field: 'decision',
            code: 'ENUM',
            message: 'karar APPROVE ya da REJECT olmalı',
          });
        }
        if (body.reasonCode != null && !REASON_CODE.test(body.reasonCode)) {
          errors.push({
            field: 'reasonCode',
            code: 'FORMAT',
            message: 'gerekçe kodu BÜYÜK_HARF biçiminde olmalı',
          });
        }
        if (body.decision === 'REJECT' && !body.reasonCode) {
          errors.push({
            field: 'reasonCode',
            code: 'REQUIRED',
            message: 'ret için gerekçe kodu zorunlu',
          });
        }
        if (body.approvedAmount != null && !DECIMAL.test(body.approvedAmount)) {
          errors.push({
            field: 'approvedAmount',
            code: 'FORMAT',
            message: 'tutar ondalık sayı olmalı',
          });
        }
        if (errors.length > 0) return validationFailed(api, errors);

        const requested = toMicros(row.requestedAmount);
        const now = new Date().toISOString();

        if (body.decision === 'REJECT') {
          // Nothing is consumed. A member whose receipt was refused has the same balance
          // afterwards as before, exactly.
          row.status = 'REJECTED';
          row.approvedAmount = '0';
          row.decisionReasonCode = body.reasonCode ?? null;
          row.decisionReasonText = body.reasonText ?? null;
          row.decidedBy = g.session.account.actorId;
          row.decidedAt = now;
          row.rowVersion += 1;
          return HttpResponse.json(view(row), {
            headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
          });
        }

        const approved = body.approvedAmount != null ? toMicros(body.approvedAmount) : requested;
        if (approved <= 0n) {
          return validationFailed(api, [
            {
              field: 'approvedAmount',
              code: 'RANGE',
              message: 'onaylanan tutar sıfırdan büyük olmalı; reddetmek için REJECT kullanın',
            },
          ]);
        }
        if (approved > requested) {
          return validationFailed(api, [
            {
              field: 'approvedAmount',
              code: 'RANGE',
              message: 'onaylanan tutar talep edilen tutardan büyük olamaz',
            },
          ]);
        }
        if (approved < requested && !body.reasonCode) {
          return validationFailed(api, [
            {
              field: 'reasonCode',
              code: 'REQUIRED',
              message: 'kısmi onay için gerekçe kodu zorunlu',
            },
          ]);
        }

        const account = moneyAccountOf(g.tenantId, row.personId, row.currencyCode);
        if (!account) {
          return problem(
            api,
            422,
            'ENTITLEMENT_ACCOUNT_NOT_FOUND',
            'Bu hizmet için parasal hak hesabı bulunamadı',
            {
              detail:
                'Onaylanan tutar üyenin parasal hakkından düşülür; planda bu para biriminde ' +
                'bir parasal hak tanımlı olmalı.',
            },
          );
        }
        if (toMicros(account.available) < approved) {
          return problem(
            api,
            409,
            'ENTITLEMENT_INSUFFICIENT',
            'Üyenin kalan hakkı onaylanan tutar için yeterli değil',
            { detail: 'Kalan hak kadar kısmi onay verebilirsiniz.' },
          );
        }

        // Exactly the approved amount, and not the requested one.
        account.available = fromMicros(toMicros(account.available) - approved);
        account.consumed = fromMicros(toMicros(account.consumed) + approved);
        account.rowVersion += 1;

        row.approvedAmount = amount(approved);
        row.decisionReasonCode = body.reasonCode ?? null;
        row.decisionReasonText = body.reasonText ?? null;
        row.decidedBy = g.session.account.actorId;
        row.decidedAt = now;
        // The claim WP-I7-01 raises at the approval and never before. The mock names it rather
        // than writing a whole claim: what a screen needs is that there is one.
        row.claimId = world().nextId();
        // Straight to PAYMENT_ORDERED: the payment adapter has been handed the order, and in
        // this milestone the adapter records it and does nothing (v1.2 §4.3).
        row.status = 'PAYMENT_ORDERED';
        row.rowVersion += 1;
        return HttpResponse.json(view(row), {
          headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
        });
      },
    ),

    http.post(
      `${ANY}/api/v1/reimbursements/:reimbursementId/payment`,
      async ({ request, params }) => {
        await wait(api);
        let g = guardTenant(api, request, PERMISSION_SETTLEMENT_RECORD_PAYMENT, true);
        if ('error' in g) {
          const fallback = guardTenant(api, request, PERMISSION_CLAIM_FINANCIAL_REVIEW, true);
          if (!('error' in fallback)) g = fallback;
        }
        if ('error' in g) return g.error;
        const missing = requireIdempotencyKey(api, request);
        if (missing) return missing;
        const row = rowOf(g.tenantId, pathParam(params, 'reimbursementId'));
        if (!row) return notFoundProblem();
        const expected = requireIfMatch(api, request);
        if (expected instanceof Response) return expected;
        if (expected !== row.rowVersion) {
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        }
        const body = await readJson<Schemas['RecordReimbursementPayment']>(request);
        if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        if (!EXTERNAL_REFERENCE.test(body.paymentReference ?? '')) {
          return validationFailed(api, [
            {
              field: 'paymentReference',
              code: 'FORMAT',
              message: 'ödeme referansı harf, rakam ve . _ / - karakterlerinden oluşmalı',
            },
          ]);
        }
        if (
          row.status !== 'APPROVED' &&
          row.status !== 'PARTIALLY_APPROVED' &&
          row.status !== 'PAYMENT_ORDERED'
        ) {
          return transitionInvalid();
        }
        row.status = 'PAID';
        row.paymentReference = body.paymentReference;
        row.paidAt = body.paidAt ?? new Date().toISOString();
        row.rowVersion += 1;
        return HttpResponse.json(view(row), {
          headers: { ETag: etagOf(row.rowVersion), ...NO_STORE },
        });
      },
    ),
  ];
}

const REFERENCE_ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';

/** `RB-YYYYMM-XXXXXXXX`, drawn from the world's own stream so a rebuild is identical. */
function nextReference(api: MockApi): string {
  let tail = '';
  for (let i = 0; i < 8; i += 1) {
    tail += REFERENCE_ALPHABET[Math.floor(api.world.random() * REFERENCE_ALPHABET.length)];
  }
  const now = new Date();
  const month = `${now.getUTCFullYear()}${String(now.getUTCMonth() + 1).padStart(2, '0')}`;
  return `RB-${month}-${tail}`;
}
