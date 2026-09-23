/**
 * MSW handlers for the M4 service request lifecycle: the request, the lines of its
 * current version, the six commands and the version history.
 *
 * Three rules carry the module and each of them is a rule a screen can be written wrong
 * against. Nothing writes a status: every move is its own route with its own permission,
 * its own precondition and its own reason, and a PATCH carrying `status` is answered 422
 * with the field code IMMUTABLE rather than quietly ignored. A submit never rests at
 * SUBMITTED — the eligibility resolver and the tenant's published DOCUMENT and PREAUTH
 * rules run inside it and it lands on ELIGIBILITY_FAILED, PENDING_DOCUMENT,
 * PENDING_REVIEW or APPROVED. And a returned request is corrected in a new version: the
 * decided version is frozen and stays readable exactly as it was submitted, which is what
 * makes a correction auditable rather than a row that changed its mind.
 */
import { HttpResponse, http, type HttpHandler, type PathParams } from 'msw';

import {
  currentVersionOf,
  draftVersionOf,
  isDecimalText,
  pseudoHash,
  toDecimal,
  toMicros,
  fromMicros,
  toServiceRequest,
  toServiceRequestVersion,
  toServiceRequestVersionSummary,
  type MockWorld,
  type StoredServiceRequest,
  type StoredServiceRequestItem,
  type StoredServiceRequestVersion,
} from './data';
import { resolveEligibility } from './eligibility-handlers';
import type { MockApi, MockSession } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  organizationScope,
  parseLimit,
  pathParam,
  problem,
  readJson,
  requireIdempotencyKey,
  requireIfMatch,
  requireMergePatch,
  validationFailed,
  wait,
  withinScope,
  type FieldError,
  type Schemas,
  ownFile,
} from './handlers';
import { runRules } from './rules-handlers';

const REASON_CODE = /^[A-Z][A-Z0-9_.:-]{1,79}$/;
const CURRENCY = /^[A-Z]{3}$/;
const MAX_ITEMS = 100;
const MAX_REASON_TEXT = 1000;

const REQUEST_TYPES = new Set<string>([
  'DIRECT_SERVICE',
  'PREAUTHORIZATION',
  'RESERVATION',
  'REIMBURSEMENT',
]);
const CHANNELS = new Set<string>([
  'BACKOFFICE',
  'PROVIDER_PORTAL',
  'MEMBER_PORTAL',
  'API',
  'BATCH_IMPORT',
  'CALL_CENTER',
]);
const UNIT_TYPES = new Set<string>([
  'MONEY',
  'COUNT',
  'NIGHT',
  'SESSION',
  'HOUR',
  'KILOMETER',
  'POINT',
]);
const STATUSES = new Set<string>([
  'DRAFT',
  'SUBMITTED',
  'ELIGIBILITY_FAILED',
  'PENDING_DOCUMENT',
  'PENDING_REVIEW',
  'APPROVED',
  'PARTIALLY_APPROVED',
  'REJECTED',
  'CANCELLED',
  'EXPIRED',
  'CLOSED',
]);
const DECISION_ITEM_STATUSES = new Set<string>(['APPROVED', 'PARTIALLY_APPROVED', 'REJECTED']);
/** A preauthorization and a reservation are both promises made to somebody named. */
const REQUIRES_PROVIDER = new Set<string>(['PREAUTHORIZATION', 'RESERVATION']);

/**
 * The whole lifecycle. Nothing outside this table is a legal move, and there is no entry
 * that leads out of REJECTED, CANCELLED, EXPIRED or CLOSED: a finished request stays
 * finished, and asking again is a new request naming this one.
 */
const TRANSITIONS: Record<string, Partial<Record<string, Schemas['ServiceRequestStatus']>>> = {
  SUBMIT: { DRAFT: 'SUBMITTED' },
  RETURN: { PENDING_REVIEW: 'DRAFT', PENDING_DOCUMENT: 'DRAFT' },
  REJECT: { PENDING_REVIEW: 'REJECTED' },
  APPROVE: { PENDING_REVIEW: 'APPROVED' },
  PARTIALLY_APPROVE: { PENDING_REVIEW: 'PARTIALLY_APPROVED' },
  CANCEL: {
    DRAFT: 'CANCELLED',
    SUBMITTED: 'CANCELLED',
    PENDING_REVIEW: 'CANCELLED',
    PENDING_DOCUMENT: 'CANCELLED',
    ELIGIBILITY_FAILED: 'CANCELLED',
  },
};

function transitionTarget(command: string, from: string): Schemas['ServiceRequestStatus'] | null {
  return TRANSITIONS[command]?.[from] ?? null;
}

const REFERENCE_ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';

/** The server's reference shape: the day it was raised plus eight base32 characters. */
function newReference(world: MockWorld, createdAt: string): string {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    let tail = '';
    for (let i = 0; i < 8; i += 1) {
      tail += REFERENCE_ALPHABET[Math.floor(world.random() * REFERENCE_ALPHABET.length)]!;
    }
    const reference = `SR-${createdAt.slice(0, 10).replace(/-/g, '')}-${tail}`;
    if (!world.serviceRequests.some((r) => r.reference === reference)) return reference;
  }
  return '';
}

function requestNotFound(api: MockApi): Response {
  return problem(api, 404, 'SERVICE_REQUEST_NOT_FOUND', 'Hizmet talebi bulunamadı');
}

function versionNotFound(api: MockApi): Response {
  return problem(api, 404, 'SERVICE_REQUEST_VERSION_NOT_FOUND', 'Talep sürümü bulunamadı');
}

function transitionInvalid(api: MockApi): Response {
  return problem(api, 409, 'REQUEST_TRANSITION_INVALID', 'Bu durum geçişi yapılamaz', {
    detail: 'Talebin bulunduğu durumda bu komut geçerli değil.',
  });
}

function versionImmutable(api: MockApi): Response {
  return problem(
    api,
    409,
    'SERVICE_REQUEST_VERSION_IMMUTABLE',
    'Gönderilmiş talep sürümü değiştirilemez',
    {
      detail: 'Bu sürümle verilmiş kararlar var; talep geri gönderildiğinde yeni bir sürüm açılır.',
    },
  );
}

function etagMismatch(api: MockApi): Response {
  return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti', {
    detail: 'Güncel sürümü alıp değişikliğinizi yeniden uygulayın.',
  });
}

// --- validation ------------------------------------------------------------------------

