/** Consent joins a session's own account; invitation contact never resolves an actor. */
import { http, HttpResponse, type HttpHandler } from 'msw';
import { hasTenantUserPermission } from './admin-handlers';
import {
  ANY,
  appOfRequest,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardSession,
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
import { invitationProofDigest, type StoredMockInvitation } from './invitation-state';

function managerGate(api: MockApi, request: Request, mutation: boolean) {
  const permission = mutation ? 'identity.user.manage' : 'identity.user.read';
  const gate = guardTenant(api, request, permission, mutation);
  if ('error' in gate) return gate;
  const tenant = api.world.tenants.find((item) => item.id === gate.tenantId)!;
  const app = appOfRequest(request);
  if (
    !hasTenantUserPermission(gate.session.account, tenant.code, permission) ||
    (app !== null && app !== 'backoffice')
  )
    return { error: problem(api, 403, 'PERMISSION_DENIED', 'Bu işlem için yetkiniz yok') };
  if (mutation && !hasStepUp(gate.session)) return { error: stepUpRequired(api) };
  return gate;
}

function unavailable(api: MockApi) {
  return problem(api, 404, 'INVITATION_UNAVAILABLE', 'Davet kullanılamıyor');
}

function expire(api: MockApi, row: StoredMockInvitation): void {
  if (row.summary.status === 'PENDING' && Date.parse(row.summary.expiresAt) <= Date.now()) {
    row.summary.status = 'EXPIRED';
    if (row.summary.deliveryStatus !== 'SENT') row.summary.deliveryStatus = 'CANCELLED';
    row.summary.rowVersion += 1;
    row.contactIndex = null;
    row.proofDigest = '';
    row.terminalAt = Date.now();
    api.invitations.purgeDelivery(row.summary.invitationId);
  }
  if (row.terminalAt !== null && Date.now() - row.terminalAt >= 30 * 24 * 3_600_000) {
    row.summary.maskedRecipient = '';
  }
}

function invitationResponse(row: Schemas['TenantInvitation']) {
  return HttpResponse.json(row, {
    headers: { ETag: etagOf(row.rowVersion), 'Cache-Control': 'no-store' },
  });
}

async function proof(api: MockApi, code: unknown) {
  if (typeof code !== 'string' || code.length > 160) return undefined;
  const match = /^v1\.([0-9a-f-]{36})\.([0-9a-f-]{36})\.([A-Za-z0-9_-]{43})$/.exec(code);
  if (!match) return undefined;
  const row = api.invitations.rows.get(match[2]!);
  if (!row || row.tenantId !== match[1] || row.proofDigest !== (await invitationProofDigest(code)))
    return undefined;
  if (api.world.tenants.find((tenant) => tenant.id === row.tenantId)?.status !== 'ACTIVE')
    return undefined;
  expire(api, row);
  if (row.terminalAt !== null && Date.now() - row.terminalAt >= 24 * 3_600_000) return undefined;
  if (row.summary.status === 'CANCELLED' || row.summary.status === 'EXPIRED') return undefined;
  return row;
}

export function invitationHandlers(api: MockApi): HttpHandler[] {
  return [
    http.get(`${ANY}/api/v1/admin/invitations`, async ({ request }) => {
      await wait(api);
      const gate = managerGate(api, request, false);
      if ('error' in gate) return gate.error;
      const url = new URL(request.url);
      const offset = decodeCursor(url.searchParams.get('cursor'));
      const limit = parseLimit(url);
      const status = url.searchParams.get('status');
      if (
        offset === null ||
        limit === 'invalid' ||
        (status && !['PENDING', 'ACCEPTED', 'CANCELLED', 'EXPIRED'].includes(status))
      )
        return problem(api, 400, 'INVITATION_QUERY_INVALID', 'Geçersiz liste isteği');
      const rows = [...api.invitations.rows.values()].filter(
        (row) => row.tenantId === gate.tenantId,
      );
      rows.forEach((row) => expire(api, row));
      const summaries = rows
        .map((row) => row.summary)
        .filter((row) => !status || row.status === status)
        .sort(
          (a, b) =>
            b.createdAt.localeCompare(a.createdAt) || b.invitationId.localeCompare(a.invitationId),
        );
      return HttpResponse.json({
        items: summaries.slice(offset, offset + limit),
        nextCursor: offset + limit < summaries.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['TenantInvitationPage']);
    }),
    http.get(`${ANY}/api/v1/admin/invitations/:invitationId`, async ({ request, params }) => {
      await wait(api);
      const gate = managerGate(api, request, false);
      if ('error' in gate) return gate.error;
      const row = api.invitations.rows.get(pathParam(params, 'invitationId'));
      if (!row || row.tenantId !== gate.tenantId)
        return problem(api, 404, 'INVITATION_NOT_FOUND', 'Davet bulunamadı');
      expire(api, row);
      return invitationResponse(row.summary);
    }),
    http.post(`${ANY}/api/v1/admin/invitations`, async ({ request }) => {
      await wait(api);
      const gate = managerGate(api, request, true);
      if ('error' in gate) return gate.error;
      const invalidKey = requireIdempotencyKey(api, request);
      if (invalidKey) return invalidKey;
      const body = await readJson<Record<string, unknown>>(request);
      const raw = body?.['email'];
      if (typeof raw !== 'string' || Object.keys(body ?? {}).some((key) => key !== 'email'))
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'Geçersiz istek gövdesi');
      const value = raw.trim();
      const mailbox = /^([^\s@<>]+)@([A-Za-z0-9.-]+)$/.exec(value);
      if (
        !mailbox ||
        value.length > 254 ||
        mailbox[1]!.length > 64 ||
        !mailbox[2]!.includes('.') ||
        mailbox[2]!.includes('..')
      )
        return problem(api, 400, 'INVITATION_EMAIL_INVALID', 'Geçerli bir e-posta adresi yazın');
      const normalized = `${mailbox[1]}@${mailbox[2]!.toLowerCase()}`;
      const contactIndex = await api.invitations.fingerprint([
        'contact-v1',
        gate.tenantId,
        normalized,
      ]);
      const fingerprint = await api.invitations.fingerprint([
        'create-v1',
        gate.tenantId,
        gate.session.account.actorId,
        normalized,
      ]);
      const freshGate = managerGate(api, request, true);
      if ('error' in freshGate) return freshGate.error;
      if (freshGate.session !== gate.session) return unavailable(api);
      const receiptKey = `${gate.tenantId}:${gate.session.account.actorId}:${request.headers.get('Idempotency-Key')}`;
      let receipt = api.invitations.createReceipts.get(receiptKey);
      if (receipt && Date.now() - Date.parse(receipt.summary.createdAt) >= 24 * 3_600_000) {
        api.invitations.createReceipts.delete(receiptKey);
        receipt = undefined;
      }
      if (receipt) {
        if (receipt.fingerprint !== fingerprint)
          return problem(
            api,
            409,
            'IDEMPOTENCY_KEY_REUSED',
            'Komut anahtarı farklı bir işlemde kullanıldı',
          );
        return invitationResponse(receipt.summary);
      }
      for (const row of api.invitations.rows.values()) {
        expire(api, row);
        if (
          row.tenantId === gate.tenantId &&
          row.contactIndex === contactIndex &&
          row.summary.status === 'PENDING'
        )
          return problem(
            api,
            409,
            'INVITATION_PENDING_EXISTS',
            'Bu alıcı için bekleyen bir davet var',
          );
      }
      const invitationId = crypto.randomUUID();
      const secret = btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(32))))
        .replace(/\+/g, '-')
        .replace(/\//g, '_')
        .replace(/=+$/, '');
      const code = `v1.${gate.tenantId}.${invitationId}.${secret}`;
      const digest = await invitationProofDigest(code);
      const commitGate = managerGate(api, request, true);
      if ('error' in commitGate) return commitGate.error;
      if (commitGate.session !== gate.session) return unavailable(api);
      // Recheck after asynchronous proof generation before the synchronous commit below.
      if (api.invitations.createReceipts.has(receiptKey)) {
        const existing = api.invitations.createReceipts.get(receiptKey)!;
        return existing.fingerprint === fingerprint
          ? invitationResponse(existing.summary)
          : problem(
              api,
              409,
              'IDEMPOTENCY_KEY_REUSED',
              'Komut anahtarı farklı bir işlemde kullanıldı',
            );
      }
      if (
        [...api.invitations.rows.values()].some(
          (row) =>
            row.tenantId === gate.tenantId &&
            row.contactIndex === contactIndex &&
            row.summary.status === 'PENDING',
        )
      )
        return problem(
          api,
          409,
          'INVITATION_PENDING_EXISTS',
          'Bu alıcı için bekleyen bir davet var',
        );
      const summary: Schemas['TenantInvitation'] = {
        invitationId,
        maskedRecipient: `${mailbox[1]!.slice(0, 1)}***@***`,
        status: 'PENDING',
        createdAt: new Date().toISOString(),
        expiresAt: new Date(Date.now() + 48 * 3_600_000).toISOString(),
        rowVersion: 1,
        deliveryStatus: 'SENT',
      };
      api.invitations.rows.set(invitationId, {
        summary,
        tenantId: gate.tenantId,
        contactIndex,
        proofDigest: digest,
        acceptedActorId: null,
        acceptedKey: null,
        acceptedOutcome: null,
        terminalAt: null,
      });
      api.invitations.createReceipts.set(receiptKey, {
        fingerprint,
        summary: structuredClone(summary),
      });
      api.invitations.events.push({ action: 'create', invitationId });
      api.invitations.deliver(invitationId, code);
      return invitationResponse(summary);
    }),
    http.post(
      `${ANY}/api/v1/admin/invitations/:invitationId/cancel`,
      async ({ request, params }) => {
        await wait(api);
        const gate = managerGate(api, request, true);
        if ('error' in gate) return gate.error;
        const invalidKey = requireIdempotencyKey(api, request);
        if (invalidKey) return invalidKey;
        const version = requireIfMatch(api, request);
        if (version instanceof Response) return version;
        if (request.headers.get('If-Match') !== etagOf(version))
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        const rawBody = await request.text();
        if (rawBody.length !== 0)
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'Geçersiz istek gövdesi');
        const id = pathParam(params, 'invitationId');
        const receiptKey = `${gate.tenantId}:${gate.session.account.actorId}:${id}:${request.headers.get('Idempotency-Key')}`;
        const receipt = api.invitations.cancelReceipts.get(receiptKey);
        if (receipt)
          return receipt.version === version
            ? invitationResponse(receipt.summary)
            : problem(
                api,
                409,
                'IDEMPOTENCY_KEY_REUSED',
                'Komut anahtarı farklı bir işlemde kullanıldı',
              );
        const row = api.invitations.rows.get(id);
        if (!row || row.tenantId !== gate.tenantId)
          return problem(api, 404, 'INVITATION_NOT_FOUND', 'Davet bulunamadı');
        expire(api, row);
        if (row.summary.rowVersion !== version)
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt değişti');
        if (row.summary.status !== 'PENDING')
          return problem(api, 409, 'INVITATION_STATE_CONFLICT', 'Davet bu durumda iptal edilemez');
        row.summary.status = 'CANCELLED';
        row.terminalAt = Date.now();
        if (row.summary.deliveryStatus !== 'SENT') row.summary.deliveryStatus = 'CANCELLED';
        row.summary.rowVersion += 1;
        row.contactIndex = null;
        row.proofDigest = '';
        api.invitations.purgeDelivery(id);
        api.invitations.events.push({ action: 'cancel', invitationId: id });
        api.invitations.cancelReceipts.set(receiptKey, {
          version,
          summary: structuredClone(row.summary),
        });
        return invitationResponse(row.summary);
      },
    ),
    http.post(`${ANY}/api/v1/invitations/inspect`, async ({ request }) => {
      await wait(api);
      const session = guardSession(api, request, true);
      if (session instanceof Response) return session;
      const body = await readJson<Record<string, unknown>>(request);
      if (Object.keys(body ?? {}).some((key) => key !== 'code')) return unavailable(api);
      const row = await proof(api, body?.['code']);
      if (
        !row ||
        api.session !== session ||
        (session.account.actorStatus ?? 'ACTIVE') !== 'ACTIVE' ||
        (row.summary.status === 'ACCEPTED' && row.acceptedActorId !== session.account.actorId)
      )
        return unavailable(api);
      return HttpResponse.json(
        {
          tenantDisplayName: api.world.tenants.find((item) => item.id === row.tenantId)!
            .displayName,
          invitationStatus: row.summary.status,
          expiresAt: row.summary.expiresAt,
        } satisfies Schemas['InspectInvitationResponse'],
        { headers: { 'Cache-Control': 'no-store' } },
      );
    }),
    http.post(`${ANY}/api/v1/invitations/accept-existing`, async ({ request }) => {
      await wait(api);
      const session = guardSession(api, request, true);
      if (session instanceof Response) return session;
      const invalidKey = requireIdempotencyKey(api, request);
      if (invalidKey) return invalidKey;
      const body = await readJson<Record<string, unknown>>(request);
      if (
        body?.['confirmed'] !== true ||
        Object.keys(body).some((key) => !['code', 'confirmed'].includes(key))
      )
        return problem(api, 400, 'INVITATION_CONFIRMATION_REQUIRED', 'Kuruma katılmayı onaylayın');
      const row = await proof(api, body['code']);
      if (!row || api.session !== session || (session.account.actorStatus ?? 'ACTIVE') !== 'ACTIVE')
        return unavailable(api);
      const key = request.headers.get('Idempotency-Key')!;
      if (row.summary.status === 'ACCEPTED') {
        if (row.acceptedActorId !== session.account.actorId) return unavailable(api);
        if (row.acceptedKey !== key)
          return problem(api, 409, 'IDEMPOTENCY_KEY_REUSED', 'Davet zaten kabul edildi');
        return HttpResponse.json(row.acceptedOutcome, { headers: { 'Cache-Control': 'no-store' } });
      }
      const account = session.account;
      const tenant = api.world.tenants.find((item) => item.id === row.tenantId)!;
      const existing = account.memberships.some((grant) => grant.tenantCode === tenant.code);
      if (existing && !api.membershipIsActive(account, tenant.id))
        return problem(
          api,
          409,
          'INVITATION_MEMBERSHIP_CONFLICT',
          'Mevcut üyelik bu işlemle etkinleştirilemez',
        );
      if (!existing)
        account.memberships.push({
          tenantCode: tenant.code,
          permissions: [],
          membershipOnly: true,
        });
      const outcome: Schemas['AcceptExistingInvitationResponse'] = {
        tenantId: tenant.id,
        tenantDisplayName: tenant.displayName,
        membershipId: api.tenantMembership(account, tenant.id).id,
        membershipStatus: 'ACTIVE',
        accessPending: !account.memberships.some(
          (grant) =>
            grant.tenantCode === tenant.code &&
            grant.permissions.length > 0 &&
            !grant.validityEmpty &&
            withinPeriod(new Date().toISOString(), grant.validFrom ?? null, grant.validTo ?? null),
        ),
      };
      row.summary.status = 'ACCEPTED';
      if (row.summary.deliveryStatus !== 'SENT') row.summary.deliveryStatus = 'CANCELLED';
      row.terminalAt = Date.now();
      row.summary.rowVersion += 1;
      row.contactIndex = null;
      row.acceptedActorId = account.actorId;
      row.acceptedKey = key;
      row.acceptedOutcome = outcome;
      api.invitations.purgeDelivery(row.summary.invitationId);
      api.invitations.events.push({ action: 'accept', invitationId: row.summary.invitationId });
      return HttpResponse.json(outcome, { headers: { 'Cache-Control': 'no-store' } });
    }),
  ];
}
