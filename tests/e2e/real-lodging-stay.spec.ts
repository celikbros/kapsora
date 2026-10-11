import { mkdir } from 'node:fs/promises';
import { expect, request, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const root = '/api/v1/accommodation';
const resume = process.env['E2E_LODGING_STAY_BOOKING'] ?? '';
test.use({ trace: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || process.env['E2E_LODGING_STAY'] !== '1',
  'requires explicit opt-in for a synthetic completed stay and operator-started system',
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
function micros(value: string | number) {
  const text = String(value);
  expect(text).toMatch(/^-?\d+(?:\.\d{1,6})?$/);
  const [whole, fraction = ''] = text.replace(/^-/, '').split('.');
  return (
    (text.startsWith('-') ? -1n : 1n) *
    (BigInt(whole!) * 1000000n + BigInt(fraction.padEnd(6, '0')))
  );
}

// A synthetic same-day departure uses the product's one-night minimum. It does not
// alter server time or invent a future checkout timestamp. The unused two nights return.
test('hotel desk checks in by voucher and checks out once, releasing unused nights and raising one stay claim', async ({
  browser,
}) => {
  test.setTimeout(180000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const deskPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const memberPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const desk = new Actor(deskPage.request, 'provider', true);
  const member = new Actor(memberPage.request, 'member', true);
  const doctor = new Actor(await request.newContext(), 'backoffice', true);
  const finance = new Actor(await request.newContext(), 'backoffice', true);
  let bookingId = resume;
  let inventoryBefore: S<'InventoryDay'>[] | undefined;
  try {
    await member.login('member.a');
    await desk.login('reservation.a');
    await doctor.login('doctor.a');
    await finance.login('financial.reviewer');
    const me = (await member.call<S<'MyPerson'>>('GET', '/api/v1/me/person')).data;
    const properties = (
      await member.call<S<'PropertyPage'>>('GET', root + '/properties?status=ACTIVE&limit=100')
    ).data;
    const property = properties.items.find((p) => p.code === 'DEMO_OTEL');
    expect(property).toBeDefined();
    if (!bookingId) {
      const prior = (
        await member.call<S<'BookingList'>>(
          'GET',
          `${root}/bookings?propertyId=${property!.id}&checkInFrom=${day(0)}&checkInTo=${day(0)}&limit=100`,
        )
      ).data;
      expect(prior.nextCursor).toBeFalsy();
      expect(
        prior.items,
        'an existing arrival requires E2E_LODGING_STAY_BOOKING; do not create a duplicate stay',
      ).toEqual([]);
      const search = (
        await member.call<S<'AvailabilitySearchResult'>>('POST', root + '/availability/search', {
          propertyId: property!.id,
          checkIn: day(0),
          checkOut: day(3),
          adults: 2,
          children: 0,
        })
      ).data;
      expect(search.eligible).toBe(true);
      const room = search.results.find((r) => r.roomType.code === 'STD');
      expect(room?.quote).toBeTruthy();
      expect(micros(room!.quote!.memberAmount)).toBe(0n);
      inventoryBefore = (
        await desk.call<S<'RoomTypeInventoryRange'>>(
          'GET',
          `${root}/room-types/${room!.roomType.id}/inventory?from=${day(0)}&to=${day(2)}`,
        )
      ).data.days;
      expect(inventoryBefore).toHaveLength(3);
      const held = (
        await member.call<S<'Booking'>>(
          'POST',
          root + '/holds',
          {
            roomTypeId: room!.roomType.id,
            checkIn: search.checkIn,
            checkOut: search.checkOut,
            adults: 2,
            children: 0,
            channel: 'MEMBER_PORTAL',
          },
          { expected: 201 },
        )
      ).data;
      bookingId = held.id;
      await test.info().attach('pc06-stay-held', {
        contentType: 'application/json',
        body: JSON.stringify({ bookingId }),
      });
    }
    const getBooking = async () =>
      (await member.call<S<'Booking'>>('GET', `${root}/bookings/${bookingId}`)).data;
    let booking = await getBooking();
    expect(booking.personId).toBe(me.person.id);
    expect(booking.propertyId).toBe(property!.id);
    expect(booking.nights).toBe(3);
    expect(booking.quoteSnapshot.coveredNights).toBe(3);
    if (booking.status === 'HOLD') {
      await member.call('POST', `${root}/bookings/${bookingId}/confirm`);
      booking = await getBooking();
    }
    if (booking.status === 'PENDING_APPROVAL') {
      const source = await doctor.call<S<'ServiceRequest'>>(
        'GET',
        `/api/v1/service-requests/${booking.serviceRequestId}`,
      );
      if (source.data.status === 'PENDING_REVIEW')
        await doctor.call(
          'POST',
          `/api/v1/service-requests/${source.data.id}/approve`,
          { reasonCode: 'PC06_TEST_APPROVAL' },
          { etag: source.etag },
        );
      else expect(source.data.status).toBe('APPROVED');
      await expect
        .poll(async () => (await getBooking()).status, { timeout: 45000 })
        .toBe('CONFIRMED');
      booking = await getBooking();
    }
    expect(['CONFIRMED', 'CHECKED_IN', 'COMPLETED']).toContain(booking.status);
    const accountList = async () =>
      (
        await member.call<{ items: S<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${me.person.id}/entitlements?asOf=${booking.checkIn}`,
        )
      ).data.items;
    const account = (await accountList()).find(
      (a) => a.enrollmentId === booking.enrollmentId && a.definition.unitType === 'NIGHT',
    );
    expect(account).toBeDefined();
    const accountNow = async () => (await accountList()).find((a) => a.id === account!.id)!;
    await deskPage.goto(base + '/portal/lodging/desk');
    if (booking.status === 'CONFIRMED') {
      expect(booking.checkIn, 'resume arrivals only inside their real check-in window').toBe(
        day(0),
      );
      const beforeCheckIn = await accountNow();
      const row = deskPage.getByTestId('arrival-row').filter({ hasText: booking.reference });
      await expect(row).toBeVisible();
      // A token is kept in memory only. Traces are disabled and no screenshot contains it.
      const issued = (
        await member.call<S<'BookingVoucher'>>(
          'POST',
          `${root}/bookings/${bookingId}/voucher`,
          undefined,
          { expected: 201 },
        )
      ).data;
      await desk.call(
        'POST',
        `${root}/bookings/${bookingId}/check-in`,
        { token: 'KPS-PC06-INVALID' },
        { expected: 404 },
      );
      expect(await accountNow()).toEqual(beforeCheckIn);
      const response = deskPage.waitForResponse(
        (r) => new URL(r.url()).pathname === `${root}/bookings/${bookingId}/check-in`,
      );
      await row.getByLabel(/Kupon kodu/).fill(issued.token);
      await row.getByRole('button', { name: 'Giriş yap', exact: true }).click();
      expect((await response).status()).toBe(200);
      booking = await getBooking();
      expect(booking.status).toBe('CHECKED_IN');
      expect(await accountNow()).toEqual(beforeCheckIn);
      await desk.call(
        'POST',
        `${root}/bookings/${bookingId}/check-in`,
        { token: issued.token },
        { expected: 409 },
      );
      expect(await accountNow()).toEqual(beforeCheckIn);
    }
    if (booking.status === 'CHECKED_IN') {
      expect(
        booking.checkIn,
        'this acceptance measures the same-day minimum, not a later stay',
      ).toBe(day(0));
      await deskPage.reload();
      const departure = deskPage
        .getByTestId('departure-row')
        .filter({ hasText: booking.reference });
      await expect(departure).toBeVisible();
      const response = deskPage.waitForResponse(
        (r) => new URL(r.url()).pathname === `${root}/bookings/${bookingId}/check-out`,
      );
      await departure.getByRole('button', { name: 'Çıkış yap', exact: true }).click();
      const result = await response;
      expect(result.status()).toBe(200);
      const completed = (await result.json()) as S<'Booking'>;
      const key = result.request().headers()['idempotency-key'];
      expect(key).toBeTruthy();
      const replay = await desk.call<S<'Booking'>>(
        'POST',
        `${root}/bookings/${bookingId}/check-out`,
        result.request().postDataJSON(),
        { key },
      );
      expect(replay.data).toEqual(completed);
    }
    booking = await getBooking();
    expect(booking.status).toBe('COMPLETED');
    expect(booking.actualNights).toBe(1);
    expect.soft(booking.overBooking).toBe(false);
    const after = await accountNow();
    await desk.call('POST', `${root}/bookings/${bookingId}/check-out`, {}, { expected: 409 });
    expect(await accountNow()).toEqual(after);
    const ledger = (
      await member.call<S<'LedgerPage'>>(
        'GET',
        `/api/v1/entitlement-accounts/${account!.id}/ledger?limit=100`,
      )
    ).data;
    expect(ledger.nextCursor).toBeFalsy();
    const movements = ledger.items.filter(
      (e) => e.reservationId === booking.entitlementReservationId,
    );
    expect(movements.map((e) => e.movementType).sort()).toEqual(['CONSUME', 'RELEASE', 'RESERVE']);
    expect(movements.reduce((sum, e) => sum + micros(e.deltaConsumed), 0n)).toBe(1000000n);
    expect(movements.reduce((sum, e) => sum + micros(e.deltaReserved), 0n)).toBe(0n);
    expect(movements.reduce((sum, e) => sum + micros(e.deltaAvailable), 0n)).toBe(-1000000n);
    if (inventoryBefore) {
      const inventoryAfter = (
        await desk.call<S<'RoomTypeInventoryRange'>>(
          'GET',
          `${root}/room-types/${booking.roomTypeId}/inventory?from=${day(0)}&to=${day(2)}`,
        )
      ).data.days;
      expect(
        inventoryAfter.map((d) => [d.stayDate, d.capacity, d.held, d.confirmed, d.available]),
      ).toEqual(
        inventoryBefore.map((d, i) => [
          d.stayDate,
          d.capacity,
          d.held,
          d.confirmed + (i === 0 ? 1 : 0),
          d.available - (i === 0 ? 1 : 0),
        ]),
      );
    }
    const claims = async () => {
      const page = (
        await finance.call<S<'ClaimPage'>>(
          'GET',
          `/api/v1/claims?personId=${me.person.id}&providerOrganizationId=${property!.providerOrganizationId}&serviceDateFrom=${booking.checkIn}&serviceDateTo=${booking.checkIn}&limit=100`,
        )
      ).data;
      expect(page.nextCursor).toBeFalsy();
      return page.items.filter((c) => c.sourceType === 'BOOKING' && c.sourceId === bookingId);
    };
    await expect.poll(async () => (await claims()).length, { timeout: 45000 }).toBe(1);
    const claim = (await claims())[0]!;
    const claimPath = `/api/v1/claims/${claim.id}`;
    let reviewed = await finance.call<S<'Claim'>>('GET', claimPath);
    // The worker records the frozen line decision, but a financial reviewer still
    // closes every lodging claim. Resume a decided claim without another decision.
    if (reviewed.data.status === 'PENDING_FINANCIAL') {
      expect(reviewed.data.lines).toHaveLength(1);
      const decision = reviewed.data.lines[0]!.decision;
      expect(decision?.decision).toBe('APPROVED');
      expect(micros(decision!.payerAmount)).toBe(micros(booking.nightlyAmounts[0]!.payerAmount));
      expect(micros(decision!.memberAmount)).toBe(0n);
      const notReady = await finance.call<S<'Problem'>>(
        'GET',
        claimPath + '/invoice-readiness',
        undefined,
        { expected: 409 },
      );
      expect(notReady.data.code).toBe('CLAIM_NOT_DECIDED');
      reviewed = await finance.call<S<'Claim'>>(
        'POST',
        claimPath + '/approve',
        { reasonCode: 'PC06_LODGING_REVIEW' },
        { etag: reviewed.etag },
      );
    }
    const detail = reviewed.data;
    expect(detail.status).toBe('APPROVED');
    expect(detail.lines).toHaveLength(1);
    expect(detail.lines[0]!.unitType).toBe('NIGHT');
    expect(micros(detail.lines[0]!.lineAmount)).toBe(micros(booking.nightlyAmounts[0]!.unitAmount));
    const readiness = (
      await finance.call<S<'ClaimInvoiceReadiness'>>(
        'GET',
        `/api/v1/claims/${claim.id}/invoice-readiness`,
      )
    ).data;
    expect(readiness.ready).toBe(true);
    expect(micros(readiness.payerTotal)).toBe(micros(booking.nightlyAmounts[0]!.payerAmount));
    expect(micros(readiness.memberTotal)).toBe(0n);
    expect(await accountNow()).toEqual(after);
    await memberPage.goto(base + `/uye/bookings/${bookingId}`);
    await expect(memberPage.getByTestId('booking-status')).toHaveText('Tamamlandı');
    await mkdir('.impeccable/review/lodging', { recursive: true });
    for (const width of [390, 1440]) {
      await memberPage.setViewportSize({ width, height: 900 });
      await expect
        .poll(() => memberPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
        .toBe(true);
      await memberPage.screenshot({
        path: `.impeccable/review/lodging/completed-${width}.png`,
        fullPage: true,
      });
    }
    await test.info().attach('pc06-stay-claim', {
      contentType: 'application/json',
      body: JSON.stringify({
        bookingId,
        claimId: claim.id,
        accountId: account!.id,
        status: 'COMPLETED',
        invoiceReady: true,
      }),
    });
  } finally {
    // Keep the stay and its consumption auditable; resume this ID after any failure.
    if (bookingId)
      await test.info().attach('pc06-stay-resume', {
        contentType: 'application/json',
        body: JSON.stringify({ bookingId }),
      });
    await finance.close();
    await doctor.close();
    await member.close();
    await desk.close();
    await memberPage.close();
    await deskPage.close();
  }
});