function validateReason(reasonCode: unknown, reasonText: unknown): FieldError[] {
  const errors: FieldError[] = [];
  if (typeof reasonCode !== 'string' || !REASON_CODE.test(reasonCode)) {
    errors.push({
      field: 'reasonCode',
      code: 'FORMAT',
      message: 'büyük harf, rakam ve alt çizgi; 2-80 karakter',
    });
  }
  if (typeof reasonText === 'string' && [...reasonText].length > MAX_REASON_TEXT) {
    errors.push({ field: 'reasonText', code: 'LENGTH', message: 'en fazla 1000 karakter' });
  }
  return errors;
}

/**
 * One requested line. Both decimals arrive as exact text and stay exact: a quantity that
 * passed through a float is a quantity nobody can reconcile against the ledger.
 */
function validateItems(
  world: MockWorld,
  tenantId: string,
  items: Schemas['ServiceRequestItemInput'][] | undefined,
): FieldError[] {
  const errors: FieldError[] = [];
  if (!Array.isArray(items) || items.length === 0) {
    errors.push({ field: 'items', code: 'REQUIRED', message: 'en az bir kalem gerekli' });
    return errors;
  }
  if (items.length > MAX_ITEMS) {
    errors.push({ field: 'items', code: 'RANGE', message: 'en fazla 100 kalem gönderilebilir' });
    return errors;
  }
  items.forEach((item, i) => {
    const path = `items[${i}]`;
    if (!item?.serviceDefinitionId) {
      errors.push({ field: `${path}.serviceDefinitionId`, code: 'REQUIRED' });
    } else {
      const definition = world.serviceDefinitions.find(
        (d) => d.id === item.serviceDefinitionId && d.tenantId === tenantId,
      );
      if (!definition) {
        errors.push({ field: `${path}.serviceDefinitionId`, code: 'NOT_FOUND' });
      } else if (!definition.active) {
        errors.push({ field: `${path}.serviceDefinitionId`, code: 'INACTIVE' });
      }
    }
    if (typeof item?.requestedQuantity !== 'string' || !isDecimalText(item.requestedQuantity)) {
      errors.push({ field: `${path}.requestedQuantity`, code: 'FORMAT' });
    } else if (toMicros(item.requestedQuantity) <= 0n) {
      errors.push({ field: `${path}.requestedQuantity`, code: 'RANGE' });
    }
    if (!UNIT_TYPES.has(item?.unitType as string)) {
      errors.push({ field: `${path}.unitType`, code: 'ENUM' });
    }
    if (item?.requestedAmount !== undefined) {
      if (!isDecimalText(item.requestedAmount)) {
        errors.push({ field: `${path}.requestedAmount`, code: 'FORMAT' });
      } else if (toMicros(item.requestedAmount) < 0n) {
        errors.push({ field: `${path}.requestedAmount`, code: 'RANGE' });
      }
    }
    if (item?.currencyCode !== undefined) {
      if (!CURRENCY.test(item.currencyCode)) {
        errors.push({ field: `${path}.currencyCode`, code: 'FORMAT' });
      }
      if (item.requestedAmount === undefined) {
        errors.push({ field: `${path}.requestedAmount`, code: 'REQUIRED' });
      }
    }
  });
  return errors;
}

function toStoredItems(
  world: MockWorld,
  items: Schemas['ServiceRequestItemInput'][],
): StoredServiceRequestItem[] {
  return items.map((item, i) => ({
    id: world.nextId(),
    lineNo: i + 1,
    serviceDefinitionId: item.serviceDefinitionId,
    unitType: item.unitType,
    requestedQuantity: item.requestedQuantity,
    requestedAmount: item.requestedAmount ?? null,
    currencyCode: item.currencyCode ?? null,
    status: 'REQUESTED',
    approvedQuantity: null,
    approvedAmount: null,
    decisionReasonCode: null,
  }));
}

// --- the submit gate --------------------------------------------------------------------

interface GateDecision {
  status: Schemas['ServiceRequestStatus'];
  reasonCode: string;
  requiredDocumentTypes: string[] | null;
  eligibilityEvaluationId: string | null;
  ruleEvaluationId: string | null;
}

/**
 * Section 2.3 of the work package in the order the server runs it: eligibility first,
 * then the published DOCUMENT and PREAUTH rules, then the program's own answer to "does a
 * person still have to look at this". A missing document blocks before a review does,
 * because nobody can review what has not been produced yet.
 */
