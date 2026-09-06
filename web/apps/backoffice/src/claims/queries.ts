import type {
  AccessContext,
  ClaimDecisionReason,
  ClaimListQuery,
  ClaimReturnReason,
  DecideClaimLines,
  DecideMedicalReport,
  HealthAccessLogQuery,
  MedicalReportListQuery,
  RejectMedicalReport,
} from '@kapsora/api-client';
import { etagOf } from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

/**
 * The review desk's reads and commands. Every read of a claim or a report carries the
 * access context the operator is in — a stated purpose, a decline, or nothing yet — and a
 * 428 is not retried: it is the server asking a question the screen puts to the operator.
 */

export function useClaims(query: ClaimListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['claims', tenantId, 'list', query],
    queryFn: () => ops.claims.list(tenantId, query),
  });
}

export function useClaim(claimId: string, access: AccessContext | undefined) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['claims', tenantId, 'claim', claimId, access ?? null],
    queryFn: () => ops.claims.get(tenantId, claimId, access),
    retry: false,
  });
}

export function useClaimVersion(
  claimId: string,
  versionNo: number | null,
  access: AccessContext | undefined,
) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['claims', tenantId, 'claim', claimId, 'version', versionNo ?? 0, access ?? null],
    queryFn: () => ops.claims.getVersion(tenantId, claimId, versionNo!, access),
    enabled: versionNo !== null && versionNo > 0,
    retry: false,
  });
}

export function useClaimVersions(claimId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['claims', tenantId, 'claim', claimId, 'versions'],
    queryFn: () => ops.claims.listVersions(tenantId, claimId),
  });
}

export function useReadiness(claimId: string, enabled: boolean) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['claims', tenantId, 'claim', claimId, 'readiness'],
    queryFn: () => ops.claims.readiness(tenantId, claimId),
    enabled,
    retry: false,
  });
}

function useInvalidateClaim(claimId: string) {
  const client = useQueryClient();
  const tenantId = useTenantId();
  return () =>
    Promise.all([
      client.invalidateQueries({ queryKey: ['claims', tenantId, 'claim', claimId] }),
      client.invalidateQueries({ queryKey: ['claims', tenantId, 'list'] }),
      client.invalidateQueries({ queryKey: ['worklist'] }),
    ]);
}

export function useClaimCommands(claimId: string, access: AccessContext | undefined) {
  const ops = useOps();
  const tenantId = useTenantId();
  const done = useInvalidateClaim(claimId);
  return {
    decide: useMutation({
      mutationFn: (input: { rowVersion: number; body: DecideClaimLines }) =>
        ops.claims.decideLines(
          tenantId,
          claimId,
          etagOf(input.rowVersion),
          input.body,
          undefined,
          access,
        ),
      onSuccess: done,
    }),
    approve: useMutation({
      mutationFn: (input: { rowVersion: number; body: ClaimDecisionReason }) =>
        ops.claims.approve(tenantId, claimId, etagOf(input.rowVersion), input.body),
      onSuccess: done,
    }),
    reject: useMutation({
      mutationFn: (input: { rowVersion: number; body: ClaimDecisionReason }) =>
        ops.claims.reject(tenantId, claimId, etagOf(input.rowVersion), input.body),
      onSuccess: done,
    }),
    returnForCorrection: useMutation({
      mutationFn: (input: { rowVersion: number; body: ClaimReturnReason }) =>
        ops.claims.returnForCorrection(tenantId, claimId, etagOf(input.rowVersion), input.body),
      onSuccess: done,
    }),
  };
}

// --- reports under review ------------------------------------------------------------------

export function useReports(query: MedicalReportListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['reports', tenantId, 'list', query],
    queryFn: () => ops.reports.list(tenantId, query),
  });
}

export function useReport(reportId: string, access: AccessContext | undefined) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['reports', tenantId, 'report', reportId, access ?? null],
    queryFn: () => ops.reports.get(tenantId, reportId, access),
    retry: false,
  });
}

export function useReportCommands(reportId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  const done = () =>
    Promise.all([
      client.invalidateQueries({ queryKey: ['reports', tenantId] }),
      client.invalidateQueries({ queryKey: ['worklist'] }),
    ]);
  return {
    startReview: useMutation({
      mutationFn: (input: { rowVersion: number }) =>
        ops.reports.startReview(tenantId, reportId, etagOf(input.rowVersion)),
      onSuccess: done,
    }),
    approve: useMutation({
      mutationFn: (input: { rowVersion: number; body: DecideMedicalReport }) =>
        ops.reports.approve(tenantId, reportId, etagOf(input.rowVersion), input.body),
      onSuccess: done,
    }),
    reject: useMutation({
      mutationFn: (input: { rowVersion: number; body: RejectMedicalReport }) =>
        ops.reports.reject(tenantId, reportId, etagOf(input.rowVersion), input.body),
      onSuccess: done,
    }),
  };
}

// --- a person's health, as the sponsor's HR and the auditor see it ---------------------------

export function useCasesOfPerson(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['health', tenantId, 'cases', 'person', personId],
    queryFn: () => ops.health.listCases(tenantId, { personId, limit: 50 }),
  });
}

export function useClaimsOfPerson(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['claims', tenantId, 'list', { personId, limit: 50 }],
    queryFn: () => ops.claims.list(tenantId, { personId, limit: 50 }),
  });
}

export function useAccessLog(query: HealthAccessLogQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['health', tenantId, 'access-log', query],
    queryFn: () => ops.health.listAccessLog(tenantId, query),
  });
}
