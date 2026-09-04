import { z } from 'zod';

export { parseIssueMessage } from '../problems';

/** Contract, price-list and package codes are the tenant's own stable identifiers. */
const CODE = /^[A-Z][A-Z0-9_-]{1,39}$/;

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

export const CONTRACT_TRANSITIONS: Record<string, readonly string[]> = {
  DRAFT: ['ACTIVE'],
  ACTIVE: ['SUSPENDED', 'CLOSED'],
  SUSPENDED: ['ACTIVE', 'CLOSED'],
  CLOSED: [],
};

export const PRICING_METHODS = ['FIXED', 'UNIT', 'PERCENT_OF_LIST', 'FORMULA'] as const;
export const SHARE_METHODS = ['NONE', 'FIXED', 'PERCENT'] as const;
export const UNIT_TYPES = [
  'MONEY',
  'COUNT',
  'NIGHT',
  'SESSION',
  'HOUR',
  'KILOMETER',
  'POINT',
] as const;
export const SCOPE_TYPES = ['DEFINITION', 'CATEGORY', 'PACKAGE'] as const;
export const QUOTA_PERIODS = ['DAY', 'WEEK', 'MONTH', 'YEAR', 'CONTRACT'] as const;
export const SETTLEMENT_METHODS = ['BANK_TRANSFER', 'OFFSET', 'OTHER'] as const;
export const TAX_BEHAVIOURS = ['EXCLUSIVE', 'INCLUSIVE', 'EXEMPT'] as const;
export const RETIRE_REASONS = ['SUPERSEDED', 'SPONSOR_REQUEST', 'ERROR', 'REGULATORY'] as const;

/**
 * A money field as the operator types it. It stays a string all the way to the wire: the
 * API stores numeric(20,6) and a JavaScript number would round it. Empty means absent.
 */
const money = z
  .string()
  .trim()
  .refine((v) => v === '' || /^\d{1,14}(\.\d{1,6})?$/.test(v), 'QUANTITY_INVALID');

const requiredMoney = z
  .string()
  .trim()
  .refine((v) => /^\d{1,14}(\.\d{1,6})?$/.test(v), 'QUANTITY_INVALID');

const percent = z
  .string()
  .trim()
  .refine(
    (v) => v === '' || (/^\d{1,3}(\.\d{1,6})?$/.test(v) && Number.parseFloat(v) <= 100),
    'QUANTITY_INVALID',
  );

export const contractFormSchema = z.object({
  code: z.string().trim().regex(CODE, 'PATTERN'),
  name: z.string().trim().min(1, 'REQUIRED').max(200, 'MAX_LENGTH:200'),
  payerOrganizationId: z.string().uuid('REQUIRED'),
  sponsorOrganizationId: z.string(),
  providerProfileId: z.string().uuid('REQUIRED'),
  domainCode: z.enum(SERVICE_DOMAINS),
});

export type ContractFormValues = z.infer<typeof contractFormSchema>;

export const emptyContractForm: ContractFormValues = {
  code: '',
  name: '',
  payerOrganizationId: '',
  sponsorOrganizationId: '',
  providerProfileId: '',
  domainCode: 'HEALTH',
};

export const versionFormSchema = z
  .object({
    validFrom: z.string().min(1, 'REQUIRED'),
    validTo: z.string(),
    currencyCode: z
      .string()
      .trim()
      .regex(/^[A-Z]{3}$/, 'FORMAT'),
    notes: z.string().trim().max(2000, 'MAX_LENGTH:2000'),
    copyFromVersionId: z.string(),
  })
  .superRefine((form, ctx) => {
    if (form.validTo !== '' && form.validTo <= form.validFrom) {
      ctx.addIssue({ code: 'custom', message: 'DATE_ORDER', path: ['validTo'] });
    }
  });

export type VersionFormValues = z.infer<typeof versionFormSchema>;

export const priceListRowSchema = z
  .object({
    code: z.string().trim().regex(CODE, 'PATTERN'),
    name: z.string().trim().min(1, 'REQUIRED').max(200, 'MAX_LENGTH:200'),
    priority: z.string().refine((v) => /^\d{1,6}$/.test(v), 'QUANTITY_INVALID'),
    seasonFrom: z.string(),
    seasonTo: z.string(),
    weekdayMask: z.array(z.boolean()).length(7),
  })
  .superRefine((row, ctx) => {
    // The database takes both season bounds or neither; asking for one is a half-stated rule.
    if ((row.seasonFrom === '') !== (row.seasonTo === '')) {
      ctx.addIssue({ code: 'custom', message: 'REQUIRED', path: ['seasonTo'] });
    }
    if (row.seasonFrom !== '' && row.seasonTo !== '' && row.seasonTo <= row.seasonFrom) {
      ctx.addIssue({ code: 'custom', message: 'DATE_ORDER', path: ['seasonTo'] });
    }
  });

export type PriceListRowValues = z.infer<typeof priceListRowSchema>;

export const priceListsFormSchema = z
  .object({ items: z.array(priceListRowSchema) })
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

export type PriceListsFormValues = z.infer<typeof priceListsFormSchema>;

export const emptyPriceListRow: PriceListRowValues = {
  code: '',
  name: '',
  priority: '100',
  seasonFrom: '',
  seasonTo: '',
  weekdayMask: [true, true, true, true, true, true, true],
};

