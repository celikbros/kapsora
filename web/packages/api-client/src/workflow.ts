import { randomId, type KapsoraClient } from './client';
import type { components } from './generated/kapsora-v1';
import { unwrap } from './problem';
import { versioned, type Versioned } from './versioned';

export type WorkQueue = components['schemas']['WorkQueue'];
export type WorkItem = components['schemas']['WorkItem'];
export type WorkItemPage = components['schemas']['WorkItemPage'];
export type WorkItemStatus = components['schemas']['WorkItemStatus'];
export type WorkItemComment = components['schemas']['WorkItemComment'];
export type CommentVisibility = components['schemas']['CommentVisibility'];
export type ApprovalPolicy = components['schemas']['ApprovalPolicy'];
export type CreateWorkQueue = components['schemas']['CreateWorkQueue'];
export type PatchWorkQueue = components['schemas']['PatchWorkQueue'];
export type ReassignWorkItem = components['schemas']['ReassignWorkItem'];
export type ReleaseWorkItem = components['schemas']['ReleaseWorkItem'];
export type CompleteWorkItem = components['schemas']['CompleteWorkItem'];
export type AddWorkItemComment = components['schemas']['AddWorkItemComment'];

export interface WorkItemListQuery {
  cursor?: string;
  limit?: number;
  queueId?: string;
  status?: WorkItemStatus;
  assignedToMe?: boolean;
  overdue?: boolean;
  aggregateType?: string;
  aggregateId?: string;
}

/**
 * Work queues and the items waiting in them.
 *
 * Claiming is optimistic and never a race: `claim` carries the ETag the caller read, and
 * the server refuses with 409 WORK_ITEM_ALREADY_CLAIMED naming whoever holds it. A screen
 * must show that name — "somebody else got it" without saying who leaves two people
 * clicking the same button.
 *
 * `slaMinutesSnapshot` and `dueAt` belong to the item, not to its queue. Retuning a queue
 * moves neither, so an item is judged by the clock it was given; never recompute a due
 * date on the client from the queue's current SLA.
 */
export function workflowOperations(client: KapsoraClient) {
  const header = (tenantId: string) => ({ 'X-Tenant-ID': tenantId });
  const withEtag = (tenantId: string, etag: string) => ({
    'X-Tenant-ID': tenantId,
    'If-Match': etag,
  });
  const command = (tenantId: string, etag: string, idempotencyKey: string) => ({
    'X-Tenant-ID': tenantId,
    'If-Match': etag,
    'Idempotency-Key': idempotencyKey,
  });
  const mergePatch = {
    headers: { 'Content-Type': 'application/merge-patch+json' },
    bodySerializer: (b: unknown) => JSON.stringify(b),
  };

  return {
    async listQueues(tenantId: string): Promise<WorkQueue[]> {
      const r = await unwrap(
        client.GET('/api/v1/work-queues', { params: { header: header(tenantId) } }),
      );
      return r.data.items;
    },

    async createQueue(
      tenantId: string,
      body: CreateWorkQueue,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<WorkQueue>> {
      const r = await unwrap(
        client.POST('/api/v1/work-queues', {
          params: { header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    /** Changing a queue's SLA does not move any existing item's due date. */
    async patchQueue(
      tenantId: string,
      queueId: string,
      etag: string,
      body: PatchWorkQueue,
    ): Promise<Versioned<WorkQueue>> {
      const r = await unwrap(
        client.PATCH('/api/v1/work-queues/{queueId}', {
          params: { header: withEtag(tenantId, etag), path: { queueId } },
          body,
          ...mergePatch,
        }),
      );
      return versioned(r.data, r.response);
    },

    async listItems(tenantId: string, query: WorkItemListQuery = {}): Promise<WorkItemPage> {
      const q: WorkItemListQuery = {};
      if (query.cursor) q.cursor = query.cursor;
      if (query.limit) q.limit = query.limit;
      if (query.queueId) q.queueId = query.queueId;
      if (query.status) q.status = query.status;
      if (query.assignedToMe !== undefined) q.assignedToMe = query.assignedToMe;
      if (query.overdue !== undefined) q.overdue = query.overdue;
      if (query.aggregateType) q.aggregateType = query.aggregateType;
      if (query.aggregateId) q.aggregateId = query.aggregateId;
      return (
        await unwrap(
          client.GET('/api/v1/work-items', { params: { header: header(tenantId), query: q } }),
        )
      ).data;
    },

    async getItem(tenantId: string, workItemId: string): Promise<Versioned<WorkItem>> {
      const r = await unwrap(
        client.GET('/api/v1/work-items/{workItemId}', {
          params: { header: header(tenantId), path: { workItemId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    /**
     * Take the item. A 409 here is not an error to swallow: it carries the current
     * assignee, and the screen owes the operator that name.
     */
    async claimItem(
      tenantId: string,
      workItemId: string,
      etag: string,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<WorkItem>> {
      const r = await unwrap(
        client.POST('/api/v1/work-items/{workItemId}/claim', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { workItemId } },
        }),
      );
      return versioned(r.data, r.response);
    },

    async releaseItem(
      tenantId: string,
      workItemId: string,
      etag: string,
      body: ReleaseWorkItem = {},
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<WorkItem>> {
      const r = await unwrap(
        client.POST('/api/v1/work-items/{workItemId}/release', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { workItemId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async reassignItem(
      tenantId: string,
      workItemId: string,
      etag: string,
      body: ReassignWorkItem,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<WorkItem>> {
      const r = await unwrap(
        client.POST('/api/v1/work-items/{workItemId}/reassign', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { workItemId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async completeItem(
      tenantId: string,
      workItemId: string,
      etag: string,
      body: CompleteWorkItem,
      idempotencyKey: string = randomId(),
    ): Promise<Versioned<WorkItem>> {
      const r = await unwrap(
        client.POST('/api/v1/work-items/{workItemId}/complete', {
          params: { header: command(tenantId, etag, idempotencyKey), path: { workItemId } },
          body,
        }),
      );
      return versioned(r.data, r.response);
    },

    async listComments(tenantId: string, workItemId: string): Promise<WorkItemComment[]> {
      const r = await unwrap(
        client.GET('/api/v1/work-items/{workItemId}/comments', {
          params: { header: header(tenantId), path: { workItemId } },
        }),
      );
      return r.data.items;
    },

    /**
     * A comment carries the audience it will be judged against. PROVIDER and MEMBER
     * visibility mean somebody outside this office reads it.
     */
    async addComment(
      tenantId: string,
      workItemId: string,
      body: AddWorkItemComment,
      idempotencyKey: string = randomId(),
    ): Promise<WorkItemComment> {
      return (
        await unwrap(
          client.POST('/api/v1/work-items/{workItemId}/comments', {
            params: {
              header: { ...header(tenantId), 'Idempotency-Key': idempotencyKey },
              path: { workItemId },
            },
            body,
          }),
        )
      ).data;
    },

    async listApprovalPolicies(tenantId: string, actionCode?: string): Promise<ApprovalPolicy[]> {
      const query: { actionCode?: string } = {};
      if (actionCode) query.actionCode = actionCode;
      const r = await unwrap(
        client.GET('/api/v1/approval-policies', {
          params: { header: header(tenantId), query },
        }),
      );
      return r.data.items;
    },
  };
}
