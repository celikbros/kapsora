/**
 * End-to-end flows through the typed client against the M4 mock world: the service
 * request lifecycle, the worklist, the document pipeline and the notification log.
 *
 * These are the behaviours a screen leans on, and a divergence between the mock and the
 * Go server is a screen that passes its tests and fails in production. Each test below is
 * written against something the server does that a plausible mock would get wrong: the
 * submit that never rests at SUBMITTED, the claim that names who won the race, the SLA
 * that does not move when its queue does, the download refused until a scan clears it,
 * and the message that was deliberately not sent and says so.
 */
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { createKapsoraClient, randomId, type KapsoraClient } from '../client';
import { createOperations } from '../operations';
import { ApiError, unwrap, type Problem } from '../problem';
import { createMockServer } from './node';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

/**
 * The M4 surface has no operations wrapper yet, so these flows go through the generated
 * client directly. That is deliberate: it is the same typed contract a screen will use,
 * and nothing here can accidentally assert a shape the contract does not declare.
 */
function client(): KapsoraClient {
  return createKapsoraClient({ baseUrl: BASE, csrfToken: () => api.session?.csrfToken ?? null });
}

interface Session {
  c: KapsoraClient;
  tenantId: string;
  actorId: string;
}

async function signIn(username: string): Promise<Session> {
  const c = client();
  const o = createOperations(c);
  await o.session.login(username, PASSWORD);
  const session = await o.session.get();
  const tenantId = session.activeTenantId ?? (await o.session.tenants())[0]!.id;
  if (!session.activeTenantId) await o.session.switchTenant(tenantId);
  return { c, tenantId, actorId: session.actorId };
}

/** Idempotency keys are bounded 16-128 characters, so a short one is not a valid key. */
const key = (): string => `mock-${randomId()}`;

/** The problem document behind a rejected call, or a failure naming what happened. */
async function refusal(call: Promise<unknown>): Promise<Problem> {
  try {
    await call;
  } catch (error) {
    if (error instanceof ApiError) return error.problem;
    throw error;
  }
  throw new Error('expected the call to be refused');
}

const tenant = (s: Session) => ({ 'X-Tenant-ID': s.tenantId });

// --- fixture lookups, so no test depends on a generated id -------------------------------

function definitionByCode(code: string) {
  const row = api.world.serviceDefinitions.find((d) => d.code === code);
  if (!row) throw new Error(`fixture: no service definition ${code}`);
  return row;
}
function queueByCode(code: string) {
  const row = api.world.workQueues.find((q) => q.code === code);
  if (!row) throw new Error(`fixture: no work queue ${code}`);
  return row;
}
function itemByTitle(title: string) {
  const row = api.world.workItems.find((i) => i.title === title);
  if (!row) throw new Error(`fixture: no work item ${title}`);
  return row;
}
function documentByName(filename: string) {
  const row = api.world.documents.find((d) => d.originalFilename === filename);
  if (!row) throw new Error(`fixture: no document ${filename}`);
  return row;
}
function messageWithReason(reason: string) {
  const row = api.world.notificationMessages.find((m) => m.suppressedReason === reason);
  if (!row) throw new Error(`fixture: no message suppressed with ${reason}`);
  return row;
}
/**
 * An enrollment whose person holds exactly one. Two active enrollments in the same
 * program is itself a REVIEW_REQUIRED answer from the resolver, so a draft raised against
 * one could never reach the gate's APPROVED branch.
 */
function theEnrollment(tenantId: string) {
  const row = api.world.enrollments.find(
    (e) =>
      e.tenantId === tenantId &&
      api.world.enrollments.filter((o) => o.personId === e.personId).length === 1,
  );
  if (!row) throw new Error('fixture: no singly-enrolled person');
  return row;
}
function theProviderOrganizationId(): string {
  const provider = api.world.accounts.find((a) => a.username === 'provider.a')!;
  const grant = provider.memberships[0]!.scopes!.find((g) => g.type === 'ORGANIZATION')!;
  return grant.id!;
}

/** Opens a draft the way a screen would, and hands back its id and current ETag. */
async function createDraft(
  s: Session,
  over: Partial<{
    requestType: 'DIRECT_SERVICE' | 'PREAUTHORIZATION' | 'RESERVATION' | 'REIMBURSEMENT';
    quantity: string;
    amount: string;
    definitionCode: string;
  }> = {},
): Promise<{ id: string; etag: string }> {
  const enrollment = theEnrollment(s.tenantId);
  const definition = definitionByCode(over.definitionCode ?? 'PHYSIO_SESSION');
  const created = await unwrap(
    s.c.POST('/api/v1/service-requests', {
      params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
      body: {
        requestType: over.requestType ?? 'DIRECT_SERVICE',
        channel: 'BACKOFFICE',
        personId: enrollment.personId,
        programId: enrollment.programId,
        enrollmentId: enrollment.id,
        providerOrganizationId: theProviderOrganizationId(),
        serviceDate: '2026-08-15',
        items: [
          {
            serviceDefinitionId: definition.id,
            unitType: definition.defaultUnitType,
            requestedQuantity: over.quantity ?? '2.000000',
            requestedAmount: over.amount ?? '1200.000000',
            currencyCode: 'TRY',
          },
        ],
      },
    }),
  );
  return { id: created.data.id, etag: created.response.headers.get('ETag')! };
}

async function submit(s: Session, id: string, etag: string) {
  return unwrap(
    s.c.POST('/api/v1/service-requests/{requestId}/submit', {
      params: {
        header: { ...tenant(s), 'Idempotency-Key': key(), 'If-Match': etag },
        path: { requestId: id },
      },
      body: {},
    }),
  );
}

