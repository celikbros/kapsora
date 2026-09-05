/**
 * End-to-end flows through the typed client against the treatment report half of the M5 mock
 * world: the lifecycle, the freeze, the version chain, the usage trace and the two
 * projections.
 *
 * The mock is a test double of the Go server and M4 treats a divergence in either direction
 * as a bug, so every test below is written against a behaviour the server has and a plausible
 * mock would get wrong: the approved report that answers 409 rather than quietly accepting an
 * edit, the correction that inherits the reference and the lines, the chain that holds exactly
 * one approved version, the submit gate that wants both a service line and a scanned-clean
 * file, and the sponsor HR user who is served the covered limits and never the summary.
 *
 * The headline is the same assertion the Go tests make, made here: the serialised body a
 * sponsor HR user receives is scanned for the report type, the summary, the reviewer's comment
 * and the attachment filename, and none may appear. Remove the projection from
 * medical-report-handlers.ts and this fails.
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

const key = (): string => `mock-${randomId()}`;
const tenant = (s: Session) => ({ 'X-Tenant-ID': s.tenantId });

async function refusal(call: Promise<unknown>): Promise<Problem> {
  try {
    await call;
  } catch (error) {
    if (error instanceof ApiError) return error.problem;
    throw error;
  }
  throw new Error('expected the call to be refused');
}

// --- fixture lookups, so no test depends on a generated id -------------------------------

function reportWithStatus(status: string) {
  const row = api.world.medicalReports.find((r) => r.status === status && r.versionNo === 1);
  if (!row) throw new Error(`fixture: no ${status} medical report`);
  return row;
}

/** The correction the world seeds: version 2 of the rejected report's chain. */
function correction() {
  const row = api.world.medicalReports.find((r) => r.versionNo === 2);
  if (!row) throw new Error('fixture: no corrected report version');
  return row;
}

function linesOf(reportId: string) {
  return api.world.medicalReportServices.filter((line) => line.reportId === reportId);
}

function storedReport(id: string) {
  const row = api.world.medicalReports.find((r) => r.id === id);
  if (!row) throw new Error(`fixture: no report ${id}`);
  return row;
}

/** The stored row as one document, so a comparison cannot pass over a field. */
function snapshot(id: string): string {
  return JSON.stringify({
    report: storedReport(id),
    lines: linesOf(id),
  });
}

// --- lifecycle helpers -------------------------------------------------------------------

async function get(s: Session, id: string) {
  return unwrap(
    s.c.GET('/api/v1/medical-reports/{reportId}', {
      params: { header: tenant(s), path: { reportId: id } },
    }),
  );
}

async function etagOf(s: Session, id: string): Promise<string> {
  return (await get(s, id)).response.headers.get('ETag')!;
}

async function command(s: Session, id: string, verb: string, body?: unknown) {
  const etag = await etagOf(s, id);
  const path =
    `/api/v1/medical-reports/{reportId}/${verb}` as '/api/v1/medical-reports/{reportId}/submit';
  return unwrap(
    s.c.POST(path, {
      params: {
        header: { ...tenant(s), 'Idempotency-Key': key(), 'If-Match': etag },
        path: { reportId: id },
      },
      ...(body === undefined ? {} : { body: body as never }),
    }),
  );
}

/**
 * Takes a draft the whole way to APPROVED, provider then reviewer.
 *
 * It signs in again between the two halves because the mock holds one session at a time, the
 * way one browser holds one cookie — and because the two halves genuinely are two people: the
 * provider that writes a report may not decide about it, and the reviewer that decides may not
 * write one.
 */
async function approve(id: string) {
  const provider = await signIn('provider.a');
  await command(provider, id, 'submit');
  const reviewer = await signIn('doctor.a');
  await command(reviewer, id, 'start-review');
  return command(reviewer, id, 'approve', {
    reviewComment: 'Rapordaki bulgular ile istenen hizmet uyumlu bulundu.',
  });
}

/** Links a file the scanner has cleared, so a fresh report can be submitted. */
function attachCleanDocument(reportId: string, documentTypeCode = 'MEDICAL_REPORT'): void {
  const source = api.world.documents.find((d) => d.scanStatus === 'CLEAN')!;
  api.world.documentLinks.push({
    tenantId: source.tenantId,
    id: api.world.nextId(),
    documentId: source.id,
    aggregateType: 'MEDICAL_REPORT',
    aggregateId: reportId,
    documentTypeCode,
    purpose: null,
    requiredPermission: 'health.clinical.read',
    createdBy: null,
    createdAt: new Date().toISOString(),
  });
}

