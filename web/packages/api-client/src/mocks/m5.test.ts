/**
 * End-to-end flows through the typed client against the M5 mock world: the health case, its
 * encounters, their diagnoses, the clinical/financial split and the clinical access log.
 *
 * The mock is a test double of the Go server, so every test below is written against a
 * behaviour the server has and a plausible mock would get wrong: the sponsor HR user who
 * sees the case and not what it is about, the sensitive case that narrows rather than
 * refuses, the purpose header that is a precondition rather than a hint, the diagnosis list
 * that is a 403 rather than an empty array, and the access log that carries the reads and
 * not the writes.
 *
 * The headline is the same assertion the Go tests make, made here: the serialised body a
 * sponsor HR user receives is scanned for the diagnosis code and the clinical notes, and
 * neither may appear. Remove the projection from health-handlers.ts and this fails.
 */
import { readFileSync } from 'node:fs';

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

// --- fixture lookups, so no test depends on a generated id -------------------------------

function caseWithSensitivity(sensitivity: 'STANDARD' | 'SENSITIVE') {
  const row = api.world.healthCases.find((c) => c.sensitivity === sensitivity);
  if (!row) throw new Error(`fixture: no ${sensitivity} health case`);
  return row;
}
function encounterOfCase(caseId: string) {
  const row = api.world.encounters.find((e) => e.caseId === caseId);
  if (!row) throw new Error(`fixture: no encounter on case ${caseId}`);
  return row;
}
function codeValueByCode(code: string) {
  const row = api.world.codeValues.find((v) => v.code === code);
  if (!row) throw new Error(`fixture: no code value ${code}`);
  return row;
}
function enrollmentOf(personId: string) {
  const row = api.world.enrollments.find((e) => e.personId === personId);
  if (!row) throw new Error(`fixture: no enrollment for ${personId}`);
  return row;
}

describe('the health world', () => {
  it('carries a standard case and a sensitive one for the same member', () => {
    const standard = caseWithSensitivity('STANDARD');
    const sensitive = caseWithSensitivity('SENSITIVE');
    expect(standard.personId).toBe(sensitive.personId);
    // The sensitive one is sensitive because of the code its diagnosis names, not because
    // a fixture said so: the same fact the server derives at write time.
    const encounter = encounterOfCase(sensitive.id);
    const diagnosis = api.world.diagnoses.find((d) => d.encounterId === encounter.id)!;
    expect(diagnosis.sensitive).toBe(true);
    expect(codeValueByCode('F32.1').attributes.sensitive).toBe(true);
    expect(codeValueByCode('J06.9').attributes.sensitive).toBeUndefined();
  });

  it('carries a sponsor.hr account holding health.case.read and never the clinical grant', () => {
    const account = api.world.accounts.find((a) => a.username === 'sponsor.hr');
    expect(account).toBeDefined();
    const permissions = account!.memberships[0]!.permissions;
    expect(permissions).toContain('health.case.read');
    expect(permissions).not.toContain('health.clinical.read');
    expect(permissions).not.toContain('health.sensitive.read');
    expect(permissions).not.toContain('health.case.manage');
  });
});

/**
 * The mock's role fixtures are copies of internal/identity/application/roles.go, and a copy
 * drifts. These read the Go source itself rather than a second copy of it: a permission
 * added to a role on the server and not here would leave a screen passing against the mock
 * and refused in production, which is exactly what happened once to PROVIDER_STAFF.
 */
function goRolePermissions(code: string): string[] {
  const source = readFileSync(
    new URL('../../../../../internal/identity/application/roles.go', import.meta.url),
    'utf8',
  );
  const role = source.indexOf(`{Code: "${code}"`);
  if (role < 0) throw new Error(`roles.go declares no role ${code}`);
  const marker = 'Permissions: []string{';
  const open = source.indexOf(marker, role);
  if (open < 0) throw new Error(`roles.go role ${code} declares no permissions`);
  const from = open + marker.length;
  const to = source.indexOf('}', from);
  return [...source.slice(from, to).matchAll(/"([^"]+)"/g)].map((m) => m[1]!);
}

