import { z } from 'zod';

// The issue-code parser lives with the other problem helpers; every provider form needs
// its schema and its message parser together.
export { parseIssueMessage } from '../problems';

/** A location code is the provider's own stable key, so it is strict. */
const LOCATION_CODE = /^[A-Z0-9][A-Z0-9_-]{0,39}$/;

export const PROVIDER_TYPES = [
  'HOSPITAL',
  'CLINIC',
  'PHARMACY',
  'LABORATORY',
  'IMAGING',
  'HOTEL',
  'AGENCY',
  'TRANSPORT',
  'EDUCATION',
  'SPORT',
  'OTHER',
] as const;

export const PROVIDER_STATUSES = ['PENDING', 'ACTIVE', 'SUSPENDED', 'TERMINATED'] as const;
export const LOCATION_STATUSES = ['ACTIVE', 'SUSPENDED', 'CLOSED'] as const;
export const REGISTRATION_AUTHORITIES = ['TTB', 'SB', 'TDB', 'TEB', 'OTHER'] as const;
export const PRACTITIONER_STATUSES = ['ACTIVE', 'SUSPENDED', 'ENDED'] as const;
export const PRACTITIONER_ROLES = [
  'ATTENDING',
  'CONSULTANT',
  'TECHNICIAN',
  'ADMINISTRATIVE',
] as const;
/** A capability names either one service or a whole category, never both. */
export const CAPABILITY_TARGETS = ['DEFINITION', 'CATEGORY'] as const;

/**
 * The status moves only through the explicit commands, and only along the edges the
 * server accepts. TERMINATED is final, so it offers nothing.
 */
export const PROVIDER_TRANSITIONS: Record<string, readonly string[]> = {
  PENDING: ['ACTIVE', 'TERMINATED'],
  ACTIVE: ['SUSPENDED', 'TERMINATED'],
  SUSPENDED: ['ACTIVE', 'TERMINATED'],
  TERMINATED: [],
};

const dateOrEmpty = z.string();

function periodInOrder(form: { validFrom: string; validTo: string }, ctx: z.RefinementCtx): void {
  if (form.validFrom !== '' && form.validTo !== '' && form.validTo < form.validFrom) {
    ctx.addIssue({ code: 'custom', message: 'DATE_ORDER', path: ['validTo'] });
  }
}

/** The create form. `tenantOrganizationId` is the PROVIDER relationship, not the org. */
export const providerFormSchema = z
  .object({
    tenantOrganizationId: z.string().uuid('REQUIRED'),
    providerType: z.enum(PROVIDER_TYPES),
    networkTier: z.string().trim().max(20, 'MAX_LENGTH:20'),
    contractedFrom: dateOrEmpty,
    contractedTo: dateOrEmpty,
    notes: z.string().trim().max(2000, 'MAX_LENGTH:2000'),
  })
  .superRefine((form, ctx) => {
    if (
      form.contractedFrom !== '' &&
      form.contractedTo !== '' &&
      form.contractedTo < form.contractedFrom
    ) {
      ctx.addIssue({ code: 'custom', message: 'DATE_ORDER', path: ['contractedTo'] });
    }
  });

export type ProviderFormValues = z.infer<typeof providerFormSchema>;

export const emptyProviderForm: ProviderFormValues = {
  tenantOrganizationId: '',
  providerType: 'HOSPITAL',
  networkTier: '',
  contractedFrom: '',
  contractedTo: '',
  notes: '',
};

/** Suspend and terminate both ask why; activate does not. */
export const reasonFormSchema = z.object({
  reasonCode: z.string().trim().min(1, 'REQUIRED').max(60, 'MAX_LENGTH:60'),
  reasonText: z.string().trim().max(500, 'MAX_LENGTH:500'),
});

export type ReasonFormValues = z.infer<typeof reasonFormSchema>;

export const emptyReasonForm: ReasonFormValues = { reasonCode: '', reasonText: '' };

/**
 * One shape for both location dialogs. The create dialog offers the code and leaves the
 * status alone — a new location is born ACTIVE; the edit dialog shows the code read-only,
 * because the server answers IMMUTABLE to a code inside a merge-patch.
 */
export const locationFormSchema = z
  .object({
    code: z.string().trim().regex(LOCATION_CODE, 'PATTERN'),
    name: z.string().trim().min(2, 'MIN_LENGTH:2').max(200, 'MAX_LENGTH:200'),
    addressLine: z.string().trim().max(300, 'MAX_LENGTH:300'),
    district: z.string().trim().max(100, 'MAX_LENGTH:100'),
    city: z.string().trim().max(100, 'MAX_LENGTH:100'),
    postalCode: z.string().trim().max(20, 'MAX_LENGTH:20'),
    countryCode: z.string().trim().length(2, 'LENGTH'),
    phone: z.string().trim().max(40, 'MAX_LENGTH:40'),
    timezone: z.string().trim().min(1, 'REQUIRED').max(60, 'MAX_LENGTH:60'),
    // Coordinates are the one place a float is the right type. They are geography, not
    // money: numeric(9,6) is nine significant digits, which a double holds exactly, and
    // the sixth decimal is about ten centimetres. Empty means the location has no pin.
    latitude: z
      .string()
      .trim()
      .refine(
        (v) => v === '' || (/^-?\d{1,2}(\.\d{1,6})?$/.test(v) && Math.abs(Number(v)) <= 90),
        'FORMAT',
      ),
    longitude: z
      .string()
      .trim()
      .refine(
        (v) => v === '' || (/^-?\d{1,3}(\.\d{1,6})?$/.test(v) && Math.abs(Number(v)) <= 180),
        'FORMAT',
      ),
    status: z.enum(LOCATION_STATUSES),
  })
  .superRefine((form, ctx) => {
    // A half-given pin is not a location on a map; the database refuses one too.
    if ((form.latitude === '') !== (form.longitude === '')) {
      ctx.addIssue({ code: 'custom', message: 'REQUIRED', path: ['longitude'] });
    }
  });