describe('the treatment report world', () => {
  it('carries a draft, an approved report with two covered services, a rejection and its version 2', () => {
    const draft = reportWithStatus('DRAFT');
    const approved = reportWithStatus('APPROVED');
    const rejected = reportWithStatus('REJECTED');
    const second = correction();

    expect(draft.submittedAt).toBeNull();
    expect(linesOf(approved.id)).toHaveLength(2);
    expect(rejected.rejectReasonCode).toBeTruthy();

    // The correction is version 2 of the rejected report's chain: same reference, same root,
    // a draft again, and the lines carried over.
    expect(second.reference).toBe(rejected.reference);
    expect(second.rootReportId).toBe(rejected.rootReportId);
    expect(second.supersedesReportId).toBe(rejected.id);
    expect(second.status).toBe('DRAFT');
    expect(linesOf(second.id).length).toBeGreaterThan(0);
    // And the rejection is exactly as it was decided.
    expect(rejected.status).toBe('REJECTED');
    expect(linesOf(rejected.id)).toHaveLength(1);
  });

  it('grants the report to the provider and the review to the reviewer, and neither to both', () => {
    const provider = api.world.accounts.find((a) => a.username === 'provider.a')!;
    const reviewer = api.world.accounts.find((a) => a.username === 'doctor.a')!;
    const sponsor = api.world.accounts.find((a) => a.username === 'sponsor.hr')!;
    const permissions = (a: typeof provider) => a.memberships[0]!.permissions;

    expect(permissions(provider)).toContain('health.medical_report.manage');
    expect(permissions(provider)).not.toContain('health.medical_report.review');
    expect(permissions(reviewer)).toContain('health.medical_report.review');
    expect(permissions(reviewer)).not.toContain('health.medical_report.manage');
    expect(permissions(sponsor)).not.toContain('health.medical_report.manage');
    expect(permissions(sponsor)).not.toContain('health.medical_report.review');
  });
});

describe('a decided report', () => {
  /**
   * The package's whole reason for existing, at the mock's API. Both edits are refused, the
   * stored row is unchanged after them, and a correction is a new version that leaves the old
   * decision exactly where it is.
   */
  it('refuses both edits with 409 and is untouched by the refusals', async () => {
    const draft = reportWithStatus('DRAFT');
    await approve(draft.id);
    const provider = await signIn('provider.a');
    const before = snapshot(draft.id);
    const etag = await etagOf(provider, draft.id);

    const patched = await refusal(
      unwrap(
        provider.c.PATCH('/api/v1/medical-reports/{reportId}', {
          params: {
            header: { ...tenant(provider), 'Idempotency-Key': key(), 'If-Match': etag },
            path: { reportId: draft.id },
          },
          body: {
            reportType: 'BASKA_TUR',
            issuedAt: '2026-01-01',
            validFrom: '2026-01-01',
            validTo: '2027-01-01',
          },
        }),
      ),
    );
    expect(patched.status).toBe(409);
    expect(patched.code).toBe('MEDICAL_REPORT_IMMUTABLE');

    const replaced = await refusal(
      unwrap(
        provider.c.PUT('/api/v1/medical-reports/{reportId}/services', {
          params: {
            header: { ...tenant(provider), 'Idempotency-Key': key(), 'If-Match': etag },
            path: { reportId: draft.id },
          },
          body: { items: [] },
        }),
      ),
    );
    expect(replaced.status).toBe(409);
    expect(replaced.code).toBe('MEDICAL_REPORT_IMMUTABLE');

    // Byte-identical: two refusals wrote nothing at all.
    expect(snapshot(draft.id)).toBe(before);
  });

  it('is corrected by a new version, and the chain then holds exactly one approval', async () => {
    const first = reportWithStatus('DRAFT');
    await approve(first.id);
    const provider = await signIn('provider.a');
    const decidedAt = storedReport(first.id).reviewedAt;

    const created = await unwrap(
      provider.c.POST('/api/v1/medical-reports', {
        params: { header: { ...tenant(provider), 'Idempotency-Key': key() } },
        body: { personId: first.personId, supersedesReportId: first.id },
      }),
    );
    const second = created.data;
    expect(second.versionNo).toBe(2);
    expect(second.reference).toBe(first.reference);
    expect(second.rootReportId).toBe(first.rootReportId);
    expect(second.status).toBe('DRAFT');
    // The lines are copied, with ids of their own: a line belongs to the version that holds it.
    expect(second.services).toHaveLength(linesOf(first.id).length);
    expect(second.services.map((l) => l.id)).not.toEqual(linesOf(first.id).map((l) => l.id));

    attachCleanDocument(second.id);
    await approve(second.id);
    await signIn('provider.a');

    const chain = await unwrap(
      provider.c.GET('/api/v1/medical-reports', {
        params: {
          header: tenant(provider),
          query: { rootReportId: first.rootReportId, limit: 50 },
        },
      }),
    );
    const approved = chain.data.items.filter((r) => r.status === 'APPROVED');
    expect(approved).toHaveLength(1);
    expect(approved[0]!.id).toBe(second.id);
    expect(chain.data.items.find((r) => r.id === first.id)!.status).toBe('SUPERSEDED');

    // The old version keeps its decision and stays readable.
    const old = await get(provider, first.id);
    expect(old.data.reviewComment).toBeTruthy();
    expect(storedReport(first.id).reviewedAt).toBe(decidedAt);
    expect(old.data.services.length).toBeGreaterThan(0);
  });

  it('refuses a correction of a draft, and a second correction of one version', async () => {
    const provider = await signIn('provider.a');
    const draft = reportWithStatus('DRAFT');

    const tooEarly = await refusal(
      unwrap(
        provider.c.POST('/api/v1/medical-reports', {
          params: { header: { ...tenant(provider), 'Idempotency-Key': key() } },
          body: { personId: draft.personId, supersedesReportId: draft.id },
        }),
      ),
    );
    expect(tooEarly.status).toBe(422);
    expect(tooEarly.code).toBe('MEDICAL_REPORT_SUPERSEDES_INVALID');

    await approve(draft.id);
    await signIn('provider.a');
    await unwrap(
      provider.c.POST('/api/v1/medical-reports', {
        params: { header: { ...tenant(provider), 'Idempotency-Key': key() } },
        body: { personId: draft.personId, supersedesReportId: draft.id },
      }),
    );
    const forked = await refusal(
      unwrap(
        provider.c.POST('/api/v1/medical-reports', {
          params: { header: { ...tenant(provider), 'Idempotency-Key': key() } },
          body: { personId: draft.personId, supersedesReportId: draft.id },
        }),
      ),
    );
    expect(forked.status).toBe(422);
    expect(forked.code).toBe('MEDICAL_REPORT_SUPERSEDES_INVALID');
  });
});

