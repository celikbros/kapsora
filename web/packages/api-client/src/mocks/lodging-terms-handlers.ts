import { HttpResponse, http, type HttpHandler } from 'msw';

import type { StoredLodgingTerms } from './data';
import type { MockApi } from './handlers';
import {
  ANY,
  etagOf,
  guardTenant,
  pathParam,
  problem,
  readJson,
  requireIfMatch,
  validationFailed,
  wait,
  type FieldError,
  type Schemas,
} from './handlers';

/**
 * WP-I6-04's lodging terms on the contract version: written on a DRAFT, frozen everywhere
 * else, and copied onto every booking confirmed under the version. The If-Match is the
 * version's own row version, because the terms are part of the version and the write locks
 * the draft, exactly as the server does.
 */
export function lodgingTermsHandlers(api: MockApi): HttpHandler[] {
  const world = () => api.world;

  const toView = (row: StoredLodgingTerms): Schemas['LodgingTerms'] => ({
    id: row.id,
    contractVersionId: row.contractVersionId,
    freeCancellationHoursBefore: row.freeCancellationHoursBefore,
    penaltyKind: row.penaltyKind,
    penaltyNights: row.penaltyNights,
    penaltyPercent: row.penaltyPercent,
    noShowPercent: row.noShowPercent,
    holdMinutes: row.holdMinutes,
    minNights: row.minNights,
    maxNights: row.maxNights,
    childFreeUnderAge: row.childFreeUnderAge,
    rowVersion: row.rowVersion,
  });

  const findVersion = (tenantId: string, id: string) =>
    world().contractVersions.find((v) => v.tenantId === tenantId && v.id === id) ?? null;
  const findTerms = (tenantId: string, versionId: string) =>
    world().lodgingTerms.find(
      (x) => x.tenantId === tenantId && x.contractVersionId === versionId,
    ) ?? null;

  function validate(body: Schemas['PutLodgingTermsRequest']): FieldError[] {
    const errors: FieldError[] = [];
    if (
      !Number.isInteger(body.freeCancellationHoursBefore) ||
      body.freeCancellationHoursBefore < 0
    ) {
      errors.push({ field: 'freeCancellationHoursBefore', code: 'RANGE' });
    }
    if (body.penaltyKind !== 'NIGHTS' && body.penaltyKind !== 'PERCENT') {
      errors.push({ field: 'penaltyKind', code: 'ENUM' });
    }
    if (
      body.penaltyKind === 'NIGHTS' &&
      (body.penaltyNights === null || body.penaltyNights === undefined)
    ) {
      errors.push({ field: 'penaltyNights', code: 'REQUIRED' });
    }
    if (body.penaltyKind === 'PERCENT' && !body.penaltyPercent) {
      errors.push({ field: 'penaltyPercent', code: 'REQUIRED' });
    }
    if (typeof body.noShowPercent !== 'string' || !/^\d+(\.\d{1,4})?$/.test(body.noShowPercent)) {
      errors.push({ field: 'noShowPercent', code: 'FORMAT' });
    }
    if (!Number.isInteger(body.minNights) || body.minNights < 1) {
      errors.push({ field: 'minNights', code: 'RANGE' });
    }
    if (
      body.maxNights !== null &&
      body.maxNights !== undefined &&
      body.maxNights < body.minNights
    ) {
      errors.push({ field: 'maxNights', code: 'RANGE' });
    }
    return errors;
  }

  return [
    http.get(
      `${ANY}/api/v1/contract-versions/:contractVersionId/lodging-terms`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.read', false);
        if ('error' in g) return g.error;
        const versionId = pathParam(params, 'contractVersionId');
        if (!findVersion(g.tenantId, versionId)) {
          return problem(api, 404, 'CONTRACT_VERSION_NOT_FOUND', 'Sözleşme sürümü bulunamadı');
        }
        const row = findTerms(g.tenantId, versionId);
        if (!row) {
          return problem(api, 404, 'LODGING_TERMS_NOT_FOUND', 'Konaklama koşulu yazılmamış');
        }
        return HttpResponse.json(toView(row), { headers: { ETag: etagOf(row.rowVersion) } });
      },
    ),

    http.put(
      `${ANY}/api/v1/contract-versions/:contractVersionId/lodging-terms`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.manage', true);
        if ('error' in g) return g.error;
        const versionId = pathParam(params, 'contractVersionId');
        const version = findVersion(g.tenantId, versionId);
        if (!version) {
          return problem(api, 404, 'CONTRACT_VERSION_NOT_FOUND', 'Sözleşme sürümü bulunamadı');
        }
        const expected = requireIfMatch(api, request);
        if (typeof expected !== 'number') return expected;
        if (version.status !== 'DRAFT') {
          return problem(api, 409, 'CONTRACT_VERSION_IMMUTABLE', 'Sürüm taslak değil', {
            detail: 'Konaklama koşulları yalnızca taslak sürümde yazılır.',
          });
        }
        if (expected !== version.rowVersion) {
          return problem(api, 409, 'VERSION_CONFLICT', 'Kayıt bu arada değişti', {
            detail: 'Sürümü yeniden yükleyip tekrar deneyin.',
          });
        }
        const body = await readJson<Schemas['PutLodgingTermsRequest']>(request);
        if (!body) return validationFailed(api, [{ field: 'body', code: 'REQUIRED' }]);
        const errors = validate(body);
        if (errors.length > 0) return validationFailed(api, errors);
        let row = findTerms(g.tenantId, versionId);
        if (!row) {
          row = {
            id: world().nextId(),
            tenantId: g.tenantId,
            contractVersionId: versionId,
            freeCancellationHoursBefore: 0,
            penaltyKind: 'NIGHTS',
            penaltyNights: 0,
            penaltyPercent: null,
            noShowPercent: '0',
            holdMinutes: null,
            minNights: 1,
            maxNights: null,
            childFreeUnderAge: null,
            rowVersion: 0,
          };
          world().lodgingTerms.push(row);
        }
        row.freeCancellationHoursBefore = body.freeCancellationHoursBefore;
        row.penaltyKind = body.penaltyKind;
        row.penaltyNights = body.penaltyKind === 'NIGHTS' ? (body.penaltyNights ?? 0) : null;
        row.penaltyPercent = body.penaltyKind === 'PERCENT' ? (body.penaltyPercent ?? null) : null;
        row.noShowPercent = body.noShowPercent;
        row.holdMinutes = body.holdMinutes ?? null;
        row.minNights = body.minNights;
        row.maxNights = body.maxNights ?? null;
        row.childFreeUnderAge = body.childFreeUnderAge ?? null;
        row.rowVersion += 1;
        // The write locks the draft version, so its row version moves with the terms.
        version.rowVersion += 1;
        return HttpResponse.json(toView(row), { headers: { ETag: etagOf(row.rowVersion) } });
      },
    ),

    http.get(
      `${ANY}/api/v1/contract-versions/:contractVersionId/lodging-policy`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.read', false);
        if ('error' in g) return g.error;
        const versionId = pathParam(params, 'contractVersionId');
        const row = findTerms(g.tenantId, versionId);
        if (!row) {
          return problem(api, 404, 'LODGING_TERMS_NOT_FOUND', 'Konaklama koşulu yazılmamış');
        }
        const snapshot: Schemas['LodgingPolicySnapshot'] = {
          contractVersionId: row.contractVersionId,
          snapshotAt: new Date().toISOString(),
          timezone: 'Europe/Istanbul',
          freeCancellationHoursBefore: row.freeCancellationHoursBefore,
          penaltyKind: row.penaltyKind,
          penaltyNights: row.penaltyNights,
          penaltyPercent: row.penaltyPercent,
          noShowPercent: row.noShowPercent,
          minNights: row.minNights,
          maxNights: row.maxNights,
          childFreeUnderAge: row.childFreeUnderAge,
        };
        return HttpResponse.json(snapshot);
      },
    ),
  ];
}
