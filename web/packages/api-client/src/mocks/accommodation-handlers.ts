/**
 * MSW handlers for the accommodation vertical (WP-I6-01): the properties, their room types,
 * the daily allotment and the availability search that prices a stay.
 *
 * The mock is a test double of the Go server and M6 treats a divergence in either direction
 * as a bug. Six things here are transcriptions rather than re-implementations, because they
 * are the six a screen would be built wrongly against:
 *
 *   - **the boundary**: an ORGANIZATION-scoped caller sees its own buildings and no others,
 *     and another provider's property or room type is 404 rather than 403 — that a hotel
 *     exists at all is not this caller's business. The one exception is `createProperty`,
 *     which has no existing row to hide behind and answers 403 PROPERTY_SCOPE;
 *   - **the gaps**: an inventory range answers one entry per date whether or not there is a
 *     row behind it, and a night with no row is `allotted: false` rather than "zero free";
 *   - **the allotment**: a capacity below what is already held or confirmed on even one night
 *     refuses the *whole* range with 409 INVENTORY_BELOW_COMMITMENT and writes nothing,
 *     naming the first offending date in the `stayDate` extension member;
 *   - **`available`**: the minimum daily availability over the whole range, and zero for a
 *     room type without an allotment on even one night of it. A room with capacity on
 *     twenty-nine of thirty nights is not "mostly available" — the guest would have nowhere
 *     to sleep on the thirtieth;
 *   - **the nights**: the count of civil days in [checkIn, checkOut), counted on the calendar
 *     and never by dividing an elapsed duration. A stay over a summer-time change is still
 *     the number of nights somebody slept;
 *   - **the money**: the plan's remaining NIGHT entitlement limits how many nights it
 *     carries, not how much money. The first N nights are carried and the rest are entirely
 *     the member's, every figure is computed in integer micro-units, each night is rounded
 *     once and the member's share is taken as the difference, and the nights are summed once.
 *
 * Nothing here reserves anything. An availability answer is an answer; the hold is WP-I6-02's
 * and there is no endpoint for it in this module.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import { bookingHandlers } from './booking-handlers';
import { resolveContractPrice } from './contract-handlers';
import {
  fromMicros,
  multiplyMicros,
  percentOfMicros,
  toMicros,
  type Decimal,
  type MockWorld,
  type StoredEvaluation,
  type StoredInventoryDay,
  type StoredPriceItem,
  type StoredProperty,
  type StoredRoomType,
} from './data';
import { resolveEligibility } from './eligibility-handlers';
import type { EligibilityCheckRequest } from '../decimals';
import type { MockApi, MockSession } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  organizationScope,
  parseLimit,
  pathParam,
  problem,
  readJson,
  requireIdempotencyKey,
  requireIfMatch,
  validationFailed,
  wait,
  withinPeriod,
  withinScope,
  type FieldError,
  type Schemas,
} from './handlers';

/**
 * Reading a hotel and opening its allotment are two grants on purpose:
 * `accommodation.property.read` is what a member holds, and `accommodation.inventory.manage`
 * is what it deliberately does not.
 */
const PERMISSION_READ = 'accommodation.property.read';
const PERMISSION_MANAGE = 'accommodation.inventory.manage';

/** How many nights one allotment call may cover; a mistyped year is a 422, not a timeout. */
const MAX_INVENTORY_RANGE = 730;
/** The largest allotment a single night may carry. */
const MAX_CAPACITY = 10_000;
/** accommodation.max_nights for a tenant that has configured nothing. */
const DEFAULT_MAX_NIGHTS = 30;
/** A region with more hotels than this needs a filter, not four hundred quotes. */
const MAX_SEARCH_PROPERTIES = 50;

const DAY_MS = 86_400_000;
const DATE_ONLY = /^\d{4}-\d{2}-\d{2}$/;
const CODE = /^[A-Z0-9][A-Z0-9_-]{0,39}$/;
const REGION_CODE = /^[A-Z0-9][A-Z0-9_-]{0,15}$/;
const COST_CENTER = /^[A-Z0-9][A-Z0-9_.-]{0,31}$/;

const PROPERTY_TYPES = new Set<string>([
  'HOTEL',
  'RESORT',
  'GUESTHOUSE',
  'SOCIAL_FACILITY',
  'OTHER',
]);
const STATUSES = new Set<string>(['ACTIVE', 'INACTIVE']);
const AMENITIES = new Set<string>([
  'WIFI',
  'PARKING',
  'BREAKFAST',
  'HALF_BOARD',
  'FULL_BOARD',
  'ALL_INCLUSIVE',
  'POOL',
  'SPA',
  'GYM',
  'AIR_CONDITIONING',
  'RESTAURANT',
  'BEACH',
  'STEP_FREE_ACCESS',
  'PET_FRIENDLY',
  'FAMILY_ROOM',
  'SHUTTLE',
  'LAUNDRY',
  'MEETING_ROOM',
  'KITCHENETTE',
  'THERMAL',
]);

/**
 * The IANA zones this mock believes in. The server asks the runtime; a browser test double
 * asking `Intl` would answer differently in different engines, so the list is short, explicit
 * and enough for every fixture — a timezone is refused on the field either way.
 */
const TIMEZONES = new Set<string>([
  'Europe/Istanbul',
  'Europe/Berlin',
  'Europe/London',
  'Europe/Paris',
  'UTC',
]);

const ZERO = 0n;
/** One room, one night: the quantity of every line the search prices. */
const ONE = 1_000_000n;

function propertyNotFound(api: MockApi): Response {
  return problem(api, 404, 'PROPERTY_NOT_FOUND', 'Tesis bulunamadı');
}

function roomTypeNotFound(api: MockApi): Response {
  return problem(api, 404, 'ROOM_TYPE_NOT_FOUND', 'Oda tipi bulunamadı');
}

// --- calendar --------------------------------------------------------------------------

/** True when the text is a civil date this module can count with. */
function validDate(value: unknown): value is string {
  return typeof value === 'string' && DATE_ONLY.test(value) && !Number.isNaN(Date.parse(value));
}

function addDays(date: string, days: number): string {
  return new Date(Date.parse(`${date}T00:00:00.000Z`) + days * DAY_MS).toISOString().slice(0, 10);
}

/** Every civil date of the inclusive range [from, to], in order. */
function datesInclusive(from: string, to: string): string[] {
  const out: string[] = [];
  for (let day = from; day <= to; day = addDays(day, 1)) out.push(day);
  return out;
}

/**
 * The nights of the half-open stay [checkIn, checkOut), counted on the calendar.
 *
 * It walks civil dates and never divides an elapsed duration. That is the whole of the
 * function and the whole of the reason it exists: on the night a zone leaves or enters
 * summer time the wall clock advances by 23 or 25 hours, and dividing by twenty-four would
 * answer a stay over the last Sunday of March one night short.
 */