describe('the submit gate', () => {
  it('refuses a report with no service line', async () => {
    const provider = await signIn('provider.a');
    const draft = reportWithStatus('DRAFT');
    const cleared = await unwrap(
      provider.c.PUT('/api/v1/medical-reports/{reportId}/services', {
        params: {
          header: {
            ...tenant(provider),
            'Idempotency-Key': key(),
            'If-Match': await etagOf(provider, draft.id),
          },
          path: { reportId: draft.id },
        },
        body: { items: [] },
      }),
    );
    expect(cleared.data.services).toHaveLength(0);

    const refused = await refusal(
      unwrap(
        provider.c.POST('/api/v1/medical-reports/{reportId}/submit', {
          params: {
            header: {
              ...tenant(provider),
              'Idempotency-Key': key(),
              'If-Match': cleared.response.headers.get('ETag')!,
            },
            path: { reportId: draft.id },
          },
        }),
      ),
    );
    expect(refused.status).toBe(422);
    expect(refused.code).toBe('MEDICAL_REPORT_SERVICE_REQUIRED');
  });

  it('refuses a report whose file the scanner has not cleared', async () => {
    const provider = await signIn('provider.a');
    const draft = reportWithStatus('DRAFT');
    // Take the cleared file away and leave one still in quarantine in its place: a link to
    // an object nobody has scanned is not a document a reviewer can open.
    api.world.documentLinks = api.world.documentLinks.filter(
      (link) => !(link.aggregateType === 'MEDICAL_REPORT' && link.aggregateId === draft.id),
    );
    const pending = api.world.documents.find((d) => d.scanStatus === 'PENDING')!;
    api.world.documentLinks.push({
      tenantId: pending.tenantId,
      id: api.world.nextId(),
      documentId: pending.id,
      aggregateType: 'MEDICAL_REPORT',
      aggregateId: draft.id,
      documentTypeCode: 'MEDICAL_REPORT',
      purpose: null,
      requiredPermission: 'health.clinical.read',
      createdBy: null,
      createdAt: new Date().toISOString(),
    });

    const refused = await refusal(
      unwrap(
        provider.c.POST('/api/v1/medical-reports/{reportId}/submit', {
          params: {
            header: {
              ...tenant(provider),
              'Idempotency-Key': key(),
              'If-Match': await etagOf(provider, draft.id),
            },
            path: { reportId: draft.id },
          },
        }),
      ),
    );
    expect(refused.status).toBe(422);
    expect(refused.code).toBe('MEDICAL_REPORT_DOCUMENT_REQUIRED');

    // With a cleared file it goes, and it raises a work item whose title carries the
    // reference and no clinical word at all.
    attachCleanDocument(draft.id);
    const submitted = await command(provider, draft.id, 'submit');
    expect(submitted.data.status).toBe('SUBMITTED');
    const item = api.world.workItems.find(
      (i) => i.aggregateType === 'MEDICAL_REPORT' && i.aggregateId === draft.id,
    );
    expect(item).toBeDefined();
    expect(item!.title).toContain(submitted.data.reference);
    for (const clinical of [
      storedReport(draft.id).reportType,
      storedReport(draft.id).reportSubtype!,
      storedReport(draft.id).clinicalSummary!,
    ]) {
      expect(item!.title.includes(clinical)).toBe(false);
    }
  });

  it('refuses a withdrawal once a reviewer has picked the report up', async () => {
    const provider = await signIn('provider.a');
    const draft = reportWithStatus('DRAFT');
    await command(provider, draft.id, 'submit');
    const reviewer = await signIn('doctor.a');
    await command(reviewer, draft.id, 'start-review');
    await signIn('provider.a');

    const refused = await refusal(
      unwrap(
        provider.c.POST('/api/v1/medical-reports/{reportId}/cancel', {
          params: {
            header: {
              ...tenant(provider),
              'Idempotency-Key': key(),
              'If-Match': await etagOf(provider, draft.id),
            },
            path: { reportId: draft.id },
          },
        }),
      ),
    );
    expect(refused.status).toBe(409);
    expect(refused.code).toBe('MEDICAL_REPORT_TRANSITION_INVALID');
  });
});

