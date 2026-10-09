import { expect, it } from 'vitest';
import { adminOperations } from './admin';
import { createKapsoraClient } from './client';

it('sends the anonymous marker without app, tenant, CSRF or a scripted Origin header', async () => {
  const seen: Request[] = [];
  const client = createKapsoraClient({
    baseUrl: 'https://example.test',
    app: 'backoffice',
    csrfToken: () => 'existing-session-csrf',
    fetch: async (request) => {
      seen.push(request as Request);
      return new Response(
        JSON.stringify({
          tenantDisplayName: 'Test institution',
          invitationStatus: 'PENDING',
          expiresAt: new Date(Date.now() + 60_000).toISOString(),
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      );
    },
  });
  await adminOperations(client).inspectNewInvitation('synthetic-proof');
  expect(seen).toHaveLength(1);
  const request = seen[0]!;
  expect(request.headers.get('X-Invitation-Request')).toBe('1');
  expect(request.headers.get('Content-Type')).toContain('application/json');
  expect(request.headers.has('Origin')).toBe(false);
  expect(request.headers.has('X-Kapsora-App')).toBe(false);
  expect(request.headers.has('X-CSRF-Token')).toBe(false);
  expect(request.headers.has('X-Tenant-ID')).toBe(false);
  expect(request.url).not.toContain('synthetic-proof');
});
