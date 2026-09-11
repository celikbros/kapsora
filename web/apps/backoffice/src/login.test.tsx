import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { App } from './App';
import { createServices } from './api';

/**
 * The sign-in screen on the development server with the sample data: the password can be shown
 * and hidden again, and each demo account signs in with one press.
 */
const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';

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

describe('the sign-in screen', () => {
  it('shows the password on request and hides it again', async () => {
    mount('/');
    const user = userEvent.setup();
    const password = await screen.findByLabelText(/^Parola/);
    await user.type(password, 'demo parola 2026 kapsora');
    expect(password).toHaveAttribute('type', 'password');
    await user.click(screen.getByRole('button', { name: 'Göster' }));
    expect(password).toHaveAttribute('type', 'text');
    await user.click(screen.getByRole('button', { name: 'Gizle' }));
    expect(password).toHaveAttribute('type', 'password');
  });

  it('signs in as a demo account with one press', async () => {
    mount('/');
    const user = userEvent.setup();
    const list = await screen.findByTestId('demo-accounts');
    await user.click(
      within(list).getByRole('button', { name: 'Fuat Mali Değerlendirici olarak gir' }),
    );
    expect(await screen.findByTestId('dashboard')).toBeInTheDocument();
  });
});
