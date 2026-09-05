/**
 * MSW handlers for the eligibility check and its stored evaluation. The resolution
 * mirrors the Go resolver: person, membership, enrollment, published plan version, then
 * balances per requested line, with an explanation code for anything that is not simply
 * eligible. The stored snapshot carries ids, dates and quantities only.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import { reachableEntitlementAccounts, type MockWorld, type StoredEvaluation } from './data';
import type {
  EligibilityBalance,
  EligibilityCheckRequest as DecimalRequest,
  EligibilityCheckResult as DecimalResult,
  EligibilityEvaluation as DecimalEvaluation,
  EligibilityItemResult,
} from '../decimals';
import type { MockApi } from './handlers';
import { ANY, guardTenant, pathParam, problem, readJson, wait } from './handlers';

// The mock speaks the wire format: quantities are decimal strings (see ../decimals.ts).
type Result = DecimalResult;
type Explanation = Result['explanations'][number];
type ItemResult = EligibilityItemResult;

const MESSAGES: Record<string, string> = {
  PERSON_NOT_FOUND: 'Hak sahibi bulunamadı',
  PERSON_INACTIVE: 'Hak sahibi kaydı aktif değil',
  MEMBERSHIP_NONE: 'Bu tarihte aktif sponsor üyeliği yok',
  MEMBERSHIP_SUSPENDED: 'Sponsor üyeliği askıda',
  ENROLLMENT_NONE: 'Bu tarihte aktif plan kaydı yok',
  ENROLLMENT_SUSPENDED: 'Plan kaydı askıda',
  ENROLLMENT_MULTIPLE: 'Birden fazla plan kaydı var',
  PLAN_VERSION_NONE: 'Bu tarihte yayınlanmış plan sürümü yok',
  BALANCE_INSUFFICIENT: 'Bakiye istenen miktar için yeterli değil',
  BALANCE_OVERDRAFT_ALLOWED: 'Bakiye yetersiz ama bu hak eksiye izin veriyor',
  SERVICE_MAPPING_PENDING: 'Hizmet tanımı hak koduna bağlanmadı',
};

function explain(code: string, severity: Explanation['severity']): Explanation {
  return { code, message: MESSAGES[code] ?? code, severity };
}

/** True while a half-open period covers the date. */
function active(from: string, to: string | null, date: string): boolean {
  return from <= date && (to === null || date < to);
}

/** Multiplies two decimal strings exactly, at the six decimals every quantity carries. */
function multiplyDecimal(a: string, b: string): string {
  const micros = (v: string): bigint => {
    const [i, f = ''] = v.split('.');
    return BigInt(i ?? '0') * 1_000_000n + BigInt((f + '000000').slice(0, 6));
  };
  const product = (micros(a) * micros(b)) / 1_000_000n;
  return `${product / 1_000_000n}.${(product % 1_000_000n).toString().padStart(6, '0')}`;
}

/** Compares two decimal strings without floating point. */
function lessThan(a: string, b: string): boolean {
  const [ai, af = ''] = a.split('.');
  const [bi, bf = ''] = b.split('.');
  const an = BigInt(ai ?? '0') * 1_000_000n + BigInt((af + '000000').slice(0, 6));
  const bn = BigInt(bi ?? '0') * 1_000_000n + BigInt((bf + '000000').slice(0, 6));
  return an < bn;
}

/**
 * The eligibility resolution, as one function so the check endpoint and the service
 * request submit gate can never disagree about it. It mirrors the Go resolver: person,
 * membership, enrollment, published plan version, then a balance per requested line, with
 * an explanation code for anything that is not simply eligible. It writes nothing.
 */
