export const WORKLOADS = ['read', 'write', 'eligibility', 'hold', 'import'];
export const LATENCY_LIMITS = { read: 300, write: 700, eligibility: 1500, hold: 1000 };

export function validateFixture(fixture, mode) {
  if (!['smoke', 'load'].includes(mode)) throw new Error('Mode must be smoke or load.');
  if (fixture.version !== 1 || fixture.synthetic !== true) {
    throw new Error('A version 1 synthetic-data fixture is required.');
  }
  if (!/^https?:\/\/[^/@?#]+(?::\d+)?$/.test(fixture.baseUrl ?? '')) {
    throw new Error('Use an HTTP(S) origin without credentials, a path or query.');
  }
  if (!fixture.environment?.name) throw new Error('Name the target environment.');
  if (mode === 'load' && fixture.environment.kind !== 'dedicated-load') {
    throw new Error('Load mode requires a designated dedicated-load environment.');
  }
  for (const workload of WORKLOADS) {
    const cases = fixture.cases?.[workload];
    if (!Array.isArray(cases) || cases.length === 0) throw new Error(`Missing ${workload} cases.`);
    for (const item of cases) {
      if (
        !item.username ||
        !['backoffice', 'provider', 'member'].includes(item.app) ||
        !item.tenantId
      ) {
        throw new Error(`Invalid ${workload} account context.`);
      }
      if ('password' in item || 'cookie' in item || 'csrfToken' in item) {
        throw new Error('Credentials and sessions must not be stored in fixtures.');
      }
      if (workload === 'read' && !/^\/api\/v1\/[a-z-]+(?:\?limit=\d+)?$/.test(item.path ?? '')) {
        throw new Error('Read cases must use a fixed collection path.');
      }
      if (workload !== 'read' && (!item.body || typeof item.body !== 'object')) {
        throw new Error(`Missing ${workload} request body.`);
      }
      if (workload === 'hold' && !item.search)
        throw new Error('A hold needs an availability search for reconciliation.');
      if (
        workload === 'import' &&
        (!Number.isInteger(item.rows) || item.rows < 1 || item.rows > 1000)
      ) {
        throw new Error('Import staging uses 1–1000 synthetic rows per iteration.');
      }
    }
  }
  return fixture;
}

export function accountKey(item) {
  return `${item.app}:${item.tenantId}:${item.username}`;
}

export function uniqueAccounts(fixture, workloads = WORKLOADS) {
  const accounts = new Map();
  for (const workload of workloads) {
    for (const item of fixture.cases[workload]) accounts.set(accountKey(item), item);
  }
  return [...accounts.values()];
}

// Reconciliation keeps the server's decimal lexemes. JSON.parse alone would first
// round a large balance to a JavaScript Number, hiding a one-unit difference.
export function exactJSON(text) {
  JSON.parse(text);
  return JSON.parse(
    text.replace(/"(?:\\.|[^"\\])*"|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g, (token, number) =>
      number === undefined ? token : JSON.stringify(number),
    ),
  );
}

export function canonicalDecimal(value) {
  const match = /^(-?)(\d+)(?:\.(\d*))?(?:[eE]([+-]?\d+))?$/.exec(value);
  if (!match) throw new Error('Invalid exact decimal.');
  const exponent = Number(match[4] ?? 0);
  if (Math.abs(exponent) > 100) throw new Error('Decimal exponent out of bounds.');
  const digits = match[2] + (match[3] ?? '');
  const point = match[2].length + exponent;
  const integer = (point <= 0 ? '0' : digits.slice(0, point).padEnd(point, '0')).replace(
    /^0+(?=\d)/,
    '',
  );
  const fraction = (point < 0 ? '0'.repeat(-point) + digits : digits.slice(point)).replace(
    /0+$/,
    '',
  );
  const sign = integer === '0' && !fraction ? '' : match[1];
  return sign + integer + (fraction ? '.' + fraction : '');
}

export function balanceSnapshot(text) {
  return exactJSON(text)
    .items.map((item) => ({
      id: item.id,
      totalGranted: canonicalDecimal(item.totalGranted),
      available: canonicalDecimal(item.available),
      reserved: canonicalDecimal(item.reserved),
      consumed: canonicalDecimal(item.consumed),
      expired: canonicalDecimal(item.expired),
    }))
    .sort((a, b) => a.id.localeCompare(b.id));
}