function permissionsOf(username: string): string[] {
  const account = api.world.accounts.find((a) => a.username === username);
  if (!account) throw new Error(`fixture: no account ${username}`);
  return account.memberships[0]!.permissions;
}

describe('the M5 review and billing accounts', () => {
  it('grants sponsor.hr exactly the Go SPONSOR_HR list', () => {
    expect(permissionsOf('sponsor.hr')).toEqual(goRolePermissions('SPONSOR_HR'));
  });

  it('grants financial.reviewer exactly the Go FINANCIAL_REVIEWER list', () => {
    expect(permissionsOf('financial.reviewer')).toEqual(goRolePermissions('FINANCIAL_REVIEWER'));
  });

  it('grants billing.a exactly the Go PROVIDER_BILLING list', () => {
    expect(permissionsOf('billing.a')).toEqual(goRolePermissions('PROVIDER_BILLING'));
  });

  /**
   * Neither of them holds a clinical grant, which is what makes them the accounts the
   * financial projection is written for. Asserted rather than assumed: adding
   * health.clinical.read to either list would silently turn every WP-I5-04 projection test
   * that uses them into a test of nothing.
   */
  it('gives neither of them any clinical grant', () => {
    for (const username of ['financial.reviewer', 'billing.a']) {
      const permissions = permissionsOf(username);
      expect(permissions).not.toContain('health.clinical.read');
      expect(permissions).not.toContain('health.sensitive.read');
      expect(permissions).not.toContain('health.case.read');
    }
  });

  /** The billing desk is bounded by the same relationship row the clinic desk is. */
  it('scopes billing.a to the organization provider.a is scoped to', () => {
    const billing = api.world.accounts.find((a) => a.username === 'billing.a')!;
    const provider = api.world.accounts.find((a) => a.username === 'provider.a')!;
    expect(billing.memberships[0]!.scopes).toEqual(provider.memberships[0]!.scopes);
    expect(billing.memberships[0]!.scopes![0]!.type).toBe('ORGANIZATION');
  });
});

