/**
 * Synthetic Turkish data for the mock API. Deterministic (seeded) so screenshots,
 * Playwright runs and reviews see the same rows. No real names, tax numbers are random
 * with a valid checksum, nothing here refers to a real person or company.
 */
import type { components } from '../generated/kapsora-v1';
import type {
  EligibilityCheckRequest as DecimalEligibilityRequest,
  EligibilityCheckResult as DecimalEligibilityResult,
} from '../decimals';
import {
  isValidTCKN,
  maskIdentifier,
  normalizeDigits,
  randomTCKN,
  randomVKN,
} from '../identifiers';

type Schemas = components['schemas'];
export type MockTenant = Schemas['TenantSummary'];

/** Linear congruential generator: small, deterministic, good enough for fixtures. */
export function seededRandom(seed: number): () => number {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 2 ** 32;
  };
}

/** UUIDv7-like ids: time-ordered prefix, random tail, from the seeded generator. */
export function makeIdFactory(
  random: () => number,
  startMillis: number,
): (offsetMs?: number) => string {
  let counter = 0;
  return (offsetMs = 0) => {
    counter += 1;
    const ms = startMillis + offsetMs + counter;
    const hex = ms.toString(16).padStart(12, '0');
    const tail = Array.from({ length: 18 }, () => Math.floor(random() * 16).toString(16)).join('');
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-7${tail.slice(0, 3)}-${(8 + Math.floor(random() * 4)).toString(16)}${tail.slice(3, 6)}-${tail.slice(6, 18)}`;
  };
}

export interface MockAccount {
  actorId: string;
  username: string;
  displayName: string;
  email: string;
  /** Tenant codes the account is a member of, with permissions per tenant. */
  memberships: { tenantCode: string; permissions: string[] }[];
}

export interface StoredOrganization {
  /** Global legal entity id (directory.organization). */
  organizationId: string;
  legalName: string;
  displayName: string;
  organizationKind: Schemas['Organization']['organizationKind'];
  countryCode: string;
  organizationStatus: Schemas['Organization']['organizationStatus'];
  /** Plain tax number, kept only inside the mock to emulate dedup; never returned. */
  taxNumber?: { type: 'VKN' | 'TCKN'; value: string };
  otherIdentifiers: {
    type: 'MERSIS' | 'PROVIDER_REGISTRY' | 'OTHER';
    value: string;
    primary: boolean;
  }[];
}

export interface StoredRelationship {
  id: string;
  tenantId: string;
  organizationId: string;
  relationshipRole: Schemas['Organization']['relationshipRole'];
  relationshipStatus: Schemas['Organization']['relationshipStatus'];
  tenantCode: string | null;
  validFrom: string;
  validTo: string | null;
  createdAt: string;
  rowVersion: number;
}

export interface StoredPerson {
  id: string;
  tenantId: string;
  firstName: string;
  middleName: string | null;
  lastName: string;
  birthDate: string | null;
  sexAtBirth: 'FEMALE' | 'MALE' | 'INTERSEX' | 'UNKNOWN' | null;
  status: Schemas['PersonSummary']['status'];
  identifiers: { type: string; value: string; primary: boolean }[];
  rowVersion: number;
  createdAt: string;
}

export type StoredServiceRequest = Schemas['ServiceRequest'] & { tenantId: string };

/** Decimal quantity carried as a string on the wire (never a JS number, see entitlements.ts). */
export type Decimal = string;

/** Formats a JS number as the fixed 6-decimal string the mock uses for every balance. */
export function toDecimal(n: number): Decimal {
  return n.toFixed(6);
}

/** Adds two decimal-string quantities without floating point drift for the fixture data. */
export function addDecimal(a: Decimal, b: Decimal): Decimal {
  return toDecimal(Number(a) + Number(b));
}

// --- exact decimal arithmetic ---------------------------------------------------------
// Money and quantities in M3 are numeric(20,6) decimal strings end to end. Every sum,
// product and comparison below runs on integer micro-units held in BigInt, so no amount
// ever passes through a binary float: `Number('0.1') + Number('0.2')` is exactly the class
// of error a tariff row must never contain.

const MICROS = 1_000_000n;
const DECIMAL_TEXT = /^(-?)(\d{1,20})(?:\.(\d{1,6}))?$/;

/** Parses an exact decimal string into integer micro-units; anything unparsable is zero. */
export function toMicros(value: Decimal | null | undefined): bigint {
  const m = DECIMAL_TEXT.exec((value ?? '').trim());
  if (!m) return 0n;
  const sign = m[1] === '-' ? -1n : 1n;
  const fraction = (m[3] ?? '').padEnd(6, '0');
  return sign * (BigInt(m[2]!) * MICROS + BigInt(fraction));
}

/** Renders integer micro-units back as the canonical six-decimal string. */
export function fromMicros(micros: bigint): Decimal {
  const negative = micros < 0n;
  const abs = negative ? -micros : micros;
  const whole = abs / MICROS;
  const fraction = (abs % MICROS).toString().padStart(6, '0');
  return `${negative ? '-' : ''}${whole}.${fraction}`;
}

/** Multiplies two micro-unit values, rounding the product half away from zero. */
export function multiplyMicros(a: bigint, b: bigint): bigint {
  const product = a * b;
  const negative = product < 0n;
  const abs = negative ? -product : product;
  const rounded = (abs + MICROS / 2n) / MICROS;
  return negative ? -rounded : rounded;
}

/** `amount` × `percent`/100, in micro-units; used for member shares and percent prices. */
export function percentOfMicros(amount: bigint, percent: bigint): bigint {
  return multiplyMicros(amount, percent) / 100n;
}

/** Orders two decimal strings exactly: -1, 0 or 1. */
export function compareDecimal(a: Decimal, b: Decimal): number {
  const left = toMicros(a);
  const right = toMicros(b);
  return left < right ? -1 : left > right ? 1 : 0;
}

/** True when the text is a decimal the mock can compare exactly. */
export function isDecimalText(value: string): boolean {
  return DECIMAL_TEXT.test(value.trim());
}

/** Small, deterministic hex digest so `configurationHash` looks like a real sha256. */
export function pseudoHash(seed: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < seed.length; i++) {
    h ^= seed.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  const hex = (h >>> 0).toString(16).padStart(8, '0');
  return hex.repeat(8).slice(0, 64);
}

export interface StoredPersonRelationship {
  id: string;
  tenantId: string;
  sourcePersonId: string;
  targetPersonId: string;
  relationshipType: string;
  status: 'ACTIVE' | 'SUSPENDED' | 'ENDED';
  validFrom: string;
  validTo: string | null;
  endReasonCode: string | null;
  rowVersion: number;
}

export interface StoredMembership {
  id: string;
  tenantId: string;
  personId: string;
  /** Tenant organization relationship id (directory.tenant_organization) of the sponsor/payer. */
  sponsorOrganizationId: string;
  membershipType: string;
  principalMembershipId: string | null;
  externalMemberNo: string | null;
  status: 'PENDING' | 'ACTIVE' | 'SUSPENDED' | 'ENDED';
  validFrom: string;
  validTo: string | null;
  sourceSystem: string | null;
  rowVersion: number;
}

export interface StoredProgram {
  id: string;
  tenantId: string;
  code: string;
  name: string;
  programType: string;
  sponsorOrganizationId: string;
  payerOrganizationId: string;
  status: 'DRAFT' | 'ACTIVE' | 'SUSPENDED' | 'CLOSED';
  validFrom: string | null;
  validTo: string | null;
  rowVersion: number;
}

export interface StoredPlan {
  id: string;
  tenantId: string;
  programId: string;
  code: string;
  name: string;
  status: 'DRAFT' | 'ACTIVE' | 'RETIRED';
  rowVersion: number;
}

export interface StoredEntitlementDefinition {
  id: string;
  code: string;
  name: string;
  unitType: 'MONEY' | 'COUNT' | 'NIGHT' | 'SESSION' | 'HOUR' | 'KILOMETER' | 'POINT';
  currencyCode: string | null;
  familyShared: boolean;
  allowOverdraft: boolean;
  initialQuantity: number;
  periodType: 'CALENDAR_YEAR' | 'PLAN_YEAR' | 'ROLLING_DAYS' | 'LIFETIME' | 'CUSTOM';
  periodLength: number | null;
  rolloverPolicy: 'NONE' | 'FULL' | 'CAPPED';
  rolloverCap: number | null;
  status: 'ACTIVE' | 'INACTIVE';
}

export interface StoredPlanVersion {
  id: string;
  tenantId: string;
  planId: string;
  versionNo: number;
  status: 'DRAFT' | 'UNDER_REVIEW' | 'PUBLISHED' | 'RETIRED';
  validFrom: string | null;
  validTo: string | null;
  notes: string | null;
  definitions: StoredEntitlementDefinition[];
  configurationHash: string | null;
  publishedAt: string | null;
  publishedBy: string | null;
  submittedAt: string | null;
  submittedBy: string | null;
  retireReasonCode: string | null;
  reviewComment: string | null;
  rowVersion: number;
}

export interface StoredEnrollment {
  id: string;
  tenantId: string;
  personId: string;
  planId: string;
  planCode: string;
  programId: string;
  sponsorMembershipId: string;
  status: 'PENDING' | 'ACTIVE' | 'SUSPENDED' | 'ENDED';
  validFrom: string;
  validTo: string | null;
  enrollmentReason: string | null;
  sourceSystem: string | null;
  rowVersion: number;
}

/** Embedded definition summary of an entitlement account, matching the response shape. */
export interface StoredAccountDefinition {
  id: string;
  code: string;
  name: string;
  unitType: StoredEntitlementDefinition['unitType'];
  currencyCode: string | null;
  familyShared: boolean;
  allowOverdraft: boolean;
}

/**
 * An entitlement account. Balances are kept as decimal strings (6 fraction digits) the
 * whole way through the mock, exactly like the Go API's `decimal.Decimal` JSON encoding;
 * see entitlements.ts for why these must never become JS numbers.
 */
export interface StoredEntitlementAccount {
  id: string;
  tenantId: string;
  enrollmentId: string;
  /** Owner of the enrollment; for family-shared accounts, the principal. */
  personId: string;
  definition: StoredAccountDefinition;
  benefitPeriodFrom: string;
  benefitPeriodTo: string | null;
  totalGranted: Decimal;
  available: Decimal;
  consumed: Decimal;
  reserved: Decimal;
  expired: Decimal;
  status: 'OPEN' | 'FROZEN' | 'CLOSED';
  rowVersion: number;
}

export interface StoredLedgerEntry {
  id: string;
  tenantId: string;
  accountId: string;
  effectiveAt: string;
  movementType: 'GRANT' | 'RESERVE' | 'RELEASE' | 'CONSUME' | 'REVERSE' | 'EXPIRE' | 'ADJUST';
  deltaAvailable: Decimal;
  deltaConsumed: Decimal;
  deltaExpired: Decimal;
  deltaReserved: Decimal;
  deltaTotal: Decimal;
  referenceType: string;
  referenceId: string;
  reservationId: string | null;
  reasonCode: string | null;
  reasonText: string | null;
  createdBy: string | null;
}

export interface StoredAdjustment {
  id: string;
  tenantId: string;
  accountId: string;
  deltaQuantity: Decimal;
  reasonCode: string;
  reasonText: string | null;
  status: 'PENDING' | 'APPROVED' | 'REJECTED';
  requestedBy: string;
  requestedAt: string;
  decidedBy: string | null;
  decidedAt: string | null;
  decisionComment: string | null;
  ledgerEntryId: string | null;
  rowVersion: number;
}

export interface StoredEvaluation {
  id: string;
  /** Set when the caller passed an Idempotency-Key, so a repeat replays this row. */
  idempotencyKey?: string;
  tenantId: string;
  personId: string;
  programId: string | null;
  planVersionId: string | null;
  enrollmentId: string | null;
  serviceDate: string;
  evaluatedAt: string;
  evaluatedBy: string | null;
  outcome: Schemas['EligibilityCheckResult']['outcome'];
  // Quantities are decimal strings on the wire; see ../decimals.ts.
  request: DecimalEligibilityRequest;
  result: DecimalEligibilityResult;
}

export interface StoredImportBatch {
  id: string;
  tenantId: string;
  sponsorOrganizationId: string;
  sourceSystem: string;
  sourceVersion: string;
  fileName: string;
  fileSha256: string;
  format: 'CSV_V1';
  planId: string | null;
  status:
    | 'RECEIVED'
    | 'VALIDATING'
    | 'REVIEW'
    | 'READY'
    | 'APPLYING'
    | 'APPLIED'
    | 'FAILED'
    | 'CANCELLED';
  rowCount: number;
  counters: {
    conflict: number;
    created: number;
    invalid: number;
    matched: number;
    skipped: number;
    updated: number;
    valid: number;
  };
  errorSummary: string | null;
  createdAt: string;
  appliedAt: string | null;
  rowVersion: number;
}

export interface StoredImportRow {
  id: string;
  tenantId: string;
  importId: string;
  rowNo: number;
  sourceRecordId: string;
  displayName: string;
  birthDate: string | null;
  identifiers: { type: string; value: string; primary: boolean }[];
  membershipType: string | null;
  planCode: string | null;
  principalSourceRecordId: string | null;
  status: 'PENDING' | 'VALID' | 'INVALID' | 'MATCHED' | 'CONFLICT' | 'APPLIED' | 'SKIPPED';
  errors: { code: string; field: string; message?: string }[];
  candidatePersonIds: string[];
  matchedPersonId: string | null;
  decision: 'CREATE' | 'UPDATE' | 'SKIP' | null;
  appliedPersonId: string | null;
  rowVersion: number;
}

// --- M3: catalog, providers, contracts, rules and pricing -----------------------------

export interface StoredServiceCategory {
  id: string;
  tenantId: string;
  parentId: string | null;
  code: string;
  name: string;
  domain: Schemas['ServiceDomain'];
  active: boolean;
  rowVersion: number;
}

export interface StoredServiceDefinition {
  id: string;
  tenantId: string;
  categoryId: string;
  code: string;
  name: string;
  description: string | null;
  fulfillmentMode: Schemas['FulfillmentMode'];
  defaultUnitType: Schemas['ServiceUnitType'];
  requiresProvider: boolean;
  active: boolean;
  rowVersion: number;
}

export interface StoredCodeSystem {
  id: string;
  tenantId: string;
  code: string;
  name: string;
  version: string;
  authority: Schemas['CodeSystemAuthority'];
  licensed: boolean;
  status: 'ACTIVE' | 'INACTIVE';
  validFrom: string;
  validTo: string | null;
  rowVersion: number;
}

export interface StoredCodeValue {
  id: string;
  tenantId: string;
  codeSystemId: string;
  code: string;
  display: string;
  parentCode: string | null;
  validFrom: string;
  validTo: string | null;
  active: boolean;
  attributes: Record<string, unknown>;
}

export interface StoredCodeMapping {
  id: string;
  tenantId: string;
  serviceDefinitionId: string;
  codeSystemId: string;
  code: string;
  validFrom: string;
  validTo: string | null;
  primary: boolean;
}

export interface StoredProvider {
  id: string;
  tenantId: string;
  /** The tenant's organization relationship carrying the PROVIDER role. */
  tenantOrganizationId: string;
  providerType: Schemas['ProviderType'];
  status: Schemas['ProviderStatus'];
  networkTier: string | null;
  contractedFrom: string | null;
  contractedTo: string | null;
  notes: string | null;
  rowVersion: number;
}

export interface StoredProviderLocation {
  id: string;
  tenantId: string;
  providerId: string;
  code: string;
  name: string;
  addressLine: string | null;
  district: string | null;
  city: string | null;
  countryCode: string;
  postalCode: string | null;
  latitude: number | null;
  longitude: number | null;
  timezone: string;
  phone: string | null;
  status: Schemas['ProviderLocationStatus'];
  rowVersion: number;
}

export interface StoredProviderCapability {
  id: string;
  tenantId: string;
  locationId: string;
  serviceDefinitionId: string | null;
  serviceCategoryId: string | null;
  validFrom: string;
  validTo: string | null;
  notes: string | null;
}

export interface StoredPractitionerLocation {
  id: string;
  practitionerId: string;
  locationId: string;
  role: Schemas['PractitionerRole'];
  validFrom: string;
  validTo: string | null;
}

export interface StoredPractitioner {
  id: string;
  tenantId: string;
  providerId: string;
  personId: string | null;
  fullName: string;
  title: string | null;
  branchCode: string | null;
  registrationAuthority: Schemas['RegistrationAuthority'];
  /**
   * Plain registration number, kept only inside the mock so the blind-index search can be
   * emulated. It is never returned, never logged and never put in a URL: responses carry
   * `maskedRegistrationNumber` only.
   */
  registrationNumber: string;
  validFrom: string | null;
  validTo: string | null;
  status: Schemas['PractitionerStatus'];
  locations: StoredPractitionerLocation[];
  rowVersion: number;
}

export interface StoredContract {
  id: string;
  tenantId: string;
  code: string;
  name: string;
  payerOrganizationId: string;
  providerProfileId: string;
  sponsorOrganizationId: string | null;
  domainCode: Schemas['ServiceDomain'];
  status: Schemas['ContractStatus'];
  rowVersion: number;
}

export interface StoredContractVersion {
  id: string;
  tenantId: string;
  contractId: string;
  versionNo: number;
  status: Schemas['ContractVersionStatus'];
  validFrom: string | null;
  validTo: string | null;
  currencyCode: string;
  notes: string | null;
  configurationHash: string | null;
  submittedAt: string | null;
  submittedBy: string | null;
  publishedAt: string | null;
  publishedBy: string | null;
  reviewComment: string | null;
  retireReasonCode: string | null;
  rowVersion: number;
}

export interface StoredPriceList {
  id: string;
  tenantId: string;
  contractVersionId: string;
  code: string;
  name: string;
  priority: number;
  seasonFrom: string | null;
  seasonTo: string | null;
  /** Bit 1 = Monday … bit 64 = Sunday; null means every day. */
  weekdayMask: number | null;
  rowVersion: number;
}

/** One tariff row. Every amount is an exact decimal string, never a JS number. */
export interface StoredPriceItem {
  id: string;
  tenantId: string;
  priceListId: string;
  serviceDefinitionId: string | null;
  serviceCategoryId: string | null;
  packageDefinitionId: string | null;
  locationId: string | null;
  unitType: Schemas['ServiceUnitType'];
  pricingMethod: Schemas['PricingMethod'];
  amount: Decimal | null;
  percent: Decimal | null;
  formulaKey: string | null;
  minAmount: Decimal | null;
  maxAmount: Decimal | null;
  memberShareMethod: Schemas['MemberShareMethod'];
  memberShareAmount: Decimal | null;
  memberSharePercent: Decimal | null;
  validFrom: string;
  validTo: string | null;
  priority: number;
}

export interface StoredPackageLine {
  serviceDefinitionId: string;
  includedQuantity: Decimal;
}

export interface StoredPackageDefinition {
  id: string;
  tenantId: string;
  contractVersionId: string;
  code: string;
  name: string;
  inclusionRule: Schemas['PackageInclusionRule'];
  minLines: number | null;
  lines: StoredPackageLine[];
}

export interface StoredProviderQuota {
  id: string;
  tenantId: string;
  contractVersionId: string;
  locationId: string | null;
  serviceDefinitionId: string | null;
  periodType: Schemas['QuotaPeriodType'];
  periodFrom: string;
  periodTo: string;
  capacity: Decimal;
  consumed: Decimal;
  allowOverdraft: boolean;
}

export interface StoredPaymentTerm {
  id: string;
  tenantId: string;
  contractVersionId: string;
  dueDays: number;
  settlementMethod: Schemas['SettlementMethod'];
  taxBehaviour: Schemas['TaxBehaviour'];
  vatRate: Decimal | null;
  lateFeePercent: Decimal | null;
  rowVersion: number;
}

export interface StoredRuleSet {
  id: string;
  tenantId: string;
  code: string;
  name: string;
  domainCode: Schemas['ServiceDomain'];
  purpose: Schemas['RuleSetPurpose'];
  status: Schemas['RuleSetStatus'];
  rowVersion: number;
}

export interface StoredRule {
  id: string;
  code: string;
  name: string;
  priority: number;
  condition: string;
  actions: Schemas['RuleAction'][];
  explanationCode: string;
  explanationParams: Record<string, unknown> | null;
  stopOnMatch: boolean;
  active: boolean;
}

export interface StoredRuleTestCase {
  id: string;
  code: string;
  description: string | null;
  input: Record<string, unknown>;
  expectedOutcome: Schemas['RuleOutcome'];
  expectedExplanations: string[];
  expectedActions: Schemas['RuleAction'][] | null;
}

export interface StoredRuleSetVersion {
  id: string;
  tenantId: string;
  ruleSetId: string;
  versionNo: number;
  status: Schemas['RuleSetVersionStatus'];
  validFrom: string | null;
  validTo: string | null;
  inputSchema: Record<string, Schemas['RuleInputType']>;
  notes: string | null;
  contentHash: string | null;
  submittedAt: string | null;
  submittedBy: string | null;
  publishedAt: string | null;
  publishedBy: string | null;
  reviewComment: string | null;
  retireReasonCode: string | null;
  rules: StoredRule[];
  testCases: StoredRuleTestCase[];
  rowVersion: number;
}

/** An append-only recorded decision. Simulations and test runs never produce one. */
export type StoredRuleEvaluation = Schemas['RuleEvaluation'] & { tenantId: string };

/** A stored quote. It reserves nothing: no account and no ledger row is ever touched. */
export type StoredPriceQuote = Schemas['PriceQuote'] & { tenantId: string };

/** Reference catalogs; tenant-independent so every tenant sees the same options. */
export const IDENTIFIER_TYPE_CATALOG: Schemas['PartyCatalogEntry'][] = [
  {
    code: 'TCKN',
    displayName: 'T.C. Kimlik No',
    status: 'ACTIVE',
    isSensitive: true,
    uniquenessScope: 'TENANT',
  },
  {
    code: 'PASSPORT',
    displayName: 'Pasaport No',
    status: 'ACTIVE',
    isSensitive: true,
    uniquenessScope: 'TENANT',
  },
  {
    code: 'MEMBER_NO',
    displayName: 'Üye No',
    status: 'ACTIVE',
    isSensitive: false,
    uniquenessScope: 'SPONSOR',
  },
  {
    code: 'CUSTOMER_NO',
    displayName: 'Müşteri No',
    status: 'ACTIVE',
    isSensitive: false,
    uniquenessScope: 'SPONSOR',
  },
];
export const RELATIONSHIP_TYPE_CATALOG: Schemas['PartyCatalogEntry'][] = [
  { code: 'SPOUSE', displayName: 'Eş', status: 'ACTIVE', isDirectional: false },
  { code: 'CHILD', displayName: 'Çocuk', status: 'ACTIVE', isDirectional: true },
  { code: 'PARENT', displayName: 'Ebeveyn', status: 'ACTIVE', isDirectional: true },
  {
    code: 'DEPENDENT',
    displayName: 'Bakmakla Yükümlü Kişi',
    status: 'ACTIVE',
    isDirectional: true,
  },
  { code: 'GUARDIAN', displayName: 'Vasi', status: 'ACTIVE', isDirectional: true },
  { code: 'DELEGATE', displayName: 'Vekil', status: 'ACTIVE', isDirectional: true },
];
export const MEMBERSHIP_TYPE_CATALOG: Schemas['PartyCatalogEntry'][] = [
  { code: 'EMPLOYEE', displayName: 'Çalışan', status: 'ACTIVE', requiresPrincipal: false },
  { code: 'RETIREE', displayName: 'Emekli', status: 'ACTIVE', requiresPrincipal: false },
  { code: 'MEMBER', displayName: 'Üye', status: 'ACTIVE', requiresPrincipal: false },
  { code: 'CUSTOMER', displayName: 'Müşteri', status: 'ACTIVE', requiresPrincipal: false },
  { code: 'INSURED', displayName: 'Sigortalı', status: 'ACTIVE', requiresPrincipal: false },
  { code: 'STUDENT', displayName: 'Öğrenci', status: 'ACTIVE', requiresPrincipal: false },
  { code: 'BENEFICIARY', displayName: 'Hak Sahibi', status: 'ACTIVE', requiresPrincipal: false },
  { code: 'FAMILY', displayName: 'Aile Bireyi', status: 'ACTIVE', requiresPrincipal: true },
];

// The permission codes are the ones migration 000008 seeds, so a screen that hides a
// control on the mock hides it against the real API too.
const ADMIN_PERMISSIONS = [
  'organization.read',
  'organization.manage',
  'member.read',
  'member.identifier.read',
  'member.identifier.search',
  'member.manage',
  'member.relationship.manage',
  'membership.manage',
  'enrollment.manage',
  'eligibility.check',
  'program.read',
  'program.manage',
  'plan.manage',
  'plan.publish',
  'entitlement.read',
  'entitlement.adjust',
  'service_request.read',
  'service_request.manage',
  'catalog.read',
  'catalog.manage',
  'provider.read',
  'provider.manage',
  'provider.practitioner.manage',
  'contract.read',
  'contract.manage',
  'contract.publish',
  'rule.read',
  'rule.draft',
  'rule.publish',
  'pricing.quote',
  'import.execute',
  'identity.user.read',
  'identity.user.manage',
  'identity.role.manage',
  'audit.read',
  'report.read',
  'integration.manage',
];
const REVIEWER_PERMISSIONS = [
  'organization.read',
  'member.read',
  'program.read',
  'entitlement.read',
  'service_request.read',
  'service_request.review',
  // Read-only M3 grants: enough to see the agreed prices and rules, never the drafts.
  'catalog.read',
  'provider.read',
  'contract.read',
  'rule.read',
];

const ORG_PREFIXES = [
  'Anadolu',
  'Marmara',
  'Ege',
  'Karadeniz',
  'Akdeniz',
  'Boğaziçi',
  'Toros',
  'Kapadokya',
  'Truva',
  'Efes',
  'Palandöken',
  'Uludağ',
  'Erciyes',
  'Sakarya',
  'Meriç',
  'Kızılırmak',
  'Fırat',
  'Dicle',
  'Göksu',
  'Çoruh',
];
const ORG_SUFFIXES: Record<Schemas['Organization']['organizationKind'], string[]> = {
  BANK: ['Bankası A.Ş.', 'Katılım Bankası A.Ş.'],
  INSURER: ['Sigorta A.Ş.', 'Hayat ve Emeklilik A.Ş.'],
  SPONSOR: ['Holding A.Ş.', 'Vakfı'],
  PROVIDER: [
    'Hastanesi A.Ş.',
    'Tıp Merkezi Ltd. Şti.',
    'Termal Otel A.Ş.',
    'Diş Kliniği Ltd. Şti.',
    'Fizik Tedavi Merkezi A.Ş.',
  ],
  VENDOR: ['Bilişim A.Ş.', 'Lojistik Ltd. Şti.'],
  PUBLIC_BODY: ['Belediyesi', 'İl Sağlık Müdürlüğü'],
  OTHER: ['Derneği', 'Kooperatifi'],
};
const KIND_TO_ROLE: Record<
  Schemas['Organization']['organizationKind'],
  Schemas['Organization']['relationshipRole']
> = {
  BANK: 'PAYER',
  INSURER: 'PAYER',
  SPONSOR: 'SPONSOR',
  PROVIDER: 'PROVIDER',
  VENDOR: 'VENDOR',
  PUBLIC_BODY: 'PARTNER',
  OTHER: 'PARTNER',
};
const KIND_WEIGHTS: Schemas['Organization']['organizationKind'][] = [
  'PROVIDER',
  'PROVIDER',
  'PROVIDER',
  'PROVIDER',
  'PROVIDER',
  'PROVIDER',
  'VENDOR',
  'INSURER',
  'BANK',
  'SPONSOR',
  'PUBLIC_BODY',
  'OTHER',
];

const FIRST_NAMES = [
  'Ayşe',
  'Mehmet',
  'Elif',
  'Can',
  'Zeynep',
  'Emre',
  'Defne',
  'Kerem',
  'Nehir',
  'Arda',
  'Ada',
  'Yusuf',
];
const LAST_NAMES = [
  'Yılmaz',
  'Demir',
  'Şahin',
  'Çelik',
  'Kaya',
  'Aydın',
  'Öztürk',
  'Arslan',
  'Doğan',
  'Kılıç',
  'Aslan',
  'Çetin',
];

function pick<T>(random: () => number, list: readonly T[]): T {
  return list[Math.floor(random() * list.length)]!;
}

function isoDaysAgo(base: number, days: number): string {
  return new Date(base - days * 86_400_000).toISOString();
}

/** The whole in-memory world of the mock API. */
export interface MockWorld {
  tenants: MockTenant[];
  accounts: MockAccount[];
  organizations: Map<string, StoredOrganization>;
  relationships: StoredRelationship[];
  people: StoredPerson[];
  serviceRequests: StoredServiceRequest[];
  personRelationships: StoredPersonRelationship[];
  memberships: StoredMembership[];
  programs: StoredProgram[];
  plans: StoredPlan[];
  planVersions: StoredPlanVersion[];
  enrollments: StoredEnrollment[];
  entitlementAccounts: StoredEntitlementAccount[];
  ledgerEntries: StoredLedgerEntry[];
  adjustments: StoredAdjustment[];
  evaluations: Map<string, StoredEvaluation>;
  importBatches: StoredImportBatch[];
  importRows: StoredImportRow[];
  serviceCategories: StoredServiceCategory[];
  serviceDefinitions: StoredServiceDefinition[];
  codeSystems: StoredCodeSystem[];
  codeValues: StoredCodeValue[];
  codeMappings: StoredCodeMapping[];
  providers: StoredProvider[];
  providerLocations: StoredProviderLocation[];
  providerCapabilities: StoredProviderCapability[];
  practitioners: StoredPractitioner[];
  contracts: StoredContract[];
  contractVersions: StoredContractVersion[];
  priceLists: StoredPriceList[];
  priceItems: StoredPriceItem[];
  packageDefinitions: StoredPackageDefinition[];
  providerQuotas: StoredProviderQuota[];
  paymentTerms: StoredPaymentTerm[];
  ruleSets: StoredRuleSet[];
  ruleSetVersions: StoredRuleSetVersion[];
  ruleEvaluations: StoredRuleEvaluation[];
  priceQuotes: StoredPriceQuote[];
  nextId: (offsetMs?: number) => string;
  random: () => number;
}

/** Builds the seeded world; `organizationsPerTenant` defaults to 120 to exercise paging. */
export function buildWorld(
  options: { seed?: number; organizationsPerTenant?: number } = {},
): MockWorld {
  const random = seededRandom(options.seed ?? 20260903);
  const base = Date.UTC(2026, 8, 3, 9, 0, 0);
  const nextId = makeIdFactory(random, base - 400 * 86_400_000);
  const perTenant = options.organizationsPerTenant ?? 120;

  const tenants: MockTenant[] = [
    {
      id: nextId(),
      code: 'DEMO_A',
      displayName: 'Demo Banka A.Ş.',
      status: 'ACTIVE',
      defaultLocale: 'tr-TR',
      defaultTimeZone: 'Europe/Istanbul',
    },
    {
      id: nextId(),
      code: 'DEMO_B',
      displayName: 'Demo Sigorta A.Ş.',
      status: 'ACTIVE',
      defaultLocale: 'tr-TR',
      defaultTimeZone: 'Europe/Istanbul',
    },
  ];
  const accounts: MockAccount[] = [
    {
      actorId: nextId(),
      username: 'admin.a',
      displayName: 'Ayşe Yönetici',
      email: 'admin.a@example.invalid',
      memberships: [{ tenantCode: 'DEMO_A', permissions: ADMIN_PERMISSIONS }],
    },
    {
      actorId: nextId(),
      username: 'reviewer.a',
      displayName: 'Refik İnceleyici',
      email: 'reviewer.a@example.invalid',
      memberships: [{ tenantCode: 'DEMO_A', permissions: REVIEWER_PERMISSIONS }],
    },
    {
      actorId: nextId(),
      username: 'admin.b',
      displayName: 'Bora Yönetici',
      email: 'admin.b@example.invalid',
      memberships: [{ tenantCode: 'DEMO_B', permissions: ADMIN_PERMISSIONS }],
    },
    {
      actorId: nextId(),
      username: 'both.ab',
      displayName: 'Belgin Çift',
      email: 'both.ab@example.invalid',
      memberships: [
        { tenantCode: 'DEMO_A', permissions: ADMIN_PERMISSIONS },
        { tenantCode: 'DEMO_B', permissions: REVIEWER_PERMISSIONS },
      ],
    },
  ];

  const organizations = new Map<string, StoredOrganization>();
  const relationships: StoredRelationship[] = [];
  const usedNames = new Set<string>();

  for (const tenant of tenants) {
    for (let i = 0; i < perTenant; i++) {
      const kind = pick(random, KIND_WEIGHTS);
      let name = `${pick(random, ORG_PREFIXES)} ${pick(random, ORG_SUFFIXES[kind])}`;
      let attempt = 0;
      while (usedNames.has(name)) {
        attempt += 1;
        name = `${pick(random, ORG_PREFIXES)} ${attempt + 1}. ${pick(random, ORG_SUFFIXES[kind])}`;
      }
      usedNames.add(name);
      const soleTrader = kind === 'PROVIDER' && random() < 0.1;
      const organizationId = nextId();
      const org: StoredOrganization = {
        organizationId,
        legalName: name,
        displayName: name.replace(/ (A\.Ş\.|Ltd\. Şti\.)$/, ''),
        organizationKind: kind,
        countryCode: 'TR',
        organizationStatus: 'ACTIVE',
        taxNumber: soleTrader
          ? { type: 'TCKN', value: randomTCKN(random) }
          : { type: 'VKN', value: randomVKN(random) },
        otherIdentifiers:
          random() < 0.3
            ? [
                {
                  type: 'MERSIS',
                  value: Array.from({ length: 16 }, () => Math.floor(random() * 10)).join(''),
                  primary: false,
                },
              ]
            : [],
      };
      organizations.set(organizationId, org);
      const daysAgo = perTenant - i + Math.floor(random() * 3);
      const statusRoll = random();
      relationships.push({
        id: nextId(daysAgo * -86_400_000 + i),
        tenantId: tenant.id,
        organizationId,
        relationshipRole: KIND_TO_ROLE[kind],
        relationshipStatus:
          statusRoll < 0.85 ? 'ACTIVE' : statusRoll < 0.95 ? 'SUSPENDED' : 'PENDING',
        tenantCode:
          random() < 0.5 ? `${tenant.code.slice(-1)}-${String(i + 1).padStart(4, '0')}` : null,
        validFrom: isoDaysAgo(base, daysAgo).slice(0, 10),
        validTo: null,
        createdAt: isoDaysAgo(base, daysAgo),
        rowVersion: 1 + Math.floor(random() * 3),
      });
    }
  }
  // One organization shared by both tenants: exercises the shared-name protection.
  const shared = relationships.find(
    (r) => r.tenantId === tenants[0]!.id && r.relationshipRole === 'PROVIDER',
  );
  if (shared) {
    relationships.push({
      ...shared,
      id: nextId(),
      tenantId: tenants[1]!.id,
      tenantCode: null,
      rowVersion: 1,
    });
  }

  const people: StoredPerson[] = [];
  for (const tenant of tenants) {
    for (let i = 0; i < 40; i++) {
      const daysAgo = 200 - i * 3;
      people.push({
        id: nextId(daysAgo * -86_400_000),
        tenantId: tenant.id,
        firstName: pick(random, FIRST_NAMES),
        middleName: random() < 0.2 ? pick(random, FIRST_NAMES) : null,
        lastName: pick(random, LAST_NAMES),
        birthDate: `${1950 + Math.floor(random() * 55)}-${String(1 + Math.floor(random() * 12)).padStart(2, '0')}-${String(1 + Math.floor(random() * 28)).padStart(2, '0')}`,
        sexAtBirth: random() < 0.5 ? 'FEMALE' : 'MALE',
        status: random() < 0.95 ? 'ACTIVE' : 'INACTIVE',
        identifiers: [{ type: 'TCKN', value: randomTCKN(random), primary: true }],
        rowVersion: 1,
        createdAt: isoDaysAgo(base, daysAgo),
      });
    }
  }

  const serviceRequests: StoredServiceRequest[] = [];
  // SUBMITTED is not in this list on purpose. The server passes through it inside the
  // submit transaction and lands on one of the four below, so no stored row is ever
  // observably SUBMITTED — seeding one would show the screens a state the product never
  // shows them.
  const statuses: Schemas['ServiceRequest']['status'][] = [
    'DRAFT',
    'PENDING_DOCUMENT',
    'PENDING_REVIEW',
    'APPROVED',
    'REJECTED',
    'CLOSED',
  ];
  for (const tenant of tenants) {
    const tenantPeople = people.filter((p) => p.tenantId === tenant.id);
    const providers = relationships.filter(
      (r) => r.tenantId === tenant.id && r.relationshipRole === 'PROVIDER',
    );
    for (let i = 0; i < 25; i++) {
      const daysAgo = 60 - i * 2;
      const status = pick(random, statuses);
      const id = nextId(daysAgo * -86_400_000);
      serviceRequests.push({
        tenantId: tenant.id,
        id,
        reference: `SR-2026-${String(1000 + i)}`,
        personId: pick(random, tenantPeople).id,
        programId: nextId(),
        enrollmentId: nextId(),
        providerOrganizationId: pick(random, providers)?.id ?? null,
        requestType: pick(random, [
          'DIRECT_SERVICE',
          'PREAUTHORIZATION',
          'RESERVATION',
          'REIMBURSEMENT',
        ] as const),
        channel: pick(random, ['BACKOFFICE', 'PROVIDER_PORTAL', 'MEMBER_PORTAL'] as const),
        status,
        serviceDate: isoDaysAgo(base, daysAgo - 5).slice(0, 10),
        requestedStartAt: null,
        requestedEndAt: null,
        submittedAt: status === 'DRAFT' ? null : isoDaysAgo(base, daysAgo),
        createdAt: isoDaysAgo(base, daysAgo),
        currentVersionNo: 1,
        // The same invariants migration 000025 checks: a rejected request names why it was
        // rejected, and one waiting on documents names which ones. A fixture that breaks a
        // constraint the database enforces is a screen written against a row that cannot
        // exist.
        rejectReasonCode: status === 'REJECTED' ? 'NOT_COVERED_BY_PLAN' : null,
        requiredDocumentTypes:
          status === 'PENDING_DOCUMENT' ? ['MEDICAL_REPORT', 'INVOICE'] : null,
        rowVersion: 1,
        items: [
          {
            id: nextId(),
            lineNo: 1,
            serviceDefinitionId: nextId(),
            unitType: 'SESSION',
            // Quantity and amount are exact decimal strings, as they are on the wire. Both
            // are built from integers (kuruş for the amount) so no fixture value ever
            // passes through a float on its way to becoming a string.
            requestedQuantity: toDecimal(1 + Math.floor(random() * 5)),
            requestedAmount: fromMicros(BigInt(50_000 + Math.floor(random() * 450_000)) * 10_000n),
            currencyCode: 'TRY',
            status: 'REQUESTED',
            approvedQuantity: null,
            approvedAmount: null,
            decisionReasonCode: null,
          },
        ],
      });
    }
  }

  // --- Dedicated sponsor and payer organizations for the family and benefit fixtures below.
  const demoA = tenants[0]!;
  const sponsorOrgId = nextId();
  organizations.set(sponsorOrgId, {
    organizationId: sponsorOrgId,
    legalName: 'Kapsora Mensupları Vakfı',
    displayName: 'Kapsora Mensupları Vakfı',
    organizationKind: 'SPONSOR',
    countryCode: 'TR',
    organizationStatus: 'ACTIVE',
    taxNumber: { type: 'VKN', value: randomVKN(random) },
    otherIdentifiers: [],
  });
  const sponsorRel: StoredRelationship = {
    id: nextId(),
    tenantId: demoA.id,
    organizationId: sponsorOrgId,
    relationshipRole: 'SPONSOR',
    relationshipStatus: 'ACTIVE',
    tenantCode: null,
    validFrom: isoDaysAgo(base, 300).slice(0, 10),
    validTo: null,
    createdAt: isoDaysAgo(base, 300),
    rowVersion: 1,
  };
  relationships.push(sponsorRel);

  const payerOrgId = nextId();
  organizations.set(payerOrgId, {
    organizationId: payerOrgId,
    legalName: 'Kapsora Ödeme Bankası A.Ş.',
    displayName: 'Kapsora Ödeme Bankası',
    organizationKind: 'BANK',
    countryCode: 'TR',
    organizationStatus: 'ACTIVE',
    taxNumber: { type: 'VKN', value: randomVKN(random) },
    otherIdentifiers: [],
  });
  const payerRel: StoredRelationship = {
    id: nextId(),
    tenantId: demoA.id,
    organizationId: payerOrgId,
    relationshipRole: 'PAYER',
    relationshipStatus: 'ACTIVE',
    tenantCode: null,
    validFrom: isoDaysAgo(base, 300).slice(0, 10),
    validTo: null,
    createdAt: isoDaysAgo(base, 300),
    rowVersion: 1,
  };
  relationships.push(payerRel);

  // --- A demo family under DEMO_A: a principal, a spouse and two children.
  const familyPrincipal: StoredPerson = {
    id: nextId(),
    tenantId: demoA.id,
    firstName: 'Kaan',
    middleName: null,
    lastName: 'Aydemir',
    birthDate: '1982-04-11',
    sexAtBirth: 'MALE',
    status: 'ACTIVE',
    identifiers: [{ type: 'TCKN', value: randomTCKN(random), primary: true }],
    rowVersion: 1,
    createdAt: isoDaysAgo(base, 260),
  };
  const familySpouse: StoredPerson = {
    id: nextId(),
    tenantId: demoA.id,
    firstName: 'Sevgi',
    middleName: null,
    lastName: 'Aydemir',
    birthDate: '1985-07-02',
    sexAtBirth: 'FEMALE',
    status: 'ACTIVE',
    identifiers: [{ type: 'TCKN', value: randomTCKN(random), primary: true }],
    rowVersion: 1,
    createdAt: isoDaysAgo(base, 260),
  };
  const familyChild1: StoredPerson = {
    id: nextId(),
    tenantId: demoA.id,
    firstName: 'Deniz',
    middleName: null,
    lastName: 'Aydemir',
    birthDate: '2012-03-15',
    sexAtBirth: 'MALE',
    status: 'ACTIVE',
    identifiers: [{ type: 'TCKN', value: randomTCKN(random), primary: true }],
    rowVersion: 1,
    createdAt: isoDaysAgo(base, 260),
  };
  const familyChild2: StoredPerson = {
    id: nextId(),
    tenantId: demoA.id,
    firstName: 'Ece',
    middleName: null,
    lastName: 'Aydemir',
    birthDate: '2015-09-21',
    sexAtBirth: 'FEMALE',
    status: 'ACTIVE',
    identifiers: [{ type: 'TCKN', value: randomTCKN(random), primary: true }],
    rowVersion: 1,
    createdAt: isoDaysAgo(base, 260),
  };
  people.push(familyPrincipal, familySpouse, familyChild1, familyChild2);

  const familyValidFrom = isoDaysAgo(base, 260).slice(0, 10);
  const personRelationships: StoredPersonRelationship[] = [
    {
      id: nextId(),
      tenantId: demoA.id,
      sourcePersonId: familyPrincipal.id,
      targetPersonId: familySpouse.id,
      relationshipType: 'SPOUSE',
      status: 'ACTIVE',
      validFrom: familyValidFrom,
      validTo: null,
      endReasonCode: null,
      rowVersion: 1,
    },
    {
      id: nextId(),
      tenantId: demoA.id,
      sourcePersonId: familyPrincipal.id,
      targetPersonId: familyChild1.id,
      relationshipType: 'CHILD',
      status: 'ACTIVE',
      validFrom: familyValidFrom,
      validTo: null,
      endReasonCode: null,
      rowVersion: 1,
    },
    {
      id: nextId(),
      tenantId: demoA.id,
      sourcePersonId: familyPrincipal.id,
      targetPersonId: familyChild2.id,
      relationshipType: 'CHILD',
      status: 'ACTIVE',
      validFrom: familyValidFrom,
      validTo: null,
      endReasonCode: null,
      rowVersion: 1,
    },
  ];

  const principalMembership: StoredMembership = {
    id: nextId(),
    tenantId: demoA.id,
    personId: familyPrincipal.id,
    sponsorOrganizationId: sponsorRel.id,
    membershipType: 'EMPLOYEE',
    principalMembershipId: null,
    externalMemberNo: 'EMP-100001',
    status: 'ACTIVE',
    validFrom: familyValidFrom,
    validTo: null,
    sourceSystem: null,
    rowVersion: 1,
  };
  const memberships: StoredMembership[] = [
    principalMembership,
    ...[familySpouse, familyChild1, familyChild2].map((p) => ({
      id: nextId(),
      tenantId: demoA.id,
      personId: p.id,
      sponsorOrganizationId: sponsorRel.id,
      membershipType: 'FAMILY',
      principalMembershipId: principalMembership.id,
      externalMemberNo: null,
      status: 'ACTIVE' as const,
      validFrom: familyValidFrom,
      validTo: null,
      sourceSystem: null,
      rowVersion: 1,
    })),
  ];

  // --- Two programs, three plans; each plan gets one PUBLISHED and one DRAFT version.
  const programHealth: StoredProgram = {
    id: nextId(),
    tenantId: demoA.id,
    code: 'HEALTH',
    name: 'Sağlık Yardımı Programı',
    programType: 'HEALTH_BENEFIT',
    sponsorOrganizationId: sponsorRel.id,
    payerOrganizationId: payerRel.id,
    status: 'ACTIVE',
    validFrom: isoDaysAgo(base, 250).slice(0, 10),
    validTo: null,
    rowVersion: 1,
  };
  const programPhysio: StoredProgram = {
    id: nextId(),
    tenantId: demoA.id,
    code: 'PHYSIO',
    name: 'Fizik Tedavi Destek Programı',
    programType: 'WELLNESS_BENEFIT',
    sponsorOrganizationId: sponsorRel.id,
    payerOrganizationId: payerRel.id,
    status: 'ACTIVE',
    validFrom: isoDaysAgo(base, 250).slice(0, 10),
    validTo: null,
    rowVersion: 1,
  };
  const programs: StoredProgram[] = [programHealth, programPhysio];

  const planFamilyHealth: StoredPlan = {
    id: nextId(),
    tenantId: demoA.id,
    programId: programHealth.id,
    code: 'FAM-HEALTH',
    name: 'Aile Sağlık Planı',
    status: 'ACTIVE',
    rowVersion: 1,
  };
  const planIndividualHealth: StoredPlan = {
    id: nextId(),
    tenantId: demoA.id,
    programId: programHealth.id,
    code: 'IND-HEALTH',
    name: 'Bireysel Sağlık Planı',
    status: 'ACTIVE',
    rowVersion: 1,
  };
  const planPhysio: StoredPlan = {
    id: nextId(),
    tenantId: demoA.id,
    programId: programPhysio.id,
    code: 'PHYSIO-STD',
    name: 'Fizik Tedavi Planı',
    status: 'ACTIVE',
    rowVersion: 1,
  };
  const plans: StoredPlan[] = [planFamilyHealth, planIndividualHealth, planPhysio];

  function buildDefinitions(): StoredEntitlementDefinition[] {
    return [
      {
        id: nextId(),
        code: 'HEALTH_MONEY',
        name: 'Sağlık Harcama Bakiyesi',
        unitType: 'MONEY',
        currencyCode: 'TRY',
        familyShared: false,
        allowOverdraft: false,
        initialQuantity: 5000,
        periodType: 'CALENDAR_YEAR',
        periodLength: null,
        rolloverPolicy: 'NONE',
        rolloverCap: null,
        status: 'ACTIVE',
      },
      {
        id: nextId(),
        code: 'PHYSIO_SESSION',
        name: 'Fizyoterapi Seansı',
        unitType: 'SESSION',
        currencyCode: null,
        familyShared: true,
        allowOverdraft: false,
        initialQuantity: 12,
        periodType: 'PLAN_YEAR',
        periodLength: null,
        rolloverPolicy: 'NONE',
        rolloverCap: null,
        status: 'ACTIVE',
      },
    ];
  }

  function buildPlanVersions(plan: StoredPlan): StoredPlanVersion[] {
    const published: StoredPlanVersion = {
      id: nextId(),
      tenantId: demoA.id,
      planId: plan.id,
      versionNo: 1,
      status: 'PUBLISHED',
      validFrom: isoDaysAgo(base, 200).slice(0, 10),
      validTo: null,
      notes: null,
      definitions: buildDefinitions(),
      configurationHash: pseudoHash(plan.code),
      publishedAt: isoDaysAgo(base, 195),
      publishedBy: accounts[3]!.actorId, // both.ab (checker)
      submittedAt: isoDaysAgo(base, 197),
      submittedBy: accounts[0]!.actorId, // admin.a (maker)
      retireReasonCode: null,
      reviewComment: null,
      rowVersion: 3,
    };
    const draft: StoredPlanVersion = {
      id: nextId(),
      tenantId: demoA.id,
      planId: plan.id,
      versionNo: 2,
      status: 'DRAFT',
      validFrom: null,
      validTo: null,
      notes: 'Taslak güncelleme',
      definitions: [],
      configurationHash: null,
      publishedAt: null,
      publishedBy: null,
      submittedAt: null,
      submittedBy: null,
      retireReasonCode: null,
      reviewComment: null,
      rowVersion: 1,
    };
    return [published, draft];
  }

  const planVersions: StoredPlanVersion[] = [
    ...buildPlanVersions(planFamilyHealth),
    ...buildPlanVersions(planIndividualHealth),
    ...buildPlanVersions(planPhysio),
  ];

  // --- Enrollments: the principal in the family plan and, separately, the individual plan
  // (whose account below is FROZEN to demonstrate that state).
  const familyEnrollment: StoredEnrollment = {
    id: nextId(),
    tenantId: demoA.id,
    personId: familyPrincipal.id,
    planId: planFamilyHealth.id,
    planCode: planFamilyHealth.code,
    programId: programHealth.id,
    sponsorMembershipId: principalMembership.id,
    status: 'ACTIVE',
    validFrom: isoDaysAgo(base, 190).slice(0, 10),
    validTo: null,
    enrollmentReason: 'İşe giriş',
    sourceSystem: null,
    rowVersion: 1,
  };
  const individualEnrollment: StoredEnrollment = {
    id: nextId(),
    tenantId: demoA.id,
    personId: familyPrincipal.id,
    planId: planIndividualHealth.id,
    planCode: planIndividualHealth.code,
    programId: programHealth.id,
    sponsorMembershipId: principalMembership.id,
    status: 'ACTIVE',
    validFrom: isoDaysAgo(base, 190).slice(0, 10),
    validTo: null,
    enrollmentReason: null,
    sourceSystem: null,
    rowVersion: 1,
  };
  const enrollments: StoredEnrollment[] = [familyEnrollment, individualEnrollment];

  const familyHealthDefs = planVersions.find(
    (v) => v.planId === planFamilyHealth.id && v.status === 'PUBLISHED',
  )!.definitions;
  const moneyDef = familyHealthDefs.find((d) => d.unitType === 'MONEY')!;
  const sessionDef = familyHealthDefs.find((d) => d.unitType === 'SESSION')!;
  const individualHealthDefs = planVersions.find(
    (v) => v.planId === planIndividualHealth.id && v.status === 'PUBLISHED',
  )!.definitions;
  const individualMoneyDef = individualHealthDefs.find((d) => d.unitType === 'MONEY')!;

  const toAccountDefinition = (d: StoredEntitlementDefinition): StoredAccountDefinition => ({
    id: d.id,
    code: d.code,
    name: d.name,
    unitType: d.unitType,
    currencyCode: d.currencyCode,
    familyShared: d.familyShared,
    allowOverdraft: d.allowOverdraft,
  });

  const familyMoneyAccount: StoredEntitlementAccount = {
    id: nextId(),
    tenantId: demoA.id,
    enrollmentId: familyEnrollment.id,
    personId: familyPrincipal.id,
    definition: toAccountDefinition(moneyDef),
    benefitPeriodFrom: isoDaysAgo(base, 190).slice(0, 10),
    benefitPeriodTo: null,
    totalGranted: '5000.000000',
    available: '1250.000000',
    consumed: '3750.000000',
    reserved: '0.000000',
    expired: '0.000000',
    status: 'OPEN',
    rowVersion: 1,
  };
  const familySessionAccount: StoredEntitlementAccount = {
    id: nextId(),
    tenantId: demoA.id,
    enrollmentId: familyEnrollment.id,
    personId: familyPrincipal.id,
    definition: toAccountDefinition(sessionDef),
    benefitPeriodFrom: isoDaysAgo(base, 190).slice(0, 10),
    benefitPeriodTo: null,
    totalGranted: '12.000000',
    available: '9.000000',
    consumed: '3.000000',
    reserved: '0.000000',
    expired: '0.000000',
    status: 'OPEN',
    rowVersion: 1,
  };
  const frozenAccount: StoredEntitlementAccount = {
    id: nextId(),
    tenantId: demoA.id,
    enrollmentId: individualEnrollment.id,
    personId: familyPrincipal.id,
    definition: toAccountDefinition(individualMoneyDef),
    benefitPeriodFrom: isoDaysAgo(base, 190).slice(0, 10),
    benefitPeriodTo: null,
    totalGranted: '5000.000000',
    available: '5000.000000',
    consumed: '0.000000',
    reserved: '0.000000',
    expired: '0.000000',
    status: 'FROZEN',
    rowVersion: 1,
  };
  const entitlementAccounts: StoredEntitlementAccount[] = [
    familyMoneyAccount,
    familySessionAccount,
    frozenAccount,
  ];

  // --- ~30 ledger movements on the family MONEY account, newest first.
  const MOVEMENT_TYPES: StoredLedgerEntry['movementType'][] = [
    'CONSUME',
    'RESERVE',
    'RELEASE',
    'ADJUST',
  ];
  const ledgerEntries: StoredLedgerEntry[] = [];
  for (let i = 0; i < 29; i++) {
    const movementType = MOVEMENT_TYPES[i % MOVEMENT_TYPES.length]!;
    const amount = toDecimal(50 + (i % 5) * 25);
    ledgerEntries.push({
      id: nextId(),
      tenantId: demoA.id,
      accountId: familyMoneyAccount.id,
      effectiveAt: isoDaysAgo(base, 5 + i * 6),
      movementType,
      deltaAvailable:
        movementType === 'CONSUME' || movementType === 'RESERVE' ? `-${amount}` : amount,
      deltaConsumed: movementType === 'CONSUME' ? amount : '0.000000',
      deltaExpired: '0.000000',
      deltaReserved:
        movementType === 'RESERVE'
          ? amount
          : movementType === 'RELEASE'
            ? `-${amount}`
            : '0.000000',
      deltaTotal: movementType === 'ADJUST' ? amount : '0.000000',
      referenceType: movementType === 'ADJUST' ? 'MANUAL' : 'SERVICE_REQUEST',
      referenceId: nextId(),
      reservationId: null,
      reasonCode: movementType === 'ADJUST' ? 'MANUAL_CORRECTION' : null,
      reasonText: null,
      createdBy: accounts[0]!.actorId,
    });
  }
  ledgerEntries.push({
    id: nextId(),
    tenantId: demoA.id,
    accountId: familyMoneyAccount.id,
    effectiveAt: isoDaysAgo(base, 190),
    movementType: 'GRANT',
    deltaAvailable: '5000.000000',
    deltaConsumed: '0.000000',
    deltaExpired: '0.000000',
    deltaReserved: '0.000000',
    deltaTotal: '5000.000000',
    referenceType: 'ENROLLMENT',
    referenceId: familyEnrollment.id,
    reservationId: null,
    reasonCode: null,
    reasonText: null,
    createdBy: null,
  });

  // --- M3 catalog: a three-level category tree plus one isolated pricing branch.
  const category = (
    code: string,
    name: string,
    parentId: string | null,
  ): StoredServiceCategory => ({
    id: nextId(),
    tenantId: demoA.id,
    parentId,
    code,
    name,
    domain: 'HEALTH',
    active: true,
    rowVersion: 1,
  });
  const catHealth = category('HEALTH', 'Sağlık Hizmetleri', null);
  const catOutpatient = category('HEALTH_OUTPATIENT', 'Ayakta Tedavi', catHealth.id);
  const catPhysio = category('HEALTH_PHYSIO', 'Fizik Tedavi', catOutpatient.id);
  const catImaging = category('HEALTH_IMAGING', 'Görüntüleme', catHealth.id);
  // Kept apart from the tree above: it exists only to hold the ambiguous price pair, so
  // no other fixture can trip over the tie.
  const catPricingLab = category('PRICING_LAB', 'Laboratuvar (fiyat çakışma örneği)', null);
  const serviceCategories: StoredServiceCategory[] = [
    catHealth,
    catOutpatient,
    catPhysio,
    catImaging,
    catPricingLab,
  ];

  const definition = (
    categoryId: string,
    code: string,
    name: string,
    fulfillmentMode: Schemas['FulfillmentMode'],
    defaultUnitType: Schemas['ServiceUnitType'],
  ): StoredServiceDefinition => ({
    id: nextId(),
    tenantId: demoA.id,
    categoryId,
    code,
    name,
    description: null,
    fulfillmentMode,
    defaultUnitType,
    requiresProvider: true,
    active: true,
    rowVersion: 1,
  });
  const defPhysio = definition(
    catPhysio.id,
    'PHYSIO_SESSION',
    'Fizyoterapi Seansı',
    'SESSION',
    'SESSION',
  );
  const defGpVisit = definition(
    catOutpatient.id,
    'GP_VISIT',
    'Pratisyen Muayenesi',
    'APPOINTMENT',
    'COUNT',
  );
  const defMri = definition(catImaging.id, 'MRI_SCAN', 'MR Çekimi', 'APPOINTMENT', 'COUNT');
  const defAmbiguous = definition(
    catPricingLab.id,
    'LAB_PANEL_AMBIGUOUS',
    'Laboratuvar Paneli (çakışan fiyat)',
    'DIRECT',
    'COUNT',
  );
  const serviceDefinitions: StoredServiceDefinition[] = [
    defPhysio,
    defGpVisit,
    defMri,
    defAmbiguous,
  ];

  const codeSystemSut: StoredCodeSystem = {
    id: nextId(),
    tenantId: demoA.id,
    code: 'SUT',
    name: 'Sağlık Uygulama Tebliği',
    version: '2026',
    authority: 'SGK',
    licensed: false,
    status: 'ACTIVE',
    validFrom: '2026-01-01',
    validTo: null,
    rowVersion: 1,
  };
  const codeValue = (
    code: string,
    display: string,
    parentCode: string | null,
    validFrom = '2026-01-01',
  ): StoredCodeValue => ({
    id: nextId(),
    tenantId: demoA.id,
    codeSystemId: codeSystemSut.id,
    code,
    display,
    parentCode,
    validFrom,
    validTo: null,
    active: true,
    attributes: {},
  });
  const codeValues: StoredCodeValue[] = [
    codeValue('P', 'Fizik tedavi ve rehabilitasyon', null),
    codeValue('520030', 'Fizik tedavi seansı', 'P'),
    codeValue('803930', 'Manyetik rezonans görüntüleme', null),
    codeValue('530010', 'Pratisyen hekim muayenesi', null),
    // Retired at the end of 2026, so an asOf in 2027 must not return it.
    { ...codeValue('520031', 'Fizik tedavi seansı (eski)', 'P'), validTo: '2027-01-01' },
  ];
  const codeMappings: StoredCodeMapping[] = [
    {
      id: nextId(),
      tenantId: demoA.id,
      serviceDefinitionId: defPhysio.id,
      codeSystemId: codeSystemSut.id,
      code: '520030',
      validFrom: '2026-01-01',
      validTo: null,
      primary: true,
    },
  ];

  // --- M3 provider: one profile, two locations, two capabilities, three practitioners.
  const providerOrgId = nextId();
  organizations.set(providerOrgId, {
    organizationId: providerOrgId,
    legalName: 'Kapsora Anlaşmalı Sağlık Grubu A.Ş.',
    displayName: 'Kapsora Anlaşmalı Sağlık Grubu',
    organizationKind: 'PROVIDER',
    countryCode: 'TR',
    organizationStatus: 'ACTIVE',
    taxNumber: { type: 'VKN', value: randomVKN(random) },
    otherIdentifiers: [],
  });
  const providerRel: StoredRelationship = {
    id: nextId(),
    tenantId: demoA.id,
    organizationId: providerOrgId,
    relationshipRole: 'PROVIDER',
    relationshipStatus: 'ACTIVE',
    tenantCode: null,
    validFrom: '2025-12-01',
    validTo: null,
    createdAt: isoDaysAgo(base, 300),
    rowVersion: 1,
  };
  relationships.push(providerRel);

  const providerHealth: StoredProvider = {
    id: nextId(),
    tenantId: demoA.id,
    tenantOrganizationId: providerRel.id,
    providerType: 'HOSPITAL',
    status: 'ACTIVE',
    networkTier: 'A',
    contractedFrom: '2026-01-01',
    contractedTo: null,
    notes: null,
    rowVersion: 1,
  };
  const providers: StoredProvider[] = [providerHealth];

  const locationIstanbul: StoredProviderLocation = {
    id: nextId(),
    tenantId: demoA.id,
    providerId: providerHealth.id,
    code: 'IST-01',
    name: 'Kadıköy Tıp Merkezi',
    addressLine: 'Bağdat Caddesi No: 120',
    district: 'Kadıköy',
    city: 'İstanbul',
    countryCode: 'TR',
    postalCode: '34710',
    latitude: 40.9833,
    longitude: 29.0333,
    timezone: 'Europe/Istanbul',
    phone: '+902165550101',
    status: 'ACTIVE',
    rowVersion: 1,
  };
  const locationAnkara: StoredProviderLocation = {
    id: nextId(),
    tenantId: demoA.id,
    providerId: providerHealth.id,
    code: 'ANK-01',
    name: 'Çankaya Poliklinik',
    addressLine: 'Atatürk Bulvarı No: 45',
    district: 'Çankaya',
    city: 'Ankara',
    countryCode: 'TR',
    postalCode: '06680',
    latitude: 39.9208,
    longitude: 32.8541,
    timezone: 'Europe/Istanbul',
    phone: '+903125550202',
    status: 'ACTIVE',
    rowVersion: 1,
  };
  const providerLocations: StoredProviderLocation[] = [locationIstanbul, locationAnkara];

  const providerCapabilities: StoredProviderCapability[] = [
    // A category capability: it covers every definition under HEALTH_OUTPATIENT,
    // including PHYSIO_SESSION one level further down and any added later.
    {
      id: nextId(),
      tenantId: demoA.id,
      locationId: locationIstanbul.id,
      serviceDefinitionId: null,
      serviceCategoryId: catOutpatient.id,
      validFrom: '2026-01-01',
      validTo: null,
      notes: 'Ayakta tedavi hizmetlerinin tamamı',
    },
    // A definition capability: only this one service, and only here.
    {
      id: nextId(),
      tenantId: demoA.id,
      locationId: locationIstanbul.id,
      serviceDefinitionId: defMri.id,
      serviceCategoryId: null,
      validFrom: '2026-01-01',
      validTo: null,
      notes: null,
    },
  ];

  const practitioner = (
    fullName: string,
    title: string,
    branchCode: string,
    registrationNumber: string,
    locations: { locationId: string; role: Schemas['PractitionerRole'] }[],
  ): StoredPractitioner => {
    const id = nextId();
    return {
      id,
      tenantId: demoA.id,
      providerId: providerHealth.id,
      personId: null,
      fullName,
      title,
      branchCode,
      registrationAuthority: 'TTB',
      registrationNumber,
      validFrom: '2026-01-01',
      validTo: null,
      status: 'ACTIVE',
      locations: locations.map((l) => ({
        id: nextId(),
        practitionerId: id,
        locationId: l.locationId,
        role: l.role,
        validFrom: '2026-01-01',
        validTo: null,
      })),
      rowVersion: 1,
    };
  };
  const practitioners: StoredPractitioner[] = [
    practitioner('Elif Şahin', 'Dr.', 'FTR', '10045001', [
      { locationId: locationIstanbul.id, role: 'ATTENDING' },
    ]),
    practitioner('Murat Kılıç', 'Uzm. Dr.', 'RAD', '10045002', [
      { locationId: locationIstanbul.id, role: 'CONSULTANT' },
      { locationId: locationAnkara.id, role: 'CONSULTANT' },
    ]),
    practitioner('Zeynep Arslan', 'Fzt.', 'FTR', '10045003', [
      { locationId: locationAnkara.id, role: 'TECHNICIAN' },
    ]),
  ];

  // --- M3 contract: one published version with a full price sheet, one draft beside it.
  const contractHealth: StoredContract = {
    id: nextId(),
    tenantId: demoA.id,
    code: 'HLT-2026',
    name: 'Sağlık Hizmet Sözleşmesi 2026',
    payerOrganizationId: payerRel.id,
    providerProfileId: providerHealth.id,
    sponsorOrganizationId: sponsorRel.id,
    domainCode: 'HEALTH',
    status: 'ACTIVE',
    rowVersion: 1,
  };
  const contracts: StoredContract[] = [contractHealth];

  const publishedContractVersion: StoredContractVersion = {
    id: nextId(),
    tenantId: demoA.id,
    contractId: contractHealth.id,
    versionNo: 1,
    status: 'PUBLISHED',
    validFrom: '2026-01-01',
    validTo: null,
    currencyCode: 'TRY',
    notes: null,
    configurationHash: pseudoHash(`${contractHealth.code}:1`),
    submittedAt: isoDaysAgo(base, 240),
    submittedBy: accounts[0]!.actorId, // admin.a (maker)
    publishedAt: isoDaysAgo(base, 239),
    publishedBy: accounts[3]!.actorId, // both.ab (checker)
    reviewComment: null,
    retireReasonCode: null,
    rowVersion: 3,
  };
  const draftContractVersion: StoredContractVersion = {
    id: nextId(),
    tenantId: demoA.id,
    contractId: contractHealth.id,
    versionNo: 2,
    status: 'DRAFT',
    validFrom: '2027-01-01',
    validTo: null,
    currencyCode: 'TRY',
    notes: '2027 zam görüşmesi taslağı',
    configurationHash: null,
    submittedAt: null,
    submittedBy: null,
    publishedAt: null,
    publishedBy: null,
    reviewComment: null,
    retireReasonCode: null,
    rowVersion: 1,
  };
  const contractVersions: StoredContractVersion[] = [
    publishedContractVersion,
    draftContractVersion,
  ];

  const standardList: StoredPriceList = {
    id: nextId(),
    tenantId: demoA.id,
    contractVersionId: publishedContractVersion.id,
    code: 'STD',
    name: 'Standart Tarife',
    priority: 100,
    seasonFrom: null,
    seasonTo: null,
    weekdayMask: null,
    rowVersion: 1,
  };
  const winterList: StoredPriceList = {
    id: nextId(),
    tenantId: demoA.id,
    contractVersionId: publishedContractVersion.id,
    code: 'WINTER',
    name: 'Kış Dönemi Tarifesi',
    priority: 200,
    seasonFrom: '2026-12-01',
    seasonTo: '2027-03-01',
    weekdayMask: null,
    rowVersion: 1,
  };
  const priceLists: StoredPriceList[] = [standardList, winterList];

  const packageCheckup: StoredPackageDefinition = {
    id: nextId(),
    tenantId: demoA.id,
    contractVersionId: publishedContractVersion.id,
    code: 'CHECKUP',
    name: 'Yıllık Kontrol Paketi',
    inclusionRule: 'ALL',
    minLines: null,
    // GP_VISIT is deliberately not a package line: it is the fixture that proves a
    // definition with no price of its own resolves through its category.
    lines: [
      { serviceDefinitionId: defMri.id, includedQuantity: '1.000000' },
      { serviceDefinitionId: defPhysio.id, includedQuantity: '4.000000' },
    ],
  };
  const packageDefinitions: StoredPackageDefinition[] = [packageCheckup];

  const priceItem = (
    priceListId: string,
    target: Partial<
      Pick<
        StoredPriceItem,
        'serviceDefinitionId' | 'serviceCategoryId' | 'packageDefinitionId' | 'locationId'
      >
    >,
    rest: Pick<StoredPriceItem, 'unitType' | 'pricingMethod' | 'amount'> & Partial<StoredPriceItem>,
  ): StoredPriceItem => ({
    id: nextId(),
    tenantId: demoA.id,
    priceListId,
    serviceDefinitionId: target.serviceDefinitionId ?? null,
    serviceCategoryId: target.serviceCategoryId ?? null,
    packageDefinitionId: target.packageDefinitionId ?? null,
    locationId: target.locationId ?? null,
    percent: null,
    formulaKey: null,
    minAmount: null,
    maxAmount: null,
    memberShareMethod: 'NONE',
    memberShareAmount: null,
    memberSharePercent: null,
    validFrom: '2026-01-01',
    validTo: null,
    priority: 100,
    ...rest,
  });

  const priceItems: StoredPriceItem[] = [
    // Names the definition itself, with a 20% member share.
    priceItem(
      standardList.id,
      { serviceDefinitionId: defPhysio.id },
      {
        unitType: 'SESSION',
        pricingMethod: 'UNIT',
        amount: '750.000000',
        memberShareMethod: 'PERCENT',
        memberSharePercent: '20.000000',
      },
    ),
    // Location-specific: beats a tenant-wide price of the same tier.
    priceItem(
      standardList.id,
      { serviceDefinitionId: defMri.id, locationId: locationIstanbul.id },
      { unitType: 'COUNT', pricingMethod: 'FIXED', amount: '2500.000000' },
    ),
    priceItem(
      standardList.id,
      { serviceDefinitionId: defMri.id },
      { unitType: 'COUNT', pricingMethod: 'FIXED', amount: '2900.000000' },
    ),
    // A category price: GP_VISIT has no price of its own and resolves through this one.
    priceItem(
      standardList.id,
      { serviceCategoryId: catOutpatient.id },
      { unitType: 'COUNT', pricingMethod: 'FIXED', amount: '500.000000' },
    ),
    priceItem(
      standardList.id,
      { packageDefinitionId: packageCheckup.id },
      { unitType: 'COUNT', pricingMethod: 'FIXED', amount: '3000.000000' },
    ),
    // The deliberate tie: two equally specific, equally prioritised prices for the same
    // service on the same date, in the same list. The resolver must answer
    // REVIEW_REQUIRED / PRICE_AMBIGUOUS rather than invent a winner.
    priceItem(
      standardList.id,
      { serviceDefinitionId: defAmbiguous.id },
      { unitType: 'COUNT', pricingMethod: 'FIXED', amount: '1200.000000' },
    ),
    priceItem(
      standardList.id,
      { serviceDefinitionId: defAmbiguous.id },
      { unitType: 'COUNT', pricingMethod: 'FIXED', amount: '1350.000000' },
    ),
    // Only inside the winter season window; outside it GP_VISIT falls back to the
    // category price above.
    priceItem(
      winterList.id,
      { serviceDefinitionId: defGpVisit.id },
      { unitType: 'COUNT', pricingMethod: 'FIXED', amount: '900.000000' },
    ),
  ];

  const providerQuotas: StoredProviderQuota[] = [
    {
      id: nextId(),
      tenantId: demoA.id,
      contractVersionId: publishedContractVersion.id,
      locationId: locationIstanbul.id,
      serviceDefinitionId: defMri.id,
      periodType: 'YEAR',
      periodFrom: '2026-01-01',
      periodTo: '2027-01-01',
      capacity: '1200.000000',
      consumed: '318.000000',
      allowOverdraft: false,
    },
  ];

  const paymentTerms: StoredPaymentTerm[] = [
    {
      id: nextId(),
      tenantId: demoA.id,
      contractVersionId: publishedContractVersion.id,
      dueDays: 30,
      settlementMethod: 'BANK_TRANSFER',
      taxBehaviour: 'EXCLUSIVE',
      vatRate: '10.00',
      lateFeePercent: '1.50',
      rowVersion: 1,
    },
  ];

  // --- M3 rules: one set with a published version carrying two rules and two test cases.
  const ruleSetDocuments: StoredRuleSet = {
    id: nextId(),
    tenantId: demoA.id,
    code: 'HLT_DOCUMENTS',
    name: 'Sağlık Belge Kuralları',
    domainCode: 'HEALTH',
    purpose: 'DOCUMENT',
    status: 'ACTIVE',
    rowVersion: 1,
  };
  const ruleSets: StoredRuleSet[] = [ruleSetDocuments];

  const publishedRuleVersion: StoredRuleSetVersion = {
    id: nextId(),
    tenantId: demoA.id,
    ruleSetId: ruleSetDocuments.id,
    versionNo: 1,
    status: 'PUBLISHED',
    validFrom: '2026-01-01',
    validTo: null,
    inputSchema: { serviceCode: 'string', quantity: 'double', requestedAmount: 'double' },
    notes: null,
    contentHash: pseudoHash(`${ruleSetDocuments.code}:1`),
    submittedAt: isoDaysAgo(base, 220),
    submittedBy: accounts[0]!.actorId,
    publishedAt: isoDaysAgo(base, 219),
    publishedBy: accounts[3]!.actorId,
    reviewComment: null,
    retireReasonCode: null,
    rules: [
      {
        id: nextId(),
        code: 'PHYSIO_REPORT_REQUIRED',
        name: 'Uzun fizik tedavi için rapor',
        priority: 10,
        condition: 'serviceCode == "PHYSIO_SESSION" && quantity > 6',
        actions: [{ type: 'REQUIRE_DOCUMENT', payload: { documentTypeCode: 'MEDICAL_REPORT' } }],
        explanationCode: 'DOCUMENT_REQUIRED',
        explanationParams: { documentTypeCode: 'MEDICAL_REPORT' },
        stopOnMatch: false,
        active: true,
      },
      {
        id: nextId(),
        code: 'HIGH_AMOUNT_REVIEW',
        name: 'Yüksek tutar mali inceleme',
        priority: 20,
        condition: 'requestedAmount > 5000',
        actions: [{ type: 'REQUIRE_FINANCIAL_REVIEW' }],
        explanationCode: 'AMOUNT_ABOVE_THRESHOLD',
        explanationParams: null,
        stopOnMatch: false,
        active: true,
      },
    ],
    testCases: [
      {
        id: nextId(),
        code: 'SHORT_PHYSIO_APPROVED',
        description: 'Kısa seans, düşük tutar: hiçbir kural eşleşmez.',
        input: { serviceCode: 'PHYSIO_SESSION', quantity: '2', requestedAmount: '1500.000000' },
        expectedOutcome: 'APPROVED',
        expectedExplanations: [],
        expectedActions: null,
      },
      {
        id: nextId(),
        code: 'LONG_PHYSIO_REVIEW',
        description: 'Uzun seans ve yüksek tutar: iki kural da eşleşir.',
        input: { serviceCode: 'PHYSIO_SESSION', quantity: '10', requestedAmount: '7500.000000' },
        expectedOutcome: 'REVIEW_REQUIRED',
        expectedExplanations: ['DOCUMENT_REQUIRED', 'AMOUNT_ABOVE_THRESHOLD'],
        expectedActions: null,
      },
    ],
    rowVersion: 4,
  };
  const ruleSetVersions: StoredRuleSetVersion[] = [publishedRuleVersion];

  // One decision that really was recorded, so the append-only read has a fixture. The
  // snapshot carries ids, dates and quantities only: no identity number, no name.
  const ruleEvaluations: StoredRuleEvaluation[] = [
    {
      tenantId: demoA.id,
      id: nextId(),
      ruleSetVersionId: publishedRuleVersion.id,
      ruleSetId: ruleSetDocuments.id,
      ruleSetCode: ruleSetDocuments.code,
      versionNo: 1,
      subjectType: 'SERVICE_REQUEST',
      subjectId: null,
      outcome: 'REVIEW_REQUIRED',
      inputHash: pseudoHash('rule-evaluation-fixture'),
      inputSnapshot: {
        serviceCode: 'PHYSIO_SESSION',
        quantity: '10',
        requestedAmount: '7500.000000',
      },
      durationMs: 3,
      evaluatedAt: isoDaysAgo(base, 12),
      evaluatedBy: accounts[0]!.actorId,
      results: [
        {
          sequence: 1,
          ruleId: publishedRuleVersion.rules[0]!.id,
          ruleCode: 'PHYSIO_REPORT_REQUIRED',
          matched: true,
          actionType: 'REQUIRE_DOCUMENT',
          actionPayload: { documentTypeCode: 'MEDICAL_REPORT' },
          explanationCode: 'DOCUMENT_REQUIRED',
          severity: 'WARNING',
        },
        {
          sequence: 2,
          ruleId: publishedRuleVersion.rules[1]!.id,
          ruleCode: 'HIGH_AMOUNT_REVIEW',
          matched: true,
          actionType: 'REQUIRE_FINANCIAL_REVIEW',
          actionPayload: null,
          explanationCode: 'AMOUNT_ABOVE_THRESHOLD',
          severity: 'WARNING',
        },
      ],
    },
  ];

  return {
    tenants,
    accounts,
    organizations,
    relationships,
    people,
    serviceRequests,
    personRelationships,
    memberships,
    programs,
    plans,
    planVersions,
    enrollments,
    entitlementAccounts,
    ledgerEntries,
    adjustments: [],
    evaluations: new Map(),
    importBatches: [],
    importRows: [],
    serviceCategories,
    serviceDefinitions,
    codeSystems: [codeSystemSut],
    codeValues,
    codeMappings,
    providers,
    providerLocations,
    providerCapabilities,
    practitioners,
    contracts,
    contractVersions,
    priceLists,
    priceItems,
    packageDefinitions,
    providerQuotas,
    paymentTerms,
    ruleSets,
    ruleSetVersions,
    ruleEvaluations,
    priceQuotes: [],
    nextId,
    random,
  };
}

/** Projects a relationship + organization into the contract's Organization shape. */
export function toOrganization(
  rel: StoredRelationship,
  org: StoredOrganization,
): Schemas['Organization'] {
  const identifiers: Schemas['OrganizationIdentifier'][] = [];
  if (org.taxNumber) {
    identifiers.push({
      type: org.taxNumber.type,
      maskedValue: maskIdentifier(org.taxNumber.type, org.taxNumber.value),
      primary: true,
    });
  }
  for (const other of org.otherIdentifiers) {
    identifiers.push({
      type: other.type,
      maskedValue: maskIdentifier(other.type, other.value),
      primary: other.primary,
    });
  }
  const out: Schemas['Organization'] = {
    id: rel.id,
    organizationId: org.organizationId,
    legalName: org.legalName,
    displayName: org.displayName,
    organizationKind: org.organizationKind,
    countryCode: org.countryCode,
    organizationStatus: org.organizationStatus,
    relationshipRole: rel.relationshipRole,
    relationshipStatus: rel.relationshipStatus,
    identifiers,
    validFrom: rel.validFrom,
    rowVersion: rel.rowVersion,
  };
  if (rel.tenantCode !== null) out.tenantCode = rel.tenantCode;
  if (rel.validTo !== null) out.validTo = rel.validTo;
  return out;
}

/** Projects to the list row shape. */
export function toOrganizationSummary(
  rel: StoredRelationship,
  org: StoredOrganization,
): Schemas['OrganizationSummary'] {
  return {
    id: rel.id,
    organizationId: org.organizationId,
    displayName: org.displayName,
    organizationKind: org.organizationKind,
    relationshipRole: rel.relationshipRole,
    relationshipStatus: rel.relationshipStatus,
  };
}

export function toPersonSummary(p: StoredPerson): Schemas['PersonSummary'] {
  const primary = p.identifiers.find((i) => i.primary);
  return {
    id: p.id,
    displayName: [p.firstName, p.middleName, p.lastName].filter(Boolean).join(' '),
    status: p.status,
    maskedPrimaryIdentifier: primary ? maskIdentifier(primary.type as 'TCKN', primary.value) : null,
  };
}

export function toPerson(p: StoredPerson): Schemas['Person'] {
  return {
    ...toPersonSummary(p),
    firstName: p.firstName,
    middleName: p.middleName,
    lastName: p.lastName,
    birthDate: p.birthDate,
    sexAtBirth: p.sexAtBirth,
    identifiers: p.identifiers.map((i) => ({
      type: i.type,
      maskedValue: maskIdentifier(i.type as 'TCKN', i.value),
      primary: i.primary,
    })),
    rowVersion: p.rowVersion,
  };
}

export function toPersonRelationship(
  world: MockWorld,
  rel: StoredPersonRelationship,
  viewpointPersonId: string,
): Schemas['PersonRelationship'] {
  const type = RELATIONSHIP_TYPE_CATALOG.find((t) => t.code === rel.relationshipType);
  const otherId =
    rel.sourcePersonId === viewpointPersonId ? rel.targetPersonId : rel.sourcePersonId;
  const other = world.people.find((p) => p.id === otherId);
  const direction: Schemas['PersonRelationship']['direction'] =
    type?.isDirectional === false
      ? 'MUTUAL'
      : rel.sourcePersonId === viewpointPersonId
        ? 'OUTGOING'
        : 'INCOMING';
  const otherPerson: Schemas['PersonSummary'] = other
    ? toPersonSummary(other)
    : {
        id: otherId,
        displayName: 'Bilinmeyen Kişi',
        status: 'ACTIVE',
        maskedPrimaryIdentifier: null,
      };
  return {
    direction,
    endReasonCode: rel.endReasonCode,
    id: rel.id,
    otherPerson,
    relationshipType: rel.relationshipType,
    rowVersion: rel.rowVersion,
    status: rel.status,
    validFrom: rel.validFrom,
    validTo: rel.validTo,
  };
}

export function toSponsorMembership(
  world: MockWorld,
  m: StoredMembership,
): Schemas['SponsorMembership'] {
  const rel = world.relationships.find((r) => r.id === m.sponsorOrganizationId);
  const org = rel ? world.organizations.get(rel.organizationId) : undefined;
  return {
    externalMemberNo: m.externalMemberNo,
    id: m.id,
    membershipType: m.membershipType,
    personId: m.personId,
    principalMembershipId: m.principalMembershipId,
    rowVersion: m.rowVersion,
    sourceSystem: m.sourceSystem,
    sponsorDisplayName: org?.displayName ?? 'Bilinmeyen Sponsor',
    sponsorOrganizationId: m.sponsorOrganizationId,
    status: m.status,
    validFrom: m.validFrom,
    validTo: m.validTo,
  };
}

/** Definitions are typed exactly like the schema: `initialQuantity` is plan configuration, not a balance. */
export function toEntitlementDefinition(
  d: StoredEntitlementDefinition,
): Schemas['EntitlementDefinition'] {
  const out: Schemas['EntitlementDefinition'] = {
    allowOverdraft: d.allowOverdraft,
    code: d.code,
    familyShared: d.familyShared,
    id: d.id,
    initialQuantity: d.initialQuantity,
    name: d.name,
    periodType: d.periodType,
    rolloverPolicy: d.rolloverPolicy,
    status: d.status,
    unitType: d.unitType,
  };
  if (d.currencyCode !== null) out.currencyCode = d.currencyCode;
  if (d.periodLength !== null) out.periodLength = d.periodLength;
  if (d.rolloverCap !== null) out.rolloverCap = d.rolloverCap;
  return out;
}

export function toProgram(world: MockWorld, p: StoredProgram): Schemas['Program'] {
  const sponsorRel = world.relationships.find((r) => r.id === p.sponsorOrganizationId);
  const payerRel = world.relationships.find((r) => r.id === p.payerOrganizationId);
  const sponsorOrg = sponsorRel ? world.organizations.get(sponsorRel.organizationId) : undefined;
  const payerOrg = payerRel ? world.organizations.get(payerRel.organizationId) : undefined;
  return {
    code: p.code,
    id: p.id,
    name: p.name,
    payerDisplayName: payerOrg?.displayName ?? 'Bilinmeyen Ödeyen',
    payerOrganizationId: p.payerOrganizationId,
    planCount: world.plans.filter((pl) => pl.programId === p.id).length,
    programType: p.programType,
    rowVersion: p.rowVersion,
    sponsorDisplayName: sponsorOrg?.displayName ?? 'Bilinmeyen Sponsor',
    sponsorOrganizationId: p.sponsorOrganizationId,
    status: p.status,
    validFrom: p.validFrom,
    validTo: p.validTo,
  };
}

export function toPlanVersionSummary(v: StoredPlanVersion): Schemas['PlanVersionSummary'] {
  return {
    id: v.id,
    planId: v.planId,
    publishedAt: v.publishedAt,
    rowVersion: v.rowVersion,
    status: v.status,
    validFrom: v.validFrom,
    validTo: v.validTo,
    versionNo: v.versionNo,
  };
}

export function toPlanVersion(v: StoredPlanVersion): Schemas['PlanVersion'] {
  return {
    ...toPlanVersionSummary(v),
    configurationHash: v.configurationHash,
    definitions: v.definitions.map(toEntitlementDefinition),
    notes: v.notes,
    publishedBy: v.publishedBy,
    retireReasonCode: v.retireReasonCode,
    reviewComment: v.reviewComment,
    submittedAt: v.submittedAt,
    submittedBy: v.submittedBy,
  };
}

export function toPlan(world: MockWorld, plan: StoredPlan): Schemas['Plan'] {
  const versions = world.planVersions
    .filter((v) => v.planId === plan.id)
    .sort((a, b) => b.versionNo - a.versionNo)
    .map(toPlanVersionSummary);
  return {
    code: plan.code,
    id: plan.id,
    name: plan.name,
    programId: plan.programId,
    rowVersion: plan.rowVersion,
    status: plan.status,
    versions,
  };
}

export function toEnrollment(e: StoredEnrollment): Schemas['Enrollment'] {
  return {
    enrollmentReason: e.enrollmentReason,
    id: e.id,
    personId: e.personId,
    planCode: e.planCode,
    planId: e.planId,
    programId: e.programId,
    rowVersion: e.rowVersion,
    sourceSystem: e.sourceSystem,
    sponsorMembershipId: e.sponsorMembershipId,
    status: e.status,
    validFrom: e.validFrom,
    validTo: e.validTo,
  };
}

/**
 * `EntitlementAccount` with its quantity fields kept as decimal strings; the checked-in
 * generated schema types them `number`, but entitlements.ts documents (and the real Go
 * API's `decimal.Decimal` JSON encoding produces) decimal strings on the wire. See the
 * mocks README note in handlers.ts for the reasoning.
 */
export type MockEntitlementAccount = Omit<
  Schemas['EntitlementAccount'],
  'available' | 'consumed' | 'expired' | 'reserved' | 'totalGranted'
> & {
  available: Decimal;
  consumed: Decimal;
  expired: Decimal;
  reserved: Decimal;
  totalGranted: Decimal;
};

export function toEntitlementAccount(
  a: StoredEntitlementAccount,
  shared: boolean,
): MockEntitlementAccount {
  return {
    available: a.available,
    benefitPeriodFrom: a.benefitPeriodFrom,
    benefitPeriodTo: a.benefitPeriodTo,
    consumed: a.consumed,
    definition: a.definition,
    enrollmentId: a.enrollmentId,
    expired: a.expired,
    id: a.id,
    personId: a.personId,
    reserved: a.reserved,
    rowVersion: a.rowVersion,
    shared,
    status: a.status,
    totalGranted: a.totalGranted,
  };
}

export interface ReachableAccount {
  account: StoredEntitlementAccount;
  shared: boolean;
}

/**
 * Accounts a person can reach on a date: their own enrollments' accounts, plus
 * family-shared accounts of the principal they are a FAMILY member under.
 */
export function reachableEntitlementAccounts(
  world: MockWorld,
  tenantId: string,
  personId: string,
  asOf: string,
): ReachableAccount[] {
  const inPeriod = (a: StoredEntitlementAccount) =>
    a.benefitPeriodFrom <= asOf && (a.benefitPeriodTo === null || asOf < a.benefitPeriodTo);
  const own = world.entitlementAccounts.filter(
    (a) => a.tenantId === tenantId && a.personId === personId && inPeriod(a),
  );
  const out: ReachableAccount[] = own.map((account) => ({ account, shared: false }));
  const ownIds = new Set(own.map((a) => a.id));
  const dependentMemberships = world.memberships.filter(
    (m) => m.tenantId === tenantId && m.personId === personId && m.principalMembershipId !== null,
  );
  for (const dependent of dependentMemberships) {
    const principal = world.memberships.find((m) => m.id === dependent.principalMembershipId);
    if (!principal) continue;
    const shared = world.entitlementAccounts.filter(
      (a) =>
        a.tenantId === tenantId &&
        a.personId === principal.personId &&
        a.definition.familyShared &&
        inPeriod(a) &&
        !ownIds.has(a.id),
    );
    for (const account of shared) {
      out.push({ account, shared: true });
    }
  }
  return out;
}

export type MockLedgerEntry = Omit<
  Schemas['LedgerEntry'],
  'deltaAvailable' | 'deltaConsumed' | 'deltaExpired' | 'deltaReserved' | 'deltaTotal'
> & {
  deltaAvailable: Decimal;
  deltaConsumed: Decimal;
  deltaExpired: Decimal;
  deltaReserved: Decimal;
  deltaTotal: Decimal;
};

export function toLedgerEntry(e: StoredLedgerEntry): MockLedgerEntry {
  return {
    createdBy: e.createdBy,
    deltaAvailable: e.deltaAvailable,
    deltaConsumed: e.deltaConsumed,
    deltaExpired: e.deltaExpired,
    deltaReserved: e.deltaReserved,
    deltaTotal: e.deltaTotal,
    effectiveAt: e.effectiveAt,
    id: e.id,
    movementType: e.movementType,
    reasonCode: e.reasonCode,
    reasonText: e.reasonText,
    referenceId: e.referenceId,
    referenceType: e.referenceType,
    reservationId: e.reservationId,
  };
}

export type MockEntitlementAdjustment = Omit<Schemas['EntitlementAdjustment'], 'deltaQuantity'> & {
  deltaQuantity: Decimal;
};

export function toEntitlementAdjustment(a: StoredAdjustment): MockEntitlementAdjustment {
  return {
    accountId: a.accountId,
    decidedAt: a.decidedAt,
    decidedBy: a.decidedBy,
    decisionComment: a.decisionComment,
    deltaQuantity: a.deltaQuantity,
    id: a.id,
    ledgerEntryId: a.ledgerEntryId,
    reasonCode: a.reasonCode,
    reasonText: a.reasonText,
    requestedAt: a.requestedAt,
    requestedBy: a.requestedBy,
    rowVersion: a.rowVersion,
    status: a.status,
  };
}

export function toImportBatch(b: StoredImportBatch): Schemas['MemberImportBatch'] {
  return {
    appliedAt: b.appliedAt,
    counters: b.counters,
    createdAt: b.createdAt,
    errorSummary: b.errorSummary,
    fileName: b.fileName,
    fileSha256: b.fileSha256,
    format: b.format,
    id: b.id,
    planId: b.planId,
    rowCount: b.rowCount,
    rowVersion: b.rowVersion,
    sourceSystem: b.sourceSystem,
    sourceVersion: b.sourceVersion,
    sponsorOrganizationId: b.sponsorOrganizationId,
    status: b.status,
  };
}

export function toImportRow(r: StoredImportRow): Schemas['MemberImportRow'] {
  return {
    appliedPersonId: r.appliedPersonId,
    birthDate: r.birthDate,
    candidatePersonIds: r.candidatePersonIds,
    decision: r.decision,
    displayName: r.displayName,
    errors: r.errors,
    id: r.id,
    identifiers: r.identifiers.map((i) => ({
      type: i.type,
      maskedValue: maskIdentifier(i.type as 'TCKN', i.value),
      primary: i.primary,
    })),
    matchedPersonId: r.matchedPersonId,
    membershipType: r.membershipType,
    planCode: r.planCode,
    principalSourceRecordId: r.principalSourceRecordId,
    rowNo: r.rowNo,
    rowVersion: r.rowVersion,
    sourceRecordId: r.sourceRecordId,
    status: r.status,
  };
}

export interface ImportCounters {
  conflict: number;
  created: number;
  invalid: number;
  matched: number;
  skipped: number;
  updated: number;
  valid: number;
}

/**
 * Parses the uploaded CSV_V1 file into rows, classifying each deterministically against
 * the tenant's existing people so the review screen always has at least one CONFLICT
 * (identifier matches an existing person with a different name) and one INVALID row
 * (identifier fails validation) when the test data is built that way. Header:
 * `sourceRecordId,firstName,lastName,birthDate,identifierType,identifierValue,membershipType,planCode,principalSourceRecordId`
 */
export function parseImportCsv(
  world: MockWorld,
  tenantId: string,
  importId: string,
  csvText: string,
): { rows: StoredImportRow[]; counters: ImportCounters } {
  const lines = csvText.split(/\r?\n/).filter((l) => l.trim().length > 0);
  const rows: StoredImportRow[] = [];
  const counters: ImportCounters = {
    conflict: 0,
    created: 0,
    invalid: 0,
    matched: 0,
    skipped: 0,
    updated: 0,
    valid: 0,
  };
  if (lines.length === 0) return { rows, counters };
  const header = lines[0]!.split(',').map((h) => h.trim());
  const idx = (name: string) => header.indexOf(name);
  const iSourceId = idx('sourceRecordId');
  const iFirst = idx('firstName');
  const iLast = idx('lastName');
  const iBirth = idx('birthDate');
  const iIdType = idx('identifierType');
  const iIdValue = idx('identifierValue');
  const iMembership = idx('membershipType');
  const iPlanCode = idx('planCode');
  const iPrincipalSource = idx('principalSourceRecordId');
  for (let rowNo = 1; rowNo < lines.length; rowNo++) {
    const cols = lines[rowNo]!.split(',').map((c) => c.trim());
    const get = (i: number): string => (i >= 0 && i < cols.length ? (cols[i] ?? '') : '');
    const sourceRecordId = get(iSourceId) || `ROW-${rowNo}`;
    const firstName = get(iFirst);
    const lastName = get(iLast);
    const birthDate = get(iBirth);
    const identifierType = get(iIdType) || 'TCKN';
    const identifierValue = get(iIdValue);
    const membershipType = get(iMembership);
    const planCode = get(iPlanCode);
    const principalSourceRecordId = get(iPrincipalSource);

    const errors: { code: string; field: string; message?: string }[] = [];
    if (!firstName || !lastName) {
      errors.push({ field: 'firstName', code: 'REQUIRED', message: 'Ad ve soyad zorunlu' });
    }
    if (identifierType === 'TCKN' && !isValidTCKN(identifierValue)) {
      errors.push({
        field: 'identifiers[0].value',
        code: 'IDENTIFIER_INVALID',
        message: 'TCKN kontrol basamağı hatalı',
      });
    }

    let status: StoredImportRow['status'];
    let matchedPersonId: string | null = null;
    const candidatePersonIds: string[] = [];
    if (errors.length > 0) {
      status = 'INVALID';
    } else {
      const existing = world.people.find(
        (p) =>
          p.tenantId === tenantId &&
          p.identifiers.some(
            (i) =>
              i.type === identifierType &&
              normalizeDigits(i.value) === normalizeDigits(identifierValue),
          ),
      );
      if (!existing) {
        status = 'VALID';
      } else {
        const sameName =
          existing.firstName.toLocaleLowerCase('tr') === firstName.toLocaleLowerCase('tr') &&
          existing.lastName.toLocaleLowerCase('tr') === lastName.toLocaleLowerCase('tr');
        if (sameName) {
          status = 'MATCHED';
          matchedPersonId = existing.id;
        } else {
          status = 'CONFLICT';
          candidatePersonIds.push(existing.id);
        }
      }
    }
    switch (status) {
      case 'INVALID':
        counters.invalid += 1;
        break;
      case 'VALID':
        counters.valid += 1;
        break;
      case 'MATCHED':
        counters.matched += 1;
        break;
      case 'CONFLICT':
        counters.conflict += 1;
        break;
      default:
        break;
    }

    rows.push({
      id: world.nextId(),
      tenantId,
      importId,
      rowNo,
      sourceRecordId,
      displayName: [firstName, lastName].filter(Boolean).join(' ') || sourceRecordId,
      birthDate: birthDate || null,
      identifiers: identifierValue
        ? [{ type: identifierType, value: identifierValue, primary: true }]
        : [],
      membershipType: membershipType || null,
      planCode: planCode || null,
      principalSourceRecordId: principalSourceRecordId || null,
      status,
      errors,
      candidatePersonIds,
      matchedPersonId,
      decision: null,
      appliedPersonId: null,
      rowVersion: 1,
    });
  }
  return { rows, counters };
}

// --- M3 projections -------------------------------------------------------------------

export function toServiceCategory(c: StoredServiceCategory): Schemas['ServiceCategory'] {
  return {
    id: c.id,
    parentId: c.parentId,
    code: c.code,
    name: c.name,
    domain: c.domain,
    active: c.active,
    rowVersion: c.rowVersion,
  };
}

export function toServiceDefinition(
  world: MockWorld,
  d: StoredServiceDefinition,
): Schemas['ServiceDefinition'] {
  const category = world.serviceCategories.find((c) => c.id === d.categoryId);
  return {
    id: d.id,
    categoryId: d.categoryId,
    categoryCode: category?.code ?? '',
    domain: category?.domain ?? 'GENERIC',
    code: d.code,
    name: d.name,
    description: d.description,
    fulfillmentMode: d.fulfillmentMode,
    defaultUnitType: d.defaultUnitType,
    requiresProvider: d.requiresProvider,
    active: d.active,
    rowVersion: d.rowVersion,
  };
}

/** The category itself and every ancestor above it, nearest first. */
export function categoryChain(world: MockWorld, categoryId: string): StoredServiceCategory[] {
  const chain: StoredServiceCategory[] = [];
  let current = world.serviceCategories.find((c) => c.id === categoryId);
  // The tree is at most six deep by contract; the guard stops a cycle a bad patch made.
  while (current && chain.length < 16) {
    chain.push(current);
    const parentId: string | null = current.parentId;
    current = parentId ? world.serviceCategories.find((c) => c.id === parentId) : undefined;
  }
  return chain;
}

/** Depth of a category, counting the root as 1. */
export function categoryDepth(world: MockWorld, categoryId: string): number {
  return categoryChain(world, categoryId).length;
}

/** True when `ancestorId` is the category itself or sits above it in the tree. */
export function categoryCovers(
  world: MockWorld,
  ancestorId: string,
  categoryId: string,
): number | null {
  const chain = categoryChain(world, categoryId);
  const index = chain.findIndex((c) => c.id === ancestorId);
  return index < 0 ? null : index;
}

export function toCodeSystem(s: StoredCodeSystem): Schemas['CodeSystem'] {
  return {
    id: s.id,
    code: s.code,
    name: s.name,
    version: s.version,
    authority: s.authority,
    licensed: s.licensed,
    status: s.status,
    validFrom: s.validFrom,
    validTo: s.validTo,
    rowVersion: s.rowVersion,
  };
}

export function toCodeValue(v: StoredCodeValue): Schemas['CodeValue'] {
  return {
    id: v.id,
    codeSystemId: v.codeSystemId,
    code: v.code,
    display: v.display,
    parentCode: v.parentCode,
    validFrom: v.validFrom,
    validTo: v.validTo,
    active: v.active,
    attributes: v.attributes,
  };
}

export function toServiceCodeMapping(
  world: MockWorld,
  m: StoredCodeMapping,
): Schemas['ServiceCodeMapping'] {
  const system = world.codeSystems.find((s) => s.id === m.codeSystemId);
  return {
    id: m.id,
    serviceDefinitionId: m.serviceDefinitionId,
    codeSystemId: m.codeSystemId,
    codeSystemCode: system?.code ?? '',
    codeSystemVersion: system?.version ?? '',
    code: m.code,
    validFrom: m.validFrom,
    validTo: m.validTo,
    primary: m.primary,
  };
}

/** Display name of the organization behind a tenant relationship. */
export function organizationNameOf(world: MockWorld, relationshipId: string): string {
  const rel = world.relationships.find((r) => r.id === relationshipId);
  if (!rel) return '';
  return world.organizations.get(rel.organizationId)?.displayName ?? '';
}

export function toProvider(world: MockWorld, p: StoredProvider): Schemas['Provider'] {
  return {
    id: p.id,
    tenantOrganizationId: p.tenantOrganizationId,
    organizationName: organizationNameOf(world, p.tenantOrganizationId),
    providerType: p.providerType,
    status: p.status,
    networkTier: p.networkTier,
    contractedFrom: p.contractedFrom,
    contractedTo: p.contractedTo,
    notes: p.notes,
    rowVersion: p.rowVersion,
  };
}

export function toProviderLocation(l: StoredProviderLocation): Schemas['ProviderLocation'] {
  return {
    id: l.id,
    providerId: l.providerId,
    code: l.code,
    name: l.name,
    addressLine: l.addressLine,
    district: l.district,
    city: l.city,
    countryCode: l.countryCode,
    postalCode: l.postalCode,
    latitude: l.latitude,
    longitude: l.longitude,
    timezone: l.timezone,
    phone: l.phone,
    status: l.status,
    rowVersion: l.rowVersion,
  };
}

export function toProviderCapability(
  world: MockWorld,
  c: StoredProviderCapability,
): Schemas['ProviderCapability'] {
  const definition = c.serviceDefinitionId
    ? world.serviceDefinitions.find((d) => d.id === c.serviceDefinitionId)
    : undefined;
  const category = c.serviceCategoryId
    ? world.serviceCategories.find((x) => x.id === c.serviceCategoryId)
    : undefined;
  return {
    id: c.id,
    locationId: c.locationId,
    serviceDefinitionId: c.serviceDefinitionId,
    serviceDefinitionCode: definition?.code ?? null,
    serviceCategoryId: c.serviceCategoryId,
    serviceCategoryCode: category?.code ?? null,
    validFrom: c.validFrom,
    validTo: c.validTo,
    notes: c.notes,
  };
}

export function toPractitionerLocation(
  world: MockWorld,
  l: StoredPractitionerLocation,
): Schemas['PractitionerLocation'] {
  const location = world.providerLocations.find((x) => x.id === l.locationId);
  return {
    id: l.id,
    practitionerId: l.practitionerId,
    locationId: l.locationId,
    locationCode: location?.code ?? '',
    locationName: location?.name ?? '',
    role: l.role,
    validFrom: l.validFrom,
    validTo: l.validTo,
  };
}

/** The registration number is masked here and nowhere else undone. */
export function toPractitioner(world: MockWorld, p: StoredPractitioner): Schemas['Practitioner'] {
  return {
    id: p.id,
    providerId: p.providerId,
    personId: p.personId,
    fullName: p.fullName,
    title: p.title,
    branchCode: p.branchCode,
    registrationAuthority: p.registrationAuthority,
    maskedRegistrationNumber: maskIdentifier('OTHER', p.registrationNumber),
    validFrom: p.validFrom,
    validTo: p.validTo,
    status: p.status,
    rowVersion: p.rowVersion,
    locations: p.locations.map((l) => toPractitionerLocation(world, l)),
  };
}

export function toContract(world: MockWorld, c: StoredContract): Schemas['Contract'] {
  const provider = world.providers.find((p) => p.id === c.providerProfileId);
  return {
    id: c.id,
    code: c.code,
    name: c.name,
    payerOrganizationId: c.payerOrganizationId,
    payerName: organizationNameOf(world, c.payerOrganizationId),
    providerProfileId: c.providerProfileId,
    providerName: provider ? organizationNameOf(world, provider.tenantOrganizationId) : null,
    sponsorOrganizationId: c.sponsorOrganizationId,
    sponsorName: c.sponsorOrganizationId
      ? organizationNameOf(world, c.sponsorOrganizationId)
      : null,
    domainCode: c.domainCode,
    status: c.status,
    rowVersion: c.rowVersion,
  };
}

export function toPriceList(world: MockWorld, l: StoredPriceList): Schemas['PriceList'] {
  return {
    id: l.id,
    contractVersionId: l.contractVersionId,
    code: l.code,
    name: l.name,
    priority: l.priority,
    seasonFrom: l.seasonFrom,
    seasonTo: l.seasonTo,
    weekdayMask: l.weekdayMask,
    itemCount: world.priceItems.filter((i) => i.priceListId === l.id).length,
    rowVersion: l.rowVersion,
  };
}

export function toContractVersionSummary(
  v: StoredContractVersion,
): Schemas['ContractVersionSummary'] {
  return {
    id: v.id,
    contractId: v.contractId,
    versionNo: v.versionNo,
    status: v.status,
    validFrom: v.validFrom,
    validTo: v.validTo,
    currencyCode: v.currencyCode,
    notes: v.notes,
    publishedAt: v.publishedAt,
    rowVersion: v.rowVersion,
  };
}

export function toContractVersion(
  world: MockWorld,
  v: StoredContractVersion,
): Schemas['ContractVersion'] {
  return {
    ...toContractVersionSummary(v),
    configurationHash: v.configurationHash,
    submittedAt: v.submittedAt,
    submittedBy: v.submittedBy,
    publishedBy: v.publishedBy,
    reviewComment: v.reviewComment,
    retireReasonCode: v.retireReasonCode,
    priceLists: world.priceLists
      .filter((l) => l.contractVersionId === v.id)
      .sort((a, b) => b.priority - a.priority || a.code.localeCompare(b.code, 'tr'))
      .map((l) => toPriceList(world, l)),
  };
}

export function toPriceItem(world: MockWorld, i: StoredPriceItem): Schemas['PriceItem'] {
  const definition = i.serviceDefinitionId
    ? world.serviceDefinitions.find((d) => d.id === i.serviceDefinitionId)
    : undefined;
  const category = i.serviceCategoryId
    ? world.serviceCategories.find((c) => c.id === i.serviceCategoryId)
    : undefined;
  const pkg = i.packageDefinitionId
    ? world.packageDefinitions.find((p) => p.id === i.packageDefinitionId)
    : undefined;
  return {
    id: i.id,
    priceListId: i.priceListId,
    serviceDefinitionId: i.serviceDefinitionId,
    serviceDefinitionCode: definition?.code ?? null,
    serviceCategoryId: i.serviceCategoryId,
    serviceCategoryCode: category?.code ?? null,
    packageDefinitionId: i.packageDefinitionId,
    packageDefinitionCode: pkg?.code ?? null,
    locationId: i.locationId,
    unitType: i.unitType,
    pricingMethod: i.pricingMethod,
    amount: i.amount,
    percent: i.percent,
    formulaKey: i.formulaKey,
    minAmount: i.minAmount,
    maxAmount: i.maxAmount,
    memberShareMethod: i.memberShareMethod,
    memberShareAmount: i.memberShareAmount,
    memberSharePercent: i.memberSharePercent,
    validFrom: i.validFrom,
    validTo: i.validTo,
    priority: i.priority,
  };
}

export function toPackageDefinition(
  world: MockWorld,
  p: StoredPackageDefinition,
): Schemas['PackageDefinition'] {
  return {
    id: p.id,
    contractVersionId: p.contractVersionId,
    code: p.code,
    name: p.name,
    inclusionRule: p.inclusionRule,
    minLines: p.minLines,
    lines: p.lines.map((l) => ({
      serviceDefinitionId: l.serviceDefinitionId,
      serviceDefinitionCode:
        world.serviceDefinitions.find((d) => d.id === l.serviceDefinitionId)?.code ?? null,
      includedQuantity: l.includedQuantity,
    })),
  };
}

export function toProviderQuota(q: StoredProviderQuota): Schemas['ProviderQuota'] {
  return {
    id: q.id,
    contractVersionId: q.contractVersionId,
    locationId: q.locationId,
    serviceDefinitionId: q.serviceDefinitionId,
    periodType: q.periodType,
    periodFrom: q.periodFrom,
    periodTo: q.periodTo,
    capacity: q.capacity,
    consumed: q.consumed,
    allowOverdraft: q.allowOverdraft,
  };
}

export function toPaymentTerm(t: StoredPaymentTerm): Schemas['PaymentTerm'] {
  return {
    id: t.id,
    contractVersionId: t.contractVersionId,
    dueDays: t.dueDays,
    settlementMethod: t.settlementMethod,
    taxBehaviour: t.taxBehaviour,
    vatRate: t.vatRate,
    lateFeePercent: t.lateFeePercent,
    rowVersion: t.rowVersion,
  };
}

export function toRuleSet(world: MockWorld, s: StoredRuleSet): Schemas['RuleSet'] {
  return {
    id: s.id,
    code: s.code,
    name: s.name,
    domainCode: s.domainCode,
    purpose: s.purpose,
    status: s.status,
    versionCount: world.ruleSetVersions.filter((v) => v.ruleSetId === s.id).length,
    rowVersion: s.rowVersion,
  };
}

export function toRule(versionId: string, r: StoredRule): Schemas['Rule'] {
  return {
    id: r.id,
    ruleSetVersionId: versionId,
    code: r.code,
    name: r.name,
    priority: r.priority,
    condition: r.condition,
    actions: r.actions,
    explanationCode: r.explanationCode,
    ...(r.explanationParams ? { explanationParams: r.explanationParams } : {}),
    stopOnMatch: r.stopOnMatch,
    active: r.active,
  };
}

export function toRuleTestCase(versionId: string, c: StoredRuleTestCase): Schemas['RuleTestCase'] {
  return {
    id: c.id,
    ruleSetVersionId: versionId,
    code: c.code,
    description: c.description,
    input: c.input,
    expectedOutcome: c.expectedOutcome,
    expectedExplanations: c.expectedExplanations,
    expectedActions: c.expectedActions,
  };
}

export function toRuleSetVersionSummary(v: StoredRuleSetVersion): Schemas['RuleSetVersionSummary'] {
  return {
    id: v.id,
    ruleSetId: v.ruleSetId,
    versionNo: v.versionNo,
    status: v.status,
    validFrom: v.validFrom,
    validTo: v.validTo,
    notes: v.notes,
    publishedAt: v.publishedAt,
    ruleCount: v.rules.length,
    testCaseCount: v.testCases.length,
    rowVersion: v.rowVersion,
  };
}

export function toRuleSetVersion(v: StoredRuleSetVersion): Schemas['RuleSetVersion'] {
  return {
    ...toRuleSetVersionSummary(v),
    inputSchema: v.inputSchema,
    contentHash: v.contentHash,
    submittedAt: v.submittedAt,
    submittedBy: v.submittedBy,
    publishedBy: v.publishedBy,
    reviewComment: v.reviewComment,
    retireReasonCode: v.retireReasonCode,
    rules: [...v.rules].sort((a, b) => a.priority - b.priority).map((r) => toRule(v.id, r)),
    testCases: v.testCases.map((c) => toRuleTestCase(v.id, c)),
  };
}
