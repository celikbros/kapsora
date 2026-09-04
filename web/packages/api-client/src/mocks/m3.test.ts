/**
 * End-to-end flows through the real client against the M3 mock world: catalog, providers,
 * contracts and the price selection, rules, and the price quote. These are the behaviours
 * a screen will lean on, so a divergence here is a screen that passes its tests and fails
 * in production.
 */
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { createKapsoraClient } from '../client';
import { createOperations, type Operations } from '../operations';
import type { ApiError } from '../problem';
import { createMockServer } from './node';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';
/** A weekday inside the published contract version and outside the winter season. */
const SERVICE_DATE = '2026-06-15';

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.reset());
afterAll(() => server.close());

function ops(): Operations {
  return createOperations(
    createKapsoraClient({ baseUrl: BASE, csrfToken: () => api.session?.csrfToken ?? null }),
  );
}

async function signIn(username: string): Promise<{ o: Operations; tenantId: string }> {
  const o = ops();
  await o.session.login(username, PASSWORD);
  const session = await o.session.get();
  const tenantId = session.activeTenantId ?? (await o.session.tenants())[0]!.id;
  if (!session.activeTenantId) await o.session.switchTenant(tenantId);
  return { o, tenantId };
}

/** Fixture lookups by code, so a test never depends on a generated id. */
function definitionByCode(code: string) {
  const row = api.world.serviceDefinitions.find((d) => d.code === code);
  if (!row) throw new Error(`fixture: no service definition ${code}`);
  return row;
}
function locationByCode(code: string) {
  const row = api.world.providerLocations.find((l) => l.code === code);
  if (!row) throw new Error(`fixture: no provider location ${code}`);
  return row;
}
function theProvider() {
  const row = api.world.providers[0];
  if (!row) throw new Error('fixture: no provider');
  return row;
}
function theContract() {
  const row = api.world.contracts[0];
  if (!row) throw new Error('fixture: no contract');
  return row;
}
function contractVersion(status: 'PUBLISHED' | 'DRAFT') {
  const row = api.world.contractVersions.find((v) => v.status === status);
  if (!row) throw new Error(`fixture: no ${status} contract version`);
  return row;
}
function theRuleSetVersion() {
  const row = api.world.ruleSetVersions[0];
  if (!row) throw new Error('fixture: no rule set version');
  return row;
}
function principalPersonId(tenantId: string): string {
  const membership = api.world.memberships.find(
    (m) => m.tenantId === tenantId && m.principalMembershipId === null,
  );
  if (!membership) throw new Error('fixture: no principal membership');
  return membership.personId;
}

