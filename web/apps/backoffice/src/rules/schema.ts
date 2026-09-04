import type {
  Rule,
  RuleInput,
  RuleInputType,
  RuleTestCase,
  RuleTestCaseInput,
} from '@kapsora/api-client';
import { z } from 'zod';

/** A rule set code is the tenant's own stable identifier, so it is strict. */
const SET_CODE = /^[A-Z][A-Z0-9_]{1,39}$/;
/** Rule, explanation and test case codes share one shape with the server. */
const RULE_CODE = /^[A-Z][A-Z0-9_]{1,63}$/;

export const SERVICE_DOMAINS = [
  'GENERIC',
  'HEALTH',
  'ACCOMMODATION',
  'ASSISTANCE',
  'EDUCATION',
  'SPORT',
  'TRANSPORT',
  'CARE',
  'OTHER',
] as const;

export const RULE_SET_PURPOSES = [
  'ELIGIBILITY',
  'DOCUMENT',
  'PREAUTH',
  'LIMIT',
  'DUPLICATE',
  'DIAGNOSIS_SERVICE',
  'PRICE',
  'ADJUDICATION',
] as const;

export const RULE_SET_STATUSES = ['ACTIVE', 'INACTIVE'] as const;

/** ADR-023: the actions are a closed list held by the server, never CEL. */
export const RULE_ACTION_TYPES = [
  'APPROVE',
  'REJECT',
  'WARN',
  'REQUIRE_DOCUMENT',
  'REQUIRE_PREAUTH',
  'REQUIRE_MEDICAL_REVIEW',
  'REQUIRE_FINANCIAL_REVIEW',
  'PARTIAL_APPROVE',
  'RESERVE_ENTITLEMENT',
  'ADJUST_PRICE',
  'SET_LIMIT',
] as const;

export const RULE_OUTCOMES = [
  'APPROVED',
  'REJECTED',
  'REVIEW_REQUIRED',
  'PARTIALLY_APPROVED',
] as const;

/** Reason codes the server recognises for taking a published version out of use. */
export const RETIRE_REASONS = ['SUPERSEDED', 'SPONSOR_REQUEST', 'ERROR', 'REGULATORY'] as const;

/** True for text that parses to a JSON object; arrays and scalars are not inputs. */
function isJsonObject(text: string): boolean {
  try {
    const parsed: unknown = JSON.parse(text);
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed);
  } catch {
    return false;
  }
}

/** Parses text already validated as a JSON object. */
export function parseJsonObject(text: string): Record<string, unknown> {
  return JSON.parse(text) as Record<string, unknown>;
}

/** Explanation codes as the operator types them: separated by commas or spaces. */
export function parseExplanationCodes(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((code) => code.trim())
    .filter((code) => code !== '');
}

const jsonObjectText = z.string().trim().min(1, 'REQUIRED').refine(isJsonObject, 'FORMAT');

export const ruleSetFormSchema = z.object({
  code: z.string().trim().regex(SET_CODE, 'PATTERN'),
  name: z.string().trim().min(2, 'MIN_LENGTH:2').max(200, 'MAX_LENGTH:200'),
  domainCode: z.enum(SERVICE_DOMAINS),
  purpose: z.enum(RULE_SET_PURPOSES),
});

export type RuleSetFormValues = z.infer<typeof ruleSetFormSchema>;

export const emptyRuleSetForm: RuleSetFormValues = {
  code: '',
  name: '',
  domainCode: 'HEALTH',
  purpose: 'DOCUMENT',
};

/** The set's own editable fields; code, domain and purpose are immutable after creation. */
export const ruleSetEditFormSchema = z.object({
  name: z.string().trim().min(2, 'MIN_LENGTH:2').max(200, 'MAX_LENGTH:200'),
  status: z.enum(RULE_SET_STATUSES),
});

export type RuleSetEditFormValues = z.infer<typeof ruleSetEditFormSchema>;

export const ruleSetVersionFormSchema = z
  .object({
    validFrom: z.string().min(1, 'REQUIRED'),
    validTo: z.string(),
    notes: z.string().trim().max(2000, 'MAX_LENGTH:2000'),
    copyFromVersionId: z.string(),
  })
  .superRefine((form, ctx) => {
    if (form.validTo !== '' && form.validTo < form.validFrom) {
      ctx.addIssue({ code: 'custom', message: 'DATE_ORDER', path: ['validTo'] });
    }
  });

