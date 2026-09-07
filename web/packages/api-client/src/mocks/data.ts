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
  /**
   * Tenant codes the account is a member of, with permissions per tenant and the access
   * grants that narrow them. A grant of type ORGANIZATION is the provider boundary the
   * service request and document repositories apply; WORK_QUEUE is the worklist's. A
   * membership with no scope of a type is unrestricted for that type, exactly as a nil
   * slice is on the server.
   */
  memberships: {
    tenantCode: string;
    permissions: string[];
    scopes?: { type: string; id: string | null }[];
  }[];
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

/**
 * A service request without its lines: the lines belong to a version, exactly as they do
 * in the schema, and `items` on the wire is always the current version's line set.
 */
export type StoredServiceRequest = Omit<
  Schemas['ServiceRequest'],
  'items' | 'personDisplayName' | 'providerDisplayName'
> & {
  tenantId: string;
};

/** One line of one version. */
export type StoredServiceRequestItem = Schemas['ServiceRequestItem'];

/**
 * One version of a request's content. A SUBMITTED version is frozen and answers from
 * `snapshotItems`, which is what was sent, whatever the live rows say afterwards; a
 * SUPERSEDED one was replaced by a later version after a return.
 */
export interface StoredServiceRequestVersion {
  id: string;
  tenantId: string;
  serviceRequestId: string;
  versionNo: number;
  status: Schemas['ServiceRequestVersionStatus'];
  submittedAt: string | null;
  submittedBy: string | null;
  returnedAt: string | null;
  returnedBy: string | null;
  returnReasonCode: string | null;
  returnReasonText: string | null;
  createdAt: string;
  /** The live rows of this version; decisions are recorded on them. */
  items: StoredServiceRequestItem[];
  /** Frozen at submit; null on a version nobody has submitted yet. */
  snapshotItems: StoredServiceRequestItem[] | null;
}

/**
 * One row of benefit.service_entitlement_mapping (WP-I5-05 section 2.1): which entitlement
 * a catalogue service draws from inside one plan version, and how much of it one unit of
 * the service draws. The two codes the wire carries are derived on the way out, because a
 * code copied onto the row is a code that stops being true when the catalogue is renamed.
 */
export interface StoredEntitlementMapping {
  id: string;
  tenantId: string;
  planVersionId: string;
  serviceDefinitionId: string;
  entitlementDefinitionId: string;
  unitFactor: string;
  validFrom: string | null;
  validTo: string | null;
  rowVersion: number;
}

/**
 * One row of party.person_contact. The mock holds the plaintext the way it holds a
 * practitioner's registration number — it is a test double of a database it cannot
 * encrypt — and, exactly like the server, never lets it out: every projection masks it and
 * no endpoint returns it.
 */
export interface StoredPersonContact {
  id: string;
  tenantId: string;
  personId: string;
  channel: 'EMAIL' | 'SMS';
  value: string;
  verifiedAt: string | null;
  primary: boolean;
  createdAt: string;
  rowVersion: number;
}

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
  /**
   * The tenant setting the submit gate consults last: when nothing objected, does a
   * person still look at this? The server reads it out of platform.tenant_setting and
   * treats a missing answer as true, so the mock defaults it to true too.
   */
  reviewRequired: boolean;
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

// --- M4: worklist, documents and notifications -----------------------------------------

/** A place work waits, with the clock it hands out. */
export type StoredWorkQueue = Schemas['WorkQueue'] & { tenantId: string };

/**
 * One piece of work. `slaMinutesSnapshot` and `dueAt` are copied from the queue when the
 * item is raised and never read again: changing the queue's SLA leaves every existing
 * item on the clock it was already given, which is what makes a late item stay late.
 */
export type StoredWorkItem = Omit<Schemas['WorkItem'], 'assigneeDisplayName'> & {
  tenantId: string;
};

/** Append-only; a comment that could be edited is not a record of why anything happened. */
export type StoredWorkItemComment = Schemas['WorkItemComment'] & { tenantId: string };

/** One band of one action. Both amounts are exact decimal strings, never JSON numbers. */
export type StoredApprovalPolicy = Schemas['ApprovalPolicy'] & { tenantId: string };

/**
 * One document.object row. `links` and `downloadable` are computed on the way out — the
 * second in exactly one place, so "can this be fetched" has one answer — and `objectKey`
 * never leaves the mock, as it never leaves the server.
 */
export type StoredDocument = Omit<Schemas['Document'], 'links' | 'downloadable'> & {
  tenantId: string;
  objectKey: string;
};

export type StoredDocumentLink = Schemas['DocumentLink'] & { tenantId: string };

export type StoredLegalHold = Schemas['LegalHold'] & { tenantId: string };

export type StoredNotificationTemplate = Schemas['NotificationTemplate'] & { tenantId: string };

/** A message log row. A SUPPRESSED one always names its reason (migration 000029). */
export type StoredNotificationMessage = Schemas['NotificationMessage'] & { tenantId: string };

export type StoredNotificationDelivery = Schemas['NotificationDelivery'] & { tenantId: string };

export type StoredNotificationPreference = Schemas['NotificationPreference'] & {
  tenantId: string;
};

/** The verdict a scan may come back with; PENDING and SCANNING are states, not verdicts. */
/**
 * M5. The stored rows carry every column the database has, clinical fields included, exactly
 * as the Go repository does: the mock's health handlers apply the projection when they
 * answer, in one place, so a divergence between the two is a divergence in one function
 * rather than a field somebody forgot in a mapper.
 */
export interface StoredHealthCase {
  id: string;
  tenantId: string;
  personId: string;
  programId: string;
  enrollmentId: string;
  caseType: Schemas['HealthCaseType'];
  providerOrganizationId: string | null;
  openedAt: string;
  closedAt: string | null;
  status: Schemas['HealthCaseStatus'];
  /** Derived from the diagnoses the case carries; never sent by a caller. */
  sensitivity: Schemas['HealthCaseSensitivity'];
  serviceRequestId: string | null;
  createdAt: string;
  rowVersion: number;
}

export interface StoredEncounter {
  id: string;
  tenantId: string;
  caseId: string;
  encounterType: Schemas['EncounterType'];
  startedAt: string;
  endedAt: string | null;
  locationId: string | null;
  practitionerId: string | null;
  /** Clinical: served only in the clinical projection. */
  branchCode: string | null;
  /** Clinical: the one free-text clinical field in the module. */
  notesClinical: string | null;
  createdAt: string;
  rowVersion: number;
}

export interface StoredDiagnosis {
  id: string;
  tenantId: string;
  encounterId: string;
  codeSystemId: string;
  codeValueId: string;
  diagnosisType: Schemas['DiagnosisType'];
  /** Read from the code value's own attributes at write time. */
  sensitive: boolean;
  recordedAt: string;
  recordedBy: string | null;
}

export type StoredHealthAccessEvent = Schemas['HealthAccessEvent'] & { tenantId: string };

/**
 * One health.medical_report row, with every column the database has — the report type, the
 * clinical summary and the reviewer's comment included. The projection is applied when the
 * handlers answer, in one place, exactly as the Go repository leaves it to the service.
 *
 * A report has no sensitivity of its own: it is as sensitive as the case it hangs off, and a
 * report with no case is STANDARD. That is why there is no `sensitivity` column here.
 */
export interface StoredMedicalReport {
  id: string;
  tenantId: string;
  personId: string;
  caseId: string | null;
  /** Names the chain: every version of one report shares it. */
  reference: string;
  versionNo: number;
  rootReportId: string;
  supersedesReportId: string | null;
  /** Clinical: served only in the clinical projection. */
  reportType: string;
  /** Clinical. */
  reportSubtype: string | null;
  issuingPractitionerId: string | null;
  issuingProviderOrganizationId: string | null;
  issuedAt: string;
  validFrom: string;
  validTo: string;
  status: Schemas['MedicalReportStatus'];
  /** Clinical: the free text a doctor wrote. */
  clinicalSummary: string | null;
  /** Clinical: the free text a reviewer wrote. */
  reviewComment: string | null;
  rejectReasonCode: string | null;
  reviewedBy: string | null;
  reviewedAt: string | null;
  submittedAt: string | null;
  submittedBy: string | null;
  createdAt: string;
  rowVersion: number;
}

/** One covered service of a report. The amounts are exact decimals as strings. */
export interface StoredMedicalReportService {
  id: string;
  tenantId: string;
  reportId: string;
  serviceDefinitionId: string;
  coveredQuantity: string | null;
  coveredAmount: string | null;
  currencyCode: string | null;
  /** Clinical: the line a doctor wrote about this service for this person. */
  notes: string | null;
}

export type StoredMedicalReportUsage = Schemas['MedicalReportUsage'] & { tenantId: string };

/**
 * One health.inpatient_stay row, with every column the database has. The projection is
 * applied when the handlers answer, in one place, exactly as the Go repository leaves it to
 * the service.
 *
 * A stay has no sensitivity of its own: it is as sensitive as the case it hangs off, which is
 * why there is no `sensitivity` column here — the same reason a medical report has none.
 *
 * The three day counts are exact decimals as strings and are null until there is something to
 * say: `authorizedDays` until the request is decided, `actualDays` and `releasedDays` until
 * the discharge.
 */
export interface StoredInpatientStay {
  id: string;
  tenantId: string;
  caseId: string;
  personId: string;
  providerOrganizationId: string;
  locationId: string | null;
  attendingPractitionerId: string | null;
  admissionAt: string;
  estimatedDays: number;
  expectedDischargeAt: string;
  dischargeAt: string | null;
  status: Schemas['InpatientStayStatus'];
  serviceRequestId: string;
  authorizationId: string | null;
  /** Clinical: that an admission has a recorded diagnosis at all is a fact about the patient. */
  admissionDiagnosisId: string | null;
  authorizedDays: string | null;
  actualDays: string | null;
  releasedDays: string | null;
  overAuthorization: boolean;
  cancelReasonCode: string | null;
  createdAt: string;
  rowVersion: number;
}

/** One health.stay_extension row. `reasonText` is clinical; the code and the days are not. */
export interface StoredStayExtension {
  id: string;
  tenantId: string;
  stayId: string;
  sequenceNo: number;
  additionalDays: number;
  reasonCode: string;
  /** Clinical: served only in the clinical projection. */
  reasonText: string | null;
  serviceRequestId: string;
  authorizationId: string | null;
  status: Schemas['StayExtensionStatus'];
  createdAt: string;
  rowVersion: number;
}

/**
 * One health.stay_segment row. Nothing here is clinical in the projection's sense: where
 * somebody slept and for how long is what a claim is priced from, and a claims reviewer who
 * could not see an intensive care night could not check the bill for one.
 */
export interface StoredStaySegment {
  id: string;
  tenantId: string;
  stayId: string;
  segmentType: Schemas['StaySegmentType'];
  startsAt: string;
  endsAt: string | null;
  roomCode: string | null;
  bedCode: string | null;
  createdAt: string;
  rowVersion: number;
}

export type ScanVerdict = 'CLEAN' | 'INFECTED' | 'FAILED';

// --- M6: the accommodation vertical -----------------------------------------------------

/**
 * One row of accommodation.property (WP-I6-01). `providerOrganizationId` is the tenant's
 * organization *relationship* carrying the PROVIDER role — the same id every provider
 * boundary in this mock is read from — and never the bare organization, so a property and a
 * claim are narrowed by exactly the same grant.
 *
 * `timezone` is not decoration: a night begins and ends where the building is, and a stay
 * in a Berlin hotel booked by a Turkish payer is counted in Berlin nights.
 */
export interface StoredProperty {
  id: string;
  tenantId: string;
  providerOrganizationId: string;
  /** The provider location this building already is in the network; null for a facility. */
  locationId: string | null;
  code: string;
  name: string;
  propertyType: Schemas['PropertyType'];
  timezone: string;
  city: string | null;
  regionCode: string | null;
  amenities: Schemas['PropertyAmenity'][];
  /** The internal cost centre a facility the tenant runs itself charges (v1.2 9.13). */
  costCenter: string | null;
  status: Schemas['PropertyStatus'];
  createdAt: string;
  updatedAt: string | null;
  rowVersion: number;
}

/**
 * One sellable kind of room. `serviceDefinitionId` is the joint with the rest of the
 * platform: the room type *is* a catalogue service, so it is priced by the contract prices
 * of the pricing ladder and entitled through the service-to-entitlement mapping. It names a
 * NIGHT-unit service and is never re-pointed at another one.
 */
export interface StoredRoomType {
  id: string;
  tenantId: string;
  propertyId: string;
  code: string;
  name: string;
  maxAdults: number;
  maxChildren: number;
  maxOccupancy: number;
  /** The provider's own free-form facts about the room. It carries nothing about a guest. */
  attributes: Record<string, unknown>;
  serviceDefinitionId: string;
  status: Schemas['PropertyStatus'];
  createdAt: string;
  updatedAt: string | null;
  rowVersion: number;
}

/**
 * One night of one room type's allotment, and only for a night the provider has actually
 * opened: a date with no row is "no allotment" and not "zero free", which is why `available`
 * is computed rather than stored and why the search treats a missing night as unavailable.
 *
 * `held` and `confirmed` belong to the holds and bookings of WP-I6-02 and nothing in this
 * package writes them. `held + confirmed <= capacity` is a CHECK on the row on the server,
 * and no fixture or handler here is allowed to break it either.
 */
export interface StoredInventoryDay {
  id: string;
  tenantId: string;
  roomTypeId: string;
  stayDate: string;
  capacity: number;
  held: number;
  confirmed: number;
  updatedAt: string;
  rowVersion: number;
}

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
  // M5 (migration 000035). The mapping is part of a plan's configuration, so whoever may
  // write the entitlements may say which service draws from them; the contact grants are
  // SENSITIVE and separate from member.read for the same reason the identifier ones are.
  'entitlement.mapping.manage',
  'member.contact.read',
  'member.contact.manage',
  // A back-office administrator reads claims; deciding one is a reviewer's job.
  'claim.read',
  // Every move through the request lifecycle is its own grant: the person who asks for
  // something is not the person who grants it, so `review` is never implied by `create`.
  'service_request.read',
  'service_request.create',
  'service_request.submit',
  'service_request.review',
  'service_request.cancel',
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
  // M4 (migrations 000027-000029).
  'worklist.read',
  'worklist.claim',
  'worklist.reassign',
  'workflow.queue.manage',
  'workflow.policy.manage',
  'document.read',
  'document.upload',
  'document.link',
  'document.legal_hold.manage',
  'notification.read',
  'notification.manage',
  'identity.user.read',
  'identity.user.manage',
  'identity.role.manage',
  'audit.read',
  'report.read',
  'integration.manage',
  // M6 (migration 000040). The back-office administrator of the mock is a composite of
  // several Go roles, and reading a hotel and opening its allotment are two grants on
  // purpose: `accommodation.property.read` is what a member holds too, and
  // `accommodation.inventory.manage` is what it deliberately does not.
  'accommodation.property.read',
  'accommodation.inventory.manage',
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
  // A reviewer works a worklist and reads the documents attached to what it reviews. It
  // may not reassign somebody else's work and it holds no clinical grant.
  'worklist.read',
  'worklist.claim',
  'document.read',
  'notification.read',
  // The financial half of a claim review (WP-I5-04). No clinical grant sits beside it, which
  // is what makes the financial projection the one this account is served.
  'claim.read',
  'claim.financial.review',
];

