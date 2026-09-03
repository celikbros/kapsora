import {
  validateIdentifier,
  type CreateOrganizationRequest,
  type IdentifierType,
} from '@kapsora/api-client';
import { z } from 'zod';

/** Enumerations from the contract; kept as tuples so zod and the selects share them. */
export const ORGANIZATION_KINDS = [
  'BANK',
  'INSURER',
  'SPONSOR',
  'PROVIDER',
  'VENDOR',
  'PUBLIC_BODY',
  'OTHER',
] as const;
export const RELATIONSHIP_ROLES = ['PAYER', 'SPONSOR', 'PROVIDER', 'VENDOR', 'PARTNER'] as const;
export const RELATIONSHIP_STATUSES = ['ACTIVE', 'SUSPENDED', 'TERMINATED'] as const;
export const IDENTIFIER_TYPES = ['VKN', 'TCKN', 'MERSIS', 'PROVIDER_REGISTRY', 'OTHER'] as const;

const tenantCodePattern = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$/;

/**
 * Client-side validation mirrors the server rules (instant feedback); the server stays
 * the authority and its 422 field errors are mapped back onto the same paths.
 * Error messages are i18n codes resolved by the form (fieldErrors.*).
 */
export const identifierSchema = z
  .object({
    type: z.enum(IDENTIFIER_TYPES),
    value: z.string().trim(),
    primary: z.boolean(),
  })
  .superRefine((id, ctx) => {
    const code = validateIdentifier(id.type as IdentifierType, id.value);
    if (code) ctx.addIssue({ code: 'custom', message: code, path: ['value'] });
  });

export const organizationFormSchema = z
  .object({
    legalName: z.string().trim().min(2, 'MIN_LENGTH:2').max(300, 'MAX_LENGTH:300'),
    displayName: z.string().trim().min(2, 'MIN_LENGTH:2').max(200, 'MAX_LENGTH:200'),
    organizationKind: z.enum(ORGANIZATION_KINDS, { message: 'ENUM' }),
    relationshipRole: z.enum(RELATIONSHIP_ROLES, { message: 'ENUM' }),
    countryCode: z
      .string()
      .trim()
      .toUpperCase()
      .regex(/^[A-Z]{2}$/, 'FORMAT'),
    tenantCode: z
      .string()
      .trim()
      .refine((v) => v === '' || tenantCodePattern.test(v), 'PATTERN'),
    identifiers: z.array(identifierSchema).min(1, 'REQUIRED').max(10, 'MAX_LENGTH:10'),
  })
  .superRefine((form, ctx) => {
    const tax = form.identifiers.filter((i) => i.type === 'VKN' || i.type === 'TCKN').length;
    if (tax > 1)
      ctx.addIssue({ code: 'custom', message: 'IDENTIFIER_CONFLICT', path: ['identifiers'] });
    else if (tax === 0 && form.countryCode === 'TR')
      ctx.addIssue({ code: 'custom', message: 'IDENTIFIER_REQUIRED', path: ['identifiers'] });
  });

export type OrganizationFormValues = z.infer<typeof organizationFormSchema>;

export const emptyOrganizationForm: OrganizationFormValues = {
  legalName: '',
  displayName: '',
  organizationKind: 'PROVIDER',
  relationshipRole: 'PROVIDER',
  countryCode: 'TR',
  tenantCode: '',
  identifiers: [{ type: 'VKN', value: '', primary: true }],
};

/** Form values → create request body. */
export function toCreateRequest(values: OrganizationFormValues): CreateOrganizationRequest {
  const body: CreateOrganizationRequest = {
    legalName: values.legalName,
    displayName: values.displayName,
    organizationKind: values.organizationKind,
    relationshipRole: values.relationshipRole,
    countryCode: values.countryCode,
    identifiers: values.identifiers.map((i) => ({
      type: i.type,
      value: i.value,
      primary: i.primary,
    })),
  };
  if (values.tenantCode !== '') body.tenantCode = values.tenantCode;
  return body;
}

/** "MIN_LENGTH:2" → { code: 'MIN_LENGTH', params: { min: 2 } } for the i18n lookup. */
export function parseIssueMessage(message: string): {
  code: string;
  params: Record<string, unknown>;
} {
  const [code, arg] = message.split(':');
  if (!code) return { code: 'FORMAT', params: {} };
  if (code === 'MIN_LENGTH') return { code, params: { min: Number(arg) } };
  if (code === 'MAX_LENGTH') return { code, params: { max: Number(arg) } };
  return { code, params: {} };
}

/** Edit form: the three patchable fields. */
export const organizationEditSchema = z.object({
  displayName: z.string().trim().min(2, 'MIN_LENGTH:2').max(200, 'MAX_LENGTH:200'),
  relationshipStatus: z.enum(RELATIONSHIP_STATUSES, { message: 'ENUM' }),
  tenantCode: z
    .string()
    .trim()
    .refine((v) => v === '' || tenantCodePattern.test(v), 'PATTERN'),
});

export type OrganizationEditValues = z.infer<typeof organizationEditSchema>;
