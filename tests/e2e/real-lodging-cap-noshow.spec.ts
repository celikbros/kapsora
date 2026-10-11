import { createHash, randomUUID } from 'node:crypto';
import { link, mkdir, open, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { dirname, isAbsolute, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, request, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';
import { syntheticPDF } from './synthetic-pdf';

type S<K extends keyof components['schemas']> = components['schemas'][K];
const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const fixturePath = process.env['E2E_LODGING_CAP_NOSHOW_FIXTURE'] ?? '';
const root = '/api/v1/accommodation';
type Intent = { key: string; state: 'pending' | 'done'; createdAt: string };
type HoldBody = {
  roomTypeId: string;
  checkIn: string;
  checkOut: string;
  adults: number;
  children: number;
  channel: 'MEMBER_PORTAL';
};
type Fixture = {
  bookingId?: string;
  checkIn?: string;
  hold?: Intent & { body: HoldBody };
  confirm?: Intent;
  approve?: Intent & { etag: string };
  uploadStarted?: boolean;
  documentId?: string;
  documentSha256?: string;
  report?: Intent;
  reportId?: string;
  review?: Intent;
  beforeAccount?: S<'EntitlementAccount'>;
  beforeInventory?: S<'InventoryDay'>[];
  fee?: { assessed: string; payer: string; member: string; currency: string };
  feeBearingProof?: boolean;
  estimatedRetryAt?: string;
};

function decimal(value: string | number) {
  const input = String(value);
  expect(input).toMatch(/^-?\d+(?:\.\d{1,6})?$/);
  const sign = input.startsWith('-') ? -1n : 1n;
  const [whole, fraction = ''] = input.replace(/^-/, '').split('.');
  return sign * (BigInt(whole!) * 1_000_000n + BigInt(fraction.padEnd(6, '0')));
}
function day(offset: number, zone: string) {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: zone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).format(new Date());
  return new Date(Date.parse(parts + 'T12:00:00Z') + offset * 86_400_000)
    .toISOString()
    .slice(0, 10);
}
function policyNights(covered: number, percent: string) {
  const scaled = decimal(percent);
  return Number((BigInt(covered) * scaled + 100_000_000n - 1n) / 100_000_000n);
}

test.use({ trace: 'off', screenshot: 'off', video: 'off' });
test.skip(
  process.env['E2E_REAL_API'] !== '1' ||
    process.env['E2E_LODGING_CAP_NOSHOW'] !== '1' ||
    !base ||
    !fixturePath,
  'requires an operator-started local system, explicit cap/no-show opt-in and a persistent private fixture',
);

test('approved 1.5-night remainder caps a confirmed no-show exactly once', async ({ browser }) => {
  test.setTimeout(180_000);
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const privateRoot = fileURLToPath(new URL('../../tmp/', import.meta.url));
  const fixtureAbsolute = resolve(fixturePath);
  const privateRelative = relative(privateRoot, fixtureAbsolute);
  expect(
    privateRelative && !privateRelative.startsWith('..') && !isAbsolute(privateRelative),
  ).toBeTruthy();
  await mkdir(dirname(fixtureAbsolute), { recursive: true });
  // No second process may use this journal concurrently. A stale lock needs inspection;
  // deleting it automatically could replay an uncertain business command.
  let lock;
  try {
    lock = await open(`${fixtureAbsolute}.lock`, 'wx', 0o600);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'EEXIST')
      throw new Error('PENDING: fixture lock exists; inspect the prior run before resuming', {
        cause: error,
      });
    throw error;
  }
  try {
    let fixture: Fixture = {};
    let fixtureExists = false;
    try {
      fixture = JSON.parse(await readFile(fixtureAbsolute, 'utf8')) as Fixture;
      fixtureExists = true;
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
    }
    const save = async () => {
      const temporary = `${fixtureAbsolute}.${randomUUID()}.tmp`;
      try {
        await writeFile(temporary, JSON.stringify(fixture, null, 2), { flag: 'wx', mode: 0o600 });
        if (fixtureExists) await rename(temporary, fixtureAbsolute);
        else {
          // The first publish cannot overwrite another process's previously saved fixture.
          await link(temporary, fixtureAbsolute);
          fixtureExists = true;
        }
      } finally {
        await rm(temporary, { force: true });
      }
    };
    const intent = async (name: 'confirm' | 'report' | 'review') => {
      fixture[name] ??= {
        key: randomUUID(),
        state: 'pending',
        createdAt: new Date().toISOString(),
      };
      await save();
      return fixture[name]!.key;
    };
    const deskPage = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
    const desk = new Actor(deskPage.request, 'provider', true);
    const member = new Actor(await request.newContext(), 'member', true);
    const doctor = new Actor(await request.newContext(), 'backoffice', true);
    const reviewer = new Actor(await request.newContext(), 'backoffice', true);
    const finance = new Actor(await request.newContext(), 'backoffice', true);
    try {
      await member.login('member.a');
      await desk.login('reservation.a');
      await doctor.login('doctor.a');
      await reviewer.login('admin.a');
      await finance.login('financial.reviewer');
      const me = (await member.call<S<'MyPerson'>>('GET', '/api/v1/me/person')).data;
      const properties = (
        await member.call<S<'PropertyPage'>>('GET', `${root}/properties?status=ACTIVE&limit=100`)
      ).data;
      expect(properties.nextCursor).toBeFalsy();
      const property = properties.items.find((p) => p.code === 'DEMO_OTEL');
      expect(property).toBeDefined();
      expect(property!.timezone).toBe('Europe/Istanbul');
      const enrollmentPage = (
        await reviewer.call<S<'EnrollmentPage'>>(
          'GET',
          `/api/v1/people/${me.person.id}/enrollments?limit=100`,
        )
      ).data;
      expect(enrollmentPage.nextCursor).toBeFalsy();
      const publishedNightFactorOne = async (serviceId: string, code: string, at: string) => {
        for (const enrollment of enrollmentPage.items.filter((item) => item.status === 'ACTIVE')) {
          const versions = (
            await reviewer.call<{ items: S<'PlanVersionSummary'>[] }>(
              'GET',
              `/api/v1/plans/${enrollment.planId}/versions`,
            )
          ).data.items;
          for (const version of versions.filter(
            (item) =>
              item.status === 'PUBLISHED' &&
              (!item.validFrom || item.validFrom <= at) &&
              (!item.validTo || item.validTo >= at),
          )) {
            const mappings = (
              await reviewer.call<{ items: S<'EntitlementMapping'>[] }>(
                'GET',
                `/api/v1/plan-versions/${version.id}/entitlement-mappings`,
              )
            ).data.items;
            if (
              mappings.some(
                (mapping) =>
                  mapping.serviceDefinitionId === serviceId &&
                  mapping.entitlementCode === code &&
                  mapping.unitType === 'NIGHT' &&
                  decimal(mapping.unitFactor) === 1_000_000n &&
                  (!mapping.validFrom || mapping.validFrom <= at) &&
                  (!mapping.validTo || mapping.validTo >= at),
              )
            )
              return true;
          }
        }
        return false;
      };
      let arrival = fixture.checkIn ?? day(0, property!.timezone);
      if (!fixture.bookingId) {
        if (!fixture.hold) {
          let body: HoldBody | undefined;
          for (const offset of [0, 1]) {
            const candidate = day(offset, property!.timezone);
            const prior = (
              await member.call<S<'BookingList'>>(
                'GET',
                `${root}/bookings?propertyId=${property!.id}&checkInFrom=${candidate}&checkInTo=${candidate}&limit=100`,
              )
            ).data;
            expect(
              prior.nextCursor,
              'a paged arrival list cannot prove safe fixture creation',
            ).toBeFalsy();
            expect(
              prior.items.filter(
                (b) => !['CANCELLED', 'EXPIRED', 'COMPLETED', 'NO_SHOW'].includes(b.status),
              ),
              'reuse the saved fixture; never create a second arrival',
            ).toEqual([]);
            const search = (
              await member.call<S<'AvailabilitySearchResult'>>(
                'POST',
                `${root}/availability/search`,
                {
                  propertyId: property!.id,
                  checkIn: candidate,
                  checkOut: day(offset + 3, property!.timezone),
                  adults: 2,
                  children: 0,
                },
              )
            ).data;
            const room = search.results.find(
              (r) => r.roomType.code === 'STD' && r.quote && r.available > 0,
            );
            if (
              !search.eligible ||
              search.nights !== 3 ||
              search.entitlement?.unit !== 'NIGHT' ||
              !room
            )
              continue;
            if (
              !(await publishedNightFactorOne(
                room.roomType.serviceDefinitionId,
                search.entitlement.entitlementCode,
                candidate,
              ))
            )
              continue;
            body = {
              roomTypeId: room.roomType.id,
              checkIn: search.checkIn,
              checkOut: search.checkOut,
              adults: 2,
              children: 0,
              channel: 'MEMBER_PORTAL',
            };
            arrival = candidate;
            break;
          }
          expect(
            body,
            'published DEMO_A data must offer a valid three-night NIGHT quote today or tomorrow',
          ).toBeDefined();
          fixture.checkIn = arrival;
          fixture.hold = {
            key: randomUUID(),
            state: 'pending',
            createdAt: new Date().toISOString(),
            body: body!,
          };
          await save();
        }
        expect(
          Date.now() - Date.parse(fixture.hold.createdAt),
          'an uncertain hold older than 24 hours needs manual reconciliation',
        ).toBeLessThan(23 * 3_600_000);
        const held = (
          await member.call<S<'Booking'>>('POST', `${root}/holds`, fixture.hold.body, {
            expected: 201,
            key: fixture.hold.key,
          })
        ).data;
        fixture.bookingId = held.id;
        fixture.hold!.state = 'done';
        await save();
      }
      const bookingPath = `${root}/bookings/${fixture.bookingId}`;
      const getBooking = async () => (await member.call<S<'Booking'>>('GET', bookingPath)).data;
      let booking = await getBooking();
      expect(booking.personId).toBe(me.person.id);
      expect(booking.propertyId).toBe(property!.id);
      expect(booking.checkIn).toBe(arrival);
      expect(booking.nights).toBe(3);
      expect(booking.quoteSnapshot.coveredNights).toBe(3);
      const enrollment = enrollmentPage.items.find((e) => e.id === booking.enrollmentId);
      expect(enrollment).toBeDefined();
      const versions = (
        await reviewer.call<{ items: S<'PlanVersionSummary'>[] }>(
          'GET',
          `/api/v1/plans/${enrollment!.planId}/versions`,
        )
      ).data.items;
      const published = versions.filter(
        (v) =>
          v.status === 'PUBLISHED' &&
          (!v.validFrom || v.validFrom <= arrival) &&
          (!v.validTo || v.validTo >= arrival),
      );
      expect(published, 'one applicable published version is required').toHaveLength(1);
      const mappings = (
        await reviewer.call<{ items: S<'EntitlementMapping'>[] }>(
          'GET',
          `/api/v1/plan-versions/${published[0]!.id}/entitlement-mappings`,
        )
      ).data.items;
      const mapping = mappings.find(
        (m) =>
          m.serviceDefinitionId === booking.quoteSnapshot.serviceDefinitionId &&
          m.unitType === 'NIGHT' &&
          (!m.validFrom || m.validFrom <= arrival) &&
          (!m.validTo || m.validTo >= arrival),
      );
      expect(mapping).toBeDefined();
      expect(decimal(mapping!.unitFactor)).toBe(1_000_000n);
      if (booking.status === 'HOLD') {
        const key = await intent('confirm');
        await member.call('POST', `${bookingPath}/confirm`, undefined, { key });
        fixture.confirm!.state = 'done';
        await save();
        booking = await getBooking();
      }
      if (booking.status === 'PENDING_APPROVAL') {
        const sourcePath = `/api/v1/service-requests/${booking.serviceRequestId}`;
        const source = await doctor.call<S<'ServiceRequest'>>('GET', sourcePath);
        expect(source.data.items).toHaveLength(1);
        expect(decimal(source.data.items[0]!.requestedQuantity)).toBe(3_000_000n);
        if (source.data.status === 'PENDING_REVIEW') {
          fixture.approve ??= {
            key: randomUUID(),
            etag: source.etag,
            state: 'pending',
            createdAt: new Date().toISOString(),
          };
          await save();
          await doctor.call<S<'ServiceRequest'>>(
            'POST',
            `${sourcePath}/partially-approve`,
            {
              reasonCode: 'PC06_CAP_NOSHOW',
              items: [
                {
                  lineNo: source.data.items[0]!.lineNo,
                  status: 'PARTIALLY_APPROVED',
                  approvedQuantity: '1.5',
                  decisionReasonCode: 'PC06_CAP_NOSHOW',
                },
              ],
            } satisfies S<'ServiceRequestDecision'>,
            { key: fixture.approve.key, etag: fixture.approve.etag },
          );
          fixture.approve.state = 'done';
          await save();
        } else {
          expect(source.data.status).toBe('PARTIALLY_APPROVED');
        }
        await expect
          .poll(async () => (await getBooking()).status, { timeout: 45_000 })
          .toBe('CONFIRMED');
        booking = await getBooking();
      }
      expect(['CONFIRMED', 'NO_SHOW']).toContain(booking.status);
      expect(booking.authorizationId).toBeTruthy();
      expect(booking.entitlementReservationId).toBeTruthy();
      const authorization = (
        await doctor.call<S<'Authorization'>>(
          'GET',
          `/api/v1/authorizations/${booking.authorizationId}`,
        )
      ).data;
      const item = authorization.items.find(
        (i) => i.entitlementReservationId === booking.entitlementReservationId,
      );
      expect(item).toBeDefined();
      expect(decimal(item!.approvedQuantity)).toBe(1_500_000n);
      expect(item!.serviceDefinitionId).toBe(mapping!.serviceDefinitionId);
      expect(booking.policySnapshot).toBeTruthy();
      expect(
        policyNights(booking.quoteSnapshot.coveredNights, booking.policySnapshot!.noShowPercent),
        'frozen policy must exceed 1.5 service nights',
      ).toBeGreaterThan(1.5);
      const accounts = async () =>
        (
          await member.call<{ items: S<'EntitlementAccount'>[] }>(
            'GET',
            `/api/v1/people/${me.person.id}/entitlements?asOf=${arrival}`,
          )
        ).data.items;
      const account = (await accounts()).find(
        (a) =>
          a.enrollmentId === booking.enrollmentId &&
          a.definition.id === mapping!.entitlementDefinitionId,
      );
      expect(account?.definition.unitType).toBe('NIGHT');
      const currentAccount = async () => (await accounts()).find((a) => a.id === account!.id)!;
      const lastNight = new Date(Date.parse(booking.checkOut + 'T12:00:00Z') - 86_400_000)
        .toISOString()
        .slice(0, 10);
      const inventory = async () =>
        (
          await desk.call<S<'RoomTypeInventoryRange'>>(
            'GET',
            `${root}/room-types/${booking.roomTypeId}/inventory?from=${arrival}&to=${lastNight}`,
          )
        ).data.days;
      if (!fixture.beforeAccount && booking.status === 'CONFIRMED') {
        fixture.beforeAccount = await currentAccount();
        fixture.beforeInventory = await inventory();
        await save();
      }
      expect(
        fixture.beforeAccount && fixture.beforeInventory,
        'confirmed pre-review baseline must be persisted',
      ).toBeTruthy();
      if (booking.status === 'CONFIRMED') {
        // An uncertain upload is reconciled by a human against the document list; never upload another file.
        if (fixture.uploadStarted && !fixture.documentId)
          throw new Error(
            'PENDING: document upload outcome unknown; reconcile the private fixture before resume',
          );
        if (!fixture.documentId) {
          await deskPage.goto(base + '/portal/lodging/desk');
          const row = deskPage.getByTestId('arrival-row').filter({ hasText: booking.reference });
          await expect(row).toBeVisible();
          await row.getByRole('button', { name: 'Gelmedi', exact: true }).click();
          const upload = row.getByTestId('no-show-form').getByTestId('document-upload-form');
          const bytes = syntheticPDF();
          fixture.documentSha256 = createHash('sha256').update(bytes).digest('hex');
          fixture.uploadStarted = true;
          await save();
          await upload.getByLabel(/Dosya se\u00e7/).setInputFiles({
            name: 'pc06-cap-noshow.pdf',
            mimeType: 'application/pdf',
            buffer: bytes,
          });
          await upload.getByLabel(/Belge t\u00fcr\u00fc/).selectOption('NO_SHOW_EVIDENCE');
          const uploaded = deskPage.waitForResponse(
            (r) =>
              new URL(r.url()).pathname === '/api/v1/documents' && r.request().method() === 'POST',
          );
          await upload.getByRole('button', { name: 'Belge y\u00fckle', exact: true }).click();
          const response = await uploaded;
          expect(response.status()).toBe(201);
          fixture.documentId = ((await response.json()) as { document: S<'Document'> }).document.id;
          await save();
        }
        await expect
          .poll(
            async () => {
              const current = (
                await desk.call<S<'Document'>>('GET', `/api/v1/documents/${fixture.documentId}`)
              ).data;
              return (
                current.downloadable &&
                current.links.some(
                  (link) =>
                    link.aggregateType === 'BOOKING' &&
                    link.aggregateId === booking.id &&
                    link.documentTypeCode === 'NO_SHOW_EVIDENCE',
                )
              );
            },
            {
              timeout: 45_000,
              message:
                'PENDING: reserved document must become downloadable and linked as NO_SHOW_EVIDENCE; reconcile this document before resuming',
            },
          )
          .toBe(true);
        const document = (
          await desk.call<S<'Document'>>('GET', `/api/v1/documents/${fixture.documentId}`)
        ).data;
        expect(document.sha256).toBe(fixture.documentSha256);
        expect(
          document.links.some(
            (l) =>
              l.aggregateType === 'BOOKING' &&
              l.aggregateId === booking.id &&
              l.documentTypeCode === 'NO_SHOW_EVIDENCE',
          ),
        ).toBe(true);
        const existing = await reviewer.call<S<'NoShowResult'> | S<'Problem'>>(
          'GET',
          `${bookingPath}/no-show`,
          undefined,
          { expected: [200, 404] },
        );
        if (existing.status === 404) {
          const key = await intent('report');
          const reported = await desk.call<S<'NoShowResult'> | S<'Problem'>>(
            'POST',
            `${bookingPath}/no-show`,
            { evidenceDocumentId: document.id },
            { expected: [201, 409], key },
          );
          if (reported.status === 409) {
            expect((reported.data as S<'Problem'>).code).toBe('NO_SHOW_TOO_EARLY');
            expect(await currentAccount()).toEqual(fixture.beforeAccount);
            expect(await inventory()).toEqual(fixture.beforeInventory);
            // This was a definitive refusal. A later valid command gets its own key.
            fixture.report = undefined;
            // This is only the documented 24-hour default-window estimate. The server's
            // effective tenant setting and next NO_SHOW_TOO_EARLY response remain authoritative.
            fixture.estimatedRetryAt = new Date(
              Date.parse(arrival + 'T00:00:00+03:00') + 24 * 3_600_000,
            ).toISOString();
            await save();
            test.skip(
              true,
              `PENDING natural no-show window; default-window retry estimate ${fixture.estimatedRetryAt}; server eligibility is authoritative`,
            );
          }
          const result = reported.data as S<'NoShowResult'>;
          fixture.reportId = result.report.id;
          fixture.fee = {
            assessed: result.report.assessedFeeAmount,
            payer: result.report.payerAmount,
            member: result.report.memberAmount,
            currency: result.report.currencyCode,
          };
          fixture.report!.state = 'done';
          await save();
        }
        const pending = (await reviewer.call<S<'NoShowResult'>>('GET', `${bookingPath}/no-show`))
          .data;
        expect(pending.report.status).toBe('REPORTED');
        expect(pending.report.evidenceDocumentId).toBe(document.id);
        expect(pending.report.entitlementEffect).toBeFalsy();
        expect(pending.report.consumedNights).toBe(0);
        fixture.feeBearingProof = decimal(pending.report.assessedFeeAmount) > 0n;
        expect(decimal(pending.report.assessedFeeAmount)).toBe(
          decimal(pending.report.payerAmount) + decimal(pending.report.memberAmount),
        );
        fixture.reportId = pending.report.id;
        fixture.fee = {
          assessed: pending.report.assessedFeeAmount,
          payer: pending.report.payerAmount,
          member: pending.report.memberAmount,
          currency: pending.report.currencyCode,
        };
        await save();
        expect(await currentAccount()).toEqual(fixture.beforeAccount);
        expect(await inventory()).toEqual(fixture.beforeInventory);
        const key = await intent('review');
        const reviewed = (
          await reviewer.call<S<'NoShowResult'>>(
            'POST',
            `${bookingPath}/no-show/review`,
            { status: 'CONFIRMED' },
            { key },
          )
        ).data;
        expect(reviewed.report.status).toBe('CONFIRMED');
        fixture.review!.state = 'done';
        await save();
      }
      const closed = (await reviewer.call<S<'NoShowResult'>>('GET', `${bookingPath}/no-show`)).data;
      expect(closed.report.id).toBe(fixture.reportId);
      expect(closed.report.status).toBe('CONFIRMED');
      expect(closed.booking.status).toBe('NO_SHOW');
      expect(closed.report.reviewedBy).not.toBe(closed.report.reportedByActorId);
      expect(closed.report.consumedNights).toBe(1);
      expect(closed.report.entitlementEffect).toEqual({
        consumedServiceNights: '1.5',
        releasedServiceNights: '0',
        consumedEntitlementUnits: '1.5',
        releasedEntitlementUnits: '0',
      });
      expect(decimal(closed.report.assessedFeeAmount)).toBe(decimal(fixture.fee!.assessed));
      expect(decimal(closed.report.payerAmount)).toBe(decimal(fixture.fee!.payer));
      expect(decimal(closed.report.memberAmount)).toBe(decimal(fixture.fee!.member));
      expect(closed.report.currencyCode).toBe(fixture.fee!.currency);
      fixture.feeBearingProof = decimal(closed.report.assessedFeeAmount) > 0n;
      await save();
      const after = await currentAccount();
      expect(decimal(after.available)).toBe(decimal(fixture.beforeAccount!.available));
      expect(decimal(after.reserved)).toBe(decimal(fixture.beforeAccount!.reserved) - 1_500_000n);
      expect(decimal(after.consumed)).toBe(decimal(fixture.beforeAccount!.consumed) + 1_500_000n);
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
      expect(movements.map((m) => m.movementType).sort()).toEqual([
        'CONSUME',
        'RELEASE',
        'RESERVE',
      ]);
      expect(movements.filter((m) => m.movementType === 'CONSUME')).toHaveLength(1);
      expect(movements.reduce((sum, m) => sum + decimal(m.deltaReserved), 0n)).toBe(0n);
      expect(movements.reduce((sum, m) => sum + decimal(m.deltaConsumed), 0n)).toBe(1_500_000n);
      const afterInventory = await inventory();
      expect(afterInventory).toHaveLength(3);
      afterInventory.forEach((row, index) => {
        const before = fixture.beforeInventory![index]!;
        expect(row.stayDate).toBe(before.stayDate);
        expect(row.confirmed).toBe(before.confirmed - 1);
        expect(row.available).toBe(before.available + 1);
      });
      const claims = async () => {
        const page = (
          await finance.call<S<'ClaimPage'>>(
            'GET',
            `/api/v1/claims?personId=${me.person.id}&providerOrganizationId=${property!.providerOrganizationId}&serviceDateFrom=${arrival}&serviceDateTo=${arrival}&limit=100`,
          )
        ).data;
        expect(page.nextCursor).toBeFalsy();
        return page.items.filter((c) => c.sourceType === 'BOOKING' && c.sourceId === booking.id);
      };
      if (fixture.feeBearingProof) {
        await expect.poll(async () => (await claims()).length, { timeout: 45_000 }).toBe(1);
        const claim = (
          await finance.call<S<'Claim'>>('GET', `/api/v1/claims/${(await claims())[0]!.id}`)
        ).data;
        expect(claim.lines).toHaveLength(1);
        expect(claim.lines[0]!.unitType).toBe('NO_SHOW_FEE');
        expect(decimal(claim.lines[0]!.lineAmount)).toBe(decimal(fixture.fee!.assessed));
        if (decimal(fixture.fee!.payer) > 0n) {
          expect(decimal(claim.lines[0]!.decision!.payerAmount)).toBe(decimal(fixture.fee!.payer));
        } else {
          expect(claim.lines[0]!.decision).toBeFalsy();
        }
      } else {
        expect(decimal(fixture.fee!.assessed)).toBe(0n);
        expect(decimal(fixture.fee!.payer)).toBe(0n);
        expect(decimal(fixture.fee!.member)).toBe(0n);
        expect(await claims()).toHaveLength(0);
      }
      if (fixture.report?.key) {
        const retained = Date.now() - Date.parse(fixture.report.createdAt) < 23 * 3_600_000;
        const replay = await desk.call<S<'NoShowResult'> | S<'Problem'>>(
          'POST',
          `${bookingPath}/no-show`,
          { evidenceDocumentId: fixture.documentId },
          { expected: retained ? 201 : 409, key: retained ? fixture.report.key : randomUUID() },
        );
        if (retained) expect((replay.data as S<'NoShowResult'>).report.id).toBe(closed.report.id);
      }
      if (fixture.review?.key) {
        const retained = Date.now() - Date.parse(fixture.review.createdAt) < 23 * 3_600_000;
        const replay = await reviewer.call<S<'NoShowResult'> | S<'Problem'>>(
          'POST',
          `${bookingPath}/no-show/review`,
          { status: 'CONFIRMED' },
          { expected: retained ? 200 : 409, key: retained ? fixture.review.key : randomUUID() },
        );
        if (retained) expect((replay.data as S<'NoShowResult'>).report.id).toBe(closed.report.id);
      }
      expect(
        (await reviewer.call<S<'NoShowResult'>>('GET', `${bookingPath}/no-show`)).data,
      ).toEqual(closed);
      expect(await currentAccount()).toEqual(after);
      expect(await inventory()).toEqual(afterInventory);
      expect(
        (
          await member.call<S<'LedgerPage'>>(
            'GET',
            `/api/v1/entitlement-accounts/${account!.id}/ledger?limit=100`,
          )
        ).data,
      ).toEqual(ledger);
      expect((await claims()).length).toBe(fixture.feeBearingProof ? 1 : 0);
    } finally {
      await Promise.allSettled([
        desk.close(),
        member.close(),
        doctor.close(),
        reviewer.close(),
        finance.close(),
      ]);
      await deskPage.close();
    }
  } finally {
    await lock.close();
    await rm(`${fixtureAbsolute}.lock`);
  }
});