export const priceItemRowSchema = z
  .object({
    scopeType: z.enum(SCOPE_TYPES),
    serviceDefinitionId: z.string(),
    serviceCategoryId: z.string(),
    packageDefinitionId: z.string(),
    locationId: z.string(),
    unitType: z.enum(UNIT_TYPES),
    pricingMethod: z.enum(PRICING_METHODS),
    amount: money,
    percent,
    formulaKey: z.string().trim(),
    minAmount: money,
    maxAmount: money,
    memberShareMethod: z.enum(SHARE_METHODS),
    memberShareAmount: money,
    memberSharePercent: percent,
    validFrom: z.string().min(1, 'REQUIRED'),
    validTo: z.string(),
    priority: z.string().refine((v) => /^\d{1,6}$/.test(v), 'QUANTITY_INVALID'),
  })
  .superRefine((row, ctx) => {
    // Each rule below is one the server enforces too. Mirroring them keeps a row that
    // cannot be priced out of the round trip, and out of the sheet an operator is reading.
    const target = {
      DEFINITION: row.serviceDefinitionId,
      CATEGORY: row.serviceCategoryId,
      PACKAGE: row.packageDefinitionId,
    }[row.scopeType];
    if (!target) {
      const path = {
        DEFINITION: 'serviceDefinitionId',
        CATEGORY: 'serviceCategoryId',
        PACKAGE: 'packageDefinitionId',
      }[row.scopeType];
      ctx.addIssue({ code: 'custom', message: 'REQUIRED', path: [path] });
    }
    if ((row.pricingMethod === 'FIXED' || row.pricingMethod === 'UNIT') && row.amount === '') {
      ctx.addIssue({ code: 'custom', message: 'REQUIRED', path: ['amount'] });
    }
    if (row.pricingMethod === 'PERCENT_OF_LIST' && row.percent === '') {
      ctx.addIssue({ code: 'custom', message: 'REQUIRED', path: ['percent'] });
    }
    if (row.pricingMethod === 'FORMULA' && row.formulaKey === '') {
      ctx.addIssue({ code: 'custom', message: 'REQUIRED', path: ['formulaKey'] });
    }
    if (row.memberShareMethod === 'FIXED' && row.memberShareAmount === '') {
      ctx.addIssue({ code: 'custom', message: 'REQUIRED', path: ['memberShareAmount'] });
    }
    if (row.memberShareMethod === 'PERCENT' && row.memberSharePercent === '') {
      ctx.addIssue({ code: 'custom', message: 'REQUIRED', path: ['memberSharePercent'] });
    }
    if (row.validTo !== '' && row.validTo <= row.validFrom) {
      ctx.addIssue({ code: 'custom', message: 'DATE_ORDER', path: ['validTo'] });
    }
  });

export type PriceItemRowValues = z.infer<typeof priceItemRowSchema>;

export const priceItemsFormSchema = z.object({ items: z.array(priceItemRowSchema) });
export type PriceItemsFormValues = z.infer<typeof priceItemsFormSchema>;

export const emptyPriceItemRow: PriceItemRowValues = {
  scopeType: 'DEFINITION',
  serviceDefinitionId: '',
  serviceCategoryId: '',
  packageDefinitionId: '',
  locationId: '',
  unitType: 'MONEY',
  pricingMethod: 'FIXED',
  amount: '',
  percent: '',
  formulaKey: '',
  minAmount: '',
  maxAmount: '',
  memberShareMethod: 'NONE',
  memberShareAmount: '',
  memberSharePercent: '',
  validFrom: '',
  validTo: '',
  priority: '100',
};

export const packageRowSchema = z.object({
  code: z.string().trim().regex(CODE, 'PATTERN'),
  name: z.string().trim().min(1, 'REQUIRED').max(200, 'MAX_LENGTH:200'),
  inclusionRule: z.enum(['ALL', 'ANY_OF_N']),
  minLines: z.string(),
  lines: z
    .array(
      z.object({
        serviceDefinitionId: z.string().uuid('REQUIRED'),
        includedQuantity: requiredMoney,
      }),
    )
    .min(1, 'REQUIRED'),
});

export const packagesFormSchema = z.object({ items: z.array(packageRowSchema) });
export type PackageRowValues = z.infer<typeof packageRowSchema>;
export type PackagesFormValues = z.infer<typeof packagesFormSchema>;

export const quotaRowSchema = z
  .object({
    locationId: z.string(),
    serviceDefinitionId: z.string(),
    periodType: z.enum(QUOTA_PERIODS),
    periodFrom: z.string().min(1, 'REQUIRED'),
    periodTo: z.string().min(1, 'REQUIRED'),
    capacity: requiredMoney,
    allowOverdraft: z.boolean(),
  })
  .superRefine((row, ctx) => {
    if (row.periodTo <= row.periodFrom) {
      ctx.addIssue({ code: 'custom', message: 'DATE_ORDER', path: ['periodTo'] });
    }
  });

export const quotasFormSchema = z.object({ items: z.array(quotaRowSchema) });
export type QuotaRowValues = z.infer<typeof quotaRowSchema>;
export type QuotasFormValues = z.infer<typeof quotasFormSchema>;

export const paymentTermSchema = z
  .object({
    dueDays: z.string().refine((v) => /^\d{1,3}$/.test(v), 'QUANTITY_INVALID'),
    settlementMethod: z.enum(SETTLEMENT_METHODS),
    taxBehaviour: z.enum(TAX_BEHAVIOURS),
    vatRate: percent,
    lateFeePercent: percent,
  })
  .superRefine((form, ctx) => {
    if (form.taxBehaviour !== 'EXEMPT' && form.vatRate === '') {
      ctx.addIssue({ code: 'custom', message: 'REQUIRED', path: ['vatRate'] });
    }
  });

export type PaymentTermValues = z.infer<typeof paymentTermSchema>;

export const retireFormSchema = z.object({
  reasonCode: z.string().trim().min(1, 'REQUIRED'),
  reasonText: z.string().trim().max(500, 'MAX_LENGTH:500'),
});

export type RetireFormValues = z.infer<typeof retireFormSchema>;
