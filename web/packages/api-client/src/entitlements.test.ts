import { describe, expect, it } from 'vitest';
import { createKapsoraClient } from './client';
import { entitlementOperations } from './entitlements';

function opsFor(respond: (url: URL) => Response) {
  const client = createKapsoraClient({
    baseUrl: 'http://api.test',
    fetch: async (input, init) => respond(new URL(new Request(input, init).url)),
  });
  return entitlementOperations(client);
}

function json(raw: string, status = 200, headers: Record<string, string> = {}) {
  return new Response(raw, { status, headers: { 'Content-Type': 'application/json', ...headers } });
}

describe('entitlement response decimals', () => {
  it('preserves account and ledger quantities while retaining version, cursor and ETag', async () => {
    const account =
      '{"id":"a","available":9007199254740993.000001,"reserved":0.000001,"consumed":0,"expired":0,"totalGranted":9007199254740993.000002,"rowVersion":8,"openReservations":[{"quantity":3.000001,"consumedQuantity":0,"releasedQuantity":0}]}';
    const ops = opsFor((url) => {
      if (url.pathname.endsWith('/ledger')) {
        return json(
          '{"items":[{"id":"e","deltaTotal":-9007199254740993.000001,"deltaAvailable":-1.000001}],"nextCursor":"next"}',
        );
      }
      if (url.pathname.includes('/people/')) return json(`{"items":[${account}]}`);
      return json(account, 200, { ETag: '"8"' });
    });

    const [listed] = await ops.listPersonEntitlements('tenant', 'person');
    expect(listed?.available).toBe('9007199254740993.000001');
    expect(listed?.openReservations?.[0]?.quantity).toBe('3.000001');
    expect(listed?.rowVersion).toBe(8);
    const single = await ops.getAccount('tenant', 'account');
    expect(single.etag).toBe('"8"');
    expect(single.data.totalGranted).toBe('9007199254740993.000002');
    const ledger = await ops.listLedger('tenant', 'account');
    expect(ledger.items[0]?.deltaTotal).toBe('-9007199254740993.000001');
    expect(ledger.nextCursor).toBe('next');
  });

  it('keeps structured API problems when a text response fails', async () => {
    const ops = opsFor(() =>
      json(
        '{"type":"about:blank","title":"Denied","status":403,"code":"PERMISSION_DENIED","traceId":"trace"}',
        403,
      ),
    );
    await expect(ops.listPersonEntitlements('tenant', 'person')).rejects.toMatchObject({
      problem: { code: 'PERMISSION_DENIED', status: 403 },
    });
  });
});
