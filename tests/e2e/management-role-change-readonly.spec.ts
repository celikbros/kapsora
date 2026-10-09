import { expect, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const localOperatorUrl = (() => {
  try {
    const url = new URL(base);
    return url.protocol === 'http:' && url.hostname === '127.0.0.1' && url.port === '5181';
  } catch {
    return false;
  }
})();
const enabled =
  process.env['E2E_REAL_API'] === '1' &&
  process.env['E2E_MANAGEMENT_ROLE_CHANGE_READONLY'] === '1' &&
  localOperatorUrl;

// Login, tenant selection, GETs and logout only. Never creates or decides a role request.
test.use({ trace: 'off' });
test.skip(
  !enabled,
  'requires E2E_REAL_API=1, E2E_MANAGEMENT_ROLE_CHANGE_READONLY=1 and the operator UI at 127.0.0.1:5181',
);

const roleCodes = [
  'TENANT_ADMIN',
  'PLAN_PUBLISHER',
  'CONTRACT_PUBLISHER',
  'RULE_APPROVER',
  'PAYER_APPROVER',
];
const summaryKeys = [
  'id',
  'operation',
  'status',
  'targetMembershipId',
  'makerMembershipId',
  'roleCode',
  'scopeType',
  'reasonCode',
  'createdAt',
  'decidedAt',
  'rowVersion',
];
const requestKeys = [
  ...summaryKeys,
  'permissionSnapshot',
  'configurationHash',
  'targetMembershipVersion',
  'revokeGrantId',
  'revokeValidity',
  'decidedByMembershipId',
  'decisionReasonCode',
  'appliedGrantId',
  'appliedMembershipVersion',
  'appliedValidity',
];
const missingId = '00000000-0000-4000-8000-000000000001';

test('tenant manager reads privileged options, target eligibility and bounded pending requests', async ({
  browser,
}) => {
  const page = await browser.newPage();
  const actor = new Actor(page.request, 'backoffice');
  try {
    await actor.login('admin.a');
    const options = await actor.call<components['schemas']['PrivilegedRoleAssignmentOptions']>(
      'GET',
      '/api/v1/admin/privileged-role-assignment-options',
    );
    expect(options.cacheControl).toBe('no-store');
    expectKeys(options.data, ['items']);
    expect(options.data.items.length).toBeGreaterThan(0);
    expect(options.data.items.length).toBeLessThanOrEqual(roleCodes.length);
    expect(new Set(options.data.items.map((option) => option.code)).size).toBe(
      options.data.items.length,
    );
    for (const option of options.data.items) {
      expectKeys(option, [
        'code',
        'configurationHash',
        'description',
        'hasSensitivePermissions',
        'name',
        'permissionCodes',
        'requiresApproval',
        'scopeType',
      ]);
      expect(roleCodes).toContain(option.code);
      expect(option.scopeType).toBe('TENANT');
      expect(option.requiresApproval).toBe(true);
      expect(option.configurationHash).toMatch(/^[0-9a-f]{64}$/);
      expect(option.permissionCodes.length).toBeGreaterThan(0);
      expect(option.permissionCodes).toEqual([...new Set(option.permissionCodes)].sort());
    }

    const directory = await actor.call<components['schemas']['TenantUserPage']>(
      'GET',
      '/api/v1/admin/users?limit=1',
    );
    expect(directory.data.items.length).toBe(1);
    const membershipId = directory.data.items[0]!.id;
    const eligibility = await actor.call<components['schemas']['RoleChangeEligibility']>(
      'GET',
      `/api/v1/admin/users/${membershipId}/role-change-eligibility`,
    );
    expect(eligibility.cacheControl).toBe('no-store');
    expectKeys(eligibility.data, [
      'assignmentRefusalCode',
      'canRequestAssignment',
      'checkerAvailability',
      'membershipId',
      'membershipRowVersion',
      'revokeGrantIds',
    ]);
    expect(eligibility.data.membershipId).toBe(membershipId);
    expect(eligibility.etag).toBe(`"${eligibility.data.membershipRowVersion}"`);
    expect(['AVAILABLE', 'NO_ELIGIBLE_CHECKER']).toContain(eligibility.data.checkerAvailability);
    expect(eligibility.data.revokeGrantIds.length).toBeLessThanOrEqual(1);

    const requests = await actor.call<components['schemas']['RoleChangeRequestPage']>(
      'GET',
      '/api/v1/admin/role-change-requests?limit=2',
    );
    expect(requests.cacheControl).toBe('no-store');
    expectKeys(requests.data, ['items', 'nextCursor']);
    expect(requests.data.items.length).toBeLessThanOrEqual(2);
    for (const item of requests.data.items) {
      expectKeys(item, summaryKeys);
      expect(item.status).toBe('PENDING');
      expect(item.scopeType).toBe('TENANT');
      expect(roleCodes).toContain(item.roleCode);
    }

    // Existing history is optional. Do not manufacture a live request to populate the page.
    const first = requests.data.items[0];
    if (first) {
      const detail = await actor.call<components['schemas']['RoleChangeRequestDetail']>(
        'GET',
        `/api/v1/admin/role-change-requests/${first.id}`,
      );
      expect(detail.cacheControl).toBe('no-store');
      expectKeys(detail.data, [
        'approvalRefusalCode',
        'canApprove',
        'canCancel',
        'cancellationRefusalCode',
        'canReject',
        'checkerAvailability',
        'rejectionRefusalCode',
        'request',
      ]);
      expectKeys(detail.data.request, requestKeys);
      expect(detail.data.request.id).toBe(first.id);
      expect(detail.etag).toBe(`"${detail.data.request.rowVersion}"`);
      const snapshot = detail.data.request.permissionSnapshot;
      expect(snapshot.length).toBeGreaterThan(0);
      expect(snapshot.some((permission) => permission.sensitivity === 'PRIVILEGED')).toBe(true);
      for (const permission of snapshot) {
        expectKeys(permission, ['code', 'sensitivity']);
      }
    }

    const unavailable = await actor.call(
      'GET',
      `/api/v1/admin/role-change-requests/${missingId}`,
      undefined,
      {
        expected: 404,
      },
    );
    expect(unavailable.cacheControl).toBe('no-store');
  } finally {
    await actor.close();
    await page.close();
  }
});

test('financial reviewer cannot read privileged-role administration endpoints', async ({
  browser,
}) => {
  const page = await browser.newPage();
  const actor = new Actor(page.request, 'backoffice');
  try {
    await actor.login('financial.reviewer');
    for (const path of [
      '/api/v1/admin/privileged-role-assignment-options',
      `/api/v1/admin/users/${missingId}/role-change-eligibility`,
      '/api/v1/admin/role-change-requests?limit=2',
      `/api/v1/admin/role-change-requests/${missingId}`,
    ]) {
      const denied = await actor.call('GET', path, undefined, { expected: 403 });
      expect(denied.cacheControl).toBe('no-store');
    }
  } finally {
    await actor.close();
    await page.close();
  }
});

function expectKeys(value: object, keys: string[]) {
  expect(Object.keys(value).sort().join('|'), 'strict response property allowlist').toBe(
    [...keys].sort().join('|'),
  );
}
