import { randomId, type KapsoraClient } from './client';
import type { components } from './generated/kapsora-v1';
import { accessHeaders, type AccessContext } from './health';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type InpatientStay = components['schemas']['InpatientStay'];
export type InpatientStayPage = components['schemas']['InpatientStayPage'];
export type InpatientStayStatus = components['schemas']['InpatientStayStatus'];
export type StayExtension = components['schemas']['StayExtension'];
export type StayExtensionStatus = components['schemas']['StayExtensionStatus'];
export type StaySegment = components['schemas']['StaySegment'];
export type StaySegmentInput = components['schemas']['StaySegmentInput'];
export type StaySegmentType = components['schemas']['StaySegmentType'];
export type StayReconciliation = components['schemas']['StayReconciliation'];
export type CreateInpatientStay = components['schemas']['CreateInpatientStay'];
export type ExtendInpatientStay = components['schemas']['ExtendInpatientStay'];
export type PutStaySegments = components['schemas']['PutStaySegments'];
export type DischargeInpatientStay = components['schemas']['DischargeInpatientStay'];
export type CancelInpatientStay = components['schemas']['CancelInpatientStay'];

/** Filters of the inpatient stay list. */
export interface InpatientStayListQuery {
  cursor?: string;
  limit?: number;
  caseId?: string;
  personId?: string;
  providerOrganizationId?: string;
  status?: InpatientStayStatus;
  admittedFrom?: string;
  admittedTo?: string;
}

/**
 * Admissions: asking for one, recording where the patient actually is, extending the stay
 * and settling up at discharge.
 *
 * Nothing here authorizes anything. `create` raises a PREAUTHORIZATION service request and
 * writes the stay REQUESTED; a medical reviewer decides that request on the request page,
 * where they decide every request, and the stay follows into AUTHORIZED or REJECTED without
 * the reviewer ever learning a stay exists. A screen must not offer an "approve" button
 * here — there is no endpoint behind it.
 *
 * There is no admit command either. Recording where somebody is *is* the admission:
 * `putSegments` on an AUTHORIZED stay moves it to ADMITTED. Segments may not claim the same
 * hours, checked by an exclusion constraint over half-open ranges, so a ward segment ending
 * at 14:00 beside an intensive care segment starting at 14:00 is a transfer and not a 409.
 * COMPANION sits outside that rule: the relative sleeping in the room overlaps the patient
 * by definition.
 *
 * Day counts and released days are exact decimal strings and are passed through untouched.
 */
