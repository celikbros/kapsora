import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';

import { App } from '../App';
import { createServices } from '../services';

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

function ownRequests() {
  const account = api.world.accounts.find((a) => a.username === 'provider.a')!;
  const provider = account.memberships[0]!.scopes!.find((s) => s.type === 'ORGANIZATION')!.id;
  return api.world.serviceRequests.filter((r) => r.providerOrganizationId === provider);
}

async function mount(path: string, services = createServices({ baseUrl: 'http://mock.test' })) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(<App services={services} history={history} />);
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'provider.a');
  await user.type(screen.getByLabelText(/^Parola/), 'demo parola 2026 kapsora');
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return { history, services, user };
}

it('keeps a failed linked request in linked mode until retry and creates with its source fields', async () => {
  const request = ownRequests()[0]!;
  const services = createServices({ baseUrl: 'http://mock.test' });
  const get = services.ops.requests.get.bind(services.ops.requests);
  let failing = true;
  vi.spyOn(services.ops.requests, 'get').mockImplementation((...args) =>
    failing ? Promise.reject(new Error('read failed')) : get(...args),
  );
  const create = vi.spyOn(services.ops.health, 'createCase');
  const { history, user } = await mount(`/cases/new?requestId=${request.id}`, services);
  await screen.findByRole('alert');
  expect(screen.queryByLabelText('Ada göre ara')).toBeNull();
  expect(screen.queryByRole('button', { name: 'Vakayı aç' })).toBeNull();
  expect(create).not.toHaveBeenCalled();

  failing = false;
  await user.click(screen.getByRole('button', { name: 'Yeniden dene' }));
  await waitFor(() => expect(document.querySelector('input[name="member"]')).not.toBeNull());
  await user.click(screen.getByRole('button', { name: 'Vakayı aç' }));
  await waitFor(() => expect(create).toHaveBeenCalledOnce());
  expect(create.mock.calls[0]?.[1]).toMatchObject({
    personId: request.personId,
    enrollmentId: request.enrollmentId,
    serviceRequestId: request.id,
    programId: request.programId,
  });
  await waitFor(() => expect(history.location.pathname).toMatch(/^\/cases\/[^/]+$/));
  expect(screen.getByText('Vaka açıldı.')).toBeInTheDocument();
}, 20_000);

it('blocks creation from cached linked data after a failed refresh', async () => {
  const request = ownRequests()[0]!;
  const { services } = await mount(`/cases/new?requestId=${request.id}`);
  await waitFor(() => expect(document.querySelector('input[name="member"]')).not.toBeNull());
  const create = vi.spyOn(services.ops.health, 'createCase');
  vi.spyOn(services.ops.requests, 'get').mockRejectedValue(new Error('refresh failed'));
  const tenantId = services.store.getState().activeTenant!.tenant.id;
  await act(async () => {
    await services.queryClient.refetchQueries({
      queryKey: ['provider', tenantId, 'request', request.id],
    });
  });
  await screen.findByRole('alert');
  expect(screen.queryByRole('button', { name: 'Vakayı aç' })).toBeNull();
  expect(screen.queryByLabelText('Ada göre ara')).toBeNull();
  expect(create).not.toHaveBeenCalled();
}, 20_000);

it('resets a linked draft when the URL changes to another request', async () => {
  const [first, second] = ownRequests();
  expect(second).toBeDefined();
  const { history, user } = await mount(`/cases/new?requestId=${first!.id}`);
  await waitFor(() => expect(document.querySelector('input[name="member"]')).not.toBeNull());
  await user.selectOptions(screen.getByLabelText(/^Vaka türü/), 'INPATIENT');
  await user.clear(screen.getByLabelText(/^Açılış tarihi/));
  await act(async () => {
    await history.push(`/cases/new?requestId=${second!.id}`);
  });
  await waitFor(() => expect(document.querySelector('input[name="member"]')).not.toBeNull());
  expect(screen.getByLabelText(/^Vaka türü/)).toHaveValue('OUTPATIENT');
  expect(screen.getByLabelText(/^Açılış tarihi/)).not.toHaveValue('');
}, 20_000);

