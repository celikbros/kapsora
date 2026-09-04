import type { KapsoraClient } from './client';
import { randomId } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type RuleSet = components['schemas']['RuleSet'];
export type RuleSetPage = components['schemas']['RuleSetPage'];
export type RuleSetStatus = components['schemas']['RuleSetStatus'];
export type RuleSetPurpose = components['schemas']['RuleSetPurpose'];
export type CreateRuleSetRequest = components['schemas']['CreateRuleSetRequest'];
export type UpdateRuleSetRequest = components['schemas']['UpdateRuleSetRequest'];
export type RuleSetVersion = components['schemas']['RuleSetVersion'];
export type RuleSetVersionSummary = components['schemas']['RuleSetVersionSummary'];
export type RuleSetVersionStatus = components['schemas']['RuleSetVersionStatus'];
export type CreateRuleSetVersionRequest = components['schemas']['CreateRuleSetVersionRequest'];
export type UpdateRuleSetVersionRequest = components['schemas']['UpdateRuleSetVersionRequest'];
export type RuleInputSchema = components['schemas']['RuleInputSchema'];
export type RuleInputType = components['schemas']['RuleInputType'];
export type Rule = components['schemas']['Rule'];
export type RuleInput = components['schemas']['RuleInput'];
export type RuleAction = components['schemas']['RuleAction'];
export type RuleActionType = components['schemas']['RuleActionType'];
export type RuleOutcome = components['schemas']['RuleOutcome'];
export type RuleSeverity = components['schemas']['RuleSeverity'];
export type RuleTestCase = components['schemas']['RuleTestCase'];
export type RuleTestCaseInput = components['schemas']['RuleTestCaseInput'];
export type RuleTestCaseResult = components['schemas']['RuleTestCaseResult'];
export type RuleTestRunResult = components['schemas']['RuleTestRunResult'];
export type RuleEvaluation = components['schemas']['RuleEvaluation'];
export type RuleEvaluationTrace = components['schemas']['RuleEvaluationTrace'];
export type RuleEvaluationResultLine = components['schemas']['RuleEvaluationResultLine'];
export type ReviewComment = components['schemas']['ReviewComment'];
export type ReasonCommand = components['schemas']['ReasonCommand'];
export type ServiceDomain = components['schemas']['ServiceDomain'];

/** Filters of the rule set list. */
export interface RuleSetListQuery {
  cursor?: string;
  limit?: number;
  q?: string;
  domainCode?: ServiceDomain;
  purpose?: RuleSetPurpose;
  status?: RuleSetStatus;
}

/**
 * Rule sets, their versions, the rules and test cases inside a version, and the two
 * read-only ways to try a version out.
 *
 * `runTests` and `simulate` write nothing at all: no evaluation row, no side effect, so an
 * author may run either as often as they like. A version cannot be submitted without at
 * least one test case and without every case passing, and publishing is maker-checker:
 * the submitter cannot be the publisher, and the publisher needs a recent step-up.
 */
