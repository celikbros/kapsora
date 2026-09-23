/**
 * MSW handlers for the claim: the version model, the submit pipeline, the two review stages,
 * the two projections and invoice readiness.
 *
 * The mock is a test double of the Go server and M5 treats a divergence in either direction as
 * a bug. Seven things here are transcriptions rather than re-implementations, because they are
 * the seven a screen would be built wrongly against:
 *
 *   - **the freeze**: a claim whose current version has been submitted answers 409
 *     CLAIM_VERSION_FROZEN to `patchClaimDraft` and `putClaimLines`, and a correction is
 *     `returnClaim` followed by a new draft version;
 *   - **the pipeline**: price, then rules, then the cross-checks that are not rules, then the
 *     route — in that order, because a rule compares against a contract amount and a
 *     cross-check should not draw on a hold for a line nobody will pay for;
 *   - **the stage**: a decision is written at the stage the *claim* is waiting in, never the
 *     one the caller happens to hold, so medical review precedes financial rather than merely
 *     usually happening first;
 *   - **append-only decisions**: a line decided twice has two rows and the latest is the
 *     decision; nothing below ever edits one;
 *   - **the split**: `payerAmount + memberAmount` must equal `approvedAmount` exactly, and a
 *     decision that does not is 422 rather than a row nobody can reconcile;
 *   - **the projections**: the financial half drops the line description, the diagnosis
 *     reference, the medical report reference, the medical reviewer's comment, a MEDICAL
 *     decision's reason text and the three report exceptions;
 *   - **readiness**: named blockers, and totals summed here once from the line decisions.
 *
 * As on the server, the projection is applied to the stored row before it becomes a body, in
 * `projectLine` and `projectClaim`, so no handler below can leak a field by forgetting one. A
 * claim is exactly as sensitive as the case it hangs off, and it asks `decideProjection` — the
 * same function the case, the report and the stay handlers ask.
 *
 * Every figure is an exact decimal computed in integer micro-units and rendered in the
 * canonical trimmed form the server's `trim_scale` produces. No amount here passes through a
 * binary float, and nothing is added up by a caller.
 */
import { HttpResponse, http, type HttpHandler } from 'msw';