describe('catalog', () => {
  it('serves the category tree, refuses a duplicate code and refuses a cycle', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const page = await o.catalog.listCategories(tenantId, { limit: 100 });
    const byCode = new Map(page.items.map((c) => [c.code, c]));
    const physio = byCode.get('HEALTH_PHYSIO');
    const outpatient = byCode.get('HEALTH_OUTPATIENT');
    const health = byCode.get('HEALTH');
    expect(physio?.parentId).toBe(outpatient?.id);
    expect(outpatient?.parentId).toBe(health?.id);
    expect(health?.parentId).toBeNull();

    const children = await o.catalog.listCategories(tenantId, { parentId: health!.id });
    expect(children.items.map((c) => c.code).sort()).toEqual([
      'HEALTH_IMAGING',
      'HEALTH_OUTPATIENT',
    ]);

    const taken = (await o.catalog
      .createCategory(tenantId, { code: 'HEALTH', name: 'Yeni', domain: 'HEALTH' })
      .catch((e: unknown) => e)) as ApiError;
    expect(taken.status).toBe(409);
    expect(taken.problem.code).toBe('CATEGORY_CODE_TAKEN');

    // Moving HEALTH under its own grandchild would close a loop.
    const current = await o.catalog.getCategory(tenantId, health!.id);
    const cycle = (await o.catalog
      .patchCategory(tenantId, health!.id, current.etag, { parentId: physio!.id })
      .catch((e: unknown) => e)) as ApiError;
    expect(cycle.status).toBe(409);
    expect(cycle.problem.code).toBe('CATEGORY_CYCLE');
  });

  it('keeps a service definition code immutable and reads values as of a date', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const definition = await o.catalog.getDefinition(tenantId, definitionByCode('GP_VISIT').id);
    expect(definition.data.categoryCode).toBe('HEALTH_OUTPATIENT');
    expect(definition.data.domain).toBe('HEALTH');

    const immutable = (await o.catalog
      .patchDefinition(tenantId, definition.data.id, definition.etag, {
        code: 'GP_VISIT_2',
      } as never)
      .catch((e: unknown) => e)) as ApiError;
    expect(immutable.status).toBe(422);
    expect(immutable.fieldErrors().get('code')?.code).toBe('IMMUTABLE');

    const system = api.world.codeSystems[0]!;
    const in2026 = await o.catalog.listCodeValues(tenantId, system.id, { asOf: '2026-06-01' });
    const in2027 = await o.catalog.listCodeValues(tenantId, system.id, { asOf: '2027-06-01' });
    expect(in2026.asOf).toBe('2026-06-01');
    expect(in2026.items.map((v) => v.code)).toContain('520031');
    expect(in2027.items.map((v) => v.code)).not.toContain('520031');
  });

  it('refuses overlapping code mappings and then stores a clean set', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const definition = await o.catalog.getDefinition(tenantId, definitionByCode('MRI_SCAN').id);
    const system = api.world.codeSystems[0]!;

    const overlap = (await o.catalog
      .replaceCodeMappings(tenantId, definition.data.id, definition.etag, [
        { codeSystemId: system.id, code: '803930', validFrom: '2026-01-01', primary: true },
        { codeSystemId: system.id, code: '803930', validFrom: '2026-06-01', primary: false },
      ])
      .catch((e: unknown) => e)) as ApiError;
    expect(overlap.status).toBe(409);
    expect(overlap.problem.code).toBe('CODE_MAPPING_OVERLAP');
    expect(
      api.world.codeMappings.filter((m) => m.serviceDefinitionId === definition.data.id),
    ).toHaveLength(0);

    const stored = await o.catalog.replaceCodeMappings(
      tenantId,
      definition.data.id,
      definition.etag,
      [
        {
          codeSystemId: system.id,
          code: '803930',
          validFrom: '2026-01-01',
          validTo: '2026-06-01',
          primary: true,
        },
        { codeSystemId: system.id, code: '803931', validFrom: '2026-06-01', primary: true },
      ],
    );
    expect(stored.data).toHaveLength(2);
    expect(stored.data[0]!.codeSystemCode).toBe('SUT');
    expect(stored.etag).not.toBe(definition.etag);
  });

  it('imports code values all-or-nothing', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const system = api.world.codeSystems[0]!;
    const before = api.world.codeValues.length;

    const rejected = await o.catalog.importCodeValues(tenantId, system.id, [
      { code: 'NEW1', display: 'Yeni 1', validFrom: '2026-01-01' },
      { code: '', display: 'Kodsuz', validFrom: '2026-01-01' },
    ]);
    expect(rejected.created).toBe(0);
    expect(rejected.errors[0]).toMatchObject({ index: 1, field: 'code', code: 'REQUIRED' });
    expect(api.world.codeValues).toHaveLength(before);

    const accepted = await o.catalog.importCodeValues(tenantId, system.id, [
      { code: 'NEW1', display: 'Yeni 1', validFrom: '2026-01-01' },
      { code: '520030', display: 'Fizik tedavi seansı (güncel)', validFrom: '2026-01-01' },
    ]);
    expect(accepted.created).toBe(1);
    expect(accepted.updated).toBe(1);
  });
});

