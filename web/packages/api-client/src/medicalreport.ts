import { randomId, type KapsoraClient } from './client';
import type { components } from './generated/kapsora-v1';
import { accessHeaders, type AccessContext } from './health';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type MedicalReport = components['schemas']['MedicalReport'];
export type MedicalReportPage = components['schemas']['MedicalReportPage'];
export type MedicalReportStatus = components['schemas']['MedicalReportStatus'];
export type MedicalReportService = components['schemas']['MedicalReportService'];
export type MedicalReportServiceInput = components['schemas']['MedicalReportServiceInput'];
export type MedicalReportDocument = components['schemas']['MedicalReportDocument'];
export type MedicalReportUsage = components['schemas']['MedicalReportUsage'];
export type MedicalReportUsagePage = components['schemas']['MedicalReportUsagePage'];
export type MedicalReportUsedByType = components['schemas']['MedicalReportUsedByType'];
export type CreateMedicalReport = components['schemas']['CreateMedicalReport'];
export type PatchMedicalReportDraft = components['schemas']['PatchMedicalReportDraft'];
export type PutMedicalReportServices = components['schemas']['PutMedicalReportServices'];
export type DecideMedicalReport = components['schemas']['DecideMedicalReport'];
export type RejectMedicalReport = components['schemas']['RejectMedicalReport'];

/** Filters of the treatment report list. */
export interface MedicalReportListQuery {
  cursor?: string;
  limit?: number;
  personId?: string;
  caseId?: string;
  /** Every version of one report chain; this is how a screen draws "sürüm 2 / 2". */
  rootReportId?: string;
  providerOrganizationId?: string;
  status?: MedicalReportStatus;
  reportType?: string;
  /** Keep only the reports whose validity covers this day, both bounds inclusive. */
  validOn?: string;
}

/** Paging of one report version's usage trail. */
export interface MedicalReportUsageQuery {
  cursor?: string;
  limit?: number;
}

/**
 * Treatment reports: the document a doctor signs, the decision a reviewer gives on it, and
 * the trail of everything that later leaned on it.
 *
 * A report is frozen the moment it leaves DRAFT. `patchDraft` and `putServices` answer 409
 * MEDICAL_REPORT_IMMUTABLE on anything else, and that is not a state to work around: an
 * approved report is what the reviewer saw and a claim is leaning on it. A correction is
 * `create` with `supersedesReportId`, which opens version n+1 of the same chain, inherits
 * the reference — so a member quoting "MR-2026…" is still quoting the same report —
 * and copies the lines forward. The version being corrected keeps its decision and stays
 * readable, so a screen showing a correction owes the reader both.
 *
 * Amounts and quantities on a service line are exact decimal strings. Pass them through
 * untouched: parsing one into a JavaScript number is how a covered limit two systems
 * disagree about gets created.
 */