function nightsOf(checkIn: string, checkOut: string): number {
  if (checkOut <= checkIn) return 0;
  let nights = 0;
  for (let day = checkIn; day < checkOut; day = addDays(day, 1)) nights += 1;
  return nights;
}

// --- projections -----------------------------------------------------------------------

function toProperty(row: StoredProperty): Schemas['Property'] {
  return {
    id: row.id,
    providerOrganizationId: row.providerOrganizationId,
    locationId: row.locationId,
    code: row.code,
    name: row.name,
    propertyType: row.propertyType,
    timezone: row.timezone,
    city: row.city,
    regionCode: row.regionCode,
    amenities: row.amenities,
    costCenter: row.costCenter,
    status: row.status,
    createdAt: row.createdAt,
    ...(row.updatedAt === null ? {} : { updatedAt: row.updatedAt }),
    rowVersion: row.rowVersion,
  };
}

function toRoomType(row: StoredRoomType): Schemas['RoomType'] {
  return {
    id: row.id,
    propertyId: row.propertyId,
    code: row.code,
    name: row.name,
    maxAdults: row.maxAdults,
    maxChildren: row.maxChildren,
    maxOccupancy: row.maxOccupancy,
    attributes: row.attributes,
    serviceDefinitionId: row.serviceDefinitionId,
    status: row.status,
    createdAt: row.createdAt,
    ...(row.updatedAt === null ? {} : { updatedAt: row.updatedAt }),
    rowVersion: row.rowVersion,
  };
}

/** capacity minus held minus confirmed, computed here so no screen subtracts. */
function availableOf(row: StoredInventoryDay): number {
  return Math.max(0, row.capacity - row.held - row.confirmed);
}

function toInventoryDay(
  stayDate: string,
  row: StoredInventoryDay | undefined,
): Schemas['InventoryDay'] {
  if (!row) {
    // A night this provider has opened nothing on. It is not the same statement as "zero
    // free", and it carries no rowVersion because there is no row to have a version of.
    return { stayDate, allotted: false, capacity: 0, held: 0, confirmed: 0, available: 0 };
  }
  return {
    stayDate,
    allotted: true,
    capacity: row.capacity,
    held: row.held,
    confirmed: row.confirmed,
    available: availableOf(row),
    updatedAt: row.updatedAt,
    rowVersion: row.rowVersion,
  };
}

// --- money -----------------------------------------------------------------------------

/**
 * The contract amount of one night, in micro-units, or null when the method needs a human.
 * It mirrors internal/pricing: a FORMULA price needs the formula engine no test double
 * carries, and guessing a number would be worse than saying so.
 */
function contractAmountMicros(price: StoredPriceItem): bigint | null {
  let amount: bigint;
  switch (price.pricingMethod) {
    case 'FIXED':
      amount = toMicros(price.amount);
      break;
    case 'UNIT':
      amount = multiplyMicros(toMicros(price.amount), ONE);
      break;
    case 'PERCENT_OF_LIST':
      // A room night carries no requested amount to take a percentage of, so the percentage
      // is of nothing. The server reaches the same figure by the same route.
      amount = ZERO;
      break;
    default:
      return null;
  }
  const min = price.minAmount === null ? null : toMicros(price.minAmount);
  const max = price.maxAmount === null ? null : toMicros(price.maxAmount);
  if (min !== null && amount < min) amount = min;
  if (max !== null && amount > max) amount = max;
  return amount;
}

/** The member's own share of one night's contract amount, in micro-units. */
function memberShareMicros(price: StoredPriceItem, contract: bigint): bigint {
  switch (price.memberShareMethod) {
    case 'FIXED': {
      const fixed = toMicros(price.memberShareAmount);
      return fixed > contract ? contract : fixed;
    }
    case 'PERCENT':
      return percentOfMicros(contract, toMicros(price.memberSharePercent));
    default:
      return ZERO;
  }
}

/**
 * How many whole nights a balance carries. A half night is not a night somebody can sleep,
 * so this truncates towards zero and never rounds up: telling a member 2.9 was three nights
 * would be a promise the ledger cannot keep.
 */
function wholeNights(available: Decimal): number {
  const micros = toMicros(available);
  if (micros <= ZERO) return 0;
  return Number(micros / 1_000_000n);
}

/** What one room type's stay was resolved to, before it becomes a body. */
interface RoomTypeQuote {
  quote: Schemas['AvailabilityQuote'] | null;
  reason: Schemas['QuoteUnavailableReason'] | null;
  /**
   * How many nights of the stay the plan was applied to. It is counted where the decision is
   * made rather than read back off `payerAmount` afterwards: a night the plan covers whose
   * split happens to leave the payer nothing is still a night drawn from the count, and a
   * contract with a 100 % member share would make the two readings disagree.
   */
  coveredNights: number;
}

