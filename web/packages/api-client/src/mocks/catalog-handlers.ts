/**
 * MSW handlers for the M3 catalog surface: service categories, service definitions with
 * their external code mappings, code systems and code values. They enforce the same rules
 * as the Go API, because the screens are built against these responses: immutable codes,
 * a category tree at most six deep and no loops, overlapping code mappings refused as a
 * conflict, and code values read as of a date.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  categoryChain,
  categoryDepth,
  toCodeSystem,
  toCodeValue,
  toServiceCategory,
  toServiceCodeMapping,
  toServiceDefinition,
  type MockWorld,
  type StoredCodeMapping,
  type StoredCodeSystem,
  type StoredCodeValue,
  type StoredServiceCategory,
  type StoredServiceDefinition,
} from './data';
import type { MockApi } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  notFound,
  parseIfMatch,
  parseLimit,
  pathParam,
  periodsOverlap,
  problem,
  readJson,
  requireMergePatch,
  wait,
  withinPeriod,
  type FieldError,
  type Schemas,
} from './handlers';

const CATEGORY_CODE = /^[A-Z][A-Z0-9_]{1,63}$/;
const SYSTEM_CODE = /^[A-Z][A-Z0-9_]{1,39}$/;
const MAX_DEPTH = 6;
const MAX_IMPORT_ROWS = 5000;

/** Today in the mock, used as the default `asOf`. */
function today(): string {
  return new Date().toISOString().slice(0, 10);
}

