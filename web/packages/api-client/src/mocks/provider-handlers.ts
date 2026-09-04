/**
 * MSW handlers for the M3 provider surface: provider profiles and their lifecycle
 * commands, locations, the capabilities that say what a location can deliver, the
 * practitioners registered at a provider, and the search eligibility actually asks —
 * which location can deliver this service on this date.
 *
 * A practitioner's registration number is treated exactly like a TCKN: it stays inside
 * the mock world, it never appears in a path or a query key, and every response carries
 * the masked form only.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  categoryCovers,
  toPractitioner,
  toProvider,
  toProviderCapability,
  toProviderLocation,
  type MockWorld,
  type StoredPractitioner,
  type StoredProvider,
  type StoredProviderCapability,
  type StoredProviderLocation,
} from './data';
import type { MockApi } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  hasStepUp,
  notFound,
  parseIfMatch,
  parseLimit,
  pathParam,
  periodsOverlap,
  problem,
  readJson,
  requireMergePatch,
  stepUpRequired,
  wait,
  withinPeriod,
  type FieldError,
  type Schemas,
} from './handlers';

const LOCATION_CODE = /^[A-Z0-9][A-Z0-9_-]{0,39}$/;

const PROVIDER_TRANSITIONS: Record<string, Schemas['ProviderStatus'][]> = {
  PENDING: ['ACTIVE', 'TERMINATED'],
  ACTIVE: ['SUSPENDED', 'TERMINATED'],
  SUSPENDED: ['ACTIVE', 'TERMINATED'],
  TERMINATED: [],
};

function today(): string {
  return new Date().toISOString().slice(0, 10);
}

export function providerHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  const findProvider = (tenantId: string, id: string): StoredProvider | undefined =>
    world().providers.find((p) => p.id === id && p.tenantId === tenantId);
  const findLocation = (tenantId: string, id: string): StoredProviderLocation | undefined =>
    world().providerLocations.find((l) => l.id === id && l.tenantId === tenantId);
  const findPractitioner = (tenantId: string, id: string): StoredPractitioner | undefined =>
    world().practitioners.find((p) => p.id === id && p.tenantId === tenantId);

  const immutable = (fields: string[], patch: Record<string, unknown>): FieldError[] =>
    fields.filter((f) => f in patch).map((field) => ({ field, code: 'IMMUTABLE' }));

  /** Shared body of activate / suspend / terminate. */
  const transition = async (
    request: Request,
    providerId: string,
    target: Schemas['ProviderStatus'],
    reasonRequired: boolean,
  ): Promise<Response> => {
    const g = guardTenant(api, request, 'provider.manage', true);
    if ('error' in g) return g.error;
    const expected = parseIfMatch(request.headers.get('If-Match'));
    if (expected === null) {
      return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
    }
    const provider = findProvider(g.tenantId, providerId);
    if (!provider) return notFound(api);
    if (provider.rowVersion !== expected) {
      return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
    }
    if (!(PROVIDER_TRANSITIONS[provider.status] ?? []).includes(target)) {
      return problem(api, 409, 'PROVIDER_TRANSITION_INVALID', 'Sağlayıcı bu duruma geçemez');
    }
    if (reasonRequired) {
      const body = await readJson<Schemas['ReasonCommand']>(request);
      if (!body?.reasonCode) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'reasonCode', code: 'REQUIRED' }],
        });
      }
    }
    provider.status = target;
    provider.rowVersion += 1;
    return HttpResponse.json(toProvider(world(), provider), {
      headers: { ETag: etagOf(provider.rowVersion) },
    });
  };

  return [
    // --- providers --------------------------------------------------------------
    http.get(`${ANY}/api/v1/providers`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.read', false);
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
      const providerType = url.searchParams.get('providerType');
      const status = url.searchParams.get('status');
      const networkTier = url.searchParams.get('networkTier');
      const q = (url.searchParams.get('q') ?? '').trim().toLocaleLowerCase('tr');
      const rows = world()
        .providers.filter((p) => p.tenantId === g.tenantId)
        .filter((p) => !providerType || p.providerType === providerType)
        .filter((p) => !status || p.status === status)
        .filter((p) => !networkTier || p.networkTier === networkTier)
        .map((p) => toProvider(world(), p))
        .filter((p) => !q || p.organizationName.toLocaleLowerCase('tr').includes(q));
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page,
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['ProviderPage']);
    }),

    http.post(`${ANY}/api/v1/providers`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.manage', true);
      if ('error' in g) return g.error;
      const body = await readJson<Schemas['CreateProviderRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const relationship = world().relationships.find(
        (r) => r.id === body.tenantOrganizationId && r.tenantId === g.tenantId,
      );
      if (!relationship) return notFound(api);
      const errors: FieldError[] = [];
      if (relationship.relationshipRole !== 'PROVIDER') {
        errors.push({ field: 'tenantOrganizationId', code: 'PROVIDER_ROLE_REQUIRED' });
      }
      if (!body.providerType) errors.push({ field: 'providerType', code: 'REQUIRED' });
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (world().providers.some((p) => p.tenantOrganizationId === relationship.id)) {
        return problem(
          api,
          409,
          'PROVIDER_PROFILE_EXISTS',
          'Bu kuruluş ilişkisinin zaten bir sağlayıcı profili var',
        );
      }
      const provider: StoredProvider = {
        id: world().nextId(),
        tenantId: g.tenantId,
        tenantOrganizationId: relationship.id,
        providerType: body.providerType,
        status: 'PENDING',
        networkTier: body.networkTier ?? null,
        contractedFrom: body.contractedFrom ?? null,
        contractedTo: body.contractedTo ?? null,
        notes: body.notes ?? null,
        rowVersion: 1,
      };
      world().providers.push(provider);
      return HttpResponse.json(toProvider(world(), provider), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    // Registered before /providers/:providerId so the literal segment wins.
    http.get(`${ANY}/api/v1/providers/search`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.read', false);
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
      const serviceDefinitionId = url.searchParams.get('serviceDefinitionId');
      if (!serviceDefinitionId) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'serviceDefinitionId', code: 'REQUIRED' }],
        });
      }
      const definition = world().serviceDefinitions.find(
        (d) => d.id === serviceDefinitionId && d.tenantId === g.tenantId,
      );
      const asOf = url.searchParams.get('asOf') ?? today();
      const city = url.searchParams.get('city');
      const q = (url.searchParams.get('q') ?? '').trim().toLocaleLowerCase('tr');
      const rows: Schemas['ProviderSearchResult'][] = [];
      if (definition) {
        for (const location of world().providerLocations) {
          if (location.tenantId !== g.tenantId || location.status !== 'ACTIVE') continue;
          const provider = world().providers.find((p) => p.id === location.providerId);
          if (!provider || provider.status !== 'ACTIVE') continue;
          if (city && location.city !== city) continue;
          if (
            q &&
            !location.code.toLocaleLowerCase('tr').includes(q) &&
            !location.name.toLocaleLowerCase('tr').includes(q)
          ) {
            continue;
          }
          // The category tree is resolved at read time, so a capability naming a category
          // covers definitions added under it later.
          let matchedVia: 'DEFINITION' | 'CATEGORY' | null = null;
          for (const capability of world().providerCapabilities) {
            if (capability.locationId !== location.id) continue;
            if (!withinPeriod(asOf, capability.validFrom, capability.validTo)) continue;
            if (capability.serviceDefinitionId === definition.id) {
              matchedVia = 'DEFINITION';
              break;
            }
            if (
              capability.serviceCategoryId &&
              categoryCovers(world(), capability.serviceCategoryId, definition.categoryId) !== null
            ) {
              matchedVia = 'CATEGORY';
            }
          }
          if (!matchedVia) continue;
          const projected = toProvider(world(), provider);
          rows.push({
            providerId: provider.id,
            organizationName: projected.organizationName,
            providerType: provider.providerType,
            networkTier: provider.networkTier,
            locationId: location.id,
            locationCode: location.code,
            locationName: location.name,
            city: location.city,
            district: location.district,
            latitude: location.latitude,
            longitude: location.longitude,
            matchedVia,
          });
        }
      }
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page,
        asOf,
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['ProviderSearchPage']);
    }),

    http.get(`${ANY}/api/v1/providers/:providerId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.read', false);
      if ('error' in g) return g.error;
      const provider = findProvider(g.tenantId, pathParam(params, 'providerId'));
      if (!provider) return notFound(api);
      return HttpResponse.json(toProvider(world(), provider), {
        headers: { ETag: etagOf(provider.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/providers/:providerId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.manage', true);
      if ('error' in g) return g.error;
      const unsupported = requireMergePatch(api, request);
      if (unsupported) return unsupported;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null) {
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      }
      const provider = findProvider(g.tenantId, pathParam(params, 'providerId'));
      if (!provider) return notFound(api);
      if (provider.rowVersion !== expected) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      }
      const patch = await readJson<Record<string, unknown>>(request);
      if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      // The status moves through the explicit commands, never through a field assignment.
      const errors = immutable(['status', 'tenantOrganizationId'], patch);
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (typeof patch['providerType'] === 'string') {
        provider.providerType = patch['providerType'] as Schemas['ProviderType'];
      }
      if ('networkTier' in patch) {
        provider.networkTier = (patch['networkTier'] as string | null) ?? null;
      }
      if ('contractedFrom' in patch) {
        provider.contractedFrom = (patch['contractedFrom'] as string | null) ?? null;
      }
      if ('contractedTo' in patch) {
        provider.contractedTo = (patch['contractedTo'] as string | null) ?? null;
      }
      if ('notes' in patch) provider.notes = (patch['notes'] as string | null) ?? null;
      provider.rowVersion += 1;
      return HttpResponse.json(toProvider(world(), provider), {
        headers: { ETag: etagOf(provider.rowVersion) },
      });
    }),

    http.post(`${ANY}/api/v1/providers/:providerId/activate`, async ({ request, params }) => {
      await wait(api);
      return transition(request, pathParam(params, 'providerId'), 'ACTIVE', false);
    }),

    http.post(`${ANY}/api/v1/providers/:providerId/suspend`, async ({ request, params }) => {
      await wait(api);
      return transition(request, pathParam(params, 'providerId'), 'SUSPENDED', true);
    }),

    http.post(`${ANY}/api/v1/providers/:providerId/terminate`, async ({ request, params }) => {
      await wait(api);
      return transition(request, pathParam(params, 'providerId'), 'TERMINATED', true);
    }),

    // --- locations --------------------------------------------------------------
    http.get(`${ANY}/api/v1/providers/:providerId/locations`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.read', false);
      if ('error' in g) return g.error;
      const provider = findProvider(g.tenantId, pathParam(params, 'providerId'));
      if (!provider) return notFound(api);
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'limit', code: 'FORMAT' }],
        });
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const city = url.searchParams.get('city');
      const status = url.searchParams.get('status');
      const q = (url.searchParams.get('q') ?? '').trim().toLocaleLowerCase('tr');
      const rows = world()
        .providerLocations.filter((l) => l.providerId === provider.id)
        .filter((l) => !city || l.city === city)
        .filter((l) => !status || l.status === status)
        .filter(
          (l) =>
            !q ||
            l.code.toLocaleLowerCase('tr').includes(q) ||
            l.name.toLocaleLowerCase('tr').includes(q),
        );
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map(toProviderLocation),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['ProviderLocationPage']);
    }),

    http.post(`${ANY}/api/v1/providers/:providerId/locations`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.manage', true);
      if ('error' in g) return g.error;
      const provider = findProvider(g.tenantId, pathParam(params, 'providerId'));
      if (!provider) return notFound(api);
      const body = await readJson<Schemas['CreateProviderLocationRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors: FieldError[] = [];
      if (!body.code || !LOCATION_CODE.test(body.code)) {
        errors.push({ field: 'code', code: 'FORMAT' });
      }
      if (!body.name || [...body.name].length < 2) errors.push({ field: 'name', code: 'LENGTH' });
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (
        world().providerLocations.some((l) => l.providerId === provider.id && l.code === body.code)
      ) {
        return problem(api, 409, 'LOCATION_CODE_TAKEN', 'Bu lokasyon kodu sağlayıcıda zaten var');
      }
      const location: StoredProviderLocation = {
        id: world().nextId(),
        tenantId: g.tenantId,
        providerId: provider.id,
        code: body.code,
        name: body.name,
        addressLine: body.addressLine ?? null,
        district: body.district ?? null,
        city: body.city ?? null,
        countryCode: body.countryCode ?? 'TR',
        postalCode: body.postalCode ?? null,
        latitude: body.latitude ?? null,
        longitude: body.longitude ?? null,
        timezone: body.timezone ?? 'Europe/Istanbul',
        phone: body.phone ?? null,
        status: 'ACTIVE',
        rowVersion: 1,
      };
      world().providerLocations.push(location);
      return HttpResponse.json(toProviderLocation(location), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.get(`${ANY}/api/v1/provider-locations/:locationId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.read', false);
      if ('error' in g) return g.error;
      const location = findLocation(g.tenantId, pathParam(params, 'locationId'));
      if (!location) return notFound(api);
      return HttpResponse.json(toProviderLocation(location), {
        headers: { ETag: etagOf(location.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/provider-locations/:locationId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.manage', true);
      if ('error' in g) return g.error;
      const unsupported = requireMergePatch(api, request);
      if (unsupported) return unsupported;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null) {
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      }
      const location = findLocation(g.tenantId, pathParam(params, 'locationId'));
      if (!location) return notFound(api);
      if (location.rowVersion !== expected) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      }
      const patch = await readJson<Record<string, unknown>>(request);
      if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors = immutable(['code'], patch);
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (typeof patch['name'] === 'string') location.name = patch['name'];
      for (const field of ['addressLine', 'district', 'city', 'postalCode', 'phone'] as const) {
        if (field in patch) location[field] = (patch[field] as string | null) ?? null;
      }
      for (const field of ['latitude', 'longitude'] as const) {
        if (field in patch) location[field] = (patch[field] as number | null) ?? null;
      }
      if (typeof patch['countryCode'] === 'string') location.countryCode = patch['countryCode'];
      if (typeof patch['timezone'] === 'string') location.timezone = patch['timezone'];
      if (typeof patch['status'] === 'string') {
        location.status = patch['status'] as Schemas['ProviderLocationStatus'];
      }
      location.rowVersion += 1;
      return HttpResponse.json(toProviderLocation(location), {
        headers: { ETag: etagOf(location.rowVersion) },
      });
    }),

    // --- capabilities -----------------------------------------------------------
    http.get(
      `${ANY}/api/v1/provider-locations/:locationId/capabilities`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'provider.read', false);
        if ('error' in g) return g.error;
        const location = findLocation(g.tenantId, pathParam(params, 'locationId'));
        if (!location) return notFound(api);
        const items = world()
          .providerCapabilities.filter((c) => c.locationId === location.id)
          .map((c) => toProviderCapability(world(), c));
        return HttpResponse.json({ items } satisfies Schemas['ProviderCapabilityList']);
      },
    ),

    http.put(
      `${ANY}/api/v1/provider-locations/:locationId/capabilities`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'provider.manage', true);
        if ('error' in g) return g.error;
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null) {
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        }
        const location = findLocation(g.tenantId, pathParam(params, 'locationId'));
        if (!location) return notFound(api);
        if (location.rowVersion !== expected) {
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        }
        const body = await readJson<Schemas['ReplaceProviderCapabilitiesRequest']>(request);
        if (!body?.items) {
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        }
        const errors: FieldError[] = [];
        body.items.forEach((c, i) => {
          const named = [c.serviceDefinitionId, c.serviceCategoryId].filter(Boolean).length;
          if (named !== 1) {
            errors.push({ field: `items[${i}].serviceDefinitionId`, code: 'EXACTLY_ONE_TARGET' });
          }
          if (!c.validFrom) errors.push({ field: `items[${i}].validFrom`, code: 'REQUIRED' });
          if (
            c.serviceDefinitionId &&
            !world().serviceDefinitions.some(
              (d) => d.id === c.serviceDefinitionId && d.tenantId === g.tenantId,
            )
          ) {
            errors.push({ field: `items[${i}].serviceDefinitionId`, code: 'NOT_FOUND' });
          }
          if (
            c.serviceCategoryId &&
            !world().serviceCategories.some(
              (x) => x.id === c.serviceCategoryId && x.tenantId === g.tenantId,
            )
          ) {
            errors.push({ field: `items[${i}].serviceCategoryId`, code: 'NOT_FOUND' });
          }
        });
        if (errors.length > 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
        }
        for (let i = 0; i < body.items.length; i++) {
          for (let j = i + 1; j < body.items.length; j++) {
            const a = body.items[i]!;
            const b = body.items[j]!;
            const sameTarget =
              (a.serviceDefinitionId !== undefined &&
                a.serviceDefinitionId === b.serviceDefinitionId) ||
              (a.serviceCategoryId !== undefined && a.serviceCategoryId === b.serviceCategoryId);
            if (!sameTarget) continue;
            if (periodsOverlap(a.validFrom, a.validTo ?? null, b.validFrom, b.validTo ?? null)) {
              return problem(
                api,
                409,
                'CAPABILITY_OVERLAP',
                'Aynı hizmet için çakışan yetkinlik dönemi var',
              );
            }
          }
        }
        world().providerCapabilities = world().providerCapabilities.filter(
          (c) => c.locationId !== location.id,
        );
        const stored: StoredProviderCapability[] = body.items.map((c) => ({
          id: world().nextId(),
          tenantId: g.tenantId,
          locationId: location.id,
          serviceDefinitionId: c.serviceDefinitionId ?? null,
          serviceCategoryId: c.serviceCategoryId ?? null,
          validFrom: c.validFrom,
          validTo: c.validTo ?? null,
          notes: c.notes ?? null,
        }));
        world().providerCapabilities.push(...stored);
        location.rowVersion += 1;
        return HttpResponse.json(
          {
            items: stored.map((c) => toProviderCapability(world(), c)),
          } satisfies Schemas['ProviderCapabilityList'],
          { headers: { ETag: etagOf(location.rowVersion) } },
        );
      },
    ),

    // --- practitioners ----------------------------------------------------------
    http.get(`${ANY}/api/v1/providers/:providerId/practitioners`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.read', false);
      if ('error' in g) return g.error;
      const provider = findProvider(g.tenantId, pathParam(params, 'providerId'));
      if (!provider) return notFound(api);
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
      const branchCode = url.searchParams.get('branchCode');
      const q = (url.searchParams.get('q') ?? '').trim().toLocaleLowerCase('tr');
      const rows = world()
        .practitioners.filter((p) => p.providerId === provider.id)
        .filter((p) => !status || p.status === status)
        .filter((p) => !branchCode || p.branchCode === branchCode)
        .filter((p) => !q || p.fullName.toLocaleLowerCase('tr').includes(q));
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map((p) => toPractitioner(world(), p)),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['PractitionerPage']);
    }),

    http.post(`${ANY}/api/v1/providers/:providerId/practitioners`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.practitioner.manage', true);
      if ('error' in g) return g.error;
      const provider = findProvider(g.tenantId, pathParam(params, 'providerId'));
      if (!provider) return notFound(api);
      const body = await readJson<Schemas['CreatePractitionerRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors: FieldError[] = [];
      if (!body.fullName || [...body.fullName].length < 2) {
        errors.push({ field: 'fullName', code: 'LENGTH' });
      }
      if (!body.registrationAuthority) {
        errors.push({ field: 'registrationAuthority', code: 'REQUIRED' });
      }
      if (!body.registrationNumber) {
        errors.push({ field: 'registrationNumber', code: 'REQUIRED' });
      }
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (
        world().practitioners.some(
          (p) =>
            p.tenantId === g.tenantId &&
            p.registrationAuthority === body.registrationAuthority &&
            p.registrationNumber === body.registrationNumber,
        )
      ) {
        return problem(
          api,
          409,
          'PRACTITIONER_REGISTRATION_TAKEN',
          'Bu tescil numarası zaten kayıtlı',
        );
      }
      const practitioner: StoredPractitioner = {
        id: world().nextId(),
        tenantId: g.tenantId,
        providerId: provider.id,
        personId: body.personId ?? null,
        fullName: body.fullName,
        title: body.title ?? null,
        branchCode: body.branchCode ?? null,
        registrationAuthority: body.registrationAuthority,
        registrationNumber: body.registrationNumber,
        validFrom: body.validFrom ?? null,
        validTo: body.validTo ?? null,
        status: 'ACTIVE',
        locations: [],
        rowVersion: 1,
      };
      world().practitioners.push(practitioner);
      return HttpResponse.json(toPractitioner(world(), practitioner), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    // Registered before /practitioners/:practitionerId so the literal segment wins.
    http.post(`${ANY}/api/v1/practitioners/search-by-registration`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.practitioner.manage', true);
      if ('error' in g) return g.error;
      if (!hasStepUp(g.session)) return stepUpRequired(api);
      const body = await readJson<Schemas['PractitionerRegistrationSearchRequest']>(request);
      if (!body?.registrationNumber || !body.registrationAuthority) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'registrationNumber', code: 'REQUIRED' }],
        });
      }
      const found = world().practitioners.find(
        (p) =>
          p.tenantId === g.tenantId &&
          p.registrationAuthority === body.registrationAuthority &&
          p.registrationNumber === body.registrationNumber,
      );
      if (!found) return notFound(api);
      return HttpResponse.json(toPractitioner(world(), found));
    }),

    http.get(`${ANY}/api/v1/practitioners/:practitionerId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.read', false);
      if ('error' in g) return g.error;
      const practitioner = findPractitioner(g.tenantId, pathParam(params, 'practitionerId'));
      if (!practitioner) return notFound(api);
      return HttpResponse.json(toPractitioner(world(), practitioner), {
        headers: { ETag: etagOf(practitioner.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/practitioners/:practitionerId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'provider.practitioner.manage', true);
      if ('error' in g) return g.error;
      const unsupported = requireMergePatch(api, request);
      if (unsupported) return unsupported;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null) {
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      }
      const practitioner = findPractitioner(g.tenantId, pathParam(params, 'practitionerId'));
      if (!practitioner) return notFound(api);
      if (practitioner.rowVersion !== expected) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      }
      const patch = await readJson<Record<string, unknown>>(request);
      if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      // A registration is ended and re-registered, never rewritten.
      const errors = immutable(['registrationAuthority', 'registrationNumber'], patch);
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (typeof patch['fullName'] === 'string') practitioner.fullName = patch['fullName'];
      for (const field of ['title', 'branchCode', 'personId', 'validFrom', 'validTo'] as const) {
        if (field in patch) practitioner[field] = (patch[field] as string | null) ?? null;
      }
      if (typeof patch['status'] === 'string') {
        practitioner.status = patch['status'] as Schemas['PractitionerStatus'];
      }
      practitioner.rowVersion += 1;
      return HttpResponse.json(toPractitioner(world(), practitioner), {
        headers: { ETag: etagOf(practitioner.rowVersion) },
      });
    }),

    http.put(
      `${ANY}/api/v1/practitioners/:practitionerId/locations`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'provider.practitioner.manage', true);
        if ('error' in g) return g.error;
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null) {
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        }
        const practitioner = findPractitioner(g.tenantId, pathParam(params, 'practitionerId'));
        if (!practitioner) return notFound(api);
        if (practitioner.rowVersion !== expected) {
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        }
        const body = await readJson<Schemas['ReplacePractitionerLocationsRequest']>(request);
        if (!body?.items) {
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        }
        const errors: FieldError[] = [];
        body.items.forEach((l, i) => {
          const location = findLocation(g.tenantId, l.locationId);
          if (!location || location.providerId !== practitioner.providerId) {
            errors.push({ field: `items[${i}].locationId`, code: 'PROVIDER_MISMATCH' });
          }
          if (!l.role) errors.push({ field: `items[${i}].role`, code: 'REQUIRED' });
          if (!l.validFrom) errors.push({ field: `items[${i}].validFrom`, code: 'REQUIRED' });
        });
        if (errors.length > 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
        }
        for (let i = 0; i < body.items.length; i++) {
          for (let j = i + 1; j < body.items.length; j++) {
            const a = body.items[i]!;
            const b = body.items[j]!;
            if (a.locationId !== b.locationId || a.role !== b.role) continue;
            if (periodsOverlap(a.validFrom, a.validTo ?? null, b.validFrom, b.validTo ?? null)) {
              return problem(
                api,
                409,
                'PRACTITIONER_ASSIGNMENT_OVERLAP',
                'Aynı lokasyon ve rol için çakışan görevlendirme var',
              );
            }
          }
        }
        practitioner.locations = body.items.map((l) => ({
          id: world().nextId(),
          practitionerId: practitioner.id,
          locationId: l.locationId,
          role: l.role,
          validFrom: l.validFrom,
          validTo: l.validTo ?? null,
        }));
        practitioner.rowVersion += 1;
        return HttpResponse.json(
          {
            items: toPractitioner(world(), practitioner).locations ?? [],
          } satisfies Schemas['PractitionerLocationList'],
          { headers: { ETag: etagOf(practitioner.rowVersion) } },
        );
      },
    ),
  ];
}
