import { z } from 'zod';

/** Program and plan codes are the tenant's own stable identifiers, so they are strict. */
const CODE = /^[A-Z][A-Z0-9_-]{1,39}$/;

export const PROGRAM_TYPES = [
  'EMPLOYEE_BENEFIT',
  'INSURANCE',
  'MEMBERSHIP',
  'SOCIAL_AID',
  'OTHER',
] as const;

/** Transitions the server accepts, mirrored so the form offers nothing it will refuse. */
export const PROGRAM_TRANSITIONS: Record<string, readonly string[]> = {
  DRAFT: ['ACTIVE'],
  ACTIVE: ['SUSPENDED', 'CLOSED'],
  SUSPENDED: ['ACTIVE', 'CLOSED'],
  CLOSED: [],
};
export const PLAN_TRANSITIONS: Record<string, readonly string[]> = {
  DRAFT: ['ACTIVE'],
  ACTIVE: ['RETIRED'],
  RETIRED: [],
};

export const UNIT_TYPES = [
  'MONEY',
  'COUNT',
  'NIGHT',
  'SESSION',
  'HOUR',
  'KILOMETER',
  'POINT',
] as const;
export const PERIOD_TYPES = [
  'CALENDAR_YEAR',
  'PLAN_YEAR',
  'ROLLING_DAYS',
  'LIFETIME',
  'CUSTOM',
] as const;
export const ROLLOVER_POLICIES = ['NONE', 'FULL', 'CAPPED'] as const;

const dateOrEmpty = z.string();

export const programFormSchema = z
  .object({
    code: z.string().trim().regex(CODE, 'PATTERN'),
    name: z.string().trim().min(1, 'REQUIRED').max(200, 'MAX_LENGTH:200'),
    programType: z.string().min(1, 'REQUIRED'),
    sponsorOrganizationId: z.string().uuid('REQUIRED'),
    payerOrganizationId: z.string().uuid('REQUIRED'),
    validFrom: dateOrEmpty,
    validTo: dateOrEmpty,
  })
  .superRefine((form, ctx) => {
    if (form.validFrom !== '' && form.validTo !== '' && form.validTo < form.validFrom) {
      ctx.addIssue({ code: 'custom', message: 'DATE_ORDER', path: ['validTo'] });
    }
  });

export type ProgramFormValues = z.infer<typeof programFormSchema>;

export const emptyProgramForm: ProgramFormValues = {
  code: '',
  name: '',
  programType: 'EMPLOYEE_BENEFIT',
  sponsorOrganizationId: '',
  payerOrganizationId: '',
  validFrom: '',
  validTo: '',
};

export const planFormSchema = z.object({
  code: z.string().trim().regex(CODE, 'PATTERN'),
  name: z.string().trim().min(1, 'REQUIRED').max(200, 'MAX_LENGTH:200'),
});

export type PlanFormValues = z.infer<typeof planFormSchema>;

export const planVersionFormSchema = z
  .object({
    validFrom: z.string().min(1, 'REQUIRED'),
    validTo: dateOrEmpty,
    notes: z.string().trim().max(2000, 'MAX_LENGTH:2000'),
    copyFromVersionId: z.string(),
  })
  .superRefine((form, ctx) => {
    if (form.validTo !== '' && form.validTo < form.validFrom) {
      ctx.addIssue({ code: 'custom', message: 'DATE_ORDER', path: ['validTo'] });
    }
  });

export type PlanVersionFormValues = z.infer<typeof planVersionFormSchema>;

/**
 * A quantity as the operator types it. It stays a string all the way to the wire: the API
 * stores `numeric(20,6)` and a JavaScript number would silently round it.
 */
const decimalString = z
  .string()
  .trim()
  .refine((value) => /^-?\d{1,14}(\.\d{1,6})?$/.test(value), 'QUANTITY_INVALID');

export const definitionRowSchema = z
  .object({
    code: z
      .string()
      .trim()
      .regex(/^[A-Z][A-Z0-9_]{1,39}$/, 'PATTERN'),
    name: z.string().trim().min(1, 'REQUIRED').max(200, 'MAX_LENGTH:200'),
    unitType: z.enum(UNIT_TYPES),
    currencyCode: z.string(),
    periodType: z.enum(PERIOD_TYPES),
    periodLength: z.string(),
    initialQuantity: decimalString,
    rolloverPolicy: z.enum(ROLLOVER_POLICIES),
    rolloverCap: z.string(),
    allowOverdraft: z.boolean(),
    familyShared: z.boolean(),
  })
  .superRefine((row, ctx) => {
    // These four rules are the server's; mirroring them keeps a bad row out of the round trip.
    if (row.unitType === 'MONEY' && row.currencyCode === '') {
      ctx.addIssue({ code: 'custom', message: 'CURRENCY_REQUIRED', path: ['currencyCode'] });
    }
    if (row.unitType !== 'MONEY' && row.currencyCode !== '') {
      ctx.addIssue({ code: 'custom', message: 'CURRENCY_FORBIDDEN', path: ['currencyCode'] });
    }
    if (row.periodType === 'ROLLING_DAYS' && row.periodLength === '') {
      ctx.addIssue({ code: 'custom', message: 'PERIOD_LENGTH_REQUIRED', path: ['periodLength'] });
    }
    if (row.rolloverPolicy === 'CAPPED' && row.rolloverCap === '') {
      ctx.addIssue({ code: 'custom', message: 'ROLLOVER_CAP_REQUIRED', path: ['rolloverCap'] });
    }
  });

export const definitionsFormSchema = z
  .object({ items: z.array(definitionRowSchema).min(1, 'REQUIRED') })
  .superRefine((form, ctx) => {
    const seen = new Set<string>();
    form.items.forEach((row, index) => {
      if (seen.has(row.code)) {
        ctx.addIssue({
          code: 'custom',
          message: 'IDENTIFIER_DUPLICATE',
          path: ['items', index, 'code'],
        });
      }
      seen.add(row.code);
    });
  });

export type DefinitionRowValues = z.infer<typeof definitionRowSchema>;
export type DefinitionsFormValues = z.infer<typeof definitionsFormSchema>;

export const emptyDefinitionRow: DefinitionRowValues = {
  code: '',
  name: '',
  unitType: 'MONEY',
  currencyCode: 'TRY',
  periodType: 'CALENDAR_YEAR',
  periodLength: '',
  initialQuantity: '0',
  rolloverPolicy: 'NONE',
  rolloverCap: '',
  allowOverdraft: false,
  familyShared: false,
};

/** The reason a published version is taken out of use. */
export const retireFormSchema = z.object({
  reasonCode: z.string().trim().min(1, 'REQUIRED').max(60, 'MAX_LENGTH:60'),
  reasonText: z.string().trim().max(500, 'MAX_LENGTH:500'),
});

export type RetireFormValues = z.infer<typeof retireFormSchema>;
