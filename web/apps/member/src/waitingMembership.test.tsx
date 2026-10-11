import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it } from 'vitest';
import { App } from './App';
import { createServices } from './services';

const { api, server } = createMockServer();

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => api.reset());
afterAll(() => server.close());

it('shows a new zero-grant institution after login while preserving the member app choice', async () => {
  const member = api.world.accounts.find((account) => account.username === 'member.a')!;
  member.memberships.push({ tenantCode: 'DEMO_B', permissions: [], membershipOnly: true });
  const services = createServices({ baseUrl: 'http://mock.test' });
  const history = createMemoryHistory({ initialEntries: ['/'] });
  render(<App services={services} history={history} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'member.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  expect(await screen.findByText('Yetki ataması bekleyen kurumlar')).toBeInTheDocument();
  expect(
    screen.getByText(api.world.tenants.find((tenant) => tenant.code === 'DEMO_B')!.displayName),
  ).toBeInTheDocument();
  expect(screen.getByTestId('app-choices')).toHaveTextContent('Üye uygulaması');
  expect(history.location.pathname).toBe('/auth/apps');
});
