import { expect, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const enabled =
  process.env['E2E_REAL_API'] === '1' &&
  process.env['E2E_MANAGEMENT_DIRECTORY_READONLY'] === '1' &&
  base.length > 0;

test.use({ trace: 'off' });
test.skip(
  !enabled,
  'requires E2E_REAL_API=1, E2E_MANAGEMENT_DIRECTORY_READONLY=1, and an operator-started local system',
);

const membershipKeys = [
  'actorStatus',
  'actorType',
  'displayName',
  'id',
  'membershipStatus',
  'rowVersion',
  'validFrom',
  'validTo',
  'validityEmpty',
].sort();
const assignedRoleKeys = [
  'code',
  'isSystemRole',
  'name',
  'scopeType',
  'validFrom',
  'validTo',
  'validityEmpty',
].sort();

test('tenant admin can read a bounded membership directory and versioned detail', async ({
  browser,
}) => {
  expectLocalOperatorUrl();
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const actor = new Actor(page.request, 'backoffice');
  try {
    await actor.login('admin.a');
    const me = await actor.call<components['schemas']['UserContext']>('GET', '/api/v1/me');
    const context = me.data.tenants.find((tenant) => tenant.tenant.code === 'DEMO_A');
    expect(context?.canReadTenantUsers === true, 'tenant admin read capability').toBe(true);
    expect(context?.canManageTenantUsers === true, 'tenant admin manage capability').toBe(true);

    const list = await actor.call<components['schemas']['TenantUserPage']>(
      'GET',
      '/api/v1/admin/users?limit=2',
    );
    expect(list.status).toBe(200);
    expect(list.data.items.length > 0 && list.data.items.length <= 2, 'page is bounded').toBe(true);
    for (const membership of list.data.items) {
      expect(Object.keys(membership).sort().join('|'), 'membership response allowlist').toBe(
        membershipKeys.join('|'),
      );
      expect(membership.rowVersion >= 1, 'membership has a usable row version').toBe(true);
      expect(/^[0-9a-f-]{36}$/i.test(membership.id), 'directory id is a membership UUID').toBe(
        true,
      );
    }

    const detail = await actor.call<components['schemas']['TenantUserDetail']>(
      'GET',
      `/api/v1/admin/users/${list.data.items[0]!.id}`,
    );
    expect(detail.status).toBe(200);
    expect(detail.etag.length > 0, 'detail includes the suspension concurrency ETag').toBe(true);
    expect(Object.keys(detail.data).sort().join('|'), 'detail response allowlist').toBe(
      'assignedRoles|membership',
    );
    expect(
      Object.keys(detail.data.membership).sort().join('|'),
      'detail membership allowlist',
    ).toBe(membershipKeys.join('|'));
    expect(detail.data.membership.rowVersion, 'detail and list versions agree').toBe(
      list.data.items[0]!.rowVersion,
    );
    for (const role of detail.data.assignedRoles) {
      expect(Object.keys(role).sort().join('|'), 'assigned role response allowlist').toBe(
        assignedRoleKeys.join('|'),
      );
    }
  } finally {
    await actor.close();
    await page.close();
  }
});

test('financial reviewer cannot read the tenant membership directory', async ({ browser }) => {
  expectLocalOperatorUrl();
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const actor = new Actor(page.request, 'backoffice');
  try {
    await actor.login('financial.reviewer');
    const me = await actor.call<components['schemas']['UserContext']>('GET', '/api/v1/me');
    const context = me.data.tenants.find((tenant) => tenant.tenant.code === 'DEMO_A');
    expect(
      context?.canReadTenantUsers === false,
      'financial role has no directory read capability',
    ).toBe(true);
    expect(
      context?.canManageTenantUsers === false,
      'financial role has no directory manage capability',
    ).toBe(true);

    const denied = await actor.call('GET', '/api/v1/admin/users?limit=2', undefined, {
      expected: 403,
    });
    expect(denied.status).toBe(403);
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
