import type { KapsoraClient } from './client';
import { randomId } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import {
  asDecimals,
  type EntitlementDefinitionInput as DefinitionInput,
  type PlanVersion as Version,
} from './decimals';
import { versioned, type Versioned } from './versioned';

export type Program = components['schemas']['Program'];
export type ProgramPage = components['schemas']['ProgramPage'];
export type CreateProgramRequest = components['schemas']['CreateProgramRequest'];
export type UpdateProgramRequest = components['schemas']['UpdateProgramRequest'];
export type Plan = components['schemas']['Plan'];
export type CreatePlanRequest = components['schemas']['CreatePlanRequest'];
export type UpdatePlanRequest = components['schemas']['UpdatePlanRequest'];
export type { EntitlementDefinition, EntitlementDefinitionInput, PlanVersion } from './decimals';
export type PlanVersionSummary = components['schemas']['PlanVersionSummary'];
export type CreatePlanVersionRequest = components['schemas']['CreatePlanVersionRequest'];
export type UpdatePlanVersionRequest = components['schemas']['UpdatePlanVersionRequest'];
export type ReviewComment = components['schemas']['ReviewComment'];
export type ReasonCommand = components['schemas']['ReasonCommand'];
export type Enrollment = components['schemas']['Enrollment'];
export type EnrollmentPage = components['schemas']['EnrollmentPage'];
export type CreateEnrollmentRequest = components['schemas']['CreateEnrollmentRequest'];
export type UpdateEnrollmentRequest = components['schemas']['UpdateEnrollmentRequest'];
export type ProgramStatus = Program['status'];
export type EnrollmentStatus = Enrollment['status'];

/** Filters of the program list. */
export interface ProgramListQuery {
  cursor?: string;
  limit?: number;
  q?: string;
  status?: ProgramStatus;
}

/** Filters of the tenant-wide enrollment list. */
export interface EnrollmentListQuery {
  cursor?: string;
  limit?: number;
  planId?: string;
  status?: EnrollmentStatus;
}

/**
 * Programs, plans, plan versions with their entitlement definitions, and enrollments.
 * Publishing is a maker-checker step: the submitter cannot be the publisher.
 */
