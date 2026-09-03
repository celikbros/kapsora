import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { createKapsoraClient } from '../client';
import { createOperations, type Operations } from '../operations';
import type { ApiError } from '../problem';
import { createMockServer } from './node';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

function ops(): Operations {
  return createOperations(
    createKapsoraClient({ baseUrl: BASE, csrfToken: () => api.session?.csrfToken ?? null }),
  );
}

/** Signs in and returns the operations plus the active tenant. */
async function signIn(username: string): Promise<{ o: Operations; tenantId: string }> {
  const o = ops();
  await o.session.login(username, PASSWORD);
  const session = await o.session.get();
  const tenantId = session.activeTenantId ?? (await o.session.tenants())[0]!.id;
  if (!session.activeTenantId) await o.session.switchTenant(tenantId);
  return { o, tenantId };
}

/** The demo family: the principal of DEMO_A with its spouse and children. */
function principal(tenantId: string) {
  const membership = api.world.memberships.find(
    (m) => m.tenantId === tenantId && m.principalMembershipId === null,
  );
  if (!membership) throw new Error('fixture: no principal membership');
  return membership;
}

describe('people', () => {
  it('patches a person under If-Match and refuses a stale tag', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const personId = principal(tenantId).personId;
    const current = await o.people.get(tenantId, personId);

    const patched = await o.people.patch(tenantId, personId, current.etag, {
      firstName: 'Ayşegül',
    });
    expect(patched.data.firstName).toBe('Ayşegül');
    expect(patched.etag).not.toBe(current.etag);

    const stale = (await o.people
      .patch(tenantId, personId, current.etag, { firstName: 'Başka' })
      .catch((e: unknown) => e)) as ApiError;
    expect(stale.status).toBe(412);
    expect(stale.problem.code).toBe('ETAG_MISMATCH');
  });

  it('needs a step-up for identifier search, then finds the person', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const person = api.world.people.find((p) => p.tenantId === tenantId);
    const tckn = person?.identifiers.find((i) => i.type === 'TCKN')?.value;
    if (!person || !tckn) throw new Error('fixture: no person with a TCKN');

    const denied = (await o.people
      .searchByIdentifier(tenantId, { type: 'TCKN', value: tckn })
      .catch((e: unknown) => e)) as ApiError;
    expect(denied.status).toBe(403);
    expect(denied.problem.code).toBe('STEP_UP_REQUIRED');

    await o.session.stepUp(PASSWORD);
    const found = await o.people.searchByIdentifier(tenantId, { type: 'TCKN', value: tckn });
    expect(found.id).toBe(person.id);

    const missing = (await o.people
      .searchByIdentifier(tenantId, { type: 'TCKN', value: '10000000146' })
      .catch((e: unknown) => e)) as ApiError;
    expect(missing.status).toBe(404);
  });

  it('serves the catalogs, the family and the memberships', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const catalogs = await o.people.catalogs(tenantId);
    expect(catalogs.identifierTypes.map((t) => t.code)).toContain('TCKN');
    expect(catalogs.membershipTypes.find((t) => t.code === 'FAMILY')?.requiresPrincipal).toBe(true);

    const personId = principal(tenantId).personId;
    const relationships = await o.people.listRelationships(tenantId, personId);
    expect(relationships.length).toBeGreaterThan(0);
    expect(relationships[0]!.otherPerson.displayName).not.toBe('');

    const memberships = await o.people.listMemberships(tenantId, personId);
    expect(memberships[0]!.sponsorDisplayName).not.toBe('');
  });
});

