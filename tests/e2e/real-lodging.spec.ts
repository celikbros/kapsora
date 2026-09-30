import { mkdir } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { expect, request, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const root = '/api/v1/accommodation';
test.use({ trace: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' || !base,
  'requires the operator-started local system',
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
  const [whole, fraction = ''] = text.replace(/^-/, '').split('.');
  return (
    (text.startsWith('-') ? -1n : 1n) *
    (BigInt(whole!) * 1000000n + BigInt(fraction.padEnd(6, '0')))
  );
}

for (const nights of [3, 11]) {
  test(`real lodging ${nights} nights: search, hold, reviewed confirmation, voucher and free cancellation conserve nights`, async ({
    browser,
  }) => {
    test.setTimeout(180000);
    expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
    const page = await browser.newPage({
      locale: 'tr-TR',
      timezoneId: 'Europe/Istanbul',
      viewport: { width: 390, height: 844 },
    });
    const member = new Actor(page.request, 'member', true);
    const admin = new Actor(await request.newContext(), 'backoffice', true);
    const doctor = new Actor(await request.newContext(), 'backoffice', true);
    let bookingId = '';
    try {
      await member.login('member.a');
      await admin.login('admin.a');
      await doctor.login('doctor.a');
      const me = (await member.call<S<'MyPerson'>>('GET', '/api/v1/me/person')).data;
      const properties = (
        await member.call<S<'PropertyPage'>>('GET', root + '/properties?status=ACTIVE&limit=100')
      ).data;
      const property = properties.items.find((p) => p.code === 'DEMO_OTEL');
      expect(property, 'seeded demo hotel must exist').toBeDefined();
      const checkIn = day(30),
        checkOut = day(30 + nights);
      const accounts = async () =>
        (
          await admin.call<{ items: S<'EntitlementAccount'>[] }>(
            'GET',
            `/api/v1/people/${me.person.id}/entitlements?asOf=${checkIn}`,
          )
        ).data.items;
      const before = await accounts();
      const input = { propertyId: property!.id, checkIn, checkOut, adults: 2, children: 0 };
      const search = (
        await member.call<S<'AvailabilitySearchResult'>>(
          'POST',
          root + '/availability/search',
          input,
        )
      ).data;
      expect(search.personId).toBe(me.person.id);
      expect(search.nights).toBe(nights);
      expect(search.eligible).toBe(nights === 3);
      const room = search.results.find((r) => r.roomType.code === 'STD');
      expect(room?.quote, 'published lodging terms must price the complete stay').toBeTruthy();
      expect(room!.available).toBeGreaterThan(0);
      if (nights === 3) expect(units(room!.quote!.memberAmount)).toBe(0n);
      else expect(units(room!.quote!.memberAmount)).toBeGreaterThan(0n);
      expect(units(room!.quote!.payerAmount) + units(room!.quote!.memberAmount)).toBe(
        units(room!.quote!.totalAmount),
      );
      expect(await accounts()).toEqual(before);
      const account = before.find(
        (a) =>
          a.definition.code === search.entitlement?.entitlementCode &&
          a.definition.unitType === 'NIGHT',
      );
      expect(account).toBeDefined();
      const covered = Math.min(nights, Number(account!.available));
      expect(covered).toBeGreaterThan(0);
      const reservedUnits = BigInt(covered) * 1000000n;
      const balance = async () => (await accounts()).find((a) => a.id === account!.id)!;
      const amounts = (a: S<'EntitlementAccount'>) => [
        units(a.available),
        units(a.reserved),
        units(a.consumed),
      ];
      const inventory = async () =>
        (
          await member.call<S<'RoomTypeInventoryRange'>>(
            'GET',
            `${root}/room-types/${room!.roomType.id}/inventory?from=${checkIn}&to=${day(29 + nights)}`,
          )
        ).data.days;
      const initialInventory = await inventory();
      expect(initialInventory).toHaveLength(nights);
      const inventoryAmounts = (days: S<'InventoryDay'>[]) =>
        days.map((d) => [d.stayDate, d.capacity, d.held, d.confirmed, d.available]);
      const foreign = await member.call<{ code: string }>(
        'POST',
        root + '/availability/search',
        { ...input, personId: randomUUID() },
        { expected: 403 },
      );
      expect(foreign.data.code).toBe('PERSON_SCOPE');

      await page.goto(base + '/uye/search');
      await page.getByLabel(/^Giriş/).fill(checkIn);
      await page.getByLabel(/^Çıkış/).fill(checkOut);
      await page.getByLabel(/^Nerede/).selectOption('property:' + property!.id);
      await page.getByRole('button', { name: 'Ara', exact: true }).click();
      const row = page.getByTestId('room-row').filter({ hasText: room!.roomType.name });
      await expect(row).toBeVisible();
      const displayed = (await row.getByTestId('room-member-amount').textContent())!.trim();
      await row.getByRole('button', { name: 'Seç', exact: true }).click();
      await expect(page.getByTestId('receipt-member-amount')).toHaveText(displayed);
      const heldResponse = page.waitForResponse(
        (r) => new URL(r.url()).pathname === root + '/holds' && r.request().method() === 'POST',
      );
      await page.getByRole('button', { name: 'Odayı tut', exact: true }).click();
      const response = await heldResponse;
      expect(response.status()).toBe(201);
      const held = (await response.json()) as S<'Booking'>;
      bookingId = held.id;
      await test.info().attach('pc06-booking', {
        contentType: 'application/json',
        body: JSON.stringify({ bookingId }),
      });
      expect(held.status).toBe('HOLD');
      expect(held.personId).toBe(me.person.id);
      expect(held.quoteSnapshot.coveredNights).toBe(covered);
      expect(held.entitlementReservationId).toBeTruthy();
      expect(amounts(await balance())).toEqual([
        units(account!.available) - reservedUnits,
        units(account!.reserved) + reservedUnits,
        units(account!.consumed),
      ]);
      expect(inventoryAmounts(await inventory())).toEqual(
        initialInventory.map((d) => [
          d.stayDate,
          d.capacity,
          d.held + 1,
          d.confirmed,
          d.available - 1,
        ]),
      );
      const heldAccount = await balance();
      await expect(page.getByTestId('booking-status')).toHaveText('Tutuldu');
      await expect(page.getByTestId('receipt-member-amount')).toHaveText(displayed);
      const confirmation = page.waitForResponse(
        (r) => new URL(r.url()).pathname === `${root}/bookings/${bookingId}/confirm`,
      );
      await page.getByTestId('confirm-button').click();
      const confirmationResponse = await confirmation;
      if (confirmationResponse.status() === 403) {
        expect((await confirmationResponse.json()).code).toBe('STEP_UP_REQUIRED');
        const dialog = page.getByRole('dialog', { name: 'Parolanızı doğrulayın' });
        await expect(dialog).toBeVisible();
        await dialog
          .getByLabel(/^Parola/)
          .fill(process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora');
        await dialog.getByRole('button', { name: 'Doğrula', exact: true }).click();
        await expect(dialog).toBeHidden();
      } else expect(confirmationResponse.status()).toBe(200);
      const getBooking = async () =>
        (await member.call<S<'Booking'>>('GET', `${root}/bookings/${bookingId}`)).data;
      await expect.poll(async () => (await getBooking()).serviceRequestId).toBeTruthy();
      const pending = await getBooking();
      const source = await doctor.call<S<'ServiceRequest'>>(
        'GET',
        `/api/v1/service-requests/${pending.serviceRequestId}`,
      );
      expect(source.data.status).toBe('PENDING_REVIEW');
      expect(pending.status).toBe('PENDING_APPROVAL');
      await doctor.call(
        'POST',
        `/api/v1/service-requests/${source.data.id}/approve`,
        { reasonCode: 'PC06_TEST_APPROVAL' },
        { etag: source.etag },
      );
      await expect
        .poll(async () => (await getBooking()).status, { timeout: 45000 })
        .toBe('CONFIRMED');
      const confirmed = await getBooking();
      expect(confirmed.entitlementReservationId).toBe(held.entitlementReservationId);
      expect(confirmed.authorizationId).toBeTruthy();
      expect(confirmed.policySnapshot).toBeTruthy();
      expect(await balance()).toEqual(heldAccount);
      expect(inventoryAmounts(await inventory())).toEqual(
        initialInventory.map((d) => [
          d.stayDate,
          d.capacity,
          d.held,
          d.confirmed + 1,
          d.available - 1,
        ]),
      );
      await expect(page.getByTestId('booking-status')).toHaveText('Onaylandı');
      await expect(page.getByTestId('receipt-member-amount')).toHaveText(displayed);
      await page.getByRole('button', { name: 'Kuponu göster', exact: true }).click();
      await expect(page.getByTestId('voucher-token')).toBeVisible();
      expect(await page.evaluate(() => localStorage.length + sessionStorage.length)).toBe(0);
      // Hide the usable token before any screenshots; traces are also disabled.
      await page.getByRole('button', { name: 'Kodu gizle', exact: true }).click();
      await mkdir('.impeccable/review/lodging', { recursive: true });
      for (const width of [390, 1440]) {
        await page.setViewportSize({ width, height: 900 });
        await expect
          .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
          .toBe(true);
        await page.screenshot({
          path: `.impeccable/review/lodging/confirmed-${nights}-${width}.png`,
          fullPage: true,
        });
      }
      const preview = (
        await member.call<S<'CancellationPreview'>>(
          'POST',
          `${root}/bookings/${bookingId}/cancellation-preview`,
        )
      ).data;
      expect(preview.quote.free).toBe(true);
      expect(units(preview.quote.feeAmount)).toBe(0n);
      expect(preview.quote.releasedNights).toBe(covered);
      await page.getByRole('button', { name: 'İptal edersem ne öderim?', exact: true }).click();
      await expect(page.getByTestId('cancellation-preview')).toBeVisible();
      await page.getByRole('button', { name: 'İptali onayla', exact: true }).click();
      await expect.poll(async () => (await getBooking()).status).toBe('CANCELLED');
      expect(amounts(await balance())).toEqual(amounts(account!));
      expect(inventoryAmounts(await inventory())).toEqual(inventoryAmounts(initialInventory));
      const ledger = (
        await admin.call<S<'LedgerPage'>>(
          'GET',
          `/api/v1/entitlement-accounts/${account!.id}/ledger?limit=100`,
        )
      ).data;
      const movements = ledger.items.filter(
        (row) => row.reservationId === held.entitlementReservationId,
      );
      expect(movements.map((row) => row.movementType).sort()).toEqual(['RELEASE', 'RESERVE']);
      expect(movements.reduce((sum, row) => sum + units(row.deltaAvailable), 0n)).toBe(0n);
      expect(movements.reduce((sum, row) => sum + units(row.deltaReserved), 0n)).toBe(0n);
      expect(movements.every((row) => row.deltaConsumed === 0)).toBe(true);
      await test.info().attach('pc06-free-cancel-evidence', {
        contentType: 'application/json',
        body: JSON.stringify({
          bookingId,
          ledgerRows: ledger.items.length,
          status: 'CANCELLED',
          reservationId: held.entitlementReservationId,
        }),
      });
    } finally {
      // Release only this run's unfinished hold; never manufacture a compensating financial operation.
      if (bookingId) {
        const current = (await member.call<S<'Booking'>>('GET', `${root}/bookings/${bookingId}`))
          .data;
        if (current.status === 'HOLD' || current.status === 'PENDING_APPROVAL')
          await member.call('POST', `${root}/bookings/${bookingId}/release`);
        if (current.status === 'CONFIRMED') {
          const preview = (
            await member.call<S<'CancellationPreview'>>(
              'POST',
              `${root}/bookings/${bookingId}/cancellation-preview`,
            )
          ).data;
          if (preview.quote.free) await member.call('POST', `${root}/bookings/${bookingId}/cancel`);
        }
      }
      await doctor.close();
      await admin.close();
      await member.close();
      await page.close();
    }
  });
}

test('abandoning a partly covered live hold returns only its reserved entitlement', async () => {
  test.setTimeout(60000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const member = new Actor(await request.newContext(), 'member', true);
  let id = '';
  try {
    await member.login('member.a');
    const me = (await member.call<S<'MyPerson'>>('GET', '/api/v1/me/person')).data;
    const properties = (
      await member.call<S<'PropertyPage'>>('GET', root + '/properties?status=ACTIVE&limit=100')
    ).data;
    const property = properties.items.find((p) => p.code === 'DEMO_OTEL');
    expect(property).toBeDefined();
    const accounts = async () =>
      (
        await member.call<{ items: S<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${me.person.id}/entitlements?asOf=${day(45)}`,
        )
      ).data.items;
    const before = await accounts();
    const search = (
      await member.call<S<'AvailabilitySearchResult'>>('POST', root + '/availability/search', {
        propertyId: property!.id,
        checkIn: day(45),
        checkOut: day(56),
        adults: 2,
        children: 0,
      })
    ).data;
    const account = before.find(
      (a) =>
        a.definition.code === search.entitlement?.entitlementCode &&
        a.definition.unitType === 'NIGHT',
    );
    expect(account).toBeDefined();
    expect(units(account!.available)).toBeGreaterThan(0n);
    expect(units(account!.available)).toBeLessThan(11000000n);
    const room = search.results.find((r) => r.roomType.code === 'STD');
    expect(room?.quote).toBeTruthy();
    const inventoryPath = `${root}/room-types/${room!.roomType.id}/inventory?from=${day(45)}&to=${day(55)}`;
    const inventory = async () =>
      (await member.call<S<'RoomTypeInventoryRange'>>('GET', inventoryPath)).data.days.map((d) => [
        d.stayDate,
        d.capacity,
        d.held,
        d.confirmed,
        d.available,
      ]);
    const originalInventory = await inventory();
    const key = randomUUID();
    const body = {
      roomTypeId: room!.roomType.id,
      checkIn: day(45),
      checkOut: day(56),
      adults: 2,
      children: 0,
      channel: 'MEMBER_PORTAL',
    };
    const held = await member.call<S<'Booking'>>('POST', root + '/holds', body, {
      expected: 201,
      key,
    });
    id = held.data.id;
    await test.info().attach('pc06-partial-release', {
      contentType: 'application/json',
      body: JSON.stringify({ bookingId: id }),
    });
    expect(
      (await member.call<S<'Booking'>>('POST', root + '/holds', body, { expected: 201, key })).data,
    ).toEqual(held.data);
    const released = await member.call<S<'Booking'>>(
      'POST',
      `${root}/bookings/${id}/release`,
      undefined,
      { key },
    );
    expect(released.data.status).toBe('CANCELLED');
    const after = (await accounts()).find((a) => a.id === account!.id)!;
    expect([units(after.available), units(after.reserved), units(after.consumed)]).toEqual([
      units(account!.available),
      units(account!.reserved),
      units(account!.consumed),
    ]);
    expect(await inventory()).toEqual(originalInventory);
    await member.call('POST', `${root}/bookings/${id}/release`, undefined, { key });
    expect((await accounts()).find((a) => a.id === account!.id)).toEqual(after);
    expect(await inventory()).toEqual(originalInventory);
  } finally {
    if (id) {
      const current = (await member.call<S<'Booking'>>('GET', `${root}/bookings/${id}`)).data;
      if (current.status === 'HOLD') await member.call('POST', `${root}/bookings/${id}/release`);
    }
    await member.close();
  }
});
