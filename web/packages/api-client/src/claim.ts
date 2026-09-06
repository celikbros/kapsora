import { randomId, type KapsoraClient } from './client';
import type { components } from './generated/kapsora-v1';
import { accessHeaders, type AccessContext } from './health';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type Claim = components['schemas']['Claim'];
export type ClaimPage = components['schemas']['ClaimPage'];
export type ClaimStatus = components['schemas']['ClaimStatus'];
export type ClaimLine = components['schemas']['ClaimLine'];
export type ClaimLineDecision = components['schemas']['ClaimLineDecision'];
export type ClaimLineDecisionInput = components['schemas']['ClaimLineDecisionInput'];
export type ClaimDecisionKind = components['schemas']['ClaimDecisionKind'];
export type ClaimDecisionStage = components['schemas']['ClaimDecisionStage'];
export type ClaimException = components['schemas']['ClaimException'];
export type ClaimVersion = components['schemas']['ClaimVersion'];
export type ClaimVersionList = components['schemas']['ClaimVersionList'];
export type ClaimVersionStatus = components['schemas']['ClaimVersionStatus'];
export type ClaimVersionSummary = components['schemas']['ClaimVersionSummary'];
export type ClaimInvoiceReadiness = components['schemas']['ClaimInvoiceReadiness'];
export type ClaimInvoiceBlocker = components['schemas']['ClaimInvoiceBlocker'];
export type ClaimReason = components['schemas']['ClaimReason'];
export type ClaimDecisionReason = components['schemas']['ClaimDecisionReason'];
export type ClaimReturnReason = components['schemas']['ClaimReturnReason'];
export type NewClaimLine = components['schemas']['NewClaimLine'];
export type CreateClaim = components['schemas']['CreateClaim'];
export type PatchClaimDraft = components['schemas']['PatchClaimDraft'];
export type PutClaimLines = components['schemas']['PutClaimLines'];
export type DecideClaimLines = components['schemas']['DecideClaimLines'];

/** Filters of the claim list. */
export interface ClaimListQuery {
  cursor?: string;
  limit?: number;
  personId?: string;
  caseId?: string;
  providerOrganizationId?: string;
  status?: ClaimStatus;
  serviceDateFrom?: string;
  serviceDateTo?: string;
}

/**
 * Claims: the provider's statement, the pipeline that prices and routes it, the two review
 * stages that decide it and the versions that keep every earlier answer readable.
 *
 * A submitted version is never edited. `patchDraft` and `putLines` answer 409
 * CLAIM_VERSION_FROZEN on anything that has been sent, and the correction path is
 * `returnForCorrection` — which supersedes the decided version, keeps its line decisions, and opens
 * version n+1 as a draft with the lines copied. A screen that offers "edit" on a submitted
 * claim is offering something the server will refuse.
 *
 * The review stage is never the caller's choice. A claim in PENDING_MEDICAL is decided by a
 * medical reviewer and the rows are written MEDICAL; PENDING_FINANCIAL is decided by a
 * financial reviewer and written FINANCIAL. A financial reviewer reaching a claim still in
 * medical review is answered 409 CLAIM_STAGE_MISMATCH — medical precedes financial when
 * both are needed, and that ordering is the point of having two stages. Decisions are
 * append-only: a line decided twice has two rows and `decision` is the head of that
 * history, so "who cut this line" is answerable and a screen should not present the latest
 * decision as the only one there ever was.
 *
 * `exceptions` is why the claim is in front of somebody, line by line, and it is not a
 * decision — the lines it names are still undecided. Draw it first.
 *
 * Every amount and quantity is an exact decimal string, summed on the server. Pass them
 * through untouched and never add a total up in the browser: `payerAmount + memberAmount`
 * must equal `approvedAmount` exactly, and that is a database CHECK, not a nicety.
 */
