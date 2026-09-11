import { createMockServer } from '@kapsora/api-client/mocks/node';
import { browser } from '@kapsora/auth';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { App } from './App';
import { createServices } from './api';

/**
 * One account, two apps: staff.member is a medical reviewer and a member. Signing in to the
 * backoffice asks where to continue, the backoffice first; continuing lands in the backoffice
 * as the reviewer, and the member app is one link away.
 */
const { api, server } = createMockServer({ organizationsPerTenant: 4 });

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  api.reset();
  vi.restoreAllMocks();
});
afterAll(() => server.close());

describe('the single sign-in in the backoffice', () => {
  it('lets an account with two apps choose, and continues here as staff', async () => {
    const leave = vi.spyOn(browser, 'assign').mockImplementation(() => {});
    const services = createServices({ baseUrl: 'http://mock.test' });
    const history = createMemoryHistory({ initialEntries: ['/'] });
    render(<App services={services} history={history} />);
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'staff.member');
    await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
    await user.click(screen.getByRole('button', { name: 'Giriş yap' }));

    expect(
      await screen.findByRole('heading', { name: 'Nereden devam etmek istersiniz?' }),
    ).toBeInTheDocument();
    const choices = within(screen.getByTestId('app-choices')).getAllByRole('listitem');
    expect(choices.map((li) => li.textContent)).toEqual([
      'Yönetim paneliDevam et',
      'Üye uygulamasıAç',
    ]);

    await user.click(within(choices[0]!).getByRole('button', { name: 'Devam et' }));
    await screen.findByRole('navigation');
    expect(history.location.pathname).toBe('/');
    // As the reviewer: the backoffice context carries the staff role and acts for nobody.
    const active = services.store.getState().activeTenant!;
    expect(active.permissions).toContain('claim.medical.review');
    expect(active.personId).toBeNull();
    expect(active.selfPersonId).toBeTruthy();
    expect(active.apps).toEqual(['backoffice', 'member']);
    expect(leave).not.toHaveBeenCalled();
  });
});
