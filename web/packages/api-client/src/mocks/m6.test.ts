/**
 * End-to-end flows through the typed client against the M6 accommodation mock world: the
 * allotment, the availability search and the contribution quote.
 *
 * The mock is a test double of the Go server and M6 treats a divergence in either direction
 * as a bug, so every test below is written against a behaviour the server has and a plausible
 * mock would get wrong: the allotment that is refused for the whole season by one full
 * weekend inside it, the stay that reaches one night past the last allotted night and is
 * therefore not available at all, the split that has to add up on every night rather than
 * only on the total, the member with two nights left and a three-night stay, the checkout on
 * the day of arrival, and the room type that belongs to somebody else and is 404 rather
 * than 403.
 *
 * Every one of them asserts a figure or a code. A test that only asserted "no error" would
 * pass against a handler that answered zeroes.
 */
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { createKapsoraClient, randomId, type KapsoraClient } from '../client';
import { createOperations } from '../operations';
import { ApiError, unwrap, type Problem } from '../problem';
import { fromMicros, toMicros, type Decimal } from './data';
import { createMockServer } from './node';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';
const DAY_MS = 86_400_000;

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

function client(): KapsoraClient {
  return createKapsoraClient({ baseUrl: BASE, csrfToken: () => api.session?.csrfToken ?? null });
}

interface Session {
  c: KapsoraClient;
  tenantId: string;
}

async function signIn(username: string): Promise<Session> {
  const c = client();
  const o = createOperations(c);
  await o.session.login(username, PASSWORD);
  const session = await o.session.get();
  const tenantId = session.activeTenantId ?? (await o.session.tenants())[0]!.id;
  if (!session.activeTenantId) await o.session.switchTenant(tenantId);
  return { c, tenantId };
}

const key = (): string => `mock-${randomId()}`;
const tenant = (s: Session) => ({ 'X-Tenant-ID': s.tenantId });

async function refusal(call: Promise<unknown>): Promise<Problem & Record<string, unknown>> {
  try {
    await call;
  } catch (error) {
    if (error instanceof ApiError) return error.problem as Problem & Record<string, unknown>;
    throw error;
  }
  throw new Error('expected the call to be refused');
}

// --- fixtures, found by what they are rather than by a generated id ----------------------

function roomTypeByCode(code: string) {
  const row = api.world.roomTypes.find((r) => r.code === code);
  if (!row) throw new Error(`fixture: no room type ${code}`);
  return row;
}

function propertyOf(roomTypeCode: string) {
  const room = roomTypeByCode(roomTypeCode);
  return api.world.properties.find((p) => p.id === room.propertyId)!;
}

function personByFirstName(firstName: string) {
  const row = api.world.people.find((p) => p.firstName === firstName && p.lastName === 'Aydemir');
  if (!row) throw new Error(`fixture: no person ${firstName} Aydemir`);
  return row;
}

/** The seeded nights of one room type, in date order. */
function allottedNights(roomTypeId: string): string[] {
  return api.world.inventoryDays
    .filter((d) => d.roomTypeId === roomTypeId)
    .map((d) => d.stayDate)
    .sort();
}

/** The seeded night on which this room type is sold out: held plus confirmed is the capacity. */
function soldOutNights(roomTypeId: string): string[] {
  return api.world.inventoryDays
    .filter((d) => d.roomTypeId === roomTypeId && d.held + d.confirmed === d.capacity)
    .map((d) => d.stayDate)
    .sort();
}

const addDays = (date: string, days: number): string =>
  new Date(Date.parse(`${date}T00:00:00.000Z`) + days * DAY_MS).toISOString().slice(0, 10);

/** Adds exact decimal strings the way the server does: in integer micro-units, never a float. */
const sum = (...values: Decimal[]): Decimal =>
  fromMicros(values.reduce((acc, v) => acc + toMicros(v), 0n));

// --- calls --------------------------------------------------------------------------------

async function getInventory(s: Session, roomTypeId: string, from: string, to: string) {
  return (
    await unwrap(
      s.c.GET('/api/v1/accommodation/room-types/{roomTypeId}/inventory', {
        params: { header: tenant(s), path: { roomTypeId }, query: { from, to } },
      }),
    )
  ).data;
}

function putInventory(
  s: Session,
  roomTypeId: string,
  body: { from: string; to: string; capacity: number },
) {
  return unwrap(
    s.c.PUT('/api/v1/accommodation/room-types/{roomTypeId}/inventory', {
      params: { header: { ...tenant(s), 'Idempotency-Key': key() }, path: { roomTypeId } },
      body,
    }),
  );
}

interface SearchBody {
  personId: string;
  checkIn: string;
  checkOut: string;
  adults: number;
  children?: number;
  propertyId?: string;
  regionCode?: string;
}

function search(s: Session, body: SearchBody) {
  return unwrap(
    s.c.POST('/api/v1/accommodation/availability/search', {
      params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
      body,
    }),
  );
}

