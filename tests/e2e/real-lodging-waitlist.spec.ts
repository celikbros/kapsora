import { randomUUID } from 'node:crypto';
import { expect, request, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const root = '/api/v1/accommodation';
test.use({ trace: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || process.env['E2E_LODGING_WAITLIST'] !== '1',
  'requires opt-in and an operator-started scheduler; never changes its clock or interval',
);

function day(offset: number) {
  const today = new Intl.DateTimeFormat('en-CA', {
    timeZone: 'Europe/Istanbul',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).format(new Date());
  return new Date(new Date(today + 'T12:00:00Z').getTime() + offset * 86400000)
    .toISOString()
    .slice(0, 10);
}
function units(value: string | number) {
  const text = String(value);
  expect(text).toMatch(/^-?\d+(?:\.\d{1,6})?$/);
  const [whole, fraction = ''] = text.replace(/^-/, '').split('.');
  return (
    (text.startsWith('-') ? -1n : 1n) *
    (BigInt(whole!) * 1000000n + BigInt(fraction.padEnd(6, '0')))
  );
}
const balance = (a: S<'EntitlementAccount'>) => [
  units(a.available),
  units(a.reserved),
  units(a.consumed),
];
const counts = (days: S<'InventoryDay'>[]) =>
  days.map((d) => [d.stayDate, d.capacity, d.held, d.confirmed, d.available]);

test('real scheduler offers the selected enrollment once and leaving returns its room and nights', async () => {
  test.setTimeout(420000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const member = new Actor(await request.newContext(), 'member', true);
  const desk = new Actor(await request.newContext(), 'provider', true);
  let entryId = '';
  let closed = false;
  try {
    await member.login('member.a');
    await desk.login('reservation.a');
    const me = (await member.call<S<'MyPerson'>>('GET', '/api/v1/me/person')).data;
    const property = (
      await member.call<S<'PropertyPage'>>('GET', root + '/properties?status=ACTIVE&limit=100')
    ).data.items.find((p) => p.code === 'DEMO_OTEL');
    expect(property).toBeDefined();
    const entries = async () => {
      const list = (
        await member.call<S<'WaitlistEntryList'>>(
          'GET',
          root + `/waitlist?propertyId=${property!.id}&limit=200`,
        )
      ).data.items;
      expect(list.length, 'do not rely on a truncated queue').toBeLessThan(200);
      return list;
    };
    const current = (await entries()).filter(
      (e) => e.status === 'WAITING' || e.status === 'OFFERED',
    );
    expect(current, 'finish existing member queue entries before creating another').toEqual([]);
    const search = (
      await member.call<S<'AvailabilitySearchResult'>>('POST', root + '/availability/search', {
        propertyId: property!.id,
        checkIn: day(20),
        checkOut: day(22),
        adults: 2,
        children: 0,
      })
    ).data;
    expect(search.eligible).toBe(true);
    const room = search.results.find((r) => r.roomType.code === 'STD');
    expect(room?.quote).toBeTruthy();
    expect(room!.available).toBeGreaterThan(0);
    const accounts = async () =>
      (
        await member.call<{ items: S<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${me.person.id}/entitlements?asOf=${search.checkIn}`,
        )
      ).data.items;
    const before = (await accounts()).find(
      (a) =>
        a.definition.unitType === 'NIGHT' &&
        a.definition.code === search.entitlement?.entitlementCode,
    );
    expect(before).toBeDefined();
    const account = async () => (await accounts()).find((a) => a.id === before!.id)!;
    const inventory = async () =>
      (
        await desk.call<S<'RoomTypeInventoryRange'>>(
          'GET',
          `${root}/room-types/${room!.roomType.id}/inventory?from=${day(20)}&to=${day(21)}`,
        )
      ).data.days;
    const inventoryBefore = await inventory();
    expect(inventoryBefore).toHaveLength(2);
    const input = {
      propertyId: property!.id,
      roomTypeId: room!.roomType.id,
      checkIn: search.checkIn,
      checkOut: search.checkOut,
      adults: 2,
      children: 0,
    };
    const key = randomUUID();
    const joined = (
      await member.call<S<'WaitlistEntry'>>('POST', root + '/waitlist', input, {
        expected: 201,
        key,
      })
    ).data;
    entryId = joined.id;
    await test.info().attach('pc06-waitlist-entry', {
      contentType: 'application/json',
      body: JSON.stringify({ entryId }),
    });
    expect(joined.enrollmentId).toBe(before!.enrollmentId);
    expect(joined.priority).toBe(0);
    expect(
      (
        await member.call<S<'WaitlistEntry'>>('POST', root + '/waitlist', input, {
          expected: 201,
          key,
        })
      ).data,
    ).toEqual(joined);
    await member.call('POST', root + '/waitlist', input, { expected: 409 });
    // Observe the real five-minute scheduler. Do not call the sweep or alter production time.
    await expect
      .poll(async () => (await entries()).find((e) => e.id === entryId)?.status, {
        timeout: 360000,
        intervals: [5000],
      })
      .toBe('OFFERED');
    const offered = (await entries()).find((e) => e.id === entryId)!;
    const booking = offered.offer!;
    expect(booking).toBeTruthy();
    expect(booking.id).toBe(offered.offeredBookingId);
    expect(booking.status).toBe('HOLD');
    expect(booking.enrollmentId).toBe(joined.enrollmentId);
    expect(new Date(booking.holdExpiresAt!).getTime()).toBe(
      new Date(offered.offerExpiresAt!).getTime(),
    );
    expect(booking.quoteSnapshot.coveredNights).toBe(2);
    expect(balance(await account())).toEqual([
      units(before!.available) - 2000000n,
      units(before!.reserved) + 2000000n,
      units(before!.consumed),
    ]);
    expect(counts(await inventory())).toEqual(
      inventoryBefore.map((d) => [
        d.stayDate,
        d.capacity,
        d.held + 1,
        d.confirmed,
        d.available - 1,
      ]),
    );
    const cancelKey = randomUUID();
    const cancelled = (
      await member.call<S<'WaitlistEntry'>>(
        'POST',
        `${root}/waitlist/${entryId}/cancel`,
        undefined,
        { key: cancelKey },
      )
    ).data;
    closed = true;
    expect(cancelled.status).toBe('CANCELLED');
    expect(
      (
        await member.call<S<'WaitlistEntry'>>(
          'POST',
          `${root}/waitlist/${entryId}/cancel`,
          undefined,
          { key: cancelKey },
        )
      ).data,
    ).toEqual(cancelled);
    await member.call('POST', `${root}/waitlist/${entryId}/cancel`, undefined, { expected: 409 });
    expect(balance(await account())).toEqual(balance(before!));
    expect(counts(await inventory())).toEqual(counts(inventoryBefore));
    expect(
      (await member.call<S<'Booking'>>('GET', `${root}/bookings/${booking.id}`)).data.status,
    ).toBe('CANCELLED');
    const ledger = (
      await member.call<S<'LedgerPage'>>(
        'GET',
        `/api/v1/entitlement-accounts/${before!.id}/ledger?limit=100`,
      )
    ).data;
    expect(ledger.nextCursor).toBeFalsy();
    const moves = ledger.items.filter((e) => e.reservationId === booking.entitlementReservationId);
    expect(moves.map((e) => e.movementType).sort()).toEqual(['RELEASE', 'RESERVE']);
    expect(moves.reduce((sum, e) => sum + units(e.deltaConsumed), 0n)).toBe(0n);
    await test.info().attach('pc06-waitlist-release', {
      contentType: 'application/json',
      body: JSON.stringify({ entryId, bookingId: booking.id, accountId: before!.id }),
    });
  } finally {
    // Only release this run's queue/hold; never cancel unrelated member entries.
    if (entryId && !closed) await member.call('POST', `${root}/waitlist/${entryId}/cancel`);
    await desk.close();
    await member.close();
  }
});