describe('the sponsor HR user', () => {
  /**
   * The Phase 6 acceptance criterion, at the mock's API. It is written against the bytes
   * rather than the decoded object on purpose: an assertion that `notesClinical` is
   * undefined would pass if the notes came back under another key or nested somewhere the
   * type does not describe. Scanning the serialised document cannot.
   */
  it('cannot see a diagnosis, a branch code or a clinical note, anywhere in the body', async () => {
    const s = await signIn('sponsor.hr');
    const sensitive = caseWithSensitivity('SENSITIVE');
    const standard = caseWithSensitivity('STANDARD');

    const page = await unwrap(
      s.c.GET('/api/v1/health-cases', { params: { header: tenant(s), query: { limit: 50 } } }),
    );
    const one = await unwrap(
      s.c.GET('/api/v1/health-cases/{caseId}', {
        params: { header: tenant(s), path: { caseId: sensitive.id } },
      }),
    );
    const encounter = encounterOfCase(sensitive.id);
    const encounterBody = await unwrap(
      s.c.GET('/api/v1/encounters/{encounterId}', {
        params: { header: tenant(s), path: { encounterId: encounter.id } },
      }),
    );
    const diagnosesRefusal = await refusal(
      unwrap(
        s.c.GET('/api/v1/encounters/{encounterId}/diagnoses', {
          params: { header: tenant(s), path: { encounterId: encounter.id } },
        }),
      ),
    );

    // The scan. Nothing a sponsor HR user was sent contains a diagnosis code, a display, a
    // branch code, a clinical note, or even the word that says the case is protected.
    const secrets = [
      'F32.1',
      'J06.9',
      'Orta düzeyde depresif atak',
      'Üst solunum yolu enfeksiyonu',
      encounterOfCase(sensitive.id).notesClinical!,
      encounterOfCase(standard.id).notesClinical!,
      '"PSK"',
      'SENSITIVE',
      'STANDARD',
    ];
    for (const [what, body] of [
      ['listHealthCases', page.data],
      ['getHealthCase', one.data],
      ['getEncounter', encounterBody.data],
      ['listEncounterDiagnoses refusal', diagnosesRefusal],
    ] as const) {
      const serialized = JSON.stringify(body);
      for (const secret of secrets) {
        expect(
          serialized.includes(secret),
          `${what} answered a sponsor HR user with ${JSON.stringify(secret)}:\n${serialized}`,
        ).toBe(false);
      }
    }

    // And the structural half, which is what a screen actually reads.
    expect(one.data.projection).toBe('FINANCIAL');
    expect(one.data.sensitivity).toBeUndefined();
    expect(one.data.encounters.length).toBeGreaterThan(0);
    for (const e of one.data.encounters) {
      expect(e.branchCode).toBeUndefined();
      expect(e.notesClinical).toBeUndefined();
      // The dates are not clinical and are the half a sponsor legitimately needs.
      expect(e.startedAt).toBeTruthy();
    }
    for (const c of page.data.items) expect(c.projection).toBe('FINANCIAL');
  });

  it('is refused the diagnosis list rather than handed an empty one', async () => {
    const s = await signIn('sponsor.hr');
    const encounter = encounterOfCase(caseWithSensitivity('STANDARD').id);
    const problem = await refusal(
      unwrap(
        s.c.GET('/api/v1/encounters/{encounterId}/diagnoses', {
          params: { header: tenant(s), path: { encounterId: encounter.id } },
        }),
      ),
    );
    // An empty list would tell the caller the encounter has no diagnosis, which is itself
    // a clinical fact it does not hold the grant for.
    expect(problem.status).toBe(403);
    expect(problem.code).toBe('CLINICAL_READ_REQUIRED');
  });

  it('may not open a case or write a diagnosis at all', async () => {
    const s = await signIn('sponsor.hr');
    const sensitive = caseWithSensitivity('SENSITIVE');
    const created = await refusal(
      unwrap(
        s.c.POST('/api/v1/health-cases', {
          params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
          body: {
            personId: sensitive.personId,
            enrollmentId: sensitive.enrollmentId,
            caseType: 'OUTPATIENT',
          },
        }),
      ),
    );
    expect(created.status).toBe(403);
    const written = await refusal(
      unwrap(
        s.c.PUT('/api/v1/encounters/{encounterId}/diagnoses', {
          params: {
            header: { ...tenant(s), 'Idempotency-Key': key() },
            path: { encounterId: encounterOfCase(sensitive.id).id },
          },
          body: { items: [] },
        }),
      ),
    );
    expect(written.status).toBe(403);
  });

  it('reads no clinical access log at all', async () => {
    const s = await signIn('sponsor.hr');
    const problem = await refusal(
      unwrap(s.c.GET('/api/v1/health-access-log', { params: { header: tenant(s) } })),
    );
    expect(problem.status).toBe(403);
  });
});

