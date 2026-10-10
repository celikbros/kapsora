/** Persistent browser model for the bounded MGT-03B request lifecycle. */
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

type Option = Schemas['PrivilegedRoleAssignmentOption'];
type Entry = MockApi['roleChangeRequests'][number];
type Grant = MockAccount['memberships'][number];
const roles: Array<[string, string, string, string, string[]]> = [
  [
    'TENANT_ADMIN',
    'Kurum Yöneticisi',
    'Kurum kullanıcılarını, rollerini ve ayarlarını yönetir; klinik verilere kendiliğinden erişmez.',
    '1',
    'identity.user.read identity.user.manage identity.role.manage identity.access_review organization.read organization.manage program.read catalog.read provider.read contract.read rule.read report.read audit.read notification.manage notification.read integration.manage worklist.reassign workflow.queue.manage workflow.policy.manage document.legal_hold.manage'.split(
      ' ',
    ),
  ],
  [
    'PLAN_PUBLISHER',
    'Plan Onaylayıcı',
    'Plan sürümlerini yayımlar ve hak düzeltmelerini onaylar.',
    '2',
    'program.read plan.publish entitlement.read entitlement.adjust'.split(' '),
  ],
  [
    'CONTRACT_PUBLISHER',
    'Sözleşme Onaylayıcı',
    'Sözleşme sürümlerini yayımlar.',
    '3',
    'contract.read contract.publish'.split(' '),
  ],
  [
    'RULE_APPROVER',
    'Kural Onaylayıcı',
    'Kural sürümlerini yayımlar.',
    '4',
    'rule.read rule.publish'.split(' '),
  ],
  [
    'PAYER_APPROVER',
    'Ödeyici Onaylayıcı',
    'Mutabakat, GİB yanıtı ve muhasebe gönderiminde ikinci onay verir.',
    '5',
    'settlement.read settlement.approve settlement.record_payment fiscal.response.send accounting.posting.send accounting.reconcile report.read report.export worklist.read worklist.claim'.split(
      ' ',
    ),
  ],
];
const privileged = new Set([
  'identity.role.manage',
  'plan.publish',
  'entitlement.adjust',
  'contract.publish',
  'rule.publish',
  'settlement.approve',
  'fiscal.response.send',
  'accounting.posting.send',
  'workflow.policy.manage',
  'document.legal_hold.manage',
]);
const sensitive = new Set([
  'identity.user.manage',
  'identity.access_review',
  'integration.manage',
  'audit.read',
  'worklist.reassign',
  'accounting.reconcile',
]);
function sensitivity(code: string): Schemas['RoleChangePermission']['sensitivity'] {
  return privileged.has(code) ? 'PRIVILEGED' : sensitive.has(code) ? 'SENSITIVE' : 'NORMAL';
}
const options: Option[] = roles.map(([code, name, description, digit, permissions]) => ({
  code,
  name,
  description,
  scopeType: 'TENANT',
  requiresApproval: true,
  permissionCodes: [...permissions].sort(),
  hasSensitivePermissions: true,
  configurationHash: digit.repeat(64),
}));
function configured(api: MockApi, option: Option) {
  const actual = api.rolePermissionOverrides.get(option.code);
  return (
    !api.privilegedRoleOverrides.has(option.code) &&
    (!actual || JSON.stringify([...actual].sort()) === JSON.stringify(option.permissionCodes))
  );
}
function tenantCode(api: MockApi, tenantId: string) {
  return api.world.tenants.find((tenant) => tenant.id === tenantId)?.code ?? '';
}
function member(api: MockApi, tenantId: string, membershipId: string) {
  const code = tenantCode(api, tenantId);
  return api.world.accounts.find(
    (account) =>
      account.memberships.some((grant) => grant.tenantCode === code) &&
      api.tenantMembership(account, tenantId).id === membershipId,
  );
}
function grants(api: MockApi, tenantId: string, account: MockAccount): Grant[] {
  const code = tenantCode(api, tenantId);
  return account.memberships.filter((grant) => grant.tenantCode === code && !grant.membershipOnly);
}
function future(grant: Grant) {
  return (
    !grant.validityEmpty &&
    (grant.validTo === undefined ||
      grant.validTo === null ||
      grant.validTo > new Date().toISOString())
  );
}
function current(grant: Grant) {
  return (
    !grant.validityEmpty &&
    withinPeriod(new Date().toISOString(), grant.validFrom ?? null, grant.validTo ?? null)
  );
}
function personHistory(api: MockApi, tenantId: string, account: MockAccount) {
  return grants(api, tenantId, account).some((grant) =>
    grant.scopes?.some((scope) => scope.type === 'PERSON'),
  );
}
function manager(api: MockApi, account: MockAccount, tenantId: string) {
  const code = tenantCode(api, tenantId);
  return (
    (account.actorType ?? 'HUMAN') === 'HUMAN' &&
    api.membershipIsActive(account, tenantId) &&
    hasTenantUserPermission(account, code, 'identity.user.read') &&
    hasTenantUserPermission(account, code, 'identity.role.manage')
  );
}
function availability(
  api: MockApi,
  tenantId: string,
  makerActorId: string,
  targetActorId: string,
): 'AVAILABLE' | 'NO_ELIGIBLE_CHECKER' {
  return api.world.accounts.some(
    (account) =>
      account.actorId !== makerActorId &&
      account.actorId !== targetActorId &&
      !!account.username &&
      manager(api, account, tenantId),
  )
    ? 'AVAILABLE'
    : 'NO_ELIGIBLE_CHECKER';
}
function targetState(api: MockApi, tenantId: string, actor: MockAccount, target: MockAccount) {
  if (actor.actorId === target.actorId) return 'SELF_ROLE_CHANGE_FORBIDDEN';
  if ((target.actorType ?? 'HUMAN') !== 'HUMAN' || !api.membershipIsActive(target, tenantId))
    return 'MEMBERSHIP_STATE_CONFLICT';
  if (personHistory(api, tenantId, target)) return 'EXISTING_ACCESS_CONFLICT';
  return null;
}
function assignRefusal(api: MockApi, tenantId: string, actor: MockAccount, target: MockAccount) {
  return (
    targetState(api, tenantId, actor, target) ??
    (grants(api, tenantId, target).some(future) ? 'EXISTING_ACCESS_CONFLICT' : null)
  );
}
function revokeGrant(
  api: MockApi,
  tenantId: string,
  actor: MockAccount,
  target: MockAccount,
): Grant | null {
  if (targetState(api, tenantId, actor, target)) return null;
  const all = grants(api, tenantId, target);
  const liveOrFuture = all.filter(future);
  if (liveOrFuture.length !== 1 || !current(liveOrFuture[0]!)) return null;
  const grant = liveOrFuture[0]!;
  const option = options.find((item) => item.code === grant.roleCode);
  return option &&
    configured(api, option) &&
    (grant.isSystemRole ?? true) &&
    grant.scopes?.[0]?.type === 'TENANT' &&
    grant.scopes[0].id === null &&
    grant.scopes.length === 1 &&
    JSON.stringify([...grant.permissions].sort()) === JSON.stringify(option.permissionCodes)
    ? grant
    : null;
}
function gate(api: MockApi, request: Request, mutation: boolean) {
  const guarded = guardTenant(api, request, 'identity.user.read', mutation);
  if ('error' in guarded) return guarded;
  if (
    (appOfRequest(request) !== null && appOfRequest(request) !== 'backoffice') ||
    !manager(api, guarded.session.account, guarded.tenantId)
  )
    return { error: problem(api, 403, 'PERMISSION_DENIED', 'Bu işlem için yetkiniz yok') };
  if (mutation && !hasStepUp(guarded.session)) return { error: stepUpRequired(api) };
  return guarded;
}
function commandHeaders(api: MockApi, request: Request) {
  const badKey = requireIdempotencyKey(api, request);
  if (badKey) return { error: badKey };
  const expected = requireIfMatch(api, request);
  if (expected instanceof Response) return { error: expected };
  if (request.headers.get('If-Match') !== etagOf(expected))
    return { error: problem(api, 428, 'IF_MATCH_REQUIRED', 'Güçlü If-Match sürümü gerekli') };
  if (
    request.headers.get('Content-Type')?.split(';', 1)[0]?.trim().toLowerCase() !==
    'application/json'
  )
    return { error: problem(api, 415, 'UNSUPPORTED_MEDIA_TYPE', 'İstek biçimi desteklenmiyor') };
  return { expected, key: request.headers.get('Idempotency-Key')! };
}
function reply(result: Schemas['RoleChangeCommandResult'], etag: string, status = 200) {
  return HttpResponse.json(result, {
    status,
    headers: { ETag: etag, 'Cache-Control': 'no-store' },
  });
}
function entryDetail(
  api: MockApi,
  entry: Entry,
  caller: MockAccount,
): Schemas['RoleChangeRequestDetail'] {
  const { request, makerActorId, targetActorId, tenantId } = entry;
  const pending = request.status === 'PENDING';
  const sameMaker = caller.actorId === makerActorId;
  const sameTarget = caller.actorId === targetActorId;
  const maker = member(api, tenantId, request.makerMembershipId);
  const makerValid = !!maker && manager(api, maker, tenantId);
  const target = member(api, tenantId, request.targetMembershipId);
  const changed =
    !target ||
    api.tenantMembership(target, tenantId).rowVersion !== request.targetMembershipVersion;
  const option = options.find((item) => item.code === request.roleCode);
  const drift =
    !option || !configured(api, option) || option.configurationHash !== request.configurationHash;
  const canApprove = pending && !sameMaker && !sameTarget && makerValid && !changed && !drift;
  return {
    request,
    canApprove,
    canReject: pending && !sameMaker && !sameTarget,
    canCancel: pending && sameMaker,
    approvalRefusalCode: canApprove
      ? null
      : !pending
        ? 'ROLE_CHANGE_NOT_PENDING'
        : sameTarget
          ? 'MAKER_CHECKER_SAME_ACTOR'
          : sameMaker
            ? 'MAKER_CHECKER_SAME_ACTOR'
            : !makerValid
              ? 'ROLE_CHANGE_MAKER_UNAUTHORIZED'
              : changed
                ? 'ROLE_CHANGE_TARGET_CHANGED'
                : drift
                  ? 'ROLE_CHANGE_CONFIGURATION_CHANGED'
                  : null,
    rejectionRefusalCode:
      pending && !sameMaker && !sameTarget
        ? null
        : sameMaker
          ? 'MAKER_CHECKER_SAME_ACTOR'
          : sameTarget
            ? 'MAKER_CHECKER_SAME_ACTOR'
            : 'ROLE_CHANGE_NOT_PENDING',
    cancellationRefusalCode: pending && sameMaker ? null : 'ROLE_CHANGE_CANCEL_FORBIDDEN',
    checkerAvailability: availability(api, tenantId, makerActorId, targetActorId),
  };
}
function summary(request: Schemas['RoleChangeRequest']): Schemas['RoleChangeRequestSummary'] {
  return {
    id: request.id,
    operation: request.operation,
    status: request.status,
    targetMembershipId: request.targetMembershipId,
    makerMembershipId: request.makerMembershipId,
    roleCode: request.roleCode,
    scopeType: 'TENANT',
    reasonCode: request.reasonCode,
    createdAt: request.createdAt,
    decidedAt: request.decidedAt,
    rowVersion: request.rowVersion,
  };
}
function validity(grant: Grant): Schemas['RoleChangeValidity'] {
  return {
    from: grant.validFrom ?? null,
    fromInclusive: true,
    to: grant.validTo ?? null,
    toInclusive: false,
  };
}
function appliedGrant(grant: Grant): Schemas['RoleChangeAppliedGrant'] {
  const option = options.find((item) => item.code === grant.roleCode)!;
  return {
    id: grant.grantId!,
    roleCode: option.code,
    roleName: option.name,
    isSystemRole: true,
    scopeType: 'TENANT',
    validFrom: grant.validFrom ?? null,
    validTo: grant.validTo ?? null,
    validityEmpty: false,
  };
}