describe('providers', () => {
  it('finds a location through a category capability and through a definition one', async () => {
    const { o, tenantId } = await signIn('admin.a');

    const viaCategory = await o.providers.search(tenantId, {
      serviceDefinitionId: definitionByCode('PHYSIO_SESSION').id,
      asOf: SERVICE_DATE,
    });
    expect(viaCategory.items).toHaveLength(1);
    expect(viaCategory.items[0]!.matchedVia).toBe('CATEGORY');
    expect(viaCategory.items[0]!.locationCode).toBe('IST-01');

    const viaDefinition = await o.providers.search(tenantId, {
      serviceDefinitionId: definitionByCode('MRI_SCAN').id,
      asOf: SERVICE_DATE,
    });
    expect(viaDefinition.items[0]!.matchedVia).toBe('DEFINITION');

    // Nothing at that location can deliver the isolated pricing fixture.
    const nothing = await o.providers.search(tenantId, {
      serviceDefinitionId: definitionByCode('LAB_PANEL_AMBIGUOUS').id,
      asOf: SERVICE_DATE,
    });
    expect(nothing.items).toHaveLength(0);
  });

  it('refuses overlapping capabilities and drops a suspended provider from the search', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const location = await o.providers.getLocation(tenantId, locationByCode('IST-01').id);
    const physio = definitionByCode('PHYSIO_SESSION');

    const overlap = (await o.providers
      .replaceCapabilities(tenantId, location.data.id, location.etag, [
        { serviceDefinitionId: physio.id, validFrom: '2026-01-01' },
        { serviceDefinitionId: physio.id, validFrom: '2026-06-01' },
      ])
      .catch((e: unknown) => e)) as ApiError;
    expect(overlap.status).toBe(409);
    expect(overlap.problem.code).toBe('CAPABILITY_OVERLAP');

    const provider = await o.providers.get(tenantId, theProvider().id);
    const suspended = await o.providers.suspend(tenantId, provider.data.id, provider.etag, {
      reasonCode: 'AUDIT',
    });
    expect(suspended.data.status).toBe('SUSPENDED');
    const empty = await o.providers.search(tenantId, {
      serviceDefinitionId: physio.id,
      asOf: SERVICE_DATE,
    });
    expect(empty.items).toHaveLength(0);

    const terminated = await o.providers.terminate(tenantId, provider.data.id, suspended.etag, {
      reasonCode: 'CONTRACT_ENDED',
    });
    const again = (await o.providers
      .activate(tenantId, provider.data.id, terminated.etag)
      .catch((e: unknown) => e)) as ApiError;
    expect(again.status).toBe(409);
    expect(again.problem.code).toBe('PROVIDER_TRANSITION_INVALID');
  });

  it('masks the registration number and needs a step-up to search by it', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const page = await o.providers.listPractitioners(tenantId, theProvider().id);
    expect(page.items).toHaveLength(3);
    for (const p of page.items) {
      expect(p.maskedRegistrationNumber).toMatch(/^\d{2}\*+$/);
      expect(JSON.stringify(p)).not.toContain('10045001');
    }

    const denied = (await o.providers
      .searchByRegistration(tenantId, {
        registrationAuthority: 'TTB',
        registrationNumber: '10045001',
      })
      .catch((e: unknown) => e)) as ApiError;
    expect(denied.status).toBe(403);
    expect(denied.problem.code).toBe('STEP_UP_REQUIRED');

    await o.session.stepUp(PASSWORD);
    const found = await o.providers.searchByRegistration(tenantId, {
      registrationAuthority: 'TTB',
      registrationNumber: '10045001',
    });
    expect(found.fullName).toBe('Elif Şahin');
    expect(found.locations?.[0]!.locationCode).toBe('IST-01');
  });
});

