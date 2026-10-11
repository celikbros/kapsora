import { expect, request, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const bookingId = process.env['E2E_LODGING_STAY_BOOKING'] ?? '';
const root = '/api/v1/accommodation';
test.use({ trace: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || !bookingId,
  'requires explicit completed stay and operator-started system',
);
const shifted = (day: string, nights: number) =>
  new Date(new Date(day + 'T12:00:00Z').getTime() + nights * 86400000).toISOString().slice(0, 10);

test('completed early departure occupies its used night only and inventory matches every hotel booking', async () => {
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const desk = new Actor(await request.newContext(), 'provider', true);
  try {
    await desk.login('reservation.a');
    const source = (await desk.call<S<'Booking'>>('GET', `${root}/bookings/${bookingId}`)).data;
    expect(source.status).toBe('COMPLETED');
    expect(source.nights).toBe(3);
    expect(source.actualNights).toBe(1);
    const bookings: S<'Booking'>[] = [];
    const seen = new Set<string>();
    let cursor = '';
    do {
      const suffix = cursor ? `&cursor=${encodeURIComponent(cursor)}` : '';
      const page = (
        await desk.call<S<'BookingList'>>(
          'GET',
          `${root}/bookings?propertyId=${source.propertyId}&limit=100${suffix}`,
        )
      ).data;
      for (const row of page.items) {
        expect(seen.has(row.id)).toBe(false);
        seen.add(row.id);
        bookings.push(row);
      }
      cursor = page.nextCursor ?? '';
      expect(bookings.length, 'never silently truncate the hotel inventory proof').toBeLessThan(
        10000,
      );
    } while (cursor);
    expect(bookings.some((b) => b.id === source.id)).toBe(true);
    const roomBookings = bookings.filter((b) => b.roomTypeId === source.roomTypeId);
    const inventory = (
      await desk.call<S<'RoomTypeInventoryRange'>>(
        'GET',
        `${root}/room-types/${source.roomTypeId}/inventory?from=${source.checkIn}&to=${shifted(source.checkOut, -1)}`,
      )
    ).data.days;
    expect(inventory).toHaveLength(3);
    const sourceOccupancy: number[] = [];
    for (const day of inventory) {
      let held = 0,
        confirmed = 0,
        sourceCount = 0;
      for (const booking of roomBookings) {
        if (day.stayDate < booking.checkIn || day.stayDate >= booking.checkOut) continue;
        if (['HOLD', 'PENDING_APPROVAL'].includes(booking.status)) {
          held++;
          continue;
        }
        let occupied = ['CONFIRMED', 'CHECKED_IN'].includes(booking.status);
        if (booking.status === 'COMPLETED') {
          expect(booking.actualNights).not.toBeNull();
          expect(booking.actualNights).toBeDefined();
          occupied =
            day.stayDate <
            shifted(booking.checkIn, Math.min(booking.actualNights!, booking.nights));
        }
        if (occupied) {
          confirmed++;
          if (booking.id === source.id) sourceCount++;
        }
      }
      expect(day.held, `held on ${day.stayDate}`).toBe(held);
      expect(day.confirmed, `confirmed on ${day.stayDate}`).toBe(confirmed);
      expect(day.available).toBe(day.capacity - held - confirmed);
      sourceOccupancy.push(sourceCount);
    }
    expect(sourceOccupancy).toEqual([1, 0, 0]);
    await test.info().attach('pc06-completed-stay-inventory', {
      contentType: 'application/json',
      body: JSON.stringify({
        bookingId,
        checkedDays: inventory.length,
        roomBookings: roomBookings.length,
        sourceOccupancy,
      }),
    });
  } finally {
    await desk.close();
  }
});