export type RuleSetVersionFormValues = z.infer<typeof ruleSetVersionFormSchema>;

/**
 * The CEL types a condition's variables may have (ADR-023). The environment offers these
 * and nothing else: no type here reaches a network, a file or a database.
 */
export const RULE_INPUT_TYPES = [
  'string',
  'int',
  'double',
  'bool',
  'timestamp',
  'duration',
  'map',
  'list',
] as const;

/**
 * The declared variables of a version, edited as the JSON map the API stores. CEL is code
 * and so is its environment, so an operator writing rules reads this better as a document
 * than as a row of pickers.
 */
const inputSchemaText = z
  .string()
  .trim()
  .min(1, 'REQUIRED')
  .superRefine((text, ctx) => {
    if (!isJsonObject(text)) {
      ctx.addIssue({ code: 'custom', message: 'FORMAT' });
      return;
    }
    const declared: string[] = Object.values(parseJsonObject(text)).map(String);
    if (declared.some((type) => !RULE_INPUT_TYPES.some((known) => known === type))) {
      ctx.addIssue({ code: 'custom', message: 'TYPE' });
    }
  });

/** The draft version's own fields, including the environment its conditions compile in. */
export const ruleVersionDraftFormSchema = z
  .object({
    validFrom: z.string().min(1, 'REQUIRED'),
    validTo: z.string(),
    notes: z.string().trim().max(2000, 'MAX_LENGTH:2000'),
    inputSchema: inputSchemaText,
  })
  .superRefine((form, ctx) => {
    if (form.validTo !== '' && form.validTo < form.validFrom) {
      ctx.addIssue({ code: 'custom', message: 'DATE_ORDER', path: ['validTo'] });
    }
  });

export type RuleVersionDraftFormValues = z.infer<typeof ruleVersionDraftFormSchema>;

export function parseInputSchema(text: string): Record<string, RuleInputType> {
  return parseJsonObject(text) as Record<string, RuleInputType>;
}

export const emptyRuleSetVersionForm: RuleSetVersionFormValues = {
  validFrom: '',
  validTo: '',
  notes: '',
  copyFromVersionId: '',
};

const ruleActionRowSchema = z
  .object({
    type: z.enum(RULE_ACTION_TYPES),
    /** Optional; the typed payload as JSON, with every amount an exact decimal string. */
    payload: z.string().trim(),
  })
  .superRefine((row, ctx) => {
    if (row.payload !== '' && !isJsonObject(row.payload)) {
      ctx.addIssue({ code: 'custom', message: 'FORMAT', path: ['payload'] });
    }
  });

export type RuleActionRowValues = z.infer<typeof ruleActionRowSchema>;

export const ruleRowSchema = z.object({
  code: z.string().trim().regex(RULE_CODE, 'PATTERN'),
  name: z.string().trim().min(1, 'REQUIRED').max(200, 'MAX_LENGTH:200'),
  /** Kept as text so an empty field is a validation error rather than a silent zero. */
  priority: z
    .string()
    .trim()
    .regex(/^\d{1,6}$/, 'FORMAT'),
  /** A CEL expression. It is compiled by the server; the client only insists on one. */
  condition: z.string().trim().min(1, 'REQUIRED'),
  explanationCode: z.string().trim().regex(RULE_CODE, 'PATTERN'),
  stopOnMatch: z.boolean(),
  active: z.boolean(),
  actions: z.array(ruleActionRowSchema),
});

export type RuleRowValues = z.infer<typeof ruleRowSchema>;

export const rulesFormSchema = z
  .object({ items: z.array(ruleRowSchema) })
  .superRefine((form, ctx) => {
    // Evaluation order has to be total and reproducible, so two rules of one version may
    // not share a priority. Catching it here saves the operator a refused round trip.
    const codes = new Set<string>();
    const priorities = new Set<string>();
    form.items.forEach((row, index) => {
      if (codes.has(row.code)) {
        ctx.addIssue({ code: 'custom', message: 'DUPLICATE', path: ['items', index, 'code'] });
      }
      codes.add(row.code);
      if (priorities.has(row.priority)) {
        ctx.addIssue({
          code: 'custom',
          message: 'DUPLICATE',
          path: ['items', index, 'priority'],
        });
      }
      priorities.add(row.priority);
    });
  });

