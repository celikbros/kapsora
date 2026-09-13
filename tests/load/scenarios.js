import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import {
  WORKLOADS,
  accountKey,
  uniqueAccounts,
  validateFixture,
  optionsFor,
  selectedWorkloads,
  sameSnapshot,
  balanceSnapshot,
  csvForRows,
} from './model.mjs';
import {
  request,
  json,
  header,
  authenticate,
  authenticatedContexts,
  elevate,
  measuredPhase,
  key,
  completed,
  refusals,
  successfulHolds,
  cleanupFailures,
  reconciliationFailures,
  measuredRequests,
} from './client.js';

const mode = __ENV.KAPSORA_LOAD_MODE ?? 'smoke';
const fixture = validateFixture(JSON.parse(open(__ENV.KAPSORA_LOAD_FIXTURE)), mode);
if (!__ENV.KAPSORA_LOAD_PASSWORD || !__ENV.KAPSORA_LOAD_RUN_ID)
  throw new Error('Use the load runner with a password in the environment.');
const workloads = selectedWorkloads(__ENV.KAPSORA_LOAD_WORKLOADS, mode);
export const options = optionsFor(mode, fixture.profile, workloads);
if (mode === 'load') options.thresholds.successful_holds = ['rate>0.95'];

function sessionFor(data, item) {
  return data.sessions[accountKey(item)];
}
function itemFor(workload) {
  const cases = fixture.cases[workload];
  return cases[exec.scenario.iterationInTest % cases.length];
}
function settings(workload, name, extra = {}) {
  return { workload, name, measured: measuredPhase(mode), ...extra };
}
function assert(value, name) {
  if (!check(value, { [name]: (actual) => Boolean(actual) })) throw new Error(name);
}
function snapshot(session, item) {
  const availability = json(
    request(
      fixture.baseUrl,
      session,
      'POST',
      '/api/v1/accommodation/availability/search',
      item.search,
      { name: 'reconcile.availability' },
    ),
  );
  const room = availability.results.find((result) => result.roomType.id === item.body.roomTypeId);
  if (!room) throw new Error('The reconciliation room is missing.');
  const response = request(
    fixture.baseUrl,
    session,
    'GET',
    `/api/v1/people/${item.body.personId}/entitlements?asOf=${item.body.checkIn}`,
    undefined,
    { name: 'reconcile.entitlements' },
  );
  return { availableRooms: room.available, balances: balanceSnapshot(response.body) };
}

export function setup() {
  const ready = http.get(fixture.baseUrl + '/health/ready', {
    tags: { name: 'health.ready' },
    responseType: 'none',
    redirects: 0,
    timeout: '5s',
  });
  assert(ready.status === 200, 'API dependencies are ready');
  cleanupFailures.add(0);
  reconciliationFailures.add(0);
  measuredRequests.add(0);
  const data = { sessions: {}, before: [] };
  try {
    for (const account of uniqueAccounts(fixture, workloads)) {
      data.sessions[accountKey(account)] = authenticate(
        fixture.baseUrl,
        account,
        __ENV.KAPSORA_LOAD_PASSWORD,
      );
      authenticatedContexts.add(1);
      // Authentication is control traffic, never benchmarked or exempted from server limits.
      sleep(1);
    }
    const requiredPermissions = {
      read: ['organization.read'],
      write: ['service_request.create', 'service_request.cancel'],
      eligibility: ['eligibility.check'],
      hold: ['accommodation.booking.create'],
      import: ['import.execute'],
    };
    for (const workload of workloads) {
      for (const item of fixture.cases[workload]) {
        const permissions = sessionFor(data, item).permissions ?? [];
        if (
          !requiredPermissions[workload].every((permission) => permissions.includes(permission))
        ) {
          throw new Error(
            `The ${workload} account lacks a required grant. No workload mutations have started.`,
          );
        }
      }
    }
    for (const item of workloads.includes('hold') ? fixture.cases.hold : [])
      data.before.push(snapshot(sessionFor(data, item), item));
    return data;
  } catch (error) {
    logout(data);
    throw error;
  }
}

function logout(data) {
  for (const session of Object.values(data.sessions)) {
    try {
      request(fixture.baseUrl, session, 'POST', '/api/v1/session/logout', undefined, {
        name: 'session.logout',
        accepted: [204],
      });
    } catch {
      cleanupFailures.add(1);
    }
  }
}

