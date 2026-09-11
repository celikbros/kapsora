import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { App } from './App';
import { createServices } from './api';

/**
 * A reviewer who is also a member reads their own request but cannot decide it: the note
 * stands where the decision would be. A reviewer who is somebody else decides the same one.
 */
const { api, server } = createMockServer({ organizationsPerTenant: 4 });
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => api.reset());
afterAll(() => server.close());

/** A request awaiting review whose person is the person staff.member is. */
function ownPendingRequestId(): string {
  const account = api.world.accounts.find((a) => a.username === 'staff.member')!;
  const self = account.memberships
    .flatMap((m) => m.scopes ?? [])
    .find((g) => g.type === 'PERSON')!.id!;
  const request = api.world.serviceRequests.find(
    (r) => r.personId === self && r.status === 'PENDING_REVIEW',
  );
  if (!request) throw new Error('fixture: staff.member has no request awaiting review');
  return request.id;
}

async function openAs(username: string, path: string) {
  const services = createServices({ baseUrl: 'http://mock.test' });
  const history = createMemoryHistory({ initialEntries: [path] });
  render(<App services={services} history={history} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

describe('a file of the reviewer’s own', () => {
  it('shows the note and no decision on a request of their own person', async () => {
    const id = ownPendingRequestId();
    const user = await openAs('staff.member', `/requests/${id}`);
    // Two apps: the single sign-in asks, and continuing lands on the request.
    await user.click(await screen.findByRole('button', { name: 'Devam et' }));
    expect(await screen.findByTestId('own-file-notice')).toHaveTextContent('Bu dosya size ait.');
    expect(screen.queryByRole('button', { name: 'Onayla' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reddet' })).not.toBeInTheDocument();
  });

  it('offers the decision to a reviewer who is somebody else', async () => {
    const id = ownPendingRequestId();
    await openAs('doctor.a', `/requests/${id}`);
    expect(await screen.findByRole('button', { name: 'Onayla' })).toBeInTheDocument();
    expect(screen.queryByTestId('own-file-notice')).not.toBeInTheDocument();
  });
});