export function accommodationHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  // --- the provider boundary -------------------------------------------------------------

  /**
   * True when the caller may see this building. `organizationScope` is the caller's own
   * ORGANIZATION grants and `null` means unrestricted, exactly as a nil slice does on the
   * server — there is no second, client-side rule here that could disagree with it.
   */
  const visible = (session: MockSession, tenantId: string, row: StoredProperty): boolean => {
    const scope = organizationScope(api, session, tenantId);
    return scope === null ? true : withinScope(scope, row.providerOrganizationId);
  };

  const findProperty = (
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredProperty | undefined => {
    const row = world().properties.find((p) => p.id === id && p.tenantId === tenantId);
    if (!row) return undefined;
    return visible(session, tenantId, row) ? row : undefined;
  };

  /** A room type the caller may see: the property behind it decides, as it does on the server. */
  const findRoomType = (
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredRoomType | undefined => {
    const row = world().roomTypes.find((r) => r.id === id && r.tenantId === tenantId);
    if (!row) return undefined;
    return findProperty(session, tenantId, row.propertyId) ? row : undefined;
  };

  /**
   * Refuses a property naming an organization that is not a contracted provider, or a
   * location that is not that provider's. Both are field errors rather than raw foreign key
   * violations, because a clerk who pasted the wrong id needs to be told which field.
   */
  const checkPropertyTargets = (
    tenantId: string,
    organizationId: string,
    locationId: string | null,
  ): FieldError | null => {
    const provider = world().providers.find(
      (p) => p.tenantId === tenantId && p.tenantOrganizationId === organizationId,
    );
    if (!provider) {
      return {
        field: 'providerOrganizationId',
        code: 'NOT_FOUND',
        message: 'bu kurum için sağlayıcı profili yok',
      };
    }
    if (locationId === null) return null;
    const location = world().providerLocations.find(
      (l) => l.id === locationId && l.tenantId === tenantId && l.providerId === provider.id,
    );
    return location
      ? null
      : { field: 'locationId', code: 'NOT_FOUND', message: 'lokasyon bu sağlayıcıya ait değil' };
  };

  // --- validation ------------------------------------------------------------------------

  /** The rules a property create and a property patch share, so the two cannot drift apart. */
  const validatePropertyBody = (
    errors: FieldError[],
    body: {
      name?: unknown;
      propertyType?: unknown;
      timezone?: unknown;
      city?: unknown;
      regionCode?: unknown;
      amenities?: unknown;
      costCenter?: unknown;
      status?: unknown;
    },
    status: string,
  ): void => {
    const name = typeof body.name === 'string' ? body.name.trim() : '';
    if (name === '' || [...name].length > 200) {
      errors.push({ field: 'name', code: 'RANGE', message: '1-200 karakter olmalı' });
    }
    if (!PROPERTY_TYPES.has(String(body.propertyType))) {
      errors.push({
        field: 'propertyType',
        code: 'ENUM',
        message: 'tanımlı bir tesis türü olmalı',
      });
    }
    const timezone = typeof body.timezone === 'string' ? body.timezone.trim() : '';
    if (!TIMEZONES.has(timezone)) {
      errors.push({
        field: 'timezone',
        code: 'ENUM',
        message: 'geçerli bir IANA saat dilimi olmalı',
      });
    }
    if (!STATUSES.has(status)) {
      errors.push({ field: 'status', code: 'ENUM', message: 'ACTIVE veya INACTIVE olmalı' });
    }
    if (typeof body.city === 'string' && [...body.city].length > 100) {
      errors.push({ field: 'city', code: 'RANGE', message: 'en fazla 100 karakter olabilir' });
    }
    if (typeof body.regionCode === 'string' && !REGION_CODE.test(body.regionCode)) {
      errors.push({
        field: 'regionCode',
        code: 'FORMAT',
        message: 'A-Z, 0-9, _ ve - içeren en fazla 16 karakter olmalı',
      });
    }
    if (typeof body.costCenter === 'string' && !COST_CENTER.test(body.costCenter)) {
      errors.push({
        field: 'costCenter',
        code: 'FORMAT',
        message: 'A-Z, 0-9, ., _ ve - içeren en fazla 32 karakter olmalı',
      });
    }
    const amenities = Array.isArray(body.amenities) ? body.amenities : [];
    amenities.forEach((key, i) => {
      if (!AMENITIES.has(String(key))) {
        errors.push({ field: `amenities[${i}]`, code: 'ENUM', message: 'tanınmayan olanak kodu' });
      }
    });
  };

  /** The amenity list a row is stored with: deduplicated, in the contract's own order. */
  const normaliseAmenities = (raw: unknown): Schemas['PropertyAmenity'][] => {
    const seen = new Set(Array.isArray(raw) ? raw.map(String) : []);
    return [...AMENITIES].filter((key) => seen.has(key)) as Schemas['PropertyAmenity'][];
  };

  /** The rules a room type create and a room type patch share. */
  const validateRoomTypeBody = (
    errors: FieldError[],
    body: {
      name?: unknown;
      maxAdults?: unknown;
      maxChildren?: unknown;
      maxOccupancy?: unknown;
      attributes?: unknown;
    },
    status: string,
  ): void => {
    const name = typeof body.name === 'string' ? body.name.trim() : '';
    if (name === '' || [...name].length > 200) {
      errors.push({ field: 'name', code: 'RANGE', message: '1-200 karakter olmalı' });
    }
    const adults = Number(body.maxAdults);
    const children = Number(body.maxChildren ?? 0);
    const occupancy = Number(body.maxOccupancy);
    if (!Number.isInteger(adults) || adults < 1 || adults > 20) {
      errors.push({ field: 'maxAdults', code: 'RANGE', message: '1-20 arasında olmalı' });
    }
    if (!Number.isInteger(children) || children < 0 || children > 20) {
      errors.push({ field: 'maxChildren', code: 'RANGE', message: '0-20 arasında olmalı' });
    }
    if (!Number.isInteger(occupancy) || occupancy < 1 || occupancy > 40) {
      errors.push({ field: 'maxOccupancy', code: 'RANGE', message: '1-40 arasında olmalı' });
    } else if (Number.isInteger(adults) && occupancy < adults) {
      // "Sleeps two adults and three people in total" is a typo a clerk can fix; a
      // constraint violation is not something they can read.
      errors.push({
        field: 'maxOccupancy',
        code: 'RANGE',
        message: 'en az yetişkin kapasitesi kadar olmalı',
      });
    }
    if (!STATUSES.has(status)) {
      errors.push({ field: 'status', code: 'ENUM', message: 'ACTIVE veya INACTIVE olmalı' });
    }
  };

  /**
   * Refuses a service definition this tenant does not have, one that is inactive, and one
   * whose unit is not NIGHT. The unit is checked on the field rather than discovered later,
   * because a service measured in anything else produces a quote in the wrong unit and draws
   * an entitlement down in the wrong currency of counting.
   */
  const checkRoomTypeService = (tenantId: string, definitionId: string): FieldError | null => {
    const definition = world().serviceDefinitions.find(
      (d) => d.id === definitionId && d.tenantId === tenantId,
    );
    if (!definition) {
      return {
        field: 'serviceDefinitionId',
        code: 'NOT_FOUND',
        message: 'hizmet tanımı bulunamadı',
      };
    }
    if (!definition.active) {
      return { field: 'serviceDefinitionId', code: 'STATE', message: 'hizmet tanımı pasif' };
    }
    if (definition.defaultUnitType !== 'NIGHT') {
      return {
        field: 'serviceDefinitionId',
        code: 'STATE',
        message: 'oda tipinin hizmet tanımı NIGHT birimli olmalı',
      };
    }
    return null;
  };

  const validateInventoryRange = (from: unknown, to: unknown): FieldError[] => {
    const errors: FieldError[] = [];
    if (!validDate(from)) {
      errors.push({ field: 'from', code: 'REQUIRED', message: 'başlangıç tarihi zorunlu' });
    }
    if (!validDate(to)) {
      errors.push({ field: 'to', code: 'REQUIRED', message: 'bitiş tarihi zorunlu' });
    }
    if (errors.length > 0) return errors;
    if ((to as string) < (from as string)) {
      errors.push({ field: 'to', code: 'RANGE', message: 'başlangıç tarihinden önce olamaz' });
      return errors;
    }
    const nights = Math.round(
      (Date.parse(`${to as string}T00:00:00.000Z`) -
        Date.parse(`${from as string}T00:00:00.000Z`)) /
        DAY_MS +
        1,
    );
    if (nights > MAX_INVENTORY_RANGE) {
      errors.push({
        field: 'to',
        code: 'RANGE',
        message: `bir çağrıda en fazla ${MAX_INVENTORY_RANGE} gece ayarlanabilir`,
      });
    }
    return errors;
  };

  const inventoryRangeBody = (
    tenantId: string,
    roomTypeId: string,
    from: string,
    to: string,
  ): Schemas['RoomTypeInventoryRange'] => {
    const byDate = new Map(
      world()
        .inventoryDays.filter((d) => d.tenantId === tenantId && d.roomTypeId === roomTypeId)
        .map((d) => [d.stayDate, d] as const),
    );
    return {
      roomTypeId,
      from,
      to,
      // One entry per date of the range, in order, gaps included.
      days: datesInclusive(from, to).map((day) => toInventoryDay(day, byDate.get(day))),
    };
  };

  // --- the availability search -----------------------------------------------------------

  /**
   * The properties this person may be shown for this stay: ACTIVE buildings of ACTIVE
   * providers holding an ACTIVE contract whose published version covers the whole stay, and
   * whose payer organization is one of those behind the programs the person is enrolled in on
   * the first night.
   *
   * A person with no active enrollment is narrowed to nothing rather than widened to
   * everything: an empty payer list is not "no filter", and reading it as one would show a
   * member every hotel in the tenant.
   */
  const searchableProperties = (
    session: MockSession,
    tenantId: string,
    personId: string,
    programId: string | null,
    checkIn: string,
    lastNight: string,
  ): { property: StoredProperty; providerProfileId: string }[] => {
    const payers = new Set<string>();
    for (const enrollment of world().enrollments) {
      if (enrollment.tenantId !== tenantId) continue;
      if (enrollment.personId !== personId) continue;
      if (enrollment.status !== 'ACTIVE') continue;
      if (programId && enrollment.programId !== programId) continue;
      if (!withinPeriod(checkIn, enrollment.validFrom, enrollment.validTo)) continue;
      const program = world().programs.find((p) => p.id === enrollment.programId);
      if (program) payers.add(program.payerOrganizationId);
    }
    if (payers.size === 0) return [];

    const scope = organizationScope(api, session, tenantId);
    const out: { property: StoredProperty; providerProfileId: string }[] = [];
    for (const property of world().properties) {
      if (property.tenantId !== tenantId || property.status !== 'ACTIVE') continue;
      if (!withinScope(scope, property.providerOrganizationId)) continue;
      const provider = world().providers.find(
        (p) =>
          p.tenantId === tenantId &&
          p.tenantOrganizationId === property.providerOrganizationId &&
          p.status === 'ACTIVE',
      );
      if (!provider) continue;
      const contracted = world().contracts.some(
        (contract) =>
          contract.tenantId === tenantId &&
          contract.status === 'ACTIVE' &&
          contract.providerProfileId === provider.id &&
          payers.has(contract.payerOrganizationId) &&
          world().contractVersions.some(
            (version) =>
              version.contractId === contract.id &&
              version.status === 'PUBLISHED' &&
              version.validFrom !== null &&
              // Published over the whole stay: a version that lapses mid-stay has not
              // agreed the last night of it.
              withinPeriod(checkIn, version.validFrom, version.validTo) &&
              withinPeriod(lastNight, version.validFrom, version.validTo),
          ),
      );
      if (!contracted) continue;
      out.push({ property, providerProfileId: provider.id });
      if (out.length >= MAX_SEARCH_PROPERTIES) break;
    }
    return out;
  };

  /**
   * The rule the whole search turns on: the minimum daily availability over the range, and
   * zero for a room type that lacks an allotment on even one night of it.
   *
   * A room type with capacity on twenty-nine of thirty nights is not "mostly available". The
   * guest would have nowhere to sleep on the thirtieth, so the answer for the stay is none.
   */
  const availabilityOver = (tenantId: string, roomTypeId: string, nights: string[]): number => {
    const byDate = new Map(
      world()
        .inventoryDays.filter((d) => d.tenantId === tenantId && d.roomTypeId === roomTypeId)
        .map((d) => [d.stayDate, d] as const),
    );
    let min = Number.MAX_SAFE_INTEGER;
    for (const night of nights) {
      const row = byDate.get(night);
      if (!row) return 0;
      min = Math.min(min, availableOf(row));
    }
    return min === Number.MAX_SAFE_INTEGER ? 0 : min;
  };

  /**
   * What the plan carries for one service, as the search decides it for every service at
   * once: whether the person may use it at all, and how many nights of it are left.
   *
   * It is the same rule the search above applies -- one eligibility check, the NIGHT unit
   * read as a count of nights and anything else as a budget -- reached through one service
   * rather than a list, because a hold is about one room type. WP-I6-02 asks for it so the
   * hold reserves the nights the member was quoted and not the length of the stay.
   */
  const coverForService = (
    tenantId: string,
    personId: string,
    programId: string | null,
    serviceDate: string,
    serviceDefinitionId: string,
  ): { eligible: boolean; nightsCarried: number; money: bigint | null } => {
    const check: EligibilityCheckRequest = {
      personId,
      programId,
      serviceDate,
      serviceItems: [{ serviceDefinitionId, quantity: '1.000000' }],
      context: { domain: 'ACCOMMODATION' },
    };
    const verdict = resolveEligibility(world(), tenantId, check, world().nextId());
    const item = (verdict.items ?? [])[0];
    if (!item) return { eligible: false, nightsCarried: 0, money: null };
    const eligible = item.outcome === 'ELIGIBLE';
    const code = item.entitlementCode ?? null;
    const available = item.availableQuantity ?? null;
    if (code === null || available === null || available === undefined) {
      return { eligible, nightsCarried: 0, money: null };
    }
    const unit = (verdict.balances ?? []).find((b) => b.entitlementCode === code)?.unit ?? '';
    return {
      eligible,
      nightsCarried: unit === 'NIGHT' ? wholeNights(available) : 0,
      money: unit === 'NIGHT' ? null : toMicros(available),
    };
  };

  /**
   * Prices one room type over the stay and sums it once.
   *
   * Each night is resolved through the same price ladder every quote, authorization and claim
   * reads, rounded once to its own figures, and the member's share is taken as the difference
   * — so `payerAmount + memberAmount` is exactly the amount, on every night and on the total.
   * The nights are summed once and the sum is never rounded again: a kuruş is the difference
   * between an invoice that reconciles and one that does not.
   */
  const quoteRoomType = (
    tenantId: string,
    providerProfileId: string,
    property: StoredProperty,
    room: StoredRoomType,
    nights: string[],
    cover: { eligible: boolean; nightsCarried: number; money: bigint | null },
  ): RoomTypeQuote => {
    const nightly: Schemas['AvailabilityNightAmount'][] = [];
    let currency = '';
    let reason: Schemas['QuoteUnavailableReason'] | null = null;
    let totalContract = ZERO;
    let totalPayer = ZERO;
    let totalMember = ZERO;
    let moneyLeft = cover.money ?? ZERO;
    // Counted where the plan is applied, not inferred from the figures afterwards.
    let carried = 0;

    nights.forEach((night, index) => {
      const resolution = resolveContractPrice(world(), tenantId, {
        serviceDate: night,
        providerProfileId,
        serviceDefinitionId: room.serviceDefinitionId,
        locationId: property.locationId,
      });
      if (resolution.result.outcome === 'NOT_FOUND') {
        reason ??= 'PRICE_NOT_FOUND';
        return;
      }
      if (resolution.result.outcome === 'REVIEW_REQUIRED' || !resolution.winner) {
        // Two equally specific contracted prices tied. The system does not choose between
        // them, because a random winner is a silent financial error nobody would ever see.
        reason ??= 'PRICE_AMBIGUOUS';
        return;
      }
      const contract = contractAmountMicros(resolution.winner);
      if (contract === null) {
        reason ??= 'PRICE_FORMULA_UNKNOWN';
        return;
      }
      const nightCurrency = resolution.currencyCode ?? '';
      if (currency === '') currency = nightCurrency;
      else if (currency !== nightCurrency) {
        // Two nights of one stay priced in two currencies. Adding lira to euros to produce
        // a total would be the one thing worse than saying so.
        reason ??= 'PRICE_CURRENCY_MISMATCH';
        return;
      }

      let payer: bigint;
      let member: bigint;
      if (cover.eligible && (cover.money !== null || index < cover.nightsCarried)) carried += 1;
      if (!cover.eligible) {
        // A member the plan does not cover may still choose to pay privately, so the night
        // is still priced; the payer simply carries none of it.
        payer = ZERO;
        member = contract;
      } else if (cover.money !== null) {
        // A MONEY-unit entitlement is a budget shared across the stay: the balance caps what
        // the plan carries and the member takes the difference.
        const wanted = contract - memberShareMicros(resolution.winner, contract);
        payer = wanted > moneyLeft ? moneyLeft : wanted;
        member = contract - payer;
        moneyLeft -= payer;
      } else if (index < cover.nightsCarried) {
        // A NIGHT-unit entitlement is a count: the plan carries the first `nightsCarried`
        // nights and the member carries the rest, whole.
        member = memberShareMicros(resolution.winner, contract);
        payer = contract - member;
      } else {
        payer = ZERO;
        member = contract;
      }

      totalContract += contract;
      totalPayer += payer;
      totalMember += member;
      nightly.push({
        stayDate: night,
        amount: fromMicros(contract),
        payerAmount: fromMicros(payer),
        memberAmount: fromMicros(member),
      });
    });

    if (reason !== null) return { quote: null, reason, coveredNights: 0 };
    if (currency === '') return { quote: null, reason: 'PRICE_NOT_FOUND', coveredNights: 0 };
    return {
      quote: {
        currencyCode: currency,
        nightlyAmounts: nightly,
        totalAmount: fromMicros(totalContract),
        payerAmount: fromMicros(totalPayer),
        memberAmount: fromMicros(totalMember),
      },
      reason: null,
      coveredNights: carried,
    };
  };

  return [
    http.get(`${ANY}/api/v1/accommodation/properties`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) {
        return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      }
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return validationFailed(api, [{ field: 'limit', code: 'RANGE' }]);
      }
      const errors: FieldError[] = [];
      const status = url.searchParams.get('status');
      const propertyType = url.searchParams.get('propertyType');
      if (status !== null && !STATUSES.has(status)) {
        errors.push({ field: 'status', code: 'ENUM', message: 'ACTIVE veya INACTIVE olmalı' });
      }
      if (propertyType !== null && !PROPERTY_TYPES.has(propertyType)) {
        errors.push({
          field: 'propertyType',
          code: 'ENUM',
          message: 'tanımlı bir tesis türü olmalı',
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const providerOrganizationId = url.searchParams.get('providerOrganizationId');
      const regionCode = url.searchParams.get('regionCode');
      const city = url.searchParams.get('city');
      // The back-office and provider list, so INACTIVE buildings are shown too. The member's
      // view is `searchAvailability`, which shows only what is ACTIVE and actually free.
      const rows = world()
        .properties.filter(
          (p) =>
            p.tenantId === g.tenantId &&
            visible(g.session, g.tenantId, p) &&
            (!providerOrganizationId || p.providerOrganizationId === providerOrganizationId) &&
            (status === null || p.status === status) &&
            (propertyType === null || p.propertyType === propertyType) &&
            (regionCode === null || p.regionCode === regionCode) &&
            (city === null || p.city === city),
        )
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      const page = rows.slice(offset, offset + limit);
      const body: Schemas['PropertyPage'] = {
        items: page.map(toProperty),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body);
    }),

    http.post(`${ANY}/api/v1/accommodation/properties`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_MANAGE, true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const key = `${g.tenantId}:${request.headers.get('Idempotency-Key')!}`;
      const replay = api.replay(key);
      if (replay) {
        return HttpResponse.json(replay.body as Schemas['Property'], {
          status: replay.status,
          headers: replay.etag ? { ETag: replay.etag } : {},
        });
      }
      const body = await readJson<Schemas['CreateProperty']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

      const errors: FieldError[] = [];
      const providerOrganizationId = (body.providerOrganizationId ?? '').trim();
      if (providerOrganizationId === '') {
        errors.push({
          field: 'providerOrganizationId',
          code: 'REQUIRED',
          message: 'sağlayıcı kurumu zorunlu',
        });
      }
      const code = (body.code ?? '').trim();
      if (!CODE.test(code)) {
        errors.push({
          field: 'code',
          code: 'FORMAT',
          message: 'A-Z, 0-9, _ ve - içeren en fazla 40 karakter olmalı',
        });
      }
      const status = body.status ?? 'ACTIVE';
      validatePropertyBody(errors, body, status);
      if (errors.length > 0) return validationFailed(api, errors);

      // The one write with no existing row to hide behind a 404: a provider clerk naming
      // somebody else's organization is told it may not write there rather than that the
      // organization does not exist, because it plainly does.
      const scope = organizationScope(api, g.session, g.tenantId);
      if (!withinScope(scope, providerOrganizationId)) {
        return problem(api, 403, 'PROPERTY_SCOPE', 'Bu sağlayıcı adına tesis tanımlayamazsınız', {
          detail: 'Yalnızca kendi kurumunuzun tesislerini yönetebilirsiniz.',
        });
      }
      const target = checkPropertyTargets(
        g.tenantId,
        providerOrganizationId,
        body.locationId ?? null,
      );
      if (target) return validationFailed(api, [target]);

      const row: StoredProperty = {
        id: world().nextId(),
        tenantId: g.tenantId,
        providerOrganizationId,
        locationId: body.locationId ?? null,
        code,
        name: (body.name ?? '').trim(),
        propertyType: body.propertyType,
        timezone: (body.timezone ?? '').trim(),
        city: body.city ?? null,
        regionCode: body.regionCode ?? null,
        amenities: normaliseAmenities(body.amenities),
        costCenter: body.costCenter ?? null,
        status,
        createdAt: new Date().toISOString(),
        updatedAt: null,
        rowVersion: 1,
      };
      world().properties.push(row);
      const out = toProperty(row);
      api.rememberIdempotent(key, 201, out, etagOf(1));
      return HttpResponse.json(out, {
        status: 201,
        headers: { ETag: etagOf(1), Location: `/api/v1/accommodation/properties/${row.id}` },
      });
    }),

    http.get(`${ANY}/api/v1/accommodation/properties/:propertyId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      const row = findProperty(g.session, g.tenantId, pathParam(params, 'propertyId'));
      // Another provider's property is 404 rather than 403.
      if (!row) return propertyNotFound(api);
      return HttpResponse.json(toProperty(row), { headers: { ETag: etagOf(row.rowVersion) } });
    }),

    http.patch(
      `${ANY}/api/v1/accommodation/properties/:propertyId`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_MANAGE, true);
        if ('error' in g) return g.error;
        // The Idempotency-Key middleware runs before the handler on the server, so a repeat
        // of a patch replays the answer rather than bumping the row version twice.
        const missing = requireIdempotencyKey(api, request);
        if (missing) return missing;
        const idempotency = `${g.tenantId}:${request.headers.get('Idempotency-Key')!}`;
        const replay = api.replay(idempotency);
        if (replay) {
          return HttpResponse.json(replay.body as Schemas['Property'], {
            status: replay.status,
            headers: replay.etag ? { ETag: replay.etag } : {},
          });
        }
        // The read next, so a property this caller may not see is 404 rather than a version
        // mismatch that would confirm it exists.
        const row = findProperty(g.session, g.tenantId, pathParam(params, 'propertyId'));
        if (!row) return propertyNotFound(api);
        const expected = requireIfMatch(api, request);
        if (typeof expected !== 'number') return expected;
        const body = await readJson<Schemas['PatchProperty']>(request);
        if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

        const errors: FieldError[] = [];
        validatePropertyBody(errors, body, body.status);
        if (errors.length > 0) return validationFailed(api, errors);
        const target = checkPropertyTargets(
          g.tenantId,
          row.providerOrganizationId,
          body.locationId ?? null,
        );
        if (target) return validationFailed(api, [target]);
        if (row.rowVersion !== expected) {
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti', {
            detail: 'Güncel sürümü alıp değişikliğinizi yeniden uygulayın.',
          });
        }

        // Every field is sent every time: a merge would make "this hotel no longer has a
        // location" and "it no longer bills a cost centre" inexpressible. `code` and
        // `providerOrganizationId` are not editable and are not read here.
        row.locationId = body.locationId ?? null;
        row.name = body.name.trim();
        row.propertyType = body.propertyType;
        row.timezone = body.timezone.trim();
        row.city = body.city ?? null;
        row.regionCode = body.regionCode ?? null;
        row.amenities = normaliseAmenities(body.amenities);
        row.costCenter = body.costCenter ?? null;
        row.status = body.status;
        row.updatedAt = new Date().toISOString();
        row.rowVersion += 1;
        const out = toProperty(row);
        api.rememberIdempotent(idempotency, 200, out, etagOf(row.rowVersion));
        return HttpResponse.json(out, { headers: { ETag: etagOf(row.rowVersion) } });
      },
    ),

    http.get(
      `${ANY}/api/v1/accommodation/properties/:propertyId/room-types`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_READ, false);
        if ('error' in g) return g.error;
        // The property is read first, so a building this caller may not see answers 404
        // rather than an empty list — which would say the building exists and has no rooms.
        const property = findProperty(g.session, g.tenantId, pathParam(params, 'propertyId'));
        if (!property) return propertyNotFound(api);
        const status = new URL(request.url).searchParams.get('status');
        if (status !== null && !STATUSES.has(status)) {
          return validationFailed(api, [
            { field: 'status', code: 'ENUM', message: 'ACTIVE veya INACTIVE olmalı' },
          ]);
        }
        const body: Schemas['RoomTypeList'] = {
          items: world()
            .roomTypes.filter(
              (r) =>
                r.tenantId === g.tenantId &&
                r.propertyId === property.id &&
                (status === null || r.status === status),
            )
            .sort((a, b) => a.code.localeCompare(b.code))
            .map(toRoomType),
        };
        return HttpResponse.json(body);
      },
    ),

    http.post(
      `${ANY}/api/v1/accommodation/properties/:propertyId/room-types`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_MANAGE, true);
        if ('error' in g) return g.error;
        const missing = requireIdempotencyKey(api, request);
        if (missing) return missing;
        const key = `${g.tenantId}:${request.headers.get('Idempotency-Key')!}`;
        const replay = api.replay(key);
        if (replay) {
          return HttpResponse.json(replay.body as Schemas['RoomType'], {
            status: replay.status,
            headers: replay.etag ? { ETag: replay.etag } : {},
          });
        }
        const property = findProperty(g.session, g.tenantId, pathParam(params, 'propertyId'));
        if (!property) return propertyNotFound(api);
        const body = await readJson<Schemas['CreateRoomType']>(request);
        if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

        const errors: FieldError[] = [];
        const code = (body.code ?? '').trim();
        if (!CODE.test(code)) {
          errors.push({
            field: 'code',
            code: 'FORMAT',
            message: 'A-Z, 0-9, _ ve - içeren en fazla 40 karakter olmalı',
          });
        }
        const serviceDefinitionId = (body.serviceDefinitionId ?? '').trim();
        if (serviceDefinitionId === '') {
          errors.push({
            field: 'serviceDefinitionId',
            code: 'REQUIRED',
            message: 'hizmet tanımı zorunlu',
          });
        }
        const status = body.status ?? 'ACTIVE';
        validateRoomTypeBody(errors, body, status);
        if (errors.length > 0) return validationFailed(api, errors);
        const service = checkRoomTypeService(g.tenantId, serviceDefinitionId);
        if (service) return validationFailed(api, [service]);

        const row: StoredRoomType = {
          id: world().nextId(),
          tenantId: g.tenantId,
          propertyId: property.id,
          code,
          name: body.name.trim(),
          maxAdults: body.maxAdults,
          maxChildren: body.maxChildren ?? 0,
          maxOccupancy: body.maxOccupancy,
          attributes: body.attributes ?? {},
          serviceDefinitionId,
          status,
          createdAt: new Date().toISOString(),
          updatedAt: null,
          rowVersion: 1,
        };
        world().roomTypes.push(row);
        const out = toRoomType(row);
        api.rememberIdempotent(key, 201, out, etagOf(1));
        return HttpResponse.json(out, {
          status: 201,
          headers: { ETag: etagOf(1), Location: `/api/v1/accommodation/room-types/${row.id}` },
        });
      },
    ),

    http.patch(
      `${ANY}/api/v1/accommodation/room-types/:roomTypeId`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_MANAGE, true);
        if ('error' in g) return g.error;
        const missing = requireIdempotencyKey(api, request);
        if (missing) return missing;
        const idempotency = `${g.tenantId}:${request.headers.get('Idempotency-Key')!}`;
        const replay = api.replay(idempotency);
        if (replay) {
          return HttpResponse.json(replay.body as Schemas['RoomType'], {
            status: replay.status,
            headers: replay.etag ? { ETag: replay.etag } : {},
          });
        }
        const row = findRoomType(g.session, g.tenantId, pathParam(params, 'roomTypeId'));
        if (!row) return roomTypeNotFound(api);
        const expected = requireIfMatch(api, request);
        if (typeof expected !== 'number') return expected;
        const body = await readJson<Schemas['PatchRoomType']>(request);
        if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

        const errors: FieldError[] = [];
        validateRoomTypeBody(errors, body, body.status);
        if (errors.length > 0) return validationFailed(api, errors);
        if (row.rowVersion !== expected) {
          return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti', {
            detail: 'Güncel sürümü alıp değişikliğinizi yeniden uygulayın.',
          });
        }

        // `serviceDefinitionId` is not among the editable fields and never will be: a room
        // type re-pointed at another service would silently change what every booking
        // already taken against it was priced and entitled as.
        row.name = body.name.trim();
        row.maxAdults = body.maxAdults;
        row.maxChildren = body.maxChildren ?? 0;
        row.maxOccupancy = body.maxOccupancy;
        row.attributes = body.attributes ?? {};
        row.status = body.status;
        row.updatedAt = new Date().toISOString();
        row.rowVersion += 1;
        const out = toRoomType(row);
        api.rememberIdempotent(idempotency, 200, out, etagOf(row.rowVersion));
        return HttpResponse.json(out, { headers: { ETag: etagOf(row.rowVersion) } });
      },
    ),

    http.get(
      `${ANY}/api/v1/accommodation/room-types/:roomTypeId/inventory`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_READ, false);
        if ('error' in g) return g.error;
        const url = new URL(request.url);
        const from = url.searchParams.get('from');
        const to = url.searchParams.get('to');
        const errors = validateInventoryRange(from, to);
        if (errors.length > 0) return validationFailed(api, errors);
        const row = findRoomType(g.session, g.tenantId, pathParam(params, 'roomTypeId'));
        if (!row) return roomTypeNotFound(api);
        return HttpResponse.json(
          inventoryRangeBody(g.tenantId, row.id, from as string, to as string),
        );
      },
    ),

    http.put(
      `${ANY}/api/v1/accommodation/room-types/:roomTypeId/inventory`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_MANAGE, true);
        if ('error' in g) return g.error;
        const rawKey = request.headers.get('Idempotency-Key');
        const key = rawKey ? `${g.tenantId}:${rawKey}` : null;
        if (key) {
          const replay = api.replay(key);
          if (replay) {
            return HttpResponse.json(replay.body as Schemas['RoomTypeInventoryRange'], {
              status: replay.status,
            });
          }
        }
        const body = await readJson<Schemas['PutRoomTypeInventory']>(request);
        if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
        const errors = validateInventoryRange(body.from, body.to);
        const capacity = Number(body.capacity);
        if (!Number.isInteger(capacity) || capacity < 0 || capacity > MAX_CAPACITY) {
          errors.push({
            field: 'capacity',
            code: 'RANGE',
            message: `0-${MAX_CAPACITY} arasında olmalı`,
          });
        }
        if (errors.length > 0) return validationFailed(api, errors);
        const row = findRoomType(g.session, g.tenantId, pathParam(params, 'roomTypeId'));
        if (!row) return roomTypeNotFound(api);

        const dates = datesInclusive(body.from, body.to);
        const existing = new Map(
          world()
            .inventoryDays.filter((d) => d.tenantId === g.tenantId && d.roomTypeId === row.id)
            .map((d) => [d.stayDate, d] as const),
        );
        // The first night the new capacity would fall under, in date order. The whole range
        // is refused with it and nothing is written: an allotment meant to apply to a season
        // is not half applied, and a provider opening ninety nights needs to be told which
        // night to look at rather than that something somewhere failed.
        for (const day of dates) {
          const night = existing.get(day);
          if (night && night.held + night.confirmed > capacity) {
            return problem(
              api,
              409,
              'INVENTORY_BELOW_COMMITMENT',
              'Kontenjan, verilmiş sözden az olamaz',
              {
                detail:
                  'Bu gecede zaten tutulmuş veya onaylanmış oda sayısı, girilen kontenjandan fazla.',
                extensions: {
                  stayDate: night.stayDate,
                  capacity,
                  held: night.held,
                  confirmed: night.confirmed,
                },
              },
            );
          }
        }

        const now = new Date().toISOString();
        for (const day of dates) {
          const night = existing.get(day);
          if (night) {
            // `held` and `confirmed` are never touched here: they belong to the holds and
            // bookings of WP-I6-02, and an allotment that could edit them would be an
            // allotment that could make a confirmed booking disappear.
            night.capacity = capacity;
            night.updatedAt = now;
            night.rowVersion += 1;
            continue;
          }
          world().inventoryDays.push({
            id: world().nextId(),
            tenantId: g.tenantId,
            roomTypeId: row.id,
            stayDate: day,
            capacity,
            held: 0,
            confirmed: 0,
            updatedAt: now,
            rowVersion: 1,
          });
        }
        const out = inventoryRangeBody(g.tenantId, row.id, body.from, body.to);
        if (key) api.rememberIdempotent(key, 200, out, null);
        return HttpResponse.json(out);
      },
    ),

    http.post(`${ANY}/api/v1/accommodation/availability/search`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, true);
      if ('error' in g) return g.error;
      const rawKey = request.headers.get('Idempotency-Key');
      const key = rawKey ? `${g.tenantId}:${rawKey}` : null;
      if (key) {
        const replay = api.replay(key);
        if (replay) {
          return HttpResponse.json(replay.body as Schemas['AvailabilitySearchResult'], {
            status: replay.status,
          });
        }
      }
      const body = await readJson<Schemas['AvailabilitySearchRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

      // Whose stay. On the server a member account is bound to one person and the person is
      // taken from that binding, never from the body: a body naming somebody else is refused
      // with PERSON_SCOPE, which is what stops a member searching — and later holding — a
      // room for their neighbour. None of the mock's seeded accounts carries a PERSON grant,
      // so every caller here is a desk, and a desk has to name the person it is acting for.
      const personId = (body.personId ?? '').trim();
      if (personId === '') {
        return problem(api, 422, 'PERSON_REQUIRED', 'Sorgu bir hak sahibi adına yapılmalı', {
          detail: 'Hangi hak sahibi için sorguladığınızı personId ile belirtin.',
        });
      }

      const errors: FieldError[] = [];
      const named = [body.propertyId, body.regionCode].filter(
        (v) => typeof v === 'string' && v !== '',
      ).length;
      if (named !== 1) {
        errors.push({
          field: 'propertyId',
          code: 'REQUIRED',
          message: 'tesis veya bölge kodundan yalnız biri verilmeli',
        });
      }
      const adults = Number(body.adults);
      const children = Number(body.children ?? 0);
      if (!Number.isInteger(adults) || adults < 1 || adults > 20) {
        errors.push({ field: 'adults', code: 'RANGE', message: '1-20 arasında olmalı' });
      }
      if (!Number.isInteger(children) || children < 0 || children > 20) {
        errors.push({ field: 'children', code: 'RANGE', message: '0-20 arasında olmalı' });
      }
      if (!validDate(body.checkIn)) {
        errors.push({ field: 'checkIn', code: 'REQUIRED', message: 'giriş tarihi zorunlu' });
      }
      if (!validDate(body.checkOut)) {
        errors.push({ field: 'checkOut', code: 'REQUIRED', message: 'çıkış tarihi zorunlu' });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const checkIn = body.checkIn;
      const checkOut = body.checkOut;
      const nights = nightsOf(checkIn, checkOut);
      if (nights === 0) {
        // `checkOut` is the morning the guest leaves and never a night, so the same date is
        // a stay of no nights rather than a stay of one.
        return validationFailed(api, [
          { field: 'checkOut', code: 'RANGE', message: 'giriş tarihinden sonra olmalı' },
        ]);
      }
      if (nights > DEFAULT_MAX_NIGHTS) {
        return validationFailed(api, [
          {
            field: 'checkOut',
            code: 'RANGE',
            message: `en fazla ${DEFAULT_MAX_NIGHTS} gece sorgulanabilir`,
          },
        ]);
      }

      const stayDates = datesInclusive(checkIn, addDays(checkIn, nights - 1));
      const lastNight = stayDates[stayDates.length - 1]!;
      const properties = searchableProperties(
        g.session,
        g.tenantId,
        personId,
        body.programId ?? null,
        checkIn,
        lastNight,
      );
      const propertyById = new Map(properties.map((p) => [p.property.id, p] as const));
      const rooms = world()
        .roomTypes.filter(
          (r) =>
            r.tenantId === g.tenantId &&
            r.status === 'ACTIVE' &&
            propertyById.has(r.propertyId) &&
            // The room has to fit the party. A room that sleeps two is not an answer to a
            // question about four.
            r.maxAdults >= adults &&
            r.maxChildren >= children &&
            r.maxOccupancy >= adults + children,
        )
        .sort((a, b) => a.propertyId.localeCompare(b.propertyId) || a.code.localeCompare(b.code));

      const answer = (result: Schemas['AvailabilitySearchResult']): Response => {
        if (key) api.rememberIdempotent(key, 200, result, null);
        return HttpResponse.json(result);
      };

      if (rooms.length === 0) {
        // No room type matched, so there was no service to evaluate and no evaluation to
        // name. Nothing is recorded either: an empty answer is not an eligibility decision.
        return answer({
          personId,
          checkIn,
          checkOut,
          nights,
          evaluationId: null,
          eligible: false,
          entitlement: null,
          results: [],
        });
      }

      // One eligibility check for the whole search, through the same resolver the check
      // endpoint uses, and its evaluation is what the answer is remembered by. The quantity
      // asked about is one night rather than the length of the stay, and that is the point:
      // the answer then separates "this person may not use this benefit at all" from "they
      // may, and they have fewer nights left than they asked for".
      const definitionIds = [...new Set(rooms.map((r) => r.serviceDefinitionId))];
      const check: EligibilityCheckRequest = {
        personId,
        programId: body.programId ?? null,
        serviceDate: checkIn,
        serviceItems: definitionIds.map((id) => ({
          serviceDefinitionId: id,
          quantity: '1.000000',
        })),
        context: { domain: 'ACCOMMODATION' },
      };
      const evaluationId = world().nextId();
      const verdict = resolveEligibility(world(), g.tenantId, check, evaluationId);
      const stored: StoredEvaluation = {
        id: evaluationId,
        tenantId: g.tenantId,
        personId,
        programId: body.programId ?? null,
        planVersionId: verdict.planVersionId ?? null,
        enrollmentId: verdict.enrollmentId ?? null,
        serviceDate: checkIn,
        evaluatedAt: verdict.evaluatedAt,
        evaluatedBy: g.session.account.actorId,
        outcome: verdict.outcome,
        request: check,
        result: verdict,
        ...(rawKey ? { idempotencyKey: rawKey } : {}),
      };
      world().evaluations.set(evaluationId, stored);

      const unitByCode = new Map(
        (verdict.balances ?? []).map((b) => [b.entitlementCode, b.unit] as const),
      );
      const cover = new Map<
        string,
        { eligible: boolean; nightsCarried: number; money: bigint | null }
      >();
      let entitlement: Schemas['AvailabilityEntitlement'] | null = null;
      for (const item of verdict.items ?? []) {
        const definitionId = definitionIds[item.index];
        if (definitionId === undefined) continue;
        const eligible = item.outcome === 'ELIGIBLE';
        const code = item.entitlementCode ?? null;
        const available = item.availableQuantity ?? null;
        if (code === null || available === null || available === undefined) {
          cover.set(definitionId, { eligible, nightsCarried: 0, money: null });
          continue;
        }
        const unit = unitByCode.get(code) ?? '';
        cover.set(definitionId, {
          eligible,
          // A NIGHT entitlement limits how many nights the plan carries, not how much money:
          // a member with two nights left has two nights left, not two lira.
          nightsCarried: unit === 'NIGHT' ? wholeNights(available) : 0,
          money: unit === 'NIGHT' ? null : toMicros(available),
        });
        entitlement ??= { entitlementCode: code, unit, remaining: available };
      }

      // Two nights left and a three-night stay is not "eligible with a caveat": the third
      // night is the member's to pay, and the flag says so rather than letting a screen
      // discover it in the figures.
      let eligible = false;
      for (const [, value] of cover) {
        if (!value.eligible) continue;
        if (value.money !== null || value.nightsCarried >= nights) eligible = true;
      }

      const results: Schemas['AvailabilityRoomTypeResult'][] = rooms.map((room) => {
        const entry = propertyById.get(room.propertyId)!;
        const priced = quoteRoomType(
          g.tenantId,
          entry.providerProfileId,
          entry.property,
          room,
          stayDates,
          cover.get(room.serviceDefinitionId) ?? {
            eligible: false,
            nightsCarried: 0,
            money: null,
          },
        );
        return {
          property: toProperty(entry.property),
          roomType: toRoomType(room),
          available: availabilityOver(g.tenantId, room.id, stayDates),
          quote: priced.quote,
          quoteUnavailableReason: priced.reason,
        };
      });

      return answer({
        personId,
        checkIn,
        checkOut,
        nights,
        evaluationId,
        eligible,
        entitlement,
        results,
      });
    }),

    // The hold and the booking (WP-I6-02). They live in their own module and are handed the
    // four search helpers above rather than copying them: a second pricing ladder or a second
    // provider boundary here is exactly the divergence the mock exists to catch.
    ...bookingHandlers(api, {
      findRoomType,
      findProperty,
      searchableProperties,
      quoteRoomType,
      coverForService,
    }),
  ];
}