export type LocationFormValues = z.infer<typeof locationFormSchema>;

export const emptyLocationForm: LocationFormValues = {
  code: '',
  name: '',
  addressLine: '',
  district: '',
  city: '',
  postalCode: '',
  countryCode: 'TR',
  phone: '',
  latitude: '',
  longitude: '',
  timezone: 'Europe/Istanbul',
  status: 'ACTIVE',
};

/** One row of the capability set. */
export const capabilityRowSchema = z
  .object({
    targetType: z.enum(CAPABILITY_TARGETS),
    serviceDefinitionId: z.string(),
    serviceCategoryId: z.string(),
    validFrom: z.string().min(1, 'REQUIRED'),
    validTo: dateOrEmpty,
    notes: z.string().trim().max(500, 'MAX_LENGTH:500'),
  })
  .superRefine((row, ctx) => {
    if (row.targetType === 'DEFINITION' && row.serviceDefinitionId === '') {
      ctx.addIssue({ code: 'custom', message: 'REQUIRED', path: ['serviceDefinitionId'] });
    }
    if (row.targetType === 'CATEGORY' && row.serviceCategoryId === '') {
      ctx.addIssue({ code: 'custom', message: 'REQUIRED', path: ['serviceCategoryId'] });
    }
    periodInOrder(row, ctx);
  });

export type CapabilityRowValues = z.infer<typeof capabilityRowSchema>;

export const capabilitiesFormSchema = z.object({
  items: z.array(capabilityRowSchema).max(200, 'MAX_ITEMS'),
});

export type CapabilitiesFormValues = z.infer<typeof capabilitiesFormSchema>;

export const emptyCapabilityRow: CapabilityRowValues = {
  targetType: 'DEFINITION',
  serviceDefinitionId: '',
  serviceCategoryId: '',
  validFrom: '',
  validTo: '',
  notes: '',
};

/**
 * A new practitioner. The registration number is entered here and nowhere else: after the
 * server stores it only the masked form comes back, and the edit form has no field for it.
 */
export const practitionerFormSchema = z
  .object({
    fullName: z.string().trim().min(2, 'MIN_LENGTH:2').max(200, 'MAX_LENGTH:200'),
    title: z.string().trim().max(60, 'MAX_LENGTH:60'),
    branchCode: z.string().trim().max(40, 'MAX_LENGTH:40'),
    registrationAuthority: z.enum(REGISTRATION_AUTHORITIES),
    registrationNumber: z.string().trim().min(1, 'REQUIRED').max(60, 'MAX_LENGTH:60'),
    validFrom: dateOrEmpty,
    validTo: dateOrEmpty,
  })
  .superRefine(periodInOrder);

export type PractitionerFormValues = z.infer<typeof practitionerFormSchema>;

export const emptyPractitionerForm: PractitionerFormValues = {
  fullName: '',
  title: '',
  branchCode: '',
  registrationAuthority: 'TTB',
  registrationNumber: '',
  validFrom: '',
  validTo: '',
};

/** The edit form: everything but the registration, which is never rewritten. */
export const practitionerEditSchema = z
  .object({
    fullName: z.string().trim().min(2, 'MIN_LENGTH:2').max(200, 'MAX_LENGTH:200'),
    title: z.string().trim().max(60, 'MAX_LENGTH:60'),
    branchCode: z.string().trim().max(40, 'MAX_LENGTH:40'),
    status: z.enum(PRACTITIONER_STATUSES),
    validFrom: dateOrEmpty,
    validTo: dateOrEmpty,
  })
  .superRefine(periodInOrder);

export type PractitionerEditValues = z.infer<typeof practitionerEditSchema>;

/** One row of the assignment set: where a practitioner works, in which role, when. */
export const assignmentRowSchema = z
  .object({
    locationId: z.string().min(1, 'REQUIRED'),
    role: z.enum(PRACTITIONER_ROLES),
    validFrom: z.string().min(1, 'REQUIRED'),
    validTo: dateOrEmpty,
  })
  .superRefine(periodInOrder);

export type AssignmentRowValues = z.infer<typeof assignmentRowSchema>;

export const assignmentsFormSchema = z.object({
  items: z.array(assignmentRowSchema).max(100, 'MAX_ITEMS'),
});

export type AssignmentsFormValues = z.infer<typeof assignmentsFormSchema>;

export const emptyAssignmentRow: AssignmentRowValues = {
  locationId: '',
  role: 'ATTENDING',
  validFrom: '',
  validTo: '',
};

/** The step-up guarded lookup. The number lives in the dialog's state and nowhere else. */
export const registrationSearchSchema = z.object({
  registrationAuthority: z.enum(REGISTRATION_AUTHORITIES),
  registrationNumber: z.string().trim().min(1, 'REQUIRED').max(60, 'MAX_LENGTH:60'),
});

export type RegistrationSearchValues = z.infer<typeof registrationSearchSchema>;