describe('service requests: the world', () => {
  it('carries a request in every stored status and never one resting at SUBMITTED', async () => {
    const s = await signIn('admin.a');
    const page = await unwrap(
      s.c.GET('/api/v1/service-requests', { params: { header: tenant(s), query: { limit: 200 } } }),
    );
    const statuses = new Set(page.data.items.map((r) => r.status));
    for (const status of [
      'DRAFT',
      'ELIGIBILITY_FAILED',
      'PENDING_DOCUMENT',
      'PENDING_REVIEW',
      'APPROVED',
      'PARTIALLY_APPROVED',
      'REJECTED',
      'CANCELLED',
      'EXPIRED',
      'CLOSED',
    ]) {
      expect(statuses).toContain(status);
    }
    // The server passes through SUBMITTED inside the submit transaction; no row is ever
    // observably in it, so a screen never has to render one.
    expect(statuses).not.toContain('SUBMITTED');
  });

  it('honours the invariants the migration checks, and keeps money as exact strings', async () => {
    const s = await signIn('admin.a');
    const page = await unwrap(
      s.c.GET('/api/v1/service-requests', { params: { header: tenant(s), query: { limit: 200 } } }),
    );
    for (const r of page.data.items) {
      if (r.status === 'REJECTED') expect(r.rejectReasonCode).toBeTruthy();
      if (r.status === 'PENDING_DOCUMENT') {
        expect(r.requiredDocumentTypes?.length ?? 0).toBeGreaterThan(0);
      }
      for (const item of r.items) {
        expect(typeof item.requestedQuantity).toBe('string');
        if (item.requestedAmount != null) expect(typeof item.requestedAmount).toBe('string');
        if (item.approvedAmount != null) expect(typeof item.approvedAmount).toBe('string');
      }
    }
    // A request that has never been submitted has not been asked; one the rules cleared
    // has been asked and needed nothing. The two are different answers.
    const draft = page.data.items.find((r) => r.status === 'DRAFT' && r.currentVersionNo === 1);
    expect(draft?.requiredDocumentTypes ?? null).toBeNull();

    // And one the fixture sent back is a draft again, on version 2, naming why.
    const corrected = page.data.items.find((r) => r.status === 'DRAFT' && r.currentVersionNo === 2);
    expect(corrected?.returnReasonCode).toBe('MISSING_INVOICE');
    const history = await unwrap(
      s.c.GET('/api/v1/service-requests/{requestId}/versions', {
        params: { header: tenant(s), path: { requestId: corrected!.id } },
      }),
    );
    expect(history.data.items.map((v) => v.status)).toEqual(['DRAFT', 'SUPERSEDED']);
  });
});