describe('the accommodation allotment', () => {
  it('refuses the whole range for one committed night, names it, and writes nothing', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('STD_DBL');
    const soldOut = soldOutNights(room.id);
    // The seeded world sells this room type out over one Saturday and the Sunday after it.
    expect(soldOut).toHaveLength(2);
    const from = addDays(soldOut[0]!, -5);
    const to = addDays(soldOut[1]!, 5);
    const before = await getInventory(s, room.id, from, to);
    const capacityBefore = before.days.map((d) => d.capacity);
    expect(new Set(capacityBefore)).toEqual(new Set([12]));

    const refused = await refusal(putInventory(s, room.id, { from, to, capacity: 4 }));
    expect(refused.status).toBe(409);
    expect(refused.code).toBe('INVENTORY_BELOW_COMMITMENT');
    // The *first* offending date, not merely one of them: the Saturday, never the Sunday.
    expect(refused.stayDate).toBe(soldOut[0]!);
    expect(refused.stayDate).not.toBe(soldOut[1]!);
    expect(refused.capacity).toBe(4);
    expect(refused.held).toBe(2);
    expect(refused.confirmed).toBe(10);

    // Nothing was written: not the nights before the offending one, and not the ones after.
    const after = await getInventory(s, room.id, from, to);
    expect(after.days.map((d) => d.capacity)).toEqual(capacityBefore);
    expect(after.days.map((d) => d.rowVersion)).toEqual(before.days.map((d) => d.rowVersion));
  });

  it('opens a range in one call without touching held or confirmed', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('STD_DBL');
    const nights = allottedNights(room.id);
    const from = nights[0]!;
    const to = nights[4]!;
    const before = await getInventory(s, room.id, from, to);

    const written = (await putInventory(s, room.id, { from, to, capacity: 20 })).data;
    expect(written.days).toHaveLength(5);
    expect(written.days.every((d) => d.capacity === 20)).toBe(true);
    // held and confirmed belong to the holds and bookings of WP-I6-02 and are untouched.
    expect(written.days.map((d) => d.held)).toEqual(before.days.map((d) => d.held));
    expect(written.days.map((d) => d.confirmed)).toEqual(before.days.map((d) => d.confirmed));
    // available is the server's subtraction, never a screen's.
    expect(written.days.map((d) => d.available)).toEqual(
      written.days.map((d) => d.capacity - d.held - d.confirmed),
    );
  });

  it('answers a night with no row as unallotted rather than as zero free', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('STD_DBL');
    const nights = allottedNights(room.id);
    const last = nights[nights.length - 1]!;
    const range = await getInventory(s, room.id, addDays(last, -1), last);

    const past = await getInventory(s, room.id, last, addDays(last, 2));
    expect(past.days.map((d) => d.stayDate)).toEqual([last, addDays(last, 1), addDays(last, 2)]);
    expect(past.days[0]!.allotted).toBe(true);
    expect(past.days[0]!.rowVersion).toBeDefined();
    for (const gap of past.days.slice(1)) {
      expect(gap.allotted).toBe(false);
      expect(gap.capacity).toBe(0);
      expect(gap.available).toBe(0);
      expect(gap.rowVersion).toBeUndefined();
      expect(gap.updatedAt ?? null).toBeNull();
    }
    expect(range.days.length).toBeGreaterThan(0);
  });

  it('refuses a range that ends before it starts and one longer than 730 nights', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('STD_DBL');
    const from = allottedNights(room.id)[0]!;

    const backwards = await refusal(
      putInventory(s, room.id, { from, to: addDays(from, -1), capacity: 1 }),
    );
    expect(backwards.status).toBe(422);
    expect(backwards.errors?.[0]?.field).toBe('to');

    const tooLong = await refusal(
      putInventory(s, room.id, { from, to: addDays(from, 730), capacity: 1 }),
    );
    expect(tooLong.errors?.[0]?.field).toBe('to');

    const tooMuch = await refusal(
      putInventory(s, room.id, { from, to: addDays(from, 1), capacity: 10_001 }),
    );
    expect(tooMuch.errors?.[0]?.field).toBe('capacity');
  });
});

