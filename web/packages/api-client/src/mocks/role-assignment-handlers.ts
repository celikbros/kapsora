/** An in-memory model of the bounded MGT-03A grant contract. */
import { HttpResponse, http, type HttpHandler } from 'msw';
import type { MockAccount } from './data';
import { hasTenantUserPermission } from './admin-handlers';
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

type Option = Schemas['RoleAssignmentOption'];
type Grant = MockAccount['memberships'][number];

// These are the existing system templates' supported codes, scopes and permission sets.
// They are test data for the mock API; live availability is always decided by the server.
const catalogRows: Array<[string, string, string, Option['scopeType'], string]> = [
  [
    'PROGRAM_MANAGER',
    'Program ve Fayda Yöneticisi',
    'Program, plan taslağı, hak sahibi ve kayıt yönetimi.',
    'TENANT',
    'organization.read member.read member.manage member.relationship.manage membership.manage member.contact.read member.contact.manage enrollment.manage import.execute eligibility.check program.read program.manage plan.manage entitlement.read entitlement.mapping.manage catalog.read catalog.manage pricing.quote authorization.manage fulfilment.record voucher.redeem claim.read report.read report.export worklist.read worklist.claim accommodation.property.read accommodation.waitlist.manage accommodation.no_show.review notification.read',
  ],
  [
    'CONTRACT_MANAGER',
    'Sözleşme Yöneticisi',
    'Sağlayıcı, lokasyon, uygulayıcı ve sözleşme taslağı yönetimi.',
    'TENANT',
    'organization.read provider.read provider.manage provider.practitioner.manage contract.read contract.manage contract.lodging_terms.manage catalog.read pricing.quote',
  ],
  [
    'RULE_AUTHOR',
    'Kural Yazarı',
    'Kural taslağı ve test senaryosu yazar; kendi kuralını yayımlayamaz.',
    'TENANT',
    'rule.read rule.draft catalog.read program.read',
  ],
  [
    'MEDICAL_REVIEWER',
    'Tıbbi Değerlendirici',
    'Sağlık ön onayı, rapor ve klinik talep değerlendirmesi; ödeme kararı vermez.',
    'TENANT',
    'member.read service_request.read service_request.review authorization.manage health.case.read health.clinical.read health.sensitive.read health.medical_report.review claim.read claim.medical.review document.read document.link worklist.read worklist.claim',
  ],
  [
    'FINANCIAL_REVIEWER',
    'Mali Değerlendirici',
    'Fiyat, fatura ve claim mali incelemesi yapar; hassas satır ayrıntılarını içeren raporları dışa aktarabilir.',
    'TENANT',
    'member.read service_request.read claim.read claim.financial.review invoice.read invoice.manage batch.review settlement.read settlement.record_payment fiscal.edocument.read fiscal.edocument.match accounting.posting.read document.read document.link pricing.quote report.read report.export report.export.sensitive worklist.read worklist.claim',
  ],
  [
    'AUDITOR',
    'Denetçi',
    'Raporları ve denetim kayıtlarını inceler; hassas raporları dışa aktarabilir.',
    'TENANT',
    'report.read report.export report.export.sensitive audit.read security.audit.read entitlement.read notification.read',
  ],
  [
    'SPONSOR_HR',
    'Sponsor İK',
    'Üye, talep ve hak durumu görür; klinik ayrıntıya erişmez.',
    'TENANT',
    'member.read service_request.read health.case.read claim.read invoice.read entitlement.read report.read accommodation.property.read',
  ],
  [
    'PROVIDER_ADMIN',
    'Sağlayıcı Yöneticisi',
    'Kendi sağlayıcı kuruluşunun kullanıcı, lokasyon ve uygulayıcılarını yönetir.',
    'ORGANIZATION',
    'identity.user.read identity.user.manage provider.read provider.manage provider.practitioner.manage',
  ],
  [
    'PROVIDER_STAFF',
    'Sağlayıcı Kayıt/Klinik',
    'Hak sorgusu, hizmet talebi, sağlık vakası, belge ve hizmet kaydı.',
    'ORGANIZATION',
    'member.read eligibility.check catalog.read service_request.read service_request.create service_request.submit service_request.cancel fulfilment.record voucher.redeem health.case.read health.case.manage health.clinical.read health.medical_report.manage document.upload document.read document.link pricing.quote',
  ],
  [
    'PROVIDER_BILLING',
    'Sağlayıcı Faturalama',
    'Claim, dış fatura, icmal ve settlement takibi; klinik belgeye sınırlı erişim.',
    'ORGANIZATION',
    'claim.read claim.create claim.submit claim.cancel invoice.read invoice.manage batch.create batch.submit settlement.read fiscal.edocument.read document.upload document.read report.read report.export document.link',
  ],
  [
    'PROVIDER_RESERVATION',
    'Sağlayıcı Rezervasyon',
    'Konaklama kontenjanı, rezervasyon, giriş ve çıkış işlemleri.',
    'ORGANIZATION',
    'accommodation.property.read accommodation.inventory.manage accommodation.booking.manage accommodation.waitlist.manage member.read eligibility.check document.read document.upload document.booking_evidence.link',
  ],
];
const CATALOG: Option[] = catalogRows.map(([code, name, description, scopeType, permissions]) => ({
  code,
  name,
  description,
  scopeType: scopeType as Option['scopeType'],
  permissionCodes: permissions.split(' ').sort(),
  hasSensitivePermissions: [
    'PROGRAM_MANAGER',
    'CONTRACT_MANAGER',
    'MEDICAL_REVIEWER',
    'FINANCIAL_REVIEWER',
    'AUDITOR',
    'SPONSOR_HR',
    'PROVIDER_ADMIN',
    'PROVIDER_STAFF',
  ].includes(code),
}));

