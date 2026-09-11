/**
 * MSW handlers for entitlement balances, the append-only ledger and the manual
 * adjustments that need a second actor's approval. Every quantity stays a decimal string,
 * exactly as the Go API sends it.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  addDecimal,
  reachableEntitlementAccounts,
  toDecimal,
  toEntitlementAccount,
  toEntitlementAdjustment,
  toLedgerEntry,
  type MockWorld,
  type StoredAdjustment,
  type StoredEntitlementAccount,
} from './data';
import type { MockApi } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  hasStepUp,
  parseIfMatch,
  parseLimit,
  pathParam,
  problem,
  readJson,
  stepUpRequired,
  wait,
  type Schemas,
  ownFile,
} from './handlers';

export function entitlementHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  const findAccount = (tenantId: string, id: string): StoredEntitlementAccount | undefined =>
    world().entitlementAccounts.find((a) => a.id === id && a.tenantId === tenantId);
  const findAdjustment = (tenantId: string, id: string): StoredAdjustment | undefined =>
    world().adjustments.find((a) => a.id === id && a.tenantId === tenantId);

  return [
    http.get(`${ANY}/api/v1/people/:personId/entitlements`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'entitlement.read', false);
      if ('error' in g) return g.error;
      const personId = pathParam(params, 'personId');
      if (!world().people.some((p) => p.id === personId && p.tenantId === g.tenantId)) {
        return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      }
      const asOf =
        new URL(request.url).searchParams.get('asOf') ?? new Date().toISOString().slice(0, 10);
      const items = reachableEntitlementAccounts(world(), g.tenantId, personId, asOf).map((r) =>
        toEntitlementAccount(r.account, r.shared),
      );
      return HttpResponse.json({ items });
    }),

    http.get(`${ANY}/api/v1/entitlement-accounts/:accountId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'entitlement.read', false);
      if ('error' in g) return g.error;
      const account = findAccount(g.tenantId, pathParam(params, 'accountId'));
      if (!account) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      return HttpResponse.json(toEntitlementAccount(account, false), {
        headers: { ETag: etagOf(account.rowVersion) },
      });
    }),

    http.get(
      `${ANY}/api/v1/entitlement-accounts/:accountId/ledger`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'entitlement.read', false);
        if ('error' in g) return g.error;
        const account = findAccount(g.tenantId, pathParam(params, 'accountId'));
        if (!account) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        const url = new URL(request.url);
        const limit = parseLimit(url);
        if (limit === 'invalid') {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: [{ field: 'limit', code: 'FORMAT' }],
          });
        }
        const offset = decodeCursor(url.searchParams.get('cursor'));
        if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
        const rows = world()
          .ledgerEntries.filter((e) => e.tenantId === g.tenantId && e.accountId === account.id)
          .sort((a, b) =>
            a.effectiveAt < b.effectiveAt ? 1 : a.effectiveAt > b.effectiveAt ? -1 : 0,
          );
        const page = rows.slice(offset, offset + limit);
        return HttpResponse.json({
          items: page.map(toLedgerEntry),
          nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
        });
      },
    ),

    http.post(
      `${ANY}/api/v1/entitlement-accounts/:accountId/adjustments`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'entitlement.adjust', true);
        if ('error' in g) return g.error;
        const account = findAccount(g.tenantId, pathParam(params, 'accountId'));
        if (!account) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        const body = await readJson<{
          deltaQuantity: unknown;
          reasonCode?: string;
          reasonText?: string;
        }>(request);
        const delta =
          typeof body?.deltaQuantity === 'string'
            ? body.deltaQuantity
            : String(body?.deltaQuantity ?? '');
        if (!body?.reasonCode || delta === '' || Number(delta) === 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: [
              {
                field: delta === '' || Number(delta) === 0 ? 'deltaQuantity' : 'reasonCode',
                code: 'QUANTITY_ZERO',
              },
            ],
          });
        }
        const adjustment: StoredAdjustment = {
          id: world().nextId(),
          tenantId: g.tenantId,
          accountId: account.id,
          deltaQuantity: toDecimal(Number(delta)),
          reasonCode: body.reasonCode,
          reasonText: body.reasonText ?? null,
          status: 'PENDING',
          requestedBy: g.session.account.actorId,
          requestedAt: new Date().toISOString(),
          decidedBy: null,
          decidedAt: null,
          decisionComment: null,
          ledgerEntryId: null,
          rowVersion: 1,
        };
        world().adjustments.push(adjustment);
        return HttpResponse.json(toEntitlementAdjustment(adjustment), {
          status: 201,
          headers: { ETag: etagOf(1) },
        });
      },
    ),

    http.get(`${ANY}/api/v1/entitlement-adjustments`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'entitlement.read', false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'limit', code: 'FORMAT' }],
        });
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const status = url.searchParams.get('status') ?? 'PENDING';
      const rows = world()
        .adjustments.filter((a) => a.tenantId === g.tenantId && a.status === status)
        .sort((a, b) => (a.requestedAt < b.requestedAt ? 1 : -1));
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map(toEntitlementAdjustment),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      });
    }),

    http.post(
      `${ANY}/api/v1/entitlement-adjustments/:adjustmentId/approve`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'entitlement.adjust', true);
        if ('error' in g) return g.error;
        if (!hasStepUp(g.session)) return stepUpRequired(api);
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null)
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        const adjustment = findAdjustment(g.tenantId, pathParam(params, 'adjustmentId'));
        if (!adjustment) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        if (adjustment.status !== 'PENDING') {
          return problem(api, 409, 'IMPORT_STATE_INVALID', 'Düzeltme zaten karara bağlanmış');
        }
        if (adjustment.rowVersion !== expected)
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        if (adjustment.requestedBy === g.session.account.actorId) {
          return problem(api, 403, 'MAKER_CHECKER_SAME_ACTOR', 'Kendi talebinizi onaylayamazsınız');
        }
        const own = ownFile(
          api,
          g.session,
          g.tenantId,
          findAccount(g.tenantId, adjustment.accountId)?.personId,
        );
        if (own) return own;
        const account = findAccount(g.tenantId, adjustment.accountId);
        if (!account) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        const body = (await readJson<Schemas['ReviewComment']>(request)) ?? {};
        const entry = {
          id: world().nextId(),
          tenantId: g.tenantId,
          accountId: account.id,
          effectiveAt: new Date().toISOString(),
          movementType: 'ADJUST' as const,
          deltaAvailable: adjustment.deltaQuantity,
          deltaConsumed: '0.000000',
          deltaExpired: '0.000000',
          deltaReserved: '0.000000',
          deltaTotal: adjustment.deltaQuantity,
          referenceType: 'ADJUSTMENT',
          referenceId: adjustment.id,
          reservationId: null,
          reasonCode: adjustment.reasonCode,
          reasonText: adjustment.reasonText,
          createdBy: g.session.account.actorId,
        };
        world().ledgerEntries.push(entry);
        account.available = addDecimal(account.available, adjustment.deltaQuantity);
        account.totalGranted = addDecimal(account.totalGranted, adjustment.deltaQuantity);
        account.rowVersion += 1;
        adjustment.status = 'APPROVED';
        adjustment.decidedBy = g.session.account.actorId;
        adjustment.decidedAt = new Date().toISOString();
        adjustment.decisionComment = body.comment ?? null;
        adjustment.ledgerEntryId = entry.id;
        adjustment.rowVersion += 1;
        return HttpResponse.json(toEntitlementAdjustment(adjustment), {
          headers: { ETag: etagOf(adjustment.rowVersion) },
        });
      },
    ),

    http.post(
      `${ANY}/api/v1/entitlement-adjustments/:adjustmentId/reject`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'entitlement.adjust', true);
        if ('error' in g) return g.error;
        if (!hasStepUp(g.session)) return stepUpRequired(api);
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null)
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        const adjustment = findAdjustment(g.tenantId, pathParam(params, 'adjustmentId'));
        if (!adjustment) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        if (adjustment.status !== 'PENDING') {
          return problem(api, 409, 'IMPORT_STATE_INVALID', 'Düzeltme zaten karara bağlanmış');
        }
        if (adjustment.rowVersion !== expected)
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        if (adjustment.requestedBy === g.session.account.actorId) {
          return problem(api, 403, 'MAKER_CHECKER_SAME_ACTOR', 'Kendi talebinizi reddedemezsiniz');
        }
        const own = ownFile(
          api,
          g.session,
          g.tenantId,
          findAccount(g.tenantId, adjustment.accountId)?.personId,
        );
        if (own) return own;
        const body = await readJson<Schemas['ReasonCommand']>(request);
        if (!body?.reasonCode) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: [{ field: 'reasonCode', code: 'REQUIRED' }],
          });
        }
        adjustment.status = 'REJECTED';
        adjustment.decidedBy = g.session.account.actorId;
        adjustment.decidedAt = new Date().toISOString();
        adjustment.decisionComment = body.reasonText ?? body.reasonCode;
        adjustment.rowVersion += 1;
        return HttpResponse.json(toEntitlementAdjustment(adjustment), {
          headers: { ETag: etagOf(adjustment.rowVersion) },
        });
      },
    ),
  ];
}