describe('the availability search', () => {
  it('answers none for a room type with an allotment on every night but one', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('STD_DBL');
    const property = propertyOf('STD_DBL');
    const person = personByFirstName('Kaan');
    const nights = allottedNights(room.id);
    const last = nights[nights.length - 1]!;

    // Two nights, both allotted: the room type is free.
    const inside = (
      await search(s, {
        personId: person.id,
        checkIn: addDays(last, -1),
        checkOut: addDays(last, 1),
        adults: 2,
        propertyId: property.id,
      })
    ).data;
    const insideRoom = inside.results.find((r) => r.roomType.code === 'STD_DBL')!;
    expect(inside.nights).toBe(2);
    expect(insideRoom.available).toBeGreaterThan(0);

    // The same stay with one more night on the end, and that night has no row at all.
    const past = (
      await search(s, {
        personId: person.id,
        checkIn: addDays(last, -1),
        checkOut: addDays(last, 2),
        adults: 2,
        propertyId: property.id,
      })
    ).data;
    expect(past.nights).toBe(3);
    // Every room type of the property, not only the one above: none of them is allotted on
    // the night past the end of the season.
    expect(past.results.length).toBeGreaterThan(0);
    for (const result of past.results) expect(result.available).toBe(0);
    // The price is still an answer: availability and cost are two different questions.
    expect(past.results.find((r) => r.roomType.code === 'STD_DBL')!.quote).not.toBeNull();
  });

  it('splits every night and the total so payer plus member is exactly the amount', async () => {
    const s = await signIn('admin.a');
    const property = propertyOf('STD_DBL');
    const person = personByFirstName('Kaan');
    const checkIn = allottedNights(roomTypeByCode('STD_DBL').id)[0]!;

    const result = (
      await search(s, {
        personId: person.id,
        checkIn,
        checkOut: addDays(checkIn, 3),
        adults: 2,
        propertyId: property.id,
      })
    ).data;
    expect(result.nights).toBe(3);
    expect(result.evaluationId).not.toBeNull();
    // The search is remembered as an eligibility evaluation, so "what did the system show
    // them" is answerable later.
    expect(api.world.evaluations.get(result.evaluationId!)?.personId).toBe(person.id);

    const room = result.results.find((r) => r.roomType.code === 'STD_DBL')!;
    const quote = room.quote!;
    expect(room.quoteUnavailableReason).toBeNull();
    expect(quote.currencyCode).toBe('TRY');
    expect(quote.nightlyAmounts).toHaveLength(3);

    // The high-season list wins on list priority inside its window: 4250 a night with the
    // contract's 15% member share, and never the 2400 of the standard list.
    for (const night of quote.nightlyAmounts) {
      expect(night.amount).toBe('4250.000000');
      expect(night.memberAmount).toBe('637.500000');
      expect(night.payerAmount).toBe('3612.500000');
      // On every night, and as exact strings.
      expect(sum(night.payerAmount, night.memberAmount)).toBe(night.amount);
    }
    // And on the total, which is the nights summed once and never rounded again.
    expect(quote.totalAmount).toBe(sum(...quote.nightlyAmounts.map((n) => n.amount)));
    expect(quote.payerAmount).toBe(sum(...quote.nightlyAmounts.map((n) => n.payerAmount)));
    expect(quote.memberAmount).toBe(sum(...quote.nightlyAmounts.map((n) => n.memberAmount)));
    expect(sum(quote.payerAmount, quote.memberAmount)).toBe(quote.totalAmount);
    expect(quote.totalAmount).toBe('12750.000000');
  });

  it('carries the first nights the plan has left and gives the member the rest', async () => {
    const s = await signIn('admin.a');
    const property = propertyOf('STD_DBL');
    // The spouse holds two nights of the NIGHT entitlement and the stay is three long.
    const spouse = personByFirstName('Sevgi');
    const checkIn = allottedNights(roomTypeByCode('STD_DBL').id)[0]!;

    const result = (
      await search(s, {
        personId: spouse.id,
        checkIn,
        checkOut: addDays(checkIn, 3),
        adults: 2,
        propertyId: property.id,
      })
    ).data;
    expect(result.entitlement).toEqual({
      entitlementCode: 'ACCOMMODATION_NIGHT',
      unit: 'NIGHT',
      remaining: '2.000000',
    });
    // Two nights left and a three-night stay is not "eligible with a caveat".
    expect(result.eligible).toBe(false);

    const quote = result.results.find((r) => r.roomType.code === 'STD_DBL')!.quote!;
    const [first, second, third] = quote.nightlyAmounts;
    for (const night of [first!, second!]) {
      expect(night.payerAmount).toBe('3612.500000');
      expect(night.memberAmount).toBe('637.500000');
    }
    // The third night is hers, whole: the plan carries nothing on it.
    expect(third!.payerAmount).toBe('0.000000');
    expect(third!.memberAmount).toBe(third!.amount);
    expect(third!.memberAmount).toBe('4250.000000');
    // The identity survives the partial cover, on the total as on every night.
    expect(quote.payerAmount).toBe('7225.000000');
    expect(quote.memberAmount).toBe('5525.000000');
    expect(sum(quote.payerAmount, quote.memberAmount)).toBe(quote.totalAmount);

    // The principal has ten nights left, so the same stay is covered whole and says so.
    const covered = (
      await search(s, {
        personId: personByFirstName('Kaan').id,
        checkIn,
        checkOut: addDays(checkIn, 3),
        adults: 2,
        propertyId: property.id,
      })
    ).data;
    expect(covered.entitlement?.remaining).toBe('10.000000');
    expect(covered.eligible).toBe(true);
  });

  it('refuses a checkout on the day of arrival and a stay of thirty-one nights', async () => {
    const s = await signIn('admin.a');
    const property = propertyOf('STD_DBL');
    const person = personByFirstName('Kaan');
    const checkIn = allottedNights(roomTypeByCode('STD_DBL').id)[0]!;
    const body = { personId: person.id, adults: 2, propertyId: property.id };

    const sameDay = await refusal(search(s, { ...body, checkIn, checkOut: checkIn }));
    expect(sameDay.status).toBe(422);
    expect(sameDay.code).toBe('VALIDATION_FAILED');
    expect(sameDay.errors?.[0]?.field).toBe('checkOut');

    const tooLong = await refusal(search(s, { ...body, checkIn, checkOut: addDays(checkIn, 31) }));
    expect(tooLong.status).toBe(422);
    expect(tooLong.errors?.[0]?.field).toBe('checkOut');

    // Thirty is the boundary and is accepted, so the refusal above is a bound and not a ban.
    const thirty = (await search(s, { ...body, checkIn, checkOut: addDays(checkIn, 30) })).data;
    expect(thirty.nights).toBe(30);

    // Exactly one of propertyId and regionCode.
    const neither = await refusal(
      search(s, { personId: person.id, adults: 2, checkIn, checkOut: addDays(checkIn, 1) }),
    );
    expect(neither.errors?.[0]?.field).toBe('propertyId');
    const both = await refusal(
      search(s, { ...body, regionCode: 'ANTALYA', checkIn, checkOut: addDays(checkIn, 1) }),
    );
    expect(both.errors?.[0]?.field).toBe('propertyId');

    // And a search with no person named at all.
    const anonymous = await refusal(
      search(s, {
        personId: '',
        adults: 2,
        propertyId: property.id,
        checkIn,
        checkOut: addDays(checkIn, 1),
      }),
    );
    expect(anonymous.status).toBe(422);
    expect(anonymous.code).toBe('PERSON_REQUIRED');
  });

  it('finds the region’s hotels and skips the room types the party does not fit', async () => {
    const s = await signIn('admin.a');
    const person = personByFirstName('Kaan');
    const checkIn = allottedNights(roomTypeByCode('STD_DBL').id)[0]!;

    const family = (
      await search(s, {
        personId: person.id,
        checkIn,
        checkOut: addDays(checkIn, 2),
        adults: 2,
        children: 2,
        regionCode: 'ANTALYA',
      })
    ).data;
    const codes = family.results.map((r) => r.roomType.code).sort();
    // A single room sleeps nobody's family and a double sleeps three, so only the two rooms
    // that hold four people are answers to this question.
    expect(codes).toEqual(['FAM_SUITE', 'STD_DBL']);
  });
});

