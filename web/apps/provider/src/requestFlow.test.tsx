import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor, within, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';
import { App } from './App';
import { createServices } from './services';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  vi.restoreAllMocks();
  api.reset();
});
afterAll(() => server.close());

async function mount(path = '/') {
  const services = createServices({ baseUrl: 'http://mock.test' });
  const history = createMemoryHistory({ initialEntries: [path] });
  render(<App services={services} history={history} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'provider.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return { user, services, history };
}

it('requires an explicit plan and waits for its check; a failed check never submits a stale choice', async () => {
  const tenant = api.world.tenants.find((t) => t.code === 'DEMO_A')!;
  const person = api.world.people.find(
    (p) =>
      p.tenantId === tenant.id &&
      p.status === 'ACTIVE' &&
      api.world.enrollments.filter((e) => e.personId === p.id && e.status === 'ACTIVE').length ===
        1,
  )!;
  const enrollment = api.world.enrollments.find(
    (e) => e.personId === person.id && e.status === 'ACTIVE',
  )!;
  const second = { ...enrollment, id: api.world.nextId() };
  api.world.enrollments.push(second);
  const { user, services } = await mount();
  await screen.findByRole('heading', { name: 'Yeni talep' });
  const name = [person.firstName, person.middleName, person.lastName].filter(Boolean).join(' ');
  await user.type(screen.getByLabelText('Ada göre ara'), name.slice(0, 3));
  await user.click(await screen.findByRole('button', { name }));
  const service = screen.getByRole('combobox', { name: /^Hizmet/ });
  await waitFor(() => expect(within(service).getAllByRole('option').length).toBeGreaterThan(1));
  await user.selectOptions(
    service,
    (within(service).getAllByRole('option')[1] as HTMLOptionElement).value,
  );
  const choice = await screen.findByRole('combobox', { name: /^Plan kaydı/ });
  expect(within(choice).getAllByRole('option')).toHaveLength(3);
  expect(screen.queryByRole('button', { name: 'Gönder' })).toBeNull();
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  const actual = services.ops.eligibility.check;
  const check = vi
    .spyOn(services.ops.eligibility, 'check')
    .mockImplementationOnce(async (...args) => {
      await pending;
      return actual(...args);
    });
  await user.selectOptions(choice, second.id);
  expect(screen.queryByRole('button', { name: 'Gönder' })).toBeNull();
  await act(async () => release());
  await screen.findByRole('button', { name: 'Gönder' });
  check.mockRejectedValueOnce(new Error('connection lost'));
  await user.selectOptions(choice, enrollment.id);
  await screen.findByRole('alert');
  expect(screen.queryByRole('button', { name: 'Gönder' })).toBeNull();
  await user.selectOptions(choice, second.id);
  const date = screen.getByLabelText(/^Hizmet tarihi/);
  const originalDate = (date as HTMLInputElement).value;
  await user.clear(date);
  await user.type(date, originalDate);
  const resetChoice = await screen.findByRole('combobox', { name: /^Plan/ });
  expect(resetChoice).toHaveValue('');
  expect(screen.queryByRole('button', { name: /^G.*nder$/ })).toBeNull();
  await user.selectOptions(resetChoice, second.id);
  const create = vi.spyOn(services.ops.requests, 'create');
  await user.click(await screen.findByRole('button', { name: 'Gönder' }));
  await waitFor(() => expect(create).toHaveBeenCalledOnce());
  expect(create.mock.calls[0]?.[1].enrollmentId).toBe(second.id);
}, 20_000);

function returnedRequest() {
  const account = api.world.accounts.find((a) => a.username === 'provider.a')!;
  const provider = account.memberships[0]!.scopes!.find((s) => s.type === 'ORGANIZATION')!.id;
  const request = api.world.serviceRequests.find(
    (r) => r.providerOrganizationId === provider && r.status === 'DRAFT' && r.currentVersionNo > 1,
  );
  expect(request, 'fixture must include a returned request').toBeDefined();
  return request!;
}

it('corrects the existing draft, preserves the decided version and retries the same submit command', async () => {
  const request = returnedRequest();
  const history = JSON.stringify(
    api.world.serviceRequestVersions.filter(
      (v) => v.serviceRequestId === request.id && v.versionNo < request.currentVersionNo,
    ),
  );
  const count = api.world.serviceRequests.length;
  const { user, services } = await mount(`/requests/${request.id}`);
  const form = await screen.findByTestId('request-correction');
  const put = vi.spyOn(services.ops.requests, 'putItems');
  const submit = vi
    .spyOn(services.ops.requests, 'submit')
    .mockRejectedValueOnce(new Error('connection lost'));
  const quantity = within(form).getAllByLabelText(/^Miktar/)[0]!;
  await user.clear(quantity);
  await user.type(quantity, '2');
  expect(within(form).queryByRole('button', { name: 'Talebi gönder' })).toBeNull();
  await user.click(within(form).getByRole('button', { name: 'Değişiklikleri kaydet' }));
  await user.click(await within(form).findByRole('button', { name: 'Talebi gönder' }));
  await screen.findByRole('alert');
  expect(quantity).toBeDisabled();
  await user.click(within(form).getByRole('button', { name: 'Talebi gönder' }));
  await waitFor(() => expect(screen.queryByTestId('request-correction')).toBeNull());
  expect(put).toHaveBeenCalledOnce();
  expect(submit).toHaveBeenCalledTimes(2);
  expect(submit.mock.calls[1]).toEqual(submit.mock.calls[0]);
  expect(api.world.serviceRequests).toHaveLength(count);
  expect(
    JSON.stringify(
      api.world.serviceRequestVersions.filter(
        (v) => v.serviceRequestId === request.id && v.versionNo < request.currentVersionNo,
      ),
    ),
  ).toBe(history);
}, 20_000);

it('refuses stale edits and reloads only on request without overwriting the newer record', async () => {
  const request = returnedRequest();
  const { user } = await mount(`/requests/${request.id}`);
  const form = await screen.findByTestId('request-correction');
  const quantity = within(form).getAllByLabelText(/^Miktar/)[0]!;
  const original = (quantity as HTMLInputElement).value;
  await user.clear(quantity);
  await user.type(quantity, '3');
  request.rowVersion += 1;
  await user.click(within(form).getByRole('button', { name: 'Değişiklikleri kaydet' }));
  await screen.findByRole('alert');
  expect(quantity).toHaveValue('3');
  expect(quantity).toBeDisabled();
  expect(screen.queryByRole('button', { name: 'Talebi gönder' })).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Güncel kaydı yükle' }));
  await waitFor(() =>
    expect(
      within(screen.getByTestId('request-correction')).getAllByLabelText(/^Miktar/)[0],
    ).toHaveValue(original),
  );
}, 20_000);

it('explains invalid correction fields and accepts comma decimals without floating point conversion', async () => {
  const request = returnedRequest();
  const { user, services } = await mount(`/requests/${request.id}`);
  const form = await screen.findByTestId('request-correction');
  const quantity = within(form).getAllByLabelText(/^Miktar/)[0]!;
  await user.clear(quantity);
  await user.type(quantity, '0');
  expect(quantity).toHaveAttribute('aria-invalid', 'true');
  expect(quantity).toHaveAccessibleDescription(/Sıfırdan büyük/);
  const save = within(form).getByRole('button', { name: 'Değişiklikleri kaydet' });
  expect(save).toBeDisabled();
  await user.clear(quantity);
  await user.type(quantity, '1,5');
  expect(quantity).toHaveValue('1.5');
  const amount = within(form).getAllByLabelText('Tutar')[0]!;
  await user.type(amount, 'xyz');
  expect(amount).toHaveAccessibleDescription(/Sıfır veya daha büyük/);
  expect(save).toBeDisabled();
  await user.clear(amount);
  const currency = within(form).getAllByLabelText('Para birimi')[0]!;
  await user.clear(currency);
  expect(currency).toHaveAccessibleDescription(/Üç harfli/);
  await user.type(currency, 'TRY');
  const date = within(form).getByLabelText(/^Hizmet tarihi/);
  await user.clear(date);
  expect(date).toHaveAccessibleDescription('Geçerli bir hizmet tarihi seçiniz.');
  await user.type(date, request.serviceDate);
  const put = vi.spyOn(services.ops.requests, 'putItems');
  await user.click(save);
  await waitFor(() => expect(put).toHaveBeenCalledOnce());
  expect(put.mock.calls[0]?.[3].items[0]?.requestedQuantity).toBe('1.5');
  expect(put.mock.calls[0]?.[3].items[0]).not.toHaveProperty('requestedAmount');
}, 20_000);