describe('contracts', () => {
  it('hides a draft version from a caller holding only contract.read', async () => {
    const draft = contractVersion('DRAFT');
    const published = contractVersion('PUBLISHED');

    const manager = await signIn('admin.a');
    const all = await manager.o.contracts.listVersions(manager.tenantId, theContract().id);
    expect(all.map((v) => v.status).sort()).toEqual(['DRAFT', 'PUBLISHED']);

    const reader = await signIn('reviewer.a');
    const visible = await reader.o.contracts.listVersions(reader.tenantId, theContract().id);
    expect(visible.map((v) => v.id)).toEqual([published.id]);

    // 404 rather than 403: an unagreed price does not announce its own existence.
    const hidden = (await reader.o.contracts
      .getVersion(reader.tenantId, draft.id)
      .catch((e: unknown) => e)) as ApiError;
    expect(hidden.status).toBe(404);
    expect(hidden.problem.code).toBe('RESOURCE_NOT_FOUND');
  });

  it('walks a maker-checker publish and freezes the version afterwards', async () => {
    const maker = await signIn('admin.a');
    const contract = await maker.o.contracts.create(maker.tenantId, {
      code: 'TEST-CONTRACT',
      name: 'Test Sözleşmesi',
      payerOrganizationId: theContract().payerOrganizationId,
      providerProfileId: theProvider().id,
      domainCode: 'HEALTH',
    });
    const version = await maker.o.contracts.createVersion(maker.tenantId, contract.data.id, {
      validFrom: '2028-01-01',
      currencyCode: 'TRY',
    });

    // An empty sheet cannot be reviewed: publishing it would make every lookup fail.
    const empty = (await maker.o.contracts
      .submitVersion(maker.tenantId, version.data.id, version.etag)
      .catch((e: unknown) => e)) as ApiError;
    expect(empty.status).toBe(422);
    expect(empty.fieldErrors().get('priceLists')?.code).toBe('PRICE_ITEMS_REQUIRED');

    const lists = await maker.o.contracts.replacePriceLists(
      maker.tenantId,
      version.data.id,
      version.etag,
      [{ code: 'STD', name: 'Standart' }],
    );
    const listId = lists.data[0]!.id;
    const listPage = await maker.o.contracts.listPriceItems(maker.tenantId, listId);
    const items = await maker.o.contracts.replacePriceItems(maker.tenantId, listId, listPage.etag, [
      {
        serviceDefinitionId: definitionByCode('GP_VISIT').id,
        unitType: 'COUNT',
        pricingMethod: 'FIXED',
        amount: '640.500000',
        validFrom: '2028-01-01',
      },
    ]);
    expect(items.data[0]!.amount).toBe('640.500000');

    const afterLists = await maker.o.contracts.getVersion(maker.tenantId, version.data.id);
    const submitted = await maker.o.contracts.submitVersion(
      maker.tenantId,
      version.data.id,
      afterLists.etag,
    );
    expect(submitted.data.status).toBe('UNDER_REVIEW');

    const withoutStepUp = (await maker.o.contracts
      .publishVersion(maker.tenantId, version.data.id, submitted.etag)
      .catch((e: unknown) => e)) as ApiError;
    expect(withoutStepUp.problem.code).toBe('STEP_UP_REQUIRED');

    await maker.o.session.stepUp(PASSWORD);
    const sameActor = (await maker.o.contracts
      .publishVersion(maker.tenantId, version.data.id, submitted.etag)
      .catch((e: unknown) => e)) as ApiError;
    expect(sameActor.status).toBe(403);
    expect(sameActor.problem.code).toBe('MAKER_CHECKER_SAME_ACTOR');

    const checker = await signIn('both.ab');
    await checker.o.session.stepUp(PASSWORD);
    const publishedVersion = await checker.o.contracts.publishVersion(
      checker.tenantId,
      version.data.id,
      submitted.etag,
    );
    expect(publishedVersion.data.status).toBe('PUBLISHED');
    expect(publishedVersion.data.configurationHash).not.toBeNull();

    const frozen = (await checker.o.contracts
      .patchVersion(checker.tenantId, version.data.id, publishedVersion.etag, { notes: 'geç' })
      .catch((e: unknown) => e)) as ApiError;
    expect(frozen.status).toBe(409);
    expect(frozen.problem.code).toBe('CONTRACT_VERSION_IMMUTABLE');

    const frozenItems = (await checker.o.contracts
      .replacePriceItems(checker.tenantId, listId, items.etag, [])
      .catch((e: unknown) => e)) as ApiError;
    expect(frozenItems.problem.code).toBe('CONTRACT_VERSION_IMMUTABLE');
  });

  it('reads packages, quotas and the payment term of the published version', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const version = contractVersion('PUBLISHED');

    const packages = await o.contracts.listPackageDefinitions(tenantId, version.id);
    expect(packages.data[0]!.code).toBe('CHECKUP');
    expect(packages.data[0]!.lines.map((l) => l.includedQuantity)).toEqual([
      '1.000000',
      '4.000000',
    ]);

    const quotas = await o.contracts.listProviderQuotas(tenantId, version.id);
    expect(quotas.data[0]!.capacity).toBe('1200.000000');
    expect(quotas.data[0]!.consumed).toBe('318.000000');

    const term = await o.contracts.getPaymentTerm(tenantId, version.id);
    expect(term.data.dueDays).toBe(30);
    expect(term.data.vatRate).toBe('10.00');

    const draft = contractVersion('DRAFT');
    const none = (await o.contracts
      .getPaymentTerm(tenantId, draft.id)
      .catch((e: unknown) => e)) as ApiError;
    expect(none.status).toBe(404);
  });
});