export function claimOperations(client: KapsoraClient) {
  const header = (tenantId: string) => ({ 'X-Tenant-ID': tenantId });
  const read = (tenantId: string, access?: AccessContext) => ({
    'X-Tenant-ID': tenantId,
    ...accessHeaders(access),
  });
  const create = (tenantId: string, idempotencyKey: string, access?: AccessContext) => ({
    'X-Tenant-ID': tenantId,
    'Idempotency-Key': idempotencyKey,
    ...accessHeaders(access),
  });
  const command = (
    tenantId: string,
    etag: string,
    idempotencyKey: string,
    access?: AccessContext,
  ) => ({
    'X-Tenant-ID': tenantId,
    'If-Match': etag,
    'Idempotency-Key': idempotencyKey,
    ...accessHeaders(access),
  });

  return {
    /**
     * Claims newest first. The financial half keeps every figure, decision and reason code
     * and drops the line description, `diagnosisId`, `medicalReportId` and
     * `reviewCommentMedical` — enough to reconcile the money, nothing that says what was
     * wrong with the person.
     */
    async list(
      tenantId: string,
      query: ClaimListQuery = {},
      access?: AccessContext,
    ): Promise<ClaimPage> {
      const q: ClaimListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.personId) q.personId = query.personId;
      if (query.caseId) q.caseId = query.caseId;
      if (query.providerOrganizationId) q.providerOrganizationId = query.providerOrganizationId;
      if (query.status) q.status = query.status;
      if (query.serviceDateFrom) q.serviceDateFrom = query.serviceDateFrom;
      if (query.serviceDateTo) q.serviceDateTo = query.serviceDateTo;
      return (
        await unwrap(
          client.GET('/api/v1/claims', { params: { header: read(tenantId, access), query: q } }),
        )
      ).data;
    },

    /**
     * Opens a draft with version 1 and the lines it was given. Nothing is priced and
     * nothing is decided here — the pipeline runs at `submit`, so a draft's amounts
     * are the provider's statement and never the payer's answer. `caseId`, `fulfilmentId`
     * and `authorizationId` each change what submit will check: a claim naming an
     * authorization consumes it line by line, and one naming none consumes nothing.
     */
    async create(
      tenantId: string,
      body: CreateClaim,
      idempotencyKey: string = randomId(),
      access?: AccessContext,
    ): Promise<Versioned<Claim>> {
      const r = await unwrap(
        client.POST('/api/v1/claims', {
          params: { header: create(tenantId, idempotencyKey, access) },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * One claim with the lines of its current version and the decision each carries. This
     * is the read that demands a purpose: a claim on a sensitive case without one is 428,
     * logged DENIED against `claim`.
     */
    async get(
      tenantId: string,
      claimId: string,
      access?: AccessContext,
    ): Promise<Versioned<Claim>> {
      const r = await unwrap(
        client.GET('/api/v1/claims/{claimId}', {
          params: { header: read(tenantId, access), path: { claimId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Edits the header of a claim whose current version is still a DRAFT. Every field goes
     * every time — a full replacement, not a merge patch. A submitted version answers 409
     * CLAIM_VERSION_FROZEN; the way forward from there is `returnForCorrection`.
     */
    async patchDraft(
      tenantId: string,
      claimId: string,
      etag: string,
      body: PatchClaimDraft,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Claim>> {
      const r = await unwrap(
        client.PATCH('/api/v1/claims/{claimId}', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { claimId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Replaces the whole line set of the DRAFT version. The set is the unit: a claim is
     * what its lines say together, and a per-line edit would let two clerks leave a claim
     * nobody meant to send.
     */
    async putLines(
      tenantId: string,
      claimId: string,
      etag: string,
      body: PutClaimLines,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Claim>> {
      const r = await unwrap(
        client.PUT('/api/v1/claims/{claimId}/lines', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { claimId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Freezes the draft and runs price, rules, cross-checks and routing in one transaction.
     * The claim that comes back may already be finished — AUTO_ADJUDICATED and then
     * APPROVED, PARTIALLY_APPROVED or REJECTED — or waiting in PENDING_MEDICAL or
     * PENDING_FINANCIAL. Read `status` and `exceptions` off the answer rather than assuming
     * a submit leads to a queue.
     */
    async submit(
      tenantId: string,
      claimId: string,
      etag: string,
      idempotencyKey: string = randomId(),
      access?: AccessContext,
    ): Promise<Versioned<Claim>> {
      const r = await unwrap(
        client.POST('/api/v1/claims/{claimId}/submit', {
          params: { header: command(tenantId, etag, idempotencyKey, access), path: { claimId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Records a decision on each named line, at the stage the claim is waiting in. The
     * stage is read from the claim and the caller's grant, never sent; 409
     * CLAIM_STAGE_MISMATCH means this reviewer is early, not wrong. On every line
     * `payerAmount + memberAmount` must equal `approvedAmount` exactly — validate that in
     * the form, because a kuruş out is a 422 the reviewer will not be able to explain.
     */
    async decideLines(
      tenantId: string,
      claimId: string,
      etag: string,
      body: DecideClaimLines,
      idempotencyKey: string = randomId(),
      access?: AccessContext,
    ): Promise<Versioned<Claim>> {
      const r = await unwrap(
        client.POST('/api/v1/claims/{claimId}/line-decisions', {
          params: { header: command(tenantId, etag, idempotencyKey, access), path: { claimId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Finishes the claim; every line must already carry a decision, or it is 409
     * CLAIM_LINE_UNDECIDED. The status it lands in is what those decisions say, so do not
     * show "onaylandı" before reading it back. The approval policy for `claim.approve`
     * decides who may do this by the band the approved total falls in: a caller whose
     * review stage is not named is 403 CLAIM_APPROVAL_NOT_PERMITTED, which is a routing
     * fact worth telling them plainly.
     */
    async approve(
      tenantId: string,
      claimId: string,
      etag: string,
      body: ClaimDecisionReason,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Claim>> {
      const r = await unwrap(
        client.POST('/api/v1/claims/{claimId}/approve', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { claimId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Refuses the claim outright. Every line of the current version is recorded REJECTED at
     * the caller's stage, whatever it carried before, and anything the claim was holding on
     * its authorization is released.
     */
    async reject(
      tenantId: string,
      claimId: string,
      etag: string,
      body: ClaimDecisionReason,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Claim>> {
      const r = await unwrap(
        client.POST('/api/v1/claims/{claimId}/reject', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { claimId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Sends the claim back to the provider to be corrected. It never edits the decided
     * version: that one becomes SUPERSEDED carrying the return reason and keeps every line
     * decision, and version n+1 opens as a DRAFT with the lines copied. The claim that
     * comes back is therefore a new draft — re-read it before letting anybody edit.
     */
    async returnForCorrection(
      tenantId: string,
      claimId: string,
      etag: string,
      body: ClaimReturnReason,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Claim>> {
      const r = await unwrap(
        client.POST('/api/v1/claims/{claimId}/return', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { claimId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Withdraws a claim the provider should not have raised. Refused on anything already
     * decided: cancelling a settled claim would be an accounting entry rather than a
     * withdrawal.
     */
    async cancel(
      tenantId: string,
      claimId: string,
      etag: string,
      body: ClaimReason,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Claim>> {
      const r = await unwrap(
        client.POST('/api/v1/claims/{claimId}/cancel', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { claimId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Every version, newest first, with what each was submitted and returned for. It is how
     * a screen draws "sürüm 2 / 2" and how anybody finds the decision made about a version
     * somebody has since corrected.
     */
    async listVersions(tenantId: string, claimId: string): Promise<ClaimVersionSummary[]> {
      const r = await unwrap(
        client.GET('/api/v1/claims/{claimId}/versions', {
          params: { header: header(tenantId), path: { claimId } },
        }),
      );
      return r.data.items;
    },

    /**
     * One version with the lines it carried and the decision each was given. A superseded
     * version answers exactly what it answered the day it was decided, so this is the read
     * a dispute is settled from — and it carries its own `projection`.
     */
    async getVersion(
      tenantId: string,
      claimId: string,
      versionNo: number,
      access?: AccessContext,
    ): Promise<ClaimVersion> {
      return (
        await unwrap(
          client.GET('/api/v1/claims/{claimId}/versions/{versionNo}', {
            params: { header: read(tenantId, access), path: { claimId, versionNo } },
          }),
        )
      ).data;
    },

    /**
     * What M7's invoice will need, answered now: the approved, payer and member totals as
     * exact decimals summed on the server once, and the blockers by name —
     * PROVIDER_TAX_IDENTITY_MISSING, CURRENCY_NOT_SINGLE, LINE_NOT_DECIDED. Show the names:
     * a screen that says "not ready" without saying what to fix has thrown the answer away.
     * Only an APPROVED or PARTIALLY_APPROVED claim has one; anything else is 409
     * CLAIM_NOT_DECIDED.
     */
    async readiness(tenantId: string, claimId: string): Promise<ClaimInvoiceReadiness> {
      return (
        await unwrap(
          client.GET('/api/v1/claims/{claimId}/invoice-readiness', {
            params: { header: header(tenantId), path: { claimId } },
          }),
        )
      ).data;
    },
  };
}
