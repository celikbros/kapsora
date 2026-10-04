/** Tenant administration projects memberships and never edits a global account. */
import { HttpResponse, http, type HttpHandler } from 'msw';
import type { MockAccount } from './data';
import {
  ANY,
  appOfRequest,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  hasStepUp,
  parseLimit,
  pathParam,
  problem,
  readJson,
  requireIdempotencyKey,
  requireIfMatch,
  stepUpRequired,
  wait,
  withinPeriod,
  type MockApi,
  type Schemas,
} from './handlers';

export function hasTenantUserPermission(
  account: MockAccount,
  tenantCode: string,
  permission: string,
): boolean {
  return account.memberships.some(
    (grant) =>
      grant.tenantCode === tenantCode &&
      grant.permissions.includes(permission) &&
      !grant.validityEmpty &&
      withinPeriod(new Date().toISOString(), grant.validFrom ?? null, grant.validTo ?? null) &&
      ((grant.scopes?.length ?? 0) === 0 ||
        grant.scopes?.some((scope) => scope.type === 'TENANT') === true),
  );
}

function projectMember(
  api: MockApi,
  account: MockAccount,
  tenantId: string,
): Schemas['TenantUser'] {
  const membership = api.tenantMembership(account, tenantId);
  return {
    id: membership.id,
    displayName: account.displayName,
    actorType: 'HUMAN',
    actorStatus: account.actorStatus ?? 'ACTIVE',
    membershipStatus: membership.status,
    validFrom: membership.validFrom,
    validTo: membership.validTo,
    validityEmpty: membership.validityEmpty,
    rowVersion: membership.rowVersion,
  };
}

function detail(api: MockApi, account: MockAccount, tenantId: string): Schemas['TenantUserDetail'] {
  const tenant = api.world.tenants.find((item) => item.id === tenantId)!;
  return {
    membership: projectMember(api, account, tenantId),
    assignedRoles: account.memberships
      .filter((grant) => grant.tenantCode === tenant.code)
      .map((grant) => ({
        code: grant.permissions.includes('identity.user.read') ? 'TENANT_ADMIN' : 'DEMO_ROLE',
        name: grant.permissions.includes('identity.user.read') ? 'Kurum Yöneticisi' : 'Demo Rol',
        isSystemRole: true,
        scopeType: (grant.scopes?.[0]?.type ??
          'TENANT') as Schemas['TenantAssignedRole']['scopeType'],
        validFrom: grant.validityEmpty ? null : (grant.validFrom ?? '2026-01-01T00:00:00Z'),
        validTo: grant.validTo ?? null,
        validityEmpty: grant.validityEmpty ?? false,
      })),
  };
}

function findAccount(
  api: MockApi,
  tenantId: string,
  membershipId: string,
): MockAccount | undefined {
  const tenant = api.world.tenants.find((item) => item.id === tenantId)!;
  return api.world.accounts.find(
    (account) =>
      account.memberships.some((grant) => grant.tenantCode === tenant.code) &&
      api.tenantMembership(account, tenantId).id === membershipId,
  );
}

function correlatedGate(api: MockApi, request: Request, permission: string, mutation: boolean) {
  const gate = guardTenant(api, request, permission, mutation);
  if ('error' in gate) return gate;
  const tenant = api.world.tenants.find((item) => item.id === gate.tenantId)!;
  const app = appOfRequest(request);
  if (
    !hasTenantUserPermission(gate.session.account, tenant.code, permission) ||
    (app !== null && app !== 'backoffice')
  )
    return { error: problem(api, 403, 'PERMISSION_DENIED', 'Bu işlem için yetkiniz yok') };
  return gate;
}