describe('service requests: the lifecycle', () => {
  it('refuses a status written through the draft patch, and a body of the wrong media type', async () => {
    const s = await signIn('admin.a');
    const { id, etag } = await createDraft(s);
    const immutable = await refusal(
      unwrap(
        s.c.PATCH('/api/v1/service-requests/{requestId}', {
          params: { header: { ...tenant(s), 'If-Match': etag }, path: { requestId: id } },
          body: { status: 'APPROVED' } as never,
          headers: { 'Content-Type': 'application/merge-patch+json' },
          bodySerializer: (b) => JSON.stringify(b),
        }),
      ),
    );
    expect(immutable.status).toBe(422);
    expect(immutable.errors?.[0]).toMatchObject({ field: 'status', code: 'IMMUTABLE' });

    // The media type is checked before the If-Match, exactly as the server checks it.
    const wrongType = await refusal(
      unwrap(
        s.c.PATCH('/api/v1/service-requests/{requestId}', {
          params: { header: { ...tenant(s), 'If-Match': etag }, path: { requestId: id } },
          body: { serviceDate: '2026-08-20' },
        }),
      ),
    );
    expect(wrongType.status).toBe(415);
    expect(wrongType.code).toBe('UNSUPPORTED_MEDIA_TYPE');
  });

  it('replaces the draft lines and moves the ETag even though the header did not change', async () => {
    const s = await signIn('admin.a');
    const { id, etag } = await createDraft(s);
    const definition = definitionByCode('GP_VISIT');
    const replaced = await unwrap(
      s.c.PUT('/api/v1/service-requests/{requestId}/items', {
        params: { header: { ...tenant(s), 'If-Match': etag }, path: { requestId: id } },
        body: {
          items: [
            {
              serviceDefinitionId: definition.id,
              unitType: 'COUNT',
              requestedQuantity: '1.000000',
              requestedAmount: '900.000000',
              currencyCode: 'TRY',
            },
          ],
        },
      }),
    );
    expect(replaced.data.items).toHaveLength(1);
    expect(replaced.data.items[0]!.serviceDefinitionId).toBe(definition.id);
    // A caller holding the old tag wrote against a line set that no longer exists.
    expect(replaced.response.headers.get('ETag')).not.toBe(etag);

    const stale = await refusal(
      unwrap(
        s.c.PUT('/api/v1/service-requests/{requestId}/items', {
          params: { header: { ...tenant(s), 'If-Match': etag }, path: { requestId: id } },
          body: { items: [] },
        }),
      ),
    );
    expect(stale.status).toBe(412);
    expect(stale.code).toBe('ETAG_MISMATCH');
  });

  it('needs an If-Match and an Idempotency-Key on the submit', async () => {
    const s = await signIn('admin.a');
    const { id, etag } = await createDraft(s);
    const noKey = await refusal(
      unwrap(
        s.c.POST('/api/v1/service-requests/{requestId}/submit', {
          params: {
            header: { ...tenant(s), 'If-Match': etag, 'Idempotency-Key': '' },
            path: { requestId: id },
          },
          body: {},
        }),
      ),
    );
    expect(noKey.status).toBe(400);
    expect(noKey.code).toBe('IDEMPOTENCY_KEY_REQUIRED');

    const shortKey = await refusal(
      unwrap(
        s.c.POST('/api/v1/service-requests/{requestId}/submit', {
          params: {
            header: { ...tenant(s), 'If-Match': etag, 'Idempotency-Key': 'short' },
            path: { requestId: id },
          },
          body: {},
        }),
      ),
    );
    expect(shortKey.code).toBe('IDEMPOTENCY_KEY_INVALID');

    const noMatch = await refusal(
      unwrap(
        s.c.POST('/api/v1/service-requests/{requestId}/submit', {
          params: {
            header: { ...tenant(s), 'If-Match': '', 'Idempotency-Key': key() },
            path: { requestId: id },
          },
          body: {},
        }),
      ),
    );
    expect(noMatch.status).toBe(428);
    expect(noMatch.code).toBe('IF_MATCH_REQUIRED');
  });

  it('lands a submit on the gate outcome and never on SUBMITTED', async () => {
    const s = await signIn('admin.a');

    // Nothing objected and the program does not want a second pair of eyes.
    const approved = await createDraft(s);
    const a = await submit(s, approved.id, approved.etag);
    expect(a.data.status).toBe('APPROVED');
    // Asked and required nothing, which is not the same as never asked.
    expect(a.data.requiredDocumentTypes).toEqual([]);
    expect(a.data.eligibilityEvaluationId).toBeTruthy();

    // A preauthorization needs paperwork before anybody can review it.
    const preauth = await createDraft(s, { requestType: 'PREAUTHORIZATION' });
    const p = await submit(s, preauth.id, preauth.etag);
    expect(p.data.status).toBe('PENDING_DOCUMENT');
    expect(p.data.requiredDocumentTypes).toEqual(['INVOICE', 'MEDICAL_REPORT']);

    // A large total is a rule asking for a person.
    const review = await createDraft(s, { amount: '25000.000000' });
    const r = await submit(s, review.id, review.etag);
    expect(r.data.status).toBe('PENDING_REVIEW');
    expect(r.data.ruleEvaluationId).toBeTruthy();

    // More sessions than the balance holds, and the balance does not allow an overdraft.
    const failed = await createDraft(s, { quantity: '400.000000' });
    const f = await submit(s, failed.id, failed.etag);
    expect(f.data.status).toBe('ELIGIBILITY_FAILED');
    // The rules were never asked, so the list is null rather than empty.
    expect(f.data.requiredDocumentTypes ?? null).toBeNull();
    expect(f.data.ruleEvaluationId ?? null).toBeNull();

    for (const outcome of [a, p, r, f]) {
      expect(outcome.data.status).not.toBe('SUBMITTED');
      expect(outcome.data.submittedAt).toBeTruthy();
    }
  });

  it('corrects a returned request in a new version and freezes the one that was decided', async () => {
    const s = await signIn('admin.a');
    const { id, etag } = await createDraft(s, { amount: '25000.000000' });
    const submitted = await submit(s, id, etag);
    expect(submitted.data.status).toBe('PENDING_REVIEW');

    const returned = await unwrap(
      s.c.POST('/api/v1/service-requests/{requestId}/return', {
        params: {
          header: {
            ...tenant(s),
            'Idempotency-Key': key(),
            'If-Match': submitted.response.headers.get('ETag')!,
          },
          path: { requestId: id },
        },
        body: { reasonCode: 'MISSING_INVOICE', reasonText: 'Fatura okunaksız.' },
      }),
    );
    // A return is not a rejection: the reference survives and the request is editable again.
    expect(returned.data.status).toBe('DRAFT');
    expect(returned.data.reference).toBe(submitted.data.reference);
    expect(returned.data.currentVersionNo).toBe(2);
    expect(returned.data.returnReasonCode).toBe('MISSING_INVOICE');

    const versions = await unwrap(
      s.c.GET('/api/v1/service-requests/{requestId}/versions', {
        params: { header: tenant(s), path: { requestId: id } },
      }),
    );
    expect(versions.data.items.map((v) => v.versionNo)).toEqual([2, 1]);
    expect(versions.data.items[0]!.status).toBe('DRAFT');
    expect(versions.data.items[1]!.status).toBe('SUPERSEDED');
    expect(versions.data.items[1]!.returnReasonCode).toBe('MISSING_INVOICE');

    // Version 1 answers from the snapshot frozen at submit, so what was decided against
    // stays readable however the request moved on.
    const frozen = await unwrap(
      s.c.GET('/api/v1/service-requests/{requestId}/versions/{versionNo}', {
        params: { header: tenant(s), path: { requestId: id, versionNo: 1 } },
      }),
    );
    expect(frozen.data.items[0]!.requestedAmount).toBe('25000.000000');
    expect(frozen.data.submittedAt).toBeTruthy();
  });

  it('answers a command the status does not allow with its own conflict code', async () => {
    const s = await signIn('admin.a');
    const { id, etag } = await createDraft(s);
    // Approving a draft is not a move this table has.
    const early = await refusal(
      unwrap(
        s.c.POST('/api/v1/service-requests/{requestId}/approve', {
          params: {
            header: { ...tenant(s), 'Idempotency-Key': key(), 'If-Match': etag },
            path: { requestId: id },
          },
          body: { reasonCode: 'WITHIN_PLAN' },
        }),
      ),
    );
    expect(early.status).toBe(409);
    expect(early.code).toBe('REQUEST_TRANSITION_INVALID');

    // Editing a request that is no longer a draft is an immutable version, not a
    // transition problem: the version has been submitted and decisions were made on it.
    const submitted = await submit(s, id, etag);
    const frozen = await refusal(
      unwrap(
        s.c.PUT('/api/v1/service-requests/{requestId}/items', {
          params: {
            header: { ...tenant(s), 'If-Match': submitted.response.headers.get('ETag')! },
            path: { requestId: id },
          },
          body: { items: [] },
        }),
      ),
    );
    expect(frozen.status).toBe(409);
    expect(frozen.code).toBe('SERVICE_REQUEST_VERSION_IMMUTABLE');
  });

  it('refuses a partial approval that approved everything, then decides the lines', async () => {
    const s = await signIn('admin.a');
    const { id, etag } = await createDraft(s, { amount: '25000.000000' });
    const submitted = await submit(s, id, etag);
    const tag = submitted.response.headers.get('ETag')!;

    const notPartial = await refusal(
      unwrap(
        s.c.POST('/api/v1/service-requests/{requestId}/partially-approve', {
          params: {
            header: { ...tenant(s), 'Idempotency-Key': key(), 'If-Match': tag },
            path: { requestId: id },
          },
          body: {
            reasonCode: 'WITHIN_PLAN',
            items: [{ lineNo: 1, status: 'APPROVED' }],
          },
        }),
      ),
    );
    expect(notPartial.status).toBe(422);
    expect(notPartial.errors?.[0]).toMatchObject({ field: 'items', code: 'NOT_PARTIAL' });

    const tooMuch = await refusal(
      unwrap(
        s.c.POST('/api/v1/service-requests/{requestId}/partially-approve', {
          params: {
            header: { ...tenant(s), 'Idempotency-Key': key(), 'If-Match': tag },
            path: { requestId: id },
          },
          body: {
            reasonCode: 'WITHIN_PLAN',
            items: [{ lineNo: 1, status: 'PARTIALLY_APPROVED', approvedQuantity: '99.000000' }],
          },
        }),
      ),
    );
    expect(tooMuch.errors?.[0]).toMatchObject({
      field: 'items[0].approvedQuantity',
      code: 'RANGE',
    });

    const decided = await unwrap(
      s.c.POST('/api/v1/service-requests/{requestId}/partially-approve', {
        params: {
          header: { ...tenant(s), 'Idempotency-Key': key(), 'If-Match': tag },
          path: { requestId: id },
        },
        body: {
          reasonCode: 'SESSION_CAP',
          items: [{ lineNo: 1, status: 'PARTIALLY_APPROVED', approvedQuantity: '1.000000' }],
        },
      }),
    );
    expect(decided.data.status).toBe('PARTIALLY_APPROVED');
    expect(decided.data.items[0]!.approvedQuantity).toBe('1.000000');
    expect(decided.data.items[0]!.decisionReasonCode).toBe('SESSION_CAP');
  });

  it('keeps a reviewer out of the commands it does not hold', async () => {
    const s = await signIn('reviewer.a');
    const denied = await refusal(
      unwrap(
        s.c.POST('/api/v1/service-requests', {
          params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
          body: {
            requestType: 'DIRECT_SERVICE',
            channel: 'BACKOFFICE',
            personId: api.world.people[0]!.id,
            programId: api.world.programs[0]!.id,
            enrollmentId: theEnrollment(s.tenantId).id,
            serviceDate: '2026-08-15',
            items: [],
          },
        }),
      ),
    );
    expect(denied.status).toBe(403);
    expect(denied.code).toBe('PERMISSION_DENIED');
    expect(denied.detail).toBe('service_request.create');
  });
});

