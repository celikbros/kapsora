import type {
  CreateRuleSetRequest,
  CreateRuleSetVersionRequest,
  ReasonCommand,
  ReviewComment,
  RuleInput,
  RuleSetListQuery,
  RuleTestCaseInput,
  UpdateRuleSetRequest,
  UpdateRuleSetVersionRequest,
} from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

export const ruleKeys = {
  all: (tenantId: string) => ['rules', tenantId] as const,
  sets: (tenantId: string, query: RuleSetListQuery) => ['rules', tenantId, 'sets', query] as const,
  set: (tenantId: string, id: string) => ['rules', tenantId, 'set', id] as const,
  versions: (tenantId: string, ruleSetId: string) =>
    ['rules', tenantId, 'versions', ruleSetId] as const,
  version: (tenantId: string, id: string) => ['rules', tenantId, 'version', id] as const,
  testRun: (tenantId: string, id: string) => ['rules', tenantId, 'test-run', id] as const,
};

export function useRuleSets(query: RuleSetListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ruleKeys.sets(tenantId, query),
    queryFn: () => ops.rules.listSets(tenantId, query),
    placeholderData: (previous) => previous,
  });
}

export function useRuleSet(ruleSetId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ruleKeys.set(tenantId, ruleSetId),
    queryFn: () => ops.rules.getSet(tenantId, ruleSetId),
    enabled: ruleSetId !== '',
  });
}

/** Version history of a set. Drafts are absent for a caller holding only `rule.read`. */
export function useRuleSetVersions(ruleSetId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ruleKeys.versions(tenantId, ruleSetId),
    queryFn: () => ops.rules.listVersions(tenantId, ruleSetId),
    enabled: ruleSetId !== '',
  });
}

export function useRuleSetVersion(ruleSetVersionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ruleKeys.version(tenantId, ruleSetVersionId),
    queryFn: () => ops.rules.getVersion(tenantId, ruleSetVersionId),
    enabled: ruleSetVersionId !== '',
  });
}

export function useCreateRuleSet() {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateRuleSetRequest; idempotencyKey: string }) =>
      ops.rules.createSet(tenantId, input.body, input.idempotencyKey),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ruleKeys.all(tenantId) }),
  });
}

export function useUpdateRuleSet(ruleSetId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { etag: string; patch: UpdateRuleSetRequest }) =>
      ops.rules.patchSet(tenantId, ruleSetId, input.etag, input.patch),
    onSuccess: (result) => {
      qc.setQueryData(ruleKeys.set(tenantId, ruleSetId), result);
      void qc.invalidateQueries({ queryKey: ruleKeys.all(tenantId) });
    },
  });
}

export function useCreateRuleSetVersion(ruleSetId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: CreateRuleSetVersionRequest; idempotencyKey: string }) =>
      ops.rules.createVersion(tenantId, ruleSetId, input.body, input.idempotencyKey),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ruleKeys.all(tenantId) }),
  });
}

/**
 * The stored test cases run against the version. This is a query rather than a command
 * because the run writes nothing at all — no evaluation row, no side effect — so it may
 * be cached, refetched and read as often as the screen likes. Its answer is the gate the
 * submit button stands behind, which is why the page wants it without being asked.
 */
export function useRuleTestRun(ruleSetVersionId: string, enabled: boolean) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: ruleKeys.testRun(tenantId, ruleSetVersionId),
    queryFn: () => ops.rules.runTests(tenantId, ruleSetVersionId),
    enabled: enabled && ruleSetVersionId !== '',
  });
}

/** One supplied input against the version. Writes nothing either. */
export function useRuleSimulation(ruleSetVersionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useMutation({
    mutationFn: (input: { input: Record<string, unknown> }) =>
      ops.rules.simulate(tenantId, ruleSetVersionId, input.input),
  });
}

/** Every write a rule set version accepts; each carries the ETag the screen saw. */
export function useRuleSetVersionCommands(ruleSetVersionId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const qc = useQueryClient();
  const invalidate = () => void qc.invalidateQueries({ queryKey: ruleKeys.all(tenantId) });

  const patch = useMutation({
    mutationFn: (input: { etag: string; patch: UpdateRuleSetVersionRequest }) =>
      ops.rules.patchVersion(tenantId, ruleSetVersionId, input.etag, input.patch),
    onSuccess: invalidate,
  });
  const rules = useMutation({
    mutationFn: (input: { etag: string; items: RuleInput[] }) =>
      ops.rules.replaceRules(tenantId, ruleSetVersionId, input.etag, input.items),
    onSuccess: invalidate,
  });
  const testCases = useMutation({
    mutationFn: (input: { etag: string; items: RuleTestCaseInput[] }) =>
      ops.rules.replaceTestCases(tenantId, ruleSetVersionId, input.etag, input.items),
    onSuccess: invalidate,
  });
  const submit = useMutation({
    mutationFn: (input: { etag: string; body?: ReviewComment }) =>
      ops.rules.submitVersion(tenantId, ruleSetVersionId, input.etag, input.body ?? {}),
    onSuccess: invalidate,
  });
  const publish = useMutation({
    mutationFn: (input: { etag: string; body?: ReviewComment }) =>
      ops.rules.publishVersion(tenantId, ruleSetVersionId, input.etag, input.body ?? {}),
    onSuccess: invalidate,
  });
  const retire = useMutation({
    mutationFn: (input: { etag: string; body: ReasonCommand }) =>
      ops.rules.retireVersion(tenantId, ruleSetVersionId, input.etag, input.body),
    onSuccess: invalidate,
  });
  return { patch, rules, testCases, submit, publish, retire };
}
