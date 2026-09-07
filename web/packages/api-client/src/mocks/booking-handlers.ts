/**
 * MSW handlers for the hold and the booking (WP-I6-02): setting a room aside with a
 * countdown, giving it back, confirming it through the request gate, and the voucher a
 * member shows at the desk.
 *
 * The mock is a test double of the Go server and M6 treats a divergence in either direction
 * as a bug. Seven things here are transcriptions rather than re-implementations:
 *
 *   - **the counters**: a hold increments `held` on every night of the stay and never
 *     touches `confirmed`; a confirmation moves one room from `held` to `confirmed` on every
 *     night; a release or an expiry decrements `held`. `held + confirmed <= capacity` is a
 *     CHECK on the server's row and nothing here is allowed to break it either, which is why
 *     the availability of every night is read before anything is written and the whole hold
 *     is refused with the first full night;
 *   - **`ROOM_UNAVAILABLE` names a night**: a member looking at a fortnight is told which
 *     night to move, and a night with no allotment row is not "zero free" — it is no
 *     allotment, and `allotted: false` says which of the two it is;
 *   - **one live booking per person, room type and arrival**: the server states it as a
 *     partial unique index over four live statuses, so a cancelled or expired booking must
 *     not stop the member booking the same room again;
 *   - **the frozen quote**: it is written once, at the hold, and never recomputed. Confirming
 *     charges what the member saw, and a quote older than an hour is refused with
 *     `QUOTE_STALE` rather than silently repriced;
 *   - **the countdown**: `secondsToExpiry` is computed by the server and never derived from
 *     two timestamps by a browser whose clock is out. A hold past its deadline is swept to
 *     `EXPIRED` before any read answers, which is how the sweep is "advanceable" here: a test
 *     moves `holdExpiresAt` into the past and the next call finds the room already back;
 *   - **the policy**: a confirmation freezes the contract version's lodging terms, and a
 *     version with none is refused with `LODGING_TERMS_MISSING`. A stay agreed with no
 *     cancellation policy is a stay nobody could cancel fairly, and no default is invented;
 *   - **the voucher**: the token is returned once, by the command that mints it, and appears
 *     in no other body. Reissuing retires the previous one in the same breath, so exactly one
 *     code works at any moment — and that route carries no Idempotency-Key, because the
 *     replay store keeps response bodies and this response is the one place a usable token
 *     exists.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  type MockWorld,
  type StoredBooking,
  type StoredBookingGuest,
  type StoredBookingNight,
  type StoredProperty,
  type StoredRoomType,
} from './data';
import type { AfterTools } from './after-handlers';
import type { MockApi, MockSession } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  guardTenant,
  organizationScope,
  parseLimit,
  personScope,
  pathParam,
  problem,
  readJson,
  resolvePerson,
  validationFailed,
  wait,
  withinScope,
  type FieldError,
  type Schemas,
} from './handlers';

const PERMISSION_READ = 'accommodation.property.read';
/**
 * Two grants and neither is a superset of the other: `create` is what a member holds so they
 * may book for themselves, and `manage` is what a reservation desk holds, which is the same
 * commands for somebody else. Whose stay it is remains the person binding's answer.
 */
const PERMISSION_BOOK = 'accommodation.booking.create';
const PERMISSION_BOOKING_MANAGE = 'accommodation.booking.manage';

/** accommodation.hold_minutes and accommodation.quote_ttl_minutes for a tenant with no rows. */
const DEFAULT_HOLD_MINUTES = 15;
const DEFAULT_QUOTE_TTL_MINUTES = 60;
const DEFAULT_MAX_NIGHTS = 30;

const DAY_MS = 86_400_000;
const DATE_ONLY = /^\d{4}-\d{2}-\d{2}$/;

/** The statuses in which a booking still claims a room. */
const LIVE_STATUSES = new Set<string>(['HOLD', 'PENDING_APPROVAL', 'CONFIRMED', 'CHECKED_IN']);
/** The statuses whose rooms are counted in `held` rather than in `confirmed`. */
const HELD_STATUSES = new Set<string>(['HOLD', 'PENDING_APPROVAL']);
const GUEST_TYPES = new Set<string>(['MEMBER', 'DEPENDANT', 'GUEST']);
const CHANNELS = new Set<string>([
  'BACKOFFICE',
  'PROVIDER_PORTAL',
  'MEMBER_PORTAL',
  'API',
  'BATCH_IMPORT',
  'CALL_CENTER',
]);

/**
 * The half of the availability search this module borrows rather than reimplements: which
 * buildings this member's programmes reach, what is free over a range, and what a stay costs.
 *
 * They are passed in rather than exported from the search module because they are closures
 * over the same `api`, and a second copy of the pricing ladder here is exactly the divergence
 * the mock exists to avoid.
 */
