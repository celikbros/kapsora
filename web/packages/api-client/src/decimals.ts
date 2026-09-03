/**
 * Money and quantity fields cross the wire as decimal strings: the API stores them as
 * `numeric(20,6)` and encodes them as text so no value is ever rounded by a binary float.
 * The generated types call them `number`, because that is what `type: number` means in
 * JSON Schema, so this module restates the affected shapes with `string` where the value
 * is a quantity. Screens format these strings; they never parse them.
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
 * Reinterprets a decoded response whose quantity fields the generated types call `number`
 * but the API actually sends as decimal strings. One cast, in one place, with the reason
 * written down, instead of a lie repeated at every call site.
 */
export function asDecimals<T>(value: unknown): T {
  return value as T;
}