export function selectedWorkloads(raw, mode) {
  const selected = raw ? raw.split(',').map((value) => value.trim()) : [...WORKLOADS];
  if (
    !selected.length ||
    new Set(selected).size !== selected.length ||
    selected.some((name) => !WORKLOADS.includes(name))
  ) {
    throw new Error('Choose distinct known workloads.');
  }
  if (mode === 'load' && selected.length !== WORKLOADS.length)
    throw new Error('Load mode requires all five workloads.');
  return selected;
}

export function sameSnapshot(before, after) {
  const canonical = (value) =>
    Array.isArray(value)
      ? value.map(canonical)
      : value && typeof value === 'object'
        ? Object.fromEntries(
            Object.keys(value)
              .sort()
              .map((key) => [key, canonical(value[key])]),
          )
        : value;
  return JSON.stringify(canonical(before)) === JSON.stringify(canonical(after));
}

export function optionsFor(mode, profile = {}, workloads = WORKLOADS) {
  const thresholds = {
    checks: ['rate==1'],
    unexpected_responses: ['rate==0'],
    cleanup_failures: ['count==0'],
    reconciliation_failures: ['count==0'],
  };
  const scenarios = {};
  for (const workload of workloads) {
    thresholds[`workflows_completed{workload:${workload}}`] = ['count>0'];
    if (LATENCY_LIMITS[workload]) {
      thresholds[`operation_latency{workload:${workload},phase:measured}`] = [
        `p(95)<${LATENCY_LIMITS[workload]}`,
      ];
    }
    if (mode === 'smoke') {
      scenarios[workload] = {
        executor: 'shared-iterations',
        vus: 1,
        iterations: 1,
        exec: `${workload}Flow`,
        startTime: `${workloads.indexOf(workload) * 3}s`,
        maxDuration: '60s',
        gracefulStop: '30s',
      };
    } else {
      const settings = profile[workload];
      if (
        !settings ||
        !Number.isInteger(settings.rate) ||
        settings.rate < 1 ||
        !Number.isInteger(settings.maxVUs) ||
        settings.maxVUs < 1
      ) {
        throw new Error(`Specify a positive workflow rate and maxVUs for ${workload}.`);
      }
      scenarios[workload] = {
        executor: 'ramping-arrival-rate',
        startRate: 1,
        timeUnit: '1s',
        preAllocatedVUs: settings.maxVUs,
        maxVUs: settings.maxVUs,
        exec: `${workload}Flow`,
        stages: [
          { duration: '2m', target: settings.rate },
          { duration: '10m', target: settings.rate },
        ],
        gracefulStop: '30s',
      };
    }
  }
  if (mode === 'load') {
    thresholds.dropped_iterations = ['count==0'];
    // The workload scheduler emits iterations, not requests. Measure actual API traffic.
    thresholds['measured_api_requests'] = ['count>=180000'];
  }
  return {
    scenarios,
    thresholds,
    setupTimeout: '30m',
    teardownTimeout: '2m',
    discardResponseBodies: true,
    summaryTrendStats: ['min', 'avg', 'med', 'p(95)', 'p(99)', 'max'],
    systemTags: ['status', 'method', 'name', 'scenario', 'expected_response'],
  };
}

export function csvForRows(count, prefix, from, planCode) {
  const header =
    'source_record_id,first_name,middle_name,last_name,birth_date,sex_at_birth,tckn,member_no,employee_no,membership_type,principal_member_no,relationship,valid_from,valid_to,plan_code';
  const rows = [header];
  for (let i = 0; i < count; i++) {
    // Deterministic synthetic checksum-valid identifiers; never sourced from a person.
    const digits = String(900000000 + i)
      .split('')
      .map(Number);
    const odd = digits[0] + digits[2] + digits[4] + digits[6] + digits[8];
    const even = digits[1] + digits[3] + digits[5] + digits[7];
    digits.push((((odd * 7 - even) % 10) + 10) % 10);
    digits.push(digits.reduce((sum, digit) => sum + digit, 0) % 10);
    rows.push(
      `${prefix}-${i},Load,,Synthetic,1990-01-01,FEMALE,${digits.join('')},${prefix}-${i},${prefix}-${i},EMPLOYEE,,,${from},,${planCode}`,
    );
  }
  return rows.join('\n') + '\n';
}