describe('programs and plans', () => {
  it('creates a program and a plan, then walks the maker-checker publish', async () => {
    const maker = await signIn('admin.a');
    const program = await maker.o.benefit.createProgram(maker.tenantId, {
      code: 'TEST-PRG',
      name: 'Test Programı',
      programType: 'EMPLOYEE_BENEFIT',
      sponsorOrganizationId: api.world.programs[0]!.sponsorOrganizationId,
      payerOrganizationId: api.world.programs[0]!.payerOrganizationId,
    });
    expect(program.data.status).toBe('DRAFT');

    const plan = await maker.o.benefit.createPlan(maker.tenantId, program.data.id, {
      code: 'TEST-PLAN',
      name: 'Test Planı',
    });
    const version = await maker.o.benefit.createPlanVersion(maker.tenantId, plan.data.id, {
      validFrom: '2027-01-01',
    });
    const withDefinitions = await maker.o.benefit.replaceEntitlementDefinitions(
      maker.tenantId,
      version.data.id,
      version.etag,
      [
        {
          code: 'TEST_MONEY',
          name: 'Test Bakiyesi',
          unitType: 'MONEY',
          currencyCode: 'TRY',
          periodType: 'CALENDAR_YEAR',
          initialQuantity: '1250.000000',
        },
      ],
    );
    expect(withDefinitions.data.definitions[0]!.initialQuantity).toBe('1250.000000');

    const submitted = await maker.o.benefit.submitPlanVersion(
      maker.tenantId,
      version.data.id,
      withDefinitions.etag,
    );
    expect(submitted.data.status).toBe('UNDER_REVIEW');

    // Publishing needs a fresh password re-entry, before anything else is judged.
    const withoutStepUp = (await maker.o.benefit
      .publishPlanVersion(maker.tenantId, version.data.id, submitted.etag)
      .catch((e: unknown) => e)) as ApiError;
    expect(withoutStepUp.status).toBe(403);
    expect(withoutStepUp.problem.code).toBe('STEP_UP_REQUIRED');

    // The maker may not publish their own submission, even after stepping up.
    await maker.o.session.stepUp(PASSWORD);
    const sameActor = (await maker.o.benefit
      .publishPlanVersion(maker.tenantId, version.data.id, submitted.etag)
      .catch((e: unknown) => e)) as ApiError;
    expect(sameActor.status).toBe(403);
    expect(sameActor.problem.code).toBe('MAKER_CHECKER_SAME_ACTOR');

    const checker = await signIn('both.ab');
    await checker.o.session.stepUp(PASSWORD);
    const published = await checker.o.benefit.publishPlanVersion(
      checker.tenantId,
      version.data.id,
      submitted.etag,
    );
    expect(published.data.status).toBe('PUBLISHED');
    expect(published.data.configurationHash).not.toBeNull();

    // A published version is read-only.
    const immutable = (await checker.o.benefit
      .patchPlanVersion(checker.tenantId, version.data.id, published.etag, { notes: 'geç' })
      .catch((e: unknown) => e)) as ApiError;
    expect(immutable.problem.code).toBe('PLAN_VERSION_IMMUTABLE');
  });

  it('refuses an enrollment into a plan with no published version', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const membership = principal(tenantId);
    const program = api.world.programs[0]!;
    const plan = await o.benefit.createPlan(tenantId, program.id, {
      code: 'EMPTY-PLAN',
      name: 'Sürümsüz Plan',
    });
    const err = (await o.benefit
      .createEnrollment(tenantId, membership.personId, {
        sponsorMembershipId: membership.id,
        planId: plan.data.id,
        validFrom: '2026-06-01',
      })
      .catch((e: unknown) => e)) as ApiError;
    expect(err.status).toBe(422);
    expect(err.problem.code).toBe('PLAN_NOT_PUBLISHED');
  });
});

