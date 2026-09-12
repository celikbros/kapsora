/**
 * MSW handlers for the M4 worklist: work queues, the items waiting in them, the four
 * commands that move an item, the comments on it, and the approval policies.
 *
 * Two behaviours carry the module. Claiming is optimistic and is decided by re-reading
 * the row: claiming an item somebody else already holds answers 409
 * WORK_ITEM_ALREADY_CLAIMED naming the actor who holds it, so a screen can say who won
 * rather than "bir hata oluştu". And the SLA is a snapshot — an item carries the
 * `slaMinutesSnapshot` and `dueAt` its queue handed out when the item was raised, and
 * nothing moves them afterwards, escalation included: a late item stays late.
 */
import { HttpResponse, http, type HttpHandler, type PathParams } from 'msw';

import {
  compareDecimal,
  isDecimalText,
  toApprovalPolicy,
  assigneeDisplayName,
  toWorkItem,
  toWorkItemComment,
  toWorkQueue,
  type MockWorld,
  type StoredApprovalPolicy,
  type StoredWorkItem,
  type StoredWorkQueue,
} from './data';
import { grantsFor, type MockApi, type MockSession } from './handlers';
import {
  ANY,
  decodeCursor,
  encodeCursor,
  etagOf,
  guardTenant,
  parseLimit,
  pathParam,
  periodsOverlap,
  problem,
  readJson,
  requireIdempotencyKey,
  requireIfMatch,
  requireMergePatch,
  validationFailed,
  wait,
  type FieldError,
  type Guarded,
  type Schemas,
} from './handlers';

const QUEUE_CODE = /^[A-Z][A-Z0-9_]{1,63}$/;
const ACTION_CODE = /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,4}$/;
const SCOPE_CODE = /^[A-Z][A-Z0-9_]{0,63}$/;
const OUTCOME_CODE = /^[A-Z][A-Z0-9_]{1,63}$/;
const MAX_SLA_MINUTES = 525_600;
const MAX_POLICIES = 50;
const MAX_ROLE_CODES = 20;
const MAX_COMMENTS = 500;

const DOMAINS = new Set<string>([
  'GENERIC',
  'HEALTH',
  'ACCOMMODATION',
  'ASSISTANCE',
  'EDUCATION',
  'SPORT',
  'TRANSPORT',
  'CARE',
  'OTHER',
]);
const ASSIGNMENT_POLICIES = new Set<string>(['MANUAL', 'ROUND_ROBIN', 'LEAST_LOADED']);
const VISIBILITIES = new Set<string>(['INTERNAL', 'PROVIDER', 'MEMBER']);

/**
 * The whole item lifecycle. There is no entry that leads out of COMPLETED, CANCELLED or
 * ESCALATED: an escalated item cannot be resurrected by somebody claiming it.
 */
const TRANSITIONS: Record<string, Partial<Record<string, Schemas['WorkItemStatus']>>> = {
  CLAIM: { OPEN: 'CLAIMED' },
  RELEASE: { CLAIMED: 'OPEN' },
  REASSIGN: { OPEN: 'CLAIMED', CLAIMED: 'CLAIMED' },
  COMPLETE: { CLAIMED: 'COMPLETED' },
};

function queueNotFound(api: MockApi): Response {
  return problem(api, 404, 'WORK_QUEUE_NOT_FOUND', 'İş kuyruğu bulunamadı');
}

function itemNotFound(api: MockApi): Response {
  return problem(api, 404, 'WORK_ITEM_NOT_FOUND', 'İş kalemi bulunamadı');
}

function transitionInvalid(api: MockApi): Response {
  return problem(api, 409, 'WORK_ITEM_TRANSITION_INVALID', 'Bu durum geçişi yapılamaz');
}

function notAssignee(api: MockApi): Response {
  return problem(api, 409, 'WORK_ITEM_NOT_ASSIGNEE', 'Bu iş kalemi sizde değil');
}

