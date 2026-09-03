import { describe, expect, it } from 'vitest';
import { createKapsoraClient } from './client';
import { createOperations } from './operations';
import { ApiError, NETWORK_ERROR } from './problem';

interface Captured {
  url: string;
  method: string;
  headers: Headers;
  body: string | null;
}

/** A fetch stub that records the request and answers with the given response. */
function stubFetch(respond: (req: Captured) => Response) {
  const calls: Captured[] = [];
  const fetchImpl: typeof fetch = async (input, init) => {
    const request = new Request(input, init);
    const captured: Captured = {
      url: request.url,
      method: request.method,
      headers: request.headers,
      body: request.method === 'GET' ? null : await request.text(),
    };
    calls.push(captured);
    return respond(captured);
  };
  return { calls, fetchImpl };
}

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function problem(status: number, code: string, extra: Record<string, unknown> = {}): Response {
  return new Response(
    JSON.stringify({
      type: 'https://errors.kapsora.example/x',
      title: 'Hata',
      status,
      code,
      traceId: 'trace-1',
      ...extra,
    }),
    { status, headers: { 'Content-Type': 'application/problem+json' } },
  );
}

const organization = {
  id: '01a06604-96e6-7731-850b-60b065448d36',
  organizationId: '01a06604-96e3-7509-abcd-f5f991724bae',
  legalName: 'Örnek A.Ş.',
  displayName: 'Örnek',
  organizationKind: 'PROVIDER',
  countryCode: 'TR',
  organizationStatus: 'ACTIVE',
  relationshipRole: 'PROVIDER',
  relationshipStatus: 'ACTIVE',
  identifiers: [{ type: 'VKN', maskedValue: '12******90', primary: true }],
  validFrom: '2026-09-03',
  rowVersion: 1,
} as const;

describe('header injection', () => {
  it('adds request id, tenant, CSRF and idempotency headers', async () => {
    const { calls, fetchImpl } = stubFetch(() => json(201, organization, { ETag: '"1"' }));
    const client = createKapsoraClient({
      baseUrl: 'http://api.test',
      fetch: fetchImpl,
      requestId: () => 'req-1',
      csrfToken: () => 'csrf-1',
    });
    const ops = createOperations(client);
    const result = await ops.organizations.create(
      'tenant-1',
      {
        legalName: 'Örnek A.Ş.',
        displayName: 'Örnek',
        organizationKind: 'PROVIDER',
        relationshipRole: 'PROVIDER',
        identifiers: [{ type: 'VKN', value: '1234567890', primary: true }],
      },
      'idem-1',
    );
    expect(result.etag).toBe('"1"');
    expect(result.data.id).toBe(organization.id);
    const req = calls[0]!;
    expect(req.method).toBe('POST');
    expect(req.url).toBe('http://api.test/api/v1/organizations');
    expect(req.headers.get('X-Request-ID')).toBe('req-1');
    expect(req.headers.get('X-Tenant-ID')).toBe('tenant-1');
    expect(req.headers.get('X-CSRF-Token')).toBe('csrf-1');
    expect(req.headers.get('Idempotency-Key')).toBe('idem-1');
    expect(req.headers.get('Content-Type')).toContain('application/json');
  });

  it('does not send CSRF on GET and sends merge-patch with If-Match on update', async () => {
    const { calls, fetchImpl } = stubFetch((req) =>
      req.method === 'GET'
        ? json(200, organization, { ETag: '"3"' })
        : json(200, organization, { ETag: '"4"' }),
    );
    const ops = createOperations(
      createKapsoraClient({
        baseUrl: 'http://api.test',
        fetch: fetchImpl,
        csrfToken: () => 'csrf-1',
      }),
    );
    const got = await ops.organizations.get('tenant-1', organization.id);
    expect(got.etag).toBe('"3"');
    expect(calls[0]!.headers.get('X-CSRF-Token')).toBeNull();

    await ops.organizations.update('tenant-1', organization.id, '"3"', { tenantCode: null });
    const patch = calls[1]!;
    expect(patch.method).toBe('PATCH');
    expect(patch.headers.get('Content-Type')).toBe('application/merge-patch+json');
    expect(patch.headers.get('If-Match')).toBe('"3"');
    expect(patch.body).toBe('{"tenantCode":null}');
  });

  it('serialises list filters as query parameters', async () => {
    const { calls, fetchImpl } = stubFetch(() => json(200, { items: [], nextCursor: null }));
    const ops = createOperations(
      createKapsoraClient({ baseUrl: 'http://api.test', fetch: fetchImpl }),
    );
    await ops.organizations.list('t', { role: 'PROVIDER', q: 'hasta', limit: 20, cursor: 'abc' });
    const url = new URL(calls[0]!.url);
    expect(url.searchParams.get('role')).toBe('PROVIDER');
    expect(url.searchParams.get('q')).toBe('hasta');
    expect(url.searchParams.get('limit')).toBe('20');
    expect(url.searchParams.get('cursor')).toBe('abc');
  });
});

describe('problem parsing', () => {
  it('throws ApiError with the problem document and field errors', async () => {
    const { fetchImpl } = stubFetch(() =>
      problem(422, 'VALIDATION_FAILED', {
        errors: [{ field: 'identifiers[0].value', code: 'IDENTIFIER_INVALID', message: 'x' }],
      }),
    );
    const ops = createOperations(
      createKapsoraClient({ baseUrl: 'http://api.test', fetch: fetchImpl }),
    );
    const err = await ops.session.login('a', 'b').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const api = err as ApiError;
    expect(api.status).toBe(422);
    expect(api.problem.code).toBe('VALIDATION_FAILED');
    expect(api.problem.traceId).toBe('trace-1');
    expect(api.fieldErrors().get('identifiers[0].value')?.code).toBe('IDENTIFIER_INVALID');
  });

  it('wraps non-problem bodies and network failures', async () => {
    const { fetchImpl } = stubFetch(
      () => new Response('<html>bad gateway</html>', { status: 502, statusText: 'Bad Gateway' }),
    );
    const ops = createOperations(
      createKapsoraClient({ baseUrl: 'http://api.test', fetch: fetchImpl }),
    );
    const err = (await ops.session.get().catch((e: unknown) => e)) as ApiError;
    expect(err.problem.code).toBe('HTTP_502');
    expect(err.problem.status).toBe(502);

    const failing: typeof fetch = async () => {
      throw new TypeError('Failed to fetch');
    };
    const offline = createOperations(
      createKapsoraClient({ baseUrl: 'http://api.test', fetch: failing }),
    );
    const netErr = (await offline.session.get().catch((e: unknown) => e)) as ApiError;
    expect(netErr.problem.code).toBe(NETWORK_ERROR);
    expect(netErr.problem.status).toBe(0);
  });
});
