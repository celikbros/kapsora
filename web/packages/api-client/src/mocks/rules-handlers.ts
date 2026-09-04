/**
 * MSW handlers for the M3 rules surface: rule sets, their versions with an input schema,
 * rules and test cases, the test run, the simulation and the maker-checker publish.
 *
 * Three behaviours carry the module. A version that is not DRAFT is frozen (409
 * RULE_VERSION_IMMUTABLE). A version cannot be submitted for review without at least one
 * test case and without every case passing (422 TESTS_REQUIRED / TESTS_FAILING). And the
 * test run and the simulation write nothing at all — no evaluation row, no side effect —
 * which is what lets an author run either as often as they like.
 *
 * The real engine compiles CEL (ADR-023). The mock compiles a deliberately small subset:
 * comparisons of a declared variable against a literal, joined by && and ||. It is enough
 * for a screen to see a condition accepted, rejected with the compiler's message, and
 * evaluated deterministically — and it fails closed, so anything it cannot parse is a
 * compile error rather than a silently true rule.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  compareDecimal,
  isDecimalText,
  pseudoHash,
  toRule,
  toRuleSet,
  toRuleSetVersion,
  toRuleSetVersionSummary,
  toRuleTestCase,
  type MockWorld,
  type StoredRule,
  type StoredRuleSet,
  type StoredRuleSetVersion,
  type StoredRuleTestCase,
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
  type FieldError,
  type Schemas,
} from './handlers';

const SET_CODE = /^[A-Z][A-Z0-9_]{1,39}$/;
const RULE_CODE = /^[A-Z][A-Z0-9_]{1,63}$/;

// --- the condition subset -------------------------------------------------------------

type Comparison = {
  variable: string;
  operator: '==' | '!=' | '>' | '>=' | '<' | '<=';
  literal: string;
  literalIsText: boolean;
};
type Conjunction = Comparison[];
/** Disjunction of conjunctions: `a && b || c` is `[[a, b], [c]]`. */
type Condition = Conjunction[];