function etagMismatch(api: MockApi): Response {
  return problem(api, 412, 'ETAG_MISMATCH', 'Kayıt bu arada değişti');
}

/**
 * The caller's queue boundary, read from its WORK_QUEUE access grants. `null` means
 * unrestricted; anything outside the boundary is genuinely absent from the answer, which
 * is why a caller outside it is told 404 and never 403.
 */
function queueScope(api: MockApi, session: MockSession, tenantId: string): string[] | null {
  const tenant = api.world.tenants.find((t) => t.id === tenantId);
  if (!tenant) return [];
  const membership = grantsFor(session, tenant.code);
  const grants = (membership?.scopes ?? []).filter((g) => g.type === 'WORK_QUEUE');
  if (grants.length === 0) return null;
  return grants.filter((g) => g.id !== null).map((g) => g.id!);
}

function inQueueScope(scope: string[] | null, queueId: string): boolean {
  return scope === null || scope.includes(queueId);
}

/**
 * The permission a queue's work takes (migration 000048). An item is listed to, and claimable
 * by, only a caller who holds it: a worklist that showed every queue would put the medical
 * review queue in front of somebody who cannot read a report, and let them take the work off
 * the people who can.
 */
function canWorkQueue(
  api: MockApi,
  session: MockSession,
  tenantId: string,
  queueId: string,
): boolean {
  const queue = api.world.workQueues.find((q) => q.id === queueId && q.tenantId === tenantId);
  const tenant = api.world.tenants.find((t) => t.id === tenantId);
  if (!queue || !tenant) return false;
  return grantsFor(session, tenant.code).permissions.includes(queue.requiredPermission);
}

