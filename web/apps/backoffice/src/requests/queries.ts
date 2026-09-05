import type {
  CreateServiceRequest,
  ReasonCommand,
  ServiceRequestDecision,
  ServiceRequestItems,
  ServiceRequestListQuery,
  UpdateServiceRequest,
} from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

export const requestKeys = {
  all: (tenantId: string) => ['requests', tenantId] as const,
  list: (tenantId: string, query: ServiceRequestListQuery) =>
    ['requests', tenantId, 'list', query] as const,
  one: (tenantId: string, id: string) => ['requests', tenantId, 'one', id] as const,
  versions: (tenantId: string, id: string) => ['requests', tenantId, 'versions', id] as const,
  version: (tenantId: string, id: string, no: number) =>
    ['requests', tenantId, 'version', id, no] as const,
  eligibility: (tenantId: string, id: string) => ['requests', tenantId, 'eligibility', id] as const,
  rules: (tenantId: string, id: string) => ['requests', tenantId, 'rules', id] as const,
};

export function useRequests(query: ServiceRequestListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: requestKeys.list(tenantId, query),
    queryFn: () => ops.requests.list(tenantId, query),
    placeholderData: (previous) => previous,
  });
}

export function useRequest(requestId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: requestKeys.one(tenantId, requestId),
    queryFn: () => ops.requests.get(tenantId, requestId),
    enabled: requestId !== '',
  });
}

export function useRequestVersions(requestId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: requestKeys.versions(tenantId, requestId),
    queryFn: () => ops.requests.listVersions(tenantId, requestId),
    enabled: requestId !== '',
  });
}

export function useRequestVersion(requestId: string, versionNo: number | null) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: requestKeys.version(tenantId, requestId, versionNo ?? 0),
    queryFn: () => ops.requests.getVersion(tenantId, requestId, versionNo!),
    enabled: requestId !== '' && versionNo !== null,
  });
}

/**
 * What the system said. The two evaluations a submit was decided against are fetched
 * only when the request names them; a request that was never submitted has nothing to
 * show, and the section says so rather than pretending.
 */
export function useRequestEligibility(evaluationId: string | null | undefined) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: requestKeys.eligibility(tenantId, evaluationId ?? ''),
    queryFn: () => ops.eligibility.getEvaluation(tenantId, evaluationId!),
    enabled: Boolean(evaluationId),
    retry: false,
  });
}

export function useRequestRules(ruleEvaluationId: string | null | undefined) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: requestKeys.rules(tenantId, ruleEvaluationId ?? ''),
    queryFn: () => ops.rules.getEvaluation(tenantId, ruleEvaluationId!),
    enabled: Boolean(ruleEvaluationId),
    retry: false,
  });
}

/**
 * Every transition is its own command. There is deliberately no `setStatus` here: the
 * screen can only offer what the server has a route for, and each route carries its own
 * precondition and reason.
 */
export function useRequestCommands(requestId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  const settle = () => client.invalidateQueries({ queryKey: requestKeys.all(tenantId) });

  return {
    patch: useMutation({
      mutationFn: (v: { etag: string; body: UpdateServiceRequest }) =>
        ops.requests.patch(tenantId, requestId, v.etag, v.body),
      onSuccess: settle,
    }),
    items: useMutation({
      mutationFn: (v: { etag: string; body: ServiceRequestItems }) =>
        ops.requests.putItems(tenantId, requestId, v.etag, v.body),
      onSuccess: settle,
    }),
    submit: useMutation({
      mutationFn: (v: { etag: string }) => ops.requests.submit(tenantId, requestId, v.etag),
      onSuccess: settle,
    }),
    returnForCorrection: useMutation({
      mutationFn: (v: { etag: string; body: ReasonCommand }) =>
        ops.requests.returnForCorrection(tenantId, requestId, v.etag, v.body),
      onSuccess: settle,
    }),
    reject: useMutation({
      mutationFn: (v: { etag: string; body: ReasonCommand }) =>
        ops.requests.reject(tenantId, requestId, v.etag, v.body),
      onSuccess: settle,
    }),
    approve: useMutation({
      mutationFn: (v: { etag: string; body: ServiceRequestDecision }) =>
        ops.requests.approve(tenantId, requestId, v.etag, v.body),
      onSuccess: settle,
    }),
    partiallyApprove: useMutation({
      mutationFn: (v: { etag: string; body: ServiceRequestDecision }) =>
        ops.requests.partiallyApprove(tenantId, requestId, v.etag, v.body),
      onSuccess: settle,
    }),
    cancel: useMutation({
      mutationFn: (v: { etag: string; body: ReasonCommand }) =>
        ops.requests.cancel(tenantId, requestId, v.etag, v.body),
      onSuccess: settle,
    }),
  };
}

export function useCreateRequest() {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateServiceRequest) => ops.requests.create(tenantId, body),
    onSuccess: () => client.invalidateQueries({ queryKey: requestKeys.all(tenantId) }),
  });
}
