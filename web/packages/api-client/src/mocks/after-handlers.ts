/**
 * MSW handlers for what happens after the promise (WP-I6-03): the cancellation, the check-in
 * and check-out, the no-show and the waiting list.
 *
 * The mock is a test double of the Go server and M6 treats a divergence in either direction
 * as a bug. Six things here are transcriptions rather than re-implementations:
 *
 *   - **a cancellation is judged by the booking's own frozen policy**, never by the contract
 *     as it stands today. `policySnapshot` was written onto the booking at confirmation and
 *     the arithmetic below reads that document and the booking's own night amounts; the
 *     `cancellation` row keeps a copy, so it proves what it was judged by;
 *   - **the free window is counted on the property's clock**, back from the start of the
 *     arrival day, because that is the only reading of "48 hours before" that does not depend
 *     on who is looking;
 *   - **`payerFee + memberFee === feeAmount`, exactly**: the two halves are summed from the
 *     same night rows rather than one being derived from the other, and nothing here goes
 *     through a float;
 *   - **a token that is not this booking's is 404**, exactly as an unknown one is. A refusal
 *     that could tell a real code for another stay apart from one that does not exist would
 *     be an oracle a stolen list could be tested against;
 *   - **a no-show costs the member nothing until a second person confirms it**: the report
 *     leaves the booking CONFIRMED and the room taken, the reporter may not review their own
 *     claim, and a rejection leaves the stay exactly as it was;
 *   - **the queue is walked in priority order, then FIFO**, and an offer nobody accepts
 *     returns the entry to WAITING behind everybody who was already queued -- which is what
 *     moving its `createdAt` to now does.
 *
 * The offer job is advanceable the way the expiry sweep is: it runs on demand before anything
 * is read or written, so a test frees a room and the very next call finds the offer made.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  type MockWorld,
  type StoredBooking,
  type StoredCancellation,
  type StoredNoShow,
  type StoredWaitlistEntry,
} from './data';
import type { MockApi, MockSession } from './handlers';
import {
  ANY,
  guardTenant,
  organizationScope,
  parseLimit,
  pathParam,
  personScope,
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
const PERMISSION_BOOK = 'accommodation.booking.create';
const PERMISSION_BOOKING_MANAGE = 'accommodation.booking.manage';
const PERMISSION_WAITLIST_MANAGE = 'accommodation.waitlist.manage';

const DAY_MS = 86_400_000;
const HOUR_MS = 3_600_000;
const DATE_ONLY = /^\d{4}-\d{2}-\d{2}$/;

/** The tenant's own check-in window, for a tenant with no rows of its own. */
const CHECK_IN_EARLY_HOURS = 6;
const CHECK_IN_LATE_HOURS = 24;

const HELD_STATUSES = new Set<string>(['HOLD', 'PENDING_APPROVAL']);
const NO_SHOW_DECISIONS = new Set<string>(['CONFIRMED', 'DISPUTED', 'REJECTED']);
const WAITLIST_STATUSES = new Set<string>([
  'WAITING',
  'OFFERED',
  'ACCEPTED',
  'EXPIRED',
  'CANCELLED',
]);

/**
 * Exact decimal arithmetic in micro-units, the same scale the numeric(20,6) columns carry.
 * Every amount in this file is a string on the way in and a string on the way out; the only
 * thing that ever holds one is a bigint.
 */
const SCALE = 1_000_000n;

function toMicros(value: string): bigint {
  const [whole, fraction = ''] = value.split('.');
  const padded = (fraction + '000000').slice(0, 6);
  const sign = whole?.startsWith('-') ? -1n : 1n;
  const abs = BigInt((whole ?? '0').replace('-', '') + padded);
  return sign * abs;
}

function fromMicros(value: bigint): string {
  const negative = value < 0n;
  const abs = negative ? -value : value;
  const whole = abs / SCALE;
  const fraction = (abs % SCALE).toString().padStart(6, '0');
  return (negative ? '-' : '') + whole.toString() + '.' + fraction;
}

/** p per cent of an amount, rounded half away from zero, without a float anywhere. */
function percentOfMicrosExact(amount: bigint, percent: string): bigint {
  const rate = toMicros(percent);
  const divisor = 100n * SCALE;
  const product = amount * rate;
  const quotient = product / divisor;
  const remainder = product % divisor;
  const abs = remainder < 0n ? -remainder : remainder;
  if (abs * 2n >= divisor) return quotient + (product < 0n ? -1n : 1n);
  return quotient;
}

function nightsOf(checkIn: string, checkOut: string): number {
  const from = Date.parse(`${checkIn}T00:00:00Z`);
  const to = Date.parse(`${checkOut}T00:00:00Z`);
  if (Number.isNaN(from) || Number.isNaN(to) || to <= from) return 0;
  return Math.round((to - from) / DAY_MS);
}

/**
 * The instant the arrival day starts on the property's own clock.
 *
 * The offset is read from the zone rather than assumed, because that is the single reason the
 * snapshot carries a timezone at all: a member cancelling a Berlin hotel late in the evening
 * is inside a 48-hour window there and outside it in Istanbul, and only one of those is the
 * answer they agreed to.
 */