function available(api: MockApi, option: Option): boolean {
  const actual = api.rolePermissionOverrides.get(option.code);
  return (
    !api.privilegedRoleOverrides.has(option.code) &&
    (!actual || JSON.stringify([...actual].sort()) === JSON.stringify(option.permissionCodes))
  );
}

function member(api: MockApi, tenantId: string, membershipId: string): MockAccount | undefined {
  const tenant = api.world.tenants.find((item) => item.id === tenantId);
  return api.world.accounts.find(
    (account) =>
      account.memberships.some((grant) => grant.tenantCode === tenant?.code) &&
      api.tenantMembership(account, tenantId).id === membershipId,
  );
}

function grants(api: MockApi, account: MockAccount, tenantId: string): Grant[] {
  const code = api.world.tenants.find((tenant) => tenant.id === tenantId)!.code;
  return account.memberships.filter((grant) => grant.tenantCode === code && !grant.membershipOnly);
}

function nowOrFuture(grant: Grant): boolean {
  return (
    !grant.validityEmpty && (grant.validTo == null || grant.validTo > new Date().toISOString())
  );
}

function historicalPerson(grant: Grant): boolean {
  return grant.scopes?.some((scope) => scope.type === 'PERSON') === true;
}

function roleOf(grant: Grant): Option | undefined {
  return CATALOG.find((option) => option.code === grant.roleCode);
}

function organization(api: MockApi, tenantId: string, id: string) {
  const day = new Date().toISOString().slice(0, 10);
  const rel = api.world.relationships.find((row) => row.id === id && row.tenantId === tenantId);
  if (
    !rel ||
    rel.relationshipRole !== 'PROVIDER' ||
    rel.relationshipStatus !== 'ACTIVE' ||
    !withinPeriod(day, rel.validFrom, rel.validTo)
  )
    return null;
  const global = api.world.organizations.get(rel.organizationId);
  const profile = api.world.providers.find(
    (row) => row.tenantId === tenantId && row.tenantOrganizationId === rel.id,
  );
  if (
    !global ||
    global.organizationStatus !== 'ACTIVE' ||
    !profile ||
    profile.status !== 'ACTIVE' ||
    !withinPeriod(day, profile.contractedFrom, profile.contractedTo)
  )
    return null;
  return { id: rel.id, displayName: global.displayName, tenantCode: rel.tenantCode };
}

function relationshipDisplay(api: MockApi, tenantId: string, id: string) {
  const rel = api.world.relationships.find((row) => row.id === id && row.tenantId === tenantId);
  if (!rel || rel.relationshipRole !== 'PROVIDER') return null;
  const global = api.world.organizations.get(rel.organizationId);
  return global ? { id: rel.id, displayName: global.displayName } : null;
}