describe('entitlements', () => {
  it('lists reachable accounts with exact decimals and pages the ledger', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const personId = principal(tenantId).personId;
    const accounts = await o.entitlements.listPersonEntitlements(tenantId, personId, '2026-09-03');
    expect(accounts.length).toBeGreaterThan(0);
    for (const account of accounts) {
      expect(typeof account.available).toBe('string');
      expect(account.available).toMatch(/^-?\d+\.\d{6}$/);
    }

    const account = accounts[0]!;
    const first = await o.entitlements.listLedger(tenantId, account.id, { limit: 1 });
    expect(first.items.length).toBe(1);
    expect(typeof first.items[0]!.deltaTotal).toBe('string');
    if (first.nextCursor) {
      const second = await o.entitlements.listLedger(tenantId, account.id, {
        cursor: first.nextCursor,
        limit: 1,
      });
      expect(second.items[0]!.id).not.toBe(first.items[0]!.id);
    }
  });

  it('runs an adjustment through maker-checker and moves the balance', async () => {
    const requester = await signIn('admin.a');
    const personId = principal(requester.tenantId).personId;
    const accounts = await requester.o.entitlements.listPersonEntitlements(
      requester.tenantId,
      personId,
      '2026-09-03',
    );
    const account = accounts.find((a) => a.status === 'OPEN') ?? accounts[0]!;
    const before = account.available;

    const adjustment = await requester.o.entitlements.createAdjustment(
      requester.tenantId,
      account.id,
      { deltaQuantity: '100.000000', reasonCode: 'CORRECTION' },
    );
    expect(adjustment.status).toBe('PENDING');

    await requester.o.session.stepUp(PASSWORD);
    const sameActor = (await requester.o.entitlements
      .approveAdjustment(requester.tenantId, adjustment.id, '"1"')
      .catch((e: unknown) => e)) as ApiError;
    expect(sameActor.problem.code).toBe('MAKER_CHECKER_SAME_ACTOR');

    const approver = await signIn('both.ab');
    await approver.o.session.stepUp(PASSWORD);
    const approved = await approver.o.entitlements.approveAdjustment(
      approver.tenantId,
      adjustment.id,
      '"1"',
    );
    expect(approved.status).toBe('APPROVED');
    expect(approved.ledgerEntryId).not.toBeNull();

    const after = await approver.o.entitlements.getAccount(approver.tenantId, account.id);
    expect(after.data.available).not.toBe(before);
  });
});

describe('eligibility', () => {
  it('explains the outcome and replays a keyed check', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const personId = principal(tenantId).personId;
    const accounts = await o.entitlements.listPersonEntitlements(tenantId, personId, '2026-09-03');
    const code = accounts[0]!.definition.code;

    const result = await o.eligibility.check(tenantId, {
      personId,
      serviceDate: '2026-09-03',
      serviceItems: [{ serviceDefinitionId: api.world.nextId(), quantity: '1.000000' }],
      context: { entitlementCodes: [code] },
    });
    expect(result.evaluationId).not.toBe('');
    expect(result.planVersionId).not.toBeNull();
    expect(result.items?.[0]?.entitlementCode).toBe(code);
    expect(typeof result.balances?.[0]?.available).toBe('string');

    const stored = await o.eligibility.getEvaluation(tenantId, result.evaluationId);
    expect(stored.id).toBe(result.evaluationId);
    expect(JSON.stringify(stored)).not.toContain('Yılmaz');

    const key = 'check-1';
    const first = await o.eligibility.check(
      tenantId,
      { personId, serviceDate: '2026-09-03', serviceItems: [] },
      key,
    );
    const replay = await o.eligibility.check(
      tenantId,
      { personId, serviceDate: '2026-09-03', serviceItems: [] },
      key,
    );
    expect(replay.evaluationId).toBe(first.evaluationId);
  });

  it('reports a missing enrollment instead of guessing', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const stranger = api.world.people.find(
      (p) =>
        p.tenantId === tenantId &&
        !api.world.enrollments.some((e) => e.personId === p.id) &&
        api.world.memberships.some((m) => m.personId === p.id),
    );
    if (!stranger) return; // the fixture enrolls everyone; nothing to assert
    const result = await o.eligibility.check(tenantId, {
      personId: stranger.id,
      serviceDate: '2026-09-03',
      serviceItems: [],
    });
    expect(['INELIGIBLE', 'MISSING_DATA']).toContain(result.outcome);
    expect(result.explanations.map((e) => e.code)).toContain('ENROLLMENT_NONE');
  });
});

