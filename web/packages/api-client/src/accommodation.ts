import { randomId, type KapsoraClient } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type Property = components['schemas']['Property'];
export type PropertyPage = components['schemas']['PropertyPage'];
export type PropertyType = components['schemas']['PropertyType'];
export type PropertyStatus = components['schemas']['PropertyStatus'];
export type PropertyAmenity = components['schemas']['PropertyAmenity'];
export type CreateProperty = components['schemas']['CreateProperty'];
export type PatchProperty = components['schemas']['PatchProperty'];
export type RoomType = components['schemas']['RoomType'];
export type RoomTypeList = components['schemas']['RoomTypeList'];
export type CreateRoomType = components['schemas']['CreateRoomType'];
export type PatchRoomType = components['schemas']['PatchRoomType'];
export type InventoryDay = components['schemas']['InventoryDay'];
export type RoomTypeInventoryRange = components['schemas']['RoomTypeInventoryRange'];
export type PutRoomTypeInventory = components['schemas']['PutRoomTypeInventory'];
export type AvailabilitySearchRequest = components['schemas']['AvailabilitySearchRequest'];
export type AvailabilitySearchResult = components['schemas']['AvailabilitySearchResult'];
export type AvailabilityRoomTypeResult = components['schemas']['AvailabilityRoomTypeResult'];
export type AvailabilityQuote = components['schemas']['AvailabilityQuote'];
export type AvailabilityEntitlement = components['schemas']['AvailabilityEntitlement'];
export type QuoteUnavailableReason = components['schemas']['QuoteUnavailableReason'];
export type Booking = components['schemas']['Booking'];
export type BookingList = components['schemas']['BookingList'];
export type BookingStatus = components['schemas']['BookingStatus'];
export type BookingGuest = components['schemas']['BookingGuest'];
export type BookingGuestType = components['schemas']['BookingGuestType'];
export type BookingNight = components['schemas']['BookingNight'];
export type BookingQuoteSnapshot = components['schemas']['BookingQuoteSnapshot'];
export type BookingVoucher = components['schemas']['BookingVoucher'];
export type CreateHoldRequest = components['schemas']['CreateHoldRequest'];
export type CreateBookingGuest = components['schemas']['CreateBookingGuest'];
export type LodgingPolicySnapshot = components['schemas']['LodgingPolicySnapshot'];
export type LodgingTerms = components['schemas']['LodgingTerms'];
export type LodgingTermsPolicy = components['schemas']['LodgingTermsPolicy'];
export type LodgingPenaltyKind = components['schemas']['LodgingPenaltyKind'];
export type PutLodgingTermsRequest = components['schemas']['PutLodgingTermsRequest'];
export type CancellationPreview = components['schemas']['CancellationPreview'];
export type CancellationQuote = components['schemas']['CancellationQuote'];
export type CancellationResult = components['schemas']['CancellationResult'];
export type Cancellation = components['schemas']['Cancellation'];
export type CancelBookingRequest = components['schemas']['CancelBookingRequest'];
export type CheckInBookingRequest = components['schemas']['CheckInBookingRequest'];
export type CheckOutBookingRequest = components['schemas']['CheckOutBookingRequest'];
export type ReportNoShowRequest = components['schemas']['ReportNoShowRequest'];
export type ReviewNoShowRequest = components['schemas']['ReviewNoShowRequest'];
export type NoShowResult = components['schemas']['NoShowResult'];
export type NoShowReport = components['schemas']['NoShowReport'];
export type NoShowStatus = components['schemas']['NoShowStatus'];
export type WaitlistEntry = components['schemas']['WaitlistEntry'];
export type WaitlistEntryList = components['schemas']['WaitlistEntryList'];
export type WaitlistStatus = components['schemas']['WaitlistStatus'];
export type JoinWaitlistRequest = components['schemas']['JoinWaitlistRequest'];
export type MyPerson = components['schemas']['MyPerson'];

/** Filters of the property list. */
export interface PropertyListQuery {
  cursor?: string;
  limit?: number;
  providerOrganizationId?: string;
  status?: PropertyStatus;
  propertyType?: PropertyType;
  regionCode?: string;
  city?: string;
}

/** Filters of the booking list. */
export interface BookingListQuery {
  cursor?: string;
  limit?: number;
  personId?: string;
  propertyId?: string;
  status?: BookingStatus;
  checkInFrom?: string;
  checkInTo?: string;
}

/** Filters of the waiting list. */
export interface WaitlistQuery {
  limit?: number;
  personId?: string;
  propertyId?: string;
  status?: WaitlistStatus;
}

