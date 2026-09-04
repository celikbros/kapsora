import { createMockServer } from '@kapsora/api-client/mocks/node';
import { SessionProvider } from '@kapsora/auth';
import { initI18n } from '@kapsora/i18n';
import { ToastProvider } from '@kapsora/ui';
import { QueryClientProvider } from '@tanstack/react-query';
import {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRouteWithContext,
  createRoute,
  createRouter,
} from '@tanstack/react-router';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import { App } from '../App';
import { ServicesProvider, createServices, type AppServices } from '../api';
import { RuleSetDetailPage } from './RuleSetDetailPage';
import { RuleSetListPage } from './RuleSetListPage';
import { RuleSetVersionPage } from './RuleSetVersionPage';
import { type RuleSetListSearch } from './routing';

const { api, server } = createMockServer({ organizationsPerTenant: 6 });
const BASE = 'http://mock.test';
const PASSWORD = 'demo parola 2026 kapsora';

beforeAll(() => {
  initI18n('tr');
  server.listen({ onUnhandledRequest: 'error' });
});
afterEach(() => api.reset());
afterAll(() => server.close());

/**
 * The rule routes are registered in `router.tsx` by another engineer, so these tests wire
 * the same three paths into a router of their own. Everything below the router — the
 * services, the session store, the toasts — is exactly what the app provides.
 */
const rootRoute = createRootRouteWithContext<{ services: AppServices }>()({
  component: () => <Outlet />,
});

function ruleSetListSearch(raw: Record<string, unknown>): RuleSetListSearch {
  const out: RuleSetListSearch = {};
  const purpose = raw['purpose'];
  if (
    purpose === 'ELIGIBILITY' ||
    purpose === 'DOCUMENT' ||
    purpose === 'PREAUTH' ||
    purpose === 'LIMIT' ||
    purpose === 'DUPLICATE' ||
    purpose === 'DIAGNOSIS_SERVICE' ||
    purpose === 'PRICE' ||
    purpose === 'ADJUDICATION'
  ) {
    out.purpose = purpose;
  }
  if (typeof raw['q'] === 'string' && raw['q'] !== '') out.q = raw['q'];
  if (typeof raw['cursor'] === 'string' && raw['cursor'] !== '') out.cursor = raw['cursor'];
  return out;
}

const routeTree = rootRoute.addChildren([
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/rule-sets',
    validateSearch: ruleSetListSearch,
    component: RuleSetListPage,
  }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/rule-sets/$ruleSetId',
    component: RuleSetDetailPage,
  }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/rule-set-versions/$ruleSetVersionId',
    component: RuleSetVersionPage,
  }),
]);

const tenantId = () => api.world.tenants[0]!.id;

