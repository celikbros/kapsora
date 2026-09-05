import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { App } from './App';
import { createServices } from './api';

/**
 * M4 screens against the mock world. Every fixture these flows land on is looked up by a
 * property the server guarantees (a status, a scan verdict, a held item), and a lookup
 * that finds nothing fails the test rather than letting it pass over an empty set.
 */
const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => api.reset());
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

function tenantA() {
  return api.world.tenants.find((t) => t.code === 'DEMO_A')!;
}

function requestIn(status: string) {
  const row = api.world.serviceRequests.find(
    (r) => r.tenantId === tenantA().id && r.status === status && !r.returnReasonCode,
  );
  expect(row, `no fixture request in status ${status}`).toBeDefined();
  return row!;
}

describe('requests', () => {
  it('offers only the commands each status allows, and never writes a status', async () => {
    const { history } = mount('/requests');
    await login('admin.a');
    await screen.findByTestId('request-table');

    // A request under review: every review command, each its own button.
    await history.push(`/requests/${requestIn('PENDING_REVIEW').id}`);
    const commands = await screen.findByTestId('request-commands');
    for (const name of ['Onayla', 'Kısmen onayla', 'İade et', 'Reddet', 'İptal et']) {
      expect(within(commands).getByRole('button', { name })).toBeInTheDocument();
    }
    expect(within(commands).queryByRole('button', { name: 'Gönder' })).toBeNull();

    // A rejected one is finished: it says why, and offers nothing.
    await history.push(`/requests/${requestIn('REJECTED').id}`);
    await screen.findByTestId('rejected-panel');
    expect(screen.queryByTestId('request-commands')).toBeNull();
    expect(screen.queryByTestId('returned-panel')).toBeNull();

    // An approved one can still be cancelled, and nothing else.
    await history.push(`/requests/${requestIn('APPROVED').id}`);
    const approvedCommands = await screen.findByTestId('request-commands');
    expect(within(approvedCommands).getAllByRole('button')).toHaveLength(1);
    expect(within(approvedCommands).getByRole('button', { name: 'İptal et' })).toBeInTheDocument();
  });

  it('returns a request for correction, then resubmits it as version 2', async () => {
    const { history } = mount('/requests');
    await login('admin.a');
    await screen.findByTestId('request-table');
    const target = requestIn('PENDING_REVIEW');
    await history.push(`/requests/${target.id}`);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'İade et' }));
    const dialog = await screen.findByRole('dialog', { name: 'İade et' });
    await user.type(within(dialog).getByLabelText(/Gerekçe kodu/), 'MISSING_INVOICE');
    await user.type(within(dialog).getByLabelText(/Açıklama/), 'Fatura eksik');
    await user.click(within(dialog).getByRole('button', { name: 'İade et' }));

    // Returned and rejected look different: the returned panel invites a correction.
    const returned = await screen.findByTestId('returned-panel');
    expect(returned).toHaveTextContent('düzeltilmek üzere iade edildi');
    expect(returned).toHaveTextContent('MISSING_INVOICE');
    expect(returned).toHaveTextContent('Fatura eksik');
    expect(screen.queryByTestId('rejected-panel')).toBeNull();

    // The correction path is the draft editor, and the resubmit is a real command.
    await screen.findByTestId('request-items-editor');
    await user.click(screen.getByRole('button', { name: 'Gönder' }));
    const confirm = await screen.findByRole('dialog', { name: 'Gönder' });
    await user.click(within(confirm).getByRole('button', { name: 'Gönder' }));
    await waitFor(() => expect(screen.queryByTestId('returned-panel')).toBeNull());

    // Version 2 exists and version 1 is the one that was decided; the screen shows both.
    await waitFor(() => expect(screen.getByText(/Sürüm 2/)).toBeInTheDocument());
    expect(screen.getByText(/Sürüm 1/)).toBeInTheDocument();
    const stored = api.world.serviceRequests.find((r) => r.id === target.id)!;
    expect(stored.currentVersionNo).toBe(2);
    expect(stored.status).not.toBe('SUBMITTED');
  });

  it('names the document types a request is still waiting for', async () => {
    const { history } = mount('/requests');
    await login('admin.a');
    await screen.findByTestId('request-table');
    const waiting = requestIn('PENDING_DOCUMENT');
    expect(
      waiting.requiredDocumentTypes?.length,
      'fixture names no required types',
    ).toBeGreaterThan(0);
    await history.push(`/requests/${waiting.id}`);
    const missing = await screen.findByRole('heading', { name: 'Eksik belgeler' });
    const section = missing.closest('section')!;
    for (const code of waiting.requiredDocumentTypes!) {
      expect(within(section).getByText(code)).toBeInTheDocument();
    }
  });

  it('shows an infected file as infected and offers it no download', async () => {
    // The scenario is an infected file on a request. The world has infected documents;
    // whether one is linked to a request is fixture luck, so the test links one itself.
    // Links are their own rows in the world; a document knows nothing about its records.
    const infected = api.world.documents.find(
      (d) => d.tenantId === tenantA().id && d.scanStatus === 'INFECTED',
    );
    expect(infected, 'no infected document in the fixture world').toBeDefined();
    const infectedDoc = infected!;
    const request = requestIn('PENDING_REVIEW');
    const link = {
      id: api.world.nextId(),
      tenantId: tenantA().id,
      documentId: infectedDoc.id,
      aggregateType: 'SERVICE_REQUEST',
      aggregateId: request.id,
      documentTypeCode: 'INVOICE',
      createdAt: new Date().toISOString(),
    };
    api.world.documentLinks.push(link);

    const { history } = mount('/requests');
    await login('admin.a');
    await screen.findByTestId('request-table');
    await history.push(`/requests/${link.aggregateId}`);
    const table = await screen.findByTestId('documents-table');
    const row = within(table)
      .getAllByRole('row')
      .find((r) => r.textContent?.includes(infectedDoc.originalFilename))!;
    expect(row).toHaveTextContent('Zararlı yazılım bulundu');
    expect(within(row).queryByRole('button', { name: 'İndir' })).toBeNull();

    // And a clean one on the same record, if there is one, does offer it.
    const cleanLink = api.world.documentLinks.find(
      (l) =>
        l.aggregateId === link.aggregateId &&
        api.world.documents.some(
          (d) => d.id === l.documentId && d.scanStatus === 'CLEAN' && d.bucket === 'secure',
        ),
    );
    if (cleanLink) {
      const clean = api.world.documents.find((d) => d.id === cleanLink.documentId)!;
      const cleanRow = within(table)
        .getAllByRole('row')
        .find((r) => r.textContent?.includes(clean.originalFilename))!;
      expect(within(cleanRow).getByRole('button', { name: 'İndir' })).toBeInTheDocument();
    }
  });
});

