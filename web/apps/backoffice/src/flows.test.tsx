import { randomVKN } from '@kapsora/api-client';
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { App } from './App';
import { createServices } from './api';

const { api, server } = createMockServer({ organizationsPerTenant: 60 });
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

describe('login and guards', () => {
  it('redirects anonymous users to login and returns them after signing in', async () => {
    const { history } = mount('/profile');
    await screen.findByRole('heading', { name: 'Oturum aç' });
    expect(history.location.pathname).toBe('/auth/login');
    await login('admin.a');
    await screen.findByRole('heading', { name: 'Profilim ve Yetkilerim' });
    expect(history.location.pathname).toBe('/profile');
    expect(screen.getByText(/organization\.manage/)).toBeInTheDocument();
    expect(Object.keys(localStorage)).toHaveLength(0);
    expect(Object.keys(sessionStorage)).toHaveLength(0);
  });

  it('shows the problem message on wrong credentials', async () => {
    mount('/auth/login');
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'admin.a');
    await user.type(screen.getByLabelText(/^Parola/), 'short');
    await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Kullanıcı adı veya parola hatalı.');
    expect(alert).toHaveAttribute('data-problem-code', 'INVALID_CREDENTIALS');
  });

  it('sends multi-tenant users to the picker and colours the header per tenant', async () => {
    const { history } = mount('/');
    await login('both.ab');
    await screen.findByRole('heading', { name: 'Çalışma alanı seçin' });
    expect(history.location.pathname).toBe('/auth/tenant');
    const user = userEvent.setup();
    const rowB = screen.getAllByRole('listitem').find((li) => li.textContent?.includes('DEMO_B'))!;
    await user.click(within(rowB).getByRole('button', { name: 'Seç' }));
    await screen.findByRole('heading', { name: 'Ana Sayfa' });
    // The code lives in its own span so a narrow header can drop it; the badge still says both.
    expect(within(screen.getByRole('banner')).getByText(/Demo Sigorta A\.Ş\./)).toHaveTextContent(
      'Demo Sigorta A.Ş. · DEMO_B',
    );
  });
});

describe('organizations', () => {
  it('lists with paging, validates VKN instantly and creates a record', async () => {
    const { history } = mount('/organizations');
    await login('admin.a');
    // The mock world seeds `organizationsPerTenant` rows plus the sponsor, payer and
    // provider relationships the benefit fixtures need, so the second page size comes
    // from the world rather than a hardcoded number. Rows include the header row.
    const tenantId = api.world.tenants[0]!.id;
    const total = api.world.relationships.filter((r) => r.tenantId === tenantId).length;
    const table = await screen.findByTestId('organization-table');
    await waitFor(() => expect(within(table).getAllByRole('row').length).toBe(51));
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Sonraki sayfa' }));
    await screen.findByText('Sayfa 2');
    await waitFor(() =>
      expect(within(screen.getByTestId('organization-table')).getAllByRole('row').length).toBe(
        Math.min(total - 50, 50) + 1,
      ),
    );

    await user.click(screen.getByRole('link', { name: 'Yeni kurum' }));
    await screen.findByRole('heading', { name: 'Yeni kurum' });
    await user.type(screen.getByLabelText(/Ticari unvan/), 'Yeni Klinik Ltd. Şti.');
    await user.type(screen.getByLabelText(/Görünen ad/), 'Yeni Klinik');
    const value = screen.getByLabelText(/^Değer/);
    await user.type(value, '1234567891');
    await user.tab();
    expect(
      await screen.findByText('VKN 10 hane olmalı ve kontrol basamağı tutmalı'),
    ).toBeInTheDocument();
    await user.clear(value);
    await user.type(value, randomVKN());
    await user.tab();
    await waitFor(() =>
      expect(screen.queryByText('VKN 10 hane olmalı ve kontrol basamağı tutmalı')).toBeNull(),
    );
    await user.click(screen.getByRole('button', { name: 'Kaydet' }));
    await screen.findByRole('heading', { name: 'Yeni Klinik' });
    expect(history.location.pathname).toMatch(/^\/organizations\/[0-9a-f-]+$/);
    expect(screen.getByText(/\*{6}/)).toBeInTheDocument();
  });

  it('shows both versions on an ETag conflict instead of overwriting', async () => {
    const { history } = mount('/organizations');
    await login('admin.a');
    await screen.findByTestId('organization-table');
    const tenant = api.world.tenants[0]!;
    const rel = api.world.relationships.find(
      (r) => r.tenantId === tenant.id && r.relationshipStatus === 'ACTIVE',
    )!;
    const user = userEvent.setup();
    await history.push(`/organizations/${rel.id}/edit`);
    await screen.findByRole('heading', { name: 'Kurumu düzenle' });
    const code = await screen.findByLabelText(/Kurum kodu/);
    await waitFor(() => expect(screen.getByText(/Sürüm: \d+/)).toBeInTheDocument());

    // Someone else changes the record while the form is open.
    rel.rowVersion += 1;
    rel.tenantCode = 'BASKASI-DEGISTI';

    await user.clear(code);
    await user.type(code, 'BENIM-KODUM');
    await user.click(screen.getByRole('button', { name: 'Kaydet' }));
    const dialog = await screen.findByRole('dialog', { name: 'Kayıt bu arada değişti' });
    const conflict = within(dialog).getByTestId('conflict-table');
    expect(conflict).toHaveTextContent('BENIM-KODUM');
    expect(conflict).toHaveTextContent('BASKASI-DEGISTI');
    await user.click(within(dialog).getByRole('button', { name: 'Güncel sürümü yükle' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(screen.getByLabelText(/Kurum kodu/)).toHaveValue('BASKASI-DEGISTI');
  });
});
