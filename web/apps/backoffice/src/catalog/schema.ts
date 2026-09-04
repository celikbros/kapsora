import type { CodeValueInput } from '@kapsora/api-client';
import { z } from 'zod';

// The parser that turns a zod issue back into an i18n key lives with the other problem
// helpers; every catalog form needs both, so it is re-exported here.
export { parseIssueMessage } from '../problems';

/** Category and service codes: the tenant's own stable vocabulary, so they are strict. */
const CODE = /^[A-Z][A-Z0-9_]{1,63}$/;
/** A code system code is shorter; it prefixes every value it publishes. */
const SYSTEM_CODE = /^[A-Z][A-Z0-9_]{1,39}$/;

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

export const FULFILLMENT_MODES = [
  'APPOINTMENT',
  'RESERVATION',
  'WORK_ORDER',
  'MEMBERSHIP',
  'SESSION',
  'VOUCHER',
  'REIMBURSEMENT',
  'DIRECT',
] as const;

export const SERVICE_UNIT_TYPES = [
  'MONEY',
  'COUNT',
  'NIGHT',
  'SESSION',
  'HOUR',
  'KILOMETER',
  'POINT',
] as const;

export const CODE_SYSTEM_AUTHORITIES = ['SGK', 'SB', 'WHO', 'TENANT', 'OTHER'] as const;
export const CODE_SYSTEM_STATUSES = ['ACTIVE', 'INACTIVE'] as const;

/** At most 5000 rows per import, all or nothing (WP-I3-01). */
export const MAX_IMPORT_ROWS = 5000;

const isoDate = z.string();
const requiredDate = z.string().min(1, 'REQUIRED');

/** Half-open periods: an end before the start is the operator's mistake, not the server's. */
function checkPeriod(
  period: { validFrom: string; validTo: string },
  ctx: z.RefinementCtx,
  path: PropertyKey[] = ['validTo'],
): void {
  if (period.validFrom !== '' && period.validTo !== '' && period.validTo < period.validFrom) {
    ctx.addIssue({ code: 'custom', message: 'DATE_ORDER', path });
  }
}

/** A new category. The parent is chosen from a picker; the server owns depth and cycles. */
export const categoryFormSchema = z.object({
  code: z.string().trim().regex(CODE, 'PATTERN'),
  name: z.string().trim().min(2, 'MIN_LENGTH:2').max(200, 'MAX_LENGTH:200'),
  domain: z.enum(SERVICE_DOMAINS),
  parentId: z.string(),
  active: z.boolean(),
});

export type CategoryFormValues = z.infer<typeof categoryFormSchema>;

export const emptyCategoryForm: CategoryFormValues = {
  code: '',
  name: '',
  domain: 'HEALTH',
  parentId: '',
  active: true,
};

/** The merge-patch of a category: code and domain are what the category is. */
export const categoryEditSchema = z.object({
  name: z.string().trim().min(2, 'MIN_LENGTH:2').max(200, 'MAX_LENGTH:200'),
  parentId: z.string(),
  active: z.boolean(),
});

export type CategoryEditValues = z.infer<typeof categoryEditSchema>;

export const definitionFormSchema = z.object({
  code: z.string().trim().regex(CODE, 'PATTERN'),
  name: z.string().trim().min(2, 'MIN_LENGTH:2').max(200, 'MAX_LENGTH:200'),
  categoryId: z.string().uuid('REQUIRED'),
  fulfillmentMode: z.enum(FULFILLMENT_MODES),
  defaultUnitType: z.enum(SERVICE_UNIT_TYPES),
  description: z.string().trim().max(2000, 'MAX_LENGTH:2000'),
  requiresProvider: z.boolean(),
  active: z.boolean(),
});

export type DefinitionFormValues = z.infer<typeof definitionFormSchema>;

export const emptyDefinitionForm: DefinitionFormValues = {
  code: '',
  name: '',
  categoryId: '',
  fulfillmentMode: 'APPOINTMENT',
  defaultUnitType: 'COUNT',
  description: '',
  requiresProvider: true,
  active: true,
};

/**
 * The edit form. `code` is present so the field can be shown and so a server 422 with
 * `IMMUTABLE` has somewhere to land, but the patch never carries it.
 */
export const definitionEditSchema = definitionFormSchema;

export type DefinitionEditValues = DefinitionFormValues;

