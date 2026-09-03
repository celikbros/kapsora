/**
 * MSW handlers for the M2 benefit surface: programs, plans, plan versions with their
 * entitlement definitions, and enrollments. They enforce the same rules as the Go API,
 * because the screens are built against these responses: maker-checker publishing,
 * immutable published versions, non-overlapping published periods and enrollments that
 * need a published version on their start date.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  pseudoHash,
  toEnrollment,
  toPlan,
  toPlanVersion,
  toPlanVersionSummary,
  toProgram,
  type MockWorld,
  type StoredPlan,
  type StoredPlanVersion,
  type StoredProgram,
} from './data';
import type {
  MockApi} from './handlers';
import {
  ANY,
  encodeCursor,
  decodeCursor,
  etagOf,
  guardTenant,
  parseIfMatch,
  parseLimit,
  pathParam,
  problem,
  readJson,
  wait,
  type Schemas,
} from './handlers';

const PROGRAM_TRANSITIONS: Record<string, string[]> = {
  DRAFT: ['ACTIVE'],
  ACTIVE: ['SUSPENDED', 'CLOSED'],
  SUSPENDED: ['ACTIVE', 'CLOSED'],
  CLOSED: [],
};
const PLAN_TRANSITIONS: Record<string, string[]> = {
  DRAFT: ['ACTIVE'],
  ACTIVE: ['RETIRED'],
  RETIRED: [],
};
const CODE = /^[A-Z][A-Z0-9_-]{1,39}$/;

/** True when `date` falls inside the half-open period of a published version. */
function covers(version: StoredPlanVersion, date: string): boolean {
  if (version.status !== 'PUBLISHED' || version.validFrom === null) return false;
  if (date < version.validFrom) return false;
  return version.validTo === null || date < version.validTo;
}

/** Two half-open periods overlap when each starts before the other ends. */
function periodsOverlap(
  aFrom: string,
  aTo: string | null,
  bFrom: string,
  bTo: string | null,
): boolean {
  return (aTo === null || bFrom < aTo) && (bTo === null || aFrom < bTo);
}

