import { expect, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const enabled =
  process.env['E2E_REAL_API'] === '1' &&
  process.env['E2E_MANAGEMENT_ROLE_GRANTS_READONLY'] === '1' &&
  base.length > 0;

test.use({ trace: 'off' });
test.skip(
  !enabled,
  'requires E2E_REAL_API=1, E2E_MANAGEMENT_ROLE_GRANTS_READONLY=1, and an operator-started local system',
);

const scopes = new Map([
  ['PROGRAM_MANAGER', 'TENANT'],
  ['CONTRACT_MANAGER', 'TENANT'],
  ['RULE_AUTHOR', 'TENANT'],
  ['MEDICAL_REVIEWER', 'TENANT'],
  ['FINANCIAL_REVIEWER', 'TENANT'],
  ['AUDITOR', 'TENANT'],
  ['SPONSOR_HR', 'TENANT'],
  ['PROVIDER_ADMIN', 'ORGANIZATION'],
  ['PROVIDER_STAFF', 'ORGANIZATION'],
  ['PROVIDER_BILLING', 'ORGANIZATION'],
  ['PROVIDER_RESERVATION', 'ORGANIZATION'],
]);
const grantKeys = [
  'id',
  'roleCode',
  'roleName',
  'isSystemRole',
  'scopeType',
  'organizationRelationshipId',
  'organizationDisplayName',
  'validFrom',
  'validTo',
  'validityEmpty',
  'canRevoke',
  'revocationRefusalCode',
].sort();

test('tenant role manager reads bounded candidates, provider relationships and grant history', async ({
  browser,
}) => {
  expectLocalOperatorUrl();
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const actor = new Actor(page.request, 'backoffice');
  try {
    await actor.login('admin.a');
    const me = await actor.call<components['schemas']['UserContext']>('GET', '/api/v1/me');
    expect(
      me.data.tenants.find((tenant) => tenant.tenant.code === 'DEMO_A')?.canManageTenantRoles,
      'current tenant role-management capability',
    ).toBe(true);

    const options = await actor.call<components['schemas']['RoleAssignmentOptions']>(
      'GET',
      '/api/v1/admin/role-assignment-options',
    );
    expect(options.cacheControl).toBe('no-store');
    expect(Object.keys(options.data).sort().join('|')).toBe('items');
    expect(options.data.items.length > 0 && options.data.items.length <= scopes.size).toBe(true);
    expect(new Set(options.data.items.map((option) => option.code)).size).toBe(
      options.data.items.length,
    );
    for (const option of options.data.items) {
      expect(Object.keys(option).sort().join('|'), 'candidate response allowlist').toBe(
        'code|description|hasSensitivePermissions|name|permissionCodes|scopeType',
      );
      expect(scopes.get(option.code), 'only supported candidate scopes').toBe(option.scopeType);
      expect(option.permissionCodes.length > 0, 'no empty permission candidates').toBe(true);
      expect(typeof option.hasSensitivePermissions).toBe('boolean');
    }

    const organizations = await actor.call<components['schemas']['RoleAssignmentOrganizationPage']>(
      'GET',
      '/api/v1/admin/role-assignment-organizations?limit=2',
    );
    expect(organizations.cacheControl).toBe('no-store');
    expect(Object.keys(organizations.data).sort().join('|')).toBe('items|nextCursor');
    expect(organizations.data.items.length <= 2, 'provider page is bounded').toBe(true);
    for (const organization of organizations.data.items) {
      expect(Object.keys(organization).sort().join('|'), 'relationship response allowlist').toBe(
        'displayName|id|tenantCode',
      );
      expect(/^[0-9a-f-]{36}$/i.test(organization.id)).toBe(true);
    }

    const directory = await actor.call<components['schemas']['TenantUserPage']>(
      'GET',
      '/api/v1/admin/users?limit=1',
    );
    expect(directory.data.items.length, 'an existing membership is available').toBe(1);
    const membershipId = directory.data.items[0]!.id;
    const history = await actor.call<components['schemas']['TenantRoleGrantPage']>(
      'GET',
      `/api/v1/admin/users/${membershipId}/role-grants?limit=2`,
    );
    expect(history.cacheControl).toBe('no-store');
    expect(Object.keys(history.data).sort().join('|'), 'grant-page response allowlist').toBe(
      'assignmentRefusalCode|canAssign|items|membershipId|membershipRowVersion|nextCursor',
    );
    expect(history.etag.length > 0, 'grant page has an aggregate concurrency ETag').toBe(true);
    expect(history.data.membershipId).toBe(membershipId);
    expect(history.data.membershipRowVersion >= 1).toBe(true);
    expect(history.data.items.length <= 2, 'grant history is bounded').toBe(true);
    for (const grant of history.data.items) {
      expect(Object.keys(grant).sort().join('|'), 'grant response allowlist').toBe(
        grantKeys.join('|'),
      );
      if (grant.scopeType !== 'ORGANIZATION' || scopes.get(grant.roleCode) !== 'ORGANIZATION') {
        expect(grant.organizationRelationshipId, 'unsupported scope identifiers are scrubbed').toBe(
          null,
        );
        expect(grant.organizationDisplayName).toBe(null);
      }
    }
  } finally {
    await actor.close();
    await page.close();
  }
});

test('financial reviewer has no tenant role-management capability or endpoint access', async ({
  browser,
}) => {
  expectLocalOperatorUrl();
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const actor = new Actor(page.request, 'backoffice');
  try {
    await actor.login('financial.reviewer');
    const me = await actor.call<components['schemas']['UserContext']>('GET', '/api/v1/me');
    expect(
      me.data.tenants.find((tenant) => tenant.tenant.code === 'DEMO_A')?.canManageTenantRoles,
      'financial reviewer has no tenant role-management capability',
    ).toBe(false);
    for (const path of [
      '/api/v1/admin/role-assignment-options',
      '/api/v1/admin/role-assignment-organizations?limit=2',
      '/api/v1/admin/users/00000000-0000-4000-8000-000000000001/role-grants?limit=2',
    ]) {
      const denied = await actor.call('GET', path, undefined, { expected: 403 });
      expect(denied.status).toBe(403);
      expect(denied.cacheControl).toBe('no-store');
    }
  } finally {
    await actor.close();
    await page.close();
  }
});

function expectLocalOperatorUrl() {
  expect(/^(localhost|127\.0\.0\.1)$/.test(new URL(base).hostname), 'local API gateway only').toBe(
    true,
  );
}