export function adminHandlers(api: MockApi): HttpHandler[] {
  // Bind the version as well as the body, matching this command's server idempotency option.
  const fingerprints = new Map<string, string>();
  return [
    http.get(`${ANY}/api/v1/admin/users`, async ({ request }) => {
      await wait(api);
      const gate = correlatedGate(api, request, 'identity.user.read', false);
      if ('error' in gate) return gate.error;
      const tenant = api.world.tenants.find((item) => item.id === gate.tenantId)!;
      const url = new URL(request.url);
      const status = url.searchParams.get('status');
      if (status && !['PENDING', 'ACTIVE', 'SUSPENDED', 'REVOKED'].includes(status))
        return problem(api, 400, 'DIRECTORY_QUERY_INVALID', 'Geçersiz liste isteği');
      const offset = decodeCursor(url.searchParams.get('cursor'));
      const limit = parseLimit(url);
      if (offset === null || limit === 'invalid')
        return problem(api, 400, 'DIRECTORY_QUERY_INVALID', 'Geçersiz liste isteği');
      const rows = api.world.accounts
        .filter((account) => account.memberships.some((grant) => grant.tenantCode === tenant.code))
        .map((account) => projectMember(api, account, gate.tenantId))
        .filter((row) => !status || row.membershipStatus === status)
        .sort((a, b) => a.id.localeCompare(b.id));
      return HttpResponse.json({
        items: rows.slice(offset, offset + limit),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['TenantUserPage']);
    }),
    http.get(`${ANY}/api/v1/admin/users/:membershipId`, async ({ request, params }) => {
      await wait(api);
      const gate = correlatedGate(api, request, 'identity.user.read', false);
      if ('error' in gate) return gate.error;
      const account = findAccount(api, gate.tenantId, pathParam(params, 'membershipId'));
      if (!account) return problem(api, 404, 'MEMBERSHIP_NOT_FOUND', 'Üyelik bulunamadı');
      return HttpResponse.json(detail(api, account, gate.tenantId), {
        headers: { ETag: etagOf(api.tenantMembership(account, gate.tenantId).rowVersion) },
      });
    }),
    http.post(`${ANY}/api/v1/admin/users/:membershipId/suspend`, async ({ request, params }) => {
      await wait(api);
      const gate = correlatedGate(api, request, 'identity.user.manage', true);
      if ('error' in gate) return gate.error;
      if (!hasStepUp(gate.session)) return stepUpRequired(api);
      const invalidKey = requireIdempotencyKey(api, request);
      if (invalidKey) return invalidKey;
      const expected = requireIfMatch(api, request);
      if (expected instanceof Response) return expected;
      if (request.headers.get('If-Match')?.trim() !== etagOf(expected))
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      const raw = await readJson<Record<string, unknown>>(request);
      const reasonCode = raw?.['reasonCode'];
      if (
        typeof reasonCode !== 'string' ||
        Object.keys(raw ?? {}).some((key) => key !== 'reasonCode')
      )
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'Geçersiz istek gövdesi');
      if (!['ACCESS_REVIEW', 'STAFF_DEPARTURE', 'SECURITY_CONCERN'].includes(reasonCode))
        return problem(
          api,
          400,
          'SUSPENSION_REASON_INVALID',
          'Geçerli bir askıya alma nedeni seçin',
        );
      const membershipId = pathParam(params, 'membershipId');
      const cacheKey = [
        'membership-suspend',
        gate.tenantId,
        gate.session.account.actorId,
        membershipId,
        request.headers.get('Idempotency-Key'),
      ].join(':');
      const fingerprint = JSON.stringify([expected, reasonCode]);
      const cached = api.replay(cacheKey);
      if (cached) {
        if (fingerprints.get(cacheKey) !== fingerprint)
          return problem(
            api,
            409,
            'IDEMPOTENCY_KEY_REUSED',
            'Komut anahtarı farklı bir istekte kullanıldı',
          );
        return HttpResponse.json(cached.body as Schemas['TenantUserDetail'], {
          status: cached.status,
          headers: { ETag: cached.etag! },
        });
      }
      const account = findAccount(api, gate.tenantId, membershipId);
      if (!account) return problem(api, 404, 'MEMBERSHIP_NOT_FOUND', 'Üyelik bulunamadı');
      const membership = api.tenantMembership(account, gate.tenantId);
      if (account.actorId === gate.session.account.actorId)
        return problem(
          api,
          409,
          'SELF_SUSPENSION_FORBIDDEN',
          'Kendi kurum erişiminizi askıya alamazsınız',
        );
      if (membership.status !== 'ACTIVE')
        return problem(
          api,
          409,
          'MEMBERSHIP_STATE_CONFLICT',
          'Yalnızca aktif üyelik askıya alınabilir',
        );
      if (membership.rowVersion !== expected)
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      const tenant = api.world.tenants.find((item) => item.id === gate.tenantId)!;
      if (
        api.membershipIsActive(account, gate.tenantId) &&
        hasTenantUserPermission(account, tenant.code, 'identity.user.manage') &&
        !api.world.accounts.some(
          (other) =>
            other.actorId !== account.actorId &&
            api.membershipIsActive(other, gate.tenantId) &&
            hasTenantUserPermission(other, tenant.code, 'identity.user.manage'),
        )
      )
        return problem(
          api,
          409,
          'LAST_TENANT_MANAGER',
          'Kurumun son yetkili yöneticisi askıya alınamaz',
        );
      // Synchronous update after all awaits makes the mock's concurrent commands serialize.
      membership.status = 'SUSPENDED';
      membership.rowVersion += 1;
      api.membershipSuspensionEvents.push({
        tenantId: gate.tenantId,
        membershipId,
        actorId: gate.session.account.actorId,
        reasonCode,
      });
      const body = detail(api, account, gate.tenantId);
      const etag = etagOf(membership.rowVersion);
      fingerprints.set(cacheKey, fingerprint);
      api.rememberIdempotent(cacheKey, 200, body, etag);
      return HttpResponse.json(body, { headers: { ETag: etag } });
    }),
  ];
}