export function workflowHandlers(api: MockApi): HttpHandler[] {
  const world = (): MockWorld => api.world;

  /**
   * The queue list is readable by two grants: the person who defines queues and the
   * person who works them. Both are tried in the order the server tries them, so the
   * denial a caller holding neither sees names the first.
   */
  const guardAny = (request: Request, permissions: string[], mutation: boolean): Guarded => {
    let last: Guarded = {
      error: problem(api, 403, 'PERMISSION_DENIED', 'Bu işlem için yetkiniz yok'),
    };
    for (const permission of permissions) {
      const g = guardTenant(api, request, permission, mutation);
      if (!('error' in g)) return g;
      last = g;
    }
    return last;
  };

  const findQueue = (
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredWorkQueue | undefined => {
    const scope = queueScope(api, session, tenantId);
    const row = world().workQueues.find((q) => q.id === id && q.tenantId === tenantId);
    return row && inQueueScope(scope, row.id) ? row : undefined;
  };

  const findItem = (
    session: MockSession,
    tenantId: string,
    id: string,
  ): StoredWorkItem | undefined => {
    const scope = queueScope(api, session, tenantId);
    const row = world().workItems.find((i) => i.id === id && i.tenantId === tenantId);
    return row &&
      inQueueScope(scope, row.queueId) &&
      canWorkQueue(api, session, tenantId, row.queueId)
      ? row
      : undefined;
  };

  const answerItem = (item: StoredWorkItem): Response =>
    HttpResponse.json(toWorkItem(world(), item), { headers: { ETag: etagOf(item.rowVersion) } });

  /** The plumbing release, reassign and complete share; claim is deliberately not here. */
  const itemCommand =
    <T>(
      permission: string,
      command: 'RELEASE' | 'REASSIGN' | 'COMPLETE',
      validate: (body: T | null) => FieldError[],
      checkAssignee: boolean,
      apply: (item: StoredWorkItem, body: T | null, actorId: string, to: string) => void,
    ) =>
    async ({ request, params }: { request: Request; params: PathParams }) => {
      await wait(api);
      const g = guardTenant(api, request, permission, true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const expected = requireIfMatch(api, request);
      if (typeof expected !== 'number') return expected;
      const body = await readJson<T>(request);
      const errors = validate(body);
      if (errors.length > 0) return validationFailed(api, errors);
      const item = findItem(g.session, g.tenantId, pathParam(params, 'workItemId'));
      if (!item) return itemNotFound(api);
      const to = TRANSITIONS[command]?.[item.status];
      if (!to) return transitionInvalid(api);
      // Whether the caller holds the item is asked before the row version: "this is not
      // yours" is a more useful answer than "somebody changed it".
      if (checkAssignee && item.assigneeActorId !== g.session.account.actorId) {
        return notAssignee(api);
      }
      if (item.rowVersion !== expected) return etagMismatch(api);
      apply(item, body, g.session.account.actorId, to);
      item.rowVersion += 1;
      return answerItem(item);
    };

  return [
    http.get(`${ANY}/api/v1/work-queues`, async ({ request }) => {
      await wait(api);
      const g = guardAny(request, ['workflow.queue.manage', 'worklist.read'], false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') {
        return validationFailed(api, [{ field: 'limit', code: 'FORMAT' }]);
      }
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const domainCode = url.searchParams.get('domainCode');
      if (domainCode && !DOMAINS.has(domainCode)) {
        return validationFailed(api, [{ field: 'domainCode', code: 'ENUM' }]);
      }
      const activeRaw = url.searchParams.get('active');
      const scope = queueScope(api, g.session, g.tenantId);
      const rows = world()
        .workQueues.filter(
          (q) =>
            q.tenantId === g.tenantId &&
            inQueueScope(scope, q.id) &&
            (!domainCode || q.domainCode === domainCode) &&
            (activeRaw === null || q.active === (activeRaw === 'true')),
        )
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      const page: Schemas['WorkQueuePage'] = {
        items: rows.slice(offset, offset + limit).map(toWorkQueue),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(page);
    }),

    http.post(`${ANY}/api/v1/work-queues`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'workflow.queue.manage', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const body = await readJson<Schemas['CreateWorkQueue']>(request);
      const errors: FieldError[] = [];
      if (typeof body?.code !== 'string' || !QUEUE_CODE.test(body.code)) {
        errors.push({ field: 'code', code: 'FORMAT' });
      }
      if (typeof body?.name !== 'string' || body.name.trim().length < 2) {
        errors.push({ field: 'name', code: 'LENGTH' });
      }
      if (!DOMAINS.has(body?.domainCode as string)) {
        errors.push({ field: 'domainCode', code: 'ENUM' });
      }
      if (body?.assignmentPolicy !== undefined && !ASSIGNMENT_POLICIES.has(body.assignmentPolicy)) {
        errors.push({ field: 'assignmentPolicy', code: 'ENUM' });
      }
      if (
        body?.slaMinutes !== undefined &&
        body.slaMinutes !== null &&
        (!Number.isInteger(body.slaMinutes) ||
          body.slaMinutes < 1 ||
          body.slaMinutes > MAX_SLA_MINUTES)
      ) {
        errors.push({ field: 'slaMinutes', code: 'RANGE' });
      }
      if (errors.length > 0) return validationFailed(api, errors);
      if (body!.escalationQueueId) {
        const target = world().workQueues.find(
          (q) => q.id === body!.escalationQueueId && q.tenantId === g.tenantId,
        );
        if (!target) return queueNotFound(api);
      }
      if (world().workQueues.some((q) => q.tenantId === g.tenantId && q.code === body!.code)) {
        return problem(api, 409, 'WORK_QUEUE_CODE_TAKEN', 'Bu kuyruk kodu zaten kullanılıyor');
      }
      const queue: StoredWorkQueue = {
        tenantId: g.tenantId,
        id: world().nextId(),
        code: body!.code,
        name: body!.name,
        domainCode: body!.domainCode,
        assignmentPolicy: body!.assignmentPolicy ?? 'MANUAL',
        // Copied onto every item raised from this moment on, and never read again.
        slaMinutes: body!.slaMinutes ?? null,
        escalationQueueId: body!.escalationQueueId ?? null,
        active: body!.active ?? true,
        requiredPermission: body!.requiredPermission ?? 'worklist.read',
        rowVersion: 1,
        createdAt: new Date().toISOString(),
      };
      world().workQueues.push(queue);
      return HttpResponse.json(toWorkQueue(queue), {
        status: 201,
        headers: { ETag: etagOf(queue.rowVersion) },
      });
    }),

    http.patch(`${ANY}/api/v1/work-queues/:queueId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'workflow.queue.manage', true);
      if ('error' in g) return g.error;
      const wrongType = requireMergePatch(api, request);
      if (wrongType) return wrongType;
      const expected = requireIfMatch(api, request);
      if (typeof expected !== 'number') return expected;
      const patch = (await readJson<Record<string, unknown>>(request)) ?? {};
      const errors: FieldError[] = [];
      const writable = new Set([
        'name',
        'assignmentPolicy',
        'slaMinutes',
        'escalationQueueId',
        'active',
        'requiredPermission',
      ]);
      // The code and the domain are what other rows already point at; changing them in
      // place would silently repoint them.
      const immutable = new Set(['code', 'domainCode', 'id', 'rowVersion', 'createdAt']);
      for (const key of Object.keys(patch)) {
        if (writable.has(key)) continue;
        errors.push(
          immutable.has(key)
            ? { field: key, code: 'IMMUTABLE', message: 'bu alan değiştirilemez' }
            : { field: key, code: 'UNKNOWN_FIELD', message: 'bilinmeyen alan' },
        );
      }
      const sla = patch['slaMinutes'];
      if (
        sla !== undefined &&
        sla !== null &&
        (!Number.isInteger(sla) || (sla as number) < 1 || (sla as number) > MAX_SLA_MINUTES)
      ) {
        errors.push({ field: 'slaMinutes', code: 'RANGE' });
      }
      if (
        patch['assignmentPolicy'] !== undefined &&
        !ASSIGNMENT_POLICIES.has(patch['assignmentPolicy'] as string)
      ) {
        errors.push({ field: 'assignmentPolicy', code: 'ENUM' });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      const queue = findQueue(g.session, g.tenantId, pathParam(params, 'queueId'));
      if (!queue) return queueNotFound(api);
      if (queue.rowVersion !== expected) return etagMismatch(api);
      const escalation = patch['escalationQueueId'];
      if (escalation !== undefined && escalation !== null) {
        if (escalation === queue.id) {
          return problem(
            api,
            422,
            'WORK_QUEUE_ESCALATION_CYCLE',
            'Bir kuyruk kendine eskale edemez',
          );
        }
        if (!world().workQueues.some((q) => q.id === escalation && q.tenantId === g.tenantId)) {
          return queueNotFound(api);
        }
      }
      if (typeof patch['name'] === 'string') queue.name = patch['name'];
      if (typeof patch['requiredPermission'] === 'string')
        queue.requiredPermission = patch['requiredPermission'];
      if (patch['assignmentPolicy'] !== undefined) {
        queue.assignmentPolicy = patch['assignmentPolicy'] as Schemas['AssignmentPolicy'];
      }
      // Changing the SLA changes what items raised from now on are given and nothing
      // else: every item already in the queue keeps the clock it was given.
      if ('slaMinutes' in patch) queue.slaMinutes = (sla as number | null) ?? null;
      if ('escalationQueueId' in patch) {
        queue.escalationQueueId = (escalation as string | null) ?? null;
      }
      if (typeof patch['active'] === 'boolean') queue.active = patch['active'];
      queue.rowVersion += 1;
      return HttpResponse.json(toWorkQueue(queue), {
        headers: { ETag: etagOf(queue.rowVersion) },
      });
    }),

    http.get(`${ANY}/api/v1/work-items`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'worklist.read', false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const limit = parseLimit(url);
      if (limit === 'invalid') return validationFailed(api, [{ field: 'limit', code: 'FORMAT' }]);
      const offset = decodeCursor(url.searchParams.get('cursor'));
      if (offset === null) return problem(api, 400, 'CURSOR_INVALID', 'Sayfa imleci geçersiz');
      const queueId = url.searchParams.get('queueId');
      const status = url.searchParams.get('status');
      const aggregateType = url.searchParams.get('aggregateType');
      const aggregateId = url.searchParams.get('aggregateId');
      const assignedToMe = url.searchParams.get('assignedToMe');
      const overdue = url.searchParams.get('overdue');
      const now = new Date().toISOString();
      const scope = queueScope(api, g.session, g.tenantId);
      const rows = world()
        .workItems.filter((i) => {
          if (i.tenantId !== g.tenantId || !inQueueScope(scope, i.queueId)) return false;
          if (!canWorkQueue(api, g.session, g.tenantId, i.queueId)) return false;
          if (queueId && i.queueId !== queueId) return false;
          if (status && i.status !== status) return false;
          if (aggregateType && i.aggregateType !== aggregateType) return false;
          if (aggregateId && i.aggregateId !== aggregateId) return false;
          // Resolved against the calling actor rather than taking an actor id, so
          // somebody else's list is not a question this endpoint can be made to answer.
          if (assignedToMe !== null) {
            const mine = i.assigneeActorId === g.session.account.actorId;
            if (mine !== (assignedToMe === 'true')) return false;
          }
          // Answered from the item's own due date, never from its queue's SLA today.
          if (overdue !== null) {
            const late = i.dueAt !== null && i.dueAt !== undefined && i.dueAt < now;
            if (late !== (overdue === 'true')) return false;
          }
          return true;
        })
        .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id));
      const page: Schemas['WorkItemPage'] = {
        items: rows.slice(offset, offset + limit).map((i) => toWorkItem(world(), i)),
        nextCursor: offset + limit < rows.length ? encodeCursor(offset + limit) : null,
      };
      return HttpResponse.json(page);
    }),

    http.get(`${ANY}/api/v1/work-items/:workItemId`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'worklist.read', false);
      if ('error' in g) return g.error;
      const item = findItem(g.session, g.tenantId, pathParam(params, 'workItemId'));
      if (!item) return itemNotFound(api);
      return answerItem(item);
    }),

    http.post(`${ANY}/api/v1/work-items/:workItemId/claim`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'worklist.claim', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const expected = requireIfMatch(api, request);
      if (typeof expected !== 'number') return expected;
      const item = findItem(g.session, g.tenantId, pathParam(params, 'workItemId'));
      if (!item) return itemNotFound(api);
      // Optimistic: the status is not pre-checked, the write is conditional on it. When
      // the write does not take, the row is read again and the answer says which of the
      // three things happened — somebody else took it, the tag was stale, or the item had
      // moved on. Naming the holder is the whole point: a screen can say who won.
      if (item.status !== 'OPEN' || item.rowVersion !== expected) {
        if (item.status === 'CLAIMED') {
          // The detail is the sentence a person reads; the extension members are the
          // same fact in a form a screen can use without parsing Turkish (WP-I5-05
          // section 2.6).
          const winner = assigneeDisplayName(world(), item.assigneeActorId);
          return problem(
            api,
            409,
            'WORK_ITEM_ALREADY_CLAIMED',
            'İş kalemi başkası tarafından üstlenilmiş',
            {
              detail: winner
                ? `İş kalemi ${winner} kullanıcısında.`
                : item.assigneeActorId
                  ? `İş kalemi ${item.assigneeActorId} kimlikli kullanıcıda.`
                  : 'İş kalemi artık üstlenilebilir durumda değil.',
              ...(item.assigneeActorId
                ? {
                    extensions: {
                      assigneeActorId: item.assigneeActorId,
                      ...(winner ? { assigneeDisplayName: winner } : {}),
                    },
                  }
                : {}),
            },
          );
        }
        if (item.status === 'OPEN') return etagMismatch(api);
        return transitionInvalid(api);
      }
      item.status = 'CLAIMED';
      item.assigneeActorId = g.session.account.actorId;
      item.assignedAt = new Date().toISOString();
      item.rowVersion += 1;
      return answerItem(item);
    }),

    http.post(
      `${ANY}/api/v1/work-items/:workItemId/release`,
      itemCommand<Schemas['ReleaseWorkItem']>(
        'worklist.claim',
        'RELEASE',
        () => [],
        true,
        (item) => {
          item.status = 'OPEN';
          item.assigneeActorId = null;
          item.assignedAt = null;
        },
      ),
    ),

    http.post(
      `${ANY}/api/v1/work-items/:workItemId/reassign`,
      itemCommand<Schemas['ReassignWorkItem']>(
        'worklist.reassign',
        'REASSIGN',
        (body) =>
          body?.assigneeActorId
            ? []
            : [
                {
                  field: 'assigneeActorId',
                  code: 'REQUIRED',
                  message: 'devredilecek kişi zorunlu',
                },
              ],
        // Deliberately not checked: taking an item off somebody is exactly what the
        // separate `worklist.reassign` grant is for.
        false,
        (item, body) => {
          item.status = 'CLAIMED';
          item.assigneeActorId = body!.assigneeActorId;
          item.assignedAt = new Date().toISOString();
        },
      ),
    ),

    http.post(
      `${ANY}/api/v1/work-items/:workItemId/complete`,
      itemCommand<Schemas['CompleteWorkItem']>(
        'worklist.claim',
        'COMPLETE',
        (body) =>
          typeof body?.outcomeCode === 'string' && OUTCOME_CODE.test(body.outcomeCode)
            ? []
            : [
                {
                  field: 'outcomeCode',
                  code: 'FORMAT',
                  // An item closed with no outcome is a row nobody can report on.
                  message: 'sonuç kodu zorunlu; büyük harf, rakam ve alt çizgi',
                },
              ],
        true,
        (item, body, actorId) => {
          item.status = 'COMPLETED';
          item.outcomeCode = body!.outcomeCode;
          item.completedAt = new Date().toISOString();
          item.completedBy = actorId;
          const note = body!.comment?.trim();
          if (note) {
            // Stored as an INTERNAL comment in the same step: it is free text a person
            // typed, and it belongs to nobody outside the office.
            api.world.workItemComments.push({
              tenantId: item.tenantId,
              id: api.world.nextId(),
              workItemId: item.id,
              aggregateType: item.aggregateType,
              aggregateId: item.aggregateId,
              visibility: 'INTERNAL',
              body: note,
              authorActorId: actorId,
              createdAt: item.completedAt,
            });
          }
        },
      ),
    ),

    http.get(`${ANY}/api/v1/work-items/:workItemId/comments`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'worklist.read', false);
      if ('error' in g) return g.error;
      const item = findItem(g.session, g.tenantId, pathParam(params, 'workItemId'));
      if (!item) return itemNotFound(api);
      const url = new URL(request.url);
      const wanted = url.searchParams.getAll('visibility');
      for (const v of wanted) {
        if (!VISIBILITIES.has(v))
          return validationFailed(api, [{ field: 'visibility', code: 'ENUM' }]);
      }
      const list: Schemas['WorkItemCommentList'] = {
        // Oldest first: a conversation read in the order it happened.
        items: world()
          .workItemComments.filter(
            (c) =>
              c.workItemId === item.id &&
              c.tenantId === g.tenantId &&
              (wanted.length === 0 || wanted.includes(c.visibility)),
          )
          .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id))
          .slice(0, MAX_COMMENTS)
          .map(toWorkItemComment),
      };
      return HttpResponse.json(list);
    }),

    http.post(`${ANY}/api/v1/work-items/:workItemId/comments`, async ({ request, params }) => {
      await wait(api);
      const g = guardTenant(api, request, 'worklist.read', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const body = await readJson<Schemas['AddWorkItemComment']>(request);
      const errors: FieldError[] = [];
      if (typeof body?.body !== 'string' || body.body.trim().length === 0) {
        errors.push({ field: 'body', code: 'REQUIRED' });
      }
      if (!VISIBILITIES.has(body?.visibility as string)) {
        errors.push({ field: 'visibility', code: 'ENUM' });
      }
      if (errors.length > 0) return validationFailed(api, errors);
      const item = findItem(g.session, g.tenantId, pathParam(params, 'workItemId'));
      if (!item) return itemNotFound(api);
      // Append-only, and no If-Match: the comment does not touch the item's version, so a
      // note left while somebody else is deciding never collides with the decision.
      const comment = {
        tenantId: g.tenantId,
        id: world().nextId(),
        workItemId: item.id,
        aggregateType: item.aggregateType,
        aggregateId: item.aggregateId,
        // The visibility is stored, not interpreted: this is where the audience a comment
        // will be judged against is recorded.
        visibility: body!.visibility,
        body: body!.body.trim(),
        authorActorId: g.session.account.actorId,
        createdAt: new Date().toISOString(),
      };
      world().workItemComments.push(comment);
      return HttpResponse.json(toWorkItemComment(comment), { status: 201 });
    }),

    http.get(`${ANY}/api/v1/approval-policies`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'workflow.policy.manage', false);
      if ('error' in g) return g.error;
      const url = new URL(request.url);
      const actionCode = url.searchParams.get('actionCode');
      const scopeCode = url.searchParams.get('scopeCode');
      const list: Schemas['ApprovalPolicyList'] = {
        // Short and complete rather than paged: a policy set is read as a whole, and half
        // of one answers nothing.
        items: world()
          .approvalPolicies.filter(
            (p) =>
              p.tenantId === g.tenantId &&
              (!actionCode || p.actionCode === actionCode) &&
              (!scopeCode || p.scopeCode === scopeCode),
          )
          .sort(
            (a, b) =>
              a.actionCode.localeCompare(b.actionCode) ||
              a.scopeCode.localeCompare(b.scopeCode) ||
              a.validFrom.localeCompare(b.validFrom),
          )
          .map(toApprovalPolicy),
      };
      return HttpResponse.json(list);
    }),

    http.put(`${ANY}/api/v1/approval-policies`, async ({ request }) => {
      await wait(api);
      const g = guardTenant(api, request, 'workflow.policy.manage', true);
      if ('error' in g) return g.error;
      const missingKey = requireIdempotencyKey(api, request);
      if (missingKey) return missingKey;
      const body = await readJson<Schemas['PutApprovalPolicies']>(request);
      const errors: FieldError[] = [];
      if (typeof body?.actionCode !== 'string' || !ACTION_CODE.test(body.actionCode)) {
        errors.push({ field: 'actionCode', code: 'FORMAT' });
      }
      const policies = body?.policies;
      if (!Array.isArray(policies)) {
        errors.push({ field: 'policies', code: 'REQUIRED' });
      } else if (policies.length > MAX_POLICIES) {
        errors.push({ field: 'policies', code: 'RANGE' });
      } else {
        policies.forEach((p, i) => {
          const path = `policies[${i}]`;
          if (typeof p?.scopeCode !== 'string' || !SCOPE_CODE.test(p.scopeCode)) {
            errors.push({ field: `${path}.scopeCode`, code: 'FORMAT' });
          }
          if (typeof p?.validFrom !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(p.validFrom)) {
            errors.push({ field: `${path}.validFrom`, code: 'FORMAT' });
          }
          if (p?.validTo !== undefined && p.validTo !== null) {
            if (!/^\d{4}-\d{2}-\d{2}$/.test(p.validTo)) {
              errors.push({ field: `${path}.validTo`, code: 'FORMAT' });
            } else if (typeof p.validFrom === 'string' && p.validTo <= p.validFrom) {
              errors.push({ field: `${path}.validTo`, code: 'RANGE' });
            }
          }
          // Both amounts are exact decimal strings: a band that had passed through a float
          // would refuse an approval it should have allowed.
          for (const field of ['minAmount', 'maxAmount'] as const) {
            const value = p?.[field];
            if (value !== undefined && value !== null && !isDecimalText(value)) {
              errors.push({ field: `${path}.${field}`, code: 'FORMAT' });
            }
          }
          if (
            p?.minAmount != null &&
            p?.maxAmount != null &&
            isDecimalText(p.minAmount) &&
            isDecimalText(p.maxAmount) &&
            compareDecimal(p.minAmount, p.maxAmount) > 0
          ) {
            errors.push({ field: `${path}.maxAmount`, code: 'RANGE' });
          }
          if (
            p?.requiredApproverCount !== undefined &&
            (!Number.isInteger(p.requiredApproverCount) || p.requiredApproverCount < 1)
          ) {
            errors.push({ field: `${path}.requiredApproverCount`, code: 'RANGE' });
          }
          if (Array.isArray(p?.requiredRoleCodes) && p.requiredRoleCodes.length > MAX_ROLE_CODES) {
            errors.push({ field: `${path}.requiredRoleCodes`, code: 'RANGE' });
          }
        });
      }
      if (errors.length > 0) return validationFailed(api, errors);

      // Two policies for the same action and scope may not cover the same day: leaving the
      // question of how many approvals an amount needs with two answers is worse than
      // refusing the write.
      for (let i = 0; i < policies!.length; i += 1) {
        for (let j = i + 1; j < policies!.length; j += 1) {
          const a = policies![i]!;
          const b = policies![j]!;
          if (a.scopeCode !== b.scopeCode) continue;
          if (periodsOverlap(a.validFrom, a.validTo ?? null, b.validFrom, b.validTo ?? null)) {
            return problem(
              api,
              409,
              'APPROVAL_POLICY_OVERLAP',
              'Aynı kapsam için dönemler çakışıyor',
            );
          }
        }
      }

      const previous = world().approvalPolicies.filter(
        (p) => p.tenantId === g.tenantId && p.actionCode === body!.actionCode,
      );
      const versionNo = previous.reduce((max, p) => Math.max(max, p.versionNo), 0) + 1;
      // A replace rather than a merge: the set is read as a whole, and a merge would leave
      // behind a band the caller believed it had removed. An empty array is a legitimate
      // request — this action needs no policy at all.
      world().approvalPolicies = world().approvalPolicies.filter(
        (p) => !(p.tenantId === g.tenantId && p.actionCode === body!.actionCode),
      );
      const now = new Date().toISOString();
      for (const input of policies!) {
        const row: StoredApprovalPolicy = {
          tenantId: g.tenantId,
          id: world().nextId(),
          actionCode: body!.actionCode,
          scopeCode: input.scopeCode,
          versionNo,
          minAmount: input.minAmount ?? null,
          maxAmount: input.maxAmount ?? null,
          requiredRoleCodes: input.requiredRoleCodes ?? [],
          requiredApproverCount: input.requiredApproverCount ?? 1,
          validFrom: input.validFrom,
          validTo: input.validTo ?? null,
          rowVersion: 1,
          createdAt: now,
        };
        world().approvalPolicies.push(row);
      }
      const list: Schemas['ApprovalPolicyList'] = {
        items: world()
          .approvalPolicies.filter(
            (p) => p.tenantId === g.tenantId && p.actionCode === body!.actionCode,
          )
          .sort(
            (a, b) =>
              a.scopeCode.localeCompare(b.scopeCode) || a.validFrom.localeCompare(b.validFrom),
          )
          .map(toApprovalPolicy),
      };
      return HttpResponse.json(list);
    }),
  ];
}