describe('the provider boundary', () => {
  it("hides another provider's property and room type behind a 404", async () => {
    const room = roomTypeByCode('STD_DBL');
    const property = propertyOf('STD_DBL');
    const nights = allottedNights(room.id);

    // The desk of the organization that runs these hotels sees them.
    const own = await signIn('reservation.a');
    const mine = await getInventory(own, room.id, nights[0]!, nights[1]!);
    expect(mine.days).toHaveLength(2);
    const seen = await unwrap(
      own.c.GET('/api/v1/accommodation/properties/{propertyId}', {
        params: { header: tenant(own), path: { propertyId: property.id } },
      }),
    );
    expect(seen.data.code).toBe('KEMER_RESORT');

    // Another provider's desk is told the room type does not exist, never that it may not
    // look: that a hotel exists at all is not this caller's business.
    const other = await signIn('reservation.other');
    const hiddenInventory = await refusal(getInventory(other, room.id, nights[0]!, nights[1]!));
    expect(hiddenInventory.status).toBe(404);
    expect(hiddenInventory.code).toBe('ROOM_TYPE_NOT_FOUND');

    const hiddenProperty = await refusal(
      unwrap(
        other.c.GET('/api/v1/accommodation/properties/{propertyId}', {
          params: { header: tenant(other), path: { propertyId: property.id } },
        }),
      ),
    );
    expect(hiddenProperty.status).toBe(404);
    expect(hiddenProperty.code).toBe('PROPERTY_NOT_FOUND');

    // And the list is not a filtered view of everybody's: the hotels are simply not in it.
    const list = await unwrap(
      other.c.GET('/api/v1/accommodation/properties', {
        params: { header: tenant(other), query: {} },
      }),
    );
    expect(list.data.items).toHaveLength(0);
  });

  it('refuses a create naming another organization with 403 rather than 404', async () => {
    const other = await signIn('reservation.other');
    const property = propertyOf('STD_DBL');

    const refused = await refusal(
      unwrap(
        other.c.POST('/api/v1/accommodation/properties', {
          params: { header: { ...tenant(other), 'Idempotency-Key': key() } },
          body: {
            providerOrganizationId: property.providerOrganizationId,
            code: 'YENI_OTEL',
            name: 'Yeni Otel',
            propertyType: 'HOTEL',
            timezone: 'Europe/Istanbul',
          },
        }),
      ),
    );
    // A create has no existing row to hide behind, so the organization is not pretended away.
    expect(refused.status).toBe(403);
    expect(refused.code).toBe('PROPERTY_SCOPE');
    expect(api.world.properties.some((p) => p.code === 'YENI_OTEL')).toBe(false);
  });

  it('creates a property and a room type for its own organization', async () => {
    const own = await signIn('reservation.a');
    const property = propertyOf('STD_DBL');
    const service = api.world.serviceDefinitions.find((d) => d.code === 'ROOM_NIGHT')!;

    const created = await unwrap(
      own.c.POST('/api/v1/accommodation/properties', {
        params: { header: { ...tenant(own), 'Idempotency-Key': key() } },
        body: {
          providerOrganizationId: property.providerOrganizationId,
          code: 'BODRUM_OTEL',
          name: 'Kapsora Bodrum Oteli',
          propertyType: 'HOTEL',
          timezone: 'Europe/Istanbul',
          regionCode: 'MUGLA',
          amenities: ['POOL', 'WIFI'],
        },
      }),
    );
    expect(created.response.status).toBe(201);
    // The amenity list is stored in the contract's own order, deduplicated.
    expect(created.data.amenities).toEqual(['WIFI', 'POOL']);
    expect(created.data.rowVersion).toBe(1);

    const room = await unwrap(
      own.c.POST('/api/v1/accommodation/properties/{propertyId}/room-types', {
        params: {
          header: { ...tenant(own), 'Idempotency-Key': key() },
          path: { propertyId: created.data.id },
        },
        body: {
          code: 'DLX',
          name: 'Deluxe Oda',
          maxAdults: 2,
          maxChildren: 1,
          maxOccupancy: 3,
          serviceDefinitionId: service.id,
        },
      }),
    );
    expect(room.response.status).toBe(201);
    expect(room.data.serviceDefinitionId).toBe(service.id);

    // A room type may only ever name a NIGHT-unit service.
    const notNight = api.world.serviceDefinitions.find((d) => d.code === 'GP_VISIT')!;
    const refused = await refusal(
      unwrap(
        own.c.POST('/api/v1/accommodation/properties/{propertyId}/room-types', {
          params: {
            header: { ...tenant(own), 'Idempotency-Key': key() },
            path: { propertyId: created.data.id },
          },
          body: {
            code: 'BAD',
            name: 'Yanlış Birim',
            maxAdults: 2,
            maxOccupancy: 2,
            serviceDefinitionId: notNight.id,
          },
        }),
      ),
    );
    expect(refused.status).toBe(422);
    expect(refused.errors?.[0]?.field).toBe('serviceDefinitionId');
  });

  it('needs an If-Match to patch and refuses a stale one', async () => {
    const s = await signIn('admin.a');
    const property = propertyOf('STD_DBL');
    // The stored row is the live object the handler writes to, so its version is read now.
    const version = property.rowVersion;
    const body = {
      name: 'Kapsora Kemer Tatil Köyü',
      propertyType: 'RESORT' as const,
      timezone: 'Europe/Istanbul',
      status: 'INACTIVE' as const,
      amenities: ['WIFI' as const],
    };

    const missing = await refusal(
      unwrap(
        s.c.PATCH('/api/v1/accommodation/properties/{propertyId}', {
          params: {
            header: { ...tenant(s), 'If-Match': '', 'Idempotency-Key': key() },
            path: { propertyId: property.id },
          },
          body,
        }),
      ),
    );
    expect(missing.status).toBe(428);
    expect(missing.code).toBe('IF_MATCH_REQUIRED');

    const stale = await refusal(
      unwrap(
        s.c.PATCH('/api/v1/accommodation/properties/{propertyId}', {
          params: {
            header: { ...tenant(s), 'If-Match': '"99"', 'Idempotency-Key': key() },
            path: { propertyId: property.id },
          },
          body,
        }),
      ),
    );
    expect(stale.status).toBe(412);
    expect(stale.code).toBe('ETAG_MISMATCH');

    const patched = await unwrap(
      s.c.PATCH('/api/v1/accommodation/properties/{propertyId}', {
        params: {
          header: { ...tenant(s), 'If-Match': `"${version}"`, 'Idempotency-Key': key() },
          path: { propertyId: property.id },
        },
        body,
      }),
    );
    // Every field is sent every time, so the cost centre and the region are cleared by
    // their absence rather than kept by a merge.
    expect(patched.data.status).toBe('INACTIVE');
    expect(patched.data.regionCode).toBeNull();
    expect(patched.data.amenities).toEqual(['WIFI']);
    expect(patched.data.rowVersion).toBe(version + 1);
  });
});