describe('the sponsor HR user and a treatment report', () => {
  /**
   * The WP-I5-01 scan, over a report. Written against the bytes rather than the decoded
   * object: an assertion that `clinicalSummary` is undefined would pass if the summary came
   * back under another key or nested somewhere the type does not describe.
   */
  it('is served the covered limits and never a word of what was wrong', async () => {
    const s = await signIn('sponsor.hr');
    const approved = reportWithStatus('APPROVED');

    const page = await unwrap(
      s.c.GET('/api/v1/medical-reports', {
        params: { header: tenant(s), query: { limit: 50 } },
      }),
    );
    const one = await get(s, approved.id);

    const secrets = [
      approved.reportType,
      approved.reportSubtype!,
      approved.clinicalSummary!,
      approved.reviewComment!,
      linesOf(approved.id)[0]!.notes!,
      'tedavi-raporu.pdf',
    ];
    for (const [what, body] of [
      ['listMedicalReports', page.data],
      ['getMedicalReport', one.data],
    ] as const) {
      const serialized = JSON.stringify(body);
      for (const secret of secrets) {
        expect(
          serialized.includes(secret),
          `${what} answered a sponsor HR user with ${JSON.stringify(secret)}:\n${serialized}`,
        ).toBe(false);
      }
    }

    // The structural half, which is what a screen actually reads.
    expect(one.data.projection).toBe('FINANCIAL');
    expect(one.data.reportType).toBeUndefined();
    expect(one.data.reportSubtype).toBeUndefined();
    expect(one.data.clinicalSummary).toBeUndefined();
    expect(one.data.reviewComment).toBeUndefined();
    expect(one.data.documents).toHaveLength(0);
    // And what it does carry: everything a financial reviewer reconciles a claim against.
    expect(one.data.reference).toBe(approved.reference);
    expect(one.data.status).toBe('APPROVED');
    expect(one.data.validFrom).toBe(approved.validFrom);
    expect(one.data.services).toHaveLength(2);
    for (const line of one.data.services) {
      expect(line.serviceCode).toBeTruthy();
      expect(line.notes).toBeUndefined();
    }
    expect(one.data.services.some((l) => l.coveredQuantity !== null)).toBe(true);
    for (const row of page.data.items) expect(row.projection).toBe('FINANCIAL');

    // A financial read writes no access event: it carries nothing clinical.
    expect(api.world.healthAccessEvents.filter((e) => e.resourceId === approved.id)).toHaveLength(
      0,
    );
  });

  it('may neither write a report nor decide about one', async () => {
    const s = await signIn('sponsor.hr');
    const approved = reportWithStatus('APPROVED');
    const created = await refusal(
      unwrap(
        s.c.POST('/api/v1/medical-reports', {
          params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
          body: {
            personId: approved.personId,
            reportType: 'FIZIK_TEDAVI',
            issuedAt: approved.issuedAt,
            validFrom: approved.validFrom,
            validTo: approved.validTo,
          },
        }),
      ),
    );
    expect(created.status).toBe(403);
  });
});