describe('service requests: the provider boundary', () => {
  it('shows a provider only its own requests, and answers 404 for anybody else’s', async () => {
    const admin = await signIn('admin.a');
    const all = await unwrap(
      admin.c.GET('/api/v1/service-requests', {
        params: { header: tenant(admin), query: { limit: 200 } },
      }),
    );
    const own = theProviderOrganizationId();
    const foreign = all.data.items.find(
      (r) => r.providerOrganizationId !== null && r.providerOrganizationId !== own,
    );
    const tenantSide = all.data.items.find((r) => (r.providerOrganizationId ?? null) === null);
    expect(foreign).toBeDefined();
    expect(tenantSide).toBeDefined();

    const provider = await signIn('provider.a');
    const mine = await unwrap(
      provider.c.GET('/api/v1/service-requests', {
        params: { header: tenant(provider), query: { limit: 200 } },
      }),
    );
    expect(mine.data.items.length).toBeGreaterThan(0);
    // The boundary is applied where the rows are read, so it can never disagree with
    // itself: what is off the page is unreachable by id too.
    for (const r of mine.data.items) expect(r.providerOrganizationId).toBe(own);

    for (const hidden of [foreign!, tenantSide!]) {
      const missing = await refusal(
        unwrap(
          provider.c.GET('/api/v1/service-requests/{requestId}', {
            params: { header: tenant(provider), path: { requestId: hidden.id } },
          }),
        ),
      );
      expect(missing.status).toBe(404);
      expect(missing.code).toBe('SERVICE_REQUEST_NOT_FOUND');
    }
  });

  it('refuses a provider raising a request for an organization it does not hold', async () => {
    const provider = await signIn('provider.a');
    const other = api.world.relationships.find(
      (r) =>
        r.tenantId === provider.tenantId &&
        r.relationshipRole === 'PROVIDER' &&
        r.id !== theProviderOrganizationId(),
    )!;
    const enrollment = theEnrollment(provider.tenantId);
    const definition = definitionByCode('PHYSIO_SESSION');
    const denied = await refusal(
      unwrap(
        provider.c.POST('/api/v1/service-requests', {
          params: { header: { ...tenant(provider), 'Idempotency-Key': key() } },
          body: {
            requestType: 'DIRECT_SERVICE',
            channel: 'PROVIDER_PORTAL',
            personId: enrollment.personId,
            programId: enrollment.programId,
            enrollmentId: enrollment.id,
            providerOrganizationId: other.id,
            serviceDate: '2026-08-15',
            items: [
              {
                serviceDefinitionId: definition.id,
                unitType: 'SESSION',
                requestedQuantity: '1.000000',
              },
            ],
          },
        }),
      ),
    );
    // Named an organization it does not hold, so this one is a 403 and not a 404: the
    // caller said which organization it meant.
    expect(denied.status).toBe(403);
    expect(denied.code).toBe('SERVICE_REQUEST_PROVIDER_SCOPE');
  });
});

