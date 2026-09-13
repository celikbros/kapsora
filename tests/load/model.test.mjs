import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import {
  balanceSnapshot,
  canonicalDecimal,
  sameSnapshot,
  selectedWorkloads,
  exactJSON,
  csvForRows,
  validateFixture,
  optionsFor,
  uniqueAccounts,
} from './model.mjs';

function fixture() {
  const account = { username: 'synthetic', app: 'backoffice', tenantId: 'synthetic-tenant' };
  return {
    version: 1,
    synthetic: true,
    baseUrl: 'http://127.0.0.1:8090',
    environment: { name: 'unit', kind: 'local' },
    cases: {
      read: [{ ...account, path: '/api/v1/organizations?limit=25' }],
      write: [{ ...account, body: {} }],
      eligibility: [{ ...account, body: {} }],
      hold: [{ ...account, body: {}, search: {} }],
      import: [{ ...account, body: {}, rows: 5 }],
    },
  };
}

describe('load evidence and configuration', () => {
  it('allows an explicitly partial smoke but never silently drops a load workload', () => {
    assert.deepEqual(selectedWorkloads('read,hold', 'smoke'), ['read', 'hold']);
    assert.throws(() => selectedWorkloads('read,hold', 'load'), /all five/);
    assert.throws(() => selectedWorkloads('read,read', 'smoke'), /distinct/);
  });
  it('refuses a full load against an ordinary local fixture', () => {
    assert.throws(() => validateFixture(fixture(), 'load'), /designated/);
    assert.equal(validateFixture(fixture(), 'smoke').synthetic, true);
  });
  it('rejects missing work, embedded passwords, credentials in the URL and unsafe read URLs', () => {
    for (const modify of [
      (f) => {
        f.cases.hold = [];
      },
      (f) => {
        f.cases.read[0].password = 'synthetic-password';
      },
      (f) => {
        f.baseUrl = 'http://user:password@localhost';
      },
      (f) => {
        f.cases.read[0].path = '/api/v1/people/search-by-identifier?identifier=synthetic';
      },
    ]) {
      const f = fixture();
      modify(f);
      assert.throws(() => validateFixture(f, 'smoke'));
    }
  });
  it('does not count one reused account as thousands of authenticated sessions', () => {
    assert.equal(uniqueAccounts(fixture()).length, 1);
  });
  it('keeps smoke small and requires every workload to complete', () => {
    const options = optionsFor('smoke');
    assert.equal(Object.keys(options.scenarios).length, 5);
    for (const scenario of Object.values(options.scenarios)) {
      assert.equal(scenario.iterations, 1);
      assert.equal(scenario.vus, 1);
    }
    assert.deepEqual(options.thresholds.cleanup_failures, ['count==0']);
    assert.deepEqual(options.thresholds['workflows_completed{workload:hold}'], ['count>0']);
    assert.equal(options.systemTags.includes('url'), false);
  });
  it('measures requests separately from scheduled workflows and fails on dropped work', () => {
    const f = fixture();
    const profile = Object.fromEntries(
      Object.keys(f.cases).map((name) => [name, { rate: 1, maxVUs: 1 }]),
    );
    const options = optionsFor('load', profile);
    assert.deepEqual(options.thresholds.measured_api_requests, ['count>=180000']);
    assert.deepEqual(options.thresholds.dropped_iterations, ['count==0']);
    assert.throws(() => optionsFor('load'), /Specify/);
  });
});

describe('exact reconciliation', () => {
  it('ignores object key reordering during k6 setup serialization but catches value changes', () => {
    assert.equal(
      sameSnapshot(
        { availableRooms: 5, balances: [{ id: 'a', reserved: '0' }] },
        { balances: [{ reserved: '0', id: 'a' }], availableRooms: 5 },
      ),
      true,
    );
    assert.equal(sameSnapshot({ reserved: '0' }, { reserved: '1' }), false);
  });
  it('compares decimal values without treating harmless scale changes as lost money', () => {
    assert.equal(canonicalDecimal('10.000000'), canonicalDecimal('10'));
    assert.equal(canonicalDecimal('1.000e-6'), '0.000001');
    assert.equal(canonicalDecimal('-0.00'), '0');
    assert.equal(canonicalDecimal('1.2e3'), '1200');
  });
  it('preserves decimal differences beyond Number precision and leaves quoted values alone', () => {
    const before =
      '{"items":[{"id":"a","available":9007199254740993.000001,"reserved":0,"consumed":0,"expired":0,"totalGranted":9007199254740993.000001}]}';
    const after = before.replaceAll('9007199254740993.000001', '9007199254740993.000002');
    assert.notDeepEqual(balanceSnapshot(before), balanceSnapshot(after));
    assert.equal(balanceSnapshot(before)[0].available, '9007199254740993.000001');
    assert.equal(exactJSON('{"text":"a \\"12\\" value","number":12}').text, 'a "12" value');
  });
  it('refuses malformed JSON instead of silently normalizing it', () => {
    assert.throws(() => exactJSON('{"value":01}'));
  });
  it('generates parseable synthetic CSV rows with checksum-valid identifiers', () => {
    const rows = csvForRows(5, 'load-unit', '2026-09-13', 'DEMO_STANDARD').trim().split('\n');
    assert.equal(rows.length, 6);
    for (const row of rows.slice(1)) {
      const fields = row.split(',');
      assert.equal(fields.length, 15);
      const digits = fields[6].split('').map(Number);
      assert.equal(digits.length, 11);
      assert.equal(digits.slice(0, 10).reduce((sum, digit) => sum + digit, 0) % 10, digits[10]);
    }
  });
});
