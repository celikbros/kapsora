/**
 * MSW handlers for the inpatient stay: the admission window, the one-open-stay rule, the
 * one-undecided-extension rule, the segments and the discharge that settles up.
 *
 * The mock is a test double of the Go server and M5 treats a divergence in either direction
 * as a bug. Five things here are transcriptions rather than re-implementations, because they
 * are the five a screen would be built wrongly against:
 *
 *   - **the window**: an admission dated more than `health.inpatient.backdate_days` back or
 *     more than `health.inpatient.future_days` forward is 422 ADMISSION_DATE_OUT_OF_WINDOW,
 *     with the same defaults of 3 and 30;
 *   - **one open stay per case and provider**: a second REQUESTED, AUTHORIZED or ADMITTED
 *     stay at the same provider is 409 INPATIENT_STAY_ALREADY_OPEN — and the second provider
 *     is allowed, because a transfer is a second admission;
 *   - **one undecided extension**: a second one while the first is REQUESTED is 409
 *     STAY_EXTENSION_PENDING;
 *   - **the segments**: two non-companion segments may not claim the same half-open hours,
 *     and a COMPANION may overlap anything, because a relative is in the room while the
 *     patient is;
 *   - **the discharge**: the actual day count is the elapsed time rounded up and never less
 *     than one, what was reserved and not used is released, and a stay that ran over is
 *     flagged rather than refused.
 *
 * As on the server, the projection is applied to the stored row before it becomes a body, in
 * `projectStay`, so no handler below can leak a field by forgetting one. A stay is exactly as
 * sensitive as the case it hangs off, and it asks `decideProjection` — the same function the
 * case and the report handlers ask.
 *
 * What the mock does not do is decide the admission. There is no approve endpoint here for
 * the same reason there is none on the server: a reviewer decides the request, and the stay
 * follows through an outbox event no browser ever sees. The seeded world therefore contains a
 * stay that has already been through that, and the mock's own `createInpatientStay` leaves
 * the stay REQUESTED — which is exactly what a screen has to be able to draw.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  type MockWorld,
  type StoredHealthCase,
  type StoredInpatientStay,
  type StoredStayExtension,
  type StoredStaySegment,
} from './data';
import type { MockApi, MockSession } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  organizationScope,
  parseIfMatch,
  parseLimit,
  pathParam,
  problem,
  readJson,
  requireIdempotencyKey,
  validationFailed,
  wait,
  withinScope,
  type FieldError,
  type Schemas,
} from './handlers';
import {
  NO_STORE,
  accessHeaderProblem,
  accessPurposeRequired,
  accessRequest,
  decideProjection,
  type AccessRequest,
  type Projection,
} from './health-handlers';

const PERMISSION_CASE_READ = 'health.case.read';
const PERMISSION_CASE_MANAGE = 'health.case.manage';

/** health.inpatient_stay.status, as migration 000033 writes it. */
const STAY_STATUSES = new Set<string>([
  'REQUESTED',
  'AUTHORIZED',
  'ADMITTED',
  'DISCHARGED',
  'CANCELLED',
  'REJECTED',
]);

/** The three the partial unique index calls open. */
const OPEN_STATUSES = new Set<string>(['REQUESTED', 'AUTHORIZED', 'ADMITTED']);

const SEGMENT_TYPES = new Set<string>(['WARD', 'ICU', 'SURGERY', 'OBSERVATION', 'COMPANION']);

/** The window a tenant that has configured nothing gets. */
const DEFAULT_BACKDATE_DAYS = 3;
const DEFAULT_FUTURE_DAYS = 30;

const MAX_DAYS = 365;
const MAX_SEGMENTS = 200;
const MAX_REASON_TEXT = 1000;
const REASON_CODE = /^[A-Z][A-Z0-9_.:-]{1,79}$/;
const SEGMENT_CODE = /^[A-Za-z0-9][A-Za-z0-9 ._/-]{0,31}$/;
const DAY_MS = 86_400_000;

function stayNotFound(api: MockApi): Response {
  return problem(api, 404, 'INPATIENT_STAY_NOT_FOUND', 'Yatış kaydı bulunamadı');
}

function transitionInvalid(api: MockApi): Response {
  return problem(
    api,
    409,
    'INPATIENT_STAY_TRANSITION_INVALID',
    'Yatış bu durumda bu işleme uygun değil',
  );
}

