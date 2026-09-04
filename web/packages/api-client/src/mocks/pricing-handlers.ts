/**
 * MSW handlers for the M3 pricing surface: the price quote.
 *
 * A quote answers "what does this cost, what does the plan carry, what does the member
 * pay" and it reserves nothing. Nothing below writes to an entitlement account or to the
 * ledger — that is the single most important property of the endpoint, because a counter
 * clerk who mistakes a quote for an approval has given away money nobody authorised. Every
 * figure is an exact decimal string computed in integer micro-units; no amount here ever
 * passes through a binary float.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  fromMicros,
  multiplyMicros,
  percentOfMicros,
  reachableEntitlementAccounts,
  toMicros,
  type MockWorld,
  type StoredPriceItem,
  type StoredPriceQuote,
} from './data';
import { resolveContractPrice } from './contract-handlers';
import { runRules } from './rules-handlers';
import type { MockApi } from './handlers';
import {
  ANY,
  etagOf,
  guardTenant,
  notFound,
  pathParam,
  problem,
  readJson,
  wait,
  withinPeriod,
  type FieldError,
  type Schemas,
} from './handlers';

/** Tenant setting pricing.quote_ttl_hours; 72 hours is the contract's default. */
const QUOTE_TTL_HOURS = 72;

const DISCLAIMER =
  'Bu fiyat teklifi bir onay ya da provizyon değildir: hiçbir hak rezerve edilmez, ' +
  'bakiye hareket etmez ve hizmet garanti edilmez. Fiyatlar geçerlilik süresi içinde ' +
  'değişebilir.';

const ZERO = 0n;

/** One priced line, before it is projected onto the wire. */
interface PricedLine {
  item: Schemas['PriceQuoteItem'];
  contractVersionId: string | null;
  currencyCode: string | null;
  priceItemMicros: { contract: bigint; covered: bigint; payer: bigint; member: bigint };
}

function explanation(
  code: string,
  severity: 'INFO' | 'WARNING' | 'ERROR',
  source?: string,
): Schemas['PriceQuoteExplanation'] {
  return { code, severity, source: source ?? null };
}

/** The contract amount of a line, in micro-units, or null when the method needs a human. */
function contractAmountMicros(
  price: StoredPriceItem,
  quantity: bigint,
  requestedAmount: bigint,
): bigint | null {
  let amount: bigint;
  switch (price.pricingMethod) {
    case 'FIXED':
      amount = toMicros(price.amount);
      break;
    case 'UNIT':
      amount = multiplyMicros(toMicros(price.amount), quantity);
      break;
    case 'PERCENT_OF_LIST':
      amount = percentOfMicros(requestedAmount, toMicros(price.percent));
      break;
    default:
      // A FORMULA price needs the formula engine the mock does not carry.
      return null;
  }
  const min = price.minAmount === null ? null : toMicros(price.minAmount);
  const max = price.maxAmount === null ? null : toMicros(price.maxAmount);
  if (min !== null && amount < min) amount = min;
  if (max !== null && amount > max) amount = max;
  return amount;
}

/** The member's own share of a contract amount, in micro-units. */
function memberShareMicros(price: StoredPriceItem, contract: bigint): bigint {
  switch (price.memberShareMethod) {
    case 'FIXED': {
      const fixed = toMicros(price.memberShareAmount);
      return fixed > contract ? contract : fixed;
    }
    case 'PERCENT':
      return percentOfMicros(contract, toMicros(price.memberSharePercent));
    default:
      return ZERO;
  }
}