function projected(
  api: MockApi,
  tenantId: string,
  grant: Grant,
  canRevoke: boolean,
  refusalCode = 'ROLE_ASSIGNMENT_UNSUPPORTED',
): Schemas['TenantRoleGrant'] {
  grant.grantId ??= crypto.randomUUID();
  const option = roleOf(grant);
  const scope = grant.scopes?.[0];
  const rel =
    option?.scopeType === 'ORGANIZATION' &&
    available(api, option) &&
    (grant.isSystemRole ?? true) &&
    scope?.type === 'ORGANIZATION' &&
    scope.id
      ? relationshipDisplay(api, tenantId, scope.id)
      : null;
  return {
    id: grant.grantId,
    roleCode:
      grant.roleCode ??
      (grant.permissions.includes('identity.role.manage') ? 'TENANT_ADMIN' : 'DEMO_ROLE'),
    roleName: option?.name ?? grant.roleCode ?? 'Mevcut rol',
    isSystemRole: grant.isSystemRole ?? true,
    scopeType: scope?.type ?? 'TENANT',
    organizationRelationshipId: rel?.id ?? null,
    organizationDisplayName: rel?.displayName ?? null,
    validFrom: grant.validityEmpty ? null : (grant.validFrom ?? '2026-01-01T00:00:00Z'),
    validTo: grant.validTo ?? null,
    validityEmpty: grant.validityEmpty ?? false,
    canRevoke,
    revocationRefusalCode: canRevoke ? null : refusalCode,
  };
}

function eligible(api: MockApi, actor: MockAccount, target: MockAccount, tenantId: string) {
  return (
    actor.actorId !== target.actorId &&
    (target.actorType ?? 'HUMAN') === 'HUMAN' &&
    api.membershipIsActive(target, tenantId) &&
    !grants(api, target, tenantId).some((grant) => nowOrFuture(grant) || historicalPerson(grant))
  );
}

function revocable(
  api: MockApi,
  actor: MockAccount,
  target: MockAccount,
  tenantId: string,
  grant: Grant,
): boolean {
  const option = roleOf(grant);
  const scope = grant.scopes?.[0];
  return (
    actor.actorId !== target.actorId &&
    (target.actorType ?? 'HUMAN') === 'HUMAN' &&
    api.membershipIsActive(target, tenantId) &&
    nowOrFuture(grant) &&
    withinPeriod(new Date().toISOString(), grant.validFrom ?? null, grant.validTo ?? null) &&
    !!option &&
    available(api, option) &&
    (grant.isSystemRole ?? true) &&
    (scope?.type ?? 'TENANT') === option.scopeType &&
    (option.scopeType !== 'ORGANIZATION' ||
      !!(scope?.id && relationshipDisplay(api, tenantId, scope.id))) &&
    grants(api, target, tenantId).filter(nowOrFuture).length === 1 &&
    !grants(api, target, tenantId).some(historicalPerson)
  );
}

function assignmentRefusal(
  api: MockApi,
  actor: MockAccount,
  target: MockAccount,
  tenantId: string,
): string | null {
  if (actor.actorId === target.actorId) return 'SELF_ROLE_CHANGE_FORBIDDEN';
  if ((target.actorType ?? 'HUMAN') !== 'HUMAN') return 'MEMBERSHIP_STATE_CONFLICT';
  if (!api.membershipIsActive(target, tenantId)) return 'MEMBERSHIP_STATE_CONFLICT';
  return eligible(api, actor, target, tenantId) ? null : 'EXISTING_ACCESS_CONFLICT';
}

function revocationRefusal(
  api: MockApi,
  actor: MockAccount,
  target: MockAccount,
  tenantId: string,
  grant: Grant,
): string {
  if (actor.actorId === target.actorId) return 'SELF_ROLE_CHANGE_FORBIDDEN';
  if ((target.actorType ?? 'HUMAN') !== 'HUMAN') return 'MEMBERSHIP_STATE_CONFLICT';
  if (!api.membershipIsActive(target, tenantId)) return 'MEMBERSHIP_STATE_CONFLICT';
  if (
    grant.validityEmpty ||
    !withinPeriod(new Date().toISOString(), grant.validFrom ?? null, grant.validTo ?? null)
  )
    return 'GRANT_STATE_CONFLICT';
  if (
    grants(api, target, tenantId).filter(nowOrFuture).length !== 1 ||
    grants(api, target, tenantId).some(historicalPerson)
  )
    return 'EXISTING_ACCESS_CONFLICT';
  return 'ROLE_ASSIGNMENT_UNSUPPORTED';
}

