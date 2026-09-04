/**
 * MSW handlers for the M3 contract surface: contracts, their versions, the price sheet of
 * a version (lists, items, packages, quotas, payment term) and the deterministic price
 * selection every quote, authorization and claim reads.
 *
 * Two rules carry the whole module. A version that is not DRAFT is frozen — every write
 * answers 409 CONTRACT_VERSION_IMMUTABLE — and publishing needs a step-up and an actor
 * other than the submitter. A draft is an unagreed proposal, so a caller holding only
 * `contract.read` is not told it exists: the list omits it and a direct read answers 404.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  categoryCovers,
  fromMicros,
  pseudoHash,
  toContract,
  toContractVersion,
  toContractVersionSummary,
  toPackageDefinition,
  toPaymentTerm,
  toPriceItem,
  toPriceList,
  toProviderQuota,
  type Decimal,
  type MockWorld,
  type StoredContract,
  type StoredContractVersion,
  type StoredPackageDefinition,
  type StoredPriceItem,
  type StoredPriceList,
  type StoredProviderQuota,
} from './data';
import type { MockApi, MockSession } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  hasPermission,
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

const CONTRACT_CODE = /^[A-Z][A-Z0-9_-]{1,39}$/;
const LIST_CODE = /^[A-Z][A-Z0-9_-]{1,39}$/;
const CURRENCY = /^[A-Z]{3}$/;

/** Specificity tiers of the price ladder; a nearer ancestor category beats a further one. */
const TIER_DEFINITION = 300;
const TIER_PACKAGE = 200;
const TIER_CATEGORY = 100;

/** Bit of the weekday mask for a date; bit 1 is Monday and bit 64 is Sunday. */
function weekdayBit(date: string): number {
  const day = new Date(`${date}T00:00:00.000Z`).getUTCDay();
  return 1 << ((day + 6) % 7);
}

/** What the caller wants a price for. Exactly one target is named. */
export interface PriceResolutionInput {
  serviceDate: string;
  providerProfileId: string;
  serviceDefinitionId?: string | null;
  packageDefinitionId?: string | null;
  locationId?: string | null;
}

/** The resolution plus the stored row behind it, which the quote needs to price a line. */
export interface PriceResolution {
  result: Schemas['ResolvePriceResult'];
  winner: StoredPriceItem | null;
  contractVersionId: string | null;
  currencyCode: string | null;
}

function toResolvedPrice(
  item: StoredPriceItem,
  list: StoredPriceList,
  version: StoredContractVersion,
  contract: StoredContract,
  matchedVia: Schemas['PriceMatchTarget'],
): Schemas['ResolvedPrice'] {
  return {
    priceItemId: item.id,
    priceListId: list.id,
    priceListCode: list.code,
    contractVersionId: version.id,
    contractId: contract.id,
    contractCode: contract.code,
    versionNo: version.versionNo,
    currencyCode: version.currencyCode,
    locationId: item.locationId,
    unitType: item.unitType,
    pricingMethod: item.pricingMethod,
    amount: item.amount,
    percent: item.percent,
    formulaKey: item.formulaKey,
    minAmount: item.minAmount,
    maxAmount: item.maxAmount,
    memberShareMethod: item.memberShareMethod,
    memberShareAmount: item.memberShareAmount,
    memberSharePercent: item.memberSharePercent,
    matchedVia,
  };
}

/** The declared target of a row, used to label a candidate that did not match. */
function declaredTarget(item: StoredPriceItem): Schemas['PriceMatchTarget'] {
  if (item.serviceDefinitionId) return 'DEFINITION';
  if (item.packageDefinitionId) return 'PACKAGE';
  return 'CATEGORY';
}

/**
 * The deterministic price selection. Only published versions of ACTIVE contracts whose
 * period contains the service date are considered; the winner is the most specific
 * candidate, and equal candidates are never resolved by picking one — a silent financial
 * error is worse than an answer of "somebody has to look at this".
 */