describe('worklist', () => {
  it('names the actor who already holds an item a claim lost the race for', async () => {
    const s = await signIn('admin.a');
    const held = itemByTitle('Başkasının üstlendiği inceleme');
    const conflict = await refusal(
      unwrap(
        s.c.POST('/api/v1/work-items/{workItemId}/claim', {
          params: {
            header: {
              ...tenant(s),
              'Idempotency-Key': key(),
              'If-Match': `"${held.rowVersion}"`,
            },
            path: { workItemId: held.id },
          },
        }),
      ),
    );
    expect(conflict.status).toBe(409);
    expect(conflict.code).toBe('WORK_ITEM_ALREADY_CLAIMED');
    // The screen has to be able to say who won. The detail is the sentence a person reads
    // and now names the holder rather than their uuid; the RFC 9457 extension members
    // carry the same fact in a form the screen reads without parsing Turkish (WP-I5-05
    // section 2.6). Both are asserted, because a screen that scraped the sentence would
    // break the day the sentence changed.
    const holder = api.world.accounts.find((a) => a.actorId === held.assigneeActorId);
    expect(conflict.detail).toContain(holder!.displayName);
    const extended = conflict as unknown as Record<string, unknown>;
    expect(extended.assigneeActorId).toBe(held.assigneeActorId);
    expect(extended.assigneeDisplayName).toBe(holder!.displayName);
  });

  it('claims an open item, and refuses a release from somebody who does not hold it', async () => {
    const s = await signIn('admin.a');
    const open = itemByTitle('Bekleyen inceleme');
    const claimed = await unwrap(
      s.c.POST('/api/v1/work-items/{workItemId}/claim', {
        params: {
          header: { ...tenant(s), 'Idempotency-Key': key(), 'If-Match': `"${open.rowVersion}"` },
          path: { workItemId: open.id },
        },
      }),
    );
    expect(claimed.data.status).toBe('CLAIMED');
    expect(claimed.data.assigneeActorId).toBe(s.actorId);
    const tag = claimed.response.headers.get('ETag')!;

    const other = itemByTitle('Başkasının üstlendiği inceleme');
    const notMine = await refusal(
      unwrap(
        s.c.POST('/api/v1/work-items/{workItemId}/release', {
          params: {
            header: {
              ...tenant(s),
              'Idempotency-Key': key(),
              'If-Match': `"${other.rowVersion}"`,
            },
            path: { workItemId: other.id },
          },
          body: {},
        }),
      ),
    );
    expect(notMine.status).toBe(409);
    expect(notMine.code).toBe('WORK_ITEM_NOT_ASSIGNEE');

    const released = await unwrap(
      s.c.POST('/api/v1/work-items/{workItemId}/release', {
        params: {
          header: { ...tenant(s), 'Idempotency-Key': key(), 'If-Match': tag },
          path: { workItemId: open.id },
        },
        body: { reasonCode: 'REASSIGNED' },
      }),
    );
    expect(released.data.status).toBe('OPEN');
    expect(released.data.assigneeActorId ?? null).toBeNull();
  });

  it('leaves an item on the clock it was given when its queue is repointed', async () => {
    const s = await signIn('admin.a');
    const queue = queueByCode('HEALTH_REVIEW');
    const before = await unwrap(
      s.c.GET('/api/v1/work-items', {
        params: { header: tenant(s), query: { queueId: queue.id, limit: 200 } },
      }),
    );
    const clocks = before.data.items.map((i) => [i.id, i.dueAt, i.slaMinutesSnapshot] as const);
    expect(clocks.length).toBeGreaterThan(0);

    await unwrap(
      s.c.PATCH('/api/v1/work-queues/{queueId}', {
        params: {
          header: { ...tenant(s), 'If-Match': `"${queue.rowVersion}"` },
          path: { queueId: queue.id },
        },
        body: { slaMinutes: 15 },
        headers: { 'Content-Type': 'application/merge-patch+json' },
        bodySerializer: (b) => JSON.stringify(b),
      }),
    );

    const after = await unwrap(
      s.c.GET('/api/v1/work-items', {
        params: { header: tenant(s), query: { queueId: queue.id, limit: 200 } },
      }),
    );
    const byId = new Map(after.data.items.map((i) => [i.id, i]));
    for (const [id, dueAt, sla] of clocks) {
      // The SLA is a snapshot: an item is judged by the clock it was handed, and nothing
      // moves it afterwards.
      expect(byId.get(id)!.dueAt ?? null).toBe(dueAt ?? null);
      expect(byId.get(id)!.slaMinutesSnapshot ?? null).toBe(sla ?? null);
    }
  });

  it('answers the morning list: what is late and what is mine', async () => {
    const s = await signIn('admin.a');
    const overdue = await unwrap(
      s.c.GET('/api/v1/work-items', {
        params: { header: tenant(s), query: { overdue: true, limit: 200 } },
      }),
    );
    expect(overdue.data.items.length).toBeGreaterThan(0);
    const now = new Date().toISOString();
    for (const i of overdue.data.items) expect(i.dueAt! < now).toBe(true);

    const mine = await unwrap(
      s.c.GET('/api/v1/work-items', {
        params: { header: tenant(s), query: { assignedToMe: true, limit: 200 } },
      }),
    );
    expect(mine.data.items.length).toBeGreaterThan(0);
    // Resolved against the caller, never against an actor id somebody could supply.
    for (const i of mine.data.items) expect(i.assigneeActorId).toBe(s.actorId);
  });

  it('completes an item with an outcome and stores the note as an internal comment', async () => {
    const s = await signIn('admin.a');
    const mine = itemByTitle('Üstlendiğim inceleme');
    const noOutcome = await refusal(
      unwrap(
        s.c.POST('/api/v1/work-items/{workItemId}/complete', {
          params: {
            header: {
              ...tenant(s),
              'Idempotency-Key': key(),
              'If-Match': `"${mine.rowVersion}"`,
            },
            path: { workItemId: mine.id },
          },
          body: { outcomeCode: '' },
        }),
      ),
    );
    expect(noOutcome.status).toBe(422);
    expect(noOutcome.errors?.[0]?.field).toBe('outcomeCode');

    const done = await unwrap(
      s.c.POST('/api/v1/work-items/{workItemId}/complete', {
        params: {
          header: { ...tenant(s), 'Idempotency-Key': key(), 'If-Match': `"${mine.rowVersion}"` },
          path: { workItemId: mine.id },
        },
        body: { outcomeCode: 'APPROVED', comment: 'Belgeler tamam.' },
      }),
    );
    expect(done.data.status).toBe('COMPLETED');
    expect(done.data.outcomeCode).toBe('APPROVED');

    const internal = await unwrap(
      s.c.GET('/api/v1/work-items/{workItemId}/comments', {
        params: {
          header: tenant(s),
          path: { workItemId: mine.id },
          query: { visibility: ['INTERNAL'] },
        },
      }),
    );
    expect(internal.data.items.map((c) => c.body)).toContain('Belgeler tamam.');
    for (const c of internal.data.items) expect(c.visibility).toBe('INTERNAL');
  });

  it('narrows the comments to one audience so a provider screen is not handed the rest', async () => {
    const s = await signIn('admin.a');
    const item = itemByTitle('Başkasının üstlendiği inceleme');
    const all = await unwrap(
      s.c.GET('/api/v1/work-items/{workItemId}/comments', {
        params: { header: tenant(s), path: { workItemId: item.id } },
      }),
    );
    expect(all.data.items.length).toBeGreaterThan(1);
    const provider = await unwrap(
      s.c.GET('/api/v1/work-items/{workItemId}/comments', {
        params: {
          header: tenant(s),
          path: { workItemId: item.id },
          query: { visibility: ['PROVIDER'] },
        },
      }),
    );
    expect(provider.data.items.every((c) => c.visibility === 'PROVIDER')).toBe(true);
    expect(provider.data.items.length).toBeLessThan(all.data.items.length);
  });

  it('replaces a policy set as a whole and refuses two bands covering the same day', async () => {
    const s = await signIn('admin.a');
    const overlap = await refusal(
      unwrap(
        s.c.PUT('/api/v1/approval-policies', {
          params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
          body: {
            actionCode: 'service_request.approve',
            policies: [
              { scopeCode: 'STANDARD', validFrom: '2026-01-01', minAmount: '0.000000' },
              { scopeCode: 'STANDARD', validFrom: '2026-06-01', maxAmount: '100.000000' },
            ],
          },
        }),
      ),
    );
    expect(overlap.status).toBe(409);
    expect(overlap.code).toBe('APPROVAL_POLICY_OVERLAP');

    const written = await unwrap(
      s.c.PUT('/api/v1/approval-policies', {
        params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
        body: {
          actionCode: 'service_request.approve',
          policies: [
            {
              scopeCode: 'ONLY',
              validFrom: '2026-01-01',
              minAmount: '0.000000',
              maxAmount: '5000.000000',
              requiredApproverCount: 2,
              requiredRoleCodes: ['REVIEWER'],
            },
          ],
        },
      }),
    );
    // A replace, not a merge: the bands that were there are gone.
    expect(written.data.items.map((p) => p.scopeCode)).toEqual(['ONLY']);
    expect(written.data.items[0]!.maxAmount).toBe('5000.000000');
    expect(typeof written.data.items[0]!.minAmount).toBe('string');
  });
});

