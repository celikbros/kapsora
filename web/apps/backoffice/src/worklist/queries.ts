import type {
  AddWorkItemComment,
  CompleteWorkItem,
  ReassignWorkItem,
  WorkItemListQuery,
} from '@kapsora/api-client';
import { useSession, useTenantId } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useOps } from '../api';

export const worklistKeys = {
  all: (tenantId: string) => ['worklist', tenantId] as const,
  queues: (tenantId: string) => ['worklist', tenantId, 'queues'] as const,
  items: (tenantId: string, query: WorkItemListQuery) =>
    ['worklist', tenantId, 'items', query] as const,
  item: (tenantId: string, id: string) => ['worklist', tenantId, 'item', id] as const,
  comments: (tenantId: string, id: string) => ['worklist', tenantId, 'comments', id] as const,
};

export function useWorkQueues() {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: worklistKeys.queues(tenantId),
    queryFn: () => ops.worklist.listQueues(tenantId),
    staleTime: 5 * 60_000,
  });
}

export function useWorkItems(query: WorkItemListQuery) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: worklistKeys.items(tenantId, query),
    queryFn: () => ops.worklist.listItems(tenantId, query),
    placeholderData: (previous) => previous,
  });
}

export function useWorkItem(workItemId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: worklistKeys.item(tenantId, workItemId),
    queryFn: () => ops.worklist.getItem(tenantId, workItemId),
    enabled: workItemId !== '',
  });
}

export function useWorkItemComments(workItemId: string) {
  const ops = useOps();
  const tenantId = useTenantId();
  return useQuery({
    queryKey: worklistKeys.comments(tenantId, workItemId),
    queryFn: () => ops.worklist.listComments(tenantId, workItemId),
    enabled: workItemId !== '',
  });
}

/** Claim, release, complete and reassign — each its own command with its own ETag. */
export function useWorkItemCommands() {
  const ops = useOps();
  const tenantId = useTenantId();
  const client = useQueryClient();
  const settle = () => client.invalidateQueries({ queryKey: worklistKeys.all(tenantId) });
  return {
    claim: useMutation({
      mutationFn: (v: { id: string; etag: string }) =>
        ops.worklist.claimItem(tenantId, v.id, v.etag),
      // Settle on failure too: a 409 means the row moved, and the list should show who has it.
      onSettled: settle,
    }),
    release: useMutation({
      mutationFn: (v: { id: string; etag: string }) =>
        ops.worklist.releaseItem(tenantId, v.id, v.etag),
      onSettled: settle,
    }),
    complete: useMutation({
      mutationFn: (v: { id: string; etag: string; body: CompleteWorkItem }) =>
        ops.worklist.completeItem(tenantId, v.id, v.etag, v.body),
      onSettled: settle,
    }),
    reassign: useMutation({
      mutationFn: (v: { id: string; etag: string; body: ReassignWorkItem }) =>
        ops.worklist.reassignItem(tenantId, v.id, v.etag, v.body),
      onSettled: settle,
    }),
    comment: useMutation({
      mutationFn: (v: { id: string; body: AddWorkItemComment }) =>
        ops.worklist.addComment(tenantId, v.id, v.body),
      onSuccess: settle,
    }),
  };
}

/**
 * Who an actor id is, as far as this screen can say. The contract carries no actor
 * directory, so the only name the screen knows is the operator's own; every other actor
 * is shown by id, truthfully, rather than by a name the screen would have to invent.
 * A display name on the work item is the contract change that fixes this (ROADMAP.md).
 */
export function useActorLabel(): (actorId: string | null | undefined) => string {
  const { t } = useTranslation();
  const me = useSession((s) => s.me);
  return (actorId) => {
    if (!actorId) return t('worklist.unassigned');
    if (me && actorId === me.actorId) return t('worklist.me');
    return actorId;
  };
}