describe('worklist', () => {
  it('tells the loser of a claim race who holds the item', async () => {
    const { history } = mount('/worklist');
    await login('admin.a');
    await history.push('/worklist?view=unassigned');
    const table = await screen.findByTestId('worklist-table');
    // Take the first item the screen itself offers to claim, and find its row in the world.
    const claimButtons = await within(table).findAllByRole('button', { name: 'Üstlen' });
    expect(claimButtons.length, 'the unassigned view offers nothing to claim').toBeGreaterThan(0);
    const row = claimButtons[0]!.closest('tr')!;
    const open = api.world.workItems.find(
      (w) => w.tenantId === tenantA().id && row.textContent?.includes(w.title),
    );
    expect(open, 'the claimable row is not a world item').toBeDefined();

    // Somebody else gets there first, between the render and the click.
    const rival = api.world.accounts.find((a) => a.username === 'reviewer.a')!;
    open!.status = 'CLAIMED';
    open!.assigneeActorId = rival.actorId;
    open!.rowVersion += 1;

    const user = userEvent.setup();
    await user.click(claimButtons[0]!);
    const status = await screen.findByRole('status');
    expect(status).toHaveTextContent('üstlendi');
    expect(status).toHaveTextContent(rival.actorId);
  });

  it('marks a late item by its own clock and shows what is mine', async () => {
    const { history } = mount('/worklist');
    await login('admin.a');
    await history.push('/worklist?view=overdue');
    const table = await screen.findByTestId('worklist-table');
    const lateRows = within(table)
      .getAllByRole('row')
      .filter((r) => r.getAttribute('data-late') === 'true');
    expect(lateRows.length).toBeGreaterThan(0);
    for (const r of lateRows) expect(r).toHaveTextContent('Gecikti');

    await history.push('/worklist?view=mine');
    const mine = await screen.findByTestId('worklist-table');
    for (const r of within(mine).getAllByRole('row').slice(1)) {
      expect(r).toHaveTextContent('siz');
    }
  });
});

describe('notifications', () => {
  it('shows a message nobody was sent, with the reason', async () => {
    mount('/notifications');
    await login('admin.a');
    const user = userEvent.setup();
    await user.click(await screen.findByRole('tab', { name: 'Gönderi kaydı' }));
    const table = await screen.findByTestId('message-table');
    const suppressed = api.world.notificationMessages.find((m) => m.status === 'SUPPRESSED');
    expect(suppressed, 'no suppressed message in the fixture world').toBeDefined();
    const row = within(table)
      .getAllByRole('row')
      .find((r) => r.textContent?.includes('Gönderilmedi'))!;
    expect(row).toBeDefined();
    // The reason is on the row, not behind a click: a silent nothing is what this forbids.
    expect(row.textContent).toMatch(/istemiyor|sessiz saat|şablon yok|adres yok|sağlayıcı yok/);
  });
});
