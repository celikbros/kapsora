import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { App } from './App';
import { createServices } from './services';

/**
 * The provider portal against the mock world, as provider.a: an actor whose grants are
 * bound to one organization. Every lookup that finds nothing fails the test, so a flow
 * cannot pass over an empty fixture.
 */
const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

/**
 * How many times a screen asked the server for one person. Section 2.6 exists so a list is
 * not one extra read per row, and the only way to assert that is to count the calls.
 */
let personCalls = 0;

beforeAll(() => {
  initI18n('tr');
  server.events.on('request:start', ({ request }) => {
    if (/\/api\/v1\/people\/[^/]+$/.test(new URL(request.url).pathname)) personCalls += 1;
  });
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  api.reset();
  personCalls = 0;
});
afterAll(() => server.close());

function mount(path: string) {
  const services = createServices({ baseUrl: BASE });
  const history = createMemoryHistory({ initialEntries: [path] });
  render(<App services={services} history={history} />);
  return { services, history };
}

async function login(username: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

function providerOrganizationId(): string {
  const account = api.world.accounts.find((a) => a.username === 'provider.a')!;
  const grant = account.memberships[0]!.scopes?.find((s) => s.type === 'ORGANIZATION');
  expect(grant?.id, 'provider.a carries no ORGANIZATION grant').toBeTruthy();
  return grant!.id!;
}

describe('provider portal', () => {
  it('shows a provider only its own requests, and what each waits for', async () => {
    mount('/requests');
    await login('provider.a');
    const table = await screen.findByTestId('my-requests-table');
    const own = providerOrganizationId();
    const tenant = api.world.tenants.find((t) => t.code === 'DEMO_A')!;
    const mine = api.world.serviceRequests.filter(
      (r) => r.tenantId === tenant.id && r.providerOrganizationId === own,
    );
    const foreign = api.world.serviceRequests.filter(
      (r) =>
        r.tenantId === tenant.id && r.providerOrganizationId && r.providerOrganizationId !== own,
    );
    expect(foreign.length, 'the world has no other provider to be hidden from').toBeGreaterThan(0);
    // Rows include the header; the count is exactly the provider's own and nothing else.
    await waitFor(() =>
      expect(within(table).getAllByRole('row')).toHaveLength(Math.min(mine.length, 50) + 1),
    );
    for (const r of foreign) {
      expect(within(table).queryByText(r.reference)).toBeNull();
    }
    const waiting = mine.find((r) => r.status === 'PENDING_DOCUMENT');
    expect(waiting, 'no own request waiting on a document').toBeDefined();
    const row = within(table)
      .getAllByRole('row')
      .find((tr) => tr.textContent?.includes(waiting!.reference))!;
    expect(row).toHaveTextContent('Sizden belge bekliyor');
    // The member's name comes with the row (WP-I5-05 section 2.6): it is asserted without
    // waiting, because there is nothing left to wait for, and the person endpoint is not
    // called at all for this list. A row of dashes would mean the screen shows ids in
    // disguise; a '…' would mean it is still resolving one name per line.
    const person = api.world.people.find((p) => p.id === waiting!.personId)!;
    const displayName = [person.firstName, person.middleName, person.lastName]
      .filter(Boolean)
      .join(' ');
    expect(row).toHaveTextContent(displayName);
    expect(row.textContent).not.toContain('…');
    expect(row.textContent).not.toMatch(/—.*—/);
    expect(personCalls, 'the list read a person per row').toBe(0);
  });

  it.each([false, true])(
    'checks eligibility and submits without duplicate drafts (first submit fails: %s)',
    { timeout: 20_000 },
    async (failFirstSubmit) => {
      const { history, services: appServices } = mount('/');
      const create = vi.spyOn(appServices.ops.requests, 'create');
      const submit = vi.spyOn(appServices.ops.requests, 'submit');
      if (failFirstSubmit) submit.mockRejectedValueOnce(new Error('connection lost'));
      await login('provider.a');
      await screen.findByRole('heading', { name: 'Yeni talep' });
      const pane = screen.getByTestId('eligibility-pane');
      expect(pane).toHaveTextContent('cevap burada görünür');

      const user = userEvent.setup();
      // The member by name: two letters narrow the list, a click picks. A person with exactly
      // one active enrollment. The multiple-candidate path is covered in requestFlow.test.tsx;
      // this case preserves direct single-plan submission.
      const person = api.world.people.find(
        (p) =>
          p.tenantId === api.world.tenants.find((t) => t.code === 'DEMO_A')!.id &&
          p.status === 'ACTIVE' &&
          api.world.enrollments.filter((e) => e.personId === p.id && e.status === 'ACTIVE')
            .length === 1,
      );
      expect(person, 'no enrolled person in the world').toBeDefined();
      // The world stores the parts; the API shows the name the way toPersonSummary joins them.
      const displayName = [person!.firstName, person!.middleName, person!.lastName]
        .filter(Boolean)
        .join(' ');
      await user.type(screen.getByLabelText('Ada göre ara'), displayName.slice(0, 3));
      await user.click(await screen.findByRole('button', { name: displayName }));
      await screen.findByText(`Bulunan: ${displayName}`);

      // The service select is the one combobox whose name starts with Hizmet; the date is an input.
      // The right column answers on its own, with one of three verdicts in the server's
      // words. A service the catalog has not mapped yet is "review required", not a refusal.
      const services = screen.getByRole('combobox', { name: /^Hizmet/ });
      await waitFor(() =>
        expect(within(services).getAllByRole('option').length).toBeGreaterThan(1),
      );
      const first = within(services).getAllByRole('option')[1] as HTMLOptionElement;
      await user.selectOptions(services, first.value);
      // The heading itself says 'Uygunluk'; wait for the answer, not the word.
      await waitFor(() => expect(pane).not.toHaveTextContent(/Hesaplanıyor|cevap burada görünür/), {
        timeout: 5_000,
      });

      // Gönder exists only once the check found the enrollment; its absence is the diagnosis.
      const send = await screen.findByRole('button', { name: 'Gönder' }, { timeout: 5_000 });
      await user.click(send);
      if (failFirstSubmit) {
        await screen.findByRole('alert');
        await waitFor(() => expect(send).toBeEnabled());
        await user.click(send);
      }
      await waitFor(
        () => {
          const alert = screen.queryByRole('alert');
          if (alert) throw new Error(`submit refused: ${alert.textContent}`);
          expect(history.location.pathname).toMatch(/^\/requests\/[0-9a-f-]+$/);
        },
        { timeout: 5_000 },
      );
      const id = history.location.pathname.split('/').pop()!;
      const stored = api.world.serviceRequests.find((r) => r.id === id)!;
      // Created and submitted in one act, landing on a gate outcome and never on SUBMITTED.
      expect(stored.status).not.toBe('DRAFT');
      expect(stored.status).not.toBe('SUBMITTED');
      expect(stored.channel).toBe('PROVIDER_PORTAL');
      expect(stored.providerOrganizationId).toBe(providerOrganizationId());
      expect(create).toHaveBeenCalledTimes(1);
      expect(submit).toHaveBeenCalledTimes(failFirstSubmit ? 2 : 1);
      if (failFirstSubmit) expect(submit.mock.calls[1]).toEqual(submit.mock.calls[0]);
    },
  );

  it(
    'uploads the document a request is waiting for and shows the scan as it happens',
    { timeout: 20_000 },
    async () => {
      const own = providerOrganizationId();
      const waiting = api.world.serviceRequests.find(
        (r) => r.providerOrganizationId === own && r.status === 'PENDING_DOCUMENT',
      );
      expect(waiting, 'no own request waiting on a document').toBeDefined();
      expect(waiting!.requiredDocumentTypes?.length).toBeGreaterThan(0);

      const { history } = mount('/requests');
      await login('provider.a');
      await screen.findByTestId('my-requests-table');
      await history.push(`/requests/${waiting!.id}`);
      const form = await screen.findByTestId('document-upload-form');
      const user = userEvent.setup();
      const file = new File(['%PDF-1.4 fatura'], 'fatura.pdf', { type: 'application/pdf' });
      await user.upload(within(form).getByLabelText(/Dosya seç/), file);
      // The form offers a select of the types still missing, or a code field when every
      // named type is already attached and the desk is adding something further.
      const typeField = within(form).getByLabelText(/Belge türü/);
      if (typeField instanceof HTMLSelectElement) {
        const offered = Array.from(typeField.options)
          .map((o) => o.value)
          .filter(Boolean);
        await user.selectOptions(typeField, offered[0]!);
      } else {
        await user.type(typeField, 'INVOICE');
      }
      await user.click(within(form).getByRole('button', { name: 'Belge yükle' }));

      // The state is shown as it is: queued or scanning, never "uploaded", and no download.
      const table = await screen.findByTestId('documents-table');
      const row = await waitFor(() => {
        const r = within(table)
          .getAllByRole('row')
          .find((tr) => tr.textContent?.includes('fatura.pdf'));
        expect(r).toBeDefined();
        return r!;
      });
      expect(row.textContent).toMatch(/sıraya alındı|Taranıyor/);
      expect(within(row).queryByRole('button', { name: 'İndir' })).toBeNull();

      // The worker's verdict lands, and only then is the file downloadable.
      const uploaded = api.world.documents.find((d) => d.originalFilename === 'fatura.pdf')!;
      api.world.advanceScan(uploaded.id, 'CLEAN');
      await waitFor(() => expect(row).toHaveTextContent('Temiz'), { timeout: 5_000 });
      expect(within(row).getByRole('button', { name: 'İndir' })).toBeInTheDocument();
    },
  );

  // The defect this guards was found by a smoke run, not by a reviewer: the type field
  // used to be a select of the *missing* types, and the missing list is only known once the
  // linked documents arrive. On a request whose named types are already attached the field
  // was a select for one frame and a text box the next — a control that changes what it is
  // while somebody is using it. It now follows the request, which is loaded before the
  // panel renders at all, so it is the same control before and after the documents land.
  it(
    'keeps the document type field the same control while the linked documents arrive',
    { timeout: 20_000 },
    async () => {
      const own = providerOrganizationId();
      // A request whose named types are all attached already: the old rule made this one
      // flip, because `missing` starts full and empties.
      const settled = api.world.serviceRequests.find((r) => {
        if (r.providerOrganizationId !== own) return false;
        const named = r.requiredDocumentTypes ?? [];
        if (named.length === 0) return false;
        const attached = new Set(
          api.world.documentLinks
            .filter((l) => l.aggregateId === r.id)
            .map((l) => l.documentTypeCode),
        );
        return named.every((code) => attached.has(code));
      });
      expect(settled, 'no own request with every named document already attached').toBeDefined();

      const { history } = mount('/requests');
      await login('provider.a');
      await screen.findByTestId('my-requests-table');
      await history.push(`/requests/${settled!.id}`);
      const form = await screen.findByTestId('document-upload-form');

      const first = within(form).getByLabelText(/Belge türü/).tagName;
      // Wait for the documents themselves — the query whose settling used to change it.
      const table = await screen.findByTestId('documents-table');
      await waitFor(() => expect(within(table).getAllByRole('row').length).toBeGreaterThan(1));
      const after = within(form).getByLabelText(/Belge türü/).tagName;

      expect(after).toBe(first);
      expect(after).toBe('SELECT');
    },
  );
});
