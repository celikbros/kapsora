import type { KapsoraClient } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';

export type EligibilityCheckRequest = components['schemas']['EligibilityCheckRequest'];
export type EligibilityCheckResult = components['schemas']['EligibilityCheckResult'];
export type EligibilityEvaluation = components['schemas']['EligibilityEvaluation'];
export type EligibilityOutcome = EligibilityCheckResult['outcome'];
export type EligibilityExplanation = EligibilityCheckResult['explanations'][number];

/**
 * Eligibility checks. The call changes no business state but records an immutable
 * evaluation; passing the same idempotency key replays that evaluation instead of
 * producing a second one.
 */
export function eligibilityOperations(client: KapsoraClient) {
  return {
    async check(
      tenantId: string,
      body: EligibilityCheckRequest,
      idempotencyKey?: string,
    ): Promise<EligibilityCheckResult> {
      const header: { 'X-Tenant-ID': string; 'Idempotency-Key'?: string } = {
        'X-Tenant-ID': tenantId,
      };
      if (idempotencyKey) header['Idempotency-Key'] = idempotencyKey;
      return (await unwrap(client.POST('/api/v1/eligibility/checks', { params: { header }, body })))
        .data;
    },

    async getEvaluation(tenantId: string, evaluationId: string): Promise<EligibilityEvaluation> {
      return (
        await unwrap(
          client.GET('/api/v1/eligibility/evaluations/{evaluationId}', {
            params: { header: { 'X-Tenant-ID': tenantId }, path: { evaluationId } },
          }),
        )
      ).data;
    },
  };
}