async function mount(path: string, username = 'admin.a') {
  const services = createServices({ baseUrl: BASE });
  await services.store.login(username, PASSWORD);
  // both.ab belongs to two tenants, so it lands without an active one.
  if (!services.store.getState().activeTenant) {
    await services.store.switchTenant(tenantId());
  }
  const router = createRouter({
    routeTree,
    context: { services },
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(
    <ServicesProvider services={services}>
      <QueryClientProvider client={services.queryClient}>
        <SessionProvider store={services.store}>
          <ToastProvider>
            <RouterProvider router={router} />
          </ToastProvider>
        </SessionProvider>
      </QueryClientProvider>
    </ServicesProvider>,
  );
  return { services, router, user: userEvent.setup() };
}

/** The seeded DOCUMENT rule set of DEMO_A and its one published version. */
function ruleSet() {
  const found = api.world.ruleSets.find((s) => s.tenantId === tenantId());
  if (!found) throw new Error('fixture: no rule set in DEMO_A');
  return found;
}

function publishedVersion() {
  const found = api.world.ruleSetVersions.find(
    (v) => v.tenantId === tenantId() && v.status === 'PUBLISHED',
  );
  if (!found) throw new Error('fixture: no published rule set version');
  return found;
}

/**
 * A second version of the seeded set carrying the same rules and cases. The world has one
 * published version and no draft, and creating one through the screen is its own test, so
 * the tests that need a draft to work on put one there directly.
 */
function seedVersion(status: 'DRAFT' | 'UNDER_REVIEW' = 'DRAFT') {
  const source = publishedVersion();
  const version = {
    ...source,
    id: api.world.nextId(),
    versionNo: source.versionNo + 1,
    status,
    validFrom: '2027-01-01',
    validTo: null,
    contentHash: null,
    publishedAt: null,
    publishedBy: null,
    submittedAt: status === 'UNDER_REVIEW' ? new Date().toISOString() : null,
    submittedBy: status === 'UNDER_REVIEW' ? api.world.accounts[0]!.actorId : null,
    reviewComment: null,
    retireReasonCode: null,
    rules: source.rules.map((rule) => ({ ...rule, id: api.world.nextId() })),
    testCases: source.testCases.map((testCase) => ({ ...testCase, id: api.world.nextId() })),
    rowVersion: 1,
  };
  api.world.ruleSetVersions.push(version);
  return version;
}

describe('rule set list', () => {
  it('lists rule sets, filters by purpose and opens one', async () => {
    const { router, user } = await mount('/rule-sets');
    const table = await screen.findByTestId('rule-set-table');
    expect(within(table).getAllByRole('row').length).toBeGreaterThan(1);

    // A purpose no set answers empties the list; its own purpose finds it again.
    await user.selectOptions(screen.getByLabelText('Amaç'), 'PRICE');
    expect(await screen.findByText('Kural seti bulunamadı.')).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText('Amaç'), 'DOCUMENT');

    const set = ruleSet();
    await user.click(await screen.findByRole('link', { name: set.name }));
    await screen.findByRole('heading', { name: set.name });
    expect(router.state.location.pathname).toBe(`/rule-sets/${set.id}`);
  });

  it('renders no authoring control for an operator who may only read', async () => {
    await mount('/rule-sets', 'reviewer.a');
    await screen.findByTestId('rule-set-table');
    expect(screen.queryByRole('button', { name: 'Yeni kural seti' })).not.toBeInTheDocument();
  });
});

describe('rule set version', () => {
  it('shows a published version read-only, with its content hash and no editor', async () => {
    const version = publishedVersion();
    await mount(`/rule-set-versions/${version.id}`);

    expect(await screen.findByTestId('rule-table')).toBeInTheDocument();
    expect(screen.queryByTestId('rules-form')).not.toBeInTheDocument();
    expect(screen.queryByTestId('test-cases-form')).not.toBeInTheDocument();
    expect(screen.getByText(/Yayınlanmış sürüm değiştirilemez/)).toBeInTheDocument();
    expect(screen.getByText('İçerik özeti')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'İncelemeye gönder' })).not.toBeInTheDocument();
  });

  it('creates a version by copying the published one and carries its rules over', async () => {
    const set = ruleSet();
    const { user } = await mount(`/rule-sets/${set.id}`);
    await screen.findByTestId('rule-version-table');

    await user.click(screen.getByRole('button', { name: 'Yeni sürüm' }));
    const dialog = await screen.findByRole('dialog', { name: 'Yeni kural sürümü' });
    await user.type(within(dialog).getByLabelText(/^Başlangıç/), '2027-01-01');
    await user.selectOptions(
      within(dialog).getByLabelText(/Kopyalanacak sürüm/),
      publishedVersion().id,
    );
    await user.click(within(dialog).getByRole('button', { name: 'Kaydet' }));

    await screen.findByTestId('rules-form');
    expect(screen.getByTestId('rule-row-1')).toBeInTheDocument();
    // The copied cases still pass, so the draft may go straight to review.
    expect(await screen.findByRole('button', { name: 'İncelemeye gönder' })).toBeInTheDocument();
  });
});

describe('the publishing gate', () => {
  it('offers no submit while the version has no test case, and says so', async () => {
    const version = seedVersion();
    version.testCases = [];
    await mount(`/rule-set-versions/${version.id}`);

    expect(await screen.findByText(/en az bir test senaryosu gerekir/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'İncelemeye gönder' })).not.toBeInTheDocument();
  });

  it('withdraws submit and names the case as soon as one stops passing', async () => {
    const version = seedVersion();
    await mount(`/rule-set-versions/${version.id}`);
    const user = userEvent.setup();
    await screen.findByTestId('test-cases-form');
    expect(await screen.findByRole('button', { name: 'İncelemeye gönder' })).toBeInTheDocument();

    const row = screen.getByTestId('test-case-row-0');
    await user.selectOptions(within(row).getByLabelText(/^Beklenen sonuç/), 'REJECTED');
    await user.click(
      within(screen.getByTestId('test-cases-form')).getByRole('button', { name: 'Kaydet' }),
    );

    expect(
      await screen.findByText(/Şu senaryolar geçmiyor: SHORT_PHYSIO_APPROVED/),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'İncelemeye gönder' })).not.toBeInTheDocument(),
    );
  });

  it('submits a passing draft and then refuses to let the submitter publish', async () => {
    const version = seedVersion();
    const { user } = await mount(`/rule-set-versions/${version.id}`);

    await user.click(await screen.findByRole('button', { name: 'İncelemeye gönder' }));
    const confirm = await screen.findByRole('dialog', { name: 'İncelemeye gönder' });
    await user.click(within(confirm).getByRole('button', { name: 'İncelemeye gönder' }));

    await screen.findByText('İncelemede');
    expect(await screen.findByText(/gönderen kişi olamaz/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Yayınla' })).not.toBeInTheDocument();
  });

  it('lets a second person publish after a password re-entry', async () => {
    // The seeded v1 is published open-ended, so close it or the new period overlaps it.
    publishedVersion().validTo = '2026-12-31';
    const version = seedVersion('UNDER_REVIEW');
    const { user } = await mount(`/rule-set-versions/${version.id}`, 'both.ab');

    await user.click(await screen.findByRole('button', { name: 'Yayınla' }));
    const confirm = await screen.findByRole('dialog', { name: 'Yayınla' });
    await user.click(within(confirm).getByRole('button', { name: 'Yayınla' }));

    const stepUp = await screen.findByRole('dialog', { name: 'Parolanızı doğrulayın' });
    await user.type(within(stepUp).getByLabelText(/^Parola/), PASSWORD);
    await user.click(within(stepUp).getByRole('button', { name: 'Doğrula' }));

    await waitFor(() =>
      expect(api.world.ruleSetVersions.find((v) => v.id === version.id)?.status).toBe('PUBLISHED'),
    );
    expect(await screen.findByText('İçerik özeti')).toBeInTheDocument();
    expect(screen.getByText(/Yayınlanmış sürüm değiştirilemez/)).toBeInTheDocument();
  });
});

