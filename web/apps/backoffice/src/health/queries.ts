import type { AccessContext, Diagnosis } from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useQuery } from '@tanstack/react-query';

import { useOps } from '../api';

/**
 * Every diagnosis on a case, through its encounters, so a claim line's `diagnosisId` can be
 * shown as a code and a name rather than an id. Read only on the clinical projection and
 * with the operator's access context, because the diagnoses of a sensitive case answer 403
 * or 428 to anything less.
 */
export function useCaseDiagnoses(caseId: string, access: AccessContext | undefined) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['health', tenantId, 'case-diagnoses', caseId, access ?? null],
    queryFn: async (): Promise<Diagnosis[]> => {
      const record = await ops.health.getCase(tenantId, caseId, access);
      const lists = await Promise.all(
        record.data.encounters.map((e) => ops.health.listDiagnoses(tenantId, e.id, access)),
      );
      return lists.flat();
    },
    enabled: caseId !== '',
    retry: false,
  });
}
