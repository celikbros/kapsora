import { mkdir, writeFile } from 'node:fs/promises';
import { resolve, dirname } from 'node:path';
import { execFileSync } from 'node:child_process';
import { validateFixture } from '../../tests/load/model.mjs';

const baseUrl = (process.env.KAPSORA_LOAD_BASE_URL ?? 'http://127.0.0.1:8090').replace(/\/$/, '');
const target = new URL(baseUrl);
if (
  target.username ||
  target.password ||
  target.pathname !== '/' ||
  target.search ||
  target.hash ||
  !['localhost', '127.0.0.1', '[::1]'].includes(target.hostname)
) {
  throw new Error('Demo discovery is restricted to a local operator-started API.');
}
const password = process.env.KAPSORA_LOAD_PASSWORD;
if (!password) throw new Error('Set KAPSORA_LOAD_PASSWORD to the local synthetic demo password.');
const sessions = [];

async function request(session, method, path, body) {
  const response = await fetch(baseUrl + path, {
    method,
    redirect: 'error',
    signal: AbortSignal.timeout(15000),
    headers: {
      'Content-Type': 'application/json',
      'X-Kapsora-App': session.app,
      ...(session.cookie ? { Cookie: session.cookie } : {}),
      ...(session.csrf ? { 'X-CSRF-Token': session.csrf } : {}),
      ...(session.tenantId ? { 'X-Tenant-ID': session.tenantId } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok)
    throw new Error(`Demo discovery: ${method} ${path.split('?')[0]} returned ${response.status}.`);
  const data = response.status === 204 ? null : await response.json();
  if (data?.csrfToken) session.csrf = data.csrfToken;
  const cookie = response.headers.getSetCookie().find((entry) => !entry.startsWith('__Host-csrf'));
  if (cookie) session.cookie = cookie.split(';')[0];
  return data;
}

async function login(username, app) {
  const session = { username, app };
  await request(session, 'POST', '/api/v1/session/login', { username, password });
  sessions.push(session);
  const tenants = await request(session, 'GET', '/api/v1/tenants');
  const tenant = (Array.isArray(tenants) ? tenants : tenants.items).find(
    (item) => item.code === 'DEMO_A',
  );
  if (!tenant) throw new Error('The local account must belong to the seeded DEMO_A tenant.');
  session.tenantId = tenant.id;
  await request(session, 'POST', '/api/v1/session/switch-tenant', { tenantId: tenant.id });
  return session;
}

try {
  const admin = await login('admin.a', 'backoffice');
  const member = await login('member.a', 'member');
  const provider = await login('provider.a', 'provider');
  const me = await request(member, 'GET', '/api/v1/me/person');
  const personId = me.person.id;
  const organizations = await request(admin, 'GET', '/api/v1/organizations?limit=100');
  const details = await Promise.all(
    organizations.items.map((item) => request(admin, 'GET', `/api/v1/organizations/${item.id}`)),
  );
  const sponsor = details.find((item) => item.tenantCode === 'DEMO_SPONSOR');
  const hospital = details.find((item) => item.tenantCode === 'DEMO_HOSPITAL');
  const services = await request(admin, 'GET', '/api/v1/service-definitions?limit=100');
  const service = services.items.find((item) => item.code === 'PHYSIO_SESSION');
  if (!sponsor || !hospital || !service)
    throw new Error('The complete demo business seed is required.');
  const today = new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Istanbul' }).format(
    new Date(),
  );
  const eligibility = {
    personId,
    serviceDate: today,
    providerOrganizationId: hospital.id,
    serviceItems: [{ serviceDefinitionId: service.id, quantity: 1 }],
  };
  const eligible = await request(provider, 'POST', '/api/v1/eligibility/checks', eligibility);
  if (!eligible.eligible || !eligible.enrollmentId)
    throw new Error('Demo physiotherapy must be eligible with an unambiguous enrollment.');
  const checkIn = new Date(`${today}T12:00:00Z`);
  checkIn.setUTCDate(checkIn.getUTCDate() + 14);
  const checkOut = new Date(checkIn);
  checkOut.setUTCDate(checkOut.getUTCDate() + 1);
  const properties = await request(member, 'GET', '/api/v1/accommodation/properties?limit=100');
  const property = properties.items.find((item) => item.code === 'DEMO_OTEL');
  if (!property) throw new Error('The demo hotel is required.');
  const search = {
    personId,
    propertyId: property.id,
    adults: 1,
    children: 0,
    checkIn: checkIn.toISOString().slice(0, 10),
    checkOut: checkOut.toISOString().slice(0, 10),
  };
  const available = await request(
    member,
    'POST',
    '/api/v1/accommodation/availability/search',
    search,
  );
  const room = available.results.find((item) => item.available > 0 && item.quote);
  if (!room) throw new Error('The demo hotel needs priced inventory for the chosen dates.');
  const identity = (s) => ({ username: s.username, app: s.app, tenantId: s.tenantId });
  const fixture = {
    version: 1,
    synthetic: true,
    baseUrl,
    environment: {
      name: 'local-demo',
      kind: 'local',
      revision: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(),
    },
    dataset: { source: 'cmd/seed demo', capacityVerified: false },
    cases: {
      read: [{ ...identity(admin), path: '/api/v1/organizations?limit=25' }],
      write: [
        {
          ...identity(provider),
          body: {
            requestType: 'DIRECT_SERVICE',
            personId,
            enrollmentId: eligible.enrollmentId,
            providerOrganizationId: hospital.id,
            serviceDate: today,
            channel: 'PROVIDER_PORTAL',
            items: [
              { serviceDefinitionId: service.id, requestedQuantity: '1', unitType: 'SESSION' },
            ],
          },
        },
      ],
      eligibility: [{ ...identity(provider), body: eligibility }],
      hold: [
        {
          ...identity(member),
          search,
          body: {
            personId,
            roomTypeId: room.roomType.id,
            checkIn: search.checkIn,
            checkOut: search.checkOut,
            adults: 1,
            channel: 'MEMBER_PORTAL',
          },
        },
      ],
      import: [
        {
          ...identity(admin),
          rows: 5,
          planCode: 'DEMO_STANDARD',
          validFrom: today,
          body: { sponsorOrganizationId: sponsor.id, sourceSystem: 'K6_LOAD' },
        },
      ],
    },
  };
  validateFixture(fixture, 'smoke');
  const output = resolve(process.env.KAPSORA_LOAD_FIXTURE ?? 'test-results/load/fixture.json');
  await mkdir(dirname(output), { recursive: true });
  await writeFile(output, JSON.stringify(fixture, null, 2) + '\n', { mode: 0o600 });
  console.log(
    'Prepared five synthetic demo workloads; no passwords, sessions or personal details written.',
  );
} finally {
  for (const session of sessions) {
    try {
      await request(session, 'POST', '/api/v1/session/logout');
    } catch {
      console.error('A discovery session could not be logged out; it will expire normally.');
    }
  }
}