function arrivalMoment(checkIn: string, timezone: string): number {
  const midnightUtc = Date.parse(`${checkIn}T00:00:00Z`);
  const formatter = new Intl.DateTimeFormat('en-US', {
    timeZone: timezone,
    timeZoneName: 'longOffset',
  });
  const offsetPart = formatter
    .formatToParts(new Date(midnightUtc))
    .find((p) => p.type === 'timeZoneName')?.value;
  const match = /GMT([+-])(\d{2}):(\d{2})/.exec(offsetPart ?? '');
  if (!match) return midnightUtc;
  const sign = match[1] === '-' ? -1 : 1;
  const minutes = Number(match[2]) * 60 + Number(match[3]);
  return midnightUtc - sign * minutes * 60_000;
}

/** The tools the booking module already owns, handed in rather than copied. */
export interface AfterTools {
  /** Runs the hold expiry sweep, which the offer job leans on to free an unaccepted room. */
  sweepExpiredHolds(tenantId: string): void;
  /** Renders a booking exactly as `getBooking` does, countdown and all. */
  toBooking(booking: StoredBooking): Schemas['Booking'];
  /** Moves `held` by a signed delta over every night of the stay. */
  moveHeld(booking: StoredBooking, delta: number): void;
  /** Moves `confirmed` by a signed delta over a range of the stay's nights. */
  moveConfirmed(booking: StoredBooking, delta: number, from?: number): void;
  /** Creates a hold on somebody's behalf, exactly as `createHold` does. */
  placeHold(
    tenantId: string,
    personId: string,
    roomTypeId: string,
    checkIn: string,
    checkOut: string,
    adults: number,
    children: number,
  ): StoredBooking | null;
  /** Confirms a hold, exactly as `confirmBooking` does. */
  confirmHold(tenantId: string, booking: StoredBooking): Response | null;
}