export function roleChangeHandlers(api: MockApi): HttpHandler[] {
  return [
    http.get(`${ANY}/api/v1/admin/privileged-role-assignment-options`, async ({ request }) => {
      await wait(api);
      const g = gate(api, request, false);
      if ('error' in g) return g.error;
      return HttpResponse.json(
        {
          items: options.filter((item) => configured(api, item)),
        } satisfies Schemas['PrivilegedRoleAssignmentOptions'],
        { headers: { 'Cache-Control': 'no-store' } },
      );
    }),
    http.get(
      `${ANY}/api/v1/admin/users/:membershipId/role-change-eligibility`,
      async ({ request, params }) => {
        await wait(api);
        const g = gate(api, request, false);
        if ('error' in g) return g.error;
        const target = member(api, g.tenantId, pathParam(params, 'membershipId'));
        if (!target) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        const membership = api.tenantMembership(target, g.tenantId);
        const candidate = revokeGrant(api, g.tenantId, g.session.account, target);
        if (candidate) candidate.grantId ??= crypto.randomUUID();
        return HttpResponse.json(
          {
            membershipId: membership.id,
            membershipRowVersion: membership.rowVersion,
            canRequestAssignment: !assignRefusal(api, g.tenantId, g.session.account, target),
            assignmentRefusalCode: assignRefusal(api, g.tenantId, g.session.account, target),
            revokeGrantIds: candidate ? [candidate.grantId!] : [],
            checkerAvailability: availability(
              api,
              g.tenantId,
              g.session.account.actorId,
              target.actorId,
            ),
          } satisfies Schemas['RoleChangeEligibility'],
          { headers: { ETag: etagOf(membership.rowVersion), 'Cache-Control': 'no-store' } },
        );
      },
    ),
    http.post(
      `${ANY}/api/v1/admin/users/:membershipId/role-change-requests`,
      async ({ request, params }) => {
        await wait(api);
        const g = gate(api, request, true);
        if ('error' in g) return g.error;
        const headers = commandHeaders(api, request);
        if ('error' in headers) return headers.error;
        const body = await readJson<Record<string, unknown>>(request);
        if (
          !body ||
          !['ASSIGN', 'REVOKE'].includes(String(body['operation'])) ||
          typeof body['configurationHash'] !== 'string' ||
          (body['operation'] === 'ASSIGN'
            ? Object.keys(body).sort().join() !==
                'configurationHash,operation,reasonCode,roleCode' ||
              !['ONBOARDING', 'DUTY_ASSIGNMENT'].includes(String(body['reasonCode']))
            : Object.keys(body).sort().join() !==
                'configurationHash,grantId,operation,reasonCode' ||
              !['ACCESS_REVIEW', 'DUTY_ENDED', 'SECURITY_CONCERN'].includes(
                String(body['reasonCode']),
              ))
        )
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'Geçersiz istek gövdesi');
        const id = pathParam(params, 'membershipId');
        const target = member(api, g.tenantId, id);
        if (!target) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        if (target.actorId === g.session.account.actorId)
          return problem(
            api,
            409,
            'SELF_ROLE_CHANGE_FORBIDDEN',
            'Kendi rolünüzü değiştiremezsiniz',
          );
        const receiptKey = `${g.tenantId}:${g.session.account.actorId}:create:${headers.key}`;
        const fingerprint = JSON.stringify([id, headers.expected, body]);
        const previous = api.roleChangeReceipts.get(receiptKey);
        if (previous)
          return previous.fingerprint === fingerprint
            ? reply(previous.result, previous.etag, previous.status)
            : problem(
                api,
                409,
                'IDEMPOTENCY_KEY_REUSED',
                'İşlem anahtarı farklı istekte kullanıldı',
              );
        const membership = api.tenantMembership(target, g.tenantId);
        if (membership.rowVersion !== headers.expected)
          return problem(api, 412, 'ETAG_MISMATCH', 'Üyelik değişti');
        const refusal = targetState(api, g.tenantId, g.session.account, target);
        if (refusal) return problem(api, 409, refusal, 'Hedef üyelik uygun değil');
        if (
          api.roleChangeRequests.some(
            (entry) =>
              entry.tenantId === g.tenantId &&
              entry.request.targetMembershipId === id &&
              entry.request.status === 'PENDING',
          )
        )
          return problem(api, 409, 'ROLE_CHANGE_PENDING_EXISTS', 'Bekleyen talep var');
        const grant =
          body['operation'] === 'REVOKE'
            ? revokeGrant(api, g.tenantId, g.session.account, target)
            : null;
        if (
          body['operation'] === 'ASSIGN' &&
          assignRefusal(api, g.tenantId, g.session.account, target)
        )
          return problem(api, 409, 'EXISTING_ACCESS_CONFLICT', 'Mevcut erişim var');
        if (
          body['operation'] === 'REVOKE' &&
          (!grant || (grant.grantId ??= crypto.randomUUID()) !== body['grantId'])
        )
          return problem(api, 409, 'GRANT_STATE_CONFLICT', 'Atama kaldırılamıyor');
        const roleCode =
          body['operation'] === 'ASSIGN' ? String(body['roleCode']) : (grant!.roleCode ?? '');
        const option = options.find((item) => item.code === roleCode);
        if (!option) return problem(api, 409, 'ROLE_ASSIGNMENT_UNSUPPORTED', 'Rol desteklenmiyor');
        if (!configured(api, option) || option.configurationHash !== body['configurationHash'])
          return problem(
            api,
            409,
            'ROLE_CHANGE_CONFIGURATION_CHANGED',
            'Rol yapılandırması değişti',
          );
        const requestId = crypto.randomUUID();
        const createdAt = new Date().toISOString();
        const proposal: Schemas['RoleChangeRequest'] = {
          id: requestId,
          operation: body['operation'] as 'ASSIGN' | 'REVOKE',
          status: 'PENDING',
          targetMembershipId: id,
          makerMembershipId: api.tenantMembership(g.session.account, g.tenantId).id,
          roleCode,
          scopeType: 'TENANT',
          permissionSnapshot: option.permissionCodes.map((code) => ({
            code,
            sensitivity: sensitivity(code),
          })),
          configurationHash: option.configurationHash,
          targetMembershipVersion: membership.rowVersion,
          revokeGrantId: grant?.grantId ?? null,
          revokeValidity: grant ? validity(grant) : null,
          reasonCode: String(body['reasonCode']),
          createdAt,
          rowVersion: 1,
          decidedAt: null,
          decidedByMembershipId: null,
          decisionReasonCode: null,
          appliedGrantId: null,
          appliedMembershipVersion: null,
          appliedValidity: null,
        };
        api.roleChangeRequests.push({
          tenantId: g.tenantId,
          makerActorId: g.session.account.actorId,
          targetActorId: target.actorId,
          request: proposal,
        });
        api.roleChangeEvents.push({ action: 'create', requestId });
        const result: Schemas['RoleChangeCommandResult'] = {
          request: structuredClone(proposal),
          appliedGrant: null,
          membershipRowVersion: null,
        };
        const etag = etagOf(1);
        api.roleChangeReceipts.set(receiptKey, {
          fingerprint,
          result: structuredClone(result),
          etag,
          status: 201,
        });
        return reply(result, etag, 201);
      },
    ),
    http.get(`${ANY}/api/v1/admin/role-change-requests`, async ({ request }) => {
      await wait(api);
      const g = gate(api, request, false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const status = url.searchParams.get('status') ?? 'PENDING';
      const membershipId = url.searchParams.get('membershipId');
      const offset = decodeCursor(url.searchParams.get('cursor'));
      const limit = parseLimit(url);
      if (
        !['PENDING', 'APPROVED', 'REJECTED', 'CANCELLED'].includes(status) ||
        offset === null ||
        limit === 'invalid'
      )
        return problem(api, 400, 'ROLE_CHANGE_QUERY_INVALID', 'Geçersiz liste isteği');
      const rows = api.roleChangeRequests
        .filter(
          (entry) =>
            entry.tenantId === g.tenantId &&
            entry.request.status === status &&
            (!membershipId || entry.request.targetMembershipId === membershipId),
        )
        .sort(
          (a, b) =>
            b.request.createdAt.localeCompare(a.request.createdAt) ||
            b.request.id.localeCompare(a.request.id),
        );
      return HttpResponse.json(
        {
          items: rows.slice(offset, offset + limit).map((entry) => summary(entry.request)),
          nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
        } satisfies Schemas['RoleChangeRequestPage'],
        { headers: { 'Cache-Control': 'no-store' } },
      );
    }),
    http.get(`${ANY}/api/v1/admin/role-change-requests/:requestId`, async ({ request, params }) => {
      await wait(api);
      const g = gate(api, request, false);
      if ('error' in g) return g.error;
      const entry = api.roleChangeRequests.find(
        (item) =>
          item.tenantId === g.tenantId && item.request.id === pathParam(params, 'requestId'),
      );
      if (!entry) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      return HttpResponse.json(entryDetail(api, entry, g.session.account), {
        headers: { ETag: etagOf(entry.request.rowVersion), 'Cache-Control': 'no-store' },
      });
    }),
    ...(['approve', 'reject', 'cancel'] as const).map((action) =>
      http.post(
        `${ANY}/api/v1/admin/role-change-requests/:requestId/${action}`,
        async ({ request, params }) => {
          await wait(api);
          const g = gate(api, request, true);
          if ('error' in g) return g.error;
          const headers = commandHeaders(api, request);
          if ('error' in headers) return headers.error;
          const body = await readJson<Record<string, unknown>>(request);
          if (
            !body ||
            (action === 'approve'
              ? Object.keys(body).length !== 0
              : Object.keys(body).join() !== 'reasonCode' ||
                !(action === 'cancel'
                  ? body['reasonCode'] === 'WITHDRAWN'
                  : ['NOT_JUSTIFIED', 'INCORRECT_ACCESS', 'STALE_REQUEST'].includes(
                      String(body['reasonCode']),
                    )))
          )
            return problem(api, 400, 'INVALID_REQUEST_BODY', 'Geçersiz istek gövdesi');
          const requestId = pathParam(params, 'requestId');
          const entry = api.roleChangeRequests.find(
            (item) => item.tenantId === g.tenantId && item.request.id === requestId,
          );
          if (!entry) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
          const caller = g.session.account;
          if (
            action === 'cancel'
              ? caller.actorId !== entry.makerActorId
              : caller.actorId === entry.makerActorId
          )
            return problem(
              api,
              403,
              action === 'cancel' ? 'ROLE_CHANGE_CANCEL_FORBIDDEN' : 'MAKER_CHECKER_SAME_ACTOR',
              'Bu kararı veremezsiniz',
            );
          if (action !== 'cancel' && caller.actorId === entry.targetActorId)
            return problem(
              api,
              403,
              'MAKER_CHECKER_SAME_ACTOR',
              'Kendi erişiminizi karara bağlayamazsınız',
            );
          const receiptKey = `${g.tenantId}:${caller.actorId}:${action}:${headers.key}`;
          const fingerprint = JSON.stringify([requestId, headers.expected, body]);
          const previous = api.roleChangeReceipts.get(receiptKey);
          if (previous)
            return previous.fingerprint === fingerprint
              ? reply(previous.result, previous.etag, previous.status)
              : problem(
                  api,
                  409,
                  'IDEMPOTENCY_KEY_REUSED',
                  'İşlem anahtarı farklı istekte kullanıldı',
                );
          const proposal = entry.request;
          if (proposal.rowVersion !== headers.expected)
            return problem(api, 412, 'ETAG_MISMATCH', 'Talep değişti');
          if (proposal.status !== 'PENDING')
            return problem(api, 409, 'ROLE_CHANGE_NOT_PENDING', 'Talep beklemiyor');
          const target = member(api, g.tenantId, proposal.targetMembershipId);
          if (!target) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
          let changedGrant: Grant | null = null;
          if (action === 'approve') {
            const maker = member(api, g.tenantId, proposal.makerMembershipId);
            if (!maker || !manager(api, maker, g.tenantId))
              return problem(
                api,
                409,
                'ROLE_CHANGE_MAKER_UNAUTHORIZED',
                'Talep edenin yetkisi sona erdi',
              );
            const membership = api.tenantMembership(target, g.tenantId);
            if (
              membership.rowVersion !== proposal.targetMembershipVersion ||
              targetState(api, g.tenantId, caller, target)
            )
              return problem(api, 409, 'ROLE_CHANGE_TARGET_CHANGED', 'Hedef üyelik değişti');
            const option = options.find((item) => item.code === proposal.roleCode);
            if (
              !option ||
              !configured(api, option) ||
              option.configurationHash !== proposal.configurationHash ||
              JSON.stringify(
                option.permissionCodes.map((code) => ({
                  code,
                  sensitivity: sensitivity(code),
                })),
              ) !== JSON.stringify(proposal.permissionSnapshot)
            )
              return problem(
                api,
                409,
                'ROLE_CHANGE_CONFIGURATION_CHANGED',
                'Rol yapılandırması değişti',
              );
            if (proposal.operation === 'ASSIGN') {
              if (assignRefusal(api, g.tenantId, caller, target))
                return problem(api, 409, 'ROLE_CHANGE_TARGET_CHANGED', 'Hedef erişim değişti');
              changedGrant = {
                tenantCode: tenantCode(api, g.tenantId),
                grantId: crypto.randomUUID(),
                roleCode: option.code,
                isSystemRole: true,
                permissions: [...option.permissionCodes],
                scopes: [{ type: 'TENANT', id: null }],
                validFrom: new Date().toISOString(),
                validTo: null,
              };
              target.memberships.push(changedGrant);
            } else {
              const grant = grants(api, g.tenantId, target).find(
                (item) => item.grantId === proposal.revokeGrantId,
              );
              if (
                !grant ||
                !current(grant) ||
                validity(grant).from !== proposal.revokeValidity?.from ||
                grants(api, g.tenantId, target).filter(future).length !== 1
              )
                return problem(api, 409, 'ROLE_CHANGE_TARGET_CHANGED', 'Hedef erişim değişti');
              grant.validTo = new Date().toISOString();
              changedGrant = grant;
            }
            membership.rowVersion += 1;
            proposal.appliedGrantId = changedGrant.grantId!;
            proposal.appliedMembershipVersion = membership.rowVersion;
            proposal.appliedValidity = validity(changedGrant);
          }
          proposal.status =
            action === 'approve' ? 'APPROVED' : action === 'reject' ? 'REJECTED' : 'CANCELLED';
          proposal.decidedAt = new Date().toISOString();
          proposal.decidedByMembershipId = api.tenantMembership(caller, g.tenantId).id;
          proposal.decisionReasonCode = action === 'approve' ? null : String(body['reasonCode']);
          proposal.rowVersion += 1;
          api.roleChangeEvents.push({ action, requestId });
          const result: Schemas['RoleChangeCommandResult'] = {
            request: structuredClone(proposal),
            appliedGrant: changedGrant ? appliedGrant(changedGrant) : null,
            membershipRowVersion: changedGrant ? proposal.appliedMembershipVersion : null,
          };
          const etag = etagOf(proposal.rowVersion);
          api.roleChangeReceipts.set(receiptKey, {
            fingerprint,
            result: structuredClone(result),
            etag,
            status: 200,
          });
          return reply(result, etag);
        },
      ),
    ),
  ];
}
