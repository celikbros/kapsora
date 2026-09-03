import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { App } from '../App';
import { createServices } from '../api';

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

async function login(username = 'admin.a') {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

/** The principal of the seeded family in DEMO_A. */
function principal() {
  const tenantId = api.world.tenants[0]!.id;
  const membership = api.world.memberships.find(
    (m) => m.tenantId === tenantId && m.principalMembershipId === null,
  );
  if (!membership) throw new Error('fixture: no principal membership');
  const person = api.world.people.find((p) => p.id === membership.personId);
  if (!person) throw new Error('fixture: principal has no person');
  return { person, membership };
}

describe('member list', () => {
  it('lists members, filters by name and opens one', async () => {
    const { history } = mount('/people');
    const user = await login();
    const table = await screen.findByTestId('person-table');
    await waitFor(() => expect(within(table).getAllByRole('row').length).toBeGreaterThan(1));

    const { person } = principal();
    await user.clear(screen.getByLabelText(/Ad veya soyad ara/));
    await user.type(screen.getByLabelText(/Ad veya soyad ara/), person.lastName);
    await user.click(screen.getByRole('button', { name: 'Ara' }));
    await waitFor(() =>
      expect(String(history.location.search)).toContain(encodeURIComponent(person.lastName)),
    );

    // Every filtered row shares the surname, so click the one with this person's full name.
    const fullName = `${person.firstName} ${person.lastName}`;
    await user.click(await screen.findByRole('link', { name: fullName }));
    await screen.findByRole('tab', { name: 'Kimlik' });
    expect(history.location.pathname).toBe(`/people/${person.id}`);
  });

  it('asks for the password before searching by identifier', async () => {
    mount('/people');
    const user = await login();
    await screen.findByTestId('person-table');
    const { person } = principal();
    const tckn = person.identifiers.find((i) => i.type === 'TCKN')?.value;
    if (!tckn) throw new Error('fixture: principal has no TCKN');

    await user.click(screen.getByRole('button', { name: 'Kimlik ile ara' }));
    const dialog = await screen.findByRole('dialog', { name: 'Kimlik numarasıyla ara' });
    await user.type(within(dialog).getByLabelText(/Numara/), tckn);
    await user.click(within(dialog).getByRole('button', { name: 'Ara' }));

    // The server refuses without a step-up, so the password dialog takes over.
    const stepUp = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
    await user.type(within(stepUp).getByLabelText(/^Parola/), PASSWORD);
    await user.click(within(stepUp).getByRole('button', { name: 'Doğrula' }));

    // The retried search lands on the person.
    await screen.findByRole('tab', { name: 'Kimlik' });
    expect(screen.getByRole('heading', { level: 1 }).textContent).toContain(person.lastName);
  });

  it('shows an instant error for a bad identity number', async () => {
    mount('/people/new');
    const user = await login();
    await screen.findByRole('heading', { name: 'Yeni hak sahibi' });
    await user.type(screen.getByLabelText(/^Ad/), 'Ayşe');
    await user.type(screen.getByLabelText(/^Soyad/), 'Yılmaz');
    await user.type(screen.getByLabelText(/^Numara/), '12345678902');
    await user.click(screen.getByRole('button', { name: 'Kaydet' }));
    expect(
      await screen.findByText('TCKN 11 hane olmalı ve kontrol basamakları tutmalı'),
    ).toBeInTheDocument();
  });
});

describe('member detail', () => {
  it('shows masked identifiers and never the raw number', async () => {
    const { person } = principal();
    mount(`/people/${person.id}`);
    await login();
    await screen.findByRole('tab', { name: 'Kimlik' });
    const tckn = person.identifiers.find((i) => i.type === 'TCKN')?.value;
    if (!tckn) throw new Error('fixture: principal has no TCKN');
    expect(document.body.textContent).not.toContain(tckn);
    expect(screen.getAllByText(/\*{3,}/).length).toBeGreaterThan(0);
  });

  it('lists the family, the memberships and the entitlement balances', async () => {
    const { person } = principal();
    mount(`/people/${person.id}`);
    const user = await login();

    await user.click(await screen.findByRole('tab', { name: 'Aile' }));
    const family = await screen.findByTestId('relationship-table');
    expect(within(family).getAllByRole('row').length).toBeGreaterThan(1);

    await user.click(screen.getByRole('tab', { name: 'Üyelikler' }));
    const memberships = await screen.findByTestId('membership-table');
    expect(within(memberships).getAllByRole('row').length).toBeGreaterThan(1);

    await user.click(screen.getByRole('tab', { name: 'Haklar' }));
    const entitlements = await screen.findByTestId('entitlement-table');
    // Balances are decimal text, so a session count reads "12" and money keeps its code.
    expect(within(entitlements).getAllByRole('row').length).toBeGreaterThan(1);
    expect(entitlements.textContent).toMatch(/TRY/);
  });

  it('runs an eligibility check and explains the answer', async () => {
    const { person } = principal();
    mount(`/people/${person.id}`);
    const user = await login();
    await user.click(await screen.findByRole('tab', { name: 'Uygunluk' }));
    await screen.findByRole('heading', { name: 'Uygunluk sorgusu' });

    // Pick the first entitlement code the person can reach, then ask.
    const codeSelect = await screen.findByLabelText(/Hak kodu/);
    await waitFor(() =>
      expect(within(codeSelect).getAllByRole('option').length).toBeGreaterThan(1),
    );
    const option = within(codeSelect).getAllByRole('option')[1]!;
    await user.selectOptions(codeSelect, option);
    await user.click(screen.getByRole('button', { name: 'Sorgula' }));

    const items = await screen.findByTestId('eligibility-items');
    expect(within(items).getAllByRole('row').length).toBe(2);
    expect(screen.getByText(/Değerlendirme no/)).toBeInTheDocument();
  });

  it('keeps nothing in browser storage', async () => {
    const { person } = principal();
    mount(`/people/${person.id}`);
    await login();
    await screen.findByRole('tab', { name: 'Kimlik' });
    expect(Object.keys(localStorage)).toHaveLength(0);
    expect(Object.keys(sessionStorage)).toHaveLength(0);
  });
});