/**
 * A provider-side actor: it may raise and submit requests for its own organization and
 * read its own documents, and the ORGANIZATION grant below is what stops it seeing
 * anybody else's. The boundary is applied where the repository applies it, in the list
 * and the read, so it can never disagree with the server by being a filter of its own.
 */
// PROVIDER_STAFF as internal/identity/application/roles.go grants it. The mock used to
// leave out member.read and eligibility.check, which let a provider screen pass here and
// be refused by the server — or, as it happened, the other way round.
const PROVIDER_PERMISSIONS = [
  'member.read',
  'eligibility.check',
  'service_request.read',
  'service_request.create',
  'service_request.submit',
  'service_request.cancel',
  'fulfilment.record',
  'voucher.redeem',
  'health.case.read',
  'health.case.manage',
  'health.clinical.read',
  'health.medical_report.manage',
  'document.upload',
  'document.read',
  'document.link',
  'pricing.quote',
  'organization.read',
  'catalog.read',
  'provider.read',
  // The provider's billing side (WP-I5-04): raise, send, and take back a claim.
  'claim.read',
  'claim.create',
  'claim.submit',
  'claim.cancel',
];

/**
 * SPONSOR_HR as internal/identity/application/roles.go grants it, and no wider. This is the
 * role WP-I5-01 exists for: it may see that a member has an open health case and may never
 * see what the case is about. health.clinical.read is absent on purpose, and m5.test.ts
 * asserts the absence rather than trusting this list to stay short.
 */
const SPONSOR_HR_PERMISSIONS = [
  'member.read',
  'service_request.read',
  'health.case.read',
  // It reads claims and is served the financial projection of every one of them: the money
  // and the process, and never a description, a diagnosis or a reviewer's clinical sentence.
  'claim.read',
  'entitlement.read',
  'report.read',
];

/**
 * MEDICAL_REVIEWER's health half: clinical detail, the sensitive-category grant and the
 * treatment report review. It deliberately does not hold health.medical_report.manage —
 * writing a report is the provider's job and deciding about one is the payer's, and a role
 * holding both would be a provider approving its own reports.
 */
const MEDICAL_REVIEWER_PERMISSIONS = [
  'member.read',
  'service_request.read',
  'service_request.review',
  'health.case.read',
  'health.clinical.read',
  'health.sensitive.read',
  'health.medical_report.review',
  // The clinical half of a claim review (WP-I5-04).
  'claim.read',
  'claim.medical.review',
  'document.read',
  'worklist.read',
  'worklist.claim',
  'audit.read',
];

/**
 * FINANCIAL_REVIEWER exactly as internal/identity/application/roles.go grants it, in the
 * same order, and m5.test.ts asserts the two lists are the same rather than trusting this
 * copy. It is the account WP-I5-04's financial stage exists for: it reviews the money on a
 * claim and holds no clinical grant at all, so every claim, report and stay it reads
 * arrives in the financial projection — which is the point, not a limitation to work
 * around by adding health.clinical.read here.
 */
const FINANCIAL_REVIEWER_PERMISSIONS = [
  'member.read',
  'service_request.read',
  'claim.read',
  'claim.financial.review',
  'invoice.read',
  'invoice.manage',
  'batch.review',
  'settlement.read',
  'fiscal.edocument.read',
  'fiscal.edocument.match',
  'accounting.posting.read',
  'document.read',
  'document.link',
  'pricing.quote',
  'report.read',
  'worklist.read',
  'worklist.claim',
];

/**
 * PROVIDER_BILLING exactly as internal/identity/application/roles.go grants it, in the same
 * order, asserted against the Go list by m5.test.ts. It is the provider's billing desk:
 * it raises, sends and takes back a claim and follows the invoice, and it holds neither
 * health.case.read nor any clinical grant — the clinic side of the same organization is
 * PROVIDER_STAFF, and keeping the two apart is what stops a billing clerk reading a
 * diagnosis.
 */
const PROVIDER_BILLING_PERMISSIONS = [
  'claim.read',
  'claim.create',
  'claim.submit',
  'claim.cancel',
  'invoice.read',
  'invoice.manage',
  'batch.create',
  'batch.submit',
  'settlement.read',
  'fiscal.edocument.read',
  'document.read',
  'document.link',
];

/**
 * PROVIDER_RESERVATION exactly as internal/identity/application/roles.go grants it, in the
 * same order. It is the provider's reservation desk: it maintains the hotel and its
 * allotment and holds no clinical grant at all — a room is a building, not a person, and
 * the desk that opens ninety nights of it has no business reading a diagnosis.
 */