it('does not show success or navigate after leaving a source during creation', async () => {
  const request = ownRequests()[0]!;
  const services = createServices({ baseUrl: 'http://mock.test' });
  let release!: () => void;
  const result = new Promise<Awaited<ReturnType<typeof services.ops.health.createCase>>>(
    (resolve) => {
      release = () =>
        resolve({ data: { id: 'late-case' } } as Awaited<
          ReturnType<typeof services.ops.health.createCase>
        >);
    },
  );
  const create = vi.spyOn(services.ops.health, 'createCase').mockReturnValue(result);
  const { history, user } = await mount(`/cases/new?requestId=${request.id}`, services);
  await waitFor(() => expect(document.querySelector('input[name="member"]')).not.toBeNull());
  await user.click(screen.getByRole('button', { name: 'Vakayı aç' }));
  await waitFor(() => expect(create).toHaveBeenCalledOnce());
  await act(async () => {
    await history.push('/cases');
  });
  await act(async () => release());
  await waitFor(() => {
    const mutation = services.queryClient
      .getMutationCache()
      .getAll()
      .find((entry) => {
        const variables = entry.state.variables;
        return (
          typeof variables === 'object' &&
          variables !== null &&
          'serviceRequestId' in variables &&
          variables.serviceRequestId === request.id
        );
      });
    expect(mutation?.state.status).toBe('success');
  });
  expect(history.location.pathname).toBe('/cases');
  expect(screen.queryByText('Vaka açıldı.')).toBeNull();
}, 20_000);

it('requires a settled successful standalone eligibility read even when cached data exists', async () => {
  const tenant = api.world.tenants.find((t) => t.code === 'DEMO_A')!;
  const person = api.world.people.find(
    (p) =>
      p.tenantId === tenant.id &&
      p.status === 'ACTIVE' &&
      api.world.enrollments.filter((e) => e.personId === p.id && e.status === 'ACTIVE').length ===
        1,
  )!;
  const name = [person.firstName, person.middleName, person.lastName].filter(Boolean).join(' ');
  const enrollment = api.world.enrollments.find(
    (entry) => entry.personId === person.id && entry.status === 'ACTIVE',
  )!;
  const { user, services } = await mount('/cases/new');
  await user.type(screen.getByLabelText('Ada göre ara'), name.slice(0, 3));
  await user.click(await screen.findByRole('button', { name }));
  const service = document.querySelector('select[name="serviceDefinitionId"]') as HTMLSelectElement;
  expect(service).not.toBeNull();
  await waitFor(() => expect(service.querySelectorAll('option').length).toBeGreaterThan(1));
  await user.selectOptions(
    service,
    (service.querySelectorAll('option')[1] as HTMLOptionElement).value,
  );
  await screen.findByRole('button', { name: 'Vakayı aç' });
  const check = services.ops.eligibility.check.bind(services.ops.eligibility);
  let failing = true;
  vi.spyOn(services.ops.eligibility, 'check').mockImplementation((...args) =>
    failing ? Promise.reject(new Error('refresh failed')) : check(...args),
  );
  const tenantId = services.store.getState().activeTenant!.tenant.id;
  await act(async () => {
    await services.queryClient.refetchQueries({ queryKey: ['provider', tenantId, 'eligibility'] });
  });
  await screen.findByRole('alert');
  expect(screen.queryByRole('button', { name: 'Vakayı aç' })).toBeNull();
  failing = false;
  await user.click(screen.getByRole('button', { name: 'Yeniden dene' }));
  await screen.findByRole('button', { name: 'Vakayı aç' });
  const create = vi.spyOn(services.ops.health, 'createCase');
  await user.click(screen.getByRole('button', { name: 'Vakayı aç' }));
  await waitFor(() => expect(create).toHaveBeenCalledOnce());
  expect(create.mock.calls[0]?.[1]).toMatchObject({
    personId: person.id,
    enrollmentId: enrollment.id,
  });
  expect(create.mock.calls[0]?.[1]).not.toHaveProperty('serviceRequestId');
}, 20_000);
