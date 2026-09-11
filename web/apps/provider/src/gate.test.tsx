import { createMockServer } from '@kapsora/api-client/mocks/node';
import { browser } from '@kapsora/auth';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { App } from './App';
import { createServices } from './services';

/**
 * The single sign-in, from the provider portal's door. An account whose only app is another
 * one is handed there; one with several apps, none of them this one, chooses; a provider
 * account stays. Nothing of the portal renders for an account with no work in it.
 */
const { api, server } = createMockServer({ organizationsPerTenant: 4 });
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  api.reset();
  vi.restoreAllMocks();
});
afterAll(() => server.close());

async function signInAs(username: string, path = '/') {
  const leave = vi.spyOn(browser, 'assign').mockImplementation(() => {});
  const services = createServices({ baseUrl: 'http://mock.test' });
  const history = createMemoryHistory({ initialEntries: [path] });
  render(<App services={services} history={history} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return { history, user, leave };
}

describe('the single sign-in from the provider portal', () => {
  it('hands a backoffice account straight to the backoffice', async () => {
    const { leave } = await signInAs('financial.reviewer');
    await waitFor(() => expect(leave).toHaveBeenCalledTimes(1));
    expect(leave.mock.calls[0]![0]).toMatch(/:5181\/$/);
    expect(screen.queryByRole('navigation')).not.toBeInTheDocument();
  });

  it('hands a member straight to the member app', async () => {
    const { leave } = await signInAs('member.a');
    await waitFor(() => expect(leave).toHaveBeenCalledTimes(1));
    expect(leave.mock.calls[0]![0]).toMatch(/:5183\/$/);
  });

  it('lets an account with two other apps choose, and offers no way to stay', async () => {
    const { history, leave } = await signInAs('staff.member');
    expect(
      await screen.findByRole('heading', { name: 'Hesabınızın bu uygulamada bir görevi yok' }),
    ).toBeInTheDocument();
    expect(history.location.pathname).toBe('/auth/apps');
    const choices = within(screen.getByTestId('app-choices')).getAllByRole('listitem');
    expect(choices.map((li) => li.textContent)).toEqual(['Yönetim paneliAç', 'Üye uygulamasıAç']);
    expect(within(choices[1]!).getByRole('link', { name: 'Aç' })).toHaveAttribute(
      'href',
      expect.stringMatching(/:5183\/$/),
    );
    expect(screen.queryByRole('button', { name: 'Devam et' })).not.toBeInTheDocument();
    expect(leave).not.toHaveBeenCalled();
  });

  it('lets a provider account straight in', async () => {
    const { history, leave } = await signInAs('billing.a');
    await screen.findByRole('link', { name: /KAPSORA/ });
    expect(history.location.pathname).not.toBe('/auth/apps');
    expect(leave).not.toHaveBeenCalled();
  });

  it('sends a signed-in account that opens the portal by address to the chooser, and forwards it', async () => {
    // Sign in through the portal as a member, then come back to a portal deep link: the
    // guard, not the sign-in screen, is what catches it this time.
    const { leave, history } = await signInAs('member.a', '/claims');
    await waitFor(() => expect(leave).toHaveBeenCalled());
    expect(history.location.pathname).not.toBe('/claims');
  });
});