describe('documents', () => {
  it('refuses every download but a clean one, each with its own reason', async () => {
    const s = await signIn('admin.a');
    const cases: [string, string][] = [
      ['yeni-fatura.pdf', 'DOCUMENT_NOT_SCANNED'],
      ['taslak.pdf', 'DOCUMENT_NOT_SCANNED'],
      ['bozuk.pdf', 'DOCUMENT_NOT_SCANNED'],
      ['makbuz.pdf', 'DOCUMENT_INFECTED'],
      ['eski-rapor.pdf', 'DOCUMENT_PURGED'],
    ];
    for (const [filename, code] of cases) {
      const doc = documentByName(filename);
      const refused = await refusal(
        unwrap(
          s.c.POST('/api/v1/documents/{documentId}/download', {
            params: { header: tenant(s), path: { documentId: doc.id } },
            body: { purposeCode: 'CLAIM_REVIEW' },
          }),
        ),
      );
      expect(refused.status).toBe(409);
      expect(refused.code).toBe(code);
    }

    const clean = documentByName('fatura-2026-03.pdf');
    const url = await unwrap(
      s.c.POST('/api/v1/documents/{documentId}/download', {
        params: { header: tenant(s), path: { documentId: clean.id } },
        body: { purposeCode: 'CLAIM_REVIEW', reasonText: 'Fatura kontrolü' },
      }),
    );
    expect(url.data.method).toBe('GET');
    expect(url.data.classification).toBe(clean.classification);
    expect(url.data.url).toContain('secure');
    // A bearer URL must not be cached by anything between here and the browser.
    expect(url.response.headers.get('Cache-Control')).toBe('no-store');
  });

  it('shows a document scanning and then its verdict, with downloadable moving with it', async () => {
    const s = await signIn('admin.a');
    const doc = documentByName('yeni-fatura.pdf');
    const scanning = await unwrap(
      s.c.GET('/api/v1/documents/{documentId}', {
        params: { header: tenant(s), path: { documentId: doc.id } },
      }),
    );
    expect(scanning.data.scanStatus).toBe('SCANNING');
    expect(scanning.data.bucket).toBe('quarantine');
    expect(scanning.data.downloadable).toBe(false);

    // Only the worker writes a verdict; no endpoint does, on the server or here.
    api.world.advanceScan(doc.id, 'CLEAN');
    const cleared = await unwrap(
      s.c.GET('/api/v1/documents/{documentId}', {
        params: { header: tenant(s), path: { documentId: doc.id } },
      }),
    );
    expect(cleared.data.scanStatus).toBe('CLEAN');
    expect(cleared.data.bucket).toBe('secure');
    expect(cleared.data.downloadable).toBe(true);
    await unwrap(
      s.c.POST('/api/v1/documents/{documentId}/download', {
        params: { header: tenant(s), path: { documentId: doc.id } },
        body: {},
      }),
    );
  });

  it('refuses a clinical attachment to a caller who may read documents but not those', async () => {
    const s = await signIn('admin.a');
    const clinical = documentByName('rapor.pdf');
    // The document is clean and in the secure bucket; the link is what refuses it.
    expect(clinical.scanStatus).toBe('CLEAN');
    const denied = await refusal(
      unwrap(
        s.c.POST('/api/v1/documents/{documentId}/download', {
          params: { header: tenant(s), path: { documentId: clinical.id } },
          body: { purposeCode: 'CLAIM_REVIEW' },
        }),
      ),
    );
    expect(denied.status).toBe(403);
    expect(denied.code).toBe('DOCUMENT_LINK_PERMISSION_DENIED');
    expect(denied.detail).toBe('health.clinical.read');
  });

  it('reserves an upload, records it once, and refuses a second completion', async () => {
    const s = await signIn('admin.a');
    const digest = 'a'.repeat(64);
    const reserved = await unwrap(
      s.c.POST('/api/v1/documents', {
        params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
        body: {
          originalFilename: 'C:\\Users\\biri\\yeni.pdf',
          contentType: 'application/pdf',
          byteSize: 4096,
          classification: 'PERSONAL',
        },
      }),
    );
    // Any directory part a browser sent is stripped, and it never reaches the object key.
    expect(reserved.data.document.originalFilename).toBe('yeni.pdf');
    expect(reserved.data.document.scanStatus).toBe('PENDING');
    expect(reserved.data.document.byteSize ?? null).toBeNull();
    expect(reserved.data.upload?.method).toBe('PUT');

    const id = reserved.data.document.id;
    const completed = await unwrap(
      s.c.POST('/api/v1/documents/{documentId}/complete', {
        params: { header: { ...tenant(s), 'Idempotency-Key': key() }, path: { documentId: id } },
        body: { sha256: digest, byteSize: 4096 },
      }),
    );
    expect(completed.data.scanStatus).toBe('SCANNING');
    expect(completed.data.downloadable).toBe(false);

    const again = await refusal(
      unwrap(
        s.c.POST('/api/v1/documents/{documentId}/complete', {
          params: { header: { ...tenant(s), 'Idempotency-Key': key() }, path: { documentId: id } },
          body: { sha256: digest, byteSize: 4096 },
        }),
      ),
    );
    expect(again.status).toBe(409);
    expect(again.code).toBe('DOCUMENT_ALREADY_COMPLETED');
  });

  it('hands a provider its own documents and the tenant’s, and no other provider’s', async () => {
    const provider = await signIn('provider.a');
    const own = theProviderOrganizationId();
    const page = await unwrap(
      provider.c.GET('/api/v1/documents', {
        params: { header: tenant(provider), query: { limit: 200 } },
      }),
    );
    expect(page.data.items.length).toBeGreaterThan(0);
    for (const d of page.data.items) {
      expect(d.ownerOrganizationId === null || d.ownerOrganizationId === own).toBe(true);
    }
    const foreign = documentByName('baska-saglayici-fatura.pdf');
    const missing = await refusal(
      unwrap(
        provider.c.GET('/api/v1/documents/{documentId}', {
          params: { header: tenant(provider), path: { documentId: foreign.id } },
        }),
      ),
    );
    expect(missing.status).toBe(404);
    expect(missing.code).toBe('DOCUMENT_NOT_FOUND');
  });

  it('places a legal hold once, and releases it once', async () => {
    const s = await signIn('admin.a');
    const doc = documentByName('fatura-2026-03.pdf');
    const placed = await unwrap(
      s.c.POST('/api/v1/legal-holds', {
        params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
        body: { documentId: doc.id, reason: 'Denetim talebi' },
      }),
    );
    const tag = placed.response.headers.get('ETag')!;
    expect(placed.data.releasedAt ?? null).toBeNull();

    const twice = await refusal(
      unwrap(
        s.c.POST('/api/v1/legal-holds', {
          params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
          body: { documentId: doc.id, reason: 'Yine' },
        }),
      ),
    );
    expect(twice.status).toBe(409);
    expect(twice.code).toBe('LEGAL_HOLD_EXISTS');

    const nothing = await refusal(
      unwrap(
        s.c.POST('/api/v1/legal-holds', {
          params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
          body: { reason: 'Hiçbir şeyi korumayan saklama' },
        }),
      ),
    );
    expect(nothing.status).toBe(422);

    const released = await unwrap(
      s.c.POST('/api/v1/legal-holds/{legalHoldId}/release', {
        params: {
          header: { ...tenant(s), 'Idempotency-Key': key(), 'If-Match': tag },
          path: { legalHoldId: placed.data.id },
        },
      }),
    );
    // Who lifted it and when are both recorded.
    expect(released.data.releasedAt).toBeTruthy();
    expect(released.data.releasedBy).toBe(s.actorId);

    const already = await refusal(
      unwrap(
        s.c.POST('/api/v1/legal-holds/{legalHoldId}/release', {
          params: {
            header: {
              ...tenant(s),
              'Idempotency-Key': key(),
              'If-Match': released.response.headers.get('ETag')!,
            },
            path: { legalHoldId: placed.data.id },
          },
        }),
      ),
    );
    expect(already.code).toBe('LEGAL_HOLD_ALREADY_RELEASED');
  });
});

