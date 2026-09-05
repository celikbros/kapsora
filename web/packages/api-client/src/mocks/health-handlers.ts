/**
 * MSW handlers for the M5 health case: the case, its encounters, their diagnoses, and the
 * clinical access log.
 *
 * The mock is a test double of the Go server, and M4 found divergences in both directions
 * and treats each as a bug. This module is the one where a divergence would be worst: if the
 * mock served a diagnosis to an actor the server refuses, a screen would be built against a
 * body that never arrives — and if it refused one the server serves, the screen would be
 * built to hide something the API had already sent.
 *
 * So the rule lives in exactly one function here, `decide`, and it is a line-for-line
 * transcription of internal/health/application/projection.go:
 *
 *   - no health.clinical.read at all -> the financial projection, whatever the case is;
 *   - a STANDARD case with the clinical grant -> the clinical projection;
 *   - a SENSITIVE case without health.sensitive.read -> the financial projection again,
 *     never a refusal, because a refusal would itself say the case is sensitive;
 *   - a SENSITIVE case with the grant and no X-Access-Purpose -> 428 on a single read;
 *   - a SENSITIVE case with the grant and a purpose -> the clinical projection, recorded.
 *
 * And the projection is applied to the stored row before it is answered, in `projectCase`
 * and `projectEncounter`, so no handler below can leak a field by forgetting one.
 *
 * `listEncounterDiagnoses` is the endpoint that is a refusal rather than a narrower body:
 * an empty list would tell the caller the encounter has no diagnosis, which is a clinical
 * fact it may not have.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  type MockWorld,
  type StoredDiagnosis,
  type StoredEncounter,
  type StoredHealthCase,
} from './data';
import type { MockApi, MockSession } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  hasPermission,
  organizationScope,
  parseLimit,
  parseIfMatch,
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

const PERMISSION_CASE_READ = 'health.case.read';
const PERMISSION_CASE_MANAGE = 'health.case.manage';
const PERMISSION_CLINICAL_READ = 'health.clinical.read';
const PERMISSION_SENSITIVE_READ = 'health.sensitive.read';
const PERMISSION_AUDIT_READ = 'audit.read';

const CASE_TYPES = new Set<string>(['OUTPATIENT', 'INPATIENT', 'CHRONIC', 'MATERNITY', 'OTHER']);
const CASE_STATUSES = new Set<string>(['OPEN', 'CLOSED']);
const ENCOUNTER_TYPES = new Set<string>(['OUTPATIENT', 'INPATIENT', 'EMERGENCY', 'TELEHEALTH']);
const DIAGNOSIS_TYPES = new Set<string>(['PRIMARY', 'SECONDARY', 'SUSPECTED']);
/** health.clinical_access_purpose, as migration 000031 seeds it. */
export const ACCESS_PURPOSES = new Set<string>([
  'TREATMENT',
  'PRE_AUTHORIZATION',
  'CLAIM_REVIEW',
  'MEDICAL_REVIEW',
  'AUDIT',
  'MEMBER_REQUEST',
]);
const MAX_DIAGNOSES = 50;
const MAX_NOTES = 4000;
export const MAX_ACCESS_REASON = 200;
const MAX_CLOSE_REASON = 200;
const BRANCH_CODE = /^[A-Z][A-Z0-9_.-]{0,63}$/;

/** A clinical body is not something an intermediary should keep a copy of. */
export const NO_STORE = { 'Cache-Control': 'no-store' } as const;

export type Projection = Schemas['HealthProjection'];

/** Why the caller is opening clinical data, as the two headers state it. */
export interface AccessRequest {
  purposeCode: string;
  reasonText: string;
}

/**
 * The reason is percent-decoded, because an HTTP header value is ISO-8859-1 and a browser
 * refuses to send one containing ğ, ş or ı. The contract says the value is percent-encoded
 * UTF-8 and the server decodes it the same way; a value with no percent sequences decodes
 * to itself, so a plain ASCII reason is untouched.
 */