describe('price selection', () => {
  it('prefers the definition, then the location, then the category', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const provider = theProvider();

    const physio = await o.contracts.resolvePrice(tenantId, {
      serviceDate: SERVICE_DATE,
      providerProfileId: provider.id,
      serviceDefinitionId: definitionByCode('PHYSIO_SESSION').id,
    });
    expect(physio.outcome).toBe('MATCHED');
    expect(physio.winner?.matchedVia).toBe('DEFINITION');
    expect(physio.winner?.amount).toBe('750.000000');
    expect(physio.winner?.memberSharePercent).toBe('20.000000');

    const mriAnywhere = await o.contracts.resolvePrice(tenantId, {
      serviceDate: SERVICE_DATE,
      providerProfileId: provider.id,
      serviceDefinitionId: definitionByCode('MRI_SCAN').id,
    });
    expect(mriAnywhere.winner?.amount).toBe('2900.000000');

    const mriHere = await o.contracts.resolvePrice(tenantId, {
      serviceDate: SERVICE_DATE,
      providerProfileId: provider.id,
      serviceDefinitionId: definitionByCode('MRI_SCAN').id,
      locationId: locationByCode('IST-01').id,
    });
    expect(mriHere.winner?.amount).toBe('2500.000000');
    expect(mriHere.winner?.locationId).toBe(locationByCode('IST-01').id);

    // GP_VISIT has no price of its own and resolves through its category.
    const gp = await o.contracts.resolvePrice(tenantId, {
      serviceDate: SERVICE_DATE,
      providerProfileId: provider.id,
      serviceDefinitionId: definitionByCode('GP_VISIT').id,
    });
    expect(gp.winner?.matchedVia).toBe('CATEGORY');
    expect(gp.winner?.amount).toBe('500.000000');
    expect(gp.considered.some((c) => c.excluded === 'SERVICE_MISMATCH')).toBe(true);

    // Inside the winter season the list carries a price naming the definition itself.
    const gpInWinter = await o.contracts.resolvePrice(tenantId, {
      serviceDate: '2026-12-15',
      providerProfileId: provider.id,
      serviceDefinitionId: definitionByCode('GP_VISIT').id,
    });
    expect(gpInWinter.winner?.matchedVia).toBe('DEFINITION');
    expect(gpInWinter.winner?.amount).toBe('900.000000');
    expect(gp.considered.some((c) => c.excluded === 'SEASON_MISMATCH')).toBe(true);
  });

  it('never picks a winner between two equally specific prices', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const result = await o.contracts.resolvePrice(tenantId, {
      serviceDate: SERVICE_DATE,
      providerProfileId: theProvider().id,
      serviceDefinitionId: definitionByCode('LAB_PANEL_AMBIGUOUS').id,
    });
    expect(result.outcome).toBe('REVIEW_REQUIRED');
    expect(result.reason).toBe('PRICE_AMBIGUOUS');
    expect(result.winner).toBeUndefined();
    expect(result.tied).toHaveLength(2);
    expect(result.tied.map((t) => t.amount).sort()).toEqual(['1200.000000', '1350.000000']);
    expect(new Set(result.tied.map((t) => t.priceItemId)).size).toBe(2);
  });

  it('answers PRICE_NOT_FOUND outside the contract period', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const result = await o.contracts.resolvePrice(tenantId, {
      serviceDate: '2025-06-15',
      providerProfileId: theProvider().id,
      serviceDefinitionId: definitionByCode('PHYSIO_SESSION').id,
    });
    expect(result.outcome).toBe('NOT_FOUND');
    expect(result.reason).toBe('PRICE_NOT_FOUND');
    expect(result.considered).toHaveLength(0);
  });
});

