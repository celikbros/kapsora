/**
 * MSW handlers for the treatment report: its lifecycle, its immutability, its version chain
 * and the two projections of it.
 *
 * The mock is a test double of the Go server and M4 treats a divergence in either direction
 * as a bug. Three things here are transcriptions rather than re-implementations, because
 * they are the three a screen would be built wrongly against:
 *
 *   - **the projection**, which is `decideProjection` in health-handlers.ts — the same
 *     function the case handlers ask, given the sensitivity of the case the report hangs off.
 *     A report is exactly as sensitive as the episode of care it belongs to, and a second
 *     rule about that would be a second place for the two to disagree;
 *   - **the freeze**: everything that has left DRAFT answers 409 MEDICAL_REPORT_IMMUTABLE to
 *     `patchMedicalReportDraft` and `putMedicalReportServices`, and a correction is
 *     `createMedicalReport` with `supersedesReportId`;
 *   - **the chain**: approving version n+1 moves version n out of APPROVED in the same
 *     breath, so a chain holds exactly one approved version at every moment — and the version
 *     it replaced keeps its decision, its lines and its usage rows exactly as they were.
 *
 * As on the server, the projection is applied to the stored row before it becomes a body, in
 * `projectReport`, so no handler below can leak a field by forgetting one.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import {
  type MockWorld,
  type StoredHealthCase,
  type StoredMedicalReport,
  type StoredMedicalReportService,
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
  ownFile,
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
const PERMISSION_REPORT_MANAGE = 'health.medical_report.manage';
const PERMISSION_REPORT_REVIEW = 'health.medical_report.review';

/** health.medical_report.status, as migration 000032 writes it. */
const REPORT_STATUSES = new Set<string>([
  'DRAFT',
  'SUBMITTED',
  'UNDER_REVIEW',
  'APPROVED',
  'REJECTED',
  'CANCELLED',
  'EXPIRED',
  'SUPERSEDED',
]);

/** The lifecycle, as one table. Nothing below decides a transition for itself. */
const TRANSITIONS: Record<string, { from: string[]; to: Schemas['MedicalReportStatus'] }> = {
  submit: { from: ['DRAFT'], to: 'SUBMITTED' },
  'start-review': { from: ['SUBMITTED'], to: 'UNDER_REVIEW' },
  approve: { from: ['UNDER_REVIEW'], to: 'APPROVED' },
  reject: { from: ['UNDER_REVIEW'], to: 'REJECTED' },
  cancel: { from: ['DRAFT', 'SUBMITTED'], to: 'CANCELLED' },
};

const MAX_SERVICES = 50;
const MAX_SUMMARY = 4000;
const MAX_LINE_NOTES = 1000;
const CODE = /^[A-Z][A-Z0-9_.-]{0,63}$/;
const REASON_CODE = /^[A-Z][A-Z0-9_]{1,63}$/;
const CURRENCY = /^[A-Z]{3}$/;
const DECIMAL = /^[0-9]{1,14}(\.[0-9]{1,6})?$/;
const DATE_ONLY = /^\d{4}-\d{2}-\d{2}$/;

/** The generic document type the submit gate accepts besides the report's own. */
const DOCUMENT_TYPE_MEDICAL_REPORT = 'MEDICAL_REPORT';

function reportNotFound(api: MockApi): Response {
  return problem(api, 404, 'MEDICAL_REPORT_NOT_FOUND', 'Tedavi raporu bulunamadı');
}

function reportImmutable(api: MockApi): Response {
  return problem(api, 409, 'MEDICAL_REPORT_IMMUTABLE', 'Karara bağlanmış rapor değiştirilemez', {
    detail: 'Düzeltme için raporun yeni bir sürümünü oluşturun.',
  });
}

function transitionInvalid(api: MockApi): Response {
  return problem(
    api,
    409,
    'MEDICAL_REPORT_TRANSITION_INVALID',
    'Rapor bu durumda bu işleme uygun değil',
  );
}

/** A decided report is what a correction supersedes, and what may never be edited. */
function decided(status: string): boolean {
  return status === 'APPROVED' || status === 'REJECTED' || status === 'SUPERSEDED';
}