function decodeReason(raw: string): string {
  const trimmed = raw.trim();
  try {
    return decodeURIComponent(trimmed).trim();
  } catch {
    return trimmed;
  }
}

export function accessRequest(request: Request): AccessRequest {
  return {
    purposeCode: (request.headers.get('X-Access-Purpose') ?? '').trim(),
    reasonText: decodeReason(request.headers.get('X-Access-Reason') ?? ''),
  };
}

export interface Decision {
  projection: Projection;
  /** The clinical half was withheld from a caller that holds the clinical grant. */
  refusedSensitive: boolean;
  /** A single read must answer 428 rather than narrow. */
  purposeMissing: boolean;
}

function caseNotFound(api: MockApi): Response {
  return problem(api, 404, 'HEALTH_CASE_NOT_FOUND', 'Sağlık vakası bulunamadı');
}

function encounterNotFound(api: MockApi): Response {
  return problem(api, 404, 'ENCOUNTER_NOT_FOUND', 'Encounter bulunamadı');
}

export function clinicalReadRequired(api: MockApi): Response {
  return problem(api, 403, 'CLINICAL_READ_REQUIRED', 'Klinik detay için ek yetki gerekiyor', {
    detail: 'Tanı bilgisini görmek için klinik okuma yetkisi gerekir.',
  });
}

export function accessPurposeRequired(api: MockApi): Response {
  return problem(api, 428, 'ACCESS_PURPOSE_REQUIRED', 'Erişim amacı belirtilmeli', {
    detail: 'Bu kaydı görüntülemek için X-Access-Purpose başlığıyla erişim amacınızı bildirin.',
  });
}

/**
 * The one place the visibility rules live, transcribed from `decide` in
 * internal/health/application/projection.go. Every handler of this module and of
 * medical-report-handlers.ts asks this and nothing else, so the mock has one rule to keep in
 * step with the server rather than nineteen — and a treatment report is exactly as sensitive
 * as the episode of care it belongs to, decided by the same function.
 *
 * It takes the sensitivity rather than the row so a report, which has no sensitivity of its
 * own, can hand it the sensitivity of its case.
 */
/**
 * The two access headers, checked against the reference migration 000031 seeds and the
 * length the audit sanitiser will accept. Shared with the report handlers, so a purpose the
 * server rejects is rejected in both places by one list.
 */
export function accessHeaderErrors(req: AccessRequest): FieldError[] {
  const errors: FieldError[] = [];
  if (req.purposeCode !== '' && !ACCESS_PURPOSES.has(req.purposeCode)) {
    errors.push({
      field: 'X-Access-Purpose',
      code: 'ENUM',
      message: 'tanımlı bir erişim amacı olmalı',
    });
  }
  if ([...req.reasonText].length > MAX_ACCESS_REASON) {
    errors.push({
      field: 'X-Access-Reason',
      code: 'LENGTH',
      message: 'en fazla 200 karakter',
    });
  }
  return errors;
}

export function decideProjection(
  api: MockApi,
  session: MockSession,
  tenantId: string,
  sensitivity: Schemas['HealthCaseSensitivity'],
  req: AccessRequest,
): Decision {
  if (!hasPermission(api, session, tenantId, PERMISSION_CLINICAL_READ)) {
    return { projection: 'FINANCIAL', refusedSensitive: false, purposeMissing: false };
  }
  if (sensitivity !== 'SENSITIVE') {
    return { projection: 'CLINICAL', refusedSensitive: false, purposeMissing: false };
  }
  if (!hasPermission(api, session, tenantId, PERMISSION_SENSITIVE_READ)) {
    return { projection: 'FINANCIAL', refusedSensitive: true, purposeMissing: false };
  }
  if (req.purposeCode === '') {
    return { projection: 'FINANCIAL', refusedSensitive: true, purposeMissing: true };
  }
  return { projection: 'CLINICAL', refusedSensitive: false, purposeMissing: false };
}