// --- the hold and the booking (WP-I6-02) --------------------------------------------------

/** A night this room type has an allotment on and nothing has taken yet. */
function freeNight(roomTypeId: string, from = 60): string {
  const nights = allottedNights(roomTypeId);
  const sold = new Set(soldOutNights(roomTypeId));
  for (let i = from; i < nights.length - 4; i += 1) {
    const start = nights[i]!;
    const window = nights.slice(i, i + 3);
    if (window.length === 3 && window.every((n) => !sold.has(n))) return start;
  }
  throw new Error('fixture: no free three-night window');
}

function holdBody(personId: string, roomTypeId: string, checkIn: string, nights = 2, adults = 2) {
  return {
    personId,
    roomTypeId,
    checkIn,
    checkOut: addDays(checkIn, nights),
    adults,
  };
}

function createHold(s: Session, body: ReturnType<typeof holdBody>) {
  return unwrap(
    s.c.POST('/api/v1/accommodation/holds', {
      params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
      body,
    }),
  );
}

describe('the hold', () => {
  /**
   * The acceptance criterion of the package, as far as a mock can carry it: the room is set
   * aside on every night of the stay, the countdown is a real one, and the frozen quote adds
   * up night by night rather than only in the total.
   */
  it('sets a room aside on every night and freezes the quote', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('STD_DBL');
    const person = personByFirstName('Kaan');
    const checkIn = freeNight(room.id);
    const before = await getInventory(s, room.id, checkIn, addDays(checkIn, 1));

    const held = (await createHold(s, holdBody(person.id, room.id, checkIn))).data;
    expect(held.status).toBe('HOLD');
    expect(held.nights).toBe(2);
    expect(held.nightlyAmounts).toHaveLength(2);
    // The countdown is the server's own, not a difference two clocks computed.
    expect(held.secondsToExpiry).toBeGreaterThan(0);
    expect(held.secondsToExpiry).toBeLessThanOrEqual(15 * 60);
    // Payer plus member is exactly the amount, on every night and on the total.
    for (const night of held.nightlyAmounts) {
      expect(sum(night.payerAmount, night.memberAmount)).toBe(night.unitAmount);
    }
    expect(sum(held.quoteSnapshot.payerAmount, held.quoteSnapshot.memberAmount)).toBe(
      held.quoteSnapshot.totalAmount,
    );
    expect(sum(...held.nightlyAmounts.map((n) => n.unitAmount))).toBe(
      held.quoteSnapshot.totalAmount,
    );

    const after = await getInventory(s, room.id, checkIn, addDays(checkIn, 1));
    for (let i = 0; i < after.days.length; i += 1) {
      expect(after.days[i]!.held).toBe(before.days[i]!.held + 1);
      expect(after.days[i]!.confirmed).toBe(before.days[i]!.confirmed);
      expect(after.days[i]!.available).toBe(before.days[i]!.available - 1);
    }
  });

  /**
   * A night with no room refuses the whole stay and names itself. A member looking at a
   * fortnight needs the night to move, not a message that something somewhere failed.
   */
  it('refuses the whole stay with the first full night', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('STD_DBL');
    const person = personByFirstName('Kaan');
    const soldOut = soldOutNights(room.id)[0]!;
    // A stay that starts the day before the sold-out night: the first night is free and the
    // second is not, so the refusal has to name the second.
    const checkIn = addDays(soldOut, -1);

    const refused = await refusal(createHold(s, holdBody(person.id, room.id, checkIn)));
    expect(refused.status).toBe(409);
    expect(refused.code).toBe('ROOM_UNAVAILABLE');
    expect(refused.stayDate).toBe(soldOut);
    expect(refused.allotted).toBe(true);
  });

  /** A night the provider opened nothing on is "no allotment" and says so. */
  it('tells a night with no allotment apart from a full one', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('STD_DBL');
    const person = personByFirstName('Kaan');
    const nights = allottedNights(room.id);
    // The last allotted night, so the second night of the stay has no row at all.
    const checkIn = nights[nights.length - 1]!;

    const refused = await refusal(createHold(s, holdBody(person.id, room.id, checkIn)));
    expect(refused.code).toBe('ROOM_UNAVAILABLE');
    expect(refused.allotted).toBe(false);
  });

  /**
   * One live booking of one room type per person and arrival. The server states it as a
   * partial unique index, and the "partial" is the half a naive unique constraint gets wrong:
   * once the first is given back, the same room and the same day may be booked again.
   */
  it('allows one live booking per person, room type and arrival', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('GUEST_DBL');
    const person = personByFirstName('Kaan');
    const checkIn = freeNight(room.id);

    const first = (await createHold(s, holdBody(person.id, room.id, checkIn))).data;
    const clash = await refusal(createHold(s, holdBody(person.id, room.id, checkIn)));
    expect(clash.status).toBe(409);
    expect(clash.code).toBe('BOOKING_ALREADY_LIVE');

    await unwrap(
      s.c.POST('/api/v1/accommodation/bookings/{bookingId}/release', {
        params: { header: { ...tenant(s), 'Idempotency-Key': key() }, path: { bookingId: first.id } },
      }),
    );
    const again = (await createHold(s, holdBody(person.id, room.id, checkIn))).data;
    expect(again.status).toBe('HOLD');
    expect(again.id).not.toBe(first.id);
  });

  /**
   * Giving the room back before anything was agreed. Nothing is charged, the counters return
   * to where they were, and the reason code says which of the two cancellations this is.
   */
  it('gives the room back when the hold is released', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('FAM_SUITE');
    const person = personByFirstName('Kaan');
    const checkIn = freeNight(room.id);
    const before = await getInventory(s, room.id, checkIn, addDays(checkIn, 1));

    const held = (await createHold(s, holdBody(person.id, room.id, checkIn))).data;
    const released = (
      await unwrap(
        s.c.POST('/api/v1/accommodation/bookings/{bookingId}/release', {
          params: {
            header: { ...tenant(s), 'Idempotency-Key': key() },
            path: { bookingId: held.id },
          },
        }),
      )
    ).data;
    expect(released.status).toBe('CANCELLED');
    expect(released.cancelReasonCode).toBe('HOLD_RELEASED');
    expect(released.secondsToExpiry).toBe(0);

    const after = await getInventory(s, room.id, checkIn, addDays(checkIn, 1));
    for (let i = 0; i < after.days.length; i += 1) {
      expect(after.days[i]!.held).toBe(before.days[i]!.held);
    }
  });

  /**
   * The expiry, advanced by moving the deadline into the past. On the server this is a
   * scheduler job; either way the room comes back without anybody calling anything, and a
   * second pass gives nothing back twice.
   */
  it('expires a hold nobody confirmed and releases once', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('GUEST_SGL');
    const person = personByFirstName('Kaan');
    const checkIn = freeNight(room.id);
    const before = await getInventory(s, room.id, checkIn, addDays(checkIn, 1));

    const held = (await createHold(s, holdBody(person.id, room.id, checkIn, 2, 1))).data;
    api.world.bookings.find((b) => b.id === held.id)!.holdExpiresAt = new Date(
      Date.now() - 60_000,
    ).toISOString();

    const expired = (
      await unwrap(
        s.c.GET('/api/v1/accommodation/bookings/{bookingId}', {
          params: { header: tenant(s), path: { bookingId: held.id } },
        }),
      )
    ).data;
    expect(expired.status).toBe('EXPIRED');
    expect(expired.secondsToExpiry).toBe(0);

    const after = await getInventory(s, room.id, checkIn, addDays(checkIn, 1));
    for (let i = 0; i < after.days.length; i += 1) {
      expect(after.days[i]!.held).toBe(before.days[i]!.held);
    }
    // A second read must not give the room back again.
    await unwrap(
      s.c.GET('/api/v1/accommodation/bookings/{bookingId}', {
        params: { header: tenant(s), path: { bookingId: held.id } },
      }),
    );
    const twice = await getInventory(s, room.id, checkIn, addDays(checkIn, 1));
    for (let i = 0; i < twice.days.length; i += 1) {
      expect(twice.days[i]!.held).toBe(before.days[i]!.held);
    }
  });
});