export function resolveContractPrice(
  world: MockWorld,
  tenantId: string,
  input: PriceResolutionInput,
): PriceResolution {
  const definition = input.serviceDefinitionId
    ? world.serviceDefinitions.find(
        (d) => d.id === input.serviceDefinitionId && d.tenantId === tenantId,
      )
    : undefined;
  const considered: Schemas['ScoredPriceCandidate'][] = [];
  const matches: {
    candidate: Schemas['ScoredPriceCandidate'];
    resolved: Schemas['ResolvedPrice'];
    item: StoredPriceItem;
    version: StoredContractVersion;
  }[] = [];

  for (const contract of world.contracts) {
    if (contract.tenantId !== tenantId) continue;
    if (contract.status !== 'ACTIVE') continue;
    if (contract.providerProfileId !== input.providerProfileId) continue;
    for (const version of world.contractVersions) {
      if (version.contractId !== contract.id) continue;
      if (version.status !== 'PUBLISHED') continue;
      if (version.validFrom === null) continue;
      if (!withinPeriod(input.serviceDate, version.validFrom, version.validTo)) continue;
      for (const list of world.priceLists) {
        if (list.contractVersionId !== version.id) continue;
        const seasonMismatch =
          (list.seasonFrom !== null || list.seasonTo !== null) &&
          !withinPeriod(input.serviceDate, list.seasonFrom, list.seasonTo);
        const weekdayMismatch =
          list.weekdayMask !== null && (list.weekdayMask & weekdayBit(input.serviceDate)) === 0;
        for (const item of world.priceItems) {
          if (item.priceListId !== list.id) continue;
          const base = {
            priceItemId: item.id,
            priceListId: list.id,
            contractVersionId: version.id,
            itemPriority: item.priority,
            listPriority: list.priority,
          };
          const reject = (excluded: string): void => {
            considered.push({
              ...base,
              score: 0,
              matched: false,
              excluded,
              matchedVia: declaredTarget(item),
            });
          };
          if (seasonMismatch) {
            reject('SEASON_MISMATCH');
            continue;
          }
          if (weekdayMismatch) {
            reject('WEEKDAY_MISMATCH');
            continue;
          }
          if (!withinPeriod(input.serviceDate, item.validFrom, item.validTo)) {
            reject('PERIOD_MISMATCH');
            continue;
          }
          if (item.locationId !== null && item.locationId !== (input.locationId ?? null)) {
            reject('LOCATION_MISMATCH');
            continue;
          }
          let tier: number | null = null;
          let matchedVia: Schemas['PriceMatchTarget'] = 'DEFINITION';
          if (input.packageDefinitionId) {
            if (item.packageDefinitionId === input.packageDefinitionId) {
              tier = TIER_PACKAGE;
              matchedVia = 'PACKAGE';
            }
          } else if (definition) {
            if (item.serviceDefinitionId === definition.id) {
              tier = TIER_DEFINITION;
              matchedVia = 'DEFINITION';
            } else if (item.packageDefinitionId) {
              const pkg = world.packageDefinitions.find((p) => p.id === item.packageDefinitionId);
              if (pkg?.lines.some((l) => l.serviceDefinitionId === definition.id)) {
                tier = TIER_PACKAGE;
                matchedVia = 'PACKAGE';
              }
            } else if (item.serviceCategoryId) {
              const distance = categoryCovers(world, item.serviceCategoryId, definition.categoryId);
              if (distance !== null) {
                tier = TIER_CATEGORY - distance;
                matchedVia = 'CATEGORY';
              }
            }
          }
          if (tier === null) {
            reject('SERVICE_MISMATCH');
            continue;
          }
          const candidate: Schemas['ScoredPriceCandidate'] = {
            ...base,
            score: tier * 10 + (item.locationId !== null ? 1 : 0),
            matched: true,
            excluded: null,
            matchedVia,
          };
          considered.push(candidate);
          matches.push({
            candidate,
            resolved: toResolvedPrice(item, list, version, contract, matchedVia),
            item,
            version,
          });
        }
      }
    }
  }

  if (matches.length === 0) {
    return {
      result: {
        outcome: 'NOT_FOUND',
        reason: 'PRICE_NOT_FOUND',
        serviceDate: input.serviceDate,
        tied: [],
        considered,
      },
      winner: null,
      contractVersionId: null,
      currencyCode: null,
    };
  }
  matches.sort(
    (a, b) =>
      b.candidate.score - a.candidate.score ||
      (b.candidate.itemPriority ?? 0) - (a.candidate.itemPriority ?? 0) ||
      (b.candidate.listPriority ?? 0) - (a.candidate.listPriority ?? 0),
  );
  const best = matches[0]!;
  const top = matches.filter(
    (m) =>
      m.candidate.score === best.candidate.score &&
      m.candidate.itemPriority === best.candidate.itemPriority &&
      m.candidate.listPriority === best.candidate.listPriority,
  );
  if (top.length > 1) {
    return {
      result: {
        outcome: 'REVIEW_REQUIRED',
        reason: 'PRICE_AMBIGUOUS',
        serviceDate: input.serviceDate,
        tied: top.map((m) => m.resolved),
        considered,
      },
      winner: null,
      contractVersionId: best.version.id,
      currencyCode: best.version.currencyCode,
    };
  }
  return {
    result: {
      outcome: 'MATCHED',
      reason: null,
      serviceDate: input.serviceDate,
      winner: best.resolved,
      tied: [],
      considered,
    },
    winner: best.item,
    contractVersionId: best.version.id,
    currencyCode: best.version.currencyCode,
  };
}