export function readFlow(data) {
  const item = itemFor('read');
  const response = request(
    fixture.baseUrl,
    sessionFor(data, item),
    'GET',
    item.path,
    undefined,
    settings('read', 'collection.read', { primary: true }),
  );
  assert(Array.isArray(json(response).items), 'Read returns a collection');
  completed.add(1, { workload: 'read' });
}

export function eligibilityFlow(data) {
  const item = itemFor('eligibility');
  const result = json(
    request(
      fixture.baseUrl,
      sessionFor(data, item),
      'POST',
      '/api/v1/eligibility/checks',
      item.body,
      settings('eligibility', 'eligibility.check', { primary: true }),
    ),
  );
  assert(
    result.eligible === true && Boolean(result.evaluationId),
    'Eligibility returns a positive stored evaluation',
  );
  completed.add(1, { workload: 'eligibility' });
}

export function writeFlow(data) {
  const item = itemFor('write');
  const session = sessionFor(data, item);
  const response = request(
    fixture.baseUrl,
    session,
    'POST',
    '/api/v1/service-requests',
    item.body,
    settings('write', 'request.create', { primary: true, accepted: [201] }),
  );
  const draft = json(response);
  assert(Boolean(draft.id), 'Created request has an id');
  try {
    assert(draft.status === 'DRAFT', 'Request remains a draft');
  } finally {
    try {
      const result = json(
        request(
          fixture.baseUrl,
          session,
          'POST',
          `/api/v1/service-requests/${draft.id}/cancel`,
          { reasonCode: 'LOAD_TEST_COMPLETE' },
          settings('write', 'request.cancel', {
            headers: { 'If-Match': header(response, 'ETag') },
          }),
        ),
      );
      assert(result.status === 'CANCELLED', 'Draft cleanup is recorded');
    } catch {
      cleanupFailures.add(1);
      exec.test.abort('Cleanup failed; load generation stopped.');
    }
  }
  completed.add(1, { workload: 'write' });
}

export function holdFlow(data) {
  const item = itemFor('hold');
  const session = sessionFor(data, item);
  const response = request(
    fixture.baseUrl,
    session,
    'POST',
    '/api/v1/accommodation/holds',
    item.body,
    settings('hold', 'booking.hold', {
      primary: true,
      accepted: mode === 'load' ? [201, 409] : [201],
    }),
  );
  const booking = json(response);
  if (response.status === 409) {
    const expected = [
      'ROOM_UNAVAILABLE',
      'BOOKING_ALREADY_LIVE',
      'ENTITLEMENT_INSUFFICIENT',
    ].includes(booking.code);
    assert(expected, 'Only documented contention refusals are accepted');
    successfulHolds.add(false);
    refusals.add(1, { workload: 'hold', code: booking.code });
    return;
  }
  successfulHolds.add(true);
  assert(Boolean(booking.id), 'Held booking has an id');
  try {
    assert(booking.status === 'HOLD', 'Booking is held');
  } finally {
    try {
      const released = json(
        request(
          fixture.baseUrl,
          session,
          'POST',
          `/api/v1/accommodation/bookings/${booking.id}/release`,
          undefined,
          settings('hold', 'booking.release'),
        ),
      );
      assert(released.status === 'CANCELLED', 'Held room is released');
    } catch {
      cleanupFailures.add(1);
      exec.test.abort('Cleanup failed; load generation stopped.');
    }
  }
  completed.add(1, { workload: 'hold' });
}