describe('the clinical projection', () => {
  it('carries everything for a provider clinician on an ordinary case', async () => {
    const s = await signIn('provider.a');
    const standard = caseWithSensitivity('STANDARD');
    const one = await unwrap(
      s.c.GET('/api/v1/health-cases/{caseId}', {
        params: { header: tenant(s), path: { caseId: standard.id } },
      }),
    );
    // A guard that refuses everybody is not a guard, it is an outage.
    expect(one.data.projection).toBe('CLINICAL');
    expect(one.data.sensitivity).toBe('STANDARD');
    const encounter = one.data.encounters[0]!;
    expect(encounter.branchCode).toBe(encounterOfCase(standard.id).branchCode);
    expect(encounter.notesClinical).toBe(encounterOfCase(standard.id).notesClinical);

    const diagnoses = await unwrap(
      s.c.GET('/api/v1/encounters/{encounterId}/diagnoses', {
        params: { header: tenant(s), path: { encounterId: encounterOfCase(standard.id).id } },
      }),
    );
    expect(diagnoses.data.items).toHaveLength(1);
    expect(diagnoses.data.items[0]!.code).toBe('J06.9');
    expect(diagnoses.data.items[0]!.codeSystemCode).toBe('ICD10');
    expect(diagnoses.data.items[0]!.sensitive).toBe(false);
  });

  it('records every clinical read on the access log, with the purpose it stated', async () => {
    const clinician = await signIn('provider.a');
    const standard = caseWithSensitivity('STANDARD');
    await unwrap(
      clinician.c.GET('/api/v1/health-cases/{caseId}', {
        params: {
          header: {
            ...tenant(clinician),
            'X-Access-Purpose': 'TREATMENT',
            // Percent-encoded: an HTTP header value is ISO-8859-1 and a browser refuses
            // to send Turkish text containing ğ, ş or ı. Both sides decode it.
            'X-Access-Reason': encodeURIComponent('kontrol muayenesi'),
          },
          path: { caseId: standard.id },
        },
      }),
    );

    const auditor = await signIn('doctor.a');
    const log = await unwrap(
      auditor.c.GET('/api/v1/health-access-log', {
        params: { header: tenant(auditor), query: { personId: standard.personId } },
      }),
    );
    const recorded = log.data.items.find((e) => e.purposeCode === 'TREATMENT');
    expect(recorded).toBeDefined();
    expect(recorded!.outcome).toBe('SUCCESS');
    expect(recorded!.personId).toBe(standard.personId);
    expect(recorded!.reasonText).toBe('kontrol muayenesi');
    expect(recorded!.resourceType).toBe('health_case');
  });

  it('writes nothing to the access log for a financial-projection read', async () => {
    const hr = await signIn('sponsor.hr');
    const standard = caseWithSensitivity('STANDARD');
    for (let i = 0; i < 3; i += 1) {
      await unwrap(
        hr.c.GET('/api/v1/health-cases/{caseId}', {
          params: { header: tenant(hr), path: { caseId: standard.id } },
        }),
      );
    }
    const auditor = await signIn('doctor.a');
    const log = await unwrap(
      auditor.c.GET('/api/v1/health-access-log', {
        params: { header: tenant(auditor), query: { personId: standard.personId } },
      }),
    );
    // The read that saw nothing clinical is not a clinical access, so the log is empty of
    // it — otherwise a member asking "who looked at my file" would be handed the HR user
    // who only ever saw a date.
    expect(log.data.items.filter((e) => e.actorId === hr.actorId)).toHaveLength(0);
  });
});