function runGate(
  world: MockWorld,
  tenantId: string,
  actorId: string,
  request: StoredServiceRequest,
  items: StoredServiceRequestItem[],
): GateDecision {
  const evaluationId = world.nextId();
  const codes = items.map(
    (item) => world.serviceDefinitions.find((d) => d.id === item.serviceDefinitionId)?.code ?? '',
  );
  // The service definition's own code names the entitlement, which is the convention the
  // server's resolver is given too; a line whose code matches nothing is left for a
  // reviewer with SERVICE_MAPPING_PENDING rather than silently treated as covered.
  const eligibility = resolveEligibility(
    world,
    tenantId,
    {
      personId: request.personId,
      programId: request.programId,
      serviceDate: request.serviceDate,
      serviceItems: items.map((item) => ({
        serviceDefinitionId: item.serviceDefinitionId,
        quantity: item.requestedQuantity,
      })),
      context: { entitlementCodes: codes },
    },
    evaluationId,
  );
  world.evaluations.set(evaluationId, {
    id: evaluationId,
    tenantId,
    personId: request.personId,
    programId: request.programId,
    planVersionId: eligibility.planVersionId ?? null,
    enrollmentId: eligibility.enrollmentId ?? null,
    serviceDate: request.serviceDate,
    evaluatedAt: eligibility.evaluatedAt,
    evaluatedBy: actorId,
    outcome: eligibility.outcome,
    request: {
      personId: request.personId,
      serviceDate: request.serviceDate,
      serviceItems: [],
    },
    result: eligibility,
  });

  // A person who may not use the benefit at all is told so now. Running the document and
  // preauthorization rules on top would ask for paperwork nobody needs to produce.
  if (eligibility.outcome === 'INELIGIBLE' || eligibility.outcome === 'MISSING_DATA') {
    const first =
      eligibility.explanations.find((e) => e.severity === 'ERROR') ?? eligibility.explanations[0];
    return {
      status: 'ELIGIBILITY_FAILED',
      reasonCode: first?.code ?? 'ELIGIBILITY_FAILED',
      requiredDocumentTypes: null,
      eligibilityEvaluationId: evaluationId,
      ruleEvaluationId: null,
    };
  }

  const { documents, review, ruleEvaluationId } = runGateRules(
    world,
    tenantId,
    actorId,
    request,
    items,
    codes,
    eligibility.outcome,
    eligibility.eligible,
  );

  const satisfied = new Set(
    world.documentLinks
      .filter((link) => {
        if (
          link.tenantId !== tenantId ||
          link.aggregateType !== 'SERVICE_REQUEST' ||
          link.aggregateId !== request.id ||
          !documents.includes(link.documentTypeCode)
        )
          return false;
        const doc = world.documents.find(
          (d) => d.tenantId === tenantId && d.id === link.documentId,
        );
        if (!doc || doc.scanStatus !== 'CLEAN' || doc.bucket !== 'secure' || doc.purgedAt)
          return false;
        if (
          request.providerOrganizationId &&
          doc.ownerOrganizationId &&
          doc.ownerOrganizationId !== request.providerOrganizationId
        )
          return false;
        const stored = doc.duplicateOfDocumentId
          ? world.documents.find(
              (d) => d.tenantId === tenantId && d.id === doc.duplicateOfDocumentId,
            )
          : doc;
        return (
          !!stored &&
          stored.scanStatus === 'CLEAN' &&
          stored.bucket === 'secure' &&
          !stored.purgedAt
        );
      })
      .map((link) => link.documentTypeCode),
  );
  if (documents.some((code) => !satisfied.has(code))) {
    return {
      status: 'PENDING_DOCUMENT',
      reasonCode: 'DOCUMENT_REQUIRED',
      requiredDocumentTypes: documents,
      eligibilityEvaluationId: evaluationId,
      ruleEvaluationId,
    };
  }
  if (review) {
    return {
      status: 'PENDING_REVIEW',
      reasonCode: 'RULE_REVIEW_REQUIRED',
      requiredDocumentTypes: documents,
      eligibilityEvaluationId: evaluationId,
      ruleEvaluationId,
    };
  }
  if (eligibility.outcome !== 'ELIGIBLE') {
    // PARTIALLY_ELIGIBLE and REVIEW_REQUIRED both mean something could not be settled by
    // arithmetic, which is exactly what a reviewer is for.
    return {
      status: 'PENDING_REVIEW',
      reasonCode: 'ELIGIBILITY_REVIEW_REQUIRED',
      requiredDocumentTypes: documents,
      eligibilityEvaluationId: evaluationId,
      ruleEvaluationId,
    };
  }
  // Nothing objected. Whether that is an approval or still a review is the program's
  // setting rather than a rule hard-coded here. An empty non-null list says "the rules
  // were asked and required nothing", which is a different statement from the null a
  // request that has never been submitted carries.
  const program = world.programs.find((p) => p.id === request.programId);
  const reviewRequired = program?.reviewRequired ?? true;
  return {
    status: reviewRequired ? 'PENDING_REVIEW' : 'APPROVED',
    reasonCode: reviewRequired ? 'PROGRAM_REVIEW_REQUIRED' : 'AUTO_APPROVED',
    requiredDocumentTypes: documents,
    eligibilityEvaluationId: evaluationId,
    ruleEvaluationId,
  };
}

/**
 * Every published DOCUMENT and PREAUTH version covering the service date, evaluated over
 * one input document: the whole request rather than one line. A rule never refuses a
 * request outright — a refusal is a decision somebody has to be answerable for — so every
 * matched action other than APPROVE, WARN and REQUIRE_DOCUMENT means "a person decides".
 */
function runGateRules(
  world: MockWorld,
  tenantId: string,
  actorId: string,
  request: StoredServiceRequest,
  items: StoredServiceRequestItem[],
  codes: string[],
  eligibilityOutcome: string,
  eligible: boolean,
): { documents: string[]; review: boolean; ruleEvaluationId: string | null } {
  const versions = world.ruleSetVersions.filter((version) => {
    if (version.tenantId !== tenantId || version.status !== 'PUBLISHED') return false;
    const set = world.ruleSets.find((s) => s.id === version.ruleSetId);
    if (!set || (set.purpose !== 'DOCUMENT' && set.purpose !== 'PREAUTH')) return false;
    if (version.validFrom !== null && request.serviceDate < version.validFrom) return false;
    return version.validTo === null || request.serviceDate < version.validTo;
  });
  if (versions.length === 0) return { documents: [], review: false, ruleEvaluationId: null };

  let totalQuantity = 0n;
  let totalAmount = 0n;
  let currencyCode = '';
  for (const item of items) {
    totalQuantity += toMicros(item.requestedQuantity);
    if (item.requestedAmount) totalAmount += toMicros(item.requestedAmount);
    if (!currencyCode && item.currencyCode) currencyCode = item.currencyCode;
  }
  const input: Record<string, unknown> = {
    serviceDate: request.serviceDate,
    requestType: request.requestType,
    channel: request.channel,
    personId: request.personId,
    programId: request.programId,
    enrollmentId: request.enrollmentId,
    providerOrganizationId: request.providerOrganizationId ?? '',
    itemCount: items.length,
    totalQuantity: fromMicros(totalQuantity),
    totalAmount: fromMicros(totalAmount),
    currencyCode,
    serviceCodes: codes,
    unitTypes: items.map((i) => i.unitType),
    eligibilityOutcome,
    eligible,
  };

  const documents: string[] = [];
  const seen = new Set<string>();
  let review = false;
  const trace: Schemas['RuleEvaluationResultLine'][] = [];
  let sequence = 0;
  let decidingVersionId = versions[0]!.id;
  for (const version of versions) {
    const run = runRules(version, input);
    for (const line of run.results) {
      sequence += 1;
      trace.push({ ...line, sequence });
      if (!line.matched || !line.actionType) continue;
      if (line.actionType === 'APPROVE' || line.actionType === 'WARN') continue;
      if (line.actionType === 'REQUIRE_DOCUMENT') {
        for (const code of documentTypesOf(line.actionPayload)) {
          if (seen.has(code)) continue;
          seen.add(code);
          documents.push(code);
        }
        if (documents.length > 0 && decidingVersionId === versions[0]!.id) {
          decidingVersionId = version.id;
        }
        continue;
      }
      if (!review) decidingVersionId = version.id;
      review = true;
    }
  }
  documents.sort();

  const decidingVersion = versions.find((v) => v.id === decidingVersionId)!;
  const decidingSet = world.ruleSets.find((s) => s.id === decidingVersion.ruleSetId)!;
  const ruleEvaluationId = world.nextId();
  world.ruleEvaluations.push({
    tenantId,
    id: ruleEvaluationId,
    ruleSetVersionId: decidingVersion.id,
    ruleSetId: decidingSet.id,
    ruleSetCode: decidingSet.code,
    versionNo: decidingVersion.versionNo,
    subjectType: 'SERVICE_REQUEST',
    subjectId: request.id,
    outcome: review || documents.length > 0 ? 'REVIEW_REQUIRED' : 'APPROVED',
    inputHash: pseudoHash(JSON.stringify(input)),
    inputSnapshot: input,
    durationMs: 0,
    evaluatedAt: new Date().toISOString(),
    evaluatedBy: actorId,
    results: trace,
  });
  return { documents, review, ruleEvaluationId };
}