export function medicalReportOperations(client: KapsoraClient) {
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
     * Reports newest first. Like every M5 list this one narrows rather than refuses: a row
     * on a sensitive case arrives in the financial projection instead of a 428, and
     * `projection` on each row says which half was served.
     */
    async list(
      tenantId: string,
      query: MedicalReportListQuery = {},
      access?: AccessContext,
    ): Promise<MedicalReportPage> {
      const q: MedicalReportListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.personId) q.personId = query.personId;
      if (query.caseId) q.caseId = query.caseId;
      if (query.rootReportId) q.rootReportId = query.rootReportId;
      if (query.providerOrganizationId) q.providerOrganizationId = query.providerOrganizationId;
      if (query.status) q.status = query.status;
      if (query.reportType) q.reportType = query.reportType;
      if (query.validOn) q.validOn = query.validOn;
      return (
        await unwrap(
          client.GET('/api/v1/medical-reports', {
            params: { header: read(tenantId, access), query: q },
          }),
        )
      ).data;
    },

    /**
     * Writes a draft: the first version of a new chain, or — with `supersedesReportId` —
     * a correction of a decided one, which inherits every header field it does not send
     * along with the service lines of the version it corrects.
     */
    async create(
      tenantId: string,
      body: CreateMedicalReport,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<MedicalReport>> {
      const r = await unwrap(
        client.POST('/api/v1/medical-reports', {
          params: { header: create(tenantId, idempotencyKey) },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * One report with its service lines and, in the clinical projection, its attachments.
     * This is the read that demands a purpose: a report on a sensitive case without one is
     * 428, recorded as a DENIED access event against MEDICAL_REPORT.
     */
    async get(
      tenantId: string,
      reportId: string,
      access?: AccessContext,
    ): Promise<Versioned<MedicalReport>> {
      const r = await unwrap(
        client.GET('/api/v1/medical-reports/{reportId}', {
          params: { header: read(tenantId, access), path: { reportId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Rewrites the draft's header. Every field goes every time — this is a full
     * replacement and not a merge patch, because a merge would make "clear the subtype"
     * unexpressible, so a form must submit what it read even where the user changed
     * nothing.
     */
    async patchDraft(
      tenantId: string,
      reportId: string,
      etag: string,
      body: PatchMedicalReportDraft,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<MedicalReport>> {
      const r = await unwrap(
        client.PATCH('/api/v1/medical-reports/{reportId}', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { reportId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Replaces the report's service set as a whole; an empty array clears it. It moves the
     * report's ETag, so a caller still holding the old one is holding a report that no
     * longer says what it said.
     */
    async putServices(
      tenantId: string,
      reportId: string,
      etag: string,
      body: PutMedicalReportServices,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<MedicalReport>> {
      const r = await unwrap(
        client.PUT('/api/v1/medical-reports/{reportId}/services', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { reportId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Hands the draft to the medical reviewers and raises the MEDICAL_REVIEW work item in
     * the same transaction. Two 422s are worth naming to the user rather than showing as
     * "geçersiz": MEDICAL_REPORT_SERVICE_REQUIRED (no service line) and
     * MEDICAL_REPORT_DOCUMENT_REQUIRED (no scanned-clean attachment of the report's own
     * type — a file still in quarantine is not one a reviewer can open).
     */
    async submit(
      tenantId: string,
      reportId: string,
      etag: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<MedicalReport>> {
      const r = await unwrap(
        client.POST('/api/v1/medical-reports/{reportId}/submit', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { reportId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * A reviewer has picked the report up. Claiming the report's work item raises the same
     * transition, so a reviewer working from the queue never has to give this command.
     */
    async startReview(
      tenantId: string,
      reportId: string,
      etag: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<MedicalReport>> {
      const r = await unwrap(
        client.POST('/api/v1/medical-reports/{reportId}/start-review', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { reportId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * The decision claims and authorizations lean on. Approving a correction moves the
     * version it corrects out of APPROVED in the same transaction, so a screen holding the
     * older version has to re-read it before showing anybody its status.
     */
    async approve(
      tenantId: string,
      reportId: string,
      etag: string,
      body: DecideMedicalReport = {},
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<MedicalReport>> {
      const r = await unwrap(
        client.POST('/api/v1/medical-reports/{reportId}/approve', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { reportId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * The other half of the same decision, and it says why in a code a report can group
     * by. The comment beside it is clinical text and is served only in the clinical
     * projection, so a rejection shown to a provider may carry the code and not always
     * the sentence.
     */
    async reject(
      tenantId: string,
      reportId: string,
      etag: string,
      body: RejectMedicalReport,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<MedicalReport>> {
      const r = await unwrap(
        client.POST('/api/v1/medical-reports/{reportId}/reject', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { reportId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Takes back a report nobody has started reading. Once a reviewer has picked it up it
     * is theirs to decide and an UNDER_REVIEW report answers 409 — offer the provider the
     * reviewer's decision to wait for, not a retry.
     */
    async cancel(
      tenantId: string,
      reportId: string,
      etag: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<MedicalReport>> {
      const r = await unwrap(
        client.POST('/api/v1/medical-reports/{reportId}/cancel', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { reportId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Which service request, authorization or claim leaned on *this version* of the report,
     * and when. It names the version rather than the chain: a claim settled against version
     * 1 was settled against version 1, whatever version 3 later says. A usage row is three
     * ids and a moment, carries nothing clinical, and is served whole to anybody who may
     * read the report at all.
     */
    async listUsages(
      tenantId: string,
      reportId: string,
      query: MedicalReportUsageQuery = {},
    ): Promise<MedicalReportUsagePage> {
      const q: MedicalReportUsageQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      return (
        await unwrap(
          client.GET('/api/v1/medical-reports/{reportId}/usages', {
            params: { header: header(tenantId), path: { reportId }, query: q },
          }),
        )
      ).data;
    },
  };
}
