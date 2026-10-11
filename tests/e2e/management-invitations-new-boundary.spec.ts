import { expect, request, test } from '@playwright/test';

const base = process.env['E2E_EXISTING_UI_URL'] ?? '';
const localOperatorUrl = (() => {
  try {
    const ui = new URL(base);
    return ui.hostname === '127.0.0.1' && ui.port === '5181';
  } catch {
    return false;
  }
})();
const enabled =
  process.env['E2E_REAL_API'] === '1' &&
  process.env['E2E_MANAGEMENT_INVITATIONS_NEW_BOUNDARY'] === '1' &&
  localOperatorUrl;

// This spec sends anonymous boundary probes only; it never logs in or mutates invitation state.
test.use({ trace: 'off' });
test.skip(
  !enabled,
  'requires E2E_REAL_API=1, E2E_MANAGEMENT_INVITATIONS_NEW_BOUNDARY=1, and E2E_EXISTING_UI_URL at numeric loopback 127.0.0.1:5181',
);

test('new invitation anonymous boundary stays separate from existing-recipient routes', async () => {
  const ui = new URL(base);
  expect(ui.hostname, 'numeric loopback UI only').toBe('127.0.0.1');
  expect(ui.port, 'operator UI port').toBe('5181');

  const origin = ui.origin;
  const api = await request.newContext({ baseURL: origin });
  const code = `v1.00000000-0000-4000-8000-000000000001.00000000-0000-4000-8000-000000000002.${'A'.repeat(43)}`;
  try {
    const acceptedHeaders = {
      Origin: origin,
      'X-Invitation-Request': '1',
      'Content-Type': 'application/json',
    };
    const validOrigin = await api.post('/api/v1/invitations/inspect-new', {
      headers: { ...acceptedHeaders, Cookie: 'kapsora_session=invalid-session-cookie' },
      data: { code },
    });
    expect(validOrigin.status()).toBe(404);
    expect((await validOrigin.json()).code).toBe('INVITATION_UNAVAILABLE');
    expect(validOrigin.headers()['cache-control']).toBe('no-store');
    expect(validOrigin.headers()['set-cookie']).toBeUndefined();

    for (const headers of [
      { 'X-Invitation-Request': '1', 'Content-Type': 'application/json' },
      { ...acceptedHeaders, Origin: 'https://foreign.example' },
    ]) {
      const denied = await api.post('/api/v1/invitations/inspect-new', {
        headers,
        data: { code },
      });
      expect(denied.status()).toBe(403);
      expect((await denied.json()).code).toBe('INVITATION_REQUEST_FORBIDDEN');
      expect(denied.headers()['cache-control']).toBe('no-store');
    }

    for (const [path, data, headers] of [
      ['/api/v1/invitations/inspect', { code }, { 'Content-Type': 'application/json' }],
      [
        '/api/v1/invitations/accept-existing',
        { code, confirmed: true },
        {
          'Content-Type': 'application/json',
          'Idempotency-Key': 'b2-anonymous-boundary-probe-0001',
        },
      ],
    ] as const) {
      const denied = await api.post(path, { headers, data });
      expect([401, 403], `${path} remains behind the authentication gate`).toContain(
        denied.status(),
      );
      expect(denied.status()).not.toBe(404);
      expect(denied.status()).not.toBe(405);
      expect((await denied.json()).code).not.toBe('INVITATION_UNAVAILABLE');
    }
  } finally {
    await api.dispose();
  }
});