export function resolveEligibility(
  world: MockWorld,
  tenantId: string,
  input: EligibilityInput,
  evaluationId: string,
): Result {
  const serviceDate = input.serviceDate;
  const explanations: Explanation[] = [];
  const person = world.people.find((p) => p.id === input.personId && p.tenantId === tenantId);
  let outcome: Result['outcome'] = 'ELIGIBLE';
  let planVersionId: string | null = null;
  let enrollmentId: string | null = null;
  let candidates: NonNullable<Result['enrollmentCandidates']> = [];

  if (!person) {
    explanations.push(explain('PERSON_NOT_FOUND', 'ERROR'));
    outcome = 'MISSING_DATA';
  } else if (person.status !== 'ACTIVE') {
    explanations.push(explain('PERSON_INACTIVE', 'ERROR'));
    outcome = 'INELIGIBLE';
  }

  const memberships = person
    ? world.memberships.filter(
        (m) =>
          m.tenantId === tenantId &&
          m.personId === person.id &&
          active(m.validFrom, m.validTo, serviceDate),
      )
    : [];
  if (person && memberships.length === 0) {
    explanations.push(explain('MEMBERSHIP_NONE', 'ERROR'));
    outcome = 'MISSING_DATA';
  } else if (memberships.every((m) => m.status === 'SUSPENDED') && memberships.length > 0) {
    explanations.push(explain('MEMBERSHIP_SUSPENDED', 'ERROR'));
    outcome = 'INELIGIBLE';
  }

  const enrollments = person
    ? world.enrollments.filter(
        (e) =>
          e.tenantId === tenantId &&
          e.personId === person.id &&
          active(e.validFrom, e.validTo, serviceDate) &&
          (!input.programId || e.programId === input.programId) &&
          // An id that is not one of this person's own enrollments selects nothing, which
          // lands on ENROLLMENT_NONE rather than on somebody else's plan.
          (!input.enrollmentId || e.id === input.enrollmentId),
      )
    : [];
  const chosen = enrollments.find((e) => e.status === 'ACTIVE') ?? enrollments[0];
  if (person && memberships.length > 0) {
    if (!chosen) {
      explanations.push(explain('ENROLLMENT_NONE', 'ERROR'));
      outcome = 'INELIGIBLE';
    } else {
      enrollmentId = chosen.id;
      if (chosen.status === 'SUSPENDED') {
        explanations.push(explain('ENROLLMENT_SUSPENDED', 'ERROR'));
        outcome = 'INELIGIBLE';
      }
      const activeEnrollments = enrollments.filter((e) => e.status === 'ACTIVE');
      if (activeEnrollments.length > 1) {
        explanations.push(explain('ENROLLMENT_MULTIPLE', 'WARNING'));
        outcome = 'REVIEW_REQUIRED';
        // Naming the choice is the point: a desk told "there is more than one plan" and
        // nothing else can only guess, and a provider may not list a person's enrollments
        // to find out.
        candidates = activeEnrollments.map((e) => ({
          enrollmentId: e.id,
          planCode: e.planCode,
          planName: world.plans.find((p) => p.id === e.planId)?.name ?? e.planCode,
          validFrom: e.validFrom,
          validTo: e.validTo,
        }));
      }
      const version = world.planVersions.find(
        (v) =>
          v.planId === chosen.planId &&
          v.status === 'PUBLISHED' &&
          v.validFrom !== null &&
          active(v.validFrom, v.validTo, serviceDate),
      );
      if (!version) {
        explanations.push(explain('PLAN_VERSION_NONE', 'ERROR'));
        outcome = 'INELIGIBLE';
      } else {
        planVersionId = version.id;
      }
    }
  }

  // Balances of every account the person can reach on the date.
  const reachable = person
    ? reachableEntitlementAccounts(world, tenantId, person.id, serviceDate)
    : [];
  const balances: EligibilityBalance[] = reachable.map((r) => ({
    entitlementCode: r.account.definition.code,
    available: r.account.available,
    unit: r.account.definition.unitType,
  }));

  // Items are matched by the entitlement code hints in the request context.
  const context = (input.context ?? {}) as {
    entitlementCode?: unknown;
    entitlementCodes?: unknown;
  };
  const codes = Array.isArray(context.entitlementCodes)
    ? (context.entitlementCodes as unknown[]).map((c) => (typeof c === 'string' ? c : ''))
    : [];
  const singleCode = typeof context.entitlementCode === 'string' ? context.entitlementCode : '';
  // The mapping of the resolved plan version, keyed by service definition. It decides
  // before the context hint does: a service that is mapped is judged against its
  // entitlement's balance, and only an unmapped one falls back to the hint.
  const mappings = new Map(
    world.entitlementMappings
      .filter((m) => m.tenantId === tenantId && m.planVersionId === planVersionId)
      .map((m) => {
        const version = world.planVersions.find((v) => v.id === m.planVersionId);
        const definition = version?.definitions.find((d) => d.id === m.entitlementDefinitionId);
        return [
          m.serviceDefinitionId,
          { code: definition?.code ?? '', unitFactor: m.unitFactor },
        ] as const;
      }),
  );

  const items: ItemResult[] = input.serviceItems.map((item, index) => {
    const mapping = mappings.get(item.serviceDefinitionId);
    const code = mapping?.code || (codes[index] ?? singleCode);
    const quantity = String(item.quantity);
    // One session of a service may draw two units of the entitlement, so what is compared
    // with the balance is the requested quantity times the mapping's factor.
    const drawn = mapping ? multiplyDecimal(quantity, mapping.unitFactor) : quantity;
    if (!code) {
      return {
        index,
        outcome: 'REVIEW_REQUIRED',
        requestedQuantity: quantity,
        explanations: [explain('SERVICE_MAPPING_PENDING', 'INFO')],
      };
    }
    const match = reachable.find((r) => r.account.definition.code === code);
    if (!match) {
      return {
        index,
        entitlementCode: code,
        outcome: 'REVIEW_REQUIRED',
        requestedQuantity: quantity,
        explanations: [explain('SERVICE_MAPPING_PENDING', 'INFO')],
      };
    }
    const enough = !lessThan(match.account.available, drawn);
    const itemExplanations: Explanation[] = [];
    let itemOutcome: ItemResult['outcome'] = 'ELIGIBLE';
    if (!enough) {
      if (match.account.definition.allowOverdraft) {
        itemExplanations.push(explain('BALANCE_OVERDRAFT_ALLOWED', 'WARNING'));
      } else {
        itemExplanations.push(explain('BALANCE_INSUFFICIENT', 'ERROR'));
        itemOutcome = 'INELIGIBLE';
      }
    }
    return {
      index,
      entitlementCode: code,
      outcome: itemOutcome,
      requestedQuantity: quantity,
      availableQuantity: match.account.available,
      explanations: itemExplanations,
    };
  });

  if (outcome === 'ELIGIBLE' && items.length > 0) {
    const eligible = items.filter((i) => i.outcome === 'ELIGIBLE').length;
    const review = items.some((i) => i.outcome === 'REVIEW_REQUIRED');
    if (review) outcome = 'REVIEW_REQUIRED';
    else if (eligible === 0) outcome = 'INELIGIBLE';
    else if (eligible < items.length) outcome = 'PARTIALLY_ELIGIBLE';
  }
  for (const item of items) {
    for (const e of item.explanations) {
      if (!explanations.some((x) => x.code === e.code)) explanations.push(e);
    }
  }

  return {
    evaluationId,
    eligible: outcome === 'ELIGIBLE',
    outcome,
    evaluatedAt: new Date().toISOString(),
    explanations,
    items,
    balances,
    planVersionId,
    enrollmentId,
    // Present only alongside an ENROLLMENT_MULTIPLE explanation, so its presence is itself
    // the answer to "was there a choice to make".
    ...(candidates.length > 0 ? { enrollmentCandidates: candidates } : {}),
    ruleSetVersionIds: [],
  };
}

