import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { App } from '../App';
import { createServices } from '../api';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => api.reset());
afterAll(() => server.close());

function mount(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(<App services={createServices({ baseUrl: 'http://mock.test' })} history={history} />);
  return history;
}

async function login(username: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

describe('health services landing', () => {
  it('does not fetch records while opening the landing', async () => {
    const requested: string[] = [];
    const recordRequest = ({ request }: { request: Request }) => {
      requested.push(new URL(request.url).pathname);
    };
    server.events.on('request:start', recordRequest);
    mount('/health-services');
    await login('doctor.a');
    await screen.findByTestId('health-services');
    server.events.removeListener('request:start', recordRequest);
    expect(
      requested.filter((path) =>
        /\/api\/v1\/(claims|medical-reports|health|people|persons|requests)(?:\/|$)/.test(path),
      ),
    ).toEqual([]);
  });

  it('takes a financial reviewer to the filtered financial claims', async () => {
    const history = mount('/health-services');
    const user = await login('financial.reviewer');
    const hub = await screen.findByTestId('health-services');
    expect(within(hub).getByTestId('health-financial-claims')).toHaveTextContent(
      'Mali değerlendirme',
    );
    expect(within(hub).queryByTestId('health-medical-claims')).toBeNull();
    expect(within(hub).queryByTestId('health-report-review')).toBeNull();
    await user.click(within(hub).getByTestId('health-financial-claims'));
    await waitFor(() => expect(history.location.pathname).toBe('/claims'));
    expect(history.location.search).toBe('?status=PENDING_FINANCIAL');
    expect(await screen.findByLabelText('Durum')).toHaveValue('PENDING_FINANCIAL');
  });

  it('offers a medical reviewer the report and medical claim queues', async () => {
    const history = mount('/health-services');
    const user = await login('doctor.a');
    const hub = await screen.findByTestId('health-services');
    expect(within(hub).getByTestId('health-report-review')).toBeInTheDocument();
    expect(within(hub).getByTestId('health-medical-claims')).toBeInTheDocument();
    expect(within(hub).queryByTestId('health-financial-claims')).toBeNull();
    await user.click(within(hub).getByTestId('health-report-review'));
    await waitFor(() => expect(history.location.pathname).toBe('/medical-reports'));
  });

  it('offers HR records and requests without review actions', async () => {
    mount('/health-services');
    await login('sponsor.hr');
    const hub = await screen.findByTestId('health-services');
    for (const id of ['health-claim-records', 'health-requests', 'health-person-records']) {
      expect(within(hub).getByTestId(id)).toBeInTheDocument();
    }
    for (const id of ['health-report-review', 'health-medical-claims', 'health-financial-claims']) {
      expect(within(hub).queryByTestId(id)).toBeNull();
    }
  });

  it('shows no actions on a direct visit and hides health in both menus without a grant', async () => {
    const account = api.world.accounts.find((a) => a.username === 'admin.a')!;
    account.memberships[0]!.permissions = ['organization.read'];
    const history = mount('/health-services');
    const user = await login('admin.a');
    const hub = await screen.findByTestId('health-services');
    expect(within(hub).getByText('Bu işlem için yetkiniz yok.')).toBeInTheDocument();
    expect(within(hub).queryAllByRole('link')).toHaveLength(0);
    const nav = screen.getByRole('navigation', { name: 'Ana menü' });
    expect(within(nav).queryByRole('link', { name: 'Sağlık' })).toBeNull();
    await user.click(screen.getByRole('link', { name: 'KAPSORA' }));
    await waitFor(() => expect(history.location.pathname).toBe('/'));
    expect(screen.queryByRole('link', { name: 'Sağlık' })).toBeNull();
  });
});