export function importFlow(data) {
  const item = itemFor('import');
  const session = sessionFor(data, item);
  elevate(fixture.baseUrl, session, __ENV.KAPSORA_LOAD_PASSWORD);
  const version = key().slice(0, 60);
  const csv = csvForRows(item.rows, version.slice(0, 24), item.validFrom, item.planCode);
  let response = request(
    fixture.baseUrl,
    session,
    'POST',
    '/api/v1/imports/members',
    {
      ...item.body,
      sourceVersion: version,
      file: http.file(csv, 'synthetic-members.csv', 'text/csv'),
    },
    settings('import', 'import.stage', { multipart: true, primary: true, accepted: [201, 202] }),
  );
  let batch = json(response);
  assert(Boolean(batch.id), 'Staged import has an id');
  try {
    const stop = Date.now() + 15000;
    while (['RECEIVED', 'VALIDATING'].includes(batch.status) && Date.now() < stop) {
      sleep(0.25);
      response = request(
        fixture.baseUrl,
        session,
        'GET',
        `/api/v1/imports/members/${batch.id}`,
        undefined,
        settings('import', 'import.validation'),
      );
      batch = json(response);
    }
    assert(['READY', 'REVIEW'].includes(batch.status), 'Import validation finishes');
    assert(
      batch.rowCount === item.rows && batch.counters.invalid === 0,
      'Synthetic import rows are valid and accounted for',
    );
  } finally {
    try {
      // Refresh the ETag because asynchronous validation may have advanced the version.
      response = request(
        fixture.baseUrl,
        session,
        'GET',
        `/api/v1/imports/members/${batch.id}`,
        undefined,
        settings('import', 'import.get-for-cancel'),
      );
      const cancelled = json(
        request(
          fixture.baseUrl,
          session,
          'POST',
          `/api/v1/imports/members/${batch.id}/cancel`,
          { reasonCode: 'LOAD_TEST_COMPLETE' },
          settings('import', 'import.cancel', {
            headers: { 'If-Match': header(response, 'ETag') },
          }),
        ),
      );
      assert(cancelled.status === 'CANCELLED', 'Import staging is cancelled without applying');
    } catch {
      cleanupFailures.add(1);
      exec.test.abort('Cleanup failed; load generation stopped.');
    }
  }
  completed.add(1, { workload: 'import' });
}

export function teardown(data) {
  try {
    for (let i = 0; i < data.before.length; i++) {
      const item = fixture.cases.hold[i];
      const after = snapshot(sessionFor(data, item), item);
      const same = sameSnapshot(after, data.before[i]);
      reconciliationFailures.add(same ? 0 : 1);
      assert(same, 'Room inventory and exact entitlement balances return to baseline');
    }
  } finally {
    logout(data);
  }
}

export function handleSummary(data) {
  // Only aggregate metrics are persisted. Setup sessions and fixture records are excluded.
  const report = {
    schemaVersion: 1,
    generatedAt: new Date().toISOString(),
    fixtureSha256: __ENV.KAPSORA_LOAD_FIXTURE_SHA256,
    fullCapacityAccepted: false,
    mode,
    runId: __ENV.KAPSORA_LOAD_RUN_ID,
    qualification:
      mode === 'smoke'
        ? workloads.length === 5
          ? 'functional_smoke_only'
          : 'partial_functional_smoke'
        : 'load_candidate_not_full_capacity_acceptance',
    coveredWorkloads: workloads,
    omittedWorkloads: WORKLOADS.filter((name) => !workloads.includes(name)),
    environment: fixture.environment.name,
    revision: __ENV.KAPSORA_LOAD_REVISION,
    workloadSha256: __ENV.KAPSORA_LOAD_WORKLOAD_SHA256,
    workingTreeDirty: __ENV.KAPSORA_LOAD_TREE_DIRTY === 'true',
    k6Version: '2.2.0',
    authenticatedSessions: data.metrics.authenticated_contexts?.values?.count ?? 0,
    caseCounts: Object.fromEntries(
      WORKLOADS.map((workload) => [workload, fixture.cases[workload].length]),
    ),
    datasetCapacityVerified: fixture.dataset?.capacityVerified === true,
    workloadRatesAreIterations: true,
    plannedMeasurementSeconds: mode === 'load' ? 600 : null,
    totalRunSeconds: (data.state?.testRunDurationMs ?? 0) / 1000,
    scheduledScenarios: options.scenarios,
    measuredApiRps:
      mode === 'load' ? (data.metrics.measured_api_requests?.values?.count ?? 0) / 600 : null,
    metrics: data.metrics,
    note: 'Import measures staging and validation, not apply. Notification/outbox SLOs and full dataset/session capacity need separate evidence.',
  };
  return {
    [__ENV.KAPSORA_LOAD_OUTPUT]: JSON.stringify(report, null, 2) + '\n',
    stdout: `KAPSORA ${mode}: aggregate report written. This is not full-capacity acceptance.\n`,
  };
}