describe('notifications', () => {
  it('logs a message nobody was sent, with the reason they were not', async () => {
    const s = await signIn('admin.a');
    const suppressed = await unwrap(
      s.c.GET('/api/v1/notification-messages', {
        params: { header: tenant(s), query: { status: 'SUPPRESSED', limit: 200 } },
      }),
    );
    const reasons = suppressed.data.items.map((m) => m.suppressedReason);
    expect(reasons).toContain('PREFERENCE_DISABLED');
    expect(reasons).toContain('QUIET_HOURS');
    expect(reasons).toContain('NO_TEMPLATE');
    // Suppression names its reason, and only a suppressed message has one: this is the
    // whole difference between "not told" and "nothing happened".
    for (const m of suppressed.data.items) expect(m.suppressedReason).toBeTruthy();

    const everything = await unwrap(
      s.c.GET('/api/v1/notification-messages', {
        params: { header: tenant(s), query: { limit: 200 } },
      }),
    );
    for (const m of everything.data.items) {
      if (m.status !== 'SUPPRESSED') expect(m.suppressedReason ?? null).toBeNull();
    }
  });

  it('shows every attempt made on a message, not just where it ended up', async () => {
    const s = await signIn('admin.a');
    const sent = api.world.notificationMessages.find((m) => m.status === 'SENT')!;
    const detail = await unwrap(
      s.c.GET('/api/v1/notification-messages/{messageId}', {
        params: { header: tenant(s), path: { messageId: sent.id } },
      }),
    );
    expect(detail.data.message.status).toBe('SENT');
    expect(detail.data.deliveries.map((d) => d.attemptNo)).toEqual([1, 2]);
    expect(detail.data.deliveries[0]!.outcome).toBe('ERROR');
    expect(detail.data.deliveries[1]!.outcome).toBe('ACCEPTED');
  });

  it('resends as a new message, and refuses one there is nothing to resend of', async () => {
    const s = await signIn('admin.a');
    const sent = api.world.notificationMessages.find((m) => m.status === 'SENT')!;
    const copy = await unwrap(
      s.c.POST('/api/v1/notification-messages/{messageId}/resend', {
        params: {
          header: { ...tenant(s), 'Idempotency-Key': key() },
          path: { messageId: sent.id },
        },
      }),
    );
    // The original is evidence of what happened; the copy names it and carries the text.
    expect(copy.data.id).not.toBe(sent.id);
    expect(copy.data.resentFromMessageId).toBe(sent.id);
    expect(copy.data.bodyRendered).toBe(sent.bodyRendered);
    expect(copy.data.status).toBe('QUEUED');

    const noText = messageWithReason('NO_TEMPLATE');
    const nothing = await refusal(
      unwrap(
        s.c.POST('/api/v1/notification-messages/{messageId}/resend', {
          params: {
            header: { ...tenant(s), 'Idempotency-Key': key() },
            path: { messageId: noText.id },
          },
        }),
      ),
    );
    expect(nothing.status).toBe(409);
    expect(nothing.code).toBe('NOTIFICATION_MESSAGE_NOT_RESENDABLE');
  });

  it('will not resend past somebody’s own answer to “do you want to hear about this”', async () => {
    const s = await signIn('admin.a');
    const person = api.world.notificationPreferences.find((p) => !p.enabled)!;
    // A SENT message on the channel the recipient turned off.
    const sent = api.world.notificationMessages.find((m) => m.status === 'SENT')!;
    sent.recipientType = person.recipientType;
    sent.recipientId = person.recipientId;
    sent.channel = person.channel;
    const denied = await refusal(
      unwrap(
        s.c.POST('/api/v1/notification-messages/{messageId}/resend', {
          params: {
            header: { ...tenant(s), 'Idempotency-Key': key() },
            path: { messageId: sent.id },
          },
        }),
      ),
    );
    expect(denied.status).toBe(409);
    expect(denied.code).toBe('NOTIFICATION_RECIPIENT_OPTED_OUT');
  });

  it('refuses a template whose declared variables and placeholders are not the same set', async () => {
    const s = await signIn('admin.a');
    const undeclared = await refusal(
      unwrap(
        s.c.POST('/api/v1/notification-templates', {
          params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
          body: {
            eventCode: 'service_request.returned',
            channel: 'SMS',
            locale: 'tr-TR',
            body: '{{reference_no}} numaralı talebiniz {{status_code}}.',
            declaredVariables: ['reference_no'],
          },
        }),
      ),
    );
    expect(undeclared.status).toBe(422);
    expect(undeclared.errors?.some((e) => e.code === 'NOT_DECLARED')).toBe(true);

    const unused = await refusal(
      unwrap(
        s.c.POST('/api/v1/notification-templates', {
          params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
          body: {
            eventCode: 'service_request.returned',
            channel: 'SMS',
            locale: 'tr-TR',
            body: '{{reference_no}} numaralı talebiniz güncellendi.',
            declaredVariables: ['reference_no', 'status_code'],
          },
        }),
      ),
    );
    expect(unused.errors?.some((e) => e.code === 'UNUSED')).toBe(true);

    // A variable outside the safe catalogue is refused rather than dropped.
    const unsafe = await refusal(
      unwrap(
        s.c.POST('/api/v1/notification-templates', {
          params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
          body: {
            eventCode: 'service_request.returned',
            channel: 'SMS',
            locale: 'tr-TR',
            body: '{{diagnosis}}',
            declaredVariables: ['diagnosis'] as never,
          },
        }),
      ),
    );
    expect(unsafe.errors?.some((e) => e.code === 'ENUM')).toBe(true);
  });

  it('publishes a draft and retires the version it replaces in the same step', async () => {
    const s = await signIn('admin.a');
    const created = await unwrap(
      s.c.POST('/api/v1/notification-templates', {
        params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
        body: {
          eventCode: 'service_request.decided',
          channel: 'EMAIL',
          locale: 'tr-TR',
          subject: 'Talebiniz hakkında',
          body: 'Sayın {{given_name}}, talebiniz {{status_code}} oldu.',
          declaredVariables: ['given_name', 'status_code'],
        },
      }),
    );
    expect(created.data.status).toBe('DRAFT');
    // The version number is computed, never supplied.
    expect(created.data.versionNo).toBe(3);

    const published = await unwrap(
      s.c.POST('/api/v1/notification-templates/{templateId}/publish', {
        params: {
          header: {
            ...tenant(s),
            'Idempotency-Key': key(),
            'If-Match': created.response.headers.get('ETag')!,
          },
          path: { templateId: created.data.id },
        },
      }),
    );
    expect(published.data.status).toBe('PUBLISHED');

    const all = await unwrap(
      s.c.GET('/api/v1/notification-templates', {
        params: {
          header: tenant(s),
          query: { eventCode: 'service_request.decided', channel: 'EMAIL', limit: 200 },
        },
      }),
    );
    // Exactly one published template per event, channel and language, and the versions it
    // replaced are all still readable.
    expect(all.data.items.filter((t) => t.status === 'PUBLISHED')).toHaveLength(1);
    expect(all.data.items.filter((t) => t.status === 'RETIRED').length).toBeGreaterThan(1);

    const again = await refusal(
      unwrap(
        s.c.POST('/api/v1/notification-templates/{templateId}/publish', {
          params: {
            header: {
              ...tenant(s),
              'Idempotency-Key': key(),
              'If-Match': published.response.headers.get('ETag')!,
            },
            path: { templateId: created.data.id },
          },
        }),
      ),
    );
    expect(again.status).toBe(409);
    expect(again.code).toBe('NOTIFICATION_TEMPLATE_NOT_DRAFT');
  });

  it('replaces a recipient’s preferences as a whole, and reads quiet hours back', async () => {
    const s = await signIn('admin.a');
    const existing = api.world.notificationPreferences[0]!;
    const before = await unwrap(
      s.c.GET('/api/v1/notification-preferences', {
        params: {
          header: tenant(s),
          query: { recipientType: existing.recipientType, recipientId: existing.recipientId },
        },
      }),
    );
    expect(before.data.items.length).toBe(2);
    expect(before.data.items.find((p) => p.channel === 'EMAIL')?.quietHoursStart).toBe('22:00');

    const halfHours = await refusal(
      unwrap(
        s.c.PUT('/api/v1/notification-preferences', {
          params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
          body: {
            recipientType: existing.recipientType,
            recipientId: existing.recipientId,
            preferences: [{ channel: 'EMAIL', enabled: true, quietHoursStart: '23:00' }],
          },
        }),
      ),
    );
    // Quiet hours are a pair or are absent: one edge on its own means nothing.
    expect(halfHours.status).toBe(422);

    const written = await unwrap(
      s.c.PUT('/api/v1/notification-preferences', {
        params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
        body: {
          recipientType: existing.recipientType,
          recipientId: existing.recipientId,
          preferences: [
            {
              channel: 'EMAIL',
              enabled: true,
              quietHoursStart: '23:00:00',
              quietHoursEnd: '07:00',
              timezone: 'Europe/Berlin',
            },
          ],
        },
      }),
    );
    // A replace, not a merge: the SMS row the person had is gone.
    expect(written.data.items).toHaveLength(1);
    // Seconds are dropped, because a window ending at 07:59:59 is one somebody meant to
    // end at 08:00.
    expect(written.data.items[0]!.quietHoursStart).toBe('23:00');
    expect(written.data.items[0]!.timezone).toBe('Europe/Berlin');
    expect(written.data.items[0]!.eventCode ?? null).toBeNull();
  });

  it('reads the log with either notification grant and writes with only one', async () => {
    const reviewer = await signIn('reviewer.a');
    const readable = await unwrap(
      reviewer.c.GET('/api/v1/notification-messages', {
        params: { header: tenant(reviewer), query: { limit: 10 } },
      }),
    );
    expect(readable.data.items.length).toBeGreaterThan(0);

    const denied = await refusal(
      unwrap(
        reviewer.c.POST('/api/v1/notification-templates', {
          params: { header: { ...tenant(reviewer), 'Idempotency-Key': key() } },
          body: {
            eventCode: 'service_request.decided',
            channel: 'SMS',
            locale: 'tr-TR',
            body: '{{status_code}}',
            declaredVariables: ['status_code'],
          },
        }),
      ),
    );
    expect(denied.status).toBe(403);
    expect(denied.detail).toBe('notification.manage');
  });
});