/** One external code a definition is reported under, for one period. */
export const mappingRowSchema = z
  .object({
    codeSystemId: z.string().uuid('REQUIRED'),
    code: z.string().trim().min(1, 'REQUIRED').max(64, 'MAX_LENGTH:64'),
    validFrom: requiredDate,
    validTo: isoDate,
    primary: z.boolean(),
  })
  .superRefine((row, ctx) => checkPeriod(row, ctx));

export const mappingsFormSchema = z.object({ items: z.array(mappingRowSchema) });

export type MappingRowValues = z.infer<typeof mappingRowSchema>;
export type MappingsFormValues = z.infer<typeof mappingsFormSchema>;

export function emptyMappingRow(validFrom: string): MappingRowValues {
  return { codeSystemId: '', code: '', validFrom, validTo: '', primary: false };
}

export const codeSystemFormSchema = z
  .object({
    code: z.string().trim().regex(SYSTEM_CODE, 'PATTERN'),
    name: z.string().trim().min(2, 'MIN_LENGTH:2').max(200, 'MAX_LENGTH:200'),
    version: z.string().trim().min(1, 'REQUIRED').max(40, 'MAX_LENGTH:40'),
    authority: z.enum(CODE_SYSTEM_AUTHORITIES),
    licensed: z.boolean(),
    validFrom: requiredDate,
    validTo: isoDate,
  })
  .superRefine((form, ctx) => checkPeriod(form, ctx));

export type CodeSystemFormValues = z.infer<typeof codeSystemFormSchema>;

export function emptyCodeSystemForm(validFrom: string): CodeSystemFormValues {
  return {
    code: '',
    name: '',
    version: '',
    authority: 'TENANT',
    licensed: false,
    validFrom,
    validTo: '',
  };
}

/** The merge-patch of a code system: the code and the version identify it, so they stay. */
export const codeSystemEditSchema = z.object({
  name: z.string().trim().min(2, 'MIN_LENGTH:2').max(200, 'MAX_LENGTH:200'),
  authority: z.enum(CODE_SYSTEM_AUTHORITIES),
  licensed: z.boolean(),
  status: z.enum(CODE_SYSTEM_STATUSES),
  validTo: isoDate,
});

export type CodeSystemEditValues = z.infer<typeof codeSystemEditSchema>;

/** One row of a pasted import batch, exactly as the API takes it. */
export const codeValueImportRowSchema = z.object({
  code: z.string().min(1, 'REQUIRED'),
  display: z.string().min(1, 'REQUIRED'),
  parentCode: z.string().nullish(),
  validFrom: requiredDate,
  validTo: z.string().nullish(),
  active: z.boolean().optional(),
  attributes: z.record(z.string(), z.unknown()).optional(),
});

export const codeValueImportSchema = z
  .array(codeValueImportRowSchema)
  .min(1, 'REQUIRED')
  .max(MAX_IMPORT_ROWS, 'MAX_ITEMS');

type CodeValueImportRow = z.infer<typeof codeValueImportRowSchema>;

/** Drops the keys the paste left out rather than sending them as `undefined`. */
function toCodeValueInput(row: CodeValueImportRow): CodeValueInput {
  return {
    code: row.code,
    display: row.display,
    validFrom: row.validFrom,
    ...(row.parentCode !== undefined ? { parentCode: row.parentCode } : {}),
    ...(row.validTo !== undefined ? { validTo: row.validTo } : {}),
    ...(row.active !== undefined ? { active: row.active } : {}),
    ...(row.attributes !== undefined ? { attributes: row.attributes } : {}),
  };
}

/**
 * Reads a pasted batch. It accepts either a bare array or the `{ items: [...] }` envelope
 * the API takes, because an operator copying from the contract will paste either. The
 * error is an i18n code, never a parser message: a JSON stack trace helps nobody.
 */
export function parseCodeValueBatch(raw: string): { rows: CodeValueInput[] } | { error: string } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return { error: 'FORMAT' };
  }
  const envelope =
    parsed !== null && typeof parsed === 'object' && 'items' in parsed
      ? (parsed as { items: unknown }).items
      : parsed;
  const result = codeValueImportSchema.safeParse(envelope);
  if (!result.success) {
    return { error: result.error.issues[0]?.message ?? 'FORMAT' };
  }
  return { rows: result.data.map(toCodeValueInput) };
}