export function afterHandlers(api: MockApi, tools: AfterTools): HttpHandler[] {
  const world = (): MockWorld => api.world;

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

  const guardWaitlist = (request: Request, mutation: boolean) => {
    const first = guardTenant(api, request, PERMISSION_BOOK, mutation);
    if (!('error' in first)) return first;
    return guardTenant(api, request, PERMISSION_WAITLIST_MANAGE, mutation);
  };

  /** The member boundary and the provider boundary, both applied before anything is read. */
  const findBooking = (
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredBooking | undefined => {
    const row = world().bookings.find((b) => b.id === id && b.tenantId === tenantId);
    if (!row) return undefined;
    const bound = personScope(api, session, tenantId);
    if (bound !== null && row.personId !== bound) return undefined;
    const scope = organizationScope(api, session, tenantId);
    if (scope === null) return row;
    const property = world().properties.find((p) => p.id === row.propertyId);
    return property && withinScope(scope, property.providerOrganizationId) ? row : undefined;
  };

  const timezoneOf = (booking: StoredBooking): string =>
    world().properties.find((p) => p.id === booking.propertyId)?.timezone ?? 'Europe/Istanbul';

  const nightRowsOf = (booking: StoredBooking) =>
    world()
      .bookingNights.filter((n) => n.bookingId === booking.id)
      .sort((a, b) => a.stayDate.localeCompare(b.stayDate));

  /**
   * What a cancellation now would cost and give back, from the booking's own frozen policy.
   *
   * The money and the nights are two different questions. The money is what the hotel is owed:
   * for a NIGHTS policy the first `penaltyNights` nights of the stay at the amounts the
   * booking's own rows carry, so the two shares sum to the fee exactly because they come from
   * the same rows; for a PERCENT policy a share of the member's own amount, which is entirely
   * hers. The nights are what the plan spends, capped at what it actually covered.
   */
  const quoteCancellation = (booking: StoredBooking): Schemas['CancellationQuote'] | null => {
    const policy = booking.policySnapshot;
    const covered = Math.max(0, booking.quoteSnapshot.coveredNights);
    const currency = booking.quoteSnapshot.currencyCode || 'TRY';
    if (booking.status === 'PENDING_APPROVAL') {
      // Nothing has been agreed, so nothing is owed and there is no policy to read.
      return {
        free: true,
        penaltyNights: 0,
        releasedNights: covered,
        feeAmount: '0.000000',
        payerFee: '0.000000',
        memberFee: '0.000000',
        currencyCode: currency,
        freeUntil: null,
      };
    }
    if (!policy) return null;

    const freeUntil =
      arrivalMoment(booking.checkIn, policy.timezone) -
      policy.freeCancellationHoursBefore * HOUR_MS;
    const now = Date.now();
    const base: Schemas['CancellationQuote'] = {
      free: false,
      penaltyNights: 0,
      releasedNights: covered,
      feeAmount: '0.000000',
      payerFee: '0.000000',
      memberFee: '0.000000',
      currencyCode: currency,
      freeUntil: new Date(freeUntil).toISOString(),
    };
    if (now <= freeUntil) return { ...base, free: true };

    if (policy.penaltyKind === 'NIGHTS') {
      const charged = Math.min(policy.penaltyNights ?? 0, booking.nights);
      const nights = nightRowsOf(booking).slice(0, Math.max(0, charged));
      let fee = 0n;
      let payer = 0n;
      let member = 0n;
      for (const night of nights) {
        fee += toMicros(night.unitAmount);
        payer += toMicros(night.payerAmount);
        member += toMicros(night.memberAmount);
      }
      const spent = Math.min(charged, covered);
      return {
        ...base,
        penaltyNights: charged,
        releasedNights: covered - spent,
        feeAmount: fromMicros(fee),
        payerFee: fromMicros(payer),
        memberFee: fromMicros(member),
      };
    }
    // A percentage of the member's own share is the member's, whole: splitting it would be
    // inventing a payer contribution the contract does not mention.
    const fee = percentOfMicrosExact(
      toMicros(booking.quoteSnapshot.memberAmount),
      policy.penaltyPercent ?? '0',
    );
    return { ...base, feeAmount: fromMicros(fee), memberFee: fromMicros(fee) };
  };

  /** The no-show fee, and how many whole nights a confirmation would spend. */
  const quoteNoShow = (
    booking: StoredBooking,
  ): { fee: string; member: string; nights: number } | null => {
    const policy = booking.policySnapshot;
    if (!policy) return null;
    const fee = percentOfMicrosExact(
      toMicros(booking.quoteSnapshot.memberAmount),
      policy.noShowPercent,
    );
    // The policy is a percentage and the entitlement is whole nights, so the rate is applied
    // to the covered nights and rounded **up**: a rate of fifty per cent that rounded down
    // would cost the payer nothing on a one-night stay, which is not what it means.
    const covered = Math.max(0, booking.quoteSnapshot.coveredNights);
    const exact = percentOfMicrosExact(BigInt(covered) * SCALE, policy.noShowPercent);
    const nights = Math.min(covered, Number((exact + SCALE - 1n) / SCALE));
    return { fee: fromMicros(fee), member: fromMicros(fee), nights };
  };

  const noShowOf = (booking: StoredBooking): StoredNoShow | undefined =>
    world().noShows.find((n) => n.bookingId === booking.id);

  const toNoShow = (row: StoredNoShow): Schemas['NoShowReport'] => {
    const { tenantId: _tenantId, ...rest } = row;
    return rest;
  };

  const toCancellation = (row: StoredCancellation): Schemas['Cancellation'] => {
    const { tenantId: _tenantId, ...rest } = row;
    return rest;
  };

  const bookingOfEntry = (entry: StoredWaitlistEntry): StoredBooking | undefined =>
    entry.offeredBookingId
      ? world().bookings.find((b) => b.id === entry.offeredBookingId)
      : undefined;

  const toWaitlistEntry = (entry: StoredWaitlistEntry): Schemas['WaitlistEntry'] => {
    const { tenantId: _tenantId, ...rest } = entry;
    const offer = bookingOfEntry(entry);
    return { ...rest, offer: offer ? tools.toBooking(offer) : null, updatedAt: null };
  };

  /**
   * The offer sweep, run before anything is read or written.
   *
   * On the server this is a scheduler job that runs every five minutes; here it runs on
   * demand, which is what makes it advanceable in a test. The order inside it is the server's:
   * offers nobody took go back to the queue first, so a room freed by an expiring offer is
   * available to the entry behind it in the very same pass.
   */
  const sweepWaitlist = (tenantId: string): void => {
    tools.sweepExpiredHolds(tenantId);
    const now = Date.now();

    for (const entry of world().waitlistEntries) {
      if (entry.tenantId !== tenantId || entry.status !== 'OFFERED') continue;
      const offered = bookingOfEntry(entry);
      const expired =
        (entry.offerExpiresAt !== null && Date.parse(entry.offerExpiresAt) <= now) ||
        (offered !== undefined && !HELD_STATUSES.has(offered.status));
      if (!expired) continue;
      if (offered && HELD_STATUSES.has(offered.status)) {
        tools.moveHeld(offered, -1);
        offered.status = 'EXPIRED';
        offered.holdExpiresAt = null;
        offered.rowVersion += 1;
      }
      entry.status = 'WAITING';
      entry.offeredBookingId = null;
      entry.offerExpiresAt = null;
      // The queue is ordered by createdAt, so moving it to now *is* going to the back: a
      // member who let a room go waits again rather than being handed the next one first.
      entry.createdAt = new Date(now).toISOString();
      entry.rowVersion += 1;
    }

    const queue = world()
      .waitlistEntries.filter((e) => e.tenantId === tenantId && e.status === 'WAITING')
      .sort((a, b) => b.priority - a.priority || a.createdAt.localeCompare(b.createdAt));
    for (const entry of queue) {
      const roomTypes = entry.roomTypeId
        ? [entry.roomTypeId]
        : world()
            .roomTypes.filter((r) => r.propertyId === entry.propertyId && r.status === 'ACTIVE')
            .map((r) => r.id);
      for (const roomTypeId of roomTypes) {
        const held = tools.placeHold(
          tenantId,
          entry.personId,
          roomTypeId,
          entry.checkIn,
          entry.checkOut,
          entry.adults,
          entry.children,
        );
        if (!held) continue;
        entry.status = 'OFFERED';
        entry.offeredBookingId = held.id;
        // The hold's own deadline, not a second one: an offer that outlived its hold would
        // be an offer whose room somebody else had already been sold.
        entry.offerExpiresAt = held.holdExpiresAt;
        entry.rowVersion += 1;
        break;
      }
    }
  };

  return [
    // -------------------------------------------------------------------------------------
    // previewCancellation
    // -------------------------------------------------------------------------------------
    http.post(
      `${ANY}/api/v1/accommodation/bookings/:bookingId/cancellation-preview`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardBooking(request, true);
        if ('error' in g) return g.error;
        tools.sweepExpiredHolds(g.tenantId);
        const booking = findBooking(g.session, g.tenantId, pathParam(params, 'bookingId'));
        if (!booking) return bookingNotFound();
        const refusal = cancellable(booking);
        if (refusal) return refusal;
        const quote = quoteCancellation(booking);
        if (!quote) return policyMissing();
        return HttpResponse.json({
          booking: tools.toBooking(booking),
          quote,
        } satisfies Schemas['CancellationPreview']);
      },
    ),

    // -------------------------------------------------------------------------------------
    // cancelBooking
    // -------------------------------------------------------------------------------------
    http.post(
      `${ANY}/api/v1/accommodation/bookings/:bookingId/cancel`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardBooking(request, true);
        if ('error' in g) return g.error;
        tools.sweepExpiredHolds(g.tenantId);
        const rawKey = request.headers.get('Idempotency-Key');
        if (!rawKey) {
          return problem(api, 400, 'IDEMPOTENCY_KEY_REQUIRED', 'Idempotency-Key başlığı gerekli');
        }
        const key = `${g.tenantId}:${rawKey}`;
        const replay = api.replay(key);
        if (replay) {
          return HttpResponse.json(replay.body as Schemas['CancellationResult'], {
            status: replay.status,
          });
        }

        const booking = findBooking(g.session, g.tenantId, pathParam(params, 'bookingId'));
        if (!booking) return bookingNotFound();
        const refusal = cancellable(booking);
        if (refusal) return refusal;
        const body = await readJson<Schemas['CancelBookingRequest']>(request).catch(() => null);
        const reasonCode = body?.reasonCode ?? 'MEMBER_CANCELLED';
        if (!/^[A-Z][A-Z0-9_]{1,63}$/.test(reasonCode)) {
          return validationFailed(api, [
            {
              field: 'reasonCode',
              code: 'FORMAT',
              message: 'büyük harf, rakam ve alt çizgiden oluşan bir kod olmalı',
            },
          ]);
        }
        const quote = quoteCancellation(booking);
        if (!quote) return policyMissing();

        // The room, on every night. A stay that was agreed is counted in `confirmed`; one
        // still waiting on a reviewer is counted in `held`.
        if (HELD_STATUSES.has(booking.status)) tools.moveHeld(booking, -1);
        else tools.moveConfirmed(booking, -1);

        const now = new Date().toISOString();
        booking.status = 'CANCELLED';
        booking.cancelledAt = now;
        booking.cancelReasonCode = reasonCode;
        booking.holdExpiresAt = null;
        booking.updatedAt = now;
        booking.rowVersion += 1;
        // The code that would have opened the room stops working with the stay.
        for (const voucher of world().bookingVouchers) {
          if (voucher.bookingId === booking.id && voucher.status === 'ISSUED') {
            voucher.status = 'REVOKED';
          }
        }

        const row: StoredCancellation = {
          id: world().nextId(),
          tenantId: g.tenantId,
          bookingId: booking.id,
          cancelledAt: now,
          cancelledBy: g.session.account.actorId,
          reasonCode,
          policySnapshot: booking.policySnapshot,
          free: quote.free,
          penaltyNights: quote.penaltyNights,
          releasedNights: quote.releasedNights,
          feeAmount: quote.feeAmount,
          payerFee: quote.payerFee,
          memberFee: quote.memberFee,
          currencyCode: quote.currencyCode,
        };
        world().cancellations.push(row);

        const out: Schemas['CancellationResult'] = {
          booking: tools.toBooking(booking),
          quote,
          cancellation: toCancellation(row),
        };
        api.rememberIdempotent(key, 200, out, null);
        return HttpResponse.json(out);
      },
    ),

    // -------------------------------------------------------------------------------------
    // checkInBooking
    // -------------------------------------------------------------------------------------
    //
    // No Idempotency-Key, and that is not an oversight: this request body carries a usable
    // voucher token and the replay store keeps bodies. A replayed check-in is refused by the
    // voucher's own status instead, which is a better answer than a stored one.
    http.post(
      `${ANY}/api/v1/accommodation/bookings/:bookingId/check-in`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_BOOKING_MANAGE, true);
        if ('error' in g) return g.error;
        tools.sweepExpiredHolds(g.tenantId);
        const booking = findBooking(g.session, g.tenantId, pathParam(params, 'bookingId'));
        if (!booking) return bookingNotFound();
        const body = await readJson<Schemas['CheckInBookingRequest']>(request);
        const token = (body?.token ?? '').trim();
        if (token === '') {
          return problem(api, 422, 'VOUCHER_REQUIRED', 'Giriş için kupon kodu gerekli', {
            detail: 'Misafirin gösterdiği kodu girin.',
          });
        }
        if (booking.status !== 'CONFIRMED') return transitionInvalid();

        const arrival = arrivalMoment(booking.checkIn, timezoneOf(booking));
        const opens = arrival - CHECK_IN_EARLY_HOURS * HOUR_MS;
        const closes = arrival + CHECK_IN_LATE_HOURS * HOUR_MS;
        const at = body?.at ? Date.parse(body.at) : Date.now();
        if (at < opens || at > closes) {
          return problem(api, 409, 'BOOKING_CHECK_IN_WINDOW', 'Giriş saati aralığının dışında', {
            detail:
              'Bu rezervasyon için giriş yalnızca tesisin kendi saatiyle belirlenen ' +
              'aralıkta kaydedilebilir.',
            extensions: {
              opensAt: new Date(opens).toISOString(),
              closesAt: new Date(closes).toISOString(),
              timezone: timezoneOf(booking),
            },
          });
        }

        const voucher = world().bookingVouchers.find(
          (v) => v.tenantId === g.tenantId && v.token === token && v.bookingId === booking.id,
        );
        // A code that does not exist and a real code for somebody else's stay are the same
        // answer, deliberately.
        if (!voucher) {
          return problem(api, 404, 'VOUCHER_NOT_FOUND', 'Bu rezervasyona ait böyle bir kupon yok');
        }
        if (voucher.status === 'REDEEMED') {
          return problem(api, 409, 'VOUCHER_ALREADY_REDEEMED', 'Kupon zaten kullanılmış');
        }
        if (voucher.status === 'REVOKED') {
          return problem(api, 409, 'VOUCHER_REVOKED', 'Kupon iptal edilmiş', {
            detail: 'Rezervasyon sahibine yeni bir kupon düzenlenebilir.',
          });
        }
        if (voucher.status === 'EXPIRED') {
          return problem(api, 409, 'VOUCHER_EXPIRED', 'Kupon geçerlilik süresi dışında');
        }

        voucher.status = 'REDEEMED';
        const now = new Date(at).toISOString();
        booking.status = 'CHECKED_IN';
        booking.checkedInAt = now;
        booking.updatedAt = now;
        booking.rowVersion += 1;
        return HttpResponse.json(tools.toBooking(booking));
      },
    ),

    // -------------------------------------------------------------------------------------
    // checkOutBooking
    // -------------------------------------------------------------------------------------
    http.post(
      `${ANY}/api/v1/accommodation/bookings/:bookingId/check-out`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_BOOKING_MANAGE, true);
        if ('error' in g) return g.error;
        tools.sweepExpiredHolds(g.tenantId);
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
        if (booking.status !== 'CHECKED_IN') return transitionInvalid();

        const body = await readJson<Schemas['CheckOutBookingRequest']>(request).catch(() => null);
        const at = body?.at ? Date.parse(body.at) : Date.now();
        // The number of civil days between the booked arrival and the day the guest left, on
        // the property's own clock, and never fewer than one: a room used for an afternoon is
        // a night the hotel cannot sell to anybody else.
        const arrival = arrivalMoment(booking.checkIn, timezoneOf(booking));
        const departureDay = Math.floor((at - arrival) / DAY_MS);
        const actual = Math.max(1, departureDay);
        const covered = Math.max(0, booking.quoteSnapshot.coveredNights);
        const over = covered > 0 && actual > covered;

        // The rooms of the nights nobody slept in go back; the ones that were used stay taken.
        if (actual < booking.nights) tools.moveConfirmed(booking, -1, actual);

        const now = new Date(at).toISOString();
        booking.status = 'COMPLETED';
        booking.checkedOutAt = now;
        booking.actualNights = actual;
        booking.overBooking = over;
        booking.updatedAt = now;
        booking.rowVersion += 1;
        const out = tools.toBooking(booking);
        api.rememberIdempotent(key, 200, out, null);
        return HttpResponse.json(out);
      },
    ),

    // -------------------------------------------------------------------------------------
    // getNoShow / reportNoShow
    // -------------------------------------------------------------------------------------
    http.get(
      `${ANY}/api/v1/accommodation/bookings/:bookingId/no-show`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_READ, false);
        if ('error' in g) return g.error;
        const booking = findBooking(g.session, g.tenantId, pathParam(params, 'bookingId'));
        if (!booking) return bookingNotFound();
        const report = noShowOf(booking);
        if (!report) {
          return problem(api, 404, 'NO_SHOW_NOT_FOUND', 'Gelmedi bildirimi bulunamadı');
        }
        return HttpResponse.json({
          report: toNoShow(report),
          booking: tools.toBooking(booking),
        } satisfies Schemas['NoShowResult']);
      },
    ),

    http.post(
      `${ANY}/api/v1/accommodation/bookings/:bookingId/no-show`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_BOOKING_MANAGE, true);
        if ('error' in g) return g.error;
        tools.sweepExpiredHolds(g.tenantId);
        const booking = findBooking(g.session, g.tenantId, pathParam(params, 'bookingId'));
        if (!booking) return bookingNotFound();
        if (booking.status !== 'CONFIRMED') return transitionInvalid();
        if (noShowOf(booking)) {
          return problem(
            api,
            409,
            'NO_SHOW_ALREADY_REPORTED',
            'Bu rezervasyon için zaten bildirim var',
            {
              detail:
                'Reddedilen bir bildirim silinmez; her rezervasyon için tek bildirim yapılır.',
            },
          );
        }
        const body = await readJson<Schemas['ReportNoShowRequest']>(request).catch(() => null);
        const at = body?.at ? Date.parse(body.at) : Date.now();
        const closes =
          arrivalMoment(booking.checkIn, timezoneOf(booking)) + CHECK_IN_LATE_HOURS * HOUR_MS;
        if (at < closes) {
          return problem(api, 409, 'NO_SHOW_TOO_EARLY', 'Giriş saati aralığı henüz kapanmadı', {
            detail:
              'Geç kalan misafir ile gelmeyen misafir aynı şey değildir; aralık kapandıktan ' +
              'sonra bildirin.',
          });
        }
        // The evidence: a clean document linked to this booking. A claim that costs a member
        // money and rests on nothing is a claim nobody can review.
        const evidence = world().documentLinks.find(
          (l) =>
            l.tenantId === g.tenantId &&
            l.aggregateType === 'BOOKING' &&
            l.aggregateId === booking.id &&
            (!body?.evidenceDocumentId || l.documentId === body.evidenceDocumentId) &&
            world().documents.some((d) => d.id === l.documentId && d.scanStatus === 'CLEAN'),
        );
        if (!evidence) {
          return problem(
            api,
            409,
            'NO_SHOW_EVIDENCE_REQUIRED',
            'Gelmedi bildirimi için belge gerekli',
            {
              detail:
                'Rezervasyona bağlı, taraması temiz en az bir belge olmadan bildirim yapılamaz.',
            },
          );
        }
        const quote = quoteNoShow(booking);
        if (!quote) return policyMissing();

        const row: StoredNoShow = {
          id: world().nextId(),
          tenantId: g.tenantId,
          bookingId: booking.id,
          reportedByActorId: g.session.account.actorId,
          reportedAt: new Date(at).toISOString(),
          evidenceDocumentId: evidence.documentId,
          assessedFeeAmount: quote.fee,
          payerAmount: '0.000000',
          memberAmount: quote.member,
          currencyCode: booking.quoteSnapshot.currencyCode || 'TRY',
          status: 'REPORTED',
          reviewedBy: null,
          reviewedAt: null,
          reviewComment: null,
          consumedNights: 0,
          rowVersion: 1,
        };
        world().noShows.push(row);
        // The booking is untouched. That is the whole rule.
        return HttpResponse.json(
          {
            report: toNoShow(row),
            booking: tools.toBooking(booking),
          } satisfies Schemas['NoShowResult'],
          { status: 201 },
        );
      },
    ),

    // -------------------------------------------------------------------------------------
    // reviewNoShow
    // -------------------------------------------------------------------------------------
    http.post(
      `${ANY}/api/v1/accommodation/bookings/:bookingId/no-show/review`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_BOOKING_MANAGE, true);
        if ('error' in g) return g.error;
        const booking = findBooking(g.session, g.tenantId, pathParam(params, 'bookingId'));
        if (!booking) return bookingNotFound();
        const report = noShowOf(booking);
        if (!report) {
          return problem(api, 404, 'NO_SHOW_NOT_FOUND', 'Gelmedi bildirimi bulunamadı');
        }
        const body = await readJson<Schemas['ReviewNoShowRequest']>(request);
        if (!body || !NO_SHOW_DECISIONS.has(body.status)) {
          return validationFailed(api, [
            { field: 'status', code: 'ENUM', message: 'tanınmayan karar' },
          ]);
        }
        if (report.status !== 'REPORTED') {
          return problem(api, 409, 'NO_SHOW_DECIDED', 'Bu bildirim zaten karara bağlandı', {
            detail: 'Güncel durumu alıp yeniden deneyin.',
          });
        }
        // The maker-checker rule: the person who said nobody came may never be the person who
        // decides it costs the member anything.
        if (report.reportedByActorId === g.session.account.actorId) {
          return problem(api, 403, 'NO_SHOW_SAME_ACTOR', 'Bildirimi yapan kişi onu onaylayamaz', {
            detail: 'Gelmedi bildirimini değerlendiren, bildiren kullanıcıdan farklı olmalı.',
          });
        }

        let consumed = 0;
        if (body.status === 'CONFIRMED') {
          if (booking.status !== 'CONFIRMED') return transitionInvalid();
          const quote = quoteNoShow(booking);
          if (!quote) return policyMissing();
          consumed = quote.nights;
          // The room goes back: the guest did not come, so the hotel may sell those nights.
          tools.moveConfirmed(booking, -1);
          booking.status = 'NO_SHOW';
          booking.cancelReasonCode = 'NO_SHOW';
          booking.rowVersion += 1;
          for (const voucher of world().bookingVouchers) {
            if (voucher.bookingId === booking.id && voucher.status === 'ISSUED') {
              voucher.status = 'REVOKED';
            }
          }
        }
        const now = new Date().toISOString();
        report.status = body.status;
        report.reviewedBy = g.session.account.actorId;
        report.reviewedAt = now;
        report.reviewComment = body.comment ?? null;
        report.consumedNights = consumed;
        report.rowVersion += 1;
        booking.updatedAt = now;
        return HttpResponse.json({
          report: toNoShow(report),
          booking: tools.toBooking(booking),
        } satisfies Schemas['NoShowResult']);
      },
    ),

    // -------------------------------------------------------------------------------------
    // listWaitlist
    // -------------------------------------------------------------------------------------
    http.get(`${ANY}/api/v1/accommodation/waitlist`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      sweepWaitlist(g.tenantId);
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return validationFailed(api, [
          { field: 'limit', code: 'RANGE', message: '1 ile 200 arasında olmalı' },
        ]);
      }
      const status = url.searchParams.get('status');
      if (status && !WAITLIST_STATUSES.has(status)) {
        return validationFailed(api, [
          { field: 'status', code: 'ENUM', message: 'tanınmayan bekleme listesi durumu' },
        ]);
      }
      const propertyId = url.searchParams.get('propertyId');
      // The member boundary is computed from the grant and never widened by a filter.
      const bound = personScope(api, g.session, g.tenantId);
      const personId = bound ?? url.searchParams.get('personId');
      const scope = organizationScope(api, g.session, g.tenantId);

      const items = world()
        .waitlistEntries.filter((e) => {
          if (e.tenantId !== g.tenantId) return false;
          if (personId && e.personId !== personId) return false;
          if (propertyId && e.propertyId !== propertyId) return false;
          if (status && e.status !== status) return false;
          if (scope !== null) {
            const property = world().properties.find((p) => p.id === e.propertyId);
            if (!property || !withinScope(scope, property.providerOrganizationId)) return false;
          }
          return true;
        })
        .sort((a, b) => b.priority - a.priority || a.createdAt.localeCompare(b.createdAt))
        .slice(0, limit)
        .map(toWaitlistEntry);
      return HttpResponse.json({ items } satisfies Schemas['WaitlistEntryList']);
    }),

    // -------------------------------------------------------------------------------------
    // joinWaitlist
    // -------------------------------------------------------------------------------------
    http.post(`${ANY}/api/v1/accommodation/waitlist`, async ({ request }) => {
      await wait(api);
      const g = guardWaitlist(request, true);
      if ('error' in g) return g.error;
      const rawKey = request.headers.get('Idempotency-Key');
      if (!rawKey) {
        return problem(api, 400, 'IDEMPOTENCY_KEY_REQUIRED', 'Idempotency-Key başlığı gerekli');
      }
      const key = `${g.tenantId}:${rawKey}`;
      const replay = api.replay(key);
      if (replay) {
        return HttpResponse.json(replay.body as Schemas['WaitlistEntry'], {
          status: replay.status,
        });
      }
      const body = await readJson<Schemas['JoinWaitlistRequest']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const whose = resolvePerson(api, g.session, g.tenantId, body.personId);
      if ('error' in whose) return whose.error;

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
      if (errors.length === 0 && nightsOf(body.checkIn, body.checkOut) === 0) {
        errors.push({
          field: 'checkOut',
          code: 'RANGE',
          message: 'giriş tarihinden sonra olmalı',
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const property = world().properties.find(
        (p) => p.id === body.propertyId && p.tenantId === g.tenantId,
      );
      if (!property) return problem(api, 404, 'PROPERTY_NOT_FOUND', 'Tesis bulunamadı');
      if (body.roomTypeId) {
        const room = world().roomTypes.find(
          (r) => r.id === body.roomTypeId && r.propertyId === property.id,
        );
        if (!room) return problem(api, 404, 'ROOM_TYPE_NOT_FOUND', 'Oda tipi bulunamadı');
      }
      const enrollment = world().enrollments.find(
        (e) => e.tenantId === g.tenantId && e.personId === whose.personId && e.status === 'ACTIVE',
      );
      if (!enrollment) {
        return problem(
          api,
          422,
          'ENROLLMENT_NOT_FOUND',
          'Bu tarihlerde geçerli bir plan kaydı yok',
          {
            detail: 'Bekleme kaydı, giriş tarihinde aktif bir plan kaydı üzerinden yapılır.',
          },
        );
      }
      // One live place in the queue per person, property and arrival. The server states it as
      // a partial unique index, so a cancelled or expired entry does not stop a member asking
      // again.
      const clash = world().waitlistEntries.find(
        (e) =>
          e.tenantId === g.tenantId &&
          e.personId === whose.personId &&
          e.propertyId === property.id &&
          e.checkIn === body.checkIn &&
          (e.status === 'WAITING' || e.status === 'OFFERED'),
      );
      if (clash) {
        return problem(
          api,
          409,
          'WAITLIST_ALREADY_WAITING',
          'Bu tesis ve tarih için zaten bekleme kaydınız var',
          {
            detail:
              'Aynı kişi, aynı tesis ve aynı giriş tarihi için tek bir açık bekleme kaydı olabilir.',
          },
        );
      }

      // A member joining for themselves is placed at zero whatever they send: a queue a
      // member can push themselves up is not a queue.
      const desk = personScope(api, g.session, g.tenantId) === null;
      const entry: StoredWaitlistEntry = {
        id: world().nextId(),
        tenantId: g.tenantId,
        personId: whose.personId,
        enrollmentId: enrollment.id,
        propertyId: property.id,
        roomTypeId: body.roomTypeId ?? null,
        checkIn: body.checkIn,
        checkOut: body.checkOut,
        adults,
        children,
        priority: desk ? (body.priority ?? 0) : 0,
        status: 'WAITING',
        offeredBookingId: null,
        offerExpiresAt: null,
        createdAt: new Date().toISOString(),
        rowVersion: 1,
      };
      world().waitlistEntries.push(entry);
      const out = toWaitlistEntry(entry);
      api.rememberIdempotent(key, 201, out, null);
      return HttpResponse.json(out, { status: 201 });
    }),

    // -------------------------------------------------------------------------------------
    // cancelWaitlistEntry
    // -------------------------------------------------------------------------------------
    http.post(
      `${ANY}/api/v1/accommodation/waitlist/:waitlistEntryId/cancel`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardWaitlist(request, true);
        if ('error' in g) return g.error;
        const entry = findEntry(g.session, g.tenantId, pathParam(params, 'waitlistEntryId'));
        if (!entry) {
          return problem(api, 404, 'WAITLIST_ENTRY_NOT_FOUND', 'Bekleme listesi kaydı bulunamadı');
        }
        if (entry.status !== 'WAITING' && entry.status !== 'OFFERED') {
          return problem(
            api,
            409,
            'WAITLIST_TRANSITION_INVALID',
            'Bekleme kaydı bu işlem için uygun durumda değil',
            { detail: 'Kaydın güncel durumunu alıp yeniden deneyin.' },
          );
        }
        // A room set aside for somebody who has walked away is a room nobody can book.
        const offered = bookingOfEntry(entry);
        if (offered && HELD_STATUSES.has(offered.status)) {
          tools.moveHeld(offered, -1);
          offered.status = 'CANCELLED';
          offered.cancelledAt = new Date().toISOString();
          offered.cancelReasonCode = 'HOLD_RELEASED';
          offered.holdExpiresAt = null;
          offered.rowVersion += 1;
        }
        entry.status = 'CANCELLED';
        entry.offeredBookingId = null;
        entry.offerExpiresAt = null;
        entry.rowVersion += 1;
        return HttpResponse.json(toWaitlistEntry(entry));
      },
    ),

    // -------------------------------------------------------------------------------------
    // acceptWaitlistOffer
    // -------------------------------------------------------------------------------------
    http.post(
      `${ANY}/api/v1/accommodation/waitlist/:waitlistEntryId/accept`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardWaitlist(request, true);
        if ('error' in g) return g.error;
        sweepWaitlist(g.tenantId);
        const entry = findEntry(g.session, g.tenantId, pathParam(params, 'waitlistEntryId'));
        if (!entry) {
          return problem(api, 404, 'WAITLIST_ENTRY_NOT_FOUND', 'Bekleme listesi kaydı bulunamadı');
        }
        const offered = bookingOfEntry(entry);
        if (entry.status !== 'OFFERED' || !offered) {
          return problem(api, 409, 'WAITLIST_NOT_OFFERED', 'Bu kayda açık bir teklif yok', {
            detail: 'Teklifin süresi dolmuş olabilir; sıradaki yerinizi koruyoruz.',
          });
        }
        // An offer accepted *is* a booking confirmed, so it goes through the same path.
        const refusal = tools.confirmHold(g.tenantId, offered);
        if (refusal) return refusal;
        entry.status = 'ACCEPTED';
        entry.offerExpiresAt = null;
        entry.rowVersion += 1;
        return HttpResponse.json(toWaitlistEntry(entry));
      },
    ),
  ];

  function policyMissing(): Response {
    return problem(
      api,
      409,
      'POLICY_SNAPSHOT_MISSING',
      'Rezervasyonun iptal koşulları kayıtlı değil',
      {
        detail:
          'İptal, rezervasyonun onaylandığı andaki koşullara göre hesaplanır; ' +
          'bu rezervasyonda o kayıt yok.',
      },
    );
  }

  /** A hold is given back with releaseHold; a guest who has arrived checks out. */
  function cancellable(booking: StoredBooking): Response | null {
    if (booking.status === 'CONFIRMED' || booking.status === 'PENDING_APPROVAL') return null;
    if (booking.status === 'CHECKED_IN' || booking.status === 'COMPLETED') {
      return problem(
        api,
        409,
        'BOOKING_CANCELLATION_TOO_LATE',
        'Giriş yapılmış rezervasyon iptal edilemez',
        { detail: 'Konaklama başladıktan sonra iptal değil, çıkış işlemi yapılır.' },
      );
    }
    return transitionInvalid();
  }

  function findEntry(
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredWaitlistEntry | undefined {
    const row = world().waitlistEntries.find((e) => e.id === id && e.tenantId === tenantId);
    if (!row) return undefined;
    const bound = personScope(api, session, tenantId);
    if (bound !== null && row.personId !== bound) return undefined;
    const scope = organizationScope(api, session, tenantId);
    if (scope === null) return row;
    const property = world().properties.find((p) => p.id === row.propertyId);
    return property && withinScope(scope, property.providerOrganizationId) ? row : undefined;
  }
}