const COMPARISON =
  /^\s*([A-Za-z_][A-Za-z0-9_]*)\s*(==|!=|>=|<=|>|<)\s*("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|-?\d+(?:\.\d+)?|true|false)\s*$/;

export interface CompiledCondition {
  condition?: Condition;
  error?: string;
  variables: string[];
}

/** Parses a condition and names the variables it reads. */
export function compileCondition(source: string): CompiledCondition {
  const variables: string[] = [];
  const disjuncts: Condition = [];
  for (const orPart of source.split('||')) {
    const conjunction: Conjunction = [];
    for (const andPart of orPart.split('&&')) {
      const m = COMPARISON.exec(andPart);
      if (!m) {
        return {
          error: `desteklenmeyen koşul parçası: ${andPart.trim()}`,
          variables,
        };
      }
      const raw = m[3]!;
      const quoted = raw.startsWith('"') || raw.startsWith("'");
      conjunction.push({
        variable: m[1]!,
        operator: m[2] as Comparison['operator'],
        literal: quoted ? raw.slice(1, -1) : raw,
        literalIsText: quoted,
      });
      variables.push(m[1]!);
    }
    disjuncts.push(conjunction);
  }
  return { condition: disjuncts, variables };
}

/** Reads one input value as text; decimal strings are preferred over JSON numbers. */
function valueText(value: unknown): string | null {
  if (typeof value === 'string') return value;
  if (typeof value === 'boolean') return value ? 'true' : 'false';
  if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  return null;
}

function compareValues(left: string, right: string): number {
  // Exact decimal comparison when both sides are numeric text; no float ever appears.
  if (isDecimalText(left) && isDecimalText(right)) return compareDecimal(left, right);
  return left < right ? -1 : left > right ? 1 : 0;
}

function evaluateComparison(c: Comparison, input: Record<string, unknown>): boolean {
  const text = valueText(input[c.variable]);
  if (text === null) return false;
  const order = compareValues(text, c.literal);
  switch (c.operator) {
    case '==':
      return order === 0;
    case '!=':
      return order !== 0;
    case '>':
      return order > 0;
    case '>=':
      return order >= 0;
    case '<':
      return order < 0;
    default:
      return order <= 0;
  }
}

function evaluateCondition(source: string, input: Record<string, unknown>): boolean {
  const compiled = compileCondition(source);
  if (!compiled.condition) return false;
  return compiled.condition.some((conjunction) =>
    conjunction.every((c) => evaluateComparison(c, input)),
  );
}

/** Folds the actions of a whole pass into the one answer the caller acts on. */
export function foldOutcome(actions: Schemas['RuleAction'][]): Schemas['RuleOutcome'] {
  if (actions.some((a) => a.type === 'REJECT')) return 'REJECTED';
  if (actions.some((a) => a.type.startsWith('REQUIRE_'))) return 'REVIEW_REQUIRED';
  if (actions.some((a) => a.type === 'PARTIAL_APPROVE')) return 'PARTIALLY_APPROVED';
  return 'APPROVED';
}

export interface RuleRun {
  outcome: Schemas['RuleOutcome'];
  results: Schemas['RuleEvaluationResultLine'][];
  actions: Schemas['RuleAction'][];
  explanations: string[];
}

/** Runs a version's rules in priority order. Writes nothing anywhere. */
export function runRules(version: StoredRuleSetVersion, input: Record<string, unknown>): RuleRun {
  const results: Schemas['RuleEvaluationResultLine'][] = [];
  const actions: Schemas['RuleAction'][] = [];
  const explanations: string[] = [];
  const ordered = [...version.rules]
    .filter((r) => r.active)
    .sort((a, b) => a.priority - b.priority);
  let sequence = 0;
  for (const rule of ordered) {
    sequence += 1;
    const matched = evaluateCondition(rule.condition, input);
    const action = matched ? rule.actions[0] : undefined;
    results.push({
      sequence,
      ruleId: rule.id,
      ruleCode: rule.code,
      matched,
      actionType: action?.type ?? null,
      actionPayload: action?.payload ?? null,
      explanationCode: rule.explanationCode,
      severity: matched ? 'WARNING' : 'INFO',
    });
    if (!matched) continue;
    actions.push(...rule.actions);
    explanations.push(rule.explanationCode);
    if (rule.stopOnMatch) break;
  }
  return { outcome: foldOutcome(actions), results, actions, explanations };
}

function actionsMatch(expected: Schemas['RuleAction'][], actual: Schemas['RuleAction'][]): boolean {
  if (expected.length !== actual.length) return false;
  return expected.every((e, i) => {
    const a = actual[i]!;
    if (e.type !== a.type) return false;
    // An entry without a payload asserts only its type.
    if (!e.payload) return true;
    return Object.entries(e.payload).every(
      ([key, value]) => JSON.stringify(a.payload?.[key]) === JSON.stringify(value),
    );
  });
}

/** Runs the stored cases of a version and reports, per case, what actually happened. */
export function runTestCases(version: StoredRuleSetVersion): Schemas['RuleTestRunResult'] {
  const cases = version.testCases.map((testCase) => {
    const run = runRules(version, testCase.input);
    const mismatches: ('OUTCOME' | 'EXPLANATIONS' | 'ACTIONS')[] = [];
    if (run.outcome !== testCase.expectedOutcome) mismatches.push('OUTCOME');
    const expectedCodes = [...testCase.expectedExplanations].sort();
    const actualCodes = [...run.explanations].sort();
    if (JSON.stringify(expectedCodes) !== JSON.stringify(actualCodes)) {
      mismatches.push('EXPLANATIONS');
    }
    if (testCase.expectedActions && !actionsMatch(testCase.expectedActions, run.actions)) {
      mismatches.push('ACTIONS');
    }
    return {
      code: testCase.code,
      passed: mismatches.length === 0,
      expectedOutcome: testCase.expectedOutcome,
      actualOutcome: run.outcome,
      expectedExplanations: testCase.expectedExplanations,
      actualExplanations: run.explanations,
      expectedActions: testCase.expectedActions,
      actualActions: run.actions,
      mismatches,
    } satisfies Schemas['RuleTestCaseResult'];
  });
  const failed = cases.filter((c) => !c.passed).length;
  return {
    ruleSetVersionId: version.id,
    passed: failed === 0 && cases.length > 0,
    total: cases.length,
    failed,
    cases,
  };
}

export function rulesHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  const findSet = (tenantId: string, id: string): StoredRuleSet | undefined =>
    world().ruleSets.find((s) => s.id === id && s.tenantId === tenantId);
  const findVersion = (tenantId: string, id: string): StoredRuleSetVersion | undefined =>
    world().ruleSetVersions.find((v) => v.id === id && v.tenantId === tenantId);

  /** A version the caller may see: a draft needs `rule.draft`, not merely `rule.read`. */
  const visibleVersion = (
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredRuleSetVersion | undefined => {
    const version = findVersion(tenantId, id);
    if (!version) return undefined;
    const agreed = version.status === 'PUBLISHED' || version.status === 'RETIRED';
    if (agreed || hasPermission(api, session, tenantId, 'rule.draft')) return version;
    return undefined;
  };

  /** Loads a DRAFT version for a set write, or the response that refuses the write. */
  const draftForWrite = (
    request: Request,
    tenantId: string,
    versionId: string,
  ): StoredRuleSetVersion | Response => {
    const version = findVersion(tenantId, versionId);
    if (!version) return notFound(api);
    const expected = parseIfMatch(request.headers.get('If-Match'));
    if (expected === null) {
      return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
    }
    if (version.status !== 'DRAFT') {
      return problem(
        api,
        409,
        'RULE_VERSION_IMMUTABLE',
        'Yalnız taslak sürümün kuralları değiştirilebilir',
      );
    }
    if (version.rowVersion !== expected) {
      return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
    }
    return version;
  };

  const immutable = (fields: string[], patch: Record<string, unknown>): FieldError[] =>
    fields.filter((f) => f in patch).map((field) => ({ field, code: 'IMMUTABLE' }));

  return [
    // --- rule sets --------------------------------------------------------------
    http.get(`${ANY}/api/v1/rule-sets`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'rule.read', false);
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
      const domainCode = url.searchParams.get('domainCode');
      const purpose = url.searchParams.get('purpose');
      const status = url.searchParams.get('status');
      const q = (url.searchParams.get('q') ?? '').trim().toLocaleLowerCase('tr');
      const rows = world()
        .ruleSets.filter((s) => s.tenantId === g.tenantId)
        .filter((s) => !domainCode || s.domainCode === domainCode)
        .filter((s) => !purpose || s.purpose === purpose)
        .filter((s) => !status || s.status === status)
        .filter(
          (s) =>
            !q ||
            s.code.toLocaleLowerCase('tr').includes(q) ||
            s.name.toLocaleLowerCase('tr').includes(q),
        );
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map((s) => toRuleSet(world(), s)),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['RuleSetPage']);
    }),

    http.post(`${ANY}/api/v1/rule-sets`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'rule.draft', true);
      if ('error' in g) return g.error;
      const body = await readJson<Schemas['CreateRuleSetRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors: FieldError[] = [];
      if (!body.code || !SET_CODE.test(body.code)) errors.push({ field: 'code', code: 'FORMAT' });
      if (!body.name || [...body.name].length < 2) errors.push({ field: 'name', code: 'LENGTH' });
      if (!body.domainCode) errors.push({ field: 'domainCode', code: 'REQUIRED' });
      if (!body.purpose) errors.push({ field: 'purpose', code: 'REQUIRED' });
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (world().ruleSets.some((s) => s.tenantId === g.tenantId && s.code === body.code)) {
        return problem(api, 409, 'RULE_SET_CODE_TAKEN', 'Bu kural seti kodu zaten kullanılıyor');
      }
      const ruleSet: StoredRuleSet = {
        id: world().nextId(),
        tenantId: g.tenantId,
        code: body.code,
        name: body.name,
        domainCode: body.domainCode,
        purpose: body.purpose,
        status: 'ACTIVE',
        rowVersion: 1,
      };
      world().ruleSets.push(ruleSet);
      return HttpResponse.json(toRuleSet(world(), ruleSet), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.get(`${ANY}/api/v1/rule-sets/:ruleSetId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'rule.read', false);
      if ('error' in g) return g.error;
      const ruleSet = findSet(g.tenantId, pathParam(params, 'ruleSetId'));
      if (!ruleSet) return notFound(api);
      return HttpResponse.json(toRuleSet(world(), ruleSet), {
        headers: { ETag: etagOf(ruleSet.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/rule-sets/:ruleSetId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'rule.draft', true);
      if ('error' in g) return g.error;
      const unsupported = requireMergePatch(api, request);
      if (unsupported) return unsupported;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null) {
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
      }
      const ruleSet = findSet(g.tenantId, pathParam(params, 'ruleSetId'));
      if (!ruleSet) return notFound(api);
      if (ruleSet.rowVersion !== expected) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      }
      const patch = await readJson<Record<string, unknown>>(request);
      if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors = immutable(['code', 'domainCode', 'purpose'], patch);
      if (errors.length > 0) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
      }
      if (typeof patch['name'] === 'string') ruleSet.name = patch['name'];
      if (patch['status'] === 'ACTIVE' || patch['status'] === 'INACTIVE') {
        ruleSet.status = patch['status'];
      }
      ruleSet.rowVersion += 1;
      return HttpResponse.json(toRuleSet(world(), ruleSet), {
        headers: { ETag: etagOf(ruleSet.rowVersion) },
      });
    }),

    // --- rule set versions ------------------------------------------------------
    http.get(`${ANY}/api/v1/rule-sets/:ruleSetId/versions`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'rule.read', false);
      if ('error' in g) return g.error;
      const ruleSet = findSet(g.tenantId, pathParam(params, 'ruleSetId'));
      if (!ruleSet) return notFound(api);
      const maySeeDrafts = hasPermission(api, g.session, g.tenantId, 'rule.draft');
      const items = world()
        .ruleSetVersions.filter((v) => v.ruleSetId === ruleSet.id)
        .filter((v) => maySeeDrafts || v.status === 'PUBLISHED' || v.status === 'RETIRED')
        .sort((a, b) => b.versionNo - a.versionNo)
        .map(toRuleSetVersionSummary);
      return HttpResponse.json({ items } satisfies Schemas['RuleSetVersionList']);
    }),

    http.post(`${ANY}/api/v1/rule-sets/:ruleSetId/versions`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'rule.draft', true);
      if ('error' in g) return g.error;
      const ruleSet = findSet(g.tenantId, pathParam(params, 'ruleSetId'));
      if (!ruleSet) return notFound(api);
      const body = (await readJson<Schemas['CreateRuleSetVersionRequest']>(request)) ?? {};
      const siblings = world().ruleSetVersions.filter((v) => v.ruleSetId === ruleSet.id);
      const source = body.copyFromVersionId
        ? siblings.find((v) => v.id === body.copyFromVersionId)
        : undefined;
      if (body.copyFromVersionId && !source) {
        return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
          errors: [{ field: 'copyFromVersionId', code: 'NOT_FOUND' }],
        });
      }
      const version: StoredRuleSetVersion = {
        id: world().nextId(),
        tenantId: g.tenantId,
        ruleSetId: ruleSet.id,
        versionNo: siblings.reduce((max, v) => Math.max(max, v.versionNo), 0) + 1,
        status: 'DRAFT',
        validFrom: body.validFrom ?? null,
        validTo: body.validTo ?? null,
        inputSchema: body.inputSchema ?? source?.inputSchema ?? {},
        notes: body.notes ?? null,
        contentHash: null,
        submittedAt: null,
        submittedBy: null,
        publishedAt: null,
        publishedBy: null,
        reviewComment: null,
        retireReasonCode: null,
        rules: (source?.rules ?? []).map((r) => ({ ...r, id: world().nextId() })),
        testCases: (source?.testCases ?? []).map((c) => ({ ...c, id: world().nextId() })),
        rowVersion: 1,
      };
      world().ruleSetVersions.push(version);
      return HttpResponse.json(toRuleSetVersion(version), {
        status: 201,
        headers: { ETag: etagOf(1) },
      });
    }),

    http.get(`${ANY}/api/v1/rule-set-versions/:ruleSetVersionId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'rule.read', false);
      if ('error' in g) return g.error;
      const version = visibleVersion(g.session, g.tenantId, pathParam(params, 'ruleSetVersionId'));
      if (!version) return notFound(api);
      return HttpResponse.json(toRuleSetVersion(version), {
        headers: { ETag: etagOf(version.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/rule-set-versions/:ruleSetVersionId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'rule.draft', true);
      if ('error' in g) return g.error;
      const unsupported = requireMergePatch(api, request);
      if (unsupported) return unsupported;
      const loaded = draftForWrite(request, g.tenantId, pathParam(params, 'ruleSetVersionId'));
      if (loaded instanceof Response) return loaded;
      const patch = await readJson<Record<string, unknown>>(request);
      if (!patch) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      if ('inputSchema' in patch) {
        const next = (patch['inputSchema'] ?? {}) as Record<string, Schemas['RuleInputType']>;
        // A variable a condition still names cannot be withdrawn: the version would
        // stop compiling.
        const stillUsed = new Set(
          loaded.rules.flatMap((r) => compileCondition(r.condition).variables),
        );
        const withdrawn = [...stillUsed].filter((v) => !(v in next));
        if (withdrawn.length > 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: withdrawn.map((field) => ({
              field: `inputSchema.${field}`,
              code: 'VARIABLE_IN_USE',
            })),
          });
        }
        loaded.inputSchema = next;
      }
      if ('validFrom' in patch) loaded.validFrom = (patch['validFrom'] as string | null) ?? null;
      if ('validTo' in patch) loaded.validTo = (patch['validTo'] as string | null) ?? null;
      if ('notes' in patch) loaded.notes = (patch['notes'] as string | null) ?? null;
      loaded.rowVersion += 1;
      return HttpResponse.json(toRuleSetVersion(loaded), {
        headers: { ETag: etagOf(loaded.rowVersion) },
      });
    }),

    http.put(
      `${ANY}/api/v1/rule-set-versions/:ruleSetVersionId/rules`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'rule.draft', true);
        if ('error' in g) return g.error;
        const loaded = draftForWrite(request, g.tenantId, pathParam(params, 'ruleSetVersionId'));
        if (loaded instanceof Response) return loaded;
        const body = await readJson<Schemas['ReplaceRulesRequest']>(request);
        if (!body?.items) {
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        }
        const errors: FieldError[] = [];
        const priorities = new Set<number>();
        const codes = new Set<string>();
        body.items.forEach((r, i) => {
          if (!r.code || !RULE_CODE.test(r.code)) {
            errors.push({ field: `items[${i}].code`, code: 'FORMAT' });
          }
          if (codes.has(r.code)) errors.push({ field: `items[${i}].code`, code: 'DUPLICATE' });
          codes.add(r.code);
          if (priorities.has(r.priority)) {
            // Evaluation order has to be total and reproducible.
            errors.push({ field: `items[${i}].priority`, code: 'PRIORITY_DUPLICATE' });
          }
          priorities.add(r.priority);
          if (!r.explanationCode || !RULE_CODE.test(r.explanationCode)) {
            errors.push({ field: `items[${i}].explanationCode`, code: 'FORMAT' });
          }
          // Compiled against the version's input schema, before anything is stored.
          const compiled = compileCondition(r.condition ?? '');
          if (!compiled.condition) {
            errors.push({
              field: `items[${i}].condition`,
              code: 'CONDITION_COMPILE_FAILED',
              ...(compiled.error ? { message: `${r.code}: ${compiled.error}` } : {}),
            });
          } else {
            for (const variable of compiled.variables) {
              if (!(variable in loaded.inputSchema)) {
                errors.push({
                  field: `items[${i}].condition`,
                  code: 'UNDECLARED_VARIABLE',
                  message: `${r.code}: tanımsız değişken ${variable}`,
                });
              }
            }
          }
        });
        if (errors.length > 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
        }
        loaded.rules = body.items.map((r): StoredRule => ({
          id: world().nextId(),
          code: r.code,
          name: r.name,
          priority: r.priority,
          condition: r.condition,
          actions: r.actions ?? [],
          explanationCode: r.explanationCode,
          explanationParams: r.explanationParams ?? null,
          stopOnMatch: r.stopOnMatch ?? false,
          active: r.active ?? true,
        }));
        loaded.rowVersion += 1;
        return HttpResponse.json(
          {
            items: [...loaded.rules]
              .sort((a, b) => a.priority - b.priority)
              .map((r) => toRule(loaded.id, r)),
          } satisfies Schemas['RuleList'],
          { headers: { ETag: etagOf(loaded.rowVersion) } },
        );
      },
    ),

    http.put(
      `${ANY}/api/v1/rule-set-versions/:ruleSetVersionId/test-cases`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'rule.draft', true);
        if ('error' in g) return g.error;
        const loaded = draftForWrite(request, g.tenantId, pathParam(params, 'ruleSetVersionId'));
        if (loaded instanceof Response) return loaded;
        const body = await readJson<Schemas['ReplaceRuleTestCasesRequest']>(request);
        if (!body?.items) {
          return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        }
        const errors: FieldError[] = [];
        const codes = new Set<string>();
        body.items.forEach((c, i) => {
          if (!c.code || !RULE_CODE.test(c.code)) {
            errors.push({ field: `items[${i}].code`, code: 'FORMAT' });
          }
          if (codes.has(c.code)) errors.push({ field: `items[${i}].code`, code: 'DUPLICATE' });
          codes.add(c.code);
          if (!c.expectedOutcome) {
            errors.push({ field: `items[${i}].expectedOutcome`, code: 'REQUIRED' });
          }
        });
        if (errors.length > 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', { errors });
        }
        loaded.testCases = body.items.map((c): StoredRuleTestCase => ({
          id: world().nextId(),
          code: c.code,
          description: c.description ?? null,
          input: c.input ?? {},
          expectedOutcome: c.expectedOutcome,
          expectedExplanations: c.expectedExplanations ?? [],
          expectedActions: c.expectedActions ?? null,
        }));
        loaded.rowVersion += 1;
        return HttpResponse.json(
          {
            items: loaded.testCases.map((c) => toRuleTestCase(loaded.id, c)),
          } satisfies Schemas['RuleTestCaseList'],
          { headers: { ETag: etagOf(loaded.rowVersion) } },
        );
      },
    ),

    // The colon is escaped so path-to-regexp reads ":run" as literal text.
    http.post(
      `${ANY}/api/v1/rule-set-versions/:ruleSetVersionId/tests\\:run`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'rule.read', true);
        if ('error' in g) return g.error;
        const version = visibleVersion(
          g.session,
          g.tenantId,
          pathParam(params, 'ruleSetVersionId'),
        );
        if (!version) return notFound(api);
        // Writes nothing: no evaluation row, no test result row, no side effect.
        return HttpResponse.json(runTestCases(version));
      },
    ),

    // The colon is escaped so path-to-regexp reads ":simulate" as literal text.
    http.post(
      `${ANY}/api/v1/rule-set-versions/:ruleSetVersionId\\:simulate`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'rule.read', true);
        if ('error' in g) return g.error;
        const version = visibleVersion(
          g.session,
          g.tenantId,
          pathParam(params, 'ruleSetVersionId'),
        );
        if (!version) return notFound(api);
        const body = await readJson<Schemas['SimulateRuleSetVersionRequest']>(request);
        if (!body?.input) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: [{ field: 'input', code: 'REQUIRED' }],
          });
        }
        const run = runRules(version, body.input);
        // The engine returns actions and performs none of them; nothing is recorded.
        return HttpResponse.json({
          ruleSetVersionId: version.id,
          versionNo: version.versionNo,
          status: version.status,
          outcome: run.outcome,
          durationMs: 0,
          results: run.results,
        } satisfies Schemas['RuleEvaluationTrace']);
      },
    ),

    http.post(
      `${ANY}/api/v1/rule-set-versions/:ruleSetVersionId/submit`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'rule.draft', true);
        if ('error' in g) return g.error;
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null) {
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        }
        const version = findVersion(g.tenantId, pathParam(params, 'ruleSetVersionId'));
        if (!version) return notFound(api);
        if (version.status !== 'DRAFT') {
          return problem(
            api,
            409,
            'RULE_VERSION_IMMUTABLE',
            'Yalnız taslak sürüm incelemeye gönderilebilir',
          );
        }
        if (version.rowVersion !== expected) {
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
        }
        if (version.validFrom === null) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: [{ field: 'validFrom', code: 'REQUIRED' }],
          });
        }
        // The gate the whole package exists for: an untested rule that decides what a
        // member is owed is not a rule anybody should have to trust.
        if (version.testCases.length === 0) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: [{ field: 'testCases', code: 'TESTS_REQUIRED' }],
          });
        }
        const run = runTestCases(version);
        if (!run.passed) {
          return problem(api, 422, 'VALIDATION_FAILED', 'Doğrulama hatası', {
            errors: [
              {
                field: 'testCases',
                code: 'TESTS_FAILING',
                message: run.cases
                  .filter((c) => !c.passed)
                  .map((c) => c.code)
                  .join(', '),
              },
            ],
          });
        }
        const body = (await readJson<Schemas['ReviewComment']>(request)) ?? {};
        version.status = 'UNDER_REVIEW';
        version.submittedAt = new Date().toISOString();
        version.submittedBy = g.session.account.actorId;
        version.reviewComment = body.comment ?? null;
        version.rowVersion += 1;
        return HttpResponse.json(toRuleSetVersion(version), {
          headers: { ETag: etagOf(version.rowVersion) },
        });
      },
    ),

    http.post(
      `${ANY}/api/v1/rule-set-versions/:ruleSetVersionId/publish`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'rule.publish', true);
        if ('error' in g) return g.error;
        if (!hasStepUp(g.session)) return stepUpRequired(api);
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null) {
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        }
        const version = findVersion(g.tenantId, pathParam(params, 'ruleSetVersionId'));
        if (!version) return notFound(api);
        if (version.status !== 'UNDER_REVIEW') {
          return problem(
            api,
            409,
            'RULE_VERSION_IMMUTABLE',
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
        const clash = world().ruleSetVersions.find(
          (v) =>
            v.id !== version.id &&
            v.ruleSetId === version.ruleSetId &&
            v.status === 'PUBLISHED' &&
            v.validFrom !== null &&
            version.validFrom !== null &&
            periodsOverlap(v.validFrom, v.validTo, version.validFrom, version.validTo),
        );
        if (clash) {
          return problem(
            api,
            409,
            'RULE_SET_VERSION_OVERLAP',
            'Bu tarih aralığında yayında başka bir sürüm var',
          );
        }
        const body = (await readJson<Schemas['ReviewComment']>(request)) ?? {};
        version.status = 'PUBLISHED';
        version.publishedAt = new Date().toISOString();
        version.publishedBy = g.session.account.actorId;
        version.contentHash = pseudoHash(
          `${version.ruleSetId}:${version.versionNo}:${JSON.stringify(version.inputSchema)}:${version.rules
            .map((r) => `${r.code}=${r.condition}`)
            .join('|')}`,
        );
        if (body.comment) version.reviewComment = body.comment;
        version.rowVersion += 1;
        return HttpResponse.json(toRuleSetVersion(version), {
          headers: { ETag: etagOf(version.rowVersion) },
        });
      },
    ),

    http.post(
      `${ANY}/api/v1/rule-set-versions/:ruleSetVersionId/retire`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'rule.publish', true);
        if ('error' in g) return g.error;
        if (!hasStepUp(g.session)) return stepUpRequired(api);
        const expected = parseIfMatch(request.headers.get('If-Match'));
        if (expected === null) {
          return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli');
        }
        const version = findVersion(g.tenantId, pathParam(params, 'ruleSetVersionId'));
        if (!version) return notFound(api);
        if (version.status !== 'PUBLISHED') {
          return problem(
            api,
            409,
            'RULE_VERSION_IMMUTABLE',
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
        return HttpResponse.json(toRuleSetVersion(version), {
          headers: { ETag: etagOf(version.rowVersion) },
        });
      },
    ),

    // --- recorded evaluations ---------------------------------------------------
    http.get(`${ANY}/api/v1/rule-evaluations/:ruleEvaluationId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'rule.read', false);
      if ('error' in g) return g.error;
      const evaluation = world().ruleEvaluations.find(
        (e) => e.id === pathParam(params, 'ruleEvaluationId') && e.tenantId === g.tenantId,
      );
      if (!evaluation) return notFound(api);
      const { tenantId: _tenantId, ...out } = evaluation;
      return HttpResponse.json(out);
    }),
  ];
}