const PROVIDER_RESERVATION_PERMISSIONS = [
  'accommodation.property.read',
  'accommodation.inventory.manage',
  'accommodation.booking.manage',
  'member.read',
  'eligibility.check',
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
/**
 * The claim (WP-I5-04). Five arrays rather than one nested shape, because that is what the
 * schema is: a version freezes, its lines hang off it, and a line's decisions are append-only
 * — a line decided twice has two rows and the latest is the decision.
 */
export interface StoredClaim {
  id: string;
  tenantId: string;
  reference: string;
  personId: string;
  programId: string;
  enrollmentId: string;
  providerOrganizationId: string;
  domainCode: string;
  caseId: string | null;
  fulfilmentId: string | null;
  authorizationId: string | null;
  currentVersionNo: number;
  status: Schemas['ClaimStatus'];
  serviceDateFrom: string;
  serviceDateTo: string;
  channel: Schemas['ServiceRequestChannel'];
  rejectReasonCode: string | null;
  returnReasonCode: string | null;
  /** Clinical: served only in the clinical projection. */
  reviewCommentMedical: string | null;
  reviewCommentFinancial: string | null;
  closedAt: string | null;
  createdAt: string;
  rowVersion: number;
}

export interface StoredClaimVersion {
  id: string;
  tenantId: string;
  claimId: string;
  versionNo: number;
  status: Schemas['ClaimVersionStatus'];
  submittedAt: string | null;
  submittedBy: string | null;
  returnedAt: string | null;
  returnedBy: string | null;
  returnReasonCode: string | null;
  returnReasonText: string | null;
  /**
   * What the submit decided about routing and what it found, frozen. `financialRequired` is
   * what makes "medical first, then financial" survive the medical stage.
   */
  financialRequired: boolean;
  exceptions: Schemas['ClaimException'][];
  createdAt: string;
  rowVersion: number;
}

export interface StoredClaimLine {
  id: string;
  tenantId: string;
  versionId: string;
  lineNo: number;
  serviceDefinitionId: string;
  unitType: string;
  quantity: string;
  unitAmount: string | null;
  lineAmount: string;
  currencyCode: string;
  /** The three clinical fields the financial projection drops. */
  diagnosisId: string | null;
  medicalReportId: string | null;
  practitionerId: string | null;
  description: string | null;
  createdAt: string;
  rowVersion: number;
}

export interface StoredClaimLineDecision {
  id: string;
  tenantId: string;
  lineId: string;
  decidedInVersionNo: number;
  decision: Schemas['ClaimDecisionKind'];
  approvedQuantity: string;
  approvedAmount: string;
  contractAmount: string | null;
  payerAmount: string;
  memberAmount: string;
  reasonCode: string;
  reasonText: string | null;
  decidedBy: string | null;
  decidedAt: string;
  stage: Schemas['ClaimDecisionStage'];
}

export interface StoredClaimAdjustment {
  id: string;
  tenantId: string;
  claimId: string;
  versionNo: number;
  adjustmentType: 'CUT' | 'RECOVERY' | 'CORRECTION';
  amount: string;
  currencyCode: string;
  reasonCode: string;
  reasonText: string | null;
  createdBy: string | null;
  createdAt: string;
}

/**
 * The hold a claim draws on, as much of it as the claim needs. WP-I4-02 has no mock surface of
 * its own — nothing in this file serves /api/v1/authorizations — so this is the smallest honest
 * stand-in: an approved quantity per service and what has been drawn from it. It exists so the
 * one rule the claim owns can be exercised, which is that an over-consumption is an exception
 * and **nothing moves**, not even the part that was left.
 */
export interface StoredClaimAuthorization {
  id: string;
  tenantId: string;
  reference: string;
  personId: string;
  items: { serviceDefinitionId: string; approvedQuantity: string; consumedQuantity: string }[];
}

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
  // M4.
  serviceRequestVersions: StoredServiceRequestVersion[];
  workQueues: StoredWorkQueue[];
  workItems: StoredWorkItem[];
  workItemComments: StoredWorkItemComment[];
  approvalPolicies: StoredApprovalPolicy[];
  documents: StoredDocument[];
  documentLinks: StoredDocumentLink[];
  legalHolds: StoredLegalHold[];
  notificationTemplates: StoredNotificationTemplate[];
  notificationMessages: StoredNotificationMessage[];
  notificationDeliveries: StoredNotificationDelivery[];
  notificationPreferences: StoredNotificationPreference[];
  // M5.
  entitlementMappings: StoredEntitlementMapping[];
  personContacts: StoredPersonContact[];
  healthCases: StoredHealthCase[];
  encounters: StoredEncounter[];
  diagnoses: StoredDiagnosis[];
  /**
   * The clinical access log. The handlers append to it exactly where the Go service writes
   * an audit.access_event, so "who looked and why" is answerable in the mock too — and a
   * screen that shows it has something to show.
   */
  healthAccessEvents: StoredHealthAccessEvent[];
  medicalReports: StoredMedicalReport[];
  medicalReportServices: StoredMedicalReportService[];
  /**
   * The trace v1.2 10.5 step 6 asks for: which request, authorization or claim leaned on
   * which version of a report. Append-only in the schema and treated as append-only here.
   */
  medicalReportUsages: StoredMedicalReportUsage[];
  /**
   * The inpatient stay, its extensions and its segments (WP-I5-03). They are three arrays
   * rather than one nested shape because that is what the schema is: an extension and a
   * segment each have their own lifecycle, and a screen pages the stays without ever loading
   * every segment of every one of them.
   */
  inpatientStays: StoredInpatientStay[];
  stayExtensions: StoredStayExtension[];
  staySegments: StoredStaySegment[];
  /**
   * The claim and everything that hangs off it (WP-I5-04). `claimLineDecisions` is treated as
   * append-only here exactly as the schema treats it: nothing below ever edits a row, and the
   * latest row for a line is the decision.
   */
  claims: StoredClaim[];
  claimVersions: StoredClaimVersion[];
  claimLines: StoredClaimLine[];
  claimLineDecisions: StoredClaimLineDecision[];
  claimAdjustments: StoredClaimAdjustment[];
  claimAuthorizations: StoredClaimAuthorization[];
  // M6.
  /**
   * The accommodation vertical (WP-I6-01): the buildings, the kinds of room in them and the
   * nights a provider has opened on each. Three arrays rather than one nested shape because
   * that is what the schema is — and because `inventoryDays` is ninety rows per room type,
   * which no property read should ever have to carry.
   *
   * `inventoryDays` holds only the nights that exist. The gaps are the point: a screen and
   * the availability search both have to tell "this provider opened nothing here" apart from
   * "this is full", and a fixture that pre-filled every date with zeroes would hide the
   * difference the whole search turns on.
   */
  properties: StoredProperty[];
  roomTypes: StoredRoomType[];
  inventoryDays: StoredInventoryDay[];
  /**
   * Moves a document on from SCANNING the way the scan worker does. It is the mock's
   * stand-in for the worker, so a screen can show "taranıyor" and then a verdict without
   * the fixture having to guess which one it will be. Only the worker ever writes CLEAN,
   * INFECTED or FAILED — no endpoint does, on the server or here.
   */
  advanceScan: (documentId: string, verdict: ScanVerdict) => StoredDocument | null;
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
      username: 'provider.a',
      displayName: 'Pelin Sağlayıcı',
      email: 'provider.a@example.invalid',
      // The ORGANIZATION grant is filled in below, once the provider relationship exists.
      memberships: [{ tenantCode: 'DEMO_A', permissions: PROVIDER_PERMISSIONS, scopes: [] }],
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
    // Nothing objected means approved here, so the gate has an APPROVED branch to take.
    reviewRequired: false,
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
    // A program that always wants a pair of eyes, so PROGRAM_REVIEW_REQUIRED is reachable.
    reviewRequired: true,
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
  // The spouse holds exactly one enrollment, which is what makes a clean gate run
  // possible: two active enrollments in the same program is itself a REVIEW_REQUIRED
  // answer, and a fixture with only that shape would hide the APPROVED branch.
  const spouseEnrollment: StoredEnrollment = {
    id: nextId(),
    tenantId: demoA.id,
    personId: familySpouse.id,
    planId: planIndividualHealth.id,
    planCode: planIndividualHealth.code,
    programId: programHealth.id,
    sponsorMembershipId: memberships.find((m) => m.personId === familySpouse.id)!.id,
    status: 'ACTIVE',
    validFrom: isoDaysAgo(base, 180).slice(0, 10),
    validTo: null,
    enrollmentReason: null,
    sourceSystem: null,
    rowVersion: 1,
  };
  const enrollments: StoredEnrollment[] = [
    familyEnrollment,
    individualEnrollment,
    spouseEnrollment,
  ];

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
  const spouseSessionAccount: StoredEntitlementAccount = {
    id: nextId(),
    tenantId: demoA.id,
    enrollmentId: spouseEnrollment.id,
    personId: familySpouse.id,
    definition: toAccountDefinition(sessionDef),
    benefitPeriodFrom: isoDaysAgo(base, 180).slice(0, 10),
    benefitPeriodTo: null,
    totalGranted: '12.000000',
    available: '12.000000',
    consumed: '0.000000',
    reserved: '0.000000',
    expired: '0.000000',
    status: 'OPEN',
    rowVersion: 1,
  };
  const entitlementAccounts: StoredEntitlementAccount[] = [
    familyMoneyAccount,
    familySessionAccount,
    frozenAccount,
    spouseSessionAccount,
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
  // The provider-side actor is granted exactly this organization. Everything it may see is
  // decided by this one row: there is no second, client-side filter that could disagree.
  accounts
    .find((a) => a.username === 'provider.a')!
    .memberships[0]!.scopes!.push({
      type: 'ORGANIZATION',
      id: providerRel.id,
    });

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
  /**
   * The gate's own rule set. It is separate from HLT_DOCUMENTS on purpose: these rules are
   * written against the variables the submit gate supplies — the whole request rather than
   * one line — and those are written against the ones an author supplies by hand in a
   * simulation. Neither set can match the other's input, so keeping them apart is what
   * stops a simulation of one showing the other's rules.
   */
  const ruleSetGate: StoredRuleSet = {
    id: nextId(),
    tenantId: demoA.id,
    code: 'SR_SUBMIT_GATE',
    name: 'Talep Gönderim Kapısı',
    domainCode: 'HEALTH',
    purpose: 'PREAUTH',
    status: 'ACTIVE',
    rowVersion: 1,
  };
  ruleSets.push(ruleSetGate);

  const publishedGateVersion: StoredRuleSetVersion = {
    id: nextId(),
    tenantId: demoA.id,
    ruleSetId: ruleSetGate.id,
    versionNo: 1,
    status: 'PUBLISHED',
    validFrom: '2026-01-01',
    validTo: null,
    inputSchema: {
      requestType: 'string',
      totalAmount: 'double',
      totalQuantity: 'double',
      eligibilityOutcome: 'string',
    },
    notes: null,
    contentHash: pseudoHash(`${ruleSetGate.code}:1`),
    submittedAt: isoDaysAgo(base, 200),
    submittedBy: accounts[0]!.actorId,
    publishedAt: isoDaysAgo(base, 199),
    publishedBy: accounts[3]!.actorId,
    reviewComment: null,
    retireReasonCode: null,
    rules: [
      {
        id: nextId(),
        code: 'GATE_PREAUTH_REPORT',
        name: 'Ön onay için rapor ve fatura',
        priority: 30,
        condition: 'requestType == "PREAUTHORIZATION"',
        actions: [
          {
            type: 'REQUIRE_DOCUMENT',
            payload: { documentTypeCodes: ['MEDICAL_REPORT', 'INVOICE'] },
          },
        ],
        explanationCode: 'DOCUMENT_REQUIRED',
        explanationParams: null,
        stopOnMatch: false,
        active: true,
      },
      {
        id: nextId(),
        code: 'GATE_HIGH_TOTAL_REVIEW',
        name: 'Yüksek toplam tutar mali inceleme',
        priority: 40,
        condition: 'totalAmount > 20000',
        actions: [{ type: 'REQUIRE_FINANCIAL_REVIEW' }],
        explanationCode: 'AMOUNT_ABOVE_THRESHOLD',
        explanationParams: null,
        stopOnMatch: false,
        active: true,
      },
    ],
    testCases: [],
    rowVersion: 2,
  };

  const ruleSetVersions: StoredRuleSetVersion[] = [publishedRuleVersion, publishedGateVersion];

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

  // === M4 =============================================================================
  // Service requests with their versions, the worklist, the document pipeline and the
  // notification log. Everything below honours the CHECK constraints of migrations
  // 000025 and 000027-000029: a REJECTED request names a reject reason, a
  // PENDING_DOCUMENT one names the document types it is waiting for, a CLAIMED work item
  // names an assignee, a document is only in the secure bucket once its scan came back
  // CLEAN, and a SUPPRESSED message names why nobody was told.

  const reviewerActorId = accounts.find((a) => a.username === 'reviewer.a')!.actorId;
  const adminActorId = accounts[0]!.actorId;
  const providerActorId = accounts.find((a) => a.username === 'provider.a')!.actorId;
  // A second provider, so "only my own" is a statement with something to exclude.
  const otherProviderRel = relationships.find(
    (r) => r.tenantId === demoA.id && r.relationshipRole === 'PROVIDER' && r.id !== providerRel.id,
  )!;

  const REFERENCE_ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  /** The server's reference shape: the day it was raised plus eight random base32 chars. */
  function requestReference(createdAt: string): string {
    let tail = '';
    for (let i = 0; i < 8; i += 1) {
      tail += REFERENCE_ALPHABET[Math.floor(random() * REFERENCE_ALPHABET.length)]!;
    }
    return `SR-${createdAt.slice(0, 10).replace(/-/g, '')}-${tail}`;
  }

  /** Money as an exact decimal string, built from kuruş so no float is ever involved. */
  const lira = (kurus: number): Decimal => fromMicros(BigInt(kurus) * 10_000n);

  const serviceRequests: StoredServiceRequest[] = [];
  const serviceRequestVersions: StoredServiceRequestVersion[] = [];

  interface SeedLine {
    serviceDefinitionId: string;
    unitType: Schemas['ServiceUnitType'];
    requestedQuantity: Decimal;
    requestedAmount: Decimal | null;
    currencyCode: string | null;
    status: Schemas['ServiceRequestItemStatus'];
    approvedQuantity: Decimal | null;
    approvedAmount: Decimal | null;
    decisionReasonCode: string | null;
  }

  const line = (
    definitionId: string,
    unitType: Schemas['ServiceUnitType'],
    quantity: number,
    kurus: number | null,
    decided: Partial<SeedLine> = {},
  ): SeedLine => ({
    serviceDefinitionId: definitionId,
    unitType,
    requestedQuantity: toDecimal(quantity),
    requestedAmount: kurus === null ? null : lira(kurus),
    currencyCode: kurus === null ? null : 'TRY',
    status: 'REQUESTED',
    approvedQuantity: null,
    approvedAmount: null,
    decisionReasonCode: null,
    ...decided,
  });

  interface SeedRequest {
    tenantId: string;
    daysAgo: number;
    personId: string;
    programId: string;
    enrollmentId: string;
    providerOrganizationId: string | null;
    requestType: Schemas['ServiceRequestType'];
    channel: Schemas['ServiceRequestChannel'];
    status: Schemas['ServiceRequestStatus'];
    lines: SeedLine[];
    requiredDocumentTypes?: string[] | null;
    rejectReasonCode?: string | null;
    reviewComment?: string | null;
    /** When set, version 1 was sent back and version 2 is the draft being corrected. */
    returned?: { reasonCode: string; reasonText: string };
    supersedesRequestId?: string | null;
  }

  function seedRequest(seed: SeedRequest): StoredServiceRequest {
    const createdAt = isoDaysAgo(base, seed.daysAgo);
    const id = nextId(seed.daysAgo * -86_400_000);
    const decided =
      seed.status === 'APPROVED' ||
      seed.status === 'PARTIALLY_APPROVED' ||
      seed.status === 'REJECTED' ||
      seed.status === 'CANCELLED' ||
      seed.status === 'EXPIRED' ||
      seed.status === 'CLOSED';
    const submitted =
      decided ||
      seed.status === 'PENDING_REVIEW' ||
      seed.status === 'PENDING_DOCUMENT' ||
      seed.status === 'ELIGIBILITY_FAILED';
    const submittedAt = submitted ? isoDaysAgo(base, seed.daysAgo - 1) : null;
    const items: StoredServiceRequestItem[] = seed.lines.map((l, i) => ({
      id: nextId(),
      lineNo: i + 1,
      serviceDefinitionId: l.serviceDefinitionId,
      unitType: l.unitType,
      requestedQuantity: l.requestedQuantity,
      requestedAmount: l.requestedAmount,
      currencyCode: l.currencyCode,
      status: l.status,
      approvedQuantity: l.approvedQuantity,
      approvedAmount: l.approvedAmount,
      decisionReasonCode: l.decisionReasonCode,
    }));
    const request: StoredServiceRequest = {
      tenantId: seed.tenantId,
      id,
      reference: requestReference(createdAt),
      personId: seed.personId,
      programId: seed.programId,
      enrollmentId: seed.enrollmentId,
      providerOrganizationId: seed.providerOrganizationId,
      requestType: seed.requestType,
      channel: seed.channel,
      status: seed.status,
      serviceDate: isoDaysAgo(base, seed.daysAgo + 2).slice(0, 10),
      requestedStartAt: null,
      requestedEndAt: null,
      submittedAt,
      closedAt: decided ? isoDaysAgo(base, seed.daysAgo - 2) : null,
      createdAt,
      currentVersionNo: seed.returned ? 2 : 1,
      supersedesRequestId: seed.supersedesRequestId ?? null,
      eligibilityEvaluationId: submitted ? nextId() : null,
      ruleEvaluationId: submitted && seed.status !== 'ELIGIBILITY_FAILED' ? nextId() : null,
      // Tri-state, exactly as the server keeps it: null is "the rules were never asked",
      // [] is "asked and nothing needed", and a list is what a rule named.
      requiredDocumentTypes:
        seed.requiredDocumentTypes !== undefined
          ? seed.requiredDocumentTypes
          : seed.status === 'PENDING_DOCUMENT'
            ? ['INVOICE', 'MEDICAL_REPORT']
            : submitted && seed.status !== 'ELIGIBILITY_FAILED'
              ? []
              : null,
      returnReasonCode: seed.returned?.reasonCode ?? null,
      rejectReasonCode:
        seed.rejectReasonCode ?? (seed.status === 'REJECTED' ? 'NOT_COVERED_BY_PLAN' : null),
      reviewComment: seed.reviewComment ?? null,
      rowVersion: seed.returned ? 3 : submitted ? 2 : 1,
    };
    serviceRequests.push(request);

    const first: StoredServiceRequestVersion = {
      id: nextId(),
      tenantId: seed.tenantId,
      serviceRequestId: id,
      versionNo: 1,
      status: seed.returned ? 'SUPERSEDED' : submitted ? 'SUBMITTED' : 'DRAFT',
      submittedAt: seed.returned ? isoDaysAgo(base, seed.daysAgo - 1) : submittedAt,
      submittedBy: seed.returned || submitted ? providerActorId : null,
      returnedAt: seed.returned ? isoDaysAgo(base, seed.daysAgo - 2) : null,
      returnedBy: seed.returned ? reviewerActorId : null,
      returnReasonCode: seed.returned?.reasonCode ?? null,
      returnReasonText: seed.returned?.reasonText ?? null,
      createdAt,
      items,
      // A submitted version answers from what was frozen at submit, so it never drifts.
      snapshotItems: seed.returned || submitted ? items.map((i) => ({ ...i })) : null,
    };
    serviceRequestVersions.push(first);

    if (seed.returned) {
      // The correction is a new version carrying fresh REQUESTED lines: the decisions on
      // the version that was sent back were about that version.
      serviceRequestVersions.push({
        id: nextId(),
        tenantId: seed.tenantId,
        serviceRequestId: id,
        versionNo: 2,
        status: 'DRAFT',
        submittedAt: null,
        submittedBy: null,
        returnedAt: isoDaysAgo(base, seed.daysAgo - 2),
        returnedBy: reviewerActorId,
        returnReasonCode: seed.returned.reasonCode,
        returnReasonText: seed.returned.reasonText,
        createdAt: isoDaysAgo(base, seed.daysAgo - 2),
        items: items.map((i, n): StoredServiceRequestItem => ({
          id: nextId(),
          lineNo: n + 1,
          serviceDefinitionId: i.serviceDefinitionId,
          unitType: i.unitType,
          requestedQuantity: i.requestedQuantity,
          requestedAmount: i.requestedAmount ?? null,
          currencyCode: i.currencyCode ?? null,
          status: 'REQUESTED',
          approvedQuantity: null,
          approvedAmount: null,
          decisionReasonCode: null,
        })),
        snapshotItems: null,
      });
    }
    return request;
  }

  const physioLine = (quantity: number, kurus: number, decided?: Partial<SeedLine>) =>
    line(defPhysio.id, 'SESSION', quantity, kurus, decided);
  const gpLine = (decided?: Partial<SeedLine>) => line(defGpVisit.id, 'COUNT', 1, 90_000, decided);

  const commonSeed = {
    tenantId: demoA.id,
    personId: familyPrincipal.id,
    programId: programHealth.id,
    enrollmentId: familyEnrollment.id,
    providerOrganizationId: providerRel.id,
    requestType: 'DIRECT_SERVICE' as const,
    channel: 'PROVIDER_PORTAL' as const,
  };

  // One request in every status a stored row may hold. SUBMITTED is deliberately absent:
  // the server passes through it inside the submit transaction and lands on one of the
  // gate's four outcomes, so no row is ever observably SUBMITTED, and seeding one would
  // show a screen a state the product never shows it.
  seedRequest({ ...commonSeed, daysAgo: 30, status: 'DRAFT', lines: [physioLine(2, 120_000)] });
  seedRequest({
    ...commonSeed,
    daysAgo: 28,
    status: 'PENDING_REVIEW',
    lines: [physioLine(4, 480_000)],
    reviewComment: 'Tutar eşiği aşıldı.',
  });
  seedRequest({
    ...commonSeed,
    daysAgo: 26,
    requestType: 'PREAUTHORIZATION',
    status: 'PENDING_DOCUMENT',
    lines: [physioLine(8, 960_000)],
  });
  seedRequest({
    ...commonSeed,
    daysAgo: 24,
    status: 'ELIGIBILITY_FAILED',
    lines: [physioLine(40, 4_800_000)],
  });
  seedRequest({
    ...commonSeed,
    daysAgo: 22,
    status: 'APPROVED',
    lines: [
      physioLine(2, 240_000, {
        status: 'APPROVED',
        approvedQuantity: toDecimal(2),
        approvedAmount: lira(240_000),
        decisionReasonCode: 'WITHIN_PLAN',
      }),
    ],
  });
  seedRequest({
    ...commonSeed,
    daysAgo: 20,
    status: 'PARTIALLY_APPROVED',
    lines: [
      physioLine(6, 720_000, {
        status: 'PARTIALLY_APPROVED',
        approvedQuantity: toDecimal(4),
        approvedAmount: lira(480_000),
        decisionReasonCode: 'SESSION_CAP',
      }),
      gpLine({
        status: 'REJECTED',
        approvedQuantity: toDecimal(0),
        decisionReasonCode: 'NOT_COVERED_BY_PLAN',
      }),
    ],
  });
  const rejectedRequest = seedRequest({
    ...commonSeed,
    daysAgo: 18,
    status: 'REJECTED',
    rejectReasonCode: 'NOT_COVERED_BY_PLAN',
    lines: [
      gpLine({
        status: 'REJECTED',
        approvedQuantity: toDecimal(0),
        decisionReasonCode: 'NOT_COVERED_BY_PLAN',
      }),
    ],
  });
  // "We asked again, differently" is a new request naming the refused one, never the old
  // row changing its mind.
  seedRequest({
    ...commonSeed,
    daysAgo: 17,
    status: 'PENDING_REVIEW',
    supersedesRequestId: rejectedRequest.id,
    lines: [gpLine()],
  });
  seedRequest({
    ...commonSeed,
    daysAgo: 16,
    status: 'CANCELLED',
    lines: [physioLine(2, 240_000, { status: 'CANCELLED', decisionReasonCode: 'MEMBER_WITHDREW' })],
  });
  seedRequest({
    ...commonSeed,
    daysAgo: 14,
    requestType: 'RESERVATION',
    status: 'EXPIRED',
    lines: [physioLine(1, 120_000)],
  });
  seedRequest({
    ...commonSeed,
    daysAgo: 12,
    status: 'CLOSED',
    lines: [
      physioLine(3, 360_000, {
        status: 'APPROVED',
        approvedQuantity: toDecimal(3),
        approvedAmount: lira(360_000),
        decisionReasonCode: 'WITHIN_PLAN',
      }),
    ],
  });
  // A returned request: version 1 stays readable exactly as it was submitted, version 2 is
  // the draft being corrected, and the reference survives both.
  seedRequest({
    ...commonSeed,
    daysAgo: 10,
    status: 'DRAFT',
    lines: [physioLine(5, 600_000)],
    returned: { reasonCode: 'MISSING_INVOICE', reasonText: 'Fatura okunaksız, yeniden yükleyin.' },
  });
  // The other provider's request. It is on nobody's page but its own provider's.
  seedRequest({
    ...commonSeed,
    daysAgo: 9,
    providerOrganizationId: otherProviderRel.id,
    status: 'PENDING_REVIEW',
    lines: [gpLine()],
  });
  // A tenant-side request naming no provider at all: invisible to any provider-scoped
  // actor, because a grant that names organizations matches no row that names none.
  seedRequest({
    ...commonSeed,
    daysAgo: 8,
    providerOrganizationId: null,
    channel: 'BACKOFFICE',
    status: 'PENDING_REVIEW',
    lines: [gpLine()],
  });

  // Volume, so the list pages against something.
  for (let i = 0; i < 20; i += 1) {
    seedRequest({
      ...commonSeed,
      daysAgo: 60 - i,
      status: i % 3 === 0 ? 'APPROVED' : i % 3 === 1 ? 'PENDING_REVIEW' : 'CLOSED',
      providerOrganizationId: i % 4 === 0 ? otherProviderRel.id : providerRel.id,
      lines: [
        physioLine(
          1 + (i % 4),
          120_000 * (1 + (i % 4)),
          i % 3 === 1
            ? {}
            : {
                status: 'APPROVED',
                approvedQuantity: toDecimal(1 + (i % 4)),
                approvedAmount: lira(120_000 * (1 + (i % 4))),
                decisionReasonCode: 'WITHIN_PLAN',
              },
        ),
      ],
    });
  }
  // The second tenant has its own, so a cross-tenant read has something to fail to find.
  const demoBPeople = people.filter((p) => p.tenantId === tenants[1]!.id);
  for (let i = 0; i < 6; i += 1) {
    seedRequest({
      tenantId: tenants[1]!.id,
      daysAgo: 40 - i * 2,
      personId: demoBPeople[i % demoBPeople.length]!.id,
      programId: nextId(),
      enrollmentId: nextId(),
      providerOrganizationId: null,
      requestType: 'DIRECT_SERVICE',
      channel: 'BACKOFFICE',
      status: i % 2 === 0 ? 'PENDING_REVIEW' : 'APPROVED',
      lines: [
        line(
          nextId(),
          'COUNT',
          1,
          150_000,
          i % 2 === 0
            ? {}
            : {
                status: 'APPROVED',
                approvedQuantity: toDecimal(1),
                approvedAmount: lira(150_000),
                decisionReasonCode: 'WITHIN_PLAN',
              },
        ),
      ],
    });
  }

  // --- The worklist ---------------------------------------------------------------------
  const escalationQueue: StoredWorkQueue = {
    id: nextId(),
    tenantId: demoA.id,
    code: 'HEALTH_ESCALATION',
    name: 'Sağlık Eskalasyon',
    domainCode: 'HEALTH',
    assignmentPolicy: 'MANUAL',
    slaMinutes: null,
    escalationQueueId: null,
    active: true,
    rowVersion: 1,
    createdAt: isoDaysAgo(base, 120),
  };
  const reviewQueue: StoredWorkQueue = {
    id: nextId(),
    tenantId: demoA.id,
    code: 'HEALTH_REVIEW',
    name: 'Sağlık İncelemesi',
    domainCode: 'HEALTH',
    assignmentPolicy: 'MANUAL',
    slaMinutes: 240,
    escalationQueueId: escalationQueue.id,
    active: true,
    rowVersion: 2,
    createdAt: isoDaysAgo(base, 119),
  };
  const retiredQueue: StoredWorkQueue = {
    id: nextId(),
    tenantId: demoA.id,
    code: 'DOC_CHECK',
    name: 'Belge Kontrolü (kapalı)',
    domainCode: 'GENERIC',
    assignmentPolicy: 'ROUND_ROBIN',
    slaMinutes: 60,
    escalationQueueId: null,
    active: false,
    rowVersion: 3,
    createdAt: isoDaysAgo(base, 118),
  };
  const workQueues: StoredWorkQueue[] = [escalationQueue, reviewQueue, retiredQueue];

  const pendingReviewRequests = serviceRequests.filter(
    (r) => r.tenantId === demoA.id && r.status === 'PENDING_REVIEW',
  );
  const aggregateAt = (i: number): string =>
    pendingReviewRequests[i % pendingReviewRequests.length]!.id;
  const minutesAgo = (n: number): string => new Date(base - n * 60_000).toISOString();
  const inMinutes = (n: number): string => new Date(base + n * 60_000).toISOString();

  const workItemOf = (
    queue: StoredWorkQueue,
    aggregateId: string,
    title: string,
    over: Partial<StoredWorkItem>,
  ): StoredWorkItem => ({
    tenantId: demoA.id,
    id: nextId(),
    queueId: queue.id,
    aggregateType: 'SERVICE_REQUEST',
    aggregateId,
    title,
    priority: 100,
    assigneeActorId: null,
    assignedAt: null,
    dueAt: null,
    // The clock the queue handed out when the item was raised; nothing moves it after.
    slaMinutesSnapshot: queue.slaMinutes ?? null,
    status: 'OPEN',
    outcomeCode: null,
    completedAt: null,
    completedBy: null,
    escalatedAt: null,
    escalatedFromQueueId: null,
    rowVersion: 1,
    createdAt: minutesAgo(120),
    ...over,
  });

  const overdueItem = workItemOf(reviewQueue, aggregateAt(0), 'Geciken inceleme', {
    createdAt: minutesAgo(600),
    dueAt: minutesAgo(360),
    priority: 200,
  });
  const openItem = workItemOf(reviewQueue, aggregateAt(1), 'Bekleyen inceleme', {
    createdAt: minutesAgo(60),
    dueAt: inMinutes(180),
  });
  // Claimed by somebody else: claiming it answers 409 naming who holds it, which is the
  // only way a screen can say "Refik aldı" rather than "bir hata oluştu".
  const claimedByOther = workItemOf(reviewQueue, aggregateAt(2), 'Başkasının üstlendiği inceleme', {
    createdAt: minutesAgo(90),
    dueAt: inMinutes(150),
    status: 'CLAIMED',
    assigneeActorId: reviewerActorId,
    assignedAt: minutesAgo(45),
    rowVersion: 2,
  });
  const claimedByAdmin = workItemOf(reviewQueue, aggregateAt(3), 'Üstlendiğim inceleme', {
    createdAt: minutesAgo(80),
    dueAt: inMinutes(160),
    status: 'CLAIMED',
    assigneeActorId: adminActorId,
    assignedAt: minutesAgo(30),
    rowVersion: 2,
  });
  const completedItem = workItemOf(reviewQueue, aggregateAt(4), 'Kapanmış inceleme', {
    createdAt: minutesAgo(2000),
    dueAt: minutesAgo(1760),
    status: 'COMPLETED',
    assigneeActorId: reviewerActorId,
    assignedAt: minutesAgo(1900),
    outcomeCode: 'APPROVED',
    completedAt: minutesAgo(1800),
    completedBy: reviewerActorId,
    rowVersion: 4,
  });
  // Escalation moved the queue and left the clock alone: a late item stays late.
  const escalatedItem = workItemOf(escalationQueue, aggregateAt(5), 'Eskale edilmiş inceleme', {
    createdAt: minutesAgo(1500),
    dueAt: minutesAgo(1260),
    slaMinutesSnapshot: reviewQueue.slaMinutes ?? null,
    status: 'ESCALATED',
    escalatedAt: minutesAgo(1200),
    escalatedFromQueueId: reviewQueue.id,
    rowVersion: 3,
  });
  const workItems: StoredWorkItem[] = [
    overdueItem,
    openItem,
    claimedByOther,
    claimedByAdmin,
    completedItem,
    escalatedItem,
  ];

  const workItemComments: StoredWorkItemComment[] = [
    {
      tenantId: demoA.id,
      id: nextId(),
      workItemId: claimedByOther.id,
      aggregateType: claimedByOther.aggregateType,
      aggregateId: claimedByOther.aggregateId,
      visibility: 'INTERNAL',
      body: 'Seans sayısı plan üst sınırının üzerinde; mali incelemeye alındı.',
      authorActorId: reviewerActorId,
      createdAt: minutesAgo(40),
    },
    {
      tenantId: demoA.id,
      id: nextId(),
      workItemId: claimedByOther.id,
      aggregateType: claimedByOther.aggregateType,
      aggregateId: claimedByOther.aggregateId,
      visibility: 'PROVIDER',
      body: 'Fatura tarihi ile hizmet tarihi uyuşmuyor, lütfen kontrol edin.',
      authorActorId: reviewerActorId,
      createdAt: minutesAgo(35),
    },
  ];

  const approvalPolicies: StoredApprovalPolicy[] = [
    {
      tenantId: demoA.id,
      id: nextId(),
      actionCode: 'service_request.approve',
      scopeCode: 'STANDARD',
      versionNo: 1,
      minAmount: null,
      maxAmount: lira(1_000_000),
      requiredRoleCodes: ['REVIEWER'],
      requiredApproverCount: 1,
      validFrom: '2026-01-01',
      validTo: null,
      rowVersion: 1,
      createdAt: isoDaysAgo(base, 100),
    },
    {
      tenantId: demoA.id,
      id: nextId(),
      actionCode: 'service_request.approve',
      scopeCode: 'HIGH_VALUE',
      versionNo: 1,
      minAmount: lira(1_000_000),
      maxAmount: null,
      requiredRoleCodes: ['REVIEWER', 'FINANCE'],
      requiredApproverCount: 2,
      validFrom: '2026-01-01',
      validTo: null,
      rowVersion: 1,
      createdAt: isoDaysAgo(base, 100),
    },
  ];

  // --- Documents ------------------------------------------------------------------------
  const documents: StoredDocument[] = [];
  const documentLinks: StoredDocumentLink[] = [];
  const documentOf = (
    filename: string,
    classification: Schemas['DocumentClassification'],
    scanStatus: Schemas['DocumentScanStatus'],
    over: Partial<StoredDocument> = {},
  ): StoredDocument => {
    const id = nextId();
    const day = new Date(base - 7 * 86_400_000);
    const clean = scanStatus === 'CLEAN';
    const scanned = clean || scanStatus === 'INFECTED';
    const row: StoredDocument = {
      tenantId: demoA.id,
      id,
      // The schema makes "in secure but never scanned" unrepresentable, and so does this.
      bucket: clean ? 'secure' : 'quarantine',
      classification,
      originalFilename: filename,
      contentType: 'application/pdf',
      // Null until the upload is completed; afterwards it is what the worker counted, not
      // what the client claimed.
      byteSize: scanned ? 128_000 : null,
      sha256: scanned ? pseudoHash(`${filename}:${id}`) : null,
      scanStatus,
      ownerOrganizationId: providerRel.id,
      duplicateOfDocumentId: null,
      uploadedBy: providerActorId,
      uploadedAt: isoDaysAgo(base, 7),
      purgedAt: null,
      objectKey: `${demoA.id}/${day.getUTCFullYear()}/${String(day.getUTCMonth() + 1).padStart(2, '0')}/${id}`,
      createdAt: isoDaysAgo(base, 7),
      rowVersion: scanned ? 3 : scanStatus === 'PENDING' ? 1 : 2,
      ...over,
    };
    documents.push(row);
    return row;
  };
  const linkDocument = (
    doc: StoredDocument,
    aggregateId: string,
    documentTypeCode: string,
    requiredPermission: string | null = null,
  ): StoredDocumentLink => {
    const row: StoredDocumentLink = {
      tenantId: demoA.id,
      id: nextId(),
      documentId: doc.id,
      aggregateType: 'SERVICE_REQUEST',
      aggregateId,
      documentTypeCode,
      purpose: null,
      requiredPermission,
      createdBy: providerActorId,
      createdAt: doc.createdAt,
    };
    documentLinks.push(row);
    return row;
  };

  const pendingDocumentRequest = serviceRequests.find(
    (r) => r.tenantId === demoA.id && r.status === 'PENDING_DOCUMENT',
  )!;
  const cleanInvoice = documentOf('fatura-2026-03.pdf', 'PERSONAL', 'CLEAN');
  linkDocument(cleanInvoice, pendingDocumentRequest.id, 'INVOICE');
  // Clinical material stays clinical wherever it is reached from: the link names the
  // permission, and a caller who may read documents in general is still refused.
  const clinicalReport = documentOf('rapor.pdf', 'HEALTH', 'CLEAN');
  linkDocument(clinicalReport, pendingDocumentRequest.id, 'MEDICAL_REPORT', 'health.clinical.read');
  documentOf('yeni-fatura.pdf', 'PERSONAL', 'SCANNING');
  documentOf('taslak.pdf', 'INTERNAL', 'PENDING');
  documentOf('makbuz.pdf', 'PERSONAL', 'INFECTED');
  documentOf('bozuk.pdf', 'INTERNAL', 'FAILED');
  // The bytes are gone and the row outlives them, so "this existed and was removed on this
  // day" stays answerable.
  const purgedDocument = documentOf('eski-rapor.pdf', 'PERSONAL', 'CLEAN', {
    purgedAt: isoDaysAgo(base, 1),
  });
  documentOf('sozlesme.pdf', 'CONFIDENTIAL', 'CLEAN', {
    ownerOrganizationId: null,
    uploadedBy: adminActorId,
  });
  documentOf('baska-saglayici-fatura.pdf', 'PERSONAL', 'CLEAN', {
    ownerOrganizationId: otherProviderRel.id,
  });

  const legalHolds: StoredLegalHold[] = [
    {
      tenantId: demoA.id,
      id: nextId(),
      documentId: purgedDocument.id,
      personId: null,
      aggregateType: null,
      aggregateId: null,
      reason: 'Devam eden itiraz incelemesi.',
      placedBy: adminActorId,
      placedAt: isoDaysAgo(base, 5),
      releasedAt: null,
      releasedBy: null,
      rowVersion: 1,
    },
    {
      tenantId: demoA.id,
      id: nextId(),
      documentId: null,
      personId: null,
      aggregateType: 'SERVICE_REQUEST',
      aggregateId: rejectedRequest.id,
      reason: 'Kapanan dava; saklama kaldırıldı.',
      placedBy: adminActorId,
      placedAt: isoDaysAgo(base, 40),
      releasedAt: isoDaysAgo(base, 3),
      releasedBy: adminActorId,
      rowVersion: 2,
    },
  ];

  // --- Notifications --------------------------------------------------------------------
  const publishedTemplate: StoredNotificationTemplate = {
    tenantId: demoA.id,
    id: nextId(),
    eventCode: 'service_request.decided',
    channel: 'EMAIL',
    locale: 'tr-TR',
    versionNo: 2,
    status: 'PUBLISHED',
    subject: 'Talebiniz hakkında',
    body: 'Sayın {{given_name}}, {{reference_no}} numaralı talebiniz {{status_code}} durumuna geçti. Ayrıntı: {{deep_link}}',
    declaredVariables: ['given_name', 'reference_no', 'status_code', 'deep_link'],
    publishedAt: isoDaysAgo(base, 30),
    publishedBy: adminActorId,
    createdAt: isoDaysAgo(base, 31),
    rowVersion: 2,
  };
  // Publishing retires rather than refuses, so the version it replaced is still readable
  // next to the messages it produced.
  const retiredTemplate: StoredNotificationTemplate = {
    tenantId: demoA.id,
    id: nextId(),
    eventCode: 'service_request.decided',
    channel: 'EMAIL',
    locale: 'tr-TR',
    versionNo: 1,
    status: 'RETIRED',
    subject: 'Talep durumu',
    body: 'Sayın {{given_name}}, talebiniz {{status_code}} oldu.',
    declaredVariables: ['given_name', 'status_code'],
    publishedAt: isoDaysAgo(base, 90),
    publishedBy: adminActorId,
    createdAt: isoDaysAgo(base, 91),
    rowVersion: 3,
  };
  const draftTemplate: StoredNotificationTemplate = {
    tenantId: demoA.id,
    id: nextId(),
    eventCode: 'service_request.decided',
    channel: 'SMS',
    locale: 'tr-TR',
    versionNo: 1,
    status: 'DRAFT',
    subject: null,
    body: '{{reference_no}} numaralı talebiniz {{status_code}}.',
    declaredVariables: ['reference_no', 'status_code'],
    publishedAt: null,
    publishedBy: null,
    createdAt: isoDaysAgo(base, 4),
    rowVersion: 1,
  };
  const notificationTemplates: StoredNotificationTemplate[] = [
    publishedTemplate,
    retiredTemplate,
    draftTemplate,
  ];

  // The safe variables of a message. There is no slot here for a diagnosis, an identity
  // number or anything an operator typed, and no value carries a run of eight digits —
  // the same rule migration 000029 repeats as a CHECK.
  const messageReference = 'KPS-2026-0042';
  const notificationDeliveries: StoredNotificationDelivery[] = [];
  const notificationMessages: StoredNotificationMessage[] = [];
  const messageOf = (
    daysAgo: number,
    status: Schemas['NotificationMessageStatus'],
    over: Partial<StoredNotificationMessage> = {},
  ): StoredNotificationMessage => {
    const deepLink = `/service-requests/${serviceRequests[0]!.id}`;
    const row: StoredNotificationMessage = {
      tenantId: demoA.id,
      id: nextId(daysAgo * -86_400_000),
      eventCode: 'service_request.decided',
      recipientType: 'ACTOR',
      recipientId: adminActorId,
      channel: 'EMAIL',
      locale: 'tr-TR',
      templateId: publishedTemplate.id,
      templateVersionNo: publishedTemplate.versionNo,
      subjectRendered: 'Talebiniz hakkında',
      bodyRendered: `Sayın Ayşe, ${messageReference} numaralı talebiniz APPROVED durumuna geçti. Ayrıntı: https://kapsora.local${deepLink}`,
      safeVariables: {
        given_name: 'Ayşe',
        reference_no: messageReference,
        status_code: 'APPROVED',
        deep_link: deepLink,
      },
      status,
      // Suppression names its reason and only a suppressed message has one: that is the
      // whole difference between "not told" and "nothing happened".
      suppressedReason: null,
      resentFromMessageId: null,
      sentAt: status === 'SENT' ? isoDaysAgo(base, daysAgo) : null,
      createdAt: isoDaysAgo(base, daysAgo),
      rowVersion: 1,
      ...over,
    };
    notificationMessages.push(row);
    return row;
  };

  const sentMessage = messageOf(6, 'SENT');
  const failedMessage = messageOf(5, 'FAILED');
  messageOf(4, 'QUEUED');
  messageOf(3, 'SUPPRESSED', {
    suppressedReason: 'PREFERENCE_DISABLED',
    recipientType: 'PERSON',
    recipientId: familyPrincipal.id,
  });
  messageOf(2, 'SUPPRESSED', {
    suppressedReason: 'QUIET_HOURS',
    recipientType: 'PERSON',
    recipientId: familyPrincipal.id,
  });
  // Suppressed before anything was rendered: nobody has written a template for the event,
  // so there is no text and nothing to resend.
  messageOf(1, 'SUPPRESSED', {
    suppressedReason: 'NO_TEMPLATE',
    eventCode: 'authorization.expiring',
    templateId: null,
    templateVersionNo: null,
    subjectRendered: null,
    bodyRendered: null,
  });

  const deliveryOf = (
    m: StoredNotificationMessage,
    attemptNo: number,
    outcome: Schemas['NotificationDeliveryOutcome'],
    detail: string | null,
  ): void => {
    notificationDeliveries.push({
      tenantId: demoA.id,
      id: nextId(),
      messageId: m.id,
      attemptNo,
      providerCode: 'SMTP',
      providerMessageId: outcome === 'ACCEPTED' ? `smtp-${attemptNo}-${m.id.slice(0, 8)}` : null,
      outcome,
      detail,
      attemptedAt: m.createdAt,
    });
  };
  // A message that says SENT after a failure and one that went first time are the same
  // status and different stories, which is why the attempts travel with the message.
  deliveryOf(sentMessage, 1, 'ERROR', 'geçici bağlantı hatası');
  deliveryOf(sentMessage, 2, 'ACCEPTED', null);
  deliveryOf(failedMessage, 1, 'BOUNCED', 'alıcı adresi bulunamadı');

  const notificationPreferences: StoredNotificationPreference[] = [
    {
      tenantId: demoA.id,
      id: nextId(),
      recipientType: 'PERSON',
      recipientId: familyPrincipal.id,
      eventCode: null,
      channel: 'EMAIL',
      enabled: true,
      // Read in the recipient's own zone, and the window wraps midnight.
      quietHoursStart: '22:00',
      quietHoursEnd: '08:00',
      timezone: 'Europe/Istanbul',
      createdAt: isoDaysAgo(base, 60),
      rowVersion: 1,
    },
    {
      tenantId: demoA.id,
      id: nextId(),
      recipientType: 'PERSON',
      recipientId: familyPrincipal.id,
      eventCode: null,
      channel: 'SMS',
      enabled: false,
      quietHoursStart: null,
      quietHoursEnd: null,
      timezone: 'Europe/Istanbul',
      createdAt: isoDaysAgo(base, 60),
      rowVersion: 1,
    },
  ];

  /**
   * Stands in for the scan worker. No endpoint writes a verdict — not here and not on the
   * server — so this is the only way a document leaves SCANNING, which is what lets a
   * screen show "taranıyor" and then whichever answer the fixture chooses.
   */
  function advanceScan(documentId: string, verdict: ScanVerdict): StoredDocument | null {
    const doc = documents.find((d) => d.id === documentId);
    if (!doc || doc.scanStatus !== 'SCANNING') return null;
    doc.scanStatus = verdict;
    doc.rowVersion += 1;
    if (verdict !== 'FAILED') {
      doc.byteSize = doc.byteSize ?? 128_000;
      doc.sha256 = doc.sha256 ?? pseudoHash(`${doc.originalFilename}:${doc.id}`);
    }
    // Only a clean verdict promotes the bytes out of quarantine; an infected file's bytes
    // are deleted and the row survives to say the incident happened.
    if (verdict === 'CLEAN') doc.bucket = 'secure';
    return doc;
  }

  // ICD-10 is a code system like any other; WP-I5-05 seeds the real one. Two codes are
  // enough here, and what matters about the second is not its text: it is that the code
  // value itself says its category is one v1.2 11.10 protects further. The Go side reads
  // exactly this — catalog.code_value.attributes ->> 'sensitive' — so a diagnosis is
  // sensitive in the mock for the same reason it is sensitive on the server.
  const codeSystemIcd: StoredCodeSystem = {
    id: nextId(),
    tenantId: demoA.id,
    code: 'ICD10',
    name: 'ICD-10',
    version: '2026',
    authority: 'WHO',
    licensed: false,
    status: 'ACTIVE',
    validFrom: '2026-01-01',
    validTo: null,
    rowVersion: 1,
  };
  const icdPlain: StoredCodeValue = {
    id: nextId(),
    tenantId: demoA.id,
    codeSystemId: codeSystemIcd.id,
    code: 'J06.9',
    display: 'Üst solunum yolu enfeksiyonu',
    parentCode: 'J00-J99',
    validFrom: '2026-01-01',
    validTo: null,
    active: true,
    attributes: { chapter: 'X' },
  };
  const icdSensitive: StoredCodeValue = {
    id: nextId(),
    tenantId: demoA.id,
    codeSystemId: codeSystemIcd.id,
    code: 'F32.1',
    display: 'Orta düzeyde depresif atak',
    parentCode: 'F00-F99',
    validFrom: '2026-01-01',
    validTo: null,
    active: true,
    attributes: { chapter: 'V', sensitive: true },
  };
  codeValues.push(icdPlain, icdSensitive);

  // The two M5 accounts. They are appended here rather than declared with the others so
  // that adding them shifts none of the seeded random draws above: `buildWorld` runs off one
  // seeded stream, and an extra id taken early changes which organizations a small world
  // gets — which is exactly how adding a fixture broke four unrelated app tests once.
  accounts.push(
    {
      actorId: nextId(),
      username: 'sponsor.hr',
      displayName: 'Selin İnsan Kaynakları',
      email: 'sponsor.hr@example.invalid',
      memberships: [{ tenantCode: 'DEMO_A', permissions: SPONSOR_HR_PERMISSIONS }],
    },
    {
      actorId: nextId(),
      username: 'doctor.a',
      displayName: 'Demet Tıbbi Değerlendirici',
      email: 'doctor.a@example.invalid',
      memberships: [{ tenantCode: 'DEMO_A', permissions: MEDICAL_REVIEWER_PERMISSIONS }],
    },
    {
      actorId: nextId(),
      username: 'financial.reviewer',
      displayName: 'Fuat Mali Değerlendirici',
      email: 'financial.reviewer@example.invalid',
      memberships: [{ tenantCode: 'DEMO_A', permissions: FINANCIAL_REVIEWER_PERMISSIONS }],
    },
    {
      actorId: nextId(),
      username: 'billing.a',
      displayName: 'Burak Faturalama',
      email: 'billing.a@example.invalid',
      // Scoped to provider organization A exactly as provider.a is, by the same
      // relationship row: the billing desk and the clinic desk of one hospital see the
      // same organization and neither sees anybody else's.
      memberships: [
        {
          tenantCode: 'DEMO_A',
          permissions: PROVIDER_BILLING_PERMISSIONS,
          scopes: [{ type: 'ORGANIZATION', id: providerRel.id }],
        },
      ],
    },
  );

  // M5: two cases for the same member. One is ordinary and one carries a diagnosis from a
  // protected category, which is what makes the sponsor-HR criterion testable at all: the
  // sponsor's HR user must not be able to tell the two apart.
  const healthCase = (
    caseType: Schemas['HealthCaseType'],
    daysAgo: number,
    sensitivity: Schemas['HealthCaseSensitivity'],
  ): StoredHealthCase => ({
    id: nextId(daysAgo * -86_400_000),
    tenantId: demoA.id,
    personId: familyPrincipal.id,
    programId: programHealth.id,
    enrollmentId: familyEnrollment.id,
    caseType,
    providerOrganizationId: providerRel.id,
    openedAt: isoDaysAgo(base, daysAgo),
    closedAt: null,
    status: 'OPEN',
    sensitivity,
    serviceRequestId: null,
    createdAt: isoDaysAgo(base, daysAgo),
    rowVersion: 1,
  });
  const caseStandard = healthCase('OUTPATIENT', 20, 'STANDARD');
  const caseSensitive = healthCase('CHRONIC', 12, 'SENSITIVE');
  const healthCases: StoredHealthCase[] = [caseStandard, caseSensitive];

  const encounter = (
    row: StoredHealthCase,
    daysAgo: number,
    branchCode: string,
    notesClinical: string,
  ): StoredEncounter => ({
    id: nextId(daysAgo * -86_400_000),
    tenantId: demoA.id,
    caseId: row.id,
    encounterType: 'OUTPATIENT',
    startedAt: isoDaysAgo(base, daysAgo),
    endedAt: isoDaysAgo(base, daysAgo - 1),
    locationId: locationIstanbul.id,
    practitionerId: practitioners[0]!.id,
    branchCode,
    notesClinical,
    createdAt: isoDaysAgo(base, daysAgo),
    rowVersion: 1,
  });
  const encounterStandard = encounter(
    caseStandard,
    20,
    'KBB',
    'Boğaz ağrısı ve öksürük şikayeti; iki gün istirahat önerildi.',
  );
  const encounterSensitive = encounter(
    caseSensitive,
    12,
    'PSK',
    'Hasta uyku düzeninden şikayetçi olduğunu belirtti.',
  );
  const encounters: StoredEncounter[] = [encounterStandard, encounterSensitive];

  const diagnosis = (
    row: StoredEncounter,
    value: StoredCodeValue,
    diagnosisType: Schemas['DiagnosisType'],
  ): StoredDiagnosis => ({
    id: nextId(),
    tenantId: demoA.id,
    encounterId: row.id,
    codeSystemId: value.codeSystemId,
    codeValueId: value.id,
    diagnosisType,
    // Read from the code value, exactly as the server reads it at write time.
    sensitive: value.attributes.sensitive === true,
    recordedAt: row.startedAt,
    recordedBy: null,
  });
  const diagnoses: StoredDiagnosis[] = [
    diagnosis(encounterStandard, icdPlain, 'PRIMARY'),
    diagnosis(encounterSensitive, icdSensitive, 'PRIMARY'),
  ];

  // The service to entitlement mappings (WP-I5-05 section 2.1). They are appended here for
  // the same reason the two M5 accounts are: `buildWorld` runs off one seeded stream, and
  // an id drawn earlier would change which organizations a small world gets.
  //
  // They are attached to the PUBLISHED versions, which is the state the rule produces
  // rather than a hole in it: mappings are written while a version is a draft and are
  // frozen when it is published, so a published version carrying them is exactly right.
  // LAB_PANEL_AMBIGUOUS is deliberately left unmapped, so SERVICE_MAPPING_PENDING is still
  // reachable and still means what it says.
  const mappingPlan: { service: string; entitlement: string; unitFactor: string }[] = [
    { service: 'PHYSIO_SESSION', entitlement: 'PHYSIO_SESSION', unitFactor: '1.000000' },
    { service: 'GP_VISIT', entitlement: 'HEALTH_MONEY', unitFactor: '1.000000' },
    { service: 'MRI_SCAN', entitlement: 'HEALTH_MONEY', unitFactor: '1.000000' },
  ];
  const entitlementMappings: StoredEntitlementMapping[] = [];
  for (const version of planVersions) {
    if (version.status !== 'PUBLISHED') continue;
    for (const line of mappingPlan) {
      const service = serviceDefinitions.find(
        (d) => d.tenantId === version.tenantId && d.code === line.service,
      );
      const definition = version.definitions.find((d) => d.code === line.entitlement);
      if (!service || !definition) continue;
      entitlementMappings.push({
        id: nextId(),
        tenantId: version.tenantId,
        planVersionId: version.id,
        serviceDefinitionId: service.id,
        entitlementDefinitionId: definition.id,
        unitFactor: line.unitFactor,
        validFrom: null,
        validTo: null,
        rowVersion: 1,
      });
    }
  }

  // Contact details for the demo family's principal (WP-I5-05 section 2.5). Two channels,
  // one primary each, neither verified — an unverified contact is still an address, which
  // is what makes the SMS suppression read CHANNEL_NOT_DELIVERABLE rather than NO_ADDRESS.
  const personContacts: StoredPersonContact[] = [
    {
      id: nextId(),
      tenantId: demoA.id,
      personId: familyPrincipal.id,
      channel: 'EMAIL',
      value: 'kaan.aydemir@example.invalid',
      verifiedAt: null,
      primary: true,
      createdAt: isoDaysAgo(base, 120),
      rowVersion: 1,
    },
    {
      id: nextId(),
      tenantId: demoA.id,
      personId: familyPrincipal.id,
      channel: 'SMS',
      value: '+905321234567',
      verifiedAt: null,
      primary: true,
      createdAt: isoDaysAgo(base, 120),
      rowVersion: 1,
    },
  ];

  // M5: the treatment report (WP-I5-02). Four reports, appended here at the very end for
  // the same reason the two M5 accounts are: `buildWorld` runs off one seeded random stream,
  // and an id drawn earlier would change which organizations a small world gets.
  //
  // The four are the four states a screen has to be able to draw: a draft nobody has sent, an
  // approved report with two covered services that a claim can lean on, a rejected one, and
  // the correction of that rejection waiting to be sent. The last two share a reference and a
  // chain root, which is what makes "version 2 of 2" drawable.
  const doctorActorId = accounts.find((a) => a.username === 'doctor.a')!.actorId;
  // The queue a submitted report waits in. It is pushed here rather than declared with the
  // other three so that adding it shifts none of the seeded random draws above.
  workQueues.push({
    id: nextId(),
    tenantId: demoA.id,
    code: 'MEDICAL_REVIEW',
    name: 'Tıbbi Değerlendirme',
    domainCode: 'HEALTH',
    assignmentPolicy: 'MANUAL',
    slaMinutes: 480,
    escalationQueueId: escalationQueue.id,
    active: true,
    rowVersion: 1,
    createdAt: isoDaysAgo(base, 118),
  });
  const medicalReports: StoredMedicalReport[] = [];
  const medicalReportServices: StoredMedicalReportService[] = [];
  const medicalReportUsages: StoredMedicalReportUsage[] = [];
  const reportOf = (
    over: Partial<StoredMedicalReport> & {
      daysAgo: number;
      status: Schemas['MedicalReportStatus'];
    },
  ): StoredMedicalReport => {
    const { daysAgo, status, ...rest } = over;
    const id = nextId(daysAgo * -86_400_000);
    const decided = status === 'APPROVED' || status === 'REJECTED';
    const row: StoredMedicalReport = {
      id,
      tenantId: demoA.id,
      personId: familyPrincipal.id,
      caseId: null,
      reference: `MR-20260${String(daysAgo).padStart(3, '0')}-AAAAAAAA`,
      versionNo: 1,
      rootReportId: id,
      supersedesReportId: null,
      reportType: 'FIZIK_TEDAVI',
      reportSubtype: 'AMBULATUVAR',
      issuingPractitionerId: practitioners[0]!.id,
      issuingProviderOrganizationId: providerRel.id,
      issuedAt: isoDaysAgo(base, daysAgo).slice(0, 10),
      validFrom: isoDaysAgo(base, daysAgo).slice(0, 10),
      validTo: isoDaysAgo(base, daysAgo - 180).slice(0, 10),
      status,
      clinicalSummary: 'Sol dizde artroskopi sonrası altı hafta fizik tedavi gereklidir.',
      reviewComment: decided ? 'Rapordaki bulgular ile istenen hizmet değerlendirildi.' : null,
      rejectReasonCode: status === 'REJECTED' ? 'MISSING_EVIDENCE' : null,
      reviewedBy: decided ? doctorActorId : null,
      reviewedAt: decided ? isoDaysAgo(base, daysAgo - 1) : null,
      submittedAt: status === 'DRAFT' ? null : isoDaysAgo(base, daysAgo),
      submittedBy: status === 'DRAFT' ? null : providerActorId,
      createdAt: isoDaysAgo(base, daysAgo),
      rowVersion: status === 'DRAFT' ? 1 : 3,
      ...rest,
    };
    medicalReports.push(row);
    return row;
  };
  const reportLine = (
    report: StoredMedicalReport,
    service: StoredServiceDefinition,
    coveredQuantity: string | null,
    coveredAmount: string | null,
    notes: string | null,
  ): StoredMedicalReportService => {
    const row: StoredMedicalReportService = {
      id: nextId(),
      tenantId: demoA.id,
      reportId: report.id,
      serviceDefinitionId: service.id,
      coveredQuantity,
      coveredAmount,
      currencyCode: coveredAmount === null ? null : 'TRY',
      notes,
    };
    medicalReportServices.push(row);
    return row;
  };

  const reportDraft = reportOf({ daysAgo: 9, status: 'DRAFT' });
  reportLine(reportDraft, defPhysio, '10.000000', null, 'Haftada iki seans, sol diz.');

  const reportApproved = reportOf({ daysAgo: 30, status: 'APPROVED' });
  reportLine(reportApproved, defPhysio, '20.000000', '12000.000000', 'Haftada iki seans, sol diz.');
  reportLine(reportApproved, defGpVisit, '4.000000', null, 'Kontrol muayeneleri.');

  const reportRejected = reportOf({
    daysAgo: 45,
    status: 'REJECTED',
    reportSubtype: 'YATAN',
  });
  reportLine(reportRejected, defPhysio, '30.000000', null, 'Yatarak tedavi talebi.');
  // The correction: version 2 of the same chain, a draft again, with the line copied. The
  // rejected version keeps its decision and its own line exactly as they were.
  const reportCorrection = reportOf({
    daysAgo: 44,
    status: 'DRAFT',
    reference: reportRejected.reference,
    versionNo: 2,
    rootReportId: reportRejected.rootReportId,
    supersedesReportId: reportRejected.id,
    reportSubtype: 'AMBULATUVAR',
    reviewComment: null,
  });
  reportLine(reportCorrection, defPhysio, '20.000000', null, 'Ayaktan tedaviye çevrildi.');

  // The report file each of them hangs off, and the usage the approved one already has: a
  // claim that leaned on version 1 of a chain, which is what a screen showing "bu rapora
  // dayanan hasarlar" draws.
  for (const report of [reportDraft, reportApproved, reportRejected, reportCorrection]) {
    const file = documentOf('tedavi-raporu.pdf', 'HEALTH', 'CLEAN');
    const link = linkDocument(file, report.id, 'MEDICAL_REPORT', 'health.clinical.read');
    link.aggregateType = 'MEDICAL_REPORT';
  }
  medicalReportUsages.push({
    id: nextId(),
    tenantId: demoA.id,
    reportId: reportApproved.id,
    usedByType: 'CLAIM',
    usedById: nextId(),
    usedAt: isoDaysAgo(base, 10),
  });

  // The inpatient stay (WP-I5-03), appended at the very end for the same reason the medical
  // reports are: `buildWorld` runs off one seeded random stream, and an id drawn earlier
  // would change which organizations a small world gets and break four unrelated app tests.
  //
  // One admitted stay, because that is the state a screen has the most to draw: a ward night
  // followed by an intensive care night, and one extension a reviewer has already approved.
  // Its preauthorization is the fixture's own approved request rather than a request invented
  // here — a stay whose request page 404s would be a fixture that teaches a screen to link
  // nowhere.
  const stayRequest = serviceRequests.find(
    (r) => r.tenantId === demoA.id && r.status === 'APPROVED',
  )!;
  const stayAdmissionAt = isoDaysAgo(base, 6);
  const inpatientStay: StoredInpatientStay = {
    id: nextId(-6 * 86_400_000),
    tenantId: demoA.id,
    caseId: caseStandard.id,
    personId: familyPrincipal.id,
    providerOrganizationId: providerRel.id,
    locationId: locationIstanbul.id,
    attendingPractitionerId: practitioners[0]!.id,
    admissionAt: stayAdmissionAt,
    estimatedDays: 4,
    // Four days asked for, five authorized: the approved extension below added the fifth,
    // and the expected discharge moved with it.
    expectedDischargeAt: isoDaysAgo(base, -1),
    dischargeAt: null,
    status: 'ADMITTED',
    serviceRequestId: stayRequest.id,
    authorizationId: nextId(),
    admissionDiagnosisId: null,
    authorizedDays: '5',
    actualDays: null,
    releasedDays: null,
    overAuthorization: false,
    cancelReasonCode: null,
    createdAt: stayAdmissionAt,
    rowVersion: 4,
  };
  const inpatientStays: StoredInpatientStay[] = [inpatientStay];
  const stayExtensions: StoredStayExtension[] = [
    {
      id: nextId(-4 * 86_400_000),
      tenantId: demoA.id,
      stayId: inpatientStay.id,
      sequenceNo: 1,
      additionalDays: 1,
      reasonCode: 'COMPLICATION',
      reasonText: 'Ateş devam ettiği için bir gün daha gözlem gerekiyor.',
      serviceRequestId: stayRequest.id,
      authorizationId: nextId(),
      status: 'APPROVED',
      createdAt: isoDaysAgo(base, 4),
      rowVersion: 2,
    },
  ];
  // Two segments, meeting rather than overlapping: a transfer from the ward to intensive
  // care. The second is open, because the patient is still there.
  const staySegments: StoredStaySegment[] = [
    {
      id: nextId(-6 * 86_400_000),
      tenantId: demoA.id,
      stayId: inpatientStay.id,
      segmentType: 'WARD',
      startsAt: stayAdmissionAt,
      endsAt: isoDaysAgo(base, 4),
      roomCode: 'A-214',
      bedCode: '1',
      createdAt: stayAdmissionAt,
      rowVersion: 1,
    },
    {
      id: nextId(-4 * 86_400_000),
      tenantId: demoA.id,
      stayId: inpatientStay.id,
      segmentType: 'ICU',
      startsAt: isoDaysAgo(base, 4),
      endsAt: null,
      roomCode: 'YB-3',
      bedCode: '2',
      createdAt: isoDaysAgo(base, 4),
      rowVersion: 1,
    },
  ];

  // The claim (WP-I5-04), appended at the very end for the same reason the reports and the
  // stay are: `buildWorld` runs off one seeded random stream, and an id drawn earlier would
  // change which organizations a small world gets and break four unrelated app tests.
  //
  // One claim per resting status a screen has to be able to draw. SUBMITTED and
  // AUTO_ADJUDICATED are not among them on purpose: the pipeline passes through both inside
  // one transaction and a claim is never found sitting in either, so seeding one would teach a
  // screen to draw a state the server never serves.
  const claims: StoredClaim[] = [];
  const claimVersions: StoredClaimVersion[] = [];
  const claimLines: StoredClaimLine[] = [];
  const claimLineDecisions: StoredClaimLineDecision[] = [];
  const claimAdjustments: StoredClaimAdjustment[] = [];

  // The hold two of the claims draw on. WP-I4-02 has no mock surface, so this is the claim's
  // own minimal stand-in; see StoredClaimAuthorization.
  const claimAuthorizations: StoredClaimAuthorization[] = [
    {
      id: nextId(-30 * 86_400_000),
      tenantId: demoA.id,
      reference: 'AUT-20260801-CLAIMFIX',
      personId: familyPrincipal.id,
      items: [
        {
          serviceDefinitionId: defPhysio.id,
          approvedQuantity: '4.000000',
          consumedQuantity: '2.000000',
        },
      ],
    },
  ];

  let claimSequence = 0;
  const seedClaim = (
    status: Schemas['ClaimStatus'],
    daysAgo: number,
    over: Partial<StoredClaim> = {},
  ): StoredClaim => {
    claimSequence += 1;
    const row: StoredClaim = {
      id: nextId(daysAgo * -86_400_000),
      tenantId: demoA.id,
      reference: `CLM-20260${String(600 + claimSequence)}-AAAAAAA${claimSequence}`,
      personId: familyPrincipal.id,
      programId: programHealth.id,
      enrollmentId: familyEnrollment.id,
      providerOrganizationId: providerRel.id,
      domainCode: 'HEALTH',
      caseId: caseStandard.id,
      fulfilmentId: null,
      authorizationId: null,
      currentVersionNo: 1,
      status,
      serviceDateFrom: isoDaysAgo(base, daysAgo).slice(0, 10),
      serviceDateTo: isoDaysAgo(base, daysAgo).slice(0, 10),
      channel: 'PROVIDER_PORTAL',
      rejectReasonCode: status === 'REJECTED' ? 'NOT_COVERED' : null,
      returnReasonCode: status === 'RETURNED' ? 'DOCUMENT_MISSING' : null,
      reviewCommentMedical: null,
      reviewCommentFinancial: null,
      closedAt:
        status === 'REJECTED' || status === 'CANCELLED' ? isoDaysAgo(base, daysAgo - 1) : null,
      createdAt: isoDaysAgo(base, daysAgo),
      rowVersion: 2,
      ...over,
    };
    claims.push(row);
    return row;
  };

  const seedVersion = (
    claim: StoredClaim,
    versionNo: number,
    status: Schemas['ClaimVersionStatus'],
    over: Partial<StoredClaimVersion> = {},
  ): StoredClaimVersion => {
    const row: StoredClaimVersion = {
      id: nextId(),
      tenantId: demoA.id,
      claimId: claim.id,
      versionNo,
      status,
      submittedAt: status === 'DRAFT' ? null : claim.createdAt,
      submittedBy: status === 'DRAFT' ? null : providerActorId,
      returnedAt: null,
      returnedBy: null,
      returnReasonCode: null,
      returnReasonText: null,
      financialRequired: false,
      exceptions: [],
      createdAt: claim.createdAt,
      rowVersion: 1,
      ...over,
    };
    claimVersions.push(row);
    return row;
  };

  const seedLine = (
    version: StoredClaimVersion,
    lineNo: number,
    service: StoredServiceDefinition,
    quantity: string,
    amount: string,
    description: string | null,
    diagnosisId: string | null,
  ): StoredClaimLine => {
    const row: StoredClaimLine = {
      id: nextId(),
      tenantId: demoA.id,
      versionId: version.id,
      lineNo,
      serviceDefinitionId: service.id,
      unitType: service.defaultUnitType,
      quantity,
      unitAmount: null,
      lineAmount: amount,
      currencyCode: 'TRY',
      diagnosisId,
      medicalReportId: null,
      practitionerId: practitioners[0]!.id,
      description,
      createdAt: version.createdAt,
      rowVersion: 1,
    };
    claimLines.push(row);
    return row;
  };

  const seedDecision = (
    line: StoredClaimLine,
    versionNo: number,
    decision: Schemas['ClaimDecisionKind'],
    approved: string,
    payer: string,
    member: string,
    reasonCode: string,
    stage: Schemas['ClaimDecisionStage'],
    reasonText: string | null = null,
  ): StoredClaimLineDecision => {
    const row: StoredClaimLineDecision = {
      id: nextId(),
      tenantId: demoA.id,
      lineId: line.id,
      decidedInVersionNo: versionNo,
      decision,
      approvedQuantity: line.quantity,
      approvedAmount: approved,
      contractAmount: line.lineAmount,
      payerAmount: payer,
      memberAmount: member,
      reasonCode,
      reasonText,
      decidedBy: stage === 'AUTO' ? null : doctorActorId,
      decidedAt: line.createdAt,
      stage,
    };
    claimLineDecisions.push(row);
    return row;
  };

  // The physiotherapy diagnosis every seeded line points at, so a screen drawing the clinical
  // projection has something to draw and the financial projection has something to drop.
  const seededDiagnosisId = diagnoses[0]!.id;
  const physioDescription = 'Sol diz menisküs onarımı sonrası seans';
  const visitDescription = 'Kontrol muayenesi, sol diz';

  // APPROVED, and invoice-ready: two lines, both decided, the halves adding up.
  const claimApproved = seedClaim('APPROVED', 26, {
    authorizationId: claimAuthorizations[0]!.id,
    reviewCommentFinancial: 'Tarife ve ön onay ile uyumlu.',
  });
  const versionApproved = seedVersion(claimApproved, 1, 'SUBMITTED');
  seedDecision(
    seedLine(versionApproved, 1, defGpVisit, '1', '450', visitDescription, seededDiagnosisId),
    1,
    'APPROVED',
    '450',
    '360',
    '90',
    'AUTO_APPROVED',
    'AUTO',
  );
  seedDecision(
    seedLine(versionApproved, 2, defPhysio, '2', '500', physioDescription, seededDiagnosisId),
    1,
    'APPROVED',
    '500',
    '500',
    '0',
    'WITHIN_AUTHORIZATION',
    'FINANCIAL',
  );

  // PARTIALLY_APPROVED: one line cut, and the cut is on the adjustment ledger M7 will read.
  const claimPartial = seedClaim('PARTIALLY_APPROVED', 20);
  const versionPartial = seedVersion(claimPartial, 1, 'SUBMITTED');
  seedDecision(
    seedLine(versionPartial, 1, defGpVisit, '1', '450', visitDescription, seededDiagnosisId),
    1,
    'APPROVED',
    '450',
    '360',
    '90',
    'AUTO_APPROVED',
    'AUTO',
  );
  const partialCutLine = seedLine(
    versionPartial,
    2,
    defPhysio,
    '2',
    '500',
    physioDescription,
    seededDiagnosisId,
  );
  seedDecision(partialCutLine, 1, 'CUT', '400', '400', '0', 'TARIFF_EXCEEDED', 'FINANCIAL');
  claimAdjustments.push({
    id: nextId(),
    tenantId: demoA.id,
    claimId: claimPartial.id,
    versionNo: 1,
    adjustmentType: 'CUT',
    amount: '100',
    currencyCode: 'TRY',
    reasonCode: 'TARIFF_EXCEEDED',
    reasonText: null,
    createdBy: doctorActorId,
    createdAt: claimPartial.createdAt,
  });

  // PENDING_MEDICAL, carrying the exception that sent it there and the financial review it
  // still owes: this is the claim a screen draws the "why is this in front of me" list from.
  const claimMedical = seedClaim('PENDING_MEDICAL', 4, {
    authorizationId: claimAuthorizations[0]!.id,
  });
  const versionMedical = seedVersion(claimMedical, 1, 'SUBMITTED', {
    financialRequired: true,
    exceptions: [
      { lineNo: 1, code: 'AUTHORIZATION_EXCEEDED', stage: 'MEDICAL', detail: '2' },
      { lineNo: 2, code: 'RULE_FINANCIAL_REVIEW', stage: 'FINANCIAL', detail: 'HIGH_AMOUNT' },
    ],
  });
  seedLine(versionMedical, 1, defPhysio, '3', '750', physioDescription, seededDiagnosisId);
  seedLine(versionMedical, 2, defMri, '1', '2400', 'Kontrol MR', seededDiagnosisId);

  // PENDING_FINANCIAL, with a duplicate suspicion naming the approved claim above.
  const claimFinancial = seedClaim('PENDING_FINANCIAL', 3);
  const versionFinancial = seedVersion(claimFinancial, 1, 'SUBMITTED', {
    exceptions: [
      {
        lineNo: 1,
        code: 'DUPLICATE_SUSPECTED',
        stage: 'FINANCIAL',
        detail: claimApproved.reference,
      },
    ],
  });
  seedLine(versionFinancial, 1, defGpVisit, '1', '450', visitDescription, seededDiagnosisId);

  // RETURNED: version 1 superseded with its decision intact, version 2 a draft with the lines
  // copied. This is the correction model, drawable.
  const claimReturned = seedClaim('RETURNED', 8, { currentVersionNo: 2, rowVersion: 4 });
  const returnedV1 = seedVersion(claimReturned, 1, 'SUPERSEDED', {
    returnedAt: isoDaysAgo(base, 7),
    returnedBy: doctorActorId,
    returnReasonCode: 'DOCUMENT_MISSING',
    returnReasonText: 'Ameliyat notu eklenmemiş.',
  });
  const returnedLine = seedLine(
    returnedV1,
    1,
    defPhysio,
    '2',
    '500',
    physioDescription,
    seededDiagnosisId,
  );
  seedDecision(returnedLine, 1, 'CUT', '400', '400', '0', 'TARIFF_EXCEEDED', 'FINANCIAL');
  const returnedV2 = seedVersion(claimReturned, 2, 'DRAFT');
  seedLine(returnedV2, 1, defPhysio, '2', '500', physioDescription, seededDiagnosisId);

  // REJECTED, CANCELLED and a DRAFT nobody has sent yet.
  const claimRejected = seedClaim('REJECTED', 15);
  const versionRejected = seedVersion(claimRejected, 1, 'SUBMITTED');
  seedDecision(
    seedLine(versionRejected, 1, defMri, '1', '2400', 'Kontrol MR', seededDiagnosisId),
    1,
    'REJECTED',
    '0',
    '0',
    '0',
    'NOT_COVERED',
    'MEDICAL',
    'Endikasyon dosyada gösterilmemiştir.',
  );
  const claimCancelled = seedClaim('CANCELLED', 10);
  seedLine(
    seedVersion(claimCancelled, 1, 'DRAFT'),
    1,
    defGpVisit,
    '1',
    '450',
    visitDescription,
    seededDiagnosisId,
  );
  const claimDraft = seedClaim('DRAFT', 1, { rowVersion: 1, closedAt: null });
  seedLine(
    seedVersion(claimDraft, 1, 'DRAFT'),
    1,
    defGpVisit,
    '1',
    '450',
    visitDescription,
    seededDiagnosisId,
  );

  // M5 screens: a claim on the sensitive case, so the purpose prompt has something to guard
  // in a browser as well as in a test, and two looks on the person's access log — one
  // stated and recorded, one refused — so the log is a table rather than an empty state.
  // Appended last for the same reason the accounts were: nothing seeded above moves.
  const sensitiveDiagnosisId =
    diagnoses.find((d) => d.encounterId === encounterSensitive.id)?.id ?? null;
  const claimSensitive = seedClaim('PENDING_MEDICAL', 2, { caseId: caseSensitive.id });
  const versionSensitive = seedVersion(claimSensitive, 1, 'SUBMITTED', {
    financialRequired: false,
    exceptions: [
      { lineNo: 1, code: 'RULE_MEDICAL_REVIEW', stage: 'MEDICAL', detail: 'HEALTH_REVIEW' },
    ],
  });
  seedLine(
    versionSensitive,
    1,
    defGpVisit,
    '1',
    '450',
    'Depresif atak sonrası kontrol görüşmesi',
    sensitiveDiagnosisId,
  );
  const healthAccessEvents: StoredHealthAccessEvent[] = [
    {
      id: nextId(-3 * 86_400_000),
      tenantId: demoA.id,
      actorId: reviewerActorId,
      personId: familyPrincipal.id,
      membershipId: null,
      resourceType: 'HEALTH_CASE',
      resourceId: caseSensitive.id,
      accessType: 'VIEW',
      purposeCode: 'MEDICAL_REVIEW',
      reasonText: 'Rapor incelemesi',
      outcome: 'SUCCESS',
      occurredAt: isoDaysAgo(base, 3),
    },
    {
      id: nextId(-2 * 86_400_000),
      tenantId: demoA.id,
      actorId: providerActorId,
      personId: familyPrincipal.id,
      membershipId: null,
      resourceType: 'HEALTH_CASE',
      resourceId: caseSensitive.id,
      accessType: 'VIEW',
      purposeCode: null,
      reasonText: null,
      outcome: 'DENIED',
      occurredAt: isoDaysAgo(base, 2),
    },
  ];

  // --- M6 accommodation (WP-I6-01 section 2.4): two properties of one contracted provider,
  // four room types, ninety nights of allotment with one full weekend, and a season with two
  // price levels.
  //
  // Appended last, like the M5 mappings and accounts above and for the same reason:
  // `buildWorld` runs off one seeded random stream, and an id drawn earlier would change
  // which organizations a small world gets and break tests that have nothing to do with
  // hotels.

  // The catalogue side comes first, because a room type *is* a catalogue service: it is
  // priced by the contract prices of the pricing ladder and entitled through the
  // service-to-entitlement mapping, so a NIGHT-unit definition has to exist before a room
  // type can name one.
  const catAccommodation: StoredServiceCategory = {
    id: nextId(),
    tenantId: demoA.id,
    parentId: null,
    code: 'ACCOMMODATION',
    name: 'Konaklama',
    domain: 'ACCOMMODATION',
    active: true,
    rowVersion: 1,
  };
  serviceCategories.push(catAccommodation);

  const nightService = (code: string, name: string): StoredServiceDefinition => ({
    id: nextId(),
    tenantId: demoA.id,
    categoryId: catAccommodation.id,
    code,
    name,
    description: null,
    fulfillmentMode: 'RESERVATION',
    // NIGHT and nothing else: `createRoomType` refuses a service measured in anything else,
    // because a quote in the wrong unit draws an entitlement down in the wrong currency of
    // counting and nobody would see it until a member was billed for it.
    defaultUnitType: 'NIGHT',
    requiresProvider: true,
    active: true,
    rowVersion: 1,
  });
  const defRoomNight = nightService('ROOM_NIGHT', 'Standart Oda Gecelemesi');
  const defSuiteNight = nightService('SUITE_NIGHT', 'Suit Oda Gecelemesi');
  serviceDefinitions.push(defRoomNight, defSuiteNight);

  // The season, as two price lists on the contract version that is already published. The
  // dearer one wins inside its window on list priority alone and is rejected outside it with
  // SEASON_MISMATCH, so one service carries two price levels across the year and nothing
  // downstream has to know there is a season at all.
  const lowSeasonList: StoredPriceList = {
    id: nextId(),
    tenantId: demoA.id,
    contractVersionId: publishedContractVersion.id,
    code: 'ACC_STD',
    name: 'Konaklama Standart Tarifesi',
    priority: 100,
    seasonFrom: null,
    seasonTo: null,
    weekdayMask: null,
    rowVersion: 1,
  };
  const highSeasonList: StoredPriceList = {
    id: nextId(),
    tenantId: demoA.id,
    contractVersionId: publishedContractVersion.id,
    code: 'ACC_HIGH',
    name: 'Konaklama Yüksek Sezon Tarifesi',
    priority: 200,
    seasonFrom: '2026-06-01',
    seasonTo: '2026-10-01',
    weekdayMask: null,
    rowVersion: 1,
  };
  priceLists.push(lowSeasonList, highSeasonList);

  const nightPrice = (listId: string, definitionId: string, amount: Decimal): StoredPriceItem =>
    priceItem(
      listId,
      { serviceDefinitionId: definitionId },
      {
        unitType: 'NIGHT',
        pricingMethod: 'UNIT',
        amount,
        // The member's own contribution, as a percentage rather than a round sum: a split
        // that has to be computed is a split a mock adding the nights up before rounding
        // would get visibly wrong.
        memberShareMethod: 'PERCENT',
        memberSharePercent: '15.000000',
      },
    );
  priceItems.push(
    nightPrice(lowSeasonList.id, defRoomNight.id, '2400.000000'),
    nightPrice(lowSeasonList.id, defSuiteNight.id, '3800.000000'),
    nightPrice(highSeasonList.id, defRoomNight.id, '4250.000000'),
    nightPrice(highSeasonList.id, defSuiteNight.id, '6900.000000'),
  );

  // The plan side: a NIGHT entitlement mapped to both room services on the published version
  // of each health plan. A room night draws down a count of nights and never a sum of money,
  // which is the whole reason the search caps how many nights the plan carries rather than
  // capping the lira — a member with two nights left has two nights left, not two lira.
  const nightDefinitions = new Map<string, StoredEntitlementDefinition>();
  for (const plan of [planFamilyHealth, planIndividualHealth]) {
    const version = planVersions.find((v) => v.planId === plan.id && v.status === 'PUBLISHED')!;
    const definition: StoredEntitlementDefinition = {
      id: nextId(),
      code: 'ACCOMMODATION_NIGHT',
      name: 'Konaklama Gecesi',
      unitType: 'NIGHT',
      currencyCode: null,
      // Not family-shared on purpose: the spouse's two nights below are hers, and a shared
      // pool would quietly hand her the principal's ten and hide the partial-cover case.
      familyShared: false,
      allowOverdraft: false,
      initialQuantity: 10,
      periodType: 'PLAN_YEAR',
      periodLength: null,
      rolloverPolicy: 'NONE',
      rolloverCap: null,
      status: 'ACTIVE',
    };
    version.definitions.push(definition);
    nightDefinitions.set(plan.id, definition);
    for (const service of [defRoomNight, defSuiteNight]) {
      entitlementMappings.push({
        id: nextId(),
        tenantId: demoA.id,
        planVersionId: version.id,
        serviceDefinitionId: service.id,
        entitlementDefinitionId: definition.id,
        unitFactor: '1.000000',
        validFrom: null,
        validTo: null,
        rowVersion: 1,
      });
    }
  }

  // Two balances, so both answers to "does the plan cover this stay" are drawable: the
  // principal has ten nights left and the spouse two. A three-night stay is carried whole for
  // one of them and for exactly two nights for the other, and the third night is hers.
  entitlementAccounts.push(
    {
      id: nextId(),
      tenantId: demoA.id,
      enrollmentId: familyEnrollment.id,
      personId: familyPrincipal.id,
      definition: toAccountDefinition(nightDefinitions.get(planFamilyHealth.id)!),
      benefitPeriodFrom: isoDaysAgo(base, 190).slice(0, 10),
      benefitPeriodTo: null,
      totalGranted: '10.000000',
      available: '10.000000',
      consumed: '0.000000',
      reserved: '0.000000',
      expired: '0.000000',
      status: 'OPEN',
      rowVersion: 1,
    },
    {
      id: nextId(),
      tenantId: demoA.id,
      enrollmentId: spouseEnrollment.id,
      personId: familySpouse.id,
      definition: toAccountDefinition(nightDefinitions.get(planIndividualHealth.id)!),
      benefitPeriodFrom: isoDaysAgo(base, 180).slice(0, 10),
      benefitPeriodTo: null,
      totalGranted: '10.000000',
      available: '2.000000',
      consumed: '8.000000',
      reserved: '0.000000',
      expired: '0.000000',
      status: 'OPEN',
      rowVersion: 1,
    },
  );

  // The two reservation desks. Each holds PROVIDER_RESERVATION and exactly one ORGANIZATION
  // grant, and that single row decides everything either of them may see: there is no second,
  // client-side filter here that could disagree with the server.
  //
  // There are two of them because the provider boundary has two sides. One desk belongs to
  // the organization that runs the hotels below; the other belongs to `otherProviderRel`, the
  // second provider the M4 fixtures already needed, and it runs no hotel here at all — which
  // is what lets a test show that another provider's room type is 404 and never 403.
  accounts.push(
    {
      actorId: nextId(),
      username: 'reservation.a',
      displayName: 'Rezan Rezervasyon',
      email: 'reservation.a@example.invalid',
      memberships: [
        {
          tenantCode: 'DEMO_A',
          permissions: PROVIDER_RESERVATION_PERMISSIONS,
          scopes: [{ type: 'ORGANIZATION', id: providerRel.id }],
        },
      ],
    },
    {
      actorId: nextId(),
      username: 'reservation.other',
      displayName: 'Ozan Öteki',
      email: 'reservation.other@example.invalid',
      memberships: [
        {
          tenantCode: 'DEMO_A',
          permissions: PROVIDER_RESERVATION_PERMISSIONS,
          scopes: [{ type: 'ORGANIZATION', id: otherProviderRel.id }],
        },
      ],
    },
  );

  const property = (
    code: string,
    name: string,
    propertyType: Schemas['PropertyType'],
    amenities: Schemas['PropertyAmenity'][],
    extra: Partial<StoredProperty> = {},
  ): StoredProperty => ({
    id: nextId(),
    tenantId: demoA.id,
    providerOrganizationId: providerRel.id,
    // Neither building is one of the provider's health locations: a Kadıköy medical centre
    // is not a hotel, and a property with no provider location is the ordinary case.
    locationId: null,
    code,
    name,
    propertyType,
    timezone: 'Europe/Istanbul',
    city: 'Antalya',
    regionCode: 'ANTALYA',
    amenities,
    costCenter: null,
    status: 'ACTIVE',
    createdAt: isoDaysAgo(base, 120),
    updatedAt: null,
    rowVersion: 1,
    ...extra,
  });
  const resort = property('KEMER_RESORT', 'Kapsora Kemer Tatil Köyü', 'RESORT', [
    'WIFI',
    'PARKING',
    'ALL_INCLUSIVE',
    'POOL',
    'BEACH',
    'FAMILY_ROOM',
  ]);
  // The tenant's own guest house rather than a commercial hotel: it charges an internal cost
  // centre instead of invoicing (v1.2 9.13), which is what SOCIAL_FACILITY means.
  const guestHouse = property(
    'SIDE_MISAFIREVI',
    'Kapsora Side Misafirevi',
    'SOCIAL_FACILITY',
    ['WIFI', 'PARKING', 'BREAKFAST', 'STEP_FREE_ACCESS'],
    { costCenter: 'CC.KONAKLAMA.01' },
  );
  const properties: StoredProperty[] = [resort, guestHouse];

  const roomType = (
    ofProperty: StoredProperty,
    service: StoredServiceDefinition,
    code: string,
    name: string,
    party: { maxAdults: number; maxChildren: number; maxOccupancy: number },
    attributes: Record<string, unknown>,
  ): StoredRoomType => ({
    id: nextId(),
    tenantId: demoA.id,
    propertyId: ofProperty.id,
    code,
    name,
    ...party,
    // Bed layout and view, and nothing that could ever be a fact about a guest: the
    // attributes bag is written by a provider clerk and read by everybody.
    attributes,
    serviceDefinitionId: service.id,
    status: 'ACTIVE',
    createdAt: ofProperty.createdAt,
    updatedAt: null,
    rowVersion: 1,
  });
  const roomTypes: StoredRoomType[] = [
    roomType(
      resort,
      defRoomNight,
      'STD_DBL',
      'Standart Çift Kişilik Oda',
      { maxAdults: 2, maxChildren: 2, maxOccupancy: 4 },
      { beds: '1 çift kişilik', view: 'BAHCE' },
    ),
    roomType(
      resort,
      defSuiteNight,
      'FAM_SUITE',
      'Aile Suiti',
      { maxAdults: 4, maxChildren: 3, maxOccupancy: 6 },
      { beds: '1 çift kişilik + 2 tek kişilik', view: 'DENIZ' },
    ),
    roomType(
      guestHouse,
      defRoomNight,
      'GUEST_SGL',
      'Tek Kişilik Misafir Odası',
      { maxAdults: 1, maxChildren: 0, maxOccupancy: 1 },
      { beds: '1 tek kişilik' },
    ),
    roomType(
      guestHouse,
      defRoomNight,
      'GUEST_DBL',
      'Çift Kişilik Misafir Odası',
      { maxAdults: 2, maxChildren: 1, maxOccupancy: 3 },
      { beds: '2 tek kişilik' },
    ),
  ];

  // Ninety consecutive nights from the world's base date, one row per room type per night.
  //
  // The capacity differs by room type because an allotment is how many rooms this provider
  // gives this payer on a night, not how many rooms the hotel owns. On the first Saturday
  // three weeks in and the Sunday after it every room type is sold out — held plus confirmed
  // is exactly the capacity — so a screen has a genuinely full date to draw and the
  // `held + confirmed <= capacity` invariant is exercised at its edge rather than only well
  // inside it. On every other night the two counters together never exceed three, so an
  // allotment cut low enough is refused by the weekend and by nothing else.
  //
  // The ninetieth night is the last one with a row at all. That is deliberate too: a stay
  // reaching past it is a stay with an allotment on every night but one, which the search
  // has to answer with no availability rather than with "mostly available".
  const ACCOMMODATION_NIGHTS = 90;
  const nightOf = (offset: number): string =>
    new Date(base + offset * 86_400_000).toISOString().slice(0, 10);
  let fullWeekend = 21;
  while (new Date(base + fullWeekend * 86_400_000).getUTCDay() !== 6) {
    fullWeekend += 1;
  }
  const allotmentOf: Record<string, number> = {
    STD_DBL: 12,
    FAM_SUITE: 5,
    GUEST_SGL: 8,
    GUEST_DBL: 6,
  };
  const inventoryDays: StoredInventoryDay[] = [];
  for (const room of roomTypes) {
    const capacity = allotmentOf[room.code]!;
    for (let offset = 0; offset < ACCOMMODATION_NIGHTS; offset += 1) {
      const full = offset === fullWeekend || offset === fullWeekend + 1;
      inventoryDays.push({
        id: nextId(),
        tenantId: demoA.id,
        roomTypeId: room.id,
        stayDate: nightOf(offset),
        capacity,
        held: full ? 2 : offset % 2,
        confirmed: full ? capacity - 2 : offset % 3,
        updatedAt: isoDaysAgo(base, 30),
        rowVersion: 1,
      });
    }
  }

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
    codeSystems: [codeSystemSut, codeSystemIcd],
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
    serviceRequestVersions,
    workQueues,
    workItems,
    workItemComments,
    approvalPolicies,
    documents,
    documentLinks,
    legalHolds,
    notificationTemplates,
    notificationMessages,
    notificationDeliveries,
    notificationPreferences,
    entitlementMappings,
    personContacts,
    healthCases,
    encounters,
    diagnoses,
    healthAccessEvents,
    medicalReports,
    medicalReportServices,
    medicalReportUsages,
    inpatientStays,
    stayExtensions,
    staySegments,
    claims,
    claimVersions,
    claimLines,
    claimLineDecisions,
    claimAdjustments,
    claimAuthorizations,
    properties,
    roomTypes,
    inventoryDays,
    advanceScan,
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

// --- M4 projections ---------------------------------------------------------------------
// Every one of these drops `tenantId` and whatever else is the mock's own bookkeeping, so
// no handler can answer with a field the contract does not declare.

/** The version a request currently reads from, whether or not it has been submitted. */
export function currentVersionOf(
  world: MockWorld,
  request: StoredServiceRequest,
): StoredServiceRequestVersion | undefined {
  return world.serviceRequestVersions.find(
    (v) => v.serviceRequestId === request.id && v.versionNo === request.currentVersionNo,
  );
}

/** The one DRAFT version a request may have; there is never more than one. */
export function draftVersionOf(
  world: MockWorld,
  request: StoredServiceRequest,
): StoredServiceRequestVersion | undefined {
  return world.serviceRequestVersions.find(
    (v) => v.serviceRequestId === request.id && v.status === 'DRAFT',
  );
}

/**
 * The member's name as the wire carries it (WP-I5-05 section 2.6). It is derived here, on
 * the way out, exactly as the Go server derives it in SQL: a stored request holds ids, and
 * a name that had been copied onto the row would be a name that stopped being true the
 * moment somebody corrected the person.
 */
export function personDisplayName(world: MockWorld, personId: string): string {
  const person = world.people.find((p) => p.id === personId);
  if (!person) return '';
  return [person.firstName, person.middleName, person.lastName].filter(Boolean).join(' ');
}

/** The organization's name, by the tenant-scoped id everything on the wire uses. */
export function organizationDisplayName(
  world: MockWorld,
  tenantOrganizationId: string | null | undefined,
): string | null {
  if (!tenantOrganizationId) return null;
  const rel = world.relationships.find((r) => r.id === tenantOrganizationId);
  if (!rel) return null;
  return world.organizations.get(rel.organizationId)?.displayName ?? null;
}

export function toServiceRequest(
  world: MockWorld,
  request: StoredServiceRequest,
): Schemas['ServiceRequest'] {
  const { tenantId: _tenantId, ...rest } = request;
  return {
    ...rest,
    personDisplayName: personDisplayName(world, request.personId),
    providerDisplayName: organizationDisplayName(world, request.providerOrganizationId),
    items: currentVersionOf(world, request)?.items ?? [],
  };
}

export function toServiceRequestVersionSummary(
  version: StoredServiceRequestVersion,
): Schemas['ServiceRequestVersionSummary'] {
  return {
    id: version.id,
    versionNo: version.versionNo,
    status: version.status,
    submittedAt: version.submittedAt,
    submittedBy: version.submittedBy,
    returnedAt: version.returnedAt,
    returnedBy: version.returnedBy,
    returnReasonCode: version.returnReasonCode,
    returnReasonText: version.returnReasonText,
    createdAt: version.createdAt,
  };
}

/**
 * One version with the lines it carried. A submitted version answers from the snapshot
 * frozen at submit — so a reader sees what the reviewer decided against — with the
 * decisions recorded afterwards overlaid by line number, because the snapshot was written
 * before they existed.
 */
export function toServiceRequestVersion(
  version: StoredServiceRequestVersion,
): Schemas['ServiceRequestVersion'] {
  const live = new Map(version.items.map((i) => [i.lineNo, i]));
  const items =
    version.status === 'DRAFT' || version.snapshotItems === null
      ? version.items
      : version.snapshotItems.map((frozen): StoredServiceRequestItem => {
          const current = live.get(frozen.lineNo);
          if (!current) return frozen;
          return {
            ...frozen,
            id: current.id,
            serviceDefinitionId: current.serviceDefinitionId,
            status: current.status,
            approvedQuantity: current.approvedQuantity ?? null,
            approvedAmount: current.approvedAmount ?? null,
            decisionReasonCode: current.decisionReasonCode ?? null,
          };
        });
  return {
    ...toServiceRequestVersionSummary(version),
    serviceRequestId: version.serviceRequestId,
    items,
  };
}

export function toWorkQueue(queue: StoredWorkQueue): Schemas['WorkQueue'] {
  const { tenantId: _tenantId, ...rest } = queue;
  return rest;
}

/**
 * The mapping as the wire carries it: the ids the row holds plus the two codes a person
 * reads it by, resolved on the way out.
 */
export function toEntitlementMapping(
  world: MockWorld,
  mapping: StoredEntitlementMapping,
): Schemas['EntitlementMapping'] {
  const service = world.serviceDefinitions.find((d) => d.id === mapping.serviceDefinitionId);
  const version = world.planVersions.find((v) => v.id === mapping.planVersionId);
  const definition = version?.definitions.find((d) => d.id === mapping.entitlementDefinitionId);
  return {
    id: mapping.id,
    planVersionId: mapping.planVersionId,
    serviceDefinitionId: mapping.serviceDefinitionId,
    serviceCode: service?.code ?? '',
    serviceName: service?.name ?? '',
    entitlementDefinitionId: mapping.entitlementDefinitionId,
    entitlementCode: definition?.code ?? '',
    unitType: definition?.unitType ?? 'COUNT',
    unitFactor: mapping.unitFactor,
    validFrom: mapping.validFrom,
    validTo: mapping.validTo,
    rowVersion: mapping.rowVersion,
  };
}

/**
 * The mask a contact is read back as. It keeps enough to recognise a value somebody
 * already knows and never enough to learn one they do not, which is the same rule the
 * identifier masks follow.
 */
export function maskContact(channel: 'EMAIL' | 'SMS', value: string): string {
  if (channel === 'EMAIL') {
    const at = value.lastIndexOf('@');
    if (at <= 0) return '**';
    const local = value.slice(0, at);
    const host = value.slice(at);
    if (local.length <= 1) return `***${host}`;
    return `${local[0]}${'*'.repeat(local.length - 1)}${host}`;
  }
  const plus = value.startsWith('+') ? '+' : '';
  const digits = plus ? value.slice(1) : value;
  if (digits.length <= 2) return plus + '*'.repeat(digits.length);
  return `${plus}${'*'.repeat(digits.length - 2)}${digits.slice(-2)}`;
}

/** The contact as the wire carries it: the mask, and never the value. */
export function toPersonContact(contact: StoredPersonContact): Schemas['PersonContact'] {
  return {
    id: contact.id,
    personId: contact.personId,
    channel: contact.channel,
    maskedValue: maskContact(contact.channel, contact.value),
    verifiedAt: contact.verifiedAt,
    primary: contact.primary,
    createdAt: contact.createdAt,
    rowVersion: contact.rowVersion,
  };
}

/** Who holds a work item, by name. An id in the worklist names nobody. */
export function assigneeDisplayName(
  world: MockWorld,
  actorId: string | null | undefined,
): string | null {
  if (!actorId) return null;
  return world.accounts.find((a) => a.actorId === actorId)?.displayName ?? null;
}

export function toWorkItem(world: MockWorld, item: StoredWorkItem): Schemas['WorkItem'] {
  const { tenantId: _tenantId, ...rest } = item;
  return { ...rest, assigneeDisplayName: assigneeDisplayName(world, item.assigneeActorId) };
}

export function toWorkItemComment(comment: StoredWorkItemComment): Schemas['WorkItemComment'] {
  const { tenantId: _tenantId, ...rest } = comment;
  return rest;
}

export function toApprovalPolicy(policy: StoredApprovalPolicy): Schemas['ApprovalPolicy'] {
  const { tenantId: _tenantId, ...rest } = policy;
  return rest;
}

/**
 * Whether downloadDocument would answer a URL right now, computed in one place: clean, in
 * the secure bucket, and not purged. A second copy of this rule somewhere else is how a
 * screen ends up offering a button the server refuses.
 */
export function documentDownloadable(doc: StoredDocument): boolean {
  return doc.scanStatus === 'CLEAN' && doc.bucket === 'secure' && doc.purgedAt === null;
}

export function toDocumentLink(link: StoredDocumentLink): Schemas['DocumentLink'] {
  const { tenantId: _tenantId, ...rest } = link;
  return rest;
}

export function toDocument(world: MockWorld, doc: StoredDocument): Schemas['Document'] {
  const { tenantId: _tenantId, objectKey: _objectKey, ...rest } = doc;
  return {
    ...rest,
    downloadable: documentDownloadable(doc),
    links: world.documentLinks
      .filter((l) => l.documentId === doc.id)
      .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id))
      .map(toDocumentLink),
  };
}

export function toLegalHold(hold: StoredLegalHold): Schemas['LegalHold'] {
  const { tenantId: _tenantId, ...rest } = hold;
  return rest;
}

export function toNotificationTemplate(
  template: StoredNotificationTemplate,
): Schemas['NotificationTemplate'] {
  const { tenantId: _tenantId, ...rest } = template;
  return rest;
}

export function toNotificationMessage(
  message: StoredNotificationMessage,
): Schemas['NotificationMessage'] {
  const { tenantId: _tenantId, ...rest } = message;
  return rest;
}

export function toNotificationDelivery(
  delivery: StoredNotificationDelivery,
): Schemas['NotificationDelivery'] {
  const { tenantId: _tenantId, ...rest } = delivery;
  return rest;
}

export function toNotificationPreference(
  preference: StoredNotificationPreference,
): Schemas['NotificationPreference'] {
  const { tenantId: _tenantId, ...rest } = preference;
  return rest;
}