export function catalogHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  const findCategory = (tenantId: string, id: string): StoredServiceCategory | undefined =>
    world().serviceCategories.find((c) => c.id === id && c.tenantId === tenantId);
  const findDefinition = (tenantId: string, id: string): StoredServiceDefinition | undefined =>
    world().serviceDefinitions.find((d) => d.id === id && d.tenantId === tenantId);
  const findSystem = (tenantId: string, id: string): StoredCodeSystem | undefined =>
    world().codeSystems.find((s) => s.id === id && s.tenantId === tenantId);

  /** Guards that a merge-patch body does not try to move an immutable field. */
  const immutable = (fields: string[], patch: Record<string, unknown>): FieldError[] =>
    fields.filter((f) => f in patch).map((field) => ({ field, code: 'IMMUTABLE' }));

  return [
    // --- service categories -----------------------------------------------------
    http.get(`${ANY}/api/v1/service-categories`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'catalog.read', false);
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
      const parentId = url.searchParams.get('parentId');
      const domain = url.searchParams.get('domain');
      const active = url.searchParams.get('active');
      const q = (url.searchParams.get('q') ?? '').trim().toLocaleLowerCase('tr');
      const rows = world()
        .serviceCategories.filter((c) => c.tenantId === g.tenantId)
        .filter((c) => !parentId || c.parentId === parentId)
        .filter((c) => !domain || c.domain === domain)
        .filter((c) => active === null || c.active === (active === 'true'))
        .filter(
          (c) =>
            !q ||
            c.code.toLocaleLowerCase('tr').includes(q) ||
            c.name.toLocaleLowerCase('tr').includes(q),
        );
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map(toServiceCategory),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['ServiceCategoryPage']);
    }),

    http.post(`${ANY}/api/v1/service-categories`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'catalog.manage', true);
      if ('error' in g) return g.error;
      const body = await readJson<Schemas['CreateServiceCategoryRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors: FieldError[] = [];
      if (!body.code || !CATEGORY_CODE.test(body.code)) {
        errors.push({ field: 'code', code: 'FORMAT' });
      }
      if (!body.name || [...body.name].length < 2) errors.push({ field: 'name', code: 'LENGTH' });
      if (!body.domain) errors.push({ field: 'domain', code: 'REQUIRED' });
      const parent = body.parentId ? findCategory(g.tenantId, body.parentId) : undefined;
      if (body.parentId && !parent) errors.push({ field: 'parentId', code: 'NOT_FOUND' });
      if (parent && categoryDepth(world(), parent.id) >= MAX_DEPTH) {
        errors.push({ field: 'parentId', code: 'DEPTH_EXCEEDED' });
      }
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (
        world().serviceCategories.some((c) => c.tenantId === g.tenantId && c.code === body.code)
      ) {
        return problem(api, 409, 'CATEGORY_CODE_TAKEN', 'Bu kategori kodu zaten kullanılıyor');
      }
      const category: StoredServiceCategory = {
        id: world().nextId(),
        tenantId: g.tenantId,
        parentId: body.parentId ?? null,
        code: body.code,
        name: body.name,
        domain: body.domain,
        active: body.active ?? true,
        rowVersion: 1,
      };
      world().serviceCategories.push(category);
      return HttpResponse.json(toServiceCategory(category), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.get(`${ANY}/api/v1/service-categories/:categoryId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'catalog.read', false);
      if ('error' in g) return g.error;
      const category = findCategory(g.tenantId, pathParam(params, 'categoryId'));
      if (!category) return notFound(api);
      return HttpResponse.json(toServiceCategory(category), {
        headers: { ETag: etagOf(category.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/service-categories/:categoryId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'catalog.manage', true);
      if ('error' in g) return g.error;
      const unsupported = requireMergePatch(api, request);
      if (unsupported) return unsupported;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null) {
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      }
      const category = findCategory(g.tenantId, pathParam(params, 'categoryId'));
      if (!category) return notFound(api);
      if (category.rowVersion !== expected) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      }
      const patch = await readJson<Record<string, unknown>>(request);
      if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors = immutable(['code', 'domain'], patch);
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if ('parentId' in patch) {
        const parentId = (patch['parentId'] as string | null) ?? null;
        if (parentId !== null) {
          const parent = findCategory(g.tenantId, parentId);
          if (!parent) {
            return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
              errors: [{ field: 'parentId', code: 'NOT_FOUND' }],
            });
          }
          // Re-parenting under one's own descendant would close a loop.
          if (categoryChain(world(), parent.id).some((c) => c.id === category.id)) {
            return problem(api, 409, 'CATEGORY_CYCLE', 'Kategori kendi altına taşınamaz');
          }
          const deepestChild = world()
            .serviceCategories.filter((c) => c.tenantId === g.tenantId)
            .filter((c) => categoryChain(world(), c.id).some((x) => x.id === category.id))
            .reduce(
              (max, c) =>
                Math.max(
                  max,
                  categoryDepth(world(), c.id) - categoryDepth(world(), category.id) + 1,
                ),
              1,
            );
          if (categoryDepth(world(), parent.id) + deepestChild > MAX_DEPTH) {
            return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
              errors: [{ field: 'parentId', code: 'DEPTH_EXCEEDED' }],
            });
          }
        }
        category.parentId = parentId;
      }
      if (typeof patch['name'] === 'string') category.name = patch['name'];
      if (typeof patch['active'] === 'boolean') category.active = patch['active'];
      category.rowVersion += 1;
      return HttpResponse.json(toServiceCategory(category), {
        headers: { ETag: etagOf(category.rowVersion) },
      });
    }),

    // --- service definitions ----------------------------------------------------
    http.get(`${ANY}/api/v1/service-definitions`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'catalog.read', false);
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
      const categoryId = url.searchParams.get('categoryId');
      const domain = url.searchParams.get('domain');
      const active = url.searchParams.get('active');
      const q = (url.searchParams.get('q') ?? '').trim().toLocaleLowerCase('tr');
      const rows = world()
        .serviceDefinitions.filter((d) => d.tenantId === g.tenantId)
        .filter((d) => !categoryId || d.categoryId === categoryId)
        .filter(
          (d) =>
            !domain ||
            world().serviceCategories.find((c) => c.id === d.categoryId)?.domain === domain,
        )
        .filter((d) => active === null || d.active === (active === 'true'))
        .filter(
          (d) =>
            !q ||
            d.code.toLocaleLowerCase('tr').includes(q) ||
            d.name.toLocaleLowerCase('tr').includes(q),
        );
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map((d) => toServiceDefinition(world(), d)),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['ServiceDefinitionPage']);
    }),

    http.post(`${ANY}/api/v1/service-definitions`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'catalog.manage', true);
      if ('error' in g) return g.error;
      const body = await readJson<Schemas['CreateServiceDefinitionRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors: FieldError[] = [];
      if (!body.code || !CATEGORY_CODE.test(body.code)) {
        errors.push({ field: 'code', code: 'FORMAT' });
      }
      if (!body.name || [...body.name].length < 2) errors.push({ field: 'name', code: 'LENGTH' });
      if (!body.categoryId || !findCategory(g.tenantId, body.categoryId)) {
        errors.push({ field: 'categoryId', code: 'NOT_FOUND' });
      }
      if (!body.fulfillmentMode) errors.push({ field: 'fulfillmentMode', code: 'REQUIRED' });
      if (!body.defaultUnitType) errors.push({ field: 'defaultUnitType', code: 'REQUIRED' });
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (
        world().serviceDefinitions.some((d) => d.tenantId === g.tenantId && d.code === body.code)
      ) {
        return problem(
          api,
          409,
          'SERVICE_DEFINITION_CODE_TAKEN',
          'Bu hizmet kodu zaten kullanılıyor',
        );
      }
      const definition: StoredServiceDefinition = {
        id: world().nextId(),
        tenantId: g.tenantId,
        categoryId: body.categoryId,
        code: body.code,
        name: body.name,
        description: body.description ?? null,
        fulfillmentMode: body.fulfillmentMode,
        defaultUnitType: body.defaultUnitType,
        requiresProvider: body.requiresProvider ?? true,
        active: body.active ?? true,
        rowVersion: 1,
      };
      world().serviceDefinitions.push(definition);
      return HttpResponse.json(toServiceDefinition(world(), definition), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.get(`${ANY}/api/v1/service-definitions/:definitionId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'catalog.read', false);
      if ('error' in g) return g.error;
      const definition = findDefinition(g.tenantId, pathParam(params, 'definitionId'));
      if (!definition) return notFound(api);
      return HttpResponse.json(toServiceDefinition(world(), definition), {
        headers: { ETag: etagOf(definition.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/service-definitions/:definitionId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'catalog.manage', true);
      if ('error' in g) return g.error;
      const unsupported = requireMergePatch(api, request);
      if (unsupported) return unsupported;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null) {
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      }
      const definition = findDefinition(g.tenantId, pathParam(params, 'definitionId'));
      if (!definition) return notFound(api);
      if (definition.rowVersion !== expected) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      }
      const patch = await readJson<Record<string, unknown>>(request);
      if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors = immutable(['code'], patch);
      if (
        typeof patch['categoryId'] === 'string' &&
        !findCategory(g.tenantId, patch['categoryId'])
      ) {
        errors.push({ field: 'categoryId', code: 'NOT_FOUND' });
      }
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (typeof patch['categoryId'] === 'string') definition.categoryId = patch['categoryId'];
      if (typeof patch['name'] === 'string') definition.name = patch['name'];
      if ('description' in patch) {
        definition.description = (patch['description'] as string | null) ?? null;
      }
      if (typeof patch['fulfillmentMode'] === 'string') {
        definition.fulfillmentMode = patch['fulfillmentMode'] as Schemas['FulfillmentMode'];
      }
      if (typeof patch['defaultUnitType'] === 'string') {
        definition.defaultUnitType = patch['defaultUnitType'] as Schemas['ServiceUnitType'];
      }
      if (typeof patch['requiresProvider'] === 'boolean') {
        definition.requiresProvider = patch['requiresProvider'];
      }
      if (typeof patch['active'] === 'boolean') definition.active = patch['active'];
      definition.rowVersion += 1;
      return HttpResponse.json(toServiceDefinition(world(), definition), {
        headers: { ETag: etagOf(definition.rowVersion) },
      });
    }),

    // --- code mappings of a definition ------------------------------------------
    http.get(
      `${ANY}/api/v1/service-definitions/:definitionId/code-mappings`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'catalog.read', false);
        if ('error' in g) return g.error;
        const definition = findDefinition(g.tenantId, pathParam(params, 'definitionId'));
        if (!definition) return notFound(api);
        const items = world()
          .codeMappings.filter((m) => m.serviceDefinitionId === definition.id)
          .sort(
            (a, b) =>
              a.codeSystemId.localeCompare(b.codeSystemId) || (a.validFrom < b.validFrom ? -1 : 1),
          )
          .map((m) => toServiceCodeMapping(world(), m));
        return HttpResponse.json({ items } satisfies Schemas['ServiceCodeMappingList']);
      },
    ),

    http.put(
      `${ANY}/api/v1/service-definitions/:definitionId/code-mappings`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'catalog.manage', true);
        if ('error' in g) return g.error;
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null) {
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        }
        const definition = findDefinition(g.tenantId, pathParam(params, 'definitionId'));
        if (!definition) return notFound(api);
        if (definition.rowVersion !== expected) {
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        }
        const body = await readJson<Schemas['ReplaceServiceCodeMappingsRequest']>(request);
        if (!body?.items) {
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        }
        const errors: FieldError[] = [];
        body.items.forEach((m, i) => {
          if (!m.codeSystemId || !findSystem(g.tenantId, m.codeSystemId)) {
            errors.push({ field: `items[${i}].codeSystemId`, code: 'NOT_FOUND' });
          }
          if (!m.code) errors.push({ field: `items[${i}].code`, code: 'REQUIRED' });
          if (!m.validFrom) errors.push({ field: `items[${i}].validFrom`, code: 'REQUIRED' });
        });
        if (errors.length > 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
        }
        // Same system and code over overlapping periods, or a second primary for the same
        // system over an overlapping period: both are the same conflict.
        for (let i = 0; i < body.items.length; i++) {
          for (let j = i + 1; j < body.items.length; j++) {
            const a = body.items[i]!;
            const b = body.items[j]!;
            if (a.codeSystemId !== b.codeSystemId) continue;
            const overlap = periodsOverlap(
              a.validFrom,
              a.validTo ?? null,
              b.validFrom,
              b.validTo ?? null,
            );
            if (!overlap) continue;
            if (a.code === b.code || ((a.primary ?? false) && (b.primary ?? false))) {
              return problem(
                api,
                409,
                'CODE_MAPPING_OVERLAP',
                'Aynı kod sistemi için çakışan eşleme var',
              );
            }
          }
        }
        world().codeMappings = world().codeMappings.filter(
          (m) => m.serviceDefinitionId !== definition.id,
        );
        const stored: StoredCodeMapping[] = body.items.map((m) => ({
          id: world().nextId(),
          tenantId: g.tenantId,
          serviceDefinitionId: definition.id,
          codeSystemId: m.codeSystemId,
          code: m.code,
          validFrom: m.validFrom,
          validTo: m.validTo ?? null,
          primary: m.primary ?? false,
        }));
        world().codeMappings.push(...stored);
        definition.rowVersion += 1;
        return HttpResponse.json(
          {
            items: stored.map((m) => toServiceCodeMapping(world(), m)),
          } satisfies Schemas['ServiceCodeMappingList'],
          { headers: { ETag: etagOf(definition.rowVersion) } },
        );
      },
    ),

    // --- code systems -----------------------------------------------------------
    http.get(`${ANY}/api/v1/code-systems`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'catalog.read', false);
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
      const authority = url.searchParams.get('authority');
      const status = url.searchParams.get('status');
      const q = (url.searchParams.get('q') ?? '').trim().toLocaleLowerCase('tr');
      const rows = world()
        .codeSystems.filter((s) => s.tenantId === g.tenantId)
        .filter((s) => !authority || s.authority === authority)
        .filter((s) => !status || s.status === status)
        .filter(
          (s) =>
            !q ||
            s.code.toLocaleLowerCase('tr').includes(q) ||
            s.name.toLocaleLowerCase('tr').includes(q),
        );
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map(toCodeSystem),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['CodeSystemPage']);
    }),

    http.post(`${ANY}/api/v1/code-systems`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'catalog.manage', true);
      if ('error' in g) return g.error;
      const body = await readJson<Schemas['CreateCodeSystemRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors: FieldError[] = [];
      if (!body.code || !SYSTEM_CODE.test(body.code))
        errors.push({ field: 'code', code: 'FORMAT' });
      if (!body.name || [...body.name].length < 2) errors.push({ field: 'name', code: 'LENGTH' });
      if (!body.version) errors.push({ field: 'version', code: 'REQUIRED' });
      if (!body.authority) errors.push({ field: 'authority', code: 'REQUIRED' });
      if (!body.validFrom) errors.push({ field: 'validFrom', code: 'REQUIRED' });
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (
        world().codeSystems.some(
          (s) => s.tenantId === g.tenantId && s.code === body.code && s.version === body.version,
        )
      ) {
        return problem(api, 409, 'CODE_SYSTEM_TAKEN', 'Bu kod sistemi sürümü zaten kayıtlı');
      }
      const system: StoredCodeSystem = {
        id: world().nextId(),
        tenantId: g.tenantId,
        code: body.code,
        name: body.name,
        version: body.version,
        authority: body.authority,
        licensed: body.licensed ?? false,
        status: 'ACTIVE',
        validFrom: body.validFrom,
        validTo: body.validTo ?? null,
        rowVersion: 1,
      };
      world().codeSystems.push(system);
      return HttpResponse.json(toCodeSystem(system), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.get(`${ANY}/api/v1/code-systems/:codeSystemId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'catalog.read', false);
      if ('error' in g) return g.error;
      const system = findSystem(g.tenantId, pathParam(params, 'codeSystemId'));
      if (!system) return notFound(api);
      return HttpResponse.json(toCodeSystem(system), {
        headers: { ETag: etagOf(system.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/code-systems/:codeSystemId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'catalog.manage', true);
      if ('error' in g) return g.error;
      const unsupported = requireMergePatch(api, request);
      if (unsupported) return unsupported;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null) {
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      }
      const system = findSystem(g.tenantId, pathParam(params, 'codeSystemId'));
      if (!system) return notFound(api);
      if (system.rowVersion !== expected) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      }
      const patch = await readJson<Record<string, unknown>>(request);
      if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors = immutable(['code', 'version'], patch);
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (typeof patch['name'] === 'string') system.name = patch['name'];
      if (typeof patch['authority'] === 'string') {
        system.authority = patch['authority'] as Schemas['CodeSystemAuthority'];
      }
      if (typeof patch['licensed'] === 'boolean') system.licensed = patch['licensed'];
      if (patch['status'] === 'ACTIVE' || patch['status'] === 'INACTIVE') {
        system.status = patch['status'];
      }
      if ('validTo' in patch) system.validTo = (patch['validTo'] as string | null) ?? null;
      system.rowVersion += 1;
      return HttpResponse.json(toCodeSystem(system), {
        headers: { ETag: etagOf(system.rowVersion) },
      });
    }),

    // --- code values ------------------------------------------------------------
    http.get(`${ANY}/api/v1/code-systems/:codeSystemId/values`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'catalog.read', false);
      if ('error' in g) return g.error;
      const system = findSystem(g.tenantId, pathParam(params, 'codeSystemId'));
      if (!system) return notFound(api);
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'limit', code: 'FORMAT' }],
        });
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const asOf = url.searchParams.get('asOf') ?? today();
      const code = url.searchParams.get('code');
      const q = (url.searchParams.get('q') ?? '').trim().toLocaleLowerCase('tr');
      const rows = world()
        .codeValues.filter((v) => v.codeSystemId === system.id)
        .filter((v) => withinPeriod(asOf, v.validFrom, v.validTo))
        .filter((v) => !code || v.code === code)
        .filter(
          (v) =>
            !q ||
            v.code.toLocaleLowerCase('tr').includes(q) ||
            v.display.toLocaleLowerCase('tr').includes(q),
        );
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map(toCodeValue),
        asOf,
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['CodeValuePage']);
    }),

    // The colon is escaped so path-to-regexp reads ":import" as literal text rather than
    // as a path parameter.
    http.post(
      `${ANY}/api/v1/code-systems/:codeSystemId/values\\:import`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'catalog.manage', true);
        if ('error' in g) return g.error;
        const system = findSystem(g.tenantId, pathParam(params, 'codeSystemId'));
        if (!system) return notFound(api);
        const body = await readJson<Schemas['ImportCodeValuesRequest']>(request);
        if (!body?.items) {
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        }
        if (body.items.length > MAX_IMPORT_ROWS) {
          return problem(api, 413, 'PAYLOAD_TOO_LARGE', 'Tek seferde en fazla 5000 satır');
        }
        // All-or-nothing: a rejected batch leaves nothing behind.
        const importErrors: Schemas['CodeValueImportError'][] = [];
        const seen = new Set<string>();
        body.items.forEach((v, index) => {
          if (!v.code) importErrors.push({ index, field: 'code', code: 'REQUIRED' });
          if (!v.display) importErrors.push({ index, field: 'display', code: 'REQUIRED' });
          if (!v.validFrom) importErrors.push({ index, field: 'validFrom', code: 'REQUIRED' });
          const key = `${v.code}@${v.validFrom}`;
          if (seen.has(key)) importErrors.push({ index, field: 'code', code: 'DUPLICATE' });
          seen.add(key);
        });
        if (importErrors.length > 0) {
          return HttpResponse.json({
            created: 0,
            updated: 0,
            skipped: body.items.length,
            errors: importErrors,
          } satisfies Schemas['CodeValueImportResult']);
        }
        let created = 0;
        let updated = 0;
        for (const v of body.items) {
          const existing = world().codeValues.find(
            (x) => x.codeSystemId === system.id && x.code === v.code && x.validFrom === v.validFrom,
          );
          if (existing) {
            existing.display = v.display;
            existing.parentCode = v.parentCode ?? null;
            existing.validTo = v.validTo ?? null;
            existing.active = v.active ?? true;
            existing.attributes = v.attributes ?? {};
            updated += 1;
          } else {
            const stored: StoredCodeValue = {
              id: world().nextId(),
              tenantId: g.tenantId,
              codeSystemId: system.id,
              code: v.code,
              display: v.display,
              parentCode: v.parentCode ?? null,
              validFrom: v.validFrom,
              validTo: v.validTo ?? null,
              active: v.active ?? true,
              attributes: v.attributes ?? {},
            };
            world().codeValues.push(stored);
            created += 1;
          }
        }
        return HttpResponse.json({
          created,
          updated,
          skipped: 0,
          errors: [],
        } satisfies Schemas['CodeValueImportResult']);
      },
    ),
  ];
}
