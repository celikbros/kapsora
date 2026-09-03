import type { KapsoraClient } from './client';
import {
  asDecimals,
  type EligibilityCheckRequest as CheckBody,
  type EligibilityCheckResult as CheckResult,
  type EligibilityEvaluation as Evaluation,
} from './decimals';
import { unwrap } from './problem';

// Quantities in the request and the result are decimal strings; see decimals.ts.
export type {
  EligibilityBalance,
  EligibilityCheckRequest,
  EligibilityCheckResult,
  EligibilityEvaluation,
  EligibilityItemResult,
  EligibilityServiceItem,
} from './decimals';
export type EligibilityOutcome = CheckResult['outcome'];
export type EligibilityExplanation = CheckResult['explanations'][number];

/**
 * Eligibility checks. The call changes no business state but records an immutable
 * evaluation; passing the same idempotency key replays that evaluation instead of
 * producing a second one.
 */
export function eligibilityOperations(client: KapsoraClient) {
  return {
    async check(tenantId: string, body: CheckBody, idempotencyKey?: string): Promise<CheckResult> {
      const header: { 'X-Tenant-ID': string; 'Idempotency-Key'?: string } = {
        'X-Tenant-ID': tenantId,
      };
      if (idempotencyKey) header['Idempotency-Key'] = idempotencyKey;
      const r = await unwrap(
        client.POST('/api/v1/eligibility/checks', {
          params: { header },
          body: asDecimals<never>(body),
        }),
      );
      return asDecimals<CheckResult>(r.data);
    },

    async getEvaluation(tenantId: string, evaluationId: string): Promise<Evaluation> {
      const r = await unwrap(
        client.GET('/api/v1/eligibility/evaluations/{evaluationId}', {
          params: { header: { 'X-Tenant-ID': tenantId }, path: { evaluationId } },
        }),
      );
      return asDecimals<Evaluation>(r.data);
    },
  };
}