export interface BookingTools {
  findRoomType(session: MockSession, tenantId: string, id: string): StoredRoomType | undefined;
  findProperty(session: MockSession, tenantId: string, id: string): StoredProperty | undefined;
  searchableProperties(
    session: MockSession,
    tenantId: string,
    personId: string,
    programId: string | null,
    checkIn: string,
    lastNight: string,
  ): { property: StoredProperty; providerProfileId: string }[];
  quoteRoomType(
    tenantId: string,
    providerProfileId: string,
    property: StoredProperty,
    room: StoredRoomType,
    nights: string[],
    cover: { eligible: boolean; nightsCarried: number; money: bigint | null },
  ): {
    quote: Schemas['AvailabilityQuote'] | null;
    reason: Schemas['QuoteUnavailableReason'] | null;
    coveredNights: number;
  };
  coverForService(
    tenantId: string,
    personId: string,
    programId: string | null,
    serviceDate: string,
    serviceDefinitionId: string,
  ): { eligible: boolean; nightsCarried: number; money: bigint | null };
}

function addDays(date: string, days: number): string {
  return new Date(Date.parse(`${date}T00:00:00Z`) + days * DAY_MS).toISOString().slice(0, 10);
}

/** Nights of [checkIn, checkOut), counted on the calendar and never by dividing a duration. */
function nightsOf(checkIn: string, checkOut: string): number {
  const from = Date.parse(`${checkIn}T00:00:00Z`);
  const to = Date.parse(`${checkOut}T00:00:00Z`);
  if (Number.isNaN(from) || Number.isNaN(to) || to <= from) return 0;
  return Math.round((to - from) / DAY_MS);
}

/**
 * A voucher's plaintext, derived from its own id.
 *
 * The tail of the id and not its head: the mock's ids are a sequence, so two vouchers minted
 * moments apart share a leading prefix, and a token built from that prefix would be the same
 * string for both. A rotation that handed the member the code it had just retired would look
 * like it worked and would not be one.
 */
function voucherToken(voucherId: string): string {
  return `KPS-${voucherId.replace(/-/g, '').slice(-10).toUpperCase()}`;
}

function stayDatesOf(checkIn: string, nights: number): string[] {
  return Array.from({ length: nights }, (_, i) => addDays(checkIn, i));
}

/**
 * The handlers, and the four things WP-I6-03 needs to reach into them.
 *
 * They are handed out rather than reimplemented next to the offer sweep: a hold placed by the
 * sweep and a hold placed at the search screen are the same act, and two placements would be
 * two answers to "may this room be set aside" that drift the first time either is corrected.
 */
export interface BookingModule {
  handlers: HttpHandler[];
  after: AfterTools;
}