export function pricingHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  /** Recomputes `expired` on read: a quote that has lapsed says so rather than hiding. */
  const project = (quote: StoredPriceQuote): Schemas['PriceQuote'] => {
    const { tenantId: _tenantId, ...out } = quote;
    return { ...out, expired: Date.parse(out.expiresAt) <= Date.now() };
  };

  /** Prices one requested line. Reads the world; writes nothing. */
  const priceLine = (
    tenantId: string,
    body: Schemas['CreatePriceQuoteRequest'],
    line: Schemas['PriceQuoteRequestItem'],
    lineNo: number,
    eligible: boolean,
    entitlementCode: string | null,
    ruleSetVersionIds: Set<string>,
  ): PricedLine => {
    const quantity = toMicros(line.quantity);
    const requested = toMicros(line.requestedAmount ?? '0.000000');
    const explanations: Schemas['PriceQuoteExplanation'][] = [];
    const base = {
      lineNo,
      serviceDefinitionId: line.serviceDefinitionId ?? null,
      packageDefinitionId: line.packageDefinitionId ?? null,
      quantity: line.quantity,
      requestedAmount: fromMicros(requested),
    };
    const review = (code: string): PricedLine => ({
      item: {
        ...base,
        priceItemId: null,
        contractAmount: fromMicros(ZERO),
        coveredAmount: fromMicros(ZERO),
        payerAmount: fromMicros(ZERO),
        memberAmount: fromMicros(ZERO),
        outcome: 'REVIEW_REQUIRED',
        explanations: [...explanations, explanation(code, 'ERROR', 'PRICING')],
      },
      contractVersionId: null,
      currencyCode: null,
      priceItemMicros: { contract: ZERO, covered: ZERO, payer: ZERO, member: ZERO },
    });

    const resolution = resolveContractPrice(world(), tenantId, {
      serviceDate: body.serviceDate,
      providerProfileId: body.providerProfileId,
      serviceDefinitionId: line.serviceDefinitionId ?? null,
      packageDefinitionId: line.packageDefinitionId ?? null,
      locationId: body.locationId ?? null,
    });
    if (resolution.result.outcome === 'NOT_FOUND') return review('PRICE_NOT_FOUND');
    if (resolution.result.outcome === 'REVIEW_REQUIRED' || !resolution.winner) {
      // Two equally specific prices: the answer is never a guess.
      return review('PRICE_AMBIGUOUS');
    }
    const price = resolution.winner;
    const contract = contractAmountMicros(price, quantity, requested);
    if (contract === null) return review('PRICE_FORMULA_UNRESOLVED');

    // The tenant's published PRICE rules, valid on the service date, get a say next.
    for (const version of world().ruleSetVersions) {
      if (version.tenantId !== tenantId || version.status !== 'PUBLISHED') continue;
      if (version.validFrom === null) continue;
      if (!withinPeriod(body.serviceDate, version.validFrom, version.validTo)) continue;
      const set = world().ruleSets.find((s) => s.id === version.ruleSetId);
      if (set?.purpose !== 'PRICE' || set.status !== 'ACTIVE') continue;
      ruleSetVersionIds.add(version.id);
      const run = runRules(version, {
        serviceCode:
          world().serviceDefinitions.find((d) => d.id === line.serviceDefinitionId)?.code ?? '',
        quantity: line.quantity,
        requestedAmount: fromMicros(requested),
        contractAmount: fromMicros(contract),
        ...(body.context ?? {}),
      });
      if (run.outcome === 'REJECTED' || run.outcome === 'REVIEW_REQUIRED') {
        for (const code of run.explanations) {
          explanations.push(explanation(code, 'WARNING', 'RULES'));
        }
        return review('RULE_REVIEW_REQUIRED');
      }
    }

    let member = memberShareMicros(price, contract);
    if (member > ZERO) explanations.push(explanation('MEMBER_SHARE_APPLIED', 'INFO', 'CONTRACT'));
    let covered = contract - member;
    let outcome: Schemas['PriceQuoteOutcome'] = 'QUOTED';

    if (!eligible) {
      // A member the plan does not cover may still choose to pay privately, so the
      // service is still priced; the payer simply carries none of it.
      explanations.push(explanation('NOT_ELIGIBLE', 'WARNING', 'ELIGIBILITY'));
      covered = ZERO;
      member = contract;
      outcome = 'NOT_ELIGIBLE';
    } else if (entitlementCode) {
      const account = reachableEntitlementAccounts(
        world(),
        tenantId,
        body.personId,
        body.serviceDate,
      ).find((a) => a.account.definition.code === entitlementCode && a.account.status === 'OPEN');
      const available = account ? toMicros(account.account.available) : ZERO;
      if (covered > available) {
        // The balance caps what the plan carries; the member takes the difference. No
        // account and no ledger row is touched: a quote holds nothing back.
        explanations.push(explanation('ENTITLEMENT_LIMIT_APPLIED', 'WARNING', 'ENTITLEMENT'));
        covered = available;
        member = contract - covered;
        outcome = covered > ZERO ? 'PARTIAL' : 'NOT_ELIGIBLE';
      }
    }

    return {
      item: {
        ...base,
        priceItemId: price.id,
        contractAmount: fromMicros(contract),
        coveredAmount: fromMicros(covered),
        payerAmount: fromMicros(covered),
        memberAmount: fromMicros(member),
        outcome,
        explanations,
      },
      contractVersionId: resolution.contractVersionId,
      currencyCode: resolution.currencyCode,
      priceItemMicros: { contract, covered, payer: covered, member },
    };
  };

  return [
    http.post(`${ANY}/api/v1/pricing/quotes`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'pricing.quote', true);
      if ('error' in g) return g.error;
      const body = await readJson<Schemas['CreatePriceQuoteRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors: FieldError[] = [];
      if (!body.personId) errors.push({ field: 'personId', code: 'REQUIRED' });
      if (!body.providerProfileId) errors.push({ field: 'providerProfileId', code: 'REQUIRED' });
      if (!body.serviceDate) errors.push({ field: 'serviceDate', code: 'REQUIRED' });
      if (!body.items || body.items.length === 0) {
        errors.push({ field: 'items', code: 'REQUIRED' });
      }
      (body.items ?? []).forEach((item, i) => {
        const named = [item.serviceDefinitionId, item.packageDefinitionId].filter(Boolean).length;
        if (named !== 1) {
          errors.push({ field: `items[${i}].serviceDefinitionId`, code: 'EXACTLY_ONE_TARGET' });
        }
        if (!item.quantity || toMicros(item.quantity) <= ZERO) {
          errors.push({ field: `items[${i}].quantity`, code: 'POSITIVE_REQUIRED' });
        }
      });
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (!world().people.some((p) => p.id === body.personId && p.tenantId === g.tenantId)) {
        return notFound(api);
      }
      const provider = world().providers.find(
        (p) => p.id === body.providerProfileId && p.tenantId === g.tenantId,
      );
      if (!provider) return notFound(api);

      const enrollment = world().enrollments.find(
        (e) =>
          e.tenantId === g.tenantId &&
          e.personId === body.personId &&
          e.status === 'ACTIVE' &&
          withinPeriod(body.serviceDate, e.validFrom, e.validTo) &&
          (!body.programId || e.programId === body.programId),
      );
      const planVersion = enrollment
        ? world().planVersions.find(
            (v) =>
              v.planId === enrollment.planId &&
              v.status === 'PUBLISHED' &&
              v.validFrom !== null &&
              withinPeriod(body.serviceDate, v.validFrom, v.validTo),
          )
        : undefined;
      const eligible = Boolean(enrollment && planVersion);

      const context = (body.context ?? {}) as {
        entitlementCode?: unknown;
        entitlementCodes?: unknown;
      };
      const perLineCodes = Array.isArray(context.entitlementCodes)
        ? (context.entitlementCodes as unknown[])
        : [];
      const defaultCode =
        typeof context.entitlementCode === 'string' ? context.entitlementCode : null;

      const ruleSetVersionIds = new Set<string>();
      const lines = body.items.map((item, i) => {
        const positional = perLineCodes[i];
        const code = typeof positional === 'string' ? positional : defaultCode;
        return priceLine(g.tenantId, body, item, i + 1, eligible, code, ruleSetVersionIds);
      });

      const totals = lines.reduce(
        (acc, l) => ({
          requested: acc.requested + toMicros(l.item.requestedAmount),
          contract: acc.contract + l.priceItemMicros.contract,
          covered: acc.covered + l.priceItemMicros.covered,
          payer: acc.payer + l.priceItemMicros.payer,
          member: acc.member + l.priceItemMicros.member,
        }),
        { requested: ZERO, contract: ZERO, covered: ZERO, payer: ZERO, member: ZERO },
      );
      const outcomes = lines.map((l) => l.item.outcome);
      const review = outcomes.includes('REVIEW_REQUIRED');
      const outcome: Schemas['PriceQuoteOutcome'] = review
        ? 'REVIEW_REQUIRED'
        : outcomes.includes('PARTIAL')
          ? 'PARTIAL'
          : outcomes.every((o) => o === 'NOT_ELIGIBLE')
            ? 'NOT_ELIGIBLE'
            : 'QUOTED';

      const quotedAt = new Date();
      const quote: StoredPriceQuote = {
        tenantId: g.tenantId,
        id: world().nextId(),
        personId: body.personId,
        programId: body.programId ?? null,
        providerProfileId: body.providerProfileId,
        locationId: body.locationId ?? null,
        serviceDate: body.serviceDate,
        currencyCode: lines.find((l) => l.currencyCode)?.currencyCode ?? 'TRY',
        outcome,
        requestedAmount: fromMicros(totals.requested),
        contractAmount: fromMicros(totals.contract),
        // A quote under review carries no payer and no member figure at all.
        coveredAmount: fromMicros(review ? ZERO : totals.covered),
        payerAmount: fromMicros(review ? ZERO : totals.payer),
        memberAmount: fromMicros(review ? ZERO : totals.member),
        items: lines.map((l) => l.item),
        contractVersionId: lines.find((l) => l.contractVersionId)?.contractVersionId ?? null,
        planVersionId: planVersion?.id ?? null,
        ruleSetVersionIds: [...ruleSetVersionIds],
        eligibilityEvaluationId: body.eligibilityEvaluationId ?? null,
        expiresAt: new Date(quotedAt.getTime() + QUOTE_TTL_HOURS * 3_600_000).toISOString(),
        expired: false,
        quotedAt: quotedAt.toISOString(),
        quotedBy: g.session.account.actorId,
        disclaimer: DISCLAIMER,
      };
      world().priceQuotes.push(quote);
      return HttpResponse.json(project(quote));
    }),

    http.get(`${ANY}/api/v1/pricing/quotes/:priceQuoteId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'pricing.quote', false);
      if ('error' in g) return g.error;
      const quote = world().priceQuotes.find(
        (q) => q.id === pathParam(params, 'priceQuoteId') && q.tenantId === g.tenantId,
      );
      // The row is append-only: what somebody was quoted cannot be rewritten.
      if (!quote) return notFound(api);
      return HttpResponse.json(project(quote), { headers: { ETag: etagOf(1) } });
    }),
  ];
}
