import { http, HttpResponse, type HttpHandler } from 'msw';
import {
  currentVersionOf,
  fromMicros,
  reachableEntitlementAccounts,
  toMicros,
  type MockWorld,
} from './data';
import {
  ANY,
  guardTenant,
  organizationScope,
  withinScope,
  readJson,
  requireIdempotencyKey,
  problem,
  etagOf,
  wait,
  type MockApi,
  type Schemas,
} from './handlers';

// Request-screen mock only. Claim fixtures keep their independent consumption stand-in;
// real ledger, fulfilment, cancellation and expiry correctness is tested in Go/PostgreSQL.
const records = new WeakMap<MockWorld, (Schemas['Authorization'] & { tenantId: string })[]>();
export function authorizationHandlers(api: MockApi): HttpHandler[] {
  const rows = () => {
    let result = records.get(api.world);
    if (!result) {
      result = [];
      records.set(api.world, result);
    }
    return result;
  };
  return [
    http.get(`${ANY}/api/v1/authorizations`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'service_request.read', false);
      if ('error' in g) return g.error;
      const scope = organizationScope(api, g.session, g.tenantId);
      const url = new URL(request.url);
      const requestId = url.searchParams.get('requestId');
      const items = rows()
        .filter((a) => a.tenantId === g.tenantId && (!requestId || a.requestId === requestId))
        .filter((a) =>
          api.world.serviceRequests.some(
            (r) =>
              r.id === a.requestId &&
              r.tenantId === g.tenantId &&
              withinScope(scope, r.providerOrganizationId ?? null),
          ),
        )
        .map(({ tenantId: _tenantId, ...a }) => a);
      return HttpResponse.json({ items, nextCursor: null });
    }),
    http.post(`${ANY}/api/v1/authorizations`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'authorization.manage', true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const body = await readJson<Schemas['CreateAuthorization']>(request);
      const key = `authorization:${g.tenantId}:${g.session.account.actorId}:${request.headers.get('Idempotency-Key')}`;
      const replay = api.replay(key);
      if (replay)
        return HttpResponse.json(replay.body as Schemas['Authorization'], {
          status: replay.status,
          headers: { ETag: replay.etag! },
        });
      const scope = organizationScope(api, g.session, g.tenantId);
      const source = api.world.serviceRequests.find(
        (r) =>
          r.id === body?.requestId &&
          r.tenantId === g.tenantId &&
          withinScope(scope, r.providerOrganizationId ?? null),
      );
      if (!source) return problem(api, 404, 'REQUEST_NOT_FOUND', 'Talep bulunamadı');
      if (source.status !== 'APPROVED' && source.status !== 'PARTIALLY_APPROVED')
        return problem(api, 409, 'REQUEST_NOT_APPROVED', 'Talep onaylanmadı');
      const start = body?.validFrom ?? new Date().toISOString();
      if (
        !body ||
        !Number.isFinite(Date.parse(body.validTo)) ||
        Date.parse(body.validTo) <= Date.parse(start)
      )
        return problem(api, 422, 'VALIDATION_FAILED', 'Geçerlilik sonu başlangıçtan sonra olmalı');
      const enrollment = api.world.enrollments.find(
        (e) => e.id === source.enrollmentId && e.tenantId === g.tenantId,
      );
      const version = api.world.planVersions.find(
        (v) =>
          v.planId === enrollment?.planId &&
          v.status === 'PUBLISHED' &&
          v.validFrom &&
          v.validFrom <= source.serviceDate &&
          (!v.validTo || v.validTo >= source.serviceDate),
      );
      if (!version)
        return problem(api, 422, 'ENTITLEMENT_ACCOUNT_MISSING', 'Hak hesabı bulunamadı');
      const lines = (currentVersionOf(api.world, source)?.items ?? []).filter(
        (i) =>
          (i.status === 'APPROVED' || i.status === 'PARTIALLY_APPROVED') &&
          toMicros(i.approvedQuantity ?? '0') > 0n,
      );
      if (!lines.length) return problem(api, 422, 'NO_APPROVED_ITEMS', 'Onaylı kalem yok');
      const reachable = reachableEntitlementAccounts(
        api.world,
        g.tenantId,
        source.personId,
        source.serviceDate,
      );
      const holds = [];
      const pools = new Map<string, bigint>();
      for (const line of lines) {
        const mapping = api.world.entitlementMappings.find(
          (m) =>
            m.tenantId === g.tenantId &&
            m.planVersionId === version.id &&
            m.serviceDefinitionId === line.serviceDefinitionId &&
            (!m.validFrom || m.validFrom <= source.serviceDate) &&
            (!m.validTo || m.validTo >= source.serviceDate),
        );
        const code = api.world.serviceDefinitions.find(
          (d) => d.id === line.serviceDefinitionId,
        )?.code;
        const definitionId =
          mapping?.entitlementDefinitionId ?? version.definitions.find((d) => d.code === code)?.id;
        const selected = reachable
          .filter(
            (r) =>
              r.account.status === 'OPEN' &&
              r.account.definition.id === definitionId &&
              (r.account.enrollmentId === source.enrollmentId || r.shared),
          )
          .sort((a, b) =>
            toMicros(a.account.available) > toMicros(b.account.available) ? -1 : 1,
          )[0];
        if (!selected)
          return problem(api, 422, 'ENTITLEMENT_ACCOUNT_MISSING', 'Hak hesabı bulunamadı');
        const quantity = toMicros(line.approvedQuantity!);
        const draw = (quantity * toMicros(mapping?.unitFactor ?? '1') + 500000n) / 1000000n;
        const account = selected.account;
        const left = pools.get(account.id) ?? toMicros(account.available);
        if (draw > left && !account.definition.allowOverdraft)
          return problem(api, 409, 'BALANCE_INSUFFICIENT', 'Hak bakiyesi yetersiz');
        pools.set(account.id, left - draw);
        holds.push({ line, account, draw, reservationId: api.world.nextId() });
      }
      const now = new Date().toISOString();
      const authorization: Schemas['Authorization'] = {
        id: api.world.nextId(),
        requestId: source.id,
        reference: `AUT-${now.slice(0, 10).replaceAll('-', '')}-${api.world.nextId().slice(-8).toUpperCase()}`,
        status: 'ACTIVE',
        validFrom: start,
        validTo: body.validTo,
        approvedAt: now,
        approvedBy: g.session.account.actorId,
        createdAt: now,
        rowVersion: 1,
        consumedTotal: '0',
        reservedTotal: fromMicros(lines.reduce((n, l) => n + toMicros(l.approvedQuantity!), 0n)),
        vouchers: [],
        items: holds.map((h) => ({
          id: api.world.nextId(),
          requestItemId: h.line.id,
          serviceDefinitionId: h.line.serviceDefinitionId,
          approvedQuantity: h.line.approvedQuantity!,
          consumedQuantity: '0',
          memberAmount: '0',
          entitlementReservationId: h.reservationId,
        })),
      };
      for (const { account, draw, reservationId } of holds) {
        account.available = fromMicros(toMicros(account.available) - draw);
        account.reserved = fromMicros(toMicros(account.reserved) + draw);
        account.rowVersion += 1;
        api.world.ledgerEntries.push({
          id: api.world.nextId(),
          tenantId: g.tenantId,
          accountId: account.id,
          effectiveAt: now,
          movementType: 'RESERVE',
          deltaAvailable: fromMicros(-draw),
          deltaReserved: fromMicros(draw),
          deltaConsumed: '0',
          deltaExpired: '0',
          deltaTotal: '0',
          referenceType: 'AUTHORIZATION',
          referenceId: authorization.id,
          reservationId,
          reasonCode: 'AUTHORIZATION',
          reasonText: null,
          createdBy: g.session.account.actorId,
        });
      }
      rows().push({ ...authorization, tenantId: g.tenantId });
      api.rememberIdempotent(key, 201, authorization, etagOf(1));
      return HttpResponse.json(authorization, { status: 201, headers: { ETag: etagOf(1) } });
    }),
  ];
}
