import type { ClaimStatus, CreateClaim, PatchClaimDraft, PutClaimLines } from '@kapsora/api-client';
import { etagOf } from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../services';

/**
 * The provider's claims. Money on every record is the server's decimal string and is
 * rendered as it arrives: a draft carries only what the provider asked, and the priced,
 * approved, payer and member figures exist from the moment the server decided them and
 * not before.
 */

export function useClaims(status: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'claims', status],
    queryFn: () => ops.claims.list(tenantId, status ? { status: status as ClaimStatus } : {}),
  });
}

export function useClaimsOfCase(caseId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'claims', 'case', caseId],
    queryFn: () => ops.claims.list(tenantId, { caseId, limit: 50 }),
    enabled: caseId !== '',
  });
}

export function useClaim(claimId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'claim', claimId],
    queryFn: () => ops.claims.get(tenantId, claimId),
  });
}

/** One earlier version, read to stand beside the draft that corrects it. */
export function useClaimVersion(claimId: string, versionNo: number | null) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'claim', claimId, 'version', versionNo ?? 0],
    queryFn: () => ops.claims.getVersion(tenantId, claimId, versionNo!),
    enabled: versionNo !== null && versionNo > 0,
  });
}

export function useClaimVersions(claimId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'claim', claimId, 'versions'],
    queryFn: () => ops.claims.listVersions(tenantId, claimId),
  });
}

export function useReadiness(claimId: string, enabled: boolean) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ['provider', tenantId, 'claim', claimId, 'readiness'],
    queryFn: () => ops.claims.readiness(tenantId, claimId),
    enabled,
    retry: false,
  });
}

function useInvalidateClaims() {
  const client = useQueryClient();
  const tenantId = useTenantId();
  return (claimId?: string) =>
    Promise.all([
      client.invalidateQueries({ queryKey: ['provider', tenantId, 'claims'] }),
      claimId
        ? client.invalidateQueries({ queryKey: ['provider', tenantId, 'claim', claimId] })
        : Promise.resolve(),
    ]);
}

export function useCreateClaim() {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateClaims();
  return useMutation({
    mutationFn: (body: CreateClaim) => ops.claims.create(tenantId, body),
    onSuccess: () => invalidate(),
  });
}

export function useClaimCommands(claimId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const invalidate = useInvalidateClaims();
  const done = () => invalidate(claimId);
  return {
    patch: useMutation({
      mutationFn: (input: { rowVersion: number; body: PatchClaimDraft }) =>
        ops.claims.patchDraft(tenantId, claimId, etagOf(input.rowVersion), input.body),
      onSuccess: done,
    }),
    putLines: useMutation({
      mutationFn: (input: { rowVersion: number; body: PutClaimLines }) =>
        ops.claims.putLines(tenantId, claimId, etagOf(input.rowVersion), input.body),
      onSuccess: done,
    }),
    submit: useMutation({
      mutationFn: (input: { rowVersion: number }) =>
        ops.claims.submit(tenantId, claimId, etagOf(input.rowVersion)),
      onSuccess: done,
    }),
    cancel: useMutation({
      mutationFn: (input: { rowVersion: number; reasonCode: string; reasonText?: string }) =>
        ops.claims.cancel(tenantId, claimId, etagOf(input.rowVersion), {
          reasonCode: input.reasonCode,
          ...(input.reasonText ? { reasonText: input.reasonText } : {}),
        }),
      onSuccess: done,
    }),
  };
}
