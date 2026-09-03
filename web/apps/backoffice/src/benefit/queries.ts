import type {
  CreateAdjustmentRequest,
  CreateEnrollmentRequest,
  CreatePlanRequest,
  CreatePlanVersionRequest,
  CreateProgramRequest,
  EligibilityCheckRequest,
  EntitlementDefinitionInput,
  LedgerQuery,
  ProgramListQuery,
  ReasonCommand,
  ReviewComment,
  UpdateEnrollmentRequest,
  UpdatePlanRequest,
  UpdatePlanVersionRequest,
  UpdateProgramRequest,
} from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

export const benefitKeys = {
  all: (tenantId: string) => ['benefit', tenantId] as const,
  programs: (tenantId: string, query: ProgramListQuery) =>
    ['benefit', tenantId, 'programs', query] as const,
  program: (tenantId: string, id: string) => ['benefit', tenantId, 'program', id] as const,
  plans: (tenantId: string, programId: string) =>
    ['benefit', tenantId, 'plans', programId] as const,
  plan: (tenantId: string, id: string) => ['benefit', tenantId, 'plan', id] as const,
  versions: (tenantId: string, planId: string) =>
    ['benefit', tenantId, 'versions', planId] as const,
  version: (tenantId: string, id: string) => ['benefit', tenantId, 'version', id] as const,
  enrollments: (tenantId: string, personId: string) =>
    ['benefit', tenantId, 'enrollments', personId] as const,
  entitlements: (tenantId: string, personId: string, asOf: string) =>
    ['benefit', tenantId, 'entitlements', personId, asOf] as const,
  ledger: (tenantId: string, accountId: string, query: LedgerQuery) =>
    ['benefit', tenantId, 'ledger', accountId, query] as const,
  adjustments: (tenantId: string, status: string) =>
    ['benefit', tenantId, 'adjustments', status] as const,
};

export function usePrograms(query: ProgramListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: benefitKeys.programs(tenantId, query),
    queryFn: () => ops.benefit.listPrograms(tenantId, query),
    placeholderData: (previous) => previous,
  });
}

export function useProgram(programId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: benefitKeys.program(tenantId, programId),
    queryFn: () => ops.benefit.getProgram(tenantId, programId),
  });
}

export function useCreateProgram() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateProgramRequest; idempotencyKey: string }) =>
      ops.benefit.createProgram(tenantId, input.body, input.idempotencyKey),
    onSuccess: () => void qc.invalidateQueries({ queryKey: benefitKeys.all(tenantId) }),
  });
}

export function useUpdateProgram(programId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { etag: string; patch: UpdateProgramRequest }) =>
      ops.benefit.patchProgram(tenantId, programId, input.etag, input.patch),
    onSuccess: (result) => {
      qc.setQueryData(benefitKeys.program(tenantId, programId), result);
      void qc.invalidateQueries({ queryKey: benefitKeys.all(tenantId) });
    },
  });
}

export function usePlans(programId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: benefitKeys.plans(tenantId, programId),
    queryFn: () => ops.benefit.listPlans(tenantId, programId),
  });
}

export function usePlan(planId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: benefitKeys.plan(tenantId, planId),
    queryFn: () => ops.benefit.getPlan(tenantId, planId),
  });
}

export function useCreatePlan(programId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreatePlanRequest; idempotencyKey: string }) =>
      ops.benefit.createPlan(tenantId, programId, input.body, input.idempotencyKey),
    onSuccess: () => void qc.invalidateQueries({ queryKey: benefitKeys.all(tenantId) }),
  });
}

export function useUpdatePlan(planId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { etag: string; patch: UpdatePlanRequest }) =>
      ops.benefit.patchPlan(tenantId, planId, input.etag, input.patch),
    onSuccess: () => void qc.invalidateQueries({ queryKey: benefitKeys.all(tenantId) }),
  });
}

export function usePlanVersion(planVersionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: benefitKeys.version(tenantId, planVersionId),
    queryFn: () => ops.benefit.getPlanVersion(tenantId, planVersionId),
  });
}

export function useCreatePlanVersion(planId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreatePlanVersionRequest; idempotencyKey: string }) =>
      ops.benefit.createPlanVersion(tenantId, planId, input.body, input.idempotencyKey),
    onSuccess: () => void qc.invalidateQueries({ queryKey: benefitKeys.all(tenantId) }),
  });
}