/** Whole days between two instants, rounded up and never less than one. */
function actualDays(admissionAt: string, dischargeAt: string): number {
  const elapsed = Date.parse(dischargeAt) - Date.parse(admissionAt);
  if (!(elapsed > 0)) return 1;
  return Math.max(1, Math.ceil(elapsed / DAY_MS));
}

/** Two half-open ranges overlap unless one ends at or before the other begins. */
function overlaps(
  a: { startsAt: string; endsAt: string | null },
  b: { startsAt: string; endsAt: string | null },
): boolean {
  if (a.endsAt !== null && Date.parse(a.endsAt) <= Date.parse(b.startsAt)) return false;
  if (b.endsAt !== null && Date.parse(b.endsAt) <= Date.parse(a.startsAt)) return false;
  return true;
}

export function inpatientHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  const caseOf = (row: StoredInpatientStay): StoredHealthCase | undefined =>
    world().healthCases.find((c) => c.id === row.caseId);

  /** A stay has no sensitivity of its own: it is as sensitive as its episode of care. */
  const sensitivityOf = (row: StoredInpatientStay): Schemas['HealthCaseSensitivity'] =>
    caseOf(row)?.sensitivity ?? 'STANDARD';

  const decide = (
    session: MockSession,
    tenantId: string,
    row: StoredInpatientStay,
    req: AccessRequest,
  ) => decideProjection(api, session, tenantId, sensitivityOf(row), req);

  const extensionsOf = (stayId: string): StoredStayExtension[] =>
    world()
      .stayExtensions.filter((e) => e.stayId === stayId)
      .sort((a, b) => a.sequenceNo - b.sequenceNo);

  const segmentsOf = (stayId: string): StoredStaySegment[] =>
    world()
      .staySegments.filter((g) => g.stayId === stayId)
      .sort((a, b) => a.startsAt.localeCompare(b.startsAt) || a.id.localeCompare(b.id));

  /**
   * The projection, applied to the row before it becomes a body. A clinical field is omitted
   * rather than sent as null, because the server omits it: a screen that distinguished the
   * two would work against one and not the other.
   */
  const projectStay = (
    row: StoredInpatientStay,
    projection: Projection,
  ): Schemas['InpatientStay'] => {
    const clinical = projection === 'CLINICAL';
    return {
      id: row.id,
      caseId: row.caseId,
      personId: row.personId,
      providerOrganizationId: row.providerOrganizationId,
      locationId: row.locationId,
      attendingPractitionerId: row.attendingPractitionerId,
      admissionAt: row.admissionAt,
      estimatedDays: row.estimatedDays,
      expectedDischargeAt: row.expectedDischargeAt,
      dischargeAt: row.dischargeAt,
      status: row.status,
      projection,
      serviceRequestId: row.serviceRequestId,
      authorizationId: row.authorizationId,
      // The one field the financial projection drops: that an admission has a recorded
      // diagnosis at all is a fact about the patient.
      ...(clinical ? { admissionDiagnosisId: row.admissionDiagnosisId } : {}),
      authorizedDays: row.authorizedDays,
      actualDays: row.actualDays,
      releasedDays: row.releasedDays,
      overAuthorization: row.overAuthorization,
      cancelReasonCode: row.cancelReasonCode,
      extensions: extensionsOf(row.id).map((e) => ({
        id: e.id,
        stayId: e.stayId,
        sequenceNo: e.sequenceNo,
        additionalDays: e.additionalDays,
        reasonCode: e.reasonCode,
        // The reason text is clinical: "solunum sıkıntısı devam ediyor" is a diagnosis in a
        // sentence, whatever the day count beside it says.
        ...(clinical ? { reasonText: e.reasonText } : {}),
        serviceRequestId: e.serviceRequestId,
        authorizationId: e.authorizationId,
        status: e.status,
        createdAt: e.createdAt,
        rowVersion: e.rowVersion,
      })),
      // Segments survive both projections: they are what a claim is priced from.
      segments: segmentsOf(row.id).map((g) => ({
        id: g.id,
        stayId: g.stayId,
        segmentType: g.segmentType,
        startsAt: g.startsAt,
        endsAt: g.endsAt,
        roomCode: g.roomCode,
        bedCode: g.bedCode,
        createdAt: g.createdAt,
        rowVersion: g.rowVersion,
      })),
      createdAt: row.createdAt,
      rowVersion: row.rowVersion,
    };
  };

  /** The access event a clinical read of a stay owes, with resource type INPATIENT_STAY. */
  const recordAccess = (
    session: MockSession,
    tenantId: string,
    row: StoredInpatientStay,
    accessType: Schemas['HealthAccessEvent']['accessType'],
    req: AccessRequest,
    outcome: Schemas['HealthAccessEvent']['outcome'],
  ): void => {
    world().healthAccessEvents.push({
      tenantId,
      id: world().nextId(),
      occurredAt: new Date().toISOString(),
      actorId: session.account.actorId,
      membershipId: null,
      personId: row.personId,
      resourceType: 'inpatient_stay',
      resourceId: row.id,
      accessType,
      purposeCode: req.purposeCode === '' ? null : req.purposeCode,
      reasonText: req.reasonText === '' ? null : req.reasonText,
      outcome,
    });
  };

  const visible = (session: MockSession, tenantId: string, row: StoredInpatientStay): boolean => {
    const scope = organizationScope(api, session, tenantId);
    return scope === null ? true : withinScope(scope, row.providerOrganizationId);
  };

  const findStay = (
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredInpatientStay | undefined => {
    const row = world().inpatientStays.find((r) => r.id === id && r.tenantId === tenantId);
    if (!row) return undefined;
    return visible(session, tenantId, row) ? row : undefined;
  };

  /** The body a command answers with: read back, projected, and recording nothing. */
  const answer = (
    session: MockSession,
    tenantId: string,
    row: StoredInpatientStay,
    status = 200,
  ): Response =>
    HttpResponse.json(
      projectStay(
        row,
        decide(session, tenantId, row, {
          purposeCode: '',
          reasonText: '',
          financialOnly: false,
          projectionInvalid: false,
        }).projection,
      ),
      { status, headers: { ...NO_STORE, ETag: etagOf(row.rowVersion) } },
    );

  /**
   * The read every command makes before it writes: the stay, the caller's boundary, the
   * lifecycle and the If-Match. The transition table is the same one the Go domain holds.
   */
  const forCommand = (
    request: Request,
    session: MockSession,
    tenantId: string,
    id: string,
    from: string[],
  ): { row: StoredInpatientStay } | { error: Response } => {
    const row = findStay(session, tenantId, id);
    if (!row) return { error: stayNotFound(api) };
    const expected = parseIfMatch(request.headers.get('If-Match'));
    if (expected === null || expected < 1) {
      return {
        error: problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli', {
          detail: 'GET yanıtındaki ETag değerini If-Match olarak gönderin.',
        }),
      };
    }
    if (!from.includes(row.status)) return { error: transitionInvalid(api) };
    if (row.rowVersion !== expected) {
      return {
        error: problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti', {
          detail: 'Güncel sürümü alıp değişikliğinizi yeniden uygulayın.',
        }),
      };
    }
    return { row };
  };

  return [
    http.get(`${ANY}/api/v1/inpatient-stays`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_READ, false);
      if ('error' in g) return g.error;
      const req = accessRequest(request);
      const badAccess = accessHeaderProblem(api, req);
      if (badAccess) return badAccess;

      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) {
        return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      }
      const status = url.searchParams.get('status');
      if (status !== null && !STAY_STATUSES.has(status)) {
        return validationFailed(api, [
          { field: 'status', code: 'ENUM', message: 'geçerli bir durum olmalı' },
        ]);
      }
      const caseId = url.searchParams.get('caseId');
      const personId = url.searchParams.get('personId');
      const providerOrganizationId = url.searchParams.get('providerOrganizationId');
      const admittedFrom = url.searchParams.get('admittedFrom');
      const admittedTo = url.searchParams.get('admittedTo');

      const rows = world()
        .inpatientStays.filter(
          (r) =>
            r.tenantId === g.tenantId &&
            visible(g.session, g.tenantId, r) &&
            (caseId === null || r.caseId === caseId) &&
            (personId === null || r.personId === personId) &&
            (providerOrganizationId === null ||
              r.providerOrganizationId === providerOrganizationId) &&
            (status === null || r.status === status) &&
            (admittedFrom === null || r.admissionAt >= admittedFrom) &&
            (admittedTo === null || r.admissionAt < admittedTo),
        )
        .sort((a, b) => b.admissionAt.localeCompare(a.admissionAt) || b.id.localeCompare(a.id));

      const page = rows.slice(offset, offset + limit);
      const items = page.map((row) => {
        // A list never answers 428 and never records a refusal: refusing a page over one
        // sensitive row would say which row is sensitive.
        const d = decide(g.session, g.tenantId, row, req);
        if (d.projection === 'CLINICAL') {
          recordAccess(g.session, g.tenantId, row, 'SEARCH', req, 'SUCCESS');
        }
        return projectStay(row, d.projection);
      });
      const body: Schemas['InpatientStayPage'] = {
        items,
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/inpatient-stays/:stayId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_READ, false);
      if ('error' in g) return g.error;
      const req = accessRequest(request);
      const badAccess = accessHeaderProblem(api, req);
      if (badAccess) return badAccess;

      const row = findStay(g.session, g.tenantId, pathParam(params, 'stayId'));
      if (!row) return stayNotFound(api);
      const d = decide(g.session, g.tenantId, row, req);
      if (d.purposeMissing) {
        // The refusal is a row too: "who tried" is as much of the record as "who looked".
        recordAccess(g.session, g.tenantId, row, 'VIEW', req, 'DENIED');
        return accessPurposeRequired(api);
      }
      if (d.projection === 'CLINICAL') {
        recordAccess(g.session, g.tenantId, row, 'VIEW', req, 'SUCCESS');
      } else if (d.refusedSensitive) {
        recordAccess(g.session, g.tenantId, row, 'VIEW', req, 'DENIED');
      }
      return HttpResponse.json(projectStay(row, d.projection), {
        headers: { ...NO_STORE, ETag: etagOf(row.rowVersion) },
      });
    }),

    http.get(
      `${ANY}/api/v1/inpatient-stays/:stayId/reconciliation`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_CASE_READ, false);
        if ('error' in g) return g.error;
        const row = findStay(g.session, g.tenantId, pathParam(params, 'stayId'));
        if (!row) return stayNotFound(api);
        if (row.status !== 'DISCHARGED' || row.dischargeAt === null) {
          return problem(
            api,
            409,
            'INPATIENT_STAY_NOT_DISCHARGED',
            'Yatış henüz taburcu edilmedi',
            { detail: 'Mutabakat yalnızca taburcu edilmiş bir yatış için hesaplanır.' },
          );
        }
        const body: Schemas['StayReconciliation'] = {
          stayId: row.id,
          authorizationId: row.authorizationId,
          admissionAt: row.admissionAt,
          dischargeAt: row.dischargeAt,
          authorizedDays: row.authorizedDays ?? '0',
          actualDays: row.actualDays ?? '0',
          releasedDays: row.releasedDays ?? '0',
          overAuthorization: row.overAuthorization,
        };
        return HttpResponse.json(body, { headers: NO_STORE });
      },
    ),

    http.post(`${ANY}/api/v1/inpatient-stays`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_MANAGE, false);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const body = await readJson<Schemas['CreateInpatientStay']>(request);
      if (body === null) {
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      }

      const errors: FieldError[] = [];
      const caseId = (body.caseId ?? '').trim();
      const providerOrganizationId = (body.providerOrganizationId ?? '').trim();
      if (caseId === '') {
        errors.push({ field: 'caseId', code: 'REQUIRED', message: 'vaka zorunlu' });
      }
      if (providerOrganizationId === '') {
        errors.push({
          field: 'providerOrganizationId',
          code: 'REQUIRED',
          message: 'sağlayıcı kurumu zorunlu',
        });
      }
      const admissionAt = (body.admissionAt ?? '').trim();
      if (admissionAt === '' || Number.isNaN(Date.parse(admissionAt))) {
        errors.push({ field: 'admissionAt', code: 'REQUIRED', message: 'yatış zamanı zorunlu' });
      }
      const estimatedDays = Number(body.estimatedDays ?? 0);
      if (!Number.isInteger(estimatedDays) || estimatedDays < 1 || estimatedDays > MAX_DAYS) {
        errors.push({
          field: 'estimatedDays',
          code: 'RANGE',
          message: `tahmini yatış süresi 1 ile ${MAX_DAYS} gün arasında olmalı`,
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const episode = world().healthCases.find((c) => c.id === caseId && c.tenantId === g.tenantId);
      if (!episode) return problem(api, 404, 'HEALTH_CASE_NOT_FOUND', 'Sağlık vakası bulunamadı');
      if (episode.status === 'CLOSED') {
        return problem(api, 409, 'HEALTH_CASE_CLOSED', 'Sağlık vakası kapalı', {
          detail: 'Kapanmış bir vakaya yatış eklenemez.',
        });
      }

      // The window, with the tenant's own limits and the documented defaults. Whole days,
      // because a stay entered at 23:00 three days ago and one entered at 01:00 three days
      // ago are both "three days ago" to the person entering them.
      const now = Date.now();
      const startOfToday = Math.floor(now / DAY_MS) * DAY_MS;
      const earliest = startOfToday - DEFAULT_BACKDATE_DAYS * DAY_MS;
      const latest = startOfToday + (DEFAULT_FUTURE_DAYS + 1) * DAY_MS;
      const admission = Date.parse(admissionAt);
      if (admission < earliest || admission >= latest) {
        return problem(
          api,
          422,
          'ADMISSION_DATE_OUT_OF_WINDOW',
          'Yatış tarihi izin verilen aralığın dışında',
          {
            detail: 'Geriye ve ileriye dönük yatış tarihi sınırları tenant ayarlarında belirlenir.',
          },
        );
      }

      // One open stay per case and provider. The server leaves this to a partial unique
      // index; here it is the same three statuses, checked in the same place.
      const open = world().inpatientStays.find(
        (r) =>
          r.tenantId === g.tenantId &&
          r.caseId === caseId &&
          r.providerOrganizationId === providerOrganizationId &&
          OPEN_STATUSES.has(r.status),
      );
      if (open) {
        return problem(
          api,
          409,
          'INPATIENT_STAY_ALREADY_OPEN',
          'Bu vakada bu sağlayıcıda açık bir yatış var',
          { detail: 'Yeni yatış açmadan önce mevcut yatışı taburcu edin ya da iptal edin.' },
        );
      }

      const nowIso = new Date().toISOString();
      const row: StoredInpatientStay = {
        id: world().nextId(),
        tenantId: g.tenantId,
        caseId,
        personId: episode.personId,
        providerOrganizationId,
        locationId: body.locationId ?? null,
        attendingPractitionerId: body.attendingPractitionerId ?? null,
        admissionAt: new Date(admission).toISOString(),
        estimatedDays,
        expectedDischargeAt: new Date(admission + estimatedDays * DAY_MS).toISOString(),
        dischargeAt: null,
        // REQUESTED, always. The stay becomes AUTHORIZED when a reviewer decides its
        // preauthorization request, and no browser ever gives that command.
        status: 'REQUESTED',
        serviceRequestId: world().nextId(),
        authorizationId: null,
        admissionDiagnosisId: body.admissionDiagnosisId ?? null,
        authorizedDays: null,
        actualDays: null,
        releasedDays: null,
        overAuthorization: false,
        cancelReasonCode: null,
        createdAt: nowIso,
        rowVersion: 1,
      };
      world().inpatientStays.push(row);
      return answer(g.session, g.tenantId, row, 201);
    }),

    http.post(`${ANY}/api/v1/inpatient-stays/:stayId/extensions`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_MANAGE, false);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const found = forCommand(request, g.session, g.tenantId, pathParam(params, 'stayId'), [
        'AUTHORIZED',
        'ADMITTED',
      ]);
      if ('error' in found) return found.error;
      const body = await readJson<Schemas['ExtendInpatientStay']>(request);
      if (body === null) {
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      }

      const errors: FieldError[] = [];
      const additionalDays = Number(body.additionalDays ?? 0);
      if (!Number.isInteger(additionalDays) || additionalDays < 1 || additionalDays > MAX_DAYS) {
        errors.push({
          field: 'additionalDays',
          code: 'RANGE',
          message: `ek gün sayısı 1 ile ${MAX_DAYS} arasında olmalı`,
        });
      }
      const reasonCode = (body.reasonCode ?? '').trim();
      if (!REASON_CODE.test(reasonCode)) {
        errors.push({ field: 'reasonCode', code: 'FORMAT', message: 'geçersiz kod biçimi' });
      }
      if ((body.reasonText ?? '').length > MAX_REASON_TEXT) {
        errors.push({ field: 'reasonText', code: 'LENGTH', message: 'en fazla 1000 karakter' });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const existing = extensionsOf(found.row.id);
      if (existing.some((e) => e.status === 'REQUESTED')) {
        return problem(
          api,
          409,
          'STAY_EXTENSION_PENDING',
          'Karara bağlanmamış bir uzatma talebi var',
          {
            detail: 'Yeni uzatma istemeden önce bekleyen uzatmanın sonuçlanmasını bekleyin.',
          },
        );
      }
      world().stayExtensions.push({
        id: world().nextId(),
        tenantId: g.tenantId,
        stayId: found.row.id,
        sequenceNo: existing.reduce((max, e) => Math.max(max, e.sequenceNo), 0) + 1,
        additionalDays,
        reasonCode,
        reasonText: (body.reasonText ?? '').trim() === '' ? null : body.reasonText!.trim(),
        serviceRequestId: world().nextId(),
        authorizationId: null,
        status: 'REQUESTED',
        createdAt: new Date().toISOString(),
        rowVersion: 1,
      });
      found.row.rowVersion += 1;
      return answer(g.session, g.tenantId, found.row);
    }),

    http.put(`${ANY}/api/v1/inpatient-stays/:stayId/segments`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_MANAGE, false);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const found = forCommand(request, g.session, g.tenantId, pathParam(params, 'stayId'), [
        'AUTHORIZED',
        'ADMITTED',
      ]);
      if ('error' in found) return found.error;
      const body = await readJson<Schemas['PutStaySegments']>(request);
      if (body === null || !Array.isArray(body.items)) {
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      }

      const items = body.items;
      if (items.length > MAX_SEGMENTS) {
        return validationFailed(api, [
          { field: 'items', code: 'RANGE', message: `en fazla ${MAX_SEGMENTS} segment` },
        ]);
      }
      const errors: FieldError[] = [];
      items.forEach((item, index) => {
        const path = `items[${index}]`;
        if (!SEGMENT_TYPES.has(item.segmentType)) {
          errors.push({
            field: `${path}.segmentType`,
            code: 'ENUM',
            message: 'geçerli bir segment türü olmalı',
          });
        }
        if (!item.startsAt || Number.isNaN(Date.parse(item.startsAt))) {
          errors.push({
            field: `${path}.startsAt`,
            code: 'REQUIRED',
            message: 'başlangıç zamanı zorunlu',
          });
          return;
        }
        if (item.endsAt && Date.parse(item.endsAt) <= Date.parse(item.startsAt)) {
          errors.push({
            field: `${path}.endsAt`,
            code: 'RANGE',
            message: 'bitiş zamanı başlangıçtan sonra olmalı',
          });
        }
        if (Date.parse(item.startsAt) < Date.parse(found.row.admissionAt)) {
          errors.push({
            field: `${path}.startsAt`,
            code: 'RANGE',
            message: 'segment yatış zamanından önce başlayamaz',
          });
        }
        for (const [field, value] of [
          ['roomCode', item.roomCode],
          ['bedCode', item.bedCode],
        ] as const) {
          if (value !== undefined && value !== null && !SEGMENT_CODE.test(value)) {
            errors.push({ field: `${path}.${field}`, code: 'FORMAT', message: 'geçersiz kod' });
          }
        }
      });
      // The exclusion constraint, as the caller meets it: which two lines collided. A
      // COMPANION is skipped on both sides, because a relative is in the room while the
      // patient is.
      items.forEach((a, i) => {
        if (a.segmentType === 'COMPANION' || !a.startsAt) return;
        for (let j = 0; j < i; j += 1) {
          const b = items[j]!;
          if (b.segmentType === 'COMPANION' || !b.startsAt) continue;
          if (
            overlaps(
              { startsAt: a.startsAt, endsAt: a.endsAt ?? null },
              { startsAt: b.startsAt, endsAt: b.endsAt ?? null },
            )
          ) {
            errors.push({
              field: `items[${i}].startsAt`,
              code: 'OVERLAP',
              message: `bu segment items[${j}] ile çakışıyor`,
            });
            break;
          }
        }
      });
      if (errors.length > 0) return validationFailed(api, errors);

      const nowIso = new Date().toISOString();
      const kept = world().staySegments.filter((row) => row.stayId !== found.row.id);
      kept.push(
        ...items.map((item) => ({
          id: world().nextId(),
          tenantId: g.tenantId,
          stayId: found.row.id,
          segmentType: item.segmentType,
          startsAt: new Date(Date.parse(item.startsAt)).toISOString(),
          endsAt: item.endsAt ? new Date(Date.parse(item.endsAt)).toISOString() : null,
          roomCode: item.roomCode ?? null,
          bedCode: item.bedCode ?? null,
          createdAt: nowIso,
          rowVersion: 1,
        })),
      );
      world().staySegments.length = 0;
      world().staySegments.push(...kept);
      // Recording where somebody actually is *is* the admission.
      if (found.row.status === 'AUTHORIZED') found.row.status = 'ADMITTED';
      found.row.rowVersion += 1;
      return answer(g.session, g.tenantId, found.row);
    }),

    http.post(`${ANY}/api/v1/inpatient-stays/:stayId/discharge`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_MANAGE, false);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const found = forCommand(request, g.session, g.tenantId, pathParam(params, 'stayId'), [
        'AUTHORIZED',
        'ADMITTED',
      ]);
      if ('error' in found) return found.error;
      const body = (await readJson<Schemas['DischargeInpatientStay']>(request)) ?? {};

      const dischargeAt = body.dischargeAt ?? new Date().toISOString();
      if (Number.isNaN(Date.parse(dischargeAt))) {
        return validationFailed(api, [
          { field: 'dischargeAt', code: 'FORMAT', message: 'RFC3339 biçiminde olmalı' },
        ]);
      }
      if (Date.parse(dischargeAt) < Date.parse(found.row.admissionAt)) {
        return validationFailed(api, [
          { field: 'dischargeAt', code: 'RANGE', message: 'taburcu zamanı yatıştan önce olamaz' },
        ]);
      }
      if (Date.parse(dischargeAt) > Date.now()) {
        return validationFailed(api, [
          { field: 'dischargeAt', code: 'RANGE', message: 'taburcu zamanı gelecekte olamaz' },
        ]);
      }

      const actual = actualDays(found.row.admissionAt, dischargeAt);
      const authorized = Number(found.row.authorizedDays ?? '0');
      const released = Math.max(0, authorized - actual);
      // A stay that ran over releases nothing and is flagged instead, for the claim to raise
      // as an exception.
      found.row.overAuthorization = authorized > 0 && actual > authorized;
      found.row.actualDays = String(actual);
      found.row.releasedDays = String(released);
      found.row.dischargeAt = new Date(Date.parse(dischargeAt)).toISOString();
      found.row.status = 'DISCHARGED';
      found.row.rowVersion += 1;
      // Every segment nobody ended, ended: one left open past the discharge would price a
      // night the patient was not there for.
      for (const segment of world().staySegments) {
        if (segment.stayId === found.row.id && segment.endsAt === null) {
          segment.endsAt = found.row.dischargeAt;
          segment.rowVersion += 1;
        }
      }
      return answer(g.session, g.tenantId, found.row);
    }),

    http.post(`${ANY}/api/v1/inpatient-stays/:stayId/cancel`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_MANAGE, false);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const found = forCommand(request, g.session, g.tenantId, pathParam(params, 'stayId'), [
        'REQUESTED',
        'AUTHORIZED',
        'ADMITTED',
      ]);
      if ('error' in found) return found.error;
      const body = await readJson<Schemas['CancelInpatientStay']>(request);
      if (body === null) {
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      }
      const reasonCode = (body.reasonCode ?? '').trim();
      if (!REASON_CODE.test(reasonCode)) {
        return validationFailed(api, [
          { field: 'reasonCode', code: 'FORMAT', message: 'geçersiz kod biçimi' },
        ]);
      }
      // Every undecided extension goes with the admission: one outliving the stay it
      // extends would be a request nobody can decide against anything.
      for (const extension of world().stayExtensions) {
        if (extension.stayId === found.row.id && extension.status === 'REQUESTED') {
          extension.status = 'CANCELLED';
          extension.rowVersion += 1;
        }
      }
      found.row.status = 'CANCELLED';
      found.row.cancelReasonCode = reasonCode;
      found.row.rowVersion += 1;
      return answer(g.session, g.tenantId, found.row);
    }),
  ];
}
