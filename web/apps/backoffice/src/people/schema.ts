import { validateIdentifier, type IdentifierType } from '@kapsora/api-client';
import { z } from 'zod';

/** Identifier types a person form offers; the tenant catalog narrows this at runtime. */
export const IDENTIFIER_TYPES = ['TCKN', 'PASSPORT', 'MEMBER_NO', 'CUSTOMER_NO'] as const;
export const SEXES = ['FEMALE', 'MALE', 'INTERSEX', 'UNKNOWN'] as const;
export const PERSON_STATUSES = ['ACTIVE', 'INACTIVE', 'DECEASED'] as const;

/** Earliest birth date the server accepts. */
const MIN_BIRTH_DATE = '1900-01-01';

const identifierSchema = z
  .object({
    type: z.string().min(1, 'REQUIRED'),
    value: z.string().trim(),
    primary: z.boolean(),
  })
  .superRefine((identifier, ctx) => {
    const code = validateIdentifier(identifier.type as IdentifierType, identifier.value);
    if (code) ctx.addIssue({ code: 'custom', message: code, path: ['value'] });
  });

/**
 * The create form. Client validation mirrors the server so a wrong check digit is caught
 * before the round trip; the server stays the authority and its field errors win.
 */
export const personFormSchema = z
  .object({
    firstName: z.string().trim().min(1, 'REQUIRED').max(100, 'MAX_LENGTH:100'),
    middleName: z.string().trim().max(100, 'MAX_LENGTH:100'),
    lastName: z.string().trim().min(1, 'REQUIRED').max(100, 'MAX_LENGTH:100'),
    birthDate: z
      .string()
      .refine((value) => value === '' || value >= MIN_BIRTH_DATE, 'DATE_RANGE')
      .refine(
        (value) => value === '' || value <= new Date().toISOString().slice(0, 10),
        'DATE_RANGE',
      ),
    sexAtBirth: z.enum(['', ...SEXES]),
    identifiers: z.array(identifierSchema).max(10, 'MAX_LENGTH:10'),
  })
  .superRefine((form, ctx) => {
    const seen = new Set<string>();
    form.identifiers.forEach((identifier, index) => {
      if (seen.has(identifier.type)) {
        ctx.addIssue({
          code: 'custom',
          message: 'IDENTIFIER_DUPLICATE',
          path: ['identifiers', index, 'type'],
        });
      }
      seen.add(identifier.type);
    });
  });

export type PersonFormValues = z.infer<typeof personFormSchema>;

export const emptyPersonForm: PersonFormValues = {
  firstName: '',
  middleName: '',
  lastName: '',
  birthDate: '',
  sexAtBirth: '',
  identifiers: [{ type: 'TCKN', value: '', primary: true }],
};

/** The edit form: the fields a merge-patch may carry. */
export const personEditSchema = z.object({
  firstName: z.string().trim().min(1, 'REQUIRED').max(100, 'MAX_LENGTH:100'),
  middleName: z.string().trim().max(100, 'MAX_LENGTH:100'),
  lastName: z.string().trim().min(1, 'REQUIRED').max(100, 'MAX_LENGTH:100'),
  birthDate: z.string(),
  sexAtBirth: z.enum(['', ...SEXES]),
  status: z.enum(PERSON_STATUSES),
});

export type PersonEditValues = z.infer<typeof personEditSchema>;

/** A relationship the operator adds on the family tab. */
export const relationshipFormSchema = z.object({
  targetPersonId: z.string().uuid('REQUIRED'),
  relationshipType: z.string().min(1, 'REQUIRED'),
  validFrom: z.string().min(1, 'REQUIRED'),
  validTo: z.string(),
});

export type RelationshipFormValues = z.infer<typeof relationshipFormSchema>;

/** A sponsor membership the operator adds on the memberships tab. */
export const membershipFormSchema = z.object({
  sponsorOrganizationId: z.string().uuid('REQUIRED'),
  membershipType: z.string().min(1, 'REQUIRED'),
  principalMembershipId: z.string(),
  externalMemberNo: z.string().trim().max(80, 'MAX_LENGTH:80'),
  validFrom: z.string().min(1, 'REQUIRED'),
  validTo: z.string(),
});

export type MembershipFormValues = z.infer<typeof membershipFormSchema>;

/** "MAX_LENGTH:100" → { code, params } for the i18n lookup. */
export function parseIssueMessage(message: string): {
  code: string;
  params: Record<string, unknown>;
} {
  const [code, argument] = message.split(':');
  if (!code) return { code: 'FORMAT', params: {} };
  if (code === 'MIN_LENGTH') return { code, params: { min: Number(argument) } };
  if (code === 'MAX_LENGTH') return { code, params: { max: Number(argument) } };
  return { code, params: {} };
}