/** The four commands of a plan version's life; each carries the ETag it saw. */
export function usePlanVersionCommands(planVersionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  const invalidate = () => void qc.invalidateQueries({ queryKey: benefitKeys.all(tenantId) });

  const definitions = useMutation({
    mutationFn: (input: { etag: string; items: EntitlementDefinitionInput[] }) =>
      ops.benefit.replaceEntitlementDefinitions(tenantId, planVersionId, input.etag, input.items),
    onSuccess: invalidate,
  });
  const patch = useMutation({
    mutationFn: (input: { etag: string; patch: UpdatePlanVersionRequest }) =>
      ops.benefit.patchPlanVersion(tenantId, planVersionId, input.etag, input.patch),
    onSuccess: invalidate,
  });
  const submit = useMutation({
    mutationFn: (input: { etag: string; body?: ReviewComment }) =>
      ops.benefit.submitPlanVersion(tenantId, planVersionId, input.etag, input.body ?? {}),
    onSuccess: invalidate,
  });
  const publish = useMutation({
    mutationFn: (input: { etag: string; body?: ReviewComment }) =>
      ops.benefit.publishPlanVersion(tenantId, planVersionId, input.etag, input.body ?? {}),
    onSuccess: invalidate,
  });
  const retire = useMutation({
    mutationFn: (input: { etag: string; body: ReasonCommand }) =>
      ops.benefit.retirePlanVersion(tenantId, planVersionId, input.etag, input.body),
    onSuccess: invalidate,
  });
  return { definitions, patch, submit, publish, retire };
}

export function usePersonEnrollments(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: benefitKeys.enrollments(tenantId, personId),
    queryFn: () => ops.benefit.listPersonEnrollments(tenantId, personId),
  });
}

export function useCreateEnrollment(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateEnrollmentRequest; idempotencyKey: string }) =>
      ops.benefit.createEnrollment(tenantId, personId, input.body, input.idempotencyKey),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: benefitKeys.enrollments(tenantId, personId) });
    },
  });
}

export function useUpdateEnrollment(personId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { enrollmentId: string; etag: string; patch: UpdateEnrollmentRequest }) =>
      ops.benefit.patchEnrollment(tenantId, input.enrollmentId, input.etag, input.patch),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: benefitKeys.enrollments(tenantId, personId) });
    },
  });
}

export function usePersonEntitlements(personId: string, asOf: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: benefitKeys.entitlements(tenantId, personId, asOf),
    queryFn: () => ops.entitlements.listPersonEntitlements(tenantId, personId, asOf),
  });
}

export function useLedger(accountId: string, query: LedgerQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: benefitKeys.ledger(tenantId, accountId, query),
    queryFn: () => ops.entitlements.listLedger(tenantId, accountId, query),
    placeholderData: (previous) => previous,
  });
}

export function useAdjustments(status: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: benefitKeys.adjustments(tenantId, status),
    queryFn: () =>
      ops.entitlements.listAdjustments(tenantId, {
        status: status as 'PENDING' | 'APPROVED' | 'REJECTED',
      }),
  });
}

export function useAdjustmentCommands() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  const invalidate = () => void qc.invalidateQueries({ queryKey: benefitKeys.all(tenantId) });

  const create = useMutation({
    mutationFn: (input: {
      accountId: string;
      body: CreateAdjustmentRequest;
      idempotencyKey: string;
    }) =>
      ops.entitlements.createAdjustment(
        tenantId,
        input.accountId,
        input.body,
        input.idempotencyKey,
      ),
    onSuccess: invalidate,
  });
  const approve = useMutation({
    mutationFn: (input: { adjustmentId: string; etag: string; body?: ReviewComment }) =>
      ops.entitlements.approveAdjustment(
        tenantId,
        input.adjustmentId,
        input.etag,
        input.body ?? {},
      ),
    onSuccess: invalidate,
  });
  const reject = useMutation({
    mutationFn: (input: { adjustmentId: string; etag: string; body: ReasonCommand }) =>
      ops.entitlements.rejectAdjustment(tenantId, input.adjustmentId, input.etag, input.body),
    onSuccess: invalidate,
  });
  return { create, approve, reject };
}

/** The eligibility check: a command, never a cached query. */
export function useEligibilityCheck() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({
    mutationFn: (input: { body: EligibilityCheckRequest; idempotencyKey?: string }) =>
      ops.eligibility.check(tenantId, input.body, input.idempotencyKey),
  });
}