/** What the resolver needs; the check request is one shape of it. */
export interface EligibilityInput {
  personId: string;
  programId?: string | null;
  /**
   * Which of the person's enrollments to answer for (WP-I5-05 section 2.2). It exists for
   * the second ask after an ENROLLMENT_MULTIPLE answer: the caller picks one of the
   * candidates the first answer named.
   */
  enrollmentId?: string | null;
  serviceDate: string;
  serviceItems: DecimalRequest['serviceItems'];
  context?: DecimalRequest['context'];
}

export function eligibilityHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  return [
    http.post(`${ANY}/api/v1/eligibility/checks`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'eligibility.check', true);
      if ('error' in g) return g.error;
      const body = await readJson<DecimalRequest>(request);
      if (!body?.personId || !body.serviceDate || !Array.isArray(body.serviceItems)) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'personId', code: 'REQUIRED' }],
        });
      }

      // A repeated key replays the stored evaluation instead of writing a second one.
      const key = request.headers.get('Idempotency-Key');
      if (key) {
        const replay = [...world().evaluations.values()].find(
          (e) => e.tenantId === g.tenantId && e.idempotencyKey === key,
        );
        if (replay) return HttpResponse.json(replay.result);
      }

      const evaluationId = world().nextId();
      const result = resolveEligibility(world(), g.tenantId, body, evaluationId);
      const stored: StoredEvaluation = {
        id: evaluationId,
        tenantId: g.tenantId,
        personId: body.personId,
        programId: body.programId ?? null,
        planVersionId: result.planVersionId ?? null,
        enrollmentId: result.enrollmentId ?? null,
        serviceDate: body.serviceDate,
        evaluatedAt: result.evaluatedAt,
        evaluatedBy: g.session.account.actorId,
        outcome: result.outcome,
        request: body,
        result,
        ...(key ? { idempotencyKey: key } : {}),
      };
      world().evaluations.set(evaluationId, stored);
      return HttpResponse.json(result);
    }),

    http.get(`${ANY}/api/v1/eligibility/evaluations/:evaluationId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'eligibility.check', false);
      if ('error' in g) return g.error;
      const stored = world().evaluations.get(pathParam(params, 'evaluationId'));
      if (!stored || stored.tenantId !== g.tenantId) {
        return problem(api, 404, 'RESOURCE_NOT_FOUND', 'Kaynak bulunamadı');
      }
      const out: DecimalEvaluation = {
        id: stored.id,
        personId: stored.personId,
        programId: stored.programId,
        enrollmentId: stored.enrollmentId,
        planVersionId: stored.planVersionId,
        serviceDate: stored.serviceDate,
        outcome: stored.outcome,
        evaluatedAt: stored.evaluatedAt,
        evaluatedBy: stored.evaluatedBy,
        request: stored.request,
        result: stored.result,
      };
      return HttpResponse.json(out);
    }),
  ];
}