describe('a sensitive case', () => {
  it('narrows rather than refuses when the caller lacks health.sensitive.read', async () => {
    const s = await signIn('provider.a');
    const sensitive = caseWithSensitivity('SENSITIVE');
    const one = await unwrap(
      s.c.GET('/api/v1/health-cases/{caseId}', {
        params: { header: tenant(s), path: { caseId: sensitive.id } },
      }),
    );
    // A refusal would itself say the case carries a protected category, which is the fact
    // being protected — so a clinical reader without the sensitive grant sees exactly what
    // anybody else sees.
    expect(one.data.projection).toBe('FINANCIAL');
    expect(one.data.sensitivity).toBeUndefined();
    expect(JSON.stringify(one.data)).not.toContain('SENSITIVE');

    // And its diagnoses are the same 403 a caller with no clinical grant at all gets, so
    // the refusal never says which of the two it was.
    const problem = await refusal(
      unwrap(
        s.c.GET('/api/v1/encounters/{encounterId}/diagnoses', {
          params: { header: tenant(s), path: { encounterId: encounterOfCase(sensitive.id).id } },
        }),
      ),
    );
    expect(problem.code).toBe('CLINICAL_READ_REQUIRED');
  });

  it('demands a stated purpose from a caller that does hold the grant', async () => {
    const s = await signIn('doctor.a');
    const sensitive = caseWithSensitivity('SENSITIVE');
    const problem = await refusal(
      unwrap(
        s.c.GET('/api/v1/health-cases/{caseId}', {
          params: { header: tenant(s), path: { caseId: sensitive.id } },
        }),
      ),
    );
    expect(problem.status).toBe(428);
    expect(problem.code).toBe('ACCESS_PURPOSE_REQUIRED');

    // The refusal is on the record: "who tried" is as much of it as "who looked".
    const log = await unwrap(
      s.c.GET('/api/v1/health-access-log', {
        params: { header: tenant(s), query: { personId: sensitive.personId } },
      }),
    );
    expect(log.data.items.some((e) => e.outcome === 'DENIED')).toBe(true);
  });

  it('opens with both the grant and a purpose, and the look is on the record', async () => {
    const s = await signIn('doctor.a');
    const sensitive = caseWithSensitivity('SENSITIVE');
    const one = await unwrap(
      s.c.GET('/api/v1/health-cases/{caseId}', {
        params: {
          header: {
            ...tenant(s),
            'X-Access-Purpose': 'MEDICAL_REVIEW',
            'X-Access-Reason': encodeURIComponent('ön onay değerlendirmesi'),
          },
          path: { caseId: sensitive.id },
        },
      }),
    );
    expect(one.data.projection).toBe('CLINICAL');
    expect(one.data.sensitivity).toBe('SENSITIVE');
    expect(one.data.encounters[0]!.notesClinical).toBeTruthy();

    const diagnoses = await unwrap(
      s.c.GET('/api/v1/encounters/{encounterId}/diagnoses', {
        params: {
          header: { ...tenant(s), 'X-Access-Purpose': 'MEDICAL_REVIEW' },
          path: { encounterId: encounterOfCase(sensitive.id).id },
        },
      }),
    );
    expect(diagnoses.data.items[0]!.code).toBe('F32.1');
    expect(diagnoses.data.items[0]!.sensitive).toBe(true);

    const log = await unwrap(
      s.c.GET('/api/v1/health-access-log', {
        params: { header: tenant(s), query: { personId: sensitive.personId } },
      }),
    );
    // Both reads are on it — the case and the diagnoses — and each carries the purpose it
    // was actually given, which is why the case's row is picked by resource rather than by
    // being the newest.
    const success = log.data.items.find(
      (e) =>
        e.outcome === 'SUCCESS' &&
        e.purposeCode === 'MEDICAL_REVIEW' &&
        e.resourceType === 'health_case',
    );
    expect(success).toBeDefined();
    expect(success!.reasonText).toBe('ön onay değerlendirmesi');
    expect(
      log.data.items.some((e) => e.outcome === 'SUCCESS' && e.resourceType === 'health_encounter'),
    ).toBe(true);
  });

  it('refuses a purpose that is not in the reference rather than recording it as stated', async () => {
    const s = await signIn('doctor.a');
    const problem = await refusal(
      unwrap(
        s.c.GET('/api/v1/health-cases/{caseId}', {
          params: {
            // The header is not in the generated type: only the six seeded codes are, which
            // is itself part of the contract. A caller reaching the API by hand can still
            // send anything, and the mock refuses it exactly as the server does.
            header: { ...tenant(s), 'X-Access-Purpose': 'CURIOSITY' } as never,
            path: { caseId: caseWithSensitivity('SENSITIVE').id },
          },
        }),
      ),
    );
    expect(problem.status).toBe(422);
    expect(problem.errors?.[0]?.field).toBe('X-Access-Purpose');
  });

  it('serves the financial half, with no purpose and no access row, to a caller that declines', async () => {
    // doctor.a holds both grants, so a plain read of the sensitive case would be the 428
    // above and a plain read of the standard one would be the clinical projection. Saying
    // "financial only" is the reviewer declining to look, and choosing not to look is not a
    // look: no precondition, no clinical field, and nothing on the member's access log.
    const s = await signIn('doctor.a');
    for (const sensitivity of ['SENSITIVE', 'STANDARD'] as const) {
      const row = caseWithSensitivity(sensitivity);
      const one = await unwrap(
        s.c.GET('/api/v1/health-cases/{caseId}', {
          params: {
            header: { ...tenant(s), 'X-Access-Projection': 'FINANCIAL' },
            path: { caseId: row.id },
          },
        }),
      );
      expect(one.data.projection).toBe('FINANCIAL');
      expect(one.data.sensitivity).toBeUndefined();
      expect(one.data.encounters.every((e) => e.notesClinical === undefined)).toBe(true);
      expect(JSON.stringify(one.data)).not.toContain('SENSITIVE');

      const log = await unwrap(
        s.c.GET('/api/v1/health-access-log', {
          params: { header: tenant(s), query: { personId: row.personId } },
        }),
      );
      expect(log.data.items.filter((e) => e.actorId === s.actorId)).toHaveLength(0);
    }
  });

  it('refuses a projection the contract does not define, before anything is read', async () => {
    const s = await signIn('doctor.a');
    const problem = await refusal(
      unwrap(
        s.c.GET('/api/v1/health-cases/{caseId}', {
          params: {
            // CLINICAL is not a value a caller may ask for: the clinical projection is
            // earned, not requested. The generated type carries only FINANCIAL.
            header: { ...tenant(s), 'X-Access-Projection': 'CLINICAL' } as never,
            path: { caseId: caseWithSensitivity('SENSITIVE').id },
          },
        }),
      ),
    );
    expect(problem.status).toBe(400);
    expect(problem.errors?.[0]?.field).toBe('X-Access-Projection');
  });
});

