// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useOrganizationName, usePersonName } from '../claims/names';
import { useCategoryOptions, useDefinitionOptions } from '../catalogOptions';

const mocks = vi.hoisted(() => ({
  actorId: 'actor-a',
  tenantId: 'tenant-a',
  permissions: [] as string[],
  personGet: vi.fn(),
  organizationGet: vi.fn(),
  listDefinitions: vi.fn(),
  listCategories: vi.fn(),
}));

vi.mock('@kapsora/auth', () => ({
  useTenantId: () => mocks.tenantId,
  useSession: (selector: (state: unknown) => unknown) =>
    selector({ session: { actorId: mocks.actorId } }),
  usePermission: (permission: string) => mocks.permissions.includes(permission),
}));
vi.mock('../api', () => ({
  useOps: () => ({
    people: { get: mocks.personGet },
    organizations: { get: mocks.organizationGet },
    catalog: { listDefinitions: mocks.listDefinitions, listCategories: mocks.listCategories },
  }),
}));

function LookupLabels() {
  const person = usePersonName('person-1');
  const organization = useOrganizationName('organization-1');
  const definitions = useDefinitionOptions();
  const categories = useCategoryOptions();
  return (
    <>
      <output data-testid="person">{person ?? (person === null ? 'none' : 'loading')}</output>
      <output data-testid="organization">
        {organization ?? (organization === null ? 'none' : 'loading')}
      </output>
      <output data-testid="definitions">
        {definitions.data?.map((option) => option.label).join(',') ?? 'loading'}
      </output>
      <output data-testid="definitions-pending">{String(definitions.isPending)}</output>
      <output data-testid="categories">
        {categories.data?.map((option) => option.label).join(',')}
      </output>
    </>
  );
}

function mount(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  const view = render(
    <QueryClientProvider client={client}>
      <LookupLabels />
    </QueryClientProvider>,
  );
  return { ...view, client };
}

beforeEach(() => {
  mocks.actorId = 'actor-a';
  mocks.tenantId = 'tenant-a';
  mocks.permissions = [];
  mocks.personGet.mockReset().mockResolvedValue({ data: { displayName: 'A Person' } });
  mocks.organizationGet.mockReset().mockResolvedValue({ data: { displayName: 'A Clinic' } });
  mocks.listDefinitions.mockReset().mockResolvedValue({
    items: [{ id: 'definition-a', code: 'CONSULT', name: 'Consultation' }],
  });
  mocks.listCategories.mockReset().mockResolvedValue({
    items: [{ id: 'category-a', code: 'OUTPATIENT', name: 'Outpatient' }],
  });
});
afterEach(cleanup);

describe('billing lookup permissions', () => {
  it('skips forbidden reads and hides cached labels after permission loss', async () => {
    mocks.permissions = ['member.read', 'organization.read', 'catalog.read'];
    const { rerender, client } = mount();
    await waitFor(() => expect(screen.getByTestId('definitions').textContent).toContain('CONSULT'));
    expect(screen.getByTestId('categories')).toHaveTextContent('OUTPATIENT');
    expect(await screen.findByTestId('person')).toHaveTextContent('A Person');
    expect(await screen.findByTestId('organization')).toHaveTextContent('A Clinic');

    mocks.permissions = [];
    rerender(
      <QueryClientProvider client={client}>
        <LookupLabels />
      </QueryClientProvider>,
    );
    expect(screen.getByTestId('person')).toHaveTextContent('none');
    expect(screen.getByTestId('organization')).toHaveTextContent('none');
    expect(screen.getByTestId('definitions')).toHaveTextContent('');
    expect(screen.getByTestId('definitions-pending')).toHaveTextContent('false');
    expect(screen.getByTestId('categories')).toHaveTextContent('');
    expect(mocks.personGet).toHaveBeenCalledTimes(1);
    expect(mocks.organizationGet).toHaveBeenCalledTimes(1);
    expect(mocks.listDefinitions).toHaveBeenCalledTimes(1);
    expect(mocks.listCategories).toHaveBeenCalledTimes(1);
  });

  it('fetches names and catalog options after their read permissions are granted', async () => {
    const { rerender, client } = mount();
    expect(mocks.personGet).not.toHaveBeenCalled();
    expect(mocks.organizationGet).not.toHaveBeenCalled();
    expect(mocks.listDefinitions).not.toHaveBeenCalled();
    expect(mocks.listCategories).not.toHaveBeenCalled();

    mocks.permissions = ['member.read', 'organization.read', 'catalog.read'];
    rerender(
      <QueryClientProvider client={client}>
        <LookupLabels />
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByTestId('person')).toHaveTextContent('A Person'));
    expect(screen.getByTestId('organization')).toHaveTextContent('A Clinic');
    expect(screen.getByTestId('definitions')).toHaveTextContent('CONSULT · Consultation');
    expect(mocks.personGet).toHaveBeenCalledTimes(1);
    expect(mocks.organizationGet).toHaveBeenCalledTimes(1);
    expect(mocks.listDefinitions).toHaveBeenCalledTimes(1);
    expect(mocks.listCategories).toHaveBeenCalledTimes(1);
  });

  it('isolates cached lookup labels and options when the signed-in actor changes', async () => {
    mocks.permissions = ['member.read', 'organization.read', 'catalog.read'];
    const { rerender, client } = mount();
    await waitFor(() => expect(screen.getByTestId('definitions').textContent).toContain('CONSULT'));
    mocks.personGet.mockResolvedValue({ data: { displayName: 'B Person' } });
    mocks.organizationGet.mockResolvedValue({ data: { displayName: 'B Clinic' } });
    mocks.listDefinitions.mockResolvedValue({
      items: [{ id: 'definition-b', code: 'THERAPY', name: 'Therapy' }],
    });
    mocks.listCategories.mockResolvedValue({
      items: [{ id: 'category-b', code: 'INPATIENT', name: 'Inpatient' }],
    });

    mocks.actorId = 'actor-b';
    rerender(
      <QueryClientProvider client={client}>
        <LookupLabels />
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByTestId('person')).toHaveTextContent('B Person'));
    expect(screen.getByTestId('organization')).toHaveTextContent('B Clinic');
    expect(screen.getByTestId('definitions')).toHaveTextContent('THERAPY · Therapy');
    expect(mocks.personGet).toHaveBeenCalledTimes(2);
    expect(mocks.organizationGet).toHaveBeenCalledTimes(2);
    expect(mocks.listDefinitions).toHaveBeenCalledTimes(2);
    expect(mocks.listCategories).toHaveBeenCalledTimes(2);
  });
});
