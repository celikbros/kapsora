import { randomUUID } from 'node:crypto';
import { mkdir, readFile, rename, writeFile } from 'node:fs/promises';
import { dirname, isAbsolute, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, request, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
type Intent = { key: string; body: object; createdAt: string; etag?: string };
type Fixture = {
  version: 1;
  base: string;
  tenantId: string;
  personId: string;
  propertyId: string;
  roomTypeId: string;
  checkIn: string;
  checkOut: string;
  accountId: string;
  baseline: { available: string; reserved: string; consumed: string };
  inventory: S<'InventoryDay'>[];
  hold?: Intent;
  bookingId?: string;
  requestId?: string;
  confirm?: Intent;
  approval?: Intent;
  authorizationId?: string;
  preview?: S<'CancellationQuote'>;
  cancel?: Intent;
  cancellationId?: string;
  cancellationResult?: S<'CancellationResult'>;
  replayVerifiedAt?: string;
  claimId?: string;
};

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const fixturePath = process.env['E2E_LODGING_CAP_CANCELLATION_FIXTURE'] ?? '';
const root = '/api/v1/accommodation';
test.use({ trace: 'off', screenshot: 'off', video: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' ||
    process.env['E2E_LODGING_CAP_CANCELLATION'] !== '1' ||
    !base ||
    !fixturePath,
  'requires explicit cap-cancellation opt-in, private resume fixture, and operator-started API',
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
function nextDate(value: string, offset: number) {
  expect(value).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  const parsed = new Date(`${value}T12:00:00Z`);
  expect(parsed.toISOString().slice(0, 10)).toBe(value);
  return new Date(parsed.getTime() + offset * 86400000).toISOString().slice(0, 10);
}
function receiptFresh(intent: Intent) {
  const created = Date.parse(intent.createdAt);
  expect(Number.isFinite(created), 'journaled command must have a valid creation time').toBe(true);
  const age = Date.now() - created;
  expect(age, 'journaled command time cannot be in the future').toBeGreaterThanOrEqual(-60_000);
  return age < 24 * 60 * 60 * 1000;
}
function micros(value: string | number) {
  const raw = String(value);
  expect(raw).toMatch(/^-?\d+(?:\.\d{1,6})?$/);
  const [whole, fraction = ''] = raw.replace(/^-/, '').split('.');
  return (
    (raw.startsWith('-') ? -1n : 1n) * (BigInt(whole!) * 1000000n + BigInt(fraction.padEnd(6, '0')))
  );
}
function amount(value: string | number) {
  return micros(value).toString();
}
function expectEffect(effect: S<'CancellationEntitlementEffect'> | undefined) {
  expect(effect, 'the confirmed NIGHT authorization must expose exact cap evidence').toBeDefined();
  expect(micros(effect!.consumedServiceNights)).toBe(500000n);
  expect(micros(effect!.releasedServiceNights)).toBe(0n);
  expect(micros(effect!.consumedEntitlementUnits)).toBe(500000n);
  expect(micros(effect!.releasedEntitlementUnits)).toBe(0n);
}

test('fractional approval caps terminal cancellation entitlement while preserving the frozen fee', async () => {
  test.setTimeout(180000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const fixtureRoot = fileURLToPath(new URL('../../tmp/', import.meta.url));
  const path = resolve(fixturePath);
  const rel = relative(fixtureRoot, path);
  expect(
    rel && !rel.startsWith('..') && !isAbsolute(rel),
    'fixture must be private under repo tmp/',
  ).toBe(true);
  await mkdir(dirname(path), { recursive: true });
  let fixture: Fixture | undefined;
  try {
    fixture = JSON.parse(await readFile(path, 'utf8')) as Fixture;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
  }
  if (fixture) {
    expect(fixture.version).toBe(1);
    expect(fixture.base).toBe(base);
  }
  const save = async () => {
    expect(fixture).toBeDefined();
    const temporary = `${path}.${randomUUID()}.tmp`;
    await writeFile(temporary, JSON.stringify(fixture), { flag: 'wx', mode: 0o600 });
    await rename(temporary, path);
  };
  const memberContext = await request.newContext();
  const member = new Actor(memberContext, 'member', true);
  const desk = new Actor(await request.newContext(), 'provider', true);
  const doctor = new Actor(await request.newContext(), 'backoffice', true);
  const finance = new Actor(await request.newContext(), 'backoffice', true);
  try {
    await member.login('member.a');
    await desk.login('reservation.a');
    await doctor.login('doctor.a');
    await finance.login('financial.reviewer');
    const tenant = (
      await member.call<{ items: { id: string; code: string }[] }>('GET', '/api/v1/tenants')
    ).data.items.find((t) => t.code === 'DEMO_A');
    expect(tenant).toBeDefined();
    const me = (await member.call<S<'MyPerson'>>('GET', '/api/v1/me/person')).data;
    const properties = (
      await member.call<S<'PropertyPage'>>('GET', root + '/properties?status=ACTIVE&limit=100')
    ).data;
    expect(properties.nextCursor).toBeFalsy();
    const property = properties.items.find((p) => p.code === 'DEMO_OTEL');
    expect(property).toBeDefined();
    if (fixture) {
      expect(fixture.tenantId).toBe(tenant!.id);
      expect(fixture.personId).toBe(me.person.id);
      expect(fixture.propertyId).toBe(property!.id);
      expect(nextDate(fixture.checkIn, 3)).toBe(fixture.checkOut);
      expect(fixture.inventory.map((entry) => entry.stayDate)).toEqual([
        fixture.checkIn,
        nextDate(fixture.checkIn, 1),
        nextDate(fixture.checkIn, 2),
      ]);
      expect(fixture.roomTypeId).toMatch(/^[0-9a-f-]{36}$/i);
      expect(fixture.accountId).toMatch(/^[0-9a-f-]{36}$/i);
    } else {
      // The arrival is tomorrow. An existing booking without this journal is never adopted.
      const checkIn = day(1),
        checkOut = nextDate(checkIn, 3);
      const prior = (
        await member.call<S<'BookingList'>>(
          'GET',
          `${root}/bookings?propertyId=${property!.id}&checkInFrom=${checkIn}&checkInTo=${checkIn}&limit=100`,
        )
      ).data;
      expect(prior.nextCursor).toBeFalsy();
      expect(prior.items, 'existing arrival requires its original private fixture').toEqual([]);
      const search = (
        await member.call<S<'AvailabilitySearchResult'>>('POST', root + '/availability/search', {
          propertyId: property!.id,
          checkIn,
          checkOut,
          adults: 2,
          children: 0,
        })
      ).data;
      expect(search.eligible).toBe(true);
      const room = search.results.find((r) => r.roomType.code === 'STD');
      expect(room?.quote).toBeTruthy();
      expect(search.entitlement?.unit).toBe('NIGHT');
      expect(micros(search.entitlement!.remaining)).toBeGreaterThanOrEqual(3000000n);
      expect(micros(room!.quote!.memberAmount)).toBe(0n);
      const accounts = (
        await member.call<{ items: S<'EntitlementAccount'>[] }>(
          'GET',
          `/api/v1/people/${me.person.id}/entitlements?asOf=${checkIn}`,
        )
      ).data.items;
      const nights = accounts.filter(
        (a) =>
          a.definition.unitType === 'NIGHT' &&
          a.definition.code === search.entitlement!.entitlementCode,
      );
      expect(nights).toHaveLength(1);
      expect(micros(nights[0]!.available)).toBeGreaterThanOrEqual(3000000n);
      const inventory = (
        await desk.call<S<'RoomTypeInventoryRange'>>(
          'GET',
          `${root}/room-types/${room!.roomType.id}/inventory?from=${checkIn}&to=${nextDate(checkOut, -1)}`,
        )
      ).data.days;
      expect(inventory).toHaveLength(3);
      fixture = {
        version: 1,
        base,
        tenantId: tenant!.id,
        personId: me.person.id,
        propertyId: property!.id,
        roomTypeId: room!.roomType.id,
        checkIn,
        checkOut,
        accountId: nights[0]!.id,
        baseline: {
          available: amount(nights[0]!.available),
          reserved: amount(nights[0]!.reserved),
          consumed: amount(nights[0]!.consumed),
        },
        inventory,
      };
      // Exclusive creation refuses any other writer's fixture. Every later intent is saved before its command.
      await writeFile(path, JSON.stringify(fixture), { flag: 'wx', mode: 0o600 });
    }
    const f = fixture;
    const bookingList = async () => {
      const list = (
        await member.call<S<'BookingList'>>(
          'GET',
          `${root}/bookings?propertyId=${f.propertyId}&checkInFrom=${f.checkIn}&checkInTo=${f.checkIn}&limit=100`,
        )
      ).data;
      expect(list.nextCursor).toBeFalsy();
      return list.items;
    };
    const booking = async () =>
      (await member.call<S<'Booking'>>('GET', `${root}/bookings/${f.bookingId}`)).data;
    if (!f.bookingId) {
      if (!f.hold) {
        expect(
          await bookingList(),
          'never create a second arrival without its saved booking ID',
        ).toEqual([]);
        f.hold = {
          key: randomUUID(),
          createdAt: new Date().toISOString(),
          body: {
            roomTypeId: f.roomTypeId,
            checkIn: f.checkIn,
            checkOut: f.checkOut,
            adults: 2,
            children: 0,
            channel: 'MEMBER_PORTAL',
          },
        };
        await save();
      }
      // A lost hold response may already have committed. Reconcile before reusing the same key.
      const found = (await bookingList()).filter(
        (b) => b.personId === f.personId && b.roomTypeId === f.roomTypeId,
      );
      expect(found.length).toBeLessThanOrEqual(1);
      test.skip(
        !receiptFresh(f.hold),
        'PENDING: saved hold response identity is unproven after receipt retention; reconcile manually',
      );
      if (found.length) {
        const replayed = (
          await member.call<S<'Booking'>>('POST', root + '/holds', f.hold.body, {
            expected: 201,
            key: f.hold.key,
          })
        ).data;
        expect(replayed.id, 'recover only this journaled hold command').toBe(found[0]!.id);
        f.bookingId = replayed.id;
      } else {
        expect(f.checkIn, 'only a new hold for tomorrow may be issued').toBe(day(1));
        f.bookingId = (
          await member.call<S<'Booking'>>('POST', root + '/holds', f.hold.body, {
            expected: 201,
            key: f.hold.key,
          })
        ).data.id;
      }
      await save();
    }
    let current = await booking();
    expect(current.personId).toBe(f.personId);
    expect(current.propertyId).toBe(f.propertyId);
    expect(current.roomTypeId).toBe(f.roomTypeId);
    expect(current.checkIn).toBe(f.checkIn);
    expect(current.checkOut).toBe(f.checkOut);
    expect(current.nights).toBe(3);
    expect(current.quoteSnapshot.coveredNights).toBe(3);
    if (current.status === 'HOLD') {
      const heldAccount = (
        await member.call<S<'EntitlementAccount'>>(
          'GET',
          `/api/v1/entitlement-accounts/${f.accountId}`,
        )
      ).data;
      expect(micros(heldAccount.available)).toBe(BigInt(f.baseline.available) - 3000000n);
      expect(micros(heldAccount.reserved)).toBe(BigInt(f.baseline.reserved) + 3000000n);
      const ownReservation = heldAccount.openReservations?.filter(
        (r) => r.id === current.entitlementReservationId,
      );
      expect(ownReservation).toHaveLength(1);
      expect(micros(ownReservation![0]!.quantity)).toBe(3000000n);
      if (!f.confirm) {
        f.confirm = { key: randomUUID(), createdAt: new Date().toISOString(), body: {} };
        await save();
      }
      await member.call('POST', `${root}/bookings/${f.bookingId}/confirm`, f.confirm.body, {
        key: f.confirm.key,
      });
      current = await booking();
    }
    expect(current.serviceRequestId).toBeTruthy();
    if (!f.requestId) {
      f.requestId = current.serviceRequestId!;
      await save();
    }
    expect(current.serviceRequestId).toBe(f.requestId);
    if (current.status === 'PENDING_APPROVAL') {
      const sourcePath = `/api/v1/service-requests/${f.requestId}`;
      const source = await doctor.call<S<'ServiceRequest'>>('GET', sourcePath);
      expect(source.data.items).toHaveLength(1);
      expect(micros(source.data.items[0]!.requestedQuantity)).toBe(3000000n);
      if (source.data.status === 'PENDING_REVIEW') {
        if (!f.approval) {
          f.approval = {
            key: randomUUID(),
            createdAt: new Date().toISOString(),
            etag: source.etag,
            body: {
              reasonCode: 'PC06_CAP_CANCELLATION',
              items: [
                {
                  lineNo: 1,
                  status: 'PARTIALLY_APPROVED',
                  approvedQuantity: '0.5',
                  decisionReasonCode: 'PC06_CAP_CANCELLATION',
                },
              ],
            },
          };
          await save();
        }
        await doctor.call('POST', sourcePath + '/partially-approve', f.approval.body, {
          key: f.approval.key,
          etag: f.approval.etag,
        });
      } else expect(source.data.status).toBe('PARTIALLY_APPROVED');
      await expect.poll(async () => (await booking()).status, { timeout: 45000 }).toBe('CONFIRMED');
      current = await booking();
    }
    expect(['CONFIRMED', 'CANCELLED']).toContain(current.status);
    expect(current.authorizationId).toBeTruthy();
    if (!f.authorizationId) {
      f.authorizationId = current.authorizationId!;
      await save();
    }
    expect(current.authorizationId).toBe(f.authorizationId);
    const auth = (
      await doctor.call<S<'Authorization'>>('GET', `/api/v1/authorizations/${f.authorizationId}`)
    ).data;
    expect(auth.requestId).toBe(f.requestId);
    expect(auth.items).toHaveLength(1);
    expect(micros(auth.items[0]!.approvedQuantity)).toBe(500000n);
    const firstNight = current.nightlyAmounts[0]!;
    const fee = {
      total: micros(firstNight.unitAmount),
      payer: micros(firstNight.payerAmount),
      member: micros(firstNight.memberAmount),
    };
    expect(fee.total).toBe(fee.payer + fee.member);
    expect(fee.total).toBeGreaterThan(0n);
    if (current.status === 'CONFIRMED') {
      const preview = (
        await member.call<S<'CancellationPreview'>>(
          'POST',
          `${root}/bookings/${f.bookingId}/cancellation-preview`,
        )
      ).data.quote;
      expect(preview.free).toBe(false);
      expect(preview.penaltyNights).toBe(1);
      expectEffect(preview.entitlementEffect);
      expect(micros(preview.feeAmount)).toBe(fee.total);
      expect(micros(preview.payerFee)).toBe(fee.payer);
      expect(micros(preview.memberFee)).toBe(fee.member);
      if (!f.preview) {
        f.preview = preview;
        await save();
      } else expect(preview).toEqual(f.preview);
      if (!f.cancel) {
        f.cancel = { key: randomUUID(), createdAt: new Date().toISOString(), body: {} };
        await save();
      }
      test.skip(
        !receiptFresh(f.cancel),
        'PENDING: cancellation intent outlived receipt retention while booking remains confirmed',
      );
      const result = (
        await member.call<S<'CancellationResult'>>(
          'POST',
          `${root}/bookings/${f.bookingId}/cancel`,
          f.cancel.body,
          { key: f.cancel.key },
        )
      ).data;
      expect(result.quote).toEqual(f.preview);
      expectEffect(result.cancellation.entitlementEffect);
      f.cancellationId = result.cancellation.id;
      f.cancellationResult = result;
      await save();
      current = result.booking;
    }
    expect(current.status).toBe('CANCELLED');
    expect(f.cancel, 'terminal state requires the saved original cancellation key').toBeDefined();
    const withinReceiptWindow = receiptFresh(f.cancel!);
    let replay: S<'CancellationResult'> | undefined;
    if (withinReceiptWindow) {
      replay = (
        await member.call<S<'CancellationResult'>>(
          'POST',
          `${root}/bookings/${f.bookingId}/cancel`,
          f.cancel!.body,
          { key: f.cancel!.key },
        )
      ).data;
      expect(replay.booking.status).toBe('CANCELLED');
      expectEffect(replay.cancellation.entitlementEffect);
      expect(replay.quote).toEqual(f.preview);
      if (f.cancellationResult) expect(replay).toEqual(f.cancellationResult);
      f.cancellationId = replay.cancellation.id;
      f.cancellationResult = replay;
      f.replayVerifiedAt = new Date().toISOString();
      await save();
    } else {
      // A terminal booking needs no new command. An expired receipt key is never retried.
      replay = f.cancellationResult;
      if (replay) {
        expect(replay.booking.id).toBe(f.bookingId);
        expect(replay.booking.status).toBe('CANCELLED');
        expect(replay.cancellation.id).toBe(f.cancellationId);
        expectEffect(replay.cancellation.entitlementEffect);
        expect(replay.quote).toEqual(f.preview);
      }
    }
    const after = (
      await member.call<S<'EntitlementAccount'>>(
        'GET',
        `/api/v1/entitlement-accounts/${f.accountId}`,
      )
    ).data;
    expect(micros(after.available)).toBe(BigInt(f.baseline.available) - 500000n);
    expect(micros(after.consumed)).toBe(BigInt(f.baseline.consumed) + 500000n);
    expect(micros(after.reserved)).toBe(BigInt(f.baseline.reserved));
    const inventory = (
      await desk.call<S<'RoomTypeInventoryRange'>>(
        'GET',
        `${root}/room-types/${f.roomTypeId}/inventory?from=${f.checkIn}&to=${nextDate(f.checkOut, -1)}`,
      )
    ).data.days;
    expect(inventory.map((d) => [d.stayDate, d.available, d.held, d.confirmed])).toEqual(
      f.inventory.map((d) => [d.stayDate, d.available, d.held, d.confirmed]),
    );
    const ledger = (
      await member.call<S<'LedgerPage'>>(
        'GET',
        `/api/v1/entitlement-accounts/${f.accountId}/ledger?limit=100`,
      )
    ).data;
    expect(ledger.nextCursor).toBeFalsy();
    const movement = ledger.items.filter(
      (e) => e.reservationId === current.entitlementReservationId,
    );
    expect(movement.reduce((sum, e) => sum + micros(e.deltaAvailable), 0n)).toBe(-500000n);
    expect(movement.reduce((sum, e) => sum + micros(e.deltaConsumed), 0n)).toBe(500000n);
    expect(movement.reduce((sum, e) => sum + micros(e.deltaReserved), 0n)).toBe(0n);
    const claims = async () => {
      const list = (
        await finance.call<S<'ClaimPage'>>(
          'GET',
          `/api/v1/claims?personId=${f.personId}&providerOrganizationId=${property!.providerOrganizationId}&serviceDateFrom=${f.checkIn}&serviceDateTo=${f.checkIn}&limit=100`,
        )
      ).data;
      expect(list.nextCursor).toBeFalsy();
      return list.items.filter((c) => c.sourceType === 'BOOKING' && c.sourceId === f.bookingId);
    };
    await expect.poll(async () => (await claims()).length, { timeout: 45000 }).toBe(1);
    const claim = (await claims())[0]!;
    if (!f.claimId) {
      f.claimId = claim.id;
      await save();
    }
    expect(claim.id).toBe(f.claimId);
    const detail = (await finance.call<S<'Claim'>>('GET', `/api/v1/claims/${f.claimId}`)).data;
    expect(detail.lines).toHaveLength(1);
    expect(detail.lines[0]!.unitType).toBe('CANCELLATION_FEE');
    expect(micros(detail.lines[0]!.lineAmount)).toBe(fee.total);
    expect(micros(detail.lines[0]!.decision!.payerAmount)).toBe(fee.payer);
    expect(micros(detail.lines[0]!.decision!.memberAmount)).toBe(fee.member);
    expect((await claims()).map((c) => c.id)).toEqual([f.claimId]);
    test.skip(
      !replay,
      'PENDING: terminal GET evidence is consistent, but the expired cancellation response identity is unproven',
    );
    await test.info().attach('pc06-cap-cancellation-ids', {
      contentType: 'application/json',
      body: JSON.stringify({
        bookingId: f.bookingId,
        requestId: f.requestId,
        authorizationId: f.authorizationId,
        cancellationId: f.cancellationId,
        claimId: f.claimId,
      }),
    });
  } finally {
    await finance.close();
    await doctor.close();
    await desk.close();
    await member.close();
  }
});