describe('the rules editor', () => {
  it('renders a compile error on the rule that caused it', async () => {
    const version = seedVersion();
    const { user } = await mount(`/rule-set-versions/${version.id}`);
    const form = await screen.findByTestId('rules-form');
    const row = screen.getByTestId('rule-row-0');

    fireEvent.change(within(row).getByLabelText(/^Koşul/), {
      target: { value: 'serviceCode ~~ PHYSIO' },
    });
    await user.click(within(form).getByRole('button', { name: 'Kaydet' }));

    // The compiler's own words, on that rule's field rather than in a page-level alert.
    expect(await within(row).findByText(/desteklenmeyen koşul parçası/)).toBeInTheDocument();
  });

  it('catches two rules sharing a priority before the round trip', async () => {
    const version = seedVersion();
    const { user } = await mount(`/rule-set-versions/${version.id}`);
    const form = await screen.findByTestId('rules-form');
    const second = screen.getByTestId('rule-row-1');

    fireEvent.change(within(second).getByLabelText(/^Sıra/), { target: { value: '10' } });
    await user.click(within(form).getByRole('button', { name: 'Kaydet' }));

    expect(
      await within(second).findByText('Aynı değer birden çok kez verilmiş'),
    ).toBeInTheDocument();
    // Nothing was sent: the stored rule keeps the priority it had.
    expect(api.world.ruleSetVersions.find((v) => v.id === version.id)?.rules[1]?.priority).toBe(20);
  });
});

describe('simulation', () => {
  it('runs one input, shows the fired rules in order and records nothing', async () => {
    const version = publishedVersion();
    const before = api.world.ruleEvaluations.length;
    const { user } = await mount(`/rule-set-versions/${version.id}`);

    const panel = await screen.findByTestId('simulation-panel');
    expect(within(panel).getByText(/Hiçbir kayıt yazılmaz/)).toBeInTheDocument();
    fireEvent.change(within(panel).getByLabelText(/^Girdi/), {
      target: {
        value: '{"serviceCode":"PHYSIO_SESSION","quantity":"10","requestedAmount":"7500.000000"}',
      },
    });
    await user.click(within(panel).getByRole('button', { name: 'Çalıştır' }));

    const trace = await screen.findByTestId('simulation-trace');
    const rows = within(trace).getAllByRole('row').slice(1);
    expect(rows).toHaveLength(2);
    expect(rows[0]?.textContent).toContain('PHYSIO_REPORT_REQUIRED');
    expect(rows[1]?.textContent).toContain('HIGH_AMOUNT_REVIEW');
    // A simulation is side-effect free: no evaluation row was appended.
    expect(api.world.ruleEvaluations).toHaveLength(before);
  });

  it('keeps nothing in browser storage', async () => {
    await mount('/rule-sets');
    await screen.findByTestId('rule-set-table');
    expect(Object.keys(localStorage)).toHaveLength(0);
    expect(Object.keys(sessionStorage)).toHaveLength(0);
  });
});

// The tests above mount a router of their own so each screen can be exercised in
// isolation. This one mounts the real application, which is the only way to catch a rule
// page that works perfectly but was never wired into router.tsx or the menu.
describe('rule screens are reachable in the real application', () => {
  it('opens the rule sets from the main menu', async () => {
    const services = createServices({ baseUrl: BASE });
    const history = createMemoryHistory({ initialEntries: ['/'] });
    render(<App services={services} history={history} />);

    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/Kullanıcı adı/), 'admin.a');
    await user.type(screen.getByLabelText(/^Parola/), PASSWORD);
    await user.click(screen.getByRole('button', { name: 'Giriş yap' }));

    await user.click(
      await within(await screen.findByRole('navigation', { name: 'Ana menü' })).findByRole('link', {
        name: 'Kurallar',
      }),
    );
    expect(history.location.pathname).toBe('/rule-sets');
    await screen.findByRole('heading', { name: 'Kurallar' });
  });
});
