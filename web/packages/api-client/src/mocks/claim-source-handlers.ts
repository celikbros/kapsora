import { http, HttpResponse } from 'msw';
import { mockAuthorizations } from './authorization-handlers';
import {
  currentVersionOf,
  fromMicros,
  toMicros,
  type StoredServiceRequest,
  type StoredHealthCase,
} from './data';
import {
  ANY,
  guardTenant,
  organizationScope,
  withinScope,
  problem,
  etagOf,
  parseIfMatch,
  readJson,
  requireIdempotencyKey,
  pathParam,
  wait,
  decodeCursor,
  encodeCursor,
  parseLimit,
  type MockApi,
  type MockSession,
  type Schemas,
} from './handlers';

/** UI test double. Real reservation/consumption atomicity is verified in PostgreSQL. */
export function claimSourceHandlers(
  api: MockApi,
  createDraft: (session: MockSession, tenantId: string, body: Schemas['CreateClaim']) => Response,
) {
  const sourceFor = (session: MockSession, tenantId: string, id: string) => {
    const row = api.world.healthCases.find(
      (c) =>
        c.id === id &&
        c.tenantId === tenantId &&
        c.caseType === 'OUTPATIENT' &&
        withinScope(organizationScope(api, session, tenantId), c.providerOrganizationId),
    );
    const request = api.world.serviceRequests.find(
      (r) =>
        r.id === row?.serviceRequestId &&
        r.tenantId === tenantId &&
        r.personId === row.personId &&
        r.programId === row.programId &&
        r.enrollmentId === row.enrollmentId &&
        r.providerOrganizationId === row.providerOrganizationId &&
        ['APPROVED', 'PARTIALLY_APPROVED'].includes(r.status),
    );
    if (!row || !request) return null;
    const authorizations = mockAuthorizations(api.world).filter(
      (a) =>
        a.tenantId === tenantId &&
        a.requestId === request.id &&
        ['ACTIVE', 'PARTIALLY_USED'].includes(a.status) &&
        Date.parse(a.validTo) > Date.now(),
    );
    const raised = api.world.claims.some(
      (c) => c.tenantId === tenantId && c.caseId === id && c.status !== 'CANCELLED',
    );
    return { row, request, authorizations, raised };
  };
  const summary = (
    row: StoredHealthCase,
    request: StoredServiceRequest,
  ): Schemas['ClaimCaseSource'] => {
    const person = api.world.people.find(
      (p) => p.tenantId === row.tenantId && p.id === row.personId,
    );
    return {
      caseId: row.id,
      rowVersion: row.rowVersion,
      openedAt: row.openedAt,
      serviceDate: request.serviceDate,
      requestReference: request.reference,
      personDisplayName: person
        ? [person.firstName, person.middleName, person.lastName].filter(Boolean).join(' ')
        : '',
    };
  };
  const resolve = (session: MockSession, tenantId: string, id: string) => {
    const source = sourceFor(session, tenantId, id);
    if (!source) return { error: problem(api, 404, 'CLAIM_SOURCE_NOT_FOUND', 'Vaka bulunamadı') };
    if (source.raised)
      return {
        error: problem(api, 409, 'CLAIM_CASE_ALREADY_CLAIMED', 'Bu vaka için dosya zaten var'),
      };
    const notReady = () => ({
      error: problem(api, 409, 'CLAIM_SOURCE_NOT_READY', 'Vaka faturalamaya hazır değil'),
    });
    const encounters = api.world.encounters.filter(
      (e) => e.tenantId === tenantId && e.caseId === id,
    );
    const diagnoses = api.world.diagnoses.filter(
      (d) =>
        d.tenantId === tenantId &&
        d.diagnosisType === 'PRIMARY' &&
        encounters.some((e) => e.id === d.encounterId && e.endedAt),
    );
    if (
      source.authorizations.length !== 1 ||
      diagnoses.length !== 1 ||
      encounters.some((e) => !e.endedAt)
    )
      return notReady();
    const auth = source.authorizations[0]!;
    const lines: (Schemas['ClaimCaseSourceLine'] & { reportId: string })[] = [];
    for (const item of auth.items.filter(
      (i) => toMicros(i.approvedQuantity) > toMicros(i.consumedQuantity),
    )) {
      const reports = api.world.medicalReports.filter(
        (r) =>
          r.tenantId === tenantId &&
          r.caseId === id &&
          r.personId === source.row.personId &&
          r.issuingProviderOrganizationId === source.row.providerOrganizationId &&
          r.status === 'APPROVED' &&
          r.validFrom <= source.request.serviceDate &&
          r.validTo >= source.request.serviceDate &&
          api.world.medicalReportServices.some(
            (s) =>
              s.tenantId === tenantId &&
              s.reportId === r.id &&
              s.serviceDefinitionId === item.serviceDefinitionId,
          ),
      );
      const service = api.world.serviceDefinitions.find(
        (s) => s.tenantId === tenantId && s.id === item.serviceDefinitionId,
      );
      const requestItem = currentVersionOf(api.world, source.request)?.items.find(
        (i) => i.id === item.requestItemId,
      );
      if (
        reports.length !== 1 ||
        !service ||
        !requestItem ||
        lines.some((l) => l.serviceDefinitionId === service.id)
      )
        return notReady();
      lines.push({
        serviceDefinitionId: service.id,
        serviceCode: service.code,
        serviceName: service.name,
        unitType: requestItem.unitType,
        quantity: fromMicros(toMicros(item.approvedQuantity) - toMicros(item.consumedQuantity)),
        reportId: reports[0]!.id,
      });
    }
    if (!lines.length) return notReady();
    return { source, auth, diagnosisId: diagnoses[0]!.id, lines };
  };
  const fingerprints = new Map<string, string>();
  return [
    http.get(`${ANY}/api/v1/claims/case-sources`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'claim.create', false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const offset = decodeCursor(url.searchParams.get('cursor'));
      const limit = parseLimit(url);
      if (offset === null || limit === 'invalid')
        return problem(api, 400, 'INVALID_PAGING', 'Sayfalama geçersiz');
      const rows = api.world.healthCases
        .map((c) => sourceFor(g.session, g.tenantId, c.id))
        .filter((s) => s && s.authorizations.length && !s.raised)
        .map((s) => summary(s!.row, s!.request))
        .sort((a, b) => b.openedAt.localeCompare(a.openedAt) || b.caseId.localeCompare(a.caseId));
      return HttpResponse.json({
        items: rows.slice(offset, offset + limit),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      });
    }),
    http.get(`${ANY}/api/v1/claims/case-sources/:caseId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'claim.create', false);
      if ('error' in g) return g.error;
      const found = resolve(g.session, g.tenantId, pathParam(params, 'caseId'));
      if ('error' in found) return found.error;
      return HttpResponse.json(
        {
          source: summary(found.source.row, found.source.request),
          lines: found.lines.map(({ reportId: _reportId, ...line }) => line),
        },
        { headers: { ETag: etagOf(found.source.row.rowVersion) } },
      );
    }),
    http.post(`${ANY}/api/v1/claims/case-sources/:caseId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'claim.create', true);
      if ('error' in g) return g.error;
      const missing = requireIdempotencyKey(api, request);
      if (missing) return missing;
      const id = pathParam(params, 'caseId');
      const body = await readJson<Schemas['CreateClaimFromCase']>(request);
      const key = `claim-source:${g.tenantId}:${g.session.account.actorId}:${id}:${request.headers.get('Idempotency-Key')}`;
      const fingerprint = JSON.stringify(body);
      const replay = api.replay(key);
      if (replay) {
        if (fingerprints.get(key) !== fingerprint)
          return problem(api, 409, 'IDEMPOTENCY_CONFLICT', 'İstek içeriği değişti');
        return HttpResponse.json(replay.body as Schemas['Claim'], {
          status: replay.status,
          headers: { ETag: replay.etag! },
        });
      }
      const found = resolve(g.session, g.tenantId, id);
      if ('error' in found) return found.error;
      const expected = parseIfMatch(request.headers.get('If-Match'));
      if (expected === null) return problem(api, 428, 'PRECONDITION_REQUIRED', 'Sürüm gerekli');
      if (expected !== found.source.row.rowVersion)
        return problem(api, 412, 'VERSION_MISMATCH', 'Vaka değişti');
      if (!body?.lines?.length || body.lines.length > 100)
        return problem(api, 422, 'VALIDATION_FAILED', 'Hizmet satırı gerekli');
      const seen = new Set<string>();
      const lines: Schemas['NewClaimLine'][] = [];
      for (const charge of body.lines) {
        const line = found.lines.find((l) => l.serviceDefinitionId === charge.serviceDefinitionId);
        if (
          !line ||
          seen.has(charge.serviceDefinitionId) ||
          !/^\d{1,14}(\.\d{1,6})?$/.test(charge.quantity) ||
          !/^\d{1,14}(\.\d{1,6})?$/.test(charge.lineAmount) ||
          toMicros(charge.quantity) <= 0n ||
          toMicros(charge.quantity) > toMicros(line.quantity)
        )
          return problem(api, 422, 'VALIDATION_FAILED', 'Hizmet, miktar veya tutar geçersiz');
        seen.add(charge.serviceDefinitionId);
        lines.push({
          lineNo: lines.length + 1,
          serviceDefinitionId: line.serviceDefinitionId,
          unitType: line.unitType,
          quantity: charge.quantity,
          lineAmount: charge.lineAmount,
          currencyCode: 'TRY',
          diagnosisId: found.diagnosisId,
          medicalReportId: line.reportId,
        });
      }
      const row = found.source.row;
      const response = createDraft(g.session, g.tenantId, {
        personId: row.personId,
        programId: row.programId,
        enrollmentId: row.enrollmentId,
        providerOrganizationId: row.providerOrganizationId!,
        caseId: id,
        authorizationId: found.auth.id,
        serviceDateFrom: found.source.request.serviceDate,
        serviceDateTo: found.source.request.serviceDate,
        lines,
      });
      // Existing claim mock owns consumption; expose this request hold to that stand-in.
      if (!api.world.claimAuthorizations.some((a) => a.id === found.auth.id))
        api.world.claimAuthorizations.push({
          id: found.auth.id,
          tenantId: g.tenantId,
          reference: found.auth.reference,
          personId: row.personId,
          items: found.auth.items.map((i) => ({
            serviceDefinitionId: i.serviceDefinitionId,
            approvedQuantity: i.approvedQuantity,
            consumedQuantity: i.consumedQuantity,
          })),
        });
      const output: unknown = await response.clone().json();
      fingerprints.set(key, fingerprint);
      api.rememberIdempotent(key, response.status, output, response.headers.get('ETag'));
      return response;
    }),
  ];
}