/**
 * The document codes a REQUIRE_DOCUMENT action names, in either shape. A rule that asks
 * for a document without naming one is answered with a code that says exactly that,
 * rather than with an empty list that would let the request through.
 */
function documentTypesOf(payload: Record<string, unknown> | null | undefined): string[] {
  const out: string[] = [];
  const one = payload?.['documentTypeCode'];
  if (typeof one === 'string' && one !== '') out.push(one);
  const many = payload?.['documentTypeCodes'];
  if (Array.isArray(many)) {
    for (const code of many) if (typeof code === 'string' && code !== '') out.push(code);
  }
  return out.length > 0 ? out : ['UNSPECIFIED'];
}

// --- handlers ----------------------------------------------------------------------------

export function serviceRequestHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  /**
   * The row, or nothing. The provider boundary is applied here rather than at the edge,
   * so a request belonging to another provider is not merely hidden from a page — it is
   * unreachable by id too, and answered 404 rather than 403: that such a request exists
   * at all is itself information about somebody else's business.
   */
  const find = (
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredServiceRequest | undefined => {
    const scope = organizationScope(api, session, tenantId);
    const row = world().serviceRequests.find((r) => r.id === id && r.tenantId === tenantId);
    if (!row) return undefined;
    return withinScope(scope, row.providerOrganizationId ?? null) ? row : undefined;
  };

  const answer = (request: StoredServiceRequest, status = 200): Response =>
    HttpResponse.json(toServiceRequest(world(), request), {
      status,
      headers: { ETag: etagOf(request.rowVersion) },
    });

  /** The precondition a PATCH and a PUT of the lines share. */
  const lockDraft = (
    session: MockSession,
    tenantId: string,
    id: string,
    expected: number,
  ): StoredServiceRequest | Response => {
    const request = find(session, tenantId, id);
    if (!request) return requestNotFound(api);
    // Editing anything that is not a draft is refused as an immutable version, which is
    // what it is: the version has been submitted and decisions were made against it.
    if (request.status !== 'DRAFT') return versionImmutable(api);
    if (request.rowVersion !== expected) return etagMismatch(api);
    return request;
  };

  /** Everything return, reject and cancel share except the permission and the target. */
  const reasonCommand =
    (
      permission: string,
      command: 'RETURN' | 'REJECT' | 'CANCEL',
      apply: (
        request: StoredServiceRequest,
        version: StoredServiceRequestVersion,
        reasonCode: string,
        reasonText: string | null,
        actorId: string,
        to: Schemas['ServiceRequestStatus'],
      ) => void,
    ) =>
    async ({ request: http_, params }: { request: Request; params: PathParams }) => {
      await wait(api);
      const g = guardTenant(api, http_, permission, true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, http_);
      if (missingKey) return missingKey;
      const expected = requireIfMatch(api, http_);
      if (typeof expected !== 'number') return expected;
      const body = await readJson<Schemas['ReasonCommand']>(http_);
      // A malformed reason is refused before the transaction, so it beats the transition
      // check and the ETag check exactly as it does on the server.
      const errors = validateReason(body?.reasonCode, body?.reasonText);
      if (errors.length > 0) return validationFailed(api, errors);
      const found = find(g.session, g.tenantId, pathParam(params, 'requestId'));
      if (!found) return requestNotFound(api);
      if (command !== 'CANCEL') {
        const own = ownFile(api, g.session, g.tenantId, found.personId);
        if (own) return own;
      }
      const replayKey = `request:${g.tenantId}:${g.session.account.actorId}:${command}:${found.id}:${http_.headers.get('Idempotency-Key')}`;
      const fingerprint = JSON.stringify(body);
      const replay = api.replay(replayKey);
      if (replay) {
        const saved = replay.body as { fingerprint: string; response: Schemas['ServiceRequest'] };
        if (saved.fingerprint !== fingerprint)
          return problem(
            api,
            409,
            'IDEMPOTENCY_KEY_REUSED',
            'Aynı işlem anahtarı farklı bir istekle kullanıldı',
          );
        return HttpResponse.json(saved.response, {
          status: replay.status,
          headers: { ETag: replay.etag! },
        });
      }
      const to = transitionTarget(command, found.status);
      if (!to) return transitionInvalid(api);
      if (found.rowVersion !== expected) return etagMismatch(api);
      const version = currentVersionOf(world(), found);
      if (!version) return versionNotFound(api);
      apply(
        found,
        version,
        body!.reasonCode,
        body!.reasonText?.trim() || null,
        g.session.account.actorId,
        to,
      );
      found.rowVersion += 1;
      const response = structuredClone(toServiceRequest(world(), found));
      api.rememberIdempotent(replayKey, 200, { fingerprint, response }, etagOf(found.rowVersion));
      return answer(found);
    };

  /** Everything approve and partiallyApprove share. */
  const decisionCommand =
    (command: 'APPROVE' | 'PARTIALLY_APPROVE', partial: boolean) =>
    async ({ request: http_, params }: { request: Request; params: PathParams }) => {
      await wait(api);
      const g = guardTenant(api, http_, 'service_request.review', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, http_);
      if (missingKey) return missingKey;
      const expected = requireIfMatch(api, http_);
      if (typeof expected !== 'number') return expected;
      const body = await readJson<Schemas['ServiceRequestDecision']>(http_);
      const decisions = body?.items ?? [];
      const errors = validateReason(body?.reasonCode, body?.reasonText);
      const seenLines = new Map<number, number>();
      decisions.forEach((d, i) => {
        const path = `items[${i}]`;
        if (!Number.isInteger(d?.lineNo) || d.lineNo < 1) {
          errors.push({ field: `${path}.lineNo`, code: 'RANGE' });
        } else if (seenLines.has(d.lineNo)) {
          errors.push({
            field: `${path}.lineNo`,
            code: 'DUPLICATE',
            message: `bu satır items[${seenLines.get(d.lineNo)!}] içinde de var`,
          });
        } else {
          seenLines.set(d.lineNo, i);
        }
        if (!DECISION_ITEM_STATUSES.has(d?.status as string)) {
          errors.push({ field: `${path}.status`, code: 'ENUM' });
        }
        if (d?.approvedQuantity !== undefined && !isDecimalText(d.approvedQuantity)) {
          errors.push({ field: `${path}.approvedQuantity`, code: 'FORMAT' });
        }
        if (d?.approvedAmount !== undefined && !isDecimalText(d.approvedAmount)) {
          errors.push({ field: `${path}.approvedAmount`, code: 'FORMAT' });
        }
        if (d?.decisionReasonCode !== undefined && !REASON_CODE.test(d.decisionReasonCode)) {
          errors.push({ field: `${path}.decisionReasonCode`, code: 'FORMAT' });
        }
      });
      if (partial) {
        // A partial approval that approved everything would leave a member reading a word
        // that does not match the numbers.
        if (decisions.length === 0) {
          errors.push({
            field: 'items',
            code: 'REQUIRED',
            message: 'kısmi onayda kalem kararları zorunlu',
          });
        } else if (decisions.every((d) => d.status === 'APPROVED')) {
          errors.push({
            field: 'items',
            code: 'NOT_PARTIAL',
            message: 'her kalem tam onaylandıysa bu kısmi onay değil; onay komutunu kullanın',
          });
        }
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const found = find(g.session, g.tenantId, pathParam(params, 'requestId'));
      if (!found) return requestNotFound(api);
      const own = ownFile(api, g.session, g.tenantId, found.personId);
      if (own) return own;
      const to = transitionTarget(command, found.status);
      if (!to) return transitionInvalid(api);
      if (found.rowVersion !== expected) return etagMismatch(api);
      const version = currentVersionOf(world(), found);
      if (!version) return versionNotFound(api);

      const byLine = new Map(version.items.map((i) => [i.lineNo, i]));
      const lineErrors: FieldError[] = [];
      decisions.forEach((d, i) => {
        const path = `items[${i}]`;
        const item = byLine.get(d.lineNo);
        if (!item) {
          lineErrors.push({
            field: `${path}.lineNo`,
            code: 'NOT_FOUND',
            message: 'bu satır numarası talepte yok',
          });
          return;
        }
        if (d.status !== 'REJECTED' && d.approvedQuantity !== undefined) {
          if (toMicros(d.approvedQuantity) > toMicros(item.requestedQuantity)) {
            lineErrors.push({
              field: `${path}.approvedQuantity`,
              code: 'RANGE',
              message: 'onaylanan miktar istenenden fazla olamaz',
            });
          }
        }
      });
      if (lineErrors.length > 0) return validationFailed(api, lineErrors);

      const decided = new Map(decisions.map((d) => [d.lineNo, d]));
      for (const item of version.items) {
        const d = decided.get(item.lineNo);
        if (!d) {
          // A line the reviewer did not mention is approved for what it asked for, which
          // is what "approve this request" means.
          item.status = 'APPROVED';
          item.approvedQuantity = item.requestedQuantity;
          item.approvedAmount = item.requestedAmount ?? null;
          item.decisionReasonCode = body!.reasonCode;
          continue;
        }
        item.status = d.status;
        item.decisionReasonCode = d.decisionReasonCode ?? body!.reasonCode;
        if (d.status === 'REJECTED') {
          // A refused line is refused for nothing; an approved quantity here would leave
          // two contradictory statements on the same row.
          item.approvedQuantity = toDecimal(0);
          item.approvedAmount = null;
        } else {
          item.approvedQuantity = d.approvedQuantity ?? item.requestedQuantity;
          item.approvedAmount = d.approvedAmount ?? item.requestedAmount ?? null;
        }
      }
      found.status = to;
      found.reviewComment = body!.reasonText?.trim() || null;
      found.closedAt = new Date().toISOString();
      found.rowVersion += 1;
      return answer(found);
    };

  return [
    http.get(`${ANY}/api/v1/service-requests`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'service_request.read', false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return validationFailed(api, [
          { field: 'limit', code: 'FORMAT', message: '1-200 arası tam sayı olmalı' },
        ]);
      }
      const errors: FieldError[] = [];
      const status = url.searchParams.get('status');
      if (status && !STATUSES.has(status)) {
        errors.push({
          field: 'status',
          code: 'ENUM',
          message: `geçerli değerler: ${[...STATUSES].join(', ')}`,
        });
      }
      const channel = url.searchParams.get('channel');
      if (channel && !CHANNELS.has(channel)) {
        errors.push({
          field: 'channel',
          code: 'ENUM',
          message: `geçerli değerler: ${[...CHANNELS].join(', ')}`,
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');

      const personId = url.searchParams.get('personId');
      const programId = url.searchParams.get('programId');
      const providerOrganizationId = url.searchParams.get('providerOrganizationId');
      const serviceDateFrom = url.searchParams.get('serviceDateFrom');
      const serviceDateTo = url.searchParams.get('serviceDateTo');
      const createdFrom = url.searchParams.get('createdFrom');
      const createdTo = url.searchParams.get('createdTo');
      const scope = organizationScope(api, g.session, g.tenantId);
      const rows = world()
        .serviceRequests.filter(
          (r) =>
            r.tenantId === g.tenantId &&
            withinScope(scope, r.providerOrganizationId ?? null) &&
            (!status || r.status === status) &&
            (!channel || r.channel === channel) &&
            (!personId || r.personId === personId) &&
            (!programId || r.programId === programId) &&
            (!providerOrganizationId || r.providerOrganizationId === providerOrganizationId) &&
            (!serviceDateFrom || r.serviceDate >= serviceDateFrom) &&
            (!serviceDateTo || r.serviceDate <= serviceDateTo) &&
            (!createdFrom || r.createdAt >= createdFrom) &&
            (!createdTo || r.createdAt <= createdTo),
        )
        // Newest first, tie-broken by descending id: the same keyset order the server pages in.
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      const page: Schemas['ServiceRequestPage'] = {
        items: rows.slice(offset, offset + limit).map((r) => toServiceRequest(world(), r)),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(page);
    }),

    http.post(`${ANY}/api/v1/service-requests`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'service_request.create', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const body = await readJson<Schemas['CreateServiceRequest']>(request);
      const errors: FieldError[] = [];
      if (!REQUEST_TYPES.has(body?.requestType as string)) {
        errors.push({ field: 'requestType', code: 'ENUM' });
      }
      if (!CHANNELS.has(body?.channel as string)) errors.push({ field: 'channel', code: 'ENUM' });
      if (!body?.serviceDate) errors.push({ field: 'serviceDate', code: 'REQUIRED' });
      if (!body?.personId || !body.enrollmentId) {
        errors.push({
          field: 'enrollmentId',
          code: 'REQUIRED',
          message: 'hak sahibi, program ve plan kaydı zorunlu',
        });
      }
      if (body?.requestedEndAt && !body.requestedStartAt) {
        errors.push({ field: 'requestedStartAt', code: 'REQUIRED' });
      }
      if (
        body?.requestedStartAt &&
        body.requestedEndAt &&
        body.requestedEndAt <= body.requestedStartAt
      ) {
        errors.push({ field: 'requestedEndAt', code: 'RANGE' });
      }
      errors.push(...validateItems(world(), g.tenantId, body?.items));
      const definitions = (body?.items ?? []).map((i) =>
        world().serviceDefinitions.find((d) => d.id === i.serviceDefinitionId),
      );
      const needsProvider =
        REQUIRES_PROVIDER.has(body?.requestType as string) ||
        definitions.some((d) => d?.requiresProvider === true);
      if (needsProvider && !body?.providerOrganizationId) {
        errors.push({ field: 'providerOrganizationId', code: 'REQUIRED' });
      }
      if (
        body?.providerOrganizationId &&
        !world().relationships.some(
          (r) => r.id === body.providerOrganizationId && r.tenantId === g.tenantId,
        )
      ) {
        errors.push({ field: 'providerOrganizationId', code: 'NOT_FOUND' });
      }
      if (body?.supersedesRequestId) {
        const superseded = world().serviceRequests.find(
          (r) => r.id === body.supersedesRequestId && r.tenantId === g.tenantId,
        );
        if (!superseded) {
          errors.push({ field: 'supersedesRequestId', code: 'NOT_FOUND' });
        } else if (superseded.status !== 'REJECTED') {
          errors.push({
            field: 'supersedesRequestId',
            code: 'CONFLICT',
            message: 'yalnız reddedilmiş bir talebin yerine yenisi açılabilir',
          });
        }
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const enrollment = world().enrollments.find(
        (e) => e.id === body!.enrollmentId && e.tenantId === g.tenantId,
      );
      // The program is the enrollment's. A caller may repeat it, in which case it has to
      // be the right one; a provider, who may read neither, sends none.
      if (
        !enrollment ||
        enrollment.personId !== body!.personId ||
        (body!.programId !== undefined && enrollment.programId !== body!.programId)
      ) {
        return problem(
          api,
          422,
          'SERVICE_REQUEST_ENROLLMENT_MISMATCH',
          'Plan kaydı bu kişiye veya programa ait değil',
        );
      }
      if (body!.serviceDate < enrollment.validFrom) {
        return validationFailed(api, [{ field: 'serviceDate', code: 'RANGE' }]);
      }
      if (enrollment.validTo !== null && body!.serviceDate >= enrollment.validTo) {
        return validationFailed(api, [{ field: 'serviceDate', code: 'RANGE' }]);
      }
      // A provider-scoped caller may only raise a request for an organization it holds.
      // This one is a 403, not a 404: the caller named the organization itself.
      const scope = organizationScope(api, g.session, g.tenantId);
      if (scope !== null && !withinScope(scope, body!.providerOrganizationId ?? null)) {
        return problem(
          api,
          403,
          'SERVICE_REQUEST_PROVIDER_SCOPE',
          'Bu sağlayıcı adına işlem yapamazsınız',
        );
      }

      const now = new Date().toISOString();
      const reference = newReference(world(), now);
      if (!reference) {
        return problem(
          api,
          409,
          'SERVICE_REQUEST_REFERENCE_UNAVAILABLE',
          'Talep numarası üretilemedi',
          { detail: 'Lütfen isteği yeniden gönderin.' },
        );
      }
      const created: StoredServiceRequest = {
        tenantId: g.tenantId,
        id: world().nextId(),
        reference,
        personId: body!.personId,
        programId: enrollment.programId,
        enrollmentId: body!.enrollmentId,
        providerOrganizationId: body!.providerOrganizationId ?? null,
        requestType: body!.requestType,
        channel: body!.channel,
        // The status cannot be chosen: a request always starts in DRAFT.
        status: 'DRAFT',
        serviceDate: body!.serviceDate,
        requestedStartAt: body!.requestedStartAt ?? null,
        requestedEndAt: body!.requestedEndAt ?? null,
        submittedAt: null,
        closedAt: null,
        createdAt: now,
        currentVersionNo: 1,
        supersedesRequestId: body!.supersedesRequestId ?? null,
        eligibilityEvaluationId: null,
        ruleEvaluationId: null,
        // Null, not []: the rules have not been asked yet.
        requiredDocumentTypes: null,
        returnReasonCode: null,
        rejectReasonCode: null,
        reviewComment: null,
        rowVersion: 1,
      };
      world().serviceRequests.push(created);
      world().serviceRequestVersions.push({
        id: world().nextId(),
        tenantId: g.tenantId,
        serviceRequestId: created.id,
        versionNo: 1,
        status: 'DRAFT',
        submittedAt: null,
        submittedBy: null,
        returnedAt: null,
        returnedBy: null,
        returnReasonCode: null,
        returnReasonText: null,
        createdAt: now,
        items: toStoredItems(world(), body!.items),
        snapshotItems: null,
      });
      return HttpResponse.json(toServiceRequest(world(), created), {
        status: 201,
        headers: {
          ETag: etagOf(created.rowVersion),
          Location: `/api/v1/service-requests/${created.id}`,
        },
      });
    }),

    http.get(`${ANY}/api/v1/service-requests/:requestId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'service_request.read', false);
      if ('error' in g) return g.error;
      const found = find(g.session, g.tenantId, pathParam(params, 'requestId'));
      if (!found) return requestNotFound(api);
      return answer(found);
    }),

    http.patch(`${ANY}/api/v1/service-requests/:requestId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'service_request.create', true);
      if ('error' in g) return g.error;
      // The media type is checked before the If-Match, exactly as the server checks it.
      const wrongType = requireMergePatch(api, request);
      if (wrongType) return wrongType;
      const expected = requireIfMatch(api, request);
      if (typeof expected !== 'number') return expected;
      const patch = (await readJson<Record<string, unknown>>(request)) ?? {};

      // A field this endpoint does not own is refused rather than ignored: a caller who is
      // allowed to send `status` will keep believing it did something.
      const errors: FieldError[] = [];
      const writable = new Set([
        'providerOrganizationId',
        'serviceDate',
        'requestedStartAt',
        'requestedEndAt',
      ]);
      const immutable = new Set([
        'status',
        'reference',
        'requestType',
        'personId',
        'programId',
        'enrollmentId',
        'channel',
        'currentVersionNo',
        'supersedesRequestId',
        'items',
        'rowVersion',
        'eligibilityEvaluationId',
        'ruleEvaluationId',
        'requiredDocumentTypes',
        'returnReasonCode',
        'rejectReasonCode',
        'reviewComment',
        'submittedAt',
        'closedAt',
      ]);
      for (const key of Object.keys(patch)) {
        if (writable.has(key)) continue;
        errors.push(
          immutable.has(key)
            ? {
                field: key,
                code: 'IMMUTABLE',
                message:
                  'bu alan bu uçtan değiştirilemez; durum değişiklikleri kendi komutlarıyla yapılır',
              }
            : { field: key, code: 'UNKNOWN_FIELD', message: 'bilinmeyen alan' },
        );
      }
      if ('serviceDate' in patch && patch['serviceDate'] === null) {
        errors.push({
          field: 'serviceDate',
          code: 'REQUIRED',
          message: 'hizmet tarihi kaldırılamaz',
        });
      }
      if (
        typeof patch['serviceDate'] === 'string' &&
        !/^\d{4}-\d{2}-\d{2}$/.test(patch['serviceDate'])
      ) {
        errors.push({
          field: 'serviceDate',
          code: 'FORMAT',
          message: 'YYYY-MM-DD biçiminde olmalı',
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const locked = lockDraft(g.session, g.tenantId, pathParam(params, 'requestId'), expected);
      if (!('id' in locked)) return locked;
      const next = {
        providerOrganizationId:
          'providerOrganizationId' in patch
            ? ((patch['providerOrganizationId'] as string | null) ?? null)
            : (locked.providerOrganizationId ?? null),
        requestedStartAt:
          'requestedStartAt' in patch
            ? ((patch['requestedStartAt'] as string | null) ?? null)
            : (locked.requestedStartAt ?? null),
        requestedEndAt:
          'requestedEndAt' in patch
            ? ((patch['requestedEndAt'] as string | null) ?? null)
            : (locked.requestedEndAt ?? null),
      };
      if (next.requestedEndAt && !next.requestedStartAt) {
        return validationFailed(api, [{ field: 'requestedStartAt', code: 'REQUIRED' }]);
      }
      if (
        next.requestedStartAt &&
        next.requestedEndAt &&
        next.requestedEndAt <= next.requestedStartAt
      ) {
        return validationFailed(api, [{ field: 'requestedEndAt', code: 'RANGE' }]);
      }
      const scope = organizationScope(api, g.session, g.tenantId);
      if (scope !== null && !withinScope(scope, next.providerOrganizationId)) {
        return problem(
          api,
          403,
          'SERVICE_REQUEST_PROVIDER_SCOPE',
          'Bu sağlayıcı adına işlem yapamazsınız',
        );
      }
      if (
        next.providerOrganizationId &&
        !world().relationships.some(
          (r) => r.id === next.providerOrganizationId && r.tenantId === g.tenantId,
        )
      ) {
        return validationFailed(api, [{ field: 'providerOrganizationId', code: 'NOT_FOUND' }]);
      }
      locked.providerOrganizationId = next.providerOrganizationId;
      locked.requestedStartAt = next.requestedStartAt;
      locked.requestedEndAt = next.requestedEndAt;
      if (typeof patch['serviceDate'] === 'string') locked.serviceDate = patch['serviceDate'];
      locked.rowVersion += 1;
      return answer(locked);
    }),

    http.put(`${ANY}/api/v1/service-requests/:requestId/items`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'service_request.create', true);
      if ('error' in g) return g.error;
      const expected = requireIfMatch(api, request);
      if (typeof expected !== 'number') return expected;
      const body = await readJson<Schemas['ServiceRequestItems']>(request);
      const locked = lockDraft(g.session, g.tenantId, pathParam(params, 'requestId'), expected);
      if (!('id' in locked)) return locked;
      const draft = draftVersionOf(world(), locked);
      if (!draft) {
        return problem(
          api,
          409,
          'SERVICE_REQUEST_DRAFT_MISSING',
          'Talebin düzenlenebilir bir sürümü yok',
          { detail: 'Talebi düzeltmek için önce inceleyenden geri gönderilmesini isteyin.' },
        );
      }
      const errors = validateItems(world(), g.tenantId, body?.items);
      if (errors.length > 0) return validationFailed(api, errors);
      // A replacement rather than a patch: a line number identifies a line only inside one
      // version, and a partial write would leave the caller guessing which of the lines it
      // sent survived.
      draft.items = toStoredItems(world(), body!.items);
      // The request row carries no line, and its ETag still has to change: a caller
      // holding the old tag wrote against a line set that no longer exists.
      locked.rowVersion += 1;
      return answer(locked);
    }),

    http.post(`${ANY}/api/v1/service-requests/:requestId/submit`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'service_request.submit', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const expected = requireIfMatch(api, request);
      if (typeof expected !== 'number') return expected;
      // The body is optional; an empty one is a submit with no comment.
      const body = await readJson<Schemas['ReviewComment']>(request);
      const comment = body?.comment ?? null;
      if (comment !== null && [...comment].length > MAX_REASON_TEXT) {
        return validationFailed(api, [
          { field: 'comment', code: 'LENGTH', message: 'en fazla 1000 karakter' },
        ]);
      }
      const found = find(g.session, g.tenantId, pathParam(params, 'requestId'));
      if (!found) return requestNotFound(api);
      if (!transitionTarget('SUBMIT', found.status)) return transitionInvalid(api);
      if (found.rowVersion !== expected) return etagMismatch(api);
      const draft = draftVersionOf(world(), found);
      if (!draft) {
        return problem(
          api,
          409,
          'SERVICE_REQUEST_DRAFT_MISSING',
          'Talebin düzenlenebilir bir sürümü yok',
        );
      }
      const errors: FieldError[] = [];
      if (draft.items.length === 0) {
        errors.push({
          field: 'items',
          code: 'REQUIRED',
          message: 'gönderim için en az bir kalem gerekli',
        });
      }
      if (!found.serviceDate) errors.push({ field: 'serviceDate', code: 'REQUIRED' });
      if (REQUIRES_PROVIDER.has(found.requestType) && !found.providerOrganizationId) {
        errors.push({
          field: 'providerOrganizationId',
          code: 'REQUIRED',
          message: 'bu talep türü için sağlayıcı zorunlu',
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const now = new Date().toISOString();
      // The version is frozen and the gate runs in one step, exactly as they are one
      // transaction on the server: a request that was frozen but not decided, or decided
      // against an evaluation that was rolled back, is a request nobody can explain.
      const decision = runGate(world(), g.tenantId, g.session.account.actorId, found, draft.items);
      draft.status = 'SUBMITTED';
      draft.submittedAt = now;
      draft.submittedBy = g.session.account.actorId;
      draft.snapshotItems = draft.items.map((i) => ({ ...i }));
      found.status = decision.status;
      found.submittedAt = now;
      found.eligibilityEvaluationId = decision.eligibilityEvaluationId;
      found.ruleEvaluationId = decision.ruleEvaluationId;
      found.requiredDocumentTypes = decision.requiredDocumentTypes;
      found.reviewComment = comment?.trim() || null;
      found.rowVersion += 1;
      return answer(found);
    }),

    http.post(
      `${ANY}/api/v1/service-requests/:requestId/return`,
      reasonCommand(
        'service_request.review',
        'RETURN',
        (found, version, reasonCode, reasonText, actorId, to) => {
          // The frozen version is marked as replaced and nothing else about it changes;
          // that is the only update the schema lets anybody make to it.
          version.status = 'SUPERSEDED';
          version.returnedAt = new Date().toISOString();
          version.returnedBy = actorId;
          version.returnReasonCode = reasonCode;
          version.returnReasonText = reasonText;
          const nextNo = found.currentVersionNo + 1;
          world().serviceRequestVersions.push({
            id: world().nextId(),
            tenantId: found.tenantId,
            serviceRequestId: found.id,
            versionNo: nextNo,
            status: 'DRAFT',
            submittedAt: null,
            submittedBy: null,
            returnedAt: version.returnedAt,
            returnedBy: actorId,
            returnReasonCode: reasonCode,
            returnReasonText: reasonText,
            createdAt: version.returnedAt,
            // The requester corrects what was sent, not an empty form — as fresh
            // REQUESTED lines, because the decisions were about the old version.
            items: version.items.map((item, i): StoredServiceRequestItem => ({
              id: world().nextId(),
              lineNo: i + 1,
              serviceDefinitionId: item.serviceDefinitionId,
              unitType: item.unitType,
              requestedQuantity: item.requestedQuantity,
              requestedAmount: item.requestedAmount ?? null,
              currencyCode: item.currencyCode ?? null,
              status: 'REQUESTED',
              approvedQuantity: null,
              approvedAmount: null,
              decisionReasonCode: null,
            })),
            snapshotItems: null,
          });
          found.currentVersionNo = nextNo;
          found.status = to;
          found.returnReasonCode = reasonCode;
          found.reviewComment = reasonText;
        },
      ),
    ),

    http.post(
      `${ANY}/api/v1/service-requests/:requestId/reject`,
      reasonCommand(
        'service_request.review',
        'REJECT',
        (found, version, reasonCode, reasonText, _actorId, to) => {
          for (const item of version.items) {
            item.status = 'REJECTED';
            item.decisionReasonCode = reasonCode;
          }
          found.status = to;
          found.rejectReasonCode = reasonCode;
          found.reviewComment = reasonText;
          found.closedAt = new Date().toISOString();
        },
      ),
    ),

    http.post(
      `${ANY}/api/v1/service-requests/:requestId/cancel`,
      reasonCommand(
        'service_request.cancel',
        'CANCEL',
        (found, version, reasonCode, reasonText, _actorId, to) => {
          for (const item of version.items) {
            item.status = 'CANCELLED';
            item.decisionReasonCode = reasonCode;
          }
          found.status = to;
          found.reviewComment = reasonText;
          found.closedAt = new Date().toISOString();
        },
      ),
    ),

    http.post(
      `${ANY}/api/v1/service-requests/:requestId/approve`,
      decisionCommand('APPROVE', false),
    ),
    http.post(
      `${ANY}/api/v1/service-requests/:requestId/partially-approve`,
      decisionCommand('PARTIALLY_APPROVE', true),
    ),

    http.get(`${ANY}/api/v1/service-requests/:requestId/versions`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'service_request.read', false);
      if ('error' in g) return g.error;
      const found = find(g.session, g.tenantId, pathParam(params, 'requestId'));
      if (!found) return requestNotFound(api);
      const list: Schemas['ServiceRequestVersionList'] = {
        // Newest first, and complete: the list is what makes a correction auditable.
        items: world()
          .serviceRequestVersions.filter((v) => v.serviceRequestId === found.id)
          .sort((a, b) => b.versionNo - a.versionNo)
          .map(toServiceRequestVersionSummary),
      };
      return HttpResponse.json(list);
    }),

    http.get(
      `${ANY}/api/v1/service-requests/:requestId/versions/:versionNo`,
      async ({ request, params }) => {
        await wait(api);
        const g = guardTenant(api, request, 'service_request.read', false);
        if ('error' in g) return g.error;
        const number = Number(pathParam(params, 'versionNo'));
        if (!Number.isInteger(number) || number < 1) return versionNotFound(api);
        const found = find(g.session, g.tenantId, pathParam(params, 'requestId'));
        if (!found) return requestNotFound(api);
        const version = world().serviceRequestVersions.find(
          (v) => v.serviceRequestId === found.id && v.versionNo === number,
        );
        if (!version) return versionNotFound(api);
        return HttpResponse.json(toServiceRequestVersion(version));
      },
    ),
  ];
}
