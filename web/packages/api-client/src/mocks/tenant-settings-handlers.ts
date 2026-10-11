import { HttpResponse, http, type HttpHandler } from 'msw';
import { hasTenantUserPermission } from './admin-handlers';
import {
  appOfRequest,
  guardTenant,
  hasStepUp,
  problem,
  requireIdempotencyKey,
  stepUpRequired,
  wait,
  type MockApi,
  type Schemas,
} from './handlers';

type Field = 'holdMinutes' | 'quoteTtlMinutes';
type Body = Schemas['SetAccommodationTenantSettingsRequest'];
const path = '*/api/v1/admin/tenant-settings/accommodation';
const defaults: Record<Field, number> = { holdMinutes: 15, quoteTtlMinutes: 60 };

function state(api: MockApi, tenantId: string) {
  return api.accommodationSettings.get(tenantId);
}
function etag(api: MockApi, tenantId: string) {
  return `"accommodation-v${state(api, tenantId)?.version ?? 0}"`;
}
function projection(
  api: MockApi,
  tenantId: string,
  canManage: boolean,
): Schemas['AccommodationTenantSettings'] {
  const saved = state(api, tenantId);
  const field = (key: Field): Schemas['TenantSettingInteger'] => ({
    effectiveValue: saved?.[key].value ?? defaults[key],
    defaultValue: defaults[key],
    minimum: 1,
    maximum: 1440,
    source: saved?.[key].source ?? 'DEFAULT',
    updatedAt: saved?.[key].updatedAt ?? null,
  });
  return {
    holdMinutes: field('holdMinutes'),
    quoteTtlMinutes: field('quoteTtlMinutes'),
    canManage,
  };
}
function response(body: Schemas['AccommodationTenantSettings'], version: string) {
  return HttpResponse.json(body, { headers: { ETag: version, 'Cache-Control': 'no-store' } });
}
function gate(api: MockApi, request: Request, mutation: boolean) {
  const guarded = guardTenant(api, request, 'platform.tenant_settings.read', mutation);
  if ('error' in guarded) return guarded;
  const tenant = api.world.tenants.find((item) => item.id === guarded.tenantId)!;
  const account = guarded.session.account;
  if (
    (appOfRequest(request) !== null && appOfRequest(request) !== 'backoffice') ||
    !hasTenantUserPermission(account, tenant.code, 'platform.tenant_settings.read') ||
    (account.actorType ?? 'HUMAN') !== 'HUMAN'
  )
    return { error: problem(api, 403, 'PERMISSION_DENIED', 'Bu işlem için yetkiniz yok') };
  if (mutation && !hasTenantUserPermission(account, tenant.code, 'platform.tenant_settings.manage'))
    return { error: problem(api, 403, 'PERMISSION_DENIED', 'Bu işlem için yetkiniz yok') };
  return guarded;
}
function validBody(body: unknown): body is Body {
  if (!body || typeof body !== 'object' || Array.isArray(body)) return false;
  const record = body as Record<string, unknown>;
  if (Object.keys(record).sort().join('|') !== 'holdMinutes|quoteTtlMinutes|reasonCode')
    return false;
  return (
    (['holdMinutes', 'quoteTtlMinutes'] as const).every(
      (field) =>
        typeof record[field] === 'number' &&
        Number.isSafeInteger(record[field]) &&
        (record[field] as number) >= 1 &&
        (record[field] as number) <= 1440,
    ) &&
    (record.reasonCode === 'OPERATING_REQUIREMENT' || record.reasonCode === 'CORRECT_CONFIGURATION')
  );
}

export function tenantSettingsHandlers(api: MockApi): HttpHandler[] {
  return [
    http.get(path, async ({ request }) => {
      await wait(api);
      const guarded = gate(api, request, false);
      if ('error' in guarded) return guarded.error;
      const tenant = api.world.tenants.find((item) => item.id === guarded.tenantId)!;
      const canManage = hasTenantUserPermission(
        guarded.session.account,
        tenant.code,
        'platform.tenant_settings.manage',
      );
      return response(projection(api, guarded.tenantId, canManage), etag(api, guarded.tenantId));
    }),
    http.put(path, async ({ request }) => {
      await wait(api);
      const guarded = gate(api, request, true);
      if ('error' in guarded) return guarded.error;
      if (!hasStepUp(guarded.session)) return stepUpRequired(api);
      const keyError = requireIdempotencyKey(api, request);
      if (keyError) return keyError;
      const ifMatch = request.headers.get('If-Match');
      if (!ifMatch || !/^"[^"\r\n]+"$/.test(ifMatch))
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'Güçlü If-Match sürümü gerekli');
      const bodyText = await request.text();
      let parsed: unknown;
      try {
        parsed = JSON.parse(bodyText);
      } catch {
        parsed = null;
      }
      if (!validBody(parsed))
        return problem(
          api,
          422,
          'TENANT_SETTINGS_INVALID',
          'Konaklama ayarlarını ve gerekçeyi kontrol edin',
        );
      const key = request.headers.get('Idempotency-Key')!;
      const receiptKey = [guarded.tenantId, guarded.session.account.actorId, key].join(':');
      const fingerprint = JSON.stringify([bodyText, ifMatch]);
      const prior = api.tenantSettingsReceipts.get(receiptKey);
      if (prior) {
        if (prior.fingerprint !== fingerprint)
          return problem(
            api,
            409,
            'IDEMPOTENCY_KEY_REUSED',
            'İstek anahtarı farklı içerikle kullanıldı',
          );
        return response(prior.result, prior.etag);
      }
      if (ifMatch !== etag(api, guarded.tenantId))
        return problem(
          api,
          412,
          'TENANT_SETTINGS_VERSION_CONFLICT',
          'Ayarlar değişti; yeniden yükleyin',
        );
      const priorState = state(api, guarded.tenantId);
      const changed =
        !priorState ||
        (['holdMinutes', 'quoteTtlMinutes'] as const).some(
          (field) =>
            priorState[field].source !== 'CONFIGURED' || priorState[field].value !== parsed[field],
        );
      if (changed) {
        const now = new Date().toISOString();
        api.accommodationSettings.set(guarded.tenantId, {
          holdMinutes: { value: parsed.holdMinutes, source: 'CONFIGURED', updatedAt: now },
          quoteTtlMinutes: { value: parsed.quoteTtlMinutes, source: 'CONFIGURED', updatedAt: now },
          version: (priorState?.version ?? 0) + 1,
        });
        api.tenantSettingsEvents.push({
          tenantId: guarded.tenantId,
          actorId: guarded.session.account.actorId,
          reasonCode: parsed.reasonCode,
        });
      }
      const result = projection(api, guarded.tenantId, true);
      const version = etag(api, guarded.tenantId);
      api.tenantSettingsReceipts.set(receiptKey, { fingerprint, result, etag: version });
      return response(result, version);
    }),
  ];
}