import { claimSourceHandlers } from './claim-source-handlers';
import { resolveContractPrice } from './contract-handlers';
import {
  fromMicros,
  multiplyMicros,
  percentOfMicros,
  toMicros,
  type MockWorld,
  type StoredClaim,
  type StoredClaimAdjustment,
  type StoredClaimLine,
  type StoredClaimLineDecision,
  type StoredClaimVersion,
  type StoredHealthCase,
  type StoredPriceItem,
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
  parseIfMatch,
  parseLimit,
  pathParam,
  problem,
  readJson,
  validationFailed,
  wait,
  withinPeriod,
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
import { runRules } from './rules-handlers';

const PERMISSION_READ = 'claim.read';
const PERMISSION_CREATE = 'claim.create';
const PERMISSION_SUBMIT = 'claim.submit';
const PERMISSION_CANCEL = 'claim.cancel';
const PERMISSION_MEDICAL_REVIEW = 'claim.medical.review';
const PERMISSION_FINANCIAL_REVIEW = 'claim.financial.review';

const ZERO = 0n;

/** claim.claim.status, as migration 000034 writes it. */
const CLAIM_STATUSES = new Set<string>([
  'DRAFT',
  'SUBMITTED',
  'AUTO_ADJUDICATED',
  'PENDING_MEDICAL',
  'PENDING_FINANCIAL',
  'RETURNED',
  'PARTIALLY_APPROVED',
  'APPROVED',
  'REJECTED',
  'INVOICED',
  'BATCHED',
  'SETTLED',
  'CANCELLED',
]);

const DECISION_KINDS = new Set<string>(['APPROVED', 'PARTIALLY_APPROVED', 'REJECTED', 'CUT']);

/**
 * The adjustment types a caller may name. REVERSAL is absent on purpose: a reversal is raised
 * by naming what it reverses, not by asking for one.
 */
const CALLER_ADJUSTMENT_TYPES = new Set<string>(['CUT', 'RECOVERY', 'CORRECTION']);

/**
 * The closed list of adjustment reasons, transcribed from the Go domain. It is closed because
 * an adjustment is what a provider disputes, and a dispute is answerable only if the reason is
 * a code somebody can count rather than a sentence one reviewer typed.
 */
const ADJUSTMENT_REASONS = new Set<string>([
  'TARIFF_EXCEEDED',
  'CONTRACT_TERMS',
  'NOT_COVERED',
  'DUPLICATE_SERVICE',
  'DOCUMENT_MISSING',
  'OVERPAYMENT',
  'DUPLICATE_PAYMENT',
  'MEMBER_LIABILITY',
  'ARITHMETIC_ERROR',
  'PRICE_CORRECTION',
  'CURRENCY_CORRECTION',
  'REVIEW_REVERSED',
  'ENTERED_IN_ERROR',
]);

/** The statuses a claim counts towards a provider's earnings in. */
const EARNING_STATUSES = new Set<string>([
  'APPROVED',
  'PARTIALLY_APPROVED',
  'INVOICED',
  'BATCHED',
  'SETTLED',
]);
const CHANNELS = new Set<string>([
  'BACKOFFICE',
  'PROVIDER_PORTAL',
  'MEMBER_PORTAL',
  'API',
  'BATCH_IMPORT',
  'CALL_CENTER',
]);

/** The statuses each command may run from — the transition table of the Go domain. */
const FROM: Record<string, string[]> = {
  EDIT: ['DRAFT', 'RETURNED'],
  SUBMIT: ['DRAFT', 'RETURNED'],
  DECIDE: ['PENDING_MEDICAL', 'PENDING_FINANCIAL'],
  APPROVE: ['AUTO_ADJUDICATED', 'PENDING_MEDICAL', 'PENDING_FINANCIAL'],
  REJECT: ['AUTO_ADJUDICATED', 'PENDING_MEDICAL', 'PENDING_FINANCIAL'],
  RETURN: ['AUTO_ADJUDICATED', 'PENDING_MEDICAL', 'PENDING_FINANCIAL'],
  CANCEL: ['DRAFT', 'RETURNED', 'SUBMITTED', 'PENDING_MEDICAL', 'PENDING_FINANCIAL'],
};

/** The reason codes the AUTO stage decides and routes under; the Go constants, spelled once. */
const REASON = {
  autoApproved: 'AUTO_APPROVED',
  priceReview: 'PRICE_REVIEW_REQUIRED',
  ruleRejected: 'RULE_REJECTED',
  ruleCut: 'RULE_CUT',
  ruleMedical: 'RULE_MEDICAL_REVIEW',
  ruleFinancial: 'RULE_FINANCIAL_REVIEW',
  authorizationExceeded: 'AUTHORIZATION_EXCEEDED',
  stayOverAuthorization: 'STAY_OVER_AUTHORIZATION',
  duplicate: 'DUPLICATE_SUSPECTED',
} as const;

/** The three exception codes the financial projection drops, for the reason medicalReportId is. */
const CLINICAL_EXCEPTIONS = new Set<string>([
  'REPORT_NOT_APPROVED',
  'REPORT_OUT_OF_WINDOW',
  'SERVICE_NOT_IN_REPORT',
]);

const REASON_CODE = /^[A-Z][A-Z0-9_.:-]{1,79}$/;
const UNIT_TYPE = /^[A-Z][A-Z0-9_]{1,31}$/;
const MAX_LINES = 500;
const MAX_DESCRIPTION = 1000;
const MAX_REASON_TEXT = 1000;
const MAX_REVIEW_COMMENT = 2000;

/**
 * Renders micro-units in the canonical trimmed decimal the server's `trim_scale` produces:
 * "500" rather than "500.000000", "56.25" rather than "56.250000". The mock and the server have
 * to agree digit for digit, or a screen comparing two amounts would find them different.
 */
function amount(micros: bigint): string {
  const text = fromMicros(micros);
  if (!text.includes('.')) return text;
  const trimmed = text.replace(/0+$/, '').replace(/\.$/, '');
  return trimmed === '' || trimmed === '-' ? '0' : trimmed;
}

/**
 * What a claim is worth, summed once, for every reader that needs the figure: the readiness
 * endpoint, the provider's earnings view and — from WP-I7-02 — the invoice's allocation
 * ceiling. It is a module-level function rather than a closure precisely so the invoice
 * handlers can call *this* one: a second implementation would be a second answer to "what did
 * the payer approve", and the second answer is the one a provider would be refused against.
 *
 * Lines minus adjustments, in integer micro-units, exactly as `billing.claim_approved_total`
 * computes it on the server.
 */
export function claimTotals(
  world: MockWorld,
  claimId: string,
): { lineTotal: bigint; adjustmentTotal: bigint; approved: bigint; payer: bigint; member: bigint } {
  const claim = world.claims.find((c) => c.id === claimId);
  const empty = {
    lineTotal: 0n,
    adjustmentTotal: 0n,
    approved: 0n,
    payer: 0n,
    member: 0n,
  };
  if (!claim) return empty;
  const version = world.claimVersions.find(
    (v) => v.claimId === claimId && v.versionNo === claim.currentVersionNo,
  );
  if (!version) return empty;
  let lineTotal = 0n;
  let payer = 0n;
  let member = 0n;
  for (const line of world.claimLines.filter((l) => l.versionId === version.id)) {
    const decisions = world.claimLineDecisions.filter((d) => d.lineId === line.id);
    const decision = decisions[decisions.length - 1];
    if (!decision) continue;
    lineTotal += toMicros(decision.approvedAmount);
    payer += toMicros(decision.payerAmount);
    member += toMicros(decision.memberAmount);
  }
  let adjustmentTotal = 0n;
  let adjustedPayer = 0n;
  let adjustedMember = 0n;
  for (const row of world.claimAdjustments.filter((a) => a.claimId === claimId)) {
    adjustmentTotal += toMicros(row.amount);
    adjustedPayer += toMicros(row.payerAmount);
    adjustedMember += toMicros(row.memberAmount);
  }
  return {
    lineTotal,
    adjustmentTotal,
    approved: lineTotal - adjustmentTotal,
    payer: payer - adjustedPayer,
    member: member - adjustedMember,
  };
}

/**
 * The claims sitting on a live invoice of this tenant (WP-I7-02).
 *
 * It is the half a claim's *status* cannot answer: a claim allocated to a **draft** invoice is
 * still APPROVED, and offering it again in the earnings view is exactly how the same money ends
 * up on two documents.
 */
export function claimsOnLiveInvoice(world: MockWorld, tenantId: string): Set<string> {
  const out = new Set<string>();
  for (const link of world.invoiceAllocations) {
    if (link.tenantId === tenantId && link.active) out.add(link.claimId);
  }
  return out;
}

/** Rounds micro-units to the currency's minor unit, half away from zero. Two decimals for TRY. */
function roundToMinor(micros: bigint): bigint {
  const factor = 10_000n; // 10^(6-2)
  const negative = micros < ZERO;
  const abs = negative ? -micros : micros;
  const rounded = ((abs + factor / 2n) / factor) * factor;
  return negative ? -rounded : rounded;
}

function claimNotFound(api: MockApi): Response {
  return problem(api, 404, 'CLAIM_NOT_FOUND', 'Hasar dosyası bulunamadı');
}

function versionFrozen(api: MockApi): Response {
  return problem(api, 409, 'CLAIM_VERSION_FROZEN', 'Gönderilmiş sürüm değiştirilemez', {
    detail: 'Düzeltme için dosyayı iade edin; yeni sürüm taslak olarak açılır.',
  });
}

function transitionInvalid(api: MockApi): Response {
  return problem(
    api,
    409,
    'CLAIM_TRANSITION_INVALID',
    'Hasar dosyası bu durumda bu işleme uygun değil',
  );
}

/** The contract amount of a line, in micro-units, or null when the method needs a person. */
function contractAmountMicros(price: StoredPriceItem, quantity: bigint, requested: bigint) {
  let value: bigint;
  switch (price.pricingMethod) {
    case 'FIXED':
      value = toMicros(price.amount);
      break;
    case 'UNIT':
      value = multiplyMicros(toMicros(price.amount), quantity);
      break;
    case 'PERCENT_OF_LIST':
      value = percentOfMicros(requested, toMicros(price.percent));
      break;
    default:
      return null;
  }
  const min = price.minAmount === null ? null : toMicros(price.minAmount);
  const max = price.maxAmount === null ? null : toMicros(price.maxAmount);
  if (min !== null && value < min) value = min;
  if (max !== null && value > max) value = max;
  return value;
}

function memberShareMicros(price: StoredPriceItem, contract: bigint): bigint {
  switch (price.memberShareMethod) {
    case 'FIXED': {
      const fixed = toMicros(price.memberShareAmount);
      return fixed > contract ? contract : fixed;
    }
    case 'PERCENT':
      return percentOfMicros(contract, toMicros(price.memberSharePercent));
    default:
      return ZERO;
  }
}

/** One line's way through the pipeline: what it was priced at, what decided it, what it needs. */
interface LineOutcome {
  line: StoredClaimLine;
  contract: string | null;
  payer: bigint;
  member: bigint;
  priceReview: boolean;
  decision: Schemas['ClaimDecisionKind'] | null;
  decisionAmounts: { approved: bigint; payer: bigint; member: bigint };
  reasonCode: string;
  needsMedical: boolean;
  needsFinancial: boolean;
  exceptions: Schemas['ClaimException'][];
}

export function claimHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;
  // Actual draws only; an over-limit attempt must never restore entitlement.
  const draws = new WeakMap<
    MockWorld,
    Map<string, { item: { consumedQuantity: string }; quantity: bigint }>
  >();
  const drawsOf = () => {
    let entries = draws.get(world());
    if (!entries) {
      entries = new Map();
      draws.set(world(), entries);
    }
    return entries;
  };

  const caseOf = (row: StoredClaim): StoredHealthCase | undefined =>
    row.caseId === null ? undefined : world().healthCases.find((c) => c.id === row.caseId);

  /** A claim has no sensitivity of its own: it is as sensitive as its episode of care. */
  const decide = (session: MockSession, tenantId: string, row: StoredClaim, req: AccessRequest) =>
    decideProjection(api, session, tenantId, caseOf(row)?.sensitivity ?? 'STANDARD', req);

  const versionsOf = (claimId: string): StoredClaimVersion[] =>
    world()
      .claimVersions.filter((v) => v.claimId === claimId)
      .sort((a, b) => b.versionNo - a.versionNo);

  const versionOf = (claimId: string, versionNo: number): StoredClaimVersion | undefined =>
    world().claimVersions.find((v) => v.claimId === claimId && v.versionNo === versionNo);

  const draftVersionOf = (claimId: string): StoredClaimVersion | undefined =>
    world().claimVersions.find((v) => v.claimId === claimId && v.status === 'DRAFT');

  const linesOf = (versionId: string): StoredClaimLine[] =>
    world()
      .claimLines.filter((l) => l.versionId === versionId)
      .sort((a, b) => a.lineNo - b.lineNo);

  /** The head of a line's append-only decision history, which is the decision. */
  const decisionOf = (lineId: string): StoredClaimLineDecision | undefined => {
    const rows = world().claimLineDecisions.filter((d) => d.lineId === lineId);
    return rows.length === 0 ? undefined : rows[rows.length - 1];
  };

  const serviceCodeOf = (id: string): string =>
    world().serviceDefinitions.find((d) => d.id === id)?.code ?? '';

  /**
   * One line decision as the caller may see it. Every figure survives both projections — a cut
   * is money and money is the financial reviewer's business — and the reason *text* of a
   * MEDICAL decision does not: "fizik tedavi endikasyonu yok" is a clinical judgement about a
   * person, exactly as protected as the diagnosis it rests on. The reason *code* survives,
   * because a provider disputing a cut has to be told which rule cut it.
   */
  const projectDecision = (
    row: StoredClaimLineDecision,
    clinical: boolean,
  ): Schemas['ClaimLineDecision'] => ({
    id: row.id,
    lineId: row.lineId,
    decidedInVersionNo: row.decidedInVersionNo,
    decision: row.decision,
    approvedQuantity: row.approvedQuantity,
    approvedAmount: row.approvedAmount,
    contractAmount: row.contractAmount,
    payerAmount: row.payerAmount,
    memberAmount: row.memberAmount,
    reasonCode: row.reasonCode,
    ...(clinical || row.stage !== 'MEDICAL' ? { reasonText: row.reasonText } : {}),
    decidedBy: row.decidedBy,
    decidedAt: row.decidedAt,
    stage: row.stage,
  });

  /**
   * The projection, applied to the row before it becomes a body. A clinical field is omitted
   * rather than sent as null, because the server omits it.
   */
  const projectLine = (row: StoredClaimLine, projection: Projection): Schemas['ClaimLine'] => {
    const clinical = projection === 'CLINICAL';
    const decision = decisionOf(row.id);
    return {
      id: row.id,
      lineNo: row.lineNo,
      serviceDefinitionId: row.serviceDefinitionId,
      serviceCode: serviceCodeOf(row.serviceDefinitionId),
      unitType: row.unitType,
      quantity: row.quantity,
      unitAmount: row.unitAmount,
      lineAmount: row.lineAmount,
      currencyCode: row.currencyCode,
      // The three the financial projection drops.
      ...(clinical
        ? {
            diagnosisId: row.diagnosisId,
            medicalReportId: row.medicalReportId,
            description: row.description,
          }
        : {}),
      practitionerId: row.practitionerId,
      decision: decision === undefined ? null : projectDecision(decision, clinical),
      createdAt: row.createdAt,
      rowVersion: row.rowVersion,
    };
  };

  const projectExceptions = (
    rows: Schemas['ClaimException'][],
    projection: Projection,
  ): Schemas['ClaimException'][] =>
    projection === 'CLINICAL' ? rows : rows.filter((e) => !CLINICAL_EXCEPTIONS.has(e.code));

  const projectClaim = (row: StoredClaim, projection: Projection): Schemas['Claim'] => {
    const clinical = projection === 'CLINICAL';
    const version = versionOf(row.id, row.currentVersionNo);
    return {
      id: row.id,
      reference: row.reference,
      personId: row.personId,
      programId: row.programId,
      enrollmentId: row.enrollmentId,
      providerOrganizationId: row.providerOrganizationId,
      domainCode: row.domainCode,
      sourceType: row.sourceType,
      sourceId: row.sourceId,
      caseId: row.caseId,
      fulfilmentId: row.fulfilmentId,
      authorizationId: row.authorizationId,
      currentVersionNo: row.currentVersionNo,
      status: row.status,
      serviceDateFrom: row.serviceDateFrom,
      serviceDateTo: row.serviceDateTo,
      channel: row.channel,
      projection,
      rejectReasonCode: row.rejectReasonCode,
      returnReasonCode: row.returnReasonCode,
      // The medical reviewer's comment is a doctor's sentence about a patient.
      ...(clinical ? { reviewCommentMedical: row.reviewCommentMedical } : {}),
      reviewCommentFinancial: row.reviewCommentFinancial,
      closedAt: row.closedAt,
      lines: version ? linesOf(version.id).map((l) => projectLine(l, projection)) : [],
      exceptions: projectExceptions(version?.exceptions ?? [], projection),
      createdAt: row.createdAt,
      rowVersion: row.rowVersion,
    };
  };

  const versionSummary = (v: StoredClaimVersion): Schemas['ClaimVersionSummary'] => ({
    id: v.id,
    versionNo: v.versionNo,
    status: v.status,
    submittedAt: v.submittedAt,
    submittedBy: v.submittedBy,
    returnedAt: v.returnedAt,
    returnedBy: v.returnedBy,
    returnReasonCode: v.returnReasonCode,
    returnReasonText: v.returnReasonText,
    createdAt: v.createdAt,
    rowVersion: v.rowVersion,
  });

  /** The access event a clinical read of a claim owes, with resource type CLAIM. */
  const recordAccess = (
    session: MockSession,
    tenantId: string,
    row: StoredClaim,
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
      resourceType: 'CLAIM',
      resourceId: row.id,
      accessType,
      purposeCode: req.purposeCode === '' ? null : req.purposeCode,
      reasonText: req.reasonText === '' ? null : req.reasonText,
      outcome,
    });
  };

  const visible = (session: MockSession, tenantId: string, row: StoredClaim): boolean => {
    const scope = organizationScope(api, session, tenantId);
    return scope === null ? true : withinScope(scope, row.providerOrganizationId);
  };

  const findClaim = (
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredClaim | undefined => {
    const row = world().claims.find((c) => c.id === id && c.tenantId === tenantId);
    if (!row) return undefined;
    return visible(session, tenantId, row) ? row : undefined;
  };

  /** The body a command answers with: read back, projected, recording nothing. */
  const answer = (session: MockSession, tenantId: string, row: StoredClaim, status = 200) =>
    HttpResponse.json(
      projectClaim(
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

  /** The read every command makes before it writes: the claim, the boundary and the If-Match. */
  const forCommand = (
    request: Request,
    session: MockSession,
    tenantId: string,
    id: string,
    command: keyof typeof FROM,
  ): { row: StoredClaim } | { error: Response } => {
    const row = findClaim(session, tenantId, id);
    if (!row) return { error: claimNotFound(api) };
    const expected = parseIfMatch(request.headers.get('If-Match'));
    if (expected === null || expected < 1) {
      return {
        error: problem(api, 428, 'IF_MATCH_REQUIRED', 'If-Match başlığı gerekli', {
          detail: 'GET yanıtındaki ETag değerini If-Match olarak gönderin.',
        }),
      };
    }
    if (!FROM[command]!.includes(row.status)) {
      // The freeze speaks for itself on the two write commands; everything else is a
      // lifecycle refusal.
      return { error: command === 'EDIT' ? versionFrozen(api) : transitionInvalid(api) };
    }
    if (row.rowVersion !== expected) {
      return {
        error: problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti', {
          detail: 'Güncel sürümü alıp değişikliğinizi yeniden uygulayın.',
        }),
      };
    }
    return { row };
  };

  /** Validates a line set exactly as the Go domain does, one message per problem. */
  const lineErrors = (rows: Schemas['NewClaimLine'][] | undefined): FieldError[] => {
    const errors: FieldError[] = [];
    if (!rows || rows.length === 0) {
      errors.push({ field: 'lines', code: 'REQUIRED', message: 'en az bir kalem gerekli' });
      return errors;
    }
    if (rows.length > MAX_LINES) {
      errors.push({ field: 'lines', code: 'LENGTH', message: `en fazla ${MAX_LINES} kalem` });
      return errors;
    }
    const seen = new Set<number>();
    let currency: string | null = null;
    rows.forEach((row, i) => {
      const path = `lines[${i}]`;
      if (!(row.lineNo >= 1)) {
        errors.push({ field: `${path}.lineNo`, code: 'RANGE' });
      } else if (seen.has(row.lineNo)) {
        errors.push({ field: `${path}.lineNo`, code: 'DUPLICATE' });
      } else {
        seen.add(row.lineNo);
      }
      if (!UNIT_TYPE.test(row.unitType)) errors.push({ field: `${path}.unitType`, code: 'FORMAT' });
      if (toMicros(row.quantity) <= ZERO) errors.push({ field: `${path}.quantity`, code: 'RANGE' });
      if (toMicros(row.lineAmount) < ZERO) {
        errors.push({ field: `${path}.lineAmount`, code: 'RANGE' });
      }
      if (!world().serviceDefinitions.some((d) => d.id === row.serviceDefinitionId)) {
        errors.push({ field: `${path}.serviceDefinitionId`, code: 'NOT_FOUND' });
      }
      if (row.currencyCode) {
        if (currency === null) currency = row.currencyCode;
        else if (currency !== row.currencyCode) {
          errors.push({ field: `${path}.currencyCode`, code: 'CONFLICT' });
        }
      }
      if ((row.description ?? '').length > MAX_DESCRIPTION) {
        errors.push({ field: `${path}.description`, code: 'LENGTH' });
      }
    });
    return errors;
  };

  /** Replaces a version's whole line set. The set is the unit, exactly as on the server. */
  const replaceLines = (
    tenantId: string,
    version: StoredClaimVersion,
    rows: Schemas['NewClaimLine'][],
  ): void => {
    api.world.claimLines = world().claimLines.filter((l) => l.versionId !== version.id);
    for (const row of rows) {
      world().claimLines.push({
        id: world().nextId(),
        tenantId,
        versionId: version.id,
        lineNo: row.lineNo,
        serviceDefinitionId: row.serviceDefinitionId,
        unitType: row.unitType,
        quantity: row.quantity,
        unitAmount: row.unitAmount ?? null,
        lineAmount: row.lineAmount,
        currencyCode: row.currencyCode ?? 'TRY',
        diagnosisId: row.diagnosisId ?? null,
        medicalReportId: row.medicalReportId ?? null,
        practitionerId: row.practitionerId ?? null,
        description: row.description ?? null,
        createdAt: new Date().toISOString(),
        rowVersion: 1,
      });
    }
  };

  const writeDecision = (
    tenantId: string,
    line: StoredClaimLine,
    versionNo: number,
    decision: Schemas['ClaimDecisionKind'],
    approved: string,
    payer: string,
    member: string,
    reasonCode: string,
    stage: Schemas['ClaimDecisionStage'],
    options: {
      approvedQuantity?: string;
      contractAmount?: string | null;
      reasonText?: string | null;
      decidedBy?: string | null;
    } = {},
  ): void => {
    // Append-only: nothing here ever edits a row, so a line decided twice has two.
    world().claimLineDecisions.push({
      id: world().nextId(),
      tenantId,
      lineId: line.id,
      decidedInVersionNo: versionNo,
      decision,
      approvedQuantity: options.approvedQuantity ?? line.quantity,
      approvedAmount: approved,
      contractAmount: options.contractAmount ?? null,
      payerAmount: payer,
      memberAmount: member,
      reasonCode,
      reasonText: options.reasonText ?? null,
      decidedBy: stage === 'AUTO' ? null : (options.decidedBy ?? null),
      decidedAt: new Date().toISOString(),
      stage,
    });
  };

  /**
   * Steps 1 to 3 of section 2.2: price, then rules, then the cross-checks that are not rules.
   * It uses the mock's own pricing resolution and its own rule runner, so the mock and the
   * server disagree only if one of them is wrong.
   */
  const runPipeline = (
    tenantId: string,
    claim: StoredClaim,
    lines: StoredClaimLine[],
  ): LineOutcome[] => {
    const providerProfile = world().providers.find(
      (p) => p.tenantOrganizationId === claim.providerOrganizationId,
    );
    const outcomes: LineOutcome[] = lines.map((line) => ({
      line,
      contract: null,
      payer: ZERO,
      member: ZERO,
      priceReview: false,
      decision: null,
      decisionAmounts: { approved: ZERO, payer: ZERO, member: ZERO },
      reasonCode: '',
      needsMedical: false,
      needsFinancial: false,
      exceptions: [],
    }));

    // ---- 1. Price -------------------------------------------------------
    for (const outcome of outcomes) {
      const resolution = providerProfile
        ? resolveContractPrice(world(), tenantId, {
            serviceDate: claim.serviceDateFrom,
            providerProfileId: providerProfile.id,
            serviceDefinitionId: outcome.line.serviceDefinitionId,
            packageDefinitionId: null,
            locationId: null,
          })
        : null;
      const price = resolution?.winner ?? null;
      const contract =
        price === null
          ? null
          : contractAmountMicros(
              price,
              toMicros(outcome.line.quantity),
              toMicros(outcome.line.lineAmount),
            );
      if (price === null || contract === null) {
        // A line the ladder cannot price is never guessed at.
        outcome.priceReview = true;
        outcome.needsFinancial = true;
        outcome.exceptions.push({
          lineNo: outcome.line.lineNo,
          code: REASON.priceReview,
          stage: 'FINANCIAL',
          detail:
            resolution?.result.outcome === 'REVIEW_REQUIRED'
              ? 'PRICE_AMBIGUOUS'
              : 'PRICE_NOT_FOUND',
        });
        continue;
      }
      const share = memberShareMicros(price, contract);
      const covered = contract - share;
      // One rounding, at the end, and the member takes the difference — so payer + member is
      // exactly the contract amount, which independent rounding would break by a kuruş.
      const roundedContract = roundToMinor(contract);
      const roundedPayer =
        roundToMinor(covered) > roundedContract ? roundedContract : roundToMinor(covered);
      outcome.contract = amount(roundedContract);
      outcome.payer = roundedPayer;
      outcome.member = roundedContract - roundedPayer;
    }

    // ---- 2. Rules -------------------------------------------------------
    for (const outcome of outcomes) {
      for (const version of world().ruleSetVersions) {
        if (version.tenantId !== tenantId || version.status !== 'PUBLISHED') continue;
        if (version.validFrom === null) continue;
        if (!withinPeriod(claim.serviceDateFrom, version.validFrom, version.validTo)) continue;
        const set = world().ruleSets.find((s) => s.id === version.ruleSetId);
        if (set?.purpose !== 'ADJUDICATION' || set.status !== 'ACTIVE') continue;
        const run = runRules(version, {
          claimReference: claim.reference,
          serviceDate: claim.serviceDateFrom,
          channel: claim.channel,
          domainCode: claim.domainCode,
          personId: claim.personId,
          programId: claim.programId,
          enrollmentId: claim.enrollmentId,
          providerOrganizationId: claim.providerOrganizationId,
          lineNo: outcome.line.lineNo,
          serviceDefinitionId: outcome.line.serviceDefinitionId,
          serviceCode: serviceCodeOf(outcome.line.serviceDefinitionId),
          unitType: outcome.line.unitType,
          quantity: outcome.line.quantity,
          lineAmount: outcome.line.lineAmount,
          currencyCode: outcome.line.currencyCode,
          contractAmount: outcome.contract ?? '',
          payerAmount: amount(outcome.payer),
          memberAmount: amount(outcome.member),
        });
        for (const action of run.actions) {
          applyAction(outcome, action);
        }
      }
    }

    // ---- 3. Cross-checks that are not rules -----------------------------
    // The stay's reconciliation is a fact about the case rather than about any one line.
    if (claim.caseId !== null && outcomes.length > 0) {
      const over = world().inpatientStays.some(
        (s) => s.tenantId === tenantId && s.caseId === claim.caseId && s.overAuthorization,
      );
      if (over) {
        outcomes[0]!.needsMedical = true;
        outcomes[0]!.exceptions.push({
          lineNo: null,
          code: REASON.stayOverAuthorization,
          stage: 'MEDICAL',
          detail: null,
        });
      }
    }
    for (const outcome of outcomes) {
      // A line the rules already decided consumes nothing and is checked against nothing.
      if (outcome.decision !== null) continue;
      checkAuthorization(tenantId, claim, outcome);
      checkDuplicate(tenantId, claim, outcome);
    }
    return outcomes;
  };

  const applyAction = (outcome: LineOutcome, action: Schemas['RuleAction']): void => {
    switch (action.type) {
      case 'REQUIRE_MEDICAL_REVIEW':
        outcome.needsMedical = true;
        outcome.exceptions.push({
          lineNo: outcome.line.lineNo,
          code: REASON.ruleMedical,
          stage: 'MEDICAL',
          detail: null,
        });
        break;
      case 'REQUIRE_FINANCIAL_REVIEW':
        outcome.needsFinancial = true;
        outcome.exceptions.push({
          lineNo: outcome.line.lineNo,
          code: REASON.ruleFinancial,
          stage: 'FINANCIAL',
          detail: null,
        });
        break;
      case 'REJECT':
        outcome.decision = 'REJECTED';
        outcome.reasonCode =
          typeof action.payload?.reasonCode === 'string'
            ? action.payload.reasonCode
            : REASON.ruleRejected;
        outcome.decisionAmounts = { approved: ZERO, payer: ZERO, member: ZERO };
        break;
      case 'PARTIAL_APPROVE': {
        // A cut is the payer refusing part of a bill; the member does not pick up what the
        // payer put down, so the approved amount comes down with the payer's share.
        let payer = outcome.payer;
        if (typeof action.payload?.percent === 'string') {
          payer = percentOfMicros(payer, toMicros(action.payload.percent));
        } else if (typeof action.payload?.amount === 'string') {
          const cap = toMicros(action.payload.amount);
          if (cap < payer) payer = cap;
        } else {
          break;
        }
        if (payer < ZERO) payer = ZERO;
        outcome.decision = 'CUT';
        outcome.reasonCode = REASON.ruleCut;
        outcome.decisionAmounts = {
          approved: payer + outcome.member,
          payer,
          member: outcome.member,
        };
        break;
      }
      default:
        // APPROVE and WARN decide nothing: the pipeline's own answer to "nothing objected" is
        // the auto-approval below.
        break;
    }
  };

  /**
   * An over-consumption is an exception and **nothing moves** — not even the part that was
   * left. A hold for four sessions billed for six is either a mistake or a fraud, and
   * consuming the four quietly would hide both.
   */
  const checkAuthorization = (tenantId: string, claim: StoredClaim, outcome: LineOutcome): void => {
    if (claim.authorizationId === null) return;
    const hold = world().claimAuthorizations.find(
      (a) => a.id === claim.authorizationId && a.tenantId === tenantId,
    );
    const item = hold?.items.find(
      (i) => i.serviceDefinitionId === outcome.line.serviceDefinitionId,
    );
    if (!item) return;
    const remaining = toMicros(item.approvedQuantity) - toMicros(item.consumedQuantity);
    const wanted = toMicros(outcome.line.quantity);
    if (wanted > remaining) {
      outcome.needsMedical = true;
      outcome.exceptions.push({
        lineNo: outcome.line.lineNo,
        code: REASON.authorizationExceeded,
        stage: 'MEDICAL',
        detail: amount(remaining < ZERO ? ZERO : remaining),
      });
      return;
    }
    item.consumedQuantity = amount(toMicros(item.consumedQuantity) + wanted);
    drawsOf().set(outcome.line.id, { item, quantity: wanted });
  };

  /** Another live claim of the same person, service and day, named by its reference. */
  const checkDuplicate = (tenantId: string, claim: StoredClaim, outcome: LineOutcome): void => {
    const other = world()
      .claims.filter(
        (c) =>
          c.tenantId === tenantId &&
          c.id !== claim.id &&
          c.personId === claim.personId &&
          c.status !== 'REJECTED' &&
          c.status !== 'CANCELLED' &&
          c.serviceDateFrom <= claim.serviceDateTo &&
          c.serviceDateTo >= claim.serviceDateFrom,
      )
      .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id))
      .find((c) => {
        const version = versionOf(c.id, c.currentVersionNo);
        return (
          version !== undefined &&
          linesOf(version.id).some(
            (l) => l.serviceDefinitionId === outcome.line.serviceDefinitionId,
          )
        );
      });
    if (!other) return;
    outcome.needsFinancial = true;
    outcome.exceptions.push({
      lineNo: outcome.line.lineNo,
      code: REASON.duplicate,
      stage: 'FINANCIAL',
      detail: other.reference,
    });
  };

  /** What the decisions say the claim is. */
  const statusFor = (decisions: StoredClaimLineDecision[]): Schemas['ClaimStatus'] => {
    const rejected = decisions.filter((d) => d.decision === 'REJECTED').length;
    const approved = decisions.length - rejected;
    if (approved === 0) return 'REJECTED';
    return rejected > 0 ? 'PARTIALLY_APPROVED' : 'APPROVED';
  };

  const latestDecisions = (versionId: string): StoredClaimLineDecision[] => {
    const out: StoredClaimLineDecision[] = [];
    for (const line of linesOf(versionId)) {
      const decision = decisionOf(line.id);
      if (decision) out.push(decision);
    }
    return out;
  };

  const adjustmentsOf = (claimId: string): StoredClaimAdjustment[] =>
    world().claimAdjustments.filter((a) => a.claimId === claimId);

  /** One ledger row as the contract renders it. */
  const adjustmentView = (row: StoredClaimAdjustment): Schemas['ClaimAdjustment'] => ({
    id: row.id,
    claimId: row.claimId,
    versionNo: row.versionNo,
    claimLineId: row.claimLineId,
    adjustmentType: row.adjustmentType,
    amount: amount(toMicros(row.amount)),
    payerAmount: amount(toMicros(row.payerAmount)),
    memberAmount: amount(toMicros(row.memberAmount)),
    currencyCode: row.currencyCode,
    reasonCode: row.reasonCode,
    reasonText: row.reasonText,
    sourceType: row.sourceType,
    sourceId: row.sourceId,
    reversesAdjustmentId: row.reversesAdjustmentId,
    createdBy: row.createdBy,
    createdAt: row.createdAt,
  });

  /**
   * What an invoice would need, answered the way the server answers it: the line decisions
   * summed once, the adjustment ledger summed once, and the approved total the difference.
   */
  const readinessOf = (row: StoredClaim): Schemas['ClaimInvoiceReadiness'] => {
    const version = versionOf(row.id, row.currentVersionNo);
    const lines = version ? linesOf(version.id) : [];
    const decisions = version ? latestDecisions(version.id) : [];
    // One implementation of "what did the payer approve", shared with the earnings view and
    // with WP-I7-02's allocation ceiling.
    const totals = claimTotals(world(), row.id);

    const blockers: Schemas['ClaimInvoiceBlocker'][] = [];
    const relationship = world().relationships.find((r) => r.id === row.providerOrganizationId);
    const organization = relationship
      ? world().organizations.get(relationship.organizationId)
      : undefined;
    if (!organization?.taxNumber) blockers.push('PROVIDER_TAX_IDENTITY_MISSING');
    if (new Set(lines.map((l) => l.currencyCode)).size > 1) blockers.push('CURRENCY_NOT_SINGLE');
    if (decisions.length !== lines.length) blockers.push('LINE_NOT_DECIDED');

    return {
      claimId: row.id,
      status: row.status,
      currencyCode: lines[0]?.currencyCode ?? 'TRY',
      lineTotal: amount(totals.lineTotal),
      adjustmentTotal: amount(totals.adjustmentTotal),
      approvedTotal: amount(totals.approved),
      payerTotal: amount(totals.payer),
      memberTotal: amount(totals.member),
      lineCount: lines.length,
      adjustmentCount: adjustmentsOf(row.id).length,
      decidedLineCount: decisions.length,
      ready: blockers.length === 0,
      blockers,
    };
  };

  const touch = (row: StoredClaim): void => {
    row.rowVersion += 1;
  };

  const createDraft = (session: MockSession, tenantId: string, body: Schemas['CreateClaim']) => {
    const now = new Date().toISOString();
    const claim: StoredClaim = {
      id: world().nextId(),
      tenantId: tenantId,
      reference: `CLM-${body.serviceDateFrom.replace(/-/g, '')}-${world()
        .nextId()
        .slice(-8)
        .toUpperCase()}`,
      personId: body.personId,
      programId: body.programId,
      enrollmentId: body.enrollmentId,
      providerOrganizationId: body.providerOrganizationId,
      // A claim comes from one thing. `bookingId` makes it a lodging claim and `caseId`
      // makes it a health one; the two together are refused above.
      domainCode: body.bookingId ? 'ACCOMMODATION' : 'HEALTH',
      sourceType: body.bookingId ? 'BOOKING' : body.caseId ? 'HEALTH_CASE' : null,
      sourceId: body.bookingId ?? body.caseId ?? null,
      caseId: body.caseId ?? null,
      fulfilmentId: body.fulfilmentId ?? null,
      authorizationId: body.authorizationId ?? null,
      currentVersionNo: 1,
      status: 'DRAFT',
      serviceDateFrom: body.serviceDateFrom,
      serviceDateTo: body.serviceDateTo,
      channel: body.channel ?? 'PROVIDER_PORTAL',
      rejectReasonCode: null,
      returnReasonCode: null,
      reviewCommentMedical: null,
      reviewCommentFinancial: null,
      closedAt: null,
      createdAt: now,
      rowVersion: 1,
    };
    world().claims.push(claim);
    const version: StoredClaimVersion = {
      id: world().nextId(),
      tenantId: tenantId,
      claimId: claim.id,
      versionNo: 1,
      status: 'DRAFT',
      submittedAt: null,
      submittedBy: null,
      returnedAt: null,
      returnedBy: null,
      returnReasonCode: null,
      returnReasonText: null,
      financialRequired: false,
      exceptions: [],
      createdAt: now,
      rowVersion: 1,
    };
    world().claimVersions.push(version);
    replaceLines(tenantId, version, body.lines);
    return answer(session, tenantId, claim, 201);
  };
  return [
    ...claimSourceHandlers(api, createDraft),
    http.get(`${ANY}/api/v1/claims`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
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
      if (status !== null && !CLAIM_STATUSES.has(status)) {
        return validationFailed(api, [
          { field: 'status', code: 'ENUM', message: 'geçerli bir durum olmalı' },
        ]);
      }
      const personId = url.searchParams.get('personId');
      const caseId = url.searchParams.get('caseId');
      const providerOrganizationId = url.searchParams.get('providerOrganizationId');
      const from = url.searchParams.get('serviceDateFrom');
      const to = url.searchParams.get('serviceDateTo');

      const rows = world()
        .claims.filter(
          (c) =>
            c.tenantId === g.tenantId &&
            visible(g.session, g.tenantId, c) &&
            (personId === null || c.personId === personId) &&
            (caseId === null || c.caseId === caseId) &&
            (providerOrganizationId === null ||
              c.providerOrganizationId === providerOrganizationId) &&
            (status === null || c.status === status) &&
            (from === null || c.serviceDateTo >= from) &&
            (to === null || c.serviceDateFrom <= to),
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
        return projectClaim(row, d.projection);
      });
      const body: Schemas['ClaimPage'] = {
        items,
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.post(`${ANY}/api/v1/claims`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CREATE, true);
      if ('error' in g) return g.error;
      const body = await readJson<Schemas['CreateClaim']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

      const errors = lineErrors(body.lines);
      if (!body.personId) errors.push({ field: 'personId', code: 'REQUIRED' });
      if (!body.providerOrganizationId) {
        errors.push({ field: 'providerOrganizationId', code: 'REQUIRED' });
      }
      if (!body.serviceDateFrom) errors.push({ field: 'serviceDateFrom', code: 'REQUIRED' });
      if (body.serviceDateTo && body.serviceDateFrom && body.serviceDateTo < body.serviceDateFrom) {
        errors.push({ field: 'serviceDateTo', code: 'RANGE' });
      }
      if (body.channel && !CHANNELS.has(body.channel)) {
        errors.push({ field: 'channel', code: 'ENUM' });
      }
      if (body.bookingId && body.caseId) {
        errors.push({ field: 'bookingId', code: 'CONFLICT' });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      // One live claim per booking, as `uq_claim_live_booking` says it on the server.
      if (body.bookingId) {
        const live = world().claims.find(
          (c) =>
            c.tenantId === g.tenantId &&
            c.sourceType === 'BOOKING' &&
            c.sourceId === body.bookingId &&
            c.status !== 'CANCELLED' &&
            c.status !== 'REJECTED',
        );
        if (live) {
          return problem(
            api,
            409,
            'CLAIM_SOURCE_ALREADY_CLAIMED',
            'Bu rezervasyon için açık bir dosya zaten var',
            {
              detail: 'Bir rezervasyonun aynı anda yalnızca bir açık hasar dosyası olur.',
            },
          );
        }
      }

      const scope = organizationScope(api, g.session, g.tenantId);
      if (scope !== null && !withinScope(scope, body.providerOrganizationId)) {
        return problem(api, 403, 'CLAIM_PROVIDER_SCOPE', 'Bu sağlayıcı adına işlem yapamazsınız');
      }
      const provider = world().relationships.find(
        (r) => r.id === body.providerOrganizationId && r.tenantId === g.tenantId,
      );
      if (!provider || provider.relationshipRole !== 'PROVIDER') {
        return problem(api, 422, 'CLAIM_PROVIDER_UNKNOWN', "Bu kurum tenant'ın sağlayıcısı değil");
      }

      return createDraft(g.session, g.tenantId, body);
    }),

    http.get(`${ANY}/api/v1/claims/:claimId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      const req = accessRequest(request);
      const badAccess = accessHeaderProblem(api, req);
      if (badAccess) return badAccess;

      const row = findClaim(g.session, g.tenantId, pathParam(params, 'claimId'));
      if (!row) return claimNotFound(api);
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
      return HttpResponse.json(projectClaim(row, d.projection), {
        headers: { ...NO_STORE, ETag: etagOf(row.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/claims/:claimId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CREATE, true);
      if ('error' in g) return g.error;
      const found = forCommand(
        request,
        g.session,
        g.tenantId,
        pathParam(params, 'claimId'),
        'EDIT',
      );
      if ('error' in found) return found.error;
      const body = await readJson<Schemas['PatchClaimDraft']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      if (body.serviceDateTo < body.serviceDateFrom) {
        return validationFailed(api, [{ field: 'serviceDateTo', code: 'RANGE' }]);
      }
      const row = found.row;
      row.serviceDateFrom = body.serviceDateFrom;
      row.serviceDateTo = body.serviceDateTo;
      row.channel = body.channel ?? 'PROVIDER_PORTAL';
      row.caseId = body.caseId ?? null;
      row.fulfilmentId = body.fulfilmentId ?? null;
      row.authorizationId = body.authorizationId ?? null;
      touch(row);
      return answer(g.session, g.tenantId, row);
    }),

    http.put(`${ANY}/api/v1/claims/:claimId/lines`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CREATE, true);
      if ('error' in g) return g.error;
      const found = forCommand(
        request,
        g.session,
        g.tenantId,
        pathParam(params, 'claimId'),
        'EDIT',
      );
      if ('error' in found) return found.error;
      const body = await readJson<Schemas['PutClaimLines']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      const errors = lineErrors(body.lines);
      if (errors.length > 0) return validationFailed(api, errors);
      const version = draftVersionOf(found.row.id);
      if (!version) return versionFrozen(api);
      if (!hasPermission(api, g.session, g.tenantId, 'health.clinical.read')) {
        const old = world().claimLines.filter((line) => line.versionId === version.id);
        for (const line of body.lines) {
          const previous = old.find((item) => item.lineNo === line.lineNo);
          if (!previous) continue;
          if (
            (previous.diagnosisId || previous.medicalReportId) &&
            previous.serviceDefinitionId !== line.serviceDefinitionId
          )
            return validationFailed(api, [{ field: 'lines', code: 'CLINICAL_LINK' }]);
          line.diagnosisId = previous.diagnosisId;
          line.medicalReportId = previous.medicalReportId;
          line.description = previous.description;
        }
      }
      replaceLines(g.tenantId, version, body.lines);
      // The lines belong to the version and the ETag belongs to the claim.
      touch(found.row);
      return answer(g.session, g.tenantId, found.row);
    }),

    http.post(`${ANY}/api/v1/claims/:claimId/submit`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_SUBMIT, true);
      if ('error' in g) return g.error;
      const found = forCommand(
        request,
        g.session,
        g.tenantId,
        pathParam(params, 'claimId'),
        'SUBMIT',
      );
      if ('error' in found) return found.error;
      const claim = found.row;
      const version = draftVersionOf(claim.id);
      if (!version) return versionFrozen(api);
      const lines = linesOf(version.id);
      if (lines.length === 0) {
        return problem(api, 422, 'CLAIM_LINE_REQUIRED', 'Gönderim için en az bir kalem gerekli');
      }

      const outcomes = runPipeline(g.tenantId, claim, lines);
      const medicalRequired = outcomes.some((o) => o.decision === null && o.needsMedical);
      const financialRequired = outcomes.some((o) => o.decision === null && o.needsFinancial);

      // The freeze, with what the pipeline decided and found written onto it.
      version.status = 'SUBMITTED';
      version.submittedAt = new Date().toISOString();
      version.submittedBy = g.session.account.actorId;
      version.financialRequired = financialRequired;
      version.contractAmounts = Object.fromEntries(
        outcomes.map((o) => [o.line.lineNo, o.contract]),
      );
      version.exceptions = outcomes.flatMap((o) => (o.decision === null ? o.exceptions : []));

      for (const outcome of outcomes) {
        if (outcome.decision !== null) {
          writeDecision(
            g.tenantId,
            outcome.line,
            version.versionNo,
            outcome.decision,
            amount(outcome.decisionAmounts.approved),
            amount(outcome.decisionAmounts.payer),
            amount(outcome.decisionAmounts.member),
            outcome.reasonCode,
            'AUTO',
            { contractAmount: outcome.contract },
          );
          continue;
        }
        if (outcome.needsMedical || outcome.needsFinancial) continue;
        // Nothing objected: decided here, at the AUTO stage, with the ladder's own figures.
        writeDecision(
          g.tenantId,
          outcome.line,
          version.versionNo,
          'APPROVED',
          amount(outcome.payer + outcome.member),
          amount(outcome.payer),
          amount(outcome.member),
          REASON.autoApproved,
          'AUTO',
          { contractAmount: outcome.contract },
        );
      }

      if (medicalRequired) {
        claim.status = 'PENDING_MEDICAL';
      } else if (financialRequired) {
        claim.status = 'PENDING_FINANCIAL';
      } else {
        claim.status = statusFor(latestDecisions(version.id));
        if (claim.status === 'REJECTED') {
          claim.rejectReasonCode = REASON.ruleRejected;
          claim.closedAt = new Date().toISOString();
        }
      }
      touch(claim);
      return answer(g.session, g.tenantId, claim);
    }),

    http.post(`${ANY}/api/v1/claims/:claimId/line-decisions`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_MEDICAL_REVIEW, true);
      const gg = 'error' in g ? guardTenant(api, request, PERMISSION_FINANCIAL_REVIEW, true) : g;
      if ('error' in gg) return gg.error;
      const found = forCommand(
        request,
        gg.session,
        gg.tenantId,
        pathParam(params, 'claimId'),
        'DECIDE',
      );
      if ('error' in found) return found.error;
      const own = ownFile(api, gg.session, gg.tenantId, found.row.personId);
      if (own) return own;
      const claim = found.row;
      const stage: Schemas['ClaimDecisionStage'] =
        claim.status === 'PENDING_MEDICAL' ? 'MEDICAL' : 'FINANCIAL';
      // The stage is the claim's, not the caller's. A financial reviewer reaching a claim
      // still in medical review is told so rather than allowed to write a clinical decision.
      const holds =
        stage === 'MEDICAL'
          ? hasPermission(api, gg.session, gg.tenantId, PERMISSION_MEDICAL_REVIEW)
          : hasPermission(api, gg.session, gg.tenantId, PERMISSION_FINANCIAL_REVIEW);
      if (!holds) {
        return problem(api, 409, 'CLAIM_STAGE_MISMATCH', 'Dosya bu inceleme aşamasında değil', {
          detail: 'Tıbbi inceleme, mali incelemeden önce tamamlanır.',
        });
      }
      const body = await readJson<Schemas['DecideClaimLines']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');

      const version = versionOf(claim.id, claim.currentVersionNo);
      if (!version) return claimNotFound(api);
      const byLineNo = new Map(linesOf(version.id).map((l) => [l.lineNo, l]));

      const errors: FieldError[] = [];
      if (!body.decisions || body.decisions.length === 0) {
        errors.push({ field: 'decisions', code: 'REQUIRED' });
      }
      (body.decisions ?? []).forEach((d, i) => {
        const path = `decisions[${i}]`;
        if (!DECISION_KINDS.has(d.decision))
          errors.push({ field: `${path}.decision`, code: 'ENUM' });
        if (!REASON_CODE.test(d.reasonCode))
          errors.push({ field: `${path}.reasonCode`, code: 'FORMAT' });
        if ((d.reasonText ?? '').length > MAX_REASON_TEXT) {
          errors.push({ field: `${path}.reasonText`, code: 'LENGTH' });
        }
        // The split, exactly. A settlement that could not be reconciled by a kuruş is a
        // settlement nobody can sign off.
        if (toMicros(d.payerAmount) + toMicros(d.memberAmount) !== toMicros(d.approvedAmount)) {
          errors.push({ field: `${path}.memberAmount`, code: 'SPLIT' });
        }
        if (d.decision === 'REJECTED' && toMicros(d.approvedAmount) > ZERO) {
          errors.push({ field: `${path}.approvedAmount`, code: 'CONFLICT' });
        }
      });
      if ((body.reviewComment ?? '').length > MAX_REVIEW_COMMENT) {
        errors.push({ field: 'reviewComment', code: 'LENGTH' });
      }
      if (errors.length > 0) return validationFailed(api, errors);
      for (const d of body.decisions) {
        if (!byLineNo.has(d.lineNo)) {
          return problem(api, 422, 'CLAIM_LINE_NOT_FOUND', 'Bu sürümde böyle bir satır yok');
        }
      }

      for (const d of body.decisions) {
        const line = byLineNo.get(d.lineNo)!;
        writeDecision(
          gg.tenantId,
          line,
          version.versionNo,
          d.decision,
          d.approvedAmount,
          d.payerAmount,
          d.memberAmount,
          d.reasonCode,
          stage,
          {
            contractAmount: version.contractAmounts?.[line.lineNo] ?? null,
            approvedQuantity: d.approvedQuantity,
            reasonText: d.reasonText ?? null,
            decidedBy: gg.session.account.actorId,
          },
        );
        // A CUT is not also an adjustment row. The line decision *is* the cut — it records
        // the reduced approved amount with the reviewer, the stage and the reason — and
        // `claim.adjustment` is the ledger of money that moved **outside** a line decision.
        // Writing both would subtract the same hundred lira twice from `lines - adjustments`.
      }
      if (body.reviewComment !== undefined && body.reviewComment !== null) {
        if (stage === 'MEDICAL') claim.reviewCommentMedical = body.reviewComment;
        else claim.reviewCommentFinancial = body.reviewComment;
      }
      // Medical review finished and financial is still owed: the claim moves on rather than
      // waiting for somebody to notice.
      if (
        stage === 'MEDICAL' &&
        version.financialRequired &&
        latestDecisions(version.id).length === linesOf(version.id).length
      ) {
        claim.status = 'PENDING_FINANCIAL';
      }
      touch(claim);
      return answer(gg.session, gg.tenantId, claim);
    }),

    http.post(`${ANY}/api/v1/claims/:claimId/approve`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_FINANCIAL_REVIEW, true);
      const gg = 'error' in g ? guardTenant(api, request, PERMISSION_MEDICAL_REVIEW, true) : g;
      if ('error' in gg) return gg.error;
      const found = forCommand(
        request,
        gg.session,
        gg.tenantId,
        pathParam(params, 'claimId'),
        'APPROVE',
      );
      if ('error' in found) return found.error;
      const own = ownFile(api, gg.session, gg.tenantId, found.row.personId);
      if (own) return own;
      const body = await readJson<Schemas['ClaimDecisionReason']>(request);
      if (!body || !REASON_CODE.test(body.reasonCode)) {
        return validationFailed(api, [{ field: 'reasonCode', code: 'FORMAT' }]);
      }
      const claim = found.row;
      const version = versionOf(claim.id, claim.currentVersionNo);
      if (!version) return claimNotFound(api);
      const decisions = latestDecisions(version.id);
      if (decisions.length !== linesOf(version.id).length) {
        return problem(api, 409, 'CLAIM_LINE_UNDECIDED', 'Karara bağlanmamış satır var', {
          detail: 'Dosyayı sonuçlandırmadan önce her satır için karar girin.',
        });
      }
      claim.status = statusFor(decisions);
      if (claim.status === 'REJECTED') {
        claim.rejectReasonCode = body.reasonCode;
        claim.closedAt = new Date().toISOString();
      }
      if (body.reviewComment) claim.reviewCommentFinancial = body.reviewComment;
      touch(claim);
      return answer(gg.session, gg.tenantId, claim);
    }),

    http.post(`${ANY}/api/v1/claims/:claimId/reject`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_FINANCIAL_REVIEW, true);
      const gg = 'error' in g ? guardTenant(api, request, PERMISSION_MEDICAL_REVIEW, true) : g;
      if ('error' in gg) return gg.error;
      const found = forCommand(
        request,
        gg.session,
        gg.tenantId,
        pathParam(params, 'claimId'),
        'REJECT',
      );
      if ('error' in found) return found.error;
      const own = ownFile(api, gg.session, gg.tenantId, found.row.personId);
      if (own) return own;
      const body = await readJson<Schemas['ClaimDecisionReason']>(request);
      if (!body || !REASON_CODE.test(body.reasonCode)) {
        return validationFailed(api, [{ field: 'reasonCode', code: 'FORMAT' }]);
      }
      const claim = found.row;
      const version = versionOf(claim.id, claim.currentVersionNo);
      if (!version) return claimNotFound(api);
      const stage: Schemas['ClaimDecisionStage'] = hasPermission(
        api,
        gg.session,
        gg.tenantId,
        PERMISSION_MEDICAL_REVIEW,
      )
        ? 'MEDICAL'
        : 'FINANCIAL';
      // Every line is recorded REJECTED: a claim refused as a whole while its lines still
      // said "approved" would be a claim two systems disagreed about.
      for (const line of linesOf(version.id)) {
        writeDecision(
          gg.tenantId,
          line,
          version.versionNo,
          'REJECTED',
          '0',
          '0',
          '0',
          body.reasonCode,
          stage,
          {
            contractAmount: version.contractAmounts?.[line.lineNo] ?? null,
            approvedQuantity: '0',
            reasonText: body.reasonText ?? null,
            decidedBy: gg.session.account.actorId,
          },
        );
      }
      claim.status = 'REJECTED';
      claim.rejectReasonCode = body.reasonCode;
      claim.closedAt = new Date().toISOString();
      if (body.reviewComment) {
        if (stage === 'MEDICAL') claim.reviewCommentMedical = body.reviewComment;
        else claim.reviewCommentFinancial = body.reviewComment;
      }
      touch(claim);
      return answer(gg.session, gg.tenantId, claim);
    }),

    http.post(`${ANY}/api/v1/claims/:claimId/return`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_FINANCIAL_REVIEW, true);
      const gg = 'error' in g ? guardTenant(api, request, PERMISSION_MEDICAL_REVIEW, true) : g;
      if ('error' in gg) return gg.error;
      const found = forCommand(
        request,
        gg.session,
        gg.tenantId,
        pathParam(params, 'claimId'),
        'RETURN',
      );
      if ('error' in found) return found.error;
      const own = ownFile(api, gg.session, gg.tenantId, found.row.personId);
      if (own) return own;
      const body = await readJson<Schemas['ClaimReturnReason']>(request);
      if (!body || !REASON_CODE.test(body.reasonCode)) {
        return validationFailed(api, [{ field: 'reasonCode', code: 'FORMAT' }]);
      }
      const claim = found.row;
      const version = versionOf(claim.id, claim.currentVersionNo);
      if (!version || version.status !== 'SUBMITTED') return transitionInvalid(api);

      for (const line of linesOf(version.id)) {
        const draw = drawsOf().get(line.id);
        if (!draw) continue;
        draw.item.consumedQuantity = amount(toMicros(draw.item.consumedQuantity) - draw.quantity);
        drawsOf().delete(line.id);
      }
      // The decided version keeps everything it was given; only the return reason is added.
      version.status = 'SUPERSEDED';
      version.returnedAt = new Date().toISOString();
      version.returnedBy = gg.session.account.actorId;
      version.returnReasonCode = body.reasonCode;
      version.returnReasonText = body.reasonText ?? null;

      const next: StoredClaimVersion = {
        id: world().nextId(),
        tenantId: gg.tenantId,
        claimId: claim.id,
        versionNo: version.versionNo + 1,
        status: 'DRAFT',
        submittedAt: null,
        submittedBy: null,
        returnedAt: null,
        returnedBy: null,
        returnReasonCode: null,
        returnReasonText: null,
        financialRequired: false,
        exceptions: [],
        createdAt: new Date().toISOString(),
        rowVersion: 1,
      };
      world().claimVersions.push(next);
      // The lines are copied, clinical fields and all: the provider is correcting its own
      // statement, and dropping what a line named would change more than it meant.
      for (const line of linesOf(version.id)) {
        world().claimLines.push({
          ...line,
          id: world().nextId(),
          versionId: next.id,
          createdAt: next.createdAt,
          rowVersion: 1,
        });
      }
      claim.currentVersionNo = next.versionNo;
      claim.status = 'RETURNED';
      claim.returnReasonCode = body.reasonCode;
      touch(claim);
      return answer(gg.session, gg.tenantId, claim);
    }),

    http.post(`${ANY}/api/v1/claims/:claimId/cancel`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_CANCEL, true);
      if ('error' in g) return g.error;
      const found = forCommand(
        request,
        g.session,
        g.tenantId,
        pathParam(params, 'claimId'),
        'CANCEL',
      );
      if ('error' in found) return found.error;
      const body = await readJson<Schemas['ClaimReason']>(request);
      if (!body || !REASON_CODE.test(body.reasonCode)) {
        return validationFailed(api, [{ field: 'reasonCode', code: 'FORMAT' }]);
      }
      const claim = found.row;
      claim.status = 'CANCELLED';
      claim.closedAt = new Date().toISOString();
      touch(claim);
      return answer(g.session, g.tenantId, claim);
    }),

    http.get(`${ANY}/api/v1/claims/:claimId/versions`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      const row = findClaim(g.session, g.tenantId, pathParam(params, 'claimId'));
      if (!row) return claimNotFound(api);
      const body: Schemas['ClaimVersionList'] = {
        items: versionsOf(row.id).map(versionSummary),
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/claims/:claimId/versions/:versionNo`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      const req = accessRequest(request);
      const badAccess = accessHeaderProblem(api, req);
      if (badAccess) return badAccess;
      const row = findClaim(g.session, g.tenantId, pathParam(params, 'claimId'));
      if (!row) return claimNotFound(api);
      const versionNo = Number.parseInt(pathParam(params, 'versionNo'), 10);
      const version = Number.isFinite(versionNo) ? versionOf(row.id, versionNo) : undefined;
      if (!version) {
        return problem(api, 404, 'CLAIM_VERSION_NOT_FOUND', 'Hasar dosyası sürümü bulunamadı');
      }
      const d = decide(g.session, g.tenantId, row, req);
      if (d.purposeMissing) {
        recordAccess(g.session, g.tenantId, row, 'VIEW', req, 'DENIED');
        return accessPurposeRequired(api);
      }
      if (d.projection === 'CLINICAL') {
        recordAccess(g.session, g.tenantId, row, 'VIEW', req, 'SUCCESS');
      }
      const body: Schemas['ClaimVersion'] = {
        version: versionSummary(version),
        projection: d.projection,
        lines: linesOf(version.id).map((l) => projectLine(l, d.projection)),
        exceptions: projectExceptions(version.exceptions, d.projection),
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/claims/:claimId/invoice-readiness`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      const row = findClaim(g.session, g.tenantId, pathParam(params, 'claimId'));
      if (!row) return claimNotFound(api);
      if (row.status !== 'APPROVED' && row.status !== 'PARTIALLY_APPROVED') {
        return problem(api, 409, 'CLAIM_NOT_DECIDED', 'Dosya henüz sonuçlanmadı', {
          detail:
            'Fatura hazırlığı yalnızca onaylanmış ya da kısmen onaylanmış dosya için sorulur.',
        });
      }
      return HttpResponse.json(readinessOf(row), { headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/claims/:claimId/adjustments`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      const row = findClaim(g.session, g.tenantId, pathParam(params, 'claimId'));
      if (!row) return claimNotFound(api);
      const body: Schemas['ClaimAdjustmentList'] = {
        // Oldest first, which is the order the reversal chain reads in.
        items: adjustmentsOf(row.id).map(adjustmentView),
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),

    http.post(`${ANY}/api/v1/claims/:claimId/adjustments`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_FINANCIAL_REVIEW, true);
      if ('error' in g) return g.error;
      const row = findClaim(g.session, g.tenantId, pathParam(params, 'claimId'));
      if (!row) return claimNotFound(api);
      const body = await readJson<Schemas['CreateClaimAdjustment']>(request);
      if (!body) return problem(api, 400, 'INVALID_REQUEST_BODY', 'İstek gövdesi geçersiz');
      if (row.status !== 'APPROVED' && row.status !== 'PARTIALLY_APPROVED') {
        return problem(api, 409, 'CLAIM_NOT_DECIDED', 'Dosya henüz sonuçlanmadı');
      }
      const version = versionOf(row.id, row.currentVersionNo);
      const lines = version ? linesOf(version.id) : [];
      const currency = lines[0]?.currencyCode ?? 'TRY';

      let written: StoredClaimAdjustment;
      if (body.reversesAdjustmentId) {
        // A reversal carries no figures of its own; they are read off the row it takes back.
        if (
          body.amount !== undefined ||
          body.payerAmount !== undefined ||
          body.memberAmount !== undefined ||
          body.adjustmentType !== undefined
        ) {
          return validationFailed(api, [{ field: 'amount', code: 'CONFLICT' }]);
        }
        const original = adjustmentsOf(row.id).find((a) => a.id === body.reversesAdjustmentId);
        if (!original) {
          return problem(api, 404, 'CLAIM_ADJUSTMENT_NOT_FOUND', 'Düzeltme kaydı bulunamadı');
        }
        if (original.adjustmentType === 'REVERSAL') {
          return problem(api, 409, 'CLAIM_ADJUSTMENT_NOT_REVERSIBLE', 'İptal kaydı iptal edilemez');
        }
        if (adjustmentsOf(row.id).some((a) => a.reversesAdjustmentId === original.id)) {
          return problem(api, 409, 'CLAIM_ADJUSTMENT_REVERSED', 'Bu düzeltme zaten iptal edilmiş');
        }
        if (!ADJUSTMENT_REASONS.has(body.reasonCode)) {
          return validationFailed(api, [{ field: 'reasonCode', code: 'ENUM' }]);
        }
        written = {
          id: world().nextId(),
          tenantId: g.tenantId,
          claimId: row.id,
          versionNo: row.currentVersionNo,
          claimLineId: original.claimLineId,
          adjustmentType: 'REVERSAL',
          amount: amount(-toMicros(original.amount)),
          payerAmount: amount(-toMicros(original.payerAmount)),
          memberAmount: amount(-toMicros(original.memberAmount)),
          currencyCode: original.currencyCode,
          reasonCode: body.reasonCode,
          reasonText: body.reasonText ?? null,
          sourceType: 'MANUAL',
          sourceId: null,
          reversesAdjustmentId: original.id,
          createdBy: g.session.account.actorId,
          createdAt: new Date().toISOString(),
        };
      } else {
        const errors: FieldError[] = [];
        if (!body.adjustmentType || !CALLER_ADJUSTMENT_TYPES.has(body.adjustmentType)) {
          errors.push({ field: 'adjustmentType', code: 'ENUM' });
        }
        if (!ADJUSTMENT_REASONS.has(body.reasonCode)) {
          errors.push({ field: 'reasonCode', code: 'ENUM' });
        }
        const amountMicros = toMicros(body.amount ?? '');
        const payerMicros = toMicros(body.payerAmount ?? '');
        const memberMicros = toMicros(body.memberAmount ?? '');
        // The split, the same rule the column CHECKs: the two halves are the whole, exactly.
        if (payerMicros + memberMicros !== amountMicros) {
          errors.push({ field: 'memberAmount', code: 'SPLIT' });
        }
        if (amountMicros === ZERO) errors.push({ field: 'amount', code: 'RANGE' });
        if (body.adjustmentType !== 'CORRECTION' && amountMicros < ZERO) {
          errors.push({ field: 'amount', code: 'RANGE' });
        }
        let lineId: string | null = null;
        if (body.lineNo !== undefined && body.lineNo !== null) {
          const line = lines.find((l) => l.lineNo === body.lineNo);
          if (!line) {
            return problem(
              api,
              422,
              'CLAIM_ADJUSTMENT_LINE_NOT_FOUND',
              'Bu sürümde böyle bir satır yok',
            );
          }
          lineId = line.id;
        }
        if (body.currencyCode && body.currencyCode !== currency) {
          return problem(
            api,
            422,
            'CLAIM_ADJUSTMENT_CURRENCY',
            'Düzeltme dosyanın para biriminde olmalı',
          );
        }
        if (errors.length > 0) return validationFailed(api, errors);
        written = {
          id: world().nextId(),
          tenantId: g.tenantId,
          claimId: row.id,
          versionNo: row.currentVersionNo,
          claimLineId: lineId,
          adjustmentType: body.adjustmentType!,
          amount: amount(amountMicros),
          payerAmount: amount(payerMicros),
          memberAmount: amount(memberMicros),
          currencyCode: currency,
          reasonCode: body.reasonCode,
          reasonText: body.reasonText ?? null,
          sourceType: body.adjustmentType === 'RECOVERY' ? 'RECOVERY' : 'REVIEW',
          sourceId: null,
          reversesAdjustmentId: null,
          createdBy: g.session.account.actorId,
          createdAt: new Date().toISOString(),
        };
      }
      world().claimAdjustments.push(written);
      const result: Schemas['ClaimAdjustmentResult'] = {
        adjustment: adjustmentView(written),
        readiness: readinessOf(row),
      };
      return HttpResponse.json(result, { status: 201, headers: NO_STORE });
    }),

    http.get(`${ANY}/api/v1/providers/:providerId/earnings`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, PERMISSION_READ, false);
      if ('error' in g) return g.error;
      const providerId = pathParam(params, 'providerId');
      const scope = organizationScope(api, g.session, g.tenantId);
      if (scope !== null && !withinScope(scope, providerId)) {
        return problem(api, 403, 'CLAIM_PROVIDER_SCOPE', 'Bu sağlayıcı adına işlem yapamazsınız');
      }
      const relationship = world().relationships.find(
        (r) => r.id === providerId && r.tenantId === g.tenantId,
      );
      if (!relationship) return problem(api, 404, 'CLAIM_NOT_FOUND', 'Sağlayıcı bulunamadı');
      const organization = world().organizations.get(relationship.organizationId);

      const url = new URL(request.url);
      const from = url.searchParams.get('from');
      const to = url.searchParams.get('to');
      const currencyFilter = url.searchParams.get('currency');

      const buckets = new Map<
        string,
        {
          claimCount: number;
          approved: bigint;
          payer: bigint;
          member: bigint;
          adjusted: bigint;
          billable: bigint;
          ids: string[];
          byStatus: Map<string, { count: number; total: bigint }>;
        }
      >();
      const onLiveInvoice = claimsOnLiveInvoice(world(), g.tenantId);
      for (const claim of world().claims) {
        if (claim.tenantId !== g.tenantId) continue;
        if (claim.providerOrganizationId !== providerId) continue;
        if (!EARNING_STATUSES.has(claim.status)) continue;
        const version = versionOf(claim.id, claim.currentVersionNo);
        if (!version) continue;
        const decisions = latestDecisions(version.id);
        if (decisions.length === 0) continue;
        // The period bounds the decision, not the service date: what a provider earned in
        // March is what was decided in March.
        const decidedAt = decisions
          .map((d) => d.decidedAt)
          .sort()
          .at(-1)!;
        if (from && decidedAt.slice(0, 10) < from) continue;
        if (to && decidedAt.slice(0, 10) > to) continue;
        const lines = linesOf(version.id);
        const currency = lines[0]?.currencyCode ?? 'TRY';
        if (currencyFilter && currency !== currencyFilter) continue;

        const totals = claimTotals(world(), claim.id);
        const approved = totals.approved;
        let bucket = buckets.get(currency);
        if (!bucket) {
          bucket = {
            claimCount: 0,
            approved: ZERO,
            payer: ZERO,
            member: ZERO,
            adjusted: ZERO,
            billable: ZERO,
            ids: [],
            byStatus: new Map(),
          };
          buckets.set(currency, bucket);
        }
        bucket.claimCount += 1;
        bucket.approved += approved;
        bucket.payer += totals.payer;
        bucket.member += totals.member;
        bucket.adjusted += totals.adjustmentTotal;
        // Invoiceable is "the payer has answered it and nobody is already collecting it".
        // Both halves are needed: the status keeps out a draft and a rejection, and the link
        // keeps out a claim already sitting on a live invoice -- which is still APPROVED, and
        // which a provider offered it twice would put on two documents.
        if (
          (claim.status === 'APPROVED' || claim.status === 'PARTIALLY_APPROVED') &&
          !onLiveInvoice.has(claim.id)
        ) {
          bucket.billable += approved;
          bucket.ids.push(claim.id);
        }
        const status = bucket.byStatus.get(claim.status) ?? { count: 0, total: ZERO };
        status.count += 1;
        status.total += approved;
        bucket.byStatus.set(claim.status, status);
      }

      const body: Schemas['ProviderEarnings'] = {
        providerOrganizationId: providerId,
        providerName: organization?.displayName ?? '',
        from: from ?? null,
        to: to ?? null,
        currencies: [...buckets.entries()]
          .sort((a, b) => a[0].localeCompare(b[0]))
          .map(([currencyCode, bucket]) => ({
            currencyCode,
            claimCount: bucket.claimCount,
            approvedTotal: amount(bucket.approved),
            payerTotal: amount(bucket.payer),
            memberTotal: amount(bucket.member),
            adjustmentTotal: amount(bucket.adjusted),
            invoiceableTotal: amount(bucket.billable),
            invoiceableClaimIds: bucket.ids,
            byStatus: [...bucket.byStatus.entries()]
              .sort((a, b) => a[0].localeCompare(b[0]))
              .map(([status, row]) => ({
                status: status as Schemas['ClaimStatus'],
                claimCount: row.count,
                approvedTotal: amount(row.total),
              })),
          })),
      };
      return HttpResponse.json(body, { headers: NO_STORE });
    }),
  ];
}
