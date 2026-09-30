import { randomUUID } from 'node:crypto';
import { expect, request, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const resume = process.env['E2E_LODGING_PENALTY_BOOKING'] ?? '';
const root = '/api/v1/accommodation';
test.use({ trace: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base || process.env['E2E_LODGING_PENALTY'] !== '1',
  'requires explicit opt-in for a synthetic cancellation fee and operator-started system',
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

test('penalized cancellation consumes one covered night and the worker raises one exact fee claim', async ({
  browser,
}) => {
  test.setTimeout(180000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const member = new Actor(page.request, 'member', true);
  const doctor = new Actor(await request.newContext(), 'backoffice', true);
  const finance = new Actor(await request.newContext(), 'backoffice', true);
  let bookingId = resume;
  try {
    await member.login('member.a');
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
          `${root}/bookings?propertyId=${property!.id}&checkInFrom=${day(1)}&checkInTo=${day(1)}&limit=100`,
        )
      ).data;
      expect(
        prior.nextCursor,
        'inspect all existing reservations before creating another',
      ).toBeFalsy();
      expect(
        prior.items,
        'an existing arrival requires its explicit E2E_LODGING_PENALTY_BOOKING resume ID',
      ).toEqual([]);
      const search = (
        await member.call<S<'AvailabilitySearchResult'>>('POST', root + '/availability/search', {
          propertyId: property!.id,
          checkIn: day(1),
          checkOut: day(4),
          adults: 2,
          children: 0,
        })
      ).data;
      expect(search.eligible).toBe(true);
      const room = search.results.find((r) => r.roomType.code === 'STD');
      expect(room?.quote).toBeTruthy();
      expect(micros(room!.quote!.memberAmount)).toBe(0n);
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
      await test.info().attach('pc06-penalty-held', {
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
    expect(['CONFIRMED', 'CANCELLED']).toContain(booking.status);
    const penalty = booking.nightlyAmounts[0]!;
    let cancelled: S<'CancellationResult'> | undefined;
    if (booking.status === 'CONFIRMED') {
      const preview = (
        await member.call<S<'CancellationPreview'>>(
          'POST',
          `${root}/bookings/${bookingId}/cancellation-preview`,
        )
      ).data;
      expect(preview.quote.free).toBe(false);
      expect(preview.quote.penaltyNights).toBe(1);
      expect(preview.quote.releasedNights).toBe(2);
      expect(micros(preview.quote.feeAmount)).toBe(micros(penalty.unitAmount));
      expect(micros(preview.quote.payerFee)).toBe(micros(penalty.payerAmount));
      expect(micros(preview.quote.memberFee)).toBe(0n);
      await page.goto(base + `/uye/bookings/${bookingId}`);
      await page.getByRole('button', { name: 'İptal edersem ne öderim?', exact: true }).click();
      await expect(page.getByTestId('cancellation-preview')).toBeVisible();
      const response = page.waitForResponse(
        (r) => new URL(r.url()).pathname === `${root}/bookings/${bookingId}/cancel`,
      );
      await page.getByRole('button', { name: 'İptali onayla', exact: true }).click();
      const result = await response;
      expect(result.status()).toBe(200);
      cancelled = (await result.json()) as S<'CancellationResult'>;
      expect(cancelled.quote).toEqual(preview.quote);
      const key = result.request().headers()['idempotency-key'];
      expect(key).toBeTruthy();
      const body = result.request().postDataJSON() as unknown;
      const replay = await member.call<S<'CancellationResult'>>(
        'POST',
        `${root}/bookings/${bookingId}/cancel`,
        body,
        { key },
      );
      expect(replay.data).toEqual(cancelled);
    }
    expect((await getBooking()).status).toBe('CANCELLED');
    const accounts = (
      await member.call<{ items: S<'EntitlementAccount'>[] }>(
        'GET',
        `/api/v1/people/${me.person.id}/entitlements?asOf=${booking.checkIn}`,
      )
    ).data.items;
    const account = accounts.find(
      (a) => a.enrollmentId === booking.enrollmentId && a.definition.unitType === 'NIGHT',
    );
    expect(account).toBeDefined();
    const ledger = (
      await member.call<S<'LedgerPage'>>(
        'GET',
        `/api/v1/entitlement-accounts/${account!.id}/ledger?limit=100`,
      )
    ).data;
    expect(ledger.nextCursor, 'ledger proof must not omit a later page').toBeFalsy();
    const movements = ledger.items.filter(
      (e) => e.reservationId === booking.entitlementReservationId,
    );
    expect(movements.map((e) => e.movementType).sort()).toEqual(['CONSUME', 'RELEASE', 'RESERVE']);
    expect(movements.reduce((sum, e) => sum + micros(e.deltaConsumed), 0n)).toBe(1000000n);
    expect(movements.reduce((sum, e) => sum + micros(e.deltaReserved), 0n)).toBe(0n);
    expect(movements.reduce((sum, e) => sum + micros(e.deltaAvailable), 0n)).toBe(-1000000n);
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
    const detail = (await finance.call<S<'Claim'>>('GET', `/api/v1/claims/${claim.id}`)).data;
    expect(detail.status).toBe('APPROVED');
    expect(detail.lines).toHaveLength(1);
    expect(detail.lines[0]!.unitType).toBe('CANCELLATION_FEE');
    expect(micros(detail.lines[0]!.lineAmount)).toBe(micros(penalty.unitAmount));
    const readiness = (
      await finance.call<S<'ClaimInvoiceReadiness'>>(
        'GET',
        `/api/v1/claims/${claim.id}/invoice-readiness`,
      )
    ).data;
    expect(readiness.ready).toBe(true);
    expect(micros(readiness.payerTotal)).toBe(micros(penalty.payerAmount));
    expect(micros(readiness.memberTotal)).toBe(0n);
    await member.call(
      'POST',
      `${root}/bookings/${bookingId}/cancel`,
      {},
      { expected: 409, key: randomUUID() },
    );
    expect((await claims()).map((c) => c.id)).toEqual([claim.id]);
    await test.info().attach('pc06-penalty-claim', {
      contentType: 'application/json',
      body: JSON.stringify({
        bookingId,
        claimId: claim.id,
        accountId: account!.id,
        status: 'CANCELLED',
        invoiceReady: true,
      }),
    });
  } finally {
    // Preserve the known booking and fee for explicit resume; never create an offsetting payment.
    if (bookingId)
      await test.info().attach('pc06-penalty-resume', {
        contentType: 'application/json',
        body: JSON.stringify({ bookingId }),
      });
    await finance.close();
    await doctor.close();
    await member.close();
    await page.close();
  }
});