describe('the clinical reader and a treatment report', () => {
  it('is served everything, and the look is on the record', async () => {
    const s = await signIn('provider.a');
    const approved = reportWithStatus('APPROVED');
    const one = await get(s, approved.id);

    expect(one.data.projection).toBe('CLINICAL');
    expect(one.data.reportType).toBe(approved.reportType);
    expect(one.data.reportSubtype).toBe(approved.reportSubtype);
    expect(one.data.clinicalSummary).toBe(approved.clinicalSummary);
    expect(one.data.reviewComment).toBe(approved.reviewComment);
    expect(one.data.documents.length).toBeGreaterThan(0);
    expect(one.data.services.every((l) => l.notes !== undefined)).toBe(true);

    const events = api.world.healthAccessEvents.filter((e) => e.resourceId === approved.id);
    expect(events.length).toBeGreaterThan(0);
    expect(events.at(-1)!.resourceType).toBe('MEDICAL_REPORT');
    expect(events.at(-1)!.accessType).toBe('VIEW');
    expect(events.at(-1)!.outcome).toBe('SUCCESS');
    expect(events.at(-1)!.personId).toBe(approved.personId);
  });

  it('follows the case: a report on a sensitive case narrows, then demands a purpose', async () => {
    const sensitiveCase = api.world.healthCases.find((c) => c.sensitivity === 'SENSITIVE')!;
    const report = reportWithStatus('APPROVED');
    report.caseId = sensitiveCase.id;
    report.personId = sensitiveCase.personId;

    // A clinical reader with no sensitive grant is narrowed rather than refused: a refusal
    // would itself say the case carries a protected category.
    const provider = await signIn('provider.a');
    void provider;
    const narrowed = await get(provider, report.id);
    expect(narrowed.data.projection).toBe('FINANCIAL');
    expect(narrowed.data.clinicalSummary).toBeUndefined();

    // The sensitive grant with no purpose is 428, and the refusal is on the record.
    const reviewer = await signIn('doctor.a');
    const refused = await refusal(get(reviewer, report.id));
    expect(refused.status).toBe(428);
    expect(refused.code).toBe('ACCESS_PURPOSE_REQUIRED');
    expect(
      api.world.healthAccessEvents.some(
        (e) => e.resourceId === report.id && e.outcome === 'DENIED',
      ),
    ).toBe(true);

    // With both, the clinical projection, and the purpose on the event.
    const full = await unwrap(
      reviewer.c.GET('/api/v1/medical-reports/{reportId}', {
        params: {
          header: {
            ...tenant(reviewer),
            'X-Access-Purpose': 'MEDICAL_REVIEW',
            'X-Access-Reason': 'Rapor%20incelemesi',
          },
          path: { reportId: report.id },
        },
      }),
    );
    expect(full.data.projection).toBe('CLINICAL');
    expect(full.data.clinicalSummary).toBeTruthy();
    const recorded = api.world.healthAccessEvents.find(
      (e) => e.resourceId === report.id && e.purposeCode === 'MEDICAL_REVIEW',
    );
    expect(recorded).toBeDefined();
    expect(recorded!.reasonText).toBe('Rapor incelemesi');
  });
});

describe('the usage trace', () => {
  it('says which version a claim leaned on', async () => {
    const s = await signIn('provider.a');
    const approved = reportWithStatus('APPROVED');
    const usages = await unwrap(
      s.c.GET('/api/v1/medical-reports/{reportId}/usages', {
        params: { header: tenant(s), path: { reportId: approved.id } },
      }),
    );
    expect(usages.data.items.length).toBeGreaterThan(0);
    for (const row of usages.data.items) {
      expect(row.reportId).toBe(approved.id);
      expect(row.usedByType).toBe('CLAIM');
    }
    // A report nobody has used has an empty trace rather than a refusal: "nothing leaned on
    // this yet" is an answer.
    const draft = reportWithStatus('DRAFT');
    const empty = await unwrap(
      s.c.GET('/api/v1/medical-reports/{reportId}/usages', {
        params: { header: tenant(s), path: { reportId: draft.id } },
      }),
    );
    expect(empty.data.items).toHaveLength(0);
  });
});
