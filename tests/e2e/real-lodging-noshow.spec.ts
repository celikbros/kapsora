import { createHash } from 'node:crypto';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { resolve, relative, isAbsolute } from 'node:path';
import { fileURLToPath } from 'node:url';
import { syntheticPDF } from './synthetic-pdf';
import { expect, request, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const root = '/api/v1/accommodation';
const fixturePath = process.env['E2E_LODGING_NOSHOW_FIXTURE'] ?? '';
type Fixture = {
  bookingId: string;
  documentId?: string;
  uploadedSha256?: string;
  beforeReview?: S<'EntitlementAccount'>;
  inventory?: S<'InventoryDay'>[];
};
const prepare = process.env['E2E_LODGING_NOSHOW_PREPARE'] === '1';
const reportOnly = process.env['E2E_LODGING_NOSHOW_REPORT_ONLY'] === '1';
test.use({ trace: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' ||
    !base ||
    process.env['E2E_LODGING_NOSHOW'] !== '1' ||
    !fixturePath,
  'requires explicit opt-in for a synthetic no-show and operator-started system',
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

// Prepare before the real arrival window closes, then resume the same booking.
// No server-time overrides, fabricated scan verdicts, settings writes or duplicate stays.
test(
  reportOnly
    ? 'desk evidence and reported no-show preserve balances while payer review is unavailable'
    : 'desk links real evidence and a separate reviewer decides no-show without duplicate consumption',
  async ({ browser }) => {
    test.setTimeout(180000);
    expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
    const notBefore = process.env['E2E_LODGING_NOSHOW_NOT_BEFORE'];
    if (!prepare && notBefore) {
      const cutoff = Date.parse(notBefore);
      expect(Number.isFinite(cutoff)).toBe(true);
      const wait = Math.max(0, cutoff - Date.now());
      expect(wait, 'only a bounded natural wait; never change clocks/settings').toBeLessThanOrEqual(
        15 * 60_000,
      );
      test.setTimeout(180_000 + wait);
      await expect
        .poll(() => Date.now() >= cutoff, { timeout: wait + 5000, intervals: [1000] })
        .toBe(true);
    }

    const deskPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
    const memberPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
    const desk = new Actor(deskPage.request, 'provider', true);
    const member = new Actor(memberPage.request, 'member', true);
    const doctor = new Actor(await request.newContext(), 'backoffice', true);
    const officePage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
    const office = new Actor(officePage.request, 'backoffice', true);
    for (const page of [deskPage, memberPage, officePage]) page.setDefaultTimeout(15_000);
    const fixtureRelative = relative(
      fileURLToPath(new URL('../../tmp/', import.meta.url)),
      resolve(fixturePath),
    );
    expect(fixtureRelative.startsWith('..') || isAbsolute(fixtureRelative)).toBe(false);
    let fixture: Fixture = { bookingId: '' };
    try {
      fixture = JSON.parse(await readFile(fixturePath, 'utf8')) as Fixture;
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
    }
    let bookingId = fixture.bookingId;
    const save = async () => writeFile(fixturePath, JSON.stringify(fixture), 'utf8');
    try {
      await member.login('member.a');
      await desk.login('reservation.a');
      await doctor.login('doctor.a');
      await office.login('admin.a');
      const me = (await member.call<S<'MyPerson'>>('GET', '/api/v1/me/person')).data;
      const properties = (
        await member.call<S<'PropertyPage'>>('GET', root + '/properties?status=ACTIVE&limit=100')
      ).data;
      const property = properties.items.find((p) => p.code === 'DEMO_OTEL');
      expect(property).toBeDefined();
      if (!bookingId) {
        expect(prepare, 'create only in the explicit prepare stage').toBe(true);
        const prior = (
          await member.call<S<'BookingList'>>(
            'GET',
            `${root}/bookings?propertyId=${property!.id}&checkInFrom=${day(0)}&checkInTo=${day(0)}&limit=100`,
          )
        ).data;
        expect(prior.nextCursor).toBeFalsy();
        expect(
          prior.items.filter(
            (b) => !['CANCELLED', 'EXPIRED', 'COMPLETED', 'NO_SHOW'].includes(b.status),
          ),
          'an existing arrival requires the saved fixture; do not create a duplicate stay',
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
        fixture.inventory = (
          await desk.call<S<'RoomTypeInventoryRange'>>(
            'GET',
            `${root}/room-types/${room!.roomType.id}/inventory?from=${day(0)}&to=${day(2)}`,
          )
        ).data.days;
        expect(fixture.inventory).toHaveLength(3);
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
        fixture.bookingId = bookingId;
        await save();
        await test.info().attach('pc06-noshow-held', {
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
      expect(['CONFIRMED', 'NO_SHOW']).toContain(booking.status);
      const path = `${root}/bookings/${bookingId}`;
      const accounts = async () =>
        (
          await member.call<{ items: S<'EntitlementAccount'>[] }>(
            'GET',
            `/api/v1/people/${me.person.id}/entitlements?asOf=${booking.checkIn}`,
          )
        ).data.items;
      const account = (await accounts()).find(
        (a) => a.enrollmentId === booking.enrollmentId && a.definition.unitType === 'NIGHT',
      )!;
      expect(account).toBeDefined();
      const accountNow = async () => (await accounts()).find((a) => a.id === account.id)!;
      const inventory = async () =>
        (
          await desk.call<S<'RoomTypeInventoryRange'>>(
            'GET',
            `${root}/room-types/${booking.roomTypeId}/inventory?from=${booking.checkIn}&to=${new Date(new Date(booking.checkOut + 'T12:00:00Z').getTime() - 86400000).toISOString().slice(0, 10)}`,
          )
        ).data.days;
      if (!fixture.beforeReview) {
        fixture.beforeReview = account;
        await save();
      }
      const meDesk = (await desk.call<{ tenants: S<'TenantContext'>[] }>('GET', '/api/v1/me')).data;
      expect(meDesk.tenants.find((t) => t.tenant.code === 'DEMO_A')!.permissions).toContain(
        'document.booking_evidence.link',
      );
      expect(meDesk.tenants.find((t) => t.tenant.code === 'DEMO_A')!.permissions).not.toContain(
        'document.link',
      );
      expect(meDesk.tenants.find((t) => t.tenant.code === 'DEMO_A')!.permissions).not.toContain(
        'health.clinical.read',
      );
      if (booking.status === 'CONFIRMED') {
        await deskPage.goto(base + '/portal/lodging/desk');
        const row = deskPage.getByTestId('arrival-row').filter({ hasText: booking.reference });
        await expect(row).toBeVisible();
        await row.getByRole('button', { name: 'Gelmedi', exact: true }).click();
        const form = row.getByTestId('no-show-form');
        if (!fixture.documentId) {
          await expect(
            form.getByRole('button', { name: 'Gelmedi\u011fini bildir', exact: true }),
          ).toBeDisabled();
          const upload = form.getByTestId('document-upload-form');
          const original = syntheticPDF();
          fixture.uploadedSha256 = createHash('sha256').update(original).digest('hex');
          await save();
          await upload.getByLabel(/Dosya se\u00e7/).setInputFiles({
            name: `pc06-noshow-${bookingId}.pdf`,
            mimeType: 'application/pdf',
            buffer: original,
          });
          await upload.getByLabel(/Belge t\u00fcr\u00fc/).selectOption('NO_SHOW_EVIDENCE');
          const reserved = deskPage.waitForResponse(
            (r) =>
              new URL(r.url()).pathname === '/api/v1/documents' && r.request().method() === 'POST',
          );
          await upload.getByRole('button', { name: 'Belge y\u00fckle', exact: true }).click();
          const response = await reserved;
          expect(response.status()).toBe(201);
          const data = (await response.json()) as { document: S<'Document'> };
          fixture.documentId = data.document.id;
          await save();
        }
        await expect
          .poll(
            async () =>
              (await desk.call<S<'Document'>>('GET', `/api/v1/documents/${fixture.documentId}`))
                .data.downloadable,
            { timeout: 45000 },
          )
          .toBe(true);
        const document = (
          await desk.call<S<'Document'>>('GET', `/api/v1/documents/${fixture.documentId}`)
        ).data;
        const link = document.links.find(
          (l) =>
            l.aggregateType === 'BOOKING' &&
            l.aggregateId === bookingId &&
            l.documentTypeCode === 'NO_SHOW_EVIDENCE',
        );
        expect(link).toBeDefined();
        // Older prepared fixtures did not retain the original digest; their resume still
        // verifies downloaded bytes against the stored digest without inventing new bytes.
        if (fixture.uploadedSha256) expect(document.sha256).toBe(fixture.uploadedSha256);
        const download = (
          await desk.call<S<'DocumentDownload'>>(
            'POST',
            `/api/v1/documents/${document.id}/download`,
            {},
          )
        ).data;
        const bytes = await deskPage.request.get(download.url);
        expect(bytes.status()).toBe(200);
        expect(
          createHash('sha256')
            .update(await bytes.body())
            .digest('hex'),
        ).toBe(document.sha256);

        await desk.call('DELETE', `/api/v1/documents/${document.id}/links/${link!.id}`, undefined, {
          expected: 403,
        });
        await desk.call(
          'POST',
          `/api/v1/documents/${document.id}/links`,
          { aggregateType: 'BOOKING', aggregateId: bookingId, documentTypeCode: 'INVOICE' },
          { expected: 403 },
        );
        if (prepare) {
          const early = await desk.call<S<'Problem'>>(
            'POST',
            path + '/no-show',
            { evidenceDocumentId: document.id },
            { expected: 409 },
          );
          expect(early.data.code).toBe('NO_SHOW_TOO_EARLY');
          expect(await accountNow()).toEqual(fixture.beforeReview);
          expect((await getBooking()).status).toBe('CONFIRMED');
          await test.info().attach('pc06-noshow-prepared', {
            contentType: 'application/json',
            body: JSON.stringify({
              bookingId,
              documentId: document.id,
              checkIn: booking.checkIn,
              checkOut: booking.checkOut,
            }),
          });
          return;
        }
        const current = await office.call<S<'NoShowResult'> | S<'Problem'>>(
          'GET',
          path + '/no-show',
          undefined,
          { expected: [200, 404] },
        );
        if (current.status === 404) {
          const beforeInventory = await inventory();
          const pending = deskPage.waitForResponse(
            (r) =>
              new URL(r.url()).pathname === path + '/no-show' && r.request().method() === 'POST',
          );
          await form.getByRole('button', { name: 'Gelmedi\u011fini bildir', exact: true }).click();
          const reported = await pending;
          expect(reported.status()).toBe(201);
          const result = (await reported.json()) as S<'NoShowResult'>;
          expect(result.report.status).toBe('REPORTED');
          expect(result.booking.status).toBe('CONFIRMED');
          expect(micros(result.report.assessedFeeAmount)).toBe(0n);
          expect(await accountNow()).toEqual(fixture.beforeReview);
          expect(await inventory()).toEqual(beforeInventory);
          const key = reported.request().headers()['idempotency-key'];
          expect(key).toBeTruthy();
          const replay = await desk.call<S<'NoShowResult'>>(
            'POST',
            path + '/no-show',
            { evidenceDocumentId: document.id },
            { expected: 201, key },
          );
          expect(replay.data).toEqual(result);
        } else {
          const existing = current.data as S<'NoShowResult'>;
          expect(existing.report.status).toBe('REPORTED');
          expect(existing.report.evidenceDocumentId).toBe(document.id);
          expect(micros(existing.report.assessedFeeAmount)).toBe(0n);
          expect(await accountNow()).toEqual(fixture.beforeReview);
        }
        await desk.call(
          'POST',
          path + '/no-show/review',
          { status: 'CONFIRMED' },
          { expected: 403 },
        );
        expect(await accountNow()).toEqual(fixture.beforeReview);
        await officePage.goto(base + `/lodging/bookings/${bookingId}`);
        await test.info().attach('pc06-noshow-review-page', {
          contentType: 'text/plain',
          body: new URL(officePage.url()).pathname,
        });
        await expect(officePage.getByTestId('no-show-status')).toHaveText('Bildirildi');
        if (reportOnly) {
          await expect(officePage.getByTestId('no-show-review')).toHaveCount(0);
          const denied = await office.call<S<'Problem'>>(
            'POST',
            path + '/no-show/review',
            { status: 'CONFIRMED' },
            { expected: 403 },
          );
          expect(denied.data.code).toBe('PERMISSION_DENIED');
          expect((await getBooking()).status).toBe('CONFIRMED');
          expect(await accountNow()).toEqual(fixture.beforeReview);
          const report = (await office.call<S<'NoShowResult'>>('GET', path + '/no-show')).data
            .report;
          expect(report.status).toBe('REPORTED');
          expect(report.consumedNights).toBe(0);
          await test.info().attach('pc06-noshow-reported-only', {
            contentType: 'application/json',
            body: JSON.stringify({
              bookingId,
              documentId: document.id,
              reportId: report.id,
              status: report.status,
              consumedNights: 0,
              payerReview: 'PERMISSION_DENIED',
            }),
          });
          return;
        }
        await expect(
          officePage.getByTestId('no-show-review'),
          'payer role must have an explicitly approved review grant before completing this acceptance',
        ).toBeVisible();

        const decision = officePage.waitForResponse(
          (r) => new URL(r.url()).pathname === path + '/no-show/review',
        );
        await officePage
          .getByTestId('no-show-review')
          .getByRole('button', { name: 'Onayla', exact: true })
          .click();
        const reviewed = await decision;
        expect(reviewed.status()).toBe(200);
        const closed = (await reviewed.json()) as S<'NoShowResult'>;
        expect(closed.report.status).toBe('CONFIRMED');
        expect(closed.report.consumedNights).toBe(3);
        expect(closed.booking.status).toBe('NO_SHOW');
        const reviewedKey = reviewed.request().headers()['idempotency-key'];
        expect(reviewedKey).toBeTruthy();
        expect(
          (
            await office.call<S<'NoShowResult'>>(
              'POST',
              path + '/no-show/review',
              { status: 'CONFIRMED' },
              { key: reviewedKey },
            )
          ).data,
        ).toEqual(closed);
      }
      booking = await getBooking();
      expect(booking.status).toBe('NO_SHOW');
      const after = await accountNow();
      expect(micros(after.available)).toBe(micros(fixture.beforeReview!.available));
      expect(micros(after.reserved)).toBe(micros(fixture.beforeReview!.reserved) - 3000000n);
      expect(micros(after.consumed)).toBe(micros(fixture.beforeReview!.consumed) + 3000000n);
      const ledger = (
        await member.call<S<'LedgerPage'>>(
          'GET',
          `/api/v1/entitlement-accounts/${account.id}/ledger?limit=100`,
        )
      ).data;
      expect(ledger.nextCursor).toBeFalsy();
      const movements = ledger.items.filter(
        (e) => e.reservationId === booking.entitlementReservationId,
      );
      expect(movements.map((e) => e.movementType).sort()).toEqual(['CONSUME', 'RESERVE']);
      expect(movements.reduce((sum, e) => sum + micros(e.deltaConsumed), 0n)).toBe(3000000n);
      expect(movements.reduce((sum, e) => sum + micros(e.deltaReserved), 0n)).toBe(0n);
      expect(
        (await inventory()).map((d) => [d.stayDate, d.held, d.confirmed, d.available]),
      ).toEqual(fixture.inventory!.map((d) => [d.stayDate, d.held, d.confirmed, d.available]));
      await office.call(
        'POST',
        path + '/no-show/review',
        { status: 'CONFIRMED' },
        { expected: 409 },
      );
      await desk.call(
        'POST',
        path + '/no-show',
        { evidenceDocumentId: fixture.documentId },
        { expected: 409 },
      );
      expect(await accountNow()).toEqual(after);
      await mkdir('.impeccable/review/lodging-noshow', { recursive: true });
      await officePage.goto(base + `/lodging/bookings/${bookingId}`);
      await expect(officePage.getByTestId('no-show-status')).toHaveText('Onayland\u0131');
      for (const width of [390, 1440]) {
        await officePage.setViewportSize({ width, height: 1000 });
        // A full-page capture otherwise retains the sticky header's previous scroll position.
        await officePage.evaluate(() => window.scrollTo(0, 0));
        await expect.poll(() => officePage.evaluate(() => window.scrollY)).toBe(0);
        expect(
          await officePage.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
        ).toBe(true);
        await officePage.screenshot({
          path: `.impeccable/review/lodging-noshow/reviewed-${width}.png`,
          fullPage: true,
        });
      }
      await test.info().attach('pc06-noshow-confirmed', {
        contentType: 'application/json',
        body: JSON.stringify({
          bookingId,
          documentId: fixture.documentId,
          consumedNights: 3,
          assessedFee: '0',
          accountId: account.id,
        }),
      });
    } finally {
      if (bookingId)
        await test.info().attach('pc06-noshow-resume', {
          contentType: 'application/json',
          body: JSON.stringify({ bookingId, documentId: fixture.documentId }),
        });
      await Promise.allSettled([office.close(), doctor.close(), desk.close(), member.close()]);
      await officePage.close();
      await deskPage.close();
      await memberPage.close();
    }
  },
);