function gate(api: MockApi, request: Request, mutation: boolean) {
  const result = guardTenant(api, request, 'identity.user.read', mutation);
  if ('error' in result) return result;
  const tenant = api.world.tenants.find((row) => row.id === result.tenantId)!;
  if (
    (appOfRequest(request) !== null && appOfRequest(request) !== 'backoffice') ||
    !hasTenantUserPermission(result.session.account, tenant.code, 'identity.user.read') ||
    !hasTenantUserPermission(result.session.account, tenant.code, 'identity.role.manage')
  )
    return { error: problem(api, 403, 'PERMISSION_DENIED', 'Bu işlem için yetkiniz yok') };
  return result;
}

function response(api: MockApi, result: Schemas['TenantRoleGrantResult'], etag: string) {
  return HttpResponse.json(result, { headers: { ETag: etag, 'Cache-Control': 'no-store' } });
}

export function roleAssignmentHandlers(api: MockApi): HttpHandler[] {
  return [
    http.get(`${ANY}/api/v1/admin/role-assignment-options`, async ({ request }) => {
      await wait(api);
      const g = gate(api, request, false);
      if ('error' in g) return g.error;
      return HttpResponse.json(
        { items: CATALOG.filter((option) => available(api, option)) },
        { headers: { 'Cache-Control': 'no-store' } },
      );
    }),
    http.get(`${ANY}/api/v1/admin/role-assignment-organizations`, async ({ request }) => {
      await wait(api);
      const g = gate(api, request, false);
      if ('error' in g) return g.error;
      const url = new URL(request.url),
        offset = decodeCursor(url.searchParams.get('cursor')),
        limit = parseLimit(url);
      if (offset === null || limit === 'invalid')
        return problem(api, 400, 'DIRECTORY_QUERY_INVALID', 'Geçersiz liste isteği');
      const rows = api.world.relationships
        .filter((rel) => rel.tenantId === g.tenantId)
        .map((rel) => organization(api, g.tenantId, rel.id))
        .filter((row) => row !== null)
        .sort((a, b) => a.displayName.localeCompare(b.displayName, 'tr'));
      return HttpResponse.json(
        {
          items: rows.slice(offset, offset + limit),
          nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
        },
        { headers: { 'Cache-Control': 'no-store' } },
      );
    }),
    http.get(`${ANY}/api/v1/admin/users/:membershipId/role-grants`, async ({ request, params }) => {
      await wait(api);
      const g = gate(api, request, false);
      if ('error' in g) return g.error;
      const account = member(api, g.tenantId, pathParam(params, 'membershipId'));
      if (!account) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const url = new URL(request.url),
        offset = decodeCursor(url.searchParams.get('cursor')),
        limit = parseLimit(url);
      if (offset === null || limit === 'invalid')
        return problem(api, 400, 'DIRECTORY_QUERY_INVALID', 'Geçersiz liste isteği');
      const all = grants(api, account, g.tenantId).sort((a, b) =>
        (b.validFrom ?? '').localeCompare(a.validFrom ?? ''),
      );
      const membership = api.tenantMembership(account, g.tenantId);
      const items = all
        .slice(offset, offset + limit)
        .map((grant) =>
          projected(
            api,
            g.tenantId,
            grant,
            revocable(api, g.session.account, account, g.tenantId, grant),
            revocationRefusal(api, g.session.account, account, g.tenantId, grant),
          ),
        );
      return HttpResponse.json(
        {
          membershipId: membership.id,
          membershipRowVersion: membership.rowVersion,
          canAssign: eligible(api, g.session.account, account, g.tenantId),
          assignmentRefusalCode: assignmentRefusal(api, g.session.account, account, g.tenantId),
          items,
          nextCursor: offset + limit < all.length ? encodeCursor(offset + limit) : null,
        } satisfies Schemas['TenantRoleGrantPage'],
        { headers: { ETag: etagOf(membership.rowVersion), 'Cache-Control': 'no-store' } },
      );
    }),
    http.post(
      `${ANY}/api/v1/admin/users/:membershipId/role-grants`,
      async ({ request, params }) => {
        await wait(api);
        const g = gate(api, request, true);
        if ('error' in g) return g.error;
        if (!hasStepUp(g.session)) return stepUpRequired(api);
        const badKey = requireIdempotencyKey(api, request);
        if (badKey) return badKey;
        const expected = requireIfMatch(api, request);
        if (expected instanceof Response) return expected;
        if (request.headers.get('If-Match') !== etagOf(expected))
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'Güçlü If-Match sürümü gerekli');
        if (
          request.headers.get('Content-Type')?.split(';', 1)[0]?.trim().toLowerCase() !==
          'application/json'
        )
          return problem(api, 415, 'UNSUPPORTED_MEDIA_TYPE', 'İstek biçimi desteklenmiyor');
        const body = await readJson<Record<string, unknown>>(request);
        if (
          !body ||
          typeof body['roleCode'] !== 'string' ||
          !['TENANT', 'ORGANIZATION'].includes(String(body['scopeType'])) ||
          !['ONBOARDING', 'DUTY_ASSIGNMENT'].includes(String(body['reasonCode'])) ||
          Object.keys(body).some(
            (key) =>
              !['roleCode', 'scopeType', 'organizationRelationshipId', 'reasonCode'].includes(key),
          ) ||
          (body['scopeType'] === 'TENANT' && 'organizationRelationshipId' in body) ||
          (body['scopeType'] === 'ORGANIZATION' &&
            typeof body['organizationRelationshipId'] !== 'string')
        )
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'Geçersiz istek gövdesi');
        const id = pathParam(params, 'membershipId');
        const cacheKey = `${g.tenantId}:${g.session.account.actorId}:${id}:assign:${request.headers.get('Idempotency-Key')}`;
        const fingerprint = JSON.stringify([expected, body]);
        const receipt = api.roleGrantReceipts.get(cacheKey);
        if (receipt)
          return receipt.fingerprint === fingerprint
            ? response(api, receipt.result, receipt.etag)
            : problem(
                api,
                409,
                'IDEMPOTENCY_KEY_REUSED',
                'İşlem anahtarı farklı istekte kullanıldı',
              );
        const account = member(api, g.tenantId, id);
        if (!account) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        if (account.actorId === g.session.account.actorId)
          return problem(
            api,
            409,
            'SELF_ROLE_CHANGE_FORBIDDEN',
            'Kendi rolünüzü değiştiremezsiniz',
          );
        const membership = api.tenantMembership(account, g.tenantId);
        if (
          (account.actorType ?? 'HUMAN') !== 'HUMAN' ||
          !api.membershipIsActive(account, g.tenantId)
        )
          return problem(api, 409, 'MEMBERSHIP_STATE_CONFLICT', 'Üyelik etkin değil');
        if (membership.rowVersion !== expected)
          return problem(api, 412, 'ETAG_MISMATCH', 'Üyelik değişti');
        const option = CATALOG.find((item) => item.code === body['roleCode']);
        if (!option || option.scopeType !== body['scopeType'])
          return problem(api, 409, 'ROLE_ASSIGNMENT_UNSUPPORTED', 'Rol ve kapsam desteklenmiyor');
        if (!available(api, option))
          return problem(
            api,
            409,
            'ROLE_CONFIGURATION_UNSUPPORTED',
            'Rol yapılandırması desteklenmiyor',
          );
        if (!eligible(api, g.session.account, account, g.tenantId))
          return problem(api, 409, 'EXISTING_ACCESS_CONFLICT', 'Mevcut erişim var');
        const rel =
          option.scopeType === 'ORGANIZATION'
            ? organization(api, g.tenantId, String(body['organizationRelationshipId']))
            : null;
        if (option.scopeType === 'ORGANIZATION' && !rel)
          return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        const now = new Date().toISOString();
        const grant: Grant = {
          tenantCode: api.world.tenants.find((row) => row.id === g.tenantId)!.code,
          grantId: crypto.randomUUID(),
          roleCode: option.code,
          isSystemRole: true,
          permissions: [...option.permissionCodes],
          scopes: [{ type: option.scopeType, id: rel?.id ?? null }],
          validFrom: now,
          validTo: null,
        };
        account.memberships.push(grant);
        membership.rowVersion += 1;
        const result: Schemas['TenantRoleGrantResult'] = {
          membershipId: membership.id,
          membershipRowVersion: membership.rowVersion,
          grant: projected(api, g.tenantId, grant, true),
        };
        const etag = etagOf(membership.rowVersion);
        api.roleGrantReceipts.set(cacheKey, { fingerprint, result: structuredClone(result), etag });
        api.roleGrantEvents.push({ action: 'assign', membershipId: id, grantId: grant.grantId! });
        return response(api, result, etag);
      },
    ),
    http.post(
      `${ANY}/api/v1/admin/users/:membershipId/role-grants/:grantId/revoke`,
      async ({ request, params }) => {
        await wait(api);
        const g = gate(api, request, true);
        if ('error' in g) return g.error;
        if (!hasStepUp(g.session)) return stepUpRequired(api);
        const badKey = requireIdempotencyKey(api, request);
        if (badKey) return badKey;
        const expected = requireIfMatch(api, request);
        if (expected instanceof Response) return expected;
        if (request.headers.get('If-Match') !== etagOf(expected))
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'Güçlü If-Match sürümü gerekli');
        if (
          request.headers.get('Content-Type')?.split(';', 1)[0]?.trim().toLowerCase() !==
          'application/json'
        )
          return problem(api, 415, 'UNSUPPORTED_MEDIA_TYPE', 'İstek biçimi desteklenmiyor');
        const body = await readJson<Record<string, unknown>>(request);
        if (
          !body ||
          Object.keys(body).length !== 1 ||
          !['ACCESS_REVIEW', 'DUTY_ENDED', 'SECURITY_CONCERN'].includes(String(body['reasonCode']))
        )
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'Geçersiz istek gövdesi');
        const id = pathParam(params, 'membershipId'),
          grantId = pathParam(params, 'grantId');
        const cacheKey = `${g.tenantId}:${g.session.account.actorId}:${id}:${grantId}:revoke:${request.headers.get('Idempotency-Key')}`;
        const fingerprint = JSON.stringify([expected, body]);
        const receipt = api.roleGrantReceipts.get(cacheKey);
        if (receipt)
          return receipt.fingerprint === fingerprint
            ? response(api, receipt.result, receipt.etag)
            : problem(
                api,
                409,
                'IDEMPOTENCY_KEY_REUSED',
                'İşlem anahtarı farklı istekte kullanıldı',
              );
        const account = member(api, g.tenantId, id);
        if (!account) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        if (account.actorId === g.session.account.actorId)
          return problem(
            api,
            409,
            'SELF_ROLE_CHANGE_FORBIDDEN',
            'Kendi rolünüzü değiştiremezsiniz',
          );
        const grant = grants(api, account, g.tenantId).find((item) => {
          item.grantId ??= crypto.randomUUID();
          return item.grantId === grantId;
        });
        if (!grant) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        const membership = api.tenantMembership(account, g.tenantId);
        if (
          (account.actorType ?? 'HUMAN') !== 'HUMAN' ||
          !api.membershipIsActive(account, g.tenantId)
        )
          return problem(api, 409, 'MEMBERSHIP_STATE_CONFLICT', 'Üyelik etkin değil');
        if (membership.rowVersion !== expected)
          return problem(api, 412, 'ETAG_MISMATCH', 'Üyelik değişti');
        if (!revocable(api, g.session.account, account, g.tenantId, grant))
          return problem(api, 409, 'GRANT_STATE_CONFLICT', 'Atama kaldırılamıyor');
        grant.validTo = new Date().toISOString();
        membership.rowVersion += 1;
        const result: Schemas['TenantRoleGrantResult'] = {
          membershipId: membership.id,
          membershipRowVersion: membership.rowVersion,
          grant: projected(api, g.tenantId, grant, false, 'GRANT_STATE_CONFLICT'),
        };
        const etag = etagOf(membership.rowVersion);
        api.roleGrantReceipts.set(cacheKey, { fingerprint, result: structuredClone(result), etag });
        api.roleGrantEvents.push({ action: 'revoke', membershipId: id, grantId });
        return response(api, result, etag);
      },
    ),
  ];
}