export function ruleOperations(client: KapsoraClient) {
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
    async listSets(tenantId: string, query: RuleSetListQuery = {}): Promise<RuleSetPage> {
      const q: RuleSetListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.q) q.q = query.q;
      if (query.domainCode) q.domainCode = query.domainCode;
      if (query.purpose) q.purpose = query.purpose;
      if (query.status) q.status = query.status;
      return (
        await unwrap(
          client.GET('/api/v1/rule-sets', { params: { header: header(tenantId), query: q } }),
        )
      ).data;
    },

    async createSet(
      tenantId: string,
      body: CreateRuleSetRequest,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<RuleSet>> {
      const r = await unwrap(
        client.POST('/api/v1/rule-sets', {
          params: { header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getSet(tenantId: string, ruleSetId: string): Promise<Versioned<RuleSet>> {
      const r = await unwrap(
        client.GET('/api/v1/rule-sets/{ruleSetId}', {
          params: { header: header(tenantId), path: { ruleSetId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchSet(
      tenantId: string,
      ruleSetId: string,
      etag: string,
      patch: UpdateRuleSetRequest,
    ): Promise<Versioned<RuleSet>> {
      const r = await unwrap(
        client.PATCH('/api/v1/rule-sets/{ruleSetId}', {
          params: { header: withEtag(tenantId, etag), path: { ruleSetId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Drafts are omitted for a caller holding only `rule.read`. */
    async listVersions(tenantId: string, ruleSetId: string): Promise<RuleSetVersionSummary[]> {
      return (
        await unwrap(
          client.GET('/api/v1/rule-sets/{ruleSetId}/versions', {
            params: { header: header(tenantId), path: { ruleSetId } },
          }),
        )
      ).data.items;
    },

    async createVersion(
      tenantId: string,
      ruleSetId: string,
      body: CreateRuleSetVersionRequest = {},
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<RuleSetVersion>> {
      const r = await unwrap(
        client.POST('/api/v1/rule-sets/{ruleSetId}/versions', {
          params: {
            header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey },
            path: { ruleSetId },
          },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async getVersion(
      tenantId: string,
      ruleSetVersionId: string,
    ): Promise<Versioned<RuleSetVersion>> {
      const r = await unwrap(
        client.GET('/api/v1/rule-set-versions/{ruleSetVersionId}', {
          params: { header: header(tenantId), path: { ruleSetVersionId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async patchVersion(
      tenantId: string,
      ruleSetVersionId: string,
      etag: string,
      patch: UpdateRuleSetVersionRequest,
    ): Promise<Versioned<RuleSetVersion>> {
      const r = await unwrap(
        client.PATCH('/api/v1/rule-set-versions/{ruleSetVersionId}', {
          params: { header: withEtag(tenantId, etag), path: { ruleSetVersionId } },
          body: patch,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Replaces the whole rule set of a DRAFT version; conditions compile before storing. */
    async replaceRules(
      tenantId: string,
      ruleSetVersionId: string,
      etag: string,
      items: RuleInput[],
    ): Promise<Versioned<Rule[]>> {
      const r = await unwrap(
        client.PUT('/api/v1/rule-set-versions/{ruleSetVersionId}/rules', {
          params: { header: withEtag(tenantId, etag), path: { ruleSetVersionId } },
          body: { items },
        }),
      );
      return versioned(r.data.items, r.response);
    },

    async replaceTestCases(
      tenantId: string,
      ruleSetVersionId: string,
      etag: string,
      items: RuleTestCaseInput[],
    ): Promise<Versioned<RuleTestCase[]>> {
      const r = await unwrap(
        client.PUT('/api/v1/rule-set-versions/{ruleSetVersionId}/test-cases', {
          params: { header: withEtag(tenantId, etag), path: { ruleSetVersionId } },
          body: { items },
        }),
      );
      return versioned(r.data.items, r.response);
    },

    /** Runs the stored cases. Writes nothing. */
    async runTests(tenantId: string, ruleSetVersionId: string): Promise<RuleTestRunResult> {
      return (
        await unwrap(
          client.POST('/api/v1/rule-set-versions/{ruleSetVersionId}/tests:run', {
            params: { header: header(tenantId), path: { ruleSetVersionId } },
          }),
        )
      ).data;
    },

    /** Runs one supplied input and returns the full trace. Writes nothing. */
    async simulate(
      tenantId: string,
      ruleSetVersionId: string,
      input: Record<string, unknown>,
    ): Promise<RuleEvaluationTrace> {
      return (
        await unwrap(
          client.POST('/api/v1/rule-set-versions/{ruleSetVersionId}:simulate', {
            params: { header: header(tenantId), path: { ruleSetVersionId } },
            body: { input },
          }),
        )
      ).data;
    },

    /** Refused with 422 on `testCases` when there is no case, or when one fails. */
    async submitVersion(
      tenantId: string,
      ruleSetVersionId: string,
      etag: string,
      body: ReviewComment = {},
    ): Promise<Versioned<RuleSetVersion>> {
      const r = await unwrap(
        client.POST('/api/v1/rule-set-versions/{ruleSetVersionId}/submit', {
          params: { header: withEtag(tenantId, etag), path: { ruleSetVersionId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Needs a recent step-up and a different actor than the submitter. */
    async publishVersion(
      tenantId: string,
      ruleSetVersionId: string,
      etag: string,
      body: ReviewComment = {},
    ): Promise<Versioned<RuleSetVersion>> {
      const r = await unwrap(
        client.POST('/api/v1/rule-set-versions/{ruleSetVersionId}/publish', {
          params: { header: withEtag(tenantId, etag), path: { ruleSetVersionId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Needs a recent step-up; the version stays readable for every decision it produced. */
    async retireVersion(
      tenantId: string,
      ruleSetVersionId: string,
      etag: string,
      body: ReasonCommand,
    ): Promise<Versioned<RuleSetVersion>> {
      const r = await unwrap(
        client.POST('/api/v1/rule-set-versions/{ruleSetVersionId}/retire', {
          params: { header: withEtag(tenantId, etag), path: { ruleSetVersionId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** One decision that really was recorded; the row is append-only. */
    async getEvaluation(tenantId: string, ruleEvaluationId: string): Promise<RuleEvaluation> {
      return (
        await unwrap(
          client.GET('/api/v1/rule-evaluations/{ruleEvaluationId}', {
            params: { header: header(tenantId), path: { ruleEvaluationId } },
          }),
        )
      ).data;
    },
  };
}