describe('rules', () => {
  it('runs the seeded test cases and simulates without writing anything', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const version = theRuleSetVersion();
    const evaluationsBefore = api.world.ruleEvaluations.length;

    const run = await o.rules.runTests(tenantId, version.id);
    expect(run.passed).toBe(true);
    expect(run.total).toBe(2);
    expect(run.failed).toBe(0);

    const trace = await o.rules.simulate(tenantId, version.id, {
      serviceCode: 'PHYSIO_SESSION',
      quantity: '10',
      requestedAmount: '7500.000000',
    });
    expect(trace.outcome).toBe('REVIEW_REQUIRED');
    expect(trace.results.filter((r) => r.matched).map((r) => r.explanationCode)).toEqual([
      'DOCUMENT_REQUIRED',
      'AMOUNT_ABOVE_THRESHOLD',
    ]);

    const quiet = await o.rules.simulate(tenantId, version.id, {
      serviceCode: 'PHYSIO_SESSION',
      quantity: '2',
      requestedAmount: '1500.000000',
    });
    expect(quiet.outcome).toBe('APPROVED');

    // Neither call may leave a row behind.
    expect(api.world.ruleEvaluations).toHaveLength(evaluationsBefore);
  });

  it('refuses to submit a version with no test case, or with a failing one', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const set = await o.rules.createSet(tenantId, {
      code: 'TEST_RULES',
      name: 'Test Kuralları',
      domainCode: 'HEALTH',
      purpose: 'ELIGIBILITY',
    });
    const version = await o.rules.createVersion(tenantId, set.data.id, {
      validFrom: '2028-01-01',
      inputSchema: { age: 'int' },
    });

    const rules = await o.rules.replaceRules(tenantId, version.data.id, version.etag, [
      {
        code: 'ADULT_ONLY',
        name: 'Yalnız yetişkin',
        priority: 10,
        condition: 'age < 18',
        actions: [{ type: 'REJECT' }],
        explanationCode: 'AGE_BELOW_LIMIT',
      },
    ]);

    const noTests = (await o.rules
      .submitVersion(tenantId, version.data.id, rules.etag)
      .catch((e: unknown) => e)) as ApiError;
    expect(noTests.status).toBe(422);
    expect(noTests.fieldErrors().get('testCases')?.code).toBe('TESTS_REQUIRED');

    const failing = await o.rules.replaceTestCases(tenantId, version.data.id, rules.etag, [
      { code: 'CHILD', input: { age: '10' }, expectedOutcome: 'APPROVED' },
    ]);
    const failed = (await o.rules
      .submitVersion(tenantId, version.data.id, failing.etag)
      .catch((e: unknown) => e)) as ApiError;
    expect(failed.status).toBe(422);
    const error = failed.fieldErrors().get('testCases');
    expect(error?.code).toBe('TESTS_FAILING');
    expect(error?.message).toContain('CHILD');

    const passing = await o.rules.replaceTestCases(tenantId, version.data.id, failing.etag, [
      {
        code: 'CHILD',
        input: { age: '10' },
        expectedOutcome: 'REJECTED',
        expectedExplanations: ['AGE_BELOW_LIMIT'],
      },
      { code: 'ADULT', input: { age: '30' }, expectedOutcome: 'APPROVED' },
    ]);
    const submitted = await o.rules.submitVersion(tenantId, version.data.id, passing.etag);
    expect(submitted.data.status).toBe('UNDER_REVIEW');

    const checker = await signIn('both.ab');
    await checker.o.session.stepUp(PASSWORD);
    const published = await checker.o.rules.publishVersion(
      checker.tenantId,
      version.data.id,
      submitted.etag,
    );
    expect(published.data.status).toBe('PUBLISHED');
    expect(published.data.contentHash).not.toBeNull();

    const frozen = (await checker.o.rules
      .replaceRules(checker.tenantId, version.data.id, published.etag, [])
      .catch((e: unknown) => e)) as ApiError;
    expect(frozen.status).toBe(409);
    expect(frozen.problem.code).toBe('RULE_VERSION_IMMUTABLE');
  });

  it('refuses a condition naming a variable the input schema does not declare', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const set = await o.rules.createSet(tenantId, {
      code: 'SCHEMA_RULES',
      name: 'Şema Kuralları',
      domainCode: 'HEALTH',
      purpose: 'DOCUMENT',
    });
    const version = await o.rules.createVersion(tenantId, set.data.id, {
      inputSchema: { age: 'int' },
    });

    const undeclared = (await o.rules
      .replaceRules(tenantId, version.data.id, version.etag, [
        {
          code: 'BAD',
          name: 'Tanımsız değişken',
          priority: 10,
          condition: 'diagnosis == "J45"',
          explanationCode: 'NOPE',
        },
      ])
      .catch((e: unknown) => e)) as ApiError;
    expect(undeclared.status).toBe(422);
    expect(undeclared.fieldErrors().get('items[0].condition')?.code).toBe('UNDECLARED_VARIABLE');

    const unparsable = (await o.rules
      .replaceRules(tenantId, version.data.id, version.etag, [
        {
          code: 'WORSE',
          name: 'Derlenmeyen koşul',
          priority: 10,
          condition: 'age.matches(',
          explanationCode: 'NOPE',
        },
      ])
      .catch((e: unknown) => e)) as ApiError;
    expect(unparsable.fieldErrors().get('items[0].condition')?.code).toBe(
      'CONDITION_COMPILE_FAILED',
    );

    const duplicatePriority = (await o.rules
      .replaceRules(tenantId, version.data.id, version.etag, [
        { code: 'A', name: 'A', priority: 10, condition: 'age > 1', explanationCode: 'A_CODE' },
        { code: 'B', name: 'B', priority: 10, condition: 'age > 2', explanationCode: 'B_CODE' },
      ])
      .catch((e: unknown) => e)) as ApiError;
    expect(duplicatePriority.fieldErrors().get('items[1].priority')?.code).toBe(
      'PRIORITY_DUPLICATE',
    );
  });

  it('reads back a recorded evaluation with no identity data in its snapshot', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const recorded = api.world.ruleEvaluations[0]!;
    const evaluation = await o.rules.getEvaluation(tenantId, recorded.id);
    expect(evaluation.outcome).toBe('REVIEW_REQUIRED');
    expect(evaluation.results).toHaveLength(2);
    expect(Object.keys(evaluation.inputSnapshot).sort()).toEqual([
      'quantity',
      'requestedAmount',
      'serviceCode',
    ]);
  });
});