describe('worklist: the permission a queue names', () => {
  it('shows a queue only to the people who can do its work, and lets only them claim it', async () => {
    // The tenant points the review queue at the permission its work actually takes
    // (migration 000048). Before it did, a financial reviewer saw the clinical queue in the
    // morning list, could open work they may not read, and could take it off the doctors.
    const queue = api.world.workQueues.find((q) => q.code === 'HEALTH_REVIEW')!;
    queue.requiredPermission = 'health.medical_report.review';
    const open = itemByTitle('Bekleyen inceleme');

    const clerk = await signIn('financial.reviewer');
    const clerkList = await unwrap(
      clerk.c.GET('/api/v1/work-items', {
        params: { header: tenant(clerk), query: { queueId: queue.id } },
      }),
    );
    expect(clerkList.data.items).toHaveLength(0);

    // Not shown is not found: neither opening the item nor claiming it is a way in.
    const hidden = await refusal(
      unwrap(
        clerk.c.GET('/api/v1/work-items/{workItemId}', {
          params: { header: tenant(clerk), path: { workItemId: open.id } },
        }),
      ),
    );
    expect(hidden.status).toBe(404);
    const refused = await refusal(
      unwrap(
        clerk.c.POST('/api/v1/work-items/{workItemId}/claim', {
          params: {
            header: {
              ...tenant(clerk),
              'Idempotency-Key': key(),
              'If-Match': `"${open.rowVersion}"`,
            },
            path: { workItemId: open.id },
          },
        }),
      ),
    );
    expect(refused.status).toBe(404);

    // The reviewer who can do the work still has it in their list.
    const reviewer = await signIn('doctor.a');
    const seen = await unwrap(
      reviewer.c.GET('/api/v1/work-items', {
        params: { header: tenant(reviewer), query: { queueId: queue.id } },
      }),
    );
    expect(seen.data.items.map((i) => i.id)).toContain(open.id);
  });
});