export function contractHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  const findContract = (tenantId: string, id: string): StoredContract | undefined =>
    world().contracts.find((c) => c.id === id && c.tenantId === tenantId);
  const findList = (tenantId: string, id: string): StoredPriceList | undefined =>
    world().priceLists.find((l) => l.id === id && l.tenantId === tenantId);

  /** A version the caller may see: a draft is invisible without `contract.manage`. */
  const visibleVersion = (
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredContractVersion | undefined => {
    const version = world().contractVersions.find((v) => v.id === id && v.tenantId === tenantId);
    if (!version) return undefined;
    const agreed = version.status === 'PUBLISHED' || version.status === 'RETIRED';
    if (agreed || hasPermission(api, session, tenantId, 'contract.manage')) return version;
    return undefined;
  };

  /** Loads a DRAFT version for a set write, or the response that refuses the write. */
  const draftForWrite = (
    request: Request,
    tenantId: string,
    versionId: string,
  ): StoredContractVersion | Response => {
    const version = world().contractVersions.find(
      (v) => v.id === versionId && v.tenantId === tenantId,
    );
    if (!version) return notFound(api);
    const expected = parseIfMatch(request.headers.get('If-Match'));
    if (expected === null) {
      return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
    }
    if (version.status !== 'DRAFT') {
      return problem(
        api,
        409,
        'CONTRACT_VERSION_IMMUTABLE',
        'Yalnız taslak sürümün fiyat içeriği değiştirilebilir',
      );
    }
    if (version.rowVersion !== expected) {
      return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
    }
    return version;
  };

  const immutable = (fields: string[], patch: Record<string, unknown>): FieldError[] =>
    fields.filter((f) => f in patch).map((field) => ({ field, code: 'IMMUTABLE' }));

  /** Validates one price item body; returns the field errors it produced. */
  const validatePriceItem = (
    tenantId: string,
    versionId: string,
    i: number,
    item: Schemas['PriceItemInput'],
  ): FieldError[] => {
    const errors: FieldError[] = [];
    const targets = [
      item.serviceDefinitionId,
      item.serviceCategoryId,
      item.packageDefinitionId,
    ].filter(Boolean);
    if (targets.length !== 1) {
      errors.push({ field: `items[${i}].serviceDefinitionId`, code: 'EXACTLY_ONE_TARGET' });
    }
    if (
      item.serviceDefinitionId &&
      !world().serviceDefinitions.some(
        (d) => d.id === item.serviceDefinitionId && d.tenantId === tenantId,
      )
    ) {
      errors.push({ field: `items[${i}].serviceDefinitionId`, code: 'NOT_FOUND' });
    }
    if (
      item.serviceCategoryId &&
      !world().serviceCategories.some(
        (c) => c.id === item.serviceCategoryId && c.tenantId === tenantId,
      )
    ) {
      errors.push({ field: `items[${i}].serviceCategoryId`, code: 'NOT_FOUND' });
    }
    if (
      item.packageDefinitionId &&
      !world().packageDefinitions.some(
        (p) => p.id === item.packageDefinitionId && p.contractVersionId === versionId,
      )
    ) {
      errors.push({ field: `items[${i}].packageDefinitionId`, code: 'NOT_FOUND' });
    }
    if (!item.validFrom) errors.push({ field: `items[${i}].validFrom`, code: 'REQUIRED' });
    if (!item.unitType) errors.push({ field: `items[${i}].unitType`, code: 'REQUIRED' });
    if (
      (item.pricingMethod === 'FIXED' || item.pricingMethod === 'UNIT') &&
      item.amount === undefined
    ) {
      errors.push({ field: `items[${i}].amount`, code: 'AMOUNT_REQUIRED' });
    }
    if (item.pricingMethod === 'PERCENT_OF_LIST' && item.percent === undefined) {
      errors.push({ field: `items[${i}].percent`, code: 'PERCENT_REQUIRED' });
    }
    if (item.pricingMethod === 'FORMULA' && !item.formulaKey) {
      errors.push({ field: `items[${i}].formulaKey`, code: 'FORMULA_KEY_REQUIRED' });
    }
    if (item.memberShareMethod === 'FIXED' && item.memberShareAmount === undefined) {
      errors.push({ field: `items[${i}].memberShareAmount`, code: 'REQUIRED' });
    }
    if (item.memberShareMethod === 'PERCENT' && item.memberSharePercent === undefined) {
      errors.push({ field: `items[${i}].memberSharePercent`, code: 'REQUIRED' });
    }
    return errors;
  };

  /** Amounts are stored exactly as they arrived: decimal strings, never parsed. */
  const decimalOrNull = (value: string | undefined): Decimal | null => value ?? null;

  return [
    // --- contracts --------------------------------------------------------------
    http.get(`${ANY}/api/v1/contracts`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'contract.read', false);
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
      const providerProfileId = url.searchParams.get('providerProfileId');
      const payerOrganizationId = url.searchParams.get('payerOrganizationId');
      const domainCode = url.searchParams.get('domainCode');
      const status = url.searchParams.get('status');
      const q = (url.searchParams.get('q') ?? '').trim().toLocaleLowerCase('tr');
      const rows = world()
        .contracts.filter((c) => c.tenantId === g.tenantId)
        .filter((c) => !providerProfileId || c.providerProfileId === providerProfileId)
        .filter((c) => !payerOrganizationId || c.payerOrganizationId === payerOrganizationId)
        .filter((c) => !domainCode || c.domainCode === domainCode)
        .filter((c) => !status || c.status === status)
        .filter(
          (c) =>
            !q ||
            c.code.toLocaleLowerCase('tr').includes(q) ||
            c.name.toLocaleLowerCase('tr').includes(q),
        );
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map((c) => toContract(world(), c)),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['ContractPage']);
    }),

    http.post(`${ANY}/api/v1/contracts`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'contract.manage', true);
      if ('error' in g) return g.error;
      const body = await readJson<Schemas['CreateContractRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors: FieldError[] = [];
      if (!body.code || !CONTRACT_CODE.test(body.code)) {
        errors.push({ field: 'code', code: 'FORMAT' });
      }
      if (!body.name || [...body.name].length < 2) errors.push({ field: 'name', code: 'LENGTH' });
      if (!body.domainCode) errors.push({ field: 'domainCode', code: 'REQUIRED' });
      const payer = world().relationships.find(
        (r) => r.id === body.payerOrganizationId && r.tenantId === g.tenantId,
      );
      if (!payer) errors.push({ field: 'payerOrganizationId', code: 'NOT_FOUND' });
      const provider = world().providers.find(
        (p) => p.id === body.providerProfileId && p.tenantId === g.tenantId,
      );
      if (!provider) errors.push({ field: 'providerProfileId', code: 'NOT_FOUND' });
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (world().contracts.some((c) => c.tenantId === g.tenantId && c.code === body.code)) {
        return problem(api, 409, 'CONTRACT_CODE_TAKEN', 'Bu sözleşme kodu zaten kullanılıyor');
      }
      const contract: StoredContract = {
        id: world().nextId(),
        tenantId: g.tenantId,
        code: body.code,
        name: body.name,
        payerOrganizationId: body.payerOrganizationId,
        providerProfileId: body.providerProfileId,
        sponsorOrganizationId: body.sponsorOrganizationId ?? null,
        domainCode: body.domainCode,
        status: 'DRAFT',
        rowVersion: 1,
      };
      world().contracts.push(contract);
      return HttpResponse.json(toContract(world(), contract), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.get(`${ANY}/api/v1/contracts/:contractId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'contract.read', false);
      if ('error' in g) return g.error;
      const contract = findContract(g.tenantId, pathParam(params, 'contractId'));
      if (!contract) return notFound(api);
      return HttpResponse.json(toContract(world(), contract), {
        headers: { ETag: etagOf(contract.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/contracts/:contractId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'contract.manage', true);
      if ('error' in g) return g.error;
      const unsupported = requireMergePatch(api, request);
      if (unsupported) return unsupported;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null) {
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      }
      const contract = findContract(g.tenantId, pathParam(params, 'contractId'));
      if (!contract) return notFound(api);
      if (contract.rowVersion !== expected) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      }
      const patch = await readJson<Record<string, unknown>>(request);
      if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors = immutable(
        ['code', 'payerOrganizationId', 'providerProfileId', 'domainCode'],
        patch,
      );
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (typeof patch['name'] === 'string') contract.name = patch['name'];
      if ('sponsorOrganizationId' in patch) {
        contract.sponsorOrganizationId = (patch['sponsorOrganizationId'] as string | null) ?? null;
      }
      if (typeof patch['status'] === 'string') {
        contract.status = patch['status'] as Schemas['ContractStatus'];
      }
      contract.rowVersion += 1;
      return HttpResponse.json(toContract(world(), contract), {
        headers: { ETag: etagOf(contract.rowVersion) },
      });
    }),

    // --- contract versions ------------------------------------------------------
    http.get(`${ANY}/api/v1/contracts/:contractId/versions`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'contract.read', false);
      if ('error' in g) return g.error;
      const contract = findContract(g.tenantId, pathParam(params, 'contractId'));
      if (!contract) return notFound(api);
      const maySeeDrafts = hasPermission(api, g.session, g.tenantId, 'contract.manage');
      const items = world()
        .contractVersions.filter((v) => v.contractId === contract.id)
        .filter((v) => maySeeDrafts || v.status === 'PUBLISHED' || v.status === 'RETIRED')
        .sort((a, b) => b.versionNo - a.versionNo)
        .map(toContractVersionSummary);
      return HttpResponse.json({ items } satisfies Schemas['ContractVersionList']);
    }),

    http.post(`${ANY}/api/v1/contracts/:contractId/versions`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'contract.manage', true);
      if ('error' in g) return g.error;
      const contract = findContract(g.tenantId, pathParam(params, 'contractId'));
      if (!contract) return notFound(api);
      const body = (await readJson<Schemas['CreateContractVersionRequest']>(request)) ?? {};
      if (body.currencyCode && !CURRENCY.test(body.currencyCode)) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'currencyCode', code: 'FORMAT' }],
        });
      }
      const siblings = world().contractVersions.filter((v) => v.contractId === contract.id);
      const source = body.copyFromVersionId
        ? siblings.find((v) => v.id === body.copyFromVersionId)
        : undefined;
      if (body.copyFromVersionId && !source) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'copyFromVersionId', code: 'NOT_FOUND' }],
        });
      }
      const version: StoredContractVersion = {
        id: world().nextId(),
        tenantId: g.tenantId,
        contractId: contract.id,
        versionNo: siblings.reduce((max, v) => Math.max(max, v.versionNo), 0) + 1,
        status: 'DRAFT',
        validFrom: body.validFrom ?? null,
        validTo: body.validTo ?? null,
        currencyCode: body.currencyCode ?? source?.currencyCode ?? 'TRY',
        notes: body.notes ?? null,
        configurationHash: null,
        submittedAt: null,
        submittedBy: null,
        publishedAt: null,
        publishedBy: null,
        reviewComment: null,
        retireReasonCode: null,
        rowVersion: 1,
      };
      world().contractVersions.push(version);
      if (source) {
        // Copying starts a yearly revision from last year's sheet: lists, items, packages,
        // quotas and the payment term, with fresh ids and a reset consumed counter.
        const packageIds = new Map<string, string>();
        for (const pkg of world().packageDefinitions.filter(
          (p) => p.contractVersionId === source.id,
        )) {
          const id = world().nextId();
          packageIds.set(pkg.id, id);
          world().packageDefinitions.push({
            ...pkg,
            id,
            contractVersionId: version.id,
            lines: pkg.lines.map((l) => ({ ...l })),
          });
        }
        for (const list of world().priceLists.filter((l) => l.contractVersionId === source.id)) {
          const listId = world().nextId();
          world().priceLists.push({
            ...list,
            id: listId,
            contractVersionId: version.id,
            rowVersion: 1,
          });
          for (const item of world().priceItems.filter((i) => i.priceListId === list.id)) {
            world().priceItems.push({
              ...item,
              id: world().nextId(),
              priceListId: listId,
              packageDefinitionId: item.packageDefinitionId
                ? (packageIds.get(item.packageDefinitionId) ?? null)
                : null,
            });
          }
        }
        for (const quota of world().providerQuotas.filter(
          (q) => q.contractVersionId === source.id,
        )) {
          world().providerQuotas.push({
            ...quota,
            id: world().nextId(),
            contractVersionId: version.id,
            consumed: '0.000000',
          });
        }
        const term = world().paymentTerms.find((t) => t.contractVersionId === source.id);
        if (term) {
          world().paymentTerms.push({
            ...term,
            id: world().nextId(),
            contractVersionId: version.id,
            rowVersion: 1,
          });
        }
      }
      return HttpResponse.json(toContractVersion(world(), version), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.get(`${ANY}/api/v1/contract-versions/:contractVersionId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'contract.read', false);
      if ('error' in g) return g.error;
      const version = visibleVersion(g.session, g.tenantId, pathParam(params, 'contractVersionId'));
      // A draft answers 404 rather than 403: a price nobody has agreed to yet does not
      // announce its own existence.
      if (!version) return notFound(api);
      return HttpResponse.json(toContractVersion(world(), version), {
        headers: { ETag: etagOf(version.rowVersion) },
      });
    }),

    http.patch(
      `${ANY}/api/v1/contract-versions/:contractVersionId`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.manage', true);
        if ('error' in g) return g.error;
        const unsupported = requireMergePatch(api, request);
        if (unsupported) return unsupported;
        const loaded = draftForWrite(request, g.tenantId, pathParam(params, 'contractVersionId'));
        if (loaded instanceof Response) return loaded;
        const patch = await readJson<Record<string, unknown>>(request);
        if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        if (typeof patch['currencyCode'] === 'string' && !CURRENCY.test(patch['currencyCode'])) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: [{ field: 'currencyCode', code: 'FORMAT' }],
          });
        }
        if ('validFrom' in patch) loaded.validFrom = (patch['validFrom'] as string | null) ?? null;
        if ('validTo' in patch) loaded.validTo = (patch['validTo'] as string | null) ?? null;
        if (typeof patch['currencyCode'] === 'string') loaded.currencyCode = patch['currencyCode'];
        if ('notes' in patch) loaded.notes = (patch['notes'] as string | null) ?? null;
        loaded.rowVersion += 1;
        return HttpResponse.json(toContractVersion(world(), loaded), {
          headers: { ETag: etagOf(loaded.rowVersion) },
        });
      },
    ),

    http.post(
      `${ANY}/api/v1/contract-versions/:contractVersionId/submit`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.manage', true);
        if ('error' in g) return g.error;
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null) {
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        }
        const version = world().contractVersions.find(
          (v) => v.id === pathParam(params, 'contractVersionId') && v.tenantId === g.tenantId,
        );
        if (!version) return notFound(api);
        if (version.status !== 'DRAFT') {
          return problem(
            api,
            409,
            'CONTRACT_VERSION_IMMUTABLE',
            'Yalnız taslak sürüm incelemeye gönderilebilir',
          );
        }
        if (version.rowVersion !== expected) {
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        }
        const errors: FieldError[] = [];
        if (version.validFrom === null) errors.push({ field: 'validFrom', code: 'REQUIRED' });
        const lists = world().priceLists.filter((l) => l.contractVersionId === version.id);
        const hasItems = lists.some((l) => world().priceItems.some((i) => i.priceListId === l.id));
        if (!hasItems) errors.push({ field: 'priceLists', code: 'PRICE_ITEMS_REQUIRED' });
        if (errors.length > 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
        }
        const body = (await readJson<Schemas['ReviewComment']>(request)) ?? {};
        version.status = 'UNDER_REVIEW';
        version.submittedAt = new Date().toISOString();
        version.submittedBy = g.session.account.actorId;
        version.reviewComment = body.comment ?? null;
        version.rowVersion += 1;
        return HttpResponse.json(toContractVersion(world(), version), {
          headers: { ETag: etagOf(version.rowVersion) },
        });
      },
    ),

    http.post(
      `${ANY}/api/v1/contract-versions/:contractVersionId/publish`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.publish', true);
        if ('error' in g) return g.error;
        if (!hasStepUp(g.session)) return stepUpRequired(api);
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null) {
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        }
        const version = world().contractVersions.find(
          (v) => v.id === pathParam(params, 'contractVersionId') && v.tenantId === g.tenantId,
        );
        if (!version) return notFound(api);
        if (version.status !== 'UNDER_REVIEW') {
          return problem(
            api,
            409,
            'CONTRACT_VERSION_IMMUTABLE',
            'Yalnız incelemedeki sürüm yayınlanabilir',
          );
        }
        if (version.rowVersion !== expected) {
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        }
        if (version.submittedBy === g.session.account.actorId) {
          return problem(
            api,
            403,
            'MAKER_CHECKER_SAME_ACTOR',
            'Gönderen ile yayınlayan aynı kişi olamaz',
          );
        }
        const clash = world().contractVersions.find(
          (v) =>
            v.id !== version.id &&
            v.contractId === version.contractId &&
            v.status === 'PUBLISHED' &&
            v.validFrom !== null &&
            version.validFrom !== null &&
            periodsOverlap(v.validFrom, v.validTo, version.validFrom, version.validTo),
        );
        if (clash) {
          return problem(
            api,
            409,
            'CONTRACT_VERSION_OVERLAP',
            'Bu tarih aralığında yayında başka bir sürüm var',
          );
        }
        const body = (await readJson<Schemas['ReviewComment']>(request)) ?? {};
        const itemCodes = world()
          .priceLists.filter((l) => l.contractVersionId === version.id)
          .flatMap((l) => world().priceItems.filter((i) => i.priceListId === l.id))
          .map((i) => `${i.id}:${i.amount ?? i.percent ?? i.formulaKey ?? ''}`)
          .join(',');
        version.status = 'PUBLISHED';
        version.publishedAt = new Date().toISOString();
        version.publishedBy = g.session.account.actorId;
        version.configurationHash = pseudoHash(
          `${version.contractId}:${version.versionNo}:${itemCodes}`,
        );
        if (body.comment) version.reviewComment = body.comment;
        version.rowVersion += 1;
        return HttpResponse.json(toContractVersion(world(), version), {
          headers: { ETag: etagOf(version.rowVersion) },
        });
      },
    ),

    http.post(
      `${ANY}/api/v1/contract-versions/:contractVersionId/retire`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.publish', true);
        if ('error' in g) return g.error;
        if (!hasStepUp(g.session)) return stepUpRequired(api);
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null) {
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        }
        const version = world().contractVersions.find(
          (v) => v.id === pathParam(params, 'contractVersionId') && v.tenantId === g.tenantId,
        );
        if (!version) return notFound(api);
        if (version.status !== 'PUBLISHED') {
          return problem(
            api,
            409,
            'CONTRACT_VERSION_IMMUTABLE',
            'Yalnız yayında olan sürüm kullanımdan çıkarılabilir',
          );
        }
        if (version.rowVersion !== expected) {
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        }
        if (version.publishedBy === g.session.account.actorId) {
          return problem(
            api,
            403,
            'MAKER_CHECKER_SAME_ACTOR',
            'Yayınlayan ile kullanımdan çıkaran aynı kişi olamaz',
          );
        }
        const body = await readJson<Schemas['ReasonCommand']>(request);
        if (!body?.reasonCode) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: [{ field: 'reasonCode', code: 'REQUIRED' }],
          });
        }
        version.status = 'RETIRED';
        version.retireReasonCode = body.reasonCode;
        version.rowVersion += 1;
        return HttpResponse.json(toContractVersion(world(), version), {
          headers: { ETag: etagOf(version.rowVersion) },
        });
      },
    ),

    // --- price lists and items --------------------------------------------------
    http.put(
      `${ANY}/api/v1/contract-versions/:contractVersionId/price-lists`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.manage', true);
        if ('error' in g) return g.error;
        const loaded = draftForWrite(request, g.tenantId, pathParam(params, 'contractVersionId'));
        if (loaded instanceof Response) return loaded;
        const body = await readJson<Schemas['ReplacePriceListsRequest']>(request);
        if (!body?.items) {
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        }
        const errors: FieldError[] = [];
        const seen = new Set<string>();
        body.items.forEach((l, i) => {
          if (!l.code || !LIST_CODE.test(l.code)) {
            errors.push({ field: `items[${i}].code`, code: 'FORMAT' });
          }
          if (seen.has(l.code)) errors.push({ field: `items[${i}].code`, code: 'DUPLICATE' });
          seen.add(l.code);
          if (!l.name || [...l.name].length < 2) {
            errors.push({ field: `items[${i}].name`, code: 'LENGTH' });
          }
        });
        if (errors.length > 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
        }
        const existing = world().priceLists.filter((l) => l.contractVersionId === loaded.id);
        // A list absent from the body goes, and its items go with it.
        const removed = existing.filter((l) => !seen.has(l.code));
        const removedIds = new Set(removed.map((l) => l.id));
        world().priceItems = world().priceItems.filter((i) => !removedIds.has(i.priceListId));
        world().priceLists = world().priceLists.filter((l) => !removedIds.has(l.id));
        const items: StoredPriceList[] = body.items.map((l) => {
          const kept = existing.find((x) => x.code === l.code);
          if (kept) {
            kept.name = l.name;
            kept.priority = l.priority ?? 100;
            kept.seasonFrom = l.seasonFrom ?? null;
            kept.seasonTo = l.seasonTo ?? null;
            kept.weekdayMask = l.weekdayMask ?? null;
            kept.rowVersion += 1;
            return kept;
          }
          const created: StoredPriceList = {
            id: world().nextId(),
            tenantId: g.tenantId,
            contractVersionId: loaded.id,
            code: l.code,
            name: l.name,
            priority: l.priority ?? 100,
            seasonFrom: l.seasonFrom ?? null,
            seasonTo: l.seasonTo ?? null,
            weekdayMask: l.weekdayMask ?? null,
            rowVersion: 1,
          };
          world().priceLists.push(created);
          return created;
        });
        loaded.rowVersion += 1;
        return HttpResponse.json(
          { items: items.map((l) => toPriceList(world(), l)) } satisfies Schemas['PriceListList'],
          { headers: { ETag: etagOf(loaded.rowVersion) } },
        );
      },
    ),

    http.get(`${ANY}/api/v1/price-lists/:priceListId/items`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'contract.read', false);
      if ('error' in g) return g.error;
      const list = findList(g.tenantId, pathParam(params, 'priceListId'));
      if (!list) return notFound(api);
      if (!visibleVersion(g.session, g.tenantId, list.contractVersionId)) return notFound(api);
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'limit', code: 'FORMAT' }],
        });
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const rows = world().priceItems.filter((i) => i.priceListId === list.id);
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json(
        {
          items: page.map((i) => toPriceItem(world(), i)),
          nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
        } satisfies Schemas['PriceItemPage'],
        { headers: { ETag: etagOf(list.rowVersion) } },
      );
    }),

    http.put(`${ANY}/api/v1/price-lists/:priceListId/items`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'contract.manage', true);
      if ('error' in g) return g.error;
      const list = findList(g.tenantId, pathParam(params, 'priceListId'));
      if (!list) return notFound(api);
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null) {
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      }
      const version = world().contractVersions.find((v) => v.id === list.contractVersionId);
      if (!version) return notFound(api);
      if (version.status !== 'DRAFT') {
        return problem(
          api,
          409,
          'CONTRACT_VERSION_IMMUTABLE',
          'Yalnız taslak sürümün fiyat satırları değiştirilebilir',
        );
      }
      if (list.rowVersion !== expected) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      }
      const body = await readJson<Schemas['ReplacePriceItemsRequest']>(request);
      if (!body?.items) {
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      }
      const errors = body.items.flatMap((item, i) =>
        validatePriceItem(g.tenantId, version.id, i, item),
      );
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      world().priceItems = world().priceItems.filter((i) => i.priceListId !== list.id);
      const stored: StoredPriceItem[] = body.items.map((item) => ({
        id: world().nextId(),
        tenantId: g.tenantId,
        priceListId: list.id,
        serviceDefinitionId: item.serviceDefinitionId ?? null,
        serviceCategoryId: item.serviceCategoryId ?? null,
        packageDefinitionId: item.packageDefinitionId ?? null,
        locationId: item.locationId ?? null,
        unitType: item.unitType,
        pricingMethod: item.pricingMethod,
        amount: decimalOrNull(item.amount),
        percent: decimalOrNull(item.percent),
        formulaKey: item.formulaKey ?? null,
        minAmount: decimalOrNull(item.minAmount),
        maxAmount: decimalOrNull(item.maxAmount),
        memberShareMethod: item.memberShareMethod ?? 'NONE',
        memberShareAmount: decimalOrNull(item.memberShareAmount),
        memberSharePercent: decimalOrNull(item.memberSharePercent),
        validFrom: item.validFrom,
        validTo: item.validTo ?? null,
        priority: item.priority ?? 100,
      }));
      world().priceItems.push(...stored);
      list.rowVersion += 1;
      return HttpResponse.json(
        {
          items: stored.map((i) => toPriceItem(world(), i)),
          nextCursor: null,
        } satisfies Schemas['PriceItemPage'],
        { headers: { ETag: etagOf(list.rowVersion) } },
      );
    }),

    // --- package definitions ----------------------------------------------------
    http.get(
      `${ANY}/api/v1/contract-versions/:contractVersionId/package-definitions`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.read', false);
        if ('error' in g) return g.error;
        const version = visibleVersion(
          g.session,
          g.tenantId,
          pathParam(params, 'contractVersionId'),
        );
        if (!version) return notFound(api);
        const items = world()
          .packageDefinitions.filter((p) => p.contractVersionId === version.id)
          .map((p) => toPackageDefinition(world(), p));
        return HttpResponse.json({ items } satisfies Schemas['PackageDefinitionList'], {
          headers: { ETag: etagOf(version.rowVersion) },
        });
      },
    ),

    http.put(
      `${ANY}/api/v1/contract-versions/:contractVersionId/package-definitions`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.manage', true);
        if ('error' in g) return g.error;
        const loaded = draftForWrite(request, g.tenantId, pathParam(params, 'contractVersionId'));
        if (loaded instanceof Response) return loaded;
        const body = await readJson<Schemas['ReplacePackageDefinitionsRequest']>(request);
        if (!body?.items) {
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        }
        const errors: FieldError[] = [];
        const seen = new Set<string>();
        body.items.forEach((p, i) => {
          if (!p.code || !LIST_CODE.test(p.code)) {
            errors.push({ field: `items[${i}].code`, code: 'FORMAT' });
          }
          if (seen.has(p.code)) errors.push({ field: `items[${i}].code`, code: 'DUPLICATE' });
          seen.add(p.code);
          if (!p.lines || p.lines.length === 0) {
            errors.push({ field: `items[${i}].lines`, code: 'REQUIRED' });
          }
          const rule = p.inclusionRule ?? 'ALL';
          if (rule === 'ANY_OF_N' && !p.minLines) {
            errors.push({ field: `items[${i}].minLines`, code: 'MIN_LINES_REQUIRED' });
          }
          if (rule === 'ALL' && p.minLines) {
            errors.push({ field: `items[${i}].minLines`, code: 'MIN_LINES_FORBIDDEN' });
          }
          (p.lines ?? []).forEach((l, j) => {
            if (
              !world().serviceDefinitions.some(
                (d) => d.id === l.serviceDefinitionId && d.tenantId === g.tenantId,
              )
            ) {
              errors.push({
                field: `items[${i}].lines[${j}].serviceDefinitionId`,
                code: 'NOT_FOUND',
              });
            }
          });
        });
        if (errors.length > 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
        }
        const existing = world().packageDefinitions.filter(
          (p) => p.contractVersionId === loaded.id,
        );
        const removed = existing.filter((p) => !seen.has(p.code));
        const removedIds = new Set(removed.map((p) => p.id));
        // A package that goes takes the price items that priced it with it.
        world().priceItems = world().priceItems.filter(
          (i) => i.packageDefinitionId === null || !removedIds.has(i.packageDefinitionId),
        );
        world().packageDefinitions = world().packageDefinitions.filter(
          (p) => !removedIds.has(p.id),
        );
        const items: StoredPackageDefinition[] = body.items.map((p) => {
          const lines = p.lines.map((l) => ({
            serviceDefinitionId: l.serviceDefinitionId,
            includedQuantity: l.includedQuantity,
          }));
          const kept = existing.find((x) => x.code === p.code);
          if (kept) {
            kept.name = p.name;
            kept.inclusionRule = p.inclusionRule ?? 'ALL';
            kept.minLines = p.minLines ?? null;
            kept.lines = lines;
            return kept;
          }
          const created: StoredPackageDefinition = {
            id: world().nextId(),
            tenantId: g.tenantId,
            contractVersionId: loaded.id,
            code: p.code,
            name: p.name,
            inclusionRule: p.inclusionRule ?? 'ALL',
            minLines: p.minLines ?? null,
            lines,
          };
          world().packageDefinitions.push(created);
          return created;
        });
        loaded.rowVersion += 1;
        return HttpResponse.json(
          {
            items: items.map((p) => toPackageDefinition(world(), p)),
          } satisfies Schemas['PackageDefinitionList'],
          { headers: { ETag: etagOf(loaded.rowVersion) } },
        );
      },
    ),

    // --- provider quotas --------------------------------------------------------
    http.get(
      `${ANY}/api/v1/contract-versions/:contractVersionId/provider-quotas`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.read', false);
        if ('error' in g) return g.error;
        const version = visibleVersion(
          g.session,
          g.tenantId,
          pathParam(params, 'contractVersionId'),
        );
        if (!version) return notFound(api);
        const items = world()
          .providerQuotas.filter((q) => q.contractVersionId === version.id)
          .map(toProviderQuota);
        return HttpResponse.json({ items } satisfies Schemas['ProviderQuotaList'], {
          headers: { ETag: etagOf(version.rowVersion) },
        });
      },
    ),

    http.put(
      `${ANY}/api/v1/contract-versions/:contractVersionId/provider-quotas`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.manage', true);
        if ('error' in g) return g.error;
        const loaded = draftForWrite(request, g.tenantId, pathParam(params, 'contractVersionId'));
        if (loaded instanceof Response) return loaded;
        const body = await readJson<Schemas['ReplaceProviderQuotasRequest']>(request);
        if (!body?.items) {
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        }
        const errors: FieldError[] = [];
        const scopes = new Set<string>();
        const scopeOf = (q: Schemas['ProviderQuotaInput']): string =>
          `${q.locationId ?? '*'}|${q.serviceDefinitionId ?? '*'}|${q.periodFrom}|${q.periodTo}`;
        body.items.forEach((q, i) => {
          if (!q.capacity) errors.push({ field: `items[${i}].capacity`, code: 'REQUIRED' });
          if (!q.periodFrom || !q.periodTo) {
            errors.push({ field: `items[${i}].periodFrom`, code: 'REQUIRED' });
          }
          const scope = scopeOf(q);
          if (scopes.has(scope)) {
            errors.push({ field: `items[${i}].locationId`, code: 'QUOTA_SCOPE_DUPLICATE' });
          }
          scopes.add(scope);
        });
        if (errors.length > 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
        }
        const existing = world().providerQuotas.filter((q) => q.contractVersionId === loaded.id);
        world().providerQuotas = world().providerQuotas.filter(
          (q) => q.contractVersionId !== loaded.id,
        );
        // A quota already present under the same scope and period keeps its consumed
        // counter: capacity is renegotiated, what was already used is not.
        const items: StoredProviderQuota[] = body.items.map((q) => {
          const kept = existing.find(
            (x) =>
              (x.locationId ?? '*') === (q.locationId ?? '*') &&
              (x.serviceDefinitionId ?? '*') === (q.serviceDefinitionId ?? '*') &&
              x.periodFrom === q.periodFrom &&
              x.periodTo === q.periodTo,
          );
          return {
            id: kept?.id ?? world().nextId(),
            tenantId: g.tenantId,
            contractVersionId: loaded.id,
            locationId: q.locationId ?? null,
            serviceDefinitionId: q.serviceDefinitionId ?? null,
            periodType: q.periodType,
            periodFrom: q.periodFrom,
            periodTo: q.periodTo,
            capacity: q.capacity,
            consumed: kept?.consumed ?? fromMicros(0n),
            allowOverdraft: q.allowOverdraft ?? false,
          };
        });
        world().providerQuotas.push(...items);
        loaded.rowVersion += 1;
        return HttpResponse.json(
          { items: items.map(toProviderQuota) } satisfies Schemas['ProviderQuotaList'],
          { headers: { ETag: etagOf(loaded.rowVersion) } },
        );
      },
    ),

    // --- payment term -----------------------------------------------------------
    http.get(
      `${ANY}/api/v1/contract-versions/:contractVersionId/payment-term`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.read', false);
        if ('error' in g) return g.error;
        const version = visibleVersion(
          g.session,
          g.tenantId,
          pathParam(params, 'contractVersionId'),
        );
        if (!version) return notFound(api);
        const term = world().paymentTerms.find((t) => t.contractVersionId === version.id);
        // 404 tells "not agreed yet" apart from "agreed as zero".
        if (!term) return notFound(api);
        return HttpResponse.json(toPaymentTerm(term), {
          headers: { ETag: etagOf(term.rowVersion) },
        });
      },
    ),

    http.put(
      `${ANY}/api/v1/contract-versions/:contractVersionId/payment-term`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'contract.manage', true);
        if ('error' in g) return g.error;
        const loaded = draftForWrite(request, g.tenantId, pathParam(params, 'contractVersionId'));
        if (loaded instanceof Response) return loaded;
        const body = await readJson<Schemas['PutPaymentTermRequest']>(request);
        if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        const errors: FieldError[] = [];
        if (typeof body.dueDays !== 'number' || body.dueDays < 0 || body.dueDays > 365) {
          errors.push({ field: 'dueDays', code: 'RANGE' });
        }
        if (!body.settlementMethod) errors.push({ field: 'settlementMethod', code: 'REQUIRED' });
        if (!body.taxBehaviour) errors.push({ field: 'taxBehaviour', code: 'REQUIRED' });
        if (errors.length > 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
        }
        const existing = world().paymentTerms.find((t) => t.contractVersionId === loaded.id);
        const term = existing ?? {
          id: world().nextId(),
          tenantId: g.tenantId,
          contractVersionId: loaded.id,
          dueDays: body.dueDays,
          settlementMethod: body.settlementMethod,
          taxBehaviour: body.taxBehaviour,
          vatRate: body.vatRate ?? null,
          lateFeePercent: body.lateFeePercent ?? null,
          rowVersion: 1,
        };
        if (existing) {
          existing.dueDays = body.dueDays;
          existing.settlementMethod = body.settlementMethod;
          existing.taxBehaviour = body.taxBehaviour;
          existing.vatRate = body.vatRate ?? null;
          existing.lateFeePercent = body.lateFeePercent ?? null;
          existing.rowVersion += 1;
        } else {
          world().paymentTerms.push(term);
        }
        loaded.rowVersion += 1;
        return HttpResponse.json(toPaymentTerm(term), {
          headers: { ETag: etagOf(term.rowVersion) },
        });
      },
    ),

    // --- price selection --------------------------------------------------------
    // The colon is escaped so path-to-regexp reads ":resolve" as literal text.
    http.post(`${ANY}/api/v1/prices\\:resolve`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'contract.read', true);
      if ('error' in g) return g.error;
      const body = await readJson<Schemas['ResolvePriceRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors: FieldError[] = [];
      if (!body.serviceDate) errors.push({ field: 'serviceDate', code: 'REQUIRED' });
      if (!body.providerProfileId) errors.push({ field: 'providerProfileId', code: 'REQUIRED' });
      if (!body.serviceDefinitionId) {
        errors.push({ field: 'serviceDefinitionId', code: 'REQUIRED' });
      }
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (
        !world().providers.some((p) => p.id === body.providerProfileId && p.tenantId === g.tenantId)
      ) {
        return notFound(api);
      }
      const resolution = resolveContractPrice(world(), g.tenantId, {
        serviceDate: body.serviceDate,
        providerProfileId: body.providerProfileId,
        serviceDefinitionId: body.serviceDefinitionId,
        locationId: body.locationId ?? null,
      });
      // Changes no state, by construction: nothing above writes to the world.
      return HttpResponse.json(resolution.result);
    }),
  ];
}
