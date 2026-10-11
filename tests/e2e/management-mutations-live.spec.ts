import { randomBytes, randomUUID } from 'node:crypto';
import { mkdir, readFile, rename, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { expect, request, test, type APIRequestContext } from '@playwright/test';
import type { components } from '../../web/packages/api-client/src/generated/kapsora-v1';

type Invitation = components['schemas']['TenantInvitation'];
type Member = components['schemas']['TenantUserDetail'];
type GrantPage = components['schemas']['TenantRoleGrantPage'];
type GrantResult = components['schemas']['TenantRoleGrantResult'];
type NewResult = components['schemas']['AcceptNewInvitationResponse'];
type ExistingResult = components['schemas']['AcceptExistingInvitationResponse'];
type RoleChangeResult = components['schemas']['RoleChangeCommandResult'];
type RoleChangeDetail = components['schemas']['RoleChangeRequestDetail'];
type Command = {
  method: 'POST';
  path: string;
  body: Record<string, unknown>;
  key: string;
  etag?: string;
  actor: 'adminA' | 'adminB' | 'synthetic' | 'anonymous';
};
type Journal = {
  version: 1;
  origin: string;
  recipient: string;
  displayName: string;
  password: string;
  baselineMailIds: string[];
  codes: { a?: string; b?: string };
  commands: Record<
    string,
    { command: Command; status: 'prepared' | 'committed'; result?: unknown }
  >;
  invitationA?: string;
  invitationB?: string;
  membershipA?: string;
  membershipB?: string;
  loginHandle?: string;
  grantId?: string;
  grantA2Id?: string;
  grantBId?: string;
  privilegedRequestId?: string;
  privilegedCreateReplayObserved?: boolean;
  privilegedCancelReplayObserved?: boolean;
  privilegedCheckerAvailability?: 'AVAILABLE' | 'NO_ELIGIBLE_CHECKER';
};

const origin = process.env['E2E_EXISTING_UI_URL'] ?? '';
const mailpit = process.env['E2E_MANAGEMENT_MAILPIT_URL'] ?? 'http://127.0.0.1:8025';
const enabled =
  process.env['E2E_REAL_API'] === '1' && process.env['E2E_MANAGEMENT_MUTATIONS_LIVE'] === '1';
const journalPath = resolve('tmp/management-live-acceptance-fixture.json');
const seededPassword = process.env['KAPSORA_SEED_DEMO_PASSWORD'] ?? 'demo parola 2026 kapsora';

test.use({ trace: 'off', screenshot: 'off', video: 'off' });
test.skip(!enabled, 'requires explicit E2E_MANAGEMENT_MUTATIONS_LIVE=1 and E2E_REAL_API=1');
test.setTimeout(300_000);

function loopback(url: string, port: string) {
  try {
    const parsed = new URL(url);
    return (
      parsed.protocol === 'http:' &&
      parsed.hostname === '127.0.0.1' &&
      parsed.port === port &&
      parsed.pathname === '/' &&
      !parsed.search &&
      !parsed.hash
    );
  } catch {
    return false;
  }
}

async function save(journal: Journal) {
  await mkdir(resolve('tmp'), { recursive: true });
  const next = `${journalPath}.${randomUUID()}.tmp`;
  await writeFile(next, JSON.stringify(journal, null, 2), { flag: 'wx', mode: 0o600 });
  await rename(next, journalPath);
}

async function load(): Promise<Journal> {
  try {
    const journal = JSON.parse(await readFile(journalPath, 'utf8')) as Journal;
    expect(journal.version).toBe(1);
    expect(journal.origin).toBe(origin);
    expect(journal.recipient.endsWith('@example.invalid')).toBe(true);
    return journal;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT')
      // The parse error may include private journal text; discard its cause.
      // eslint-disable-next-line preserve-caught-error
      throw new Error('Existing acceptance journal is invalid; stop and inspect it locally');
    const journal: Journal = {
      version: 1,
      origin,
      recipient: `kapsora-acceptance-${randomUUID()}@example.invalid`,
      displayName: `Synthetic Management ${randomUUID().slice(0, 8)}`,
      password: `Mgt!${randomBytes(25).toString('base64url')}7a`,
      baselineMailIds: [],
      codes: {},
      commands: {},
    };
    await save(journal);
    return journal;
  }
}

async function loadExisting(): Promise<Journal> {
  let journal: Journal;
  try {
    journal = JSON.parse(await readFile(journalPath, 'utf8')) as Journal;
  } catch {
    throw new Error(
      'Existing synthetic acceptance journal is required; no replacement fixture is allowed',
    );
  }
  expect(journal.version === 1 && journal.origin === origin, 'matching synthetic journal').toBe(
    true,
  );
  expect(journal.recipient.endsWith('@example.invalid'), 'synthetic recipient').toBe(true);
  expect(
    journal.commands.acceptNewA?.status === 'committed',
    'new-account acceptance completed',
  ).toBe(true);
  expect(journal.commands.acceptExistingB?.status === 'committed', 'B acceptance completed').toBe(
    true,
  );
  expect(journal.commands.revokeB?.status === 'committed', 'ordinary B grant revoked').toBe(true);
  const newAccount = journal.commands.acceptNewA?.result as NewResult | undefined;
  const joinedB = journal.commands.acceptExistingB?.result as ExistingResult | undefined;
  expect(newAccount?.loginHandle === journal.loginHandle, 'same generated account').toBe(true);
  expect(joinedB?.membershipId === journal.membershipB, 'same synthetic B membership').toBe(true);
  return journal;
}

/** No request/response body, cookie, code or password enters a Playwright assertion or error. */
class Client {
  private cookie = '';
  private csrf = '';
  private tenant = '';
  constructor(
    private api: APIRequestContext,
    private password: string,
  ) {}
  async call<T>(
    method: 'GET' | 'POST',
    path: string,
    body?: unknown,
    command?: Command,
    allowedStatuses?: number[],
  ): Promise<{ data: T; etag: string; status: number; replayed: boolean }> {
    const frozenOptions = {
      method,
      maxRedirects: 0,
      timeout: 30_000,
      headers: {
        'X-Kapsora-App': 'backoffice',
        ...(this.cookie ? { Cookie: this.cookie } : {}),
        ...(this.csrf ? { 'X-CSRF-Token': this.csrf } : {}),
        ...(this.tenant ? { 'X-Tenant-ID': this.tenant } : {}),
        ...(command ? { 'Idempotency-Key': command.key } : {}),
        ...(command?.etag ? { 'If-Match': command.etag } : {}),
      },
      ...(body === undefined ? {} : { data: body }),
    };
    const fetch = async () => {
      try {
        return await this.api.fetch(origin + path, frozenOptions);
      } catch {
        throw new Error(
          `Transport uncertainty at ${method} ${path.split('?')[0]}; retain journal and replay the exact prepared command`,
        );
      }
    };
    let response = await fetch();
    for (
      let retry = 0;
      response.status() === 429 && !allowedStatuses?.includes(429) && retry < 3;
      retry++
    ) {
      const seconds = Number(response.headers()['retry-after']);
      if (!Number.isInteger(seconds) || seconds < 1 || seconds > 30) break;
      await delay(seconds * 1000);
      response = await fetch();
    }
    const cookie = response
      .headersArray()
      .find((h) => h.name.toLowerCase() === 'set-cookie' && !h.value.startsWith('__Host-csrf'));
    if (cookie) this.cookie = cookie.value.split(';')[0]!;
    const status = response.status();
    if (allowedStatuses ? !allowedStatuses.includes(status) : status < 200 || status >= 300)
      throw new Error(
        `${method} ${path.split('?')[0]} returned ${status}; retain journal, no replacement command`,
      );
    let data: T;
    try {
      data = (status === 204 ? null : await response.json()) as T;
    } catch {
      throw new Error(`Response uncertainty at ${method} ${path.split('?')[0]}; retain journal`);
    }
    return {
      data,
      etag: response.headers()['etag'] ?? '',
      status,
      replayed: response.headers()['idempotent-replayed'] === 'true',
    };
  }
  async login(username: string, tenantCode: string) {
    const login = await this.call<{ csrfToken: string }>('POST', '/api/v1/session/login', {
      username,
      password: this.password,
    });
    this.csrf = login.data.csrfToken;
    const tenants = await this.call<{ items: { id: string; code: string }[] }>(
      'GET',
      '/api/v1/tenants',
    );
    const tenant = tenants.data.items.find((t) => t.code === tenantCode);
    expect(tenant, `${tenantCode} must be visible`).toBeDefined();
    this.tenant = tenant!.id;
    const switched = await this.call<{ csrfToken: string }>(
      'POST',
      '/api/v1/session/switch-tenant',
      { tenantId: this.tenant },
    );
    if (switched.data.csrfToken) this.csrf = switched.data.csrfToken;
  }
  async stepUp() {
    await this.call('POST', '/api/v1/session/step-up', { password: this.password });
  }
  async deniedRead(path: string) {
    try {
      const response = await this.api.get(origin + path, {
        headers: {
          'X-Kapsora-App': 'backoffice',
          Cookie: this.cookie,
          'X-Tenant-ID': this.tenant,
        },
        timeout: 30_000,
      });
      expect(response.status(), `${path} must deny access`).toBe(403);
    } catch {
      // Playwright transport errors may contain the Cookie header; discard them.
      throw new Error(`Protected read failed or did not deny access at ${path}`);
    }
  }
  async close() {
    await this.api.dispose();
  }
}

async function anonymous<T>(
  api: APIRequestContext,
  path: string,
  body: unknown,
  key?: string,
): Promise<T> {
  let response;
  try {
    response = await api.post(origin + path, {
      timeout: 30_000,
      maxRedirects: 0,
      headers: {
        Origin: origin,
        'X-Invitation-Request': '1',
        'Content-Type': 'application/json',
        ...(key ? { 'Idempotency-Key': key } : {}),
      },
      data: body,
    });
  } catch {
    throw new Error(`Anonymous transport uncertainty at ${path}; retain journal`);
  }
  if (response.status() !== 200)
    throw new Error(`Anonymous ${path} returned ${response.status()}; retain journal`);
  expect(response.headers()['cache-control']).toBe('no-store');
  expect(response.headers()['set-cookie'] === undefined, 'anonymous response has no cookie').toBe(
    true,
  );
  try {
    return (await response.json()) as T;
  } catch {
    throw new Error(`Anonymous response uncertainty at ${path}; retain journal`);
  }
}

async function command<T>(
  journal: Journal,
  name: string,
  proposed: () => Command,
  clients: Record<Command['actor'], Client | APIRequestContext>,
): Promise<T> {
  let record = journal.commands[name];
  if (record?.status === 'committed') {
    if (record.result === undefined)
      throw new Error(`Committed ${name} has no saved response; stop`);
    return record.result as T;
  }
  if (!record) {
    record = { command: proposed(), status: 'prepared' };
    journal.commands[name] = record;
    await save(journal); // Durable body/key/ETag before the first network dispatch.
  }
  const frozen = record.command;
  let result: T;
  if (frozen.actor === 'anonymous')
    result = await anonymous<T>(
      clients.anonymous as APIRequestContext,
      frozen.path,
      frozen.body,
      frozen.key,
    );
  else
    result = (
      await (clients[frozen.actor] as Client).call<T>(
        frozen.method,
        frozen.path,
        frozen.body,
        frozen,
      )
    ).data;
  record.status = 'committed';
  record.result = result;
  await save(journal);
  return result;
}

async function refusedCommand(
  journal: Journal,
  name: string,
  proposed: () => Command,
  actor: Client,
  expectedCode: string,
) {
  let record = journal.commands[name];
  if (record?.status === 'committed') {
    expect(record.result).toEqual({ status: 403, code: expectedCode });
    return;
  }
  if (!record) {
    record = { command: proposed(), status: 'prepared' };
    journal.commands[name] = record;
    await save(journal);
  }
  const frozen = record.command;
  const denied = await actor.call<{ code: string }>(
    frozen.method,
    frozen.path,
    frozen.body,
    frozen,
    [403],
  );
  expect(denied.data.code).toBe(expectedCode);
  record.status = 'committed';
  record.result = { status: denied.status, code: denied.data.code };
  await save(journal);
}

type MailMessage = { ID: string; Text?: string; To?: { Address?: string }[] };
async function mailList(api: APIRequestContext): Promise<MailMessage[]> {
  try {
    const response = await api.get(`${mailpit}/api/v1/messages?limit=100`);
    if (response.status() !== 200) throw new Error();
    const body = (await response.json()) as { messages?: MailMessage[] };
    return body.messages ?? [];
  } catch {
    throw new Error('Loopback Mailpit is unavailable; no invitation command should be dispatched');
  }
}
async function invitationCode(
  api: APIRequestContext,
  journal: Journal,
  expectedInvitationId: string,
): Promise<string> {
  for (let attempt = 0; attempt < 30; attempt++) {
    for (const item of await mailList(api)) {
      if (journal.baselineMailIds.includes(item.ID)) continue;
      let full: MailMessage;
      try {
        full = (await (
          await api.get(`${mailpit}/api/v1/message/${encodeURIComponent(item.ID)}`)
        ).json()) as MailMessage;
      } catch {
        throw new Error('Mailpit message read failed; retain journal');
      }
      if (!full.To?.some((to) => to.Address?.toLowerCase() === journal.recipient)) continue;
      const code = full.Text?.match(/v1\.[0-9a-f-]{36}\.[0-9a-f-]{36}\.[A-Za-z0-9_-]{43}/)?.[0];
      if (!code) throw new Error('Synthetic invitation delivery has no valid code; retain journal');
      if (code.split('.')[2] !== expectedInvitationId) continue;
      return code;
    }
    await delay(1000);
  }
  throw new Error('Synthetic invitation was not delivered to loopback Mailpit; retain journal');
}

test('synthetic two-tenant invitation, grant/revoke and A-only suspension', async () => {
  expect(loopback(origin, '5181'), 'operator-started numeric-loopback UI at 5181').toBe(true);
  expect(loopback(mailpit, '8025'), 'numeric-loopback Mailpit at 8025').toBe(true);
  const journal = await load();
  const mail = await request.newContext();
  const anonymousApi = await request.newContext();
  const a = new Client(await request.newContext(), seededPassword);
  const b = new Client(await request.newContext(), seededPassword);
  const synthetic = new Client(await request.newContext(), journal.password);
  const syntheticB = new Client(await request.newContext(), journal.password);
  const clients = { adminA: a, adminB: b, synthetic, anonymous: anonymousApi };
  try {
    await mailList(mail); // Fail closed before any command when the sink is unavailable.
    if (!journal.baselineMailIds.length && !journal.commands.inviteA) {
      journal.baselineMailIds = (await mailList(mail)).map((item) => item.ID);
      await save(journal);
    }
    await a.login('admin.a', 'DEMO_A');
    await b.login('admin.b', 'DEMO_B');
    await a.stepUp();
    await b.stepUp();
    if (journal.commands.suspendA) {
      if (journal.commands.suspendA.status === 'prepared') {
        await command<Member>(
          journal,
          'suspendA',
          () => {
            throw new Error('Prepared suspension must reuse its saved request');
          },
          clients,
        );
      }
      const suspended = await a.call<Member>('GET', `/api/v1/admin/users/${journal.membershipA}`);
      const intactB = await b.call<Member>('GET', `/api/v1/admin/users/${journal.membershipB}`);
      expect(suspended.data.membership.membershipStatus).toBe('SUSPENDED');
      expect(intactB.data.membership.membershipStatus).toBe('ACTIVE');
      const retainedA = await a.call<GrantPage>(
        'GET',
        `/api/v1/admin/users/${journal.membershipA}/role-grants?limit=2`,
      );
      expect(retainedA.data.items.some((item) => item.id === journal.grantA2Id)).toBe(true);
      await syntheticB.login(journal.loginHandle!, 'DEMO_B');
      if (journal.commands.revokeB?.status !== 'committed') {
        if (!journal.commands.revokeB) await syntheticB.call('GET', '/api/v1/rule-sets?limit=1');
        const before = await b.call<GrantPage>(
          'GET',
          `/api/v1/admin/users/${journal.membershipB}/role-grants?limit=2`,
        );
        if (!journal.commands.revokeB)
          expect(before.data.items.find((item) => item.id === journal.grantBId)?.canRevoke).toBe(
            true,
          );
        await command<GrantResult>(
          journal,
          'revokeB',
          () => ({
            method: 'POST',
            path: `/api/v1/admin/users/${journal.membershipB}/role-grants/${journal.grantBId}/revoke`,
            body: { reasonCode: 'ACCESS_REVIEW' },
            key: randomUUID(),
            etag: before.etag,
            actor: 'adminB',
          }),
          clients,
        );
      }
      await syntheticB.deniedRead('/api/v1/rule-sets?limit=1');
      return;
    }
    if (!journal.invitationA) {
      const invited = await command<Invitation>(
        journal,
        'inviteA',
        () => ({
          method: 'POST',
          path: '/api/v1/admin/invitations',
          body: { email: journal.recipient },
          key: randomUUID(),
          actor: 'adminA',
        }),
        clients,
      );
      journal.invitationA = invited.invitationId;
      await save(journal);
    }
    if (!journal.codes.a) {
      journal.codes.a = await invitationCode(mail, journal, journal.invitationA!);
      await save(journal);
    }
    if (!journal.membershipA) {
      if (!journal.commands.acceptNewA) {
        const preview = await anonymous<components['schemas']['InspectInvitationResponse']>(
          anonymousApi,
          '/api/v1/invitations/inspect-new',
          { code: journal.codes.a },
        );
        expect(preview.invitationStatus).toBe('PENDING');
      }
      const accepted = await command<NewResult>(
        journal,
        'acceptNewA',
        () => ({
          method: 'POST',
          path: '/api/v1/invitations/accept-new',
          body: {
            code: journal.codes.a,
            displayName: journal.displayName,
            password: journal.password,
            confirmed: true,
          },
          key: randomUUID(),
          actor: 'anonymous',
        }),
        clients,
      );
      expect(accepted.membershipStatus).toBe('ACTIVE');
      expect(accepted.accessPending).toBe(true);
      journal.membershipA = accepted.membershipId;
      journal.loginHandle = accepted.loginHandle;
      await save(journal);
    }
    expect(/^k_[0-9a-f]{32}$/.test(journal.loginHandle ?? ''), 'private handle format').toBe(true);
    await synthetic.login(journal.loginHandle!, 'DEMO_A');
    const zeroA = await a.call<Member>('GET', `/api/v1/admin/users/${journal.membershipA}`);
    expect(zeroA.data.membership.membershipStatus).toBe('ACTIVE');
    if (!journal.commands.assignA) expect(zeroA.data.assignedRoles).toHaveLength(0);
    if (!journal.commands.assignA) await synthetic.deniedRead('/api/v1/rule-sets?limit=1');
    if (!journal.invitationB) {
      const invited = await command<Invitation>(
        journal,
        'inviteB',
        () => ({
          method: 'POST',
          path: '/api/v1/admin/invitations',
          body: { email: journal.recipient },
          key: randomUUID(),
          actor: 'adminB',
        }),
        clients,
      );
      journal.invitationB = invited.invitationId;
      await save(journal);
    }
    if (!journal.codes.b) {
      journal.codes.b = await invitationCode(mail, journal, journal.invitationB!);
      await save(journal);
    }
    if (!journal.membershipB) {
      if (!journal.commands.acceptExistingB) {
        const inspected = await synthetic.call<components['schemas']['InspectInvitationResponse']>(
          'POST',
          '/api/v1/invitations/inspect',
          { code: journal.codes.b },
        );
        expect(inspected.data.invitationStatus).toBe('PENDING');
      }
      const accepted = await command<ExistingResult>(
        journal,
        'acceptExistingB',
        () => ({
          method: 'POST',
          path: '/api/v1/invitations/accept-existing',
          body: { code: journal.codes.b, confirmed: true },
          key: randomUUID(),
          actor: 'synthetic',
        }),
        clients,
      );
      expect(accepted.membershipStatus).toBe('ACTIVE');
      expect(accepted.accessPending).toBe(true);
      journal.membershipB = accepted.membershipId;
      await save(journal);
    }
    const zeroB = await b.call<Member>('GET', `/api/v1/admin/users/${journal.membershipB}`);
    expect(zeroB.data.membership.membershipStatus).toBe('ACTIVE');
    if (!journal.commands.assignB) expect(zeroB.data.assignedRoles).toHaveLength(0);
    await syntheticB.login(journal.loginHandle!, 'DEMO_B');
    if (!journal.commands.assignB) await syntheticB.deniedRead('/api/v1/rule-sets?limit=1');
    const options = await a.call<components['schemas']['RoleAssignmentOptions']>(
      'GET',
      '/api/v1/admin/role-assignment-options',
    );
    const role = options.data.items.find(
      (item) => item.code === 'RULE_AUTHOR' && item.scopeType === 'TENANT',
    );
    expect(role, 'supported public nonprivileged TENANT role').toBeDefined();
    expect(role!.hasSensitivePermissions).toBe(false);
    expect([...role!.permissionCodes].sort()).toEqual(
      ['rule.read', 'rule.draft', 'catalog.read', 'program.read'].sort(),
    );
    const optionsB = await b.call<components['schemas']['RoleAssignmentOptions']>(
      'GET',
      '/api/v1/admin/role-assignment-options',
    );
    const roleB = optionsB.data.items.find(
      (item) => item.code === 'RULE_AUTHOR' && item.scopeType === 'TENANT',
    );
    expect(roleB, 'DEMO_B public nonprivileged TENANT role').toBeDefined();
    expect(roleB!.hasSensitivePermissions).toBe(false);
    expect([...roleB!.permissionCodes].sort()).toEqual([...role!.permissionCodes].sort());
    if (!journal.grantId) {
      const before = await a.call<GrantPage>(
        'GET',
        `/api/v1/admin/users/${journal.membershipA}/role-grants?limit=2`,
      );
      if (!journal.commands.assignA) {
        expect(before.data.canAssign).toBe(true);
        expect(before.data.items).toHaveLength(0);
        expect(before.etag).not.toBe('');
      }
      const assigned = await command<GrantResult>(
        journal,
        'assignA',
        () => ({
          method: 'POST',
          path: `/api/v1/admin/users/${journal.membershipA}/role-grants`,
          body: { roleCode: role!.code, scopeType: 'TENANT', reasonCode: 'ONBOARDING' },
          key: randomUUID(),
          etag: before.etag,
          actor: 'adminA',
        }),
        clients,
      );
      expect(assigned.grant.roleCode).toBe(role!.code);
      journal.grantId = assigned.grant.id;
      await save(journal);
    }
    if (!journal.grantBId) {
      const before = await b.call<GrantPage>(
        'GET',
        `/api/v1/admin/users/${journal.membershipB}/role-grants?limit=2`,
      );
      if (!journal.commands.assignB) {
        expect(before.data.canAssign).toBe(true);
        expect(before.data.items).toHaveLength(0);
      }
      const assigned = await command<GrantResult>(
        journal,
        'assignB',
        () => ({
          method: 'POST',
          path: `/api/v1/admin/users/${journal.membershipB}/role-grants`,
          body: { roleCode: role!.code, scopeType: 'TENANT', reasonCode: 'ONBOARDING' },
          key: randomUUID(),
          etag: before.etag,
          actor: 'adminB',
        }),
        clients,
      );
      journal.grantBId = assigned.grant.id;
      await save(journal);
    }
    const roleRead = '/api/v1/rule-sets?limit=1';
    if (!journal.commands.revokeA) await synthetic.call('GET', roleRead);
    await syntheticB.call('GET', roleRead);
    if (!journal.commands.revokeA || journal.commands.revokeA.status !== 'committed') {
      const before = await a.call<GrantPage>(
        'GET',
        `/api/v1/admin/users/${journal.membershipA}/role-grants?limit=2`,
      );
      const grant = before.data.items.find((item) => item.id === journal.grantId);
      if (!journal.commands.revokeA) expect(grant?.canRevoke).toBe(true);
      await command<GrantResult>(
        journal,
        'revokeA',
        () => ({
          method: 'POST',
          path: `/api/v1/admin/users/${journal.membershipA}/role-grants/${journal.grantId}/revoke`,
          body: { reasonCode: 'ACCESS_REVIEW' },
          key: randomUUID(),
          etag: before.etag,
          actor: 'adminA',
        }),
        clients,
      );
    }
    const revoked = await a.call<GrantPage>(
      'GET',
      `/api/v1/admin/users/${journal.membershipA}/role-grants?limit=2`,
    );
    expect(revoked.data.items.find((item) => item.id === journal.grantId)?.canRevoke).toBe(false);
    if (!journal.commands.reassignA) await synthetic.deniedRead(roleRead);
    await syntheticB.call('GET', roleRead);
    if (!journal.grantA2Id) {
      const before = await a.call<GrantPage>(
        'GET',
        `/api/v1/admin/users/${journal.membershipA}/role-grants?limit=2`,
      );
      if (!journal.commands.reassignA) expect(before.data.canAssign).toBe(true);
      const assigned = await command<GrantResult>(
        journal,
        'reassignA',
        () => ({
          method: 'POST',
          path: `/api/v1/admin/users/${journal.membershipA}/role-grants`,
          body: { roleCode: role!.code, scopeType: 'TENANT', reasonCode: 'DUTY_ASSIGNMENT' },
          key: randomUUID(),
          etag: before.etag,
          actor: 'adminA',
        }),
        clients,
      );
      journal.grantA2Id = assigned.grant.id;
      await save(journal);
    }
    await synthetic.call('GET', roleRead);
    if (!journal.commands.suspendA) {
      const before = await a.call<Member>('GET', `/api/v1/admin/users/${journal.membershipA}`);
      if (!journal.commands.suspendA) {
        expect(before.data.membership.membershipStatus).toBe('ACTIVE');
        expect(before.etag).not.toBe('');
      }
      await command<Member>(
        journal,
        'suspendA',
        () => ({
          method: 'POST',
          path: `/api/v1/admin/users/${journal.membershipA}/suspend`,
          body: { reasonCode: 'ACCESS_REVIEW' },
          key: randomUUID(),
          etag: before.etag,
          actor: 'adminA',
        }),
        clients,
      );
    }
    const suspended = await a.call<Member>('GET', `/api/v1/admin/users/${journal.membershipA}`);
    expect(suspended.data.membership.membershipStatus).toBe('SUSPENDED');
    await synthetic.deniedRead(roleRead);
    await syntheticB.call('GET', roleRead);
    const retainedA = await a.call<GrantPage>(
      'GET',
      `/api/v1/admin/users/${journal.membershipA}/role-grants?limit=2`,
    );
    expect(retainedA.data.items.some((item) => item.id === journal.grantA2Id)).toBe(true);
    const intactB = await b.call<Member>('GET', `/api/v1/admin/users/${journal.membershipB}`);
    expect(intactB.data.membership.membershipStatus).toBe('ACTIVE');
    expect(intactB.data.assignedRoles.some((item) => item.code === role!.code)).toBe(true);
    if (!journal.commands.revokeB || journal.commands.revokeB.status !== 'committed') {
      const before = await b.call<GrantPage>(
        'GET',
        `/api/v1/admin/users/${journal.membershipB}/role-grants?limit=2`,
      );
      expect(before.data.items.find((item) => item.id === journal.grantBId)?.canRevoke).toBe(true);
      await command<GrantResult>(
        journal,
        'revokeB',
        () => ({
          method: 'POST',
          path: `/api/v1/admin/users/${journal.membershipB}/role-grants/${journal.grantBId}/revoke`,
          body: { reasonCode: 'ACCESS_REVIEW' },
          key: randomUUID(),
          etag: before.etag,
          actor: 'adminB',
        }),
        clients,
      );
    }
    await syntheticB.deniedRead(roleRead);
  } finally {
    await Promise.allSettled([
      a.close(),
      b.close(),
      synthetic.close(),
      syntheticB.close(),
      anonymousApi.dispose(),
      mail.dispose(),
    ]);
  }
});

test('privileged request stays pending through maker refusal and is cancelled without a grant', async () => {
  test.skip(
    process.env['E2E_MANAGEMENT_PRIVILEGED_REQUEST_LIVE'] !== '1',
    'requires a separate explicit privileged-request opt-in',
  );
  expect(loopback(origin, '5181'), 'operator-started numeric-loopback UI at 5181').toBe(true);
  const journal = await loadExisting();
  const maker = new Client(await request.newContext(), seededPassword);
  const target = new Client(await request.newContext(), journal.password);
  const clients = {
    adminA: maker,
    adminB: maker,
    synthetic: target,
    anonymous: await request.newContext(),
  };
  const memberId = journal.membershipB!;
  const grantPath = `/api/v1/admin/users/${memberId}/role-grants?limit=10`;
  const eligibilityPath = `/api/v1/admin/users/${memberId}/role-change-eligibility`;
  const readPath = '/api/v1/rule-sets?limit=1';
  let completed = false;
  let failure: unknown;
  let cleanupUncertain = false;
  try {
    await maker.login('admin.b', 'DEMO_B');
    await maker.stepUp();
    await target.login(journal.loginHandle!, 'DEMO_B');
    const memberBefore = await maker.call<Member>('GET', `/api/v1/admin/users/${memberId}`);
    expect(memberBefore.data.membership.membershipStatus).toBe('ACTIVE');
    expect(memberBefore.data.membership.actorType).toBe('HUMAN');
    expect(
      memberBefore.data.membership.displayName === journal.displayName,
      'synthetic target',
    ).toBe(true);
    const grantsBefore = await maker.call<GrantPage>('GET', grantPath);
    expect(grantsBefore.data.membershipId).toBe(memberId);
    expect(grantsBefore.data.canAssign).toBe(true);
    expect(
      grantsBefore.data.items.some((item) => item.canRevoke),
      'zero effective grants',
    ).toBe(false);
    expect(
      grantsBefore.data.items.some((item) => item.id === journal.grantBId),
      'known ended B grant',
    ).toBe(true);
    await target.deniedRead(readPath);

    const options = await maker.call<components['schemas']['PrivilegedRoleAssignmentOptions']>(
      'GET',
      '/api/v1/admin/privileged-role-assignment-options',
    );
    const role = options.data.items.find(
      (item) => item.code === 'RULE_APPROVER' && item.scopeType === 'TENANT',
    );
    expect(role, 'public RULE_APPROVER proposal option').toBeDefined();
    expect(role!.requiresApproval).toBe(true);
    expect([...role!.permissionCodes].sort()).toEqual(['rule.publish', 'rule.read']);
    expect(/^[0-9a-f]{64}$/.test(role!.configurationHash)).toBe(true);
    const eligible = await maker.call<components['schemas']['RoleChangeEligibility']>(
      'GET',
      eligibilityPath,
    );
    expect(eligible.data.membershipId).toBe(memberId);
    expect(eligible.data.membershipRowVersion).toBe(memberBefore.data.membership.rowVersion);
    expect(eligible.etag).toBe(`"${eligible.data.membershipRowVersion}"`);
    if (!journal.commands.privilegedCreateB) expect(eligible.data.canRequestAssignment).toBe(true);
    journal.privilegedCheckerAvailability = eligible.data.checkerAvailability;
    await save(journal);

    const created = await command<RoleChangeResult>(
      journal,
      'privilegedCreateB',
      () => ({
        method: 'POST',
        path: `/api/v1/admin/users/${memberId}/role-change-requests`,
        body: {
          operation: 'ASSIGN',
          roleCode: role!.code,
          configurationHash: role!.configurationHash,
          reasonCode: 'ONBOARDING',
        },
        key: randomUUID(),
        etag: eligible.etag,
        actor: 'adminB',
      }),
      clients,
    );
    expect(created.request.operation).toBe('ASSIGN');
    expect(created.request.status).toBe('PENDING');
    expect(created.request.targetMembershipId).toBe(memberId);
    expect(created.request.roleCode).toBe('RULE_APPROVER');
    expect(created.appliedGrant).toBe(null);
    expect(created.membershipRowVersion).toBe(null);
    journal.privilegedRequestId = created.request.id;
    await save(journal);
    if (!journal.privilegedCreateReplayObserved) {
      const frozen = journal.commands.privilegedCreateB!.command;
      const replay = await maker.call<RoleChangeResult>(
        frozen.method,
        frozen.path,
        frozen.body,
        frozen,
      );
      expect(replay.status).toBe(201);
      expect(replay.replayed).toBe(true);
      expect(
        JSON.stringify(replay.data) === JSON.stringify(created),
        'identical frozen create receipt',
      ).toBe(true);
      journal.privilegedCreateReplayObserved = true;
      await save(journal);
    }

    const detailPath = `/api/v1/admin/role-change-requests/${journal.privilegedRequestId}`;
    let detail = await maker.call<RoleChangeDetail>('GET', detailPath);
    if (journal.commands.privilegedCancelB?.status !== 'committed') {
      if (!journal.commands.privilegedCancelB) expect(detail.data.request.status).toBe('PENDING');
      if (detail.data.request.status === 'PENDING') {
        expect(detail.data.canApprove).toBe(false);
        expect(detail.data.approvalRefusalCode).toBe('MAKER_CHECKER_SAME_ACTOR');
        expect(detail.data.canCancel).toBe(true);
      }
      await refusedCommand(
        journal,
        'privilegedSelfApproveB',
        () => ({
          method: 'POST',
          path: `${detailPath}/approve`,
          body: {},
          key: randomUUID(),
          etag: detail.etag,
          actor: 'adminB',
        }),
        maker,
        'MAKER_CHECKER_SAME_ACTOR',
      );
      detail = await maker.call<RoleChangeDetail>('GET', detailPath);
      if (!journal.commands.privilegedCancelB) expect(detail.data.request.status).toBe('PENDING');
      const grantsPending = await maker.call<GrantPage>('GET', grantPath);
      expect(grantsPending.data.membershipRowVersion).toBe(grantsBefore.data.membershipRowVersion);
      expect(grantsPending.data.items.map((item) => item.id).join('|')).toBe(
        grantsBefore.data.items.map((item) => item.id).join('|'),
      );
      await target.deniedRead(readPath);
    }

    const cancelled = await command<RoleChangeResult>(
      journal,
      'privilegedCancelB',
      () => ({
        method: 'POST',
        path: `${detailPath}/cancel`,
        body: { reasonCode: 'WITHDRAWN' },
        key: randomUUID(),
        etag: detail.etag,
        actor: 'adminB',
      }),
      clients,
    );
    expect(cancelled.request.status).toBe('CANCELLED');
    expect(cancelled.appliedGrant).toBe(null);
    expect(cancelled.membershipRowVersion).toBe(null);
    if (!journal.privilegedCancelReplayObserved) {
      const frozen = journal.commands.privilegedCancelB!.command;
      const replay = await maker.call<RoleChangeResult>(
        frozen.method,
        frozen.path,
        frozen.body,
        frozen,
      );
      expect(replay.status).toBe(200);
      expect(replay.replayed).toBe(true);
      expect(
        JSON.stringify(replay.data) === JSON.stringify(cancelled),
        'identical frozen cancel receipt',
      ).toBe(true);
      journal.privilegedCancelReplayObserved = true;
      await save(journal);
    }
    const finalDetail = await maker.call<RoleChangeDetail>('GET', detailPath);
    expect(finalDetail.data.request.status).toBe('CANCELLED');
    expect(finalDetail.data.request.appliedGrantId).toBe(null);
    const history = await maker.call<components['schemas']['RoleChangeRequestPage']>(
      'GET',
      `/api/v1/admin/role-change-requests?status=CANCELLED&membershipId=${memberId}&limit=10`,
    );
    expect(
      history.data.items.some(
        (item) => item.id === journal.privilegedRequestId && item.status === 'CANCELLED',
      ),
    ).toBe(true);
    const grantsFinal = await maker.call<GrantPage>('GET', grantPath);
    expect(grantsFinal.data.membershipRowVersion).toBe(grantsBefore.data.membershipRowVersion);
    expect(grantsFinal.data.canAssign).toBe(true);
    expect(grantsFinal.data.items.map((item) => item.id).join('|')).toBe(
      grantsBefore.data.items.map((item) => item.id).join('|'),
    );
    await target.deniedRead(readPath);
    expect(journal.commands.privilegedSelfApproveB?.status).toBe('committed');
    expect(journal.commands.privilegedSelfApproveB?.result).toEqual({
      status: 403,
      code: 'MAKER_CHECKER_SAME_ACTOR',
    });
    expect(journal.privilegedCreateReplayObserved).toBe(true);
    expect(journal.privilegedCancelReplayObserved).toBe(true);
    completed = true;
  } catch (error) {
    failure = error;
  } finally {
    if (
      !completed &&
      journal.commands.privilegedCreateB &&
      journal.commands.privilegedCancelB?.status !== 'committed'
    ) {
      try {
        const created = await command<RoleChangeResult>(
          journal,
          'privilegedCreateB',
          () => {
            throw new Error('Prepared request must reuse its saved command');
          },
          clients,
        );
        const requestId = created.request.id;
        journal.privilegedRequestId = requestId;
        await save(journal);
        const detailPath = `/api/v1/admin/role-change-requests/${requestId}`;
        const detail = await maker.call<RoleChangeDetail>('GET', detailPath);
        if (
          detail.data.request.status === 'PENDING' ||
          journal.commands.privilegedCancelB?.status === 'prepared'
        ) {
          await command<RoleChangeResult>(
            journal,
            'privilegedCancelB',
            () => ({
              method: 'POST',
              path: `${detailPath}/cancel`,
              body: { reasonCode: 'WITHDRAWN' },
              key: randomUUID(),
              etag: detail.etag,
              actor: 'adminB',
            }),
            clients,
          );
        }
        const closed = await maker.call<RoleChangeDetail>('GET', detailPath);
        if (closed.data.request.status !== 'CANCELLED') cleanupUncertain = true;
      } catch {
        cleanupUncertain = true;
      }
    }
    await Promise.allSettled([maker.close(), target.close(), clients.anonymous.dispose()]);
  }
  if (cleanupUncertain)
    throw new Error(
      'Privileged request cleanup is uncertain; retain the journal and rerun this test to finish the frozen cancellation',
    );
  if (failure) throw failure;
});
