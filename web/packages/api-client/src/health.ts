import { randomId, type KapsoraClient } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type HealthCase = components['schemas']['HealthCase'];
export type HealthCasePage = components['schemas']['HealthCasePage'];
export type HealthCaseSensitivity = components['schemas']['HealthCaseSensitivity'];
export type HealthCaseStatus = components['schemas']['HealthCaseStatus'];
export type HealthCaseType = components['schemas']['HealthCaseType'];
export type HealthProjection = components['schemas']['HealthProjection'];
export type Encounter = components['schemas']['Encounter'];
export type EncounterType = components['schemas']['EncounterType'];
export type Diagnosis = components['schemas']['Diagnosis'];
export type DiagnosisInput = components['schemas']['DiagnosisInput'];
export type DiagnosisList = components['schemas']['DiagnosisList'];
export type DiagnosisType = components['schemas']['DiagnosisType'];
export type HealthAccessEvent = components['schemas']['HealthAccessEvent'];
export type HealthAccessLogPage = components['schemas']['HealthAccessLogPage'];
export type CreateHealthCase = components['schemas']['CreateHealthCase'];
export type CloseHealthCase = components['schemas']['CloseHealthCase'];
export type CreateEncounter = components['schemas']['CreateEncounter'];
export type PutEncounterDiagnoses = components['schemas']['PutEncounterDiagnoses'];

/** The codes the contract admits in X-Access-Purpose; it is an enum, not free text. */
export type AccessPurpose = components['parameters']['AccessPurposeHeader'];

/**
 * Why the caller is opening clinical data, carried on the read that opens it.
 *
 * The purpose is not a hint. A SENSITIVE case, encounter, report, stay or claim read
 * without one is answered 428 ACCESS_PURPOSE_REQUIRED and the refusal is itself written to
 * the access log, so a screen that reads clinical detail has to have asked the operator why
 * before it asks the server. The reason is the operator's own sentence beside the code.
 */
export interface AccessContext {
  purpose?: AccessPurpose;
  reason?: string;
  /**
   * Ask for the financial half only. A caller who holds the sensitive grant may decline to
   * read clinical detail: the answer is the financial projection, no purpose is required,
   * and nothing is written to the access log, because choosing not to look is not a look.
   */
  projection?: AccessProjection;
}

export type AccessProjection = components['parameters']['AccessProjectionHeader'];

/**
 * The two access headers, ready to spread beside X-Tenant-ID.
 *
 * `reason` is percent-encoded on the way out. An HTTP header value is ISO-8859-1 and a
 * browser refuses to send one carrying ğ, ş or ı — which is most of the Turkish somebody
 * would actually type — so the contract asks for `encodeURIComponent` and the server
 * decodes it back. Nothing here decodes: what comes back on an access event is already
 * plain text.
 */
export function accessHeaders(access?: AccessContext): {
  'X-Access-Purpose'?: AccessPurpose;
  'X-Access-Reason'?: string;
  'X-Access-Projection'?: AccessProjection;
} {
  if (!access) return {};
  const headers: {
    'X-Access-Purpose'?: AccessPurpose;
    'X-Access-Reason'?: string;
    'X-Access-Projection'?: AccessProjection;
  } = {};
  if (access.purpose) headers['X-Access-Purpose'] = access.purpose;
  if (access.reason) headers['X-Access-Reason'] = encodeURIComponent(access.reason);
  if (access.projection) headers['X-Access-Projection'] = access.projection;
  return headers;
}

/** Filters of the health case list. */
export interface HealthCaseListQuery {
  cursor?: string;
  limit?: number;
  personId?: string;
  programId?: string;
  providerOrganizationId?: string;
  status?: HealthCaseStatus;
  caseType?: HealthCaseType;
  openedFrom?: string;
  /** Exclusive upper bound. */
  openedTo?: string;
}

/** Filters of the clinical access log. */
export interface HealthAccessLogQuery {
  cursor?: string;
  limit?: number;
  personId?: string;
}

/**
 * Health cases, their encounters and the diagnoses recorded against them.
 *
 * Every record here arrives in one of two projections and says which one it is in. A
 * screen must read `projection` and say "you may not see clinical detail" rather than draw
 * a record with holes in it: the financial projection is a complete answer to a different
 * question, not a broken answer to this one. `sensitivity`, `branchCode`, `notesClinical`
 * and the diagnosis count are simply absent from it, because their absence is the point.
 *
 * `listDiagnoses` is the one read that refuses rather than narrows. An empty array would
 * tell the caller the encounter has no diagnosis, which is a clinical fact it may not have,
 * so a caller without the clinical grant is answered 403 CLINICAL_READ_REQUIRED. Render
 * that refusal as a refusal; never as "no diagnoses recorded".
 */
