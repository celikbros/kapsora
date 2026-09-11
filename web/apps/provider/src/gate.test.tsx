import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { App } from './App';
import { createServices } from './services';

/**
 * The provider portal admits only accounts that hold a hospital or hotel grant. A backoffice
 * account that signs in here is told which app it is for and shown nothing else.
 */
const { api, server } = createMockServer({ organizationsPerTenant: 4 });
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => api.reset());
afterAll(() => server.close());

async function signInAs(username: string) {
  const services = createServices({ baseUrl: 'http://mock.test' });
  const history = createMemoryHistory({ initialEntries: ['/'] });
  render(<App services={services} history={history} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return { history, user };
}

describe('the app gate', () => {
  it('tells a backoffice account it has no work here and names the app it has', async () => {
    const { history } = await signInAs('financial.reviewer');
    expect(
      await screen.findByRole('heading', { name: 'Hesabınızın bu uygulamada bir görevi yok' }),
    ).toBeInTheDocument();
    expect(history.location.pathname).toBe('/auth/not-for-app');
    const fits = screen.getByTestId('not-for-app-fits');
    expect(
      within(fits)
        .getAllByRole('listitem')
        .map((li) => li.textContent),
    ).toEqual(['Yönetim paneli']);
    // Nothing of the portal renders behind the notice.
    expect(screen.queryByRole('navigation')).not.toBeInTheDocument();
  });

  it('lets a provider account straight in', async () => {
    const { history } = await signInAs('billing.a');
    await screen.findByRole('link', { name: /KAPSORA/ });
    expect(history.location.pathname).not.toBe('/auth/not-for-app');
  });

  it('signs the account out from the notice', async () => {
    const { history, user } = await signInAs('member.a');
    await user.click(await screen.findByRole('button', { name: 'Başka bir hesapla giriş yap' }));
    expect(await screen.findByLabelText(/Kullanıcı adı/)).toBeInTheDocument();
    expect(history.location.pathname).toBe('/auth/login');
  });
});
