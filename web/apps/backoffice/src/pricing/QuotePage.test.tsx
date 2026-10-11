// @vitest-environment jsdom
import { createMockServer } from '@kapsora/api-client/mocks/node';
import { initI18n } from '@kapsora/i18n';
import { createMemoryHistory } from '@tanstack/react-router';
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { App } from '../App';
import { createServices } from '../api';

const { api, server } = createMockServer();
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => {
  cleanup();
  api.reset();
  vi.restoreAllMocks();
});
afterAll(() => server.close());

function mount() {
  const services = createServices({ baseUrl: 'http://mock.test' });
  render(
    <App services={services} history={createMemoryHistory({ initialEntries: ['/pricing'] })} />,
  );
  return services;
}

async function login(username: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Kullanıcı adı/), username);
  await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
  await user.click(screen.getByRole('button', { name: 'Giriş yap' }));
  return user;
}

describe('price query access and inputs', () => {
  it('denies a caller missing member.read before any pricing or member read', async () => {
    const manager = api.world.accounts.find((item) => item.username === 'admin.a')!;
    manager.memberships[0]!.permissions = ['pricing.quote'];
    const services = mount();
    const providers = vi.spyOn(services.ops.pricing, 'listProviderOptions');
    const servicesRead = vi.spyOn(services.ops.pricing, 'listServiceOptions');
    const people = vi.spyOn(services.ops.people, 'list');
    await login('admin.a');
    expect(await screen.findByText('Bu işlem için yetkiniz yok.')).toBeTruthy();
    expect(providers).not.toHaveBeenCalled();
    expect(servicesRead).not.toHaveBeenCalled();
    expect(people).not.toHaveBeenCalled();
  });

  it('denies a member reader missing pricing.quote before any pricing or member read', async () => {
    const services = mount();
    const providers = vi.spyOn(services.ops.pricing, 'listProviderOptions');
    const servicesRead = vi.spyOn(services.ops.pricing, 'listServiceOptions');
    const people = vi.spyOn(services.ops.people, 'list');
    await login('sponsor.hr');
    expect(await screen.findByText('Bu işlem için yetkiniz yok.')).toBeTruthy();
    expect(providers).not.toHaveBeenCalled();
    expect(servicesRead).not.toHaveBeenCalled();
    expect(people).not.toHaveBeenCalled();
  });

  it('lets finance load pricing options without provider or catalog directory reads', async () => {
    const services = mount();
    const providerDirectory = vi.spyOn(services.ops.providers, 'list');
    const catalog = vi.spyOn(services.ops.catalog, 'listDefinitions');
    const providerOptions = vi
      .spyOn(services.ops.pricing, 'listProviderOptions')
      .mockResolvedValue({
        items: [{ providerProfileId: 'provider-1', organizationName: 'Hospital A' }],
        nextCursor: null,
      });
    const serviceOptions = vi.spyOn(services.ops.pricing, 'listServiceOptions').mockResolvedValue({
      items: [{ serviceDefinitionId: 'service-1', code: 'PHYSIO_SESSION', name: 'Physiotherapy' }],
      nextCursor: null,
    });
    const user = await login('financial.reviewer');
    expect(await screen.findByRole('option', { name: 'Hospital A' })).toBeTruthy();
    expect(screen.getByRole('option', { name: /PHYSIO_SESSION/ })).toBeTruthy();
    expect(providerOptions).toHaveBeenCalled();
    expect(serviceOptions).toHaveBeenCalled();
    expect(providerDirectory).not.toHaveBeenCalled();
    expect(catalog).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Hesapla' }));
    expect((await screen.findByRole('alert')).textContent).toContain('Hak sahibi');
    expect(api.world.priceQuotes).toHaveLength(0);
  });

  it('removes loaded choices and results immediately when the active quote grant is revoked', async () => {
    const services = mount();
    vi.spyOn(services.ops.pricing, 'listProviderOptions').mockResolvedValue({
      items: [{ providerProfileId: 'provider-1', organizationName: 'Hospital A' }],
      nextCursor: null,
    });
    vi.spyOn(services.ops.pricing, 'listServiceOptions').mockResolvedValue({
      items: [],
      nextCursor: null,
    });
    await login('financial.reviewer');
    expect(await screen.findByRole('option', { name: 'Hospital A' })).toBeTruthy();
    services.store.setState((state) => ({
      activeTenant: {
        ...state.activeTenant!,
        permissions: state.activeTenant!.permissions.filter(
          (permission) => permission !== 'pricing.quote',
        ),
      },
    }));
    await waitFor(() => expect(screen.getByText('Bu işlem için yetkiniz yok.')).toBeTruthy());
    expect(screen.queryByRole('option', { name: 'Hospital A' })).toBeNull();
  });

  it('loads one provider page at a time and retries a failed continuation without discarding page one', async () => {
    const services = mount();
    services.queryClient.setDefaultOptions({ queries: { retry: false } });
    let pageTwoAttempts = 0;
    const options = vi
      .spyOn(services.ops.pricing, 'listProviderOptions')
      .mockImplementation(async (_tenant, query) => {
        if (query?.cursor) {
          pageTwoAttempts += 1;
          if (pageTwoAttempts === 1) throw new Error('temporary failure');
          return {
            items: [{ providerProfileId: 'provider-2', organizationName: 'Hotel B' }],
            nextCursor: null,
          };
        }
        return {
          items: [{ providerProfileId: 'provider-1', organizationName: 'Hospital A' }],
          nextCursor: 'page-2',
        };
      });
    vi.spyOn(services.ops.pricing, 'listServiceOptions').mockResolvedValue({
      items: [],
      nextCursor: null,
    });
    const user = await login('financial.reviewer');
    expect(await screen.findByRole('option', { name: 'Hospital A' })).toBeTruthy();
    await user.click(screen.getByTestId('price-providers-more'));
    expect(await screen.findByTestId('price-providers-retry')).toBeTruthy();
    expect(screen.getByRole('option', { name: 'Hospital A' })).toBeTruthy();
    await user.click(screen.getByTestId('price-providers-retry'));
    expect(await screen.findByRole('option', { name: 'Hotel B' })).toBeTruthy();
    expect(pageTwoAttempts).toBe(2);
    expect(options.mock.calls.filter(([, query]) => query?.cursor === 'page-2')).toHaveLength(2);
  });

  it('submits selected IDs, clears a stale result on edit, and leaves contract reference unlinked without contract.read', async () => {
    const services = mount();
    vi.spyOn(services.ops.pricing, 'listProviderOptions').mockResolvedValue({
      items: [{ providerProfileId: 'provider-1', organizationName: 'Hospital A' }],
      nextCursor: null,
    });
    vi.spyOn(services.ops.pricing, 'listServiceOptions').mockResolvedValue({
      items: [{ serviceDefinitionId: 'service-1', code: 'PHYSIO_SESSION', name: 'Physiotherapy' }],
      nextCursor: null,
    });
    const answer: Awaited<ReturnType<typeof services.ops.pricing.createQuote>> = {
      id: 'quote-1',
      outcome: 'QUOTED',
      expired: false,
      expiresAt: new Date().toISOString(),
      currencyCode: 'TRY',
      contractAmount: '400.000000',
      coveredAmount: '400.000000',
      payerAmount: '400.000000',
      memberAmount: '0.000000',
      requestedAmount: '400.000000',
      disclaimer: 'This quote grants nothing.',
      personId: 'person-1',
      providerProfileId: 'provider-1',
      quotedAt: new Date().toISOString(),
      serviceDate: '2026-10-02',
      items: [],
      contractVersionId: 'contract-private',
      planVersionId: null,
      ruleSetVersionIds: [],
      eligibilityEvaluationId: null,
    };
    const create = vi.spyOn(services.ops.pricing, 'createQuote').mockResolvedValue(answer);
    const user = await login('financial.reviewer');
    const provider = (await screen.findByTestId('price-provider-select')) as HTMLSelectElement;
    await waitFor(() => expect(provider.options.length).toBeGreaterThan(1));
    const person = screen.getByRole('combobox', { name: /Hak sahibi/ }) as HTMLSelectElement;
    await waitFor(() => expect(person.options.length).toBeGreaterThan(1));
    await user.selectOptions(person, person.options[1]!.value);
    await user.selectOptions(provider, 'provider-1');
    const service = screen.getByRole('combobox', { name: /^Hizmet 1$/ }) as HTMLSelectElement;
    await user.selectOptions(service, 'service-1');
    await user.click(screen.getByRole('button', { name: 'Hesapla' }));
    expect(await screen.findByTestId('quote-table')).toBeTruthy();
    expect(create).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({
        personId: person.value,
        providerProfileId: 'provider-1',
        items: [{ serviceDefinitionId: 'service-1', quantity: '1' }],
      }),
      expect.any(String),
    );
    expect(screen.getByText('contract-private').closest('a')).toBeNull();
    await user.clear(screen.getByLabelText('Miktar 1'));
    await user.type(screen.getByLabelText('Miktar 1'), '0');
    expect(screen.queryByTestId('quote-table')).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Hesapla' }));
    expect((await screen.findByRole('alert')).textContent).toContain('miktar');
    expect(create).toHaveBeenCalledTimes(1);
    await user.clear(screen.getByLabelText('Miktar 1'));
    await user.type(screen.getByLabelText('Miktar 1'), '-1');
    await user.click(screen.getByRole('button', { name: 'Hesapla' }));
    expect(create).toHaveBeenCalledTimes(1);
    await user.clear(screen.getByLabelText('Miktar 1'));
    await user.type(screen.getByLabelText('Miktar 1'), '1.1234567');
    await user.click(screen.getByRole('button', { name: 'Hesapla' }));
    expect(create).toHaveBeenCalledTimes(1);
    let resolveLate!: (value: Awaited<ReturnType<typeof services.ops.pricing.createQuote>>) => void;
    const lateResponse = new Promise<Awaited<ReturnType<typeof services.ops.pricing.createQuote>>>(
      (resolve) => {
        resolveLate = resolve;
      },
    );
    create.mockImplementationOnce(() => lateResponse);
    await user.clear(screen.getByLabelText('Miktar 1'));
    await user.type(screen.getByLabelText('Miktar 1'), '2');
    await user.click(screen.getByRole('button', { name: 'Hesapla' }));
    expect(create).toHaveBeenCalledTimes(2);
    await user.clear(screen.getByLabelText('Miktar 1'));
    await user.type(screen.getByLabelText('Miktar 1'), '3');
    await act(async () => resolveLate({ ...answer, id: 'late-quote' }));
    expect(screen.queryByTestId('quote-table')).toBeNull();
    create.mockResolvedValueOnce({
      ...answer,
      outcome: 'REVIEW_REQUIRED',
      payerAmount: '0.000000',
      memberAmount: '0.000000',
      disclaimer: 'Stored review disclaimer',
      items: [
        {
          lineNo: 1,
          outcome: 'REVIEW_REQUIRED',
          quantity: '3',
          serviceDefinitionId: 'service-1',
          contractAmount: '400.000000',
          coveredAmount: '400.000000',
          requestedAmount: '400.000000',
          payerAmount: '0.000000',
          memberAmount: '0.000000',
          explanations: [],
        },
      ],
    });
    await user.click(screen.getByRole('button', { name: 'Hesapla' }));
    const reviewTable = await screen.findByTestId('quote-table');
    expect(within(reviewTable).queryByText('0.000000')).toBeNull();
    expect(reviewTable.textContent?.match(/—/g)).toHaveLength(4);
    expect(screen.getByText('Stored review disclaimer')).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Satır ekle' }));
    expect(screen.getByLabelText('Hizmet 1')).toBeTruthy();
    expect(screen.getByLabelText('Hizmet 2')).toBeTruthy();
    expect(screen.getByLabelText('Miktar 1')).toBeTruthy();
    expect(screen.getByLabelText('Miktar 2')).toBeTruthy();
  });

  it('isolates a late provider response from a changed actor context', async () => {
    const services = mount();
    let resolveOld!: (value: {
      items: { providerProfileId: string; organizationName: string }[];
      nextCursor: null;
    }) => void;
    const oldResponse = new Promise<{
      items: { providerProfileId: string; organizationName: string }[];
      nextCursor: null;
    }>((resolve) => {
      resolveOld = resolve;
    });
    const providers = vi
      .spyOn(services.ops.pricing, 'listProviderOptions')
      .mockImplementationOnce(() => oldResponse)
      .mockResolvedValue({
        items: [{ providerProfileId: 'new-provider', organizationName: 'New Actor Hospital' }],
        nextCursor: null,
      });
    vi.spyOn(services.ops.pricing, 'listServiceOptions').mockResolvedValue({
      items: [],
      nextCursor: null,
    });
    await login('financial.reviewer');
    await waitFor(() => expect(providers).toHaveBeenCalledTimes(1));
    services.store.setState((state) => ({
      session: { ...state.session!, actorId: 'other-actor' },
    }));
    expect(await screen.findByRole('option', { name: 'New Actor Hospital' })).toBeTruthy();
    resolveOld({
      items: [{ providerProfileId: 'old-provider', organizationName: 'Old Actor Hospital' }],
      nextCursor: null,
    });
    await waitFor(() =>
      expect(screen.queryByRole('option', { name: 'Old Actor Hospital' })).toBeNull(),
    );
  });
});