export function inpatientOperations(client: KapsoraClient) {
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
     * Admissions newest first. The financial projection keeps every date, every figure of
     * the reconciliation and every segment — a claims reviewer who could not see an
     * intensive care night could not check the bill for one — and drops
     * `admissionDiagnosisId` and the extension reason text.
     */
    async list(
      tenantId: string,
      query: InpatientStayListQuery = {},
      access?: AccessContext,
    ): Promise<InpatientStayPage> {
      const q: InpatientStayListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.caseId) q.caseId = query.caseId;
      if (query.personId) q.personId = query.personId;
      if (query.providerOrganizationId) q.providerOrganizationId = query.providerOrganizationId;
      if (query.status) q.status = query.status;
      if (query.admittedFrom) q.admittedFrom = query.admittedFrom;
      if (query.admittedTo) q.admittedTo = query.admittedTo;
      return (
        await unwrap(
          client.GET('/api/v1/inpatient-stays', {
            params: { header: read(tenantId, access), query: q },
          }),
        )
      ).data;
    },

    /**
     * Asks for an admission. Three refusals a screen should name rather than generalise:
     * 422 ADMISSION_DATE_OUT_OF_WINDOW (outside the tenant's backdating/future window),
     * 409 INPATIENT_STAY_ALREADY_OPEN (this case already has an open stay at this provider
     * — refused by a partial unique index, so two callers racing leave exactly one stay),
     * and 422 INPATIENT_ADMISSION_SERVICE_UNKNOWN (the catalogue has no active INPATIENT_DAY
     * definition to book the days against, which is a setup problem and not the user's).
     */
    async create(
      tenantId: string,
      body: CreateInpatientStay,
      idempotencyKey: string = randomId(),
      access?: AccessContext,
    ): Promise<Versioned<InpatientStay>> {
      const r = await unwrap(
        client.POST('/api/v1/inpatient-stays', {
          params: { header: create(tenantId, idempotencyKey, access) },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * One admission with its extensions and its segments. This is the read that demands a
     * purpose: a stay on a sensitive case without one is 428, logged DENIED against
     * `inpatient_stay`.
     */
    async get(
      tenantId: string,
      stayId: string,
      access?: AccessContext,
    ): Promise<Versioned<InpatientStay>> {
      const r = await unwrap(
        client.GET('/api/v1/inpatient-stays/{stayId}', {
          params: { header: read(tenantId, access), path: { stayId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Asks for more days, through the extension's own preauthorization request. 409
     * STAY_EXTENSION_PENDING means an earlier extension is still undecided — one at a time,
     * enforced by a partial unique index as well as a check — and 409
     * INPATIENT_STAY_TRANSITION_INVALID means the stay is not AUTHORIZED or ADMITTED.
     * `reasonText` is clinical and comes back only in the clinical projection.
     */
    async extend(
      tenantId: string,
      stayId: string,
      etag: string,
      body: ExtendInpatientStay,
      idempotencyKey: string = randomId(),
      access?: AccessContext,
    ): Promise<Versioned<InpatientStay>> {
      const r = await unwrap(
        client.POST('/api/v1/inpatient-stays/{stayId}/extensions', {
          params: { header: command(tenantId, etag, idempotencyKey, access), path: { stayId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Replaces the stay's whole segment set — where the patient was, hour by hour. An empty
     * array clears it. The first set on an AUTHORIZED stay admits the patient, so a screen
     * calling this is performing the admission and should say so. 409 STAY_SEGMENT_OVERLAP
     * means two segments claim the same hours; COMPANION never causes it.
     */
    async putSegments(
      tenantId: string,
      stayId: string,
      etag: string,
      body: PutStaySegments,
      idempotencyKey: string = randomId(),
      access?: AccessContext,
    ): Promise<Versioned<InpatientStay>> {
      const r = await unwrap(
        client.PUT('/api/v1/inpatient-stays/{stayId}/segments', {
          params: { header: command(tenantId, etag, idempotencyKey, access), path: { stayId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Ends the admission and settles up in one transaction: open segments are ended, the
     * actual days are the elapsed time rounded up and never less than 1, and the reserved
     * days nobody used are released back to the member's entitlement. A stay that ran over
     * releases nothing and comes back with `overAuthorization` true — which the claim will
     * raise as an exception, so the screen should say it here rather than let it surprise
     * somebody later.
     */
    async discharge(
      tenantId: string,
      stayId: string,
      etag: string,
      body: DischargeInpatientStay = {},
      idempotencyKey: string = randomId(),
      access?: AccessContext,
    ): Promise<Versioned<InpatientStay>> {
      const r = await unwrap(
        client.POST('/api/v1/inpatient-stays/{stayId}/discharge', {
          params: { header: command(tenantId, etag, idempotencyKey, access), path: { stayId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Withdraws an admission nobody has finished. Every undecided extension is cancelled
     * with it and everything the authorization still holds is released: an admission that
     * did not happen has held nothing.
     */
    async cancel(
      tenantId: string,
      stayId: string,
      etag: string,
      body: CancelInpatientStay,
      idempotencyKey: string = randomId(),
      access?: AccessContext,
    ): Promise<Versioned<InpatientStay>> {
      const r = await unwrap(
        client.POST('/api/v1/inpatient-stays/{stayId}/cancel', {
          params: { header: command(tenantId, etag, idempotencyKey, access), path: { stayId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * What the discharge settled: authorized days, actual days, released days, and whether
     * the admission ran over. It answers only for a discharged stay — a live one is 409
     * INPATIENT_STAY_NOT_DISCHARGED, because "authorized 5, used nothing" about somebody
     * currently in a bed would be read as a completed settlement. Do not draw a placeholder
     * reconciliation while a stay is running; there is nothing to draw yet.
     */
    async reconciliation(
      tenantId: string,
      stayId: string,
      access?: AccessContext,
    ): Promise<StayReconciliation> {
      return (
        await unwrap(
          client.GET('/api/v1/inpatient-stays/{stayId}/reconciliation', {
            params: { header: read(tenantId, access), path: { stayId } },
          }),
        )
      ).data;
    },
  };
}