export function medicalReportHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  const caseOf = (row: StoredMedicalReport): StoredHealthCase | undefined =>
    row.caseId === null ? undefined : world().healthCases.find((c) => c.id === row.caseId);

  /**
   * The sensitivity the projection decides on. A report has none of its own: it is as
   * sensitive as the episode of care it belongs to, and a report written outside one the
   * platform holds is STANDARD.
   */
  const sensitivityOf = (row: StoredMedicalReport): Schemas['HealthCaseSensitivity'] =>
    caseOf(row)?.sensitivity ?? 'STANDARD';

  const decide = (
    session: MockSession,
    tenantId: string,
    row: StoredMedicalReport,
    req: AccessRequest,
  ) => decideProjection(api, session, tenantId, sensitivityOf(row), req);

  const servicesOf = (reportId: string): StoredMedicalReportService[] =>
    world()
      .medicalReportServices.filter((line) => line.reportId === reportId)
      .sort((a, b) => codeOf(a.serviceDefinitionId).localeCompare(codeOf(b.serviceDefinitionId)));

  const codeOf = (serviceDefinitionId: string): string =>
    world().serviceDefinitions.find((d) => d.id === serviceDefinitionId)?.code ?? '';

  const nameOf = (serviceDefinitionId: string): string =>
    world().serviceDefinitions.find((d) => d.id === serviceDefinitionId)?.name ?? '';

  /** The report's attachments, joined with what the object store knows about each file. */
  const documentsOf = (reportId: string): Schemas['MedicalReportDocument'][] =>
    world()
      .documentLinks.filter(
        (link) => link.aggregateType === 'MEDICAL_REPORT' && link.aggregateId === reportId,
      )
      .flatMap((link) => {
        const doc = world().documents.find((d) => d.id === link.documentId);
        if (!doc) return [];
        return [
          {
            id: link.id,
            objectId: doc.id,
            documentTypeCode: link.documentTypeCode,
            purpose: link.purpose ?? null,
            requiredPermission: link.requiredPermission ?? null,
            originalFilename: doc.originalFilename,
            contentType: doc.contentType,
            scanStatus: doc.scanStatus,
            classification: doc.classification,
            createdAt: link.createdAt,
          },
        ];
      });

  /**
   * The projection, applied to the row before it becomes a body. A clinical field is omitted
   * rather than sent as null, because the server omits it: a screen that distinguished the
   * two would work against one and not the other.
   */
  const projectReport = (
    row: StoredMedicalReport,
    projection: Projection,
  ): Schemas['MedicalReport'] => {
    const clinical = projection === 'CLINICAL';
    return {
      id: row.id,
      personId: row.personId,
      caseId: row.caseId,
      reference: row.reference,
      versionNo: row.versionNo,
      rootReportId: row.rootReportId,
      supersedesReportId: row.supersedesReportId,
      issuingPractitionerId: row.issuingPractitionerId,
      issuingProviderOrganizationId: row.issuingProviderOrganizationId,
      issuedAt: row.issuedAt,
      validFrom: row.validFrom,
      validTo: row.validTo,
      status: row.status,
      projection,
      rejectReasonCode: row.rejectReasonCode,
      reviewedBy: row.reviewedBy,
      reviewedAt: row.reviewedAt,
      submittedAt: row.submittedAt,
      submittedBy: row.submittedBy,
      // What the financial projection never carries: the type, the subtype, the summary, the
      // reviewer's comment and the attachments. Everything that says what was wrong with the
      // person, and nothing a claim needs to be reconciled.
      ...(clinical
        ? {
            reportType: row.reportType,
            reportSubtype: row.reportSubtype,
            clinicalSummary: row.clinicalSummary,
            reviewComment: row.reviewComment,
          }
        : {}),
      services: servicesOf(row.id).map((line) => ({
        id: line.id,
        serviceDefinitionId: line.serviceDefinitionId,
        serviceCode: codeOf(line.serviceDefinitionId),
        serviceName: nameOf(line.serviceDefinitionId),
        coveredQuantity: line.coveredQuantity,
        coveredAmount: line.coveredAmount,
        currencyCode: line.currencyCode,
        // The line note is clinical too: "sol dizde artroskopi sonrası" is a diagnosis in a
        // sentence, whatever the service is.
        ...(clinical ? { notes: line.notes } : {}),
      })),
      // Empty rather than shortened: a count of documents is a fact about the patient too.
      documents: clinical ? documentsOf(row.id) : [],
      createdAt: row.createdAt,
      rowVersion: row.rowVersion,
    };
  };

  /** The access event a clinical read of a report owes, with resource type MEDICAL_REPORT. */
  const recordAccess = (
    session: MockSession,
    tenantId: string,
    row: StoredMedicalReport,
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
      resourceType: 'MEDICAL_REPORT',
      resourceId: row.id,
      accessType,
      purposeCode: req.purposeCode === '' ? null : req.purposeCode,
      reasonText: req.reasonText === '' ? null : req.reasonText,
      outcome,
    });
  };

  const visible = (session: MockSession, tenantId: string, row: StoredMedicalReport): boolean => {
    const scope = organizationScope(api, session, tenantId);
    return scope === null ? true : withinScope(scope, row.issuingProviderOrganizationId);
  };

  const findReport = (
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredMedicalReport | undefined => {
    const row = world().medicalReports.find((r) => r.id === id && r.tenantId === tenantId);
    if (!row) return undefined;
    return visible(session, tenantId, row) ? row : undefined;
  };

  /** The header a create or a patch sends, validated the way the Go domain validates it. */
  const headerErrors = (
    body: {
      reportType?: string | null;
      reportSubtype?: string | null;
      issuedAt?: string | null;
      validFrom?: string | null;
      validTo?: string | null;
      clinicalSummary?: string | null;
    },
    required: boolean,
  ): FieldError[] => {
    const errors: FieldError[] = [];
    const type = (body.reportType ?? '').trim();
    if (type === '') {
      if (required) errors.push({ field: 'reportType', code: 'REQUIRED', message: 'kod zorunlu' });
    } else if (!CODE.test(type)) {
      errors.push({ field: 'reportType', code: 'FORMAT', message: 'geçersiz kod biçimi' });
    }
    const subtype = (body.reportSubtype ?? '').trim();
    if (subtype !== '' && !CODE.test(subtype)) {
      errors.push({ field: 'reportSubtype', code: 'FORMAT', message: 'geçersiz kod biçimi' });
    }
    for (const [field, value] of [
      ['issuedAt', body.issuedAt],
      ['validFrom', body.validFrom],
      ['validTo', body.validTo],
    ] as const) {
      if (value === undefined || value === null || value === '') {
        if (required) errors.push({ field, code: 'REQUIRED', message: 'tarih zorunlu' });
        continue;
      }
      if (!DATE_ONLY.test(value)) {
        errors.push({ field, code: 'FORMAT', message: 'YYYY-AA-GG biçiminde olmalı' });
      }
    }
    if (body.issuedAt && DATE_ONLY.test(body.issuedAt) && body.issuedAt > today()) {
      errors.push({ field: 'issuedAt', code: 'RANGE', message: 'rapor tarihi gelecekte olamaz' });
    }
    if (body.validFrom && body.validTo && body.validTo < body.validFrom) {
      errors.push({
        field: 'validTo',
        code: 'RANGE',
        message: 'geçerlilik bitişi başlangıçtan önce olamaz',
      });
    }
    if ((body.clinicalSummary ?? '').length > MAX_SUMMARY) {
      errors.push({ field: 'clinicalSummary', code: 'LENGTH', message: 'en fazla 4000 karakter' });
    }
    return errors;
  };

  const serviceErrors = (items: Schemas['MedicalReportServiceInput'][]): FieldError[] => {
    const errors: FieldError[] = [];
    if (items.length > MAX_SERVICES) {
      return [
        { field: 'items', code: 'RANGE', message: 'en fazla 50 hizmet satırı gönderilebilir' },
      ];
    }
    const seen = new Map<string, number>();
    items.forEach((item, index) => {
      const path = `items[${index}]`;
      const id = (item.serviceDefinitionId ?? '').trim();
      if (id === '') {
        errors.push({
          field: `${path}.serviceDefinitionId`,
          code: 'REQUIRED',
          message: 'hizmet tanımı zorunlu',
        });
      } else if (seen.has(id)) {
        errors.push({
          field: `${path}.serviceDefinitionId`,
          code: 'DUPLICATE',
          message: `bu hizmet items[${seen.get(id)!}] içinde de var`,
        });
      } else {
        seen.set(id, index);
      }
      for (const [field, value] of [
        ['coveredQuantity', item.coveredQuantity],
        ['coveredAmount', item.coveredAmount],
      ] as const) {
        if (value === undefined || value === null || value === '') continue;
        if (!DECIMAL.test(value)) {
          errors.push({ field: `${path}.${field}`, code: 'FORMAT', message: 'geçersiz sayı' });
        } else if (Number(value) <= 0) {
          errors.push({
            field: `${path}.${field}`,
            code: 'RANGE',
            message: 'sıfırdan büyük olmalı',
          });
        }
      }
      const currency = (item.currencyCode ?? '').trim();
      if (currency !== '' && !CURRENCY.test(currency)) {
        errors.push({
          field: `${path}.currencyCode`,
          code: 'FORMAT',
          message: 'üç büyük harfli para birimi kodu olmalı',
        });
      }
      if ((item.coveredAmount ?? '') !== '' && currency === '') {
        errors.push({
          field: `${path}.currencyCode`,
          code: 'REQUIRED',
          message: 'tutar verildiğinde para birimi zorunlu',
        });
      }
      if ((item.notes ?? '').length > MAX_LINE_NOTES) {
        errors.push({ field: `${path}.notes`, code: 'LENGTH', message: 'en fazla 1000 karakter' });
      }
    });
    return errors;
  };

  /**
   * The read every command makes before it writes: the report, the caller's boundary, the
   * lifecycle and the If-Match. A frozen report answers MEDICAL_REPORT_IMMUTABLE rather than
   * a transition error, because "you cannot edit an approved report" is what the caller needs
   * to hear.
   */
  const forCommand = (
    request: Request,
    session: MockSession,
    tenantId: string,
    id: string,
    command: string | null,
  ): { row: StoredMedicalReport; to: Schemas['MedicalReportStatus'] } | { error: Response } => {
    const row = findReport(session, tenantId, id);
    if (!row) return { error: reportNotFound(api) };
    const expected = parseIfMatch(request.headers.get('If-Match'));
    if (expected === null || expected < 1) {
      return {
        error: problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli', {
          detail: 'GET yanıtındaki ETag değerini If-Match olarak gönderin.',
        }),
      };
    }
    let to: Schemas['MedicalReportStatus'] = 'DRAFT';
    if (command === null) {
      // An edit: only a draft accepts one.
      if (row.status !== 'DRAFT') return { error: reportImmutable(api) };
    } else {
      const transition = TRANSITIONS[command]!;
      if (!transition.from.includes(row.status)) {
        return { error: decided(row.status) ? reportImmutable(api) : transitionInvalid(api) };
      }
      to = transition.to;
    }
    if (row.rowVersion !== expected) {
      return {
        error: problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti', {
          detail: 'Güncel sürümü alıp değişikliğinizi yeniden uygulayın.',
        }),
      };
    }
    return { row, to };
  };

  /** The body a command answers with: read back, projected, and recording nothing. */
  const answer = (
    session: MockSession,
    tenantId: string,
    row: StoredMedicalReport,
    status = 200,
  ): Response =>
    HttpResponse.json(
      projectReport(
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

  return [
    http.get(`${ANY}/api/v1/medical-reports`, async ({ request }) => {
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
      if (status !== null && !REPORT_STATUSES.has(status)) {
        return validationFailed(api, [
          { field: 'status', code: 'ENUM', message: 'geçerli bir durum olmalı' },
        ]);
      }
      const personId = url.searchParams.get('personId');
      const caseId = url.searchParams.get('caseId');
      const rootReportId = url.searchParams.get('rootReportId');
      const providerOrganizationId = url.searchParams.get('providerOrganizationId');
      const reportType = url.searchParams.get('reportType');
      const validOn = url.searchParams.get('validOn');

      const rows = world()
        .medicalReports.filter(
          (r) =>
            r.tenantId === g.tenantId &&
            visible(g.session, g.tenantId, r) &&
            (personId === null || r.personId === personId) &&
            (caseId === null || r.caseId === caseId) &&
            (rootReportId === null || r.rootReportId === rootReportId) &&
            (providerOrganizationId === null ||
              r.issuingProviderOrganizationId === providerOrganizationId) &&
            (status === null || r.status === status) &&
            (reportType === null || r.reportType === reportType) &&
            (validOn === null || (validOn >= r.validFrom && validOn <= r.validTo)),
        )
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));

      const page = rows.slice(offset, offset + limit);
      const items = page.map((row) => {
        // A list never answers 428 and never records a refusal: refusing a page over one
        // sensitive row would say which row is sensitive.
        const d = decide(g.session, g.tenantId, row, req);
        if (d.projection === 'CLINICAL') {
          recordAccess(g.session, g.tenantId, row, 'SEARCH', req, 'SUCCESS');
        }
        return projectReport(row, d.projection);
      });
      const body: Schemas['MedicalReportPage'] = {
        items,
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/medical-reports/:reportId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_READ, false);
      if ('error' in g) return g.error;
      const req = accessRequest(request);
      const badAccess = accessHeaderProblem(api, req);
      if (badAccess) return badAccess;
      const row = findReport(g.session, g.tenantId, pathParam(params, 'reportId'));
      if (!row) return reportNotFound(api);

      const d = decide(g.session, g.tenantId, row, req);
      if (d.purposeMissing) {
        recordAccess(g.session, g.tenantId, row, 'VIEW', req, 'DENIED');
        return accessPurposeRequired(api);
      }
      if (d.projection === 'CLINICAL') {
        recordAccess(g.session, g.tenantId, row, 'VIEW', req, 'SUCCESS');
      } else if (d.refusedSensitive) {
        recordAccess(g.session, g.tenantId, row, 'VIEW', req, 'DENIED');
      }
      return HttpResponse.json(projectReport(row, d.projection), {
        headers: { ...NO_STORE, ETag: etagOf(row.rowVersion) },
      });
    }),

    http.post(`${ANY}/api/v1/medical-reports`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_MANAGE, true);
      if ('error' in g) return g.error;
      const replay = requireIdempotencyKey(api, request);
      if (replay) return replay;
      const body = await readJson<Schemas['CreateMedicalReport']>(request);
      if (!body) {
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      }

      let previous: StoredMedicalReport | undefined;
      if (body.supersedesReportId) {
        previous = findReport(g.session, g.tenantId, body.supersedesReportId);
        if (
          !previous ||
          !decided(previous.status) ||
          (body.personId && body.personId !== previous.personId) ||
          // Only the newest version of a chain may be corrected: a fork would make "which
          // version is in force" a question with two answers.
          world().medicalReports.some(
            (r) =>
              r.tenantId === g.tenantId &&
              r.rootReportId === previous!.rootReportId &&
              r.versionNo > previous!.versionNo,
          )
        ) {
          return problem(api, 422, 'MEDICAL_REPORT_SUPERSEDES_INVALID', 'Bu rapor düzeltilemez', {
            detail: 'Yalnızca bir zincirin en son karara bağlanmış sürümü düzeltilebilir.',
          });
        }
      }

      // A correction stands on the header of the version it corrects wherever the caller
      // sent nothing.
      const header = {
        reportType: body.reportType ?? previous?.reportType ?? null,
        reportSubtype: body.reportSubtype ?? previous?.reportSubtype ?? null,
        issuedAt: body.issuedAt ?? previous?.issuedAt ?? null,
        validFrom: body.validFrom ?? previous?.validFrom ?? null,
        validTo: body.validTo ?? previous?.validTo ?? null,
        clinicalSummary: body.clinicalSummary ?? previous?.clinicalSummary ?? null,
      };
      const errors = headerErrors(header, true);
      if (errors.length > 0) return validationFailed(api, errors);

      const personId = previous?.personId ?? body.personId;
      const caseId = body.caseId ?? previous?.caseId ?? null;
      if (caseId !== null) {
        const row = world().healthCases.find((c) => c.id === caseId && c.tenantId === g.tenantId);
        if (!row) return problem(api, 404, 'HEALTH_CASE_NOT_FOUND', 'Sağlık vakası bulunamadı');
        if (row.personId !== personId) {
          return problem(
            api,
            422,
            'MEDICAL_REPORT_CASE_MISMATCH',
            'Vaka başka bir hak sahibine ait',
          );
        }
      }
      const providerOrganizationId =
        body.issuingProviderOrganizationId ?? previous?.issuingProviderOrganizationId ?? null;
      const scope = organizationScope(api, g.session, g.tenantId);
      if (scope !== null && !withinScope(scope, providerOrganizationId)) {
        return problem(
          api,
          403,
          'HEALTH_CASE_PROVIDER_SCOPE',
          'Bu sağlayıcı adına işlem yapamazsınız',
        );
      }

      const now = new Date().toISOString();
      const id = world().nextId();
      const row: StoredMedicalReport = {
        id,
        tenantId: g.tenantId,
        personId,
        caseId,
        // A correction inherits the reference: a member quoting "MR-2026…" is still quoting
        // the same report.
        reference: previous?.reference ?? mintReference(world()),
        versionNo: previous ? previous.versionNo + 1 : 1,
        rootReportId: previous?.rootReportId ?? id,
        supersedesReportId: previous?.id ?? null,
        reportType: header.reportType!,
        reportSubtype: header.reportSubtype,
        issuingPractitionerId:
          body.issuingPractitionerId ?? previous?.issuingPractitionerId ?? null,
        issuingProviderOrganizationId: providerOrganizationId,
        issuedAt: header.issuedAt!,
        validFrom: header.validFrom!,
        validTo: header.validTo!,
        status: 'DRAFT',
        clinicalSummary: header.clinicalSummary,
        reviewComment: null,
        rejectReasonCode: null,
        reviewedBy: null,
        reviewedAt: null,
        submittedAt: null,
        submittedBy: null,
        createdAt: now,
        rowVersion: 1,
      };
      world().medicalReports.push(row);
      // The lines of the version being corrected are copied. The ids are not: a line belongs
      // to the version that holds it, and a claim that quoted version 1's line quoted
      // version 1.
      if (previous) {
        for (const line of servicesOf(previous.id)) {
          world().medicalReportServices.push({
            ...line,
            id: world().nextId(),
            reportId: row.id,
          });
        }
      }
      // A command records no access event: the access log answers who *looked* at a member's
      // clinical data, and the author of a record is not a reader of it.
      return HttpResponse.json(projectReport(row, 'CLINICAL'), {
        status: 201,
        headers: { ...NO_STORE, ETag: etagOf(row.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/medical-reports/:reportId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_MANAGE, true);
      if ('error' in g) return g.error;
      const replay = requireIdempotencyKey(api, request);
      if (replay) return replay;
      const guard = forCommand(request, g.session, g.tenantId, pathParam(params, 'reportId'), null);
      if ('error' in guard) return guard.error;
      const body = await readJson<Schemas['PatchMedicalReportDraft']>(request);
      if (!body) {
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      }
      const errors = headerErrors(body, true);
      if (errors.length > 0) return validationFailed(api, errors);

      const row = guard.row;
      row.caseId = body.caseId ?? null;
      row.reportType = body.reportType;
      row.reportSubtype = body.reportSubtype ?? null;
      row.issuingPractitionerId = body.issuingPractitionerId ?? null;
      row.issuingProviderOrganizationId = body.issuingProviderOrganizationId ?? null;
      row.issuedAt = body.issuedAt;
      row.validFrom = body.validFrom;
      row.validTo = body.validTo;
      row.clinicalSummary = body.clinicalSummary ?? null;
      row.rowVersion += 1;
      return answer(g.session, g.tenantId, row);
    }),

    http.put(`${ANY}/api/v1/medical-reports/:reportId/services`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_MANAGE, true);
      if ('error' in g) return g.error;
      const replay = requireIdempotencyKey(api, request);
      if (replay) return replay;
      const guard = forCommand(request, g.session, g.tenantId, pathParam(params, 'reportId'), null);
      if ('error' in guard) return guard.error;
      const body = await readJson<Schemas['PutMedicalReportServices']>(request);
      if (!body) {
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      }
      const errors = serviceErrors(body.items ?? []);
      if (errors.length > 0) return validationFailed(api, errors);
      for (const item of body.items ?? []) {
        if (
          !world().serviceDefinitions.some(
            (d) => d.id === item.serviceDefinitionId && d.tenantId === g.tenantId,
          )
        ) {
          return problem(
            api,
            422,
            'MEDICAL_REPORT_SERVICE_UNKNOWN',
            'Hizmet tanımı katalogda bulunamadı',
          );
        }
      }

      const row = guard.row;
      // The set is the unit: the whole set is replaced, and a line id is not something
      // anything else hangs off.
      api.world.medicalReportServices = world().medicalReportServices.filter(
        (line) => line.reportId !== row.id,
      );
      for (const item of body.items ?? []) {
        api.world.medicalReportServices.push({
          id: world().nextId(),
          tenantId: g.tenantId,
          reportId: row.id,
          serviceDefinitionId: item.serviceDefinitionId,
          coveredQuantity: item.coveredQuantity ?? null,
          coveredAmount: item.coveredAmount ?? null,
          currencyCode: item.currencyCode ?? null,
          notes: item.notes ?? null,
        });
      }
      // The lines are part of what the report says, so replacing them moves the ETag.
      row.rowVersion += 1;
      return answer(g.session, g.tenantId, row);
    }),

    http.post(`${ANY}/api/v1/medical-reports/:reportId/submit`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_MANAGE, true);
      if ('error' in g) return g.error;
      const replay = requireIdempotencyKey(api, request);
      if (replay) return replay;
      const guard = forCommand(
        request,
        g.session,
        g.tenantId,
        pathParam(params, 'reportId'),
        'submit',
      );
      if ('error' in guard) return guard.error;
      const row = guard.row;

      if (servicesOf(row.id).length === 0) {
        return problem(
          api,
          422,
          'MEDICAL_REPORT_SERVICE_REQUIRED',
          'Raporda en az bir hizmet satırı olmalı',
        );
      }
      // A link to an object still in quarantine is not a document a reviewer can open.
      const cleared = world()
        .documentLinks.filter(
          (link) =>
            link.aggregateType === 'MEDICAL_REPORT' &&
            link.aggregateId === row.id &&
            (link.documentTypeCode === row.reportType ||
              link.documentTypeCode === DOCUMENT_TYPE_MEDICAL_REPORT),
        )
        .some((link) => {
          const doc = world().documents.find((d) => d.id === link.documentId);
          return doc?.scanStatus === 'CLEAN' && doc.purgedAt === null;
        });
      if (!cleared) {
        return problem(api, 422, 'MEDICAL_REPORT_DOCUMENT_REQUIRED', 'Rapor belgesi yüklenmeli', {
          detail: 'Gönderimden önce rapor dosyasını yükleyin ve taramanın tamamlanmasını bekleyin.',
        });
      }

      row.status = guard.to;
      row.submittedAt = new Date().toISOString();
      row.submittedBy = g.session.account.actorId;
      row.rowVersion += 1;
      // The work item the server raises in the same transaction. Its title carries the
      // reference and nothing else: a queue is a list people read across a room.
      const queue = world().workQueues.find(
        (q) => q.tenantId === g.tenantId && q.code === 'MEDICAL_REVIEW',
      );
      if (queue && queue.active) {
        world().workItems.push({
          tenantId: g.tenantId,
          id: world().nextId(),
          queueId: queue.id,
          aggregateType: 'MEDICAL_REPORT',
          aggregateId: row.id,
          title: `Tıbbi rapor ${row.reference}`,
          priority: 100,
          assigneeActorId: null,
          assignedAt: null,
          dueAt:
            queue.slaMinutes === null || queue.slaMinutes === undefined
              ? null
              : new Date(Date.now() + queue.slaMinutes * 60_000).toISOString(),
          slaMinutesSnapshot: queue.slaMinutes ?? null,
          status: 'OPEN',
          outcomeCode: null,
          completedAt: null,
          completedBy: null,
          escalatedAt: null,
          escalatedFromQueueId: null,
          createdAt: row.submittedAt,
          rowVersion: 1,
        });
      }
      return answer(g.session, g.tenantId, row);
    }),

    http.post(
      `${ANY}/api/v1/medical-reports/:reportId/start-review`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, PERMISSION_REPORT_REVIEW, true);
        if ('error' in g) return g.error;
        const replay = requireIdempotencyKey(api, request);
        if (replay) return replay;
        const guard = forCommand(
          request,
          g.session,
          g.tenantId,
          pathParam(params, 'reportId'),
          'start-review',
        );
        if ('error' in guard) return guard.error;
        const own = ownFile(api, g.session, g.tenantId, guard.row.personId);
        if (own) return own;
        guard.row.status = guard.to;
        guard.row.rowVersion += 1;
        return answer(g.session, g.tenantId, guard.row);
      },
    ),

    http.post(`${ANY}/api/v1/medical-reports/:reportId/approve`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_REVIEW, true);
      if ('error' in g) return g.error;
      const replay = requireIdempotencyKey(api, request);
      if (replay) return replay;
      const guard = forCommand(
        request,
        g.session,
        g.tenantId,
        pathParam(params, 'reportId'),
        'approve',
      );
      if ('error' in guard) return guard.error;
      const own = ownFile(api, g.session, g.tenantId, guard.row.personId);
      if (own) return own;
      const body = (await readJson<Schemas['DecideMedicalReport']>(request)) ?? {};
      if ((body.reviewComment ?? '').length > MAX_SUMMARY) {
        return validationFailed(api, [
          { field: 'reviewComment', code: 'LENGTH', message: 'en fazla 4000 karakter' },
        ]);
      }
      const row = guard.row;
      // The approval moves to this version. The one it replaces keeps everything but its
      // status, so "what did the reviewer approve on the fifth" stays answerable.
      for (const other of world().medicalReports) {
        if (
          other.tenantId === g.tenantId &&
          other.rootReportId === row.rootReportId &&
          other.id !== row.id &&
          other.status === 'APPROVED'
        ) {
          other.status = 'SUPERSEDED';
          other.rowVersion += 1;
        }
      }
      decideRow(row, guard.to, body.reviewComment ?? null, null, g.session.account.actorId);
      return answer(g.session, g.tenantId, row);
    }),

    http.post(`${ANY}/api/v1/medical-reports/:reportId/reject`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_REVIEW, true);
      if ('error' in g) return g.error;
      const replay = requireIdempotencyKey(api, request);
      if (replay) return replay;
      const guard = forCommand(
        request,
        g.session,
        g.tenantId,
        pathParam(params, 'reportId'),
        'reject',
      );
      if ('error' in guard) return guard.error;
      const own = ownFile(api, g.session, g.tenantId, guard.row.personId);
      if (own) return own;
      const body = await readJson<Schemas['RejectMedicalReport']>(request);
      if (!body) {
        return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      }
      const errors: FieldError[] = [];
      const reason = (body.rejectReasonCode ?? '').trim();
      if (reason === '') {
        errors.push({
          field: 'rejectReasonCode',
          code: 'REQUIRED',
          message: 'ret gerekçe kodu zorunlu',
        });
      } else if (!REASON_CODE.test(reason)) {
        errors.push({ field: 'rejectReasonCode', code: 'FORMAT', message: 'geçersiz kod biçimi' });
      }
      if ((body.reviewComment ?? '').length > MAX_SUMMARY) {
        errors.push({ field: 'reviewComment', code: 'LENGTH', message: 'en fazla 4000 karakter' });
      }
      if (errors.length > 0) return validationFailed(api, errors);
      decideRow(guard.row, guard.to, body.reviewComment ?? null, reason, g.session.account.actorId);
      return answer(g.session, g.tenantId, guard.row);
    }),

    http.post(`${ANY}/api/v1/medical-reports/:reportId/cancel`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_REPORT_MANAGE, true);
      if ('error' in g) return g.error;
      const replay = requireIdempotencyKey(api, request);
      if (replay) return replay;
      const guard = forCommand(
        request,
        g.session,
        g.tenantId,
        pathParam(params, 'reportId'),
        'cancel',
      );
      if ('error' in guard) return guard.error;
      guard.row.status = guard.to;
      guard.row.rowVersion += 1;
      return answer(g.session, g.tenantId, guard.row);
    }),

    http.get(`${ANY}/api/v1/medical-reports/:reportId/usages`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CASE_READ, false);
      if ('error' in g) return g.error;
      const row = findReport(g.session, g.tenantId, pathParam(params, 'reportId'));
      if (!row) return reportNotFound(api);
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) {
        return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      }
      const rows = world()
        .medicalReportUsages.filter((u) => u.tenantId === g.tenantId && u.reportId === row.id)
        .sort((a, b) => b.usedAt.localeCompare(a.usedAt) || b.id.localeCompare(a.id));
      const body: Schemas['MedicalReportUsagePage'] = {
        items: rows.slice(offset, offset + limit).map(({ tenantId: _tenantId, ...rest }) => rest),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),
  ];
}

/** Writes a reviewer's answer onto the row. Both decisions are one act with two words. */
function decideRow(
  row: StoredMedicalReport,
  status: Schemas['MedicalReportStatus'],
  comment: string | null,
  reasonCode: string | null,
  actorId: string,
): void {
  row.status = status;
  row.reviewComment = comment;
  row.rejectReasonCode = reasonCode;
  row.reviewedBy = actorId;
  row.reviewedAt = new Date().toISOString();
  row.rowVersion += 1;
}

/**
 * A report reference of the form the server mints, MR-YYYYMMDD-XXXXXXXX. The tail is drawn
 * from the world's own seeded stream rather than from Math.random, so a mock world is the
 * same world twice.
 */
function mintReference(world: MockWorld): string {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let tail = '';
  for (let i = 0; i < 8; i += 1) {
    tail += alphabet[Math.floor(world.random() * alphabet.length)];
  }
  const day = new Date().toISOString().slice(0, 10).replace(/-/g, '');
  return `MR-${day}-${tail}`;
}

function today(): string {
  return new Date().toISOString().slice(0, 10);
}