export type RulesFormValues = z.infer<typeof rulesFormSchema>;

export const emptyRuleRow: RuleRowValues = {
  code: '',
  name: '',
  priority: '10',
  condition: '',
  explanationCode: '',
  stopOnMatch: false,
  active: true,
  actions: [{ type: 'WARN', payload: '' }],
};

export const testCaseRowSchema = z
  .object({
    code: z.string().trim().regex(RULE_CODE, 'PATTERN'),
    description: z.string().trim().max(500, 'MAX_LENGTH:500'),
    input: jsonObjectText,
    expectedOutcome: z.enum(RULE_OUTCOMES),
    expectedExplanations: z.string().trim(),
  })
  .superRefine((row, ctx) => {
    const invalid = parseExplanationCodes(row.expectedExplanations).some(
      (code) => !RULE_CODE.test(code),
    );
    if (invalid) {
      ctx.addIssue({ code: 'custom', message: 'PATTERN', path: ['expectedExplanations'] });
    }
  });

export type TestCaseRowValues = z.infer<typeof testCaseRowSchema>;

export const testCasesFormSchema = z
  .object({ items: z.array(testCaseRowSchema) })
  .superRefine((form, ctx) => {
    const codes = new Set<string>();
    form.items.forEach((row, index) => {
      if (codes.has(row.code)) {
        ctx.addIssue({ code: 'custom', message: 'DUPLICATE', path: ['items', index, 'code'] });
      }
      codes.add(row.code);
    });
  });

export type TestCasesFormValues = z.infer<typeof testCasesFormSchema>;

export const emptyTestCaseRow: TestCaseRowValues = {
  code: '',
  description: '',
  input: '{}',
  expectedOutcome: 'APPROVED',
  expectedExplanations: '',
};

export const simulationFormSchema = z.object({ input: jsonObjectText });

export type SimulationFormValues = z.infer<typeof simulationFormSchema>;

export const retireFormSchema = z.object({
  reasonCode: z.string().trim().min(1, 'REQUIRED').max(60, 'MAX_LENGTH:60'),
  reasonText: z.string().trim().max(500, 'MAX_LENGTH:500'),
});

export type RetireFormValues = z.infer<typeof retireFormSchema>;

/** A stored rule as the form holds it; the priority stays text until it is sent. */
export function toRuleRow(rule: Rule): RuleRowValues {
  return {
    code: rule.code,
    name: rule.name,
    priority: String(rule.priority),
    condition: rule.condition,
    explanationCode: rule.explanationCode,
    stopOnMatch: rule.stopOnMatch,
    active: rule.active,
    actions: rule.actions.map((action) => ({
      type: action.type,
      payload: action.payload ? JSON.stringify(action.payload) : '',
    })),
  };
}

export function toRuleInput(row: RuleRowValues): RuleInput {
  return {
    code: row.code,
    name: row.name,
    // An ordinal, not a quantity: parsed as an integer after the digits-only check.
    priority: Number.parseInt(row.priority, 10),
    condition: row.condition,
    explanationCode: row.explanationCode,
    stopOnMatch: row.stopOnMatch,
    active: row.active,
    actions: row.actions.map((action) => ({
      type: action.type,
      ...(action.payload !== '' ? { payload: parseJsonObject(action.payload) } : {}),
    })),
  };
}

export function toTestCaseRow(testCase: RuleTestCase): TestCaseRowValues {
  return {
    code: testCase.code,
    description: testCase.description ?? '',
    input: JSON.stringify(testCase.input, null, 2),
    expectedOutcome: testCase.expectedOutcome,
    expectedExplanations: testCase.expectedExplanations.join(', '),
  };
}

export function toTestCaseInput(row: TestCaseRowValues): RuleTestCaseInput {
  return {
    code: row.code,
    input: parseJsonObject(row.input),
    expectedOutcome: row.expectedOutcome,
    expectedExplanations: parseExplanationCodes(row.expectedExplanations),
    ...(row.description !== '' ? { description: row.description } : {}),
  };
}

/** The next free priority, so an appended rule never collides with an existing one. */
export function nextPriority(rows: RuleRowValues[]): string {
  const highest = rows.reduce((max, row) => {
    const value = Number.parseInt(row.priority, 10);
    return Number.isFinite(value) && value > max ? value : max;
  }, 0);
  return String(highest + 10);
}