/**
 * The accommodation vertical: what a provider has, what a member may have, and what happens
 * to a promise of a room.
 *
 * Every amount is an exact decimal string the server produced — the search's quote is
 * summed once there, the hold freezes it, and nothing here adds, rounds or compares money.
 * A member account is bound to one person on the server: the member-side calls take no
 * `personId` and a body naming one is refused with PERSON_SCOPE; a desk names the person.
 *
 * The voucher token is the one value that must not linger. `voucher` returns it exactly
 * once, from a route the idempotency store does not keep; a screen shows it and forgets it.
 */
export function accommodationOperations(client: KapsoraClient) {
  const read = (tenantId: string) => ({ 'X-Tenant-ID': tenantId });
  const create = (tenantId: string, idempotencyKey: string) => ({
    'X-Tenant-ID': tenantId,
    'Idempotency-Key': idempotencyKey,
  });
  const command = (tenantId: string, etag: string, idempotencyKey: string) => ({
    'X-Tenant-ID': tenantId,
    'If-Match': etag,
    'Idempotency-Key': idempotencyKey,
  });

  return {
    // --- Properties, room types, the allotment ---------------------------------------

    async listProperties(tenantId: string, query: PropertyListQuery = {}): Promise<PropertyPage> {
      const q: PropertyListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.providerOrganizationId) q.providerOrganizationId = query.providerOrganizationId;
      if (query.status) q.status = query.status;
      if (query.propertyType) q.propertyType = query.propertyType;
      if (query.regionCode) q.regionCode = query.regionCode;
      if (query.city) q.city = query.city;
      return (
        await unwrap(
          client.GET('/api/v1/accommodation/properties', {
            params: { header: read(tenantId), query: q },
          }),
        )
      ).data;
    },

    async createProperty(
      tenantId: string,
      body: CreateProperty,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Property>> {
      const r = await unwrap(
        client.POST('/api/v1/accommodation/properties', {
          params: { header: create(tenantId, idempotencyKey) },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getProperty(tenantId: string, propertyId: string): Promise<Versioned<Property>> {
      const r = await unwrap(
        client.GET('/api/v1/accommodation/properties/{propertyId}', {
          params: { header: read(tenantId), path: { propertyId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchProperty(
      tenantId: string,
      propertyId: string,
      etag: string,
      body: PatchProperty,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Property>> {
      const r = await unwrap(
        client.PATCH('/api/v1/accommodation/properties/{propertyId}', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { propertyId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async listRoomTypes(
      tenantId: string,
      propertyId: string,
      status?: PropertyStatus,
    ): Promise<RoomType[]> {
      return (
        await unwrap(
          client.GET('/api/v1/accommodation/properties/{propertyId}/room-types', {
            params: {
              header: read(tenantId),
              path: { propertyId },
              query: status ? { status } : {},
            },
          }),
        )
      ).data.items;
    },

    async createRoomType(
      tenantId: string,
      propertyId: string,
      body: CreateRoomType,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<RoomType>> {
      const r = await unwrap(
        client.POST('/api/v1/accommodation/properties/{propertyId}/room-types', {
          params: { header: create(tenantId, idempotencyKey), path: { propertyId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchRoomType(
      tenantId: string,
      roomTypeId: string,
      etag: string,
      body: PatchRoomType,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<RoomType>> {
      const r = await unwrap(
        client.PATCH('/api/v1/accommodation/room-types/{roomTypeId}', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { roomTypeId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** The allotment over a date range, with `available` computed by the server. */
    async getInventory(
      tenantId: string,
      roomTypeId: string,
      from: string,
      to: string,
    ): Promise<RoomTypeInventoryRange> {
      return (
        await unwrap(
          client.GET('/api/v1/accommodation/room-types/{roomTypeId}/inventory', {
            params: { header: read(tenantId), path: { roomTypeId }, query: { from, to } },
          }),
        )
      ).data;
    },

    /**
     * Opens or resizes the allotment for a range. A capacity below what is already held or
     * confirmed on any night is refused with INVENTORY_BELOW_COMMITMENT naming that night.
     */
    async putInventory(
      tenantId: string,
      roomTypeId: string,
      body: PutRoomTypeInventory,
      idempotencyKey: string = randomId(),
    ): Promise<RoomTypeInventoryRange> {
      return (
        await unwrap(
          client.PUT('/api/v1/accommodation/room-types/{roomTypeId}/inventory', {
            params: { header: create(tenantId, idempotencyKey), path: { roomTypeId } },
            body,
          }),
        )
      ).data;
    },

    // --- Search, hold, booking ----------------------------------------------------------

    /** What is free between two dates and what the member would pay, per room type. */
    async search(
      tenantId: string,
      body: AvailabilitySearchRequest,
    ): Promise<AvailabilitySearchResult> {
      return (
        await unwrap(
          client.POST('/api/v1/accommodation/availability/search', {
            params: { header: read(tenantId) },
            body,
          }),
        )
      ).data;
    },

    /** A room set aside under the lock, with the quote frozen and the countdown started. */
    async hold(
      tenantId: string,
      body: CreateHoldRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Booking>> {
      const r = await unwrap(
        client.POST('/api/v1/accommodation/holds', {
          params: { header: create(tenantId, idempotencyKey) },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async listBookings(tenantId: string, query: BookingListQuery = {}): Promise<BookingList> {
      const q: BookingListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.personId) q.personId = query.personId;
      if (query.propertyId) q.propertyId = query.propertyId;
      if (query.status) q.status = query.status;
      if (query.checkInFrom) q.checkInFrom = query.checkInFrom;
      if (query.checkInTo) q.checkInTo = query.checkInTo;
      return (
        await unwrap(
          client.GET('/api/v1/accommodation/bookings', {
            params: { header: read(tenantId), query: q },
          }),
        )
      ).data;
    },

    async getBooking(tenantId: string, bookingId: string): Promise<Versioned<Booking>> {
      const r = await unwrap(
        client.GET('/api/v1/accommodation/bookings/{bookingId}', {
          params: { header: read(tenantId), path: { bookingId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Keeps the promise. The request behind it is decided asynchronously: the answer is the
     * booking as it stands now (still HOLD, or PENDING_APPROVAL), and the screen refreshes
     * until it reads CONFIRMED. A step-up is asked for above the tenant's member-amount
     * threshold.
     */
    async confirm(
      tenantId: string,
      bookingId: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Booking>> {
      const r = await unwrap(
        client.POST('/api/v1/accommodation/bookings/{bookingId}/confirm', {
          params: { header: create(tenantId, idempotencyKey), path: { bookingId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /** The member giving the room back before the countdown ends. */
    async release(
      tenantId: string,
      bookingId: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Booking>> {
      const r = await unwrap(
        client.POST('/api/v1/accommodation/bookings/{bookingId}/release', {
          params: { header: create(tenantId, idempotencyKey), path: { bookingId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Mints the voucher and returns its token exactly once, retiring any previous token in
     * the same transaction. Not idempotent by design: a replayed body would be a second
     * copy of a door key.
     */
    async voucher(tenantId: string, bookingId: string): Promise<BookingVoucher> {
      return (
        await unwrap(
          client.POST('/api/v1/accommodation/bookings/{bookingId}/voucher', {
            params: { header: read(tenantId), path: { bookingId } },
          }),
        )
      ).data;
    },

    // --- After the promise: cancellation, the door, no-show -----------------------------

    /** What cancelling now would cost, judged by the booking's own policy snapshot. */
    async previewCancellation(tenantId: string, bookingId: string): Promise<CancellationPreview> {
      return (
        await unwrap(
          client.POST('/api/v1/accommodation/bookings/{bookingId}/cancellation-preview', {
            params: { header: read(tenantId), path: { bookingId } },
          }),
        )
      ).data;
    },

    async cancel(
      tenantId: string,
      bookingId: string,
      body: CancelBookingRequest = {},
      idempotencyKey: string = randomId(),
    ): Promise<CancellationResult> {
      return (
        await unwrap(
          client.POST('/api/v1/accommodation/bookings/{bookingId}/cancel', {
            params: { header: create(tenantId, idempotencyKey), path: { bookingId } },
            body,
          }),
        )
      ).data;
    },

    /** The desk redeeming the guest's token. The token travels in the body and nowhere else. */
    async checkIn(
      tenantId: string,
      bookingId: string,
      body: CheckInBookingRequest,
    ): Promise<Versioned<Booking>> {
      const r = await unwrap(
        client.POST('/api/v1/accommodation/bookings/{bookingId}/check-in', {
          params: { header: read(tenantId), path: { bookingId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async checkOut(
      tenantId: string,
      bookingId: string,
      body: CheckOutBookingRequest = {},
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Booking>> {
      const r = await unwrap(
        client.POST('/api/v1/accommodation/bookings/{bookingId}/check-out', {
          params: { header: create(tenantId, idempotencyKey), path: { bookingId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async reportNoShow(
      tenantId: string,
      bookingId: string,
      body: ReportNoShowRequest,
      idempotencyKey: string = randomId(),
    ): Promise<NoShowResult> {
      return (
        await unwrap(
          client.POST('/api/v1/accommodation/bookings/{bookingId}/no-show', {
            params: { header: create(tenantId, idempotencyKey), path: { bookingId } },
            body,
          }),
        )
      ).data;
    },

    async getNoShow(tenantId: string, bookingId: string): Promise<NoShowResult> {
      return (
        await unwrap(
          client.GET('/api/v1/accommodation/bookings/{bookingId}/no-show', {
            params: { header: read(tenantId), path: { bookingId } },
          }),
        )
      ).data;
    },

    async reviewNoShow(
      tenantId: string,
      bookingId: string,
      body: ReviewNoShowRequest,
      idempotencyKey: string = randomId(),
    ): Promise<NoShowResult> {
      return (
        await unwrap(
          client.POST('/api/v1/accommodation/bookings/{bookingId}/no-show/review', {
            params: { header: create(tenantId, idempotencyKey), path: { bookingId } },
            body,
          }),
        )
      ).data;
    },

    // --- The waiting list ---------------------------------------------------------------

    async listWaitlist(tenantId: string, query: WaitlistQuery = {}): Promise<WaitlistEntry[]> {
      const q: WaitlistQuery = {};
      if (query.limit) q.limit = query.limit;
      if (query.personId) q.personId = query.personId;
      if (query.propertyId) q.propertyId = query.propertyId;
      if (query.status) q.status = query.status;
      return (
        await unwrap(
          client.GET('/api/v1/accommodation/waitlist', {
            params: { header: read(tenantId), query: q },
          }),
        )
      ).data.items;
    },

    async joinWaitlist(
      tenantId: string,
      body: JoinWaitlistRequest,
      idempotencyKey: string = randomId(),
    ): Promise<WaitlistEntry> {
      return (
        await unwrap(
          client.POST('/api/v1/accommodation/waitlist', {
            params: { header: create(tenantId, idempotencyKey) },
            body,
          }),
        )
      ).data;
    },

    async cancelWaitlistEntry(
      tenantId: string,
      waitlistEntryId: string,
      idempotencyKey: string = randomId(),
    ): Promise<WaitlistEntry> {
      return (
        await unwrap(
          client.POST('/api/v1/accommodation/waitlist/{waitlistEntryId}/cancel', {
            params: { header: create(tenantId, idempotencyKey), path: { waitlistEntryId } },
          }),
        )
      ).data;
    },

    /** Takes the offered hold; the entry comes back ACCEPTED with the booking in `offer`. */
    async acceptWaitlistOffer(
      tenantId: string,
      waitlistEntryId: string,
      idempotencyKey: string = randomId(),
    ): Promise<WaitlistEntry> {
      return (
        await unwrap(
          client.POST('/api/v1/accommodation/waitlist/{waitlistEntryId}/accept', {
            params: { header: create(tenantId, idempotencyKey), path: { waitlistEntryId } },
          }),
        )
      ).data;
    },

    // --- Lodging terms on the contract version --------------------------------------------

    /** The version's lodging terms as the contract desk edits them; 404 while none are written. */
    async lodgingTerms(
      tenantId: string,
      contractVersionId: string,
    ): Promise<Versioned<LodgingTerms>> {
      const r = await unwrap(
        client.GET('/api/v1/contract-versions/{contractVersionId}/lodging-terms', {
          params: { header: read(tenantId), path: { contractVersionId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Replaces the terms of a DRAFT version; any other status is refused by the server. */
    async putLodgingTerms(
      tenantId: string,
      contractVersionId: string,
      etag: string,
      body: PutLodgingTermsRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<LodgingTerms>> {
      const r = await unwrap(
        client.PUT('/api/v1/contract-versions/{contractVersionId}/lodging-terms', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { contractVersionId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** The snapshot a booking confirmed under this version would freeze, stamped now. */
    async lodgingPolicy(
      tenantId: string,
      contractVersionId: string,
    ): Promise<LodgingPolicySnapshot> {
      return (
        await unwrap(
          client.GET('/api/v1/contract-versions/{contractVersionId}/lodging-policy', {
            params: { header: read(tenantId), path: { contractVersionId } },
          }),
        )
      ).data;
    },

    // --- The member's own person ---------------------------------------------------------

    /** The person this account acts for: the masked identifier, the enrollments, the contacts. */
    async myPerson(tenantId: string): Promise<MyPerson> {
      return (
        await unwrap(
          client.GET('/api/v1/me/person', {
            params: { header: read(tenantId) },
          }),
        )
      ).data;
    },
  };
}
