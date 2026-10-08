import { expect, test } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';
import { Actor } from './real-api-actor';

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const enabled =
  process.env['E2E_REAL_API'] === '1' &&
  process.env['E2E_MANAGEMENT_INVITATIONS_READONLY'] === '1' &&
  base.length > 0;

// These tests are opt-in and use only authenticated reads after ordinary login/tenant selection.
test.use({ trace: 'off' });
test.skip(
  !enabled,
  'requires E2E_REAL_API=1, E2E_MANAGEMENT_INVITATIONS_READONLY=1, and an operator-started local system',
);

const invitationKeys = [
  'createdAt',
  'deliveryStatus',
  'expiresAt',
  'invitationId',
  'maskedRecipient',
  'rowVersion',
  'status',
].sort();

test('tenant admin can read a bounded invitation page and an existing invitation detail', async ({
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

    const list = await actor.call<components['schemas']['TenantInvitationPage']>(
      'GET',
      '/api/v1/admin/invitations?limit=2',
    );
    expect(list.status).toBe(200);
    expect(list.data.items.length <= 2, 'page is bounded').toBe(true);
    expect(
      list.data.nextCursor === null || typeof list.data.nextCursor === 'string',
      'cursor is nullable text',
    ).toBe(true);
    for (const invitation of list.data.items) {
      expect(Object.keys(invitation).sort().join('|'), 'invitation response allowlist').toBe(
        invitationKeys.join('|'),
      );
      expect(invitation.rowVersion >= 1, 'invitation has a usable row version').toBe(true);
      expect(/^[0-9a-f-]{36}$/i.test(invitation.invitationId), 'invitation id is a UUID').toBe(
        true,
      );
      expect(invitation.maskedRecipient.length > 0, 'recipient is masked').toBe(true);
    }

    const existing = list.data.items[0];
    if (existing) {
      const detail = await actor.call<components['schemas']['TenantInvitation']>(
        'GET',
        `/api/v1/admin/invitations/${existing.invitationId}`,
      );
      expect(detail.status).toBe(200);
      expect(detail.etag.length > 0, 'detail has a concurrency ETag').toBe(true);
      expect(Object.keys(detail.data).sort().join('|'), 'detail response allowlist').toBe(
        invitationKeys.join('|'),
      );
      expect(detail.data.invitationId, 'detail belongs to the selected list row').toBe(
        existing.invitationId,
      );
      expect(detail.data.rowVersion, 'detail and list versions agree').toBe(existing.rowVersion);
    }
  } finally {
    await actor.close();
    await page.close();
  }
});

test('financial reviewer has no invitation capability and cannot list invitations', async ({
  browser,
}) => {
  expectLocalOperatorUrl();
  const page = await browser.newPage({ locale: 'tr-TR', timezoneId: 'Europe/Istanbul' });
  const actor = new Actor(page.request, 'backoffice');
  try {
    await actor.login('financial.reviewer');
    const me = await actor.call<components['schemas']['UserContext']>('GET', '/api/v1/me');
    const context = me.data.tenants.find((tenant) => tenant.tenant.code === 'DEMO_A');
    expect(
      context?.canReadTenantUsers === false,
      'financial role has no tenant-user read capability',
    ).toBe(true);
    expect(
      context?.canManageTenantUsers === false,
      'financial role has no tenant-user manage capability',
    ).toBe(true);

    const denied = await actor.call('GET', '/api/v1/admin/invitations?limit=2', undefined, {
      expected: 403,
    });
    expect(denied.status).toBe(403);
  } finally {
    await actor.close();
    await page.close();
  }
});

function expectLocalOperatorUrl() {
  expect(/^(localhost|127\.0\.0\.1)$/.test(new URL(base).hostname), 'local operator UI only').toBe(
    true,
  );
}