export function bookingHandlers(api: MockApi, tools: BookingTools): BookingModule {
  const world = (): MockWorld => api.world;

  const visible = (session: MockSession, tenantId: string, booking: StoredBooking): boolean => {
    // The member boundary first: an account bound to a person sees that person's bookings
    // and nobody else's, whatever it asks for. It is computed here rather than applied as a
    // filter afterwards, because a filter applied after a page has already been defeated.
    const bound = personScope(api, session, tenantId);
    if (bound !== null && booking.personId !== bound) return false;
    const scope = organizationScope(api, session, tenantId);
    if (scope === null) return true;
    const property = world().properties.find((p) => p.id === booking.propertyId);
    return property ? withinScope(scope, property.providerOrganizationId) : false;
  };

  /**
   * The expiry sweep, run before anything is read or written.
   *
   * On the server this is a scheduler job that runs every minute; here it runs on demand,
   * which is what makes it advanceable in a test: move a hold's `holdExpiresAt` into the past
   * and the very next call finds the room and the nights already back. The effect is the
   * same either way, and so is the idempotency — a booking that is no longer `HOLD` is passed
   * over, so nothing is given back twice.
   */
  const sweepExpiredHolds = (tenantId: string): void => {
    const now = Date.now();
    for (const booking of world().bookings) {
      if (booking.tenantId !== tenantId) continue;
      if (booking.status !== 'HOLD') continue;
      if (!booking.holdExpiresAt || Date.parse(booking.holdExpiresAt) > now) continue;
      moveHeld(booking, -1);
      booking.status = 'EXPIRED';
      booking.holdExpiresAt = null;
      booking.updatedAt = new Date().toISOString();
      booking.rowVersion += 1;
    }
  };

  /** Moves `held` by a signed delta over every night of the stay. */
  const moveHeld = (booking: StoredBooking, delta: number): void => {
    for (const day of stayDatesOf(booking.checkIn, booking.nights)) {
      const night = world().inventoryDays.find(
        (d) =>
          d.tenantId === booking.tenantId &&
          d.roomTypeId === booking.roomTypeId &&
          d.stayDate === day,
      );
      if (night) night.held = Math.max(0, night.held + delta);
    }
  };

  /**
   * Moves `confirmed` by a signed delta over the stay's nights, optionally from an offset.
   *
   * The offset is what an early check-out needs: the nights the guest actually slept stay
   * taken, because the room really was occupied on them, and only the tail goes back to the
   * allotment where the search can sell it again.
   */
  const moveConfirmed = (booking: StoredBooking, delta: number, from = 0): void => {
    for (const day of stayDatesOf(booking.checkIn, booking.nights).slice(from)) {
      const night = world().inventoryDays.find(
        (d) =>
          d.tenantId === booking.tenantId &&
          d.roomTypeId === booking.roomTypeId &&
          d.stayDate === day,
      );
      if (night) night.confirmed = Math.max(0, night.confirmed + delta);
    }
  };

  /** One room stops being held and starts being taken, on every night, in one step. */
  const confirmNights = (booking: StoredBooking): void => {
    for (const day of stayDatesOf(booking.checkIn, booking.nights)) {
      const night = world().inventoryDays.find(
        (d) =>
          d.tenantId === booking.tenantId &&
          d.roomTypeId === booking.roomTypeId &&
          d.stayDate === day,
      );
      if (!night) continue;
      night.held = Math.max(0, night.held - 1);
      night.confirmed += 1;
    }
  };

  const secondsToExpiry = (booking: StoredBooking): number => {
    if (!HELD_STATUSES.has(booking.status) || !booking.holdExpiresAt) return 0;
    const left = Math.floor((Date.parse(booking.holdExpiresAt) - Date.now()) / 1000);
    return left > 0 ? left : 0;
  };

  const toBooking = (booking: StoredBooking): Schemas['Booking'] => {
    const { tenantId: _tenantId, ...rest } = booking;
    return {
      ...rest,
      secondsToExpiry: secondsToExpiry(booking),
      nightlyAmounts: world()
        .bookingNights.filter((n) => n.bookingId === booking.id)
        .sort((a, b) => a.stayDate.localeCompare(b.stayDate))
        .map(({ id: _id, tenantId: _t, bookingId: _b, ...night }) => night),
      guests: world()
        .bookingGuests.filter((g) => g.bookingId === booking.id)
        .map(({ tenantId: _t, bookingId: _b, ...guest }) => guest),
    };
  };

  const findBooking = (
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredBooking | undefined => {
    const row = world().bookings.find((b) => b.id === id && b.tenantId === tenantId);
    if (!row) return undefined;
    return visible(session, tenantId, row) ? row : undefined;
  };

  const bookingNotFound = (): Response =>
    problem(api, 404, 'BOOKING_NOT_FOUND', 'Rezervasyon bulunamadı');

  const transitionInvalid = (): Response =>
    problem(
      api,
      409,
      'BOOKING_TRANSITION_INVALID',
      'Rezervasyon bu işlem için uygun durumda değil',
      { detail: 'Rezervasyonun güncel durumunu alıp yeniden deneyin.' },
    );

  const guardBooking = (request: Request, mutation: boolean) => {
    const first = guardTenant(api, request, PERMISSION_BOOK, mutation);
    if (!('error' in first)) return first;
    return guardTenant(api, request, PERMISSION_BOOKING_MANAGE, mutation);
  };

  /**
   * The hold itself, without the HTTP around it: the counters, the frozen quote, the booking
   * and its nights and guests.
   *
   * It is a function rather than the body of one handler because the waitlist offer job
   * (WP-I6-03) places a hold on a waiting member's behalf, and it has to be *this* hold --
   * the same availability check, the same one-live-booking rule, the same frozen quote. A
   * second placement written next to the sweep would be a second answer to "may this room be
   * set aside", and the two would drift the first time either was corrected.
   *
   * It returns a Response for every refusal, so the caller that has one to send sends it and
   * the sweep, which has nobody to tell, reads it as "not this entry's turn" and moves on.
   */
  const placeHold = (
    session: MockSession,
    tenantId: string,
    personId: string,
    roomTypeId: string,
    checkIn: string,
    checkOut: string,
    adults: number,
    children: number,
    nights: number,
    guests: Schemas['CreateBookingGuest'][],
    programId?: string | null,
    channel?: Schemas['ServiceRequestChannel'],
  ): StoredBooking | Response => {
    const room = tools.findRoomType(session, tenantId, roomTypeId);
    if (!room || room.status !== 'ACTIVE') {
      return problem(api, 404, 'ROOM_TYPE_NOT_FOUND', 'Oda tipi bulunamadı');
    }
    if (
      adults > room.maxAdults ||
      children > room.maxChildren ||
      adults + children > room.maxOccupancy
    ) {
      return problem(api, 422, 'OCCUPANCY_EXCEEDED', 'Kişi sayısı bu oda tipine sığmıyor', {
        detail: 'Yetişkin, çocuk ve toplam kişi sınırlarını aşmayan bir oda tipi seçin.',
      });
    }
    const enrollment = world().enrollments.find(
      (e) =>
        e.tenantId === tenantId &&
        e.personId === personId &&
        e.status === 'ACTIVE' &&
        (!programId || e.programId === programId),
    );
    if (!enrollment) {
      return problem(api, 422, 'ENROLLMENT_NOT_FOUND', 'Bu tarihlerde geçerli bir plan kaydı yok', {
        detail: 'Rezervasyon, giriş tarihinde aktif bir plan kaydı üzerinden yapılır.',
      });
    }

    const stayDates = stayDatesOf(checkIn, nights);
    const lastNight = stayDates[stayDates.length - 1]!;
    const reachable = tools.searchableProperties(
      session,
      tenantId,
      personId,
      programId ?? null,
      checkIn,
      lastNight,
    );
    const entry = reachable.find((p) => p.property.id === room.propertyId);
    if (!entry) {
      // No contract with a payer behind this person's programmes covers the stay. It is
      // the same answer the search gives them, and for the same reason.
      return problem(api, 404, 'PROPERTY_NOT_FOUND', 'Tesis bulunamadı');
    }

    // One live booking of one room type per person and arrival. The server states it as a
    // partial unique index, so the four live statuses are the whole of the rule: a
    // cancelled or expired booking must not stop the member booking the same room again.
    const clash = world().bookings.find(
      (b) =>
        b.tenantId === tenantId &&
        b.personId === personId &&
        b.roomTypeId === room.id &&
        b.checkIn === checkIn &&
        LIVE_STATUSES.has(b.status),
    );
    if (clash) {
      return problem(
        api,
        409,
        'BOOKING_ALREADY_LIVE',
        'Bu tarihte bu oda tipinde açık bir rezervasyonunuz var',
        {
          detail:
            'Aynı kişi, aynı oda tipi ve aynı giriş tarihi için tek bir açık rezervasyon olabilir.',
        },
      );
    }

    // The nights, checked before anything is written. The first one with no room refuses
    // the whole stay and names itself: a member looking at a fortnight needs to know which
    // night to move, and `allotted` separates a full night from one nobody opened.
    for (const day of stayDates) {
      const night = world().inventoryDays.find(
        (d) => d.tenantId === tenantId && d.roomTypeId === room.id && d.stayDate === day,
      );
      if (!night || night.capacity - night.held - night.confirmed < 1) {
        return problem(api, 409, 'ROOM_UNAVAILABLE', 'Bu tarihlerde boş oda yok', {
          detail: 'Konaklamanın en az bir gecesinde bu oda tipinden boş oda kalmadı.',
          extensions: {
            stayDate: day,
            allotted: Boolean(night),
            capacity: night?.capacity ?? 0,
            held: night?.held ?? 0,
            confirmed: night?.confirmed ?? 0,
          },
        });
      }
    }

    // What the plan carries, decided the same way the search decides it. A member with
    // two nights left and a three-night stay holds the room and reserves two: the search
    // has already told them the third is theirs, and refusing here would refuse exactly
    // the booking they were quoted.
    const cover = tools.coverForService(
      tenantId,
      personId,
      programId ?? null,
      checkIn,
      room.serviceDefinitionId,
    );
    const priced = tools.quoteRoomType(
      tenantId,
      entry.providerProfileId,
      entry.property,
      room,
      stayDates,
      cover,
    );
    if (!priced.quote) {
      return problem(api, 409, 'QUOTE_UNAVAILABLE', 'Bu tarihler için fiyat bulunamadı', {
        detail: 'Sözleşmede bu oda tipi için konaklamanın tüm gecelerini kapsayan fiyat yok.',
        extensions: { reason: priced.reason ?? 'PRICE_NOT_FOUND' },
      });
    }
    const quote = priced.quote;
    // A stay the plan carries no night of is the one refusal. Fewer nights than the stay
    // is long is a booking, not an error.
    if (priced.coveredNights === 0) {
      return problem(
        api,
        409,
        'ENTITLEMENT_INSUFFICIENT',
        'Planınız bu konaklamanın hiçbir gecesini karşılamıyor',
        {
          detail:
            'Bu hizmet planınızda tanımlı değil ya da konaklama hakkınız tükendi; ' +
            'planın karşılamadığı bir konaklama bu ekrandan rezerve edilemez.',
        },
      );
    }

    const now = new Date();
    const bookingId = world().nextId();
    const booking: StoredBooking = {
      id: bookingId,
      tenantId: tenantId,
      reference: `BK-${now.toISOString().slice(0, 10).replace(/-/g, '')}-${bookingId
        .replace(/-/g, '')
        .slice(0, 8)
        .toUpperCase()}`,
      personId,
      enrollmentId: enrollment.id,
      programId: enrollment.programId,
      propertyId: room.propertyId,
      roomTypeId: room.id,
      checkIn: checkIn,
      checkOut: checkOut,
      nights,
      adults,
      children,
      status: 'HOLD',
      holdExpiresAt: new Date(now.getTime() + DEFAULT_HOLD_MINUTES * 60_000).toISOString(),
      entitlementReservationId: world().nextId(),
      serviceRequestId: null,
      authorizationId: null,
      voucherId: null,
      // Frozen here and never recomputed. Confirming charges these figures, which is the
      // whole reason a stale one is refused rather than quietly repriced.
      quoteSnapshot: {
        version: 1,
        quotedAt: now.toISOString(),
        evaluationId: null,
        propertyId: room.propertyId,
        roomTypeId: room.id,
        serviceDefinitionId: room.serviceDefinitionId,
        currencyCode: quote.currencyCode,
        totalAmount: quote.totalAmount,
        payerAmount: quote.payerAmount,
        memberAmount: quote.memberAmount,
        nights: quote.nightlyAmounts.map((n) => ({
          stayDate: n.stayDate,
          amount: n.amount,
          payerAmount: n.payerAmount,
          memberAmount: n.memberAmount,
        })),
        coveredNights: priced.coveredNights,
        entitlement: null,
        // Whether the plan covers *every* night, which is not the same as covering some.
        eligible: priced.coveredNights === nights,
      },
      policySnapshot: null,
      channel: channel ?? 'BACKOFFICE',
      confirmedAt: null,
      checkedInAt: null,
      checkedOutAt: null,
      cancelledAt: null,
      cancelReasonCode: null,
      actualNights: null,
      overBooking: false,
      createdAt: now.toISOString(),
      updatedAt: null,
      rowVersion: 1,
    };
    world().bookings.push(booking);
    for (const night of quote.nightlyAmounts) {
      const row: StoredBookingNight = {
        id: world().nextId(),
        tenantId: tenantId,
        bookingId,
        stayDate: night.stayDate,
        roomTypeId: room.id,
        unitAmount: night.amount,
        payerAmount: night.payerAmount,
        memberAmount: night.memberAmount,
        currencyCode: quote.currencyCode,
      };
      world().bookingNights.push(row);
    }
    for (const guest of guests) {
      const row: StoredBookingGuest = {
        id: world().nextId(),
        tenantId: tenantId,
        bookingId,
        personId: guest.personId ?? null,
        displayName: guest.displayName,
        guestType: guest.guestType,
        isMinor: guest.isMinor ?? false,
      };
      world().bookingGuests.push(row);
    }
    moveHeld(booking, 1);
    return booking;
  };

  /**
   * The confirmation itself, without the HTTP around it: the staleness check, the policy the
   * stay is agreed under, the counters and the voucher.
   *
   * It is a function for the same reason `placeHold` is: accepting a waitlist offer (WP-I6-03)
   * confirms the hold the sweep placed, and it has to be *this* confirmation -- the same quote
   * TTL, the same LODGING_TERMS_MISSING refusal, the same voucher. An offer accepted through a
   * second path would be a stay agreed under rules nobody else applies.
   *
   * It returns a Response for a refusal and null for success.
   */
  const confirmHold = (tenantId: string, booking: StoredBooking): Response | null => {
    if (booking.status !== 'HOLD') return transitionInvalid();

    // The frozen prices are what the member will be charged, and a price nobody has
    // looked at for an hour is not one anybody should be committed to.
    const quotedAt = Date.parse(booking.quoteSnapshot.quotedAt);
    if (Date.now() - quotedAt > DEFAULT_QUOTE_TTL_MINUTES * 60_000) {
      return problem(api, 409, 'QUOTE_STALE', 'Fiyat teklifi güncelliğini yitirdi', {
        detail: 'Aramayı yenileyip odayı yeniden seçin; onaylanan tutar gördüğünüz tutar olmalı.',
      });
    }

    // The policy the stay is agreed under. A contract version with none is refused: a
    // stay with no cancellation policy is a stay nobody could cancel fairly.
    const property = world().properties.find((p) => p.id === booking.propertyId);
    const provider = property
      ? world().providers.find(
          (pp) =>
            pp.tenantId === tenantId && pp.tenantOrganizationId === property.providerOrganizationId,
        )
      : undefined;
    const contract = provider
      ? world().contracts.find(
          (c) => c.tenantId === tenantId && c.providerProfileId === provider.id,
        )
      : undefined;
    const version = contract
      ? world().contractVersions.find(
          (v) => v.contractId === contract.id && v.status === 'PUBLISHED',
        )
      : undefined;
    const terms = version
      ? world().lodgingTerms.find((t) => t.contractVersionId === version.id)
      : undefined;
    if (!terms || !version) {
      return problem(
        api,
        409,
        'LODGING_TERMS_MISSING',
        'Sözleşmede konaklama koşulları tanımlı değil',
        { detail: 'İptal ve iade koşulları tanımlanmadan rezervasyon onaylanamaz.' },
      );
    }

    // The gate. In the mock it approves outright, which is what the server's own gate
    // does for a request with no document requirement and no pre-authorisation rule; a
    // request a reviewer has to see leaves the booking in PENDING_APPROVAL there, and a
    // screen must not assume either.
    const now = new Date().toISOString();
    booking.serviceRequestId = world().nextId();
    booking.authorizationId = world().nextId();
    booking.status = 'CONFIRMED';
    booking.confirmedAt = now;
    booking.holdExpiresAt = null;
    booking.policySnapshot = {
      contractVersionId: version.id,
      snapshotAt: now,
      timezone: property?.timezone ?? 'Europe/Istanbul',
      freeCancellationHoursBefore: terms.freeCancellationHoursBefore,
      penaltyKind: terms.penaltyKind,
      penaltyNights: terms.penaltyNights,
      noShowPercent: terms.noShowPercent,
      minNights: terms.minNights,
      maxNights: terms.maxNights,
      childFreeUnderAge: terms.childFreeUnderAge,
    };
    booking.updatedAt = now;
    booking.rowVersion += 1;
    confirmNights(booking);
    // The voucher exists from the moment the stay does; its token is not in this body
    // and is minted only by the voucher command.
    const voucherId = world().nextId();
    world().bookingVouchers.push({
      id: voucherId,
      tenantId: tenantId,
      bookingId: booking.id,
      token: voucherToken(voucherId),
      maskedToken: `KPS-****${voucherToken(voucherId).slice(-4)}`,
      validFrom: `${booking.checkIn}T00:00:00Z`,
      validTo: `${booking.checkOut}T00:00:00Z`,
      status: 'ISSUED',
    });
    booking.voucherId = voucherId;
    return null;
  };

  const handlers: HttpHandler[] = [
    // -------------------------------------------------------------------------------------
    // createHold
    // -------------------------------------------------------------------------------------
    http.post(`${ANY}/api/v1/accommodation/holds`, async ({ request }) => {
      await wait(api);
      const g = guardBooking(request, true);
      if ('error' in g) return g.error;
      sweepExpiredHolds(g.tenantId);
      const rawKey = request.headers.get('Idempotency-Key');
      if (!rawKey) {
        return problem(api, 400, 'IDEMPOTENCY_KEY_REQUIRED', 'Idempotency-Key başlığı gerekli');
      }
      const key = `${g.tenantId}:${rawKey}`;
      const replay = api.replay(key);
      if (replay) {
        return HttpResponse.json(replay.body as Schemas['Booking'], { status: replay.status });
      }

      const body = await readJson<Schemas['CreateHoldRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

      // Whose stay. A member's own binding wins over the body, and a body naming somebody
      // else is PERSON_SCOPE; a desk is bound to nobody and has to name whom it acts for.
      const whose = resolvePerson(api, g.session, g.tenantId, body.personId);
      if ('error' in whose) return whose.error;
      const personId = whose.personId;

      const errors: FieldError[] = [];
      const adults = Number(body.adults);
      const children = Number(body.children ?? 0);
      if (!Number.isInteger(adults) || adults < 1 || adults > 20) {
        errors.push({ field: 'adults', code: 'RANGE', message: '1-20 arasında olmalı' });
      }
      if (!Number.isInteger(children) || children < 0 || children > 20) {
        errors.push({ field: 'children', code: 'RANGE', message: '0-20 arasında olmalı' });
      }
      if (!DATE_ONLY.test(body.checkIn ?? '')) {
        errors.push({ field: 'checkIn', code: 'REQUIRED', message: 'giriş tarihi zorunlu' });
      }
      if (!DATE_ONLY.test(body.checkOut ?? '')) {
        errors.push({ field: 'checkOut', code: 'REQUIRED', message: 'çıkış tarihi zorunlu' });
      }
      if (body.channel && !CHANNELS.has(body.channel)) {
        errors.push({ field: 'channel', code: 'ENUM', message: 'tanınmayan kanal' });
      }
      const guests = body.guests ?? [];
      guests.forEach((guest, index) => {
        if (!GUEST_TYPES.has(guest.guestType)) {
          errors.push({
            field: `guests[${index}].guestType`,
            code: 'ENUM',
            message: 'tanınmayan misafir tipi',
          });
        }
        if (!guest.displayName || guest.displayName.length > 200) {
          errors.push({
            field: `guests[${index}].displayName`,
            code: 'RANGE',
            message: '1-200 karakter olmalı',
          });
        }
      });
      if (guests.length > 0 && guests.length !== adults + children) {
        errors.push({
          field: 'guests',
          code: 'RANGE',
          message: 'misafir sayısı yetişkin ve çocuk toplamına eşit olmalı',
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const nights = nightsOf(body.checkIn, body.checkOut);
      if (nights === 0) {
        return validationFailed(api, [
          { field: 'checkOut', code: 'RANGE', message: 'giriş tarihinden sonra olmalı' },
        ]);
      }
      if (nights > DEFAULT_MAX_NIGHTS) {
        return validationFailed(api, [
          {
            field: 'checkOut',
            code: 'RANGE',
            message: `en fazla ${DEFAULT_MAX_NIGHTS} gece rezerve edilebilir`,
          },
        ]);
      }

      const held = placeHold(
        g.session,
        g.tenantId,
        personId,
        body.roomTypeId,
        body.checkIn,
        body.checkOut,
        adults,
        children,
        nights,
        guests,
        body.programId ?? null,
        body.channel,
      );
      if (held instanceof Response) return held;

      const out = toBooking(held);
      api.rememberIdempotent(key, 201, out, null);
      return HttpResponse.json(out, { status: 201 });
    }),

    // -------------------------------------------------------------------------------------
    // listBookings
    // -------------------------------------------------------------------------------------
    http.get(`${ANY}/api/v1/accommodation/bookings`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      sweepExpiredHolds(g.tenantId);
      const url = new URL(request.url);
      const personId = url.searchParams.get('personId');
      const propertyId = url.searchParams.get('propertyId');
      const status = url.searchParams.get('status');
      const from = url.searchParams.get('checkInFrom');
      const to = url.searchParams.get('checkInTo');
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return validationFailed(api, [
          { field: 'limit', code: 'RANGE', message: '1 ile 200 arasında olmalı' },
        ]);
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');

      // A member's own binding *replaces* the person filter rather than narrowing it: a
      // member who asks for somebody else's bookings gets their own, which is what the
      // server does and what stops a filter being read as an escape from the boundary.
      const bound = personScope(api, g.session, g.tenantId);
      const wanted = bound ?? personId;
      const rows = world()
        .bookings.filter(
          (b) =>
            b.tenantId === g.tenantId &&
            visible(g.session, g.tenantId, b) &&
            (!wanted || b.personId === wanted) &&
            (!propertyId || b.propertyId === propertyId) &&
            (!status || b.status === status) &&
            (!from || b.checkIn >= from) &&
            (!to || b.checkIn <= to),
        )
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      const page = rows.slice(offset, offset + limit);
      return HttpResponse.json({
        items: page.map(toBooking),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      } satisfies Schemas['BookingList']);
    }),

    // -------------------------------------------------------------------------------------
    // getBooking
    // -------------------------------------------------------------------------------------
    http.get(`${ANY}/api/v1/accommodation/bookings/:bookingId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      sweepExpiredHolds(g.tenantId);
      const booking = findBooking(g.session, g.tenantId, pathParam(params, 'bookingId'));
      if (!booking) return bookingNotFound();
      return HttpResponse.json(toBooking(booking));
    }),

    // -------------------------------------------------------------------------------------
    // confirmBooking
    // -------------------------------------------------------------------------------------
    http.post(
      `${ANY}/api/v1/accommodation/bookings/:bookingId/confirm`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardBooking(request, true);
        if ('error' in g) return g.error;
        sweepExpiredHolds(g.tenantId);
        const rawKey = request.headers.get('Idempotency-Key');
        if (!rawKey) {
          return problem(api, 400, 'IDEMPOTENCY_KEY_REQUIRED', 'Idempotency-Key başlığı gerekli');
        }
        const key = `${g.tenantId}:${rawKey}`;
        const replay = api.replay(key);
        if (replay) {
          return HttpResponse.json(replay.body as Schemas['Booking'], { status: replay.status });
        }

        const booking = findBooking(g.session, g.tenantId, pathParam(params, 'bookingId'));
        if (!booking) return bookingNotFound();
        const refusal = confirmHold(g.tenantId, booking);
        if (refusal) return refusal;

        const out = toBooking(booking);
        api.rememberIdempotent(key, 200, out, null);
        return HttpResponse.json(out);
      },
    ),

    // -------------------------------------------------------------------------------------
    // releaseHold
    // -------------------------------------------------------------------------------------
    http.post(
      `${ANY}/api/v1/accommodation/bookings/:bookingId/release`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardBooking(request, true);
        if ('error' in g) return g.error;
        sweepExpiredHolds(g.tenantId);
        const booking = findBooking(g.session, g.tenantId, pathParam(params, 'bookingId'));
        if (!booking) return bookingNotFound();
        if (!HELD_STATUSES.has(booking.status)) return transitionInvalid();

        moveHeld(booking, -1);
        const now = new Date().toISOString();
        booking.status = 'CANCELLED';
        booking.cancelledAt = now;
        // Nothing was agreed, so nothing is charged: the reason says which of the two
        // cancellations this is, and a fee may only follow the other one.
        booking.cancelReasonCode = 'HOLD_RELEASED';
        booking.holdExpiresAt = null;
        booking.updatedAt = now;
        booking.rowVersion += 1;
        return HttpResponse.json(toBooking(booking));
      },
    ),

    // -------------------------------------------------------------------------------------
    // reissueBookingVoucher
    // -------------------------------------------------------------------------------------
    //
    // No Idempotency-Key, and that is not an oversight: the replay store keeps response
    // bodies and this response is the one place a usable token exists. A replay mints a new
    // token and retires the previous one, so nobody ends up holding two codes that work.
    http.post(
      `${ANY}/api/v1/accommodation/bookings/:bookingId/voucher`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardBooking(request, true);
        if ('error' in g) return g.error;
        sweepExpiredHolds(g.tenantId);
        const booking = findBooking(g.session, g.tenantId, pathParam(params, 'bookingId'));
        if (!booking) return bookingNotFound();
        if (booking.status !== 'CONFIRMED' && booking.status !== 'CHECKED_IN') {
          return transitionInvalid();
        }
        if (!booking.authorizationId) {
          return problem(
            api,
            409,
            'VOUCHER_NOT_AVAILABLE',
            'Rezervasyon belgesi henüz oluşturulamaz',
            { detail: 'Rezervasyon onaylanmadan belge düzenlenemez.' },
          );
        }
        for (const voucher of world().bookingVouchers) {
          if (voucher.bookingId === booking.id && voucher.status === 'ISSUED') {
            voucher.status = 'REVOKED';
          }
        }
        const voucherId = world().nextId();
        const token = voucherToken(voucherId);
        world().bookingVouchers.push({
          id: voucherId,
          tenantId: g.tenantId,
          bookingId: booking.id,
          token,
          maskedToken: `KPS-****${token.slice(-4)}`,
          validFrom: `${booking.checkIn}T00:00:00Z`,
          validTo: `${booking.checkOut}T00:00:00Z`,
          status: 'ISSUED',
        });
        booking.voucherId = voucherId;
        booking.updatedAt = new Date().toISOString();
        booking.rowVersion += 1;
        return HttpResponse.json(
          {
            id: voucherId,
            token,
            maskedToken: `KPS-****${token.slice(-4)}`,
            validFrom: `${booking.checkIn}T00:00:00Z`,
            validTo: `${booking.checkOut}T00:00:00Z`,
          } satisfies Schemas['BookingVoucher'],
          { status: 201 },
        );
      },
    ),
  ];

  return {
    handlers,
    after: {
      sweepExpiredHolds,
      toBooking,
      moveHeld,
      moveConfirmed,
      // The sweep has nobody to hand a refusal to: a room that is not free is simply not
      // this entry's turn, so a Response becomes null and the caller tries the next room.
      placeHold: (tenantId, personId, roomTypeId, checkIn, checkOut, adults, children) => {
        const nights = nightsOf(checkIn, checkOut);
        if (nights === 0) return null;
        const session = api.session;
        if (!session) return null;
        const held = placeHold(
          session,
          tenantId,
          personId,
          roomTypeId,
          checkIn,
          checkOut,
          adults,
          children,
          nights,
          [],
          null,
          'BACKOFFICE',
        );
        return held instanceof Response ? null : held;
      },
      confirmHold,
    },
  };
}