export function healthHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  const decide = (
    session: MockSession,
    tenantId: string,
    row: StoredHealthCase,
    req: AccessRequest,
  ): Decision => decideProjection(api, session, tenantId, row.sensitivity, req);

  /**
   * The projection, applied to the row before it becomes a body. The financial projection
   * omits the field rather than sending it as null, because the server omits it: a screen
   * that distinguished the two would work against one and not the other.
   */
  const projectCase = (
    row: StoredHealthCase,
    encounters: StoredEncounter[],
    projection: Projection,
  ): Schemas['HealthCase'] => ({
    id: row.id,
    personId: row.personId,
    programId: row.programId,
    enrollmentId: row.enrollmentId,
    caseType: row.caseType,
    providerOrganizationId: row.providerOrganizationId,
    openedAt: row.openedAt,
    closedAt: row.closedAt,
    status: row.status,
    serviceRequestId: row.serviceRequestId,
    projection,
    ...(projection === 'CLINICAL' ? { sensitivity: row.sensitivity } : {}),
    encounters: encounters.map((e) => projectEncounter(e, projection)),
    createdAt: row.createdAt,
    rowVersion: row.rowVersion,
  });

  const projectEncounter = (
    row: StoredEncounter,
    projection: Projection,
  ): Schemas['Encounter'] => ({
    id: row.id,
    caseId: row.caseId,
    encounterType: row.encounterType,
    startedAt: row.startedAt,
    endedAt: row.endedAt,
    locationId: row.locationId,
    practitionerId: row.practitionerId,
    projection,
    ...(projection === 'CLINICAL'
      ? { branchCode: row.branchCode, notesClinical: row.notesClinical }
      : {}),
    createdAt: row.createdAt,
    rowVersion: row.rowVersion,
  });

  const toDiagnosis = (row: StoredDiagnosis): Schemas['Diagnosis'] => {
    const value = world().codeValues.find((v) => v.id === row.codeValueId)!;
    const system = world().codeSystems.find((sy) => sy.id === row.codeSystemId)!;
    return {
      id: row.id,
      encounterId: row.encounterId,
      codeSystemId: row.codeSystemId,
      codeSystemCode: system.code,
      codeValueId: row.codeValueId,
      code: value.code,
      display: value.display,
      diagnosisType: row.diagnosisType,
      sensitive: row.sensitive,
      recordedAt: row.recordedAt,
      recordedBy: row.recordedBy,
    };
  };

  /** The access event a clinical read owes, written where the Go service writes one. */
  const recordAccess = (
    session: MockSession,
    tenantId: string,
    row: StoredHealthCase,
    resourceType: string,
    resourceId: string,
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
      resourceType,
      resourceId,
      accessType,
      purposeCode: req.purposeCode === '' ? null : req.purposeCode,
      reasonText: req.reasonText === '' ? null : req.reasonText,
      outcome,
    });
  };

  /**
   * A case, or nothing. A provider-scoped actor sees the cases its own organizations opened
   * and no others; one outside the boundary is genuinely absent, which is why the answer is
   * 404 rather than 403. Unlike documents there is no escape for a case the tenant owns
   * itself: a back-office case naming no provider is not a provider's business either.
   */
  const findCase = (
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredHealthCase | undefined => {
    const row = world().healthCases.find((c) => c.id === id && c.tenantId === tenantId);
    if (!row) return undefined;
    return visible(session, tenantId, row) ? row : undefined;
  };

  const visible = (session: MockSession, tenantId: string, row: StoredHealthCase): boolean => {
    const scope = organizationScope(api, session, tenantId);
    return scope === null ? true : withinScope(scope, row.providerOrganizationId);
  };

  const encountersOf = (caseId: string): StoredEncounter[] =>
    world()
      .encounters.filter((e) => e.caseId === caseId)
      .sort((a, b) => a.startedAt.localeCompare(b.startedAt));

  /** The purpose header, checked against the reference the migration seeds. */
  const badPurpose = (req: AccessRequest): FieldError[] => accessHeaderErrors(req);

  /** Recomputes a case's sensitivity from the diagnoses that are actually stored. */
  const refreshSensitivity = (row: StoredHealthCase): void => {
    const ids = new Set(encountersOf(row.id).map((e) => e.id));
    row.sensitivity = world().diagnoses.some((d) => ids.has(d.encounterId) && d.sensitive)
      ? 'SENSITIVE'
      : 'STANDARD';
  };

  return [
    http.get(`${ANY}/api/v1/health-cases`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_READ, false);
      if ('error' in g) return g.error;
      const req = accessRequest(request);
      const bad = badPurpose(req);
      if (bad.length > 0) return validationFailed(api, bad);

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
      if (status !== null && !CASE_STATUSES.has(status)) {
        return validationFailed(api, [
          { field: 'status', code: 'ENUM', message: 'geçerli bir durum olmalı' },
        ]);
      }
      const caseType = url.searchParams.get('caseType');
      if (caseType !== null && !CASE_TYPES.has(caseType)) {
        return validationFailed(api, [
          { field: 'caseType', code: 'ENUM', message: 'geçerli bir vaka türü olmalı' },
        ]);
      }
      const personId = url.searchParams.get('personId');
      const providerOrganizationId = url.searchParams.get('providerOrganizationId');

      const rows = world()
        .healthCases.filter(
          (c) =>
            c.tenantId === g.tenantId &&
            visible(g.session, g.tenantId, c) &&
            (personId === null || c.personId === personId) &&
            (status === null || c.status === status) &&
            (caseType === null || c.caseType === caseType) &&
            (providerOrganizationId === null ||
              c.providerOrganizationId === providerOrganizationId),
        )
        .sort((a, b) => b.openedAt.localeCompare(a.openedAt) || b.id.localeCompare(a.id));

      const page = rows.slice(offset, offset + limit);
      const items = page.map((row) => {
        // A list never answers 428 and never records a refusal: refusing a page over one
        // sensitive row would say which row is sensitive.
        const d = decide(g.session, g.tenantId, row, req);
        if (d.projection === 'CLINICAL') {
          recordAccess(g.session, g.tenantId, row, 'health_case', row.id, 'SEARCH', req, 'SUCCESS');
        }
        return projectCase(row, encountersOf(row.id), d.projection);
      });
      const body: Schemas['HealthCasePage'] = {
        items,
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.post(`${ANY}/api/v1/health-cases`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_MANAGE, true);
      if ('error' in g) return g.error;
      const replay = requireIdempotencyKey(api, request);
      if (replay) return replay;
      const body = await readJson<Schemas['CreateHealthCase']>(request);
      if (!body) {
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      }
      const errors: FieldError[] = [];
      if (!CASE_TYPES.has(body.caseType)) {
        errors.push({ field: 'caseType', code: 'ENUM', message: 'geçerli bir vaka türü olmalı' });
      }
      const enrollment = world().enrollments.find(
        (e) => e.id === body.enrollmentId && e.tenantId === g.tenantId,
      );
      if (!enrollment) {
        errors.push({ field: 'enrollmentId', code: 'NOT_FOUND', message: 'plan kaydı bulunamadı' });
      } else if (enrollment.personId !== body.personId) {
        return problem(
          api,
          422,
          'HEALTH_CASE_ENROLLMENT_MISMATCH',
          'Plan kaydı bu kişiye veya programa ait değil',
        );
      } else if (
        body.programId !== undefined &&
        body.programId !== null &&
        body.programId !== enrollment.programId
      ) {
        return problem(
          api,
          422,
          'HEALTH_CASE_ENROLLMENT_MISMATCH',
          'Plan kaydı bu kişiye veya programa ait değil',
        );
      }
      if (body.openedAt && body.openedAt > new Date().toISOString()) {
        errors.push({
          field: 'openedAt',
          code: 'RANGE',
          message: 'vaka açılış zamanı gelecekte olamaz',
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const scope = organizationScope(api, g.session, g.tenantId);
      const providerOrganizationId = body.providerOrganizationId ?? null;
      if (scope !== null && !withinScope(scope, providerOrganizationId)) {
        return problem(
          api,
          403,
          'HEALTH_CASE_PROVIDER_SCOPE',
          'Bu sağlayıcı adına işlem yapamazsınız',
        );
      }
      if (body.serviceRequestId) {
        const req = world().serviceRequests.find(
          (r) => r.id === body.serviceRequestId && r.tenantId === g.tenantId,
        );
        if (!req) {
          return problem(api, 404, 'SERVICE_REQUEST_NOT_FOUND', 'Hizmet talebi bulunamadı');
        }
        // The same three questions the server's GetHealthCaseServiceRequest answers: whose
        // request it is, what kind it is, and whether any line of any of its versions names
        // a HEALTH-domain service. A case opened from an accommodation booking is not a
        // health case, and leaving that check out here would be a divergence a screen
        // discovers in production.
        const hasHealthLine = world()
          .serviceRequestVersions.filter((v) => v.serviceRequestId === req.id)
          .flatMap((v) => v.items)
          .some((item) => {
            const definition = world().serviceDefinitions.find(
              (d) => d.id === item.serviceDefinitionId,
            );
            if (!definition) return false;
            const category = world().serviceCategories.find((c) => c.id === definition.categoryId);
            return category?.domain === 'HEALTH';
          });
        if (
          req.personId !== body.personId ||
          (req.requestType !== 'DIRECT_SERVICE' && req.requestType !== 'PREAUTHORIZATION') ||
          !hasHealthLine
        ) {
          return problem(
            api,
            422,
            'HEALTH_CASE_REQUEST_NOT_ELIGIBLE',
            'Bu talepten sağlık vakası açılamaz',
          );
        }
      }

      const now = new Date().toISOString();
      const row: StoredHealthCase = {
        id: world().nextId(),
        tenantId: g.tenantId,
        personId: body.personId,
        programId: enrollment!.programId,
        enrollmentId: body.enrollmentId,
        caseType: body.caseType,
        providerOrganizationId,
        openedAt: body.openedAt ?? now,
        closedAt: null,
        status: 'OPEN',
        // Always STANDARD: sensitivity is what the diagnoses make it, and nothing here
        // accepts one from a caller.
        sensitivity: 'STANDARD',
        serviceRequestId: body.serviceRequestId ?? null,
        createdAt: now,
        rowVersion: 1,
      };
      world().healthCases.push(row);
      // A command records no access event: the access log answers who *looked* at a
      // member's clinical data, and the author of a record is not a reader of it.
      return HttpResponse.json(projectCase(row, [], 'CLINICAL'), {
        status: 201,
        headers: { ...NO_STORE, ETag: etagOf(row.rowVersion) },
      });
    }),

    http.get(`${ANY}/api/v1/health-cases/:caseId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_READ, false);
      if ('error' in g) return g.error;
      const req = accessRequest(request);
      const bad = badPurpose(req);
      if (bad.length > 0) return validationFailed(api, bad);
      const row = findCase(g.session, g.tenantId, pathParam(params, 'caseId'));
      if (!row) return caseNotFound(api);

      const d = decide(g.session, g.tenantId, row, req);
      if (d.purposeMissing) {
        recordAccess(g.session, g.tenantId, row, 'health_case', row.id, 'VIEW', req, 'DENIED');
        return accessPurposeRequired(api);
      }
      if (d.projection === 'CLINICAL') {
        recordAccess(g.session, g.tenantId, row, 'health_case', row.id, 'VIEW', req, 'SUCCESS');
      } else if (d.refusedSensitive) {
        recordAccess(g.session, g.tenantId, row, 'health_case', row.id, 'VIEW', req, 'DENIED');
      }
      return HttpResponse.json(projectCase(row, encountersOf(row.id), d.projection), {
        headers: { ...NO_STORE, ETag: etagOf(row.rowVersion) },
      });
    }),

    http.post(`${ANY}/api/v1/health-cases/:caseId/close`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_MANAGE, true);
      if ('error' in g) return g.error;
      const replay = requireIdempotencyKey(api, request);
      if (replay) return replay;
      const row = findCase(g.session, g.tenantId, pathParam(params, 'caseId'));
      if (!row) return caseNotFound(api);
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null || expected < 1) {
        return problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli', {
          detail: 'GET yanıtındaki ETag değerini If-Match olarak gönderin.',
        });
      }
      const body = (await readJson<Schemas['CloseHealthCase']>(request)) ?? {};
      if (body.reasonText && [...body.reasonText].length > MAX_CLOSE_REASON) {
        return validationFailed(api, [
          { field: 'reasonText', code: 'LENGTH', message: 'en fazla 200 karakter' },
        ]);
      }
      if (row.status === 'CLOSED') {
        return problem(api, 409, 'HEALTH_CASE_CLOSED', 'Sağlık vakası kapalı');
      }
      if (encountersOf(row.id).some((e) => e.endedAt === null)) {
        return problem(
          api,
          409,
          'HEALTH_CASE_ENCOUNTER_OPEN',
          'Vakada bitmemiş bir encounter var',
          { detail: "Vakayı kapatmadan önce tüm encounter'ları sonlandırın." },
        );
      }
      if (row.rowVersion !== expected) {
        return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
      }
      row.status = 'CLOSED';
      row.closedAt = new Date().toISOString();
      row.rowVersion += 1;
      // Like the server, the answer a command hands back is not a projection anybody has
      // to state a purpose for, and it records no access event.
      const d = decide(g.session, g.tenantId, row, accessRequest(request));
      return HttpResponse.json(projectCase(row, encountersOf(row.id), d.projection), {
        headers: { ...NO_STORE, ETag: etagOf(row.rowVersion) },
      });
    }),

    http.post(`${ANY}/api/v1/health-cases/:caseId/encounters`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_MANAGE, true);
      if ('error' in g) return g.error;
      // Writing a clinical note needs the grant to read one: the caller could not read back
      // what it wrote, and the server refuses for the same reason.
      if (!hasPermission(api, g.session, g.tenantId, PERMISSION_CLINICAL_READ)) {
        return problem(api, 403, 'PERMISSION_DENIED', 'Bu işlem için yetkiniz yok', {
          detail: PERMISSION_CLINICAL_READ,
        });
      }
      const replay = requireIdempotencyKey(api, request);
      if (replay) return replay;
      const row = findCase(g.session, g.tenantId, pathParam(params, 'caseId'));
      if (!row) return caseNotFound(api);
      if (row.status === 'CLOSED') {
        return problem(api, 409, 'HEALTH_CASE_CLOSED', 'Sağlık vakası kapalı');
      }
      const body = await readJson<Schemas['CreateEncounter']>(request);
      if (!body) {
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      }
      const errors: FieldError[] = [];
      if (!ENCOUNTER_TYPES.has(body.encounterType)) {
        errors.push({
          field: 'encounterType',
          code: 'ENUM',
          message: 'geçerli bir encounter türü olmalı',
        });
      }
      if (body.endedAt && body.endedAt < body.startedAt) {
        errors.push({
          field: 'endedAt',
          code: 'RANGE',
          message: 'bitiş zamanı başlangıçtan önce olamaz',
        });
      }
      if (body.branchCode && !BRANCH_CODE.test(body.branchCode)) {
        errors.push({
          field: 'branchCode',
          code: 'FORMAT',
          message: 'büyük harf, rakam, nokta ve tire; 1-64 karakter',
        });
      }
      if (body.notesClinical && [...body.notesClinical].length > MAX_NOTES) {
        errors.push({ field: 'notesClinical', code: 'LENGTH', message: 'en fazla 4000 karakter' });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const encounter: StoredEncounter = {
        id: world().nextId(),
        tenantId: g.tenantId,
        caseId: row.id,
        encounterType: body.encounterType,
        startedAt: body.startedAt,
        endedAt: body.endedAt ?? null,
        locationId: body.locationId ?? null,
        practitionerId: body.practitionerId ?? null,
        branchCode: body.branchCode ?? null,
        notesClinical: body.notesClinical ?? null,
        createdAt: new Date().toISOString(),
        rowVersion: 1,
      };
      world().encounters.push(encounter);
      return HttpResponse.json(projectEncounter(encounter, 'CLINICAL'), {
        status: 201,
        headers: { ...NO_STORE, ETag: etagOf(encounter.rowVersion) },
      });
    }),

    http.get(`${ANY}/api/v1/encounters/:encounterId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_READ, false);
      if ('error' in g) return g.error;
      const req = accessRequest(request);
      const bad = badPurpose(req);
      if (bad.length > 0) return validationFailed(api, bad);
      const encounter = world().encounters.find(
        (e) => e.id === pathParam(params, 'encounterId') && e.tenantId === g.tenantId,
      );
      if (!encounter) return encounterNotFound(api);
      const row = findCase(g.session, g.tenantId, encounter.caseId);
      // The boundary is the case's: an encounter is reached through it.
      if (!row) return encounterNotFound(api);

      const d = decide(g.session, g.tenantId, row, req);
      if (d.purposeMissing) {
        recordAccess(
          g.session,
          g.tenantId,
          row,
          'health_encounter',
          encounter.id,
          'VIEW',
          req,
          'DENIED',
        );
        return accessPurposeRequired(api);
      }
      if (d.projection === 'CLINICAL') {
        recordAccess(
          g.session,
          g.tenantId,
          row,
          'health_encounter',
          encounter.id,
          'VIEW',
          req,
          'SUCCESS',
        );
      } else if (d.refusedSensitive) {
        recordAccess(
          g.session,
          g.tenantId,
          row,
          'health_encounter',
          encounter.id,
          'VIEW',
          req,
          'DENIED',
        );
      }
      return HttpResponse.json(projectEncounter(encounter, d.projection), {
        headers: { ...NO_STORE, ETag: etagOf(encounter.rowVersion) },
      });
    }),

    http.get(`${ANY}/api/v1/encounters/:encounterId/diagnoses`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_READ, false);
      if ('error' in g) return g.error;
      const req = accessRequest(request);
      const bad = badPurpose(req);
      if (bad.length > 0) return validationFailed(api, bad);
      const encounter = world().encounters.find(
        (e) => e.id === pathParam(params, 'encounterId') && e.tenantId === g.tenantId,
      );
      if (!encounter) return encounterNotFound(api);
      const row = findCase(g.session, g.tenantId, encounter.caseId);
      if (!row) return encounterNotFound(api);

      const d = decide(g.session, g.tenantId, row, req);
      if (d.purposeMissing) {
        recordAccess(
          g.session,
          g.tenantId,
          row,
          'health_encounter',
          encounter.id,
          'VIEW',
          req,
          'DENIED',
        );
        return accessPurposeRequired(api);
      }
      if (d.projection !== 'CLINICAL') {
        // Never an empty list. An empty list would tell the caller the encounter has no
        // diagnosis, and the same refusal covers a sensitive case it may not read, so the
        // answer itself never says which of the two it was.
        recordAccess(
          g.session,
          g.tenantId,
          row,
          'health_encounter',
          encounter.id,
          'VIEW',
          req,
          'DENIED',
        );
        return clinicalReadRequired(api);
      }
      recordAccess(
        g.session,
        g.tenantId,
        row,
        'health_encounter',
        encounter.id,
        'VIEW',
        req,
        'SUCCESS',
      );
      const items = world()
        .diagnoses.filter((dg) => dg.encounterId === encounter.id)
        .sort((a, b) => a.diagnosisType.localeCompare(b.diagnosisType))
        .map(toDiagnosis);
      const body: Schemas['DiagnosisList'] = { items };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.put(`${ANY}/api/v1/encounters/:encounterId/diagnoses`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_MANAGE, true);
      if ('error' in g) return g.error;
      if (!hasPermission(api, g.session, g.tenantId, PERMISSION_CLINICAL_READ)) {
        return problem(api, 403, 'PERMISSION_DENIED', 'Bu işlem için yetkiniz yok', {
          detail: PERMISSION_CLINICAL_READ,
        });
      }
      const replay = requireIdempotencyKey(api, request);
      if (replay) return replay;
      const encounter = world().encounters.find(
        (e) => e.id === pathParam(params, 'encounterId') && e.tenantId === g.tenantId,
      );
      if (!encounter) return encounterNotFound(api);
      const row = findCase(g.session, g.tenantId, encounter.caseId);
      if (!row) return encounterNotFound(api);
      if (row.status === 'CLOSED') {
        return problem(api, 409, 'HEALTH_CASE_CLOSED', 'Sağlık vakası kapalı');
      }
      const body = await readJson<Schemas['PutEncounterDiagnoses']>(request);
      if (!body || !Array.isArray(body.items)) {
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      }
      if (body.items.length > MAX_DIAGNOSES) {
        return validationFailed(api, [
          { field: 'items', code: 'RANGE', message: 'en fazla 50 tanı gönderilebilir' },
        ]);
      }
      const errors: FieldError[] = [];
      let primary = -1;
      const seen = new Map<string, number>();
      body.items.forEach((item, i) => {
        const path = `items[${i}]`;
        const first = seen.get(item.codeValueId);
        if (first !== undefined) {
          errors.push({
            field: `${path}.codeValueId`,
            code: 'DUPLICATE',
            message: `bu tanı items[${first}] içinde de var`,
          });
        } else {
          seen.set(item.codeValueId, i);
        }
        if (!DIAGNOSIS_TYPES.has(item.diagnosisType)) {
          errors.push({
            field: `${path}.diagnosisType`,
            code: 'ENUM',
            message: 'geçerli bir tanı türü olmalı',
          });
        }
        if (item.diagnosisType !== 'PRIMARY') return;
        if (primary >= 0) {
          errors.push({
            field: `${path}.diagnosisType`,
            code: 'DUPLICATE_PRIMARY',
            message: `bir encounter'da tek ana tanı olur; items[${primary}] zaten ana tanı`,
          });
          return;
        }
        primary = i;
      });
      const resolved = body.items.map((item, i) => {
        const value = world().codeValues.find(
          (v) => v.id === item.codeValueId && v.tenantId === g.tenantId,
        );
        if (!value) {
          errors.push({
            field: `items[${i}].codeValueId`,
            code: 'NOT_FOUND',
            message: 'bu tanı kodu katalogda yok',
          });
        } else if (!value.active) {
          errors.push({
            field: `items[${i}].codeValueId`,
            code: 'INACTIVE',
            message: 'bu tanı kodu artık kullanılmıyor',
          });
        }
        return value;
      });
      if (errors.length > 0) return validationFailed(api, errors);

      const now = new Date().toISOString();
      const kept = world().diagnoses.filter((d) => d.encounterId !== encounter.id);
      const written: StoredDiagnosis[] = body.items.map((item, i) => ({
        id: world().nextId(),
        tenantId: g.tenantId,
        encounterId: encounter.id,
        codeSystemId: resolved[i]!.codeSystemId,
        codeValueId: item.codeValueId,
        diagnosisType: item.diagnosisType,
        // Read from the code value, never from the caller.
        sensitive: resolved[i]!.attributes.sensitive === true,
        recordedAt: now,
        recordedBy: g.session.account.actorId,
      }));
      world().diagnoses = [...kept, ...written];
      refreshSensitivity(row);
      const responseBody: Schemas['DiagnosisList'] = { items: written.map(toDiagnosis) };
      return HttpResponse.json(responseBody, { headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/health-access-log`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_AUDIT_READ, false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) {
        return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      }
      const personId = url.searchParams.get('personId');
      const rows = world()
        .healthAccessEvents.filter(
          (e) => e.tenantId === g.tenantId && (personId === null || e.personId === personId),
        )
        .sort((a, b) => b.occurredAt.localeCompare(a.occurredAt) || b.id.localeCompare(a.id));
      const page = rows.slice(offset, offset + limit);
      const body: Schemas['HealthAccessLogPage'] = {
        items: page.map(({ tenantId: _tenantId, ...event }) => event),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),
  ];
}