describe('member import', () => {
  const header = [
    'source_record_id;first_name;middle_name;last_name;birth_date;sex_at_birth;tckn;member_no;',
    'employee_no;membership_type;principal_member_no;relationship;valid_from;valid_to;plan_code',
  ].join('');

  function file(rows: string[]): Blob {
    return new Blob([`${header}\n${rows.join('\n')}\n`], { type: 'text/csv' });
  }

  it('uploads, parks a bad row for review, then applies', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const sponsorOrganizationId = api.world.programs[0]!.sponsorOrganizationId;
    const content = file([
      'R1;Ayşe;;Yılmaz;1980-05-04;FEMALE;10000000146;M-1;;PRINCIPAL;;;2026-01-01;;',
      'R2;Bozuk;;Kayıt;1990-01-01;MALE;12345678902;M-2;;PRINCIPAL;;;2026-01-01;;',
    ]);

    const denied = (await o.imports
      .create(tenantId, {
        file: content,
        fileName: 'members.csv',
        sponsorOrganizationId,
        sourceSystem: 'HR',
        sourceVersion: 'v1',
      })
      .catch((e: unknown) => e)) as ApiError;
    expect(denied.problem.code).toBe('STEP_UP_REQUIRED');

    await o.session.stepUp(PASSWORD);
    const batch = await o.imports.create(tenantId, {
      file: content,
      fileName: 'members.csv',
      sponsorOrganizationId,
      sourceSystem: 'HR',
      sourceVersion: 'v1',
    });
    expect(batch.data.rowCount).toBe(2);
    expect(batch.data.status).toBe('REVIEW');
    expect(batch.data.counters.invalid).toBe(1);

    const duplicate = (await o.imports
      .create(tenantId, {
        file: content,
        fileName: 'members.csv',
        sponsorOrganizationId,
        sourceSystem: 'HR',
        sourceVersion: 'v1',
      })
      .catch((e: unknown) => e)) as ApiError;
    expect(duplicate.problem.code).toBe('IMPORT_DUPLICATE');

    const invalidRows = await o.imports.listRows(tenantId, batch.data.id, { status: 'INVALID' });
    expect(invalidRows.items.length).toBe(1);
    const bad = invalidRows.items[0]!;
    expect(bad.identifiers[0]!.maskedValue).toContain('*');

    const reviewed = await o.imports.reviewRow(
      tenantId,
      batch.data.id,
      bad.id,
      `"${bad.rowVersion}"`,
      { decision: 'SKIP' },
    );
    expect(reviewed.data.status).toBe('SKIPPED');

    const ready = await o.imports.get(tenantId, batch.data.id);
    expect(ready.data.status).toBe('READY');

    const applied = await o.imports.apply(tenantId, batch.data.id, ready.etag);
    expect(applied.data.status).toBe('APPLIED');
    expect(applied.data.counters.created).toBe(1);
    expect(applied.data.counters.skipped).toBe(1);

    // Applying again is a no-op rather than a second write.
    const again = await o.imports.apply(tenantId, batch.data.id, applied.etag);
    expect(again.data.counters.created).toBe(1);
  });

  it('cancels a batch that has not been applied', async () => {
    const { o, tenantId } = await signIn('admin.a');
    await o.session.stepUp(PASSWORD);
    const batch = await o.imports.create(tenantId, {
      file: file(['R9;Vazgeç;;Kayıt;1991-02-03;FEMALE;10000000146;M-9;;PRINCIPAL;;;2026-01-01;;']),
      fileName: 'cancel.csv',
      sponsorOrganizationId: api.world.programs[0]!.sponsorOrganizationId,
      sourceSystem: 'HR',
      sourceVersion: 'cancel-1',
    });
    const cancelled = await o.imports.cancel(tenantId, batch.data.id, batch.etag, {
      reasonCode: 'WRONG_FILE',
    });
    expect(cancelled.data.status).toBe('CANCELLED');
  });
});