describe('the booking', () => {
  /**
   * Confirming moves the room from held to confirmed on every night, freezes the cancellation
   * policy, and charges the amounts the member already saw. The nights are asserted one by
   * one because a confirmation that moved the counters on the first night only would still
   * look right in a total.
   */
  it('confirms the stay, moves the counters and freezes the policy', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('STD_DBL');
    const person = personByFirstName('Kaan');
    const checkIn = freeNight(room.id, 70);
    const before = await getInventory(s, room.id, checkIn, addDays(checkIn, 1));

    const held = (await createHold(s, holdBody(person.id, room.id, checkIn))).data;
    const confirmed = (
      await unwrap(
        s.c.POST('/api/v1/accommodation/bookings/{bookingId}/confirm', {
          params: {
            header: { ...tenant(s), 'Idempotency-Key': key() },
            path: { bookingId: held.id },
          },
        }),
      )
    ).data;
    expect(confirmed.status).toBe('CONFIRMED');
    expect(confirmed.confirmedAt).not.toBeNull();
    expect(confirmed.serviceRequestId).not.toBeNull();
    expect(confirmed.authorizationId).not.toBeNull();
    expect(confirmed.policySnapshot?.freeCancellationHoursBefore).toBe(48);
    expect(confirmed.secondsToExpiry).toBe(0);
    // Pricing is never recomputed: the confirmation carries the hold's own figures.
    expect(confirmed.quoteSnapshot.totalAmount).toBe(held.quoteSnapshot.totalAmount);

    const after = await getInventory(s, room.id, checkIn, addDays(checkIn, 1));
    for (let i = 0; i < after.days.length; i += 1) {
      expect(after.days[i]!.held).toBe(before.days[i]!.held);
      expect(after.days[i]!.confirmed).toBe(before.days[i]!.confirmed + 1);
    }
  });

  /**
   * A quote nobody has looked at for an hour is not a price anybody should be committed to.
   * The room stays held, so the member can search again rather than losing it.
   */
  it('refuses a stale quote and keeps the room held', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('FAM_SUITE');
    const person = personByFirstName('Kaan');
    const checkIn = freeNight(room.id, 70);

    const held = (await createHold(s, holdBody(person.id, room.id, checkIn))).data;
    const stored = api.world.bookings.find((b) => b.id === held.id)!;
    stored.quoteSnapshot.quotedAt = new Date(Date.now() - 2 * 3_600_000).toISOString();

    const refused = await refusal(
      unwrap(
        s.c.POST('/api/v1/accommodation/bookings/{bookingId}/confirm', {
          params: {
            header: { ...tenant(s), 'Idempotency-Key': key() },
            path: { bookingId: held.id },
          },
        }),
      ),
    );
    expect(refused.status).toBe(409);
    expect(refused.code).toBe('QUOTE_STALE');
    expect(stored.status).toBe('HOLD');
  });

  /**
   * The voucher token is returned once, by the command that mints it, and appears in no
   * other body. Reissuing retires the previous one, so exactly one code works at a time.
   */
  it('hands out a voucher token once and rotates it on a reissue', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('GUEST_DBL');
    const person = personByFirstName('Kaan');
    const checkIn = freeNight(room.id, 70);

    const held = (await createHold(s, holdBody(person.id, room.id, checkIn))).data;
    await unwrap(
      s.c.POST('/api/v1/accommodation/bookings/{bookingId}/confirm', {
        params: {
          header: { ...tenant(s), 'Idempotency-Key': key() },
          path: { bookingId: held.id },
        },
      }),
    );
    const first = (
      await unwrap(
        s.c.POST('/api/v1/accommodation/bookings/{bookingId}/voucher', {
          params: { header: tenant(s), path: { bookingId: held.id } },
        }),
      )
    ).data;
    expect(first.token).not.toBe('');
    expect(first.maskedToken).not.toBe(first.token);

    const second = (
      await unwrap(
        s.c.POST('/api/v1/accommodation/bookings/{bookingId}/voucher', {
          params: { header: tenant(s), path: { bookingId: held.id } },
        }),
      )
    ).data;
    expect(second.token).not.toBe(first.token);
    const live = api.world.bookingVouchers.filter(
      (v) => v.bookingId === held.id && v.status === 'ISSUED',
    );
    expect(live).toHaveLength(1);
    expect(live[0]!.id).toBe(second.id);

    // The booking itself never carries a token, on any read.
    const read = (
      await unwrap(
        s.c.GET('/api/v1/accommodation/bookings/{bookingId}', {
          params: { header: tenant(s), path: { bookingId: held.id } },
        }),
      )
    ).data;
    expect(JSON.stringify(read)).not.toContain(first.token);
    expect(JSON.stringify(read)).not.toContain(second.token);
  });

  /** A voucher may not be minted for a stay nobody has agreed to yet. */
  it('refuses a voucher on a booking that is still a hold', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('GUEST_SGL');
    const person = personByFirstName('Kaan');
    const checkIn = freeNight(room.id, 70);

    const held = (await createHold(s, holdBody(person.id, room.id, checkIn, 2, 1))).data;
    const refused = await refusal(
      unwrap(
        s.c.POST('/api/v1/accommodation/bookings/{bookingId}/voucher', {
          params: { header: tenant(s), path: { bookingId: held.id } },
        }),
      ),
    );
    expect(refused.status).toBe(409);
    expect(refused.code).toBe('BOOKING_TRANSITION_INVALID');
  });

  /**
   * Partial coverage is a booking, not a refusal. The spouse has two nights left; a
   * three-night stay holds the room, reserves the two the plan carries, and tells the screen
   * which is which. A hold that insisted on the whole stay would refuse exactly the booking
   * the search had just quoted.
   */
  it('holds a stay the plan covers only part of', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('STD_DBL');
    const spouse = personByFirstName('Sevgi');
    const checkIn = freeNight(room.id, 75);

    const held = (await createHold(s, holdBody(spouse.id, room.id, checkIn, 3))).data;
    expect(held.nights).toBe(3);
    expect(held.nightlyAmounts).toHaveLength(3);
    expect(held.quoteSnapshot.coveredNights).toBe(2);
    // `eligible` is "the plan covers every night", which this stay is not.
    expect(held.quoteSnapshot.eligible).toBe(false);
    // The two carried nights have a payer share; the third is entirely the member's.
    const carried = held.nightlyAmounts.filter((n) => toMicros(n.payerAmount) > 0n);
    expect(carried).toHaveLength(2);
    const own = held.nightlyAmounts.find((n) => toMicros(n.payerAmount) === 0n)!;
    expect(own.memberAmount).toBe(own.unitAmount);
    // And the totals still add up exactly.
    expect(sum(held.quoteSnapshot.payerAmount, held.quoteSnapshot.memberAmount)).toBe(
      held.quoteSnapshot.totalAmount,
    );
  });

  /** A stay the plan carries no night of is the one refusal left. */
  it('refuses a stay the plan carries no night of', async () => {
    const s = await signIn('admin.a');
    const room = roomTypeByCode('STD_DBL');
    const spouse = personByFirstName('Sevgi');
    const checkIn = freeNight(room.id, 75);
    for (const account of api.world.entitlementAccounts) {
      if (account.personId === spouse.id) account.available = '0.000000';
    }

    const refused = await refusal(createHold(s, holdBody(spouse.id, room.id, checkIn, 3)));
    expect(refused.status).toBe(409);
    expect(refused.code).toBe('ENTITLEMENT_INSUFFICIENT');
  });

  /** The seeded world holds the four states a screen has to draw. */
  it('seeds a hold, a confirmed stay, a check-in and a completed stay', async () => {
    const s = await signIn('admin.a');
    const page = (
      await unwrap(
        s.c.GET('/api/v1/accommodation/bookings', { params: { header: tenant(s) } }),
      )
    ).data;
    const statuses = new Set(page.items.map((b) => b.status));
    expect(statuses.has('HOLD')).toBe(true);
    expect(statuses.has('CONFIRMED')).toBe(true);
    expect(statuses.has('CHECKED_IN')).toBe(true);
    expect(statuses.has('COMPLETED')).toBe(true);
    // Every seeded booking carries as many night rows as it says it has nights.
    for (const booking of page.items) {
      expect(booking.nightlyAmounts).toHaveLength(booking.nights);
    }
    // The confirmed ones carry the frozen policy; the hold does not.
    const hold = page.items.find((b) => b.status === 'HOLD')!;
    expect(hold.policySnapshot ?? null).toBeNull();
    expect(hold.secondsToExpiry).toBeGreaterThan(0);
    const confirmed = page.items.find((b) => b.status === 'CONFIRMED')!;
    expect(confirmed.policySnapshot?.penaltyKind).toBe('NIGHTS');
  });
});