export function benefitHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  const findProgram = (tenantId: string, id: string): StoredProgram | undefined =>
    world().programs.find((p) => p.id === id && p.tenantId === tenantId);
  const findPlan = (tenantId: string, id: string): StoredPlan | undefined =>
    world().plans.find((p) => p.id === id && p.tenantId === tenantId);
  const findVersion = (tenantId: string, id: string): StoredPlanVersion | undefined =>
    world().planVersions.find((v) => v.id === id && v.tenantId === tenantId);

  /** Rejects a body that is not a merge-patch document. */
  const requireMergePatch = (request: Request): Response | null => {
    const type = (request.headers.get('Content-Type') ?? '').toLowerCase();
    return type.startsWith('application/merge-patch+json')
      ? null
      : problem(
          api,
          415,
          'UNSUPPORTED_MEDIA_TYPE',
          'Content-Type application/merge-patch+json olmalı',
        );
  };

  return [
    // --- programs ---------------------------------------------------------------
    http.get(`${ANY}/api/v1/programs`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'program.read', false);
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
      const status = url.searchParams.get('status');
      const q = (url.searchParams.get('q') ?? '').trim().toLocaleLowerCase('tr');
      const rows = world()
        .programs.filter((p) => p.tenantId === g.tenantId)
        .filter((p) => !status || p.status === status)
        .filter(
          (p) =>
            !q ||
            p.code.toLocaleLowerCase('tr').includes(q) ||
            p.name.toLocaleLowerCase('tr').includes(q),
        );
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map((p) => toProgram(world(), p)),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['ProgramPage']);
    }),

    http.post(`${ANY}/api/v1/programs`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'program.manage', true);
      if ('error' in g) return g.error;
      const body = await readJson<Schemas['CreateProgramRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors: { field: string; code: string; message?: string }[] = [];
      if (!body.code || !CODE.test(body.code)) errors.push({ field: 'code', code: 'FORMAT' });
      if (!body.name || [...body.name].length < 2) errors.push({ field: 'name', code: 'LENGTH' });
      if (!body.programType) errors.push({ field: 'programType', code: 'REQUIRED' });
      for (const [field, id, role] of [
        ['sponsorOrganizationId', body.sponsorOrganizationId, 'SPONSOR'],
        ['payerOrganizationId', body.payerOrganizationId, 'PAYER'],
      ] as const) {
        const rel = world().relationships.find((r) => r.id === id && r.tenantId === g.tenantId);
        if (!rel || (rel.relationshipRole !== role && rel.relationshipRole !== 'PAYER')) {
          errors.push({ field, code: 'SPONSOR_ORGANIZATION_INVALID' });
        }
      }
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (world().programs.some((p) => p.tenantId === g.tenantId && p.code === body.code)) {
        return problem(api, 409, 'PROGRAM_CODE_TAKEN', 'Bu program kodu zaten kullanılıyor');
      }
      const program: StoredProgram = {
        id: world().nextId(),
        tenantId: g.tenantId,
        code: body.code,
        name: body.name,
        programType: body.programType,
        sponsorOrganizationId: body.sponsorOrganizationId,
        payerOrganizationId: body.payerOrganizationId,
        status: 'DRAFT',
        validFrom: body.validFrom ?? null,
        validTo: body.validTo ?? null,
        rowVersion: 1,
      };
      world().programs.push(program);
      return HttpResponse.json(toProgram(world(), program), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.get(`${ANY}/api/v1/programs/:programId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'program.read', false);
      if ('error' in g) return g.error;
      const program = findProgram(g.tenantId, pathParam(params, 'programId'));
      if (!program) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      return HttpResponse.json(toProgram(world(), program), {
        headers: { ETag: etagOf(program.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/programs/:programId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'program.manage', true);
      if ('error' in g) return g.error;
      const unsupported = requireMergePatch(request);
      if (unsupported) return unsupported;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null)
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      const program = findProgram(g.tenantId, pathParam(params, 'programId'));
      if (!program) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      if (program.rowVersion !== expected)
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      const patch = await readJson<Schemas['UpdateProgramRequest']>(request);
      if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      if (patch.status && !(PROGRAM_TRANSITIONS[program.status] ?? []).includes(patch.status)) {
        return problem(api, 409, 'PROGRAM_TRANSITION_INVALID', 'Program bu duruma geçemez');
      }
      if (patch.name !== undefined) program.name = patch.name;
      if (patch.status !== undefined) program.status = patch.status;
      if ('validFrom' in patch) program.validFrom = patch.validFrom ?? null;
      if ('validTo' in patch) program.validTo = patch.validTo ?? null;
      program.rowVersion += 1;
      return HttpResponse.json(toProgram(world(), program), {
        headers: { ETag: etagOf(program.rowVersion) },
      });
    }),

    // --- plans ------------------------------------------------------------------
    http.get(`${ANY}/api/v1/programs/:programId/plans`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'program.read', false);
      if ('error' in g) return g.error;
      const program = findProgram(g.tenantId, pathParam(params, 'programId'));
      if (!program) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const items = world()
        .plans.filter((p) => p.tenantId === g.tenantId && p.programId === program.id)
        .map((p) => toPlan(world(), p));
      return HttpResponse.json({ items });
    }),

    http.post(`${ANY}/api/v1/programs/:programId/plans`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'plan.manage', true);
      if ('error' in g) return g.error;
      const program = findProgram(g.tenantId, pathParam(params, 'programId'));
      if (!program) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const body = await readJson<Schemas['CreatePlanRequest']>(request);
      if (!body?.code || !CODE.test(body.code) || !body.name) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'code', code: 'FORMAT' }],
        });
      }
      if (
        world().plans.some(
          (p) => p.tenantId === g.tenantId && p.programId === program.id && p.code === body.code,
        )
      ) {
        return problem(api, 409, 'PLAN_CODE_TAKEN', 'Bu plan kodu programda zaten var');
      }
      const plan: StoredPlan = {
        id: world().nextId(),
        tenantId: g.tenantId,
        programId: program.id,
        code: body.code,
        name: body.name,
        status: 'DRAFT',
        rowVersion: 1,
      };
      world().plans.push(plan);
      return HttpResponse.json(toPlan(world(), plan), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.get(`${ANY}/api/v1/plans/:planId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'program.read', false);
      if ('error' in g) return g.error;
      const plan = findPlan(g.tenantId, pathParam(params, 'planId'));
      if (!plan) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      return HttpResponse.json(toPlan(world(), plan), {
        headers: { ETag: etagOf(plan.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/plans/:planId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'plan.manage', true);
      if ('error' in g) return g.error;
      const unsupported = requireMergePatch(request);
      if (unsupported) return unsupported;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null)
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      const plan = findPlan(g.tenantId, pathParam(params, 'planId'));
      if (!plan) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      if (plan.rowVersion !== expected)
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      const patch = await readJson<Schemas['UpdatePlanRequest']>(request);
      if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      if (patch.status && !(PLAN_TRANSITIONS[plan.status] ?? []).includes(patch.status)) {
        return problem(api, 409, 'PLAN_TRANSITION_INVALID', 'Plan bu duruma geçemez');
      }
      if (patch.name !== undefined) plan.name = patch.name;
      if (patch.status !== undefined) plan.status = patch.status;
      plan.rowVersion += 1;
      return HttpResponse.json(toPlan(world(), plan), {
        headers: { ETag: etagOf(plan.rowVersion) },
      });
    }),

    // --- plan versions ----------------------------------------------------------
    http.get(`${ANY}/api/v1/plans/:planId/versions`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'program.read', false);
      if ('error' in g) return g.error;
      const plan = findPlan(g.tenantId, pathParam(params, 'planId'));
      if (!plan) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const items = world()
        .planVersions.filter((v) => v.tenantId === g.tenantId && v.planId === plan.id)
        .sort((a, b) => b.versionNo - a.versionNo)
        .map(toPlanVersionSummary);
      return HttpResponse.json({ items });
    }),

    http.post(`${ANY}/api/v1/plans/:planId/versions`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'plan.manage', true);
      if ('error' in g) return g.error;
      const plan = findPlan(g.tenantId, pathParam(params, 'planId'));
      if (!plan) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      const body = (await readJson<Schemas['CreatePlanVersionRequest']>(request)) ?? {};
      const versions = world().planVersions.filter(
        (v) => v.tenantId === g.tenantId && v.planId === plan.id,
      );
      let definitions: StoredPlanVersion['definitions'] = [];
      if (body.copyFromVersionId) {
        const source = versions.find((v) => v.id === body.copyFromVersionId);
        if (!source) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: [{ field: 'copyFromVersionId', code: 'NOT_FOUND' }],
          });
        }
        definitions = source.definitions.map((d) => ({ ...d, id: world().nextId() }));
      }
      const version: StoredPlanVersion = {
        id: world().nextId(),
        tenantId: g.tenantId,
        planId: plan.id,
        versionNo: versions.reduce((max, v) => Math.max(max, v.versionNo), 0) + 1,
        status: 'DRAFT',
        validFrom: body.validFrom ?? null,
        validTo: body.validTo ?? null,
        notes: body.notes ?? null,
        definitions,
        configurationHash: null,
        publishedAt: null,
        publishedBy: null,
        submittedAt: null,
        submittedBy: null,
        retireReasonCode: null,
        reviewComment: null,
        rowVersion: 1,
      };
      world().planVersions.push(version);
      return HttpResponse.json(toPlanVersion(version), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.get(`${ANY}/api/v1/plan-versions/:planVersionId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'program.read', false);
      if ('error' in g) return g.error;
      const version = findVersion(g.tenantId, pathParam(params, 'planVersionId'));
      if (!version) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      return HttpResponse.json(toPlanVersion(version), {
        headers: { ETag: etagOf(version.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/plan-versions/:planVersionId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'plan.manage', true);
      if ('error' in g) return g.error;
      const unsupported = requireMergePatch(request);
      if (unsupported) return unsupported;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null)
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      const version = findVersion(g.tenantId, pathParam(params, 'planVersionId'));
      if (!version) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      if (version.status !== 'DRAFT') {
        return problem(api, 409, 'PLAN_VERSION_IMMUTABLE', 'Yalnız taslak sürüm değiştirilebilir');
      }
      if (version.rowVersion !== expected)
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      const patch = await readJson<Schemas['UpdatePlanVersionRequest']>(request);
      if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      if ('validFrom' in patch) version.validFrom = patch.validFrom ?? null;
      if ('validTo' in patch) version.validTo = patch.validTo ?? null;
      if ('notes' in patch) version.notes = patch.notes ?? null;
      version.rowVersion += 1;
      return HttpResponse.json(toPlanVersion(version), {
        headers: { ETag: etagOf(version.rowVersion) },
      });
    }),

    http.put(
      `${ANY}/api/v1/plan-versions/:planVersionId/entitlement-definitions`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'plan.manage', true);
        if ('error' in g) return g.error;
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null)
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        const version = findVersion(g.tenantId, pathParam(params, 'planVersionId'));
        if (!version) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
        if (version.status !== 'DRAFT') {
          return problem(
            api,
            409,
            'PLAN_VERSION_IMMUTABLE',
            'Yalnız taslak sürümün hak tanımları değiştirilebilir',
          );
        }
        if (version.rowVersion !== expected)
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        const body = await readJson<{ items: Schemas['EntitlementDefinitionInput'][] }>(request);
        if (!body?.items)
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        const errors: { field: string; code: string }[] = [];
        const seen = new Set<string>();
        body.items.forEach((d, i) => {
          if (!d.code || seen.has(d.code))
            errors.push({ field: `items[${i}].code`, code: 'FORMAT' });
          seen.add(d.code);
          if (d.unitType === 'MONEY' && !d.currencyCode) {
            errors.push({ field: `items[${i}].currencyCode`, code: 'CURRENCY_REQUIRED' });
          }
          if (d.unitType !== 'MONEY' && d.currencyCode) {
            errors.push({ field: `items[${i}].currencyCode`, code: 'CURRENCY_FORBIDDEN' });
          }
          if (d.periodType === 'ROLLING_DAYS' && !d.periodLength) {
            errors.push({ field: `items[${i}].periodLength`, code: 'PERIOD_LENGTH_REQUIRED' });
          }
          if (d.rolloverPolicy === 'CAPPED' && d.rolloverCap === undefined) {
            errors.push({ field: `items[${i}].rolloverCap`, code: 'ROLLOVER_CAP_REQUIRED' });
          }
        });
        if (errors.length > 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
        }
        version.definitions = body.items.map((d) => ({
          id: world().nextId(),
          code: d.code,
          name: d.name,
          unitType: d.unitType,
          currencyCode: d.currencyCode ?? null,
          familyShared: d.familyShared ?? false,
          allowOverdraft: d.allowOverdraft ?? false,
          initialQuantity: d.initialQuantity,
          periodType: d.periodType,
          periodLength: d.periodLength ?? null,
          rolloverPolicy: d.rolloverPolicy ?? 'NONE',
          rolloverCap: d.rolloverCap ?? null,
          status: 'ACTIVE',
        }));
        version.rowVersion += 1;
        return HttpResponse.json(toPlanVersion(version), {
          headers: { ETag: etagOf(version.rowVersion) },
        });
      },
    ),

    http.post(`${ANY}/api/v1/plan-versions/:planVersionId/submit`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'plan.manage', true);
      if ('error' in g) return g.error;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null)
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      const version = findVersion(g.tenantId, pathParam(params, 'planVersionId'));
      if (!version) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      if (version.status !== 'DRAFT') {
        return problem(api, 409, 'PLAN_VERSION_IMMUTABLE', 'Yalnız taslak sürüm gönderilebilir');
      }
      if (version.rowVersion !== expected)
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      if (version.definitions.length === 0 || version.validFrom === null) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [
            { field: version.validFrom === null ? 'validFrom' : 'definitions', code: 'REQUIRED' },
          ],
        });
      }
      const body = (await readJson<Schemas['ReviewComment']>(request)) ?? {};
      version.status = 'UNDER_REVIEW';
      version.submittedAt = new Date().toISOString();
      version.submittedBy = g.session.account.actorId;
      version.reviewComment = body.comment ?? null;
      version.rowVersion += 1;
      return HttpResponse.json(toPlanVersion(version), {
        headers: { ETag: etagOf(version.rowVersion) },
      });
    }),

    http.post(`${ANY}/api/v1/plan-versions/:planVersionId/publish`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'plan.publish', true);
      if ('error' in g) return g.error;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null)
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      const version = findVersion(g.tenantId, pathParam(params, 'planVersionId'));
      if (!version) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      if (version.status !== 'UNDER_REVIEW') {
        return problem(
          api,
          409,
          'PLAN_VERSION_IMMUTABLE',
          'Yalnız incelemedeki sürüm yayınlanabilir',
        );
      }
      if (version.rowVersion !== expected)
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      if (version.submittedBy === g.session.account.actorId) {
        return problem(
          api,
          403,
          'MAKER_CHECKER_SAME_ACTOR',
          'Gönderen ile yayınlayan aynı kişi olamaz',
        );
      }
      const clash = world().planVersions.find(
        (v) =>
          v.id !== version.id &&
          v.planId === version.planId &&
          v.status === 'PUBLISHED' &&
          v.validFrom !== null &&
          version.validFrom !== null &&
          periodsOverlap(v.validFrom, v.validTo, version.validFrom, version.validTo),
      );
      if (clash) {
        return problem(
          api,
          409,
          'PLAN_VERSION_OVERLAP',
          'Bu tarih aralığında yayında başka bir sürüm var',
        );
      }
      const body = (await readJson<Schemas['ReviewComment']>(request)) ?? {};
      version.status = 'PUBLISHED';
      version.publishedAt = new Date().toISOString();
      version.publishedBy = g.session.account.actorId;
      version.configurationHash = pseudoHash(
        `${version.planId}:${version.versionNo}:${version.definitions.map((d) => d.code).join(',')}`,
      );
      if (body.comment) version.reviewComment = body.comment;
      version.rowVersion += 1;
      return HttpResponse.json(toPlanVersion(version), {
        headers: { ETag: etagOf(version.rowVersion) },
      });
    }),

    http.post(`${ANY}/api/v1/plan-versions/:planVersionId/retire`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'plan.publish', true);
      if ('error' in g) return g.error;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null)
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      const version = findVersion(g.tenantId, pathParam(params, 'planVersionId'));
      if (!version) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      if (version.status !== 'PUBLISHED') {
        return problem(
          api,
          409,
          'PLAN_VERSION_IMMUTABLE',
          'Yalnız yayında olan sürüm kullanımdan çıkarılabilir',
        );
      }
      if (version.rowVersion !== expected)
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      const body = await readJson<Schemas['ReasonCommand']>(request);
      if (!body?.reasonCode) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'reasonCode', code: 'REQUIRED' }],
        });
      }
      version.status = 'RETIRED';
      version.retireReasonCode = body.reasonCode;
      version.rowVersion += 1;
      return HttpResponse.json(toPlanVersion(version), {
        headers: { ETag: etagOf(version.rowVersion) },
      });
    }),

    // --- enrollments ------------------------------------------------------------
    http.get(`${ANY}/api/v1/people/:personId/enrollments`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'program.read', false);
      if ('error' in g) return g.error;
      const personId = pathParam(params, 'personId');
      if (!world().people.some((p) => p.id === personId && p.tenantId === g.tenantId)) {
        return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      }
      const items = world()
        .enrollments.filter((e) => e.tenantId === g.tenantId && e.personId === personId)
        .map(toEnrollment);
      return HttpResponse.json({ items });
    }),

    http.post(`${ANY}/api/v1/people/:personId/enrollments`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'enrollment.manage', true);
      if ('error' in g) return g.error;
      const personId = pathParam(params, 'personId');
      if (!world().people.some((p) => p.id === personId && p.tenantId === g.tenantId)) {
        return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      }
      const body = await readJson<Schemas['CreateEnrollmentRequest']>(request);
      if (!body?.sponsorMembershipId || !body.planId || !body.validFrom) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'planId', code: 'REQUIRED' }],
        });
      }
      const membership = world().memberships.find(
        (m) =>
          m.id === body.sponsorMembershipId && m.tenantId === g.tenantId && m.personId === personId,
      );
      if (!membership) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'sponsorMembershipId', code: 'NOT_FOUND' }],
        });
      }
      if (membership.status !== 'ACTIVE') {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'sponsorMembershipId', code: 'MEMBERSHIP_NOT_ACTIVE' }],
        });
      }
      const plan = findPlan(g.tenantId, body.planId);
      if (!plan) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'planId', code: 'NOT_FOUND' }],
        });
      }
      const published = world().planVersions.some(
        (v) => v.planId === plan.id && covers(v, body.validFrom),
      );
      if (!published) {
        return problem(api, 422, 'PLAN_NOT_PUBLISHED', 'Planın bu tarihte yayınlanmış sürümü yok', {
          errors: [{ field: 'planId', code: 'PLAN_NOT_PUBLISHED' }],
        });
      }
      const overlap = world().enrollments.some(
        (e) =>
          e.tenantId === g.tenantId &&
          e.sponsorMembershipId === membership.id &&
          e.planId === plan.id &&
          e.status !== 'ENDED' &&
          periodsOverlap(e.validFrom, e.validTo, body.validFrom, body.validTo ?? null),
      );
      if (overlap) {
        return problem(api, 409, 'ENROLLMENT_OVERLAP', 'Çakışan bir plan kaydı var');
      }
      const enrollment = {
        id: world().nextId(),
        tenantId: g.tenantId,
        personId,
        planId: plan.id,
        planCode: plan.code,
        programId: plan.programId,
        sponsorMembershipId: membership.id,
        status: body.status ?? ('ACTIVE' as const),
        validFrom: body.validFrom,
        validTo: body.validTo ?? null,
        enrollmentReason: body.enrollmentReason ?? null,
        sourceSystem: null,
        rowVersion: 1,
      };
      world().enrollments.push(enrollment);
      return HttpResponse.json(toEnrollment(enrollment), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.get(`${ANY}/api/v1/enrollments`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'program.read', false);
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
      const planId = url.searchParams.get('planId');
      const status = url.searchParams.get('status');
      const rows = world()
        .enrollments.filter((e) => e.tenantId === g.tenantId)
        .filter((e) => !planId || e.planId === planId)
        .filter((e) => !status || e.status === status);
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map(toEnrollment),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['EnrollmentPage']);
    }),

    http.get(`${ANY}/api/v1/enrollments/:enrollmentId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'program.read', false);
      if ('error' in g) return g.error;
      const enrollment = world().enrollments.find(
        (e) => e.id === pathParam(params, 'enrollmentId') && e.tenantId === g.tenantId,
      );
      if (!enrollment) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      return HttpResponse.json(toEnrollment(enrollment), {
        headers: { ETag: etagOf(enrollment.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/enrollments/:enrollmentId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'enrollment.manage', true);
      if ('error' in g) return g.error;
      const unsupported = requireMergePatch(request);
      if (unsupported) return unsupported;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null)
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      const enrollment = world().enrollments.find(
        (e) => e.id === pathParam(params, 'enrollmentId') && e.tenantId === g.tenantId,
      );
      if (!enrollment) return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      if (enrollment.rowVersion !== expected)
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      const patch = await readJson<Schemas['UpdateEnrollmentRequest']>(request);
      if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      if (patch.status !== undefined) enrollment.status = patch.status;
      if ('validTo' in patch) enrollment.validTo = patch.validTo ?? null;
      enrollment.rowVersion += 1;
      return HttpResponse.json(toEnrollment(enrollment), {
        headers: { ETag: etagOf(enrollment.rowVersion) },
      });
    }),
  ];
}