describe('price quotes', () => {
  /** A deep copy of everything a reservation would have moved. */
  function balancesSnapshot(): string {
    return JSON.stringify({
      accounts: api.world.entitlementAccounts,
      ledger: api.world.ledgerEntries.length,
    });
  }

  it('quotes a line, splits the member share and reserves nothing', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const before = balancesSnapshot();

    const quote = await o.pricing.createQuote(tenantId, {
      personId: principalPersonId(tenantId),
      providerProfileId: theProvider().id,
      locationId: locationByCode('IST-01').id,
      serviceDate: SERVICE_DATE,
      items: [{ serviceDefinitionId: definitionByCode('PHYSIO_SESSION').id, quantity: '2.000000' }],
      context: { entitlementCode: 'HEALTH_MONEY' },
    });

    expect(quote.outcome).toBe('QUOTED');
    // 750 per session × 2 sessions, 20% of which the member carries.
    expect(quote.contractAmount).toBe('1500.000000');
    expect(quote.memberAmount).toBe('300.000000');
    expect(quote.payerAmount).toBe('1200.000000');
    expect(quote.currencyCode).toBe('TRY');
    expect(quote.items[0]!.explanations.map((e) => e.code)).toContain('MEMBER_SHARE_APPLIED');
    expect(quote.disclaimer.length).toBeGreaterThan(0);
    expect(quote.expired).toBe(false);
    expect(quote.planVersionId).not.toBeNull();

    // The single most important property of the endpoint.
    expect(balancesSnapshot()).toBe(before);

    const read = await o.pricing.getQuote(tenantId, quote.id);
    expect(read.id).toBe(quote.id);
    expect(read.memberAmount).toBe('300.000000');
  });

  it('caps what the plan carries at the balance and marks the line PARTIAL', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const before = balancesSnapshot();

    // Ten sessions at 750 is 7500; the member's 20% leaves 6000 for the plan, and the
    // seeded HEALTH_MONEY balance is 1250.
    const quote = await o.pricing.createQuote(tenantId, {
      personId: principalPersonId(tenantId),
      providerProfileId: theProvider().id,
      serviceDate: SERVICE_DATE,
      items: [
        { serviceDefinitionId: definitionByCode('PHYSIO_SESSION').id, quantity: '10.000000' },
      ],
      context: { entitlementCode: 'HEALTH_MONEY' },
    });
    expect(quote.outcome).toBe('PARTIAL');
    expect(quote.contractAmount).toBe('7500.000000');
    expect(quote.payerAmount).toBe('1250.000000');
    expect(quote.memberAmount).toBe('6250.000000');
    expect(quote.items[0]!.explanations.map((e) => e.code)).toContain('ENTITLEMENT_LIMIT_APPLIED');
    expect(balancesSnapshot()).toBe(before);
  });

  it('carries no member figure when a line is ambiguous or has no price', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const before = balancesSnapshot();

    const ambiguous = await o.pricing.createQuote(tenantId, {
      personId: principalPersonId(tenantId),
      providerProfileId: theProvider().id,
      serviceDate: SERVICE_DATE,
      items: [
        { serviceDefinitionId: definitionByCode('LAB_PANEL_AMBIGUOUS').id, quantity: '1.000000' },
      ],
    });
    expect(ambiguous.outcome).toBe('REVIEW_REQUIRED');
    expect(ambiguous.items[0]!.explanations[0]!.code).toBe('PRICE_AMBIGUOUS');
    expect(ambiguous.memberAmount).toBe('0.000000');
    expect(ambiguous.payerAmount).toBe('0.000000');

    const missing = await o.pricing.createQuote(tenantId, {
      personId: principalPersonId(tenantId),
      providerProfileId: theProvider().id,
      serviceDate: '2025-06-15',
      items: [{ serviceDefinitionId: definitionByCode('PHYSIO_SESSION').id, quantity: '1.000000' }],
    });
    expect(missing.outcome).toBe('REVIEW_REQUIRED');
    expect(missing.items[0]!.explanations[0]!.code).toBe('PRICE_NOT_FOUND');

    expect(balancesSnapshot()).toBe(before);
  });

  it('still prices the service for somebody the plan does not cover', async () => {
    const { o, tenantId } = await signIn('admin.a');
    const stranger = api.world.people.find(
      (p) => p.tenantId === tenantId && !api.world.enrollments.some((e) => e.personId === p.id),
    );
    if (!stranger) throw new Error('fixture: everyone is enrolled');

    const quote = await o.pricing.createQuote(tenantId, {
      personId: stranger.id,
      providerProfileId: theProvider().id,
      serviceDate: SERVICE_DATE,
      items: [{ serviceDefinitionId: definitionByCode('GP_VISIT').id, quantity: '1.000000' }],
    });
    expect(quote.outcome).toBe('NOT_ELIGIBLE');
    expect(quote.contractAmount).toBe('500.000000');
    expect(quote.payerAmount).toBe('0.000000');
    expect(quote.memberAmount).toBe('500.000000');
    expect(quote.items[0]!.explanations.map((e) => e.code)).toContain('NOT_ELIGIBLE');
  });
});