export function benefitOperations(client: KapsoraClient) {
  const header = (tenantId: string) => ({ 'X-Tenant-ID': tenantId });
  const withEtag = (tenantId: string, etag: string) => ({
    'X-Tenant-ID': tenantId,
    'If-Match': etag,
  });
  const mergePatch = {
    headers: { 'Content-Type': 'application/merge-patch+json' },
    bodySerializer: (b: unknown) => JSON.stringify(b),
  };

  return {
    async listPrograms(tenantId: string, query: ProgramListQuery = {}): Promise<ProgramPage> {
      const q: ProgramListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.q) q.q = query.q;
      if (query.status) q.status = query.status;
      return (
        await unwrap(
          client.GET('/api/v1/programs', { params: { header: header(tenantId), query: q } }),
        )
      ).data;
    },

    async createProgram(
      tenantId: string,
      body: CreateProgramRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Program>> {
      const r = await unwrap(
        client.POST('/api/v1/programs', {
          params: { header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getProgram(tenantId: string, programId: string): Promise<Versioned<Program>> {
      const r = await unwrap(
        client.GET('/api/v1/programs/{programId}', {
          params: { header: header(tenantId), path: { programId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchProgram(
      tenantId: string,
      programId: string,
      etag: string,
      patch: UpdateProgramRequest,
    ): Promise<Versioned<Program>> {
      const r = await unwrap(
        client.PATCH('/api/v1/programs/{programId}', {
          params: { header: withEtag(tenantId, etag), path: { programId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    async listPlans(tenantId: string, programId: string): Promise<Plan[]> {
      return (
        await unwrap(
          client.GET('/api/v1/programs/{programId}/plans', {
            params: { header: header(tenantId), path: { programId } },
          }),
        )
      ).data.items;
    },

    async createPlan(
      tenantId: string,
      programId: string,
      body: CreatePlanRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Plan>> {
      const r = await unwrap(
        client.POST('/api/v1/programs/{programId}/plans', {
          params: {
            header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey },
            path: { programId },
          },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getPlan(tenantId: string, planId: string): Promise<Versioned<Plan>> {
      const r = await unwrap(
        client.GET('/api/v1/plans/{planId}', {
          params: { header: header(tenantId), path: { planId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchPlan(
      tenantId: string,
      planId: string,
      etag: string,
      patch: UpdatePlanRequest,
    ): Promise<Versioned<Plan>> {
      const r = await unwrap(
        client.PATCH('/api/v1/plans/{planId}', {
          params: { header: withEtag(tenantId, etag), path: { planId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    async listPlanVersions(tenantId: string, planId: string): Promise<PlanVersionSummary[]> {
      return (
        await unwrap(
          client.GET('/api/v1/plans/{planId}/versions', {
            params: { header: header(tenantId), path: { planId } },
          }),
        )
      ).data.items;
    },

    async createPlanVersion(
      tenantId: string,
      planId: string,
      body: CreatePlanVersionRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Version>> {
      const r = await unwrap(
        client.POST('/api/v1/plans/{planId}/versions', {
          params: {
            header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey },
            path: { planId },
          },
          body,
        }),
      );
      return versioned(asDecimals<Version>(r.data), r.response);
    },

    async getPlanVersion(tenantId: string, planVersionId: string): Promise<Versioned<Version>> {
      const r = await unwrap(
        client.GET('/api/v1/plan-versions/{planVersionId}', {
          params: { header: header(tenantId), path: { planVersionId } },
        }),
      );
      return versioned(asDecimals<Version>(r.data), r.response);
    },

    async patchPlanVersion(
      tenantId: string,
      planVersionId: string,
      etag: string,
      patch: UpdatePlanVersionRequest,
    ): Promise<Versioned<Version>> {
      const r = await unwrap(
        client.PATCH('/api/v1/plan-versions/{planVersionId}', {
          params: { header: withEtag(tenantId, etag), path: { planVersionId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(asDecimals<Version>(r.data), r.response);
    },

    /** Replaces the whole definition list of a draft version. */
    async replaceEntitlementDefinitions(
      tenantId: string,
      planVersionId: string,
      etag: string,
      items: DefinitionInput[],
    ): Promise<Versioned<Version>> {
      const r = await unwrap(
        client.PUT('/api/v1/plan-versions/{planVersionId}/entitlement-definitions', {
          params: { header: withEtag(tenantId, etag), path: { planVersionId } },
          body: { items: asDecimals<never>(items) },
        }),
      );
      return versioned(asDecimals<Version>(r.data), r.response);
    },

    async submitPlanVersion(
      tenantId: string,
      planVersionId: string,
      etag: string,
      body: ReviewComment = {},
    ): Promise<Versioned<Version>> {
      const r = await unwrap(
        client.POST('/api/v1/plan-versions/{planVersionId}/submit', {
          params: { header: withEtag(tenantId, etag), path: { planVersionId } },
          body,
        }),
      );
      return versioned(asDecimals<Version>(r.data), r.response);
    },

    /** Needs a recent step-up and a different actor than the submitter. */
    async publishPlanVersion(
      tenantId: string,
      planVersionId: string,
      etag: string,
      body: ReviewComment = {},
    ): Promise<Versioned<Version>> {
      const r = await unwrap(
        client.POST('/api/v1/plan-versions/{planVersionId}/publish', {
          params: { header: withEtag(tenantId, etag), path: { planVersionId } },
          body,
        }),
      );
      return versioned(asDecimals<Version>(r.data), r.response);
    },

    async retirePlanVersion(
      tenantId: string,
      planVersionId: string,
      etag: string,
      body: ReasonCommand,
    ): Promise<Versioned<Version>> {
      const r = await unwrap(
        client.POST('/api/v1/plan-versions/{planVersionId}/retire', {
          params: { header: withEtag(tenantId, etag), path: { planVersionId } },
          body,
        }),
      );
      return versioned(asDecimals<Version>(r.data), r.response);
    },

    async listPersonEnrollments(tenantId: string, personId: string): Promise<Enrollment[]> {
      return (
        await unwrap(
          client.GET('/api/v1/people/{personId}/enrollments', {
            params: { header: header(tenantId), path: { personId } },
          }),
        )
      ).data.items;
    },

    async createEnrollment(
      tenantId: string,
      personId: string,
      body: CreateEnrollmentRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<Enrollment>> {
      const r = await unwrap(
        client.POST('/api/v1/people/{personId}/enrollments', {
          params: {
            header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey },
            path: { personId },
          },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async listEnrollments(
      tenantId: string,
      query: EnrollmentListQuery = {},
    ): Promise<EnrollmentPage> {
      const q: EnrollmentListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.planId) q.planId = query.planId;
      if (query.status) q.status = query.status;
      return (
        await unwrap(
          client.GET('/api/v1/enrollments', { params: { header: header(tenantId), query: q } }),
        )
      ).data;
    },

    async getEnrollment(tenantId: string, enrollmentId: string): Promise<Versioned<Enrollment>> {
      const r = await unwrap(
        client.GET('/api/v1/enrollments/{enrollmentId}', {
          params: { header: header(tenantId), path: { enrollmentId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchEnrollment(
      tenantId: string,
      enrollmentId: string,
      etag: string,
      patch: UpdateEnrollmentRequest,
    ): Promise<Versioned<Enrollment>> {
      const r = await unwrap(
        client.PATCH('/api/v1/enrollments/{enrollmentId}', {
          params: { header: withEtag(tenantId, etag), path: { enrollmentId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },
  };
}
