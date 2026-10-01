/**
 * Money and quantity fields are exact decimal strings in the client. Some API responses
 * encode them as JSON number tokens; those reads must preserve the original token text
 * before JSON.parse can round it. Generated types call them `number` because the schema
 * uses type: number. Screens format the exact strings; they never parse them as floats.
 */
import type { components } from './generated/kapsora-v1';

type Schemas = components['schemas'];

/** A decimal number in canonical text form, e.g. "1250.000000". */
export type Decimal = string;

/** Entitlement definition with exact quantities. */
export type EntitlementDefinition = Omit<
  Schemas['EntitlementDefinition'],
  'initialQuantity' | 'rolloverCap'
> & {
  initialQuantity: Decimal;
  rolloverCap?: Decimal;
};

/** Entitlement definition as submitted on a draft plan version. */
export type EntitlementDefinitionInput = Omit<
  Schemas['EntitlementDefinitionInput'],
  'initialQuantity' | 'rolloverCap'
> & {
  initialQuantity: Decimal;
  rolloverCap?: Decimal;
};

/** Plan version whose definitions carry exact quantities. */
export type PlanVersion = Omit<Schemas['PlanVersion'], 'definitions'> & {
  definitions: EntitlementDefinition[];
};

/** Entitlement account balances. */
export type EntitlementAccount = Omit<
  Schemas['EntitlementAccount'],
  'available' | 'consumed' | 'expired' | 'reserved' | 'totalGranted' | 'openReservations'
> & {
  available: Decimal;
  consumed: Decimal;
  expired: Decimal;
  reserved: Decimal;
  totalGranted: Decimal;
  openReservations?: EntitlementReservation[];
};

/** One hold on an account. */
export type EntitlementReservation = Omit<
  Schemas['EntitlementReservation'],
  'consumedQuantity' | 'quantity' | 'releasedQuantity'
> & {
  consumedQuantity: Decimal;
  quantity: Decimal;
  releasedQuantity: Decimal;
};

/** One ledger movement. */
export type LedgerEntry = Omit<
  Schemas['LedgerEntry'],
  'deltaAvailable' | 'deltaConsumed' | 'deltaExpired' | 'deltaReserved' | 'deltaTotal'
> & {
  deltaAvailable: Decimal;
  deltaConsumed: Decimal;
  deltaExpired: Decimal;
  deltaReserved: Decimal;
  deltaTotal: Decimal;
};

/** One page of ledger movements. */
export interface LedgerPage {
  items: LedgerEntry[];
  nextCursor?: string | null;
}

/** A manual adjustment waiting for or carrying a decision. */
export type EntitlementAdjustment = Omit<Schemas['EntitlementAdjustment'], 'deltaQuantity'> & {
  deltaQuantity: Decimal;
};

/** The body of an adjustment request. */
export type CreateAdjustmentRequest = Omit<Schemas['CreateAdjustmentRequest'], 'deltaQuantity'> & {
  deltaQuantity: Decimal;
};

/** One requested line of an eligibility check. */
export type EligibilityServiceItem = Omit<
  Schemas['EligibilityCheckRequest']['serviceItems'][number],
  'quantity' | 'requestedAmount'
> & {
  quantity: Decimal;
  requestedAmount?: Decimal;
};

/** The body of an eligibility check. */
export type EligibilityCheckRequest = Omit<Schemas['EligibilityCheckRequest'], 'serviceItems'> & {
  serviceItems: EligibilityServiceItem[];
};

/** Per-line result of a check. */
export type EligibilityItemResult = Omit<
  NonNullable<Schemas['EligibilityCheckResult']['items']>[number],
  'availableQuantity' | 'requestedQuantity'
> & {
  availableQuantity?: Decimal | null;
  requestedQuantity?: Decimal;
};

/** One entitlement balance reported by a check. */
export type EligibilityBalance = Omit<
  NonNullable<Schemas['EligibilityCheckResult']['balances']>[number],
  'available'
> & {
  available: Decimal;
};

/** The result of a check. */
export type EligibilityCheckResult = Omit<
  Schemas['EligibilityCheckResult'],
  'items' | 'balances'
> & {
  items?: EligibilityItemResult[];
  balances?: EligibilityBalance[];
};

/** The stored snapshot of a check. */
export type EligibilityEvaluation = Omit<Schemas['EligibilityEvaluation'], 'request' | 'result'> & {
  request: EligibilityCheckRequest;
  result: EligibilityCheckResult;
};

/**
 * Reinterprets responses that already carry decimal strings. For endpoints that send
 * JSON number tokens, parseDecimalJson must run before this cast or normal JSON.parse.
 */
export function asDecimals<T>(value: unknown): T {
  return value as T;
}