describe('writing a case', () => {
  it('opens STANDARD and becomes sensitive only through the code its diagnosis names', async () => {
    const s = await signIn('provider.a');
    const person = caseWithSensitivity('STANDARD').personId;
    const enrollment = enrollmentOf(person);
    const provider = api.world.accounts.find((a) => a.username === 'provider.a')!;
    const organizationId = provider.memberships[0]!.scopes!.find(
      (g) => g.type === 'ORGANIZATION',
    )!.id!;

    const created = await unwrap(
      s.c.POST('/api/v1/health-cases', {
        params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
        body: {
          personId: person,
          enrollmentId: enrollment.id,
          caseType: 'OUTPATIENT',
          providerOrganizationId: organizationId,
        },
      }),
    );
    expect(created.data.sensitivity).toBe('STANDARD');
    expect(created.data.status).toBe('OPEN');
    expect(created.data.programId).toBe(enrollment.programId);

    const encounter = await unwrap(
      s.c.POST('/api/v1/health-cases/{caseId}/encounters', {
        params: {
          header: { ...tenant(s), 'Idempotency-Key': key() },
          path: { caseId: created.data.id },
        },
        body: {
          encounterType: 'OUTPATIENT',
          startedAt: '2026-06-15T09:00:00.000Z',
          endedAt: '2026-06-15T09:40:00.000Z',
          branchCode: 'PSK',
          notesClinical: 'Hasta uyku düzeninden şikayetçi.',
        },
      }),
    );

    await unwrap(
      s.c.PUT('/api/v1/encounters/{encounterId}/diagnoses', {
        params: {
          header: { ...tenant(s), 'Idempotency-Key': key() },
          path: { encounterId: encounter.data.id },
        },
        body: { items: [{ codeValueId: codeValueByCode('F32.1').id, diagnosisType: 'PRIMARY' }] },
      }),
    );

    // Writing a sensitive code made the case sensitive; nothing sent that.
    const after = await unwrap(
      s.c.GET('/api/v1/health-cases/{caseId}', {
        params: { header: tenant(s), path: { caseId: created.data.id } },
      }),
    );
    // The clinician holds no sensitive grant, so its own case now answers in the financial
    // projection — which is the rule working, not a bug.
    expect(after.data.projection).toBe('FINANCIAL');

    const reviewer = await signIn('doctor.a');
    const seen = await unwrap(
      reviewer.c.GET('/api/v1/health-cases/{caseId}', {
        params: {
          header: { ...tenant(reviewer), 'X-Access-Purpose': 'MEDICAL_REVIEW' },
          path: { caseId: created.data.id },
        },
      }),
    );
    expect(seen.data.sensitivity).toBe('SENSITIVE');
  });

  it('refuses a second primary diagnosis, naming the line', async () => {
    const s = await signIn('provider.a');
    const encounter = encounterOfCase(caseWithSensitivity('STANDARD').id);
    const problem = await refusal(
      unwrap(
        s.c.PUT('/api/v1/encounters/{encounterId}/diagnoses', {
          params: {
            header: { ...tenant(s), 'Idempotency-Key': key() },
            path: { encounterId: encounter.id },
          },
          body: {
            items: [
              { codeValueId: codeValueByCode('J06.9').id, diagnosisType: 'PRIMARY' },
              { codeValueId: codeValueByCode('F32.1').id, diagnosisType: 'PRIMARY' },
            ],
          },
        }),
      ),
    );
    expect(problem.status).toBe(422);
    expect(problem.errors?.some((e) => e.code === 'DUPLICATE_PRIMARY')).toBe(true);
  });

  it('refuses a case opened from a request that is not an episode of care', async () => {
    const s = await signIn('provider.a');
    const person = caseWithSensitivity('STANDARD').personId;
    const enrollment = enrollmentOf(person);
    const request = api.world.serviceRequests.find(
      (r) => r.tenantId === s.tenantId && r.personId === person,
    );
    if (!request) throw new Error('fixture: no service request for the member');
    const originalType = request.requestType;
    // A reimbursement on the same lines is not an episode of care.
    request.requestType = 'REIMBURSEMENT';
    const problem = await refusal(
      unwrap(
        s.c.POST('/api/v1/health-cases', {
          params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
          body: {
            personId: person,
            enrollmentId: enrollment.id,
            caseType: 'OUTPATIENT',
            // The provider boundary is checked before the request is, on both sides, so a
            // scoped caller has to name its own organization to reach the check under test.
            providerOrganizationId: api.world.accounts
              .find((a) => a.username === 'provider.a')!
              .memberships[0]!.scopes!.find((g) => g.type === 'ORGANIZATION')!.id!,
            serviceRequestId: request.id,
          },
        }),
      ),
    );
    request.requestType = originalType;
    expect(problem.status).toBe(422);
    expect(problem.code).toBe('HEALTH_CASE_REQUEST_NOT_ELIGIBLE');
  });

  it('refuses a close over an encounter nobody ended', async () => {
    const s = await signIn('provider.a');
    const person = caseWithSensitivity('STANDARD').personId;
    const enrollment = enrollmentOf(person);
    const organizationId = api.world.accounts
      .find((a) => a.username === 'provider.a')!
      .memberships[0]!.scopes!.find((g) => g.type === 'ORGANIZATION')!.id!;

    const created = await unwrap(
      s.c.POST('/api/v1/health-cases', {
        params: { header: { ...tenant(s), 'Idempotency-Key': key() } },
        body: {
          personId: person,
          enrollmentId: enrollment.id,
          caseType: 'INPATIENT',
          providerOrganizationId: organizationId,
        },
      }),
    );
    const etag = created.response.headers.get('ETag')!;
    await unwrap(
      s.c.POST('/api/v1/health-cases/{caseId}/encounters', {
        params: {
          header: { ...tenant(s), 'Idempotency-Key': key() },
          path: { caseId: created.data.id },
        },
        body: { encounterType: 'INPATIENT', startedAt: '2026-06-15T09:00:00.000Z' },
      }),
    );
    const problem = await refusal(
      unwrap(
        s.c.POST('/api/v1/health-cases/{caseId}/close', {
          params: {
            header: { ...tenant(s), 'Idempotency-Key': key(), 'If-Match': etag },
            path: { caseId: created.data.id },
          },
          body: {},
        }),
      ),
    );
    expect(problem.status).toBe(409);
    expect(problem.code).toBe('HEALTH_CASE_ENCOUNTER_OPEN');
  });
});

describe('the provider boundary', () => {
  it('hides another provider’s case rather than refusing it', async () => {
    const s = await signIn('provider.a');
    const standard = caseWithSensitivity('STANDARD');
    // Move the case to an organization the provider is not granted.
    const other = api.world.relationships.find(
      (r) => r.tenantId === s.tenantId && r.id !== standard.providerOrganizationId,
    )!;
    standard.providerOrganizationId = other.id;

    const problem = await refusal(
      unwrap(
        s.c.GET('/api/v1/health-cases/{caseId}', {
          params: { header: tenant(s), path: { caseId: standard.id } },
        }),
      ),
    );
    // 404 rather than 403: that such a case exists at all is somebody else's business.
    expect(problem.status).toBe(404);
    const page = await unwrap(
      s.c.GET('/api/v1/health-cases', { params: { header: tenant(s), query: { limit: 50 } } }),
    );
    expect(page.data.items.some((c) => c.id === standard.id)).toBe(false);
  });
});