export function healthOperations(client: KapsoraClient) {
  const header = (tenantId: string) => ({ 'X-Tenant-ID': tenantId });
  const read = (tenantId: string, access?: AccessContext) => ({
    'X-Tenant-ID': tenantId,
    ...accessHeaders(access),
  });
  const create = (tenantId: string, idempotencyKey: string) => ({
    'X-Tenant-ID': tenantId,
    'Idempotency-Key': idempotencyKey,
  });
  const command = (tenantId: string, etag: string, idempotencyKey: string) => ({
    'X-Tenant-ID': tenantId,
    'If-Match': etag,
    'Idempotency-Key': idempotencyKey,
  });

  return {
    /**
     * The tenant's cases, newest first. A list never answers 428: a sensitive row a caller
     * may not open arrives in the financial projection like any other, because refusing it
     * would say which row is sensitive.
     */
    async listCases(
      tenantId: string,
      query: HealthCaseListQuery = {},
      access?: AccessContext,
    ): Promise<HealthCasePage> {
      const q: HealthCaseListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.personId) q.personId = query.personId;
      if (query.programId) q.programId = query.programId;
      if (query.providerOrganizationId) q.providerOrganizationId = query.providerOrganizationId;
      if (query.status) q.status = query.status;
      if (query.caseType) q.caseType = query.caseType;
      if (query.openedFrom) q.openedFrom = query.openedFrom;
      if (query.openedTo) q.openedTo = query.openedTo;
      return (
        await unwrap(
          client.GET('/api/v1/health-cases', {
            params: { header: read(tenantId, access), query: q },
          }),
        )
      ).data;
    },

    /**
     * Opens a case. Sensitivity is never sent: it is derived from the diagnoses recorded
     * against the case's encounters, and a caller that could set it could clear it.
     */
    async createCase(
      tenantId: string,
      body: CreateHealthCase,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<HealthCase>> {
      const r = await unwrap(
        client.POST('/api/v1/health-cases', {
          params: { header: create(tenantId, idempotencyKey) },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * One case with its encounters. This is the read that demands a purpose: without one, a
     * sensitive case answers 428 ACCESS_PURPOSE_REQUIRED and the refusal is logged as a
     * DENIED access event. Ask the operator why before retrying, and never retry silently.
     */
    async getCase(
      tenantId: string,
      caseId: string,
      access?: AccessContext,
    ): Promise<Versioned<HealthCase>> {
      const r = await unwrap(
        client.GET('/api/v1/health-cases/{caseId}', {
          params: { header: read(tenantId, access), path: { caseId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Closes a case. Every encounter has to have ended and no inpatient stay may still be
     * open, so a 409 here names something the screen can send the user to fix.
     */
    async closeCase(
      tenantId: string,
      caseId: string,
      etag: string,
      body: CloseHealthCase = {},
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<HealthCase>> {
      const r = await unwrap(
        client.POST('/api/v1/health-cases/{caseId}/close', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { caseId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Records one contact inside a case. `notesClinical` is the module's one free-text
     * clinical field: writing it needs the same clinical grant reading it does.
     */
    async createEncounter(
      tenantId: string,
      caseId: string,
      body: CreateEncounter,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Encounter>> {
      const r = await unwrap(
        client.POST('/api/v1/health-cases/{caseId}/encounters', {
          params: { header: create(tenantId, idempotencyKey), path: { caseId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * One encounter. The financial projection carries its dates, location and practitioner
     * and neither its branch code nor its clinical notes — a branch of "onkoloji" is a
     * diagnosis anybody can read off a roster.
     */
    async getEncounter(
      tenantId: string,
      encounterId: string,
      access?: AccessContext,
    ): Promise<Versioned<Encounter>> {
      const r = await unwrap(
        client.GET('/api/v1/encounters/{encounterId}', {
          params: { header: read(tenantId, access), path: { encounterId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * The encounter's diagnoses, primary first. A caller without the clinical read — or
     * without the sensitive grant on a sensitive case — is answered 403, not an empty
     * array. Show the refusal; an empty list drawn here would be a lie about the patient.
     */
    async listDiagnoses(
      tenantId: string,
      encounterId: string,
      access?: AccessContext,
    ): Promise<Diagnosis[]> {
      const r = await unwrap(
        client.GET('/api/v1/encounters/{encounterId}/diagnoses', {
          params: { header: read(tenantId, access), path: { encounterId } },
        }),
      );
      return r.data.items;
    },

    /**
     * Replaces the encounter's diagnoses as a whole; the set is the unit. A second PRIMARY
     * is 422. `sensitive` is not accepted from the caller: it is read from each code
     * value's own category, and the case's sensitivity is recomputed from what was stored,
     * so the case a screen holds is stale after this call.
     */
    async putDiagnoses(
      tenantId: string,
      encounterId: string,
      body: PutEncounterDiagnoses,
      idempotencyKey: string = randomId(),
    ): Promise<Diagnosis[]> {
      const r = await unwrap(
        client.PUT('/api/v1/encounters/{encounterId}/diagnoses', {
          params: { header: create(tenantId, idempotencyKey), path: { encounterId } },
          body,
        }),
      );
      return r.data.items;
    },

    /**
     * Who looked at a person's clinical data, when, and why — the member's own right to
     * know, answered from the audit log. It carries reads and not writes, and a refused
     * sensitive read is present with outcome DENIED: a screen that filtered those out would
     * hide the attempts, which are the rows worth reading.
     */
    async listAccessLog(
      tenantId: string,
      query: HealthAccessLogQuery = {},
    ): Promise<HealthAccessLogPage> {
      const q: HealthAccessLogQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.personId) q.personId = query.personId;
      return (
        await unwrap(
          client.GET('/api/v1/health-access-log', {
            params: { header: header(tenantId), query: q },
          }),
        )
      ).data;
    },
  };
}
